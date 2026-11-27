// This file drives the Team controls through the served page and generated HTTP API.
package controlplane

import (
	"context"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/require"
)

// pageTeamSnapshot supplies independent terminal and assignment facts.
// Each replacement is immutable after publication to the fixture's readers.
func pageTeamSnapshot(state string) genhighbay.TeamSnapshot {
	return genhighbay.TeamSnapshot{
		Team: "page-team", Channel: "#page-team", ChorusStatus: "connected",
		SavedChoices: map[string]*genhighbay.RoleAgentChoice{}, DismissedNotices: []string{},
		Roles: []*genhighbay.TeamRoleState{
			{Role: "guide", Handle: "page-guide", State: "not_started", Attention: "none", Assignment: "no_work"},
			{Role: "builder", Handle: "page-builder", HasTerminal: true, State: state, Attention: "question", Assignment: "unread"},
			{Role: "test_writer", Handle: "page-writer", State: "unknown", Attention: "unknown", Assignment: "nothing_owed"},
		},
	}
}

// TestViewsPageDescribeGuideStatusControlsNext follows the same Team reads
// as Describe and checks that their guidance yields to an existing card.
func TestViewsPageDescribeGuideStatusControlsNext(t *testing.T) {
	f := &teamServiceFixture{snapshot: genhighbay.TeamSnapshot{
		Team: "page-team", Channel: "#page-team", ChorusStatus: "connected",
		SavedChoices: map[string]*genhighbay.RoleAgentChoice{}, DismissedNotices: []string{},
	}}
	cp := pageControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	ps := startPageServer(t, cp)
	for _, state := range []string{"unknown", "starting", "working", "waiting", "ended", "not_started"} {
		ps.act(state, func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			next := pageTeamSnapshot("working")
			next.Roles[0].State = state
			next.Roles[1].Assignment = "no_work"
			next.Roles[2].Assignment = "no_work"
			next.Roles[2].State = "waiting"
			f.snapshot = next
		})
	}
	initMsg := "Resolving the service's Go modules"
	owner := pageOwner
	ps.act("init-running", func() { cp.initPhase.Store(&initMsg) })
	ps.act("init-done", func() {
		cp.initOwner.Store(&owner)
		cp.initPhase.Store(nil)
	})
	setStoryState := func(id, state string) {
		wsDir, wsDev, wsIno := cp.currentWorkspaceTarget()
		cp.storyRefreshMu.Lock()
		defer cp.storyRefreshMu.Unlock()
		if cp.storyRefreshes == nil {
			cp.storyRefreshes = make(map[storyRefreshKey]*storyRefreshOp)
		}
		cp.storyRefreshes[storyRefreshKey{
			owner:        pageOwner,
			workspaceDir: wsDir,
			workspaceDev: wsDev,
			workspaceIno: wsIno,
		}] = &storyRefreshOp{id: id, state: state, snapshotDigest: "digest-" + id}
	}
	ps.act("story-evaluating", func() { setStoryState("op-eval", storyRefreshStateRunning) })
	ps.act("story-ready", func() { setStoryState("op-ready", storyRefreshStateSucceeded) })
	ps.act("story-failed", func() { setStoryState("op-failed", storyRefreshStateFailed) })
	ps.act("draft-reset", func() {
		cp.initOwner.Store(nil)
		cp.draftOwner.Store(nil)
		cp.storyRefreshMu.Lock()
		clear(cp.storyRefreshes)
		cp.storyRefreshMu.Unlock()
	})
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
await control(origin, "hold", { key: "GET /team" });
const page = await openPage(origin, { link: input.link });
const next = () => page.text("#highbay-frame-next dd");
const attention = () => page.text(".highbay-frame-attention dd");
// Without a guide, a message starts one. Describe says so in its welcome
// panel, which replaces the status frame while nothing needs the user; Team
// says so as the next step.
const welcome = (text) => !page.$(".highbay-frame-slots") &&
  page.text("#highbay-change-list-title") === "Describe what you want to build" &&
  page.text("#highbay-change-list-empty").includes(text);
