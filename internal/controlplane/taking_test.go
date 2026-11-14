// This file exercises merge detection and durable drop decisions with real Git.
package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/scaffold"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommitTakenRecognizesSquashBeforeLaterEdit checks the historical tree,
// not only the current tree, while rejecting a different project edit.
func TestCommitTakenRecognizesSquashBeforeLaterEdit(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Verification: verificationConfig(t)})
	workspace, _, _ := cp.currentWorkspaceTarget()
	base := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "candidate-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "candidate.txt"), []byte("candidate\n"), 0o600))
	productionBuildGit(t, workspace, "add", "candidate.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Candidate")
	candidate := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	got, err := commitTaken(t.Context(), workspace, candidate, base)
	require.NoError(t, err)
	assert.False(t, got)
	productionBuildGit(t, workspace, "merge", "--squash", "candidate-fixture")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Take candidate")
	squash := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	got, err = commitTaken(t.Context(), workspace, candidate, squash)
	require.NoError(t, err)
	assert.True(t, got)
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "candidate.txt"), []byte("later edit\n"), 0o600))
	productionBuildGit(t, workspace, "add", "candidate.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Edit candidate")
	tip := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	got, err = commitTaken(t.Context(), workspace, candidate, tip)
	require.NoError(t, err)
	assert.True(t, got)
}

// TestDropBuildPersistsAndRejectsRepeatedDrop checks the Drop result and
// subsequent reads, including worker assignments and a repeated request.
func TestDropBuildPersistsAndRejectsRepeatedDrop(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Verification: verificationConfig(t)})
	card := buildableChange(t, cp, "x")
	user := asCaller("alice@example.com", CallerUser)
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	payload := &gensurla.DropBuildPayload{ChangeID: "x", StartedAt: first.Build.StartedAt}
	dropped, err := cp.DropBuild(user, payload)
	require.NoError(t, err)
	start, ok := dropped.Build.AsStarted()
	require.True(t, ok)
	detail, ok := start.Taking.AsDropped()
	require.True(t, ok)
	assert.Empty(t, detail.KeptBranches)
	read, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	start, ok = read.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, gensurla.BuildTakingKindDropped, start.Taking.Kind())
	_, err = cp.DropBuild(user, payload)
	assert.Error(t, err)
	assignments, err := cp.ListBuilderAssignments(user)
	require.NoError(t, err)
	assert.Empty(t, assignments.Assignments)
}

// TestDropRefusesStalePageKeepsTheOpenBuild ensures a stale identity never
// changes the provenance, and gives the current start back to the page.
func TestDropRefusesStalePageKeepsTheOpenBuild(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Verification: verificationConfig(t)})
	card := buildableChange(t, cp, "x")
	user := asCaller("alice@example.com", CallerUser)
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "x", StartedAt: "2000-01-01T00:00:00Z"})
	var refusal *gensurla.OpenBuildRefusal
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "build_changed", refusal.Name)
	assert.Equal(t, first.Build.StartedAt, refusal.Build.StartedAt)
	read, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	start, ok := read.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, gensurla.BuildTakingKindBuilding, start.Taking.Kind())
}

// TestDropDuringStartPreservesBuild proves Drop can interleave with the copy
// preflight, and the second admission check refuses without saving a build.
func TestDropDuringStartPreservesBuild(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := buildableChange(t, cp, "x")
	user := asCaller("alice@example.com", CallerUser)
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	before := readBuildFile(t, cp, "x")
	entered, release := make(chan struct{}), make(chan struct{})
	factory := cp.buildCopyFactory
	cp.buildCopyFactory = func(ctx context.Context, folder string, layout scaffold.Layout) (buildCopy, error) {
		close(entered)
		select {
		case <-release:
			return factory(ctx, folder, layout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	result := make(chan error, 1)
	go func() {
		_, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision, Replaces: &gensurla.ReplacedBuild{ChangeID: "x", StartedAt: first.Build.StartedAt}})
		result <- err
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("start did not reach copy preflight")
	}
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "x", StartedAt: first.Build.StartedAt})
	close(release)
	require.NoError(t, err)
	select {
	case err := <-result:
		assert.Error(t, err)
	case <-time.After(20 * time.Second):
		t.Fatal("start did not finish")
	}
	assert.Equal(t, before, readBuildFile(t, cp, "x"))
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	assertRefused(t, err, "drafted_before_drop")
}

