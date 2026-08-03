package poll

import (
	"reflect"
	"regexp"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/mafri27/snmpscan/internal/config"
)

// newDiscovery mirrors what Discover sets up, so the streaming behaviour can
// be exercised without an agent.
func newDiscovery(p *Poller) *discovery {
	return &discovery{poller: p, names: map[int]string{}, aliases: map[int]string{}, kept: map[int]bool{}}
}

func TestDiscoveryFiltersAndOrders(t *testing.T) {
	p := &Poller{filters: mustCompile(t, `^[gx]e-0/[012]/`, `^et-`)}
	d := newDiscovery(p)

	// Deliberately out of order, as two parallel walks would deliver them.
	d.name(5, "xe-0/1/1")
	d.name(1, "ge-0/0/0")
	d.name(2, "ge-0/3/0") // slot 3 is outside the pattern
	d.name(4, "lo0")
	d.name(3, "et-0/1/0")
	d.alias(2, "uplink")
	d.alias(4, "loopback")

	if want := []int{1, 3, 5}; !reflect.DeepEqual(indexesOf(p), want) {
		t.Errorf("selected %v, want %v — and sorted by ifIndex", indexesOf(p), want)
	}
	if got := p.currentInterfaces()[0].name; got != "ge-0/0/0" {
		t.Errorf("name = %q", got)
	}
}

// The point of the rewrite: an interface has to be pollable the moment it is
// found, not when the walk finishes.
func TestDiscoveryPublishesWhileWalking(t *testing.T) {
	p := &Poller{filters: mustCompile(t, "")}
	d := newDiscovery(p)

	d.name(1, "xe-0/0/0")

	if got := p.Interfaces(); got != 1 {
		t.Fatalf("%d interfaces after the first value, want 1", got)
	}
	if got := p.currentInterfaces()[0].name; got != "xe-0/0/0" {
		t.Errorf("name = %q, want the interface usable right away", got)
	}
}

// The alias walk runs beside the name walk and may arrive either way round.
func TestDiscoveryFillsTheAliasInLater(t *testing.T) {
	p := &Poller{filters: mustCompile(t, "")}
	d := newDiscovery(p)

	d.alias(7, "customer uplink")
	if p.Interfaces() != 0 {
		t.Error("an alias alone must not create a nameless row")
	}

	d.name(7, "xe-0/0/7")
	if p.Interfaces() != 1 {
		t.Fatal("the interface should appear once it has a name")
	}

	d.alias(7, "customer uplink")
	got := p.currentInterfaces()[0]
	if got.alias != "customer uplink" {
		t.Errorf("alias = %q, want it filled in", got.alias)
	}
	if p.Interfaces() != 1 {
		t.Errorf("%d interfaces, want the row updated rather than duplicated", p.Interfaces())
	}
}

// An interface only the alias matches must still show up, once its name is in.
func TestDiscoveryMatchesOnAliasToo(t *testing.T) {
	p := &Poller{filters: mustCompile(t, "customer")}
	d := newDiscovery(p)

	d.name(1, "xe-0/0/1")
	if p.Interfaces() != 0 {
		t.Error("the name does not match, nothing should be listed yet")
	}

	d.alias(1, "customer uplink")
	if p.Interfaces() != 1 {
		t.Error("the alias matches, so it belongs on the list")
	}
}

func TestRetainDropsWhatTheWalkDidNotSee(t *testing.T) {
	p := &Poller{ifaces: []iface{{index: 1}, {index: 2}, {index: 3}}}

	p.retain(map[int]bool{1: true, 3: true})

	if want := []int{1, 3}; !reflect.DeepEqual(indexesOf(p), want) {
		t.Errorf("kept %v, want %v", indexesOf(p), want)
	}
}

func TestAddedFiresOnlyUntilTheFirstWalkIsThrough(t *testing.T) {
	p := &Poller{added: make(chan struct{}, 1)}

	p.upsert(iface{index: 1, name: "xe-0/0/0"})
	if !signalled(p) {
		t.Fatal("no wake-up for the first interface found")
	}

	// Hundreds of ports arriving in one walk must not queue hundreds of polls.
	p.upsert(iface{index: 2, name: "xe-0/0/1"})
	p.upsert(iface{index: 3, name: "xe-0/0/2"})
	if !signalled(p) {
		t.Fatal("no wake-up while the walk was still finding ports")
	}
	if signalled(p) {
		t.Error("the wake-ups queued up instead of coalescing")
	}

	// An interface merely being refreshed is not news.
	p.upsert(iface{index: 1, name: "xe-0/0/0", alias: "uplink"})
	if signalled(p) {
		t.Error("an update of a known interface woke the poll loop")
	}

	// Once a walk has completed the list is known; ports coming and going are
	// then the normal interval's business.
	p.retain(map[int]bool{1: true, 2: true, 3: true})
	p.upsert(iface{index: 4, name: "xe-0/0/3"})
	if signalled(p) {
		t.Error("a port found after the first full walk still cut the interval short")
	}
}

