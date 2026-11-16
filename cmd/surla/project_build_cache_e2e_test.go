//go:build e2e

// These native fixtures exercise the project cache through the actual internal
// child entrypoint. They author trusted programs, rather than claiming to test
// model-authored acceptance or the public change/build admission workflow.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cacheRun binds a launched supervisor to its immutable run ID for exact
// cancellation and state reads when several consumers overlap.
type cacheRun struct {
	// id is the run directory name returned by admission.
	id string
	// supervisor is the process Start returned for this exact run.
	supervisor int
}

// cacheSuiteSource is a dependency-free trusted test program. Its live mode
// announces readiness; verification actually executes each supplied artifact
// and binds its measured digest in the report.
const cacheSuiteSource = `package main

import (
 "crypto/sha256"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "os"
 "os/exec"
 "os/signal"
 "path/filepath"
 "strings"
 "syscall"
 "time"
)

func main() {
 pgid, err := syscall.Getpgid(0)
 if err != nil { panic(err) }
 if len(os.Args) > 1 && os.Args[1] == "--live" {
  if err := os.WriteFile(filepath.Join(os.TempDir(),"cache-live-process"),[]byte(fmt.Sprintf("%d %d",os.Getpid(),pgid)),0600); err != nil { panic(err) }
  if err := json.NewEncoder(os.Stdout).Encode(map[string]string{"url": "http://127.0.0.1:4100"}); err != nil { panic(err) }
  ending := make(chan os.Signal, 1)
  signal.Notify(ending, syscall.SIGTERM)
  <-ending
  return
 }
 if len(os.Args) > 1 && os.Args[1] == "--describe" {
  declaration := map[string]any{
   "id": "cache_fixture", "environment": "native",
   "scenarios": []map[string]any{{"id": "runs", "tags": []string{}}},
  }
  if err := json.NewEncoder(os.Stdout).Encode(declaration); err != nil { panic(err) }
  return
 }
 artifacts := []map[string]string{}
 passed := true
 for i := 1; i+1 < len(os.Args); i += 2 {
  if os.Args[i] != "--artifact" { continue }
  name, path, _ := strings.Cut(os.Args[i+1], "=")
  data, err := os.ReadFile(path)
  if err != nil { panic(err) }
  sum := sha256.Sum256(data)
  artifacts = append(artifacts, map[string]string{
   "name": "candidate:" + name, "uri": "sha256:" + hex.EncodeToString(sum[:]),
  })
  out, err := exec.Command(path).Output()
  passed = passed && err == nil && (string(out) == "one\n" || string(out) == "two\n")
 }
 now := time.Now().UTC()
 report := map[string]any{
  "suite_id": "cache_fixture", "started_at": now, "duration": 0, "passed": passed,
  "scenarios": []any{map[string]any{
   "id": "runs", "started_at": now, "duration": 0, "passed": passed,
   "result": map[string]any{"artifacts": artifacts,"output": fmt.Sprintf("%d %d",os.Getpid(),pgid)},
  }},
 }
 if err := json.NewEncoder(os.Stdout).Encode(report); err != nil { panic(err) }
}
`

