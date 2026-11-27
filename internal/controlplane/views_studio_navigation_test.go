// This file tests the Highbay Studio redesign contracts across multi-change
// draft and mutation isolation, per-scope composer serialization and non-empty
// draft preservation on failed Agree sends, multi-draft pending-link recovery,
// opaque history.state reload and popstate navigation, Team-to-Build stepper
// initialization, current-build failure routing beside an older mergeable pass,
// and passive-to-needs_you terminal promotion.
package controlplane

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

type (
	// pageStudioFailureFixture attaches a LatestVerificationItem and StartedBuild
	// to ListChanges and GetBuild matching ControlPlane's on-disk contracts.
	pageStudioFailureFixture struct {
		*pageVerifications
		changeID string
		latest   *genhighbay.LatestVerificationItem
		build    *genhighbay.ChangeBuild
	}
)

// TestViewsPageStudioMultiChangeIsolationAndHistory proves per-change draft and
// late-mutation isolation across A -> B -> A, answer-later stale refreshOpenCard
// generation discard after adoption, handleAdopt diff subtraction, per-scope
// rail/agree serialization and preservation of non-empty composer text on Agree
// send refusal, skip-link focus without URL/history mutation, 2-card Story reload +
// uncached popstate Forward preservation, and #highbay-discard-all-drafts cleanup.
func TestViewsPageStudioMultiChangeIsolationAndHistory(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFiles(t, workspace, validWorkspaceStub("package design\n"))
	isoTeam := &teamServiceFixture{
		snapshot: pageTeamSnapshot("waiting"),
		send: func(_ context.Context, p *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error) {
			return fixtureTeamMessage(p.Text), nil
		},
	}
	cp := newBuildControlPlane(t, Options{
		WorkspaceDir: workspace,
		Team:         &TeamBinding{Owner: pageOwner, Service: isoTeam},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond

	guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
	user := asCaller(pageOwner, CallerUser)
	opA := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	fastOpt := "fast"
	cardA, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID:         "chg-iso-a",
		StoryRefreshOpID: &opA,
		Prompt:           "First isolated change.",
		Analysis: &genhighbay.CP1Analysis{
			Summary: "First isolated change.",
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{
				{Question: "Mode?", Options: []string{"fast", "safe"}, DraftOption: &fastOpt},
			},
			Examples: []*genhighbay.CP1AnalysisExample{
				{Scenario: "Scenario A", InputSummary: "in-a", ExpectedOutcome: "out-a", VerificationKind: "deterministic"},
			},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)
	_, err = cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
		ChangeID:        cardA.ChangeID,
		RequestRevision: cardA.RequestRevision,
		Answers:         map[string]string{cardA.Ambiguities[0].ID: "fast"},
		Examples:        cardA.Examples,
	})
	require.NoError(t, err)

	opB := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	cardB, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID:         "chg-iso-b",
		StoryRefreshOpID: &opB,
		Prompt:           "Second isolated change.",
		Analysis: &genhighbay.CP1Analysis{
			Summary:     "Second isolated change.",
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{},
			Examples: []*genhighbay.CP1AnalysisExample{
				{Scenario: "Scenario B1", InputSummary: "in-b1", ExpectedOutcome: "out-b1", VerificationKind: "deterministic"},
				{Scenario: "Scenario B2", InputSummary: "in-b2", ExpectedOutcome: "out-b2", VerificationKind: "deterministic"},
			},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)
	confirmedB, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
		ChangeID:        cardB.ChangeID,
		RequestRevision: cardB.RequestRevision,
		Answers:         map[string]string{},
		Examples:        cardB.Examples,
	})
	require.NoError(t, err)

	writeWorkspaceFiles(t, filepath.Join(workspace, draftFolderName), map[string]string{"design/design.go": "package design // iso-b-v2\n"})
	opB2 := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	priorBEx1 := confirmedB.Examples[0].ID
	priorBEx2 := confirmedB.Examples[1].ID
	_, err = cp.ReviseChange(guide, &genhighbay.ReviseChangePayload{
		ChangeID:         cardB.ChangeID,
		RequestRevision:  confirmedB.RequestRevision,
		StoryRefreshOpID: &opB2,
		Prompt:           "Second isolated change proposed v2.",
		RevisionNote:     "Update expected outcome.",
		Analysis: &genhighbay.CP1RevisionAnalysis{
			Summary:     "Second isolated change proposed v2.",
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{},
			Examples: []*genhighbay.CP1RevisionAnalysisExample{
				{PriorID: &priorBEx1, Scenario: "Scenario B1", InputSummary: "in-b1", ExpectedOutcome: "out-b1-adopted", VerificationKind: "deterministic"},
				{PriorID: &priorBEx2, Scenario: "Scenario B2", InputSummary: "in-b2", ExpectedOutcome: "out-b2", VerificationKind: "deterministic"},
			},
			RemovedExampleIds:  []string{},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)

	ps := startPageServer(t, cp)
	ps.act("touch-b", func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		rec, ok, err := cp.cards.load("chg-iso-b")
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, cp.cards.save(rec))
	})

	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-open-change-chg-iso-a") && page.$("#highbay-open-change-chg-iso-b"), "both changes did not list");

