package poll

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/mafri27/snmpscan/internal/config"
)

// TestTuning measures how batch size and session count affect a full poll.
// Same gating as TestAgainstLiveAgent.
func TestTuning(t *testing.T) {
	addr := os.Getenv("SNMPSCAN_TEST_AGENT")
	if addr == "" {
		t.Skip("set SNMPSCAN_TEST_AGENT=host:port to run")
	}
	community := os.Getenv("SNMPSCAN_TEST_COMMUNITY")
	if community == "" {
		community = "public"
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	set, err := config.Load([]string{"../../.snmpscan"})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{
		Host: host, Port: uint16(port), Community: community,
		Timeout: 3 * time.Second, Retries: 1,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, tc := range []struct {
		sessions, batch int
	}{
		{4, 60}, {8, 60}, {8, 20}, {16, 20}, {16, 10},
	} {
		p, err := New(ctx, Options{Target: target, Config: set, Sessions: tc.sessions})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		p.batchSize = tc.batch

		if err := p.Discover(ctx); err != nil {
			t.Errorf("sessions=%d batch=%d: discover: %v", tc.sessions, tc.batch, err)
			p.Close()
			continue
		}
		if _, err := p.Poll(ctx, nil); err != nil {
			t.Errorf("sessions=%d batch=%d: %v", tc.sessions, tc.batch, err)
			p.Close()
			continue
		}
		snap, err := p.Poll(ctx, nil)
		p.Close()
		if err != nil {
			t.Errorf("sessions=%d batch=%d: %v", tc.sessions, tc.batch, err)
			continue
		}
		t.Logf("sessions=%-3d batch=%-3d  %6v  %3d requests  %d interfaces",
			tc.sessions, tc.batch, snap.Elapsed.Round(time.Millisecond), snap.Requests, len(snap.Rows))
	}
}

// TestFilterPayoff shows what the two-stage fetch buys: with a filter only the
// visible interfaces cost a counter round trip.
func TestFilterPayoff(t *testing.T) {
	addr := os.Getenv("SNMPSCAN_TEST_AGENT")
	if addr == "" {
		t.Skip("set SNMPSCAN_TEST_AGENT=host:port to run")
	}
	community := os.Getenv("SNMPSCAN_TEST_COMMUNITY")
	if community == "" {
		community = "public"
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)

	set, err := config.Load([]string{"../../.snmpscan"})
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Host: host, Port: uint16(port), Community: community, Timeout: 3 * time.Second, Retries: 1}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	for _, tc := range []struct {
		name    string
		filters []string
		maxRep  uint32
	}{
		{"no filter, maxrep 10", nil, 10},
		{"no filter, maxrep 25", nil, 25},
		{"physical ports only, maxrep 10", []string{`^[xe]e-\d`}, 10},
		{"physical ports only, maxrep 25", []string{`^[xe]e-\d`}, 25},
	} {
		p, err := New(ctx, Options{Target: target, Config: set, Filters: tc.filters})
		if err != nil {
			t.Fatalf("connect: %v", err)
		}
		for _, c := range append(append([]*gosnmp.GoSNMP{}, p.pool.all...), p.discoveryPool.all...) {
			c.MaxRepetitions = tc.maxRep
		}
		if err := p.Discover(ctx); err != nil {
			t.Errorf("%s: discover: %v", tc.name, err)
			p.Close()
			continue
		}
		if _, err := p.Poll(ctx, nil); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			p.Close()
			continue
		}
		snap, err := p.Poll(ctx, nil)
		p.Close()
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		t.Logf("%-32s %7v  %3d requests  %3d interfaces",
			tc.name, snap.Elapsed.Round(time.Millisecond), snap.Requests, len(snap.Rows))
	}
}

// TestBulkWalkColumn times a single GETBULK walk of one column, the
// alternative to fetching each interface's counters individually.
func TestBulkWalkColumn(t *testing.T) {
	addr := os.Getenv("SNMPSCAN_TEST_AGENT")
	if addr == "" {
		t.Skip("set SNMPSCAN_TEST_AGENT=host:port to run")
	}
	community := os.Getenv("SNMPSCAN_TEST_COMMUNITY")
	if community == "" {
		community = "public"
	}
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	target := Target{Host: host, Port: uint16(port), Community: community, Timeout: 3 * time.Second, Retries: 1}

	for _, reps := range []uint32{10, 25, 50, 100} {
		c, err := target.dial(nil)
		if err != nil {
			t.Fatal(err)
		}
		c.MaxRepetitions = reps
		start := time.Now()
		pdus, err := c.BulkWalkAll(oidIfHCInOctets)
		took := time.Since(start)
		c.Close()
		if err != nil {
			t.Logf("maxrep=%-4d failed: %v", reps, err)
			continue
		}
		t.Logf("maxrep=%-4d %6v  %d values", reps, took.Round(time.Millisecond), len(pdus))
	}

	// And all columns at once, in parallel.
	cols := []string{oidIfHCInOctets, oidIfHCOutOctets, oidIfInUcastPkts, oidIfOutUcastPkt, oidIfInErrors}
	pl, err := newPool(target, len(cols), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pl.Close()

	ctx := context.Background()
	start := time.Now()
	tasks := make([]func() error, len(cols))
	for i, col := range cols {
		tasks[i] = func() error {
			return pl.with(ctx, func(c *gosnmp.GoSNMP) error {
				c.MaxRepetitions = 50
				_, err := c.BulkWalkAll(col)
				return err
			})
		}
	}
	if err := parallel(tasks); err != nil {
		t.Errorf("parallel walk: %v", err)
	}
	t.Logf("%d columns walked in parallel: %v", len(cols), time.Since(start).Round(time.Millisecond))
}
