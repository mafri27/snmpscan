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
	return c.pattern.MatchString(value)
}

// Evaluate returns the text to display for a reading and whether it should be
// flagged. For KindSame the first matching case wins; the Ruby version printed
// every match, so riverstone's "noSuchObject means ERROR" case showed up right
// next to its own catch-all "OK".
func (a *AddInfo) Evaluate(value string) (out string, isError bool, ok bool) {
	switch a.Kind {
	case KindSame:
		for i := range a.Cases {
			if a.Cases[i].Matches(value) {
				return a.Cases[i].Output, a.Cases[i].IsError, true
			}
		}
		return "", false, false
	case KindMax:
		return value, parseInt(value) > a.Limit, true
	case KindMin:
		return value, parseInt(value) < a.Limit, true
	}
	return "", false, false
}

var addInfoKeys = map[string]bool{"oid": true, "name": true, "type": true, "relation": true}

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
	// Decode ignores unknown keys here, so a typo would silently disable a
	// reading. Reject it instead.
	for i := 0; i+1 < len(n.Content); i += 2 {
		if !addInfoKeys[n.Content[i].Value] {
			return fmt.Errorf("line %d: unknown add_info key %q", n.Content[i].Line, n.Content[i].Value)
		}
	}

	a.OID = strings.TrimSpace(raw.OID)
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
