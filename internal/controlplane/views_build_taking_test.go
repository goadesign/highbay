// This file checks taking, exact Drop consent and typed build refusals through
// the served page. Synthetic taking answers isolate presentation; the Drop
// journey uses the real owner and does not claim candidate execution proof.
package controlplane

import (
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/require"
)

// TestViewsPageShowsTakingStates keeps commands bound to passed commit IDs and
// lists only the dropped build's retained branches, including an empty list.
func TestViewsPageShowsTakingStates(t *testing.T) {
	for _, state := range []string{"building", "passed", "evaluator", "rewritten", "merged", "dropped", "empty", "merged_findings", "dropped_findings"} {
		t.Run(state, func(t *testing.T) {
			cp := newBuildControlPlane(t, Options{Team: &TeamBinding{
				Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")},
			}})
			cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
			card := buildableChange(t, cp, "chg-taking")
			started, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &gensurla.StartBuildPayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
			})
			require.NoError(t, err)
			encoded, err := gensurla.EncodeStartedBuild(started.Build)
			require.NoError(t, err)
			ps := startPageServer(t, cp)
			ps.act("resave-taking", func() {
				cp.mu.Lock()
				defer cp.mu.Unlock()
				record, ok, err := cp.cards.load(card.ChangeID)
				require.NoError(t, err)
				require.True(t, ok)
				require.NoError(t, cp.cards.save(record))
			})
			runPageScript(t, ps, map[string]any{"build": string(encoded), "state": state, "notJudged": testsNotJudged}, `
const path = "/changes/chg-taking/build";
const candidate = "1".repeat(40), tests = "2".repeat(40);
const passed = { state: "passed", run_id: "run-taking", candidate_commit: candidate,
  candidate_branch: "surla/build-7", tests: { source: "written_tests", commit: tests, branch: "surla/tests-7" }, base_in_branch: true };
const variants = {
  building: { state: "building" }, passed,
  evaluator: { ...passed, tests: { source: "evaluator" } },
  rewritten: { ...passed, base_in_branch: false },
  merged: { state: "merged" },
  dropped: { state: "dropped", kept_branches: ["surla/tests-7"] },
  empty: { state: "dropped", kept_branches: [] },
  merged_findings: { state: "merged" },
  dropped_findings: { state: "dropped", kept_branches: [] },
};
const saved = JSON.parse(input.build);
if (input.state !== "building") saved.test_writer.tests = { state: "accepted", commit: tests,
  checked_at: saved.started_at, not_judged: input.notJudged };
const closedFindings = input.state.endsWith("_findings");
if (closedFindings) saved.test_writer.tests = { state: "needs_changes", checked_at: saved.started_at, finding_count: 1 };
await control(origin, "reply", { key: "GET " + path, status: 200,
  body: { change_id: "chg-taking", build: { ...saved, state: "started", taking: variants[input.state] } } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-taking") && !page.$("#surla-build-loading"), "taking state did not appear");
const taking = page.text("#surla-build-taking");
if (["passed", "rewritten", "evaluator"].includes(input.state)) {
  expect(page.text("#surla-build-state") === "The builder’s commit passed the accepted tests.", "passed build still describes future building or checking");
  expect(page.$("#surla-build-state").nextElementSibling?.classList.contains("surla-build-merge"), "merge instructions are not immediately below the passed state");
  expect(!taking.includes("builder finishes") && !taking.includes("after the check passes"), "passed build retains a stale next step");
  const commands = page.$$("#surla-build-taking ol code").map((node) => node.textContent);
  expect(commands[0] === "git merge " + candidate && commands[1] === "surla/build-7", "candidate command used a branch tip or lost its verified branch");
  if (input.state === "evaluator") expect(commands.length === 2 && !taking.includes(tests), "stored evaluator offered a tests merge");
  else expect(commands[2] === "git merge " + tests && commands[3] === "surla/tests-7", "tests command did not follow the candidate's exact commit");
  expect(taking.includes("Surla does not merge or deploy anything"), "taking claims a merge side effect");
  expect(taking.includes("commit where this build started") === (input.state === "rewritten"), "rewrite warning does not follow base_in_branch");
} else {
  expect(!taking.includes("git merge"), "an unpassed or closed build offers merge commands");
  if (input.state === "building") expect(taking.includes("Next: the test writer prepares tests") && !taking.includes("nothing to merge yet"), "building lost its next step or repeats an unpassed summary");
  if (variants[input.state].state === "merged") expect(page.text("#surla-build-state") === "Merged." && taking.includes("No role has work"), "merged state is unclear");
  if (input.state === "dropped") expect(taking.includes("surla/tests-7") && !taking.includes("surla/build-7") && taking.includes("git log surla/tests-7"), "dropped state invented a retained branch");
  if (input.state === "empty") expect(taking.includes("Nothing was committed for it") && !taking.includes("surla/"), "empty dropped state invented work");
}
const open = ["building", "passed", "rewritten", "evaluator"].includes(input.state);
expect(Boolean(page.$("#surla-build-drop")) === open, "Drop availability does not follow taking");
for (const selector of ["#surla-build-builder", "#surla-build-test-writer", ...(input.state === "building" || closedFindings ? [] : ["#surla-build-not-judged"])]) {
  expect(Boolean(page.$(selector)?.closest("#surla-build-details")) === (input.state !== "building"), "role facts did not follow the active-versus-saved disclosure");
}
expect(page.$("#surla-build-details").textContent.includes("No tests of this change’s Story were required."), "required-test facts are not in the saved disclosure");
expect(!page.$("#surla-build-details").open, "secondary build facts expanded by default");
const buttons = page.$$(".surla-build-actions button");
expect(buttons.length > 0 && (!open || buttons.at(-1).id === "surla-build-drop"), "Drop is not last in the single action row");
expect(page.$$(".surla-build-actions").length === 1, "build actions are split into separate rows");
const state = variants[input.state].state;
expect(page.text("#surla-build-taking-state") === state[0].toUpperCase() + state.slice(1), "the header badge does not describe the saved taking state");
expect(Boolean(page.$(".surla-build-header .surla-build-actions")), "current build controls are not in the header");
expect(Boolean(page.$("#surla-build-start-explanation")) === (state === "dropped"), "the execution warning appears outside the agreed Build-offer states");
if (closedFindings) {
  expect(page.text("#surla-build-findings").includes("1 finding") && page.$("#surla-build-findings").closest("#surla-build-details"), "closed build lost its historical finding count");
  expect(!page.$("#surla-build-findings button") && page.$("#surla-build-findings").getAttribute("aria-busy") === "false", "closed build exposes active findings controls or loading");
  expect(page.sent("GET", path + "/findings").length === 0, "closed historical facts read active findings");
}
if (open) {
  page.$("#surla-build-drop").click();
  await until(() => page.$("#surla-build-drop-cancel"), "Drop did not ask");
  expect(page.text("#surla-build-drop-cancel") === (input.state === "building" ? "Keep building" : "Keep this build"), "Drop cancel assumes passed work is still building");
  page.$("#surla-build-drop-cancel").click();
  await until(() => !page.$("#surla-build-drop-question"), "Drop cancel did not close");
  expect(page.doc.activeElement === page.$("#surla-build-drop"), "Drop cancel lost trigger focus");
}
if (input.state === "passed") {
  const replaceTaking = async (taking) => {
    await control(origin, "reply", { key: "GET " + path, status: 200,
      body: { change_id: "chg-taking", build: { ...saved, state: "started", taking } } });
    await control(origin, "act", { key: "resave-taking" });
    await until(() => page.$("#surla-build-taking")?.dataset.state === taking.state && !page.$("#surla-build-loading"), "taking refresh did not settle");
  };
  page.$("#surla-build-taking pre").focus();
  await replaceTaking(variants.building);
  expect(page.doc.activeElement === page.$("#surla-build-title"), "removing a focused merge command lost the stable Build heading");
  await replaceTaking(passed);
  const field = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
  page.type(field, "keep editor focus when commands vanish"); field.focus();
  await replaceTaking(variants.merged);
  expect(page.doc.activeElement === field && field.value === "keep editor focus when commands vanish", "removing unfocused commands stole external editor focus");
}
expect(page.sent("POST", path).length === 0 && page.sent("POST", path + "/drop").length === 0, "reading taking changed the build");
page.close();
`)
		})
	}
}

