// Command snmpscan shows live interface counters of a network device.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/mafri27/snmpscan/internal/config"
	"github.com/mafri27/snmpscan/internal/poll"
	"github.com/mafri27/snmpscan/internal/ui"
)

const version = "2.0.0"

// maxSessions is a sanity limit, not a tuned one: every session dials its own
// UDP socket at startup, so an absurd -sessions would fail on the fd limit with
// a connect error that says nothing about the cause.
const maxSessions = 64

// stringList collects a flag that may be repeated, like -r.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

type options struct {
	host         string
	port         uint
	community    string
	filters      stringList
	interval     int
	discovery    time.Duration
	readings     bool
	timeout      time.Duration
	retries      int
	sessions     int
	maxRep       uint
	ignoreBroken bool
	version      bool
}

// register declares the command line. Split out so a test can build the same
// FlagSet the program does.
func register(fs *flag.FlagSet, opts *options) {
	fs.StringVar(&opts.host, "h", "", "IP address or hostname of the target system")
	fs.UintVar(&opts.port, "p", 161, "SNMP port of the target system")
	fs.StringVar(&opts.community, "c", "", "SNMP community")
	fs.Var(&opts.filters, "r", "only show interfaces matching this pattern (repeatable)")
	fs.IntVar(&opts.interval, "i", 0, "seconds between polls, at least 1")
	fs.DurationVar(&opts.discovery, "discover", 0, "how often to re-read the interface list; negative runs it once at startup")
	fs.BoolVar(&opts.readings, "a", false, "also show the additional system readings")
	fs.DurationVar(&opts.timeout, "timeout", 2*time.Second, "SNMP request timeout")
	fs.IntVar(&opts.retries, "retries", 2, "SNMP retries per request")
	fs.IntVar(&opts.sessions, "sessions", poll.DefaultSessions, "parallel SNMP sessions")
	fs.UintVar(&opts.maxRep, "maxrep", 0, "GETBULK max-repetitions; raise it for a faster poll on healthy agents")
	fs.BoolVar(&opts.ignoreBroken, "ignore-broken-configs", false, "start anyway when a config file does not parse, listing it as a warning")
	fs.BoolVar(&opts.version, "version", false, "print version and exit")
	fs.BoolVar(&opts.version, "v", false, "print version and exit")
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "snmpscan:", err)
		os.Exit(1)
	}
}

func run() error {
	var opts options

	fs := flag.NewFlagSet("snmpscan", flag.ExitOnError)
	fs.Usage = usage(fs)
	register(fs, &opts)
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	// Which flags the user actually gave. A sentinel default cannot tell "-i 0"
	// from "no -i", and it made "-discover -1s" unreachable although the help
	// documented it.
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })

	if opts.version {
		fmt.Printf("snmpscan %s\n", version)
		return nil
	}
	if opts.host == "" || opts.community == "" {
		fs.Usage()
		return fmt.Errorf("both -h and -c are required")
	}
	if err := validate(opts, given); err != nil {
		return err
	}

	set, err := config.Load(config.SearchDirs())
	if err != nil {
		return err
	}
	// A profile that does not parse is not silently half-applied: the run
	// stops unless the operator says otherwise, because a missing CPU OID or
	// filter is not obvious from the screen.
	warnings, err := configWarnings(set.Broken, opts.ignoreBroken)
	if err != nil {
		return err
	}
	if given["i"] {
		set.Settings.Interval = &opts.interval
	}

	// Draw in the normal buffer instead of the alternate screen, so the last
	// table is still on the terminal after quitting. tcell only clears on exit
	// when it has an alternate screen to leave.
	if err := os.Setenv("TCELL_ALTSCREEN", "disable"); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	poller, err := poll.New(ctx, poll.Options{
		Target: poll.Target{
			Host:           opts.host,
			Port:           uint16(opts.port),
			Community:      opts.community,
			Timeout:        opts.timeout,
			Retries:        opts.retries,
			MaxRepetitions: uint32(opts.maxRep),
		},
		Config:   set,
		Filters:  opts.filters,
		Readings: opts.readings,
		Sessions: opts.sessions,
		Warnings: warnings,
	})
	if err != nil {
		return err
	}
	defer poller.Close()

	interval := config.Seconds(set.Settings.Interval, config.DefaultInterval)
	// Discovery keeps pace with the polls unless told otherwise; it runs
	// alongside them on its own sessions.
	discovery := config.Seconds(set.Settings.Discovery, int(interval.Seconds()))
	if given["discover"] {
		discovery = opts.discovery
	}
	err = ui.New(poller, opts.host, interval, discovery).Run(ctx)
	// The table stays on screen, so give the shell prompt its own line.
	fmt.Println()
	return err
}

