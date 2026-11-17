package candidate

import (
	"bufio"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/goadesign/surla/internal/procgroup"
	"github.com/goadesign/surla/internal/testmodule"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// probeResult mirrors the probe's output line.
type probeResult struct {
	Action  string `json:"action"`
	Allowed bool   `json:"allowed"`
	Detail  string `json:"detail"`
}

// libGoMod is the go.mod of the single-package test modules.
const libGoMod = "module example.com/lib\n\ngo 1.26\n"

// TestExecGivesTheProcessOnlyItsEnvironment runs the probe through Exec with a
// variable set in this test's own environment: the probe sees only HOME and
// TMPDIR, both its physical state directory, and the entries of Spec.Env, and
// a relative write lands in that state directory.
func TestExecGivesTheProcessOnlyItsEnvironment(t *testing.T) {
	probe := buildProbe(t)
	t.Setenv("EVAL_SECRET", "must not leak")
	state := t.TempDir()
	physicalState, err := filepath.EvalSymlinks(state)
	require.NoError(t, err)
	p, err := Exec(t.Context(), probe, Spec{
		Args:     []string{"env:EVAL_SECRET", "env:PATH", "env:GIVEN", "env:HOME", "env:TMPDIR", "write:ok.txt"},
		Env:      []string{"GIVEN=explicit"},
		StateDir: state,
		LogFile:  filepath.Join(t.TempDir(), "probe.log"),
	})
	require.NoError(t, err)
	out, truncated, err := excerptAfterExit(p)
	require.NoError(t, err)
	require.False(t, truncated)
	got := probeResults(t, out)
	assert.False(t, got["env:EVAL_SECRET"].Allowed, "the caller's environment is not inherited")
	assert.False(t, got["env:PATH"].Allowed, "only Spec.Env, HOME and TMPDIR are set")
	assert.Equal(t, probeResult{Action: "env:GIVEN", Allowed: true, Detail: "explicit"}, got["env:GIVEN"])
	assert.Equal(t, physicalState, got["env:HOME"].Detail)
	assert.Equal(t, physicalState, got["env:TMPDIR"].Detail)
	assert.True(t, got["write:ok.txt"].Allowed, got["write:ok.txt"].Detail)
	assert.FileExists(t, filepath.Join(physicalState, "ok.txt"), "the working directory is the state directory")
}

func TestExecRefusesAModifiedBinary(t *testing.T) {
	probe := buildProbe(t)
	require.NoError(t, os.Chmod(filepath.Dir(probe.path), 0o700))
	require.NoError(t, os.Chmod(probe.path, 0o700))
	require.NoError(t, os.WriteFile(probe.path, []byte("tampered"), 0o700))
	_, err := Exec(t.Context(), probe, Spec{StateDir: t.TempDir(), LogFile: filepath.Join(t.TempDir(), "x.log")})
	require.ErrorIs(t, err, ErrDigestMismatch)
}

func TestPinIsolatesTheArtifactFromItsSource(t *testing.T) {
	src := filepath.Join(t.TempDir(), "bin")
	require.NoError(t, os.WriteFile(src, []byte("original"), 0o700))
	store := filepath.Join(t.TempDir(), "artifacts")
	t.Cleanup(func() { require.NoError(t, RemoveStore(store)) })
	a, err := Pin(store, src)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(src, []byte("replaced after pinning"), 0o700))
	got, err := fileDigest(a.path)
	require.NoError(t, err)
	assert.Equal(t, a.Digest(), got, "the pinned copy keeps the original bytes")
	info, err := os.Stat(a.path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o500), info.Mode().Perm())
}

// TestPinKeepsItsBinaryAfterTheCallerChangesDirectories pins through a relative
// symlink and then reads the same copy from another working directory.
func TestPinKeepsItsBinaryAfterTheCallerChangesDirectories(t *testing.T) {
	base := t.TempDir()
	t.Chdir(base)
	require.NoError(t, os.Mkdir("storage", 0o700))
	require.NoError(t, os.Symlink("storage", "alias"))
	require.NoError(t, os.WriteFile("source", []byte("pinned bytes"), 0o700))
	store := filepath.Join(base, "storage", "artifacts")
	t.Cleanup(func() { require.NoError(t, RemoveStore(store)) })

	art, err := Pin(filepath.Join("alias", "artifacts"), "source")
	require.NoError(t, err)
	t.Chdir(t.TempDir())
	got, err := fileDigest(art.path)
	require.NoError(t, err)
	assert.Equal(t, art.Digest(), got)
	assert.True(t, filepath.IsAbs(art.path))
	resolved, err := filepath.EvalSymlinks(art.path)
	require.NoError(t, err)
	assert.Equal(t, resolved, art.path)
}

