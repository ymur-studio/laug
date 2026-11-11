# Dev host gate runs, kept as fixtures

Seven gate runs from the dev host, a Linux VM with 24 vCPU and 47 GiB running Docker, each on server 1.31.0 and each
BLOCK. Each directory is `gate/results/<id>` as a clone held it, byte for byte: 82 files beside this README, and no
empty item directory, since git carries none. Their gate.json and exit.json still name gate/results, where the gate
wrote them. They are fixtures, not evidence: the tests read them for their shapes.

| Run | Shape | Named by |
| --- | --- | --- |
| g20261014-184524 | class harness: make harness-test timed out, exit 124 at 300 s | comments in gate/lib.sh and load/testdata/harness-test.sh |
| g20261014-212013, -212526 | class refused at the lease of steady, whose directory was left empty | cmd/laug/gate_test.go; comments in gate/lib.sh and harness-test.sh |
| g20261014-213037, -214041 | T1 trap verdicts, class invalid | internal/judge/trap_test.go, internal/rejudge/s1_test.go, cmd/laug/gate_test.go |
| g20261014-215905 | the v1 run: class regression, a load item and a fault item | the v1 base of internal/judge/corpus_test.go and both shell corpora; internal/rejudge s1_test.go and s2_test.go; cmd/laug judge_test.go and gate_test.go; internal/jsonx/number_test.go row 19 |
| g20261014-220820 | class regression, an integer reading in a reason | internal/jsonx/number_test.go row 20 |
