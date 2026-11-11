# Runbook 3: run one raun

You run one raun, one fault on a cell of your own, and get its verdict, ADVANCE or BLOCK, with a JSON file of
everything it read.

Once [runbook 2](2-lease-and-load.md) has run on this host, it is three commands. Type them one at a time, so the
pool listens before the raun asks. Step 5 runs them for real, with a check before the raun and your JSON after it.

```console
$ bin/laug pool serve -backend compose -min 0 -max 1 -log cells/run/pool.log &
$ bin/laug raun persistence-latency -run-id rb3-$(date -u +%Y%m%d-%H%M%S)
$ bin/laug pool down -remove-cells
```

Run everything on this page in bash, from the checkout's root. Under each command is what it prints. Steps 1 to 3 print
the same on any host. Steps 4 and 5 show only the lines that do not depend on the host's speed. Where the lab host and a
slower host differ, the prose says what each shows, and the numbers are the lab host's. In the lines under a command,
`...` stands for text that changes from run to run, or for lines left out.

## The words on this page

- **Cell**: one complete, throwaway Temporal server on this host, with its own Postgres.
- **Pool**: the cells laug keeps warm and ready; `bin/laug pool serve` runs it. This page's pool keeps none waiting
  and holds one at most.
- **Lease**: take a cell from the pool for one job, then give it back. A raun's lease lasts 30 minutes unless `-ttl`
  says otherwise, and the raun gives the cell back as soon as it ends.
- **Cold cell**: a cell the pool builds when you ask for one, because none was waiting.
- **Fault**: one row of `faults/catalog.yaml`. This page's, persistence-latency, adds 200 ms, give or take 50, to
  every round trip between the server and its Postgres, and holds it for 120 s.
- **Windows**: the spans of load the raun reads. Before: the last 60 s of 90 s of load, with no fault. During: the
  fault held. Drain: from the fault's removal to the after window, recorded and not judged. After: 90 s of load, once
  the cell has recovered and then settled for 30 s.
- **Raun**: one fault on one leased cell. laug loads the cell, injects the fault, reads the windows before, during
  and after it, and judges the result.
- **Reading** and **max**: a reading is one number over a window, such as persistence p99, the 99th percentile of the
  time the server's calls to Postgres took. The threshold set gives each reading its max; `bin/laug thresholds`
  prints the set.
- **Signal**: the one reading that shows the fault. Here it is persistence p99, against its max of 23 ms.
- **Budget**: how long the signal may take to come back under its max once the fault is off. Here, 60 s.
- **Failed start**: a workflow the load tried to start and could not. Omes logs it; the summary and the JSON call it
  a failed iteration.
- **Omes**: Temporal's load generator. The raun runs it at up to 20 workflows a second, at most 50 running at once.
- **Verdict**: ADVANCE when the cell took the fault and came back; BLOCK when it did not, or when a window failed. A
  BLOCK's reason line says which.
- **Lab host**: the lab host, an m8i.8xlarge (32 vCPU, 128 GiB). laug's maxima were measured on it.
  `bin/laug thresholds` names it with 123 GiB: 128 GiB nominal, in whole GiB of `free -b`.
- **Gate**: judges one release by several items at once, each on a cell of its own. This page runs none, and step 5's
  last line refuses while one runs.
- **Boot**: one batch of runs on the lab host, with its record in `cells/results`. Boot 2 ran this page's fault four
  times: a warm-up it did not count, then three.

## Before you start

**You need**, each with the line that checks it:

- bin/laug built from the commit you have checked out: `make build`. [Runbook 1](1-offline.md) starts there. The
  bin/laug row of `bin/laug deps -cell`, in step 4, checks it.
- Docker with compose v2, the temporal CLI, Go, jq, yq, bc and the rest of what a raun calls: `bin/laug deps -cell`,
  which step 4 runs, checks all 14 and says `0 of 14 missing` when every one is there.
- Omes at the commit every committed result used: `bin/laug upstream`, which step 4 runs. The first time, it clones
  Omes over the network.
- No pool and no cell left from runbook 2 or an earlier walk: bare `bin/laug pool down`, which step 4 runs, lists
  what is left and removes nothing.
