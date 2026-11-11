## laug · verdict for raun 25851b0

Gate run g20261014-212526 on server 1.31.0: 2 item(s), 310.781 s from the gate start to this verdict on the wall clock (309.52 s monotonic).

Refused before the wave: lease steady: laugd refused it (curl exit 22): cell b5fb9ba7 not healthy before deadline: context deadline exceeded.

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| steady | ADVANCE | none | invalid | no exit record: steady/exit.json is missing |
| pl | ADVANCE | none | invalid | no JSON: there is no pl/ directory |

| Rule | Result |
| --- | --- |
| R1 harness | harness: harness.json is missing, so make harness-test was not seen to pass |
| R2 coverage | steady: no exit record: steady/exit.json is missing; steady: no JSON: steady/g20261014-212526-steady.json is missing; steady: no JSON: steady/g20261014-212526-steady.verdict.json is missing; pl: no JSON: there is no pl/ directory |
| R3 contract | steady: contract: no exit code was recorded; pl: contract: no exit code was recorded |
| R4 outcome | steady: expected ADVANCE, observed nothing; pl: expected ADVANCE, observed nothing |
| R5 live control | pl: control: service_error_rate read null in the during window, not above its max 0.001 |
| R6 identity | steady: identity: the cell said server_version null, and the gate SERVER_TAG is 1.31.0; steady: identity: steady/g20261014-212526-steady.verdict.json records no thresholds_release; pl: identity: server_tag null, and the gate SERVER_TAG is 1.31.0; pl: identity: pl/g20261014-212526-pl.json records no thresholds_release |

Host quiet: no. poll.jsonl is missing, so the host was not seen quiet.

Class: refused.

**BLOCK**
