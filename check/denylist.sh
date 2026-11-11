#!/usr/bin/env bash
# The deny list's reader: its pass over a tree, and its rules for bin/obs pack's word check (_obs/obs.sh,
# deny_check). The list stays outside the tracked tree, so no tracked file names a line of it. It is read by
# the rule internal/denylist's package comment gives, which laug orchestrator record reads it by too: a line a rule,
# named by its own line in the file ("denylist line N"); a trailing \r dropped; an empty line, one of white space, or
# one whose first byte that is not white space is #, skipped; a line that starts "re:" a regular expression in the
# subset grep -E, Go and Python read alike (re_subset), any other a fixed string; both matched with ASCII letters
# folded, a line at a time, byte by byte (LC_ALL=C). A re: line outside the subset, one that matches the empty string,
# or a byte-order mark before line 1 stops the reader with exit 2 and names its line, never its text.
#
#   check/denylist.sh [-list <file>] [<dir>]          the pass over the tree <dir>, the checkout this script is in
#   check/denylist.sh -list <file> -rules <new dir>   the rules as files, for a reader that greps its own data
#   check/denylist.sh -list <file> -count             the list's plain and re: lines, counted
#
# The pass reads each file git sees in <dir>, tracked and untracked but not ignored, binary files as bytes; a directory
# that is not a git work tree, such as a git archive's, file by file less what its .gitignore files leave out (git grep
# --no-index --exclude-standard). git lists the files and GNU grep reads them, as bin/obs
# pack's check does: git grep's . never matches a 0 byte, and GNU grep's does, as Go's and Python's do. Each hit
# prints one line, "<path>:<line>: denylist line <N>", never the text: neither the line's nor the list's. File names
# are read too, one a line: a name that matches prints "file name <K> of <files>: denylist line <N>", and a hit in
# that file "file <K>:<line>", K the file's place among the files git sees, in byte order, so no name with a word in it
# is printed. A list that is not there, or none named, is said and passes: a fresh clone has none, and CI, and a
# worktree holds none of its own. One that is there but is not a regular file, such as a directory or a link to
# nothing, is exit 2. -list is LAUG_DENYLIST's value by default, none when that is unset or empty, read from where this
# script runs, before it changes to <dir>. Exit 0 no hit, 1 a hit, 2 usage, a refused line, or a grep that failed.
#
# -rules makes <new dir>, mode 700, and writes in it each rule's text alone in <N>, the plain lines in plain and the
# re: lines in re, one a line, and rules, "<N> p" or "<N> r" a line in the list's order; then prints "<plain> <re>".
# On a refused line it removes <new dir> again. -count prints that line alone. Both name a refused line on stderr with
# no prefix, for the caller to give its own.
set +x # an xtrace from the environment, SHELLOPTS's or BASH_ENV's, would print each rule's text
set -uo pipefail
export LC_ALL=C
P="laug · denylist:"
usage="usage: check/denylist.sh [-list <file>] [<dir>] | -list <file> -rules <new dir> | -list <file> -count"
WHY_SUBSET="a re: line outside the subset grep -E, Go and Python read alike, or one that does not compile"
WHY_EMPTY="a re: line that matches the empty string"
WHY_BOM="a byte-order mark"

list=${LAUG_DENYLIST:-} dir="" rules="" count=false
while (( $# )); do
  case "$1" in
    -list|-rules) (( $# > 1 )) || { echo "$P $1 needs a value; $usage" >&2; exit 2; }
                  if [[ "$1" == -list ]]; then list="$2"; else rules="$2"; fi; shift ;;
    -count) count=true ;;
    -h|-help|--help) echo "$usage"; exit 0 ;;
    -*) echo "$P $1: no such flag; $usage" >&2; exit 2 ;;
    *) [[ -z "$dir" ]] || { echo "$P one tree at a time; $usage" >&2; exit 2; }; dir="$1" ;;
  esac
  shift
