//go:build e2e

// This file holds the whole-journey test's changes and the legs that follow
// the first approval: a second verification after later tests, a second change
// refused its build while build 1 is open, the user's merge, the second
// change's build from the merged branch, its drop, and a third change's build
// after the drop.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/testwriter"
)

type (
	// journeyProposal is one change as the guide drafts and proposes it: the
	// finished flow it adds to its draft's Story and the analysis it sends.
	journeyProposal struct {
		// change is the Change Card's identifier.
		change string
		// after is the Story text the guide adds flow right after; flow is the
		// finished flow's declaration.
		after, flow string
		// flows are the keys of the Story's finished flows once flow is added,
		// in declaration order.
		flows []string
		// prompt is the user's request. summary, scenario, input and outcome
		// are the guide's analysis of it with its one confirmation example.
		prompt, summary, scenario, input, outcome string
		// preserved is the existing behavior the change keeps.
		preserved string
	}

	// journeyCopy is where a worker's working copy stands, as git reports it
	// there.
	journeyCopy struct {
		// branch is the branch checked out in the copy.
		branch string
		// commit is the commit that branch points to.
		commit string
		// status is git status --porcelain --untracked-files=all: one line per
		// uncommitted or untracked path, empty when the copy is clean.
		status string
	}
)

const (
	// journeyMuffinChangeID is the second change, agreed while build 1 is open
	// and built as build 2, which the user drops.
	journeyMuffinChangeID = "add-muffin-price"
	// journeyBagelChangeID is the third change, drafted after the drop and
	// built as build 3.
	journeyBagelChangeID = "add-bagel-price"
	// journeyMuffinFlow is the finished flow the guide adds for the second
	// change, after the scone's.
	journeyMuffinFlow = `	Flow("prices-muffin", "Price a muffin", func() {
		StartsWith("ask-muffin-price", "customer", "The customer asks how much a muffin is")
		Outcome("muffin-priced", "customer", "The helper answers that a muffin costs $3.00")
	})
`
	// journeyBagelFlow is the finished flow the guide adds for the third
	// change, after the scone's in the draft Highbay made again.
	journeyBagelFlow = `	Flow("prices-bagel", "Price a bagel", func() {
		StartsWith("ask-bagel-price", "customer", "The customer asks how much a bagel is")
		Outcome("bagel-priced", "customer", "The helper answers that a bagel costs $2.25")
	})
`
	// journeySconePrice is the catalog line the builder adds for the first
	// change, which the merge brings into the project's branch.
	journeySconePrice = "\t\"scone\":  275,\n"
	// journeyMuffinPrice is the catalog line the builder leaves uncommitted in
	// its copy when the user drops build 2.
	journeyMuffinPrice = "\t\"muffin\": 300,\n"
	// journeyLeftoverFile is the file the builder leaves untracked at the top
	// of its copy when the user drops build 2, and journeyLeftoverNotes its
	// text.
	journeyLeftoverFile  = "MUFFIN_NOTES.md"
	journeyLeftoverNotes = "Muffins: the price is in the catalog; the helper's answer is not written yet.\n"
	// journeyDroppedText is what every builder and test-writer tool answers
	// for a change whose build the user dropped (workerOwnedBuild and
	// builderCopyReady in internal/controlplane), with the change's id.
	journeyDroppedText = "The user dropped change %s: stop work on it; make no more edits or commits for it. " +
		"The user starts builds on the views page."
)

