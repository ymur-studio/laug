# Runbook 6: add a fault, check it, run it four times

You write a fault's row and check it with no cell. Then you run it on a cell once, and three more times. The three,
side by side, say whether it holds.

The short way, once you know the steps:

```
bin/laug raun <fault> -dry-run           # laug finds the row; nothing is leased
make test                                # the harness reads every row, about 5 min
bin/laug raun <fault> -run-id <id>       # one raun on a cold compose cell, about 7 min
```

The third needs a pool of compose cells, which step 3 starts.

## The words on this page

- **cell**: a throwaway Temporal server on this host, with its own Postgres. This page's are built by docker compose;
  some committed rauns ran on microVM cells, laug's other kind, which boot as Firecracker microVMs.
- **pool**: the cells laug keeps warm and ready; `bin/laug pool serve` runs it. This page's pool keeps none warm.
- **lease**: take a cell from the pool for one job, then give it back.
- **cold lease**: a lease the pool builds a new cell for, when you ask.
- **raun**: one fault on one leased cell. laug loads the cell, injects the fault, reads three windows and judges.
- **verdict**: ADVANCE, the cell took the fault and came back; or BLOCK, with a reason that names the window that
  failed.
- **row**: one fault in `faults/catalog.yaml`.
- **mechanism**: the first word of a row's `inject` or `remove`, the tool that puts the fault in or takes it out.
- **signal**: the one reading that shows the fault is in, and its max.
- **budget**: the row's `budget_s`, the seconds from the fault's removal to the signal back under its max.
- **threshold**: one reading laug judges every window by, with its max. The set is in `load/steady-state.yaml`,
  measured on the lab host. p99 and p95 are the 99th and 95th percentiles: 99 or 95 in 100 were at least this fast.
- **the three windows**: before, the last 60 s of 90 s of load; during, the fault held, 120 s for most rows; after,
  90 s that start 30 s after the signal is back.
- **drain**: from the fault's removal to the after window; recorded, not judged.
- **landed**: the signal went over its max while the fault was held.
- **recovered**: once the fault was out, the signal came back under its max within the budget.
- **clean**: every threshold under its max in the after window.
- **never steady**: a threshold was over its max in the before window, so the raun injected nothing.
- **Omes**: Temporal's load generator. Every raun's load runs it.
- **the harness**: laug's tests of its own scripts, which `make test` runs on fakes, with no cell.
- **boot**: one batch of runs on the lab host, with its record in `cells/results/`. Boot 2 ran this page's fault four
  times, a warm-up and three counted, and boot 6 four more from one pool.

## Before you start

This walk changes no tracked file. It uses persistence-latency, a row already in the catalog, as the stand-in for your
fault: step 1 writes a practice row into a copy, and steps 2 to 4 run persistence-latency. When you add a fault you
mean to keep, write its row in `faults/catalog.yaml` in step 1, and type its name wherever this page types
persistence-latency.

**You need**, each with the command that checks it:

- A `bin/laug` built from HEAD, Docker, the `temporal` CLI, jq, yq and Omes. Run `make build`, then
  `bin/laug deps -cell`. It should exit 0, with no row marked `MISSING`. A `note` row is not missing: on the
  bin/laug row it says your tree has changes bin/laug was built without, such as a row you added.
- No other laug pool or cell on this host. `bin/laug pool down`, bare, removes nothing, exits 2, and should print this
  line:
  `laug · pool down: no pool serve, gate pool or pool cell, and no per-cell dir or last lease: nothing to stop or remove`.
  Steps 3 and 4 end with `bin/laug pool down -remove-cells`, which removes every laug pool cell on the host. This check
  makes sure it removes only what this page made.
- No plain cell: bare `bin/laug cell down` should print
  `laug · cell down: project laug has no container, volume or network: nothing to remove` and exit 2.
- Ports 7700, 7233, 8080, 3000, 9090, 8474, 9092 and 9093 free:
  `ss -ltn | grep -wE '7700|7233|8080|3000|9090|8474|9092|9093'` prints nothing when all eight are free.
- No gate running on this host. Bare `bin/laug pool down`, above, adds a line `A gate runs (...)` while one does, and
  steps 3 and 4's last line refuses then.

| You see | It means | Do this |
| --- | --- | --- |
| `MISSING  bin/laug ... make build builds it from HEAD` | bin/laug was built from another commit | `make build`, then `bin/laug deps -cell` again |
| `MISSING  omes ... bin/laug upstream clones it` | Omes is not cloned, so a raun's load cannot start | `bin/laug upstream`, once: it clones Omes at the commit every committed result used |
| `MISSING` on docker compose or docker engine | no Docker, or its daemon does not answer | start Docker; steps 3 and 4 build their cells with it |
| `laug · pool down: would stop and remove ...` | a pool or cells from another page are up | stop that pool where you started it, with Ctrl-C or `kill %1` in its shell. If cells stay, read what `bin/laug pool down` names, then `bin/laug pool down -remove-cells` |
| `A gate runs (...)` under bare `pool down`'s list | a gate on this host holds a pool, and steps 3 and 4's last line refuses while it runs | walk this page once the gate has ended |
| `laug · cell down: would remove project laug, ...` | a cell from `bin/laug cell up` holds block 0's ports, the ones your pool's cell needs | once you are done with it, `bin/laug cell down -remove-volumes` removes it with its data |

