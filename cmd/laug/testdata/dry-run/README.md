# make -n goldens

What make printed for the daily path's, the pool's, the gate's, the host's, the cell's, the HealthCheck workflow's and
the gate workflow's targets before laug had subcommands. Those targets have left make. The goldens are laug's expected
`-dry-run` lines, refusals and requests, with make's text kept as the record. The stack's goldens, `obs-*.txt`, are in
`_obs/cmd/obs/testdata/dry-run`, held to bin/obs.
`cmd/laug/dryrun_test.go` reads every `.txt` file here. Each is one case: a header of `# key: value` lines, then make's
output as it printed it, stdout and stderr together.

| Header | Says |
| --- | --- |
| `make` | the command the output came from: `make --no-print-directory -n <target> [NAME=value ...]` |
| `laug` | the laug command that stands for it |
| `lease` | `present`: `.last-lease.json` held `lease.json`, cell c0ffee01 on port block 1, server 1.32.1; `absent`: no file |
| `docker` | `<project> <image>`: `docker ps` found that compose project's temporal-history container running that image; without it, no container |
| `file` | a file the checkout held: its path, then its content; one whose content starts `#!` is executable |
| `passes` | the make variables on the command line that reached the script only through make's environment, which `make -n` does not print; in internal/script's table order |
| `adds` | the knobs laug sets that make's recipe did not, which laug's line writes after `passes`, in the same order: `laug verdict` keeps its JSON beside the run's, `VERDICT_JSON=load/results/<run-id>.verdict.json`, where make verdict kept none |
| `compare` | how laug is held to it, below |
| `moved` | `<from> <to>`: the directory make's recipe ran in, `cd <from> &&`, and the one the package is in now: laug's line has `cd <to> &&` there |
| `now` | laug's `-dry-run` line where it no longer matches make's, as `-update` wrote it: laug is held to this line instead |

| `compare` | laug passes when |
| --- | --- |
| `line <script> [<argument>]` | `laug ... -dry-run` prints the command that runs the script, as make printed it, `passes` and `adds` in front, one blank between words; with an argument, the one command whose first argument it is; a recipe's `cd <dir> &&` before it stays in front |
| `resolved <script> $name=value ...` | the same, with the value the recipe's shell resolved at run time in place of each `"$name"`, or `$name` where the recipe left it bare; a name in double quotes, `"${srv:-0}"=0`, is replaced as written |
| `whole <word>` | laug's line is the one line of make's output that holds the word, a backslash and a newline joined, as replay-test's pipe into grep; nothing `passes` |
| `contains <script>` | the `-dry-run` line holds the script's call as make printed it, `passes` in front |
| `request <method> <path>` | lease, reset and release run in-process: the request laug sends is the one make's curl sent (the URL, the body jq built), and `.last-lease.json` ends as the recipe left it |
| `refused` | make's recipe stops before it acts, with exit 2, and laug exits 2 having run and sent nothing |

In every mode, make's `ORCH_NOFLAGS`, `env $(compgen -e | sed ...)` before an orchestrator binary or go, is left out of
the line laug is held to: it unset `TEMPORAL_SDK_FLAG_*`, `TEMPORAL_DEBUG` and `OTEL_*`, and internal/script hands a
program none of them.
Every golden's verb is in laug's table, and a golden whose verb is not fails.
`laug gate rejudge`, `gate compare`, `gate signature` and `gate tolerance` run in Go. So do the verbs that run a
Temporal SDK role in laug's own process: health check, loop, record and timer, orchestrator worker, probe and record,
gate run -workflow, and gate status, cancel, terminate and stop. They take no `-dry-run`, so their `line` goldens are
gone. The refused goldens of gate rejudge and compare, health loop, orchestrator worker and record, gate run -workflow,
and gate status and terminate remain, run without `-dry-run`. TestHealth, TestOrchestrator and TestGateWorkflow hold
each role's flags and environment instead. fault, raun and candidate check a name against `faults/catalog.yaml`
or `faults/candidates.yaml` before they act, so the test copies both into each case's checkout; make -n never ran that
check.

## When laug's line changes

