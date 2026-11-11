## laug · verdict for raun 515e50e

Gate run g20261018-075357 on server 1.31.0: 3 item(s), 587.039 s from the gate start to this verdict on the wall clock (587.04 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE |  |  |
| pl | ADVANCE | ADVANCE |  |  |

Tolerances from cells/results/2026-10-17-lab-arm-calibration.txt: 20 A/A gates of 1.31.0 on m8i.8xlarge, the wave incumbent, steady, pl.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 16.887 ms | 17.087 ms | -0.200 | 1.02 | ok |
| persistence_latency_p99_ms | 7.901 ms | 7.975 ms | -0.074 | 0.35 | ok |
| activity_schedule_to_start_over_10ms | 0.93% | 0.92% | +0.0045 points | 0.72 points | ok |
| frontend_request_over_10ms | 1.08% | 1.16% | -0.0871 points | 0.32 points | ok |
| workflow_end_to_end_over_50ms | 2.98% | 2.78% | +0.1988 points | 0.64 points | ok |
| activity_schedule_to_start_p99_ms | 9.996 ms | 9.995 ms | 0.000 |  | recorded |
| workflow_end_to_end_p95_ms | 49.376 ms | 49.316 ms | +0.060 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 10.808 ms | 11.644 ms | -0.836 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.37% | 0.38% | -0.0067 points |  | recorded |
| persistence_latency_over_10ms | 0.15% | 0.14% | +0.0049 points |  | recorded |
| workflows_completed | 5920 | 5954 | -34 |  | recorded |

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