// 1. Open A -> Agree, type draft on A; hold POST /changes/confirm (409 failure), switch to B, release -> error stays on A, B undisturbed!
page.$("#highbay-open-change-chg-iso-a").click();
await until(() => page.$("#highbay-checkpoint-view").dataset.changeId === "chg-iso-a" && page.state() === "build", "chg-iso-a did not open on Build");
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a Agree did not open");
expect(page.text("#highbay-nav-cp1").includes("completed"), "confirmed Agree step omitted accessible screen-reader status");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "draft-only-on-A");
expect(page.text("#highbay-nav-cp1").includes("unsaved edits"), "dirty Agree step omitted unsaved accessible status");

await control(origin, "reply", { key: "POST /changes/confirm", status: 409, body: { name: "conflict", id: "c-a", message: "Simulated conflict on A.", temporary: false, timeout: false, fault: false } });
await control(origin, "hold", { key: "POST /changes/confirm" });
page.$("#highbay-confirm-submit").click();
await until(() => page.sent("POST", "/changes/confirm").some((r) => r.status === undefined), "chg-iso-a confirm was not held");

// Navigate to B while A's confirm is in flight.
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-b").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b" && page.$("#highbay-proposal"), "chg-iso-b proposal did not open");
await control(origin, "release", { key: "POST /changes/confirm" });
await until(() => page.sent("POST", "/changes/confirm")[0]?.status === 409, "chg-iso-a 409 confirm did not settle");
expect(page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b" && page.win.history.state.changeId === "chg-iso-b" && !page.$("#highbay-confirm-error"), "late failure on A wrote #highbay-confirm-error onto B or hijacked view");

// 2. On B, use answer-later to compute a pre-adopt R1+proposal GET /changes/chg-iso-b BEFORE adopting, then release after adopt!
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="expected_outcome"]'), "out-b1-adopted");
page.type(page.$('textarea[data-example-id="check_02"][data-field="scenario"]'), "Scenario B2 local edit");
await control(origin, "answer-later", { key: "GET /changes/chg-iso-b" });
await control(origin, "act", { key: "touch-b" });
await until(() => page.sent("GET", "/changes/chg-iso-b").some((r) => r.status === undefined), "pre-adopt answer-later GET /changes/chg-iso-b did not start");
page.$("#highbay-proposal-adopt").click();
await until(() => !page.$("#highbay-proposal"), "chg-iso-b proposal did not adopt");
await control(origin, "release", { key: "GET /changes/chg-iso-b" });
await until(() => page.sent("GET", "/changes/chg-iso-b").every((r) => r.status === 200), "delayed pre-adopt GET /changes/chg-iso-b did not finish");
expect(!page.$("#highbay-incoming-change-card") && !page.$("#highbay-proposal"), "stale pre-adopt get_change was applied as incomingCard or resurrected #highbay-proposal");
expect(page.$("#highbay-preserved-draft") && page.text("#highbay-preserved-draft").includes("Scenario B2 local edit") && !page.text("#highbay-preserved-draft").includes("out-b1-adopted"), "handleAdopt did not subtract examples already satisfied by the adopted revision");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_02"][data-field="scenario"]'), "Scenario B2 post-adopt dirty");

