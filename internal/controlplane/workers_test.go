// This file tests worker copy turns, joins on assignment reads, builder and
// test-writer working-copy refusals, and Go workspace detection in .surla-build.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/scaffold"
	"github.com/goadesign/surla/internal/shquote"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAssignmentReadJoinsCopyToBuild verifies that GetBuilderAssignment and
// GetTestWriterAssignment join their respective working copies (.surla-build
// and .surla-tests) to a new build, saving uncommitted work left on an earlier
// build branch, moving a clean non-build branch or detached HEAD without a save
// commit, and leaving uncommitted work in place when already on the build's
// branch.
func TestAssignmentReadJoinsCopyToBuild(t *testing.T) {
	repo, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)

	// Start build 1 for change "c1"; StartBuild creates both copies on
	// surla/build-1 and surla/tests-1.
	c1 := buildableChange(t, cp, "c1")
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	require.NoError(t, err)

	var prepCount int
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{
			folder: filepath.Join(project, testwriter.TestsRole.Folder),
			prepare: func(context.Context, []designeval.StorySummary) error {
				prepCount++
				return nil
			},
		}, nil
	}

	builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
	testsDir := filepath.Join(project, testwriter.TestsRole.Folder)
	require.NoError(t, os.WriteFile(filepath.Join(builderDir, "draft.go"), []byte("package shop\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "draft_test.go"), []byte("package eval\n"), 0o600))

	// First reads on build 1 find both copies already on surla/<role>-1:
	// uncommitted files remain in place, Read becomes true, and Prepare runs
	// once on the test writer's marking read.
	bAssign1, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	assert.True(t, bAssign1.Read)
	assert.Equal(t, builderDir, bAssign1.WorkingFolder)
	assert.FileExists(t, filepath.Join(builderDir, "draft.go"))

	twAssign1, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	assert.True(t, twAssign1.Progress.Read)
	assert.Equal(t, testsDir, twAssign1.WorkingFolder)
	assert.Equal(t, 1, prepCount, "marking read prepares gen/")
	assert.FileExists(t, filepath.Join(testsDir, "eval", "draft_test.go"))

	// Re-reading when Read is already true does not re-run Prepare.
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	assert.Equal(t, 1, prepCount, "non-marking read does not re-run Prepare")

	// Drop build 1 and start build 2 for change "c2" after a new project commit.
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: first.Build.StartedAt})
	require.NoError(t, err)
	writeWorkspaceFiles(t, project, map[string]string{"v2.txt": "v2\n"})
	runGitForTest(t, repo, "add", "v2.txt")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "v2 base")
	b2 := productionBuildGit(t, repo, "rev-parse", "HEAD")

	c2 := buildableChange(t, cp, "c2")
	provenance, found, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	require.True(t, found)
	record := loadMetadataRecord(t, cp, "c2")
	record.madeFrom.Start = provenance.Start
	record.madeFrom.Entries = nil
	saveMetadataRecord(t, cp, record)

	cp.buildCopyFactory = nil
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c2", RequestRevision: c2.RequestRevision,
	})
	require.NoError(t, err)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{
			folder: testsDir,
			prepare: func(context.Context, []designeval.StorySummary) error {
				prepCount++
				return nil
			},
		}, nil
	}

	// StartBuild left both existing copies on surla/<role>-1 with their
	// uncommitted files. Reading the assignments for "c2" saves the dirty work
	// on surla/<role>-1 and switches the copies to surla/<role>-2 at b2.
	assert.Equal(t, "surla/build-1", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, "surla/tests-1", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))

	bAssign2, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, bAssign2.Read)
	assert.Equal(t, "surla/build-2", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, b2, productionBuildGit(t, builderDir, "rev-parse", "HEAD"))
	assert.Equal(t, "Left uncommitted when change c2 started",
		productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/build-1"))

	twAssign2, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, twAssign2.Progress.Read)
	assert.Equal(t, 2, prepCount, "marking read of build 2 prepares gen/")
	assert.Equal(t, "surla/tests-2", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, b2, productionBuildGit(t, testsDir, "rev-parse", "HEAD"))
	assert.Equal(t, "Left uncommitted when change c2 started",
		productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/tests-1"))
}

