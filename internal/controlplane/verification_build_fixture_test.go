// This file supplies committed builder work for execution-entry unit tests.
// Historical report fixtures continue to write their saved requests directly.
package controlplane

import (
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/stretchr/testify/require"
)

// commitBuilderCandidateFixture requires an existing numbered build, makes its
// real builder copy through the copy owner, reads the assignment through the
// control plane and commits authored work on that build's branch. It neither
// starts a build nor changes test acceptance or historical report records.
func commitBuilderCandidateFixture(
	t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult,
) string {
	t.Helper()
	folder := readBuilderFixture(t, cp, card)
	writeWorkspaceFiles(t, folder, map[string]string{
		"builder_work.txt": "Implement the agreed change.\n",
	})
	return commitAllForLayoutTest(t, folder)
}

// readBuilderCandidateFixture places a test's authored immutable candidate on
// its numbered build branch after the real assignment read. Its caller starts
// the build at the intended project base; no saved build facts are rewritten.
func readBuilderCandidateFixture(
	t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult, candidate string,
) string {
	t.Helper()
	folder := readBuilderFixture(t, cp, card)
	gitForLayoutTest(t, folder, "reset", "--hard", candidate)
	return folder
}

// readBuilderFixture opens the build's real builder copy and reads its
// assignment through the owner, without changing tests or making a candidate.
func readBuilderFixture(t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult) string {
	t.Helper()
	cp.mu.RLock()
	build, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found, "execution fixtures must first establish their open build")
	require.Equal(t, card.ChangeID, build.ChangeID)
	require.Equal(t, card.RequestRevision, build.RequestRevision)
	workspace := cp.currentWorkspaceDir()
	copy, err := testwriter.MakeCopy(t.Context(), workspace, testwriter.BuilderRole,
		testwriter.BuilderRole.Branch(build.Number), build.Base)
	require.NoError(t, err)
	record := loadMetadataRecord(t, cp, card.ChangeID)
	assignment, err := cp.GetBuilderAssignment(asCaller(record.owner, CallerBuilder),
		&gensurla.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	require.True(t, assignment.Read)
	require.Equal(t, copy.Project(), assignment.WorkingFolder)
	return copy.Project()
}
