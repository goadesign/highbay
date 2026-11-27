// This file drives the views page through the requests it sends to the real
// generated HTTP API: finding work, opening and confirming a change, staying
// current while the user types, and the diagnostics an idle page leaves.
package controlplane

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

// viewsPageScriptHelpers defines JavaScript helpers the page tests share:
// proposal payloads and the guide's direct calls.
const viewsPageScriptHelpers = `
function analysis(summary, outcome) {
  return {
    summary,
    ambiguities: [{ question: "Does the $50 limit include tax?", options: ["subtotal_only", "total_with_tax"] }],
    examples: [{ scenario: "Refund under $50", input_summary: "$40 subtotal", expected_outcome: outcome, verification_kind: "deterministic" }],
    preserved_behaviors: ["Cross-tenant lookups return 404."],
  };
}
async function propose(changeId, summary, outcome) {
  const out = await api(origin, "POST", "/changes/propose", { change_id: changeId, prompt: summary, analysis: analysis(summary, outcome) }, input.guide);
  expect(out.status === 200, "guide could not propose " + changeId + ": " + JSON.stringify(out));
  return out.body;
}
async function revise(card, summary, outcome) {
  const out = await api(origin, "POST", "/changes/revise", {
    change_id: card.change_id, request_revision: card.request_revision, prompt: summary, revision_note: "Revise the change.",
    analysis: { ...analysis(summary, outcome), examples: [{ prior_id: card.examples[0].id, scenario: "Refund under $50", input_summary: "$40 subtotal", expected_outcome: outcome, verification_kind: "deterministic" }], removed_example_ids: [] },
  }, input.guide);
  expect(out.status === 200, "guide could not revise " + card.change_id + ": " + JSON.stringify(out));
  return out.body;
}
// checkControl checks that a control is a native, focusable, enabled button
// with an accessible name, so it works from the keyboard and is announced.
function checkControl(page, element, label) {
  expect(element, label + " is missing");
  expect(element.tagName === "BUTTON" && element.type === "button", label + " is not a native button");
  expect(!element.disabled && element.tabIndex >= 0, label + " is not reachable by keyboard");
  const name = (element.getAttribute("aria-label") || element.textContent || "").trim();
  expect(name.length > 0, label + " has no accessible name");
  element.focus();
  expect(page.doc.activeElement === element, label + " does not take keyboard focus");
}
async function readCard(changeId) {
  const out = await api(origin, "GET", "/changes/" + encodeURIComponent(changeId), undefined, input.guide);
  expect(out.status === 200, "guide could not read " + changeId);
  return out.body;
}
`

// TestViewsPagePolicyAllowsOnlyTheServedScript checks the served page against
// the policy sent with it: the page as an HTML parser reads it holds exactly
// one script, and the policy allows scripts only by the SHA-256 digest of that
// script's text, computed here from the served page rather than taken from
// the build.
func TestViewsPagePolicyAllowsOnlyTheServedScript(t *testing.T) {
	ps := startPageServer(t, pageControlPlane(t, Options{}))
	runPageScript(t, ps, nil, `
const crypto = require("crypto");
const page = await openPage(origin);
const scripts = page.$$("script");
expect(scripts.length === 1, "the page holds " + scripts.length + " scripts");
const digest = crypto.createHash("sha256").update(scripts[0].textContent, "utf8").digest("base64");
const directives = page.headers.get("content-security-policy").split(";").map((d) => d.trim());
const scriptPolicy = directives.filter((d) => d.startsWith("script-src"));
expect(JSON.stringify(scriptPolicy) === JSON.stringify(["script-src 'sha256-" + digest + "'"]), "the script policy is not the served script's digest alone: " + JSON.stringify(scriptPolicy));
page.close();
`)
}

