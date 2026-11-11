# Runbook 2: lease a cell, load it, judge the run

You lease one throwaway Temporal server from laug's pool, put 5 minutes of steady load on it, and get ADVANCE or
BLOCK, with every reading beside its max.

Once a pool runs, it is three commands. Step 5 runs them for real, with the pool's start before them and the tidying
up after.

```console
$ bin/laug lease
$ bin/laug load -duration 5m -rate 20 -max-concurrent 50 -run-id rb2-first
$ bin/laug verdict rb2-first
```

Run everything on this page in bash, from the checkout's root. Under each command is what it prints. In those lines,
`...` stands for text that changes from run to run, or for lines left out.

## The words on this page

- **Cell**: one complete, throwaway Temporal server on this host, with its own Postgres.
- **Pool**: the cells laug keeps warm and ready; `bin/laug pool serve` runs it. Here it holds one cell at most and
  keeps none warm, so yours is built when you lease it.
- **Lease**: take a cell from the pool for one job, then give it back. A lease lasts 30 minutes unless you ask for
  another length.
- **Cold cell**: a cell the pool builds when you ask for one, because none was waiting.
- **Port block**: the five ports one cell answers on. Block 0 is 7233 for Temporal, 8080 for its web UI, 3000 for
  Grafana, 9090 for Prometheus and 8474 for Toxiproxy.
- **Prometheus**: the cell's own metrics store. The verdict and the baseline read it, and it goes when the cell goes.
- **Omes**: Temporal's load generator, which puts the load on a cell. laug runs it from `.upstream/omes`.
- **Load**: Omes starting workflows on the cell for a set time. Here: up to 20 a second for 5 minutes, at most 50
  running at once. Each workflow runs one activity that does nothing.
- **Run id**: the name of one load and of its files in `load/results`. This page's run ids start with `rb2-`.
- **Window**: the span from the load's start to its end. The verdict reads the cell's metrics over it.
- **Reading**: one number over the window, such as persistence p99: the 99th percentile of the time the server's
  calls to Postgres took.
- **Threshold set**: the max for each reading. `bin/laug thresholds` prints it.
- **Lost work**: a workflow the load started that never finished, or Omes ending in error.
- **Verdict**: ADVANCE when every reading is under its max and no work was lost; BLOCK otherwise.
- **Lab host**: the lab host, an m8i.8xlarge (32 vCPU, 128 GiB). The threshold set was measured on it, with nothing
  else running.
