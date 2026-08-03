package poll

import (
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
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
	if got := listOf(p)[0].name; got != "ge-0/0/0" {
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
	if got := listOf(p)[0].name; got != "xe-0/0/0" {
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
	got := listOf(p)[0]
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

// One walk without an interface is not proof that it is gone: gosnmp reports a
// walk the agent cut short as a clean end, so a truncated walk looks exactly
// like a table that shrank. Acting on it would delete hundreds of live ports.
func TestRetainNeedsTwoWalksToDropAnInterface(t *testing.T) {
	p := &Poller{ifaces: []iface{{index: 1}, {index: 2}, {index: 3}}}

	p.retain(map[int]bool{1: true, 3: true})
	if want := []int{1, 2, 3}; !reflect.DeepEqual(indexesOf(p), want) {
		t.Errorf("kept %v, want %v — a single missing walk is not enough", indexesOf(p), want)
	}

	p.retain(map[int]bool{1: true, 3: true})
	if want := []int{1, 3}; !reflect.DeepEqual(indexesOf(p), want) {
		t.Errorf("kept %v, want %v once a second walk missed it too", indexesOf(p), want)
	}
}

// Turning up again clears the strike. Without that, an interface on a device
// with the occasional truncated walk would collect misses and eventually
// disappear although it was there all along.
func TestRetainForgivesAnInterfaceThatComesBack(t *testing.T) {
	p := &Poller{ifaces: []iface{{index: 1}, {index: 2}}}

	p.retain(map[int]bool{1: true})          // 2 missed once
	p.retain(map[int]bool{1: true, 2: true}) // and is back
	p.retain(map[int]bool{1: true})          // missed once again

	if want := []int{1, 2}; !reflect.DeepEqual(indexesOf(p), want) {
		t.Errorf("kept %v, want %v — the earlier strike should be forgotten", indexesOf(p), want)
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

// listOf reads the interface list without the drain takeInterfaces does, so a
// test can look at it without consuming the wake-up signal.
func listOf(p *Poller) []iface {
	p.ifmu.Lock()
	defer p.ifmu.Unlock()
	return slices.Clone(p.ifaces)
}

func indexesOf(p *Poller) []int {
	var out []int
	for _, i := range listOf(p) {
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
		profile: config.Profile{SecValueFactor: 8},
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

	// Interface 2 was never seen before, so it has no rates yet.
	if rows[1].InOctets != nil || rows[1].InPkts != nil {
		t.Error("a new interface must not report rates on its first poll")
	}
}

// A row whose counters have not come back yet must keep showing the previous
// poll's numbers. Blanking it makes the whole table flicker on a slow agent.
func TestBuildRowsKeepsPreviousValuesWhilePending(t *testing.T) {
	previous := int64(4242)
	p := &Poller{
		profile: config.Profile{SecValueFactor: 1},
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
	// Interface 2 answered but has no history, so there is no rate to show yet.
	// The mark says "these figures are from this poll", and there are none.
	if rows[1].InOctets != nil {
		t.Errorf("unseen interface = %v, want no value", deref(rows[1].InOctets))
	}
	if rows[1].Fresh {
		t.Error("a row showing no figures at all must not be marked fresh")
	}
}

// An interface that was pending in one poll and answers in the next has no
// baseline yet — but it does have the figures the pending row was showing.
// Blanking them for one round and then filling them in again is a flicker with
// no information in it.
func TestBuildRowsKeepsValuesUntilThereIsABaseline(t *testing.T) {
	previous := int64(4242)
	p := &Poller{
		profile:  config.Profile{SecValueFactor: 1},
		lastRows: map[int]Row{1: {Index: 1, InOctets: &previous}},
		prev:     map[int]counters{}, // answered last time, but nothing recorded
	}
	st := &state{
		ifaces: []iface{{index: 1, name: "xe-0/0/0"}},
		raw:    []counters{{inOct: val(7), outOct: val(7), inPkt: val(1), outPkt: val(1), errIn: val(0)}},
		secs:   10,
		done:   []bool{true},
	}

	rows := p.buildRows(st)

	if rows[0].InOctets == nil || *rows[0].InOctets != previous {
		t.Errorf("row = %v, want the previous %d rather than a dash", deref(rows[0].InOctets), previous)
	}
	if rows[0].Fresh {
		t.Error("those figures are not from this poll")
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

	p.remember(st, []Row{{Index: 1}, {Index: 2}}, time.Now())

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
		profile:  config.Profile{SecValueFactor: 1},
		lastRows: map[int]Row{2: {Index: 2, InOctets: &previous}},
	}
	st := &state{
		ifaces: []iface{{index: 1, name: "xe-0/0/0"}, {index: 2, name: "xe-0/0/1"}},
		raw: []counters{
			{inOct: val(10), outOct: val(10), inPkt: val(1), outPkt: val(1), errIn: val(0)},
			{denied: true}, // every varbind came back as noSuchInstance
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

// An answer that simply left the varbinds out looks exactly like a removed
// port from the outside, and an agent replying tooBig produces precisely that.
// Deleting the whole batch over it would empty the table in chunks of twelve.
func TestASilentAnswerIsNotAremovedInterface(t *testing.T) {
	previous := int64(500)
	p := &Poller{
		profile:  config.Profile{SecValueFactor: 1},
		lastRows: map[int]Row{1: {Index: 1, InOctets: &previous}},
	}
	st := &state{
		ifaces: []iface{{index: 1, name: "xe-0/0/0"}},
		raw:    []counters{{}}, // answered, nothing assigned, no denial
		done:   []bool{true},
	}

	if gone := vanished(st); len(gone) != 0 {
		t.Errorf("vanished = %v, want nothing dropped without an explicit denial", gone)
	}
	// The same rule has to hold for the table: dropping the row here made it
	// disappear for one poll and come back in the next, in batch-sized blocks.
	rows := p.buildRows(st)
	if len(rows) != 1 {
		t.Fatalf("%d rows, want the interface kept on screen too", len(rows))
	}
	if rows[0].InOctets == nil || *rows[0].InOctets != previous {
		t.Errorf("row = %v, want the previous value carried", deref(rows[0].InOctets))
	}
	if rows[0].Fresh {
		t.Error("nothing came back for this row, so it is not fresh")
	}
}

// A port that reports only the vendor's per-second objects has everything the
// table needs. Counting it as empty threw exactly the rows away that
// sec_value_factor exists for.
func TestVendorOnlyCountersAreNotEmpty(t *testing.T) {
	p := &Poller{profile: config.Profile{SecValueFactor: 8}}
	st := &state{
		ifaces: []iface{{index: 1, name: "et-0/0/0"}},
		raw:    []counters{{secInOct: val(8000), secOutOct: val(16000)}},
		secs:   10,
		done:   []bool{true},
	}

	rows := p.buildRows(st)

	if len(rows) != 1 {
		t.Fatalf("%d rows, want the interface kept", len(rows))
	}
	if rows[0].InOctets == nil || *rows[0].InOctets != 1000 {
		t.Errorf("in = %v, want the vendor rate 8000/8", deref(rows[0].InOctets))
	}
	if !rows[0].Fresh {
		t.Error("those figures are from this poll")
	}
	// And it is not a removed port either.
	if gone := vanished(st); len(gone) != 0 {
		t.Errorf("vanished = %v, want nothing dropped", gone)
	}
}

// A timeout looks similar but must not remove anything: the port is probably
// still there, the agent just did not answer in time.
func TestTimeoutIsNotMistakenForARemovedInterface(t *testing.T) {
	previous := int64(500)
	p := &Poller{
		profile:  config.Profile{SecValueFactor: 1},
		lastRows: map[int]Row{1: {Index: 1, InOctets: &previous}},
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

// forget is as sceptical as retain, and for the same reason: a port with no
// counters at all keeps appearing in ifName, so deleting it every poll and
// having the next discovery put it straight back would make the row blink.
// The table does not wait for this — buildRows skips a denied interface — so
// the only question here is when it stops being polled.
func TestForgetNeedsTwoPollsToDropAnInterface(t *testing.T) {
	p := &Poller{ifaces: []iface{{index: 1}, {index: 2}, {index: 3}}}

	p.forget(map[int]bool{2: true})
	if want := []int{1, 2, 3}; !reflect.DeepEqual(indexesOf(p), want) {
		t.Errorf("kept %v, want %v — one denial is not enough", indexesOf(p), want)
	}

	p.forget(map[int]bool{2: true})
	if want := []int{1, 3}; !reflect.DeepEqual(indexesOf(p), want) {
		t.Errorf("kept %v, want %v after a second denial", indexesOf(p), want)
	}
}

// A discovery that lists the port again clears its strikes, whichever of the
// two put them there.
func TestForgetAndRetainShareTheirStrikes(t *testing.T) {
	p := &Poller{ifaces: []iface{{index: 1}, {index: 2}}}

	p.forget(map[int]bool{2: true})          // denied by a poll
	p.retain(map[int]bool{1: true, 2: true}) // but the walk still lists it
	p.forget(map[int]bool{2: true})          // denied again

	if want := []int{1, 2}; !reflect.DeepEqual(indexesOf(p), want) {
		t.Errorf("kept %v, want %v — the walk cleared the earlier strike", indexesOf(p), want)
	}
}

func TestIntervalPrefersAgentClock(t *testing.T) {
	p := &Poller{lastUptime: val(1000), baseUptime: val(1000),
		lastPoll: time.Now().Add(-time.Minute), prev: map[int]counters{1: {}}}

	// 1500 - 1000 TimeTicks is 5 seconds, not the wall clock's 60.
	if got := p.interval(val(1500), time.Now()); got != 5 {
		t.Errorf("interval = %v, want 5", got)
	}
}

func TestIntervalDetectsReboot(t *testing.T) {
	p := &Poller{lastUptime: val(50000), baseUptime: val(50000), prev: map[int]counters{1: {inOct: val(9)}}}

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

// A lost scalar request costs a warning, not the poll: three OIDs must not
// decide over six hundred interface counters. What it would have shown stands
// in from the previous poll rather than blanking the header and collapsing the
// readings block.
func TestSnapshotFallsBackToTheLastGoodScalars(t *testing.T) {
	p := &Poller{}
	good := &state{sysName: "leaf16", cpu: "8 4", scalarsOK: true, cpuOK: true,
		readings: []Reading{{Name: "Temperatur", Value: "31"}}}
	p.remember(good, nil, time.Now())

	lost := &state{warnings: []string{"system: request timeout"}}
	snap := p.snapshot(lost, true, time.Now())

	if snap.SysName != "leaf16" || snap.CPU != "8 4" {
		t.Errorf("header = %q/%q, want the previous values", snap.SysName, snap.CPU)
	}
	if len(snap.Readings) != 1 {
		t.Errorf("%d readings, want the previous one so the block keeps its height", len(snap.Readings))
	}
	if len(snap.Warnings) != 1 {
		t.Errorf("warnings = %v, want the failure reported", snap.Warnings)
	}
}

// An empty answer from a request that worked is the truth. Papering over it
// would make a CPU OID that stopped answering look like one that still does.
func TestSnapshotShowsAnEmptyAnswerAsEmpty(t *testing.T) {
	p := &Poller{}
	p.remember(&state{sysName: "leaf16", cpu: "8 4", scalarsOK: true, cpuOK: true}, nil, time.Now())

	snap := p.snapshot(&state{scalarsOK: true, cpuOK: true}, true, time.Now())

	if snap.SysName != "" || snap.CPU != "" {
		t.Errorf("header = %q/%q, want both empty rather than frozen", snap.SysName, snap.CPU)
	}
}

// The tick baseline has to survive a lost scalar request, or the restart it
// would have caught is read as a counter wrap: a four-billion-packet spike.
func TestRememberKeepsTheLastGoodUptime(t *testing.T) {
	p := &Poller{}
	p.remember(&state{uptime: val(100_000), scalarsOK: true}, nil, time.Now())

	p.remember(&state{}, nil, time.Now()) // scalars lost

	if !p.lastUptime.ok || p.lastUptime.n != 100_000 {
		t.Errorf("lastUptime = %+v, want the last one that arrived", p.lastUptime)
	}
	if p.baseUptime.ok {
		t.Error("baseUptime must be unset: the counters now come from a poll without an uptime")
	}

	// The restart is still caught against that older yardstick.
	p.prev = map[int]counters{1: {inPkt: val(5000)}}
	if secs := p.interval(val(300), time.Now()); secs != 0 {
		t.Errorf("secs = %v, want 0 — the agent restarted", secs)
	}
	if len(p.prev) != 0 {
		t.Error("counters from before the restart were kept")
	}
}

// With no baseline the ticks would span two intervals while the counters span
// one, halving every rate. The wall clock is the honest answer that round.
func TestIntervalIgnoresAStaleBaseline(t *testing.T) {
	p := &Poller{
		lastUptime: val(100_000), // seen two polls ago
		lastPoll:   time.Now().Add(-10 * time.Second),
	}

	// 20s of ticks, but only 10s since the counters it is compared against.
	got := p.interval(val(102_000), time.Now())

	if got < 9.5 || got > 10.5 {
		t.Errorf("secs = %v, want about 10 from the wall clock, not 20 from the ticks", got)
	}
}

// Recovery: once a poll brings an uptime again, the agent's clock takes over
// from the wall clock. Without this the rates stay on the poller's clock for
// good, which is a silent and permanent degradation.
func TestBaselineRecoversAfterAGoodPoll(t *testing.T) {
	p := &Poller{}
	p.remember(&state{uptime: val(100_000), scalarsOK: true}, nil, time.Now())
	p.remember(&state{}, nil, time.Now())                                      // lost
	p.remember(&state{uptime: val(102_000), scalarsOK: true}, nil, time.Now()) // good again

	// Deliberately a wall clock that would give a different answer.
	p.lastPoll = time.Now().Add(-99 * time.Second)

	if got := p.interval(val(103_000), time.Now()); got != 10 {
		t.Errorf("secs = %v, want 10 from the ticks again", got)
	}
}

// gosnmp leaves a non-zero error-status in the packet and reports no error of
// its own. Reading such a response as valid-but-empty is what let a tooBig
// wipe a batch of interfaces off the table.
func TestResponseError(t *testing.T) {
	if err := responseError(&gosnmp.SnmpPacket{Error: gosnmp.NoError}); err != nil {
		t.Errorf("noError became %v", err)
	}
	if err := responseError(nil); err == nil {
		t.Error("a missing packet must be an error")
	}
	for _, status := range []gosnmp.SNMPError{
		gosnmp.TooBig, gosnmp.GenErr, gosnmp.ResourceUnavailable, gosnmp.AuthorizationError,
	} {
		err := responseError(&gosnmp.SnmpPacket{Error: status})
		if err == nil {
			t.Errorf("%v passed as a valid response", status)
			continue
		}
		if !strings.Contains(err.Error(), status.String()) {
			t.Errorf("error %q does not name the status %v", err, status)
		}
	}
}

func TestDeniedOnlyForAnExplicitDenial(t *testing.T) {
	for _, typ := range []gosnmp.Asn1BER{gosnmp.NoSuchObject, gosnmp.NoSuchInstance} {
		if !denied(gosnmp.SnmpPDU{Type: typ}) {
			t.Errorf("%v is a denial", typ)
		}
	}
	// endOfMibView and Null mean "no value here", not "no such interface".
	for _, typ := range []gosnmp.Asn1BER{gosnmp.EndOfMibView, gosnmp.Null, gosnmp.Counter64} {
		if denied(gosnmp.SnmpPDU{Type: typ}) {
			t.Errorf("%v must not count as a denial", typ)
		}
	}
}

// A tick delta of zero measures nothing. It happens on an agent whose
// sysUpTime is stuck or reports whole seconds coarsely; taking it as the
// interval would print "-" in every cell rather than showing rates at all.
func TestIntervalIgnoresAStandingClock(t *testing.T) {
	p := &Poller{
		lastUptime: val(100_000),
		baseUptime: val(100_000),
		lastPoll:   time.Now().Add(-4 * time.Second),
		prev:       map[int]counters{1: {inOct: val(5)}},
	}

	got := p.interval(val(100_000), time.Now()) // same tick as last time

	if got < 3.5 || got > 4.5 {
		t.Errorf("secs = %v, want about 4 from the wall clock", got)
	}
	if len(p.prev) == 0 {
		t.Error("an unchanged clock is not a restart; the counters must stay")
	}
}

// The info block gives each warning one row, and against a mute agent the error
// from parallel() carries one entry per request. Fifty timeouts have to read as
// one fact on one line, or the display cuts everything after the first.
func TestWarningIsOneLine(t *testing.T) {
	joined := errors.Join(
		errors.New("request timeout (after 1 retries)"),
		errors.New("request timeout (after 1 retries)"),
		errors.New("request timeout (after 1 retries)"),
		errors.New("agent reported TooBig"),
	)

	got := Warning("counters", joined)

	if strings.Contains(got, "\n") {
		t.Fatalf("warning spans several lines: %q", got)
	}
	if !strings.HasPrefix(got, "counters: ") {
		t.Errorf("%q does not say which request failed", got)
	}
	if !strings.Contains(got, "(x3)") {
		t.Errorf("%q does not collapse the three identical timeouts", got)
	}
	if !strings.Contains(got, "TooBig") {
		t.Errorf("%q lost the second, different failure", got)
	}
}

// A yaml error brings its own newlines without being a joined error.
func TestWarningFlattensASingleError(t *testing.T) {
	got := Warning("system", errors.New("first line\n  second line"))
	if strings.Contains(got, "\n") {
		t.Errorf("warning spans several lines: %q", got)
	}
	if !strings.Contains(got, "second line") {
		t.Errorf("%q lost the detail", got)
	}
}

// Joins nest once the caller joins what it got from parallel(). Unwrapping has
// to reach through that, but stop at a wrapped error: its own message carries
// the context ("file.device: …") that unwrapping would throw away.
func TestWarningFlattensNestedJoinsButKeepsContext(t *testing.T) {
	nested := errors.Join(
		errors.Join(errors.New("timeout"), errors.New("timeout")),
		errors.New("agent reported TooBig"),
	)

	got := Warning("counters", nested)

	if !strings.Contains(got, "timeout (x2)") {
		t.Errorf("%q did not count the two nested timeouts", got)
	}
	if !strings.Contains(got, "TooBig") {
		t.Errorf("%q lost the other failure", got)
	}

	// A wrapped error stays whole, context and all.
	wrapped := fmt.Errorf("legacy.device: %w", errors.New("field :name not found"))
	if got := Warning("ignored config", wrapped); !strings.Contains(got, "legacy.device: field :name not found") {
		t.Errorf("%q lost the file name", got)
	}
}
