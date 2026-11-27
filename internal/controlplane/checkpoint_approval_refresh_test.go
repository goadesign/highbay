// This file tests the views page's approval check: reading the card again
// shows whether a recorded approval still matches the promotion target,
// without discarding the user's edits, and a failed read stays visible.
package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

// TestCheckpointUIRefreshesApprovalWithoutDiscardingEdits drives the page's
// "Check current approval" control through real get_change requests:
// matching inputs keep the approval and the draft, changed inputs remove it
// while keeping the draft, and a failed read keeps both and can be retried.
func TestCheckpointUIRefreshesApprovalWithoutDiscardingEdits(t *testing.T) {
	spec := reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: strings.Repeat("2", 40),
		Environment: "sim", Artifacts: map[string]string{"orders": strings.Repeat("a", 64)},
	}
	target := &pageTarget{artifacts: spec.Artifacts}
	cp := pageControlPlane(t, buildFixtureOptions(t, Options{ResolvePromotionTarget: target.resolve}))
	user := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: pageUserID, Kind: CallerUser})
	proposed := saveProposedCardFixture(t, cp, pageOwner, "approval-refresh", "Refunds above $50 require a manager.", refundAnalysis("Refunds above $50 require a manager."))
	proposed = attachBuildSnapshot(t, cp, proposed.ChangeID)
	confirmed, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
		ChangeID: proposed.ChangeID, RequestRevision: proposed.RequestRevision,
		Answers: map[string]string{}, Examples: proposed.Examples,
	})
	require.NoError(t, err)
	saveStoredBuildFixture(t, cp, confirmed)
	commit := commitBuilderCandidateFixture(t, cp, confirmed)
	report := passingReport(t, confirmed.RequestRevision, commit, spec)
	target.update(func(p *pageTarget) { p.report = report })
	_, err = cp.ApproveMerge(user, &genhighbay.ApproveMergePayload{
		ChangeID: confirmed.ChangeID, RequestRevision: confirmed.RequestRevision,
		CandidateCommit: commit, ReportDigest: report.Digest,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	ps.act("inputs-changed", func() {
		target.update(func(p *pageTarget) {
			p.artifacts = map[string]string{"orders": strings.Repeat("c", 64)}
		})
	})
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-nav-cp1"), "the change navigation did not appear");
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-review-approval"), "the recorded approval summary is not shown");
const field = () => page.$('textarea[data-field="expected_outcome"]');
const reads = () => page.sent("GET", "/changes/approval-refresh");
page.$("#highbay-refine-agreement").open = true;
page.type(field(), "Keep this draft");
page.$("#highbay-review-approval").click();
await until(() => page.$("#highbay-cp5-approval"), "the saved approval did not open");
const before = reads().length;
page.$("#highbay-cp5-refresh-approval").click();
await until(() => reads().length === before + 1 && !page.$("#highbay-cp5-refresh-approval").disabled, "checking the approval did not read the card once");
expect(page.$("#highbay-cp5-approval"), "matching inputs removed the approval");
expect(!page.$("#highbay-cp1-form-card"), "Approve did not open a full-width stage");
expect(!page.doc.body.textContent.includes("Approved for promotion"), "recorded snapshot claims continuing authority");

// A failed read is visible, keeps the record and the draft, and can be retried.
await control(origin, "reply", { key: "GET /changes/approval-refresh", status: 500, body: { name: "fault", id: "x", message: "Target inputs are unavailable", temporary: false, timeout: false, fault: true } });
page.$("#highbay-cp5-refresh-approval").click();
await until(() => page.$("#highbay-cp5-read-error"), "the failed read is not visible");
expect(page.text("#highbay-cp5-read-error").includes("Target inputs are unavailable"), "the server's error is not shown");
expect(page.$("#highbay-cp5-approval"), "a failed read discarded the recorded check");
expect(page.$("#highbay-cp5-approval"), "a failed read erased the saved record");

// After the target's inputs change, retrying removes the approval and keeps the draft.
await control(origin, "act", { key: "inputs-changed" });
const beforeRetry = reads().length;
page.$("#highbay-cp5-refresh-approval").click();
await until(() => reads().length === beforeRetry + 1 && !page.$("#highbay-cp5-approval"), "retrying did not read the card and drop the stale approval");
page.$("#highbay-nav-cp1").click();
expect(field().value === "Keep this draft", "the retried read erased the draft");
await sleep(200);
expect(reads().length === beforeRetry + 1, "the approval check looped");
page.close();
`)
}
