#!/bin/sh
# Trusted stand-ins exercise process output without compiling or running candidate code.
set -eu
role=$1
root=$2
shift 2
case "$role" in
setup)
  mkdir -p "$root/bin" "$root/goroot/bin" "$root/cache" "$root/repo/app" "$root/repo/eval"
  for module in app eval; do
    printf 'module example.invalid/%s\ngo 1.27.3\n' "$module" > "$root/repo/$module/go.mod"
    printf 'package main\nfunc main() {}\n' > "$root/repo/$module/main.go"
  done
  git -C "$root/repo" init -q
  git -C "$root/repo" add app eval
  git -C "$root/repo" -c user.name=fixture -c user.email=fixture@example.invalid -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -qm fixture
  git -C "$root/repo" rev-parse HEAD > "$root/commit"
  printf '%s\n' '{"suite_id":"private-diagnostics","scenarios":[{"id":"smoke","started_at":"2026-01-01T00:00:00Z","passed":true,"result":{"checks":[{"name":"ok","passed":true}],"artifacts":[{"name":"candidate:app","uri":"sha256:34e2bdd06b59d6222bdaa2c16d240554627e0639a22ee08804f2f7aec46fc8cd"}]}}]}' > "$root/report"
  command -v git > "$root/real-git"
  # Roots are test-owned paths; quote apostrophes before embedding them in wrappers.
  quoted_root=$(printf '%s' "$root" | sed "s/'/'\\\\''/g")
  quoted_script=$(printf '%s' "$0" | sed "s/'/'\\\\''/g")
  for target in bin/git goroot/bin/go suite; do
    name=${target##*/}
    printf "#!/bin/sh\nexec /bin/sh '%s' '%s' '%s' \"\$@\"\n" "$quoted_script" "$name" "$quoted_root" > "$root/$target"
    chmod 700 "$root/$target"
  done
  ;;
git)
  mode=$(cat "$root/case")
  real=$(cat "$root/real-git")
  case "$*" in
    *' clone '*) operation=clone; for destination do :; done; label=${destination##*/} ;;
    *' checkout '*) operation=checkout; label=${PWD##*/} ;;
    'rev-parse HEAD') operation=head; label=${PWD##*/} ;;
    *) exec "$real" "$@" ;;
  esac
  if [ "$mode" = "$operation-$label" ]; then
    printf '%s\n' 'https://FAKE_GIT_USER_521:fake@repo.invalid/FAKE_GIT_PATH_521 FAKE_HELPER_521'
    printf '%s\n' 'https://FAKE_GIT_USER_521:fake@repo.invalid/FAKE_GIT_PATH_521 FAKE_HELPER_521' >&2
    exit 7
  fi
  if [ "$mode" = "head-mismatch-$label" ] && [ "$operation" = head ]; then
    printf '%s\n' 'FAKE_HEAD_VALUE_521'
    exit 0
  fi
  exec "$real" "$@"
  ;;
go)
  mode=$(cat "$root/case")
  case "${PWD##*/}" in app) label=candidate ;; eval) label=suite ;; *) exit 11 ;; esac
  case "$1" in
    mod)
      [ "$*" = 'mod download' ]; [ "$GOWORK" = off ]
      if [ "$mode" = "go-mod-download-$label" ]; then
        printf '%s\n' 'http://FAKE_PROXY_USER_521:fake@proxy.invalid/FAKE_PROXY_PATH_521 -FAKE_GOFLAGS_521'
        printf '%s\n' 'http://FAKE_PROXY_USER_521:fake@proxy.invalid/FAKE_PROXY_PATH_521 -FAKE_GOFLAGS_521' >&2
        exit 7
      fi
      exit 0 ;;
    env)
      [ "$*" = 'env GOROOT GOMODCACHE' ]
      if [ "$mode" = "go-env-$label" ]; then
        printf '%s\n' 'http://FAKE_PROXY_USER_521:fake@proxy.invalid/FAKE_PROXY_PATH_521 -FAKE_GOFLAGS_521'
        printf '%s\n' 'http://FAKE_PROXY_USER_521:fake@proxy.invalid/FAKE_PROXY_PATH_521 -FAKE_GOFLAGS_521' >&2
        exit 7
      fi
      if [ "$mode" = "go-env-malformed-$label" ]; then
        printf '%s\n' 'FAKE_GO_SETTINGS_521'
        exit 0
      fi
      printf '%s\n%s\n' "$root/goroot" "$root/cache"
      ;;
    build)
      [ "$GOPROXY" = off ]; [ "$GOFLAGS" = -mod=readonly ]
      out=''
      while [ "$#" -gt 0 ]; do if [ "$1" = -o ]; then shift; out=$1; fi; shift; done
      [ -n "$out" ]
      case "$label" in suite) cp "$root/suite" "$out"; chmod 700 "$out" ;; *) printf 'placeholder, never executed' > "$out" ;; esac
      ;;
    *) exit 9 ;;
  esac
  ;;
suite)
  mode=$(cat "$root/case")
  case "${1-}" in
    --describe)
      case "$mode" in
        describe)
          printf '%s\n' "${HIGHBAY_TEST_SERVER_VALUE-absent} ${ANTHROPIC_API_KEY-absent}"
          printf '%s\n' "${HIGHBAY_TEST_SERVER_VALUE-absent} ${ANTHROPIC_API_KEY-absent}" >&2
          exit 7 ;;
        declaration)
          printf '{"id":%s}' 'FAKE_DECLARATION_JSON_521'
          exit 0 ;;
      esac
      printf '%s\n' '{"id":"private-diagnostics","environment":"sim","scenarios":[{"id":"smoke"}]}'
      ;;
    *)
      case "$mode" in
        report-*)
          printf '%s\n' "${HIGHBAY_TEST_SERVER_VALUE-absent} ${ANTHROPIC_API_KEY-absent}" >&2
          case "$mode" in
            report-malformed-*) printf '%s' '{"suite_id":"private-diagnostics","started_at":"FAKE_REPORT_JSON_521"}' ;;
            report-missing-*) : ;;
            *) cat "$root/report" ;;
          esac
          case "$mode" in *-failed|report-partial) exit 7 ;; report-abnormal) kill -KILL $$ ;; esac
          ;;
        *) cat "$root/report" ;;
      esac
      ;;
  esac
  ;;
*) exit 10 ;;
esac
