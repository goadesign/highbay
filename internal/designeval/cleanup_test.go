package designeval

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReleaseTempKeepsTheDirectoryOnlyForAStuckGroup checks the evaluation's
// temporary directory: removed, with a read-only artifact store inside, after
// a success or an ordinary failure; kept and named in the error when the
// processes of its build or design program may still use it.
func TestReleaseTempKeepsTheDirectoryOnlyForAStuckGroup(t *testing.T) {
	errBuild := errors.New("exit status 2")
	errStop := errors.New("process group 42 still has processes after 10s")
	cases := []struct {
		name string
		err  error
		kept bool
	}{
		{"success", nil, false},
		{"ordinary failure", errBuild, false},
		{"stuck group", errors.Join(&stuckGroupError{err: errStop}, errBuild), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tmpDir, storeDir := tempWithReadOnlyStore(t)

			err := releaseTemp(tmpDir, storeDir, c.err)

			if !c.kept {
				if c.err == nil {
					assert.NoError(t, err)
				} else {
					assert.ErrorIs(t, err, c.err)
					assert.EqualError(t, err, c.err.Error(), "removal succeeded, so nothing is added")
				}
				assert.NoDirExists(t, tmpDir)
				return
			}
			assert.DirExists(t, tmpDir)
			assert.ErrorContains(t, err, tmpDir)
			assert.ErrorIs(t, err, errStop, "the stop failure is kept")
			assert.ErrorIs(t, err, errBuild, "the build's own error is kept")
			var stuck *stuckGroupError
			assert.ErrorAs(t, err, &stuck)
		})
	}
}

// TestRunInGroupReportsTheCommandsOwnResult checks that a command whose
// process group empties reports exactly the command's result, never a stuck
// group.
func TestRunInGroupReportsTheCommandsOwnResult(t *testing.T) {
	assert.NoError(t, runInGroup(exec.CommandContext(t.Context(), "true")))

	err := runInGroup(exec.CommandContext(t.Context(), "sh", "-c", "exit 3"))
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 3, exit.ExitCode())
	var stuck *stuckGroupError
	assert.False(t, errors.As(err, &stuck))
}

// tempWithReadOnlyStore lays out a temporary directory like Evaluate's, with
// an artifact store whose directories are read-only as candidate.Pin leaves
// them, and returns both paths.
func tempWithReadOnlyStore(t *testing.T) (tmpDir, storeDir string) {
	t.Helper()
	tmpDir = filepath.Join(t.TempDir(), "highbay-designeval-test")
	storeDir = filepath.Join(tmpDir, "store")
	pinned := filepath.Join(storeDir, "abc")
	require.NoError(t, os.MkdirAll(pinned, 0o750))
	require.NoError(t, os.WriteFile(filepath.Join(pinned, "designeval-bin"), []byte("bin"), 0o500))
	require.NoError(t, os.Chmod(pinned, 0o500))
	require.NoError(t, os.Chmod(storeDir, 0o500))
	t.Cleanup(func() {
		// A kept directory still has the read-only store.
		err := filepath.WalkDir(tmpDir, func(path string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			return os.Chmod(path, 0o700)
		})
		if !errors.Is(err, os.ErrNotExist) {
			assert.NoError(t, err)
		}
	})
	return tmpDir, storeDir
}