// TestViewsPagePassedBlockerClearsOnlyAfterItsOwnFreshRead checks that a
// selected change's absent build cannot clear another change's open pass.
func TestViewsPagePassedBlockerClearsOnlyAfterItsOwnFreshRead(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{
		Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")},
	}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := buildableChange(t, cp, "chg-passed-open")
	buildableChange(t, cp, "chg-passed-next")
	user := asCaller(pageOwner, CallerUser)
	started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	encoded, err := gensurla.EncodeStartedBuild(started.Build)
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	ps.act("drop-passed-open", func() {
		_, err := cp.DropBuild(user, &gensurla.DropBuildPayload{
			ChangeID: card.ChangeID, StartedAt: started.Build.StartedAt,
		})
		require.NoError(t, err)
	})
	ps.act("resave-next", func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		record, ok, err := cp.cards.load("chg-passed-next")
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, cp.cards.save(record))
	})
	runPageScript(t, ps, map[string]any{"build": string(encoded)}, `
const path = "/changes/chg-passed-next/build", other = "/changes/chg-passed-open/build";
const saved = { ...JSON.parse(input.build), taking: { state: "passed", run_id: "passed-open",
  candidate_commit: "1".repeat(40), candidate_branch: "surla/build-7", tests: { source: "evaluator" }, base_in_branch: true } };
await control(origin, "reply", { key: "POST " + path, status: 409,
  body: { name: "previous_change_open", message: "Merge or drop the open change.", change_id: "chg-passed-open", build: saved } });
await control(origin, "reply", { key: "GET " + other, status: 200, body: { change_id: "chg-passed-open", build: { ...saved, state: "started" } } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-open-change-chg-passed-next"), "change list did not load");
page.$("#surla-open-change-chg-passed-next").click();
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "next change did not offer Build");
page.$("#surla-build-start").click();
await until(() => page.$("#surla-build-blocking-change") && page.sent("GET", other).length > 0 && !page.$("#surla-build-loading"), "blocker was not read by its exact change ID");
const message = page.text("#surla-build-action-error");
expect(message.includes("chg-passed-open has an open build") && message.includes("commit passed the accepted tests") && !message.includes("Nothing of it has passed"), "passed blocker was described as unpassed");
expect(page.$("#surla-build-start").disabled && page.text("#surla-build-taking").includes("git merge " + "1".repeat(40)), "selected not_started observation cleared the passed blocker");
expect(!page.text("#surla-checkpoint-status").includes("Build did not start"), "blocker was duplicated in global status");
page.$("#surla-build-drop").click();
await until(() => page.$("#surla-build-drop-question"), "old passed blocker did not ask for Drop consent");
const replacement = { ...saved, started_at: new Date(Date.parse(saved.started_at) + 1000).toISOString(), taking: { state: "building" } };
await control(origin, "reply", { key: "GET " + other, status: 200,
  body: { change_id: "chg-passed-open", build: { ...replacement, state: "started" } } });
await control(origin, "act", { key: "resave-next" });
await until(() => !page.$("#surla-build-drop-question") && page.$("#surla-build-taking")?.dataset.state === "building", "a freshly observed different open start did not invalidate old consent");
expect(page.$("#surla-build-start").disabled && !page.text("#surla-build-taking").includes("git merge"), "fresh replacement retained old merge commits or unblocked the selected Build");
expect(page.sent("POST", other + "/drop").length === 0, "fresh replacement reused old Drop consent");
const field = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
page.type(field, "keep this draft through blocker recovery");
await control(origin, "reply", { key: "GET " + other, status: 200,
  body: { change_id: "chg-passed-open", build: { ...saved, state: "started", taking: { state: "dropped", kept_branches: [] } } } });
await control(origin, "act", { key: "drop-passed-open" });
await until(() => !page.$("#surla-build-blocking-change") && page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "fresh closed blocker did not reenable Build");
expect(field === page.$('textarea[data-example-id="check_01"][data-field="scenario"]') && field.value === "keep this draft through blocker recovery", "blocker recovery replaced unsaved editor text");
expect(page.sent("POST", path).length === 1, "a fresh read retried Build without consent");
page.close();
`)
}

