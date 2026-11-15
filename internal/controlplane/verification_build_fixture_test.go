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
	assignment, build := readBuilderFixture(t, cp, card)
	writeBuilderDesignFixture(t, assignment.WorkingFolder, build.DesignSource)
	writeWorkspaceFiles(t, assignment.WorkingFolder, map[string]string{
		"builder_work.txt": "Implement the agreed change.\n",
	})
	return commitAllForLayoutTest(t, assignment.WorkingFolder)
}

// readBuilderCandidateFixture places a test's authored immutable candidate on
// its numbered build branch after the real assignment read. Its caller starts
// the build at the intended project base; no saved build facts are rewritten.
func readBuilderCandidateFixture(
	t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult, candidate string,
) string {
	t.Helper()
	assignment, _ := readBuilderFixture(t, cp, card)
	gitForLayoutTest(t, assignment.WorkingFolder, "reset", "--hard", candidate)
	return assignment.WorkingFolder
}

// writeBuilderDesignFixture authors the assignment's exact saved files in a
// candidate fixture. It runs no generator and leaves excluded files alone.
func writeBuilderDesignFixture(t *testing.T, folder, source string) {
	t.Helper()
	files, err := decodeDesignSource(source)
	require.NoError(t, err)
	require.NotEmpty(t, files)
	writeWorkspaceFiles(t, folder, files)
}

// readBuilderFixture opens the build's real builder copy and reads its
// assignment through the owner, without changing tests or making a candidate.
func readBuilderFixture(
	t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult,
) (*gensurla.BuilderAssignment, buildFile) {
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
	return assignment, build
}

// verificationSourceOptions configures both unit profiles at construction so
// the report checker never races a later replacement of its verifier.
func verificationSourceOptions(cfg *VerificationConfig, stored bool) Options {
	if stored {
		return Options{Verification: cfg}
	}
	return Options{RunsDir: cfg.RunsDir, WrittenTestRuns: &WrittenTestRuns{
		ReportDir: cfg.Plan.ReportDir, Command: cfg.Command, Timeout: cfg.Timeout,
	}}
}
