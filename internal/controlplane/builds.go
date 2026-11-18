// This file owns builds: the user's decision to build a confirmed change at
// one revision, saved once per project beside the change cards, and the
// builder's and the test writer's assignments for that build.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	goa "goa.design/goa/v3/pkg"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
)

type (
	// heldBuild is a saved build together with its change card and whether it
	// is for its change's revision in force, as every read of a build or an
	// assignment reports.
	heldBuild struct {
		// file is the saved build.
		file buildFile
		// record is the saved Change Card for file.ChangeID.
		record storedChangeCard
		// inForce is buildInForce for file and its change's card.
		inForce bool
		// storedEvaluator is true when projectTestSource answers that the
		// project verifies with its stored evaluator (storedEvaluator): then
		// no test writer is used, whatever file records.
		storedEvaluator bool
	}

	// builderAssignment is the builder's saved assignment for one build. It
	// has no state: it exists from the moment the user starts the build, and
	// verification of the build's revision, not the assignment, judges the
	// builder's work.
	builderAssignment struct {
		// Read records that the builder read the assignment in full.
		Read bool `json:"read"`
	}
)

const (
	// buildRolesBuilderOnly records a build made with a stored evaluator.
	buildRolesBuilderOnly = "builder_only"
	// buildRolesBuilderAndTestWriter records a build with its own test-writer copy.
	buildRolesBuilderAndTestWriter = "builder_and_test_writer"
	// testsAssigned is the test writer's state until it reports.
	testsAssigned = "assigned"
	// testsReported is the test writer's state after it reported its tests
	// finished and before they are judged.
	testsReported = "reported"
	// testsNeedChanges is the test writer's state after a check found problems.
	testsNeedChanges = "needs_changes"
	// testsAccepted is the test writer's state after a check accepted the tests.
	testsAccepted = "accepted"
	// testsCouldNotRun is the test writer's state after a check could not run
	// for a cause the test writer cannot fix.
	testsCouldNotRun = "could_not_run"
	// maxCheckReason and maxCheckStep bound, in characters, the reason and
	// the user's step of testsCouldNotRun, as the design's TestsCouldNotRun
	// declares.
	maxCheckReason = 1024
	maxCheckStep   = 512
	// testsNotJudged is the sentence every accepted result of the test
	// writer's tests carries, the one value the design's TestsAccepted
	// not_judged allows.
	testsNotJudged = "Surla checked that these tests build into the generated test program and declare every required scenario; it did not judge what they assert."
)

// GetBuild returns the owner's change's usable build, or not_started when no
// build serves the current test source, and whether it is for the revision in force.
// It fails for an unknown change (not_found) or another owner's change
// (forbidden) and changes nothing.
func (c *ControlPlane) GetBuild(ctx context.Context, p *gensurla.GetBuildPayload) (*gensurla.ChangeBuild, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.mu.RLock()
	record, err := c.ownedCard(principal, p.ChangeID)
	var saved buildFile
	var found bool
	var provenance madeFromRecord
	if err == nil {
		saved, found, err = c.cards.loadOpenBuild()
	}
	if err == nil {
		provenance, _, err = c.cards.loadMadeFrom()
	}
	c.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	return c.changeBuild(ctx, record, saved, found, draftedBeforeDrop(record, provenance))
}

// GetTestFindings returns, for the owner's page, the text of the findings of
// the owner's change's build, with the build's revision and start time, the
// report checked and the verdict's time; get_build carries only their number.
// It fails for an unknown change or a change with no build (not_found),
// another owner's change (forbidden), and a build whose tests do not need
// changes (conflict), and changes nothing.
func (c *ControlPlane) GetTestFindings(ctx context.Context, p *gensurla.GetTestFindingsPayload) (*gensurla.TestFindings, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	held, err := c.ownedBuild(principal, p.ChangeID)
	if err != nil {
		return nil, err
	}
	saved := held.file
	if state := testWriterProgress(held).Tests.Kind(); state != gensurla.TestsKindNeedsChanges {
		return nil, gensurla.MakeConflict(fmt.Errorf(
			"the tests of change %q do not need changes: their state is %s, which get_build shows", p.ChangeID, state))
	}
	return &gensurla.TestFindings{
		ChangeID:        saved.ChangeID,
		RequestRevision: saved.RequestRevision,
		StartedAt:       saved.StartedAt.UTC().Format(time.RFC3339Nano),
		Report:          saved.ReportNumber,
		CheckedAt:       verdictTime(saved.TestWriter.CheckedAt),
		Findings:        slices.Clone(saved.TestWriter.Findings),
	}, nil
}

