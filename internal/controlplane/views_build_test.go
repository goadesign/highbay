// This file exercises Build actions through the served page's HTTP contract.
package controlplane

import (
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/require"
)

// TestViewsPageBuildRequiresASeparateRebuildConfirmation checks that saved
// assignments follow actual role reads and that replacing a current build
// requires a distinct confirming action without discarding an edited card.
// Git, agent windows and test generation are explicit fixture boundaries;
// this test claims page/HTTP proof, not candidate execution proof.
func TestViewsPageBuildRequiresASeparateRebuildConfirmation(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	card := buildableChange(t, cp, "chg")
	ps := startPageServer(t, cp)
	ps.act("builder-read", func() {
		_, err := cp.GetBuilderAssignment(asCaller(pageOwner, CallerBuilder), &gensurla.GetBuilderAssignmentPayload{ChangeID: "chg"})
		require.NoError(t, err)
	})
	runPageScript(t, ps, map[string]any{"revision": card.RequestRevision}, `
const path = "/changes/chg/build";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "first Build action did not become available");
expect(page.sent("POST", path).length === 0, "opening the card started a build");
expect(page.text("#surla-build-start-explanation").includes("If this project needs a test writer") && page.text("#surla-build-start-explanation").includes("From Build on, the tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges."), "pre-send explanation must name the conditional writer and say that from Build on the tests and the builder's code run unconfined, before any approval");
expect(page.text("#surla-build-start-explanation").includes("accepted tests to check the builder’s work and to start live targets"), "the first Build explanation must name the accepted tests' live-target role");
await control(origin, "hold", { key: "POST " + path });
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "Build did not become ready after the gate was installed");
page.$("#surla-build-start").click();
await until(() => page.sent("POST", path).length === 1, "Build did not send a start");
expect(page.sent("POST", path)[0].body.request_revision === input.revision, "Build did not send the displayed revision");
expect(!("replaces" in page.sent("POST", path)[0].body), "the first Build sent a replacement identity");
expect(page.text("#surla-build-starting") === "Starting build… This can take a few minutes and continues if you close the page.", "pending work is not explained");
page.$("#surla-build-start").click();
expect(page.sent("POST", path).length === 1, "pending Build submitted twice");
await control(origin, "release", { key: "POST " + path });
await until(() => page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "saved Build did not appear");
const originalStart = JSON.parse(page.sent("POST", path)[0].answer).build.started_at;
expect(page.text("#surla-build-builder").includes("Assignment unread"), "page read marked the builder assignment read");
await control(origin, "act", { key: "builder-read" });
await until(() => page.text("#surla-build-builder").includes("Assignment read.") && page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "real assignment read did not update the ready Build controls");
const typed = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
expect(typed, "editable example is missing");
page.type(typed, "unsaved page text");
page.$("#surla-build-again").click();
await until(() => page.$("#surla-build-confirm"), "Build again did not ask first");
expect(page.text("#surla-build-question").includes("accepted again before verification or a new live target"), "the rebuild question does not explain the verification and live-target consequences");
expect(page.text("#surla-build-confirm-explanation").includes("accepted tests to check the builder’s work and to start live targets"), "the rebuild explanation must name the accepted tests' live-target role");
expect(page.sent("POST", path).length === 1, "opening rebuild confirmation sent a start");
page.$("#surla-build-confirm").dispatchEvent(new page.win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
await until(() => !page.$("#surla-build-confirm") && page.doc.activeElement === page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "Escape did not cancel and restore an enabled trigger's focus");
expect(page.sent("POST", path).length === 1, "Escape sent a replacement");
page.$("#surla-build-again").click();
await until(() => page.$("#surla-build-cancel"), "rebuild question did not reopen");
page.$("#surla-build-cancel").click();
await until(() => !page.$("#surla-build-confirm") && page.doc.activeElement === page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "Cancel did not restore the ready rebuild control and focus");
expect(page.sent("POST", path).length === 1, "Cancel sent a start");
page.$("#surla-build-again").click();
await until(() => page.$("#surla-build-confirm") && !page.$("#surla-build-confirm").disabled, "final rebuild confirmation is not ready");
page.$("#surla-build-confirm").click();
await until(() => page.sent("POST", path).length === 2 && page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "confirmed rebuild did not finish exactly once");
const replacement = page.sent("POST", path)[1].body;
expect(replacement.request_revision === input.revision && replacement.replaces?.change_id === "chg" && replacement.replaces.started_at === originalStart && Object.keys(replacement.replaces).length === 2, "confirmed restart did not name exactly the observed unfinished build");
await until(() => page.text("#surla-build-builder").includes("Assignment unread"), "rebuild did not clear the assignment read");
expect(typed === page.$('textarea[data-example-id="check_01"][data-field="scenario"]') && typed.value === "unsaved page text", "build updates discarded the unsaved editor");
expect(page.sent("POST", path).length === 2, "one confirmed rebuild submitted twice");
page.close();
`)
}

