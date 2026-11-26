// This file proves revision warnings before the served page changes an agreed
// card. Team and working-copy effects use the existing explicit stand-ins.
package controlplane

import (
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/require"
)

// TestViewsPageWarnsBeforeChangingABuiltRevision distinguishes a writer build,
// stored settings, unread/failed build reads, and no build. A no-op refinement
// keeps its revision and needs no invalidation warning; a changed submission
// or adoption shows the consequence beside its action before sending anything.
func TestViewsPageWarnsBeforeChangingABuiltRevision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stored bool
		build  bool
		read   string
		adopt  bool
	}{
		{name: "writer refine", build: true},
		{name: "writer adopt", build: true, adopt: true},
		{name: "stored refine", stored: true, build: true},
		{name: "stored adopt", stored: true, build: true, adopt: true},
		{name: "unread refine", build: true, read: "held"},
		{name: "unread adopt", build: true, read: "held", adopt: true},
		{name: "failed read", build: true, read: "failed"},
		{name: "no build"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := Options{Team: &TeamBinding{Owner: pageOwner, Service: &teamServiceFixture{}}}
			if tc.stored {
				opts.BuildCacheDir = t.TempDir()
				opts.Verification = &VerificationConfig{RunsDir: t.TempDir()}
			}
			cp := newBuildControlPlane(t, opts)
			cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
			card := buildableChange(t, cp, "chg-warning")
			if tc.build {
				_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
					ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
				})
				require.NoError(t, err)
			}
			if tc.adopt {
				example := card.Examples[0]
				_, err := cp.ReviseChange(asCaller(pageOwner, CallerGuide), &genhighbay.ReviseChangePayload{
					ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
					Prompt:       "Review the revised refund decision.",
					RevisionNote: "Revise the change.",
					Analysis: &genhighbay.CP1RevisionAnalysis{
						Summary:     "The revised refund decision.",
						Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{}, RemovedExampleIds: []string{},
						Examples: []*genhighbay.CP1RevisionAnalysisExample{{
							PriorID: &example.ID, Scenario: example.Scenario, InputSummary: example.InputSummary,
							ExpectedOutcome: "A manager reviews the revised decision", VerificationKind: "deterministic",
						}},
						PreservedBehaviors: card.PreservedBehaviors,
					},
				})
				require.NoError(t, err)
			}
			ps := startPageServer(t, cp)
			runPageScript(t, ps, map[string]any{
				"revision": card.RequestRevision, "stored": tc.stored,
				"build": tc.build, "read": tc.read, "adopt": tc.adopt,
			}, `
const buildPath = "/changes/chg-warning/build";
if (input.read === "held") await control(origin, "hold", { key: "GET " + buildPath });
if (input.read === "failed") await control(origin, "reply", { key: "GET " + buildPath, status: 503,
  body: { name: "unavailable", id: "warning-read", message: "Build read unavailable.", fault: false, temporary: true, timeout: false } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-warning", "the agreed card did not open");
if (input.read === "held") await until(() => page.$("#highbay-build-loading"), "the build read was not held");
else if (input.read === "failed") await until(() => page.$("#highbay-build-error"), "the build read did not fail");
else await until(() => page.$("#highbay-build-section")?.dataset.state === (input.build ? "started" : "not_started") && !page.$("#highbay-build-loading"), "the saved build read did not settle");

let previousSaves = 0;
let warningId = "highbay-proposal-build-consequence";
let actionId = "highbay-proposal-adopt";
if (!input.adopt) {
  expect(!page.$("#highbay-confirm-build-consequence"), "an unchanged confirmation falsely warns of invalidation");
  if (!input.read) {
    await until(() => page.$("#highbay-confirm-submit") && !page.$("#highbay-confirm-submit").disabled, "no-op confirmation was not available");
    page.$("#highbay-confirm-submit").click();
    await until(() => page.sent("POST", "/changes/confirm").some((r) => r.status === 200) && !page.$("#highbay-confirm-submit").disabled, "no-op confirmation did not return");
    expect(page.$("#highbay-cp1-editor").dataset.requestRevision === input.revision, "no-op confirmation changed the revision");
    previousSaves = page.sent("POST", "/changes/confirm").length;
  }
  const field = page.$('textarea[data-example-id="check_01"][data-field="expected_outcome"]');
  field.focus();
  page.type(field, "A manager reviews the revised decision");
  expect(page.doc.activeElement === field && field.value.includes("revised decision"), "the warning replaced or unfocused the draft");
  warningId = "highbay-confirm-build-consequence";
  actionId = "highbay-confirm-submit";
}
const warning = page.$("#" + warningId);
const action = page.$("#" + actionId);
if (input.build) {
  expect(warning && action && action.getAttribute("aria-describedby")?.split(" ").includes(warningId), "the action has no linked build warning");
  expect(action.parentElement.previousElementSibling === warning, "the build consequence is not immediately beside the action");
  expect(warning.textContent.includes("earlier revision"), "the warning does not explain the build becoming earlier");
  if (input.stored) {
    expect(warning.textContent.includes("builder’s assignment") && warning.textContent.includes("stored verification settings") && !warning.textContent.includes("acceptance again"), "the warning invented a test-writer gate for stored settings");
  } else {
    expect(warning.textContent.includes(input.read ? "any existing build" : "this build") && warning.textContent.includes("before verification"), "the warning did not distinguish known and unread builds");
    expect(warning.textContent.includes("report") && warning.textContent.includes("verdict"), "the rebuild reset is not explained");
  }
} else expect(!warning, "a known absent build gets an invalidation warning");
expect(page.sent("POST", "/changes/confirm").length === previousSaves && page.sent("POST", buildPath).length === 0, "showing a consequence caused an effect");
await until(() => page.$("#" + actionId) && !page.$("#" + actionId).disabled, "the warned action was not available");
page.$("#" + actionId).click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.requestRevision !== input.revision, "the explicit revision action did not save");
expect(page.sent("POST", "/changes/confirm").length === previousSaves + 1 && page.sent("POST", buildPath).length === 0, "the action was repeated or rebuilt automatically");
if (input.read === "held") await control(origin, "release", { key: "GET " + buildPath });
if (input.build) await until(() => page.text(".highbay-build-badges").includes("Earlier built agreement " + input.revision.slice(0, 12)),
  "the earlier build did not identify the agreement it built");
page.close();
`)
		})
	}
}
