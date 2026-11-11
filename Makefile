# laug's Makefile. Three entry points: bin/laug runs laug (the pool, cells, loads, faults, verdicts and gates; bin/laug
# help lists its commands), bin/obs, _obs's own binary, runs the stack, and make builds, checks and documents the tree.
# A make target never does what a subcommand does. Two exceptions: a gate's R1 step runs make harness-test
# (gate/lib.sh), as the judge's reason text names it, and make docs runs the renderer, its own module under _report (go
# -C _report run ./cmd/report -docs), which writes the committed documents and adds the rows docs/tables lacks. The
# Firecracker image targets stay here, since they need root and Docker; laug runs the pool on their cells.

SHELL := /bin/bash
# A recipe that pipes into grep or jq exits with the last command's status unless pipefail is on.
.SHELLFLAGS := -o pipefail -c
.DEFAULT_GOAL := help

# Values from the command line and the environment. make expands a variable at each use. Unguarded, a $(shell ...) in a
# value given on the command line runs before any check sees it. A ' in one pasted between quotes ends the recipe's
# quote, so the shell runs the rest. make also expands every command-line variable it exports to a recipe, used there or
# not (GNU Make 4.3). So every variable given on the command line, and each of TYPED_VARS that came from the
# environment, is taken as typed: $(value), once, into a simple variable that make never expands again, and exported.
# TYPED_VARS names the knobs the Firecracker targets read, which run as root. A recipe hands a value to the shell
# through shq: one shell word in single quotes, each ' in it written '\'' and each newline '$$'\n''. A number a recipe
# pastes bare is checked first by CHECK_VARS.
TYPED_VARS      := RESTORES SERVER_TAG
typed            = $(eval override $(1) := $$(value $(1)))$(eval export $(1))
$(foreach v,$(.VARIABLES),$(if $(filter command%,$(origin $(v))),$(call typed,$(v))))
$(foreach v,$(TYPED_VARS),$(if $(filter environment%,$(origin $(v))),$(call typed,$(v))))
define NEWLINE


endef
shq              = '$(subst $(NEWLINE),'$$'\n'',$(subst ','\'',$(1)))'
NUM_VARS        := RESTORES
check_var        = $(if $(filter override,$(origin $(1))),[[ $(call shq,$($(1))) =~ $(2) ]] || { echo "laug · make $@: $(1)="$(call shq,$($(1)))" is not $(3)" >&2; exit 2; };)
CHECK_VARS       = $(foreach v,$(NUM_VARS),$(call check_var,$(v),^[0-9]+$$,a whole number))

# The temporalio/server tag a compose cell runs by default (cells/compose/cell.yml, laug's -server-tag). microVM cells
# run the tag their image baked, this one; harness-test holds it to laug's, cell.yml's and cells/backend-compose.sh's.
SERVER_TAG_DEFAULT := 1.31.0
SERVER_TAG      ?= $(SERVER_TAG_DEFAULT)

.PHONY: help build test test-go harness-test obs-test lint docs clean fc-image fc-snapshot fc-restore

# help prints each target with a ## line, its name padded to 12; make lint fails a line past 79 columns. lint prints
# the text through HELP_TEXT, not a make of its own, which make -n would run and hand its -n.
HELP_TEXT        = printf '\n  laug  ·  ephemeral Temporal cells from a warm pool, for release validation\n\n' && \
                   grep -E '^[a-z][a-z-]*:.*\#\# ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*\#\# "}; {printf "  %-12s %s\n", $$1, $$2}' && \
                   printf '\n  bin/laug runs laug: make build, then bin/laug help lists its commands.\n\n'
help: ## list these targets
	@$(HELP_TEXT)

# bin/laug, the command line (cmd/laug and internal/), the Temporal SDK's roles among it. go build relinks only what
# changed. The commit reaches it through -ldflags: HEAD, with -dirty when git status prints anything, untracked files
# included; dev outside a git checkout. laug orchestrator worker runs as laug-worker@<that commit>. Every script runs
# bin/laug by a verb, and make links no other name to it: the gate drivers run laug pool serve and the cell scripts
# laug health check. bin/laug is also a file that make builds when a Go file under cmd/laug or internal/ is newer, or
# internal/judge/versions.txt, which internal/judge's identity covers with its .go files and internal/jsonx's, the
# tests left out; harness-test builds it so.
LAUG_GO         := $(shell find cmd/laug internal -name '*.go' ! -name '*_test.go' 2>/dev/null) \
                   internal/judge/versions.txt
LAUG_BUILD       = rev=$$(git rev-parse HEAD 2>/dev/null || echo dev) && { [ -z "$$(git status --porcelain 2>/dev/null)" ] || rev=$$rev-dirty; } && \
                   echo "go build -ldflags '-X main.commit=$$rev' -o bin/laug ./cmd/laug" && \
                   go build -ldflags "-X main.commit=$$rev" -o bin/laug ./cmd/laug