// journeySconeProposal, journeyMuffinProposal and journeyBagelProposal are the
// journey's three changes, each one more priced item with its finished flow.
var (
	journeySconeProposal = journeyProposal{
		change: journeyChangeID, after: journeyCookieFlow, flow: journeySconeFlow,
		flows:  []string{"prices-cookie", "prices-scone"},
		prompt: "Sell scones for $2.75 and let the helper answer their price.",
		summary: "The shop sells scones at $2.75, and the helper answers a scone's price " +
			"as it answers a cookie's.",
		scenario: "Scone price", input: "Ask how much a scone is.",
		outcome:   "The helper answers that a scone costs $2.75.",
		preserved: "Tea costs $3.50 and a cookie $1.25.",
	}
	journeyMuffinProposal = journeyProposal{
		change: journeyMuffinChangeID, after: journeySconeFlow, flow: journeyMuffinFlow,
		flows:  []string{"prices-cookie", "prices-scone", "prices-muffin"},
		prompt: "Sell muffins for $3.00 and let the helper answer their price.",
		summary: "The shop sells muffins at $3.00, and the helper answers a muffin's price " +
			"as it answers a scone's.",
		scenario: "Muffin price", input: "Ask how much a muffin is.",
		outcome:   "The helper answers that a muffin costs $3.00.",
		preserved: "A cookie costs $1.25 and a scone $2.75.",
	}
	journeyBagelProposal = journeyProposal{
		change: journeyBagelChangeID, after: journeySconeFlow, flow: journeyBagelFlow,
		flows:  []string{"prices-cookie", "prices-scone", "prices-bagel"},
		prompt: "Sell bagels for $2.25 and let the helper answer their price.",
		summary: "The shop sells bagels at $2.25, and the helper answers a bagel's price " +
			"as it answers a scone's.",
		scenario: "Bagel price", input: "Ask how much a bagel is.",
		outcome:   "The helper answers that a bagel costs $2.25.",
		preserved: "A cookie costs $1.25 and a scone $2.75.",
	}
)

// verifyLaterTests follows what a builder does once later tests are accepted
// after a pass: its assignment is listed again and names the later tests'
// commit, it verifies the same commit again, and the run passes with those
// tests. The change list shows the new run as current, the build reads passed
// with the later tests' commit to merge, the builder has no work left, and
// the user approves the new run. From here the journey's passed run and tests
// commit are the ones this approval names.
func (j *wholeJourney) verifyLaterTests(t *testing.T) {
	ctx := stepContext(t, journeyQuickWait)
	work := j.builderWork(t, ctx)
	require.Len(t, work, 1, "a pass with tests no longer selected leaves the builder its assignment")
	assert.Equal(t, journeyChangeID, work[0].ChangeID)
	later, ok := j.readBuilderAssignment(t, ctx, journeyChangeID).WrittenTests.AsAccepted()
	require.True(t, ok, "the builder's assignment names accepted tests")
	require.NotEqual(t, j.testsCommit, later.Commit, "the assignment names the later tests")
	assert.Equal(t, later.Commit, j.testsBranch(t, 1))

	earlier := j.passed
	run := j.passRun(t, stepContext(t, journeyOwnerWait), earlier.CandidateCommit, later.Commit)
	assert.NotEqual(t, earlier.RunID, run.RunID, "verifying again starts a new run")
	ctx = stepContext(t, journeyQuickWait)
	for who, latest := range j.latestVerifications(t, ctx, journeyChangeID) {
		assert.Equal(t, &genhighbay.LatestVerificationItem{RunID: run.RunID, State: "passed", InputsCurrent: true},
			latest, "the %s sees the new run passed, for the tests selected now", who)
	}
	j.passed, j.testsCommit = run, later.Commit
	j.assertPassedTaking(t, ctx)
	assert.Empty(t, j.builderWork(t, ctx), "the new pass is current, so the builder has no work left")
	j.approvePassedRun(t)
}

