// These served-page checks use the real saved-run review owner. They execute
// neither candidate code nor a model, and keep unknown coverage distinct from
// excluded drafts and completed test results.
package controlplane

import (
	"strings"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/designeval"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/goadesign/highbay/internal/verifyrun"
	"github.com/stretchr/testify/require"
)

// TestViewsPageApprovalEvidencePreservesCoverageMeaning compares Check and
// Approve against a saved run, including a test absent from the report and
// model-judged examples with and without a writer's coverage statement.
func TestViewsPageApprovalEvidencePreservesCoverageMeaning(t *testing.T) {
	for _, mode := range []string{"mapped", "evaluator", "empty", "not_recorded"} {
		t.Run(mode, func(t *testing.T) {
			cp, card, runsDir, candidate := selectedInputsFixture(t)
			runID := strings.Repeat("1", 32)
			writeListedRunAt(t, runsDir, pageOwner, card, runID,
				time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
			setListedRunSource(t, runsDir, runID, verify.TestsWritten)
			editVerificationRequest(t, runsDir, runID, func(req *verifyrun.Request) {
				if mode == "not_recorded" {
					return
				}
				req.StoryContext = verifyrun.StoryContext{State: verifyrun.StoryRecorded,
					BuildNumber: 1, BuildStartedAt: req.AcceptedAt.Add(-time.Minute),
					Stories: []designeval.StorySummary{}}
				if mode == "empty" {
					req.StoryContext.TestCoverage = &verifyrun.TestCoverageSnapshot{Report: 1,
						Answers: []*genhighbay.ConfirmedAnswer{}, Examples: []*genhighbay.ExampleItem{},
						CoverageStatements: []*genhighbay.TestCoverageStatement{}}
					return
				}
				story := designeval.StorySummary{Name: "Recorded orders"}
				for _, key := range []string{"a", "unreported", "draft"} {
					flow := designeval.StoryFlow{Key: key, Title: "Recorded " + key, Complete: key != "draft",
						Steps: []designeval.StoryStep{{Key: "start", Summary: "Begin", Kind: "start"}}}
					if flow.Complete {
						flow.Steps = append(flow.Steps, designeval.StoryStep{Key: "end", Summary: "See the outcome", Kind: "outcome"})
					}
					story.Flows = append(story.Flows, flow)
				}
				req.StoryContext.Stories = append(req.StoryContext.Stories, story)
				if mode == "evaluator" {
					req.Plan.Tests = verify.TestsEvaluator
				} else {
					req.StoryContext.TestCoverage = &verifyrun.TestCoverageSnapshot{
						Report: 1,
						Answers: []*genhighbay.ConfirmedAnswer{
							{ID: "tax", Question: "Does the limit include tax?", Answer: "No"},
							{ID: "destination", Question: "Where does the refund go?", Answer: "Original payment"},
						},
						Examples: []*genhighbay.ExampleItem{
							{ID: "check_01", Scenario: "Recorded refund at the limit", InputSummary: "$50 before tax", ExpectedOutcome: "Refund", VerificationKind: "deterministic"},
							{ID: "check_02", Scenario: "Recorded refund explanation", InputSummary: "Refund rejected", ExpectedOutcome: "Explain the refund rule", VerificationKind: "model_judged"},
							{ID: "check_03", Scenario: "Recorded refund guidance", InputSummary: "Refund needs review", ExpectedOutcome: "Explain the next step", VerificationKind: "model_judged"},
						},
						CoverageStatements: []*genhighbay.TestCoverageStatement{
							{Scenario: "a", QuestionIds: []genhighbay.TestCoverageReference{"tax"}, ExampleIds: []genhighbay.TestCoverageReference{"check_01", "check_03"}, Explanation: "Checks the agreed refund limit and next step."},
						},
					}
				}
			})
			ps := startPageServer(t, cp)
			runPageScript(t, ps, map[string]any{
				"mode": mode, "run": runID, "currentExample": card.Examples[0].ExpectedOutcome,
			}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-nav-cp4"), "the change did not open");
const coverageDetails = (selector, name) => {
  const term = page.$$(selector + " dt").find((term) => term.textContent === name);
  return term?.nextElementSibling.textContent ?? "";
};
const checkLimits = (selector) => {
  const limits = page.text(selector);
  expect(/no separate.*check_project.*attached/i.test(limits), "separate builder check results were presented as run evidence");
  if (input.mode === "evaluator") {
    expect(limits.includes("A passed test does not prove that a particular Story flow was covered"), "unknown mapping lost its explanation");
  }
  if (input.mode === "mapped") {
    expect(limits.includes("Recorded draft") && limits.includes("Draft, no required test"), "the recorded draft exclusion disappeared");
    expect(limits.includes("Recorded refund explanation") && limits.includes("Recorded refund guidance"), "the recorded model-judged examples were omitted from the limits");
    expect(/no separate.*example.*verdict/i.test(limits) && !limits.includes("Not run"), "the limits misstate whether individual examples were judged");
  }
};
const checkCoverage = (selector) => {
  const coverage = page.text(selector);
  expect(!coverage.includes(input.currentExample), "the run's coverage was backfilled from today's card");
  if (input.mode === "empty") {
    expect(coverage.includes("This agreement has no confirmed answers or examples."), "recorded empty coverage basis was treated as absent");
  } else if (input.mode === "mapped") {
    expect(coverage.includes("Test writer's stated coverage") && coverage.includes("Highbay did not judge them") && coverage.includes("Original payment"), "writer attribution or the recorded answer was lost");
    expect(coverageDetails(selector, "Where does the refund go?").includes("Not stated"), "an unclaimed answer acquired a writer relation");
    expect(coverageDetails(selector, "Recorded refund at the limit").includes("Deterministic check"), "the deterministic example lost its classification");
    const unstated = coverageDetails(selector, "Recorded refund explanation");
    expect(unstated.includes("Model-judged check") && unstated.includes("Not stated") && !unstated.includes("Not run"), "a model-judged example without a statement acquired an execution verdict");
    const stated = coverageDetails(selector, "Recorded refund guidance");
    expect(stated.includes("Model-judged check") && stated.includes("Checks the agreed refund limit and next step.") && stated.includes("Run result: Passed"), "the stated model-judged example lost its writer relation or the declared scenario outcome");
  }
};
page.$("#highbay-nav-cp4").click();
const reviewPath = "/verifications/" + input.run + "/review";
await until(() => page.$("#highbay-result-view").dataset.runId === input.run &&
  page.sent("GET", reviewPath).some((request) => request.status === 200) &&
  page.$("#highbay-cp4-evidence-limits") && !page.$("#highbay-cp4-verification").hasAttribute("aria-busy"),
  "Check did not load the selected run's recorded review");
checkCoverage("#highbay-cp4-coverage");
checkLimits("#highbay-cp4-evidence-limits");
const boundReport = page.text("#highbay-cp4-test-binding").match(/\breport \d+\b/i)?.[0].toLowerCase();
expect(boundReport === (["mapped", "empty"].includes(input.mode) ? "report 1" : undefined), "Check did not distinguish a recorded report number from missing evidence");
if (input.mode === "evaluator") {
  await until(() => page.text("#highbay-cp4-inputs").includes("does not match"), "an owner-negative source mismatch lost its generic warning");
  expect(!page.$("#highbay-cp4-input-difference"), "Check invented a visible cause for the evaluator mismatch");
}
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-approval-stage")?.dataset.runId === input.run && !page.$("#highbay-cp5-observation-status"), "owner review did not settle");
const stage = page.text("#highbay-approval-stage");
checkCoverage("#highbay-approval-coverage");
checkLimits(".highbay-approval-exclusions");
if (input.mode === "not_recorded") {
  expect(stage.includes("Story context was not recorded for this run") && !page.$(".highbay-flow-evidence"), "missing run context was backfilled from current state");
} else if (input.mode === "empty") {
  expect(stage.includes("This agreement recorded no Story flows") && !stage.includes("Story context was not recorded"), "recorded empty Story was treated as absent");
} else {
  const rows = page.$$(".highbay-flow-evidence tbody tr");
  expect(rows.length === 2, "finished flows were lost or included a draft");
  if (input.mode === "evaluator") {
    expect(rows.every((row) => row.textContent.includes("Mapping not recorded for this evaluator") && row.textContent.includes("Coverage not recorded")), "evaluator results invented flow coverage");
    expect(page.text("#highbay-approval-coverage").includes("configured evaluator. Test-writer coverage: Not stated."), "operator tests acquired invented writer statements");
  } else {
    expect(rows[0].textContent.includes("Passed") && rows[1].textContent.includes("Not reported"), "missing expected scenario was presented as a pass or exclusion");
  }
}
const otherIds = page.$$(".highbay-approval-test-list code").map((node) => node.textContent);
expect(otherIds.includes("holdout-1") && !otherIds.includes("h"), "safe report projection was replaced by the private holdout identifier");
expect(page.sent("POST", "/changes/approve").length === 0 && !page.requests.some((r) => r.method === "POST" && /^\/changes\/[^/]+\/verifications$/.test(r.path)), "reading evidence started work");
page.close();
`)
		})
	}
}

// TestViewsPageCheckKeepsFrozenEvidenceWithSelectedRun releases a saved
// review after selection changes. Copies of the same candidate still retain
// their separate report numbers and examples.
func TestViewsPageCheckKeepsFrozenEvidenceWithSelectedRun(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	older, latest := strings.Repeat("1", 32), strings.Repeat("2", 32)
	const laterExample = "Explain the revised refund rule"
	example := *card.Examples[0]
	example.Scenario = laterExample
	example.VerificationKind = "model_judged"
	current, err := cp.ConfirmIntent(asCaller(pageOwner, CallerUser), &genhighbay.ConfirmIntentPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		Answers: map[string]string{}, Examples: []*genhighbay.ExampleItem{&example},
	})
	require.NoError(t, err)
	require.NotEqual(t, card.RequestRevision, current.RequestRevision)
	accepted := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	for _, run := range []struct {
		id     string
		card   *genhighbay.ChangeCardResult
		report int
		at     time.Time
	}{
		{id: older, card: card, report: 1, at: accepted},
		{id: latest, card: current, report: 2, at: accepted.Add(time.Minute)},
	} {
		writeListedRunAt(t, runsDir, pageOwner, run.card, run.id, run.at, true, candidate)
		editVerificationRequest(t, runsDir, run.id, func(req *verifyrun.Request) {
			req.Plan.Tests = verify.TestsWritten
			req.StoryContext = verifyrun.StoryContext{
				State: verifyrun.StoryRecorded, BuildNumber: run.report,
				BuildStartedAt: run.at.Add(-time.Second), Stories: []designeval.StorySummary{},
				TestCoverage: &verifyrun.TestCoverageSnapshot{
					Report: run.report, Answers: []*genhighbay.ConfirmedAnswer{}, Examples: run.card.Examples,
					CoverageStatements: []*genhighbay.TestCoverageStatement{},
				},
			}
		})
	}
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{
		"older": older, "latest": latest, "earlierExample": card.Examples[0].Scenario, "laterExample": laterExample,
		"earlierRevision": card.RequestRevision, "currentRevision": current.RequestRevision,
	}, `
const page = await openPage(origin, { link: input.link });
const coverage = () => page.text("#highbay-cp4-coverage");
const binding = () => page.text("#highbay-cp4-test-binding");
const selected = () => page.$("#highbay-result-view").dataset.runId;
const latestEvidence = () => selected() === input.latest && coverage().includes(input.laterExample) &&
  !coverage().includes(input.earlierExample) && /\breport 2\b/i.test(binding()) && !/\breport 1\b/i.test(binding());
await until(() => page.$("#highbay-nav-cp4"), "the change did not open");
page.$("#highbay-nav-cp4").click();
await until(() => latestEvidence() && page.$("#highbay-cp4-run-" + input.older), "Check did not show the latest selected run's frozen evidence");
const earlierPath = "/verifications/" + input.older + "/review";
expect(page.sent("GET", earlierPath).length === 0, "Check read a review for an unselected run");
await control(origin, "answer-later", { key: "GET " + earlierPath });
page.$("#highbay-cp4-run-" + input.older).click();
await until(() => selected() === input.older && page.sent("GET", earlierPath).some((r) => r.status === undefined), "the earlier selected run did not request its own review");
expect(!coverage().includes(input.laterExample) && !/\breport 2\b/i.test(binding()), "the pending review displayed a different run's frozen evidence");
expect(page.text("#highbay-cp4-title").includes("passed") && page.text("#highbay-cp4-verification").includes("holdout-1"), "the pending review hid the declared results");
page.$("#highbay-cp4-run-" + input.latest).click();
await until(latestEvidence, "the latest run could not be reviewed independently");
await control(origin, "release", { key: "GET " + earlierPath });
await until(() => page.sent("GET", earlierPath).some((r) => r.status === 200), "the held review did not finish");
await sleep(0);
expect(latestEvidence(), "the late review changed the selected run's evidence");
page.$("#highbay-cp4-run-" + input.older).click();
await until(() => selected() === input.older && coverage().includes(input.earlierExample) &&
  !coverage().includes(input.laterExample) && /\breport 1\b/i.test(binding()), "selecting the earlier run did not restore its report and examples");
const revisions = () => page.$$("#highbay-cp4-input-difference code").map((node) => node.textContent);
await until(() => {
  const [used, current] = revisions();
  return page.text("#highbay-cp4-inputs").includes("does not match") && used && current && used !== current &&
    input.earlierRevision.startsWith(used) && input.currentRevision.startsWith(current);
},
  "Check did not identify the run's agreement and the confirmed current agreement");
expect(!/test.*commit/i.test(page.text("#highbay-cp4-input-difference")), "a changed agreement also acquired an unobserved tests mismatch");
page.close();
`)
}

// TestViewsPageDelayedReviewCannotReplaceTheSelectedRun releases an earlier
// available review after the user has selected a different, blocked result.
func TestViewsPageDelayedReviewCannotReplaceTheSelectedRun(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	older, latest := strings.Repeat("1", 32), strings.Repeat("2", 32)
	writeListedRunAt(t, runsDir, pageOwner, card, older,
		time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC), true, candidate)
	setListedRunSource(t, runsDir, older, verify.TestsWritten)
	writeSelectedInputsRun(t, runsDir, card, latest, strings.Repeat("3", 40), candidate)
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"older": older, "latest": latest}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check") && !page.$("#highbay-build-review-check").disabled, "latest saved run did not load");
page.$("#highbay-build-review-check").click();
await until(() => page.$("#highbay-cp5-approve-blocked") && page.$("#highbay-cp4-run-" + input.older), "latest blocked review did not settle");
const path = "/verifications/" + input.older + "/review";
await control(origin, "answer-later", { key: "GET " + path });
page.$("#highbay-cp4-run-" + input.older).click();
await until(() => page.sent("GET", path).some((r) => r.status === undefined), "older review was not held");
page.$("#highbay-cp4-run-" + input.latest).click();
await until(() => page.$("#highbay-approval-stage").dataset.runId === input.latest && page.$("#highbay-cp5-approve-blocked") && !page.$("#highbay-cp5-observation-status"), "selected blocked review did not settle independently");
await control(origin, "release", { key: "GET " + path });
await until(() => page.sent("GET", path).some((r) => r.status === 200), "held review did not finish");
expect(page.$("#highbay-approval-stage").dataset.runId === input.latest && !page.$("#highbay-cp5-approve"), "late available review replaced the selected run or its permission");
page.$("#highbay-cp4-run-" + input.older).click();
await until(() => page.$("#highbay-approval-stage").dataset.runId === input.older && page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "explicitly selecting the matching run did not recover");
expect(page.$("#highbay-cp5-review-latest") && !page.$("#highbay-cp5-review-latest").classList.contains("shadcn-btn-primary"), "latest navigation removed the selected older run's available approval");
page.$("#highbay-cp5-review-latest").click();
await until(() => page.$("#highbay-approval-stage").dataset.runId === input.latest && page.$("#highbay-cp5-approve-blocked"), "latest action did not review the owner's exact latest run");
expect(page.sent("POST", "/changes/approve").length === 0, "selection submitted approval");
page.close();
`)
}

// TestViewsPageOffersANewerCheckWithoutReplacingTheSavedResult follows a new
// passing run while the user reviews an earlier failure. Recorded fixtures and
// the existing served-page consumer prove navigation without candidate execution.
func TestViewsPageOffersANewerCheckWithoutReplacingTheSavedResult(t *testing.T) {
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	older, latest := strings.Repeat("1", 32), strings.Repeat("2", 32)
	accepted := time.Date(2026, 11, 13, 9, 0, 0, 0, time.UTC)
	writeListedRunAt(t, runsDir, pageOwner, card, older, accepted, false, candidate)
	setListedRunSource(t, runsDir, older, verify.TestsWritten)
	ps := startPageServer(t, cp)
	ps.act("save-passing-check", func() {
		writeListedRunAt(t, runsDir, pageOwner, card, latest, accepted.Add(time.Minute), true, candidate)
		setListedRunSource(t, runsDir, latest, verify.TestsWritten)
	})
	runPageScript(t, ps, map[string]any{"older": older, "latest": latest}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-review-check") && !page.$("#highbay-build-review-check").disabled, "failed check did not load");
page.$("#highbay-build-review-check").click();
await until(() => page.$("#highbay-approval-stage")?.dataset.runId === input.older && page.$("#highbay-cp5-approve-blocked") && !page.$("#highbay-cp5-observation-status"), "old failure did not settle");
expect(!page.$("#highbay-cp5-review-latest"), "the only check offered a newer result");
await control(origin, "act", { key: "save-passing-check" });
await until(() => page.$("#highbay-cp5-review-latest"), "a new passing run did not offer a direct review action");
expect(page.$("#highbay-approval-stage").dataset.runId === input.older && page.$("#highbay-cp5-approve-blocked"), "background latest discovery replaced historical evidence");
expect(page.$("#highbay-cp5-review-latest").classList.contains("shadcn-btn-primary"), "the failed run's next action does not lead to the latest check");
const oldPath = "/verifications/" + input.older + "/review";
await until(() => !page.$("#highbay-cp5-refresh-approval").disabled, "background review did not settle before refresh");
const reads = page.sent("GET", oldPath).length;
page.$("#highbay-cp5-refresh-approval").click();
await until(() => page.sent("GET", oldPath).length > reads && !page.$("#highbay-cp5-refresh-approval").disabled, "refresh did not reread the displayed failure");
expect(page.$("#highbay-approval-stage").dataset.runId === input.older, "refresh silently selected another check");
page.$("#highbay-cp5-review-latest").click();
await until(() => page.$("#highbay-approval-stage").dataset.runId === input.latest && page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "latest action did not load the passed run and owner review");
expect(page.doc.activeElement === page.$("#highbay-cp5-title"), "latest action did not focus the review destination");
expect(!page.$("#highbay-cp5-review-latest"), "latest check still offered itself as newer");
page.$("#highbay-cp4-run-" + input.older).click();
await until(() => page.$("#highbay-approval-stage").dataset.runId === input.older && page.$("#highbay-cp5-approve-blocked"), "earlier failure was no longer selectable");
expect(page.sent("POST", "/changes/approve").length === 0 && !page.requests.some((r) => r.method === "POST" && /^\/changes\/[^/]+\/verifications$/.test(r.path)), "review navigation started work or approved it");
page.close();
`)
}
