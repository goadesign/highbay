// These tests verify the review's durability and history-boundary witnesses.
package controlplane

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/goadesign/surla/internal/shquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestDraftTakeWaitsForDirectoryDurability forces the existing fsync owner to
// fail after replacement/deletion. Pending remains until recovery syncs it.
func TestDraftTakeWaitsForDirectoryDurability(t *testing.T) {
	for _, kind := range []string{"replace", "delete", "new directories"} {
		t.Run(kind, func(t *testing.T) {
			f := newDraftUpdateFixture(t, "package design\nvar A=0\n", "package design\nvar A=0\n", "package design\nvar A=0\n")
			file := updateDesignFile
			want := "package design\n\nvar A = 1\n"
			if kind == "new directories" {
				file = "design/new/deeper/types.go"
				want = "package deeper\n\nvar A = 1\n"
				writeWorkspaceFiles(t, f.dir, map[string]string{file: want})
			} else if kind == "delete" {
				require.NoError(t, os.Remove(filepath.Join(f.dir, file)))
			} else {
				f.project(want)
			}
			leaf := filepath.Join(f.dir, draftFolderName, filepath.Dir(file))
			prior := fsyncDir
			failed := false
			observedPending := false
			fsyncDir = func(fd int) error {
				var actual unix.Stat_t
				if err := unix.Fstat(fd, &actual); err != nil {
					return err
				}
				info, err := os.Stat(leaf)
				if err == nil {
					stat := info.Sys().(*syscall.Stat_t)
					if uint64(actual.Dev) == uint64(stat.Dev) && uint64(actual.Ino) == uint64(stat.Ino) {
						list, found, err := f.cp.cards.loadMadeFrom()
						require.NoError(t, err)
						require.True(t, found)
						observedPending = list.Pending != nil
						if !failed {
							failed = true
							return syscall.EIO
						}
					}
				}
				return prior(fd)
			}
			t.Cleanup(func() { fsyncDir = prior })
			_, err := f.cp.UpdateDraft(asCaller("alice@example.com", CallerGuide))
			require.Error(t, err)
			assert.True(t, observedPending)
			require.NotNil(t, f.list().Pending)
			if kind == "delete" {
				_, err := os.Stat(filepath.Join(f.dir, draftFolderName, file))
				assert.ErrorIs(t, err, os.ErrNotExist)
			} else {
				got, err := os.ReadFile(filepath.Join(f.dir, draftFolderName, file))
				require.NoError(t, err)
				assert.Equal(t, want, string(got))
			}
			// Keep the observing owner installed: a completion-only recovery must
			// call it again before saving Taken and clearing Pending.
			observedPending = false
			f.update()
			assert.True(t, observedPending)
			assert.Nil(t, f.list().Pending)
		})
	}
}

// TestDraftHistoryDoesNotReadExcludedAncestry makes an unnecessary unbounded
// rev-list exceed the real Git owner's output cap. At P==S none is needed;
// after P moves every history query must explicitly exclude S.
func TestDraftHistoryDoesNotReadExcludedAncestry(t *testing.T) {
	f := newDraftUpdateFixture(t, "package design\nvar A=0\n", "package design\nvar A=0\n", "package design\nvar A=0\n")
	list := f.list()
	start := f.head()
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := t.TempDir()
	log := filepath.Join(bin, "queries")
	huge := filepath.Join(bin, "excluded-history")
	require.NoError(t, os.WriteFile(huge, []byte(strings.Repeat(strings.Repeat("a", 40)+"\n", 30000)), 0o600))
	script := "#!/bin/sh\ncase \" $* \" in *\" rev-list \"*)\n printf '%s\\n' \"$*\" >> " + shquote.Quote(log) + "\n case \" $* \" in *\" --not " + start + " \"*) ;; *) cat " + shquote.Quote(huge) + "; exit 0;; esac\n;; esac\nexec " + shquote.Quote(git) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	history, err := readDesignHistory(t.Context(), f.dir, start, madeFromSnapshot{Start: list.Start, Entries: list.Entries}, list.Taken)
	require.NoError(t, err)
	assert.Empty(t, history.versions)
	_, err = os.Stat(log)
	assert.ErrorIs(t, err, os.ErrNotExist)
	f.project("package design\nvar A=1\n")
	f.commit()
	_, err = readDesignHistory(t.Context(), f.dir, f.head(), madeFromSnapshot{Start: list.Start, Entries: list.Entries}, list.Taken)
	require.NoError(t, err)
	queries, err := os.ReadFile(log)
	require.NoError(t, err)
	for _, line := range strings.Split(strings.TrimSpace(string(queries)), "\n") {
		assert.Contains(t, line, "--not "+start)
	}
}

// TestRepairedHistoryDoesNotKeepBrokenGoAsAPermanentBlocker preserves strict
// current-commit validation while treating old unparsable text as nonmatching.
func TestRepairedHistoryDoesNotKeepBrokenGoAsAPermanentBlocker(t *testing.T) {
	base := "package design\nvar A=0\n"
	f := newDraftUpdateFixture(t, base, base, base)
	list := f.list()
	snapshot := madeFromSnapshot{Start: list.Start, Entries: list.Entries}
	f.project("package design\nvar = broken\n")
	f.commit()
	assert.Error(t, checkDesignBase(t.Context(), f.dir, f.head(), &snapshot))
	f.project(base)
	f.commit()
	assert.NoError(t, checkDesignBase(t.Context(), f.dir, f.head(), &snapshot))
	f.project("package design\nvar A=2\n")
	r := f.update()
	assert.Empty(t, r.ToCorrect)
	assert.Equal(t, []string{updateDesignFile}, r.Taken)
	// A real Git read failure still refuses instead of becoming an unlisted
	// version: the immutable start cannot resolve to a missing object.
	snapshot.Start.ProjectCommit = strings.Repeat("f", 40)
	err := checkDesignBase(t.Context(), f.dir, f.head(), &snapshot)
	require.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrNotExist))
}
