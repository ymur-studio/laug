#!/usr/bin/env bash
# fi-template-check.sh [tag | temporalio/server:<tag>]: check cells/compose/config/laug-server.yaml against a server
# release. The tag defaults to SERVER_TAG, else cell.yml's default (laug cell fi-template-check passes -server-tag as
# the tag). The first line names the tag checked and the tag the file was vendored from (its laug-server-tag line);
# the last says pass or FAIL on that tag. Needs Docker and the image, Go for the module download, no cell. Six checks,
# each prints one line; the script exits 1 if any fails, a file the server cannot render included.
#   1. the image's binary embeds common/config/config_template_embedded.yaml from go.temporal.io/server at the
#      image's tag byte for byte (the file is found in the binary by its first line, then compared)
#   2. the vendored file, every laug-begin..laug-end span dropped, is that template line for line
#   3. rendered by the image's own binary (temporal-server render-config, no network) with every LAUG_FI_* unset,
#      the vendored file gives the config the embedded template gives, under the cell's server env
#   4. the name lists the vendored block checks against are the server's: the six error names newFault accepts
#      (common/persistence/faultinjection/fault.go) and the DataStoreName constants (common/config/config.go)
#   5. rendered with LAUG_FI_*: one error lands on the default store only, never visibility; the profile default
#      is DefaultFaultInjection's set (common/config/config.go), each method its own seed (LAUG_FI_SEED plus 0 to 5),
#      and all 0 with no seed; a name the server does not know (ShardOwnershipLostError, the example in config.go's
#      comment, which panics the server) fails to render
#   6. every serverfi row in faults/catalog.yaml (CATALOG overrides) names a store and a method that store's fault
#      injection wrapper draws for at this tag: the generate("<Method>") calls in faultinjection/<store>_gen.go. The
#      server takes any method name and draws nothing for one it does not wrap (store_fault_generator.go generate), so
#      a misspelt method would inject nothing and the raun would BLOCK as "did not land"
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/.." && pwd)"
vendored="$here/compose/config/laug-server.yaml"
# cell.yml's image line is temporalio/server:${SERVER_TAG:-<default>}; the default is the part after :-.
default_tag="$(yq -r '."x-server".image' "$here/compose/cell.yml" 2>/dev/null | sed -n 's/^temporalio\/server:\${SERVER_TAG:-\([0-9.]*\)}$/\1/p')"
arg="${1:-${SERVER_TAG:-$default_tag}}"; [[ "$arg" == temporalio/server:* ]] || arg="temporalio/server:$arg"
image="$arg"
[[ "$image" =~ ^temporalio/server:([0-9]+\.[0-9]+\.[0-9]+)$ ]] || { echo "laug · fi-template-check: want a tag x.y.z or temporalio/server:<x.y.z>, got '${1:-${SERVER_TAG:-$default_tag}}'" >&2; exit 2; }
version="${BASH_REMATCH[1]}"
vendored_tag="$(sed -n 's/^# laug-server-tag: \([0-9.]*\)$/\1/p' "$vendored")"
for t in docker go jq yq cmp diff; do command -v "$t" >/dev/null || { echo "laug · fi-template-check needs $t" >&2; exit 2; }; done

mod="$(cd "$root" && go mod download -json "go.temporal.io/server@v$version" 2>/dev/null | jq -r '.Dir // empty')"
[[ -n "$mod" && -d "$mod" ]] || { echo "laug · fi-template-check: go mod download go.temporal.io/server@v$version failed" >&2; exit 2; }
upstream="$mod/common/config/config_template_embedded.yaml"
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
failed=0
ok() { printf '  ok    %s\n' "$1"; }
bad() { printf '  FAIL  %s\n' "$1"; shift; local l; for l in "$@"; do printf '        %s\n' "$l"; done; failed=1; }
echo "laug · fi-template-check: server tag $version. cells/compose/config/laug-server.yaml (vendored from ${vendored_tag:-an unknown tag}) against $image and go.temporal.io/server@v$version"
if [[ "$version" != "$vendored_tag" ]]; then
  echo "  note  the file was vendored from ${vendored_tag:-an unknown tag}, not $version: faults/run.sh serverfi and ${LAUG:-bin/laug} cell up -server-fi refuse a $version cell"
fi

