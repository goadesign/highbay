// This file serves the user's evidence and approval observation for one saved run.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/goadesign/highbay/internal/verifyrun"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// ReviewVerification returns one run's immutable Story and current approval
// observation to the user. It shares approval preflight with ApproveMerge but
// never saves approval or changes build/assignment state. Agent reads keep their
// existing builder-safe contract; this result retains that same safe report.
func (c *ControlPlane) ReviewVerification(
	ctx context.Context, p *genhighbay.GetVerificationPayload,
) (*genhighbay.VerificationReviewResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok || principal.Kind != CallerUser {
		return nil, genhighbay.MakeForbidden(errors.New("only the user may review a verification for approval"))
	}
	ctx, span := c.tracer("highbay/controlplane").Start(ctx, "highbay.controlplane.review_verification",
		trace.WithAttributes(attribute.String("highbay.run_id", p.RunID)))
	defer span.End()
	req, state, inputsCurrent, err := c.readVerificationRun(principal, p.RunID, span)
	if err != nil {
		return nil, err
	}
	story, err := runStoryContext(req)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "read run Story context")
		return nil, errors.New("saved verification Story context could not be read")
	}
	approval, err := c.reviewRunApproval(ctx, principal, req, state)
	if err != nil {
		if expectedRequestRefusal(err) {
			approval = blockedRunApproval(err.Error())
		} else {
			span.RecordError(err)
			span.SetStatus(codes.Error, "observe approval")
			approval = blockedRunApproval("Approval requirements could not be read. Refresh this result before approving.")
		}
	}
	// A private binaries path is useful only with a configured promotion target.
	// A failed target read does not hide the saved report or its unverified record.
	var binaries *string
	if state.Kind == verifyrun.StateCompleted {
		visible, readErr := c.verificationBinariesVisible(ctx, principal)
		if readErr == nil {
			binaries, readErr = verificationBinariesDirectory(req, state, visible)
		}
		if readErr != nil {
			span.RecordError(readErr)
			span.SetStatus(codes.Error, "read verification binaries location")
		}
	}
	status := statusResult(req, state, inputsCurrent, binaries)
	return &genhighbay.VerificationReviewResult{
		RunID: status.RunID, ChangeID: status.ChangeID,
		RequestRevision: status.RequestRevision, CandidateCommit: status.CandidateCommit,
		SuiteCommit: status.SuiteCommit, AcceptedAt: status.AcceptedAt,
		InputsCurrent: status.InputsCurrent, CandidateBinariesDir: status.CandidateBinariesDir,
		TestSource: status.TestSource, State: status.State,
		Review: &genhighbay.VerificationReview{StoryContext: story, Approval: approval},
	}, nil
}

// runStoryContext renders only the accepted run's snapshot and owner's exact
// test mapping. It never reads today's card or infers operator-test coverage.
func runStoryContext(req verifyrun.Request) (genhighbay.StoryContext, error) {
	context := req.StoryContext
	if context.State == verifyrun.StoryNotRecorded {
		return genhighbay.NewStoryContextNotRecorded(&genhighbay.RunStoryNotRecorded{}), nil
	}
	recorded := &genhighbay.RecordedRunStory{
		BuildNumber:    context.BuildNumber,
		BuildStartedAt: context.BuildStartedAt.UTC().Format(time.RFC3339Nano),
		Stories:        storySummaryItems(context.Stories),
		TestCoverage:   genhighbay.NewTestCoverageNotRecorded(&genhighbay.TestCoverageNotRecorded{}),
	}
	if context.TestCoverage != nil {
		recorded.TestCoverage = genhighbay.NewTestCoverageRecorded((*genhighbay.RecordedTestCoverage)(context.TestCoverage))
	}
	if req.Plan.Tests == verify.TestsEvaluator {
		recorded.Mapping = genhighbay.NewMappingNotRecordedForEvaluator(&genhighbay.RunEvaluatorMappingUnknown{})
	} else {
		required, err := requiredStoryTests(context.Stories)
		if err != nil {
			return genhighbay.StoryContext{}, err
		}
		recorded.Mapping = genhighbay.NewMappingMapped(&genhighbay.RunStoryMapping{RequiredTests: required})
	}
	return genhighbay.NewStoryContextRecorded(recorded), nil
}