// The wake-up must not survive the poll it asked for. Interfaces published
// while the loop is on its way to the agent are still in the list it reads, so
// a signal left over afterwards polls the same set a second time.
func TestPollingClearsTheWakeUpForWhatItReads(t *testing.T) {
	p := &Poller{added: make(chan struct{}, 1)}

	p.upsert(iface{index: 1, name: "xe-0/0/0"})
	// Between the wake-up and the poll another one arrives; it makes the same
	// list and is fetched along with the first.
	p.upsert(iface{index: 2, name: "xe-0/0/1"})

	if got := len(p.takeInterfaces()); got != 2 {
		t.Fatalf("polling %d interfaces, want both", got)
	}
	if signalled(p) {
		t.Error("still asking for a poll of interfaces the poll just read")
	}

	// One that turns up afterwards did miss the poll, so it must wake it.
	p.upsert(iface{index: 3, name: "xe-0/0/2"})
	if !signalled(p) {
		t.Error("an interface found after the list was read got no poll")
	}
}

func signalled(p *Poller) bool {
	select {
	case <-p.Added():
		return true
	default:
		return false
	}
}

func indexesOf(p *Poller) []int {
	var out []int
	for _, i := range p.currentInterfaces() {
		out = append(out, i.index)
	}
	return out
}

func TestMatchesAlias(t *testing.T) {
	p := &Poller{filters: mustCompile(t, "customer")}
	if !p.matches("ge-0/0/1", "customer uplink") {
		t.Error("a filter must also match the alias")
	}
	if p.matches("ge-0/0/1", "internal") {
		t.Error("unexpected match")
	}
}

func TestEmptyFilterMatchesEverything(t *testing.T) {
	p := &Poller{filters: mustCompile(t, "")}
	if !p.matches("anything", "") {
		t.Error("the empty default filter must match every interface")
	}
}

func TestBuildRowsUsesVendorRates(t *testing.T) {
	p := &Poller{
		profile:   config.Profile{SecValueFactor: 8},
		markIndex: 2,
		prev: map[int]counters{
			1: {inOct: val(1000), outOct: val(2000), inPkt: val(10), outPkt: val(20), errIn: val(0)},
		},
	}
	st := &state{
		ifaces: []iface{{index: 1, name: "ge-0/0/0"}, {index: 2, name: "ge-0/0/1"}},
		raw: []counters{
			{
				inOct: val(11000), outOct: val(2000), inPkt: val(110), outPkt: val(20), errIn: val(5),
				// The agent's own bits-per-second reading must win over our delta.
				secInOct: val(8000),
			},
			{inOct: val(500)},
		},
		secs: 10,
		done: []bool{true, true},
	}

	rows := p.buildRows(st)

	if got := *rows[0].InOctets; got != 1000 {
		t.Errorf("in = %d, want the vendor value 8000/8", got)
	}
	if got := *rows[0].OutOctets; got != 0 {
		t.Errorf("out = %d, want 0 from an unchanged counter", got)
	}
	if got := *rows[0].InPkts; got != 10 {
		t.Errorf("in pps = %d, want 10", got)
	}
	if got := *rows[0].InErrors; got != 0 {
		t.Errorf("errors = %d, want 0 (5 errors over 10s truncates)", got)
	}
	if rows[0].Marked {
		t.Error("interface 1 must not be marked")
	}

	// Interface 2 was never seen before, so it has no rates yet.
	if rows[1].InOctets != nil || rows[1].InPkts != nil {
		t.Error("a new interface must not report rates on its first poll")
	}
	if !rows[1].Marked {
		t.Error("interface 2 carries the marked IP")
	}
}

// A row whose counters have not come back yet must keep showing the previous
// poll's numbers. Blanking it makes the whole table flicker on a slow agent.
func TestBuildRowsKeepsPreviousValuesWhilePending(t *testing.T) {
	previous := int64(4242)
	p := &Poller{
		profile:   config.Profile{SecValueFactor: 1},
		markIndex: -1,
		lastRows: map[int]Row{
			1: {Index: 1, InOctets: &previous, InPkts: &previous},
		},
	}
	st := &state{
		ifaces: []iface{{index: 1, name: "xe-0/0/0"}, {index: 2, name: "xe-0/0/1"}},
		// Interface 2 answered with real counters, it just has no history yet.
		raw:  []counters{{}, {inOct: val(7), outOct: val(7), inPkt: val(1), outPkt: val(1), errIn: val(0)}},
		secs: 10,
		done: []bool{false, true}, // interface 1 still waiting
	}

	rows := p.buildRows(st)

	if rows[0].InOctets == nil || *rows[0].InOctets != previous {
		t.Errorf("pending row = %v, want the previous %d", deref(rows[0].InOctets), previous)
	}
	if rows[0].Fresh {
		t.Error("a pending row must not be marked fresh")
	}
	// Interface 2 answered but has no history, so it legitimately has no rate.
	if !rows[1].Fresh {
		t.Error("a row whose varbinds all arrived is fresh")
	}
	if rows[1].InOctets != nil {
		t.Errorf("unseen interface = %v, want no value", deref(rows[1].InOctets))
	}
}

