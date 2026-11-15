// This file decides which tests judge a change at a revision and what a
// verification run builds and runs: testsChoice chooses the tests' commit and
// source, verificationPlan builds the run's configuration from that choice,
// and starterPlanAt turns a commit's test layout into a plan that live copies
// use too.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"go/scanner"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	goa "goa.design/goa/v3/pkg"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/scaffold"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/goadesign/surla/internal/verify"
)

type (
	// WrittenTestRuns says where and how the project server runs verification
	// of the tests accepted for each change, in a project without a stored
	// evaluator. Runs are kept in Options.RunsDir. The composition root binds
	// it; the plan of each run comes from the change's build and the
	// repository, never from this value.
	WrittenTestRuns struct {
		// ReportDir receives each run's full report, named by its digest. It
		// must lie outside the workspace.
		ReportDir string
		// Command is the argument list that runs this surla executable;
		// runs start `<Command> verify-run supervise <dir>`.
		Command []string
		// Timeout is how long a run may take before it is stopped and
		// recorded as interrupted.
		Timeout time.Duration
	}

	// verifier is how a control plane verifies changes, fixed by New:
	// noVerification, storedVerification or writtenTestVerification.
	verifier interface {
		// isVerifier marks the three cases.
		isVerifier()
	}

	// noVerification is a control plane that runs no verification.
	noVerification struct{}

	// storedVerification verifies every change with the operator's stored
	// evaluator: cfg is complete, its Plan naming the operator's suite.
	storedVerification struct {
		// cfg is Options.Verification with its folders resolved.
		cfg *VerificationConfig
	}

	// writtenTestVerification verifies each change with the tests accepted
	// for its build. runs holds the run settings: RunsDir, Command, Timeout
	// and Plan.ReportDir; the rest of each plan comes from verificationPlan.
	writtenTestVerification struct {
		// runs is Options.WrittenTestRuns and Options.RunsDir with their
		// folders resolved.
		runs *VerificationConfig
	}

	// layoutActor names who changes a commit that lacks the test layout:
	// the builder for the candidate, the test writer for accepted tests.
	layoutActor string

	// refusalReason is why starterPlanAt refuses: refusedReplacedFolder,
	// refusedNoRepository, refusedUnknownCommit, refusedMissingTests,
	// refusedNoLayout, refusedTestModuleChanged or refusedNoCommonBase.
	refusalReason int

	// layoutRefusal is starterPlanAt's refusal: the project folder was
	// replaced or is not in a git repository, the repository does not hold
	// the candidate commit or the accepted tests' commit, a commit lacks
	// the test layout, the candidate commit has no common history with the
	// accepted tests' commit, or the candidate commit changed hand-written
	// files in the test module. Each caller maps it to its own declared error.
	layoutRefusal struct {
		// reason is why starterPlanAt refused.
		reason refusalReason
		// folder is the project folder, for refusedReplacedFolder and
		// refusedNoRepository.
		folder string
		// commit is the commit refused, for refusedUnknownCommit,
		// refusedMissingTests, refusedNoLayout, refusedTestModuleChanged and
		// refusedNoCommonBase.
		commit string
		// testsCommit is the accepted tests' commit, for refusedNoCommonBase.
		testsCommit string
		// baseCommit is the common ancestor of the accepted tests' commit and
		// the candidate commit, for refusedTestModuleChanged.
		baseCommit string
		// testModule is the repository-relative test module folder, for
		// refusedTestModuleChanged.
		testModule string
		// changed lists the hand-written files under testModule that differ
		// between baseCommit and commit, for refusedTestModuleChanged.
		changed []string
		// actor is who changes that commit, for refusedMissingTests and
		// refusedNoLayout.
		actor layoutActor
		// missing says what the commit lacks, for refusedNoLayout.
		missing string
	}

	// testsNotAccepted is testsChoice's refusal of a build whose tests are not
	// accepted. It carries the test writer's saved assignment, not text: the
	// builder refused by verify_change and the user refused by approve_merge
	// each read the refusal worded for them, so each caller that returns it
	// turns it into refuseTestsNotAccepted or refuseApprovalTestsNotAccepted.
	testsNotAccepted struct {
		// saved is the test writer's assignment whose state is not accepted.
		saved testWriterAssignment
	}
)