// refuseSecondBuild has the guide draft, evaluate and propose a second change
// and the user confirm it while build 1 is open and unmerged, then the user
// build it. The start is refused with previous_change_open, whose body names
// build 1's change and gives the page what it shows to merge: the passed
// run's candidate commit and tests commit, each with the branch it was
// verified on. The second change has no build, and build 1 is as it was.
func (j *wholeJourney) refuseSecondBuild(t *testing.T) {
	_, j.muffin = j.agreeChange(t, journeyMuffinProposal)
	// The call is a build start whatever it answers, so it waits as one does.
	_, err := j.user.StartBuild(stepContext(t, journeyStepWait), &genhighbay.StartBuildPayload{
		ChangeID: journeyMuffinChangeID, RequestRevision: j.muffin,
	})
	var refused *genhighbay.OpenBuildRefusal
	require.ErrorAs(t, err, &refused, "another change's build is refused while build 1 is open")
	assert.Equal(t, "previous_change_open", refused.Name)
	assert.Equal(t, fmt.Sprintf(
		"Change %s still has an open build. Merge a passed run or drop that build before building %s.",
		journeyChangeID, journeyMuffinChangeID), refused.Message)
	assert.Equal(t, journeyChangeID, refused.ChangeID, "the refusal names the open build's change")
	require.NotNil(t, refused.Build)
	assert.Equal(t, j.startedAt, refused.Build.StartedAt, "the refusal names build 1, which Drop would take")
	assert.Equal(t, j.confirmed, refused.Build.RequestRevision)
	assert.Equal(t, j.passedTaking(), refused.Build.Taking,
		"the refusal gives the passed run's commits to merge and their branches")

	ctx := stepContext(t, journeyQuickWait)
	second, err := j.user.GetBuild(ctx, &genhighbay.GetBuildPayload{ChangeID: journeyMuffinChangeID})
	require.NoError(t, err)
	assert.Equal(t, genhighbay.BuildKindNotStarted, second.Build.Kind(), "a refused start saves no build")
	for who, build := range j.buildReads(t, ctx, journeyChangeID) {
		assert.Equal(t, j.startedAt, build.StartedAt, "the %s still reads build 1 as the user started it", who)
		assert.Equal(t, j.passedTaking(), build.Taking, "the %s", who)
	}
}

// mergePassedRun has the user take build 1 with git alone: the two git merge
// commands the page forms from the build's taking, the builder's commit first
// and then the tests', run in the project folder. Highbay then reads the build
// as merged, for the user and for an agent, from the project branch's own
// history; neither worker has work left, and the project's git status is
// empty.
func (j *wholeJourney) mergePassedRun(t *testing.T) {
	page, err := j.user.GetBuild(stepContext(t, journeyQuickWait),
		&genhighbay.GetBuildPayload{ChangeID: journeyChangeID})
	require.NoError(t, err)
	started, ok := page.Build.AsStarted()
	require.True(t, ok)
	passed, ok := started.Taking.AsPassed()
	require.True(t, ok, "the page shows the commits to merge: %s", started.Taking.Kind())
	tests, ok := passed.Tests.AsWrittenTests()
	require.True(t, ok, "the passed run used written tests: %s", passed.Tests.Kind())
	for _, commit := range []string{passed.CandidateCommit, tests.Commit} {
		runTestGit(t, j.dir, "merge", commit)
	}
	j.merged = strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", "HEAD"))
	require.NotEqual(t, j.projectCommit, j.merged)
	history := runTestGit(t, j.dir, "rev-list", j.merged)
	assert.Contains(t, history, passed.CandidateCommit, "the project's branch holds the builder's commit")
	assert.Contains(t, history, tests.Commit, "the project's branch holds the tests' commit")
	assert.Empty(t, runTestGit(t, j.dir, "status", "--porcelain", "--untracked-files=all"),
		"the two merges leave the project's git status empty")
	assert.Contains(t, journeyFile(t, filepath.Join(j.dir, "design", "story.go")), journeySconeFlow,
		"the merge brings the agreed design into the project folder")

	ctx := stepContext(t, journeyQuickWait)
	for who, build := range j.buildReads(t, ctx, journeyChangeID) {
		assert.Equal(t, genhighbay.NewBuildTakingMerged(&genhighbay.BuildMerged{}), build.Taking,
			"the %s reads the build merged from the user's own git", who)
		assert.Equal(t, j.startedAt, build.StartedAt)
	}
	assert.Empty(t, j.builderWork(t, ctx), "a merged build leaves the builder no work")
	assert.Empty(t, j.testWriterWork(t, ctx), "a merged build leaves the test writer no work")
}

