# laug: ephemeral cells with a warm pool for release validation

Twelve sections, for an engineer who knows Temporal and has never seen this repository. Numbers come from the lab
host, an m8i.8xlarge (32 vCPU, 128 GiB), unless a line says the metal host, an m8id.metal-48xl (192 vCPU, 768 GiB).
Each number comes with the boot, batch or file that recorded it. [EVIDENCE](EVIDENCE.md) lists every gate run, and
`docs/tables/records.jsonl` every transcript.

## 1. Problem

A cell that outlives a suite hands the next suite whatever the last one left in it, and a fresh cell has to be made,
then trusted. On the lab host a lease from a drained pool took 17.5 s: the cell was made and health-checked while the
requester waited (`cells/results/2026-10-23-boot1.txt`:367). On 3 of 3 fresh cells the first HealthCheck workflow
failed after the Temporal CLI's `temporal operator cluster health` had answered (:160-170; 4.1). A reading needs care
too: an idle cell's frontend p99 read 19 600 ms with its long polls in and 9.93 ms without (:184-186; 4.2). laug keeps
cells warm and hands one out in 0 to 1 ms (:366). Six thresholds, each with a reason, are read over a run's whole
window. The verdict is ADVANCE or BLOCK, and it names the reading that decided it.

## 2. Goals and non-goals

Goals: self-serve ephemeral cells; a steady state with a stated reason for each max; repeatable faults; a verdict in
minutes. Non-goals: replacing unit tests; SDKs other than Go; production-grade pool scheduling; parity with Temporal
Cloud's control plane.

## 3. Proposal

`laug pool serve` keeps `-min` cells warm, hands one out on `POST /lease` with a TTL, destroys it on `POST /release` or
when the TTL expires, and builds the pool back. A cell is a whole Temporal deployment (section 4) owned by the lease
holder; nothing is shared between leases and nothing survives one. `laug reset` rebuilds the cell under the same lease
and port block. A backend is a script with three verbs, `create`, `destroy` and `health`.

```
requester (CI job, engineer, workflow Gate)
    |  laug lease -ttl 30m                    0 to 1 ms from warm
    v
  laug pool serve -- free port blocks -->  backend create <id>  (LAUG_BLOCK=n)
    |                                         |
    |  {address, prometheus, toxiproxy, ...}  v
    v                                      cell: nginx > frontend x2, history, matching, worker
  requester runs its suite                        postgres (via toxiproxy), prometheus, grafana
    |  laug release -last  (or the TTL)
    v
  backend destroy; replenish
```

Guardrails: a TTL on every lease (30 min by default, at most `-max-lease`, 2 h), 2 leases per requester, 6 cells per
host, port blocks the pool owns, and a lease past `-max` refused with 409. Compose (boot 1, P2 and P3): 3 warm in 50.7
s, serial; a lease from warm 0, 1 and 1 ms at p50, p95 and max over 20 cycles; a drained pool's cold lease 17.5 s; 3
warm cells 2037 MiB. Firecracker (`-backend firecracker`, run by root, each VMM under Firecracker's jailer by default)
restores a warm cell's snapshot into a microVM at `10.254.<block>.2`, host only. Boot 10 ran jailed, frozen and with
the drive's `cache_type` Unsafe, nested on the lab host. A create took 2.39 to 2.91 s alone (19 creates; F's 10 in 2.42
to 2.61) and 2.78 to 3.73 s in a pool of 2 to 6; a cold lease took 7.6 s and a reset 5.9 to 7.2 s (boot 10, J, G and
DS). Boot 11's 21 frozen creates took 2.30 to 2.90 s (thaw 1.13 to 1.57 s) and its 3 unfrozen ones 1.43 to 1.45 s
(boot 11, X and F8). On the metal host, bare metal at its default idle states, boot 12's 35 jailed frozen creates took
1.42 to 1.58 s (thaw 0.36 to 0.49 s; boot 12, R). Boot 13's 3, with C6 and C6P off, took 1.32 to 1.39 s (boot 13, V).

## 4. Cell anatomy

