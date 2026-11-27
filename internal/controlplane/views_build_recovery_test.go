// This file tests Build recovery through the served page and generated HTTP API.
// Working copies, generation and agent windows are explicitly simulated by the
// existing build fixture; these tests do not prove candidate execution.
package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/require"
)

// TestViewsPageBuildIgnoresThePreviousChangesDelayedRead holds a real build
// answer while the user opens a different change with a saved build. Releasing
// the older answer must not replace the selected change's facts or controls.
func TestViewsPageBuildIgnoresThePreviousChangesDelayedRead(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	buildableChange(t, cp, "chg-first")
	second := buildableChange(t, cp, "chg-second")
	started, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
		ChangeID: second.ChangeID, RequestRevision: second.RequestRevision,
	})
	require.NoError(t, err)
	current, err := cp.GetBuild(asCaller(pageOwner, CallerUser), &genhighbay.GetBuildPayload{ChangeID: second.ChangeID})
	require.NoError(t, err)
	recent, ok := current.Build.AsStarted()
	require.True(t, ok)
	branchTip, checkTime, checkPassed := strings.Repeat("d", 40), "2026-11-24T14:02:21Z", false
	recent.Builder.HeadCommit = &branchTip
	recent.Builder.LastCheckAt = &checkTime
	recent.Builder.LastCheckPassed = &checkPassed
	encoded, err := genhighbay.EncodeChangeBuild(current)
	require.NoError(t, err)
	ps := startPageServer(t, cp)

	runPageScript(t, ps, map[string]any{
		"revision": second.RequestRevision, "started": started.Build.StartedAt,
		"build": string(encoded), "tip": branchTip, "checked": checkTime,
	}, `
const firstPath = "/changes/chg-first/build";
await control(origin, "answer-later", { key: "GET " + firstPath });
await control(origin, "reply", { key: "GET /changes/chg-second/build", status: 200, body: JSON.parse(input.build) });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-open-change-chg-first"), "the change list did not open");
page.$("#highbay-open-change-chg-first").click();
await until(() => page.sent("GET", firstPath).length === 1 && page.$("#highbay-build-loading"), "the first build read was not held");
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-second").click();
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-second" && page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the second change's current build did not open");
expect(page.text("#highbay-build-details").includes(input.revision) && page.text("#highbay-build-details").includes(input.started), "the selected build's revision and start are missing");
expect(page.text("#highbay-build-builder-branch").includes("highbay/build-1") &&
       page.text("#highbay-build-branch-tip") === "Branch tip " + input.tip.slice(0, 12) &&
       page.text("#highbay-build-details").includes(input.tip), "Build did not identify the branch and its exact tip");
expect(page.text("#highbay-build-last-project-check").includes("Last project check did not pass") &&
       page.$("#highbay-build-last-project-check time")?.dateTime === input.checked &&
       !page.text("#highbay-build-last-project-check").includes(input.tip.slice(0, 12)),
       "the last project check lost its reported result or was bound to the branch tip");
const replaced = [];
const observer = new page.win.MutationObserver(() => {
  if (page.$("#highbay-build-start") || page.$("#highbay-build-error") || page.$("#highbay-build-section").dataset.state !== "started") replaced.push(page.text("#highbay-build-section"));
});
observer.observe(page.$("#highbay-build-section"), { childList: true, attributes: true, subtree: true });
await control(origin, "release", { key: "GET " + firstPath });
await until(() => page.sent("GET", firstPath)[0].status === 200, "the delayed answer never arrived");
// Let the delivered answer's promise and React update finish in the next task.
await sleep(0);
observer.disconnect();
expect(replaced.length === 0, "the previous change's delayed answer replaced the current build: " + JSON.stringify(replaced));
expect(page.text("#highbay-build-details").includes(input.started), "the selected build lost its saved start");
expect(page.text("#highbay-build-branch-tip").includes(input.tip.slice(0, 12)) &&
       page.$("#highbay-build-last-project-check time")?.dateTime === input.checked, "the delayed read replaced the selected builder progress");
expect(page.sent("POST", firstPath).length === 0 && page.sent("POST", "/changes/chg-second/build").length === 0, "navigation started a build");
page.close();
`)
}

