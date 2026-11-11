## laug · verdict for raun 515e50e

Gate run g20261018-091444 on server 1.32.1: 3 item(s), 587.426 s from the gate start to this verdict on the wall clock (587.43 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE |  |  |
| pl | ADVANCE | ADVANCE |  |  |

Tolerances from cells/results/2026-10-17-lab-arm-calibration.txt: 20 A/A gates of 1.31.0 on m8i.8xlarge, the wave incumbent, steady, pl.

| Reading | steady (1.32.1) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 17.054 ms | 16.858 ms | +0.195 | 1.02 | ok |
| persistence_latency_p99_ms | 8.409 ms | 8.269 ms | +0.140 | 0.35 | ok |
| activity_schedule_to_start_over_10ms | none | 1.05% |  | 0.72 points | not produced |
| frontend_request_over_10ms | 1.06% | 1.06% | +0.0001 points | 0.32 points | ok |
| workflow_end_to_end_over_50ms | 1.51% | 2.61% | -1.1020 points | 0.64 points | ok |
| activity_schedule_to_start_p99_ms | none | 10.526 ms |  |  | recorded |
| workflow_end_to_end_p95_ms | 48.936 ms | 49.264 ms | -0.327 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 10.630 ms | 10.629 ms | +0.001 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.40% | 0.40% | 0.00-0 points |  | recorded |
| persistence_latency_over_10ms | 0.13% | 0.13% | -0.0031 points |  | recorded |
| workflows_completed | 5988 | 5990 | -2 |  | recorded |

| Rule | Result |
| --- | --- |
| R1 harness | pass |
| R2 coverage | pass |
| R3 contract | pass |
| R4 outcome | pass |
| R5 live control | pass |
| R6 identity | pass |
| R7 incumbent | pass |

Host quiet: yes.

**ADVANCE**
