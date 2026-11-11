# What laug does

laug's capabilities, one a section: what it does, what shows it, and its limits. Every number cites a kept record, a
section of [RECORDS](RECORDS.md), or a row of the tables under `docs/tables/`, by its key. A number from a record kept
outside this tree rests on that record's sha256 in `docs/tables/records.jsonl`; where the page cites the record by
line, `docs/tables/quotes.jsonl` holds the line. A run is on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), unless it
says. Times of day are UTC. Open questions close the page.

The results, one row each; the sections below hold the rest, and the limits.

| What | Result | Evidence |
| --- | --- | --- |
| Replay | a HealthCheck recorded on the lab host replays; a build with its two activities swapped fails on it, TMPRL1100 at event 12 | `make test-go`: TestReplayHealthCheck, TestReplayDetectsSwappedActivities; `cells/results/2026-10-23-boot1.txt`, A9e, A9f |
| Pool, compose cells | a cell SERVING 44 s from nothing; cached, 22 s for the host's first since its start and 14 to 15 s for 13 more; a lease 1 ms at p95 from warm, 17.5 s with the pool drained | `cells/results/2026-10-23-boot1.txt`, W, A1, P2 |
| Pool, microVM cells | a Firecracker cell warm 4.60 to 4.72 s after a restore, frozen, nested on the lab host, the drive's cache_type Unsafe, sent and read back; a cold lease 7.6 s with the pool drained, jailed, nested; on the metal host, an m8id.metal-48xl (192 vCPU, 768 GiB), bare metal, a jailed create 1.42 to 1.58 s | `cells/results/2026-10-29-boot9.txt`, R, P; `docs/tables/records.jsonl#cells/results/2026-10-29-boot10.txt`, J-P2; `docs/tables/records.jsonl#cells/results/2026-10-30-boot12.txt`, R; `docs/tables/records.jsonl#cells/results/2026-11-01-boot13.txt`, BOX |
| Fault | persistence-latency ADVANCE 3 of 3 with 537 to 540 failed starts during the hold and 0 after; recovered 25 s after removal. ADVANCE beside those failed starts because the fault is expected to stop user traffic, and only the after window judges every failed start | `faults/results/raun-lab2-persistence-latency-*.json`; `faults/catalog.yaml`:35; `faults/run.sh`:12-16 |
| Gate | verdict handed on 612.5 to 614.3 s after start, in 4 counted A/A gates | `cells/results/2026-10-24-boot4.txt`, G1, G2, G4, G5 |
| Controls | a known-bad change BLOCKs through `laug gate run`; 25 µs on the release's Postgres BLOCKs 3 of 3, every max held | `cells/results/2026-10-24-boot4.txt`, G3; `gate/sets/control-arm-known-slow.txt`, the 25 µs gates |
| Noise | 0 false BLOCKs from the arm in 30 A/A gates: at most 9.5%, 95% confidence | `gate/sets/validation-arm-3item-m8i.8xlarge.txt` |
| A/B, 1.32.1 against 1.31.0 | Persistence p99 sums every operation and every role, and 1.32 changed the mix, so a gap in it may be a change of mix rather than of speed; the files do not say. Of the 3 judged gates on this one host and one load, 2 ADVANCEd and 1 BLOCKed, g20261018-092540, persistence p99 +0.401 ms past 0.35. | `gate/results/g20261018-092540/verdict.md`, `gate/results/g20261018-091444/verdict.md`, `docs/tables/gates.jsonl#g20261018-090337` |
| Judge | on the dev host, before the shell judges were deleted, the Go judge and the shell judges judged the 158 committed runs and agreed on every run: 156 identical, 2 re-rendered (trap verdicts), 0 unexplained | `cells/results/2026-11-04-judge-shell-proof.txt`; the current judge's rows, whose outcomes match the proof's: `docs/tables/rejudge.jsonl`; their counts: `docs/EVIDENCE.md` |
| Durable | workflow Gate's verdict held with its worker killed 60 s and 300 s into the wave, handed on 612.8 and 614.2 s | `cells/results/2026-10-24-boot4.txt`, G4, G5 |

## Replay

**What it does.** `make test-go` replays a HealthCheck workflow history recorded on a real cell, with the Temporal Go
SDK's replayer, and fails the build when the workflow code no longer matches it.