// TestViewsPageOpenBlockingChangePreservesDraftChoices exercises the new
// navigation through the existing preserved-text recovery boundary.
func TestViewsPageOpenBlockingChangePreservesDraftChoices(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{
		Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")},
	}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	open := buildableChange(t, cp, "chg-nav-open")
	next := buildableChange(t, cp, "chg-nav-next")
	_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &gensurla.StartBuildPayload{
		ChangeID: open.ChangeID, RequestRevision: open.RequestRevision,
	})
	require.NoError(t, err)
	encoded, err := gensurla.EncodeChangeCardResult(next)
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"card": string(encoded)}, `
const card = { ...JSON.parse(input.card), proposed_revision: {
  request_revision: "f".repeat(64), summary: "A proposed revision", prompt: "Choose the next result.",
  ambiguities: [{ id: "q-review", question: "Which result?", options: ["keep", "replace"] }],
  examples: [], preserved_behaviors: [], changed_design_files: [],
} };
await control(origin, "reply", { key: "GET /changes/chg-nav-next", status: 200, body: card });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-open-change-chg-nav-next"), "change list did not load");
page.$("#surla-open-change-chg-nav-next").click();
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled && page.$("#surla-proposal"), "proposed change did not open");
page.$('#surla-proposal [data-question-id="q-review"] [data-option="keep"]').click();
page.type(page.$('textarea[data-example-id="check_01"][data-field="scenario"]'), "keep my local scenario");
page.$("#surla-build-start").click();
await until(() => page.$("#surla-build-open-blocking-change"), "other-change recovery action did not appear");
page.$("#surla-build-open-blocking-change").click();
await until(() => page.$("#surla-cp1-editor")?.dataset.changeId === "chg-nav-open", "open-blocking-change action did not show its subject");
const preserved = page.text("#surla-preserved-draft");
expect(preserved.includes("keep my local scenario") && preserved.includes("[Proposed " + "f".repeat(64) + "] q-review (Which result?)=keep"), "opening the blocker lost unsent draft text or proposal choices");
page.$("#surla-nav-cp1").click();
expect(page.text("#surla-preserved-draft") === preserved, "returning to the same card duplicated preserved proposal choices");
expect(page.sent("POST", "/changes/confirm").length === 0 && page.sent("POST", "/changes/chg-nav-open/build/drop").length === 0, "navigation adopted a proposal or dropped work");
page.close();
`)
}

