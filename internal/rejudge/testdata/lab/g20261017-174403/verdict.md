## laug · verdict for raun d14a4fe

Gate run g20261017-174403 on server 1.31.0: 3 item(s), 588.907 s from the gate start to this verdict on the wall clock (588.9 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE |  |  |
| pl | ADVANCE | ADVANCE |  |  |

R7: recorded, not judged: no tolerance in load/steady-state.yaml.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 16.858 ms | 16.965 ms | -0.106 |  | recorded |
| persistence_latency_p99_ms | 8.226 ms | 8.182 ms | +0.044 |  | recorded |
| activity_schedule_to_start_over_10ms | 1.13% | 1.13% | -0.0026 points |  | recorded |
| frontend_request_over_10ms | 1.18% | 1.12% | +0.0688 points |  | recorded |
| workflow_end_to_end_over_50ms | 3.09% | 3.01% | +0.0780 points |  | recorded |
| activity_schedule_to_start_p99_ms | 11.267 ms | 11.228 ms | +0.039 |  | recorded |
| workflow_end_to_end_p95_ms | 49.409 ms | 49.386 ms | +0.024 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 11.812 ms | 11.210 ms | +0.602 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.36% | 0.35% | +0.0077 points |  | recorded |
| persistence_latency_over_10ms | 0.15% | 0.15% | -0.0021 points |  | recorded |
| workflows_completed | 5936 | 5927 | +9 |  | recorded |

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
