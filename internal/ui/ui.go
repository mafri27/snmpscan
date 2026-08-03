// Package ui renders a poller's snapshots as a full screen table.
package ui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/mafri27/snmpscan/internal/poll"
	"github.com/rivo/tview"
)

const (
	colMark = iota
	colIfNr
	colName
	colIn
	colOut
	colInPps
	colOutPps
	colErrors
	colAlias
)

// Column widths carried over from the Ruby layout, each one short by the
// separator tview inserts, so the table lines up the same way. The alias has
// no width of its own — it takes the remaining space.
var columns = []struct {
	title string
	align int
	width int
}{
	colMark:   {"", tview.AlignLeft, 1},
	colIfNr:   {"ifNr", tview.AlignLeft, 9},
	colName:   {"Name", tview.AlignLeft, 29},
	colIn:     {"Mbps in", tview.AlignRight, 9},
	colOut:    {"Mbps out", tview.AlignRight, 9},
	colInPps:  {"Kpps in", tview.AlignRight, 9},
	colOutPps: {"Kpps out", tview.AlignRight, 9},
	colErrors: {"Errors", tview.AlignRight, 9},
	colAlias:  {"Alias", tview.AlignLeft, 0},
}

// warmupDelay caps how often a newly discovered interface may pull the next
// poll forward. An interval below it wins, so -i 0 stays pauseless.
const warmupDelay = 2 * time.Second

// freshMark flags a row whose values arrived during the current poll; a blank
// means the row is still showing what the previous poll found.
const (
	freshMark = "•"
	staleMark = " "
)

// The blank columns the table is inset by, so neither the alias nor a
// highlighted row runs into the edge of the terminal.
const (
	leftMargin  = 1
	rightMargin = 1
)

// redrawInterval decouples the screen from the poller: batches may land far
// faster than a terminal can usefully repaint.
const redrawInterval = 200 * time.Millisecond

// UI drives the terminal application.
type UI struct {
	app    *tview.Application
	header *tview.TextView
	info   *tview.TextView
	table  *tview.Table
	status *tview.TextView
	body   *tview.Flex

	poller    *poll.Poller
	host      string
	interval  time.Duration
	discovery time.Duration
	refresh   chan struct{}

	mu       sync.Mutex
	snap     *poll.Snapshot
	err      error
	nextPoll time.Time
	width    int
	dirty    bool
	sort     sortOrder
	order    map[int]int
}

// New builds the widget tree.
func New(p *poll.Poller, host string, interval, discovery time.Duration) *UI {
	u := &UI{
		app:       tview.NewApplication(),
		header:    tview.NewTextView().SetDynamicColors(true),
		info:      tview.NewTextView().SetDynamicColors(true),
		table:     tview.NewTable(),
		status:    tview.NewTextView().SetDynamicColors(true),
		poller:    p,
		host:      host,
		interval:  interval,
		discovery: discovery,
		// Buffered so pressing r never blocks the input handler.
		refresh: make(chan struct{}, 1),
		sort:    defaultOrder(SortName),
	}

	u.table.SetFixed(1, 0).SetSelectable(true, false).SetSeparator(' ')
	u.drawStatus()

	table := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(tview.NewBox(), leftMargin, 0, false).
		AddItem(u.table, 0, 1, true).
		AddItem(tview.NewBox(), rightMargin, 0, false)

	u.body = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(tview.NewBox(), 1, 0, false). // blank line above everything
		AddItem(u.header, 1, 0, false).
		AddItem(u.info, 0, 0, false).
		AddItem(tview.NewBox(), 1, 0, false). // blank line before the table
		AddItem(table, 0, 1, true).
		AddItem(u.status, 1, 0, false)

	u.app.SetInputCapture(u.onKey)
	// The column layout depends on the terminal width, which is only known
	// once there is a screen. Reformat on resize instead of waiting for the
	// next poll.
	u.app.SetBeforeDrawFunc(func(screen tcell.Screen) bool {
		w, _ := screen.Size()
		u.mu.Lock()
		changed := w != u.width
		u.width = w
		u.mu.Unlock()
		if changed {
			u.drawTable()
		}
		// The footer shows the visible range, which scrolling changes without
		// going through any of our own redraw paths.
		u.drawStatus()
		return false
	})
	return u
}

func (u *UI) onKey(ev *tcell.EventKey) *tcell.EventKey {
	switch ev.Key() {
	case tcell.KeyEsc, tcell.KeyCtrlC:
		u.app.Stop()
		return nil
	case tcell.KeyRune:
		switch ev.Rune() {
		case 'q', 'Q':
			u.app.Stop()
			return nil
		case 'r', 'R':
			select {
			case u.refresh <- struct{}{}:
			default:
			}
			return nil
		case 'n', 'N':
			u.setSort(SortName)
			return nil
		case 't', 'T':
			u.setSort(SortTraffic)
			return nil
		case 'e', 'E':
			u.setSort(SortErrors)
			return nil
		case 'a', 'A':
			u.setSort(SortAlias)
			return nil
		}
	}
	return ev
}