// TestViewsPageDropRequiresExactConsentAndPersists exercises the owner write,
// cancellation, duplicate-click guard, retained edits and a new page's read.
func TestViewsPageDropRequiresExactConsentAndPersists(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{
		Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")},
	}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := buildableChange(t, cp, "chg-drop")
	started, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"started": started.Build.StartedAt}, `
const path = "/changes/chg-drop/build/drop";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-drop") && !page.$("#surla-build-drop").disabled, "Drop did not become ready");
const field = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
page.type(field, "keep my unsaved scenario");
page.$("#surla-build-drop").click();
await until(() => page.$("#surla-build-drop-confirm"), "Drop did not ask separately");
expect(page.text("#surla-build-drop-question").includes("edits it has not proposed are lost") && page.text("#surla-build-drop-question").includes("your branch is unchanged"), "Drop omitted its effects");
expect(page.sent("POST", path).length === 0, "opening Drop changed a build");
page.$("#surla-build-drop-confirm").dispatchEvent(new page.win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
await until(() => !page.$("#surla-build-drop-question") && page.doc.activeElement === page.$("#surla-build-drop"), "Escape did not restore the Drop trigger");
page.$("#surla-build-drop").click();
await until(() => page.$("#surla-build-drop-cancel"), "Drop did not reopen");
page.$("#surla-build-drop-cancel").click();
await until(() => !page.$("#surla-build-drop-question") && !page.$("#surla-build-drop").disabled, "Cancel did not restore Drop");
expect(page.sent("POST", path).length === 0, "cancelling Drop sent a write");
await control(origin, "hold", { key: "POST " + path });
page.$("#surla-build-drop").click();
await until(() => page.$("#surla-build-drop-confirm"), "final Drop did not ask");
const confirm = page.$("#surla-build-drop-confirm");
confirm.click(); confirm.click();
await until(() => page.sent("POST", path).length === 1, "Drop did not send exactly one write");
const body = page.sent("POST", path)[0].body;
expect(body.started_at === input.started && Object.keys(body).length === 1, "Drop did not send the exact saved start; change_id belongs in its generated route");
await control(origin, "release", { key: "POST " + path });
await until(() => page.$("#surla-build-taking")?.dataset.state === "dropped" && !page.$("#surla-build-loading"), "owner did not show the durable dropped state");
expect(!page.$("#surla-build-drop") && !page.$("#surla-build-drop-question"), "closed build still offers Drop");
expect(field === page.$('textarea[data-example-id="check_01"][data-field="scenario"]') && field.value === "keep my unsaved scenario", "Drop lost unsaved editor state");
expect(page.sent("POST", path).length === 1, "Drop submitted twice");
page.close();
const again = await openPage(origin, { link: input.link + "-2" });
await until(() => again.$("#surla-build-taking")?.dataset.state === "dropped", "a new page did not read the saved drop");
again.close();
`)
}

