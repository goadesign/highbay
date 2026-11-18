// This file drives the views page when the guide revises an agreed change:
// the revision waits beside the agreed one as a proposal, which the page
// shows against the agreed revision and lets the user adopt or decline.
package controlplane

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
)

// TestViewsPageProposalChoicesGuardAReplacementLink protects unadopted choices
// independently of CP1 edits and discards them locally without declining.
func TestViewsPageProposalChoicesGuardAReplacementLink(t *testing.T) {
	ps, _ := proposalPage(t)
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-confirm-submit"), "the agreed card did not open");
expect(!page.beforeUnload(), "a saved agreement unexpectedly blocked leaving");
await control(origin, "act", { key: "propose" });
await until(() => page.$('#surla-proposal [data-option="with_tax"]'), "the proposed question did not arrive");
page.$('#surla-proposal [data-option="with_tax"]').click();
expect(page.beforeUnload(), "an unadopted proposal answer did not guard leaving");
page.win.location.hash = "link=pending-proposal-link";
await until(() => page.$("#surla-pending-link") && page.win.location.hash === "", "proposal choices did not defer the replacement link");
expect(page.$("#surla-open-pending-link").disabled, "unadopted choices allowed the replacement link");
expect(page.text("#surla-pending-link").includes("Adopt or Discard Unsaved Edits") && page.text("#surla-pending-link").includes("without declining"), "the notice did not name the local way to clear proposal answers");
page.$("#surla-discard-draft").click();
await until(() => !page.$("#surla-open-pending-link").disabled, "local discard did not resolve the proposal choices");
expect(!page.beforeUnload() && page.$("#surla-proposal"), "local discard removed the proposal or retained its choices");
expect(page.sent("POST", "/changes/confirm").length === 0 && page.sent("POST", "/changes/decline").length === 0, "local discard changed the saved agreement or proposal");
page.close();
`)
}

// TestViewsPagePendingEditDefersAReplacementLink protects an outstanding save
// even when the submitted values have no local differences from the card.
func TestViewsPagePendingEditDefersAReplacementLink(t *testing.T) {
	ps, _ := proposalPage(t)
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-confirm-submit"), "the agreed card did not open");
expect(!page.beforeUnload(), "a clean card unexpectedly blocked leaving");
const agreement = page.text(".surla-frame-summary");
await control(origin, "hold", { key: "POST /changes/confirm" });
page.$("#surla-confirm-submit").click();
await until(() => page.sent("POST", "/changes/confirm").some((r) => r.status === undefined), "the edit request was not held pending");
page.$("#surla-nav-changes").click();
expect(page.$("#surla-agreement-details") && page.$("#surla-read-full-summary") && page.text(".surla-frame-summary") === agreement, "Describe hid the agreement while protecting a pending save");
page.win.location.hash = "link=pending-save-link";
await until(() => page.$("#surla-pending-link") && page.win.location.hash === "", "an outstanding edit did not defer the link");
expect(page.beforeUnload() && page.$("#surla-open-pending-link").disabled, "the pending edit did not guard both exit paths");
expect(page.text("#surla-pending-link").includes("Wait for the current save to finish"), "the pending-save notice did not tell the user to wait");
await control(origin, "release", { key: "POST /changes/confirm" });
await until(() => !page.$("#surla-open-pending-link").disabled, "a completed save did not release the pending link");
expect(!page.beforeUnload(), "a completed unchanged save still blocked leaving");
page.close();
`)
}