const teamStartGuide = "Send a message in Team conversation to start Guide.";
await until(() => page.$("#highbay-change-list-empty") && page.sent("GET", "/team").length === 1, "empty Describe did not begin reading Team");
expect(next() === "Wait for Team status.", "pending Team read was treated as an absent guide");
expect(attention() === "Waiting for Team status.", "unread Team status appeared settled on Describe");
page.$("#highbay-nav-team").click();
expect(attention() === "Waiting for Team status.", "unread Team status appeared settled on Team");
page.$("#highbay-nav-changes").click();
await control(origin, "release", { key: "GET /team" });
await until(() => welcome("launch Guide"), "Describe did not explain how to start a guide: " + page.text("#highbay-change-list-empty"));
page.$("#highbay-nav-team").click();
expect(next() === teamStartGuide && !page.$("#highbay-team-start"), "Team offered worker startup without a build assignment: " + next());
page.$("#highbay-nav-changes").click();

await control(origin, "reply", { key: "GET /team", status: 503, body: { name: "unavailable", message: "Team unavailable." } });
await control(origin, "act", { key: "unknown" });
await until(() => next().includes("Open Team to retry"), "a failed Team read still used its previous empty observation");
expect(attention() === "Team status could not be read.", "failed Team status appeared settled on Describe");
page.$("#highbay-nav-team").click();
await until(() => page.$("#highbay-team-role-guide")?.dataset.state === "unknown", "Team did not retry its failed read");
page.$("#highbay-nav-changes").click();
// A guide outside a terminal whose state is unknown is still a guide to write to.
await until(() => welcome("Send your request to Guide below"), "a guide of unknown state appeared absent: " + page.text("#highbay-change-list-empty"));
await control(origin, "act", { key: "starting" });
await until(() => next().includes("Wait for Guide to start"), "a starting guide did not tell the user to wait: " + next());
await control(origin, "act", { key: "working" });
await until(() => !page.$(".highbay-frame-slots") && page.text("#highbay-change-list-title") === "Working on your request",
       "a working coding agent did not replace the connection steps: " + page.text("#highbay-change-list-title"));
expect(!page.$(".highbay-welcome-steps"), "a working coding agent showed the idle welcome prompt");
await control(origin, "act", { key: "init-running" });
await until(() => page.text("#highbay-change-list-title") === "Preparing your project", "init_project did not show Preparing your project");
expect(page.doc.title.startsWith("Preparing project:"), "init_project left the Describe title: " + page.doc.title);
await control(origin, "act", { key: "init-done" });
await until(() => page.text("#highbay-change-list-title") === "Drafting your Change Card", "finishing init_project snapped back instead of showing Drafting your Change Card");
expect(page.text("#highbay-change-list-empty").includes("Starter project ready") &&
       page.doc.title.startsWith("Drafting Change Card:"),
       "post-init working state did not explain draft progress: " + page.text("#highbay-change-list-empty"));
await control(origin, "act", { key: "story-evaluating" });
await until(() => page.text("#highbay-change-list-empty").includes("Checking the Story and Goa design"), "story evaluation progress was not shown");
await control(origin, "act", { key: "story-ready" });
await until(() => page.text("#highbay-change-list-empty").includes("Story and design checked"), "succeeded story evaluation was not shown");
// A coding agent that is working is expected to see the failed check itself;
// the page asks for the user only once the agent has stopped.
await control(origin, "act", { key: "waiting" });
await control(origin, "act", { key: "story-failed" });
await until(() => page.text("#highbay-change-list-title") === "Story check needs attention" &&
       attention() === "Story check needs attention.",
       "failed story evaluation did not surface attention once the coding agent waited: " +
       page.text("#highbay-change-list-title") + " / " + attention());
await control(origin, "act", { key: "draft-reset" });
await control(origin, "act", { key: "ended" });
await until(() => welcome("launch Guide"), "Describe did not explain how to start a guide again: " + page.text("#highbay-change-list-empty"));
page.$("#highbay-nav-team").click();
for (const state of ["ended", "not_started"]) {
  if (state === "not_started") await control(origin, "act", { key: state });
  await until(() => page.$("#highbay-team-role-guide")?.dataset.state === state && !page.$("#highbay-team-loading"), "Team did not read " + state);
  expect(next() === teamStartGuide && !page.$("#highbay-team-start"), "Team offered worker startup for guide state " + state + " without a build assignment: " + next());
}
page.$("#highbay-nav-changes").click();

