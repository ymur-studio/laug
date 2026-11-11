# The kept records

This page is an appendix, for checking a kept record that a page cites by its path.

A record is what laug wrote when it ran: a gate run's directory, a raun's JSON, a load's files, a lab session's
transcript. The 110 files in the 15 numbered sections below stay in this tree as laug wrote them, but for the
transcripts' cites of working files, below. Every other record's numbers live as rows under `docs/tables/`: a gate, gate
item, raun, load or record row holds the sha256 of its record's files, and a reading, compare or rejudge row names its
parent row by key; `docs/tables/README.md` names each table's key and columns, and a page cites a row by its key.

A test machine is named by its type and size, in one of four phrases: "the lab host, an m8i.8xlarge (32 vCPU,
128 GiB)", "the dev host, a Linux VM with 24 vCPU and 47 GiB running Docker", "the metal host, an m8id.metal-48xl
(192 vCPU, 768 GiB)" and "the first lab host, an m8i.2xlarge (8 vCPU, 32 GiB)".

A gate's verdict can be checked from its directory alone: `bin/laug gate rejudge <id>` judges the run again in Go and
compares the bytes, and `bin/laug gate rejudge -all` does every run under `gate/results/`. A raun, a load or a lab
session can only be run again, on a cell.

Each section gives the record's path, what it shows, its key lines quoted byte for byte with their file and line, and
the one command that judges it again or runs it again. `TestKept`, in `make test-go`, holds every quote to its file and
every record in `docs/tables/src/kept.txt` to a section. In the transcripts, `<...>` stands where a line named a working
file that is not in this tree, inside a quotation too, and a line cite of a page names that page as it stood on
the run's tree. STATUS.md, the project's working log, kept its name: the transcripts cite it by line, as
`STATUS.md:N`, and it is not in this tree. A laug commit id in a record, such as a gate's `laug_commit`, names a commit
of laug's working history, which is not public.

## 1. The A/B BLOCK

`gate/results/g20261018-092540/`: gate g20261018-092540 BLOCKed, its persistence p99 0.401 ms worse than the
incumbent's and past a 0.35 ms tolerance, while every fixed limit held (R4 pass). [CAPABILITIES](CAPABILITIES.md), its
A/B row, gives the releases and the caveat on this reading.

```quote gate/results/g20261018-092540/verdict.md
3: Gate run g20261018-092540 on server 1.32.1: 3 item(s), 587.455 s from the gate start to this verdict on the wall clock (587.45 s monotonic).
8: | steady | ADVANCE | ADVANCE | regression | worse than the incumbent: persistence_latency_p99_ms 8.460 against 8.059 on incumbent, +0.401 past a tolerance of 0.35 |
16: | persistence_latency_p99_ms | 8.460 ms | 8.059 ms | +0.401 | 0.35 | WORSE |
33: | R4 outcome | pass |
36: | R7 incumbent | steady: worse than the incumbent: persistence_latency_p99_ms 8.460 against 8.059 on incumbent, +0.401 past a tolerance of 0.35 |
40: Class: regression.
42: **BLOCK**
```

Judge it again: `bin/laug gate rejudge g20261018-092540`

## 2. The A/B ADVANCE

`gate/results/g20261018-091444/`: the same pair 11 min earlier ADVANCEd at d +0.140 ms: R7's d moves from run to run.

```quote gate/results/g20261018-091444/verdict.md
3: Gate run g20261018-091444 on server 1.32.1: 3 item(s), 587.426 s from the gate start to this verdict on the wall clock (587.43 s monotonic).
15: | workflow_task_schedule_to_start_p99_ms | 17.054 ms | 16.858 ms | +0.195 | 1.02 | ok |
16: | persistence_latency_p99_ms | 8.409 ms | 8.269 ms | +0.140 | 0.35 | ok |
36: | R7 incumbent | pass |
40: **ADVANCE**
```

Judge it again: `bin/laug gate rejudge g20261018-091444`

## 3. One A/A gate

`gate/results/g20261018-075357/`: 1.31.0 beside itself drifts d -0.074 ms; the tolerances are set from 20 A/A gates like
it.

