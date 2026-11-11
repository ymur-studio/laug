## laug · verdict for raun 1c86d67

Gate run g20261015-223429 on server 1.31.0: 2 item(s), 518.005 s from the gate start to this verdict on the wall clock (518 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| steady | ADVANCE | BLOCK | regression | load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 97934.941 EXCEEDS, activity_schedule_to_start_p99_ms 48242.969 EXCEEDS, workflow_end_to_end_p95_ms 137612.667 EXCEEDS, service_error_rate 0.073 EXCEEDS, workflows_completed 287 BELOW (0.95 x 20/s x 300 s) |
| pl | ADVANCE | BLOCK | invalid | never steady: workflow_task_schedule_to_start_p99_ms=95222.2222222222 activity_schedule_to_start_p99_ms=48625 workflow_end_to_end_p95_ms=95000 service_error_rate=0.07790741437711704 |

| Rule | Result |
| --- | --- |
| R1 harness | pass |
| R2 coverage | pass |
| R3 contract | pass |
| R4 outcome | steady: expected ADVANCE, observed BLOCK: load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 97934.941 EXCEEDS, activity_schedule_to_start_p99_ms 48242.969 EXCEEDS, workflow_end_to_end_p95_ms 137612.667 EXCEEDS, service_error_rate 0.073 EXCEEDS, workflows_completed 287 BELOW (0.95 x 20/s x 300 s); pl: expected ADVANCE, observed BLOCK: never steady: workflow_task_schedule_to_start_p99_ms=95222.2222222222 activity_schedule_to_start_p99_ms=48625 workflow_end_to_end_p95_ms=95000 service_error_rate=0.07790741437711704 |
| R5 live control | pl: control: service_error_rate read null in the during window, not above its max 0.001 |
| R6 identity | pass |

Host quiet: yes.

Class: regression.

**BLOCK**