// TestViewsPageFindsOpensAndConfirmsChanges walks the user's journey: the
// empty project explains how a change appears, the first proposal opens by
// itself, the list orders changes by save time and opens one, and confirming
// records the user's session as who confirmed.
func TestViewsPageFindsOpensAndConfirmsChanges(t *testing.T) {
	cp := pageControlPlane(t, Options{})
	ps := startPageServer(t, cp)
	resolving, generating := "Resolving the service's Go modules", "Generating the service's code"
	ps.act("resolving", func() { cp.initPhase.Store(&resolving) })
	ps.act("generating", func() { cp.initPhase.Store(&generating) })
	ps.act("initialized", func() { cp.initPhase.Store(nil) })
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-change-list-empty"), "empty project did not show its empty state");
await until(() => page.text("#highbay-change-list-title") === "Describe what you want to build" &&
       page.text("#highbay-change-list-empty").includes("Your Change Card will appear here automatically."),
       "empty state did not say where to start and how a change appears: " + page.text("#highbay-change-list-title") + " / " + page.text("#highbay-change-list-empty"));
expect(page.win.location.href === origin + "/views", "the link stayed in the address: " + page.win.location.href);

const phaseStatus = page.$("#highbay-change-list-empty");
await control(origin, "act", { key: "resolving" });
await until(() => phaseStatus.textContent === "Resolving the service's Go modules", "the waiting page did not show initializer progress");
expect(phaseStatus.getAttribute("role") === "status" && !page.$(".highbay-frame-slots") &&
       page.text("#highbay-change-list-title") === "Preparing your project", "initialization still prompted project setup or was not announced");
await control(origin, "act", { key: "generating" });
await until(() => phaseStatus.textContent === "Generating the service's code", "the next phase did not update the same live status");
await control(origin, "act", { key: "initialized" });
await until(() => phaseStatus.textContent.includes("Your Change Card will appear here automatically."), "completion did not restore the welcome card");
expect(!phaseStatus.hasAttribute("role") && page.sent("POST", "/project/browser-session").length === 1, "clearing initialization replaced the current browser session");

await propose("chg-first", "Route refunds to a manager.", "Auto-approved");
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-first", "the first proposal did not open without a reload");
page.$("#highbay-nav-changes").click();
await propose("chg-second", "Hold refunds for review.", "Held");
await until(() => page.$$("#highbay-change-list li").length === 2, "a second change did not appear");
const ids = page.$$("#highbay-change-list li").map((li) => li.dataset.changeId);
expect(JSON.stringify(ids) === JSON.stringify(["chg-second", "chg-first"]), "list is not most recently saved first: " + ids);
expect(page.text("#highbay-change-list").includes("Needs confirmation") && page.text("#highbay-change-list").includes("No saved runs"), "list omitted state or saved-run history");

checkControl(page, page.$("#highbay-open-change-chg-first"), "list control");
const openFirst = page.$("#highbay-open-change-chg-first");
expect(openFirst.textContent === "Open: Route refunds to a manager." &&
       openFirst.getAttribute("aria-label") === openFirst.textContent + " (chg-first)",
       "the list control's accessible name omitted its visible label or change ID");
page.$("#highbay-open-change-chg-first").click();
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-first", "opening a listed change did not show its card");
const answer = page.$('[data-ambiguity-id="chg-first-ambiguity-01"][data-option="subtotal_only"]');
checkControl(page, page.$("#highbay-cp1-next-question"), "jump to unanswered question");
page.$("#highbay-cp1-next-question").click();
expect(page.doc.activeElement === answer, "Next did not focus the first unanswered option");
answer.click();
const example = page.$('textarea[data-field="expected_outcome"]');
const originalOutcome = example.value;
page.type(example, " ");
checkControl(page, page.$("#highbay-confirm-submit"), "confirm control");
page.$("#highbay-confirm-submit").click();
expect(page.doc.activeElement === example && page.text("#highbay-confirm-error").includes("expected result for example 1") &&
       page.sent("POST", "/changes/confirm").length === 0, "invalid example did not focus its field and preserve the draft");
page.type(example, originalOutcome);
checkControl(page, page.$("#highbay-cp1-next-confirm"), "reachable confirm control");
await control(origin, "hold", { key: "POST /changes/confirm" });
page.$("#highbay-cp1-next-confirm").click();
expect(page.$("#highbay-cp1-next-confirm").disabled && page.$("#highbay-confirm-submit").disabled &&
       page.$("#highbay-cp1-form-card").getAttribute("aria-busy") === "true", "confirming showed no busy state");
await control(origin, "release", { key: "POST /changes/confirm" });
await until(() => page.$("#highbay-agreement-details") &&
                  page.text("#highbay-agreement-summary").includes("Route refunds to a manager."),
                  "confirming did not show the saved agreement");
const confirmed = await readCard("chg-first");
expect(confirmed.status === "confirmed" && confirmed.confirmed_by === input.owner, "the saved change does not record the user as who confirmed: " + confirmed.confirmed_by);
expect(page.doc.activeElement === page.$("#highbay-cp1-editor"), "confirming did not move focus to the result");
page.close();
`)
}

// TestViewsPageWaitsBeforeReadingAFailedListAgain makes the server announce a
// change and then refuse the list read that follows: the page shows the
// failure, does not ask for the list again at once, and reads it after its
// retry wait.
func TestViewsPageWaitsBeforeReadingAFailedListAgain(t *testing.T) {
	cp := pageControlPlane(t, Options{})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-change-list-empty"), "empty project did not show its empty state");
await control(origin, "reply", { key: "GET /changes", status: 500, body: { name: "fault", id: "x", message: "Saved change cards could not be read", temporary: false, timeout: false, fault: true } });
await propose("chg-first", "Route refunds to a manager.", "Auto-approved");
await until(() => page.$("#highbay-change-list-error"), "a refused list read was not shown on the list");
expect(page.text("#highbay-change-list-error").includes("Saved change cards could not be read"), "the list error did not carry the server's message");
await until(() => page.status().includes("Could not read this project's changes; trying again."), "the status line did not say the page is retrying: " + page.status());
const reads = page.sent("GET", "/changes").length;
await sleep(2500);
expect(page.sent("GET", "/changes").length === reads, "the page asked for the list again at once: " + (page.sent("GET", "/changes").length - reads) + " more reads");
await until(() => page.$$("#highbay-change-list li").length === 1, "the list was not read again after the retry wait", 8000);
expect(!page.$("#highbay-change-list-error"), "the list error stayed after the list was read");
await until(() => page.status() === "This page is up to date with the Highbay server again.", "the status line did not say the page recovered: " + page.status());
page.close();
`)
}