# 1. The binary holds the template as go:embed stored it, a contiguous byte copy. head then tail, not tail then head:
# under pipefail the tail that head cuts off exits 141 and a match reads as a miss.
docker run --rm --network none --entrypoint sh "$image" -c 'cat "$(command -v temporal-server)"' > "$tmp/temporal-server"
size="$(wc -c < "$upstream")"; first="$(head -1 "$upstream")"; at=""
while read -r off; do
  head -c "$(( off + size ))" "$tmp/temporal-server" | tail -c "$size" > "$tmp/at"
  if cmp -s "$tmp/at" "$upstream"; then at="$off"; break; fi
done < <(LC_ALL=C grep -obaF -- "$first" "$tmp/temporal-server" | cut -d: -f1)
rm -f "$tmp/temporal-server"
if [[ -n "$at" ]]; then ok "1. $image embeds the module's template byte for byte ($size bytes at offset $at)"
else bad "1. $image does not embed go.temporal.io/server@v$version's config_template_embedded.yaml ($size bytes)"; fi

# 2. Upstream lines unchanged; everything laug added is fenced.
if d="$(sed '/laug-begin/,/laug-end/d' "$vendored" | diff -u --label "upstream v$version" --label "laug-server.yaml, laug spans dropped" "$upstream" -)"; then
  ok "2. the vendored file is the v$version template plus $(grep -c 'laug-begin' "$vendored") laug span(s), $(( $(wc -l < "$vendored") - $(wc -l < "$upstream") )) lines"
else bad "2. the vendored file differs from the v$version template outside its laug spans" "$(head -80 <<<"$d")"; fi
head -c 1024 "$vendored" | grep -qE '^[[:space:]]*#.*enable-template' && ok "2. a comment in the first 1 KiB says enable-template" \
  || bad "2. no enable-template comment in the first 1 KiB: the server would read the file as plain YAML"

# render <dir> [docker -e args...]: the config the image's binary loads from <dir>/development.yaml, under the env the
# cell gives every server role (cell.yml x-server-env), as YAML; the binary's own log lines dropped. BIND_ON_IP stands in for
# the address the image's entrypoint.sh sets from the container's host name, so a template that renders it is compared.
mkdir -p "$tmp/up" "$tmp/laug"; cp "$upstream" "$tmp/up/development.yaml"; cp "$vendored" "$tmp/laug/development.yaml"
chmod 755 "$tmp" "$tmp/up" "$tmp/laug"; chmod 644 "$tmp/up/development.yaml" "$tmp/laug/development.yaml"
cell_env=(-e BIND_ON_IP=192.0.2.10)
while IFS= read -r kv; do cell_env+=(-e "$kv"); done < <(yq -r '."x-server-env" | to_entries[] | "\(.key)=\(.value)"' "$here/compose/cell.yml" 2>/dev/null | sed 's/\${ENABLE_ES:-false}/false/')
# It returns render-config's exit status, so a file the server cannot render is a FAIL line, not the end of the script.
render() {
  local dir="$1" out rc=0; shift
  out="$(docker run --rm --network none --entrypoint temporal-server -v "$dir:/etc/temporal/config:ro" "${cell_env[@]}" "$@" "$image" render-config 2>&1)" || rc=$?
  grep -vE '^[0-9]{4}/[0-9]{2}/[0-9]{2} [0-9:]{8} ' <<<"$out" || true
  return "$rc"
}

# 3. Unset, the file is the template.
rc_up=0; render "$tmp/up" -e TEMPORAL_SERVICES=history > "$tmp/up.yaml" || rc_up=$?
rc_laug=0; render "$tmp/laug" -e TEMPORAL_SERVICES=history > "$tmp/laug.yaml" || rc_laug=$?
if (( rc_up )) || ! yq -e '.persistence.datastores.default.sql.pluginName == "postgres12"' "$tmp/up.yaml" >/dev/null 2>&1; then
  bad "3. the upstream template did not render with the cell's env (render-config exit $rc_up)" "$(tail -5 "$tmp/up.yaml")"
elif (( rc_laug )); then
  bad "3. the vendored file did not render with LAUG_FI_* unset (render-config exit $rc_laug)" "$(tail -5 "$tmp/laug.yaml")"
elif d="$(diff "$tmp/up.yaml" "$tmp/laug.yaml")"; then
  ok "3. LAUG_FI_* unset: the same config as the embedded template ($(wc -l < "$tmp/up.yaml") lines rendered), faultInjection null"