- **Dev host**: the dev host, a Linux VM with 24 vCPU and 47 GiB running Docker. It is slower than the lab host: see
  [If your host is slower](#if-your-host-is-slower).
- **Gate**: judges one server release by several items at once, each on a cell of its own. This page runs none, and
  step 5's last line refuses while one runs.
- **Raun**: one fault on one leased cell. This page runs no fault; [runbook 3](3-raun.md) runs one raun.
- Temporal's own words, such as workflow, activity, frontend, history and shard, are in
  [LEARNING's list](../LEARNING.md#before-you-start).

## Before you start

**You need**, each with what checks it:

- bin/laug built from the commit you have checked out. `make build` builds it ([runbook 1](1-offline.md) starts
  there), and the bin/laug row of `bin/laug deps -cell` checks it.
- Docker with compose v2, the temporal CLI, jq, yq, curl, bc and the rest: `bin/laug deps -cell`, which step 4
  runs, checks all 14 and says `0 of 14 missing` when every one is there.
- Omes at the commit every committed result used: `bin/laug upstream`, which step 4 runs. The first time, it clones
  Omes over the network.
- The network, the first time, for the cell's images and for Go. The first cell pulls 8 images, 778 MB as one saved
  tar on the lab host. The first load builds Omes, and Go may download the version Omes asks for first; the go row of
  `bin/laug deps -cell` says so. `bin/laug deps -cell` doesn't check the images. `docker image ls` lists what this
  host has, and the first lease pulls the rest.
- These ports free on this host: 7700 for the pool, block 0's five for the cell, and 9092 and 9093 for Omes.
  `ss -ltn | grep -wE '7700|7233|8080|3000|9090|8474|9092|9093'` prints nothing when all eight are free.
- No other pool and no cell from `bin/laug cell up`, since either one holds those ports. Step 4 lists both.
  Anything else on a port stops step 5 with `address already in use`, or a refused lease.
- No gate running on this host. Step 5's last line would stop a gate's pool and remove its cells, so it refuses
  while one runs. Step 4's listing names a gate's pool.
- About 1.2 GiB of memory free for the cell under load, and more for Omes's build and worker. `free -m` shows it:
  read its `available` column, in MiB. The lab host's run ended at 1,254 MiB across the cell's 11 containers.

**It takes** about 6 minutes on the lab host, 5 of them the load. Steps 1 to 4 take seconds. The first time on a host
adds the image pulls and Omes's first build. On a slower host, such as the dev host, setup takes longer and the verdict
can BLOCK: see [If your host is slower](#if-your-host-is-slower).

**It leaves** four files in `load/results`, each named by your run id: the run (`.json`), Omes's own output (`.log`),
the verdict (`.verdict.json`) and the baseline (`.baseline.json`). It also leaves the pool's log, `cells/run/pool.log`.
git ignores all five, so your tree stays clean. Docker keeps the 8 images. No cell, pool or lease is left: step 5's
last line says so. The first time, step 4 also leaves Omes's clone in `.upstream/omes`, which git ignores, and Go
keeps what it downloaded and built in its own cache.

## 1. See the bar your run has to clear

Your cell runs Temporal server 1.31.0 unless you ask for another release, so your run is judged by 1.31.0's threshold
set. Print it first. It takes under a second and needs no cell.

You should see six rows with a max, five without one, and the host the maxima were measured on:

```sh
bin/laug thresholds
```

```text
laug · thresholds: load/steady-state.yaml, the base (1.31.0 baselines), unchanged: no load/releases/1.31.yaml for server 1.31.0
metric                                         max  no data reads
workflow_task_schedule_to_start_p99_ms          38  EXCEEDS
activity_schedule_to_start_p99_ms               20  EXCEEDS
workflow_end_to_end_p95_ms                      84  EXCEEDS
persistence_latency_p99_ms                      23  EXCEEDS
service_error_rate                           0.001  EXCEEDS
frontend_request_p99_ms                         22  EXCEEDS
workflow_task_schedule_to_start_over_20ms     null  recorded, not judged (no max)
activity_schedule_to_start_over_10ms          null  recorded, not judged (no max)
persistence_latency_over_10ms                 null  recorded, not judged (no max)
frontend_request_over_10ms                    null  recorded, not judged (no max)
workflow_end_to_end_over_50ms                 null  recorded, not judged (no max)
host              m8i.8xlarge, 32 vCPU, 123 GiB, the host its numbers were measured on
compare           judging: ...
...
```

What it means:

- **Six rows have a max.** These are the readings a run can fail on. Five are latencies in milliseconds: four at the
  99th percentile, end to end at the 95th. The sixth, `service_error_rate`, is the share of the server's requests that
  failed, Canceled left out. Over any max, the verdict is BLOCK.
- **EXCEEDS under "no data reads"** means a reading with no data counts as over its max. A verdict with nothing
  behind it is not an ADVANCE.
- **Five rows read "recorded, not judged".** Each is the share of requests slower than a bucket edge, such as 10 ms.
  Your run records them, and nothing judges them yet. [Stop 7](../LEARNING.md#stop-7) says why they matter.
- **The host line** names where the maxima were measured: the lab host. It reads 123 GiB, not 128: that is the
  machine's memory as `free -b` totals it, in whole GiB ([load/baseline.md](../../load/baseline.md), A set for a new
  host type).
- **The compare lines** belong to a gate, which judges a server release beside the one it replaces. Step 5's verdict
  does not use them.

The maxima come from two 5-minute runs on the lab host, each on a cold cell from a pool of one, as step 5 runs it.
Each latency max is 1.7 to 2.5 times the worse run, and the error rate's is fixed at 0.001.
So a healthy run sits well under each one: the run in step 3 read persistence p99 at 9.2 ms against a max of 23.

It exits 0.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| a first line that names a file in `load/releases` for 1.31 | a release file now changes some rows for 1.31 | Its overrides are listed under the first line. This page's lines need the new set. |
| a line from jq or yq, and no table | the set did not resolve | `bin/laug deps -cell` checks jq and yq, which read it. |
| `No such file or directory` | bin/laug is not built, or you are not at the checkout's root | `make build` from the root. |

## 2. See the load step 5 will run

`bin/laug load` hands its settings to a script that runs Omes. With `-dry-run` it prints that command and runs
nothing. Here it is for the lab host's run `lab1-v1310-1`, with the cell's addresses given so the line does not
depend on a lease.

You should see one line:

```sh
bin/laug load -duration 5m -rate 20 -max-concurrent 50 -run-id lab1-v1310-1 -server-tag 1.31.0 \
  -address 127.0.0.1:7233 -prom http://127.0.0.1:9090 -project laug -dry-run
```

```text
DURATION=5m RATE=20 MAX_CONCURRENT=50 OMES_DIR='.upstream/omes' CELL_ADDRESS='127.0.0.1:7233' PROM_URL='http://127.0.0.1:9090' PROJECT=laug NAMESPACE=default RUN_ID=lab1-v1310-1 SERVER_TAG=1.31.0 load/run-omes.sh workflow_with_single_noop_activity
```

What it means:

- `-duration 5m` runs the load for 5 minutes. `-rate 20` starts at most 20 workflows a second. `-max-concurrent 50`
  keeps at most 50 running at once. `-run-id` names the run and its files.
- `workflow_with_single_noop_activity` is the scenario, the default: each workflow runs one activity that does
  nothing.
- `OMES_DIR='.upstream/omes'` is where Omes has to be. Step 4 puts it there.
- `-address`, `-prom`, `-project` and `-server-tag` name block 0's cell. In step 5 the lease gives them, so you leave
  them out.

The maxima were measured at exactly this load. Change `-rate`, `-max-concurrent` or
`-duration`, and they no longer apply.

It exits 0.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| `laug · load: ... in the environment is not passed on; ...` first | your shell sets a variable laug sets itself | Nothing: laug uses its own value. Unset the variable to drop the line. |
| `flag provided but not defined: ...` | bin/laug is older than this page | `make build`, then the line again. |

## 3. Read a run the lab host recorded

The lab host ran exactly that load on 1.31.0 and committed what it wrote. These two lines read the fields step 5 will
print for your run: first the window and Omes's exit, then the verdict, one reading and the count of finished
workflows. That run was on a cell from `bin/laug cell up`, not a lease, with the same load and the same set.

You should see two lines of JSON:

```sh
jq -c '{run_id, start, end, omes_exit, iterations_failed}' load/results/lab1-v1310-1.json
jq -c '{verdict, exit, window_s, persistence_p99: .readings.persistence_latency_p99_ms, completed: .readings.workflows_completed}' \
  load/results/lab1-v1310-1.verdict.json
```

```text
{"run_id":"lab1-v1310-1","start":"2026-10-23T06:54:04Z","end":"2026-10-23T06:59:05Z","omes_exit":0,"iterations_failed":0}
{"verdict":"ADVANCE","exit":0,"window_s":301,"persistence_p99":9.235424657002568,"completed":5906}
```

What it means:

- `start` and `end` are the window, in UTC: 301 s, the 5 minutes and a second.
- `omes_exit` 0 means Omes finished. `iterations_failed` 0 means no workflow failed; Omes calls each workflow an
  iteration.
- `ADVANCE`, exit 0: every reading under its max, and no work lost.
- `persistence_p99` read 9.235 ms against a max of 23, about 40% of it.
- `completed` is 5,906 workflows, against a floor of 5,700: 0.95 times 20 a second for 300 s.

Your run in step 5 writes the same two files. These lines read yours with your run id in place of `lab1-v1310-1`.

It exits 0.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| `Could not open load/results/lab1-v1310-1.json` | you are not at the checkout's root | Go to the root and run it again. |
| `jq: command not found` | jq is not installed | `bin/laug deps -cell` names the version laug needs. |

## 4. Get the host ready

This is the first step that needs Docker. It runs three checks: Omes and every tool are there, no pool runs, and no
cell from `bin/laug cell up` runs. Each takes a second or two, except the first `bin/laug upstream`, which clones over
the network.

First, Omes and the tools. `bin/laug upstream` clones Omes the first time, and after that only checks out its commit.
`bin/laug deps -cell` then checks every tool a lease and a load call. You should see Omes ready and 0 missing:

```sh needs=cell
bin/laug upstream
bin/laug deps -cell
```

```text
laug · upstream: ...
laug · upstream: ready: Omes f0de206 in .upstream/omes, the commit every committed result used
laug · deps -cell: 0 of 14 missing: what a lease, a load and a raun on compose cells call, the docker daemon and Omes among them
...
```

A row for each tool follows, each `ok` or `note`. A `note` on the go row is normal: Go downloads the version Omes asks
for on its first build. A `note` on the bin/laug row says the tree has changes bin/laug was built without, such as a
page you edited; it does not stop this page. It exits 0.

Then the pool. Bare, `bin/laug pool down` only lists what `-remove-cells` would stop and remove. You should see
nothing to stop:

```sh needs=cell exit=2
bin/laug pool down
```

```text
laug · pool down: no pool serve, gate pool or pool cell, and no per-cell dir or last lease: nothing to stop or remove
```

It exits 2 either way, because bare it removes nothing.

Then a cell from `bin/laug cell up`, which answers on block 0's ports, the ones your pool's cell needs. Bare,
`bin/laug cell down` only lists it. You should see nothing to remove:

```sh needs=cell exit=2
bin/laug cell down
```

```text
laug · cell down: project laug has no container, volume or network: nothing to remove
```

It exits 2, for the same reason.

**If you see something else**

| You see | It means | Do this |
| --- | --- | --- |
| `laug · upstream: git clone ...` and an error | the clone failed: no network, or GitHub did not answer | Run it again once the network is back. |
| a `MISSING` row from deps | that tool is not on your PATH, or is too old | The row says what needs it. Install it, then run deps again. |
| a `MISSING` bin/laug row that says `make build builds it from HEAD` | bin/laug was built from another commit | `make build`, then this step again. |
| a `MISSING` docker engine row | the Docker daemon does not answer | Start Docker, then this step again. |
| `laug · pool down: would stop and remove pool serve, pid ...`, then a list | a pool from this checkout still runs: runbook 1's, or an earlier walk's | `bin/laug pool down -remove-cells` stops it and removes what it listed, cells and their data included. |
| a pid in the `gate pools` row | a gate runs on this host | Wait for it to end. Its cells are not yours to remove. |
| only `last lease  .last-lease.json, removed` | an earlier lease's file is left, with no pool | `bin/laug pool down -remove-cells` removes it. |
| `laug · cell down: would remove project laug, ...` | a cell from `bin/laug cell up` runs on block 0's ports | Once you are done with it, `bin/laug cell down -remove-volumes` removes it with its data. |

## 5. Lease a cell, load it, judge it

This is the step with a real server: about 6 minutes on the lab host, 5 of them the load. You start a pool that holds
one cell at most and keeps none waiting. That is the pool the maxima were measured with, so nothing else builds on the
host during your load. [Runbook 3](3-raun.md) uses the same pool.

The first time on a host, this step also pulls the cell's 8 images, inside the lease's wait, and builds Omes, inside
the load's window. Walk this page first, and runbooks 3 and 6 skip both.

The pool's own lines, and compose's for each cell it builds and removes, go to `cells/run/pool.log`, so your terminal
shows laug's lines alone. To watch the cell being built, run `tail -f cells/run/pool.log` in a second terminal: compose
writes there as it creates, starts, stops and removes each container, network and volume. The pool's own lines there
start with a UTC date and time.

You should see the pool listen with min 0 and max 1, a lease on block 0's cell, the load's first and last lines, the
verdict, the baseline, the release, and the pool's stop with nothing left. On the lab host the verdict is ADVANCE. On
the dev host it is BLOCK, with latency rows at `EXCEEDS`, and the lines after it still run
([If your host is slower](#if-your-host-is-slower)):

```sh needs=cell
run=rb2-$(date -u +%Y%m%d-%H%M%S)  # this walk's run id: its files in load/results start with it
bin/laug pool serve -backend compose -min 0 -max 1 -log cells/run/pool.log & sleep 2
bin/laug lease                      # waits while the pool builds your cell
bin/laug load -duration 5m -rate 20 -max-concurrent 50 -run-id "$run"
bin/laug verdict "$run" || echo "verdict exit $?"  # before the release: it reads the cell's Prometheus
bin/laug baseline "$run"            # every reading, and each container's CPU and memory
bin/laug release -last              # the cell destroyed, its Prometheus with it
bin/laug pool down -remove-cells    # the pool stopped, and nothing left
```

```text
laug · pool serve: listening on 127.0.0.1:7700 (min 0, max 1, quota 2, max lease 2h0m0s, backend compose, ...); pid ... in cells/run/pool.pid; its log and the cells' output go to cells/run/pool.log; Ctrl-C or SIGTERM stops it
...
laug · lease: ... for laug until ... UTC; kept in .last-lease.json; bin/laug release ... gives it back
{
...
  "address": "127.0.0.1:7233",
  "ui": "http://127.0.0.1:8080",
...
}
laug · load: workflow_with_single_noop_activity on 127.0.0.1:7233 (lease ...), for 5m, run rb2-...; Ctrl-C stops it
...
window ... recorded in load/results/rb2-....json; Omes exit 0, 0 failed iteration(s) in its log
reading Prometheus at http://127.0.0.1:9090
laug · verdict for run rb2-... (... s window)
...
VERDICT: ...
laug · verdict: kept in load/results/rb2-....verdict.json
...
laug · baseline reading for run rb2-... (... s window)
...
written to load/results/rb2-....baseline.json
laug · release: ... given back, its cell destroyed; .last-lease.json removed
laug · pool down: pool serve, pid ..., 0 gate pools; 0 pool cells: ...
...
stopped laug pool serve, pid ...
left: 0 containers, 0 volumes, 0 networks, 0 per-cell dirs
```

**What you see, and when**, on the lab host, counted from Enter on the first line:

| About | You see | It means |
| --- | --- | --- |
| 0 s | `laug · pool serve: listening on 127.0.0.1:7700 (min 0, max 1, ...)` | Your pool is up, on loopback only. |
| 3 s | `laug · lease: waiting for a cell from the pool on 127.0.0.1:7700` | No cell was waiting, so the pool is building yours. |
| 20 s | `laug · lease: <id> for laug until <time> UTC`, then the lease as JSON | The cell passed its check and is yours for 30 minutes. |
| 20 s | `laug · load: workflow_with_single_noop_activity on 127.0.0.1:7233 (lease <id>)`, then `run rb2-...` | The load starts, and its window with it. |
| 22 s | two lines from Omes as its worker starts | Omes runs, and workflows arrive on the cell. |
| 1 min 20 s | `laug · load: 60 s of 300 s; Ctrl-C stops it`, then one a minute | Still running. Omes prints nothing more until the end. |
| 5 min 21 s | two lines from Omes as its worker stops, then `window ... Omes exit 0, 0 failed iteration(s)` | The load is over and recorded. |
| 5 min 21 s | the verdict's tables, then `VERDICT: ADVANCE` and `laug · verdict: kept in ...` | Every reading under its max, no work lost. |
| 5 min 24 s | the baseline's tables, then `written to ...` | Every reading in full, and each container's CPU and memory. |
| 5 min 40 s | `laug · release: <id> given back, its cell destroyed` | The cell and its data are gone. |
| then | the pool's stop, then `left: 0 containers, 0 volumes, 0 networks, 0 per-cell dirs` | Nothing of the walk runs. |

**What each line does**

1. **`run=...`** names this walk. It prints nothing. Every file the walk writes starts with this id, and git ignores a
   name in `load/results` that starts with `rb` and a digit.
2. **`bin/laug pool serve ... &`** starts the pool in the background, on loopback only, because nothing in a cell has
   auth ([SECURITY.md](../../SECURITY.md)). `-min 0` keeps no cell waiting, and `-max 1` builds one at most. `-log`
   sends its log to `cells/run/pool.log`. `sleep 2` gives it time to listen. Its first line names its settings and its
   pid file, `cells/run/pool.pid`, where `bin/laug pool down` finds it. Typed by hand, your shell also prints the
   job's number and pid.
3. **`bin/laug lease`** asks the pool for a cell. None is waiting, so the pool builds a cold cell on block 0. Then it
   checks it. The frontend answers, the namespace is there, and the history service owns every shard. Last, one workflow
   runs on it end to end. On the lab host that took 17.5 s. The first time on a host, Docker pulls the 8 images first,
   inside this wait. A cell built from nothing took 44 s there with `bin/laug cell up`, pulls included, and a lease adds
   its one-workflow check to that. Then comes the lease line. It names the cell, then `for laug`, the name the pool
   counts the lease under, laug by default. It ends with the time the lease ends, in UTC, 30 minutes on. The load needs
   the lease to last a minute past the run, so 30 minutes is enough. The lease as JSON follows. `address` is the cell's
   frontend. `ui` is Temporal's web UI: open http://127.0.0.1:8080 to watch the workflows arrive. `grafana` and
   `prometheus` are the cell's dashboards and metrics. `toxiproxy` is the proxy between the server and its Postgres,
   where [runbook 3](3-raun.md) adds its fault. `block` is the port block, 0. `.last-lease.json` keeps the lease, so the
   next lines find the cell without being told.
4. **`bin/laug load`** runs Omes against the leased cell. Its first line names the scenario, the cell, the lease and
   your run id. Then come the script's line and two lines from Omes as its worker starts. Omes's own lines carry your
   local time and this machine's name, so keep them out of anything you commit. Then laug says a line a minute, from
   `60 s of 300 s` to `300 s of 300 s`. The last line gives the window's start and end, in UTC, and the file the run
   is kept in. It ends with Omes's exit and its count of failed workflows. On the lab host the load took 301.5 s and
   its window read 301 s. The first load builds Omes first, inside the window, so that window reads longer.
5. **`bin/laug verdict`** reads the cell's Prometheus over the window and judges each reading against its max, then
   the lost work. It prints where it reads, a header with your run id and the window's length, the server release
   the cell runs, the threshold set, the readings table, the lost-work table, and the verdict on a line of its own.
   It keeps the verdict as JSON in your run's `.verdict.json`. It comes before the release, because the release
   destroys the cell and its Prometheus. ADVANCE is exit 0 and BLOCK exit 1. On a BLOCK, `|| echo` prints
   `verdict exit 1` and lets the next lines run, so the cell still goes back and the pool still stops. Typed by hand
   you can leave it off.
6. **`bin/laug baseline`** reads every reading over the whole window, with the workflows completed and their rate.
   Then it takes one CPU and memory sample of each of the cell's containers. It writes your run's `.baseline.json`.
   On the lab host it read 19.6 workflows a second, with the history service at 7.30% of one CPU and 555 MiB, and
   Postgres at 2.85% and 284 MiB. It needs the cell's containers, so it too comes before the release.
   [Stop 7](../LEARNING.md#stop-7) reads these numbers.
7. **`bin/laug release -last`** gives the cell back. The pool destroys it with its data, and laug removes
   `.last-lease.json`. It prints nothing until the cell is gone: 16.5 s on the lab host.
8. **`bin/laug pool down -remove-cells`** stops the pool, then removes every pool cell on this host with its data. It
   also stops a gate's pool left behind once that gate has ended. While a gate runs, it refuses and removes nothing.
   Here no cell is left, so it stops the pool alone. Between its lines comes the pool's own last line: it stopped
   with 0 cells in its table. The last line counts what is left: 0 of each. Typed by hand, your shell then says the
   background job is done.

Typed by hand, each line has its own exit, and the last exits 0. Under `bin/laug runbook check -cell` they end 0 on
either host, since `|| echo` takes the verdict's 1.

**The lab host's verdict**, for its run of this load, as it printed it:

```text
metric                                           value          max  result
workflow_task_schedule_to_start_p99_ms          19.104           38  ok
activity_schedule_to_start_p99_ms                9.993           20  ok
workflow_end_to_end_p95_ms                      49.898           84  ok
persistence_latency_p99_ms                       9.235           23  ok
service_error_rate                               0.000        0.001  ok
frontend_request_p99_ms                         10.902           22  ok
workflow_task_schedule_to_start_over_20ms      0.00194         null  no threshold yet
activity_schedule_to_start_over_10ms           0.00878         null  no threshold yet
persistence_latency_over_10ms                  0.00067         null  no threshold yet
frontend_request_over_10ms                     0.01091         null  no threshold yet
workflow_end_to_end_over_50ms                  0.04677         null  no threshold yet

lost work                                        value        limit  result
workflows_completed                               5906      >= 5700  ok (0.95 x 20/s x 300 s)
omes_exit                                            0            0  ok
iterations_failed                                    0               not judged apart from omes_exit
workflows_not_completed                              0            0  ok (5385 started before 2026-10-23T06:58:35Z, all Completed)

VERDICT: ADVANCE
```

Each judged latency read 40% to 59% of its max, the highest end to end. The error rate read 0. In the lost-work table,
`workflows_completed` must reach 95% of the 6,000 the rate allows. `omes_exit` must be 0. `workflows_not_completed`
counts the workflows started more than 30 s before the end that did not complete.

**To stay on the cell.** Typed by hand, you can stop after the baseline's line. The cell is yours until its lease
ends, 30 minutes after it began. Open the UI at http://127.0.0.1:8080, or run `bin/laug health timer 60s`, which arms
one timer and times it ([stop 1](../LEARNING.md#stop-1)). Then type the last two lines. If the lease runs out first,
the pool destroys the cell itself and refuses the release; `bin/laug pool down -remove-cells` still tidies up.

**If you see something else: the pool and the lease**

| You see | It means | Do this |
| --- | --- | --- |
| `laug · pool serve: cells/run/pool.pid names pid ..., which runs: one pool serve a checkout; ...` | a pool from this checkout runs already, so yours did not start, and the lease goes to that one | Stop. `bin/laug pool down -remove-cells` stops it and removes its cells. Then this step again. |
| `laug · pool serve: listen tcp 127.0.0.1:7700: bind: address already in use` | something else holds port 7700, such as another checkout's pool | `bin/laug pool status` says what answers there. Stop that, then this step again. If your lease went to it, `bin/laug release -last` first. |
| the pool's line says `min 3, max 6` | the pool runs without `-min 0 -max 1` | `bin/laug pool down -remove-cells`, then this step as written: cells building beside yours move its readings. |
| nothing after `laug · lease: waiting for a cell ...` for a minute or more | the pool is still building your cell; on a host with none of the 8 images, Docker pulls them first | Wait. `tail -f cells/run/pool.log` shows the pulls. The pool gives up after 5 minutes, its `-create-timeout`. |
| `laug · lease: the pool on 127.0.0.1:7700 refused: backend create ...` | the cell could not be built: a port of block 0 is taken, or a pull failed | `cells/run/pool.log` holds compose's error. `bin/laug cell down` lists a cell from `bin/laug cell up`. Then `bin/laug pool down -remove-cells`, and this step again. |
| `laug · lease: the pool on 127.0.0.1:7700 refused: cell ... not healthy before deadline: ...` | the cell started but did not pass its check in 5 minutes | Read `cells/run/pool.log`. On a slow host, add `-create-timeout 10m` to the pool's line. |
| `laug · lease: the pool on 127.0.0.1:7700 refused: pool is at max cells ...` | the pool's one cell is taken, by an earlier lease in this walk or by the cell of a lease you stopped with Ctrl-C | After an earlier lease, `bin/laug release -last`. After a Ctrl-C, wait until `bin/laug pool status` says `0 cells`. Then lease again. |
| `laug · lease: no pool answers on 127.0.0.1:7700 (connection refused); ...` | the pool did not start, or has stopped | The pool's own line, above it, says why. |

**If you see something else: the load**

| You see | It means | Do this |
| --- | --- | --- |
| `no Omes at .upstream/omes; run ... upstream first`, and exit 2 | Omes is not cloned: step 4 was skipped | `bin/laug upstream`, then the load again. The lease is still yours. |
| `lease on 127.0.0.1:7233 expires ..., run needs until ...`, and exit 3 | the lease ends less than a minute after the run would | `bin/laug release -last`, then `bin/laug lease`, then the load. |
| no line from Omes for a minute or more after `run rb2-...: ... against 127.0.0.1:7233/default`, though laug's `60 s of 300 s` can come first | the first load builds Omes first, and Go may download the version Omes asks for | Wait. The window starts before the build, so this run's window reads longer than 301 s, and laug's minute lines run ahead of Omes. |
| `laug · load: ... s, past its 300 s: Omes waits up to 120 s for the workflows in flight; ...` | Omes is finishing the workflows in flight, or it started late because the first load built it | Wait. Omes ends at most 2 minutes after its own 5 minutes, and a first build starts those later. |
| `window ...; Omes exit 1, ...`, and exit 1 | Omes stopped at a failed workflow | Your run's `.log` in `load/results` holds Omes's error. The verdict will BLOCK on `omes_exit`. |

**If you see something else: the verdict and the baseline**

| You see | It means | Do this |
| --- | --- | --- |
| `VERDICT: BLOCK`, then `verdict exit 1` | a reading over its max, or lost work | The rows that read `EXCEEDS` or `BELOW` name it. Off the lab host, with only latency rows at `EXCEEDS`, see [If your host is slower](#if-your-host-is-slower). On the lab host, a BLOCK on 1.31.0 is news: keep your run's files. |
| `no run metadata at load/results/rb2-....json`, then `verdict exit 2` | the load wrote no run JSON: you stopped it, or it failed before Omes ran | Run the load again on the same lease, then the verdict. |
| `laug · verdict: no verdict: ...`, then `verdict exit 2` | the verdict could not read the cell's Prometheus: the cell is gone | Its lease ran out, or the pool stopped. Run this step again. |
| `verdict exit 3` | the run was shorter than the window the set trusts, and lost nothing | The load stopped early. Run it again. |
| `no running containers in compose project laug-...; ...` from the baseline | the cell went before the baseline | The baseline reads the cell's containers, so it runs before the release. |

**If you see something else: the release and the pool's stop**

| You see | It means | Do this |
| --- | --- | --- |
| `laug · release: no pool answers on 127.0.0.1:7700 (connection refused); .last-lease.json still names it` | the pool stopped before the release: `bin/laug runbook check` stops it when a line of step 5 fails, or you stopped it | `bin/laug pool down -remove-cells` alone removes the cell and the lease file. |
| `laug · pool down: a gate runs (...): ...` | a gate runs on this host | Wait for it to end, then `bin/laug pool down -remove-cells`. |
| `left:` with containers above 0 | a cell from `bin/laug cell up` runs: its containers start with `laug-` too, but it is not a pool cell | `bin/laug cell down` lists it. `bin/laug cell down -remove-volumes` removes it with its data. |

**When a line fails.** Typed by hand, a failed line does not stop the next one, so read its row above before you go
on. Under `bin/laug runbook check -cell`, a failed line ends step 5's lines, and the check then stops the pool they
started, with its cell still up. `bin/laug release -last` then finds no pool. `bin/laug pool down -remove-cells` alone
removes the cell and `.last-lease.json`.

## If your host is slower

The maxima are the lab host's, and nothing on this page checks the host. On a slower host, a load with nothing wrong can
BLOCK. The dev host, a Linux VM with 24 vCPU and 47 GiB running Docker, read end-to-end p95 at 173 to 176 ms in single
loads at this rate, each with one dynamic config change. That is about twice the max of 84. There, step 5 ends
`VERDICT: BLOCK` with latency rows at `EXCEEDS`, and `|| echo` prints `verdict exit 1`. The baseline, the release and
the pool's stop still run, so step 5 still holds under `bin/laug runbook check -cell`.

What else changes on such a host:

- **Setup takes longer.** A cold lease took 22.4 s on the dev host, against 17.5 s on the lab host. A cell built from
  nothing took 55 s, against 44 s.
- **The clock can drift.** The dev host's wall clock moved 1.045 to 1.048 s for each second of its uptime clock,
  against 0.9999 on the lab host. Once the two have drifted more than 10 s apart since the pool started, the pool's
  log says `reaper: clock step detected` every 10 s, its `-reap-interval`. That is the host's clock, not your cell.
  `bin/laug host clock` measures it.
- **Read which rows BLOCK.** If only latency rows read `EXCEEDS`, and the error rate and the lost-work rows read `ok`,
  suspect the host before the server release.

To judge a run by your own host, measure a set on it. [load/baseline.md](../../load/baseline.md), A set for a new host
type, says how, and `bin/laug verdict <run> -thresholds <file>` judges a run by it. `bin/laug host type` prints this
host's vCPU and memory, and what a gate would say of it.

## If you stop halfway, and tidying up

Ctrl-C stops the line that runs, and only that one: the pool runs on in the background.

- **During the lease's wait**, the lease says `stopped before the pool on 127.0.0.1:7700 answered; the pool gives
  back a cell it could not hand over`. The pool finishes that cell, then destroys it, and `cells/run/pool.log` says
  `lease <id>: client gone before the reply; releasing`. Lease again once `bin/laug pool status` says `0 cells`.
  Before then, the lease reads `pool is at max cells`.
- **During the load**, Omes stops, and the load writes no run JSON. Its last line says so:
  `no run JSON for a load cut short`. The cell is still leased. `bin/laug release -last` gives it back.
- **The other lines** take seconds. Let them finish.

At any point, `bin/laug pool down -remove-cells` is the one line that tidies up. It stops the pool, removes every pool
cell with its data, the per-cell dirs and `.last-lease.json`, then counts what is left. Bare, `bin/laug pool down`
lists the same and removes nothing.

Your walk's files stay: four in `load/results` for each run, and `cells/run/pool.log`, which laug appends to on each
walk. git ignores them all. To remove them, `rm load/results/rb2-* cells/run/pool.log`. Never paste the logs into a
file you commit: Omes's lines carry your local time and this machine's name, and the pool's log can name paths on
this host.

## How this page is checked

`make test` runs steps 1 to 3 through `bin/laug runbook check`, and `bin/laug runbook check -cell` on this page runs
steps 4 and 5 too, holding each block to its exit and its lines. The blocks above held 7 of 7 on the lab host in
boot 8, a lab batch, step 5 ADVANCE.

## Next

- [Runbook 3](3-raun.md) runs one raun on the same pool of one: this load, with a fault in the middle.
- [Stop 7](../LEARNING.md#stop-7) reads a baseline like yours, and [stop 1](../LEARNING.md#stop-1)'s
  `bin/laug health timer 60s` runs on the cell while you hold it.

## Evidence

`cells/results/2026-10-23-boot1.txt`, `cells/results/2026-10-27-boot6.txt`, `load/results/lab1-v1310-1.json`,
`load/results/lab1-v1310-1.verdict.json`, `load/results/lab1-v1310-1.baseline.json`, `load/baseline.md`,
`load/steady-state.yaml`, `cmd/laug/pool.go`,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-boot1/steps/D1b.txt:3`, :9, :10,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-boot1/steps/D1c.txt:9-28`, :29,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-boot1/steps/D1d.txt:37`,
`docs/tables/quotes.jsonl#cells/results/2026-10-23-boot3.txt:177-178`, :222-223,
`docs/tables/quotes.jsonl#cells/results/2026-10-29-boot8.txt:125-133`, :135-170.