await control(origin, "reply", { key: "GET /changes/chg-guide-next", status: 503, body: { name: "unavailable", message: "Card unavailable." } });
await propose("chg-guide-next", "Route refunds to a manager.", "Auto-approved");
await until(() => page.$("#highbay-change-open-error"), "the failed card read did not appear");
expect(next().includes("Try opening the change again"), "card recovery did not take precedence over ordinary guidance");
page.$("#highbay-open-change-chg-guide-next").click();
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-guide-next", "the existing card remained unavailable without a running guide");
expect(page.sent("POST", "/team/agents").length === 0, "guidance performed an agent action");
page.close();
`)
}

// TestViewsPageAttentionUsesOnlyTheCurrentLiveRegion follows permission,
// question and recovery facts on empty Describe without overwriting unrelated
// operation feedback. A terminal that finished its turn is exposed by the owner as waiting.
func TestViewsPageAttentionUsesOnlyTheCurrentLiveRegion(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := pageControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	ps := startPageServer(t, cp)
	for _, transition := range []struct {
		name, guideState, guideAttention, builderState, builderAttention string
	}{
		{"guide-permission", "needs_you", "permission", "working", "none"},
		{"two-roles", "needs_you", "question", "needs_you", "auth"},
		{"guide-done", "waiting", "none", "needs_you", "auth"},
		{"all-done", "waiting", "none", "waiting", "none"},
	} {
		ps.act(transition.name, func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			next := pageTeamSnapshot(transition.builderState)
			next.Roles[0].State, next.Roles[0].Attention = transition.guideState, transition.guideAttention
			next.Roles[1].Attention = transition.builderAttention
			f.snapshot = next
		})
	}
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.status() === "This project has no changes yet." && page.$("#highbay-team-attention"), "empty Describe did not settle");
const live = page.$("#highbay-team-attention"), operation = page.status();
expect(live.getAttribute("role") === "status" && live.getAttribute("aria-live") === "polite", "attention owner is not an announced live region");
expect(live.textContent === "", "idle roles invented an attention state");
await control(origin, "act", { key: "guide-permission" });
await until(() => page.text("#highbay-team-attention-guide").includes("Guide needs you: permission"), "real permission attention was suppressed");
expect(page.text(".highbay-frame-attention dd").includes("Guide needs you: permission"), "Describe did not surface the coding agent’s reported blocker");
expect(page.$("#highbay-team-attention") === live, "attention arrived in a newly mounted live region");
expect(page.status() === operation, "team attention overwrote operation feedback");
await control(origin, "act", { key: "two-roles" });
await until(() => page.text("#highbay-team-attention-guide").includes("waiting on your answer") && page.text("#highbay-team-attention-builder").includes("sign-in required"), "changed and additional real blockers were not shown");
expect(!live.textContent.includes("permission"), "replaced permission attention stayed stale");
await control(origin, "act", { key: "guide-done" });
await until(() => !page.$("#highbay-team-attention-guide"), "done guide retained an attention line");
expect(page.text("#highbay-team-attention-builder").includes("Builder needs you: sign-in required"), "guide recovery hid another role's real blocker");
expect(page.text(".highbay-frame-attention dd").includes("Builder needs you: sign-in required"), "Describe lost the builder’s current request");
await control(origin, "act", { key: "all-done" });
await until(() => live.textContent === "", "recovered roles retained stale attention");
expect(page.status() === operation && !page.status().includes("needs you"), "recovery left a duplicate stale status or replaced operation feedback");
expect(page.$("#highbay-team-attention") === live && live.getAttribute("aria-live") === "polite", "recovery removed the next attention announcement owner");
expect(page.sent("POST", "/team/agents").length === 0, "reading attention performed a team action");
page.close();
`)
}

