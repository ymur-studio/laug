# follow test data

Files the tests copy to a temp dir. No test reads a host lock path, a laugd or Docker: a gate's laugd is a fake proc
tree in a temp dir, and a lock is a flock on a temp file.

| Path | What | Source |
| --- | --- | --- |
| `g20261017-020417/` | a gate directory: gate.json, plan.json, thresholds.json, clock.json, harness.json, timeline.jsonl (21 lines), poll.jsonl (71 polls), verdict.json, verdict.md, cleanup.json, and the items steady (lease, exit, run and verdict JSON, load.exit) and pl (lease, exit and raun JSON) | `gate/results/g20261017-020417` at this commit, byte for byte: a two-item gate on server 1.31.0 on the lab host, ADVANCE |
| `targets/cell-13880449.json`, `targets/cell-7ee6b0fa.json` | the federation target files follow writes for that gate's two leases: steady's on block 0, pl's on block 1, life from each lease's leased_at | written by hand, in the layout follow writes |
| `status/hand.json` | laugd's /status outside a gate: a busy, a warm and three leased cells, one of them a microVM's on 10.254.3.2, one past its expires_at; requesters of three forms, a name, a gate's and one with a slash and quotes | written by hand in internal/pool's statusView shape; ids and requesters made up |
| `status/reset.json` | the same leased cell after a reset: the same id and block, a new leased_at | written by hand |

The gate directory holds no item.log, since `*.log` stays out of the repository: TestItemLogMarks writes one in the
form faults/lib.sh's log() and faults/run.sh's messages give (TestItemLogLineForms pins both), with the times of the
raun JSON's stamps.
