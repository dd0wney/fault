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
# path contains no dot. Everything else is external, except this module's own
# packages.
#
# Usage:
#   scripts/no-external-deps.sh            # check this module
#   go list -deps ./... | scripts/no-external-deps.sh -   # check a supplied list
#
# Exit codes:
#   0  standard library only
#   1  an external dependency is present
#   2  the check could not run
#
# Exit 2 matters as much as exit 1. A check that read nothing must not report a
# pass.

set -uo pipefail

MODULE="github.com/dd0wney/fault"

if [ "${1:-}" = "-" ]; then
  DEPS="$(cat)"
else
  ROOT="$(cd "$(dirname "$0")/.." && pwd)"
  DEPS="$(cd "$ROOT" && go list -deps ./... 2>&1)" || {
    echo "no-external-deps: 'go list -deps' failed:" >&2
    echo "$DEPS" >&2
    exit 2
  }
fi

if [ -z "$DEPS" ]; then
  echo "no-external-deps: the dependency list is empty — refusing to report a pass" >&2
  exit 2
fi

EXTERNAL="$(printf '%s\n' "$DEPS" \
  | grep -v "^${MODULE}\(/\|$\)" \
  | awk -F/ '$1 ~ /\./ { print }')"

if [ -n "$EXTERNAL" ]; then
  echo "no-external-deps: these are outside the standard library:" >&2
  printf '  %s\n' $EXTERNAL >&2
  exit 1
fi

COUNT="$(printf '%s\n' "$DEPS" | grep -c .)"
echo "no-external-deps: $COUNT packages, standard library only"