// buildSecondChange has the guide bring the merged project folder into its
// draft with update_draft, which finds nothing to bring because the merged
// design came from that draft, and the user build the second change at the
// revision agreed before the merge. It is build 2, based on the project's
// branch as the merge left it. The start moves neither working copy; each
// joins build 2's branch at that base when its worker reads the assignment,
// with nothing committed on build 1's branches; and the builder's next commit
// has the new base as its parent.
func (j *wholeJourney) buildSecondChange(t *testing.T) {
	updated, err := genhighbay.DecodeDraftUpdateResult(
		trustedSuiteCall(t, stepContext(t, journeyQuickWait), j.guide, "update_draft", []byte(`{}`)))
	require.NoError(t, err)
	assert.Equal(t, &genhighbay.DraftUpdateResult{
		DraftFolder: j.draftFolder, Taken: []string{}, Merged: []string{}, ToCorrect: []*genhighbay.DraftCorrection{},
	}, updated, "the merged design came from the draft, so the project folder brings nothing")
	assert.Contains(t, journeyFile(t, filepath.Join(j.draftFolder, "design", "story.go")), journeyMuffinFlow,
		"the draft keeps the second change's flow")

	builderTip := journeyBranchTip(t, j.dir, testwriter.BuilderRole.Branch(1))
	testsTip := j.testsBranch(t, 1)
	j.startNextBuild(t, journeyMuffinChangeID, j.muffin, 2, len(journeyMuffinProposal.flows))
	j.joinBuild(t, journeyMuffinChangeID, j.muffin, 2, []*genhighbay.RequiredTest{
		{Story: "Prices", FlowKey: "prices-cookie", Scenario: "prices_cookie", Hook: "PricesCookie"},
		{Story: "Prices", FlowKey: "prices-scone", Scenario: "prices_scone", Hook: "PricesScone"},
		{Story: "Prices", FlowKey: "prices-muffin", Scenario: "prices_muffin", Hook: "PricesMuffin"},
	})
	assert.Equal(t, builderTip, journeyBranchTip(t, j.dir, testwriter.BuilderRole.Branch(1)),
		"joining build 2 commits nothing on build 1's builder branch")
	assert.Equal(t, testsTip, j.testsBranch(t, 1), "joining build 2 commits nothing on build 1's tests branch")

	commit := j.generateAndCommit(t, "Generate the agreed muffin design")
	assert.Equal(t, j.merged, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", commit+"^")),
		"the builder's commit for build 2 follows the merged project commit")
	assert.Contains(t, runTestGit(t, j.dir, "show", commit+":design/story.go"), journeyMuffinFlow)
	assert.Equal(t, commit, journeyBranchTip(t, j.dir, testwriter.BuilderRole.Branch(2)))
	j.leftBehind = commit
}

