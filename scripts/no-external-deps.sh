#!/usr/bin/env bash
#
# Refuse any dependency outside the standard library.
#
# The core package is a candidate for the Go standard library, which permits no
# external dependency at all. That promise is broken silently: an editor's
# auto-import adds one line, everything compiles, and the constraint is gone. It
# happened once during this module's first day, when `file.Error{}` pulled in
# github.com/expr-lang/expr/file.
#
# A package path belongs to the standard library when the first element of the
# path contains no dot. Everything else is external, except the checked module's
# own packages.
#
# Usage:
#   scripts/no-external-deps.sh                  # check this module
#   scripts/no-external-deps.sh --root DIR       # check the module in DIR
#   go list -deps -test ./... | scripts/no-external-deps.sh -   # a supplied list
#
# Exit codes:
#   0  standard library only
#   1  an external dependency is present
#   2  the check could not run
#
# Exit 2 matters as much as exit 1. A check that read nothing must not report a
# pass.

set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STDIN=0
while [ $# -gt 0 ]; do
  case "$1" in
    --root)
      ROOT="$(cd "$2" 2>/dev/null && pwd)" || {
        echo "no-external-deps: --root '$2' is not a directory" >&2
        exit 2
      }
      shift 2
      ;;
    -) STDIN=1; shift ;;
    *) break ;;
  esac
done

# The module path is READ from the module, never written into this file. A
# hardcoded path is correct for exactly one root. Aimed at any other module, a
# hardcoded path makes the gate report THAT module's own packages as external
# and exit 1 for the wrong reason -- a fixture that goes red while proving
# nothing, which is the same defect as one that goes green while proving
# nothing.
#
# Measured against scripts/testdata/testonly-external, a hardcoded path flags
# example.com/depfixture beside the one real finding.
# `go list -m` outside a module prints "command-line-arguments" and exits 0.
# Measured 2026-08-31. It answers, and the answer is not a module path, so the
# gate would carry that string into the filter and report findings about a
# module that does not exist. That is this repository's recurring defect: an
# instrument that reads a real thing and answers a narrower question than the
# one asked, with the narrowing invisible in the output.
#
# `go env GOMOD` is the probe that can say no. It prints /dev/null when there
# is no module, so the gate can refuse instead of guessing.
GOMOD="$(cd "$ROOT" && go env GOMOD 2>/dev/null)"
if [ -z "$GOMOD" ] || [ "$GOMOD" = /dev/null ]; then
  echo "no-external-deps: $ROOT is not inside a Go module — refusing to report a pass" >&2
  exit 2
fi

MODULE="$(cd "$ROOT" && go list -m 2>&1)" || {
  echo "no-external-deps: 'go list -m' failed in $ROOT — refusing to report a pass:" >&2
  echo "$MODULE" >&2
  exit 2
}
# In a workspace `go list -m` prints one line per module. The first is the one
# this root names.
MODULE="$(printf '%s\n' "$MODULE" | head -1)"
case "$MODULE" in
  '' | *[[:space:]]*)
    echo "no-external-deps: '$MODULE' is not a module path — refusing to report a pass" >&2
    exit 2
    ;;
esac

if [ "$STDIN" = 1 ]; then
  DEPS="$(cat)"
else
  # -test, because `go list -deps` alone omits every test import, and a test
  # file is exactly where a third-party driver arrives. The gate was correct
  # about a graph that excluded the file it most needed to read.
  DEPS="$(cd "$ROOT" && go list -deps -test ./... 2>&1)" || {
    echo "no-external-deps: 'go list -deps -test' failed:" >&2
    echo "$DEPS" >&2
    exit 2
  }
fi

if [ -z "$DEPS" ]; then
  echo "no-external-deps: the dependency list is empty — refusing to report a pass" >&2
  exit 2
fi

# -test adds three shapes that a plain module-path anchor cannot read. Measured
# on this module: 108 packages become 148 lines.
#
#   github.com/dd0wney/fault/crash [github.com/dd0wney/fault/crash.test]
#   github.com/dd0wney/fault/crash.test
#   github.com/dd0wney/fault_test
#
# The first is a package recompiled in a test context. The trailing " [...]" is
# metadata saying WHY the package is listed and is not part of any import path,
# so it is removed here once rather than tolerated by every rule downstream.
# Removing it makes the list duplicate itself, so sort -u follows.
#
# The second is the synthetic test binary. The third is a package's external
# test package. Both are the checked module's own code under a name the
# toolchain generates, so the anchor below accepts them. It does not blanket
# drop "*.test", which would also hide a genuine external module whose path
# ends that way.
#
# The third shape occurs for the ROOT package only: in
# github.com/dd0wney/fault/alloc_test the "/" arrives first and the plain
# anchor already matches it. A repair tested against one sub-package would look
# complete and would not be.
DEPS="$(printf '%s\n' "$DEPS" | sed 's/ \[.*\]$//' | grep -v '^$' | sort -u)"

# grep reads "." as any character, and every module path has dots in it. Without
# this escape, "github.com/dd0wney/fault" would also match "githubXcom/...".
# The "lookalike module path" selftest row guards the other direction: a path
# that merely starts with the module path must stay external.
MODULE_RE="$(printf '%s' "$MODULE" | sed 's/[.[\*^$]/\\&/g')"

EXTERNAL="$(printf '%s\n' "$DEPS" \
  | grep -vE "^${MODULE_RE}(/.*)?(_test|\.test)?$" \
  | awk -F/ '$1 ~ /\./ { print }')"

if [ -n "$EXTERNAL" ]; then
  echo "no-external-deps: these are outside the standard library:" >&2
  printf '  %s\n' $EXTERNAL >&2
  exit 1
fi

COUNT="$(printf '%s\n' "$DEPS" | grep -c .)"
echo "no-external-deps: $COUNT packages in $MODULE, standard library only"
