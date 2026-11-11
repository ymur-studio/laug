# cells/results

The transcripts of laug's runs, `<date>-<label>.txt`: the host, the tree and the times in UTC first, then each step's
command and what it printed. Eight are kept here, each the transcript alone, and `docs/RECORDS.md` quotes the key
lines of each, with the command that checks it. Each header names its host. Seven ran on the lab host, an m8i.8xlarge
(32 vCPU, 128 GiB): boots 1, 2, 4, 6 and 9, batch E, and the one-frontend run. The eighth, the judge against the shell
judges, ran on the dev host.

A run's raw files (`steps/`, `extra/`, `readings.json`) and the transcripts not kept here are kept outside the public
tree. What the pages read of them are rows: `docs/tables/records.jsonl` names each record's files with their sha256,
`docs/tables/readings-<batch>.jsonl` holds 13 batches' readings, and `docs/tables/quotes.jsonl` each line a page cites
by line.

## Which page cites each entry

An entry is a file or a run's directory (name/). Pages lists the public .md files outside the four results directories
that cite it; Other files counts the other public files there that do: code, tests, plans and figures. A file cites an
entry with its path, a glob or {a,b} that matches it, or its name alone of 8 characters or more with a digit (the rule:
`check/results_test.go`). `go test ./check`, in `make test-go`, holds the table to the tree and prints the one it wants.

| Entry | Pages | Other files |
| --- | --- | ---: |
| `2026-10-23-boot1.txt` | `SECURITY.md`, `docs/CAPABILITIES.md`, `docs/DESIGN.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/img/README.md`, `docs/runbooks/2-lease-and-load.md`, `docs/runbooks/3-raun.md`, `docs/runbooks/6-add-a-fault.md`, `docs/runbooks/7-lab-batch.md`, `faults/README.md`, `internal/health/README.md`, `internal/pool/README.md`, `load/README.md`, `load/baseline.md` | 31 |
| `2026-10-23-boot2.txt` | `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/3-raun.md`, `docs/runbooks/6-add-a-fault.md`, `docs/runbooks/7-lab-batch.md`, `faults/README.md` | 10 |
| `2026-10-24-batchE.txt` | `docs/CAPABILITIES.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/3-raun.md`, `docs/runbooks/7-lab-batch.md`, `faults/README.md` | 8 |
| `2026-10-24-boot4.txt` | `README.md`, `docs/BRAND.md`, `docs/CAPABILITIES.md`, `docs/DESIGN.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/1-offline.md`, `docs/runbooks/4-gate.md`, `docs/runbooks/6-add-a-fault.md`, `docs/runbooks/7-lab-batch.md`, `docs/tables/README.md` | 18 |
| `2026-10-27-boot6.txt` | `README.md`, `docs/CAPABILITIES.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/img/README.md`, `docs/runbooks/1-offline.md`, `docs/runbooks/2-lease-and-load.md`, `docs/runbooks/3-raun.md`, `docs/runbooks/4-gate.md`, `docs/runbooks/6-add-a-fault.md`, `docs/runbooks/7-lab-batch.md`, `faults/README.md` | 14 |
| `2026-10-29-boot9.txt` | `SECURITY.md`, `docs/CAPABILITIES.md`, `docs/DESIGN.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `faults/README.md` | 10 |
| `2026-11-02-frontend1.txt` | `docs/LEARNING.md`, `docs/RECORDS.md` | 4 |
| `2026-11-04-judge-shell-proof.txt` | `docs/CAPABILITIES.md`, `docs/RECORDS.md` | 3 |

## Recorded citations outside the tree

A result keeps its bytes, so some name dev-host results or gate runs, which are kept outside this tree. A row is
one file here, with the count of such names it holds and the names; the same test holds the rows.

| Result file | Count | Files outside the tree |
| --- | ---: | --- |
| `2026-10-23-boot1.txt` | 3 | cells/results/2026-10-13-dev-readiness.txt, cells/results/2026-10-14-server-1.32.1.txt, cells/results/2026-10-14-serverfi-smoke.txt |
| `2026-10-23-boot2.txt` | 2 | cells/results/2026-10-14-controls-and-shards.txt, cells/results/2026-10-14-dev-faults.txt |
| `2026-10-24-boot4.txt` | 3 | cells/results/2026-10-14-gate-runs.txt, cells/results/2026-10-14-server-1.32.1.txt, g20261014-212013 |