// dropSecondBuild has the builder leave an uncommitted edit and an untracked
// file in its copy, and the user drop build 2 on the page, naming the build
// the page showed. The result reads dropped and names the builder's branch as
// kept, because it holds a commit; Drop touches neither the copy nor the
// branch. The builder's next generate_code is refused with the dropped text
// and writes nothing, neither worker has work, and the guide's next refresh
// says Highbay made the draft again from the project folder, which no longer
// holds the dropped change's flow.
func (j *wholeJourney) dropSecondBuild(t *testing.T) {
	service := filepath.Join(j.builderFolder, "internal", "service", "service.go")
	notes := filepath.Join(j.builderFolder, journeyLeftoverFile)
	replaceOnce(t, service, journeySconePrice, journeySconePrice+journeyMuffinPrice)
	require.NoError(t, os.WriteFile(notes, []byte(journeyLeftoverNotes), 0o644))
	left := journeyCopy{
		branch: testwriter.BuilderRole.Branch(2), commit: j.leftBehind,
		status: " M internal/service/service.go\n?? " + journeyLeftoverFile + "\n",
	}
	require.Equal(t, left, journeyCopyState(t, j.builderFolder))

	page, err := j.user.GetBuild(stepContext(t, journeyQuickWait),
		&genhighbay.GetBuildPayload{ChangeID: journeyMuffinChangeID})
	require.NoError(t, err)
	shown, ok := page.Build.AsStarted()
	require.True(t, ok)
	dropped, err := j.user.DropBuild(stepContext(t, journeyQuickWait), &genhighbay.DropBuildPayload{
		ChangeID: journeyMuffinChangeID, StartedAt: shown.StartedAt,
	})
	require.NoError(t, err)
	ctx := stepContext(t, journeyQuickWait)
	assert.Equal(t, journeyMuffinChangeID, dropped.ChangeID)
	assert.True(t, dropped.DraftedBeforeDrop, "the dropped change's revision was drafted before the drop")
	build, ok := dropped.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, shown.StartedAt, build.StartedAt, "the result is the build the page showed")
	kept := genhighbay.NewBuildTakingDropped(&genhighbay.BuildDropped{
		KeptBranches: []string{testwriter.BuilderRole.Branch(2)},
	})
	assert.Equal(t, kept, build.Taking, "the builder's branch holds a commit, the tests branch none")
	for who, read := range j.buildReads(t, ctx, journeyMuffinChangeID) {
		assert.Equal(t, kept, read.Taking, "the %s reads the build dropped", who)
	}
	assert.Equal(t, left, journeyCopyState(t, j.builderFolder),
		"Drop leaves the builder's copy and branch alone")
	assert.Equal(t, journeyLeftoverNotes, journeyFile(t, notes))
	assert.Contains(t, journeyFile(t, service), journeyMuffinPrice)

	// The call is a generation whatever it answers, so it waits as one does.
	refused := journeyRefusal(t, stepContext(t, journeyGenerateWait), j.builder, "generate_code", []byte(`{}`))
	assert.Contains(t, refused, fmt.Sprintf(journeyDroppedText, journeyMuffinChangeID))
	assert.Equal(t, left, journeyCopyState(t, j.builderFolder), "a refused generation writes nothing")
	ctx = stepContext(t, journeyQuickWait)
	assert.Empty(t, j.builderWork(t, ctx), "a dropped build leaves the builder no work")
	assert.Empty(t, j.testWriterWork(t, ctx), "a dropped build leaves the test writer no work")

	refresh := trustedSuiteRefresh(t, stepContext(t, journeyStepWait), j.guide)
	assert.Equal(t, j.draftFolder, refresh.DraftFolder)
	assert.Contains(t, refresh.Message, fmt.Sprintf(
		" Highbay made this draft again from the project folder when the user dropped change %s; "+
			"edits made before then are gone.", journeyMuffinChangeID))
	require.Len(t, refresh.Stories, 1)
	keys := make([]string, 0, len(refresh.Stories[0].Flows))
	for _, flow := range refresh.Stories[0].Flows {
		keys = append(keys, flow.Key)
	}
	assert.Equal(t, journeySconeProposal.flows, keys, "the draft made again holds the merged Story only")
	draft := journeyFile(t, filepath.Join(j.draftFolder, "design", "story.go"))
	assert.Contains(t, draft, journeySconeFlow)
	assert.NotContains(t, draft, "muffin", "the dropped change's flow is gone from the draft")
	assert.Equal(t, j.merged, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", "HEAD")),
		"Drop does not move the project's branch")
	assert.Empty(t, runTestGit(t, j.dir, "status", "--porcelain", "--untracked-files=all"))
}

