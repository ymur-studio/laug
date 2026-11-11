# load/results

One load run's files, named by its run id: `<id>.json` is the run as `laug load` wrote it (the load's counts and the
server's metrics over the window), `<id>.baseline.json` the thresholds `laug baseline` read over the run's whole
window, and `<id>.verdict.json` what `laug verdict` judged, ADVANCE or BLOCK. Six runs are kept here, three files
each: boot 1's release loads on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), `lab1-v1310-{1,2,3}` on 1.31.0 and
`lab1-v1321-{1,2,3}` on 1.32.1 (`docs/CAPABILITIES.md`, Loads). `docs/RECORDS.md` quotes the key lines of each,
with the command that checks it.

Every load run of the records, these six among them, is a row of `docs/tables/loads.jsonl`, with the readings that
decided it in `docs/tables/load-readings.jsonl`; `docs/tables/records.jsonl` names each run's files with their sha256.
The runs not kept here, and the dev host's, are kept outside this tree.

## Which page cites each entry

An entry is a file or a run's directory (name/). Pages lists the public .md files outside the four results directories
that cite it; Other files counts the other public files there that do: code, tests, plans and figures. A file cites an
entry with its path, a glob or {a,b} that matches it, or its name alone of 8 characters or more with a digit (the rule:
`check/results_test.go`). `go test ./check`, in `make test-go`, holds the table to the tree and prints the one it wants.

| Entry | Pages | Other files |
| --- | --- | ---: |
| `lab1-v1310-1.baseline.json` | `docs/CAPABILITIES.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/2-lease-and-load.md`, `load/README.md` | 7 |
| `lab1-v1310-1.json` | `docs/CAPABILITIES.md`, `docs/RECORDS.md`, `docs/runbooks/2-lease-and-load.md` | 8 |
| `lab1-v1310-1.verdict.json` | `docs/CAPABILITIES.md`, `docs/RECORDS.md`, `docs/runbooks/2-lease-and-load.md`, `load/README.md`, `load/baseline.md` | 8 |
| `lab1-v1310-2.baseline.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `load/README.md` | 6 |
| `lab1-v1310-2.json` | `docs/RECORDS.md` | 6 |
| `lab1-v1310-2.verdict.json` | `docs/RECORDS.md`, `load/README.md`, `load/baseline.md` | 6 |
| `lab1-v1310-3.baseline.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `load/README.md` | 6 |
| `lab1-v1310-3.json` | `docs/RECORDS.md` | 6 |
| `lab1-v1310-3.verdict.json` | `docs/RECORDS.md`, `load/README.md`, `load/baseline.md` | 6 |
| `lab1-v1321-1.baseline.json` | `docs/CAPABILITIES.md`, `docs/LEARNING.md`, `docs/RECORDS.md` | 8 |
| `lab1-v1321-1.json` | `docs/CAPABILITIES.md`, `docs/RECORDS.md` | 8 |
| `lab1-v1321-1.verdict.json` | `docs/CAPABILITIES.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `load/README.md`, `load/baseline.md` | 8 |
| `lab1-v1321-2.baseline.json` | `docs/LEARNING.md`, `docs/RECORDS.md` | 6 |
| `lab1-v1321-2.json` | `docs/RECORDS.md` | 6 |
| `lab1-v1321-2.verdict.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `load/README.md`, `load/baseline.md` | 6 |
| `lab1-v1321-3.baseline.json` | `docs/LEARNING.md`, `docs/RECORDS.md` | 6 |
| `lab1-v1321-3.json` | `docs/RECORDS.md` | 6 |
| `lab1-v1321-3.verdict.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `load/README.md`, `load/baseline.md` | 6 |

## Recorded citations outside the tree

A result keeps its bytes, so some name dev-host results or gate runs, which are kept outside this tree. A row is
one file here, with the count of such names it holds and the names; the same test holds the rows.

| Result file | Count | Files outside the tree |
| --- | ---: | --- |
| `lab1-v1321-1.verdict.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
| `lab1-v1321-2.verdict.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
| `lab1-v1321-3.verdict.json` | 1 | cells/results/2026-10-14-dev-1.32.1.txt |