// TestViewsPageTeamFollowsAttention proves that global attention follows the
// one version chain on every view and that Team shows each role's own state
// and assignment facts.
func TestViewsPageTeamFollowsAttention(t *testing.T) {
	snapshot := pageTeamSnapshot("needs_you")
	snapshot.Roles[2].Assignment = "no_work"
	f := &teamServiceFixture{snapshot: snapshot}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	buildableChange(t, cp, "chg-team-attention")
	ps := startPageServer(t, cp)
	ps.act("builder-working", func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.snapshot = pageTeamSnapshot("working")
	})
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-team-attention-builder") && page.$("#highbay-build-section")?.dataset.state === "not_started" && !page.$("#highbay-build-loading"), "the eligible build or builder attention did not arrive");
expect(page.text("#highbay-team-attention-builder").includes("Builder needs you: waiting on your answer"), "attention reason is missing");
const attention = () => page.text(".highbay-frame-attention dd");
expect(attention().includes("Builder needs you: waiting on your answer"), "unstarted Build hid the builder’s request");
for (const stage of ["changes", "cp1", "cp2", "story", "live"]) {
  page.$("#highbay-nav-" + stage).click();
  expect(attention().includes("Builder needs you: waiting on your answer"), stage + " hid the builder’s request");
}
expect(page.sent("POST", "/team/agents").length === 0, "reading attention started agents");
page.$("#highbay-nav-team").click();
await until(() => page.state() === "team" && page.$("#highbay-team-role-builder"), "Team did not open");
expect(attention().includes("Builder needs you: waiting on your answer"), "Team hid the builder’s request");
expect(page.text("#highbay-team-role-builder").includes("Assignment unread"), "the builder's assignment fact was inferred from terminal state");
expect(page.text("#highbay-team-role-test_writer").includes("Status unknown"), "the test writer's unknown terminal state is missing");
expect(page.text("#highbay-team-role-test_writer").includes("No work now"), "the declared no_work assignment fact is missing");
expect(page.doc.activeElement === page.$("#highbay-team-title"), "opening Team did not focus its title");
await control(origin, "act", { key: "builder-working" });
await until(() => !page.$("#highbay-team-attention-builder"), "attention did not clear through updates");
await until(() => page.$("#highbay-team-role-builder").dataset.state === "working", "Team did not follow the same update");
expect(attention() === "Team status unknown.", "another role’s unknown status appeared settled");
const teamReads = page.sent("GET", "/team").length;
expect(teamReads <= page.sent("GET", "/changes").length + 2, "Team introduced an independent polling loop");
page.close();
`)
}

// TestViewsPageTeamStartsExplicitlyAndRetainsFailedFacts checks an explicit
// start receipt and a failed read without replacing known facts with emptiness.
func TestViewsPageTeamStartsExplicitlyAndRetainsFailedFacts(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("needs_you")}
	f.start = func(context.Context) (*genhighbay.ProjectTeam, error) {
		f.mu.Lock()
		f.snapshot = pageTeamSnapshot("working")
		f.mu.Unlock()
		return &genhighbay.ProjectTeam{Team: "page-team", Step: "The project agents have started.", Roles: []*genhighbay.ProjectTeamRole{}}, nil
	}
	cp := pageControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-team-attention-builder"), "initial team was not read");
page.$("#highbay-nav-team").click();
await until(() => page.text("#highbay-team-start") === "Start workers", "Start workers is missing");
expect(page.sent("POST", "/team/agents").length === 0, "opening Team started workers");
await control(origin, "reply", { key: "GET /team", status: 503, body: { name: "unavailable", id: "team-read", message: "Team owner unavailable.", temporary: true, timeout: false, fault: false } });
page.$("#highbay-team-refresh").click();
await until(() => page.$("#highbay-team-read-error"), "read failure was not shown");
expect(page.text("#highbay-team-read-error") === "Could not read the project team. Team owner unavailable.", "Team did not name the failed read before the raw diagnostic: " + page.text("#highbay-team-read-error"));
expect(page.$("#highbay-team-read-error").getAttribute("role") === "alert", "the failed Team read is not announced as an alert");
expect(page.text("#highbay-team-stale").includes("Last-read"), "retained facts are presented as current");
expect(page.text("#highbay-team-attention-builder").includes("Last read:"), "global attention is presented as current after read failure");
expect(page.text(".highbay-frame-attention dd") === "Team status could not be read.", "Team frame presented retained attention as current");
expect(page.$("#highbay-team-role-builder").dataset.state === "needs_you", "failed read discarded known facts");
page.$("#highbay-team-refresh").click();
await until(() => !page.$("#highbay-team-read-error"), "retry did not recover");
expect(!page.text("#highbay-team-attention-builder").includes("Last read:"), "recovered facts remain marked stale");
await control(origin, "hold", { key: "POST /team/agents" });
page.$("#highbay-team-start").click();
await until(() => page.sent("POST", "/team/agents").length === 1, "the explicit start request did not begin");
expect(page.$("#highbay-team-start").disabled && page.text("#highbay-frame-next dd") === "Wait for workers to start.", "pending startup was presented as another action");
await control(origin, "release", { key: "POST /team/agents" });
await until(() => page.$("#highbay-team-start-receipt"), "explicit start receipt is missing");
expect(page.text("#highbay-team-start-receipt").includes("This start (begun ") && page.text("#highbay-team-start-receipt").includes("The project agents have started."), "receipt is not dated owner text");
await until(() => page.$("#highbay-team-role-builder").dataset.state === "working" && !page.$("#highbay-team-attention-builder"), "current facts were replaced by the historical start receipt");
expect(page.sent("POST", "/team/agents").length === 1, "one click did not send one start");
page.close();
`)
}

