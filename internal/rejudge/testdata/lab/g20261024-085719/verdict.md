## laug · verdict for raun 5fe3f15

Gate run g20261024-085719 on server 1.31.0: 3 item(s), 558.343 s from the gate start to this verdict on the wall clock (558.34 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE |  |  |
| pl | ADVANCE | BLOCK | invalid | never steady: workflow_task_schedule_to_start_p99_ms=155.555555555557 |

Tolerances from cells/results/2026-10-17-lab-arm-calibration.txt: 20 A/A gates of 1.31.0 on m8i.8xlarge, the wave incumbent, steady, pl.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 18.271 ms | 18.067 ms | +0.204 | 1.02 | ok |
| persistence_latency_p99_ms | 8.446 ms | 8.499 ms | -0.053 | 0.35 | ok |
| activity_schedule_to_start_over_10ms | 0.98% | 0.94% | +0.0359 points | 0.72 points | ok |
| frontend_request_over_10ms | 1.17% | 1.26% | -0.0971 points | 0.32 points | ok |
| workflow_end_to_end_over_50ms | 3.38% | 3.00% | +0.3792 points | 0.64 points | ok |
| activity_schedule_to_start_p99_ms | 9.999 ms | 9.997 ms | +0.002 |  | recorded |
| workflow_end_to_end_p95_ms | 49.497 ms | 49.382 ms | +0.115 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 11.883 ms | 12.618 ms | -0.735 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.54% | 0.51% | +0.0267 points |  | recorded |
| persistence_latency_over_10ms | 0.17% | 0.18% | -0.0114 points |  | recorded |
| workflows_completed | 5903 | 5913 | -10 |  | recorded |

| Rule | Result |
| --- | --- |
| R1 harness | pass |
| R2 coverage | pass |
| R3 contract | pass |
| R4 outcome | pl: expected ADVANCE, observed BLOCK: never steady: workflow_task_schedule_to_start_p99_ms=155.555555555557 |
| R5 live control | pl: control: service_error_rate read null in the during window, not above its max 0.001 |
| R6 identity | pass |
| R7 incumbent | pass |

Host quiet: yes.

Class: invalid.

**BLOCK**