// buildThirdChange has the guide draft a third change in the draft Highbay made
// again, and the user confirm and build it: build 3, based on the same merged
// project commit. The builder's read commits what its copy left uncommitted
// on build 2's branch, the edit and the untracked file, with Highbay's message
// naming the new change, and puts the copy on build 3's branch at the base,
// clean. The candidate the builder then generates and commits there holds
// neither leftover file nor the dropped change's flow, and the leftover
// commit is not in its history.
func (j *wholeJourney) buildThirdChange(t *testing.T) {
	_, j.bagel = j.agreeChange(t, journeyBagelProposal)
	j.startNextBuild(t, journeyBagelChangeID, j.bagel, 3, len(journeyBagelProposal.flows))
	j.joinBuild(t, journeyBagelChangeID, j.bagel, 3, []*genhighbay.RequiredTest{
		{Story: "Prices", FlowKey: "prices-cookie", Scenario: "prices_cookie", Hook: "PricesCookie"},
		{Story: "Prices", FlowKey: "prices-scone", Scenario: "prices_scone", Hook: "PricesScone"},
		{Story: "Prices", FlowKey: "prices-bagel", Scenario: "prices_bagel", Hook: "PricesBagel"},
	})

	leftover := journeyBranchTip(t, j.dir, testwriter.BuilderRole.Branch(2))
	require.NotEqual(t, j.leftBehind, leftover, "the builder's read commits the leftovers on build 2's branch")
	assert.Equal(t, "Left uncommitted when change "+journeyBagelChangeID+" started\n",
		runTestGit(t, j.dir, "log", "-1", "--format=%s", leftover))
	assert.Equal(t, j.leftBehind, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", leftover+"^")),
		"the leftover commit follows the builder's own commit for the dropped change")
	assert.Equal(t, journeyLeftoverFile+"\ninternal/service/service.go\n",
		runTestGit(t, j.dir, "diff-tree", "--no-commit-id", "--name-only", "-r", leftover),
		"the leftover commit holds the untracked file and the uncommitted edit")
	assert.Equal(t, journeyLeftoverNotes, runTestGit(t, j.dir, "show", leftover+":"+journeyLeftoverFile))
	assert.Contains(t, runTestGit(t, j.dir, "show", leftover+":internal/service/service.go"), journeyMuffinPrice)
	_, err := os.Stat(filepath.Join(j.builderFolder, journeyLeftoverFile))
	assert.ErrorIs(t, err, fs.ErrNotExist, "the untracked file stays on build 2's branch, not in the copy")
	assert.NotContains(t, journeyFile(t, filepath.Join(j.builderFolder, "internal", "service", "service.go")),
		"muffin", "the uncommitted edit stays on build 2's branch, not in the copy")
	assert.Equal(t, j.merged, j.testsBranch(t, 2), "the test writer left nothing of its own for build 2")

	commit := j.generateAndCommit(t, "Generate the agreed bagel design")
	assert.Equal(t, j.merged, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", commit+"^")),
		"the third candidate follows the merged project commit")
	assert.Equal(t, commit, journeyBranchTip(t, j.dir, testwriter.BuilderRole.Branch(3)))
	history := runTestGit(t, j.dir, "rev-list", commit)
	assert.NotContains(t, history, leftover, "the leftover commit is not in the third candidate's history")
	assert.NotContains(t, history, j.leftBehind, "nor is the dropped change's commit")
	assert.NotContains(t, runTestGit(t, j.dir, "ls-tree", "-r", "--name-only", commit), journeyLeftoverFile)
	story := runTestGit(t, j.dir, "show", commit+":design/story.go")
	assert.Contains(t, story, journeyBagelFlow)
	assert.NotContains(t, story, "muffin", "the third candidate's design holds no flow of the dropped change")
	assert.NotContains(t, runTestGit(t, j.dir, "show", commit+":internal/service/service.go"), "muffin")
	assert.Equal(t, j.merged, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", "HEAD")),
		"builds 2 and 3 never move the project's branch")
	assert.Empty(t, runTestGit(t, j.dir, "status", "--porcelain", "--untracked-files=all"))
}

