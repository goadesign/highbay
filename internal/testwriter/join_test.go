package testwriter

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/designeval"
)

// TestFactsReadsBranchOperationsAndUncommitted verifies WorkingCopy.Facts and
// ShownName across Surla build branches, agent-chosen branches, detached HEAD,
// every Git operation in the administrative folder (including bisect as
// "none"), and uncommitted/untracked paths (including more than five paths and
// unprintable names).
func TestFactsReadsBranchOperationsAndUncommitted(t *testing.T) {
	t.Parallel()
	repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
	base := runGit(t, repo, "rev-parse", "HEAD")
	wc, err := MakeCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1), base)
	require.NoError(t, err)

	// Clean Surla build branch.
	facts, err := wc.Facts(t.Context())
	require.NoError(t, err)
	assert.Equal(t, CopyFacts{
		Branch:           "surla/build-1",
		Detached:         false,
		Operation:        "none",
		Uncommitted:      []string{},
		UncommittedCount: 0,
	}, facts)

	// Agent-chosen branches (including surla/build-2-backup and surla/build-007)
	// are shell-quoted; unprintable names are withheld.
	for _, tc := range []struct {
		branch string
		want   string
	}{
		{branch: "feature/work", want: "'feature/work'"},
		{branch: "surla/build-2-backup", want: "'surla/build-2-backup'"},
		{branch: "surla/build-007", want: "'surla/build-007'"},
	} {
		runGit(t, wc.Dir(), "switch", "--quiet", "-c", tc.branch, base)
		facts, err = wc.Facts(t.Context())
		require.NoError(t, err)
		assert.Equal(t, tc.want, facts.Branch)
		assert.False(t, facts.Detached)
	}
	assert.Equal(t, "a name that is not shown", ShownName("bad\nbranch"))
	assert.Equal(t, "a name that is not shown", ShownName("bad\x07path.go"))
	assert.Equal(t, "a name that is not shown", shownBranch("bad\nbranch"))

	// Detached HEAD.
	runGit(t, wc.Dir(), "switch", "--quiet", "--detach", base)
	facts, err = wc.Facts(t.Context())
	require.NoError(t, err)
	assert.True(t, facts.Detached)
	assert.Empty(t, facts.Branch)

	// Return to surla/build-1 and verify every operation marker in wc.gitDir.
	runGit(t, wc.Dir(), "switch", "--quiet", "surla/build-1")
	opCases := []struct {
		name  string
		files map[string]string
		dirs  []string
		want  string
	}{
		{name: "MERGE_HEAD", files: map[string]string{"MERGE_HEAD": base + "\n"}, want: "merge"},
		{name: "rebase-merge", dirs: []string{"rebase-merge"}, want: "rebase"},
		{name: "rebase-apply without applying", dirs: []string{"rebase-apply"}, want: "rebase"},
		{name: "rebase-apply with applying", files: map[string]string{"rebase-apply/applying": "1\n"}, want: "am"},
		{name: "CHERRY_PICK_HEAD", files: map[string]string{"CHERRY_PICK_HEAD": base + "\n"}, want: "cherry_pick"},
		{name: "REVERT_HEAD", files: map[string]string{"REVERT_HEAD": base + "\n"}, want: "revert"},
		{name: "sequencer cherry-pick", files: map[string]string{"sequencer/todo": "pick " + base + "\n"}, want: "cherry_pick"},
		{name: "sequencer revert", files: map[string]string{"sequencer/todo": "# comment\nrevert " + base + "\n"}, want: "revert"},
		{name: "bisect is none", files: map[string]string{"BISECT_LOG": "# bisect\n", "BISECT_START": "main\n"}, want: "none"},
	}
	for _, tc := range opCases {
		for _, d := range tc.dirs {
			require.NoError(t, os.MkdirAll(filepath.Join(wc.gitDir, d), 0o755))
		}
		writeFiles(t, wc.gitDir, tc.files)
		facts, err = wc.Facts(t.Context())
		require.NoError(t, err, tc.name)
		assert.Equal(t, tc.want, facts.Operation, tc.name)
		for _, d := range tc.dirs {
			require.NoError(t, os.RemoveAll(filepath.Join(wc.gitDir, d)))
		}
		for rel := range tc.files {
			top := filepath.SplitList(rel)[0]
			if i := filepath.Dir(rel); i != "." {
				top = i
			}
			require.NoError(t, os.RemoveAll(filepath.Join(wc.gitDir, top)))
		}
	}

	// Uncommitted paths: 6 paths, including one with a newline in its name,
	// return the first 5 in byte order and UncommittedCount == 6.
	writeFiles(t, wc.Dir(), map[string]string{
		"a.txt":        "1\n",
		"b\nbreak.txt": "2\n",
		"c.txt":        "3\n",
		"d.txt":        "4\n",
		"e.txt":        "5\n",
		"f.txt":        "6\n",
	})
	facts, err = wc.Facts(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 6, facts.UncommittedCount)
	assert.Equal(t, []string{
		"'a.txt'",
		"a name that is not shown",
		"'c.txt'",
		"'d.txt'",
		"'e.txt'",
	}, facts.Uncommitted)
}