// TestExecUsesRelativeSymlinkedState preserves the physical state directory
// selected by relative paths and by symlinks followed by parent components.
func TestExecUsesRelativeSymlinkedState(t *testing.T) {
	probe := buildProbe(t)
	base := t.TempDir()
	t.Chdir(base)
	require.NoError(t, os.Mkdir("state", 0o700))
	require.NoError(t, os.Symlink("state", "state-link"))
	require.NoError(t, os.MkdirAll("target/nested", 0o700))
	require.NoError(t, os.Mkdir("target/state", 0o700))
	require.NoError(t, os.Symlink("target/nested", "alias"))
	physicalBase, err := filepath.EvalSymlinks(base)
	require.NoError(t, err)
	cases := []struct {
		name string
		// stateArg keeps the caller's spelling, including symlinks before "..".
		stateArg string
		// state is the physical directory that spelling must select.
		state string
	}{
		{"relative_symlink", "state-link", filepath.Join(physicalBase, "state")},
		{"relative_parent", "alias/../state", filepath.Join(physicalBase, "target", "state")},
		{"absolute_parent", base + "/alias/../state", filepath.Join(physicalBase, "target", "state")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			writeAction := "write:" + tc.name + ".txt"
			proc, err := Exec(ctx, probe, Spec{
				Args:     []string{writeAction, "env:HOME", "env:TMPDIR"},
				StateDir: tc.stateArg,
				LogFile:  tc.name + ".log",
			})
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, proc.Kill()) })
			out, truncated, err := excerptAfterExit(proc)
			require.NoError(t, err)
			require.False(t, truncated)
			results := make(map[string]probeResult)
			scanner := bufio.NewScanner(strings.NewReader(out))
			for scanner.Scan() {
				var result probeResult
				require.NoError(t, json.Unmarshal(scanner.Bytes(), &result))
				results[result.Action] = result
			}
			require.NoError(t, scanner.Err())
			require.Len(t, results, 3)
			for _, action := range []string{writeAction, "env:HOME", "env:TMPDIR"} {
				assert.True(t, results[action].Allowed, action)
			}
			assert.Equal(t, tc.state, results["env:HOME"].Detail)
			assert.Equal(t, tc.state, results["env:TMPDIR"].Detail)
			assert.FileExists(t, filepath.Join(tc.state, tc.name+".txt"))
		})
	}
}

func TestExcerptIsBoundedAndReportsTruncation(t *testing.T) {
	probe := buildProbe(t)
	state := t.TempDir()
	p, err := Exec(t.Context(), probe, Spec{
		Args:     []string{"env:A", "env:B", "env:C"},
		StateDir: state,
		LogFile:  filepath.Join(t.TempDir(), "probe.log"),
	})
	require.NoError(t, err)
	require.NoError(t, p.Wait(), "the probe reports failed lookups in its output and exits normally")
	full, truncated, err := p.Excerpt(1 << 20)
	require.NoError(t, err)
	assert.False(t, truncated)
	tail, truncated, err := p.Excerpt(20)
	require.NoError(t, err)
	assert.True(t, truncated)
	assert.Len(t, tail, 20)
	assert.True(t, strings.HasSuffix(full, tail))
}

// TestOutputKeepsStandardOutputApart runs the probe through Output while it
// prints noise, including a JSON object, on standard error: the returned
// standard output holds exactly the probe's result lines, and the log holds
// only the noise. A limit below the output fails without returning a part.
func TestOutputKeepsStandardOutputApart(t *testing.T) {
	probe := buildProbe(t)
	const noise = `{"id":"noise","scenarios":[]}`
	args := []string{"stderr:" + noise, "env:GIVEN", "stderr:after"}
	cases := []struct {
		name     string
		maxBytes int
		// wantErr is the expected error; nil means success.
		wantErr error
	}{
		{name: "within the limit", maxBytes: 1 << 20},
		{name: "a limit below the output", maxBytes: 10, wantErr: ErrOutputTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logFile := filepath.Join(t.TempDir(), "probe.log")
			out, err := Output(t.Context(), probe, Spec{
				Args:     args,
				Env:      []string{"GIVEN=explicit"},
				StateDir: t.TempDir(),
				LogFile:  logFile,
			}, tc.maxBytes, (*exec.Cmd).Run)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, out)
				return
			}
			require.NoError(t, err)
			var got []probeResult
			dec := json.NewDecoder(strings.NewReader(string(out)))
			for dec.More() {
				var r probeResult
				require.NoError(t, dec.Decode(&r), "standard output holds only the probe's results:\n%s", out)
				got = append(got, r)
			}
			assert.Equal(t, []probeResult{
				{Action: "stderr:" + noise, Allowed: true},
				{Action: "env:GIVEN", Allowed: true, Detail: "explicit"},
				{Action: "stderr:after", Allowed: true},
			}, got)
			logged, err := os.ReadFile(logFile)
			require.NoError(t, err)
			assert.Equal(t, noise+"\nafter\n", string(logged), "the log holds standard error only")
		})
	}
}

// TestOutputReturnsWhatWasPrintedBeforeAFailedExit runs the probe through
// Output until it exits with status 3: the exit error comes back with the
// result the probe printed first.
func TestOutputReturnsWhatWasPrintedBeforeAFailedExit(t *testing.T) {
	out, err := Output(t.Context(), buildProbe(t), Spec{
		Args:     []string{"env:GIVEN", "exit:3"},
		Env:      []string{"GIVEN=explicit"},
		StateDir: t.TempDir(),
		LogFile:  filepath.Join(t.TempDir(), "probe.log"),
	}, 1<<20, (*exec.Cmd).Run)
	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 3, exitErr.ExitCode())
	assert.Equal(t, map[string]probeResult{
		"env:GIVEN": {Action: "env:GIVEN", Allowed: true, Detail: "explicit"},
	}, probeResults(t, string(out)))
}