// agreeChange has the guide add p's finished flow to the Story in its draft,
// evaluate the draft and propose the change with that evaluation, then the
// user confirm the proposal on the page. It returns the change's revisions
// before and after the confirmation; the confirmed one is its agreed
// revision, the one a build is for.
func (j *wholeJourney) agreeChange(t *testing.T, p journeyProposal) (proposedRevision, agreed string) {
	t.Helper()
	replaceOnce(t, filepath.Join(j.draftFolder, "design", "story.go"), p.after, p.after+p.flow)
	refresh := trustedSuiteRefresh(t, stepContext(t, journeyStepWait), j.guide)
	require.Len(t, refresh.Stories, 1)
	assert.Equal(t, "Prices", refresh.Stories[0].Name, "the guide extends the Story the starter ships")
	require.Len(t, refresh.Stories[0].Flows, len(p.flows))
	for i, key := range p.flows {
		assert.Equal(t, key, refresh.Stories[0].Flows[i].Key)
		assert.True(t, refresh.Stories[0].Flows[i].Complete, "a flow with a start and an outcome is finished")
	}

	args, err := genhighbay.EncodeProposeChangePayload(&genhighbay.ProposeChangePayload{
		ChangeID:         p.change,
		Prompt:           p.prompt,
		StoryRefreshOpID: refresh.OpID,
		Analysis: &genhighbay.CP1Analysis{
			Summary:     p.summary,
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{},
			Examples: []*genhighbay.CP1AnalysisExample{{
				Scenario: p.scenario, InputSummary: p.input,
				ExpectedOutcome:  p.outcome,
				VerificationKind: "deterministic",
			}},
			PreservedBehaviors: []string{p.preserved},
		},
	})
	require.NoError(t, err)
	// The evaluation has ended: the proposal and the confirmation save and
	// read stored state, with their own wait.
	ctx := stepContext(t, journeyQuickWait)
	proposed, err := genhighbay.DecodeChangeCardResult(
		trustedSuiteCall(t, ctx, j.guide, "propose_change", args))
	require.NoError(t, err)
	assert.Equal(t, controlplane.StatusNeedsConfirmation, proposed.Status)
	assert.Equal(t, refresh.Stories, proposed.Stories, "the card adopts the evaluated Story")

	confirmed, err := j.user.ConfirmIntent(ctx, &genhighbay.ConfirmIntentPayload{
		ChangeID: p.change, RequestRevision: proposed.RequestRevision,
		Answers: map[string]string{}, Examples: proposed.Examples,
	})
	require.NoError(t, err)
	assert.Equal(t, controlplane.StatusConfirmed, confirmed.Status)
	assert.NotEqual(t, proposed.RequestRevision, confirmed.RequestRevision,
		"confirming makes a new revision")
	assert.Equal(t, proposed.Stories, confirmed.Stories)
	if assert.NotNil(t, confirmed.ConfirmedBy) {
		assert.Equal(t, j.state.Record.Owner, *confirmed.ConfirmedBy,
			"the page session acts for the project's owner")
	}
	return proposed.RequestRevision, confirmed.RequestRevision
}

