#!/usr/bin/env bash
#
# Mutation testing: change one operator, run the tests, ask whether any test
# noticed.
#
# Coverage says which lines ran. It says nothing about which lines anything
# checked. On this module's first day, five defects were found by reverting a
# repair by hand and watching the tests stay green. This is that, automatically,
# one mutant at a time — and on its first real run it found four more: a
# swallowed base error in three places, five File methods whose injected error
# nothing asserted, an unchecked byte count on a failed read, and an allFrom
# allocator that would have refused from the first allocation regardless of the
# armed point.
#
# Usage:
#   scripts/mutation.sh [--baseline FILE] [--root DIR] [--survivors FILE] [packages...]
#   scripts/mutation.sh --print-keys FILE
#
# THE WORKING TREE IS THIS TOOL'S SCRATCH SPACE. go-mutesting rewrites source
# files IN PLACE, runs the tests, and restores them. So while this script runs,
# the checkout is not a stable thing to read, copy or commit.
#
# That is not theoretical. On 2026-08-31 a `git add -A` during a background run
# of this script committed fs/fault.go mid-mutation, with `return f.fail(...)`
# replaced by `_ = f.fail` — fault injection silently disabled on Truncate, in
# the package whose entire purpose is to find that defect. It was caught by a
# `gh` warning about an uncommitted change, and by nothing else: the tests
# passed, because the working tree was fine by then, and the mutation gate had
# already run.
#
# The guard below closes the window from both sides. It REFUSES to start on a
# dirty tree, which makes the restore unambiguous — any difference at exit is
# this script's debris and nothing else — and it restores on every exit path,
# including an interrupt.
#
# Exit codes:
#   0  every package met its recorded floor, and every survivor matched
#   1  a package scored below its floor, or its escaped mutants did not match
#      mutation-survivors.tsv as a multiset (an escaped mutant with no row is
#      a NEW SURVIVOR; a row with no matching escaped mutant is a STALE ROW)
#   2  the run could not produce a trustworthy number: a missing tool
#      (go-mutesting, jq, sha256sum), a missing baseline or survivors file,
#      a report.json that is missing or whose escapedCount does not match
#      the length of .escaped, or a survivors row with an empty reason
#
# On tool choice: gremlins reported nothing for every invocation tried in this
# module, and works fine in other repositories on the same Go version with the
# same target shapes. The variable was never isolated, so the cause is unknown
# and this file makes no claim about it. go-mutesting is kept on the comparison
# that does hold: it produces 17 mutants per package here where gremlins
# produces 2 or 3.
#
# Both tools fail silently in some configuration, which is why the selftest
# beside this file exists.
#
# Exit 2 is the important one. A tool that is missing, a baseline that is
# absent, or a test suite that already fails all produce a number that means
# nothing — and a failing baseline suite is the worst, because every mutant then
# counts as killed and the score reads 1.000.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BASELINE=""
SURVIVORS=""
PRINT_KEYS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --root)        ROOT="$(cd "$2" && pwd)"; shift 2 ;;
    --baseline)    BASELINE="$2"; shift 2 ;;
    --survivors)   SURVIVORS="$2"; shift 2 ;;
    --print-keys)  PRINT_KEYS="$2"; shift 2 ;;
    *)             break ;;
  esac
done
[ -n "$BASELINE" ] || BASELINE="$ROOT/scripts/mutation-baseline.tsv"
[ -n "$SURVIVORS" ] || SURVIVORS="$(dirname "$BASELINE")/mutation-survivors.tsv"