const (
	// builderActs refuses a candidate commit without the layout.
	builderActs layoutActor = "builder"
	// testWriterActs refuses an accepted tests' commit without the layout.
	testWriterActs layoutActor = "test writer"
	// maxChangedTestFiles bounds how many changed test-module paths one
	// refusal message names in full.
	maxChangedTestFiles = 20
)

const (
	// refusedReplacedFolder is a project folder that is no longer the
	// directory the control plane bound: replaced, removed or made a link.
	// Reasons start at 1, so a zero refusal is none of them.
	refusedReplacedFolder refusalReason = iota + 1
	// refusedNoRepository is a project folder outside any git repository.
	refusedNoRepository
	// refusedUnknownCommit is a candidate commit the repository does not
	// hold.
	refusedUnknownCommit
	// refusedMissingTests is an accepted tests' commit the repository no
	// longer holds, such as after its branch was deleted or rewritten.
	refusedMissingTests
	// refusedNoLayout is a commit without the test layout `surla init`
	// makes.
	refusedNoLayout
	// refusedTestModuleChanged is a candidate commit that changed
	// hand-written files in the test module since its common ancestor with
	// the accepted tests' commit.
	refusedTestModuleChanged
	// refusedNoCommonBase is a candidate commit that shares no common
	// ancestor with the accepted tests' commit.
	refusedNoCommonBase
)

// testsNotAcceptedBuilderStep is the one step a builder refused with
// tests_not_accepted can take in every state. Only an acceptance makes the
// builder's assignment unread, so the project server's Chorus reminder reaches
// the builder exactly then.
const testsNotAcceptedBuilderStep = "keep your Chorus watcher running; Surla messages you there when the tests are accepted; then read get_builder_assignment and verify or call start_live_target again"

// Error says what is refused, why and who acts.
func (r *layoutRefusal) Error() string {
	switch r.reason {
	case refusedReplacedFolder:
		return fmt.Sprintf("the project folder %s was replaced or removed after Surla started, so the user restarts it:"+
			" stop and start Surla in the project folder, then try again", r.folder)
	case refusedMissingTests:
		return fmt.Sprintf("the accepted tests' commit %s is not in the project's repository:"+
			" the test writer reports again, so Surla records the tests anew, or the user builds again", r.commit)
	case refusedNoRepository:
		return fmt.Sprintf("the project folder %s is not inside a git repository, so it has no commits to verify:"+
			" make the project folder a git repository and commit it", r.folder)
	case refusedUnknownCommit:
		return fmt.Sprintf("the candidate commit %s is not in the project's repository; commit it and try again", r.commit)
	case refusedNoCommonBase:
		return fmt.Sprintf("candidate commit %s shares no history with the accepted tests' commit %s;"+
			" build the change again from the project's current commit, then read the new assignments and verify again", r.commit, r.testsCommit)
	case refusedTestModuleChanged:
		shown := r.changed[:min(len(r.changed), maxChangedTestFiles)]
		list := strings.Join(shown, ", ")
		if rest := len(r.changed) - len(shown); rest > 0 {
			list = fmt.Sprintf("%s (and %d more)", list, rest)
		}
		renameNote := ""
		testGoMod := path.Join(r.testModule, "go.mod")
		for _, c := range r.changed {
			if c == testGoMod {
				renameNote = "; renaming the service module is the user's to do on their own branch between changes, not during a build"
				break
			}
		}
		return fmt.Sprintf("candidate commit %s changes hand-written files in the test module %s since base commit %s (%s);"+
			" tests in %s belong to the test writer, not the builder%s:"+
			" restore these files to their versions or absence in commit %s, commit, and call verify_change or start_live_target again",
			r.commit, r.testModule, r.baseCommit, list, r.testModule, renameNote, r.baseCommit)
	}
	step := "the builder commits the layout surla init makes and verifies again"
	if r.actor == testWriterActs {
		step = "the test writer reports again, so Surla checks and records its tests again"
	}
	return fmt.Sprintf("commit %s does not have the test layout surla init makes (%s): %s", r.commit, r.missing, step)
}

// Error says only what is refused; each caller words the state and the step
// for its own reader.
func (r testsNotAccepted) Error() string {
	return "the tests of this build are not accepted"
}

