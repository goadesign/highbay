// This file checks taking, exact Drop consent and typed build refusals through
// the served page. Synthetic taking answers isolate presentation; the Drop
// journey uses the real owner and does not claim candidate execution proof.
package controlplane

import (
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/require"
)

// TestViewsPageShowsTakingStates preserves passed commit evidence before approval and
// lists only the dropped build's retained branches, including an empty list.
func TestViewsPageShowsTakingStates(t *testing.T) {
	for _, state := range []string{"building", "passed", "evaluator", "rewritten", "merged", "merged_configured", "dropped", "empty", "merged_findings", "dropped_findings", "earlier_passed", "earlier_merged", "earlier_dropped", "earlier_configured"} {
		t.Run(state, func(t *testing.T) {
			cp := newBuildControlPlane(t, Options{Team: &TeamBinding{
				Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("working")},
			}})
			cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
			card := buildableChange(t, cp, "chg-taking")
			started, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
			})
			require.NoError(t, err)
			encoded, err := genhighbay.EncodeStartedBuild(started.Build)
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
  candidate_branch: "highbay/build-7", tests: { source: "written_tests", commit: tests, branch: "highbay/tests-7" }, base_in_branch: true };
const variants = {
  building: { state: "building" }, passed,
  evaluator: { ...passed, tests: { source: "evaluator" } },
  rewritten: { ...passed, base_in_branch: false },
  merged: { state: "merged" }, merged_configured: { state: "merged" },
  dropped: { state: "dropped", kept_branches: ["highbay/tests-7"] },
  empty: { state: "dropped", kept_branches: [] },
  merged_findings: { state: "merged" },
  dropped_findings: { state: "dropped", kept_branches: [] },
  earlier_passed: passed, earlier_merged: { state: "merged" },
  earlier_dropped: { state: "dropped", kept_branches: [] }, earlier_configured: { state: "merged" },
};
const saved = JSON.parse(input.build);
if (input.state.startsWith("earlier_")) {
  saved.in_force = false;
  saved.request_revision = "0".repeat(saved.request_revision.length);
}
if (input.state !== "building") saved.test_writer.tests = { state: "accepted", commit: tests,
  checked_at: saved.started_at, not_judged: input.notJudged };
