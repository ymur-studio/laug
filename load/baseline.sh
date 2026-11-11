#!/usr/bin/env bash
# Baseline reading for one recorded run: every threshold over the run's whole window (read-thresholds.sh), plus CPU
# and memory per service at the end of the run. Writes <results>/<run>.baseline.json and prints a table.
set -euo pipefail

RUN_ID="${1:?run id}"
RESULTS_DIR="${RESULTS_DIR:-load/results}"
here="$(cd "$(dirname "$0")" && pwd)"
meta="$RESULTS_DIR/$RUN_ID.json"
[[ -f "$meta" ]] || { echo "no run metadata at $meta" >&2; exit 2; }
# The run's own Prometheus, recorded by run-omes.sh, so this reads the cell the load hit and not whatever
# .last-lease.json names now. PROM_URL overrides; runs recorded before 2026-10-13 have no field.
PROM_URL="${PROM_URL:-$(jq -r '.prometheus // empty' "$meta")}"
PROM_URL="${PROM_URL:-${FALLBACK_PROM_URL:-http://127.0.0.1:9090}}"
echo "reading Prometheus at $PROM_URL" >&2
# A microVM cell's containers run in its guest's Docker as project laug, and the host has no laug-<id>: when the last
# lease's cell is the run's (laug-<id>, the project the run recorded) and names a guest, docker runs there over the
# lease's ssh. Not by address: a block's address passes to the next cell on it. A PROJECT given still wins.
LEASE_FILE="${LEASE_FILE:-.last-lease.json}"; dk() { docker "$@"; }
if [[ -z "${PROJECT+set}" && -f "$LEASE_FILE" ]] && [[ -n "$(jq -r '.ssh // ""' "$LEASE_FILE")" ]] \
    && [[ "laug-$(jq -r '.id // ""' "$LEASE_FILE")" == "$(jq -r '.project // ""' "$meta")" ]]; then
  root="$(cd "$here/.." && pwd)"; prom="$PROM_URL"
  # shellcheck source=faults/lib.sh
  . "$root/faults/lib.sh"
  ( lease_read "$LEASE_FILE" ) || { echo "the run's cell is the last lease's microVM cell, whose CPU and memory are read in its guest over ssh; ${LAUG:-bin/laug} baseline -project '' skips those rows" >&2; exit 2; }
  lease_read "$LEASE_FILE"; PROM_URL="$prom"   # lease_read sets PROJECT=laug, and PROM_URL
  echo "reading CPU and memory in cell $CELL's guest, $GUEST_SSH, over the lease's ssh" >&2
fi
PROJECT="${PROJECT-$(jq -r '.project // empty' "$meta")}"   # compose project of the cell, for docker stats; empty skips the per-service rows

start_epoch="$(date -u -d "$(jq -r .start "$meta")" +%s)"
end_epoch="$(date -u -d "$(jq -r .end "$meta")" +%s)"
# The compose project label, not a name prefix: name=laug- would also match every pool cell's laug-<id>-*. With
# no ids, docker stats would report every running container. Checked before anything is written.
ids=()
if [[ -n "$PROJECT" ]]; then
  mapfile -t ids < <(dk ps -q --filter "label=com.docker.compose.project=$PROJECT")
  (( ${#ids[@]} )) || { echo "no running containers in compose project $PROJECT${GUEST_SSH:+, in the guest of cell $CELL at $GUEST_SSH}; PROJECT= skips the per-service rows" >&2; exit 2; }
fi
out="$RESULTS_DIR/$RUN_ID.baseline.json"
# Committed baselines back load/baseline.md; reading them again against another Prometheus would overwrite them.
[[ -e "$out" && "${FORCE:-0}" != 1 ]] && { echo "$out exists and may be committed evidence; ${LAUG:-bin/laug} baseline -overwrite replaces it" >&2; exit 2; }
PROM_URL="$PROM_URL" "$here/read-thresholds.sh" "$start_epoch" "$end_epoch" > "$out"

printf "laug · baseline reading for run %s (%d s window)\n\n" "$RUN_ID" "$(( end_epoch - start_epoch ))"
printf "%-41s %12s\n" metric value
jq -r 'to_entries[] | "\(.key)\t\(.value)"' "$out" | while IFS=$'\t' read -r k v; do printf "%-41s %12s\n" "$k" "$v"; done
awk -v n="$(jq -r .workflows_completed "$out")" -v w="$(jq -r .window_seconds "$out")" 'BEGIN { printf "%-41s %12.1f\n", "workflows_per_second", n/w }'

if [[ -n "$PROJECT" ]]; then
  echo
  printf "%-40s %8s %10s\n" service cpu mem_mib
  dk stats --no-stream --format '{{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}' "${ids[@]}" \
    | sort | while IFS=$'\t' read -r name cpu mem; do
        svc="${name#"${PROJECT}"-}"; svc="${svc%-1}"
        mib="$(awk -v m="${mem%% *}" 'BEGIN { if (m ~ /GiB/) { sub(/GiB/,"",m); m*=1024 } else sub(/MiB/,"",m); printf "%.0f", m }')"
        printf "%-40s %8s %10s\n" "$svc" "$cpu" "$mib"
        jq --arg s "$svc" --arg c "${cpu%\%}" --argjson m "$mib" '.services[$s]={cpu_percent:($c|tonumber), mem_mib:$m}' "$out" > "$out.tmp" && mv "$out.tmp" "$out"
      done
fi
echo
echo "written to $out"