// TestViewsPageAdoptsAndDeclinesAProposedRevision opens an agreed change with
// a Story and a typed edit. The guide's revision appears as a proposal
// without a reload, keeping the edit; the page shows every part that would
// change (the request, summary, a new question, an edited example with what
// it was and what it would become, a removed and an added example, a kept, a
// dropped and a new preserved behavior, an added flow and a changed design
// file), blocks refining the agreed revision, keeps a chosen answer across a
// re-read, lists the proposal, and declines it. A second proposal is adopted:
// it becomes the agreed revision, confirmed by the user, with the earlier
// revision shown as prior and the edit typed on it preserved.
func TestViewsPageAdoptsAndDeclinesAProposedRevision(t *testing.T) {
	ps, agreed := proposalPage(t)
	runPageScript(t, ps, map[string]any{"agreed": agreed}, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
const editor = () => page.$("#surla-cp1-editor");
const outcome = () => page.$('textarea[data-example-id="check_01"][data-field="expected_outcome"]');
const answer = () => page.$('#surla-proposal [data-question-id="chg-proposal-ambiguity-01"] [data-option="with_tax"]');
await until(() => editor().dataset.changeId === "chg-proposal" && page.text("#surla-cp1-editor").includes("Agreed:"), "the agreed change did not open");
const next = () => page.text("#surla-frame-next dd");
page.$("#surla-nav-cp1").click();
expect(next().includes("Review Agreement details"), "clean Agree did not point to the saved agreement");
const agreement = page.text(".surla-frame-summary");
page.type(outcome(), "Typed on the agreed revision");
expect(next().includes("Save or discard your refinements"), "dirty Agree did not point to refinement");

await control(origin, "act", { key: "propose" });
await until(() => page.$("#surla-proposal"), "the guide's revision did not appear without a reload");
expect(next().includes("Adopt or Decline") && page.text(".surla-frame-summary") === agreement, "the proposal did not guide the decision while retaining the current agreement");
expect(page.status().includes("proposed a new revision"), "the proposal was not announced: " + page.status());
expect(editor().dataset.requestRevision === input.agreed && outcome().value === "Typed on the agreed revision", "the proposal replaced the agreed revision or lost a typed edit");
expect(page.$("#surla-proposal-summary").dataset.changed === "true" && page.text("#surla-proposal-summary").includes("exchanges ship a replacement"), "the changed summary is not shown");
const question = page.$('#surla-proposal [data-question-id="chg-proposal-ambiguity-01"]');
expect(question && question.dataset.new === "true" && question.textContent.includes("Does the limit include tax?"), "the new question is not shown");
const added = page.$$('#surla-proposal-story [data-difference="added"] strong').map((s) => s.textContent);
expect(JSON.stringify(added) === JSON.stringify(["Exchange instead of refund"]), "the added flow is not named: " + JSON.stringify(added));
expect(page.$("#surla-proposal-prompt").dataset.changed === "true" && page.text("#surla-proposal-prompt").includes("Refunds up to $60 are instant.") && page.text("#surla-proposal-prompt").includes("Route refunds to a manager."), "the changed request is not shown beside the agreed one");
expect(page.text("#surla-proposal-revision-note") === "Revision note: Raise the instant refund limit to $60 and add exchanges.", "the proposal does not show its revision note: " + page.text("#surla-proposal-revision-note"));
const example = (id) => page.$('#surla-proposal-examples [data-example-id="' + id + '"]');
const edited = example("check_01");
expect(edited && edited.dataset.status === "Edited", "the edited example is not shown as edited");
const was = edited.querySelector(".surla-proposal-example-agreed");
const becomes = edited.querySelector(".surla-proposal-example-proposed");
expect(was && was.textContent.includes("Refund at the limit") && was.textContent.includes("Order total $50.00"), "the edited example does not show what it was: " + edited.textContent);
expect(becomes && becomes.textContent.includes("Refund at the raised limit") && becomes.textContent.includes("Order total $60.00"), "the edited example does not show what it would become: " + edited.textContent);
const removed = example("check_02");
expect(removed && removed.dataset.status === "Removed" && removed.textContent.includes("Refund over the limit"), "the removed example is not listed as removed");
const addedExample = example("check_03");
expect(addedExample && addedExample.dataset.status === "New" && addedExample.textContent.includes("Exchange within the limit"), "the added example is not marked new");
const behaviors = page.$$("#surla-proposal-preserved li").map((b) => [b.textContent.replace(/New$/, "").trim(), b.dataset.new]);
expect(JSON.stringify(behaviors) === JSON.stringify([["Refunds are recorded on the order.", "false"], ["An exchange keeps the original order's tenant.", "true"]]), "the kept and new preserved behaviors are not marked: " + JSON.stringify(behaviors));
expect(page.text("#surla-proposal-dropped-preserved").includes("Cross-tenant order lookups return 404 Not Found."), "the dropped preserved behavior is not shown");
expect(page.$("#surla-proposal-design").dataset.changed === "true" && page.text("#surla-proposal-design").includes("design/design.go"), "the changed design file is not named");
expect(page.$("#surla-confirm-submit").disabled && page.$("#surla-confirm-blocked"), "refining the agreed revision is still offered");
expect(page.$("#surla-proposal-adopt").disabled && page.$("#surla-proposal-adopt-hint"), "Adopt is offered before every question is answered");
checkControl(page, page.$("#surla-proposal-decline"), "Decline");

answer().click();
expect(!page.$("#surla-proposal-adopt").disabled, "answering every question did not enable Adopt");
const reads = page.sent("GET", "/changes/chg-proposal").length;
await control(origin, "act", { key: "resave" });
await until(() => page.sent("GET", "/changes/chg-proposal").length > reads, "the change was not read again");
await sleep(100);
expect(answer().getAttribute("aria-pressed") === "true" && !page.$("#surla-proposal-adopt").disabled, "a re-read lost the chosen answer");

page.$("#surla-nav-changes").click();
await until(() => page.$('#surla-change-list [data-change-id="chg-proposal"] .surla-proposal-badge'), "the change list does not name the waiting proposal");
page.$("#surla-nav-cp1").click();
await until(() => page.$("#surla-proposal-decline"), "returning to the change did not show the proposal");

page.$("#surla-proposal-decline").click();
await until(() => !page.$("#surla-proposal"), "declining did not remove the proposal");
expect(editor().dataset.requestRevision === input.agreed && outcome().value === "Typed on the agreed revision" && !page.$("#surla-confirm-submit").disabled, "declining changed the agreed revision or lost the typed edit");
const declined = await readCard("chg-proposal");
expect(declined.request_revision === input.agreed && !declined.proposed_revision, "the server still holds the declined proposal");

await control(origin, "act", { key: "propose" });
await until(() => page.$("#surla-proposal"), "the second proposal did not appear");
answer().click();
checkControl(page, page.$("#surla-proposal-adopt"), "Adopt");
const proposal = (await readCard("chg-proposal")).proposed_revision;
page.$("#surla-proposal-adopt").click();
await until(() => !page.$("#surla-proposal") && editor().dataset.requestRevision !== input.agreed, "adopting did not make the proposal the agreed revision");
const adoption = page.sent("POST", "/changes/confirm").pop().body;
expect(adoption.request_revision === proposal.request_revision && adoption.answers["chg-proposal-ambiguity-01"] === "with_tax", "Adopt did not confirm the proposal with the chosen answer: " + JSON.stringify(adoption));
const adopted = await readCard("chg-proposal");
expect(adopted.status === "confirmed" && adopted.confirmed_by === input.owner && !adopted.proposed_revision && adopted.summary.includes("exchanges"), "the adopted revision is not in force: " + JSON.stringify(adopted));
expect(editor().dataset.requestRevision === adopted.request_revision && page.$("#surla-prior-revision") && page.text("#surla-prior-revision").includes(input.agreed), "the earlier agreed revision is not shown as prior");
expect(adopted.revision_note === "Raise the instant refund limit to $60 and add exchanges." && page.text("#surla-agreement-revision-note") === "Revision note: " + adopted.revision_note, "the adopted revision does not show its revision note: " + page.text("#surla-agreement-revision-note"));
expect(!page.text("#surla-prior-revision").includes("This is the proposal before confirmation.") && !page.text("#surla-prior-revision").includes("Not yet answered in that proposal"), "confirmed history is described as an unanswered proposal");
expect(page.$("#surla-preserved-draft") && page.text("#surla-preserved-draft").includes("Typed on the agreed revision"), "adopting lost the edit typed on the earlier revision");
page.close();
`)
}

// TestViewsPageKeepsAnswersWhenAdoptOrDeclineIsRefused answers a proposal's
// question, then has the server refuse first the adoption and then the
// decline: each refusal is shown with the server's message and the answer
// the user chose stays selected, with the proposal still waiting.
func TestViewsPageKeepsAnswersWhenAdoptOrDeclineIsRefused(t *testing.T) {
	ps, agreed := proposalPage(t)
	runPageScript(t, ps, map[string]any{"agreed": agreed}, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
const answer = () => page.$('#surla-proposal [data-question-id="chg-proposal-ambiguity-01"] [data-option="with_tax"]');
await until(() => page.$("#surla-cp1-editor").dataset.changeId === "chg-proposal", "the agreed change did not open");
await control(origin, "act", { key: "propose" });
await until(() => page.$("#surla-proposal"), "the proposal did not appear");
answer().click();
const refusal = (message) => ({ name: "conflict", id: "x", message, temporary: false, timeout: false, fault: false });

await control(origin, "reply", { key: "POST /changes/confirm", status: 409, body: refusal("The adoption was refused for this test") });
page.$("#surla-proposal-adopt").click();
await until(() => page.$("#surla-proposal-error") && page.text("#surla-proposal-error").includes("The adoption was refused for this test"), "a refused adoption was not shown");
await sleep(200);
expect(answer().getAttribute("aria-pressed") === "true" && !page.$("#surla-proposal-adopt").disabled, "a refused adoption lost the chosen answer");
expect(page.$("#surla-cp1-editor").dataset.requestRevision === input.agreed && page.$("#surla-proposal"), "a refused adoption changed what is shown in force");

await control(origin, "reply", { key: "POST /changes/decline", status: 409, body: refusal("The decline was refused for this test") });
page.$("#surla-proposal-decline").click();
await until(() => page.text("#surla-proposal-error").includes("The decline was refused for this test"), "a refused decline was not shown");
await sleep(200);
expect(answer().getAttribute("aria-pressed") === "true" && page.$("#surla-proposal"), "a refused decline lost the chosen answer or the proposal");
const saved = await readCard("chg-proposal");
expect(saved.proposed_revision && saved.request_revision === input.agreed, "the refusals changed the saved change");
page.close();
`)
}

