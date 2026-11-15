package testwriter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecordCommitsTheCheckedTreeOnce records a verdict made from the copy's
// snapshot. It makes one commit on the copy's branch, with the user's own
// identity and no hook run, holding the files as they were judged: an edit
// made after the verdict stays an uncommitted change in the copy. Recording
// the unchanged copy again returns the same commit.
func TestRecordCommitsTheCheckedTreeOnce(t *testing.T) {
	t.Parallel()
	repo, markers := newProject(t, map[string]string{"eval/go.mod": "module example.com/shop/eval\n"})
	branch := TestsRole.Branch(1)
	wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	start := runGit(t, repo, "rev-parse", branch)
	writeFiles(t, wc.Project(), map[string]string{"eval/refund_test.txt": "judged\n"})
	judged := snapshotVerdict(t, wc)
	writeFiles(t, wc.Project(), map[string]string{"eval/refund_test.txt": "edited after the check\n"})

	commit, err := Record(t.Context(), judged, branch, runGit(t, repo, "rev-parse", branch), "Add the accepted tests")
	require.NoError(t, err)
	assert.Equal(t, commit, runGit(t, repo, "rev-parse", branch))
	assert.Equal(t, start, runGit(t, repo, "rev-parse", commit+"^"), "one commit on top of the branch")
	assert.Equal(t, "judged", runGit(t, repo, "show", commit+":eval/refund_test.txt"))
	assert.Equal(t, "Test User <test@example.com> Test User <test@example.com> Add the accepted tests",
		runGit(t, repo, "log", "-1", "--format=%an <%ae> %cn <%ce> %B", commit))
	assert.Equal(t, "M eval/refund_test.txt", runGit(t, wc.Dir(), "status", "--porcelain"),
		"the copy shows only the edit made after the check")
	assert.Empty(t, runGit(t, repo, "status", "--porcelain", "--untracked-files=all"))
	assert.Empty(t, ranHooks(t, markers), "no hook runs, including the copy's own")

	writeFiles(t, wc.Project(), map[string]string{"eval/refund_test.txt": "judged\n"})
	again, err := Record(t.Context(), snapshotVerdict(t, wc), branch, runGit(t, repo, "rev-parse", branch), "Add the accepted tests")
	require.NoError(t, err)
	assert.Equal(t, commit, again, "an unchanged copy returns the existing commit")
	assert.Equal(t, commit, runGit(t, repo, "rev-parse", branch))
	assert.Empty(t, runGit(t, wc.Dir(), "status", "--porcelain"))
}

// TestRecordRefusesAVerdictWithFindingsOrNoMessage records nothing for a
// copy that was not accepted, a verdict Check did not make, without a
// branch, or without a commit message.
func TestRecordRefusesAVerdictWithFindingsOrNoMessage(t *testing.T) {
	t.Parallel()
	repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
	branch := TestsRole.Branch(1)
	wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	start := runGit(t, repo, "rev-parse", branch)
	accepted := snapshotVerdict(t, wc)
	cases := []struct {
		name       string
		verdict    Verdict
		branch     string
		checkedTip string
		message    string
		wantErr    string
	}{
		{
			name:    "findings",
			verdict: Verdict{Findings: []Finding{{Kind: FindingBuild, Message: "missing method"}}, copy: wc},
			branch:  branch, checkedTip: start,
			message: "Add the accepted tests",
			wantErr: "only an accepted working copy is recorded",
		},
		{name: "empty branch", verdict: accepted, branch: " ", checkedTip: start, message: "Add the accepted tests", wantErr: "the branch is empty"},
		{name: "empty checked tip", verdict: accepted, branch: branch, checkedTip: " ", message: "Write tests", wantErr: "the checked tip is empty"},
		{name: "symbolic checked tip", verdict: accepted, branch: branch, checkedTip: branch, message: "Write tests", wantErr: "must be a full commit ID"},
		{name: "empty message", verdict: accepted, branch: branch, checkedTip: start, message: " ", wantErr: "the commit message is empty"},
		{name: "a verdict Check did not make", verdict: Verdict{}, branch: branch, checkedTip: start, message: "Add the accepted tests", wantErr: "did not come from Check"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Record(t.Context(), tc.verdict, tc.branch, tc.checkedTip, tc.message)
			require.ErrorContains(t, err, tc.wantErr)
			assert.Equal(t, start, runGit(t, repo, "rev-parse", branch))
		})
	}
}

