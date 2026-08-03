// Package poll queries a device over SNMP and turns raw counters into the
// per-second rates the table displays.
package poll

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
	"github.com/mafri27/snmpscan/internal/config"
)

// Row is one interface as shown in the table. A nil rate means the value is
// not available yet or could not be trusted this interval.
type Row struct {
	Index     int
	Name      string
	Alias     string
	InOctets  *int64 // bytes/s
	OutOctets *int64 // bytes/s
	InPkts    *int64 // packets/s
	OutPkts   *int64 // packets/s
	InErrors  *int64 // errors/s
	// Fresh reports whether these values came back during the current poll.
	// A stale row is showing what the previous poll found, which beats
	// blanking it while the agent is still answering.
	Fresh bool
}

// Reading is one of the extra values above the table, already judged against
// its profile.
type Reading struct {
	Name    string
	Value   string
	IsError bool
}

// Snapshot is one complete poll of a device.
type Snapshot struct {
	SysName  string
	CPU      string
	Readings []Reading
	Rows     []Row
	Elapsed  time.Duration
	Requests int
	Warnings []string
	// Complete is false while counters are still arriving.
	Complete bool
}

// Options configures a Poller.
type Options struct {
	Target Target
	Config *config.Set
	// Filters replaces the profile's default_filter when not empty.
	Filters []string
	// Readings includes the profile's extra readings (temperature, alarms).
	// Off by default; they take screen rows away from the interface table.
	Readings bool
	// Sessions is the number of parallel SNMP conversations.
	Sessions int
	// Warnings are shown above the poll's own, for trouble found before any
	// polling started — a config file that would not parse, say.
	Warnings []string
}

// Poller keeps the SNMP sessions and the previous counter readings needed to
// compute rates.
type Poller struct {
	opts    Options
	profile config.Profile
	filters []*regexp.Regexp
	pool    *pool

	sysDescr  string
	batchSize int
	warnings  []string

	sent         atomic.Int64
	prev         map[int]counters
	lastRows     map[int]Row
	lastSysName  string
	lastCPU      string
	lastReadings []Reading
	lastUptime   value // newest sysUpTime seen at all: spots a restart
	baseUptime   value // sysUpTime of the poll prev came from; see interval
	lastPoll     time.Time

	// The interface list is refreshed on its own schedule: walking ifName and
	// ifAlias costs far more packets than reading the counters, and ports do
	// not come and go between two polls. It gets its own sessions so a
	// discovery cannot take any away from a poll running at the same time,
	// and its packets stay out of the poll's count.
	discoveryPool *pool
	discoverMu    sync.Mutex // serialises Discover against itself
	ifmu          sync.Mutex
	ifaces        []iface
	discovered    bool
	walked        bool // a discovery has run to completion at least once
	added         chan struct{}
}

type counters struct {
	inOct, outOct, inPkt, outPkt, errIn      value
	secInOct, secOutOct, secInPkt, secOutPkt value
	// denied means the agent answered that it has no such object. A value can
	// go missing in plenty of ways; only this one says the interface is gone.
	denied bool
}

type iface struct {
	index int
	name  string
	alias string
	// missed counts consecutive completed walks that did not list this
	// interface. See retain.
	missed int
}

// missedWalksBeforeDrop is how often a walk has to come back without an
// interface before it leaves the table. One is not enough: gosnmp reports a
// walk the agent cut short as a clean end — it logs the error-status and
// returns nil — so a truncated walk is indistinguishable from a table that
// really shrank, and taking it at face value would delete hundreds of ports.
const missedWalksBeforeDrop = 2

