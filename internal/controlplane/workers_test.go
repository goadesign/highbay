// This file tests worker copy turns, joins on assignment reads, builder and
// test-writer working-copy refusals, and Go workspace detection in .surla-build.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

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
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
	require.NoError(t, err)
	var prepCount int
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{
			folder:  filepath.Join(project, testwriter.TestsRole.Folder),
			prepare: func(context.Context, []designeval.StorySummary) error { prepCount++; return nil },
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

	c2 := freshBuildableAfterDrop(t, cp, "c2")
	cp.buildCopyFactory = nil
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c2", RequestRevision: c2.RequestRevision})
	require.NoError(t, err)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{
			folder:  testsDir,
			prepare: func(context.Context, []designeval.StorySummary) error { prepCount++; return nil },
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
	assert.Equal(t, "Left uncommitted when change c2 started", productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/build-1"))

	twAssign2, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, twAssign2.Progress.Read)
	assert.Equal(t, 2, prepCount, "marking read of build 2 prepares gen/")
	assert.Equal(t, "surla/tests-2", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, b2, productionBuildGit(t, testsDir, "rev-parse", "HEAD"))
	assert.Equal(t, "Left uncommitted when change c2 started", productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/tests-1"))
}

// TestAssignmentReadRefusesBusyAndDroppedBuilds drives assignment_unread,
// working_copy_busy, working_folder_missing, and no_build (a dropped build)
// from get_builder_assignment, get_test_writer_assignment, and
// report_tests_finished over HTTP (409 Conflict with the refusal's name and
// message) and MCP, and pins B2's stop text on builderCopyReady and GetTestFindings.
func TestAssignmentReadRefusesBusyAndDroppedBuilds(t *testing.T) {
	_, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)
	c1 := buildableChange(t, cp, "c1")
	started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
	require.NoError(t, err)
	srv := roleServer(t, cp)
	builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
	testsDir := filepath.Join(project, testwriter.TestsRole.Folder)
	reportBody := fmt.Sprintf(`{"request_revision":%q}`, c1.RequestRevision)
	reportArgs := fmt.Sprintf(`{"change_id":"c1","request_revision":%q}`, c1.RequestRevision)
	const wantDropMsg = "The user dropped change c1: stop work on it; make no more edits or commits for it. The user starts builds on the views page."

	for _, tc := range []struct {
		name, token, method, path, body, tool, args, wantName, wantMsg string
		before, after                                                  func()
	}{
		{"assignment_unread report", "tests", http.MethodPost, "/assignments/test-writer/c1/finished", reportBody, "report_tests_finished", reportArgs,
			"assignment_unread", "Change c1's build started after your last read: stop your current work, then call get_test_writer_assignment for c1.", nil, nil},
		{"working_copy_busy builder", "builder", http.MethodGet, "/assignments/builder/c1", "", "get_builder_assignment", `{"change_id":"c1"}`,
			"working_copy_busy", "Surla is starting a build in your working folder; call get_builder_assignment again.",
			func() { cp.builderCopyTurn <- struct{}{} }, func() { <-cp.builderCopyTurn }},
		{"working_copy_busy tests", "tests", http.MethodGet, "/assignments/test-writer/c1", "", "get_test_writer_assignment", `{"change_id":"c1"}`,
			"working_copy_busy", "Surla is checking the tests in your working folder; call get_test_writer_assignment again when the check has ended.",
			func() { cp.workingCopyTurn <- struct{}{} }, func() { <-cp.workingCopyTurn }},
		{"working_folder_missing builder", "builder", http.MethodGet, "/assignments/builder/c1", "", "get_builder_assignment", `{"change_id":"c1"}`,
			"working_folder_missing", "The builder's folder " + shquote.Quote(builderDir) + " is missing: ask the user to build this change again on the views page.",
			func() {
				cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
					return &buildCopyFixture{folder: testsDir}, nil
				}
				_, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
				require.NoError(t, err)
				cp.buildCopyFactory = nil
				require.NoError(t, os.RemoveAll(builderDir))
				require.NoError(t, os.RemoveAll(testsDir))
			}, nil},
		{"working_folder_missing tests", "tests", http.MethodGet, "/assignments/test-writer/c1", "", "get_test_writer_assignment", `{"change_id":"c1"}`,
			"working_folder_missing", "The test writer's folder " + shquote.Quote(testsDir) + " is missing: ask the user to build this change again on the views page.", nil, nil},
		{"working_folder_missing report", "tests", http.MethodPost, "/assignments/test-writer/c1/finished", reportBody, "report_tests_finished", reportArgs,
			"working_folder_missing", "The test writer's folder " + shquote.Quote(testsDir) + " is missing: ask the user to build this change again on the views page.", nil, nil},
		{"no_build builder", "builder", http.MethodGet, "/assignments/builder/c1", "", "get_builder_assignment", `{"change_id":"c1"}`,
			"no_build", wantDropMsg, func() {
				_, err := cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: started.Build.StartedAt})
				require.NoError(t, err)
			}, nil},
		{"no_build tests", "tests", http.MethodGet, "/assignments/test-writer/c1", "", "get_test_writer_assignment", `{"change_id":"c1"}`,
			"no_build", wantDropMsg, nil, nil},
		{"no_build report", "tests", http.MethodPost, "/assignments/test-writer/c1/finished", reportBody, "report_tests_finished", reportArgs,
			"no_build", wantDropMsg, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.before != nil {
				tc.before()
			}
			if tc.after != nil {
				defer tc.after()
			}
			status, raw := httpCall(t, srv.URL, tc.token, tc.method, tc.path, tc.body)
			assert.Equal(t, http.StatusConflict, status)
			var got struct{ Name, Message string }
			require.NoError(t, json.Unmarshal([]byte(raw), &got))
			assert.Equal(t, tc.wantName, got.Name)
			assert.Equal(t, tc.wantMsg, got.Message)
			mcpErr, _ := mcpToolCall(t, srv.URL, tc.token, tc.tool, tc.args)
			assert.Equal(t, tc.wantMsg, mcpErr)
		})
	}
	_, err = cp.builderCopyReady(builder, project, "verify_change")
	assertRefused(t, err, "no_build")
	assert.EqualError(t, err, wantDropMsg)
	_, err = cp.GetTestFindings(user, &gensurla.GetTestFindingsPayload{ChangeID: "c1"})
	assertRefused(t, err, "not_found")
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

	c1 := buildableChange(t, cp, "c1")
	started1, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
	require.NoError(t, err)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: filepath.Join(project, testwriter.TestsRole.Folder)}, nil
	}

	// 2. Before reading assignments -> builderCopyReady and ReportTestsFinished
	// refuse with assignment_unread.
	_, err = cp.builderCopyReady(builder, project, "generate_code")
	assertRefused(t, err, "assignment_unread")
	assert.EqualError(t, err, "Change c1's build started after your last read: stop your current work, then call get_builder_assignment for c1.")
	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
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
	assert.Equal(t, "off", ready.goWork)

	// Q8: An index.lock while .surla-build is on its build branch with no
	// operation in progress refuses builderCopyReady with the lock step.
	builderGitDir := productionBuildGit(t, builderDir, "rev-parse", "--path-format=absolute", "--git-dir")
	builderLockFile := filepath.Join(builderGitDir, "index.lock")
	require.NoError(t, os.WriteFile(builderLockFile, nil, 0o600))
	_, err = cp.builderCopyReady(builder, project, "verify_change")
	var builderLockRefusal *gensurla.CopyOffBranch
	require.ErrorAs(t, err, &builderLockRefusal)
	require.NotNil(t, builderLockRefusal.Branch)
	assert.Equal(t, "surla/build-1", *builderLockRefusal.Branch)
	require.NotNil(t, builderLockRefusal.BuildBranch)
	assert.Equal(t, "surla/build-1", *builderLockRefusal.BuildBranch)
	assert.Equal(t, "none", builderLockRefusal.Operation)
	assert.Contains(t, builderLockRefusal.Message, "Your working copy is on surla/build-1 for change c1. Git's index lock (")
	assert.Contains(t, builderLockRefusal.Message, "index.lock') exists in your working copy: wait for the running git command to finish, or remove ")
	require.NoError(t, os.Remove(builderLockFile))

	// An index.lock while .surla-tests is on its build branch with no operation
	// in progress refuses ReportTestsFinished with the lock step, writes nothing,
	// and records no verdict.
	testsGitDir := productionBuildGit(t, testsDir, "rev-parse", "--path-format=absolute", "--git-dir")
	lockFile := filepath.Join(testsGitDir, "index.lock")
	require.NoError(t, os.WriteFile(lockFile, nil, 0o600))
	beforeLockBuild := readBuildFile(t, cp, "c1")
	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
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
	assert.Equal(t, "Your working copy is on branch 'experiment'; change c1 is built on surla/build-1. Uncommitted (2): a.go, b.go. Put your work on surla/build-1, commit it, and call verify_change again.", offBranch.Message)

	// Switch tests copy to detached HEAD with 1 uncommitted file; ReportTestsFinished
	// refuses with CopyOffBranch where Branch is nil and BuildBranch is "surla/tests-1".
	runGitForTest(t, testsDir, "switch", "--quiet", "--detach", "HEAD")
	require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "t_test.go"), []byte("package eval\n"), 0o600))
	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
	offBranch = nil
	require.ErrorAs(t, err, &offBranch)
	assert.Nil(t, offBranch.Branch, "detached HEAD has no Branch")
	require.NotNil(t, offBranch.BuildBranch)
	assert.Equal(t, "surla/tests-1", *offBranch.BuildBranch)
	assert.Equal(t, []string{"eval/t_test.go"}, offBranch.Uncommitted)
	assert.Equal(t, 1, offBranch.UncommittedCount)
	assert.Equal(t, "Your working copy has a detached HEAD (not on any branch); change c1 is built on surla/tests-1. Uncommitted (1): eval/t_test.go. Put your test files on surla/tests-1, uncommitted (Surla commits what it accepts), and call report_tests_finished again.", offBranch.Message)

	// 4. Before a join on build 2 (BuildBranch == nil): drop build 1, start build 2
	// for "c2". Both copies are off earlier build branches with uncommitted files.
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: started1.Build.StartedAt})
	require.NoError(t, err)
	c2 := freshBuildableAfterDrop(t, cp, "c2")
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c2", RequestRevision: c2.RequestRevision})
	require.NoError(t, err)

	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	offBranch = nil
	require.ErrorAs(t, err, &offBranch)
	require.NotNil(t, offBranch.Branch)
	assert.Equal(t, "experiment", *offBranch.Branch)
	assert.Nil(t, offBranch.BuildBranch, "BuildBranch is absent before the copy joined the build")
	assert.Equal(t, "Your working copy is on branch 'experiment'. Uncommitted (2): a.go, b.go. Surla puts it on change c2's new branch surla/build-2 only when nothing is uncommitted, or when it is on an earlier surla/build branch, where Surla commits what is left. Commit your work on a branch of your own, not one of the user's, then call get_builder_assignment again.", offBranch.Message)

	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	offBranch = nil
	require.ErrorAs(t, err, &offBranch)
	assert.Nil(t, offBranch.Branch)
	assert.Nil(t, offBranch.BuildBranch, "BuildBranch is absent before the copy joined the build")
	assert.Equal(t, "Your working copy has a detached HEAD (not on any branch). Uncommitted (1): eval/t_test.go. Surla puts it on change c2's new branch surla/tests-2 only when nothing is uncommitted, or when it is on an earlier surla/tests branch, where Surla commits what is left. Return your copy to an earlier surla/tests branch with your files, where Surla commits them, or remove them; then call get_test_writer_assignment again.", offBranch.Message)

	// 5. copyOffBranchRefusal covers in-progress Git operations, index.lock,
	// truncated uncommitted lists, and unprintable branch names.
	builderCherryPick := copyOffBranchRefusal(testwriter.BuilderRole, "c2", 2, true, testwriter.CopyFacts{
		Branch: "surla/build-2", Operation: "cherry_pick", IndexLock: ".surla-build/.git/index.lock",
		Uncommitted: []string{"a.go", "b.go"}, UncommittedCount: 5,
	}, "verify_change")
	assert.Equal(t, "Your working copy is on surla/build-2 for change c2. A cherry-pick is in progress in your working copy: finish or abort it. Git's index lock ('.surla-build/.git/index.lock') exists in your working copy: wait for the running git command to finish, or remove '.surla-build/.git/index.lock' if no git process is running. Uncommitted (5, first 2): a.go, b.go. Put your work on surla/build-2, commit it, and call verify_change again.", builderCherryPick.Message)
	testsUnprintable := copyOffBranchRefusal(testwriter.TestsRole, "c2", 2, false, testwriter.CopyFacts{
		Branch: "a name that is not shown", Operation: "merge",
	}, "get_test_writer_assignment")
	assert.Equal(t, "Your working copy is on branch a name that is not shown. A merge is in progress in your working copy: abort it. Uncommitted: none. Surla puts it on change c2's new branch surla/tests-2 only when nothing is uncommitted, or when it is on an earlier surla/tests branch, where Surla commits what is left. Return your copy to an earlier surla/tests branch with your files, where Surla commits them, or remove them; then call get_test_writer_assignment again.", testsUnprintable.Message)
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
		_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
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
		_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
		require.NoError(t, err)
		builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
		assert.FileExists(t, filepath.Join(builderDir, "go.work"))
		assign, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder), &gensurla.GetBuilderAssignmentPayload{ChangeID: "chg"})
		require.NoError(t, err)
		assert.Equal(t, builderDir, assign.WorkingFolder)
		assert.False(t, assign.GoworkOff, "committed go.work inside .surla-build sets gowork_off false")
	})
}