// TestViewsPageOpenBuildRefusalRecoversStaleDrop shows another change's typed
// refusal, sends the shown start, then requires new consent after a stale Drop.
func TestViewsPageOpenBuildRefusalRecoversStaleDrop(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{
		Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")},
	}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	open := buildableChange(t, cp, "chg-open")
	buildableChange(t, cp, "chg-next")
	user := asCaller(pageOwner, CallerUser)
	started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: open.ChangeID, RequestRevision: open.RequestRevision,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	ps.act("replace-open", func() {
		_, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
			ChangeID: open.ChangeID, RequestRevision: open.RequestRevision,
			Replaces: &gensurla.ReplacedBuild{ChangeID: open.ChangeID, StartedAt: started.Build.StartedAt},
		})
		require.NoError(t, err)
	})
	runPageScript(t, ps, map[string]any{"started": started.Build.StartedAt}, `
const nextPath = "/changes/chg-next/build", dropPath = "/changes/chg-open/build/drop";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-open-change-chg-next"), "change list did not open");
page.$("#surla-open-change-chg-next").click();
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "next change did not offer Build");
page.$("#surla-build-start").click();
await until(() => page.$("#surla-build-blocking-change") && page.$("#surla-build-drop") && !page.$("#surla-build-drop").disabled, "typed previous_change_open did not offer the other change's Drop");
const refusalText = "Build did not start: change chg-open has an open build. Nothing of it has passed its check yet: let its builder finish, or drop it.";
expect(page.text("#surla-build-action-error") === refusalText, "the refusal lost the other build's state and next step");
expect(!page.text("#surla-checkpoint-status").includes("Build did not start") && page.text("#surla-build-blocking-change").split(refusalText).length === 2, "the same refusal is repeated outside its card alert");
expect(page.$("#surla-build-start").disabled && page.$("#surla-build-start").getAttribute("aria-describedby") === "surla-build-action-error", "the blocked Build action lacks its disabled reason");
expect(page.text("#surla-build-drop") === "Drop chg-open’s build…" && page.text("#surla-build-open-blocking-change") === "Open change chg-open", "other-change recovery does not identify its subject");
await until(() => page.sent("GET", "/changes/chg-open/build").some((reply) => reply.status === 200), "initial exact-other observation did not settle");
await control(origin, "hold", { key: "GET /changes/chg-open/build" });
page.$("#surla-build-drop").click();
await until(() => page.$("#surla-build-drop-confirm"), "other build's Drop did not ask");
await control(origin, "act", { key: "replace-open" });
await until(() => page.$("#surla-build-drop-confirm") && !page.$("#surla-build-drop-confirm").disabled, "unrelated selected-card refresh destroyed consent instead of preserving its exact old identity");
page.$("#surla-build-drop-confirm").click();
await until(() => page.sent("POST", dropPath)[0]?.status === 409 && page.text("#surla-build-action-error").includes("open build changed"), "stale Drop did not show the typed replacement");
const refusal = JSON.parse(page.sent("POST", dropPath)[0].answer);
expect(refusal.name === "build_changed" && refusal.build.started_at !== input.started, "fixture did not replace the open build");
expect(page.sent("POST", dropPath)[0].body.started_at === input.started, "Drop silently changed the consent identity");
expect(!page.$("#surla-build-drop-question") && page.sent("POST", dropPath).length === 1, "stale Drop retried or kept its stale question");
await control(origin, "release", { key: "GET /changes/chg-open/build" });
await until(() => page.$("#surla-build-drop") && !page.$("#surla-build-drop").disabled && !page.$("#surla-build-loading"), "replacement facts did not recover Drop");
page.$("#surla-build-drop").click();
await until(() => page.$("#surla-build-drop-confirm"), "replacement build did not require fresh Drop consent");
page.$("#surla-build-drop-confirm").click();
await until(() => page.sent("POST", dropPath)[1]?.status === 200 && !page.$("#surla-build-blocking-change"), "fresh consent did not drop the replacement");
expect(page.sent("POST", dropPath)[1].body.started_at === refusal.build.started_at, "fresh Drop did not name the typed replacement start");
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "selected change's Build did not recover");
page.$("#surla-build-start").click();
await until(() => page.text("#surla-build-action-error").includes("refresh_story") && page.text("#surla-build-action-error").includes("adopt the revision, then build"), "drafted_before_drop did not show the guide/revise/adopt recovery");
page.close();
`)
}