// validate rejects values that would otherwise fail somewhere far from the
// flag that caused them: silently truncated to a narrower type, or turned into
// a device-shaped error message by the SNMP library.
func validate(opts options, given map[string]bool) error {
	// -p 65536 would come out as uint16(0) and then quietly become 161.
	if opts.port == 0 || opts.port > 65535 {
		return fmt.Errorf("-p %d is not a port", opts.port)
	}
	if opts.maxRep > math.MaxUint32 {
		return fmt.Errorf("-maxrep %d is out of range", opts.maxRep)
	}
	if opts.sessions < 1 || opts.sessions > maxSessions {
		return fmt.Errorf("-sessions %d is outside 1..%d", opts.sessions, maxSessions)
	}
	if given["i"] && opts.interval < config.MinInterval {
		return fmt.Errorf("-i %d: an interval of less than a second polls the device to death", opts.interval)
	}
	// A negative -discover is a real setting: walk once and never again. Between
	// that and the minimum there is nothing sensible — the two walks are the
	// expensive half, and back to back they are all the agent would be doing.
	if least := config.MinInterval * time.Second; given["discover"] && opts.discovery >= 0 && opts.discovery < least {
		return fmt.Errorf("-discover %v: use at least %v, or a negative value to walk once at startup", opts.discovery, least)
	}
	// gosnmp turns a non-positive timeout into a deadline of "now", so every
	// request fails with a message that reads like the device's fault.
	if opts.timeout <= 0 {
		return fmt.Errorf("-timeout %v must be positive", opts.timeout)
	}
	return nil
}

// configWarnings decides what a file that would not parse means. Refusing to
// start is the default: a profile that fell out silently costs the CPU reading
// or the filter, and nothing on screen would say so. With -ignore-broken-configs
// the run goes ahead, but the files stay visible as warnings rather than being
// dropped without a word.
func configWarnings(broken []error, ignore bool) ([]string, error) {
	if len(broken) == 0 {
		return nil, nil
	}
	if !ignore {
		return nil, fmt.Errorf("%w\nuse -ignore-broken-configs to start without them", errors.Join(broken...))
	}
	warnings := make([]string, 0, len(broken))
	for _, err := range broken {
		warnings = append(warnings, poll.Warning("ignored config", err))
	}
	return warnings, nil
}

// usage is written by hand rather than from fs.PrintDefaults, which sorts
// alphabetically and puts every flag on two lines. TestUsageListsEveryFlag
// guards the obvious way this goes wrong — a flag added here or there but not
// in both.
func usage(fs *flag.FlagSet) func() {
	return func() {
		out := fs.Output()
		fmt.Fprintf(out, `
  snmpscan -c <community> -h <host> [options]

  SNMPSCAN version %s

  -h            IP address or hostname of the target system
  -c            SNMP community
  -p            SNMP port (default 161)
  -r            only show interfaces matching this pattern (repeatable)
  -i            seconds between polls (default 10, at least 1)
  -discover     how often to re-read the interface list (defaults to -i);
                runs alongside the polls, not as part of them. A negative
                value walks once at startup and then never again
  -a            also show the additional system readings
                (temperature, alarms; off by default)

  -timeout      SNMP request timeout (default 2s)
  -retries      SNMP retries per request (default 2)
  -sessions     parallel SNMP sessions (default 8, at most 64)
  -maxrep       GETBULK max-repetitions (default 10, raise it for a
                faster poll on healthy agents)

  -ignore-broken-configs
                start even when a config file does not parse; the files
                are listed as warnings instead of stopping the run

  -help         display this help and exit
  -version      output version information and exit

  Configuration is read from %s and *.device in
  /etc/snmpscan, ~/.snmpscan and ./.snmpscan.

`, version, config.SettingsFile)
	}
}
