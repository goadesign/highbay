// This file exercises the confirmed card's Build-first reading order and its
// native refinement disclosure through the served page and generated HTTP API.
package controlplane

import (
	"encoding/json"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/require"
)

// TestViewsPageConfirmedBuildLeadsTheFoldedAgreement keeps the next work ahead
// of the saved decision while retaining editable answers, exact attribution,
// the explicit refinement action and the existing build consequence warning.
// Build and agent processes are fixture boundaries, not execution proof.
func TestViewsPageConfirmedBuildLeadsTheFoldedAgreement(t *testing.T) {
	team := &teamServiceFixture{snapshot: genhighbay.TeamSnapshot{
		Team: "page-team", HerdrMode: "available", Roles: []*genhighbay.TeamRoleState{},
		SavedChoices: map[string]*genhighbay.RoleAgentChoice{}, DismissedNotices: []string{},
	}}
	cp := newBuildControlPlane(t, Options{
		Team: &TeamBinding{Owner: pageOwner, Service: team},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	proposed := saveProposedCardFixture(t, cp, pageOwner, "chg-fold",
		"Route refunds to a manager.", refundAnalysis("A manager reviews refunds.",
			&genhighbay.CP1AnalysisAmbiguity{
				Question: "Does the limit include tax?", Options: []string{"Subtotal only", "With tax"},
			}))
	proposed = attachBuildSnapshot(t, cp, proposed.ChangeID)
	questionID := proposed.Ambiguities[0].ID
	card, err := cp.ConfirmIntent(asCaller(pageOwner, CallerUser), &genhighbay.ConfirmIntentPayload{
		ChangeID: proposed.ChangeID, RequestRevision: proposed.RequestRevision,
		Answers: map[string]string{questionID: "Subtotal only"}, Examples: proposed.Examples,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	ps.act("resave", func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		record, ok, err := cp.cards.load(card.ChangeID)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, cp.cards.save(record))
	})
	ps.act("start-build", func() {
		_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
			ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		})
		require.NoError(t, err)
	})

	wireCard, err := genhighbay.EncodeChangeCardResult(card)
	require.NoError(t, err)
	runPageScript(t, ps, map[string]any{"card": json.RawMessage(wireCard)}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "Build was not ready");
const next = () => page.text("#highbay-frame-next dd");
expect(next().includes("Build controls below") && !page.$("#highbay-frame-next button"), "initial Build did not point to its existing controls");
page.$("#highbay-nav-changes").click();
expect(!page.$("#highbay-agreement-details") && !page.$("#highbay-read-full-summary"), "Describe exposed a hidden editor’s agreement");
expect(!page.$(".highbay-frame-agreed"), "Changes labeled a hidden agreement as open");
page.$("#highbay-nav-build").click();
const editor = page.$("#highbay-cp1-editor");
const build = page.$("#highbay-build-section");
const agreement = page.$("#highbay-agreement-details");
const refinement = page.$("#highbay-refine-agreement");
expect(build.compareDocumentPosition(agreement) & page.win.Node.DOCUMENT_POSITION_FOLLOWING, "the long agreement precedes Build");
expect(agreement.tagName === "DETAILS" && !agreement.open, "saved agreement is not folded in a native disclosure");
expect(agreement.querySelector("summary").textContent === "Agreement details", "agreement disclosure does not name its contents");
expect(agreement.parentElement.querySelector("h2").textContent === "Agreed: " + input.card.prompt, "agreement does not use the user's request");
expect(agreement.textContent.includes(input.card.summary) && agreement.textContent.includes("Subtotal only") && agreement.textContent.includes(input.card.confirmed_by) && agreement.textContent.includes("recorded user identity") && agreement.textContent.includes(input.card.request_revision), "folded agreement lost saved facts or attribution");
expect(refinement.tagName === "DETAILS" && !refinement.open && refinement.querySelector("summary").textContent === "Refine agreed answers and examples", "refinement is not folded behind a named native disclosure");
expect(page.$("#highbay-cp1-form-card").closest("details") === refinement, "the full form still fills the confirmed view");
expect(page.text("#highbay-cp1-form-card").includes("Your answers (1)") && !page.text("#highbay-cp1-form-card").includes("[Confirmed answer:"), "confirmed answers retain open-question or duplicate-answer copy");
expect(page.sent("POST", "/changes/confirm").length === 0, "opening the agreement submitted a refinement");

refinement.open = true;
const field = page.$('textarea[data-field="expected_outcome"]');
field.focus();
field.setSelectionRange(2, 7);
const reads = page.sent("GET", "/changes/chg-fold").length;
await control(origin, "act", { key: "resave" });
await until(() => page.sent("GET", "/changes/chg-fold").some((r, i) => i >= reads && r.status === 200), "the saved card was not refreshed");
await sleep(100);
expect(refinement.open && page.$('textarea[data-field="expected_outcome"]') === field && page.doc.activeElement === field && field.selectionStart === 2 && field.selectionEnd === 7, "refresh collapsed or remounted the focused refinement");

page.type(field, "A manager reviews this refund");
field.setSelectionRange(3, 9);
await control(origin, "act", { key: "start-build" });
await until(() => page.$("#highbay-build-section")?.dataset.state === "started" && !page.$("#highbay-build-loading"), "the background build update did not arrive");
expect(refinement.open && page.$('textarea[data-field="expected_outcome"]') === field && field.value === "A manager reviews this refund" && page.doc.activeElement === field && field.selectionStart === 3 && field.selectionEnd === 9, "Build refresh hid or disturbed the dirty refinement");
expect(page.beforeUnload(), "the folded draft lost its unload protection");
const submit = page.$("#highbay-confirm-submit");
const warning = page.$("#highbay-confirm-build-consequence");
expect(warning && warning.textContent.includes("earlier revision") && submit.getAttribute("aria-describedby") === warning.id, "refinement lost its build consequence warning");
expect(!submit.disabled && submit.textContent.includes("Refine Confirmed Answers & Examples"), "the explicit refinement action is missing");
expect(page.sent("POST", "/changes/confirm").length === 0, "editing or refreshing submitted the draft");
page.$("#highbay-nav-changes").click();
expect(page.text(".highbay-frame-summary") === input.card.summary, "Describe lost its protected editor’s agreement");
const summary = page.$("#highbay-agreement-summary");
// jsdom has no layout or scrolling; native browser coverage owns that proof.
summary.scrollIntoView = () => {};
page.$("#highbay-read-full-summary").click();
expect(agreement.open && page.doc.activeElement === summary && summary.textContent.includes(input.card.summary), "Read full summary did not reveal and focus the complete saved text");
expect(page.$('textarea[data-field="expected_outcome"]') === field && field.value === "A manager reviews this refund" && page.beforeUnload(), "reading the saved summary lost the dirty refinement");
expect(page.sent("POST", "/changes/confirm").length === 0, "reading the saved summary submitted the draft");
submit.click();
await until(() => editor.dataset.requestRevision !== input.card.request_revision, "the explicit refinement did not save");
expect(page.sent("POST", "/changes/confirm").length === 1 && page.sent("POST", "/changes/chg-fold/build").length === 0, "refinement submitted twice or automatically rebuilt");
expect(page.text("#highbay-prior-revision").includes("Confirmed answer: Subtotal only") && !page.text("#highbay-prior-revision").includes("This is the proposal before confirmation."), "refining the agreement changed its earlier confirmed answer into an unanswered proposal");
page.close();
`)
}

// TestViewsPageConfirmedProposalKeepsRefinementClosed lets the proposal stay in
// focus while the untouched agreement's refinement form remains collapsed.
func TestViewsPageConfirmedProposalKeepsRefinementClosed(t *testing.T) {
	ps, agreed := proposalPage(t)
	runPageScript(t, ps, map[string]any{"agreed": agreed}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-refine-agreement"), "the agreed change did not open");
expect(!page.$("#highbay-refine-agreement").open, "an unchanged form was expanded");
await control(origin, "act", { key: "propose" });
await until(() => page.$("#highbay-proposal"), "the proposal did not appear");
expect(!page.$("#highbay-refine-agreement").open, "the proposal opened a clean agreement form");
expect(page.$("#highbay-confirm-submit").disabled && page.$("#highbay-confirm-blocked") && page.$("#highbay-cp1-editor").dataset.requestRevision === input.agreed, "the proposal replaced the agreement or lost its explanation");
page.$("#highbay-proposal-decline").click();
await until(() => !page.$("#highbay-proposal") && !page.$("#highbay-confirm-submit").disabled, "declining did not restore refinement");
expect(!page.$("#highbay-refine-agreement").open, "resolving the proposal opened the untouched refinement form");
page.close();
`)
}

