// This file owns worker copy turns, joins at assignment reads, and worker
// working-copy refusals across builder and test-writer operations.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/shquote"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/goadesign/surla/verify/candidate"
)

// builderReadyCopy holds the open build and resolved paths for a builder
// operation whose working copy (.surla-build) passed builderCopyReady.
type builderReadyCopy struct {
	// build is the open, undropped build for the caller's change.
	build heldBuild
	// repo is the project's git repository root.
	repo string
	// copyRoot is the builder copy root (<workspace>/.surla-build), which
	// check_project and update_dependencies use as their source-root grant.
	copyRoot string
	// folder is the builder's module folder inside copyRoot
	// (<workspace>/.surla-build/<prefix>).
	folder string
	// goWork is candidate.GoWork's selected file inside copyRoot, or "off".
	goWork string
}

// GetBuilderAssignment returns the builder's assignment for the caller's
// change, joins the builder's working copy (.surla-build) to the build's
// branch under the builder copy turn, and records that the builder read it.
func (c *ControlPlane) GetBuilderAssignment(
	ctx context.Context,
	p *gensurla.GetBuilderAssignmentPayload,
) (*gensurla.BuilderAssignment, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.workspaceMu.RLock()
	defer c.workspaceMu.RUnlock()
	_, span := c.tracer("surla/controlplane").Start(ctx, "surla.controlplane.get_builder_assignment", trace.WithAttributes(
		attribute.String("surla.change_id", p.ChangeID),
	))
	defer span.End()

	c.mu.RLock()
	owned, err := c.workerOwnedBuild(principal, p.ChangeID)
	c.mu.RUnlock()
	if err != nil {
		return nil, recordBuildError(span, err, "load build")
	}
	span.SetAttributes(attribute.String("surla.request_revision", owned.file.RequestRevision))

	select {
	case c.builderCopyTurn <- struct{}{}:
	default:
		return nil, recordBuildError(span, gensurla.MakeWorkingCopyBusy(errors.New(
			"Surla is starting a build in your working folder; call get_builder_assignment again.",
		)), "acquire builder copy turn")
	}
	defer func() { <-c.builderCopyTurn }()

	c.mu.RLock()
	owned, err = c.workerOwnedBuild(principal, p.ChangeID)
	c.mu.RUnlock()
	if err != nil {
		return nil, recordBuildError(span, err, "load build")
	}

	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	repo, folder, copyRoot, err := builderCopyFolder(work, c.activeWorkspaceDir)
	if err != nil {
		return nil, recordBuildError(span, err, "read builder folder")
	}
	wc, hasCopy, err := testwriter.OpenCopy(work, c.activeWorkspaceDir, testwriter.BuilderRole)
	if err != nil {
		return nil, recordBuildError(span, err, "open builder copy")
	}
	if !hasCopy && c.buildCopyFactory == nil {
		return nil, recordBuildError(span, gensurla.MakeWorkingFolderMissing(fmt.Errorf(
			"The builder's folder %s is missing: ask the user to build this change again on the views page.",
			shquote.Quote(folder),
		)), "open builder copy")
	}
	if hasCopy {
		outcome, facts, joinErr := wc.Join(
			work, testwriter.BuilderRole, owned.file.Number, owned.file.Base, owned.file.ChangeID,
		)
		if joinErr != nil {
			return nil, recordBuildError(span, joinErr, "join builder copy")
		}
		switch outcome {
		case testwriter.ChangedDuringJoin:
			return nil, recordBuildError(span, gensurla.MakeWorkingCopyBusy(fmt.Errorf(
				"Files in your working folder changed while Surla moved it to change %s; nothing was lost. Call get_builder_assignment again.",
				owned.file.ChangeID,
			)), "join builder copy")
		case testwriter.Refused:
			joined := copyBranchExists(work, repo, testwriter.BuilderRole.Branch(owned.file.Number))
			return nil, recordBuildError(span, copyOffBranchRefusal(
				testwriter.BuilderRole, owned.file.ChangeID, owned.file.Number, joined, facts, "get_builder_assignment",
			), "join builder copy")
		}
		if info, statErr := os.Stat(folder); errors.Is(statErr, fs.ErrNotExist) || (statErr == nil && !info.IsDir()) {
			return nil, recordBuildError(span, gensurla.MakeWorkingFolderMissing(fmt.Errorf(
				"The builder's folder %s is missing: ask the user to build this change again on the views page.",
				shquote.Quote(folder),
			)), "check builder folder")
		}
	}
	folder, goworkOff, err := builderWorkingFolder(folder, copyRoot)
	if err != nil {
		return nil, recordBuildError(span, err, "read builder folder")
	}

	c.mu.Lock()
	held, err := c.workerOwnedBuild(principal, p.ChangeID)
	if err != nil {
		c.mu.Unlock()
		return nil, recordBuildError(span, err, "load build")
	}
	if !held.file.StartedAt.Equal(owned.file.StartedAt) {
		c.mu.Unlock()
		return nil, recordBuildError(span, gensurla.MakeWorkingCopyBusy(errors.New(
			"Surla is starting a build in your working folder; call get_builder_assignment again.",
		)), "load build")
	}
	changed := !held.file.Builder.Read
	held.file.Builder.Read = true
	if changed {
		if err := c.cards.saveOpenBuild(held.file); err != nil {
			c.mu.Unlock()
			return nil, recordBuildError(span, err, "save build")
		}
		span.AddEvent("assignment read for the first time")
	}
	c.mu.Unlock()
	return builderAssignmentResult(held, folder, goworkOff), nil
}