// 3. Reopen A -> A's draft AND A's earlier 409 #highbay-confirm-error are intact; now hold a 200 confirm on A, switch to B, release -> B stays open!
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-a").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a did not reopen on Agree");
expect(page.$('textarea[data-field="expected_outcome"]').value === "draft-only-on-A" && page.$("#highbay-confirm-error") && page.text("#highbay-confirm-error").includes("Simulated conflict on A."), "A lost its draft or 409 formError");

await control(origin, "hold", { key: "POST /changes/confirm" });
page.$("#highbay-confirm-submit").click();
await until(() => page.sent("POST", "/changes/confirm").some((r) => r.status === undefined), "second confirm on A was not held");
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-b").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b", "chg-iso-b did not reopen while A save was pending");
await control(origin, "release", { key: "POST /changes/confirm" });
await until(() => page.sent("POST", "/changes/confirm").at(-1)?.status === 200, "second confirm on A did not complete");
expect(page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b" && page.win.history.state.changeId === "chg-iso-b", "late 200 confirm on A hijacked visible sibling change B");

// 3b. Same-change stage preservation: confirm A on Agree, navigate to A's Build before confirm settles -> stays on Build!
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-a").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a Agree did not reopen for same-change stage test");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "saved-while-viewing-build");
await control(origin, "hold", { key: "POST /changes/confirm" });
page.$("#highbay-confirm-submit").click();
await until(() => page.sent("POST", "/changes/confirm").some((r) => r.status === undefined), "third confirm on A was not held");
page.$("#highbay-nav-build").click();
await until(() => page.state() === "build", "chg-iso-a Build did not open while confirm was in flight");
await control(origin, "release", { key: "POST /changes/confirm" });
await until(() => page.sent("POST", "/changes/confirm").at(-1)?.status === 200, "third confirm on A did not complete");
expect(page.state() === "build" && page.win.history.state.stage === "build", "late confirm on A hijacked A's Build stage back to Agree");

// 3c. Cross-scope async rail success, per-scope rail+agree serialization, & preserving non-empty composer text on Agree refusal!
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-b").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b", "chg-iso-b Agree did not open for composer isolation");
page.type(page.$("#highbay-composer-input"), "Unsent composer note on B");

page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-a").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a Agree did not open for composer isolation");
expect(page.$("#highbay-composer-input").value === "", "chg-iso-a inherited B's composer draft");
page.type(page.$("#highbay-composer-input"), "Sent rail note from A");
await control(origin, "hold", { key: "POST /team/messages" });
page.$("#highbay-composer-send").click();
await until(() => page.sent("POST", "/team/messages").some((r) => r.status === undefined), "rail send on A was not held");
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-b").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b", "chg-iso-b did not reopen while A rail send was held");
expect(page.$("#highbay-composer-input").value === "Unsent composer note on B" && !page.$("#highbay-composer-send").disabled, "in-flight rail send on A clobbered or locked B's composer");
await control(origin, "release", { key: "POST /team/messages" });
await until(() => page.sent("POST", "/team/messages").at(-1)?.status === 201, "held rail send on A did not settle");
expect(page.$("#highbay-composer-input").value === "Unsent composer note on B", "settled rail send on A cleared B's composer draft");