// TestJoinCommitsLeftoversAndReplacesIgnoredFile (S1, S4, N1, N2) verifies:
//   - S1: uncommitted edits and untracked files on an earlier build branch are
//     committed there with "Left uncommitted when change <id> started", the
//     copy switches to surla/build-<n> at the new base, and an ignored file in
//     the copy at a path the new base tracks is replaced by the base's file;
//   - S4: joining again when already on surla/build-<n> returns AlreadyJoined,
//     and joining a next build with nothing uncommitted makes no leftover
//     commit;
//   - N1 & N2: build numbers skip missing builds cleanly and legacy surla/tests
//     (k == 0) is treated as an earlier build branch of TestsRole.
func TestJoinCommitsLeftoversAndReplacesIgnoredFile(t *testing.T) {
	t.Parallel()
	repo, markers := newProject(t, map[string]string{
		".gitignore": "bin/\n",
		"svc/a.go":   "package svc\n",
	})
	b1 := runGit(t, repo, "rev-parse", "HEAD")
	wc, err := MakeCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1), b1)
	require.NoError(t, err)

	// Builder makes a committed change C1, an uncommitted edit, an untracked
	// file, and an ignored file at bin/tool.
	writeFiles(t, wc.Dir(), map[string]string{"svc/refund.go": "package svc // refund\n"})
	runGit(t, wc.Dir(), "add", "-A")
	runGit(t, wc.Dir(), "commit", "--quiet", "-m", "C1")
	writeFiles(t, wc.Dir(), map[string]string{
		"svc/a.go":    "package svc // edit\n",
		"svc/wip.txt": "wip\n",
		"bin/tool":    "out\n",
	})

	// User advances main to b2, force-adding a tracked file at bin/tool.
	writeFiles(t, repo, map[string]string{"bin/tool": "tracked\n"})
	runGit(t, repo, "add", "-f", "bin/tool")
	runGit(t, repo, "commit", "--quiet", "-m", "B2")
	b2 := runGit(t, repo, "rev-parse", "HEAD")

	// S1: join build 2 (and N1: skip to build 3 later).
	outcome, facts, err := wc.Join(t.Context(), BuilderRole, 2, b2, "exports")
	require.NoError(t, err)
	assert.Equal(t, Joined, outcome)
	assert.Equal(t, CopyFacts{
		Branch:           "surla/build-2",
		Detached:         false,
		Operation:        "none",
		Uncommitted:      []string{},
		UncommittedCount: 0,
	}, facts)
	assert.Equal(t, b2, runGit(t, wc.Dir(), "rev-parse", "HEAD"))
	tool, err := os.ReadFile(filepath.Join(wc.Dir(), "bin", "tool"))
	require.NoError(t, err)
	assert.Equal(t, "tracked\n", string(tool), "ignored file at a path the new base tracks is replaced")
	assert.Equal(t, "Left uncommitted when change exports started\nC1\nStart the project",
		runGit(t, repo, "log", "--format=%s", "surla/build-1"))
	assert.Equal(t, "wip", runGit(t, repo, "show", "surla/build-1:svc/wip.txt"))
	assert.Equal(t, "package svc // edit", runGit(t, repo, "show", "surla/build-1:svc/a.go"))

	// S4: calling Join again for build 2 returns AlreadyJoined and commits nothing.
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 2, b2, "exports")
	require.NoError(t, err)
	assert.Equal(t, AlreadyJoined, outcome)
	assert.Equal(t, "surla/build-2", facts.Branch)

	// S4 + N1: joining build 4 with nothing uncommitted on surla/build-2 makes
	// no leftover commit on surla/build-2.
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 4, b2, "invoices")
	require.NoError(t, err)
	assert.Equal(t, Joined, outcome)
	assert.Equal(t, "surla/build-4", facts.Branch)
	assert.Equal(t, b2, runGit(t, repo, "rev-parse", "surla/build-2"), "clean earlier branch gets no leftover commit")

	// N2: legacy surla/tests (k == 0) is an earlier build branch of TestsRole.
	tw, err := MakeCopy(t.Context(), repo, TestsRole, Branch, b1)
	require.NoError(t, err)
	writeFiles(t, tw.Dir(), map[string]string{"eval/acceptance/wip_test.go": "package acceptance\n"})
	outcome, facts, err = tw.Join(t.Context(), TestsRole, 1, b2, "exports")
	require.NoError(t, err)
	assert.Equal(t, Joined, outcome)
	assert.Equal(t, "surla/tests-1", facts.Branch)
	assert.Equal(t, "Left uncommitted when change exports started\nStart the project",
		runGit(t, repo, "log", "--format=%s", "surla/tests"))
	assert.Empty(t, ranHooks(t, markers))
}