// TestViewsPageBuildWaitsForConfirmationAndARecoveredRead distinguishes an
// unconfirmed card, an unread build and a failed read from a saved absent build.
// Only a successful retry can enable the confirmed card's first Build action.
func TestViewsPageBuildWaitsForConfirmationAndARecoveredRead(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	buildableChange(t, cp, "chg-confirmed")
	saveProposedCardFixture(t, cp, pageOwner, "chg-unconfirmed", "Route refunds to a manager.", refundAnalysis("Route refunds to a manager."))
	ps := startPageServer(t, cp)

	runPageScript(t, ps, nil, `
const path = "/changes/chg-confirmed/build";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-open-change-chg-unconfirmed"), "the change list did not open");
page.$("#highbay-open-change-chg-unconfirmed").click();
await until(() => page.$("#highbay-cp1-editor").dataset.changeId === "chg-unconfirmed", "the unconfirmed card did not open");
expect(!page.$("#highbay-build-start") && !page.$("#highbay-build-again") && !page.$("#highbay-build-confirm"), "an unconfirmed card offers Build");
await control(origin, "hold", { key: "GET " + path });
await control(origin, "reply", { key: "GET " + path, status: 503, body: { name: "unavailable", id: "build-read", message: "Build owner unavailable.", fault: false, temporary: true, timeout: false } });
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-confirmed").click();
await until(() => page.$("#highbay-build-loading") && page.sent("GET", path).length === 1, "the unread build was not held");
expect(!page.$("#highbay-build-start") && !page.$("#highbay-build-again"), "an unread build offers an unsafe start");
expect(page.$("#highbay-build-section").dataset.state === "unread", "an unread build is presented as absent");
await control(origin, "release", { key: "GET " + path });
await until(() => page.$("#highbay-build-error") && page.$("#highbay-build-refresh") && !page.$("#highbay-build-refresh").disabled, "the failed build read did not offer retry");
expect(page.text("#highbay-build-error").includes("Build owner unavailable."), "the owner failure is missing");
expect(!page.$("#highbay-build-start") && !page.$("#highbay-build-again"), "a failed initial read offers an unsafe start");
page.$("#highbay-build-refresh").click();
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "retry did not recover the first Build action");
expect(!page.$("#highbay-build-error") && page.$("#highbay-build-section").dataset.state === "not_started", "retry did not establish the saved absent-build state");
expect(page.sent("POST", path).length === 0, "confirmation checks, reads or retry started a build");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the recovered Build action did not start successfully");
expect(page.sent("POST", path).length === 1, "the recovered action did not submit exactly once");
page.close();
`)
}

// TestViewsPageBuildWaitsForTheListedAgreementBeforeCardReadCompletes holds
// the detailed card read after the list announces a new agreement. The older
// displayed card must stop offering Build before the detailed answer arrives.
func TestViewsPageBuildWaitsForTheListedAgreementBeforeCardReadCompletes(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	first := buildableChange(t, cp, "chg")
	ps := startPageServer(t, cp)
	ps.act("refine-agreement", func() {
		_, err := cp.ConfirmIntent(asCaller(pageOwner, CallerUser), &genhighbay.ConfirmIntentPayload{
			ChangeID: first.ChangeID, RequestRevision: first.RequestRevision, Answers: map[string]string{},
			Examples: []*genhighbay.ExampleItem{{
				ID: first.Examples[0].ID, Scenario: first.Examples[0].Scenario,
				InputSummary:    first.Examples[0].InputSummary,
				ExpectedOutcome: "A manager reviews the refund", VerificationKind: "deterministic",
			}},
		})
		require.NoError(t, err)
	})

	runPageScript(t, ps, map[string]any{"revision": first.RequestRevision}, `
const path = "/changes/chg/build";
const cardPath = "/changes/chg";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "the initial agreement's Build did not become available");
const cardReads = page.sent("GET", cardPath).length;
await control(origin, "hold", { key: "GET " + cardPath });
await control(origin, "act", { key: "refine-agreement" });
await until(() => page.sent("GET", "/changes").some((r) => r.status === 200 && JSON.parse(r.answer).changes.some((c) => c.change_id === "chg" && c.request_revision !== input.revision)) && page.sent("GET", cardPath).length > cardReads, "the new list agreement did not reach the page before its held card read");
await until(() => !page.$("#highbay-build-loading"), "the independent build read did not settle");
expect(page.$("#highbay-cp1-editor").dataset.requestRevision === input.revision, "the detailed card read was not held");
// Clicking the old DOM control must also be harmless if it remains rendered.
const oldBuild = page.$("#highbay-build-start");
if (oldBuild) oldBuild.click();
expect(page.sent("POST", path).length === 0, "Build sent the old displayed revision after the list announced a new agreement");
expect(!oldBuild || oldBuild.disabled, "the old displayed agreement still offers Build while its replacement card is unread");
await control(origin, "release", { key: "GET " + cardPath });
await until(() => page.$("#highbay-cp1-editor").dataset.requestRevision !== input.revision && page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "reading the new agreement did not recover Build");
expect(page.sent("POST", path).length === 0, "reading the new agreement started a build");
page.close();
`)
}

