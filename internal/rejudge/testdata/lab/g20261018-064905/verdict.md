## laug · verdict for raun 515e50e

Gate run g20261018-064905 on server 1.31.0: 3 item(s), 588.522 s from the gate start to this verdict on the wall clock (588.52 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE |  |  |
| pl | ADVANCE | ADVANCE |  |  |

Tolerances from cells/results/2026-10-17-lab-arm-calibration.txt: 20 A/A gates of 1.31.0 on m8i.8xlarge, the wave incumbent, steady, pl.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 15.914 ms | 16.073 ms | -0.159 | 1.02 | ok |
| persistence_latency_p99_ms | 8.213 ms | 8.106 ms | +0.107 | 0.35 | ok |
| activity_schedule_to_start_over_10ms | 1.16% | 0.90% | +0.2624 points | 0.72 points | ok |
| frontend_request_over_10ms | 1.07% | 1.06% | +0.0096 points | 0.32 points | ok |
| workflow_end_to_end_over_50ms | 2.64% | 2.81% | -0.1733 points | 0.64 points | ok |
| activity_schedule_to_start_p99_ms | 11.526 ms | 9.994 ms | +1.532 |  | recorded |
| workflow_end_to_end_p95_ms | 49.272 ms | 49.324 ms | -0.052 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 10.681 ms | 10.621 ms | +0.060 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.18% | 0.26% | -0.0823 points |  | recorded |
| persistence_latency_over_10ms | 0.12% | 0.12% | -0.0051 points |  | recorded |
| workflows_completed | 5950 | 5880 | +70 |  | recorded |

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
