# snmpscan

Live interface rates of a router or switch, in a scrollable terminal table.

```
 System: 2001:db8::1   Sysname: sw01   CPU: 41 2   172 ports   reload in 7s   (37 req, 473ms)

   ifNr      Name ▲                          Mbps in  Mbps out   Kpps in  Kpps out    Errors Alias
 • 1069      et-0/0/100                         4279      4274       399       381         0 core01_re0 - 1
 • 519       et-0/0/101                         5052      4493       463       400         0 core02_re0 - 2
 • 1063      et-1/0/100                         4591      4610       424       409         0 core02_re0 - 1
 • 531       et-1/0/101                         3899      4494       366       401         0 core01_re0 - 2
 • 887       xe-0/0/0                            543       452        24        26         0
 • 888       xe-0/0/1                            766       484        32        28         0
 • 889       xe-0/0/2                            444       463        26        19         0

 q quit   r refresh   sort n/t/e/a ▲   ↑↓ PgUp PgDn scroll   1-19 of 172
```

## Install

```
make            # build ./snmpscan
make install    # into $(PREFIX)/bin, profiles into /etc/snmpscan
make test
```

Needs Go and nothing else at runtime.

## Usage

```
snmpscan -c <community> -h <host> [-r <pattern>] [-i <seconds>]
```

SNMPv2c only for now. An IPv6 target is used as such, no brackets needed.

| Flag | |
|---|---|
| `-h` | IP address or hostname |
| `-c` | community |
| `-p` | port, default 161 |
| `-r` | only show interfaces matching this pattern; repeatable, replaces the profile's filter |
| `-i` | seconds between polls, default 10, at least 1 |
| `-discover` | how often to re-read the interface list, default the same as `-i`; a negative value walks once at startup |
| `-a` | also show the extra readings (temperature, alarms) and which profiles matched |
| `-timeout` | per request, default 2s |
| `-retries` | per request, default 2 |
| `-sessions` | parallel SNMP conversations, default 8, at most 64 |
| `-maxrep` | GETBULK max-repetitions, default 10 |
| `-ignore-broken-configs` | start even when a config file does not parse |

`-help` lists them too, `-version` prints the version.

## Keys

| | |
|---|---|
| `q`, `Esc` | quit — the table stays on screen |
| `r` | poll now, without waiting out the interval |
| `↑` `↓` `PgUp` `PgDn` | scroll |
| `n` `t` `e` `a` | sort by name, traffic, errors or alias |

Pressing the same sort key again reverses the direction. Traffic and errors start
with the biggest first, names and aliases from A to Z. The sorted column is
marked in the header, the active key underlined in the footer.

Name and alias sort right away. Traffic and errors would make rows jump while
counters are still arriving, so they keep the order from the end of the last poll
and re-rank once it completes.

The cursor stays on its interface rather than on a line number, so re-sorting
carries it along — as long as the row stays on screen. tview scrolls the view to
wherever the cursor is, and a re-sort must not yank the table off to some row far
down the list.

## Reading the screen

**Rates** are decimal: a gigabit port at line rate reads `1000` under `Mbps`.
Packet rates are in thousands. Both units live in the column heading rather than
in every cell, which is what keeps the columns narrow enough to leave the alias
room.

**`•` in the first column** means this row's values came back during the current
poll. A row still waiting keeps showing the previous poll's numbers rather than
blanking, so a slow agent does not make the table flicker:

```
   ifNr      Name ▲                          Mbps in  Mbps out   Kpps in  Kpps out    Errors Alias
 • 1069      et-0/0/100                         4279      4274       399       381         0 core01_re0 - 1
   519       et-0/0/101                         5052      4493       463       400         0 core02_re0 - 2
```

The second row is from the poll before — the agent has not answered for it yet.

**Colours** follow the thresholds: a row whose rates all stay below `dim` is
greyed out as uninteresting, one reaching `alert` turns red.

**The footer** says which rows are on screen, `1-19 of 172` or `all 10` when
nothing is scrolled away. Sorted by traffic, a screen with the busiest ports
above the fold otherwise looks no different from the top of the list.

**With `-a`** the block above the table carries the extra readings and the
profiles the sysDescr matched:

```
 Profile                      ^Juniper Networks.* (juniper.device)
 Memoryusage                  37
 Temperatur                   30
 Alarm                        NO
```

A reading over its limit turns red — including one whose OID answers with
something that is not a number at all, which is a reading that is not there
rather than a value within limits.

**Warnings** appear in the same block, one line each, and name the request that
failed. Identical failures are counted instead of repeated:

```
 counters: request timeout (after 1 retries) (x3)
 discovery: walk 1.3.6.1.2.1.31.1.1.1.1: request timeout (after 1 retries)
```

A single lost request does not fail the poll. What did arrive is shown, the rest
keeps the previous values, and the header still counts down to the next round.

## Configuration

Read from `/etc/snmpscan`, `~/.snmpscan` and `./.snmpscan`. For `snmpscan.yml`
the more specific location wins; device profiles all merge, see below.