// TestViewsPageBuildUsesTheNewAgreementAndKeepsDraftAndProposal covers the
// earlier-build recovery and then rebuilding the current agreement. Draft text
// and a waiting proposal retain their own actions and survive the rebuild.
// Navigating away also dismisses the separate rebuild confirmation question.
func TestViewsPageBuildUsesTheNewAgreementAndKeepsDraftAndProposal(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	first := buildableChange(t, cp, "chg")
	_, err := cp.StartBuild(asCaller(pageOwner, CallerUser), &genhighbay.StartBuildPayload{
		ChangeID: first.ChangeID, RequestRevision: first.RequestRevision,
	})
	require.NoError(t, err)
	second, err := cp.ConfirmIntent(asCaller(pageOwner, CallerUser), &genhighbay.ConfirmIntentPayload{
		ChangeID: first.ChangeID, RequestRevision: first.RequestRevision, Answers: map[string]string{},
		Examples: []*genhighbay.ExampleItem{{
			ID: first.Examples[0].ID, Scenario: first.Examples[0].Scenario,
			InputSummary:    first.Examples[0].InputSummary,
			ExpectedOutcome: "A manager reviews the refund", VerificationKind: "deterministic",
		}},
	})
	require.NoError(t, err)
	require.NotEqual(t, first.RequestRevision, second.RequestRevision)
	ps := startPageServer(t, cp)

	runPageScript(t, ps, map[string]any{"revision": second.RequestRevision}, viewsPageScriptHelpers+`
const path = "/changes/chg/build";
const primary = (element) => element.classList.contains("highbay-btn-primary");
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "the earlier build did not offer the current-agreement action");
expect(page.text("#highbay-build-start") === "Build current agreed revision", "the earlier build's recovery action is unclear");
expect(page.$("#highbay-build-start").getAttribute("aria-describedby").split(/\s+/).includes("highbay-build-start-explanation") && page.text("#highbay-build-start-explanation").includes("From Build on, the tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges."), "earlier building offer omitted its execution explanation");
expect(page.text("#highbay-build-state").includes("Earlier build") && page.text("#highbay-build-state").includes("The agreed revision has changed."), "the saved build is presented as current");
expect(primary(page.$("#highbay-build-start")) && !primary(page.$("#highbay-build-view-team")), "the current-agreement Build is not the primary action");
const earlierRead = page.sent("GET", path).find((read) => read.status === 200 && JSON.parse(read.answer).build.state === "started");
expect(earlierRead, "the earlier build was not read from its owner");
const earlier = JSON.parse(earlierRead.answer).build;
expect(earlier.taking.state === "building" && earlier.request_revision !== input.revision, "the fixture is not an unfinished earlier revision");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "replacing an unfinished earlier build did not ask for consent");
expect(page.sent("POST", path).length === 0, "opening the earlier-build question sent a start");
page.$("#highbay-build-confirm").dispatchEvent(new page.win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
await until(() => !page.$("#highbay-build-confirm") && page.doc.activeElement === page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "Escape did not restore the earlier-build trigger and focus");
expect(page.sent("POST", path).length === 0, "Escape sent an earlier-build replacement");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-cancel"), "the earlier-build question did not reopen");
page.$("#highbay-build-cancel").click();
await until(() => !page.$("#highbay-build-confirm") && page.doc.activeElement === page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "Cancel did not restore the earlier-build trigger and focus");
expect(page.sent("POST", path).length === 0, "Cancel sent an earlier-build replacement");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "the earlier-build confirmation did not become ready");
page.$("#highbay-build-confirm").click();
await until(() => page.sent("POST", path).length === 1 && page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the confirmed current agreement did not finish building");
const firstReplacement = page.sent("POST", path)[0];
expect(firstReplacement.body.request_revision === input.revision && firstReplacement.body.replaces?.change_id === "chg" && firstReplacement.body.replaces.started_at === earlier.started_at && Object.keys(firstReplacement.body.replaces).length === 2, "the confirmed new agreement did not replace exactly the observed earlier build");
const currentStart = JSON.parse(firstReplacement.answer).build.started_at;
expect(!primary(page.$("#highbay-build-view-team")) && !primary(page.$("#highbay-build-again")), "agent work gives navigation or rebuilding primary weight");
const field = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
page.type(field, "Unsaved scenario on the agreed revision");
expect(primary(page.$("#highbay-confirm-submit")) && !primary(page.$("#highbay-build-view-team")), "an unsaved edit does not retain refinement priority");
const agreed = await readCard("chg");
await revise(agreed, "Refunds up to $60 are instant.", "A manager reviews the refund");
await until(() => page.$("#highbay-proposal"), "the waiting proposal did not appear");
const answer = page.$('#highbay-proposal [data-option="total_with_tax"]');
expect(answer, "the proposal's question is missing");
answer.click();
expect(primary(page.$("#highbay-proposal-adopt")) && !primary(page.$("#highbay-build-view-team")) && !primary(page.$("#highbay-build-again")), "the waiting proposal lost action priority to Build");
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the proposal's background build read did not settle before rebuilding");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "rebuilding the current agreement did not offer an enabled confirmation");
expect(page.sent("POST", path).length === 1, "opening the current rebuild question sent a start");
page.$("#highbay-build-confirm").click();
await until(() => page.sent("POST", path).length === 2 && page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the confirmed current rebuild did not finish");
const currentReplacement = page.sent("POST", path)[1].body;
expect(currentReplacement.request_revision === input.revision, "the rebuild used the unadopted proposal");
expect(currentReplacement.replaces?.change_id === "chg" && currentReplacement.replaces.started_at === currentStart && Object.keys(currentReplacement.replaces).length === 2, "the current-agreement restart did not name exactly its observed predecessor");
expect(field === page.$('textarea[data-example-id="check_01"][data-field="scenario"]') && field.value === "Unsaved scenario on the agreed revision", "the rebuild discarded or replaced the unsaved editor");
expect(page.$('#highbay-proposal [data-option="total_with_tax"]').getAttribute("aria-pressed") === "true", "the rebuild discarded the proposal answer");
expect(page.$("#highbay-proposal") && !page.$("#highbay-proposal-adopt").disabled && page.$("#highbay-confirm-submit").disabled, "the rebuild changed the waiting proposal's available actions");
const after = await readCard("chg");
expect(after.request_revision === input.revision && after.proposed_revision && after.examples[0].scenario !== field.value, "the rebuild saved draft text or adopted the proposal");
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the background read did not settle after inspecting the saved card");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-confirm"), "the next rebuild question did not open");
page.$("#highbay-build-view-team").click();
await until(() => page.state() === "team" && !page.$("#highbay-build-confirm"), "opening Team left the rebuild question active beside the retained draft");
expect(page.sent("POST", path).length === 2, "navigating away confirmed a rebuild");
page.$("#highbay-nav-cp1").click();
await until(() => page.state() === "cp1-change-card" && page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "returning to the decisions did not restore Build again");
expect(!page.$("#highbay-build-confirm"), "returning to the decisions restored an old rebuild question");
expect(field === page.$('textarea[data-example-id="check_01"][data-field="scenario"]') && field.value === "Unsaved scenario on the agreed revision" && page.$('#highbay-proposal [data-option="total_with_tax"]').getAttribute("aria-pressed") === "true", "dismissing the rebuild question discarded draft text or the proposal answer");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-confirm"), "a fresh Build again did not ask for confirmation");
expect(page.sent("POST", path).length === 2, "reopening the rebuild question sent a start");
page.$("#highbay-build-cancel").click();
await until(() => !page.$("#highbay-build-confirm") && page.doc.activeElement === page.$("#highbay-build-again"), "the fresh rebuild question did not cancel and restore focus");
expect(page.sent("POST", path).length === 2, "Cancel sent another replacement");
page.close();
`)
}