// GetTestWriterAssignment returns the test writer's assignment for the
// caller's change in full, joins the test writer's working copy (.surla-tests)
// to the build's branch under the working-copy turn, records that the test
// writer read it, and on a marking read prepares the required tests' generated
// folder for the build's Story.
func (c *ControlPlane) GetTestWriterAssignment(
	ctx context.Context,
	p *gensurla.GetTestWriterAssignmentPayload,
) (*gensurla.TestWriterAssignment, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.mu.RLock()
	owned, err := c.workerOwnedBuild(principal, p.ChangeID)
	c.mu.RUnlock()
	if err != nil {
		return nil, err
	}
	if owned.file.Roles != buildRolesBuilderAndTestWriter {
		return nil, gensurla.MakeNotFound(errors.New("this build has no test-writer assignment because it was started with stored verification settings"))
	}
	if len(owned.file.Examples) == 0 {
		return nil, fmt.Errorf("the build of change %q holds none of the agreed answers, examples and preserved behaviors; "+
			"the user builds this change again on the views page", owned.file.ChangeID)
	}

	accepted, finish, err := c.admitBuildStart(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()

	accepted, span := c.tracer("surla/controlplane").Start(accepted, "surla.controlplane.get_test_writer_assignment", trace.WithAttributes(
		attribute.String("surla.change_id", p.ChangeID),
	))
	defer span.End()

	select {
	case c.workingCopyTurn <- struct{}{}:
	default:
		return nil, recordBuildError(span, gensurla.MakeWorkingCopyBusy(errors.New(
			"Surla is checking the tests in your working folder; call get_test_writer_assignment again when the check has ended.",
		)), "acquire working copy turn")
	}
	defer func() { <-c.workingCopyTurn }()

	work, cancel := c.buildStartDeadline(accepted)
	defer cancel()

	c.mu.RLock()
	owned, err = c.workerOwnedBuild(principal, p.ChangeID)
	c.mu.RUnlock()
	if err != nil {
		return nil, recordBuildError(span, err, "load build")
	}
	if owned.file.Roles != buildRolesBuilderAndTestWriter {
		return nil, recordBuildError(span, gensurla.MakeNotFound(errors.New(
			"this build has no test-writer assignment because it was started with stored verification settings",
		)), "read assignment")
	}
	if len(owned.file.Examples) == 0 {
		return nil, recordBuildError(span, fmt.Errorf(
			"the build of change %q holds none of the agreed answers, examples and preserved behaviors; "+
				"the user builds this change again on the views page", owned.file.ChangeID,
		), "read assignment")
	}
	span.SetAttributes(attribute.String("surla.request_revision", owned.file.RequestRevision))

	workspace, _, _ := c.currentWorkspaceTarget()
	readCtx, readCancel := context.WithTimeout(work, layoutReadTimeout)
	repo, prefix, err := resolveStarterRepo(readCtx, workspace)
	readCancel()
	if err != nil {
		return nil, recordBuildError(span, err, "read project repository")
	}
	folder := filepath.Join(workspace, testwriter.Folder, filepath.FromSlash(prefix))

	wc, hasCopy, err := testwriter.OpenCopy(work, workspace, testwriter.TestsRole)
	if err != nil {
		return nil, recordBuildError(span, err, "open test-writer copy")
	}
	if !hasCopy && c.buildCopyFactory == nil {
		return nil, recordBuildError(span, gensurla.MakeWorkingFolderMissing(fmt.Errorf(
			"The test writer's folder %s is missing: ask the user to build this change again on the views page.",
			shquote.Quote(folder),
		)), "open test-writer copy")
	}
	if hasCopy {
		outcome, facts, joinErr := wc.Join(
			work, testwriter.TestsRole, owned.file.Number, owned.file.Base, owned.file.ChangeID,
		)
		if joinErr != nil {
			return nil, recordBuildError(span, joinErr, "join test-writer copy")
		}
		switch outcome {
		case testwriter.ChangedDuringJoin:
			return nil, recordBuildError(span, gensurla.MakeWorkingCopyBusy(fmt.Errorf(
				"Files in your working folder changed while Surla moved it to change %s; nothing was lost. Call get_test_writer_assignment again.",
				owned.file.ChangeID,
			)), "join test-writer copy")
		case testwriter.Refused:
			joined := copyBranchExists(work, repo, testwriter.TestsRole.Branch(owned.file.Number))
			return nil, recordBuildError(span, copyOffBranchRefusal(
				testwriter.TestsRole, owned.file.ChangeID, owned.file.Number, joined, facts, "get_test_writer_assignment",
			), "join test-writer copy")
		}
		if info, statErr := os.Stat(folder); errors.Is(statErr, fs.ErrNotExist) || (statErr == nil && !info.IsDir()) {
			return nil, recordBuildError(span, gensurla.MakeWorkingFolderMissing(fmt.Errorf(
				"The test writer's folder %s is missing: ask the user to build this change again on the views page.",
				shquote.Quote(folder),
			)), "check test-writer folder")
		}
	}

	c.mu.Lock()
	held, err := c.workerOwnedBuild(principal, p.ChangeID)
	if err != nil {
		c.mu.Unlock()
		return nil, recordBuildError(span, err, "load build")
	}
	if !held.file.StartedAt.Equal(owned.file.StartedAt) {
		c.mu.Unlock()
		return nil, recordBuildError(span, gensurla.MakeWorkingCopyBusy(errors.New(
			"A new build started while Surla was moving your working copy; call get_test_writer_assignment again.",
		)), "load build")
	}
	if held.file.Roles != buildRolesBuilderAndTestWriter {
		c.mu.Unlock()
		return nil, recordBuildError(span, gensurla.MakeNotFound(errors.New(
			"this build has no test-writer assignment because it was started with stored verification settings",
		)), "read assignment")
	}
	if len(held.file.Examples) == 0 {
		c.mu.Unlock()
		return nil, recordBuildError(span, fmt.Errorf(
			"the build of change %q holds none of the agreed answers, examples and preserved behaviors; "+
				"the user builds this change again on the views page", held.file.ChangeID,
		), "read assignment")
	}
	marking := !held.file.TestWriter.Read
	shouldPrepare := marking && held.file.TestWriter.State == testsAssigned
	held.file.TestWriter.Read = true
	if marking {
		if err := c.cards.saveOpenBuild(held.file); err != nil {
			c.mu.Unlock()
			return nil, recordBuildError(span, err, "save build")
		}
		span.AddEvent("assignment read for the first time")
	}
	c.mu.Unlock()

	if shouldPrepare {
		_, layout, workspace, prepErr := c.projectTestLayout(work)
		var copy buildCopy
		if prepErr == nil {
			if c.buildCopyFactory != nil || !hasCopy {
				copy, prepErr = c.openBuildCopy(work, workspace, layout)
			} else {
				copy, prepErr = c.newWriterBuildCopy(wc, layout)
			}
		}
		if prepErr == nil {
			prepErr = work.Err()
		}
		if prepErr == nil {
			prepErr = copy.Prepare(work, testwriter.TestsRole.Branch(held.file.Number), held.file.Base, held.file.Stories)
		}
		if author := (*testwriter.AuthorError)(nil); prepErr != nil && !errors.As(prepErr, &author) {
			span.RecordError(prepErr)
			span.SetStatus(codes.Error, "prepare required tests")
		}
		var saveErr error
		held, saveErr = c.saveBuildPreparation(work, held.file, prepErr)
		if saveErr != nil {
			span.RecordError(errors.New("saved build preparation could not be recorded"))
			span.SetStatus(codes.Error, "record build preparation")
			return nil, saveErr
		}
	}

	required, err := requiredStoryTests(held.file.Stories)
	if err != nil {
		return nil, fmt.Errorf("name the required tests of change %q: %w", held.file.ChangeID, err)
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
// cleared. It then wakes the report checker without waiting for it.
func (c *ControlPlane) ReportTestsFinished(
	ctx context.Context,
	p *gensurla.ReportTestsFinishedPayload,
) (*gensurla.TestWriterAssignmentItem, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	c.workspaceMu.RLock()
	defer c.workspaceMu.RUnlock()
	_, span := c.tracer("surla/controlplane").Start(ctx, "surla.controlplane.report_tests_finished", trace.WithAttributes(
		attribute.String("surla.change_id", p.ChangeID),
		attribute.String("surla.request_revision", p.RequestRevision),
	))
	defer span.End()

	// reportMu first: a report waits while the report checker confirms,
	// records and saves a verdict, so it never falls between them.
	c.reportMu.Lock()
	defer c.reportMu.Unlock()

	c.mu.RLock()
	held, err := c.workerOwnedBuild(principal, p.ChangeID)
	c.mu.RUnlock()
	if err != nil {
		return nil, recordBuildError(span, err, "load build")
	}
	saved := held.file
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
	if !saved.TestWriter.Read && saved.ReportNumber == 0 && saved.TestWriter.State == testsAssigned {
		return nil, recordBuildError(span, gensurla.MakeAssignmentUnread(fmt.Errorf(
			"Change %s's build started after your last read: stop your current work, then call get_test_writer_assignment for %s.",
			saved.ChangeID, saved.ChangeID,
		)), "check assignment read")
	}

	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	repo, prefix, err := resolveStarterRepo(work, c.activeWorkspaceDir)
	if err != nil {
		return nil, recordBuildError(span, err, "read project repository")
	}
	folder := filepath.Join(c.activeWorkspaceDir, testwriter.Folder, filepath.FromSlash(prefix))
	wc, hasCopy, err := testwriter.OpenCopy(work, c.activeWorkspaceDir, testwriter.TestsRole)
	if err != nil {
		return nil, recordBuildError(span, err, "open test-writer copy")
	}
	if !hasCopy && c.buildCopyFactory == nil {
		return nil, recordBuildError(span, gensurla.MakeWorkingFolderMissing(fmt.Errorf(
			"The test writer's folder %s is missing: ask the user to build this change again on the views page.",
			shquote.Quote(folder),
		)), "open test-writer copy")
	}
	if hasCopy {
		ownBranch := testwriter.TestsRole.Branch(saved.Number)
		if !copyBranchExists(work, repo, ownBranch) {
			return nil, recordBuildError(span, gensurla.MakeAssignmentUnread(fmt.Errorf(
				"Change %s's build started after your last read: stop your current work, then call get_test_writer_assignment for %s.",
				saved.ChangeID, saved.ChangeID,
			)), "check assignment read")
		}
		if info, statErr := os.Stat(folder); errors.Is(statErr, fs.ErrNotExist) || (statErr == nil && !info.IsDir()) {
			return nil, recordBuildError(span, gensurla.MakeWorkingFolderMissing(fmt.Errorf(
				"The test writer's folder %s is missing: ask the user to build this change again on the views page.",
				shquote.Quote(folder),
			)), "check test-writer folder")
		}
		facts, factsErr := wc.Facts(work)
		if factsErr != nil {
			return nil, recordBuildError(span, factsErr, "read test-writer copy")
		}
		if facts.Detached || facts.Branch != ownBranch || facts.Operation != "none" || facts.IndexLock != "" {
			return nil, recordBuildError(span, copyOffBranchRefusal(
				testwriter.TestsRole, saved.ChangeID, saved.Number, true, facts, "report_tests_finished",
			), "check test-writer copy")
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	held, err = c.workerOwnedBuild(principal, p.ChangeID)
	if err != nil {
		return nil, recordBuildError(span, err, "load build")
	}
	if !held.file.StartedAt.Equal(saved.StartedAt) {
		return nil, recordBuildError(span, gensurla.MakeAssignmentUnread(fmt.Errorf(
			"Change %s's build started after your last read: stop your current work, then call get_test_writer_assignment for %s.",
			held.file.ChangeID, held.file.ChangeID,
		)), "check assignment read")
	}
	if held.file.Roles != buildRolesBuilderAndTestWriter {
		return nil, gensurla.MakeNotFound(errors.New("this build has no test-writer assignment because it was started with stored verification settings"))
	}
	if held.storedEvaluator {
		return nil, gensurla.MakeConflict(errors.New("no test writer is used: this project has stored verification settings"))
	}
	if held.file.RequestRevision != p.RequestRevision {
		return nil, gensurla.MakeConflict(fmt.Errorf(
			"the test assignment for %q is for request_revision %q, not %q",
			p.ChangeID, held.file.RequestRevision, p.RequestRevision,
		))
	}
	if !held.file.TestWriter.Read && held.file.ReportNumber == 0 && held.file.TestWriter.State == testsAssigned {
		return nil, recordBuildError(span, gensurla.MakeAssignmentUnread(fmt.Errorf(
			"Change %s's build started after your last read: stop your current work, then call get_test_writer_assignment for %s.",
			held.file.ChangeID, held.file.ChangeID,
		)), "check assignment read")
	}
	held.file.ReportNumber++
	held.file.TestWriter = testWriterAssignment{
		State: testsReported,
		Read:  held.file.TestWriter.Read,
	}
	if err := c.cards.saveOpenBuild(held.file); err != nil {
		return nil, recordBuildError(span, err, "save build")
	}
	select {
	case c.reportWake <- struct{}{}:
	default: // A wake is already waiting; the checker reads every reported build.
	}
	span.SetAttributes(attribute.String("surla.tests_state", held.file.TestWriter.State), attribute.Int("surla.report_number", held.file.ReportNumber))
	return testWriterAssignmentItem(held), nil
}

// workerOwnedBuild loads the build of the caller's change changeID for worker
// operations, failing with not_found when the change or its build does not
// exist and no_build when the open build was dropped. Callers hold c.mu.
func (c *ControlPlane) workerOwnedBuild(principal Principal, changeID string) (heldBuild, error) {
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
		return heldBuild{}, gensurla.MakeNoBuild(fmt.Errorf(
			"The user dropped change %s: stop work on it; make no more edits or commits for it. The user starts builds on the views page.",
			saved.ChangeID,
		))
	}
	return heldBuild{file: saved, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()}, nil
}

// builderCopyReady checks that an open, undropped build exists, that the
// builder has read its assignment since the build started, and that the
// builder's working copy (.surla-build) exists on its build branch with no
// in-progress Git operation or index lock.
//
// Callers take c.builderCopyTurn before calling builderCopyReady and hold it
// until their work with the copy finishes (a synchronous generation, or an
// asynchronous helper's completion and cleanup), pass the bound workspace (or
// hold workspaceMu.RLock), and hold no c.mu. The returned builderReadyCopy
// carries the open build, which the caller compares with the change it was
// asked about.
func (c *ControlPlane) builderCopyReady(
	ctx context.Context, workspace, operation string,
) (builderReadyCopy, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return builderReadyCopy{}, errors.New("authenticated principal is required")
	}
	if workspace == "" {
		workspace, _, _ = c.currentWorkspaceTarget()
	}

	c.mu.RLock()
	saved, found, err := c.cards.loadOpenBuild()
	if err != nil {
		c.mu.RUnlock()
		return builderReadyCopy{}, err
	}
	if !found || !c.usableBuild(saved) {
		c.mu.RUnlock()
		return builderReadyCopy{}, gensurla.MakeNoBuild(errors.New("No build is open: the user starts builds on the views page."))
	}
	record, cardFound, err := c.cards.load(saved.ChangeID)
	if err != nil {
		c.mu.RUnlock()
		return builderReadyCopy{}, err
	}
	if !cardFound {
		c.mu.RUnlock()
		return builderReadyCopy{}, gensurla.MakeNoBuild(errors.New("No build is open: the user starts builds on the views page."))
	}
	if record.owner != principal.ID {
		c.mu.RUnlock()
		return builderReadyCopy{}, gensurla.MakeForbidden(fmt.Errorf("principal %q is not the owner of change_id %q", principal.ID, saved.ChangeID))
	}
	provenance, hasProvenance, err := c.cards.loadMadeFrom()
	if err != nil {
		c.mu.RUnlock()
		return builderReadyCopy{}, err
	}
	if hasProvenance && buildDropped(provenance, saved) {
		c.mu.RUnlock()
		return builderReadyCopy{}, gensurla.MakeNoBuild(fmt.Errorf(
			"The user dropped change %s: stop work on it; make no more edits or commits for it. The user starts builds on the views page.",
			saved.ChangeID,
		))
	}
	held := heldBuild{file: saved, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()}
	c.mu.RUnlock()

	if !saved.Builder.Read {
		return builderReadyCopy{}, gensurla.MakeAssignmentUnread(fmt.Errorf(
			"Change %s's build started after your last read: stop your current work, then call get_builder_assignment for %s.",
			saved.ChangeID, saved.ChangeID,
		))
	}

	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	repo, folder, copyRoot, err := builderCopyFolder(work, workspace)
	if err != nil {
		return builderReadyCopy{}, err
	}
	wc, hasCopy, err := testwriter.OpenCopy(work, workspace, testwriter.BuilderRole)
	if err != nil {
		return builderReadyCopy{}, err
	}
	if !hasCopy && c.buildCopyFactory == nil {
		return builderReadyCopy{}, gensurla.MakeWorkingFolderMissing(fmt.Errorf(
			"The builder's folder %s is missing: ask the user to build this change again on the views page.",
			shquote.Quote(folder),
		))
	}
	if hasCopy {
		ownBranch := testwriter.BuilderRole.Branch(saved.Number)
		if !copyBranchExists(work, repo, ownBranch) {
			return builderReadyCopy{}, gensurla.MakeAssignmentUnread(fmt.Errorf(
				"Change %s's build started after your last read: stop your current work, then call get_builder_assignment for %s.",
				saved.ChangeID, saved.ChangeID,
			))
		}
		if info, statErr := os.Stat(folder); errors.Is(statErr, fs.ErrNotExist) || (statErr == nil && !info.IsDir()) {
			return builderReadyCopy{}, gensurla.MakeWorkingFolderMissing(fmt.Errorf(
				"The builder's folder %s is missing: ask the user to build this change again on the views page.",
				shquote.Quote(folder),
			))
		}
		facts, factsErr := wc.Facts(work)
		if factsErr != nil {
			return builderReadyCopy{}, factsErr
		}
		if facts.Detached || facts.Branch != ownBranch || facts.Operation != "none" || facts.IndexLock != "" {
			return builderReadyCopy{}, copyOffBranchRefusal(
				testwriter.BuilderRole, saved.ChangeID, saved.Number, true, facts, operation,
			)
		}
	}
	goWork, err := candidate.GoWork(copyRoot, folder)
	if err != nil {
		return builderReadyCopy{}, gensurla.MakeUnprocessable(fmt.Errorf("read builder Go workspace: %w", err))
	}
	return builderReadyCopy{
		build:    held,
		repo:     repo,
		copyRoot: copyRoot,
		folder:   folder,
		goWork:   goWork,
	}, nil
}

// copyBranchExists reports whether refs/heads/<branch> exists in repo.
func copyBranchExists(ctx context.Context, repo, branch string) bool {
	_, err := runGitRead(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false
	}
	return false
}

// copyOffBranchRefusal builds the structured CopyOffBranch error and its
// role-specific recovery prose for a worker copy that cannot join or is off its
// build branch, in the middle of a Git operation, or holding an index lock.
func copyOffBranchRefusal(
	role testwriter.Role,
	changeID string,
	number int,
	joined bool,
	facts testwriter.CopyFacts,
	operation string,
) *gensurla.CopyOffBranch {
	ownBranch := role.Branch(number)
	var branchPtr *string
	if !facts.Detached && facts.Branch != "" {
		b := facts.Branch
		branchPtr = &b
	}
	var buildBranchPtr *string
	if joined {
		b := ownBranch
		buildBranchPtr = &b
	}
	uncommitted := slices.Clone(facts.Uncommitted)
	if uncommitted == nil {
		uncommitted = []string{}
	}

	var parts []string
	switch {
	case !joined && facts.Detached:
		parts = append(parts, "Your working copy has a detached HEAD (not on any branch).")
	case !joined:
		parts = append(parts, fmt.Sprintf("Your working copy is on branch %s.", formatRefusalBranch(facts.Branch)))
	case facts.Detached:
		parts = append(parts, fmt.Sprintf("Your working copy has a detached HEAD (not on any branch); change %s is built on %s.", changeID, ownBranch))
	case facts.Branch != ownBranch:
		parts = append(parts, fmt.Sprintf("Your working copy is on branch %s; change %s is built on %s.", formatRefusalBranch(facts.Branch), changeID, ownBranch))
	default:
		parts = append(parts, fmt.Sprintf("Your working copy is on %s for change %s.", ownBranch, changeID))
	}

	if facts.Operation != "none" {
		opLabel := strings.ReplaceAll(facts.Operation, "_", "-")
		if role.Folder == testwriter.BuilderRole.Folder {
			parts = append(parts, fmt.Sprintf("A %s is in progress in your working copy: finish or abort it.", opLabel))
		} else {
			parts = append(parts, fmt.Sprintf("A %s is in progress in your working copy: abort it.", opLabel))
		}
	}
	if facts.IndexLock != "" {
		lockPath := shquote.Quote(facts.IndexLock)
		parts = append(parts, fmt.Sprintf(
			"Git's index lock (%s) exists in your working copy: wait for the running git command to finish, or remove %s if no git process is running.",
			lockPath, lockPath,
		))
	}

	switch {
	case facts.UncommittedCount == 0:
		parts = append(parts, "Uncommitted: none.")
	case facts.UncommittedCount <= len(uncommitted):
		parts = append(parts, fmt.Sprintf("Uncommitted (%d): %s.", facts.UncommittedCount, strings.Join(uncommitted, ", ")))
	default:
		parts = append(parts, fmt.Sprintf("Uncommitted (%d, first %d): %s.", facts.UncommittedCount, len(uncommitted), strings.Join(uncommitted, ", ")))
	}

	switch {
	case !joined && role.Folder == testwriter.BuilderRole.Folder:
		parts = append(parts, fmt.Sprintf(
			"Surla puts it on change %s's new branch %s only when nothing is uncommitted, or when it is on an earlier surla/build branch, where Surla commits what is left. Commit your work on a branch of your own, not one of the user's, then call %s again.",
			changeID, ownBranch, operation,
		))
	case !joined:
		parts = append(parts, fmt.Sprintf(
			"Surla puts it on change %s's new branch %s only when nothing is uncommitted, or when it is on an earlier surla/tests branch, where Surla commits what is left. Return your copy to an earlier surla/tests branch with your files, where Surla commits them, or remove them; then call %s again.",
			changeID, ownBranch, operation,
		))
	case role.Folder == testwriter.BuilderRole.Folder:
		parts = append(parts, fmt.Sprintf("Put your work on %s, commit it, and call %s again.", ownBranch, operation))
	default:
		parts = append(parts, fmt.Sprintf("Put your test files on %s, uncommitted (Surla commits what it accepts), and call %s again.", ownBranch, operation))
	}

	return &gensurla.CopyOffBranch{
		Name:             "copy_off_branch",
		Message:          strings.Join(parts, " "),
		Branch:           branchPtr,
		BuildBranch:      buildBranchPtr,
		Operation:        facts.Operation,
		Uncommitted:      uncommitted,
		UncommittedCount: facts.UncommittedCount,
	}
}

// formatRefusalBranch formats a branch name from CopyFacts for recovery prose.
func formatRefusalBranch(branch string) string {
	if branch == "a name that is not shown" {
		return branch
	}
	return shquote.Quote(branch)
}
