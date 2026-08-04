package main

import (
	"bytes"
	"errors"
	"flag"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestConfigWarnings(t *testing.T) {
	broken := []error{
		errors.New("/etc/snmpscan/legacy.device: field :name not found"),
		errors.New("/etc/snmpscan/old.device: field :prio not found"),
	}

	if _, err := configWarnings(nil, false); err != nil {
		t.Errorf("nothing broken must not be an error: %v", err)
	}

	// The default refuses to start, and says how to override that.
	err := mustFail(t, broken, false)
	for _, want := range []string{"legacy.device", "old.device", "-ignore-broken-configs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}

	// With the flag the run goes ahead — but every skipped file stays visible.
	warnings, err := configWarnings(broken, true)
	if err != nil {
		t.Fatalf("the flag must let the run start: %v", err)
	}
	if len(warnings) != len(broken) {
		t.Fatalf("%d warnings for %d broken files — one went missing", len(warnings), len(broken))
	}
	if !strings.Contains(warnings[0], "legacy.device") {
		t.Errorf("warning does not name the file: %q", warnings[0])
	}

	// The info block gives each warning one line, so a multi-line yaml error
	// has to be folded or its detail is cut off.
	multi := []error{errors.New("a.device: yaml: unmarshal errors:\n  line 2: field :name not found")}
	folded, err := configWarnings(multi, true)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(folded[0], "\n") {
		t.Errorf("warning spans several lines: %q", folded[0])
	}
	if !strings.Contains(folded[0], "line 2: field :name not found") {
		t.Errorf("folding lost the detail: %q", folded[0])
	}
}

func mustFail(t *testing.T, broken []error, ignore bool) error {
	t.Helper()
	warnings, err := configWarnings(broken, ignore)
	if err == nil {
		t.Fatalf("broken config accepted, warnings = %v", warnings)
	}
	return err
}

func TestValidate(t *testing.T) {
	ok := options{port: 161, sessions: 8, timeout: 2 * time.Second}
	if err := validate(ok, nil); err != nil {
		t.Fatalf("the defaults were rejected: %v", err)
	}

	// Each of these fails somewhere far from the flag if it gets through: a
	// truncated port silently becomes 161, a zero timeout looks like the device
	// not answering, a sub-second discovery walks the agent to death.
	cases := map[string]struct {
		opts  options
		given map[string]bool
	}{
		"-p 0":              {options{port: 0, sessions: 8, timeout: time.Second}, nil},
		"-p 65536":          {options{port: 65536, sessions: 8, timeout: time.Second}, nil},
		"-sessions 0":       {options{port: 161, sessions: 0, timeout: time.Second}, nil},
		"-sessions 999":     {options{port: 161, sessions: 999, timeout: time.Second}, nil},
		"-timeout 0":        {options{port: 161, sessions: 8, timeout: 0}, nil},
		"-timeout negative": {options{port: 161, sessions: 8, timeout: -time.Second}, nil},
		"-i 0":              {options{port: 161, sessions: 8, timeout: time.Second, interval: 0}, map[string]bool{"i": true}},
		"-discover 500ms":   {options{port: 161, sessions: 8, timeout: time.Second, discovery: 500 * time.Millisecond}, map[string]bool{"discover": true}},
		"-retries negative": {options{port: 161, sessions: 8, timeout: time.Second, retries: -1}, nil},
	}
	for name, tc := range cases {
		if err := validate(tc.opts, tc.given); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}

	// Not given is not the same as zero: the config file decides then.
	if err := validate(options{port: 161, sessions: 8, timeout: time.Second}, nil); err != nil {
		t.Errorf("an absent -i must not be judged: %v", err)
	}
	// A negative discovery is the documented "walk once at startup".
	single := options{port: 161, sessions: 8, timeout: time.Second, discovery: -time.Second}
	if err := validate(single, map[string]bool{"discover": true}); err != nil {
		t.Errorf("-discover -1s was rejected: %v", err)
	}
}

// The usage text is written by hand, so it can fall behind the flags. Matching
// has to be per line and on a word boundary: a plain substring search finds "-i"
// inside "-ignore-broken-configs" and "-c" inside "broken-configs", which left
// the five most important flags unguarded.
func TestUsageListsEveryFlag(t *testing.T) {
	var help bytes.Buffer
	fs := flag.NewFlagSet("snmpscan", flag.ContinueOnError)
	fs.SetOutput(&help)
	// Our own usage, not the FlagSet's default — that one prints the flags from
	// the registry and would agree with itself no matter what.
	fs.Usage = usage(fs)
	var opts options
	register(fs, &opts)
	fs.Usage()
	text := help.String()

	// An alias may share the line with the flag it stands for, so both groups
	// count. Without that the test would dictate the layout instead of checking
	// it — "-v, -version" on one line would report -version as undocumented.
	documented := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\s+-([\w-]+)(?:,\s*-([\w-]+))?`).FindAllStringSubmatch(text, -1) {
		documented[m[1]] = true
		if m[2] != "" {
			documented[m[2]] = true
		}
	}

	fs.VisitAll(func(f *flag.Flag) {
		if !documented[f.Name] {
			t.Errorf("flag -%s is registered but missing from the usage text", f.Name)
		}
	})
	// And the other way round, from the same extraction rather than a third
	// hand-kept list: nothing documented that no longer exists.
	for name := range documented {
		if name == "help" {
			continue // provided by the flag package itself
		}
		if fs.Lookup(name) == nil {
			t.Errorf("usage documents -%s, which is not a flag any more", name)
		}
	}
}
