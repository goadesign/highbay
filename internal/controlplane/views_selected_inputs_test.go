// These served-page tests keep saved verification evidence separate from the
// owner's changing observation of accepted tests. Runs and checker results are
// saved fixtures; neither candidate code nor a model executes.
package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/designeval"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/goadesign/highbay/internal/verifyrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// selectedInputsListRead captures a real owner answer before the HTTP harness
// delays its delivery. Other operations continue to reach the real owner.
type selectedInputsListRead struct {
	*ControlPlane
	// armed selects one list answer after its delivery gate is installed.
	armed atomic.Bool
	// captured proves the old positive observation preceded new acceptance.
	captured chan *genhighbay.VerificationListResult
}

// TestViewsPageAcceptedTestsRefreshOpenedBuildAndSelectedProof follows a second
// accepted test commit without changing views or reloading the page. The
// mismatch names both reports without changing the saved report's binding.
func TestViewsPageAcceptedTestsRefreshOpenedBuildAndSelectedProof(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	runID := strings.Repeat("1", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	editVerificationRequest(t, runsDir, runID, func(req *verifyrun.Request) {
		req.Plan.Tests = verify.TestsWritten
		req.StoryContext = verifyrun.StoryContext{
			State: verifyrun.StoryRecorded, BuildNumber: 1,
			BuildStartedAt: req.AcceptedAt.Add(-time.Minute), Stories: []designeval.StorySummary{},
			TestCoverage: &verifyrun.TestCoverageSnapshot{
				Report: 1, Answers: []*genhighbay.ConfirmedAnswer{}, Examples: card.Examples,
				CoverageStatements: []*genhighbay.TestCoverageStatement{},
			},
		}
	})
	ps := startPageServer(t, cp)
	selectedInputsActions(t, ps, cp, card.ChangeID)
	user := asCaller(pageOwner, CallerUser)
	build := mustStartedBuild(t, cp, user, card.ChangeID)
	ps.act("drop-current-build", func() {
		_, err := cp.DropBuild(user, &genhighbay.DropBuildPayload{ChangeID: card.ChangeID, StartedAt: build.StartedAt})
		require.NoError(t, err)
	})
	runPageScript(t, ps, map[string]any{"run": runID}, `
const page = await openPage(origin, { link: input.link });
const review = () => page.$("#highbay-build-review-check");
const currentBuild = () => review()?.classList.contains("shadcn-btn-primary") && !review().disabled;
await until(currentBuild, "the opened Build did not offer its current passing check");
expect(review().textContent === "Review result", "the current check has no review action");
expect(page.$$("#highbay-build-section .shadcn-btn-primary").length === 1, "Build has competing primary actions");
const beforeReplacement = page.requests.length;
await control(origin, "act", { key: "accept-t2" });
await until(() => page.requests.slice(beforeReplacement).some((r) => r.path === "/verifications/" + input.run && r.status === 200 && JSON.parse(r.answer).inputs_current === false), "the opened Build did not read the second acceptance's negative match");
await until(() => review() && !review().disabled && !currentBuild() && review().textContent === "Review saved check", "a second accepted commit left the opened Build claiming current proof");
expect(page.state() === "build" && !page.$("#highbay-cp4-verification"), "a background acceptance navigated away from Build");
await control(origin, "act", { key: "accept-t1" });
await until(currentBuild, "reaccepting the recorded tests did not restore Build's decision");
review().click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.run && page.$("#highbay-cp5-approve") && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "Review result did not open the exact owner-supplied saved run");
expect(page.doc.activeElement === page.$("#highbay-cp5-title"), "Review result did not focus the approval destination");
const verdict = page.text("#highbay-cp4-title");
const details = page.text("#highbay-cp4-details");
expect(verdict === "The check passed: 2 of 2 scenarios.", "the saved passing verdict is missing");
const binding = () => page.text("#highbay-cp4-test-binding");
const difference = () => page.text("#highbay-cp4-input-difference");
expect(/\breport 1\b/i.test(binding()) && !difference(), "the original run lost its recorded report or claimed a mismatch");
await control(origin, "act", { key: "accept-t2" });
const testsChanged = () => {
  const [used, current] = difference().toLowerCase().split("current accepted tests");
  return page.text("#highbay-cp4-inputs").includes("does not match") &&
    /\breport 1\b/.test(used) && used.includes("2222222") &&
    /\breport 2\b/.test(current ?? "") && current.includes("3333333");
};
await until(() => testsChanged() && (page.$("#highbay-cp5-approve")?.disabled ?? true), "the selected run did not name its recorded tests and the current accepted tests");
expect(/\breport 1\b/i.test(binding()) && !/\breport 2\b/i.test(binding()), "the new acceptance replaced the selected run's report number");
expect(page.$("#highbay-result-view").dataset.runId === input.run && page.text("#highbay-cp4-title") === verdict && page.text("#highbay-cp4-details") === details, "freshness changed the saved report or selection");
page.$("#highbay-cp5-approve")?.click();
expect(page.sent("POST", "/changes/approve").length === 0, "a stale selected report submitted approval");
const listPath = "/changes/chg-selected-inputs/verifications";
await control(origin, "answer-later", { key: "GET " + listPath });
page.$("#highbay-load-verifications").click();
await until(() => page.$("#highbay-load-verifications").disabled && page.text("#highbay-cp4-inputs").includes("not known"), "a pending input read did not make currentness unknown");
expect(!difference() && /\breport 1\b/i.test(binding()), "pending currentness kept an exact mismatch or hid the frozen report");
await control(origin, "release", { key: "GET " + listPath });
await until(testsChanged, "the exact mismatch did not return after the owner confirmed it");
await control(origin, "act", { key: "accept-t1" });
await until(() => page.text("#highbay-cp4-inputs").includes("matches the agreed revision") && !difference() && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "the selected report did not regain its current owner match");
expect(page.text("#highbay-cp4-title") === verdict && page.text("#highbay-cp4-details") === details, "restoring freshness replaced historical evidence");
await control(origin, "act", { key: "accept-t2" });
await until(testsChanged, "the second acceptance did not restore the tests mismatch");
await control(origin, "act", { key: "drop-current-build" });
await until(() => page.text("#highbay-cp4-inputs").includes("does not match") && !difference(), "the dropped build was still described as the current accepted tests");
expect(/\breport 1\b/i.test(binding()) && page.text("#highbay-cp4-title") === verdict, "dropping the build hid the saved evidence");
expect(!page.requests.some((r) => r.method === "POST" && /^\/changes\/[^/]+\/verifications$/.test(r.path)), "reading saved proof started verification");
page.close();
`)
}

// TestViewsPageRefreshesAnOlderSelectionWhenLatestStaysStale prevents the
// newest run's unchanged false match from masking changes to a selected older
// run. The list remains owner-ordered; selection stays on the older identity.
func TestViewsPageRefreshesAnOlderSelectionWhenLatestStaysStale(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	olderID, latestID := strings.Repeat("1", 32), strings.Repeat("2", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, olderID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, olderID, verify.TestsWritten)
	writeSelectedInputsRun(t, runsDir, card, latestID, strings.Repeat("3", 40), candidate)
	ps := startPageServer(t, cp)
	selectedInputsActions(t, ps, cp, card.ChangeID)
	ps.act("accept-t3", func() {
		saveListedTestState(t, cp, card.ChangeID, testsAccepted, strings.Repeat("4", 40))
	})
	runPageScript(t, ps, map[string]any{"older": olderID, "latest": latestID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check") && !page.$("#highbay-build-review-check").disabled, "the latest saved check did not reach Build");
expect(!page.$("#highbay-build-review-check").classList.contains("shadcn-btn-primary"), "the latest stale run was presented as an approval decision");
page.$("#highbay-build-review-check").click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.latest && page.$("#highbay-cp4-run-" + input.older) && page.text("#highbay-cp4-inputs").includes("does not match"), "Build did not open the exact latest identity with its stale observation");
expect(page.doc.activeElement === page.$("#highbay-cp5-title"), "reviewing a stale passed check did not focus its blocked approval");
const row = (id) => page.text("#highbay-cp4-run-" + id).replaceAll("’", "'");
expect(row(input.older).includes("Tests' commit") && row(input.older).includes("2222222") &&
  row(input.latest).includes("Tests' commit") && row(input.latest).includes("3333333"), "run rows did not identify their own tests commits");
expect(!/\breport \d+\b/i.test(page.text("#highbay-cp4-test-binding")), "a historical run without coverage inherited the build's current report number");
const listPath = "/changes/chg-selected-inputs/verifications";
await control(origin, "answer-later", { key: "GET " + listPath });
const listsBeforeSelection = page.sent("GET", listPath).length;
page.$("#highbay-load-verifications").click();
await until(() => page.sent("GET", listPath).length > listsBeforeSelection && page.$("#highbay-load-verifications").disabled, "the selection-preservation list did not remain pending");
page.$("#highbay-cp4-run-" + input.older).click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.older, "the older run could not be selected during the pending list");
await control(origin, "release", { key: "GET " + listPath });
await until(() => page.$("#highbay-result-view").dataset.runId === input.older && page.$("#highbay-cp5-approve") && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "the older matching run inherited the latest run's false observation");
const oldDetails = page.text("#highbay-cp4-details");
const latestPath = "/verifications/" + input.latest;
const olderPath = "/verifications/" + input.older + "/review";
const before = page.requests.length;
await control(origin, "act", { key: "accept-t3" });
await until(() => page.$("#highbay-result-view").dataset.runId === input.older && page.text("#highbay-cp4-inputs").includes("does not match") && (page.$("#highbay-cp5-approve")?.disabled ?? true), "changing T1 to T3 did not invalidate the independently selected older run");
await until(() => page.requests.slice(before).some((r) => r.path === latestPath && r.status === 200 && JSON.parse(r.answer).inputs_current === false) && page.requests.slice(before).some((r) => r.path === olderPath && r.status === 200 && JSON.parse(r.answer).inputs_current === false), "the owner did not refresh both exact run identities");
expect(page.text("#highbay-cp4-details") === oldDetails && page.text("#highbay-cp4-title") === "The check passed: 2 of 2 scenarios.", "invalidating the older selection replaced its report");
await control(origin, "act", { key: "accept-t1" });
await until(() => page.$("#highbay-result-view").dataset.runId === input.older && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "the older run could not recover independently of the still-stale latest run");
page.$("#highbay-cp4-run-" + input.latest).click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.latest && page.text("#highbay-cp4-inputs").includes("does not match") && (page.$("#highbay-cp5-approve")?.disabled ?? true), "the latest run acquired the older run's positive match");
expect(page.text("#highbay-cp4-details").includes("3333333333333333333333333333333333333333"), "the latest run lost its distinct saved tests commit");
expect(page.sent("POST", "/changes/approve").length === 0, "selecting historical evidence submitted approval");
page.close();
`)
}

// TestViewsPageAnOlderPositiveListCannotRestoreApproval captures a positive
// owner list, accepts different tests, and delivers that answer only after a
// negative current Get. The completed report remains visible throughout.
func TestViewsPageAnOlderPositiveListCannotRestoreApproval(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	runID := strings.Repeat("1", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, runID, verify.TestsWritten)
	service := &selectedInputsListRead{ControlPlane: cp, captured: make(chan *genhighbay.VerificationListResult, 1)}
	ps := startPageServer(t, service)
	selectedInputsActions(t, ps, cp, card.ChangeID)
	ps.act("hold-old-list", func() {
		ps.answerLater("GET /changes/"+card.ChangeID+"/verifications", func() { service.armed.Store(true) })
	})
	ps.act("await-old-list", func() {
		select {
		case captured := <-service.captured:
			if assert.Len(t, captured.Runs, 1) {
				assert.Equal(t, runID, captured.Runs[0].RunID)
				assert.True(t, captured.Runs[0].InputsCurrent, "capture must precede the second accepted commit")
			}
		case <-time.After(10 * time.Second):
			t.Error("the owner did not capture the held list answer")
		}
	})
	runPageScript(t, ps, map[string]any{"run": runID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check")?.classList.contains("shadcn-btn-primary") && !page.$("#highbay-build-review-check").disabled, "the current saved run did not load");
page.$("#highbay-build-review-check").click();
await until(() => page.$("#highbay-cp5-approve") && !(page.$("#highbay-cp5-approve")?.disabled ?? true) && !page.$("#highbay-load-verifications").disabled, "initial proof reads did not settle");
const verdict = page.text("#highbay-cp4-title");
const listPath = "/changes/chg-selected-inputs/verifications";
const runPath = "/verifications/" + input.run + "/review";
await control(origin, "act", { key: "hold-old-list" });
const previousLists = page.sent("GET", listPath).length;
page.$("#highbay-load-verifications").click();
await until(() => page.sent("GET", listPath).length > previousLists && page.$("#highbay-load-verifications").disabled && (page.$("#highbay-cp5-approve")?.disabled ?? true), "the held list did not make freshness unknown");
const old = page.sent("GET", listPath)[previousLists];
await control(origin, "act", { key: "await-old-list" });
expect(old.status === undefined, "the positive owner answer escaped its delivery gate");
const before = page.requests.length;
await control(origin, "act", { key: "accept-t2" });
await until(() => page.requests.slice(before).some((r) => r.path === runPath && r.status === 200 && JSON.parse(r.answer).inputs_current === false), "the second acceptance did not produce a newer negative owner answer");
expect(old.status === undefined && (page.$("#highbay-cp5-approve")?.disabled ?? true), "approval became available before the old answer was released");
const regressions = [];
const observe = () => {
  if (page.$("#highbay-cp5-approve") && !(page.$("#highbay-cp5-approve")?.disabled ?? true)) regressions.push("approval enabled");
  if (page.text("#highbay-cp4-title") !== verdict) regressions.push("saved verdict changed");
};
const observer = new page.win.MutationObserver(observe);
observer.observe(page.$("#highbay-result-view"), { childList: true, attributes: true, subtree: true });
await control(origin, "release", { key: "GET " + listPath });
await until(() => old.status === 200 && !page.$("#highbay-load-verifications").disabled && page.text("#highbay-cp4-inputs").includes("does not match"), "the delayed list did not settle behind the newer observation");
expect(JSON.parse(old.answer).runs[0].inputs_current === true, "the held response did not actually carry the older positive match");
await sleep(0);
observe();
observer.disconnect();
expect(regressions.length === 0, "a delayed positive list restored stale approval: " + JSON.stringify(regressions));
page.$("#highbay-cp5-approve")?.click();
expect(page.sent("POST", "/changes/approve").length === 0, "the delayed positive answer enabled submission");
expect(page.$("#highbay-result-view").dataset.runId === input.run, "the delayed list changed the selected identity");
page.close();
`)
}

