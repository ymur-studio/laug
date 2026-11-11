#!/usr/bin/env bash
# _obs/obs.sh: the observability stack's lifecycle (_obs/README.md), run by bin/obs <verb> and test by make
# obs-test, from the repository's root; each line it prints starts "laug · obs <verb>: ", the verb that runs it,
# and test's lines the same with make obs-test in its place (VERB). Built so far: up (the fetch, the configs copied,
# each part's start line and record, the listener audit, the settle wait), down, status and purge; test, its static
# half and the fake cell; find; backfill; pack and view. up starts all six parts: the four
# releases, and cellstat's listener and follow, the checkout's bin/obs copied once into OBS_DIR/bin and run as bin/obs
# usage record -out none and bin/obs follow.
# obs-watch, one viewer's load on the gate run board, is not built: a stub, with no verb or target.
#
# State lives in OBS_DIR, outside every git work tree: ${XDG_STATE_HOME:-$HOME/.local/state}/laug-obs by default.
#   .laug-obs     the marker: when it was made and by which commit; a directory without it is not the stack's
#   dist/         the release tarballs, as fetched
#   bin/<part>-<version>/   each tarball unpacked in a directory of its own, with .checked once its hashes matched:
#                 the versions.txt line it was checked against, then the tarball's sha256
#   bin/obs-<sha256, 12 hex>/obs   bin/obs as an obs up copied it, a directory for each build
#   conf/         the configs, boards and A/A lists as bin/obs up copied them, and COMMIT, the commit they came from
#   prometheus/   the store's TSDB; jaeger/keys and jaeger/values, Badger; grafana/, its database and admin-password
#   run/<part>.pid  {pid, start, boot_id}; run/<part>.conf, the configs it started with; log/<part>.log, the previous
#                 start's kept as <part>.log.1
#   targets/      the federation's target files and .expires.json, written by follow only
#   follow/       the gate directories follow follows, one <gate id>.json each: absolute paths, never shown or served
#   backfill.new/ bin/obs backfill's OpenMetrics and blocks while it runs; kept only when a gate did not render
#   packs/        bin/obs pack's tarballs, unless -pack names another place; pack.new/ while one is made. bin/obs
#                 purge refuses while it holds one, unless -remove-packs
#   view/         bin/obs view's pack, unpacked, with its own conf/, grafana/ and log/; run/view.json while it runs
set -uo pipefail

VERSIONS=_obs/versions.txt
OBS_DIR="${OBS_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/laug-obs}"; OBS_DIR="${OBS_DIR%/}"
OBS_MIN_FREE_GIB="${OBS_MIN_FREE_GIB:-30}"
# Where a part's start line finds its configs, the store's TSDB and its log: OBS_DIR's own, or bin/obs view's.
PART_CONF="$OBS_DIR/conf" PART_TSDB="$OBS_DIR/prometheus" PART_LOGS="$OBS_DIR/log"
say() { echo "laug · $VERB: $*"; }
plural() { if [[ "$1" == 1 ]]; then echo "1 $2"; else echo "$1 ${3:-$2s}"; fi; }   # plural <n> <one> [<many>]
die() { echo "laug · $VERB: $*" >&2; exit 2; }

# versions_lines: each release line of versions.txt as "<line number> <part> <version> <url> <size> <sha256> [file=sha256 ...]",
# its comment cut off; a line of another shape stops the caller, and so does a tarball sha256 of - with no
# <file>=<sha256>, since nothing of that line would be hashed.
versions_lines() {
  local n line part ver url size sum rest
  while IFS= read -r line; do
    n="${line%%:*}"; line="${line#*:}"; line="${line%%#*}"
    read -r part ver url size sum rest <<<"$line"
    [[ -n "$part" ]] || continue
    [[ "$part" =~ ^[a-z]+$ && "$ver" =~ ^[0-9]+(\.[0-9]+)+$ && "$url" =~ ^https://[A-Za-z0-9./_-]+\.tar\.gz$ \
       && "$size" =~ ^[0-9]+$ && "$sum" =~ ^([0-9a-f]{64}|-)$ && "$rest" =~ ^([a-z]+=[0-9a-f]{64}( |$))*$ ]] \
      || { echo "$VERSIONS:$n: not <part> <version> <https URL of a .tar.gz> <size> <sha256 or -> [<file>=<sha256>]" >&2; return 2; }
    [[ "$sum" != - || -n "$rest" ]] || { echo "$VERSIONS:$n: a tarball sha256 of - needs a <file>=<sha256>, or nothing of $part is hashed" >&2; return 2; }
    echo "$n $part $ver $url $size $sum $rest"
  done < <(grep -n '' "$VERSIONS" | grep -v '^[0-9]*:[[:space:]]*#')
}

# line_record <url> <size> <sum> <rest>: a line of versions.txt as the first line of .checked holds it.
line_record() { local r=($4); echo "$1 $2 $3${r[*]:+ ${r[*]}}"; }

# tree_ok <tree> <url> <size> <sum> <rest>: true when <tree> was checked against this very line of versions.txt (the
# first line of its .checked is the line's record) and each <file>=<sha256> of the line hashes the same in the tree
# now. Otherwise false, and why on stdout. So a line changed after its tree was checked, its tarball sha256 or a file's,
# is never taken for a checked one.
tree_ok() {
  local tree="$1" pair file want path got
  [[ -f "$tree/.checked" ]] || { echo "it was never checked"; return 1; }
  [[ "$(head -1 -- "$tree/.checked")" == "$(line_record "$2" "$3" "$4" "$5")" ]] \
    || { echo "it was checked against another line of $VERSIONS than this one"; return 1; }
  for pair in $5; do
    file="${pair%%=*}"; want="${pair#*=}"
    path="$(find "$tree" -type f -name "$file")"
    [[ -n "$path" && "$path" != *$'\n'* ]] || { echo "it holds not one file named $file"; return 1; }
    got="$(sha256sum -- "$path" | cut -d' ' -f1)"
    [[ "$got" == "$want" ]] || { echo "its ${path#"$tree"/} has sha256 $got now, not $want"; return 1; }
  done
}

# obs_bin <part> <file>: the path of <file> in <part>'s tree when that tree holds against versions.txt as it reads now
# (tree_ok), or nothing.
obs_bin() {
  local n part ver url size sum rest
  while read -r n part ver url size sum rest; do
    [[ "$part" == "$1" ]] && tree_ok "$OBS_DIR/bin/$part-$ver" "$url" "$size" "$sum" "$rest" >/dev/null || continue
    find "$OBS_DIR/bin/$part-$ver" -maxdepth 3 -type f -name "$2" -perm -u+x | head -1
  done < <(versions_lines)
}

# gate_open: why and 0 when a gate is open, else 1 (docs/DESIGN.md section 9). One function answers it, bin/obs
# follow -gate-running, so follow and this script never disagree: a gate is open when the host lock is held, laug-gate.lock
# in XDG_RUNTIME_DIR, in /run/user/<uid> whether XDG_RUNTIME_DIR is set or not, or in /tmp (gate/lib.sh,
# GATE_HOST_LOCK), or when the laugd on 127.0.0.1:7700 is a gate's (its stdout is a gate directory's laugd.log) and
# that gate's timeline has no done line. A lock is read from /proc/locks and the holders' fdinfo, never taken: a probe
# that took it, even shared and for a few ms, made a gate's flock -n in that moment refuse (seen on the dev
# host, cells/results/2026-10-17-obs-offline.txt section 2), and gate/run.sh's lock, taken by flock -n 8 whose process
# exits at once, shows in the holder's fdinfo only. make build builds bin/obs; bin/obs up, backfill, pack, view and
# purge refuse without it, make obs-test builds it first, and bin/obs down sets OBS_FOLLOW_BUILD=failed without it,
# so that -while-gate-runs can still stop the stack. An answer follow cannot give, a missing binary among them, counts
# as a gate open.
gate_open() {
  local out rc
  if [[ "${OBS_FOLLOW_BUILD:-}" == failed ]]; then
    echo "bin/obs is not built, so it cannot tell, and a gate counts as running"; return 0
  fi
  out="$(bin/obs follow -gate-running 2>&1)"; rc=$?
  case "$rc" in
    0) echo "${out#laug · obs follow: a gate runs: }"; return 0 ;;
    1) return 1 ;;
    *) echo "bin/obs follow -gate-running could not tell (exit $rc: ${out:-no output}), so a gate counts as running; make build builds bin/obs"; return 0 ;;
  esac
}

