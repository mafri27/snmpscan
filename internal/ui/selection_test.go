package ui

import (
	"testing"

	"github.com/mafri27/snmpscan/internal/poll"
)

// The cursor is on an interface, not on a line number: re-sorting must carry
// it along rather than leave it pointing at whatever slid into that slot.
func TestCursorFollowsItsInterface(t *testing.T) {
	rows := []poll.Row{{Index: 7}, {Index: 3}, {Index: 9}}

	if got := rowOf(rows, 9, 1); got != 3 {
		t.Errorf("cursor on ifIndex 9 landed on row %d, want 3", got)
	}
	// Nothing selected yet.
	if got := rowOf(rows, -1, 2); got != 2 {
		t.Errorf("got %d, want the row it was on", got)
	}
	// The interface is gone — a removed port must not throw the cursor to the
	// top of the table.
	if got := rowOf(rows, 42, 2); got != 2 {
		t.Errorf("got %d, want the row it was on", got)
	}
}

// tview scrolls the viewport to wherever the cursor is, so the cursor may only
// follow its interface while that stays visible. Otherwise a re-sort would
// yank the table to some row far down the list.
func TestCursorStaysPutWhenItsRowScrollsOff(t *testing.T) {
	const height = 10 // header plus nine data rows

	for _, tc := range []struct {
		name   string
		row    int
		offRow int
		want   bool
	}{
		{"first visible row", 1, 0, true},
		{"last visible row", 9, 0, true},
		{"one past the bottom", 10, 0, false},
		{"scrolled: row above the window", 5, 5, false},
		{"scrolled: first visible row", 6, 5, true},
		{"scrolled: last visible row", 14, 5, true},
		{"scrolled: one past the bottom", 15, 5, false},
		{"the header is never the cursor", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := onScreen(tc.row, tc.offRow, height); got != tc.want {
				t.Errorf("onScreen(%d, %d, %d) = %v, want %v",
					tc.row, tc.offRow, height, got, tc.want)
			}
		})
	}
}
