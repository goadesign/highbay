// These tests drive base admission before a build can acquire a number/copy.
package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildAdmitsANewDraftFileAndRejectsLaterRemovalAndRevert demonstrates
// derived initial absence with actual draft/provenance/Build owners. The copy
// and team are the existing deterministic fixtures; no candidate is executed.
func TestBuildAdmitsANewDraftFileAndRejectsLaterRemovalAndRevert(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar A=0\n"))
	cp := newBuildControlPlane(t, Options{WorkspaceDir: project})
	readDraftForTest(t, cp)
	newFile := "design/added.go"
	text := "package design\n\nvar Added = 1\n"
	writeWorkspaceFiles(t, filepath.Join(project, draftFolderName), map[string]string{newFile: text})
	saveCompleteRemakeCard(t, cp, "chg")
	record := loadMetadataRecord(t, cp, "chg")
	assert.NotContains(t, record.madeFrom.Start.FolderVersions, newFile)
	_, explicit := enteredVersion(record.madeFrom.Entries[newFile], absentDesignVersion)
	assert.False(t, explicit, "the real revision writer does not manufacture explicit initial absence")
	card, err := cp.ConfirmIntent(asCaller("alice@example.com", CallerUser), &gensurla.ConfirmIntentPayload{ChangeID: "chg", RequestRevision: record.card.RequestRevision, Answers: map[string]string{}, Examples: record.card.Examples})
	require.NoError(t, err)
	started, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	assert.NotNil(t, started.Build)
	_, err = os.Stat(filepath.Join(project, newFile))
	assert.ErrorIs(t, err, os.ErrNotExist, "building does not write the guide's design into the user's folder")
	immutable := loadMetadataRecord(t, cp, "chg").madeFrom
	writeWorkspaceFiles(t, project, map[string]string{newFile: text})
	commitProjectDesign(t, project, newFile)
	require.NoError(t, os.Remove(filepath.Join(project, newFile)))
	commitProjectDesign(t, project, newFile)
	head := strings.TrimSpace(gitForLayoutTest(t, project, "rev-parse", "HEAD"))
	assert.ErrorContains(t, checkDesignBase(t.Context(), project, head, immutable), "changed outside Surla")
	_, err = cp.UpdateDraft(asCaller("alice@example.com", CallerGuide))
	require.NoError(t, err)
	list, _, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	removed := madeFromSnapshot{Start: list.Start, Entries: list.Entries}
	assert.NoError(t, checkDesignBase(t.Context(), project, head, &removed))
	// Reverting that deletion is a later committed return to the older
	// listed text; the explicit absence entry must retain its newer order.
	writeWorkspaceFiles(t, project, map[string]string{newFile: text})
	commitProjectDesign(t, project, newFile)
	head = strings.TrimSpace(gitForLayoutTest(t, project, "rev-parse", "HEAD"))
	assert.Error(t, checkDesignBase(t.Context(), project, head, &removed))
	_, err = cp.UpdateDraft(asCaller("alice@example.com", CallerGuide))
	require.NoError(t, err)
	list, _, err = cp.cards.loadMadeFrom()
	require.NoError(t, err)
	assert.NoError(t, checkDesignBase(t.Context(), project, head, &madeFromSnapshot{Start: list.Start, Entries: list.Entries}))
}

// TestBuildBaseRefusalHasNoStartEffects proves an outside commit is rejected
// before identity checks, preparing either copy, or saving an open build.
func TestBuildBaseRefusalHasNoStartEffects(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := buildableChange(t, cp, "chg")
	writeWorkspaceFiles(t, cp.activeWorkspaceDir, map[string]string{"design/outside.go": "package design\nvar Outside=1\n"})
	commitProjectDesign(t, cp.activeWorkspaceDir, "design/outside.go")
	identityCalled := false
	cp.buildIdentityCheck = func(context.Context, string) error { identityCalled = true; return nil }
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	require.Error(t, err)
	assert.ErrorContains(t, err, "design/outside.go")
	assert.False(t, identityCalled)
	assertNoBuildSaved(t, cp, "chg")
	assert.Empty(t, strings.TrimSpace(gitForLayoutTest(t, cp.activeWorkspaceDir, "for-each-ref", "--format=%(refname)", "refs/heads/surla/")))
}

// TestBuildRechecksTheSameHashProvenance prevents a metadata-only update from
// replacing the immutable group that passed the base check during preflight.
func TestBuildRechecksTheSameHashProvenance(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := buildableChange(t, cp, "chg")
	cp.buildIdentityCheck = func(context.Context, string) error {
		record := loadMetadataRecord(t, cp, "chg")
		record.madeFrom.Entries["design/unrelated.go"] = []madeFromVersion{{Version: strings.Repeat("e", 64), Entered: 1}}
		saveMetadataRecord(t, cp, record)
		return nil
	}
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	require.Error(t, err)
	assert.ErrorContains(t, err, "provenance changed")
	assertNoBuildSaved(t, cp, "chg")
}

// TestBuildLegacyCardNamesTheGuideStep preserves old storage decoding while
// refusing to build a card that cannot establish its design's provenance.
func TestBuildLegacyCardNamesTheGuideStep(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := confirmedChange(t, cp, "chg")
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	require.Error(t, err)
	assert.ErrorContains(t, err, "records no design")
	assert.ErrorContains(t, err, "guide to propose a revision from its draft")
	assertNoBuildSaved(t, cp, "chg")
}

// commitProjectDesign commits only this test's named project paths.
func commitProjectDesign(t *testing.T, dir string, files ...string) {
	t.Helper()
	gitForLayoutTest(t, dir, append([]string{"add", "--"}, files...)...)
	gitForLayoutTest(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "Project design change")
}
