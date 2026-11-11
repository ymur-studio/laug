## laug · verdict for raun d6eefcd

Gate run g20261018-074337 on server 1.31.0: 3 item(s), 559.536 s from the gate start to this verdict on the wall clock (559.54 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | BLOCK | invalid | incumbent: load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 166591.944 EXCEEDS, activity_schedule_to_start_p99_ms 45200.136 EXCEEDS, workflow_end_to_end_p95_ms 167566.054 EXCEEDS, service_error_rate 0.065 EXCEEDS, workflows_completed 280 BELOW (0.95 x 20/s x 300 s) |
| steady | ADVANCE | BLOCK | regression | load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 99610.046 EXCEEDS, activity_schedule_to_start_p99_ms 82063.388 EXCEEDS, workflow_end_to_end_p95_ms 166311.215 EXCEEDS, service_error_rate 0.075 EXCEEDS, workflows_completed 283 BELOW (0.95 x 20/s x 300 s) |
| pl | ADVANCE | BLOCK | invalid | never steady: workflow_task_schedule_to_start_p99_ms=75500.00000000012 activity_schedule_to_start_p99_ms=19706.25 workflow_end_to_end_p95_ms=92656.25 service_error_rate=0.07246891651865008 |

Tolerances from cells/results/2026-10-17-lab-arm-calibration.txt: 20 A/A gates of 1.31.0 on m8i.8xlarge, the wave incumbent, steady, pl.
The incumbent BLOCKed too, on workflow_task_schedule_to_start_p99_ms, activity_schedule_to_start_p99_ms, workflow_end_to_end_p95_ms, service_error_rate, workflows_completed: the host, or the cell config both arms share, is the likelier cause.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | 99610.046 ms | 166591.944 ms | -66981.898 | 1.02 | not compared |
| persistence_latency_p99_ms | 8.821 ms | 8.893 ms | -0.073 | 0.35 | not compared |
| activity_schedule_to_start_over_10ms | 66.77% | 73.41% | -6.6380 points | 0.72 points | not compared |
| frontend_request_over_10ms | 0.76% | 1.04% | -0.2825 points | 0.32 points | not compared |
| workflow_end_to_end_over_50ms | 100.00% | 100.00% | 0.0000 points | 0.64 points | not compared |
| activity_schedule_to_start_p99_ms | 82063.388 ms | 45200.136 ms | +36863.251 |  | not compared |
| workflow_end_to_end_p95_ms | 166311.215 ms | 167566.054 ms | -1254.840 |  | not compared |
| service_error_rate | 7.54% | 6.50% | +1.0332 points |  | not compared |
| frontend_request_p99_ms | 9.985 ms | 10.454 ms | -0.469 |  | not compared |
| workflow_task_schedule_to_start_over_20ms | 93.07% | 93.79% | -0.7197 points |  | not compared |
| persistence_latency_over_10ms | 0.05% | 0.05% | -0.0048 points |  | not compared |
| workflows_completed | 283 | 280 | +3 |  | not compared |

| Rule | Result |
| --- | --- |
| R1 harness | pass |
| R2 coverage | pass |
| R3 contract | pass |
| R4 outcome | incumbent: expected ADVANCE, observed BLOCK: incumbent: load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 166591.944 EXCEEDS, activity_schedule_to_start_p99_ms 45200.136 EXCEEDS, workflow_end_to_end_p95_ms 167566.054 EXCEEDS, service_error_rate 0.065 EXCEEDS, workflows_completed 280 BELOW (0.95 x 20/s x 300 s); steady: expected ADVANCE, observed BLOCK: load verdict BLOCK: workflow_task_schedule_to_start_p99_ms 99610.046 EXCEEDS, activity_schedule_to_start_p99_ms 82063.388 EXCEEDS, workflow_end_to_end_p95_ms 166311.215 EXCEEDS, service_error_rate 0.075 EXCEEDS, workflows_completed 283 BELOW (0.95 x 20/s x 300 s); pl: expected ADVANCE, observed BLOCK: never steady: workflow_task_schedule_to_start_p99_ms=75500.00000000012 activity_schedule_to_start_p99_ms=19706.25 workflow_end_to_end_p95_ms=92656.25 service_error_rate=0.07246891651865008 |
| R5 live control | pl: control: service_error_rate read null in the during window, not above its max 0.001 |
| R6 identity | pass |
| R7 incumbent | incumbent: not compared: incumbent did not ADVANCE (verdict BLOCK) |

Host quiet: yes.

Class: regression.

**BLOCK**