page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-a").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a did not reopen after rail send");
expect(page.$("#highbay-composer-input").value === "", "settled rail send on A stayed cached in A's scope");
page.type(page.$("#highbay-composer-input"), "Pre-existing note on A");
page.$('button[data-option="safe"]').click();
await until(() => page.$("#highbay-confirm-revision-needed-send"), "A RevisionAid did not appear");
await control(origin, "reply", { key: "POST /team/messages", status: 422, body: { name: "unprocessable", id: "u-iso", message: "Refused A agree send.", temporary: false, timeout: false, fault: false } });
await control(origin, "hold", { key: "POST /team/messages" });
page.$("#highbay-confirm-revision-needed-send").click();
await until(() => page.sent("POST", "/team/messages").some((r) => r.status === undefined), "agree send on A was not held");
expect(page.$("#highbay-composer-send").disabled && page.$("#highbay-confirm-revision-needed-send").disabled && page.$("#highbay-confirm-revision-needed-compose").disabled, "in-flight Agree send did not serialize rail and Agree controls in A's scope");
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-b").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b", "chg-iso-b did not reopen while A agree send was held");
await control(origin, "release", { key: "POST /team/messages" });
await until(() => page.sent("POST", "/team/messages").at(-1)?.status === 422, "held 422 agree send on A did not settle");
expect(page.$("#highbay-composer-input").value === "Unsent composer note on B" && !page.$("#highbay-composer-context-chip") && !page.$("#highbay-composer-error"), "rejected agree send on A overwrote B's draft or injected A's error");

page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-a").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a did not reopen after refused agree send");
expect(page.$("#highbay-composer-input").value === "Pre-existing note on A" && !page.$("#highbay-composer-context-chip") && page.$("#highbay-composer-error") && page.text("#highbay-composer-error").includes("Refused A agree send."), "rejected agree send on A overwrote A's pre-existing non-empty composer text or omitted error");
page.$('button[data-option="fast"]').click();

// Re-dirty A so both A and B are dirty for #highbay-discard-all-drafts.
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "draft-again-on-A");

// 4. Both A and B are dirty -> deferred #link= offers one-click #highbay-discard-all-drafts and clears composer errors too.
page.win.location.hash = "link=multi-dirty-link";
await until(() => page.$("#highbay-pending-link") && page.$("#highbay-discard-all-drafts"), "multi-change dirty deferred link did not offer #highbay-discard-all-drafts");
expect(page.text("#highbay-pending-link").includes("chg-iso-a") && page.text("#highbay-pending-link").includes("chg-iso-b") && page.text("#highbay-discard-all-drafts") === "Discard all unsaved drafts" && page.$("#highbay-open-pending-link").disabled, "discard-all notice did not name both dirty changes");
page.$("#highbay-discard-all-drafts").click();
await until(() => !page.$("#highbay-open-pending-link").disabled && !page.beforeUnload() && !page.$("#highbay-composer-error"), "Discard all unsaved drafts did not clear dirty editors and composer error");

// 5. Skip link moves focus programmatically without mutating location.hash or window.history.state.
page.$("#highbay-new-change").click();
await until(() => page.$("#highbay-describe-input"), "+ New change did not open");
page.type(page.$("#highbay-describe-input"), "Sensitive prompt never written to history.state");
const stateBeforeSkip = JSON.stringify(page.win.history.state);
expect(!stateBeforeSkip.includes("Sensitive prompt") && page.win.location.pathname === "/views" && page.win.location.hash === "", "prompt text leaked into history.state or mutated URL");
page.$("#highbay-skip-link").click();
expect(page.win.location.hash === "" && JSON.stringify(page.win.history.state) === stateBeforeSkip, "skip link mutated location.hash or pushed null history.state");

// 6. Cold reload on 2-card Story restores activeChangeId + stepper, and history.back() to uncached card A preserves history.forward()!
const reloaded = await openPage(origin, {
  storage: page.win.localStorage,
  historyEntries: [
    { view: "change", changeId: "chg-iso-a", stage: "agree" },
    { view: "story", changeId: "chg-iso-b" },
  ],
});
await until(() => reloaded.state() === "story" && reloaded.$("#highbay-nav-cp1") && reloaded.$("#highbay-result-view")?.dataset.changeId === "chg-iso-b", "2-card Story reload did not restore activeChangeId chg-iso-b and 5-step stepper");
reloaded.win.history.back();
await until(() => reloaded.state() === "agree" && reloaded.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "history.back() did not load uncached chg-iso-a on Agree");
reloaded.win.history.forward();
await until(() => reloaded.state() === "story" && reloaded.$("#highbay-result-view")?.dataset.changeId === "chg-iso-b", "async uncached openChange called pushState and wiped Forward history");

