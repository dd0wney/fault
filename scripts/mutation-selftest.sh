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

# Generated baselines go OUTSIDE the working tree.
#
# They used to be written beside the fixtures and removed afterwards, so an
# interrupted selftest left stray .tsv files in the checkout. It also meant
# this script dirtied the tree it was testing a gate against -- and the first
# version of that gate's working-tree guard refused to run because of exactly
# these two files. --baseline takes any path, so none of that is necessary.
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM
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

printf './\t0.00\tany score meets this\n' > "$TMP/floor-zero.tsv"
expect 0 "a package that meets its floor" \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv"

# The [packages...] filter. Until 2026-08-31 the usage line documented it and
# nothing read it, so `mutation.sh ./crash/` measured all five packages and
# said nothing. Measured then: exit 1, with ./ reported, for a package name
# that does not exist.
expect 2 "a package argument the baseline does not hold" \
  "$GATE" --root "$DATA/thin" --baseline "$DATA/thin/baseline.tsv" ./no-such-package/

# The next two rows are a PAIR, and neither means anything alone.
#
# The baseline below holds a second row for a package that is not there, so an
# unfiltered run reaches it and refuses. The filtered run must exclude that row
# and pass. A filter that did nothing would exit 2 on the second row too, so
# the pair is what proves the filter excludes rather than merely being
# accepted.
printf './\t0.00\tany score meets this\n./absent/\t0.00\tno such package: an unfiltered run must reach it\n' \
  > "$TMP/two-rows.tsv"
expect 2 "an unfiltered run reaches a package it cannot measure" \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/two-rows.tsv"
expect 0 "a package argument excludes the other rows" \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/two-rows.tsv" ./

if [ "$FAILURES" != 0 ]; then
  echo "selftest: $FAILURES fixture(s) wrong — the gate is not trustworthy" >&2
  exit 1
fi
echo "selftest: the gate reports every outcome correctly"