// TestJoinRestoresAndCleansGeneratedPathsInTestsCopy (Q9 and after-merge
// review note 4) verifies that joining the tests copy restores and cleans
// eval/gen/, eval/AGENTS_QUICKSTART.md, and eval/design/story_tests.go
// (including resolving an unmerged index entry on a generated path) before
// committing uncommitted hand-written test changes on the earlier build branch.
func TestJoinRestoresAndCleansGeneratedPathsInTestsCopy(t *testing.T) {
	t.Parallel()
	repo, _ := newProject(t, map[string]string{
		"eval/gen/evals/suite.go":       "package evals // committed\n",
		"eval/AGENTS_QUICKSTART.md":     "committed guide\n",
		"eval/acceptance/acceptance.go": "package acceptance // v0\n",
	})
	b1 := runGit(t, repo, "rev-parse", "HEAD")
	tw, err := MakeCopy(t.Context(), repo, TestsRole, TestsRole.Branch(1), b1)
	require.NoError(t, err)

	// Dirty all three generated paths, add an untracked file under eval/gen,
	// put an unmerged stage-1/2/3 entry on eval/gen/evals/suite.go and on
	// eval/design/story_tests.go, and edit a hand-written file.
	writeFiles(t, tw.Dir(), map[string]string{
		"eval/gen/evals/suite.go":           "package evals // prepared\n",
		"eval/gen/evals/extra.go":           "package evals // untracked in gen\n",
		"eval/AGENTS_QUICKSTART.md":         "prepared guide\n",
		"eval/" + designeval.StoryTestsPath: "package design // story table\n",
		"eval/acceptance/acceptance.go":     "package acceptance // hand-written edit\n",
	})
	blob := runGit(t, repo, "hash-object", "-w", filepath.Join(tw.Dir(), "eval/gen/evals/suite.go"))
	indexInfo := fmt.Sprintf("100644 %s 1\teval/gen/evals/suite.go\n100644 %s 2\teval/gen/evals/suite.go\n100644 %s 2\teval/%s\n",
		blob, blob, blob, designeval.StoryTestsPath)
	runGitStdin(t, tw.Dir(), indexInfo, "update-index", "--index-info")

	outcome, facts, err := tw.Join(t.Context(), TestsRole, 2, b1, "refunds")
	require.NoError(t, err)
	assert.Equal(t, Joined, outcome)
	assert.Equal(t, "surla/tests-2", facts.Branch)
	assert.Zero(t, facts.UncommittedCount)

	// The leftover commit on surla/tests-1 holds the hand-written edit and
	// none of the generated edits or untracked generated files.
	assert.Equal(t, "Left uncommitted when change refunds started\nStart the project",
		runGit(t, repo, "log", "--format=%s", "surla/tests-1"))
	assert.Equal(t, "package acceptance // hand-written edit",
		runGit(t, repo, "show", "surla/tests-1:eval/acceptance/acceptance.go"))
	assert.Equal(t, "package evals // committed",
		runGit(t, repo, "show", "surla/tests-1:eval/gen/evals/suite.go"))
	assert.Equal(t, "committed guide",
		runGit(t, repo, "show", "surla/tests-1:eval/AGENTS_QUICKSTART.md"))
	assert.NotContains(t, runGit(t, repo, "ls-tree", "-r", "--name-only", "surla/tests-1"),
		designeval.StoryTestsPath)
	assert.NotContains(t, runGit(t, repo, "ls-tree", "-r", "--name-only", "surla/tests-1"),
		"eval/gen/evals/extra.go")

	// A symbolic link at eval, eval/design, or eval/gen pointing to a folder
	// outside the copy must never be followed to delete files outside the copy.
	for idx, linkRel := range []string{"eval", "eval/design", "eval/gen"} {
		outside := t.TempDir()
		writeFiles(t, outside, map[string]string{
			"gen/precious.txt":      "user's file\n",
			"AGENTS_QUICKSTART.md":  "user's notes\n",
			"design/story_tests.go": "user's code\n",
			"story_tests.go":        "user's design code\n",
			"precious.txt":          "user's gen file\n",
		})
		linkRepo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
		linkBase := runGit(t, linkRepo, "rev-parse", "HEAD")
		linkCopy, err := MakeCopy(t.Context(), linkRepo, TestsRole, TestsRole.Branch(1), linkBase)
		require.NoError(t, err)
		target := filepath.Join(linkCopy.Dir(), filepath.FromSlash(linkRel))
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o755))
		require.NoError(t, os.Symlink(outside, target))

		outcome, _, err := linkCopy.Join(t.Context(), TestsRole, 2+idx, linkBase, "exports")
		require.NoError(t, err)
		assert.Equal(t, Joined, outcome)
		assert.FileExists(t, filepath.Join(outside, "gen", "precious.txt"),
			"Join must not delete outside files through link %s", linkRel)
		assert.FileExists(t, filepath.Join(outside, "AGENTS_QUICKSTART.md"),
			"Join must not delete outside files through link %s", linkRel)
		assert.FileExists(t, filepath.Join(outside, "design", "story_tests.go"),
			"Join must not delete outside files through link %s", linkRel)
		assert.FileExists(t, filepath.Join(outside, "story_tests.go"),
			"Join must not delete outside files through link %s", linkRel)
		assert.FileExists(t, filepath.Join(outside, "precious.txt"),
			"Join must not delete outside files through link %s", linkRel)
	}
}