// TestViewsPagePendingAndFailedFreshnessPreservesReportAndDraft exercises
// pending, failed and retried list reads on an already passed result. Only
// freshness changes; the saved verdict and unsaved agreement input survive.
func TestViewsPagePendingAndFailedFreshnessPreservesReportAndDraft(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	runID := strings.Repeat("1", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, runID, verify.TestsWritten)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"run": runID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check")?.classList.contains("shadcn-btn-primary") && !page.$("#highbay-build-review-check").disabled, "the saved passing check did not load");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
const draft = page.$('textarea[data-field="expected_outcome"]');
page.type(draft, "Keep this unsaved agreement outcome");
page.$("#highbay-nav-build").click();
page.$("#highbay-build-review-check").click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.run && page.$("#highbay-cp5-approve") && !(page.$("#highbay-cp5-approve")?.disabled ?? true) && !page.$("#highbay-load-verifications").disabled, "the selected passing proof did not settle");
const verdict = page.text("#highbay-cp4-title");
const details = page.text("#highbay-cp4-details");
const preserved = () => page.text("#highbay-cp4-title") === verdict && page.text("#highbay-cp4-details") === details && page.$("#highbay-cp5-approval-dirty-warning") !== null;
const path = "/changes/chg-selected-inputs/verifications";
await control(origin, "reply", { key: "GET " + path, status: 503, body: { name: "internal", message: "The saved run could not be read." } });
await control(origin, "hold", { key: "GET " + path });
const before = page.sent("GET", path).length;
page.$("#highbay-load-verifications").click();
await until(() => page.sent("GET", path).length > before && page.$("#highbay-load-verifications").disabled && (page.$("#highbay-cp5-approve")?.disabled ?? true), "pending freshness retained enabled approval");
expect(page.text("#highbay-cp4-inputs").includes("not known") && preserved(), "a pending freshness read lost saved evidence or the unsaved draft");
page.$("#highbay-cp5-approve")?.click();
expect(page.sent("POST", "/changes/approve").length === 0, "pending freshness submitted approval");
await control(origin, "release", { key: "GET " + path });
await until(() => page.$("#highbay-cp4-list-error") && !page.$("#highbay-load-verifications").disabled, "the failed freshness read offered no recovery");
expect((page.$("#highbay-cp5-approve")?.disabled ?? true) && page.text("#highbay-cp4-inputs").includes("not known") && preserved(), "a failed read restored approval or discarded evidence/draft");
page.$("#highbay-load-verifications").click();
await until(() => !page.$("#highbay-cp4-list-error") && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "a successful retry did not restore current-input approval");
expect(preserved(), "retry changed the saved verdict or unsaved draft");

