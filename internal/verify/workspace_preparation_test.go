// These tests exercise configured workspace preparation with trusted Go stand-ins.
package verify

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/surla/internal/shquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWorkspacePreparationKeepsTargetsProfileAndSecrets proves persisted
// download policy survives while ambient compiler selectors do not. The fake
// emits planted markers on both streams even on success; none may be retained.
func TestWorkspacePreparationKeepsTargetsProfileAndSecrets(t *testing.T) {
	const proxyMarker = "FAKE_PROXY_CREDENTIAL_41"
	const flagsMarker = "FAKE_GOFLAGS_CREDENTIAL_41"
	for _, mode := range []string{"success", "lookup failure", "prefill failure", "no candidate packages"} {
		t.Run(mode, func(t *testing.T) {
			repo, _ := goWorkVerificationFixture(t, true)
			for _, name := range []string{"service", "worker"} {
				folder := filepath.Join(repo, "app", "cmd", name)
				require.NoError(t, os.MkdirAll(folder, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(folder, "main.go"), []byte("package main\nfunc main() {}\n"), 0o600))
			}
			git(t, repo, "add", "app/cmd")
			git(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "Configure two binary targets")
			commit := strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
			fake := t.TempDir()
			goroot, cache := filepath.Join(fake, "goroot"), filepath.Join(fake, "cache")
			calls, config := filepath.Join(fake, "calls"), filepath.Join(fake, "go.env")
			require.NoError(t, os.MkdirAll(filepath.Join(goroot, "bin"), 0o700))
			require.NoError(t, os.Mkdir(cache, 0o700))
			proxy := "https://fixture:" + proxyMarker + "@proxy.example.invalid"
			require.NoError(t, os.WriteFile(config, []byte("GOOS=js\nGOARCH=wasm\nGOEXPERIMENT=loopvar\nGOFLAGS=-tags="+flagsMarker+"\nCGO_ENABLED=1\nGOPROXY="+proxy+"\nGOAUTH=off\nGOTOOLCHAIN=auto\n"), 0o600))
			settings, err := json.Marshal(map[string]string{
				"GOROOT": goroot, "GOMODCACHE": cache, "GOPROXY": proxy,
				"GOAUTH": "off", "GOTOOLCHAIN": "auto", "GOSUMDB": "off",
			})
			require.NoError(t, err)
			var failure string
			if mode == "lookup failure" {
				failure = "exit 7\n"
			}
			prefillFailure := ""
			if mode == "prefill failure" {
				prefillFailure = "exit 8\n"
			}
			// Go owns reading the persisted file; its stand-in supplies the
			// effective policy rather than exposing a config parser to Verify.
			script := "#!/bin/sh\nset -eu\nprintf '%s|%s\\n' \"$PWD\" \"$1\" >> " + shquote.Quote(calls) + "\n" +
				"case \"$1\" in\nenv) [ \"$2\" = -json ]; [ \"$GOENV\" = " + shquote.Quote(config) + " ]; " +
				"printf '%s\\n' " + shquote.Quote(string(settings)) + "; printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + " >&2\n" + failure + ";;\n" +
				"list) [ \"$GOENV\" = off ]; [ \"$CGO_ENABLED\" = 0 ]; [ \"$GOFLAGS\" = -mod=readonly ]; [ \"$GOTOOLCHAIN\" = local ]; " +
				"[ -z \"${GOOS+x}${GOARCH+x}${GOEXPERIMENT+x}${GOAMD64+x}${GOARM64+x}\" ]; " +
				"[ \"$GOPROXY\" = " + shquote.Quote(proxy) + " ]; [ \"$GOAUTH\" = off ]; " +
				"[ \"$HOME\" = " + shquote.Quote(fake) + " ]; [ \"$XDG_CONFIG_HOME\" = " + shquote.Quote(fake) + " ]; " +
				"[ \"$GIT_ASKPASS\" = fixture-helper ]; [ -z \"${GIT_DIR+x}\" ]; " +
				"case \"$PWD\" in */app) [ \"$*\" = 'list -deps ./cmd/service ./cmd/worker' ] ;; */eval) [ \"$*\" = 'list -deps .' ] ;; *) exit 9 ;; esac\n" +
				"printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + "; printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + " >&2\n" + prefillFailure + ";;\n" +
				"build) [ \"$GOPROXY\" = off ]; [ \"$GOFLAGS\" = -mod=readonly ]; [ -z \"${GOAUTH+x}${GOOS+x}${GOARCH+x}${GOEXPERIMENT+x}\" ]; " +
				"out=''; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = -o ]; then shift; out=$1; fi; shift; done; " +
				"[ -n \"$out\" ]; printf 'placeholder, never executed' > \"$out\" ;;\n*) exit 10 ;;\nesac\n"
			require.NoError(t, os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte(script), 0o700))
			t.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("HOME", fake)
			t.Setenv("XDG_CONFIG_HOME", fake)
			t.Setenv("GOENV", config)
			t.Setenv("GOPROXY", "")
			t.Setenv("GOFLAGS", "-tags="+flagsMarker)
			t.Setenv("GOOS", "windows")
			t.Setenv("GOARCH", "386")
			t.Setenv("GOAMD64", "v4")
			t.Setenv("GOARM64", "v9.0")
			t.Setenv("GOEXPERIMENT", "loopvar")
			t.Setenv("GIT_ASKPASS", "fixture-helper")
			work := filepath.Join(t.TempDir(), "run")
			plan := goWorkVerificationPlan(repo, commit, work)
			plan.Binaries = map[string]string{"service": "./cmd/service", "worker": "./cmd/worker"}
			if mode == "no candidate packages" {
				plan.Binaries = nil
			}
			if mode == "lookup failure" || mode == "prefill failure" {
				report, err := Run(t.Context(), plan, os.Environ(), t.TempDir())
				require.Error(t, err)
				assert.NotContains(t, err.Error(), proxyMarker)
				assert.NotContains(t, err.Error(), flagsMarker)
				var stage *StageError
				require.ErrorAs(t, err, &stage)
				assert.Equal(t, StageBuild, stage.Stage)
				assert.Equal(t, summaryCandidateBuild, stage.Summary)
				encoded, encodeErr := json.Marshal(report.ForBuilders())
				require.NoError(t, encodeErr)
				assert.NotContains(t, string(encoded), proxyMarker)
				assert.NotContains(t, string(encoded), flagsMarker)
			} else {
				prepared, err := Prepare(t.Context(), plan, t.TempDir())
				require.NoError(t, err)
				assert.Len(t, prepared.Binding.Artifacts, len(plan.Binaries))
				data, err := os.ReadFile(calls)
				require.NoError(t, err)
				if mode == "no candidate packages" {
					assert.NotContains(t, string(data), "/candidate/app|list")
				}
				written := plan
				written.Tests = TestsWritten
				programEnv := ProgramEnv(written, os.Environ(), t.TempDir())
				assert.Len(t, programEnv, 2, "written code receives only PATH and its private TMPDIR")
				for _, value := range programEnv {
					assert.NotContains(t, value, proxyMarker)
					assert.NotContains(t, value, flagsMarker)
				}
			}
			require.NoError(t, filepath.WalkDir(work, func(name string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				data, err := os.ReadFile(name)
				if assert.NoError(t, err) {
					assert.NotContains(t, string(data), proxyMarker, name)
					assert.NotContains(t, string(data), flagsMarker, name)
				}
				return err
			}))
		})
	}
}