// TestJoinRecoversFromInterruptedSteps (J8 and review note 5) verifies that:
//   - a stop right after the private index is written, or right after the
//     copy's own index is updated by read-tree before update-ref moves the
//     branch, leaves the copy able to join on the next call, which makes
//     exactly one leftover commit on the earlier build branch;
//   - a stop inside git switch -c after Git unpacked the new base's tree into
//     the copy's index and working tree while HEAD is still the earlier build
//     branch does not make a misleading leftover commit reverting the earlier
//     branch to the base's tree.
func TestJoinRecoversFromInterruptedSteps(t *testing.T) {
	t.Parallel()

	for _, stopAt := range []string{"afterPrivateIndex", "afterCopyIndex"} {
		t.Run("J8_"+stopAt, func(t *testing.T) {
			t.Parallel()
			repo, _ := newProject(t, map[string]string{"svc/a.go": "package svc\n"})
			b1 := runGit(t, repo, "rev-parse", "HEAD")
			wc, err := MakeCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1), b1)
			require.NoError(t, err)
			writeFiles(t, wc.Dir(), map[string]string{"svc/wip.go": "package svc // leftover\n"})

			hooks := joinHooks{}
			stopErr := fmt.Errorf("simulated stop at %s", stopAt)
			if stopAt == "afterPrivateIndex" {
				hooks.afterPrivateIndex = func() error { return stopErr }
			} else {
				hooks.afterCopyIndex = func() error { return stopErr }
			}
			_, _, err = wc.join(t.Context(), BuilderRole, 2, b1, "exports", hooks)
			require.ErrorIs(t, err, stopErr)

			// The next Join succeeds and commits the leftover work once on surla/build-1.
			outcome, facts, err := wc.Join(t.Context(), BuilderRole, 2, b1, "exports")
			require.NoError(t, err)
			assert.Equal(t, Joined, outcome)
			assert.Equal(t, "surla/build-2", facts.Branch)
			assert.Equal(t, "Left uncommitted when change exports started\nStart the project",
				runGit(t, repo, "log", "--format=%s", "surla/build-1"))
			assert.Equal(t, "package svc // leftover",
				runGit(t, repo, "show", "surla/build-1:svc/wip.go"))
		})
	}

	t.Run("stop_inside_git_switch_c", func(t *testing.T) {
		t.Parallel()
		repo, _ := newProject(t, map[string]string{"svc/a.go": "package svc // base\n"})
		base := runGit(t, repo, "rev-parse", "HEAD")
		wc, err := MakeCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1), base)
		require.NoError(t, err)

		// Commit C1 on surla/build-1 so surla/build-1's tree differs from base.
		writeFiles(t, wc.Dir(), map[string]string{"svc/a.go": "package svc // C1\n"})
		runGit(t, wc.Dir(), "add", "-A")
		runGit(t, wc.Dir(), "commit", "--quiet", "-m", "C1")
		c1 := runGit(t, wc.Dir(), "rev-parse", "HEAD")

		// Simulate a stop inside git switch -c surla/build-2 <base>: Git has
		// unpacked base's tree into the copy's index and working tree, but has
		// not yet created surla/build-2 or moved HEAD off surla/build-1.
		runGit(t, wc.Dir(), "read-tree", "-u", "--reset", base)
		require.Equal(t, "refs/heads/surla/build-1", runGit(t, wc.Dir(), "symbolic-ref", "HEAD"))

		outcome, facts, err := wc.Join(t.Context(), BuilderRole, 2, base, "exports")
		require.NoError(t, err)
		assert.Equal(t, Joined, outcome)
		assert.Equal(t, "surla/build-2", facts.Branch)
		assert.Equal(t, base, runGit(t, wc.Dir(), "rev-parse", "HEAD"))
		assert.Equal(t, c1, runGit(t, repo, "rev-parse", "surla/build-1"),
			"no misleading leftover commit reverts surla/build-1 to base's tree")
	})

	t.Run("stop_inside_git_switch_c_tests_copy_with_differing_generated_files", func(t *testing.T) {
		t.Parallel()
		repo, _ := newProject(t, map[string]string{
			"eval/gen/evals/suite.go":   "package evals // v1\n",
			"eval/acceptance/a_test.go": "package acceptance // v1\n",
		})
		b1 := runGit(t, repo, "rev-parse", "HEAD")
		tw, err := MakeCopy(t.Context(), repo, TestsRole, TestsRole.Branch(1), b1)
		require.NoError(t, err)
		writeFiles(t, tw.Dir(), map[string]string{"eval/acceptance/a_test.go": "package acceptance // T1\n"})
		runGit(t, tw.Dir(), "commit", "--quiet", "-am", "T1")
		t1 := runGit(t, tw.Dir(), "rev-parse", "HEAD")

		// main moves to B2 with a regenerated eval/gen and a new service file.
		writeFiles(t, repo, map[string]string{
			"eval/gen/evals/suite.go": "package evals // v2\n",
			"svc/b.go":                "package svc // b2\n",
		})
		runGit(t, repo, "add", "-A")
		runGit(t, repo, "commit", "--quiet", "-m", "B2")
		b2 := runGit(t, repo, "rev-parse", "HEAD")

		// Simulate a stop inside git switch -c after unpacking B2.
		runGit(t, tw.Dir(), "read-tree", "-u", "--reset", b2)
		twLock := filepath.Join(tw.gitDir, "index.lock")
		writeFiles(t, tw.gitDir, map[string]string{"index.lock": "held\n"})
		outcome, facts, err := tw.Join(t.Context(), TestsRole, 2, b2, "exports")
		require.NoError(t, err)
		assert.Equal(t, Refused, outcome)
		assert.Equal(t, twLock, facts.IndexLock)
		assert.Contains(t, facts.Step,
			"If no git command is running in your working folder, delete "+twLock+", then call get_test_writer_assignment again.")
		require.NoError(t, os.Remove(twLock))

		outcome, facts, err = tw.Join(t.Context(), TestsRole, 2, b2, "exports")
		require.NoError(t, err)
		assert.Equal(t, Joined, outcome)
		assert.Equal(t, "surla/tests-2", facts.Branch)
		assert.Equal(t, t1, runGit(t, repo, "rev-parse", "surla/tests-1"),
			"no misleading leftover commit on surla/tests-1; got:\n%s",
			runGit(t, repo, "log", "--stat", "--format=%s", t1+"..surla/tests-1"))
	})

	t.Run("cancelled_and_partial_switch_c", func(t *testing.T) {
		t.Parallel()
		repo, _ := newProject(t, map[string]string{"svc/a.go": "package svc // a\n"})
		b1 := runGit(t, repo, "rev-parse", "HEAD")
		wc, err := MakeCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1), b1)
		require.NoError(t, err)
		writeFiles(t, wc.Dir(), map[string]string{"svc/c1.go": "package svc // c1\n"})
		runGit(t, wc.Dir(), "add", "-A")
		runGit(t, wc.Dir(), "commit", "--quiet", "-m", "C1")
		c1 := runGit(t, wc.Dir(), "rev-parse", "HEAD")

		// B2 adds 200 files on main.
		b2Files := map[string]string{"svc/b.go": "package svc // b2\n"}
		for i := range 200 {
			b2Files[fmt.Sprintf("big/f%03d.txt", i)] = fmt.Sprintf("content %d\n", i)
		}
		writeFiles(t, repo, b2Files)
		runGit(t, repo, "add", "-A")
		runGit(t, repo, "commit", "--quiet", "-m", "B2")
		b2 := runGit(t, repo, "rev-parse", "HEAD")

		// 1. Simulate a partial unpack of B2: 15 of B2's 200 files written
		// with B2's contents plus one 0-byte file (big/f015.txt) interrupted
		// between open and write while HEAD and index are still at C1 on
		// surla/build-1.
		for i := range 15 {
			writeFiles(t, wc.Dir(), map[string]string{
				fmt.Sprintf("big/f%03d.txt", i): fmt.Sprintf("content %d\n", i),
			})
		}
		writeFiles(t, wc.Dir(), map[string]string{"big/f015.txt": ""})

		// 2. A copy already on its own branch (surla/build-1 for build 1)
		// writes nothing and returns AlreadyJoined even when index.lock is
		// held by another process (such as a prompt's or editor's git status).
		lockPath := filepath.Join(wc.gitDir, "index.lock")
		writeFiles(t, wc.gitDir, map[string]string{"index.lock": "held by external git\n"})
		outcome, facts, err := wc.Join(t.Context(), BuilderRole, 1, b1, "refunds")
		require.NoError(t, err)
		assert.Equal(t, AlreadyJoined, outcome)
		assert.Equal(t, "surla/build-1", facts.Branch)
		assert.Empty(t, facts.Step)
		assert.FileExists(t, lockPath)

		// 3. When index.lock is present on a path that goes on to write (build
		// 2), Join refuses without deleting index.lock and names the recovery
		// step for the role.
		outcome, facts, err = wc.Join(t.Context(), BuilderRole, 2, b2, "exports")
		require.NoError(t, err)
		assert.Equal(t, Refused, outcome)
		assert.Equal(t, "surla/build-1", facts.Branch)
		assert.Equal(t, "none", facts.Operation)
		assert.Equal(t, lockPath, facts.IndexLock)
		assert.Contains(t, facts.Step,
			"If no git command is running in your working folder, delete "+lockPath+", then call get_builder_assignment again.")
		assert.Equal(t, 16, facts.UncommittedCount)
		assert.FileExists(t, lockPath, "Join must not delete an existing index.lock")

		// 4. If another process acquires index.lock in the middle of Join
		// (while writeCtx has not timed out), handleJoinWriteError refuses
		// with the lock's path and recovery step without deleting the lock.
		require.NoError(t, os.Remove(lockPath))
		outcome, facts, err = wc.join(t.Context(), BuilderRole, 2, b2, "exports", joinHooks{
			beforeSwitch: func() error {
				writeFiles(t, wc.gitDir, map[string]string{"index.lock": "acquired mid-join\n"})
				return nil
			},
		})
		require.NoError(t, err)
		assert.Equal(t, Refused, outcome)
		assert.Equal(t, lockPath, facts.IndexLock)
		assert.Contains(t, facts.Step,
			"If no git command is running in your working folder, delete "+lockPath+", then call get_builder_assignment again.")
		assert.FileExists(t, lockPath)

		// 5. After a timeout-killed write, a leftover index.lock stays on disk
		// (Surla never deletes index.lock) and the next Join refuses with the
		// lock's recovery step.
		require.NoError(t, os.Remove(lockPath))
		_, _, err = wc.join(t.Context(), BuilderRole, 2, b2, "exports", joinHooks{
			writeTimeout: 500 * time.Millisecond,
			beforeSwitch: func() error {
				writeFiles(t, wc.gitDir, map[string]string{"index.lock": ""})
				time.Sleep(600 * time.Millisecond)
				return nil
			},
		})
		require.Error(t, err)
		assert.FileExists(t, lockPath, "Surla never deletes index.lock after a timeout")

		outcome, facts, err = wc.Join(t.Context(), BuilderRole, 2, b2, "exports")
		require.NoError(t, err)
		assert.Equal(t, Refused, outcome)
		assert.Equal(t, lockPath, facts.IndexLock)
		assert.Contains(t, facts.Step,
			"If no git command is running in your working folder, delete "+lockPath+", then call get_builder_assignment again.")
		require.NoError(t, os.Remove(lockPath))

		// 6. Re-plant the partial unpack (with the 0-byte big/f015.txt) after
		// step 4's recoverStoppedSwitch completed, and cancel the caller
		// context right as beforeSwitch returns to verify the copy-changing
		// commands finish under writeCtx without leaving index.lock or making
		// a partial commit on surla/build-1.
		runGit(t, wc.Dir(), "read-tree", "-u", "--reset", c1)
		for i := range 15 {
			writeFiles(t, wc.Dir(), map[string]string{
				fmt.Sprintf("big/f%03d.txt", i): fmt.Sprintf("content %d\n", i),
			})
		}
		writeFiles(t, wc.Dir(), map[string]string{"big/f015.txt": ""})
		ctx, cancel := context.WithCancel(t.Context())
		outcome, facts, err = wc.join(ctx, BuilderRole, 2, b2, "exports", joinHooks{
			beforeSwitch: func() error {
				cancel()
				return nil
			},
		})
		require.NoError(t, err)
		assert.Equal(t, Joined, outcome)
		assert.Equal(t, "surla/build-2", facts.Branch)
		assert.Empty(t, facts.IndexLock)
		assert.Empty(t, facts.Step)
		assert.NoFileExists(t, lockPath)
		assert.Equal(t, c1, runGit(t, repo, "rev-parse", "surla/build-1"),
			"partial B2 unpack (including one empty blob) must not be committed as leftovers on surla/build-1")
		assert.Equal(t, b2, runGit(t, wc.Dir(), "rev-parse", "HEAD"))

		// 5. Counterexample for isStoppedSwitch: when B3 changes both svc/a.go
		// and svc/b.go, and the copy on surla/build-2 has one path equal to
		// B3's (svc/a.go) alongside a non-empty agent edit to another path B3
		// changes (svc/b.go), Join must NOT treat the working tree as a
		// stopped switch; it must commit both files as leftovers on
		// surla/build-2 before switching to surla/build-3.
		writeFiles(t, repo, map[string]string{
			"svc/a.go": "package svc // b3 a\n",
			"svc/b.go": "package svc // b3 b\n",
		})
		runGit(t, repo, "add", "-A")
		runGit(t, repo, "commit", "--quiet", "-m", "B3")
		b3 := runGit(t, repo, "rev-parse", "HEAD")

		writeFiles(t, wc.Dir(), map[string]string{
			"svc/a.go": "package svc // b3 a\n",
			"svc/b.go": "package svc // agent non-empty edit\n",
		})
		outcome, facts, err = wc.Join(t.Context(), BuilderRole, 3, b3, "invoices")
		require.NoError(t, err)
		assert.Equal(t, Joined, outcome)
		assert.Equal(t, "surla/build-3", facts.Branch)
		assert.Equal(t, "Left uncommitted when change invoices started",
			runGit(t, repo, "log", "-1", "--format=%s", "surla/build-2"))
		assert.Equal(t, "package svc // agent non-empty edit",
			runGit(t, repo, "show", "surla/build-2:svc/b.go"))
		assert.Equal(t, "package svc // b3 a",
			runGit(t, repo, "show", "surla/build-2:svc/a.go"))
	})
}

