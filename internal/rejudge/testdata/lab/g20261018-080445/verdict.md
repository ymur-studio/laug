## laug · verdict for raun 997afca

Gate run g20261018-080445 on server 1.31.0: 3 item(s), 215.774 s from the gate start to this verdict on the wall clock (215.77 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | none | invalid | no exit record: incumbent/exit.json is missing |
| steady | ADVANCE | none | invalid | no exit record: steady/exit.json is missing |
| pl | ADVANCE | none | invalid | no exit record: pl/exit.json is missing |

Tolerances from cells/results/2026-10-17-lab-arm-calibration.txt: 20 A/A gates of 1.31.0 on m8i.8xlarge, the wave incumbent, steady, pl.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | none | none |  | 1.02 | not compared |
| persistence_latency_p99_ms | none | none |  | 0.35 | not compared |
| activity_schedule_to_start_over_10ms | none | none |  | 0.72 points | not compared |
| frontend_request_over_10ms | none | none |  | 0.32 points | not compared |
| workflow_end_to_end_over_50ms | none | none |  | 0.64 points | not compared |
| activity_schedule_to_start_p99_ms | none | none |  |  | not compared |
| workflow_end_to_end_p95_ms | none | none |  |  | not compared |
| service_error_rate | none | none |  |  | not compared |
| frontend_request_p99_ms | none | none |  |  | not compared |
| workflow_task_schedule_to_start_over_20ms | none | none |  |  | not compared |
| persistence_latency_over_10ms | none | none |  |  | not compared |
| workflows_completed | none | none |  |  | not compared |

| Rule | Result |
| --- | --- |
| R1 harness | harness: make harness-test exited 2 |
| R2 coverage | incumbent: no exit record: incumbent/exit.json is missing; incumbent: no JSON: incumbent/g20261018-080445-incumbent.json is missing; incumbent: no JSON: incumbent/g20261018-080445-incumbent.verdict.json is missing; steady: no exit record: steady/exit.json is missing; steady: no JSON: steady/g20261018-080445-steady.json is missing; steady: no JSON: steady/g20261018-080445-steady.verdict.json is missing; pl: no exit record: pl/exit.json is missing; pl: no JSON: pl/g20261018-080445-pl.json is missing |
| R3 contract | incumbent: contract: no exit code was recorded; steady: contract: no exit code was recorded; pl: contract: no exit code was recorded |
| R4 outcome | incumbent: expected ADVANCE, observed nothing; steady: expected ADVANCE, observed nothing; pl: expected ADVANCE, observed nothing |
| R5 live control | pl: control: service_error_rate read null in the during window, not above its max 0.001 |
| R6 identity | incumbent: identity: the cell said server_version null, and the plan names incumbent 1.31.0; incumbent: identity: incumbent/g20261018-080445-incumbent.verdict.json records no thresholds_release; steady: identity: the cell said server_version null, and the gate SERVER_TAG is 1.31.0; steady: identity: steady/g20261018-080445-steady.verdict.json records no thresholds_release; pl: identity: server_tag null, and the gate SERVER_TAG is 1.31.0; pl: identity: pl/g20261018-080445-pl.json records no thresholds_release |
| R7 incumbent | pass |

Host quiet: no. poll.jsonl is missing, so the host was not seen quiet.

Class: harness.

**BLOCK**