// Batches must never split an interface, otherwise one response would leave a
// row half updated and it could not be shown until the next one arrived.
func TestBatchInterfacesKeepsInterfacesWhole(t *testing.T) {
	const cols, maxOids = 9, 60

	groups := batchInterfaces(172, cols, maxOids)

	if len(groups) == 0 {
		t.Fatal("no batches")
	}
	covered := 0
	for i, g := range groups {
		n := g.end - g.start
		if n*cols > maxOids {
			t.Errorf("batch %d asks for %d varbinds, over the %d limit", i, n*cols, maxOids)
		}
		if g.start != covered {
			t.Errorf("batch %d starts at %d, want %d — a gap or an overlap", i, g.start, covered)
		}
		covered = g.end
	}
	if covered != 172 {
		t.Errorf("batches cover %d interfaces, want 172", covered)
	}
	// 60/9 is 6 interfaces per request, so 172 needs 29 of them.
	if len(groups) != 29 {
		t.Errorf("got %d batches, want 29", len(groups))
	}
}

func TestBatchInterfacesEdgeCases(t *testing.T) {
	if got := batchInterfaces(0, 9, 60); got != nil {
		t.Errorf("no interfaces should mean no batches, got %v", got)
	}
	// More columns than fit in one PDU: still one interface per request rather
	// than an empty batch that would loop forever.
	groups := batchInterfaces(3, 80, 60)
	if len(groups) != 3 {
		t.Fatalf("got %d batches, want one per interface", len(groups))
	}
	for i, g := range groups {
		if g.end-g.start != 1 {
			t.Errorf("batch %d holds %d interfaces, want 1", i, g.end-g.start)
		}
	}
}

// Only interfaces that fully answered may seed the next delta, or the rate
// after a partial poll is computed against the wrong baseline.
func TestRememberSkipsPendingInterfaces(t *testing.T) {
	p := &Poller{}
	st := &state{
		ifaces: []iface{{index: 1}, {index: 2}},
		raw:    []counters{{inOct: val(10)}, {inOct: val(20)}},
		done:   []bool{false, true},
	}

	p.remember(st, []Row{{Index: 1}, {Index: 2}})

	if _, ok := p.prev[1]; ok {
		t.Error("a pending interface must not become the next baseline")
	}
	if _, ok := p.prev[2]; !ok {
		t.Error("a complete interface must become the next baseline")
	}
	if len(p.lastRows) != 2 {
		t.Errorf("lastRows has %d entries, want both rows for the next poll", len(p.lastRows))
	}
}

// A port pulled since the last discovery answers noSuchInstance on every
// counter. It must disappear rather than freeze at its last values.
func TestVanishedInterfaceIsDroppedFromTheTable(t *testing.T) {
	previous := int64(500)
	p := &Poller{
		profile:   config.Profile{SecValueFactor: 1},
		markIndex: -1,
		lastRows:  map[int]Row{2: {Index: 2, InOctets: &previous}},
	}
	st := &state{
		ifaces: []iface{{index: 1, name: "xe-0/0/0"}, {index: 2, name: "xe-0/0/1"}},
		raw: []counters{
			{inOct: val(10), outOct: val(10), inPkt: val(1), outPkt: val(1), errIn: val(0)},
			{}, // every varbind came back as noSuchInstance
		},
		secs: 10,
		done: []bool{true, true},
	}

	rows := p.buildRows(st)

	if len(rows) != 1 || rows[0].Index != 1 {
		t.Fatalf("got %d rows starting at %d, want only interface 1", len(rows), rows[0].Index)
	}
	if gone := vanished(st); !gone[2] || gone[1] {
		t.Errorf("vanished = %v, want only interface 2", gone)
	}
}

