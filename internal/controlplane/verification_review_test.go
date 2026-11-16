// This file tests user review evidence through the real run and approval owners.
package controlplane

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reviewFixture binds a real build/candidate and saved run to a promotion stub.
type reviewFixture struct {
	cp     *ControlPlane
	card   *gensurla.ChangeCardResult
	cfg    *VerificationConfig
	req    verifyrun.Request
	report verify.Report
	target *promotionTargetStub
}

// TestRunStoryEvidenceUsesRecordedFlows checks exact generation mapping and the
// distinction between excluded drafts, no recorded context and evaluator unknowns.
func TestRunStoryEvidenceUsesRecordedFlows(t *testing.T) {
	stories := []designeval.StorySummary{{Name: "Orders", Actors: []designeval.StoryActor{{Key: "buyer", Title: "Buyer"}}}}
	for _, key := range []string{"place-order", "cancel-order", "draft-return"} {
		flow := designeval.StoryFlow{Key: key, Title: key, Complete: key != "draft-return",
			Steps: []designeval.StoryStep{{Key: "request", ActorKey: "buyer", Summary: "Buyer requests the action", Kind: "start"}}}
		if flow.Complete {
			flow.Steps = append(flow.Steps, designeval.StoryStep{Key: "result", ActorKey: "buyer", Summary: "Buyer sees the result", Kind: "outcome"})
		}
		stories[0].Flows = append(stories[0].Flows, flow)
	}
	req := verifyrun.Request{Plan: verify.Plan{Tests: verify.TestsWritten}, StoryContext: verifyrun.StoryContext{
		State: verifyrun.StoryRecorded, BuildNumber: 3, BuildStartedAt: time.Now().UTC(), Stories: stories,
	}}
	context, err := runStoryContext(req)
	require.NoError(t, err)
	recorded, ok := context.AsRecorded()
	require.True(t, ok)
	require.Len(t, recorded.Stories, 1)
	assert.Len(t, recorded.Stories[0].Flows, 3, "the draft stays visible")
	mapping, ok := recorded.Mapping.AsMapped()
	require.True(t, ok)
	require.Len(t, mapping.RequiredTests, 2)
	assert.Equal(t, "place_order", mapping.RequiredTests[0].Scenario)
	assert.Equal(t, "cancel_order", mapping.RequiredTests[1].Scenario)
	assert.Equal(t, "place-order", mapping.RequiredTests[0].FlowKey)
	req.Plan.Tests = verify.TestsEvaluator
	context, err = runStoryContext(req)
	require.NoError(t, err)
	recorded, ok = context.AsRecorded()
	require.True(t, ok)
	_, unknown := recorded.Mapping.AsNotRecordedForEvaluator()
	assert.True(t, unknown)
	req.StoryContext = verifyrun.StoryContext{}
	context, err = runStoryContext(req)
	require.NoError(t, err)
	_, missing := context.AsNotRecorded()
	assert.True(t, missing)
}

