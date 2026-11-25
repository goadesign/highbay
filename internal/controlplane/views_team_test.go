// This file drives the Team controls through the served page and generated HTTP API.
package controlplane

import (
	"context"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/require"
)

// pageTeamSnapshot supplies independent terminal, watcher and assignment facts.
// Each replacement is immutable after publication to the fixture's readers.
func pageTeamSnapshot(state string) genhighbay.TeamSnapshot {
	return genhighbay.TeamSnapshot{HerdrMode: "attached", SavedChoices: map[string]*genhighbay.RoleAgentChoice{}, DismissedNotices: []string{}, Team: "page-team", Roles: []*genhighbay.TeamRoleState{
		{Role: "guide", Handle: "page-guide", State: "not_started", Attention: "none", Watcher: "unknown", Assignment: "unknown"},
		{Role: "builder", Handle: "page-builder", State: state, Attention: "question", Watcher: "gone", Assignment: "unread"},
		{Role: "test_writer", Handle: "page-writer", State: "unknown", Attention: "unknown", Watcher: "held", Assignment: "nothing_owed"},
	}}
}

// TestViewsPageDescribeGuideStatusControlsNext follows the same Team reads
// as Describe and checks that their guidance yields to an existing card.
func TestViewsPageDescribeGuideStatusControlsNext(t *testing.T) {
	f := &teamServiceFixture{snapshot: genhighbay.TeamSnapshot{HerdrMode: "attached", SavedChoices: map[string]*genhighbay.RoleAgentChoice{}, DismissedNotices: []string{},
		Team: "page-team", Roles: []*genhighbay.TeamRoleState{},
	}}
	cp := pageControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	ps := startPageServer(t, cp)
	for _, state := range []string{"unknown", "starting", "working", "ended", "not_started"} {
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
	for _, mode := range []string{"available", "unavailable"} {
		ps.act("external-"+mode, func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			next := pageTeamSnapshot("working")
			next.HerdrMode = mode
			next.Roles[0].State, next.Roles[0].Watcher = "unknown", "unknown"
			next.Roles[2].State = "waiting"
			f.snapshot = next
		})
	}
	ps.act("worker-unknown", func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		next := pageTeamSnapshot("working")
		next.HerdrMode = "available"
		next.Roles[0].State = "unknown"
		f.snapshot = next
	})
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
const connectGuide = "Connect your coding agent to this project’s Highbay MCP server.";
await until(() => page.$("#highbay-change-list-empty") && page.sent("GET", "/team").length === 1, "empty Describe did not begin reading Team");
expect(next() === "Wait for Team status.", "pending Team read was treated as an absent guide");
expect(attention() === "Waiting for Team status.", "unread Team status appeared settled on Describe");
page.$("#highbay-nav-team").click();
expect(attention() === "Waiting for Team status.", "unread Team status appeared settled on Team");
page.$("#highbay-nav-changes").click();
await control(origin, "release", { key: "GET /team" });
await until(() => next().includes(connectGuide), "Describe did not explain how to connect a guide");
page.$("#highbay-nav-team").click();
expect(next() === connectGuide && !page.$("#highbay-team-start"), "Team offered worker startup without a build assignment");
page.$("#highbay-nav-changes").click();