// verificationPlan returns the configuration of a verification run of
// candidateCommit for the change changeID at revision, which its caller has
// already authorized: it owns the change, the change is confirmed and
// revision is its revision in force. It chooses the tests through
// testsChoice:
//
//   - With the stored evaluator, the operator's plan, as stored; it needs no
//     build.
//   - Otherwise the change's build must be at revision and its tests
//     accepted. The candidate's programs come from candidateCommit's layout,
//     the tests from the accepted commit's, and the test command is that
//     commit's generated entry point (scaffold.Layout.GeneratedSuiteCommand).
//
// The returned Plan carries the candidate commit, the revision and its test
// source; the caller adds the run's work folder. It refuses with
// not_implemented when this control plane runs no verification; no_build,
// tests_not_accepted and no_test_layout as the error constructors below
// describe; and conflict when the repository does not hold candidateCommit,
// the project folder was replaced, candidateCommit shares no history with the
// accepted tests' commit, or candidateCommit changes hand-written files in the
// test module since their common ancestor.
func (c *ControlPlane) verificationPlan(
	ctx context.Context,
	principal Principal,
	changeID, revision, candidateCommit string,
) (VerificationConfig, error) {
	c.mu.RLock()
	testsCommit, _, err := c.testsChoice(principal, changeID, revision)
	c.mu.RUnlock()
	return c.verificationPlanForTests(ctx, candidateCommit, revision, testsCommit, err)
}

// verificationBuild requires the current owned, undropped build at the entry
// to verification or approval, including stored-evaluator projects. Callers
// hold c.mu. Missing verification configuration is refused before classifying
// build roles; historical reads and live starts keep their own selection rules.
func (c *ControlPlane) verificationBuild(
	principal Principal, changeID, revision string,
) (heldBuild, error) {
	if _, none := c.verifier.(noVerification); none {
		return heldBuild{}, gensurla.MakeNotImplemented(errVerificationUnavailable)
	}
	held, err := c.workerOwnedBuild(principal, changeID)
	var missing *goa.ServiceError
	if errors.As(err, &missing) && missing.Name == "not_found" {
		return heldBuild{}, refuseNoBuild(changeID, revision, buildFile{}, false)
	}
	if err != nil {
		return heldBuild{}, err
	}
	if held.file.RequestRevision != revision {
		return heldBuild{}, refuseNoBuild(changeID, revision, held.file, true)
	}
	return held, nil
}

// checkCandidateBuildCommit applies rule 13 to an immutable candidate, using
// both the repository's build branches and the tips preserved by Drop. It
// takes no copy turn and changes no branch; its caller owns the entry's turn
// and supplies the bound workspace. Approval passes the report's commit. Each
// entry formats a failed invariant for its own reader.
func (c *ControlPlane) checkCandidateBuildCommit(
	ctx context.Context, workspace string, saved buildFile, candidate string,
) (testwriter.CommitCheck, error) {
	return c.checkBuildCommit(ctx, workspace, saved, candidate, testwriter.BuilderRole.Branch(saved.Number))
}

// checkBuildCommit applies L1 at the owning entry's explicit build branch.
// Drop tips remain a control-plane read; callers own their copy turn and
// supply the bound workspace without holding c.mu across Git reads.
func (c *ControlPlane) checkBuildCommit(
	ctx context.Context, workspace string, saved buildFile, commit, ownBranch string,
) (testwriter.CommitCheck, error) {
	c.mu.RLock()
	provenance, found, err := c.cards.loadMadeFrom()
	c.mu.RUnlock()
	if err != nil {
		return testwriter.CommitCheck{}, err
	}
	var recorded []testwriter.BranchTip
	if found && provenance.Start.Dropped != nil {
		tips := provenance.Start.Dropped.BranchTips
		for _, branch := range slices.Sorted(maps.Keys(tips)) {
			recorded = append(recorded, testwriter.BranchTip{Branch: branch, Commit: tips[branch]})
		}
	}
	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	exists, err := commitExists(work, workspace, commit)
	if err != nil {
		return testwriter.CommitCheck{}, err
	}
	if !exists {
		return testwriter.CommitCheck{}, gensurla.MakeConflict(&layoutRefusal{reason: refusedUnknownCommit, commit: commit})
	}
	branches := [2]string{testwriter.BuilderRole.Branch(saved.Number), testwriter.TestsRole.Branch(saved.Number)}
	return testwriter.CheckBuildCommit(work, workspace, commit, ownBranch, saved.Base, branches, recorded)
}

