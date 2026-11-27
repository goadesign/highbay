// This file implements Checkpoint 5 (CP5) promotion approval recording
// (`approve_merge`) and read-time verification of saved approvals against the
// bound PromotionTarget (`currentCard`).

package controlplane

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/testwriter"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/goadesign/highbay/internal/verifyrun"
)

// approvalInputs is the owned build/test selection before unlocked report work.
type approvalInputs struct {
	// target is the optional configured promotion owner.
	target PromotionTarget
	// build is the exact open build checked before external reads.
	build heldBuild
	// testsCommit and source are the selected tests, not caller choices.
	testsCommit string
	source      verify.TestsSource
}

// approvalChecks is the report/artifact observation to validate under the lock.
type approvalChecks struct {
	// artifacts are the staged artifacts saved on approval, empty without a target.
	artifacts map[string]string
	// candidateErr is deferred until the current build is rechecked.
	candidateErr error
}

// ApproveMerge records Checkpoint 5 approval of the caller's confirmed Change
// Card revision. Both test sources require the current owned open build. It
// chooses the tests' commit for the revision through
// testsChoice (returning no_build or tests_not_accepted when written tests are
// not ready), loads the verification report by digest (from the bound
// PromotionTarget when promotion settings are configured, or from the
// verifier's report store otherwise), requires the report to have run that
// tests' commit and verify.Approve to accept the report for the candidate
// commit, revision, tests' commit and (when a PromotionTarget is bound) the
// current binary digests in <release_dir>/<commit>/, and records the report's
// own SuiteID and Environment without comparing them against separate settings.
// When no PromotionTarget is bound, no release folder or staged binaries are
// required and Artifacts is recorded as empty.
// The approval is saved on a copy of the card only if the card still has the
// same request_revision and open build and testsChoice still returns the same tests' commit
// after those calls, so a confirm_intent or a new test acceptance meanwhile is
// never approved with the old report; a revise_change only proposes a revision
// and leaves the card's revision as it was. It returns the approved card with
// its derived TestSource and any proposal waiting beside it; nothing is merged
// or deployed.
func (c *ControlPlane) ApproveMerge(
	ctx context.Context,
	p *genhighbay.ApproveMergePayload,
) (*genhighbay.ChangeCardResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	inputs, err := c.readApprovalInputs(ctx, principal, p)
	if err != nil {
		return nil, err
	}
	// Report and artifact reads may be slow, so they run without holding c.mu.
	// A card is never removed and never changes owner; its request_revision,
	// confirmed status and chosen tests' commit are checked again under c.mu
	// before the approval is written.
	report, err := c.loadApprovalReport(ctx, inputs.target, p.ReportDigest)
	if err != nil {
		return nil, err
	}
	checked, err := c.checkApprovalReport(ctx, p, inputs, report)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	record, nowSource, err := c.recheckApproval(principal, p, inputs, checked)
	if err != nil {
		return nil, err
	}

	approved := *record.card
	approved.Approval = &genhighbay.ApprovalItem{
		ApprovedBy:      principal.ID,
		ReportDigest:    report.Digest,
		CandidateCommit: p.CandidateCommit,
		SuiteID:         report.Binding.SuiteID,
		SuiteCommit:     inputs.testsCommit,
		Environment:     report.Binding.Environment,
		Artifacts:       checked.artifacts,
		ScenarioCount:   report.Counts.Required,
		TestSource:      approvalTestSource(nowSource, inputs.testsCommit),
	}
	record.card = &approved
	if err := c.cards.save(record); err != nil {
		return nil, err
	}
	result := approved
	result.ProposedRevision, err = proposedRevisionItem(record)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// readApprovalInputs checks ownership and the agreed build/test selection once.
func (c *ControlPlane) readApprovalInputs(
	ctx context.Context, principal Principal, p *genhighbay.ApproveMergePayload,
) (approvalInputs, error) {
	target, err := c.resolvePromotion(ctx)
	if err != nil {
		return approvalInputs{}, err
	}

	c.mu.RLock()
	record, exists, err := c.cards.load(p.ChangeID)
	var (
		testsCommit string
		source      verify.TestsSource
		choiceErr   error
		build       heldBuild
	)
	if err == nil && exists && record.owner == principal.ID &&
		record.card.Status == StatusConfirmed && record.card.RequestRevision == p.RequestRevision {
		build, choiceErr = c.verificationBuild(principal, p.ChangeID, p.RequestRevision)
		if choiceErr == nil {
			var selection testSelection
			selection, choiceErr = c.testsChoice(principal, p.ChangeID, p.RequestRevision)
			testsCommit, source = selection.commit, selection.source
		}
	}
	c.mu.RUnlock()
	if err != nil {
		return approvalInputs{}, err
	}
	if !exists {
		return approvalInputs{}, genhighbay.MakeNotFound(fmt.Errorf("unknown change_id %q", p.ChangeID))
	}
	if record.owner != principal.ID {
		return approvalInputs{}, genhighbay.MakeForbidden(fmt.Errorf("principal %q is not the owner of change_id %q", principal.ID, p.ChangeID))
	}
	if record.card.Status != StatusConfirmed {
		return approvalInputs{}, genhighbay.MakeConflict(fmt.Errorf(
			"change_id %q must be confirmed before approval (status %q)",
			p.ChangeID,
			record.card.Status,
		))
	}
	if record.card.RequestRevision != p.RequestRevision {
		return approvalInputs{}, genhighbay.MakeConflict(fmt.Errorf(
			"request_revision mismatch for %q: got %q, want %q",
			p.ChangeID,
			p.RequestRevision,
			record.card.RequestRevision,
		))
	}
	var notAccepted testsNotAccepted
	if errors.As(choiceErr, &notAccepted) {
		// approve_merge's caller is the user, so the refusal names the builder
		// and never instructs an agent.
		return approvalInputs{}, refuseApprovalTestsNotAccepted(notAccepted.saved)
	}
	if choiceErr != nil {
		return approvalInputs{}, choiceErr
	}

	return approvalInputs{target: target, build: build, testsCommit: testsCommit, source: source}, nil
}

// loadApprovalReport reads through the promotion owner's current report store.
// Both the review offer and approval write use this lookup; a run's own saved
// report remains historical evidence and cannot substitute for this owner read.
func (c *ControlPlane) loadApprovalReport(
	ctx context.Context, target PromotionTarget, digest string,
) (verify.Report, error) {
	var report verify.Report
	var err error
	if target != nil {
		report, err = target.LoadReport(ctx, digest)
	} else {
		runs := verifierRuns(c.verifier)
		if runs == nil {
			return verify.Report{}, genhighbay.MakeNotImplemented(errVerificationUnavailable)
		}
		report, err = verifyrun.LoadReport(runs.Plan.ReportDir, digest)
	}
	if err != nil {
		return verify.Report{}, fmt.Errorf("load verification report %s: %w", digest, err)
	}
	return report, nil
}

// checkApprovalReport is the shared promotion and candidate preflight used by
// review_verification and approve_merge before their locked rechecks.
func (c *ControlPlane) checkApprovalReport(
	ctx context.Context, p *genhighbay.ApproveMergePayload, inputs approvalInputs, report verify.Report,
) (approvalChecks, error) {
	target, build, testsCommit, source := inputs.target, inputs.build, inputs.testsCommit, inputs.source

	if report.Digest != p.ReportDigest {
		return approvalChecks{}, fmt.Errorf("promotion target returned report %s for digest %s", report.Digest, p.ReportDigest)
	}
	if report.Binding.SuiteCommit != testsCommit {
		if source == verify.TestsEvaluator {
			return approvalChecks{}, genhighbay.MakeUnprocessable(fmt.Errorf(
				"the report ran the tests at %s; this project's stored verification settings name the suite at %s: the builder verifies the candidate again; then approve that run",
				report.Binding.SuiteCommit,
				testsCommit,
			))
		}
		return approvalChecks{}, genhighbay.MakeUnprocessable(fmt.Errorf(
			"the report ran the tests at %s; the tests accepted for revision %s are at %s: the builder verifies the candidate again; then approve that run",
			report.Binding.SuiteCommit,
			p.RequestRevision,
			testsCommit,
		))
	}
	expectedArtifacts := report.Binding.Artifacts
	recordedArtifacts := map[string]string{}
	if target != nil {
		artifacts, err := target.CurrentArtifacts(ctx, p.CandidateCommit)
		if err != nil {
			return approvalChecks{}, genhighbay.MakeUnprocessable(err)
		}
		expectedArtifacts = artifacts
		if cloned := maps.Clone(artifacts); cloned != nil {
			recordedArtifacts = cloned
		}
	}
	current := verify.Binding{
		CandidateCommit: p.CandidateCommit,
		RequestRevision: p.RequestRevision,
		SuiteID:         report.Binding.SuiteID,
		SuiteCommit:     testsCommit,
		Artifacts:       expectedArtifacts,
		Environment:     report.Binding.Environment,
	}
	if err := verify.Approve(current, report); err != nil {
		if errors.Is(err, verify.ErrStaleReport) {
			if target != nil && !maps.Equal(expectedArtifacts, report.Binding.Artifacts) {
				return approvalChecks{}, genhighbay.MakeUnprocessable(fmt.Errorf(
					"%w: publish the binaries the run built, whose folder the run's Proof view shows, into the release folder that the promotion settings name, in a folder named after the commit, or remove the promotion settings",
					err,
				))
			}
			return approvalChecks{}, genhighbay.MakeUnprocessable(err)
		}
		return approvalChecks{}, err
	}
	// A passed report names an immutable candidate, which must still belong
	// to this open build after all unlocked report and artifact reads.
	check, err := c.checkCandidateBuildCommit(ctx, c.currentWorkspaceDir(), build.file, report.Binding.CandidateCommit)
	if err != nil {
		return approvalChecks{}, err
	}
	candidateErr := approvalBuildRefusal(report.Binding.CandidateCommit, build.file.Base,
		testwriter.BuilderRole.Branch(build.file.Number), check)

	return approvalChecks{artifacts: recordedArtifacts, candidateErr: candidateErr}, nil
}

// recheckApproval rejects changes during external reads. The caller holds c.mu;
// approval writes keep that lock until persistence, while reviews only observe.
func (c *ControlPlane) recheckApproval(
	principal Principal, p *genhighbay.ApproveMergePayload, inputs approvalInputs, checked approvalChecks,
) (storedChangeCard, verify.TestsSource, error) {
	build, testsCommit, candidateErr := inputs.build, inputs.testsCommit, checked.candidateErr

	record, exists, err := c.cards.load(p.ChangeID)
	if err != nil {
		return storedChangeCard{}, "", err
	}
	if !exists {
		return storedChangeCard{}, "", genhighbay.MakeNotFound(fmt.Errorf("change card %q is no longer saved", p.ChangeID))
	}
	if record.card.Status != StatusConfirmed {
		return storedChangeCard{}, "", genhighbay.MakeConflict(fmt.Errorf(
			"change_id %q must be confirmed before approval (status %q)",
			p.ChangeID,
			record.card.Status,
		))
	}
	if record.card.RequestRevision != p.RequestRevision {
		return storedChangeCard{}, "", genhighbay.MakeConflict(fmt.Errorf(
			"request_revision mismatch for %q: got %q, want %q",
			p.ChangeID,
			p.RequestRevision,
			record.card.RequestRevision,
		))
	}
	nowBuild, err := c.verificationBuild(principal, p.ChangeID, p.RequestRevision)
	if err != nil {
		return storedChangeCard{}, "", err
	}
	if nowBuild.file.Number != build.file.Number ||
		!nowBuild.file.StartedAt.Equal(build.file.StartedAt) {
		return storedChangeCard{}, "", genhighbay.MakeConflict(errors.New("the open build changed while approving: verify the current build, then approve its run"))
	}
	// A drop can change the recorded branch tips while Git reads them. Its
	// current stop instruction wins over a semantic refusal of the old build.
	if candidateErr != nil {
		return storedChangeCard{}, "", candidateErr
	}
	selection, err := c.testsChoice(principal, p.ChangeID, p.RequestRevision)
	if err != nil || selection.commit != testsCommit {
		return storedChangeCard{}, "", genhighbay.MakeConflict(errors.New("the accepted tests changed while approving: approve again"))
	}
	return record, selection.source, nil
}

// currentCard returns record.card as get_change, an unchanged confirm_intent,
// revise_change and decline_revision report it:
//  1. When card has no saved approval, card is returned as-is (`Approval == nil`,
//     `UnverifiedApproval == nil`) without calling testsChoice or PromotionTarget.
//  2. When card has a saved approval and testsChoice refuses or returns a
//     tests' commit other than the approval's SuiteCommit, an event carrying
//     the change ID and the approval's SuiteCommit is recorded on the
//     `controlplane.check_approval` span and a shallow copy is returned with
//     `Approval == nil` and `UnverifiedApproval == nil` (the saved approval on
//     disk is untouched and is returned again if testsChoice returns its
//     SuiteCommit again).
//  3. When testsChoice returns the approval's SuiteCommit and no
//     PromotionTarget is bound, a shallow copy is returned with `Approval` set
//     (including its derived `TestSource`) and `UnverifiedApproval == nil`.
//  4. When testsChoice returns the approval's SuiteCommit and the bound
//     PromotionTarget reports current binary digests matching the approval's
//     Artifacts, a shallow copy is returned with `Approval` set (including its
//     derived `TestSource`) and `UnverifiedApproval == nil`.
//  5. When testsChoice returns the approval's SuiteCommit and PromotionTarget
//     reports current binary digests that differ, a shallow copy is returned
//     with `Approval == nil` and `UnverifiedApproval == nil`.
//  6. When testsChoice returns the approval's SuiteCommit and resolving the
//     PromotionTarget or calling PromotionTarget.CurrentArtifacts returns an
//     error, the raw error is recorded on the `controlplane.check_approval`
//     OpenTelemetry span and a shallow copy is returned with `Approval == nil`
//     and `UnverifiedApproval` carrying the recorded approval (with derived
//     `TestSource`) and a path-free recovery reason naming the views page's
//     controls.
//
// Callers must not hold c.mu because PromotionTarget.CurrentArtifacts may be slow.
func (c *ControlPlane) currentCard(
	ctx context.Context,
	record storedChangeCard,
) (*genhighbay.ChangeCardResult, error) {
	return c.currentCardWithPromotion(ctx, record, c.resolvePromotion)
}

// currentCardAt checks the same live approval using the workspace its caller
// already holds. It does not reacquire workspaceMu while a writer may wait.
func (c *ControlPlane) currentCardAt(
	ctx context.Context, record storedChangeCard, workspace string,
) (*genhighbay.ChangeCardResult, error) {
	return c.currentCardWithPromotion(ctx, record, func(ctx context.Context) (PromotionTarget, error) {
		return c.resolvePromotionAt(ctx, workspace)
	})
}

// currentCardWithPromotion evaluates the approval with the caller's workspace
// lifetime. Promotion I/O runs without holding c.mu.
func (c *ControlPlane) currentCardWithPromotion(
	ctx context.Context, record storedChangeCard, resolve func(context.Context) (PromotionTarget, error),
) (*genhighbay.ChangeCardResult, error) {
	card := record.card
	saved := card.Approval
	if saved == nil {
		return card, nil
	}
	ctx, span := c.tracer("github.com/goadesign/highbay/internal/controlplane").Start(
		ctx,
		"controlplane.check_approval",
		trace.WithAttributes(
			attribute.String("highbay.change_id", card.ChangeID),
			attribute.String("highbay.candidate_commit", saved.CandidateCommit),
		),
	)
	defer span.End()

	owner := Principal{ID: record.owner, CallerID: record.owner, Kind: CallerUser}
	c.mu.RLock()
	selection, choiceErr := c.testsChoice(owner, card.ChangeID, card.RequestRevision)
	testsCommit, source := selection.commit, selection.source
	c.mu.RUnlock()
	if choiceErr != nil || testsCommit != saved.SuiteCommit {
		span.AddEvent(
			"approval.hidden_tests_changed",
			trace.WithAttributes(
				attribute.String("highbay.change_id", card.ChangeID),
				attribute.String("highbay.suite_commit", saved.SuiteCommit),
			),
		)
		stale := *card
		stale.Approval = nil
		stale.UnverifiedApproval = nil
		return &stale, nil
	}
	a := *saved
	a.TestSource = approvalTestSource(source, saved.SuiteCommit)

	target, err := resolve(ctx)
	if err == nil && target == nil {
		withSource := *card
		withSource.Approval = &a
		return &withSource, nil
	}
	var artifacts map[string]string
	if err == nil {
		artifacts, err = target.CurrentArtifacts(ctx, a.CandidateCommit)
	}
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		unverified := *card
		unverified.Approval = nil
		unverified.UnverifiedApproval = &genhighbay.UnverifiedApprovalItem{
			Approval: &a,
			Reason: fmt.Sprintf(
				"Saved approval for candidate commit %s could not be verified because the release binaries could not be read. Publish the binaries the run built into the release folder that the promotion settings name, in a folder named after the commit, and press Check current approval; or remove the promotion settings with highbay promotion remove and restart Highbay; or press Approve this result on a newly verified run.",
				a.CandidateCommit,
			),
		}
		return &unverified, nil
	}
	if maps.Equal(artifacts, a.Artifacts) {
		withSource := *card
		withSource.Approval = &a
		return &withSource, nil
	}
	stale := *card
	stale.Approval = nil
	stale.UnverifiedApproval = nil
	return &stale, nil
}