await control(origin, "reply", { key: "GET /team", status: 503, body: { name: "unavailable", message: "Team unavailable." } });
await control(origin, "act", { key: "unknown" });
await until(() => next().includes("Open Team to retry"), "a failed Team read still used its previous empty observation");
expect(attention() === "Team status could not be read.", "failed Team status appeared settled on Describe");
page.$("#highbay-nav-team").click();
await until(() => page.$("#highbay-team-role-guide")?.dataset.state === "unknown", "Team did not retry its failed read");
page.$("#highbay-nav-changes").click();
expect(next().includes("refresh to check your coding agent’s status"), "unknown attached coding agent appeared ready or absent");
await control(origin, "act", { key: "starting" });
await until(() => next().includes("Wait for your coding agent to start"), "a starting coding agent did not tell the user to wait");
await control(origin, "act", { key: "working" });
await until(() => !page.$(".highbay-frame-slots") && page.$("#highbay-empty-show-guide"), "a working attached coding agent did not lead to its existing pane");
expect(page.text("#highbay-change-list-title") === "Working on your request" &&
       !page.$(".highbay-welcome-steps") &&
       !page.$("#highbay-empty-show-guide").classList.contains("shadcn-btn-primary"),
       "a working coding agent showed the idle welcome prompt or primary CTA: " + page.text("#highbay-change-list-title"));
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
await control(origin, "act", { key: "external-available" });
await until(() => !page.$("#highbay-empty-show-guide") && page.text("#highbay-change-list-title") === "Drafting your Change Card",
       "external coding agent snapped back to Describe after init_project and refresh_story");
await control(origin, "act", { key: "story-failed" });
await until(() => page.text("#highbay-change-list-title") === "Story check needs attention" &&
       attention() === "Story check needs attention.",
       "failed story evaluation did not surface attention on external Describe");
await control(origin, "act", { key: "draft-reset" });
for (const mode of ["available", "unavailable"]) {
  await control(origin, "act", { key: "external-" + mode });
  await until(() => !page.$("#highbay-empty-show-guide") && !page.$(".highbay-frame-slots"), "external coding agent was treated as a Herdr pane");
  expect(page.text("#highbay-change-list-title").includes("Describe what you want to build") &&
         !page.$("#highbay-nav-changes") && !page.$(".highbay-frame-context"), "healthy empty state kept unnecessary frame chrome");
  page.$("#highbay-nav-team").click();
  const codingAgent = page.$("#highbay-team-role-guide");
  expect(codingAgent.textContent.includes("Coding agent registered · No Herdr pane linked") &&
         codingAgent.textContent.includes("open a project tab in Herdr and run /highbay") &&
         !codingAgent.textContent.includes("Chorus watcher unknown") &&
         !page.$("#highbay-team-show-guide"), "Team showed a false warning or pane action for external mode " + mode);
  page.$("#highbay-nav-changes").click();
}
await control(origin, "act", { key: "worker-unknown" });
await until(() => attention() === "Team status unknown." && next().includes("Open Team"), "a genuinely unobserved worker was hidden with the external guide's expected state");
await control(origin, "act", { key: "ended" });
await until(() => next().includes(connectGuide), "Describe did not explain how to reconnect an ended guide");
page.$("#highbay-nav-team").click();
for (const state of ["ended", "not_started"]) {
  if (state === "not_started") await control(origin, "act", { key: state });
  await until(() => page.$("#highbay-team-role-guide")?.dataset.state === state && !page.$("#highbay-team-loading"), "Team did not read " + state);
  expect(next() === connectGuide && !page.$("#highbay-team-start"), "Team offered worker startup for guide state " + state + " without a build assignment");
}
page.$("#highbay-nav-changes").click();

