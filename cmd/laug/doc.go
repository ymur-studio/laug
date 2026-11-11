// Command laug runs laug: ephemeral Temporal cells from a warm pool, for release validation. One trial, a raun, leases
// a cell, puts load on it, breaks it on purpose and reads one verdict, ADVANCE or BLOCK. A gate judges a server release
// by every item of a plan at once.
//
//	laug <command> [flags] [arguments]
//
// The rule this binary keeps: three entry points. laug runs laug: the pool, cells, loads, faults, verdicts and gates;
// bin/obs, _obs's own binary, runs the stack; make builds, checks and documents the tree: build (Go checked first), the
// test tiers, lint, docs, clean and the Firecracker image. A make target never does what a subcommand does. A doc never
// tells a person to run a script: it names a subcommand or a target, and only once a test runs it. A bare subcommand
// changes nothing its name does not say. An irreversible act is its own verb or takes a long flag naming the loss, and
// first says what will go, with counts. Two exceptions: a gate's R1 step runs make harness-test (gate/lib.sh's
// gate_env, harness_cmd), as the judge's R1 reason names it (internal/judge's r1Of), and make docs runs the renderer,
// its own module under _report (go -C _report run ./cmd/report -docs), which writes the committed documents a test
// holds to it. The stack's verbs are bin/obs's, its own module under _obs (_obs/cmd/obs/doc.go). The test tiers are
// test-go, harness-test, obs-test and test, which runs the first two and lint; the Firecracker image targets, fc-image,
// fc-snapshot and fc-restore, run as root and need Docker. make build builds bin/laug, and bin/obs beside it, and no
// other name links to either. The Temporal SDK's six roles, the HealthCheck round trip, the timer probe, the
// orchestrator's worker, starter, probe and recorder, run in laug's own process (internal/health and
// internal/orchestrator), so laug is one binary, and a script that needs a role runs its verb: cells/health.sh and the
// compose and Firecracker backends run bin/laug health check.
//
// The table in main.go is the command surface. A row holds a name, a help group, a summary and its verbs. A verb holds
// a name, a class, a run function, run<Name>(ctx, a, args) int, where a is the App: the checkout, the environment and
// the two streams, and, when it has a name, a summary its -h prints first. main fills the App from the process and a
// test fills it with buffers and a list, so a test drives the whole binary through run. A command with one verb leaves
// its name and summary empty, as version and help do; one with several takes the verb first ("laug pool status"), and
// bare, or with an unknown verb, prints its verbs on stderr, a line each with its summary, exit 2. The class says what
// a verb's bare form may do. read changes nothing in the tree or the pool and exits 0. act does the one thing its name
// says, and its first line says what started and how to stop it. needs takes a subject; bare, it prints the choices and
// its synopsis, and exits 2. destroys removes something; bare, it says what would go, with counts, and exits 2.
//
// Every command keeps these rules. Help asked for (help, -h, -help, --help, or a command's -h) goes to stdout, exit 0;
// --version and -version run version. Usage forced by a mistake (no command, an unknown one, a bad flag, a stray
// argument) goes to stderr, exit 2. Bare, laug prints the help. Any other mistake fits on one screen, 23 rows: its
// line, then the choices when there are some, a line each, or down and across in columns when a line each would not
// fit, then the synopsis and the line that prints the flags, which only -h prints. A -h fits 80 columns: the synopsis
// wraps under the word after "usage:", a bracketed group whole when it fits, else between its inner groups, and each
// flag's text wraps beneath its name, ending with its default as the synopsis writes one, 30m and not 30m0s, a one-word
// string bare. An unknown command, verb, flag, fault or candidate names the nearest, 2 edits at most: "did you mean
// gate?". A bad value names the flag, the value as typed and what the flag takes, its default the example: "-ttl 5x: a
// duration, 30m say". Nothing answering on a pool's address is one line that names the reason and, for a lease or pool
// status, the pool serve line that starts one there. Flags are kebab-case with one dash, and may come before or after
// the arguments; a "--" in a flag's place ends them. The first line of output starts "laug · ", and an error is one
// stderr line, "laug · <command>: <what failed>: <why>". A line laug prints for a person to type, and every usage line,
// starts with one word: laug when PATH finds a laug, which main asks once, by exec.LookPath, and bin/laug when not. It
// never comes from argv, which can hold a home path. laug sets LAUG to that word for every script and role it runs, and
// their lines read it, ${LAUG:-bin/laug} in a script. A line under sudo says sudo bin/laug, since sudo's PATH is its
// own. A path laug prints is relative to the checkout, or ~/ and the rest under $HOME, never the home path, in the text
// of an error from the file system or exec too. A -dry-run line writes the checkout $ROOT, and a path under $HOME ~/
// and the rest, the ~/ outside any quotes, so a shell reads it back. -json prints one JSON document on stdout,
// snake_case, and nothing else. A verdict goes on its own line, bold in moss or ember only when its writer is a
// terminal, NO_COLOR is unset or empty and -json is off. laug reads these environment variables, each from the App:
// TERM and NO_COLOR, for that color; TMPDIR, where verdict -json keeps the file it reads back, gate run -fake makes its
// dir and gate control its worktree; PATH, where load finds docker to read a local cell's server tag, gate run, gate
// rejudge and judge -identity find git to read HEAD, gate control finds git and make to make its worktree and build
// bin/laug there, deps finds the tools it checks, upstream finds git, runbook check finds bash, cell down finds docker
// to list what goes, pool down finds docker to list the pool's cells, health record and replay find go, orchestrator
// server finds temporal, orchestrator worker finds git to read HEAD, and orchestrator probe finds ss and getconf;
// XDG_CONFIG_HOME, else HOME, where gate run reads the host type declared, laug host type -declare's file; CATALOG and
// CANDIDATES, two seams, where fault, raun and candidate check a name against the file their script reads; and
// FAKE_GATE_DIR, a seam gate run -fake keeps when it is set. pool serve builds internal/script's environment for a
// backend script, and reads SERVER_TAG, VISIBILITY and BIND_ADDR only to name a stray copy on stderr. runbook check
// hands each block it runs the environment whole. lease never reads USER. Exit 0 done or ADVANCE, 1 BLOCK or failed, 2
// usage or refused, 3 a run shorter than the window its thresholds trust; a command that uses another code names it in
// its paragraph below. A script under set -e stops with the code of the command that failed, a tool's own:
// load/verdict.sh exited 7, curl's, with no Prometheus to read. A verb that execs its script exits with that code.
// verdict, raun and host type -json keep their script a child, and turn a code their paragraph does not name into 2,
// with one stderr line that names the script's code; 128+n, a signal's, passes as it came. load keeps its script a
// child too, and exits with its code as it came.
//
// Adding a subcommand: one file, cmd/laug/<name>.go, with run<Name> and its flags from newFlags; one row in the table,
// with a class for each verb and a summary for each named one; one paragraph below that starts with its name; one test
// file, <name>_test.go, holding Test<Name>. main_test.go fails a row without a group, a paragraph or that test, a verb
// without a class, a named verb without a summary, a -h that does not print to stdout with exit 0 or whose first line
// is not "laug · <command>[ <verb>]: <summary>", a bare verb that breaks its class, a help line of 80 columns or more
// or with a tab, a default in Go's long form, and a help line that names Gate 6, make -n or a gate-open flag. The
// pool's packages and the runner, internal/pool, internal/poolapi and internal/script, the gate-open check,
// internal/gateopen, the judge's, internal/jsonx, internal/judge and internal/rejudge, internal/tolerance, the summary
// tables' reader, internal/tables, and internal/denylist, the deny list's rule, link the standard library and this
// module only; the renderer, _report, is its own module and links the same (its cmd/report writes the committed
// documents: make docs runs it), and so is the stack, _obs, with follow and cellstat. The Temporal SDK reaches this
// package through internal/health and internal/orchestrator alone, imported by health.go and orchestrator.go, the files
// that mount its roles, and by no other: stdlib_test.go fails on anything else.
//
// A verb that wraps a script runs it through internal/script, from the checkout's root, and outside a checkout refuses
// with exit 2. A verb that runs a program, go or temporal, runs it the same way, found on PATH; go test runs in
// internal/health, with "cd internal/health && " first on its -dry-run line. The script gets a clean environment: PATH,
// HOME, TMPDIR, LANG, LC_*, TERM and TZ; the tools' own, GO*, DOCKER_* and SSH_AUTH_SOCK; the seams; and the knobs the
// verb sets from its flags. Every other name is dropped, USER, LOGNAME, TEMPORAL_* and OTEL_* among them. A knob is a
// name in internal/script's table, which gives each its type, default, readers and flag. So a SERVER_TAG or a RUN_ID
// exported in the shell never reaches a script, and a stray copy of a knob the verb maps gets one stderr line that
// names the flag setting it. The seams pass through unread, for tests and experiments: FAKE_*, FC_*, LAUG_FI_*, XDG_*,
// GATE_LOCK, GATE_HOST_LOCK, GATE_POOL_LISTEN, GATE_BACKEND, THRESHOLDS_RELEASES, MIN_WINDOW_S, RESULTS_DIR,
// LEASE_FILE, CATALOG, CANDIDATES, SWEEP_EXPECTED, SWEEP_RETRIES, DYNCONFIG_TEMPLATE and the rest of internal/script's
// Seams. -dry-run prints one line and runs nothing: the script's line with the knobs the verb sets in front, the
// checkout written $ROOT; and, first, CATALOG, CANDIDATES or LEASE_FILE when the environment sets it, for a script that
// reads that file, so the line run alone reads it too. A verb with nothing to do after its script execs it, so its
// process, signals and exit code are the script's, a tool's own among them; one that acts after it waits for it and
// exits with its code, held to the codes its paragraph names.
//
// A verb that runs a Temporal SDK role runs it in laug's own process (role.go): health check, loop, record and timer,
// orchestrator worker, probe and record, gate run -workflow, and gate status, cancel, terminate and stop. The role gets
// its own flags and the environment internal/script builds, as a script would, which it reads in place of the
// process's: TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE, a gate's knobs and the seams. It gets the verb's name too, and a
// line it writes on stderr starts with it, "laug · health loop: ", as an error does. A stray copy of a knob the verb
// maps gets its stderr line. Such a verb takes no -dry-run, as there is no line to print; it shares laug's signals, so
// Ctrl-C ends the role's context, and its exit is the role's.
//
// pool runs the warm pool, internal/pool, reads it, stops it and measures it. Its README, internal/pool/README.md,
// holds the cell states, the port blocks, the answers of /lease and /reset, the guardrails and the backend's verbs.
//
// pool serve runs the pool in the foreground on -listen, 127.0.0.1:7700 by default: a loopback address and a port,
// since the API has no auth, so any other host is refused with exit 2. -backend fake, the default, answers at once and
// starts nothing, as an empty -backend does. -backend compose runs cells/backend-compose.sh, one compose cell a port
// block; it needs a checkout and its bin/laug, whose health check the backend's health verb runs, or refuses, exit 2.
// -backend firecracker runs cells/backend-firecracker.sh, one microVM a cell restored from the snapshot make
// fc-snapshot took. It runs as root alone, for /dev/kvm and each cell's network and mount namespaces, so the whole
// binary runs as root: sudo bin/laug pool serve -backend firecracker. Like compose it needs the checkout and its
// bin/laug, built as the user. Not as root, or without either, it refuses, exit 2. -min and -max are 1 and 2 there
// unless given: a clone takes 4 vCPU, fixed in the snapshot. A microVM cell runs the server tag and visibility the
// image bakes, 1.31.0 and postgres, and answers on a host-only address, 10.254.<block>.2, so -server-tag, -visibility
// and -bind other than their defaults are refused, exit 2. Once it holds the address it runs the backend's prep as a
// child, its lines on stderr: no other pool serve running, root's tools and the snapshot there, the disks volume, a
// sweep of what a last pool left, the jailer's checks (FC_JAIL=1, the default; FC_JAIL=0 skips them), the deny rules
// and the guest key for the sudo user (or FC_KEY_USER); a prep that refuses exits 2, and one that fails 1, with nothing
// served and no pid file. The pid file is serve's, and cells/run, made as root, gets the checkout's owner. Any other
// value is a backend script. A script backend runs as <script> create|destroy|health <id> with the environment
// internal/script builds, as a verb's script gets it: the base names, the tools' and the seams as pool serve got them,
// USER, LOGNAME, TEMPORAL_* and OTEL_* dropped; SERVER_TAG from -server-tag (1.31.0), VISIBILITY from -visibility
// (postgres, or es) and BIND_ADDR from -bind (loopback, 127.0.0.1, or all, 0.0.0.0), each value checked as laug cell up
// checks it, and a stray copy of each in the environment named on stderr and not passed on; LAUG_BLOCK the cell's port
// block, and SERVER_TAG the tag a lease names, per call. It keeps -min warm cells ready (3), never more than -max on
// the host, warm plus leased plus in-flight, and stuck (6, also the number of port blocks), -quota leases a requester
// (2), and clamps a lease to -max-lease (2h). -max takes 17 at most; 18 or more is refused, exit 2. Port block N's
// frontend is 7233 plus 10N, and blocks 0 to 16 keep it below 7400 and 7700, the orchestrator's dev server and laug
// pool serve. A microVM cell publishes no host port, so -backend firecracker is not held to the cap. -create-timeout
// (5m) bounds a create until healthy, and a destroy; -health-poll (2s) is how often a new cell's health is asked;
// -reap-interval (10s) how often expired leases are released and a failed create or a stuck cell's destroy is tried
// again. Its first line says where it listens, with what, its pid file and how to stop it; log lines follow on stderr
// in UTC. -log <file> sends them, and a script backend's own output, docker compose's lines among them, to that file
// instead, appended, its directory made if need be, a relative one under the checkout's root, so a pool started with &
// keeps the terminal; the first line names it. In a checkout it keeps its pid in cells/run/pool.pid while it runs, and
// refuses with exit 2 when that file names a process that runs; -pid-file none keeps no file and reads none, for a pool
// beside the checkout's own on a port of its own, as gate/run.sh and gate/step.sh start a gate's, and -backend
// firecracker refuses it, since that backend finds its pool by the file. SIGINT or SIGTERM stops it within 2 s, exit 0:
// requests in flight get 1.5 s, and no cell is destroyed (pool down -remove-cells removes the compose backend's, and
// pool down -backend firecracker -remove-cells the microVMs). Exit 1 when the address cannot be bound, prep fails or
// the pid file cannot be written; 2 on a flag, an argument, a value or a backend it refuses, a -log file it cannot
// open, or a prep that refused.
//
// pool status reads GET /status on -pool, 127.0.0.1:7700 by default, through internal/poolapi, the pool's client: a
// loopback IP and a port, or an http:// URL, since the API has no auth; any other host is refused, exit 2. It prints
// the pool's settings, its cells by state, and a row a cell: id, port block, state, server tag, requester, the lease's
// expiry in UTC and address. -json prints the pool's /status document as it came, on one line. Exit 1 when nothing
// answers or what answers is not a pool, 2 on a flag, an argument or an address off loopback.
//
// pool down -remove-cells stops this checkout's pool and every gate's, and removes every pool cell: cells/pool-down.sh
// by exec. It stops the pool serve cells/run/pool.pid names, only while that pid's command line is laug pool serve,
// TERM then 3 s at most; then every gate's own pool, a process whose command line is laug pool serve with -pid-file
// none; then docker compose down -v for each laug-<8 hex> project, its containers, its volumes (Postgres's and
// Prometheus's data, which no command brings back) and its networks; then the per-cell dirs in cells/run and
// .last-lease.json; and it prints what is left. Bare, or without -remove-cells, it stops and removes nothing: it lists
// what would go, the pool serve's pid, the gate pools' pids, the pool cells with their containers, volumes and
// networks, the per-cell dirs and the last lease, with counts, and the line that removes them, on stderr, exit 2. It
// finds docker on PATH to list them. While a gate runs, a host lock held or a gate's laugd on 127.0.0.1:7700 with no
// done line, as bin/obs follow -gate-running answers, -remove-cells refuses, exit 2 with nothing stopped, since it
// would end that gate mid-wave, unless -while-gate-runs, which its first line then names; bare says so after its list.
// The single cell, project laug, is laug cell down's. A pool serve on -backend firecracker is not stopped here, nor its
// cells removed: a line says the Firecracker pool is up when the pid file names it or /run/laug holds a cell. Exit: the
// script's, 0, or 1 when a docker compose down failed; 1 when a docker listing fails, 2 with no docker on PATH, both
// with nothing stopped or removed; 2 outside a checkout, on a flag or an argument, or while a gate runs.
//
// pool down -backend firecracker, as root alone (sudo bin/laug pool down -backend firecracker), stops the pool serve
// cells/run/pool.pid names when its command line is laug pool serve -backend firecracker: SIGTERM, then 5 s at most.
// Without -remove-cells that is all, and the cells stay: its first line says what stops and counts the cells that stay.
// With -remove-cells it then runs cells/backend-firecracker.sh sweep by exec: the sweep waits for the backend's verbs
// in flight, removes each microVM cell in /run/laug, its VMM, link, disk (its data, which no command brings back) and
// dir, then any VMM, link or disk the blocks missed, the guest key and prep's deny rules, and prints what is left; its
// first line says what goes, with counts: the pool serve's pid, the cells by their block dirs, and the VMMs. -dry-run
// with -remove-cells prints the sweep's line; -dry-run alone says what would stop and stay, exit 2; neither needs root.
// A pid file that names a pool serve on another backend is refused, exit 2. Exit: 0 when stopped, or the sweep's; 1
// when pool serve still runs 5 s after SIGTERM, with nothing removed; 2 outside a checkout, not as root, or on a flag
// or an argument.
//
// pool bench <host:port> times a running pool: cells/pool-bench.sh by exec, with POOL_ADDR the pool's http:// URL, a
// loopback address as -pool takes one. It reads the time to the pool's warm minimum, leases and releases -cycles (20)
// cells from warm for the lease p50 and p95, holds every warm cell for one cold lease, and leases one for -ttl-short
// (30s, whole seconds) to time its teardown; it prints a markdown table. The cold lease needs room for every cell in
// the pool's table, the one the pool builds to refill its -min, and itself: -max at least -min plus 2 on a pool nobody
// else leases from. The script reads /status once the pool is warm, before its first lease, and again before that
// phase; when there is no room, as on the Firecracker defaults (-min 1, -max 2), it says so with the numbers and the
// -max that fits, exit 2. When the pool refuses a lease or a release, it prints the pool's answer, exit 1. On any exit,
// a failure and Ctrl-C included, the script releases every lease it still holds. -cycles and -ttl-short reach the
// script as CYCLES and TTL_SHORT only when given. Bare, it names no pool: its usage, exit 2. Exit: the script's, 0 with
// the table; 2 on a pool off loopback or a value it refuses.
//
// lease asks the pool on -pool, as pool status reads it, for a cell for -ttl (30m, whole seconds; the pool cuts a
// longer one to its max lease, and the first line says so) as -requester (laug; the pool's quota counts leases by it).
// It never reads USER, so no login name reaches a lease or a result that copies one. With -server-tag x.y.z the cell
// runs that server release. It keeps the pool's answer as it came in -file, .last-lease.json at the checkout's root, or
// outside a checkout in the working directory: a temp file beside it, .lease.*, then a rename, so a reader sees the old
// lease or the new one whole, and a refused lease leaves the file as it was. load/run-omes.sh, faults/run.sh and
// faults/candidate.sh read it there. A cold cell takes the pool's -create-timeout at most, so when the pool has not
// answered in 1 s, one stderr line says it waits for a cell and that Ctrl-C stops the wait; raun and candidate say the
// same. Its first line names the lease, its expiry in UTC, the file and the line that gives it back; the lease follows,
// indented, with its paths, a compose cell's dynconfig, shown from the checkout's root or as ~/. -json prints the
// pool's answer alone, on one line, as the file holds it. Exit 0 leased; 1 when the pool refuses (409 for its quota or
// a full pool, 502 for a failed create; the line gives its reason), nothing answers, a signal stops the wait (the pool
// gives back a cell it could not hand over), or the file cannot be written (the line names the id and the release); 2
// on a flag, an argument or a value it refuses.
//
// reset has the pool destroy the cell behind a lease and build it again under the same lease: the id, the port block
// and the expiry stay, so the last-lease file stays as it is. A reset whose create fails ends the lease: the pool
// forgets it, and may build on its port block at once, or keeps its cell as stuck. So after any refusal but 404, when
// -file names that lease, reset asks the pool's status. When the status shows the lease neither leased nor busy, reset
// removes -file, as release does, and its line says so. A 404, which a pool other than the lease's gives too, no answer
// or a signal leaves the file, and its line says so when the file still names the lease. It takes the lease's id, or
// -last for the one -file names. Bare, it names no lease: it prints the last lease's id and the line to type, or that
// there is none, and its usage, on stderr, exit 2. Its first line says what was rebuilt and the line that gives it
// back; the lease follows, indented, its paths shown as lease shows them. -json prints the pool's answer alone, on one
// line. -pool and -file are lease's. Exit 0 rebuilt; 1 when the pool refuses (404 for a lease it does not hold, 409 for
// a cell that is not leased, 502 for a create that failed), nothing answers, or a signal stops the wait (the pool
// finishes the reset on its own); 2 on a flag, an id and -last together, or -last with no lease in the file.
//
// release gives the cell behind a lease back, and the pool destroys it, its Prometheus with it, so a verdict reads the
// cell before its release. It takes the lease's id, or -last for the one -file names, and removes -file when it names
// the lease released, as faults/candidate.sh does: left behind, the file would point load and verdict at the next cell
// built on that port block. A refused release leaves the file, and its line says so when the file still names that
// lease. Bare, it prints the last lease's id and the line to type, with -pool and -file when they are not the defaults,
// and its usage, on stderr, exit 2. -json prints the pool's answer, {"released": "<id>"}, on one line. Exit 0 released;
// 1 when the pool refuses (404 for a lease it does not hold, 409 for a cell that is not leased, 502 for a destroy that
// failed, the cell kept as stuck), nothing answers, a signal stops the wait (the pool finishes the release on its own),
// or the file it should remove stays; 2 as reset.
//
// load puts an Omes scenario on a cell: load/run-omes.sh as a child, so that laug can say a line a minute on stderr
// while Omes is silent: the seconds gone, of -duration's or toward -iterations, and past -duration that Omes waits up
// to 120 s for the workflows in flight. Ctrl-C at a terminal reaches the script, and laug sends it nothing more. A
// SIGTERM sent to laug alone goes on to the script. Either way the script stops the Omes it started, writes no run JSON
// and ends, 130 or 143. A second signal ends laug. The cell is -address, else the last lease's, else port block 0's,
// 127.0.0.1:7233; -prom and -project the same way, else http://127.0.0.1:9090 and laug. Given -address while a lease is
// kept, it needs -prom as well, or the run would record the leased cell's Prometheus: exit 2. -server-tag x.y.z is the
// tag the run records; unset, the lease's for the leased cell, else the one the project's temporal-history container
// runs, read with docker ps and docker inspect from PATH, else none. It needs a length: -duration, in whole h, m and s
// as Omes and load/lost-work.sh both read it, or -iterations, one of the two. With neither, bare among them, it starts
// nothing: it names no length, prints the cell the load would go to and where that cell came from, the line to type,
// laug load -duration 5m and the flags given, and its usage, on stderr, exit 2. -rate and -max-concurrent cap the load
// and -continue-on-failure keeps it going past a failed iteration. It sets OMES_DIR (.upstream/omes), CELL_ADDRESS,
// PROM_URL, PROJECT, NAMESPACE (-namespace, default), RUN_ID (-run-id, the UTC time) and SERVER_TAG, with -scenario
// (workflow_with_single_noop_activity) as the argument; DURATION, ITERATIONS, RATE, MAX_CONCURRENT and
// CONTINUE_ON_FAILURE only when given. Its first line names the scenario, the cell, the length and the run, and says
// Ctrl-C stops it. Exit: the script's, a tool's own among them, 3 when the lease ends before the run would; 2 with no
// length, on a flag or a value it refuses, or outside a checkout.
//
// verdict judges one run, <run-id>, by its release's thresholds: load/verdict.sh as a child. PROM_URL is -prom, empty
// unless given; FALLBACK_PROM_URL, for a run that recorded no Prometheus, is -prom, else the leased cell's, else port
// block 0's. -thresholds names a set to judge by in place of the release's, and -sweep-margin (30s) how long before the
// run's end the sweep counts; each reaches the script only when given. The script's stdout passes through but its
// verdict line, VERDICT: ADVANCE or VERDICT: BLOCK, whose word takes the color rule above. The verdict JSON, the
// script's VERDICT_JSON document, goes beside the run's, <run-id>.verdict.json in load/results (RESULTS_DIR's, a seam,
// when set), as a gate item's does, or to -out <file>, a relative one at the checkout's root, and a last line says
// where, and that git does not track it when git status lists it untracked. It never goes over a file: -out must be new
// and in a directory that is there, else exit 2 with nothing run, and a file beside the run, a committed run's verdict
// say, stays as it was, which the last line says. A file the script left cut short, on an exit but 0, 1 and 3, goes.
// -json prints the document on one line, from a temp dir under TMPDIR that it removes when the file beside the run
// stays, and moves the text to stderr. Bare, it names no run: its usage, exit 2. Exit: the script's: 0 ADVANCE, 1
// BLOCK, 2 no verdict (no run metadata, a set that does not resolve), 3 a short run that lost no work, 128+n when
// signal n ended it; any other code is a tool's the script ran, such as curl's 7 when no Prometheus answers, and laug
// exits 2, no verdict, with a line that names it; 2 as well on a flag or a run id it refuses, or outside a checkout.
//
// fault runs one fault, <fault>, through three windows on the leased cell: faults/run.sh by exec, with -scenario and
// the fault as its arguments, RUN_ID (-run-id, the UTC time) and POOL_ADDR (-pool, as an http:// URL). -rate (20),
// -max-concurrent (50), -quiet-wait (300s, the most the load waits for the pool's other cells to finish building),
// -thresholds and -sweep-margin (30s) set RATE, MAX_CONCURRENT, POOL_QUIET_WAIT_S, THRESHOLDS and SWEEP_MARGIN_S only
// when given; the defaults are the script's own, which a test holds equal. The fault must be one the catalog
// faults/run.sh reads names, CATALOG's file when the environment sets it, else faults/catalog.yaml; and the last lease
// must name a cell, but under -dry-run. Its first line names the fault, the lease and the run; Ctrl-C reaches the
// script, which removes the fault and stops. Bare, it names no fault: it lists the catalog's and its usage, exit 2.
// Exit: the script's, 0 ADVANCE, 1 BLOCK, 2 a harness failure, or a tool's own code; 2 as well on a value it refuses.
//
// raun is one trial. It leases a cell as lease does (-pool, -ttl 30m, -requester laug, -server-tag), keeps it in
// .last-lease.json, runs <fault> on it with faults/run.sh as a child, under fault's flags and RUN_ID raun-<run id>,
// then gives the cell back however the script ended and removes the file while it names that lease. Its first line
// names the lease and the run, and how long the windows take, steady_s, hold_s, settle_s and after_s of the fault's row
// in the catalog faults/run.sh reads, plus its budget_s, the most a recovery may add. The line that says the cell went
// back ends with the raun JSON's path, RESULTS_DIR's or faults/results' raun-<run id>.json, when the script wrote it
// during this raun. Ctrl-C at a terminal reaches faults/run.sh too, which removes its fault and stops; a signal sent to
// laug alone does not, and the fault runs to its end. Either way raun says on stderr that the cell goes back once the
// script ends; a second signal ends laug at once, and the pool reaps the lease at its expiry. The name and every flag
// are checked before the lease, so a typo leases nothing, and -dry-run prints faults/run.sh's line and leases nothing.
// Bare, it lists the catalog's faults, exit 2. Exit: the script's (0 ADVANCE, 1 BLOCK, 2 a harness failure, 128+n when
// signal n ended it); any other code is a tool's the script ran, and laug exits 2, a harness failure, with a line that
// names it; 1 when the lease is refused, or the cell could not go back after a script that exited 0; 2 on a value it
// refuses before the lease.
//
// gate is the release gate. run judges a server release by every item of a plan at once on this host; post hands a
// run's verdict on; rejudge, compare and signature are the same-verdict checks, S1 by bytes and S2 by signature, in Go;
// tolerance computes the incumbent arm's tolerances, in Go too; open names a run's directory and its verdict; status,
// cancel, terminate and stop act on a gate that runs as workflow Gate, gate-<gate id>, which gate run -workflow starts
// on the orchestrator's dev server, laug orchestrator server's, 127.0.0.1:7400 unless -orchestrator names another;
// control runs a change the gate must catch, one of gate/controls, through a gate on fakes. A gate run is a directory
// in -results, gate/results by default, named by its gate id, g<UTC date>-<UTC time>. The verbs that read a run take
// its id, one word, or the path to its directory, which holds a slash; a relative one starts at the checkout's root, as
// -results does. Every gate verb needs a subject: bare, it says so and prints its usage, exit 2. run asks for a tag,
// control lists the controls, and the rest name the newest run in -results, with the line that reads it when the verb
// takes one run.
//
// gate run <tag> runs gate/run.sh by exec, with its environment: SERVER_TAG the tag, PLAN (-plan, gate/plan.yaml),
// ITEMS (-items, all), BASE (-base, main; with no local main, gate/lib.sh takes origin/main, else HEAD, and says
// which), GATE_FAKE (-fake), GATE_POST (-post, local), GATE_RESULTS (-results, gate/results) and OMES_DIR
// (.upstream/omes). Before it starts, it refuses with exit 2 a checkout without bin/laug, which laug does not build:
// its pool serve -pid-file none is the gate's own pool, and a compose cell's health verb runs its health check. It
// refuses a bin/laug not built from HEAD too: the commit in bin/laug's build info, the -X main.commit make build
// stamps, is held to git rev-parse HEAD, so a run's laug_commit names the code its pool ran. With -fake, -allow-stale
// runs the gate anyway; without -fake it is a usage error, since gate/run.sh's preflight refuses a gate on cells whose
// bin/laug was not built from HEAD all the same, as a refused run in -results. Without -fake, -workflow too, it refuses
// a host that declares no host type, exit 2 with nothing written: gate/run.sh's preflight would refuse it and keep a
// BLOCK run in -results, and laug host type -declare <type> declares one. A host that declares a type other than its
// threshold set's goes on to the script, whose refusal is a record, as the lab's g20261017-171604 is. A tree with
// changes not committed gets one stderr line, since no stamp tells whether bin/laug holds them; gate.json records
// tree_dirty. The run itself is judged by bin/laug judge -write (judge, below), and gate.json's inputs end with that
// judge's identity, {path: "internal/judge", sha256, changed_vs_base, version, go, tree_clean}; gate/run.sh's preflight
// refuses a gate on cells whose bin/laug was not built from HEAD or whose judge is not the one internal/judge and
// internal/jsonx hold, committed. -fake runs the gate on fakes, no Docker, cell or Omes: it makes a dir under TMPDIR,
// laug-fake-gate-*, with the run in results/ unless -results says where, the fakes' files in fake/ as FAKE_GATE_DIR
// unless the environment sets one, and an empty omes/cmd/omes as OMES_DIR, so no run lands in gate/results. Its first
// line names them, and the dir stays for the reader; laug hands gate/run.sh the dir as FAKE_GATE_TMP, a seam, and the
// script's last line names it, $HOME written ~. -json keeps the script a child, sends its text to stderr and prints the
// run's verdict.json on one line. Exit: gate/run.sh's, passed through: 0 ADVANCE, 1 BLOCK, 2 a usage error or no
// verdict written, or a tool's own code; 2 as well on a tag or flag laug refuses, a refusal above, or outside a
// checkout.
//
// gate run -workflow runs the gate as workflow Gate on this checkout's worker instead: the starter's role,
// internal/orchestrator's StarterMain, in laug's own process, with TEMPORAL_ADDRESS (-orchestrator, 127.0.0.1:7400) and
// TEMPORAL_NAMESPACE (default) before gate run's environment, which the starter carries as the workflow's input, and
// -otlp 127.0.0.1:<port> to trace the workflow to the stack's Collector, 127.0.0.1:7751. The starter refuses a worker
// not built from HEAD, runs gate/step.sh check, starts gate-<gate id>, prints each step as it begins, then the verdict;
// Ctrl-C detaches it, and the gate runs on. -json and -dry-run do not go with -workflow, nor -orchestrator and -otlp
// without it. Exit: the starter's, 0 ADVANCE, 1 BLOCK, 2 no verdict or a detach.
//
// gate post <run> hands the run's verdict.md and verdict.json on to -post, local by default and the only sink built:
// gate/post.sh by exec with GATE_POST. It reads the two files and changes neither. Exit: the script's, 0 handed on, 2 a
// sink not wired or nothing sound to hand on, or a tool's own code.
//
// gate rejudge <run> judges a run again, S1, in Go: internal/rejudge runs the judge version the run's gate.json
// records, the one internal/judge/versions.txt names for its gate/combine.sh sha256, or for a run laug judge judged,
// the version of its internal/judge entry, on the run as it stands, and compares the verdict.json and verdict.md it
// makes with the run's, byte for byte. It writes nothing and copies nothing. A run that differs is known when a proof
// holds: the items the GONE rule (rejudge.Gone) names as an empty directory, which git does not carry, made again in
// memory give both files. A trap verdict, which the gate wrote when it stopped before its judge, is re-rendered: one of
// its three forms gives both files from the run's own step and reason. Anything else is unexplained. First the judge is
// held to HEAD's: laug built from HEAD, by the commit make build stamps, -dirty or not; git status clean under
// internal/judge and internal/jsonx; and the sources there the ones laug embeds. When one fails, every run is not
// judged, with the reason, as is a run whose recorded judge no version names or on which the judge writes no verdict.
// The long form names each file identical or the first line where it differs, or for a run not judged, why, second, and
// no file; then the judge, with the recorded sha256 (for an internal/judge entry, its version and the sha256 of the
// sources that judged the run, which need not be laug's: the run is judged all the same) and laug's own, and why.
// -brief prints the run's one line: "<id>  identical", "<id>  differs: <why>", "<id>  re-rendered: trap <form>" or
// "<id>  not judged: <why>". -all prints that line for every run in -results, by name, under a first line, then the
// counts, "laug · rejudge, N runs: a identical, b known, c re-rendered, d unexplained, e not judged". Exit 0 identical,
// or with -all every run identical, known or re-rendered; 1 a run differs, with -all one unexplained; 2 not judged, or
// no run.
//
// gate compare <a> <b> compares two runs' signatures, S2, in Go, field by field (rejudge.Compare): "laug · S2, A <a>, B
// <b>: equal", or how many of the 16 fields differ, then a line for each, "<field>: A <value> B <value>". Two numbers
// as written are equal when they are as decimals, and two objects whatever their key order, as jq's == has it. Exit 0
// equal, 1 a field differs, 2 a run it cannot read.
//
// gate signature <run> prints the fields S2 compares, under a first line; -json prints them as one object on one line.
// They are rejudge.Signature's 16, read from verdict.json and timeline.jsonl. One is judge, the name
// internal/judge/versions.txt gives the recorded gate/combine.sh sha256, else "sha256:<it>", else the version of an
// internal/judge entry, else null, where S2 had combine_sha256 before the gate judged in Go. So a run judged by a
// version's shell copy and one judged in Go under it compare equal. Exit 0; 2 for a run it cannot read, with the
// reason.
//
// gate tolerance <gate id>... computes the incumbent arm's tolerances from A/A gates, their ids as arguments, in Go:
// internal/tolerance. As in every laug line, a path under $HOME is written ~/ and the rest. It reads each gate in
// -results: verdict.json, gate.json's driver, the two threshold sets, and the verdict JSONs of the two items it
// compares, plan.json's item of arm incumbent and its pair, or with -pair <a>:<b> two plain items instead (a look, not
// a tolerance). -evidence names the file the compare: block cites, in cells/results/ or docs/tables/gates.jsonl. With
// -tables it reads each gate from the summary tables instead, its gates, gate-items, gate-readings and compare rows
// under docs/tables, prints what the directories print under load/steady-state.yaml (TestRowsMatchDirectories; a set
// naming iterations_failed, omes_exit or workflows_not_completed would read a number where the directories read none,
// internal/tolerance/rows.go), and cites docs/tables/gates.jsonl unless -evidence names a file; -tables and -results do
// not go together. The threshold set is laug gate run's, as the script resolves it: load/thresholds.sh "" through
// internal/script, piped through yq -o=json -I=0, with THRESHOLDS and THRESHOLDS_RELEASES unset and LC_ALL=C. laug
// checks each id is a gate id; the script's checks and lines follow, its usage line naming laug gate tolerance, and
// find's line for a directory find cannot read, in find's words. Where the script's jq stopped on an error, laug prints
// jq's line. Two stderr lines differ by design: where jq could not read what it was given, a file that is there and
// cannot be read, an empty gate.json, laug says so in a line of its own in place of jq's parse error; and bash's
// warning for a NUL byte in plan.json's item key, which $(...) drops, laug does not print. It writes nothing. Exit 0
// the table; 2 a usage error, a gate refused, a set that does not resolve or a rule that did not run; 5 when the
// printout stops on a jq error, after the lines before it, as the script's last jq exited.
//
// gate open <run> reads the run's verdict.json, in-process: a first line with the run, its server release, its class
// and whether it stopped early or ran on fakes; the run's directory and its verdict.md, relative to the checkout's
// root, or ~/ and the rest for a run under $HOME, so a line pasted elsewhere names no home path; then the verdict on
// its own line, in the color rule above. Exit 0; 1 when there is no such run or it holds no verdict yet; 2 on a flag.
//
// gate status <gate id> reads workflow gate-<gate id> on the dev server at -orchestrator: the starter's role with
// -status, which prints the workflow's status, started and closed in UTC, then its progress query, the live step, the
// steps done, the quiet passes and the items ended, which needs this checkout's worker. A query writes nothing to
// history. Exit: the starter's, 0; 2 when no server answers or it does not know the workflow.
//
// gate cancel <gate id> asks workflow gate-<gate id> to cancel: the starter's role with -cancel, over the SDK's client,
// as identity laug-gate. It asks the workflow's status first, and refuses, exit 2, unless it runs, since a cancel
// request on an ended workflow changes nothing; then its first line, and the request. Before the judge, the gate's stop
// path stops the live step, writes a BLOCK that names it, hands it on and releases; from the judge on nothing changes.
// Exit 0 asked; 1 the request failed; 2 when no server answers, it does not know the workflow or the workflow does not
// run.
//
// gate terminate <gate id> -reason <why> ends workflow gate-<gate id> at once: the starter's role with -terminate, over
// the SDK's client, history recording 'laug: <why>' as the reason. No workflow code runs, so the gate gets no stop
// path: gate stop runs it by hand after. -reason is required, since the CLI's own named the user, and takes letters,
// digits, blanks and ,.;:()/_=-; bare, the line to type ends with it, -reason <why>. Exit 0 ended; 1 the server refused
// it, a gate that has ended among the causes; 2 with no -reason or one laug refuses, or when no server answers.
//
// gate stop <gate id> runs the gate's stop path by hand, after a terminate, a lost dev server db or a stuck workflow
// task, none of which runs it: the starter's role with -release, with TEMPORAL_ADDRESS and TEMPORAL_NAMESPACE, then
// gate run's environment at its defaults but GATE_RESULTS (-results). The starter stops the live step by its record,
// then runs stopped, post stopped and release stopped, from the gate's gate.json: its server tag, pool and fake replace
// those defaults, and FAKE_GATE_DIR comes from the environment, a seam. It refuses while the workflow runs, and runs
// nothing for a Completed one that released. Exit: the starter's, the gate's 0, 1 or 2; 2 when it refuses.
//
// gate control -name <control> -fake runs one of the controls through a gate on fakes. A control is a change the gate
// must catch, kept in gate/controls/<control>.diff: one comment line that names it and says what it must make the gate
// do, then git diff's output, which git apply reads past that line. laug makes a dir under TMPDIR,
// laug-control-<control>-*, adds a worktree of HEAD there, tree/, by git worktree add --detach, applies the checkout's
// diff to it by git apply, and builds bin/laug in it by make's file rule, as make harness-test does. Then it runs that
// bin/laug's gate run 1.31.0 -fake -results <dir>/results in the worktree, with FAKE_GATE_HARNESS_REAL=1, so the fake
// harness runs make harness-test for real as R1 while the items and the cells stay fakes, and GATE_POOL_LISTEN a free
// port on 127.0.0.1; every other FAKE_*, MAKEFLAGS, MAKELEVEL, MFLAGS, PWD and OLDPWD are dropped, and TMPDIR is the
// dir. The gate's lines come between laug's first line and its last, which says whether the gate did on fakes what the
// control must make it do there, then what it must make a gate on cells do and the recorded runs that did it.
// gate5-always-advance, load/verdict.sh printing ADVANCE before it reads a run, and arm-always-pass, R7 never failing
// in the Go judge, internal/judge, fail make harness-test, so the gate must BLOCK, class harness, on R1, as their
// recorded runs did. gate5-known-bad and persistence-maxqps-50, frontend.rps 5 and history.persistenceMaxQPS 50 in the
// cells' dynamic config, and arm-known-slow, netem on the release cell's Postgres for ARM_SLOW_US microseconds (100;
// ARM_SLOW_*, a seam), act only on real cells: on fakes the gate must ADVANCE with R1 held, so the change leaves make
// harness-test and the gate's own run whole, and laug says so. The worktree and the run go when it ends, after a signal
// too; -keep keeps both and names them. Without -fake it refuses, exit 2: a control on cells is not built. Bare, it
// lists the controls and what each must make the gate do, exit 2. It takes about 4 min on the dev host, most of it make
// harness-test. Exit 0 held; 1 not held, or a step failed, git, make or the gate, which stderr names; 2 refused.
//
// judge <run> judges a gate run's directory in Go, by internal/judge: it makes the run's verdict.json, verdict.md and
// summary under judge version v3, the one new runs get: v2 with a d of -0 printed as zero and the wave reason naming
// laug gate tolerance; -version names another, and -recorded takes the one the run's gate.json records, by its
// gate/combine.sh sha256 or its internal/judge entry. gate/run.sh and gate/step.sh judge every gate run with judge
// -write. The run is a gate id in gate/results or the path to its directory, a relative one from the checkout's root.
// It writes nothing unless asked: its text is the summary, a line that names the two files it did not write, and the
// verdict on its own line. -write writes both into the run's directory, each through a temp file and a rename, and
// prints the summary, the files' line and the verdict. When gate.json records another judge than the one judging, a
// first line on stderr names it and -recorded. -write refuses, exit 2 and nothing written, when gate.json records
// another version that internal/judge/versions.txt names, or when verdict.json is the gate's trap verdict (its
// stopped_at set), since either would put a judged verdict over a recorded one; -overwrite-verdict writes anyway, and
// first says how many of the two files it replaces. A fresh run records the judge that judges it, so the gate's own
// -write is not refused. -json prints verdict.json on one line, and -md verdict.md, each alone. -classify <reason>
// prints the class a reason gets under v3, alone (judge.Classify), for a script to read. -identity prints the judge's
// identity: its version, the sha256 of its sources (internal/judge's and internal/jsonx's .go files that are not tests,
// and versions.txt), the Go that built it, laug's commit, and whether the checkout's internal/judge and internal/jsonx
// hold this judge, committed; -json prints {version, sha256, go, commit, tree_clean}, tree_clean null when git cannot
// tell. Bare, it names no run: the newest in gate/results and its usage, exit 2. Exit 0 ADVANCE, 1 BLOCK; 2 when the
// judge writes no verdict (no such directory, or a run jq would stop on: judge.Judge's error), on a version that does
// not exist or a run that records none, when -write would replace a recorded verdict, or on a flag; -classify and
// -identity exit 0.
//
// thresholds prints the threshold set a server release is judged by: load/thresholds.sh resolves it for -server-tag
// (1.31.0), or takes the set -thresholds names, and load/print-thresholds.sh prints the note, each threshold with its
// max and what no data reads, the release file's overrides, the host its numbers were measured on and the compare:
// block. -server-tag takes a release, x.y.z, or none for the base: a release with no file of its own gets the base, and
// the note says why; any other tag is refused before either script runs, since a script that read only the exit would
// take the base for it. -json prints the set as one JSON object. It writes nothing. Exit 0; 2 when the set does not
// resolve, or on a flag or a tag it refuses.
//
// candidate judges one dynamic config change, <name>, on a leased cell. It leases as raun does, keeps the lease in
// .last-lease.json, then execs faults/candidate.sh <name> .last-lease.json with CANDIDATE_RELEASE=1, so the script
// gives the cell back on every exit, Ctrl-C and SIGTERM among them, and removes the file while it names that cell. It
// sets RUN_ID, POOL_ADDR, OMES_DIR, NAMESPACE (-namespace) and SCENARIO (-scenario), and DURATION (-duration 5m), RATE
// (-rate 20), MAX_CONCURRENT (-max-concurrent 50), POOL_QUIET_WAIT_S (-quiet-wait 300s) and THRESHOLDS (-thresholds)
// only when given. The name must be one the file faults/candidate.sh reads holds, CANDIDATES' when the environment sets
// it, else faults/candidates.yaml, checked before the lease; bare, it lists them and its usage, exit 2. When the exec
// fails, candidate gives the cell back itself. Exit: the script's, 0 the verdict was the candidate's expect, 1 it was
// not, 2 nothing to compare, or a tool's own code; 1 when the lease is refused or the exec fails; 2 on a value it
// refuses.
//
// baseline reads every threshold over one run's whole window, <run-id>, and CPU and memory per service:
// load/baseline.sh by exec, with PROM_URL and FALLBACK_PROM_URL as verdict sets them; PROJECT only when -project is
// given, an empty one skipping the per-service rows; unset, when the last lease (LEASE_FILE, else .last-lease.json) is
// the run's cell and names a guest, the script reads those rows in the guest over its ssh; FORCE=1 with -overwrite,
// which replaces load/results/<run-id>.baseline.json, committed evidence as it may be. Bare, it names no run: its
// usage, exit 2. Exit: the script's, a tool's own code among them; 2 on a value it refuses.
//
// sweep counts one run's workflows, on its task queue omes-<run-id>, by status: laug resolves the cell and the cutoff,
// then runs load/sweep.sh by exec with the address, the task queue and the cutoff as its arguments and NAMESPACE
// (-namespace, default). The cell is the one the run recorded, a raun's address in faults/results before a load run's
// cell in load/results, the first file there deciding; -address wins; a run that recorded none reads the last lease's,
// else port block 0's. The cutoff is -sweep-margin (30s) before the run's end, after_end for a raun and end for a load
// run, else before now; -cutoff, RFC 3339, wins. One stderr line names the cell, where it came from and the cutoff.
// Bare, it names no run: its usage, exit 2. Exit: the script's, 0, or the temporal CLI's when the count fails; 2 on a
// value it refuses.
//
// cell runs one compose cell on this host. up, down, mem, health and dynconfig-check run cells/up.sh, down.sh, mem.sh,
// health.sh and dynconfig-check.sh; fi-template-check and softasserts run cells/fi-template-check.sh and
// cells/softasserts.sh. Each sets its script's knobs or arguments and runs it by exec; cell down -json keeps it a
// child. A pool cell is compose project laug-<id>. up acts. down destroys: bare, it removes nothing and lists what
// -remove-volumes would remove. The rest read: they change nothing in the tree or the pool; health starts one
// HealthCheck workflow on the cell, and dynconfig-check -template and fi-template-check may have docker pull a server
// image.
//
// cell up brings one cell up and waits until it serves. It sets ENABLE_ES and COMPOSE_PROFILES from -visibility
// (postgres, or es for Elasticsearch), BIND_ADDR from -bind (loopback, 127.0.0.1, or all, 0.0.0.0: the script waits on
// 127.0.0.1, so no single interface's address), SERVER_TAG (-server-tag, 1.31.0), the five ports from -block N (0 to
// 99: 7233, 8080, 9090, 3000 and 8474, each plus 10N), PROJECT (-project, laug), SERVER_FI (-server-fi), CELL_ADDRESS
// and PROM_URL (the block's frontend and Prometheus on 127.0.0.1), NAMESPACE (-namespace, default) and VISIBILITY.
// -server-fi lays cells/compose/fi.yml over cell.yml, its fault set by LAUG_FI_* in the environment, and runs only on
// the tag cells/compose/config/laug-server.yaml was vendored from. Its first line names the project, the tag, the
// visibility, the block's ports and their address, and the line that removes the cell. The script runs docker compose
// up -d, goes on however that exits, and waits 300 s at most for cluster health, the namespace and every shard owned;
// its last two lines say how long that took and where the UI, Prometheus and Grafana answer. Exit: the script's, 0
// serving, 1 not serving after 300 s, 2 -server-fi on another tag; 2 as well on a value laug refuses, or outside a
// checkout.
//
// cell down -remove-volumes tears -project's cell down, laug by default: docker compose down -v, the es profile's too,
// which removes its containers, its network and its volumes, Postgres's and Prometheus's data. That cannot be undone,
// so it takes a flag that names the loss, and first it lists what goes with docker from PATH, the containers, volumes
// and networks that carry the project's compose label. Bare, or with -json or -dry-run alone, it removes nothing: it
// prints that list on stderr, the count and the names of each, and the line that removes them, exit 2. With
// -remove-volumes its first line counts what goes, then the script runs. A project in a pool cell's shape, laug-<8
// hex>, is asked of the pool on 127.0.0.1:7700 first: when that pool lists the cell, -remove-volumes refuses it, exit
// 2, since the pool would still count a cell removed behind it, and names laug release <id> for a leased cell or laug
// pool down -remove-cells; bare says so after its list. A cell no pool lists, one a crashed pool left, is removed.
// -json prints what went once compose exits 0, one object: project, containers, volumes and networks, each a list of
// names; the first line and compose's go to stderr. It sets what cell up sets but CELL_ADDRESS, PROM_URL, NAMESPACE and
// VISIBILITY, every one at its default but PROJECT. -dry-run with -remove-volumes lists nothing. Exit: docker
// compose's; 1 when docker cannot list what goes, and nothing is removed; 2 bare, with no docker on PATH, on a value
// laug refuses, on a cell the pool lists, or outside a checkout.
//
// cell mem prints the memory -project's containers hold now, from one docker stats: a row a container on stderr, then
// the total in MiB on stdout. With no container in the project, docker stats reads every running container. It sets
// what cell down sets. Exit: the script's, a docker call's own code when it fails.
//
// cell health checks the cell at -address, else 127.0.0.1 and -block's frontend port: cluster health answers; one
// HealthCheck workflow round-trips in -namespace and is visible, through bin/laug health check, which cells/health.sh
// runs with neither CELL_ADDRESS nor NAMESPACE in its environment; and its Prometheus, -prom, else -block's, has 5
// server targets up within 20 s. It sets CELL_ADDRESS, NAMESPACE and PROM_URL. Exit: the script's, 0 healthy, 1 fewer
// than 5 targets up or the round trip failed, the temporal CLI's code when it fails, 2 when bin/laug is not built.
//
// cell dynconfig-check checks a cell's laug.yaml with the server's own validator, in its temporal-history container:
// -project's, or with -cell <id> pool cell laug-<id>'s. When the last lease (LEASE_FILE, else .last-lease.json) is that
// cell and names a guest, it is a microVM cell: the script gets the lease file as its argument and runs docker in the
// guest over the lease's ssh, and the first line says so. -template checks the template,
// cells/compose/dynamicconfig/laug.yaml, with no cell, in temporalio/server:<-server-tag>. The validator exits 0 on its
// own warnings, so a warning: or error: line fails. It sets NOCELL (1 with -template), SERVER_TAG and PROJECT. Exit:
// the script's, 0 clean, 1 a line flagged, 2 not one temporal-history container in the project or a microVM lease this
// host cannot reach (faults/lib.sh's lease_read), or the code of the validator or docker; 2 as well on -cell with
// -project, a flag -template does not take, or -server-tag without it.
//
// cell fi-template-check checks cells/compose/config/laug-server.yaml, the server config -server-fi lays over a cell,
// against temporalio/server:<-server-tag>, 1.31.0 by default: cells/fi-template-check.sh <tag>, whose first line names
// the tag it checks and the tag the file was vendored from, then a line for each of its six checks. It needs docker and
// go, and no cell. Exit: the script's, 0 every check passed, 1 one failed, 2 it could not check.
//
// cell softasserts counts the server's soft assertions per role from docker logs, one JSON object under laug's first
// line: cells/softasserts.sh, -since and -until as its --since and --until, then -project, or laug-<id> for -cell <id>.
// Each role counts from its container's last start unless -since says, to now unless -until says; both take what docker
// logs takes. A role whose lines from then were partly rotated away reads null. Exit: the script's, 0; 1 a role not
// read in full, the object printed all the same; 2 no server role container in the project.
//
// host holds what a gate checks about this host. host type runs gate/host-type.sh by exec. With -declare <type> it
// first declares this host's type, as EC2 names one (m8i.8xlarge), one line in $XDG_CONFIG_HOME/laug/host-type
// (~/.config when unset), outside the repository; then, and bare, it prints the declaration, this host's vCPU and
// memory, whether this host is a VM (microVM cells then run nested), the threshold set's host and whether a gate
// refuses this host. Bare, it writes nothing. laug checks -declare's shape first, exit 2. -json keeps the script a
// child with JSON=1, which prints one object on one line in place of the lines: declared (null for none), vcpu,
// mem_gib, virtualized (a hypervisor flag in /proc/cpuinfo), set (the threshold set's host: type, vcpu, mem_gib),
// passes and reason (null when a gate passes this host). Exit: the script's, 0; 2 when the set does not resolve or the
// declaration cannot be written; a tool's own code under -json is 2, with a line.
//
// host clock runs cells/clock-check.sh by exec for -for (60s, whole seconds, 2s or more), a sample a second: the wall
// clock against /proc/uptime, the ratio and any step over 0.5 s. It writes nothing. Exit: the script's, 0 once it has
// measured, 2 when it cannot, or a tool's own code.
//
// health runs the HealthCheck workflow on a cell. check, loop, record and timer run internal/health's roles in laug's
// own process: CheckMain, the round trip, and TimerMain, the timer; check, loop and timer need no checkout. replay runs
// go test in internal/health, which make test-go runs too. The cell is -address, 127.0.0.1:7233, port block 0's
// frontend, and the namespace -namespace, default: the roles read neither CELL_ADDRESS nor NAMESPACE from the
// environment, and a stray copy of either gets its stderr line. Only timer reads the last lease.
//
// health check runs one HealthCheck workflow on the cell, -metrics "" for no SDK metrics: it starts a worker in laug's
// own process, the workflow, its signal and its result, then asks visibility for the run, -visibility-wait at most, 15s
// unless given; a value that is not a Go duration over 0 is refused, exit 2. cells/health.sh runs it, and the compose
// and Firecracker backends' health verbs with -visibility-wait 10s. Exit 0 the round trip held, 1 it did not.
//
// health loop runs that round trip every -every (10s) until Ctrl-C, with the SDK's metrics on :9091, every interface,
// for the cell's sdk-workers scrape target, so Grafana has a series to draw. Exit 0 stopped by Ctrl-C between round
// trips, 1 a round trip failed.
//
// health record runs one round trip and writes its history over internal/health/testdata/healthcheck_history.json, the
// committed replay fixture, then runs replay on it; the round trip records as identity laug-healthcheck and writes the
// sticky queue's host as "host", so the fixture names no machine. Without -overwrite-fixture, bare among them, it
// writes nothing: it says what would go, the fixture's bytes and events, and the line to type, exit 2. Exit: the round
// trip's when it fails, else replay's.
//
// health timer [<duration>] arms one durable timer, 60s unless named, on the leased cell, else 127.0.0.1:7233, or
// -address: TimerMain, in laug's own process, notes the server's clock, sleeps the timer, and has an activity on this
// host stamp the time it fired; it prints when the timer was due and when it fired, in this host's time. -id names the
// workflow, timer-probe-<Unix ms> unless given. Exit 0 fired; 1 it failed, or Ctrl-C ended the wait.
//
// health replay [<file>] runs the replay tests in internal/health: go test ./... -run Replay -v -count=1, as a child,
// its stdout and stderr together through a filter, so only the test results and the lines that say why a build failed
// print. The replay passes on the recorded history and fails with the activities swapped, and the nine changes to
// HealthCheck a review replayed by hand replay as they did, five clean and four with TMPRL1100. <file>, a history the
// round trip's -export wrote, replays in place of the committed fixture, as REPLAY_HISTORY; a relative path starts at
// the checkout's root, and one that is not a file with something in it is refused, exit 2. Exit: go test's, 0 every
// replay passed, 1 one failed or the package did not build.
//
// orchestrator runs its own Temporal dev server and the programs around it: the dev server, not a cell, on
// 127.0.0.1:7400, HTTP 7401, metrics 7402 and UI 8400, clear of the ports of the 17 blocks a pool holds at most (pool
// serve's -max), laug pool serve's 7700 and the gate's Omes ports; this checkout's worker, which runs gate/step.sh one
// step at a time for workflow Gate; the probe; and the recorder of the replay fixtures. make test-go runs
// internal/orchestrator's tests: the SDK's test env, the fake gate, the six replay fixtures and the parity matrix.
// worker, probe and record run internal/orchestrator's roles, WorkerMain, ProbeMain and RecordMain, in laug's own
// process, and need a checkout. -orchestrator, 127.0.0.1:7400, is the server: they get it as TEMPORAL_ADDRESS, with
// TEMPORAL_NAMESPACE default. gate run -workflow, gate status, cancel, terminate and stop reach the same server.
//
// orchestrator server runs the dev server in the foreground: temporal server start-dev by exec, every port pinned,
// since the CLI's own and the SDK's default, 7233, is block 0's cell frontend during a gate, and its HTTP and metrics
// ports are random unless named. -orchestrator must be 127.0.0.1:<port>: the server has no auth. -http-port (7401),
// -metrics-port (7402) and -ui-port (8400) name the rest. Its state is one SQLite file, orchestrator/run/dev.db,
// gitignored, so a running server never makes a gate's tree dirty. Exit: the CLI's; 2 on a value laug refuses.
//
// orchestrator worker runs this checkout's worker in the foreground, in laug's own process, on the task queue
// orchestrator/run/queue names, as laug-worker@<commit>, the commit make build stamped into bin/laug. Before it polls,
// it replays every open Gate on its queue on this build, and refuses to poll, exit 2, when one does not replay. laug
// refuses, exit 2, when it was not built from HEAD, by that stamp: a gate refuses a worker whose commit is not HEAD's.
// It refuses a TEMPORAL_SDK_FLAG_* or TEMPORAL_DEBUG in laug's environment too, which the SDK read when laug started.
// -otlp 127.0.0.1:<port> traces workflow Gate to the stack's Collector. Exit 0 stopped; 2 refused.
//
// orchestrator probe probes this checkout's worker, in laug's own process: it starts the Probe workflow, compares host
// time with history time, runs Idle for -idle (60s, whole seconds; 0 skips it) heartbeating every 2 s, and samples the
// server's and the worker's RSS and CPU every 5 s into orchestrator/run/<workflow id>.jsonl. The server is the pid ss
// names on -orchestrator's port, 0 when it names none, and CPU counts in getconf CLK_TCK's ticks. -unpolled sends one
// HostStamp to <queue>-unpolled as well, which no worker polls. It refuses when no worker holds this checkout's lock.
// Exit 0 done; 1 the workflow failed; 2 refused, Ctrl-C, which asks the server to cancel the probe, or getconf giving
// no ticks.
//
// orchestrator record <gate id> <name> -overwrite-fixture writes a finished Gate's history as a replay fixture,
// internal/orchestrator/testdata/histories/<name>.json, one of six, advance, refused, harness, cancelled, adopted and
// real, in laug's own process: it scrubs host and user names, paths, the queue and boot ids, then checks every string
// and decoded payload against them and the deny list LAUG_DENYLIST names, if any, read as check/denylist.sh reads it; a
// leak writes nothing and exits 2. Without -overwrite-fixture it writes nothing: it says which fixture would go, its
// bytes and events, and the line to type, exit 2; bare, it lists the six. Exit 0 written; 2 refused or a leak.
//
// deps checks the tools laug's scripts call, a line each: ok, MISSING or note, the tool, the version it gives, and
// which scripts call it. Bare, it checks go, git, docker compose, temporal, jq, yq, bc, curl, ss, setsid and flock; GNU
// coreutils' env, 8.31 or later for --default-signal (gate/lib.sh, sigdefault), and GNU diffutils' cmp and diff
// (load/testdata/harness-test.sh, cells/fi-template-check.sh); then /dev/kvm, a note when absent. After go and git
// comes bin/laug, held to HEAD as gate run holds it: its version is its stamp and its need HEAD's commit; built from
// another commit, or not built, it is MISSING, since then gate run -fake refuses it and gate rejudge judges nothing,
// and a reader who pulled and did not build is in that state before anything says so; built from HEAD with changes, in
// a tree that has some, or outside a checkout, it is a note. Go is held to 1.26.5, the newer of go.mod's 1.26.0 and
// Omes's, asked from / so GOTOOLCHAIN switches nothing; an older Go under GOTOOLCHAIN=auto is a note, since its first
// build downloads the newer one. curl is held to 7.76.0, for --fail-with-body; jq to 1.7 or later; yq to mikefarah/yq
// v4; temporal to a server start-dev that takes --metrics-port. -offline checks only what the offline path calls: make
// build, thresholds, gate run -fake, gate rejudge and make harness-test, read from load/thresholds.sh, gate/lib.sh,
// gate/run.sh and load/testdata/harness-test.sh. make harness-test, part of runbook 1's make test, stops without bc or
// ss. -offline's 12 checks are go (1.26.0), git, bin/laug, jq, yq, bc, curl, ss, setsid, flock, env, and cmp and diff
// as one. It leaves out docker, temporal and /dev/kvm: gate run -fake puts stand-ins for docker, ss, nproc, free and
// Omes's go build first on PATH (load/testdata/gate/fakebin), and nothing offline calls temporal or /dev/kvm. Its last
// line says so. -cell checks what a lease, a load and a raun on compose cells call, the runbooks that need a cell: Go
// held to Omes's 1.26.5, git, bin/laug, docker compose, temporal, jq, yq, bc, curl, ss, setsid, cmp and diff, a docker
// daemon that answers and Omes, and not flock, env, /dev/kvm or the host's size and type, which only a gate or the
// Firecracker backend needs. -lab adds to bare deps' checks what a gate on a lab host needs: a docker daemon that
// answers docker version, Omes in .upstream/omes at f0de206, 16 vCPU and 8 GiB of memory available, under which
// gate/run.sh's preflight refuses, and the host type, what gate/host-type.sh answers laug host type -json: its version
// the type this host declares, its need the threshold set's, MISSING with the reason when a gate on cells would refuse
// the host. Each tool is found on PATH and asked its version from /, 20 s at most. -json prints one object: set,
// missing, missing_offline, and checks, each with name, status, version, need and why. It writes nothing. Exit 0 when
// nothing the offline path calls is missing, so a host or a CI runner with no Docker or temporal CLI passes bare deps,
// and its first line and last say so; 1 when a tool the offline path calls is missing, as -offline judges it, Go held
// to go.mod's 1.26.0; with -cell or -lab, 1 when any check is missing; notes never count. 2 on a flag, an argument, or
// two of -offline, -cell and -lab.
//
// upstream clones Omes into .upstream/omes at -ref, f0de206 by default, the commit every committed result used
// (omes_commit in load/results): a clone with no checkout from -url, https://github.com/temporalio/omes.git, when
// .upstream/omes holds none; a fetch of -ref from the clone's origin only when the clone lacks that commit; a detached
// checkout; then its submodules. load, fault, raun, candidate and gate run build Omes from there. -ref takes the full
// commit, as the default is, since a fetch by commit needs the full name; a tag or a branch works too. Its first line
// says what it starts and that Ctrl-C stops it; the clone, the fetch, the checkout and the submodules' update run
// --quiet, so git prints only its errors; and the last line names the commit .upstream/omes is at. A signal stops git
// with SIGTERM, and git clone removes a clone it had not finished. It writes in .upstream/ alone. Exit 0 ready; 1 when
// a git step fails or a signal stops it, the line naming the step; 2 on a flag, an argument or a value it refuses,
// outside a checkout, or with no git on PATH.
//
// version prints "laug · version <commit> go<version> server <tag> omes <ref>": the commit make build stamped, cut to 7
// hex digits, with -dirty when git status printed anything at build time; for a plain go build or go install in a
// checkout, the commit Go's VCS stamp records, with -dirty for a modified tree; dev when neither says; the Go version
// the binary was built with; the server tag a cell runs by default (SERVER_TAG_DEFAULT in the Makefile, which its
// Firecracker targets read); and the Omes commit every result used (OMES_REF). In a checkout that holds Omes's clone it
// adds ", .upstream/omes at <commit>", with ", not <ref>" when that is another commit. It reads that commit from
// .upstream/omes/.git as git rev-parse HEAD reads it, a detached HEAD or a branch in refs/ or packed-refs, with no git.
// -json prints one object, {"commit", "go", "server_tag", "omes_ref", "omes_commit"}, the commits in full, omes_commit
// null with no clone. It writes nothing and exits 0, or 2 on a flag or argument it does not take. --version and
// -version, in a command's place, run it.
//
// runbook holds the runbooks: docs/runbooks/*.md, a Markdown page a task, from trying laug offline to a lab batch,
// whose command blocks a reader types and check runs alike. Runbook 1 is the long walk README.md's block is cut from.
// check is its one verb.
//
// runbook check <file>... runs a runbook's command blocks and holds each to the lines it prints. A command block is a
// fence, 0 to 3 spaces in, whose info string's first word is sh; a text fence right after it, with blank lines alone
// between, holds its expected lines. Words after sh mark the block: needs=cell for one that needs Docker and cells on
// this host, which runs only with -cell; needs=lab for one that needs the lab host, which runs only with -lab; exit=<n>
// for the exit it ends with, 0 unless marked. The blocks with no needs mark are the offline ones. Each block runs as
// bash --noprofile --norc -e -o pipefail -c with its lines, bash found on PATH, from the checkout's root, with laug's
// whole environment, as the shell a reader types it in has it, stdin from /dev/null, and in a process group of its own;
// its lines are its stdout and stderr, in one pipe. So a block carries nothing to the next but files. It holds when it
// exits as marked and its lines match the expected ones line for line, trailing blanks aside: ... in a line matches any
// text, and a line of ... alone any number of lines; a block with no text fence after it is held to its exit alone.
// Once bash ends, check waits 5 s at most for the pipe, then sends SIGTERM to what the block's group still runs,
// SIGKILL 5 s on, and the block fails: a block stops what it starts. A zombie, a process that has ended and waits for
// its parent or PID 1 to reap it, runs nothing. A process whose main thread alone has exited, which ps shows as Z too,
// still runs. Every file is read first, so one that is not a runbook runs nothing. The first line counts the blocks it
// runs and skips, then a line a block: ok, skipped or FAILED, the file and line of its fence, its first command and its
// seconds; under a failure, why: its exit against the one marked, the expected line that matched nothing after a -, and
// the lines it was held to, or its last ones, each after a +, 8 at most. The last line counts the blocks that held. A
// relative <file> starts at the checkout's root. Bare, it runs nothing: it lists docs/runbooks' pages, each with its
// blocks by what they need, exit 0. make test runs the offline blocks of every runbook but 1 through check, outside a
// gate, in load/testdata/harness-test.sh's runbook section; runbook 1 is the long walk, which the case before it runs
// line by line, its last line, make test, being that test. Ctrl-C sends SIGTERM to the running block's group and stops.
// Exit 0 every block run held, or none ran; 1 a block failed, or a signal stopped one; 2 on a flag, -cell or -lab with
// no file, a file that cannot be read or is not a runbook (a fence not closed, a mark it does not take, a command block
// with no command, no command block), no bash on PATH, or outside a checkout.
//
// help prints where to start, offline with no Docker (deps, gate run 1.31.0 -fake and runbook check), four words (cell,
// raun, gate and Omes), the command list by group, how to get a command's verbs or flags, and the exit codes. help
// <command> prints that command's verbs, a line each with its summary, or its flags when it has one verb, as <command>
// -h does; help <command> <verb> prints that verb's flags, as <command> <verb> -h does. A third argument, or a verb for
// a command with one, is refused, exit 2.
package main
