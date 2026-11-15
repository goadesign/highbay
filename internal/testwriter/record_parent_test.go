// These tests bind recording to a checked parent using real Git and snapshots,
// without generating or executing candidate programs.
package testwriter

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecordV1RefusesAMovedCheckedTip covers changed and equal trees: neither
// path may accept the newer parent, including a retry after an index lock.
func TestRecordV1RefusesAMovedCheckedTip(t *testing.T) {
	for _, scenario := range []string{"changed tree", "equal tree", "lock retry"} {
		t.Run(scenario, func(t *testing.T) {
			repo, _ := newProject(t, map[string]string{"eval/hooks.txt": "original\n"})
			branch := TestsRole.Branch(1)
			wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
			require.NoError(t, err)
			tip := runGit(t, repo, "rev-parse", branch)
			if scenario != "equal tree" {
				writeFiles(t, wc.Project(), map[string]string{"eval/hooks.txt": "judged\n"})
			}
			verdict := snapshotVerdict(t, wc)
			before, err := os.ReadFile(filepath.Join(wc.gitDir, "index"))
			require.NoError(t, err)
			if scenario == "lock retry" {
				writeFiles(t, wc.gitDir, map[string]string{"index.lock": "held by another Git command\n"})
				_, err := Record(t.Context(), verdict, branch, tip, "Accept tests")
				var locked *IndexLockError
				require.ErrorAs(t, err, &locked)
				require.NoError(t, os.Remove(filepath.Join(wc.gitDir, "index.lock")))
			}
			foreign := runGit(t, repo, "commit-tree", "--no-gpg-sign", tip+"^{tree}", "-p", tip, "-m", "Other build work")
			runGit(t, repo, "update-ref", "refs/heads/surla/build-2", foreign)
			runGit(t, repo, "update-ref", "refs/heads/"+branch, foreign, tip)
			commit, err := Record(t.Context(), verdict, branch, tip, "Accept tests")
			var author *AuthorError
			require.ErrorAs(t, err, &author)
			assert.Equal(t, FindingWorkingCopy, author.Finding.Kind)
			assert.Contains(t, author.Finding.Message, "moved from checked commit "+tip+" to "+foreign)
			assert.Contains(t, author.Finding.Message, "uncommitted (Surla commits what it accepts), and report again")
			assert.Empty(t, commit)
			assert.Equal(t, foreign, runGit(t, repo, "rev-parse", branch))
			after, err := os.ReadFile(filepath.Join(wc.gitDir, "index"))
			require.NoError(t, err)
			assert.Equal(t, before, after, "rejection preserves the original index")
			want := "original\n"
			if scenario != "equal tree" {
				want = "judged\n"
			}
			file, err := os.ReadFile(filepath.Join(wc.Project(), "eval/hooks.txt"))
			require.NoError(t, err)
			assert.Equal(t, want, string(file))
		})
	}
}

// TestRecordV1CASRejectionPreservesTheIndex advances the ref after Record's
// first read. The existing expected-tip CAS fails without touching user data.
func TestRecordV1CASRejectionPreservesTheIndex(t *testing.T) {
	repo, _ := newProject(t, map[string]string{"eval/hooks.txt": "original\n"})
	branch := TestsRole.Branch(1)
	wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	tip := runGit(t, repo, "rev-parse", branch)
	writeFiles(t, wc.Project(), map[string]string{"eval/hooks.txt": "judged\n"})
	verdict := snapshotVerdict(t, wc)
	index := filepath.Join(wc.gitDir, "index")
	before, err := os.ReadFile(index)
	require.NoError(t, err)
	foreign := runGit(t, repo, "commit-tree", "--no-gpg-sign", tip+"^{tree}", "-p", tip, "-m", "Concurrent parent")
	armRecordGitMutation(t, repo, "refs/heads/"+branch, tip, foreign, "")
	commit, err := Record(t.Context(), verdict, branch, tip, "Accept tests")
	require.Error(t, err)
	var author *AuthorError
	assert.False(t, errors.As(err, &author), "a genuine CAS failure preserves its operational error")
	var exit *exec.ExitError
	assert.ErrorAs(t, err, &exit)
	assert.Empty(t, commit)
	assert.Equal(t, foreign, runGit(t, repo, "rev-parse", branch), "the concurrent ref is never rolled back")
	after, err := os.ReadFile(index)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	file, err := os.ReadFile(filepath.Join(wc.Project(), "eval/hooks.txt"))
	require.NoError(t, err)
	assert.Equal(t, "judged\n", string(file))
	assert.NoFileExists(t, filepath.Join(wc.gitDir, "index.lock"))
	left, err := filepath.Glob(filepath.Join(wc.gitDir, "surla-record-*"))
	require.NoError(t, err)
	assert.Empty(t, left)
}

