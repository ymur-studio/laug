# Runbook 7: a lab batch

A batch is a list of laug commands run on the lab host, an m8i.8xlarge (32 vCPU, 128 GiB), in a fresh clone of the tree
under test, with the whole output of each command kept. Its records come back into this tree and are committed. Boots
1 to 4 and batch E were batches: `cells/results/2026-10-23-boot1.txt`, `cells/results/2026-10-23-boot2.txt`,
`docs/tables/records.jsonl#cells/results/2026-10-23-boot3.txt`, `cells/results/2026-10-24-boot4.txt` and
`cells/results/2026-10-24-batchE.txt`, each with its records in the directory of the same name. Those directories are
not in this tree: their readings are rows of `docs/tables/readings-<batch>.jsonl`, and each directory has its
row, such as `docs/tables/records.jsonl#cells/results/2026-10-23-boot1/`.

- **You need**: the lab host, and the batch's private parameters. These say where the host runs, how it is reached,
  and how the tree goes to it and the records come back. They live outside this tree, and this page names them only
  as that.
- **It takes**: `make test` alone took 307.3 s on boot 1 (`cells/results/2026-10-23-boot1.txt`:49). This page keeps
  boot 1's times because its steps are boot 1's; other pages give boot 4's 322.3 s
  (`cells/results/2026-10-24-boot4.txt`:168). The rest depends on the batch.
- **Where blocks run**: every block needs the lab host except the last, which runs here once the records are back. The
  `needs=lab` block held under `bin/laug runbook check -lab` in boot 6 (`cells/results/2026-10-27-boot6.txt`:94-118).

## 1. The host

Start the lab host by the private parameters, and put the tree under test on it as a fresh clone of that tree. Boot
1's clone held 4277 files, the same as the tree it cloned (`cells/results/2026-10-23-boot1.txt`, S1).

## 2. The start

These are the steps boot 1 began with: S2 to S7, H1 and H2 in its transcript. `-declare` writes the host declaration
that H2 read. Boot 1 ran no `-declare`, because its host held a declaration already; boot 4 ran it as H1.

```sh needs=lab
make build                                # S2
bin/laug upstream                         # S3
bin/laug deps -lab                        # S4: 0 of 17 checks missing on boot 1
bin/laug host type -declare m8i.8xlarge   # writes the declaration H2 read
bin/laug host clock -for 60s              # H1
make test                                 # S5: 307.3 s on boot 1
bin/laug version -json                    # S6: the commit
git status --porcelain                    # S7: a clean tree
```

On boot 1, `bin/laug deps -lab` found 0 of 17 checks missing, `make test` took 307.3 s, and `git status --porcelain`
showed a clean tree (`cells/results/2026-10-23-boot1.txt`:45-52).

## 3. The batch

Run the batch's own commands, each as runbooks [2](2-lease-and-load.md), [3](3-raun.md) and [4](4-gate.md) give them:
cells, loads and verdicts, rauns (laug's word for trials), and gates.

- Send each command's stdout and stderr to a file of its own, and record its exit and seconds in a log line. Boot 1
  kept 120 such files, in its directory's `steps/` (`cells/results/2026-10-23-boot1.txt`:23).
- A result lands at its tree path, as on any host: `load/results/`, `faults/results/` or `gate/results/`.
- A batch on microVM cells, as batch E was, needs `/dev/kvm`, and root for its Firecracker steps, such as the image,
  the snapshot and the pool with `-backend firecracker`, as `cells/rootfs/README.md` sets them up. A gate's own pool
  serves compose cells only, so a gate never runs on microVM cells.

## 4. The records

Before the records leave the host:

1. Check the clone's `git status`. It should list the batch's result files, untracked, and no other path (boot 1: 24
   files, `cells/results/2026-10-23-boot1.txt`:18-19).
2. Put the batch's own records (its log, its step files and its readings) beside its transcript, in a directory of the
   same name under `cells/results/`.
3. Scrub the host's paths and name from them: a path shows as `<clone>`, `<out>` or `<box tree>`, and the host's name
   as `<host>` (boot 1's transcript, its Files paragraph).
4. Bring the records back by the private parameters, then stop the host.

## 5. Back on this host

Judge every committed run again, the batch's among them. Expect 0 unexplained:

```sh
bin/laug gate rejudge -all
```

```text
laug · gate rejudge -all: every run in gate/results, judged again in Go under the judge version its gate.json ...
...
laug · rejudge, ... runs: ... 0 unexplained, 0 not judged
```

Then, in order:

1. `make docs`, which adds the batch's rows to `docs/tables/` and writes `docs/EVIDENCE.md` from them and the figures
   from the records.
2. `make test`.
3. A scan of every file git sees, tracked or untracked but not ignored, for what must not be committed: your own
   private words, upper or lower case, and shapes such as home paths, EC2 host names and ids, keys, IPv4 addresses,
   email addresses and time zones. `check/deny-shapes.txt` holds the shapes `bin/obs pack` refuses.
4. One commit for the records.
