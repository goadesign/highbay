// This file checks the saved-operation boundary with explicit evaluation
// fixtures. Native workspace journeys use the real refresh fixture separately.
package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/designversion"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestAdoptionKeepsOnlyItsOwnSnapshotHistory fixes the A/op-B/intervening-C
// counterexample: the card holds A/B, while the live list retains A/C/B.
func TestAdoptionKeepsOnlyItsOwnSnapshotHistory(t *testing.T) {
	project := t.TempDir()
	const file = "design/design.go"
	a := "package design\n\nconst Version = \"A\"\n"
	b := "package design\n\nconst Version = \"B\"\n"
	c := "package design\n\nconst Version = \"C\"\n"
	writeWorkspaceFiles(t, project, validWorkspaceStub(a))
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	_, initial := readDraftForTest(t, cp)
	writeWorkspaceFiles(t, filepath.Join(project, draftFolderName), map[string]string{file: b})
	opID := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, sampleAdoptedStories(), "")
	selected, err := cp.adoptStoryRefresh(Principal{ID: "alice@example.com"}, opID)
	require.NoError(t, err)
	selectedHistory := cloneMadeFromSnapshot(selected.madeFrom)
	cp.draftMu.Lock()
	func() {
		defer cp.draftMu.Unlock()
		live, _, err := cp.cards.loadMadeFrom()
		require.NoError(t, err)
		live.Write++
		_, err = cp.enterDraftVersion(&live, file, map[string]string{file: c})
		require.NoError(t, err)
		require.NoError(t, cp.cards.saveMadeFrom(live))
	}()
	// Neither folder remains readable as a design. Adoption still selects B.
	writeWorkspaceFiles(t, project, map[string]string{"go.mod": "broken", file: "not Go"})
	writeWorkspaceFiles(t, filepath.Join(project, draftFolderName), map[string]string{file: "not Go"})
	card, err := cp.ProposeChange(asCaller("alice@example.com", CallerGuide), &genhighbay.ProposeChangePayload{
		ChangeID: "selected-b", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"),
		StoryRefreshOpID: &opID,
	})
	require.NoError(t, err)
	saved := loadMetadataRecord(t, cp, card.ChangeID)
	assert.Equal(t, selected.designSource, saved.designSource)
	assert.Equal(t, selected.stories, saved.stories)
	assert.Equal(t, selected.architecture, saved.architecture)
	assert.Equal(t, selected.goMod, saved.goMod)
	assert.Equal(t, initial.Start, saved.madeFrom.Start)
	assert.Equal(t, []madeFromVersion{
		{Version: designversion.Digest(a), Entered: 1}, {Version: designversion.Digest(b), Entered: 3},
	}, saved.madeFrom.Entries[file])
	live, _, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	assert.Equal(t, []madeFromVersion{
		{Version: designversion.Digest(a), Entered: 1}, {Version: designversion.Digest(c), Entered: 2},
		{Version: designversion.Digest(b), Entered: 3},
	}, live.Entries[file])
	again, err := cp.adoptStoryRefresh(Principal{ID: "alice@example.com"}, opID)
	require.NoError(t, err)
	assert.Equal(t, selectedHistory, again.madeFrom, "saving cannot rewrite the retained operation")
}

// TestSnapshotStorageFailureLeavesTheCardAbsent exercises the actual list
// writer failure and records truthful failure telemetry on the owning operation.
func TestSnapshotStorageFailureLeavesTheCardAbsent(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	cp := newControlPlane(t, Options{WorkspaceDir: project, TracerProvider: provider})
	opID := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
	before, err := os.ReadFile(filepath.Join(cp.cards.dir, madeFromFileName))
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(cp.cards.dir, madeFromStagingFile), 0o700))
	_, err = cp.ProposeChange(asCaller("alice@example.com", CallerGuide), &genhighbay.ProposeChangePayload{
		ChangeID: "chg", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"), StoryRefreshOpID: &opID,
	})
	require.Error(t, err)
	_, exists, err := cp.cards.load("chg")
	require.NoError(t, err)
	assert.False(t, exists)
	after, err := os.ReadFile(filepath.Join(cp.cards.dir, madeFromFileName))
	require.NoError(t, err)
	assert.Equal(t, before, after)
	spans := exporter.GetSpans()
	require.NotEmpty(t, spans)
	operation := spans[len(spans)-1]
	assert.Equal(t, "highbay.controlplane.propose_change", operation.Name)
	assert.Equal(t, codes.Error, operation.Status.Code)
	assert.NotEmpty(t, operation.Events)
}

