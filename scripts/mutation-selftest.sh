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
#
# go-mutesting itself writes report.json into the directory it runs in, which
# for these fixtures is under scripts/testdata/. That file is untracked and
# already matched by the repository's own .gitignore (a bare "report.json"
# line), but a case below deliberately reads and removes it, so cleanup here
# is a second guarantee rather than the only one.
TMP="$(mktemp -d)"
cleanup() {
  local status=$?
  rm -rf "$TMP"
  find "$DATA" -name report.json -delete 2>/dev/null || true
  exit $status
}
trap cleanup EXIT INT TERM
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

# Like expect, but also asserts that the combined stdout+stderr contains
# every literal substring given before the "--" separator. Used for the
# survivor-list cases below, where the exit code alone does not distinguish
# a NEW SURVIVOR from a STALE ROW from an empty-reason refusal -- all three
# exit the same way, and only the message tells them apart.
expect_out() {
  local want="$1" name="$2"; shift 2
  local pats=()
  while [ "$1" != "--" ]; do pats+=("$1"); shift; done
  shift
  local out got pat
  out="$("$@" 2>&1)"
  got=$?
  if [ "$got" != "$want" ]; then
    echo "selftest: $name — exit $got, want $want" >&2
    printf '%s\n' "$out" | sed 's/^/selftest:   /' >&2
    FAILURES=$((FAILURES + 1))
    return
  fi
  for pat in "${pats[@]}"; do
    if ! printf '%s\n' "$out" | grep -qF -- "$pat"; then
      echo "selftest: $name — exit $got matched, but output did not contain '$pat'" >&2
      printf '%s\n' "$out" | sed 's/^/selftest:   /' >&2
      FAILURES=$((FAILURES + 1))
      return
    fi
  done
  printf '  ok   %-46s exit %s\n' "$name" "$got"
}

# A survivors fixture that matches thin's report exactly: four escaped
# mutants, all in calc.go, keys computed once by hand the way the "known
# diff" case below records. Reused wherever a case needs the survivor check
# to PASS so the case can isolate a different failure (the floor, the
# filter), and by the case that tests the passing check itself.
printf '# package\tkey\twhere\treason\n./\tcalc.go:branch/if:48430df276c3b853\tcalc.go:5 branch/if\tfixture row for the selftest, not a real classification\n./\tcalc.go:expression/comparison:64a6ebd00ee15936\tcalc.go:4 expression/comparison\tfixture row for the selftest, not a real classification\n./\tcalc.go:numbers/decrementer:b8c36547feae51db\tcalc.go:4 numbers/decrementer\tfixture row for the selftest, not a real classification\n./\tcalc.go:numbers/incrementer:244ea632f3f1580d\tcalc.go:4 numbers/incrementer\tfixture row for the selftest, not a real classification\n' \
  > "$TMP/thin-survivors-ok.tsv"

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

# These four cases predate the survivor list. --survivors now points at the
# fixture above so each one still isolates the single failure it names,
# instead of also failing (or flipping to pass) on an unrelated NEW SURVIVOR.
expect 1 "a package below its floor" \
  "$GATE" --root "$DATA/thin" --baseline "$DATA/thin/baseline.tsv" \
    --survivors "$TMP/thin-survivors-ok.tsv"

printf './\t0.00\tany score meets this\n' > "$TMP/floor-zero.tsv"
expect 0 "a package that meets its floor" \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv" \
    --survivors "$TMP/thin-survivors-ok.tsv"

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
  "$GATE" --root "$DATA/thin" --baseline "$TMP/two-rows.tsv" \
    --survivors "$TMP/thin-survivors-ok.tsv"
expect 0 "a package argument excludes the other rows" \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/two-rows.tsv" \
    --survivors "$TMP/thin-survivors-ok.tsv" ./

# --- the survivor list ------------------------------------------------------
#
# Identity is (file, mutator, a hash of the hunk body), never the line
# number. thin's report.json (committed as a fixture, read directly, not
# regenerated) holds four escaped mutants, all in calc.go. The hash below was
# computed once by hand with python3 while writing this case, per the spec:
#
#   python3 -c "
#   import json, hashlib
#   r = json.load(open('scripts/testdata/thin/report.json'))
#   m = r['escaped'][0]
#   diff = m['diff']
#   lines = [l for l in diff.splitlines()
#            if l.startswith(('-','+',' ')) and not l.startswith(('---','+++'))]
#   body = '\n'.join(lines).strip()
#   print(hashlib.sha256(body.encode()).hexdigest()[:16])
#   "
#   # -> 48430df276c3b853
#
# for the first escaped entry: calc.go, branch/if. The literal below is that
# run's output, not a formula the test re-derives.
#
# report.json is untracked (already matched by the repository's own
# .gitignore) and is not regenerated here. It reads whichever copy the
# earlier thin cases above just wrote, since every one of them runs
# go-mutesting for real against the same calc.go and calc_test.go and so
# leaves an identical report.json behind -- go-mutesting's output is
# deterministic for one source tree. If this case ever moves above all of
# them, give it its own real run first.
expect_out 0 "the key of a known diff" \
  "calc.go:branch/if:48430df276c3b853" -- \
  "$GATE" --print-keys "$DATA/thin/report.json"

