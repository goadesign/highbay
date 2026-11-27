#!/bin/sh
# Install a verified Highbay release and let highbay up check its dependencies.
# HIGHBAY_VERSION selects a tag. HIGHBAY_INSTALL_DIR selects the binary folder.
# HIGHBAY_NO_DEPS=1 skips dependency installation.
# HIGHBAY_NO_START=1 installs tools without configuring or starting services.
set -eu

fail() {
  printf 'Highbay: %s\n' "$*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || fail "install $1 and rerun the installer"
}

need curl
need tar
need awk
need grep
case $(uname -s) in
  Darwin) os=darwin ;;
  Linux) os=linux ;;
  *) fail "release archives are available for macOS and Linux" ;;
esac
case $(uname -m) in
  arm64|aarch64) arch=arm64 ;;
  amd64|x86_64) arch=amd64 ;;
  *) fail "release archives are available for ARM64 and x86-64" ;;
esac

fetch() {
  curl -fsSL --proto '=https' --proto-redir '=https' --connect-timeout 10 --max-time 120 "$@"
}

releases=https://github.com/goadesign/highbay/releases
version=${HIGHBAY_VERSION:-}
if [ -z "$version" ]; then
  resolved=$(fetch --output /dev/null --write-out '%{url_effective}' "$releases/latest") ||
    fail "could not locate the latest Highbay release"
  case $resolved in
    "$releases"/tag/v*) version=${resolved##*/} ;;
    *) fail "the latest release did not resolve to a Highbay version tag" ;;
  esac
fi
version=${version#v}
case $version in
  ''|*[!0-9.]*) fail "expected a stable Highbay release version" ;;
esac
if ! printf '%s\n' "$version" | grep -Eq '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$'; then
  fail "expected a stable Highbay release version"
fi

bin=${HIGHBAY_INSTALL_DIR:-$HOME/.local/bin}
case $bin in
  /*) ;;
  *) fail "HIGHBAY_INSTALL_DIR must be an absolute path" ;;
esac

scratch=$(mktemp -d)
stage=
cleanup() {
  if [ -n "$stage" ]; then rm -f "$stage"; fi
  rm -rf "$scratch"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

archive="highbay_${version}_${os}_${arch}.tar.gz"
base="$releases/download/v$version"
fetch --output "$scratch/checksums.txt" "$base/checksums.txt" ||
  fail "could not download the checksums for v$version"
want=$(awk -v archive="$archive" '
  NF == 2 && ($2 == archive || $2 == "*" archive) { digest = $1; count++ }
  END {
    if (count != 1 || length(digest) != 64 || digest ~ /[^[:xdigit:]]/) exit 1
    print tolower(digest)
  }
' "$scratch/checksums.txt") || fail "expected one SHA-256 checksum for $archive"

fetch --output "$scratch/$archive" "$base/$archive" || fail "could not download $archive"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$scratch/$archive")
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$scratch/$archive")
else
  fail "install sha256sum or shasum to verify the release"
fi
[ "${actual%% *}" = "$want" ] || fail "SHA-256 verification failed for $archive"
tar -xzf "$scratch/$archive" -C "$scratch" highbay ||
  fail "the release archive did not contain highbay"
[ -f "$scratch/highbay" ] && [ ! -L "$scratch/highbay" ] ||
  fail "the release archive did not contain a regular highbay executable"

mkdir -p "$bin"
[ ! -d "$bin/highbay" ] || fail "the installation path $bin/highbay is a directory"
stage=$(mktemp "$bin/.highbay-install.XXXXXX")
cp "$scratch/highbay" "$stage"
chmod 0755 "$stage"
mv -f "$stage" "$bin/highbay"
stage=
printf 'Installed highbay v%s at %s/highbay\n' "$version" "$bin"

PATH="$bin:$PATH" HIGHBAY_NO_DEPS="${HIGHBAY_NO_DEPS:-0}" \
  HIGHBAY_NO_START="${HIGHBAY_NO_START:-0}" HIGHBAY_INSTALL_DIR="$bin" "$bin/highbay" up --yes </dev/null

case :$PATH: in
  *:"$bin":*) ;;
  *) printf 'Add %s to PATH to run highbay from any directory.\n' "$bin" ;;
esac
