## laug · verdict for raun 42446b7

Gate run g20261017-172556 on server 1.31.0: 3 item(s), 588.668 s from the gate start to this verdict on the wall clock (588.66 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE |  |  |
| pl | ADVANCE | ADVANCE |  |  |

R7: recorded, not judged: no tolerance in load/steady-state.yaml.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 16.853 ms | 16.506 ms | +0.348 |  | recorded |
| persistence_latency_p99_ms | 8.271 ms | 8.219 ms | +0.052 |  | recorded |
| activity_schedule_to_start_over_10ms | 0.84% | 0.98% | -0.1451 points |  | recorded |
| frontend_request_over_10ms | 1.15% | 1.11% | +0.0405 points |  | recorded |
| workflow_end_to_end_over_50ms | 2.75% | 2.61% | +0.1380 points |  | recorded |
| activity_schedule_to_start_p99_ms | 9.991 ms | 9.999 ms | -0.008 |  | recorded |
| workflow_end_to_end_p95_ms | 49.305 ms | 49.264 ms | +0.042 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 11.497 ms | 11.046 ms | +0.451 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.34% | 0.20% | +0.1401 points |  | recorded |
| persistence_latency_over_10ms | 0.13% | 0.12% | +0.0167 points |  | recorded |
| workflows_completed | 5947 | 5971 | -24 |  | recorded |

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