await control(origin, "reply", { key: "GET /changes/chg-guide-next", status: 503, body: { name: "unavailable", message: "Card unavailable." } });
await propose("chg-guide-next", "Route refunds to a manager.", "Auto-approved");
await until(() => page.$("#highbay-change-open-error"), "the failed card read did not appear");
expect(next().includes("Try opening the change again"), "card recovery did not take precedence over ordinary guidance");
page.$("#highbay-open-change-chg-guide-next").click();
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-guide-next", "the existing card remained unavailable without a running guide");
expect(page.sent("POST", "/team/agents").length === 0 && page.sent("POST", "/team/pane").length === 0, "guidance performed an agent action");
page.close();
`)
}

// TestViewsPageAttentionUsesOnlyTheCurrentLiveRegion follows permission,
// question and recovery facts on empty Describe without overwriting unrelated
// operation feedback. Herdr's done status is exposed by the owner as waiting.
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
await until(() => page.text("#highbay-team-attention-guide").includes("Coding agent needs you: permission"), "real permission attention was suppressed");
expect(page.text(".highbay-frame-attention dd").includes("Coding agent needs you: permission") && page.text("#highbay-frame-next dd").includes("Show coding agent pane"), "Describe did not surface the coding agent’s reported blocker and pane");
expect(page.$("#highbay-team-attention") === live, "attention arrived in a newly mounted live region");
expect(page.status() === operation, "team attention overwrote operation feedback");
await control(origin, "act", { key: "two-roles" });
await until(() => page.text("#highbay-team-attention-guide").includes("question") && page.text("#highbay-team-attention-builder").includes("sign-in required"), "changed and additional real blockers were not shown");
expect(!live.textContent.includes("permission"), "replaced permission attention stayed stale");
await control(origin, "act", { key: "guide-done" });
await until(() => !page.$("#highbay-team-attention-guide"), "done guide retained an attention line");
expect(page.text("#highbay-team-attention-builder").includes("Builder needs you: sign-in required"), "guide recovery hid another role's real blocker");
expect(page.text(".highbay-frame-attention dd").includes("Builder needs you: sign-in required"), "Describe lost the builder’s current request");
await control(origin, "act", { key: "all-done" });
await until(() => live.textContent === "", "recovered roles retained stale attention");
expect(page.status() === operation && !page.status().includes("needs you"), "recovery left a duplicate stale status or replaced operation feedback");
expect(page.$("#highbay-team-attention") === live && live.getAttribute("aria-live") === "polite", "recovery removed the next attention announcement owner");
expect(page.sent("POST", "/team/agents").length === 0 && page.sent("POST", "/team/pane").length === 0, "reading attention performed a team action");
page.close();
`)
}

