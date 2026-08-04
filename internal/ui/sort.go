package ui

import (
	"cmp"
	"math"
	"slices"
	"strings"

	"github.com/mafri27/snmpscan/internal/poll"
)

// SortKey selects the table order.
type SortKey int

const (
	// SortName orders by port name, numerically aware.
	SortName SortKey = iota
	// SortTraffic puts the busiest ports first.
	SortTraffic
	// SortErrors puts the most erroring ports first.
	SortErrors
	// SortAlias orders by the interface description.
	SortAlias
)

func (k SortKey) String() string {
	switch k {
	case SortTraffic:
		return "traffic"
	case SortErrors:
		return "errors"
	case SortAlias:
		return "alias"
	default:
		return "name"
	}
}

// stable reports whether the key orders by something that does not change as
// counter values arrive. Unstable keys must not be reapplied mid-poll or the
// rows shuffle under the cursor.
func (k SortKey) stable() bool { return k == SortName || k == SortAlias }

// sortOrder is a key together with the direction it runs in.
type sortOrder struct {
	key  SortKey
	desc bool
}

// defaultOrder is how a key sorts when it is first selected: busiest and most
// broken first, names and aliases from A to Z.
func defaultOrder(k SortKey) sortOrder {
	return sortOrder{key: k, desc: k == SortTraffic || k == SortErrors}
}

// reversed flips the direction, which is what pressing the same key twice does.
func (o sortOrder) reversed() sortOrder {
	o.desc = !o.desc
	return o
}

// sortedBy lists the table columns a key orders by. Traffic covers both
// directions, since it ranks on their sum.
func sortedBy(k SortKey) []int {
	switch k {
	case SortTraffic:
		return []int{colIn, colOut}
	case SortErrors:
		return []int{colErrors}
	case SortAlias:
		return []int{colAlias}
	default:
		return []int{colName}
	}
}

// sortRows returns the rows in display order. For an unstable key on an
// incomplete poll it reuses order, the ranking from the last complete poll,
// and returns it unchanged; otherwise it returns the fresh ranking.
func sortRows(rows []poll.Row, o sortOrder, complete bool, order map[int]int) ([]poll.Row, map[int]int) {
	out := slices.Clone(rows)

	switch {
	case o.key.stable():
		slices.SortFunc(out, directed(stableCompare(o.key), o.desc))
	case complete:
		slices.SortFunc(out, directed(unstableCompare(o.key), o.desc))
		order = ranking(out)
	default:
		// Hold last poll's positions. Interfaces that appeared since then have
		// no rank and go to the end, in name order.
		slices.SortFunc(out, byRanking(order))
	}
	return out, order
}

// directed flips a comparison for a descending sort. The name tiebreak turns
// over with it, so the result is a strict reversal.
func directed(cmpFn func(a, b poll.Row) int, desc bool) func(a, b poll.Row) int {
	if !desc {
		return cmpFn
	}
	return func(a, b poll.Row) int { return -cmpFn(a, b) }
}

func stableCompare(key SortKey) func(a, b poll.Row) int {
	if key == SortAlias {
		return func(a, b poll.Row) int {
			return cmp.Or(strings.Compare(a.Alias, b.Alias), byName(a, b))
		}
	}
	return byName
}

func unstableCompare(key SortKey) func(a, b poll.Row) int {
	return func(a, b poll.Row) int {
		var c int
		if key == SortErrors {
			c = cmp.Compare(deref(a.InErrors), deref(b.InErrors))
		} else {
			c = cmp.Compare(traffic(a), traffic(b))
		}
		return cmp.Or(c, byName(a, b))
	}
}

// byName is every comparison's last word, and it ends on the ifIndex on
// purpose: slices.SortFunc is not stable, and natCompare calls two different
// names equal whenever only leading zeroes tell them apart — as it should for
// ordering. Without a unique tiebreak those rows swap places on every redraw.
func byName(a, b poll.Row) int {
	return cmp.Or(natCompare(a.Name, b.Name), cmp.Compare(a.Index, b.Index))
}

func byRanking(order map[int]int) func(a, b poll.Row) int {
	return func(a, b poll.Row) int {
		ra, oka := order[a.Index]
		rb, okb := order[b.Index]
		switch {
		case oka && okb:
			return cmp.Compare(ra, rb)
		case oka:
			return -1
		case okb:
			return 1
		}
		return natCompare(a.Name, b.Name)
	}
}

func ranking(rows []poll.Row) map[int]int {
	order := make(map[int]int, len(rows))
	for i, r := range rows {
		order[r.Index] = i
	}
	return order
}

// reverseRanking turns a remembered order upside down. Flipping the direction
// of an unstable sort takes effect at once this way, rather than waiting for
// the next poll to finish.
func reverseRanking(order map[int]int) map[int]int {
	if order == nil {
		return nil
	}
	out := make(map[int]int, len(order))
	for index, rank := range order {
		out[index] = len(order) - 1 - rank
	}
	return out
}

func traffic(r poll.Row) int64 { return deref(r.InOctets) + deref(r.OutOctets) }

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// natCompare orders strings with embedded numbers the way a human reads them,
// so xe-1/0/2 sorts before xe-1/0/10 rather than after it.
func natCompare(a, b string) int {
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if isDigit(a[i]) && isDigit(b[j]) {
			ni, si := number(a, i)
			nj, sj := number(b, j)
			if c := cmp.Compare(ni, nj); c != 0 {
				return c
			}
			i, j = si, sj
			continue
		}
		if a[i] != b[j] {
			return cmp.Compare(a[i], b[j])
		}
		i++
		j++
	}
	return cmp.Compare(len(a)-i, len(b)-j)
}

// number reads the digit run starting at i and returns its value and the index
// just past it. Leading zeros are irrelevant to the comparison.
func number(s string, i int) (uint64, int) {
	var n uint64
	for ; i < len(s) && isDigit(s[i]); i++ {
		// Saturate rather than wrap on an absurdly long digit run. Stopping the
		// accumulation partway instead would leave an arbitrary value, and two
		// overlong runs could then compare in the wrong order.
		if n > (math.MaxUint64-uint64(s[i]-'0'))/10 {
			n = math.MaxUint64
			continue
		}
		n = n*10 + uint64(s[i]-'0')
	}
	return n, i
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
