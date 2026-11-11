# Runbook 1: try laug offline

Run laug end to end with no Docker and no cell. You start a pool of fake cells and lease one, print the thresholds a
release is judged by, run a whole gate on fakes and judge a committed BLOCK again, then run the test suites. Only Go's
modules are fetched.

A **cell**, Temporal's word, is one complete, throwaway Temporal server on one host, with its own Postgres. A **raun**
is laug's word for one trial: one fault, run through a before, a during and an after window on one cell. A **gate**
judges a server release beside the one it replaces, on fresh cells, and returns ADVANCE or BLOCK. The **lab host**,
an m8i.8xlarge (32 vCPU, 128 GiB), is the instance type laug's threshold set was measured on. A gate on cells refuses
any other type ([runbook 4](4-gate.md)).

- **You need**: Linux, bash, make, bc, git, setsid, flock, diffutils, ss, jq 1.7 or later (the committed runs were
  judged with 1.7.x), yq v4 (mikefarah/yq), and at least Go 1.26, curl 7.76 and GNU coreutils 8.31. Run everything
  from the checkout's root.
- **It takes**: about 6 minutes on a fresh clone on the lab host, about 5 of them `make test`
  (`cells/results/2026-10-24-boot4.txt`:168, :186).

The block below is the long walk. README.md's block, under its heading "Try it in five minutes, no Docker", is three
of these lines, word for word. `load/testdata/harness-test.sh` runs the first seven lines and holds each of README's
lines to one of this block's, in this order; `bin/laug runbook check docs/runbooks/1-offline.md` runs this block.

```sh
make build; bin/laug deps -offline      # the binary, then the offline path's tools checked
bin/laug pool serve & sleep 2           # a pool of fake cells on loopback port 7700
bin/laug lease; bin/laug pool status; bin/laug release -last
kill $!; wait $!                        # a gate starts its own pool on that port
bin/laug thresholds -server-tag 1.32.1  # the set a release is judged by, and why
bin/laug gate run 1.31.0 -fake          # a whole gate on fakes: ADVANCE, exit 0
bin/laug gate rejudge g20261018-092540  # a committed BLOCK, judged again, byte for byte
make test                               # unit, replay and harness suites, about 5 minutes on the lab host
```

**What each line shows.** `load/testdata/harness-test.sh` runs the first seven lines, in its section on this block, and
checks each against what follows. The eighth line, `make test`, runs that test and the suites beside it.

1. `make build` stamps bin/laug with HEAD's commit. `bin/laug deps -offline` runs the 12 checks the offline path needs
   and exits 0 when none reads MISSING. The 12 are bin/laug, built from HEAD, and every tool above but bash and make,
   with GNU coreutils checked through env, and diffutils through cmp and diff, one check. `bin/laug deps` alone runs
   15, 3 more for a cell and a gate, and still exits 0 without those. `make test` needs bc and ss: its harness suite
   counts lost work with bc and reads the ports its pools take with ss. Without either, that suite stops before its
   first check.
2. The pool's first line says it listens on 127.0.0.1:7700 with the fake backend.
3. The lease names a cell leased to laug and keeps it in `.last-lease.json`. `bin/laug pool status` lists it, and
   `bin/laug release -last` gives it back and removes the file.
4. `kill $!; wait $!` stops the pool on SIGTERM, with exit 0, within 2 s. That frees the port for the gate's own
   pool.
5. `bin/laug thresholds -server-tag 1.32.1` prints the threshold set a 1.32.1 run is judged by: `load/steady-state.yaml`
   with the two overrides in `load/releases/1.32.yaml`.
6. `bin/laug gate run 1.31.0 -fake` prints `VERDICT: ADVANCE (GATE_FAKE=1: fixtures, no cell ran)` and exits 0. Its
   run stays in a directory under `TMPDIR`, named `laug-fake-gate-*`, which its last line names, and nothing lands in
   `gate/results`.
7. `bin/laug gate rejudge g20261018-092540` judges a committed BLOCK again and prints `verdict.json  identical` and
   `verdict.md    identical`.
8. `make test` took 322.3 s on a fresh clone on the lab host (`cells/results/2026-10-24-boot4.txt`:168).

## The runbooks

One page per task. A block that needs only the tools above is offline, and `make test` runs the offline blocks of
runbooks 2 to 7 (the runbook section of `load/testdata/harness-test.sh`).

| Runbook | Task | A block needs |
| --- | --- | --- |
| 1, this page | try laug offline | nothing |
| [2](2-lease-and-load.md) | lease a cell, load it, judge the run | Docker, for the cell |
| [3](3-raun.md) | one raun: a fault through three windows | Docker, for the cell |
| [4](4-gate.md) | a gate: a release beside the one it replaces | the lab host, for a gate on cells |
| [5](5-rejudge.md) | judge committed runs again, compare two, write the docs | nothing |
| [6](6-add-a-fault.md) | add a fault to the catalog | Docker, for its rauns |
| [7](7-lab-batch.md) | a lab batch | the lab host |

## How a runbook is checked

`bin/laug runbook check <file>` runs a page's command blocks and checks each against what it prints.
`bin/laug runbook check` alone lists the runbooks and their blocks.

- **Command blocks** are fenced as `sh`. A `text` fence right after one holds the lines it prints, stdout and stderr
  together. In those lines, `...` inside a line matches any text, and a line of `...` alone matches any number of
  lines.
- **Each block runs** as `bash -e -o pipefail` from the checkout's root. It stops at its first failing command, and it
  passes nothing to the next block except files. Two kinds of failure do not stop it: one in a command started with `&`,
  such as `bin/laug pool serve &`, and one inside an `&&` list, unless it is the list's last command. So keep `&&` lists
  out of a block: give each command its own line, or put `;` between two.
- **Markers after `sh`**: `needs=cell` marks a block that needs Docker and cells on this host, and `needs=lab` one that
  needs the lab host. `-cell` and `-lab` run them; without those flags they are skipped. `exit=<n>` marks a block that
  ends with exit n.

**Where they have run.** All 10 marked blocks have held on the lab host. Boot 6 ran runbooks 7 and 4 under `-lab`: 2
of 2 and 4 of 4 blocks held, of which 1 and 1 need the lab host, runbook 4's gate ADVANCE
(`cells/results/2026-10-27-boot6.txt`:94-118, :382-446). Both `needs=lab` blocks read the same now; two of runbook
4's offline blocks have new expected lines since, and `make test` holds them. Boot 8 ran runbooks 2, 3 and 6 under
`-cell` as they stand: 7 of 7, 5 of 5 and 7 of 7 blocks held, of which 4, 2 and 2 need a cell, every verdict ADVANCE
(`docs/tables/quotes.jsonl#cells/results/2026-10-29-boot8.txt:125-133` and :135-170). This page's block, which needs
no marker, held 1 of 1 under `bin/laug runbook check` on the lab host in boot 6
(`cells/results/2026-10-27-boot6.txt`:119-138), and reads the same now. That record keeps the block's ok line and its
seconds, not what it printed. The lab blocks of runbooks 4 and 7 hold no expected lines.