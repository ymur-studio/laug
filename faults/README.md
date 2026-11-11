# faults

`faults/catalog.yaml` holds one row per fault: its hypothesis, its signal, its recovery budget and the mechanisms that
inject and remove it. `faults/run.sh` reads it. `laug fault <fault>` runs one row on the leased cell; `laug raun
<fault>` leases a cell, runs the row, judges it and releases the cell. This page holds what the catalog does not: what
each row measured, and how a fault is added.

The catalog is a gate input: gate/run.sh records its sha256 in every gate.json, so each gate run names the catalog it
ran. A row's status lives here, so a new run changes no gate input.

## The three windows

A raun loads the cell at 20 workflows/s and reads three windows (faults/catalog.yaml:3-8):

- **before**: the last 60 s of 90 s of load. Every threshold of the cell's release is under its max, or the run is
  invalid, "never steady".
- **during**: the fault held, 120 s unless the row says otherwise. The fault's signal must exceed its max, or the run
  "did not land", which is never ADVANCE.
- **after**: removal to the signal back under its max is the time to recover, within `budget_s`. After a 30 s settle,
  90 s in which every threshold is under its max. The drain, removal to the after window, is recorded and not judged.

ADVANCE means the cell took the fault and came back. A BLOCK's reason names the window that failed.

## The catalog

Compose cells on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), unless a row says otherwise. Boot 2 ran the compose
rows (`cells/results/2026-10-23-boot2.txt`, sections R1 to R9). kill-history ran alone on the same host type, 3 rauns on
2026-10-17 and 3 in boot 6 (`docs/tables/quotes.jsonl#cells/results/2026-10-17-lab-kill-history.txt:2`;
`cells/results/2026-10-27-boot6.txt`, A4). The microVM runs, on the same host type, are batch E's, on warm leases
(`cells/results/2026-10-24-batchE.txt`, section F), boot 9's (`cells/results/2026-10-29-boot9.txt`, section F): 52 rauns
of 18 rows, frozen, cache_type Unsafe, io_engine Sync, each on a cold lease from a pool of one with its load about 5 s
after the create, and boot 10's disk-slow, 3 rauns on jailed microVM cells, frozen, Unsafe, Sync, each on a cold lease
left idle 120 s before its load (`docs/tables/quotes.jsonl#cells/results/2026-10-29-boot10.txt:493-496`). 38 of boot 9's
were never steady before any fault, 36 on frontend p99, so a row's boot 9 clause gives k of n and names its never-steady
rauns, which show nothing of the fault. The microVM runs of batch E and boot 9 replace those of the first lab host, an
m8i.2xlarge (8 vCPU, 32 GiB), whose rauns stay as that host's record, rows of `docs/tables/rauns.jsonl` (`raun-fcp*`,
`raun-fckill*`, `raun-fvthrottle*`, `raun-fwoom*` and the rest). Boot 6 ran the serverfi rows, wf-update-ownership-lost
to history-recreate, 3 rauns each on cold leases, after a warm-up raun that is not counted
(`cells/results/2026-10-27-boot6.txt`, AUD). Their first runs, on the first lab host, stand beside them in the table
(the rows of `raun-fi-*` in `docs/tables/rauns.jsonl`). serverfi recreates history with docker compose on the host for a
compose cell, and inside the guest over ssh for a microVM cell, after checking that the guest's laug-server.yaml is this
checkout's (faults/catalog.yaml:213-219; faults/run.sh:335-393). The guest path runs in `make harness-test` on a fake
guest, and ran on a real guest in boot 9's 6 serverfi ADVANCEs, history's shards ready 5.5 to 5.7 s after the recreate.
disk-slow runs on microVM cells only: its vm mechanism puts a token bucket on the guest's drive through laugd, which
`make harness-test` runs on a fake laugd. So all 19 rows ran on microVM cells and 12 ADVANCEd there; 5 have had no
steady microVM raun: stuck-shard-norestore, wf-update-execute-timeout, acquire-lost, persistence-chaos and
history-recreate.