// TestAssignmentReadRefusesBusyAndDroppedBuilds verifies nonblocking copy-turn
// refusals, concurrent-modification refusals during Join, and dropped-build
// refusals before and after Join.
func TestAssignmentReadRefusesBusyAndDroppedBuilds(t *testing.T) {
	_, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)

	c1 := buildableChange(t, cp, "c1")
	started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	require.NoError(t, err)

	// Holding builderCopyTurn refuses GetBuilderAssignment without waiting.
	cp.builderCopyTurn <- struct{}{}
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	<-cp.builderCopyTurn
	assertRefused(t, err, "working_copy_busy")
	assert.EqualError(t, err, "Surla is starting a build in your working folder; call get_builder_assignment again.")

	// Holding workingCopyTurn refuses GetTestWriterAssignment without waiting.
	cp.workingCopyTurn <- struct{}{}
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	<-cp.workingCopyTurn
	assertRefused(t, err, "working_copy_busy")
	assert.EqualError(t, err, "Surla is checking the tests in your working folder; call get_test_writer_assignment again when the check has ended.")

	// Dropping the build refuses GetBuilderAssignment, GetTestWriterAssignment,
	// ReportTestsFinished, and builderCopyReady with no_build.
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: started.Build.StartedAt})
	require.NoError(t, err)
	const wantDropMsg = "The user dropped change c1: stop work on it; make no more edits or commits for it. The user starts builds on the views page."

	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	assertRefused(t, err, "no_build")
	assert.EqualError(t, err, wantDropMsg)

	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	assertRefused(t, err, "no_build")
	assert.EqualError(t, err, wantDropMsg)

	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	assertRefused(t, err, "no_build")
	assert.EqualError(t, err, wantDropMsg)

	_, err = cp.builderCopyReady(builder, project, "verify_change")
	assertRefused(t, err, "no_build")
	assert.EqualError(t, err, wantDropMsg)
}