// A delayed nonterminal snapshot may carry a newer freshness observation,
// but it cannot replace a terminal report already received for this run.
const snapshot = JSON.parse(page.sent("GET", path).findLast((r) => r.status === 200).answer);
snapshot.runs[0].state = { type: "running", value: { started_at: snapshot.runs[0].accepted_at } };
await control(origin, "reply", { key: "GET " + path, status: 200, body: snapshot });
const previous = page.sent("GET", path).length;
page.$("#highbay-load-verifications").click();
await until(() => page.sent("GET", path).length > previous && page.sent("GET", path).at(-1).status === 200 && !page.$("#highbay-load-verifications").disabled, "the delayed running snapshot did not settle");
await until(() => page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "the owner review did not settle after the delayed running snapshot");
expect(preserved(), "a running snapshot regressed a terminal saved report");
expect(page.sent("POST", "/changes/approve").length === 0 && page.sent("POST", "/changes/confirm").length === 0, "freshness recovery submitted unsaved work");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-field="expected_outcome"]').value === "Keep this unsaved agreement outcome", "returning to Agree lost the unsaved draft");
page.close();
`)
}

// TestViewsPageWaitOutageDisablesRenderedApprovalUntilSameVersionRecovers
// exercises recovery without a changed owner version or another navigation.
// Retained evidence cannot keep an already rendered approval control usable
// while the update channel cannot establish that the page is current.
func TestViewsPageWaitOutageDisablesRenderedApprovalUntilSameVersionRecovers(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	runID := strings.Repeat("1", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, runID, verify.TestsWritten)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"run": runID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check")?.classList.contains("shadcn-btn-primary") && !page.$("#highbay-build-review-check").disabled, "the saved passing check did not load");
page.$("#highbay-build-review-check").click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.run && page.$("#highbay-cp5-approve") && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "the initial approval did not settle");
const approve = page.$("#highbay-cp5-approve");
const verdict = page.text("#highbay-cp4-title");
const details = page.text("#highbay-cp4-details");
const lists = page.sent("GET", "/changes").length;
const version = JSON.parse(page.sent("GET", "/changes").findLast((r) => r.status === 200).answer).version;
const waits = page.sent("GET", "/change-updates").length;
await control(origin, "reply", { key: "GET /change-updates", status: 503, body: { name: "internal", message: "The update channel is temporarily unavailable." } });
await until(() => page.sent("GET", "/change-updates").some((r) => r.status === 503) && page.status().includes("Lost contact with the Highbay server"), "the wait outage was not announced");
expect(page.$("#highbay-cp5-approve") === approve && approve.disabled && page.text("#highbay-cp4-inputs").includes("not known"), "the outage left the already rendered approval usable");
expect(page.text("#highbay-cp4-title") === verdict && page.text("#highbay-cp4-details") === details, "the outage discarded saved evidence");
approve.click();
expect(page.sent("POST", "/changes/approve").length === 0, "the unavailable update channel allowed approval submission");
await until(() => page.status() === "This page is up to date with the Highbay server again." && !approve.disabled && page.text("#highbay-cp4-inputs").includes("matches the agreed revision"), "same-version wait recovery did not restore rendered approval", 15000);
expect(page.sent("GET", "/changes").length === lists, "the recovery depended on a changed version or another list read");
expect(page.sent("GET", "/change-updates").slice(waits).some((r) => r.status === 200 && JSON.parse(r.answer).version === version), "recovery did not observe the unchanged owner version");
expect(page.$("#highbay-result-view").dataset.runId === input.run && page.text("#highbay-cp4-title") === verdict, "wait recovery changed the selected evidence");
page.close();
`)
}

