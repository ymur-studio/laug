// Command obs runs the observability stack, laug's receiver for what it emits: laug sends traces with -otlp and serves
// metrics on Prometheus endpoints, and the stack is one place on this host to receive, keep and read them. The gate
// never needs it. The tracing code stays in laug, under internal/orchestrator. make build builds this binary as
// bin/obs, from its own module under _obs, beside bin/laug; the main module never imports it or reads a file under
// _obs.
//
//	bin/obs <verb> [flags] [arguments]
//
// Its verbs are those bin/laug held as obs and usage, with the same flags and the same lines: up, down, status, find,
// backfill, follow, pack, view and purge, each a command of its own here, and usage record, sample, stop and report.
// Each line still starts "laug · obs <verb>: " or "laug · usage <verb>: ", and a line to type starts with bin/obs, the
// word this binary sets OBS to for _obs/obs.sh, which reads it as ${OBS:-bin/obs}. Its own lines, as laug's, follow
// cmd/laug/doc.go's rules: help asked for (help, -h, -help, --help, or a verb's -h) goes to stdout, exit 0; a mistake
// to stderr, exit 2, with the nearest names; a -h fits 80 columns; no line names the home path. The code it shares
// with bin/laug, the flags, the App and the script edge, is a copy of cmd/laug's, each file naming where it came from.
// It imports this module's _obs/follow and _obs/cellstat, and the main module's internal/script through the replace
// in its go.mod. Exit 0 done, 1 failed, 2 usage or refused; a verb that keeps another code names it in its paragraph
// below.
//
// The stack's verbs run the observability stack (_obs/README.md), as make obs-up and its siblings did: Prometheus,
// Jaeger, an OpenTelemetry Collector, cellstat's listener, follow and Grafana, plain processes on this host, every
// listener on 127.0.0.1 in 7710 to 7769, all state in one directory outside every checkout, OBS_DIR: -dir, which must
// be absolute, else $XDG_STATE_HOME/laug-obs, else ~/.local/state/laug-obs, and this binary's lines write it so, never
// the home path they stand for (a -dir under $HOME as ~/ and the rest). up, down, status, find, backfill, pack, view
// and purge run _obs/obs.sh <verb> by exec, and follow runs _obs/follow in this process. make obs-test runs _obs/obs.sh
// test, the stack's own test, and make lint _obs/lint.sh, the board rules. Each sets the names make passed _obs/obs.sh
// from its command line, only when given, so its -dry-run line is make -n's with them in front: OBS_DIR (-dir), ONLY
// (-only), OBS_CELLSTAT (-no-cellstat), FORCE (-while-gate-runs, -remove-packs), GATE (<gate id>), GATE_RESULTS
// (-results), RUN (-run-id), PACK (-pack, <pack>) and JSON (-json). _obs/obs.sh begins every line it prints "laug · obs
// <verb>: ", but test's, "laug · make obs-test: ". Whether a gate runs is bin/obs follow -gate-running's answer, which
// _obs/obs.sh asks the checkout's bin/obs, and up, backfill, pack, view and purge refuse while one runs, down unless
// -while-gate-runs; so those five refuse first, exit 2, when bin/obs is not built: make build builds it.
// OBS_MIN_FREE_GIB and OBS_BIN pass through as seams. obs-watch, one viewer's load on the gate run board, is not built.
//
// up starts the stack: the four releases fetched and checked by sha256, the configs copied into OBS_DIR, each part
// started on 127.0.0.1 and its listeners checked, then the store settled, 20 min at most. cellstat and follow are one
// copy of bin/obs, OBS_DIR/bin/obs-<sha256, 12 hex>/obs, run as bin/obs usage record -out none -listen 127.0.0.1:7710
// and bin/obs follow, each part's record held to its verb. -only <part> starts one of prometheus, jaeger, collector,
// cellstat, follow and grafana; -no-cellstat starts no cellstat, for a batch whose bin/obs usage record -listen
// 127.0.0.1:7710 serves the store. Its first line names what starts, where the state is and the line that stops it.
// Exit: the script's, 0 up, 1 not settled after 20 min, the stack left up; 2 refused: a gate running, a port taken,
// OBS_DIR in a work tree or under 30 GiB free, or a value laug refuses.
//
// down stops the stack's parts by their records (pid, start, boot id, binary), TERM then KILL after 30 s, each
// port then checked free; the data stays. -only <part> stops one. While a gate runs it refuses, exit 2, unless
// -while-gate-runs. It sets OBS_FOLLOW_BUILD to ok when bin/obs is built, else failed, with a stderr line, as make
// obs-down did after its build of follow: a gate then counts as running, and -while-gate-runs still stops the stack.
// Its first line says what stops. Exit: the script's, 0 every port free, 1 a port still taken, 2 refused.
//
// status reads each part, up since, CPU, RSS and its peak, IO, listeners and the configs it runs; the store's
// cells federated, head series, samples a second and oldest sample; disk and free space. -json keeps the script a
// child with JSON=1 and prints its object on one line: parts, store, disk_bytes, disk_of, free_bytes, target_files,
// target_files_past_expiry, configs, last_gate and view; with no OBS_DIR, {"obs_dir":false}. It writes nothing. Exit:
// the script's, 0 every part up, 1 a part down or no OBS_DIR, the object printed all the same; 2 refused.
//
// find reads one run three weeks later: a gate's board link with its window, its trace search, and what the store
// holds of it, its cells federated, its records live or backfilled and equal to its files, and its trace; or, with
// -run-id <id>, a load run's, from load/results/<id>.json. The gate is <gate id>, its directory in -results
// (gate/results) or followed in OBS_DIR, else the store's records; bare, the newest gate run in -results, named on
// stderr. It writes and starts nothing, so it runs with the stack down and while a gate runs. Exit: the script's, 0;
// 1 bare with no gate run in -results; 2 on a gate id laug refuses, as make's check did, or a run with no time.
//
// backfill puts gate runs over 3 h old that the store lacks into it: each rendered by follow -openmetrics with
// its own times, checked by promtool, made into blocks, moved into the store, then read back against its files. Bare,
// every gate run in -results; <gate id>, that one alone, refused under 3 h or over 90 d old. Exit: the script's, 0 each
// read back equal, 1 one differs; 2 refused: a gate running, the store down, a gate that does not render.
//
// follow runs follow, _obs/follow, in this process, the part bin/obs up starts from its copy: a poll of the pool on
// -pool (127.0.0.1:7700) every -every (2s) writes each leased cell's federation target into OBS_DIR/targets, and GET
// /metrics on -listen (127.0.0.1:7720) serves each gate's records, until Ctrl-C or SIGTERM. Both take 127.0.0.1 alone:
// the listener has no auth. OBS_DIR must hold the .laug-obs marker bin/obs up writes, or follow's files would make it a
// dir bin/obs up refuses. Its first line, once the listener is bound, names both and how to stop it; follow's lines
// follow on stderr in UTC. It reads XDG_RUNTIME_DIR from the process, as follow does, for the gate lock. Three flags
// answer once instead, as _obs/obs.sh asks bin/obs, one at a time and never with -listen: -gate-running says whether a
// gate runs on this host, a host lock held or a gate's pool on -pool with no done line, in one line, exit 0 and why
// when one is, 1 when none is, and reads nothing in OBS_DIR; -openmetrics <gate dir> prints that gate run's records as
// OpenMetrics in its own times, for promtool and backfill, exit 1 when the directory has no timeline; -decode -out
// <dir> decodes OBS_DIR's store and trace records into an empty dir and prints one JSON object of counts, for pack's
// check, exit 2 when a file cannot be placed or read. -openmetrics and -decode print no laug · line, as -json prints
// none: stdout holds the data alone. Exit 0 stopped, or answered; 1 when the listener cannot bind (the stack's own
// follow holds 7720) or OBS_DIR cannot be written, or as above; 2 on a value it refuses.
//
// pack packs the stopped stack's store and traces, OBS_DIR/prometheus and OBS_DIR/jaeger, as one tarball with
// PACK.json and SHA256SUMS, into -pack, a <name>.tar.gz outside every checkout, else OBS_DIR/packs/: every file, and
// the WAL's and the trace tables' records decoded by follow -decode, grepped first for check/deny-shapes.txt's
// shapes, the deny list LAUG_DENYLIST names, when set, and this host's name, full name, user name and home path. Its
// first line says what goes where. Exit: the script's, 0 packed; 2 refused: a part or a view running, a gate
// running, a hit, a file the check cannot read, -pack in a work tree or there already, or too little disk.
//
// view <pack> reads a pack on this host: unpacked into OBS_DIR/view and checked against its SHA256SUMS, then the store
// and Grafana alone on 127.0.0.1:7730 and 7740, with no scrape job, so it measures nothing; bin/obs down stops it. A
// relative path starts at the checkout's root. Bare, it names no pack: the packs in OBS_DIR/packs, the line to type
// each, and its usage, exit 2. Exit: the script's, 0 viewing; 2 refused: no such file, a tarball bin/obs pack did not
// write, a part or a view running, a gate running.
//
// purge removes the stack's state. Bare, it removes nothing: it says what -remove-state would remove, OBS_DIR by area
// with files and MiB on disk (the store, the traces, the releases, Grafana, a view's copy, the packs and the rest), and
// the line that removes it, on stderr, exit 2. -remove-state runs _obs/obs.sh purge by exec: bin/obs down, then OBS_DIR
// removed, then what is left counted, processes, ports and files; its first line says what goes, with counts. It
// refuses, exit 2, an OBS_DIR without the .laug-obs marker, or one whose packs/ holds a pack, each named, unless
// -remove-packs, FORCE=1, which removes them with it. Exit: the script's, 0 nothing left, 1 something left; 2 refused,
// a gate running among the causes.
//
// usage reads laug cells' cgroups, the Omes processes and the host through _obs/cellstat, in this process, one JSON
// line a sample; _obs/cellstat/main.go says what a line holds, and usage report -h what the report holds. Its four
// verbs were make cell-usage, ONCE=1, cell-usage-stop and cell-usage-report. record and sample find docker on PATH;
// with none there, a line holds no container and says list failed, and one stderr line says so first.
//
// usage record writes a line into -out, cells/run/cellstat/usage.jsonl by default, a relative path starting at the
// checkout's root: at start, every -interval (5s), and once more at -for (20m; 0 waits), at bin/obs usage stop, Ctrl-C
// or SIGTERM. A second signal while that last line is read ends it the same way. -out must be new: record refuses one
// that exists, empty or not, and names the cellstat that writes it, or its line count, and the line to type. <file>.pid
// holds the pid while it records, and the process takes the name cellstat, by which usage stop and _obs/obs.sh's batch
// check find a recording. -listen 127.0.0.1:<port> also serves the last sample on GET /metrics, 7710 for the
// observability stack, and no other host, since it has no auth. -out none writes no line and keeps no pid file, so it
// takes -listen and serves the last sample alone, as the stack's cellstat, which bin/obs up starts from its copy; usage
// stop finds no file to stop it by, and bin/obs down stops it by its record. -pids <dir> also reads the processes the
// records there name, <OBS_DIR>/run for the stack's, and the orchestrator's dev server and worker. Durations are Go's,
// 5s or 20m; the Makefile laug replaced also took bare seconds. Its first line says where the lines go, until when, its
// pid and how to stop it, once the file is locked and the listener bound; cellstat's own lines follow on stderr in UTC,
// and its last line counts the lines, or under -out none says no file holds one. Exit 0 stopped; 1 when the file, its
// pid file or the listener cannot be set up, a line cannot be written or the process cannot take its name; 2 on a value
// it refuses, an -out that exists, or a host with no /proc and /sys/fs/cgroup.
//
// usage sample reads one line and writes nothing: a summary, a row a container (project, service, id, state, CPU
// seconds, anon and file MiB, why) and a row a process. -json prints the line as record writes it; -pids is record's.
// Exit 0; 1 when the line cannot be read back; 2 as record.
//
// usage stop <file> ends the recording that writes <file>: TERM to the pid in <file>.pid, then a wait of up to 25 s
// for its last line, since a sample makes two docker calls of 4 s at most. A cellstat writes <file> while <file>.pid
// names it, its process is named cellstat and it holds <file>'s lock, so a pid file a killed one left is not enough;
// the recording has ended when either goes. Bare, it lists the recordings running in cells/run/cellstat, a line to
// type each, and its usage, exit 2. Exit 0 stopped, with the file's line count; 1 when none writes <file>, TERM is
// refused (another user's), it is still up after 25 s (a rerun waits 25 s more) or a signal cuts the wait short.
//
// usage report <file> sums the lines per item and service, per Omes process, for the host and for cellstat: cores,
// CPU and IO pressure, IO, Omes run-queue wait and memory. It reads the whole file, or with -gate <run> that gate
// run's wave, its items naming the rows: a gate id in -results (gate/results), or the path to a run's directory.
// Bare, it lists the files in cells/run/cellstat, a line to type each, and its usage, exit 2. -json prints the report
// as one JSON object on one line: window, items, omes, host and cellstat. It writes nothing. Exit 0; 1 when the file
// or the gate run cannot be read, or the window holds fewer than 2 lines, since a rate needs 2; 2 on a value it
// refuses.
//
// help prints the list of verbs; with a verb, its flags, as its -h does; with usage, the usage verbs, and with usage
// and a verb, that verb's flags. It writes nothing.
package main