# --- a mutant's identity, and the report it is read from --------------------
#
# The key is (file, mutator, a hash of the hunk body) -- never the line
# number, because a line moves when code above it moves and the mutant does
# not. The hunk body: keep the diff lines that start with '-', '+' or a
# space, drop the '---'/'+++' header lines (the '@@' line is dropped for
# free, since it starts with neither), join with newlines, strip leading and
# trailing whitespace from the whole text. The key's hash is the first 16
# hex characters of that text's SHA-256. scratchpad/a5-compare.py's `hunk`
# function is the reference this matches.
#
# jq's @json, one escaped mutant per line, keeps a diff that contains any
# character -- including embedded newlines and tabs -- on one line of shell
# output, so it can be read back with `jq -r` per field instead of split on
# whitespace.
mutant_records() {
  local report="$1"
  jq -r '(.escaped // []).[] | [.mutator.originalFilePath, .mutator.mutatorName, .mutator.originalStartLine, .diff] | @json' "$report" |
  while IFS= read -r row; do
    local file mutator line diff body hash key hunk_b64
    file="$(printf '%s' "$row" | jq -r '.[0]')"
    mutator="$(printf '%s' "$row" | jq -r '.[1]')"
    line="$(printf '%s' "$row" | jq -r '.[2]')"
    diff="$(printf '%s' "$row" | jq -r '.[3]')"
    body="$(printf '%s\n' "$diff" | awk '
      /^---/ { next }
      /^\+\+\+/ { next }
      /^[-+ ]/ { print }
    ')"
    body="${body#"${body%%[![:space:]]*}"}"
    body="${body%"${body##*[![:space:]]}"}"
    hash="$(printf '%s' "$body" | sha256sum | cut -c1-16)"
    key="${file}:${mutator}:${hash}"
    hunk_b64="$(printf '%s' "$body" | base64 -w0)"
    printf '%s\t%s\t%s\t%s\t%s\n' "$key" "$file" "$line" "$mutator" "$hunk_b64"
  done
}

# A report.json this script did not just verify is not evidence about
# anything. escapedCount is go-mutesting's own claim about how many mutants
# escaped; .escaped is the list it printed. If they disagree, or the file is
# not there, neither the score already read nor any key computed from it can
# be trusted.
check_report_trustworthy() {
  local report="$1"
  if [ ! -f "$report" ]; then
    echo "mutation: '$report' is missing — the report cannot be trusted" >&2
    return 1
  fi
  local escaped_count escaped_len
  escaped_count="$(jq -r '.stats.escapedCount // 0' "$report" 2>/dev/null)"
  escaped_len="$(jq -r '(.escaped // []) | length' "$report" 2>/dev/null)"
  if [ -z "$escaped_count" ] || [ -z "$escaped_len" ] || [ "$escaped_count" != "$escaped_len" ]; then
    echo "mutation: '$report' says escapedCount=${escaped_count:-?} but .escaped has" >&2
    echo "mutation: ${escaped_len:-?} entries — the report cannot be trusted" >&2
    return 1
  fi
  return 0
}

# --print-keys FILE: read an already-captured report.json directly (no run,
# no baseline, no working tree involved) and print each escaped mutant's key
# and where (file:line mutator), tab-separated, one per line. Used to build
# survivors rows from a report captured elsewhere, and by the selftest to
# assert the key of a known diff.
if [ -n "$PRINT_KEYS" ]; then
  if ! command -v jq >/dev/null 2>&1; then
    echo "mutation: jq is not installed — refusing to compute keys" >&2
    exit 2
  fi
  if ! command -v sha256sum >/dev/null 2>&1; then
    echo "mutation: sha256sum is not installed — refusing to compute keys" >&2
    exit 2
  fi
  check_report_trustworthy "$PRINT_KEYS" || exit 2
  while IFS=$'\t' read -r key efile eline emutator _; do
    printf '%s\t%s:%s %s\n' "$key" "$efile" "$eline" "$emutator"
  done < <(mutant_records "$PRINT_KEYS")
  exit 0
fi

# --- the working-tree guard -------------------------------------------------

GUARDED=0
if git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  # ONLY Go files. The tool mutates .go source, so that is the hazard, and it
  # is the whole hazard. Keying on everything would fight this repository's own
  # documented workflow: every baseline row says "re-read this number after any
  # commit that adds code here", so an operator editing mutation-baseline.tsv
  # and re-running the gate is doing the right thing and must not be refused.
  # The selftest also writes a temporary baseline beside its fixtures.
  DIRTY="$(git -C "$ROOT" status --porcelain -- '*.go' 2>/dev/null)"
  if [ -n "$DIRTY" ]; then
    echo "mutation: the working tree has uncommitted changes, and this script" >&2
    echo "mutation: rewrites source files in place. Refusing to start, because" >&2
    echo "mutation: a run that fails or is interrupted could not then be told" >&2
    echo "mutation: apart from your own edits — and restoring would destroy them." >&2
    echo "mutation:" >&2
    echo "$DIRTY" | sed 's/^/mutation:   /' >&2
    echo "mutation:" >&2
    echo "mutation: commit or stash first. See the header of this file for the" >&2
    echo "mutation: commit that made this guard necessary." >&2
    exit 2
  fi
  GUARDED=1
  # Restore on EVERY exit path, including an interrupt. The refusal above is
  # what makes this safe: with a clean tree at entry, any difference now is
  # this script's own debris.
  restore_tree() {
    status=$?
    if [ -n "$(git -C "$ROOT" status --porcelain -- '*.go' 2>/dev/null)" ]; then
      echo "mutation: restoring the working tree after an in-place run" >&2
      git -C "$ROOT" checkout -- '*.go' 2>/dev/null || true
      find "$ROOT" -name '*.go.tmp' -not -path '*/.git/*' -delete 2>/dev/null || true
    fi
    exit $status
  }
  trap restore_tree EXIT INT TERM
else
  # A negative result must be reportable. Saying nothing here would let a run
  # outside a checkout look identical to a guarded one.
  echo "mutation: not a git checkout, so the working tree cannot be guarded" >&2
  echo "mutation: or restored. This script rewrites source files in place." >&2
fi

if ! command -v go-mutesting >/dev/null 2>&1; then
  echo "mutation: go-mutesting is not installed — refusing to report a pass" >&2
  echo "mutation: go install github.com/avito-tech/go-mutesting/cmd/go-mutesting@latest" >&2
  exit 2
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "mutation: jq is not installed — refusing to report a pass" >&2
  echo "mutation: the survivor-list check reads report.json with jq" >&2
  exit 2
fi
if ! command -v sha256sum >/dev/null 2>&1; then
  echo "mutation: sha256sum is not installed — refusing to report a pass" >&2
  echo "mutation: the survivor-list check hashes each hunk with sha256sum" >&2
  exit 2
fi
if [ ! -s "$BASELINE" ]; then
  echo "mutation: '$BASELINE' is missing or empty — refusing to report a pass" >&2
  exit 2
fi

# A mutant is only meaningful against a suite that passes. When the baseline
# suite already fails, every mutant is recorded as killed and the score is 1.0.
if ! (cd "$ROOT" && go test -count=1 ./... >/dev/null 2>&1); then
  echo "mutation: the test suite already fails, so every mutant would count as" >&2
  echo "mutation: killed and the score would read 1.000 — refusing to report it" >&2
  exit 2
fi

# Every package in the module must appear in the baseline. Without this, a new
# package escapes the gate entirely and the gate still reports a pass: it
# measured exactly what it was told to, and what it was told is now wrong.
#
# This is the "testing a target that resembles the gate's target is not testing
# the gate" problem, in the form it takes here. The gate's target list is data,
# and data drifts from the thing it describes.
LISTED="$(sed 's/\t.*//' "$BASELINE" | grep -v '^#' | grep -v '^$' \
  | sed 's|^\./||; s|/$||; s|^$|.|' | sort -u)"
ACTUAL="$(cd "$ROOT" && go list -f '{{.Dir}}' ./... 2>/dev/null \
  | sed "s|^$ROOT||; s|^/||; s|^$|.|" | sort -u)"
MISSING="$(comm -13 <(printf '%s\n' "$LISTED") <(printf '%s\n' "$ACTUAL"))"
if [ -n "$MISSING" ]; then
  echo "mutation: these packages are in the module and not in the baseline:" >&2
  printf '  %s\n' $MISSING >&2
  echo "mutation: the gate would report a pass having measured nothing about" >&2
  echo "mutation: them — refusing. Add a row, or say why the package is exempt." >&2
  exit 2
fi

# The remaining arguments are a package filter, and until 2026-08-31 nothing
# read them. The usage line above has documented [packages...] since this
# script was written; the flag loop left them in "$@" and no later line
# referenced it, so the run loop below read only the baseline.
#
# MEASURED BEFORE THE REPAIR: `mutation.sh --root testdata/thin --baseline
# ... ./no-such-package/` exited 1 having measured ./ instead. A caller asked
# for one package, got a different one, and no output said so. That is the
# family this repository keeps finding: the tool is correct about the work it
# did, and the work is not the work requested.
#
# An unknown name exits 2 rather than measuring nothing. A filter that matched
# no row would run zero packages and report "every package met its floor",
# which is the false pass this whole file exists to refuse.
norm() { printf '%s' "$1" | sed 's|^\./||; s|/$||; s|^$|.|'; }

WANTED=""
if [ $# -gt 0 ]; then
  LISTED_PKGS="$(sed 's/\t.*//' "$BASELINE" | grep -v '^#' | grep -v '^$')"
  for arg in "$@"; do
    want="$(norm "$arg")"
    found=0
    for listed in $LISTED_PKGS; do
      if [ "$(norm "$listed")" = "$want" ]; then found=1; break; fi
    done
    if [ "$found" = 0 ]; then
      echo "mutation: '$arg' is not a package in '$BASELINE' — refusing to run" >&2
      echo "mutation: a filter that matches no row measures nothing and would" >&2
      echo "mutation: still print that every package met its floor." >&2
      exit 2
    fi
    WANTED="$WANTED $want"
  done
fi

# A list file that was not read cannot be vouched for, the same refusal the
# baseline gets above. One file covers every package this baseline lists, so
# this is checked once, not per package.
if [ ! -f "$SURVIVORS" ]; then
  echo "mutation: '$SURVIVORS' is missing — refusing to report a pass" >&2
  echo "mutation: a list file that was not read cannot be vouched for." >&2
  exit 2
fi

FAILED=0
MEASURED=0
FAIL_SUMMARY=()
while IFS=$'\t' read -r pkg floor rest; do
  case "$pkg" in ''|'#'*) continue ;; esac
  [ -n "${floor:-}" ] || continue
  if [ -n "$WANTED" ]; then
    case " $WANTED " in
      *" $(norm "$pkg") "*) ;;
      *) continue ;;
    esac
  fi
  MEASURED=$((MEASURED + 1))

  OUT="$(cd "$ROOT" && go-mutesting "$pkg" 2>&1)"
  SCORE="$(printf '%s\n' "$OUT" | sed -n 's/.*mutation score is \([0-9.]*\).*/\1/p' | tail -1)"
  if [ -z "$SCORE" ]; then
    echo "mutation: $pkg produced no score — refusing to report a pass" >&2
    printf '%s\n' "$OUT" | tail -3 >&2
    exit 2
  fi

  # A mutant that was never evaluated is not a result. The graphdb session hit
  # the sharp version of this with a different tool: all 88 mutants of one
  # package timed out, and it reported 100% efficacy from the single mutant it
  # managed to run. Two identical invocations gave 100% and 0%.
  SUMMARY="$(printf '%s\n' "$OUT" | sed -n 's/.*(\(.*\)total is \([0-9]*\)).*/\1|\2/p' | tail -1)"
  TOTAL="${SUMMARY##*|}"
  SKIPPED="$(printf '%s\n' "$OUT" | sed -n 's/.*, \([0-9]*\) skipped.*/\1/p' | tail -1)"
  if [ -z "$TOTAL" ] || [ "$TOTAL" = 0 ]; then
    echo "mutation: $pkg produced no mutants at all, so the score above is not one" >&2
    echo "mutation: a run that generates nothing looks exactly like a package" >&2
    echo "mutation: with nothing to find — refusing to report a pass" >&2
    exit 2
  fi
  if [ -n "${SKIPPED:-}" ] && awk -v s="$SKIPPED" -v t="$TOTAL" 'BEGIN { exit !(s > t / 10) }'; then
    echo "mutation: $pkg skipped $SKIPPED of $TOTAL mutants. Those were never" >&2
    echo "mutation: evaluated, so the score above is not one — refusing it" >&2
    exit 2
  fi

  if awk -v s="$SCORE" -v f="$floor" 'BEGIN { exit !(s + 1e-9 < f) }'; then
    printf '  %-12s %s  BELOW the recorded floor of %s\n' "$pkg" "$SCORE" "$floor"
    FAILED=1
    FAIL_SUMMARY+=("$pkg  scored $SCORE, below the recorded floor of $floor")
  else
    printf '  %-12s %s  (floor %s)\n' "$pkg" "$SCORE" "$floor"
  fi

  # The survivor list: does the recorded identity of every known survivor
  # match, as a multiset, the identity of every mutant this run's report says
  # escaped? Two escaped mutants can share a key when two places in one file
  # have the same code and context, so counts matter and not just membership.
  REPORT="$ROOT/report.json"
  check_report_trustworthy "$REPORT" || exit 2

  ESCAPED_KEYS=(); ESCAPED_FILE=(); ESCAPED_LINE=(); ESCAPED_MUTATOR=(); ESCAPED_HUNK_B64=()
  while IFS=$'\t' read -r ekey efile eline emutator ehunkb64; do
    ESCAPED_KEYS+=("$ekey")
    ESCAPED_FILE+=("$efile")
    ESCAPED_LINE+=("$eline")
    ESCAPED_MUTATOR+=("$emutator")
    ESCAPED_HUNK_B64+=("$ehunkb64")
  done < <(mutant_records "$REPORT")

  ROW_KEYS=(); ROW_WHERE=(); ROW_REASON=()
  while IFS=$'\t' read -r rpkg rkey rwhere rreason; do
    case "$rpkg" in ''|'#'*) continue ;; esac
    [ "$(norm "$rpkg")" = "$(norm "$pkg")" ] || continue
    if [ -z "$rreason" ]; then
      echo "mutation: '$SURVIVORS' has a row for $pkg (key '$rkey') with an" >&2
      echo "mutation: empty reason — a survivor with no reason is a survivor" >&2
      echo "mutation: nobody read. Refusing to report a pass." >&2
      exit 2
    fi
    ROW_KEYS+=("$rkey")
    ROW_WHERE+=("$rwhere")
    ROW_REASON+=("$rreason")
  done < "$SURVIVORS"

  declare -A KEY_QUEUE=()
  for ridx in "${!ROW_KEYS[@]}"; do
    rk="${ROW_KEYS[$ridx]}"
    KEY_QUEUE["$rk"]="${KEY_QUEUE[$rk]:-}${KEY_QUEUE[$rk]:+ }$ridx"
  done
  CONSUMED=()
  for ridx in "${!ROW_KEYS[@]}"; do CONSUMED[$ridx]=0; done

  PKG_SURV_FAILED=0
  for eidx in "${!ESCAPED_KEYS[@]}"; do
    ek="${ESCAPED_KEYS[$eidx]}"
    queue="${KEY_QUEUE[$ek]:-}"
    if [ -n "$queue" ]; then
      match="${queue%% *}"
      if [ "$match" = "$queue" ]; then qrest=""; else qrest="${queue#* }"; fi
      KEY_QUEUE["$ek"]="$qrest"
      CONSUMED[$match]=1
    else
      echo "mutation: NEW SURVIVOR in $pkg" >&2
      echo "mutation:   ${ESCAPED_FILE[$eidx]}:${ESCAPED_LINE[$eidx]}  ${ESCAPED_MUTATOR[$eidx]}" >&2
      printf '%s' "${ESCAPED_HUNK_B64[$eidx]}" | base64 -d | sed 's/^/mutation:     /' >&2
      printf '\n' >&2
      where="${ESCAPED_FILE[$eidx]}:${ESCAPED_LINE[$eidx]} ${ESCAPED_MUTATOR[$eidx]}"
      echo "mutation:   $pkg	$ek	$where	TODO: read it" >&2
      PKG_SURV_FAILED=1
    fi
  done

  for ridx in "${!ROW_KEYS[@]}"; do
    if [ "${CONSUMED[$ridx]:-0}" = 0 ]; then
      echo "mutation: STALE ROW in $pkg" >&2
      echo "mutation:   ${ROW_KEYS[$ridx]}  ${ROW_WHERE[$ridx]}" >&2
      echo "mutation:   a test now kills it; remove the row or say what changed" >&2
      PKG_SURV_FAILED=1
    fi
  done
  unset KEY_QUEUE

  if [ "$PKG_SURV_FAILED" = 1 ]; then
    FAILED=1
    FAIL_SUMMARY+=("$pkg  the survivor list does not match the escaped mutants — see NEW SURVIVOR / STALE ROW above")
  fi
done < "$BASELINE"

[ "$FAILED" = 0 ] || {
  echo >&2
  echo "mutation: at least one package failed. A low score means a weakened" >&2
  echo "mutation: test or new code without assertions. A survivor mismatch" >&2
  echo "mutation: means the recorded list drifted from what this run found." >&2
  echo "mutation:" >&2
  echo "mutation: packages that failed, and why:" >&2
  for line in "${FAIL_SUMMARY[@]}"; do
    echo "mutation:   $line" >&2
  done
  exit 1
}
# A run that measured nothing must not report a pass. The filter is validated
# above so this should be unreachable, and it stays because "unreachable" is a
# property of today's code that no test protects.
if [ "$MEASURED" = 0 ]; then
  echo "mutation: no package was measured — refusing to report a pass" >&2
  exit 2
fi

if [ -n "$WANTED" ]; then
  echo "mutation: $MEASURED of the baseline's packages met their floor (filtered:$WANTED)"
else
  echo "mutation: every package met its floor"
fi