// TestAssignmentReadEditedDuringJoinAndStoppedBeforeOrAfterSwitch verifies:
//   - a file edited during the read (switch refused, no branch created, read
//     refused with working_copy_busy, and the next read commits the edit on the
//     earlier branch and joins);
//   - a join stopped before or after the switch;
//   - a clean copy on a side branch or detached HEAD joining;
//   - typed CopyOffBranch facts over HTTP (409 Conflict) and MCP.
func TestAssignmentReadEditedDuringJoinAndStoppedBeforeOrAfterSwitch(t *testing.T) {
	repo, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)
	builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
	testsDir := filepath.Join(project, testwriter.TestsRole.Folder)

	writeWorkspaceFiles(t, project, map[string]string{"a_conflict.go": "package main // base b1\n"})
	runGitForTest(t, repo, "add", "a_conflict.go")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "B1 with a_conflict.go")

	c1 := buildableChange(t, cp, "c1")
	b1Res, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
	require.NoError(t, err)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: testsDir}, nil
	}
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil

	writeWorkspaceFiles(t, project, map[string]string{"a_conflict.go": "package main // base b2\n"})
	runGitForTest(t, repo, "add", "a_conflict.go")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "B2")
	b2 := productionBuildGit(t, repo, "rev-parse", "HEAD")

	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: b1Res.Build.StartedAt})
	require.NoError(t, err)
	c2 := freshBuildableAfterDrop(t, cp, "c2")
	b2Res, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c2", RequestRevision: c2.RequestRevision})
	require.NoError(t, err)

	// 1. File edited during the read: appending to tracked a_conflict.go when
	// untracked z_trigger.txt runs its clean filter leaves a_conflict.go dirty
	// after the leftover commit updates the index, so git switch -c refuses
	// without creating surla/build-2.
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
	assert.False(t, copyBranchExists(t.Context(), repo, "surla/build-2"))
	assert.Equal(t, "surla/build-1", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.False(t, mustOpenBuildFile(t, cp).Builder.Read)

	require.NoError(t, os.Remove(enableFlag))
	bAssign2, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, bAssign2.Read)
	assert.Equal(t, "surla/build-2", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, b2, productionBuildGit(t, builderDir, "rev-parse", "HEAD"))
	assert.Contains(t, productionBuildGit(t, repo, "show", "surla/build-1:a_conflict.go"), "// edited during read")

	// Clean detached HEAD and clean side branches join when the build branch
	// does not yet exist, without making a leftover commit or moving the side branch.
	surlaTests1Tip := productionBuildGit(t, repo, "rev-parse", "surla/tests-1")
	runGitForTest(t, builderDir, "switch", "--quiet", "-c", "builder-side", b2)
	runGitForTest(t, repo, "branch", "-D", "surla/build-2")
	bSideAssign, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, bSideAssign.Read)
	assert.Equal(t, "surla/build-2", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, b2, productionBuildGit(t, repo, "rev-parse", "builder-side"))

	runGitForTest(t, testsDir, "switch", "--quiet", "--detach", surlaTests1Tip)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: testsDir}, nil
	}
	twAssign2, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	assert.True(t, twAssign2.Progress.Read)
	assert.Equal(t, "surla/tests-2", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, surlaTests1Tip, productionBuildGit(t, repo, "rev-parse", "surla/tests-1"))

	runGitForTest(t, testsDir, "switch", "--quiet", "-c", "writer-side", surlaTests1Tip)
	runGitForTest(t, repo, "branch", "-D", "surla/tests-2")
	twSideAssign, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil
	assert.True(t, twSideAssign.Progress.Read)
	assert.Equal(t, "surla/tests-2", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, surlaTests1Tip, productionBuildGit(t, repo, "rev-parse", "writer-side"))

	// 2. Join stopped before and after the switch on build 3 ("c3"):
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
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c3", RequestRevision: c3.RequestRevision})
	require.NoError(t, err)

	runGitForTest(t, builderDir, "read-tree", "-u", "--reset", b3)
	require.Equal(t, "surla/build-2", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	bAssign3, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c3"})
	require.NoError(t, err)
	assert.True(t, bAssign3.Read)
	assert.Equal(t, "surla/build-3", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	assert.Equal(t, b3, productionBuildGit(t, builderDir, "rev-parse", "HEAD"))
	assert.Equal(t, c2Tip, productionBuildGit(t, repo, "rev-parse", "surla/build-2"))

	runGitForTest(t, testsDir, "switch", "--quiet", "-c", "surla/tests-3", b3)
	require.False(t, mustOpenBuildFile(t, cp).TestWriter.Read)
	_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{ChangeID: "c3", RequestRevision: c3.RequestRevision})
	assertRefused(t, err, "assignment_unread")

	var preparedOnRetry bool
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{
			folder:  testsDir,
			prepare: func(context.Context, []designeval.StorySummary) error { preparedOnRetry = true; return nil },
		}, nil
	}
	twAssign3, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c3"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil
	assert.True(t, twAssign3.Progress.Read)
	assert.True(t, preparedOnRetry)

	// 3. Typed CopyOffBranch over HTTP (409 Conflict with structured JSON fields) and MCP.
	runGitForTest(t, testsDir, "switch", "--quiet", "-c", "agent-tests")
	require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "http_test.go"), []byte("package eval\n"), 0o600))
	srv := roleServer(t, cp)
	status, raw := httpCall(t, srv.URL, "tests", http.MethodPost, "/assignments/test-writer/c3/finished", fmt.Sprintf(`{"request_revision":%q}`, c3.RequestRevision))
	assert.Equal(t, http.StatusConflict, status)
	var body struct {
		Name, Message, Operation string
		Branch                   *string  `json:"branch"`
		BuildBranch              *string  `json:"build_branch"`
		Uncommitted              []string `json:"uncommitted"`
		UncommittedCount         int      `json:"uncommitted_count"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &body))
	assert.Equal(t, "copy_off_branch", body.Name)
	require.NotNil(t, body.Branch)
	assert.Equal(t, "agent-tests", *body.Branch)
	require.NotNil(t, body.BuildBranch)
	assert.Equal(t, "surla/tests-3", *body.BuildBranch)
	assert.Equal(t, "none", body.Operation)
	assert.Equal(t, []string{"eval/http_test.go"}, body.Uncommitted)
	assert.Equal(t, 1, body.UncommittedCount)
	assert.Contains(t, body.Message, "Your working copy is on branch 'agent-tests'; change c3 is built on surla/tests-3.")
	mcpErr, _ := mcpToolCall(t, srv.URL, "tests", "report_tests_finished", fmt.Sprintf(`{"change_id":"c3","request_revision":%q}`, c3.RequestRevision))
	assert.Contains(t, mcpErr, "Your working copy is on branch 'agent-tests'; change c3 is built on surla/tests-3.")
	assert.Contains(t, mcpErr, "call report_tests_finished again.")

	// 4. Operation in progress (MERGE_HEAD) refused at a real read over HTTP and MCP.
	runGitForTest(t, testsDir, "switch", "--quiet", "surla/tests-3")
	require.NoError(t, os.Remove(filepath.Join(testsDir, "eval", "http_test.go")))
	bGitDir := productionBuildGit(t, builderDir, "rev-parse", "--path-format=absolute", "--git-dir")
	tGitDir := productionBuildGit(t, testsDir, "rev-parse", "--path-format=absolute", "--git-dir")
	require.NoError(t, os.WriteFile(filepath.Join(bGitDir, "MERGE_HEAD"), []byte(b3+"\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tGitDir, "MERGE_HEAD"), []byte(b3+"\n"), 0o600))

	for _, tc := range []struct {
		token, path, tool, branch, step string
	}{
		{token: "builder", path: "/assignments/builder/c3", tool: "get_builder_assignment", branch: "surla/build-3", step: "A merge is in progress in your working copy: finish or abort it."},
		{token: "tests", path: "/assignments/test-writer/c3", tool: "get_test_writer_assignment", branch: "surla/tests-3", step: "A merge is in progress in your working copy: abort it."},
	} {
		status, raw := httpCall(t, srv.URL, tc.token, http.MethodGet, tc.path, "")
		assert.Equal(t, http.StatusConflict, status, tc.tool)
		var mergeBody struct {
			Name, Message, Operation string
			Branch                   *string `json:"branch"`
			BuildBranch              *string `json:"build_branch"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &mergeBody), tc.tool)
		assert.Equal(t, "copy_off_branch", mergeBody.Name, tc.tool)
		require.NotNil(t, mergeBody.Branch, tc.tool)
		assert.Equal(t, tc.branch, *mergeBody.Branch, tc.tool)
		require.NotNil(t, mergeBody.BuildBranch, tc.tool)
		assert.Equal(t, tc.branch, *mergeBody.BuildBranch, tc.tool)
		assert.Equal(t, "merge", mergeBody.Operation, tc.tool)
		assert.Contains(t, mergeBody.Message, tc.step, tc.tool)
		toolErr, _ := mcpToolCall(t, srv.URL, tc.token, tc.tool, `{"change_id":"c3"}`)
		assert.Contains(t, toolErr, tc.step, tc.tool)
		assert.Contains(t, toolErr, "call "+tc.tool+" again.", tc.tool)
	}
	require.NoError(t, os.Remove(filepath.Join(bGitDir, "MERGE_HEAD")))
	require.NoError(t, os.Remove(filepath.Join(tGitDir, "MERGE_HEAD")))
}

