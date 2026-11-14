// This file exercises merge detection and durable drop decisions with real Git.
package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/scaffold"
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

// TestDropBuildPersistsAndRejectsRepeatedDrop proves the list rename survives
// a new control-plane read and removes worker assignments without deleting work.
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