// New opens the sessions, reads sysDescr and resolves the matching profile.
func New(ctx context.Context, opts Options) (*Poller, error) {
	if opts.Sessions <= 0 {
		// Measured against a QFX5100 with 590 interfaces: 8 sessions poll in
		// 5.8s where 4 need 7.5s. Beyond that the agent, not the client, is
		// the bottleneck.
		opts.Sessions = 8
	}
	if opts.Target.Port == 0 {
		opts.Target.Port = 161
	}

	p := &Poller{opts: opts, prev: map[int]counters{},
		batchSize: min(maxVarbindsPerGet, gosnmp.MaxOids),
		added:     make(chan struct{}, 1), warnings: opts.Warnings}

	pl, err := newPool(opts.Target, opts.Sessions, &p.sent)
	if err != nil {
		return nil, err
	}
	p.pool = pl

	// Two sessions, one per walk. Deliberately separate from the poll pool:
	// discovery may well be running whenever a poll starts, and it must not
	// eat into the connections the counters are spread over. No counter is
	// passed, so its packets stay out of the figure shown per poll.
	dp, err := newPool(opts.Target, 2, nil)
	if err != nil {
		p.Close()
		return nil, err
	}
	p.discoveryPool = dp

	if err := p.pool.with(ctx, func(c *gosnmp.GoSNMP) error {
		res, err := c.Get([]string{oidSysDescr})
		if err == nil {
			err = responseError(res)
		}
		if err != nil {
			// Carrying on would match the empty sysDescr against the profiles
			// and quietly pick the fallback one.
			return fmt.Errorf("sysDescr: %w", err)
		}
		if len(res.Variables) > 0 {
			p.sysDescr = pduString(res.Variables[0])
		}
		return nil
	}); err != nil {
		p.Close()
		return nil, err
	}

	p.profile = opts.Config.Match(p.sysDescr)
	if !opts.Readings {
		p.profile.AddInfos = nil
	}

	patterns := opts.Filters
	if len(patterns) == 0 {
		patterns = p.profile.Filters
	}
	if len(patterns) == 0 {
		patterns = []string{""} // an empty pattern matches every interface
	}
	for _, pat := range patterns {
		re, err := regexp.Compile(pat)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("interface filter %q: %w", pat, err)
		}
		p.filters = append(p.filters, re)
	}

	return p, nil
}

// SysDescr is the sysDescr the profile was selected with.
func (p *Poller) SysDescr() string { return p.sysDescr }

// Profile is the merged device profile in use.
func (p *Poller) Profile() config.Profile { return p.profile }

func (p *Poller) Close() {
	p.pool.Close()
	if p.discoveryPool != nil {
		p.discoveryPool.Close()
	}
}

// state accumulates one poll. Everything in here is written from several
// goroutines at once, so mu guards all of it.
type state struct {
	mu       sync.Mutex
	sysName  string
	cpu      string
	readings []Reading
	warnings []string
	uptime   value
	// Whether the two scalar requests came through. Distinct from an empty
	// result, which is a valid answer and must not be papered over with the
	// previous poll's value.
	scalarsOK bool
	cpuOK     bool

	ifaces []iface
	raw    []counters
	secs   float64
	// done marks interfaces whose batch has come back. Batches never split an
	// interface, so a response always completes whole rows.
	done []bool
}

// Discover refreshes the list of interfaces to display. It walks ifName and
// ifAlias and applies the filter, which on a large device is the bulk of the
// SNMP traffic — hence its own entry point and its own sessions, so it can run
// alongside Poll rather than holding it up.
//
// Interfaces become visible to Poll as they are found, not when the walk ends.
// Calls are serialised against each other.
func (p *Poller) Discover(ctx context.Context) error {
	p.discoverMu.Lock()
	defer p.discoverMu.Unlock()

	d := &discovery{poller: p, names: map[int]string{}, aliases: map[int]string{}, kept: map[int]bool{}}

	err := parallel([]func() error{
		func() error {
			return p.discoveryPool.with(ctx, func(c *gosnmp.GoSNMP) error {
				return walkColumn(c, oidIfName, d.name)
			})
		},
		func() error {
			return p.discoveryPool.with(ctx, func(c *gosnmp.GoSNMP) error {
				return walkColumn(c, oidIfAlias, d.alias)
			})
		},
	})
	if err != nil {
		// Whatever was found before the walk broke off stays on the list: it
		// saw only a prefix of the table, not the whole truth. A walk the agent
		// cut short arrives here as success, which is why retain needs its own
		// safeguard on top of this one.
		return err
	}

	p.retain(d.kept)
	return nil
}

