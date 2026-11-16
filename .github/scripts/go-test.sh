#!/usr/bin/env bash
# Run every supplied package, instrumenting the declared production concurrency
# owners. Untagged Go test flags precede --; package patterns follow it. E2E measurements
# invoke go test directly so their race/plain comparison remains explicit.
set -eo pipefail
exec </dev/null

test_args=()
while [ "$#" -gt 0 ] && [ "$1" != -- ]; do
  case "$1" in
    -race|-tags|-tags=*)
      echo 'instrumentation is selected here; tagged suites invoke go test directly' >&2
      exit 1
      ;;
  esac
  test_args+=("$1")
  shift
done
if [ "$#" -lt 2 ]; then
  echo 'usage: go-test.sh [go test flags] -- package-pattern ...' >&2
  exit 1
fi
shift

repository=${GITHUB_WORKSPACE:-$(git rev-parse --show-toplevel)}
owners="$repository/.github/race-packages.txt"
test -f "$owners"
owner_names=$(tr -d '\r' < "$owners")
packages=$(go list "$@")
if [ -z "$packages" ]; then
  echo 'no Go packages selected' >&2
  exit 1
fi
race=()
plain=()
while IFS= read -r package; do
  if grep -Fxq -- "$package" <<< "$owner_names"; then
    race+=("$package")
  else
    plain+=("$package")
  fi
done <<< "$packages"

if [ "${#plain[@]}" -gt 0 ]; then
  go test "${test_args[@]}" "${plain[@]}"
fi
if [ "${#race[@]}" -gt 0 ]; then
  go test -race "${test_args[@]}" "${race[@]}"
fi