reloaded.close();
page.close();
`)
}

// TestViewsPageStudioActionableFailureRoutesToVerifyAndBuilder verifies that an
// in-force build with an older eligible passed run AND a newer failed run opens
// Verify (!, "needs attention") ahead of Approve, keeps Approve neutral until a
// fresh review is recorded, preserves approval/merge and cold-reload runId on the
// older passed run even when a newer pass arrives, initializes Build cleanly when
// clicking #highbay-nav-build after a Team reload, and promotes Type in terminal
// when a passively watched Builder enters needs_you.
func TestViewsPageStudioActionableFailureRoutesToVerifyAndBuilder(t *testing.T) {
	reportDir := t.TempDir()
	tests := strings.Repeat("2", 40)
	team := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{
		BuildCacheDir:  t.TempDir(),
		RunsDir:        t.TempDir(),
		HighbayVersion: testHighbayVersion,
		WrittenTestRuns: &WrittenTestRuns{
			ReportDir: reportDir,
			Command:   []string{"/bin/sh", "-c", "exit 0", "sh"},
			Timeout:   time.Second,
		},
		Team: &TeamBinding{Owner: pageOwner, Service: team},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := saveBuildStoryCard(t, cp, "chg-verify-fail", StatusConfirmed, refundStory("Refund fails check"))
	user := asCaller(pageOwner, CallerUser)
	started, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	acceptTests(t, cp, card.ChangeID, tests)
	candidateOld := commitBuilderCandidateFixture(t, cp, card)

	at, err := time.Parse(time.RFC3339Nano, started.Build.StartedAt)
	require.NoError(t, err)

	reportOld := passingReport(t, card.RequestRevision, candidateOld, reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: tests, Environment: "sim",
		Artifacts: map[string]string{"orders": strings.Repeat("a", 64)},
	})
	writeReportFile(t, reportDir, reportOld)
	olderPass := passedRun(card, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", candidateOld, reportOld.Digest)
	olderPass.AcceptedAt = at.Add(time.Second).Format(time.RFC3339Nano)
	olderPass.InputsCurrent = true
	olderPass.SuiteCommit = tests
	olderPass.TestSource = genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{
		Commit: tests, Notice: writtenTestsNotice,
	})

	failed := passedRun(card, "ffffffff-ffff-4fff-8fff-ffffffffffff", candidateOld, strings.Repeat("e", 64))
	failed.AcceptedAt = at.Add(2 * time.Second).Format(time.RFC3339Nano)
	failed.InputsCurrent = true
	failed.SuiteCommit = tests
	failed.TestSource = genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{
		Commit: tests, Notice: writtenTestsNotice,
	})
	failed.State = genhighbay.NewStateCompleted(&genhighbay.VerificationCompleted{
		SuiteID:     "orders_acceptance",
		Environment: "sim",
		Report: &genhighbay.BuilderVerificationReport{
			ReportDigest: strings.Repeat("e", 64),
			Passed:       false,
			Counts:       &genhighbay.VerificationCounts{Required: 2, Passed: 1, Failed: 1},
			Scenarios: []*genhighbay.VerificationScenario{
				{ID: "visible_refund", Outcome: "failed", Checks: []*genhighbay.VerificationCheck{{Name: "status_200", Passed: false}}},
				{ID: "holdout-1", Outcome: "passed"},
			},
		},
	})
	_, err = genhighbay.EncodeVerificationStatusResult(failed)
	require.NoError(t, err)

	reportNew := passingReport(t, card.RequestRevision, candidateOld, reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: tests, Environment: "sim",
		Artifacts: map[string]string{"orders": strings.Repeat("b", 64)},
	})
	writeReportFile(t, reportDir, reportNew)
	newerPass := passedRun(card, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", candidateOld, reportNew.Digest)
	newerPass.AcceptedAt = at.Add(3 * time.Second).Format(time.RFC3339Nano)
	newerPass.InputsCurrent = true
	newerPass.SuiteCommit = tests
	newerPass.TestSource = genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{
		Commit: tests, Notice: writtenTestsNotice,
	})

	afterTests, err := cp.GetBuild(user, &genhighbay.GetBuildPayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	acceptedBuild, ok := afterTests.Build.AsStarted()
	require.True(t, ok)
	progress := *acceptedBuild
	progress.Taking = genhighbay.NewBuildTakingPassed(&genhighbay.BuildPassed{
		RunID:           olderPass.RunID,
		CandidateCommit: candidateOld,
		CandidateBranch: "highbay/build-1",
		Tests: genhighbay.NewBuildPassedTestSourceWrittenTests(&genhighbay.BuildPassedTests{
			Commit: tests,
			Branch: "highbay/tests-1",
		}),
		BaseInBranch: true,
	})

	verifications := newPageVerifications(cp)
	verifications.set(olderPass, failed)
	verifications.list(card.ChangeID, failed.RunID, olderPass.RunID)
	svc := &pageStudioFailureFixture{
		pageVerifications: verifications,
		changeID:          card.ChangeID,
		latest: &genhighbay.LatestVerificationItem{
			RunID:         failed.RunID,
			State:         "failed",
			InputsCurrent: true,
		},
		build: &genhighbay.ChangeBuild{
			ChangeID: card.ChangeID,
			Build:    genhighbay.NewBuildStarted(&progress),
		},
	}

	ps := startPageServer(t, svc)
	ps.act("add-newer-pass", func() {
		verifications.set(newerPass)
		verifications.list(card.ChangeID, newerPass.RunID, failed.RunID, olderPass.RunID)
		svc.latest = &genhighbay.LatestVerificationItem{
			RunID:         newerPass.RunID,
			State:         "passed",
			InputsCurrent: true,
		}
	})
	ps.act("builder-needs-you", func() {
		team.mu.Lock()
		defer team.mu.Unlock()
		roles := make([]*genhighbay.TeamRoleState, len(team.snapshot.Roles))
		for i, r := range team.snapshot.Roles {
			c := *r
			roles[i] = &c
		}
		for _, r := range roles {
			if r.Role == "builder" {
				r.State = "needs_you"
				r.Attention = "question"
			}
		}
		team.snapshot.Roles = roles
	})

	runPageScript(t, ps, map[string]any{"oldRun": olderPass.RunID, "failRun": failed.RunID, "newRun": newerPass.RunID}, `