// TestViewsPageBackgroundCardReadRequiresRecoveryBeforeApproval keeps a
// terminal report and unsaved draft through a pending and failed background
// card refresh. Retrying the card must perform a successful owner read before
// the retained result becomes approvable again.
func TestViewsPageBackgroundCardReadRequiresRecoveryBeforeApproval(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	runID := strings.Repeat("1", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, runID, verify.TestsWritten)
	// Candidate creation reads the assignment. Reset that one fixture fact so
	// the action below produces the actual false-to-true owner notification.
	cp.mu.Lock()
	build, found, err := cp.cards.loadOpenBuild()
	require.NoError(t, err)
	require.True(t, found)
	build.Builder.Read = false
	require.NoError(t, cp.cards.saveOpenBuild(build))
	cp.mu.Unlock()
	ps := startPageServer(t, cp)
	ps.act("builder-reads-assignment", func() {
		makeBuildFixtureBuilderCopy(t, cp)
		_, err := cp.GetBuilderAssignment(asCaller(pageOwner, CallerBuilder),
			&genhighbay.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
		require.NoError(t, err)
	})
	runPageScript(t, ps, map[string]any{"run": runID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check")?.classList.contains("shadcn-btn-primary") && !page.$("#highbay-build-review-check").disabled, "the saved passing check did not load");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
const draft = page.$('textarea[data-field="expected_outcome"]');
page.type(draft, "Preserve this draft through the card read");
page.$("#highbay-nav-build").click();
page.$("#highbay-build-review-check").click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.run && page.$("#highbay-cp5-approve") && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "the selected passing proof did not settle");
const approve = page.$("#highbay-cp5-approve");
const verdict = page.text("#highbay-cp4-title");
const details = page.text("#highbay-cp4-details");
const preserved = () => page.$("#highbay-result-view").dataset.runId === input.run && page.text("#highbay-cp4-title") === verdict && page.text("#highbay-cp4-details") === details && page.$("#highbay-cp5-approval-dirty-warning") !== null;
const cardPath = "/changes/chg-selected-inputs";
const buildPath = cardPath + "/build";
const runPath = "/verifications/" + input.run + "/review";
const before = page.sent("GET", cardPath).length;
await control(origin, "reply", { key: "GET " + cardPath, status: 503, body: { name: "internal", message: "The saved card could not be refreshed." } });
await control(origin, "hold", { key: "GET " + cardPath });
await control(origin, "act", { key: "builder-reads-assignment" });
try {
await until(() => page.sent("GET", cardPath).length > before && page.sent("GET", buildPath).every((r) => r.status === 200) && page.sent("GET", runPath).every((r) => r.status === 200) && page.sent("GET", buildPath).some((r) => r.status === 200 && JSON.parse(r.answer).build.builder.read), "the changed build did not reach the page with its card refresh held");
} catch (error) {
  throw new Error(error.message + ": " + JSON.stringify(page.requests.filter((r) => [cardPath, buildPath, runPath].includes(r.path)).map((r) => ({ path: r.path, status: r.status, answer: r.answer?.slice(0, 1800) }))));
}
const held = page.sent("GET", cardPath)[before];
expect(held.status === undefined && approve.disabled && page.text("#highbay-cp4-inputs").includes("not known"), "an outstanding background card read left the rendered approval usable");
expect(preserved(), "the pending card read discarded the report or draft");
approve.click();
expect(page.sent("POST", "/changes/approve").length === 0, "a pending card refresh submitted approval");
await control(origin, "release", { key: "GET " + cardPath });
await until(() => held.status === 503 && page.$("#highbay-cp4-card-error") && page.$("#highbay-cp4-retry-card") && !page.$("#highbay-cp4-retry-card").disabled, "the failed background card read exposed no user recovery");
expect(page.text("#highbay-cp4-card-error").includes("HTTP 503") && approve.disabled && preserved(), "the failed card read hid its failure, enabled approval or discarded work: " + JSON.stringify({message: page.text("#highbay-cp4-card-error"), disabled: approve.disabled, preserved: preserved(), run: page.$("#highbay-result-view").dataset.runId === input.run, verdict: page.text("#highbay-cp4-title") === verdict, details: page.text("#highbay-cp4-details") === details, draftKept: page.$("#highbay-cp5-approval-dirty-warning") !== null}));
approve.click();
expect(page.sent("POST", "/changes/approve").length === 0, "a failed background card refresh submitted approval");
await control(origin, "hold", { key: "GET " + cardPath });
const retries = page.sent("GET", cardPath).length;
page.$("#highbay-cp4-retry-card").click();
await until(() => page.sent("GET", cardPath).length > retries && page.$("#highbay-cp4-retry-card").disabled, "retry did not issue a new card read");
expect(approve.disabled && preserved(), "requesting recovery cleared the card-read failure before its owner answered");
await control(origin, "release", { key: "GET " + cardPath });
await until(() => page.sent("GET", cardPath).slice(retries).some((r) => r.status === 200) && !page.$("#highbay-cp4-card-error") && !approve.disabled, "a successful card retry did not restore current-input approval");
expect(preserved(), "card recovery replaced the saved report or unsaved draft");
expect(page.sent("POST", "/changes/approve").length === 0 && page.sent("POST", "/changes/confirm").length === 0, "card recovery submitted approval or draft edits");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-field="expected_outcome"]').value === "Preserve this draft through the card read", "returning to Agree lost the unsaved draft");
page.close();
`)
}

// TestViewsPageExplicitCardReadFailureRequiresRecoveryBeforeApproval starts
// without a background-read error. A manual card refresh must latch its own
// failure so the retained positive run cannot become approvable on completion.
func TestViewsPageExplicitCardReadFailureRequiresRecoveryBeforeApproval(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	runID := strings.Repeat("1", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, runID, verify.TestsWritten)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"run": runID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check") && !page.$("#highbay-build-review-check").disabled, "the initial current result did not load");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
const draft = page.$('textarea[data-field="expected_outcome"]');
page.type(draft, "Keep this draft while the manual read recovers");
await control(origin, "reply", { key: "POST /changes/confirm", status: 409, body: { name: "conflict", message: "Inspect the saved agreement before refining." } });
page.$("#highbay-confirm-submit").click();
await until(() => page.$("#highbay-load-latest-card") && !page.$("#highbay-load-latest-card").disabled, "the refused refinement did not offer a manual read");
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.run && page.$("#highbay-cp5-approve") && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "the saved proof did not become eligible before the explicit read");
expect(!page.$("#highbay-cp4-card-error"), "the test started with a card-read failure already latched");
const approve = page.$("#highbay-cp5-approve");
const verdict = page.text("#highbay-cp4-title");
const preserved = () => page.$("#highbay-result-view").dataset.runId === input.run && page.text("#highbay-cp4-title") === verdict && page.$("#highbay-cp5-approval-dirty-warning") !== null;
const cardPath = "/changes/chg-selected-inputs";
await control(origin, "reply", { key: "GET " + cardPath, status: 503, body: { name: "internal", message: "The current agreement is unreadable." } });
await control(origin, "hold", { key: "GET " + cardPath });
const before = page.sent("GET", cardPath).length;
page.$("#highbay-nav-cp1").click();
page.$("#highbay-load-latest-card").click();
page.$("#highbay-nav-cp5").click();
await until(() => page.sent("GET", cardPath).length > before && (page.$("#highbay-cp5-approve")?.disabled ?? true), "the pending explicit read left approval enabled");
expect(preserved(), "the manual read discarded the report or draft");
await control(origin, "release", { key: "GET " + cardPath });
await until(() => page.$("#highbay-cp4-card-error") && page.$("#highbay-cp4-retry-card") && !page.$("#highbay-cp4-retry-card").disabled, "the failed explicit read did not latch its own recovery state");
expect(page.text("#highbay-cp4-card-error").includes("HTTP 503") && (page.$("#highbay-cp5-approve")?.disabled ?? true) && preserved(), "the failed explicit read restored approval or discarded work");
page.$("#highbay-cp5-approve")?.click();
expect(page.sent("POST", "/changes/approve").length === 0, "the failed manual card refresh submitted approval");
page.$("#highbay-cp4-retry-card").click();
await until(() => !page.$("#highbay-cp4-card-error") && !(page.$("#highbay-cp5-approve")?.disabled ?? true), "a successful explicit retry did not restore eligibility");
expect(preserved(), "successful recovery replaced the saved report or draft");
expect(page.sent("POST", "/changes/approve").length === 0 && page.sent("POST", "/changes/confirm").length === 1, "recovery submitted approval or resubmitted the draft");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-field="expected_outcome"]').value === "Keep this draft while the manual read recovers", "returning to Agree lost the unsaved draft");
// Closing a browser tab is a separate task from the navigation click. Let the
// click's mount microtasks finish before jsdom tears down the document.
await new Promise((nextTurn) => setImmediate(nextTurn));
page.close();
`)
}

