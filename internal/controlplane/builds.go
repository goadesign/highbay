// This file owns builds: the user's decision to build a confirmed change at
// one revision, saved once per project beside the change cards, and the
// builder's and the test writer's assignments for that build.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
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
	// heldBuild is a saved build together with whether it is for its change's
	// revision in force, as every read of a build or an assignment reports.
	heldBuild struct {
		// file is the saved build.
		file buildFile
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
	if err == nil {
		saved, found, err = c.cards.loadOpenBuild()
	}
	c.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	return c.changeBuild(ctx, record, saved, found)
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

// ListBuilderAssignments returns the builder's assignment of every build of
// the caller's changes, most recently started first. It records nothing.
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
	items := make([]*gensurla.BuilderAssignment, len(builds))
	if len(builds) > 0 {
		folder, goworkOff, err := builderWorkingFolder(c.activeWorkspaceDir, outermost(c.protectedDirs))
		if err != nil {
			return nil, err
		}
		for i, held := range builds {
			items[i] = builderAssignmentResult(held, folder, goworkOff)
		}
	}
	return &gensurla.BuilderAssignmentList{Assignments: items}, nil
}

// GetBuilderAssignment returns the builder's assignment for the caller's
// change and records that the builder read it; a later build of the change
// replaces it unread. It fails with not_found when the change or its build
// does not exist and forbidden for another owner's change.
func (c *ControlPlane) GetBuilderAssignment(
	ctx context.Context,
	p *gensurla.GetBuilderAssignmentPayload,
) (*gensurla.BuilderAssignment, error) {
	c.workspaceMu.RLock()
	defer c.workspaceMu.RUnlock()
	var folder string
	var goworkOff bool
	held, err := c.readAssignment(ctx, "surla.controlplane.get_builder_assignment", p.ChangeID, func(saved *buildFile) (bool, error) {
		// readAssignment established ownership; derive facts before recording
		// the read, so a filesystem failure consumes no assignment.
		var err error
		folder, goworkOff, err = builderWorkingFolder(c.activeWorkspaceDir, outermost(c.protectedDirs))
		if err != nil {
			return false, err
		}
		changed := !saved.Builder.Read
		saved.Builder.Read = true
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	return builderAssignmentResult(held, folder, goworkOff), nil
}

// ListTestWriterAssignments returns the test writer's assignment of every
// build of the caller's changes, most recently started first. It records
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
		if held.file.Roles == buildRolesBuilderAndTestWriter {
			items = append(items, testWriterAssignmentItem(held))
		}
	}
	return &gensurla.TestWriterAssignmentList{Assignments: items}, nil
}

