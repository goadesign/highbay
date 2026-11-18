// This file checks metadata persistence and mechanical revision transitions.
// Test snapshots enter through storage; later slices own refresh producers.
package controlplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// TestRevisionMetadataSurvivesReload keeps older cards valid and verifies that
// complete snapshots survive an actual store close and reopen outside the hash.
func TestRevisionMetadataSurvivesReload(t *testing.T) {
	for _, withMetadata := range []bool{false, true} {
		t.Run(map[bool]string{false: "older card", true: "snapshot card"}[withMetadata], func(t *testing.T) {
			opts := Options{CardsDir: t.TempDir()}
			cp := newControlPlane(t, opts)
			card := mustPropose(t, cp, asCaller("alice@example.com", CallerGuide),
				"chg", "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
			record := loadMetadataRecord(t, cp, "chg")
			if withMetadata {
				record.architecture, record.goMod, record.madeFrom = metadataFixture("a")
			}
			saveMetadataRecord(t, cp, record)
			raw, err := os.ReadFile(cp.cards.path("chg"))
			require.NoError(t, err)
			var saved map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &saved))
			assert.Equal(t, withMetadata, saved["architecture"] != nil)
			assert.Equal(t, withMetadata, saved["go_mod"] != nil)
			assert.Equal(t, withMetadata, saved["made_from"] != nil)
			var content map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(saved["card"], &content))
			for _, field := range []string{"architecture", "go_mod", "made_from"} {
				assert.NotContains(t, content, field)
			}
			require.NoError(t, cp.Close())
			reopened := newControlPlane(t, opts)
			loaded := loadMetadataRecord(t, reopened, "chg")
			assert.Equal(t, card.RequestRevision, loaded.card.RequestRevision)
			assert.Equal(t, record.architecture, loaded.architecture)
			assert.Equal(t, record.goMod, loaded.goMod)
			assert.Equal(t, record.madeFrom, loaded.madeFrom)
			calculated, err := computeCanonicalRevision(loaded.card, loaded.owner, loaded.designSource)
			require.NoError(t, err)
			assert.Equal(t, card.RequestRevision, calculated)
		})
	}
}

