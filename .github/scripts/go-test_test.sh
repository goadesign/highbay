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
printf 'module github.com/goadesign/highbay\n\ngo 1.20\n' > "$fixture/go.mod"
for package in external owned plain uncovered; do
  mkdir -p "$fixture/$package"
  printf 'package %s\n' "$package" > "$fixture/$package/source.go"
done
printf 'package owned\n' > "$fixture/owned/source_test.go"
printf 'package external_test\n' > "$fixture/external/source_test.go"
printf 'package plain\n' > "$fixture/plain/source_test.go"
printf '%s\n' github.com/goadesign/highbay/external github.com/goadesign/highbay/owned \
  github.com/goadesign/highbay/uncovered > "$fixture/.github/race-packages.txt"

# Only test execution is recorded; go list uses the actual selected toolchain.
cat > "$scratch/bin/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = list ]; then
  if [ "${WRAPPER_TEST_LIST_FAILURE:-}" = true ]; then
    printf '%s\n' github.com/goadesign/highbay/plain
    printf '%s\n' 'fixture metadata failure' >&2
    exit 23
  fi
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
    github.com/goadesign/highbay/plain github.com/goadesign/highbay/uncovered
  printf '\n'
  printf '%s\0' test -race -short -count=1 -run '^Test(One|Two)$' \
    github.com/goadesign/highbay/external github.com/goadesign/highbay/owned
  printf '\n'
} > "$scratch/expected"
cmp "$scratch/expected" "$WRAPPER_TEST_CALLS"

# The unchanged owner listing automatically covers the first external test.
printf 'package uncovered_test\n' > "$fixture/uncovered/source_test.go"
: > "$WRAPPER_TEST_CALLS"
bash "$repository/.github/scripts/go-test.sh" -short -count=1 -run '^Test(One|Two)$' -- ./...
{
  printf '%s\0' test -short -count=1 -run '^Test(One|Two)$' github.com/goadesign/highbay/plain
  printf '\n'
  printf '%s\0' test -race -short -count=1 -run '^Test(One|Two)$' \
    github.com/goadesign/highbay/external github.com/goadesign/highbay/owned github.com/goadesign/highbay/uncovered
  printf '\n'
} > "$scratch/expected"
cmp "$scratch/expected" "$WRAPPER_TEST_CALLS"
printf 'PASS: plain, internal-test, external-test and first-test package selection\n'

# Full-root cohorts share this metadata owner and fail closed on partial lists.
mkdir -p "$fixture/.github/scripts" "$fixture/internal/testwriter" "$fixture/internal/designeval"
printf 'package testwriter\n' > "$fixture/internal/testwriter/source.go"
printf 'package testwriter\n' > "$fixture/internal/testwriter/source_test.go"
printf 'package designeval\n' > "$fixture/internal/designeval/source.go"
printf 'package designeval\n' > "$fixture/internal/designeval/source_test.go"
printf '%s\n' github.com/goadesign/highbay/internal/designeval >> "$fixture/.github/race-packages.txt"
cp "$repository/.github/scripts/go-test.sh" "$fixture/.github/scripts/"
cp "$repository/.github/scripts/test-root.sh" "$fixture/.github/scripts/"
: > "$WRAPPER_TEST_CALLS"
bash "$fixture/.github/scripts/test-root.sh" other-plain
bash "$fixture/.github/scripts/test-root.sh" other-concurrency
{
  printf '%s\0' test -vet=all -count=1 -timeout 20m -json -run . -skip '' \
    github.com/goadesign/highbay/plain
  printf '\n'
  printf '%s\0' test -race -vet=all -count=1 -timeout 20m -json -run . -skip '' \
    github.com/goadesign/highbay/external github.com/goadesign/highbay/owned github.com/goadesign/highbay/uncovered
  printf '\n'
} > "$scratch/expected"
cmp "$scratch/expected" "$WRAPPER_TEST_CALLS"

# Dedicated compiler selections preserve each package's instrumentation and
# keep the complementary roots out of the dynamic other-package batch.
compiler_tests='^Test(CheckJudgesTheWorkingCopy|EvaluationAndGenerationUseTheExplicitParent)$'
: > "$WRAPPER_TEST_CALLS"
bash "$fixture/.github/scripts/test-root.sh" compiler-first
bash "$fixture/.github/scripts/test-root.sh" testwriter-remaining
bash "$fixture/.github/scripts/test-root.sh" designeval
{
  printf '%s\0' test -vet=all -count=1 -timeout 20m -json -run "$compiler_tests" -skip '' \
    github.com/goadesign/highbay/internal/testwriter
  printf '\n'
  printf '%s\0' test -race -vet=all -count=1 -timeout 20m -json -run "$compiler_tests" -skip '' \
    github.com/goadesign/highbay/internal/designeval
  printf '\n'
  printf '%s\0' test -vet=all -count=1 -timeout 20m -json -run . -skip "$compiler_tests" \
    github.com/goadesign/highbay/internal/testwriter
  printf '\n'
  printf '%s\0' test -race -vet=all -count=1 -timeout 20m -json -run . -skip "$compiler_tests" \
    github.com/goadesign/highbay/internal/designeval
  printf '\n'
} > "$scratch/expected"
cmp "$scratch/expected" "$WRAPPER_TEST_CALLS"

for arguments in '' unknown 'pages-first extra'; do
  if bash "$fixture/.github/scripts/test-root.sh" $arguments > "$scratch/output" 2>&1; then
    echo "Root selection accepted invalid arguments: $arguments" >&2
    exit 1
  fi
done
: > "$WRAPPER_TEST_CALLS"
if WRAPPER_TEST_LIST_FAILURE=true bash "$fixture/.github/scripts/test-root.sh" other-plain > "$scratch/output" 2>&1; then
  echo 'Root selection accepted partial package metadata' >&2
  exit 1
fi
test ! -s "$WRAPPER_TEST_CALLS"
grep -Fq 'fixture metadata failure' "$scratch/output"
printf 'PASS: full-root cohorts, invalid groups and failed package enumeration\n'
