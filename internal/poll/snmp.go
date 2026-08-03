package poll

import (
	"cmp"
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/gosnmp/gosnmp"
)

// Standard MIB-II objects. The 64 bit counters live in ifXTable, which is why
// they carry a different prefix than ifInErrors.
const (
	oidSysDescr  = "1.3.6.1.2.1.1.1.0"
	oidSysUpTime = "1.3.6.1.2.1.1.3.0"
	oidSysName   = "1.3.6.1.2.1.1.5.0"

	oidIfName  = "1.3.6.1.2.1.31.1.1.1.1"
	oidIfAlias = "1.3.6.1.2.1.31.1.1.1.18"

	oidIfHCInOctets  = "1.3.6.1.2.1.31.1.1.1.6"
	oidIfHCOutOctets = "1.3.6.1.2.1.31.1.1.1.10"
	oidIfInUcastPkts = "1.3.6.1.2.1.2.2.1.11"
	oidIfOutUcastPkt = "1.3.6.1.2.1.2.2.1.12"
	oidIfInErrors    = "1.3.6.1.2.1.2.2.1.14"

	oidIPAdEntIfIndex = "1.3.6.1.2.1.4.20.1.2"
)

// Target describes how to reach a device. SNMPv3 will slot in here as
// additional fields; nothing outside this file needs to know the difference.
type Target struct {
	Host      string
	Port      uint16
	Community string
	Timeout   time.Duration
	Retries   int
	// MaxRepetitions is how many rows one GETBULK may return. Zero picks the
	// default.
	MaxRepetitions uint32
}

// defaultMaxRepetitions matches net-snmp's snmpbulkwalk and the icinga
// checkgo plugins, which run this value against ~10k systems. Higher is
// faster on a healthy agent (25 polls a 590 interface QFX5100 in 5.5s where
// 10 needs 8.7s) but slow agents start timing out, so raising it is a
// per-device decision via -maxrep rather than a default.
const defaultMaxRepetitions = 10

func (t Target) dial(sent *atomic.Int64) (*gosnmp.GoSNMP, error) {
	c := &gosnmp.GoSNMP{
		Target:    t.Host,
		Port:      t.Port,
		Community: t.Community,
		Version:   gosnmp.Version2c,
		Transport: "udp",
		Timeout:   t.Timeout,
		Retries:   t.Retries,
		MaxOids:   gosnmp.MaxOids,
		// A GETBULK returns many rows per round trip instead of the single row
		// a GETNEXT gives — this is what replaces the per-interface round trip.
		MaxRepetitions: cmp.Or(t.MaxRepetitions, defaultMaxRepetitions),
		// Disable the strictly-increasing-OID check (net-snmp's -Cc). Juniper
		// PTX returns GETBULK varbinds out of order, which otherwise aborts
		// the walk before a single value is read.
		AppOpts: map[string]any{"c": true},
	}
	if ip := net.ParseIP(t.Host); ip != nil && ip.To4() == nil {
		c.Transport = "udp6"
	}
	// Count actual packets. A BulkWalk is one call but many round trips, and
	// on a high latency link those round trips are the whole story.
	if sent != nil {
		c.OnSent = func(*gosnmp.GoSNMP) { sent.Add(1) }
	}
	if err := c.Connect(); err != nil {
		return nil, fmt.Errorf("connect to %s: %w", t.Host, err)
	}
	return c, nil
}

// pool hands out SNMP sessions. A gosnmp connection carries a single socket
// and request-id counter, so it must not be shared between goroutines — the
// buffered channel doubles as the pool and as the concurrency limit.
type pool struct {
	conns chan *gosnmp.GoSNMP
	all   []*gosnmp.GoSNMP
}

func newPool(t Target, size int, sent *atomic.Int64) (*pool, error) {
	p := &pool{conns: make(chan *gosnmp.GoSNMP, size)}
	for range size {
		c, err := t.dial(sent)
		if err != nil {
			p.Close()
			return nil, err
		}
		p.all = append(p.all, c)
		p.conns <- c
	}
	return p, nil
}

// with runs fn on a session borrowed from the pool.
func (p *pool) with(ctx context.Context, fn func(*gosnmp.GoSNMP) error) error {
	select {
	case c := <-p.conns:
		defer func() { p.conns <- c }()
		c.Context = ctx
		return fn(c)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pool) Close() {
	for _, c := range p.all {
		_ = c.Close()
	}
}

// value is a counter reading that may be absent, because the agent answered
// noSuchInstance for an object it does not implement.
type value struct {
	n  uint64
	ok bool
}

// pduString renders a varbind the way the Ruby SNMP library did. The exception
// markers matter: riverstone.device tests for the literal "noSuchObject" to
// detect a missing power supply.
func pduString(p gosnmp.SnmpPDU) string {
	switch p.Type {
	case gosnmp.NoSuchObject:
		return "noSuchObject"
	case gosnmp.NoSuchInstance:
		return "noSuchInstance"
	case gosnmp.EndOfMibView:
		return "endOfMibView"
	case gosnmp.Null:
		return ""
	}
	switch v := p.Value.(type) {
	case []byte:
		return string(v)
	case string:
		return v
	case nil:
		return ""
	default:
		return fmt.Sprint(v)
	}
}

func pduValue(p gosnmp.SnmpPDU) value {
	switch p.Type {
	case gosnmp.NoSuchObject, gosnmp.NoSuchInstance, gosnmp.EndOfMibView, gosnmp.Null:
		return value{}
	}
	n := gosnmp.ToBigInt(p.Value)
	if n == nil || n.Sign() < 0 || !n.IsUint64() {
		return value{}
	}
	return value{n: n.Uint64(), ok: true}
}
