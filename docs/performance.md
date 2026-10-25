# Performance

a-stack has a stress test, `scripts/bench.py`, that builds a Session like one after weeks of heavy use and then runs many Agents against it at once. This page lists what it measured for the speed fixes in the next release and how to run it yourself.

## What the stress test does

1. It builds a-stack from the current source.
2. It fills a new, temporary state directory with a busy Session: 20 Agents, 2,000 Tasks, 2,424 dependencies and 50,000 Task mail messages. The backup of this database is 107 MB. It never reads or changes your own a-stack data.
3. It runs real `a-stack` CLI processes in a loop, the way Agents work: check mail, update a Task, send mail. It does this with 1, 10 and 20 Agents at the same time, for 30 seconds each, while 2 browser views poll the Session.
4. It records commands per second, the time each command takes, the service's CPU time and memory, and Go benchmarks of single operations.

## Results

Measured on a 12-core laptop on AC power, with other programs running. The old and the new code ran in turns, two rounds each. The ranges show both rounds.

| Agents at once | Commands per second, before → after | `mail check` time, typical (p50) before → after | `mail check` time, slowest 1% (p99) before → after |
| ---: | --- | --- | --- |
| 1 | 110–122 → 179–194 | 10 ms → 4.5–4.7 ms | 28–32 ms → 10 ms |
| 10 | 216–232 → 422–658 | 40–42 ms → 12–18 ms | 126–148 ms → 45–110 ms |
| 20 | 187–212 → 475–493 | 80–85 ms → 29–30 ms | 299–514 ms → 174–196 ms |

- The service now uses 1.5–1.7 ms of CPU per command instead of about 6 ms.
- No command failed with `storage_unavailable`. The only errors were the expected revision conflicts when two Agents changed the same Task.
- Service memory peaks at 48–50 MB instead of 35–37 MB, and garbage collection runs about 9 times less often.

Single operations, median of 3 rounds:

| Operation | Before | After |
| --- | ---: | ---: |
| `mail check` with no new mail, 185 Tasks | 21.1 ms | 2.7 ms |
| `mail check` catching up on all mail | 251 ms | 89 ms |
| `task show` | 103 µs | 38 µs |
| `task status` | 1.35 ms | 0.98 ms |
| One full refresh of the browser view | 225 ms | 85 ms |
| Worst-case `dependency add` (cycle check) | 70 ms | 0.76 ms |

## Storage size

Schema 8 stores Task texts and Task mail in rowid tables, and new Tasks no longer store an empty description or the fixed "Not started" reason. Measured with the database of the stress test after its runs, and with a smaller Session that has realistic long texts:

| Database | Backup before | Backup after |
| --- | ---: | ---: |
| 2,000 Tasks, 50,000 short mail messages | 91.0 MB | 80.0 MB (12% smaller) |
| 500 Tasks, two 1–3 KB report versions each, 10 mail messages each (2 of them 1–3 KB) | 18.5 MB | 14.6 MB (21% smaller) |

- Task texts take 9.0 MB instead of 16.2 MB, and the mail indexes 5.3 MB instead of 9.4 MB, in the first database.
- A new Task with no description takes about 1,060 bytes instead of 1,820; with a short description, about 1,460 instead of 1,840.
- The migration to schema 8 added 2 to 3 seconds to the start of the first database. It leaves the freed pages in the file (113.1 MB with 32.0 MB free); a backup and restore gives the compact file.
- A `--quick` run of the stress test in turns with the old code showed no slowdown: 193–200 instead of 133–155 commands per second with 1 Agent, and a typical `mail check` of 4.2 ms instead of 6.5–6.9 ms. Quick runs are noisy, so these show only that nothing got slower.
- After a burst of writes, SQLite truncates the WAL file back to at most 8 MiB.

## What changed

- SQLite keeps prepared statements in a cache instead of preparing each query again.
- `mail check` no longer checks each Task's access twice in the same transaction, and it reads only the cursors of the Tasks it scans.
- The dependency cycle check uses a fixed join order, so its speed does not depend on table statistics.
- The service runs Go's garbage collector less often (`GOGC=400`, unless you set `GOGC` yourself).
- When the service is busy, the browser view says so instead of saying that it cannot reach the service.

## Known limits

- All commands share one database connection. With more than about 4 busy Agents, commands wait in line, so each one takes longer while the total rate stays about the same.
- `a-stack storage backup` holds that connection for about 0.4–0.5 s at this size, so other commands wait during a backup.
- While Agents change Tasks without pause, the browser view can fall behind by many seconds, because a refresh starts again when the data changes during it.

## Run it

```sh
GOTOOLCHAIN=go1.27.3 python3 scripts/bench.py --out /absolute/path/outside/the/repo --quick
```

`--quick` is a short smoke run. Without it, a full run takes about 25 minutes. `--out` must be an absolute folder outside any Git checkout, on a normal disk (not tmpfs), so that disk syncs cost what they really cost. The script deletes its temporary state when it ends, unless you add `--keep`. Run `python3 scripts/bench.py --help` for the options to change the Session size, the number of Agents and the run time.

Laptop power settings change the numbers a lot: a run on battery was about 2.2 times slower. Compare only runs made under the same conditions.