done
reader=false   # -rules or -count: a reader's helper, not the pass
if [[ -n "$rules" ]] || $count; then
  [[ -z "$dir" && ( -z "$rules" || "$count" == false ) ]] || { echo "$P -rules, -count and a tree go alone; $usage" >&2; exit 2; }
  reader=true
fi
pl() { if (( $1 == 1 )); then echo "$1 $2"; else echo "$1 $3"; fi; }   # pl <n> <one> <more>

# re_subset <pattern>: true when the pattern is in the subset. The same reader as internal/denylist's subset, in Go,
# check for check and in the same order, so the two take and refuse the same lines (internal/orchestrator's
# TestDenylistSubsetBash). RP is the pattern and RI the place in it, RDEPTH the groups open there; each part sets
# RPROD, the largest product of the counts inside it, and an atom RANCHOR, a quantifier RQUANT and RCOUNT, its largest
# count. MAX_BYTES and MAX_DEPTH keep the recursion short: 800 nested groups crashed bash.
SPECIALS='.[]()*+?{}|^$\'
PRINTABLE='^[ -~]+$'
COUNT_RE='^\{(0|[1-9][0-9]{0,2})(,(0|[1-9][0-9]{0,2})?)?\}'
KINDS='^([0-9][0-9]|[a-z][a-z]|[A-Z][A-Z])$'
MAX_BYTES=256 MAX_DEPTH=32
re_subset() {
  RP="$1" RI=0 RN=${#1} RDEPTH=0
  (( RN <= MAX_BYTES )) || return 1
  [[ "$RP" =~ $PRINTABLE ]] || return 1
  re_alternation || return 1
  (( RI == RN ))
}
re_alternation() { # branches joined by |, up to a ) or the end
  local most=1
  while :; do
    re_branch || return 1
    (( RPROD <= most )) || most=$RPROD
    [[ "${RP:RI:1}" == "|" ]] || { RPROD=$most; return 0; }
    RI=$(( RI + 1 ))
  done
}
re_branch() { # one or more atoms, each with its quantifier, up to a |, a ) or the end
  local most=1 pieces=0 c inner anchor
  while (( RI < RN )); do
    c="${RP:RI:1}"
    [[ "$c" != "|" && "$c" != ")" ]] || break
    re_atom || return 1
    inner=$RPROD anchor=$RANCHOR
    re_quantifier || return 1
    if (( RQUANT )); then (( ! anchor )) || return 1; inner=$(( inner * RCOUNT )); fi
    (( inner <= 1000 )) || return 1
    (( inner <= most )) || most=$inner
    pieces=$(( pieces + 1 ))
  done
  RPROD=$most
  (( pieces > 0 ))
}
re_atom() {
  local c="${RP:RI:1}" e
  case "$c" in
    "(") RI=$(( RI + 1 )) RDEPTH=$(( RDEPTH + 1 ))
         (( RDEPTH <= MAX_DEPTH )) || return 1
         [[ "${RP:RI:1}" != "?" ]] || return 1
         re_alternation || return 1
         [[ "${RP:RI:1}" == ")" ]] || return 1
         RI=$(( RI + 1 )) RDEPTH=$(( RDEPTH - 1 )) RANCHOR=0 ;;
    "[") re_class || return 1
         RPROD=1 RANCHOR=0 ;;
    "\\") e="${RP:RI+1:1}"
          [[ -n "$e" && "$SPECIALS" == *"$e"* ]] || return 1
          RI=$(( RI + 2 )) RPROD=1 RANCHOR=0 ;;
    "^"|"$") RI=$(( RI + 1 )) RPROD=1 RANCHOR=1 ;;
    "*"|"+"|"?"|"{") return 1 ;;
    *) RI=$(( RI + 1 )) RPROD=1 RANCHOR=0 ;;
  esac
}
re_quantifier() {
  local c="${RP:RI:1}" lo hi
  RQUANT=0 RCOUNT=1
  case "$c" in
    "*"|"+"|"?") RI=$(( RI + 1 )) ;;
    "{") [[ "${RP:RI}" =~ $COUNT_RE ]] || return 1
         lo="${BASH_REMATCH[1]}" hi="${BASH_REMATCH[3]}" RCOUNT="${BASH_REMATCH[1]}"
         if [[ -n "$hi" ]]; then (( hi >= lo )) || return 1; RCOUNT=$hi; fi
         (( lo <= 255 && RCOUNT <= 255 )) || return 1
         RI=$(( RI + ${#BASH_REMATCH[0]} )) ;;
    *) return 0 ;;
  esac
  RQUANT=1
  c="${RP:RI:1}"
  [[ "$c" != "*" && "$c" != "+" && "$c" != "?" && "$c" != "{" ]]
}
re_class() { # a bracket class, its [ first
  local start c next hi
  RI=$(( RI + 1 ))
  [[ "${RP:RI:1}" != "^" ]] || RI=$(( RI + 1 ))
  [[ "${RP:RI:1}" != ":" ]] || return 1
  start=$RI
  while (( RI < RN )); do
    c="${RP:RI:1}" next="${RP:RI+1:1}"
    if [[ "$c" == "]" ]]; then
      (( RI > start )) || return 1
      RI=$(( RI + 1 )); return 0
    elif [[ "$c" == "\\" || "$c" == "[" ]]; then return 1
    elif [[ "$c" == "-" ]]; then
      (( RI == start )) || [[ "$next" == "]" ]] || return 1
      RI=$(( RI + 1 ))
    elif [[ "$next" == "-" && -n "${RP:RI+2:1}" && "${RP:RI+2:1}" != "]" ]]; then
      hi="${RP:RI+2:1}"
      [[ "$c$hi" =~ $KINDS && ! "$c" > "$hi" ]] || return 1
      RI=$(( RI + 3 ))
    elif [[ ( "$c" == "&" || "$c" == "~" || "$c" == "|" ) && "$next" == "$c" ]]; then return 1
    else RI=$(( RI + 1 )); fi
  done
  return 1
}

# The scratch directory, mode 700, holds the rules while the script runs, and goes on any exit, a signal's too.
scratch="$(mktemp -d)" || { echo "$P no scratch directory" >&2; exit 2; }
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT; trap 'exit 143' TERM; trap 'exit 129' HUP

# read_list <dir or "">: the list's rules, by the rule, counted in NPLAIN and NRE; with a dir, written into it as
# -rules writes them. On a refused line WHY names it, and 2. A re: line reaches grep through a pipe or a file of a
# directory of mode 700, never an argument, which ps would show.
read_list() {
  local out="$1" n=0 line p kind
  NPLAIN=0 NRE=0 WHY=""
  [[ -z "$out" ]] || { : >"$out/plain"; : >"$out/re"; : >"$out/rules"; }
  while IFS= read -r line || [[ -n "$line" ]]; do
    n=$(( n + 1 ))
    (( n > 1 )) || [[ "$line" != $'\xef\xbb\xbf'* ]] || { WHY="denylist line 1: $WHY_BOM"; return 2; }
    line="${line%$'\r'}"
    [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
    if [[ "$line" == re:* ]]; then
      p="${line#re:}" kind=re
      [[ -n "$p" ]] || { WHY="denylist line $n: $WHY_EMPTY"; return 2; }
      re_subset "$p" || { WHY="denylist line $n: $WHY_SUBSET"; return 2; }
      grep -E -i -f <(printf '%s\n' "$p") </dev/null >/dev/null 2>&1
      (( $? != 2 )) || { WHY="denylist line $n: $WHY_SUBSET"; return 2; }
      ! printf '\n' | grep -q -E -i -f <(printf '%s\n' "$p") 2>/dev/null || { WHY="denylist line $n: $WHY_EMPTY"; return 2; }
      NRE=$(( NRE + 1 ))
    else
      p="$line" kind=plain NPLAIN=$(( NPLAIN + 1 ))
    fi
    [[ -n "$out" ]] || continue
    printf '%s\n' "$p" >"$out/$n"
    printf '%s\n' "$p" >>"$out/$kind"
    printf '%s %s\n' "$n" "${kind:0:1}" >>"$out/rules"
  done <"$list"
}

pre="$P "; ! $reader || pre=""
if [[ -z "$list" ]]; then
  $reader && { echo "-rules and -count need a list: -list <file>, or LAUG_DENYLIST" >&2; exit 2; }
  echo "$P no deny list named (LAUG_DENYLIST, or -list), so no private word was checked"
  exit 0
fi
if [[ ! -f "$list" ]]; then
  [[ ! -e "$list" && ! -L "$list" ]] || { echo "$pre$list is not a file" >&2; exit 2; }
  $reader && { echo "no $list" >&2; exit 2; }
  # A linked worktree's .git names its own git directory, apart from the main checkout's; the main checkout's list
  # is not read there unless LAUG_DENYLIST or -list names it by an absolute path.
  if [[ "$list" != /* ]] && gd="$(git rev-parse --path-format=absolute --git-dir 2>/dev/null)" &&
     [[ "$gd" != "$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null)" ]]; then
    echo "$P no $list in this worktree, so no private word was checked: an absolute path in LAUG_DENYLIST or -list checks one"
  else
    echo "$P no $list on this host, so no private word was checked"
  fi
  exit 0
fi
[[ -r "$list" ]] || { echo "$pre$list does not read" >&2; exit 2; }
if $reader; then
  if [[ -n "$rules" ]]; then
    mkdir -m 700 -- "$rules" 2>/dev/null || { echo "cannot make $rules, a new directory for the rules" >&2; exit 2; }
    read_list "$rules" || { echo "$WHY" >&2; rm -rf -- "$rules"; exit 2; }
  else
    read_list "" || { echo "$WHY" >&2; exit 2; }
  fi
  echo "$NPLAIN $NRE"; exit 0
fi
mkdir -m 700 -- "$scratch/list" || { echo "$P no scratch directory" >&2; exit 2; }
read_list "$scratch/list" || { echo "$P $WHY" >&2; exit 2; }
if (( NPLAIN + NRE == 0 )); then echo "$P $list holds no patterns, so no private word was checked"; exit 0; fi

where="${dir:-the checkout}"; [[ -n "$dir" ]] || dir="$(dirname -- "$0")/.."
cd -- "$dir" 2>/dev/null || { echo "$P $dir: no such directory" >&2; exit 2; }
if [[ "$(git rev-parse --is-inside-work-tree 2>/dev/null)" == true ]]; then mode=(--untracked); else mode=(--no-index --exclude-standard); fi

# The files git sees: those with a line (-l) and the empty ones (-L), regular files only, each ended by a 0 byte, in
# byte order. File K is the K-th of them, and line K of names, where a name's newlines read as spaces.
: >"$scratch/seen"
for l in -l -L; do
  git grep "${mode[@]}" -a "$l" -z -e '' -- . >>"$scratch/seen" 2>/dev/null; rc=$?
  (( rc <= 1 )) || { echo "$P git grep failed (exit $rc) listing the files of $where" >&2; exit 2; }
done
sort -z -u -- "$scratch/seen" >"$scratch/files" || { echo "$P sort failed on the files of $where" >&2; exit 2; }
mapfile -d '' -t ALL <"$scratch/files"
files=${#ALL[@]}
tr '\n\0' ' \n' <"$scratch/files" >"$scratch/names"

# grep_files <in> <out> <grep options>...: GNU grep -a -i over the files <in> names, 0-byte ended, its lines in <out>.
# xargs exits 123 both when a grep found nothing and when one failed, so a failure is any other exit, or stderr that
# is not empty. No grep error is shown: one can quote a file's name.
grep_files() {
  local in="$1" out="$2" rc; shift 2
  xargs -0 -r grep -a -i "$@" -- <"$in" >"$out" 2>"$scratch/err"; rc=$?
  (( rc == 0 || rc == 123 )) && [[ ! -s "$scratch/err" ]]
}

# One grep of the plain lines and one of the re: lines name the files and the names that hold a hit; in those alone
# each rule runs on its own, so each hit names the line of the list that made it. grep -n -H -Z writes "<path>\0<line>:
# <the line's text>\n"; the text is read past and dropped.
declare -A K=() NAMED=() NAMES=()
: >"$scratch/hits"
for kind in plain re; do
  : >"$scratch/in.$kind"
  [[ -s "$scratch/list/$kind" ]] || continue
  if [[ "$kind" == plain ]]; then how=-F; else how=-E; fi
  grep_files "$scratch/files" "$scratch/in.$kind" -l -Z "$how" -f "$scratch/list/$kind" \
    || { echo "$P grep failed on the $kind lines of $list in $where" >&2; exit 2; }
  grep -q -a -i "$how" -f "$scratch/list/$kind" -- "$scratch/names" 2>/dev/null; rc=$?
  (( rc <= 1 )) || { echo "$P grep failed on the $kind lines of $list over the file names of $where" >&2; exit 2; }
  (( rc == 1 )) || NAMES[$kind]=1
done
if [[ -s "$scratch/in.plain" || -s "$scratch/in.re" ]]; then
  for i in "${!ALL[@]}"; do K["${ALL[i]}"]=$(( i + 1 )); done
fi
while read -r n kind; do
  if [[ "$kind" == p ]]; then how=-F kind=plain; else how=-E kind=re; fi
  if [[ -s "$scratch/in.$kind" ]]; then
    grep_files "$scratch/in.$kind" "$scratch/m" -n -H -Z "$how" -f "$scratch/list/$n" \
      || { echo "$P grep failed on denylist line $n in $where" >&2; exit 2; }
    while IFS= read -r -d '' path && IFS= read -r rest; do
      printf '%s\t%s\t%s\n' "${K["$path"]}" "${rest%%:*}" "$n" >>"$scratch/hits"
    done <"$scratch/m"
  fi
  if [[ -n "${NAMES[$kind]:-}" ]]; then
    grep -a -i -n "$how" -f "$scratch/list/$n" -- "$scratch/names" >"$scratch/m" 2>/dev/null; rc=$?
    (( rc <= 1 )) || { echo "$P grep failed on denylist line $n over the file names of $where" >&2; exit 2; }
    while IFS= read -r rest; do
      printf '%s\t0\t%s\n' "${rest%%:*}" "$n" >>"$scratch/hits"; NAMED["${rest%%:*}"]=1
    done <"$scratch/m"
  fi
done <"$scratch/list/rules"
rm -f -- "$scratch/m"

# A hit at line 0 is the file's name. A file whose name holds a match, or a newline or a tab, is shown by its place.
if [[ -s "$scratch/hits" ]]; then
  sort -t$'\t' -k1,1n -k2,2n -k3,3n "$scratch/hits" | while IFS=$'\t' read -r k at n; do
    path="${ALL[k-1]}"
    if (( at == 0 )); then echo "$P file name $k of $files: denylist line $n"
    elif [[ -n "${NAMED[$k]:-}" || "$path" == *[$'\n\t']* ]]; then echo "$P file $k:$at: denylist line $n"
    else echo "$P $path:$at: denylist line $n"; fi
  done
  echo "$P $(pl "$(wc -l <"$scratch/hits")" "hit" "hits") above, each a file, its line and the line of $list that matched, never the text: take the text out, or edit that line of the list" >&2
  (( ${#NAMED[@]} == 0 )) || echo "$P a file whose name matches is shown by its place K among the $files files git lists, in byte order, never by its name: rename it, or edit that line of the list" >&2
  exit 1
fi
echo "$P $(pl "$NPLAIN" "plain line" "plain lines") and $(pl "$NRE" "re: line" "re: lines") of $list over $(pl "$files" file files): 0 hits"