// TestWorkerRefusalsCoverUnreadMissingAndOffBranch verifies no_build when no
// build is open, assignment_unread before a copy joins or reads the build,
// working_folder_missing when a copy folder is absent, and copy_off_branch
// before and after a join for both roles.
func TestWorkerRefusalsCoverUnreadMissingAndOffBranch(t *testing.T) {
	repo, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)

	// 1. No open build -> builderCopyReady refuses with no_build.
	_, err := cp.builderCopyReady(builder, project, "generate_code")
	assertRefused(t, err, "no_build")
	assert.EqualError(t, err, "No build is open: the user starts builds on the views page.")

	// Start build 1 for "c1".
	c1 := buildableChange(t, cp, "c1")
	started1, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	require.NoError(t, err)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: filepath.Join(project, testwriter.TestsRole.Folder)}, nil
	}

	// 2. Before reading assignments -> builderCopyReady and ReportTestsFinished
	// refuse with assignment_unread.
	_, err = cp.builderCopyReady(builder, project, "generate_code")
	assertRefused(t, err, "assignment_unread")
	assert.EqualError(t, err, "Change c1's build started after your last read: stop your current work, then call get_builder_assignment for c1.")

	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	assertRefused(t, err, "assignment_unread")
	assert.EqualError(t, err, "Change c1's build started after your last read: stop your current work, then call get_test_writer_assignment for c1.")

	// 3. After reading assignments, builderCopyReady succeeds on surla/build-1
	// and copy_off_branch refuses when moved off-branch (BuildBranch != nil).
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil
	builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
	testsDir := filepath.Join(project, testwriter.TestsRole.Folder)

	ready, err := cp.builderCopyReady(builder, project, "verify_change")
	require.NoError(t, err)
	assert.Equal(t, "c1", ready.build.file.ChangeID)
	assert.Equal(t, repo, ready.repo)
	assert.Equal(t, builderDir, ready.copyRoot)
	assert.Equal(t, builderDir, ready.folder)
	assert.True(t, ready.goworkOff)

	// An index.lock while .surla-tests is on its build branch with no operation
	// in progress refuses ReportTestsFinished with the lock step, writes nothing,
	// and records no verdict.
	testsGitDir := productionBuildGit(t, testsDir, "rev-parse", "--path-format=absolute", "--git-dir")
	lockFile := filepath.Join(testsGitDir, "index.lock")
	require.NoError(t, os.WriteFile(lockFile, nil, 0o600))
	beforeLockBuild := readBuildFile(t, cp, "c1")
	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	var lockRefusal *gensurla.CopyOffBranch
	require.ErrorAs(t, err, &lockRefusal)
	require.NotNil(t, lockRefusal.Branch)
	assert.Equal(t, "surla/tests-1", *lockRefusal.Branch)
	require.NotNil(t, lockRefusal.BuildBranch)
	assert.Equal(t, "surla/tests-1", *lockRefusal.BuildBranch)
	assert.Equal(t, "none", lockRefusal.Operation)
	assert.Contains(t, lockRefusal.Message, "Your working copy is on surla/tests-1 for change c1. Git's index lock (")
	assert.Contains(t, lockRefusal.Message, "index.lock') exists in your working copy: wait for the running git command to finish, or remove ")
	assert.Equal(t, beforeLockBuild, readBuildFile(t, cp, "c1"), "on-branch index.lock refuses report without writing or recording a verdict")
	require.NoError(t, os.Remove(lockFile))

	// Switch builder copy to side branch 'experiment' with 2 uncommitted files.
	runGitForTest(t, builderDir, "switch", "--quiet", "-c", "experiment")
	require.NoError(t, os.WriteFile(filepath.Join(builderDir, "a.go"), []byte("package a\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(builderDir, "b.go"), []byte("package b\n"), 0o600))

	_, err = cp.builderCopyReady(builder, project, "verify_change")
	var offBranch *gensurla.CopyOffBranch
	require.ErrorAs(t, err, &offBranch)
	assert.Equal(t, "copy_off_branch", offBranch.Name)
	require.NotNil(t, offBranch.Branch)
	assert.Equal(t, "experiment", *offBranch.Branch)
	require.NotNil(t, offBranch.BuildBranch)
	assert.Equal(t, "surla/build-1", *offBranch.BuildBranch)
	assert.Equal(t, "none", offBranch.Operation)
	assert.Equal(t, []string{"a.go", "b.go"}, offBranch.Uncommitted)
	assert.Equal(t, 2, offBranch.UncommittedCount)
	assert.Equal(t,
		"Your working copy is on branch 'experiment'; change c1 is built on surla/build-1. Uncommitted (2): a.go, b.go. Put your work on surla/build-1, commit it, and call verify_change again.",
		offBranch.Message,
	)

	// Switch tests copy to detached HEAD with 1 uncommitted file; ReportTestsFinished
	// refuses with CopyOffBranch where Branch is nil and BuildBranch is "surla/tests-1".
	runGitForTest(t, testsDir, "switch", "--quiet", "--detach", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "t_test.go"), []byte("package eval\n"), 0o600))

	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	offBranch = nil
	require.ErrorAs(t, err, &offBranch)
	assert.Nil(t, offBranch.Branch, "detached HEAD has no Branch")
	require.NotNil(t, offBranch.BuildBranch)
	assert.Equal(t, "surla/tests-1", *offBranch.BuildBranch)
	assert.Equal(t, []string{"eval/t_test.go"}, offBranch.Uncommitted)
	assert.Equal(t, 1, offBranch.UncommittedCount)
	assert.Equal(t,
		"Your working copy has a detached HEAD (not on any branch); change c1 is built on surla/tests-1. Uncommitted (1): eval/t_test.go. Put your test files on surla/tests-1, uncommitted (Surla commits what it accepts), and call report_tests_finished again.",
		offBranch.Message,
	)

	// 4. Before a join on build 2 (BuildBranch == nil): drop build 1, start build 2
	// for "c2". Both copies are off earlier build branches with uncommitted files
	// ('experiment' for builder, detached HEAD for test writer), so Join refuses
	// with BuildBranch == nil.
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: started1.Build.StartedAt})
	require.NoError(t, err)
	c2 := buildableChange(t, cp, "c2")
	provenance, found, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	require.True(t, found)
	record := loadMetadataRecord(t, cp, "c2")
	record.madeFrom.Start = provenance.Start
	record.madeFrom.Entries = nil
	saveMetadataRecord(t, cp, record)
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c2", RequestRevision: c2.RequestRevision,
	})
	require.NoError(t, err)

	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	offBranch = nil
	require.ErrorAs(t, err, &offBranch)
	require.NotNil(t, offBranch.Branch)
	assert.Equal(t, "experiment", *offBranch.Branch)
	assert.Nil(t, offBranch.BuildBranch, "BuildBranch is absent before the copy joined the build")
	assert.Equal(t,
		"Your working copy is on branch 'experiment'. Uncommitted (2): a.go, b.go. Surla puts it on change c2's new branch surla/build-2 only when nothing is uncommitted, or when it is on an earlier surla/build branch, where Surla commits what is left. Commit your work on a branch of your own, not one of the user's, then call get_builder_assignment again.",
		offBranch.Message,
	)

	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	offBranch = nil
	require.ErrorAs(t, err, &offBranch)
	assert.Nil(t, offBranch.Branch)
	assert.Nil(t, offBranch.BuildBranch, "BuildBranch is absent before the copy joined the build")
	assert.Equal(t,
		"Your working copy has a detached HEAD (not on any branch). Uncommitted (1): eval/t_test.go. Surla puts it on change c2's new branch surla/tests-2 only when nothing is uncommitted, or when it is on an earlier surla/tests branch, where Surla commits what is left. Return your copy to an earlier surla/tests branch with your files, where Surla commits them, or remove them; then call get_test_writer_assignment again.",
		offBranch.Message,
	)

	// 5. Missing copy folders -> working_folder_missing.
	require.NoError(t, os.RemoveAll(builderDir))
	require.NoError(t, os.RemoveAll(testsDir))

	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	assertRefused(t, err, "working_folder_missing")
	assert.EqualError(t, err, "The builder's folder "+shquote.Quote(builderDir)+" is missing: ask the user to build this change again on the views page.")

	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	assertRefused(t, err, "working_folder_missing")
	assert.EqualError(t, err, "The test writer's folder "+shquote.Quote(testsDir)+" is missing: ask the user to build this change again on the views page.")

	// 6. copyOffBranchRefusal covers in-progress Git operations, index.lock,
	// truncated uncommitted lists, and unprintable branch names.
	builderCherryPick := copyOffBranchRefusal(
		testwriter.BuilderRole, "c2", 2, true, testwriter.CopyFacts{
			Branch:           "surla/build-2",
			Operation:        "cherry_pick",
			IndexLock:        ".surla-build/.git/index.lock",
			Uncommitted:      []string{"a.go", "b.go"},
			UncommittedCount: 5,
		}, "verify_change",
	)
	assert.Equal(t,
		"Your working copy is on surla/build-2 for change c2. A cherry-pick is in progress in your working copy: finish or abort it. Git's index lock ('.surla-build/.git/index.lock') exists in your working copy: wait for the running git command to finish, or remove '.surla-build/.git/index.lock' if no git process is running. Uncommitted (5, first 2): a.go, b.go. Put your work on surla/build-2, commit it, and call verify_change again.",
		builderCherryPick.Message,
	)

	testsUnprintable := copyOffBranchRefusal(
		testwriter.TestsRole, "c2", 2, false, testwriter.CopyFacts{
			Branch:           "a name that is not shown",
			Operation:        "merge",
			Uncommitted:      nil,
			UncommittedCount: 0,
		}, "get_test_writer_assignment",
	)
	assert.Equal(t,
		"Your working copy is on branch a name that is not shown. A merge is in progress in your working copy: abort it. Uncommitted: none. Surla puts it on change c2's new branch surla/tests-2 only when nothing is uncommitted, or when it is on an earlier surla/tests branch, where Surla commits what is left. Return your copy to an earlier surla/tests branch with your files, where Surla commits them, or remove them; then call get_test_writer_assignment again.",
		testsUnprintable.Message,
	)
}