// TestViewsPageOpensTheOnlyChangeDirectly opens a project with one change on
// that change rather than on the list.
func TestViewsPageOpensTheOnlyChangeDirectly(t *testing.T) {
	cp := pageControlPlane(t, Options{})
	guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
	mustPropose(t, cp, guide, "chg-only", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-only", "the only change did not open directly");
expect(page.state() === "cp1-change-card" && !page.$("#highbay-change-list"), "the page showed the list instead of the only change");
expect(page.$("#highbay-nav-changes") && page.$("#highbay-nav-cp1").getAttribute("aria-current") === "step", "stage navigation does not offer the list and mark the open stage");
page.$("#highbay-nav-changes").click();
expect(page.$$("#highbay-change-list li").length === 1 && page.$("#highbay-open-change-chg-only").getAttribute("aria-current") === "true", "the list does not mark the open change");
page.close();
`)
}

// TestViewsPageKeepsTypingWhenAnotherCallerSavesARevision checks that a
// revision another caller saves appears without a reload while typed edits,
// the cursor and focus survive, that the page announces it once, and that
// leaving with unsaved edits asks the browser to confirm.
func TestViewsPageKeepsTypingWhenAnotherCallerSavesARevision(t *testing.T) {
	cp := pageControlPlane(t, Options{})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const card = await propose("chg-live", "Route refunds to a manager.", "Auto-approved");
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-live", "the change did not open");
const next = () => page.text("#highbay-frame-next dd");
const journey = page.$$('[aria-label="Change journey"] button').map((button) => button.textContent);
expect(journey.slice(-5).join(", ") === "Agree, Architecture, Build, Check, Approve", "wrong change journey: " + journey);
expect(page.$("#highbay-cp1-next-question"), "Agree did not guide the user to the unanswered question");
page.$("#highbay-nav-build").click();
expect(page.$("#highbay-nav-build").getAttribute("aria-current") === "step" && page.$("#highbay-confirm-submit") && !page.$("#highbay-build-section"), "unconfirmed Build did not expose the existing confirmation form");
expect(next().includes("then confirm the change"), "unconfirmed Build pointed to controls it did not have");
await control(origin, "hold", { key: "POST /changes/preview" });
page.$("#highbay-nav-cp2").click();
expect(page.$("#highbay-nav-cp2").getAttribute("aria-current") === "step" &&
       page.$("#highbay-cp2-loading") && next().includes("Wait for Highbay to evaluate the design"), "Architecture did not guide the pending preview");
await control(origin, "release", { key: "POST /changes/preview" });
await until(() => page.$("#highbay-cp2-error"), "Architecture did not show its refusal");
expect(next().includes("Open Agree to confirm the change"), "Architecture did not guide recovery from an unconfirmed request");
page.$("#highbay-nav-cp1").click();
expect(!page.beforeUnload(), "the page asked to confirm leaving without unsaved edits");
const field = page.$('textarea[data-field="expected_outcome"]');
field.focus();
page.type(field, "Typed but not saved");
field.setSelectionRange(5, 9);
expect(page.beforeUnload(), "leaving with unsaved edits did not ask the browser to confirm");
const announced = [];
new page.win.MutationObserver(() => announced.push(page.status())).observe(page.$("#highbay-checkpoint-status"), { childList: true, characterData: true, subtree: true });

await revise(card, "Route refunds over $50 to a manager.", "Manager approval");
await until(() => page.$("#highbay-replace-incoming-card"), "a revision saved by another caller did not appear without a reload");
expect(next().includes("Switch to Incoming Card"), "the incoming revision did not name its existing review action");
expect(page.$('textarea[data-field="expected_outcome"]') === field && field.value === "Typed but not saved", "the new revision discarded typed text");
expect(page.doc.activeElement === field, "the new revision moved focus");
expect(field.selectionStart === 5 && field.selectionEnd === 9, "the new revision moved the cursor");
expect(page.$("#highbay-cp1-editor").dataset.requestRevision === card.request_revision, "the editor left the revision being edited");

const listReads = page.sent("GET", "/changes").length;
await propose("chg-unrelated", "Show carriers on receipts.", "Carrier shown");
await until(() => page.sent("GET", "/changes").length > listReads, "another saved change did not reach the page");
await sleep(100);
const told = announced.filter((text) => text.includes("newer revision"));
expect(told.length === 1, "the new revision was announced " + told.length + " times: " + JSON.stringify(announced));
expect(page.doc.activeElement === field && field.value === "Typed but not saved", "a later list change disturbed the editor");
expect(page.sent("GET", "/changes/chg-unrelated").length === 0, "the page read a change the list did not ask it to");
page.close();
`)
}