// TestReviewN1N2: build 1 read by both; drop; build X; drop X before any read
// (N1) or after only the builder read it (N2); build Y is number 3 and each
// copy joins from its last branch, saving uncommitted files on the last branch,
// and in N1 a subsequent build 4 read only by the builder followed by build 5
// joins from surla/build-4 and surla/tests-3 to surla/*-5.
func TestReviewN1N2(t *testing.T) {
	for _, builderReadsX := range []bool{false, true} {
		name := "N1 no read of X"
		if builderReadsX {
			name = "N2 only the builder read X"
		}
		t.Run(name, func(t *testing.T) {
			repo, project, cp, _ := reviewProject(t, Options{})
			user := asCaller("alice@example.com", CallerUser)
			builder := asCaller("alice@example.com", CallerBuilder)
			writer := asCaller("alice@example.com", CallerTestWriter)
			builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
			testsDir := filepath.Join(project, testwriter.TestsRole.Folder)
			fixture := func(context.Context, string, scaffold.Layout) (buildCopy, error) {
				return &buildCopyFixture{
					folder:  testsDir,
					prepare: func(context.Context, []designeval.StorySummary) error { return nil },
				}, nil
			}
			c1 := freshBuildableAfterDrop(t, cp, "c1")
			b1, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
			require.NoError(t, err)
			_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
			require.NoError(t, err)
			cp.buildCopyFactory = fixture
			_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
			require.NoError(t, err)
			cp.buildCopyFactory = nil
			require.NoError(t, os.WriteFile(filepath.Join(builderDir, "left.go"), []byte("package shop\n"), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "left_test.go"), []byte("package eval\n"), 0o600))
			_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: b1.Build.StartedAt})
			require.NoError(t, err)

			cx := freshBuildableAfterDrop(t, cp, "cx")
			bx, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "cx", RequestRevision: cx.RequestRevision})
			require.NoError(t, err)
			assert.Equal(t, 2, mustOpenBuildFile(t, cp).Number)
			wantBuilderBranchBeforeY := "surla/build-1"
			if builderReadsX {
				_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "cx"})
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(builderDir, "left2.go"), []byte("package shop\n"), 0o600))
				wantBuilderBranchBeforeY = "surla/build-2"
			}
			_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "cx", StartedAt: bx.Build.StartedAt})
			require.NoError(t, err)

			cy := freshBuildableAfterDrop(t, cp, "cy")
			by, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "cy", RequestRevision: cy.RequestRevision})
			require.NoError(t, err)
			assert.Equal(t, 3, mustOpenBuildFile(t, cp).Number)
			_, err = cp.builderCopyReady(builder, project, "verify_change")
			assertRefused(t, err, "assignment_unread")
			assert.Equal(t, wantBuilderBranchBeforeY, productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
			_, err = cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{ChangeID: "cy", RequestRevision: cy.RequestRevision})
			assertRefused(t, err, "assignment_unread")
			assert.Equal(t, "surla/tests-1", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))

			_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "cy"})
			require.NoError(t, err)
			cp.buildCopyFactory = fixture
			_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "cy"})
			require.NoError(t, err)
			cp.buildCopyFactory = nil
			for _, role := range []testwriter.Role{testwriter.BuilderRole, testwriter.TestsRole} {
				dir := filepath.Join(project, role.Folder)
				assert.Equal(t, role.Branch(3), productionBuildGit(t, dir, "symbolic-ref", "--short", "HEAD"))
			}
			assert.False(t, copyBranchExists(t.Context(), repo, "surla/tests-2"))
			assert.Equal(t, "Left uncommitted when change cy started", productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/tests-1"))
			if !builderReadsX {
				assert.False(t, copyBranchExists(t.Context(), repo, "surla/build-2"))
				assert.Equal(t, "Left uncommitted when change cy started", productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/build-1"))
				require.NoError(t, os.WriteFile(filepath.Join(testsDir, "eval", "n2_test.go"), []byte("package eval\n"), 0o600))
				_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "cy", StartedAt: by.Build.StartedAt})
				require.NoError(t, err)
				cx2 := freshBuildableAfterDrop(t, cp, "cx2")
				bx2, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "cx2", RequestRevision: cx2.RequestRevision})
				require.NoError(t, err)
				assert.Equal(t, 4, mustOpenBuildFile(t, cp).Number)
				_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "cx2"})
				require.NoError(t, err)
				assert.Equal(t, "surla/build-4", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
				assert.Equal(t, "surla/tests-3", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
				require.NoError(t, os.WriteFile(filepath.Join(builderDir, "n2.go"), []byte("package shop\n"), 0o600))
				_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "cx2", StartedAt: bx2.Build.StartedAt})
				require.NoError(t, err)
				cy2 := freshBuildableAfterDrop(t, cp, "cy2")
				_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "cy2", RequestRevision: cy2.RequestRevision})
				require.NoError(t, err)
				assert.Equal(t, 5, mustOpenBuildFile(t, cp).Number)
				_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "cy2"})
				require.NoError(t, err)
				cp.buildCopyFactory = fixture
				_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "cy2"})
				require.NoError(t, err)
				assert.Equal(t, "surla/build-5", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
				assert.Equal(t, "surla/tests-5", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
				assert.False(t, copyBranchExists(t.Context(), repo, "surla/tests-4"))
				assert.Equal(t, "Left uncommitted when change cy2 started", productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/build-4"))
				assert.Equal(t, "Left uncommitted when change cy2 started", productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/tests-3"))
			} else {
				assert.Equal(t, "Left uncommitted when change cx started", productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/build-1"))
				assert.Equal(t, "Left uncommitted when change cy started", productionBuildGit(t, repo, "log", "-1", "--format=%s", "surla/build-2"))
			}
		})
	}
}

