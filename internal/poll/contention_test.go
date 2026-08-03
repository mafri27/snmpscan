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

// TestDiscoveryContention measures whether a discovery running in the
// background slows down the value polls. It reports rather than asserts:
// against a live agent the run to run spread is ±15% or so, well above the
// effect being looked for, so a single figure here proves nothing. Run it a
// few times. Same gating as TestAgainstLiveAgent.
func TestDiscoveryContention(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	p, err := New(ctx, Options{
		Target: Target{Host: host, Port: uint16(port), Community: community,
			Timeout: 3 * time.Second, Retries: 1},
		Config: set,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	if err := p.Discover(ctx); err != nil {
		t.Fatalf("discover: %v", err)
	}

	const rounds = 6
	quiet := timePolls(ctx, t, p, rounds, nil)

	// Now the same polls with a discovery hogging two sessions throughout.
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = p.Discover(ctx)
			}
		}
	}()
	busy := timePolls(ctx, t, p, rounds, nil)
	close(stop)
	wg.Wait()

	t.Logf("poll while idle:       %v", quiet.Round(time.Millisecond))
	t.Logf("poll during discovery: %v  (%+.0f%%)",
		busy.Round(time.Millisecond), 100*(busy.Seconds()/quiet.Seconds()-1))
}

// TestPollsDuringFirstDiscovery is the guarantee for slow devices: values
// must appear for the interfaces found so far, without waiting for the walk
// to finish. Point it at a laggy agent (see the snmplag proxy) to see the
// list grow between polls.
func TestPollsDuringFirstDiscovery(t *testing.T) {
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	p, err := New(ctx, Options{
		Target: Target{Host: host, Port: uint16(port), Community: community,
			Timeout: 5 * time.Second, Retries: 1},
		Config: set,
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	done := make(chan error, 1)
	go func() { done <- p.Discover(ctx) }()

	// Poll while that first discovery is still walking.
	var counts []int
	for range 8 {
		snap, err := p.Poll(ctx, nil)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		counts = append(counts, len(snap.Rows))
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			t.Logf("rows per poll while discovering: %v (discovery finished)", counts)
			goto finished
		case <-time.After(300 * time.Millisecond):
		}
	}
	t.Logf("rows per poll while discovering: %v (still going)", counts)

finished:
	if counts[len(counts)-1] == 0 {
		t.Error("no interfaces were pollable during discovery")
	}
	for i := 1; i < len(counts); i++ {
		if counts[i] < counts[i-1] {
			t.Errorf("row count went backwards: %v", counts)
			break
		}
	}
}

func timePolls(ctx context.Context, t *testing.T, p *Poller, rounds int, onUpdate func(*Snapshot)) time.Duration {
	t.Helper()
	var total time.Duration
	for range rounds {
		start := time.Now()
		if _, err := p.Poll(ctx, onUpdate); err != nil {
			t.Fatalf("poll: %v", err)
		}
		total += time.Since(start)
	}
	return total / time.Duration(rounds)
}