func (u *UI) setSort(key SortKey) {
	u.mu.Lock()
	if u.sort.key == key {
		// Same key again reverses the direction. The remembered ranking turns
		// over with it so an unstable sort flips right away.
		u.sort = u.sort.reversed()
		u.order = reverseRanking(u.order)
	} else {
		u.sort = defaultOrder(key)
		// A new unstable key has no ranking until a poll finishes; drop the
		// old one rather than keeping a meaningless arrangement.
		u.order = nil
	}
	u.mu.Unlock()

	u.drawStatus()
	u.drawTable()
}

// Run starts the poll loop and blocks until the user quits.
func (u *UI) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	u.drawHeader()
	go u.pollLoop(ctx)
	go u.discoverLoop(ctx)
	go u.redrawLoop(ctx)

	return u.app.SetRoot(u.body, true).Run()
}

func (u *UI) pollLoop(ctx context.Context) {
	for {
		snap, err := u.poller.Poll(ctx, u.onPartial)
		if ctx.Err() != nil {
			return
		}

		u.mu.Lock()
		// Keep the last good reading on screen when a poll fails, so a single
		// lost packet does not blank the table.
		if snap != nil {
			u.snap = snap
		}
		u.err = err
		u.nextPoll = time.Now().Add(u.interval)
		u.dirty = true
		u.mu.Unlock()

		select {
		case <-time.After(u.interval):
		case <-u.refresh:
		case <-ctx.Done():
			return
		case <-u.poller.Added():
			// The first discovery hands over its interfaces one at a time.
			// Sitting out the interval would leave the six ports found so far
			// on screen while the other six hundred arrive unpolled, so cut
			// the wait short — but not shorter than warmupDelay, or the polls
			// would run back to back for the whole walk.
			if !u.waitBefore(ctx, min(u.interval, warmupDelay)) {
				return
			}
		}
	}
}

// waitBefore holds the next poll back for d, keeping the countdown in the
// header honest. It reports whether polling should carry on.
func (u *UI) waitBefore(ctx context.Context, d time.Duration) bool {
	u.mu.Lock()
	u.nextPoll = time.Now().Add(d)
	u.dirty = true
	u.mu.Unlock()

	select {
	case <-time.After(d):
	case <-u.refresh:
	case <-ctx.Done():
		return false
	}
	return true
}

// discoverLoop keeps the interface list up to date, independently of the
// value polls. It publishes ports as it finds them, so on a device that takes
// minutes to walk the table fills in along the way instead of staying empty
// until the end.
func (u *UI) discoverLoop(ctx context.Context) {
	for {
		// Runs straight away, and again once the interval has passed since it
		// finished — an interval of zero means back to back. A walk that
		// outlasts the interval simply starts the next one late.
		if err := u.poller.Discover(ctx); err != nil && ctx.Err() == nil {
			u.mu.Lock()
			u.err = err
			u.mu.Unlock()
		}
		// A negative interval means one pass only. It still has to happen, or
		// there would be no interfaces to poll at all.
		if u.discovery < 0 {
			return
		}
		select {
		case <-time.After(u.discovery):
		case <-ctx.Done():
			return
		}
	}
}

// onPartial runs on the poller's goroutines. It only parks the snapshot —
// redrawLoop picks it up — so a slow terminal can never back pressure into
// the SNMP code.
func (u *UI) onPartial(s *poll.Snapshot) {
	u.mu.Lock()
	u.snap = s
	u.dirty = true
	u.mu.Unlock()
}

// redrawLoop repaints the header every tick for the countdown, and the table
// only when new data has arrived.
func (u *UI) redrawLoop(ctx context.Context) {
	t := time.NewTicker(redrawInterval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			u.mu.Lock()
			dirty := u.dirty
			u.dirty = false
			u.mu.Unlock()

			u.app.QueueUpdateDraw(func() {
				u.drawHeader()
				if dirty {
					u.drawReadings()
					u.drawTable()
				}
			})
		case <-ctx.Done():
			return
		}
	}
}