// TestRecordV1IndexPublicationFailureNamesTheDurableCommit distinguishes
// successful ref publication from a later index error without rolling it back.
func TestRecordV1IndexPublicationFailureNamesTheDurableCommit(t *testing.T) {
	repo, _ := newProject(t, map[string]string{"eval/hooks.txt": "original\n"})
	branch := TestsRole.Branch(1)
	wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	tip := runGit(t, repo, "rev-parse", branch)
	writeFiles(t, wc.Project(), map[string]string{"eval/hooks.txt": "judged\n"})
	verdict := snapshotVerdict(t, wc)
	index := filepath.Join(wc.gitDir, "index")
	before, err := os.ReadFile(index)
	require.NoError(t, err)
	armRecordGitMutation(t, repo, "refs/heads/"+branch, tip, "", index)
	commit, err := Record(t.Context(), verdict, branch, tip, "Accept tests")
	require.Error(t, err)
	require.NotEmpty(t, commit)
	assert.Contains(t, err.Error(), "recorded tests as commit "+commit+" on "+branch)
	var rename *os.LinkError
	assert.ErrorAs(t, err, &rename)
	assert.Equal(t, commit, runGit(t, repo, "rev-parse", branch))
	assert.Equal(t, tip, runGit(t, repo, "rev-parse", commit+"^"))
	assert.Equal(t, "judged", runGit(t, repo, "show", commit+":eval/hooks.txt"))
	preserved, readErr := os.ReadFile(index + ".before")
	require.NoError(t, readErr)
	assert.Equal(t, before, preserved)
	assert.DirExists(t, index, "Record leaves the obstructing replacement alone")
	assert.NoFileExists(t, filepath.Join(wc.gitDir, "index.lock"))
}

// armRecordGitMutation uses the real update-ref command as a deterministic
// publication boundary. A foreign ID moves it before CAS; an index path
// obstructs index publication after successful CAS. These are test-owned paths.
func armRecordGitMutation(t *testing.T, repo, ref, checkedTip, foreign, index string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := t.TempDir()
	script := "#!/bin/sh\nfor arg do\nif [ \"$arg\" = update-ref ]; then\n" +
		"if [ -n \"$SURLA_RECORD_FOREIGN\" ]; then\n" +
		"\"$SURLA_RECORD_GIT\" -C \"$SURLA_RECORD_REPO\" -c core.hooksPath=/dev/null update-ref \"$SURLA_RECORD_REF\" \"$SURLA_RECORD_FOREIGN\" \"$SURLA_RECORD_TIP\" || exit $?\n" +
		"exec \"$SURLA_RECORD_GIT\" \"$@\"\nfi\n" +
		"\"$SURLA_RECORD_GIT\" \"$@\" || exit $?\n" +
		"mv \"$SURLA_RECORD_INDEX\" \"$SURLA_RECORD_INDEX.before\" && mkdir \"$SURLA_RECORD_INDEX\"\nexit $?\nfi\ndone\n" +
		"exec \"$SURLA_RECORD_GIT\" \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
	for key, value := range map[string]string{
		"SURLA_RECORD_GIT": realGit, "SURLA_RECORD_REPO": repo, "SURLA_RECORD_REF": ref,
		"SURLA_RECORD_TIP": checkedTip, "SURLA_RECORD_FOREIGN": foreign, "SURLA_RECORD_INDEX": index,
	} {
		t.Setenv(key, value)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestRecordV1SwitchBeforeIndexReservation covers normal Git switching after
// Record's first Facts read, including the equal-tree path that writes no ref.
func TestRecordV1SwitchBeforeIndexReservation(t *testing.T) {
	for _, equal := range []bool{false, true} {
		name := "changed tree"
		if equal {
			name = "equal tree"
		}
		t.Run(name, func(t *testing.T) {
			repo, _ := newProject(t, map[string]string{"eval/hooks.txt": "original\n"})
			branch := TestsRole.Branch(1)
			wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
			require.NoError(t, err)
			tip := runGit(t, repo, "rev-parse", branch)
			if !equal {
				writeFiles(t, wc.Project(), map[string]string{"eval/hooks.txt": "judged\n"})
			}
			verdict := snapshotVerdict(t, wc)
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			bin := t.TempDir()
			trigger := "commit-tree"
			if equal {
				trigger = tip + "^{tree}"
			}
			script := "#!/bin/sh\nfor arg do\n" +
				"if [ \"$arg\" = \"$SURLA_SWITCH_TRIGGER\" ] && mkdir \"$SURLA_SWITCH_DIR/claimed\" 2>/dev/null; then\n" +
				"\"$SURLA_SWITCH_GIT\" -C \"$SURLA_SWITCH_WC\" -c core.hooksPath=/dev/null switch -q -c side-work || exit $?\n" +
				"cp \"$SURLA_SWITCH_INDEX\" \"$SURLA_SWITCH_DIR/index\" || exit $?\nfi\ndone\n" +
				"exec \"$SURLA_SWITCH_GIT\" \"$@\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
			for key, value := range map[string]string{
				"SURLA_SWITCH_GIT": realGit, "SURLA_SWITCH_WC": wc.Dir(), "SURLA_SWITCH_TRIGGER": trigger,
				"SURLA_SWITCH_INDEX": filepath.Join(wc.gitDir, "index"), "SURLA_SWITCH_DIR": bin,
			} {
				t.Setenv(key, value)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			commit, err := Record(t.Context(), verdict, branch, tip, "Accept tests")
			var author *AuthorError
			require.ErrorAs(t, err, &author)
			assert.Equal(t, FindingWorkingCopy, author.Finding.Kind)
			assert.Contains(t, author.Finding.Message, "side-work")
			assert.NotContains(t, author.Finding.Message, "index.lock", "our reservation ends before the finding is returned")
			assert.Empty(t, commit)
			assert.Equal(t, tip, runGit(t, repo, "rev-parse", branch))
			assert.Equal(t, "side-work", runGit(t, wc.Dir(), "symbolic-ref", "--short", "HEAD"))
			before, err := os.ReadFile(filepath.Join(bin, "index"))
			require.NoError(t, err)
			after, err := os.ReadFile(filepath.Join(wc.gitDir, "index"))
			require.NoError(t, err)
			assert.Equal(t, before, after, "Record cannot publish its index over the side-work index")
			assert.NoFileExists(t, filepath.Join(wc.gitDir, "index.lock"))
		})
	}
}
