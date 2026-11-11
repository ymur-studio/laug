# Lab host gate runs, kept as fixtures

Twelve gate runs from the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), each on server 1.31.0, that the tests read by
name and that are not kept in gate/results: their records leave the tree for the archive, and their rows stay in
docs/tables. Each directory is `gate/results/<id>` as a clone held it, byte for byte: TestLabRuns holds each to its
docs/tables/records.jsonl row, its file count and the sha256 of its files as sha256sum lists them under
`gate/results/<id>/`. Their gate.json and exit.json still name gate/results, where the gate wrote them. They are
fixtures, not evidence: the tests read them for their shapes, and the pages cite the rows.

| Run | Shape | Named by |
| --- | --- | --- |
| g20261015-223429 | a judge before the arm: R1 to R6, no driver recorded, BLOCK on R4 (v1) | _report's TestReadRunShapes |
| g20261017-014142 | a T2 trap: the workflow canceled in the wave (step.sh, v1), re-rendered | testdata/expected.txt; internal/judge trap_test.go; s1_test.go; cmd/laug gate_test.go and judge_test.go; internal/report |
| g20261017-014632 | a T2 trap: signal TERM in the wave (run.sh, v1), re-rendered | testdata/expected.txt; internal/judge trap_test.go; cmd/laug gate_test.go |
| g20261017-171604 | refused at preflight: another host type declared (v2, recording) | internal/judge render_test.go and summary_test.go; _report; _obs/cmd/obs obs_test.go |
| g20261017-172556 | a v2 ADVANCE that records its comparison | the recording base of internal/judge corpus_test.go |
| g20261017-174403 | the first calibration A/A gate, ADVANCE (v2, recording) | internal/tolerance's made gates; cmd/laug tolerance_test.go |
| g20261017-191109 | a v2 ADVANCE whose verdict.md prints a -0 d in a share | internal/judge v3_test.go |
| g20261017-192202 | a v2 ADVANCE whose verdict.md prints a -0 d as 0.0-0 (step.sh) | internal/judge v3_test.go; cmd/laug judge_test.go |
| g20261018-064905 | a validation gate by step.sh, ADVANCE (v2, judging) | no test now; kept with the set |
| g20261018-074337 | the gate5-known-bad control: BLOCK, class regression, with a note (v2) | internal/judge render_test.go and summary_test.go |
| g20261018-080445 | the arm-always-pass control: BLOCK on R1 under its branch's judge, v2-r7off, no poll.jsonl | _report's TestReadRunShapes |
| g20261024-085719 | judged by bin/laug judge in boot 4: an internal/judge entry, v3 | cmd/laug judge_test.go |