// TestProjectBuildCacheNativeLifecycle covers live and verification for both
// test-source profiles through real supervised processes and Go compilation.
// The project server is restarted between operations; public admission is
// covered separately by the whole journey test.
func TestProjectBuildCacheNativeLifecycle(t *testing.T) {
	for _, source := range []verify.TestsSource{verify.TestsEvaluator, verify.TestsWritten} {
		t.Run(string(source), func(t *testing.T) {
			workspace := isolateServeWorkspace(t)
			isolateProviderFamilies(t)
			for _, key := range []string{verify.LiveModelProviderEnv, verify.LiveModelNameEnv, verify.LiveModelGatewayEnv} {
				t.Setenv(key, "")
			}
			t.Setenv("GOPROXY", "off")
			t.Setenv("GOTOOLCHAIN", "local")
			files := map[string]string{"go.mod": "module example.invalid/cacheapp\ngo 1.27.3\n", "main.go": "package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"one\")}\n", "eval/go.mod": "module example.invalid/cacheapp/eval\ngo 1.27.3\n", "eval/main.go": cacheSuiteSource, "cmd/worker/main.go": "package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"one\")}\n"}
			for name, content := range files {
				path := filepath.Join(workspace, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
				require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			}
			cacheGit(t, workspace, "init", "-q")
			cacheGit(t, workspace, "add", "go.mod", "main.go", "cmd", "eval")
			cacheGit(t, workspace, "-c", "user.name=Surla Test", "-c", "user.email=test@example.com", "commit", "-qm", "Initial native fixture")
			commit := cacheGit(t, workspace, "rev-parse", "HEAD")
			state, err := fixtureProjectState(workspace)
			require.NoError(t, err)
			if source == verify.TestsEvaluator {
				config := verificationFile{CandidateRepo: workspace, CandidateModule: ".", Binaries: map[string]string{"app": ".", "worker": "./cmd/worker"}, SuiteRepo: workspace, SuiteCommit: commit, SuiteModule: "eval", SuiteCommand: ".", Timeout: "2m"}
				data, err := json.Marshal(config)
				require.NoError(t, err)
				file := filepath.Join(t.TempDir(), "verification.json")
				require.NoError(t, os.WriteFile(file, data, 0o600))
				require.NoError(t, runProjectSettings("verification", []string{"set", file}, io.Discard))
			}
			opts, err := projectServeOptions(state, os.Environ())
			require.NoError(t, err)
			cache := opts.BuildCacheDir
			assert.Equal(t, filepath.Join(state.Dir, "build-cache"), cache)
			plan := verify.Plan{CandidateRepo: workspace, CandidateCommit: commit, CandidateModule: ".", Binaries: map[string]string{"app": ".", "worker": "./cmd/worker"}, SuiteRepo: workspace, SuiteCommit: commit, SuiteModule: "eval", SuiteCommand: ".", Tests: source, RequestRevision: "cache-fixture"}
			self, err := selfCommand()
			require.NoError(t, err)
			live, err := verifyrun.LaunchLive(controlplane.LiveRoot(opts.CardsDir), plan, "cache-fixture-key", self, os.Environ(), nil, cache)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, live.Stop()) })
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			requireCacheLiveReady(t, live, ctx)
			liveIdentity, err := os.ReadFile(filepath.Join(live.Dir(), "tmp", "cache-live-process"))
			require.NoError(t, err)
			livePID, liveGroup := cacheFixtureProcessIDs(t, string(liveIdentity))
			liveWork := filepath.Join(live.Dir(), "work")
			assertNoOperationCache(t, liveWork)
			liveDigest := cacheDigest(t, filepath.Join(liveWork, "candidate-bin", "app"))
			liveSuiteDigest := cacheDigest(t, filepath.Join(liveWork, "suite-bin", "suite"))
			raw, err := os.ReadFile(filepath.Join(live.Dir(), "live.json"))
			require.NoError(t, err)
			assert.NotContains(t, string(raw), "build_cache")
			assert.NotContains(t, string(raw), cache)
			first := cacheEntries(t, cache)
			require.NotEmpty(t, first)
			t.Logf("first native live build: %d retained cache files; metadata excluded: README, trim.txt, .surla-private", len(first))
			require.NoError(t, live.Stop())
			requireCacheFixtureProcessesGone(t, livePID, liveGroup)
			report := runCacheVerification(t, opts, plan, cache, liveSuiteDigest)
			assert.Equal(t, liveDigest, report.Binding.Artifacts["app"])
			assert.Equal(t, first, cacheEntries(t, cache), "unchanged inputs reuse the retained entry set")
			// Closing/reopening the owner must retain cache identity and contents.
			server := startSharedServerProcess(t, workspace, os.Getenv("SURLA_TEST_USER_CONFIG_DIR"))
			server.stop()
			server = startSharedServerProcess(t, workspace, os.Getenv("SURLA_TEST_USER_CONFIG_DIR"))
			reopened, err := projectServeOptions(state, os.Environ())
			require.NoError(t, err)
			assert.Equal(t, cache, reopened.BuildCacheDir)
			again := runCacheVerification(t, reopened, plan, reopened.BuildCacheDir, liveSuiteDigest)
			assert.Equal(t, report.Binding.Artifacts, again.Binding.Artifacts)
			assert.Equal(t, first, cacheEntries(t, cache))
			server.stop()
			coldCache := filepath.Join(t.TempDir(), "cold-cache")
			cold := runCacheVerification(t, reopened, plan, coldCache, liveSuiteDigest)
			assert.NotEmpty(t, cacheEntries(t, coldCache), "the cold control really populates its cache")
			assert.Equal(t, cold.Binding.Artifacts, again.Binding.Artifacts)
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"two\")}\n"), 0o600))
			cacheGit(t, workspace, "add", "main.go")
			cacheGit(t, workspace, "-c", "user.name=Surla Test", "-c", "user.email=test@example.com", "commit", "-qm", "Change candidate output")
			plan.CandidateCommit = cacheGit(t, workspace, "rev-parse", "HEAD")
			changed := runCacheVerification(t, reopened, plan, cache, liveSuiteDigest)
			changedCold := runCacheVerification(t, reopened, plan, filepath.Join(t.TempDir(), "changed-cold"), liveSuiteDigest)
			assert.NotEqual(t, report.Binding.Artifacts["app"], changed.Binding.Artifacts["app"])
			assert.Equal(t, report.Binding.Artifacts["worker"], changed.Binding.Artifacts["worker"], "the unchanged worker keeps its own output")
			assert.NotEqual(t, changed.Binding.Artifacts["app"], changed.Binding.Artifacts["worker"])
			assert.Equal(t, changedCold.Binding.Artifacts, changed.Binding.Artifacts)
			overlapLive, err := verifyrun.LaunchLive(controlplane.LiveRoot(opts.CardsDir), plan, "overlap-key", self, os.Environ(), nil, cache)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, overlapLive.Stop()) })
			overlapCtx, overlapCancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer overlapCancel()
			requireCacheLiveReady(t, overlapLive, overlapCtx)
			overlapIdentity, err := os.ReadFile(filepath.Join(overlapLive.Dir(), "tmp", "cache-live-process"))
			require.NoError(t, err)
			overlapPID, overlapGroup := cacheFixtureProcessIDs(t, string(overlapIdentity))
			earlier := plan
			earlier.CandidateCommit = commit
			earlierID := startCacheVerification(t, reopened, earlier, cache)
			changedID := startCacheVerification(t, reopened, plan, cache)
			require.NoError(t, overlapLive.Stop())
			requireCacheFixtureProcessesGone(t, overlapPID, overlapGroup)
			assert.Equal(t, cold.Binding.Artifacts, waitCacheVerification(t, reopened, earlierID, cache, liveSuiteDigest).Binding.Artifacts)
			assert.Equal(t, changedCold.Binding.Artifacts, waitCacheVerification(t, reopened, changedID, cache, liveSuiteDigest).Binding.Artifacts)
			marker := filepath.Join(t.TempDir(), "candidate-started")
			blockingSource := fmt.Sprintf("package main\nimport (\"fmt\";\"os\";\"syscall\";\"time\")\nfunc main(){pgid,err:=syscall.Getpgid(0);if err!=nil{panic(err)};if err:=os.WriteFile(%q,[]byte(fmt.Sprintf(\"%%d %%d\",os.Getpid(),pgid)),0600);err!=nil{panic(err)};time.Sleep(time.Hour)}\n", marker)
			require.NoError(t, os.WriteFile(filepath.Join(workspace, "main.go"), []byte(blockingSource), 0o600))
			cacheGit(t, workspace, "add", "main.go")
			cacheGit(t, workspace, "-c", "user.name=Surla Test", "-c", "user.email=test@example.com", "commit", "-qm", "Block one native verification")
			blockedPlan := plan
			blockedPlan.CandidateCommit = cacheGit(t, workspace, "rev-parse", "HEAD")
			server = startSharedServerProcess(t, workspace, os.Getenv("SURLA_TEST_USER_CONFIG_DIR"))
			blocked := startCacheVerification(t, reopened, blockedPlan, cache)
			survivor := startCacheVerification(t, reopened, plan, cache)
			require.Eventually(t, func() bool { _, err := os.Stat(marker); return err == nil }, 30*time.Second, 10*time.Millisecond, "the actual candidate must run before cancellation")
			blockedIdentity, err := os.ReadFile(marker)
			require.NoError(t, err)
			blockedPID, blockedGroup := cacheFixtureProcessIDs(t, string(blockedIdentity))
			server.stop()
			require.NoError(t, syscall.Kill(blocked.supervisor, syscall.SIGTERM))
			require.NoError(t, verifyrun.Wait(reopened.RunsDir, blocked.id))
			_, interrupted, err := verifyrun.Read(reopened.RunsDir, blocked.id)
			require.NoError(t, err)
			assert.Equal(t, verifyrun.StateNotCompleted, interrupted.Kind)
			assert.Equal(t, verifyrun.FailureInterrupted, interrupted.Result.Failure)
			assert.Equal(t, "The verification was canceled.", interrupted.Result.Summary)
			assert.FileExists(t, filepath.Join(reopened.RunsDir, blocked.id, "result.json"), "the supervisor publishes its outcome only after its process group is empty")
			requireCacheFixtureProcessesGone(t, blockedPID, blockedGroup)
			assert.Equal(t, changedCold.Binding.Artifacts, waitCacheVerification(t, reopened, survivor, cache, liveSuiteDigest).Binding.Artifacts)
			retained := cacheEntries(t, cache)
			require.NoError(t, os.RemoveAll(filepath.Join(reopened.RunsDir, blocked.id)))
			assert.Equal(t, retained, cacheEntries(t, cache), "pruning one stopped run preserves the project cache")
			require.NoError(t, os.RemoveAll(cache)) // exact owned cache; every operation above is idle
			recreated := runCacheVerification(t, reopened, plan, cache, liveSuiteDigest)
			assert.NotEmpty(t, cacheEntries(t, cache))
			assert.Equal(t, changed.Binding.Artifacts, recreated.Binding.Artifacts)
			cacheInfo, err := os.Stat(cache)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o700), cacheInfo.Mode().Perm())
			_, err = projectServeOptions(state, os.Environ())
			require.NoError(t, err, "recreated cache must satisfy the next startup's private directory contract")
			afterDeletion := startSharedServerProcess(t, workspace, os.Getenv("SURLA_TEST_USER_CONFIG_DIR"))
			afterDeletion.stop()
			if source == verify.TestsEvaluator {
				beforeStandalone := cacheEntries(t, cache)
				for _, work := range []string{filepath.Join(t.TempDir(), "absolute-work"), "relative-work"} {
					flags := []string{"-candidate-repo", workspace, "-candidate-commit", plan.CandidateCommit, "-binary", "app=.", "-suite-repo", workspace, "-suite-commit", commit, "-suite-module", "eval", "-suite-command", ".", "-request-revision", "standalone-cache-fixture", "-work-dir", work, "-report-dir", t.TempDir(), "-timeout", "2m"}
					require.NoError(t, runVerify(flags))
					assert.NotEmpty(t, cacheEntries(t, filepath.Join(work, "gocache")))
					assert.Equal(t, beforeStandalone, cacheEntries(t, cache), "standalone compilation does not select project storage")
					if work == "relative-work" {
						used := t.TempDir()
						require.NoError(t, os.WriteFile(filepath.Join(used, "existing"), []byte("preserve me"), 0o600))
						refused := append([]string{}, flags...)
						for i, flag := range refused {
							if flag == "-work-dir" {
								refused[i+1] = used
								break
							}
						}
						assert.ErrorContains(t, runVerify(refused), "must be missing or empty")
						assert.NoDirExists(t, filepath.Join(used, "gocache"), "cache creation cannot defeat the fresh-work-dir check")
					}
				}
			}
		})
	}
}

