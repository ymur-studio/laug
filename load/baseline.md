# Load baseline

Two runs that agree within 10% make a baseline; one run is an anecdote. `laug baseline <id>` (`load/baseline.sh`)
reads the numbers below. It evaluates each threshold query over the run's whole window. It counts completed workflows
as the `workflow_success` counter's difference between the window's two edges, not with `rate()`, which extrapolates
short windows.<!-- source: Prometheus query functions, rate() and increase() -->

## The lab host baseline

`load/steady-state.yaml` holds the thresholds below. The host was the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), with
nothing else on it. Each run's JSON records the load (`docs/tables/loads.jsonl#labbase1-124757`): Omes f0de206 running
scenario `workflow_with_single_noop_activity` with its Go worker (`run-scenario-with-worker`, `load/run-omes.sh`), on
server 1.31.0. Each ran 5 m at 20 workflows/s with at most 50 in flight:
`laug load -duration 5m -rate 20 -max-concurrent 50`. Each run leased a cold cell from a pool of one
(`laug pool serve -backend compose -min 0 -max 1`), so no other cell idled beside it. The runs are `labbase1-124757`
and `labbase2-124757`, with 306 and 302 s windows. Their rows in `docs/tables/loads.jsonl` name each run's files,
`<run>.json` and `<run>.baseline.json`, with their sha256.

| Metric | Run 1 | Run 2 | Delta | Threshold | Rule |
| --- | --- | --- | --- | --- | --- |
| Workflow task schedule-to-start p99 (ms) | 19.0 | 18.8 | 1% | 38 | 2x the worse run |
| Activity schedule-to-start p99 (ms) | 10.0 | 10.0 | 0% | 20 | 2x |
| Workflow end-to-end p95 (ms) | 49.4 | 49.4 | 0% | 84 | 1.7x |
| Persistence latency p99 (ms) | 9.1 | 9.1 | 1% | 23 | 2.5x |
| Service error rate (Canceled excluded) | 0.000 | 0.000 | 0 | 0.001 | fixed |
| Frontend request p99, polls excluded (ms) | 10.7 | 10.0 | 7% | 22 | 2x the worse run |
| Workflows completed | 5 894 | 5 909 | 0% | | the client is the limiter |
| CPU, history / postgres (%) | 5.8 / 3.2 | 6.7 / 4.1 | | | one docker stats sample at the run's end |
| Memory, history / postgres (MiB) | 556 / 286 | 561 / 286 | | | the same sample |

The six readings with a threshold agree within 10% across the two runs; the widest gap is frontend p99, at 7%. Three
readings sit at or just under a bucket edge: activity schedule-to-start at 10 ms in both runs, frontend at 10 ms in
run 2, and end-to-end under 50 ms. Each of those says "at most that edge" ([the load page](README.md), Bucket edges).
For the two p99s, a threshold at about twice the edge still needs more than 1% of requests past it to read EXCEEDS.
Each threshold's reason is one line in `load/steady-state.yaml`.

Boot 1, a later session on the lab host, ran the same load six times more (`cells/results/2026-10-23-boot1.txt`, D1 to
D6). Each run had a fresh cell from `laug cell up`, and the runs alternated 1.31.0 and 1.32.1. The three 1.31.0 runs
read 18.9 to 19.1, 10.0, 49.6 to 49.9, 9.2 and 10.0 to 10.9 ms for the five latencies above, in order, and an error
rate of 0. Computed from each run's `.baseline.json`, every one of those is within 7% of this baseline's worse run. The
three completed 5 906 to 5 967 workflows. 6 of 6 read ADVANCE, the three 1.32.1 runs with `load/releases/1.32.yaml`
laid over the set (`load/results/lab1-v13*-*.verdict.json`).

A run on another host can be judged by a set of its own: `laug verdict <id> -thresholds <file>`. `laug gate run`
takes no such file (next section).

## A set for a new host type

`load/steady-state.yaml` is one host type's set. `laug gate run` judges by it alone (`gate/lib.sh` unsets
`THRESHOLDS`). It refuses a host whose declared type is not the set's `host:` type, whose vCPU count differs, or whose
memory is more than 1 GiB off (`gate/lib.sh`, host_check). A set for another type is made on that type, in this order:

1. Declare the host once: `laug host type -declare <type>` on it. Without `-declare`, `laug host type` prints the
   declaration, this host's vCPU and memory as `laug gate run` reads them, and what `laug gate run` would say.
2. The six maxima, by the rule of the lab host section above: two runs of 5 m at 20/s, each on a cold cell from a pool
   of one. Serve the pool with `laug pool serve -backend compose -min 0 -max 1`; then, for each run, `laug lease`,
   `laug load -duration 5m -rate 20 -max-concurrent 50 -run-id <id>`, `laug baseline <id>` and `laug release -last`.
   The two must agree within 10%. Each max is its Rule column's multiple of the worse run; the error rate's is fixed.
3. The host block, as step 1 prints it: `type` is the declared type, `vcpu` is `nproc`, and `mem_gib` is `free -b`'s
   total in whole GiB (an m8i.8xlarge's 128 GiB reads 123).
4. One commit of the set with every `compare:` row's tolerance null. That is recording mode, and `laug thresholds`
   says so: R7 records each comparison and judges nothing.
5. The tolerances, the way the lab host's were set (`docs/DESIGN.md`, section 7.1). Run 20 calibration A/A gates of
   the arm's wave, each `laug gate run <tag>` with the tag the plan names as `incumbent:` (`gate/plan.yaml`), so the
   release runs beside itself. Then `laug gate tolerance <the 20 gate ids> -evidence cells/results/<file>` prints the
   `compare:` block. Commit that block and nothing else. Then run 30 validation A/A gates on a fresh clone of that
   commit.

Until step 5's commit the set's `compare:` block stays in recording mode.

The caveat. A set names a type and was measured on one machine of it. The check reads only the declared type, `nproc`
and `free`. So a host of another family, declared as that type with the same vCPU and memory, passes. Before trusting
the verdicts of a replacement instance of the same type, run A/A gates on it under the set's tolerances.