- Runbook 2 walked on this host first. Its lease pulls the 8 images a cell runs, and its load builds Omes once, so
  this page's raun starts on a host that has both. `bin/laug deps -cell` does not check the images; `docker image ls`
  lists them once runbook 2 has run.
- These ports free: 7700 for the pool; 7233, 8080, 3000, 9090 and 8474 for your cell; 9092 and 9093 for Omes.
  `ss -ltn` lists the ports in use.
- No gate running on this host. Step 5's last line would stop a gate's pool and remove its cells, so it refuses
  while one runs. Step 4's listing names a gate's pool.

**It takes** about 7 minutes on the lab host. There, the first of boot 2's three counted rauns of this fault took
395.3 s on a cold cell. Of that, 330 s is the windows, and they take as long on any host. Step 4 takes seconds, more the
first time Omes is cloned. On the dev host, a Linux VM with 24 vCPU and 47 GiB running Docker, expect the raun to end
sooner, with a BLOCK: see [If your host is slower](#if-your-host-is-slower).

**It leaves** no pool, no cell and no lease. Four files stay, and git ignores each one by its name, so `git status`
stays clean: your raun's JSON, `faults/results/raun-rb3-<UTC time>.json`; the load's two logs, its script's lines in
`faults/results/raun-rb3-<UTC time>.load.log` and Omes's own in `faults/results/load/raun-rb3-<UTC time>.log`; and the
pool's log, `cells/run/pool.log`. [If you stop halfway, and tidying up](#if-you-stop-halfway-and-tidying-up) says
how to remove them.

## 1. See the faults you can run

With no fault named, raun leases nothing. It lists the faults in `faults/catalog.yaml` and refuses.

You should see the catalog's faults in two columns, then raun's usage:

```sh exit=2
bin/laug raun
```

```text
laug · raun: names no fault: faults/catalog.yaml holds ...:
  persistence-latency    ...
  persistence-timeout    ...
...
"...raun -h" prints its flags.
```

What it means:

- The catalog holds 19 faults today. This page runs the first, persistence-latency.
  [faults/README.md](../../faults/README.md) says what each one measured on the lab host.
- The usage line lists raun's flags with their defaults: `-ttl 30m` for the lease, `-rate 20` and
  `-max-concurrent 50` for the load. `bin/laug raun -h` says what each one does.

It exits 2, laug's code for a command it refused: you named no fault.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| `bin/laug: No such file or directory` | bin/laug is not built, or you are not at the checkout's root | `make build` from the root. |
| `laug · raun: names no fault: faults/catalog.yaml lists the faults, and it cannot be read: ...` | you are outside a laug checkout | Go to the checkout's root and run it again. |

## 2. See what the raun will run

raun hands its settings to `faults/run.sh`, the script that loads the cell, injects the fault and reads the
windows. With `-dry-run`, raun checks the fault's name and every flag, leases nothing, and prints the line it would
run. Give it a run id the way step 5 will.

You should see one line:

```sh
bin/laug raun persistence-latency -run-id rb3-$(date -u +%Y%m%d-%H%M%S) -dry-run
```

```text
RUN_ID=raun-rb3-... POOL_ADDR='http://127.0.0.1:7700' faults/run.sh workflow_with_single_noop_activity persistence-latency
```

What it means:

- `RUN_ID` is your run id with `raun-` in front. Your raun's files start with it, so step 5's are named
  `raun-rb3-<UTC time>`. git ignores a raun whose name starts `raun-rb`, a digit and a dash, so a walk leaves
  `git status` clean. Without `-run-id` the id is the UTC time alone, and the JSON shows up in `git status` as a new
  file.
- `POOL_ADDR` is the pool step 5 starts, on this host's port 7700.
- `workflow_with_single_noop_activity` is the scenario, the default and the one every committed raun ran: each
  workflow runs one activity that does nothing.
- `persistence-latency` is the fault, the row the script reads from the catalog.

It exits 0.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| `laug · raun: no fault persistence-latncy in faults/catalog.yaml; did you mean persistence-latency?` | a typo; the name is checked before anything runs | Copy the name from step 1's list. |
| a line that starts `CATALOG=` | your shell sets `CATALOG`, which points laug at another catalog, as [runbook 6](6-add-a-fault.md) does | `unset CATALOG`, then the line again. |
| `... in the environment is not passed on; ...` first | your shell sets a variable laug sets itself | Nothing: laug uses its own value. Unset the variable to drop the line. |

## 3. Read four rauns the lab host ran

Before you run one, read what four of them recorded. Boot 2 ran persistence-latency three times after a warm-up it
did not count, each on a cold cell from a pool of one. Their JSONs are kept whole
([section 6](../RECORDS.md#6-persistence-latency-three-rauns) of `docs/RECORDS.md`). Boot 6 ran one more with no run
id, so it is named for the UTC time it started. It is a row of `docs/tables/rauns.jsonl`, which holds the same fields
under `.windows`.

The jq prints one line a raun. It shows the verdict and its reason, and the workflows the cell finished while the
fault held. Then the failed starts in each window from the fault on, and the seconds the signal took to come back.
You should see four ADVANCE lines:

```sh
jq -c '{run_id, result, reason, completed_during: .during.workflows_completed,
  failed: (.iterations_failed | {during, drain, after}), recovered_s: .time_to_recover_s}' \
  faults/results/raun-lab2-persistence-latency-{1,2,3}.json
jq -c 'select(.id == "raun-20261027-050724") | {run_id, result, reason,
  completed_during: .windows.during.workflows_completed,
  failed: (.iterations_failed | {during, drain, after}), recovered_s: .time_to_recover_s}' docs/tables/rauns.jsonl
```

```text
{"run_id":"raun-lab2-persistence-latency-1","result":"ADVANCE","reason":"","completed_during":0,"failed":{"during":540,"drain":6,"after":0},"recovered_s":"25"}
{"run_id":"raun-lab2-persistence-latency-2","result":"ADVANCE","reason":"","completed_during":0,"failed":{"during":538,"drain":7,"after":0},"recovered_s":"25"}
{"run_id":"raun-lab2-persistence-latency-3","result":"ADVANCE","reason":"","completed_during":0,"failed":{"during":537,"drain":5,"after":0},"recovered_s":"25"}
{"run_id":"raun-20261027-050724","result":"ADVANCE","reason":"","completed_during":0,"failed":{"during":547,"drain":4,"after":0},"recovered_s":"25"}
```

What it means. Read the first line out loud:

- While the fault held, the cell finished no workflows, and 540 starts failed.
- In the 55 s from the fault's removal to the after window, the drain, 6 more failed. In the 90 s of the after
  window, none did.
- 25 s after the fault came off, the signal was back under 23 ms, inside its budget of 60.
- The reason is empty, as an ADVANCE's always is.

That is an ADVANCE, and it should be. A raun expects the fault to hurt. What it asks is whether the cell came back.
Four things must hold:

- The fault showed in its signal.
- The signal came back under its max within the budget.
- The after window met every max.
- No work was lost. That means no failed start in the after window, and every workflow started more than 30 s before
  the end completed. It also means at least 1,600 completed in the after window, 20 a second for 80 of its 90 s.

One failed start in the after window would make it a BLOCK.

The drain is recorded and not judged, because clients come back a few seconds after the server's signal does.
[Stop 13](../LEARNING.md#stop-13) says why a raun counts failed starts at all.

It exits 0.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| `jq: error: Could not open file faults/results/...: No such file or directory` | you are not at the checkout's root | Go to the root and run it again. |
| other numbers | a file under `faults/results` was changed: these four are committed and never change | `git status faults/results` names it. |

## 4. Get the host ready

This is the first step that needs Docker. It runs three checks before anything builds a cell. `bin/laug upstream`
clones Omes at f0de206 the first time, which needs the network, and after that only checks out that commit.
`bin/laug deps -cell` checks every tool a lease, a load and a raun call. Bare, `bin/laug pool down` lists what an
earlier walk left, and removes nothing.

You should see Omes ready, 0 missing, and nothing to stop:

```sh needs=cell exit=2
bin/laug upstream     # Omes at f0de206: a clone the first time, a checkout after
bin/laug deps -cell   # each tool with its version; a MISSING row names what to install
bin/laug pool down    # what an earlier walk left; it removes nothing
```

```text
laug · upstream: ...
laug · upstream: ready: Omes f0de206 in .upstream/omes, the commit every committed result used
laug · deps -cell: 0 of 14 missing: what a lease, a load and a raun on compose cells call, the docker daemon and Omes among them
...
laug · pool down: no pool serve, gate pool or pool cell, and no per-cell dir or last lease: nothing to stop or remove
```

What it means:

- upstream's first line says whether it cloned Omes or found a clone. Its last line says Omes is at the commit every
  committed result used.
- deps prints a row a tool, each `ok` or `note`. A `note` does not count as missing. On the go row it means Go
  downloads the version Omes asks for on its first build. On the bin/laug row it says the tree has changes bin/laug
  was built without, such as a page you edited; it does not stop this page.
- pool down's line says the host is clear: no pool, no gate's pool, no cell and no lease file.

It exits 2: bare `pool down` only lists, and removes nothing.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| `laug · upstream: git ...` and an error | a git step failed, most often for the network | Run `bin/laug upstream` again once the network is back. |
| `laug · deps -cell: 1 of 14 missing: ...`, and a `MISSING` row | that tool is not on your PATH, or is too old | The row says what needs it. Install it, then this step again. |
| `MISSING  bin/laug` and `make build builds it from HEAD` | bin/laug was built from another commit | `make build`, then this step again. |
| `laug · pool down: would stop and remove ...`, then a list | a pool or a cell from an earlier walk is still up | `bin/laug pool down -remove-cells` stops and removes what it listed, cells and their data included. It stops every laug pool on this host, so check that nothing else of yours runs first. |
| `A gate runs (...)` under pool down's line | a gate runs on this host | Wait for it to end. A raun beside it would share the host with its cells. |

## 5. Run one

This is the long step: about 7 minutes on the lab host, and most of it is watching. You start a pool that keeps no
cell waiting and holds one at most, the pool boot 2 ran its rauns on. The raun asks it for a cell, so the pool builds
one for you, a cold cell. No other cell builds on the host while the raun reads its windows, so no other laug cell
competes with yours.

The pool's own lines, and compose's for the cell it builds and removes, go to `cells/run/pool.log`, so your terminal
shows laug's lines and none of compose's. That is about 130 lines a cell. To watch the cell being built, run
`tail -f cells/run/pool.log` in a second terminal at the checkout's root. Compose writes a line there as each
container is created and started, and as each is stopped and removed when the cell goes.

Type the lines one at a time, the jq command's three lines as one paste, or paste the block. `run` carries the run id
from one line to the next.

You should see the pool listen with no cell, the raun's lease, its summary, the cell given back, your JSON line, and
the pool's stop with nothing left:

```sh needs=cell
bin/laug pool serve -backend compose -min 0 -max 1 -log cells/run/pool.log &
sleep 2                                           # the pool takes a moment to listen
bin/laug pool status                              # your pool: min 0, max 1, no cell yet
run=rb3-$(date -u +%Y%m%d-%H%M%S)                 # this walk's run id
bin/laug raun persistence-latency -run-id "$run" || echo "the raun exited $?"
jq -c '{run_id, result, reason, completed_during: .during.workflows_completed,
  failed: (.iterations_failed | {during, drain, after}), recovered_s: .time_to_recover_s}' \
  "faults/results/raun-$run.json"
bin/laug pool down -remove-cells                  # the pool stopped; the raun gave its cell back already
```

```text
laug · pool serve: listening on 127.0.0.1:7700 (min 0, max 1, quota 2, max lease 2h0m0s, backend compose, ...); pid ... in cells/run/pool.pid; its log and the cells' output go to cells/run/pool.log; Ctrl-C or SIGTERM stops it
laug · pool status: 127.0.0.1:7700, 0 cells: 0 warm, 0 leased, 0 busy; min 0, max 1, quota 2, max lease 2h0m0s
laug · raun: waiting for a cell from the pool on 127.0.0.1:7700; Ctrl-C stops the wait
laug · raun: persistence-latency on lease ... (127.0.0.1:7233) until ... UTC, run raun-rb3-...; its windows take 330 s, plus up to 60 s to recover; Ctrl-C stops the fault, then the cell goes back
...
laug · fault persistence-latency on raun raun-rb3-...
server      1.31.0
thresholds  load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no load/releases/1.31.yaml for server 1.31.0
pool        no busy cell at laugd before the load; busy cells by window, most at once: before 0...
...
laug · raun: ... given back, its cell destroyed; .last-lease.json removed; the raun's JSON is faults/results/raun-rb3-....json
...
{"run_id":"raun-rb3-...","result":"...","reason":"...","completed_during":...,"failed":{...},"recovered_s":"..."}
laug · pool down: pool serve, pid ..., 0 gate pools; 0 pool cells: ... go
...
left: 0 containers, 0 volumes, 0 networks, 0 per-cell dirs
```

**What each line does**

1. **`bin/laug pool serve ... &`** starts the pool in the background, on loopback only. `-min 0` keeps no cell
   waiting, and `-max 1` builds one at most. `-log` sends its log to `cells/run/pool.log`. Its line names its
   settings and its pid file, `cells/run/pool.pid`, where `bin/laug pool down` finds it. Typed by hand, your shell
   also prints the job's number and pid.
2. **`sleep 2`** gives the pool time to listen.
3. **`bin/laug pool status`** asks the pool what it holds: no cell, min 0 and max 1. That is your pool answering.
4. **`run=...`** names this walk. It prints nothing.
5. **`bin/laug raun`** asks the pool for a cell, and after 1 s says it is waiting while the pool builds one. Then
   comes the lease line: the cell's id and address, the lease's end in UTC, your run id, and how long the windows
   take. The 330 s is 90 s of load before the fault, the 120 s hold, 30 s to settle and the 90 s after window; a
   recovery adds up to the 60 s budget. From this line on, Ctrl-C takes the fault off, then gives the cell back.
   The `...` under it is the raun's log, a line a step, timed in the table below. Then the summary: the fault and
   your run, the release the cell runs, the threshold set it is judged by, and whether another cell was busy on the
   host in any window. Its other lines depend on the verdict; boot 2's are below. Last, the cell is given back and
   destroyed, and the line names your JSON.
6. **`|| echo`** prints the raun's exit when it is not 0: 1 for BLOCK, 2 for a harness failure. It keeps the block
   going, so your JSON line still prints and the pool still stops. On an ADVANCE it prints nothing.
7. **`jq`** reads your JSON as step 3 read the lab host's. On the lab host your line reads like step 3's four:
   ADVANCE, an empty reason, 0 completed during, about 540 failed starts during, a few in the drain, 0 after, and
   about 25 s to recover. On a slower host it reads BLOCK with a never-steady reason, and
   [If your host is slower](#if-your-host-is-slower) says why.
8. **`bin/laug pool down -remove-cells`** stops the pool, then removes every pool cell on this host with its data.
   The raun gave its cell back, so 0 are left, and it stops the pool alone. Between its lines comes the pool's own
   last line, `laug · pool serve: stopped with 0 cells in its table, ...`, then `stopped laug pool serve, pid ...`.
   The last line counts what is left: 0 of each. Typed by hand, your shell then says the background job is done.

The block exits 0, pool down's exit. The raun's own exit is 0 for ADVANCE, 1 for BLOCK and 2 for a harness failure.

**What you see, and when**, on the lab host, counted from Enter on the raun line. The times are those of boot 2's
raun-lab2-persistence-latency-1, the first of step 3's four; the waiting line comes 1 s in. A cold cell builds faster
or slower on another host; the windows take the same time on any.

| About | You see | It means |
| --- | --- | --- |
| 0:01 | `laug · raun: waiting for a cell from the pool on 127.0.0.1:7700; Ctrl-C stops the wait` | No cell was waiting, so the pool is building yours. |
| 0:16 | `laug · raun: persistence-latency on lease ...` | The cell passed its check and is yours. |
| 0:16 | `[raun-rb3-...] ...Z thresholds: ...`, then `... fault persistence-latency on cell ...: signal persistence_latency_p99_ms > 23; steady 90s hold 120s budget 60s after 90s; load 480s at 20/s` | The plan, and the load starts. 480 s is every window at its longest and 90 s more; the raun stops the load once it is done. |
| 1:47 | `before: {...}`, then `injected` | The before window, the last 60 s of 90, met every max. The fault goes on. |
| 1:57 | `landed after 10 s: signal ...` | persistence p99 passed 23 ms: the fault shows. |
| 3:47 | `removed 0 s after the hold ended; during: {...}` | The 120 s hold is over, and the fault is off. |
| 4:12 | `recovered 25 s after removal; settling 30s, then 90s for the after window` | The signal is back under 23 ms, 35 s inside its budget. |
| 4:42 | `drain: {...}` | The 55 s from removal to the after window, recorded. |
| 6:13 | `after: {...}`, then `sweep: {"Completed":...}, started before ...` | The 90 s after window met every max, and every workflow started before the cutoff completed. |
| 6:13 to 6:35 | the summary, `ADVANCE`, then the given-back line | The verdict, and the cell destroyed. |

Nothing prints between these rows. The longest quiet is the hold, 1:57 to 3:47.

**The summary**, as boot 2 printed it for that raun:

```text
laug · fault persistence-latency on raun raun-lab2-persistence-latency-1
server      1.31.0
thresholds  load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no load/releases/1.31.yaml for server 1.31.0
pool        no busy cell at laugd before the load; busy cells by window, most at once: before 0, during 0, drain 0, after 0
landed      10 s after inject; signal peak 9344.545454545456 (max 23)
recovered   25 s after removal (budget 60 s); the removal ran 0 s after the hold ended
failed      before 0, during 540, drain 6, after 0, late 0, unplaced 0 iteration(s), by window
sweep       Completed 4092 started before 2026-10-23T16:28:52Z
softassert  before 0, during 0, drain 0, after 0, by window (recorded, not judged)
ADVANCE
```

- `server`: the Temporal release the cell runs, 1.31.0 unless you ask for another.
- `thresholds`: the maxima the before and after windows must meet. `bin/laug thresholds` prints them.
- `pool`: no other cell was busy at laugd before the load or in any window.
- `landed`: persistence p99 passed its max 10 s after the fault went on, and peaked at 9,344.5 ms against 23.
- `recovered`: back under the max 25 s after removal, with 35 s of the budget to spare.
- `failed`: failed starts by window. Late ones were logged after the after window ended; unplaced ones carried a
  time laug could not read.
- `sweep`: every workflow the load started more than 30 s before the after window's end, by status. Any status but
  Completed is a BLOCK.
- `softassert`: the server's own internal checks that failed and carried on, by window: recorded, not judged.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| `laug · pool serve: cells/run/pool.pid names pid ..., which runs: one pool serve a checkout; ...` | a pool from this checkout runs already, so yours did not start, and the raun would lease from that one | Typed one at a time: stop here, before the raun line. Pasted: press Ctrl-C once the raun's first line shows, and see [If you stop halfway](#if-you-stop-halfway-and-tidying-up). Then `bin/laug pool down` names what runs, and `bin/laug pool down -remove-cells` stops it. Then this step again. |
| `laug · pool serve: listen tcp 127.0.0.1:7700: bind: address already in use` | another program holds port 7700, such as another checkout's pool | `ss -ltnp 'sport = :7700'` names it. Stop it, then this step again. |
| `laug · pool status: no pool answers on 127.0.0.1:7700 (connection refused); ...` | your pool did not start; the lines after it fail too | The pool's own line, above it, says why. `cells/run/pool.log` holds the rest. |
| the waiting line, then nothing for a minute or more | the pool is still building your cell; on a host with none of the 8 images, Docker pulls them first | Wait. On the lab host a cell built from nothing served in 44 s, pulls included. `tail -f cells/run/pool.log` shows the build. The pool gives up after 5 minutes, its `-create-timeout`. |
| `laug · raun: the pool on 127.0.0.1:7700 refused: ...` | the pool could not hand over a cell. `backend create ...` after the colon means compose could not build it, for a cell port already taken or a failed pull; `cell ... not healthy before deadline` means it did not pass its check in 5 minutes; `pool is at max cells` means an earlier lease holds the pool's one cell | Read the end of `cells/run/pool.log`. `bin/laug pool status` lists a cell still leased. `bin/laug pool down -remove-cells`, then step 4 again. |
| `load generator exited: no Omes at .upstream/omes; run ... upstream first`, then `the raun exited 2`, then jq's `Could not open file` | Omes is not cloned: step 4 was skipped. The raun gave its cell back | `bin/laug upstream`, then this step again. |
| `jq: error: Could not open file faults/results/raun-rb3-...` | the raun wrote no JSON: it stopped before its fault went on | The lines above it say why. Nothing else to do: the next line still stops the pool. |
| `reason      never steady: ...`, `BLOCK`, then `the raun exited 1` | before any fault, a reading was over its max; nothing was injected | On the dev host this is expected: see [If your host is slower](#if-your-host-is-slower). On the lab host, run this step once more: lab batches run a warm-up raun first and do not count it. |
| `reason      did not land: ...` | the fault never showed in its signal; for this fault on the lab host, that is new | Keep your JSON and its two logs. `jq .during "faults/results/raun-$run.json"`, in the shell you ran step 5 in, shows what the cell read while the fault was on. In another shell, name the file: `faults/results/raun-rb3-*.json`. |
| `reason      did not recover ...`, `recovered in ...s, budget 60s`, `after window not clean: ...`, or a failed start after | the cell did not come back clean, which is what a raun exists to find | Keep the JSON; the cell is gone already. Run this step again to see whether it repeats. |
| `the raun exited 2` after `inject failed`, `remove failed` or `laug · stopped after inject ...` | laug failed, not the cell: a harness failure. The cell's state is unknown, and it was given back | The lines above say which step. A JSON with `"partial": true` records what was done to the cell. |
| `laug · pool down: a gate runs (...)` | a gate started on this host while the raun ran, and pool down would end it mid-wave | Your raun is done and its cell gone. Wait for the gate to end, then `bin/laug pool down -remove-cells`. |
| `... in the environment is not passed on; ...` first | your shell sets a variable laug sets itself | Nothing: laug uses its own value. Unset the variable to drop the line. |

## If your host is slower

laug's maxima were measured on the lab host, and a raun judges its before window by them before it injects anything.
There, the four rauns of step 3 read end-to-end p95, the time from a workflow's start to its close, at 49.3 to 49.8 ms
before the fault, under a max of 84. Workflow task schedule-to-start p99, how long a workflow task waits for a worker,
read 18.8 to 19.0 ms, under a max of 38. The dev host, a Linux VM with 24 vCPU and 47 GiB running Docker, read the
same two at 173 to 176 ms and 48.8 to 49.1 ms on single loads. Both are over their max. On a host like that, the raun
BLOCKs "never steady", and the fault never goes on.

So the raun ends about 2 minutes in, not 6 and a half. On the lab host the before window closes 1:47 in, and a raun
took 21 to 25 s from its last window to its exit. The lease comes later on a slower host: a cold lease took 22.4 s on
the dev host, against 17.5 s on the lab host.

What you see there: where the lab host printed `injected`, about 1:47 in, the summary comes instead, with no landed,
recovered or sweep line. Its reason names each reading that was over its max, as name=value, such as
`reason      never steady: workflow_task_schedule_to_start_p99_ms=... workflow_end_to_end_p95_ms=...`. On a host
slower still, more latencies join them. Then come `BLOCK`, the given-back line and `the raun exited 1`. In your JSON
line, `completed_during` and the three failed counts read null, since those windows never opened.

It happens on the lab host too, on a slower cell. Two oom-history rauns there ran on microVM cells, which boot as
Firecracker microVMs rather than under compose. Both ended "never steady" on end-to-end p95, at 90.5 and 90.4 ms
against 84, and exited in 124.7 and 107.3 s.

That BLOCK says the host is slower than the maxima allow, before any fault; it says nothing about the fault. To see your
readings, run `jq .before "faults/results/raun-$run.json"` in the shell you ran step 5 in, where `run` still holds your
run id. In another shell, name the file: `faults/results/raun-rb3-*.json`. Then `bin/laug thresholds` prints each max,
and its host line names the machine the maxima came from. A raun does not check the host it runs on. A gate refuses a
host its maxima were not measured on, and [load/baseline.md](../../load/baseline.md#a-set-for-a-new-host-type) says how
to measure a set for yours; `-thresholds <file>` judges a raun by such a set. The rest of step 5 holds on a slower host:
the pool, the lease, the summary's first four lines, the given-back line, your JSON and the pool's stop. Those are the
lines `bin/laug runbook check -cell` holds step 5 to, on either host.

## If you stop halfway, and tidying up

| Where you stopped | What is left | Do this |
| --- | --- | --- |
| Ctrl-C while the raun waits for a cell | `laug · raun: stopped before the pool on 127.0.0.1:7700 answered; the pool gives back a cell it could not hand over`, and the raun exits 1. The pool still runs, and destroys the cell it was building once it is built. | `bin/laug pool down -remove-cells`. |
| Ctrl-C after the lease line | `laug · raun: stopping: ... goes back once faults/run.sh ends; ...`. The raun takes the fault off and gives the cell back, and exits 130. Before the fault went on, it writes no JSON; once the fault was on, it writes one with `"partial": true`. The pool still runs. | Wait for the given-back line, then `bin/laug pool down -remove-cells`. |
| a second Ctrl-C | laug ends at once. The cell stays leased until the time the stopping line names. `faults/run.sh` got this Ctrl-C too: if it was removing the fault, the remove stops partway and nothing runs it again.<!-- source: faults/run.sh:70-74, :619 and :638, read in source, not run --> | `bin/laug pool down -remove-cells` removes it now, with its data. |
| you closed the terminal | the pool and the raun end with the terminal, without giving back the cell; the cell and `.last-lease.json` can stay<!-- source: cmd/laug/main.go:179, which catches SIGINT and SIGTERM, not SIGHUP; read in source, not run --> | `bin/laug pool down` lists what is left; `bin/laug pool down -remove-cells` removes it. |

**Tidying up after a walk.** Each check below takes a second.

1. `bin/laug pool down` says `nothing to stop or remove`, and exits 2.
2. `git status --short` prints nothing that step 5 wrote: git ignores each of its files by its name.
3. `git status --short --ignored faults/results cells/run/` lists them, each after `!!`: your JSON,
   `faults/results/raun-rb3-<UTC time>.json`; the load's logs, `faults/results/raun-rb3-<UTC time>.load.log` and
   `faults/results/load/raun-rb3-<UTC time>.log`; and `cells/run/`, which holds the pool's log, `cells/run/pool.log`,
   longer with each walk. Omes's lines carry your local time zone and this machine's name in the worker's id, and
   once the raun stops the load, its stack trace names paths on this host. Keep those logs out of anything you
   commit.
4. Keep the JSON if you want to compare a later walk with it; nothing reads it unless you name it. To remove all four:
   `rm faults/results/raun-rb3-* faults/results/load/raun-rb3-* cells/run/pool.log`. No committed file has a name
   that starts `raun-rb3-`, and `check/results_test.go` fails one that does.
5. `pgrep -af raun-rb3` prints nothing: the raun's load ended with it. A line that names `run-omes.sh`, `omes` or
   `program worker` is a load that outlived its raun; `kill` its pid.

## How this page is checked

`make test` runs steps 1 to 3 through `bin/laug runbook check`, and `bin/laug runbook check -cell` runs steps 4 and 5
too. The blocks above held 5 of 5 on the lab host in boot 8, step 5 ADVANCE.

## Next

- [Runbook 6](6-add-a-fault.md): add a fault to a copy of the catalog, and run it as step 5 runs this one.
- [faults/README.md](../../faults/README.md#the-catalog): what every fault measured on the lab host. To run another,
  put its name in place of persistence-latency in step 5 and keep the rb3 run id; its lease line gives its windows.
- [Stop 13](../LEARNING.md#stop-13): why a raun counts failed starts, and not only its signal.
- [Runbook 4](4-gate.md): a gate runs this page's fault as one of its items, beside two loads on cells of their own.

## Evidence

`cells/results/2026-10-23-boot1.txt`, `cells/results/2026-10-23-boot2.txt`, `cells/results/2026-10-24-batchE.txt`,
`cells/results/2026-10-27-boot6.txt`, `faults/results/raun-lab2-persistence-latency-{1,2,3}.json`, `faults/run.sh`,
`load/steady-state.yaml`, `docs/tables/rauns.jsonl#raun-20261027-050724`,
`docs/tables/records.jsonl#cells/results/2026-10-23-boot2/`,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-boot2/steps/R-persistence-latency-1.txt:4-13`,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-boot3.txt:177-178`, :222,
`docs/tables/quotes.jsonl#cells/results/2026-10-29-boot8.txt:125-133`, :135-170.