**It takes**, on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB):

- Steps 1 and 2: about 6 minutes, almost all of it `make test`, which took 322.3 s in a fresh clone there.
- Step 3: one raun, about 7 minutes. Boot 6 ran the four rauns of steps 3 and 4 from one pool in 1590.0 s, 397.5 s
  a raun.
- Step 4: three rauns, about 20 minutes.
- In all, about 32 minutes.

On a host that has never run a cell, step 3's first lease also pulls the cell's 8 images: a cell built from nothing,
pulls included, served in 44 s on the lab host. Walking runbooks 2 and 3 first does that, and builds Omes once.

On the dev host, a Linux VM with 24 vCPU and 47 GiB running Docker, a raun ends early with a BLOCK. A host slower than
the lab host can do the same. There each raun BLOCKs at the end of its 90 s of steady load and gives its cell back.
So it takes its cold build, those 90 s and the give-back, not the lab host's 397.5 s.
[If your host is slower](#if-your-host-is-slower) gives the dev host's `make test` time and says why its rauns end
sooner.

**It leaves**:

- Four raun JSONs. Step 3's is `faults/results/raun-rb6-<UTC time>-1.json`; step 4's three end `-2.json` to `-4.json`,
  after the time step 4 began. Each has the load's two logs: its script's lines beside it, in `.load.log`, and Omes's
  own in `faults/results/load/raun-rb6-<UTC time>-1.log` and so on. `.gitignore` keeps every one of them out of git.
- `cells/run/pool.log`, the pool's log and compose's output, ignored as well.
- `faults/catalog.yaml` and every tracked file as they were.

Run every command on this page in bash, from the checkout's root. Under each command is what it prints. In an expected
line `...` stands for any text, and a line of `...` alone for any number of lines.

## 1. Write the row

A row is the whole fault: what you expect, the reading that shows it, how long recovery may take, and the two commands
that put the fault in and take it out. `faults/run.sh` reads nothing else about it.

Read one first. This prints persistence-latency's row, less its hypothesis, which is one line of over 700 characters:

```sh
yq '.faults["persistence-latency"] | del(.hypothesis)' faults/catalog.yaml
```

```text
signal: persistence_latency_p99_ms
budget_s: 60
inject: toxic add latency latency=200 jitter=50
remove: toxic remove latency
```

Read it out loud like this. Add 200 ms, give or take 50, to every round trip between the server and its Postgres. The
fault is in once persistence p99 goes over 23 ms, its max in the threshold set. Take it
out, and p99 must be back under 23 ms within 60 s. The command exits 0.

The five fields every row holds:

| Field | What it holds | In this row |
| --- | --- | --- |
| `hypothesis` | what you expect, and what that rests on | "200 ms added to every Postgres round trip raises persistence p99 past 50 ms; no client errors; recovery immediate on removal", then what boot 2 measured |
| `signal` | a threshold's name, or a `signal_query` of your own with a `signal_max` | `persistence_latency_p99_ms`, max 23 |
| `budget_s` | the budget, in seconds | 60 |
| `inject` | the command that puts the fault in | `toxic add latency latency=200 jitter=50` |
| `remove` | the command that takes it out | `toxic remove latency` |

The first word of `inject` and of `remove` is the mechanism. There are eight:

| Mechanism | What it does | A row that uses it |
| --- | --- | --- |
| `toxic` | adds or removes a toxic in Toxiproxy, the proxy between the server and its Postgres | persistence-latency |
| `docker` | runs a docker command on one of the cell's containers | kill-history: `docker kill temporal-history` |
| `dynconfig` | sets one dynamic config key; `dynconfig restore` puts the file back | throttle-frontend: `dynconfig frontend.rps 10` |
| `tdbg` | closes shards with Temporal's debug tool | close-shards: `tdbg close 64 1014` |
| `pg` | bumps one shard's row in the cell's Postgres; `pg restore` puts it back | stuck-shard: `pg bump seed:1014 1000` |
| `serverfi` | turns on the server's own persistence fault injection; `serverfi off` turns it off | wf-update-timeout |
| `vm` | `vm drive ops=<n> [bw=<bytes>]` puts a token bucket on a microVM cell's drive through laugd; `vm drive off` lifts it; microVM cells only, so the row adds `backend: firecracker` | disk-slow: `vm drive ops=100` |
| `none` | nothing: a remove that leaves the fault in | close-shards' remove |

Most rows need only these five fields. A shard fault takes `signal_base`, `recover`, `recover_from` and `cleanup`, and
a self-test of the harness takes `self_test`. The header of `faults/catalog.yaml` says what each one does.

A row goes at the end of `faults:`, after the last row, its name two spaces in, like the rows around it. Runbooks 3 and
6 print the catalog's first names, so a row put first fails their checks in `make test`. laug finds a name by reading
the file line by line, and it sees no name at any other indent. A name is one word of letters, digits, `.`, `_` and `-`,
starting with a letter or a digit.

**Write a practice row.** You write it into a copy, so the catalog stays as it was. The copy holds persistence-latency
again as persistence-latency-slow, at 400 ms. `CATALOG` names the copy: `bin/laug raun`, `bin/laug fault` and
`faults/run.sh` read that file in place of `faults/catalog.yaml`. `make test` never reads it. `-dry-run` checks the
name and the flags, prints the line it would hand to `faults/run.sh`, and leases nothing. You should see the practice
row, then that line:

```sh
d=$(mktemp -d)                                    # a scratch directory; the last line removes it
yq '.faults["persistence-latency-slow"] = .faults["persistence-latency"] |
  .faults["persistence-latency-slow"].inject = "toxic add latency latency=400 jitter=50"' \
  faults/catalog.yaml > "$d/catalog.yaml"
yq '.faults["persistence-latency-slow"] | del(.hypothesis)' "$d/catalog.yaml"
CATALOG="$d/catalog.yaml" bin/laug raun persistence-latency-slow -run-id rb6 -dry-run
rm -r "$d"
```

```text
signal: persistence_latency_p99_ms
budget_s: 60
inject: toxic add latency latency=400 jitter=50
remove: toxic remove latency
CATALOG=... RUN_ID=raun-rb6 POOL_ADDR='http://127.0.0.1:7700' faults/run.sh workflow_with_single_noop_activity persistence-latency-slow
```

The practice row differs from persistence-latency's in one value, the latency. The last line reads: the copy, the run
id `raun-rb6` (raun puts `raun-` before what `-run-id` gives), the pool on this host's port 7700, the default load
scenario, and the fault. You don't type that line: `bin/laug raun` runs it. The block exits 0.

The dry run does not check the row's commands. `make test` checks the first word of each one (step 2). A raun checks
the arguments of `tdbg`, `pg`, `serverfi` and `vm` before its load, and tries those of `toxic`, `docker` and
`dynconfig` only when it injects, after 90 s of load.

Without `CATALOG`, laug reads `faults/catalog.yaml`, which has no such row. It says so, lists the faults it does hold,
and exits 2:

```sh exit=2
bin/laug raun persistence-latency-slow -run-id rb6 -dry-run
```

```text
laug · raun: no fault persistence-latency-slow in faults/catalog.yaml, which holds ...:
  persistence-latency...
...
```

If you see something else:

| You see | It means | Do this |
| --- | --- | --- |
| `laug · raun: no fault <name> in faults/catalog.yaml; did you mean <name>?` | the name you typed is one or two letters off a row's | type the name it suggests, or fix the row's name |
| `laug · raun: no fault <name> in faults/catalog.yaml, which holds ...`, and your row is not in the list | laug did not find your row | check the spelling, and that the name sits two spaces in under `faults:`; save the file |
| `laug · raun: no fault <name> in` a file you did not edit | `CATALOG` is set in your shell and names that file | `echo "$CATALOG"` shows it; `unset CATALOG` reads `faults/catalog.yaml` again |
| `laug · raun: fault '<name>': not one word of letters, digits, ., _ and -, from a letter or digit` | the name holds a space or another character laug does not take | rename the row |
| `Error: ...` from yq | the copy's yq line did not parse, most often a quote lost in pasting | type the block again as it stands |

## 2. Let the harness read it

`make test` runs the Go tests, the harness and the lint, with no Docker and no cell. The harness holds every row's
mechanisms to the eight that `faults/run.sh` knows, and runs each serverfi row's inject through `faults/run.sh`'s own
argument check. Two serverfi rows also run end to end on a fake cell. It reads `faults/catalog.yaml` only, never a copy,
so your row must be there before this step. In this walk the catalog holds what it held, and the harness reads
persistence-latency with every other row.

Type:

```
make test
```

You should see these lines among many others, the last one at the end:

```text
...
  ok    every catalog mechanism is a verb faults/run.sh knows
...
  ok    catalog row wf-update-timeout: run.sh's checks pass, removed by serverfi off
...
... passed, 0 failed
...
laug · make test: passed, test-go, harness-test and lint, in ... s
```

The first `ok` line is the one that reads your row's mechanisms. Each serverfi row gets a `catalog row` line, so a
serverfi row of yours gets one too. The tally is the harness's. On the lab host, in boot 6's `make harness-test`, the
first `ok` line and the tally `1187 passed, 0 failed` read as above. Your count can be higher than 1187: tests are
added as the tree grows. What has to hold is `0 failed`. If a `level=WARN` line from yq about merge anchors shows, it
is yq's own note. The last line says all three parts passed, and `make test` exits 0.

**For a serverfi row**, also run `bin/laug cell fi-template-check -server-tag 1.31.0`, and once more for each other tag
your rauns will use. It needs Docker and Go, and no cell. Docker may pull the server's image. It checks your row's
store and method against the methods the server's fault injection wraps at that tag. It reads `CATALOG`'s copy when
your shell sets it. A misspelled method injects nothing, and the raun would BLOCK `did not land`. Its check 6 lists the
serverfi rows' store and method, yours among them, and it exits 0 with this last line:<!-- source:
cells/fi-template-check.sh:156-160; seen on the dev host, and no committed run records a pass -->
`laug · fi-template-check: pass on server tag 1.31.0 (temporalio/server:1.31.0)`.

If you see something else:

| You see | It means | Do this |
| --- | --- | --- |
| `  FAIL  every catalog mechanism is a verb faults/run.sh knows`, then `got:` with a count, and `want: 0` | a row's `inject`, `remove` or `cleanup` starts with a word that is not one of the eight | `grep -n -e '^    inject:' -e '^    remove:' -e '^    cleanup:' faults/catalog.yaml` lists every command with its line; fix the first word |
| `  FAIL  catalog row <name>: run.sh's checks pass, removed by serverfi off` | a serverfi row's `inject` fails `faults/run.sh`'s check of its arguments, or its `remove` is not `serverfi off` | read the lines under it, and compare your row with wf-update-timeout's |
| `  FAIL  bin/laug runbook check docs/runbooks/6-add-a-fault.md: exit 1, ...`, or the same for `3-raun.md` | your row is not last under `faults:`, and these pages expect the catalog's first names; or you named it persistence-latency-slow, which step 1's exit 2 example needs missing | move your row to the end of `faults:`, or give it another name |
| any other `FAIL`, in a part your row does not touch | something else in your checkout | `git diff --stat` shows what else changed; [runbook 1](1-offline.md) runs `make test` on a clean tree |
| `  FAIL  6. serverfi row(s) in ... name a method the v1.31.0 wrapper does not draw for ...`, then `laug · fi-template-check: FAIL on server tag 1.31.0 ...`, exit 1 | the server does not wrap that store's method at that tag, so the fault would inject nothing | the line under FAIL names your row and its `store.method`; check the spelling against wf-update-timeout's row |

## 3. Run it once, and read what it says

One raun shows whether the row does what its hypothesis says, before you spend 20 more minutes on three. If you walked
runbook 3 just now, its raun was this step's raun; you can go on to step 4.

The block starts a pool that keeps no cell warm and holds one at most (`-min 0 -max 1`). Your raun's lease is cold: the
pool builds the cell when you ask, so no other cell is built while your load runs. The pool writes its log, and
compose's output for each cell, to `cells/run/pool.log`, about 130 lines a cell. To watch your cell being built, run
`tail -n 0 -f cells/run/pool.log` in a second terminal; `-n 0` skips what earlier walks wrote there.
Compose writes a line as each container is `Created` and `Started`, and as each is `Stopped` and `Removed` when
the cell goes. Ctrl-C there ends the tail, not the pool.

`id` names this walk's run, `rb6-` and the UTC time, so `.gitignore` keeps its files out of git. `|| echo` prints the
raun's exit when it is not 0: 1 for a BLOCK, 2 when `faults/run.sh` failed. By then the raun has given its cell back.
The last line stops the pool and removes what it left.

You should see these lines among others:

```sh needs=cell
id=rb6-$(date -u +%Y%m%d-%H%M%S)
bin/laug pool serve -backend compose -min 0 -max 1 -log cells/run/pool.log &
sleep 2                                           # the pool takes a moment to listen
bin/laug raun persistence-latency -run-id "$id-1" || echo "the raun exited $?"
bin/laug pool down -remove-cells
```

```text
laug · pool serve: listening on 127.0.0.1:7700 (min 0, max 1, ...); ...; its log and the cells' output go to cells/run/pool.log; Ctrl-C or SIGTERM stops it
...
laug · raun: persistence-latency on lease ... until ... UTC, run raun-rb6-...-1; its windows take 330 s, plus up to 60 s to recover; Ctrl-C stops the fault, then the cell goes back
...
laug · fault persistence-latency on raun raun-rb6-...-1
...
server      1.31.0
...
laug · raun: ... given back, its cell destroyed; .last-lease.json removed; the raun's JSON is faults/results/raun-rb6-...-1.json
...
stopped laug pool serve, pid ...
...
left: 0 containers, 0 volumes, 0 networks, 0 per-cell dirs
```

The table says what each line means. Its numbers are the first of boot 6's four persistence-latency rauns on the lab
host, raun-20261027-051406.

| Line | What it says |
| --- | --- |
| `listening on 127.0.0.1:7700 (min 0, max 1, ...)` | the pool is up, keeps no cell warm, and builds one at most |
| `laug · raun: waiting for a cell ...` | the pool is building your cell: the cold lease |
| `on lease ... until ... UTC` | the cell you got, and when its lease ends: 30 minutes on, raun's `-ttl` |
| `its windows take 330 s, plus up to 60 s to recover` | 90 s of steady load, 120 s with the fault held, 30 s to settle and 90 s after, then up to the budget |
| `[raun-rb6-...-1] ...` | `faults/run.sh`'s log as the raun goes: the before window, injected, landed, removed, recovered, the drain, the after window, the sweep |
| `landed      ... s after inject; signal peak ... (max 23)` | the fault was in. Boot 6: 10 s, a peak of 9,872.6 ms against 23 |
| `recovered   ... s after removal (budget 60 s); ...` | the signal was back under its max, inside the budget. Boot 6: 25 s |
| `failed      before 0, during ..., drain ..., after 0, ...` | client starts that failed, by window. During the fault they are expected; one in the after window is a BLOCK. Boot 6: 539 during, 4 in the drain, 0 after |
| `sweep       Completed ...` | every workflow the load started more than 30 s before the after window's end, by status; anything but Completed is a BLOCK. Boot 6: Completed 4104 |
| `softassert  ...` | the server's own invariant checks that failed: recorded, not judged. Boot 6: 0 in every window |
| `ADVANCE`, or a `reason` line and `BLOCK` | the verdict. Boot 6: ADVANCE |
| `given back, its cell destroyed; ...; the raun's JSON is ...` | the raun gave the cell back, whatever the verdict, and names the file it wrote |
| `laug · pool down: pool serve, pid ..., 0 gate pools; 0 pool cells: ... go` | what pool down stops and removes, counted before it does |
| `laug · pool serve: stopped with 0 cells in its table, none destroyed (...)` | the pool's own last line. It held no cell, since the raun gave its cell back; the parenthesis is its standing advice |
| `stopped laug pool serve, pid ...` and `left: 0 containers, ...` | `pool down` stopped this page's pool and found no laug container, volume, network or cell directory left |

Typed by hand, your shell also prints the job's number and pid after the first line, and says the job is done after
the last.

After the lease line, `faults/run.sh` prints its plan, then nothing for about 90 s, the load before the fault. On the
lab host it then prints the before window's readings and `injected`, and `landed` 10 s later. Then nothing for about
110 s while the fault is held. Then `removed`, `recovered` 25 s later and `drain` 30 s after that. Then nothing for
about 90 s more, the after window. On a slower host the summary and `BLOCK` come where `injected` would, about 90 s
after the lease line.

On the lab host, boot 6's four rauns each read ADVANCE. On the dev host, expect a `reason` line and `BLOCK` where the
landed, recovered and sweep lines would be, then `the raun exited 1`. The reason names each reading over its max, such
as `never steady: workflow_task_schedule_to_start_p99_ms=... workflow_end_to_end_p95_ms=...`, and
[If your host is slower](#if-your-host-is-slower) says why. The block exits 0 on either verdict. The raun alone exits 0
on ADVANCE, 1 on BLOCK and 2 when `faults/run.sh` itself failed, such as no Omes or a failed inject. The JSON's keys are
in [faults/README.md](../../faults/README.md#reading-a-raun-json). `jq -c '{run_id, result, reason}'` on the path the
given-back line printed shows the verdict and the reason on one line.

If you see something else:

| You see | It means | Do this |
| --- | --- | --- |
| `laug · pool serve: cells/run/pool.pid names pid ..., which runs: one pool serve a checkout; ...` | another pool serve runs in this checkout, and your raun leases from it | Ctrl-C the raun. Stop the other pool where you started it, or read what `bin/laug pool down` names and run `bin/laug pool down -remove-cells`; then type the step again |
| `laug · pool serve: listen tcp 127.0.0.1:7700: bind: address already in use` | another program holds port 7700 | `ss -ltnp 'sport = :7700'` names it; stop it, then type the step again |
| `laug · raun: no pool answers on 127.0.0.1:7700 (connection refused); ...` | the pool did not start | the pool's own line, above it, says why |
| `laug · raun: the pool on 127.0.0.1:7700 refused: ...`, and the raun exits 1 | the pool could not build your cell: compose failed, or the cell did not pass its check in its 5 minutes | the end of `cells/run/pool.log` holds compose's error. Two causes to check: a cell port another program holds (`ss -ltn`), and a plain cell (bare `bin/laug cell down`) |
| `laug · pool down: a gate runs (...)` | a gate on this host holds its own pool, and pool down will not stop it mid-wave | `kill %1` stops your pool; walk this page when the gate has ended |
| `laug · raun: waiting for a cell ...`, then nothing for minutes | the pool is building your cell; on a host that has never built one, it pulls the images first | `tail -f cells/run/pool.log` shows how far it got. On the lab host a cell built from nothing, its images pulled first, served in 44 s. The pool gives up on a create after 5 minutes |
| `load generator exited: no Omes at .upstream/omes; run bin/laug upstream first`, and the raun exits 2 | Omes is not cloned | `bin/laug upstream`, then type the step again |
| `reason      never steady: ...`, then `BLOCK` | a threshold was over its max in the before window; nothing was injected, and this raun says nothing about the fault | on the lab host, type the step again. On a slower host, expect it every time: [If your host is slower](#if-your-host-is-slower) |
| `reason      did not land: signal ... stayed at ... over 120s`, then `BLOCK` | the fault went in, and its signal never passed its max | persistence-latency landed in 5 to 10 s in each of boot 6's four. For a row of yours, the fault may be too weak to see: persistence-errors at toxicity 0.05 never landed in 3 rauns. A layer between the fault and the server may absorb it ([stop 11](../LEARNING.md#stop-11)). A serverfi method the server does not wrap injects nothing (step 2) |
| `reason      recovered in ...s, budget 60s`, then `BLOCK` | the signal came back, later than the budget allows | compare the number with boot 6's 20 to 25 s. A smaller host can recover slower: [If your host is slower](#if-your-host-is-slower) |
| `reason      after window not clean: ...`, then `BLOCK` | the signal came back, and a threshold stayed over its max after | type the step again. One of three microVM throttle-frontend rauns on the lab host did this, end-to-end p95 85.1 ms against 84 |
| `reason      inject failed: ...`, and the raun exits 2 | a mechanism failed as it ran; the cell's state is unknown, and it was given back | fix the row's arguments: `toxic`, `docker` and `dynconfig` arguments are first tried here |
| `the raun exited 1` | the block's echo after a BLOCK, or after a `laug · raun:` refusal | read the `reason` line, or the refusal, above it |
| `left:` with a count over 0 | it counts every container, volume and network on the host whose name starts `laug-`: something of laug's outside this page's pool is up, or a cell did not go | `docker ps -a --filter name=laug-` lists the containers; `bin/laug pool down` names a pool's, and `bin/laug cell down` a cell from `bin/laug cell up` |

## 4. Run it three more times, and compare

One raun can be luck. `faults/README.md` counts a fault as measured only when three rauns on cold leases give the same
shape of result: each landed, recovered within the budget and left a clean after window, or each gave the same BLOCK.
When they differ, the difference is the finding.

Three that agree look like this. They are the last three of the four persistence-latency rauns boot 6 ran on the lab
host, all four from one pool that kept no cell warm. Their rows in `docs/tables/rauns.jsonl` hold what the JSONs did,
the seconds to land as `.landed_at_s`:

```sh
jq -c 'select(.id | IN("raun-20261027-052045", "raun-20261027-052716", "raun-20261027-053355"))
    | {run_id, result, landed_s: .landed_at_s, recovered_s: .time_to_recover_s, during: .iterations_failed.during,
    after: .iterations_failed.after, reason}' docs/tables/rauns.jsonl
```

```text
{"run_id":"raun-20261027-052045","result":"ADVANCE","landed_s":5,"recovered_s":"20","during":546,"after":0,"reason":""}
{"run_id":"raun-20261027-052716","result":"ADVANCE","landed_s":5,"recovered_s":"25","during":536,"after":0,"reason":""}
{"run_id":"raun-20261027-053355","result":"ADVANCE","landed_s":10,"recovered_s":"25","during":537,"after":0,"reason":""}
```

Each line is one raun: its verdict, the seconds to land and to recover, the client starts that failed during the fault
and after it, and the reason, empty for an ADVANCE. All three ADVANCEd. They landed in 5 to 10 s, recovered in 20 to
25 s, and failed 536 to 546 starts during the fault and none after. That is one shape. The command exits 0.

Now yours. The block runs three rauns on cold leases, one after another, then prints the same line for each. It takes
about 20 minutes on the lab host. You should see:

```sh needs=cell
id=rb6-$(date -u +%Y%m%d-%H%M%S)
bin/laug pool serve -backend compose -min 0 -max 1 -log cells/run/pool.log &
sleep 2                                           # the pool takes a moment to listen
for n in 2 3 4; do bin/laug raun persistence-latency -run-id "$id-$n" || echo "the raun exited $?"; done
bin/laug pool down -remove-cells
jq -c '{run_id, result,
    landed_s: (if (.landed_at // "") != "" then (.landed_at | tonumber) - (.inject_at | tonumber) else null end),
    recovered_s: .time_to_recover_s, during: .iterations_failed.during, after: .iterations_failed.after, reason}' \
  faults/results/raun-"$id"-{2,3,4}.json
```

```text
laug · pool serve: listening on 127.0.0.1:7700 (min 0, max 1, ...); ...; its log and the cells' output go to cells/run/pool.log; Ctrl-C or SIGTERM stops it
...
laug · raun: ... given back, its cell destroyed; .last-lease.json removed; the raun's JSON is faults/results/raun-rb6-...-2.json
...
laug · raun: ... given back, its cell destroyed; .last-lease.json removed; the raun's JSON is faults/results/raun-rb6-...-3.json
...
laug · raun: ... given back, its cell destroyed; .last-lease.json removed; the raun's JSON is faults/results/raun-rb6-...-4.json
...
left: 0 containers, 0 volumes, 0 networks, 0 per-cell dirs
...
{"run_id":"raun-rb6-...-2","result":"...}
{"run_id":"raun-rb6-...-3","result":"...}
{"run_id":"raun-rb6-...-4","result":"...}
```

Read the last three lines as one. On the lab host, expect the shape above: three ADVANCE, landed in 5 to 10 s,
recovered in 20 to 25 s, none failed after. On the dev host, expect three lines like
`{"run_id":"raun-rb6-...-2","result":"BLOCK","landed_s":null,"recovered_s":"",...,"reason":"never steady: ..."}`. They
agree with each other, and they measure the host, not the fault. The block exits 0 on any verdict.

If you see something else:

| You see | It means | Do this |
| --- | --- | --- |
| three lines that differ: a BLOCK among ADVANCEs, or two reasons | the shape differs, and that is the finding | read each reason. Run again only for a `never steady` BLOCK, which says nothing about the fault. A BLOCK on the budget by a few seconds is how throttle-frontend's budget moved from 120 s to 180 s: [If your host is slower](#if-your-host-is-slower) |
| three `never steady` BLOCKs | the host was over a threshold before each fault | [If your host is slower](#if-your-host-is-slower) |
| `jq: error: Could not open file faults/results/raun-rb6-...: No such file or directory` | a raun wrote no JSON: it exited 2, or you pressed Ctrl-C before its fault went on | read that raun's lines above, against step 3's table |
| a line of step 3's table | the same as there | what step 3's table says |

## 5. Keep what it did, on the lab host only

On the dev host, stop here, after step 4. Every raun is judged by the lab host's limits, so a raun on the dev host
measures the host as much as the fault. The tree keeps the dev host's rauns out, and this walk's files are `rb6-*`,
which `.gitignore` keeps out as well.

On the lab host, a fault you keep reaches the tree this way:

1. Its row goes in `faults/catalog.yaml`. The catalog is an input of every gate, laug's judge of a release
   ([runbook 4](4-gate.md)): every gate's record holds its sha256 ([faults/README.md](../../faults/README.md)).
2. Run steps 3 and 4 as a lab batch, by [runbook 7](7-lab-batch.md). Its transcript goes under `cells/results/`, with
   the host's name and paths taken out. Give each raun a `-run-id` that git does not ignore: step 3's
   `lab<N>-<fault>-0`, and step 4's three `lab<N>-<fault>-1` to `-3`, as boot 2 gave `lab2-warmup` and then
   `lab2-persistence-latency-1` to `-3`. A walk's `rb6-` ids are ignored.
3. After `make test` and runbook 7's scan (its step 3), commit the row; the three raun JSONs, each with its row in the
   table of `faults/results/README.md` that names the pages citing each file (`make test-go` prints the table it wants);
   the transcript, with its row in `cells/results/README.md`; and the row's status in the table of `faults/README.md`:
   the runs, the host, the result, the files, and what the during windows caught.

A status row in the table's shape, with every number from step 4's jq:

```text
| <fault> | <inject> | <signal> | <budget_s> | 3 of 3 ADVANCE, landed <a> to <b> s, recovered <c> to <d> s, failed <e> to <f> during and 0 after (`raun-lab<N>-<fault>-{1,2,3}.json`) | <the rules the during windows tripped> |
```

## If your host is slower

laug's limits were measured on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), and a raun is judged by them on any
host. A slower host shows it four ways.

**A raun BLOCKs before any fault.** In boot 6's four rauns, the lab host's before windows read end-to-end p95 at 49.6 to
49.9 ms, under a max of 84. Workflow task schedule-to-start p99 read 18.9 to 19.2 ms, under a max of 38. The dev host, a
Linux VM with 24 vCPU and 47 GiB running Docker, read the same two at 173 to 176 ms and 48.8 to 49.1 ms on single loads.
Both are over their max. There a raun BLOCKs `never steady` at the end of its 90 s of steady load. Its reason names each
reading over its max, as name=value; on a host slower still, more latencies join them. It injects nothing and gives its
cell back, so it ends sooner than the lab host's 397.5 s a raun. Three such BLOCKs agree with each other, and they
measure the host, not the fault. The lab host did it too, on microVM cells: oom-history's runs 1 and 3 read end-to-end
p95 at 90.5 and 90.4 ms against 84 before any fault.

**A budget fits one host and not a smaller one.** throttle-frontend's first three rauns ran on microVM cells on the
first lab host, an m8i.2xlarge (8 vCPU, 32 GiB), with a budget of 120 s:

```sh
jq -c 'select(.id | test("^raun-fcthrottle[123]$")) | {run_id, result, recovered_s: .time_to_recover_s, reason}' \
  docs/tables/rauns.jsonl
```

```text
{"run_id":"raun-fcthrottle1","result":"ADVANCE","recovered_s":"58","reason":""}
{"run_id":"raun-fcthrottle2","result":"BLOCK","recovered_s":"123","reason":"recovered in 123s, budget 120s"}
{"run_id":"raun-fcthrottle3","result":"BLOCK","recovered_s":"123","reason":"recovered in 123s, budget 120s"}
```

One came back in time, and two missed by 3 s. The budget went to 180 s. On the lab host the same fault, on microVM
cells, recovered in 60, 61 and 116 s. A BLOCK on the budget by a few seconds, on a host smaller than the lab host, is
a finding about the host as much as about the fault.

**make test takes longer.** On the lab host `make test` took 322.3 s in a fresh clone; on the dev host, 345.2 s.

**The clock can drift.** The lab host's wall clock kept to its uptime clock, 0.9999 s a second; the dev host's moved
1.045 to 1.048 s. Step 4's pool runs for three rauns, so on such a host its log, `cells/run/pool.log`, can say
`reaper: clock step detected` every 10 s, the pool's `-reap-interval`. That is the host's clock, not your cell.
[Runbook 2](2-lease-and-load.md#if-your-host-is-slower) says more.

So on the dev host, walk steps 1 to 4 to learn them, expect BLOCK, and keep nothing (step 5). To judge by limits made on
your own host, `load/baseline.md` says how to make a set, under "A set for a new host type", and
`bin/laug raun <fault> -thresholds <file>` judges a raun by it.

## If you stop halfway, and tidying up

**Between steps** nothing runs: each step's last line stopped its pool, and steps 3 and 4 each start their own.

| Where you stopped | What you see, and what is left | Do this |
| --- | --- | --- |
| Ctrl-C while a raun waits for its cell | `laug · raun: stopped before the pool on 127.0.0.1:7700 answered; the pool gives back a cell it could not hand over`, and the raun exits 1. The pool still runs, and destroys the cell once it is built | `bin/laug pool down -remove-cells` |
| Ctrl-C after the lease line | `laug · raun: stopping: ... goes back once faults/run.sh ends; ...`. The raun takes the fault off, gives the cell back and exits 130. Before the fault went on, it writes no JSON; after, one with `"partial": true`. The pool still runs | wait for the given-back line, then `bin/laug pool down -remove-cells` |
| Ctrl-C in step 4 | the raun that runs stops as above, `the raun exited 130`, and the loop starts the next raun | Ctrl-C once for each raun the loop starts, then `bin/laug pool down -remove-cells` |
| a second Ctrl-C in one raun | laug ends at once. The cell stays leased, perhaps with the fault on, until the time the stopping line names | `bin/laug pool down -remove-cells` removes it now, with its data |
| you closed the terminal | the pool or a cell may still be up | `bin/laug pool down` names what is left, with counts, and removes nothing; `bin/laug pool down -remove-cells` removes it |

**What the walk leaves is ignored.** `git status --short` lists none of it, and `git diff --stat` prints nothing.
`git status --short --ignored faults/results cells/run/` lists them after `!!`: each raun file by name, and `cells/run/`
as one line, which holds the pool's log. Omes's own logs, `faults/results/load/raun-rb6-*`, carry your local time zone
and this machine's name; keep them out of anything you commit. `pgrep -af raun-rb6` prints nothing once the rauns are
done; a line there is an Omes process that outlived its raun, and `kill` takes its pid. To remove the files:

```
rm -f faults/results/raun-rb6-* faults/results/load/raun-rb6-* cells/run/pool.log
```

**If you edited `faults/catalog.yaml`** to try a row you will not keep, `git diff faults/catalog.yaml` shows it, and
`git restore faults/catalog.yaml` puts the file back as it was.

## How this page is checked

`make test` runs this page's five offline blocks through `bin/laug runbook check`, against committed files, and
`bin/laug runbook check -cell docs/runbooks/6-add-a-fault.md` runs its two cell blocks too, which build cells and hold
on either verdict. The blocks above held 7 of 7 on the lab host in boot 8, each raun ADVANCE.

## Next

- [Runbook 7](7-lab-batch.md): run your three rauns as a lab batch, and keep them.
- [faults/README.md](../../faults/README.md#the-catalog): what every row measured, to set yours beside.
- [Stop 11](../LEARNING.md#stop-11): why a fault can go in and never be seen.
- [Runbook 4](4-gate.md): a gate runs one row, persistence-latency, as its fault item.

## Evidence

`cells/results/2026-10-23-boot1.txt`, `cells/results/2026-10-23-boot2.txt`, `cells/results/2026-10-24-boot4.txt`,
`cells/results/2026-10-27-boot6.txt`, `faults/results/raun-lab2-persistence-latency-1.json`,
`faults/results/raun-lab2-persistence-errors-{1,2,3}.json`, `faults/catalog.yaml`, `faults/results/README.md`,
`load/steady-state.yaml`, `gate/plan.yaml`, `cmd/laug/pool.go`, the rows of
raun-20261027-05{1406,2045,2716,3355}, raun-lab5-oom-history-{1,3} and raun-lab5-throttle-frontend-{1,2,3} in
`docs/tables/rauns.jsonl`, `docs/tables/records.jsonl#cells/results/2026-10-23-boot2/`,
`docs/tables/quotes.jsonl#cells/results/2026-10-27-boot6/steps/S5b.txt:473`, :1368, :1370,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-boot2/steps/R-persistence-latency-1.txt:16-19`,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-boot3.txt:177-178`, :222,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-fresh-clone.txt:21`,
`docs/tables/quotes.jsonl#cells/results/2026-10-29-boot8.txt:125-133`, :135-170.
