// This file tests the staged-release promotion target behind
// the project's stored promotion settings.

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/testmodule"
	"github.com/goadesign/surla/internal/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// workflowTeamFixture accepts build-role registration without starting agents.
type workflowTeamFixture struct {
	// registered records the actual StartBuild admission of its worker folders.
	registered atomic.Bool
}

// suiteCommit is the evaluator suite commit these tests require.
var suiteCommit = strings.Repeat("b", 40)

// TestLoadPromotionConfig checks that only a complete configuration with
// distinct plain binary names outside the bound workspace starts serve.
func TestLoadPromotionConfig(t *testing.T) {
	valid := `{"release_dir":"/rel","binaries":["orders","orders-worker"]}`
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{"complete", valid, ""},
		{"missing fields", `{"binaries":[]}`, "is missing release_dir, binaries"},
		{"unknown field", strings.TrimSuffix(valid, "}") + `,"extra":1}`, "unknown field"},
		{"legacy suite_id field", strings.TrimSuffix(valid, "}") + `,"suite_id":"orders"}`, "unknown field"},
		{"trailing data", valid + "{}", "data after the configuration"},
		{"binary with a path", strings.Replace(valid, `"orders-worker"`, `"bin/orders-worker"`, 1), "must not contain /"},
		{"parent directory", strings.Replace(valid, `"orders-worker"`, `".."`, 1), "is not a valid artifact name"},
		{"repeated binary", strings.Replace(valid, `"orders-worker"`, `"orders"`, 1), "is listed twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			wsDir := filepath.Join(root, "ws")
			require.NoError(t, os.MkdirAll(wsDir, 0o755))
			path := filepath.Join(root, "promotion.json")
			require.NoError(t, os.WriteFile(path, []byte(c.content), 0o600))
			target, err := storedPromotionTarget(t, path)
			if c.wantErr != "" {
				assert.ErrorContains(t, err, c.wantErr)
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, target)
		})
	}

	// Relative directories are resolved against baseDir once, at load, and
	// paths inside the workspace are rejected.
	root := t.TempDir()
	baseDir, err := filepath.EvalSymlinks(filepath.Join(root, "ops"))
	if err != nil {
		require.NoError(t, os.MkdirAll(filepath.Join(root, "ops"), 0o755))
		baseDir, err = filepath.EvalSymlinks(filepath.Join(root, "ops"))
		require.NoError(t, err)
	}
	wsDir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	path := filepath.Join(baseDir, "promotion.json")
	relative := strings.Replace(valid, `"/rel"`, `"release"`, 1)
	require.NoError(t, os.WriteFile(path, []byte(relative), 0o600))
	target, err := storedPromotionTarget(t, path)
	require.NoError(t, err)
	cfg := target.(*releaseTarget).cfg
	assertOutsideDirectory(t, cfg.ReportDir, wsDir)
	assert.NotEqual(t, filepath.Join(baseDir, "reports"), cfg.ReportDir, "project owns reports")
	assert.Equal(t, filepath.Join(baseDir, "release"), cfg.ReleaseDir)
}

// TestReleaseTargetMeasuresStagedBinaries checks that CurrentArtifacts hashes
// the files in the commit's release directory and fails, naming the binary,
// when one is missing or reached through a link that leaves the directory.
func TestReleaseTargetMeasuresStagedBinaries(t *testing.T) {
	commit := strings.Repeat("c", 40)
	release := t.TempDir()
	target := newReleaseTarget(t, release, t.TempDir())
	publish(t, release, commit, map[string]string{"orders": "api v1", "orders-worker": "worker v1"})

	artifacts, err := target.CurrentArtifacts(context.Background(), commit)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"orders": digestOf("api v1"), "orders-worker": digestOf("worker v1")}, artifacts)

	_, err = target.CurrentArtifacts(context.Background(), strings.Repeat("d", 40))
	assert.ErrorContains(t, err, "open release directory for commit "+strings.Repeat("d", 40))

	missing := strings.Repeat("e", 40)
	publish(t, release, missing, map[string]string{"orders": "api v1"})
	_, err = target.CurrentArtifacts(context.Background(), missing)
	assert.ErrorContains(t, err, "read release binary orders-worker for commit "+missing)

	escaping := strings.Repeat("f", 40)
	publish(t, release, escaping, map[string]string{"orders": "api v1"})
	outside := filepath.Join(t.TempDir(), "worker")
	require.NoError(t, os.WriteFile(outside, []byte("worker v1"), 0o600))
	require.NoError(t, os.Symlink(outside, filepath.Join(release, escaping, "orders-worker")))
	_, err = target.CurrentArtifacts(context.Background(), escaping)
	assert.ErrorContains(t, err, "path escapes from parent", "a link out of the release directory is not followed")

	long := strings.Repeat("a", 64)
	publish(t, release, long, map[string]string{"orders": "api v1", "orders-worker": "worker v1"})
	_, err = target.CurrentArtifacts(context.Background(), long)
	assert.NoError(t, err, "a SHA-256 commit ID names a release directory too")

	for _, bad := range []string{"../" + commit, commit + "/..", strings.Repeat("c", 39), strings.ToUpper(commit)} {
		_, err = target.CurrentArtifacts(context.Background(), bad)
		assert.ErrorContains(t, err, "is not a full commit ID", bad)
	}
}