// TestViewsPageRetriesTheLatestOpenCard repairs a failed refresh through the
// current editor's read, including after returning to the change list.
func TestViewsPageRetriesTheLatestOpenCard(t *testing.T) {
	ps := startPageServer(t, pageControlPlane(t, Options{}))
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const card = await propose("chg-card-retry", "Route refunds to a manager.", "Auto-approved");
const path = "/changes/" + card.change_id;
const page = await openPage(origin, { link: input.link });
const next = () => page.text("#highbay-frame-next dd");
const failRead = () => control(origin, "reply", { key: "GET " + path, status: 503, body: { name: "unavailable", message: "Card unavailable." } });
await until(() => page.$("#highbay-cp1-editor").dataset.requestRevision === card.request_revision, "the original card did not open");
await failRead();
const latest = await revise(card, "Route refunds above $50 to a manager.", "Manager approval");
await until(() => next().includes("Load Latest Card"), "failed refresh did not offer a read of the open card");
expect(page.text(".highbay-frame-attention dd") === "The latest saved Change Card could not be read.", "failed refresh was reported as an unopened card");
page.$("#highbay-nav-changes").click();
expect(page.$("#highbay-open-change-" + card.change_id).getAttribute("aria-current") === "true", "the list lost the already-open card");
checkControl(page, page.$("#highbay-frame-next button"), "Load Latest Card in Describe");
page.$("#highbay-nav-cp1").click();
await failRead();
const retry = page.$("#highbay-frame-next button");
checkControl(page, retry, "Load Latest Card in Agree");
retry.click();
await until(() => page.$("#highbay-load-latest-card") && next().includes("Choose Load Latest Card"), "explicit refresh failure did not guide the editor's existing recovery");
expect(!page.$("#highbay-frame-next button"), "the frame duplicated the visible recovery control");
page.$("#highbay-load-latest-card").click();
await until(() => page.$("#highbay-cp1-editor").dataset.requestRevision === latest.request_revision, "the recovery did not load the latest card");
expect(!page.$("#highbay-load-latest-card") && page.$("#highbay-cp1-next-question"), "successful recovery did not restore confirmation guidance");
expect(page.sent("GET", path).filter((read) => read.status === 503).length === 2, "the recovery did not retry both failed reads");
page.close();
`)
}

// TestViewsPageIdleAddsNothingToDiagnostics keeps a page open with nothing
// changing across several waiting reads and checks that the diagnostics
// history did not grow, while each waiting read was still traced.
func TestViewsPageIdleAddsNothingToDiagnostics(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	cp := pageControlPlane(t, Options{TracerProvider: provider})
	guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
	mustPropose(t, cp, guide, "chg-idle", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-idle", "the change did not open");
await until(() => page.sent("GET", "/change-updates").length >= 1, "the page did not keep a waiting read open");
const history = async () => {
  const out = await api(origin, "GET", "/traces?status=all&limit=100", undefined, input.guide);
  expect(out.status === 200, "diagnostics could not be read: " + JSON.stringify(out));
  return out.body.spans;
};
const before = await history();
const waits = page.sent("GET", "/change-updates").length;
await until(() => page.sent("GET", "/change-updates").length >= waits + 3, "the idle page stopped waiting", 15000);
const after = await history();
const added = after.slice(0, after.length - before.length).map((span) => span.name).sort();
expect(JSON.stringify(added) === JSON.stringify(["highbay.controlplane.list_traces", "highbay.request.list_traces"]), "an idle page added diagnostics beyond the first history read: " + JSON.stringify(added));
expect(!after.some((span) => span.name.includes("wait_for_changes")), "a waiting read was saved in the diagnostics history");
expect(page.sent("GET", "/changes").length === 1 && page.sent("GET", "/changes/chg-idle").length === 1, "an idle page read the list or the change again");
page.setHidden(true);
const hiddenWaits = page.sent("GET", "/change-updates").length;
await sleep(1200);
expect(page.sent("GET", "/change-updates").length === hiddenWaits, "a hidden tab kept asking for changes");
page.setHidden(false);
await until(() => page.sent("GET", "/change-updates").length > hiddenWaits, "showing the tab again did not resume waiting");
page.close();
`)
	exported := map[string]int{}
	for _, span := range exporter.GetSpans() {
		exported[span.Name]++
	}
	assert.GreaterOrEqual(t, exported["highbay.request.wait_for_changes"], 3, "each waiting read keeps its exported request span")
	assert.GreaterOrEqual(t, exported[waitForChangesSpan], 3, "each waiting read keeps its exported domain span")
}

