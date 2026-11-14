// This file proves base/currentness against real commits and immutable records.
package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/surla/internal/designversion"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

// TestDesignBaseUsesOnlySavedHistory rejects both an outside fix and a revert
// while admitting committed/uncommitted versions captured together at the start.
func TestDesignBaseUsesOnlySavedHistory(t *testing.T) {
	a := "package design\nvar A=0\n"
	b := "package design\nvar A=1\n"
	c := "package design\nvar A=2\n"
	t.Run("initial committed and uncommitted", func(t *testing.T) {
		f := newDraftUpdateFixture(t, a, b, b)
		list := f.list()
		assert.NoError(t, checkDesignBase(t.Context(), f.dir, f.head(), &madeFromSnapshot{Start: list.Start, Entries: list.Entries}))
	})
	t.Run("outside fix remains outside immutable card", func(t *testing.T) {
		f := newDraftUpdateFixture(t, a, a, a)
		_, saved := readDraftForTest(t, f.cp)
		f.project(b)
		f.commit()
		f.update()
		assert.ErrorContains(t, checkDesignBase(t.Context(), f.dir, f.head(), &saved), "design/design.go in commit "+f.head())
		assert.ErrorContains(t, checkDesignBase(t.Context(), f.dir, f.head(), &saved), "Commit the design change in the project folder")
		list := f.list()
		assert.NoError(t, checkDesignBase(t.Context(), f.dir, f.head(), &madeFromSnapshot{Start: list.Start, Entries: list.Entries}))
	})
	t.Run("committed revert supersedes older listed version", func(t *testing.T) {
		f := newDraftUpdateFixture(t, a, a, a)
		f.project(b)
		f.commit()
		f.update()
		list := f.list()
		saved := cloneMadeFromSnapshot(&madeFromSnapshot{Start: list.Start, Entries: list.Entries})
		f.project(a)
		f.commit()
		assert.Error(t, checkDesignBase(t.Context(), f.dir, f.head(), saved))
		f.update()
		list = f.list()
		assert.NoError(t, checkDesignBase(t.Context(), f.dir, f.head(), &madeFromSnapshot{Start: list.Start, Entries: list.Entries}))
	})
	t.Run("later unrelated live entry cannot authorize saved revision", func(t *testing.T) {
		f := newDraftUpdateFixture(t, a, a, b)
		f.save()
		saved := loadMetadataRecord(t, f.cp, "revision-1").madeFrom
		f.project(c)
		f.commit()
		f.update()
		assert.Error(t, checkDesignBase(t.Context(), f.dir, f.head(), saved))
	})
	t.Run("legacy no provenance", func(t *testing.T) {
		f := newDraftUpdateFixture(t, a, a, a)
		assert.ErrorContains(t, checkDesignBase(t.Context(), f.dir, f.head(), nil), "records no design")
	})
}

// TestDesignBaseWhenRepositoryAppearsLater never invents an initial committed
// version when the first draft predated Git and only captured an uncommitted one.
func TestDesignBaseWhenRepositoryAppearsLater(t *testing.T) {
	dir := t.TempDir()
	a := "package design\nvar A=0\n"
	b := "package design\nvar A=1\n"
	writeWorkspaceFiles(t, dir, validWorkspaceStub(b))
	cp := newControlPlane(t, Options{WorkspaceDir: dir})
	_, saved := readDraftForTest(t, cp)
	assert.Empty(t, saved.Start.ProjectCommit)
	writeWorkspaceFiles(t, dir, map[string]string{updateDesignFile: a})
	gitForLayoutTest(t, dir, "init", "-q")
	gitForLayoutTest(t, dir, "add", "go.mod", "go.sum", updateDesignFile)
	gitForLayoutTest(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "Repository appeared after draft")
	head := strings.TrimSpace(gitForLayoutTest(t, dir, "rev-parse", "HEAD"))
	writeWorkspaceFiles(t, dir, map[string]string{updateDesignFile: b})
	assert.ErrorContains(t, checkDesignBase(t.Context(), dir, head, &saved), "changed outside Surla")
}

// TestRemakeRestoresExactRevisionHistory removes an intervening unrelated
// entry even though it entered before the selected revision's own write.
func TestRemakeRestoresExactRevisionHistory(t *testing.T) {
	f := newDraftUpdateFixture(t, "package design\nvar A=0\n", "package design\nvar A=0\n", "package design\nvar A=1\n")
	files, provenance := readDraftForTest(t, f.cp)
	source, err := designSourceFromModuleSnapshot(files)
	require.NoError(t, err)
	selected := refreshedSnapshot{designSource: source, goMod: files["go.mod"], architecture: &savedArchitecture{DesignDigest: strings.Repeat("a", 64)}, madeFrom: cloneMadeFromSnapshot(&provenance)}
	f.cp.draftMu.Lock()
	list, _, err := f.cp.cards.loadMadeFrom()
	require.NoError(t, err)
	list.Write++
	_, err = f.cp.enterDraftVersion(&list, "design/unrelated.go", map[string]string{"design/unrelated.go": "package design\n\nvar C = 9\n"})
	require.NoError(t, err)
	require.NoError(t, f.cp.cards.saveMadeFrom(list))
	f.cp.mu.Lock()
	err = f.cp.recordRefreshedSnapshot(trace.SpanFromContext(t.Context()), selected)
	f.cp.mu.Unlock()
	f.cp.draftMu.Unlock()
	require.NoError(t, err)
	saveProposedCardFixture(t, f.cp, "alice@example.com", "saved", "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
	record := loadMetadataRecord(t, f.cp, "saved")
	record.designSource = source
	record.goMod = selected.goMod
	record.architecture = selected.architecture
	record.madeFrom = selected.madeFrom
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, source)
	require.NoError(t, err)
	saveMetadataRecord(t, f.cp, record)
	f.remake()
	restored := f.list()
	assert.Equal(t, selected.madeFrom.Entries, restored.Entries)
	assert.NotContains(t, restored.Entries, "design/unrelated.go")
	assert.Equal(t, selected.madeFrom.Start, restored.Start)
}

