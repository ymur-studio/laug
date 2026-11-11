# cellstat test data

Files the tests copy to a temp dir and read through `-root`. The kernel files in the dev tree were read on the dev
host, a Linux VM with 24 vCPU and 47 GiB running Docker, on a 6.6 kernel with cgroup v2, on 2026-10-16 at 01:30 and
02:20 UTC; those in the lab tree on the lab host (below).
Their contents are verbatim unless a row says edited. Container ids and pids are made up; so is every command line.

## dev/sys/fs/cgroup

| Dir | What | Source |
| --- | --- | --- |
| `docker/0123456789abcdef...` (A) | a full container group, 13 files, memory.current and memory.peak included | one compose container's group; its cgroup.events from another container's, same day |
| `system.slice/docker-bbbb....scope` (B) | the same 13 files in the systemd layout | a second compose container's group, moved under the other path |
| `docker/cccc...` (C) | a group with no controllers: cpu.stat with 3 keys, 3 pressure files, cgroup.events, an empty cgroup.controllers | a systemd service's subgroup whose parent delegates none |
| `docker/dddd...` (D) | a full container group with an empty io.stat and io and memory pressure totals of 0 | a compose container that had done no block IO |
| `docker/eeee...` (K) | io.stat alone: 31 lines, 22 of them `MAJ:MIN` with no keys | the cgroup root's io.stat |

## dev/proc

| File | What | Source |
| --- | --- | --- |
| `stat`, `schedstat`, `uptime` | the host's files, whole: 24 cpuN lines; schedstat version 15 with 9 numbers a cpuN line | read as is |
| `pressure/{cpu,io,memory}` | host PSI; cpu full total=0 | read as is |
| `101`, `202`, `303`, `404`, `505` `/cgroup` | the `0::` line of containers A, B, C, D and K | written in the seen form, `0::/<path>` |
| `4240` | the `go run` parent of the Omes client: argv `go run ./cmd/omes run-scenario-with-worker ...` | argv from load/run-omes.sh; one thread line from another process; utime 9 and stime 4 set by hand |
| `4241` | the Omes client: the built binary with the same arguments, `--run-id g20261015-144517-s1`; 4 threads | the 4 thread lines and utime 300, stime 333 from one 4-thread process |
| `4242` | the Omes worker: `worker --task-queue omes-g20261015-144517-s1`, then each passed-through flag as one `--name=value` arg, the shape of omes f0de206 internal/workerctl/run.go:127-139, read in its source and not run; 2 threads; `schedstat` is the leader alone | thread lines from a daemon's leader and one sibling; utime 412 and stime 615 set by hand to 1027 ticks |

Each `stat` is one process's line, 52 fields as seen on this kernel, edited: pid, name, parent, utime, stime and
thread count.

## lab

One compose container's group read on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB) on demand, with cgroup v2, on
2026-10-17 between 00:48 and 00:49 UTC, during the cellstat batch (cells/results/2026-10-17-lab-cell-usage.txt).
Test 1 reads it by the pid route.

| File | What | Source |
| --- | --- | --- |
| `sys/fs/cgroup/system.slice/docker-fedcba98....scope/` | 13 files, memory.current and memory.peak included: cpu.stat with 10 keys (the dev host's 8), memory.events with 7 (6), memory.stat with 70 lines (49), 9 controllers (7), an empty io.stat, `max 100000` and `max` | a temporal-history container's group, at the path its `0::` line named on the lab host, the id made up |
| `proc/606/cgroup` | that container's `0::` line | written in the seen form, `0::/<path>`, with the made-up id |

## gate

g20261015-144517 from gate/results, trimmed: `timeline.jsonl` whole (wave 1792075700.573, wave end 1792076002.483,
301.9 s), `verdict.json` cut to the gate id and verdict and each item's key, kind, observed verdict, run id and cell,
and `s1` to `s3/lease.json` cut to id, project and block.

## fake-docker.sh

Stands in for the docker CLI in tests 13, 14, 16 and 17. It answers `ps` and `inspect` from files in `$FAKE_DOCKER`,
at once, after a delay or never; its header says which.

## metrics.txt

The body `GET /metrics` renders from one sample of the dev tree: containers A, B, C and D, one container of another
project and the two Omes processes, with test 1's fixed clock and own CPU. TestMetricsNames compares the body with it
byte for byte, so the family names are fixed here; a change to a name changes this file in the same commit. The
`-pids` tests make their record tree in a temp dir: every process, record and boot id in it is made up.