const closedFindings = input.state.endsWith("_findings");
if (closedFindings) saved.test_writer.tests = { state: "needs_changes", checked_at: saved.started_at, finding_count: 1 };
if (input.state === "earlier_configured" || input.state === "merged_configured") saved.test_writer.tests = { state: "not_used" };
await control(origin, "reply", { key: "GET " + path, status: 200,
  body: { change_id: "chg-taking", drafted_before_drop: false, build: { ...saved, state: "started", taking: variants[input.state] } } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-nav-build"), "change navigation did not appear");
await until(() => page.sent("GET", path).some((r) => r.status !== undefined), "initial build read did not finish");
if (!page.$("#highbay-build-taking")) page.$("#highbay-nav-build").click();
await until(() => page.$("#highbay-build-taking") && !page.$("#highbay-build-loading"), "taking state did not appear");
await until(() => {
  const action = page.$("#highbay-build-start") ?? page.$("#highbay-build-again");
  return !action || !action.disabled;
}, "installed worker discovery did not enable the existing Build offer");
const taking = page.text("#highbay-build-taking");
expect(!page.$("#highbay-build-merge-feedback") && !page.$("#highbay-build-merge-feedback-refresh"), "unapproved build offered merge recovery before a recorded approval");
if (input.state.startsWith("earlier_")) {
  const action = page.$("#highbay-build-start");
  expect(action && !action.disabled && action.getAttribute("aria-describedby").includes("highbay-build-start-explanation"), "earlier closed/passed build lost its direct explained Build offer");
  expect(page.text("#highbay-build-start-explanation").includes("nothing confines them"), "earlier Build offer lost execution explanation");
  if (input.state === "earlier_merged") {
    expect(action.textContent === "Build current agreed revision" && action.classList.contains("highbay-btn-primary"), "earlier merged build lost the primary current-agreement action");
    expect(!page.$("#highbay-build-again"), "earlier merged build offers replay instead of the new agreement");
    expect(taking.includes("It is in your branch. Highbay's verification covered a passed run's commits before you merged them, not your branch after the merge, which may also hold other changes merged after this build started. Build the current agreed revision to implement the new agreement. No role has work on this build."), "earlier merged completion lost its new-agreement guidance");
  }
  if (input.state === "earlier_configured") {
    expect(taking.includes("It is in your branch. Highbay's verification covered a passed run's commit before you merged it, not your branch after the merge, which may also hold other changes merged after this build started. Build the current agreed revision to implement the new agreement. No role has work on this build."), "earlier configured merged completion lost its no-tests guidance");
    expect(page.text("#highbay-build-start-explanation").includes("From Build on, those tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges."), "configured tests lost their execution wording");
  }
  expect(page.sent("POST", path).length === 0, "reading an earlier build started work");
  page.close();
  return;
}
if (["passed", "rewritten", "evaluator"].includes(input.state)) {
  expect(page.text("#highbay-build-state") === "The builder’s commit passed the accepted tests.", "passed build still describes future building or checking");
  expect(page.$("#highbay-build-state").nextElementSibling?.classList.contains("highbay-build-merge"), "merge instructions are not immediately below the passed state");
  expect(!taking.includes("builder finishes") && !taking.includes("after the check passes"), "passed build retains a stale next step");
  expect(taking.includes(candidate) && taking.includes("highbay/build-7"), "candidate evidence used a branch tip or lost its verified branch");
  if (input.state === "evaluator") expect(!taking.includes(tests), "stored evaluator invented a tests commit");
  else expect(taking.includes(tests) && taking.includes("highbay/tests-7"), "tests evidence lost the exact commit");
  expect(!taking.includes("git merge") && !page.$("#highbay-build-merge"), "an unapproved passing check offered a merge action");
  expect(taking.includes("Review and approval"), "passing build omitted the required approval step");
  expect(taking.includes("commit where this build started") === (input.state === "rewritten"), "rewrite warning does not follow base_in_branch");
} else {
  expect(!taking.includes("git merge"), "an unpassed or closed build offers merge commands");
  if (input.state === "building") expect(taking.includes("Next: the test writer prepares tests") && !taking.includes("nothing to merge yet"), "building lost its next step or repeats an unpassed summary");
  if (variants[input.state].state === "merged") expect(page.text("#highbay-build-state") === "Merged." && taking.includes("No role has work"), "merged state is unclear");
  if (input.state === "dropped") expect(taking.includes("highbay/tests-7") && !taking.includes("highbay/build-7") && taking.includes("git log highbay/tests-7"), "dropped state invented a retained branch");
  if (input.state === "empty") expect(taking.includes("Nothing was committed for it") && !taking.includes("highbay/"), "empty dropped state invented work");
}
const open = ["building", "passed", "rewritten", "evaluator"].includes(input.state);
expect(Boolean(page.$("#highbay-build-drop")) === open, "Drop availability does not follow taking");
for (const selector of ["#highbay-build-builder", "#highbay-build-test-writer", ...(input.state === "building" || input.state === "merged_configured" || closedFindings ? [] : ["#highbay-build-not-judged"])]) {
  expect(Boolean(page.$(selector)?.closest("#highbay-build-details")) === (input.state !== "building"), "role facts did not follow the active-versus-saved disclosure");
}
expect(page.$("#highbay-build-details").textContent.includes("No tests of this change’s Story were required."), "required-test facts are not in the saved disclosure");
expect(!page.$("#highbay-build-details").open, "secondary build facts expanded by default");
const buttons = page.$$(".highbay-build-actions button");
expect(buttons.length > 0 && (!open || buttons.at(-1).id === "highbay-build-drop"), "Drop is not last in the single action row");
expect(page.$$(".highbay-build-actions").length === 1, "build actions are split into separate rows");
const state = variants[input.state].state;
expect(page.text("#highbay-build-taking-state") === state[0].toUpperCase() + state.slice(1), "the header badge does not describe the saved taking state");
expect(Boolean(page.$(".highbay-build-header .highbay-build-actions")), "current build controls are not in the header");
expect(Boolean(page.$("#highbay-build-start-explanation")) === (state === "dropped"), "the execution explanation must accompany each direct Build offer");
if (state === "dropped") {
  expect(taking.includes("Highbay builds nothing on it, and it no longer blocks another build. No role has work on this build.") && !taking.includes("Another change can be built"), "dropped build lost its own facts or predicts that another build is accepted");
  const action = page.$("#highbay-build-start");
  expect(action && !action.disabled && action.getAttribute("aria-describedby").split(/\s+/).includes("highbay-build-start-explanation") && page.text("#highbay-build-start-explanation").includes("nothing confines them"), "direct Build lost its execution explanation");
}
if (state === "merged") {
  expect(!page.$("#highbay-build-start"), "current merged agreement still has a primary Build action");
  const expectedMerged = input.state === "merged_configured"
    ? "It is in your branch. Highbay's verification covered a passed run's commit before you merged it, not your branch after the merge, which may also hold other changes merged after this build started. Build another change or refine this one when there is more to do. No role has work on this build."
    : "It is in your branch. Highbay's verification covered a passed run's commits before you merged them, not your branch after the merge, which may also hold other changes merged after this build started. Build another change or refine this one when there is more to do. No role has work on this build.";
  expect(page.text("#highbay-build-taking").includes(expectedMerged), "merged taking lost its verification scope or next work guidance");
  async function askReplay() {
    await control(origin, "reply", { key: "GET " + path, status: 200,
      body: { change_id: "chg-taking", drafted_before_drop: false, build: { ...saved, state: "started", taking: variants[input.state] } } });
    page.$("#highbay-build-again").click();
  }
  const replay = page.$("#highbay-build-again");
  expect(replay && !replay.disabled && !replay.classList.contains("highbay-btn-primary"), "replay is missing or primary");
  await askReplay();
  await until(() => page.$("#highbay-build-confirm"), "merged replay did not ask deliberately");
  const question = page.text("#highbay-build-question");
  expect(question.includes("new build number") && question.includes("branch as it is now") && question.includes("same agreed revision") && question.includes("fresh unread") && question.includes("Earlier reports do not establish"), "replay lost its new build consequences");
  expect(!question.includes("replaces this build"), "replay describes replacement of an open build");
  expect(question.includes("configured tests") === (input.state === "merged_configured"), "replay test source wording is wrong");
  const execution = input.state === "merged_configured"
    ? "From Build on, those tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges."
    : "From Build on, the tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges.";
  expect(page.text("#highbay-build-confirm-explanation").includes(execution), "replay lost its full execution explanation for this test source");
  page.$("#highbay-build-cancel").click();
  await until(() => !page.$("#highbay-build-question") && page.doc.activeElement?.id === "highbay-build-again", "replay cancel lost focus");
  expect(page.sent("POST", path).length === 0, "opening or cancelling replay started work");
  await askReplay();
  await until(() => page.$("#highbay-build-confirm"), "second replay did not ask");
  await control(origin, "reply", { key: "GET " + path, status: 200,
    body: { change_id: "chg-taking", drafted_before_drop: true, build: { ...saved, state: "started", taking: variants[input.state] } } });
  await control(origin, "act", { key: "resave-taking" });
  await until(() => page.$("#highbay-build-enabling-step") && !page.$("#highbay-build-loading"), "owner admission change did not show recovery");
  expect(!page.$("#highbay-build-question") && !page.$("#highbay-build-again") && !page.$("#highbay-build-start"), "owner admission change retained consent or an offer");
  expect(page.text("#highbay-build-enabling-step").includes("proposal already waiting may predate"), "recovery promises that stale adoption is sufficient");
  await control(origin, "reply", { key: "GET " + path, status: 200,
    body: { change_id: "chg-taking", drafted_before_drop: false, build: { ...saved, state: "started", taking: variants[input.state] } } });
  await control(origin, "act", { key: "resave-taking" });
  await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "fresh owner admission did not restore replay");
  expect(!page.$("#highbay-build-question"), "old consent returned after admission recovered");
  await askReplay();
  await until(() => page.$("#highbay-build-confirm"), "fresh replay did not ask");
  page.$("#highbay-build-confirm").click();
  await until(() => page.sent("POST", path).length === 1, "confirmed replay did not submit");
  const request = page.sent("POST", path)[0].body;
  expect(request.request_revision === saved.request_revision && !Object.hasOwn(request, "replaces"), "merged replay sent replacement or proposal identity");
}
if (closedFindings) {
  expect(page.text("#highbay-build-findings").includes("1 finding") && page.$("#highbay-build-findings").closest("#highbay-build-details"), "closed build lost its historical finding count");
  expect(!page.$("#highbay-build-findings button") && page.$("#highbay-build-findings").getAttribute("aria-busy") === "false", "closed build exposes active findings controls or loading");
  expect(page.sent("GET", path + "/findings").length === 0, "closed historical facts read active findings");
}
if (input.state === "building") {
  // A changed answer to the pre-question read must not create consent for it.
  await control(origin, "hold", { key: "GET " + path });
  await control(origin, "reply", { key: "GET " + path, status: 200,
    body: { change_id: "chg-taking", drafted_before_drop: false, build: { ...saved, state: "started", taking: passed } } });
  page.$("#highbay-build-again").click();
  await until(() => page.$("#highbay-build-loading") && page.$("#highbay-build-drop").disabled, "pending rebuild read did not exclude Drop");
  await control(origin, "release", { key: "GET " + path });
  await until(() => page.$("#highbay-build-taking")?.dataset.state === "passed" && !page.$("#highbay-build-loading"), "changed read did not settle");
  expect(!page.$("#highbay-build-question") && page.sent("POST", path).length === 0, "changed read created consent or started work");
  // Restore the fixture, then change only taking while a question is visible.
  await control(origin, "reply", { key: "GET " + path, status: 200,
    body: { change_id: "chg-taking", drafted_before_drop: false, build: { ...saved, state: "started", taking: variants.building } } });
  await control(origin, "act", { key: "resave-taking" });
  await until(() => page.$("#highbay-build-taking")?.dataset.state === "building" && !page.$("#highbay-build-loading"), "building fixture did not return");
  await control(origin, "reply", { key: "GET " + path, status: 503,
    body: { name: "unavailable", id: "build-read", message: "Build owner unavailable.", temporary: true, timeout: false, fault: false } });
  page.$("#highbay-build-again").click();
  await until(() => page.$("#highbay-build-error"), "failed pre-question read did not show its error");
  expect(!page.$("#highbay-build-question") && page.sent("POST", path).length === 0, "failed read created consent or started work");
  page.$("#highbay-build-refresh").click();
  await until(() => !page.$("#highbay-build-error") && page.$("#highbay-build-drop") && !page.$("#highbay-build-drop").disabled, "retry did not release the other action");
  page.$("#highbay-build-again").click();
  await until(() => page.$("#highbay-build-confirm"), "rebuild question did not open");
  expect(page.$("#highbay-build-drop").disabled, "rebuild question did not exclude Drop");
  const before = page.sent("GET", path).length;
  await control(origin, "reply", { key: "GET " + path, status: 200,
    body: { change_id: "chg-taking", drafted_before_drop: false, build: { ...saved, state: "started", taking: passed } } });
  await control(origin, "act", { key: "resave-taking" });
  await until(() => page.sent("GET", path).length > before && page.$("#highbay-build-taking")?.dataset.state === "passed", "new taking state did not arrive");
  expect(!page.$("#highbay-build-question") && page.sent("POST", path).length === 0, "same-start state transition retained consent or started work");
  page.close();
  return;
}
if (open) {
  page.$("#highbay-build-drop").click();
  await until(() => page.$("#highbay-build-drop-cancel"), "Drop did not ask");
  expect(page.text("#highbay-build-drop-cancel") === (input.state === "building" ? "Keep building" : "Keep this build"), "Drop cancel assumes passed work is still building");
  page.$("#highbay-build-drop-cancel").click();
  await until(() => !page.$("#highbay-build-drop-question"), "Drop cancel did not close");
  expect(page.doc.activeElement === page.$("#highbay-build-drop"), "Drop cancel lost trigger focus");
}
if (input.state === "passed") {
  const replaceTaking = async (taking) => {
    const before = page.sent("GET", path).length;
    await control(origin, "reply", { key: "GET " + path, status: 200,
      body: { change_id: "chg-taking", drafted_before_drop: false, build: { ...saved, state: "started", taking } } });
    await control(origin, "act", { key: "resave-taking" });
    await until(() => page.sent("GET", path).length > before && page.sent("GET", path).at(-1).status === 200 &&
      page.$("#highbay-build-taking")?.dataset.state === taking.state &&
      !page.$("#highbay-build-loading"), "taking refresh did not settle");
    expect(!page.text("#highbay-build-taking").includes("git merge"), "refresh granted approval to unapproved work");
  };
  page.$("#highbay-nav-cp1").click();
  page.$("#highbay-refine-agreement").open = true;
  page.type(page.$('textarea[data-example-id="check_01"][data-field="scenario"]'), "keep editor focus when commands vanish");
  page.$("#highbay-nav-build").click();
  await until(() => page.$("#highbay-build-taking"), "taking did not return");
  const composer = page.$("#highbay-composer-input");
  page.type(composer, "composer note"); composer.focus();
  await replaceTaking(variants.evaluator);
  expect(page.doc.activeElement === composer && composer.value === "composer note", "removing only an unfocused tests command stole external focus");
  await replaceTaking(passed);
  await replaceTaking(variants.merged);
  expect(page.doc.activeElement === composer && composer.value === "composer note", "removing unfocused commands stole external focus");
  page.$("#highbay-nav-cp1").click();
  expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "keep editor focus when commands vanish", "taking refreshes discarded Agree draft");
  page.$("#highbay-nav-build").click();
}
expect(page.sent("POST", path).length === (state === "merged" ? 1 : 0) && page.sent("POST", path + "/drop").length === 0, "taking actions wrote beyond the explicit replay confirmation");
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
	started, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	encoded, err := genhighbay.EncodeStartedBuild(started.Build)
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	ps.act("drop-passed-open", func() {
		_, err := cp.DropBuild(user, &genhighbay.DropBuildPayload{
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
  candidate_commit: "1".repeat(40), candidate_branch: "highbay/build-7", tests: { source: "evaluator" }, base_in_branch: true } };
await control(origin, "reply", { key: "POST " + path, status: 409,
  body: { name: "previous_change_open", message: "Merge or drop the open change.", change_id: "chg-passed-open", build: saved } });
await control(origin, "reply", { key: "GET " + other, status: 200, body: { change_id: "chg-passed-open", drafted_before_drop: false, build: { ...saved, state: "started" } } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-open-change-chg-passed-next"), "change list did not load");
page.$("#highbay-open-change-chg-passed-next").click();
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "next change did not offer Build");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-blocking-change") && page.sent("GET", other).length > 0 && !page.$("#highbay-build-loading"), "blocker was not read by its exact change ID");
const message = page.text("#highbay-build-action-error");
expect(message.includes("chg-passed-open has an open build") && message.includes("commit passed the accepted tests") && !message.includes("Nothing of it has passed"), "passed blocker was described as unpassed");
expect(page.$("#highbay-build-start").disabled && page.text("#highbay-build-taking").includes("1".repeat(40)), "selected not_started observation cleared the passed blocker");
expect(!page.text("#highbay-checkpoint-status").includes("Build did not start"), "blocker was duplicated in global status");
page.$("#highbay-build-drop").click();
await until(() => page.$("#highbay-build-drop-question"), "old passed blocker did not ask for Drop consent");
const replacement = { ...saved, started_at: new Date(Date.parse(saved.started_at) + 1000).toISOString(), taking: { state: "building" } };
await control(origin, "reply", { key: "GET " + other, status: 200,
  body: { change_id: "chg-passed-open", drafted_before_drop: false, build: { ...replacement, state: "started" } } });
await control(origin, "act", { key: "resave-next" });
await until(() => !page.$("#highbay-build-drop-question") && page.$("#highbay-build-taking")?.dataset.state === "building", "a freshly observed different open start did not invalidate old consent");
expect(page.$("#highbay-build-start").disabled && !page.text("#highbay-build-taking").includes("git merge"), "fresh replacement retained old merge commits or unblocked the selected Build");
expect(page.sent("POST", other + "/drop").length === 0, "fresh replacement reused old Drop consent");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="scenario"]'), "keep this draft through blocker recovery");
page.$("#highbay-nav-build").click();
await control(origin, "reply", { key: "GET " + other, status: 200,
  body: { change_id: "chg-passed-open", drafted_before_drop: false, build: { ...saved, state: "started", taking: { state: "dropped", kept_branches: [] } } } });
await control(origin, "act", { key: "drop-passed-open" });
await until(() => !page.$("#highbay-build-blocking-change") && page.$("#highbay-build-enabling-step") && !page.$("#highbay-build-start"), "fresh closed blocker did not expose the selected agreement’s post-drop recovery");
expect(page.text("#highbay-build-enabling-step").includes("This agreement was drafted before a build was dropped") && !page.text("#highbay-build-enabling-step").includes("this change again"), "never-built agreement recovery assumes an earlier build of this change");
expect(page.sent("POST", path).length === 1, "a fresh read retried Build without consent");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "keep this draft through blocker recovery", "blocker recovery replaced unsaved editor text");
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
	_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
		ChangeID: open.ChangeID, RequestRevision: open.RequestRevision,
	})
	require.NoError(t, err)
	encoded, err := genhighbay.EncodeChangeCardResult(next)
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
await until(() => page.$("#highbay-open-change-chg-nav-next"), "change list did not load");
page.$("#highbay-open-change-chg-nav-next").click();
await until(() => page.$("#highbay-proposal"), "proposed change did not open");
page.$('#highbay-proposal [data-question-id="q-review"] [data-option="keep"]').click();
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="scenario"]'), "keep my local scenario");
page.$("#highbay-nav-build").click();
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "Build start on proposed change did not load");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "earlier agreement did not ask for Build consent");
expect(page.text("#highbay-build-question").includes("Build the earlier agreed revision") &&
       page.text("#highbay-build-question").includes(card.request_revision.slice(0, 12)) &&
       page.sent("POST", "/changes/chg-nav-next/build").length === 0, "the prompt failed to name its earlier agreement or started Build before consent");