// TestViewsPageReviewFailedCheckFocusesItsSavedResult verifies the nonpassing
// branch of Build's review action moves keyboard focus to the report itself.
func TestViewsPageReviewFailedCheckFocusesItsSavedResult(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	runID := strings.Repeat("1", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), false, candidate)
	setListedRunSource(t, runsDir, runID, verify.TestsWritten)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"run": runID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check") && !page.$("#highbay-build-review-check").disabled, "the failed saved check did not reach Build");
page.$("#highbay-build-review-check").click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.run && page.text("#highbay-cp4-title").includes("did not pass"), "review did not open the exact failed result");
expect(page.doc.activeElement === page.$("#highbay-cp5-title"), "reviewing a failed check did not focus its result stage");
expect(!page.$("#highbay-cp5-approve"), "a failed result offered approval");
page.close();
`)
}

// TestViewsPageBuildRetryInvalidatesProofBeforeTheVersionNotification makes
// an explicit build read discover changed tests before the held wait channel
// can announce them. A cached positive run observation cannot survive that
// independently obtained build fact.
func TestViewsPageBuildRetryInvalidatesProofBeforeTheVersionNotification(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	runID := strings.Repeat("1", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, runID, verify.TestsWritten)
	ps := startPageServer(t, cp)
	selectedInputsActions(t, ps, cp, card.ChangeID)
	runPageScript(t, ps, map[string]any{"run": runID}, `
