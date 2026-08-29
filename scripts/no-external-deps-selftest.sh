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

if [ "$FAILURES" != 0 ]; then
  echo "selftest: $FAILURES fixture(s) wrong — the gate is not trustworthy" >&2
  exit 1
fi
echo "selftest: the gate reports every outcome correctly"