Landed is inject to the signal over its max; recovered is removal to the signal back under it; failed counts failed
client starts. A raun the table names by its id alone, as `raun-lab9-persistence-latency-{1,2,3}`, is a row of
`docs/tables/rauns.jsonl` by that key; one it names as a file, as `raun-lab2-persistence-latency-{1,2,3}.json`, is kept
whole under faults/results/ as well ([docs/RECORDS.md](../docs/RECORDS.md)). Caught by: which of a verdict's rules the
during window trips, computed with jq from each JSON's `.during`, or each row's `.windows.during`, against
load/steady-state.yaml's six maxima as they stand (no data counts as over, as a verdict counts it); the floor, fewer
than 20 x (window - 10) workflows completed; failed, starts that failed in the window. A window under 60 s has no
readings (faults/run.sh:540).

| Fault | Mechanism | Signal | Budget s | Runs, host and result | Caught by |
| --- | --- | --- | --- | --- | --- |
| persistence-latency | toxic latency 200 ms, jitter 50 | persistence p99 over 23 ms | 60 | Compose: 3 of 3 ADVANCE, landed 5 to 10 s, recovered 25 s, failed 537 to 540 during and 0 after (`raun-lab2-persistence-latency-{1,2,3}.json`). microVM: 3 of 3 ADVANCE, landed 5 to 10 s, recovered 30 to 35 s, failed 541 to 546 during and 0 after (`raun-lab5-persistence-latency-{1,2,3}`). microVM, boot 9: 0 of 3, never steady on a microVM cell under the compose rule before any fault, frontend p99 36.06, 34.84 and 37.93 ms against 22 (`raun-lab9-persistence-latency-{1,2,3}`) | 6 of 6 thresholds; the floor, 0 to 11 completed against 2000 to 2100; failed |
| persistence-timeout | toxic timeout 5000 ms | persistence p99 over 23 ms | 120 | 3 of 3 ADVANCE, landed 10 s, recovered 30 s, failed 545 to 552 during, 24 to 26 in the drain, 0 after (`raun-lab2-persistence-timeout-{1,2,3}`). microVM, boot 9: 2 of 3 ADVANCE, landed 10 s, recovered 30 s, failed 550 and 553 during, 41 and 40 in the drain, 0 after; run 3 never steady, frontend p99 33.43 ms (`raun-lab9-persistence-timeout-{1,2,3}`) | 6 of 6; the floor, 0 completed; failed; the same on microVM |
| persistence-errors | toxic reset_peer, toxicity 0.05 | persistence errors over 0.5/s | 60 | At 0.05: 0 of 3, each BLOCK "did not land", the signal at 0 over 120 s, 0 failed (`raun-lab2-persistence-errors-{1,2,3}.json`). From catalog copies with only the toxicity changed: 0.1 and 0.3 the same, once each; 0.5 ADVANCE once, landed 95 s, recovered 0 s, 0 failed (`raun-lab2-persistence-errors-t{010,030,050}.json`). microVM, boot 9, at 0.05: 0 of 3. Runs 1 and 3 did not land, the signal at 0 over 120 s, 0 failed, as on compose; run 2 never steady, frontend p99 36.99 ms (`raun-lab9-persistence-errors-{1,2,3}`) | nothing: no during reading, and 0.5's window was 25 s; 0 failed |
| disk-slow | vm drive ops=100: 100 operations a second on the guest's one drive; microVM cells only | persistence p99 over 23 ms | 60 | microVM, boot 10, jailed, each on a lease idle 120 s: 3 of 3 ADVANCE, landed 10 s, recovered 30 to 31 s, failed 215, 53 and 14 during, 10, 0 and 0 in the drain, 0 after; the drive held at 100.0 to 100.1 requests a second while limited, about a quarter of its 407 to 417 before (`raun-lab10-disk-slow-{1,2,3}`; the drive's rates, `docs/tables/quotes.jsonl#cells/results/2026-10-29-boot10.txt:517-518`) | 6 of 6 thresholds; the floor, 1 to 239 completed against 2000; failed |
| kill-history | docker kill temporal-history, then docker start | 512 less history's shards, over 0 | 120 | Compose: 3 of 3 ADVANCE, landed 5 to 6 s, recovered 5 to 6 s, shards ready 0.5 s after the restart, failed 545 during, 63 to 97 in the drain and 0 after (`raun-lab-kill-history-{1,2,3}`); in boot 6, 3 of 3 ADVANCE, landed 5 to 6 s, recovered 5 to 10 s, shards ready 0.5 to 0.6 s, failed 545 to 551 during, 44 to 51 in the drain, the last in the 5th to 12th second after the restart, and 0 after (`raun-lab6-kill-history-{1,2,3}.json`; the seconds, `docs/tables/fig-kill-history.jsonl#raun-lab6-kill-history-1/` and its two beside it). microVM: 3 of 3 ADVANCE, landed 6 s, recovered 6 to 11 s, shards ready 2.6 to 3.0 s, failed 545 during and 0 after (`raun-lab5-kill-history-{1,2,3}`). microVM, boot 9: 0 of 3, never steady on a microVM cell under the compose rule before any fault, frontend p99 28.80 ms with end-to-end p95 84.35, then 24.90 and 36.57 ms (`raun-lab9-kill-history-{1,2,3}`) | 5 of 6, persistence p99 under; the floor, 0 completed; failed |
| throttle-frontend | dynconfig frontend.rps 10, then restore | ResourceExhausted over 0.5/s | 180 | Compose: 3 of 3 ADVANCE, landed 35 to 36 s, recovered 50 to 55 s, 0 failed, 176 to 182 completed in 84 to 85 s (`raun-lab2-throttle-frontend-{1,2,3}`). microVM: 2 of 3 ADVANCE, landed 26 to 41 s, recovered 60, 61 and 116 s; run 3 BLOCKed in its after window, end-to-end p95 85.1 ms against 84 (`raun-lab5-throttle-frontend-{1,2,3}`). microVM, boot 9: 2 of 3 ADVANCE, landed 56 s, recovered 76 s, 0 failed, 123 and 145 completed in 64 s; run 1 never steady, frontend p99 26.08 ms (`raun-lab9-throttle-frontend-{1,2,3}`) | 4 of 6, both schedule-to-start rows, end-to-end and the error rate; 5 of 6 with frontend p99 in 2 microVM runs; the floor, 151 to 196 completed against 1380 to 1680; on microVM in boot 9, the same 4 of 6 and the floor, 123 and 145 against 1080; 0 failed |
| oom-history | docker update --memory 256m; then 64g and docker start | history's up below 1 in the last 30 s | 120 | Compose: 3 of 3 ADVANCE, landed 45 to 50 s, recovered 60 to 61 s, shards ready 29.7 to 35.1 s after the limit lifted, failed 102 to 107 during, 153 to 206 in the drain, 0 after (`raun-lab2-oom-history-{1,2,3}`). microVM: 0 of 3. Runs 1 and 3 never steady, end-to-end p95 90.5 and 90.4 ms against 84 before any fault; run 2 landed 36 s, recovered 71 s, shards ready 40.6 s, its after window 89.8 ms (`raun-lab5-oom-history-{1,2,3}`). microVM, boot 9: 0 of 3, never steady on a microVM cell under the compose rule before any fault, frontend p99 27.81 and 25.85 ms, then end-to-end p95 84.23 ms against 84 (`raun-lab9-oom-history-{1,2,3}`) | 5 of 6 on compose, persistence p99 under; 6 of 6 on microVM run 2; the floor, 0 completed; failed |
| close-shards | tdbg close 64 1014, no restart | shards removed, over 63 since inject | 60 | 3 of 3 ADVANCE, landed 7 s, all 64 shards back 22 to 23 s after inject, inside the 90 s hold, 0 failed (`raun-lab2-close-shards-{1,2,3}`). microVM, boot 9: 1 of 3 ADVANCE, landed 9 s, all 64 shards back by 46 s after inject, 0 failed; runs 1 and 3 never steady, frontend p99 28.24 and 23.03 ms (`raun-lab9-close-shards-{1,2,3}`) | nothing: 0 of 6, 1600 to 1700 completed against a floor of 1460, 0 failed; the same on microVM, 1600 against 1420 |
| stuck-shard | pg bump seed:1014 1000, then pg restore | shard closes since inject, over 0 | 90 | 3 of 3 ADVANCE, shard 379 held; landed 20 to 30 s, recovered 21 to 26 s, failed 2 to 6 during and 0 after (`raun-lab2-stuck-shard-{1,2,3}`). microVM, boot 9: 1 of 3 ADVANCE, shard 379 held, landed 11 s, recovered 23 s, failed 6 during and 0 after; runs 1 and 2 never steady, end-to-end p95 84.27 ms with frontend p99 22.47, then frontend p99 29.93 ms (`raun-lab9-stuck-shard-{1,2,3}`) | failed only: 0 of 6, over the floor; the same on microVM, 6 failed, 2195 completed against 1980 |
| stuck-shard-norestore | pg bump seed:1014 1000; remove none; cleanup pg restore | shard closes since inject, over 0 | 90 | The harness's self-test. 1 run: BLOCK "did not recover", the self-test passed, shard 379 refused 5 acquires in the hold (`raun-lab2-stuck-shard-norestore-1`, `.self_test`). microVM, boot 9: 0 of 1, never steady on a microVM cell under the compose rule before any fault, frontend p99 30.37 ms against 22, so the self-test did not pass, "BLOCKed for another reason"; the mechanism has not run on a microVM cell (`raun-lab9-stuck-shard-norestore-1`, `.self_test`) | failed only, 3 during: 0 of 6, over the floor |
| wf-update-ownership-lost | serverfi ShardOwnershipLost on 1% of UpdateWorkflowExecution | the injected error, over 0/s | 90 | 3 of 3 ADVANCE, landed 10 to 13 s, recovered 26 to 28 s, 0 failed; 117 to 120 shard contexts closed in the hold (`raun-lab6-wf-update-ownership-lost-{1,2,3}`). First lab host: 3 of 3 ADVANCE, landed 12 to 18 s, recovered 26 to 27 s, 0 failed; 111 to 118 closed (`raun-fi-wf-update-ownership-lost-{1,2,3}`). microVM, boot 9: 1 of 3 ADVANCE, landed 17 s, recovered 25 s, 0 failed; runs 2 and 3 never steady, frontend p99 32.28 and 36.35 ms (`raun-lab9-wf-update-ownership-lost-{1,2,3}`) | the error rate only, on both hosts and on microVM |
| wf-update-deadline | serverfi DeadlineExceeded on 1% | the injected error, over 0/s | 90 | 3 of 3 ADVANCE, landed 11 to 13 s, recovered 23 to 28 s, 0 failed, no shard closed (`raun-lab6-wf-update-deadline-{1,2,3}`). First lab host: 3 of 3 ADVANCE, landed 10 to 13 s, recovered 28 s, 0 failed, no shard closed (`raun-fi-wf-update-deadline-{1,2,3}`). microVM, boot 9: 1 of 3 ADVANCE, landed 16 s, recovered 30 s, 0 failed; runs 2 and 3 never steady, frontend p99 32.33 ms, then 33.00 ms with end-to-end p95 84.29 (`raun-lab9-wf-update-deadline-{1,2,3}`) | the error rate only, on both hosts; end-to-end p95 and the error rate on microVM |
| wf-update-timeout | serverfi Timeout on 1%, before the write | the injected error, over 0/s | 90 | 3 of 3 ADVANCE, landed 13 s, recovered 28 s, 0 failed, no shard closed (`raun-lab6-wf-update-timeout-{1,2,3}`). First lab host: 2 of 2 ADVANCE, landed 13 to 18 s, recovered 28 s, 0 failed; run 3 BLOCKed never steady, on a cell slow before any fault (`raun-fi-wf-update-timeout-{1,2,3}`). microVM, boot 9: 2 of 3 ADVANCE, landed 21 and 17 s, recovered 30 and 25 s, 0 failed; run 3 never steady, frontend p99 26.47 ms (`raun-lab9-wf-update-timeout-{1,2,3}`) | the error rate, and end-to-end p95 in 2 of 3; on the first lab host the error rate only, in 2 runs; on microVM end-to-end p95 and the error rate, 2 of 2 |
| wf-update-execute-timeout | serverfi ExecuteAndTimeout on 1%: the write lands, then Timeout | the injected error, over 0/s | 90 | 3 of 3 ADVANCE, landed 11 to 13 s, recovered 27 to 28 s, 0 failed, no shard closed (`raun-lab6-wf-update-execute-timeout-{1,2,3}`). First lab host: 3 of 3 ADVANCE, landed 11 to 13 s, recovered 28 s, 0 failed, no shard closed (`raun-fi-wf-update-execute-timeout-{1,2,3}`). microVM, boot 9: 0 of 3, never steady on a microVM cell under the compose rule before any fault, frontend p99 33.85 ms, 30.88 ms with end-to-end p95 84.89, and 29.17 ms, against 22; the mechanism has not run on a microVM cell (`raun-lab9-wf-update-execute-timeout-{1,2,3}`) | the error rate only, on both hosts |
| wf-update-resource-exhausted | serverfi ResourceExhausted on 1% | the injected error, over 0/s | 90 | 3 of 3 ADVANCE, landed 16 to 18 s, recovered 27 to 28 s, 0 failed, no shard effect (`raun-lab6-wf-update-resource-exhausted-{1,2,3}`). First lab host: 3 of 3 ADVANCE, landed 12 to 18 s, recovered 27 to 28 s, 0 failed, no shard effect (`raun-fi-wf-update-resource-exhausted-{1,2,3}`). microVM, boot 9: 1 of 3 ADVANCE, landed 22 s, recovered 25 s, 0 failed, frontend p99 46.25 ms during; runs 1 and 2 never steady, frontend p99 31.52 and 36.79 ms (`raun-lab9-wf-update-resource-exhausted-{1,2,3}`) | 4 of 6, both schedule-to-start rows, end-to-end and the error rate; 5 of 6 with frontend p99 in run 3; the same on both hosts; 5 of 6, with frontend p99, on microVM; 0 failed |
| wf-update-unavailable | serverfi Unavailable on 1% | the injected error, over 0/s | 90 | 3 of 3 ADVANCE, landed 12 to 13 s, recovered 26 to 28 s, 0 failed, no shard effect (`raun-lab6-wf-update-unavailable-{1,2,3}`). First lab host: 3 of 3 ADVANCE, landed 12 to 13 s, recovered 25 to 28 s, 0 failed, no shard effect (`raun-fi-wf-update-unavailable-{1,2,3}`). microVM, boot 9: 1 of 3 ADVANCE, landed 17 s, recovered 25 s, 0 failed; runs 2 and 3 never steady, frontend p99 30.31 and 31.08 ms (`raun-lab9-wf-update-unavailable-{1,2,3}`) | activity schedule-to-start in 3 of 3, and in 2 of 3 on the first lab host; end-to-end p95 only on microVM; 0 failed |
| acquire-lost | serverfi ShardOwnershipLost on half of UpdateShard | the injected error, over 0/s | 90 | 3 of 3 ADVANCE, landed 13 s, recovered 28 s, failed 343 to 367 during and 0 after; 859 to 890 shard contexts closed (`raun-lab6-acquire-lost-{1,2,3}`). First lab host: 3 of 3 ADVANCE, landed 13 to 18 s, recovered 23 to 28 s, failed 338 to 368 during and 0 after; 799 to 886 closed (`raun-fi-acquire-lost-{1,2,3}`). microVM, boot 9: 0 of 3, never steady on a microVM cell under the compose rule before any fault, frontend p99 26.66, 24.21 and 29.19 ms against 22; the mechanism has not run on a microVM cell (`raun-lab9-acquire-lost-{1,2,3}`) | the error rate and frontend p99; the floor, 1726 to 1808 completed against 1940, and 1692 to 1757 against 1840 to 1940 on the first lab host; failed |
| persistence-chaos | serverfi profile=default: 15 errors at 1% on 6 ExecutionStore methods | Timeout and ResourceExhausted, over 0/s | 90 | 3 of 3 ADVANCE, landed 11 to 13 s, recovered 23 to 28 s, failed 50 to 51 during and 0 after (`raun-lab6-persistence-chaos-{1,2,3}`). First lab host: 3 of 3 ADVANCE, landed 12 to 13 s, recovered 27 to 28 s, failed 37 to 55 during and 0 after (`raun-fi-persistence-chaos-{1,2,3}`). microVM, boot 9: 0 of 3, never steady on a microVM cell under the compose rule before any fault, frontend p99 24.97 and 24.25 ms against 22, then end-to-end p95 85.00 ms against 84; the mechanism has not run on a microVM cell (`raun-lab9-persistence-chaos-{1,2,3}`) | 5 of 6, persistence p99 under; the floor in 3 of 3, 1879 to 1972 against 1940 to 1980, and in 1 of 3 on the first lab host, 1858 against 1940; failed |
| history-recreate | serverfi on a method history never calls, then off: two history restarts, no fault | resets of history's persistence counters, over 0 | 90 | 3 of 3 ADVANCE, the control; landed 5 to 8 s, recovered 1 to 3 s, 0 failed (`raun-lab6-history-recreate-{1,2,3}`). First lab host: 3 of 3 ADVANCE, landed 7 to 8 s, recovered 2 to 3 s, 0 failed (`raun-fi-history-recreate-{1,2,3}`). microVM, boot 9: 0 of 3, never steady on a microVM cell under the compose rule before any fault, frontend p99 34.22, 29.26 and 35.34 ms against 22; the mechanism has not run on a microVM cell (`raun-lab9-history-recreate-{1,2,3}`) | nothing: 0 of 6, over the floor, 0 failed, on both hosts |