// ListBuilderAssignments returns the open build's builder assignment while the
// builder has work on it, or an empty list otherwise. It records nothing.
func (c *ControlPlane) ListBuilderAssignments(ctx context.Context) (*gensurla.BuilderAssignmentList, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.workspaceMu.RLock()
	defer c.workspaceMu.RUnlock()
	builds, err := c.ownedBuilds(principal.ID)
	if err != nil {
		return nil, err
	}
	active := make([]heldBuild, 0, len(builds))
	for _, held := range builds {
		hasWork, err := c.builderHasWork(principal.ID, held)
		if err != nil {
			return nil, err
		}
		if hasWork {
			active = append(active, held)
		}
	}
	items := make([]*gensurla.BuilderAssignment, len(active))
	if len(active) > 0 {
		work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
		defer cancel()
		_, folder, copyRoot, err := builderCopyFolder(work, c.activeWorkspaceDir)
		if err != nil {
			return nil, err
		}
		folder, goworkOff, err := builderWorkingFolder(folder, copyRoot)
		if err != nil {
			return nil, err
		}
		for i, held := range active {
			items[i] = builderAssignmentResult(held, folder, goworkOff)
		}
	}
	return &gensurla.BuilderAssignmentList{Assignments: items}, nil
}

// ListTestWriterAssignments returns the open build's test-writer assignment
// while the test writer has work on it, or an empty list otherwise. It records
// nothing.
func (c *ControlPlane) ListTestWriterAssignments(ctx context.Context) (*gensurla.TestWriterAssignmentList, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	builds, err := c.ownedBuilds(principal.ID)
	if err != nil {
		return nil, err
	}
	items := make([]*gensurla.TestWriterAssignmentItem, 0, len(builds))
	for _, held := range builds {
		if testWriterHasWork(held) {
			items = append(items, testWriterAssignmentItem(held))
		}
	}
	return &gensurla.TestWriterAssignmentList{Assignments: items}, nil
}

// changeBuild reads and renders record's usable build without changing it.
// The caller has authorized access to record and captured draftPredatesDrop
// from that record and the same locked provenance read. Git and run reads
// happen after the card lock is released.
func (c *ControlPlane) changeBuild(
	ctx context.Context, record storedChangeCard, saved buildFile, found, draftPredatesDrop bool,
) (*gensurla.ChangeBuild, error) {
	changeID := record.card.ChangeID
	if !found || saved.ChangeID != changeID || !c.usableBuild(saved) {
		return &gensurla.ChangeBuild{
			ChangeID: changeID, DraftedBeforeDrop: draftPredatesDrop,
			Build: gensurla.NewBuildNotStarted(&gensurla.BuildNotStarted{}),
		}, nil
	}
	held := heldBuild{file: saved, record: record, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()}
	build, err := c.startedBuild(ctx, record.owner, held)
	if err != nil {
		return nil, err
	}
	return &gensurla.ChangeBuild{
		ChangeID: changeID, DraftedBeforeDrop: draftPredatesDrop,
		Build: gensurla.NewBuildStarted(build),
	}, nil
}

// buildInForce reports whether saved is a build of record's agreed revision
// (see storedChangeCard.agreedRevision); get_build and every assignment read
// report it as in_force.
func buildInForce(saved buildFile, record storedChangeCard) bool {
	return saved.RequestRevision == record.agreedRevision()
}

