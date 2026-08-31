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
#   scripts/mutation.sh [--baseline FILE] [--root DIR] [packages...]
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
#   0  every package met its recorded floor
#   1  a package scored below its floor
#   2  the run could not produce a number worth reading
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
while [ $# -gt 0 ]; do
  case "$1" in
    --root)     ROOT="$(cd "$2" && pwd)"; shift 2 ;;
    --baseline) BASELINE="$2"; shift 2 ;;
    *)          break ;;
  esac
done
[ -n "$BASELINE" ] || BASELINE="$ROOT/scripts/mutation-baseline.tsv"

# --- the working-tree guard -------------------------------------------------

GUARDED=0
if git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  DIRTY="$(git -C "$ROOT" status --porcelain 2>/dev/null)"
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
    if [ -n "$(git -C "$ROOT" status --porcelain 2>/dev/null)" ]; then
      echo "mutation: restoring the working tree after an in-place run" >&2
      git -C "$ROOT" checkout -- . 2>/dev/null || true
      git -C "$ROOT" clean -fdq -e '*.tsv' -- '*.go.tmp' 2>/dev/null || true
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

FAILED=0
MEASURED=0
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
  else
    printf '  %-12s %s  (floor %s)\n' "$pkg" "$SCORE" "$floor"
  fi
done < "$BASELINE"

[ "$FAILED" = 0 ] || {
  echo >&2
  echo "mutation: a package scored below its floor. Either a test was weakened," >&2
  echo "mutation: or new code arrived without assertions. Look at what survived." >&2
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
