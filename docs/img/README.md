# Figures

The figures of docs/LEARNING.md's stops, a row each, by stop, then the figures other pages show, a row each, by page.
The renderer, _report, draws each SVG from the committed files its row names, on the light ground in docs/BRAND.md's
colors: a data figure by Go from the summary tables under docs/tables, which
`go -C _report run ./cmd/report -tables` cuts from the records; a diagram from its spec under docs/img/src,
with data drawn into it where the spec names a panel. Each SVG is labeled: its title and desc say what it shows and
how to read it, and the desc ends in every file it read, one by one. Two renders are the same bytes, and none reads a
row or a file that runs added later would change: a figure reads the rows it names, by key or by a record's prefix,
and `make docs` adds rows but refuses to change one.
`make docs` writes them and this page; a test in `make test-go` fails a committed copy that differs.

| Figure | Stop | Kind | Generator | Sources |
| --- | --- | --- | --- | --- |
| [healthcheck-path.svg](healthcheck-path.svg) | 1 | diagram | docs/img/src/healthcheck-path.txt, _report/diagram.go | cells/compose/cell.yml, docs/img/src/healthcheck-path.txt, docs/tables/readings-2026-10-23-boot1.jsonl, internal/health/health.go, internal/health/testdata/healthcheck_history.json |
| [replay-compared.svg](replay-compared.svg) | 2 | diagram | docs/img/src/replay-compared.txt, _report/diagram.go | docs/img/src/replay-compared.txt, internal/health/changes_test.go, internal/health/replay_test.go |
| [replay-swapped.svg](replay-swapped.svg) | 2 | diagram | docs/img/src/replay-swapped.txt, _report/diagram.go | docs/img/src/replay-swapped.txt, internal/health/replay_test.go, internal/health/testdata/healthcheck_history.json |
| [warm-check.svg](warm-check.svg) | 3 | diagram | docs/img/src/warm-check.txt, _report/diagram.go | cells/backend-compose.sh, cells/results/2026-10-23-boot1.txt, cells/shards-ready.sh, docs/img/src/warm-check.txt, docs/tables/quotes.jsonl, docs/tables/readings-2026-10-23-boot1.jsonl |
| [kill-history.svg](kill-history.svg) | 4 | data | _report/lessondata.go | docs/tables/fig-kill-history.jsonl, docs/tables/rauns.jsonl, docs/tables/src/hosts.tsv |
| [startup-race.svg](startup-race.svg) | 5 | diagram | docs/img/src/startup-race.txt, _report/diagram.go | cells/compose/cell.yml, docs/img/src/startup-race.txt, docs/tables/quotes.jsonl |
| [throttle-landing.svg](throttle-landing.svg) | 6 | data | _report/lessonfaults.go | docs/tables/fig-throttle-landing.jsonl, docs/tables/rauns.jsonl, docs/tables/src/hosts.tsv, faults/catalog.yaml |
| [bucket-edges.svg](bucket-edges.svg) | 7 | data | _report/lessondata.go | docs/tables/fig-bucket-edges.jsonl, docs/tables/gate-readings-2.jsonl, docs/tables/gate-readings.jsonl, docs/tables/gates.jsonl |
| [fc-network.svg](fc-network.svg) | 8 | diagram | docs/img/src/fc-network.txt, _report/diagram.go | SECURITY.md, cells/backend-firecracker.sh, docs/img/src/fc-network.txt |
| [restore-timing.svg](restore-timing.svg) | 8 | data | _report/lessonmicrovm.go | docs/tables/fig-restore-timing.jsonl, docs/tables/readings-2026-10-29-boot9.jsonl, docs/tables/src/hosts.tsv |
| [clock-step-timers.svg](clock-step-timers.svg) | 9 | data | _report/lessonmicrovm.go | docs/tables/fig-clock-step-timers.jsonl, docs/tables/src/hosts.tsv |
| [three-piles.svg](three-piles.svg) | 10 | diagram | docs/img/src/three-piles.txt, _report/diagram.go | cells/compose/config/laug-server.yaml, docs/img/src/three-piles.txt, docs/tables/rauns.jsonl, docs/tables/src/hosts.tsv, faults/catalog.yaml |
| [toxic-fuse.svg](toxic-fuse.svg) | 11 | diagram | docs/img/src/toxic-fuse.txt, _report/diagram.go | cells/compose/cell.yml, docs/img/src/toxic-fuse.txt, docs/tables/fig-toxic-fuse.jsonl, docs/tables/rauns.jsonl, docs/tables/src/hosts.tsv, faults/catalog.yaml |
| [eager-ab.svg](eager-ab.svg) | 12 | diagram | docs/img/src/eager-ab.txt, _report/diagram.go | docs/img/src/eager-ab.txt, docs/tables/compare.jsonl, docs/tables/gates.jsonl, docs/tables/loads.jsonl, docs/tables/src/hosts.tsv, load/releases/1.32.yaml |
| [candidates.svg](candidates.svg) | 13 | data | _report/lessoncandidates.go | docs/tables/rauns.jsonl, docs/tables/readings-2026-10-23-boot3.jsonl, docs/tables/src/hosts.tsv |
| [neighbours.svg](neighbours.svg) | 13 | data | _report/lessondata.go | docs/tables/gate-items.jsonl, docs/tables/gate-readings-2.jsonl, docs/tables/gate-readings.jsonl, docs/tables/gates.jsonl |
| [stuck-shard.svg](stuck-shard.svg) | 13 | data | _report/lessonfaults.go | docs/tables/fig-stuck-shard.jsonl, docs/tables/rauns.jsonl, docs/tables/src/hosts.tsv |
| [arm-band.svg](arm-band.svg) | 14 | data | _report/lessondata.go | docs/tables/compare.jsonl, docs/tables/gates.jsonl, gate/sets/calibration-arm-3item-m8i.8xlarge.txt, gate/sets/control-arm-known-slow.txt, gate/sets/validation-arm-3item-m8i.8xlarge.txt, load/steady-state.yaml |
| [incumbent-arm.svg](incumbent-arm.svg) | 14 | diagram | docs/img/src/incumbent-arm.txt, _report/diagram.go | docs/img/src/incumbent-arm.txt, gate/plan.yaml, internal/judge/rules.go, load/steady-state.yaml |
| [orchestrator.svg](orchestrator.svg) | 15 | diagram | docs/img/src/orchestrator.txt, _report/diagram.go | cells/compose/cell.yml, cmd/laug/constants.go, cmd/laug/doc.go, docs/img/src/orchestrator.txt |
| [cancel-path.svg](cancel-path.svg) | 16 | diagram | docs/img/src/cancel-path.txt, _report/diagram.go | cells/results/2026-10-27-boot6.txt, docs/img/src/cancel-path.txt, docs/tables/fig-cancel-path.jsonl, docs/tables/gates.jsonl, internal/orchestrator/gate.go, internal/orchestrator/steps.go |
| [adopted.svg](adopted.svg) | 17 | data | _report/lessonworkflow.go | docs/tables/fig-adopted.jsonl, docs/tables/gates.jsonl, docs/tables/readings-2026-10-24-boot4.jsonl |
| [gate-layers.svg](gate-layers.svg) | 18 | diagram | docs/img/src/gate-layers.txt, _report/diagram.go | cmd/laug/doc.go, docs/img/src/gate-layers.txt, gate/lib.sh, internal/orchestrator/gate.go, internal/orchestrator/steps.go |
| [gate-workflow.svg](gate-workflow.svg) | 18 | diagram | docs/img/src/gate-workflow.txt, _report/diagram.go | docs/img/src/gate-workflow.txt, internal/orchestrator/gate.go |
| [obs-stack.svg](obs-stack.svg) | 19 | diagram | docs/img/src/obs-stack.txt, _report/diagram.go | _obs/collector.yaml, _obs/grafana/grafana.ini, _obs/grafana/provisioning/datasources/stack.yml, _obs/jaeger.yaml, _obs/prometheus.yml, _obs/versions.txt, cells/compose/cell.yml, docs/img/src/obs-stack.txt |
| [staleness.svg](staleness.svg) | 19 | data | _report/lessonworkflow.go | _obs/follow/fakecell_test.go, _obs/prometheus.yml, docs/tables/fig-staleness.jsonl, docs/tables/gates.jsonl, faults/catalog.yaml |

## The pages' figures

Each is a diagram a page shows and no stop does, drawn as the stops' diagrams are.

| Figure | Page | Kind | Generator | Sources |
| --- | --- | --- | --- | --- |
| [map.svg](map.svg) | README.md | diagram | docs/img/src/map.txt, _report/diagram.go | cells/compose/cell.yml, cmd/laug/main.go, cmd/laug/pool.go, docs/DESIGN.md, docs/img/src/map.txt, faults/README.md, faults/catalog.yaml, gate/plan.yaml, gate/run.sh, internal/pool/pool.go |
| [cell.svg](cell.svg) | cells/README.md | diagram | docs/img/src/cell.txt, _report/diagram.go | cells/backend-compose.sh, cells/backend-firecracker.sh, cells/compose/cell.yml, cells/compose/prometheus.yml, cells/rootfs/snapshot.sh, docs/img/src/cell.txt |
| [rootfs.svg](rootfs.svg) | cells/rootfs/README.md | diagram | docs/img/src/rootfs.txt, _report/diagram.go | cells/backend-firecracker.sh, cells/rootfs/fc-lib.sh, cells/rootfs/restore.sh, cells/rootfs/snapshot.sh, docs/img/src/rootfs.txt, internal/pool/pool.go |
