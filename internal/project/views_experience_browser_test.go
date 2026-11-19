//go:build browser && (darwin || linux)

// These browser journeys use the existing typed approval fixture and real
// authentication/codecs. Worker execution and Git merge behavior have their own
// owning tests; no candidate program or agent process runs here.
package project

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// browserExperienceService records explicit user actions against immutable
	// presentation fixtures, sharing the approval fixture's lock for all fields.
	browserExperienceService struct {
		*browserApprovalService
		team        *gensurla.TeamSnapshot
		options     *gensurla.ListTeamAgentOptionsResult
		card        *gensurla.ChangeCardResult
		newer       *gensurla.VerificationReviewResult
		buildStarts []*gensurla.StartBuildPayload
		teamStarts  []*gensurla.StartTeamAgentsPayload
		merges      []*gensurla.MergeChangePayload
		mergeError  error
	}

	// experienceViewFacts contains observations from Chromium's own layout,
	// focus traversal and served action feedback, not a second admission table.
	experienceViewFacts struct {
		Name             string   `json:"name"`
		Width            int      `json:"width"`
		DocumentWidth    int      `json:"document_width"`
		ControlsFit      bool     `json:"controls_fit"`
		KeyboardControls []string `json:"keyboard_controls"`
		FocusVisible     bool     `json:"focus_visible"`
		InitialClients   []string `json:"initial_clients"`
		ClientChanged    bool     `json:"client_changed"`
		DraftPreserved   bool     `json:"draft_preserved"`
		BackgroundGated  bool     `json:"background_gated"`
		NoticeDismissed  bool     `json:"notice_dismissed"`
		NoAutoLaunch     bool     `json:"no_auto_launch"`
		ManualSecondary  bool     `json:"manual_secondary"`
		MergeCount       int      `json:"merge_count"`
		ActionText       string   `json:"action_text"`
		ActionRecovered  bool     `json:"action_recovered"`
		Screenshot       string   `json:"screenshot"`
	}
)

// GetTeam returns the saved presentation facts, including persisted dismissals.
func (s *browserExperienceService) GetTeam(context.Context) (*gensurla.TeamSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.team, nil
}

// ListTeamAgentOptions supplies installed metadata without probing the machine.
func (s *browserExperienceService) ListTeamAgentOptions(
	context.Context,
) (*gensurla.ListTeamAgentOptionsResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.options, nil
}

// GetChange adds the exact recorded approval supplied by the fixture owner.
func (s *browserExperienceService) GetChange(
	context.Context, *gensurla.GetChangePayload,
) (*gensurla.ChangeCardResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.card, nil
}

// ListVerifications exposes a newer pass alongside the older approved pass.
// The page must preserve the live approval's exact inputs when it merges.
func (s *browserExperienceService) ListVerifications(
	context.Context, *gensurla.ListVerificationsPayload,
) (*gensurla.VerificationListResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &gensurla.VerificationListResult{ChangeID: s.review.ChangeID,
		Runs: []*gensurla.VerificationStatusResult{browserApprovalStatus(s.newer), browserApprovalStatus(s.review)}}, nil
}

// ReviewVerification returns the selected run's own approval and evidence.
func (s *browserExperienceService) ReviewVerification(
	_ context.Context, p *gensurla.GetVerificationPayload,
) (*gensurla.VerificationReviewResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p.RunID == s.newer.RunID {
		return s.newer, nil
	}
	return s.review, nil
}

// GetVerification retains the distinct candidates of both saved passes.
func (s *browserExperienceService) GetVerification(
	_ context.Context, p *gensurla.GetVerificationPayload,
) (*gensurla.VerificationStatusResult, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p.RunID == s.newer.RunID {
		return browserApprovalStatus(s.newer), nil
	}
	return browserApprovalStatus(s.review), nil
}

// DismissTeamNotice persists the notice in the fixture's owner result. The
// browser must read this value again after navigation to prove dismissal.
func (s *browserExperienceService) DismissTeamNotice(
	_ context.Context, p *gensurla.DismissTeamNoticePayload,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := *s.team
	next.DismissedNotices = append(append([]string{}, s.team.DismissedNotices...), p.Notice)
	s.team = &next
	return nil
}

// StartBuild records only the explicit request and returns an existing typed
// started build; this is presentation evidence, not worker-execution evidence.
func (s *browserExperienceService) StartBuild(
	_ context.Context, p *gensurla.StartBuildPayload,
) (*gensurla.StartBuildResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buildStarts = append(s.buildStarts, p)
	started, ok := s.build.Build.AsStarted()
	if !ok {
		return nil, errors.New("browser fixture needs a started build receipt")
	}
	return &gensurla.StartBuildResult{Build: started, Team: gensurla.NewTeamReady(s.teamReceipt())}, nil
}

// StartTeamAgents records the user-triggered missing-worker request.
func (s *browserExperienceService) StartTeamAgents(
	_ context.Context, p *gensurla.StartTeamAgentsPayload,
) (*gensurla.ProjectTeam, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teamStarts = append(s.teamStarts, p)
	return s.teamReceipt(), nil
}