// TestViewsPageUnfinishedRefusalRefreshesBeforeQuestion covers a start saved
// after the page's stale absent-build observation, without automatic consent.
func TestViewsPageUnfinishedRefusalRefreshesBeforeQuestion(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{
		Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")},
	}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := buildableChange(t, cp, "chg-unfinished")
	started, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"started": started.Build.StartedAt}, `
const path = "/changes/chg-unfinished/build";
await control(origin, "reply", { key: "GET " + path, status: 200,
  body: { change_id: "chg-unfinished", build: { state: "not_started" } } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "stale absent observation did not offer first Build");
page.$("#surla-build-start").click();
await until(() => page.sent("POST", path)[0]?.status === 409 && page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "unfinished_build_open did not recover the saved build");
expect(JSON.parse(page.sent("POST", path)[0].answer).name === "unfinished_build_open", "the fixture did not exercise the typed unfinished refusal");
expect(!page.$("#surla-build-confirm") && !("replaces" in page.sent("POST", path)[0].body), "refusal invented replacement consent");
const reads = page.sent("GET", path).length;
await control(origin, "hold", { key: "GET " + path });
page.$("#surla-build-again").click();
await until(() => page.sent("GET", path).length > reads && page.$("#surla-build-loading"), "rebuild did not read its owner before asking");
expect(!page.$("#surla-build-confirm") && page.sent("POST", path).length === 1, "rebuild asked or wrote before the fresh owner answer");
await control(origin, "release", { key: "GET " + path });
await until(() => page.$("#surla-build-confirm") && !page.$("#surla-build-confirm").disabled, "fresh owner answer did not ask for explicit consent");
page.$("#surla-build-confirm").click();
await until(() => page.sent("POST", path)[1]?.status === 200, "fresh confirmed rebuild did not start");
expect(page.sent("POST", path)[1].body.replaces?.started_at === input.started && page.sent("POST", path)[1].body.replaces.change_id === "chg-unfinished", "confirmed replacement did not use the exact owner identity");
page.close();
`)
}

