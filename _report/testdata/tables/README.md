# Gate runs, cut to what the tables read

Nine gate runs of gate/results, each cut to the files report -tables reads: gate.json, verdict.json, plan.json,
thresholds.json, thresholds-incumbent.json and harness.json where the run has them, each item's verdict JSON, each
fault item's raun JSON, and known-slow.json. Byte for byte, 64 files beside this README. No run here holds
poll.jsonl, verdict.md or timeline.jsonl, so a row's files.poll is false and each run's rejudge is "not judged". They
are fixtures, not evidence: _report's TestTablesColumns and TestTablesDeterministic read them for their
shapes. No committed run is fake or a rerun: the test plants one from g20261015-065429.

| Run | Shape |
| --- | --- |
| g20261018-092540 | the A/B BLOCK: R7, a WORSE compare row, an item of arm incumbent read against thresholds-incumbent.json |
| g20261018-075357 | the A/A ADVANCE: 1.31.0 beside itself |
| g20261015-065429 | an ADVANCE before the arm: no comparison, no incumbent, no driver in gate.json |
| g20261015-223429 | BLOCK at R4: the fault item's raun never steady |
| g20261017-212148 | BLOCK with known-slow.json in steady/ |
| g20261017-014142 | a T2 trap verdict: stopped at wave, rules null |
| g20261024-100229 | refused at the incumbent's lease; gate.json records internal/judge v3 and no gate/combine.sh |
| g20261024-104246 | class control, decided at R2, comparison null |
| g20261015-230231 | class harness: the fault item's raun JSON is not there |
