# gateopen test data

Files the tests copy to a temp dir. No test reads a host lock path or a laugd: a gate's laugd is a fake proc tree in
a temp dir, and a lock is a flock on a temp file.

| Path | What | Source |
| --- | --- | --- |
| `g20261017-020417/gate.json`, `g20261017-020417/timeline.jsonl` | the two files of a gate directory GateOpen reads: gate.json, which marks the directory a gate's, and the timeline (21 lines), whose done line ends the gate | `gate/results/g20261017-020417` at this commit, byte for byte: a two-item gate on server 1.31.0 on the lab host, ADVANCE |