// discovery accumulates one Discover run. Values arrive from two walks at
// once, and each one that completes an interface is published immediately.
type discovery struct {
	poller  *Poller
	mu      sync.Mutex
	names   map[int]string
	aliases map[int]string
	kept    map[int]bool
}

func (d *discovery) name(index int, value string) {
	d.mu.Lock()
	d.names[index] = value
	d.mu.Unlock()
	d.publish(index)
}

func (d *discovery) alias(index int, value string) {
	d.mu.Lock()
	d.aliases[index] = value
	d.mu.Unlock()
	d.publish(index)
}

// publish adds an interface to the live list as soon as it is worth showing.
// It waits for the name — a row without one is useless — but not for the
// alias, which the other walk may still be catching up on.
func (d *discovery) publish(index int) {
	d.mu.Lock()
	name, named := d.names[index]
	alias := d.aliases[index]
	if named && d.poller.matches(name, alias) {
		d.kept[index] = true
	}
	keep := d.kept[index]
	d.mu.Unlock()

	if named && keep {
		d.poller.upsert(iface{index: index, name: name, alias: alias})
	}
}

// Interfaces is the number of interfaces currently on the list.
func (p *Poller) Interfaces() int {
	p.ifmu.Lock()
	defer p.ifmu.Unlock()
	return len(p.ifaces)
}

func (p *Poller) currentInterfaces() []iface {
	p.ifmu.Lock()
	defer p.ifmu.Unlock()
	return slices.Clone(p.ifaces)
}

// takeInterfaces is currentInterfaces for the poll about to read them, and it
// clears any pending Added signal on the way out. Everything published so far
// is in this list and is about to be fetched, so waking up for it afterwards
// would poll the same set twice. An interface arriving after this point sets
// the signal again and is not in the list — which is exactly the case the
// wake-up is for.
func (p *Poller) takeInterfaces() []iface {
	p.ifmu.Lock()
	defer p.ifmu.Unlock()
	select {
	case <-p.added:
	default:
	}
	return slices.Clone(p.ifaces)
}

// upsert publishes one interface, keeping the list ordered by ifIndex. This
// runs while a discovery is still walking, so polls can already work with the
// interfaces found so far.
func (p *Poller) upsert(in iface) {
	p.ifmu.Lock()
	defer p.ifmu.Unlock()

	at, found := slices.BinarySearchFunc(p.ifaces, in, func(a, b iface) int {
		return cmp.Compare(a.index, b.index)
	})
	if found {
		p.ifaces[at] = in
	} else {
		p.ifaces = slices.Insert(p.ifaces, at, in)
		p.signalAdded()
	}
	p.discovered = true
}

// Added fires when the first discovery publishes an interface nobody has
// polled yet, so the caller can fetch its values instead of waiting out the
// interval. It goes quiet once a walk has completed: from then on the list is
// known and a port appearing is no reason to break the rhythm.
// Buffered by one, so a walk finding hundreds of ports coalesces into a single
// wake-up rather than queueing one poll per port.
func (p *Poller) Added() <-chan struct{} { return p.added }

// signalAdded must be called with ifmu held.
func (p *Poller) signalAdded() {
	if p.walked {
		return
	}
	select {
	case p.added <- struct{}{}:
	default:
	}
}