// startedBuild converts a held build into its transport form, rejecting a
// saved Story whose required tests cannot be named by the generator.
func startedBuild(held heldBuild, taking gensurla.BuildTaking) (*gensurla.StartedBuild, error) {
	required, err := requiredStoryTests(held.file.Stories)
	if err != nil {
		return nil, fmt.Errorf("name the required tests of change %q: %w", held.file.ChangeID, err)
	}
	return &gensurla.StartedBuild{
		RequestRevision:    held.file.RequestRevision,
		StartedAt:          held.file.StartedAt.UTC().Format(time.RFC3339Nano),
		InForce:            held.inForce,
		Builder:            builderProgress(held.file.Builder),
		TestWriter:         testWriterProgress(held),
		RequiredTestCount:  len(required),
		RequiredTests:      required,
		Answers:            held.file.Answers,
		Examples:           held.file.Examples,
		CoverageStatements: acceptedCoverageStatements(held),
		Taking:             taking,
	}, nil
}

// requiredStoryTests shares the generator's mapping across build, assignment
// and run-review reads, preserving declaration order and excluding drafts.
func requiredStoryTests(stories []designeval.StorySummary) ([]*gensurla.RequiredTest, error) {
	scenarios, _, err := designeval.StoryScenarios(stories)
	if err != nil {
		return nil, err
	}
	required := make([]*gensurla.RequiredTest, len(scenarios))
	for i, scenario := range scenarios {
		required[i] = &gensurla.RequiredTest{
			Story: scenario.Story, FlowKey: scenario.Key,
			Scenario: scenario.Scenario, Hook: scenario.Hook,
		}
	}
	return required, nil
}

// builderAssignmentResult converts a held build into the builder's
// assignment.
func builderAssignmentResult(held heldBuild, folder string, goworkOff bool) *gensurla.BuilderAssignment {
	return &gensurla.BuilderAssignment{
		ChangeID:        held.file.ChangeID,
		RequestRevision: held.file.RequestRevision,
		InForce:         held.inForce,
		Read:            held.file.Builder.Read,
		WrittenTests:    writtenTests(held),
		WorkingFolder:   folder,
		GoworkOff:       goworkOff,
	}
}

// testWriterAssignmentItem converts a held build into the test writer's
// assignment summary.
func testWriterAssignmentItem(held heldBuild) *gensurla.TestWriterAssignmentItem {
	return &gensurla.TestWriterAssignmentItem{
		ChangeID:        held.file.ChangeID,
		RequestRevision: held.file.RequestRevision,
		InForce:         held.inForce,
		Progress:        testWriterProgress(held),
	}
}

// builderProgress converts the builder's saved assignment.
func builderProgress(saved builderAssignment) *gensurla.BuilderProgress {
	return &gensurla.BuilderProgress{Read: saved.Read}
}

// testWriterProgress converts the test writer's saved assignment and the
// build's report number. A project that verifies with its stored evaluator
// uses no test writer, so its build reads not_used whatever was saved.
// decodeBuild accepted only the five saved states below, each with exactly
// its fields.
func testWriterProgress(held heldBuild) *gensurla.TestWriterProgress {
	saved := held.file.TestWriter
	var tests gensurla.Tests
	switch {
	case held.storedEvaluator:
		tests = gensurla.NewTestsNotUsed(&gensurla.TestsNotUsed{})
	case saved.State == testsAssigned:
		tests = gensurla.NewTestsAssigned(&gensurla.TestsAssigned{})
	case saved.State == testsReported:
		tests = gensurla.NewTestsReported(&gensurla.TestsReported{})
	case saved.State == testsNeedChanges:
		tests = gensurla.NewTestsNeedsChanges(&gensurla.TestsNeedChanges{
			FindingCount: len(saved.Findings),
			CheckedAt:    verdictTime(saved.CheckedAt),
		})
	case saved.State == testsAccepted:
		tests = gensurla.NewTestsAccepted(acceptedTests(saved))
	case saved.State == testsCouldNotRun:
		tests = gensurla.NewTestsCouldNotRun(&gensurla.TestsCouldNotRun{
			Reason:    saved.Reason,
			Step:      saved.Step,
			CheckedAt: verdictTime(saved.CheckedAt),
		})
	}
	return &gensurla.TestWriterProgress{Tests: tests, Read: saved.Read, Report: held.file.ReportNumber}
}

