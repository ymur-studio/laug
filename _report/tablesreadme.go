package report

// tablesreadme.go is the summary tables' schema, a table's key, its columns in order with each one's type and source,
// and docs/tables/README.md, which report -tables writes from it. checkSchema holds every row of a table to its
// schema's columns, in order: TestTables for every table the committed records give; TestTablesColumns and
// TestTablesRecordsColumns for the fixtures' tables.

import (
	"fmt"
	"strings"
)

// TablesReadme is the generated README's name in docs/tables.
const TablesReadme = "README.md"

// A tableSchema is one table as README.md describes it. Its texts are Markdown: a placeholder such as <gate> sits in
// backticks with the path or key around it, since a bare one reads as an HTML tag and GitHub drops it
// (TestTablesReadmePlaceholders).
type tableSchema struct {
	name    string // the table's name; readings-<batch> stands for each batch's
	row     string // what a row is
	key     string // the id column's text
	source  string // the records it is read from
	columns []tableColumn
}

// A tableColumn is one column: its name, its type and where its value comes from.
type tableColumn struct{ name, typ, source string }

// Column types, as README.md names them. "as recorded" is whatever JSON the record holds there.
const (
	tString   = "string"
	tStringN  = "string or null"
	tNumber   = "number"
	tNumberN  = "number or null"
	tBool     = "bool"
	tBoolN    = "bool or null"
	tObjectN  = "object or null"
	tArray    = "array of string"
	tArrayN   = "array of string, or null"
	tRecorded = "as recorded"
)

// readingsSchema is the name README.md gives each batch's readings table.
const readingsSchema = readingsPrefix + "<batch>"

