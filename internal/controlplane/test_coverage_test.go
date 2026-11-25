// These checks use the real HTTP/MCP report entry and saved build, with the
// existing build-copy seam. No compiler or candidate process is needed here.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/testwriter"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/goadesign/highbay/internal/verifyrun"
)

// TestReportCoverageValidatesBeforeMutation proves required wire presence and
// the domain refusal before a report number, verdict or saved input changes.
func TestReportCoverageValidatesBeforeMutation(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := confirmAgreedTerms(t, cp, "chg")
	_, err := cp.StartBuild(agreedUser, &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(agreedTestWriter, &genhighbay.GetTestWriterAssignmentPayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	server := roleServer(t, cp)
	before := readBuildFile(t, cp, card.ChangeID)
	for _, field := range []string{"", `,"coverage_statements":null`, `,"coverage_statements":[null]`} {
		body := fmt.Sprintf(`{"request_revision":%q%s}`, card.RequestRevision, field)
		status, _ := httpCall(t, server.URL, "tests", http.MethodPost, "/assignments/test-writer/chg/finished", body)
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, before, readBuildFile(t, cp, card.ChangeID))
	}
	bad := fmt.Sprintf(`{"request_revision":%q,"coverage_statements":[{"scenario":"refund","question_ids":["unknown"],"example_ids":[],"explanation":"Checks the refund."}]}`, card.RequestRevision)
	status, body := httpCall(t, server.URL, "tests", http.MethodPost, "/assignments/test-writer/chg/finished", bad)
	assert.Equal(t, http.StatusBadRequest, status)
	assert.Contains(t, body, "invalid_coverage")
	assert.Contains(t, body, "unknown")
	assert.Equal(t, before, readBuildFile(t, cp, card.ChangeID))
	args := strings.TrimSuffix(bad, "}") + `,"change_id":"chg"}`
	message, _ := mcpToolCall(t, server.URL, "tests", "report_tests_finished", args)
	assert.Contains(t, message, "unknown")
	assert.Equal(t, before, readBuildFile(t, cp, card.ChangeID))
	status, body = httpCall(t, server.URL, "tests", http.MethodPost, "/assignments/test-writer/chg/finished",
		fmt.Sprintf(`{"request_revision":%q,"coverage_statements":[]}`, card.RequestRevision))
	assert.Equal(t, http.StatusOK, status, body)
	assert.Equal(t, 1, mustOpenBuildFile(t, cp).ReportNumber)
}

// TestCoverageScenarioMembership permits extra listed scenarios and refuses
// only references absent from the exact program already described by Check.
func TestCoverageScenarioMembership(t *testing.T) {
	rows := []*genhighbay.TestCoverageStatement{{Scenario: "additional_boundary"}}
	assert.Empty(t, coverageScenarioFindings(rows, []string{"required_refund", "additional_boundary"}))
	findings := coverageScenarioFindings(rows, []string{"required_refund"})
	require.Len(t, findings, 1)
	assert.Equal(t, testwriter.FindingCoverageReference, findings[0].Kind)
	assert.Contains(t, findings[0].Message, "additional_boundary")
	assert.Contains(t, findings[0].Message, "report_tests_finished")
}

// TestStoredCoverageUsesTheBuiltAgreement checks the storage boundary, including
// old omission, without imposing new wire presence rules on historical records.
func TestStoredCoverageUsesTheBuiltAgreement(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := confirmAgreedTerms(t, cp, "chg")
	_, err := cp.StartBuild(agreedUser, &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	base := mustOpenBuildFile(t, cp)
	base.ReportNumber, base.TestWriter.State = 1, testsReported
	for _, tc := range []struct {
		name string
		edit func(*buildFile)
		want string
	}{
		{name: "old omission", edit: func(saved *buildFile) { saved.TestWriter.CoverageStatements = nil }},
		{name: "accepted structure", edit: func(*buildFile) {}},
		{name: "null row", edit: func(saved *buildFile) { saved.TestWriter.CoverageStatements[0] = nil }, want: "stored coverage statement"},
		{name: "empty explanation", edit: func(saved *buildFile) { saved.TestWriter.CoverageStatements[0].Explanation = "" }, want: "stored coverage statement"},
		{name: "foreign example", edit: func(saved *buildFile) { saved.TestWriter.CoverageStatements[0].ExampleIds[0] = "foreign" }, want: "outside the built agreement"},
		{name: "invalid answer basis", edit: func(saved *buildFile) { saved.Answers = []*genhighbay.ConfirmedAnswer{nil} }, want: "stored coverage answer"},
		{name: "invalid example basis", edit: func(saved *buildFile) { saved.Examples = []*genhighbay.ExampleItem{nil} }, want: "stored coverage example"},
		{name: "unreported statements", edit: func(saved *buildFile) { saved.ReportNumber, saved.TestWriter.State = 0, testsAssigned }, want: "unreported or builder-only"},
		{name: "configured evaluator statements", edit: func(saved *buildFile) { saved.Roles = buildRolesBuilderOnly }, want: "unreported or builder-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := base
			saved.TestWriter.CoverageStatements = []*genhighbay.TestCoverageStatement{{
				Scenario: "refund", QuestionIds: []genhighbay.TestCoverageReference{},
				ExampleIds:  []genhighbay.TestCoverageReference{genhighbay.TestCoverageReference(base.Examples[0].ID)},
				Explanation: "Checks the refund example.",
			}}
			tc.edit(&saved)
			raw, err := json.Marshal(saved)
			require.NoError(t, err)
			_, err = decodeOpenBuild(raw)
			if tc.want == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.want)
			}
		})
	}
}