// TestRunWritesBothStreamsToTheLog runs the probe through Run: its standard
// output and its standard error both land in the log, and a clean exit
// returns nil.
func TestRunWritesBothStreamsToTheLog(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "probe.log")
	require.NoError(t, Run(t.Context(), buildProbe(t), Spec{
		Args:     []string{"env:GIVEN", "stderr:noise"},
		Env:      []string{"GIVEN=explicit"},
		StateDir: t.TempDir(),
		LogFile:  logFile,
	}, (*exec.Cmd).Run))
	logged, err := os.ReadFile(logFile)
	require.NoError(t, err)
	assert.Contains(t, string(logged), `{"action":"env:GIVEN","allowed":true,"detail":"explicit"}`)
	assert.Contains(t, string(logged), "noise\n")
}

// TestRunAndOutputAdmitBeforeTheRunner changes a pinned binary: Run and Output
// refuse it with ErrDigestMismatch and never call the caller's runner.
func TestRunAndOutputAdmitBeforeTheRunner(t *testing.T) {
	probe := buildProbe(t)
	require.NoError(t, os.Chmod(filepath.Dir(probe.path), 0o700))
	require.NoError(t, os.Chmod(probe.path, 0o700))
	require.NoError(t, os.WriteFile(probe.path, []byte("tampered"), 0o700))
	never := func(*exec.Cmd) error {
		t.Fatal("the runner must not run a refused artifact")
		return nil
	}
	spec := Spec{StateDir: t.TempDir(), LogFile: filepath.Join(t.TempDir(), "x.log")}
	require.ErrorIs(t, Run(t.Context(), probe, spec, never), ErrDigestMismatch)
	_, err := Output(t.Context(), probe, spec, 1<<20, never)
	require.ErrorIs(t, err, ErrDigestMismatch)
}

// TestExecLeavesChildrenInTheCallersGroup starts the probe through Exec with a
// child that outlives it: the child runs in this test's own process group,
// which Exec leaves to its caller, and is still running after the probe exits.
func TestExecLeavesChildrenInTheCallersGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	p, err := Exec(t.Context(), buildProbe(t), Spec{
		Args:     []string{"orphan:" + pidFile},
		StateDir: t.TempDir(),
		LogFile:  filepath.Join(t.TempDir(), "probe.log"),
	})
	require.NoError(t, err)
	require.NoError(t, p.Wait())
	child := readPID(t, pidFile)
	t.Cleanup(func() {
		assert.NoError(t, syscall.Kill(child, syscall.SIGKILL))
		assert.Eventually(t, func() bool { return gone(child) }, 10*time.Second, 50*time.Millisecond)
	})
	pgid, err := syscall.Getpgid(child)
	require.NoError(t, err, "the child is still running")
	assert.Equal(t, syscall.Getpgrp(), pgid)
}

// TestRunAndOutputStopTheGroupTheirRunnerOwns runs the probe through Run and
// Output with a runner that gives it its own process group, as Surla's
// in-server callers do. The probe starts a child that outlives it and, under
// Output, holds its standard output open: both calls return within the
// runner's grace, and the child no longer exists afterwards.
func TestRunAndOutputStopTheGroupTheirRunnerOwns(t *testing.T) {
	probe := buildProbe(t)
	ownGroup := func(cmd *exec.Cmd) error {
		runErr, stopErr := procgroup.Run(cmd, syscall.SIGKILL, time.Second, 5*time.Second)
		return errors.Join(runErr, stopErr)
	}
	for _, viaOutput := range []bool{false, true} {
		name := "Run"
		if viaOutput {
			name = "Output"
		}
		t.Run(name, func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			spec := Spec{
				Args:     []string{"orphan:" + pidFile},
				StateDir: t.TempDir(),
				LogFile:  filepath.Join(t.TempDir(), "probe.log"),
			}
			start := time.Now()
			if viaOutput {
				_, err := Output(t.Context(), probe, spec, 1<<20, ownGroup)
				// The child held standard output past the probe's exit, so the
				// runner's grace closed it.
				assert.ErrorIs(t, err, exec.ErrWaitDelay)
			} else {
				require.NoError(t, Run(t.Context(), probe, spec, ownGroup))
			}
			assert.Less(t, time.Since(start), 10*time.Second)
			assert.True(t, gone(readPID(t, pidFile)), "the runner stopped the child with its group")
		})
	}
}

// probeResults decodes the probe's result lines in out, keyed by action.
func probeResults(t *testing.T, out string) map[string]probeResult {
	t.Helper()
	got := map[string]probeResult{}
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		var r probeResult
		require.NoError(t, json.Unmarshal(sc.Bytes(), &r), sc.Text())
		got[r.Action] = r
	}
	require.NoError(t, sc.Err())
	return got
}

// readPID reads the process ID the probe's orphan action wrote to path.
func readPID(t *testing.T, path string) int {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	return pid
}

// gone reports whether no process with ID pid exists any more.
func gone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}

// excerptAfterExit waits for the process and returns its whole output.
func excerptAfterExit(p *Process) (string, bool, error) {
	if err := p.Wait(); err != nil {
		return "", false, err
	}
	return p.Excerpt(1 << 20)
}

// buildProbe builds the probe and pins it.
func buildProbe(t *testing.T) Artifact {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "probe")
	build := exec.Command("go", "build", "-o", bin, "./testdata/probe")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	store := filepath.Join(t.TempDir(), "artifacts")
	t.Cleanup(func() { require.NoError(t, RemoveStore(store)) })
	a, err := Pin(store, bin)
	require.NoError(t, err)
	return a
}

