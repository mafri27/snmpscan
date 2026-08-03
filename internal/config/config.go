// Package config loads snmpscan's device profiles (*.device) and global
// settings (snmpscan.yml).
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SettingsFile is the name of the optional global configuration.
const SettingsFile = "snmpscan.yml"

// Set is everything read from disk.
type Set struct {
	Devices  []*Device
	Settings Settings
	// Broken holds one error per file that would not parse. Load collects
	// them rather than stopping at the first, so the caller can decide
	// whether a stale profile is worth refusing to start over.
	Broken []error
}

// Settings holds the values that used to be constants in the Ruby version.
// The intervals are pointers so that a file leaving one out keeps whatever a
// less specific file set, rather than overwriting it with a zero.
type Settings struct {
	// Interval is the seconds between value polls, at least MinInterval.
	Interval *int `yaml:"interval"`
	// Discovery is the seconds between re-reads of the interface list. Unset
	// means "same as Interval", negative means once at startup.
	Discovery  *int       `yaml:"discovery"`
	Thresholds Thresholds `yaml:"thresholds"`
}

// MinInterval is the shortest poll interval that makes sense. Below a second
// the poller spends the device's CPU rather than measuring it, and the agent's
// own clock has no room to advance between two reads.
const MinInterval = 1

// DefaultSettings applies when no snmpscan.yml exists anywhere.
func DefaultSettings() Settings {
	return Settings{Interval: ptr(10), Thresholds: DefaultThresholds()}
}

// Seconds resolves an optional interval, falling back to def when unset.
func Seconds(v *int, def int) time.Duration {
	if v == nil {
		return time.Duration(def) * time.Second
	}
	return time.Duration(*v) * time.Second
}

// SearchDirs lists the configuration directories from least to most specific,
// so a file in the working directory wins over one in the home directory,
// which in turn wins over /etc.
func SearchDirs() []string {
	dirs := []string{"/etc/snmpscan"}
	// The Ruby version globbed the literal string "~/.snmpscan/", which no
	// shell ever expanded, so per-user configuration was silently ignored.
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".snmpscan"))
	}
	return append(dirs, ".snmpscan")
}

// Load reads every *.device file plus the most specific snmpscan.yml found in
// dirs. Missing directories are not an error; unreadable ones are. A file that
// does not parse lands in Set.Broken and is skipped — see there.
func Load(dirs []string) (*Set, error) {
	set := &Set{Settings: DefaultSettings()}

	for _, dir := range dirs {
		files, err := filepath.Glob(filepath.Join(dir, "*.device"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", dir, err)
		}
		for _, file := range files {
			devs, err := loadDevices(file)
			if err != nil {
				set.Broken = append(set.Broken, err)
				continue
			}
			set.Devices = append(set.Devices, devs...)
		}

		settings, err := loadSettings(filepath.Join(dir, SettingsFile))
		if err != nil {
			set.Broken = append(set.Broken, err)
			continue
		}
		if settings != nil {
			if settings.Interval != nil {
				set.Settings.Interval = settings.Interval
			}
			if settings.Discovery != nil {
				set.Settings.Discovery = settings.Discovery
			}
			set.Settings.Thresholds = set.Settings.Thresholds.Overlay(settings.Thresholds)
		}
	}

	sortDevices(set.Devices)
	return set, nil
}

// strictUnmarshal reads one document, rejecting unknown keys so a typo in a
// profile is reported instead of quietly disabling whatever it was meant to
// configure. A second document is an error rather than something to skip.
func strictUnmarshal(data []byte, into any) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(into); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); err == nil {
		return fmt.Errorf("line %d: only the first document is read here", extra.Line)
	}
	return nil
}

// decodeDevices reads every document in the file. Profiles conventionally start
// with a `---`, which makes a second one easy to add — and losing it without a
// word would be the worst of both worlds.
func decodeDevices(data []byte) ([]*Device, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var all []*Device
	for {
		var devs []*Device
		err := dec.Decode(&devs)
		if errors.Is(err, io.EOF) {
			return all, nil
		}
		if err != nil {
			return nil, err
		}
		all = append(all, devs...)
	}
}

