// This file guards existing views-page contracts across Team role controls,
// message drafting, Story context attachments, Live target/model read errors,
// uncertain build-start failures, and concurrent merge serialization.
package controlplane

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/stretchr/testify/require"
)

// TestViewsPageTeamInterruptStopRestartChoiceAndMessageContracts exercises:
// (a) interrupt_team_role and stop_team_role plus #highbay-team-action-error;
// (b) restart_team_role sending choice only when edited by the user;
// (c) send_team_message attaching guide_choice when Guide needs launch,
// blocking without valid installed agents, preserving drafts on failure, and
// clearing drafts on success; and
// (i) Story Ask Guide focusing the composer and sending story_flow context.
func TestViewsPageTeamInterruptStopRestartChoiceAndMessageContracts(t *testing.T) {
	team := &teamServiceFixture{
		snapshot: genhighbay.TeamSnapshot{
			Team:         "protective-team",
			Channel:      "#protective-team",
			ChorusStatus: "connected",
			Roles: []*genhighbay.TeamRoleState{
				{Role: "guide", Handle: "guide-1", HasTerminal: false, State: "not_started", Attention: "none", Assignment: "no_work"},
				{Role: "builder", Handle: "builder-1", HasTerminal: true, State: "working", Attention: "none", Assignment: "nothing_owed"},
				{Role: "test_writer", Handle: "writer-1", HasTerminal: true, State: "waiting", Attention: "none", Assignment: "nothing_owed"},
			},
			SavedChoices:     map[string]*genhighbay.RoleAgentChoice{},
			DismissedNotices: []string{},
		},
	}
	var emptyOptions, interruptFail, sendFail atomic.Bool
	team.options = func(context.Context) ([]*genhighbay.InstalledAgentOption, error) {
		if emptyOptions.Load() {
			return []*genhighbay.InstalledAgentOption{}, nil
		}
		return fixtureAgentOptions(), nil
	}
	team.interrupt = func(_ context.Context, role string) error {
		if interruptFail.Load() {
			return genhighbay.MakeRoleNotRunning(errors.New(role + " terminal already stopped"))
		}
		return nil
	}
	team.send = func(_ context.Context, p *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error) {
		if sendFail.Load() {
			return nil, genhighbay.MakeUnprocessable(errors.New("Guide client is not supported."))
		}
		team.mu.Lock()
		next := team.snapshot
		roles := make([]*genhighbay.TeamRoleState, len(next.Roles))
		for i, r := range next.Roles {
			copied := *r
			roles[i] = &copied
		}
		roles[0].State = "working"
		next.Roles = roles
		team.snapshot = next
		team.mu.Unlock()
		return fixtureTeamMessage(p.Text), nil
	}

	workspace := t.TempDir()
	writeWorkspaceFiles(t, workspace, validWorkspaceStub("package design\n"))
	cp := newBuildControlPlane(t, Options{
		WorkspaceDir: workspace,
		Team:         &TeamBinding{Owner: pageOwner, Service: team},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond

	ps := startPageServer(t, cp)
	ps.act("empty-options", func() { emptyOptions.Store(true) })
	ps.act("restore-options", func() { emptyOptions.Store(false) })
	ps.act("interrupt-fail", func() { interruptFail.Store(true) })
	ps.act("interrupt-ok", func() { interruptFail.Store(false) })
	ps.act("send-fail", func() { sendFail.Store(true) })
	ps.act("send-ok", func() { sendFail.Store(false) })
	ps.act("guide-running-with-story-card", func() {
		team.mu.Lock()
		next := team.snapshot
		roles := make([]*genhighbay.TeamRoleState, len(next.Roles))
		for i, r := range next.Roles {
			copied := *r
			roles[i] = &copied
		}
		roles[0].HasTerminal = true
		roles[0].State = "waiting"
		next.Roles = roles
		team.snapshot = next
		team.mu.Unlock()

		guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
		op := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
		proposeWithStory(t, cp, guide, "chg-protect", op)
		card := attachBuildSnapshot(t, cp, "chg-protect")
		_, err := cp.ConfirmIntent(asCaller(pageOwner, CallerUser), &genhighbay.ConfirmIntentPayload{
			ChangeID:        card.ChangeID,
			RequestRevision: card.RequestRevision,
			Answers:         map[string]string{},
			Examples:        card.Examples,
		})
		require.NoError(t, err)
	})

	runPageScript(t, ps, nil, `
const setVal = (page, el, val) => {
  const proto = el.tagName === "TEXTAREA" ? page.win.HTMLTextAreaElement.prototype : page.win.HTMLInputElement.prototype;
  Object.getOwnPropertyDescriptor(proto, "value").set.call(el, val);
  el.dispatchEvent(new page.win.Event("input", { bubbles: true }));
};
await control(origin, "act", { key: "empty-options" });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-describe-input") && page.$("#highbay-describe-submit") && page.sent("GET", "/team").some((r) => r.status === 200) && page.sent("GET", "/team/agent-options").some((r) => r.status === 200) && Array.from(page.$$("#highbay-describe-form button")).some((b) => b.textContent.includes("refresh installed agents")), "initial Describe empty-options recovery control did not settle");
setVal(page, page.$("#highbay-describe-input"), "Draft an incident triage workflow");
expect(page.$("#highbay-describe-submit").disabled, "Describe allowed send when no valid coding agent was installed");
page.$("#highbay-describe-submit").click();
expect(page.sent("POST", "/team/messages").length === 0, "blocked Describe sent POST /team/messages");

await control(origin, "act", { key: "restore-options" });
Array.from(page.$$("#highbay-describe-form button")).find((b) => b.textContent.includes("refresh installed agents")).click();
await until(() => !page.$("#highbay-describe-submit").disabled, "valid agent options did not enable Describe send");

// (c) Pre-post 422 refusal preserves draft in #highbay-describe-error; success attaches guide_choice + new_change and transitions to receipt.
await control(origin, "act", { key: "send-fail" });
page.$("#highbay-describe-submit").click();
await until(() => page.sent("POST", "/team/messages").length === 1 && page.$("#highbay-describe-error"), "failed send did not surface #highbay-describe-error");
const firstSent = page.sent("POST", "/team/messages")[0].body;
expect(firstSent.to === "guide" && Boolean(firstSent.guide_choice?.client) && Boolean(firstSent.guide_choice?.program) && firstSent.context?.type === "new_change" && firstSent.context.value?.scope === "separate_card", "Describe omitted guide_choice or new_change context: " + JSON.stringify(firstSent));
expect(page.$("#highbay-describe-input").value === "Draft an incident triage workflow", "failed Describe send cleared the user's draft");

await control(origin, "act", { key: "send-ok" });
page.$("#highbay-describe-submit").click();
await until(() => page.sent("POST", "/team/messages").length === 2 && page.$("#highbay-draft-receipt"), "successful Describe send did not transition to receipt");

// Seed a running Guide + confirmed Story Change Card to exercise Team controls and Story Ask Guide.
await control(origin, "act", { key: "guide-running-with-story-card" });
await until(() => page.$("#highbay-open-change-chg-protect") || page.$("#highbay-checkpoint-view")?.dataset.changeId === "chg-protect", "confirmed card did not arrive");
if (page.$("#highbay-checkpoint-view")?.dataset.changeId !== "chg-protect") {
  page.$("#highbay-open-change-chg-protect").click();
  await until(() => page.$("#highbay-checkpoint-view")?.dataset.changeId === "chg-protect", "confirmed card did not open");
}

// (a) Interrupt refusal -> #highbay-team-action-error, then successful Interrupt and Stop.
page.$("#highbay-nav-team").click();
await until(() => page.$("#highbay-team-interrupt-builder") && page.$("#highbay-team-stop-builder"), "Team interrupt/stop buttons did not render");
await control(origin, "act", { key: "interrupt-fail" });
page.$("#highbay-team-interrupt-builder").click();
await until(() => page.$("#highbay-team-action-error"), "failed interrupt did not render #highbay-team-action-error");
expect(page.text("#highbay-team-action-error").includes("builder terminal already stopped"), "unexpected interrupt error text: " + page.text("#highbay-team-action-error"));

await control(origin, "act", { key: "interrupt-ok" });
page.$("#highbay-team-interrupt-builder").click();
await until(() => page.sent("POST", "/team/roles/builder/interrupt").length === 2 && page.status().includes("Sent interrupt signal to builder"), "successful interrupt did not announce status");
page.$("#highbay-team-stop-builder").click();
await until(() => page.sent("POST", "/team/roles/builder/stop").length === 1 && page.status().includes("Stopped builder terminal"), "stop_team_role did not run and announce status");

// (b) Restart sends choice ONLY when edited by the user (for both guide and worker roles).
await until(() => page.$("#highbay-team-restart-guide") && !page.$("#highbay-team-restart-guide").disabled && page.$("#highbay-team-restart-builder") && !page.$("#highbay-team-restart-builder").disabled, "restart buttons missing");
page.$("#highbay-team-restart-guide").click();
await until(() => page.sent("POST", "/team/roles/guide/restart").length === 1, "guide restart request was not sent");
const uneditedRestart = page.sent("POST", "/team/roles/guide/restart")[0].body;
expect(!uneditedRestart || !("choice" in uneditedRestart), "unedited guide restart sent an explicit choice override: " + JSON.stringify(uneditedRestart));

page.$("#highbay-team-restart-builder").click();
await until(() => page.sent("POST", "/team/roles/builder/restart").length === 1, "unedited builder restart request was not sent");
const uneditedBuilderRestart = page.sent("POST", "/team/roles/builder/restart")[0].body;
expect(!uneditedBuilderRestart || !("choice" in uneditedBuilderRestart), "unedited builder restart sent an explicit choice override: " + JSON.stringify(uneditedBuilderRestart));

page.$("#highbay-nav-build").click();
await until(() => page.$("#highbay-build-agents-builder-model"), "Build worker picker did not load");
setVal(page, page.$("#highbay-build-agents-builder-model"), "custom-builder-model");
page.$("#highbay-nav-team").click();
await until(() => page.$("#highbay-team-restart-builder") && !page.$("#highbay-team-restart-builder").disabled, "builder restart button missing");
page.$("#highbay-team-restart-builder").click();
await until(() => page.sent("POST", "/team/roles/builder/restart").length === 2, "edited builder restart request was not sent");
const editedRestart = page.sent("POST", "/team/roles/builder/restart")[1].body;
expect(editedRestart?.choice?.model === "custom-builder-model", "edited worker choice was omitted from restart_team_role: " + JSON.stringify(editedRestart));

// (i) Story Ask Guide focuses #highbay-composer-input and sends story_flow context.
page.$("#highbay-nav-story").click();
await until(() => page.$('#highbay-story-change .highbay-story-flow[data-flow-key="instant-refund"] button'), "Story flow Ask Guide button did not render");
page.$('#highbay-story-change .highbay-story-flow[data-flow-key="instant-refund"] button').click();
await until(() => page.$("#highbay-composer-input") && page.doc.activeElement === page.$("#highbay-composer-input"), "Story Ask Guide did not focus #highbay-composer-input");
setVal(page, page.$("#highbay-composer-input"), "Should instant refunds emit a webhook?");
await control(origin, "act", { key: "send-fail" });
page.$("#highbay-composer-send").click();
await until(() => page.sent("POST", "/team/messages").length === 3 && page.$("#highbay-composer-error"), "failed rail send did not surface #highbay-composer-error");
expect(page.$("#highbay-composer-input").value === "Should instant refunds emit a webhook?", "failed rail send cleared composer text");

await control(origin, "act", { key: "send-ok" });
page.$("#highbay-composer-send").click();
await until(() => page.sent("POST", "/team/messages").length === 4 && page.$("#highbay-composer-input").value === "", "successful rail send did not clear composer text");
const storyPayload = page.sent("POST", "/team/messages")[3].body;
expect(storyPayload.to === "guide" && storyPayload.context?.type === "story_flow" && storyPayload.context.value?.change_id === "chg-protect" && storyPayload.context.value?.flow_key === "instant-refund", "Story Ask Guide did not attach story_flow context: " + JSON.stringify(storyPayload));
page.close();
`)
}

// pageDualTakingHistory serves explicit build and approval snapshots for two
// changes so concurrent merge blocking across changes can be observed deterministically.
type pageDualTakingHistory struct {
	*pageVerifications
	builds    map[string]*genhighbay.ChangeBuild
	approvals map[string]*genhighbay.ApprovalItem
}

// GetBuild returns the configured build snapshot when present, falling back to ControlPlane.
func (s *pageDualTakingHistory) GetBuild(
	ctx context.Context, p *genhighbay.GetBuildPayload,
) (*genhighbay.ChangeBuild, error) {
	if b, ok := s.builds[p.ChangeID]; ok {
		return b, nil
	}
	return s.ControlPlane.GetBuild(ctx, p)
}

// GetChange attaches the configured approval snapshot when present on a fixture change.
func (s *pageDualTakingHistory) GetChange(
	ctx context.Context, p *genhighbay.GetChangePayload,
) (*genhighbay.ChangeCardResult, error) {
	card, err := s.ControlPlane.GetChange(ctx, p)
	if err != nil {
		return nil, err
	}
	if a, ok := s.approvals[p.ChangeID]; ok {
		copied := *card
		copied.Approval = a
		return &copied, nil
	}
	return card, nil
}

// ReviewVerification reports the configured recorded approval for fixture changes.
func (s *pageDualTakingHistory) ReviewVerification(
	ctx context.Context, p *genhighbay.GetVerificationPayload,
) (*genhighbay.VerificationReviewResult, error) {
	res, err := s.pageVerifications.ReviewVerification(ctx, p)
	if err != nil {
		return nil, err
	}
	if a, ok := s.approvals[res.ChangeID]; ok {
		res.Review.Approval = genhighbay.NewApprovalRecorded(a)
	}
	return res, nil
}

// TestViewsPageLiveReadErrorsBuildUnknownAndConcurrentMergeBlock exercises:
// (g) Live target and model connection read failures and explicit retries;
// (j1) uncertain POST /changes/{id}/build outcome ("Could not confirm whether
// the build started.") distinct from HTTP refusal; and
// (j2) concurrent merge_change in flight on chg-merge-a blocking chg-merge-b.
func TestViewsPageLiveReadErrorsBuildUnknownAndConcurrentMergeBlock(t *testing.T) {
	reportDir := t.TempDir()
	tests := strings.Repeat("2", 40)
	cp := newBuildControlPlane(t, Options{
		BuildCacheDir:  t.TempDir(),
		RunsDir:        t.TempDir(),
		HighbayVersion: testHighbayVersion,
		Team:           &TeamBinding{Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")}},
		Verification: &VerificationConfig{
			RunsDir: t.TempDir(),
			Plan:    verify.Plan{SuiteCommit: tests, ReportDir: reportDir},
		},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	user := asCaller(pageOwner, CallerUser)

	setupMergeable := func(
		id, runChar, digChar string,
	) (*genhighbay.ChangeCardResult, *genhighbay.VerificationStatusResult, *genhighbay.ChangeBuild, string) {
		card := saveBuildStoryCard(t, cp, id, StatusConfirmed, refundStory("Merge flow "+id))
		started, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
			ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		})
		require.NoError(t, err)
		candidate := commitBuilderCandidateFixture(t, cp, card)
		report := passingReport(t, card.RequestRevision, candidate, reportSpec{
			SuiteID:     "orders_acceptance",
			SuiteCommit: tests,
			Environment: "sim",
			Artifacts:   map[string]string{"orders": strings.Repeat(digChar, 64)},
		})
		writeReportFile(t, reportDir, report)
		_, err = cp.ApproveMerge(user, &genhighbay.ApproveMergePayload{
			ChangeID:        card.ChangeID,
			RequestRevision: card.RequestRevision,
			CandidateCommit: candidate,
			ReportDigest:    report.Digest,
		})
		require.NoError(t, err)
		at, err := time.Parse(time.RFC3339Nano, started.Build.StartedAt)
		require.NoError(t, err)
		run := passedRun(card, strings.Repeat(runChar, 32), candidate, report.Digest)
		run.AcceptedAt = at.Add(time.Second).Format(time.RFC3339Nano)
		run.InputsCurrent = true
		taking := *started.Build
		taking.InForce = true
		taking.Taking = genhighbay.NewBuildTakingPassed(&genhighbay.BuildPassed{
			RunID:           run.RunID,
			CandidateCommit: candidate,
			CandidateBranch: "highbay/build-" + id,
			Tests:           genhighbay.NewBuildPassedTestSourceEvaluator(&genhighbay.OperatorEvaluatorTests{}),
			BaseInBranch:    true,
		})
		return card, run, &genhighbay.ChangeBuild{
			ChangeID: card.ChangeID,
			Build:    genhighbay.NewBuildStarted(&taking),
		}, started.Build.StartedAt
	}

	cardA, runA, buildA, _ := setupMergeable("chg-merge-a", "a", "7")
	loadedA, err := cp.GetChange(user, &genhighbay.GetChangePayload{ChangeID: cardA.ChangeID})
	require.NoError(t, err)
	require.NotNil(t, loadedA.Approval)

	cardB := saveBuildStoryCard(t, cp, "chg-merge-b", StatusConfirmed, refundStory("Merge flow chg-merge-b"))
	reportB := passingReport(t, cardB.RequestRevision, runA.CandidateCommit, reportSpec{
		SuiteID:     "orders_acceptance",
		SuiteCommit: tests,
		Environment: "sim",
		Artifacts:   map[string]string{"orders": strings.Repeat("8", 64)},
	})
	writeReportFile(t, reportDir, reportB)
	runB := passedRun(cardB, strings.Repeat("b", 32), runA.CandidateCommit, reportB.Digest)
	runB.AcceptedAt = runA.AcceptedAt
	runB.InputsCurrent = true
	approvalB := *loadedA.Approval
	approvalB.ReportDigest = reportB.Digest
	approvalB.Artifacts = map[string]string{"orders": strings.Repeat("8", 64)}
	startedA, _ := buildA.Build.AsStarted()
	takingB := *startedA
	takingB.RequestRevision = cardB.RequestRevision
	takingB.Taking = genhighbay.NewBuildTakingPassed(&genhighbay.BuildPassed{
		RunID:           runB.RunID,
		CandidateCommit: runB.CandidateCommit,
		CandidateBranch: "highbay/build-chg-merge-b",
		Tests:           genhighbay.NewBuildPassedTestSourceEvaluator(&genhighbay.OperatorEvaluatorTests{}),
		BaseInBranch:    true,
	})
	buildB := &genhighbay.ChangeBuild{
		ChangeID: cardB.ChangeID,
		Build:    genhighbay.NewBuildStarted(&takingB),
	}
	buildableChange(t, cp, "chg-build-unknown")

	verifs := newPageVerifications(cp)
	verifs.set(runA, runB)
	verifs.list("chg-merge-a", runA.RunID)
	verifs.list("chg-merge-b", runB.RunID)
	svc := &pageDualTakingHistory{
		pageVerifications: verifs,
		builds: map[string]*genhighbay.ChangeBuild{
			"chg-merge-a": buildA,
			"chg-merge-b": buildB,
		},
		approvals: map[string]*genhighbay.ApprovalItem{
			"chg-merge-b": &approvalB,
		},
	}
	ps := startPageServer(t, svc)

	runPageScript(t, ps, map[string]any{"runA": runA.RunID, "runB": runB.RunID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-change-list li"), "change list did not load");

// (g) Live target and model connection read failures and explicit retries.
await control(origin, "reply", { key: "GET /live-target", status: 503, body: { name: "unavailable", id: "live-503", message: "Live target read failed.", temporary: true, timeout: false, fault: false } });
await control(origin, "reply", { key: "GET /cp1/model", status: 503, body: { name: "unavailable", id: "model-503", message: "Model connection read failed.", temporary: true, timeout: false, fault: false } });
page.$("#highbay-nav-live").click();
await until(() => page.$("#highbay-live-error") && page.$("#highbay-model-error"), "Live target or model read error did not render");
expect(page.text("#highbay-live-error").includes("Live target read failed."), "unexpected live error: " + page.text("#highbay-live-error"));
expect(page.text("#highbay-model-error").includes("Model connection read failed."), "unexpected model error: " + page.text("#highbay-model-error"));
page.$("#highbay-live-retry").click();
page.$("#highbay-model-retry").click();
await until(() => !page.$("#highbay-live-error") && !page.$("#highbay-model-error") && page.$("#highbay-live-none") && page.$("#highbay-model-current"), "Live target and model retries did not recover");

// (j1) Uncertain build-start outcome ("Could not confirm whether the build started.").
page.$("#highbay-nav-changes").click();
await until(() => page.$("#highbay-open-change-chg-build-unknown"), "chg-build-unknown missing");
page.$("#highbay-open-change-chg-build-unknown").click();
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "chg-build-unknown Build button not ready");
const realFetch = page.win.fetch;
let dropNextBuildPost = true;
page.win.fetch = (inputUrl, init) => {
  if (dropNextBuildPost && String(init?.method || "").toUpperCase() === "POST" && String(inputUrl).endsWith("/changes/chg-build-unknown/build")) {
    dropNextBuildPost = false;
    return Promise.reject(new page.win.TypeError("Failed to fetch"));
  }
  return realFetch(inputUrl, init);
};
page.$("#highbay-build-start").click();
await until(() => page.status().includes("Could not confirm whether the build started."), "network-dropped build answer did not report unconfirmed build start: " + page.status());

// (j2) Concurrent merge on chg-merge-a blocks chg-merge-b with explicit reason.
page.$("#highbay-nav-changes").click();
await until(() => page.$("#highbay-open-change-chg-merge-a"), "chg-merge-a missing");
page.$("#highbay-open-change-chg-merge-a").click();
await until(() => page.$("#highbay-checkpoint-view")?.dataset.changeId === "chg-merge-a" && (page.$("#highbay-cp5-approval") || page.$("#highbay-review-approval")), "chg-merge-a approval did not appear");
if (!page.$("#highbay-cp5-approval")) page.$("#highbay-review-approval").click();
await until(() => page.$("#highbay-cp5-merge") && !page.$("#highbay-cp5-merge").disabled, "chg-merge-a merge button did not enable");

await control(origin, "reply", { key: "POST /changes/chg-merge-a/merge", status: 200, body: { branch: "main", head_commit: "9".repeat(40) } });
await control(origin, "hold", { key: "POST /changes/chg-merge-a/merge" });
page.$("#highbay-cp5-merge").click();
await until(() => page.$("#highbay-cp5-merge-feedback-pending"), "chg-merge-a merge did not enter pending state");

page.$("#highbay-nav-changes").click();
await until(() => page.$("#highbay-open-change-chg-merge-b"), "chg-merge-b missing");
page.$("#highbay-open-change-chg-merge-b").click();
await until(() => page.$("#highbay-checkpoint-view")?.dataset.changeId === "chg-merge-b" && (page.$("#highbay-cp5-approval") || page.$("#highbay-review-approval")), "chg-merge-b approval did not appear");
if (!page.$("#highbay-cp5-approval")) page.$("#highbay-review-approval").click();
await until(() => page.$("#highbay-approval-stage") && page.text("#highbay-approval-stage").includes("Another change is being merged. Wait for that operation to finish."), "concurrent merge block was not shown on chg-merge-b: " + page.text("#highbay-approval-stage"));
expect(!page.$("#highbay-cp5-merge") || page.$("#highbay-cp5-merge").disabled, "chg-merge-b offered an active merge while chg-merge-a merge was pending");

await control(origin, "release", { key: "POST /changes/chg-merge-a/merge" });
await until(() => !page.text("#highbay-approval-stage").includes("Another change is being merged"), "releasing chg-merge-a merge did not clear the concurrent merge block on chg-merge-b");
page.close();
`)
}