const cardPath = "/changes/chg-selected-inputs";
const buildPath = cardPath + "/build";
const runPath = "/verifications/" + input.run;
// Install both controls before opening the page: there is no earlier wait
// request that can deliver the changed version around the test's gate.
await control(origin, "hold", { key: "GET /change-updates" });
await control(origin, "reply", { key: "GET " + buildPath, status: 503, body: { name: "internal", message: "Retry the saved build read." } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-refresh") && !page.$("#highbay-build-refresh").disabled && page.sent("GET", runPath).some((r) => r.status === 200 && JSON.parse(r.answer).inputs_current === true), "the initial failed build read did not retain the positive T1 owner observation");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "Keep the editor beside saved proof");
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.run && page.$("#highbay-cp5-approve") && (page.$("#highbay-cp5-approve")?.disabled ?? true) && !page.$("#highbay-load-verifications").disabled, "the failed build's saved proof did not load");
const verdict = page.text("#highbay-cp4-title");
const lists = page.sent("GET", "/changes").length;
const before = page.requests.length;
await control(origin, "act", { key: "accept-t2" });
page.$("#highbay-nav-build").click();
page.$("#highbay-build-refresh").click();
await until(() => !page.$("#highbay-build-error") && page.sent("GET", buildPath).some((r) => r.status === 200 && JSON.parse(r.answer).build.test_writer.tests.commit === "3333333333333333333333333333333333333333"), "the explicit retry did not discover accepted T2");
await until(() => page.requests.slice(before).some((r) => r.path === runPath + "/review" && r.status === 200 && JSON.parse(r.answer).inputs_current === false), "discovering T2 through Build did not replace the positive T1 owner review");
expect(page.sent("GET", "/changes").length === lists && page.sent("GET", "/change-updates").every((r) => r.status === undefined), "the test received a version notification before the explicit retry established freshness");
page.$("#highbay-nav-cp5").click();
await until(() => page.text("#highbay-cp4-inputs").includes("does not match"), "the saved result did not reflect the recovered Build");
expect((page.$("#highbay-cp5-approve")?.disabled ?? true) && page.$("#highbay-result-view").dataset.runId === input.run && page.text("#highbay-cp4-title") === verdict, "Build recovery enabled stale proof or replaced the selected report");
page.$("#highbay-cp5-approve")?.click();
expect(page.sent("POST", "/changes/approve").length === 0 && page.sent("POST", buildPath).length === 0, "the independent build read submitted approval or started a build");
page.close();
`)
}

// TestViewsPageConfirmationWaitsForMatchingListBeforeNamingCurrentAgreement
// holds owner notifications across an in-page refinement. Neither the old list
// nor a pending replacement can name the current agreement for saved proof.
func TestViewsPageConfirmationWaitsForMatchingListBeforeNamingCurrentAgreement(t *testing.T) {
	cp, earlier, runsDir, candidate := selectedInputsFixture(t)
	older, latest := strings.Repeat("1", 32), strings.Repeat("2", 32)
	accepted := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	writeListedRunAt(t, runsDir, pageOwner, earlier, older, accepted, true, candidate)
	example := *earlier.Examples[0]
	example.ExpectedOutcome = "The manager reviews the refund before deciding"
	card, err := cp.ConfirmIntent(asCaller(pageOwner, CallerUser), &genhighbay.ConfirmIntentPayload{
		ChangeID: earlier.ChangeID, RequestRevision: earlier.RequestRevision,
		Answers: map[string]string{}, Examples: []*genhighbay.ExampleItem{&example},
	})
	require.NoError(t, err)
	require.NotEqual(t, earlier.RequestRevision, card.RequestRevision)
	writeListedRunAt(t, runsDir, pageOwner, card, latest, accepted.Add(time.Minute), true, candidate)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{
		"older": older, "latest": latest, "earlier": earlier.RequestRevision, "listed": card.RequestRevision,
	}, `
await control(origin, "hold", { key: "GET /change-updates" });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-checkpoint-view")?.dataset.requestRevision === input.listed &&
  page.sent("GET", "/change-updates").some((r) => r.status === undefined) && !page.$("#highbay-build-loading"),
  "the original agreement and held update read did not settle");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "The manager reviews the refund and records the reason");
