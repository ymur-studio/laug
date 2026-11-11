# Runbook 4: a gate

`laug gate run <tag>` judges a server release by every item of a plan at once, on fresh cells of this host. The items
are the release under a steady load, the release under a planted fault, and the release it replaces, called the
**incumbent**, under the same load. In an **A/A** gate the release and the incumbent are the same release; in an
**A/B** gate they differ. The gate returns ADVANCE or BLOCK, with the evidence in `gate/results/<gate id>/`. The plan
is `gate/plan.yaml`.

- **You need**: nothing for the offline blocks. The last block needs the lab host.
- **It takes**: in 4 counted A/A gates on the lab host, the verdict was handed on 612.5 to 614.3 s after the start,
  about 10 minutes ([CAPABILITIES](../CAPABILITIES.md), its Gate row), and the command exited 674.0 to 678.2 s after
  it began, about 11 minutes (`cells/results/2026-10-24-boot4.txt`, G1, G2, G4 and G5).
- **If the verdict is BLOCK**: `bin/laug gate run` exits 1.

## Offline: the set, a gate on fakes, a committed BLOCK

The threshold set a 1.32.1 run is judged by. It is `load/steady-state.yaml`, measured on the lab host on 1.31.0, with
the two overrides in `load/releases/1.32.yaml`, plus the tolerances rule R7 uses to compare the release with the
incumbent.

```sh
bin/laug thresholds -server-tag 1.32.1
```

```text
laug · thresholds: load/steady-state.yaml, the base (1.31.0 baselines), with load/releases/1.32.yaml for ...
metric                                         max  no data reads
workflow_task_schedule_to_start_p99_ms          38  EXCEEDS
activity_schedule_to_start_p99_ms               20  not judged: not produced on this release (load/releases/1.32.yaml)
...
compare           judging: tolerances from 20 A/A gates of 1.31.0, cells/results/2026-10-17-lab-arm-calibration.txt
  workflow_task_schedule_to_start_p99_ms  ms, floor 0.1, tolerance 1.02
  persistence_latency_p99_ms  ms, floor 0.1, tolerance 0.35
...
```

A whole gate on fakes. Fakes stand in for Docker, the cells and Omes; the items, the rules R1 to R7 and the judge are
the real ones. The run stays in a directory under `TMPDIR` for you to read, and nothing lands in `gate/results`. If the
tree has uncommitted changes outside `gate/results`, where earlier runs live, the gate prints one extra line first to
say so, because no stamp tells whether bin/laug holds those changes.

```sh
bin/laug gate run 1.31.0 -fake
```

```text
...
laug · gate run 1.31.0 -fake: fakes stand in for Docker, the cells and Omes; the run in ...
laug · gate g...: server 1.31.0, plan gate/plan.yaml, results (outside the repository)/g..., GATE_FAKE=1: ...
...
laug · gate g..., server 1.31.0: ADVANCE (GATE_FAKE=1: fixtures, no cell ran)
R1 harness       pass
...
R7 incumbent     pass
host quiet       yes
...
VERDICT: ADVANCE (GATE_FAKE=1: fixtures, no cell ran)
...
ADVANCE
laug · gate g...: on fakes, its files are in .../laug-fake-gate-...
```

A committed BLOCK from the lab host: gate g20261018-092540, whose steady item read a persistence p99 0.401 ms over the
incumbent's, past R7's tolerance of 0.35 ms. [CAPABILITIES](../CAPABILITIES.md), its A/B row, gives the releases and
the caveat on this reading. [Runbook 5](5-rejudge.md) judges this run again.

```sh
bin/laug gate open g20261018-092540
grep -F WORSE gate/results/g20261018-092540/verdict.md
```

```text
laug · gate open g20261018-092540: server 1.32.1, class regression
directory   gate/results/g20261018-092540
verdict.md  gate/results/g20261018-092540/verdict.md
BLOCK
| persistence_latency_p99_ms | 8.460 ms | 8.059 ms | +0.401 | 0.35 | WORSE |
```

## On the lab host

This block needs the lab host, and it holds no expected lines. It held under `bin/laug runbook check -lab` in boot 6,
its A/A gate ADVANCE (`cells/results/2026-10-27-boot6.txt`:382-446).

A gate on cells holds this host to the instance type its threshold set was measured on, the m8i.8xlarge, and refuses
any other; `bin/laug host type` says why. The first line declares the type, once per host.

```sh needs=lab
bin/laug host type -declare m8i.8xlarge   # once a host: the type a gate holds it to
bin/laug upstream                         # Omes at f0de206
bin/laug deps -lab                        # Docker, Omes, 16 vCPU and 8 GiB free
bin/laug gate run 1.31.0                  # an A/A gate, the incumbent beside itself: ADVANCE, exit 0
```

The last line runs an A/A gate, 1.31.0 judged beside itself. It should end ADVANCE, with exit 0. Then:

1. Judge the release with `bin/laug gate run 1.32.1`, which exits 1 on a BLOCK. Gate g20261018-092540 above
   BLOCKed; [CAPABILITIES](../CAPABILITIES.md), its A/B row, counts the release's gates and gives the caveat.
2. Find a run's files with `bin/laug gate open <gate id>`, which names the run's directory and its verdict.md.
   [Runbook 5](5-rejudge.md) judges it again.

**As a workflow.** The same gate runs as workflow Gate, on a Temporal dev server: a gate driven from a workflow gives
the same verdict as `bin/laug gate run` without `-workflow` ([CAPABILITIES](../CAPABILITIES.md#the-durable-gate)).

1. Start the dev server with `bin/laug orchestrator server`.
2. Start a worker with `bin/laug orchestrator worker`.
3. Run the gate with `bin/laug gate run 1.32.1 -workflow`.
4. While it runs, `bin/laug gate status <gate id>` reads it.