Everything is optional — without any configuration the built-in defaults apply.

### snmpscan.yml

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

The `pps` thresholds are in whole packets, so `pps: 100` means 100 packets a
second even where the column rounds that down to `0`.

See `snmpscan.yml.example` for the same with comments.

### Device profiles

`*.device` files describe per-vendor behaviour, selected by matching `name`
against the target's sysDescr. **All** entries that match are merged: scalars
from the highest `prio` win, `default_filter` and `add_infos` accumulate. Filters
are OR-ed, so an entry listing its own widens what a more generic entry already
matched rather than narrowing it.

```yaml
- name: "^Juniper Networks.*"
  prio: 1
  cpu_oid: 1.3.6.1.4.1.2636.3.1.13.1.8.9
  # The agent reports rates itself; use them instead of our own delta.
  # sec_value_factor divides them, here bits to bytes.
  sec_value_factor: 8
  in_sec_oct_oid: 1.3.6.1.4.1.2636.3.3.1.1.7
  add_infos:
  - oid: 1.3.6.1.4.1.2636.3.1.13.1.7.9.1.0.0
    name: Temperatur
    type: max          # max | min | same
    relation: 40
  default_filter:
  - '^(et|xe|ge)-[0-9]+/[0-9]+/[0-9]+(:[0-9]+)?$'
- name: "^Juniper Networks, Inc. ex..00-48t.*"
  prio: 2
  # Scalars from the higher prio win, so this replaces the CPU OID above.
  cpu_oid: 1.3.6.1.4.1.2636.3.1.13.1.8.9.1.0
```

`add_infos` are only fetched with `-a`. For `type: same` the first matching case
wins:

```yaml
    type: same
    relation:
    - test: noSuchObject
      output: ERROR
      error: true
    - test: ''          # empty matches anything: the catch-all
      output: OK
      error: false
```

A profile may carry its own `thresholds:` block to override the global ones for
one kind of hardware.

### When a config file does not parse

The run stops, naming the file and the line. A profile that fell out silently
would cost the CPU reading or the filter without anything on screen saying so:

```
snmpscan: /etc/snmpscan/legacy.device: yaml: unmarshal errors:
  line 2: field :name not found in type config.Device
use -ignore-broken-configs to start without them
```

`-ignore-broken-configs` starts anyway and lists the skipped files as warnings
above the table. That is the way past a `.device` from the old Ruby version,
whose `:name:` style keys this one does not read.

## How it polls

Two independent jobs, on separate sessions, neither waiting for the other.

**Discovery** walks ifName and ifAlias, applies the filter and maintains the list
of interfaces to show. It is by far the expensive half: the two walks are
sequential by nature, since each GETBULK needs the OID the previous one returned.
On a switch whose ifName table has 590 entries they take some 3.4s — on a loaded
device with many logical interfaces, minutes.

It therefore publishes each interface the moment it is found rather than at the
end of the walk, so polling starts on the first few ports while the rest are
still being discovered. While that is going on, an interface joining the list
cuts the wait for the next poll short, down to two seconds — otherwise the six
ports found in the first second would sit there unpolled for a whole interval.
Once the walk has completed, the normal interval applies again.

**Polling** reads the counters of the interfaces already on the list. Each
request carries whole interfaces only — never a row split across two packets — so
every response completes the rows it covers and goes straight on screen. The
requests are independent and run in parallel across `-sessions` connections. On
the same switch, 172 ports surviving the filter: 37 requests, 0.42s.

Discovery keeps pace with the polls by default. If it takes longer than one
interval it simply finishes late, without holding up the values. On a large
device it is most of the traffic, so `-discover` is worth setting — less often,
or a negative value to walk exactly once at startup.

The shortest interval is one second. Below that a poll spends the device's CPU
rather than measuring it, and the agent's own clock has no room to advance
between two reads — which is what the rates are calculated from.

### When an interface disappears

A removed port answers noSuchObject or noSuchInstance on every counter, and that
denial is what tells it apart from one that merely timed out, or from a response
that arrived without its varbinds. Only an explicit denial drops a row, and it
does so at once.

The port keeps being polled while it stays in the walk, though — a handful of
varbinds for a row nobody sees, and the reason a port that starts answering again
comes back on its own. One the walk stopped listing needs two rounds to miss it,
because an agent that cuts a walk short reports that as a clean end.

### Tuning

`-maxrep` sets GETBULK max-repetitions. The default of 10 is the value the icinga
checkgo plugins use across the fleet; raising it speeds up healthy agents, but
slow ones start timing out.

`-sessions` spreads the counter requests. Measured against a switch with 590
interfaces: 8 sessions poll in 5.8s where 4 need 7.5s. Beyond that the agent, not
the client, is the bottleneck.

A GET carries at most 45 varbinds, below the protocol limit of 60 on purpose: 60
ifXTable counters come to roughly 1.8 kB, and an agent with a small buffer
answers that with `tooBig` instead of splitting it.
