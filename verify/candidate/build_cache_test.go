// These tests exercise the build cache contract without treating cache contents
// as proof of source integrity or running any generated candidate program.
package candidate

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/goadesign/surla/internal/procgroup"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildRequiresExplicitCache covers all public compilation entries before
// any compiler command can run; no ambient cache can satisfy missing input.
func TestBuildRequiresExplicitCache(t *testing.T) {
	for _, entry := range []string{"build", "module", "test"} {
		t.Run(entry, func(t *testing.T) {
			spec := BuildSpec{Package: "."}
			ran := false
			runner := func(*exec.Cmd) error { ran = true; return nil }
			var err error
			switch entry {
			case "build":
				err = Build(t.Context(), spec, runner)
			case "module":
				err = BuildModule(t.Context(), spec, runner)
			case "test":
				_, err = BuildTest(t.Context(), spec, runner)
			}
			assert.ErrorContains(t, err, "build cache directory is required")
			assert.False(t, ran)
		})
	}
}

// TestBuildSeparatesRetainedCacheFromScratch observes the command consumed by
// the compiler runner; it checks path ownership without pretending to compile.
func TestBuildSeparatesRetainedCacheFromScratch(t *testing.T) {
	source := writeModule(t, map[string]string{"go.mod": "module example.invalid/app\ngo 1.27.3\n", "main.go": "package main\nfunc main() {}\n"})
	work, cache, out := t.TempDir(), filepath.Join(t.TempDir(), "cache"), t.TempDir()
	spec := testBuildSpec(t, source, source, filepath.Join(out, "app"), filepath.Join(out, "build.log"))
	spec.WorkDir, spec.BuildCacheDir = work, cache
	err := Build(t.Context(), spec, func(cmd *exec.Cmd) error {
		resolvedWork, err := filepath.EvalSymlinks(work)
		require.NoError(t, err)
		resolvedCacheParent, err := filepath.EvalSymlinks(filepath.Dir(cache))
		require.NoError(t, err)
		assert.Contains(t, cmd.Env, "TMPDIR="+resolvedWork)
		assert.Contains(t, cmd.Env, "GOCACHE="+filepath.Join(resolvedCacheParent, filepath.Base(cache)))
		stage := cmd.Args[len(cmd.Args)-2]
		assert.Equal(t, resolvedWork, filepath.Dir(filepath.Dir(stage)))
		return os.WriteFile(stage, []byte("trusted runner placeholder, never executed"), 0o700)
	})
	require.NoError(t, err)
	entries, err := os.ReadDir(work)
	require.NoError(t, err)
	assert.Empty(t, entries, "successful staging is cleaned from operation scratch")
	assert.DirExists(t, cache, "the build owner creates the cache before invoking the compiler")
	info, err := os.Stat(cache)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
	entries, err = os.ReadDir(cache)
	require.NoError(t, err)
	assert.Empty(t, entries, "the trusted runner stand-in creates no Go cache entries")
}

// TestBuildCanceledCompilerLeavesSharedCacheUsable starts two real compilers
// with separate scratch and one cold cache, cancels one after both processes
// start, then compares the surviving output with an isolated cold build.
func TestBuildCanceledCompilerLeavesSharedCacheUsable(t *testing.T) {
	if testing.Short() {
		t.Skip("native concurrent compiler coverage runs in the focused integration gate")
	}
	source := writeModule(t, map[string]string{"go.mod": "module example.invalid/cacheapp\ngo 1.27.3\n", "main.go": "package main\nimport \"fmt\"\nfunc main(){fmt.Println(\"cache fixture\")}\n"})
	cache := filepath.Join(t.TempDir(), "cache")
	makeSpec := func() BuildSpec {
		out := t.TempDir()
		spec := testBuildSpec(t, source, source, filepath.Join(out, "app"), filepath.Join(out, "build.log"))
		spec.BuildCacheDir = cache
		return spec
	}
	canceled, survivor := makeSpec(), makeSpec()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started, stop := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	cleanup := make(chan error, 1)
	canceledGroup := make(chan int, 1)
	go func() {
		done <- Build(ctx, canceled, func(cmd *exec.Cmd) error {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				return err
			}
			canceledGroup <- cmd.Process.Pid
			close(started)
			<-stop
			cancel()
			waitErr := cmd.Wait()
			cleanup <- procgroup.Stop(cmd.Process.Pid, 10*time.Second)
			return waitErr
		})
	}()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("compiler failed before starting: %v", err)
	case <-t.Context().Done():
		t.Fatal("compiler did not start")
	}
	err := Build(t.Context(), survivor, func(cmd *exec.Cmd) error {
		if err := cmd.Start(); err != nil {
			close(stop)
			return err
		}
		close(stop)
		return cmd.Wait()
	})
	canceledErr := <-done
	require.NoError(t, err, logText(survivor.LogFile))
	require.Error(t, canceledErr)
	require.NoError(t, <-cleanup, "cleanup errors cannot be mistaken for expected cancellation")
	pgid := <-canceledGroup
	assert.ErrorIs(t, syscall.Kill(-pgid, 0), syscall.ESRCH)
	var exit *exec.ExitError
	require.ErrorAs(t, canceledErr, &exit)
	wait, ok := exit.ProcessState.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	assert.Equal(t, syscall.SIGKILL, wait.Signal())
	t.Logf("canceled compiler group %d is absent (ESRCH); command ended with SIGKILL", pgid)
	residue, err := os.ReadDir(canceled.WorkDir)
	require.NoError(t, err)
	names := make([]string, 0, len(residue))
	for _, entry := range residue {
		names = append(names, entry.Name())
	}
	t.Logf("canceled compiler scratch stays in its operation directory: %v", names)
	retry := makeSpec()
	require.NoError(t, Build(t.Context(), retry, (*exec.Cmd).Run), logText(retry.LogFile))
	cold := makeSpec()
	cold.BuildCacheDir = filepath.Join(t.TempDir(), "cold")
	require.NoError(t, Build(t.Context(), cold, (*exec.Cmd).Run), logText(cold.LogFile))
	want, err := os.ReadFile(cold.Out)
	require.NoError(t, err)
	for _, spec := range []BuildSpec{survivor, retry} {
		actual, err := os.ReadFile(spec.Out)
		require.NoError(t, err)
		assert.Equal(t, sha256.Sum256(want), sha256.Sum256(actual))
	}
	require.NoError(t, filepath.WalkDir(cache, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		assert.False(t, strings.HasPrefix(entry.Name(), "go-build"), path)
		assert.False(t, strings.HasPrefix(entry.Name(), ".surla-stage-"), path)
		return nil
	}))
}
