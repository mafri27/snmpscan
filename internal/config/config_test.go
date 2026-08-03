package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLoadShippedProfiles guards the profiles that ship with the tool: a typo
// in one of them must fail here, not in front of a device at 3am.
func TestLoadShippedProfiles(t *testing.T) {
	set, err := Load([]string{"../../.snmpscan"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(set.Devices) == 0 {
		t.Fatal("no devices loaded")
	}

	for _, tc := range []struct {
		sysDescr  string
		wantCPU   string
		wantFiltr int
		wantInfos int
	}{
		// One Juniper entry covers every model, so qfx and ex all land on the
		// same CPU OID and the same physical-port filter.
		{"Juniper Networks, Inc. qfx5100-96s-8q Ethernet Switch, kernel JUNOS 21.4R3", "1.3.6.1.4.1.2636.3.1.13.1.8.9", 1, 3},
		{"Juniper Networks, Inc. qfx10002-72q Ethernet Switch", "1.3.6.1.4.1.2636.3.1.13.1.8.9", 1, 3},
		{"Juniper Networks, Inc. ex4300-48t internal build", "1.3.6.1.4.1.2636.3.1.13.1.8.9", 1, 3},
		{"Huawei Versatile Routing Platform", "1.3.6.1.4.1.2011.2.23.1.18.4.3.1.4", 0, 0},
		{"Some Unknown Vendor OS", "1.3.6.1.2.1.1.4", 0, 0},
	} {
		p := set.Match(tc.sysDescr)
		if p.CPUOID != tc.wantCPU {
			t.Errorf("%q: cpu oid = %q, want %q", tc.sysDescr, p.CPUOID, tc.wantCPU)
		}
		if len(p.Filters) != tc.wantFiltr {
			t.Errorf("%q: %d filters, want %d", tc.sysDescr, len(p.Filters), tc.wantFiltr)
		}
		if len(p.AddInfos) != tc.wantInfos {
			t.Errorf("%q: %d readings, want %d", tc.sysDescr, len(p.AddInfos), tc.wantInfos)
		}
	}
}

// The juniper profile must keep reporting bits, or every rate is off by 8.
func TestJuniperSecValueFactor(t *testing.T) {
	set, err := Load([]string{"../../.snmpscan"})
	if err != nil {
		t.Fatal(err)
	}
	p := set.Match("Juniper Networks, Inc. ex4300-48t")
	if p.SecValueFactor != 8 {
		t.Errorf("sec_value_factor = %d, want 8", p.SecValueFactor)
	}
	if p.InSecOctOID == "" || p.OutSecPpsOID == "" {
		t.Error("per-second OIDs not inherited from the prio 1 entry")
	}
}

func TestMatchMergesByPriority(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.device", `
- name: ".*"
  prio: 1
  cpu_oid: 1.1.1
  default_filter: ["base"]
- name: "Acme"
  prio: 2
  cpu_oid: 2.2.2
  default_filter: ["extra"]
`)
	set, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	p := set.Match("Acme Router")
	if p.CPUOID != "2.2.2" {
		t.Errorf("cpu oid = %q, want the higher priority 2.2.2", p.CPUOID)
	}
	if len(p.Filters) != 2 || p.Filters[0] != "base" || p.Filters[1] != "extra" {
		t.Errorf("filters = %v, want them to accumulate in priority order", p.Filters)
	}

	if p := set.Match("Other Router"); p.CPUOID != "1.1.1" || len(p.Filters) != 1 {
		t.Errorf("non-matching device picked up the Acme entry: %+v", p)
	}
}

func TestDeviceOverridesThresholds(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.device", `
- name: ".*"
  prio: 1
  thresholds:
    alert:
      mbit: 9000
`)
	set, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}

	p := set.Match("anything")
	if *p.Thresholds.Alert.Mbit != 9000 {
		t.Errorf("alert mbit = %d, want the device override 9000", *p.Thresholds.Alert.Mbit)
	}
	if *p.Thresholds.Alert.Pps != 100000 {
		t.Errorf("alert pps = %d, want the untouched default 100000", *p.Thresholds.Alert.Pps)
	}
	if *p.Thresholds.Dim.Mbit != 5 {
		t.Errorf("dim mbit = %d, want the untouched default 5", *p.Thresholds.Dim.Mbit)
	}
}

func TestSettingsMostSpecificWins(t *testing.T) {
	etc, home := t.TempDir(), t.TempDir()
	write(t, etc, SettingsFile, "interval: 30\nthresholds:\n  dim:\n    mbit: 1\n")
	write(t, home, SettingsFile, "interval: 5\n")

	set, err := Load([]string{etc, home})
	if err != nil {
		t.Fatal(err)
	}
	if got := Seconds(set.Settings.Interval, 10); got != 5*time.Second {
		t.Errorf("interval = %v, want 5s from the more specific file", got)
	}
	if *set.Settings.Thresholds.Dim.Mbit != 1 {
		t.Errorf("dim mbit = %d, want 1 to survive from the less specific file", *set.Settings.Thresholds.Dim.Mbit)
	}
}

// Zero is a real setting — poll without pausing — and must survive as one.
func TestZeroIntervalIsKept(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, SettingsFile, "interval: 0\n")

	set, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if set.Settings.Interval == nil || *set.Settings.Interval != 0 {
		t.Fatalf("interval = %v, want an explicit 0", set.Settings.Interval)
	}
	if got := Seconds(set.Settings.Interval, 10); got != 0 {
		t.Errorf("Seconds = %v, want 0 rather than the fallback", got)
	}
	// An absent key still falls back.
	if got := Seconds(set.Settings.Discovery, 42); got != 42*time.Second {
		t.Errorf("unset discovery = %v, want the 42s fallback", got)
	}
}

// A typo must not quietly disable whatever it was meant to configure. Load
// reports it rather than returning an error, so one bad file cannot hide the
// good ones — deciding what that means is the caller's job.
func TestUnknownKeyIsRejected(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.device", "- name: \".*\"\n  cpu_iod: 1.2.3\n")
	write(t, dir, "b.device", "- name: \"Acme\"\n  cpu_oid: 1.2.3\n")

	set, err := Load([]string{dir})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(set.Broken) != 1 {
		t.Fatalf("%d files reported broken, want the one with the typo", len(set.Broken))
	}
	if !strings.Contains(set.Broken[0].Error(), "a.device") {
		t.Errorf("error does not name the file: %v", set.Broken[0])
	}
	if len(set.Devices) != 1 {
		t.Errorf("%d devices loaded, want the intact file to survive", len(set.Devices))
	}
}

func TestMissingDirIsNotAnError(t *testing.T) {
	set, err := Load([]string{filepath.Join(t.TempDir(), "nope")})
	if err != nil {
		t.Fatalf("missing directory should be skipped: %v", err)
	}
	if got := Seconds(set.Settings.Interval, 10); got != 10*time.Second {
		t.Errorf("interval = %v, want the default 10s", got)
	}
}

func TestEvaluateFirstCaseWins(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.device", `
- name: ".*"
  add_infos:
  - oid: 1.2.3.0
    name: Powersupply
    type: same
    relation:
    - test: noSuchObject
      output: ERROR
      error: true
    - test: ''
      output: OK
      error: false
`)
	set, err := Load([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	info := set.Match("x").AddInfos[0]

	out, isErr, ok := info.Evaluate("noSuchObject")
	if !ok || out != "ERROR" || !isErr {
		t.Errorf("got (%q, %v, %v), want the specific case to win over the catch-all", out, isErr, ok)
	}
	if out, isErr, _ := info.Evaluate("1"); out != "OK" || isErr {
		t.Errorf("got (%q, %v), want the catch-all", out, isErr)
	}
}

func TestEvaluateMinMax(t *testing.T) {
	max := &AddInfo{Kind: KindMax, Limit: 40}
	if _, isErr, _ := max.Evaluate("41"); !isErr {
		t.Error("41 should exceed a max of 40")
	}
	if _, isErr, _ := max.Evaluate("40"); isErr {
		t.Error("40 should not exceed a max of 40")
	}
	// Agents like to append a unit; Ruby's to_i ignored it and so must we.
	if _, isErr, _ := max.Evaluate("55 C"); !isErr {
		t.Error(`"55 C" should be read as 55`)
	}

	min := &AddInfo{Kind: KindMin, Limit: 10}
	if _, isErr, _ := min.Evaluate("9"); !isErr {
		t.Error("9 should fall below a min of 10")
	}
}

func TestParseInt(t *testing.T) {
	for in, want := range map[string]int64{
		"42": 42, "31 C": 31, "noSuchObject": 0, "": 0, "-7": -7, " 8 ": 8,
	} {
		if got := parseInt(in); got != want {
			t.Errorf("parseInt(%q) = %d, want %d", in, got, want)
		}
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