// goEnv returns one go env value.
func goEnv(t *testing.T, name string) string {
	t.Helper()
	out, err := exec.Command("go", "env", name).Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// TestBuildFollowsASiblingReplace builds a module that replaces a sibling
// module in the same tree, which also holds unused links pointing outside it
// or nowhere: Build and BuildTest both succeed.
func TestBuildFollowsASiblingReplace(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	root := writeModule(t, map[string]string{
		"lib/go.mod":       "module example.com/lib\n\ngo 1.26\n",
		"lib/lib.go":       "package lib\n\nfunc V() int { return 7 }\n",
		"app/go.mod":       "module example.com/app\n\ngo 1.26\n\nrequire example.com/lib v0.0.0\n\nreplace example.com/lib => ../lib\n",
		"app/main.go":      "package main\n\nimport \"example.com/lib\"\n\nfunc main() { println(lib.V()) }\n",
		"app/main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) {}\n",
	})
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docs"), 0o700))
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "docs", "outside")))
	require.NoError(t, os.Symlink("missing.md", filepath.Join(root, "docs", "dangling.md")))
	app := filepath.Join(root, "app")
	buildLog := filepath.Join(t.TempDir(), "build.log")
	err := Build(t.Context(), testBuildSpec(t, root, app, filepath.Join(t.TempDir(), "app"), buildLog), (*exec.Cmd).Run)
	require.NoError(t, err, logText(buildLog))
	hasTests, err := BuildTest(t.Context(), testBuildSpec(t, root, app, filepath.Join(t.TempDir(), "app.test"), buildLog), (*exec.Cmd).Run)
	require.NoError(t, err, logText(buildLog))
	assert.True(t, hasTests)

	t.Run("module directory outside the source tree is rejected", func(t *testing.T) {
		err := Build(t.Context(), testBuildSpec(t, filepath.Join(root, "lib"), app, filepath.Join(t.TempDir(), "app"), buildLog), (*exec.Cmd).Run)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is outside source directory")
		_, err = GoWork(filepath.Join(root, "lib"), app)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is outside source directory")
	})
}

// TestBuildAppliesAGoWorkOnlyInsideItsSourceRoot builds an app whose import
// resolves only through the go.work of the workspace that holds it and the
// imported module. With the workspace as the source root, GoWork names that
// go.work and Build and BuildTest apply it and succeed. With the app as the
// source root, the go.work lies above it: GoWork is "off", and both builds
// fail on the import that only the workspace provides.
func TestBuildAppliesAGoWorkOnlyInsideItsSourceRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	ws := writeModule(t, map[string]string{
		"go.work":          "go 1.26\n\nuse (\n\t./app\n\t./dep\n)\n",
		"dep/go.mod":       "module example.com/dep\n\ngo 1.26\n",
		"dep/dep.go":       "package dep\n\nfunc V() int { return 7 }\n",
		"app/go.mod":       "module example.com/app\n\ngo 1.26\n",
		"app/main.go":      "package main\n\nimport \"example.com/dep\"\n\nfunc main() { println(dep.V()) }\n",
		"app/main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) {}\n",
	})
	app := filepath.Join(ws, "app")
	physicalWS, err := filepath.EvalSymlinks(ws)
	require.NoError(t, err)

	t.Run("a go.work inside the source root applies", func(t *testing.T) {
		goWork, err := GoWork(ws, app)
		require.NoError(t, err)
		assert.Equal(t, filepath.Join(physicalWS, "go.work"), goWork)
		buildLog := filepath.Join(t.TempDir(), "build.log")
		err = Build(t.Context(), testBuildSpec(t, ws, app, filepath.Join(t.TempDir(), "app"), buildLog), (*exec.Cmd).Run)
		require.NoError(t, err, logText(buildLog))
		hasTests, err := BuildTest(t.Context(), testBuildSpec(t, ws, app, filepath.Join(t.TempDir(), "app.test"), buildLog), (*exec.Cmd).Run)
		require.NoError(t, err, logText(buildLog))
		assert.True(t, hasTests)
	})
	t.Run("a go.work above the source root is ignored", func(t *testing.T) {
		goWork, err := GoWork(app, app)
		require.NoError(t, err)
		assert.Equal(t, "off", goWork)
		buildLog := filepath.Join(t.TempDir(), "build.log")
		err = Build(t.Context(), testBuildSpec(t, app, app, filepath.Join(t.TempDir(), "app"), buildLog), (*exec.Cmd).Run)
		require.Error(t, err)
		assert.Contains(t, logText(buildLog), "example.com/dep")
		_, err = BuildTest(t.Context(), testBuildSpec(t, app, app, filepath.Join(t.TempDir(), "app.test"), buildLog), (*exec.Cmd).Run)
		require.Error(t, err)
		assert.Contains(t, logText(buildLog), "example.com/dep")
	})
}

// TestBuildDoesNotStampVersionControl builds a main package committed in a Git
// repository: a plain go build stamps the commit into the binary, while Build,
// which passes -buildvcs=false, stamps nothing and runs no git.
func TestBuildDoesNotStampVersionControl(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	mod := writeModule(t, map[string]string{"go.mod": libGoMod, "main.go": "package main\n\nfunc main() {}\n"})
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "c"},
	} {
		cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null"}, args...)...)
		cmd.Dir = mod
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	control := filepath.Join(t.TempDir(), "control")
	build := exec.Command("go", "build", "-o", control, ".")
	build.Dir = mod
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	require.True(t, stampsVCS(t, control), "control: a plain go build stamps the commit")

	bin := filepath.Join(t.TempDir(), "app")
	buildLog := filepath.Join(t.TempDir(), "build.log")
	err = Build(t.Context(), testBuildSpec(t, mod, mod, bin, buildLog), (*exec.Cmd).Run)
	require.NoError(t, err, logText(buildLog))
	assert.False(t, stampsVCS(t, bin))
}