// writtenTests renders, for the builder, where the test writer's tests for
// held stand: not used, not yet accepted, or accepted at a commit. It reads
// the same saved state and stored-evaluator answer as testWriterProgress.
func writtenTests(held heldBuild) gensurla.WrittenTests {
	saved := held.file.TestWriter
	switch {
	case held.storedEvaluator:
		return gensurla.NewWrittenTestsNotUsed(&gensurla.TestsNotUsed{})
	case saved.State == testsAccepted:
		return gensurla.NewWrittenTestsAccepted(acceptedTests(saved))
	default:
		return gensurla.NewWrittenTestsNotYetAccepted(&gensurla.TestsNotYetAccepted{})
	}
}

// testWriterHasWork reports whether held needs the test writer: only when the
// build uses Surla's test writer and its tests are not yet accepted.
func testWriterHasWork(held heldBuild) bool {
	return !held.storedEvaluator &&
		held.file.Roles == buildRolesBuilderAndTestWriter &&
		held.file.TestWriter.State != testsAccepted
}

// acceptedTests renders the test writer's saved acceptance, which every
// reader sees the same way, with the sentence saying what Surla did not
// judge.
func acceptedTests(saved testWriterAssignment) *gensurla.TestsAccepted {
	return &gensurla.TestsAccepted{Commit: saved.Commit, CheckedAt: verdictTime(saved.CheckedAt), NotJudged: testsNotJudged}
}

// verdictTime formats a saved verdict time as its transport form.
func verdictTime(at time.Time) string {
	return at.UTC().Format(time.RFC3339Nano)
}

// recordBuildError records err on span as a failure unless it is a refusal
// the design declares, and returns err.
func recordBuildError(span trace.Span, err error, status string) error {
	var offBranch *gensurla.CopyOffBranch
	if errors.As(err, &offBranch) {
		return err
	}
	var refused *goa.ServiceError
	if !errors.As(err, &refused) || refused.Fault {
		span.RecordError(err)
		span.SetStatus(codes.Error, status)
	}
	return err
}

// boundedText reports whether s is non-empty and at most limit runes long.
func boundedText(s string, limit int) bool {
	return s != "" && utf8.RuneCountInString(s) <= limit
}

// buildableCard loads the caller's card for changeID and checks that it is
// confirmed at revision. Callers hold c.mu.
func (c *ControlPlane) buildableCard(principal Principal, changeID, revision string) (storedChangeCard, error) {
	record, err := c.ownedCard(principal, changeID)
	if err != nil {
		return storedChangeCard{}, err
	}
	agreed := record.agreedRevision()
	if agreed == "" {
		return storedChangeCard{}, gensurla.MakeConflict(fmt.Errorf("change_id %q must be confirmed before it is built (status %q)", changeID, record.card.Status))
	}
	if agreed != revision {
		return storedChangeCard{}, gensurla.MakeConflict(fmt.Errorf("request_revision %q is not the agreed revision of %q, %q; a proposed revision is built only after the user adopts it", revision, changeID, agreed))
	}
	if record.architecture == nil || record.goMod == "" || record.madeFrom == nil || record.designSource == "" {
		return storedChangeCard{}, gensurla.MakeConflict(errors.New("The agreed revision records no design, so there is nothing to build from. Ask the guide to propose a revision from its draft, adopt it, then build."))
	}
	return record, nil
}

