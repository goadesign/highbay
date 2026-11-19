// This file binds taking guidance to each saved result's own inputs through
// the served page. Saved runs and taking state are explicit owner fixtures.
package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
	"github.com/stretchr/testify/require"
)

// pageTakingHistory supplies one typed build observation alongside the existing
// verification fixture. Card, approval and HTTP ownership stay with the owner.
type pageTakingHistory struct {
	*pageVerifications
	build *gensurla.ChangeBuild
}

// GetBuild returns the explicit taking observation for this fixture's change.
func (s *pageTakingHistory) GetBuild(
	ctx context.Context, p *gensurla.GetBuildPayload,
) (*gensurla.ChangeBuild, error) {
	if p.ChangeID != s.build.ChangeID {
		return s.ControlPlane.GetBuild(ctx, p)
	}
	return s.build, nil
}

// TestViewsPageHistoricalTakingUsesTheEligibleResultsOwnCommits covers an
// older pass and approval after a newer pass, a prior start reusing identical
// commits, and an initial approval page whose automatic history read fails.
func TestViewsPageHistoricalTakingUsesTheEligibleResultsOwnCommits(t *testing.T) {
	for _, mode := range []string{"eligible", "prior_start", "prior_start_merged", "merged_evaluator", "before_start", "at_start", "after_start", "history_failure"} {
		t.Run(mode, func(t *testing.T) {
			reportDir := t.TempDir()
			tests := strings.Repeat("2", 40)
			opts := Options{BuildCacheDir: t.TempDir(),
				RunsDir: t.TempDir(), SurlaVersion: testSurlaVersion,
				WrittenTestRuns: &WrittenTestRuns{
					ReportDir: reportDir, Command: []string{"/bin/sh", "-c", "exit 0", "sh"}, Timeout: time.Second,
				},
			}
			if mode == "merged_evaluator" {
				opts.WrittenTestRuns = nil
				opts.Verification = &VerificationConfig{
					RunsDir: t.TempDir(), Plan: verify.Plan{SuiteCommit: tests, ReportDir: reportDir},
				}
			}
			cp := newBuildControlPlane(t, opts)
			cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
			card := saveBuildStoryCard(t, cp, "chg-history", StatusConfirmed, refundStory("Refund waits"))
			user := asCaller(pageOwner, CallerUser)
			started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
			})
			require.NoError(t, err)
			if mode != "merged_evaluator" {
				acceptTests(t, cp, card.ChangeID, tests)
			}
			candidate := commitBuilderCandidateFixture(t, cp, card)
			report := passingReport(t, card.RequestRevision, candidate, reportSpec{
				SuiteID: "orders_acceptance", SuiteCommit: tests, Environment: "sim",
				Artifacts: map[string]string{"orders": strings.Repeat("a", 64)},
			})
			writeReportFile(t, reportDir, report)
			_, err = cp.ApproveMerge(user, &gensurla.ApproveMergePayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
				CandidateCommit: candidate, ReportDigest: report.Digest,
			})
			require.NoError(t, err)
			at, err := time.Parse(time.RFC3339Nano, started.Build.StartedAt)
			require.NoError(t, err)
			at = at.Truncate(time.Second).Add(123456789 * time.Nanosecond)
			old := passedRun(card, strings.Repeat("a", 32), candidate, report.Digest)
			old.AcceptedAt = at.Add(time.Second).Format(time.RFC3339Nano)
			old.InputsCurrent = true
			old.TestSource = gensurla.NewTestSourceWrittenTests(&gensurla.AgentWrittenTests{
				Commit: tests, Notice: writtenTestsNotice,
			})
			newest := passedRun(card, strings.Repeat("b", 32), strings.Repeat("3", 40), strings.Repeat("d", 64))
			newest.AcceptedAt = at.Add(2 * time.Second).Format(time.RFC3339Nano)
			newest.SuiteCommit = strings.Repeat("4", 40)
			newest.TestSource = gensurla.NewTestSourceWrittenTests(&gensurla.AgentWrittenTests{
				Commit: newest.SuiteCommit, Notice: writtenTestsNotice,
			})
			if mode == "prior_start" || mode == "prior_start_merged" {
				old.AcceptedAt = at.Add(-time.Second).Format(time.RFC3339Nano)
				newest.CandidateCommit, newest.SuiteCommit = candidate, tests
				newest.TestSource = old.TestSource
			}
			switch mode {
			case "before_start":
				old.AcceptedAt = at.Add(-time.Nanosecond).Format(time.RFC3339Nano)
			case "at_start":
				old.AcceptedAt = at.Format(time.RFC3339Nano)
			case "after_start":
				old.AcceptedAt = at.Add(time.Nanosecond).Format(time.RFC3339Nano)
			}
			progress := *started.Build
			progress.StartedAt = at.Format(time.RFC3339Nano)
			progress.Taking = gensurla.NewBuildTakingPassed(&gensurla.BuildPassed{
				RunID: newest.RunID, CandidateCommit: newest.CandidateCommit, CandidateBranch: "surla/build-7",
				Tests: gensurla.NewBuildPassedTestSourceWrittenTests(&gensurla.BuildPassedTests{
					Commit: newest.SuiteCommit, Branch: "surla/tests-7",
				}),
				BaseInBranch: false,
			})
			if mode == "prior_start_merged" || mode == "merged_evaluator" {
				progress.Taking = gensurla.NewBuildTakingMerged(&gensurla.BuildMerged{})
			}
			if mode == "merged_evaluator" {
				old.TestSource = gensurla.NewTestSourceEvaluator(&gensurla.OperatorEvaluatorTests{})
				progress.TestWriter.Tests = gensurla.NewTestsNotUsed(&gensurla.TestsNotUsed{})
			}
			svc := &pageTakingHistory{pageVerifications: newPageVerifications(cp), build: &gensurla.ChangeBuild{
				ChangeID: card.ChangeID, Build: gensurla.NewBuildStarted(&progress),
			}}
			svc.set(old, newest)
			svc.list(card.ChangeID, newest.RunID, old.RunID)
			ps := startPageServer(t, svc)
			ps.act("resave-history", func() {
				cp.mu.Lock()
				defer cp.mu.Unlock()
				record, ok, err := cp.cards.load(card.ChangeID)
				require.NoError(t, err)
				require.True(t, ok)
				require.NoError(t, cp.cards.save(record))
			})
			eligible := mode == "eligible" || mode == "at_start" || mode == "after_start"
			runPageScript(t, ps, map[string]any{"mode": mode, "old": old.RunID, "eligible": eligible, "candidate": candidate, "revision": card.RequestRevision, "digest": report.Digest}, `
const history = "/changes/chg-history/verifications";
if (input.mode === "history_failure") await control(origin, "reply", { key: "GET " + history, status: 503,
  body: { name: "unavailable", id: "history-read", message: "Saved verification history unavailable.", fault: false, temporary: true, timeout: false } });
else await control(origin, "hold", { key: "GET " + history });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-review-approval") && page.sent("GET", history).length === 1, "initial saved approval did not automatically read its verification history");
page.$("#surla-review-approval").click();
await until(() => page.$("#surla-cp5-approval"), "the saved approval stage did not open");
if (input.mode !== "history_failure") {
  expect(page.text("#surla-approval-stage").includes("Reading saved verification runs"), "held history was not explained");
  expect(!page.text("#surla-cp5-approval").includes("git merge"), "unread history invented build membership");
  expect(!page.$("#surla-cp5-merge") || page.$("#surla-cp5-merge").disabled, "unread history authorized a merge");
  await control(origin, "release", { key: "GET " + history });
}
await until(() => page.sent("GET", history)[0].status !== undefined && !page.text("#surla-approval-stage").includes("Reading saved verification runs") && (!page.$("#surla-approval-stage").dataset.runId || !page.$("#surla-cp5-observation-status")), "history read did not settle");
const ownCommands = (region) => Array.from(region.querySelectorAll(".surla-build-merge li > pre > code"), (node) => node.textContent);
const expected = ["git merge " + input.candidate, "git merge " + "2".repeat(40)];
if (input.eligible) {
  expect(JSON.stringify(ownCommands(page.$(".surla-approval-basis") ?? page.$("#surla-cp5-approval"))) === JSON.stringify(expected), "recorded approval used the newest pass instead of its own C then T");
  expect(page.text(".surla-approval-basis").includes("commit where this build started"), "eligible approval lost the rewrite warning");
} else {
  expect(ownCommands(page.$(".surla-approval-basis") ?? page.$("#surla-cp5-approval")).length === 0, "unknown or prior-start approval claimed current-build membership");
  expect(!page.$("#surla-cp5-merge") || page.$("#surla-cp5-merge").disabled, "unknown or prior-start approval authorized a merge");
  if (input.mode === "merged_evaluator") {
    expect(page.text("#surla-frame-next").includes("Describe the next change when you are ready"), "merged evaluator run did not show next-change guidance");
    expect(page.text(".surla-approval-basis").includes("This saved result names the builder’s commit " + input.candidate + ". This build is merged into your branch; this verification covered these commits, not your branch after the merge."), "merged evaluator run lost its no-tests merged explanation");
    expect(!page.text(".surla-approval-basis").includes("Review and approve this result before merging; a saved passing check alone does not grant approval."), "merged evaluator run kept the pre-merge fallback");
  } else {
    expect(!page.text("#surla-frame-next").includes("Merge the verified commits") && !page.text("#surla-frame-next").includes("Describe the next change"), "unrelated historical evidence borrowed the current build's next step");
    if (input.mode === "prior_start_merged") {
      expect(page.text(".surla-approval-basis").includes("This saved result names the builder’s commit " + input.candidate + " and the tests’ commit " + "2".repeat(40) + ". This build is merged into your branch; this verification covered these commits, not your branch after the merge."), "prior-start run on a merged build lost the build-level merged state");
      expect(!page.text(".surla-approval-basis").includes("This run verified"), "prior-start run on a merged build claimed its own run was merged");
    } else if (input.mode !== "history_failure") {
      expect(page.text(".surla-approval-basis").includes("This saved result names the builder’s commit " + input.candidate + " and the tests’ commit " + "2".repeat(40) + ". Review and approve this result before merging; a saved passing check alone does not grant approval."), "unmerged run lost its fallback");
    }
  }
  if (input.mode === "history_failure") {
    expect(page.text("#surla-approval-stage").includes("Saved verification history unavailable") && page.text("#surla-approval-stage").includes("membership is unknown"), "failed automatic history read lost its uncertainty");
    await sleep(100);
    expect(page.sent("GET", history).length === 1, "failed history read retried in a loop");
  }
}
const checkCommandRemoval = async (region, fallback, external) => {
  const path = "/changes/chg-history/build", before = page.sent("GET", path).length;
  await control(origin, "hold", { key: "GET " + path });
  (external ?? region.querySelector(".surla-build-merge pre")).focus();
  await control(origin, "act", { key: "resave-history" });
  await until(() => page.sent("GET", path).length > before && ownCommands(region).length === 0, "held build refresh did not remove uncertain commands");
  expect(page.doc.activeElement === (external ?? fallback), "removed result commands lost local fallback or stole external focus: " + JSON.stringify({ active: page.doc.activeElement?.id, tag: page.doc.activeElement?.tagName, fallback: fallback?.id, external: external?.id }));
  await control(origin, "release", { key: "GET " + path });
  await until(() => ownCommands(region).length === 2 && !page.$("#surla-cp5-refresh-approval")?.disabled, "fresh build read did not restore its own result commands and settle review");
};
if (input.mode === "eligible") {
  const region = page.$(".surla-approval-basis");
  await checkCommandRemoval(region, page.$("#surla-cp5-title"), null);
  const external = page.$("#surla-cp5-refresh-approval");
  await checkCommandRemoval(region, page.$("#surla-cp5-title"), external);
}
if (input.mode !== "history_failure") {
  page.$("#surla-nav-cp4").click();
  await until(() => page.$('button[data-run-id="' + input.old + '"]'), "saved old run was not listed");
  page.$('button[data-run-id="' + input.old + '"]').click();
  await until(() => page.$("#surla-result-view").dataset.runId === input.old && page.text("#surla-cp4-inputs").includes("This result matches"), "old pass did not open");
  const commands = ownCommands(page.$("#surla-cp4-details"));
  expect((commands.length > 0) === input.eligible, "old pass membership did not follow its saved start");
  if (input.eligible) expect(JSON.stringify(commands) === JSON.stringify(expected), "older pass did not keep its own C then T");
  if (input.mode === "merged_evaluator") {
    expect(page.text("#surla-cp4-details").includes("This saved result names the builder’s commit " + input.candidate + ". This build is merged into your branch; this verification covered these commits, not your branch after the merge."), "check view lost the no-tests merged explanation");
  }
  if (input.mode === "prior_start_merged") {
    expect(page.text("#surla-cp4-details").includes("This saved result names the builder’s commit " + input.candidate + " and the tests’ commit " + "2".repeat(40) + ". This build is merged into your branch; this verification covered these commits, not your branch after the merge."), "check view lost the build-level merged state");
  }
  if (input.mode === "prior_start") {
    expect(page.text("#surla-cp4-details").includes("This saved result names the builder’s commit " + input.candidate + " and the tests’ commit " + "2".repeat(40) + ". Review and approve this result before merging; a saved passing check alone does not grant approval."), "check view lost the unmerged fallback");
  }
  if (input.mode === "eligible") {
    const region = page.$("#surla-cp4-details"); region.open = true;
    await checkCommandRemoval(region, page.$("#surla-cp4-title"), null);
    await checkCommandRemoval(region, page.$("#surla-cp4-title"), page.$("#surla-load-verifications"));
  }
}
if (input.mode === "eligible") {
  page.$("#surla-nav-build").click();
  await until(() => page.$("#surla-build-merge") && !page.$("#surla-build-merge").disabled, "Build omitted the same live-approved merge action");
  expect(page.text("#surla-build-merge") === "Merge into current branch", "Build merge action did not name its destination");
  page.$("#surla-nav-cp5").click();
  await until(() => page.$("#surla-cp5-merge") && !page.$("#surla-cp5-merge").disabled, "older approved result did not retain its merge action after a newer pass");
  expect(page.text("#surla-cp5-merge") === "Merge into current branch", "merge action did not name its destination");
  expect(page.$$(".surla-btn-primary").filter((button) => button.textContent === "Merge into current branch").length === 1, "approved view duplicated its primary merge action");
  const manual = page.$("#surla-cp5-approval details.surla-build-merge");
  expect(manual && !manual.open && manual.querySelector("summary").textContent === "Manual merge instructions", "manual commands were not secondary to the human action");
  const mergePath = "/changes/chg-history/merge";
  const refusals = [
    ["dirty_worktree", "Tracked changes need your attention."],
    ["detached_head", "Choose the branch that should receive this result."],
    ["merge_conflict", "Git could not merge this result."],
    ["not_approved", "This result no longer has current approval."],
    ["working_copy_busy", "Another project operation is using the working copy."],
    ["unavailable", "The merge outcome could not be confirmed."],
  ];
  for (const [name, message] of refusals) {
    const before = page.sent("POST", mergePath).length;
    await control(origin, "reply", { key: "POST " + mergePath, status: name === "unavailable" ? 503 : 409, body: { name, message: "Owner diagnostic for " + name } });
    page.$("#surla-cp5-merge").click();
    await until(() => page.sent("POST", mergePath).length === before + 1 && page.text("#surla-cp5-merge-feedback-error").includes(message), "merge refusal did not explain recovery for " + name);
    const request = page.sent("POST", mergePath).at(-1).body;
    expect(request.request_revision === input.revision && request.candidate_commit === input.candidate && request.report_digest === input.digest && Object.keys(request).length === 3, "merge borrowed the newest run or sent a different approval tuple");
    expect(page.text("#surla-cp5-merge-feedback-error").includes("Owner diagnostic for " + name), "merge refusal discarded owner details");
    const reads = page.sent("GET", "/changes/chg-history/build").length;
    page.$("#surla-cp5-merge-feedback-refresh").click();
    await until(() => page.sent("GET", "/changes/chg-history/build").length > reads && page.$("#surla-cp5-merge") && !page.$("#surla-cp5-merge").disabled, "read-only refresh did not restore merge admission for " + name);
    await sleep(50);
    expect(page.sent("POST", mergePath).length === before + 1, "refresh or recovery retried a merge for " + name);
  }
  await control(origin, "reply", { key: "POST " + mergePath, status: 200, body: { branch: "feature/refunds", head_commit: "9".repeat(40) } });
  await control(origin, "hold", { key: "POST " + mergePath });
  const beforeSuccess = page.sent("POST", mergePath).length;
  page.$("#surla-cp5-merge").click();
  await until(() => page.sent("POST", mergePath).length === beforeSuccess + 1 && page.$("#surla-cp5-merge-feedback-pending"), "merge did not expose its pending state");
  expect(page.$("#surla-cp5-merge").disabled, "pending merge remained available");
  page.$("#surla-cp5-merge").click();
  expect(page.sent("POST", mergePath).length === beforeSuccess + 1, "pending merge submitted twice");
  await control(origin, "release", { key: "POST " + mergePath });
  await until(() => page.$("#surla-cp5-merge-feedback-receipt"), "successful merge omitted its receipt");
  const receipt = page.text("#surla-cp5-merge-feedback-receipt");
  expect(receipt.includes("feature/refunds") && receipt.includes("9".repeat(12)) && receipt.includes("Nothing was deployed"), "receipt invented a branch, lost the resulting commit or claimed deployment");
  expect(page.text("#surla-cp5-merge-feedback-receipt-details").includes("9".repeat(40)), "receipt lost the complete returned commit");
}
page.close();
`)
		})
	}
}