**Shown by.** TestReplayHealthCheck replays `internal/health/testdata/healthcheck_history.json`, and
TestReplayDetectsSwappedActivities fails on it with the two activities swapped. In boot 1, `bin/laug health record
-overwrite-fixture` recorded a new history on the lab host, 24 events of the same types as the committed file's, and
both tests passed on it (`cells/results/2026-10-23-boot1.txt`:289-295, A9e and A9f).

**Limits.** The HealthCheck replay holds one fixture, `internal/health/testdata/healthcheck_history.json`;
TestReplayGateHistories, in `make test-go`, replays workflow Gate's six, under
`internal/orchestrator/testdata/histories/`, and fails when Gate's commands change. Five are from fake gates (an
ADVANCE, a refusal, a failed harness, a cancel, a worker restarted mid-wave), one from a real gate on the lab host.

## The pool

**What it does.** `laug pool serve` keeps cells warm: each a complete Temporal server, every role and its own
Postgres, on one host. `laug lease` takes one, `laug release` gives it back, and the pool resets and refills. Two
backends make cells: compose, Docker Compose projects, and Firecracker, microVMs on a host with `/dev/kvm`.

**Shown by.** Boot 1, on compose cells (`cells/results/2026-10-23-boot1.txt`):

| Reading | Value | Lines |
| --- | --- | --- |
| `laug cell up` from nothing, 8 images pulled | SERVING after 44 s | :90 |
| the host's first cached cell, then 13 more | 22 s, then 14 to 15 s | :74, :79 |
| a HealthCheck round trip in `laug cell health`, 9 calls on a Postgres cell | 1 090 to 1 131 ms | :95 |
| `laug pool serve` to 3 warm | 50.7 s | :365 |
| a lease from warm, p50, p95 and max over 20 cycles | 0, 1 and 1 ms | :366 |
| a lease with the pool drained | 17.5 s | :367 |
| a 30 s TTL, from the lease to the cell gone from `/status` | 54.1 s | :368 |
| 3 warm cells, 33 containers: docker stats, then anon plus file bytes | 2037 MiB; 2095 MiB: 629.7, 741.5 and 723.8 MiB a cell | :369-372 |
| `laug reset -last` three times, the id and block kept each time | 34.3, 31.1 and 38.1 s | :377-378 |
| `laug release -last` after the three resets | 16.5 s | :377-378 |
| `laug cell down -remove-volumes` | 13 containers, 2 volumes and 1 network gone, 0 left | :297 |
| `laug pool down -remove-cells`, 3 cells | 53.3 s; 39 containers, 6 volumes and 3 networks gone; 0 containers, volumes, networks and per-cell directories left | :396-397 |

Boot 9, on Firecracker cells (RECORDS, section 12): a cold boot's HealthCheck passed 25.37 to 27.36 s after
compose up, and a restore from a snapshot was warm in 4.60 to 4.72 s, 12 restores of 12
(`cells/results/2026-10-29-boot9.txt`:136, :157, :179). Boot 10, on Firecracker cells under the jailer, nested: 20
lease cycles from warm read 0 to 1 ms, p95 0 ms, and a cold lease with the pool drained 7.6 s, against boot 9's 5.4 s
unjailed (`docs/tables/records.jsonl#cells/results/2026-10-29-boot10.txt`, J-P2).