// TestViewsPageRebuildEscapeDuringRefresh holds a real page build read while
// Escape closes its question. Completing that read must restore the trigger
// or its stable heading without taking focus back from the user's editor.
func TestViewsPageRebuildEscapeDuringRefresh(t *testing.T) {
	for _, after := range []string{"ready", "editor", "read failed", "build replaced"} {
		t.Run(after, func(t *testing.T) {
			f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
			cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
			cp.changesWait = 400 * time.Millisecond
			cp.changesPoll = 20 * time.Millisecond
			card := buildableChange(t, cp, "chg")
			var replaces *gensurla.ReplacedBuild
			start := func() {
				result, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &gensurla.StartBuildPayload{
					ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, Replaces: replaces,
				})
				require.NoError(t, err)
				replaces = &gensurla.ReplacedBuild{ChangeID: card.ChangeID, StartedAt: result.Build.StartedAt}
			}
			start()
			ps := startPageServer(t, cp)
			ps.act("refresh-team", func() {
				f.mu.Lock()
				defer f.mu.Unlock()
				f.snapshot = pageTeamSnapshot("needs_you")
			})
			ps.act("replace-build", start)

			runPageScript(t, ps, map[string]any{"after": after}, `
const path = "/changes/chg/build";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "the initial rebuild control did not become ready");
const edited = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
page.type(edited, "keep this unsaved example");
page.$("#surla-build-again").click();
await until(() => page.$("#surla-build-confirm"), "rebuild confirmation did not open");
page.$("#surla-build-confirm").focus();
if (input.after === "editor") edited.focus();
await control(origin, "hold", { key: "GET " + path });
await control(origin, "act", { key: "refresh-team" });
await until(() => page.$("#surla-build-loading") && page.$("#surla-build-again").disabled && page.$("#surla-build-confirm"), "the held refresh did not disable the trigger beside the open question");
if (input.after === "editor") {
  expect(page.doc.activeElement === edited, "the refresh took focus into the open question");
  page.$("#surla-build-confirm").focus();
}
page.$("#surla-build-confirm").dispatchEvent(new page.win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
await until(() => !page.$("#surla-build-confirm"), "Escape did not cancel during the refresh");
expect(page.sent("POST", path).length === 0, "Escape sent a build request");
if (input.after === "editor") {
  edited.focus();
} else if (input.after === "read failed") {
  await control(origin, "reply", { key: "GET " + path, status: 500,
    body: { name: "fault", id: "read-failed", message: "Build read failed.", fault: true, temporary: false, timeout: false } });
} else if (input.after === "build replaced") {
  await control(origin, "act", { key: "replace-build" });
}
await control(origin, "release", { key: "GET " + path });
await until(() => !page.$("#surla-build-loading"), "the released build read did not finish");
if (input.after === "ready") {
  await until(() => page.doc.activeElement === page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "Escape lost focus when the rebuild trigger became enabled");
} else if (input.after === "editor") {
  expect(page.doc.activeElement === edited, "the finished refresh took focus from the editor");
} else {
  expect(page.doc.activeElement === page.$("#surla-build-title"), "the unavailable or replaced trigger did not leave focus on the stable Build heading");
}
expect(edited.value === "keep this unsaved example", "the refresh discarded the edited example");
expect(!page.$("#surla-build-confirm") && page.sent("POST", path).length === 0, "the refresh reopened or submitted the cancelled question");
page.close();
`)
		})
	}
}

// TestViewsPageBuildRetainsARefusalBesideItsSavedFacts keeps the owner's step
// visible after a successful recovery read and another Team status update.
func TestViewsPageBuildRetainsARefusalBesideItsSavedFacts(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	buildableChange(t, cp, "chg")
	ps := startPageServer(t, cp)
	ps.act("new-attention", func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.snapshot = pageTeamSnapshot("needs_you")
	})
	runPageScript(t, ps, nil, `
const path = "/changes/chg/build";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "Build was not ready");
await control(origin, "reply", { key: "POST " + path, status: 409, body: { name: "working_copy_busy", id: "busy-start", message: "The current test check holds the working copy. Start the build after it ends.", fault: false, temporary: true, timeout: false } });
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "Build did not become ready after the refusal was queued");
const reads = page.sent("GET", path).length;
page.$("#surla-build-start").click();
await until(() => page.$("#surla-build-action-error") && page.sent("GET", path).length > reads && !page.$("#surla-build-start").disabled, "the refusal and recovery read did not complete");
expect(page.text("#surla-build-action-error").includes("Build did not start.") && page.text("#surla-build-action-error").includes("Start the build after it ends."), "the refused action lost the owner's step");
expect(page.$("#surla-build-section").dataset.state === "not_started", "refusal invented a saved build");
await control(origin, "act", { key: "new-attention" });
await until(() => page.$("#surla-team-attention-builder"), "the independent Team update did not arrive");
expect(page.text("#surla-build-action-error").includes("Start the build after it ends."), "a later status update hid the refusal");
expect(page.sent("POST", path).length === 1, "a recovery read or Team update retried the effect");
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "the background build read did not settle before retry");
page.$("#surla-build-start").click();
await until(() => page.$("#surla-build-again") && !page.$("#surla-build-again").disabled, "an explicit retry did not build");
expect(!page.$("#surla-build-action-error"), "an explicit successful retry retained the refusal");
expect(page.sent("POST", path).length === 2, "explicit retry sent more than one start");
page.close();
`)
}