// checkCandidateDesign admits the candidate's canonical agreed design only
// when its merge base with the current project belongs to this build's own
// immutable provenance. Only VerifyChange uses this execution-entry policy.
func checkCandidateDesign(
	ctx context.Context, workspace string, saved buildFile, candidate string,
) error {
	expected, err := decodeDesignSource(saved.DesignSource)
	if err != nil {
		return err
	}
	if len(expected) == 0 {
		return gensurla.MakeConflict(errors.New("The open build records no agreed design. Ask the guide to propose a revision from its draft, adopt it, then build."))
	}
	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	repo, prefix, project, err := currentProjectCommit(work, workspace)
	if err != nil {
		return err
	}
	if project == "" {
		return gensurla.MakeConflict(errors.New("The project has no current Git commit. Commit the project, then build the change again and read its new assignment."))
	}
	actual, err := committedDesignAt(work, workspace, repo, prefix, candidate)
	if err != nil {
		// Parser faults describe candidate content, not private input storage.
		// Preserve Git/read faults for the entry's safe response and raw span.
		var syntax scanner.ErrorList
		var source *committedDesignCanonicalError
		if errors.As(err, &syntax) && errors.As(err, &source) {
			return candidateDesignRefusal(candidate, []string{source.file})
		}
		return err
	}
	var changed []string
	for _, file := range designPaths(nil, expected, actual) {
		if expected[file] != actual[file] {
			changed = append(changed, file)
		}
	}
	if len(changed) > 0 {
		return candidateDesignRefusal(candidate, changed)
	}
	base, found, err := mergeBaseCommit(work, repo, candidate, project)
	if err != nil {
		return err
	}
	if !found {
		return gensurla.MakeConflict(fmt.Errorf("candidate commit %s shares no history with the project's current commit %s: ask the user to Drop the current build on the views page; ask the guide to call update_draft, refresh_story and revise_change from the current project, adopt the revision, then start a new build, read its assignment and make the change again", candidate, project))
	}
	return checkDesignBase(work, workspace, base, &saved.MadeFrom)
}

// candidateDesignRefusal names differing candidate files and the builder's
// existing generation recovery without exposing raw parser/read diagnostics.
func candidateDesignRefusal(candidate string, files []string) error {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = testwriter.ShownName(file)
	}
	return gensurla.MakeConflict(fmt.Errorf("candidate commit %s differs from this build's agreed design in %s: call generate_code, commit the agreed design, and call verify_change again", candidate, strings.Join(names, ", ")))
}

// candidateBuildRefusal gives the builder the branch and working-copy repair
// goal, including uncommitted file changes retained by a soft reset.
func candidateBuildRefusal(candidate, base, branch string, check testwriter.CommitCheck) error {
	reasons := buildCommitReasons(candidate, base, branch, check)
	if reasons == "" {
		return nil
	}
	return gensurla.MakeBuildBranchMismatch(fmt.Errorf("%s. Make %s and your working copy hold neither the other builds' commits nor their changes to the named files. Make your own work again as a new commit on %s from base %s, then call verify_change again; if you cannot, ask the user to build the change again and make your own work again from that build's base.", reasons, branch, branch, base))
}

// approvalBuildRefusal tells the user to have the builder repair and verify
// the build again; approval never asks the user to make the builder's commits.
func approvalBuildRefusal(candidate, base, branch string, check testwriter.CommitCheck) error {
	reasons := buildCommitReasons(candidate, base, branch, check)
	if reasons == "" {
		return nil
	}
	return gensurla.MakeBuildBranchMismatch(fmt.Errorf("%s. Ask the builder to make %s and its working copy hold neither the other builds' commits nor their changes to the named files, make its own work again as a new commit on %s from base %s, and call verify_change again; then approve that run.", reasons, branch, branch, base))
}