# bin/obs, the stack's verbs (_obs/cmd/obs), _obs's own module, which imports internal/ through its replace ../:
# _obs/obs.sh runs bin/obs follow, and copies bin/obs as the stack's cellstat and follow. It is a file make builds when
# a Go file under _obs or internal/ is newer; obs-test builds it so.
OBS_GO          := $(shell find _obs internal -name '*.go' ! -name '*_test.go' 2>/dev/null)
OBS_BUILD        = echo "go -C _obs build -o ../bin/obs ./cmd/obs" && go -C _obs build -o ../bin/obs ./cmd/obs
# Go first: on PATH, and at least go.mod's go line, or a GOTOOLCHAIN that downloads it (laug deps checks the same). go
# env runs in / so it names the installed Go: in the checkout, GOTOOLCHAIN=auto would switch to go.mod's version first.
GO_CHECK         = command -v go >/dev/null 2>&1 || { echo "laug · make build: no go on PATH; laug needs go.mod's go line, go $$(awk '$$1 == "go" { print $$2; exit }' go.mod)" >&2; exit 2; }; \
                   need=$$(awk '$$1 == "go" { print $$2; exit }' go.mod); have=$$(cd / && go env GOVERSION); have=$${have\#go}; tc=$$(cd / && go env GOTOOLCHAIN); \
                   if [ "$$(printf '%s\n%s\n' "$$need" "$$have" | sort -V | head -1)" != "$$need" ]; then case "$$tc" in \
                     auto|*+auto) echo "laug · make build: go $$have; go.mod needs $$need, which GOTOOLCHAIN=$$tc fetches on a first build" ;; \
                     *) echo "laug · make build: go $$have; go.mod needs $$need, and GOTOOLCHAIN=$$tc downloads none" >&2; exit 2 ;; \
                   esac; fi
build: ## bin/laug and bin/obs; checks Go first
	@$(GO_CHECK)
	@$(LAUG_BUILD)
	@$(OBS_BUILD)
	@echo "laug · make build: bin/laug, bin/obs"
bin/laug: $(LAUG_GO) go.mod go.sum
	@$(LAUG_BUILD) && touch $@
bin/obs: $(OBS_GO) _obs/go.mod go.mod
	@$(OBS_BUILD) && touch $@

# The test tiers. make test runs the ones that need no Docker, no cell and no network, the set to run before a commit:
# test-go, harness-test and lint. obs-test is the stack's own, about 12 minutes, and runs alone. make prints nothing
# when a target passes, and harness-test's tally sits lines above lint's, so make test's last line says it passed and
# how long it took, from TEST_T0, read once as make reads this file.
test: TEST_T0 := $(shell date +%s)
test: test-go harness-test lint ## test-go, harness-test and lint: the pre-commit set, no Docker
	@echo "laug · make test: passed, test-go, harness-test and lint, in $$(( $$(date +%s) - $(TEST_T0) )) s"

# The SDK reads TEMPORAL_SDK_FLAG_<n> and TEMPORAL_DEBUG at init, before main can unset them (SDK v1.49.0
# internal_flags.go's init, internal_worker.go's debugMode), so the tests start without any (compgen -e lists
# the exported names). TEMPORAL_DEBUG lifts the deadlock detector's 1 s limit (seen in the SDK's test env). Every
# OTEL_* goes too: the OpenTelemetry SDK would read them into the orchestrator's spans (internal/orchestrator/trace.go).
# OBS_BIN goes, so _obs/follow's two fake cell tests skip here: make obs-test runs them.
ORCH_NOFLAGS       = env $$(compgen -e | sed -n -e 's/^TEMPORAL_SDK_FLAG_/-u &/p' -e 's/^TEMPORAL_DEBUG$$/-u &/p' -e 's/^OTEL_/-u &/p')
# Every package: the pool, the runner, the judge and bin/laug on fakes; internal/health's replay of the committed
# HealthCheck history, which fails with the activities swapped, and the nine changes to HealthCheck replayed
# against it; and internal/orchestrator's SDK test env, fake gate, six replay fixtures and the parity matrix, whose fake
# gate runs bin/laug pool serve on 127.0.0.1 ports 17890 to 17989. Then _report, the renderer, and _obs, the stack's
# verbs with cellstat and follow, each its own module, which ./... at the root leaves out: go -C runs their vet and
# their tests, _report's TestDocs among them, which holds the committed documents. follow's promtool test runs when
# PROMTOOL names one, and is skipped otherwise. GOTESTFLAGS goes on every go test line, empty by default: test.yml
# passes -race -timeout 30m, and make test takes it after the next lab boot re-measures.
GOTESTFLAGS     ?=
test-go: bin/laug bin/obs ## go vet and go test: pool, CLI, judge, replay, orchestrator
	@echo "go vet ./... && go test ./... -count=1 && go -C _report vet ./... && go -C _report test ./... -count=1 && go -C _obs vet ./... && go -C _obs test ./... -count=1$(if $(GOTESTFLAGS), with $(GOTESTFLAGS))"
	@$(ORCH_NOFLAGS) -u OBS_BIN go vet ./...
	@$(ORCH_NOFLAGS) -u OBS_BIN go test $(GOTESTFLAGS) ./... -count=1
	@go -C _report vet ./...
	@go -C _report test $(GOTESTFLAGS) ./... -count=1
	@go -C _obs vet ./...
	@env -u OBS_BIN go -C _obs test $(GOTESTFLAGS) ./... -count=1