// proposalPage serves an agreed change with a Story, two examples (check_01
// and check_02) and two preserved behaviors behind the page session, with two
// server-side actions a script can run: "propose", where the guide revises
// the change from changed design files with an added flow, a new question, a
// new prompt, check_01 edited, check_02 removed, a new example, one preserved
// behavior kept, the other dropped and a new one added; and "resave", which
// saves the change unchanged so the page reads it again. It returns the
// server and the agreed revision.
func proposalPage(t *testing.T) (*pageServer, string) {
	t.Helper()
	cp, ps := storyPageServer(t)
	guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
	user := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: pageUserID, Kind: CallerUser})
	op := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	analysis := refundAnalysis("Route refunds to a manager.")
	analysis.Examples = append(analysis.Examples, &gensurla.CP1AnalysisExample{
		Scenario: "Refund over the limit", InputSummary: "Order total $80.00",
		ExpectedOutcome: "Routed to a manager", VerificationKind: "deterministic",
	})
	analysis.PreservedBehaviors = append(analysis.PreservedBehaviors, "Refunds are recorded on the order.")
	_, err := cp.ProposeChange(guide, &gensurla.ProposeChangePayload{
		ChangeID: "chg-proposal", Prompt: "Route refunds to a manager.", Analysis: analysis, StoryRefreshOpID: &op,
	})
	require.NoError(t, err)
	proposed, err := cp.GetChange(guide, &gensurla.GetChangePayload{ChangeID: "chg-proposal"})
	require.NoError(t, err)
	agreed, err := cp.ConfirmIntent(user, &gensurla.ConfirmIntentPayload{
		ChangeID: proposed.ChangeID, RequestRevision: proposed.RequestRevision,
		Answers: map[string]string{}, Examples: proposed.Examples,
	})
	require.NoError(t, err)
	ps.act("propose", func() {
		draft := pageStories()
		draft[0].Flows = append(draft[0].Flows, designeval.StoryFlow{
			Key: "exchange", Title: "Exchange instead of refund", Complete: true,
			Steps: []designeval.StoryStep{
				{Key: "ask", ActorKey: "customer", Summary: "Customer asks for an exchange", Kind: "start"},
				{Key: "shipped", ActorKey: "customer", Summary: "The replacement ships", Kind: "outcome"},
			},
		})
		wsDir, _, _ := cp.currentWorkspaceTarget()
		writeWorkspaceFiles(t, filepath.Join(wsDir, draftFolderName), map[string]string{"design/design.go": "package design // exchanges\n"})
		storyOp := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, draft, "")
		change, err := cp.GetChange(guide, &gensurla.GetChangePayload{ChangeID: "chg-proposal"})
		require.NoError(t, err)
		latest := change.RequestRevision
		if change.ProposedRevision != nil {
			latest = change.ProposedRevision.RequestRevision
		}
		edited := "check_01"
		_, err = cp.ReviseChange(guide, &gensurla.ReviseChangePayload{
			ChangeID: "chg-proposal", RequestRevision: latest, Prompt: "Refunds up to $60 are instant.", RevisionNote: "Raise the instant refund limit to $60 and add exchanges.", StoryRefreshOpID: &storyOp,
			Analysis: &gensurla.CP1RevisionAnalysis{
				Summary:     "Refunds up to $60 are instant; exchanges ship a replacement.",
				Ambiguities: []*gensurla.CP1AnalysisAmbiguity{refundTaxQuestion()},
				Examples: []*gensurla.CP1RevisionAnalysisExample{
					{
						PriorID: &edited, Scenario: "Refund at the raised limit", InputSummary: "Order total $60.00",
						ExpectedOutcome: "Instant store credit", VerificationKind: "deterministic",
					},
					{
						Scenario: "Exchange within the limit", InputSummary: "Exchange of a $40.00 order",
						ExpectedOutcome: "A replacement ships", VerificationKind: "deterministic",
					},
				},
				RemovedExampleIds:  []string{"check_02"},
				PreservedBehaviors: []string{"Refunds are recorded on the order.", "An exchange keeps the original order's tenant."},
			},
		})
		require.NoError(t, err)
	})
	ps.act("resave", func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		record, ok, err := cp.cards.load("chg-proposal")
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, cp.cards.save(record))
	})
	return ps, agreed.RequestRevision
}
