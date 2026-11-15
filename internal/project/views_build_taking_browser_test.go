//go:build browser && (darwin || linux)

// This file uses the existing browser lifecycle and harness to inspect passed
// build presentation. Its typed owner fixture does not execute candidate code.
package project

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// browserTakingService supplies one saved build through its generated owner
// interface, while the real project server retains auth, cards and sessions.
type browserTakingService struct {
	gensurla.Service
	build *gensurla.ChangeBuild
}

// GetBuild returns the single presentation fixture without changing it.
func (s *browserTakingService) GetBuild(
	context.Context, *gensurla.GetBuildPayload,
) (*gensurla.ChangeBuild, error) {
	return s.build, nil
}

// TestBuildTakingAtDesktopAndPhoneWidth checks exact C then T commands, the
// rewritten-base warning, keyboard Drop consent and wrapping in one batch.
func TestBuildTakingAtDesktopAndPhoneWidth(t *testing.T) {
	executable := findBrowser(t)
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	skipWithoutNodeWebSocket(t, node)
	harness, err := filepath.Abs("../../ui/checkpoint/testing/browser_harness.cjs")
	require.NoError(t, err)
	vp := startViewsProject(t, controlplane.Options{})
	card := proposeChange(t, vp.state.Origin(), vp.agent(t, "guide"), "taking-layout", "")
	user := controlplane.WithPrincipal(t.Context(), controlplane.Principal{
		ID: vp.state.Record.Owner, CallerID: "layout-user", Kind: controlplane.CallerUser,
	})
	card, err = vp.cp.Load().ConfirmIntent(user, &gensurla.ConfirmIntentPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		Answers: map[string]string{}, Examples: card.Examples,
	})
	require.NoError(t, err)
	candidate, tests := strings.Repeat("1", 40), strings.Repeat("2", 40)
	at := time.Now().UTC().Format(time.RFC3339Nano)
	service := &browserTakingService{
		Service: vp.cp.Load(),
		build: &gensurla.ChangeBuild{
			ChangeID: card.ChangeID,
			Build: gensurla.NewBuildStarted(&gensurla.StartedBuild{
				RequestRevision: card.RequestRevision, StartedAt: at, InForce: true,
				Builder: &gensurla.BuilderProgress{Read: true},
				TestWriter: &gensurla.TestWriterProgress{Read: true, Report: 1,
					Tests: gensurla.NewTestsAccepted(&gensurla.TestsAccepted{
						Commit: tests, CheckedAt: at,
						NotJudged: "Surla checked that these tests build into the generated test program and declare every required scenario; it did not judge what they assert.",
					}),
				},
				Taking: gensurla.NewBuildTakingPassed(&gensurla.BuildPassed{
					RunID: "layout-run", CandidateCommit: candidate, CandidateBranch: "surla/build-7",
					Tests: gensurla.NewBuildPassedTestSourceWrittenTests(&gensurla.BuildPassedTests{
						Commit: tests, Branch: "surla/tests-7",
					}),
					BaseInBranch: false,
				}),
			}),
		},
	}
	server := vp.server.Load()
	application := http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(service, server.Authenticate))
	handler := server.Handler(application)
	vp.handler.Store(&handler)
	link, err := vp.link()
	require.NoError(t, err)
	capture := os.Getenv("SURLA_TEST_TAKING_CAPTURE_DIR")
	if capture == "" {
		capture = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(capture, 0o700))
	browser := startBrowser(t, executable, t.TempDir())
	facts := driveBrowser(t, node, harness, browserInput{
		DevTools: browser.devtools, Origin: vp.state.Origin(), Link: link,
		ChangeIDs: []string{}, Taking: true, CaptureDirectory: capture,
	})
	require.Len(t, facts.Taking, 2)
	for _, view := range facts.Taking {
		assert.Contains(t, []int{1280, 375}, view.Width)
		assert.LessOrEqual(t, view.DocumentWidth, view.Width)
		assert.Equal(t, []string{"git merge " + candidate, "git merge " + tests}, view.Commands)
		assert.True(t, view.RewriteWarning)
		assert.True(t, view.DropFocused)
		assert.True(t, view.DropCancelled)
		assert.Empty(t, view.Overflowing)
		t.Logf("taking presentation at %dpx: %+v", view.Width, view)
	}
	browser.stopAndCheck(t)
}