# Offline: fixtures and fake tools in load/testdata (curl, temporal, go, git, date, sleep, docker, ssh, ss on PATH),
# so no cell, no Docker and no network; the fake docker also answers tdbg runs and psql for the shard faults, and
# docker compose for serverfi. The gate cases run bin/laug pool serve on a free 127.0.0.1 port with the fakes in
# load/testdata/gate, the only network they use. The last section re-judges every committed result with the
# lost-work floors. Its name stays: a gate's R1 runs it, and the judge's reason text names it.
harness-test: ## the script harness against fixtures, no Docker, about 5 min
	load/testdata/harness-test.sh

# _obs/obs.sh test (_obs/README.md): every config address 127.0.0.1, the configs validated, the board rules,
# each planted fault refused, then the fake cell on 127.0.0.1:7768 and 7769 beside a store under test on 7730, so the
# stack's store is down while it runs; refused while a gate is open.
obs-test: bin/obs ## the stack's own test, about 12 min; refused during a gate
	@_obs/obs.sh test

# gofmt over the Go files git sees, the three modules', go vet in each (./... at the root leaves _report and _obs out),
# _obs/lint.sh's board rules 1 to 10 over _obs/stack/ and the cells' gate.json (jq and yq), and make help's width.
# · is two bytes, so it counts as one column here.
lint: ## gofmt, go vet, the board rules and make help's width
	@out=$$(gofmt -l $$(git ls-files -co --exclude-standard -- '*.go')) || exit 2; \
	  [ -z "$$out" ] || { echo "laug · make lint: gofmt would change these:" >&2; echo "$$out" >&2; exit 1; }; \
	  echo "laug · make lint: gofmt: every Go file git sees is formatted"
	@go vet ./... && echo "laug · make lint: go vet ./...: no report"
	@go -C _report vet ./... && echo "laug · make lint: go -C _report vet ./...: no report"
	@go -C _obs vet ./... && echo "laug · make lint: go -C _obs vet ./...: no report"
	@_obs/lint.sh
	@{ $(HELP_TEXT); } | sed 's/·/./g' \
	  | awk 'length > 79 { print "laug · make lint: make help prints a line of " length " columns: " $$0 > "/dev/stderr"; bad = 1 } \
	         END { if (!bad) print "laug · make lint: make help: every line under 80 columns"; exit bad }'

# The committed documents, from the committed records: the rows docs/tables lacks, never changing or removing one;
# docs/EVIDENCE.md, a row a gate run, drawn from those tables; and the figures the pages show, docs/img/*.svg
# and their index. The renderer writes them, _report's cmd/report, its own module run by go -C, the rule's second
# exception above: _report's TestDocs, in test-go, fails a committed copy that is not what the renderer writes now, so
# a commit that adds runs, changes the judge or changes a figure's code or files runs make docs.
docs: ## docs/EVIDENCE.md, docs/img's figures, docs/tables' new rows
	go -C _report run ./cmd/report -docs

clean: ## remove bin/
	rm -rf bin

# Firecracker: a cell image, a warm cell snapshotted in a microVM, and timed restores (cells/rootfs/README.md).
# Linux x86_64 with /dev/kvm, as root: sudo make fc-image, then fc-snapshot, then fc-restore RESTORES=3. Build
# bin/laug first as yourself (make build): the scripts run its health check, and check for it and for root's tools.
# The pool on these cells is laug's: sudo bin/laug pool serve -backend firecracker, and sudo bin/laug pool down
# -backend firecracker -remove-cells.
RESTORES        ?= 1
# The image bakes cell.yml's default server tag and the guest starts the cell with it; SERVER_TAG is for compose cells.
FC_TAG_GUARD    = [ $(call shq,$(SERVER_TAG)) = $(call shq,$(SERVER_TAG_DEFAULT)) ] || { echo "laug · microVM cells run server $(SERVER_TAG_DEFAULT), the tag the image bakes; SERVER_TAG="$(call shq,$(SERVER_TAG))" is for compose cells (laug cell up, laug pool serve -server-tag)" >&2; exit 2; }
fc-image: ## build the microVM cell image (root, /dev/kvm)
	@$(FC_TAG_GUARD)
	cells/rootfs/build-image.sh

fc-snapshot: ## warm a cell in a microVM and snapshot it (root, /dev/kvm)
	@$(FC_TAG_GUARD)
	cells/rootfs/snapshot.sh

fc-restore: ## restore the snapshot RESTORES times, timed (root, /dev/kvm)
	@$(CHECK_VARS)
	cells/rootfs/restore.sh $(RESTORES)