// TestReviewBuilderReadDuringATestsCheck: the tests turn is held (a check
// runs); the builder's read joins, the test writer's is busy, the next joins.
func TestReviewBuilderReadDuringATestsCheck(t *testing.T) {
	_, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)
	builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
	testsDir := filepath.Join(project, testwriter.TestsRole.Folder)

	c1 := freshBuildableAfterDrop(t, cp, "c1")
	started1, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
	require.NoError(t, err)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: testsDir}, nil
	}
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)

	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: started1.Build.StartedAt})
	require.NoError(t, err)
	c2 := freshBuildableAfterDrop(t, cp, "c2")
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c2", RequestRevision: c2.RequestRevision})
	require.NoError(t, err)

	cp.workingCopyTurn <- struct{}{}
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
	assert.NoError(t, err, "a builder read during a tests check joins")
	assert.Equal(t, "surla/build-2", productionBuildGit(t, builderDir, "symbolic-ref", "--short", "HEAD"))
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	assertRefused(t, err, "working_copy_busy")
	assert.EqualError(t, err, "Surla is checking the tests in your working folder; call get_test_writer_assignment again when the check has ended.")
	assert.Equal(t, "surla/tests-1", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
	<-cp.workingCopyTurn

	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
	assert.NoError(t, err)
	assert.Equal(t, "surla/tests-2", productionBuildGit(t, testsDir, "symbolic-ref", "--short", "HEAD"))
}

