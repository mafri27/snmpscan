package config

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Device is one entry of a .device file.
type Device struct {
	Name           string      `yaml:"name"`
	Prio           int         `yaml:"prio"`
	CPUOID         string      `yaml:"cpu_oid"`
	DefaultFilter  []string    `yaml:"default_filter"`
	AddInfos       []*AddInfo  `yaml:"add_infos"`
	SecValueFactor int         `yaml:"sec_value_factor"`
	InSecOctOID    string      `yaml:"in_sec_oct_oid"`
	OutSecOctOID   string      `yaml:"out_sec_oct_oid"`
	InSecPpsOID    string      `yaml:"in_sec_pps_oid"`
	OutSecPpsOID   string      `yaml:"out_sec_pps_oid"`
	Thresholds     *Thresholds `yaml:"thresholds"`

	pattern *regexp.Regexp
	source  string
}

// Kind selects how an AddInfo's value is judged.
type Kind string

const (
	// KindSame prints the output of the first case whose pattern matches.
	KindSame Kind = "same"
	// KindMax flags values above Limit.
	KindMax Kind = "max"
	// KindMin flags values below Limit.
	KindMin Kind = "min"
)

// AddInfo is one of the extra readings shown above the interface table,
// e.g. temperature or power supply state.
type AddInfo struct {
	OID   string
	Name  string
	Kind  Kind
	Cases []Case // KindSame
	Limit int64  // KindMax, KindMin
}

// Case maps a value to a human readable output.
type Case struct {
	Test    string `yaml:"test"`
	Output  string `yaml:"output"`
	IsError bool   `yaml:"error"`

	pattern *regexp.Regexp
}

// Matches reports whether the raw SNMP value hits this case. An empty test
// matches anything, which the riverstone profiles rely on as a catch-all.
func (c *Case) Matches(value string) bool {
	if c.pattern == nil {
		// Only UnmarshalYAML compiles the pattern, so a Case built any other
		// way has none. Falling back to the literal beats a panic that would
		// leave the terminal in full-screen mode.
		return c.Test == ""
	}
	return c.pattern.MatchString(value)
}

// Evaluate returns the text to display for a reading and whether it should be
// flagged. For KindSame the first matching case wins — printing every match put
// riverstone's "noSuchObject means ERROR" right next to its catch-all "OK".
func (a *AddInfo) Evaluate(value string) (out string, isError bool, ok bool) {
	switch a.Kind {
	case KindSame:
		for i := range a.Cases {
			if a.Cases[i].Matches(value) {
				return a.Cases[i].Output, a.Cases[i].IsError, true
			}
		}
		return "", false, false
	case KindMax, KindMin:
		n, numeric := parseInt(value)
		if !numeric {
			// An OID answering "noSuchInstance" or a word is not a value that
			// happens to be within limits — it is a reading that is not there.
			return value, true, true
		}
		if a.Kind == KindMax {
			return value, n > a.Limit, true
		}
		return value, n < a.Limit, true
	}
	return "", false, false
}

var (
	addInfoKeys = map[string]bool{"oid": true, "name": true, "type": true, "relation": true}
	caseKeys    = map[string]bool{"test": true, "output": true, "error": true}
)

// rejectUnknownKeys fails on a key that is not in allowed. Node.Decode ignores
// what it does not know, so without this a typo disables the setting it was
// meant to make and says nothing about it.
func rejectUnknownKeys(n *yaml.Node, what string, allowed map[string]bool) error {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if !allowed[n.Content[i].Value] {
			return fmt.Errorf("line %d: unknown %s key %q", n.Content[i].Line, what, n.Content[i].Value)
		}
	}
	return nil
}

func (a *AddInfo) UnmarshalYAML(n *yaml.Node) error {
	// relation is a list of cases for `same` but a bare number for max/min,
	// so it can only be decoded once the type is known.
	var raw struct {
		OID      string    `yaml:"oid"`
		Name     string    `yaml:"name"`
		Type     string    `yaml:"type"`
		Relation yaml.Node `yaml:"relation"`
	}
	if err := n.Decode(&raw); err != nil {
		return err
	}
	if err := rejectUnknownKeys(n, "add_info", addInfoKeys); err != nil {
		return err
	}

	a.OID = NormalizeOID(raw.OID)
	a.Name = raw.Name
	a.Kind = Kind(strings.ToLower(strings.TrimSpace(raw.Type)))

	if a.OID == "" {
		return fmt.Errorf("line %d: add_info %q has no oid", n.Line, a.Name)
	}
	if raw.Relation.IsZero() {
		return fmt.Errorf("line %d: add_info %q has no relation", n.Line, a.Name)
	}

	switch a.Kind {
	case KindSame:
		for _, item := range raw.Relation.Content {
			if err := rejectUnknownKeys(item, "relation", caseKeys); err != nil {
				return fmt.Errorf("add_info %q: %w", a.Name, err)
			}
		}
		if err := raw.Relation.Decode(&a.Cases); err != nil {
			return fmt.Errorf("add_info %q: relation: %w", a.Name, err)
		}
		for i := range a.Cases {
			re, err := regexp.Compile(a.Cases[i].Test)
			if err != nil {
				return fmt.Errorf("add_info %q: test %q: %w", a.Name, a.Cases[i].Test, err)
			}
			a.Cases[i].pattern = re
		}
	case KindMax, KindMin:
		if err := raw.Relation.Decode(&a.Limit); err != nil {
			return fmt.Errorf("add_info %q: relation: %w", a.Name, err)
		}
	default:
		return fmt.Errorf("line %d: add_info %q has type %q, want same, max or min", n.Line, a.Name, a.Kind)
	}
	return nil
}