```quote gate/results/g20261018-075357/verdict.md
3: Gate run g20261018-075357 on server 1.31.0: 3 item(s), 587.039 s from the gate start to this verdict on the wall clock (587.04 s monotonic).
15: | workflow_task_schedule_to_start_p99_ms | 16.887 ms | 17.087 ms | -0.200 | 1.02 | ok |
16: | persistence_latency_p99_ms | 7.901 ms | 7.975 ms | -0.074 | 0.35 | ok |
18: | frontend_request_over_10ms | 1.08% | 1.16% | -0.0871 points | 0.32 points | ok |
19: | workflow_end_to_end_over_50ms | 2.98% | 2.78% | +0.1988 points | 0.64 points | ok |
40: **ADVANCE**
```

Judge it again: `bin/laug gate rejudge g20261018-075357`

## 4. Kill-history, three rauns

`faults/results/raun-lab6-kill-history-{1,2,3}.json`: history's shards are owned again 0.5 to 0.6 s after the restart,
while 545 to 551 starts fail during the hold and 44 to 51 more in the drain.

```quote faults/results/raun-lab6-kill-history-1.json
20:   "inject_at": "1793076214",
21:   "landed_at": "1793076219",
24:   "time_to_recover_s": "5",
26:   "history_shards_ready_s": 0.5,
97:     "during": 545,
98:     "drain": 50,
161:   "result": "ADVANCE",
```

```quote faults/results/raun-lab6-kill-history-2.json
26:   "history_shards_ready_s": 0.5,
97:     "during": 545,
98:     "drain": 51,
```

```quote faults/results/raun-lab6-kill-history-3.json
26:   "history_shards_ready_s": 0.6,
97:     "during": 551,
98:     "drain": 44,
```

Run it again, on a compose pool on the lab host: `bin/laug raun kill-history -run-id <id>`

## 5. Persistence resets, six rauns

`faults/results/raun-lab2-persistence-errors-{1,2,3,t010,t030,t050}.json`: resets at a toxicity of 0.30 or less never
reach Temporal: its layers retry them away; at 0.50 the signal lands 95 s in and the raun ADVANCEs.

```quote faults/results/raun-lab2-persistence-errors-1.json
100:   "result": "BLOCK",
101:   "reason": "did not land: signal custom stayed at 0 over 120s"
```

```quote faults/results/raun-lab2-persistence-errors-t010.json
101:   "reason": "did not land: signal custom stayed at 0 over 120s"
```

```quote faults/results/raun-lab2-persistence-errors-t030.json
101:   "reason": "did not land: signal custom stayed at 0 over 120s"
```

```quote faults/results/raun-lab2-persistence-errors-t050.json
20:   "inject_at": "1792776365",
21:   "landed_at": "1792776460",
146:   "result": "ADVANCE",
```

Run it again, on a compose pool on the lab host: `bin/laug raun persistence-errors -run-id <id>`

## 6. Persistence latency, three rauns

`faults/results/raun-lab2-persistence-latency-{1,2,3}.json`: a raun ADVANCEd while 540 starts failed: why a raun counts
failed starts beside its verdict.

```quote faults/results/raun-lab2-persistence-latency-1.json
20:   "inject_at": "1792772697",
21:   "landed_at": "1792772707",
24:   "time_to_recover_s": "25",
95:   "iterations_failed": {
96:     "before": 0,
97:     "during": 540,
98:     "drain": 6,
161:   "result": "ADVANCE",
```

Run it again, on a compose pool on the lab host: `bin/laug raun persistence-latency -run-id <id>`

## 7. Two releases, six loads

`load/results/lab1-v13{10,21}-{1,2,3}{,.baseline,.verdict}.json`: 1.31.0 records an activity schedule-to-start p99 near
10 ms; 1.32.1 records none at all, and each of its verdicts reads ADVANCE with that row "not produced on this release
(load/releases/1.32.yaml)".

```quote load/results/lab1-v1310-1.json
7:   "server_tag": "1.31.0",
```