// GetTestWriterAssignment returns the test writer's assignment for the
// caller's change in full, with the Story, answers, examples and preserved
// behaviors as agreed at the built revision, one required test per finished
// flow and the text of any findings, and records that the test writer read
// it; a later build of the change replaces it unread. It fails with not_found
// when the change or its build does not exist and forbidden for another
// owner's change. A build that holds no example was saved without the agreed
// terms: the read fails, naming the step to build again, and records nothing.
func (c *ControlPlane) GetTestWriterAssignment(
	ctx context.Context,
	p *gensurla.GetTestWriterAssignmentPayload,
) (*gensurla.TestWriterAssignment, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.workspaceMu.RLock()
	defer c.workspaceMu.RUnlock()
	c.mu.RLock()
	owned, err := c.ownedBuild(principal, p.ChangeID)
	c.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if owned.file.Roles != buildRolesBuilderAndTestWriter {
		return nil, gensurla.MakeNotFound(errors.New("this build has no test-writer assignment because it was started with stored verification settings"))
	}
	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	_, prefix, err := resolveStarterRepo(work, c.activeWorkspaceDir)
	if err != nil {
		return nil, err
	}
	folder := filepath.Join(c.activeWorkspaceDir, testwriter.Folder, filepath.FromSlash(prefix))
	held, err := c.readAssignment(ctx, "surla.controlplane.get_test_writer_assignment", p.ChangeID, func(saved *buildFile) (bool, error) {
		if saved.Roles != buildRolesBuilderAndTestWriter {
			return false, gensurla.MakeNotFound(errors.New("this build has no test-writer assignment because it was started with stored verification settings"))
		}
		// Every confirmed revision has an example, so a build without one was
		// saved without the agreed terms the tests are owed.
		if len(saved.Examples) == 0 {
			return false, fmt.Errorf("the build of change %q holds none of the agreed answers, examples and preserved behaviors; "+
				"the user builds this change again on the views page", saved.ChangeID)
		}
		changed := !saved.TestWriter.Read
		saved.TestWriter.Read = true
		return changed, nil
	})
	if err != nil {
		return nil, err
	}
	// start_build refused a Story whose flows cannot be named, so an error
	// here means the saved Story changed outside this package.
	scenarios, _, err := designeval.StoryScenarios(held.file.Stories)
	if err != nil {
		return nil, fmt.Errorf("name the required tests of change %q: %w", held.file.ChangeID, err)
	}
	required := make([]*gensurla.RequiredTest, len(scenarios))
	for i, s := range scenarios {
		required[i] = &gensurla.RequiredTest{Story: s.Story, FlowKey: s.Key, Scenario: s.Scenario, Hook: s.Hook}
	}
	item := testWriterAssignmentItem(held)
	findings := []string{}
	if item.Progress.Tests.Kind() == gensurla.TestsKindNeedsChanges {
		findings = slices.Clone(held.file.TestWriter.Findings)
	}
	return &gensurla.TestWriterAssignment{
		ChangeID:           item.ChangeID,
		RequestRevision:    item.RequestRevision,
		InForce:            item.InForce,
		Progress:           item.Progress,
		Findings:           findings,
		WorkingFolder:      folder,
		DesignSource:       held.file.DesignSource,
		Stories:            storySummaryItems(held.file.Stories),
		RequiredTests:      required,
		Answers:            held.file.Answers,
		Examples:           held.file.Examples,
		PreservedBehaviors: held.file.PreservedBehaviors,
	}, nil
}

// ReportTestsFinished records that the test writer finished its tests for the
// caller's change at the build's revision: the build's report number rises by
// one, the test writer's state becomes reported and any earlier verdict is
// cleared. It then wakes the report checker without waiting for it, so the
// verdict follows without another call. It fails with not_found when the
// change or its build does not exist, forbidden for another owner's change,
// and conflict for any other revision or in a project that verifies with its
// stored evaluator, where no test writer is used; a refused report changes
// nothing.
func (c *ControlPlane) ReportTestsFinished(
	ctx context.Context,
	p *gensurla.ReportTestsFinishedPayload,
) (*gensurla.TestWriterAssignmentItem, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	_, span := c.tracer("surla/controlplane").Start(ctx, "surla.controlplane.report_tests_finished", trace.WithAttributes(
		attribute.String("surla.change_id", p.ChangeID),
		attribute.String("surla.request_revision", p.RequestRevision),
	))
	defer span.End()
	// reportMu first: a report waits while the report checker confirms,
	// records and saves a verdict, so it never falls between them.
	c.reportMu.Lock()
	defer c.reportMu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	held, err := c.ownedBuild(principal, p.ChangeID)
	if err != nil {
		return nil, recordBuildError(span, err, "load build")
	}
	saved := &held.file
	if saved.Roles != buildRolesBuilderAndTestWriter {
		return nil, gensurla.MakeNotFound(errors.New("this build has no test-writer assignment because it was started with stored verification settings"))
	}
	if held.storedEvaluator {
		return nil, gensurla.MakeConflict(errors.New("no test writer is used: this project has stored verification settings"))
	}
	if saved.RequestRevision != p.RequestRevision {
		return nil, gensurla.MakeConflict(fmt.Errorf(
			"the test assignment for %q is for request_revision %q, not %q",
			p.ChangeID, saved.RequestRevision, p.RequestRevision,
		))
	}
	saved.ReportNumber++
	saved.TestWriter = testWriterAssignment{
		State: testsReported,
		Read:  saved.TestWriter.Read,
	}
	if err := c.cards.saveOpenBuild(*saved); err != nil {
		return nil, recordBuildError(span, err, "save build")
	}
	select {
	case c.reportWake <- struct{}{}:
	default: // A wake is already waiting; the checker reads every reported build.
	}
	span.SetAttributes(attribute.String("surla.tests_state", saved.TestWriter.State), attribute.Int("surla.report_number", saved.ReportNumber))
	return testWriterAssignmentItem(held), nil
}