// TestBuilderWorkingFolderIsolatesOuterGoWork verifies that an untracked
// go.work in the outer project folder does not enter .surla-build and keeps
// gowork_off true, whereas a committed go.work inside the repository enters
// .surla-build and sets gowork_off false.
func TestBuilderWorkingFolderIsolatesOuterGoWork(t *testing.T) {
	t.Run("untracked outer go.work stays outside .surla-build", func(t *testing.T) {
		_, project, cp, _ := reviewProject(t, Options{})
		require.NoError(t, os.WriteFile(filepath.Join(project, "go.work"), []byte("go 1.27.3\nuse .\n"), 0o600))
		card := buildableChange(t, cp, "chg")
		_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
			ChangeID: "chg", RequestRevision: card.RequestRevision,
		})
		require.NoError(t, err)

		builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
		assert.NoFileExists(t, filepath.Join(builderDir, "go.work"))

		assign, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder), &gensurla.GetBuilderAssignmentPayload{ChangeID: "chg"})
		require.NoError(t, err)
		assert.Equal(t, builderDir, assign.WorkingFolder)
		assert.True(t, assign.GoworkOff, "untracked outer go.work must not disable gowork_off in .surla-build")
	})

	t.Run("committed go.work enters .surla-build", func(t *testing.T) {
		repo, project, cp, _ := reviewProject(t, Options{})
		require.NoError(t, os.WriteFile(filepath.Join(project, "go.work"), []byte("go 1.27.3\nuse .\n"), 0o600))
		runGitForTest(t, repo, "add", "go.work")
		runGitForTest(t, repo, "commit", "--quiet", "-m", "Commit go.work")

		card := buildableChange(t, cp, "chg")
		_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
			ChangeID: "chg", RequestRevision: card.RequestRevision,
		})
		require.NoError(t, err)

		builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
		assert.FileExists(t, filepath.Join(builderDir, "go.work"))

		assign, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder), &gensurla.GetBuilderAssignmentPayload{ChangeID: "chg"})
		require.NoError(t, err)
		assert.Equal(t, builderDir, assign.WorkingFolder)
		assert.False(t, assign.GoworkOff, "committed go.work inside .surla-build sets gowork_off false")
	})
}