// TestRevisionMetadataStrictDecode rejects partial or malformed metadata while
// retaining Story-only empty topology as a valid saved evaluation.
func TestRevisionMetadataStrictDecode(t *testing.T) {
	cp := newControlPlane(t, Options{})
	mustPropose(t, cp, asCaller("alice@example.com", CallerGuide),
		"chg", "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
	record := loadMetadataRecord(t, cp, "chg")
	record.architecture, record.goMod, record.madeFrom = metadataFixture("a")
	saveMetadataRecord(t, cp, record)
	raw, err := os.ReadFile(cp.cards.path("chg"))
	require.NoError(t, err)
	cases := []struct {
		name, want string
		change     func(map[string]any)
	}{
		{name: "missing architecture", want: "must be present together", change: func(v map[string]any) { delete(v, "architecture") }},
		{name: "missing module", want: "must be present together", change: func(v map[string]any) { delete(v, "go_mod") }},
		{name: "missing provenance", want: "must be present together", change: func(v map[string]any) { delete(v, "made_from") }},
		{name: "invalid digest", want: "invalid design digest", change: func(v map[string]any) {
			v["architecture"].(map[string]any)["design_digest"] = "not-a-digest"
		}},
		{name: "invalid module", want: "saved go.mod", change: func(v map[string]any) { v["go_mod"] = "require broken" }},
		{name: "invalid provenance", want: "saved provenance", change: func(v map[string]any) {
			v["made_from"].(map[string]any)["start"].(map[string]any)["write"] = 0
		}},
		{name: "unknown architecture field", want: "unknown field", change: func(v map[string]any) {
			v["architecture"].(map[string]any)["banner"] = "derived text must not be stored"
		}},
		{name: "null container", want: "null container", change: func(v map[string]any) {
			v["architecture"].(map[string]any)["containers"] = []any{nil}
		}},
		{name: "null edge", want: "null edge", change: func(v map[string]any) {
			v["architecture"].(map[string]any)["edges"] = []any{nil}
		}},
		{name: "null policy", want: "null agent policy", change: func(v map[string]any) {
			v["architecture"].(map[string]any)["agent_policies"] = []any{nil}
		}},
		{name: "null tool", want: "null tool contract", change: func(v map[string]any) {
			v["architecture"].(map[string]any)["tools"] = []any{nil}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var value map[string]any
			require.NoError(t, json.Unmarshal(raw, &value))
			tc.change(value)
			changed, err := json.Marshal(value)
			require.NoError(t, err)
			_, err = decodeCardBytes("chg", filepath.Base(cp.cards.path("chg")), true, changed)
			assert.ErrorContains(t, err, tc.want)
		})
	}
	loaded := loadMetadataRecord(t, cp, "chg")
	assert.Empty(t, loaded.architecture.SystemName)
	assert.Empty(t, loaded.architecture.Containers, "a Story-only result is valid storage")
}

// TestInvalidRevisionMetadataLeavesPreviousCard saves no partial group and
// leaves the previously persisted revision and provenance byte-for-byte intact.
func TestInvalidRevisionMetadataLeavesPreviousCard(t *testing.T) {
	cp := newControlPlane(t, Options{})
	mustPropose(t, cp, asCaller("alice@example.com", CallerGuide),
		"chg", "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
	record := loadMetadataRecord(t, cp, "chg")
	record.architecture, record.goMod, record.madeFrom = metadataFixture("a")
	saveMetadataRecord(t, cp, record)
	before, err := os.ReadFile(cp.cards.path("chg"))
	require.NoError(t, err)
	record.goMod = ""
	cp.mu.Lock()
	err = cp.cards.save(record)
	cp.mu.Unlock()
	assert.ErrorContains(t, err, "must be present together")
	after, err := os.ReadFile(cp.cards.path("chg"))
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

// TestProposedRevisionMetadataIsCheckedOnLoad validates a proposal's metadata
// as its own group rather than borrowing the agreed revision's complete group.
func TestProposedRevisionMetadataIsCheckedOnLoad(t *testing.T) {
	cp := newControlPlane(t, Options{})
	confirmedChange(t, cp, "chg")
	record := loadMetadataRecord(t, cp, "chg")
	record.architecture, record.goMod, record.madeFrom = metadataFixture("a")
	saveMetadataRecord(t, cp, record)
	next := nextMetadataRevision(t, record, "b")
	_, err := cp.saveRevision(trace.SpanFromContext(t.Context()),
		Principal{ID: "alice@example.com", Kind: CallerGuide},
		&gensurla.ReviseChangePayload{ChangeID: "chg", RequestRevision: record.card.RequestRevision}, next)
	require.NoError(t, err)
	raw, err := os.ReadFile(cp.cards.path("chg"))
	require.NoError(t, err)
	var value map[string]any
	require.NoError(t, json.Unmarshal(raw, &value))
	delete(value["proposed"].(map[string]any), "go_mod")
	changed, err := json.Marshal(value)
	require.NoError(t, err)
	_, err = decodeCardBytes("chg", filepath.Base(cp.cards.path("chg")), true, changed)
	assert.ErrorContains(t, err, "proposed revision metadata")
	assert.ErrorContains(t, err, "must be present together")
}

// TestSavedProvenanceDoesNotFollowTheLiveDraft keeps a revision readable when
// a later dropped build starts a new guide list. Only the live list changes.
func TestSavedProvenanceDoesNotFollowTheLiveDraft(t *testing.T) {
	cp := newControlPlane(t, Options{})
	confirmedChange(t, cp, "chg")
	record := loadMetadataRecord(t, cp, "chg")
	record.architecture, record.goMod, record.madeFrom = metadataFixture("a")
	saveMetadataRecord(t, cp, record)
	cp.draftMu.Lock()
	err := cp.cards.saveMadeFrom(madeFromRecord{
		Write: 2,
		Start: madeFromStart{
			Write: 2, ProjectCommit: strings.Repeat("c", 40),
			Dropped: &droppedBuild{
				ChangeID: "other", Number: 1,
				StartedAt:  time.Date(2026, 11, 14, 1, 2, 3, 0, time.UTC),
				BranchTips: map[string]string{},
			},
		},
		Entries: map[string][]madeFromVersion{}, Taken: map[string]madeFromTake{},
	})
	cp.draftMu.Unlock()
	require.NoError(t, err)
	loaded := loadMetadataRecord(t, cp, "chg")
	assert.Equal(t, uint64(1), loaded.madeFrom.Start.Write)
	assert.Equal(t, record.madeFrom, loaded.madeFrom)
}

// TestRevisionMetadataSurvivesMechanicalTransitions covers the reconstruction
// paths of latest, saveRevision, confirmation/adoption and proposal decline.
func TestRevisionMetadataSurvivesMechanicalTransitions(t *testing.T) {
	for _, confirmed := range []bool{false, true} {
		t.Run(map[bool]string{false: "replace unconfirmed", true: "propose decline and adopt"}[confirmed], func(t *testing.T) {
			cp := newControlPlane(t, Options{})
			guide := asCaller("alice@example.com", CallerGuide)
			user := asCaller("alice@example.com", CallerUser)
			card := mustPropose(t, cp, guide, "chg", "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
			record := loadMetadataRecord(t, cp, "chg")
			record.architecture, record.goMod, record.madeFrom = metadataFixture("a")
			saveMetadataRecord(t, cp, record)
			if confirmed {
				_, err := cp.ConfirmIntent(user, &gensurla.ConfirmIntentPayload{
					ChangeID: "chg", RequestRevision: card.RequestRevision, Examples: card.Examples,
					Answers: map[string]string{},
				})
				require.NoError(t, err)
				record = loadMetadataRecord(t, cp, "chg")
				assert.Equal(t, strings.Repeat("a", 64), record.architecture.DesignDigest)
			}
			latest := record.latest()
			assert.Equal(t, record.architecture, latest.architecture)
			assert.Equal(t, record.goMod, latest.goMod)
			assert.Equal(t, record.madeFrom, latest.madeFrom)

			next := nextMetadataRevision(t, record, "b")
			// This private writer fixture models an already selected refresh.
			// Public producer behavior is covered through refresh adoption.
			cp.draftMu.Lock()
			err := cp.cards.saveMadeFrom(madeFromRecord{
				Write: 1, Start: next.madeFrom.Start, Entries: map[string][]madeFromVersion{},
			})
			cp.draftMu.Unlock()
			require.NoError(t, err)
			selectedOperation := "selected-refresh-operation"
			payload := &gensurla.ReviseChangePayload{
				ChangeID: "chg", RequestRevision: record.card.RequestRevision, StoryRefreshOpID: &selectedOperation,
			}
			_, err = cp.saveRevision(trace.SpanFromContext(t.Context()),
				Principal{ID: "alice@example.com", Kind: CallerGuide}, payload, next)
			require.NoError(t, err)
			loaded := loadMetadataRecord(t, cp, "chg")
			assert.Equal(t, next.architecture, loaded.latest().architecture)
			assert.Equal(t, next.goMod, loaded.latest().goMod)
			assert.Equal(t, next.madeFrom, loaded.latest().madeFrom)
			if !confirmed {
				assert.Nil(t, loaded.proposed)
				return
			}
			assert.Equal(t, record.architecture, loaded.architecture, "a proposal preserves the agreed snapshot")
			_, err = cp.DeclineRevision(user, &gensurla.DeclineRevisionPayload{
				ChangeID: "chg", RequestRevision: next.card.RequestRevision,
			})
			require.NoError(t, err)
			declined := loadMetadataRecord(t, cp, "chg")
			assert.Nil(t, declined.proposed)
			assert.Equal(t, record.architecture, declined.architecture)
			assert.Equal(t, record.goMod, declined.goMod)
			assert.Equal(t, record.madeFrom, declined.madeFrom)
			_, err = cp.saveRevision(trace.SpanFromContext(t.Context()),
				Principal{ID: "alice@example.com", Kind: CallerGuide}, payload, next)
			require.NoError(t, err)
			_, err = cp.ConfirmIntent(user, &gensurla.ConfirmIntentPayload{
				ChangeID: "chg", RequestRevision: next.card.RequestRevision, Examples: next.card.Examples,
				Answers: map[string]string{},
			})
			require.NoError(t, err)
			adopted := loadMetadataRecord(t, cp, "chg")
			assert.Nil(t, adopted.proposed)
			assert.Equal(t, next.architecture, adopted.architecture)
			assert.Equal(t, next.goMod, adopted.goMod)
			assert.Equal(t, next.madeFrom, adopted.madeFrom)
		})
	}
}

// TestReviseWithoutRefreshKeepsTheCompleteSavedSnapshot drives the real
// revision operation after the workspace changes. Omitting a refresh must
// preserve the saved design and every snapshot field, even with zero Stories,
// and must not reread a workspace design that is no longer safe to read.
func TestReviseWithoutRefreshKeepsTheCompleteSavedSnapshot(t *testing.T) {
	for _, hasStories := range []bool{false, true} {
		t.Run(map[bool]string{false: "zero Stories", true: "with Stories"}[hasStories], func(t *testing.T) {
			for _, unreadable := range []bool{false, true} {
				t.Run(map[bool]string{false: "changed workspace", true: "unsafe workspace design"}[unreadable], func(t *testing.T) {
					workspace := t.TempDir()
					designFile := filepath.Join(workspace, "design", "design.go")
					require.NoError(t, os.Mkdir(filepath.Dir(designFile), 0o700))
					require.NoError(t, os.WriteFile(designFile, []byte("package design\n\nconst Saved = true\n"), 0o600))
					require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"),
						[]byte("module example.com/a\n\ngo 1.27.3\n"), 0o600))
					cp := newControlPlane(t, Options{WorkspaceDir: workspace})
					guide := asCaller("alice@example.com", CallerGuide)
					saveDraftSourceCardFixture(t, cp, "chg", map[string]string{"design/design.go": "package design\n\nconst Saved = true\n"})
					record := loadMetadataRecord(t, cp, "chg")
					record.architecture, record.goMod, record.madeFrom = metadataFixture("a")
					if hasStories {
						record.stories = sampleAdoptedStories()
						record.card.Stories = storySummaryItemsOrNil(record.stories)
						var err error
						record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
						require.NoError(t, err)
					}
					saveMetadataRecord(t, cp, record)
					original := loadMetadataRecord(t, cp, "chg")

					if unreadable {
						outside := filepath.Join(t.TempDir(), "outside.go")
						require.NoError(t, os.WriteFile(outside, []byte("package outside\n"), 0o600))
						require.NoError(t, os.Remove(designFile))
						require.NoError(t, os.Symlink(outside, designFile))
					} else {
						require.NoError(t, os.WriteFile(designFile, []byte("package design\n\nconst Changed = true\n"), 0o600))
					}
					require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.mod"),
						[]byte("module example.com/changed\n\ngo 1.27.3\n"), 0o600))
					prior := original.card.Examples[0]
					revised, err := cp.ReviseChange(guide, &gensurla.ReviseChangePayload{
						ChangeID: "chg", RequestRevision: original.card.RequestRevision,
						Prompt:       "Keep refunds bounded and preserve the agreed snapshot",
						RevisionNote: "Revise the change.",
						Analysis: &gensurla.CP1RevisionAnalysis{
							Summary: "Keep refunds bounded and preserve the agreed snapshot",
							Examples: []*gensurla.CP1RevisionAnalysisExample{{
								PriorID: &prior.ID, Scenario: prior.Scenario, InputSummary: prior.InputSummary,
								ExpectedOutcome: prior.ExpectedOutcome, VerificationKind: prior.VerificationKind,
							}},
							Ambiguities: []*gensurla.CP1AnalysisAmbiguity{}, RemovedExampleIds: []string{},
							PreservedBehaviors: original.card.PreservedBehaviors,
						},
					})
					require.NoError(t, err, "a revision without a refresh must not read the changed workspace")
					loaded := loadMetadataRecord(t, cp, "chg")
					assert.Equal(t, original.designSource, loaded.designSource)
					assert.Equal(t, original.stories, loaded.stories)
					assert.Equal(t, original.architecture, loaded.architecture)
					assert.Equal(t, original.goMod, loaded.goMod)
					assert.Equal(t, original.madeFrom, loaded.madeFrom)
					expected, err := computeCanonicalRevision(revised, original.owner, original.designSource)
					require.NoError(t, err)
					assert.Equal(t, expected, revised.RequestRevision, "the revision still hashes its saved design source")
				})
			}
		})
	}
}

