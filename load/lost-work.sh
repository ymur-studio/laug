# shellcheck shell=bash
# Lost work: what a verdict counts besides the thresholds. A threshold reads latency and errors from the server's
# own metrics; it cannot see an Omes iteration that failed, or a workflow still Running at the end of a run.
# The lab host's persistence-latency rauns failed 540, 538 and 537 iterations during the fault and 0 in the after
# window, by lw_buckets below (iterations_failed in faults/results/raun-lab2-persistence-latency-{1,2,3}.json).
#
# Sourced by load/run-omes.sh, load/verdict.sh, faults/run.sh and load/testdata/harness-test.sh. It defines
# functions only. Needs bash, awk, bc and jq.

# Seconds in a Go duration of the forms DURATION takes here: 5m, 390s, 1h, 1m30s. Prints nothing for an empty one.
lw_dur_s() {
  [[ -n "${1:-}" ]] || return 0
  echo "$1" | sed -E 's/([0-9]+)h/\1*3600+/; s/([0-9]+)m/\1*60+/; s/([0-9]+)s/\1+/; s/\+$//' | bc
}

# Omes logs through zap's console encoder: tab-separated time, level, caller, message, with the time in ISO 8601 and
# the host's UTC offset (2026-10-13T19:30:30.000+0530, or Z on a UTC host). A failed iteration is one ERROR
# line, "iteration N failed: ..." (omes f0de206, loadgen/generic_executor.go:195-196; seen on the dev host,
# load/testdata/omes-fail.txt holds the shape). The FATAL line Omes exits with can quote the same words further
# into its message, so the message must start with them. ts() turns the time into epoch seconds, offset applied,
# without date(1) per line; "" when the time does not parse.
LW_AWK_TS='
function civil_days(y, m, d,   era, yoe, doy) {
  y -= (m <= 2); era = int((y >= 0 ? y : y - 399) / 400); yoe = y - era * 400
  doy = int((153 * (m + (m > 2 ? -3 : 9)) + 2) / 5) + d - 1
  return era * 146097 + yoe * 365 + int(yoe / 4) - int(yoe / 100) + doy - 719468
}
function ts(s,   rest, frac, off, sign) {
  if (s !~ /^[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9]/) return ""
  rest = substr(s, 20); frac = 0; off = 0
  if (match(rest, /^\.[0-9]+/)) { frac = ("0" substr(rest, 1, RLENGTH)) + 0; rest = substr(rest, RLENGTH + 1) }
  if (rest ~ /^[+-][0-9][0-9]:?[0-9][0-9]$/) {
    sign = (substr(rest, 1, 1) == "-") ? -1 : 1; gsub(/:/, "", rest)
    off = sign * (substr(rest, 2, 2) * 3600 + substr(rest, 4, 2) * 60)
  } else if (rest != "Z") return ""
  return civil_days(substr(s, 1, 4) + 0, substr(s, 6, 2) + 0, substr(s, 9, 2) + 0) * 86400 \
    + substr(s, 12, 2) * 3600 + substr(s, 15, 2) * 60 + substr(s, 18, 2) + frac - off
}
function failed_iteration() { return toupper($2) == "ERROR" && $4 ~ /^iteration [0-9]+ failed/ }
function not_found() { return tolower($4) ~ /workflow execution not found/ }
'

# Failed iterations in an Omes log; null when the log is missing.
lw_count_failed() {
  [[ -f "$1" ]] || { echo null; return 0; }
  awk -F '\t' "$LW_AWK_TS"' failed_iteration() { n++ } END { print n + 0 }' "$1"
}

# Omes's own tally, as {"succeeded":N,"failed":M}, or null when it printed none. At --log-level warn (run-omes.sh)
# Omes prints it only when iterations failed under --continue-on-iteration-failure, inside the error it exits with:
# "run completed with N of M iterations failed" (seen on the dev host, from an orphaned load generator; the shape is
# load/testdata/omes-fail.txt:5). A clean run prints no counts at any level. At info Omes also logs "Run completed in D:
# N iterations succeeded, M failed" (omes f0de206, loadgen/generic_executor.go:226-235; read in source only).
lw_tally() {
  [[ -f "$1" ]] || { echo null; return 0; }
  awk '
    match($0, /run completed with [0-9]+ of [0-9]+ iterations failed/) {
      split(substr($0, RSTART, RLENGTH), w, " "); out = sprintf("{\"succeeded\":%d,\"failed\":%d}", w[6] - w[4], w[4])
    }
    match($0, /Run completed in [^:]*: [0-9]+ iterations succeeded, [0-9]+ failed/) {
      s = substr($0, RSTART, RLENGTH); sub(/^[^:]*: /, "", s); split(s, w, " ")
      out = sprintf("{\"succeeded\":%d,\"failed\":%d}", w[1], w[4])
    }
    END { print (out == "" ? "null" : out) }' "$1"
}

