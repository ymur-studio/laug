# load

A steady load on one cell, and a verdict on it. `laug load` runs an Omes scenario against the leased cell and records
the run's window. `laug verdict` reads the server's own metrics over that window, counts the work that did not finish,
and prints ADVANCE or BLOCK. A gate runs the same load twice, as two items: steady on the release under test, and
incumbent on the release it replaces (`gate/plan.yaml`). The numbers here were measured on the lab host, an
m8i.8xlarge (32 vCPU, 128 GiB). Most come from boot 1, `cells/results/2026-10-23-boot1.txt`, cited by its step ids
(A5c, A6f, D1 to D7), and from the baseline, `load/baseline.md`.

## A run

```sh
bin/laug lease                                                       # a warm cell, kept as the last lease
bin/laug load -duration 5m -rate 20 -max-concurrent 50 -run-id <id>  # load/results/<id>.json
bin/laug verdict <id>                                                # ADVANCE exit 0, BLOCK exit 1
bin/laug baseline <id>                                               # every reading, CPU and memory at the end
bin/laug release -last
bin/laug thresholds -server-tag 1.32.1                               # the set a 1.32.1 run is judged by
```

The load is Omes f0de206 (`laug upstream` clones it), scenario `workflow_with_single_noop_activity`, its Go worker in
one process (`load/run-omes.sh`). The run's JSON records the window, the server tag, the version the cell reported,
and Omes's commit and exit. It also records the failed iterations, the task queue, the lease's `backend` (with a
microVM's `firecracker_version` and `cache_type`) and `host_virtualized`, whether this host is a VM. `laug verdict`
exits 2 when it gives no verdict, and 3 when a run lost nothing but is shorter than the set's window. At 20/s the two
baseline runs completed 5 894 and 5 909 workflows in 5 m, 98% of what the 20/s cap allows, computed from the counts in
`load/baseline.md`. At Omes's own rate a 61 s smoke completed 63.5 workflows/s (A6f). Its schedule-to-start p99 rose
to 389.6 ms for workflow tasks and 412.3 ms for activities. Persistence p99 held 9.69 ms, against 8.77 ms on the same
cell idle (A5c), so the queueing was likely not in the store.

## What a run is judged by

The set is `load/steady-state.yaml`, measured on the lab host on server 1.31.0. Six readings have a max. A row over
its max, or with no data, BLOCKs, unless a release file marks the reading as one that release does not produce (Eager
activities, below). Five shares have no max: each is read and recorded, and `laug verdict` prints "no threshold yet".
In a gate, R7 (`docs/DESIGN.md`, section 7.1) compares five readings of steady with incumbent's. For each, d is
the release's reading minus the incumbent's, and a d over its tolerance BLOCKs. Each tolerance is at least its noise
floor, 0.1 ms or 0.0005 (`load/steady-state.yaml`, its `compare:` block). The tolerances come from 20 A/A gates
(`docs/tables/records.jsonl#cells/results/2026-10-17-lab-arm-calibration.txt`).

| Reading | Max | R7 tolerance | Why the max, or the share's edge |
| --- | --- | --- | --- |
| `workflow_task_schedule_to_start_p99_ms` | 38 ms | 1.02 ms | 2x the worse baseline run, 19.0 ms |
| `activity_schedule_to_start_p99_ms` | 20 ms | | 2x the worse run, 10.0 ms |
| `workflow_end_to_end_p95_ms` | 84 ms | | 1.7x of 49.4 ms: both schedule-to-start limits and two task executions |
| `persistence_latency_p99_ms` | 23 ms | 0.35 ms | 2.5x of 9.1 ms: the persistence-latency fault adds 200 ms |
| `service_error_rate` | 0.001 | | the smallest rate a 5 m window resolves at this load |
| `frontend_request_p99_ms` | 22 ms | | 2x the worse run, 10.7 ms |
| `workflow_task_schedule_to_start_over_20ms` | none | | past 20 ms; its p99 sits in (10, 20] ms |
| `activity_schedule_to_start_over_10ms` | none | 0.0072 | past 10 ms; its p99 sits at the 10 ms edge |
| `persistence_latency_over_10ms` | none | | past 10 ms; its p99 sits in (5, 10] ms |
| `frontend_request_over_10ms` | none | 0.0032 | past 10 ms; its p99 sits at the 10 ms edge |
| `workflow_end_to_end_over_50ms` | none | 0.0064 | past 50 ms; its p95 sits under the 50 ms edge |

The queries, as the yaml holds them. A long one wraps here at a space: its lines, joined with one space, are its
`query:`.

```promql
# workflow_task_schedule_to_start_p99_ms
histogram_quantile(0.99, sum(rate(task_schedule_to_start_latency_bucket{task_type="Workflow"}[1m])) by (le)) * 1000
# activity_schedule_to_start_p99_ms
histogram_quantile(0.99, sum(rate(task_schedule_to_start_latency_bucket{task_type="Activity"}[1m])) by (le)) * 1000
# workflow_end_to_end_p95_ms
histogram_quantile(0.95, sum(rate(workflow_schedule_to_close_latency_bucket[1m])) by (le)) * 1000
# persistence_latency_p99_ms
histogram_quantile(0.99, sum(rate(persistence_latency_bucket[1m])) by (le)) * 1000
# service_error_rate
(sum(rate(service_error_with_type{error_type!="serviceerror_Canceled"}[1m])) or vector(0)) /
  sum(rate(service_requests[1m]))
# frontend_request_p99_ms
histogram_quantile(0.99, sum(rate(service_latency_bucket{service_name="frontend", operation!~"Poll.*"}[1m])) by (le))
  * 1000
# workflow_task_schedule_to_start_over_20ms
1 - sum(rate(task_schedule_to_start_latency_bucket{task_type="Workflow", le="0.02"}[1m])) /
  sum(rate(task_schedule_to_start_latency_bucket{task_type="Workflow", le="+Inf"}[1m]))
# activity_schedule_to_start_over_10ms
1 - sum(rate(task_schedule_to_start_latency_bucket{task_type="Activity", le="0.01"}[1m])) /
  sum(rate(task_schedule_to_start_latency_bucket{task_type="Activity", le="+Inf"}[1m]))
# persistence_latency_over_10ms
1 - sum(rate(persistence_latency_bucket{le="0.01"}[1m])) / sum(rate(persistence_latency_bucket{le="+Inf"}[1m]))
# frontend_request_over_10ms
1 - sum(rate(service_latency_bucket{service_name="frontend", operation!~"Poll.*", le="0.01"}[1m])) /
  sum(rate(service_latency_bucket{service_name="frontend", operation!~"Poll.*", le="+Inf"}[1m]))
# workflow_end_to_end_over_50ms
1 - sum(rate(workflow_schedule_to_close_latency_bucket{le="0.05"}[1m])) /
  sum(rate(workflow_schedule_to_close_latency_bucket{le="+Inf"}[1m]))
```

## Six traps

**Bucket edges.** Server latency histograms have edges at 1, 2, 5 and 10 ms and on to 1 000 s in the same steps (server
v1.31.0, common/metrics/config.go:239-258).<!-- source: read in the server's source, not run --> The lab host's
Prometheus listed those 19 edges and +Inf on all five histograms the readings use
(`docs/tables/quotes.jsonl#cells/results/2026-10-17-arm-pilot.txt:530`). Prometheus documents that
`histogram_quantile` interpolates inside a bucket.<!-- source: Prometheus documentation, histogram_quantile --> So
the lab baseline's 10.0, 10.0 and 49.4 ms (activity schedule-to-start, frontend, end-to-end; `load/baseline.md`) each
say only "at most this bucket's upper edge". In boot 1's 1.31.0 loads the activity share past 10 ms read 0.69% to
0.88%, while its p99 held 9.98 to 9.99 ms (`load/results/lab1-v1310-*.baseline.json`).

**Long polls.** A worker's PollWorkflowTaskQueue and PollActivityTaskQueue wait up to a minute when there is no work
(matching.longPollExpirationInterval, server v1.31.0, common/dynamicconfig/constants.go:1158-1162).<!-- source: read
in the server's source, not run --> On an idle cell under `laug health loop`, frontend p99 over one minute read
19 600 ms with every operation and 9.93 ms with `operation!~"Poll.*"` (A5c). Over 4 minutes, 26.4% of the frontend's
requests sat in the 50 to 100 s bucket. So the frontend reading and its share leave Poll* out.

**Canceled.** Matching's AddWorkflowTask and AddActivityTask return `serviceerror_Canceled` when a child partition
forwards a task to the root and no poller waits there. The child then writes the task to the database (server
v1.31.0, service/matching/task_queue_partition_manager.go:461-464, the Canceled from
physical_task_queue_manager.go:114).<!-- source: read in the server's source, not run --> This reads as an error
between matching's own partitions, which no client is likely to see. In boot 1's 61 s smoke at Omes's own rate, the
server counted 94 855 requests, and every error among them was Canceled (A6f). Counted raw, the error rate was 0.33%,
over the 0.001 max; with Canceled left out it was 0. The rate counts every role's requests, internal ones too.

**Eager activities on 1.32.** In boot 1's three 1.32.1 loads, activity schedule-to-start had no series; in the three
1.31.0 loads it read 9.98 to 9.99 ms (D1 to D6). system.enableActivityEagerExecution defaults to false in 1.31.0 and
true in 1.32.1 (common/dynamicconfig/constants.go:193-197 in v1.31.0, :203-207 in v1.32.1).<!-- source: read in the
server's source, not run --> The likely reason, and the one `load/releases/1.32.yaml` gives, is that an activity
started eagerly is never queued in matching. A reading with no data BLOCKs, so that file marks this reading and its
share "not produced on this release". A 1.32 run that has data for them is still judged. Boot 1 got no reading of the
eager counter itself. D7's four queries came back empty, with no series. Each ran in the second its HealthCheck
ended, so the likely cause is that it ran before the next scrape.

**The whole-run window.** Every query holds `[1m]`. `load/read-thresholds.sh` replaces it with the run's length and
evaluates once, at the window's end, so a p99 covers the whole run, not its last minute. `window: 5m` is the shortest
run a reading is trusted over: `laug baseline lab1-smoke60` exited 3 on a 61 s run (A6d; its row,
`docs/tables/loads.jsonl#lab1-smoke60`). Completions are the `workflow_success` counter, read at both edges of the
window, not `increase()`. Prometheus documents that `increase()` extrapolates to the window's edges.<!-- source:
Prometheus documentation, increase() --> Over A6f's 61 s it read 3872.5, not a whole number of workflows. After a
counter reset, as when history restarts, the count uses `increase()` all the same (`load/read-thresholds.sh`:38-53).

**Lost work.** A reading cannot see an Omes iteration that failed or a workflow still Running (`load/lost-work.sh`:2-3).
So `laug verdict` also BLOCKs when Omes exits with a code other than 0, or when completions fall under 0.95 x rate x
duration, 5 700 for 5 m at 20/s (`load/verdict.sh`:20-24). It BLOCKs, too, when a sweep of the run's task queue finds no
workflow. And it BLOCKs when the sweep finds one started more than 30 s before the end in a status other than
Completed. Boot 1's six loads passed all three checks: Omes exit 0, 5 906 to 5 967 completed, and 5 384 to 5 395
swept, all Completed (`load/results/lab1-v13*-*.verdict.json`). The sweep takes only workflows started before its
cutoff, 30 s ahead of the end, so it counts fewer workflows than the completions.

## Files

| File | What it does |
| --- | --- |
| `load/run-omes.sh` | `laug load`: Omes and its worker against a cell; refuses a lease too short for the run |
| `load/verdict.sh` | `laug verdict`: the thresholds and the lost work, ADVANCE or BLOCK |
| `load/baseline.sh` | `laug baseline`: every reading, and CPU and memory per service at the run's end, a microVM cell's from its guest |
| `load/read-thresholds.sh` | the one window reader, for the verdict, the baseline, fault rauns and candidates |
| `load/thresholds.sh`, `load/print-thresholds.sh` | `laug thresholds`: a release's set, the base and its release file |
| `load/sweep.sh` | `laug sweep`: a run's workflows by status, from the cell's visibility store |
| `load/lost-work.sh` | functions the verdict, `load/run-omes.sh` and `faults/run.sh` source: floors, sweep rules |
| `load/steady-state.yaml` | the set: the six maxima, the five shares, the host, R7's tolerances |
| `load/releases/1.32.yaml` | the one release file: two readings 1.32 does not produce |
| `load/baseline.md` | the lab baseline, and how a set is made for another host type |
| `load/flaky.txt` | named by `load/steady-state.yaml` as its flaky_list; nothing reads it; lists no scenario |
| `load/results/`, `load/testdata/` | each run's JSON, verdict and baseline; `make harness-test`'s script and fixtures |