// changeBuild reads and renders record's usable build without changing it.
// The caller has authorized access to record. Git and run reads happen after
// the card lock is released.
func (c *ControlPlane) changeBuild(ctx context.Context, record storedChangeCard, saved buildFile, found bool) (*gensurla.ChangeBuild, error) {
	changeID := record.card.ChangeID
	if !found || saved.ChangeID != changeID || !c.usableBuild(saved) {
		return &gensurla.ChangeBuild{ChangeID: changeID, Build: gensurla.NewBuildNotStarted(&gensurla.BuildNotStarted{})}, nil
	}
	held := heldBuild{file: saved, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()}
	build, err := c.startedBuild(ctx, record.owner, held)
	if err != nil {
		return nil, err
	}
	return &gensurla.ChangeBuild{ChangeID: changeID, Build: gensurla.NewBuildStarted(build)}, nil
}

// buildInForce reports whether saved is a build of record's agreed revision
// (see storedChangeCard.agreedRevision); get_build and every assignment read
// report it as in_force.
func buildInForce(saved buildFile, record storedChangeCard) bool {
	return saved.RequestRevision == record.agreedRevision()
}

// startedBuild converts a held build into its transport form.
func startedBuild(held heldBuild, taking gensurla.BuildTaking) *gensurla.StartedBuild {
	required := 0
	for _, story := range held.file.Stories {
		for _, flow := range story.Flows {
			if flow.Complete {
				required++
			}
		}
	}
	return &gensurla.StartedBuild{
		RequestRevision:   held.file.RequestRevision,
		StartedAt:         held.file.StartedAt.UTC().Format(time.RFC3339Nano),
		InForce:           held.inForce,
		Builder:           builderProgress(held.file.Builder),
		TestWriter:        testWriterProgress(held),
		RequiredTestCount: required,
		Taking:            taking,
	}
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
	var refused *goa.ServiceError
	if !errors.As(err, &refused) || refused.Fault {
		span.RecordError(err)
		span.SetStatus(codes.Error, status)
	}
	return err
}

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
	if record.architecture == nil || record.goMod == "" || record.madeFrom == nil {
		return storedChangeCard{}, gensurla.MakeUnprocessable(errors.New("The agreed revision has no evaluated design snapshot. Ask the guide to refresh_story and revise_change with its op_id, adopt that revision, then build again."))
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
	return heldBuild{file: saved, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()}, nil
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
	if record.owner != owner || !c.usableBuild(saved) {
		return nil, nil
	}
	return []heldBuild{{file: saved, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()}}, nil
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

// readAssignment loads the build of the caller's change under the write lock,
// applies markRead, and saves the build when markRead reports a change, all
// within spanName.
func (c *ControlPlane) readAssignment(
	ctx context.Context,
	spanName, changeID string,
	markRead func(*buildFile) (bool, error),
) (heldBuild, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return heldBuild{}, errors.New("authenticated principal is required")
	}
	_, span := c.tracer("surla/controlplane").Start(ctx, spanName, trace.WithAttributes(attribute.String("surla.change_id", changeID)))
	defer span.End()
	c.mu.Lock()
	defer c.mu.Unlock()
	held, err := c.ownedBuild(principal, changeID)
	if err != nil {
		return heldBuild{}, recordBuildError(span, err, "load build")
	}
	span.SetAttributes(attribute.String("surla.request_revision", held.file.RequestRevision))
	changed, err := markRead(&held.file)
	if err != nil {
		return heldBuild{}, recordBuildError(span, err, "read assignment")
	}
	if changed {
		if err := c.cards.saveOpenBuild(held.file); err != nil {
			return heldBuild{}, recordBuildError(span, err, "save build")
		}
		span.AddEvent("assignment read for the first time")
	}
	return held, nil
}

