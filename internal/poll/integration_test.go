package poll

import (
	"context"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/mafri27/snmpscan/internal/config"
)

// TestAgainstLiveAgent polls a real agent. Point it at anything that speaks
// SNMPv2c, e.g. a local net-snmp:
//
//	SNMPSCAN_TEST_AGENT=127.0.0.1:11161 go test ./internal/poll -run Live -v
func TestAgainstLiveAgent(t *testing.T) {
	addr := os.Getenv("SNMPSCAN_TEST_AGENT")
	if addr == "" {
		t.Skip("set SNMPSCAN_TEST_AGENT=host:port to run")
	}
	community := os.Getenv("SNMPSCAN_TEST_COMMUNITY")
	if community == "" {
		community = "public"
	}

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SNMPSCAN_TEST_AGENT: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("SNMPSCAN_TEST_AGENT: %v", err)
	}

	set, err := config.Load([]string{"../../.snmpscan"})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	p, err := New(ctx, Options{
		Target: Target{
			Host:      host,
			Port:      uint16(port),
			Community: community,
			Timeout:   2 * time.Second,
			Retries:   1,
		},
		Config: set,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	t.Logf("sysDescr: %s", p.SysDescr())
	t.Logf("profile:  cpu=%s matched=%v", p.Profile().CPUOID, p.Profile().Matched)

	// The first poll pays for discovery, later ones reuse the interface list.
	// That difference is the whole point of splitting the two.
	discovery := time.Now()
	if err := p.Discover(ctx); err != nil {
		t.Fatalf("discover: %v", err)
	}
	t.Logf("discovery: %d interfaces in %v",
		p.Interfaces(), time.Since(discovery).Round(time.Millisecond))

	first, err := p.Poll(ctx, nil)
	if err != nil {
		t.Fatalf("first poll: %v", err)
	}
	if len(first.Rows) == 0 {
		t.Fatal("no interfaces returned")
	}
	t.Logf("first poll: %d interfaces in %v using %d requests",
		len(first.Rows), first.Elapsed.Round(time.Millisecond), first.Requests)

	// Rates need two readings, so the interesting assertions are on the second.
	time.Sleep(2 * time.Second)
	second, err := p.Poll(ctx, nil)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if second.SysName == "" {
		t.Error("sysName is empty")
	}

	var withRates int
	for _, r := range second.Rows {
		if r.InOctets != nil {
			withRates++
		}
		if r.Name == "" {
			t.Errorf("interface %d has no name", r.Index)
		}
	}
	if withRates == 0 {
		t.Error("no interface produced a rate on the second poll")
	}
	t.Logf("second poll: %d/%d interfaces with rates in %v using %d requests",
		withRates, len(second.Rows), second.Elapsed.Round(time.Millisecond), second.Requests)

	for i, r := range second.Rows {
		if i >= 5 {
			break
		}
		t.Logf("  %-6d %-16s in=%-12v out=%-12v pps=%v/%v errs=%v",
			r.Index, r.Name, deref(r.InOctets), deref(r.OutOctets),
			deref(r.InPkts), deref(r.OutPkts), deref(r.InErrors))
	}
}

// TestPollStreamsPartialResults is the guarantee that matters on a loaded
// agent: the table must not stay blank until the last batch lands.
func TestPollStreamsPartialResults(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	p, err := New(ctx, Options{
		Target: Target{Host: host, Port: uint16(port), Community: community, Timeout: 3 * time.Second, Retries: 1},
		Config: set,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	if err := p.Discover(ctx); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if _, err := p.Poll(ctx, nil); err != nil {
		t.Fatalf("warmup poll: %v", err)
	}

	type update struct {
		rows, valued, fresh int
		complete            bool
	}
	var (
		mu      sync.Mutex
		updates []update
	)

	final, err := p.Poll(ctx, func(s *Snapshot) {
		u := update{rows: len(s.Rows), complete: s.Complete}
		for _, r := range s.Rows {
			if r.InOctets != nil {
				u.valued++
			}
			if r.Fresh {
				u.fresh++
			}
		}
		mu.Lock()
		updates = append(updates, u)
		mu.Unlock()
	})
	if err != nil {
		t.Fatalf("poll: %v", err)
	}

	if len(updates) < 2 {
		t.Fatalf("got %d updates, want the interface list plus at least one batch", len(updates))
	}
	total := len(final.Rows)
	t.Logf("%d updates for %d interfaces", len(updates), total)
	for i, u := range updates {
		if i < 3 || i == len(updates)-1 {
			t.Logf("  update %2d: %3d rows, %3d with values, %3d fresh", i, u.rows, u.valued, u.fresh)
		}
	}

	// The very first update carries every interface, so the full port list is
	// on screen before a single counter has come back.
	if updates[0].rows != total {
		t.Errorf("first update has %d rows, want all %d", updates[0].rows, total)
	}
	if updates[0].fresh != 0 {
		t.Errorf("first update marks %d rows fresh, expected none yet", updates[0].fresh)
	}

	// This is what stops the table flickering: a row waiting for its counters
	// keeps the previous poll's numbers, so the count of populated rows never
	// drops back towards zero.
	if updates[0].valued != total {
		t.Errorf("first update shows %d/%d values, want every row to keep its previous reading",
			updates[0].valued, total)
	}
	for i, u := range updates {
		if u.valued != total {
			t.Errorf("update %d dropped to %d/%d populated rows", i, u.valued, total)
		}
	}

	// Freshness has to spread gradually and cover everything by the end.
	for i := 1; i < len(updates); i++ {
		if updates[i].fresh < updates[i-1].fresh {
			t.Errorf("update %d went back from %d to %d fresh rows",
				i, updates[i-1].fresh, updates[i].fresh)
		}
	}
	if last := updates[len(updates)-1]; last.fresh != total {
		t.Errorf("last update has %d/%d fresh rows", last.fresh, total)
	}

	for i, u := range updates {
		if u.complete {
			t.Errorf("update %d claims the poll is complete; only the return value may", i)
		}
	}
	if !final.Complete {
		t.Error("the returned snapshot must be marked complete")
	}
}