// TestReviewReportRefusalsWriteNothing: each report refusal leaves the build
// file byte-identical and wakes no checker.
func TestReviewReportRefusalsWriteNothing(t *testing.T) {
	_, project, cp, _ := reviewProject(t, Options{})
	user := asCaller("alice@example.com", CallerUser)
	writer := asCaller("alice@example.com", CallerTestWriter)
	c1 := freshBuildableAfterDrop(t, cp, "c1")
	_, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
	require.NoError(t, err)
	report := func() error {
		_, err := cp.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
		return err
	}
	before := readBuildFile(t, cp, "c1")
	assertRefused(t, report(), "assignment_unread")
	assert.Equal(t, before, readBuildFile(t, cp, "c1"))
	assert.Empty(t, cp.reportWake)
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return &buildCopyFixture{folder: filepath.Join(project, testwriter.TestsRole.Folder)}, nil
	}
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
	require.NoError(t, err)
	cp.buildCopyFactory = nil
	tests := filepath.Join(project, testwriter.TestsRole.Folder)
	gitDir := productionBuildGit(t, tests, "rev-parse", "--path-format=absolute", "--git-dir")
	require.NoError(t, os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(productionBuildGit(t, tests, "rev-parse", "HEAD")+"\n"), 0o600))
	before = readBuildFile(t, cp, "c1")
	err = report()
	var off *gensurla.CopyOffBranch
	require.ErrorAs(t, err, &off)
	assert.Equal(t, before, readBuildFile(t, cp, "c1"))
	assert.Empty(t, cp.reportWake)
}

