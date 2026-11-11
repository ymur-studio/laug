#!/usr/bin/env bash
# softasserts.sh: the server's soft assertions, counted per role, as one JSON object:
#   {"temporal-frontend":0,"temporal-frontend2":0,"temporal-history":0,"temporal-matching":1,"temporal-worker":0,
#    "first":["failed assertion: loadedTasks went negative"]}
#
# Usage:
#   cells/softasserts.sh [--since T] [--until T] <compose-project>    each role's container on this host's Docker
#   cells/softasserts.sh --file <role>=<path> [--file <role>=<path>]  saved logs, one role each; path - reads stdin
#
# A soft assertion is the server checking one of its own invariants and finding it false. The server does not stop:
# it logs one error line and goes on, so nothing else in laug sees it. Server 1.31.0 logs it as
# "failed assertion: <static message>" with the tag "failed-assertion":true (common/softassert/softassert.go:28-45,
# common/log/tag/tags.go:550). It has 140 call sites outside that package: 62 call That or Fail, and 78 call
# UnexpectedDataLoss (21) or UnexpectedInternalErr (57), which log through Fail with the same tag
# (common/softassert/serviceerror.go). By area: chasm 44, matching 42, history 34, common/persistence 19 (14 of them
# data loss, in reading history branches), dynamic config 1. The tag decides here, not the message. Read in source,
# and the line printed by the server's own logger and softassert package in a scratch program; no soft
# assertion has been seen on a cell yet.
#
# A role is null when it was not read in full: no container for it in the project, its logs failed, its lines from T
# on were partly rotated away (below), or no --file named it.
# first lists the distinct messages in the order each first appeared (by the line's ts), at most 10.
# T is anything docker logs takes: RFC 3339, a Unix time with up to 9 decimals, or a relative 10m. Both ends are
# inclusive (seen on Docker 29.5.3). --since defaults to each container's StartedAt, as
# cells/shards-ready.sh does, so a role that restarted counts from its restart; --until defaults to now. A microVM
# cell runs its containers in the guest; faults/run.sh fetches their logs over ssh and counts them with --file, so
# the guest needs no copy of this script.
#
# The json-file driver keeps 10 MB x 3 per container (cells/compose/cell.yml) and drops the oldest file when the
# newest fills, so a count since a role's start can miss lines: history logged 8.3 MB in 31 s of persistence-latency
# on the dev host, so 30 MB holds about 110 s of that. A role is null, and the exit 1, when its oldest
# kept line is at or after T and more than ROTATE_SLACK_S (20) s after its container's StartedAt. The 1.31.0 image
# printed its first line 0.19 to 0.26 s after StartedAt in 3 starts on a bridge network, and 10.2 s after it with
# no network, where the entrypoint's getent hosts waited (dev host). Dropping a file takes 10 MB of
# logs, about 37 s at the rate above, so an oldest line later than 20 s means lines were rotated away. Not seen: a
# first 10 MB logged within 20 s of the start, and a rotation before a restart that came after T, since the oldest
# kept line then predates the last StartedAt. Count a window soon after it closes.
#
# Exit 0; 1 when a role's logs could not be read in full or its container is missing (the JSON still prints); 2 on bad usage
# or a project with no server role containers, or more than one for a role.
set -euo pipefail

ROLES=(temporal-frontend temporal-frontend2 temporal-history temporal-matching temporal-worker)
FIRST_MAX=10
ROTATE_SLACK_S="${ROTATE_SLACK_S:-20}"
usage() {
  printf '%s\n' 'usage: cells/softasserts.sh [--since T] [--until T] <compose-project>' \
    '       cells/softasserts.sh --file <role>=<path> [--file <role>=<path>...]   (path - reads stdin)' >&2
  exit 2
}

since="" until="" project="" files=()
while (( $# )); do
  case "$1" in
    --since) [[ -n "${2:-}" ]] || usage; since="$2"; shift 2 ;;
    --until) [[ -n "${2:-}" ]] || usage; until="$2"; shift 2 ;;
    --file) [[ -n "${2:-}" ]] || usage; files+=("$2"); shift 2 ;;
    -*) echo "softasserts.sh: unknown flag $1" >&2; usage ;;
    *) [[ -z "$project" ]] || usage; project="$1"; shift ;;
  esac