// ownedCard loads the card for changeID and checks that principal owns it.
// Callers hold c.mu.
func (c *ControlPlane) ownedCard(principal Principal, changeID string) (storedChangeCard, error) {
	record, ok, err := c.cards.load(changeID)
	if err != nil {
		return storedChangeCard{}, err
	}
	if !ok {
		return storedChangeCard{}, gensurla.MakeNotFound(fmt.Errorf("unknown change_id %q", changeID))
	}
	if record.owner != principal.ID {
		return storedChangeCard{}, gensurla.MakeForbidden(fmt.Errorf("principal %q is not the owner of change_id %q", principal.ID, changeID))
	}
	return record, nil
}

// ownedBuild loads the build of the caller's change changeID, failing with
// not_found when the change or its build does not exist. Callers hold c.mu.
func (c *ControlPlane) ownedBuild(principal Principal, changeID string) (heldBuild, error) {
	record, err := c.ownedCard(principal, changeID)
	if err != nil {
		return heldBuild{}, err
	}
	saved, found, err := c.cards.loadOpenBuild()
	if err != nil {
		return heldBuild{}, err
	}
	if !found || saved.ChangeID != changeID || !c.usableBuild(saved) {
		return heldBuild{}, gensurla.MakeNotFound(fmt.Errorf("change_id %q has no usable build; start a new build on the views page", changeID))
	}
	provenance, hasProvenance, err := c.cards.loadMadeFrom()
	if err != nil {
		return heldBuild{}, err
	}
	if hasProvenance && buildDropped(provenance, saved) {
		return heldBuild{}, gensurla.MakeNotFound(fmt.Errorf("The user dropped change %s: stop work on it; make no more edits or commits for it. The user starts builds on the views page.", saved.ChangeID))
	}
	return heldBuild{file: saved, record: record, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()}, nil
}

// ownedBuilds returns only the project's one usable build when owner owns its
// card. A corrupt record or required card fails rather than being omitted.
func (c *ControlPlane) ownedBuilds(owner string) ([]heldBuild, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	saved, found, err := c.cards.loadOpenBuild()
	if err != nil || !found {
		return nil, err
	}
	record, found, err := c.cards.load(saved.ChangeID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("open build names missing change %q", saved.ChangeID)
	}
	provenance, hasProvenance, err := c.cards.loadMadeFrom()
	if err != nil {
		return nil, err
	}
	if hasProvenance && buildDropped(provenance, saved) {
		return nil, nil
	}
	if record.owner != owner || !c.usableBuild(saved) {
		return nil, nil
	}
	return []heldBuild{{file: saved, record: record, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()}}, nil
}

// usableBuild is the single predicate for every build read and consumer. A build
// started without a writer remains usable only while stored verification
// settings still supply its tests. A writer build retains its saved copy,
// snapshots and unread work even when its revision is no longer in force.
// Callers pass a build validated by decodeBuild or constructed at StartBuild.
func (c *ControlPlane) usableBuild(saved buildFile) bool {
	return saved.Roles == buildRolesBuilderAndTestWriter ||
		saved.Roles == buildRolesBuilderOnly && c.storedEvaluator()
}

// newestPassedBuild reads every run of this change and returns the newest pass
// accepted for this start's revision, including one accepted exactly at start.
// A later failed run never erases a pass. Missing run storage proves no pass.
func (c *ControlPlane) newestPassedBuild(owner string, saved buildFile) (*verifyrun.Run, error) {
	runs, err := c.eligibleBuildRuns(owner, saved)
	if err != nil || len(runs) == 0 {
		return nil, err
	}
	return &runs[0], nil
}

// builderHasWork reports whether held needs the builder: while no verification
// of this start's change and revision accepted at or after StartedAt has
// passed with inputs that are still current (verificationInputsCurrent). A
// failed run read returns an error rather than reporting no work.
func (c *ControlPlane) builderHasWork(owner string, held heldBuild) (bool, error) {
	runs, err := c.eligibleBuildRuns(owner, held.file)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if c.verificationInputsCurrent(held.record, held.file, true, run.Request.Plan) {
			return false, nil
		}
	}
	return true, nil
}