// TestReviewVerificationObservesApprovalWithoutWriting exercises the shared
// preflight, safe report projection and persisted approval states on real builds.
func TestReviewVerificationObservesApprovalWithoutWriting(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*testing.T, reviewFixture)
		want   gensurla.ApprovalKind
	}{
		{name: "passed and current", want: gensurla.ApprovalKindAvailable},
		{name: "tests changed", want: gensurla.ApprovalKindBlocked, change: func(t *testing.T, f reviewFixture) {
			f.cp.mu.Lock()
			f.cp.verifier.(storedVerification).cfg.Plan.SuiteCommit = strings.Repeat("c", 40)
			f.cp.mu.Unlock()
		}},
		{name: "agreement changed", want: gensurla.ApprovalKindBlocked, change: func(t *testing.T, f reviewFixture) {
			changeReviewAgreement(t, f)
		}},
		{name: "artifacts changed", want: gensurla.ApprovalKindBlocked, change: func(t *testing.T, f reviewFixture) {
			f.target.artifacts = map[string]string{"orders": "other"}
		}},
		{name: "target report unreadable", want: gensurla.ApprovalKindBlocked, change: func(t *testing.T, f reviewFixture) {
			f.target.loadErr = errors.New("promotion report could not be read")
		}},
		{name: "approved", want: gensurla.ApprovalKindRecorded, change: func(t *testing.T, f reviewFixture) {
			_, err := f.cp.ApproveMerge(asCaller("alice@example.com", CallerUser), reviewApprovalPayload(f))
			require.NoError(t, err)
		}},
		{name: "saved approval unreadable artifacts", want: gensurla.ApprovalKindUnverified, change: func(t *testing.T, f reviewFixture) {
			_, err := f.cp.ApproveMerge(asCaller("alice@example.com", CallerUser), reviewApprovalPayload(f))
			require.NoError(t, err)
			f.target.artifactsErr = errors.New("release binaries could not be read")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newReviewFixture(t)
			if tc.change != nil {
				tc.change(t, f)
			}
			before := savedProjectFiles(t, f.cp.cards.dir, f.cp.currentWorkspaceDir())
			got, err := f.cp.ReviewVerification(asCaller("alice@example.com", CallerUser), &gensurla.GetVerificationPayload{RunID: f.req.RunID})
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Review.Approval.Kind())
			assert.Equal(t, before, savedProjectFiles(t, f.cp.cards.dir, f.cp.currentWorkspaceDir()), "review writes no workflow state")
			completed, ok := got.State.AsCompleted()
			require.True(t, ok)
			assert.Equal(t, "holdout-1", completed.Report.Scenarios[1].ID)
			assert.Empty(t, completed.Report.Scenarios[1].Checks)
			context, ok := got.Review.StoryContext.AsRecorded()
			require.True(t, ok)
			assert.Empty(t, context.Stories, "recorded empty is distinct from not recorded")
			if blocked, ok := got.Review.Approval.AsBlocked(); ok {
				assert.NotEmpty(t, blocked.Reason)
			}
		})
	}
}

// TestReviewVerificationRechecksAgreementDuringArtifacts prevents an available
// observation assembled from agreement/test facts that changed while unlocked.
func TestReviewVerificationRechecksAgreementDuringArtifacts(t *testing.T) {
	f := newReviewFixture(t)
	reading, release := make(chan struct{}), make(chan struct{})
	releaseArtifacts := sync.OnceFunc(func() { close(release) })
	defer releaseArtifacts()
	f.target.artifactsReading, f.target.artifactsRelease = reading, release
	result := make(chan *gensurla.VerificationReviewResult, 1)
	resultErrors := make(chan error, 1)
	go func() {
		got, err := f.cp.ReviewVerification(asCaller("alice@example.com", CallerUser), &gensurla.GetVerificationPayload{RunID: f.req.RunID})
		result <- got
		resultErrors <- err
	}()
	select {
	case <-reading:
	case <-time.After(10 * time.Second):
		releaseArtifacts()
		t.Fatal("review never reached artifact read")
	}
	changeReviewAgreement(t, f)
	releaseArtifacts()
	select {
	case got := <-result:
		require.NoError(t, <-resultErrors)
		assert.Equal(t, gensurla.ApprovalKindBlocked, got.Review.Approval.Kind())
	case <-time.After(10 * time.Second):
		t.Fatal("review did not finish after artifact read")
	}
}

