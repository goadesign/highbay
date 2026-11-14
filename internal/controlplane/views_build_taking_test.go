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
	for _, state := range []string{"building", "passed", "evaluator", "rewritten", "merged", "dropped", "empty"} {
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
			runPageScript(t, ps, map[string]any{"build": string(encoded), "state": state}, `
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
};
await control(origin, "reply", { key: "GET " + path, status: 200,
  body: { change_id: "chg-taking", build: { ...JSON.parse(input.build), state: "started", taking: variants[input.state] } } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-taking") && !page.$("#surla-build-loading"), "taking state did not appear");
const taking = page.text("#surla-build-taking");
if (["passed", "rewritten", "evaluator"].includes(input.state)) {
  const commands = page.$$("#surla-build-taking ol code").map((node) => node.textContent);
  expect(commands[0] === "git merge " + candidate && commands[1] === "surla/build-7", "candidate command used a branch tip or lost its verified branch");
  if (input.state === "evaluator") expect(commands.length === 2 && !taking.includes(tests), "stored evaluator offered a tests merge");
  else expect(commands[2] === "git merge " + tests && commands[3] === "surla/tests-7", "tests command did not follow the candidate's exact commit");
  expect(taking.includes("Surla does not merge or deploy anything"), "taking claims a merge side effect");
  expect(taking.includes("commit where this build started") === (input.state === "rewritten"), "rewrite warning does not follow base_in_branch");
} else {
  expect(!taking.includes("git merge"), "an unpassed or closed build offers merge commands");
  if (input.state === "building") expect(taking.includes("nothing to merge yet"), "building omitted the next step");
  if (input.state === "merged") expect(taking.includes("Merged into your branch") && taking.includes("No role has work"), "merged state is unclear");
  if (input.state === "dropped") expect(taking.includes("surla/tests-7") && !taking.includes("surla/build-7") && taking.includes("git log surla/tests-7"), "dropped state invented a retained branch");
  if (input.state === "empty") expect(taking.includes("Nothing was committed for it") && !taking.includes("surla/"), "empty dropped state invented work");
}
const open = ["building", "passed", "rewritten", "evaluator"].includes(input.state);
expect(Boolean(page.$("#surla-build-drop")) === open, "Drop availability does not follow taking");
if (!open) expect(!page.$("#surla-build-builder") && !page.$("#surla-build-test-writer"), "closed build still assigns work");
expect(page.sent("POST", path).length === 0 && page.sent("POST", path + "/drop").length === 0, "reading taking changed the build");
page.close();
`)
		})
	}
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
expect(page.text("#surla-build-blocking-change").includes("chg-open") && page.text("#surla-build-blocking-change").includes("nothing to merge yet"), "the refusal lost the other build's facts");
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