// TestViewsPageApprovalFollowsTheRealMerge observes both Git merges through the
// existing list wait and page consumer. Saved reports avoid candidate execution;
// real Git is needed to prove the owner refreshes without a card write or reload.
func TestViewsPageApprovalFollowsTheRealMerge(t *testing.T) {
	runs := t.TempDir()
	cp := newBuildControlPlane(t, Options{RunsDir: runs, BuildCacheDir: t.TempDir(),
		WrittenTestRuns: &WrittenTestRuns{ReportDir: filepath.Join(runs, "reports"), Timeout: time.Minute},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := buildableChange(t, cp, "chg-merged-review")
	user := asCaller(pageOwner, CallerUser)
	started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	workspace := cp.currentWorkspaceDir()
	base := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-c", "tests-fixture")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "tests.txt"), []byte("tests\n"), 0o600))
	productionBuildGit(t, workspace, "add", "tests.txt")
	productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Tests")
	tests := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	productionBuildGit(t, workspace, "switch", "--quiet", "-")
	acceptTests(t, cp, card.ChangeID, tests)
	candidate := commitBuilderCandidateFixture(t, cp, card)
	builderCopy := filepath.Join(workspace, ".surla-build")
	writeWorkspaceFiles(t, builderCopy, map[string]string{
		"second_candidate.txt": "Untaken second pass.\n",
	})
	otherCandidate := commitAllForLayoutTest(t, builderCopy)
	runID := strings.Repeat("a", 32)
	otherRunID := strings.Repeat("b", 32)
	writeSelectedInputsRun(t, runs, card, otherRunID, tests, otherCandidate)
	writeSelectedInputsRun(t, runs, card, runID, tests, candidate)
	accepted, err := time.Parse(time.RFC3339Nano, started.Build.StartedAt)
	require.NoError(t, err)
	editVerificationRequest(t, runs, otherRunID, func(req *verifyrun.Request) {
		req.AcceptedAt = accepted.Add(time.Second)
	})
	editVerificationRequest(t, runs, runID, func(req *verifyrun.Request) {
		req.AcceptedAt = accepted.Add(2 * time.Second)
	})
	_, state, err := verifyrun.Read(runs, runID)
	require.NoError(t, err)
	_, err = cp.ApproveMerge(user, &gensurla.ApproveMergePayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		CandidateCommit: candidate, ReportDigest: state.Report.Digest,
	})
	require.NoError(t, err)
	ps := startPageServer(t, cp)
	ps.act("merge-builder", func() {
		productionBuildGit(t, workspace, "merge", "--ff-only", candidate)
	})
	ps.act("merge-tests", func() {
		productionBuildGit(t, workspace, "merge", "--no-edit", tests)
	})
	ps.act("squash-merge", func() {
		productionBuildGit(t, workspace, "reset", "--hard", base)
		productionBuildGit(t, workspace, "merge", "--squash", candidate)
		productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Squash builder")
		productionBuildGit(t, workspace, "merge", "--squash", tests)
		productionBuildGit(t, workspace, "commit", "--quiet", "-m", "Squash tests")
	})
	ps.act("resave-merged-card", func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		record, found, err := cp.cards.load(card.ChangeID)
		require.NoError(t, err)
		require.True(t, found)
		require.NoError(t, cp.cards.save(record))
	})
	runPageScript(t, ps, map[string]any{"run": runID, "otherRun": otherRunID, "candidate": candidate, "otherCandidate": otherCandidate, "tests": tests}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-review-approval"), "saved approval did not load");
page.$("#surla-review-approval").click();
await until(() => page.$("#surla-cp5-approval") && page.text("#surla-frame-next").includes("Merge the verified commits shown below"), "approval did not show the open build's merge step");
const record = page.text("#surla-cp5-approval");
const mergedBasis = "This saved result names the builder’s commit " + input.candidate + " and the tests’ commit " + input.tests + ". This build is merged into your branch; this verification covered these commits, not your branch after the merge.";
const otherMergedBasis = "This saved result names the builder’s commit " + input.otherCandidate + " and the tests’ commit " + input.tests + ". This build is merged into your branch; this verification covered these commits, not your branch after the merge.";
const fallbackBasis = "This saved result names the builder’s commit " + input.candidate + " and the tests’ commit " + input.tests + ". Review and approve this result before merging; a saved passing check alone does not grant approval.";
await control(origin, "act", { key: "merge-builder" });
await control(origin, "act", { key: "merge-tests" });
await until(() => page.text("#surla-frame-next").includes("Describe the next change when you are ready"), "approval did not follow both real merges without reloading");
expect(page.$("#surla-approval-stage").dataset.runId === input.run && page.text("#surla-cp5-approval") === record, "merge completion replaced the selected approval or evidence");
expect(!page.text("#surla-frame-next").includes("Merge the verified commits"), "completed build still instructs another merge");
expect(page.text(".surla-approval-basis").includes(mergedBasis) && !page.text(".surla-approval-basis").includes("before merging") && !page.text(".surla-approval-basis").includes("merge commit"), "merged written-tests approval did not explain the merged build state");
const buildPath = "/changes/chg-merged-review/build";
const before = page.sent("GET", buildPath).length;
await control(origin, "reply", { key: "GET " + buildPath, status: 503, body: { name: "internal", message: "Build could not be read." } });
await control(origin, "hold", { key: "GET " + buildPath });
await control(origin, "act", { key: "resave-merged-card" });
await until(() => page.sent("GET", buildPath).length > before, "changed owner facts did not refresh the build");
expect(!page.text("#surla-frame-next").includes("Describe the next change") && page.text(".surla-approval-basis").includes(fallbackBasis), "pending build read retained a completion claim");
await control(origin, "release", { key: "GET " + buildPath });
await until(() => page.sent("GET", buildPath).slice(before).some((r) => r.status === 503) && !page.$("#surla-frame-next button").disabled, "failed build read did not settle");
expect(page.text("#surla-frame-next").includes("Read current build") && !page.text("#surla-frame-next").includes("Describe the next change") && page.text(".surla-approval-basis").includes(fallbackBasis), "failed build read claimed completion or lost its recovery action");
page.$("#surla-frame-next button").click();
await until(() => page.text("#surla-frame-next").includes("Describe the next change when you are ready"), "fresh build read did not recover completion");
expect(page.text(".surla-approval-basis").includes(mergedBasis), "fresh build read did not restore the merged explanation");
expect(page.text("#surla-cp5-approval") === record, "read failure or recovery replaced the saved approval");
const beforeSquash = page.sent("GET", buildPath).length;
await control(origin, "act", { key: "squash-merge" });
await control(origin, "act", { key: "resave-merged-card" });
await until(() => page.sent("GET", buildPath).length > beforeSquash && page.text("#surla-frame-next").includes("Describe the next change when you are ready"), "squash merge did not stay merged");
expect(page.text(".surla-approval-basis").includes(mergedBasis) && !page.text(".surla-approval-basis").includes("merge commit"), "squash merge invented a merge commit");
page.$("#surla-nav-cp4").click();
await until(() => page.$('button[data-run-id="' + input.otherRun + '"]'), "untaken pass B was not listed");
page.$('button[data-run-id="' + input.otherRun + '"]').click();
await until(() => page.$("#surla-result-view").dataset.runId === input.otherRun && page.text("#surla-cp4-inputs").includes("This result matches"), "untaken pass B did not open in Check");
expect(page.text("#surla-cp4-details").includes(otherMergedBasis) && !page.text("#surla-cp4-details").includes("This run verified") && !page.text("#surla-cp4-details").includes("Merged into your branch."), "Check claimed untaken pass B was merged");
page.$("#surla-nav-cp5").click();
await until(() => page.$("#surla-approval-stage").dataset.runId === input.otherRun && !page.$("#surla-cp5-observation-status"), "untaken pass B did not open in Approve");
expect(page.text(".surla-approval-basis").includes(otherMergedBasis) && !page.text(".surla-approval-basis").includes("This run verified") && !page.text(".surla-approval-basis").includes("Merged into your branch."), "Approve claimed untaken pass B was merged");
expect(page.sent("POST", "/changes/approve").length === 0 && page.sent("POST", "/changes/verify").length === 0, "observing the merge approved or started work");
page.close();
`)
}