// Hold GET /verifications/<failRun> on initial open to verify interactive Build controls never flash before routing to Verify!
const failRunPath = "/verifications/" + input.failRun;
await control(origin, "hold", { key: "GET " + failRunPath });
const page = await openPage(origin, { link: input.link });
await until(() => page.sent("GET", "/changes/chg-verify-fail/build").some((r) => r.status === 200) && page.sent("GET", failRunPath).some((r) => r.status === undefined) && page.$("#highbay-build-loading"), "default-stage check resolution did not hold on loading state after build settled");
expect(!page.$("#highbay-build-start") && !page.$("#highbay-build-review-check"), "interactive Build controls flashed while default-stage verification read was still settling");
await control(origin, "release", { key: "GET " + failRunPath });

// 1. Even though build.taking.state === "passed" from olderPass, newer current-build failed run opens Verify (!, needs attention) with #highbay-cp4-message-builder as sole primary action!
await until(() => page.state() === "verify" && page.$("#highbay-result-view")?.dataset.runId === input.failRun && page.$("#highbay-cp4-message-builder"), "current-build failed run beside older pass did not open Verify with #highbay-cp4-message-builder");
expect(page.$("#highbay-cp4-message-builder").dataset.primaryAction === "true" && !page.$("#highbay-verify-review-result") && page.$$('[data-primary-action="true"]').length === 1, "#highbay-cp4-message-builder must be the sole primary action on a failed current-build check");
expect(page.text("#highbay-nav-cp4").includes("!") && page.text("#highbay-nav-cp4").includes("needs attention"), "Verify stepper pill omitted ! glyph or screen-reader status");
expect(page.$("#highbay-nav-cp5").dataset.stepStatus === "neutral" && !page.text("#highbay-nav-cp5").includes("✓"), "Approve showed ✓ before any recorded review");
page.$("#highbay-cp4-message-builder").click();
expect(page.doc.activeElement === page.$("#highbay-composer-input") && page.$("#highbay-composer-to").value === "builder", "Message Builder did not focus composer addressed to builder");

