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

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// browserApprovalService supplies explicit review states through the generated
// interface, retaining the real project's authentication, session and codecs.
type browserApprovalService struct {
	genhighbay.Service
	// mu protects the fixture while the page's waiting reads remain in flight.
	mu sync.RWMutex
	// review is the exact selected-run result for the current presentation case.
	review *genhighbay.VerificationReviewResult
	// build supplies taking facts without deriving them in the page.
	build *genhighbay.ChangeBuild
	// card supplies the same live approval as the selected-run fixture.
	card *genhighbay.ChangeCardResult
}

// GetChange returns the matching live card when the presentation fixture sets
// one, retaining the actual owner for other browser fixture users.
func (s *browserApprovalService) GetChange(
	ctx context.Context, p *genhighbay.GetChangePayload,
) (*genhighbay.ChangeCardResult, error) {
	s.mu.RLock()
	card := s.card
	s.mu.RUnlock()
	if card != nil {
		return card, nil
	}
	return s.Service.GetChange(ctx, p)
}

// ReviewVerification returns the typed owner fixture without changing state.
func (s *browserApprovalService) ReviewVerification(
	context.Context, *genhighbay.GetVerificationPayload,
) (*genhighbay.VerificationReviewResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.review, nil
}

// GetVerification retains the existing shared status shape for background reads.
func (s *browserApprovalService) GetVerification(
	context.Context, *genhighbay.GetVerificationPayload,
) (*genhighbay.VerificationStatusResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return browserApprovalStatus(s.review), nil
}

// ListVerifications returns the one selected presentation run.
func (s *browserApprovalService) ListVerifications(
	context.Context, *genhighbay.ListVerificationsPayload,
) (*genhighbay.VerificationListResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &genhighbay.VerificationListResult{ChangeID: s.review.ChangeID,
		Runs: []*genhighbay.VerificationStatusResult{browserApprovalStatus(s.review)}}, nil
}

// GetBuild returns the fixture's owner-reported taking and test state.
func (s *browserApprovalService) GetBuild(
	context.Context, *genhighbay.GetBuildPayload,
) (*genhighbay.ChangeBuild, error) {
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
		CardsDir:     vp.state.CardsDir(),
		WorkspaceDir: vp.state.Record.Directory,
		Team:         &controlplane.TeamBinding{Owner: vp.state.Record.Owner, Service: &phoneTeamOwner{Server: server}},
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
	}), &genhighbay.ConfirmIntentPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		Answers: map[string]string{}, Examples: card.Examples})
	require.NoError(t, err)
	service := &browserApprovalService{Service: cp}
	application = http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(service, server.Authenticate))
	handler = server.Handler(application)
	vp.handler.Store(&handler)
	capture := os.Getenv("HIGHBAY_TEST_APPROVAL_CAPTURE_DIR")
	if capture == "" {
		capture = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(capture, 0o700))
	browser := startBrowser(t, executable, t.TempDir())
	for _, state := range []string{"current", "agreement_changed", "approved", "tests_changed", "failed", "not_completed"} {
		review, build := browserApprovalFixture(card, vp.state.Record.Owner, state)
		fixtureCard := *card
		fixtureCard.Approval, _ = review.Review.Approval.AsRecorded()
		service.mu.Lock()
		service.review, service.build, service.card = review, build, &fixtureCard
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
			assert.True(t, view.PrimaryAtRest)
			if state == "current" {
				assert.Equal(t, 1, view.ApproveCount)
				assert.True(t, view.ApproveInNext)
				assert.True(t, view.ApproveEnabled)
				assert.True(t, view.KeyboardReachedAction)
				assert.True(t, view.FocusVisible)
				assert.True(t, view.CoverageLinked, "the native display journey must retain the same statements from Build to Approve")
			} else {
				assert.Zero(t, view.ApproveCount, "only the owner-available state offers approval")
			}
			if state == "approved" {
				assert.Equal(t, []string{"git merge " + strings.Repeat("1", 40), "git merge " + strings.Repeat("2", 40)}, view.Commands)
			} else {
				assert.Empty(t, view.Commands, "manual merge instructions require a live recorded approval")
			}
			t.Logf("presentation fixture %s/%s: %+v", state, view.Name, view)
		}
	}
	browser.stopAndCheck(t)
}