A lab cell is one compose project from `cells/compose/cell.yml`: two frontends behind nginx, one history, one matching
and one worker role (`temporalio/server:1.31.0`, 512 history shards), Postgres 16 behind a toxiproxy passthrough, the
UI, Prometheus and Grafana. That is 11 containers, using 576 MiB of memory 20 s after SERVING (boot 1, A1).
Elasticsearch is off by default: a cell with it read 1 497 MiB, and List found the HealthCheck in 10 to 532 ms against
3 to 9 ms on Postgres (boot 1, A2). Nothing in `load/steady-state.yaml` reads Elasticsearch, and since server 1.20, SQL
visibility on Postgres 12+ takes the full List query syntax.<!-- source: docs.temporal.io, Visibility --> Also out:
Loki, Jaeger, archival, TLS, authorization, Multi-Cluster Replication. Nothing has auth (SECURITY.md).

`cell.yml` began as `docker-compose-multirole.yaml` from `temporalio/samples-server` at f811a03. Its header and
comments say what laug changed, and `THIRD_PARTY_NOTICES.md` names the files that keep upstream's license. The roles
that write the cluster metadata record start one at a time, history first and the worker last, since two frontends
started together race to rewrite it ([stop 5](LEARNING.md#stop-5)). A pool cell mounts its own copy of the dynamic
config as a directory, since an edit that writes a new file never reaches a file mount. A fault edits only the leased
cell's copy, after checking its `cell-id` marker ([stop 6](LEARNING.md#stop-6)).

### 4.1 History shards

The shard count, `numHistoryShards`, is written to cluster metadata on first start and cannot change after.<!-- source:
Temporal's configuration reference, https://docs.temporal.io/references/configuration#numhistoryshards --> The 1.31.0
image's default is 4, by history's own log line and by `select count(*) from shards` (boot 1, A3), so laug pins
`NUM_HISTORY_SHARDS=512`. 512 is the example value of Temporal's
[configuration reference](https://docs.temporal.io/references/configuration#numhistoryshards) and the
[Helm chart's](https://github.com/temporalio/helm-charts/blob/main/charts/temporal/values.yaml) default, a documented
starting point rather than a sizing recommendation.<!-- source: the configuration reference and the Helm chart's
values.yaml, numHistoryShards --> It is also the count the Firecracker snapshot freezes. On three fresh cells history
owned its shards at 10.3 to 10.4 s, and `temporal operator cluster health` answered at 13.8 to 14.4 s. Yet the first
HealthCheck workflow failed, "Namespace default is not found". The next one completed at 16 to 17 s, computed from its
ID's start stamp and its round trip (boot 1, A4). A4's 24.5 to 24.9 s is when the health step returned. A cell is warm
only after all three. The client, `laug health check`, also lists each HealthCheck run in visibility by
`WorkflowType="HealthCheck"`. Nothing else in the round trip reads visibility, so a cell with a broken visibility
pipeline would otherwise pass.
`laug cell health` adds Prometheus: the five server targets up within 20 s (`cells/health.sh`).

### 4.2 Long polls in the frontend p99

`PollWorkflowTaskQueue` and `PollActivityTaskQueue` wait up to 60 s for work. On an idle cell the frontend p99 read
19 600 ms with them and 9.93 ms with `operation!~"Poll.*"`, with 26.4% of requests in the 50 to 100 s bucket (boot 1,
A5c). A p99 is a place in a bucket (server 1.31.0 `common/metrics/config.go`).<!-- source: server 1.31.0
common/metrics/config.go:239-258, the latency buckets' edges --> The steady-state maxima leave polls out.

### 4.3 A microVM cell

The Firecracker backend runs the same `cell.yml` inside a microVM, Docker and all, so the docker verbs and the faults'
mechanisms serve both backends unchanged. `make fc-snapshot` boots the image once and warms the cell. It freezes the
cell's containers with `docker pause` (unless `FREEZE=0`), pauses the microVM and writes a full snapshot of its memory
and device state.<!-- source: Firecracker docs/snapshotting/snapshot-support.md at v1.17.0 --> The disk is not in it
([stop 8](LEARNING.md#stop-8)). So the pool's create gives each clone a reflink of the golden disk, bound at the
recorded path in the VMM's own mount namespace. A network namespace of its own holds a veth pair to the host and a DNAT
to the one guest address every clone keeps. Before it resumes the VMM, create checks that both namespaces are not the
host's and that the recorded path is the cell's disk by inode. It also checks that the loaded VMM holds no handle on the
golden disk. It then sets the guest clock from the host over one ssh session and refuses an offset over 0.1 s. It thaws
a frozen snapshot's containers only after the set, so none of them should read the pause's
clock.<!-- source: Docker's docs, docker container pause --> A countdown armed before the freeze still runs on after
the thaw ([stop 9](LEARNING.md#stop-9); `cells/backend-firecracker.sh`, create). Every command a fault sends into a
guest first checks the cell id create wrote there. The drive's cache mode, `FC_CACHE_TYPE`, is Writeback unless set;
boots 9 to 13 sent Unsafe, except in their Writeback loads, and [CAPABILITIES, The pool](CAPABILITIES.md#the-pool) gives
what each mode read.

## 5. Load and steady state

Load is Omes (`temporalio/omes`, f0de206) through `laug load`: `workflow_with_single_noop_activity` at 20 workflows/s,
50 in flight, 5 min. Steady state is six maxima in `load/steady-state.yaml`, read from the server's metrics over the
whole window: workflow task schedule-to-start p99 38 ms, activity schedule-to-start p99 20 ms, end-to-end p95 84 ms,
persistence p99 23 ms, service error rate 0.001 and frontend p99 without polls 22 ms. A load also needs at least 5 700
completed. Three 1.31.0 loads read, in that order, 18.9 to 19.1, 10.0, 49.6 to 49.9 and 9.2 ms, an error rate of 0,
and 10.0 to 10.9 ms, with 5 906 to 5 967 completed (boot 1, D1 to D6). The error rate leaves out
`serviceerror_Canceled`. In a load at Omes's own rate every error was Canceled, 0.33% raw. In that load both
schedule-to-start p99s rose to 389.6 and 412.3 ms while persistence held 9.69 ms (boot 1, A6f; `load/README.md`).

Why this scenario: each workflow runs one activity that does nothing, and at 20/s the client is the limiter, not the
cell. That gives a flat baseline: two baseline runs within 7% of each other on all six readings (`load/baseline.md`).
Two methods here depend on it. Three readings sit at or just under a bucket edge, so the share past the edge shows a
move the percentile hides ([stop 7](LEARNING.md#stop-7)). A/A gates, a release run beside itself, measure the noise
that R7's tolerances sit above: 20 gates set the tolerances and 30 more held them (7.1). Omes names `throughput_stress`
as the scenario used for release validation. Its workflows add child workflows, signals, queries, updates and
continue-as-new, so it would reach server paths a no-op does not.<!-- source: Omes f0de206,
docs/throughput-stress.md:3-5 --> It would cost longer runs, more load on each cell and a new baseline. Every max and
every tolerance here was set on the no-op, so each would be set again, the tolerances from new A/A gates.

## 6. Failure modes

A raun reads three windows. **before**: the last 60 s of 90 s of load, every reading under its max, or the raun is
"never steady". **during**: the fault held 120 s, its signal over its max, or the fault "did not land" and the raun
cannot ADVANCE. **after**: the signal back under its max inside the fault's budget, then 90 s under every max. Failed
starts and short completions BLOCK too. Toxiproxy sits in every cell, so a persistence fault is one API call. Boot 2
ran the compose rows: 25 rauns, 19 ADVANCE. Of its BLOCKs, 5 are persistence-errors at toxicity 0.05 to 0.3, which
read 0 errors and did not land, and one is the norestore self-test (boot 2, the end line). Under 200 ms of latency the
cell made 103.5 persistence requests/s against 750.2 before it (boot 1, R3). In boot 2 the cell completed 0 workflows
while the latency held, and 537 to 540 starts failed in each hold (boot 2, R1). On nested microVM cells, 8 of 12 rauns
ADVANCEd on warm leases (batch E, F). On cold leases loaded about 5 s after the create, 12 of 52 ADVANCEd and 38 were
never steady before any fault (boot 9, F). The disk-slow fault, the one through the VMM, ADVANCEd 3 of 3 on jailed
cells idle 120 s (boot 10, DS). `faults/README.md` holds the 19 rows.

## 7. Release gate

`laug gate run <tag>` judges one server release on one host and writes the run to `gate/results/<gate id>/`.
`gate/plan.yaml` runs three items at once, a cell each, leased in this order: `incumbent`, steady's load on the release
the tag replaces (the plan's `incumbent:`, 1.31.0); `steady`, 5 min at 20/s; and `pl`, the persistence-latency raun.
R1 runs `make harness-test`, 1187 checks in boot 4, while the cells come up. In boot 4 the four counted gates handed
on their verdict 612.5 to 614.3 s after the start (boot 4, G). The verdict is a pure function of the run's directory,
and ADVANCE needs all seven:

| Rule | Holds when |
| --- | --- |
| R1 harness | `make harness-test` on this tree exited 0 within 300 s |
| R2 coverage | every planned item ran and wrote its JSON, whole, before the deadline |
| R3 contract | every script exited with a code its contract allows |
| R4 outcome | every item's result is its expect |
| R5 live control | in each fault item's during window, `service_error_rate` is above its max |
| R6 identity | every cell ran its item's release, judged by that release's thresholds, on the host type the set names |
| R7 incumbent | steady read no more than each compared reading's tolerance worse than the incumbent beside it |

R6's host check passes when the declared type is the set's `host:` type, the vCPU count is the set's and memory is
within 1 GiB of the set's (`internal/judge/rules.go`, g6Of; preflight runs the same check, `gate/lib.sh`,
host_check). A host under 16 vCPU is refused at preflight (`gate/run.sh`:254).

An item is one entry of the plan: a load or a fault raun on a cell of its own, with its expect. All three expect
ADVANCE. The gate leases its cells from a pool of its own, laugd, a `laug pool serve` at min 0 (`gate/run.sh`, step
3). After R1 passes comes the barrier: laugd shows 0 busy cells and only the leased projects are up, on two checks
10 s apart, within 60 s. Then the wave, the items a plan starts at once: every item starts within 1 s of the others.
A poller writes laugd's busy cells, the compose projects and `/proc/pressure` to `poll.jsonl` every 5 s. A watchdog
sends TERM to every item still running 780 s after the start, and KILL 30 s later (`gate/plan.yaml`, deadline_s and
kill_grace_s). The judge, `internal/judge`, run as `bin/laug judge -write`, reads the run's directory and writes
`verdict.json` and `verdict.md` into it.

A BLOCK carries one class. When its failures have more than one class, it takes the first in this table's order
(`internal/judge/classes.go`, classRanks).

| Class | A failure of this kind |
| --- | --- |
| refused | the gate could not run: a preflight check, a refused lease, a barrier that never held |
| harness | R1 failed |
| deadline | the watchdog stopped an item |
| regression | an item read worse: a max exceeded, a fault not recovered, failed iterations, R7 worse than the incumbent |
| unknown | a reason no row of the class table matches |
| control | R5: a fault that did not reach the error rate, or a plan with no fault item |
| invalid | an item that was never steady before its fault, wrote no JSON, ran the wrong release, or that R7 could not compare |

Host quiet holds on three counts. No poll saw a busy cell or a compose project the gate did not lease. Both laugd and
`docker compose ls` answered every poll. And laugd showed no busy cell in any window of a fault item. A BLOCK on a host
that was not quiet becomes class invalid, with rerun true. Refused and harness keep their class, because host noise does
not explain them. `class_if_quiet` keeps the class the BLOCK would have had (`internal/judge/rules.go`, evaluate;
`internal/judge/verdict.go`, output). Host noise never undoes an ADVANCE.

The warm-up rule. A batch's first gate since the host's start is a warm-up and is never counted, but the validation
batch ran before the rule, and its first gate stays in its 30
(`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-arm-validation.txt:592-593`). When it BLOCKs class invalid, one
more warm-up follows; the rule names no third. Boot 4 needed two. W1 BLOCKed class invalid on R4 and R5. Its pl item was
never steady: workflow task schedule-to-start p99 read 155.56 ms in the before window, against a max of 38. So pl never
injected its fault, and R5 had no during window. W2 BLOCKed the same way, and G1, the host's third gate, ADVANCEd.
Counting boot 4, a host's first gate BLOCKed class invalid in 4 of 5 boots, and the gate after it ADVANCEd in 3 of 4
(`cells/results/2026-10-24-boot4.txt`, W). `gate/sets/warm-up-m8i.8xlarge.txt` lists the warm-ups, 4 gates.

The four dynamic config candidates in `faults/candidates.yaml` stay out of the gate: each run took 520 to 651 s from
first load to restore (boot 3, The 14 together). [CAPABILITIES](CAPABILITIES.md#the-release-gate) holds the controls
and the noise ladder. An ADVANCE says one release held all seven rules on one host. Temporal describes its own rollout
in the post that section 12 cites.

### 7.1 The incumbent arm

R7 compares steady with the incumbent beside it: one host, one wave, one cell config. The arm is the item that runs
the plan's incumbent; steady is the release's side. R7's d is steady's reading less the incumbent's, and a row fails
when d is over its tolerance. Only worse BLOCKs: a row where steady reads better never fails. A row whose tolerance is
null is recorded, not judged. With every row null, R7 is in recording mode: it writes each comparison and judges none
(`load/steady-state.yaml`, the comment above `compare:`). The rows, from that `compare:` block, were set by 20
calibration A/A gates of release 1.31.0 (`gate/sets/calibration-arm-3item-m8i.8xlarge.txt`) by the rule
`laug gate tolerance` runs (`internal/tolerance/rule.go`). From a reading's d in the 20 gates, its tolerance is
T = up(max(2 · largest d - mean d, floor)). Here up rounds up to a multiple of 0.01 ms, or of 0.0001 for a share.
For persistence p99 (`docs/tables/quotes.jsonl#cells/results/2026-10-17-lab-arm-calibration.txt:265` and :584), the
largest d was 0.163 ms and the mean d -0.01965 ms. So 2 · 0.163 - (-0.01965) = 0.34565 ms, over the 0.1 ms floor,
and T rounds up to 0.35 ms. The five rows:

| Reading | Scale | Floor | Tolerance |
| --- | --- | --- | --- |
| workflow task schedule-to-start p99 | ms | 0.1 | 1.02 |
| persistence latency p99 | ms | 0.1 | 0.35 |
| activity schedule-to-start past 10 ms | share | 0.0005 | 0.0072 |
| frontend request past 10 ms | share | 0.0005 | 0.0032 |
| workflow end to end past 50 ms | share | 0.0005 | 0.0064 |

[CAPABILITIES](CAPABILITIES.md#the-release-gate) gives the 30 validation gates and the controls. There were 4 BLOCKs
in 63 A/A gates to boot 6, each a host's first or second gate (section 7, the warm-up rule); 0 of 55 counted over a
max (boot 4, The 19 gates together; boot 6, R11g).

### 7.2 Judged again

S1, bytes: the run's own judge, run again on a copy of its directory, writes `verdict.json` and `verdict.md` byte for
byte as the run did. `laug gate rejudge -all` does it over every committed run. A re-rendered run holds the gate's trap
verdict, written when the gate stopped before its judge. Rendered again from its own step and reason, it matches byte
for byte too (`internal/rejudge/s1.go`, Rerendered).

S2, signature: two runs of the same case have equal signatures. A signature keeps the verdict, the class, the failed
rules, each item's outcome, the judge version, the threshold set's sha256 and R7's per-row results. It leaves out what
differs from run to run by nature: ids, times, cells, readings, the host and `laug_commit` (`internal/rejudge/s2.go`,
Signature). `laug gate signature <run>` prints it; `laug gate compare <a> <b>` compares two.

A run's `gate.json` names the judge that judged it: its version, the sha256 of its sources, the Go release that built
it, and whether the committed tree holds that same judge (`tree_clean`). The sources are `internal/judge/versions.txt`
and the .go files of `internal/judge` and `internal/jsonx` that are not tests, so a test edit leaves the identity as it
was (`internal/judge/identity.go`, Sources). `laug judge -identity` prints it; [EVIDENCE](EVIDENCE.md) gives the
current one. Boot 4's 19 gates record eb37b18a8bbe, a sha256 taken while the sources still held the test files
(`docs/tables/gates.jsonl#g20261024-091739`, its `judge_sources`). Boot 6's 2 gates record af315e7661ab, the sum once
the module took its name, before later edits to the judge's files (`docs/tables/gates.jsonl#g20261027-054034`).
`laug gate rejudge` picks a run's judge by the version it records, not the sum. A run from before `bin/laug judge`
records the sha256 of its shell judge instead, and `internal/judge/versions.txt` names the version of each.

## 8. Orchestration on Temporal

`laug gate run <tag> -workflow` runs the gate as the workflow `Gate` (`internal/orchestrator/gate.go`). Each
`gate/step.sh` step is an activity whose id is the step's name, so a reordered build fails replay. Bash writes every
file the judge reads. The workflow owns the order, the waits, the cancel and the recovery. It runs on its own Temporal
CLI dev server (`laug orchestrator server`, 127.0.0.1:7400), with one Go SDK worker per checkout. Boot 4 KILLed the
worker 60 s and 300 s into a wave, and a new worker replayed the gate. Both gates ADVANCEd and handed on their verdict
at 612.8 and 614.2 s, against 612.5 s without a kill; S1 matched byte for byte and S2 was equal (boot 4, G4, G5).

A workflow cannot own a process, so a step keeps its state on disk, under the run's `.steps/`: it writes its pid record
first and its done file last. A later attempt answers from the done file, adopts a step whose record has no done file,
and never starts it again. A record whose pid, start time or boot id no longer matches is a non-retryable `StepLost`
(`internal/orchestrator/steps.go`). So a worker restart costs an attempt, never a second run.

Every step is scheduled as one activity type, `Step`, by choice. The steps share the rules above and one retry policy,
`StepOptions`; only the timeouts and the id differ, and `Gate` sets them on each call. Replay compares an activity's id
and its type, and the id already names the step.<!-- source: SDK v1.49.0 internal/internal_task_handlers.go:1653-1665,
isCommandMatchEvent: the activity id and the last part of the type's name. Read, not run. --> What one type gives up: a
view that names an activity by its type alone shows `Step` for every step. The SDK tags its activity metrics with
`activity_type`, and the server tags its own with `activityType`, so every step counts under `Step`. <!-- source: SDK
v1.49.0 internal/common/metrics/tags.go:38-44, ActivityTags, and constants.go:76, the tag activity_type; server v1.32.1
common/metrics/tags.go:37 and :222-226, ActivityTypeTag, the tag activityType, set in
service/history/api/respondactivitytaskcompleted/api.go:145. start-dev's server was 1.32.0 in the boots
(`cells/results/2026-10-24-boot4.txt`:127); the lines are read in v1.32.1, the version in the module cache
(THIRD_PARTY_NOTICES.md). Read, not run. --> A type per step would split them by step, and every recorded history
would fail replay, since each ActivityTaskScheduled event holds its type.

`Gate` returns a nil error on every path, so a cancel ends the workflow Completed, with the cancel in its result,
`GateResult`. Before the judge, a cancel takes the stop path, on a disconnected context that the cancel never reaches.
The stop path stops the live step, writes the trap verdict (7.2), BLOCK with exit 1, and releases the cells.
`gate/run.sh` answers TERM with the same verdict. From the judge on, a cancel changes only the result's reason, and the
judge's verdict stands, ADVANCE too (`TestGateCancelOnTheJudgePath`). Temporal's idiom is the other choice: run the stop
path on the disconnected context, then return the cancel error, so the workflow closes Canceled, with the result in the
error's details. <!-- source: SDK v1.49.0 workflow/context.go:55-64, NewDisconnectedContext's doc: cleanup on a
disconnected context, then return err, "workflow return CanceledError"; internal/internal_task_handlers.go:1890-1896, a
CanceledError's details on the CancelWorkflowExecution command; internal/internal_workflow_client.go:1970-1973, Get on a
Canceled close returns NewCanceledError with them. Read, not run. --> Completed keeps the verdict in the result that
`laug gate run` prints, and keeps a canceled gate recordable: `laug orchestrator record` takes only a Completed
history, and the `cancelled` fixture is one. What it costs: a list by ExecutionStatus does not tell a canceled gate
from a judged one; `laug gate status` does, from the progress query.

Every activity takes WaitForCancellation true, so the stop path starts once the activities report canceled. Boot 4
canceled two fake gates 30 s into their wave. One had the flag true, as the tree sets it; the other ran a scratch build
that set it false in `internal/orchestrator/gate.go`'s one `StepOptions` call. The stop path was scheduled 0.248 s after
the cancel request with the flag true, and 0.046 s after it with the flag false. Both completed with the trap verdict,
1.64 and 1.69 s after the request (`cells/results/2026-10-24-boot4.txt`:473, :477, :480-495).

`Gate` has no `workflow.GetVersion` branch, by choice. A gate hands on its verdict about 10 minutes after it starts
(section 7), so a build that changes `Gate`'s commands waits for the open gates to end, or ends them. Before it polls,
the worker replays each open `Gate` on its own queue on this build (`internal/orchestrator/replay.go`). When a replay
fails with `[TMPRL1100]`, nondeterminism, the worker refuses: it names the gate, does not poll and exits 2. It prints
the `laug gate terminate` and `laug gate stop` lines that end the gate and release it by hand. A history under 3 events
is skipped, not started yet.<!-- source: SDK v1.49.0 internal/internal_worker.go:2116 --> A gate started a moment before
the worker lists the open gates may be missing from that list.<!-- source: server v1.32.1
service/history/workflow/task_generator.go:414 and service/history/visibility_queue_task_executor.go:180, a start's
visibility record written from a queued task; the lag not measured --> With a
queue per checkout, that replay before the first poll is a replay check at deploy time. Temporal's Worker Versioning
meets the same need with Worker Deployments: a Pinned workflow stays on the Worker Deployment Version it started on.
A new build takes it only when `UpdateWorkflowExecutionOptions` moves it. <!-- source: docs.temporal.io, the page
Worker Versioning, section Versioning behaviors: "A Pinned Workflow is guaranteed to complete on a single Worker
Deployment Version"; go.temporal.io/api v1.63.5 enums/v1/workflow.pb.go:774-791. --> Server v1.32 is the last
release with the deprecated versioning implementations, Build ID compatibility and versioning rules among them. Their
removal is planned for v1.33, and the Worker Deployment APIs replace them. <!-- source: the temporalio/temporal v1.32.0
release notes, "Final release before removal of deprecated Worker Versioning": "`v1.32` is the final release containing
the deprecated Worker Versioning implementations"; "Complete removal is now planned for server `v1.33`"; "migrate to the
current Worker Deployment APIs". -->

The verdict is a deterministic function of the committed records; `laug gate rejudge` shows it again. Anything with a
model in it explains and proposes: it never judges, and changes a threshold or a release file only as a proposed diff.

## 9. The observability stack

Six processes on the host keep each leased cell's series after its release and judge nothing. They are Prometheus, the
store; Grafana, with the five boards of `_obs/stack/`; the OpenTelemetry Collector and Jaeger, for traces; cellstat;
and follow. Each listens on 127.0.0.1 alone (SECURITY.md names its ports) and keeps its state in one directory outside
every checkout. Each starts under `setsid`, `env -i`, `nice -n 10` and `GOMAXPROCS=2`, so no signal to a gate's
process group reaches it and a burst stays behind the cells (`_obs/obs.sh`, `start_part`).

The store federates each leased cell every 5 s, with the cell's own timestamps and staleness tracking on
(`_obs/prometheus.yml`; LEARNING, stop 19). follow names each cell from outside. Every 2 s it finds a gate by a readlink
of its laugd's stdout, `<gate dir>/laugd.log`. From the gate's files it writes a federation target for each item's
leased cell, from its lease to its release. The target carries the labels that outlive the cell: cell, life, block,
backend and server_tag, and in a gate gate_id, item and arm (`_obs/follow/targets.go`). Outside a gate, follow takes the
leased cells from the pool's `/status`. It also serves each gate's files as series, and `bin/obs backfill` renders the
same series at the gate's own times.

cellstat reads each laug container's cgroup by the pid route alone: `docker inspect`'s pid, then
`/proc/<pid>/cgroup`. It keeps the kernel's counters raw. It leaves out a key it could not read, so a 0 in a line is a
0 the kernel wrote (`_obs/cellstat/main.go`). With `-otlp`, the worker and the starter trace workflow Gate through the
Go SDK's OpenTelemetry interceptor. The history, not the trace, is the authority for steps and attempts
(`internal/orchestrator/trace.go`). A board's A/A band for a reading is its lowest and highest value over a committed
list of gates in `_obs/aa/`, never a filter on verdicts.

The stack stays down during gates because it failed its noise rule on CPU: the five parts other than cellstat read
6.772 to 7.136% of a CPU against 5% (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-obs.txt:335`; CAPABILITIES,
Observability). A gate runs while the host lock is held. It also runs while the pool on 127.0.0.1:7700 is a gate's and
that gate's timeline has no done line (`bin/obs follow -gate-running`). Then up, backfill, pack, view, purge and
`make obs-test` refuse, and so does down unless `-while-gate-runs`; find and status only read. The verdict path reads
nothing of the stack. `make harness-test` holds that: it fails when a file under load/, faults/ or gate/ names a stack
port, an `OBS_` variable, the store's datasource or a record's name.

## 10. Measures of success

Lease: 0 to 1 ms warm and 17.5 s drained on compose (boot 1, P2); 0 to 1 ms warm and 7.6 s cold on Firecracker, jailed
and nested (boot 10, J). Leases asked for in the second their pool started took 3.1 to 3.3 s on the metal host at its
default idle states, against 4.7 to 6.5 s nested in boot 11, read from its lease lines (boot 12, R). A gate handed on
its verdict in 612.5 to 614.3 s (`cells/results/2026-10-24-boot4.txt`:678-679). False BLOCKs: 4 BLOCKs in 63 A/A
gates to boot 6 (7.1); R7 BLOCKed 0 of 30, which bounds its false-BLOCK rate at 9.5% (95%)
(`gate/sets/validation-arm-3item-m8i.8xlarge.txt`). Not measured: serial against sharded suites; time to a test.

## 11. Risks and open questions

- **Readiness is not health:** on 3 of 3 fresh cells the first HealthCheck failed after `cluster health` answered
  (4.1).
- **A host's first gate** BLOCKed class invalid in 4 of 5 boots to boot 4; a batch opens with a warm-up
  (section 7, the warm-up rule). Boot 6's first gate ADVANCEd, but 9 rauns, a lease and a load had run on compose
  cells before it (boot 6, R11g).
- **The reaper is late:** it released a 10 min lease 9.7 s past its TTL, and the cell was gone from `/status` 31.2 s
  past (boot 1, P4).
- **Dynamic config is per cell:** an edit reached its cell's 5 roles in 13.5 to 18.6 s and never reached 2 other cells
  (boot 1, P6).
- **A restored guest's clock runs behind,** by 35.2 to 94.5 s (`cells/results/2026-10-29-boot9.txt`:183). A timer
  across a 120.9 s step fired 121.2 s late (batch E). With the clock set before the cell runs, a snapshot's age of 154
  to 3635 s left no trend common to two frozen passes in a 5 min load: 8 of 9 ADVANCE and one BLOCK at 86.00 ms (boot
  11, X and What it shows).
- **A copy's first minutes:** loaded 5 s after its create, 36 of 52 microVM cells read frontend p99 over its max before
  any fault (boot 9, F). Tested once, 3 of 3 were steady after 120 s idle (boot 10, DS). At boot 9's rate, 3 of 3 by
  chance is about 0.02: supported, not settled.
- **The microVM's gap:** before any fault, a median 9.75% of workflows took over 50 ms in the microVM against 2.49%
  for compose on 4 pinned CPUs (the end-to-end p95 gap, +27.41 ms in boot 10). The cost is per request in the guest.
  The 4-CPU budget is likely none of it: compose on 4 CPUs, its servers at GOMAXPROCS 4, did not show the gap (boot 10,
  G and What it shows; boot 11, gomaxprocs). At 8 vCPU the microVM read 3.22% against 9.09% at 4 vCPU in the same boot
  (boot 11, F8): nested, the guest's size is most of it (boot 11, What it shows). On the metal host at its default idle
  states the 4 vCPU microVM read 0.90%, 8 vCPU 0.74% and compose on 4 CPUs 9.77% (boot 12, R). With C6 and C6P off on
  the pool's CPUs the microVM read 0.39% and compose on 4 CPUs 0.85% (boot 13, V). The likely reading is that the gap
  seen nested was nesting's, about all of it. Boot 13's What it shows computes the nested gap less the metal one as
  +27.47 ms of end-to-end p95, against +27.33 ms for the nested gap of boots 10 and 11 pooled. Boot 13's check for a
  running package upgrade likely misfired on a helper process with the same short name; one real upgrade overlapped one
  load's last 36 s. All 15 loads are counted (boot 13, BOX and What it shows).
- **Density:** from 6 steady loads at once, items BLOCK on their own maxima (boot 4, N).
- Open: pool starvation under parallel suites; what a lab cell cannot test that a production suite would.

## 12. Prior art

- **Omes** (temporalio/omes): the load generator; `throughput_stress` "is the scenario used for release validation"
  (its throughput-stress page).<!-- source: Omes f0de206, docs/throughput-stress.md:3-5 -->
  **temporalio/samples-server** and **helm-charts**: the cell and its 512 shards.
- **Temporal Cloud's rollout**: Temporal's post [Building durable cloud control systems with
  Temporal](https://temporal.io/blog/building-durable-cloud-control-systems-with-temporal) describes its cells and
  deployment rings.<!-- source: the post: cells in deployment rings, Ring 0 "Synthetic traffic only", an entity
  workflow per cell -->
- **Toxiproxy** (Shopify), **warm pools** (connection pools, Lambda provisioned concurrency, CI runner pools,
  Firecracker snapshots) and the **Principles of Chaos** (a steady-state hypothesis, a blast radius, a loop).
