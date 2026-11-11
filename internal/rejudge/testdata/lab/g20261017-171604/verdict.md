## laug · verdict for raun 42446b7

Gate run g20261017-171604 on server 1.31.0: 3 item(s), 0.651 s from the gate start to this verdict on the wall clock (0.65 s monotonic).

Refused before the wave: this host's declared type is not m8i.8xlarge, the type load/steady-state.yaml was measured on: its numbers judge no other host type (docs/DESIGN.md section 7.1).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| incumbent | ADVANCE | none | invalid | no JSON: there is no incumbent/ directory |
| steady | ADVANCE | none | invalid | no JSON: there is no steady/ directory |
| pl | ADVANCE | none | invalid | no JSON: there is no pl/ directory |

R7: recorded, not judged: no tolerance in load/steady-state.yaml.

| Reading | steady (1.31.0) | incumbent (1.31.0) | d | Tolerance | Result |
| --- | --- | --- | --- | --- | --- |
| workflow_task_schedule_to_start_p99_ms | none | none |  |  | not compared |
| persistence_latency_p99_ms | none | none |  |  | not compared |
| activity_schedule_to_start_over_10ms | none | none |  |  | not compared |
| frontend_request_over_10ms | none | none |  |  | not compared |
| workflow_end_to_end_over_50ms | none | none |  |  | not compared |
| activity_schedule_to_start_p99_ms | none | none |  |  | not compared |
| workflow_end_to_end_p95_ms | none | none |  |  | not compared |
| service_error_rate | none | none |  |  | not compared |
| frontend_request_p99_ms | none | none |  |  | not compared |
| workflow_task_schedule_to_start_over_20ms | none | none |  |  | not compared |
| persistence_latency_over_10ms | none | none |  |  | not compared |
| workflows_completed | none | none |  |  | not compared |

| Rule | Result |
| --- | --- |
| R1 harness | harness: harness.json is missing, so make harness-test was not seen to pass |
| R2 coverage | incumbent: no JSON: there is no incumbent/ directory; steady: no JSON: there is no steady/ directory; pl: no JSON: there is no pl/ directory |
| R3 contract | incumbent: contract: no exit code was recorded; steady: contract: no exit code was recorded; pl: contract: no exit code was recorded |
| R4 outcome | incumbent: expected ADVANCE, observed nothing; steady: expected ADVANCE, observed nothing; pl: expected ADVANCE, observed nothing |
| R5 live control | pl: control: service_error_rate read null in the during window, not above its max 0.001 |
| R6 identity | gate.json records host type other with 32 vCPU and 123 GiB, and thresholds.json names a set measured on m8i.8xlarge with 32 vCPU and 123 GiB; incumbent: identity: the cell said server_version null, and the plan names incumbent 1.31.0; incumbent: identity: incumbent/g20261017-171604-incumbent.verdict.json records no thresholds_release; steady: identity: the cell said server_version null, and the gate SERVER_TAG is 1.31.0; steady: identity: steady/g20261017-171604-steady.verdict.json records no thresholds_release; pl: identity: server_tag null, and the gate SERVER_TAG is 1.31.0; pl: identity: pl/g20261017-171604-pl.json records no thresholds_release |
| R7 incumbent | pass |

Host quiet: no. poll.jsonl is missing, so the host was not seen quiet.

Class: refused.

**BLOCK**