What the catalog's comments cite, with the files:

- persistence-latency (faults/catalog.yaml:35): persistence requests 750.2/s before and 103.5/s during
  (`raun-lab1-pl-rates`, `.readings`; `cells/results/2026-10-23-boot1.txt`, R3); 0 workflows completed in each
  compose during window above.
- persistence-errors (faults/catalog.yaml:49, :53-56): at 0.05 a sampler beside each raun read 0 persistence errors, 0
  pool refresh attempts in 5 roles, 19.96 to 20.04 completions/s and 47 to 48 connections in every sample of each hold.
  At 0.5 history's refresh attempts jumped by about 1,600 to 1,700 within 20 s of the inject: attempts, refused ones
  included, not rebuilds. Its failures read 0 because that series first appeared in the burst, and increase() starts
  from a series' first sample, so the 0 says nothing about how many failed. Completions held at 20/s, and connections
  fell from 47 to 21 (the sampler's rows, `docs/tables/fig-toxic-fuse.jsonl#pe-persistence-errors-1.jsonl/` and its five
  beside it; boot 2, R3 and R4).
- throttle-frontend (faults/catalog.yaml:90-97): the 180 s budget came from the first lab host's microVM runs at 120 s,
  recovered 58, 123 and 123 s with 2 BLOCKed on the budget (`raun-fcthrottle{1,2,3}`). On the lab host the
  slowest recovery was 116 s.