// buildCommitReasons describes failed invariants without inventing file names
// for zero-file merges. L1 already bounds files and counts them all.
func buildCommitReasons(candidate, base, branch string, check testwriter.CommitCheck) string {
	var reasons []string
	if !check.OnBranch {
		reasons = append(reasons, fmt.Sprintf("candidate commit %s is not on %s", candidate, branch))
	}
	if !check.FromBase {
		reasons = append(reasons, fmt.Sprintf("candidate commit %s does not descend from this build's base %s", candidate, base))
	}
	if check.IsBase {
		reasons = append(reasons, fmt.Sprintf("candidate commit %s is this build's base and holds no builder work", candidate))
	}
	reasons = append(reasons, foreignBuildWorkReasons(check.Foreign)...)

	return strings.Join(reasons, "; ")
}

// foreignBuildWorkReasons shares L1's bounded, quoted work facts between
// builder refusals and test-writer findings, without builder-only base policy.
func foreignBuildWorkReasons(work []testwriter.ForeignWork) []string {
	var reasons []string
	for _, foreign := range work {
		reason := fmt.Sprintf("commit %s on %s merges work the user has not taken", foreign.MergeBase, testwriter.ShownBranch(foreign.Branch))
		if foreign.FileCount > 0 {
			files := make([]string, len(foreign.Files))
			for i, file := range foreign.Files {
				files[i] = testwriter.ShownName(file)
			}
			reason += fmt.Sprintf(" (%d files", foreign.FileCount)
			if len(files) > 0 {
				reason += ": " + strings.Join(files, ", ")
			}
			reason += ")"
		}
		reasons = append(reasons, reason)
	}
	return reasons
}

// verificationPlanForTests completes the shared plan from testsChoice's
// snapshot. Live starts select the confirmed card and tests in one c.mu read;
// both entry points map the same refusal and build exactly those inputs.
func (c *ControlPlane) verificationPlanForTests(
	ctx context.Context,
	candidateCommit, revision, testsCommit string,
	err error,
) (VerificationConfig, error) {
	var notAccepted testsNotAccepted
	if errors.As(err, &notAccepted) {
		// Both verification and live starts are builder operations.
		return VerificationConfig{}, refuseTestsNotAccepted(notAccepted.saved)
	}
	if err != nil {
		return VerificationConfig{}, err
	}
	switch v := c.verifier.(type) {
	case storedVerification:
		cfg := *v.cfg
		cfg.Plan.CandidateCommit, cfg.Plan.RequestRevision = candidateCommit, revision
		return cfg, nil
	case writtenTestVerification:
		plan, err := c.starterPlanAt(ctx, candidateCommit, testsCommit, testWriterActs)
		var refusal *layoutRefusal
		switch {
		case errors.As(err, &refusal) && (refusal.reason == refusedUnknownCommit ||
			refusal.reason == refusedReplacedFolder ||
			refusal.reason == refusedTestModuleChanged ||
			refusal.reason == refusedNoCommonBase):
			return VerificationConfig{}, gensurla.MakeConflict(refusal)
		case errors.As(err, &refusal):
			return VerificationConfig{}, refuseNoTestLayout(refusal)
		case err != nil:
			return VerificationConfig{}, err
		}
		cfg := *v.runs
		plan.ReportDir, plan.RequestRevision = cfg.Plan.ReportDir, revision
		cfg.Plan = plan
		return cfg, nil
	default:
		return VerificationConfig{}, gensurla.MakeNotImplemented(errVerificationUnavailable)
	}
}

