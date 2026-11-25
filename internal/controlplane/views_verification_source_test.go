// This file tests the served Proof page's test-source disclosure. Runs are
// explicit generated-service fixtures, so these checks prove presentation of
// saved results without claiming that candidate verification ran.
package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

// TestViewsPageVerificationTestSource keeps the accepted-test notice visible
// in every run state and preserves the operator evaluator's accurate labels.
func TestViewsPageVerificationTestSource(t *testing.T) {
	const suiteCommit = "2222222222222222222222222222222222222222"
	sources := []struct {
		name         string
		source       genhighbay.TestSource
		commitLabel  string
		bindingLabel string
		suiteLabel   string
		notice       string
	}{
		{
			name: "written_tests",
			source: genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{
				Commit: suiteCommit, Notice: writtenTestsNotice,
			}),
			commitLabel: "Accepted tests commit", bindingLabel: "Tests' commit", suiteLabel: "Written tests suite",
			notice: writtenTestsNotice,
		},
		{
			name:        "evaluator",
			source:      genhighbay.NewTestSourceEvaluator(&genhighbay.OperatorEvaluatorTests{}),
			commitLabel: "Evaluator commit", bindingLabel: "Evaluator commit", suiteLabel: "Evaluator suite",
		},
	}
	states := []struct {
		name    string
		state   genhighbay.State
		heading string
	}{
		{
			name: "running",
			state: genhighbay.NewStateRunning(&genhighbay.VerificationRunning{
				StartedAt: "2026-10-31T12:34:56Z",
			}),
			heading: "The check is running",
		},
		{
			name: "passed",
			state: genhighbay.NewStateCompleted(&genhighbay.VerificationCompleted{
				SuiteID: "accepted_checks", Environment: "sim",
				Report: &genhighbay.BuilderVerificationReport{
					ReportDigest: strings.Repeat("3", 64), Passed: true,
					Counts: &genhighbay.VerificationCounts{Required: 1, Passed: 1},
					Scenarios: []*genhighbay.VerificationScenario{
						{ID: "holdout-1", Outcome: "passed"},
					},
				},
			}),
			heading: "The check passed: 1 of 1 scenarios.",
		},
		{
			name: "not_completed",
			state: genhighbay.NewStateNotCompleted(&genhighbay.VerificationNotCompleted{
				Kind: "execution_failed", Stage: "suite", Summary: "The checks could not finish.",
			}),
			heading: "The check could not finish",
		},
	}
	for _, source := range sources {
		for _, state := range states {
			t.Run(source.name+"/"+state.name, func(t *testing.T) {
				cp := pageControlPlane(t, Options{})
				guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
				user := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: pageUserID, Kind: CallerUser})
				proposed := mustPropose(t, cp, guide, "chg-proof-source", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
				card, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
					ChangeID: proposed.ChangeID, RequestRevision: proposed.RequestRevision,
					Answers: map[string]string{}, Examples: proposed.Examples,
				})
				require.NoError(t, err)
				run := &genhighbay.VerificationStatusResult{
					RunID: "11111111-1111-4111-8111-111111111111", ChangeID: card.ChangeID,
					RequestRevision: card.RequestRevision, CandidateCommit: strings.Repeat("1", 40),
					SuiteCommit: suiteCommit, AcceptedAt: "2026-10-31T12:34:56Z",
					TestSource: source.source, State: state.state,
					// These saved source-disclosure fixtures do not claim a current selection.
					InputsCurrent: false,
				}
				svc := newPageVerifications(cp)
				svc.set(run)
				svc.list(card.ChangeID, run.RunID)
				ps := startPageServer(t, svc)
				runPageScript(t, ps, map[string]any{
					"change": card.ChangeID, "run": run.RunID, "state": state.name,
					"heading": state.heading, "notice": source.notice,
					"commitLabel": source.commitLabel, "bindingLabel": source.bindingLabel, "suiteLabel": source.suiteLabel,
					"suiteCommit": suiteCommit,
				}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === input.change, "the confirmed change did not open");
page.$("#highbay-nav-cp4").click();
await until(() => page.$("#highbay-result-view").dataset.runId === input.run, "the saved run did not open");
expect(page.text("#highbay-cp4-title") === input.heading, "the run's status changed");
expect(page.text("#highbay-cp4-started").startsWith("Check started ") && !page.text("#highbay-cp4-started").includes("finished"), "request acceptance was mislabeled as completion");
const binding = page.text("#highbay-cp4-test-binding").replaceAll("’", "'");
const shortCommit = binding.match(/\b[a-f0-9]{7,40}\b/i)?.[0];
expect(binding.includes(input.bindingLabel) && shortCommit && shortCommit.length < input.suiteCommit.length && input.suiteCommit.startsWith(shortCommit), "the selected run lacks a source-qualified short suite commit");
expect(!/\breport \d+\b/i.test(binding), "a run without frozen coverage acquired a test report number");
const notice = page.$("#highbay-cp4-test-source");
if (input.notice) {
  expect(notice && notice.tagName === "P" && !notice.getAttribute("role") && notice.textContent === input.notice, "the server's written-test notice is missing or changed");
  expect(!notice.closest("details") && !notice.hidden, "the source notice is hidden behind a disclosure");
  expect(!page.text("#highbay-cp4-details").includes("Evaluator"), "written tests are labeled as the evaluator");
} else {
  expect(!notice && !page.text("#highbay-cp4-verification").includes("not isolated from the builder"), "the evaluator acquired a written-test notice");
  expect(!page.text("#highbay-cp4-details").includes("Accepted tests"), "the evaluator acquired an accepted-test label");
}
const fields = [...page.$$("#highbay-cp4-details dt")];
const commit = fields.find((field) => field.textContent === input.commitLabel);
expect(commit && commit.nextElementSibling.textContent === input.suiteCommit, "the source commit label or value is wrong");
if (input.state === "passed") {
  const suite = fields.find((field) => field.textContent === input.suiteLabel);
  expect(suite && suite.nextElementSibling.textContent === "accepted_checks", "the source suite label or value is wrong");
  expect(page.text("#highbay-cp4-verification").includes("holdout-1"), "the anonymous scenario outcome disappeared");
  expect(!page.$(".highbay-verification-counts"), "a passing report repeated the outcome counts");
} else if (input.state === "not_completed") {
  expect(page.text("#highbay-cp4-verification").includes("Stopped during: Running the checks."), "the stopped stage misrepresents the checks");
}
expect(page.sent("POST", "/changes/" + input.change + "/verifications").length === 0, "reading proof started verification");
page.close();
`)
			})
		}
	}
}

// TestViewsPageEmptyVerificationNamesBuilder explains who starts checks instead
// of directing a browser user to an operation reserved for the builder.
func TestViewsPageEmptyVerificationNamesBuilder(t *testing.T) {
	cp := pageControlPlane(t, Options{})
	guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
	mustPropose(t, cp, guide, "chg-empty-proof", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
	ps := startPageServer(t, newPageVerifications(cp))
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-empty-proof", "the change did not open");
page.$("#highbay-nav-cp4").click();
await until(() => page.$("#highbay-cp4-empty"), "the empty proof state did not appear");
expect(page.text("#highbay-cp4-empty").includes("Once the tests are accepted, the builder finishes the code and asks Highbay to check it"), "the empty state does not explain who starts checks");
expect(!page.text("#highbay-cp4-empty").includes("verify_change"), "the browser user is directed to the builder-only operation");
page.close();
`)
}