- oom-history (faults/catalog.yaml:106-118): history's shards ready 29.7 to 35.1 s after the limit lifted on compose
  cells, and 40.6 s in the one microVM run that injected.
- serverfi (faults/catalog.yaml:213-216): the comment's kill-history reading, shards ready 0.5 s after the restart in 3
  of 3, is the 2026-10-17 runs' (`raun-lab-kill-history-{1,2,3}`). Boot 6's 3 read 0.5, 0.5 and 0.6 s
  (`raun-lab6-kill-history-{1,2,3}.json`).
- history-recreate (faults/catalog.yaml:319): the hypothesis says clients feel each restart for about 10 to 15 s, from
  kill-history. In boot 6, kill-history's last failed start after the restart fell in the 5th, 6th and 12th second
  after it, counted from its log by the second (`cells/results/2026-10-27-boot6.txt`, A4). history-recreate's own 6
  rauns, two restarts each, failed no start in any window (the table).

## Candidates

`faults/candidates.yaml` holds four dynamic config changes, each judged by a plain verdict over 5 m of load, not by the
three windows. `laug candidate <name>` loads the unchanged cell, makes the edit, waits two re-read periods, loads it
again and restores the file. PASS means the verdict is the row's `expect` and, for a BLOCK, a row of its `blocks_on`
decided it (faults/candidate.sh:37-40). Boot 3 ran each three times on 1.31.0 and two of them once on 1.32.1: 14 of 14
PASS (the rows of `candidate-<name>-lab3-<n>` in `docs/tables/rauns.jsonl`).