// TestWorkspacePreparationLoadsOnlyTheConfiguredCompileClosure is a small
// offline metadata proof: real Go loads packages but compiles or runs no code.
// Unused modules, unrelated packages/tests and operator-only build selectors
// must not add dependencies to the configured compile target.
func TestWorkspacePreparationLoadsOnlyTheConfiguredCompileClosure(t *testing.T) {
	repo, _ := goWorkVerificationFixture(t, true)
	for name, constraint := range map[string]string{
		"poison.go": "operator_only", "profile_js.go": "js", "profile_cgo.go": "cgo",
		"profile_experiment.go": "goexperiment.arenas", "profile_feature.go": "amd64.v4 || arm64.v9.0",
	} {
		text := "//go:build " + constraint + "\n\npackage main\nimport _ \"example.invalid/profile-mismatch\"\n"
		require.NoError(t, os.WriteFile(filepath.Join(repo, "app", name), []byte(text), 0o600))
	}
	config := filepath.Join(t.TempDir(), "go.env")
	require.NoError(t, os.WriteFile(config, []byte("GOOS=js\nGOARCH=wasm\nCGO_ENABLED=1\nGOFLAGS=-tags=operator_only\nGOEXPERIMENT=arenas\nGOAMD64=v4\nGOARM64=v9.0\n"), 0o600))
	t.Setenv("GOENV", config)
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOOS", "")
	t.Setenv("GOARCH", "")
	t.Setenv("CGO_ENABLED", "")
	t.Setenv("GOEXPERIMENT", "")
	t.Setenv("GOAMD64", "")
	t.Setenv("GOARM64", "")
	r := &runner{evidenceDir: t.TempDir()}
	toolchain, err := r.prepareBuild(t.Context(), "candidate", repo, filepath.Join(repo, "app"), t.TempDir(), []string{"."})
	require.NoError(t, err)
	assert.NotEmpty(t, toolchain.goroot)
	assert.Equal(t, os.Getenv("GOMODCACHE"), toolchain.modCache)
	entries, err := os.ReadDir(toolchain.modCache)
	require.NoError(t, err)
	assert.Empty(t, entries, "no remote module is needed by this local workspace target")
}

