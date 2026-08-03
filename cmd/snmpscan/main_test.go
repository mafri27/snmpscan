package main

import (
	"errors"
	"strings"
	"testing"
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
