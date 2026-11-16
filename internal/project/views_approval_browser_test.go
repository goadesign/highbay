//go:build browser && (darwin || linux)

// These captures inspect the served approval UI with typed presentation facts.
// Real admission, approval persistence and fresh-session recovery are exercised
// by the control-plane served-page tests; this fixture executes no candidate.
package project

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// browserApprovalService supplies explicit review states through the generated
// interface, retaining the real project's authentication, session and codecs.
type browserApprovalService struct {
	gensurla.Service
	// mu protects the fixture while the page's waiting reads remain in flight.
	mu sync.RWMutex
	// review is the exact selected-run result for the current presentation case.
	review *gensurla.VerificationReviewResult
	// build supplies taking facts without deriving them in the page.
	build *gensurla.ChangeBuild
}

// ReviewVerification returns the typed owner fixture without changing state.
func (s *browserApprovalService) ReviewVerification(
	context.Context, *gensurla.GetVerificationPayload,
) (*gensurla.VerificationReviewResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.review, nil
}

// GetVerification retains the existing shared status shape for background reads.
func (s *browserApprovalService) GetVerification(
	context.Context, *gensurla.GetVerificationPayload,
) (*gensurla.VerificationStatusResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return browserApprovalStatus(s.review), nil
}

// ListVerifications returns the one selected presentation run.
func (s *browserApprovalService) ListVerifications(
	context.Context, *gensurla.ListVerificationsPayload,
) (*gensurla.VerificationListResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &gensurla.VerificationListResult{ChangeID: s.review.ChangeID,
		Runs: []*gensurla.VerificationStatusResult{browserApprovalStatus(s.review)}}, nil
}

// GetBuild returns the fixture's owner-reported taking and test state.
func (s *browserApprovalService) GetBuild(
	context.Context, *gensurla.GetBuildPayload,
) (*gensurla.ChangeBuild, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.build, nil
}

// TestApprovalFrameAndEvidenceAtDesktopAndPhoneWidth captures each required
// result state, intermediate width, dark theme, keyboard access and reflow.
func TestApprovalFrameAndEvidenceAtDesktopAndPhoneWidth(t *testing.T) {
	executable := findBrowser(t)
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	skipWithoutNodeWebSocket(t, node)
	harness, err := filepath.Abs("../../ui/checkpoint/testing/browser_harness.cjs")
	require.NoError(t, err)
	vp := startViewsProject(t, controlplane.Options{})
	server := vp.server.Load()
	require.NoError(t, vp.cp.Load().Close())
	cp, err := controlplane.New(controlplane.Options{
		CardsDir: vp.state.CardsDir(),
		Team:     &controlplane.TeamBinding{Owner: vp.state.Record.Owner, Service: &phoneTeamOwner{Server: server}},
	})
	require.NoError(t, err)
	vp.cp.Store(cp)
	application := http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(cp, server.Authenticate))
	handler := server.Handler(application)
	vp.handler.Store(&handler)
	card := proposeChange(t, vp.state.Origin(), vp.agent(t, "guide"), "approval-layout", "")
	card, err = cp.ConfirmIntent(controlplane.WithPrincipal(t.Context(), controlplane.Principal{
		ID: vp.state.Record.Owner, CallerID: "approval-layout-user", Kind: controlplane.CallerUser,
	}), &gensurla.ConfirmIntentPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		Answers: map[string]string{}, Examples: card.Examples})
	require.NoError(t, err)
	service := &browserApprovalService{Service: cp}
	application = http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(service, server.Authenticate))
	handler = server.Handler(application)
	vp.handler.Store(&handler)
	capture := os.Getenv("SURLA_TEST_APPROVAL_CAPTURE_DIR")
	if capture == "" {
		capture = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(capture, 0o700))
	browser := startBrowser(t, executable, t.TempDir())
	for _, state := range []string{"current", "agreement_changed", "approved", "tests_changed", "failed", "not_completed"} {
		review, build := browserApprovalFixture(card, vp.state.Record.Owner, state)
		service.mu.Lock()
		service.review, service.build = review, build
		service.mu.Unlock()
		link, err := vp.link()
		require.NoError(t, err)
		facts := driveBrowser(t, node, harness, browserInput{DevTools: browser.devtools,
			Origin: vp.state.Origin(), Link: link, ChangeIDs: []string{}, Approval: true,
			ApprovalState: state, CaptureDirectory: capture})
		wantViews := 3
		if state == "current" {
			wantViews = 5
		}
		require.Len(t, facts.Approval, wantViews)
		for _, view := range facts.Approval {
			assert.LessOrEqual(t, view.DocumentWidth, view.Width)
			assert.Empty(t, view.Overflowing, "%s/%s", state, view.Name)
			assert.Equal(t, 2, view.FinishedFlows)
			assert.True(t, view.DraftVisible)
			assert.True(t, view.FooterReserved)
			assert.True(t, view.FlowLinesReadable)
			if state == "current" {
				assert.Equal(t, 1, view.ApproveCount)
				assert.True(t, view.ApproveInNext)
				assert.True(t, view.ApproveEnabled)
				assert.True(t, view.KeyboardReachedAction)
				assert.True(t, view.FocusVisible)
			} else {
				assert.Zero(t, view.ApproveCount, "only the owner-available state offers approval")
			}
			if state == "current" || state == "approved" {
				assert.Equal(t, []string{"git merge " + strings.Repeat("1", 40), "git merge " + strings.Repeat("2", 40)}, view.Commands)
			}
			t.Logf("presentation fixture %s/%s: %+v", state, view.Name, view)
		}
	}
	browser.stopAndCheck(t)
}