page.$("#highbay-build-cancel").click();
await until(() => !page.$("#highbay-build-confirm") && page.doc.activeElement === page.$("#highbay-build-start"), "Cancel lost focus or left consent open");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "asking again did not recover Build confirmation");
page.$("#highbay-build-confirm").click();
await until(() => page.$("#highbay-build-open-blocking-change"), "other-change recovery action did not appear");
const [start] = page.sent("POST", "/changes/chg-nav-next/build");
expect(start.body.request_revision === card.request_revision && !("replaces" in start.body), "first Build submitted a different revision or replaced a start");
page.$("#highbay-build-open-blocking-change").click();
await until(() => page.$("#highbay-checkpoint-view").dataset.changeId === "chg-nav-open", "open-blocking-change action did not show its subject");
page.$("#highbay-nav-cp1").click();
expect(!page.$("#highbay-preserved-draft") && !page.text("#highbay-cp1-editor").includes("keep my local scenario"), "opening chg-nav-open leaked chg-nav-next's draft");
await control(origin, "reply", { key: "GET /changes/chg-nav-next", status: 200, body: card });
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-nav-next").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-nav-next", "returning to chg-nav-next did not open Agree");
expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "keep my local scenario" && page.$('#highbay-proposal [data-question-id="q-review"] [data-option="keep"]').getAttribute("aria-pressed") === "true", "opening the blocker lost chg-nav-next's live draft text or proposal choices");
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
	started, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"started": started.Build.StartedAt}, `
const path = "/changes/chg-drop/build/drop";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-drop") && !page.$("#highbay-build-drop").disabled, "Drop did not become ready");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="scenario"]'), "keep my unsaved scenario");
page.$("#highbay-nav-build").click();
await until(() => page.$("#highbay-build-drop") && !page.$("#highbay-build-drop").disabled, "Drop did not return");
page.$("#highbay-build-drop").click();
await until(() => page.$("#highbay-build-drop-confirm"), "Drop did not ask separately");
expect(page.text("#highbay-build-drop-question").includes("edits it has not proposed are lost") && page.text("#highbay-build-drop-question").includes("Your branch keeps its current commits"), "Drop omitted its effects");
expect(page.sent("POST", path).length === 0, "opening Drop changed a build");
page.$("#highbay-build-drop-confirm").dispatchEvent(new page.win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
await until(() => !page.$("#highbay-build-drop-question") && page.doc.activeElement === page.$("#highbay-build-drop"), "Escape did not restore the Drop trigger");
page.$("#highbay-build-drop").click();
await until(() => page.$("#highbay-build-drop-cancel"), "Drop did not reopen");
expect(page.$("#highbay-build-again").disabled, "Drop question did not exclude rebuild");
page.$("#highbay-build-drop-cancel").click();
await until(() => !page.$("#highbay-build-drop-question") && !page.$("#highbay-build-drop").disabled, "Cancel did not restore Drop");
expect(page.sent("POST", path).length === 0, "cancelling Drop sent a write");
await control(origin, "hold", { key: "POST " + path });
page.$("#highbay-build-drop").click();
await until(() => page.$("#highbay-build-drop-confirm"), "final Drop did not ask");
const confirm = page.$("#highbay-build-drop-confirm");
confirm.click(); confirm.click();
await until(() => page.sent("POST", path).length === 1, "Drop did not send exactly one write");
const body = page.sent("POST", path)[0].body;
expect(body.started_at === input.started && Object.keys(body).length === 1, "Drop did not send the exact saved start; change_id belongs in its generated route");
await control(origin, "release", { key: "POST " + path });
await until(() => page.$("#highbay-build-taking")?.dataset.state === "dropped" && !page.$("#highbay-build-loading"), "owner did not show the durable dropped state");
const dropped = JSON.parse(page.sent("POST", path)[0].answer);
expect(dropped.drafted_before_drop === true && dropped.build.in_force === true && dropped.build.taking.state === "dropped", "real Drop did not report a stale agreed build");
expect(page.$("#highbay-build-enabling-step") && !page.$("#highbay-build-start") && !page.$("#highbay-build-again"), "dropped agreed revision with stale draft still offers Build instead of its enabling step");
expect(page.text("#highbay-build-enabling-step").includes("This agreement was drafted before a build was dropped"), "own-drop recovery does not explain the agreed draft");
expect(page.text("#highbay-build-taking").includes("Highbay builds nothing on it, and it no longer blocks another build.") && !page.text("#highbay-build-section").includes("Another change can be built"), "a dropped build whose agreement the server would refuse still promises another build");
expect(page.$("#highbay-build-taking").nextElementSibling === page.$("#highbay-build-enabling-step"), "the enabling step does not directly follow the dropped build's facts");
expect(!page.$("#highbay-build-drop") && !page.$("#highbay-build-drop-question"), "closed build still offers Drop");
expect(page.sent("POST", path).length === 1, "Drop submitted twice");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "keep my unsaved scenario", "Drop lost unsaved editor state");
page.close();
const again = await openPage(origin, { link: input.link + "-2" });
await until(() => again.$("#highbay-build-taking")?.dataset.state === "dropped" && !again.$("#highbay-build-loading"), "a new page did not read the saved drop");
expect(again.$("#highbay-build-enabling-step") && !again.$("#highbay-build-start") && !again.$("#highbay-build-again"), "a new page lost the saved dropped agreement’s enabling step");
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
	started, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
		ChangeID: open.ChangeID, RequestRevision: open.RequestRevision,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	ps.act("replace-open", func() {
		_, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
			ChangeID: open.ChangeID, RequestRevision: open.RequestRevision,
			Replaces: &genhighbay.ReplacedBuild{ChangeID: open.ChangeID, StartedAt: started.Build.StartedAt},
		})
		require.NoError(t, err)
	})
	runPageScript(t, ps, map[string]any{"started": started.Build.StartedAt}, `