// TestADropBetweenSelectionAndSaveRejectsTheSnapshot uses the existing tracing
// seam after op selection, before the owner locks and writes either file.
func TestADropBetweenSelectionAndSaveRejectsTheSnapshot(t *testing.T) {
	for _, operation := range []string{"propose_change", "revise_change"} {
		t.Run(operation, func(t *testing.T) {
			project := t.TempDir()
			writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
			hold := &spanHold{name: "highbay.controlplane." + operation, started: make(chan struct{}), release: make(chan struct{})}
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(hold))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
			cp := newControlPlane(t, Options{WorkspaceDir: project, TracerProvider: provider})
			guide := asCaller("alice@example.com", CallerGuide)
			opID := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
			var card *genhighbay.ChangeCardResult
			var before []byte
			if operation == "revise_change" {
				var err error
				card, err = cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
					ChangeID: "chg", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"), StoryRefreshOpID: &opID,
				})
				require.NoError(t, err)
				before, err = os.ReadFile(cp.cards.path("chg"))
				require.NoError(t, err)
				opID = seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
			}
			hold.armed.Store(true)
			done := hold.run(t, func() error {
				if operation == "propose_change" {
					_, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
						ChangeID: "chg", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"), StoryRefreshOpID: &opID,
					})
					return err
				}
				payload := revisionPayload("chg", card.RequestRevision, "Keep refunds bounded with review", card.Examples[0].ID)
				payload.StoryRefreshOpID = &opID
				_, err := cp.ReviseChange(guide, payload)
				return err
			})
			publishDroppedDraftStartForTest(t, cp)
			afterDrop, err := os.ReadFile(filepath.Join(cp.cards.dir, madeFromFileName))
			require.NoError(t, err)
			hold.releaseOnce()
			assertServiceError(t, awaitHeld(t, done), "drafted_before_drop")
			after, err := os.ReadFile(filepath.Join(cp.cards.dir, madeFromFileName))
			require.NoError(t, err)
			assert.Equal(t, afterDrop, after, "a rejected op enters no old version")
			if operation == "propose_change" {
				_, exists, err := cp.cards.load("chg")
				require.NoError(t, err)
				assert.False(t, exists)
			} else {
				after, err := os.ReadFile(cp.cards.path("chg"))
				require.NoError(t, err)
				assert.Equal(t, before, after)
			}
		})
	}
}

// TestNoOpRevisionAfterDropPreservesItsEntireSavedSnapshot includes zero
// Stories and proves no dropped design re-enters the new live generation.
func TestNoOpRevisionAfterDropPreservesItsEntireSavedSnapshot(t *testing.T) {
	for _, withStories := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero Stories", true: "Stories"}[withStories], func(t *testing.T) {
			project := t.TempDir()
			writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
			cp := newControlPlane(t, Options{WorkspaceDir: project})
			stories := sampleAdoptedStories()
			if !withStories {
				stories = nil
			}
			guide := asCaller("alice@example.com", CallerGuide)
			opID := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, stories, "")
			card, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
				ChangeID: "chg", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"), StoryRefreshOpID: &opID,
			})
			require.NoError(t, err)
			before := loadMetadataRecord(t, cp, "chg")
			publishDroppedDraftStartForTest(t, cp)
			drop, err := os.ReadFile(filepath.Join(cp.cards.dir, madeFromFileName))
			require.NoError(t, err)
			_, err = cp.ReviseChange(guide, revisionPayload("chg", card.RequestRevision, "Keep refunds bounded with review", card.Examples[0].ID))
			require.NoError(t, err)
			after := loadMetadataRecord(t, cp, "chg")
			assert.Equal(t, before.designSource, after.designSource)
			assert.Equal(t, before.stories, after.stories)
			assert.Equal(t, before.architecture, after.architecture)
			assert.Equal(t, before.goMod, after.goMod)
			assert.Equal(t, before.madeFrom, after.madeFrom)
			afterDrop, err := os.ReadFile(filepath.Join(cp.cards.dir, madeFromFileName))
			require.NoError(t, err)
			assert.Equal(t, drop, afterDrop)
		})
	}
}

// TestNoOpRevisionKeepsAConcurrentMetadataOnlyRefresh covers the same-hash
// window: module/architecture/history are deliberately outside request_revision.
func TestNoOpRevisionKeepsAConcurrentMetadataOnlyRefresh(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
	hold := &spanHold{name: "highbay.controlplane.revise_change", started: make(chan struct{}), release: make(chan struct{})}
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(hold))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	cp := newControlPlane(t, Options{WorkspaceDir: project, TracerProvider: provider})
	guide := asCaller("alice@example.com", CallerGuide)
	opA := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
	card, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID: "chg", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"), StoryRefreshOpID: &opA,
	})
	require.NoError(t, err)
	old := loadMetadataRecord(t, cp, "chg")
	writeWorkspaceFiles(t, filepath.Join(project, draftFolderName), map[string]string{
		"go.mod": old.goMod + "\n// newer module snapshot\n",
	})
	opB := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
	payload := func(op *string) *genhighbay.ReviseChangePayload {
		prior := card.Examples[0]
		return &genhighbay.ReviseChangePayload{
			ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, Prompt: card.Prompt, RevisionNote: "Revise the change.", StoryRefreshOpID: op,
			Analysis: &genhighbay.CP1RevisionAnalysis{
				Summary: card.Summary, Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{}, RemovedExampleIds: []string{},
				PreservedBehaviors: card.PreservedBehaviors,
				Examples: []*genhighbay.CP1RevisionAnalysisExample{{
					PriorID: &prior.ID, Scenario: prior.Scenario, InputSummary: prior.InputSummary,
					ExpectedOutcome: prior.ExpectedOutcome, VerificationKind: prior.VerificationKind,
				}},
			},
		}
	}
	hold.armed.Store(true)
	done := hold.run(t, func() error {
		_, err := cp.ReviseChange(guide, payload(nil))
		return err
	})
	newer, err := cp.ReviseChange(guide, payload(&opB))
	require.NoError(t, err)
	assert.Equal(t, card.RequestRevision, newer.RequestRevision, "only metadata changed")
	selected := loadMetadataRecord(t, cp, "chg")
	assert.NotEqual(t, old.goMod, selected.goMod)
	liveBefore, err := os.ReadFile(filepath.Join(cp.cards.dir, madeFromFileName))
	require.NoError(t, err)
	hold.releaseOnce()
	require.NoError(t, awaitHeld(t, done))
	kept := loadMetadataRecord(t, cp, "chg")
	assert.Equal(t, selected.designSource, kept.designSource)
	assert.Equal(t, selected.stories, kept.stories)
	assert.Equal(t, selected.architecture, kept.architecture)
	assert.Equal(t, selected.goMod, kept.goMod)
	assert.Equal(t, selected.madeFrom, kept.madeFrom)
	liveAfter, err := os.ReadFile(filepath.Join(cp.cards.dir, madeFromFileName))
	require.NoError(t, err)
	assert.Equal(t, liveBefore, liveAfter, "omission never re-enters a design")
}

