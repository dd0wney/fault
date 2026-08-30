#!/usr/bin/env bash
#
# Prove the gate can report the opposite result.
#
# A check that always passes and a check that is correct print the same thing.
# Each fixture below is a way the check should fail, and the expected exit code
# is asserted rather than assumed.

set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
GATE="$HERE/no-external-deps.sh"
DATA="$HERE/testdata"
FAILURES=0

expect() {
  local want="$1" name="$2" input="$3"
  printf '%s' "$input" | "$GATE" - >/dev/null 2>&1
  local got=$?
  if [ "$got" != "$want" ]; then
    echo "selftest: $name — exit $got, want $want" >&2
    FAILURES=$((FAILURES + 1))
  else
    printf '  ok   %-42s exit %s\n' "$name" "$got"
  fi
}

expect 0 "standard library only"        $'fmt\nos\nsync\ninternal/abi\nunsafe\n'
expect 0 "the module's own packages"    $'fmt\ngithub.com/dd0wney/fault\ngithub.com/dd0wney/fault/fs\n'
expect 1 "one external dependency"      $'fmt\ngithub.com/expr-lang/expr/file\n'
expect 1 "an x/ dependency"             $'fmt\ngolang.org/x/sync/errgroup\n'
expect 1 "a lookalike module path"      $'fmt\ngithub.com/dd0wney/faultly/thing\n'
expect 2 "an empty list"                ''

# Everything above tests the FILTER: each fixture hands the gate a list and
# asserts what it does with that list. None of them tests the `go list` call
# that BUILDS the list -- and that is where the gate was wrong. `go list -deps
# ./...` omits a test-only import, so an external dependency imported from a
# _test.go file was invisible to a check whose whole job is to see it.
#
# The lesson is the one this repository keeps relearning: ask what the gate
# actually read. A gate correct about the wrong graph reads exactly like a gate
# correct about the right one.
#
# This fixture therefore runs the gate in module mode, against a module whose
# only external import sits in a test file.
expect_root() {
  local want="$1" name="$2" dir="$3"
  "$GATE" --root "$dir" >/dev/null 2>&1
  local got=$?
  if [ "$got" != "$want" ]; then
    echo "selftest: $name -- exit $got, want $want" >&2
    FAILURES=$((FAILURES + 1))
  else
    printf '  ok   %-42s exit %s\n' "$name" "$got"
  fi
}

expect_root 1 "a test-only external import" "$DATA/testonly-external"

if [ "$FAILURES" != 0 ]; then
  echo "selftest: $FAILURES fixture(s) wrong — the gate is not trustworthy" >&2
  exit 1
fi
echo "selftest: the gate reports every outcome correctly"