# lw_buckets <omes-log> <inject_at> <remove_at> <after_start> [after_end]: failed iterations per window of a raun,
# by the time Omes logged them. Windows are [start, inject), [inject, remove), [remove, after_start) and
# [after_start, after_end); a window that never opened is null. late counts lines from after_end on, unplaced the
# lines whose time did not parse, total every failed iteration. not_found counts the failed iterations, late ones
# aside, whose error says "workflow execution not found": the server answered for a workflow it no longer holds.
# The server says it in two forms, "workflow execution not found" (service/history/consts/const.go:55) and
# "workflow execution not found for workflow ID .. and run ID .." (common/persistence/execution_manager.go:438),
# server 1.31.0, read in source. Two dev-host runs' logs held the second form 3 times, 118 to 209 s after recovery,
# past each after window, from an Omes that outlived its cell (the logs are kept outside the tree): late, and
# not_found read 0 for both runs. Prints null when the log is missing.
lw_buckets() {
  [[ -f "$1" ]] || { echo null; return 0; }
  awk -F '\t' -v inj="${2:-}" -v rem="${3:-}" -v aft="${4:-}" -v end="${5:-}" "$LW_AWK_TS"'
    failed_iteration() {
      total++; t = ts($1); b = ""
      if (t == "") b = "unplaced"
      else if (inj == "" || t < inj + 0) b = "before"
      else if (rem == "" || t < rem + 0) b = "during"
      else if (aft == "" || t < aft + 0) b = "drain"
      else if (end == "" || t < end + 0) b = "after"
      else b = "late"
      n[b]++
      if (b != "late" && not_found()) nf++
    }
    function opened(v, k) { return v == "" ? "null" : n[k] + 0 }
    END {
      printf "{\"before\":%d,\"during\":%s,\"drain\":%s,\"after\":%s,\"late\":%d,\"unplaced\":%d,\"not_found\":%d,\"total\":%d}\n",
        n["before"], opened(inj, "during"), opened(rem, "drain"), opened(aft, "after"), n["late"], n["unplaced"], nf, total
    }' "$1"
}

# lw_unexpected <sweep-json>: the statuses a sweep found outside SWEEP_EXPECTED (comma-separated, default Completed),
# as "Running=3 Failed=1"; empty when there are none. A scenario that continues as new sets
# SWEEP_EXPECTED=Completed,ContinuedAsNew; for workflow_with_single_noop_activity a ContinuedAsNew is unexpected.
lw_unexpected() {
  jq -r --arg ok "${SWEEP_EXPECTED:-Completed}" \
    '($ok | split(",")) as $ok | [to_entries[] | select(.value > 0) | select(.key as $k | any($ok[]; . == $k) | not)
      | "\(.key)=\(.value)"] | join(" ")' <<<"$1"
}

# lw_sweep_total <sweep-json>: workflows the sweep counted, every status.
lw_sweep_total() { jq '[.[]] | add // 0' <<<"$1"; }

# lw_load_floor <rate> <duration-seconds>: the fewest completions a load run may show, 0.95 x RATE x DURATION. It
# uses DURATION, the time Omes starts iterations, not the window run-omes.sh records, which adds the worker's start
# and the cooldown (base1-100551: 322 s of window for a 300 s run). Prints nothing without both.
lw_load_floor() {
  [[ -n "${1:-}" && -n "${2:-}" ]] || return 0
  awk -v r="$1" -v d="$2" 'BEGIN { printf "%d\n", 0.95 * r * d }'
}

