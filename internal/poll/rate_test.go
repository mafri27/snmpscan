package poll

import "testing"

func TestRate(t *testing.T) {
	const max32 = uint64(1) << 32

	tests := []struct {
		name       string
		cur, prev  value
		width      uint
		secs       float64
		want       int64
		wantAbsent bool
	}{
		{name: "plain difference", cur: val(2000), prev: val(1000), width: width64, secs: 10, want: 100},
		{name: "no previous reading", cur: val(2000), prev: value{}, width: width64, secs: 10, wantAbsent: true},
		{name: "no current reading", cur: value{}, prev: val(1000), width: width64, secs: 10, wantAbsent: true},
		{name: "zero interval", cur: val(2000), prev: val(1000), width: width64, secs: 0, wantAbsent: true},
		{name: "32 bit wrap", cur: val(100), prev: val(max32 - 100), width: width32, secs: 10, want: 20},
		{name: "64 bit counter going backwards is a reset", cur: val(100), prev: val(1000), width: width64, secs: 10, wantAbsent: true},
		{name: "32 bit rule needs 32 bit values", cur: val(100), prev: val(max32 + 500), width: width32, secs: 10, wantAbsent: true},
		{name: "sub-second rounding truncates", cur: val(1005), prev: val(1000), width: width64, secs: 10, want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := rate(tc.cur, tc.prev, tc.width, tc.secs)
			if tc.wantAbsent {
				if got != nil {
					t.Fatalf("got %d, want no value", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got no value, want %d", tc.want)
			}
			if *got != tc.want {
				t.Errorf("got %d, want %d", *got, tc.want)
			}
		})
	}
}

func TestScale(t *testing.T) {
	// Juniper reports bits per second where the table wants bytes.
	if got := scale(val(800), 8); got == nil || *got != 100 {
		t.Errorf("scale(800, 8) = %v, want 100", deref(got))
	}
	if got := scale(val(800), 0); got == nil || *got != 800 {
		t.Errorf("a zero factor must not divide: got %v", deref(got))
	}
	if got := scale(value{}, 8); got != nil {
		t.Errorf("absent value must stay absent, got %d", *got)
	}
}

func val(n uint64) value { return value{n: n, ok: true} }

func deref(v *int64) any {
	if v == nil {
		return "<nil>"
	}
	return *v
}
