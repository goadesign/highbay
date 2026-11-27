// This file exercises Build actions through the served page's HTTP contract.
package controlplane

import (
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
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
		makeBuildFixtureBuilderCopy(t, cp)
		_, err := cp.GetBuilderAssignment(asCaller(pageOwner, CallerBuilder), &genhighbay.GetBuilderAssignmentPayload{ChangeID: "chg"})
		require.NoError(t, err)
	})
	runPageScript(t, ps, map[string]any{"revision": card.RequestRevision}, `
const path = "/changes/chg/build";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "first Build action did not become available");
expect(page.sent("POST", path).length === 0, "opening the card started a build");
expect(page.text("#highbay-build-start-explanation").includes("If this project needs a test writer") && page.text("#highbay-build-start-explanation").includes("From Build on, the tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges."), "pre-send explanation must name the conditional writer and say that from Build on the tests and the builder's code run unconfined, before any approval");
expect(page.text("#highbay-build-start-explanation").includes("accepted tests to check the builder’s work and to start live targets"), "the first Build explanation must name the accepted tests' live-target role");
await control(origin, "hold", { key: "POST " + path });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "Build did not become ready after the gate was installed");
page.$("#highbay-build-start").click();
await until(() => page.sent("POST", path).length === 1, "Build did not send a start");
expect(page.sent("POST", path)[0].body.request_revision === input.revision, "Build did not send the displayed revision");
expect(!("replaces" in page.sent("POST", path)[0].body), "the first Build sent a replacement identity");
expect(page.text("#highbay-build-starting") === "Starting build… This can take a few minutes and continues if you close the page.", "pending work is not explained");
page.$("#highbay-build-start").click();
expect(page.sent("POST", path).length === 1, "pending Build submitted twice");
await control(origin, "release", { key: "POST " + path });
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "saved Build did not appear");
const originalStart = JSON.parse(page.sent("POST", path)[0].answer).build.started_at;
expect(page.text("#highbay-build-builder").includes("assignment unread"), "page read marked the builder assignment read");
await control(origin, "act", { key: "builder-read" });
await until(() => page.text("#highbay-build-builder").includes("assignment read") && page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "real assignment read did not update the ready Build controls");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
const typed = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
expect(typed, "editable example is missing");
page.type(typed, "unsaved page text");
page.$("#highbay-nav-build").click();
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "Build again did not return");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-confirm"), "Build again did not ask first");
expect(page.text("#highbay-build-question").includes("accepted again before verification or a new live target"), "the rebuild question does not explain the verification and live-target consequences");
expect(page.text("#highbay-build-confirm-explanation").includes("accepted tests to check the builder’s work and to start live targets"), "the rebuild explanation must name the accepted tests' live-target role");
expect(page.text("#highbay-build-confirm-explanation").includes("From Build on, the tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges."), "rebuild confirmation omitted the execution sentence");
expect(page.sent("POST", path).length === 1, "opening rebuild confirmation sent a start");
page.$("#highbay-build-confirm").dispatchEvent(new page.win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
await until(() => !page.$("#highbay-build-confirm") && page.doc.activeElement === page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "Escape did not cancel and restore an enabled trigger's focus");
expect(page.sent("POST", path).length === 1, "Escape sent a replacement");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-cancel"), "rebuild question did not reopen");
page.$("#highbay-build-cancel").click();
await until(() => !page.$("#highbay-build-confirm") && page.doc.activeElement === page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "Cancel did not restore the ready rebuild control and focus");
expect(page.sent("POST", path).length === 1, "Cancel sent a start");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "final rebuild confirmation is not ready");
page.$("#highbay-build-confirm").click();
await until(() => page.sent("POST", path).length === 2 && page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "confirmed rebuild did not finish exactly once");
const replacement = page.sent("POST", path)[1].body;
expect(replacement.request_revision === input.revision && replacement.replaces?.change_id === "chg" && replacement.replaces.started_at === originalStart && Object.keys(replacement.replaces).length === 2, "confirmed restart did not name exactly the observed unfinished build");
await until(() => page.text("#highbay-build-builder").includes("assignment unread"), "rebuild did not clear the assignment read");
expect(page.sent("POST", path).length === 2, "one confirmed rebuild submitted twice");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "unsaved page text", "build updates discarded the unsaved editor");
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
			start := func() {
				_, err := startBuildAgain(t, cp, asCaller(pageOwner, CallerUser), card.ChangeID, card.RequestRevision)
				require.NoError(t, err)
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
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the initial rebuild control did not become ready");
page.$("#highbay-nav-cp1").click();
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="scenario"]'), "keep this unsaved example");
page.$("#highbay-nav-build").click();
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "rebuild control did not return");
const externalInput = page.$("#highbay-composer-input");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-confirm"), "rebuild confirmation did not open");
page.$("#highbay-build-confirm").focus();
if (input.after === "editor") externalInput.focus();
await control(origin, "hold", { key: "GET " + path });
await control(origin, "act", { key: "refresh-team" });
await until(() => page.$("#highbay-build-loading") && page.$("#highbay-build-again").disabled && page.$("#highbay-build-confirm"), "the held refresh did not disable the trigger beside the open question");
if (input.after === "editor") {
  expect(page.doc.activeElement === externalInput, "the refresh took focus into the open question");
  page.$("#highbay-build-confirm").focus();
}
page.$("#highbay-build-confirm").dispatchEvent(new page.win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
await until(() => !page.$("#highbay-build-confirm"), "Escape did not cancel during the refresh");
expect(page.sent("POST", path).length === 0, "Escape sent a build request");
if (input.after === "editor") {
  externalInput.focus();
} else if (input.after === "read failed") {
  await control(origin, "reply", { key: "GET " + path, status: 500,
    body: { name: "fault", id: "read-failed", message: "Build read failed.", fault: true, temporary: false, timeout: false } });
} else if (input.after === "build replaced") {
  await control(origin, "act", { key: "replace-build" });
}
await control(origin, "release", { key: "GET " + path });
await until(() => !page.$("#highbay-build-loading"), "the released build read did not finish");
if (input.after === "ready") {
  await until(() => page.doc.activeElement === page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "Escape lost focus when the rebuild trigger became enabled");
} else if (input.after === "editor") {
  expect(page.doc.activeElement === externalInput, "the finished refresh took focus from the external input");
} else {
  expect(page.doc.activeElement === page.$("#highbay-build-title"), "the unavailable or replaced trigger did not leave focus on the stable Build heading");
}
expect(!page.$("#highbay-build-confirm") && page.sent("POST", path).length === 0, "the refresh reopened or submitted the cancelled question");
page.$("#highbay-nav-cp1").click();
expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "keep this unsaved example", "the refresh discarded the edited example");
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
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "Build was not ready");
await control(origin, "reply", { key: "POST " + path, status: 409, body: { name: "working_copy_busy", id: "busy-start", message: "The current test check holds the working copy. Start the build after it ends.", fault: false, temporary: true, timeout: false } });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "Build did not become ready after the refusal was queued");
const reads = page.sent("GET", path).length;
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-action-error") && page.sent("GET", path).length > reads && !page.$("#highbay-build-start").disabled, "the refusal and recovery read did not complete");
expect(page.text("#highbay-build-action-error").includes("Build did not start.") && page.text("#highbay-build-action-error").includes("Start the build after it ends."), "the refused action lost the owner's step");
expect(page.$("#highbay-build-section").dataset.state === "not_started", "refusal invented a saved build");
await control(origin, "act", { key: "new-attention" });
await until(() => page.$("#highbay-team-attention-builder"), "the independent Team update did not arrive");
expect(page.text("#highbay-build-action-error").includes("Start the build after it ends."), "a later status update hid the refusal");
expect(page.sent("POST", path).length === 1, "a recovery read or Team update retried the effect");
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "the background build read did not settle before retry");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "an explicit retry did not build");
expect(!page.$("#highbay-build-action-error"), "an explicit successful retry retained the refusal");
expect(page.sent("POST", path).length === 2, "explicit retry sent more than one start");
page.close();
`)
}