// testsChoice returns the commit and source of the tests that judge changeID
// at revision for principal, which its caller has already authorized as the
// change's owner. It is the only function that chooses which tests a
// verification or live target uses, which tests approve_merge requires a report to have
// run, and which tests a saved approval must still match on read. Callers hold
// c.mu (for reading or writing); it takes no lock.
//
//   - With the stored evaluator, it returns the stored plan's SuiteCommit and
//     verify.TestsEvaluator, and needs no build.
//   - With written-test runs, the change's build (read through ownedBuild, so
//     an unusable build is no build) must be at revision and its tests must be
//     accepted; it returns that accepted commit and verify.TestsWritten, or
//     refuses with no_build, or returns testsNotAccepted. verificationPlan
//     and ApproveMerge word that refusal for their readers; ApproveMerge's
//     second check under the lock and the saved approval's read only test
//     for an error.
//   - Without verification configured, it refuses with not_implemented.
func (c *ControlPlane) testsChoice(
	principal Principal,
	changeID, revision string,
) (commit string, source verify.TestsSource, err error) {
	switch v := c.verifier.(type) {
	case storedVerification:
		return v.cfg.Plan.SuiteCommit, verify.TestsEvaluator, nil
	case writtenTestVerification:
		held, err := c.ownedBuild(principal, changeID)
		var missing *goa.ServiceError
		switch {
		case errors.As(err, &missing) && missing.Name == "not_found":
			// The caller checked the change, so what is missing is its build.
			return "", "", refuseNoBuild(changeID, revision, buildFile{}, false)
		case err != nil:
			return "", "", err
		}
		build := held.file
		if build.RequestRevision != revision {
			return "", "", refuseNoBuild(changeID, revision, build, true)
		}
		if build.TestWriter.State != testsAccepted {
			return "", "", testsNotAccepted{saved: build.TestWriter}
		}
		return build.TestWriter.Commit, verify.TestsWritten, nil
	default:
		return "", "", gensurla.MakeNotImplemented(errVerificationUnavailable)
	}
}

// starterPlanAt reads, with git and never from the working tree, the test
// layout `surla init` makes at two commits of the repository holding the
// bound workspace, and returns the plan that builds candidateCommit's
// programs and runs testsCommit's generated entry point, written by agents
// (verify.TestsWritten, the one place written tests are marked).
// Verification and live targets pass the accepted tests' commit. A project folder that is no longer the directory
// this control plane bound or is outside any git repository, a commit the
// repository does not hold, a commit lacking the layout, a candidate commit
// that shares no history with testsCommit, or a candidate commit that changes
// hand-written files in the test module since merge-base(testsCommit,
// candidateCommit) is refused with a *layoutRefusal saying what and who acts:
// the builder for the folder, candidateCommit, and the test-module guard,
// testsActor for testsCommit. New guarantees a bound workspace whenever this
// runs.
func (c *ControlPlane) starterPlanAt(ctx context.Context, candidateCommit, testsCommit string, testsActor layoutActor) (verify.Plan, error) {
	wsDir, wsDev, wsIno := c.currentWorkspaceTarget()
	_, root, err := openModuleRootWithIdentity(wsDir, wsDev, wsIno)
	if err != nil {
		return verify.Plan{}, &layoutRefusal{reason: refusedReplacedFolder, folder: wsDir}
	}
	if err := root.Close(); err != nil {
		return verify.Plan{}, fmt.Errorf("close project folder %s: %w", wsDir, err)
	}
	ctx, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	repoRoot, prefix, err := resolveStarterRepo(ctx, wsDir)
	if errors.Is(err, errNoRepository) {
		return verify.Plan{}, &layoutRefusal{reason: refusedNoRepository, folder: wsDir}
	}
	if err != nil {
		return verify.Plan{}, err
	}
	exists, err := commitExists(ctx, repoRoot, candidateCommit)
	if err != nil {
		return verify.Plan{}, err
	}
	if !exists {
		return verify.Plan{}, &layoutRefusal{reason: refusedUnknownCommit, commit: candidateCommit}
	}
	if testsCommit != candidateCommit {
		exists, err := commitExists(ctx, repoRoot, testsCommit)
		if err != nil {
			return verify.Plan{}, err
		}
		if !exists {
			return verify.Plan{}, &layoutRefusal{reason: refusedMissingTests, commit: testsCommit, actor: testsActor}
		}
	}
	// Module folders and committed paths are slash-separated and relative to
	// the repository root: "." and "eval" for a project at the root,
	// "services/shop" and "services/shop/eval" for one nested in it.
	candidateModule := path.Clean("./" + prefix)
	testModule := path.Join(candidateModule, scaffold.SuiteDir)
	candidate, err := starterLayoutAt(ctx, repoRoot, candidateCommit, candidateModule, testModule, builderActs)
	if err != nil {
		return verify.Plan{}, err
	}
	if err := requireLayoutTree(ctx, repoRoot, candidateCommit, path.Join(candidateModule, candidate.CandidatePackage), builderActs); err != nil {
		return verify.Plan{}, err
	}
	tests, err := starterLayoutAt(ctx, repoRoot, testsCommit, candidateModule, testModule, testsActor)
	if err != nil {
		return verify.Plan{}, err
	}
	entry := tests.GeneratedSuiteCommand()
	if err := requireLayoutTree(ctx, repoRoot, testsCommit, path.Join(testModule, entry), testsActor); err != nil {
		return verify.Plan{}, err
	}
	if testsCommit != candidateCommit {
		base, hasBase, err := mergeBaseCommit(ctx, repoRoot, testsCommit, candidateCommit)
		if err != nil {
			return verify.Plan{}, err
		}
		if !hasBase {
			return verify.Plan{}, &layoutRefusal{
				reason:      refusedNoCommonBase,
				commit:      candidateCommit,
				testsCommit: testsCommit,
			}
		}
		changed, err := changedTestModuleFiles(ctx, repoRoot, base, candidateCommit, testModule)
		if err != nil {
			return verify.Plan{}, err
		}
		if len(changed) > 0 {
			return verify.Plan{}, &layoutRefusal{
				reason:     refusedTestModuleChanged,
				commit:     candidateCommit,
				baseCommit: base,
				testModule: testModule,
				changed:    changed,
			}
		}
	}
	return verify.Plan{
		CandidateRepo:   repoRoot,
		CandidateCommit: candidateCommit,
		CandidateModule: candidateModule,
		Binaries:        map[string]string{candidate.Service: candidate.CandidatePackage},
		SuiteRepo:       repoRoot,
		SuiteCommit:     testsCommit,
		SuiteModule:     testModule,
		SuiteCommand:    entry,
		Tests:           verify.TestsWritten,
	}, nil
}

