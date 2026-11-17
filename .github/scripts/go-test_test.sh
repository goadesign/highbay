#!/usr/bin/env bash
# Exercise the wrapper with real Go package metadata and recorded test commands.
# Listing tiny fixtures avoids compiling their dependencies or running tests.
set -euo pipefail
exec </dev/null

repository=$(cd "$(dirname "$0")/../.." && pwd)
go_binary=$(command -v go)
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
fixture="$scratch/fixture"
mkdir -p "$scratch/bin" "$fixture/.github"
printf 'module example.com/partition\n\ngo 1.20\n' > "$fixture/go.mod"
for package in external owned plain uncovered; do
  mkdir -p "$fixture/$package"
  printf 'package %s\n' "$package" > "$fixture/$package/source.go"
done
printf 'package owned\n' > "$fixture/owned/source_test.go"
printf 'package external_test\n' > "$fixture/external/source_test.go"
printf 'package plain\n' > "$fixture/plain/source_test.go"
printf '%s\n' example.com/partition/external example.com/partition/owned \
  example.com/partition/uncovered > "$fixture/.github/race-packages.txt"

# Only test execution is recorded; go list uses the actual selected toolchain.
cat > "$scratch/bin/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = list ]; then
  exec "$WRAPPER_TEST_GO" "$@"
fi
printf '%s\0' "$@" >> "$WRAPPER_TEST_CALLS"
printf '\n' >> "$WRAPPER_TEST_CALLS"
SH
chmod 700 "$scratch/bin/go"
export WRAPPER_TEST_GO="$go_binary" WRAPPER_TEST_CALLS="$scratch/calls"
export GITHUB_WORKSPACE="$fixture" PATH="$scratch/bin:$PATH" GOWORK=off GOTOOLCHAIN=local
cd "$fixture"

# Listed testless packages build once plain; internal and external tests race.
bash "$repository/.github/scripts/go-test.sh" -short -count=1 -run '^Test(One|Two)$' -- ./...
{
  printf '%s\0' test -short -count=1 -run '^Test(One|Two)$' \
    example.com/partition/plain example.com/partition/uncovered
  printf '\n'
  printf '%s\0' test -race -short -count=1 -run '^Test(One|Two)$' \
    example.com/partition/external example.com/partition/owned
  printf '\n'
} > "$scratch/expected"
cmp "$scratch/expected" "$WRAPPER_TEST_CALLS"

# The unchanged owner listing automatically covers the first external test.
printf 'package uncovered_test\n' > "$fixture/uncovered/source_test.go"
: > "$WRAPPER_TEST_CALLS"
bash "$repository/.github/scripts/go-test.sh" -short -count=1 -run '^Test(One|Two)$' -- ./...
{
  printf '%s\0' test -short -count=1 -run '^Test(One|Two)$' example.com/partition/plain
  printf '\n'
  printf '%s\0' test -race -short -count=1 -run '^Test(One|Two)$' \
    example.com/partition/external example.com/partition/owned example.com/partition/uncovered
  printf '\n'
} > "$scratch/expected"
cmp "$scratch/expected" "$WRAPPER_TEST_CALLS"
printf 'PASS: plain, internal-test, external-test and first-test package selection\n'
