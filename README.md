# snmpscan

Network device monitoring tool for the CLI. Shows live interface rates of a
router or switch in a scrollable table.

```
snmpscan -c <community> -h <host> [-r <pattern>] [-i <seconds>]
```

`q` quits, `r` polls immediately, arrow keys and PgUp/PgDn scroll. `n` `t` `e`
`a` sort by name, traffic, errors or alias; pressing the same key again
reverses the direction. Traffic and errors start with the biggest first, names
and aliases from A to Z.

The footer says which rows are on screen — `41-59 of 172`, or `all 10` when
nothing is scrolled away. Sorted by traffic, a screen with the busiest ports
above the fold looks no different from the top of the list otherwise.

The cursor stays on its interface rather than on a line number, so re-sorting
carries it along. It only follows while the row stays on screen: tview scrolls
the view to wherever the cursor is, and a re-sort must not yank the table off
to some row far down the list.

Name and alias sort right away. Traffic and errors would make rows jump while
counters are still arriving, so they keep the order from the end of the last
poll and re-rank once it completes. The sorted column is marked in the header,
the active key underlined in the footer.

A `•` in the first column means the row's values came back during the current
poll. A row still waiting keeps showing the previous poll's numbers rather
than blanking, so a slow agent does not make the table flicker.

The table is left on screen when you quit.

## Build

```
go build -o snmpscan ./cmd/snmpscan
```

## Configuration

Read from `/etc/snmpscan`, `~/.snmpscan` and `./.snmpscan`, in that order —
the more specific location wins.

`*.device` files describe per-vendor behaviour, selected by matching `name`
against the target's sysDescr. All entries that match are merged: scalars from
the highest `prio` win, `default_filter` and `add_infos` accumulate. Filters
are OR-ed, so an entry listing its own widens what the more generic entry
already matched rather than narrowing it.

`add_infos` are only fetched with `-a`.

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

For `type: same` the first matching case wins:

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

A file that does not parse stops the run, naming the file and the line — a
profile that fell out silently would cost the CPU reading or the filter without
anything on screen saying so. `-ignore-broken-configs` starts anyway and lists
the skipped files as warnings above the table. That is the way past an old Ruby
era `.device` left in `/etc/snmpscan`, whose `:name:` style keys this version
does not read.

`snmpscan.yml` holds the global settings. Without it the built-in defaults
apply, so it is optional. See `snmpscan.yml.example`.

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

Rates are decimal Mbit/s, so a gigabit port at line rate reads 1000. The Ruby
version divided by 1024 twice and reported about 5% low under the same label.
Packet rates are shown in thousands. Both units are in the column heading
rather than in every cell, which is what keeps the rate columns narrow enough
to leave the alias room.

The `pps` thresholds stay in whole packets, so `pps: 100` still means 100
packets a second even though the column rounds that down to `0`.

A `.device` may carry its own `thresholds:` block to override these for one
kind of hardware.

## How it polls

Polling runs as two independent jobs.

**Discovery** walks ifName and ifAlias, applies the filter and maintains the
list of interfaces to show. It is by far the expensive half: those two walks
are sequential by nature, since each GETBULK needs the OID the previous one
returned. On a QFX5100 with 590 interfaces they are about 120 of 150 packets
and take some 4s — on a loaded device with many logical interfaces, minutes.

It therefore publishes each interface the moment it is found rather than at
the end of the walk, so polling starts on the first few ports while the rest
are still being discovered. A walk that breaks off partway changes nothing,
since it only saw a prefix of the table.

**Polling** reads the counters of the interfaces already on the list. Each
request carries whole interfaces only — never a row split across two packets —
so every response completes the rows it covers and goes straight on screen.
These requests are independent of each other and run in parallel across
`-sessions` connections. On the same QFX5100: 31 packets, 0.4s.

The two run on separate sessions and do not wait for each other. Discovery
keeps pace with the polls by default; if it takes longer than one interval it
simply finishes late, without holding up the values. Set `-discover` to run it
less often — on a large device that is most of the traffic — or to a negative
value to run it exactly once at startup.

While that first discovery is still walking, an interface joining the list cuts
the wait for the next poll short, down to two seconds — otherwise the six ports
found in the first second would sit there unpolled for a whole interval while
the rest arrive. Interfaces the filter drops do not count, and once the walk has
completed the normal interval applies again. An interval below two seconds is
used as is.

The shortest interval is one second. Below that the poll spends the device's CPU
rather than measuring it, and the agent's own clock has no room to advance
between two reads — which is what the rates are calculated from.

An interface removed between two discovery runs answers noSuchObject or
noSuchInstance on every counter, and that denial is what tells it apart from one
that merely timed out or from a response that arrived without its varbinds: only
an explicit denial drops a row, at once. A port the walk stopped listing needs
two walks in a row to miss it, because an agent that cuts a walk short reports
that as a clean end.

`-maxrep` sets GETBULK max-repetitions. The default of 10 is the value the
icinga checkgo plugins use across the fleet; raising it speeds up healthy
agents but slow ones start timing out.