// TestViewsPageRefreshesAddNothingToDiagnostics changes the value the
// page's waiting read compares five times: three revisions of the open change
// and, with the Live target view shown, two new changes. Each change makes
// the page read the list again and then the open change or the live target.
// None of these successful reads by the user's session enters the
// diagnostics history, while the guide's changes do and every read keeps its
// exported spans.
func TestViewsPageRefreshesAddNothingToDiagnostics(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	cp := pageControlPlane(t, Options{TracerProvider: provider})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const history = async () => {
  const out = await api(origin, "GET", "/traces?status=all&limit=100", undefined, input.guide);
  expect(out.status === 200, "diagnostics could not be read: " + JSON.stringify(out));
  return out.body.spans;
};
const answered = (page, path) => page.sent("GET", path).filter((r) => r.status === 200).length;
let card = await propose("chg-refresh", "Route refunds to a manager.", "Auto-approved");
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-refresh", "the change did not open");
await until(() => page.sent("GET", "/change-updates").length >= 1, "the page did not keep a waiting read open");
const before = new Set((await history()).map((span) => span.span_id));
const lists = answered(page, "/changes");
const cards = answered(page, "/changes/chg-refresh");
for (const outcome of ["Manager approval", "Manager approval within a day", "Manager approval within an hour"]) {
  const reads = answered(page, "/changes/chg-refresh");
  card = await revise(card, "Route refunds over $50 to a manager.", outcome);
  await until(() => answered(page, "/changes/chg-refresh") > reads && page.$("#highbay-cp1-editor").dataset.requestRevision === card.request_revision, "the page did not read the revised change again");
}
page.$("#highbay-nav-live").click();
await until(() => page.$("#highbay-live-target") && page.$("#highbay-live-target").dataset.state !== "unread", "the live target was never read");
expect(page.text("#highbay-frame-next dd").includes("Ask the builder to start a live target"), "Live target did not say who starts it");
const targets = answered(page, "/live-target");
for (const id of ["chg-other-1", "chg-other-2"]) {
  const reads = answered(page, "/live-target");
  await propose(id, "Show carriers on receipts.", "Carrier shown");
  await until(() => answered(page, "/live-target") > reads, "the page did not read the live target again after " + id);
}
expect(answered(page, "/changes") >= lists + 5, "the page did not read the list again after each change");
expect(answered(page, "/changes/chg-refresh") >= cards + 3 && answered(page, "/live-target") >= targets + 2, "the page did not read what it shows again");
const added = (await history()).filter((span) => !before.has(span.span_id));
const byUser = added.filter((span) => span.attributes["highbay.caller_kind"] === "user").map((span) => span.name).sort();
expect(byUser.length === 0, "the page's " + byUser.length + " reads were saved in the diagnostics history: " + JSON.stringify(byUser));
const byGuide = added.filter((span) => span.attributes["highbay.caller_kind"] === "guide").map((span) => span.name);
expect(byGuide.filter((name) => name === "highbay.request.revise_change").length === 3 && byGuide.filter((name) => name === "highbay.request.propose_change").length === 2, "the guide's changes are missing from the history: " + JSON.stringify(byGuide));
page.close();
`)
	exported := map[string]int{}
	for _, span := range exporter.GetSpans() {
		exported[span.Name]++
	}
	assert.GreaterOrEqual(t, exported["highbay.request.list_changes"], 6, "each list read keeps its exported request span")
	assert.GreaterOrEqual(t, exported["highbay.controlplane.list_changes"], 6, "each list read keeps its exported domain span")
	assert.GreaterOrEqual(t, exported["highbay.request.get_change"], 4, "each read of the open change keeps its exported request span")
	assert.GreaterOrEqual(t, exported["highbay.request.get_live_target"], 3, "each live target read keeps its exported request span")
}

// TestViewsPageApprovesAPassedResult confirms a change, opens its passed
// verification run, and approves it with a keyboard-operable control that
// shows a busy state and moves focus to the recorded approval. It also
// shows why approval is unavailable without promotion settings before any
// click, still explains an approval the server refuses as not set up, and
// sends a stale approval through the editor's revision recovery.
func TestViewsPageApprovesAPassedResult(t *testing.T) {
	spec := reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: strings.Repeat("2", 40),
		Environment: "sim", Artifacts: map[string]string{"orders": strings.Repeat("a", 64)},
	}
	const commit = "1111111111111111111111111111111111111111"
	guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
	user := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: pageUserID, Kind: CallerUser})

	t.Run("approves after merging", func(t *testing.T) {
		target := &pageTarget{artifacts: spec.Artifacts}
		cp := pageControlPlane(t, buildFixtureOptions(t, Options{ResolvePromotionTarget: target.resolve}))
		card := buildableChange(t, cp, "chg-merged-approve")
		saveStoredBuildFixture(t, cp, card)
		commit := commitBuilderCandidateFixture(t, cp, card)
		read, err := cp.GetBuild(user, &genhighbay.GetBuildPayload{ChangeID: card.ChangeID})
		require.NoError(t, err)
		build, ok := read.Build.AsStarted()
		require.True(t, ok)
		encoded, err := genhighbay.EncodeStartedBuild(build)
		require.NoError(t, err)
		report := passingReport(t, card.RequestRevision, commit, spec)
		target.update(func(p *pageTarget) { p.report = report })
		svc := newPageVerifications(cp)
		run := passedRun(card, "abcdefabcdefabcdefabcdefabcdefab", commit, report.Digest)
		run.InputsCurrent = true
		svc.set(run)
		svc.list(card.ChangeID, run.RunID)
		ps := startPageServer(t, svc)
		runPageScript(t, ps, map[string]any{"build": string(encoded)}, `
await control(origin, "reply", { key: "GET /changes/chg-merged-approve/build", status: 200,
  body: { change_id: "chg-merged-approve", drafted_before_drop: false, build: { ...JSON.parse(input.build), state: "started", taking: { state: "merged" } } } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-taking")?.dataset.state === "merged", "the merged state did not appear");
expect(!page.text("#highbay-build-taking").includes("git merge") && !page.$("#highbay-build-drop"), "a merged build still offers taking actions");
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "merging hid approval of the still-current passed report");
page.$("#highbay-cp5-approve").click();
await until(() => page.$("#highbay-cp5-approval"), "approval after merging was not recorded");
expect(page.sent("POST", "/changes/approve").length === 1, "approval after merging sent more than one write");
page.close();
`)
	})

	t.Run("approves", func(t *testing.T) {
		target := &pageTarget{artifacts: spec.Artifacts}
		cp := pageControlPlane(t, buildFixtureOptions(t, Options{ResolvePromotionTarget: target.resolve}))
		proposed := saveProposedCardFixture(t, cp, pageOwner, "chg-approve", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
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
		svc := newPageVerifications(cp)
		run := passedRun(confirmed, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", commit, report.Digest)
		// The fixture models the owner observing this confirmed revision and selected tests.
		run.InputsCurrent = true
		svc.set(run)
		svc.list(confirmed.ChangeID, run.RunID)
		ps := startPageServer(t, svc)
		runPageScript(t, ps, map[string]any{"run": run.RunID, "digest": report.Digest, "revision": confirmed.RequestRevision, "candidate": commit}, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-approve", "the change did not open");
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "a passed result offered no approval control");
expect(page.text("#highbay-result-view").includes("2 of 2 declared tests passed.") && page.text("#highbay-result-view").includes("holdout-1"), "the run's result is not shown");
checkControl(page, page.$("#highbay-cp5-approve"), "approve control");
await control(origin, "hold", { key: "POST /changes/approve" });
page.$("#highbay-cp5-approve").click();
expect(page.$("#highbay-cp5-approve").disabled && page.$("#highbay-cp5-approve").textContent === "Approving…" && page.$("#highbay-approval-stage").getAttribute("aria-busy") === "true", "approving showed no busy state");
const sent = page.sent("POST", "/changes/approve");
expect(sent.length === 1 && JSON.stringify(sent[0].body) === JSON.stringify({ change_id: "chg-approve", request_revision: input.revision, candidate_commit: input.candidate, report_digest: input.digest }), "approval did not act on the exact revision and result shown: " + JSON.stringify(sent));
await control(origin, "release", { key: "POST /changes/approve" });
await until(() => page.$("#highbay-cp5-approval"), "the recorded approval is not shown");
expect(page.doc.activeElement === page.$("#highbay-cp5-approval"), "approving did not move focus to the result");
const approval = page.text("#highbay-approval-stage");
expect(approval.includes("Approval recorded") && approval.includes("Approval records permission to promote these verified inputs") && !approval.includes("Approved by") && !approval.includes(input.owner) && approval.includes(input.digest) && approval.includes("orders_acceptance") && approval.includes("Builder’s commit") && approval.includes("Tests’ commit") && approval.includes(input.candidate) && approval.includes("2222222222222222222222222222222222222222"), "the approval panel lacks its record or exposes an opaque identity: " + approval);
expect(!page.$("#highbay-cp5-approval-dirty-warning"), "a clean approved card warned about unsaved edits");
page.$("#highbay-nav-cp1").click();
const outcome = page.$('textarea[data-field="expected_outcome"]');
const saved = outcome.value;
page.type(outcome, "edited after approval");
expect(page.$("#highbay-cp1-approval-dirty-warning"), "unsaved edits on an approved card did not warn that saving clears the approval");
page.type(outcome, saved);
expect(!page.$("#highbay-cp1-approval-dirty-warning"), "restoring the saved text did not clear the warning");
const stored = await readCard("chg-approve");
expect(stored.approval && stored.approval.approved_by === input.owner && stored.approval.report_digest === input.digest, "the saved change does not record the user's approval");
page.close();

// A new page session confirming the unchanged answers keeps the revision and the approval.
const again = await openPage(origin, { link: input.link + "-2" });
await until(() => again.$("#highbay-cp1-editor").dataset.changeId === "chg-approve" && again.$("#highbay-review-approval"), "a new session did not open the approved change");
again.$("#highbay-confirm-submit").click();
await until(() => again.sent("POST", "/changes/confirm").length === 1 && !again.$("#highbay-confirm-submit").disabled, "the unchanged confirmation did not finish");
expect(again.$("#highbay-cp1-editor").dataset.requestRevision === input.revision && again.$("#highbay-review-approval"), "confirming unchanged answers changed the revision or dropped the approval");
const unchanged = await readCard("chg-approve");
expect(unchanged.request_revision === input.revision && unchanged.approval && unchanged.approval.approved_by === input.owner, "the saved revision or approval changed");
again.close();
`)
	})

	t.Run("written tests approval without promotion settings shows both commits and hides automatically when tests change", func(t *testing.T) {
		reportDir := t.TempDir()
		cp := newBuildControlPlane(t, Options{BuildCacheDir: t.TempDir(),
			WorkspaceDir:    t.TempDir(),
			RunsDir:         t.TempDir(),
			HighbayVersion:  testHighbayVersion,
			WrittenTestRuns: &WrittenTestRuns{ReportDir: reportDir, Command: []string{"/bin/sh", "-c", "exit 0", "sh"}, Timeout: 1000000000},
		})
		cp.changesWait = 400000000
		cp.changesPoll = 20000000
		confirmed := saveBuildStoryCard(t, cp, "chg-written-approve", StatusConfirmed, refundStory("Refund over the limit waits"))
		_, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{ChangeID: confirmed.ChangeID, RequestRevision: confirmed.RequestRevision})
		require.NoError(t, err)
		acceptTests(t, cp, confirmed.ChangeID, spec.SuiteCommit)
		commit := commitBuilderCandidateFixture(t, cp, confirmed)
		report := passingReport(t, confirmed.RequestRevision, commit, spec)
		writeReportFile(t, reportDir, report)
		svc := newPageVerifications(cp)
		run := passedRun(confirmed, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", commit, report.Digest)
		// The fixture models the owner observing this confirmed revision and selected tests.
		run.InputsCurrent = true
		run.TestSource = genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{Commit: spec.SuiteCommit, Notice: writtenTestsNotice})
		svc.set(run)
		svc.list(confirmed.ChangeID, run.RunID)
		ps := startPageServer(t, svc)
		const nextCommit = "4444444444444444444444444444444444444444"
		ps.act("accept-new-tests", func() {
			acceptTests(t, cp, confirmed.ChangeID, nextCommit)
			// The same saved report now has an owner observation of older tests.
			stale := *run
			stale.InputsCurrent = false
			svc.set(&stale)
		})
		runPageScript(t, ps, map[string]any{"suiteCommit": spec.SuiteCommit, "candidate": commit}, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-written-approve", "the change did not open");
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "a passed result without promotion settings offered no approval control");
page.$("#highbay-cp5-approve").click();
await until(() => page.$("#highbay-cp5-approval"), "the recorded approval is not shown");
const approval = page.text("#highbay-approval-stage");
expect(approval.includes("Builder’s commit") && approval.includes(input.candidate) && approval.includes("Tests’ commit") && approval.includes("orders_acceptance") && approval.includes(input.suiteCommit) && approval.includes("This saved result names the builder’s commit") && !approval.includes("on branch highbay/tests)."), "written-tests approval did not keep its own commits or invented a historical branch: " + approval);
const approvals = page.sent("POST", "/changes/approve").length;
const reads = page.sent("GET", "/changes/chg-written-approve").length;
await control(origin, "act", { key: "accept-new-tests" });
await until(() => page.sent("GET", "/changes/chg-written-approve").length > reads && !page.$("#highbay-cp5-approval"), "accepting another tests commit did not automatically hide the approval and merge sentence without clicking refresh");
page.$("#highbay-nav-cp5").click();
await until(() => !page.$("#highbay-cp5-approve") && page.$("#highbay-cp5-approve-blocked"), "the older tests report did not remain visible with the owner’s refusal");
expect(page.sent("POST", "/changes/approve").length === approvals && !page.$("#highbay-cp5-approve-error"), "a saved report with old tests submitted another approval");
expect(page.text("#highbay-cp5-approval-details").includes(input.suiteCommit) && page.text("#highbay-cp5-title") === "The check passed", "the stale observation changed the saved passing report or its tests commit");
page.close();
`)
	})

	t.Run("unavailable when verification is not configured", func(t *testing.T) {
		target := &pageTarget{artifacts: spec.Artifacts, absent: true}
		cp := newControlPlane(t, Options{ResolvePromotionTarget: target.resolve})
		cp.changesWait = 400000000
		cp.changesPoll = 20000000
		proposed := mustPropose(t, cp, guide, "chg-unavailable", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
		confirmed, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
			ChangeID: proposed.ChangeID, RequestRevision: proposed.RequestRevision,
			Answers: map[string]string{}, Examples: proposed.Examples,
		})
		require.NoError(t, err)
		svc := newPageVerifications(cp)
		run := passedRun(confirmed, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", commit, strings.Repeat("3", 64))
		run.InputsCurrent = false // This server has no selected verification inputs.
		svc.set(run)
		svc.list(confirmed.ChangeID, run.RunID)
		ps := startPageServer(t, svc)
		runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-unavailable", "the change did not open");
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-approval-stage"), "a passed result showed no review stage");
await until(() => page.$("#highbay-cp5-unavailable"), "approval was not shown as unavailable before any click");
const reason = page.text("#highbay-cp5-unavailable");
expect(reason.includes("Approval is unavailable on this server") && reason.includes("Start Highbay from a project directory") && !reason.includes("highbay promotion set"), "unavailable approval does not name the enabling step: " + reason);
expect(!page.$("#highbay-cp5-approve"), "an unavailable approval still offered its control");
expect(page.sent("POST", "/changes/approve").length === 0, "the page sent an approval it knew the server cannot record");
page.close();
`)
	})

	t.Run("a refused approval still shows why", func(t *testing.T) {
		target := &pageTarget{artifacts: spec.Artifacts}
		cp := pageControlPlane(t, buildFixtureOptions(t, Options{ResolvePromotionTarget: target.resolve}))
		proposed := saveProposedCardFixture(t, cp, pageOwner, "chg-refused", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
		proposed = attachBuildSnapshot(t, cp, proposed.ChangeID)
		confirmed, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
			ChangeID: proposed.ChangeID, RequestRevision: proposed.RequestRevision,
			Answers: map[string]string{}, Examples: proposed.Examples,
		})
		require.NoError(t, err)
		saveStoredBuildFixture(t, cp, confirmed)
		candidate := commitBuilderCandidateFixture(t, cp, confirmed)
		report := passingReport(t, confirmed.RequestRevision, candidate, spec)
		target.update(func(p *pageTarget) { p.report = report })
		svc := newPageVerifications(cp)
		run := passedRun(confirmed, "dddddddddddddddddddddddddddddddd", candidate, report.Digest)
		// The fixture models the owner observing this confirmed revision and selected tests.
		run.InputsCurrent = true
		svc.set(run)
		svc.list(confirmed.ChangeID, run.RunID)
		ps := startPageServer(t, svc)
		runPageScript(t, ps, nil, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-refused", "the change did not open");
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "an available approval offered no control");
checkControl(page, page.$("#highbay-cp5-approve"), "approve control");
await control(origin, "reply", { key: "POST /changes/approve", status: 501, body: { name: "not_implemented", id: "x", message: "verification is not configured on this control plane", temporary: false, timeout: false, fault: false } });
page.$("#highbay-cp5-approve").click();
await until(() => page.$("#highbay-cp5-approve-error"), "a refused approval was not shown in the review");
const reason = page.text("#highbay-cp5-unavailable");
expect(reason.includes("verification is not configured") && reason.includes("Start Highbay from a project directory") && !reason.includes("highbay promotion set"), "the refusal does not carry the server's reason and the enabling step: " + reason);
expect(page.doc.activeElement === page.$("#highbay-cp5-approve-error"), "the unavailable result did not take focus");
expect(!page.$("#highbay-cp5-approve"), "a refused approval still offered its control");
page.close();
`)
	})

	t.Run("stale revision keeps the review and preserved editor recovery", func(t *testing.T) {
		target := &pageTarget{artifacts: spec.Artifacts}
		cp := pageControlPlane(t, buildFixtureOptions(t, Options{ResolvePromotionTarget: target.resolve}))
		proposed := saveProposedCardFixture(t, cp, pageOwner, "chg-stale", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
		proposed = attachBuildSnapshot(t, cp, proposed.ChangeID)
		confirmed, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
			ChangeID: proposed.ChangeID, RequestRevision: proposed.RequestRevision,
			Answers: map[string]string{}, Examples: proposed.Examples,
		})
		require.NoError(t, err)
		saveStoredBuildFixture(t, cp, confirmed)
		candidate := commitBuilderCandidateFixture(t, cp, confirmed)
		report := passingReport(t, confirmed.RequestRevision, candidate, spec)
		target.update(func(p *pageTarget) { p.report = report })
		svc := newPageVerifications(cp)
		run := passedRun(confirmed, "cccccccccccccccccccccccccccccccc", candidate, report.Digest)
		// The fixture models the owner observing this confirmed revision and selected tests.
		run.InputsCurrent = true
		svc.set(run)
		svc.list(confirmed.ChangeID, run.RunID)
		ps := startPageServer(t, svc)
		// The user refines the confirmed change elsewhere, such as in another
		// tab, so the page's revision is stale.
		ps.act("refine", func() {
			refined := *confirmed.Examples[0]
			refined.ExpectedOutcome = "Manager approval within a day"
			_, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
				ChangeID: confirmed.ChangeID, RequestRevision: confirmed.RequestRevision,
				Answers: map[string]string{}, Examples: []*genhighbay.ExampleItem{&refined},
			})
			require.NoError(t, err)
		})
		runPageScript(t, ps, map[string]any{"revision": confirmed.RequestRevision}, viewsPageScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-stale", "the change did not open");
page.$("#highbay-nav-cp5").click();
await until(() => page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "no current approval control");
// The owner can save a new revision after this page submits a current result.
await control(origin, "hold", { key: "POST /changes/approve" });
await control(origin, "reply", { key: "POST /changes/approve", status: 409, body: { name: "conflict", id: "x", message: "request_revision mismatch", temporary: false, timeout: false, fault: false } });
page.$("#highbay-cp5-approve").click();
await control(origin, "act", { key: "refine" });
await control(origin, "release", { key: "POST /changes/approve" });
await until(() => page.$("#highbay-cp5-approve-error"), "a stale approval did not stay in the review");
expect(page.text("#highbay-cp5-approve-error").includes("request_revision mismatch"), "the owner's refusal was lost");
expect(page.$("#highbay-approval-stage") && page.doc.activeElement === page.$("#highbay-cp5-approve-error"), "the refusal left the stage or lost focus");
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor").dataset.requestRevision !== input.revision && page.$("#highbay-prior-revision"), "the owner refresh did not preserve the new revision and prior context");
page.close();
`)
	})
}
