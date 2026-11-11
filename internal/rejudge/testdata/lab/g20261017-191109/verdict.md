## laug · verdict for raun d14a4fe

Gate run g20261017-191109 on server 1.31.0: 3 item(s), 589.679 s from the gate start to this verdict on the wall clock (589.67 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | ADVANCE |  |  |
| steady | ADVANCE | ADVANCE |  |  |
| pl | ADVANCE | ADVANCE |  |  |

R7: recorded, not judged: no tolerance in load/steady-state.yaml.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 16.926 ms | 16.438 ms | +0.488 |  | recorded |
| persistence_latency_p99_ms | 8.057 ms | 8.034 ms | +0.023 |  | recorded |
| activity_schedule_to_start_over_10ms | 1.00% | 0.76% | +0.2414 points |  | recorded |
| frontend_request_over_10ms | 0.99% | 0.92% | +0.0760 points |  | recorded |
| workflow_end_to_end_over_50ms | 2.90% | 2.57% | +0.3276 points |  | recorded |
| activity_schedule_to_start_p99_ms | 10.000 ms | 9.986 ms | +0.014 |  | recorded |
| workflow_end_to_end_p95_ms | 49.350 ms | 49.251 ms | +0.099 |  | recorded |
| service_error_rate | 0.00% | 0.00% | 0.0000 points |  | recorded |
| frontend_request_p99_ms | 10.000 ms | 9.994 ms | +0.005 |  | recorded |
| workflow_task_schedule_to_start_over_20ms | 0.28% | 0.28% | 0.00-0 points |  | recorded |
| persistence_latency_over_10ms | 0.13% | 0.12% | +0.0140 points |  | recorded |
| workflows_completed | 5894 | 5884 | +10 |  | recorded |

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