// approvalTestSource builds an ApprovalItem's TestSource union from the
// tests' source and commit.
func approvalTestSource(source verify.TestsSource, commit string) genhighbay.ApprovalTestSource {
	if source == verify.TestsEvaluator {
		return genhighbay.NewApprovalTestSourceEvaluator(&genhighbay.OperatorEvaluatorTests{})
	}
	return genhighbay.NewApprovalTestSourceWrittenTests(&genhighbay.AgentWrittenTests{Commit: commit, Notice: writtenTestsNotice})
}

// resolvePromotion resolves a PromotionTarget dynamically from the currently
// bound workspace directory when ResolvePromotionTarget is set, or returns the
// static PromotionTarget.
func (c *ControlPlane) resolvePromotion(ctx context.Context) (PromotionTarget, error) {
	if c.resolvePromotionTarget != nil {
		return c.resolvePromotionAt(ctx, c.currentWorkspaceDir())
	}
	return c.promotionTarget, nil
}

// resolvePromotionAt resolves the configured target for a named workspace.
func (c *ControlPlane) resolvePromotionAt(ctx context.Context, workspace string) (PromotionTarget, error) {
	if c.resolvePromotionTarget != nil {
		return c.resolvePromotionTarget(ctx, workspace)
	}
	return c.promotionTarget, nil
}
