package config

// Thresholds decide the colour of an interface row. Values below Dim are
// painted grey, values at or above Alert red.
type Thresholds struct {
	Dim   Level `yaml:"dim"`
	Alert Level `yaml:"alert"`
}

// Level is a set of limits. A nil field means "inherit whatever was
// configured further up", which lets a .device override only the numbers it
// actually cares about.
type Level struct {
	Mbit   *int64 `yaml:"mbit"`
	Pps    *int64 `yaml:"pps"`
	Errors *int64 `yaml:"errors"`
}

// DefaultThresholds reproduces the constants that were hardcoded in the Ruby
// version, so an installation without snmpscan.yml keeps its old colours.
func DefaultThresholds() Thresholds {
	return Thresholds{
		Dim:   Level{Mbit: ptr(int64(5)), Pps: ptr(int64(100))},
		Alert: Level{Mbit: ptr(int64(700)), Pps: ptr(int64(100000)), Errors: ptr(int64(1))},
	}
}

func ptr[T any](v T) *T { return &v }

// Overlay returns t with every field the other side actually set replaced.
func (t Thresholds) Overlay(o Thresholds) Thresholds {
	t.Dim = t.Dim.overlay(o.Dim)
	t.Alert = t.Alert.overlay(o.Alert)
	return t
}

func (l Level) overlay(o Level) Level {
	if o.Mbit != nil {
		l.Mbit = o.Mbit
	}
	if o.Pps != nil {
		l.Pps = o.Pps
	}
	if o.Errors != nil {
		l.Errors = o.Errors
	}
	return l
}