else bad "3. LAUG_FI_* unset: the rendered config differs from the embedded template's" "$(head -20 <<<"$d")"; fi

# 4. The vendored block's name lists against the server source at this version.
errors_src="$(sed -n '/^func newFault(/,/^}/p' "$mod/common/persistence/faultinjection/fault.go" | sed -n 's/^[[:space:]]*case "\([A-Za-z]*\)":.*/\1/p' | sort | xargs)"
stores_src="$(sed -n 's/^[[:space:]]*[A-Za-z0-9]*Name[[:space:]]*DataStoreName = "\([A-Za-z0-9]*\)"$/\1/p' "$mod/common/config/config.go" | sort | xargs)"
list_after() { grep -F "$1" "$vendored" | head -1 | grep -oE '\(list( "[A-Za-z0-9]+")+\)' | grep -oE '"[A-Za-z0-9]+"' | tr -d '"' | sort | xargs; }
errors_tpl="$(list_after '(has $fiErr (list')"; stores_tpl="$(list_after '(has $fiStore (list')"
[[ -n "$errors_src" && "$errors_src" == "$errors_tpl" ]] && ok "4. error names: $errors_src" \
  || bad "4. error names differ" "fault.go newFault: $errors_src" "laug-server.yaml:   $errors_tpl"
[[ -n "$stores_src" && "$stores_src" == "$stores_tpl" ]] && ok "4. store names: $stores_src" \
  || bad "4. store names differ" "config.go DataStoreName: $stores_src" "laug-server.yaml:        $stores_tpl"

# 5. Set, the block renders what the server reads.
fi_of() { yq -o json -I0 '.persistence.datastores | map_values(.faultInjection)' 2>/dev/null; }
# A render that fails gives its last lines in place of the config, so the comparison below names why.
rendered() { local out; out="$(render "$@")" && echo "$out" || echo "render-config failed: $(tail -3 <<<"$out" | paste -sd ' ')"; }
got="$(rendered "$tmp/laug" -e LAUG_FI_STORE=ExecutionStore -e LAUG_FI_METHOD=UpdateWorkflowExecution -e LAUG_FI_ERROR=Timeout \
  -e LAUG_FI_RATE=0.01 -e LAUG_FI_SEED=6001)"
want='{"default":{"targets":{"dataStores":{"ExecutionStore":{"methods":{"UpdateWorkflowExecution":{"errors":{"Timeout":0.01},"seed":6001}}}}}},"visibility":null}'
[[ "$(fi_of <<<"$got")" == "$want" ]] && ok "5. one error: ExecutionStore.UpdateWorkflowExecution Timeout 0.01 seed 6001 on the default store, visibility null" \
  || bad "5. one error rendered wrong" "got:  $(fi_of <<<"$got" || true)" "want: $want" "$(grep -m1 '^render-config failed' <<<"$got" || true)"
# DefaultFaultInjection's errors as "<store> <method> <error> <rate>", in its order; each method's seed is the base plus
# the order in which the function first names that method, as the profile renders it.
dfi="$(sed -n '/^func DefaultFaultInjection/,/^}/p' "$mod/common/config/config.go" \
  | sed -n 's/.*WithError(\([A-Za-z0-9]*\)Name, "\([A-Za-z]*\)", "\([A-Za-z]*\)", \([0-9.]*\)).*/\1 \2 \3 \4/p')"
profile_seeds() { # profile_seeds <base>: the rendered profile, one "<store> <method> <error> <rate> <seed>" line per error
  rendered "$tmp/laug" -e LAUG_FI_PROFILE=default ${1:+-e LAUG_FI_SEED=$1} | yq -r '.persistence.datastores.default.faultInjection.targets.dataStores
    | to_entries[] | .key as $s | .value.methods | to_entries[] | .key as $m | .value.seed as $seed | .value.errors | to_entries[]
    | "\($s) \($m) \(.key) \(.value) \($seed)"' 2>/dev/null | sort || true
}
want="$(awk '!($2 in i) { i[$2] = n++ } { print $0, 6008 + i[$2] }' <<<"$dfi" | sort)"; got="$(profile_seeds 6008)"
if [[ -n "$dfi" && "$got" == "$want" ]]; then
  ok "5. profile default: DefaultFaultInjection's $(wc -l <<<"$want") errors on $(cut -d' ' -f2 <<<"$want" | sort -u | wc -l) methods, seeds 6008 to $(cut -d' ' -f5 <<<"$want" | sort -n | tail -1), one per method"