// TestReportedCoverageCorrection uses the existing report-check seam and real
// temporary branch state. It proves orchestration without another compilation.
func TestReportedCoverageCorrection(t *testing.T) {
	copy := &standInCopy{check: func(context.Context, int) (testwriter.Verdict, error) {
		return testwriter.Verdict{DeclaredScenarios: []string{"extra_refund"}}, nil
	}}
	cp, revision := newReportFixture(t, Options{}, copy)
	readBothAssignments(t, cp)
	basis := mustOpenBuildFile(t, cp)
	rows := []*genhighbay.TestCoverageStatement{{Scenario: "unknown", QuestionIds: []genhighbay.TestCoverageReference{},
		ExampleIds:  []genhighbay.TestCoverageReference{genhighbay.TestCoverageReference(basis.Examples[0].ID)},
		Explanation: "Checks the agreed refund example."}}
	_, err := cp.ReportTestsFinished(asCaller("alice@example.com", CallerTestWriter), &genhighbay.ReportTestsFinishedPayload{
		ChangeID: "chg", RequestRevision: revision, CoverageStatements: rows,
	})
	require.NoError(t, err)
	waitForTests(t, cp, genhighbay.TestsKindNeedsChanges)
	assert.Empty(t, copy.recorded())
	assignment, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &genhighbay.GetTestWriterAssignmentPayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(assignment.Findings, " "), "undeclared scenario")
	assert.Equal(t, rows, assignment.CoverageStatements)
	rows[0].Scenario = "extra_refund"
	_, err = cp.ReportTestsFinished(asCaller("alice@example.com", CallerTestWriter), &genhighbay.ReportTestsFinishedPayload{
		ChangeID: "chg", RequestRevision: revision, CoverageStatements: rows,
	})
	require.NoError(t, err)
	rows[0].Explanation = "A caller changed its own input after reporting."
	build := waitForTests(t, cp, genhighbay.TestsKindAccepted)
	require.Len(t, build.CoverageStatements, 1)
	assert.Equal(t, genhighbay.TestCoverageReference("extra_refund"), build.CoverageStatements[0].Scenario)
	assert.Equal(t, "Checks the agreed refund example.", build.CoverageStatements[0].Explanation)
	assert.Len(t, copy.recorded(), 1)
}

// TestWrittenTestSelectionKeepsItsAcceptedCoverage exercises the same locked
// selection used by verification, then reaccepts the same commit with a new
// statement. The earlier selected report survives a generated-codec round trip.
func TestWrittenTestSelectionKeepsItsAcceptedCoverage(t *testing.T) {
	config := verificationConfig(t)
	cp := newBuildControlPlane(t, verificationSourceOptions(config, false))
	card := confirmAgreedTerms(t, cp, "chg")
	_, err := cp.StartBuild(agreedUser, &genhighbay.StartBuildPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	saved := mustOpenBuildFile(t, cp)
	saved.ReportNumber = 1
	saved.TestWriter = testWriterAssignment{State: testsAccepted, Commit: standInCommit, CheckedAt: time.Now().UTC(),
		CoverageStatements: []*genhighbay.TestCoverageStatement{{Scenario: "refund", QuestionIds: []genhighbay.TestCoverageReference{genhighbay.TestCoverageReference(saved.Answers[0].ID)},
			ExampleIds: []genhighbay.TestCoverageReference{}, Explanation: "Report one's tax-boundary assertion."}},
	}
	cp.mu.Lock()
	err = cp.cards.saveOpenBuild(saved)
	cp.mu.Unlock()
	require.NoError(t, err)
	cp.mu.RLock()
	selection, err := cp.testsChoice(Principal{ID: agreedOwner}, card.ChangeID, card.RequestRevision)
	cp.mu.RUnlock()
	require.NoError(t, err)
	assert.Equal(t, standInCommit, selection.commit)
	assert.Equal(t, verify.TestsWritten, selection.source)
	snapshot := testCoverageSnapshot(selection.build)
	saved.ReportNumber = 2
	saved.TestWriter.CoverageStatements[0].Explanation = "Report two on the same commit."
	cp.mu.Lock()
	err = cp.cards.saveOpenBuild(saved)
	cp.mu.Unlock()
	require.NoError(t, err)
	assert.Equal(t, 1, snapshot.Report)
	assert.Equal(t, "Report one's tax-boundary assertion.", snapshot.CoverageStatements[0].Explanation)
	context := verifyrun.StoryContext{State: verifyrun.StoryRecorded, BuildNumber: saved.Number,
		BuildStartedAt: saved.StartedAt, Stories: saved.Stories, TestCoverage: snapshot}
	raw, err := json.Marshal(context)
	require.NoError(t, err)
	var loaded verifyrun.StoryContext
	require.NoError(t, json.Unmarshal(raw, &loaded))
	assert.Equal(t, context, loaded)
	run, err := runStoryContext(verifyrun.Request{Plan: verify.Plan{Tests: verify.TestsWritten}, StoryContext: loaded})
	require.NoError(t, err)
	recorded, ok := run.AsRecorded()
	require.True(t, ok)
	coverage, ok := recorded.TestCoverage.AsRecorded()
	require.True(t, ok)
	assert.Equal(t, 1, coverage.Report)
	assert.Equal(t, "Report one's tax-boundary assertion.", coverage.CoverageStatements[0].Explanation)
	assert.Equal(t, saved.Answers, coverage.Answers)
}