// runCacheVerification uses Start's real supervisor and execute entrypoint and
// reads its owner-decoded terminal report. Each operation owns fresh scratch.
func runCacheVerification(t *testing.T, opts controlplane.Options, plan verify.Plan, cache, expectedSuiteDigest string) verify.Report {
	t.Helper()
	return waitCacheVerification(t, opts, startCacheVerification(t, opts, plan, cache), cache, expectedSuiteDigest)
}

// startCacheVerification separates admission from waiting so tests can overlap
// independent supervised consumers of one project cache.
func startCacheVerification(t *testing.T, opts controlplane.Options, plan verify.Plan, cache string) cacheRun {
	t.Helper()
	runID := verifyrun.NewRunID()
	plan.WorkDir = filepath.Join(opts.RunsDir, runID, "work")
	if opts.Verification != nil {
		plan.ReportDir = opts.Verification.Plan.ReportDir
	} else {
		plan.ReportDir = opts.WrittenTestRuns.ReportDir
	}
	self, err := selfCommand()
	require.NoError(t, err)
	supervisor, err := verifyrun.Start(opts.RunsDir, verifyrun.Request{RunID: runID, Owner: "cache-fixture", AcceptedAt: time.Now().UTC(), Timeout: 2 * time.Minute, Plan: plan}, self, nil, cache)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, status, readErr := verifyrun.Read(opts.RunsDir, runID)
		if errors.Is(readErr, os.ErrNotExist) {
			return
		} // this test may explicitly prune a stopped run
		if !assert.NoError(t, readErr) {
			return
		}
		if status.Kind == verifyrun.StateRunning {
			assert.NoError(t, syscall.Kill(supervisor, syscall.SIGTERM))
			assert.NoError(t, verifyrun.Wait(opts.RunsDir, runID))
		}
	})
	return cacheRun{id: runID, supervisor: supervisor}
}

