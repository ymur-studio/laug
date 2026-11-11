# gate/results

One directory per gate run, named by its gate id, as the gate wrote it: `gate.json`, `verdict.json` and
`verdict.md`, the plan and the thresholds it read, and a directory per item. Three runs are kept here whole, each on
the lab host, an m8i.8xlarge (32 vCPU, 128 GiB): the A/B BLOCK g20261018-092540, the A/B ADVANCE g20261018-091444 and
the A/A gate g20261018-075357. `docs/RECORDS.md` quotes the key lines of each, with the command that checks it, and
`bin/laug gate rejudge <gate id>` judges each again. The `verdict.md` heading, `laug · verdict for raun <commit>`, is
the judge's historical form: it calls a gate a raun. The judge writes it until its next version; the runs here keep it.

Every lab gate run, these three among them, is a row of `docs/tables/gates.jsonl`, 158 rows: its verdict, the rule
that decided it, and its readings in the tables beside it. `docs/tables/records.jsonl` names each run's files with
their sha256, and `docs/EVIDENCE.md` is written from the rows. The runs not kept here, and the dev host's 25 gate
runs, are kept outside this tree; the 7 dev-host runs the tests read are copied, as fixtures and not evidence, to
`internal/rejudge/testdata/runs/`.

## Which page cites each entry

An entry is a file or a run's directory (name/). Pages lists the public .md files outside the four results directories
that cite it; Other files counts the other public files there that do: code, tests, plans and figures. A file cites an
entry with its path, a glob or {a,b} that matches it, or its name alone of 8 characters or more with a digit (the rule:
`check/results_test.go`). `go test ./check`, in `make test-go`, holds the table to the tree and prints the one it wants.

| Entry | Pages | Other files |
| --- | --- | ---: |
| `g20261018-075357/` | `_report/testdata/tables/README.md`, `docs/EVIDENCE.md`, `docs/LEARNING.md`, `docs/RECORDS.md` | 23 |
| `g20261018-091444/` | `docs/CAPABILITIES.md`, `docs/EVIDENCE.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/5-rejudge.md` | 12 |
| `g20261018-092540/` | `README.md`, `_report/testdata/tables/README.md`, `docs/CAPABILITIES.md`, `docs/EVIDENCE.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/1-offline.md`, `docs/runbooks/4-gate.md`, `docs/runbooks/5-rejudge.md` | 24 |

## Recorded citations outside the tree

A result keeps its bytes, so some name dev-host results or gate runs, which are kept outside this tree. A row is
one file here, with the count of such names it holds and the names; the same test holds the rows.

| Result file | Count | Files outside the tree |
| --- | ---: | --- |
| `g20261018-091444/pl/g20261018-091444-pl.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
| `g20261018-091444/steady/g20261018-091444-steady.verdict.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
| `g20261018-091444/thresholds.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
| `g20261018-092540/pl/g20261018-092540-pl.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
| `g20261018-092540/steady/g20261018-092540-steady.verdict.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
| `g20261018-092540/thresholds.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