// TestModuleDownloadKeepsDiagnosticsPrivate retains standalone/written download
// evidence while returning only a static failure, status and private filename.
func TestModuleDownloadKeepsDiagnosticsPrivate(t *testing.T) {
	const user = "FAKE_PROXY_USER_300"
	const path = "FAKE_PROXY_PATH_300"
	const flag = "-FAKE_INVALID_GOFLAGS_300"
	for _, label := range []string{"candidate", "suite"} {
		for _, problem := range []string{"proxy", "flags"} {
			t.Run(label+"/"+problem, func(t *testing.T) {
				module, fake := t.TempDir(), t.TempDir()
				require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.invalid/app\ngo 1.27.3\nrequire example.invalid/missing v1.0.0\n"), 0o600))
				proxy := "http://" + user + ":fake-password@proxy.example.invalid/" + path
				diagnostic := "go: refusing to pass credentials to insecure URL: " + proxy + "/example.invalid/missing/@v/v1.0.0.mod"
				flags := ""
				if problem == "flags" {
					flags = flag
					diagnostic = "go: parsing $GOFLAGS: unknown flag " + flag
				}
				script := "#!/bin/sh\nset -eu\n[ \"$*\" = 'mod download' ]; [ \"$GOWORK\" = off ]; " +
					"[ \"$GOPROXY\" = " + shquote.Quote(proxy) + " ]; [ \"$GOFLAGS\" = " + shquote.Quote(flags) + " ]\n" +
					"printf '%s\\n' " + shquote.Quote(diagnostic) + "; printf '%s\\n' " + shquote.Quote(diagnostic) + " >&2\nexit 7\n"
				require.NoError(t, os.WriteFile(filepath.Join(fake, "go"), []byte(script), 0o700))
				t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("GOPROXY", proxy)
				t.Setenv("GOFLAGS", flags)
				r := &runner{evidenceDir: t.TempDir()}
				_, err := r.prepareGoBuild(t.Context(), label, module, t.TempDir(), "off", []string{"."})
				require.Error(t, err)
				assert.ErrorContains(t, err, "download "+label+" modules")
				assert.ErrorContains(t, err, "exit status 7")
				private := filepath.Join(r.evidenceDir, "go-mod-download-"+label+".stderr")
				assert.EqualError(t, err, "download "+label+" modules: go-mod-download-"+label+": exit status 7; diagnostic output is private in "+private)
				var process *exec.ExitError
				require.ErrorAs(t, err, &process)
				assert.Equal(t, 7, process.ExitCode())
				assert.Empty(t, process.Stderr, "typed error carries no private output")
				for _, marker := range []string{user, path, flag} {
					assert.NotContains(t, err.Error(), marker)
				}
				for _, suffix := range []string{"stdout", "stderr"} {
					data, readErr := os.ReadFile(filepath.Join(r.evidenceDir, "go-mod-download-"+label+"."+suffix))
					require.NoError(t, readErr)
					assert.Equal(t, diagnostic+"\n", string(data), "the user's diagnostic evidence remains available")
				}
			})
		}
	}
}