// TestViewsPageTeamFollowsAttentionAndShowsPane proves that global attention
// follows the one version chain, pane actions accept real empty 204 answers,
// and a removed focused attention control returns focus to the current view.
func TestViewsPageTeamFollowsAttentionAndShowsPane(t *testing.T) {
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
expect(page.text("#highbay-team-attention-builder").includes("Builder needs you: question"), "attention reason is missing");
const attention = () => page.text(".highbay-frame-attention dd");
expect(attention().includes("Builder needs you: question"), "unstarted Build hid the builder’s request");
for (const stage of ["changes", "cp1", "cp2", "story", "live"]) {
  page.$("#highbay-nav-" + stage).click();
  expect(attention().includes("Builder needs you: question"), stage + " hid the builder’s request");
}
page.$("#highbay-team-attention-show-pane-builder").click();
await until(() => page.sent("POST", "/team/pane").some((r) => r.status === 204), "Show pane did not complete with 204");
expect(page.sent("POST", "/team/pane").length === 1, "Show pane was repeated");
expect(page.sent("POST", "/team/pane")[0].body.role === "builder", "Show pane selected a different role");
await until(() => !page.$("#highbay-team-attention-show-pane-builder").disabled, "Show pane remained pending after empty success");
expect(page.sent("POST", "/team/agents").length === 0, "a read or pane action started agents");
page.$("#highbay-nav-team").click();
await until(() => page.state() === "team" && page.$("#highbay-team-role-builder"), "Team did not open");
expect(attention().includes("Builder needs you: question"), "Team hid the builder’s request");
expect(page.text("#highbay-team-role-builder").includes("Chorus watcher absent") && page.text("#highbay-team-role-builder").includes("Assignment unread"), "independent facts were inferred from terminal state");
expect(page.text("#highbay-team-role-test_writer").includes("Status unknown") && page.text("#highbay-team-role-test_writer").includes("Chorus watcher held"), "unknown terminal hid a known watcher");
expect(page.text("#highbay-team-role-test_writer").includes("No work now"), "the declared no_work assignment fact is missing");
expect(page.doc.activeElement === page.$("#highbay-team-title"), "opening Team did not focus its title");
page.$("#highbay-team-attention-show-pane-builder").focus();
await control(origin, "act", { key: "builder-working" });
await until(() => !page.$("#highbay-team-attention-builder"), "attention did not clear through updates");
await until(() => page.$("#highbay-team-role-builder").dataset.state === "working", "Team did not follow the same update");
expect(attention() === "Team status unknown.", "another role’s unknown status appeared settled");
await until(() => page.doc.activeElement === page.$("#highbay-team-title"), "removed attention control lost focus");
const teamReads = page.sent("GET", "/team").length;
expect(teamReads <= page.sent("GET", "/changes").length + 2, "Team introduced an independent polling loop");
page.close();
`)
}

// TestViewsPageTeamNamesUnreadAssignmentStep shows, above every view, the
// user's step for a role that needs them because it has waited with an unread
// assignment, with that role's Show pane as the primary action.
func TestViewsPageTeamNamesUnreadAssignmentStep(t *testing.T) {
	snapshot := pageTeamSnapshot("needs_you")
	snapshot.Roles[1].Attention = "assignment"
	f := &teamServiceFixture{snapshot: snapshot}
	cp := pageControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	ps := startPageServer(t, cp)
	for _, transition := range []struct{ chorus, watcher string }{
		{"unavailable", "unknown"}, {"connected", "held"},
	} {
		ps.act("chorus-"+transition.chorus, func() {
			f.mu.Lock()
			defer f.mu.Unlock()
			next := pageTeamSnapshot("needs_you")
			next.ChorusStatus = &transition.chorus
			next.Roles[1].Attention, next.Roles[1].Watcher = "assignment", transition.watcher
			f.snapshot = next
		})
	}
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-team-attention-builder"), "builder attention is missing outside Team");
expect(page.text("#highbay-team-attention-builder").includes("Builder needs you: idle with an unread assignment. Show its pane and tell it to read its assignments and to keep its Chorus watcher running."), "the user's step is missing: " + page.text("#highbay-team-attention-builder"));
const show = page.$("#highbay-team-attention-show-pane-builder");
expect(show !== null && show.textContent === "Show builder pane", "Show pane is missing");
expect(show.classList.contains("highbay-btn-primary"), "Show pane is not the primary action");
expect(page.$("#highbay-team-attention-test_writer") === null, "a role that does not need the user is listed");
await control(origin, "act", { key: "chorus-unavailable" });
await until(() => page.text("#highbay-team-chorus-status").includes("Running workers can wait for assignments through Highbay"), "the owner-reported Chorus outage did not reach the page");
const builderAttention = () => page.$("#highbay-team-attention-builder");
expect(builderAttention().textContent.includes("wait for assignments through Highbay") &&
       !builderAttention().textContent.includes("Chorus watcher"), "outage recovery still claimed a watcher was needed or absent");
const copyBuilderPrompt = () => Array.from(builderAttention().querySelectorAll("button")).find((b) => b.textContent === "Copy builder prompt");
let copied;
Object.defineProperty(page.win.navigator, "clipboard", { value: { writeText: async (text) => { copied = text; } } });
checkControl(page, copyBuilderPrompt(), "Copy builder prompt");
copyBuilderPrompt().click();
await until(() => builderAttention().textContent.includes("Prompt copied."), "copying the nudge was not announced");
expect(copied.includes("wait_for_builder_assignment") && copied.includes("since_version") && copied.includes("list_builder_assignments") &&
       !copied.includes("Chorus watcher"), "the nudge did not give the assigned worker its Highbay waiting path");
await control(origin, "act", { key: "chorus-connected" });
await until(() => !page.$("#highbay-team-chorus-status") && !copyBuilderPrompt(), "recovered Chorus left an active outage warning or stale prompt");
expect(page.sent("POST", "/team/agents").length === 0 && page.sent("POST", "/team/pane").length === 0,
       "reading/copying Team suggestions started or focused a worker");
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
		return &genhighbay.ProjectTeam{Team: "page-team", Space: "page-space", HerdrMode: "attached", Step: "The project agents have started.", Roles: []*genhighbay.ProjectTeamRole{}}, nil
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