# The refusals that come before anything is written into OBS_DIR (_obs/cmd/obs/doc.go, up): a relative OBS_DIR or one
# with a character the configs could not hold, OBS_DIR inside a git work tree, a directory that is not the stack's, and
# less free space than OBS_MIN_FREE_GIB on its filesystem. bin/obs up's others are plan_check's and gate_open's.
obs_dir_ready() {
  local up="$OBS_DIR" free
  [[ "$OBS_DIR" == /* ]] || die "OBS_DIR must be an absolute path"
  [[ "$OBS_DIR" =~ ^/[A-Za-z0-9._/+-]+$ ]] || die "OBS_DIR may hold letters, digits and . _ + - / only, since ${OBS:-bin/obs} up writes it into the configs"
  [[ "$OBS_MIN_FREE_GIB" =~ ^[0-9]+$ ]] || die "OBS_MIN_FREE_GIB must be a whole number of GiB"
  while [[ ! -d "$up" ]]; do up="$(dirname -- "$up")"; done
  if git -C "$up" rev-parse --is-inside-work-tree >/dev/null 2>&1; then die "OBS_DIR is inside a git work tree; set it outside every checkout"; fi
  free=$(( $(df -Pk -- "$up" | awk 'NR == 2 { print $4 }') / 1048576 ))
  (( free >= OBS_MIN_FREE_GIB )) || die "$free GiB free on OBS_DIR's filesystem, under OBS_MIN_FREE_GIB ($OBS_MIN_FREE_GIB)"
  if [[ -d "$OBS_DIR" && ! -f "$OBS_DIR/.laug-obs" && -n "$(ls -A -- "$OBS_DIR")" ]]; then
    die "OBS_DIR holds files and no .laug-obs marker, so it is not the stack's"
  fi
  mkdir -p -- "$OBS_DIR/dist" "$OBS_DIR/bin" || die "cannot make OBS_DIR"
  [[ -f "$OBS_DIR/.laug-obs" ]] || printf 'created %s by %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)" >"$OBS_DIR/.laug-obs"
}

# fetch: each release of versions.txt into dist/, its size and sha256 checked, then unpacked into bin/<part>-<version>/
# and each <file>=<sha256> of its line checked inside the tree. A tarball is unpacked only after its hash matched; a
# tarball or a file that does not match is kept as .bad, never used, and named with the line and the sha256 seen. A
# line whose tarball sha256 is - (versions.txt holds none yet) is checked by its file hashes, and the tarball's sha256
# is printed so that it can go into versions.txt. A tree checked before is left as it is only when tree_ok holds: it
# was checked against the line as it reads now, and its files hash the same. Otherwise it is removed and checked
# again from the tarball, so a changed line stops on its own hash. Every line is tried; the exit is 2 if one failed.
# fetch <part>... fetches those releases of versions.txt only (bin/obs view's two); with none, all four.
fetch() {
  local lines n part ver url size sum rest name dist tree tmp got_size got_sum pair file want path got bad=0 only=" $* "
  lines="$(versions_lines)" || exit 2
  obs_dir_ready
  while read -r n part ver url size sum rest; do
    [[ "$only" == "  " || "$only" == *" $part "* ]] || continue
    name="${url##*/}"; dist="$OBS_DIR/dist/$name"; tree="$OBS_DIR/bin/$part-$ver"
    if [[ -e "$tree" ]]; then
      if why="$(tree_ok "$tree" "$url" "$size" "$sum" "$rest")"; then
        say "$part $ver: checked before against $VERSIONS:$n as it reads now$([[ -n "$rest" ]] && echo ", its files hashed again"); left as it is"; continue
      fi
      say "$part $ver: the tree in OBS_DIR does not hold against $VERSIONS:$n: $why; removed, and checked again from the tarball"
      rm -rf -- "$tree"
    fi
    if [[ ! -f "$dist" ]]; then
      rm -f -- "$dist.part"
      curl -fsSL --proto '=https' --proto-redir '=https' --retry 3 --connect-timeout 20 -o "$dist.part" -- "$url" \
        || { echo "laug · $VERB: $VERSIONS:$n: $part $ver: the download failed" >&2; rm -f -- "$dist.part"; bad=1; continue; }
      mv -- "$dist.part" "$dist"
    fi
    got_size="$(stat -c %s -- "$dist")"; got_sum="$(sha256sum -- "$dist" | cut -d' ' -f1)"
    if [[ "$got_size" != "$size" ]]; then
      echo "laug · $VERB: $VERSIONS:$n: $part $ver: $got_size B, not $size; sha256 $got_sum; kept as $name.bad, not unpacked" >&2
      mv -f -- "$dist" "$dist.bad"; bad=1; continue
    fi
    if [[ "$sum" != - && "$got_sum" != "$sum" ]]; then
      echo "laug · $VERB: $VERSIONS:$n: $part $ver: sha256 $got_sum, not $sum; kept as $name.bad, not unpacked" >&2
      mv -f -- "$dist" "$dist.bad"; bad=1; continue
    fi
    tmp="$OBS_DIR/bin/.$part-$ver.unpack"; rm -rf -- "$tmp" "$tree"; mkdir -p -- "$tmp"
    tar -xzf "$dist" -C "$tmp" --no-same-owner || { echo "laug · $VERB: $VERSIONS:$n: $part $ver: tar failed" >&2; rm -rf -- "$tmp"; bad=1; continue; }
    for pair in $rest; do
      file="${pair%%=*}"; want="${pair#*=}"
      path="$(find "$tmp" -type f -name "$file")"
      if [[ -z "$path" || "$path" == *$'\n'* ]]; then
        echo "laug · $VERB: $VERSIONS:$n: $part $ver: not one file named $file in the tarball" >&2; rm -rf -- "$tmp"; bad=1; continue 2
      fi
      got="$(sha256sum -- "$path" | cut -d' ' -f1)"
      if [[ "$got" != "$want" ]]; then
        echo "laug · $VERB: $VERSIONS:$n: $part $ver: ${path#"$tmp"/} sha256 $got, not $want; tarball kept as $name.bad" >&2
        rm -rf -- "$tmp"; mv -f -- "$dist" "$dist.bad"; bad=1; continue 2
      fi
      say "$part $ver: ${path#"$tmp"/} sha256 $got, as $VERSIONS:$n says"
    done
    { line_record "$url" "$size" "$sum" "$rest"; echo "tarball sha256 $got_sum"; } >"$tmp/.checked"; mv -- "$tmp" "$tree"
    if [[ "$sum" == - ]]; then
      say "$part $ver: $got_size B as $VERSIONS:$n says; $VERSIONS holds no tarball sha256 for it, this fetch's is $got_sum; unpacked"
    else
      say "$part $ver: $got_size B, sha256 $got_sum, as $VERSIONS:$n says; unpacked"
    fi
  done <<<"$lines"
  (( bad == 0 )) || exit 2
}

# The configs, as paths under _obs/: the stack's, which bin/obs up copies into OBS_DIR/conf, and the fake cell's.
CONFIGS=(prometheus.yml collector.yaml jaeger.yaml grafana/grafana.ini grafana/provisioning/datasources/stack.yml
         grafana/provisioning/dashboards/stack.yml test/cell.yml)
# Every port a config may name (SECURITY.md's listener table): the stack's listeners, obs-test's fake cell on 7768 and
# 7769, and 7402, the orchestrator's dev server, which the store scrapes. Nothing else, and never 9090.
PORTS=" 7402 7710 7720 7730 7740 7751 7752 7753 7760 7761 7762 7763 7768 7769 "
# Every listener the Collector's and Jaeger's configs open: <file>|<the key, as a jq path over the file as JSON>|<the
# one value it may hold, as JSON>. Each config's header names these listeners (_obs/collector.yaml, _obs/jaeger.yaml),
# and SECURITY.md's listener table gives their ports. A key left out binds its part's default (4318 for OTLP over HTTP,
# 8888 for a telemetry reader: the parts' docs), so a missing key is refused as a wrong one.
# Prometheus takes its listener from the start line; Grafana's is grafana.ini's [server], which audit reads on its own.
LISTENERS=(
  'collector.yaml|.receivers.otlp.protocols.http.endpoint|"127.0.0.1:7751"'
  'collector.yaml|.exporters.prometheus.endpoint|"127.0.0.1:7752"'
  'collector.yaml|.service.telemetry.metrics.readers|[{"pull":{"exporter":{"prometheus":{"host":"127.0.0.1","port":7753}}}}]'
  'jaeger.yaml|.receivers.otlp.protocols.http.endpoint|"127.0.0.1:7760"'
  'jaeger.yaml|.extensions.jaeger_query.http.endpoint|"127.0.0.1:7761"'
  'jaeger.yaml|.extensions.jaeger_query.grpc.endpoint|"127.0.0.1:7762"'
  'jaeger.yaml|.service.telemetry.metrics.readers|[{"pull":{"exporter":{"prometheus":{"host":"127.0.0.1","port":7763}}}}]'
)

# audit <dir>: the configs under <dir> (_obs/ or a planted copy), read as text and as YAML, before any binary sees them.
# Every address is 127.0.0.1 with a port of PORTS, written out: no 0.0.0.0, no ::, no localhost (it may bind ::1:
# inference), no port without a host (":7752" binds every address in Go's net.Listen), no ${...} (the Collector
# and Jaeger fill it from the environment, seen, as Grafana's docs give ${...} and $__env{...}), and every listener of
# LISTENERS at its one value, so none is left to a default. Grafana's [server] holds http_addr 127.0.0.1 and http_port
# 7740 once each, in one [server] section (an empty http_addr "will bind to all interfaces", its conf/defaults.ini
# says). An OTLP gRPC protocol or a Jaeger extension would bind a default of its own. The store has no rule
# file, alerting or remote write, and Grafana no alerting: the stack judges nothing. One line a problem on stdout, and 2.
audit() {
  local d="$1" f n=0 text hit ln addr host port path want got
  for f in "${CONFIGS[@]}"; do
    [[ -f "$d/$f" ]] || { echo "_obs/$f: missing"; n=$((n + 1)); continue; }
    # The text without its comments, one line for one: YAML's # at a line's start or after a space, ini's ; and #.
    if [[ "$f" == *.ini ]]; then text="$(sed -E 's/^[[:space:]]*[;#].*$//' "$d/$f")"; else text="$(sed -E 's/(^|[[:space:]])#.*$//' "$d/$f")"; fi
    while IFS= read -r hit; do [[ -n "$hit" ]] && { echo "_obs/$f:${hit%%:*}: 0.0.0.0 is every address"; n=$((n + 1)); }; done < <(grep -nF '0.0.0.0' <<<"$text")
    while IFS= read -r hit; do [[ -n "$hit" ]] && { echo "_obs/$f:${hit%%:*}: localhost may bind ::1; write 127.0.0.1"; n=$((n + 1)); }; done < <(grep -niF 'localhost' <<<"$text")
    while IFS= read -r hit; do [[ -n "$hit" ]] && { echo "_obs/$f:${hit%%:*}: an IPv6 address; every listener is 127.0.0.1"; n=$((n + 1)); }; done < <(grep -nF '::' <<<"$text")
    while IFS= read -r hit; do [[ -n "$hit" ]] && { echo "_obs/$f:${hit%%:*}: a port with no host binds every address; write 127.0.0.1:<port>"; n=$((n + 1)); }; done < <(grep -nE '(^|[^A-Za-z0-9._-]):[0-9]+([^0-9]|$)' <<<"$text")
    while IFS= read -r hit; do [[ -n "$hit" ]] && { echo "_obs/$f:${hit%%:*}: \${...} or \$__env{...} is filled from the environment, so an address could come from it"; n=$((n + 1)); }; done < <(grep -nE '\$\{|\$__env\{' <<<"$text")
    while IFS=: read -r ln host port; do
      [[ -n "$ln" ]] || continue
      [[ "$host" == 127.0.0.1 ]] || { echo "_obs/$f:$ln: $host:$port: not 127.0.0.1"; n=$((n + 1)); }
      [[ "$PORTS" == *" $port "* ]] || { echo "_obs/$f:$ln: $host:$port: port $port is not the stack's (SECURITY.md)"; n=$((n + 1)); }
    done < <(grep -noE '[A-Za-z0-9._-]+:[0-9]{2,5}\b' <<<"$text")
    while IFS=: read -r ln addr; do
      [[ -n "$ln" ]] || continue
      addr="$(sed -E 's/^[[:space:]]*host:[[:space:]]*//; s/"//g; s/[[:space:]]*$//' <<<"$addr")"
      [[ "$addr" == 127.0.0.1 ]] || { echo "_obs/$f:$ln: host $addr: not 127.0.0.1"; n=$((n + 1)); }
    done < <(grep -nE '^[[:space:]]*host:' <<<"$text")
    while IFS=: read -r ln addr; do
      [[ -n "$ln" ]] || continue
      addr="$(sed -E 's/^[[:space:]]*port:[[:space:]]*//; s/"//g; s/[[:space:]]*$//' <<<"$addr")"
      [[ "$PORTS" == *" $addr "* ]] || { echo "_obs/$f:$ln: port $addr is not the stack's"; n=$((n + 1)); }
    done < <(grep -nE '^[[:space:]]*port:' <<<"$text")
  done
  local ini="$d/grafana/grafana.ini"
  if [[ -f "$ini" ]]; then
    # By section: the number of [server] sections, then for http_addr and http_port how many lines set them anywhere
    # and how many of those are not [server]'s one right value. Sections and keys are read in lower case.
    read -r servers addrs bad_addr ports bad_port < <(awk '
      { line = tolower($0) }
      line ~ /^[[:space:]]*[;#]/ { next }
      line ~ /^[[:space:]]*\[/ { s = line; gsub(/[[:space:]]/, "", s); if (s == "[server]") servers++; next }
      function val(l) { sub(/^[^=]*=[[:space:]]*/, "", l); sub(/[[:space:]]*$/, "", l); return l }
      line ~ /^[[:space:]]*http_addr[[:space:]]*=/ { addrs++; if (s != "[server]" || val(line) != "127.0.0.1") bad_addr++ }
      line ~ /^[[:space:]]*http_port[[:space:]]*=/ { ports++; if (s != "[server]" || val(line) != "7740") bad_port++ }
      END { print servers + 0, addrs + 0, bad_addr + 0, ports + 0, bad_port + 0 }' "$ini")
    (( servers == 1 )) || { echo "_obs/grafana/grafana.ini: $servers [server] sections, not 1"; n=$((n + 1)); }
    (( addrs == 1 && bad_addr == 0 )) \
      || { echo "_obs/grafana/grafana.ini: http_addr is not 127.0.0.1 once, in [server] ($addrs set, $bad_addr not so; an empty one is every address)"; n=$((n + 1)); }
    (( ports == 1 && bad_port == 0 )) || { echo "_obs/grafana/grafana.ini: http_port is not 7740 once, in [server] ($ports set, $bad_port not so)"; n=$((n + 1)); }
    awk '/^\[/ { s = $0 } s == "[unified_alerting]" && /^[[:space:]]*enabled[[:space:]]*=[[:space:]]*false[[:space:]]*$/ { ok = 1 } END { exit !ok }' "$ini" \
      || { echo "_obs/grafana/grafana.ini: [unified_alerting] is not enabled = false"; n=$((n + 1)); }
    # grafana.com's two URLs are Grafana's own, so a fetch it has no key to turn off stays on 127.0.0.1.
    awk '/^\[/ { s = $0 } s == "[grafana_com]" && /^[[:space:]]*(url|api_url)[[:space:]]*=[[:space:]]*http:\/\/127\.0\.0\.1:7740(\/api)?[[:space:]]*$/ { ok++ }
         END { exit !(ok == 2) }' "$ini" \
      || { echo "_obs/grafana/grafana.ini: [grafana_com] url and api_url are not Grafana's own http://127.0.0.1:7740, so it calls grafana.com"; n=$((n + 1)); }
  fi
  # Each YAML config as JSON, read once.
  local l; local -A js=()
  for f in prometheus.yml collector.yaml jaeger.yaml; do [[ -f "$d/$f" ]] && js[$f]="$(yq -o=json -I=0 '.' "$d/$f" 2>/dev/null)"; done
  for l in "${LISTENERS[@]}"; do
    IFS='|' read -r f path want <<<"$l"
    [[ -f "$d/$f" ]] || continue
    got="$(jq -c "$path" <<<"${js[$f]}" 2>/dev/null)"
    [[ "$got" == "$(jq -c . <<<"$want")" ]] \
      || { echo "_obs/$f: $path is ${got:-unreadable}, not $want$([[ "$got" == null ]] && echo ', so it binds its default')"; n=$((n + 1)); }
  done
  ycheck() { # ycheck <file> <jq expression over the file as JSON that must print true> <what it found otherwise>
    [[ -f "$d/$1" ]] || return 0
    [[ "$(jq "$2" <<<"${js[$1]}" 2>/dev/null)" == true ]] || { echo "_obs/$1: $3"; n=$((n + 1)); }
  }
  ycheck prometheus.yml '(["rule_files", "alerting", "remote_write", "remote_read"] - keys) | length == 4' 'a rule file, alerting or remote write'
  ycheck collector.yaml '(.receivers | keys) == ["host_metrics", "otlp"]' 'a receiver other than otlp and host_metrics'
  ycheck collector.yaml '(.receivers.otlp.protocols | keys) == ["http"]' 'an OTLP protocol other than http, which binds its default'
  ycheck collector.yaml '((.extensions // {}) | length) == 0 and ((.service.extensions // []) | length) == 0' 'an extension, which binds its default'
  ycheck jaeger.yaml '(.receivers | keys) == ["otlp"] and (.receivers.otlp.protocols | keys) == ["http"]' 'a receiver or protocol other than OTLP over http'
  ycheck jaeger.yaml '.service.extensions == ["jaeger_storage", "jaeger_query"] and (.extensions | keys) == ["jaeger_query", "jaeger_storage"]' 'an extension other than jaeger_storage and jaeger_query'
  ycheck collector.yaml '.service.telemetry.traces.level == "none"' 'its own spans are not off (service.telemetry.traces.level none), so it may export them to a default address'
  ycheck jaeger.yaml '.service.telemetry.traces.level == "none" and .extensions.jaeger_query.enable_tracing == false' 'its own spans are not off (service.telemetry.traces.level none, jaeger_query.enable_tracing false): 2.21.0 sends them to 127.0.0.1:4317'
  (( n == 0 )) || return 2
}

# render <src> <dst> <state>: the configs under <src> into <dst>, as bin/obs up will copy them: OBS_DIR as <state>,
# and the device and mount point under OBS_DIR as the Collector's disk and filesystem scrapers read them (the
# device's kernel name, as in /proc/diskstats, is what the disk scraper's include matches: seen, and the hostmetrics
# receiver's disk scraper README at v0.162.0).
render() {
  local src="$1" dst="$2" state="$3" f up="$OBS_DIR" dev mnt
  while [[ ! -d "$up" ]]; do up="$(dirname -- "$up")"; done
  read -r dev mnt < <(findmnt -n -o SOURCE,TARGET --target "$up")
  dev="${dev##*/}"
  for f in "${CONFIGS[@]}"; do
    mkdir -p -- "$dst/$(dirname -- "$f")"
    sed -e "s#@OBS_DIR@#$state#g" -e "s#@OBS_DISK@#${dev:-none}#g" -e "s#@OBS_MOUNT@#${mnt:-/}#g" "$src/$f" >"$dst/$f"
  done
}

# The plants: one fault a line, put into a copy, each of which must exit 2. <name>|<file>|<how>, where <how> is a jq
# program for a board and a sed script for a config. The boards' faults go into _obs/test/board.json, which keeps every
# rule, and into the cells' board; the configs' into a copy of the CONFIGS under _obs/.
CELL_BOARD=dashboards/provisioning/dashboards/boards/gate.json
PLANTS=(
  'rule 1: a cell family with no cell|board|(.panels[] | select(.id == 3) | .targets[0].expr) = "histogram_quantile(0.99, sum(rate(persistence_latency_bucket[1m])) by (le)) * 1000"'
  'rule 2: a six-reading query changed|board|(.panels[] | select(.id == 2) | .targets[0].expr) |= sub("0[.]99"; "0.95")'
  'rule 3: a typed max|board|(.panels[] | select(.id == 3) | .fieldConfig.defaults.thresholds.steps) += [{"color": "#C44B32", "value": 23}]'
  'rule 1: cell=~".*", which also selects the series with no cell|board|(.panels[] | select(.id == 4) | .targets[0].expr) = "sum(rate(service_requests{cell=~\".*\"}[1m]))"'
  'rule 1: a cell family named inside __name__|board|(.panels[] | select(.id == 4) | .targets[0].expr) = "sum(rate({__name__=\"service_requests\"}[1m]))"'
  'rule 1: a cell variable whose All value is .*|board|.templating.list += [{"name": "any_cell", "type": "query", "multi": true, "includeAll": true, "allValue": ".*", "datasource": {"type": "prometheus", "uid": "laug-store"}, "query": {"query": "label_values(laug_item_info, cell)", "refId": "v"}}] | (.panels[] | select(.id == 4) | .targets[0].expr) |= gsub("gate_id=\"[$]gate\""; "cell=~\"$any_cell\"")'
  'rule 2: a six-reading query changed under another refId|board|(.panels[] | select(.id == 2) | .targets[0]) |= (.refId = "A" | .expr |= sub("0[.]99"; "0.95"))'
  'rule 3: a constant max line, vector(23)|board|(.panels[] | select(.id == 3) | .targets) += [{"refId": "B", "expr": "vector(23)", "datasource": {"type": "prometheus", "uid": "laug-store"}}]'
  'rule 3: a typed max beside a quantile written 0.990|board|(.panels[] | select(.id == 3) | .targets) |= map(select(.refId != "max")) | (.panels[] | select(.id == 3) | .targets[0].expr) |= sub("0[.]99"; "0.990") | (.panels[] | select(.id == 3) | .fieldConfig.defaults.thresholds.steps) += [{"color": "#C44B32", "value": 23}]'
  'rule 3: a typed max as the field'"'"'s max, the bar'"'"'s end|board|(.panels[] | select(.id == 3) | .fieldConfig.defaults.max) = 23'
  'rule 3: a typed max in an override'"'"'s max|board|(.panels[] | select(.id == 3) | .fieldConfig.overrides) += [{"matcher": {"id": "byFrameRefID", "options": "A"}, "properties": [{"id": "max", "value": 23}]}]'
  'rule 4: BLOCK outside a verdict panel|board|(.panels[] | select(.id == 9) | .title) = "Federation: BLOCK when a cell is down"'
  'rule 4: ADVANCE on the cells'"'"' board|cell|(.panels[] | select(.id == 1) | .fieldConfig.defaults.mappings) = [{"type": "range", "options": {"from": 0, "to": 0.001, "result": {"text": "ADVANCE", "index": 0}}}]'
  'rule 5: or vector(0) beside by (|board|(.panels[] | select(.id == 4) | .targets[0].expr) = "sum by (item) (rate(service_error_with_type{gate_id=\"$gate\", error_type!=\"serviceerror_Canceled\"}[1m])) or vector(0)"'
  'rule 5: or on() vector(0) beside by (|board|(.panels[] | select(.id == 4) | .targets[0].expr) = "sum by (item) (rate(service_error_with_type{gate_id=\"$gate\", error_type!=\"serviceerror_Canceled\"}[1m])) or on() vector(0)"'
  'rule 5: or vector(0) in a second query of a panel with by (|board|(.panels[] | select(.id == 4) | .targets) += [{"refId": "Z", "expr": "sum(rate(service_error_with_type{gate_id=\"$gate\", error_type!=\"serviceerror_Canceled\"}[1m])) or vector(0)", "datasource": {"type": "prometheus", "uid": "laug-store"}}]'
  'rule 6: a quantile over a gate by le alone|board|(.panels[] | select(.id == 3) | .targets[0].expr) |= sub("by [(]item, le[)]"; "by (le)")'
  'rule 6: an Omes quantile over every gate by le alone|board|(.panels[] | select(.id == 7) | .targets[0].expr) = "histogram_quantile(0.99, sum by (le) (rate(temporal_workflow_endtoend_latency_bucket{job=\"laug-omes\"}[1m]))) * 1000"'
  'rule 7: an annotation without * 1000|board|.annotations.list[1].expr |= sub(" [*] 1000$"; "")'
  'rule 8: nulls connected|board|(.panels[] | select(.id == 3) | .fieldConfig.defaults.custom.spanNulls) = true'
  'rule 9: the cells'"'"' datasource uid|board|(.panels[] | select(.id == 3) | .datasource.uid) = "laug-prometheus"'
  'rule 9: timezone browser|board|.timezone = "browser"'
  'rule 9: an Angular panel type|board|(.panels[] | select(.id == 8) | .type) = "graph"'
  'rule 10: the cells'"'"' gate.json maxima off by one|cell|(.panels[] | select(.id == 5) | .fieldConfig.defaults.thresholds.steps[1].value) += 1'
  'rule 10: a cells'"'"' board query changed|cell|(.panels[] | select(.id == 2) | .targets[0].expr) |= sub("0[.]99"; "0.95")'
  'rule 10: the cells'"'"' board in the browser'"'"'s timezone|cell|.timezone = "browser"'
  '0.0.0.0 in prometheus.yml|prometheus.yml|s/127\.0\.0\.1:7730/0.0.0.0:7730/'
  '0.0.0.0 in collector.yaml|collector.yaml|s/endpoint: 127\.0\.0\.1:7751/endpoint: 0.0.0.0:7751/'
  '0.0.0.0 in jaeger.yaml|jaeger.yaml|s/endpoint: 127\.0\.0\.1:7761/endpoint: 0.0.0.0:7761/'
  '0.0.0.0 in grafana.ini|grafana/grafana.ini|s/^http_addr = 127\.0\.0\.1$/http_addr = 0.0.0.0/'
  '0.0.0.0 in the datasources|grafana/provisioning/datasources/stack.yml|s#http://127\.0\.0\.1:7730#http://0.0.0.0:7730#'
  '0.0.0.0 in the fake cell|test/cell.yml|s/127\.0\.0\.1:7769/0.0.0.0:7769/'
  'localhost in an endpoint|collector.yaml|s/endpoint: 127\.0\.0\.1:7751/endpoint: localhost:7751/'
  'an empty http_addr|grafana/grafana.ini|s/^http_addr = 127\.0\.0\.1$/http_addr =/'
  'a port outside the table: 9090|prometheus.yml|s/127\.0\.0\.1:7402/127.0.0.1:9090/'
  'an IPv6 host|jaeger.yaml|s/host: 127\.0\.0\.1/host: "::"/'
  'an OTLP gRPC protocol|collector.yaml|s/^    protocols:$/    protocols:\n      grpc: {}/'
  'a Jaeger health extension|jaeger.yaml|s/^  extensions: \[jaeger_storage, jaeger_query\]$/  extensions: [jaeger_storage, jaeger_query, healthcheckv2]/'
  'a port with no host, ":7752"|collector.yaml|s/endpoint: 127\.0\.0\.1:7752/endpoint: ":7752"/'
  'the Collector'"'"'s OTLP endpoint left out|collector.yaml|/^      http:$/{N;s/^      http:\n        endpoint: 127\.0\.0\.1:7751.*$/      http: {}/}'
  'Jaeger'"'"'s OTLP endpoint left out|jaeger.yaml|/^      http:$/{N;s/^      http:\n        endpoint: 127\.0\.0\.1:7760$/      http: {}/}'
  'the telemetry reader'"'"'s host left out|collector.yaml|/^ *host: 127\.0\.0\.1$/d'
  'an address from the environment|jaeger.yaml|s/endpoint: 127\.0\.0\.1:7762/endpoint: "${env:JQ_GRPC}"/'
  'a second [server] with an empty http_addr|grafana/grafana.ini|$a [server]\nhttp_addr ='
  'Jaeger'"'"'s query server tracing itself|jaeger.yaml|s/^    enable_tracing: false$/    enable_tracing: true/'
  'Jaeger'"'"'s own spans left on, traces level none taken out|jaeger.yaml|/^    traces:$/{n;/^      level: none/d}'
  'the Collector'"'"'s own spans left on, traces level none taken out|collector.yaml|/^    traces:$/{n;/^      level: none/d}'
  'grafana.com left as Grafana'"'"'s API|grafana/grafana.ini|s#^api_url = http://127\.0\.0\.1:7740/api$#api_url = https://grafana.com/api#'
)

# The pack plants, in make obs-test's static half: bin/obs pack's check and bin/obs view's refusals on fixtures, each
# a stack state made in the test's scratch directory, so no release is needed, only bin/obs. A fixture is OBS_DIR's
# marker, prometheus/wal/00000000, one WAL segment of one snappy-compressed record made here byte by byte, and one
# block's chunks file. A plant is a fixture with one string put in, which must make bin/obs pack exit 2 and name the
# planted file and a line of check/deny-shapes.txt whose expression the string matches, or the kind of this host's word;
# the clean fixture must pack, its record decoded. No line here spells a planted string: each is put together at run
# time, so neither this file nor a grep of it holds one.
FX_BLOCK=01K7S2FZ0B0000000000000000   # a block's ULID, made up
# crc32c <hex byte>...: their CRC-32C (Castagnoli, 0x82F63B78 reflected), 8 hex digits, as a WAL fragment's header
# holds it (tsdb/wlog at v2.53.0: read, not run; bin/obs follow -decode checks it, and decode_test.go builds
# its segments the same way).
crc32c() {
  local c=$(( 0xFFFFFFFF )) b k
  for b in "$@"; do
    c=$(( c ^ 16#$b ))
    for k in 1 2 3 4 5 6 7 8; do c=$(( (c >> 1) ^ (0x82F63B78 & -(c & 1)) )); done
  done
  printf '%08x' $(( c ^ 0xFFFFFFFF ))
}
hex_of() { printf '%s' "$1" | od -An -v -tx1 | tr -s ' \n' '  '; }   # hex_of <string>: its bytes as hex, one a word
# shellcheck disable=SC2059  # the format is \x escapes only
bytes_of() { local h f=""; for h in "$@"; do f+="\\x$h"; done; printf "$f"; }   # bytes_of <hex>...: those bytes
# wal_segment <hex>...: a WAL segment of one record, one full fragment with its snappy flag (type 1, bit 3: 09), its
# length as a big-endian uint16 and its CRC-32C as a big-endian uint32, then the bytes, a snappy block.
wal_segment() {
  local len crc
  printf -v len '%04x' "$#"; crc="$(crc32c "$@")"
  bytes_of 09 "${len:0:2}" "${len:2:2}" "${crc:0:2}" "${crc:2:2}" "${crc:4:2}" "${crc:6:2}" "$@"
}
# snappy_literal <string>: a snappy block of the string, under 61 bytes, as one literal: its length as a uvarint, the
# tag (length - 1) << 2, then the bytes; as hex.
snappy_literal() {
  local -a h; read -r -a h <<<"$(hex_of "$1")"
  printf '%02x %02x %s' "${#h[@]}" $(( (${#h[@]} - 1) << 2 )) "${h[*]}"
}
# pack_fixture <dir> <record, hex> <string for the chunks file>
pack_fixture() {
  mkdir -p -- "$1/prometheus/wal" "$1/prometheus/$FX_BLOCK/chunks" || return 1
  echo "a make obs-test fixture" >"$1/.laug-obs"
  # shellcheck disable=SC2086  # one argument a byte
  wal_segment $2 >"$1/prometheus/wal/00000000"
  printf '\0\0%s\0\0' "$3" >"$1/prometheus/$FX_BLOCK/chunks/000001"
}
# shape_lines_of <string>: the lines of SHAPES whose expression matches in the string
shape_lines_of() {
  local line rec
  while IFS= read -r line; do
    rec="${line#*:}"
    [[ "$rec" =~ ^[[:space:]]*(#|$) || "$rec" == "except "* ]] && continue
    printf '%s' "$1" | LC_ALL=C grep -qE -e "$rec" && echo "${line%%:*}"
  done < <(grep -n '' "$SHAPES")
}
# pack_plants <scratch dir>: each plant, then the clean fixture, then obs view's two; dies at the first that does not
# hold. Prints one line a plant.
pack_plants() {
  local tmp="$1" n=0 p name where plant out rc file want ln got w clean hidden ec2
  local -a plants=()
  mkdir -p -- "$tmp/packs" || die "the pack plants: no scratch directory"
  clean="$(snappy_literal 'make obs-test record')"
  # The hidden plant: a literal of 6 bytes, an i, a p, a hyphen, a 1, a 0, a hyphen, then a copy of 8 bytes from 3
  # back (tag 11: a 1-byte offset, length 4 + 4), which decodes to ip- and four numbers, the form EC2 gives a host name,
  # made up. As written the record holds the literal and the copy's 2 bytes, which no shape matches.
  hidden="0e 14 $(hex_of "ip-1")$(hex_of "0-") 11 03"
  ec2="ip-10-$(printf '10-%.0s' 1 2)10"
  # name | where | the string; where is chunks (as written in the block's chunks file) or wal (only in the record)
  plants=("an IPv4 address of TEST-NET-1 (RFC 5737), as written|chunks|$(printf '%s.%s.%s.%s' 192 0 2 7)"
          "a loopback address 127.0.0.1's exception must not take, its last number 12, as written|chunks|$(printf '%s.%s' 127.0.0 12)"
          "an EC2-style host name only inside a snappy-compressed WAL record|wal|$ec2")
  host_words
  (( ${#HOST_CHECKED[@]} + ${#HOST_SKIPPED[@]} == 4 )) \
    || die "the pack plants: host_words gave $(( ${#HOST_CHECKED[@]} + ${#HOST_SKIPPED[@]} )) of this host's four kinds of word"
  for name in "${HOST_CHECKED[@]}"; do
    for w in "${!HOST_WORD[@]}"; do [[ "${HOST_WORD[$w]}" != "$name" ]] || plants+=("this host's $name, as written|host|$w"); done
  done
  for p in "${plants[@]}"; do
    IFS='|' read -r name where plant <<<"$p"
    rm -rf -- "$tmp/fx"
    case "$where" in
      wal) pack_fixture "$tmp/fx" "$hidden" "make obs-test chunks" || die "the pack plant '$name' could not be made"
           got="$(LC_ALL=C grep -a -r -l -E -f <(grep -vE '^[[:space:]]*(#|$)|^except ' "$SHAPES") -- "$tmp/fx/prometheus")"
           [[ -z "$got" ]] || die "the pack plant '$name': a shape matches its file as written, so it hides nothing"
           file="prometheus/wal/00000000 (decoded)" ;;
      *) pack_fixture "$tmp/fx" "$clean" "$plant" || die "the pack plant '$name' could not be made"
         file="prometheus/$FX_BLOCK/chunks/000001" ;;
    esac
    out="$(OBS_DIR="$tmp/fx" PACK="$tmp/packs/plant.tar.gz" OBS_MIN_FREE_GIB=0 _obs/obs.sh pack 2>&1)"; rc=$?
    got=""
    if [[ "$where" == host ]]; then
      want="$file: this host's ${HOST_WORD[$plant]},"; got="$(grep -F "$want" <<<"$out" | head -1)"
    else
      want="$file: $SHAPES:<a line whose expression matches>,"
      for ln in $(shape_lines_of "$plant"); do
        got="$(grep -F "$file: $SHAPES:$ln," <<<"$out" | head -1)"; [[ -z "$got" ]] || break
      done
    fi
    if (( rc == 2 )) && [[ -n "$got" && ! -e "$tmp/packs/plant.tar.gz" ]]; then
      n=$((n + 1)); say "planted in a pack, $name: exit 2: ${got#laug · obs pack: }"
    else
      echo "$out" >&2; die "planted in a pack, $name: exit $rc, not 2 with \"$want\""
    fi
  done
  (( ${#HOST_SKIPPED[@]} == 0 )) || say "not planted, this host's $(printf '%s; ' "${HOST_SKIPPED[@]}" | sed 's/; $//')"
  rm -rf -- "$tmp/fx"; pack_fixture "$tmp/fx" "$clean" "make obs-test chunks" || die "the clean pack fixture could not be made"
  out="$(OBS_DIR="$tmp/fx" PACK="$tmp/packs/clean.tar.gz" OBS_MIN_FREE_GIB=0 _obs/obs.sh pack 2>&1)"; rc=$?
  if (( rc != 0 )) || [[ ! -f "$tmp/packs/clean.tar.gz" ]] || ! grep -q 'decoded 1 record of 1 WAL segment.*: 0 hits$' <<<"$out"; then
    echo "$out" >&2; die "the clean pack fixture: exit $rc, not 0 with its record decoded and 0 hits"
  fi
  say "the clean pack fixture: exit 0, $(grep -o 'decoded 1 record of 1 WAL segment' <<<"$out"), 0 hits"
  # bin/obs view: a tarball with a symlink member; the clean pack with a byte added to its WAL segment, tarred again.
  # Each must exit 2 before anything is fetched or unpacked into OBS_DIR/view.
  mkdir -p -- "$tmp/link/prometheus" "$tmp/sum"
  printf '{"pack":1,"bytes":0}\n' >"$tmp/link/PACK.json"; : >"$tmp/link/SHA256SUMS"; ln -s ../PACK.json "$tmp/link/prometheus/x"
  tar -C "$tmp/link" --owner=0 --group=0 --numeric-owner -czf "$tmp/packs/link.tar.gz" PACK.json SHA256SUMS prometheus || die "the view plants could not be made"
  tar -xzf "$tmp/packs/clean.tar.gz" -C "$tmp/sum" && printf 'x' >>"$tmp/sum/prometheus/wal/00000000" \
    && tar -C "$tmp/sum" --sort=name --owner=0 --group=0 --numeric-owner -czf "$tmp/packs/sum.tar.gz" PACK.json SHA256SUMS prometheus \
    || die "the view plants could not be made"
  for p in "a tarball with a symlink member|link|PACK holds a member that is not a file or a directory" \
           "a pack with a byte added to a file|sum|a file of PACK does not match its SHA256SUMS"; do
    IFS='|' read -r name where want <<<"$p"
    out="$(OBS_DIR="$tmp/view-obs" PACK="$tmp/packs/$where.tar.gz" OBS_MIN_FREE_GIB=0 _obs/obs.sh view 2>&1)"; rc=$?
    if (( rc == 2 )) && grep -qF "$want" <<<"$out" && [[ ! -e "$tmp/view-obs/view" && -z "$(ls -A -- "$tmp/view-obs/dist")" ]]; then
      n=$((n + 1)); say "planted in a view, $name: exit 2: $(grep -F "$want" <<<"$out" | head -1 | sed 's/^laug · obs view: //')"
    else
      echo "$out" >&2; die "planted in a view, $name: exit $rc, not 2 with \"$want\""
    fi
  done
  say "$n pack and view plants, each refused with exit 2; the clean fixture packed"
}

# check: make obs-test, refused while a gate is open, and while anything listens on 7730, 7768 or 7769. The fetched
# binaries, never one on PATH, each from a tree that holds against versions.txt as it reads now; each run with env -i,
# so no OTEL_* or other variable of the shell reaches it. The static half: the configs' addresses, promtool check
# config, otelcol and jaeger validate, follow's -openmetrics through promtool, the lint over the boards and the fixture,
# then every plant, and the pack plants (pack_plants). Then the fake cell, about 12 minutes
# (_obs/follow/fakecell_test.go): a canned exposition on 127.0.0.1:7769, the fetched Prometheus on 7768 as the
# cell's, and the store under test on 7730 with _obs/prometheus.yml, then with track_timestamps_staleness false, the
# negative control. 7730 is the stack's store's port, so the stack's store is down while it runs, and obs-test says so
# first instead of failing 10 minutes in. Each test asks
# bin/obs follow -gate-running's question again before it starts the three, and every 2 s while they run: a gate that
# opens stops all three and fails the run (docs/DESIGN.md section 9: make obs-test refuses while a gate runs).
check() {
  local promtool otelcol jaeger missing=() tmp out rc n=0 p w name kind how copy want lock taken
  if lock="$(gate_open)"; then die "a gate runs: $lock; run it again once the gate has ended"; fi
  taken="$(listeners | awk '$1 == "tcp" && ($3 == 7730 || $3 == 7768 || $3 == 7769) { print $2 ":" $3 }' | sort -u | paste -sd' ')"
  [[ -z "$taken" ]] || die "$taken taken: the fake cell takes 127.0.0.1:7768 and 7769, and its store under test 127.0.0.1:7730, the stack's store's port; ${OBS:-bin/obs} down first"
  promtool="$(obs_bin prometheus promtool)"; otelcol="$(obs_bin otelcol otelcol)"; jaeger="$(obs_bin jaeger jaeger)"
  [[ -n "$promtool" ]] || missing+=(promtool); [[ -n "$otelcol" ]] || missing+=(otelcol); [[ -n "$jaeger" ]] || missing+=(jaeger)
  (( ${#missing[@]} == 0 )) || die "binaries missing, or their trees not checked against $VERSIONS as it reads now: ${missing[*]}; ${OBS:-bin/obs} up fetches and checks them into OBS_DIR"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/laug-obs-test.XXXXXX")" || die "no scratch directory"
  trap "rm -rf -- '$tmp'" EXIT
  run() { env -i PATH=/usr/bin:/bin HOME="$tmp" timeout 60 "$@"; }

  out="$(audit _obs)" || { echo "$out" >&2; die "the configs' addresses: $(wc -l <<<"$out") problem(s)"; }
  say "the configs' addresses: ${#CONFIGS[@]} files, each address 127.0.0.1 and a port of the table, no listener left to a default"
  render _obs "$tmp/conf" "$tmp/state"
  for p in prometheus.yml test/cell.yml; do
    out="$(run "$promtool" check config "$tmp/conf/$p" 2>&1)"; rc=$?
    (( rc == 0 )) || { sed "s#$tmp/conf/#_obs/#g; s#$tmp/state#OBS_DIR#g" <<<"$out" >&2; die "promtool check config _obs/$p: exit $rc"; }
    w="$(grep -c WARNING <<<"$out")"
    say "promtool check config _obs/$p: SUCCESS$( (( w > 0 )) && echo ", and $w warnings, each a target file follow writes at run time")"
  done
  # follow's -openmetrics output against this promtool: TestOpenMetricsPassesPromtool, which make test-go skips when
  # no promtool was fetched, run with the fetched one. A skip here is a failure.
  out="$(PROMTOOL="$(realpath -- "$promtool")" go -C _obs test ./follow -run '^TestOpenMetricsPassesPromtool$' -count=1 -v 2>&1)"; rc=$?
  if (( rc != 0 )) || ! grep -q '^--- PASS: TestOpenMetricsPassesPromtool ' <<<"$out"; then
    echo "$out" >&2
    die "follow's -openmetrics against promtool: exit $rc$(grep -q '^--- SKIP' <<<"$out" && echo ", the test skipped")"
  fi
  say "follow's -openmetrics, of a gate copy and of it with fake true: promtool check metrics passes, and tsdb create-blocks-from openmetrics makes $(grep -c 'BLOCK ULID' <<<"$out") blocks (TestOpenMetricsPassesPromtool)"
  out="$(run "$otelcol" validate --config="file:$tmp/conf/collector.yaml" 2>&1)"; rc=$?
  (( rc == 0 )) || { echo "$out" >&2; die "otelcol validate _obs/collector.yaml: exit $rc"; }
  say "otelcol validate _obs/collector.yaml: exit 0 ($(run "$otelcol" --version))"
  out="$(run "$jaeger" validate --config="file:$tmp/conf/jaeger.yaml" 2>&1)"; rc=$?
  (( rc == 0 )) || { echo "$out" >&2; die "jaeger validate _obs/jaeger.yaml: exit $rc"; }
  say "jaeger validate _obs/jaeger.yaml: exit 0 (jaeger $(grep -o 'git-version=v[0-9.]*' <<<"$out" | cut -d= -f2))"
  out="$(_obs/lint.sh 2>&1)" || { echo "$out" >&2; die "_obs/lint.sh, make lint's board rules, fails"; }
  say "the lint: ${out#laug · make lint: }"
  out="$(_obs/lint.sh _obs/test/board.json 2>&1)" || { echo "$out" >&2; die "the lint's fixture, _obs/test/board.json, breaks a rule"; }
  say "the lint over its fixture, _obs/test/board.json: ${out#laug · make lint: }"

  for p in "${PLANTS[@]}"; do
    IFS='|' read -r name kind how <<<"$p"
    rm -rf -- "$tmp/plant"; mkdir -p -- "$tmp/plant"
    case "$kind" in
      board|cell)
        if [[ "$kind" == board ]]; then copy="$tmp/plant/board.json"; jq "$how" _obs/test/board.json >"$copy"
        else copy="$tmp/plant/gate.json"; jq "$how" "$CELL_BOARD" >"$copy"; fi || die "plant '$name' could not be made"
        if [[ "$kind" == board ]]; then out="$(_obs/lint.sh "$copy" 2>&1)"; else out="$(_obs/lint.sh --cell-board "$copy" _obs/test/board.json 2>&1)"; fi; rc=$?
        want="${name%%:*}" ;;
      *)
        for c in "${CONFIGS[@]}"; do mkdir -p -- "$tmp/plant/$(dirname -- "$c")"; cp -- "_obs/$c" "$tmp/plant/$c"; done; sed -i -e "$how" "$tmp/plant/$kind"
        cmp -s "_obs/$kind" "$tmp/plant/$kind" && die "plant '$name' changed nothing in _obs/$kind"
        out="$(audit "$tmp/plant")"; rc=$?; want="_obs/$kind" ;;
    esac
    out="$(grep -v 'rule(s) broken' <<<"$out" | sed "s#$tmp/plant/#(copy) #g")"
    if (( rc == 2 )) && grep -qF "$want:" <<<"$out"; then
      n=$((n + 1)); say "planted, $name: exit 2: $(grep -F "$want:" <<<"$out" | head -1 | sed 's/^laug · make lint: //')"
    else
      echo "$out" >&2; die "planted, $name: exit $rc, not 2 with $want"
    fi
  done
  say "$n plants, each refused with exit 2"
  pack_plants "$tmp/pack"

  # The fake cell: two tests that make test-go skips, since OBS_BIN is unset there. A skip here is a failure.
  say "the fake cell, about 12 minutes: 127.0.0.1:7769 a canned exposition, 7768 Prometheus 2.53.0 as the cell's, 7730 the store under test (TestFakeCellInTheStore, TestFakeCellStalenessControl)"
  out="$(OBS_BIN="$OBS_DIR/bin" go -C _obs test ./follow -run '^(TestFakeCellInTheStore|TestFakeCellStalenessControl)$' -count=1 -v -timeout 30m 2>&1)"; rc=$?
  if (( rc != 0 )) || [[ "$(grep -c '^--- PASS: TestFakeCell' <<<"$out")" != 2 ]]; then
    echo "$out" >&2
    if grep -q 'a gate is open: \|a gate opened, ' <<<"$out"; then die "the fake cell: $(grep -o 'a gate is open: .*\|a gate opened, .*' <<<"$out" | head -1)"; fi
    die "the fake cell: exit $rc$(grep -q '^--- SKIP' <<<"$out" && echo ", a test skipped")"
  fi
  # The tests' own lines, their file and line cut off, and each test's result.
  grep -E '^(    fakecell_test\.go:[0-9]+: |        |--- PASS)' <<<"$out" | sed -E 's/^    fakecell_test\.go:[0-9]+: /  /; s/^        /    /'
  say "make obs-test holds: the static half and the fake cell, with its negative control"
}

# The lifecycle: bin/obs up, down, status and purge (_obs/cmd/obs/doc.go).
# The parts in start order; obs down stops them in reverse. TABLE is each part's listeners (SECURITY.md),
# every one on 127.0.0.1. cellstat and follow are this checkout's bin/obs, one copy, each run as its verb (OBS_VERB);
# the others are the fetched releases.
PARTS=(prometheus jaeger collector cellstat follow grafana)
declare -A TABLE=([prometheus]="7730" [jaeger]="7760 7761 7762 7763" [collector]="7751 7752 7753" [cellstat]="7710"
                  [follow]="7720" [grafana]="7740")
declare -A RELEASE=([prometheus]=prometheus [jaeger]=jaeger [collector]=otelcol [grafana]=grafana)   # versions.txt's name
declare -A BINARY=([prometheus]=prometheus [jaeger]=jaeger [collector]=otelcol [grafana]=grafana [cellstat]=obs [follow]=obs)
declare -A OBS_VERB=([cellstat]="usage record" [follow]="follow")   # bin/obs's verb that runs each
# What answers once a part is up: an HTTP 200 here, and each of its ports listening by its own process.
declare -A ANSWER=([prometheus]="http://127.0.0.1:7730/-/ready" [jaeger]="http://127.0.0.1:7761/api/v3/services"
                   [collector]="http://127.0.0.1:7753/metrics" [grafana]="http://127.0.0.1:7740/api/health"
                   [cellstat]="http://127.0.0.1:7710/metrics" [follow]="http://127.0.0.1:7720/metrics")
PROM_LISTEN=127.0.0.1:7730   # Prometheus takes its listener from the start line, not from prometheus.yml
CELLSTAT_LISTEN=127.0.0.1:7710 FOLLOW_LISTEN=127.0.0.1:7720 POOL=127.0.0.1:7700   # so do cellstat and follow
declare -A UP=() PID_PART=() PLANNED=()
STARTED=()

now() { echo "$EPOCHREALTIME"; }
dt() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.1f", b - a }'; }
this_boot() { local b; read -r b </proc/sys/kernel/random/boot_id && echo "$b"; }
curl_local() { curl -fsS --noproxy '*' -m "${2:-3}" -- "$1" 2>/dev/null; }

# proc_fields <pid>: "<state> <ppid> <utime> <stime> <start>", fields 3, 4, 14, 15 and 22 of /proc/<pid>/stat (start is
# the start time in clock ticks since boot, set at fork and kept through exec), or 1 when it has no entry.
proc_fields() {
  local st
  [[ "$1" =~ ^[1-9][0-9]*$ ]] || return 1
  { read -r st <"/proc/$1/stat"; } 2>/dev/null || return 1
  # shellcheck disable=SC2086  # the fields after the command name, split: field N is ${N-2}
  set -- ${st##*) }
  [[ "${20:-}" =~ ^[0-9]+$ ]] || return 1
  echo "$1 $2 ${12} ${13} ${20}"
}

# The records: OBS_DIR/run/<part>.pid, {pid, start, boot_id}, gate/lib.sh's shape, written after $!.
# rec_check <part>: 0 and the pid when the record names a process that runs now: this boot's, the record's start time,
# not a zombie, and the first word of its command line its part's binary under OBS_DIR/bin, then for cellstat and follow
# the part's bin/obs verb, one word or two. Otherwise 1 and why. A record from another boot is dead whatever runs under
# its pid now, and nothing is ever sent to it.
rec_check() {
  local part="$1" f p s b boot fields st now argv0 argv=() verb=()
  f="$OBS_DIR/run/$part.pid"
  [[ -f "$f" ]] || { echo "no record"; return 1; }
  read -r p s b < <(jq -er '"\(.pid) \(.start) \(.boot_id)"' "$f" 2>/dev/null) || { echo "a record that does not read"; return 1; }
  [[ "$p" =~ ^[1-9][0-9]*$ && "$s" =~ ^[0-9]+$ ]] || { echo "a record with no pid or no start"; return 1; }
  boot="$(this_boot)"
  [[ "$b" == "$boot" ]] || { echo "pid $p was recorded on boot $b, this is boot $boot: dead"; return 1; }
  fields="$(proc_fields "$p")" || { echo "pid $p has ended"; return 1; }
  read -r st _ _ _ now <<<"$fields"
  [[ "$now" == "$s" ]] || { echo "pid $p started at tick $now, recorded $s: another process"; return 1; }
  [[ "$st" != Z && "$st" != X ]] || { echo "pid $p has ended"; return 1; }
  { mapfile -d '' -t argv <"/proc/$p/cmdline"; } 2>/dev/null; argv0="${argv[0]:-}"
  [[ "$argv0" == "$OBS_DIR/bin/"*/"${BINARY[$part]:-none}" ]] \
    || { echo "pid $p runs ${argv0##*/}, not $part's binary under OBS_DIR/bin"; return 1; }
  read -ra verb <<<"${OBS_VERB[$part]:-}"
  [[ ${#verb[@]} == 0 || "${argv[*]:1:${#verb[@]}}" == "${verb[*]}" ]] \
    || { echo "pid $p runs obs ${argv[*]:1:${#verb[@]}}, not obs ${verb[*]}, $part's verb"; return 1; }
  echo "$p"
}
rec_write() { # rec_write <part> <pid>: the record, start null when the process has ended already
  local f s=null
  if f="$(proc_fields "$2")"; then s="${f##* }"; fi
  printf '{"pid":%d,"start":%s,"boot_id":"%s"}\n' "$2" "$s" "$(this_boot)" >"$OBS_DIR/run/$1.pid.new" \
    && mv -f -- "$OBS_DIR/run/$1.pid.new" "$OBS_DIR/run/$1.pid"
}
# The files of OBS_DIR/conf that each part reads at its start, and run/<part>.conf, which bin/obs up writes beside
# the part's record: {sum, commit}, a sha256 of those files as the part started with them, and conf/COMMIT then. So
# obs status says which commit's configs each part runs, and flags one that an obs up left alone while it copied other
# configs into conf/. Grafana also reads conf/boards, and again on its own: a board copied in under a Grafana left
# alone showed in its search within 7 s (seen on the dev host, cells/results/2026-10-17-obs-offline.txt
# section 2; its provider sets no updateIntervalSeconds; Grafana 11.1's provisioning docs give 10 in an example). So
# its boards are conf/'s whatever its record says.
# For cellstat and follow the file is obs/obs.sha256, the sha256 of the checkout's bin/obs that obs up copied, so a
# part left alone after a rebuild reads as running another build; follow also reads aa/ and catalog.json.
declare -A CONF_OF=([prometheus]=prometheus.yml [jaeger]=jaeger.yaml [collector]=collector.yaml [grafana]=grafana
                    [cellstat]=obs/obs.sha256 [follow]="obs/obs.sha256 aa catalog.json")
conf_sum() { # conf_sum <conf dir> <part>: one sha256, its first 16 hex, over the files of <conf dir> the part reads
  local p
  [[ -n "${CONF_OF[$2]:-}" ]] || return 1
  for p in ${CONF_OF[$2]}; do [[ -e "$1/$p" ]] || return 1; done
  # shellcheck disable=SC2086  # one argument per path
  ( cd -- "$1" && find ${CONF_OF[$2]} -type f -print0 | LC_ALL=C sort -z | xargs -0 sha256sum ) | sha256sum | cut -c1-16
}
conf_write() { # conf_write <part>: run/<part>.conf for the configs in PART_CONF (OBS_DIR/conf, or a view's) now
  jq -nc --arg s "$(conf_sum "$PART_CONF" "$1")" --arg c "$(cat "$PART_CONF/COMMIT" 2>/dev/null)" '{sum: $s, commit: $c}' \
    >"$OBS_DIR/run/$1.conf.new" && mv -f -- "$OBS_DIR/run/$1.conf.new" "$OBS_DIR/run/$1.conf"
}
conf_ran() { # conf_ran <part> <field>: sum or commit of the configs the part started with, or nothing when not recorded
  jq -r --arg f "$2" '.[$f] // empty' "$OBS_DIR/run/$1.conf" 2>/dev/null
}
load_up() { # UP and PID_PART: each part that runs now by its record
  local part p
  UP=(); PID_PART=()
  for part in "${PARTS[@]}"; do
    if p="$(rec_check "$part")"; then UP[$part]="$p"; PID_PART[$p]="$part"; fi
  done
}
part_of() { # part_of <pid>: the part whose recorded process is <pid> or one of its ancestors
  local p="$1" n=0 f
  while [[ "$p" =~ ^[1-9][0-9]*$ ]] && (( p > 1 && n < 64 )); do
    [[ -n "${PID_PART[$p]:-}" ]] && { echo "${PID_PART[$p]}"; return 0; }
    f="$(proc_fields "$p")" || return 1
    read -r _ p _ <<<"$f"; n=$((n + 1))
  done
  return 1
}
comm_of() { local c; c="$(cat "/proc/$1/comm" 2>/dev/null)"; echo "${c:-a process that has ended}"; }

# listeners: every TCP and UDP listener of this host, one a line: <tcp|udp> <address> <port> <pid ...>, or - for a
# socket whose process this user cannot see.
listeners() {
  local proto line addr pids
  for proto in t u; do
    while IFS= read -r line; do
      [[ -n "$line" ]] || continue
      read -r _ _ _ addr _ <<<"$line"
      pids="$(grep -oE 'pid=[0-9]+' <<<"$line" | cut -d= -f2 | sort -u | tr '\n' ' ')"
      echo "$([[ "$proto" == t ]] && echo tcp || echo udp) ${addr%:*} ${addr##*:} ${pids:--}"
    done < <(ss -"l${proto}npH" 2>/dev/null)
  done
}

# reserved <port>: why the port is not the stack's to take (SECURITY.md), or 1: a cell port of blocks 0
# to 15, the orchestrator's dev server, laugd, the SDK metrics listeners, a gate item's Omes, the suites' fake laugds.
reserved() {
  local p="$1" base
  for base in 7233 8080 9090 3000 8474; do
    if (( p >= base && p <= base + 150 && (p - base) % 10 == 0 )); then echo "block $(( (p - base) / 10 ))'s cell port ($base + $(( p - base )))"; return 0; fi
  done
  if (( p >= 7400 && p <= 7402 || p == 8400 )); then echo "the orchestrator's dev server (7400 to 7402, 8400)"; return 0; fi
  if (( p == 7700 )); then echo "laugd's"; return 0; fi
  if (( p >= 9091 && p <= 9093 )); then echo "an SDK metrics listener (9091 to 9093)"; return 0; fi
  if (( p >= 19100 && p <= 19131 )); then echo "a gate item's Omes (19100 to 19131)"; return 0; fi
  if (( p >= 17790 && p <= 17989 )); then echo "the suites' fake laugds (17790 to 17989)"; return 0; fi
  return 1
}

# planned <conf>: the listeners the configs under <conf> give each part, one a line: <part> <host:port>. Prometheus's
# is the start line's. A key that is not set prints null, which the checks refuse.
planned() {
  local c="$1" js
  echo "prometheus $PROM_LISTEN"
  echo "cellstat $CELLSTAT_LISTEN"
  echo "follow $FOLLOW_LISTEN"
  js='(.service.telemetry.metrics.readers // [null])[] | .pull.exporter.prometheus | if . == null then null else "\(.host):\(.port)" end'
  yq -o=json -I=0 . "$c/collector.yaml" 2>/dev/null \
    | jq -r '[.receivers.otlp.protocols.http.endpoint, .exporters.prometheus.endpoint, ('"$js"')][] | "collector \(.)"'
  yq -o=json -I=0 . "$c/jaeger.yaml" 2>/dev/null \
    | jq -r '[.receivers.otlp.protocols.http.endpoint, .extensions.jaeger_query.http.endpoint, .extensions.jaeger_query.grpc.endpoint, ('"$js"')][] | "jaeger \(.)"'
  awk '/^[[:space:]]*\[/ { s = $0; gsub(/[[:space:]]/, "", s) }
       s == "[server]" && /^[[:space:]]*http_addr[[:space:]]*=/ { sub(/^[^=]*=[[:space:]]*/, ""); a = $0 }
       s == "[server]" && /^[[:space:]]*http_port[[:space:]]*=/ { sub(/^[^=]*=[[:space:]]*/, ""); p = $0 }
       END { print "grafana " (a == "" ? "null" : a) ":" (p == "" ? "null" : p) }' "$c/grafana/grafana.ini"
}

# plan_check <conf> <part>...: each listener the configs under <conf> give the parts about to start, refused when it
# is not 127.0.0.1, not its part's port in the table, reserved, in this host's ephemeral range
# (/proc/sys/net/ipv4/ip_local_port_range, read now), or taken by a process that is not that part's. One line a
# problem on stdout, and 2.
plan_check() {
  local c="$1"; shift
  local want=" $* " part hp host port why lo hi n=0 proto addr lport pids pid owner all
  read -r lo hi </proc/sys/net/ipv4/ip_local_port_range
  all="$(listeners)"
  while read -r part hp; do
    [[ "$want" == *" $part "* ]] || continue
    host="${hp%:*}"; port="${hp##*:}"
    if [[ "$hp" == null || ! "$port" =~ ^[0-9]+$ ]]; then echo "$part: a listener with no address and port set, which binds its default"; n=$((n + 1)); continue; fi
    [[ "$host" == 127.0.0.1 ]] || { echo "$part: $hp is not on 127.0.0.1"; n=$((n + 1)); }
    [[ " ${TABLE[$part]} " == *" $port "* ]] || { echo "$part: port $port is not one of its ports in SECURITY.md's listener table (${TABLE[$part]})"; n=$((n + 1)); }
    if why="$(reserved "$port")"; then echo "$part: port $port is reserved: $why"; n=$((n + 1)); fi
    (( port < lo || port > hi )) || { echo "$part: port $port is in this host's ephemeral range, $lo to $hi"; n=$((n + 1)); }
    while read -r proto addr lport pids; do
      [[ "$lport" == "$port" ]] || continue
      for pid in $pids; do
        if [[ "$pid" == - ]]; then echo "$part: $proto $addr:$port is taken by a process this user cannot see"; n=$((n + 1))
        elif owner="$(part_of "$pid")"; then echo "$part: $proto $addr:$port is taken by the stack's $owner"; n=$((n + 1))
        else echo "$part: $proto $addr:$port is taken by $(comm_of "$pid"), pid $pid, not a part of the stack"; n=$((n + 1)); fi
      done
    done <<<"$all"
    PLANNED[$part]+="${PLANNED[$part]:+ }$hp"
  done < <(planned "$c")
  (( n == 0 )) || return 2
}

# audit_listeners: every listener of a process of the stack (a recorded part or a child of one) is TCP, on 127.0.0.1,
# and one of its part's ports in the table. One line a problem on stdout, and 2.
audit_listeners() {
  local proto addr port pids pid part n=0
  load_up
  while read -r proto addr port pids; do
    for pid in $pids; do
      [[ "$pid" != - ]] && part="$(part_of "$pid")" || continue
      if [[ "$proto" != tcp || "$addr" != 127.0.0.1 || " ${TABLE[$part]} " != *" $port "* ]]; then
        echo "$part: $proto $addr:$port is not one of its listeners in SECURITY.md's listener table (127.0.0.1, TCP ${TABLE[$part]})"; n=$((n + 1))
      fi
    done
  done < <(listeners)
  (( n == 0 )) || return 2
}
part_listeners() { # part_listeners <part>: its listeners now, "<address>:<port>" sorted, one line
  local proto addr port pids pid out=()
  while read -r proto addr port pids; do
    for pid in $pids; do
      [[ "$pid" != - && "$(part_of "$pid")" == "$1" ]] && out+=("$addr:$port$([[ "$proto" == udp ]] && echo /udp)")
    done
  done < <(listeners)
  (( ${#out[@]} )) && printf '%s\n' "${out[@]}" | sort -u -t: -k2n | tr '\n' ' ' | sed 's/ $//'
}

# answer <part> [<s>]: 0 once it answers: its URL in ANSWER gives 200 and each port the configs plan for it listens by
# its own process. 1 when its process ended, 3 when it was silent for <s> seconds, 60 unless given. Until the start
# line has reached its exec, the process runs env, setsid or nice, so its record does not hold yet; that is waited for,
# not taken as an end.
answer() {
  local part="$1" wait="${2:-60}" t0="$SECONDS" p f st hp have ok
  p="$(jq -r .pid "$OBS_DIR/run/$part.pid" 2>/dev/null)"
  while (( SECONDS - t0 < wait )); do
    f="$(proc_fields "$p")" || return 1
    read -r st _ <<<"$f"; [[ "$st" != Z && "$st" != X ]] || return 1
    if rec_check "$part" >/dev/null; then
      PID_PART[$p]="$part"
      if curl_local "${ANSWER[$part]}" 2 >/dev/null; then
        have=" $(part_listeners "$part") "; ok=1
        for hp in ${PLANNED[$part]:-}; do [[ "$have" == *" $hp "* ]] || ok=0; done
        (( ok )) && return 0
      fi
    fi
    sleep 0.25
  done
  return 3
}

# start_part <part>: its start line, then its record. setsid gives it a session of its own, so no signal to a gate's or a
# shell's group reaches it; env --default-signal undoes the SIGINT and SIGQUIT a background job inherits ignored; env
# -i keeps the shell's environment out (no OTEL_*, GF_* or LEASE_FILE); nice -n 10 and GOMAXPROCS=2 keep a burst under
# the cells. It runs in OBS_DIR with no descriptor of this script open but its log and /dev/null: the lock on fd 9 and
# whatever make or a shell passed down are closed first.
start_part() {
  local part="$1" bin pid flags=()
  if [[ -n "${OBS_VERB[$part]:-}" ]]; then bin="$(own_bin)" || die "$part: $bin; nothing started for it"
  else bin="$(obs_bin "${RELEASE[$part]}" "${BINARY[$part]}")"; fi
  [[ -n "$bin" ]] || die "$part: no binary in a tree checked against $VERSIONS as it reads now; nothing started for it"
  case "$part" in
    cellstat) flags=(usage record -out none -listen "$CELLSTAT_LISTEN" -interval 5s -for 0 -pids "$OBS_DIR/run") ;;
    follow) flags=(follow -listen "$FOLLOW_LISTEN" -pool "$POOL" -dir "$OBS_DIR") ;;
    prometheus) flags=(--config.file="$PART_CONF/prometheus.yml" --web.listen-address="$PROM_LISTEN"
                       --storage.tsdb.path="$PART_TSDB" --storage.tsdb.retention.time=90d
                       --storage.tsdb.retention.size=20GB) ;;
    jaeger) flags=(--config="$PART_CONF/jaeger.yaml") ;;
    collector) flags=(--config="$PART_CONF/collector.yaml") ;;
    grafana) flags=(server --homepath "$(dirname -- "$(dirname -- "$bin")")" --config "$PART_CONF/grafana/grafana.ini") ;;
  esac
  [[ ! -f "$PART_LOGS/$part.log" ]] || mv -f -- "$PART_LOGS/$part.log" "$PART_LOGS/$part.log.1"
  ( for fd in /proc/"$BASHPID"/fd/*; do fd="${fd##*/}"; (( fd > 2 )) && eval "exec $fd>&-"; done 2>/dev/null
    cd -- "$OBS_DIR" || exit 1
    exec env -i PATH=/usr/bin:/bin HOME="$HOME" GOMAXPROCS=2 env --default-signal \
      setsid nice -n 10 "$bin" "${flags[@]}" >"$PART_LOGS/$part.log" 2>&1 </dev/null ) &
  pid=$!
  rec_write "$part" "$pid"
  conf_write "$part"
  STARTED+=("$part")
}

# own_bin: the path of the checkout's bin/obs as copied into OBS_DIR/bin/obs-<its sha256, 12 hex>/obs, the copy
# made now when it is not there; the sum is the one stage_conf wrote into conf/obs/obs.sha256, so cellstat and follow
# run the build their record's configs name, one copy for both. A copy is written to a dot name and renamed. On
# failure, why, and 1.
own_bin() {
  local part=obs src=bin/obs sum dir got
  sum="$(cat "$OBS_DIR/conf/obs/obs.sha256" 2>/dev/null)"
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || { echo "no build sum in OBS_DIR/conf/obs"; return 1; }
  [[ -x "$src" && "$(sha256sum -- "$src" | cut -d' ' -f1)" == "$sum" ]] || { echo "$src is not the build ${OBS:-bin/obs} up staged; ${OBS:-bin/obs} up stages this one"; return 1; }
  dir="$OBS_DIR/bin/obs-${sum:0:12}"
  if [[ ! -x "$dir/$part" ]] || [[ "$(sha256sum -- "$dir/$part" | cut -d' ' -f1)" != "$sum" ]]; then
    mkdir -p -- "$dir" && cp -- "$src" "$dir/.$part.new" && chmod 755 -- "$dir/.$part.new" && mv -f -- "$dir/.$part.new" "$dir/$part" \
      || { echo "cannot copy $src into OBS_DIR/bin"; return 1; }
  fi
  got="$(sha256sum -- "$dir/$part" | cut -d' ' -f1)"
  [[ "$got" == "$sum" ]] || { echo "the copy in OBS_DIR/bin has sha256 $got, not $sum"; return 1; }
  echo "$dir/$part"
}

# stop_part <part>: TERM to its recorded process's group (setsid made it a group leader), else to the pid, up to 30 s,
# then KILL. A record that does not hold (rec_check) is removed and nothing is sent. Prints what it did.
stop_part() {
  local part="$1" p i t0
  if ! p="$(rec_check "$part")"; then
    if [[ -f "$OBS_DIR/run/$part.pid" ]]; then say "$part: $p; nothing sent; record removed"; rm -f -- "$OBS_DIR/run/$part.pid" "$OBS_DIR/run/$part.conf"; fi
    return 0
  fi
  t0="$(now)"
  kill -TERM -- "-$p" 2>/dev/null || kill -TERM "$p" 2>/dev/null
  for (( i = 0; i < 300; i++ )); do rec_check "$part" >/dev/null || break; sleep 0.1; done
  if rec_check "$part" >/dev/null; then
    kill -KILL -- "-$p" 2>/dev/null || kill -KILL "$p" 2>/dev/null
    for (( i = 0; i < 50; i++ )); do rec_check "$part" >/dev/null || break; sleep 0.1; done
    say "$part: pid $p did not stop in 30 s after TERM; KILL sent"
  else
    say "$part: pid $p stopped, $(dt "$t0" "$(now)") s after TERM"
  fi
  rec_check "$part" >/dev/null || rm -f -- "$OBS_DIR/run/$part.pid" "$OBS_DIR/run/$part.conf"
}
stop_parts() { local i; for (( i = $# ; i >= 1; i-- )); do stop_part "${!i}"; done; }

# batch_cellstat <pid>: true when <pid> is a cellstat that is not the stack's: bin/obs usage record -listen
# 127.0.0.1:7710, a batch's one cellstat, run from a checkout, whose port bin/obs up -no-cellstat leaves to it.
batch_cellstat() {
  local a=""
  { IFS= read -r -d '' a <"/proc/$1/cmdline"; } 2>/dev/null
  [[ "$(comm_of "$1")" == cellstat && "$a" != "$OBS_DIR/bin/"* ]]
}

# batch_pid: the pid of a batch's cellstat on 7710, when one listens there.
batch_pid() {
  local proto addr lport pids
  while read -r proto addr lport pids; do
    [[ "$proto" == tcp && "$lport" == 7710 && "$pids" != - ]] && batch_cellstat "${pids%% *}" && { echo "${pids%% *}"; return 0; }
  done < <(listeners)
  return 1
}

# ports_left [<part>...]: each port of the table of these parts (all six when none is named) that something listens on;
# 7710 held by a batch's cellstat is not the stack's, and is left out.
ports_left() {
  local parts=("$@") part port proto addr lport pids out=() all
  (( ${#parts[@]} )) || parts=("${PARTS[@]}")
  all="$(listeners)"
  for part in "${parts[@]}"; do
    for port in ${TABLE[$part]}; do
      while read -r proto addr lport pids; do
        [[ "$lport" == "$port" ]] || continue
        [[ "$part" == cellstat && "$pids" != - ]] && batch_cellstat "${pids%% *}" && continue
        out+=("$proto $addr:$port ($( [[ "$pids" == - ]] && echo "a process this user cannot see" || comm_of "${pids%% *}"))")
      done <<<"$all"
    done
  done
  (( ${#out[@]} )) && printf '%s\n' "${out[@]}"
}

obs_lock() { # obs_lock: one bin/obs verb at a time on OBS_DIR; fd 9, which no part inherits
  exec 9>>"$OBS_DIR/.obs.lock" || die "cannot open OBS_DIR/.obs.lock"
  flock -n 9 || die "another bin/obs up, down or purge runs on this OBS_DIR"
}
check_vars() { # the lifecycle's knobs, which bin/obs sets from its flags, each refused unless it is one of its values
  [[ -z "${ONLY:-}" || ( "$ONLY" =~ ^[a-z]+$ && " ${PARTS[*]} " == *" $ONLY "* ) ]] || die "ONLY must be one of ${PARTS[*]}; got '${ONLY}'"
  [[ "${FORCE:-}" =~ ^[01]?$ ]] || die "FORCE must be 1, 0 or empty; got '${FORCE}'"
  [[ "${JSON:-}" =~ ^[01]?$ ]] || die "JSON must be 1, 0 or empty; got '${JSON}'"
  [[ "${OBS_CELLSTAT:-}" =~ ^[01]?$ ]] || die "OBS_CELLSTAT must be 1, 0 or empty; got '${OBS_CELLSTAT}'"
}
selected() { local part; for part in "${PARTS[@]}"; do [[ -z "${ONLY:-}" || "$part" == "$ONLY" ]] && echo "$part"; done; }

# stage_conf: the configs (render), boards and A/A lists into OBS_DIR/conf.new as this checkout holds them, and
# COMMIT, the commit they came from.
stage_conf() {
  local new="$OBS_DIR/conf.new" dirty="" part
  rm -rf -- "$new"; mkdir -p -- "$new/boards" "$new/aa"
  render _obs "$new" "$OBS_DIR"
  mkdir -p -- "$new/grafana/provisioning/plugins" "$new/grafana/provisioning/alerting"   # read at start; empty, not missing
  if [[ -d _obs/stack ]]; then find _obs/stack -maxdepth 1 -type f -name '*.json' -exec cp -- {} "$new/boards/" \; ; fi
  find _obs/aa -maxdepth 1 -type f -name '*.txt' -exec cp -- {} "$new/aa/" \;
  # follow reads each fault's recovery budget from the catalog, as JSON.
  yq -o=json '.' faults/catalog.yaml >"$new/catalog.json" || die "faults/catalog.yaml does not read as YAML"
  mkdir -p -- "$new/obs"
  [[ -x bin/obs ]] || die "bin/obs is not built; make build builds it"
  sha256sum -- bin/obs | cut -d' ' -f1 >"$new/obs/obs.sha256"
  [[ -z "$(git status --porcelain -- _obs internal faults/catalog.yaml 2>/dev/null)" ]] \
    || dirty=", with _obs/, internal/ or faults/catalog.yaml changed since"
  echo "$(git rev-parse --short HEAD 2>/dev/null || echo unknown)$dirty" >"$new/COMMIT"
}

# settle: once the store is ready, until prometheus_tsdb_compactions_total has not moved for 2 minutes and
# prometheus_tsdb_head_min_time is within 3 h of now, read from its /metrics every 5 s; 20 minutes at most. A head
# compacts once it spans more than 1.5 times the 2 h block range (Prometheus 2.53.0 tsdb/head.go), so after a
# stop of days the first compaction comes soon after start, and a gate started after obs up returns never meets it.
settle() {
  local t0 since last="" m c h head_ok
  t0="$(now)"; since="$t0"
  while :; do
    m="$(curl_local http://127.0.0.1:7730/metrics)"
    c="$(awk '$1 == "prometheus_tsdb_compactions_total" { print $2 }' <<<"$m")"
    h="$(awk '$1 == "prometheus_tsdb_head_min_time" { print $2 }' <<<"$m")"
    if [[ -n "$c" && "$c" != "$last" ]]; then last="$c"; since="$(now)"; fi
    head_ok=0
    if [[ -n "$h" ]] && awk -v h="$h" -v n="$(date +%s%3N)" 'BEGIN { exit !(h <= n + 60000 && n - h <= 10800000) }'; then head_ok=1; fi
    if [[ -n "$c" ]] && (( head_ok )) && awk -v a="$since" -v b="$(now)" 'BEGIN { exit !(b - a >= 120) }'; then break; fi
    if awk -v a="$t0" -v b="$(now)" 'BEGIN { exit !(b - a >= 1200) }'; then
      SETTLE="not settled after 20 min (compactions_total ${c:-unread}, head_min_time ${h:-unread}); the stack is left up"; return 1
    fi
    sleep 5
  done
  SETTLE="settled $(dt "$t0" "$(now)") s after ready: compactions_total at $c for 2 min, the head's first sample at $(date -u -d "@$(awk -v h="$h" 'BEGIN { printf "%d", h / 1000 }')" +%H:%M:%S) UTC"
}

rss_mib() { awk '/^VmRSS:/ { printf "%.1f", $2 / 1024 }' "/proc/$1/status" 2>/dev/null; }

up() {
  local part p f problems lock todo=() alone=() t rc
  local -A answered=()
  check_vars
  if lock="$(gate_open)"; then die "a gate runs: $lock; run it again once the gate has ended"; fi
  [[ "${ONLY:-}" != cellstat || "${OBS_CELLSTAT:-}" != 0 ]] || die "ONLY=cellstat and OBS_CELLSTAT=0 start nothing"
  obs_dir_ready
  obs_lock
  if p="$(view_up)"; then die "bin/obs view runs here, a view of $p; ${OBS:-bin/obs} down first"; fi
  rm -f -- "$OBS_DIR/run/view.json"
  fetch
  load_up
  for part in $(selected); do
    if [[ "$part" == cellstat && "${OBS_CELLSTAT:-}" == 0 && -z "${UP[$part]:-}" ]]; then continue   # the batch's one cellstat serves 7710
    elif [[ -n "${UP[$part]:-}" ]]; then alone+=("$part")
    else todo+=("$part"); fi
  done
  stage_conf
  problems="$(audit _obs)"
  # Not in a $(...): plan_check fills PLANNED, which answer reads.
  plan_check "$OBS_DIR/conf.new" "${todo[@]}" >"$OBS_DIR/conf.new/.plan" || problems+="${problems:+$'\n'}$(cat "$OBS_DIR/conf.new/.plan")"
  rm -f -- "$OBS_DIR/conf.new/.plan"
  if [[ -n "$problems" ]]; then
    rm -rf -- "$OBS_DIR/conf.new"; echo "$problems" >&2
    die "$(grep -c . <<<"$problems") problem(s) with the listeners the stack would open; nothing started"
  fi
  for part in "${alone[@]}"; do
    f="$(conf_ran "$part" sum)"
    if [[ -z "$f" ]]; then
      say "$part: up by its record, pid ${UP[$part]}, left alone; which configs it started with is not recorded"
    elif [[ "$f" != "$(conf_sum "$OBS_DIR/conf.new" "$part")" ]]; then
      say "$part: up by its record, pid ${UP[$part]}, left alone; it runs the configs it started with, from $(conf_ran "$part" commit), not this checkout's, until ${OBS:-bin/obs} down -only $part and ${OBS:-bin/obs} up$([[ "$part" == grafana ]] && echo " (its boards, which it reads again on its own, are this checkout's)")"
    else
      say "$part: up by its record, pid ${UP[$part]}; left alone"
    fi
  done
  rm -rf -- "$OBS_DIR/conf.old"; [[ ! -d "$OBS_DIR/conf" ]] || mv -- "$OBS_DIR/conf" "$OBS_DIR/conf.old"
  mv -- "$OBS_DIR/conf.new" "$OBS_DIR/conf" && rm -rf -- "$OBS_DIR/conf.old"
  mkdir -p -- "$OBS_DIR"/{prometheus,jaeger/keys,jaeger/values,grafana/plugins,log,run,targets} || die "cannot make OBS_DIR's directories"
  if [[ ! -s "$OBS_DIR/grafana/admin-password" ]]; then
    ( umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=\n' >"$OBS_DIR/grafana/admin-password" ) || die "cannot write Grafana's admin password"
  fi
  chmod 600 -- "$OBS_DIR/grafana/admin-password"
  [[ " ${todo[*]} ${alone[*]} " == *" cellstat "* || " $(selected | tr '\n' ' ') " != *" cellstat "* ]] \
    || say "cellstat: -no-cellstat, not started: the batch's bin/obs usage record -listen $CELLSTAT_LISTEN -pids $OBS_DIR/run serves the store"

  for part in "${todo[@]}"; do
    t="$(now)"
    start_part "$part"
    answer "$part"; rc=$?
    if (( rc != 0 )); then
      echo "laug · $VERB: $part $([[ $rc == 1 ]] && echo "ended" || echo "was silent for 60 s") after its start; the last lines of OBS_DIR/log/$part.log:" >&2
      tail -n 5 -- "$OBS_DIR/log/$part.log" >&2
      stop_parts "${STARTED[@]}"
      die "$part did not answer; what this run started is stopped"
    fi
    answered[$part]="$(dt "$t" "$(now)")"
  done
  if ! problems="$(audit_listeners)"; then
    echo "$problems" >&2; load_up
    stop_parts "${PARTS[@]}"
    die "a part listens where SECURITY.md's listener table does not put it; the stack is stopped"
  fi
  load_up
  if [[ " ${STARTED[*]} " == *" prometheus "* ]]; then settle || { say "$SETTLE"; exit 1; }; load_up
  elif [[ -n "${UP[prometheus]:-}" ]]; then SETTLE="up by its record and left alone, so no settle wait"
  else SETTLE="down, not started$([[ -n "${ONLY:-}" ]] && echo " (ONLY=$ONLY)")"; fi
  echo "laug · obs up"
  for part in "${PARTS[@]}"; do
    if [[ -z "${UP[$part]:-}" && "$part" == cellstat && "${OBS_CELLSTAT:-}" == 0 ]]; then printf '  %-11s OBS_CELLSTAT=0, not started\n' "$part"; continue; fi
    if [[ -z "${UP[$part]:-}" ]]; then printf '  %-11s down\n' "$part"; continue; fi
    p="${UP[$part]}"
    printf '  %-11s pid %-8s %-60s RSS %6s MiB  %s\n' "$part" "$p" "$(part_listeners "$part")" "$(rss_mib "$p")" \
      "$([[ -n "${answered[$part]:-}" ]] && echo "answered ${answered[$part]} s after its start" || echo "left alone")"
  done
  [[ -z "${answered[prometheus]:-}" ]] || echo "  the store: ready ${answered[prometheus]} s after its start; $SETTLE"
  [[ -n "${answered[prometheus]:-}" ]] || echo "  the store: $SETTLE"
  f=""
  for part in "${PARTS[@]}"; do
    [[ -n "${UP[$part]:-}" && "$(conf_ran "$part" sum)" != "$(conf_sum "$OBS_DIR/conf" "$part")" ]] || continue
    f+="; $part runs those it started with, from $(conf_ran "$part" commit | sed 's/^$/a commit not recorded/')"
  done
  echo "  configs: OBS_DIR/conf, from $(cat "$OBS_DIR/conf/COMMIT")$f"
}

down() { # down [<part>...]: the parts of the stack stopped in reverse order, data kept; then every port of theirs free
  local parts=("$@") left p
  (( ${#parts[@]} )) || mapfile -t parts < <(selected)
  local view=""; [[ ! -f "$OBS_DIR/run/view.json" ]] || view="$(jq -r '.pack // "a pack"' "$OBS_DIR/run/view.json" 2>/dev/null)"
  if [[ -d "$OBS_DIR/run" ]]; then stop_parts "${parts[@]}"; else say "OBS_DIR holds no records: nothing to stop"; fi
  if [[ -n "$view" ]] && ! view_up >/dev/null; then
    rm -f -- "$OBS_DIR/run/view.json"
    say "the view of ${view:-a pack} stopped; its copy stays in OBS_DIR/view until the next ${OBS:-bin/obs} view or ${OBS:-bin/obs} purge -remove-state"
  fi
  if left="$(ports_left "${parts[@]}")"; then
    echo "$left" | sed "s/^/laug · $VERB: still taken: /" >&2; return 1
  fi
  local b=""; if p="$(batch_pid)"; then b=", $CELLSTAT_LISTEN aside: a batch's bin/obs usage record, pid $p, holds it"; fi
  say "down: each port of the table free$([[ -n "${ONLY:-}" ]] && echo " for $ONLY")$b$([[ "$VERB" == "obs down" ]] && echo "; data kept in OBS_DIR")"
}
down_cmd() {
  local lock
  check_vars
  if [[ "${FORCE:-}" != 1 ]] && lock="$(gate_open)"; then die "a gate runs: $lock; -while-gate-runs stops the stack anyway"; fi
  [[ "$OBS_DIR" == /* ]] || die "OBS_DIR must be an absolute path"
  if [[ -d "$OBS_DIR" ]]; then obs_lock; fi
  down || exit 1
}

# status: each part and the store, as text or, with JSON=1, one JSON object. Exit 1 when a part that is built is down.
status() {
  local part p f st ut stt start tck rows=() store="null" downs=0 commit="" q cells series sps oldest disk free
  local targets=0 stale=0 now_s m last last_id last_at view="" confdir="$OBS_DIR/conf" ddir="$OBS_DIR" vstarted=""
  check_vars
  # Up since is its record's mtime, the wall clock when the start line ran. A start in ticks since boot plus btime, or
  # now less the ticks since, is off by every step of the wall clock since: 13 to 20 s late on the dev host, whose wall
  # clock gained 25 s on /proc/uptime in the 278 s after one start (seen in
  # cells/results/2026-10-17-obs-offline.txt section 2).
  tck="$(getconf CLK_TCK)"; now_s="$(date +%s)"
  [[ -d "$OBS_DIR" ]] || { [[ "${JSON:-}" == 1 ]] && echo '{"obs_dir":false}' || say "no OBS_DIR: the stack was never up here, or was purged"; exit 1; }
  load_up
  # A view (bin/obs view) runs the store and Grafana alone, on its own configs and its own copy of a pack's state in
  # OBS_DIR/view, so the disk read is the view's; the other four are not part of it.
  if view="$(view_up)"; then
    confdir="$OBS_DIR/view/conf"; ddir="$OBS_DIR/view"; vstarted="$(jq -r '.started // empty' "$OBS_DIR/run/view.json" 2>/dev/null)"
  else view=""; fi
  for part in "${PARTS[@]}"; do
    if [[ -n "$view" && -z "${UP[$part]:-}" && "$part" != prometheus && "$part" != grafana ]]; then
      rows+=("$(jq -nc --arg p "$part" --arg w "not part of bin/obs view's view of $view" '{part: $p, built: true, up: false, batch: true, why: $w}')"); continue
    fi
    if [[ -z "${UP[$part]:-}" && "$part" == cellstat ]] && p="$(batch_pid)"; then
      rows+=("$(jq -nc --arg p "$part" --arg w "$CELLSTAT_LISTEN is a batch's bin/obs usage record, pid $p, not the stack's (${OBS:-bin/obs} up -no-cellstat)" \
        '{part: $p, built: true, up: false, batch: true, why: $w}')"); continue
    fi
    if [[ -z "${UP[$part]:-}" ]]; then
      downs=$((downs + 1))
      rows+=("$(jq -nc --arg p "$part" --arg w "$(rec_check "$part")" '{part: $p, built: true, up: false, why: $w}')"); continue
    fi
    p="${UP[$part]}"; f="$(proc_fields "$p")"; read -r st _ ut stt start <<<"$f"
    rows+=("$(jq -nc --arg p "$part" --argjson pid "$p" \
      --argjson since "$(stat -c %Y -- "$OBS_DIR/run/$part.pid")" \
      --argjson cpu "$(awk -v a="$ut" -v b="$stt" -v t="$tck" 'BEGIN { printf "%.2f", (a + b) / t }')" \
      --argjson rss "$(awk '/^VmRSS:/ { print $2 * 1024 }' "/proc/$p/status")" \
      --argjson peak "$(awk '/^VmHWM:/ { print $2 * 1024 }' "/proc/$p/status")" \
      --argjson rb "$(awk '$1 == "read_bytes:" { print $2 }' "/proc/$p/io" 2>/dev/null || echo null)" \
      --argjson wb "$(awk '$1 == "write_bytes:" { print $2 }' "/proc/$p/io" 2>/dev/null || echo null)" \
      --arg l "$(part_listeners "$part")" \
      --arg cs "$(conf_ran "$part" sum)" --arg cc "$(conf_ran "$part" commit)" --arg cn "$(conf_sum "$confdir" "$part")" \
      '{part: $p, built: true, up: true, pid: $pid, since: $since, cpu_s: $cpu, rss_bytes: $rss, rss_peak_bytes: $peak,
        read_bytes: $rb, write_bytes: $wb, listeners: ($l | split(" ") | map(select(. != ""))),
        config: (if $cs == "" then null else {commit: $cc, same_as_conf: ($cs == $cn)} end)}')")
  done
  if [[ -n "${UP[prometheus]:-}" ]]; then
    q() { curl -fsS --noproxy '*' -m 5 -G http://127.0.0.1:7730/api/v1/query --data-urlencode "query=$1" 2>/dev/null \
            | jq -r '.data.result[0].value[1] // "null"' 2>/dev/null || echo null; }
    m="$(curl_local http://127.0.0.1:7730/metrics)"
    cells="$(q 'count(up{job="laug-cells"} == 1) or vector(0)')"
    series="$(awk '$1 == "prometheus_tsdb_head_series" { print $2 }' <<<"$m")"
    sps="$(q 'sum(rate(prometheus_tsdb_head_samples_appended_total{job="laug-stack"}[2m]))')"
    [[ -z "$view" ]] || sps=0   # a view scrapes nothing; the rate over 2 m is the stopped stack's, from before its pack
    oldest="$(awk '$1 == "prometheus_tsdb_lowest_timestamp" { printf "%d", $2 / 1000 }' <<<"$m")"
    store="$(jq -nc --argjson c "${cells:-null}" --argjson s "${series:-null}" --argjson r "$( [[ "$sps" =~ ^[0-9.e+-]+$ ]] && echo "$sps" || echo null)" \
      --argjson o "${oldest:-null}" '{cells_federated: $c, head_series: $s, samples_per_s: $r, oldest_sample: $o}')"
  fi
  disk="$(for f in prometheus jaeger grafana; do printf '%s %s\n' "$f" "$(du -sk -- "$ddir/$f" 2>/dev/null | cut -f1)"; done \
          | jq -Rn '[inputs | split(" ") | {(.[0]): ((.[1] | tonumber? // 0) * 1024)}] | add')"
  free="$(df -Pk -- "$OBS_DIR" | awk 'NR == 2 { print $4 * 1024 }')"
  if [[ -d "$OBS_DIR/targets" ]]; then
    targets="$(find "$OBS_DIR/targets" -maxdepth 1 -type f -name '*.json' ! -name '.*' | wc -l)"
    [[ ! -f "$OBS_DIR/targets/.expires.json" ]] || stale="$(jq --argjson n "$now_s" '[.[] | select(. < $n)] | length' "$OBS_DIR/targets/.expires.json" 2>/dev/null || echo 0)"
  fi
  [[ ! -f "$OBS_DIR/conf/COMMIT" ]] || commit="$(cat "$OBS_DIR/conf/COMMIT")"
  # The last gate follow found: the newest of OBS_DIR/follow/<gate id>.json, by its id and when follow found it; the
  # path inside is never shown.
  last="$(find "$OBS_DIR/follow" -maxdepth 1 -type f -name 'g*.json' -printf '%T@ %f\n' 2>/dev/null | sort -n | tail -1)"
  last_id=""; last_at=""
  if [[ -n "$last" ]]; then last_id="${last#* }"; last_id="${last_id%.json}"; last_at="${last%% *}"; last_at="${last_at%.*}"; fi
  if [[ "${JSON:-}" == 1 ]]; then
    printf '%s\n' "${rows[@]}" | jq -s --argjson store "$store" --argjson disk "$disk" --argjson free "$free" --argjson t "$targets" \
      --argjson stale "$stale" --arg commit "$commit" --arg lg "$last_id" --arg la "$last_at" --arg v "$view" --arg vs "$vstarted" \
      '{parts: ., store: $store, disk_bytes: $disk, disk_of: (if $v == "" then "stack" else "view" end), free_bytes: $free,
        target_files: $t, target_files_past_expiry: $stale,
        configs: $commit, last_gate: (if $lg == "" then null else {gate_id: $lg, found: ($la | tonumber)} end),
        view: (if $v == "" then null else {pack: $v, started: (if $vs == "" then null else $vs end)} end)}'
  else
    echo "laug · obs status, $(date -u +%H:%M:%S) UTC"
    [[ -z "$view" ]] || echo "  view        bin/obs view's view of $view$([[ -n "$vstarted" ]] || echo ", whose bin/obs view ended before both parts were up and checked"): the store and Grafana alone, no scrape job; ${OBS:-bin/obs} down stops it"
    printf '%s\n' "${rows[@]}" | jq -r --arg obs "${OBS:-bin/obs}" '
      def mib: if . == null then "?" else (. / 1048576 * 10 | round / 10 | tostring) end;
      if .batch == true then "  \(.part | . + " " * (11 - length)) \(.why)"
      elif .up == false then "  \(.part | . + " " * (11 - length)) down: \(.why)"
      else "  \(.part | . + " " * (11 - length)) up since \(.since | strftime("%H:%M:%S")) UTC, pid \(.pid), CPU \(.cpu_s) s, RSS \(.rss_bytes | mib) MiB (peak \(.rss_peak_bytes | mib)), read \(.read_bytes | mib) MiB, written \(.write_bytes | mib) MiB; \(.listeners | join(" ")); configs \(if .config == null then "not recorded" elif .config.same_as_conf then "from \(.config.commit)" else "from \(.config.commit), not OBS_DIR/conf'"'"'s: \($obs) down -only \(.part), then \($obs) up" end)" end'
    jq -rn --argjson s "$store" --argjson d "$disk" --argjson f "$free" --argjson t "$targets" --argjson x "$stale" --arg c "$commit" \
      --arg lg "$last_id" --arg la "$last_at" --arg dl "$([[ -n "$view" ]] && echo "disk, the view's copy in OBS_DIR/view" || echo disk)" '
      def mib: (. / 1048576 * 10 | round / 10 | tostring);
      (if $s == null then "  the store: down"
       else "  the store: \($s.cells_federated) cells federated now, \($s.head_series) head series, \($s.samples_per_s // "no rate yet" | if type == "number" then (. * 10 | round / 10 | tostring) else . end) samples a second, oldest sample \(if $s.oldest_sample == null then "none" else ($s.oldest_sample | strftime("%Y-%m-%d %H:%M:%S")) + " UTC" end)" end),
      "  \($dl): store \($d.prometheus | mib) MiB, traces \($d.jaeger | mib) MiB, grafana \($d.grafana | mib) MiB; \($f / 1073741824 | floor) GiB free on OBS_DIR'"'"'s filesystem",
      "  targets: \($t) target files, \($x) past their lease'"'"'s expiry; OBS_DIR/conf copied from \(if $c == "" then "no commit yet" else $c end); the last gate follow found: \(if $lg == "" then "none" else "\($lg), at \($la | tonumber | strftime("%Y-%m-%d %H:%M:%S")) UTC" end)"'
  fi
  (( downs == 0 )) || exit 1
}

# purge: bin/obs purge -remove-state [-remove-packs]. obs down, then OBS_DIR removed: the releases, the configs, the
# store, the traces, Grafana's state, a view's copy, and OBS_DIR/packs, where bin/obs pack writes a pack unless -pack
# names another place. So it refuses while packs/ holds a pack, naming each, unless -remove-packs (FORCE=1): a
# batch's pack is gone with OBS_DIR unless it was copied out first.
purge() {
  local lock procs ports files left p a packs=()
  check_vars
  if lock="$(gate_open)"; then die "a gate runs: $lock; nothing stopped or removed"; fi
  [[ "$OBS_DIR" == /* && "$OBS_DIR" != / ]] || die "OBS_DIR must be an absolute path, not /"
  if [[ -d "$OBS_DIR" ]]; then
    [[ -f "$OBS_DIR/.laug-obs" ]] || die "OBS_DIR has no .laug-obs marker, so it is not the stack's; nothing stopped or removed"
    if git -C "$OBS_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then die "OBS_DIR is inside a git work tree; nothing removed"; fi
    mapfile -t packs < <(find "$OBS_DIR/packs" -maxdepth 1 -type f -name '*.tar.gz' -printf '%f\n' 2>/dev/null | LC_ALL=C sort)
    if (( ${#packs[@]} )) && [[ "${FORCE:-}" != 1 ]]; then
      for p in "${packs[@]}"; do say "a pack: OBS_DIR/packs/$p" >&2; done
      die "OBS_DIR/packs holds $(plural "${#packs[@]}" pack) above, which ${OBS:-bin/obs} purge -remove-state would remove with OBS_DIR; copy each out first, or -remove-packs removes them too; nothing stopped or removed"
    fi
    obs_lock
    ONLY="" down || die "a port of the stack is still taken; OBS_DIR not removed"
    rm -rf -- "$OBS_DIR"
    (( ${#packs[@]} == 0 )) || say "-remove-packs: $(plural "${#packs[@]}" pack) of OBS_DIR/packs removed with it"
  else
    say "no OBS_DIR to remove"
  fi
  procs=0
  for p in /proc/[0-9]*; do
    a=""; { IFS= read -r -d '' a <"$p/cmdline"; } 2>/dev/null
    [[ "$a" != "$OBS_DIR/bin/"* ]] || procs=$((procs + 1))
  done
  ports=0; left="$(ports_left)" && ports="$(grep -c . <<<"$left")"
  files=0; [[ ! -e "$OBS_DIR" ]] || files="$(find "$OBS_DIR" | wc -l)"
  echo "laug · $VERB: left: $procs processes, $ports ports, $files files"
  [[ -z "$left" ]] || echo "$left" | sed "s/^/laug · $VERB: still taken: /" >&2
  (( procs == 0 && ports == 0 && files == 0 ))
}

# The store's answers, for bin/obs find and obs backfill. store_q <query> [<unix time>]: an instant query's vector, one
# {metric, value} a line; 1 when the store does not answer or the answer is not a vector.
store_q() {
  local args=(--data-urlencode "query=$1") out
  [[ -z "${2:-}" ]] || args+=(--data-urlencode "time=$2")
  out="$(curl -fsS --noproxy '*' -m 10 -G http://127.0.0.1:7730/api/v1/query "${args[@]}" 2>/dev/null)" || return 1
  jq -e '.status == "success" and .data.resultType == "vector"' <<<"$out" >/dev/null 2>&1 || return 1
  jq -c '.data.result[]' <<<"$out"
}
store_up() { curl_local http://127.0.0.1:7730/-/ready 2 >/dev/null; }
GATE_RE='^g[0-9]{8}-[0-9]{6}(-[0-9]+)?$'
RUN_RE='^[A-Za-z0-9][A-Za-z0-9._-]*$'
utc() { date -u -d "@${1%.*}" +%H:%M:%S; }   # utc <unix seconds>: the time of day, UTC
ms() { awk -v s="$1" -v d="${2:-0}" 'BEGIN { printf "%.0f", (s + d) * 1000 }'; }   # ms <unix seconds> [<offset s>]

# records_check <gate dir> <gate id>: the store's records of the gate against the gate's own files, read with jq, each
# family as follow serves it (_obs/follow/render.go): the verdict and class (laug_gate_verdict, verdict.json); each
# rule's pass (laug_gate_rule, verdict.json's rules); each timeline line that parses, its step, item and wall time
# (laug_gate_step_seconds, timeline.jsonl); each item's exit and stopped_by (laug_item_exit, exit.json); each load
# item's whole-window readings (laug_item_reading, the numbers of the item's verdict JSON readings), steady's among
# them; and each fault item's readings by window, before, during, drain and after, and its four marks (laug_item_reading
# and laug_fault_mark_seconds, the raun JSON; with no raun JSON the store's marks are item.log's, which this does not
# read). The other families (the info series, clock, deadline, harness, polls, thresholds, rates, window edges, failed
# iterations, sweeps, soft assertions, signals, comparison rows and A/A lists) rest on follow's unit tests. Read at the
# gate's last timeline line plus 10 minutes, or now when that is sooner, over 2 h before, so a live gate's samples and a
# backfilled gate's grid both answer. One line on stdout, what is equal or each difference; 0 when every one is equal, 1
# when one differs, 2 when the store does not answer.
records_check() {
  local dir="$1" id="$2" at sel want got k kind file run_id diffs=() items=() n keys steps exits
  at="$(jq -nR --argjson now "$(date +%s)" '[[inputs | fromjson? | .wall? | numbers] | max // empty | . + 600, $now] | min' "$dir/timeline.jsonl" 2>/dev/null)"
  [[ "$at" =~ ^[0-9.]+$ ]] || at="$(date +%s)"
  sel="gate_id=\"$id\""
  sq() { store_q "$1" "$at"; }
  sq 'vector(1)' >/dev/null || { echo "the store does not answer"; return 2; }
  # Before the judge (bin/laug judge -write) wrote verdict.json, the store must hold no verdict or rule of the gate.
  want=""; [[ ! -f "$dir/verdict.json" ]] || want="$(jq -r '"\(.verdict) \(.class // "none")"' "$dir/verdict.json" 2>/dev/null)"
  got="$(sq "last_over_time(laug_gate_verdict{$sel}[2h])" | jq -r '"\(.metric.verdict) \(.metric.class // "none")"' | sort -u | paste -sd'|')"
  [[ "$got" == "$want" && ( -n "$want" || ! -f "$dir/verdict.json" ) ]] || diffs+=("verdict and class: the store's ${got:-none}, verdict.json's ${want:-none}")
  want="$(jq -r '(.rules // {}) | to_entries[] | "\(.key)=\(if .value.pass then 1 else 0 end)"' "$dir/verdict.json" 2>/dev/null | sort -u | paste -sd' ')"
  got="$(sq "last_over_time(laug_gate_rule{$sel}[2h])" | jq -r '"\(.metric.rule)=\(.value[1])"' | sort -u | paste -sd' ')"
  [[ "$got" == "$want" ]] || diffs+=("rules: the store's ${got:-none}, verdict.json's ${want:-none}")
  # The timeline as follow reads it: a line that does not parse, or has no step or no numeric wall, is skipped; lease,
  # leased and ended carry the item after the step's name. Lines from the last 15 s before the read are left out on
  # both sides: follow polls every 2 s and the store scrapes it every 5 s, so during a wave the newest line may not be
  # in the store yet. After the gate, and for a backfilled gate, that leaves out none.
  want="$(jq -nRcS --argjson cut "$at" '[inputs | fromjson? | objects | select((.step | type) == "string" and .step != "" and (.wall | type) == "number")
    | ((.step | capture("^(?<s>lease|leased|ended) (?<i>.+)$")) // {s: .step, i: ""}) + {w: .wall} | select(.w <= $cut - 15)] | sort_by(.s, .i, .w)' "$dir/timeline.jsonl" 2>/dev/null)"
  got="$(sq "last_over_time(laug_gate_step_seconds{$sel}[2h])" | jq -scS --argjson cut "$at" 'map({s: .metric.step, i: (.metric.item // ""), w: (.value[1] | tonumber)} | select(.w <= $cut - 15)) | sort_by(.s, .i, .w)')"
  steps="$(jq 'length' <<<"${want:-[]}" 2>/dev/null)"
  [[ "$(jq -n --argjson a "${want:-[]}" --argjson b "${got:-[]}" '$a == $b' 2>/dev/null)" == true ]] \
    || diffs+=("step lines: the store's $(jq 'length' <<<"${got:-[]}") ($(jq -nc --argjson a "${want:-[]}" --argjson b "${got:-[]}" '$b - $a | .[:3]')), timeline.jsonl's ${steps:-0} ($(jq -nc --argjson a "${want:-[]}" --argjson b "${got:-[]}" '$a - $b | .[:3]'))")
  keys="$(jq -r '.items[]?.key' "$dir/plan.json" 2>/dev/null)"
  [[ -n "$keys" ]] || keys="$(jq -r '.wiring[]?.key' "$dir/gate.json" 2>/dev/null)"
  want="$(for k in $keys; do
    [[ "$k" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || continue
    jq -c --arg k "$k" '{i: $k, e: (.exit | if type == "number" then . elif type == "string" and . != "" then (tonumber? // null) else null end),
      b: (.stopped_by | if type == "string" then . else "" end)} | select(.e != null)' "$dir/$k/exit.json" 2>/dev/null
  done | jq -scS 'sort_by(.i)')"
  got="$(sq "last_over_time(laug_item_exit{$sel}[2h])" | jq -scS 'map({i: .metric.item, e: (.value[1] | tonumber), b: (.metric.stopped_by // "")}) | sort_by(.i)')"
  exits="$(jq 'length' <<<"${want:-[]}" 2>/dev/null)"
  [[ "$(jq -n --argjson a "${want:-[]}" --argjson b "${got:-[]}" '$a == $b' 2>/dev/null)" == true ]] || diffs+=("exits: the store's $got, the exit.json files' $want")
  for k in $keys; do
    [[ "$k" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || continue
    kind="$(jq -r --arg k "$k" 'first(.items[]? | select(.key == $k) | .kind) // empty' "$dir/plan.json" 2>/dev/null)"
    [[ "$kind" == load || "$kind" == fault ]] || continue
    run_id="$(jq -r --arg k "$k" --arg id "$id" 'first(.wiring[]? | select(.key == $k) | .run_id) // "\($id)-\($k)"' "$dir/gate.json" 2>/dev/null)"
    if [[ "$kind" == load ]]; then
      file="$dir/$k/$run_id.verdict.json"
      want='{}'; [[ ! -f "$file" ]] || want="$(jq -cS '(.readings // {}) | with_entries(select(.value | type == "number"))' "$file" 2>/dev/null || echo '"unreadable"')"
      got="$(sq "last_over_time(laug_item_reading{$sel, item=\"$k\", window=\"whole\"}[2h])" | jq -scS 'map({(.metric.reading): (.value[1] | tonumber)}) | add // {}')"
      n="$(jq 'length' <<<"$want" 2>/dev/null)"
      if [[ "$(jq -n --argjson a "$want" --argjson b "$got" '$a == $b' 2>/dev/null)" == true ]]; then items+=("$k's ${n:-0} readings")
      else diffs+=("$k's readings: the store's $got, its verdict JSON's $want"); fi
      continue
    fi
    file="$dir/$k/$run_id.json"
    want='{}'; [[ ! -f "$file" ]] || want="$(jq -cS '. as $r | [("before", "during", "drain", "after") as $w | ($r[$w] | objects | to_entries[])
      | select(.value | type == "number") | {key: "\($w) \(.key)", value}] | from_entries' "$file" 2>/dev/null || echo '"unreadable"')"
    got="$(sq "last_over_time(laug_item_reading{$sel, item=\"$k\", window!=\"whole\"}[2h])" | jq -scS 'map({(.metric.window + " " + .metric.reading): (.value[1] | tonumber)}) | add // {}')"
    n="$(jq 'length' <<<"$want" 2>/dev/null)"
    if [[ "$(jq -n --argjson a "$want" --argjson b "$got" '$a == $b' 2>/dev/null)" != true ]]; then diffs+=("$k's window readings: the store's $got, its raun JSON's $want"); continue; fi
    if [[ ! -f "$file" ]]; then items+=("$k's 0 window readings (no raun JSON)"); continue; fi
    want="$(jq -cS '{inject: .inject_at, landed: .landed_at, remove: .remove_at, recovered: .recovered_at} | with_entries(.value |= (if type == "number" then .
      elif type == "string" and . != "" then (tonumber? // null) else null end)) | with_entries(select(.value != null))' "$file" 2>/dev/null || echo '"unreadable"')"
    got="$(sq "last_over_time(laug_fault_mark_seconds{$sel, item=\"$k\"}[2h])" | jq -scS 'map({(.metric.mark): (.value[1] | tonumber)}) | add // {}')"
    if [[ "$(jq -n --argjson a "$want" --argjson b "$got" '$a == $b' 2>/dev/null)" == true ]]; then items+=("$k's ${n:-0} window readings and $(jq 'length' <<<"$want") marks")
    else diffs+=("$k's marks: the store's $got, its raun JSON's $want"); fi
  done
  if (( ${#diffs[@]} )); then printf '%s; ' "${diffs[@]}" | sed 's/; $//'; echo; return 1; fi
  n="$(wc -w <<<"$(jq -r '(.rules // {}) | keys[]' "$dir/verdict.json" 2>/dev/null)")"
  echo "$(jq -r '"\(.verdict), class \(.class // "none")"' "$dir/verdict.json" 2>/dev/null || echo "no verdict.json yet and no verdict in the store"), $n rules, ${steps:-0} step lines, ${exits:-0} exits$( (( ${#items[@]} )) && printf ', %s' "${items[@]}" || echo ", no load or fault item") equal to its files"
}

# find_cmd: bin/obs find. <gate id> (GATE): its board link with the gate's window, its trace search, and what the store
# holds of it. The gate's own files, when GATE_RESULTS (gate/results by default) holds its directory, or follow still
# names it in OBS_DIR/follow, give the verdict, the window and the cells; else the store gives them. -run-id (RUN): the
# same for a laug load run, from load/results/<run id>.json's cell, start and end. It reads and prints; it starts and
# writes nothing, so it runs with the stack down and while a gate is open.
#
# The links, as their sources give them (the boards are provisioned and answer through /api/ds/query,
# cells/results/2026-10-17-obs-offline.txt section 6, but no browser has opened a link yet): Grafana's
# dashboard URL takes var-<name>, from and to in ms and timezone (Grafana 11.1 docs, Dashboard URL variables); its
# Explore URL takes panes, a URL-encoded JSON object of panes by id, each with its datasource, queries and range, with
# schemaVersion 1 and orgId (Grafana 11.1 docs, Get started with Explore, Generate Explore URLs from external tools);
# Jaeger's own UI takes service, tags as JSON, start and end in microseconds and lookback custom (the jaeger-ui bundle
# Jaeger 2.21.0 serves on 7761, static/index-*.js: its search form's submit and its /search URL parser, read
# and not run in a browser). Jaeger 2.21.0 answered 404 to the v1 query API that Grafana 11.1.0's Jaeger
# datasource asks (cells/results/2026-10-17-obs-offline.txt, section 2), so the Explore link may show an error where
# Jaeger's own UI answers. The trace line is Jaeger's API v3, which answers (find_trace).
find_cmd() {
  local gate="${GATE:-}" run="${RUN:-}" results="${GATE_RESULTS:-gate/results}" dir="" verdict items t0 done_at="" to c k
  local up rs fed=0 tried=0 never=0 none=0 rec trace from end range cells=() parts=() line
  [[ ( -n "$gate" && -z "$run" ) || ( -z "$gate" && -n "$run" ) ]] || die "a <gate id> or -run-id <run id>, one of the two"
  if [[ -n "$run" ]]; then find_run "$run"; return; fi
  [[ "$gate" =~ $GATE_RE ]] || die "<gate id>: a gate id, g<date>-<time>, as ${LAUG:-bin/laug} gate run printed it"
  if [[ -f "$results/$gate/gate.json" ]]; then dir="$results/$gate"
  elif [[ -f "$OBS_DIR/follow/$gate.json" ]]; then
    dir="$(jq -r '.dir // empty' "$OBS_DIR/follow/$gate.json" 2>/dev/null)"; [[ -f "$dir/gate.json" ]] || dir=""
  fi
  up=0; store_up && up=1
  if [[ -n "$dir" ]]; then
    verdict="$(jq -r '"\(.verdict) (verdict.json)"' "$dir/verdict.json" 2>/dev/null || echo "no verdict yet: verdict.json not written")"
    items="$(jq '.items | length' "$dir/plan.json" 2>/dev/null || echo '?')"
    t0="$(jq -r '.t0.wall // empty' "$dir/clock.json" 2>/dev/null)"
    [[ -n "$t0" ]] || t0="$(head -1 "$dir/timeline.jsonl" 2>/dev/null | jq -r '.wall // empty' 2>/dev/null)"
    done_at="$(jq -r 'select(.step == "done") | .wall' "$dir/timeline.jsonl" 2>/dev/null | tail -1)"
    for k in $(jq -r '.items[]?.key' "$dir/plan.json" 2>/dev/null); do
      [[ "$k" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || continue
      c="$(jq -r '.id // empty' "$dir/$k/lease.json" 2>/dev/null)"; [[ -z "$c" ]] || cells+=("$c $k")
    done
  elif (( up )); then
    t0="$(store_q "last_over_time(laug_gate_clock_seconds{gate_id=\"$gate\", mark=\"t0\"}[91d])" | jq -r '.value[1]' | head -1)"
    done_at="$(store_q "last_over_time(laug_gate_step_seconds{gate_id=\"$gate\", step=\"done\"}[91d])" | jq -r '.value[1]' | head -1)"
    verdict="$(store_q "last_over_time(laug_gate_verdict{gate_id=\"$gate\"}[91d])" | jq -r '"\(.metric.verdict) (the store)"' | head -1)"
    verdict="${verdict:-no verdict in the store}"
    mapfile -t cells < <(store_q "last_over_time(laug_item_info{gate_id=\"$gate\"}[91d])" | jq -r 'select(.metric.cell) | "\(.metric.cell) \(.metric.item)"' | sort -u)
    items="$(store_q "last_over_time(laug_item_info{gate_id=\"$gate\"}[91d])" | jq -r '.metric.item' | sort -u | wc -l)"
  else
    die "gate $gate: no directory in $results, follow does not name it, and the store is down (${OBS:-bin/obs} up)"
  fi
  [[ "$t0" =~ ^[0-9.]+$ ]] || die "gate $gate: no t0 in its files or in the store"
  [[ "$done_at" =~ ^[0-9.]+$ ]] || done_at=""
  from="$(ms "$t0" -30)"; to=now; [[ -z "$done_at" ]] || to="$(ms "$done_at" 30)"
  echo "laug · gate $gate: $verdict, $items items, t0 $(utc "$t0") UTC, $( [[ -n "$done_at" ]] && echo "done $(utc "$done_at") UTC" || echo "not done")"
  echo "  board   http://127.0.0.1:7740/d/laug-gate-run/?var-gate=$gate&from=$from&to=$to&timezone=utc"
  echo "  trace   http://127.0.0.1:7740/explore?schemaVersion=1&orgId=1&panes=$(jq -nr --arg g "gate-$gate" --arg f "$from" --arg t "$to" \
    '{a: {datasource: "laug-traces", queries: [{refId: "A", datasource: {type: "jaeger", uid: "laug-traces"}, queryType: "search",
      service: "laug-worker", tags: "temporalWorkflowID=\($g)"}], range: {from: $f, to: $t}}} | tojson | @uri') (Jaeger search: service laug-worker, temporalWorkflowID=gate-$gate)"
  echo "          http://127.0.0.1:7761/search?service=laug-worker&tags=$(jq -nr --arg g "gate-$gate" '{temporalWorkflowID: $g} | tojson | @uri')&start=${from}000&end=$( [[ "$to" == now ]] && echo "$(date +%s)000000" || echo "${to}000")&lookback=custom (Jaeger's own UI)"
  for c in "${cells[@]}"; do parts+=("${c%% *} (${c#* })"); done
  if (( ! up )); then
    echo "  store   down: ${OBS:-bin/obs} up, then ${OBS:-bin/obs} find again; cells $( (( ${#parts[@]} )) && printf '%s, ' "${parts[@]}" | sed 's/, $//' || echo none)"
    return 0
  fi
  end="${done_at:-$(date +%s)}"
  range="$(awk -v a="$t0" -v b="$end" 'BEGIN { printf "%.0f", b - a + 90 }')s"
  parts=()
  for c in "${cells[@]}"; do
    tried=$((tried + 1))
    rs="$(store_q "max_over_time(up{job=\"laug-cells\", gate_id=\"$gate\", cell=\"${c%% *}\"}[$range])" \
          "$(awk -v b="$end" 'BEGIN { printf "%.0f", b + 60 }')" | jq -r '.value[1]' | sort -r | head -1)"
    case "$rs" in
      1) fed=$((fed + 1)); parts+=("${c%% *} (${c#* })") ;;
      0) never=$((never + 1)); parts+=("${c%% *} (${c#* }, its target never answered)") ;;
      *) none=$((none + 1)); parts+=("${c%% *} (${c#* }, no target in the store)") ;;
    esac
  done
  if (( tried )); then line="cells $(printf '%s, ' "${parts[@]}" | sed 's/, $//'): $fed of $tried federated"; else line="no cell leased"; fi
  (( none == 0 )) || line+="; $none with no target in the store: the gate ran before the stack, or with it down"
  (( never == 0 )) || line+="; $never with a target that never answered: no Prometheus stood behind its lease"
  rec="$(store_q "count by (job) (last_over_time(laug_gate_info{gate_id=\"$gate\"}[91d]))" | jq -r '.metric.job // "backfilled"' | sort -u | paste -sd' ')"
  if [[ -z "$rec" ]]; then rec="none$( [[ -n "$dir" ]] && echo ", ${OBS:-bin/obs} backfill puts them in once the gate is 3 h old")"
  else
    rec="$(sed 's/laug-follow/live/; s/ /, /g' <<<"$rec")"
    [[ -z "$dir" ]] || rec+=", $(records_check "$dir" "$gate")"
  fi
  trace="$(find_trace "$gate" "$t0" "$end")"
  echo "  store   $line; records: $rec; trace: $trace"
}

# find_trace <gate id> <t0> <end>: the gate's own traces in Jaeger, through its API v3 on 7761 (the v1 API answers 404
# on 2.21.0, cells/results/2026-10-17-obs-offline.txt section 2): service laug-worker's traces with a span whose
# temporalWorkflowID is gate-<id>, started from t0 less 30 s to the end plus 30 s. query.attributes takes a JSON object
# and Jaeger keeps only the traces that hold it; a search that finds none answers 404, "No traces found" (seen
# on the dev host against Jaeger 2.21.0, section 4g). The tag's name is the SDK's OpenTelemetry
# interceptor's: a fake gate traced with ORCH_OTLP read "1, 41 spans" (seen on the dev host, section 5).
# Prints "none", "<traces>, <spans> spans" or why it cannot tell.
find_trace() {
  local gate="$1" min max out code
  min="$(date -u -d "@$(awk -v s="$2" 'BEGIN { printf "%d", s - 30 }')" +%Y-%m-%dT%H:%M:%SZ)"
  max="$(date -u -d "@$(awk -v s="$3" 'BEGIN { printf "%d", s + 31 }')" +%Y-%m-%dT%H:%M:%SZ)"
  out="$(curl -sS --noproxy '*' -m 10 -G -w '\n%{http_code}' http://127.0.0.1:7761/api/v3/traces \
    --data-urlencode query.service_name=laug-worker \
    --data-urlencode "query.attributes=$(jq -nc --arg g "gate-$gate" '{temporalWorkflowID: $g}')" \
    --data-urlencode "query.start_time_min=$min" --data-urlencode "query.start_time_max=$max" 2>/dev/null)" || { echo "the trace store is down"; return; }
  code="${out##*$'\n'}"; out="${out%$'\n'*}"
  case "$code" in
    200) jq -r '[.result.resourceSpans[]?.scopeSpans[]?.spans[]?] | "\(map(.traceId) | unique | length), \(length) span\(if length == 1 then "" else "s" end)"' <<<"$out" 2>/dev/null \
           || echo "the trace store's answer did not parse" ;;
    404) if [[ "$(jq -r '.error.message // empty' <<<"$out" 2>/dev/null)" == "No traces found" ]]; then echo none
         else echo "the trace store answered 404: $(head -c 200 <<<"$out")"; fi ;;
    *) echo "the trace store answered ${code:-nothing}: $(jq -r '.error.message // empty' <<<"$out" 2>/dev/null | head -c 200)" ;;
  esac
}

# find_run <run id>: bin/obs find -run-id <run id>, a laug load run outside a gate: load/results/<run id>.json's
# cell (its project, laug-<8 hex>), start and end. Such a run has no gate records (follow reads gate directories only)
# and no trace.
find_run() {
  local run="$1" f cell start end verdict rs
  [[ "$run" =~ $RUN_RE ]] || die "-run-id <run id>: one word of letters, digits, ., _ and -, from a letter or digit"
  f="load/results/$run.json"
  [[ -f "$f" ]] || die "-run-id $run: no $f in this checkout"
  cell="$(jq -r '.project // "" | capture("^laug-(?<id>[0-9a-f]{8})$").id // empty' "$f" 2>/dev/null)"
  start="$(jq -r '.start | fromdateiso8601' "$f" 2>/dev/null)"; end="$(jq -r '.end | fromdateiso8601' "$f" 2>/dev/null)"
  [[ "$start" =~ ^[0-9]+$ && "$end" =~ ^[0-9]+$ ]] || die "-run-id $run: $f has no start and end"
  verdict="$(jq -r '"\(.verdict) (\(input_filename | sub(".*/"; "")))"' "load/results/$run.verdict.json" 2>/dev/null || echo "no verdict file")"
  echo "laug · run $run: $verdict, cell ${cell:-unknown (its project is $(jq -r '.project // "none"' "$f"))}, $(date -u -d "@$start" '+%Y-%m-%d %H:%M:%S') to $(utc "$end") UTC"
  echo "  board   http://127.0.0.1:7740/d/laug-raun/?var-cell=${cell}&from=$(ms "$start" -30)&to=$(ms "$end" 30)&timezone=utc"
  if [[ -z "$cell" ]]; then rs="no cell id, so no target to look for"
  elif ! store_up; then rs="down: ${OBS:-bin/obs} up, then ${OBS:-bin/obs} find again"
  else
    case "$(store_q "max_over_time(up{job=\"laug-cells\", cell=\"$cell\"}[$(( end - start + 60 ))s])" "$(( end + 30 ))" | jq -r '.value[1]' | sort -r | head -1)" in
      1) rs="cell $cell federated" ;;
      0) rs="cell $cell: its target never answered" ;;
      *) rs="cell $cell: no target in the store, the run was before the stack or with it down" ;;
    esac
  fi
  echo "  store   $rs; records: none outside a gate; trace: none outside a gate"
}

# backfill: bin/obs backfill. Each gate directory of -results (gate/results by default), or one gate id's alone, that
# is not in the store yet and whose last timeline line is over 3 h old ("not safe to backfill data from the last 3
# hours", Prometheus 2.53.0 storage docs): rendered by bin/obs follow -openmetrics with the gate's own times,
# checked by promtool check metrics, made into blocks by promtool tsdb create-blocks-from openmetrics in
# OBS_DIR/backfill.new, and moved into the store's TSDB once every gate rendered and checked; one that fails stops the
# run before anything moves. Prometheus 2.53.0 loads moved blocks at its next reload of its block directory, with no
# restart: 15 s and 35 s after the move (seen on the dev host, cells/results/2026-10-17-obs-offline.txt
# section 4; the reload comes about once a minute, tsdb/db.go at v2.53.0). So this waits, up to 300 s, until
# the store answers for every gate it moved, then reads each one's records back against its files (records_check).
# A gate over 90 d old is left, past the store's retention; a gate whose timeline holds no line with a wall time that
# parses has no time to render at, and stops the run before anything is rendered. Refused while a gate is open, with
# the store down, and with a gate id for a gate under 3 h or over 90 d old.
backfill() {
  local results="${GATE_RESULTS:-gate/results}" promtool lock work dirs=() d id last now_s have todo=() skip_new=() skip_have=() skip_old=()
  local out rc blocks=0 b s_n r_n n=0 bad=() samples=0 series=0
  check_vars
  [[ -z "${GATE:-}" || "$GATE" =~ $GATE_RE ]] || die "<gate id>: a gate id, g<date>-<time>, as ${LAUG:-bin/laug} gate run printed it"
  if lock="$(gate_open)"; then die "a gate runs: $lock; nothing backfilled"; fi
  [[ -d "$OBS_DIR" ]] || die "no OBS_DIR: ${OBS:-bin/obs} up first"
  if d="$(view_up)"; then die "the store up is bin/obs view's, a view of $d, not the stack's; ${OBS:-bin/obs} down, then ${OBS:-bin/obs} up"; fi
  load_up
  [[ -n "${UP[prometheus]:-}" ]] && store_up || die "the store is down: ${OBS:-bin/obs} up first"
  promtool="$(obs_bin prometheus promtool)"
  [[ -n "$promtool" ]] || die "no promtool in a tree checked against $VERSIONS as it reads now; ${OBS:-bin/obs} up fetches it"
  obs_lock
  if [[ -n "${GATE:-}" ]]; then
    [[ -f "$results/$GATE/timeline.jsonl" ]] || die "gate $GATE: no $results/$GATE/timeline.jsonl"
    dirs=("$results/$GATE")
  else
    mapfile -t dirs < <(find "$results" -mindepth 1 -maxdepth 1 -type d -name 'g*' | sort)
  fi
  have=" $(store_q 'group by (gate_id) (last_over_time(laug_gate_info[91d]))' | jq -r '.metric.gate_id' | paste -sd' ') "
  now_s="$(date +%s)"
  for d in "${dirs[@]}"; do
    id="${d##*/}"
    [[ "$id" =~ $GATE_RE && -f "$d/timeline.jsonl" ]] || continue
    if [[ "$have" == *" $id "* ]]; then skip_have+=("$id"); continue; fi
    # The newest wall time of the lines that parse, as follow reads the file: a line that does not parse is skipped,
    # and a timeline with no such line gives the gate no time, so it is a bad gate, never an old or a new one.
    last="$(jq -nR '[inputs | fromjson? | .wall? | numbers] | max // empty | floor' "$d/timeline.jsonl" 2>/dev/null)"
    if [[ ! "$last" =~ ^[0-9]+$ ]]; then bad+=("$id: timeline.jsonl holds no line with a wall time that parses"); continue; fi
    if (( now_s - last < 10800 )); then
      [[ -z "${GATE:-}" ]] || die "gate $id: its last line is $(( (now_s - last) / 60 )) minutes old, under 3 h: not safe to backfill (Prometheus 2.53.0 storage docs)"
      skip_new+=("$id"); continue
    fi
    if (( now_s - last > 90 * 86400 )); then
      [[ -z "${GATE:-}" ]] || die "gate $id: its last line is $(( (now_s - last) / 86400 )) days old, past the store's 90 d retention"
      skip_old+=("$id"); continue
    fi
    todo+=("$d")
  done
  (( ${#skip_have[@]} == 0 )) || say "${#skip_have[@]} in the store already, left: ${skip_have[*]}"
  (( ${#skip_new[@]} == 0 )) || say "${#skip_new[@]} under 3 h old, left for later: ${skip_new[*]}"
  (( ${#skip_old[@]} == 0 )) || say "${#skip_old[@]} over 90 d old, past the store's retention, left: ${skip_old[*]}"
  if (( ${#bad[@]} )); then
    printf '%s\n' "${bad[@]}" | sed "s/^/laug · $VERB: /" >&2
    die "${#bad[@]} of ${#dirs[@]} gate directories have no time to backfill at; nothing rendered or moved"
  fi
  (( ${#todo[@]} )) || { say "nothing to backfill"; return 0; }
  work="$OBS_DIR/backfill.new"; rm -rf -- "$work"; mkdir -p -- "$work" || die "cannot make OBS_DIR/backfill.new"
  for d in "${todo[@]}"; do
    id="${d##*/}"
    if ! bin/obs follow -openmetrics "$d" -dir "$OBS_DIR" >"$work/$id.om" 2>"$work/$id.err"; then bad+=("$id: obs follow -openmetrics: $(head -1 "$work/$id.err")"); continue; fi
    [[ ! -s "$work/$id.err" ]] || say "$id: $(head -1 "$work/$id.err")"
    out="$(env -i PATH=/usr/bin:/bin "$promtool" check metrics <"$work/$id.om" 2>&1)"; rc=$?
    (( rc == 0 )) && [[ -z "$out" ]] || { bad+=("$id: promtool check metrics: exit $rc, $(head -1 <<<"$out")"); continue; }
    out="$(env -i PATH=/usr/bin:/bin HOME="$work" "$promtool" tsdb create-blocks-from openmetrics "$work/$id.om" "$work/$id" 2>&1)"; rc=$?
    (( rc == 0 )) || { bad+=("$id: promtool tsdb create-blocks-from openmetrics: exit $rc, $(tail -1 <<<"$out")"); continue; }
    # promtool's table: BLOCK ULID, MIN TIME, MAX TIME, DURATION, NUMBER OF SAMPLES, NUMBER OF CHUNKS, NUMBER OF SERIES, SIZE.
    read -r b s_n r_n < <(awk '$1 ~ /^[0-9A-Z]{26}$/ { n++; s += $5; r += $7 } END { print n + 0, s + 0, r + 0 }' <<<"$out")
    blocks=$((blocks + b)); samples=$((samples + s_n)); series=$((series + r_n))
    n=$((n + 1))
  done
  if (( ${#bad[@]} )); then
    printf '%s\n' "${bad[@]}" | sed "s/^/laug · $VERB: /" >&2
    die "${#bad[@]} of ${#todo[@]} gates did not render or check; nothing moved into the store (OBS_DIR/backfill.new keeps what was made)"
  fi
  say "$n gates rendered: obs follow -openmetrics, then promtool check metrics exit 0 and no output, then tsdb create-blocks-from openmetrics exit 0, for each; $blocks blocks, $samples samples, $series series"
  for d in "${todo[@]}"; do
    id="${d##*/}"
    for b in "$work/$id"/*/; do b="${b%/}"; [[ -f "$b/meta.json" ]] && mv -- "$b" "$OBS_DIR/prometheus/${b##*/}"; done
  done
  local t_mv="$SECONDS" want missing
  want="$(printf '%s\n' "${todo[@]##*/}" | sort)"
  while :; do
    missing="$(comm -23 <(echo "$want") <(store_q 'group by (gate_id) (last_over_time(laug_gate_info[91d]))' | jq -r '.metric.gate_id' | sort))"
    [[ -n "$missing" ]] || break
    (( SECONDS - t_mv < 300 )) || die "the store has not loaded $(wc -l <<<"$missing") of the $n gates 300 s after the move; ${OBS:-bin/obs} down -only prometheus, then ${OBS:-bin/obs} up loads them at its start"
    sleep 5
  done
  say "$blocks blocks moved into the store's TSDB; it answered for all $n gates $(( SECONDS - t_mv )) s after the move, with no restart"
  rm -rf -- "$work"
  local equal=0 diffs=()
  for d in "${todo[@]}"; do
    id="${d##*/}"
    if out="$(records_check "$d" "$id")"; then equal=$((equal + 1)); echo "  $id: $out"
    else diffs+=("$id"); echo "  $id: DIFFERS: $out"; fi
  done
  say "$equal of $n gates: the store's verdict, class, rules, step lines, exits, load items' readings, and fault items' readings by window and marks equal their files$( (( ${#diffs[@]} )) && echo "; differ: ${diffs[*]}")"
  (( ${#diffs[@]} == 0 ))
}

# bin/obs pack and obs view (_obs/cmd/obs/doc.go): a stopped host's store read on the dev host with no lab
# hour. A pack is OBS_DIR/prometheus and OBS_DIR/jaeger as one tarball, with PACK.json (what it holds and the check it
# passed) and SHA256SUMS; it leaves out the store's lock and its log of running queries, and everything else of
# OBS_DIR: the releases, the configs, Grafana's database and password, follow's gate paths, the targets, the logs. It
# is never committed, so PACK must be outside every work tree. A view unpacks one into OBS_DIR/view and runs the
# store and Grafana alone, on the stack's ports, with no scrape job: it measures nothing.

# The check's lists: check/deny-shapes.txt, committed, which names no private word; a deny list, the file LAUG_DENYLIST
# names when it is set and the file is here, read through check/denylist.sh by the rule every reader of it keeps
# (internal/denylist): a plain line a fixed string and a re: line a regular expression, both with ASCII letters
# folded; and this host's own words (host_words), taken at run time, so a host with no deny list, the lab host among
# them, still checks its own. A plain line of n letters, folded, matches random bytes at a given place about once in
# 2^(7n) bytes, so a 5-letter word about once in 32 GiB of state (computed).
SHAPES=check/deny-shapes.txt
WORDS=${LAUG_DENYLIST:-}

# host_words: this host's name (uname -n, which is what hostname prints), its full name (hostname -f, when it gives
# one), the user name (id -un) and the home path ($HOME), the words laug orchestrator record's leak check takes from the
# host (internal/orchestrator/record.go, Scrub). None is ever written into a tracked file, a pack or a line: a hit names
# its kind. The name, the full name and the user name are grepped as words, with a byte that is not a letter, a digit or
# _ on either side (grep -w), and the home path as written. A word under 5 bytes is not grepped, and the pack says so:
# random bytes hold a given 4-byte word with a non-word byte either side about once in 2^33 bytes, so a store of 20 GB
# would refuse by chance, and a 5-byte one about once in 2^41, within the shapes' bound (computed). Nor is a name that
# starts localhost, which names no host. HOST_WORD maps each word grepped to its kind, HOST_AS_WORD holds those grepped
# as words, and HOST_CHECKED and HOST_SKIPPED list the kinds grepped and those not, each with why.
declare -A HOST_WORD=() HOST_AS_WORD=()
HOST_CHECKED=() HOST_SKIPPED=()
host_words() {
  local LC_ALL=C kind as w full="" home="${HOME:-}"
  HOST_WORD=(); HOST_AS_WORD=(); HOST_CHECKED=(); HOST_SKIPPED=()
  if command -v hostname >/dev/null 2>&1; then full="$(timeout 5 hostname -f 2>/dev/null)"; fi
  home="${home%/}"
  while IFS=$'\t' read -r kind as w; do
    if [[ -z "$w" ]]; then HOST_SKIPPED+=("$kind, none here")
    elif [[ -n "${HOST_WORD[$w]:-}" ]]; then HOST_SKIPPED+=("$kind, the same as its ${HOST_WORD[$w]}")
    elif [[ "$w" == localhost* ]]; then HOST_SKIPPED+=("$kind, localhost, which names no host")
    elif (( ${#w} < 5 )); then HOST_SKIPPED+=("$kind, ${#w} bytes, too short to grep over binary data")
    else HOST_WORD[$w]="$kind"; [[ "$as" == 0 ]] || HOST_AS_WORD[$w]=1; HOST_CHECKED+=("$kind"); fi
  done < <(printf '%s\t1\t%s\n' name "$(uname -n 2>/dev/null)" "full name" "$full" "user name" "$(id -un 2>/dev/null)"
           printf '%s\t0\t%s\n' "home path" "$home")
}

# and_list <item>...: "a", "a and b", "a, b and c".
and_list() {
  local n=$# out
  (( n )) || return 0
  (( n > 1 )) || { echo "$1"; return 0; }
  out="$(printf '%s, ' "${@:1:n-1}")"; echo "${out%, } and ${!n}"
}

# pack_listing_bad: of a pack's listing, tar --numeric-owner -tv, each member that is not a regular file or a
# directory, not owned 0/0, with a name of more than one word or a .. in it, or outside PACK.json, SHA256SUMS,
# prometheus/ and jaeger/; by the member's name, the sixth field, never a link's target. GNU tar lists a sparse file
# as a regular one.
pack_listing_bad() {
  awk '{ t = substr($1, 1, 1); n = (NF >= 6 ? $6 : $0)
    if (NF != 6 || (t != "-" && t != "d") || $2 != "0/0" || n ~ /(^|\/)\.\.(\/|$)/ ||
        n !~ /^(PACK\.json|SHA256SUMS|(prometheus|jaeger)(\/[A-Za-z0-9._-]+)*\/?)$/) print n }'
}

# view_up: the pack a view runs and 0, when bin/obs view's record is there and the store or Grafana runs by its
# record; else 1. bin/obs view writes the record before it starts either part, so a view whose bin/obs view ended
# before both were up, by a signal or a KILL, is still a view here.
view_up() {
  local f="$OBS_DIR/run/view.json"
  [[ -f "$f" ]] || return 1
  rec_check prometheus >/dev/null || rec_check grafana >/dev/null || return 1
  jq -r '.pack // "a pack"' "$f" 2>/dev/null || echo "a pack"
}

# deny_check <dir> <decoded> <work>: every file under <dir>/prometheus and <dir>/jaeger but the store's lock and query log,
# as written, and every file under <decoded> (bin/obs follow -decode's), grepped byte for byte (LC_ALL=C, -a) for
# each shape of SHAPES and each of HOST_WORD (host_words, run first), records cut at 0 bytes, and for each line of
# WORDS when that file is here, a line at a time, as check/denylist.sh and laug orchestrator record read it. No path
# needs a grep of its own: bin/obs follow -decode places every one in a layout of the stores', ULIDs, digits and fixed
# names (decode.go), so none can hold a word. One line a hit on stdout, "<path>[ (decoded)]: <list>:<line>, <n> times"
# for a shape, "<path>[ (decoded)]: denylist line <N>, <n> times" for a line of WORDS, or for a word of this host's
# "<path>[ (decoded)]: this host's <kind>, <n> times", never the bytes that matched, which may be the very word the
# check is for: those stay in <work>, a new directory under OBS_DIR/pack.new, removed with it. 0 when there is none, 1
# when there is one, 2 when a grep failed, a list does not read or is not a file, or a line of WORDS is refused
# ("denylist line <N>: <why>", never its text). An xtrace the caller turned on is off inside: it would print the
# bytes that matched.
deny_check() {
  local -; set +x
  local dir="$1" dec="$2" tmp="$3" n rec path m ln k k2 line w rc base kind how shown counted
  local -a tops=() shape=() shape_ln=()
  local -A count=()
  mkdir -m 700 -- "$tmp" || return 2
  for k in prometheus jaeger; do [[ ! -d "$dir/$k" ]] || tops+=("$k"); done
  while IFS= read -r line; do
    ln="${line%%:*}"; rec="${line#*:}"
    [[ "$rec" =~ ^[[:space:]]*(#|$) ]] && continue
    if [[ "$rec" == "except "* ]]; then printf '[^:]*:%s\n' "${rec#except }" >>"$tmp/except"; continue; fi
    shape+=("$rec"); shape_ln+=("$ln"); printf '%s\n' "$rec" >>"$tmp/shapes"
  done < <(grep -n '' "$SHAPES")
  (( ${#shape[@]} )) || { echo "$SHAPES: no shape"; rm -rf -- "$tmp"; return 2; }
  [[ -f "$tmp/except" ]] || : >"$tmp/except"
  for n in "${!shape[@]}"; do
    LC_ALL=C grep -E -e "${shape[$n]}" </dev/null >/dev/null 2>&1
    (( $? != 2 )) || { echo "$SHAPES:${shape_ln[$n]}: not an expression grep -E reads"; rm -rf -- "$tmp"; return 2; }
  done
  # grep_state <name> <grep options>...: the state as written into <name>.raw and its decoded records into <name>.dec,
  # "<path>:<match>" records cut at 0 bytes; each grep's exit a line of <name>.rc.
  grep_state() {
    local name="$1"; shift
    { ( cd -- "$dir" && LC_ALL=C nice -n 10 grep -r -a -z -o -H "$@" --exclude=lock --exclude=queries.active -- "${tops[@]}" ); echo "$?" >>"$tmp/$name.rc"; } >"$tmp/$name.raw"
    if [[ -d "$dec" ]] && [[ -n "$(ls -A -- "$dec")" ]]; then
      { ( cd -- "$dec" && LC_ALL=C nice -n 10 grep -r -a -z -o -H "$@" -- . ); echo "$?" >>"$tmp/$name.rc"; } >"$tmp/$name.dec"
    fi
  }
  # The shapes first; a match that is all of an except is not one. A path never holds a colon (decode.go's layouts),
  # so the except's [^:]*: is the path.
  grep_state hits -E -f "$tmp/shapes"
  for k in raw dec; do
    [[ -f "$tmp/hits.$k" ]] || continue
    LC_ALL=C grep -z -v -x -E -f "$tmp/except" -- "$tmp/hits.$k" >"$tmp/hits.$k.kept"; echo "$?" >>"$tmp/except.rc"
    mv -f -- "$tmp/hits.$k.kept" "$tmp/hits.$k"
  done
  # grep_files <name> <grep options>...: the files of the state as written, and of its decoded records, that hold a
  # match, a path a line in <name>.raw and <name>.dec, read a line at a time; each grep's exit a line of <name>.rc.
  grep_files() {
    local name="$1"; shift
    { ( cd -- "$dir" && LC_ALL=C nice -n 10 grep -r -a -l "$@" --exclude=lock --exclude=queries.active -- "${tops[@]}" ); echo "$?" >>"$tmp/$name.rc"; } >"$tmp/$name.raw"
    if [[ -d "$dec" ]] && [[ -n "$(ls -A -- "$dec")" ]]; then
      { ( cd -- "$dec" && LC_ALL=C nice -n 10 grep -r -a -l "$@" -- . ); echo "$?" >>"$tmp/$name.rc"; } >"$tmp/$name.dec"
    fi
  }
  # The word list: check/denylist.sh writes its rules into <work>/list, or names the line it refuses; one grep of the
  # plain lines and one of the re: lines name the files that hold a hit, and in those alone each rule is counted on
  # its own, below, so a hit names the line of the list that made it.
  if [[ -e "$WORDS" || -L "$WORDS" ]]; then
    check/denylist.sh -list "$WORDS" -rules "$tmp/list" >/dev/null 2>"$tmp/list.err" \
      || { echo "$(head -1 -- "$tmp/list.err")"; rm -rf -- "$tmp"; return 2; }
    [[ ! -s "$tmp/list/plain" ]] || grep_files plain -i -F -f "$tmp/list/plain"
    [[ ! -s "$tmp/list/re" ]] || grep_files re -i -E -f "$tmp/list/re"
  fi
  for w in "${!HOST_WORD[@]}"; do
    if [[ -n "${HOST_AS_WORD[$w]:-}" ]]; then printf '%s\n' "$w" >>"$tmp/hostw"; else printf '%s\n' "$w" >>"$tmp/hostf"; fi
  done
  [[ ! -s "$tmp/hostw" ]] || grep_state hostw -w -F -f "$tmp/hostw"
  [[ ! -s "$tmp/hostf" ]] || grep_state hostf -F -f "$tmp/hostf"
  for k in "$tmp"/*.rc; do
    [[ -f "$k" ]] || continue
    while read -r rc; do
      (( rc < 2 )) || { echo "a grep over the state could not read a file (exit $rc)"; rm -rf -- "$tmp"; return 2; }
    done <"$k"
  done
  # Count each hit by its file and the line of the list that matched, or the kind of this host's word; the matched
  # bytes stay in this function.
  for k in hits.raw hits.dec hostw.raw hostw.dec hostf.raw hostf.dec; do
    [[ -s "$tmp/$k" ]] || continue
    while IFS= read -r -d '' rec; do
      path="${rec%%:*}"; m="${rec#*:}"; path="${path#./}"
      [[ "$k" != *.dec ]] || path="${path%.decoded} (decoded)"
      case "$k" in
        hits.*) ln="?"; for n in "${!shape[@]}"; do if [[ "$m" =~ ${shape[$n]} ]]; then ln="${shape_ln[$n]}"; break; fi; done
                k2="$path: $SHAPES:$ln" ;;
        *) k2="$path: this host's ${HOST_WORD[$m]:-word}" ;;
      esac
      count[$k2]=$(( ${count[$k2]:-0} + 1 ))
    done <"$tmp/$k"
  done
  # Each file a word grep named, against each rule of its kind on its own: its matches, by the file and the line of
  # the list. grep -o's matches go to wc, never to a line. A count grep that fails is the check's failure, and a file
  # named with no rule counted is still a hit, of line ?.
  for k in plain.raw plain.dec re.raw re.dec; do
    [[ -s "$tmp/$k" ]] || continue
    if [[ "$k" == *.raw ]]; then base="$dir"; else base="$dec"; fi
    kind="${k%%.*}"; if [[ "$kind" == plain ]]; then how=-F; else how=-E; fi
    while IFS= read -r path; do
      shown="${path#./}"; [[ "$k" != *.dec ]] || shown="${shown%.decoded} (decoded)"
      counted=false
      while read -r ln rec; do
        [[ "$rec" == "${kind:0:1}" ]] || continue
        m="$(cd -- "$base" && LC_ALL=C grep -a -o -i "$how" -f "$tmp/list/$ln" -- "$path" | wc -l)"; rc=$?
        (( rc < 2 )) || { echo "a grep over the state could not read a file (exit $rc)"; rm -rf -- "$tmp"; return 2; }
        (( m == 0 )) || { count["$shown: denylist line $ln"]=$(( ${count["$shown: denylist line $ln"]:-0} + m )); counted=true; }
      done <"$tmp/list/rules"
      $counted || count["$shown: denylist line ?"]=$(( ${count["$shown: denylist line ?"]:-0} + 1 ))
    done <"$tmp/$k"
  done
  rm -rf -- "$tmp"
  (( ${#count[@]} )) || return 0
  for k in "${!count[@]}"; do echo "$k, ${count[$k]} time$([[ ${count[$k]} == 1 ]] || echo s)"; done | LC_ALL=C sort
  return 1
}

# pack: bin/obs pack [-pack <file>], PACK. After bin/obs down: refused while a part of the stack or a view runs, while
# a gate is open, with OBS_DIR/prometheus empty, a PACK inside a work tree, inside OBS_DIR's stores or view, or already
# there, and with less free disk than the pack and the decoded records need above OBS_MIN_FREE_GIB. bin/obs follow
# -decode writes the WAL's and the tables' compressed records, decoded, into OBS_DIR/pack.new; deny_check greps those
# and the files for the shapes, the word list where it is here and this host's own words (host_words); a hit, or a file
# the check cannot read, packs nothing. Then SHA256SUMS and PACK.json, and the tarball: gzip,
# sparse files kept sparse (Badger's are, while it runs), sorted by name, owner and group 0 by number, so no user name
# rides in a header; written to <pack>.part, its listing checked, then renamed.
pack() {
  local lock pack dir name real part up=() tops=() stage sum hits rc need_kb free_kb made commit list bad files bytes
  local nshapes nexcept nwords="null" nplain="null" nre="null" words_here=false manifest
  check_vars
  [[ "$OBS_DIR" == /* ]] || die "OBS_DIR must be an absolute path"
  [[ -f "$OBS_DIR/.laug-obs" ]] || die "no OBS_DIR with its .laug-obs marker: nothing to pack"
  if lock="$(gate_open)"; then die "a gate runs: $lock; nothing packed"; fi
  obs_lock
  load_up
  if (( ${#UP[@]} )); then
    for part in "${PARTS[@]}"; do [[ -z "${UP[$part]:-}" ]] || up+=("$part (pid ${UP[$part]})"); done
    die "running: $(printf '%s, ' "${up[@]}" | sed 's/, $//'); ${OBS:-bin/obs} down first, since a part that runs writes its files while a pack reads them"
  fi
  [[ -n "$(ls -A -- "$OBS_DIR/prometheus" 2>/dev/null)" ]] || die "OBS_DIR/prometheus holds nothing: no store to pack"
  for part in prometheus jaeger; do [[ ! -d "$OBS_DIR/$part" ]] || tops+=("$part"); done
  [[ -f "$SHAPES" ]] || die "no $SHAPES, the shapes the check refuses"
  made="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  pack="${PACK:-$OBS_DIR/packs/laug-obs-$(date -u -d "$made" +%Y%m%d-%H%M%S).tar.gz}"
  name="${pack##*/}"
  [[ "$name" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*\.tar\.gz$ ]] || die "PACK's file name must end in .tar.gz and hold letters, digits and . _ + - only"
  [[ "$pack" == /* ]] || pack="$PWD/$pack"
  dir="${pack%/*}"
  [[ -n "${PACK:-}" ]] || mkdir -p -- "$dir" || die "cannot make OBS_DIR/packs"
  [[ -d "$dir" ]] || die "PACK's directory does not exist"
  dir="$(cd -- "$dir" && pwd -P)"; pack="$dir/$name"
  if git -C "$dir" rev-parse --is-inside-work-tree >/dev/null 2>&1; then die "PACK is inside a git work tree; a pack is never committed, so it goes outside every checkout"; fi
  real="$(cd -- "$OBS_DIR" && pwd -P)"
  for part in prometheus jaeger view pack.new; do
    [[ "$dir/" != "$real/$part/"* ]] || die "PACK is inside OBS_DIR/$part, which a pack reads or a view runs from"
  done
  [[ ! -e "$pack" && ! -e "$pack.part" ]] || die "PACK $name is there already; nothing overwritten"
  # Room for the pack, at most the state's size, and the decoded records, 4 times the du -sk of prometheus/wal and
  # jaeger/ they come from: on the dev host they were 2.2 and 1.9 times it in two packs (computed, follow -decode's
  # wal_bytes_decoded and table_bytes_decoded over du -sk's KiB, cells/results/2026-10-17-obs-offline.txt section 7i).
  need_kb="$(du -sk -- "${tops[@]/#/$OBS_DIR/}" | awk '{ s += $1 } END { print s + 0 }')"
  need_kb=$(( need_kb + 4 * $(du -sk -- "$OBS_DIR"/prometheus/wal "$OBS_DIR"/jaeger 2>/dev/null | awk '{ s += $1 } END { print s + 0 }') ))
  free_kb="$(df -Pk -- "$dir" | awk 'NR == 2 { print $4 }')"
  (( (free_kb - need_kb) / 1048576 >= OBS_MIN_FREE_GIB )) \
    || die "$(( free_kb / 1048576 )) GiB free where the pack goes, and the pack and the check need $(( need_kb / 1024 + 1 )) MiB, which would leave under OBS_MIN_FREE_GIB ($OBS_MIN_FREE_GIB)"
  stage="$OBS_DIR/pack.new"; rm -rf -- "$stage"; mkdir -p -- "$stage" || die "cannot make OBS_DIR/pack.new"
  PACK_LEFT=("$stage" "$pack.part"); trap 'rm -rf -- "${PACK_LEFT[@]}"' EXIT
  [[ -x bin/obs ]] || die "bin/obs is not built; make build builds it"
  sum="$(bin/obs follow -decode -dir "$OBS_DIR" -out "$stage/decoded" 2>"$stage/decode.err")" \
    || die "the check cannot read the state, so nothing is packed: $(sed 's/^follow: -decode: //; s/^laug · obs follow: //' "$stage/decode.err" | head -1)"
  host_words
  hits="$(deny_check "$OBS_DIR" "$stage/decoded" "$stage/deny")"; rc=$?
  rm -rf -- "$stage/decoded"
  case "$rc" in
    0) ;;
    1) echo "$hits" | sed "s/^/laug · $VERB: /" >&2
       die "$(plural "$(grep -c . <<<"$hits")" hit hits) above, by file and the line of the list that matched, or the kind of this host's word: the state holds a string a pack must not carry, so nothing is packed; no line shows the bytes" ;;
    *) die "the check failed, so nothing is packed: $hits" ;;
  esac
  nshapes="$(grep -vcE '^[[:space:]]*(#|$)|^except ' "$SHAPES")"; nexcept="$(grep -c '^except ' "$SHAPES")"
  if [[ -f "$WORDS" ]]; then
    read -r nplain nre < <(check/denylist.sh -list "$WORDS" -count 2>/dev/null)
    [[ "$nplain" =~ ^[0-9]+$ && "$nre" =~ ^[0-9]+$ ]] || die "check/denylist.sh does not count $WORDS, which the check read"
    words_here=true; nwords=$(( nplain + nre ))
  fi
  ( cd -- "$OBS_DIR" && find "${tops[@]}" -type f ! -path prometheus/lock ! -path prometheus/queries.active -print0 \
      | LC_ALL=C sort -z | xargs -0 -r sha256sum ) >"$stage/SHA256SUMS" || die "cannot hash the state"
  files="$(wc -l <"$stage/SHA256SUMS")"
  bytes="$(cd -- "$OBS_DIR" && find "${tops[@]}" -type f ! -path prometheus/lock ! -path prometheus/queries.active -printf '%s\n' | awk '{ s += $1 } END { print s + 0 }')"
  commit="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
  jq -n --arg made "$made" --arg commit "$commit" --argjson files "$files" --argjson bytes "$bytes" \
    --arg prom "$(versions_lines | awk '$2 == "prometheus" { print $2 " " $3 }')" --arg jaeger "$(versions_lines | awk '$2 == "jaeger" { print $2 " " $3 }')" \
    --argjson blocks "$(find "$OBS_DIR/prometheus" -mindepth 2 -maxdepth 2 -name meta.json | wc -l)" \
    --argjson decoded "$sum" --arg shapes "$SHAPES" --arg ssum "$(sha256sum -- "$SHAPES" | cut -d' ' -f1)" \
    --argjson ns "$nshapes" --argjson ne "$nexcept" --arg words "$WORDS" --argjson wh "$words_here" --argjson nw "$nwords" --argjson np "$nplain" --argjson nr "$nre" \
    --argjson hc "$(jq -nc '$ARGS.positional' --args "${HOST_CHECKED[@]}")" --argjson hs "$(jq -nc '$ARGS.positional' --args "${HOST_SKIPPED[@]}")" \
    '{pack: 1, made: $made, commit: $commit, files: $files, bytes: $bytes, store: {release: $prom, blocks: $blocks},
      traces: {release: $jaeger}, left_out: ["prometheus/lock", "prometheus/queries.active"],
      check: {shapes: {file: $shapes, sha256: $ssum, shapes: $ns, excepts: $ne}, words: {file: $words, here: $wh, words: $nw, plain: $np, re: $nr},
              host: {checked: $hc, not_checked: $hs},
              decoded: $decoded, hits: 0}}' >"$stage/PACK.json" || die "cannot write PACK.json"
  ( cd -- "$OBS_DIR" && LC_ALL=C nice -n 10 tar --create --gzip --sparse --sort=name --owner=0 --group=0 --numeric-owner \
      --exclude=prometheus/lock --exclude=prometheus/queries.active --file "$pack.part" \
      -C "$stage" PACK.json SHA256SUMS -C "$OBS_DIR" "${tops[@]}" ) || die "tar failed; nothing packed"
  list="$(LC_ALL=C tar --numeric-owner -tvzf "$pack.part")" || die "the pack does not list; nothing packed"
  bad="$(pack_listing_bad <<<"$list")"
  [[ -z "$bad" ]] || die "the pack lists a member that is not a file or a directory of the state, owned 0/0: $(head -1 <<<"$bad"); nothing packed"
  [[ "$(grep -c '^-' <<<"$list")" == $(( files + 2 )) ]] || die "the pack lists $(grep -c '^-' <<<"$list") files, not $(( files + 2 )); nothing packed"
  manifest="$(cat -- "$stage/PACK.json")"
  mv -- "$pack.part" "$pack" || die "cannot rename the pack"
  rm -rf -- "$stage"; trap - EXIT
  echo "laug · obs pack $name: $files files of the state, $(awk -v b="$bytes" 'BEGIN { printf "%.1f", b / 1048576 }') MiB as written, $(awk -v b="$(stat -c %s -- "$pack")" 'BEGIN { printf "%.1f", b / 1048576 }') MiB packed"
  echo "  holds     PACK.json, SHA256SUMS, prometheus/ ($(jq -r '.store.release' <<<"$manifest"), $(jq -r '.store.blocks' <<<"$manifest") blocks and the head's WAL) and $([[ " ${tops[*]} " == *" jaeger "* ]] && echo "jaeger/ ($(jq -r '.traces.release' <<<"$manifest"), Badger)" || echo "no jaeger/")"
  echo "  left out  prometheus/lock and prometheus/queries.active; OBS_DIR's releases, configs, Grafana, follow's gate paths, targets and logs"
  echo "  the check the $files files as written, and decoded $(plural "$(jq '.wal_records_decoded' <<<"$sum")" record records) of $(plural "$(jq '.wal_segments' <<<"$sum")" "WAL segment" "WAL segments") and $(plural "$(jq '.table_blocks' <<<"$sum")" block blocks) of $(plural "$(jq '.tables' <<<"$sum")" "trace table" "trace tables"), grepped for $nshapes shapes and $nexcept exceptions of $SHAPES, $( $words_here && echo "$(plural "$nplain" "plain line") and $(plural "$nre" "re: line") of $WORDS" || echo "no word list (${WORDS:-LAUG_DENYLIST unset}: none on this host)") and $( (( ${#HOST_CHECKED[@]} )) && echo "this host's $(and_list "${HOST_CHECKED[@]}")" || echo "none of this host's words"): 0 hits"
  (( ${#HOST_SKIPPED[@]} == 0 )) || echo "  not grepped  this host's $(printf '%s; ' "${HOST_SKIPPED[@]}" | sed 's/; $//')"
  echo "  where     $([[ -z "${PACK:-}" ]] && echo "OBS_DIR/packs/$name" || echo "-pack's file"), made $made by $commit; ${OBS:-bin/obs} view <it> reads it"
}

# view_rec <pack name> <made> <commit> [<started>]: run/view.json, bin/obs view's record; started null until both
# parts are up and checked.
view_rec() {
  jq -nc --arg p "$1" --arg m "$2" --arg c "$3" --arg s "${4:-}" \
    '{pack: $p, made: $m, commit: $c, started: (if $s == "" then null else $s end)}' >"$OBS_DIR/run/view.json.new" \
    && mv -f -- "$OBS_DIR/run/view.json.new" "$OBS_DIR/run/view.json"
}
# view_stop <why>: what this bin/obs view started, stopped, and its record removed once neither part runs; then exit 2.
view_stop() {
  trap - INT TERM HUP
  stop_parts "${STARTED[@]}"
  view_up >/dev/null || rm -f -- "$OBS_DIR/run/view.json"
  die "$1"
}

# view: bin/obs view <pack>, PACK. Refused while a gate is open, while any part of the stack or a view runs, with a
# PACK that is not bin/obs pack's (its listing: only files and directories, owned 0/0, under PACK.json, SHA256SUMS,
# prometheus/ and jaeger/; its PACK.json), with less free disk than it unpacks to above OBS_MIN_FREE_GIB, and when a
# file does not match SHA256SUMS; all of it before a release is fetched. It unpacks into OBS_DIR/view, renders
# Grafana's configs and boards there, writes a store config with no scrape job, and starts the store and Grafana as
# bin/obs up starts them, on 7730 and 7740: records in OBS_DIR/run, so bin/obs down and purge stop them, and the
# listener audit. run/view.json is written before either part starts, so a bin/obs view that ends before both are up,
# by a KILL, still leaves a view that bin/obs up and backfill refuse, obs status names and obs down stops; INT,
# TERM or HUP stop what it started and remove the record. Then the store's own targets API must hold no target and
# no scrape pool. The store waits up to 600 s for its WAL replay.
view() {
  local lock pack name list bad manifest need_kb free_kb V="$OBS_DIR/view" new="$OBS_DIR/view.new" part rc t0 oldest newest
  local head_max gates targets pools problems out
  local -A answered=()
  check_vars
  [[ -n "${PACK:-}" ]] || die "PACK, <pack>: a pack ${OBS:-bin/obs} pack wrote"
  [[ -f "$PACK" ]] || die "PACK: no such file"
  pack="$(cd -- "$(dirname -- "$PACK")" && pwd -P)/$(basename -- "$PACK")"; name="${pack##*/}"
  if lock="$(gate_open)"; then die "a gate runs: $lock; nothing started"; fi
  obs_dir_ready
  obs_lock
  load_up
  if (( ${#UP[@]} )); then
    for part in "${PARTS[@]}"; do [[ -z "${UP[$part]:-}" ]] || problems+="${problems:+, }$part (pid ${UP[$part]})"; done
    die "already up: ${problems:-}; ${OBS:-bin/obs} down first, since a view takes the store's and Grafana's ports"
  fi
  list="$(LC_ALL=C tar --numeric-owner -tvzf "$pack" 2>/dev/null)" || die "PACK does not list as a gzip tarball"
  bad="$(pack_listing_bad <<<"$list")"
  [[ -z "$bad" ]] || die "PACK holds a member that is not a file or a directory of a pack, owned 0/0: $(head -1 <<<"$bad" | head -c 80)"
  manifest="$(tar -xzOf "$pack" PACK.json 2>/dev/null)"
  jq -e '.pack == 1 and (.bytes | type) == "number"' >/dev/null 2>&1 <<<"$manifest" || die "PACK holds no PACK.json of ${OBS:-bin/obs} pack's"
  need_kb=$(( $(jq '.bytes' <<<"$manifest") / 1024 + 1 ))
  free_kb="$(df -Pk -- "$OBS_DIR" | awk 'NR == 2 { print $4 }')"
  (( (free_kb - need_kb) / 1048576 >= OBS_MIN_FREE_GIB )) \
    || die "$(( free_kb / 1048576 )) GiB free on OBS_DIR's filesystem; the pack unpacks to $(( need_kb / 1024 )) MiB, which would leave under OBS_MIN_FREE_GIB ($OBS_MIN_FREE_GIB)"
  rm -rf -- "$new"; mkdir -p -- "$new" || die "cannot make OBS_DIR/view.new"
  LC_ALL=C tar -xzf "$pack" -C "$new" --no-same-owner --no-same-permissions || { rm -rf -- "$new"; die "PACK did not unpack"; }
  ( cd -- "$new" && sha256sum --quiet --strict -c SHA256SUMS ) >/dev/null 2>&1 || { rm -rf -- "$new"; die "a file of PACK does not match its SHA256SUMS"; }
  [[ "$(cd -- "$new" && find . -type f ! -name PACK.json ! -name SHA256SUMS | wc -l)" == "$(wc -l <"$new/SHA256SUMS")" ]] \
    || { rm -rf -- "$new"; die "PACK holds a file its SHA256SUMS does not name"; }
  fetch prometheus grafana
  rm -rf -- "$V"; mv -- "$new" "$V" || die "cannot move the pack into OBS_DIR/view"
  # The view's configs: Grafana's as bin/obs up renders them, OBS_DIR read as OBS_DIR/view, the boards of this
  # checkout; and the store's, no scrape job at all.
  render _obs "$V/conf" "$V"
  cat >"$V/conf/prometheus.yml" <<'YML'
# bin/obs view's store: a pack read. No scrape job, no rule file, no alerting, no remote write: it measures nothing.
global:
  scrape_interval: 5s
scrape_configs: []
YML
  mkdir -p -- "$V/conf/boards" "$V/conf/grafana/provisioning/plugins" "$V/conf/grafana/provisioning/alerting" "$V/log" \
    "$V/grafana/plugins" "$OBS_DIR/run" || die "cannot make OBS_DIR/view's directories"
  if [[ -d _obs/stack ]]; then find _obs/stack -maxdepth 1 -type f -name '*.json' -exec cp -- {} "$V/conf/boards/" \; ; fi
  echo "view of $name, boards from $(git rev-parse --short HEAD 2>/dev/null || echo unknown)" >"$V/conf/COMMIT"
  ( umask 077; head -c 24 /dev/urandom | base64 | tr -d '/+=\n' >"$V/grafana/admin-password" ) || die "cannot write the view's admin password"
  out="$(env -i PATH=/usr/bin:/bin "$(obs_bin prometheus promtool)" check config "$V/conf/prometheus.yml" 2>&1)" \
    || die "promtool check config of the view's store config: $(tail -1 <<<"$out")"
  plan_check "$V/conf" prometheus grafana >"$V/.plan" || { cat "$V/.plan" >&2; die "$(grep -c . "$V/.plan") problem(s) with the view's listeners; nothing started"; }
  rm -f -- "$V/.plan"
  PART_CONF="$V/conf" PART_TSDB="$V/prometheus" PART_LOGS="$V/log"
  view_rec "$name" "$(jq -r '.made' <<<"$manifest")" "$(jq -r '.commit' <<<"$manifest")" || die "cannot write OBS_DIR/run/view.json; nothing started"
  trap 'view_stop "a signal came while the view started; what this run started is stopped"' INT TERM HUP
  for part in prometheus grafana; do
    t0="$(now)"
    start_part "$part"
    answer "$part" "$([[ "$part" == prometheus ]] && echo 600 || echo 60)"; rc=$?
    if (( rc != 0 )); then
      echo "laug · $VERB: $part $([[ $rc == 1 ]] && echo ended || echo "did not answer in time") after its start; the last lines of OBS_DIR/view/log/$part.log:" >&2
      tail -n 5 -- "$V/log/$part.log" >&2
      view_stop "$part did not answer; what this run started is stopped"
    fi
    answered[$part]="$(dt "$t0" "$(now)")"
  done
  if ! problems="$(audit_listeners)"; then
    echo "$problems" >&2
    view_stop "a part of the view listens where SECURITY.md's listener table does not put it; the view is stopped"
  fi
  targets="$(curl_local 'http://127.0.0.1:7730/api/v1/targets?state=any' 5 | jq -c '[(.data.activeTargets | length), (.data.droppedTargets | length)]' 2>/dev/null)"
  pools="$(curl_local http://127.0.0.1:7730/api/v1/scrape_pools 5 | jq -c '.data.scrapePools' 2>/dev/null)"
  if [[ "$targets" != "[0,0]" || "$pools" != "[]" ]]; then
    view_stop "the view's store has targets ${targets:-unread} (active, dropped) and scrape pools ${pools:-unread}, not none; the view is stopped"
  fi
  view_rec "$name" "$(jq -r '.made' <<<"$manifest")" "$(jq -r '.commit' <<<"$manifest")" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    || view_stop "cannot write OBS_DIR/run/view.json; the view is stopped"
  trap - INT TERM HUP
  load_up
  oldest="$(curl_local http://127.0.0.1:7730/metrics | awk '$1 == "prometheus_tsdb_lowest_timestamp" { printf "%d", $2 }')"
  head_max="$(curl_local http://127.0.0.1:7730/api/v1/status/tsdb 5 | jq -r '.data.headStats.maxTime // empty' 2>/dev/null)"
  newest="$( { cat "$V"/prometheus/*/meta.json 2>/dev/null | jq -r '.maxTime'; [[ "$head_max" =~ ^[0-9]+$ ]] && echo "$head_max"; } | sort -n | tail -1)"
  gates="$( [[ "$newest" =~ ^[0-9]+$ ]] && store_q 'count(group by (gate_id) (last_over_time(laug_gate_info[91d])))' "$(( newest / 1000 ))" | jq -r '.value[1]' | head -1)"
  echo "laug · obs view $name: the store and Grafana alone, no scrape job; it measures nothing"
  echo "  pack       made $(jq -r '.made' <<<"$manifest") by $(jq -r '.commit' <<<"$manifest"), $(jq -r '.files' <<<"$manifest") files, $(jq -r '.bytes / 1048576 * 10 | round / 10' <<<"$manifest") MiB; each file's sha256 as SHA256SUMS says; its check: $(jq -r 'def pl($n; $one): "\($n) \($one)\(if $n == 1 then "" else "s" end)"; .check | "\(.shapes.shapes) shapes, \(if (.words.here | not) then "no word list" elif .words.plain == null then "\(.words.words) words" else "\(pl(.words.plain; "plain line")) and \(pl(.words.re; "re: line"))" end), \(.hits) hits"' <<<"$manifest")"
  for part in prometheus grafana; do
    printf '  %-10s pid %-8s %-16s RSS %6s MiB  answered %s s after its start\n' "$part" "${UP[$part]}" "$(part_listeners "$part")" "$(rss_mib "${UP[$part]}")" "${answered[$part]}"
  done
  echo "  scrapes    none: the store's targets API holds 0 active and 0 dropped targets and 0 scrape pools"
  echo "  holds      $( [[ "$oldest" =~ ^[0-9]+$ ]] && echo "samples from $(date -u -d "@$(( oldest / 1000 ))" '+%Y-%m-%d %H:%M:%S') UTC" || echo "no sample")$( [[ "$newest" =~ ^[0-9]+$ ]] && echo " to $(date -u -d "@$(( newest / 1000 ))" '+%Y-%m-%d %H:%M:%S') UTC"); the records of ${gates:-0} gates"
  [[ ! "$oldest" =~ ^[0-9]+$ || ! "$newest" =~ ^[0-9]+$ ]] \
    || echo "  board      http://127.0.0.1:7740/d/laug-gates/?from=$oldest&to=$newest&timezone=utc (laug · gates over the pack's range); ${OBS:-bin/obs} find <gate id> gives one gate's"
  echo "  stop       ${OBS:-bin/obs} down; the copy stays in OBS_DIR/view until the next ${OBS:-bin/obs} view or ${OBS:-bin/obs} purge -remove-state"
}

# Sourced, as internal/orchestrator's TestDenylistReaders sources it to run deny_check alone, it defines and runs
# nothing more.
[[ "${BASH_SOURCE[0]}" == "$0" ]] || return 0
CMD="${1:-}"; VERB="obs $CMD"; [[ "$CMD" != test ]] || VERB="make obs-test"
# up, view, test, down and purge read this host's listeners (listeners, above). Without ss every port reads as free:
# up, view and test would pass a taken port, and down and purge would call one free. So each refuses up front, as a
# gate's preflight does. status changes nothing and shows what ss lists; find, backfill and pack read no port.
case "$CMD" in
  up|view|test|down|purge) command -v ss >/dev/null || die "no ss on PATH (iproute2): the stack's port checks read this host's listeners with it, and without it a taken port would read as free; nothing started, stopped or removed" ;;
esac
case "$CMD" in
  up) up ;;
  down) down_cmd ;;
  status) status ;;
  purge) purge ;;
  test) check ;;
  find) find_cmd ;;
  backfill) backfill ;;
  pack) pack ;;
  view) view ;;
  *) echo "usage: _obs/obs.sh up|down|status|purge|test|find|backfill|pack|view (run by ${OBS:-bin/obs} up, down, status, purge, find, backfill, pack and view, and make obs-test)" >&2; exit 2 ;;
esac