// stampsVCS reports whether the Go binary at path carries version-control
// settings (vcs, vcs.revision and the like).
func stampsVCS(t *testing.T, path string) bool {
	t.Helper()
	info, err := buildinfo.ReadFile(path)
	require.NoError(t, err)
	for _, setting := range info.Settings {
		if strings.HasPrefix(setting.Key, "vcs") {
			return true
		}
	}
	return false
}

// TestBuildTestReportsWhetherThePackageHasTests compiles one package's tests
// and checks the three outcomes a caller acts on: a test
// binary, no tests, and a compile error.
func TestBuildTestReportsWhetherThePackageHasTests(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	cases := []struct {
		name      string
		pkg       string
		files     map[string]string
		wantTests bool
		wantErr   string
	}{
		{
			name: "package with tests yields a test binary",
			files: map[string]string{
				"go.mod":      libGoMod,
				"lib.go":      "package lib\n\nfunc V() int { return 7 }\n",
				"lib_test.go": "package lib\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) {\n\tif V() != 7 {\n\t\tt.Fatal(V())\n\t}\n}\n",
			},
			wantTests: true,
		},
		{
			name: "package directory ending in .go yields a test binary",
			pkg:  "./dir.go",
			files: map[string]string{
				"go.mod":             libGoMod,
				"dir.go/lib.go":      "package lib\n\nfunc V() int { return 7 }\n",
				"dir.go/lib_test.go": "package lib\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) {\n\tif V() != 7 {\n\t\tt.Fatal(V())\n\t}\n}\n",
			},
			wantTests: true,
		},
		{
			name:  "package without tests is reported, not an error",
			files: map[string]string{"go.mod": libGoMod, "lib.go": "package lib\n\nfunc V() int { return 7 }\n"},
		},
		{
			name: "compile error in a test file fails the build",
			files: map[string]string{
				"go.mod":      libGoMod,
				"lib.go":      "package lib\n\nfunc V() int { return 7 }\n",
				"lib_test.go": "package lib\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) { undefinedInTest() }\n",
			},
			wantErr: "test build of .",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mod := writeModule(t, c.files)
			out := filepath.Join(t.TempDir(), "lib.test")
			log := filepath.Join(t.TempDir(), "build.log")
			spec := testBuildSpec(t, mod, mod, out, log)
			if c.pkg != "" {
				spec.Package = c.pkg
			}
			hasTests, err := BuildTest(t.Context(), spec, (*exec.Cmd).Run)
			logText, _ := os.ReadFile(log)
			if c.wantErr != "" {
				require.ErrorContains(t, err, c.wantErr)
				assert.Contains(t, string(logText), "undefinedInTest", "the compiler output is in the log")
				return
			}
			require.NoError(t, err, string(logText))
			assert.Equal(t, c.wantTests, hasTests)
			info, statErr := os.Lstat(out)
			if !c.wantTests {
				assert.ErrorIs(t, statErr, os.ErrNotExist, "no test binary is written")
				return
			}
			require.NoError(t, statErr)
			assert.True(t, info.Mode().IsRegular())
		})
	}

	t.Run("compiler metadata written at the output path is not mistaken for a test binary or overwritten", func(t *testing.T) {
		noTestsMod := writeModule(t, map[string]string{
			"go.mod": libGoMod,
			"lib.go": "package lib\n\nfunc V() int { return 7 }\n",
		})
		withTestsMod := writeModule(t, map[string]string{
			"go.mod":      libGoMod,
			"lib.go":      "package lib\n\nfunc V() int { return 7 }\n",
			"lib_test.go": "package lib\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) {}\n",
		})

		t.Run("GOCACHE README at output path in package without tests", func(t *testing.T) {
			workDir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(workDir, "gocache"), 0o700))
			out := filepath.Join(workDir, "gocache", "README")
			spec := testBuildSpec(t, noTestsMod, noTestsMod, out, filepath.Join(t.TempDir(), "build.log"))
			spec.WorkDir = workDir
			spec.BuildCacheDir = filepath.Join(workDir, "gocache")
			hasTests, err := BuildTest(t.Context(), spec, (*exec.Cmd).Run)
			require.NoError(t, err)
			assert.False(t, hasTests)
		})

		t.Run("symlinked GOCACHE README at output path in package without tests", func(t *testing.T) {
			workDir := t.TempDir()
			outDir := filepath.Join(workDir, "out")
			require.NoError(t, os.MkdirAll(outDir, 0o700))
			require.NoError(t, os.Symlink(outDir, filepath.Join(workDir, "gocache")))
			out := filepath.Join(outDir, "README")
			spec := testBuildSpec(t, noTestsMod, noTestsMod, out, filepath.Join(t.TempDir(), "build.log"))
			spec.WorkDir = workDir
			spec.BuildCacheDir = filepath.Join(workDir, "gocache")
			hasTests, err := BuildTest(t.Context(), spec, (*exec.Cmd).Run)
			require.NoError(t, err)
			assert.False(t, hasTests)
		})

		t.Run("relative output path in GOCACHE directory in package without tests", func(t *testing.T) {
			workDir := t.TempDir()
			gocacheDir := filepath.Join(workDir, "gocache")
			require.NoError(t, os.MkdirAll(gocacheDir, 0o700))
			t.Chdir(gocacheDir)
			spec := testBuildSpec(t, noTestsMod, noTestsMod, "README", filepath.Join(t.TempDir(), "build.log"))
			spec.WorkDir = workDir
			spec.BuildCacheDir = filepath.Join(workDir, "gocache")
			hasTests, err := BuildTest(t.Context(), spec, (*exec.Cmd).Run)
			require.NoError(t, err)
			assert.False(t, hasTests)
		})

		t.Run("workspace go.work.sum at output path in package without tests", func(t *testing.T) {
			ws := writeModule(t, map[string]string{
				"go.work":    "go 1.26\n\nuse (\n\t./app\n\t./dep\n)\n",
				"app/go.mod": "module example.com/app\n\ngo 1.26\n",
				"app/app.go": "package app\n\nfunc V() int { return 1 }\n",
				"dep/go.mod": "module example.com/dep\n\ngo 1.26\n",
				"dep/go.sum": "example.com/other v1.0.0 h1:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU=\n",
				"dep/dep.go": "package dep\n",
			})
			out := filepath.Join(ws, "go.work.sum")
			spec := testBuildSpec(t, ws, filepath.Join(ws, "app"), out, filepath.Join(t.TempDir(), "build.log"))
			hasTests, err := BuildTest(t.Context(), spec, (*exec.Cmd).Run)
			require.NoError(t, err)
			assert.False(t, hasTests)
		})

		t.Run("compiler metadata created at output path in package with tests fails without overwriting", func(t *testing.T) {
			workDir := t.TempDir()
			outDir := filepath.Join(workDir, "out")
			require.NoError(t, os.MkdirAll(outDir, 0o700))
			require.NoError(t, os.Symlink(outDir, filepath.Join(workDir, "gocache")))
			out := filepath.Join(outDir, "README")
			spec := testBuildSpec(t, withTestsMod, withTestsMod, out, filepath.Join(t.TempDir(), "build.log"))
			spec.WorkDir = workDir
			spec.BuildCacheDir = filepath.Join(workDir, "gocache")
			hasTests, err := BuildTest(t.Context(), spec, (*exec.Cmd).Run)
			require.ErrorContains(t, err, "publish test binary")
			require.ErrorIs(t, err, os.ErrExist)
			assert.False(t, hasTests)
			readme, readErr := os.ReadFile(out)
			require.NoError(t, readErr)
			assert.Contains(t, string(readme), "cached build artifacts", "GOCACHE README must not be overwritten with the compiled test binary")
		})
	})
}