// A timeout looks similar but must not remove anything: the port is probably
// still there, the agent just did not answer in time.
func TestTimeoutIsNotMistakenForARemovedInterface(t *testing.T) {
	previous := int64(500)
	p := &Poller{
		profile:   config.Profile{SecValueFactor: 1},
		markIndex: -1,
		lastRows:  map[int]Row{1: {Index: 1, InOctets: &previous}},
	}
	st := &state{
		ifaces: []iface{{index: 1, name: "xe-0/0/0"}},
		raw:    []counters{{}},
		secs:   10,
		done:   []bool{false}, // no response at all
	}

	rows := p.buildRows(st)

	if len(rows) != 1 {
		t.Fatalf("got %d rows, want the interface kept", len(rows))
	}
	if rows[0].InOctets == nil || *rows[0].InOctets != previous {
		t.Errorf("got %v, want the previous value kept", deref(rows[0].InOctets))
	}
	if gone := vanished(st); len(gone) != 0 {
		t.Errorf("vanished = %v, want nothing dropped on a timeout", gone)
	}
}

func TestForgetRemovesFromTheInterfaceList(t *testing.T) {
	p := &Poller{ifaces: []iface{{index: 1}, {index: 2}, {index: 3}}}

	p.forget(map[int]bool{2: true})

	if got := p.Interfaces(); got != 2 {
		t.Errorf("%d interfaces left, want 2", got)
	}
	for _, i := range p.currentInterfaces() {
		if i.index == 2 {
			t.Error("interface 2 is still on the list")
		}
	}
}

func TestIntervalPrefersAgentClock(t *testing.T) {
	p := &Poller{prevUptime: val(1000), lastPoll: time.Now().Add(-time.Minute), prev: map[int]counters{1: {}}}

	// 1500 - 1000 TimeTicks is 5 seconds, not the wall clock's 60.
	if got := p.interval(val(1500), time.Now()); got != 5 {
		t.Errorf("interval = %v, want 5", got)
	}
}

func TestIntervalDetectsReboot(t *testing.T) {
	p := &Poller{prevUptime: val(50000), prev: map[int]counters{1: {inOct: val(9)}}}

	if got := p.interval(val(10), time.Now()); got != 0 {
		t.Errorf("interval = %v, want 0 after a reboot", got)
	}
	if len(p.prev) != 0 {
		t.Error("counters from before the reboot must be dropped")
	}
}

func TestIntervalFallsBackToWallClock(t *testing.T) {
	p := &Poller{lastPoll: time.Now().Add(-8 * time.Second)}

	got := p.interval(value{}, time.Now())
	if got < 7.5 || got > 8.5 {
		t.Errorf("interval = %v, want roughly 8", got)
	}
}

func TestOIDIndex(t *testing.T) {
	base := "1.3.6.1.2.1.31.1.1.1.1"
	if idx, ok := oidIndex(base, "."+base+".517"); !ok || idx != 517 {
		t.Errorf("got (%d, %v), want (517, true)", idx, ok)
	}
	if _, ok := oidIndex(base, ".1.3.6.1.2.1.2.2.1.14.5"); ok {
		t.Error("an OID from a different column must not yield an index")
	}
	if _, ok := oidIndex(base, "."+base+".1.2"); ok {
		t.Error("a multi-part suffix is not a plain ifIndex")
	}
}

func TestChunk(t *testing.T) {
	oids := []string{"a", "b", "c", "d", "e"}
	got := chunk(oids, 2)
	want := [][]string{{"a", "b"}, {"c", "d"}, {"e"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("chunk = %v, want %v", got, want)
	}
	if n := len(chunk(nil, 60)); n != 0 {
		t.Errorf("chunk of nothing produced %d batches", n)
	}
}

func TestPduStringKeepsExceptionMarkers(t *testing.T) {
	// riverstone.device tests for this exact spelling.
	if got := pduString(gosnmp.SnmpPDU{Type: gosnmp.NoSuchObject}); got != "noSuchObject" {
		t.Errorf("got %q, want noSuchObject", got)
	}
	if got := pduString(gosnmp.SnmpPDU{Type: gosnmp.OctetString, Value: []byte("ge-0/0/0")}); got != "ge-0/0/0" {
		t.Errorf("got %q", got)
	}
	if got := pduString(gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: 42}); got != "42" {
		t.Errorf("got %q", got)
	}
}

func TestPduValue(t *testing.T) {
	if v := pduValue(gosnmp.SnmpPDU{Type: gosnmp.Counter64, Value: uint64(1234)}); !v.ok || v.n != 1234 {
		t.Errorf("got %+v, want 1234", v)
	}
	if v := pduValue(gosnmp.SnmpPDU{Type: gosnmp.NoSuchInstance}); v.ok {
		t.Error("noSuchInstance must not produce a value")
	}
	if v := pduValue(gosnmp.SnmpPDU{Type: gosnmp.Integer, Value: -5}); v.ok {
		t.Error("a negative counter must not produce a value")
	}
}

func mustCompile(t *testing.T, patterns ...string) []*regexp.Regexp {
	t.Helper()
	var out []*regexp.Regexp
	for _, p := range patterns {
		out = append(out, regexp.MustCompile(p))
	}
	return out
}