// waitCacheVerification consumes the owner's strict saved record and report,
// preserving the exact operation identity instead of choosing a latest run.
func waitCacheVerification(t *testing.T, opts controlplane.Options, run cacheRun, cache, expectedSuiteDigest string) verify.Report {
	t.Helper()
	runID := run.id
	require.NoError(t, verifyrun.Wait(opts.RunsDir, runID))
	req, status, err := verifyrun.Read(opts.RunsDir, runID)
	require.NoError(t, err)
	if status.Kind != verifyrun.StateCompleted {
		data, readErr := os.ReadFile(filepath.Join(opts.RunsDir, runID, "executor.log"))
		t.Fatalf("verification did not complete: %+v; log read: %v; %s", status, readErr, data)
	}
	require.True(t, status.Report.Passed)
	require.Len(t, status.Report.Evidence.Scenarios, 1)
	require.NotNil(t, status.Report.Evidence.Scenarios[0].Result)
	suitePID, suiteGroup := cacheFixtureProcessIDs(t, status.Report.Evidence.Scenarios[0].Result.Output)
	requireCacheFixtureProcessesGone(t, suitePID, suiteGroup)
	suiteDigest := cacheDigest(t, filepath.Join(req.Plan.WorkDir, "suite-bin", "suite"))
	assert.Equal(t, expectedSuiteDigest, suiteDigest, "every suite binary matches the initial cold live build")
	t.Logf("native verification %s: candidate digests %v; suite digest %s", runID, status.Report.Binding.Artifacts, suiteDigest)
	assertNoOperationCache(t, req.Plan.WorkDir)
	raw, err := os.ReadFile(filepath.Join(opts.RunsDir, runID, "request.json"))
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "build_cache")
	assert.NotContains(t, string(raw), cache)
	return status.Report
}