done
if (( ${#files[@]} )); then
  [[ -z "$project$since$until" ]] || { echo "softasserts.sh: --file takes no project, --since or --until; the file is counted whole" >&2; exit 2; }
else
  [[ -n "$project" ]] || usage
fi

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
: > "$tmp/records"
read_roles=() rc=0

# One record per soft assertion, {"role","ts","msg"}. grep keeps a 30 MB log cheap; jq decides, so a line that only
# quotes the tag in its text, a "failed-assertion":false, or a line that is not JSON (the image prints its config
# and Fx lines that way) does not count.
records() { # records <role> < log
  { grep -F 'failed-assertion' || [[ $? == 1 ]]; } \
    | jq -cR --arg role "$1" 'fromjson? | select(type == "object" and .["failed-assertion"] == true)
        | {role: $role, ts: (.ts // "" | tostring), msg: (.msg // "" | tostring)}'
}
is_role() { local r; for r in "${ROLES[@]}"; do [[ "$r" == "$1" ]] && return 0; done; return 1; }
# rotated <container> <since> <started-at>: prints the oldest kept line's time and returns 0 when lines from since on
# may have been rotated away. docker logs -t prefixes each line with its time; the first line of all the logs is the
# oldest kept, and it is also the first from since on exactly when since is at or before it. head closes the pipe
# early, so docker's own status is not read here.
rotated() {
  local oldest at_since o s
  oldest="$(docker logs -t "$1" 2>&1 | head -1)" || true
  [[ -n "$oldest" ]] || return 1
  at_since="$(docker logs -t --since "$2" "$1" 2>&1 | head -1)" || true
  [[ "$at_since" == "$oldest" ]] || return 1
  o="$(date -u -d "${oldest%% *}" +%s.%N 2>/dev/null)" && s="$(date -u -d "$3" +%s.%N 2>/dev/null)" || return 1
  awk -v o="$o" -v s="$s" -v k="$ROTATE_SLACK_S" 'BEGIN { exit !(o > s + k) }' || return 1
  echo "${oldest%% *}"
}

if (( ${#files[@]} )); then
  stdin_used=0 seen=" "
  for spec in "${files[@]}"; do
    role="${spec%%=*}"; path="${spec#*=}"
    [[ "$spec" == *=?* ]] && is_role "$role" || { echo "softasserts.sh: --file takes <role>=<path>, role one of ${ROLES[*]}" >&2; exit 2; }
    [[ "$seen" != *" $role "* ]] || { echo "softasserts.sh: $role named twice" >&2; exit 2; }
    seen+="$role "
    if [[ "$path" == - ]]; then
      (( ! stdin_used )) || { echo "softasserts.sh: only one --file reads stdin" >&2; exit 2; }
      stdin_used=1; path=/dev/stdin
    elif [[ ! -r "$path" ]]; then
      echo "softasserts.sh: cannot read $path for $role" >&2; rc=1; continue
    fi
    if records "$role" < "$path" > "$tmp/one"; then cat "$tmp/one" >> "$tmp/records"; read_roles+=("$role")
    else echo "softasserts.sh: could not count $path for $role" >&2; rc=1; fi
  done
else
  found=0 missing=()
  for role in "${ROLES[@]}"; do
    cid="$(docker ps -aq --filter "label=com.docker.compose.project=$project" --filter "label=com.docker.compose.service=$role")" \
      || { echo "softasserts.sh: docker ps failed" >&2; exit 2; }
    if [[ -z "$cid" ]]; then missing+=("$role"); continue; fi
    [[ "$(wc -l <<<"$cid")" -eq 1 ]] || { echo "softasserts.sh: more than one $role container in $project" >&2; exit 2; }
    found=$(( found + 1 ))
    if ! started="$(docker inspect -f '{{.State.StartedAt}}' "$cid")"; then
      echo "softasserts.sh: docker inspect failed for $role in $project" >&2; rc=1; continue
    fi
    s="${since:-$started}"
    if oldest="$(rotated "$cid" "$s" "$started")"; then
      echo "softasserts.sh: $role in $project: its oldest kept line is from $oldest, after $s, so older lines were rotated away (json-file keeps 10 MB x 3)" >&2
      rc=1; continue
    fi
    args=(--since "$s"); [[ -z "$until" ]] || args+=(--until "$until")
    # Both streams, merged by docker itself so lines stay whole: the server logs JSON on stdout, the image's
    # entrypoint and Fx print plain lines on stderr (seen on the server 1.31.0 image).
    if docker logs "${args[@]}" "$cid" 2>&1 | records "$role" > "$tmp/one"; then cat "$tmp/one" >> "$tmp/records"; read_roles+=("$role")
    else echo "softasserts.sh: docker logs failed for $role in $project" >&2; rc=1; fi
  done
  (( found )) || { echo "softasserts.sh: no server role containers in project $project (a microVM cell's are in its guest; ${LAUG:-bin/laug} fault counts them)" >&2; exit 2; }
  if (( ${#missing[@]} )); then echo "softasserts.sh: no container in $project for ${missing[*]}" >&2; rc=1; fi
fi

jq -sc --argjson roles "$(printf '%s\n' "${ROLES[@]}" | jq -Rsc 'split("\n")[:-1]')" \
  --argjson read "$(printf '%s\n' "${read_roles[@]+"${read_roles[@]}"}" | jq -Rsc 'split("\n") | map(select(. != ""))')" \
  --argjson max "$FIRST_MAX" '
  . as $r
  | reduce $roles[] as $role ({}; .[$role] = (if any($read[]; . == $role) then [$r[] | select(.role == $role)] | length else null end))
  + {first: ($r | sort_by(.ts) | reduce .[].msg as $m ([]; if any(.[]; . == $m) then . else . + [$m] end) | .[:$max])}' "$tmp/records"
exit "$rc"