// reviewRunApproval preserves currentCard's saved-approval semantics and uses
// the same report checks and locked rechecks as the approval write for offers.
func (c *ControlPlane) reviewRunApproval(
	ctx context.Context, principal Principal, req verifyrun.Request, state verifyrun.State,
) (genhighbay.Approval, error) {
	if state.Kind != verifyrun.StateCompleted {
		return blockedRunApproval("This run has no completed passed report. Read its progress or failure before checking again."), nil
	}
	if !state.Report.Passed {
		return blockedRunApproval("This run did not pass. The builder corrects the reported failures and verifies again."), nil
	}
	c.mu.RLock()
	record, err := c.ownedCard(principal, req.ChangeID)
	c.mu.RUnlock()
	if err != nil {
		return genhighbay.Approval{}, err
	}
	card, err := c.currentCard(ctx, record)
	if err != nil {
		return genhighbay.Approval{}, err
	}
	if card.RequestRevision == req.Plan.RequestRevision {
		var saved *genhighbay.ApprovalItem
		if card.Approval != nil {
			saved = card.Approval
		} else if card.UnverifiedApproval != nil {
			saved = card.UnverifiedApproval.Approval
		}
		if saved != nil && saved.ReportDigest == state.Report.Digest &&
			saved.CandidateCommit == req.Plan.CandidateCommit && saved.SuiteCommit == req.Plan.SuiteCommit {
			// currentCard performs external reads; do not return a record for an
			// agreement or selected tests that changed during those reads.
			c.mu.RLock()
			now, readErr := c.ownedCard(principal, req.ChangeID)
			var tests string
			if readErr == nil {
				var selection testSelection
				selection, readErr = c.testsChoice(principal, req.ChangeID, req.Plan.RequestRevision)
				tests = selection.commit
			}
			unchanged := readErr == nil && now.card.RequestRevision == req.Plan.RequestRevision &&
				now.card.Approval != nil && now.card.Approval.ReportDigest == saved.ReportDigest &&
				tests == saved.SuiteCommit
			c.mu.RUnlock()
			if readErr != nil && !expectedRequestRefusal(readErr) {
				return genhighbay.Approval{}, readErr
			}
			if !unchanged {
				return blockedRunApproval("The agreement or tests changed while reading this approval. Refresh this result."), nil
			}
			if card.UnverifiedApproval != nil {
				return genhighbay.NewApprovalUnverified(card.UnverifiedApproval), nil
			}
			return genhighbay.NewApprovalRecorded(saved), nil
		}
	}
	payload := &genhighbay.ApproveMergePayload{
		ChangeID: req.ChangeID, RequestRevision: req.Plan.RequestRevision,
		CandidateCommit: req.Plan.CandidateCommit, ReportDigest: state.Report.Digest,
	}
	inputs, err := c.readApprovalInputs(ctx, principal, payload)
	if err == nil {
		report, loadErr := c.loadApprovalReport(ctx, inputs.target, payload.ReportDigest)
		err = loadErr
		var checked approvalChecks
		if err == nil {
			checked, err = c.checkApprovalReport(ctx, payload, inputs, report)
		}
		if err == nil {
			c.mu.RLock()
			_, _, err = c.recheckApproval(principal, payload, inputs, checked)
			c.mu.RUnlock()
		}
	}
	if err != nil {
		if expectedRequestRefusal(err) {
			return blockedRunApproval(fmt.Sprint(err)), nil
		}
		return genhighbay.Approval{}, err
	}
	return genhighbay.NewApprovalAvailable(&genhighbay.RunApprovalAvailable{}), nil
}

// blockedRunApproval centralizes the required owner reason for unavailable offers.
func blockedRunApproval(reason string) genhighbay.Approval {
	return genhighbay.NewApprovalBlocked(&genhighbay.RunApprovalBlocked{Reason: reason})
}
