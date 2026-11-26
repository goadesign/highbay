//go:build browser && (darwin || linux)

// Native captures use typed presentation facts; served-page tests separately
// exercise actual preparation verdicts, report identity and assignment reads.
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// browserTestReviewService serves one immutable layout case at a time through
// the real project's authentication, browser session and generated codecs.
type browserTestReviewService struct {
	genhighbay.Service
	// mu protects the fixture while background reads remain in flight.
	mu sync.RWMutex
	// build and findings name the same saved build/report in each case.
	build    *genhighbay.ChangeBuild
	findings *genhighbay.TestFindings
}

// GetBuild returns the typed saved build without recording a read.
func (s *browserTestReviewService) GetBuild(
	context.Context, *genhighbay.GetBuildPayload,
) (*genhighbay.ChangeBuild, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.build, nil
}

// GetTestFindings returns text bound to the fixture's exact saved verdict.
func (s *browserTestReviewService) GetTestFindings(
	context.Context, *genhighbay.GetTestFindingsPayload,
) (*genhighbay.TestFindings, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.findings, nil
}

// TestTestsReturnedAtDesktopAndPhoneWidth checks requirements, long findings,
// owner next steps and historical states without executing any candidate.
func TestTestsReturnedAtDesktopAndPhoneWidth(t *testing.T) {
	executable := findBrowser(t)
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	skipWithoutNodeWebSocket(t, node)
	harness, err := filepath.Abs("../../ui/checkpoint/testing/browser_harness.cjs")
	require.NoError(t, err)
	vp := startViewsProject(t, controlplane.Options{})
	card := proposeChange(t, vp.state.Origin(), vp.agent(t, "guide"), "tests-returned-layout", "")
	card, err = vp.cp.Load().ConfirmIntent(controlplane.WithPrincipal(t.Context(), controlplane.Principal{
		ID: vp.state.Record.Owner, CallerID: "tests-layout-user", Kind: controlplane.CallerUser,
	}), &genhighbay.ConfirmIntentPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		Answers: map[string]string{}, Examples: card.Examples})
	require.NoError(t, err)
	service := &browserTestReviewService{Service: vp.cp.Load()}
	application := http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(service, vp.server.Load().Authenticate))
	handler := vp.server.Load().Handler(application)
	vp.handler.Store(&handler)
	capture := os.Getenv("HIGHBAY_TEST_RETURNED_CAPTURE_DIR")
	if capture == "" {
		capture = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(capture, 0o700))
	browser := startBrowser(t, executable, t.TempDir())
	for _, state := range []string{"needs_changes", "could_not_run", "accepted", "stale", "closed"} {
		at := time.Now().UTC().Format(time.RFC3339Nano)
		required := []*genhighbay.RequiredTest{
			{Story: "Refunds and long customer-visible requirements", FlowKey: "refund-over-limit", Scenario: "refund_over_limit", Hook: "RefundOverLimit"},
			{Story: "Receipts", FlowKey: "receipt-refund", Scenario: "receipt_refund", Hook: "ReceiptRefund"},
		}
		build := &genhighbay.StartedBuild{RequestRevision: card.RequestRevision, StartedAt: at, InForce: state != "stale",
			RequiredTestCount: len(required), RequiredTests: required,
			Builder: &genhighbay.BuilderProgress{Read: true},
			TestWriter: &genhighbay.TestWriterProgress{Read: true, Report: 2,
				Tests: genhighbay.NewTestsNeedsChanges(&genhighbay.TestsNeedChanges{FindingCount: 2, CheckedAt: at})},
			Taking: genhighbay.NewBuildTakingBuilding(&genhighbay.BuildBuilding{})}
		if state == "could_not_run" {
			build.TestWriter.Tests = genhighbay.NewTestsCouldNotRun(&genhighbay.TestsCouldNotRun{
				Reason: "The tests could not be checked because their dependencies were unavailable.",
				Step:   "Restore the module connection, then ask the test writer to report again.", CheckedAt: at})
		}
		if state == "accepted" {
			build.TestWriter.Tests = genhighbay.NewTestsAccepted(&genhighbay.TestsAccepted{Commit: strings.Repeat("a", 40), CheckedAt: at,
				NotJudged: "Highbay checked that these tests build into the generated test program and declare every required scenario; it did not judge what they assert."})
		}
		if state == "closed" {
			build.Taking = genhighbay.NewBuildTakingDropped(&genhighbay.BuildDropped{KeptBranches: []string{}})
		}
		service.mu.Lock()
		service.build = &genhighbay.ChangeBuild{ChangeID: card.ChangeID, Build: genhighbay.NewBuildStarted(build)}
		service.findings = &genhighbay.TestFindings{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
			StartedAt: at, CheckedAt: at, Report: 2, Findings: []string{
				"Implement RefundOverLimit. " + strings.Repeat("The acceptance check explains the missing behavior without assigning this work to the user. ", 30),
				"The test writer corrects the reported test module and reports again.",
			}}
		service.mu.Unlock()
		link, err := vp.link()
		require.NoError(t, err)
		facts := driveBrowser(t, node, harness, browserInput{DevTools: browser.devtools,
			Origin: vp.state.Origin(), Link: link, ChangeIDs: []string{}, TestReview: true,
			TestReviewState: state, CaptureDirectory: capture})
		require.Len(t, facts.TestReview, 5)
		for _, view := range facts.TestReview {
			assert.LessOrEqual(t, view.DocumentWidth, view.Width, state+"/"+view.Name)
			assert.Equal(t, 2, view.RequiredTests, state)
			assert.Zero(t, view.NextControls, state)
			assert.True(t, view.NextInline, state)
			assert.True(t, view.FindingsBounded, state)
			assert.True(t, view.KeyboardReachable, state)
			assert.True(t, view.FooterReserved, state)
			assert.Equal(t, state == "needs_changes" || state == "stale", view.FindingsVisible, state)
			if state == "needs_changes" {
				assert.Contains(t, view.Next, "The test writer corrects the tests and reports again.")
			}
			if state == "could_not_run" {
				assert.Contains(t, view.Next, "Restore the module connection")
			}
			info, err := os.Stat(view.Screenshot)
			require.NoError(t, err)
			assert.Positive(t, info.Size())
		}
	}
}