// TestBuildTestRejectsBeforeRunningTheToolchain checks the preconditions that
// let a missing output mean "no tests": a pattern, a flag or import path in
// place of the package directory, and an existing output path are refused
// before any command runs.
func TestBuildTestRejectsBeforeRunningTheToolchain(t *testing.T) {
	mod := writeModule(t, map[string]string{
		"go.mod":      libGoMod,
		"lib.go":      "package lib\n",
		"lib_test.go": "package lib\n\nimport \"testing\"\n\nfunc TestV(t *testing.T) {}\n",
	})
	outDir := t.TempDir()
	existing := filepath.Join(outDir, "existing.test")
	require.NoError(t, os.WriteFile(existing, []byte("stale"), 0o600))
	link := filepath.Join(outDir, "link.test")
	require.NoError(t, os.Symlink(filepath.Join(outDir, "missing"), link))
	symOutDir := filepath.Join(t.TempDir(), "sym-out")
	require.NoError(t, os.Symlink(outDir, symOutDir))
	aliasLinkTarget := filepath.Join(outDir, "alias-link.test")
	logLinkToOut := filepath.Join(t.TempDir(), "build.log")
	require.NoError(t, os.Symlink(aliasLinkTarget, logLinkToOut))
	type rejectCase struct {
		name    string
		pkg     string
		out     string
		log     string
		wantErr string
	}
	cases := []rejectCase{
		{name: "package pattern", pkg: "./...", out: filepath.Join(outDir, "new.test"), wantErr: "is not one package path"},
		{name: "parent traversal package", pkg: "./../outside", out: filepath.Join(outDir, "new.test"), wantErr: "is not one package path"},
		{name: "output flag as package", pkg: "-o=" + filepath.Join(outDir, "other.test"), out: filepath.Join(outDir, "new.test"), wantErr: "is not one package path"},
		{name: "compile flag as package", pkg: "-c=false", out: filepath.Join(outDir, "new.test"), wantErr: "is not one package path"},
		{name: "import path as package", pkg: "fmt", out: filepath.Join(outDir, "new.test"), wantErr: "is not one package path"},
		{name: "source file as package", pkg: "./lib.go", out: filepath.Join(outDir, "new.test"), wantErr: "is not a directory"},
		{name: "missing package directory", pkg: "./missing", out: filepath.Join(outDir, "new.test"), wantErr: "inspect test package directory"},
		{name: "existing output file", pkg: ".", out: existing, wantErr: "must not exist"},
		{name: "existing output symlink", pkg: ".", out: link, wantErr: "must not exist"},
		{name: "log file equals output path", pkg: ".", out: filepath.Join(outDir, "same.test"), log: filepath.Join(outDir, "same.test"), wantErr: "must not be the output binary"},
		{name: "log file aliases output path through symlinked directory", pkg: ".", out: filepath.Join(outDir, "alias-dir.test"), log: filepath.Join(symOutDir, "alias-dir.test"), wantErr: "must not be the output binary"},
		{name: "log file symlink points to output path", pkg: ".", out: aliasLinkTarget, log: logLinkToOut, wantErr: "must not be the output binary"},
	}
	if _, err := os.Stat(filepath.Join(outDir, "EXISTING.TEST")); err == nil {
		cases = append(cases, rejectCase{
			name:    "log file aliases output path by case on case-insensitive filesystem",
			pkg:     ".",
			out:     filepath.Join(outDir, "case-alias.test"),
			log:     filepath.Join(outDir, "CASE-ALIAS.TEST"),
			wantErr: "must not be the output binary",
		})
	} else {
		t.Run("distinct case paths are allowed on case-sensitive filesystem", func(t *testing.T) {
			spec := testBuildSpec(t, mod, mod, filepath.Join(outDir, "case-distinct.test"), filepath.Join(outDir, "CASE-DISTINCT.TEST"))
			var ran bool
			_, err := BuildTest(t.Context(), spec, func(*exec.Cmd) error {
				ran = true
				return nil
			})
			require.NoError(t, err)
			assert.True(t, ran)
		})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logPath := c.log
			if logPath == "" {
				logPath = filepath.Join(t.TempDir(), "build.log")
			}
			spec := testBuildSpec(t, mod, mod, c.out, logPath)
			spec.Package = c.pkg
			_, err := BuildTest(t.Context(), spec, func(*exec.Cmd) error {
				t.Fatal("the toolchain must not run")
				return nil
			})
			require.ErrorContains(t, err, c.wantErr)
			if c.out != existing && c.out != link {
				_, statErr := os.Lstat(c.out)
				assert.ErrorIs(t, statErr, os.ErrNotExist, "rejected preflight must not create output file")
			}
		})
	}
	t.Run("relative existing output path in working directory is rejected", func(t *testing.T) {
		t.Chdir(outDir)
		spec := testBuildSpec(t, mod, mod, "existing.test", filepath.Join(t.TempDir(), "build.log"))
		_, err := BuildTest(t.Context(), spec, func(*exec.Cmd) error {
			t.Fatal("the toolchain must not run")
			return nil
		})
		require.ErrorContains(t, err, "must not exist")
	})
	t.Run("relative log file aliasing relative output path in working directory is rejected", func(t *testing.T) {
		t.Chdir(outDir)
		spec := testBuildSpec(t, mod, mod, "rel-same.test", "rel-same.test")
		_, err := BuildTest(t.Context(), spec, func(*exec.Cmd) error {
			t.Fatal("the toolchain must not run")
			return nil
		})
		require.ErrorContains(t, err, "must not be the output binary")
		_, statErr := os.Lstat(filepath.Join(outDir, "rel-same.test"))
		assert.ErrorIs(t, statErr, os.ErrNotExist)
	})
	t.Run("relative output path resolves from working directory and publishes staged output", func(t *testing.T) {
		relOutDir := t.TempDir()
		t.Chdir(relOutDir)
		spec := testBuildSpec(t, mod, mod, "relative.test", filepath.Join(t.TempDir(), "build.log"))
		var stageArg string
		hasTests, err := BuildTest(t.Context(), spec, func(cmd *exec.Cmd) error {
			for i, arg := range cmd.Args {
				if arg == "-o" && i+1 < len(cmd.Args) {
					stageArg = cmd.Args[i+1]
				}
			}
			require.NotEmpty(t, stageArg)
			return os.WriteFile(stageArg, []byte("bin"), 0o700)
		})
		require.NoError(t, err)
		assert.True(t, hasTests)
		resolvedRelOutDir, err := filepath.EvalSymlinks(relOutDir)
		require.NoError(t, err)
		assert.NotEqual(t, filepath.Join(resolvedRelOutDir, "relative.test"), stageArg, "toolchain writes to staging path, not directly to Out")
		got, err := os.ReadFile(filepath.Join(resolvedRelOutDir, "relative.test"))
		require.NoError(t, err)
		assert.Equal(t, "bin", string(got))
	})
}