// metadataFixture supplies a complete, structurally valid Story-only snapshot.
func metadataFixture(mark string) (*savedArchitecture, string, *madeFromSnapshot) {
	return &savedArchitecture{DesignDigest: strings.Repeat(mark, 64)},
		"module example.com/" + mark + "\n\ngo 1.27.3\n",
		&madeFromSnapshot{Start: madeFromStart{Write: 1}, Entries: map[string][]madeFromVersion{}}
}

// loadMetadataRecord reads through the store under its normal owner lock.
func loadMetadataRecord(t *testing.T, cp *ControlPlane, changeID string) storedChangeCard {
	t.Helper()
	cp.mu.RLock()
	record, exists, err := cp.cards.load(changeID)
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, exists)
	return record
}

// saveMetadataRecord saves through the store under its normal owner lock.
func saveMetadataRecord(t *testing.T, cp *ControlPlane, record storedChangeCard) {
	t.Helper()
	cp.mu.Lock()
	err := cp.cards.save(record)
	cp.mu.Unlock()
	require.NoError(t, err)
}

// nextMetadataRevision constructs the already-validated input of saveRevision;
// it does not choose a refresh snapshot or change producer behavior.
func nextMetadataRevision(t *testing.T, record storedChangeCard, mark string) storedRevision {
	t.Helper()
	card := *record.card
	card.Status = StatusNeedsConfirmation
	card.Prompt += " again"
	card.ConfirmedBy = nil
	card.Answers = map[string]string{}
	card.Approval = nil
	card.RequestRevision = ""
	revision, err := computeCanonicalRevision(&card, record.owner, record.designSource)
	require.NoError(t, err)
	card.RequestRevision = revision
	architecture, goMod, madeFrom := metadataFixture(mark)
	return storedRevision{
		card: &card, designSource: record.designSource, stories: record.stories,
		architecture: architecture, goMod: goMod, madeFrom: madeFrom,
	}
}