// retain reconciles the list with what a completed discovery saw: ports that
// are gone from the table, and ports whose name or alias no longer matches the
// filter. An interface the walk did not list gets a strike rather than being
// dropped straight away — see missedWalksBeforeDrop. A port the agent denies
// outright still goes at once, through forget.
func (p *Poller) retain(kept map[int]bool) {
	p.ifmu.Lock()
	defer p.ifmu.Unlock()

	p.ifaces = slices.DeleteFunc(p.ifaces, func(i iface) bool {
		return !kept[i.index] && i.missed+1 >= missedWalksBeforeDrop
	})
	for i := range p.ifaces {
		if kept[p.ifaces[i].index] {
			p.ifaces[i].missed = 0
		} else {
			p.ifaces[i].missed++
		}
	}
	p.discovered = true
	p.walked = true
}

// forget drops interfaces the agent no longer knows about.
func (p *Poller) forget(gone map[int]bool) {
	if len(gone) == 0 {
		return
	}
	p.ifmu.Lock()
	p.ifaces = slices.DeleteFunc(p.ifaces, func(i iface) bool { return gone[i.index] })
	p.ifmu.Unlock()
}

// Poll reads the counters of the interfaces Discover has published so far and
// returns the rates since the previous call. It never waits for a discovery:
// on a device that takes minutes to walk, the first ports are polled while the
// rest are still being found. With no interfaces known yet it returns an empty
// table rather than an error.
//
// onUpdate, if given, receives the table as it fills up: once with every
// interface named but no values yet, then after each batch of counters comes
// back. On a loaded agent that turns a screen that sits empty for seconds
// into one that populates row by row. It is called from the goroutine that
// received the data, and the snapshot it gets is not touched again.
func (p *Poller) Poll(ctx context.Context, onUpdate func(*Snapshot)) (*Snapshot, error) {
	start := time.Now()
	p.sent.Store(0)
	st := &state{}

	addOIDs := make([]string, 0, len(p.profile.AddInfos))
	for _, a := range p.profile.AddInfos {
		addOIDs = append(addOIDs, a.OID)
	}
	scalars := append([]string{oidSysName, oidSysUpTime}, addOIDs...)

	err := parallel([]func() error{
		func() error {
			return p.pool.with(ctx, func(c *gosnmp.GoSNMP) error {
				vars, err := getAll(c, scalars)
				st.mu.Lock()
				defer st.mu.Unlock()
				if pdu, ok := vars[oidSysName]; ok {
					st.sysName = pduString(pdu)
				}
				if pdu, ok := vars[oidSysUpTime]; ok {
					st.uptime = pduValue(pdu)
				}
				st.readings = evaluate(p.profile.AddInfos, vars)
				st.scalarsOK = err == nil
				if err != nil {
					// Three scalars must not decide over six hundred
					// interface counters.
					st.warnings = append(st.warnings, Warning("system", err))
				}
				return nil
			})
		},
		func() error {
			return p.pool.with(ctx, func(c *gosnmp.GoSNMP) error {
				pdus, err := c.BulkWalkAll(p.profile.CPUOID)
				st.mu.Lock()
				defer st.mu.Unlock()
				if err != nil {
					// A missing CPU OID must not take the whole poll down;
					// plenty of profiles point at a best guess.
					st.warnings = append(st.warnings, Warning("cpu", err))
					return nil
				}
				parts := make([]string, 0, len(pdus))
				for _, pdu := range pdus {
					parts = append(parts, pduString(pdu))
				}
				st.cpu = strings.Join(parts, " ")
				st.cpuOK = true
				return nil
			})
		},
	})
	if err != nil {
		return nil, err
	}

	cols := p.counterColumns()
	st.ifaces = p.takeInterfaces()
	st.raw = make([]counters, len(st.ifaces))
	st.done = make([]bool, len(st.ifaces))
	st.secs = p.interval(st.uptime, start)

	// The names are already known, so the full list can be on screen while
	// the counters are still in flight.
	if onUpdate != nil {
		st.mu.Lock()
		onUpdate(p.snapshot(st, false, start))
		st.mu.Unlock()
	}

	var onBatch func()
	if onUpdate != nil {
		onBatch = func() { onUpdate(p.snapshot(st, false, start)) }
	}
	if err := p.readCounters(ctx, st, cols, onBatch); err != nil {
		st.mu.Lock()
		st.warnings = append(st.warnings, Warning("counters", err))
		st.mu.Unlock()
	}

	st.mu.Lock()
	final := p.snapshot(st, true, start)
	gone := vanished(st)
	st.mu.Unlock()

	// An interface that was removed since the last discovery answers every
	// counter with noSuchInstance. Drop it now instead of showing a frozen
	// row until the next discovery run.
	p.forget(gone)

	p.remember(st, final.Rows, start)
	return final, nil
}