page.$("#highbay-confirm-submit").click();
await until(() => page.sent("POST", "/changes/confirm").some((r) => r.status === 200), "in-page refinement did not save");
const revised = JSON.parse(page.sent("POST", "/changes/confirm").findLast((r) => r.status === 200).answer);
expect(revised.request_revision !== input.listed && revised.status === "confirmed", "the refinement did not produce a confirmed revision");
await until(() => page.$("#highbay-cp1-editor").dataset.requestRevision === revised.request_revision, "the page did not mount its new agreement");
expect(JSON.parse(page.sent("GET", "/changes").findLast((r) => r.status === 200).answer).changes[0].request_revision === input.listed,
  "the list caught up before the held owner notification");
page.$("#highbay-nav-cp4").click();
const reviewed = (id) => page.$("#highbay-result-view").dataset.runId === id &&
  page.sent("GET", "/verifications/" + id + "/review").some((r) => r.status === 200 && JSON.parse(r.answer).inputs_current === false) &&
  !page.$("#highbay-cp4-verification").hasAttribute("aria-busy");
await until(() => reviewed(input.latest), "Check did not read the selected saved run against the revised agreement");
const unknown = () => page.text("#highbay-cp4-inputs").includes("not known") && !page.$("#highbay-cp4-input-difference");
expect(unknown(), "a stale list named the just-replaced agreement as current");
expect(page.text("#highbay-cp4-title").includes("passed"), "unknown currentness hid the saved verdict");
page.$("#highbay-cp4-run-" + input.older).click();
await until(() => reviewed(input.older), "the earlier saved run did not load");
expect(unknown(), "the earlier run treated the stale list as the current agreement");
await control(origin, "hold", { key: "GET /changes" });
const lists = page.sent("GET", "/changes").length;
await control(origin, "release", { key: "GET /change-updates" });
await until(() => page.sent("GET", "/changes").length > lists &&
  page.sent("GET", "/changes").slice(lists).some((r) => r.status === undefined), "the replacement list was not held");
