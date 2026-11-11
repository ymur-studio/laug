## laug · verdict for raun d14a4fe

Gate run g20261017-192202 on server 1.31.0: 3 item(s), 590.016 s from the gate start to this verdict on the wall clock (590.02 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE |  |  |
| pl | ADVANCE | ADVANCE |  |  |

R7: recorded, not judged: no tolerance in load/steady-state.yaml.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 17.252 ms | 17.197 ms | +0.055 |  | recorded |
| persistence_latency_p99_ms | 8.201 ms | 8.266 ms | -0.065 |  | recorded |
| activity_schedule_to_start_over_10ms | 0.81% | 0.81% | -0.0031 points |  | recorded |
| frontend_request_over_10ms | 1.17% | 1.10% | +0.0770 points |  | recorded |
| workflow_end_to_end_over_50ms | 3.01% | 3.05% | -0.0455 points |  | recorded |
| activity_schedule_to_start_p99_ms | 9.989 ms | 9.989 ms | 0.0-0 |  | recorded |
| workflow_end_to_end_p95_ms | 49.383 ms | 49.397 ms | -0.014 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 11.732 ms | 10.995 ms | +0.737 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.36% | 0.36% | -0.0014 points |  | recorded |
| persistence_latency_over_10ms | 0.12% | 0.13% | -0.0013 points |  | recorded |
| workflows_completed | 5955 | 5924 | +31 |  | recorded |

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