# lw_after_floor <rate> <after-seconds>: the fewest completions a raun's after window may show, RATE x (after_s - 10).
# after_s is the catalog's length of the window, which faults/run.sh sleeps through; window_seconds is the same span
# read on the wall clock with date. Completions follow the monotonic clock: Omes paces its starts with time.Tick (omes
# f0de206, loadgen/generic_executor.go:107), and Prometheus scrapes on a ticker (Go's timers and sleep run on the
# monotonic clock: go1.26.0 runtime/time.go, timeSleep, and runtime/sys_linux_amd64.s, nanotime1). The dev host's wall
# clock ran 1.048, then 1.045, times its uptime (cells/results/2026-10-23-boot1.txt:69), so a 90 s window there read
# about 94 s of wall clock over the same 1800 completions, and a floor of RATE x (94 - 5) would sit at 1780. Each edge
# reads the last scrape at or before it, up to 5 s old (scrape_interval in cells/compose/prometheus.yml): 5 s of margin
# for that and 5 s for one late or missed scrape. Committed rauns with settle_s, all on the lab host (ratio 1.0000) at
# 20/s, count 1799 to 1801 in 90 or 91 s of wall clock when clean; raun-kh1-0723, from before settle_s, counted 1741 and
# ADVANCEd.
lw_after_floor() {
  [[ -n "${1:-}" && -n "${2:-}" ]] || return 0
  awk -v r="$1" -v w="$2" 'BEGIN { printf "%d\n", r * (w - 10) }'
}

# lw_raun_judge <json> [rate]: the lost-work rules of a fault raun, on a JSON with after, after_s, iterations_failed,
# sweep, sweep_error and rate (faults/run.sh builds it; a committed raun JSON works too). Prints one reason per line
# and returns 1 when any rule BLOCKs. A field the JSON lacks (rauns before 2026-10-14) is not recorded, and its rule
# passes; without after_s the floor takes the after window's window_seconds. rate falls back to the second argument.
#   - any failed iteration in the after window, or one whose log time did not parse
#   - any failed iteration, late ones aside, whose error says "workflow execution not found": a workflow the server
#     acknowledged and then lost. The sweep cannot see it, since it counts only the records that exist.
#   - a sweep that failed, found no workflows, or found one in a status outside SWEEP_EXPECTED
#   - after-window completions below RATE x (after_s - 10)
lw_raun_judge() {
  local j="$1" reasons=() n rate completed window floor bad
  n="$(jq -r '.iterations_failed.after // empty' <<<"$j")"
  [[ -n "$n" ]] && (( n > 0 )) && reasons+=("$n failed iteration(s) in the after window")
  # A failure whose time did not parse could be in the after window: fail closed.
  n="$(jq -r '.iterations_failed.unplaced // empty' <<<"$j")"
  [[ -n "$n" ]] && (( n > 0 )) && reasons+=("$n failed iteration(s) with a log time that did not parse")
  n="$(jq -r '.iterations_failed.not_found // empty' <<<"$j")"
  [[ -n "$n" ]] && (( n > 0 )) && reasons+=("$n failed iteration(s) before the after window ended: workflow execution not found")
  if [[ -n "$(jq -r '.sweep_error // empty' <<<"$j")" ]]; then
    reasons+=("sweep failed: $(jq -r .sweep_error <<<"$j")")
  elif jq -e '.sweep | type == "object"' >/dev/null 2>&1 <<<"$j"; then
    bad="$(lw_unexpected "$(jq -c .sweep <<<"$j")")"
    if (( $(lw_sweep_total "$(jq -c .sweep <<<"$j")") == 0 )); then
      reasons+=("sweep found no workflows started before $(jq -r '.sweep_cutoff // "the cutoff"' <<<"$j")")
    elif [[ -n "$bad" ]]; then
      reasons+=("sweep: $bad among workflows started before $(jq -r '.sweep_cutoff // "the cutoff"' <<<"$j")")
    fi
  fi
  rate="$(jq -r '.rate // empty' <<<"$j")"; rate="${rate:-${2:-}}"
  completed="$(jq -r '.after.workflows_completed // empty' <<<"$j")"
  window="$(jq -r '.after_s // .after.window_seconds // empty' <<<"$j")"
  if [[ -n "$rate" && -n "$completed" && -n "$window" ]]; then
    floor="$(lw_after_floor "$rate" "$window")"
    (( completed < floor )) && reasons+=("after window completed $completed workflows, floor $floor (${rate}/s x (${window} s - 10))")
  fi
  (( ${#reasons[@]} == 0 )) && return 0
  printf '%s\n' "${reasons[@]}"
  return 1
}