// TestReviewVerificationHTTPPolicy proves required review on the user wire and
// rejects all agent roles at the same boundary that governs MCP discovery.
func TestReviewVerificationHTTPPolicy(t *testing.T) {
	f := newReviewFixture(t)
	credentials := map[string]Principal{}
	for _, kind := range []CallerKind{CallerUser, CallerGuide, CallerBuilder, CallerTestWriter} {
		credentials[string(kind)] = Principal{ID: "alice@example.com", CallerID: string(kind), Kind: kind}
	}
	srv := httptest.NewServer(NewHTTPHandler(f.cp, fixtureCredentials(credentials)))
	t.Cleanup(srv.Close)
	for token := range credentials {
		status, body := httpCall(t, srv.URL, token, http.MethodGet, "/verifications/"+f.req.RunID+"/review", "")
		if token != string(CallerUser) {
			assert.Equal(t, http.StatusForbidden, status, body)
			continue
		}
		require.Equal(t, http.StatusOK, status, body)
		var wire struct {
			Review struct {
				StoryContext struct {
					State string `json:"state"`
				} `json:"story_context"`
				Approval struct {
					State string `json:"state"`
				} `json:"approval"`
			} `json:"review"`
		}
		require.NoError(t, json.Unmarshal([]byte(body), &wire))
		assert.Equal(t, "recorded", wire.Review.StoryContext.State)
		assert.Equal(t, "available", wire.Review.Approval.State)
		assert.Contains(t, body, `"holdout-1"`)
		assert.NotContains(t, body, `"id":"h"`)
	}
}

// TestReviewVerificationKeepsHistoricalStory reads accepted evidence after a
// real agreement revision and distinguishes a run written without Story context.
func TestReviewVerificationKeepsHistoricalStory(t *testing.T) {
	f := newReviewFixture(t)
	f.req.StoryContext.Stories = sampleAdoptedStories()
	raw, err := json.Marshal(f.req)
	require.NoError(t, err)
	requestFile := filepath.Join(f.cfg.RunsDir, f.req.RunID, "request.json")
	require.NoError(t, os.WriteFile(requestFile, raw, 0o600))
	changeReviewAgreement(t, f)
	got, err := f.cp.ReviewVerification(asCaller("alice@example.com", CallerUser), &gensurla.GetVerificationPayload{RunID: f.req.RunID})
	require.NoError(t, err)
	recorded, ok := got.Review.StoryContext.AsRecorded()
	require.True(t, ok)
	assert.Equal(t, storySummaryItems(f.req.StoryContext.Stories), recorded.Stories)
	assert.Equal(t, f.req.Plan.RequestRevision, got.RequestRevision)
	assert.Equal(t, gensurla.ApprovalKindBlocked, got.Review.Approval.Kind())

	// Remove the whole field as in an older on-disk request, not an explicit
	// recorded empty list; only verifyrun interprets that absence.
	var old map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &old))
	delete(old, "story_context")
	raw, err = json.Marshal(old)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(requestFile, raw, 0o600))
	got, err = f.cp.ReviewVerification(asCaller("alice@example.com", CallerUser), &gensurla.GetVerificationPayload{RunID: f.req.RunID})
	require.NoError(t, err)
	_, missing := got.Review.StoryContext.AsNotRecorded()
	assert.True(t, missing)
}