// TestAssignmentReadSkipsUnreadBuildsN1AndN2 verifies N1 (build 1 read, Build X
// dropped before any read, Build Y numbered 3 where both reads join from
// surla/*-1) and N2 (the same when only the test writer did not read the
// intermediate build).
func TestAssignmentReadSkipsUnreadBuildsN1AndN2(t *testing.T) {
	repo, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)

	builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
	testsDir := filepath.Join(project, testwriter.TestsRole.Folder)

	// Start build 1 ("c1") and read both assignments so both copies are on
	// surla/build-1 and surla/tests-1.
	c1 := buildableChange(t, cp, "c1")
	b1Res, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	require.NoError(t, err)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: testsDir}, nil
	}
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil

	require.NoError(t, os.WriteFile(filepath.Join(builderDir, "n1.go"), []byte("package shop // n1\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "n1_test.go"), []byte("package eval // n1\n"), 0o600))

	// N1: Drop build 1, start Build X ("cX", build 2), and drop Build X before
	// either worker reads it.
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: b1Res.Build.StartedAt})
	require.NoError(t, err)
	cX := freshBuildableAfterDrop(t, cp, "cX")
	bXRes, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "cX", RequestRevision: cX.RequestRevision,
	})
	require.NoError(t, err)
	assert.Equal(t, 2, mustOpenBuildFile(t, cp).Number)
	assert.False(t, copyBranchExists(t.Context(), repo, "surla/build-2"))
	assert.False(t, copyBranchExists(t.Context(), repo, "surla/tests-2"))

	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "cX", StartedAt: bXRes.Build.StartedAt})
	require.NoError(t, err)

	// Start Build Y ("cY"): its build number is 3, and before either read a
	// builder tool and a test-writer report are refused with assignment_unread
	// while the copies stay unmoved on surla/*-1.
	cY := freshBuildableAfterDrop(t, cp, "cY")
	bYRes, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "cY", RequestRevision: cY.RequestRevision,
	})
	require.NoError(t, err)
	assert.Equal(t, 3, mustOpenBuildFile(t, cp).Number)

	_, err = cp.builderCopyReady(builder, project, "verify_change")
	assertRefused(t, err, "assignment_unread")
	assert.Equal(t, "surla/build-1", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))

	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{
		ChangeID: "cY", RequestRevision: cY.RequestRevision,
	})
	assertRefused(t, err, "assignment_unread")
	assert.Equal(t, "surla/tests-1", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))

	// Both reads of Build Y join directly from surla/*-1 to surla/*-3.
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: testsDir}, nil
	}
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "cY"})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "cY"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil

	assert.Equal(t, "surla/build-3", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, "surla/tests-3", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	assert.False(t, copyBranchExists(t.Context(), repo, "surla/build-2"))
	assert.False(t, copyBranchExists(t.Context(), repo, "surla/tests-2"))
	assert.Equal(t, "Left uncommitted when change cY started",
		productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/build-1"))
	assert.Equal(t, "Left uncommitted when change cY started",
		productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/tests-1"))

	// N2: Drop Build Y (3), start Build X2 ("cX2", build 4), and have ONLY the
	// builder read cX2 while the test writer never reads cX2.
	require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "n2_test.go"), []byte("package eval // n2\n"), 0o600))
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "cY", StartedAt: bYRes.Build.StartedAt})
	require.NoError(t, err)
	cX2 := freshBuildableAfterDrop(t, cp, "cX2")
	bX2Res, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "cX2", RequestRevision: cX2.RequestRevision,
	})
	require.NoError(t, err)
	assert.Equal(t, 4, mustOpenBuildFile(t, cp).Number)

	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "cX2"})
	require.NoError(t, err)
	assert.Equal(t, "surla/build-4", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, "surla/tests-3", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	require.NoError(t, os.WriteFile(filepath.Join(builderDir, "n2.go"), []byte("package shop // n2\n"), 0o600))

	// Drop Build X2 (4) and start Build Y2 ("cY2", build 5): the builder joins
	// from surla/build-4 to surla/build-5 while the test writer skips
	// surla/tests-4 and joins from surla/tests-3 to surla/tests-5.
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "cX2", StartedAt: bX2Res.Build.StartedAt})
	require.NoError(t, err)
	cY2 := freshBuildableAfterDrop(t, cp, "cY2")
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "cY2", RequestRevision: cY2.RequestRevision,
	})
	require.NoError(t, err)
	assert.Equal(t, 5, mustOpenBuildFile(t, cp).Number)

	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: testsDir}, nil
	}
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "cY2"})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "cY2"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil

	assert.Equal(t, "surla/build-5", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, "surla/tests-5", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	assert.False(t, copyBranchExists(t.Context(), repo, "surla/tests-4"))
	assert.Equal(t, "Left uncommitted when change cY2 started",
		productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/build-4"))
	assert.Equal(t, "Left uncommitted when change cY2 started",
		productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/tests-3"))
}

// TestBuilderReadJoinsDuringTestsCheckWhileTestWriterReadWaits verifies that
// while a tests check holds workingCopyTurn, GetBuilderAssignment joins
// .surla-build under builderCopyTurn without waiting, GetTestWriterAssignment
// is refused with working_copy_busy, and the next GetTestWriterAssignment after
// the check finishes joins .surla-tests.
func TestBuilderReadJoinsDuringTestsCheckWhileTestWriterReadWaits(t *testing.T) {
	_, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)

	builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
	testsDir := filepath.Join(project, testwriter.TestsRole.Folder)

	c1 := buildableChange(t, cp, "c1")
	started1, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	require.NoError(t, err)

	inCheck := make(chan struct{})
	releaseCheck := make(chan struct{})
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{
			folder: testsDir,
			check: func(context.Context, []designeval.StorySummary) (testwriter.Verdict, error) {
				close(inCheck)
				<-releaseCheck
				return testwriter.Verdict{}, nil
			},
			record: func(context.Context, testwriter.Verdict, string) (string, error) {
				return strings.Repeat("a", 40), nil
			},
		}, nil
	}

	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)

	// Drop build 1 and start build 2 ("c2") while both copies are still on
	// surla/build-1 and surla/tests-1.
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: started1.Build.StartedAt})
	require.NoError(t, err)
	c2 := freshBuildableAfterDrop(t, cp, "c2")
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c2", RequestRevision: c2.RequestRevision,
	})
	require.NoError(t, err)

	// Put the test writer's copy on surla/tests-1 and hold workingCopyTurn as a
	// running tests check does.
	go func() {
		cp.workingCopyTurn <- struct{}{}
		close(inCheck)
		<-releaseCheck
		<-cp.workingCopyTurn
	}()
	<-inCheck

	// During the tests check, the builder's read joins .surla-build from
	// surla/build-1 to surla/build-2, while the test writer's read is refused
	// with working_copy_busy and leaves .surla-tests on surla/tests-1.
	bAssign, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, bAssign.Read)
	assert.Equal(t, "surla/build-2", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))

	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	assertRefused(t, err, "working_copy_busy")
	assert.EqualError(t, err, "Surla is checking the tests in your working folder; call get_test_writer_assignment again when the check has ended.")
	assert.Equal(t, "surla/tests-1", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))

	// Once the tests check releases workingCopyTurn, the next test-writer read
	// joins .surla-tests to surla/tests-2.
	close(releaseCheck)
	cp.workingCopyTurn <- struct{}{}
	<-cp.workingCopyTurn

	twAssign, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, twAssign.Progress.Read)
	assert.Equal(t, "surla/tests-2", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
}

