// These served-page checks use the real saved-run review owner. They execute
// neither candidate code nor a model, and keep unknown coverage distinct from
// excluded drafts and completed test results.
package controlplane

import (
	"strings"
	"testing"
	"time"

	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
)

// TestViewsPageApprovalEvidencePreservesCoverageMeaning renders the owning
// request's recorded context, including an expected test absent from a report.
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
				}
			})
			ps := startPageServer(t, cp)
			runPageScript(t, ps, map[string]any{"mode": mode, "run": runID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-nav-cp5"), "the change did not open");
page.$("#surla-nav-cp5").click();
await until(() => page.$("#surla-approval-stage")?.dataset.runId === input.run && !page.$("#surla-cp5-observation-status"), "owner review did not settle");
const stage = page.text("#surla-approval-stage");
if (input.mode === "not_recorded") {
  expect(stage.includes("Story context was not recorded for this run") && !page.$(".surla-flow-evidence"), "missing run context was backfilled from current state");
} else if (input.mode === "empty") {
  expect(stage.includes("This agreement recorded no Story flows") && !stage.includes("Story context was not recorded"), "recorded empty Story was treated as absent");
} else {
  const rows = page.$$(".surla-flow-evidence tbody tr");
  expect(rows.length === 2 && stage.includes("Recorded draft") && stage.includes("Draft, no required test"), "finished/draft coverage was lost");
  if (input.mode === "evaluator") {
    expect(rows.every((row) => row.textContent.includes("Mapping not recorded for this evaluator") && row.textContent.includes("Coverage not recorded")), "evaluator results invented flow coverage");
    expect(stage.includes("A passed test does not prove that a particular Story flow was covered"), "unknown mapping lost its explanation");
  } else {
    expect(rows[0].textContent.includes("Passed") && rows[1].textContent.includes("Not reported"), "missing expected scenario was presented as a pass or exclusion");
  }
}
const otherIds = page.$$(".surla-approval-test-list code").map((node) => node.textContent);
expect(otherIds.includes("holdout-1") && !otherIds.includes("h"), "safe report projection was replaced by the private holdout identifier");
expect(page.sent("POST", "/changes/approve").length === 0 && page.sent("POST", "/changes/verify").length === 0, "reading evidence started work");
page.close();
`)
		})
	}
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
await until(() => page.$("#surla-build-review-check") && !page.$("#surla-build-review-check").disabled, "latest saved run did not load");
page.$("#surla-build-review-check").click();
await until(() => page.$("#surla-cp5-approve-blocked") && page.$("#surla-cp4-run-" + input.older), "latest blocked review did not settle");
const path = "/verifications/" + input.older + "/review";
await control(origin, "answer-later", { key: "GET " + path });
page.$("#surla-cp4-run-" + input.older).click();
await until(() => page.sent("GET", path).some((r) => r.status === undefined), "older review was not held");
page.$("#surla-cp4-run-" + input.latest).click();
await until(() => page.$("#surla-approval-stage").dataset.runId === input.latest && page.$("#surla-cp5-approve-blocked") && !page.$("#surla-cp5-observation-status"), "selected blocked review did not settle independently");
await control(origin, "release", { key: "GET " + path });
await until(() => page.sent("GET", path).some((r) => r.status === 200), "held review did not finish");
expect(page.$("#surla-approval-stage").dataset.runId === input.latest && !page.$("#surla-cp5-approve"), "late available review replaced the selected run or its permission");
page.$("#surla-cp4-run-" + input.older).click();
await until(() => page.$("#surla-approval-stage").dataset.runId === input.older && page.$("#surla-cp5-approve") && !page.$("#surla-cp5-approve").disabled, "explicitly selecting the matching run did not recover");
expect(page.$("#surla-cp5-review-latest") && !page.$("#surla-cp5-review-latest").classList.contains("shadcn-btn-primary"), "latest navigation removed the selected older run's available approval");
page.$("#surla-cp5-review-latest").click();
await until(() => page.$("#surla-approval-stage").dataset.runId === input.latest && page.$("#surla-cp5-approve-blocked"), "latest action did not review the owner's exact latest run");
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
await until(() => page.$("#surla-build-review-check") && !page.$("#surla-build-review-check").disabled, "failed check did not load");
page.$("#surla-build-review-check").click();
await until(() => page.$("#surla-approval-stage")?.dataset.runId === input.older && page.$("#surla-cp5-approve-blocked") && !page.$("#surla-cp5-observation-status"), "old failure did not settle");
expect(!page.$("#surla-cp5-review-latest"), "the only check offered a newer result");
await control(origin, "act", { key: "save-passing-check" });
await until(() => page.$("#surla-cp5-review-latest"), "a new passing run did not offer a direct review action");
expect(page.$("#surla-approval-stage").dataset.runId === input.older && page.$("#surla-cp5-approve-blocked"), "background latest discovery replaced historical evidence");
expect(page.$("#surla-cp5-review-latest").classList.contains("shadcn-btn-primary"), "the failed run's next action does not lead to the latest check");
const oldPath = "/verifications/" + input.older + "/review";
await until(() => !page.$("#surla-cp5-refresh-approval").disabled, "background review did not settle before refresh");
const reads = page.sent("GET", oldPath).length;
page.$("#surla-cp5-refresh-approval").click();
await until(() => page.sent("GET", oldPath).length > reads && !page.$("#surla-cp5-refresh-approval").disabled, "refresh did not reread the displayed failure");
expect(page.$("#surla-approval-stage").dataset.runId === input.older, "refresh silently selected another check");
page.$("#surla-cp5-review-latest").click();
await until(() => page.$("#surla-approval-stage").dataset.runId === input.latest && page.$("#surla-cp5-approve") && !page.$("#surla-cp5-approve").disabled, "latest action did not load the passed run and owner review");
expect(page.doc.activeElement === page.$("#surla-cp5-title"), "latest action did not focus the review destination");
expect(!page.$("#surla-cp5-review-latest"), "latest check still offered itself as newer");
page.$("#surla-cp4-run-" + input.older).click();
await until(() => page.$("#surla-approval-stage").dataset.runId === input.older && page.$("#surla-cp5-approve-blocked"), "earlier failure was no longer selectable");
expect(page.sent("POST", "/changes/approve").length === 0 && page.sent("POST", "/changes/verify").length === 0, "review navigation started work or approved it");
page.close();
`)
}
