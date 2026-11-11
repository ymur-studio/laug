## laug · verdict for raun aafc0db

Gate run g20261014-220820 on server 1.31.0: 2 item(s), 546.643 s from the gate start to this verdict on the wall clock (543.97 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| steady | ADVANCE | BLOCK | regression | load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 119428.571 EXCEEDS, activity_schedule_to_start_p99_ms 48590.000 EXCEEDS, workflow_end_to_end_p95_ms 164360.676 EXCEEDS, service_error_rate 0.074 EXCEEDS, workflows_completed 283 BELOW (0.95 x 20/s x 300 s) |
| pl | ADVANCE | BLOCK | invalid | never steady: workflow_task_schedule_to_start_p99_ms=94849.99999999997 activity_schedule_to_start_p99_ms=44999.999999999985 workflow_end_to_end_p95_ms=92794.11764705881 service_error_rate=0.05930131826389742 |

| Rule | Result |
| --- | --- |
| R1 harness | pass |
| R2 coverage | pass |
| R3 contract | pass |
| R4 outcome | steady: expected ADVANCE, observed BLOCK: load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 119428.571 EXCEEDS, activity_schedule_to_start_p99_ms 48590.000 EXCEEDS, workflow_end_to_end_p95_ms 164360.676 EXCEEDS, service_error_rate 0.074 EXCEEDS, workflows_completed 283 BELOW (0.95 x 20/s x 300 s); pl: expected ADVANCE, observed BLOCK: never steady: workflow_task_schedule_to_start_p99_ms=94849.99999999997 activity_schedule_to_start_p99_ms=44999.999999999985 workflow_end_to_end_p95_ms=92794.11764705881 service_error_rate=0.05930131826389742 |
| R5 live control | pl: control: service_error_rate read null in the during window, not above its max 0.001 |
| R6 identity | pass |

Host quiet: yes.

Class: regression.

**BLOCK**