```quote load/results/lab1-v1310-1.baseline.json
3:   "activity_schedule_to_start_p99_ms": 9.993375206402114,
9:   "activity_schedule_to_start_over_10ms": 0.008777943823953582,
```

```quote load/results/lab1-v1310-2.baseline.json
3:   "activity_schedule_to_start_p99_ms": 9.985751942031149,
```

```quote load/results/lab1-v1310-3.baseline.json
3:   "activity_schedule_to_start_p99_ms": 9.983559421585808,
```

```quote load/results/lab1-v1321-1.json
7:   "server_tag": "1.32.1",
```

```quote load/results/lab1-v1321-1.baseline.json
3:   "activity_schedule_to_start_p99_ms": null,
9:   "activity_schedule_to_start_over_10ms": null,
```

Run it again, on a leased cell of the release: `bin/laug load -duration 5m -rate 20 -max-concurrent 50 -run-id <id>`

## 8. Boot 1

`cells/results/2026-10-23-boot1.txt`: a cell from nothing, the replay fixture recorded live, readiness, the pool and
loads on two releases, in one session.

```quote cells/results/2026-10-23-boot1.txt
3: an m8i.8xlarge (32 vCPU, 128 GiB; bin/laug host type read 32 vCPU and 123 GiB, H2) on demand, up 41.83 s when the
82: == A1: a cell from nothing, 06:33:16 to 06:35:20
90:   A1c bin/laug cell up                    SERVING after 44 s; the command's wall 43.6 s (steps.tsv), pulls included;
91:                                           8 of 8 images present after (steps/A1c.txt holds compose's pull lines)
95:   A1e bin/laug cell health x9             completed 1119, 1110, 1095, 1090, 1108, 1131, 1121, 1108, 1109 ms; found by
97:   A1f bin/laug cell down -remove-volumes  13 containers, 2 volumes, 1 network went, 0 left, 16.5 s
```

Run its first step again: `bin/laug cell up -server-tag 1.31.0`

## 9. Boot 2

`cells/results/2026-10-23-boot2.txt`: the fault catalog on compose cells; resets at 0.1 and 0.3 fail no start, and 0.5
lands late with 20 completions a second throughout.

```quote cells/results/2026-10-23-boot2.txt
3: started at 16:11:11. Host: the lab host, an m8i.8xlarge (32 vCPU, 128 GiB; bin/laug host type read 32 vCPU and 123
201: == R4: persistence-errors at 0.1, 0.3 and 0.5, with the sampler, 17:15:50 to 17:30:32
205:   persistence-errors-t010  17:17:37  BLOCK, "did not land: signal custom stayed at 0 over 120s"; failed 0/0
206:   persistence-errors-t030  17:21:51  BLOCK, the same; failed 0/0; waited 1 s for a quiet pool (.pool_quiet_wait_s)
207:   persistence-errors-t050  17:26:05  ADVANCE: landed 95 s, peak 1.498/s against 0.5, removed 120 s after inject, ttr 0
237: Shows: at 0.1 and 0.3, what R3 showed at 0.05; at 0.5, an ADVANCE with no failed start in any window and 20 completions
238: a second in every sample of the hold.
```

Run one of its rauns again: `bin/laug raun persistence-errors -run-id <id>`

## 10. Boot 4

`cells/results/2026-10-24-boot4.txt`: the gate as a Temporal workflow survives a worker KILLed 60 s into its wave: the
new worker replays it to ADVANCE, its verdict's bytes equal to its own rejudge and its signature equal to G1's, a gate
never killed.

```quote cells/results/2026-10-24-boot4.txt
341: bin/laug gate run 1.31.0 -workflow; at the wave mark, 09:55:17.285, plus 60.06 s, at 09:56:17.347, KILL to the worker's
342: process group (kill_G4; times computed from its Unix seconds). A new worker logged "Started Worker" at 09:56:47.461,
345: 1 open gate(s) on laug-gate-95f69246 on this build: gate-g20261024-095106" (steps/G4-worker.txt).
346: ADVANCE, exit 0 in 678.2 s, handed on 612.8 s, S1 byte for byte (8262 bytes, sha256 bd6b739ef49b) and identical on
360: Shows: a killed worker costs a gate nothing it records: G4 and G5 handed on 612.8 and 614.2 s, against G1's 612.5 and
361: G2's 614.3, with the signature of G1.
685: - Controls: G3 BLOCK regression, as its diff says; C1 and C2 BLOCK refused, as theirs says.
```