| Candidate | Change | Expect | Lab host |
| --- | --- | --- | --- |
| benign | frontend.rps 2400 to 2000 | ADVANCE | 3 of 3 PASS, 5940 to 5962 completed; on 1.32.1 PASS, 5922 |
| read-partitions | matching.numTaskqueueReadPartitions 4 to 1 | ADVANCE | 3 of 3 PASS, 5923 to 5962 completed |
| frontend-starved | frontend.rps 2400 to 5 | BLOCK on service_error_rate | 3 of 3 PASS, BLOCK on 5 rules, 283 to 290 completed |
| persistence-starved | history.persistenceMaxQPS 9000 to 50 | BLOCK on service_error_rate or omes_exit | 3 of 3 PASS, BLOCK on 6 rules, 210 to 212 completed; on 1.32.1 PASS, 235 |

## How a fault is added

1. Write its row in `faults/catalog.yaml`: a hypothesis that says what should happen and what it rests on; a signal,
   a threshold's name or a `signal_query` with a `signal_max`; a `budget_s`; and `inject` and `remove` in the
   mechanisms `faults/run.sh` knows: toxic, docker, dynconfig, tdbg, pg, serverfi, vm or none. vm is `vm drive
   ops=<n> [bw=<bytes>]` and `vm drive off`, on microVM cells only, so its row adds `backend: firecracker`. A shard
   fault adds `signal_base`, `recover`, `recover_from` and `cleanup`; a self-test of the harness adds `self_test`. The
   catalog's header and `faults/run.sh`'s say what each field does.
