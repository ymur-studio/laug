#!/usr/bin/env bash
# A stand-in for gate/step.sh in steps_test.go: gate/step.sh check, or gate/step.sh <verb> <dir> [key], with step.sh's
# files and answer channel and none of its work. check makes $GATE_RESULTS/g20260101-000001 with .steps/ and .state.json
# (the workflow's ids) and answers {"result":"next","gate_id":...,"dir":...}, or fails with exit FAKE_GATE_CHECK_EXIT.
# Any other verb waits FAKE_GATE_STEP_PRE_S (0) s, as step.sh's gate_env runs before its .pid, writes .steps/<name>.pid
# under noclobber (exit 75 when it is there), records its argv, environment, pid, pgid and sid in .steps/<name>.env,
# waits FAKE_GATE_STEP_S (0) s in a sleep of its own process group whose pid goes to .steps/<name>.sleep, and answers
# FAKE_GATE_STEP_ANSWER ({"result":"next"}) with exit FAKE_GATE_STEP_EXIT (0), in .steps/<name>.done and on fd 3, as
# step.sh does. Any exit but 0 answers null, with a why that names the directory as given, a path from /. On TERM it
# appends TERM to .steps/<name>.signals and exits 143 with a .done that answers stopped, unless FAKE_GATE_STEP_STUCK is
# set, when it ignores TERM. FAKE_GATE_STEP_NODONE set writes no .done at all. Every start but check's appends its pid
# to .steps/<name>.started first, one line per start, a copy that exits 75 too, so a test counts the starts.
set -u
exec 3>&1 1>&2
verb="${1:-}"
if [[ "$verb" == check ]]; then
  gid=g20260101-000001; d="$GATE_RESULTS/$gid"
  mkdir -p "$d/.steps" || exit 1
  printf '{"workflow_id":"%s","workflow_run_id":"%s","step":null}\n' "${GATE_WORKFLOW_ID:-}" "${GATE_WORKFLOW_RUN_ID:-}" > "$d/.state.json"
  env | sort > "$d/.steps/check.env"
  rc="${FAKE_GATE_CHECK_EXIT:-0}"
  if [[ "$rc" != 0 ]]; then echo "laug · gate/step.sh: fake check failed in $(cd "$d" && pwd -P)" >&2; exit "$rc"; fi
  printf '{"result":"next","gate_id":"%s","dir":"%s"}\n' "$gid" "$d" >&3
  exit 0
fi
dir="${2:?dir}"; key="${3:-}"
case "$verb" in lease|quiet) name="$verb-$key" ;; combine) name=judge ;; *) name="$verb" ;; esac
s="$dir/.steps"; mkdir -p "$s"
echo "$$" >> "$s/$name.started"
[[ -z "${FAKE_GATE_STEP_PRE_S:-}" ]] || sleep "$FAKE_GATE_STEP_PRE_S"
read -r st < "/proc/$$/stat"; rest="${st##*) }"; read -r -a f <<<"$rest"; start="${f[19]}"
set -C
{ printf '{"pid":%d,"start":%s,"boot_id":"%s"}\n' $$ "$start" "$(cat /proc/sys/kernel/random/boot_id)" > "$s/$name.pid"; } 2>/dev/null || exit 75
set +C
{ echo "argv: $*"; echo "ids: $$ $(ps -o pgid= -p $$ | tr -d ' ') $(ps -o sid= -p $$ | tr -d ' ')"; env | sort; } > "$s/$name.env"
write_done() { # write_done <exit> <answer JSON> <why>
  [[ -z "${FAKE_GATE_STEP_NODONE:-}" ]] || return 0
  local t; t="$(mktemp "$s/done.XXXXXX")"
  jq -cn --argjson e "$1" --argjson a "$2" --arg w "$3" '{exit: $e, answer: $a, why: (if $w == "" then null else $w end), at: null}' > "$t" \
    && mv "$t" "$s/$name.done"
}
on_term() {
  echo TERM >> "$s/$name.signals"
  [[ -z "${FAKE_GATE_STEP_STUCK:-}" ]] || return 0
  write_done 143 '{"result":"stopped"}' "signal TERM"; echo '{"result":"stopped"}' >&3; exit 143
}
trap on_term TERM
sleep "${FAKE_GATE_STEP_S:-0}" & sp=$!
echo "$sp" > "$s/$name.sleep"
while kill -0 "$sp" 2>/dev/null; do wait "$sp"; done
rc="${FAKE_GATE_STEP_EXIT:-0}"
if [[ "$rc" == 0 ]]; then
  a='{"result":"next"}'; [[ -z "${FAKE_GATE_STEP_ANSWER:-}" ]] || a="$FAKE_GATE_STEP_ANSWER"
  write_done 0 "$a" ""; echo "$a" >&3
else
  echo "laug · gate/step.sh: fake failed in $dir" >&2
  write_done "$rc" null "gate/step.sh: fake failed in $dir"; echo null >&3
fi
exit "$rc"