// TestAssignmentReadQ3DropAndReplacementDuringJoinAndQ10FailedPrepare verifies
// Q3 (a build dropped or replaced while wc.Join is in flight during
// GetBuilderAssignment or GetTestWriterAssignment) and Q10 (Read stays true
// after a failed Prepare on a marking GetTestWriterAssignment).
func TestAssignmentReadQ3DropAndReplacementDuringJoinAndQ10FailedPrepare(t *testing.T) {
	t.Run("Q3 drop and replacement during Join", func(t *testing.T) {
		repo, project, cp, _ := reviewProject(t, Options{})
		user := asCaller("alice@example.com", CallerUser)
		builder := asCaller("alice@example.com", CallerBuilder)
		writer := asCaller("alice@example.com", CallerTestWriter)
		builderDir := filepath.Join(project, testwriter.BuilderRole.Folder)
		testsDir := filepath.Join(project, testwriter.TestsRole.Folder)

		c1 := freshBuildableAfterDrop(t, cp, "c1")
		b1, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
		require.NoError(t, err)
		cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
			return &buildCopyFixture{folder: testsDir}, nil
		}
		_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c1"})
		require.NoError(t, err)
		_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
		require.NoError(t, err)

		_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c1", StartedAt: b1.Build.StartedAt})
		require.NoError(t, err)
		c2 := freshBuildableAfterDrop(t, cp, "c2")
		_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c2", RequestRevision: c2.RequestRevision})
		require.NoError(t, err)

		enteredB, releaseB := armJoinPauseFilter(t, repo, builderDir)
		bErrCh := make(chan error, 1)
		go func() {
			_, callErr := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c2"})
			bErrCh <- callErr
		}()
		waitForFile(t, enteredB)
		replaceOpenBuildStartedAt(t, cp)
		require.NoError(t, os.WriteFile(releaseB, []byte("go"), 0o600))
		bErr := <-bErrCh
		assertRefused(t, bErr, "working_copy_busy")
		assert.EqualError(t, bErr, "Surla is starting a build in your working folder; call get_builder_assignment again.")
		assert.False(t, mustOpenBuildFile(t, cp).Builder.Read)

		var prepRan bool
		cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
			return &buildCopyFixture{
				folder:  testsDir,
				prepare: func(context.Context, []designeval.StorySummary) error { prepRan = true; return nil },
			}, nil
		}
		enteredTW, releaseTW := armJoinPauseFilter(t, repo, testsDir)
		twErrCh := make(chan error, 1)
		go func() {
			_, callErr := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c2"})
			twErrCh <- callErr
		}()
		waitForFile(t, enteredTW)
		replaceOpenBuildStartedAt(t, cp)
		require.NoError(t, os.WriteFile(releaseTW, []byte("go"), 0o600))
		twErr := <-twErrCh
		assertRefused(t, twErr, "working_copy_busy")
		assert.EqualError(t, twErr, "A new build started while Surla was moving your working copy; call get_test_writer_assignment again.")
		assert.False(t, mustOpenBuildFile(t, cp).TestWriter.Read)
		assert.False(t, prepRan, "Prepare must not run when build was replaced during Join")

		current := mustOpenBuildFile(t, cp)
		_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c2", StartedAt: current.StartedAt.Format(time.RFC3339Nano)})
		require.NoError(t, err)
		c3 := freshBuildableAfterDrop(t, cp, "c3")
		b3, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c3", RequestRevision: c3.RequestRevision})
		require.NoError(t, err)
		enteredDropB, releaseDropB := armJoinPauseFilter(t, repo, builderDir)
		go func() {
			_, callErr := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "c3"})
			bErrCh <- callErr
		}()
		waitForFile(t, enteredDropB)
		_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c3", StartedAt: b3.Build.StartedAt})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(releaseDropB, []byte("go"), 0o600))
		bDropErr := <-bErrCh
		assertRefused(t, bDropErr, "no_build")
		assert.EqualError(t, bDropErr, "The user dropped change c3: stop work on it; make no more edits or commits for it. The user starts builds on the views page.")
		assert.False(t, mustOpenBuildFile(t, cp).Builder.Read)

		c4 := freshBuildableAfterDrop(t, cp, "c4")
		b4, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c4", RequestRevision: c4.RequestRevision})
		require.NoError(t, err)
		enteredDropTW, releaseDropTW := armJoinPauseFilter(t, repo, testsDir)
		go func() {
			_, callErr := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c4"})
			twErrCh <- callErr
		}()
		waitForFile(t, enteredDropTW)
		_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "c4", StartedAt: b4.Build.StartedAt})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(releaseDropTW, []byte("go"), 0o600))
		twDropErr := <-twErrCh
		assertRefused(t, twDropErr, "no_build")
		assert.EqualError(t, twDropErr, "The user dropped change c4: stop work on it; make no more edits or commits for it. The user starts builds on the views page.")
		assert.False(t, mustOpenBuildFile(t, cp).TestWriter.Read)
	})

	t.Run("Q10 Read stays true after failed Prepare", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			prepErr  error
			wantKind gensurla.TestsKind
		}{
			{name: "could_not_run", prepErr: fmt.Errorf("%w: disk full", testwriter.ErrWriteGenerated), wantKind: gensurla.TestsKindCouldNotRun},
			{name: "needs_changes", prepErr: &testwriter.AuthorError{Finding: testwriter.Finding{Kind: testwriter.FindingBuild, Message: "broken go.mod"}}, wantKind: gensurla.TestsKindNeedsChanges},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, project, cp, _ := reviewProject(t, Options{})
				user := asCaller("alice@example.com", CallerUser)
				writer := asCaller("alice@example.com", CallerTestWriter)
				c1 := freshBuildableAfterDrop(t, cp, "c1")
				_, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "c1", RequestRevision: c1.RequestRevision})
				require.NoError(t, err)
				var prepCalls int
				cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
					return &buildCopyFixture{
						folder:  filepath.Join(project, testwriter.TestsRole.Folder),
						prepare: func(context.Context, []designeval.StorySummary) error { prepCalls++; return tc.prepErr },
					}, nil
				}
				assign1, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
				require.NoError(t, err)
				assert.Equal(t, 1, prepCalls)
				assert.True(t, assign1.Progress.Read)
				assert.Equal(t, tc.wantKind, assign1.Progress.Tests.Kind())
				assert.True(t, mustOpenBuildFile(t, cp).TestWriter.Read)
				assign2, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "c1"})
				require.NoError(t, err)
				assert.Equal(t, 1, prepCalls, "second read does not re-run Prepare")
				assert.True(t, assign2.Progress.Read)
			})
		}
	})
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

