package ui

import (
	"maps"
	"strings"
	"testing"

	"github.com/mafri27/snmpscan/internal/poll"
)

func TestParseHidden(t *testing.T) {
	got, err := ParseHidden([]string{"ifnr, Errors", "kpps", ""})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]bool{colIfNr: true, colErrors: true, colInPps: true, colOutPps: true}
	if !maps.Equal(got, want) {
		t.Errorf("hidden = %v, want %v", got, want)
	}
}

func TestParseHiddenRejectsUnknown(t *testing.T) {
	_, err := ParseHidden([]string{"ifnr,mpbs"})
	if err == nil || !strings.Contains(err.Error(), `"mpbs"`) {
		t.Fatalf("err = %v, want the typo named", err)
	}
	// The message is the only place the valid names are listed short of -help.
	if !strings.Contains(err.Error(), "mbps-in") || !strings.Contains(err.Error(), "kpps") {
		t.Errorf("err = %v, want the valid names listed", err)
	}
}

func TestParseHiddenRejectsEverything(t *testing.T) {
	all := make([]string, 0, len(columns))
	for _, c := range columns {
		all = append(all, c.key)
	}
	if _, err := ParseHidden([]string{strings.Join(all, ",")}); err == nil {
		t.Error("an empty table should be refused")
	}
}

// Only counters are worth telling the poller about; name and alias come from
// the walk, which the filters need anyway.
func TestOmitted(t *testing.T) {
	got := Omitted(map[int]bool{colName: true, colAlias: true, colIn: true, colErrors: true})
	want := map[poll.Counter]bool{poll.InOctets: true, poll.InErrors: true}
	if !maps.Equal(got, want) {
		t.Errorf("omitted = %v, want %v", got, want)
	}
}