// TestViewsPageUnconfirmedIntentKeepsItsFullEditableForm preserves the first
// confirmation journey: answers, examples and submission stay directly visible.
func TestViewsPageUnconfirmedIntentKeepsItsFullEditableForm(t *testing.T) {
	ps := startPageServer(t, pageControlPlane(t, Options{}))
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const card = await propose("chg-unconfirmed-fold", "Route refunds to a manager.", "Auto-approved");
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-form-card"), "the proposed card did not open");
expect(!page.$("#highbay-refine-agreement") && !page.$("#highbay-agreement-details") && !page.$("#highbay-cp1-form-card").closest("details"), "the first confirmation form was folded");
expect(page.text("#highbay-cp1-form-card").includes("Open Questions (1)"), "the unconfirmed questions changed meaning");
expect(page.text("#highbay-example-check-labels").includes("Check labels guide test writing") &&
       page.text("#highbay-example-check-labels").includes("Starter tests use a scripted model and need no API key") &&
       page.text("#highbay-example-check-labels").includes("Live target"), "the form did not distinguish test-writing labels, starter tests and live trials");
expect(!page.$("#highbay-read-full-summary"), "an unconfirmed proposal offers an agreed summary");
page.$('[data-ambiguity-id="chg-unconfirmed-fold-ambiguity-01"][data-option="subtotal_only"]').click();
const field = page.$('textarea[data-field="expected_outcome"]');
page.type(field, "Manager approval");
checkControl(page, page.$("#highbay-confirm-submit"), "confirmation");
expect(page.text("#highbay-confirm-submit").includes("Confirm Intent & Lock Examples"), "initial confirmation is missing");
page.$("#highbay-confirm-submit").click();
await until(() => page.$("#highbay-agreement-details"), "confirmation did not show the agreed card");
const sent = page.sent("POST", "/changes/confirm");
expect(sent.length === 1 && sent[0].body.answers[card.ambiguities[0].id] === "subtotal_only" && sent[0].body.examples[0].expected_outcome === "Manager approval", "confirmation lost the chosen answer or edited example");
const prior = page.text("#highbay-prior-revision");
const priorDetails = page.$("#highbay-prior-revision");
expect(!priorDetails.open && priorDetails.querySelector("summary").textContent.includes("Previous unconfirmed draft") &&
       priorDetails.querySelector("summary code").textContent === card.request_revision.slice(0, 12),
       "the previous draft was not collapsed under its short revision");
expect(prior.includes(card.request_revision) && prior.includes("This is the proposal before confirmation. Your confirmed answers are saved in the current agreement.") && prior.includes("Not yet answered in that proposal (options: subtotal_only, total_with_tax)") && !prior.includes("Confirmed answer:"), "confirmation history misrepresents the earlier unanswered proposal");
expect(page.text("#highbay-confirmed-decisions").includes("subtotal_only"), "the current agreement lost its confirmed answer");
page.close();
`)
}
