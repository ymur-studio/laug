# Evidence

This page is an appendix, for checking a gate run that a page names by its id.

Every gate run, a row a run by gate id, drawn from docs/tables/: gates.jsonl's row for the host, the judge version
the run's gate.json names, the verdict and the rule that decided it; rejudge.jsonl's rows for the last line.
`make docs` writes this page and the tables with the renderer under _report,
`go -C _report run ./cmd/report -docs`, which adds rows and never changes or removes one, and a test in
`make test-go` fails when it differs from what the renderer writes now.

158 gate runs: 104 ADVANCE, 54 BLOCK.

| Gate run | Host | Judge | Verdict | Deciding rule |
| --- | --- | --- | --- | --- |
| g20261015-065429 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-070530 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-071518 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-072520 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-073517 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-074521 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-135406 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-140232 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-141055 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-141922 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-142759 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-143633 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-144517 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-145418 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-150315 | 32 vCPU, type not recorded | v1 | BLOCK | R2 |
| g20261015-151214 | 32 vCPU, type not recorded | v1 | BLOCK | R5 |
| g20261015-152138 | 32 vCPU, type not recorded | v1 | BLOCK | R5 |
| g20261015-214524 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-215507 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-220501 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-221451 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-222438 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261015-223429 | 32 vCPU, type not recorded | v1 | BLOCK | R4 |
| g20261015-224343 | 32 vCPU, type not recorded | v1 | BLOCK | R4 |
| g20261015-225308 | 32 vCPU, type not recorded | v1 | BLOCK | R4 |
| g20261015-230231 | 32 vCPU, type not recorded | v1 | BLOCK | R1 |
| g20261015-230856 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-024601 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-091701 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-203845 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-204845 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-205837 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-210833 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-211828 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-212825 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-213821 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-214818 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261016-215819 | 32 vCPU, type not recorded | v1 | BLOCK | R4 |
| g20261016-220739 | 32 vCPU, type not recorded | v1 | BLOCK | R4 |
| g20261016-221717 | 32 vCPU, type not recorded | v1 | BLOCK | R1 |
| g20261017-011151 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-012148 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-013145 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-014142 | 32 vCPU, type not recorded | v1 | BLOCK | stopped at wave |
| g20261017-014632 | 32 vCPU, type not recorded | v1 | BLOCK | stopped at wave |
| g20261017-020417 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-060633 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-061717 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-062801 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-063843 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-064925 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-070006 | 32 vCPU, type not recorded | v1 | ADVANCE |  |
| g20261017-171604 | other (32 vCPU) | v2 | BLOCK | refused: this host's declared type is not m8i.8xlarge, the type load/steady-state.yaml was measured on: its numbers judge no other host type (docs/DESIGN.md section 7.1) |
| g20261017-171614 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261017-172556 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-174353 | other (32 vCPU) | v2 | BLOCK | refused: this host's declared type is not m8i.8xlarge, the type load/steady-state.yaml was measured on: its numbers judge no other host type (docs/DESIGN.md section 7.1) |
| g20261017-174403 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-175458 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-180552 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-181635 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-182733 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-183826 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-184922 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-190015 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-191109 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-192202 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-193258 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-194350 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-195440 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-200534 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-201631 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-202724 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-203815 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-204906 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-205956 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-211051 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-212148 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261017-213812 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261017-215534 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261017-221200 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261017-222254 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-223357 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261017-224422 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261017-225512 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R1 |
| g20261018-062701 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-063803 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R7 |
| g20261018-064905 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-065958 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R7 |
| g20261018-071048 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-072142 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261018-073238 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-074337 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261018-075357 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-080445 | m8i.8xlarge (32 vCPU) | v2-r7off | BLOCK | R1 |
| g20261018-080919 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-082010 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-083103 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-084154 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-085245 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-090337 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-091444 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-092540 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R7 |
| g20261018-155946 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R4 |
| g20261018-160941 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-162821 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-163914 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R7 |
| g20261018-165015 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-170110 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R7 |
| g20261018-171208 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-172301 | m8i.8xlarge (32 vCPU) | v2 | BLOCK | R7 |
| g20261018-173348 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-174432 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-175523 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-180618 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-181716 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-182815 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-183909 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-185000 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-190056 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-191151 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-192249 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-193339 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-194431 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-195529 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-200626 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-201718 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-202807 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-225322 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-230413 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-231805 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-232958 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-234352 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261018-235556 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261019-000954 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261019-002158 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261019-003555 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261019-005744 | m8i.8xlarge (32 vCPU) | v2 | ADVANCE |  |
| g20261024-085719 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R4 |
| g20261024-090728 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R4 |
| g20261024-091739 | m8i.8xlarge (32 vCPU) | v3 | ADVANCE |  |
| g20261024-092853 | m8i.8xlarge (32 vCPU) | v3 | ADVANCE |  |
| g20261024-094016 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R4 |
| g20261024-095106 | m8i.8xlarge (32 vCPU) | v3 | ADVANCE |  |
| g20261024-100229 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | refused: lease incumbent: laugd refused it (curl exit 22): cell 35c802a7 not healthy before deadline: context deadline exceeded |
| g20261024-100751 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | refused: lease incumbent: laugd refused it (curl exit 22): cell 7abfaa52 not healthy before deadline: context deadline exceeded |
| g20261024-104246 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R2 |
| g20261024-105215 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R2 |
| g20261024-110203 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R2 |
| g20261024-111210 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R2 |
| g20261024-112242 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R2 |
| g20261024-113325 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R2 |
| g20261024-114403 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R2 |
| g20261024-115510 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R2 |
| g20261024-120609 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R4 |
| g20261024-121800 | m8i.8xlarge (32 vCPU) | v3 | BLOCK | R4 |
| g20261024-122949 | m8i.8xlarge (32 vCPU) | v3 | ADVANCE |  |
| g20261027-054034 | m8i.8xlarge (32 vCPU) | v3 | ADVANCE |  |
| g20261027-055212 | m8i.8xlarge (32 vCPU) | v3 | ADVANCE |  |

Each run is judged again in Go, under the judge version its gate.json records, as `bin/laug gate rejudge -all`
judges it. The judge's identity, as `bin/laug judge -identity` prints it: version v3 for a new run, sources'
sha256 0cf257e99e7ef1ce3614fcf36f56a0a957b58b0871ac7acc1b33d003dd644147. The sources are internal/judge/versions.txt
and the .go files in internal/judge and internal/jsonx that are not tests. A run judged in Go records both in
gate.json, and `bin/laug gate rejudge` picks its judge by the version. The 158 are the lab tree's, kept outside
this one; a clone holds the 3 runs kept in `gate/results/` (`gate/results/README.md`).

```text
laug · rejudge, 158 runs: 156 identical, 0 known, 2 re-rendered, 0 unexplained, 0 not judged
```