// tableSchemas are the tables in README.md's order.
var tableSchemas = []tableSchema{
	{"gates", "a gate run", "the gate id", "`gate/results/<id>/`: verdict.json, gate.json, plan.json, " +
		"thresholds.json, thresholds-incumbent.json", []tableColumn{
		{"id", tString, "the run's directory name"},
		{"record", tString, "`gate/results/<id>/`"},
		{"verdict_sha256", tStringN, "sha256 of verdict.json"},
		{"gate_sha256", tStringN, "sha256 of gate.json"},
		{"server_tag", tRecorded, "verdict.json"},
		{"incumbent", tRecorded, "verdict.json"},
		{"plan", tRecorded, "gate.json"},
		{"driver", tRecorded, "gate.json; null in the runs before it was recorded"},
		{"fake", tRecorded, "verdict.json"},
		{"rerun", tRecorded, "verdict.json"},
		{"tree_dirty", tRecorded, "verdict.json"},
		{"laug_commit", tRecorded, "verdict.json; a commit of laug's working history, which is not public"},
		{"combine_sha256", tStringN, "the gate/combine.sh sha256 gate.json records as its judge"},
		{"judge_entry", tStringN, "the internal/judge version gate.json records as its judge"},
		{"judge_sources", tStringN, "that version's sources' sha256, as gate.json records it"},
		{"host", tObjectN, "gate.json host: type, vcpu, mem_gib, mem_available_gib"},
		{"t0", tStringN, "verdict.json t0.wall's date, UTC: the gate's start"},
		{"t_post", tStringN, "verdict.json t_post.wall's date, UTC: its post"},
		{"elapsed_s", tRecorded, "verdict.json"},
		{"items_in_plan", tRecorded, "verdict.json"},
		{"deadline_s", tRecorded, "verdict.json"},
		{"verdict", tRecorded, "verdict.json: ADVANCE or BLOCK"},
		{"class", tRecorded, "verdict.json"},
		{"class_if_quiet", tRecorded, "verdict.json"},
		{"refused", tRecorded, "verdict.json"},
		{"stopped_at", tRecorded, "verdict.json"},
		{"deciding", tStringN, "the rule that decided it: R1 to R7, refused or stopped; null for an ADVANCE"},
		{"deciding_reasons", tArray, "that rule's reasons in verdict.json, the refusal for a refused gate, [] else"},
		{"rules", tRecorded, "verdict.json rules, R1 to R7 with name, pass and reasons; null in a trap verdict"},
		{"reasons", tRecorded, "verdict.json"},
		{"host_quiet", tRecorded, "verdict.json"},
		{"host_noise", tRecorded, "verdict.json"},
		{"comparison", tObjectN, "verdict.json comparison: mode, wave, wave_measured, measured_with, evidence, " +
			"gates; its rows are the compare table's"},
		{"arm_pair", tObjectN, "plan.json: a, the key of the first item of arm incumbent, and b, its pair"},
		{"th_not_judged", tArrayN, "the readings thresholds.json marks no_data not_judged"},
		{"thi_not_judged", tArrayN, "the same of thresholds-incumbent.json"},
		{"known_slow", tArray, "the item directories that hold known-slow.json, sorted"},
		{"files", "object", "poll, harness, item_dirs: whether poll.jsonl, harness.json and an item directory " +
			"are there"},
		{"boot", tStringN, "the host boot it ran in, as an id: b and t0's wall clock less its monotonic clock, " +
			"floored to the second, in UTC, `b<date>-<time>`; one boot's runs share it; null when t0 is not two " +
			"numbers"},
	}},
	{"gate-items", "an item of a gate run", "`<gate>/<key>`", "verdict.json items, and each item's " +
		"`<gate>-<key>.verdict.json` (a load) or `<gate>-<key>.json` (a raun)", []tableColumn{
		{"id", tString, "`<gate>/<key>`"},
		{"gate", tString, "the gate id"},
		{"key", tRecorded, "items[].key"},
		{"record", tString, "`gate/results/<gate>/<key>/`"},
		{"sha256", tStringN, "sha256 of the item's verdict or raun JSON; null when it is not there"},
		{"kind", tRecorded, "items[]"},
		{"arm", tRecorded, "items[]; null in the runs before the arm"},
		{"expect", tRecorded, "items[]"},
		{"observed", tRecorded, "items[]"},
		{"class", tRecorded, "items[]"},
		{"reason", tRecorded, "items[]"},
		{"failed", tRecorded, "items[]: the rules the item failed"},
		{"run_id", tRecorded, "items[]"},
		{"cell", tRecorded, "items[]"},
		{"exit", tRecorded, "items[]"},
		{"stopped_by", tRecorded, "items[]"},
		{"block", tRecorded, "items[]"},
		{"pool_busy", tRecorded, "items[]"},
		{"verdict", tRecorded, "the load's verdict JSON"},
		{"window_s", tRecorded, "the load's verdict JSON"},
		{"short_window", tRecorded, "the load's verdict JSON"},
		{"deciding", tRecorded, "the load's verdict JSON: the readings that decided it"},
		{"thresholds_release", tRecorded, "the load's verdict JSON"},
		{"verdict_file", tBool, "the item's verdict or raun JSON is there and is one object"},
		{"fault", tObjectN, "a raun item's JSON: result, reason, check (as the rauns table's), " +
			"time_to_recover_s, iterations_failed"},
	}},
	{"gate-readings", "a reading of a gate item's load verdict", "`<gate>/<item>/<reading>`", "each load item's " +
		"verdict JSON: readings, then the metrics of rows that readings lacks; thresholds.json or, for the item " +
		"of arm incumbent, thresholds-incumbent.json", []tableColumn{
		{"id", tString, "`<gate>/<item>/<reading>`"},
		{"gate", tString, "the gate id"},
		{"item", tString, "the item's key"},
		{"reading", tString, "the reading's name"},
		{"pos", tNumber, "its place among the item's readings, from 0"},
		{"value", tRecorded, "readings, else the row's value"},
		{"limit", tRecorded, "its row in rows; null with no row"},
		{"result", tRecorded, "its row; a time in it cut to its date"},
		{"blocks", tRecorded, "its row"},
		{"deciding", tBool, "the verdict's deciding names it"},
		{"no_data", tRecorded, "the thresholds file's no_data for it"},
	}},
	{"compare", "a reading R7 compared, release against incumbent", "`<gate>/<reading>`", "verdict.json " +
		"comparison.rows", []tableColumn{
		{"id", tString, "`<gate>/<reading>`"},
		{"gate", tString, "the gate id"},
		{"reading", tRecorded, "rows[].name"},
		{"pos", tNumber, "its place in rows, from 0"},
		{"scale", tRecorded, "rows[]"},
		{"release_item", tRecorded, "comparison.release"},
		{"incumbent_item", tRecorded, "comparison.incumbent"},
		{"release", tRecorded, "rows[]"},
		{"incumbent", tRecorded, "rows[]"},
		{"d", tRecorded, "rows[]"},
		{"tolerance", tRecorded, "rows[]"},
		{"judged", tRecorded, "rows[]"},
		{"result", tRecorded, "rows[]: ok, WORSE, recorded, not produced or not compared"},
	}},
	{"rejudge", "a gate run judged again by one judge", "`<gate>/<the judge's sha256, first 12 hex>`", "the run " +
		"judged again by this tree's judge (internal/rejudge)", []tableColumn{
		{"id", tString, "`<gate>/<judge sha256, 12 hex>`"},
		{"gate", tString, "the gate id"},
		{"judge_sha256", tString, "the judging judge's sources' sha256, as laug judge -identity prints it"},
		{"outcome", tString, "identical, known, re-rendered, unexplained or not judged"},
		{"why", tStringN, "why, for any outcome but identical"},
	}},
	{"rauns", "a raun or candidate JSON", "its file name less .json (a candidate's run_id repeats across " +
		"candidates)", "`faults/results/<name>.json`", []tableColumn{
		{"id", tString, "the file name less .json"},
		{"record", tString, "`faults/results/<name>.json`"},
		{"sha256", tString, "sha256 of the file"},
		{"run_id", tRecorded, "run_id"},
		{"kind", tString, "candidate when the file has candidate, else raun"},
		{"fault", tRecorded, "the raun's"},
		{"server_tag", tRecorded, "the file's"},
		{"backend", tRecorded, "the file's"},
		{"scenario", tRecorded, "the raun's"},
		{"cell", tRecorded, "the file's"},
		{"rate", tRecorded, "the raun's"},
		{"signal", tRecorded, "the raun's"},
		{"signal_max", tRecorded, "the raun's"},
		{"signal_peak", tRecorded, "the raun's"},
		{"signal_base", tRecorded, "the raun's"},
		{"result", tRecorded, "result, or a candidate's verdict: ADVANCE or BLOCK"},
		{"reason", tRecorded, "reason"},
		{"check", tStringN, "the check that decided a BLOCK: the faults/run.sh check its reason starts with " +
			"(steady, signal, inject, landed, remove, recovered, after window, lost work), or load verdict for a " +
			"candidate (faults/candidate.sh, step 7); null for an ADVANCE. -tables refuses a BLOCK no check names"},
		{"deciding_window", tStringN, "the window whose readings decided it: before for steady, during for " +
			"landed, after for after window; null else"},
		{"date", tStringN, "inject_at's date, UTC"},
		{"landed_at_s", tNumberN, "landed_at less inject_at, in seconds"},
		{"remove_at_s", tNumberN, "remove_at less inject_at, in seconds"},
		{"recovered_at_s", tNumberN, "recovered_at less inject_at, in seconds"},
		{"after_start_s", tNumberN, "after_start less inject_at, in seconds"},
		{"after_end_s", tNumberN, "after_end less inject_at, in seconds"},
		{"time_to_recover_s", tRecorded, "the raun's"},
		{"remove_wait_s", tRecorded, "the raun's"},
		{"history_shards_ready_s", tRecorded, "the raun's"},
		{"settle_s", tRecorded, "the raun's"},
		{"after_s", tRecorded, "the raun's"},
		{"windows", "object", "before, during, drain, after: each window's readings, whole, a time in it cut to its " +
			"date"},
		{"readings", tRecorded, "the fault's own readings"},
		{"iterations_failed", tRecorded, "the raun's, by window"},
		{"pool_busy", tRecorded, "the file's"},
		{"mechanisms", tRecorded, "the raun's, each one's at as at_s, its seconds after inject_at"},
		{"shards_acquired", tRecorded, "the raun's"},
		{"server_fi", tRecorded, "the raun's"},
		{"candidate", tRecorded, "the candidate's"},
		{"key", tRecorded, "the candidate's"},
		{"from", tRecorded, "the candidate's"},
		{"value", tRecorded, "the candidate's"},
		{"expect", tRecorded, "the candidate's"},
		{"blocks_on", tRecorded, "the candidate's"},
		{"deciding", tRecorded, "the candidate's: the readings that decided its load's verdict"},
		{"outcome", tRecorded, "the candidate's"},
		{"outcome_reason", tRecorded, "the candidate's"},
		{"prediction", tRecorded, "the candidate's"},
		{"wait_s", tRecorded, "the candidate's"},
		{"reread_s", tRecorded, "the candidate's"},
		{"rows", tRecorded, "the candidate's load verdict rows, a time in a result cut to its date"},
		{"load", tObjectN, "the candidate's load: duration, rate, max_concurrent, window_s"},
	}},
	{"loads", "a load", "its run id; a candidate's load copy, `faults/results/load/<name>`", "`load/results/" +
		"<id>.json`, `<id>.baseline.json`, `<id>.verdict.json`; `faults/results/load/<name>.json`", []tableColumn{
		{"id", tString, "the run id, or `faults/results/load/<name>`"},
		{"record", tString, "`load/results/<id>.*`, or `faults/results/load/<name>.json`"},
		{"files", tArray, "the record's file names, sorted"},
		{"run_sha256", tStringN, "sha256 of the run JSON"},
		{"verdict_sha256", tStringN, "sha256 of the verdict JSON; null with none"},
		{"baseline_sha256", tStringN, "sha256 of the baseline JSON; null with none"},
		{"run_id", tRecorded, "the run JSON"},
		{"server_tag", tRecorded, "the run JSON"},
		{"server_version", tRecorded, "the run JSON"},
		{"backend", tRecorded, "the run JSON"},
		{"scenario", tRecorded, "the run JSON"},
		{"rate", tRecorded, "the run JSON"},
		{"duration", tRecorded, "the run JSON"},
		{"max_concurrent", tRecorded, "the run JSON"},
		{"iterations", tRecorded, "the run JSON"},
		{"start", tRecorded, "the run JSON's, cut to its date"},
		{"end", tRecorded, "the run JSON's, cut to its date"},
		{"omes_commit", tRecorded, "the run JSON"},
		{"omes_exit", tRecorded, "the run JSON"},
		{"iterations_failed", tRecorded, "the run JSON"},
		{"verdict", tRecorded, "the verdict JSON: ADVANCE or BLOCK; null with no verdict"},
		{"exit", tRecorded, "the verdict JSON"},
		{"deciding", tRecorded, "the verdict JSON: the readings that decided it"},
		{"readings", tRecorded, "the verdict JSON"},
		{"window_s", tRecorded, "the verdict JSON"},
		{"short_window", tRecorded, "the verdict JSON"},
		{"thresholds_release", tRecorded, "the verdict JSON"},
	}},
	{"load-readings", "a reading of a load's verdict", "`<load>/<reading>`", "the verdict JSON: readings, then " +
		"the metrics of rows that readings lacks; the baseline JSON", []tableColumn{
		{"id", tString, "`<load>/<reading>`"},
		{"load", tString, "the loads row's id"},
		{"reading", tString, "the reading's name"},
		{"pos", tNumber, "its place among the load's readings, from 0"},
		{"value", tRecorded, "readings, else the row's value"},
		{"limit", tRecorded, "its row in rows; null with no row"},
		{"result", tRecorded, "its row; a time in it cut to its date"},
		{"blocks", tRecorded, "its row"},
		{"deciding", tBool, "the verdict's deciding names it"},
		{"baseline", tRecorded, "the baseline JSON's value of it; null with none"},
	}},
	{readingsSchema, "a reading of a batch, one table a batch, its name in lowercase",
		"`<step>/<name>`, then #2 and on when it repeats", "`cells/results/<batch>/readings.json`", []tableColumn{
			{"id", tString, "`<step>/<name>`, #2 on a repeat"},
			{"batch", tString, "the batch's directory name"},
			{"n", tNumber, "its place in readings.json, from 1"},
			{"boot", tRecorded, "the reading's"},
			{"step", tRecorded, "the reading's"},
			{"name", tRecorded, "the reading's"},
			{"unit", tRecorded, "the reading's"},
			{"how", tRecorded, "the reading's"},
			{"at", tRecorded, "the reading's, cut to its date"},
			{"error", tRecorded, "the reading's"},
			{"kind", tString, "the value's JSON type: number, string, boolean, null, object or array"},
			{"whole", tBool, "value is as recorded: a scalar, or an object or array src/readings.txt names"},
			{"value", tRecorded, "as recorded when whole, else bytes, leaves and sha256 of its jq -c text"},
		}},
	{"records", "a record laug wrote", "its path: a directory ends in /, a load's files are `load/results/<id>.*`",
		"every committed file under gate/results, faults/results, load/results and cells/results, but each " +
			"directory's README.md", []tableColumn{
			{"id", tString, "the record's path"},
			{"kind", tString, "gate, raun, candidate, load, transcript, run directory or readings"},
			{"files", tNumber, "its files"},
			{"bytes", tNumber, "their bytes"},
			{"sha256", tString, "sha256 of the lines sha256sum prints for its files, sorted by path in bytes"},
			{"kept", tBool, "src/kept.txt names it: the record is kept whole in this tree"},
		}},
	{"quotes", "a line of a record not kept whole that a page cites by line", "`<path>:<line>`", "the record's " +
		"file; the cites of every page, figure spec, gate set and hand TSV", []tableColumn{
		{"id", tString, "`<path>:<line>`"},
		{"text", tString, "the line as recorded"},
	}},
	{"fig-adopted", "a value of a record of boot 4's workflow gates G2, G4 and G5 that adopted.svg reads",
		"`<gate>/<file>/<line, four digits>` for a line, `<gate>/<file>/<place, from 001>` for an attempt, " +
			"`<gate>/gate.json/<member>`",
		"each gate's gate.json (driver, workflow_id), timeline.jsonl and poll.jsonl lines under gate/results; for G4 and " +
			"G5, `cells/results/2026-10-24-boot4/steps/G<n>-worker.txt`'s replay and Started Worker lines and " +
			"`extra/g<n>-attempts.json`'s entries; -tables reads each gate whole as the figure does first",
		figValueColumns("the value as recorded: a line's text, a JSON line's value, an entry or a member")},
	{"fig-bucket-edges", "an le string of server 1.31.0's latency histograms, as the lab host's Prometheus listed " +
		"them on the arm pilot's first gate; bucket-edges.svg draws them", "its place, from 00; +Inf is the last",
		"cells/results/2026-10-17-arm-pilot.txt:530, the act list; -tables checks the line's five lists are the same",
		[]tableColumn{
			{"id", tString, "its place in the list, two digits, from 00"},
			{"le", tString, "the le string as listed"},
			{"from", tString, "the transcript and line it was cut from"},
		}},
	{"fig-cancel-path", "a value of boot 4's two cancels, F5 and F7, that cancel-path.svg's panel reads",
		"`<file>/<line, four digits>` for a line, `<file>/<place, from 001>` for an event", "the headings of the " +
			"steps in cells/results/2026-10-24-boot4.txt; `extra/f<n>-events.json`'s events and " +
			"`extra/f<n>-<gate>-timeline.jsonl`'s lines; -tables reads each cancel whole as the panel does first",
		figValueColumns("the value as recorded: a line's text, an event, a JSON line's value")},
	{"fig-clock-step-timers", "a line of batch E that clock-step-timers.svg reads", "`<file>/<line, four digits>`",
		"cells/results/2026-10-24-batchE/steps/T*.txt: each timer's JSON line and exit, each step's and set's date " +
			"command and exit; the transcript's line of the step's size; -tables reads the files as the figure reads " +
			"the lines first", figValueColumns("the line as recorded")},
	{"fig-kill-history", "a second in which a kill-history raun of boot 6 had failed client starts; kill-history.svg " +
		"draws them", "`<run_id>/<its place, from 000>`", "`cells/results/2026-10-27-boot6/extra/a4-kill-history-" +
		"<n>-bins.json`; -tables checks each against its raun JSON: its run, remove_at, every failure in the log " +
		"placed, and from the restart on the drain and after windows' count", []tableColumn{
		{"id", tString, "`<run_id>/<pos>`, pos three digits"},
		{"run", tString, "the raun's run_id, its rauns row's key"},
		{"pos", tNumber, "its place in the raun's seconds, from 0"},
		{"s", tRecorded, "the second from remove_at, floored"},
		{"n", tRecorded, "the failed starts in it"},
		{"from", tString, "the bins file it was cut from"},
	}},
	{"fig-restore-timing", "a line of boot 9's step files that restore-timing.svg reads", "`<step>.txt/<line, four " +
		"digits>`", "cells/results/2026-10-29-boot9/steps/K1 to K3, R1, D1-1 to D2-3: the lines snapshot.sh, " +
		"restore.sh and the copies printed that the figure parses; -tables checks the files against readings.json " +
		"as the figure checks the lines against the readings table", figValueColumns("the line as recorded")},
	{"fig-staleness", "a line make obs-test's fake cell tests logged in boot 4 that staleness.svg reads",
		"`F12.txt/<line, four digits>`", "cells/results/2026-10-24-boot4/steps/F12.txt; -tables reads the file as " +
			"the figure reads the lines first", figValueColumns("the line as recorded, each time of day, " +
			"`at <hh:mm:ss.s> UTC`, as `at +<s> s`, its seconds after the first the lines give")},
	{"fig-stuck-shard", "a value of boot 2's stuck-shard rauns, besides their rauns rows, that stuck-shard.svg reads",
		"`R-<label>.txt/<line, four digits>` for a line, `<run_id>/self_test/passed`", "each raun's step file " +
			"under cells/results/2026-10-23-boot2/steps, its command and fault line; the self-test's passed in its " +
			"raun JSON; -tables reads each raun whole first", figValueColumns("the value as recorded: a line's text, " +
			"or passed")},
	{"fig-throttle-landing", "a line of boot 2's throttle-frontend rauns' step files that throttle-landing.svg reads",
		"`R-<label>.txt/<line, four digits>`", "each raun's step file under cells/results/2026-10-23-boot2/steps, " +
			"its command and fault line; -tables reads each raun whole first", figValueColumns("the line as recorded")},
	{"fig-toxic-fuse", "a value of boot 2's persistence-errors rauns, besides their rauns rows, that toxic-fuse.svg's " +
		"panel reads",
		"`R-<label>.txt/<line>`, `batch-log.txt/<line>` or `pe-<label>.jsonl/<line>`, each line four digits",
		"each raun's step file's command and fault line; cells/results/2026-10-23-boot2/batch-log.txt's lines of the " +
			"catalog copies; the sampler's reads in each raun's hold, `extra/pe-<label>.jsonl`; -tables reads " +
			"each raun whole first", figValueColumns("the value as recorded: a line's text, or a sampler read, " +
			"its at as at_s, its seconds after the raun's inject_at")},
}