// 2. Select olderPass, open Approve, approve olderPass, and verify olderPass keeps merge action even after newerPass arrives AND across cold reload!
await until(() => page.$('button[data-run-id="' + input.oldRun + '"]'), "olderPass was not listed in Verify history");
page.$('button[data-run-id="' + input.oldRun + '"]').click();
page.$("#highbay-nav-cp5").click();
await until(() => page.state() === "approve" && page.$("#highbay-result-view")?.dataset.runId === input.oldRun && page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "olderPass did not become eligible for approval on Approve");
page.$("#highbay-cp5-approve").click();
await until(() => page.$("#highbay-cp5-approval") && page.$("#highbay-cp5-merge") && !page.$("#highbay-cp5-merge").disabled, "approving olderPass did not enable #highbay-cp5-merge");

await control(origin, "act", { key: "add-newer-pass" });
page.$("#highbay-load-verifications").click();
await until(() => page.$('button[data-run-id="' + input.newRun + '"]') && page.$("#highbay-cp5-merge") && !page.$("#highbay-cp5-merge").disabled, "arriving newerPass clobbered selected olderPass approval/merge");
expect(page.$("#highbay-result-view")?.dataset.runId === input.oldRun, "arriving newerPass changed selectedRunId away from olderPass");

// Cold reload on 1-card Approve with olderPass runId preserves exact olderPass (and cold reload on Architecture preserves arch)!
const reloadApprove = await openPage(origin, {
  storage: page.win.localStorage,
  historyState: { view: "change", changeId: "chg-verify-fail", stage: "approve", runId: input.oldRun },
});
await until(() => reloadApprove.state() === "approve" && reloadApprove.$("#highbay-result-view")?.dataset.runId === input.oldRun && reloadApprove.$("#highbay-cp5-merge") && !reloadApprove.$("#highbay-cp5-merge").disabled, "cold reload on Approve lost olderPass runId");
reloadApprove.close();

const reloadArch = await openPage(origin, {
  storage: page.win.localStorage,
  historyState: { view: "change", changeId: "chg-verify-fail", stage: "arch" },
});
await until(() => reloadArch.state() === "arch", "1-card cold reload on Architecture was clobbered by auto-open");
reloadArch.close();

// Reload on Team: clicking #highbay-nav-build after background loadCardIntoCache reads the build (never "build has not been read")!
const reloadTeamBuild = await openPage(origin, {
  storage: page.win.localStorage,
  historyState: { view: "team" },
});
await until(() => reloadTeamBuild.state() === "team" && reloadTeamBuild.$("#highbay-nav-build") && !reloadTeamBuild.$("#highbay-nav-build").disabled, "Team reload did not enable #highbay-nav-build");
reloadTeamBuild.$("#highbay-nav-build").click();
await until(() => reloadTeamBuild.state() === "build" && reloadTeamBuild.sent("GET", "/changes/chg-verify-fail/build").some((r) => r.status === 200) && reloadTeamBuild.$("#highbay-build-section")?.dataset.state === "started", "clicking #highbay-nav-build from Team reload did not read and render the started build");
expect(!reloadTeamBuild.text("#highbay-build-section").includes("has not been read"), "Team -> Build trapped on unread build state");
reloadTeamBuild.close();

