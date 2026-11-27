#!/usr/bin/env bash
# Run every supplied package, instrumenting the declared production concurrency
# owners when they have tests. Untagged Go test flags precede --; package patterns
# follow it. E2E measurements invoke go test directly so their race/plain
# comparison remains explicit.
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
if [ -z "${HIGHBAY_TEST_BUILD_CACHE:-}" ]; then
  default_build_cache=$(go env GOCACHE)
  if [[ "$default_build_cache" = /* ]]; then
    export HIGHBAY_TEST_BUILD_CACHE="$default_build_cache"
  fi
fi
owners="$repository/.github/race-packages.txt"
test -f "$owners"
owner_names=$(tr -d '\r' < "$owners")
packages=$(go list -f '{{.ImportPath}} {{if or .TestGoFiles .XTestGoFiles}}true{{else}}false{{end}}' "$@")
if [ -z "$packages" ]; then
  echo 'no Go packages selected' >&2
  exit 1
fi
race=()
plain=()
while read -r package has_tests; do
  if [ "$has_tests" = true ] && grep -Fxq -- "$package" <<< "$owner_names"; then
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
