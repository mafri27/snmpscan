package poll

// Counter widths as announced by the MIB: ifInErrors and the ucast packet
// counters are Counter32, everything from ifXTable is Counter64.
const (
	width32 = 32
	width64 = 64
)

// rate turns two absolute counter readings into a per-second value.
//
// A counter that went backwards is either a wrap or an agent that reset it.
// Only a 32 bit wrap can be recovered; for a 64 bit counter a decrease means a
// reset, and inventing a delta there would print an absurd spike. Returning
// nil makes the table show "-" for one interval instead — the Ruby version
// printed the negative number.
func rate(cur, prev value, width uint, secs float64) *int64 {
	if !cur.ok || !prev.ok || secs <= 0 {
		return nil
	}

	d := cur.n - prev.n
	if cur.n < prev.n {
		if width != width32 || prev.n >= 1<<32 || cur.n >= 1<<32 {
			return nil
		}
		d = cur.n + 1<<32 - prev.n
	}

	r := int64(float64(d) / secs)
	return &r
}

// scale divides a vendor supplied per-second value by the factor from the
// profile. Juniper reports bits where the table wants bytes, hence factor 8.
func scale(v value, factor int) *int64 {
	if !v.ok {
		return nil
	}
	if factor <= 0 {
		factor = 1
	}
	r := int64(v.n) / int64(factor)
	return &r
}
