// This file drives the Team controls through the served page and generated HTTP API.
package controlplane

import (
	"context"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/require"
)

// pageTeamSnapshot supplies independent terminal, watcher and assignment facts.
// Each replacement is immutable after publication to the fixture's readers.
func pageTeamSnapshot(state string) gensurla.TeamSnapshot {
	return gensurla.TeamSnapshot{HerdrMode: "attached", SavedChoices: map[string]*gensurla.RoleAgentChoice{}, DismissedNotices: []string{}, Team: "page-team", Roles: []*gensurla.TeamRoleState{
		{Role: "guide", Handle: "page-guide", State: "not_started", Attention: "none", Watcher: "unknown", Assignment: "unknown"},
		{Role: "builder", Handle: "page-builder", State: state, Attention: "question", Watcher: "gone", Assignment: "unread"},
		{Role: "test_writer", Handle: "page-writer", State: "unknown", Attention: "unknown", Watcher: "held", Assignment: "nothing_owed"},
	}}
}

// TestViewsPageDescribeGuideStatusControlsNext follows the same Team reads
// as Describe and checks that their guidance yields to an existing card.
func TestViewsPageDescribeGuideStatusControlsNext(t *testing.T) {
	f := &teamServiceFixture{snapshot: gensurla.TeamSnapshot{HerdrMode: "attached", SavedChoices: map[string]*gensurla.RoleAgentChoice{}, DismissedNotices: []string{},
		Team: "page-team", Roles: []*gensurla.TeamRoleState{},
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
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
await control(origin, "hold", { key: "GET /team" });
const page = await openPage(origin, { link: input.link });
const next = () => page.text("#surla-frame-next dd");
const attention = () => page.text(".surla-frame-attention dd");
const connectGuide = "Connect your coding agent to this project’s Surla MCP server.";
await until(() => page.$("#surla-change-list-empty") && page.sent("GET", "/team").length === 1, "empty Describe did not begin reading Team");
expect(next() === "Wait for Team status.", "pending Team read was treated as an absent guide");
expect(attention() === "Waiting for Team status.", "unread Team status appeared settled on Describe");
page.$("#surla-nav-team").click();
expect(attention() === "Waiting for Team status.", "unread Team status appeared settled on Team");
page.$("#surla-nav-changes").click();
await control(origin, "release", { key: "GET /team" });
await until(() => next().includes(connectGuide), "Describe did not explain how to connect a guide");
page.$("#surla-nav-team").click();
expect(next() === connectGuide && !page.$("#surla-team-start"), "Team offered worker startup without a build assignment");
page.$("#surla-nav-changes").click();

await control(origin, "reply", { key: "GET /team", status: 503, body: { name: "unavailable", message: "Team unavailable." } });
await control(origin, "act", { key: "unknown" });
await until(() => next().includes("Open Team to retry"), "a failed Team read still used its previous empty observation");
expect(attention() === "Team status could not be read.", "failed Team status appeared settled on Describe");
page.$("#surla-nav-team").click();
await until(() => page.$("#surla-team-role-guide")?.dataset.state === "unknown", "Team did not retry its failed read");
page.$("#surla-nav-changes").click();
expect(next().includes("refresh to check your coding agent’s status"), "unknown attached coding agent appeared ready or absent");
await control(origin, "act", { key: "starting" });
await until(() => next().includes("Wait for your coding agent to start"), "a starting coding agent did not tell the user to wait");
await control(origin, "act", { key: "working" });
await until(() => !page.$(".surla-frame-slots") && page.$("#surla-empty-show-guide"), "a working attached coding agent did not lead to its existing pane");
for (const mode of ["available", "unavailable"]) {
  await control(origin, "act", { key: "external-" + mode });
  await until(() => !page.$("#surla-empty-show-guide") && !page.$(".surla-frame-slots"), "external coding agent was treated as a Herdr pane");
  expect(page.text("#surla-change-list-title").includes("Describe what you want to build") &&
         !page.$("#surla-nav-changes") && !page.$(".surla-frame-context"), "healthy empty state kept unnecessary frame chrome");
  page.$("#surla-nav-team").click();
  const codingAgent = page.$("#surla-team-role-guide");
  expect(codingAgent.textContent.includes("Coding agent registered · No Herdr pane linked") &&
         codingAgent.textContent.includes("open a project tab in Herdr and run /surla") &&
         !codingAgent.textContent.includes("Chorus watcher unknown") &&
         !page.$("#surla-team-show-guide"), "Team showed a false warning or pane action for external mode " + mode);
  page.$("#surla-nav-changes").click();
}
await control(origin, "act", { key: "worker-unknown" });
await until(() => attention() === "Team status unknown." && next().includes("Open Team"), "a genuinely unobserved worker was hidden with the external guide's expected state");
await control(origin, "act", { key: "ended" });
await until(() => next().includes(connectGuide), "Describe did not explain how to reconnect an ended guide");
page.$("#surla-nav-team").click();
for (const state of ["ended", "not_started"]) {
  if (state === "not_started") await control(origin, "act", { key: state });
  await until(() => page.$("#surla-team-role-guide")?.dataset.state === state && !page.$("#surla-team-loading"), "Team did not read " + state);
  expect(next() === connectGuide && !page.$("#surla-team-start"), "Team offered worker startup for guide state " + state + " without a build assignment");
}
page.$("#surla-nav-changes").click();

await control(origin, "reply", { key: "GET /changes/chg-guide-next", status: 503, body: { name: "unavailable", message: "Card unavailable." } });
await propose("chg-guide-next", "Route refunds to a manager.", "Auto-approved");
await until(() => page.$("#surla-change-open-error"), "the failed card read did not appear");
expect(next().includes("Try opening the change again"), "card recovery did not take precedence over ordinary guidance");
page.$("#surla-open-change-chg-guide-next").click();
await until(() => page.$("#surla-cp1-editor").dataset.changeId === "chg-guide-next", "the existing card remained unavailable without a running guide");
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
await until(() => page.status() === "This project has no changes yet." && page.$("#surla-team-attention"), "empty Describe did not settle");
const live = page.$("#surla-team-attention"), operation = page.status();
expect(live.getAttribute("role") === "status" && live.getAttribute("aria-live") === "polite", "attention owner is not an announced live region");
expect(live.textContent === "", "idle roles invented an attention state");
await control(origin, "act", { key: "guide-permission" });
await until(() => page.text("#surla-team-attention-guide").includes("Coding agent needs you: permission"), "real permission attention was suppressed");
expect(page.text(".surla-frame-attention dd").includes("Coding agent needs you: permission") && page.text("#surla-frame-next dd").includes("Show coding agent pane"), "Describe did not surface the coding agent’s reported blocker and pane");
expect(page.$("#surla-team-attention") === live, "attention arrived in a newly mounted live region");
expect(page.status() === operation, "team attention overwrote operation feedback");
await control(origin, "act", { key: "two-roles" });
await until(() => page.text("#surla-team-attention-guide").includes("question") && page.text("#surla-team-attention-builder").includes("sign-in required"), "changed and additional real blockers were not shown");
expect(!live.textContent.includes("permission"), "replaced permission attention stayed stale");
await control(origin, "act", { key: "guide-done" });
await until(() => !page.$("#surla-team-attention-guide"), "done guide retained an attention line");
expect(page.text("#surla-team-attention-builder").includes("Builder needs you: sign-in required"), "guide recovery hid another role's real blocker");
expect(page.text(".surla-frame-attention dd").includes("Builder needs you: sign-in required"), "Describe lost the builder’s current request");
await control(origin, "act", { key: "all-done" });
await until(() => live.textContent === "", "recovered roles retained stale attention");
expect(page.status() === operation && !page.status().includes("needs you"), "recovery left a duplicate stale status or replaced operation feedback");
expect(page.$("#surla-team-attention") === live && live.getAttribute("aria-live") === "polite", "recovery removed the next attention announcement owner");
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
await until(() => page.$("#surla-team-attention-builder") && page.$("#surla-build-section")?.dataset.state === "not_started" && !page.$("#surla-build-loading"), "the eligible build or builder attention did not arrive");
expect(page.text("#surla-team-attention-builder").includes("Builder needs you: question"), "attention reason is missing");
const attention = () => page.text(".surla-frame-attention dd");
expect(attention().includes("Builder needs you: question"), "unstarted Build hid the builder’s request");
for (const stage of ["changes", "cp1", "cp2", "story", "live"]) {
  page.$("#surla-nav-" + stage).click();
  expect(attention().includes("Builder needs you: question"), stage + " hid the builder’s request");
}
page.$("#surla-team-attention-show-pane-builder").click();
await until(() => page.sent("POST", "/team/pane").some((r) => r.status === 204), "Show pane did not complete with 204");
expect(page.sent("POST", "/team/pane").length === 1, "Show pane was repeated");
expect(page.sent("POST", "/team/pane")[0].body.role === "builder", "Show pane selected a different role");
await until(() => !page.$("#surla-team-attention-show-pane-builder").disabled, "Show pane remained pending after empty success");
expect(page.sent("POST", "/team/agents").length === 0, "a read or pane action started agents");
page.$("#surla-nav-team").click();
await until(() => page.state() === "team" && page.$("#surla-team-role-builder"), "Team did not open");
expect(attention().includes("Builder needs you: question"), "Team hid the builder’s request");
expect(page.text("#surla-team-role-builder").includes("Chorus watcher absent") && page.text("#surla-team-role-builder").includes("Assignment unread"), "independent facts were inferred from terminal state");
expect(page.text("#surla-team-role-test_writer").includes("Status unknown") && page.text("#surla-team-role-test_writer").includes("Chorus watcher held"), "unknown terminal hid a known watcher");
expect(page.text("#surla-team-role-test_writer").includes("No work now"), "the declared no_work assignment fact is missing");
expect(page.doc.activeElement === page.$("#surla-team-title"), "opening Team did not focus its title");
page.$("#surla-team-attention-show-pane-builder").focus();
await control(origin, "act", { key: "builder-working" });
await until(() => !page.$("#surla-team-attention-builder"), "attention did not clear through updates");
await until(() => page.$("#surla-team-role-builder").dataset.state === "working", "Team did not follow the same update");
expect(attention() === "Team status unknown.", "another role’s unknown status appeared settled");
await until(() => page.doc.activeElement === page.$("#surla-team-title"), "removed attention control lost focus");
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
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-team-attention-builder"), "builder attention is missing outside Team");
expect(page.text("#surla-team-attention-builder").includes("Builder needs you: idle with an unread assignment. Show its pane and tell it to read its assignments and to keep its Chorus watcher running."), "the user's step is missing: " + page.text("#surla-team-attention-builder"));
const show = page.$("#surla-team-attention-show-pane-builder");
expect(show !== null && show.textContent === "Show builder pane", "Show pane is missing");
expect(show.classList.contains("surla-btn-primary"), "Show pane is not the primary action");
expect(page.$("#surla-team-attention-test_writer") === null, "a role that does not need the user is listed");
page.close();
`)
}

// TestViewsPageTeamStartsExplicitlyAndRetainsFailedFacts checks an explicit
// start receipt and a failed read without replacing known facts with emptiness.
func TestViewsPageTeamStartsExplicitlyAndRetainsFailedFacts(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("needs_you")}
	f.start = func(context.Context) (*gensurla.ProjectTeam, error) {
		f.mu.Lock()
		f.snapshot = pageTeamSnapshot("working")
		f.mu.Unlock()
		return &gensurla.ProjectTeam{Team: "page-team", Space: "page-space", HerdrMode: "attached", Step: "The project agents have started.", Roles: []*gensurla.ProjectTeamRole{}}, nil
	}
	cp := pageControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-team-attention-builder"), "initial team was not read");
page.$("#surla-nav-team").click();
await until(() => page.text("#surla-team-start") === "Start workers", "Start workers is missing");
expect(page.sent("POST", "/team/agents").length === 0, "opening Team started workers");
await control(origin, "reply", { key: "GET /team", status: 503, body: { name: "unavailable", id: "team-read", message: "Team owner unavailable.", temporary: true, timeout: false, fault: false } });
page.$("#surla-team-refresh").click();
await until(() => page.$("#surla-team-read-error"), "read failure was not shown");
expect(page.text("#surla-team-read-error") === "Could not read the project team. Team owner unavailable.", "Team did not name the failed read before the raw diagnostic: " + page.text("#surla-team-read-error"));
expect(page.$("#surla-team-read-error").getAttribute("role") === "alert", "the failed Team read is not announced as an alert");
expect(page.text("#surla-team-stale").includes("Last-read"), "retained facts are presented as current");
expect(page.text("#surla-team-attention-builder").includes("Last read:"), "global attention is presented as current after read failure");
expect(page.text(".surla-frame-attention dd") === "Team status could not be read.", "Team frame presented retained attention as current");
expect(page.$("#surla-team-role-builder").dataset.state === "needs_you", "failed read discarded known facts");
page.$("#surla-team-refresh").click();
await until(() => !page.$("#surla-team-read-error"), "retry did not recover");
expect(!page.text("#surla-team-attention-builder").includes("Last read:"), "recovered facts remain marked stale");
await control(origin, "hold", { key: "POST /team/agents" });
page.$("#surla-team-start").click();
await until(() => page.sent("POST", "/team/agents").length === 1, "the explicit start request did not begin");
expect(page.$("#surla-team-start").disabled && page.text("#surla-frame-next dd") === "Wait for workers to start.", "pending startup was presented as another action");
await control(origin, "release", { key: "POST /team/agents" });
await until(() => page.$("#surla-team-start-receipt"), "explicit start receipt is missing");
expect(page.text("#surla-team-start-receipt").includes("This start (begun ") && page.text("#surla-team-start-receipt").includes("The project agents have started."), "receipt is not dated owner text");
await until(() => page.$("#surla-team-role-builder").dataset.state === "working" && !page.$("#surla-team-attention-builder"), "current facts were replaced by the historical start receipt");
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
		_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &gensurla.StartBuildPayload{
			ChangeID: "chg", RequestRevision: card.RequestRevision,
		})
		require.NoError(t, err)
		f.mu.Lock()
		f.snapshot = pageTeamSnapshot("needs_you")
		f.mu.Unlock()
	})
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-section")?.dataset.state === "not_started" && !page.$("#surla-build-loading"), "the initial selected build was not read");
page.$("#surla-nav-live").click();
await until(() => page.$("#surla-model-name"), "the model form did not open");
const provider = page.$("#surla-model-provider");
provider.value = "openai";
provider.dispatchEvent(new page.win.Event("change", { bubbles: true }));
const name = page.$("#surla-model-name");
page.type(name, "unsent-model-choice");
page.$("input[name='surla-model-kind'][value='gateway']").click();
const gateway = page.$("#surla-model-gateway");
page.type(gateway, "127.0.0.1:9455");
gateway.focus();
gateway.setSelectionRange(2, 7);
const buildReads = page.sent("GET", "/changes/chg/build").length;
await control(origin, "act", { key: "start-build-and-attention" });
await until(() => page.sent("GET", "/changes/chg/build").slice(buildReads).some((r) => r.status === 200 && JSON.parse(r.answer).build.state === "started") && page.$("#surla-team-attention-builder"), "background build and team changes did not reach Live");
// Let the completed owner answer render before checking the retained fields.
await sleep(0);
expect(page.$("#surla-model-name") === name && name.value === "unsent-model-choice", "a new update reason replaced the model input");
expect(page.$("#surla-model-provider") === provider && provider.value === "openai", "a new update reason lost the provider");
expect(page.$("#surla-model-gateway") === gateway && gateway.value === "127.0.0.1:9455" && page.doc.activeElement === gateway && gateway.selectionStart === 2 && gateway.selectionEnd === 7, "a new update reason changed gateway text, focus or selection");
expect(page.$("input[name='surla-model-kind'][value='gateway']").checked, "a new update reason changed the model kind");
page.$("#surla-nav-team").click();
await until(() => page.$("#surla-team-title"), "Team did not open");
page.$("#surla-nav-live").click();
await until(() => page.$("#surla-model-gateway"), "Live did not reopen");
expect(page.$("input[name='surla-model-kind'][value='gateway']").checked && page.$("#surla-model-provider").value === "openai" && page.$("#surla-model-name").value === "unsent-model-choice" && page.$("#surla-model-gateway").value === "127.0.0.1:9455", "the Team round-trip lost model draft fields");
expect(page.sent("POST", "/cp1/model").length === 0, "an update or navigation applied the unsent model draft");
page.$("#surla-nav-team").click();
await until(() => page.$("#surla-team-refresh") && !page.$("#surla-team-refresh").disabled, "Team did not become ready");
await control(origin, "reply", { key: "GET /team", status: 401, body: { name: "unauthorized", message: "Session ended." } });
page.$("#surla-team-refresh").click();
await until(() => page.$("#surla-checkpoint-root").dataset.session === "ended", "the refused session remained active");
expect(page.text("#surla-team-attention-builder").includes("Last read:"), "ended session shows retained attention as current");
expect(page.text("#surla-team-read-error") === "The session ended; these are last-read facts.", "ended session is presented as a failed team read: " + page.text("#surla-team-read-error"));
for (const button of page.$$("#surla-team-stage button, #surla-team-attention button")) {
  expect(button.disabled, "ended session left an active Team control: " + button.id);
}
expect(page.$("#surla-team-role-builder").dataset.state === "needs_you", "ended session discarded the observed team");
expect(page.sent("POST", "/cp1/model").length === 0, "session end applied the model draft");
page.close();
`)
}