The goldens' make targets have left make, so no golden can be captured again. When a verb's `-dry-run` line changes on
purpose, run `go test ./cmd/laug -run TestDryRunGoldens -update`. It writes laug's line as the `now` header of each
golden whose line no longer matches make's. Where the line matches make's again, it drops the header. make's output
stays as it was captured, so the golden holds both lines: make's and laug's. With no line changed, `-update` leaves
every golden as it was. Refused and request goldens print no `-dry-run` line, so `-update` passes them by, and
TestGoldensClean fails a `now` header on one.

## How they were captured

The Makefile laug replaced, the one that ran every target itself, alone in a scratch directory outside the tree. make ran
under `env -i` with PATH, HOME (a scratch directory), `USER=laug`, `LOGNAME=laug`, `REQUESTER=laug` and `LANG=C`, so
no login name and no knob from the caller's shell reached it. PATH started with two stubs. `date` printed
`20261022-120000` for `date -u +%Y%m%d-%H%M%S`, so RUN_ID's default is fixed, and the test runs laug at 2026-10-22
12:00:00 UTC. `docker` listed no container unless the case's `docker` header named one; the test puts the same stub
first on laug's PATH. The checkout's path would be written `$ROOT`; make printed it in none of these. The test fails a
golden that holds an absolute path or the login name of whoever runs it.

The gate's and the host's goldens (`gate*.txt`, `host-*.txt`) were captured later in the same way. make gate built
pool/laugd and health/healthcheck first, so the scratch directory held both, newer than empty files at their sources'
paths, and `make -n` printed no build before `gate/run.sh`; laug builds nothing, and `laug gate run` refuses when
`bin/laug` is missing instead. A compose cell's health verb runs `bin/laug health check` now, and a gate on cells
refuses a `bin/laug` not built from HEAD. host-type has no golden: its recipe ran its body inline, so make's output
held no one line for laug's to match. That body is `gate/host-type.sh` now, and the harness tests `laug host type`.

The cell's goldens (`cell-*.txt`) were captured the same way from the Makefile of the commit that added `laug cell`.
Its cell-up, cell-down, cell-mem, cell-health and dynconfig-check recipes run `cells/up.sh`, `down.sh`, `mem.sh`,
`health.sh` and `dynconfig-check.sh`, the recipe bodies of the Makefile laug replaced, moved there with each make
variable set on the script's line. The Makefile laug replaced ran those bodies inline, so its output held no one line
for laug's to match. Its fi-template-check and softasserts recipes stayed as they were, and so did their output.

The HealthCheck and gate workflows' goldens (`health-*.txt`, `orchestrator-*.txt`, `gate-workflow*.txt`,
`gate-status*.txt` and `gate-terminate*.txt`) were captured the same way from the Makefile laug replaced. laug health,
laug orchestrator and gate run -workflow, status, cancel, terminate and stop replace its health-loop, health-record,
timer-probe, replay-test and orchestrator-* recipes. The scratch directory held pool/laugd, health/healthcheck and
health/timerprobe, newer than empty files at their sources' paths, so `make -n` printed no build of them;
`orchestrator/bin/*` were phony targets, so their builds print. laug builds none of them: it runs those roles in its
own process. `-repo` is make's directory, written `$ROOT`. orchestrator-test had a golden until make test-go took its
tests, and laug orchestrator test went. Since the SDK's roles run in laug's own process, the line goldens of the verbs
that run them are gone; replay-test's go test runs in internal/health, where the package moved from health/, so its two
line goldens carry `moved: health internal/health`.
One word was changed after capture: `orchestrator-record-badname.txt`'s last line names its deny list
`<the deny list>`, where make printed a path of this checkout; no test reads that line.

The pool's goldens (`pool-down.txt`, `pool-bench*.txt`) were captured the same way from the Makefile of the
commit that added `laug pool down` and `laug pool bench`, before that commit cut make. Its pool-down recipe ran
`cells/pool-down.sh`. The Makefile laug replaced ran that body inline, so its output held no one line for laug's to
match. Its pool-gate2 recipe ran `cells/pool-bench.sh`, which was pool/gate2.sh.
