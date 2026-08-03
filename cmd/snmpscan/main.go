// Command snmpscan shows live interface counters of a network device.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
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

// stringList collects a flag that may be repeated, like -r.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

type options struct {
	host      string
	port      uint
	community string
	filters   stringList
	interval  int
	discovery time.Duration
	mark      bool
	readings  bool
	timeout   time.Duration
	retries   int
	sessions  int
	maxRep    uint
	version   bool
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
	fs.StringVar(&opts.host, "h", "", "IP address or hostname of the target system")
	fs.UintVar(&opts.port, "p", 161, "SNMP port of the target system")
	fs.StringVar(&opts.community, "c", "", "SNMP community")
	fs.Var(&opts.filters, "r", "only show interfaces matching this pattern (repeatable)")
	fs.IntVar(&opts.interval, "i", -1, "seconds between polls; 0 polls back to back")
	fs.DurationVar(&opts.discovery, "discover", -1, "how often to re-read the interface list; 0 runs it back to back")
	fs.BoolVar(&opts.mark, "m", false, "highlight the interface holding the target's IP")
	fs.BoolVar(&opts.readings, "a", false, "also show the additional system readings")
	fs.DurationVar(&opts.timeout, "timeout", 2*time.Second, "SNMP request timeout")
	fs.IntVar(&opts.retries, "retries", 2, "SNMP retries per request")
	fs.IntVar(&opts.sessions, "sessions", 8, "parallel SNMP sessions")
	fs.UintVar(&opts.maxRep, "maxrep", 0, "GETBULK max-repetitions; raise it for a faster poll on healthy agents")
	fs.BoolVar(&opts.version, "version", false, "print version and exit")
	fs.BoolVar(&opts.version, "v", false, "print version and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	if opts.version {
		fmt.Printf("snmpscan %s\n", version)
		return nil
	}
	if opts.host == "" || opts.community == "" {
		fs.Usage()
		return fmt.Errorf("both -h and -c are required")
	}

	set, err := config.Load(config.SearchDirs())
	if err != nil {
		return err
	}
	if opts.interval >= 0 {
		set.Settings.Interval = &opts.interval
	}

	markIP := ""
	if opts.mark {
		// ipAddrTable is indexed by address, so a hostname has to be resolved
		// first — the Ruby version compared the raw argument and silently
		// marked nothing whenever -h was not an IP.
		if markIP, err = resolveIP(opts.host); err != nil {
			return err
		}
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
		MarkIP:   markIP,
		Readings: opts.readings,
		Sessions: opts.sessions,
	})
	if err != nil {
		return err
	}
	defer poller.Close()

	interval := config.Seconds(set.Settings.Interval, 10)
	// Discovery keeps pace with the polls unless told otherwise; it runs
	// alongside them on its own sessions.
	discovery := config.Seconds(set.Settings.Discovery, int(interval.Seconds()))
	if opts.discovery >= 0 {
		discovery = opts.discovery
	}
	err = ui.New(poller, opts.host, interval, discovery).Run(ctx)
	// The table stays on screen, so give the shell prompt its own line.
	fmt.Println()
	return err
}

func resolveIP(host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil {
		return host, nil
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", host, err)
	}
	for _, a := range addrs {
		if ip := net.ParseIP(a); ip != nil && ip.To4() != nil {
			return a, nil
		}
	}
	return "", fmt.Errorf("resolve %s: no IPv4 address", host)
}

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
  -m            highlight the interface holding the target's IP
  -i            seconds between polls (default 10); 0 polls back to back
  -discover     how often to re-read the interface list (defaults to -i);
                runs alongside the polls, not as part of them. 0 runs it
                back to back, a negative value switches it off
  -a            also show the additional system readings
                (temperature, alarms; off by default)

  -timeout      SNMP request timeout (default 2s)
  -retries      SNMP retries per request (default 2)
  -sessions     parallel SNMP sessions (default 8)
  -maxrep       GETBULK max-repetitions (default 10, raise it for a
                faster poll on healthy agents)

  -help         display this help and exit
  -version      output version information and exit

  Configuration is read from %s and *.device in
  /etc/snmpscan, ~/.snmpscan and ./.snmpscan.

`, version, config.SettingsFile)
	}
}
