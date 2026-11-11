# faults/results

One raun's JSON, `raun-<run id>.json`, as `laug raun` wrote it: the fault, its windows' times, the readings before,
during and after, and the result (`faults/README.md` names the keys). Twelve are kept here, on the lab host, an
m8i.8xlarge (32 vCPU, 128 GiB): `raun-lab6-kill-history-{1,2,3}`, `raun-lab2-persistence-errors-{1,2,3,t010,t030,t050}`
and `raun-lab2-persistence-latency-{1,2,3}`. `docs/RECORDS.md` quotes the key lines of each, with the command that
checks it.

Every raun of the records, these twelve among them, is a row of `docs/tables/rauns.jsonl`, and so is each candidate: one
dynamic config change judged by `laug candidate`, whose two loads are rows of `docs/tables/loads.jsonl`.
`docs/tables/records.jsonl` names each record's files with their sha256. The rauns and candidates not kept here, and the
dev host's, are kept outside this tree.

## Which page cites each entry

An entry is a file or a run's directory (name/). Pages lists the public .md files outside the four results directories
that cite it; Other files counts the other public files there that do: code, tests, plans and figures. A file cites an
entry with its path, a glob or {a,b} that matches it, or its name alone of 8 characters or more with a digit (the rule:
`check/results_test.go`). `go test ./check`, in `make test-go`, holds the table to the tree and prints the one it wants.

| Entry | Pages | Other files |
| --- | --- | ---: |
| `raun-lab2-persistence-errors-1.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/6-add-a-fault.md`, `faults/README.md` | 8 |
| `raun-lab2-persistence-errors-2.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/6-add-a-fault.md`, `faults/README.md` | 7 |
| `raun-lab2-persistence-errors-3.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/6-add-a-fault.md`, `faults/README.md` | 7 |
| `raun-lab2-persistence-errors-t010.json` | `docs/RECORDS.md`, `faults/README.md` | 7 |
| `raun-lab2-persistence-errors-t030.json` | `docs/RECORDS.md`, `faults/README.md` | 7 |
| `raun-lab2-persistence-errors-t050.json` | `docs/RECORDS.md`, `faults/README.md` | 7 |
| `raun-lab2-persistence-latency-1.json` | `docs/CAPABILITIES.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/3-raun.md`, `docs/runbooks/6-add-a-fault.md`, `faults/README.md` | 12 |
| `raun-lab2-persistence-latency-2.json` | `docs/CAPABILITIES.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/3-raun.md`, `faults/README.md` | 8 |
| `raun-lab2-persistence-latency-3.json` | `docs/CAPABILITIES.md`, `docs/LEARNING.md`, `docs/RECORDS.md`, `docs/runbooks/3-raun.md`, `faults/README.md` | 8 |
| `raun-lab6-kill-history-1.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `faults/README.md` | 10 |
| `raun-lab6-kill-history-2.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `faults/README.md` | 9 |
| `raun-lab6-kill-history-3.json` | `docs/LEARNING.md`, `docs/RECORDS.md`, `faults/README.md` | 8 |

## Recorded citations outside the tree

A result keeps its bytes, so some name dev-host results or gate runs, which are kept outside this tree. A row is
one file here, with the count of such names it holds and the names; the same test holds the rows.

| Result file | Count | Files outside the tree |
| --- | ---: | --- |