Run its gate again: `bin/laug gate run 1.31.0 -workflow`

## 11. Boot 6

`cells/results/2026-10-27-boot6.txt`: the runbooks checked by running them, kill-history's drain counted by the second,
and 27 rauns of server fault injection.

```quote cells/results/2026-10-27-boot6.txt
289: == A4: stop 4's drain timing, a warm-up raun, then kill-history three times, 04:35:03 to 05:00:49
301:   run  inject    landed  hold  shards  ttr  failed b/d/dr/a  drain s  sweep  s
302:   1    04:43:34  5       120   0.5     5    0/545/50/0        35       3625   379.5
303:   2    04:49:53  6       120   0.5     10   0/545/51/0        40       3673   383.8
304:   3    04:56:17  5       121   0.6     5    0/551/44/0        35       3526   378.9
319: Shows: on the lab host every start that failed after the restart failed within 11 s of it, and 43 to 49 of them in its
320: first 4 seconds. The bursts are the hold's: 49 every 10 s, with 50 starts in flight at most (-max-concurrent 50,
```

Run one of A4's rauns again, on a compose pool on the lab host: `bin/laug raun kill-history -run-id <id>`

## 12. Boot 9

`cells/results/2026-10-29-boot9.txt`: the microVM backend's whole life: a cold boot's HealthCheck passes 25.37 to 27.36
s from compose up (K1 to K3), and a restore from a snapshot is warm in 4.60 to 4.72 s.

```quote cells/results/2026-10-29-boot9.txt
127:                                      K1       K2       K3       A          batch E K1 to K3
136:     HealthCheck done                 27.36    25.51    25.37    25.62      25.31 to 27.52
157: == R: twelve restores, 04:26:52 to 04:27:57
162:   n   disk  load  ssh   behind  set   thaw  thawed at  after set  round trip  health  ns    shards  HC done  HC call
163:   1   0.01  0.01  0.41  35.2    0.85  1.27  2.35       -0.114     0.214       2.42    2.47  3.01    4.71     1543 ms
174:   12  0.01  0.01  0.39  94.5    0.84  1.26  2.32       -0.100     0.216       2.41    2.46  3.02    4.72     1544 ms
179: Shows: a restore from a frozen Unsafe snapshot reaches warm in 4.60 to 4.72 s, every first HealthCheck passing, against
529: So 18 of the 19 rows have counted microVM rauns: 52 here, 12 ADVANCE in 9 rows, 2 did not land, 38 never steady before
530: any fault, 0 harness failures. With batch E's, 11 rows have a microVM ADVANCE and 13 a microVM raun that reached its
```

Run its restores again, as root on a host with /dev/kvm, after fc-image and fc-snapshot: `make fc-restore RESTORES=12`

## 13. Batch E

`cells/results/2026-10-24-batchE.txt`: a durable timer across a 120.9 s step back of a microVM guest's clock fires 121.2
s late by host time, while the server records 60.0 s.

```quote cells/results/2026-10-24-batchE.txt
425: == T: timers across a guest clock step, 17:32:55 to 17:39:19
432:   T2, a step: 60 s, armed at 17:33:56.016. At +5.21 s the guest's clock set to the host's less 120.9 s, never corrected
434:   host time: late 121.163 s; the server's view 60.021 s (steps/T2.txt). The guest was set to the host's time 0.048 s
447: Shows: on a microVM cell from this batch's image, a timer armed before a 120.9 s step back of the guest's clock fired
448: 121.2 s late by host time while the server recorded 60.0 s. Setting the clock right did not fire the overdue timers
449: at once: all three fired together 40.4 s after the set, 130.9 s by the server's clock.
```

Run one timer again, on a microVM lease: `bin/laug health timer 60s`

## 14. One frontend