expect(unknown(), "a pending list exposed a stale current-agreement explanation");
await control(origin, "release", { key: "GET /changes" });
const namesRevisedAgreement = (used) => {
  const codes = page.$$("#highbay-cp4-input-difference code").map((node) => node.textContent);
  return page.text("#highbay-cp4-inputs").includes("does not match") && codes.length === 2 &&
    used.startsWith(codes[0]) && revised.request_revision.startsWith(codes[1]);
};
await until(() => namesRevisedAgreement(input.earlier), "the fresh list did not identify the revised agreement for the older run");
page.$("#highbay-cp4-run-" + input.latest).click();
await until(() => namesRevisedAgreement(input.listed), "the prior agreement's run did not name the newly confirmed agreement");
expect(page.sent("POST", "/changes/approve").length === 0 && !page.requests.some((r) => r.method === "POST" && /^\/changes\/[^/]+\/verifications$/.test(r.path)),
  "reading saved proof approved or started verification");
page.close();
`)
}

// ListVerifications preserves the actual owner result and captures only the
// armed answer. The HTTP answer-later gate controls its later delivery.
func (s *selectedInputsListRead) ListVerifications(
	ctx context.Context,
	p *genhighbay.ListVerificationsPayload,
) (*genhighbay.VerificationListResult, error) {
	result, err := s.ControlPlane.ListVerifications(ctx, p)
	if err == nil && s.armed.CompareAndSwap(true, false) {
		s.captured <- result
	}
	return result, err
}

// selectedInputsFixture starts a real saved build with accepted T1 tests. Its
// simulated working-copy boundary and checker avoid executing candidate code.
func selectedInputsFixture(t *testing.T) (*ControlPlane, *genhighbay.ChangeCardResult, string, string) {
	t.Helper()
	runsDir := t.TempDir()
	cp := newBuildControlPlane(t, Options{BuildCacheDir: t.TempDir(),
		RunsDir: runsDir, WrittenTestRuns: &WrittenTestRuns{ReportDir: filepath.Join(runsDir, "reports"), Timeout: time.Minute},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := buildableChange(t, cp, "chg-selected-inputs")
	_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	saveListedTestState(t, cp, card.ChangeID, testsAccepted, strings.Repeat("2", 40))
	candidate := commitBuilderCandidateFixture(t, cp, card)
	return cp, card, runsDir, candidate
}

// selectedInputsActions changes the saved accepted report. As with the
// checker fixture, the normal wait/list reads deliver these changes.
func selectedInputsActions(t *testing.T, ps *pageServer, cp *ControlPlane, changeID string) {
	t.Helper()
	for _, selected := range []struct {
		name, commit string
		report       int
	}{
		{name: "accept-t1", commit: strings.Repeat("2", 40), report: 1},
		{name: "accept-t2", commit: strings.Repeat("3", 40), report: 2},
	} {
		ps.act(selected.name, func() {
			saveListedTestState(t, cp, changeID, testsAccepted, selected.commit)
			if selected.report == 1 {
				return
			}
			cp.mu.Lock()
			defer cp.mu.Unlock()
			saved, found, err := cp.cards.loadOpenBuild()
			require.NoError(t, err)
			require.True(t, found)
			saved.ReportNumber = selected.report
			require.NoError(t, cp.cards.saveOpenBuild(saved))
		})
	}
}

// writeSelectedInputsRun gives the newer saved run a distinct test commit in
// both its request and valid report binding. It reuses the ordinary supervisor
// fixture layout rather than mismatching a request with another report's bytes.
func writeSelectedInputsRun(
	t *testing.T,
	runsDir string,
	card *genhighbay.ChangeCardResult,
	runID, suiteCommit, candidate string,
) {
	t.Helper()
	writeListedRunAt(t, runsDir, pageOwner, card, runID,
		time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, runID, verify.TestsWritten)
	requestPath := filepath.Join(runsDir, runID, "request.json")
	raw, err := os.ReadFile(requestPath)
	require.NoError(t, err)
	var request verifyrun.Request
	require.NoError(t, json.Unmarshal(raw, &request))
	request.Plan.SuiteCommit = suiteCommit
	report := passingReport(t, card.RequestRevision, request.Plan.CandidateCommit, reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: suiteCommit, Environment: "sim",
		Artifacts: map[string]string{"orders": strings.Repeat("a", 64)},
	})
	writeReportFile(t, request.Plan.ReportDir, report)
	raw, err = json.Marshal(request)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(requestPath, raw, 0o600))
	raw, err = json.Marshal(verifyrun.Result{Kind: verifyrun.ResultReport, ReportDigest: report.Digest})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(runsDir, runID, "result.json"), raw, 0o600))
}