// TestJoinRefusesAndHandlesConcurrentEdit (J6, J9, J11, R1) verifies:
//   - J6: when surla/build-2 already exists and the copy was switched back to
//     surla/build-1 or detached, Join returns Refused without moving it;
//   - J9: a clean copy on an agent-chosen side branch or detached HEAD joins
//     surla/build-2 and leaves the side branch untouched; a copy on a side
//     branch or detached HEAD with uncommitted work is refused;
//   - J11: a copy with an operation in progress (even on its own branch) is
//     refused;
//   - R1: if a file changes in the copy after the leftover commit and before
//     git switch -c runs, Join returns ChangedDuringJoin without creating
//     surla/build-2, and the next Join commits that edit on surla/build-1 and
//     switches to surla/build-2.
func TestJoinRefusesAndHandlesConcurrentEdit(t *testing.T) {
	t.Parallel()
	repo, _ := newProject(t, map[string]string{"svc/a.go": "package svc\n"})
	b1 := runGit(t, repo, "rev-parse", "HEAD")
	writeFiles(t, repo, map[string]string{"svc/b.go": "package svc // b2\n"})
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "--quiet", "-m", "B2")
	b2 := runGit(t, repo, "rev-parse", "HEAD")

	wc, err := MakeCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1), b1)
	require.NoError(t, err)

	// J9 clean side branch joins and leaves the side branch untouched.
	runGit(t, wc.Dir(), "switch", "--quiet", "-c", "side", b1)
	writeFiles(t, wc.Dir(), map[string]string{"svc/side.go": "package svc // side\n"})
	runGit(t, wc.Dir(), "add", "-A")
	runGit(t, wc.Dir(), "commit", "--quiet", "-m", "side commit")
	sideTip := runGit(t, wc.Dir(), "rev-parse", "HEAD")

	outcome, facts, err := wc.Join(t.Context(), BuilderRole, 2, b2, "exports")
	require.NoError(t, err)
	assert.Equal(t, Joined, outcome)
	assert.Equal(t, "surla/build-2", facts.Branch)
	assert.Equal(t, b2, runGit(t, wc.Dir(), "rev-parse", "HEAD"))
	assert.Equal(t, sideTip, runGit(t, repo, "rev-parse", "side"), "side branch is untouched")

	// J6: switch back to surla/build-1 after surla/build-2 exists -> Refused.
	runGit(t, wc.Dir(), "switch", "--quiet", "surla/build-1")
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 2, b2, "exports")
	require.NoError(t, err)
	assert.Equal(t, Refused, outcome)
	assert.Equal(t, "surla/build-1", facts.Branch)

	// J9 clean detached HEAD joins build 3.
	runGit(t, wc.Dir(), "switch", "--quiet", "--detach", b1)
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 3, b2, "invoices")
	require.NoError(t, err)
	assert.Equal(t, Joined, outcome)
	assert.Equal(t, "surla/build-3", facts.Branch)

	// J9 dirty side branch, dirty later build branch (k > n), and dirty
	// detached HEAD are refused.
	runGit(t, wc.Dir(), "switch", "--quiet", "surla/build-3")
	writeFiles(t, wc.Dir(), map[string]string{"svc/dirty.go": "package svc\n"})
	build3Tip := runGit(t, repo, "rev-parse", "surla/build-3")
	runGit(t, repo, "branch", "-D", "surla/build-2")
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 2, b2, "exports")
	require.NoError(t, err)
	assert.Equal(t, Refused, outcome, "uncommitted work on a later build branch (k=3 > n=2) must be refused")
	assert.Equal(t, "surla/build-3", facts.Branch)
	assert.Equal(t, []string{"'svc/dirty.go'"}, facts.Uncommitted)
	assert.Equal(t, build3Tip, runGit(t, repo, "rev-parse", "surla/build-3"),
		"later build branch must not receive a leftover commit for an earlier build")

	runGit(t, wc.Dir(), "switch", "--quiet", "side")
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 4, b2, "billing")
	require.NoError(t, err)
	assert.Equal(t, Refused, outcome)
	assert.Equal(t, "'side'", facts.Branch)
	assert.Equal(t, []string{"'svc/dirty.go'"}, facts.Uncommitted)

	runGit(t, wc.Dir(), "switch", "--quiet", "--detach", sideTip)
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 4, b2, "billing")
	require.NoError(t, err)
	assert.Equal(t, Refused, outcome)
	assert.True(t, facts.Detached)
	assert.Equal(t, []string{"'svc/dirty.go'"}, facts.Uncommitted)
	require.NoError(t, os.Remove(filepath.Join(wc.Dir(), "svc", "dirty.go")))

	// J11: an operation in progress refuses Join both before and after the
	// build branch exists.
	runGit(t, wc.Dir(), "switch", "--quiet", "surla/build-3")
	writeFiles(t, wc.gitDir, map[string]string{"MERGE_HEAD": b1 + "\n"})
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 3, b2, "invoices")
	require.NoError(t, err)
	assert.Equal(t, Refused, outcome)
	assert.Equal(t, "merge", facts.Operation)
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 4, b2, "billing")
	require.NoError(t, err)
	assert.Equal(t, Refused, outcome)
	assert.Equal(t, "merge", facts.Operation)
	require.NoError(t, os.Remove(filepath.Join(wc.gitDir, "MERGE_HEAD")))

	// R1: a file changes after the leftover commit and before git switch -c.
	writeFiles(t, wc.Dir(), map[string]string{"svc/b.go": "package svc // leftover on build-3\n"})
	outcome, facts, err = wc.join(t.Context(), BuilderRole, 4, b2, "billing", joinHooks{
		beforeSwitch: func() error {
			writeFiles(t, wc.Dir(), map[string]string{"svc/b.go": "package svc // late edit during join\n"})
			return nil
		},
	})
	require.NoError(t, err)
	assert.Equal(t, ChangedDuringJoin, outcome)
	assert.Equal(t, "surla/build-3", facts.Branch)
	assert.Equal(t, []string{"'svc/b.go'"}, facts.Uncommitted)
	_, exists, err := lookupCommit(t.Context(), repo, "refs/heads/surla/build-4")
	require.NoError(t, err)
	assert.False(t, exists, "surla/build-4 was not created when git switch -c refused")

	// Calling Join again commits the late edit onto surla/build-3 and switches
	// to surla/build-4.
	outcome, facts, err = wc.Join(t.Context(), BuilderRole, 4, b2, "billing")
	require.NoError(t, err)
	assert.Equal(t, Joined, outcome)
	assert.Equal(t, "surla/build-4", facts.Branch)
	assert.Equal(t, "package svc // late edit during join",
		runGit(t, repo, "show", "surla/build-3:svc/b.go"))
}