// TestIdentityCheckMatchesRecord exercises the same Git-resolved identity with
// both repository configuration and explicit author/committer environment.
func TestIdentityCheckMatchesRecord(t *testing.T) {
	repo, _ := newProject(t, map[string]string{"README.md": "original\n"})
	assert.NoError(t, CheckIdentity(t.Context(), repo))
	runGit(t, repo, "config", "user.useConfigOnly", "true")
	for _, key := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		t.Setenv(key, "")
	}
	assert.ErrorIs(t, CheckIdentity(t.Context(), repo), ErrIdentityMissing)
	t.Setenv("GIT_AUTHOR_NAME", "Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "author@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Committer")
	t.Setenv("GIT_COMMITTER_EMAIL", "committer@example.com")
	require.NoError(t, CheckIdentity(t.Context(), repo))
	branch := TestsRole.Branch(1)
	wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	writeFiles(t, wc.Project(), map[string]string{"tests.txt": "accepted\n"})
	commit, err := Record(t.Context(), snapshotVerdict(t, wc), branch, runGit(t, repo, "rev-parse", branch), "Write tests")
	require.NoError(t, err)
	assert.Equal(t, "Author <author@example.com> Committer <committer@example.com>", runGit(t, repo, "log", "-1", "--format=%an <%ae> %cn <%ce>", commit))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, CheckIdentity(ctx, repo), context.Canceled)
}

// TestReviewRecordWithAnOnBranchLock verifies that an accepted verdict recorded
// while index.lock exists in the copy (which is on its build's branch) fails
// with *IndexLockError naming the lock file and leaves the branch tip unmoved,
// and succeeds once index.lock is removed.
func TestReviewRecordWithAnOnBranchLock(t *testing.T) {
	t.Parallel()
	repo, _ := newProject(t, map[string]string{"eval/go.mod": "module example.com/shop/eval\n"})
	branch := TestsRole.Branch(1)
	wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	tip := runGit(t, repo, "rev-parse", branch)
	writeFiles(t, wc.Project(), map[string]string{"eval/refund_test.txt": "judged\n"})
	accepted := snapshotVerdict(t, wc)
	lock := filepath.Join(wc.gitDir, "index.lock")
	writeFiles(t, wc.gitDir, map[string]string{"index.lock": ""})

	commit, err := Record(t.Context(), accepted, branch, tip, "Add the accepted tests")
	var lockErr *IndexLockError
	require.ErrorAs(t, err, &lockErr)
	assert.Equal(t, lock, lockErr.Path)
	assert.Empty(t, commit)
	assert.Equal(t, tip, runGit(t, repo, "rev-parse", branch), "branch tip stays unmoved when index.lock blocks read-tree")

	require.NoError(t, os.Remove(lock))
	commit, err = Record(t.Context(), accepted, branch, tip, "Add the accepted tests")
	require.NoError(t, err)
	assert.Equal(t, commit, runGit(t, repo, "rev-parse", branch))
	assert.Equal(t, tip, runGit(t, repo, "rev-parse", commit+"^"), "one commit on top of the branch")
}

// TestGitEnvSetsEnglishLocale verifies that gitEnv drops inherited LC_ALL and
// LANGUAGE entries and appends LC_ALL=C once so Git's messages stay in English.
func TestGitEnvSetsEnglishLocale(t *testing.T) {
	t.Setenv("LC_ALL", "fr_FR.UTF-8")
	t.Setenv("LANGUAGE", "fr_FR:fr")
	env := gitEnv()
	var lcAllCount int
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		assert.NotEqual(t, "LANGUAGE", name, "inherited LANGUAGE must be removed")
		if name == "LC_ALL" {
			lcAllCount++
			assert.Equal(t, "LC_ALL=C", kv)
		}
	}
	assert.Equal(t, 1, lcAllCount, "LC_ALL=C must appear exactly once")
}

// snapshotVerdict returns an accepted verdict for the copy's current files,
// made by the snapshot steps Check uses, without generating or building.
func snapshotVerdict(t *testing.T, wc WorkingCopy) Verdict {
	t.Helper()
	dir := t.TempDir()
	index, src := filepath.Join(dir, "index"), filepath.Join(dir, "src")
	require.NoError(t, stageCopy(t.Context(), wc, index, src))
	tree, err := writeTree(t.Context(), wc, index, src)
	require.NoError(t, err)
	return Verdict{copy: wc, tree: tree}
}
