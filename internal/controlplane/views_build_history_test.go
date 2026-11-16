// This file binds taking guidance to each saved result's own inputs through
// the served page. Saved runs and taking state are explicit owner fixtures.
package controlplane

import (
	"context"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
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
	for _, mode := range []string{"eligible", "prior_start", "before_start", "at_start", "after_start", "history_failure"} {
		t.Run(mode, func(t *testing.T) {
			reportDir := t.TempDir()
			cp := newBuildControlPlane(t, Options{BuildCacheDir: t.TempDir(),
				RunsDir: t.TempDir(), SurlaVersion: testSurlaVersion,
				WrittenTestRuns: &WrittenTestRuns{
					ReportDir: reportDir, Command: []string{"/bin/sh", "-c", "exit 0", "sh"}, Timeout: time.Second,
				},
			})
			cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
			card := saveBuildStoryCard(t, cp, "chg-history", StatusConfirmed, refundStory("Refund waits"))
			user := asCaller(pageOwner, CallerUser)
			started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
			})
			require.NoError(t, err)
			tests := strings.Repeat("2", 40)
			acceptTests(t, cp, card.ChangeID, tests)
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
			if mode == "prior_start" {
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
			runPageScript(t, ps, map[string]any{"mode": mode, "old": old.RunID, "eligible": eligible, "candidate": candidate}, `
const history = "/changes/chg-history/verifications";
if (input.mode === "history_failure") await control(origin, "reply", { key: "GET " + history, status: 503,
  body: { name: "unavailable", id: "history-read", message: "Saved verification history unavailable.", fault: false, temporary: true, timeout: false } });
else await control(origin, "hold", { key: "GET " + history });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-cp5-approval") && page.sent("GET", history).length === 1, "initial saved approval did not automatically read its verification history");
if (input.mode !== "history_failure") {
  expect(page.text("#surla-cp5-approval").includes("Reading saved verification runs"), "held history was not explained");
  expect(!page.text("#surla-cp5-approval").includes("git merge"), "unread history invented build membership");
  await control(origin, "release", { key: "GET " + history });
}
await until(() => page.sent("GET", history)[0].status !== undefined && !page.text("#surla-cp5-approval").includes("Reading saved verification runs"), "history read did not settle");
const ownCommands = (region) => Array.from(region.querySelectorAll(".surla-build-merge li > pre > code"), (node) => node.textContent);
const expected = ["git merge " + input.candidate, "git merge " + "2".repeat(40)];
if (input.eligible) {
  expect(JSON.stringify(ownCommands(page.$("#surla-cp5-approval"))) === JSON.stringify(expected), "recorded approval used the newest pass instead of its own C then T");
  expect(page.text("#surla-cp5-approval").includes("commit where this build started"), "eligible approval lost the rewrite warning");
} else {
  expect(ownCommands(page.$("#surla-cp5-approval")).length === 0, "unknown or prior-start approval claimed current-build membership");
  if (input.mode === "history_failure") {
    expect(page.text("#surla-cp5-approval").includes("Saved verification history unavailable") && page.text("#surla-cp5-approval").includes("membership is unknown"), "failed automatic history read lost its uncertainty");
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
  expect(page.doc.activeElement === (external ?? fallback), "removed result commands lost local fallback or stole external focus");
  await control(origin, "release", { key: "GET " + path });
  await until(() => ownCommands(region).length === 2, "fresh build read did not restore its own result commands");
};
if (input.mode === "eligible") {
  const region = page.$("#surla-cp5-approval");
  await checkCommandRemoval(region, page.$("#surla-cp1-editor"), null);
  const field = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
  page.type(field, "keep my editor focus through approval refresh");
  await checkCommandRemoval(region, page.$("#surla-cp1-editor"), field);
  expect(field.value === "keep my editor focus through approval refresh", "approval refresh lost unsaved editor text");
}
if (input.mode !== "history_failure") {
  page.$("#surla-nav-cp4").click();
  await until(() => page.$('button[data-run-id="' + input.old + '"]'), "saved old run was not listed");
  page.$('button[data-run-id="' + input.old + '"]').click();
  await until(() => page.$("#surla-result-view").dataset.runId === input.old && page.text("#surla-cp4-inputs").includes("This result matches"), "old pass did not open");
  const commands = ownCommands(page.$("#surla-cp4-details"));
  expect((commands.length > 0) === input.eligible, "old pass membership did not follow its saved start");
  if (input.eligible) expect(JSON.stringify(commands) === JSON.stringify(expected), "older pass did not keep its own C then T");
  if (input.mode === "eligible") {
    const region = page.$("#surla-cp4-details"); region.open = true;
    await checkCommandRemoval(region, page.$("#surla-cp4-title"), null);
    await checkCommandRemoval(region, page.$("#surla-cp4-title"), page.$("#surla-load-verifications"));
  }
}
page.close();
`)
		})
	}
}