`laug pool down -backend firecracker -remove-cells` with 2 cells up took 1.2 s in boot 9 and left 0 firecracker
processes, links, cell disks, block directories and deny rules (`cells/results/2026-10-29-boot9.txt`:260-262). At the
batch's end the host still held `tap0`, a MASQUERADE rule for its subnet, IP forwarding on and 2 FORWARD rules naming
`tap0` (`cells/results/2026-10-29-boot9.txt`:459-461).
[`make fc-image`, `make fc-snapshot` and `make fc-restore`](../cells/rootfs/README.md#use) leave them by design. Under
the jailer, each of boot 11's 24 pool downs also left 0 jails, 0 cell cgroups and 0 zombies
(`docs/tables/records.jsonl#cells/results/2026-10-29-boot11.txt`, E). Over a 5 min load, a cell's VMM peaked at 2679.0
to 2825.3 MiB of Pss at 4 vCPU, over 14 loads, and 2781.6 to 2810.2 MiB at 8 vCPU, over 10 (the `vmm_<arm>_<n>` rows of
`docs/tables/readings-2026-10-29-boot11.jsonl`).

Boot 10 ran 51 creates under the jail that [SECURITY.md](../SECURITY.md#firecracker-and-root) describes, and 0 failed.
On the first one's VMM, 23 checks held: its uid, gid and groups; no effective or permitted capability, no new privileges
and seccomp on each of its 7 threads. The rest covered its own namespaces, argv and cgroup, the snapshot files
read-only, the inodes the cell's own, the tap's owner and the links
(`docs/tables/records.jsonl#cells/results/2026-10-29-boot10.txt`, J and What it shows). Boot 11 read the 23 again on a
VMM 600 s old, after a 5 min load. It read 4 more: CapInh 0 on its 7 threads, its cgroup with the VMM alone in it,
laug's cgroup present, and 50 open files against 2048. All 27 held
(`docs/tables/records.jsonl#cells/results/2026-10-29-boot11.txt`, J'). On the metal host, boot 12's 35 jailed creates
had 0 failed. The 23 held on two VMMs of one pool, uids 20000 and 20001, and the 27 held at 600 s after a load
(`docs/tables/records.jsonl#cells/results/2026-10-30-boot12.txt`, J, J' and What it shows).

Faults, `laug baseline` and `laug cell dynconfig-check -cell <id>` reach a microVM guest's Docker over the lease's
`ssh`. In boot 9, `laug cell dynconfig-check` exited 0 on a microVM cell. `laug baseline` exited 3 there: it refused
the 69 s window, shorter than the 300 s `load/steady-state.yaml` trusts (`cells/results/2026-10-29-boot9.txt`, P7).
On the first lab host, an m8i.2xlarge (8 vCPU, 32 GiB), faults reached the guest this way
(`docs/tables/records.jsonl#cells/results/2026-10-13-firecracker-faults.txt`). On that host, a container was blocked
from a microVM cell's 10.254.0.2:9090 by the deny rules on the cell's link that
[SECURITY.md](../SECURITY.md#firecracker-and-root) describes
(`docs/tables/quotes.jsonl#cells/results/2026-10-13-firecracker-pool.txt:33`).
There, two creates run at once returned in 2.33 and 2.35 s and passed health at 6.13 and 8.37 s. A file written and
synced in one cell was absent in the other. A guest reached no other cell, the host's ssh or the internet, and a create
killed at 0.3 to 2.5 s, then destroyed, left nothing
(`docs/tables/records.jsonl#cells/results/2026-10-13-firecracker-pool.txt`).

**Limits.**
- Boot 9's and batch E's microVM cells ran nested, on EC2 with `/dev/kvm` inside the instance
  (`cells/results/2026-10-29-boot9.txt`:3, `cells/results/2026-10-24-batchE.txt`:3), and so did the first lab host's
  (`docs/tables/quotes.jsonl#cells/results/2026-10-13-ec2.txt:3`). Boots 12 and 13 ran on the metal host's bare
  metal: hypervisor on 0 of the flags lines of `/proc/cpuinfo`, vmx on 192
  (`docs/tables/quotes.jsonl#cells/results/2026-10-30-boot12.txt:3-5`,
  `docs/tables/quotes.jsonl#cells/results/2026-11-01-boot13.txt:3-5`).
- The drive's cache mode is `FC_CACHE_TYPE` in `cells/rootfs/fc-lib.sh`, Writeback unless set; boots 9 to 13 sent
  Unsafe, except in their Writeback loads. Under Unsafe the guest sees no flush feature, so a guest fsync reaches no
  disk. <!-- source: Firecracker v1.17, block-caching.md --> Boot 9's snapshots were frozen. It sent Unsafe and
  io_engine Sync and read Unsafe back four ways, and 1000 synced writes in a guest sent its VMM no flush
  (`cells/results/2026-10-29-boot9.txt`:145-146, :241-242, :266-267). Its 84 pool cells' VMMs flushed 0 times (their
  metrics files, `docs/tables/records.jsonl#cells/results/2026-10-29-boot9/`). Batch E set no cache mode and read none
  (`cells/results/2026-10-24-batchE.txt`:419, :533), so it ran at Firecracker's default, Unsafe by its block-caching.md.
  <!-- source: Firecracker v1.17 block-caching.md, the default cache mode -->
- Under Writeback, nested, every 5 min load at 20/s BLOCKed, with no fault. On an XFS in a loop file a guest flush cost
  7.165 ms and persistence p99 read 48.48 and 48.49 ms against its 23 ms max
  (`docs/tables/records.jsonl#cells/results/2026-10-29-boot8.txt`, L, B and C). On an XFS on a gp3 volume of its own,
  a flush cost 2.161 ms and persistence p99
  read 19.02 to 19.33 ms. But every workflow took over 50 ms and 6 of 6 loads BLOCKed
  (`docs/tables/records.jsonl#cells/results/2026-10-29-boot10.txt`, V and L, F-w). On an XFS on the metal host's local
  NVMe, at its default idle states, a flush cost 0.591 ms at 4 vCPU. There persistence p99 read 9.18 to 9.47 ms and 6 of
  6 loads ADVANCEd. At 8 vCPU it read 9.45 to 9.50 ms and 6 of 6 ADVANCEd, with no flush read. These verdicts apply
  `load/steady-state.yaml`'s maxima, set on the lab host's type
  (`docs/tables/records.jsonl#cells/results/2026-10-30-boot12.txt`, V-nvme and L, R, F8NW). The instance store is erased
  when the instance stops, so an fsync there likely outlives a crash of the guest or the VMM, not a stop.
  <!-- source: AWS EC2 User Guide, instance store data persistence -->
- A pool is one host's. Two pools on one host share more than they should: one `.last-lease.json` for every
  `laug raun` (`cmd/laug/raun.go`), the health loop's fixed metrics port :9091 (`internal/health/check.go`), and no
  cell selector in a Prometheus over its cells. A gate starts a pool of its own at min 0 (`gate/lib.sh`).
- A lease has no idempotency key (`internal/pool/pool.go`).
- A lease ends past its TTL by up to one reaper tick plus its cell's destroy and the destroys queued ahead of it, since
  expired leases are released one at a time (`internal/pool/pool.go`:615-623). The tick is
  `laug pool serve -reap-interval`, 10 s by default (`cmd/laug/pool.go`:139). In boot 1 a 10 m lease was gone from
  `/status` 631.2 s after it was taken, 31.2 s past its TTL (`cells/results/2026-10-23-boot1.txt`:373-376).
- A pool destroys no cell when it stops: its last line names the `laug pool down` that removes them
  (`cmd/laug/pool.go`). Boot 9's microVM pool stopped "with 2 cells in its table, none destroyed"
  (`cells/results/2026-10-29-boot9.txt`:263).
- The jail's checks have run on five VMMs: three idle, 11 to 15 s old, and two 600 s old after a 5 min load
  (`docs/tables/records.jsonl#cells/results/2026-10-30-boot12.txt`, J'). None ran an hour into a lease.
- A cell's Postgres image floats on its major tag, 16, as no record names its minor version; every other image is
  pinned (`cells/compose/cell.yml`). In boot 1, postgres:16 was image ca0bd484 before the cell from nothing and again
  after the saved images were reloaded. That cell's `laug cell up` pulled the tag again and likely got another build
  (`cells/results/2026-10-23-boot1.txt`:100-106).

## Loads

**What it does.** `laug load` runs an Omes scenario on a leased cell; the gate's steady load is 5 m at 20 workflows/s
with at most 50 in flight (`gate/plan.yaml`). `laug baseline` reads the run's thresholds, and `laug verdict` judges it
against `load/steady-state.yaml`: ADVANCE or BLOCK.

**Shown by.** Boot 1's six release loads (RECORDS, section 7), all ADVANCE (`docs/tables/loads.jsonl#lab1-v1310-1` to
`-3` and `#lab1-v1321-1` to `-3`). 1.31.0 read frontend p99 10.0 to 10.9 ms over its three
(`cells/results/2026-10-23-boot1.txt`:322-326). 1.32.1 records no activity schedule-to-start at all, and its verdict
says so (`load/results/lab1-v1321-1.baseline.json`:3).

**Limits.**
- One scenario on Postgres, on one host type. Thresholds set on one type are refused for another
  ([a set for a new host type](../load/baseline.md#a-set-for-a-new-host-type)).
- `laug verdict` alone reads a renamed error metric as 0 (`or vector(0)`, `load/steady-state.yaml`).
- nginx in front of the frontend sets no `grpc_read_timeout` (`cells/compose/nginx.conf`), so an idle long poll is cut
  at 60 s. <!-- source: nginx, ngx_http_grpc_module, default 60s -->
- Every cell runs with `system.forceSearchAttributesCacheRefreshOnRead` on (`cells/compose/dynamicconfig/laug.yaml`).
  Its cost is not measured.

## Faults

**What it does.** `laug raun <fault>` leases a cell, starts a load, injects a fault from `faults/catalog.yaml`, waits
for it to land, removes it, and reads the windows before, during and after: ADVANCE when the cell recovers inside the
fault's budget. `faults/README.md` lists the faults and what each one does.

**Shown by.**
- Persistence latency, 3 rauns (RECORDS, section 6): 3 of 3 ADVANCE, landed 5 to 10 s after the inject, 537 to 540
  starts failed during the hold and 0 after, recovered 25 s after the removal
  (`faults/results/raun-lab2-persistence-latency-*.json`). ADVANCE sits beside those failed starts because the
  catalog row expects this fault to stop user traffic (`faults/catalog.yaml`:35), and a failed start BLOCKs only in
  the after window, or in any window when its error says the server lost a workflow it had acknowledged
  (`faults/run.sh`:12-16, `load/lost-work.sh`:138-151). [Stop 13](LEARNING.md#stop-13) holds the reasoning.
- Kill-history, 3 rauns (RECORDS, section 4): history's shards owned again 0.5 to 0.6 s after the restart, while 545 to
  551 starts failed during the hold.
- Persistence resets, 6 rauns (RECORDS, section 5): at a toxicity of 0.30 or less the resets never reach Temporal; at
  0.50 the signal lands 95 s in and the raun ADVANCEs.
- On microVM cells, boot 9 ran 52 rauns of 18 faults: 12 ADVANCE in 9 of them, 2 did not land, 38 were never steady
  before any fault (RECORDS, section 12; `cells/results/2026-10-29-boot9.txt`:529-530).
- A durable timer across a 120.9 s step back of a microVM guest's clock fired 121.2 s late by host time, while the
  server recorded 60.0 s (RECORDS, section 13).

**Limits.**
- A fault's `signal_peak` is the larger of two samples, at landing and at the hold's end (`faults/run.sh`), and a
  signal read over a 30 s range bounds how soon a fault reads as recovered.
- 5 faults have no steady microVM raun (`cells/results/2026-10-29-boot9.txt`:526-527). disk-slow runs on microVM cells
  only.
- A raun reads its cell from `.last-lease.json`, so one host runs one `laug raun` at a time.

## The release gate

**What it does.** `laug gate run <tag>` runs a release beside the release it replaces, each on a fresh cell from a
pool of its own: the release under a steady load, the release under a fault, and the incumbent under the same load, in
one wave. Rules R1 to R7 judge the run, and R7 compares the two arms reading by reading against tolerances set from
A/A gates. It returns ADVANCE or BLOCK with the evidence in `gate/results/<gate id>/`, and `laug gate rejudge`
judges a run again and compares the bytes.

**Shown by.**
- The A/B BLOCK (RECORDS, section 1): persistence p99 sums every operation and every role, and 1.32 changed the mix.
  On the same load 1.32.1 read up to 0.401 ms worse than 1.31.0, past a 0.35 ms tolerance, every fixed limit held. The
  shift sits at the tolerance: on this one host and load, 1 of the 3 judged gates BLOCKed and 2 ADVANCEd. It may be a
  change of mix rather than of speed; the files do not say. 1.32.1 read 0.140 to 0.401 ms above 1.31.0 in all 4 gates
  of the pair. In 3 of the 4 the gap is wider than any d of the 30 A/A gates below, which stayed within 0.181 ms either
  way (`docs/tables/gates.jsonl#g20261017-222254`, `#g20261018-090337`, `#g20261018-091444`, `#g20261018-092540`).
- One A/A gate (RECORDS, section 3): 1.31.0 beside itself, persistence p99 d -0.074 ms.
- Noise: after the 20 calibration gates (DESIGN, section 7.1), 30 validation A/A gates followed on fresh clones
  (`gate/sets/validation-arm-3item-m8i.8xlarge.txt`), each a row of `docs/tables/gates.jsonl`. 30 of 30 ADVANCEd with
  the tolerances judging. They gave 0 R7 BLOCKs, which bounds R7's false-BLOCK rate at 9.5% (95% confidence,
  1 - 0.05^(1/30)), and every d at most 0.67 of its tolerance
  (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-arm-validation.txt:650` and :652-653,
  `docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-arm-validation.txt:910`). The first, g20261018-062701, was its
  host's first gate since boot
  (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-arm-validation.txt:391` and :457); over the 29 after it, the
  bound is 9.8% (1 - 0.05^(1/29)). Boot 4's A/A gates join no set: their judge version and set sha256 differ from the
  validation set's, 2 of the signature's 16 fields (`cells/results/2026-10-24-boot4.txt`, G2).
- Boot 4's 4 counted A/A gates handed their verdict on 612.5 to 614.3 s after the start
  (`cells/results/2026-10-24-boot4.txt`:679). Their harness ran 1187 checks in 237.7 to 238.3 s, and their wave began
  at +250.0 to +251.6 s (`cells/results/2026-10-24-boot4.txt`:56-78, :265).
- The judge: on the dev host, before the shell judges were deleted, the Go judge and the shell judges judged the 158
  committed runs. They agreed on every run: 156 identical, 2 re-rendered, 0 unexplained. The 2 re-rendered are trap
  verdicts, which the gate wrote when it stopped before its judge (`cells/results/2026-11-04-judge-shell-proof.txt`).
  The rows of `docs/tables/rejudge.jsonl` are the current judge's, and its outcome matches the proof's on every run;
  `docs/EVIDENCE.md` holds its rejudge line: "158 runs: 156 identical, 0 known, 2 re-rendered, 0 unexplained, 0 not
  judged". The 158 are the lab tree's, kept outside this one; a clone holds the 3 runs kept in `gate/results/`
  (`gate/results/README.md`).
- Controls. A control is a change the gate must catch. Its file, `gate/controls/<name>.diff`, holds one comment line
  saying what the change must make the gate do, then the diff. On cells, each control ran on a branch or a clone that
  held the change. arm-always-pass ran on a branch whose shell judge never failed R7
  (`gate/sets/control-arm-always-pass.txt`); its diff now makes the same change in the Go judge.
  `laug gate control -name <name> -fake` runs one on fakes. Each set file under `gate/sets/` lists the runs and the
  transcript lines that record them.

| Control | The change | Must BLOCK, class | Runs on cells |
| --- | --- | --- | --- |
| gate5-always-advance | `load/verdict.sh` prints ADVANCE before it reads a run | harness, on R1 | 3 |
| arm-always-pass | R7 never fails an item in the Go judge, `internal/judge` | harness, on R1 | 1, of the earlier shell-judge form |
| gate5-known-bad | frontend.rps 5 in every cell's dynamic config | regression, R4 R5 R7 | 8 |
| arm-known-slow | netem delays every packet into the release cell's Postgres | regression, on R7 | 10 |
| persistence-maxqps-50 | history.persistenceMaxQPS 50 in every cell's dynamic config | refused | 2 |

- arm-known-slow at 25 µs on every packet into the release's Postgres: 3 of 3 gates BLOCKed, class regression, on R7
  alone, with every max held (`gate/sets/control-arm-known-slow.txt`; `docs/tables/gates.jsonl#g20261018-163914`,
  `#g20261018-170110`, `#g20261018-172301`). Their persistence d read 1.499 to 1.645 ms against the row's tolerance of
  0.35 (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-arm-validation.txt:705-710` and :712-716).
- gate5-known-bad through `laug gate run`, boot 4's G3: steady completed 285 workflows and the incumbent 275, both
  under the 5700 floor. R7 read "not compared: incumbent did not ADVANCE" (`cells/results/2026-10-24-boot4.txt`, G3).
- persistence-maxqps-50 ran twice, as boot 4's C1 and C2. In C1 the first lease was asked for 2.8 s after the start
  and refused 317.8 s later; laugd waits 300 s for a new cell's health. No item ran, so R2 to R7 failed. Both read host
  quiet as false ("poll.jsonl is missing") and kept class refused (`cells/results/2026-10-24-boot4.txt`, C). The
  diff's comment line still says no run of this form is recorded. It stays as it is, because boot 4's transcript names
  the diff by its sha256 (`cells/results/2026-10-24-boot4.txt:369`, :399).

**Limits.**
- One host type, one scenario, one store: a gate refuses a host type its thresholds were not set on. A set for
  another type is made on that type (`load/baseline.md`, A set for a new host type). On this host type the
  calibration session took 5.35 h in the launch that counted
  (`docs/tables/quotes.jsonl#cells/results/2026-10-17-lab-arm-calibration.txt:390`). The two validation sessions took
  3.25 h (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-arm-validation.txt:443`) and 26.1 min plus 4.26 h in
  two launches (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-arm-validation.txt:817-818`). The three came
  to about 13.3 h, the calibration session's two earlier launches aside, with controls and A/B gates among the runs.
- A host's first gate is a warm-up, not counted (DESIGN, section 7, the warm-up rule): 4 BLOCKs in 63 A/A gates to
  boot 6 (`cells/results/2026-10-27-boot6.txt`:445).
- 8 steady loads at once fail their maxima on this host: 0 of 16 items ADVANCE at 8, 4 of 12 at 6, on the noise
  ladder below.
- No upgrade in place, no Multi-Cluster Replication, no region failure.

The noise ladder. `gate/plans/noise.yaml` runs N copies of steady's load at once and nothing else. It has no fault
item, so every run BLOCKs on R5, and its class is control unless an item BLOCKs on its own maxima. A run cut to fewer
than its 8 items BLOCKs on R2 too, and its class stays control. Those BLOCKs are by design. The ladder reads each
item's own verdict. Boot 4 ran N = 1, 2, 3, 4, 5, 5, 6, 6, 8 and 8, with `bin/obs usage record` beside each
(`cells/results/2026-10-24-boot4.txt`, N; `extra/n<N>-<k>-usage.json` in its directory):

| N | Items ADVANCE | Workflow task s2s p99, ms (max 38) | End to end p95, ms (max 84) | Busy cores of 32 | Run-queue wait, ms/s a CPU |
| --- | --- | --- | --- | --- | --- |
| 1 | 1 of 1 | 19.01 | 49.74 | 1.588 | 1.192 |
| 2 | 2 of 2 | 17.66 to 17.71 | 49.15 to 49.17 | 3.273 | 4.421 |
| 3 | 3 of 3 | 17.57 to 17.89 | 49.44 to 49.52 | 5.154 | 9.270 |
| 4 | 4 of 4 | 19.06 to 19.18 | 49.98 to 50.73 | 7.243 | 17.327 |
| 5, twice | 10 of 10 | 31.28 to 37.53 | 70.34 to 73.66 | 9.606 to 9.659 | 30.265 to 31.646 |
| 6, twice | 4 of 12 | 33.29 to 42.50 | 80.03 to 82.39 | 12.159 to 12.231 | 51.407 to 53.168 |
| 8, twice | 0 of 16 | 91.88 to 164.69 | 179.99 to 183.17 | 16.916 to 16.940 | 182.228 to 182.638 |

From N = 6 on, items BLOCKed on their own maxima, and the class was regression. At 6 they BLOCKed on workflow task
schedule-to-start alone. At 8 all 16 BLOCKed, each on five rows (`cells/results/2026-10-24-boot4.txt:635-638`).
Completed workflows stayed at 5839 to 5973 a run, over the 5700 floor, at every N.

## The durable gate

**What it does.** `laug gate run <tag> -workflow` runs the same gate as the Temporal workflow Gate, on a dev server
(`laug orchestrator server`) and a worker (`laug orchestrator worker`). A killed worker's gate goes on in the next
worker, and a cancel stops it.

**Shown by.**
- Boot 4 (RECORDS, section 10): `g20261024-092853` ADVANCE, its verdict byte for byte equal to its own rejudge
  (`cells/results/2026-10-24-boot4.txt`:269-271). It is boot 4's G2, through `laug gate run -workflow`, and its
  signature equals G1's, through `laug gate run` (the same record, G2).
- The worker killed 60 s and 300 s into the wave: both gates ADVANCE, handed on 612.8 and 614.2 s against 612.5 and
  614.3 s for two gates never killed, with the signature of the first (`cells/results/2026-10-24-boot4.txt`:360-361).
  Each ran the wave and its three awaits on attempt 2, and the 15 other activities on attempt 1
  (`cells/results/2026-10-24-boot4.txt`:347-348, :356).
- A fake gate closed 1.7 s after a cancel (`cells/results/2026-10-24-boot4.txt`:496).

**Limits.**
- A terminated workflow Gate runs no code; `laug gate stop` runs the stop path by hand. Boot 4 terminated a fake gate
  30 s into its wave; `laug gate stop` then TERMed the wave, wrote the trap BLOCK and released, exit 1 in 1.4 s
  (`cells/results/2026-10-24-boot4.txt`:513, :517-519).
- `laug gate post` hands a verdict to one sink, `local`, which prints it.

## Observability

**What it does.** `bin/obs up` runs a stack of six parts beside the cells on 127.0.0.1: Prometheus, Grafana, the
OpenTelemetry Collector, Jaeger, cellstat and follow, with the boards under `_obs/stack/`. `bin/obs backfill` puts the
gate runs under `gate/results/` that its store lacks into it, at their own times, and `bin/obs find <gate id>` prints a
gate's board and trace links. DESIGN section 9 says how the parts work; `_obs/README.md` says how to run the stack and
extend it.

**Shown by.**
- Fetching the four releases, each matching its size and sha256, `bin/obs up` exited 0 in 135.9 s; the store settled
  120.5 s after ready. The stack then held 11 listeners, every one on 127.0.0.1
  (`cells/results/2026-10-24-boot4.txt`:544, :553-554).
- The noise rule was fixed before its first run, with five lines, R-a to R-e. Gates ran with the stack down and up in
  turn, and in each up run one viewer read the gate run board every 10 s
  (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-obs.txt:281-283`). 9 of 9 counted gates ADVANCEd with the
  host quiet, 5 of them with the stack up, and R-a to R-d held in every run they read. R-e held on each cell's
  Prometheus, 0.014 to 0.015 cores at most against 0.05, and on the stack's RSS, 553.6 to 669.0 MiB summed over the
  five parts other than cellstat against 1.5 GiB. It failed on CPU: the same five read 6.772 to 7.136% of one CPU in
  each of 4 up waves, against 5% (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-obs.txt:327-335`).
  The store read 4.328 to 4.744% of one CPU and Grafana with its viewer 1.375 to 1.403%
  (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-obs.txt:338-339`); idle, with no cell, the five read 1.195%
  (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-obs.txt:268`).
- In each of 5 gates the store's 18 readings equal the gates' files to 2e-14. The store federated a hand-leased cell
  6 s after its lease, and 150 s into its load held 4797 of the cell's 18953 series
  (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-obs.txt:368-370`, :509-510).
- Each of the noise rule's 5 up gates gave one trace, of 38 to 41 spans
  (`docs/tables/quotes.jsonl#cells/results/2026-10-18-lab-obs.txt:309-311`). After each of three fake workflow gates
  traced with `-otlp`, `bin/obs find <gate id>` exited 0 in 0.2 s with the gate's board and trace links
  (`cells/results/2026-10-24-boot4.txt`:568-571).
- `make obs-test` exited 0 in 662.1 s with 0 tests skipped. It found every config address on 127.0.0.1, the configs
  valid and the boards within their rules, and each of 48 planted mistakes refused. The fake cell's two tests took
  340.82 and 313.25 s, and their staleness reading is LEARNING's stop 19 (`cells/results/2026-10-24-boot4.txt`:582-590).
- cellstat read every container of a two-item and a three-item gate, 0 unresolved. It cost 0.31% of one CPU on the
  idle host, 0.47% at two items and 0.57% at three
  (`docs/tables/records.jsonl#cells/results/2026-10-17-lab-cell-usage.txt`).

**Limits.**
- The stack is down while a gate runs, for that 5% line (DESIGN, section 9), so a gate's live series and logs end
  with its cells; `bin/obs backfill` puts the gate's records into the store after.
- No viewer's load runs on the gate run board.
- HealthCheck's "visible N ms" is one List call (`internal/health/check.go`), with no wait for the close record.

## Open questions

- A release that cannot boot a cell BLOCKs class refused, as a host fault would (`cells/results/2026-10-24-boot4.txt`,
  C1 and C2). A cell that boots on the same host at the same time would tell the two apart.
- Why boot 4's warm-up gates were never steady on the faulted item: the record holds no per-second reading of the
  before window (`cells/results/2026-10-24-boot4.txt`, What the pack lacks).
- Why microVM cells read end-to-end p95 over compose's before any fault, nested (`cells/results/2026-10-24-batchE.txt`,
  F).
- Why a microVM cell loaded about 5 s after its create read frontend p99 over its maximum in most of boot 9's before
  windows (`cells/results/2026-10-29-boot9.txt`, F).
- Whether a snapshot's age, from seconds to minutes, moves a load on a nested host. On the metal host, an
  m8id.metal-48xl (192 vCPU, 768 GiB), it did not, from 3.8 s to 30 min
  (`docs/tables/quotes.jsonl#cells/results/2026-10-30-boot12.txt:440`). There vm.mem was whole in the host's page
  cache at each lease, so a snapshot read cold from disk is untested
  (`docs/tables/records.jsonl#cells/results/2026-10-30-boot12.txt`, XA).
- How dense one host can run: 7 steady loads at once were not tried (the noise ladder, under The release gate).