func loadDevices(file string) ([]*Device, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	devs, err := decodeDevices(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	for _, dev := range devs {
		if dev == nil {
			continue
		}
		dev.source = file
		dev.pattern, err = regexp.Compile(dev.Name)
		if err != nil {
			return nil, fmt.Errorf("%s: name %q: %w", file, dev.Name, err)
		}
	}
	return devs, nil
}

func loadSettings(file string) (*Settings, error) {
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	var s Settings
	if err := strictUnmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	if s.Interval != nil && *s.Interval < MinInterval {
		return nil, fmt.Errorf("%s: interval %d is below the minimum of %d", file, *s.Interval, MinInterval)
	}
	if s.Discovery != nil && *s.Discovery == 0 {
		return nil, fmt.Errorf("%s: discovery 0: use a negative value to walk once at startup", file)
	}
	return &s, nil
}

// sortDevices puts low priority entries first so that later, more specific
// ones overwrite them during a merge.
func sortDevices(devs []*Device) {
	sort.SliceStable(devs, func(i, j int) bool {
		if devs[i].Prio != devs[j].Prio {
			return devs[i].Prio < devs[j].Prio
		}
		return devs[i].Name < devs[j].Name
	})
}

// Profile is the merged result of every device entry matching a sysDescr.
type Profile struct {
	CPUOID         string
	Filters        []string
	AddInfos       []*AddInfo
	SecValueFactor int
	InSecOctOID    string
	OutSecOctOID   string
	InSecPpsOID    string
	OutSecPpsOID   string
	Thresholds     Thresholds
	Matched        []string
}

// Match merges all device entries whose name pattern is found in sysDescr.
// Scalars are overwritten, filters and readings accumulate — the same
// semantics the Ruby version had.
func (s *Set) Match(sysDescr string) Profile {
	p := Profile{
		// sysContact stood in for a CPU OID on unknown devices in the Ruby
		// version; kept so those devices show the same header as before.
		CPUOID:         "1.3.6.1.2.1.1.4",
		SecValueFactor: 1,
		Thresholds:     s.Settings.Thresholds,
	}

	for _, dev := range s.Devices {
		if dev.pattern == nil || !dev.pattern.MatchString(sysDescr) {
			continue
		}
		p.Matched = append(p.Matched, fmt.Sprintf("%s (%s)", dev.Name, filepath.Base(dev.source)))

		if dev.CPUOID != "" {
			p.CPUOID = NormalizeOID(dev.CPUOID)
		}
		if dev.SecValueFactor != 0 {
			p.SecValueFactor = dev.SecValueFactor
		}
		setOID(&p.InSecOctOID, dev.InSecOctOID)
		setOID(&p.OutSecOctOID, dev.OutSecOctOID)
		setOID(&p.InSecPpsOID, dev.InSecPpsOID)
		setOID(&p.OutSecPpsOID, dev.OutSecPpsOID)

		p.Filters = append(p.Filters, dev.DefaultFilter...)
		p.AddInfos = append(p.AddInfos, dev.AddInfos...)

		if dev.Thresholds != nil {
			p.Thresholds = p.Thresholds.Overlay(*dev.Thresholds)
		}
	}
	return p
}

func setOID(dst *string, v string) {
	if v = NormalizeOID(v); v != "" {
		*dst = v
	}
}

// NormalizeOID drops the leading dot that MIB browsers like to print. Values
// are looked up against the dotless form the agent sends back, so ".1.3.6.1"
// would never match — and it fails silently: the reading is simply absent and
// a vendor rate column falls back to our own delta without saying so.
func NormalizeOID(s string) string {
	return strings.TrimPrefix(strings.TrimSpace(s), ".")
}

// parseInt mimics Ruby's String#to_i: leading digits count, anything else
// yields zero. Readings like "31 C" have to keep comparing as 31.
func parseInt(s string) int64 {
	s = strings.TrimSpace(s)
	neg := strings.HasPrefix(s, "-")
	if neg || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	var n int64
	for i := 0; i < len(s) && s[i] >= '0' && s[i] <= '9'; i++ {
		n = n*10 + int64(s[i]-'0')
	}
	if neg {
		return -n
	}
	return n
}