// TestAssignmentReadEditedDuringJoinAndStoppedBeforeOrAfterSwitch verifies:
//   - a file edited during the read (switch refused, no branch created, read
//     refused with working_copy_busy, and the next read commits the edit on the
//     earlier branch and joins);
//   - a join stopped before the switch (inside git switch -c after unpacking
//     base) and after the switch (after surla/*-<n> is checked out, before
//     Read is saved);
//   - a clean copy on a side branch or detached HEAD joining;
//   - typed CopyOffBranch facts over HTTP (409 Conflict).
func TestAssignmentReadEditedDuringJoinAndStoppedBeforeOrAfterSwitch(t *testing.T) {
	repo, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)

	builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
	testsDir := filepath.Join(project, testwriter.TestsRole.Folder)

	// Start build 1 ("c1") after committing tracked a_conflict.go in b1.
	writeWorkspaceFiles(t, project, map[string]string{"a_conflict.go": "package main // base b1\n"})
	runGitForTest(t, repo, "add", "a_conflict.go")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "B1 with a_conflict.go")

	c1 := buildableChange(t, cp, "c1")
	b1Res, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c1", RequestRevision: c1.RequestRevision,
	})
	require.NoError(t, err)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: testsDir}, nil
	}
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil

	// Advance main to b2 modifying tracked a_conflict.go ("base b2\n").
	writeWorkspaceFiles(t, project, map[string]string{"a_conflict.go": "package main // base b2\n"})
	runGitForTest(t, repo, "add", "a_conflict.go")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "B2")
	b2 := productionBuildGit(t, repo, "rev-parse", "HEAD")

	// Drop build 1 and start build 2 ("c2") at b2.
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: b1Res.Build.StartedAt})
	require.NoError(t, err)
	c2 := freshBuildableAfterDrop(t, cp, "c2")
	b2Res, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c2", RequestRevision: c2.RequestRevision,
	})
	require.NoError(t, err)

	// 1. File edited during the read: in git add --all, tracked files
	// (a_conflict.go) are hashed into the index before untracked files
	// (z_trigger.txt) are added. Appending to tracked a_conflict.go when
	// untracked z_trigger.txt runs its clean filter leaves a_conflict.go dirty
	// in the working tree after the leftover commit updates the index, so
	// git switch -c refuses without creating surla/build-2.
	enableFlag := filepath.Join(t.TempDir(), "edit-during-join")
	require.NoError(t, os.WriteFile(enableFlag, []byte("1"), 0o600))
	filterCmd := "if test -f " + shquote.Quote(enableFlag) + "; then printf '// edited during read\\n' >> " +
		shquote.Quote(filepath.Join(builderDir, "a_conflict.go")) + "; fi; cat"
	runGitForTest(t, repo, "config", "filter.lateedit.clean", filterCmd)
	require.NoError(t, os.WriteFile(filepath.Join(builderDir, "a_conflict.go"), []byte("package main // leftover\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(builderDir, ".gitattributes"), []byte("z_trigger.txt filter=lateedit\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(builderDir, "z_trigger.txt"), []byte("trigger\n"), 0o600))

	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	assertRefused(t, err, "working_copy_busy")
	assert.EqualError(t, err, "Files in your working folder changed while Surla moved it to change c2; nothing was lost. Call get_builder_assignment again.")
	assert.False(t, copyBranchExists(t.Context(), repo, "surla/build-2"), "switch refused without creating surla/build-2")
	assert.Equal(t, "surla/build-1", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.False(t, mustOpenBuildFile(t, cp).Builder.Read, "read remains false when switch is refused")

	// Once the concurrent writer stops editing, the next read commits the final
	// a_conflict.go onto surla/build-1 and joins surla/build-2.
	require.NoError(t, os.Remove(enableFlag))
	bAssign2, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, bAssign2.Read)
	assert.Equal(t, "surla/build-2", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, b2, productionBuildGit(t, builderDir, "rev-parse", "HEAD"))
	assert.Contains(t, productionBuildGit(t, repo, "show", "surla/build-1:a_conflict.go"), "// edited during read")

	// Clean detached HEAD in .surla-tests joins surla/tests-2 without a leftover commit.
	surlaTests1Tip := productionBuildGit(t, repo, "rev-parse", "surla/tests-1")
	runGitForTest(t, testsDir, "switch", "--quiet", "--detach", surlaTests1Tip)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: testsDir}, nil
	}
	twAssign2, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil
	assert.True(t, twAssign2.Progress.Read)
	assert.Equal(t, "surla/tests-2", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, surlaTests1Tip, productionBuildGit(t, repo, "rev-parse", "surla/tests-1"),
		"clean detached HEAD makes no leftover commit")

	// 2. Join stopped before and after the switch on build 3 ("c3"):
	// Commit C2 on surla/build-2, advance main to b3, drop build 2, start build 3.
	writeWorkspaceFiles(t, builderDir, map[string]string{"c2.go": "package main // C2\n"})
	runGitForTest(t, builderDir, "add", "c2.go")
	runGitForTest(t, builderDir, "commit", "--quiet", "-m", "C2")
	c2Tip := productionBuildGit(t, builderDir, "rev-parse", "HEAD")

	writeWorkspaceFiles(t, project, map[string]string{"b3.go": "package main // B3\n"})
	runGitForTest(t, repo, "add", "b3.go")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "B3")
	b3 := productionBuildGit(t, repo, "rev-parse", "HEAD")

	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c2", StartedAt: b2Res.Build.StartedAt})
	require.NoError(t, err)
	c3 := freshBuildableAfterDrop(t, cp, "c3")
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: "c3", RequestRevision: c3.RequestRevision,
	})
	require.NoError(t, err)

	// Simulate a join stopped inside git switch -c after unpacking b3 into
	// .surla-build while HEAD is still refs/heads/surla/build-2 at C2.
	runGitForTest(t, builderDir, "read-tree", "-u", "--reset", b3)
	require.Equal(t, "surla/build-2", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))

	bAssign3, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c3"})
	require.NoError(t, err)
	assert.True(t, bAssign3.Read)
	assert.Equal(t, "surla/build-3", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, b3, productionBuildGit(t, builderDir, "rev-parse", "HEAD"))
	assert.Equal(t, c2Tip, productionBuildGit(t, repo, "rev-parse", "surla/build-2"),
		"stopped switch recovery must not commit b3's tree onto surla/build-2")

	// Simulate a join of .surla-tests stopped AFTER switching onto surla/tests-3
	// and BEFORE saving TestWriter.Read = true: ReportTestsFinished still
	// refuses with assignment_unread, and the next GetTestWriterAssignment
	// finds surla/tests-3 AlreadyJoined, prepares gen/, and marks Read = true.
	runGitForTest(t, testsDir, "switch", "--quiet", "-c", "surla/tests-3", b3)
	require.False(t, mustOpenBuildFile(t, cp).TestWriter.Read)

	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{
		ChangeID: "c3", RequestRevision: c3.RequestRevision,
	})
	assertRefused(t, err, "assignment_unread")

	var preparedOnRetry bool
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{
			folder: testsDir,
			prepare: func(context.Context, []designeval.StorySummary) error {
				preparedOnRetry = true
				return nil
			},
		}, nil
	}
	twAssign3, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c3"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil
	assert.True(t, twAssign3.Progress.Read)
	assert.True(t, preparedOnRetry, "retry after a post-switch stop still runs Prepare on the marking read")

	// 3. Typed CopyOffBranch over HTTP (409 Conflict with structured JSON fields).
	runGitForTest(t, testsDir, "switch", "--quiet", "-c", "agent-tests")
	require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "http_test.go"), []byte("package eval\n"), 0o600))

	handler := NewHTTPHandler(cp, func(*http.Request) (Principal, error) {
		return Principal{ID: "alice@example.com", Kind: CallerTestWriter}, nil
	})
	req := httptest.NewRequest(
		http.MethodPost,
		"/assignments/test-writer/c3/finished",
		strings.NewReader(fmt.Sprintf(`{"request_revision":%q}`, c3.RequestRevision)),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusConflict, rec.Code)

	var body struct {
		Name             string   `json:"name"`
		Message          string   `json:"message"`
		Branch           *string  `json:"branch"`
		BuildBranch      *string  `json:"build_branch"`
		Operation        string   `json:"operation"`
		Uncommitted      []string `json:"uncommitted"`
		UncommittedCount int      `json:"uncommitted_count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "copy_off_branch", body.Name)
	require.NotNil(t, body.Branch)
	assert.Equal(t, "agent-tests", *body.Branch)
	require.NotNil(t, body.BuildBranch)
	assert.Equal(t, "surla/tests-3", *body.BuildBranch)
	assert.Equal(t, "none", body.Operation)
	assert.Equal(t, []string{"eval/http_test.go"}, body.Uncommitted)
	assert.Equal(t, 1, body.UncommittedCount)
	assert.Contains(t, body.Message, "Your working copy is on branch 'agent-tests'; change c3 is built on surla/tests-3.")
}

// freshBuildableAfterDrop creates a buildable change card whose madeFrom.Start
// matches the current project provenance after a drop.
func freshBuildableAfterDrop(t *testing.T, cp *ControlPlane, changeID string) *gensurla.ChangeCardResult {
	t.Helper()
	card := buildableChange(t, cp, changeID)
	provenance, found, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	if found {
		record := loadMetadataRecord(t, cp, changeID)
		record.madeFrom.Start = provenance.Start
		record.madeFrom.Entries = nil
		saveMetadataRecord(t, cp, record)
	}
	return card
}

// mustOpenBuildFile loads the current open build file from cp.cards.
func mustOpenBuildFile(t *testing.T, cp *ControlPlane) buildFile {
	t.Helper()
	cp.mu.RLock()
	defer cp.mu.RUnlock()
	saved, found, err := cp.cards.loadOpenBuild()
	require.NoError(t, err)
	require.True(t, found)
	return saved
}
