# make -n goldens of the stack

What make printed for the stack's targets, make obs-up and its siblings, before laug had subcommands. Those targets
have left make. The goldens are bin/obs's expected `-dry-run` lines and refusal, with make's text kept as the record.
`_obs/cmd/obs/dryrun_test.go` reads every `.txt` file here, as `cmd/laug/dryrun_test.go` reads
`cmd/laug/testdata/dry-run`, whose README says how the goldens were captured. Each is one case: a header of
`# key: value` lines, then make's output as it printed it.

| Header | Says |
| --- | --- |
| `make` | the command the output came from: `make --no-print-directory -n <target> [NAME=value ...]` |
| `obs` | the bin/obs command that stands for it; the header said `laug` and laug's command until the verbs moved |
| `lease` | `absent`: no `.last-lease.json`, which no stack target reads |
| `file` | a file the checkout held: its path, then its content; one whose content starts `#!` is executable |
| `passes` | the make variables on the command line that reached the script only through make's environment, which `make -n` does not print; in internal/script's table order |
| `compare` | `line obs/obs.sh`: the -dry-run line is the command that runs the script, `passes` in front; `resolved obs/obs.sh $name=value`: the same, with the value the recipe's shell resolved; `refused`: make's recipe stops with exit 2, and bin/obs exits 2 having run nothing |

They were captured from the Makefile laug replaced, whose obs-* recipes are the ones the stack's verbs replace. make
built pool/cellstat and pool/follow before them, so the scratch directory held both, newer than an empty
pool/go.mod, and `make -n` printed no build. obs-down's recipe holds `$(MAKE)`, so make runs it under `-n` too: its
sub-make found pool/follow up to date and printed nothing, and it ran the scratch directory's `obs/obs.sh`, a stand-in
that exits 0 and prints nothing. Its `$build` was ok, so its goldens put a stand-in at `bin/obs`: bin/obs sets
`OBS_FOLLOW_BUILD=ok` when the checkout holds a built bin/obs, which obs/obs.sh asks whether a gate is open.
make ran the script as `obs/obs.sh`, and the goldens keep that word; it is `_obs/obs.sh` now, and the test puts
one in place of the other (`seedScript` in `dryrun_test.go`). obs-watch has no golden: its recipe was a stub, and it
is gone. obs-test and obs-lint had goldens until make took them back as its own targets, make obs-test and make
lint.
