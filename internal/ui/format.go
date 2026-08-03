package ui

import (
	"strconv"
	"strings"

	"github.com/mafri27/snmpscan/internal/config"
	"github.com/mafri27/snmpscan/internal/poll"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"
)

// Style is how an interface row is painted.
type Style int

const (
	StyleNormal Style = iota
	StyleDim
	StyleAlert
)

// Mbit converts a byte rate to the figure shown in the table. Decimal, the way
// interface speeds are quoted — dividing by 1024 would report Mibit/s under a
// Mbit/s label, about 5% low.
func Mbit(bytesPerSec int64) int64 { return bytesPerSec * 8 / 1_000_000 }

// Kpps is the packet rate as shown, decimal like the Mbit/s figure beside it.
// Thresholds stay in whole packets — a limit of 100 pps is still 100 pps, even
// where the column rounds it down to 0.
func Kpps(packetsPerSec int64) int64 { return packetsPerSec / 1000 }

// classify picks a row's colour: an alert wins over a quiet row.
func classify(r poll.Row, t config.Thresholds) Style {
	switch {
	case isAlert(r, t.Alert):
		return StyleAlert
	case isDim(r, t.Dim):
		return StyleDim
	default:
		return StyleNormal
	}
}

// isDim reports whether every rate stays below the "boring" limits. An absent
// value counts as boring.
func isDim(r poll.Row, l config.Level) bool {
	if over(r.InOctets, l.Mbit, Mbit) || over(r.OutOctets, l.Mbit, Mbit) {
		return false
	}
	if over(r.InPkts, l.Pps, identity) || over(r.OutPkts, l.Pps, identity) {
		return false
	}
	return true
}

func isAlert(r poll.Row, l config.Level) bool {
	if over(r.InOctets, l.Mbit, Mbit) || over(r.OutOctets, l.Mbit, Mbit) {
		return true
	}
	if over(r.InPkts, l.Pps, identity) || over(r.OutPkts, l.Pps, identity) {
		return true
	}
	if l.Errors != nil && r.InErrors != nil && *r.InErrors >= *l.Errors {
		return true
	}
	return false
}

func over(v, limit *int64, conv func(int64) int64) bool {
	return v != nil && limit != nil && conv(*v) > *limit
}

func identity(v int64) int64 { return v }

// formatRate renders a byte rate as a bare Mbit/s figure, or "-" when
// unavailable. The unit lives in the column heading, not in every cell.
func formatRate(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(Mbit(*v), 10)
}

func formatPps(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(Kpps(*v), 10)
}

func formatCount(v *int64) string {
	if v == nil {
		return "-"
	}
	return strconv.FormatInt(*v, 10)
}

// formatIndex keeps a long ifIndex from pushing the table apart.
func formatIndex(i int) string {
	s := strconv.Itoa(i)
	if len(s) > 7 {
		return ".." + s[len(s)-7:]
	}
	return s
}

// minAliasWidth is what the alias column is granted even when nothing is in
// it, so a device without aliases does not lay out as if the column were gone.
const minAliasWidth = 12

// layout returns the display width of every column. The fixed widths are kept
// as long as the alias fits beside them. Only when it would be cut off do the columns give way, and then only by
// as much as the alias is short, taken from wherever the most space is going
// unused — they do not collapse onto their content all at once.
// A width of zero means "take whatever is left", which is the alias column.
func layout(titles []string, cells [][]string, total int) []int {
	fixedCols := len(columns) - 1
	widths := make([]int, len(columns))

	need := max(contentWidth(titles, cells, len(columns)-1), minAliasWidth)
	for i := range fixedCols {
		widths[i] = columns[i].width
		need += widths[i] + 1 // + the separator tview draws between columns
	}
	if total <= 0 || need <= total {
		return widths
	}

	content := make([]int, fixedCols)
	slack := make([]int, fixedCols)
	totalSlack := 0
	for i := range fixedCols {
		content[i] = min(contentWidth(titles, cells, i), widths[i])
		slack[i] = widths[i] - content[i]
		totalSlack += slack[i]
	}
	if totalSlack == 0 {
		return widths
	}

	deficit := min(need-total, totalSlack)
	taken := 0
	for i := range fixedCols {
		t := slack[i] * deficit / totalSlack
		widths[i] -= t
		taken += t
	}
	// Integer division leaves a remainder of less than one column; take it one
	// character at a time. Bounded so a miscalculation cannot hang the redraw.
	for i := 0; i < fixedCols && taken < deficit; i++ {
		if widths[i] > content[i] {
			widths[i]--
			taken++
		}
	}
	return widths
}

// contentWidth is the widest value in a column, the header included. The
// header comes in as a parameter because it carries the sort arrow.
func contentWidth(titles []string, cells [][]string, col int) int {
	w := visibleLen(titles[col])
	for _, row := range cells {
		w = max(w, visibleLen(row[col]))
	}
	return w
}

// pad fits s into width, truncating what does not fit. Called before
// tview.Escape, so the string still has its visible length.
func pad(s string, width, align int) string {
	if width <= 0 {
		return s
	}
	n := visibleLen(s)
	if n > width {
		return runewidth.Truncate(s, width, "")
	}
	gap := strings.Repeat(" ", width-n)
	if align == tview.AlignRight {
		return gap + s
	}
	return s + gap
}

// visibleLen is how many terminal columns a string occupies, which is not the
// same as how many characters it has: CJK and emoji take two. Counting runes
// would lay the columns out too narrow and skew every row below.
func visibleLen(s string) int { return runewidth.StringWidth(s) }
