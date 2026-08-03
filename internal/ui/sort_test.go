package ui

import (
	"reflect"
	"testing"

	"github.com/mafri27/snmpscan/internal/poll"
)

func TestNatCompare(t *testing.T) {
	// The whole point: plain string order puts xe-1/0/10 before xe-1/0/2.
	ordered := []string{"ge-0/0/0", "xe-1/0/0", "xe-1/0/2", "xe-1/0/9", "xe-1/0/10", "xe-1/0/48", "xe-2/0/0"}
	for i := 1; i < len(ordered); i++ {
		if natCompare(ordered[i-1], ordered[i]) >= 0 {
			t.Errorf("%q should sort before %q", ordered[i-1], ordered[i])
		}
	}
	if natCompare("xe-0/0/1", "xe-0/0/1") != 0 {
		t.Error("equal names must compare equal")
	}
	if natCompare("et-0/0/101", "et-0/0/101.100") >= 0 {
		t.Error("a bare port sorts before its logical units")
	}
	// Breakout ports.
	if natCompare("xe-0/0/48:0", "xe-0/0/48:10") >= 0 {
		t.Error("breakout subports must compare numerically too")
	}
}

func TestSortByName(t *testing.T) {
	rows := []poll.Row{
		{Index: 3, Name: "xe-1/0/10"},
		{Index: 1, Name: "xe-1/0/2"},
		{Index: 2, Name: "et-0/0/1"},
	}

	got, _ := sortRows(rows, defaultOrder(SortName), true, nil)

	want := []string{"et-0/0/1", "xe-1/0/2", "xe-1/0/10"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("row %d = %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestSortByTrafficNeedsACompletePoll(t *testing.T) {
	rows := []poll.Row{
		{Index: 1, Name: "xe-0/0/1", InOctets: i64(100), OutOctets: i64(0)},
		{Index: 2, Name: "xe-0/0/2", InOctets: i64(900), OutOctets: i64(0)},
		{Index: 3, Name: "xe-0/0/3", InOctets: i64(500), OutOctets: i64(0)},
	}

	// A complete poll ranks by traffic and hands back the ranking.
	got, order := sortRows(rows, defaultOrder(SortTraffic), true, nil)
	if got[0].Index != 2 || got[1].Index != 3 || got[2].Index != 1 {
		t.Fatalf("order = %d/%d/%d, want 2/3/1", got[0].Index, got[1].Index, got[2].Index)
	}
	if len(order) != 3 {
		t.Fatalf("ranking has %d entries, want 3", len(order))
	}

	// Mid-poll the values change wildly as batches land. The rows must hold
	// still, or the cursor lands on a different port than the user aimed at.
	shuffled := []poll.Row{
		{Index: 1, Name: "xe-0/0/1", InOctets: i64(9999)},
		{Index: 2, Name: "xe-0/0/2"},
		{Index: 3, Name: "xe-0/0/3"},
	}
	got, _ = sortRows(shuffled, defaultOrder(SortTraffic), false, order)
	if got[0].Index != 2 || got[1].Index != 3 || got[2].Index != 1 {
		t.Errorf("order = %d/%d/%d, want the previous 2/3/1", got[0].Index, got[1].Index, got[2].Index)
	}
}

func TestSortPlacesNewInterfacesLast(t *testing.T) {
	order := map[int]int{1: 0, 2: 1}
	rows := []poll.Row{
		{Index: 9, Name: "xe-0/0/9"},
		{Index: 2, Name: "xe-0/0/2"},
		{Index: 1, Name: "xe-0/0/1"},
	}

	got, _ := sortRows(rows, defaultOrder(SortTraffic), false, order)

	if got[2].Index != 9 {
		t.Errorf("last row = %d, want the unranked interface 9", got[2].Index)
	}
}

func TestSortByErrors(t *testing.T) {
	rows := []poll.Row{
		{Index: 1, Name: "xe-0/0/1"},
		{Index: 2, Name: "xe-0/0/2", InErrors: i64(7)},
	}

	got, _ := sortRows(rows, defaultOrder(SortErrors), true, nil)

	if got[0].Index != 2 {
		t.Errorf("first row = %d, want the erroring interface 2", got[0].Index)
	}
}

func TestSortByAliasIsStable(t *testing.T) {
	rows := []poll.Row{
		{Index: 1, Name: "xe-0/0/1", Alias: "zulu"},
		{Index: 2, Name: "xe-0/0/2", Alias: "alpha"},
		{Index: 3, Name: "xe-0/0/3", Alias: ""},
	}

	// Alias does not depend on counters, so it applies mid-poll as well.
	got, _ := sortRows(rows, defaultOrder(SortAlias), false, nil)

	if got[0].Alias != "" || got[1].Alias != "alpha" || got[2].Alias != "zulu" {
		t.Errorf("got %q/%q/%q", got[0].Alias, got[1].Alias, got[2].Alias)
	}
}

func TestSortDoesNotMutateInput(t *testing.T) {
	rows := []poll.Row{{Index: 2, Name: "b"}, {Index: 1, Name: "a"}}

	sortRows(rows, defaultOrder(SortName), true, nil)

	if rows[0].Name != "b" {
		t.Error("the poller's slice must not be reordered under it")
	}
}

func TestDefaultDirections(t *testing.T) {
	// Busiest and most broken first, names and aliases from A to Z.
	for _, k := range []SortKey{SortTraffic, SortErrors} {
		if !defaultOrder(k).desc {
			t.Errorf("%v should start descending", k)
		}
	}
	for _, k := range []SortKey{SortName, SortAlias} {
		if defaultOrder(k).desc {
			t.Errorf("%v should start ascending", k)
		}
	}
}

func TestReversedOrderFlipsTheRows(t *testing.T) {
	rows := []poll.Row{
		{Index: 3, Name: "xe-1/0/10"},
		{Index: 1, Name: "xe-1/0/2"},
		{Index: 2, Name: "et-0/0/1"},
	}

	up, _ := sortRows(rows, defaultOrder(SortName), true, nil)
	down, _ := sortRows(rows, defaultOrder(SortName).reversed(), true, nil)

	if len(up) != len(down) {
		t.Fatal("length changed")
	}
	for i := range up {
		if up[i].Index != down[len(down)-1-i].Index {
			t.Fatalf("reversed order is not the mirror image: %v vs %v", indexes(up), indexes(down))
		}
	}
}

func TestReversedTrafficPutsQuietPortsFirst(t *testing.T) {
	rows := []poll.Row{
		{Index: 1, Name: "xe-0/0/1", InOctets: i64(100)},
		{Index: 2, Name: "xe-0/0/2", InOctets: i64(900)},
	}

	got, _ := sortRows(rows, defaultOrder(SortTraffic).reversed(), true, nil)

	if got[0].Index != 1 {
		t.Errorf("first row = %d, want the quiet interface 1", got[0].Index)
	}
}

// Flipping an unstable sort has to take effect at once, not only after the
// next poll completes.
func TestReverseRankingFlipsARememberedOrder(t *testing.T) {
	order := map[int]int{7: 0, 8: 1, 9: 2}

	flipped := reverseRanking(order)

	if want := map[int]int{7: 2, 8: 1, 9: 0}; !reflect.DeepEqual(flipped, want) {
		t.Errorf("got %v, want %v", flipped, want)
	}
	if reverseRanking(nil) != nil {
		t.Error("no ranking to flip must stay nil")
	}

	rows := []poll.Row{{Index: 7, Name: "a"}, {Index: 8, Name: "b"}, {Index: 9, Name: "c"}}
	got, _ := sortRows(rows, defaultOrder(SortTraffic).reversed(), false, flipped)
	if got[0].Index != 9 || got[2].Index != 7 {
		t.Errorf("mid-poll order = %v, want 9/8/7", indexes(got))
	}
}

func indexes(rows []poll.Row) []int {
	out := make([]int, len(rows))
	for i, r := range rows {
		out[i] = r.Index
	}
	return out
}

func i64(v int64) *int64 { return &v }

// slices.SortFunc is not stable, and natCompare treats names that differ only
// in leading zeroes as equal. Without a unique tiebreak those rows swap places
// on every redraw, though the name sort is documented as deterministic.
func TestSortIsDeterministicForEqualNames(t *testing.T) {
	rows := []poll.Row{
		{Index: 30, Name: "xe-0/0/07"},
		{Index: 10, Name: "xe-0/0/7"}, // natCompare calls these two equal
		{Index: 20, Name: "xe-0/0/07"},
	}

	first, _ := sortRows(rows, defaultOrder(SortName), true, nil)
	want := indexOrder(first)
	for range 20 {
		got, _ := sortRows(rows, defaultOrder(SortName), true, nil)
		if !reflect.DeepEqual(indexOrder(got), want) {
			t.Fatalf("order changed between redraws: %v then %v", want, indexOrder(got))
		}
	}
	// And it is the ifIndex that decides, so the order is predictable.
	if !reflect.DeepEqual(want, []int{10, 20, 30}) {
		t.Errorf("order = %v, want it settled by ifIndex", want)
	}
}

// Same for the traffic sort, where equal rates are the normal case on an idle
// device: every row reads 0.
func TestSortByTrafficIsDeterministicForEqualRates(t *testing.T) {
	zero := int64(0)
	rows := []poll.Row{
		{Index: 3, Name: "xe-0/0/1", InOctets: &zero, OutOctets: &zero},
		{Index: 1, Name: "xe-0/0/1", InOctets: &zero, OutOctets: &zero},
		{Index: 2, Name: "xe-0/0/1", InOctets: &zero, OutOctets: &zero},
	}

	first, _ := sortRows(rows, defaultOrder(SortTraffic), true, nil)
	want := indexOrder(first)
	for range 20 {
		got, _ := sortRows(rows, defaultOrder(SortTraffic), true, nil)
		if !reflect.DeepEqual(indexOrder(got), want) {
			t.Fatalf("order changed between redraws: %v then %v", want, indexOrder(got))
		}
	}
}

func indexOrder(rows []poll.Row) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Index)
	}
	return out
}