// TestBuildRunsWithoutHomeOrTelemetry checks that Build and BuildTest omit
// HOME and GOTELEMETRY from the compiler environment while keeping TMPDIR and
// GOCACHE in WorkDir, so Go telemetry stays disabled without starting a
// detached child process or creating a user config directory in WorkDir.
func TestBuildRunsWithoutHomeOrTelemetry(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	mod := writeModule(t, map[string]string{
		"go.mod": libGoMod, "main.go": "package main\n\nfunc main() {}\n",
		"main_test.go": "package main\n\nimport \"testing\"\n\nfunc TestMainBuild(t *testing.T) {}\n",
	})
	runPair := func(workDir string, failFirst bool) [2]error {
		var readyWg, doneWg sync.WaitGroup
		readyWg.Add(2)
		var errs [2]error
		for i, isTestBuild := range []bool{false, true} {
			doneWg.Go(func() {
				var entered bool
				defer func() {
					if !entered {
						readyWg.Done()
					}
				}()
				dir := t.TempDir()
				spec := testBuildSpec(t, mod, mod, filepath.Join(dir, "out"), filepath.Join(dir, "build.log"))
				spec.WorkDir = workDir
				spec.BuildCacheDir = filepath.Join(workDir, "gocache")
				run := func(cmd *exec.Cmd) error {
					entered = true
					readyWg.Done()
					readyWg.Wait()
					if failFirst && i == 0 {
						return errors.New("injected runner error")
					}
					for _, entry := range cmd.Env {
						if strings.HasPrefix(entry, "HOME=") || strings.HasPrefix(entry, "GOTELEMETRY=") {
							return fmt.Errorf("unexpected compiler env entry %q", entry)
						}
					}
					envCmd := exec.Command(filepath.Join(spec.GOROOT, "bin", "go"), "env", "GOTELEMETRY")
					envCmd.Env = cmd.Env
					if out, err := envCmd.CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "off" {
						return fmt.Errorf("go env GOTELEMETRY = %q (%v), want off", out, err)
					}
					return cmd.Run()
				}
				var err error
				if isTestBuild {
					var hasTests bool
					hasTests, err = BuildTest(t.Context(), spec, run)
					if err == nil && !hasTests {
						err = errors.New("expected test binary")
					}
				} else {
					err = Build(t.Context(), spec, run)
				}
				if err == nil {
					_, err = os.Stat(spec.Out)
				}
				errs[i] = err
			})
		}
		doneWg.Wait()
		return errs
	}
	shared := t.TempDir()
	failErrs := runPair(shared, true)
	require.ErrorContains(t, failErrs[0], "injected runner error")
	require.NoError(t, failErrs[1])
	for _, err := range runPair(shared, false) {
		require.NoError(t, err)
	}
	assert.NoDirExists(t, filepath.Join(shared, "Library"))
	assert.DirExists(t, filepath.Join(shared, "gocache"))
	require.NoError(t, os.RemoveAll(shared))

	failSpec := testBuildSpec(t, mod, mod, filepath.Join(t.TempDir(), "out"), filepath.Join(t.TempDir(), "build.log"))
	failSpec.BuildCacheDir = filepath.Join(failSpec.WorkDir, "gocache")
	require.NoError(t, os.WriteFile(filepath.Join(failSpec.WorkDir, "gocache"), []byte("not a directory"), 0o600))
	require.Error(t, Build(t.Context(), failSpec, (*exec.Cmd).Run))
	logText, err := os.ReadFile(failSpec.LogFile)
	require.NoError(t, err)
	assert.Contains(t, string(logText), "create build cache directory")
	assert.NoDirExists(t, filepath.Join(failSpec.WorkDir, "Library"))
	require.NoError(t, os.RemoveAll(failSpec.WorkDir))

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	canceled := testBuildSpec(t, mod, mod, filepath.Join(t.TempDir(), "out"), filepath.Join(t.TempDir(), "build.log"))
	canceled.BuildCacheDir = filepath.Join(canceled.WorkDir, "gocache")
	assert.ErrorIs(t, Build(ctx, canceled, (*exec.Cmd).Run), context.Canceled)
}