// starterLayoutAt reads the service module's and the test module's go.mod at
// commit and returns the layout scaffold.StarterLayout derives from them,
// refusing with a *layoutRefusal, for actor, a commit without either file or
// whose files StarterLayout refuses.
func starterLayoutAt(ctx context.Context, repoRoot, commit, serviceModule, testModule string, actor layoutActor) (scaffold.Layout, error) {
	var goMods [2][]byte
	for i, file := range []string{path.Join(serviceModule, "go.mod"), path.Join(testModule, "go.mod")} {
		content, found, err := readGitBlob(ctx, repoRoot, commit, file)
		if err != nil {
			return scaffold.Layout{}, err
		}
		if !found {
			return scaffold.Layout{}, &layoutRefusal{reason: refusedNoLayout, commit: commit, actor: actor, missing: "it has no regular file " + file}
		}
		goMods[i] = content
	}
	layout, err := scaffold.StarterLayout(goMods[0], goMods[1])
	if err != nil {
		return scaffold.Layout{}, &layoutRefusal{reason: refusedNoLayout, commit: commit, actor: actor, missing: err.Error()}
	}
	return layout, nil
}

// requireLayoutTree refuses with a *layoutRefusal, for actor, a commit
// without the committed folder treePath.
func requireLayoutTree(ctx context.Context, repoRoot, commit, treePath string, actor layoutActor) error {
	kind, err := gitEntry(ctx, repoRoot, commit, treePath)
	if err != nil {
		return err
	}
	if kind != gitTree {
		return &layoutRefusal{reason: refusedNoLayout, commit: commit, actor: actor, missing: "it has no folder " + treePath}
	}
	return nil
}

// isVerifier marks noVerification as a verifier.
func (noVerification) isVerifier() {}

// isVerifier marks storedVerification as a verifier.
func (storedVerification) isVerifier() {}

// isVerifier marks writtenTestVerification as a verifier.
func (writtenTestVerification) isVerifier() {}

// newVerifier builds New's verifier from opts, resolving its folders: the
// stored evaluator when opts.Verification is set, the written-test runs (in
// runsDir) when opts.WrittenTestRuns is, and no verification otherwise. New
// has already refused both together. The stored evaluator's plan is marked
// here, once, as the operator's (verify.TestsEvaluator), for every run and
// live copy; starterPlanAt marks every plan of written tests.
func newVerifier(opts Options, runsDir string) (verifier, error) {
	switch {
	case opts.Verification != nil:
		cfg, err := normalizeVerificationConfig(opts.Verification)
		if err != nil {
			return nil, err
		}
		cfg.Plan.Tests = verify.TestsEvaluator
		return storedVerification{cfg: cfg}, nil
	case opts.WrittenTestRuns != nil:
		runs, err := normalizeVerificationConfig(&VerificationConfig{
			RunsDir: runsDir,
			Plan:    verify.Plan{ReportDir: opts.WrittenTestRuns.ReportDir},
			Command: opts.WrittenTestRuns.Command,
			Timeout: opts.WrittenTestRuns.Timeout,
		})
		if err != nil {
			return nil, err
		}
		return writtenTestVerification{runs: runs}, nil
	default:
		return noVerification{}, nil
	}
}