// figValueColumns are the columns of a figure table cut from lines and values (valueRow): its key, value, as value
// says, and from.
func figValueColumns(value string) []tableColumn {
	return []tableColumn{
		{"id", tString, "the key"},
		{"value", tRecorded, value},
		{"from", tString, "the record it was cut from, with its line for a line"},
	}
}

// schemaOf is the schema of table name: readings-<batch>'s for each batch's.
func schemaOf(name string) (tableSchema, bool) {
	if strings.HasPrefix(name, readingsPrefix) {
		name = readingsSchema
	}
	for _, s := range tableSchemas {
		if s.name == name {
			return s, true
		}
	}
	return tableSchema{}, false
}

// TablesReadmeData is docs/tables/README.md as report -tables and -docs write it: the format's rules, then each
// table's row, key, source and columns.
func TablesReadmeData() []byte {
	var b strings.Builder
	b.WriteString(`# The summary tables

Generated by the renderer under _report, ` + "`go -C _report run ./cmd/report -tables`" + `, from the committed ` +
		`records; do not edit. ` + "`go -C _report run ./cmd/report -tables -check`" + ` names what differs from a ` +
		`build and writes nothing. ` + "`make docs`" + ` runs ` + "`go -C _report run ./cmd/report -docs`" + `, which ` +
		`adds the rows a build gives that the tables lack, keeps a row the records no longer give, and refuses to ` +
		`change one.

- JSON Lines: one row a line, each an object whose first member, ` + "`id`" + `, is its key. No header.
- Rows sorted by key in bytes, keys unique, LF, a final newline, written as ` + "`jq -c`" + ` writes them.
- A table past 1 MiB goes on in ` + "`<name>-2.jsonl`" + `, then ` + "`-3`" + `; read them in that order as one table.
- Numbers keep the text the record holds. A field the record does not hold, and a text column with nothing in it,
  are null: absent and null are not told apart.
- No stamp column holds a time of day: a start, an end or an at holds its date, or its seconds after another stamp
  of its record (a raun's after its inject), and a time in a result's text is cut to its date. Durations stay as
  recorded. The record keeps the times. A value kept as recorded still holds what the record wrote, times of day
  included: a quotes row's text, a fig table's line or value (fig-staleness's excepted), a reading kept whole, an
  id.
- The record-level rows name their record and its sha256: gates in record, verdict_sha256 and gate_sha256;
  gate-items and rauns in record and sha256; loads in record, run_sha256, verdict_sha256 and baseline_sha256; a
  records row is keyed by its record's path and holds the sha256 of its files. The others name their parent row by
  its key: gate, item, load or batch.
- A row is cited by its key, ` + "`docs/tables/<table>.jsonl#<key>`" + `, never by its line; a run of quotes rows
  by ` + "`<path>:<from>-<to>`" + `, the row of each line.
- What a reader applies is never stored: a judge's version name, a host phrase, a filter of the deciding readings.
- Hand inputs, under src/: ` + "`readings.txt`" + ` (the readings kept whole), ` + "`kept.txt`" + ` (the records kept
  whole, each with what it shows), ` + "`hosts.tsv`" + ` (the host each family of rauns and loads ran on, with the
  transcript line that says it). -tables checks each line resolves.
- ` + "`laug_commit`" + ` in gates names a commit of laug's working history, which is not public.
- A ` + "`fig-<figure>`" + ` table holds the points a figure of docs/img draws, or the lines and values of the
  records it reads, that no other table's row holds, each row with ` + "`from`" + `, the record it was cut from.
`)
	for _, s := range tableSchemas {
		heading := s.name
		if strings.Contains(heading, "<") {
			heading = "`" + heading + "`" // readings-<batch>: a bare <batch> reads as an HTML tag
		}
		fmt.Fprintf(&b, "\n## %s\n\nA row: %s. Key: %s. From: %s.\n\n| Column | Type | Source |\n| --- | --- | --- |\n",
			heading, s.row, s.key, s.source)
		for _, c := range s.columns {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", c.name, c.typ, strings.ReplaceAll(c.source, "|", "\\|"))
		}
	}
	return []byte(b.String())
}
