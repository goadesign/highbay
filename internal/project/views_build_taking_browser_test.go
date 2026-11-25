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
	"sync"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// browserTakingService supplies one saved build through its generated owner
// interface, while the real project server retains auth, cards and sessions.
type browserTakingService struct {
	genhighbay.Service
	mu    sync.RWMutex
	build *genhighbay.ChangeBuild
}

// GetBuild returns the single presentation fixture without changing it.
func (s *browserTakingService) GetBuild(
	context.Context, *genhighbay.GetBuildPayload,
) (*genhighbay.ChangeBuild, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
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
	server := vp.server.Load()
	// Bind a stand-in team for Build-state captures without starting workers.
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
	card := proposeChange(t, vp.state.Origin(), vp.agent(t, "guide"), "taking-layout", "")
	user := controlplane.WithPrincipal(t.Context(), controlplane.Principal{
		ID: vp.state.Record.Owner, CallerID: "layout-user", Kind: controlplane.CallerUser,
	})
	card, err = vp.cp.Load().ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		Answers: map[string]string{}, Examples: card.Examples,
	})
	require.NoError(t, err)
	candidate, tests := strings.Repeat("1", 40), strings.Repeat("2", 40)
	at := time.Now().UTC().Format(time.RFC3339Nano)
	service := &browserTakingService{
		Service: vp.cp.Load(),
		build: &genhighbay.ChangeBuild{
			ChangeID: card.ChangeID,
			Build: genhighbay.NewBuildStarted(&genhighbay.StartedBuild{
				RequestRevision: card.RequestRevision, StartedAt: at, InForce: true,
				RequiredTests: []*genhighbay.RequiredTest{},
				Builder:       &genhighbay.BuilderProgress{Read: true},
				TestWriter: &genhighbay.TestWriterProgress{Read: true, Report: 1,
					Tests: genhighbay.NewTestsAccepted(&genhighbay.TestsAccepted{
						Commit: tests, CheckedAt: at,
						NotJudged: "Highbay checked that these tests build into the generated test program and declare every required scenario; it did not judge what they assert.",
					}),
				},
				Taking: genhighbay.NewBuildTakingPassed(&genhighbay.BuildPassed{
					RunID: "layout-run", CandidateCommit: candidate, CandidateBranch: "highbay/build-7",
					Tests: genhighbay.NewBuildPassedTestSourceWrittenTests(&genhighbay.BuildPassedTests{
						Commit: tests, Branch: "highbay/tests-7",
					}),
					BaseInBranch: false,
				}),
			}),
		},
	}
	application = http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(service, server.Authenticate))
	handler = server.Handler(application)
	vp.handler.Store(&handler)
	capture := os.Getenv("HIGHBAY_TEST_TAKING_CAPTURE_DIR")
	if capture == "" {
		capture = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(capture, 0o700))
	browser := startBrowser(t, executable, t.TempDir())
	started, ok := service.build.Build.AsStarted()
	require.True(t, ok)
	passed := started.Taking
	for _, state := range []string{"passed", "building", "dropped", "merged", "replay", "dropped_stale"} {
		fixture := *started
		switch state {
		case "passed":
			fixture.Taking = passed
		case "building":
			fixture.Taking = genhighbay.NewBuildTakingBuilding(&genhighbay.BuildBuilding{})
		case "dropped", "dropped_stale":
			fixture.Taking = genhighbay.NewBuildTakingDropped(&genhighbay.BuildDropped{KeptBranches: []string{}})
		case "merged", "replay":
			fixture.InForce = state == "replay"
			if state == "merged" {
				fixture.RequestRevision = strings.Repeat("0", 64)
			}
			fixture.Taking = genhighbay.NewBuildTakingMerged(&genhighbay.BuildMerged{})
		}
		service.mu.Lock()
		service.build = &genhighbay.ChangeBuild{
			ChangeID: card.ChangeID, DraftedBeforeDrop: state == "dropped_stale",
			Build: genhighbay.NewBuildStarted(&fixture),
		}
		service.mu.Unlock()
		link, err := vp.link()
		require.NoError(t, err)
		facts := driveBrowser(t, node, harness, browserInput{
			DevTools: browser.devtools, Origin: vp.state.Origin(), Link: link,
			ChangeIDs: []string{}, Taking: true, TakingState: state, CaptureDirectory: capture,
		})
		require.Len(t, facts.Taking, 2)
		for _, view := range facts.Taking {
			assert.Contains(t, []int{1280, 375}, view.Width)
			assert.LessOrEqual(t, view.DocumentWidth, view.Width)
			assert.Empty(t, view.Overflowing)
			assert.Empty(t, view.Commands, "a passed result alone does not authorize manual merge")
			if state == "passed" || state == "building" || state == "replay" {
				assert.True(t, view.QuestionFocused)
				assert.True(t, view.QuestionCancelled)
			}
			if state == "dropped" {
				assert.False(t, view.NoStart)
			}
			if state == "replay" || state == "dropped_stale" {
				assert.True(t, view.NoStart)
			}
			assert.Equal(t, state == "dropped_stale", view.EnablingStep)
			if state == "merged" || state == "dropped" || state == "replay" {
				assert.True(t, view.Explanation)
			}
			t.Logf("%s presentation at %dpx: %+v", state, view.Width, view)
		}
	}
	browser.stopAndCheck(t)
}