// TestReleaseTargetRefusesNonRegularFiles stages a FIFO under a binary's
// name and checks that CurrentArtifacts fails promptly instead of blocking on it.
func TestReleaseTargetRefusesNonRegularFiles(t *testing.T) {
	commit := strings.Repeat("c", 40)
	release := t.TempDir()
	target := newReleaseTarget(t, release, t.TempDir())
	publish(t, release, commit, map[string]string{"orders": "api v1"})
	require.NoError(t, syscall.Mkfifo(filepath.Join(release, commit, "orders-worker"), 0o600))

	done := make(chan error, 1)
	go func() {
		_, err := target.CurrentArtifacts(context.Background(), commit)
		done <- err
	}()
	select {
	case err := <-done:
		assert.ErrorContains(t, err, "orders-worker is not a regular file")
	case <-time.After(10 * time.Second):
		t.Fatal("CurrentArtifacts blocked on a FIFO")
	}
}

// TestApproveMergeChecksTheStagedRelease approves a confirmed card against a
// real saved report and staged release, then replaces the release directory
// with different binaries and checks that the saved approval is no longer
// returned, and comes back when the approved binaries are published again.
func TestApproveMergeChecksTheStagedRelease(t *testing.T) {
	if testing.Short() {
		t.Skip("evaluates the real refresh/build prerequisites; the full suite covers release approval")
	}
	release := t.TempDir()
	alice := controlplane.WithPrincipal(context.Background(), controlplane.Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: controlplane.CallerUser})

	// Snapshot and build prerequisites use their actual public owners. The
	// evaluator report below remains a report fixture, not execution proof.
	project := newGitRepo(t)
	files := testmodule.Snapshot(t, "", `package design

import . "goa.design/goa/v3/dsl"

var _ = API("orders", func() {})
var _ = Service("orders", func() {
	Method("health", func() {
		Result(String)
		HTTP(func() { GET("/health") })
	})
})
`)
	for name, contents := range files {
		file := filepath.Join(project, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
		require.NoError(t, os.WriteFile(file, []byte(contents), 0o600))
	}
	runTestGit(t, project, "config", "user.name", "Surla Test")
	runTestGit(t, project, "config", "user.email", "test@example.com")
	gitCommitAll(t, project, "Create the release approval project")
	cardsDir := t.TempDir()
	team := &workflowTeamFixture{}
	opts := controlplane.Options{BuildCacheDir: t.TempDir(),
		CardsDir: cardsDir, WorkspaceDir: project,
		Verification: &controlplane.VerificationConfig{Plan: verify.Plan{SuiteCommit: suiteCommit}},
		Team:         &controlplane.TeamBinding{Owner: "alice@example.com", Service: team},
	}
	cp, err := controlplane.New(opts)
	require.NoError(t, err)
	initial := cp
	t.Cleanup(func() { assert.NoError(t, initial.Close()) })
	refresh := mustRefreshStory(t, cp, alice)
	card, err := cp.ProposeChange(alice, &gensurla.ProposeChangePayload{
		ChangeID: "chg", Prompt: "Refunds up to $50 are instant.", Analysis: confirmedAnalysis(), StoryRefreshOpID: refresh.OpID,
	})
	require.NoError(t, err)
	card, err = cp.ConfirmIntent(alice, &gensurla.ConfirmIntentPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, Answers: map[string]string{}, Examples: card.Examples})
	require.NoError(t, err)
	started, err := cp.StartBuild(alice, &gensurla.StartBuildPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	assert.Equal(t, card.RequestRevision, started.Build.RequestRevision)
	assert.True(t, team.registered.Load())
	assert.Equal(t, gensurla.TeamKindUnavailable, started.Team.Kind(), "this host does not launch agents")
	builder := controlplane.WithPrincipal(t.Context(), controlplane.Principal{
		ID: "alice@example.com", CallerID: "builder", Kind: controlplane.CallerBuilder,
	})
	assignment, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	require.True(t, assignment.Read)
	require.NotEqual(t, project, assignment.WorkingFolder)
	require.NoError(t, os.WriteFile(filepath.Join(assignment.WorkingFolder, "release-notes.txt"),
		[]byte("The staged release is ready.\n"), 0o600))
	runTestGit(t, assignment.WorkingFolder, "add", "release-notes.txt")
	runTestGit(t, assignment.WorkingFolder, "commit", "-qm", "Prepare the staged release")
	commit := strings.TrimSpace(runTestGit(t, assignment.WorkingFolder, "rev-parse", "HEAD"))
	publish(t, release, commit, map[string]string{"orders": "api v1", "orders-worker": "worker v1"})
	require.NoError(t, cp.Close())
	report := saveReport(t, verify.Binding{
		CandidateCommit: commit, RequestRevision: card.RequestRevision, SuiteID: "orders", SuiteCommit: suiteCommit,
		Environment: "sim", Artifacts: map[string]string{"orders": digestOf("api v1"), "orders-worker": digestOf("worker v1")},
	}, true)
	opts.PromotionTarget = newReleaseTarget(t, release, filepath.Dir(report.path))
	cp, err = controlplane.New(opts)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cp.Close()) })

	approved, err := cp.ApproveMerge(alice, &gensurla.ApproveMergePayload{
		ChangeID: "chg", RequestRevision: card.RequestRevision, CandidateCommit: commit, ReportDigest: report.digest,
	})
	require.NoError(t, err)
	require.NotNil(t, approved.Approval)
	assert.Equal(t, digestOf("worker v1"), approved.Approval.Artifacts["orders-worker"])
	assert.Equal(t, gensurla.NewApprovalTestSourceEvaluator(&gensurla.OperatorEvaluatorTests{}), approved.Approval.TestSource)

	publish(t, release, commit, map[string]string{"orders": "api v1", "orders-worker": "worker v2"})
	got, err := cp.GetChange(alice, &gensurla.GetChangePayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Nil(t, got.Approval, "the staged binaries no longer match the approved ones")

	publish(t, release, commit, map[string]string{"orders": "api v1", "orders-worker": "worker v1"})
	got, err = cp.GetChange(alice, &gensurla.GetChangePayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Equal(t, approved.Approval, got.Approval, "the approval holds again for the approved binaries")
	assert.Nil(t, got.UnverifiedApproval)

	// Removing a configured binary or the staged commit directory causes the
	// real releaseTarget.CurrentArtifacts to fail; GetChange still returns the
	// card with UnverifiedApproval and a path-free reason, and ApproveMerge
	// refuses with a declared error naming the release folder basename and
	// recovery step without leaking the host path.
	require.NoError(t, os.Remove(filepath.Join(release, commit, "orders-worker")))
	got, err = cp.GetChange(alice, &gensurla.GetChangePayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Nil(t, got.Approval)
	require.NotNil(t, got.UnverifiedApproval)
	assert.Equal(t, approved.Approval, got.UnverifiedApproval.Approval)
	assert.Contains(t, got.UnverifiedApproval.Reason, "because the release binaries could not be read")
	assert.Contains(t, got.UnverifiedApproval.Reason, "Check current approval")
	assert.NotContains(t, got.UnverifiedApproval.Reason, release)

	require.NoError(t, os.RemoveAll(filepath.Join(release, commit)))
	got, err = cp.GetChange(alice, &gensurla.GetChangePayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Nil(t, got.Approval)
	require.NotNil(t, got.UnverifiedApproval)
	assert.Equal(t, approved.Approval, got.UnverifiedApproval.Approval)
	assert.Contains(t, got.UnverifiedApproval.Reason, "because the release binaries could not be read")
	assert.NotContains(t, got.UnverifiedApproval.Reason, release)

	_, err = cp.ApproveMerge(alice, &gensurla.ApproveMergePayload{
		ChangeID: "chg", RequestRevision: card.RequestRevision, CandidateCommit: commit, ReportDigest: report.digest,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "publish the binaries the run built, whose folder the run's Proof view shows, into the release folder that the promotion settings name, in a folder named after the commit, or remove the promotion settings")
	assert.NotContains(t, err.Error(), "/")
	assert.NotContains(t, err.Error(), release)
}

// ReadTeam returns this test host's empty roster without contacting a launcher.
func (f *workflowTeamFixture) ReadTeam(context.Context) (*gensurla.TeamSnapshot, error) {
	return &gensurla.TeamSnapshot{Team: "test-workflow", Roles: []*gensurla.TeamRoleState{}}, nil
}

// ShowPane refuses window work that this fixture does not own.
func (f *workflowTeamFixture) ShowPane(context.Context, string) error {
	return errors.New("the test host has no agent windows")
}

// StartAgents reports the actual unavailable host rather than fabricating work.
func (f *workflowTeamFixture) StartAgents(context.Context) (*gensurla.ProjectTeam, error) {
	return nil, errors.New("the test host does not launch agents")
}

// RegisterBuildRoles records admission through the existing public team seam.
func (f *workflowTeamFixture) RegisterBuildRoles(context.Context, controlplane.BuildFolders) error {
	f.registered.Store(true)
	return nil
}

// BindAssignments accepts the control plane's assignment reader before supervision starts.
func (f *workflowTeamFixture) BindAssignments(controlplane.AssignmentReader) {}

// Supervise ends with its owning control plane and starts no background worker.
func (f *workflowTeamFixture) Supervise(ctx context.Context, _ controlplane.AssignmentReader) error {
	<-ctx.Done()
	return nil
}

// confirmedAnalysis returns the analysis these tests propose: the
// instant-refund request with no open questions, so the card is confirmed at
// once, one deterministic example at the $50.00 limit, and no preserved
// behaviors.
func confirmedAnalysis() *gensurla.CP1Analysis {
	return &gensurla.CP1Analysis{
		Summary:     "Refunds up to $50 are instant.",
		Ambiguities: []*gensurla.CP1AnalysisAmbiguity{},
		Examples: []*gensurla.CP1AnalysisExample{{
			Scenario: "Refund at the limit", InputSummary: "Order total $50.00",
			ExpectedOutcome: "Instant store credit", VerificationKind: "deterministic",
		}},
		PreservedBehaviors: []string{},
	}
}

// newReleaseTarget loads a promotion configuration for the orders binaries
// with the given release and report directories.
func newReleaseTarget(t *testing.T, release, reports string) controlplane.PromotionTarget {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "promotion.json")
	content := `{"release_dir":"` + release + `","binaries":["orders","orders-worker"]}`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	target, err := storedPromotionTarget(t, path)
	require.NoError(t, err)
	target.(*releaseTarget).cfg.ReportDir = reports
	return target
}

// publish writes files into a new directory and renames it to
// <release>/<commit>, the way an operator publishes a release: an earlier
// directory for that commit is first renamed aside, complete, never changed
// in place.
func publish(t *testing.T, release, commit string, files map[string]string) {
	t.Helper()
	staged, err := os.MkdirTemp(release, "staging-")
	require.NoError(t, err)
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(staged, name), []byte(content), 0o700))
	}
	dst := filepath.Join(release, commit)
	if _, err := os.Stat(dst); err == nil {
		retired, err := os.MkdirTemp(release, "retired-")
		require.NoError(t, err)
		require.NoError(t, os.Rename(dst, filepath.Join(retired, commit)))
	}
	require.NoError(t, os.Rename(staged, dst))
}

// digestOf returns the hex SHA-256 digest of content.
func digestOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// storedPromotionTarget exercises named-file management and derives report storage at startup.
func storedPromotionTarget(t *testing.T, path string) (controlplane.PromotionTarget, error) {
	t.Helper()
	isolateServeWorkspace(t)
	if err := runProjectSettings("promotion", []string{"set", path}, io.Discard); err != nil {
		return nil, err
	}
	ws, err := configureServeWorkspace()
	return ws.promotionTarget, err
}