// assertNoOperationCache checks the consumer's storage, including nested paths.
func assertNoOperationCache(t *testing.T, work string) {
	t.Helper()
	require.NoError(t, filepath.WalkDir(work, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			assert.NotEqual(t, "gocache", entry.Name(), path)
			assert.NotEqual(t, "build-cache", entry.Name(), path)
		}
		return nil
	}))
}

// cacheEntries returns actual compiled-package/index names, excluding Go's
// maintenance files and the composition root's existing private marker.
func cacheEntries(t *testing.T, cache string) []string {
	t.Helper()
	var names []string
	require.NoError(t, filepath.WalkDir(cache, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		assert.False(t, strings.HasPrefix(entry.Name(), "go-build"), path)
		assert.False(t, strings.HasPrefix(entry.Name(), ".surla-stage-"), path)
		if !entry.IsDir() && entry.Name() != "README" && entry.Name() != "trim.txt" && entry.Name() != operatorPrivateMarker {
			relative, err := filepath.Rel(cache, path)
			if err != nil {
				return err
			}
			names = append(names, relative)
		}
		return nil
	}))
	return names
}

// cacheDigest measures published bytes without executing an artifact.
func cacheDigest(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// cacheGit uses the test-owned repository and fixed author flags at commit.
func cacheGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return strings.TrimSpace(string(out))
}

// requireCacheLiveReady retains the bounded protected executor detail before a
// failing fixture's cleanup removes its operation directory.
func requireCacheLiveReady(t *testing.T, live *verifyrun.LiveProcess, ctx context.Context) {
	t.Helper()
	err := live.AwaitReady(ctx)
	var failure *verifyrun.LiveStartError
	if errors.As(err, &failure) {
		t.Logf("live executor detail: %s", failure.Detail)
	}
	require.NoError(t, err)
}

// cacheFixtureProcessIDs decodes OS facts written by the trusted test program;
// positive IDs are required before the test probes either exact process/group.
func cacheFixtureProcessIDs(t *testing.T, identity string) (int, int) {
	t.Helper()
	var pid, pgid int
	count, err := fmt.Sscanf(identity, "%d %d", &pid, &pgid)
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Greater(t, pid, 0)
	require.Greater(t, pgid, 0)
	return pid, pgid
}

// requireCacheFixtureProcessesGone observes actual child and group absence,
// independently of Go test's exit status or a report's passing verdict.
func requireCacheFixtureProcessesGone(t *testing.T, pid, pgid int) {
	t.Helper()
	require.Eventually(t, func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) && errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
	}, 10*time.Second, 20*time.Millisecond, "owned fixture PID %d / group %d remained active", pid, pgid)
	t.Logf("owned fixture PID %d and process group %d are absent (ESRCH)", pid, pgid)
}