printf '# package\tkey\twhere\treason\n./\tcalc.go:branch/if:48430df276c3b853\tcalc.go:5 branch/if\tfixture: matched on purpose\n./\tcalc.go:expression/comparison:64a6ebd00ee15936\tcalc.go:4 expression/comparison\tfixture: matched on purpose\n./\tcalc.go:numbers/decrementer:b8c36547feae51db\tcalc.go:4 numbers/decrementer\tfixture: matched on purpose\n' \
  > "$TMP/surv-missing-one.tsv"
expect_out 1 "an escaped mutant with no row" \
  "NEW SURVIVOR" "TODO: read it" -- \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv" \
    --survivors "$TMP/surv-missing-one.tsv"

printf '# package\tkey\twhere\treason\n./\tcalc.go:branch/if:48430df276c3b853\tcalc.go:5 branch/if\tfixture: matched\n./\tcalc.go:expression/comparison:64a6ebd00ee15936\tcalc.go:4 expression/comparison\tfixture: matched\n./\tcalc.go:numbers/decrementer:b8c36547feae51db\tcalc.go:4 numbers/decrementer\tfixture: matched\n./\tcalc.go:numbers/incrementer:244ea632f3f1580d\tcalc.go:4 numbers/incrementer\tfixture: matched\n./\tcalc.go:bogus/mutator:0000000000000000\tcalc.go:99 bogus/mutator\tfixture: a row with no matching mutant, on purpose\n' \
  > "$TMP/surv-stale-row.tsv"
expect_out 1 "a row whose key matches no escaped mutant" \
  "STALE ROW" -- \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv" \
    --survivors "$TMP/surv-stale-row.tsv"

expect 0 "every escaped mutant has a row and every row has a mutant" \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv" \
    --survivors "$TMP/thin-survivors-ok.tsv"

printf '# package\tkey\twhere\treason\n./\tcalc.go:branch/if:48430df276c3b853\tcalc.go:5 branch/if\t\n' \
  > "$TMP/surv-empty-reason.tsv"
expect_out 2 "a row with an empty reason" \
  "empty reason" -- \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv" \
    --survivors "$TMP/surv-empty-reason.tsv"

expect_out 2 "the survivors file is missing" \
  "is missing" -- \
  "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv" \
    --survivors "$DATA/thin/no-such-survivors.tsv"

# jq missing: shadow the whole of /usr/bin except jq, so every other tool
# the gate needs (git, dirname, sed, go-mutesting once added below) is still
# on PATH and only jq's absence is under test -- the same reasoning as "the
# tool is not installed" above, extended to a tool that shares a directory
# with the ones that must stay.
SHIM_NOJQ="$TMP/shim-nojq"
mkdir -p "$SHIM_NOJQ"
for f in /usr/bin/*; do
  [ -d "$f" ] && continue
  name="$(basename "$f")"
  [ "$name" = "jq" ] && continue
  ln -sf "$f" "$SHIM_NOJQ/$name" 2>/dev/null || true
done
ln -sf "$(command -v go-mutesting)" "$SHIM_NOJQ/go-mutesting"
ln -sf "$(command -v go)" "$SHIM_NOJQ/go"
expect_out 2 "jq is not installed" \
  "jq" -- \
  env PATH="$SHIM_NOJQ" "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv" \
    --survivors "$TMP/thin-survivors-ok.tsv"

# report.json missing after the run: a go-mutesting wrapper runs the real
# binary and then deletes the report it just wrote, in the directory the
# gate reads it from. No change to the gate's own code is needed for this.
REAL_GOMUTESTING="$(command -v go-mutesting)"
WRAP_NOREPORT="$TMP/wrap-noreport"
mkdir -p "$WRAP_NOREPORT"
cat > "$WRAP_NOREPORT/go-mutesting" <<EOF
#!/usr/bin/env bash
"$REAL_GOMUTESTING" "\$@"
status=\$?
rm -f report.json
exit \$status
EOF
chmod +x "$WRAP_NOREPORT/go-mutesting"
expect_out 2 "report.json missing after the run" \
  "report.json" -- \
  env PATH="$WRAP_NOREPORT:$PATH" "$GATE" --root "$DATA/thin" --baseline "$TMP/floor-zero.tsv" \
    --survivors "$TMP/thin-survivors-ok.tsv"

if [ "$FAILURES" != 0 ]; then
  echo "selftest: $FAILURES fixture(s) wrong — the gate is not trustworthy" >&2
  exit 1
fi
echo "selftest: the gate reports every outcome correctly"