// TestGitIgnoresRepositoryGrafts verifies that both readGit and gitEnv set
// GIT_GRAFT_FILE=/dev/null and GIT_TERMINAL_PROMPT=0 (dropping any inherited
// values) so an agent-written info/grafts file or inherited environment
// variable cannot rewrite commit ancestry in CheckBuildCommit, Refresh,
// copyBase, or Join, and terminal prompts stay disabled in working-copy git
// commands.
func TestGitIgnoresRepositoryGrafts(t *testing.T) {
	repo, _ := newProject(t, map[string]string{"svc/a.go": "package svc\n"})
	b0 := runGit(t, repo, "rev-parse", "HEAD")

	wc1, err := MakeCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1), b0)
	require.NoError(t, err)
	writeFiles(t, wc1.Dir(), map[string]string{"svc/refund.go": "package svc // build 1\n"})
	runGit(t, wc1.Dir(), "add", "-A")
	runGit(t, wc1.Dir(), "commit", "--quiet", "-m", "C1")
	c1 := runGit(t, wc1.Dir(), "rev-parse", "HEAD")

	outcome, _, err := wc1.Join(t.Context(), BuilderRole, 2, b0, "exports")
	require.NoError(t, err)
	require.Equal(t, Joined, outcome)
	writeFiles(t, wc1.Dir(), map[string]string{"svc/export.go": "package svc // build 2\n"})
	runGit(t, wc1.Dir(), "add", "-A")
	runGit(t, wc1.Dir(), "commit", "--quiet", "-m", "C2")
	c2 := runGit(t, wc1.Dir(), "rev-parse", "HEAD")

	// Merge C1 into surla/build-2 at X.
	runGit(t, wc1.Dir(), "merge", "--quiet", "--no-ff", "-m", "merge C1", c1)
	x := runGit(t, wc1.Dir(), "rev-parse", "HEAD")

	// Also open the legacy test writer working copy on surla/tests and commit
	// unmerged T1 on it so Refresh and copyBase exercise gitEnv's merge-base
	// without going through readGit.
	tw, err := OpenWorkingCopy(t.Context(), repo)
	require.NoError(t, err)
	writeFiles(t, tw.Dir(), map[string]string{"eval/a_test.go": "package eval\n"})
	runGit(t, tw.Dir(), "add", "-A")
	runGit(t, tw.Dir(), "commit", "--quiet", "-m", "T1")
	t1 := runGit(t, tw.Dir(), "rev-parse", "HEAD")

	// Write info/grafts claiming X's only parent is C2 (dropping C1) and
	// claiming C1 and T1 are ancestors of B0, and point GIT_GRAFT_FILE and
	// GIT_TERMINAL_PROMPT in the process environment at non-default values.
	graftsPath := runGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "info/grafts")
	require.NoError(t, os.WriteFile(graftsPath, []byte(x+" "+c2+"\n"+b0+" "+c1+" "+t1+"\n"), 0o644))
	t.Setenv("GIT_GRAFT_FILE", graftsPath)
	t.Setenv("GIT_TERMINAL_PROMPT", "1")

	// 1. Refresh uses gitEnv (not readGit): if gitEnv did not set
	// GIT_GRAFT_FILE=/dev/null, merge-base --is-ancestor t1 b0 would succeed
	// via the graft and Refresh would report RefreshAtProjectCommit instead of
	// RefreshNotContained.
	refreshed, err := tw.Refresh(t.Context())
	require.NoError(t, err)
	assert.Equal(t, RefreshResult{State: RefreshNotContained, Commit: t1}, refreshed,
		"Refresh must ignore info/grafts and inherited GIT_GRAFT_FILE via gitEnv")

	// 2. Configure a required git clean filter in the repository that succeeds
	// only when git runs with GIT_TERMINAL_PROMPT=0 and
	// GIT_GRAFT_FILE=/dev/null, then run Join (which stages leftovers via
	// gitEnv, not readGit).
	runGit(t, repo, "config", "filter.envcheck.clean",
		`test "$GIT_TERMINAL_PROMPT" = 0 && test "$GIT_GRAFT_FILE" = /dev/null && cat`)
	runGit(t, repo, "config", "filter.envcheck.required", "true")
	writeFiles(t, wc1.Dir(), map[string]string{
		".gitattributes": "*.go filter=envcheck\n",
		"svc/env.go":     "package svc // staged through filter\n",
	})
	outcome, _, err = wc1.Join(t.Context(), BuilderRole, 3, b0, "billing")
	require.NoError(t, err, "Join's gitEnv must pass GIT_TERMINAL_PROMPT=0 and GIT_GRAFT_FILE=/dev/null")
	assert.Equal(t, Joined, outcome)

	// 3. CheckBuildCommit (readGit) also ignores info/grafts and GIT_GRAFT_FILE.
	check, err := CheckBuildCommit(
		t.Context(),
		repo,
		x,
		"surla/build-2",
		b0,
		[2]string{"surla/build-2", "surla/tests-2"},
		nil,
	)
	require.NoError(t, err)
	assert.Equal(t, CommitCheck{
		OnBranch: true,
		FromBase: true,
		IsBase:   false,
		Foreign: []ForeignWork{
			{
				MergeBase: c1,
				Branch:    "surla/build-1",
				Files:     []string{"svc/refund.go"},
				FileCount: 1,
			},
		},
	}, check, "rule 13 must ignore info/grafts and inherited GIT_GRAFT_FILE")
}

// runGitStdin runs git in dir with stdin fed from input.
func runGitStdin(t *testing.T, dir, input string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir, "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
}
