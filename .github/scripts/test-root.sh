#!/usr/bin/env bash
# Execute one complementary full-root group on either native CI platform.
# go-test.sh remains the sole owner of race instrumentation.
set -euo pipefail
exec </dev/null

if [ "$#" -ne 1 ]; then
  echo 'usage: test-root.sh group' >&2
  exit 1
fi
cd "$(cd "$(dirname "$0")/../.." && pwd)"

pages='^Test(ViewsPage|Checkpoint)'
first_pages='^Test(Checkpoint|ViewsPage[A-L])'
first_cli='^TestGenKeepsTheStoryTablesTests$'
run=.
skip=
packages=()
case "$1" in
  pages-first)
    run=$first_pages
    packages=(./internal/controlplane)
    ;;
  pages-remaining)
    run=$pages
    skip=$first_pages
    packages=(./internal/controlplane)
    ;;
  controlplane)
    skip=$pages
    packages=(./internal/controlplane)
    ;;
  designeval)
    packages=(./internal/designeval)
    ;;
  cli-first)
    run=$first_cli
    packages=(./cmd/surla)
    ;;
  cli-remaining)
    skip=$first_cli
    packages=(./cmd/surla)
    ;;
  other-plain|other-concurrency)
    # These independent owners remove the old plain-then-race job barrier.
    # All other packages, including newly added and testless packages, retain
    # one owner. The wrapper still decides whether a listed owner has tests.
    listed=$(go list ./...)
    while IFS= read -r package; do
      case "$package" in
        github.com/goadesign/surla/internal/controlplane|github.com/goadesign/surla/internal/designeval|github.com/goadesign/surla/cmd/surla)
          continue
          ;;
      esac
      if grep -Fxq -- "$package" .github/race-packages.txt; then
        [ "$1" = other-concurrency ] || continue
      else
        [ "$1" = other-plain ] || continue
      fi
      packages+=("$package")
    done <<< "$listed"
    ;;
  *)
    echo "Unknown root group: $1" >&2
    exit 1
    ;;
esac

exec bash .github/scripts/go-test.sh -vet=all -count=1 -timeout 20m -json \
  -run "$run" -skip "$skip" -- "${packages[@]}"