// vanished lists interfaces the agent explicitly denied knowing: the row was
// answered, that answer was a noSuchObject/noSuchInstance, and not one counter
// came back. A timeout leaves done unset, and a response that simply omitted
// the varbinds carries no denial — neither may pass for a removed port.
// The caller must hold st.mu.
func vanished(st *state) map[int]bool {
	var gone map[int]bool
	for i, ifc := range st.ifaces {
		if st.done[i] && st.raw[i].denied && st.raw[i].empty() {
			if gone == nil {
				gone = make(map[int]bool)
			}
			gone[ifc.index] = true
		}
	}
	return gone
}

// empty reports that not one standard counter came back with a value.
func (c counters) empty() bool {
	return !c.inOct.ok && !c.outOct.ok && !c.inPkt.ok && !c.outPkt.ok && !c.errIn.ok
}

// snapshot renders the current state. The caller must hold st.mu, and Poll
// must not overlap itself — the values remembered from the last poll are read
// here without a lock of their own.
func (p *Poller) snapshot(st *state, complete bool, start time.Time) *Snapshot {
	// A request that failed leaves nothing worth showing, so the previous
	// figures stand in: better than a blank header and a table that jumps as
	// the readings block collapses. A request that worked is shown as it is,
	// empty or not.
	sysName, readings := st.sysName, st.readings
	if !st.scalarsOK {
		sysName = cmp.Or(sysName, p.lastSysName)
		readings = p.lastReadings
	}
	cpu := st.cpu
	if !st.cpuOK {
		cpu = p.lastCPU
	}
	return &Snapshot{
		SysName:  sysName,
		CPU:      cpu,
		Readings: slices.Clone(readings),
		Warnings: append(slices.Clone(p.warnings), st.warnings...),
		Rows:     p.buildRows(st),
		Requests: int(p.sent.Load()),
		Elapsed:  time.Since(start),
		Complete: complete,
	}
}

func (p *Poller) matches(name, alias string) bool {
	for _, re := range p.filters {
		if re.MatchString(name) || re.MatchString(alias) {
			return true
		}
	}
	return false
}

// counterColumns lists the table columns to fetch per interface. The vendor
// specific per-second objects are appended only when the profile has them.
func (p *Poller) counterColumns() []column {
	cols := []column{
		{oidIfHCInOctets, func(c *counters, v value) { c.inOct = v }},
		{oidIfHCOutOctets, func(c *counters, v value) { c.outOct = v }},
		{oidIfInUcastPkts, func(c *counters, v value) { c.inPkt = v }},
		{oidIfOutUcastPkt, func(c *counters, v value) { c.outPkt = v }},
		{oidIfInErrors, func(c *counters, v value) { c.errIn = v }},
	}
	add := func(oid string, set func(*counters, value)) {
		if oid != "" {
			cols = append(cols, column{oid, set})
		}
	}
	add(p.profile.InSecOctOID, func(c *counters, v value) { c.secInOct = v })
	add(p.profile.OutSecOctOID, func(c *counters, v value) { c.secOutOct = v })
	add(p.profile.InSecPpsOID, func(c *counters, v value) { c.secInPkt = v })
	add(p.profile.OutSecPpsOID, func(c *counters, v value) { c.secOutPkt = v })
	return cols
}

