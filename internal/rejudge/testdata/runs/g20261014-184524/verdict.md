## laug · verdict for raun e4540a8 (tree dirty)

Gate run g20261014-184524 on server 1.31.0: 1 of 4 planned item(s) (ITEMS=1), 305.23 s from the gate start to this verdict on the wall clock (304.57 s monotonic).

| Item | Expect | Observed | Class | Reason |
| --- | --- | --- | --- | --- |
| s1 | ADVANCE | none | invalid | no exit record: s1/exit.json is missing |

| Rule | Result |
| --- | --- |
| R1 harness | harness: make harness-test exited 124 |
| R2 coverage | plan: cut to 1 of 4 items (ITEMS=1): 3 planned item(s) never ran; s1: no exit record: s1/exit.json is missing; s1: no JSON: s1/g20261014-184524-s1.json is missing; s1: no JSON: s1/g20261014-184524-s1.verdict.json is missing |
| R3 contract | s1: contract: no exit code was recorded |
| R4 outcome | s1: expected ADVANCE, observed nothing |
| R5 live control | the plan has no fault item, so no live control ran |
| R6 identity | s1: identity: the cell said server_version null, and the gate SERVER_TAG is 1.31.0; s1: identity: s1/g20261014-184524-s1.verdict.json records no thresholds_release |

Host quiet: no. poll.jsonl is missing, so the host was not seen quiet.

Class: harness.

**BLOCK**
