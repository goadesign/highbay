// These tests carry real preparation through the existing supervisor/executor.
package verifyrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goadesign/highbay/internal/shquote"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"
)

// TestWorkspacePreparationKeepsSecretsOutOfSavedRuns uses real verify.Run and
// saved-result ownership. Go and the suite are trusted stand-ins; no native
// compilation, application execution or provider call occurs.
func TestWorkspacePreparationKeepsSecretsOutOfSavedRuns(t *testing.T) {
	const proxyMarker = "FAKE_PROXY_CREDENTIAL_73"
	const flagsMarker = "FAKE_GOFLAGS_CREDENTIAL_73"
	for _, mode := range []string{"success", "lookup failure", "prefill failure"} {
		t.Run(mode, func(t *testing.T) {
			repo := t.TempDir()
			for name, content := range map[string]string{
				"go.work":      "go 1.27.3\nuse (\n ./app\n ./eval\n)\n",
				"app/go.mod":   "module example.invalid/app\ngo 1.27.3\n",
				"app/main.go":  "package main\nfunc main() {}\n",
				"eval/go.mod":  "module example.invalid/eval\ngo 1.27.3\n",
				"eval/main.go": "package main\nfunc main() {}\n",
			} {
				name = filepath.Join(repo, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
				require.NoError(t, os.WriteFile(name, []byte(content), 0o600))
			}
			for _, args := range [][]string{{"init", "-q"}, {"add", "app", "eval", "go.work"},
				{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Save workspace fixture"}} {
				output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
				require.NoError(t, err, "%s", output)
			}
			head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
			require.NoError(t, err)
			fake := t.TempDir()
			goroot, cache := filepath.Join(fake, "goroot"), filepath.Join(fake, "cache")
			require.NoError(t, os.MkdirAll(filepath.Join(goroot, "bin"), 0o700))
			require.NoError(t, os.Mkdir(cache, 0o700))
			proxy := "https://fixture:" + proxyMarker + "@proxy.example.invalid"
			config := filepath.Join(fake, "go.env")
			require.NoError(t, os.WriteFile(config, []byte("GOPROXY="+proxy+"\nGOFLAGS=-tags="+flagsMarker+"\n"), 0o600))
			settings, err := json.Marshal(map[string]string{"GOROOT": goroot, "GOMODCACHE": cache, "GOPROXY": proxy, "GOAUTH": "off"})
			require.NoError(t, err)
			const artifactBytes = "placeholder, never executed"
			digest := sha256.Sum256([]byte(artifactBytes))
			evidence, err := json.Marshal(eval.Report{SuiteID: "workspace", Scenarios: []eval.ScenarioReport{{
				ID: "smoke", StartedAt: time.Now(), Passed: true, Result: &eval.Result{
					Checks:    []eval.Check{{Name: "ok", Passed: true}},
					Artifacts: []eval.Artifact{{Name: "candidate:app", URI: "sha256:" + hex.EncodeToString(digest[:])}},
				},
			}}})
			require.NoError(t, err)
			suiteFile := filepath.Join(fake, "suite")
			suite := "#!/bin/sh\nset -eu\n[ -z \"${GOPROXY+x}${GOFLAGS+x}${GOENV+x}${GOAUTH+x}\" ]\n" +
				"case \"${1-}\" in --describe) printf '%s\\n' '{\"id\":\"workspace\",\"environment\":\"sim\",\"scenarios\":[{\"id\":\"smoke\"}]}' ;; " +
				"*) printf '%s\\n' " + shquote.Quote(string(evidence)) + ";; esac\n"
			require.NoError(t, os.WriteFile(suiteFile, []byte(suite), 0o700))
			lookupExit, prefillExit := "", ""
			if mode == "lookup failure" {
				lookupExit = "exit 7\n"
			}
			if mode == "prefill failure" {
				prefillExit = "exit 8\n"
			}
			script := "#!/bin/sh\nset -eu\ncase \"$1\" in\n" +
				"env) if [ \"$2\" = -json ]; then printf '%s\\n' " + shquote.Quote(string(settings)) + "; printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + " >&2\n" + lookupExit +
				"else printf '%s\\n%s\\n' " + shquote.Quote(goroot) + " " + shquote.Quote(cache) + "; fi ;;\n" +
				"list) printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + "; printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + " >&2\n" + prefillExit + ";;\nmod) exit 0 ;;\n" +
				"build) [ \"$GOPROXY\" = off ]; [ \"$GOFLAGS\" = -mod=readonly ]; [ -z \"${GOENV+x}${GOAUTH+x}\" ]; " +
				"out=''; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = -o ]; then shift; out=$1; fi; shift; done; " +
				"case \"$PWD\" in */eval) cp " + shquote.Quote(suiteFile) + " \"$out\" ;; *) printf '%s' " + shquote.Quote(artifactBytes) + " > \"$out\" ;; esac; chmod 700 \"$out\" ;;\n*) exit 9 ;;\nesac\n"
			require.NoError(t, os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte(script), 0o700))
			t.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GOENV", config)
			t.Setenv("GOPROXY", "")
			t.Setenv("GOFLAGS", "-tags="+flagsMarker)
			t.Setenv(fakeModeEnv, "workspace-preparation")
			root, reports := t.TempDir(), t.TempDir()
			runID := NewRunID()
			_, err = Start(root, Request{RunID: runID, Owner: "alice", ChangeID: "chg", AcceptedAt: time.Now().UTC(), Timeout: 20 * time.Second,
				Plan: verify.Plan{CandidateRepo: repo, CandidateCommit: strings.TrimSpace(string(head)), CandidateModule: "app", Binaries: map[string]string{"app": "."},
					SuiteRepo: repo, SuiteCommit: strings.TrimSpace(string(head)), SuiteModule: "eval", SuiteCommand: ".", RequestRevision: "rev-1",
					WorkDir: filepath.Join(root, runID, "work"), ReportDir: reports, Tests: verify.TestsWritten},
			}, []string{os.Args[0]}, nil, t.TempDir())
			require.NoError(t, err)
			require.NoError(t, Wait(root, runID))
			_, state, err := Read(root, runID)
			require.NoError(t, err)
			if mode == "success" {
				require.Equal(t, StateCompleted, state.Kind)
				assert.True(t, state.Report.Passed)
			} else {
				require.Equal(t, StateNotCompleted, state.Kind)
				assert.Equal(t, FailureExecution, state.Result.Failure)
				assert.Equal(t, string(verify.StageBuild), state.Result.Stage)
				log, err := os.ReadFile(filepath.Join(root, runID, "executor.log"))
				require.NoError(t, err)
				assert.Contains(t, string(log), "exit status", "a truthful process failure remains in the evaluator log")
			}
			for _, folder := range []string{root, reports} {
				require.NoError(t, filepath.WalkDir(folder, func(name string, entry fs.DirEntry, err error) error {
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
			}
			public, err := json.Marshal(state.Report.ForBuilders())
			require.NoError(t, err)
			assert.NotContains(t, string(public), proxyMarker)
			assert.NotContains(t, string(public), flagsMarker)
		})
	}
}

// TestModuleDownloadKeepsSavedFailuresPrivate exercises real saved-result and
// executor-log consumers for standalone candidate and fixed-off written suites.
// Only the user's specific private download evidence retains fake diagnostics.
func TestModuleDownloadKeepsSavedFailuresPrivate(t *testing.T) {
	const user = "FAKE_PROXY_USER_301"
	const path = "FAKE_PROXY_PATH_301"
	const flag = "-FAKE_INVALID_GOFLAGS_301"
	for _, target := range []string{"standalone", "written"} {
		for _, problem := range []string{"proxy", "flags"} {
			t.Run(target+"/"+problem, func(t *testing.T) {
				repo := t.TempDir()
				files := map[string]string{"app/go.mod": "module example.invalid/app\ngo 1.27.3\nrequire example.invalid/missing v1.0.0\n", "app/main.go": "package main\nfunc main() {}\n",
					"eval/go.mod": "module example.invalid/eval\ngo 1.27.3\nrequire example.invalid/missing v1.0.0\n", "eval/main.go": "package main\nfunc main() {}\n"}
				if target == "written" {
					files["go.work"] = "go 1.27.3\nuse (\n ./app\n ./eval\n)\n"
				}
				for name, content := range files {
					name = filepath.Join(repo, name)
					require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o700))
					require.NoError(t, os.WriteFile(name, []byte(content), 0o600))
				}
				for _, args := range [][]string{{"init", "-q"}, {"add", "--all"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "-qm", "Save module fixture"}} {
					output, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput()
					require.NoError(t, err, "%s", output)
				}
				head, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
				require.NoError(t, err)
				fake := t.TempDir()
				goroot, cache := filepath.Join(fake, "goroot"), filepath.Join(fake, "cache")
				require.NoError(t, os.MkdirAll(filepath.Join(goroot, "bin"), 0o700))
				require.NoError(t, os.Mkdir(cache, 0o700))
				proxy := "http://" + user + ":fake-password@proxy.example.invalid/" + path
				flags := ""
				diagnostic := "go: refusing to pass credentials to insecure URL: " + proxy + "/example.invalid/missing/@v/v1.0.0.mod"
				if problem == "flags" {
					flags = flag
					diagnostic = "go: parsing $GOFLAGS: unknown flag " + flag
				}
				settings, err := json.Marshal(map[string]string{"GOROOT": goroot, "GOMODCACHE": cache, "GOPROXY": proxy, "GOAUTH": "off"})
				require.NoError(t, err)
				script := "#!/bin/sh\nset -eu\ncase \"$1\" in\n" +
					"env) [ \"$2\" = -json ]; printf '%s\\n' " + shquote.Quote(string(settings)) + " ;;\nlist) exit 0 ;;\n" +
					"build) [ \"$GOPROXY\" = off ]; [ \"$GOFLAGS\" = -mod=readonly ]; out=''; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = -o ]; then shift; out=$1; fi; shift; done; printf 'placeholder, never executed' > \"$out\" ;;\n" +
					"mod) [ \"$*\" = 'mod download' ]; [ \"$GOWORK\" = off ]; [ \"$GOPROXY\" = " + shquote.Quote(proxy) + " ]; [ \"$GOFLAGS\" = " + shquote.Quote(flags) + " ]; " +
					"printf '%s\\n' " + shquote.Quote(diagnostic) + "; printf '%s\\n' " + shquote.Quote(diagnostic) + " >&2; exit 7 ;;\n*) exit 9 ;;\nesac\n"
				require.NoError(t, os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte(script), 0o700))
				t.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("GOPROXY", proxy)
				t.Setenv("GOFLAGS", flags)
				t.Setenv(fakeModeEnv, "workspace-preparation")
				root, reports := t.TempDir(), t.TempDir()
				runID := NewRunID()
				_, err = Start(root, Request{RunID: runID, Owner: "alice", ChangeID: "chg", AcceptedAt: time.Now().UTC(), Timeout: 20 * time.Second,
					Plan: verify.Plan{CandidateRepo: repo, CandidateCommit: strings.TrimSpace(string(head)), CandidateModule: "app", Binaries: map[string]string{"app": "."},
						SuiteRepo: repo, SuiteCommit: strings.TrimSpace(string(head)), SuiteModule: "eval", SuiteCommand: ".", RequestRevision: "rev-1",
						WorkDir: filepath.Join(root, runID, "work"), ReportDir: reports, Tests: verify.TestsWritten},
				}, []string{os.Args[0]}, nil, t.TempDir())
				require.NoError(t, err)
				require.NoError(t, Wait(root, runID))
				_, state, err := Read(root, runID)
				require.NoError(t, err)
				require.Equal(t, StateNotCompleted, state.Kind)
				assert.Equal(t, FailureExecution, state.Result.Failure)
				assert.Equal(t, string(verify.StageBuild), state.Result.Stage)
				label, summary := "candidate", "The candidate did not build."
				if target == "written" {
					label, summary = "suite", "The evaluator suite did not build."
				}
				assert.Equal(t, summary, state.Result.Summary)
				base := filepath.Join(root, runID, "work", "evidence", "go-mod-download-"+label)
				log, err := os.ReadFile(filepath.Join(root, runID, "executor.log"))
				require.NoError(t, err)
				assert.Contains(t, string(log), "exit status 7")
				assert.Contains(t, string(log), "diagnostic output is private in "+base+".stderr")
				for _, folder := range []string{root, reports} {
					require.NoError(t, filepath.WalkDir(folder, func(name string, entry fs.DirEntry, err error) error {
						if err != nil || entry.IsDir() {
							return err
						}
						data, err := os.ReadFile(name)
						if !assert.NoError(t, err) {
							return err
						}
						if name == base+".stdout" || name == base+".stderr" {
							assert.Equal(t, diagnostic+"\n", string(data), "the only permitted private diagnostic files")
						} else {
							for _, marker := range []string{user, path, flag} {
								assert.NotContains(t, string(data), marker, name)
							}
						}
						return nil
					}))
				}
				public, err := json.Marshal(state.Report.ForBuilders())
				require.NoError(t, err)
				for _, marker := range []string{user, path, flag} {
					assert.NotContains(t, string(public), marker)
				}
			})
		}
	}
}