// verifierRuns returns the run settings of v whose folders New checks lie
// outside the workspace, or nil for noVerification.
func verifierRuns(v verifier) *VerificationConfig {
	switch v := v.(type) {
	case storedVerification:
		return v.cfg
	case writtenTestVerification:
		return v.runs
	default:
		return nil
	}
}

// refuseNoBuild is testsChoice's no_build refusal of changeID at revision:
// ownedBuild found no build with a test writer (none was started, or the one
// started while the project verified with stored settings has none), or the
// build found is of another revision, which it names. It ends with the
// user's step.
func refuseNoBuild(changeID, revision string, build buildFile, found bool) error {
	if !found {
		return gensurla.MakeNoBuild(fmt.Errorf(
			"change %q has no build with a test writer, so no tests are accepted for revision %s: the user starts a build on the views page",
			changeID, revision))
	}
	return gensurla.MakeNoBuild(fmt.Errorf(
		"change %q is built at revision %s, but the revision in force is %s: the user starts a build of revision %s on the views page",
		changeID, build.RequestRevision, revision, revision))
}

// refuseTestsNotAccepted is verify_change's tests_not_accepted refusal of a
// build whose tests are not accepted, worded for the builder who calls
// verify_change: it names the test writer's state in the state's own words,
// then who acts, and ends with the builder's step.
func refuseTestsNotAccepted(saved testWriterAssignment) error {
	var state, who string
	switch saved.State {
	case testsAssigned:
		state, who = "assigned", "the test writer writes the required tests and reports them finished"
	case testsReported:
		state, who = "reported", "Surla is checking the test writer's report"
	case testsNeedChanges:
		state, who = "needs changes", "the test writer fixes Surla's findings and reports again"
	case testsCouldNotRun:
		// The saved step is a sentence; its full stop would end the refusal early.
		state, who = "could not run", "Surla's check could not run ("+saved.Reason+"); the user's step: "+strings.TrimSuffix(saved.Step, ".")
	}
	return gensurla.MakeTestsNotAccepted(fmt.Errorf("the tests of this build are not accepted; their state is %s: %s; your step: %s", state, who, testsNotAcceptedBuilderStep))
}

// refuseApprovalTestsNotAccepted is approve_merge's tests_not_accepted refusal
// of a build whose tests are not accepted, worded for the user who reads it on
// the views page: it names the test writer's state in the state's own words,
// then who acts, and ends with the step, naming the builder rather than
// instructing it.
func refuseApprovalTestsNotAccepted(saved testWriterAssignment) error {
	var state, step string
	switch saved.State {
	case testsAssigned:
		state, step = "assigned", "the test writer writes the required tests and reports them finished; the builder verifies again once Surla accepts them"
	case testsReported:
		state, step = "reported", "Surla is checking the test writer's report; the builder verifies again after its verdict"
	case testsNeedChanges:
		state, step = "needs changes", "the test writer fixes Surla's findings and reports again; the builder verifies again once Surla accepts them"
	case testsCouldNotRun:
		state, step = "could not run", "Surla's check could not run ("+saved.Reason+"); the user's step: "+saved.Step
	}
	return gensurla.MakeTestsNotAccepted(fmt.Errorf("the tests of this build are not accepted; their state is %s: %s", state, step))
}

// refuseNoTestLayout is verify_change's no_test_layout refusal of a commit
// without the test layout `surla init` makes, of an accepted tests' commit
// the repository no longer holds, or of a project folder outside any git
// repository: it names what is missing and ends with the step.
func refuseNoTestLayout(refusal *layoutRefusal) error {
	return gensurla.MakeNoTestLayout(refusal)
}
