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
# Exit codes:
#   0  every package met its recorded floor
#   1  a package scored below its floor
#   2  the run could not produce a number worth reading
#
# On tool choice: gremlins is not broken here, contrary to an earlier claim in
# this repository's history. It reports "No results to report" when the target
# path is "./"-prefixed, and every invocation tried used that form. go-mutesting
# is kept because it produces 17 mutants per package where gremlins produces 2
# or 3. Both fail silently in some configuration, which is why the selftest
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

FAILED=0
while IFS=$'\t' read -r pkg floor rest; do
  case "$pkg" in ''|'#'*) continue ;; esac
  [ -n "${floor:-}" ] || continue

  OUT="$(cd "$ROOT" && go-mutesting "$pkg" 2>&1)"
  SCORE="$(printf '%s\n' "$OUT" | sed -n 's/.*mutation score is \([0-9.]*\).*/\1/p' | tail -1)"
  if [ -z "$SCORE" ]; then
    echo "mutation: $pkg produced no score — refusing to report a pass" >&2
    printf '%s\n' "$OUT" | tail -3 >&2
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
echo "mutation: every package met its floor"