// TestReviewVerificationDoesNotOfferFailedOrIncompleteRuns keeps a completed
// failure distinct from execution failure and interruption in the real read.
func TestReviewVerificationDoesNotOfferFailedOrIncompleteRuns(t *testing.T) {
	for _, kind := range []string{"failed", "execution_failed", "interrupted"} {
		t.Run(kind, func(t *testing.T) {
			f := newReviewFixture(t)
			result := verifyrun.Result{Kind: verifyrun.ResultNoReport, Failure: verifyrun.FailureKind(kind),
				Stage: "build", Summary: "Verification stopped before producing a report"}
			if kind == "failed" {
				report := failingReport(t, f.req.Plan.RequestRevision, f.req.Plan.CandidateCommit, reportSpec{
					SuiteID: f.report.Binding.SuiteID, SuiteCommit: f.report.Binding.SuiteCommit,
					Environment: f.report.Binding.Environment, Artifacts: f.target.artifacts,
				})
				raw, err := json.Marshal(report)
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(f.cfg.Plan.ReportDir, report.Digest+".json"), raw, 0o600))
				result = verifyrun.Result{Kind: verifyrun.ResultReport, ReportDigest: report.Digest}
			}
			raw, err := json.Marshal(result)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(f.cfg.RunsDir, f.req.RunID, "result.json"), raw, 0o600))
			got, err := f.cp.ReviewVerification(asCaller("alice@example.com", CallerUser), &gensurla.GetVerificationPayload{RunID: f.req.RunID})
			require.NoError(t, err)
			assert.Equal(t, gensurla.ApprovalKindBlocked, got.Review.Approval.Kind())
			if kind == "failed" {
				completed, ok := got.State.AsCompleted()
				require.True(t, ok)
				assert.False(t, completed.Report.Passed)
			} else {
				stopped, ok := got.State.AsNotCompleted()
				require.True(t, ok)
				assert.Equal(t, kind, stopped.Kind)
				assert.Equal(t, "build", stopped.Stage)
			}
		})
	}
}

// newReviewFixture persists a passed run as the supervisor would, with a real
// candidate belonging to the current build and a recorded empty Story.
func newReviewFixture(t *testing.T) reviewFixture {
	t.Helper()
	cfg := verificationConfig(t)
	target := &promotionTargetStub{artifacts: map[string]string{"orders": "d1"}}
	cp, card := verificationSetupWithOptions(t, Options{Verification: cfg, PromotionTarget: target})
	commit := commitBuilderCandidateFixture(t, cp, card)
	report := passingReport(t, card.RequestRevision, commit, reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: cfg.Plan.SuiteCommit, Environment: "sim", Artifacts: target.artifacts,
	})
	target.report = report
	cp.mu.RLock()
	build, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found)
	plan := cfg.Plan
	plan.CandidateCommit, plan.RequestRevision, plan.Tests = commit, card.RequestRevision, verify.TestsEvaluator
	req := verifyrun.Request{RunID: verifyrun.NewRunID(), Owner: "alice@example.com", ChangeID: card.ChangeID,
		AcceptedAt: time.Now().UTC(), Plan: plan, StoryContext: verifyrun.StoryContext{
			State: verifyrun.StoryRecorded, BuildNumber: build.Number, BuildStartedAt: build.StartedAt,
		}}
	dir := filepath.Join(cfg.RunsDir, req.RunID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	for name, value := range map[string]any{
		"request.json": req,
		"result.json":  verifyrun.Result{Kind: verifyrun.ResultReport, ReportDigest: report.Digest},
	} {
		raw, err := json.Marshal(value)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), raw, 0o600))
	}
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(cfg.Plan.ReportDir, report.Digest+".json"), raw, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600))
	return reviewFixture{cp: cp, card: card, cfg: cfg, req: req, report: report, target: target}
}

// reviewApprovalPayload binds approval to the exact saved report in a fixture.
func reviewApprovalPayload(f reviewFixture) *gensurla.ApproveMergePayload {
	return &gensurla.ApproveMergePayload{ChangeID: f.req.ChangeID, RequestRevision: f.req.Plan.RequestRevision,
		CandidateCommit: f.req.Plan.CandidateCommit, ReportDigest: f.report.Digest}
}

// changeReviewAgreement changes a real confirmed example while retaining identity.
func changeReviewAgreement(t *testing.T, f reviewFixture) {
	t.Helper()
	example := *f.card.Examples[0]
	example.ExpectedOutcome = "Manager reviews the refund"
	_, err := f.cp.ConfirmIntent(asCaller("alice@example.com", CallerUser), &gensurla.ConfirmIntentPayload{
		ChangeID: f.card.ChangeID, RequestRevision: f.card.RequestRevision,
		Answers: map[string]string{}, Examples: []*gensurla.ExampleItem{&example},
	})
	require.NoError(t, err)
}
