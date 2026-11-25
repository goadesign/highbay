// Coverage persistence uses generated codecs and the recorded agreement only.
package verifyrun

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/verify"
)

// TestCoverageSnapshotPersistence distinguishes old absence from recorded
// empty statements and rejects malformed or foreign stored references.
func TestCoverageSnapshotPersistence(t *testing.T) {
	record := &TestCoverageSnapshot{Report: 2,
		Answers: []*genhighbay.ConfirmedAnswer{{ID: "tax", Question: "Is tax included?", Answer: "No"}},
		Examples: []*genhighbay.ExampleItem{{ID: "check_01", Scenario: "Refund at the limit",
			InputSummary: "$50 before tax", ExpectedOutcome: "Refund", VerificationKind: "deterministic"}},
		CoverageStatements: []*genhighbay.TestCoverageStatement{},
	}
	context := StoryContext{State: StoryRecorded, BuildNumber: 1, BuildStartedAt: time.Now().UTC(), TestCoverage: record}
	recordRaw, err := json.Marshal(record)
	require.NoError(t, err)
	raw, err := json.Marshal(context)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"coverage_statements":[]`)
	var loaded StoryContext
	require.NoError(t, json.Unmarshal(raw, &loaded))
	assert.Equal(t, context, loaded)
	for _, tc := range []struct {
		name, raw string
	}{
		{name: "null snapshot", raw: strings.Replace(string(raw), `"test_coverage":`+string(recordRaw), `"test_coverage":null`, 1)},
		{name: "unknown field", raw: strings.Replace(string(raw), `"report":2`, `"report":2,"invented":true`, 1)},
		{name: "null rows", raw: strings.Replace(string(raw), `"coverage_statements":[]`, `"coverage_statements":null`, 1)},
		{name: "unknown question", raw: strings.Replace(string(raw), `"coverage_statements":[]`, `"coverage_statements":[{"scenario":"refund","question_ids":["other"],"example_ids":[],"explanation":"Exercises a refund."}]`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var bad StoryContext
			assert.Error(t, json.Unmarshal([]byte(tc.raw), &bad))
		})
	}
	req := Request{RunID: NewRunID(), Owner: "fixture-user", AcceptedAt: time.Now().UTC(),
		Plan: evaluatorPlan, StoryContext: context}
	requestRaw, err := json.Marshal(req)
	require.NoError(t, err)
	_, err = decodeRequest(req.RunID, requestRaw)
	assert.ErrorContains(t, err, "only written-test runs")
	pid, err := Start(t.TempDir(), req, nil, nil, t.TempDir())
	assert.Zero(t, pid)
	assert.ErrorContains(t, err, "only written-test runs")
	req.Plan.Tests = verify.TestsWritten
	requestRaw, err = json.Marshal(req)
	require.NoError(t, err)
	_, err = decodeRequest(req.RunID, requestRaw)
	assert.NoError(t, err)
}