2. Run `make test`. The harness holds every row's mechanisms to the verbs `faults/run.sh` knows and runs each serverfi
   row through its checks. For a serverfi row, `laug cell fi-template-check -server-tag <tag>` also checks its store
   and method against the methods the server's wrapper draws for at that tag: a misspelled method injects nothing.
3. Run it once by hand: `laug fault <fault> -dry-run` prints the line it runs, and with a pool serving compose cells
   (`laug pool serve -backend compose`), `laug raun <fault>` leases a cell, runs the three windows, judges and releases.
   Its raun JSON lands in `faults/results/`.
4. Run it three times on cold leases: three `laug raun <fault>` in a row against `laug pool serve -backend compose
   -min 0`. A fault counts as measured only when the three give the same shape of result: landed, recovered within
   `budget_s`, a clean after window, or the same BLOCK. When they differ, the difference is the finding:
   throttle-frontend's first three moved its budget from 120 s to 180 s.
5. Commit the raun JSONs and the transcript, then write the row's status in the table above: the runs, the host, the
   result and the files, and what its during windows caught.

## Reading a raun JSON

`laug raun` writes `faults/results/raun-<run id>.json`; `raun-lab2-persistence-latency-1.json` is one. Its keys:

- `result` and `reason`: ADVANCE or BLOCK, and for a BLOCK the window and the reading that decided it.
- `backend`, `firecracker_version`, `cache_type`, `host_virtualized`: the lease's backend, compose or firecracker; a
  microVM's snapshot values, null on compose; and whether laug itself ran on a VM, where a microVM cell runs nested.
- `inject_at`, `landed_at`, `remove_at`, `recovered_at`, `after_start`, `after_end`: Unix seconds, as strings. Landed
  is `landed_at` less `inject_at`.
- `time_to_recover_s`, `signal_peak`, `signal_max`: removal to recovered; the higher of two reads of the signal, at
  landing and at the hold's end; the max it is judged against.
- `before`, `during`, `drain`, `after`: each window's six thresholds, its shares over a bucket edge,
  `workflows_completed` and `window_seconds`; `{}` when the window never opened or was under 60 s.
- `iterations_failed`: failed client starts by window. `softasserts`: the server's soft assertions per role and window.
  `sweep`: the workflows counted by status after the run.
- `history_shards_ready_s` on the history rows: removal to the new process's "initial shards ready" line.
  `shards_acquired` on the shard rows: the seconds each touched shard came back, and the held evidence. `self_test`:
  the self-test's verdict.