`cells/results/2026-11-02-frontend1.txt`, on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), server 1.31.0: with one
frontend on the default port, 20 cells logged no version mismatch; with two, 3 of 20 lost a frontend to a version
mismatch (one-sided Fisher exact p 0.1154).

```quote cells/results/2026-11-02-frontend1.txt
1: laug · the startup race with one frontend: two frontends against one, both in the role order before the fix
7: B: one-frontend.yml, the same order with one frontend, on 7233, the address history, matching and the worker write.
8: Reading rule, fixed before the run: if A loses no cell, the race did not reproduce and B's count says nothing.
57: A: 20 cells; 3 with a frontend that exited, 3 with any role restarted or not running; mismatch lines in 3 (15 lines); create failed in 0; healthy 20 of 20 within the cap; to healthy 2.9 to 62.3 s, median 2.9 s; from create began 13.3 to 72.8 s, median 13.4 s
58: B: 20 cells; 0 with a frontend that exited, 0 with any role restarted or not running; mismatch lines in 0 (0 lines); create failed in 0; healthy 20 of 20 within the cap; to healthy 2.9 to 2.9 s, median 2.9 s; from create began 12.3 to 13.4 s, median 13.3 s
59: Reading: A lost 3 of 20, B 0 of 20; one-sided Fisher exact p = 0.1154 that B loses less often than A.
```

No command in this tree re-runs either arm: it does not hold their compose files.

Make one cell of the order that replaced them, two frontends started one at a time: `bin/laug cell up`

## 15. The judge against the shell judges

`cells/results/2026-11-04-judge-shell-proof.txt`, on the dev host, before the shell judges were deleted: the Go judge
and the shell judges judged the 158 committed gate runs again and agreed on every run, 156 identical, and 2 trap
verdicts that the Go judge renders again and the shell side calls known. The 158 are the lab tree's, kept outside
this one; a clone holds the 3 runs kept in `gate/results/` (`gate/results/README.md`).

```quote cells/results/2026-11-04-judge-shell-proof.txt
57: g20261017-014142  re-rendered: trap T2
58: g20261017-014632  re-rendered: trap T2
172: laug · rejudge, 158 runs: 156 identical, 0 known, 2 re-rendered, 0 unexplained, 0 not judged
173: gate/compare.sh --rejudge-all, 158 runs: 156 identical, 2 known, 0 unexplained, 0 not judged
174: laug and gate/compare.sh agree on every run: 156 identical both ways, and 0 known and 2 re-rendered here are its 2 known
```

Judge again in Go every run a clone holds, the 3 kept here: `bin/laug gate rejudge -all`

## Cited paths the tree no longer has

Each path below is cited by a record above and is not in the tree, with why; `TestCitedPaths`, in
`make test-go`, fails any other cited path the tree lacks.

| path | why |
| --- | --- |
| check/allow.txt | a private shape scan's allow list, which this tree does not carry |
| gate/combine.sh | the shell judge that a run from before `bin/laug judge` names in its inputs, with its sha256. It was deleted after the Go judge matched the shell judges on every committed run; `internal/judge/versions.txt` names the version of each sha256 |
| gate/compare.sh | the shell judges' rejudge script, deleted after the Go judge matched them on every committed run (section 15) |
| gate/judges | the shell judges' archived versions, deleted after the Go judge matched them on every committed run (section 15) |
| gate/tolerance.sh | the shell judges' tolerance script, deleted after the Go judge matched them on every committed run (section 15) |
| health/testdata/healthcheck_history.json | the fixture's path before health moved under internal/; now `internal/health/testdata/healthcheck_history.json` |
| internal/internal_task_handlers.go | a file of the Go SDK, read for what a heartbeat does on a canceled context; never this tree's |
| load/releases/1.31.yaml | never a file: the thresholds note says server 1.31.0 has no release file, so the base set stood |
| internal/report/lessondata.go | the renderer's path before it moved to `_report/`, its own module; now `_report/lessondata.go` |
| internal/report/lessonpanels.go | the same move; now `_report/lessonpanels.go` |
| internal/report/records.go | the same move; now `_report/records.go` |