// Reload on Team while holding background loadCardIntoCache GET /changes/chg-verify-fail, then click Changes -> Open:
// openChange must consume the in-flight read instead of dropping the user's click!
await control(origin, "hold", { key: "GET /changes/chg-verify-fail" });
const reloadTeamOpen = await openPage(origin, {
  storage: page.win.localStorage,
  historyState: { view: "team" },
});
await until(() => reloadTeamOpen.state() === "team" && reloadTeamOpen.sent("GET", "/changes/chg-verify-fail").some((r) => r.status === undefined), "Team reload did not start held loadCardIntoCache read");
reloadTeamOpen.$("#highbay-nav-changes").click();
await until(() => reloadTeamOpen.$("#highbay-open-change-chg-verify-fail"), "Changes row did not render while background card read was held");
reloadTeamOpen.$("#highbay-open-change-chg-verify-fail").click();
await control(origin, "release", { key: "GET /changes/chg-verify-fail" });
await until(() => reloadTeamOpen.state() === "approve", "clicking Open while loadCardIntoCache was in flight dropped the open");
reloadTeamOpen.close();

// Transient 503 reading restored olderPass on Verify must keep olderPass selected (never jump to newerPass),
// and restored mobileSurface="terminal" with no open terminal must normalize to stage!
const oldRunPath = "/verifications/" + input.oldRun;
await control(origin, "reply", { key: "GET " + oldRunPath, status: 503, body: { name: "internal", id: "v503", message: "Transient verification read outage.", temporary: true, timeout: false, fault: true } });
const reloadTransient = await openPage(origin, {
  storage: page.win.localStorage,
  historyState: { view: "change", changeId: "chg-verify-fail", stage: "verify", runId: input.oldRun, mobileSurface: "terminal" },
});
await until(() => reloadTransient.state() === "verify" && reloadTransient.sent("GET", oldRunPath).some((r) => r.status === 503) && reloadTransient.sent("GET", "/changes/chg-verify-fail/verifications").some((r) => r.status === 200), "Verify reload with transient 503 did not settle");
expect(!reloadTransient.$("#highbay-checkpoint-view").hidden && reloadTransient.win.history.state.mobileSurface === undefined, "restored mobileSurface='terminal' without open terminal hid the stage");
expect(reloadTransient.$("#highbay-result-view")?.dataset.runId === undefined || reloadTransient.$("#highbay-result-view")?.dataset.runId === input.oldRun, "transient 503 replaced restored oldRun with cached newerPass");
expect(reloadTransient.win.history.state.runId === input.oldRun, "transient 503 overwrote history.state.runId");
reloadTransient.close();

// 3. Passively watching Builder terminal (observe=true) when Builder enters needs_you promotes #highbay-terminal-interactive as primary!
page.$("#highbay-rail-terminal-builder").click();
await until(() => page.$("#highbay-terminal-drawer")?.dataset.role === "builder" && page.$("#highbay-terminal-drawer")?.dataset.observe === "true", "Builder terminal did not open in passive observe=true mode");
await control(origin, "act", { key: "builder-needs-you" });
page.$("#highbay-nav-team").click();
page.$("#highbay-team-refresh").click();
await until(() => page.$("#highbay-team-attention-terminal-builder"), "Builder needs_you banner did not appear");
expect(page.$("#highbay-terminal-interactive")?.dataset.primaryAction === "true" && !page.text("#highbay-team-attention-terminal-builder").includes("terminal open") && page.$$('[data-primary-action="true"]').length === 1, "passive terminal entering needs_you did not highlight Type in terminal as sole primary action");

page.close();
`)
}

// ListChanges returns the control plane's change list with the fixture's latest verification summary.
func (s *pageStudioFailureFixture) ListChanges(ctx context.Context) (*genhighbay.ChangeList, error) {
	res, err := s.pageVerifications.ListChanges(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range res.Changes {
		if item.ChangeID == s.changeID {
			item.LatestVerification = s.latest
		}
	}
	return res, nil
}

// GetBuild returns the fixture's started build when configured.
func (s *pageStudioFailureFixture) GetBuild(ctx context.Context, p *genhighbay.GetBuildPayload) (*genhighbay.ChangeBuild, error) {
	if s.build != nil && p.ChangeID == s.changeID {
		return s.build, nil
	}
	return s.pageVerifications.GetBuild(ctx, p)
}