// TestBuildReadIgnoresExternalDiff proves repository diff configuration cannot
// execute a program or turn a different tree into a merged build.
func TestBuildReadIgnoresExternalDiff(t *testing.T) {
	cfg := verificationConfig(t)
	cp := newBuildControlPlane(t, Options{Verification: cfg})
	card := buildableChange(t, cp, "x")
	user := asCaller("alice@example.com", CallerUser)
	_, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	cp.mu.RLock()
	saved, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	workspace, _, _ := cp.currentWorkspaceTarget()
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "candidate-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "candidate.txt"), []byte("candidate\n"), 0o600))
	productionBuildGit(t, workspace, "add", "candidate.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Candidate")
	candidate := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "candidate.txt"), []byte("different\n"), 0o600))
	productionBuildGit(t, workspace, "add", "candidate.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Different work")
	marker := filepath.Join(t.TempDir(), "executed")
	script := filepath.Join(t.TempDir(), "diff-tool")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 0\n"), 0o700))
	productionBuildGit(t, workspace, "config", "diff.external", script)
	productionBuildGit(t, workspace, "config", "diff.trustExitCode", "true")
	writeListedRunAt(t, cfg.RunsDir, "alice@example.com", card, strings.Repeat("a", 32), saved.StartedAt.Add(time.Nanosecond), true, candidate)
	read, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	started, ok := read.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, gensurla.BuildTakingKindPassed, started.Taking.Kind())
	_, err = os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestDroppedBuildReportsCurrentKeptBranches observes deleted and newly
// created refs without changing the immutable tips captured by Drop.
func TestDroppedBuildReportsCurrentKeptBranches(t *testing.T) {
	cfg := verificationConfig(t)
	cp := newBuildControlPlane(t, Options{Verification: cfg})
	card := buildableChange(t, cp, "x")
	user := asCaller("alice@example.com", CallerUser)
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	workspace, _, _ := cp.currentWorkspaceTarget()
	productionBuildGit(t, workspace, "branch", "surla/build-1")
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "x", StartedAt: first.Build.StartedAt})
	require.NoError(t, err)
	provenance, found, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	require.True(t, found)
	require.Contains(t, provenance.Start.Dropped.BranchTips, "surla/build-1")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "work.txt"), []byte("work\n"), 0o600))
	productionBuildGit(t, workspace, "add", "work.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Work")
	productionBuildGit(t, workspace, "branch", "--force", "surla/build-1", "HEAD")
	read, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	started, ok := read.Build.AsStarted()
	require.True(t, ok)
	dropped, ok := started.Taking.AsDropped()
	require.True(t, ok)
	assert.Equal(t, []string{"surla/build-1"}, dropped.KeptBranches)
	productionBuildGit(t, workspace, "branch", "--delete", "--force", "surla/build-1")
	read, err = cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	started, ok = read.Build.AsStarted()
	require.True(t, ok)
	dropped, ok = started.Taking.AsDropped()
	require.True(t, ok)
	assert.Empty(t, dropped.KeptBranches)
}

// TestWrittenRunNeedsBothCommits uses independent candidate and tests commits;
// taking tests alone is insufficient, while separately squashing both works.
func TestWrittenRunNeedsBothCommits(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Verification: verificationConfig(t)})
	workspace, _, _ := cp.currentWorkspaceTarget()
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "candidate-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "candidate.txt"), []byte("candidate\n"), 0o600))
	productionBuildGit(t, workspace, "add", "candidate.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Candidate")
	candidate := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "tests-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "tests.txt"), []byte("tests\n"), 0o600))
	productionBuildGit(t, workspace, "add", "tests.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Tests")
	tests := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	run := verifyrun.Run{Request: verifyrun.Request{Plan: verify.Plan{CandidateCommit: candidate, SuiteCommit: tests, Tests: verify.TestsWritten}}}
	productionBuildGit(t, workspace, "merge", "--squash", "tests-fixture")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Take tests")
	project := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	got, err := runTaken(t.Context(), workspace, project, run)
	require.NoError(t, err)
	assert.False(t, got)
	productionBuildGit(t, workspace, "merge", "--squash", "candidate-fixture")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Take candidate")
	project = productionBuildGit(t, workspace, "rev-parse", "HEAD")
	got, err = runTaken(t.Context(), workspace, project, run)
	require.NoError(t, err)
	assert.True(t, got)
}

// TestOlderPassedRunCanEstablishMerged keeps the newest pass for display but
// lets any eligible pass prove that the user's branch took the build.
func TestOlderPassedRunCanEstablishMerged(t *testing.T) {
	cfg := verificationConfig(t)
	cp := newBuildControlPlane(t, Options{Verification: cfg})
	card := buildableChange(t, cp, "x")
	y := buildableChange(t, cp, "y")
	user := asCaller("alice@example.com", CallerUser)
	_, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	cp.mu.RLock()
	saved, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	workspace, _, _ := cp.currentWorkspaceTarget()
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "surla/build-1")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "old.txt"), []byte("old\n"), 0o600))
	productionBuildGit(t, workspace, "add", "old.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Old candidate")
	old := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "new-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("new\n"), 0o600))
	productionBuildGit(t, workspace, "add", "new.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "New candidate")
	newer := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	writeListedRunAt(t, cfg.RunsDir, "alice@example.com", card, strings.Repeat("a", 32), saved.StartedAt.Add(time.Second), true, old)
	writeListedRunAt(t, cfg.RunsDir, "alice@example.com", card, strings.Repeat("b", 32), saved.StartedAt.Add(2*time.Second), true, newer)
	read, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	started, ok := read.Build.AsStarted()
	require.True(t, ok)
	passed, ok := started.Taking.AsPassed()
	require.True(t, ok)
	assert.Equal(t, newer, passed.CandidateCommit)
	productionBuildGit(t, workspace, "merge", "--quiet", "--no-ff", "-m", "Take old candidate", old)
	read, err = cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	started, ok = read.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, gensurla.BuildTakingKindMerged, started.Taking.Kind())
	project := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "y", RequestRevision: y.RequestRevision})
	require.NoError(t, err)
	cp.mu.RLock()
	next, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 2, next.Number)
	assert.Equal(t, project, next.Base)
}