type column struct {
	base   string
	assign func(*counters, value)
}

type cell struct{ row, col int }

// readCounters fetches every column of every selected interface with plain
// GETs, spread over the session pool. Each request carries whole interfaces
// only, so a response always completes the rows it covers.
// onBatch is called after each response with st.mu held, so it may read st.
func (p *Poller) readCounters(ctx context.Context, st *state, cols []column, onBatch func()) error {
	if len(st.ifaces) == 0 {
		return nil
	}

	where := make(map[string]cell, len(st.ifaces)*len(cols))
	for ri, ifc := range st.ifaces {
		for ci, col := range cols {
			where[col.base+"."+strconv.Itoa(ifc.index)] = cell{ri, ci}
		}
	}

	groups := batchInterfaces(len(st.ifaces), len(cols), p.batchSize)
	tasks := make([]func() error, len(groups))
	for i, g := range groups {
		tasks[i] = func() error {
			oids := make([]string, 0, (g.end-g.start)*len(cols))
			for _, ifc := range st.ifaces[g.start:g.end] {
				for _, col := range cols {
					oids = append(oids, col.base+"."+strconv.Itoa(ifc.index))
				}
			}
			return p.pool.with(ctx, func(c *gosnmp.GoSNMP) error {
				res, err := c.Get(oids)
				if err == nil {
					err = responseError(res)
				}
				if err != nil {
					return err
				}
				st.mu.Lock()
				defer st.mu.Unlock()
				for _, pdu := range res.Variables {
					if at, ok := where[trimOID(pdu.Name)]; ok {
						cols[at.col].assign(&st.raw[at.row], pduValue(pdu))
						if denied(pdu) {
							st.raw[at.row].denied = true
						}
					}
				}
				// Only now are these rows known to be answered — an error
				// above leaves them pending, so they keep the last values
				// instead of being read as interfaces that went away.
				for r := g.start; r < g.end; r++ {
					st.done[r] = true
				}
				if onBatch != nil {
					onBatch()
				}
				return nil
			})
		}
	}
	return parallel(tasks)
}

// group is a half-open range of interfaces answered by one request.
type group struct{ start, end int }

// maxVarbindsPerGet keeps a response inside a normal 1500 byte path. The PDU
// limit of 60 would fit, but 60 ifXTable counters answer with roughly 1.8 kB,
// and an agent with a small buffer replies tooBig rather than splitting it.
const maxVarbindsPerGet = 45

// batchInterfaces splits count interfaces into requests of at most maxOids
// varbinds, keeping every interface whole. A response then always completes
// entire rows, so the table never shows an interface half updated.
func batchInterfaces(count, cols, maxOids int) []group {
	if count == 0 {
		return nil
	}
	perBatch := 1
	if cols > 0 && maxOids >= cols {
		perBatch = maxOids / cols
	}

	groups := make([]group, 0, (count+perBatch-1)/perBatch)
	for start := 0; start < count; start += perBatch {
		groups = append(groups, group{start, min(start+perBatch, count)})
	}
	return groups
}

// interval prefers the agent's own clock, which is immune to the poller being
// late. A sysUpTime that went backwards means the agent restarted, so the
// previous counters are dropped rather than turned into a spike.
//
// The restart check runs against the newest uptime seen, the duration only
// against the poll that prev came from: with the uptime of a poll in between
// missing, the ticks would cover two intervals where the counters cover one,
// halving every rate. The wall clock is the honest answer in that round.
func (p *Poller) interval(uptime value, now time.Time) float64 {
	if uptime.ok && p.lastUptime.ok {
		if uptime.n < p.lastUptime.n {
			p.prev = map[int]counters{}
			return 0
		}
		// Only a clock that actually moved on can measure anything. Equal ticks
		// happen with -i 0 on a quick device, and for good on an agent whose
		// sysUpTime is stuck — returning 0 there would print "-" in every
		// single cell instead of falling through to the wall clock.
		if p.baseUptime.ok && uptime.n > p.baseUptime.n {
			// TimeTicks are hundredths of a second.
			return float64(uptime.n-p.baseUptime.n) / 100
		}
	}
	if p.lastPoll.IsZero() {
		return 0
	}
	return now.Sub(p.lastPoll).Seconds()
}