// TestViewsPageBuildTeamRecoveryClearsTheOldInstructionEverywhere proves a
// later successful Start agents supersedes the receipt of a Build whose team
// could not be started, in both the card and global status, while retaining
// the saved build's identity.
func TestViewsPageBuildTeamRecoveryClearsTheOldInstructionEverywhere(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	f.start = func(context.Context) (*genhighbay.ProjectTeam, error) {
		return nil, genhighbay.MakeTeamUnavailable(errors.New("simulated terminal failure"))
	}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	buildableChange(t, cp, "chg")
	ps := startPageServer(t, cp)
	ps.act("allow-team-start", func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.start = func(context.Context) (*genhighbay.ProjectTeam, error) {
			return &genhighbay.ProjectTeam{
				Team: "page-team",
				Step: "The project agents have started.", Roles: []*genhighbay.ProjectTeamRole{},
			}, nil
		}
	})

	// An error without a typed cause gets the step for no cause.
	runPageScript(t, ps, map[string]any{"unavailableStep": buildTeamStep("")}, `
const path = "/changes/chg/build";
const oldStep = input.unavailableStep;
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "the first Build did not become available");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-start-team-receipt") && page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the build without a team did not finish");
expect(page.text("#highbay-build-start-team-receipt").includes("the build was saved and team startup was unavailable") && page.text("#highbay-build-start-team-receipt").includes(oldStep), "the unavailable team was not shown as a historical receipt");
const savedStart = JSON.parse(page.sent("POST", path)[0].answer).build.started_at;
expect(page.text("#highbay-build-details").includes(savedStart), "the saved Build identity is missing");
await control(origin, "act", { key: "allow-team-start" });
page.$("#highbay-build-view-team").click();
await until(() => page.state() === "team" && page.$("#highbay-team-start") && !page.$("#highbay-team-start").disabled, "View team did not open the recovery action");
expect(page.sent("POST", "/team/agents").length === 0, "opening Team started agents implicitly");
page.$("#highbay-team-start").click();
await until(() => page.$("#highbay-team-start-receipt") && page.text("#highbay-team-start-receipt").includes("The project agents have started."), "the successful Team start receipt did not appear");
expect(!page.doc.body.textContent.includes(oldStep), "the superseded Build instruction remained visible in Team");
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-build-again") && page.text("#highbay-build-details").includes(savedStart), "returning to the card lost the saved Build");
expect(!page.$("#highbay-build-start-team-receipt") && !page.doc.body.textContent.includes(oldStep), "the old team instruction returned on the card or global status");
expect(page.text("#highbay-build-details").includes(savedStart), "Team recovery rewrote the saved Build identity");
page.$("#highbay-nav-team").click();
await until(() => page.$("#highbay-team-start-receipt"), "the latest Team receipt was lost on navigation");
expect(!page.doc.body.textContent.includes(oldStep), "the old Build instruction returned after reopening Team");
expect(page.sent("POST", path).length === 1 && page.sent("POST", "/team/agents").length === 1, "Team recovery repeated Build or Start agents");
page.close();
`)
}