// startedBuild renders current run and branch facts outside the card lock.
// Branch names identify the numbered build where its commits were verified;
// later ref changes do not erase a pass or block reads and recovery.
func (c *ControlPlane) startedBuild(ctx context.Context, owner string, held heldBuild) (*gensurla.StartedBuild, error) {
	return c.startedBuildAt(ctx, owner, held, "")
}

// startedBuildAt uses the caller's captured project commit when provided.
func (c *ControlPlane) startedBuildAt(ctx context.Context, owner string, held heldBuild, project string) (*gensurla.StartedBuild, error) {
	c.mu.RLock()
	provenance, found, err := c.cards.loadMadeFrom()
	c.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if found && buildDropped(provenance, held.file) {
		work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
		defer cancel()
		workspace, _, _ := c.currentWorkspaceTarget()
		repo, _, err := resolveStarterRepo(work, workspace)
		if err != nil {
			return nil, err
		}
		tips, err := buildBranchTips(work, repo, held.file.Number)
		if err != nil {
			return nil, err
		}
		branches, err := keptBuildBranches(work, repo, held.file.Base, tips)
		if err != nil {
			return nil, err
		}
		return startedBuild(held, gensurla.NewBuildTakingDropped(&gensurla.BuildDropped{KeptBranches: branches}))
	}
	runs, err := c.eligibleBuildRuns(owner, held.file)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return startedBuild(held, gensurla.NewBuildTakingBuilding(&gensurla.BuildBuilding{}))
	}
	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	workspace, _, _ := c.currentWorkspaceTarget()
	repo, _, err := resolveStarterRepo(work, workspace)
	if err != nil {
		return nil, err
	}
	if project == "" {
		projectBytes, err := runGitRead(work, repo, "rev-parse", "--verify", "HEAD^{commit}")
		if err != nil {
			return nil, err
		}
		project = strings.TrimSpace(string(projectBytes))
	}
	for _, candidate := range runs {
		merged, err := runTaken(work, repo, project, candidate)
		if err != nil {
			return nil, err
		}
		if merged {
			return startedBuild(held, gensurla.NewBuildTakingMerged(&gensurla.BuildMerged{}))
		}
	}
	run := runs[0]
	candidateBranch := testwriter.BuilderRole.Branch(held.file.Number)
	tests := gensurla.NewBuildPassedTestSourceEvaluator(&gensurla.OperatorEvaluatorTests{})
	if run.Request.Plan.Tests == verify.TestsWritten {
		branch := testwriter.TestsRole.Branch(held.file.Number)
		tests = gensurla.NewBuildPassedTestSourceWrittenTests(&gensurla.BuildPassedTests{
			Commit: run.Request.Plan.SuiteCommit, Branch: branch,
		})
	}
	baseInBranch, err := buildCommitInHistory(work, repo, held.file.Base, project)
	if err != nil {
		return nil, err
	}
	return startedBuild(held, gensurla.NewBuildTakingPassed(&gensurla.BuildPassed{
		RunID: run.Request.RunID, CandidateCommit: run.Request.Plan.CandidateCommit,
		CandidateBranch: candidateBranch, Tests: tests, BaseInBranch: baseInBranch,
	}))
}

// buildCommitInHistory distinguishes a normal non-ancestor from a failed Git
// read; callers supply validated commits and Surla-owned branch names or HEAD.
func buildCommitInHistory(ctx context.Context, repo, commit, tip string) (bool, error) {
	_, err := runGitRead(ctx, repo, "merge-base", "--is-ancestor", commit, tip)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("check build commit history: %w", err)
}

// activeBuildFound derives the written-test selection from one provenance read.
// Callers hold c.mu and retain found separately when rendering a dropped build.
func (c *ControlPlane) activeBuildFound(saved buildFile, found bool) (bool, error) {
	if !found {
		return false, nil
	}
	provenance, exists, err := c.cards.loadMadeFrom()
	if err != nil {
		return false, err
	}
	return !exists || !buildDropped(provenance, saved), nil
}
