package ui

import (
	"testing"

	"github.com/mafri27/snmpscan/internal/config"
	"github.com/mafri27/snmpscan/internal/poll"
	"github.com/rivo/tview"
)

// mbitToBytes is the inverse of Mbit, so the tests can talk in Mbit/s.
func mbitToBytes(mbit int64) int64 { return mbit * 1_000_000 / 8 }

func TestMbit(t *testing.T) {
	if got := Mbit(mbitToBytes(100)); got != 100 {
		t.Errorf("Mbit round trip = %d, want 100", got)
	}
	// A gigabit port at line rate must read 1000, not 953.
	if got := Mbit(125_000_000); got != 1000 {
		t.Errorf("Mbit(125e6) = %d, want 1000", got)
	}
	// Below one Mbit the integer division floors to zero, as it always has.
	if got := Mbit(1000); got != 0 {
		t.Errorf("Mbit(1000) = %d, want 0", got)
	}
}

func TestClassify(t *testing.T) {
	th := config.DefaultThresholds()

	tests := []struct {
		name string
		row  poll.Row
		want Style
	}{
		{"idle interface", row(0, 0, 0, 0, 0), StyleDim},
		{"no data at all", poll.Row{}, StyleDim},
		{"just below the dim limit", row(mbitToBytes(5), 0, 0, 0, 0), StyleDim},
		{"busy but harmless", row(mbitToBytes(100), 0, 0, 0, 0), StyleNormal},
		{"packets alone lift it out of dim", row(0, 0, 101, 0, 0), StyleNormal},
		{"over the alert rate", row(mbitToBytes(701), 0, 0, 0, 0), StyleAlert},
		{"outgoing counts too", row(0, mbitToBytes(701), 0, 0, 0), StyleAlert},
		{"packet flood", row(0, 0, 0, 100001, 0), StyleAlert},
		{"a single error is enough", row(0, 0, 0, 0, 1), StyleAlert},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.row, th); got != tc.want {
				t.Errorf("classify = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMarkedWinsOverAlert(t *testing.T) {
	r := row(mbitToBytes(900), 0, 0, 0, 0)
	r.Marked = true
	if got := classify(r, config.DefaultThresholds()); got != StyleMarked {
		t.Errorf("classify = %v, want the mark to win", got)
	}
}

func TestClassifyHonoursOverriddenThresholds(t *testing.T) {
	th := config.DefaultThresholds()
	high := int64(9000)
	th.Alert.Mbit = &high

	if got := classify(row(mbitToBytes(900), 0, 0, 0, 0), th); got != StyleNormal {
		t.Errorf("classify = %v, want normal below the raised limit", got)
	}
}

func TestFormatters(t *testing.T) {
	// Bare figures: the unit is in the column heading.
	v := int64(mbitToBytes(42))
	if got := formatRate(&v); got != "42" {
		t.Errorf("formatRate = %q", got)
	}
	if got := formatRate(nil); got != "-" {
		t.Errorf("formatRate(nil) = %q, want -", got)
	}

	// Packet rates are shown in thousands, decimal like the Mbit/s beside them.
	p := int64(399_670)
	if got := formatPps(&p); got != "399" {
		t.Errorf("formatPps = %q, want 399", got)
	}
	// A quiet port floors to zero, the same way a sub-Mbit rate does.
	q := int64(543)
	if got := formatPps(&q); got != "0" {
		t.Errorf("formatPps(543) = %q, want 0", got)
	}
	if got := formatCount(nil); got != "-" {
		t.Errorf("formatCount(nil) = %q, want -", got)
	}
}

func TestFormatIndex(t *testing.T) {
	if got := formatIndex(517); got != "517" {
		t.Errorf("got %q", got)
	}
	// Juniper logical interfaces have huge indexes; they used to push the
	// columns apart.
	if got := formatIndex(1234567890); got != "..4567890" {
		t.Errorf("got %q, want ..4567890", got)
	}
}

func TestLayoutKeepsFixedWidthsWhenTheAliasFits(t *testing.T) {
	cells := [][]string{{"•", "1", "xe-0/0/0", "5", "5", "1", "1", "0", "uplink"}}

	got := layout(plainTitles(), cells, 200)

	for i, col := range columns[:len(columns)-1] {
		if got[i] != col.width {
			t.Errorf("column %q = %d, want the fixed %d", col.title, got[i], col.width)
		}
	}
	if got[len(got)-1] != 0 {
		t.Errorf("alias width = %d, want 0 so it takes the rest", got[len(got)-1])
	}
}

// A terminal ten characters too narrow for the alias: the columns have to give
// way rather than let it be cut off. Short interface names must not sit in a 29
// character hole while that happens.
func TestLayoutFreesRoomBeforeCuttingTheAlias(t *testing.T) {
	const alias = "hel1-cloud1-spine3_rdev1 - 1"
	cells := [][]string{
		{"•", "1069", "et-0/0/100", "2802", "5353", "282", "467", "0", alias},
		{"•", "519", "et-0/0/101", "3037", "4901", "295", "429", "0", alias},
	}

	total := usedWidth(layout(plainTitles(), cells, 10_000)) + len(alias) - 10
	got := layout(plainTitles(), cells, total)

	if left := total - usedWidth(got); left < len(alias) {
		t.Errorf("%d characters left for a %d character alias", left, len(alias))
	}
	// The name column has the most space going unused, so it has to give up
	// the most of it.
	freed := columns[colName].width - got[colName]
	if other := columns[colIfNr].width - got[colIfNr]; freed <= other {
		t.Errorf("name gave up %d, ifNr %d — the emptiest column should give most", freed, other)
	}
	// Never below what the column actually holds.
	if want := len("et-0/0/100"); got[colName] < want {
		t.Errorf("name column = %d, narrower than its widest entry %d", got[colName], want)
	}
}

func TestLayoutShrinksOnlyAsFarAsTheAliasNeeds(t *testing.T) {
	cells := [][]string{{"•", "1", "xe-0/0/0", "5", "5", "1", "1", "0", "uplink"}}

	// Ten characters short of what the layout wants when space is plentiful.
	roomy := usedWidth(layout(plainTitles(), cells, 10_000))
	total := roomy + minAliasWidth - 10
	got := layout(plainTitles(), cells, total)

	if used := usedWidth(got); used+minAliasWidth > total {
		t.Errorf("columns use %d of %d, leaving less than %d for the alias", used, total, minAliasWidth)
	}
	// Only the missing ten characters may go, not everything down to the
	// content width.
	if want := roomy - 10; usedWidth(got) != want {
		t.Errorf("columns use %d, want exactly %d", usedWidth(got), want)
	}
	for i, col := range columns[:len(columns)-1] {
		if got[i] > col.width {
			t.Errorf("column %q = %d, wider than the fixed %d", col.title, got[i], col.width)
		}
	}
}

func TestLayoutBottomsOutAtTheContentWidth(t *testing.T) {
	cells := [][]string{{"•", "1", "xe-0/0/0", "5", "5", "1", "1", "0", "uplink"}}

	got := layout(plainTitles(), cells, 40)

	// Nothing may shrink past what its widest entry needs, header included.
	for i, col := range columns[:len(columns)-1] {
		if want := contentWidth(plainTitles(), cells, i); got[i] != want {
			t.Errorf("column %q = %d, want its content width %d", col.title, got[i], want)
		}
	}
}

// plainTitles is the header without any sort arrow.
func plainTitles() []string {
	out := make([]string, len(columns))
	for i, c := range columns {
		out[i] = c.title
	}
	return out
}

func usedWidth(widths []int) int {
	n := 0
	for _, w := range widths[:len(widths)-1] {
		n += w + 1
	}
	return n
}

func TestLayoutWithoutAKnownWidth(t *testing.T) {
	// Before the first draw the terminal size is unknown; the fixed layout is
	// the right guess.
	got := layout(plainTitles(), nil, 0)
	if got[0] != columns[0].width {
		t.Errorf("column 0 = %d, want the fixed %d", got[0], columns[0].width)
	}
}

func TestPad(t *testing.T) {
	if got := pad("xe-0/0/0", 12, tview.AlignLeft); got != "xe-0/0/0    " {
		t.Errorf("pad left = %q", got)
	}
	if got := pad("5 Mbit/s", 12, tview.AlignRight); got != "    5 Mbit/s" {
		t.Errorf("pad right = %q", got)
	}
	if got := pad("a very long interface name", 8, tview.AlignLeft); got != "a very l" {
		t.Errorf("pad truncation = %q", got)
	}
	if got := pad("free", 0, tview.AlignLeft); got != "free" {
		t.Errorf("width 0 must not pad, got %q", got)
	}
}

func row(in, out, inPps, outPps, errs int64) poll.Row {
	return poll.Row{
		InOctets: &in, OutOctets: &out,
		InPkts: &inPps, OutPkts: &outPps,
		InErrors: &errs,
	}
}