const nextPath = "/changes/chg-next/build", dropPath = "/changes/chg-open/build/drop";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-open-change-chg-next"), "change list did not open");
page.$("#highbay-open-change-chg-next").click();
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "next change did not offer Build");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-blocking-change") && page.$("#highbay-build-drop") && !page.$("#highbay-build-drop").disabled, "typed previous_change_open did not offer the other change's Drop");
const refusalText = "Build did not start: change chg-open has an open build. Nothing of it has passed its check yet: let its builder finish, or drop it.";
expect(page.text("#highbay-build-action-error") === refusalText, "the refusal lost the other build's state and next step");
expect(!page.text("#highbay-checkpoint-status").includes("Build did not start") && page.text("#highbay-build-blocking-change").split(refusalText).length === 2, "the same refusal is repeated outside its card alert");
const describedBy = page.$("#highbay-build-start").getAttribute("aria-describedby").split(/\s+/);
expect(page.$("#highbay-build-start").disabled && describedBy.includes("highbay-build-action-error") && describedBy.includes("highbay-build-start-explanation"), "the blocked Build action lacks its disabled reason or execution explanation");
expect(page.text("#highbay-build-drop") === "Drop chg-open’s build…" && page.text("#highbay-build-open-blocking-change") === "Open change chg-open", "other-change recovery does not identify its subject");
await until(() => page.sent("GET", "/changes/chg-open/build").some((reply) => reply.status === 200), "initial exact-other observation did not settle");
await control(origin, "hold", { key: "GET /changes/chg-open/build" });
page.$("#highbay-build-drop").click();
await until(() => page.$("#highbay-build-drop-confirm"), "other build's Drop did not ask");
await control(origin, "act", { key: "replace-open" });
await until(() => page.$("#highbay-build-drop-confirm") && !page.$("#highbay-build-drop-confirm").disabled, "unrelated selected-card refresh destroyed consent instead of preserving its exact old identity");
page.$("#highbay-build-drop-confirm").click();
await until(() => page.sent("POST", dropPath)[0]?.status === 409 && page.text("#highbay-build-action-error").includes("open build changed"), "stale Drop did not show the typed replacement");
const refusal = JSON.parse(page.sent("POST", dropPath)[0].answer);
expect(refusal.name === "build_changed" && refusal.build.started_at !== input.started, "fixture did not replace the open build");
expect(page.sent("POST", dropPath)[0].body.started_at === input.started, "Drop silently changed the consent identity");
expect(!page.$("#highbay-build-drop-question") && page.sent("POST", dropPath).length === 1, "stale Drop retried or kept its stale question");
await control(origin, "release", { key: "GET /changes/chg-open/build" });
await until(() => page.$("#highbay-build-drop") && !page.$("#highbay-build-drop").disabled && !page.$("#highbay-build-loading"), "replacement facts did not recover Drop");
page.$("#highbay-build-drop").click();
await until(() => page.$("#highbay-build-drop-confirm"), "replacement build did not require fresh Drop consent");
page.$("#highbay-build-drop-confirm").click();
await until(() => page.sent("POST", dropPath)[1]?.status === 200 && !page.$("#highbay-build-blocking-change"), "fresh consent did not drop the replacement");
expect(page.sent("POST", dropPath)[1].body.started_at === refusal.build.started_at, "fresh Drop did not name the typed replacement start");
await until(() => page.$("#highbay-build-enabling-step") && !page.$("#highbay-build-start"), "selected stale agreement did not show its enabling step");
const step = page.text("#highbay-build-enabling-step");
expect(step.includes("refresh_story") && step.includes("revise_change") && step.includes("adopt that revision"), "owner admission fact lost refresh/revise/adopt recovery");
expect(page.sent("POST", nextPath).length === 1, "post-drop recovery retried an inadmissible Build");
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
	started, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"started": started.Build.StartedAt}, `
const path = "/changes/chg-unfinished/build";
await control(origin, "reply", { key: "GET " + path, status: 200,
  body: { change_id: "chg-unfinished", drafted_before_drop: false, build: { state: "not_started" } } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "stale absent observation did not offer first Build");
page.$("#highbay-build-start").click();
await until(() => page.sent("POST", path)[0]?.status === 409 && page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "unfinished_build_open did not recover the saved build");
expect(JSON.parse(page.sent("POST", path)[0].answer).name === "unfinished_build_open", "the fixture did not exercise the typed unfinished refusal");
expect(!page.$("#highbay-build-confirm") && !("replaces" in page.sent("POST", path)[0].body), "refusal invented replacement consent");
const reads = page.sent("GET", path).length;
await control(origin, "hold", { key: "GET " + path });
page.$("#highbay-build-again").click();
await until(() => page.sent("GET", path).length > reads && page.$("#highbay-build-loading"), "rebuild did not read its owner before asking");
expect(!page.$("#highbay-build-confirm") && page.$("#highbay-build-drop").disabled && page.sent("POST", path).length === 1, "rebuild read did not exclude Drop or wrote before the fresh owner answer");
await control(origin, "release", { key: "GET " + path });
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "fresh owner answer did not ask for explicit consent");
page.$("#highbay-build-confirm").click();
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
	_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
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
await until(() => page.$("#highbay-open-change-chg-focus-a"), "change list did not load");
page.$("#highbay-open-change-chg-focus-a").click();
await until(() => page.$("#highbay-build-drop") && !page.$("#highbay-build-drop").disabled && !page.$("#highbay-build-loading"), "first build did not open");
await control(origin, "hold", { key: "GET " + path });
await control(origin, "hold", { key: "POST " + drop });
page.$("#highbay-build-drop").click();
await until(() => page.$("#highbay-build-drop-confirm"), "Drop did not ask");
page.$("#highbay-build-drop-confirm").click();
await until(() => page.sent("POST", drop).length === 1 && page.doc.activeElement === page.$("#highbay-build-title"), "pending Drop did not focus the Build heading");
await control(origin, "act", { key: "other-build" });
await control(origin, "release", { key: "POST " + drop });
await until(() => page.sent("POST", drop)[0].status === 409 && page.$("#highbay-build-blocking-change"), "stale Drop did not name the other build");
expect(page.doc.activeElement === page.$("#highbay-build-title"), "removing the focused taking panel lost focus instead of using the stable Build heading");
await control(origin, "release", { key: "GET " + path });
await until(() => !page.$("#highbay-build-loading"), "selected build recovery did not settle");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="scenario"]'), "keep focused unsaved scenario");
page.$("#highbay-nav-build").click();
const composer = page.$("#highbay-composer-input");
page.type(composer, "keep focused composer"); composer.focus();
await control(origin, "act", { key: "restore-build" });
await until(() => !page.$("#highbay-build-blocking-change") && page.$("#highbay-build-taking")?.dataset.state === "building", "fresh owner build did not remove the other panel");
expect(page.doc.activeElement === composer && composer.value === "keep focused composer", "removing an unfocused taking panel stole external focus");
expect(page.sent("POST", drop).length === 1, "focus recovery retried Drop");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "keep focused unsaved scenario", "removing taking panel replaced Agree editor state");
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
	_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
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
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "rebuild was not ready");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="scenario"]'), "focus stays in my unsaved text");
page.$("#highbay-nav-build").click();
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "rebuild did not return");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "rebuild did not ask");
const composer = page.$("#highbay-composer-input");
page.type(composer, "focus stays in my composer"); composer.focus();
await control(origin, "act", { key: "needs-attention" });
await until(() => page.$("#highbay-team-attention-builder") && page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "background Team update did not settle with the question open");
expect(page.doc.activeElement === composer && composer.value === "focus stays in my composer", "background refresh pulled focus out of the text input into an unchanged question");
const confirm = page.$("#highbay-build-confirm"); confirm.focus();
await control(origin, "act", { key: "working-again" });
await until(() => !page.$("#highbay-team-attention-builder") && page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "second Team update did not settle");
expect(page.doc.activeElement === confirm && confirm === page.$("#highbay-build-confirm"), "background refresh pulled focus from the confirmation control into its group");
expect(page.sent("POST", "/changes/chg-question-focus/build").length === 0, "focus preservation submitted a rebuild");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "focus stays in my unsaved text", "background refresh discarded Agree draft");
page.close();
`)
}
