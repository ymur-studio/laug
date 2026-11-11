## laug · verdict for raun 515e50e

Gate run g20261018-092540 on server 1.32.1: 3 item(s), 587.455 s from the gate start to this verdict on the wall clock (587.45 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE | regression | worse than the incumbent: persistence_latency_p99_ms 8.460 against 8.059 on incumbent, +0.401 past a tolerance of 0.35 |
| pl | ADVANCE | ADVANCE |  |  |

Tolerances from cells/results/2026-10-17-lab-arm-calibration.txt: 20 A/A gates of 1.31.0 on m8i.8xlarge, the wave incumbent, steady, pl.

| Reading | steady (1.32.1) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 16.844 ms | 16.301 ms | +0.543 | 1.02 | ok |
| persistence_latency_p99_ms | 8.460 ms | 8.059 ms | +0.401 | 0.35 | WORSE |
| activity_schedule_to_start_over_10ms | none | 0.85% |  | 0.72 points | not produced |
| frontend_request_over_10ms | 1.12% | 1.03% | +0.0915 points | 0.32 points | ok |
| workflow_end_to_end_over_50ms | 1.36% | 2.76% | -1.4041 points | 0.64 points | ok |
| activity_schedule_to_start_p99_ms | none | 9.991 ms |  |  | recorded |
| workflow_end_to_end_p95_ms | 48.893 ms | 49.310 ms | -0.417 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 11.251 ms | 10.311 ms | +0.940 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.34% | 0.31% | +0.0302 points |  | recorded |
| persistence_latency_over_10ms | 0.13% | 0.13% | +0.0064 points |  | recorded |
| workflows_completed | 5948 | 5951 | -3 |  | recorded |

| Rule | Result |
| --- | --- |
| R1 harness | pass |
| R2 coverage | pass |
| R3 contract | pass |
| R4 outcome | pass |
| R5 live control | pass |
| R6 identity | pass |
| R7 incumbent | steady: worse than the incumbent: persistence_latency_p99_ms 8.460 against 8.059 on incumbent, +0.401 past a tolerance of 0.35 |

Host quiet: yes.

Class: regression.

**BLOCK**