// TestBuildTestBinaryRunsUnderExec builds a test binary and runs it through
// Pin and Exec: a test using t.TempDir passes inside its state directory, and
// a test that starts a child process passes too, because the test binary is an
// ordinary process.
func TestBuildTestBinaryRunsUnderExec(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	mod := writeModule(t, map[string]string{
		"go.mod": libGoMod,
		"lib.go": "package lib\n",
		"lib_test.go": `package lib

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestTempDir(t *testing.T) {
	if err := os.WriteFile(filepath.Join(t.TempDir(), "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSpawn(t *testing.T) {
	if err := exec.Command("/bin/echo", "spawned").Run(); err != nil {
		t.Fatalf("spawn: %v", err)
	}
}
`,
	})
	out := filepath.Join(t.TempDir(), "lib.test")
	buildLog := filepath.Join(t.TempDir(), "build.log")
	hasTests, err := BuildTest(t.Context(), testBuildSpec(t, mod, mod, out, buildLog), (*exec.Cmd).Run)
	logText, _ := os.ReadFile(buildLog)
	require.NoError(t, err, string(logText))
	require.True(t, hasTests)

	store := filepath.Join(t.TempDir(), "artifacts")
	t.Cleanup(func() { require.NoError(t, RemoveStore(store)) })
	art, err := Pin(store, out)
	require.NoError(t, err)
	p, err := Exec(t.Context(), art, Spec{
		Args:     []string{"-test.v", "-test.timeout=60s"},
		StateDir: t.TempDir(),
		LogFile:  filepath.Join(t.TempDir(), "test.log"),
	})
	require.NoError(t, err)
	require.NoError(t, p.Wait())
	got, truncated, err := p.Excerpt(1 << 20)
	require.NoError(t, err)
	require.False(t, truncated)
	assert.Contains(t, got, "--- PASS: TestTempDir")
	assert.Contains(t, got, "--- PASS: TestSpawn")
}

// writeModule writes files, keyed by slash-separated relative path, under a new
// temporary directory and returns that directory.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	return root
}

// testBuildSpec returns a BuildSpec for package "." of moduleDir in the source
// root sourceDir with trusted shared compiler inputs and a fresh work directory.
// Tests of cache contents or lifetime explicitly select their own cache.
func testBuildSpec(t *testing.T, sourceDir, moduleDir, out, log string) BuildSpec {
	t.Helper()
	work := t.TempDir()
	return BuildSpec{BuildCacheDir: testmodule.CompilerCache(t),
		SourceDir: sourceDir, ModuleDir: moduleDir, Package: ".", Out: out,
		GOROOT: goEnv(t, "GOROOT"), ModCache: goEnv(t, "GOMODCACHE"), WorkDir: work, LogFile: log,
	}
}

// logText returns the build log at path for a failure message, or why it could
// not be read.
func logText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "the build log could not be read: " + err.Error()
	}
	return string(b)
}
