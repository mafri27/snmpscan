# snmpscan

Live interface rates of a router or switch, in a scrollable terminal table.

```
 System: 2001:db8::1   Sysname: sw01   CPU: 41 2   172 ports   reload in 7s   (37 req, 473ms)

   ifNr      Name ▲                          Mbps in  Mbps out   Kpps in  Kpps out    Errors Alias
 • 1069      et-0/0/100                         4279      4274       399       381         0 core01_re0 - 1
 • 519       et-0/0/101                         5052      4493       463       400         0 core02_re0 - 2
 • 887       xe-0/0/0                            543       452        24        26         0
   888       xe-0/0/1                            766       484        32        28         0

 q quit   r refresh   sort n/t/e/a ▲   ↑↓ PgUp PgDn scroll   1-19 of 172
```

```
snmpscan -c <community> -h <host> [-r <pattern>] [-i <seconds>]
```

SNMPv2c only for now; an IPv6 target needs no brackets. `make install` puts the
binary in `$(PREFIX)/bin` and the profiles in `/etc/snmpscan`.

## Keys

`q` quits and leaves the table on screen. `r` polls now. Arrows and PgUp/PgDn
scroll. `n` `t` `e` `a` sort by name, traffic, errors or alias — same key again
reverses. The sorted column is marked in the header, the active key underlined
below.

The cursor follows its interface through a re-sort, not the line number. Traffic
and errors re-rank only when a poll completes, so rows do not jump while counters
are still arriving.

## Reading it

Rates are decimal — a gigabit port at line rate reads `1000` — and packet rates
are in thousands. The units are in the heading so the columns stay narrow enough
to leave the alias room.

`•` means this row's values are from the current poll. Without it the row shows
what the poll before found, which beats blanking it while a slow agent is still
answering. Rows below the `dim` thresholds are greyed out, rows reaching `alert`
turn red.

The footer says which rows are on screen: `1-19 of 172`, or `all 10` when nothing
is scrolled away.

`-a` adds the extra readings above the table, and which profiles matched:

```
 Profile                      ^Juniper Networks.* (juniper.device)
 Temperatur                   30
 Alarm                        NO
```

Failures land there too, one line each, named by request and counted rather than
repeated — `counters: request timeout (after 1 retries) (x3)`. A lost request
does not fail the poll: what arrived is shown, the rest keeps its old values.

## Flags

| | |
|---|---|
| `-h` `-c` `-p` | host, community, port (161) |
| `-r` | only interfaces matching this pattern; repeatable, replaces the profile's filter |
| `-i` | seconds between polls (10, at least 1) |
| `-discover` | how often to re-read the interface list (same as `-i`); negative walks once at startup |
| `-a` | show the extra readings |
| `-timeout` `-retries` | per request (2s, 2) |
| `-sessions` | parallel conversations (8, max 64) |
| `-maxrep` | GETBULK max-repetitions (10) |
| `-ignore-broken-configs` | start even when a config file does not parse |

## Configuration

Read from `/etc/snmpscan`, `~/.snmpscan` and `./.snmpscan`, all optional. For
`snmpscan.yml` the most specific location wins; device profiles all merge.

```yaml
interval: 10
thresholds:
  dim:                  # below this a row is greyed out
    mbit: 5
    pps: 100
  alert:                # at or above this it turns red
    mbit: 700
    pps: 100000
    errors: 1
```

`pps` limits are whole packets, so `pps: 100` means 100 even where the column
rounds it to `0`.

`*.device` files describe per-vendor behaviour, picked by matching `name` against
the sysDescr. Every match merges: scalars from the highest `prio` win, filters
and readings accumulate. Filters are OR-ed, so a specific entry widens what a
generic one matched.

```yaml
- name: "^Juniper Networks.*"
  prio: 1
  cpu_oid: 1.3.6.1.4.1.2636.3.1.13.1.8.9
  # The agent reports rates itself; sec_value_factor divides them, bits to bytes.
  sec_value_factor: 8
  in_sec_oct_oid: 1.3.6.1.4.1.2636.3.3.1.1.7
  add_infos:
  - oid: 1.3.6.1.4.1.2636.3.1.13.1.7.9.1.0.0
    name: Temperatur
    type: max          # max | min | same
    relation: 40
  default_filter:
  - '^(et|xe|ge)-[0-9]+/[0-9]+/[0-9]+(:[0-9]+)?$'
```

For `type: same` the first matching case wins; an empty `test` is the catch-all:

```yaml
    type: same
    relation:
    - test: noSuchObject
      output: ERROR
      error: true
    - test: ''
      output: OK
      error: false
```

A profile may carry its own `thresholds:` block. A file that does not parse stops
the run, naming file and line — `-ignore-broken-configs` starts anyway and lists
it as a warning. That is the way past a `.device` from the old Ruby version.

## How it polls

Two jobs on separate sessions, neither waiting for the other.

**Discovery** walks ifName and ifAlias and maintains the interface list. It is
the expensive half — the walks are sequential by nature — and on a switch with
590 ifName entries takes some 3.4s, on a loaded device minutes. It publishes each
interface as it is found, so polling starts on the first ports while the rest
arrive. During that first walk a new interface cuts the wait for the next poll to
two seconds.

**Polling** reads the counters of what is on the list, in parallel across
`-sessions`. Each request carries whole interfaces only, so every response
completes the rows it covers. Same switch, 172 ports after the filter: 37
requests, 0.42s.

On a large device discovery is most of the traffic, so `-discover` is worth
setting. The shortest interval is one second: below that the agent's clock has no
room to advance between two reads, and that is what the rates come from.

A port that answers noSuchObject on every counter is gone and its row disappears
at once — that denial is what tells it apart from a timeout. One the walk stopped
listing needs two rounds, because an agent cutting a walk short reports it as a
clean end.

`-maxrep 10` is the value the icinga checkgo plugins use across the fleet;
raising it speeds up healthy agents but slow ones start timing out. A GET carries
45 varbinds, deliberately below the protocol limit of 60: that many ifXTable
counters come to ~1.8 kB, which a small agent answers with `tooBig`.