// TestDropGuidanceSeesMergeOnlyChanges checks history paths introduced by a
// merge, even when no ordinary first-parent commit changed that file.
func TestDropGuidanceSeesMergeOnlyChanges(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Verification: verificationConfig(t)})
	workspace, _, _ := cp.currentWorkspaceTarget()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("base\n"), 0o600))
	productionBuildGit(t, workspace, "add", "shared.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Base")
	base := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "candidate-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("candidate\n"), 0o600))
	productionBuildGit(t, workspace, "add", "shared.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Candidate")
	candidate := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "side-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "side.txt"), []byte("side\n"), 0o600))
	productionBuildGit(t, workspace, "add", "side.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Side")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	productionBuildGit(t, workspace, "merge", "--quiet", "--no-ff", "--no-commit", "side-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "shared.txt"), []byte("merge resolution\n"), 0o600))
	productionBuildGit(t, workspace, "add", "shared.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Merge side with resolution")
	project := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	safe, err := dropSafeGuidance(t.Context(), workspace, base, project, candidate)
	require.NoError(t, err)
	assert.True(t, safe)
}

// TestDropBeforeAnyAssignmentAdvancesTheNextNumber preserves the saved number
// even when no worker ever created a numbered branch.
func TestDropBeforeAnyAssignmentAdvancesTheNextNumber(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Verification: verificationConfig(t)})
	card := buildableChange(t, cp, "x")
	y := buildableChange(t, cp, "y")
	user := asCaller("alice@example.com", CallerUser)
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	dropped, err := cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "x", StartedAt: first.Build.StartedAt})
	require.NoError(t, err)
	started, ok := dropped.Build.AsStarted()
	require.True(t, ok)
	info, ok := started.Taking.AsDropped()
	require.True(t, ok)
	assert.Empty(t, info.KeptBranches)
	provenance, found, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	require.True(t, found)
	record := loadMetadataRecord(t, cp, "y")
	record.madeFrom.Start = provenance.Start
	record.madeFrom.Entries = nil
	saveMetadataRecord(t, cp, record)
	second, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "y", RequestRevision: y.RequestRevision})
	require.NoError(t, err)
	assert.NotEqual(t, first.Build.StartedAt, second.Build.StartedAt)
	cp.mu.RLock()
	saved, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, 2, saved.Number)
}

// TestDropSurvivesOwnerRestart reloads the Drop record without another save.
func TestDropSurvivesOwnerRestart(t *testing.T) {
	cfg := verificationConfig(t)
	cp := newBuildControlPlane(t, Options{Verification: cfg})
	card := buildableChange(t, cp, "x")
	user := asCaller("alice@example.com", CallerUser)
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "x", StartedAt: first.Build.StartedAt})
	require.NoError(t, err)
	path := filepath.Join(cp.cards.dir, madeFromFileName)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	cardsDir := cp.cards.dir
	workspace, _, _ := cp.currentWorkspaceTarget()
	require.NoError(t, cp.Close())
	reopened := newBuildControlPlane(t, Options{CardsDir: cardsDir, WorkspaceDir: workspace, Verification: cfg})
	read, err := reopened.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	started, ok := read.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, gensurla.BuildTakingKindDropped, started.Taking.Kind())
	assignments, err := reopened.ListBuilderAssignments(user)
	require.NoError(t, err)
	assert.Empty(t, assignments.Assignments)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// TestDropAcceptsMergedBuild keeps Drop available even when the project took it.
func TestDropAcceptsMergedBuild(t *testing.T) {
	cfg := verificationConfig(t)
	cp := newBuildControlPlane(t, Options{Verification: cfg})
	card := buildableChange(t, cp, "x")
	user := asCaller("alice@example.com", CallerUser)
	first, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	cp.mu.RLock()
	saved, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	workspace, _, _ := cp.currentWorkspaceTarget()
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "surla/build-1")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "merged.txt"), []byte("merged\n"), 0o600))
	productionBuildGit(t, workspace, "add", "merged.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Candidate")
	candidate := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	productionBuildGit(t, workspace, "merge", "--quiet", "--no-ff", "-m", "Take candidate", candidate)
	writeListedRunAt(t, cfg.RunsDir, "alice@example.com", card, strings.Repeat("a", 32), saved.StartedAt.Add(time.Second), true, candidate)
	read, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	started, ok := read.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, gensurla.BuildTakingKindMerged, started.Taking.Kind())
	dropped, err := cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: "x", StartedAt: first.Build.StartedAt})
	require.NoError(t, err)
	started, ok = dropped.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, gensurla.BuildTakingKindDropped, started.Taking.Kind())
}