// buildRows renders the interface table from whatever has arrived so far. The
// caller must hold st.mu.
func (p *Poller) buildRows(st *state) []Row {
	rows := make([]Row, 0, len(st.ifaces))
	for i, ifc := range st.ifaces {
		// The agent answered, but denied knowing any of this interface's
		// counters: it has been removed since the last discovery.
		if i < len(st.done) && st.done[i] && st.raw[i].empty() {
			continue
		}

		row := Row{
			Index: ifc.index,
			Name:  ifc.name,
			Alias: ifc.alias,
		}

		// Still waiting on this interface: carry the previous poll's numbers
		// instead of blanking the row, and leave Fresh unset to say so.
		if i < len(st.done) && !st.done[i] {
			if old, ok := p.lastRows[ifc.index]; ok {
				row.InOctets, row.OutOctets = old.InOctets, old.OutOctets
				row.InPkts, row.OutPkts = old.InPkts, old.OutPkts
				row.InErrors = old.InErrors
			}
			rows = append(rows, row)
			continue
		}

		cur := st.raw[i]
		old, seen := p.prev[ifc.index]
		if seen {
			row.InOctets = rate(cur.inOct, old.inOct, width64, st.secs)
			row.OutOctets = rate(cur.outOct, old.outOct, width64, st.secs)
			row.InPkts = rate(cur.inPkt, old.inPkt, width32, st.secs)
			row.OutPkts = rate(cur.outPkt, old.outPkt, width32, st.secs)
			row.InErrors = rate(cur.errIn, old.errIn, width32, st.secs)
		}

		// A vendor that already reports a rate is more accurate than our own
		// difference and works on the very first poll.
		if v := scale(cur.secInOct, p.profile.SecValueFactor); v != nil {
			row.InOctets = v
		}
		if v := scale(cur.secOutOct, p.profile.SecValueFactor); v != nil {
			row.OutOctets = v
		}
		if v := scale(cur.secInPkt, 1); v != nil {
			row.InPkts = v
		}
		if v := scale(cur.secOutPkt, 1); v != nil {
			row.OutPkts = v
		}
		row.Fresh = true
		rows = append(rows, row)
	}
	return rows
}

// remember holds on to everything the next poll compares itself against.
func (p *Poller) remember(st *state, rows []Row, start time.Time) {
	counters := make(map[int]counters, len(st.ifaces))
	for i, ifc := range st.ifaces {
		// Only interfaces that answered may seed the next delta; a missing
		// reading would make the next rate wrong.
		if i < len(st.done) && st.done[i] {
			counters[ifc.index] = st.raw[i]
		}
	}
	p.prev = counters
	p.lastPoll = start

	// Only a request that came through is worth remembering. An empty value
	// from a request that worked is the truth and has to be shown as such,
	// otherwise a CPU OID that stops answering is indistinguishable from one
	// that still does.
	if st.scalarsOK {
		p.lastSysName = st.sysName
		p.lastReadings = st.readings
	}
	if st.cpuOK {
		p.lastCPU = st.cpu
	}
	if st.uptime.ok {
		p.lastUptime = st.uptime
	}
	p.baseUptime = st.uptime

	last := make(map[int]Row, len(rows))
	for _, r := range rows {
		last[r.Index] = r
	}
	p.lastRows = last
}

