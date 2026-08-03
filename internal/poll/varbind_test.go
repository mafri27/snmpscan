package poll

import (
	"errors"
	"strconv"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// fakeSession answers from a script, so the mapping of varbinds onto rows can be
// exercised without an agent. Only the live tests reached this code before, and
// they skip without one.
type fakeSession struct {
	reply func(oids []string) (*gosnmp.SnmpPacket, error)
	asked [][]string
}

func (f *fakeSession) Get(oids []string) (*gosnmp.SnmpPacket, error) {
	f.asked = append(f.asked, oids)
	return f.reply(oids)
}

func counter(oid string, n uint64) gosnmp.SnmpPDU {
	return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Counter64, Value: n}
}

// The whole point of the where map: every varbind has to land on the interface
// and column it was asked for. An off-by-one here does not crash, it shows one
// port's traffic under another's name.
func TestAssignPutsVarbindsOnTheRightRow(t *testing.T) {
	cols := []column{
		{oidIfHCInOctets, func(c *counters, v value) { c.inOct = v }},
		{oidIfHCOutOctets, func(c *counters, v value) { c.outOct = v }},
	}
	st := &state{
		ifaces: []iface{{index: 11}, {index: 22}},
		raw:    make([]counters, 2),
		done:   make([]bool, 2),
	}
	where := map[string]cell{}
	for ri, ifc := range st.ifaces {
		for ci, col := range cols {
			where[col.base+"."+strconv.Itoa(ifc.index)] = cell{ri, ci}
		}
	}

	// Deliberately shuffled, and with one varbind nobody asked for.
	assign(st, cols, where, &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{
		counter(oidIfHCOutOctets+".22", 2222),
		counter(oidIfHCInOctets+".11", 1111),
		counter("1.2.3.4.5", 9999),
		counter(oidIfHCOutOctets+".11", 1122),
		counter(oidIfHCInOctets+".22", 2211),
	}})

	for i, want := range []counters{
		{inOct: val(1111), outOct: val(1122)},
		{inOct: val(2211), outOct: val(2222)},
	} {
		if st.raw[i].inOct != want.inOct || st.raw[i].outOct != want.outOct {
			t.Errorf("row %d = in %v/out %v, want in %v/out %v", i,
				st.raw[i].inOct, st.raw[i].outOct, want.inOct, want.outOct)
		}
	}
}

// A denial has to reach the row it belongs to — that flag is what tells a
// removed port from an answer that went missing.
func TestAssignRecordsADenial(t *testing.T) {
	cols := []column{{oidIfHCInOctets, func(c *counters, v value) { c.inOct = v }}}
	st := &state{ifaces: []iface{{index: 5}, {index: 6}}, raw: make([]counters, 2), done: make([]bool, 2)}
	where := map[string]cell{
		oidIfHCInOctets + ".5": {0, 0},
		oidIfHCInOctets + ".6": {1, 0},
	}

	assign(st, cols, where, &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{
		counter(oidIfHCInOctets+".5", 500),
		{Name: "." + oidIfHCInOctets + ".6", Type: gosnmp.NoSuchInstance},
	}})

	if st.raw[0].denied {
		t.Error("a row that answered with a value must not be marked denied")
	}
	if !st.raw[1].denied {
		t.Error("noSuchInstance did not reach the row")
	}
}

// gosnmp reports a non-zero error-status only inside the packet. Treating that
// as a valid empty answer is what emptied the table in batch-sized blocks.
func TestFetchTreatsAnErrorStatusAsAnError(t *testing.T) {
	for _, status := range []gosnmp.SNMPError{gosnmp.TooBig, gosnmp.GenErr, gosnmp.AuthorizationError} {
		s := &fakeSession{reply: func([]string) (*gosnmp.SnmpPacket, error) {
			return &gosnmp.SnmpPacket{Error: status}, nil
		}}
		if _, err := fetch(s, []string{"1.2.3"}); err == nil {
			t.Errorf("%v passed as a valid response", status)
		}
	}

	s := &fakeSession{reply: func(oids []string) (*gosnmp.SnmpPacket, error) {
		return &gosnmp.SnmpPacket{Variables: []gosnmp.SnmpPDU{counter("1.2.3", 7)}}, nil
	}}
	res, err := fetch(s, []string{"1.2.3"})
	if err != nil || len(res.Variables) != 1 {
		t.Errorf("a clean response was rejected: %v", err)
	}
}

// getAll returns what the earlier chunks delivered, which is what lets a lost
// scalar request keep the sysName it already had.
func TestGetAllKeepsWhatArrivedBeforeTheFailure(t *testing.T) {
	many := make([]string, 0, gosnmp.MaxOids+5)
	for i := range gosnmp.MaxOids + 5 {
		many = append(many, "1.3.6.1.2.1.1."+strconv.Itoa(i)+".0")
	}

	calls := 0
	s := &fakeSession{reply: func(oids []string) (*gosnmp.SnmpPacket, error) {
		calls++
		if calls > 1 {
			return nil, errors.New("request timeout")
		}
		vars := make([]gosnmp.SnmpPDU, 0, len(oids))
		for _, oid := range oids {
			vars = append(vars, counter(oid, 1))
		}
		return &gosnmp.SnmpPacket{Variables: vars}, nil
	}}

	out, err := getAll(s, many)

	if err == nil {
		t.Error("the failure of the second chunk must be reported")
	}
	if len(out) != gosnmp.MaxOids {
		t.Errorf("%d values kept, want the %d from the first chunk", len(out), gosnmp.MaxOids)
	}
	if len(s.asked) != 2 {
		t.Errorf("%d requests, want the oids split into 2 chunks", len(s.asked))
	}
}