// armJoinPauseFilter configures a Git clean filter in repo and writes a dirty
// tracked file in copyDir so the next wc.Join pauses inside stageCopy (git add
// --all) after touching enteredFile until releaseFile exists.
func armJoinPauseFilter(t *testing.T, repo, copyDir string) (string, string) {
	t.Helper()
	syncDir := t.TempDir()
	enteredFile, releaseFile := filepath.Join(syncDir, "entered"), filepath.Join(syncDir, "release")
	filterCmd := fmt.Sprintf("touch %s; while ! test -f %s; do sleep 0.01; done; cat", shquote.Quote(enteredFile), shquote.Quote(releaseFile))
	runGitForTest(t, repo, "config", "filter.pausejoin.clean", filterCmd)
	require.NoError(t, os.WriteFile(filepath.Join(copyDir, ".gitattributes"), []byte("pause.txt filter=pausejoin\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(copyDir, "pause.txt"), []byte(syncDir+"\n"), 0o600))
	return enteredFile, releaseFile
}

// waitForFile waits up to 10 seconds for path to exist on disk.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			require.FailNowf(t, "timeout", "file %s did not appear within 10s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// replaceOpenBuildStartedAt advances the open build's StartedAt and resets
// both Read flags under cp.mu.Lock to simulate a replacement build starting
// while a worker copy join is in flight.
func replaceOpenBuildStartedAt(t *testing.T, cp *ControlPlane) {
	t.Helper()
	cp.mu.Lock()
	defer cp.mu.Unlock()
	saved, found, err := cp.cards.loadOpenBuild()
	require.NoError(t, err)
	require.True(t, found)
	saved.StartedAt = saved.StartedAt.Add(time.Second)
	saved.Builder.Read = false
	saved.TestWriter.Read = false
	require.NoError(t, cp.cards.saveOpenBuild(saved))
}