// MergeChange returns a declared owner refusal or actual branch/commit receipt.
// The browser is responsible only for binding approval and presenting recovery.
func (s *browserExperienceService) MergeChange(
	_ context.Context, p *gensurla.MergeChangePayload,
) (*gensurla.MergeChangeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merges = append(s.merges, p)
	if s.mergeError != nil {
		return nil, s.mergeError
	}
	started, ok := s.build.Build.AsStarted()
	if !ok {
		return nil, errors.New("browser fixture needs a started build to merge")
	}
	merged := *started
	merged.Taking = gensurla.NewBuildTakingMerged(&gensurla.BuildMerged{})
	s.build = &gensurla.ChangeBuild{ChangeID: p.ChangeID, Build: gensurla.NewBuildStarted(&merged)}
	return &gensurla.MergeChangeResult{Branch: "feature/orders", HeadCommit: strings.Repeat("3", 40)}, nil
}

// teamReceipt names the fixture's execution mode without starting a process.
func (s *browserExperienceService) teamReceipt() *gensurla.ProjectTeam {
	return &gensurla.ProjectTeam{Team: s.team.Team, Space: "fixture-space", HerdrMode: s.team.HerdrMode,
		Roles: []*gensurla.ProjectTeamRole{}, Step: "Read the build assignments."}
}