func (u *UI) drawHeader() {
	u.mu.Lock()
	snap, err, next := u.snap, u.err, u.nextPoll
	u.mu.Unlock()

	var b strings.Builder
	fmt.Fprintf(&b, " [::b]System:[::-] %s", u.host)
	if snap != nil {
		fmt.Fprintf(&b, "   [::b]Sysname:[::-] %s", tview.Escape(snap.SysName))
		if snap.CPU != "" {
			fmt.Fprintf(&b, "   [::b]CPU:[::-] %s", tview.Escape(snap.CPU))
		}
	}
	// The port count comes from the poller, not the snapshot: a snapshot mid
	// poll and one just finished would otherwise alternate between showing it
	// and not, which reads as flicker.
	fmt.Fprintf(&b, "   [gray]%d ports[-]", u.poller.Interfaces())

	switch left := int(time.Until(next).Round(time.Second).Seconds()); {
	case err != nil:
		fmt.Fprintf(&b, "   [red]%s[-]", tview.Escape(err.Error()))
	case next.IsZero() || left <= 0:
		b.WriteString("   [gray]polling…[-]")
	default:
		fmt.Fprintf(&b, "   [gray]reload in %ds[-]", left)
	}
	if snap != nil {
		fmt.Fprintf(&b, "   [gray](%d req, %dms)[-]", snap.Requests, snap.Elapsed.Milliseconds())
	}
	u.header.SetText(b.String())
}

var sortKeys = []struct {
	key  SortKey
	rune rune
}{
	{SortName, 'n'},
	{SortTraffic, 't'},
	{SortErrors, 'e'},
	{SortAlias, 'a'},
}

func (u *UI) drawStatus() {
	u.mu.Lock()
	active := u.sort
	u.mu.Unlock()

	// Attributes carry over from one tag to the next, so every section turns
	// off what it does not want. Note the capital U: tview's "-" resets the
	// attribute mask, but underline is held separately and survives it.
	const off = "::BU"
	choices := make([]string, 0, len(sortKeys))
	for _, s := range sortKeys {
		if s.key == active.key {
			choices = append(choices, fmt.Sprintf("[white::bu]%c", s.rune))
		} else {
			choices = append(choices, fmt.Sprintf("[gray%s]%c", off, s.rune))
		}
	}

	u.status.SetText(fmt.Sprintf(
		" [gray%[1]s]q[white%[1]s] quit   [gray%[1]s]r[white%[1]s] refresh   sort %[2]s [white%[1]s]%[3]s"+
			"   [gray%[1]s]↑↓ PgUp PgDn[white%[1]s] scroll %[4]s",
		off, strings.Join(choices, fmt.Sprintf("[gray%s]/", off)), arrow(active.desc),
		u.visibleRange()))
}

// visibleRange says which slice of the table is on screen. Sorted by traffic
// with ten busier ports scrolled off the top, the rows on screen look like the
// whole story otherwise.
func (u *UI) visibleRange() string {
	total := u.table.GetRowCount() - 1 // less the header
	if total <= 0 {
		return ""
	}
	offRow, _ := u.table.GetOffset()
	_, _, _, height := u.table.GetInnerRect()

	first := offRow + 1
	last := min(offRow+height-1, total)
	if first > last {
		return ""
	}
	if first == 1 && last == total {
		return fmt.Sprintf("[gray::B]all %d", total)
	}
	return fmt.Sprintf("[gray::B]%d-%d of %d", first, last, total)
}

func (u *UI) drawReadings() {
	u.mu.Lock()
	snap := u.snap
	u.mu.Unlock()
	if snap == nil {
		return
	}

	lines := make([]string, 0, len(snap.Readings)+len(snap.Warnings))
	for _, r := range snap.Readings {
		name := fmt.Sprintf(" %-28s %s", tview.Escape(r.Name), tview.Escape(r.Value))
		if r.IsError {
			name = "[red]" + name + "[-]"
		}
		lines = append(lines, name)
	}
	for _, w := range snap.Warnings {
		lines = append(lines, "[yellow] "+tview.Escape(w)+"[-]")
	}

	u.info.SetText(strings.Join(lines, "\n"))
	u.body.ResizeItem(u.info, len(lines), 0)
}

