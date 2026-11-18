//go:build browser && (darwin || linux)

// Agreement reading needs native layout and keyboard defaults; jsdom owns the
// saved-revision and draft interaction cases without starting another browser.
package project

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
)

// agreementViewFacts contains geometry and interaction observations from the
// same disposable browser at desktop and phone widths.
type agreementViewFacts struct {
	Name              string  `json:"name"`
	Width             int     `json:"width"`
	DocumentWidth     int     `json:"document_width"`
	PreviewHeight     float64 `json:"preview_height"`
	LineHeight        float64 `json:"line_height"`
	PreviewClipped    bool    `json:"preview_clipped"`
	KeyboardReachable bool    `json:"keyboard_reachable"`
	FocusVisible      bool    `json:"focus_visible"`
	FullText          string  `json:"full_text"`
	DetailsOpen       bool    `json:"details_open"`
	SummaryFocused    bool    `json:"summary_focused"`
	SummaryVisible    bool    `json:"summary_visible"`
	EndReachable      bool    `json:"end_reachable"`
	DraftPreserved    bool    `json:"draft_preserved"`
	PreviewScreenshot string  `json:"preview_screenshot"`
	SummaryScreenshot string  `json:"summary_screenshot"`
}

// TestAgreementSummaryAtDesktopAndPhoneWidths shares one browser/server for
// both widths. Native line clamping, keyboard activation and scroll position
// cannot be proved by the cheaper layout-free served-page harness.
func TestAgreementSummaryAtDesktopAndPhoneWidths(t *testing.T) {
	executable := findBrowser(t)
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	skipWithoutNodeWebSocket(t, node)
	harness, err := filepath.Abs("../../ui/checkpoint/testing/browser_harness.cjs")
	require.NoError(t, err)
	vp := startViewsProject(t, controlplane.Options{})
	guide := controlplane.WithPrincipal(t.Context(), controlplane.Principal{
		ID: vp.state.Record.Owner, CallerID: "agreement-layout-guide", Kind: controlplane.CallerGuide,
	})
	summary := strings.Repeat("Customers request a refund and receive a recorded decision. Managers review requests that exceed the agreed limit. ", 18) + "The original summary remains complete."
	card, err := vp.cp.Load().ProposeChange(guide, &gensurla.ProposeChangePayload{
		ChangeID: "agreement-layout", Prompt: "Route refunds to a manager.",
		Analysis: &gensurla.CP1Analysis{Summary: summary,
			Ambiguities: []*gensurla.CP1AnalysisAmbiguity{},
			Examples: []*gensurla.CP1AnalysisExample{{Scenario: "Refund", InputSummary: "$40",
				ExpectedOutcome: "Approved", VerificationKind: "deterministic"}},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)
	capture := os.Getenv("SURLA_TEST_AGREEMENT_CAPTURE_DIR")
	if capture == "" {
		capture = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(capture, 0o700))
	link, err := vp.link()
	require.NoError(t, err)
	browser := startBrowser(t, executable, t.TempDir())
	facts := driveBrowser(t, node, harness, browserInput{
		DevTools: browser.devtools, Origin: vp.state.Origin(), Link: link,
		ChangeIDs: []string{card.ChangeID}, Agreement: true, CaptureDirectory: capture,
	})
	require.Len(t, facts.Agreement, 2)
	for _, view := range facts.Agreement {
		assert.LessOrEqual(t, view.DocumentWidth, view.Width)
		assert.Positive(t, view.LineHeight)
		assert.InDelta(t, 2*view.LineHeight, view.PreviewHeight, 1)
		assert.True(t, view.PreviewClipped)
		assert.True(t, view.KeyboardReachable)
		assert.True(t, view.FocusVisible)
		assert.Equal(t, "Summary: "+summary, view.FullText)
		assert.True(t, view.DetailsOpen)
		assert.True(t, view.SummaryFocused)
		assert.True(t, view.SummaryVisible)
		assert.True(t, view.EndReachable)
		assert.True(t, view.DraftPreserved)
		t.Logf("agreement %s: %+v", view.Name, view)
	}
	saved, err := vp.cp.Load().GetChange(guide, &gensurla.GetChangePayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	assert.Equal(t, summary, saved.Summary)
	assert.Equal(t, "Approved", saved.Examples[0].ExpectedOutcome,
		"reading the summary must not save the draft")
	browser.stopAndCheck(t)
}