// TestViewsPageTeamAndBuildUpdatesPreserveTheModelDraft keeps all four model
// inputs through the new update reasons and navigation without applying them.
// A rejected session then leaves the team facts labeled and its actions inert.
func TestViewsPageTeamAndBuildUpdatesPreserveTheModelDraft(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	card := buildableChange(t, cp, "chg")
	ps := startPageServer(t, cp)
	ps.act("start-build-and-attention", func() {
		_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
			ChangeID: "chg", RequestRevision: card.RequestRevision,
		})
		require.NoError(t, err)
		f.mu.Lock()
		f.snapshot = pageTeamSnapshot("needs_you")
		f.mu.Unlock()
	})
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-section")?.dataset.state === "not_started" && !page.$("#highbay-build-loading"), "the initial selected build was not read");
page.$("#highbay-nav-live").click();
await until(() => page.$("#highbay-model-name"), "the model form did not open");
const provider = page.$("#highbay-model-provider");
provider.value = "openai";
provider.dispatchEvent(new page.win.Event("change", { bubbles: true }));
const name = page.$("#highbay-model-name");
page.type(name, "unsent-model-choice");
page.$("input[name='highbay-model-kind'][value='gateway']").click();
const gateway = page.$("#highbay-model-gateway");
page.type(gateway, "127.0.0.1:9455");
gateway.focus();
gateway.setSelectionRange(2, 7);
const buildReads = page.sent("GET", "/changes/chg/build").length;
await control(origin, "act", { key: "start-build-and-attention" });
await until(() => page.sent("GET", "/changes/chg/build").slice(buildReads).some((r) => r.status === 200 && JSON.parse(r.answer).build.state === "started") && page.$("#highbay-team-attention-builder"), "background build and team changes did not reach Live");
// Let the completed owner answer render before checking the retained fields.
await sleep(0);
expect(page.$("#highbay-model-name") === name && name.value === "unsent-model-choice", "a new update reason replaced the model input");
expect(page.$("#highbay-model-provider") === provider && provider.value === "openai", "a new update reason lost the provider");
expect(page.$("#highbay-model-gateway") === gateway && gateway.value === "127.0.0.1:9455" && page.doc.activeElement === gateway && gateway.selectionStart === 2 && gateway.selectionEnd === 7, "a new update reason changed gateway text, focus or selection");
expect(page.$("input[name='highbay-model-kind'][value='gateway']").checked, "a new update reason changed the model kind");
page.$("#highbay-nav-team").click();
await until(() => page.$("#highbay-team-title"), "Team did not open");
page.$("#highbay-nav-live").click();
await until(() => page.$("#highbay-model-gateway"), "Live did not reopen");
expect(page.$("input[name='highbay-model-kind'][value='gateway']").checked && page.$("#highbay-model-provider").value === "openai" && page.$("#highbay-model-name").value === "unsent-model-choice" && page.$("#highbay-model-gateway").value === "127.0.0.1:9455", "the Team round-trip lost model draft fields");
expect(page.sent("POST", "/cp1/model").length === 0, "an update or navigation applied the unsent model draft");
page.$("#highbay-nav-team").click();
await until(() => page.$("#highbay-team-refresh") && !page.$("#highbay-team-refresh").disabled, "Team did not become ready");
await control(origin, "reply", { key: "GET /team", status: 401, body: { name: "unauthorized", message: "Session ended." } });
page.$("#highbay-team-refresh").click();
await until(() => page.$("#highbay-checkpoint-root").dataset.session === "ended", "the refused session remained active");
expect(page.text("#highbay-team-attention-builder").includes("Last read:"), "ended session shows retained attention as current");
expect(page.text("#highbay-team-read-error") === "The session ended; these are last-read facts.", "ended session is presented as a failed team read: " + page.text("#highbay-team-read-error"));
for (const button of page.$$("#highbay-team-stage button, #highbay-team-attention button")) {
  expect(button.disabled, "ended session left an active Team control: " + button.id);
}
expect(page.$("#highbay-team-role-builder").dataset.state === "needs_you", "ended session discarded the observed team");
expect(page.sent("POST", "/cp1/model").length === 0, "session end applied the model draft");
page.close();
`)
}