func (u *UI) drawTable() {
	u.mu.Lock()
	snap, width, order := u.snap, u.width, u.sort
	if snap == nil {
		u.mu.Unlock()
		return
	}
	rows, ranks := sortRows(snap.Rows, order, snap.Complete, u.order)
	u.order = ranks
	u.mu.Unlock()

	thresholds := u.poller.Profile().Thresholds
	cells := make([][]string, len(rows))
	styles := make([]Style, len(rows))
	for i, row := range rows {
		cells[i] = rowValues(row)
		styles[i] = classify(row, thresholds)
	}
	titles := headerTitles(order)
	widths := layout(titles, cells, width-leftMargin-rightMargin)

	// Keep the viewport where the user left it; a refresh must not scroll the
	// list back to the top.
	selRow, selCol := u.table.GetSelection()
	offRow, offCol := u.table.GetOffset()
	selected := selectedIndex(u.table, selRow)

	u.table.Clear()
	sorted := sortedBy(order.key)
	for c, col := range columns {
		colour := tcell.ColorTeal
		if slices.Contains(sorted, c) {
			colour = tcell.ColorWhite
		}
		u.table.SetCell(0, c, tview.NewTableCell(tview.Escape(pad(titles[c], widths[c], col.align))).
			SetTextColor(colour).
			SetAttributes(tcell.AttrBold).
			SetAlign(col.align).
			SetSelectable(false).
			SetExpansion(expansion(c)))
	}

	for i, values := range cells {
		colour := styleColor(styles[i])
		for c, v := range values {
			cell := tview.NewTableCell(tview.Escape(pad(v, widths[c], columns[c].align))).
				SetTextColor(colour).
				SetSelectedStyle(selectedStyle(colour)).
				SetAlign(columns[c].align).
				SetExpansion(expansion(c))
			if c == colMark {
				// What the row is, as opposed to where it sits — this is how
				// the selection finds its interface again after a re-sort.
				cell.SetReference(rows[i].Index)
			}
			u.table.SetCell(i+1, c, cell)
		}
	}

	if n := u.table.GetRowCount(); selRow >= n {
		selRow = max(n-1, 0)
	}
	// Following the interface must not drag the viewport along: tview scrolls
	// to wherever the cursor is and SetOffset cannot talk it out of that. So
	// the cursor only follows while its row stays on screen.
	_, _, _, height := u.table.GetInnerRect()
	if next := rowOf(rows, selected, selRow); onScreen(next, offRow, height) {
		selRow = next
	}
	u.table.Select(selRow, selCol)
	u.table.SetOffset(offRow, offCol)
}

// selectedIndex is the ifIndex of the row the cursor is on, or -1 when there
// is nothing selected yet.
func selectedIndex(t *tview.Table, row int) int {
	cell := t.GetCell(row, colMark)
	if cell == nil {
		return -1
	}
	index, ok := cell.GetReference().(int)
	if !ok {
		return -1
	}
	return index
}

// onScreen reports whether a table row is within the visible window. Row 0 is
// the header, which SetFixed keeps in place; the rest scroll by offRow.
func onScreen(row, offRow, height int) bool {
	return row > offRow && row < offRow+height
}

// rowOf is where the interface the cursor was on has ended up. A sort moves
// rows around underneath the cursor, and following the interface is what the
// user means by "this row" — not the third line from the top. An interface
// that has since gone leaves the cursor where it was.
func rowOf(rows []poll.Row, index, fallback int) int {
	if index < 0 {
		return fallback
	}
	if i := slices.IndexFunc(rows, func(r poll.Row) bool { return r.Index == index }); i >= 0 {
		return i + 1 // + the header
	}
	return fallback
}

func rowValues(row poll.Row) []string {
	mark := staleMark
	if row.Fresh {
		mark = freshMark
	}
	return []string{
		colMark:   mark,
		colIfNr:   formatIndex(row.Index),
		colName:   row.Name,
		colIn:     formatRate(row.InOctets),
		colOut:    formatRate(row.OutOctets),
		colInPps:  formatPps(row.InPkts),
		colOutPps: formatPps(row.OutPkts),
		colErrors: formatCount(row.InErrors),
		colAlias:  row.Alias,
	}
}

// headerTitles marks the column the table is ordered by, with the arrow
// pointing the way the values run.
func headerTitles(o sortOrder) []string {
	titles := make([]string, len(columns))
	for i, c := range columns {
		titles[i] = c.title
	}
	for _, c := range sortedBy(o.key) {
		titles[c] += " " + arrow(o.desc)
	}
	return titles
}

func arrow(desc bool) string {
	if desc {
		return "▼"
	}
	return "▲"
}

// expansion lets the alias soak up the leftover width instead of the fixed
// 115-column arithmetic the Ruby version used.
func expansion(col int) int {
	if col == len(columns)-1 {
		return 1
	}
	return 0
}

func styleColor(s Style) tcell.Color {
	switch s {
	case StyleDim:
		return tcell.ColorGray
	case StyleAlert:
		return tcell.ColorRed
	case StyleMarked:
		return tcell.ColorYellow
	default:
		return tcell.ColorDefault
	}
}

// selectedStyle paints the cursor row as its own colour reversed. tview's
// default keeps the cell's foreground and only swaps the background, which
// leaves a dim grey row as near-black text on a dark bar.
func selectedStyle(fg tcell.Color) tcell.Style {
	if fg == tcell.ColorDefault {
		fg = tcell.ColorSilver
	}
	return tcell.StyleDefault.Background(fg).Foreground(tcell.ColorBlack)
}