// startNextBuild has the user build change at revision once the project's
// open build is merged or dropped: the build numbered number, whose Story has
// flows finished flows. Both working copies exist, so the start saves the
// build, based on the merged project commit, and moves neither copy: each
// stays on the branch of the build before it until its worker reads the new
// assignment, which both workers' lists now hold, unread. The project's
// branch and git status are untouched.
func (j *wholeJourney) startNextBuild(t *testing.T, change, revision string, number, flows int) {
	t.Helper()
	builder, tests := journeyCopyState(t, j.builderFolder), journeyCopyState(t, j.testsFolder)
	started, err := j.user.StartBuild(stepContext(t, journeyStepWait), &genhighbay.StartBuildPayload{
		ChangeID: change, RequestRevision: revision,
	})
	require.NoError(t, err)
	assert.Equal(t, revision, started.Build.RequestRevision)
	assert.True(t, started.Build.InForce)
	assert.Equal(t, flows, started.Build.RequiredTestCount, "one test per finished flow")
	assert.Equal(t, genhighbay.TestsKindAssigned, started.Build.TestWriter.Tests.Kind())
	assert.Equal(t, genhighbay.BuildTakingKindBuilding, started.Build.Taking.Kind())
	_, ready := started.Team.AsReady()
	assert.True(t, ready, "the team is ready: %s", started.Team.Kind())

	assert.Equal(t, testwriter.BuilderRole.Branch(number-1), builder.branch)
	assert.Equal(t, testwriter.TestsRole.Branch(number-1), tests.branch)
	assert.Equal(t, builder, journeyCopyState(t, j.builderFolder), "a start moves no existing builder copy")
	assert.Equal(t, tests, journeyCopyState(t, j.testsFolder), "a start moves no existing test-writer copy")
	assert.Equal(t, j.merged, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", "HEAD")))
	assert.Empty(t, runTestGit(t, j.dir, "status", "--porcelain", "--untracked-files=all"))

	ctx := stepContext(t, journeyQuickWait)
	building := j.builderWork(t, ctx)
	require.Len(t, building, 1, "the new build needs the builder")
	assert.Equal(t, change, building[0].ChangeID)
	assert.Equal(t, revision, building[0].RequestRevision)
	assert.False(t, building[0].Read, "a new build's assignment is unread")
	writing := j.testWriterWork(t, ctx)
	require.Len(t, writing, 1, "the new build needs the test writer")
	assert.Equal(t, change, writing[0].ChangeID)
	assert.Equal(t, revision, writing[0].RequestRevision)
	assert.False(t, writing[0].Progress.Read, "a new build's assignment is unread")
}

// joinBuild has the builder and then the test writer read their assignments
// for change at revision, each the first read of the build numbered number.
// The builder's read puts its copy on that build's builder branch at the
// merged project commit with nothing uncommitted; the test writer's read puts
// its copy on the build's tests branch at the same commit and names required,
// the Story's required tests.
func (j *wholeJourney) joinBuild(
	t *testing.T, change, revision string, number int, required []*genhighbay.RequiredTest,
) {
	t.Helper()
	built := j.readBuilderAssignment(t, stepContext(t, journeyQuickWait), change)
	assert.Equal(t, revision, built.RequestRevision)
	assert.True(t, built.InForce)
	assert.True(t, built.Read)
	assert.Equal(t, j.builderFolder, built.WorkingFolder, "the builder keeps its one working copy")
	assert.Equal(t, genhighbay.WrittenTestsKindNotYetAccepted, built.WrittenTests.Kind())
	assert.Equal(t, journeyCopy{branch: testwriter.BuilderRole.Branch(number), commit: j.merged},
		journeyCopyState(t, j.builderFolder),
		"the builder's read joins the build's branch at its base with nothing uncommitted")

	assignment := j.readTestWriterAssignment(t, stepContext(t, journeyStepWait), change)
	assert.Equal(t, revision, assignment.RequestRevision)
	assert.Equal(t, j.testsFolder, assignment.WorkingFolder, "the test writer keeps its one working copy")
	assert.Equal(t, required, assignment.RequiredTests,
		"each finished flow requires one test, in declaration order")
	assert.Equal(t, genhighbay.TestsKindAssigned, assignment.Progress.Tests.Kind())
	assert.True(t, assignment.Progress.Read)
	tests := journeyCopyState(t, j.testsFolder)
	assert.Equal(t, testwriter.TestsRole.Branch(number), tests.branch,
		"the test writer's read joins the build's tests branch")
	assert.Equal(t, j.merged, tests.commit, "the tests branch starts at the build's base")
}

// generateAndCommit has the builder generate the open build's agreed design
// in its copy and commit everything there with message, and returns the
// commit.
func (j *wholeJourney) generateAndCommit(t *testing.T, message string) string {
	t.Helper()
	generated, err := genhighbay.DecodeGenerateCodeResult(
		trustedSuiteCall(t, stepContext(t, journeyGenerateWait), j.builder, "generate_code", []byte(`{}`)))
	require.NoError(t, err)
	assert.NotEmpty(t, generated.FilesWritten, "generation writes the agreed design's code")
	return journeyCommitAll(t, j.builderFolder, message)
}

// journeyCopyState reads where the working copy at dir stands.
func journeyCopyState(t *testing.T, dir string) journeyCopy {
	t.Helper()
	return journeyCopy{
		branch: strings.TrimSpace(runTestGit(t, dir, "symbolic-ref", "--short", "HEAD")),
		commit: strings.TrimSpace(runTestGit(t, dir, "rev-parse", "HEAD")),
		status: runTestGit(t, dir, "status", "--porcelain", "--untracked-files=all"),
	}
}