// browserApprovalFixture keeps declared flow, test and result identities aligned;
// state differences are explicit owner values, not UI-derived eligibility.
func browserApprovalFixture(
	card *genhighbay.ChangeCardResult, owner, state string,
) (*genhighbay.VerificationReviewResult, *genhighbay.ChangeBuild) {
	candidate, tests := strings.Repeat("1", 40), strings.Repeat("2", 40)
	start := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	at := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	flows := []*genhighbay.StoryFlowItem{}
	required := []*genhighbay.RequiredTest{}
	for _, flow := range []struct{ key, title, hook string }{
		{"place_order", "Place an order and receive its confirmation", "PlaceOrder"},
		{"cancel_order", "Cancel an order before dispatch and receive the refund confirmation", "CancelOrder"},
	} {
		flows = append(flows, &genhighbay.StoryFlowItem{Key: flow.key, Title: flow.title, Complete: true,
			Questions: []*genhighbay.StoryQuestionItem{}, Steps: []*genhighbay.StoryStepItem{
				{Key: "request", ActorKey: "buyer", Summary: "Buyer requests the action", Kind: "start", Questions: []*genhighbay.StoryQuestionItem{}},
				{Key: "result", ActorKey: "buyer", Summary: "Buyer sees the confirmed result", Kind: "outcome", Questions: []*genhighbay.StoryQuestionItem{}},
			}})
		required = append(required, &genhighbay.RequiredTest{Story: "Orders", FlowKey: flow.key, Scenario: flow.key, Hook: flow.hook})
	}
	flows = append(flows, &genhighbay.StoryFlowItem{Key: "return_order", Title: "Return a delivered order",
		Steps: []*genhighbay.StoryStepItem{}, Questions: []*genhighbay.StoryQuestionItem{{Key: "deadline", Prompt: "When may an order be returned?", Options: []string{}}}})
	// Use the actual redaction owner, so the presentation cannot accidentally
	// treat a private holdout name as a safe declared-test identifier.
	projected := (verify.Report{Digest: strings.Repeat("a", 64), Passed: true,
		Counts: verify.Counts{Required: 3, Passed: 3},
		Scenarios: []verify.ScenarioResult{{ID: "place_order", Outcome: verify.OutcomePassed},
			{ID: "cancel_order", Outcome: verify.OutcomePassed},
			{ID: "private_holdout_case", Holdout: true, Outcome: verify.OutcomePassed}},
	}).ForBuilders()
	report := &genhighbay.BuilderVerificationReport{ReportDigest: projected.ReportDigest,
		Passed: projected.Passed, Counts: &genhighbay.VerificationCounts{Required: projected.Counts.Required, Passed: projected.Counts.Passed},
		Scenarios: make([]*genhighbay.VerificationScenario, 0, len(projected.Scenarios))}
	for _, scenario := range projected.Scenarios {
		report.Scenarios = append(report.Scenarios, &genhighbay.VerificationScenario{ID: scenario.ID, Outcome: string(scenario.Outcome)})
	}
	coverage := &genhighbay.RecordedTestCoverage{
		Report: 1, Answers: []*genhighbay.ConfirmedAnswer{{ID: "cancel_before_dispatch", Question: "May a buyer cancel before dispatch?", Answer: "Yes"}},
		Examples:           []*genhighbay.ExampleItem{{ID: "check_01", Scenario: "Cancel an order before dispatch", InputSummary: "An order awaiting dispatch", ExpectedOutcome: "Cancellation and refund confirmed", VerificationKind: "deterministic"}},
		CoverageStatements: []*genhighbay.TestCoverageStatement{{Scenario: "cancel_order", QuestionIds: []genhighbay.TestCoverageReference{"cancel_before_dispatch"}, ExampleIds: []genhighbay.TestCoverageReference{"check_01"}, Explanation: "The buyer cancels the waiting order and the test asserts the cancellation and refund confirmation."}},
	}
	review := &genhighbay.VerificationReviewResult{RunID: strings.Repeat("1", 32), ChangeID: card.ChangeID,
		RequestRevision: card.RequestRevision, CandidateCommit: candidate, SuiteCommit: tests, AcceptedAt: at,
		InputsCurrent: true, TestSource: genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{
			Commit: tests, Notice: "These tests were written by a separate agent on this machine and are not isolated from the builder.",
		}), State: genhighbay.NewStateCompleted(&genhighbay.VerificationCompleted{SuiteID: "orders_acceptance", Environment: "sim", Report: report}),
		Review: &genhighbay.VerificationReview{Approval: genhighbay.NewApprovalAvailable(&genhighbay.RunApprovalAvailable{}),
			StoryContext: genhighbay.NewStoryContextRecorded(&genhighbay.RecordedRunStory{BuildNumber: 7,
				TestCoverage:   genhighbay.NewTestCoverageRecorded(coverage),
				BuildStartedAt: start, Stories: []*genhighbay.StorySummaryItem{{Name: "Orders", Flows: flows,
					Actors: []*genhighbay.StoryActorItem{{Key: "buyer", Title: "Buyer"}}}},
				Mapping: genhighbay.NewMappingMapped(&genhighbay.RunStoryMapping{RequiredTests: required})})}}
	build := &genhighbay.StartedBuild{RequestRevision: card.RequestRevision, StartedAt: start, InForce: true,
		Answers: coverage.Answers, Examples: coverage.Examples, CoverageStatements: coverage.CoverageStatements,
		RequiredTestCount: len(required), RequiredTests: required, Builder: &genhighbay.BuilderProgress{Read: true},
		TestWriter: &genhighbay.TestWriterProgress{Read: true, Report: 1, Tests: genhighbay.NewTestsAccepted(&genhighbay.TestsAccepted{
			Commit: tests, CheckedAt: at, NotJudged: "Highbay checked that these tests build into the generated test program and declare every required scenario; it did not judge what they assert.",
		})}, Taking: genhighbay.NewBuildTakingPassed(&genhighbay.BuildPassed{RunID: review.RunID,
			CandidateCommit: candidate, CandidateBranch: "highbay/build-7", BaseInBranch: true,
			Tests: genhighbay.NewBuildPassedTestSourceWrittenTests(&genhighbay.BuildPassedTests{Commit: tests, Branch: "highbay/tests-7"})})}
	switch state {
	case "agreement_changed":
		review.InputsCurrent = false
		build.InForce = false
		review.Review.Approval = genhighbay.NewApprovalBlocked(&genhighbay.RunApprovalBlocked{Reason: "The agreed revision changed after this run. Review the current agreement and ask the builder to verify it again."})
	case "tests_changed":
		review.InputsCurrent = false
		build.Taking = genhighbay.NewBuildTakingBuilding(&genhighbay.BuildBuilding{})
		review.Review.Approval = genhighbay.NewApprovalBlocked(&genhighbay.RunApprovalBlocked{Reason: "This run used earlier tests. The builder verifies with the currently accepted tests before approval."})
	case "approved":
		review.Review.Approval = genhighbay.NewApprovalRecorded(&genhighbay.ApprovalItem{ApprovedBy: owner,
			ReportDigest: report.ReportDigest, CandidateCommit: candidate, SuiteCommit: tests,
			SuiteID: "orders_acceptance", Environment: "sim", ScenarioCount: 3, Artifacts: map[string]string{},
			TestSource: genhighbay.NewApprovalTestSourceWrittenTests(&genhighbay.AgentWrittenTests{Commit: tests,
				Notice: "These tests were written by a separate agent on this machine and are not isolated from the builder."})})
	case "failed":
		report.Passed, report.Counts.Passed, report.Counts.Failed = false, 2, 1
		report.Scenarios[0].Outcome = "failed"
		build.Taking = genhighbay.NewBuildTakingBuilding(&genhighbay.BuildBuilding{})
		review.Review.Approval = genhighbay.NewApprovalBlocked(&genhighbay.RunApprovalBlocked{Reason: "This run did not pass. The builder corrects the failing check and verifies again."})
	case "not_completed":
		review.State = genhighbay.NewStateNotCompleted(&genhighbay.VerificationNotCompleted{Kind: "interrupted", Stage: "suite", Summary: "The verification process stopped before completing its report."})
		build.Taking = genhighbay.NewBuildTakingBuilding(&genhighbay.BuildBuilding{})
		review.Review.Approval = genhighbay.NewApprovalBlocked(&genhighbay.RunApprovalBlocked{Reason: "No completed report is available. The builder checks the interruption and verifies again."})
	}
	return review, &genhighbay.ChangeBuild{ChangeID: card.ChangeID, Build: genhighbay.NewBuildStarted(build)}
}

// browserApprovalStatus projects the shared status fields without adding a
// review to the agent/history contract.
func browserApprovalStatus(r *genhighbay.VerificationReviewResult) *genhighbay.VerificationStatusResult {
	return &genhighbay.VerificationStatusResult{RunID: r.RunID, ChangeID: r.ChangeID,
		RequestRevision: r.RequestRevision, CandidateCommit: r.CandidateCommit,
		SuiteCommit: r.SuiteCommit, AcceptedAt: r.AcceptedAt, InputsCurrent: r.InputsCurrent,
		TestSource: r.TestSource, State: r.State}
}
