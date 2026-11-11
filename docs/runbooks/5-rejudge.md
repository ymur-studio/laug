# Runbook 5: judge committed runs again

A gate's verdict is a function of its committed records, so any committed run can be judged again. `laug gate rejudge`
runs the judge version that a run's `gate.json` records, on the run as it stands, and compares the `verdict.json` and
`verdict.md` it makes with the committed ones, byte for byte. It writes nothing.

- **You need**: nothing. Every block here is offline.

**One run.** Gate g20261018-092540's BLOCK from [runbook 4](4-gate.md), which judge v2 judged when it ran.
[CAPABILITIES](../CAPABILITIES.md), its A/B row, gives the releases and the caveat on this reading.

```sh
bin/laug gate rejudge g20261018-092540
```

```text
laug · gate rejudge g20261018-092540: the run in gate/results/g20261018-092540, judged again in Go; nothing is written
verdict.json  identical
verdict.md    identical
judge         v2: gate.json records gate/combine.sh 33b4e89dc2ec; laug's judge sources ...
```

A run from before `bin/laug judge` records its judge as the sha256 of the shell judge it ran, and
`internal/judge/versions.txt` names the version of each sha256. The shell judges this tree began with were deleted
after the Go judge matched them on every committed run.

**Every run.** Every run in `gate/results` gets a line, and then the counts. A run whose files differ from the
committed ones is one of three kinds:

- **known**: the files differ for a reason the rejudge proves.
- **re-rendered**: a trap verdict, which the gate wrote when it stopped before its judge.
- **unexplained**: any other difference. Any unexplained run makes the command exit 1.

`internal/rejudge/committed_test.go` pins the counts.

```sh
bin/laug gate rejudge -all
```

```text
laug · gate rejudge -all: every run in gate/results, judged again in Go under the judge version its gate.json ...
...
g20261018-092540  identical
...
laug · rejudge, ... runs: ... 0 unexplained, 0 not judged
```

**Under today's judge.** The same run under the judge laug holds now, v3, which writes nothing without `-write`. It
gives the same BLOCK, by R7, and exits 1.

```sh exit=1
bin/laug judge g20261018-092540
```

```text
laug · judge: judged under v3, the current judge; gate.json records v2, and -recorded judges under v2
laug · gate g20261018-092540, server 1.32.1: BLOCK, class regression
...
R7 incumbent     steady: worse than the incumbent: persistence_latency_p99_ms 8.460 against 8.059 on incumbent, ...
host quiet       yes
not written      judge v3 in Go; -write refuses: gate.json records v2
BLOCK
```

**A run's signature, and two runs compared.** A run's signature is 16 fields. S2 is the same-verdict check: it
compares two runs' signatures field by field, and `laug gate compare` exits 1 when one differs. The orchestrator's
parity test runs the workflow `Gate` and `gate/run.sh` on the fake gate, case by case, and holds the two to one
signature with S2. The first block prints four of the 16.

```sh
bin/laug gate signature g20261018-092540 -json | jq -c '{verdict, class, judge, failed_rules}'
```

```text
{"verdict":"BLOCK","class":"regression","judge":"v2","failed_rules":["R7"]}
```

Then g20261018-092540's BLOCK against g20261018-091444, the gate run of the same release just before it, which
ADVANCEd. 5 of the 16 fields differ, so the command exits 1.

```sh exit=1
bin/laug gate compare g20261018-091444 g20261018-092540
```

```text
laug · S2, A g20261018-091444, B g20261018-092540: 5 of 16 fields differ
verdict: A "ADVANCE" B "BLOCK"
class: A null B "regression"
failed_rules: A [] B ["R7"]
...
```

**The docs.** `make docs` adds to `docs/tables` the rows the same records give and the tables lack, never changing or
removing one; after a judge change those are this judge's rejudge rows, one a run. It then writes `docs/EVIDENCE.md`,
drawn from those rows, and the figures in `docs/img`. `make test-go` fails a committed copy that differs from what it
writes now (_report's TestDocs).