else bad "5. profile default differs from DefaultFaultInjection" "$(diff <(echo "$want") <(echo "$got") | head -20)"; fi
want="$(awk '{ print $0, 0 }' <<<"$dfi" | sort)"; got="$(profile_seeds "")"
[[ -n "$dfi" && "$got" == "$want" ]] && ok "5. profile default with no seed: every method seed 0, time-based in the server" \
  || bad "5. profile default with no seed: not every method 0" "$(diff <(echo "$want") <(echo "$got") | head -20)"
out="$(render "$tmp/laug" -e LAUG_FI_STORE=ShardStore -e LAUG_FI_METHOD=UpdateShard -e LAUG_FI_ERROR=ShardOwnershipLostError -e LAUG_FI_RATE=1.0)" || true
grep -q 'laug: LAUG_FI_ERROR "ShardOwnershipLostError" is not one of' <<<"$out" && ok "5. ShardOwnershipLostError fails to render, before the server could panic on it" \
  || bad "5. ShardOwnershipLostError was not refused" "$(tail -3 <<<"$out")"
out="$(render "$tmp/laug" -e LAUG_FI_PROFILE=default -e LAUG_FI_ERROR=Timeout)" || true
grep -q 'laug: set LAUG_FI_ERROR or LAUG_FI_PROFILE, not both' <<<"$out" && ok "5. LAUG_FI_ERROR and LAUG_FI_PROFILE together fail to render" \
  || bad "5. LAUG_FI_ERROR and LAUG_FI_PROFILE together were not refused" "$(tail -3 <<<"$out")"

# 6. The catalog's serverfi rows against the wrappers at this tag. The store's wrapper is the one data_store_factory.go
# builds under config.<Store>Name (indexing the config in 1.31.0, through storeFaultInjector(...) in 1.32.1); its file
# holds one generate("<Method>"...) call per wrapped method (1.32.1 also passes the request).
catalog="${CATALOG:-$root/faults/catalog.yaml}"; fi_dir="$mod/common/persistence/faultinjection"
wrapped() { # wrapped <DataStoreName>: the methods that store's fault injection wrapper draws for, one per line
  local w f
  w="$(awk -v a="config.$1Name]" -v b="config.$1Name)" '(index($0, a) || index($0, b)) { getline
    if (match($0, /newFaultInjection[A-Za-z0-9]+\(/)) print substr($0, RSTART + 17, RLENGTH - 18); exit }' "$fi_dir/data_store_factory.go")"
  [[ -n "$w" ]] && f="$(grep -l "^func newFaultInjection$w(" "$fi_dir"/*_gen.go | head -1)" && [[ -n "$f" ]] || return 1
  sed -n 's/.*\.generate("\([A-Za-z0-9]*\)"[,)].*/\1/p' "$f" | sort -u
}
checked=(); missed=(); profiles=0
while read -r row inj; do
  if [[ " $inj " == *" profile="* ]]; then profiles=$(( profiles + 1 )); continue; fi
  store=""; method=""
  for kv in $inj; do case "$kv" in store=*) store="${kv#store=}" ;; method=*) method="${kv#method=}" ;; esac; done
  list="$( [[ -n "$store" ]] && wrapped "$store" || true)"
  if [[ -n "$method" ]] && grep -qxF -- "$method" <<<"$list"; then checked+=("$store.$method")
  else missed+=("$row: ${store:-no store}.${method:-no method}"); fi
done < <(yq -r '.faults | to_entries[] | select(.value.inject // "" | test("^serverfi on ")) | .key + " " + .value.inject' "$catalog" 2>/dev/null)
also=""; (( profiles == 0 )) || also="; $profiles profile row(s), the server's own set"
if (( ${#missed[@]} == 0 && ${#checked[@]} > 0 )); then
  ok "6. ${#checked[@]} serverfi row(s) in $(basename "$catalog") name wrapped methods: $(printf '%s\n' "${checked[@]}" | sort -u | xargs)$also"
else bad "6. serverfi row(s) in ${catalog#"$root"/} name a method the v$version wrapper does not draw for, or none was found" "${missed[@]:-no serverfi rows read}"; fi

if (( failed )); then echo "laug · fi-template-check: FAIL on server tag $version ($image); the file is the ${vendored_tag:-unknown} template" >&2; exit 1; fi
echo "laug · fi-template-check: pass on server tag $version ($image)"