// browserApprovalFixture keeps declared flow, test and result identities aligned;
// state differences are explicit owner values, not UI-derived eligibility.
func browserApprovalFixture(
	card *gensurla.ChangeCardResult, owner, state string,
) (*gensurla.VerificationReviewResult, *gensurla.ChangeBuild) {
	candidate, tests := strings.Repeat("1", 40), strings.Repeat("2", 40)
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	flows := []*gensurla.StoryFlowItem{}
	required := []*gensurla.RequiredTest{}
	for _, flow := range []struct{ key, title, hook string }{
		{"place_order", "Place an order and receive its confirmation", "PlaceOrder"},
		{"cancel_order", "Cancel an order before dispatch and receive the refund confirmation", "CancelOrder"},
	} {
		flows = append(flows, &gensurla.StoryFlowItem{Key: flow.key, Title: flow.title, Complete: true,
			Questions: []*gensurla.StoryQuestionItem{}, Steps: []*gensurla.StoryStepItem{
				{Key: "request", ActorKey: "buyer", Summary: "Buyer requests the action", Kind: "start", Questions: []*gensurla.StoryQuestionItem{}},
				{Key: "result", ActorKey: "buyer", Summary: "Buyer sees the confirmed result", Kind: "outcome", Questions: []*gensurla.StoryQuestionItem{}},
			}})
		required = append(required, &gensurla.RequiredTest{Story: "Orders", FlowKey: flow.key, Scenario: flow.key, Hook: flow.hook})
	}
	flows = append(flows, &gensurla.StoryFlowItem{Key: "return_order", Title: "Return a delivered order",
		Steps: []*gensurla.StoryStepItem{}, Questions: []*gensurla.StoryQuestionItem{{Key: "deadline", Prompt: "When may an order be returned?", Options: []string{}}}})
	// Use the actual redaction owner, so the presentation cannot accidentally
	// treat a private holdout name as a safe declared-test identifier.
	projected := (verify.Report{Digest: strings.Repeat("a", 64), Passed: true,
		Counts: verify.Counts{Required: 3, Passed: 3},
		Scenarios: []verify.ScenarioResult{{ID: "place_order", Outcome: verify.OutcomePassed},
			{ID: "cancel_order", Outcome: verify.OutcomePassed},
			{ID: "private_holdout_case", Holdout: true, Outcome: verify.OutcomePassed}},
	}).ForBuilders()
	report := &gensurla.BuilderVerificationReport{ReportDigest: projected.ReportDigest,
		Passed: projected.Passed, Counts: &gensurla.VerificationCounts{Required: projected.Counts.Required, Passed: projected.Counts.Passed},
		Scenarios: make([]*gensurla.VerificationScenario, 0, len(projected.Scenarios))}
	for _, scenario := range projected.Scenarios {
		report.Scenarios = append(report.Scenarios, &gensurla.VerificationScenario{ID: scenario.ID, Outcome: string(scenario.Outcome)})
	}
	review := &gensurla.VerificationReviewResult{RunID: strings.Repeat("1", 32), ChangeID: card.ChangeID,
		RequestRevision: card.RequestRevision, CandidateCommit: candidate, SuiteCommit: tests, AcceptedAt: at,
		InputsCurrent: true, TestSource: gensurla.NewTestSourceWrittenTests(&gensurla.AgentWrittenTests{
			Commit: tests, Notice: "These tests were written by a separate agent on this machine and are not isolated from the builder.",
		}), State: gensurla.NewStateCompleted(&gensurla.VerificationCompleted{SuiteID: "orders_acceptance", Environment: "sim", Report: report}),
		Review: &gensurla.VerificationReview{Approval: gensurla.NewApprovalAvailable(&gensurla.RunApprovalAvailable{}),
			StoryContext: gensurla.NewStoryContextRecorded(&gensurla.RecordedRunStory{BuildNumber: 7,
				BuildStartedAt: start, Stories: []*gensurla.StorySummaryItem{{Name: "Orders", Flows: flows,
					Actors: []*gensurla.StoryActorItem{{Key: "buyer", Title: "Buyer"}}}},
				Mapping: gensurla.NewMappingMapped(&gensurla.RunStoryMapping{RequiredTests: required})})}}
	build := &gensurla.StartedBuild{RequestRevision: card.RequestRevision, StartedAt: start, InForce: true,
		RequiredTestCount: 2, Builder: &gensurla.BuilderProgress{Read: true},
		TestWriter: &gensurla.TestWriterProgress{Read: true, Report: 1, Tests: gensurla.NewTestsAccepted(&gensurla.TestsAccepted{
			Commit: tests, CheckedAt: at, NotJudged: "Surla checked that these tests build into the generated test program and declare every required scenario; it did not judge what they assert.",
		})}, Taking: gensurla.NewBuildTakingPassed(&gensurla.BuildPassed{RunID: review.RunID,
			CandidateCommit: candidate, CandidateBranch: "surla/build-7", BaseInBranch: true,
			Tests: gensurla.NewBuildPassedTestSourceWrittenTests(&gensurla.BuildPassedTests{Commit: tests, Branch: "surla/tests-7"})})}
	switch state {
	case "agreement_changed":
		review.InputsCurrent = false
		build.InForce = false
		review.Review.Approval = gensurla.NewApprovalBlocked(&gensurla.RunApprovalBlocked{Reason: "The agreed revision changed after this run. Review the current agreement and ask the builder to verify it again."})
	case "tests_changed":
		review.InputsCurrent = false
		build.Taking = gensurla.NewBuildTakingBuilding(&gensurla.BuildBuilding{})
		review.Review.Approval = gensurla.NewApprovalBlocked(&gensurla.RunApprovalBlocked{Reason: "This run used earlier tests. The builder verifies with the currently accepted tests before approval."})
	case "approved":
		review.Review.Approval = gensurla.NewApprovalRecorded(&gensurla.ApprovalItem{ApprovedBy: owner,
			ReportDigest: report.ReportDigest, CandidateCommit: candidate, SuiteCommit: tests,
			SuiteID: "orders_acceptance", Environment: "sim", ScenarioCount: 3, Artifacts: map[string]string{},
			TestSource: gensurla.NewApprovalTestSourceWrittenTests(&gensurla.AgentWrittenTests{Commit: tests,
				Notice: "These tests were written by a separate agent on this machine and are not isolated from the builder."})})
	case "failed":
		report.Passed, report.Counts.Passed, report.Counts.Failed = false, 2, 1
		report.Scenarios[0].Outcome = "failed"
		build.Taking = gensurla.NewBuildTakingBuilding(&gensurla.BuildBuilding{})
		review.Review.Approval = gensurla.NewApprovalBlocked(&gensurla.RunApprovalBlocked{Reason: "This run did not pass. The builder corrects the failing check and verifies again."})
	case "not_completed":
		review.State = gensurla.NewStateNotCompleted(&gensurla.VerificationNotCompleted{Kind: "interrupted", Stage: "suite", Summary: "The verification process stopped before completing its report."})
		build.Taking = gensurla.NewBuildTakingBuilding(&gensurla.BuildBuilding{})
		review.Review.Approval = gensurla.NewApprovalBlocked(&gensurla.RunApprovalBlocked{Reason: "No completed report is available. The builder checks the interruption and verifies again."})
	}
	return review, &gensurla.ChangeBuild{ChangeID: card.ChangeID, Build: gensurla.NewBuildStarted(build)}
}

// browserApprovalStatus projects the shared status fields without adding a
// review to the agent/history contract.
func browserApprovalStatus(r *gensurla.VerificationReviewResult) *gensurla.VerificationStatusResult {
	return &gensurla.VerificationStatusResult{RunID: r.RunID, ChangeID: r.ChangeID,
		RequestRevision: r.RequestRevision, CandidateCommit: r.CandidateCommit,
		SuiteCommit: r.SuiteCommit, AcceptedAt: r.AcceptedAt, InputsCurrent: r.InputsCurrent,
		TestSource: r.TestSource, State: r.State}
}