// newestPassedBuild reads every run of this change and returns the newest pass
// accepted for this start's revision, including one accepted exactly at start.
// A later failed run never erases a pass. Missing run storage proves no pass.
func (c *ControlPlane) newestPassedBuild(owner string, saved buildFile) (*verifyrun.Run, error) {
	if c.runsDir == "" {
		return nil, nil
	}
	runs, err := verifyrun.List(c.runsDir, owner, saved.ChangeID)
	if err != nil {
		return nil, fmt.Errorf("read passed runs for the open build: %w", err)
	}
	for _, run := range runs {
		if run.Request.Plan.RequestRevision == saved.RequestRevision &&
			!run.Request.AcceptedAt.Before(saved.StartedAt) &&
			run.State.Kind == verifyrun.StateCompleted && run.State.Report.Passed {
			return &run, nil
		}
	}
	return nil, nil
}

// startedBuild renders current run and branch facts outside the card lock.
// A passed run is not attributed to branches that no longer hold its commits.
func (c *ControlPlane) startedBuild(ctx context.Context, owner string, held heldBuild) (*gensurla.StartedBuild, error) {
	run, err := c.newestPassedBuild(owner, held.file)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return startedBuild(held, gensurla.NewBuildTakingBuilding(&gensurla.BuildBuilding{})), nil
	}
	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	workspace, _, _ := c.currentWorkspaceTarget()
	repo, _, err := resolveStarterRepo(work, workspace)
	if err != nil {
		return nil, err
	}
	candidateBranch := testwriter.BuilderRole.Branch(held.file.Number)
	if holds, err := buildCommitInHistory(work, repo, run.Request.Plan.CandidateCommit, candidateBranch); err != nil {
		return nil, err
	} else if !holds {
		return nil, fmt.Errorf("passed candidate commit %s is no longer held by %s", run.Request.Plan.CandidateCommit, candidateBranch)
	}
	tests := gensurla.NewBuildPassedTestSourceEvaluator(&gensurla.OperatorEvaluatorTests{})
	if run.Request.Plan.Tests == verify.TestsWritten {
		branch := testwriter.TestsRole.Branch(held.file.Number)
		if holds, err := buildCommitInHistory(work, repo, run.Request.Plan.SuiteCommit, branch); err != nil {
			return nil, err
		} else if !holds {
			return nil, fmt.Errorf("passed tests commit %s is no longer held by %s", run.Request.Plan.SuiteCommit, branch)
		}
		tests = gensurla.NewBuildPassedTestSourceWrittenTests(&gensurla.BuildPassedTests{
			Commit: run.Request.Plan.SuiteCommit, Branch: branch,
		})
	}
	baseInBranch, err := buildCommitInHistory(work, repo, held.file.Base, "HEAD")
	if err != nil {
		return nil, err
	}
	return startedBuild(held, gensurla.NewBuildTakingPassed(&gensurla.BuildPassed{
		RunID: run.Request.RunID, CandidateCommit: run.Request.Plan.CandidateCommit,
		CandidateBranch: candidateBranch, Tests: tests, BaseInBranch: baseInBranch,
	})), nil
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