// TestInlineWorkersAndApprovedMergeInChromium proves the changed user journey
// at desktop and phone widths. The batch also preserves dark/reflow inspection
// of the changed controls, while all candidate execution remains out of scope.
func TestInlineWorkersAndApprovedMergeInChromium(t *testing.T) {
	executable := findBrowser(t)
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	skipWithoutNodeWebSocket(t, node)
	harness, err := filepath.Abs("../../ui/checkpoint/testing/browser_harness.cjs")
	require.NoError(t, err)
	vp := startViewsProject(t, controlplane.Options{})
	card := proposeChange(t, vp.state.Origin(), vp.agent(t, "guide"), "experience-layout", "")
	card, err = vp.cp.Load().ConfirmIntent(controlplane.WithPrincipal(t.Context(), controlplane.Principal{
		ID: vp.state.Record.Owner, CallerID: "experience-layout-user", Kind: controlplane.CallerUser,
	}), &gensurla.ConfirmIntentPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		Answers: map[string]string{}, Examples: card.Examples})
	require.NoError(t, err)
	service := &browserExperienceService{browserApprovalService: &browserApprovalService{Service: vp.cp.Load()}}
	application := http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(service, vp.server.Load().Authenticate))
	handler := vp.server.Load().Handler(application)
	vp.handler.Store(&handler)
	capture := os.Getenv("SURLA_TEST_EXPERIENCE_CAPTURE_DIR")
	if capture == "" {
		capture = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(capture, 0o700))
	browser := startBrowser(t, executable, t.TempDir())
	for _, state := range []string{"picker", "headless", "single", "builder_only", "configured_permissions", "merge", "build_merge", "dirty_worktree", "merge_conflict"} {
		t.Run(state, func(t *testing.T) {
			mergeJourney := state == "merge" || state == "build_merge" || state == "dirty_worktree" || state == "merge_conflict"
			views := []string{"desktop", "phone"}
			if state == "picker" || state == "merge" {
				views = append(views, "dark", "reflow")
			}
			for _, viewName := range views {
				review, build := browserApprovalFixture(card, vp.state.Record.Owner, "approved")
				newer, _ := browserApprovalFixture(card, vp.state.Record.Owner, "current")
				newer.RunID, newer.CandidateCommit = strings.Repeat("2", 32), strings.Repeat("4", 40)
				newer.AcceptedAt = "2026-11-16T12:00:00Z"
				fixtureCard := *card
				fixtureCard.Approval, _ = review.Review.Approval.AsRecorded()
				team := &gensurla.TeamSnapshot{Team: "browser-team", HerdrMode: "attached",
					Roles: []*gensurla.TeamRoleState{
						{Role: "builder", Handle: "builder", State: "not_started", Attention: "none", Watcher: "unknown", Assignment: "unread"},
						{Role: "test_writer", Handle: "test-writer", State: "not_started", Attention: "none", Watcher: "unknown", Assignment: "unread"},
					}, SavedChoices: map[string]*gensurla.RoleAgentChoice{}, DismissedNotices: []string{}}
				options := browserWorkerOptions()
				if state == "headless" || state == "configured_permissions" {
					team.HerdrMode = "unavailable"
				}
				if state == "single" {
					options.Agents = options.Agents[:1]
				}
				if state == "builder_only" {
					options.BuildRoles = []string{"builder"}
					team.Roles = team.Roles[:1]
				}
				if state == "configured_permissions" {
					options.Agents = []*gensurla.InstalledAgentOption{{Client: "opencode", Program: "/fixture/opencode",
						Title: "OpenCode", Models: []string{}, Efforts: []string{}, ModelSupported: true, HeadlessSupported: true}}
				}
				if !mergeJourney {
					started, ok := build.Build.AsStarted()
					require.True(t, ok)
					started.Taking = gensurla.NewBuildTakingBuilding(&gensurla.BuildBuilding{})
					fixtureCard.Approval = nil
				}
				service.mu.Lock()
				service.review, service.build, service.card, service.team, service.options = review, build, &fixtureCard, team, options
				service.newer = newer
				service.buildStarts, service.teamStarts, service.merges, service.mergeError = nil, nil, nil, nil
				if state == "dirty_worktree" {
					service.mergeError = gensurla.MakeDirtyWorktree(errors.New("Save or commit tracked changes in orders.go, then retry the merge."))
				}
				if state == "merge_conflict" {
					service.mergeError = gensurla.MakeMergeConflict(errors.New("The merge conflicted. Your original branch and tracked files were restored. Review the commits, then retry."))
				}
				service.mu.Unlock()
				link, err := vp.link()
				require.NoError(t, err)
				facts := driveBrowser(t, node, harness, browserInput{DevTools: browser.devtools,
					Origin: vp.state.Origin(), Link: link, ChangeIDs: []string{}, Experience: state,
					ExperienceView: viewName, CaptureDirectory: capture})
				require.NotEmpty(t, facts.Experience)
				for _, view := range facts.Experience {
					assert.LessOrEqual(t, view.DocumentWidth, view.Width, view.Name)
					assert.True(t, view.ControlsFit, view.Name)
					assert.True(t, view.FocusVisible, view.Name)
					assert.True(t, view.NoAutoLaunch, view.Name)
					if mergeJourney {
						assert.Equal(t, 1, view.MergeCount, view.Name)
						assert.True(t, view.ManualSecondary, view.Name)
						assert.True(t, view.ActionRecovered, view.Name)
						if state == "merge" || state == "build_merge" {
							assert.Contains(t, view.ActionText, "feature/orders")
							assert.Contains(t, view.ActionText, "333333333333")
						}
					} else {
						assert.True(t, view.ClientChanged, view.Name)
						assert.True(t, view.DraftPreserved, view.Name)
						assert.Contains(t, view.KeyboardControls, "surla-build-agents-builder-client", view.Name)
						if state == "headless" || state == "configured_permissions" {
							assert.True(t, view.BackgroundGated, view.Name)
						}
						if state == "single" {
							assert.Equal(t, []string{"codex", "codex"}, view.InitialClients)
							assert.True(t, view.NoticeDismissed, view.Name)
						}
					}
					t.Logf("%s/%s native facts: %+v", state, view.Name, view)
				}
				service.mu.RLock()
				merges := append([]*gensurla.MergeChangePayload{}, service.merges...)
				buildStarts := append([]*gensurla.StartBuildPayload{}, service.buildStarts...)
				teamStarts := append([]*gensurla.StartTeamAgentsPayload{}, service.teamStarts...)
				service.mu.RUnlock()
				if mergeJourney {
					require.NotEmpty(t, merges)
					for _, request := range merges {
						assert.Equal(t, &gensurla.MergeChangePayload{ChangeID: card.ChangeID,
							RequestRevision: review.RequestRevision, CandidateCommit: review.CandidateCommit,
							ReportDigest: fixtureCard.Approval.ReportDigest}, request)
					}
					assert.Empty(t, buildStarts)
					assert.Empty(t, teamStarts)
				} else {
					require.NotEmpty(t, teamStarts, "only the explicit Team action should start fixture workers")
					require.NotEmpty(t, buildStarts, "the explicit Build confirmation should carry the selected choices")
					for _, request := range teamStarts {
						assert.NotContains(t, request.Choices, "guide")
						require.Contains(t, request.Choices, "builder")
						require.NotNil(t, request.Choices["builder"].Model)
						assert.Equal(t, "custom/browser-model", *request.Choices["builder"].Model)
					}
					for _, request := range buildStarts {
						assert.NotContains(t, request.Choices, "guide")
						require.Contains(t, request.Choices, "builder")
						require.NotNil(t, request.Choices["builder"].Model)
						assert.Equal(t, "custom/browser-model", *request.Choices["builder"].Model)
						if state == "builder_only" {
							assert.NotContains(t, request.Choices, "test_writer")
						}
					}
				}
			}
		})
	}
	browser.stopAndCheck(t)
}

// browserWorkerOptions mirrors installed metadata with both model families,
// custom-model inputs and explicit permission capability for native interaction.
func browserWorkerOptions() *gensurla.ListTeamAgentOptionsResult {
	return &gensurla.ListTeamAgentOptionsResult{BuildRoles: []string{"builder", "test_writer"},
		Agents: []*gensurla.InstalledAgentOption{
			{Client: "codex", Program: "/fixture/codex", Title: "Codex", Models: []string{"codex-model"}, Efforts: []string{"low", "high"},
				ModelSupported: true, ThinkingSupported: true, YoloSupported: true, HeadlessSupported: true},
			{Client: "claude", Program: "/fixture/claude", Title: "Claude Code", Models: []string{"claude-model"}, Efforts: []string{"low", "high"},
				ModelSupported: true, ThinkingSupported: true, YoloSupported: true, HeadlessSupported: true},
		}}
}