// TestViewsPageStaleDropKeepsFocusWhenTheTakingPanelIsRemoved checks the
// stable heading fallback for a refused A-to-B switch and preserves focus in
// the unsaved editor when a later owner observation removes B's panel.
func TestViewsPageStaleDropKeepsFocusWhenTheTakingPanelIsRemoved(t *testing.T) {
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{
		Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")},
	}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	first := buildableChange(t, cp, "chg-focus-a")
	second := buildableChange(t, cp, "chg-focus-b")
	_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &gensurla.StartBuildPayload{
		ChangeID: first.ChangeID, RequestRevision: first.RequestRevision,
	})
	require.NoError(t, err)
	other := fixtureOpenBuild(t, cp, second, buildRolesBuilderAndTestWriter)
	other.StartedAt = other.StartedAt.Add(time.Second)
	restored := fixtureOpenBuild(t, cp, first, buildRolesBuilderAndTestWriter)
	restored.StartedAt = other.StartedAt.Add(time.Second)
	ps := startPageServer(t, cp)
	for name, record := range map[string]buildFile{"other-build": other, "restore-build": restored} {
		ps.act(name, func() {
			cp.mu.Lock()
			err := cp.cards.saveOpenBuild(record)
			cp.mu.Unlock()
			require.NoError(t, err)
		})
	}
	runPageScript(t, ps, nil, `
const path = "/changes/chg-focus-a/build", drop = path + "/drop";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-open-change-chg-focus-a"), "change list did not load");
page.$("#surla-open-change-chg-focus-a").click();
await until(() => page.$("#surla-build-drop") && !page.$("#surla-build-drop").disabled && !page.$("#surla-build-loading"), "first build did not open");
await control(origin, "hold", { key: "GET " + path });
await control(origin, "hold", { key: "POST " + drop });
page.$("#surla-build-drop").click();
await until(() => page.$("#surla-build-drop-confirm"), "Drop did not ask");
page.$("#surla-build-drop-confirm").click();
await until(() => page.sent("POST", drop).length === 1 && page.doc.activeElement === page.$("#surla-build-taking"), "pending Drop did not retain a local focus target");
await control(origin, "act", { key: "other-build" });
await control(origin, "release", { key: "POST " + drop });
await until(() => page.sent("POST", drop)[0].status === 409 && page.$("#surla-build-blocking-change"), "stale Drop did not name the other build");
expect(page.doc.activeElement === page.$("#surla-build-title"), "removing the focused taking panel lost focus instead of using the stable Build heading");
await control(origin, "release", { key: "GET " + path });
await until(() => !page.$("#surla-build-loading"), "selected build recovery did not settle");
const field = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
page.type(field, "keep focused unsaved scenario"); field.focus();
await control(origin, "act", { key: "restore-build" });
await until(() => !page.$("#surla-build-blocking-change") && page.$("#surla-build-taking")?.dataset.state === "building", "fresh owner build did not remove the other panel");
expect(page.doc.activeElement === field && field === page.$('textarea[data-example-id="check_01"][data-field="scenario"]') && field.value === "keep focused unsaved scenario", "removing an unfocused taking panel stole or replaced editor focus");
expect(page.sent("POST", drop).length === 1, "focus recovery retried Drop");
page.close();
`)
}

// TestViewsPageOpenRebuildQuestionKeepsFocusDuringBackgroundRefresh preserves
// the user's editor and confirmation-button focus across unrelated Team reads.
func TestViewsPageOpenRebuildQuestionKeepsFocusDuringBackgroundRefresh(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := buildableChange(t, cp, "chg-question-focus")
	_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	for name, state := range map[string]string{"needs-attention": "needs_you", "working-again": "working"} {
		ps.act(name, func() {
			f.mu.Lock()
			f.snapshot = pageTeamSnapshot(state)
			f.mu.Unlock()
		})
	}
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "rebuild was not ready");
page.$("#surla-build-again").click();
await until(() => page.$("#surla-build-confirm") && !page.$("#surla-build-confirm").disabled, "rebuild did not ask");
const field = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
page.type(field, "focus stays in my unsaved text"); field.focus();
await control(origin, "act", { key: "needs-attention" });
await until(() => page.$("#surla-team-attention-builder") && page.$("#surla-build-confirm") && !page.$("#surla-build-confirm").disabled, "background Team update did not settle with the question open");
expect(page.doc.activeElement === field && field.value === "focus stays in my unsaved text", "background refresh pulled focus out of the editor into an unchanged question");
const confirm = page.$("#surla-build-confirm"); confirm.focus();
await control(origin, "act", { key: "working-again" });
await until(() => !page.$("#surla-team-attention-builder") && page.$("#surla-build-confirm") && !page.$("#surla-build-confirm").disabled, "second Team update did not settle");
expect(page.doc.activeElement === confirm && confirm === page.$("#surla-build-confirm"), "background refresh pulled focus from the confirmation control into its group");
expect(page.sent("POST", "/changes/chg-question-focus/build").length === 0, "focus preservation submitted a rebuild");
page.close();
`)
}
