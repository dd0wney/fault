#!/usr/bin/env bash
#
# Prove the mutation gate can report the opposite result.
#
# A gate that always passes and a gate that is correct print the same thing.
# Each fixture below is a way the gate should fail, and its exit code is
# asserted rather than assumed.

set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
GATE="$HERE/mutation.sh"
DATA="$HERE/testdata"
FAILURES=0

expect() {
  local want="$1" name="$2"; shift 2
  "$@" >/dev/null 2>&1
  local got=$?
  if [ "$got" != "$want" ]; then
    echo "selftest: $name — exit $got, want $want" >&2
    FAILURES=$((FAILURES + 1))
  else
    printf '  ok   %-46s exit %s\n' "$name" "$got"
  fi
}

expect 2 "the tool is not installed" \
  env PATH=/usr/bin:/bin "$GATE" --root "$DATA/thin" --baseline "$DATA/thin/baseline.tsv"

expect 2 "the baseline is missing" \
  "$GATE" --root "$DATA/thin" --baseline "$DATA/thin/no-such-file.tsv"

expect 2 "the suite already fails, so the score would lie" \
  "$GATE" --root "$DATA/redsuite" --baseline "$DATA/redsuite/baseline.tsv"

expect 2 "a package that generates no mutants" \
  "$GATE" --root "$DATA/nomutants" --baseline "$DATA/nomutants/baseline.tsv"

expect 2 "a package missing from the baseline" \
  "$GATE" --root "$DATA/unlisted" --baseline "$DATA/unlisted/baseline.tsv"

expect 1 "a package below its floor" \
  "$GATE" --root "$DATA/thin" --baseline "$DATA/thin/baseline.tsv"

printf './\t0.00\tany score meets this\n' > "$DATA/thin/floor-zero.tsv"
expect 0 "a package that meets its floor" \
  "$GATE" --root "$DATA/thin" --baseline "$DATA/thin/floor-zero.tsv"
rm -f "$DATA/thin/floor-zero.tsv"

if [ "$FAILURES" != 0 ]; then
  echo "selftest: $FAILURES fixture(s) wrong — the gate is not trustworthy" >&2
  exit 1
fi
echo "selftest: the gate reports every outcome correctly"