func evaluate(infos []*config.AddInfo, vars map[string]gosnmp.SnmpPDU) []Reading {
	out := make([]Reading, 0, len(infos))
	for _, info := range infos {
		pdu, ok := vars[info.OID]
		if !ok {
			continue
		}
		text, isErr, ok := info.Evaluate(pduString(pdu))
		if !ok {
			continue
		}
		out = append(out, Reading{Name: info.Name, Value: text, IsError: isErr})
	}
	return out
}

// getAll fetches scalars in as many GETs as the PDU limit requires and returns
// them keyed by the OID that was asked for. On error it returns what the
// earlier chunks did deliver, so a caller that can live without the rest does
// not have to throw those away too.
func getAll(c *gosnmp.GoSNMP, oids []string) (map[string]gosnmp.SnmpPDU, error) {
	out := make(map[string]gosnmp.SnmpPDU, len(oids))
	for _, batch := range chunk(oids, gosnmp.MaxOids) {
		res, err := c.Get(batch)
		if err == nil {
			err = responseError(res)
		}
		if err != nil {
			return out, err
		}
		for _, pdu := range res.Variables {
			out[trimOID(pdu.Name)] = pdu
		}
	}
	return out, nil
}

// walkColumn hands every value to fn as its GETBULK response arrives, rather
// than collecting the whole column first. On a device that takes minutes to
// walk, that is the difference between seeing the first ports immediately and
// staring at an empty table until the end.
func walkColumn(c *gosnmp.GoSNMP, base string, fn func(index int, value string)) error {
	err := c.BulkWalk(base, func(pdu gosnmp.SnmpPDU) error {
		if idx, ok := oidIndex(base, pdu.Name); ok {
			fn(idx, pduString(pdu))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("walk %s: %w", base, err)
	}
	return nil
}

// oidIndex extracts the table index from a walked OID, i.e. the part after the
// column's base OID.
func oidIndex(base, name string) (int, bool) {
	suffix := strings.TrimPrefix(trimOID(name), trimOID(base)+".")
	if suffix == trimOID(name) {
		return 0, false
	}
	idx, err := strconv.Atoi(suffix)
	if err != nil {
		return 0, false
	}
	return idx, true
}

// trimOID drops the leading dot gosnmp puts on returned OIDs.
func trimOID(oid string) string { return strings.TrimPrefix(oid, ".") }

func chunk(oids []string, size int) [][]string {
	if size <= 0 {
		size = 1
	}
	var out [][]string
	for i := 0; i < len(oids); i += size {
		out = append(out, oids[i:min(i+size, len(oids))])
	}
	return out
}

// parallel runs every task concurrently and joins their errors. Concurrency is
// bounded by the session pool the tasks borrow from.
// Warning renders an error as one line of display. Two reasons it cannot just
// be err.Error(): the info block gives every warning a single row, so the
// newlines errors.Join puts between its parts would be cut off — and against a
// mute agent that error carries one entry per request. Identical messages are
// counted instead of repeated, because fifty timeouts are one fact.
func Warning(prefix string, err error) string {
	var parts []error
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		parts = joined.Unwrap()
	} else {
		parts = []error{err}
	}

	seen := make(map[string]int, len(parts))
	var order []string
	for _, e := range parts {
		if e == nil {
			continue
		}
		msg := strings.Join(strings.Fields(e.Error()), " ")
		if seen[msg] == 0 {
			order = append(order, msg)
		}
		seen[msg]++
	}

	msgs := make([]string, 0, len(order))
	for _, msg := range order {
		if n := seen[msg]; n > 1 {
			msg = fmt.Sprintf("%s (x%d)", msg, n)
		}
		msgs = append(msgs, msg)
	}
	return prefix + ": " + strings.Join(msgs, "; ")
}

func parallel(tasks []func() error) error {
	var wg sync.WaitGroup
	errs := make([]error, len(tasks))
	for i, task := range tasks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = task()
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}