// TestMetadataLessCardsCannotSupplyARemake keeps legacy intent from inventing
// provenance for an unrelated old design when the project draft is missing.
func TestMetadataLessCardsCannotSupplyARemake(t *testing.T) {
	f := newDraftUpdateFixture(t, "package design\nvar A=0\n", "package design\nvar A=0\n", "package design\nvar A=0\n")
	saveDraftSourceCardFixture(t, f.cp, "legacy", map[string]string{updateDesignFile: "package design\n\nvar A = 0\n"})
	f.project("package design\nvar A=2\n")
	f.remake()
	assert.Equal(t, canonicalUpdateText(t, f.projectText()), f.draft())
	assert.Equal(t, f.head(), f.list().Taken[updateDesignFile].ProjectCommit)
}

// TestUpdateDraftReportsProblemsAndKeepsOtherFilesMoving verifies partial
// progress, safe additions/deletions, exact module differences and marker recovery.
func TestUpdateDraftReportsProblemsAndKeepsOtherFilesMoving(t *testing.T) {
	f := newDraftUpdateFixture(t, "package design\nvar A=0\n", "package design\nvar A=0\n", "package design\nvar A=0\n")
	writeWorkspaceFiles(t, filepath.Join(f.dir, draftFolderName), map[string]string{updateDesignFile: "package design\n<<<<<<< broken\n"})
	writeWorkspaceFiles(t, f.dir, map[string]string{"design/new.go": "package design\nvar Added=1\n"})
	r := f.update()
	require.Len(t, r.ToCorrect, 1)
	assert.Equal(t, updateDesignFile, r.ToCorrect[0].Path)
	assert.Equal(t, []string{"design/new.go"}, r.Taken)
	bad := f.draft()
	again := f.update()
	assert.Equal(t, bad, f.draft())
	assert.Len(t, again.ToCorrect, 1)
	f.guide("package design\nvar A=0\n")
	// Commit the addition before exercising its removal. Reverting an
	// uncommitted addition to original absence is the documented residual.
	commitProjectDesign(t, f.dir, "design/new.go")
	require.NoError(t, os.Remove(filepath.Join(f.dir, "design/new.go")))
	r = f.update()
	assert.Equal(t, []string{"design/new.go"}, r.Taken)
	_, err := os.Stat(filepath.Join(f.dir, draftFolderName, "design/new.go"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	projectMod := "module example.com/project\n\ngo 1.27.3\nrequire example.com/lib v1.2.0\nreplace example.com/lib => example.com/other v1.0.0\n"
	draftMod := "module example.com/project\n\ngo 1.27.3\nrequire example.com/lib v1.1.0\nreplace example.com/lib => example.com/other v1.1.0\n"
	writeWorkspaceFiles(t, f.dir, map[string]string{"go.mod": projectMod})
	writeWorkspaceFiles(t, filepath.Join(f.dir, draftFolderName), map[string]string{"go.mod": draftMod})
	r = f.update()
	require.Len(t, r.ToCorrect, 1)
	assert.Equal(t, "go.mod", r.ToCorrect[0].Path)
	assert.Contains(t, r.ToCorrect[0].Problem, "example.com/lib v1.2.0")
	assert.Contains(t, r.ToCorrect[0].Problem, "replace")
	stored, err := os.ReadFile(filepath.Join(f.dir, draftFolderName, "go.mod"))
	require.NoError(t, err)
	assert.Equal(t, draftMod, string(stored))
}

// TestUpdateDraftDeletionAgainstGuideEdit leaves the guide's file intact and
// lists the incompatible removal rather than silently accepting it.
func TestUpdateDraftDeletionAgainstGuideEdit(t *testing.T) {
	f := newDraftUpdateFixture(t, "package design\nvar A=0\n", "package design\nvar A=0\n", "package design\nvar A=1\n")
	require.NoError(t, os.Remove(filepath.Join(f.dir, updateDesignFile)))
	before := f.draft()
	r := f.update()
	require.Len(t, r.ToCorrect, 1)
	assert.Equal(t, before, f.draft())
	assert.Nil(t, f.list().Pending)
}

// TestPendingConflictCompletesWithoutNestedMarkers recovers a real conflicted
// file write, then refuses to merge its unparsable result a second time.
func TestPendingConflictCompletesWithoutNestedMarkers(t *testing.T) {
	f := newDraftUpdateFixture(t, "package design\nvar A=0\n", "package design\nvar A=0\n", "package design\nvar A=1\n")
	f.project("package design\nvar A=2\n")
	f.stop(true)
	before := f.draft()
	r := f.update()
	assert.Len(t, r.ToCorrect, 1)
	assert.Equal(t, before, f.draft())
	assert.Equal(t, 1, strings.Count(f.draft(), "<<<<<<<"))
	assert.Nil(t, f.list().Pending)
	assert.Equal(t, designversion.Digest(canonicalUpdateText(t, f.projectText())), f.list().Taken[updateDesignFile].Version)
}