// TestSavingACurrentRevisionClearsTheDropNote checks both a running server's
// presentation and the first refresh installed after its card store reopens.
func TestSavingACurrentRevisionClearsTheDropNote(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
	opts := Options{WorkspaceDir: project, CardsDir: t.TempDir()}
	cp := newControlPlane(t, opts)
	readDraftForTest(t, cp)
	publishDroppedDraftStartForTest(t, cp)
	opID := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
	cp.storyRefreshMu.Lock()
	for _, op := range cp.storyRefreshes {
		op.dropNote = true
	}
	cp.storyRefreshMu.Unlock()
	guide := asCaller("alice@example.com", CallerGuide)
	before, err := cp.GetStoryRefresh(guide)
	require.NoError(t, err)
	assert.Contains(t, before.Message, "user dropped change")
	_, err = cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID: "chg", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"), StoryRefreshOpID: &opID,
	})
	require.NoError(t, err)
	after, err := cp.GetStoryRefresh(guide)
	require.NoError(t, err)
	assert.NotContains(t, after.Message, "user dropped change")
	require.NoError(t, cp.Close())
	reopened := newControlPlane(t, opts)
	installFakeGo(t, "#!/bin/sh\nexit 1\n")
	started, err := reopened.RefreshStory(guide)
	require.NoError(t, err)
	assert.NotContains(t, started.Message, "user dropped change")
}

// publishDroppedDraftStartForTest models Drop's sole durable write. B2 owns
// the public Drop journey; these tests exercise consumers of its persisted fact.
func publishDroppedDraftStartForTest(t *testing.T, cp *ControlPlane) {
	t.Helper()
	cp.draftMu.Lock()
	defer cp.draftMu.Unlock()
	cp.mu.Lock()
	defer cp.mu.Unlock()
	list, exists, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	require.True(t, exists)
	list.Write++
	list.Start = madeFromStart{Write: list.Write, Dropped: &droppedBuild{
		ChangeID: "prior", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Number: 1,
	}}
	list.Entries, list.Taken, list.Pending = nil, nil, nil
	require.NoError(t, cp.cards.saveMadeFrom(list))
}

// TestMissingRefreshOpCannotReadAProjectDesign keeps the module gate cheap and
// permits legacy/no-Story saved designs to survive ordinary text revisions.
func TestMissingRefreshOpCannotReadAProjectDesign(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, map[string]string{"go.mod": "broken module", "design/design.go": "not Go"})
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	guide := asCaller("alice@example.com", CallerGuide)
	_, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID: "new", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"),
	})
	assertServiceError(t, err, "unprocessable")
	assert.ErrorContains(t, err, "story_refresh_op_id is required")
	legacy := saveProposedCardFixture(t, cp, "alice@example.com", "legacy", "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
	_, err = cp.ReviseChange(guide, revisionPayload("legacy", legacy.RequestRevision, "Review refunds", legacy.Examples[0].ID))
	assert.ErrorContains(t, err, "story_refresh_op_id is required")
	record := loadMetadataRecord(t, cp, "legacy")
	record.designSource = `{"design/design.go":"package design\n"}`
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
	require.NoError(t, err)
	saveMetadataRecord(t, cp, record)
	_, err = cp.ReviseChange(guide, revisionPayload("legacy", record.card.RequestRevision, "Review refunds", record.card.Examples[0].ID))
	require.NoError(t, err)
	kept := loadMetadataRecord(t, cp, "legacy")
	assert.Equal(t, record.designSource, kept.designSource)
	assert.Nil(t, kept.madeFrom)
	assert.Empty(t, strings.TrimSpace(kept.goMod))
}
