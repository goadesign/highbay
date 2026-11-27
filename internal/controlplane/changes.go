// This file lists the owner's saved Change Cards and lets the views page wait
// until that list or the owner's displayed build, Story, team or target changes.
package controlplane

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/goadesign/highbay/internal/verifyrun"
)

const (
	// defaultChangesWait bounds one wait_for_changes call when nothing changes.
	// It stays below the project server's five-second ordered-shutdown grace,
	// so an open page never makes `highbay stop` wait for its request.
	defaultChangesWait = 4 * time.Second
	// defaultChangesPoll is how often a waiting call reads the list again.
	// Verification runs are written by separate processes, so only reading
	// the saved state sees their progress.
	defaultChangesPoll = time.Second
	// waitForChangesSpan names the waiting read's span. Its spans are
	// exported like every operation's but never saved in the diagnostics
	// history (see diagnosticTracer.Start).
	waitForChangesSpan = "highbay.controlplane.wait_for_changes"
)

// ListChanges returns the caller's saved Change Cards for this project, most
// recently saved first (tie-broken by change_id), with the state of each
// card's most recently accepted verification run, whether this server can
// record approvals, and a version that changes whenever any listed value
// changes, a build's get_build rendering changes, the owner's Story
// evaluation starts, finishes or is replaced, or the live target starts,
// becomes ready, accepts a draft, ends or is stopped. The model choice is not
// part of it. A card, build or run record that
// cannot be read fails the call rather than being left out of the list.
func (c *ControlPlane) ListChanges(ctx context.Context) (*genhighbay.ChangeList, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	ctx, span := c.tracer("highbay/controlplane").Start(ctx, "highbay.controlplane.list_changes")
	defer span.End()
	list, err := c.changeList(ctx, span, principal.ID)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.Int("highbay.change_count", len(list.Changes)))
	return list, nil
}

// WaitForChanges returns the version of the caller's current change list as
// soon as it differs from p.Since, or after a bounded wait with the unchanged
// version. It reads the saved list again every poll interval and ends early
// when the caller goes away; it never changes anything. The views page keeps
// one such call open, so its span is never saved in the diagnostics history,
// and the call returns the unchanged version as soon as the server begins to
// stop, so an open page never holds the server's shutdown.
func (c *ControlPlane) WaitForChanges(
	ctx context.Context,
	p *genhighbay.WaitForChangesPayload,
) (*genhighbay.ChangeListVersion, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	ctx, span := c.tracer("highbay/controlplane").Start(ctx, waitForChangesSpan)
	defer span.End()
	wait := cmp.Or(c.changesWait, defaultChangesWait)
	poll := cmp.Or(c.changesPoll, defaultChangesPoll)
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		list, err := c.changeList(ctx, span, principal.ID)
		if err != nil {
			return nil, err
		}
		if list.Version != p.Since {
			span.SetAttributes(attribute.Bool("highbay.changed", true))
			return &genhighbay.ChangeListVersion{Version: list.Version}, nil
		}
		select {
		case <-ctx.Done():
			// The page closed or paused its request; nobody reads a result.
			return nil, ctx.Err()
		case <-deadline.C:
			span.SetAttributes(attribute.Bool("highbay.changed", false))
			return &genhighbay.ChangeListVersion{Version: list.Version}, nil
		case <-c.stopping:
			span.SetAttributes(attribute.Bool("highbay.changed", false))
			return &genhighbay.ChangeListVersion{Version: list.Version}, nil
		case <-c.serverCtx.Done():
			span.SetAttributes(attribute.Bool("highbay.changed", false))
			return &genhighbay.ChangeListVersion{Version: list.Version}, nil
		case <-ticker.C:
		}
	}
}

// changeList reads owner's cards and canonical get_build renderings under one
// read lock, deriving each newest run's input match from the same build rendering
// and keeping only build digests for the version. It reads the newest verification
// runs in one pass before taking the lock, since separate processes write them.
// The version also covers approval availability, the owner's team and Story
// evaluation, and the live target.
// An unreadable record fails the list: span keeps the cause and the returned
// error names only the kind of record. A promotion target that cannot be
// resolved keeps the list working with approvals unavailable, as approve_merge
// refuses them; span records the cause.
func (c *ControlPlane) changeList(ctx context.Context, span trace.Span, owner string) (*genhighbay.ChangeList, error) {
	// Held for the whole read: an initialization cannot take the workspace
	// between this read's look at its phase and its reads of the workspace.
	c.initGate.RLock()
	defer c.initGate.RUnlock()
	var latest map[string]verifyrun.Run
	if runsDir := c.runsDir; runsDir != "" {
		var err error
		latest, err = verifyrun.LatestByChange(runsDir, owner)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "read verification runs")
			return nil, errors.New("list changes: saved verification records could not be read")
		}
	}
	c.mu.RLock()
	records, err := c.cards.all()
	if err != nil {
		c.mu.RUnlock()
		span.RecordError(err)
		span.SetStatus(codes.Error, "read change cards")
		return nil, errors.New("list changes: saved change cards could not be read")
	}
	saved, found, err := c.cards.loadOpenBuild()
	var (
		provenance    madeFromRecord
		madeFromFound bool
	)
	if err == nil {
		provenance, madeFromFound, err = c.cards.loadMadeFrom()
	}
	activeFound := found && !buildDropped(provenance, saved)
	c.mu.RUnlock()
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "read builds")
		return nil, errors.New("list changes: saved builds could not be read")
	}
	records = slices.DeleteFunc(records, func(r storedChangeCard) bool { return r.owner != owner })
	slices.SortFunc(records, func(a, b storedChangeCard) int {
		if byTime := b.savedAt.Compare(a.savedAt); byTime != 0 {
			return byTime
		}
		return strings.Compare(a.card.ChangeID, b.card.ChangeID)
	})
	buildVersions := make([]string, 0, len(records))
	items := make([]*genhighbay.ChangeListItem, 0, len(records))
	for _, record := range records {
		build, err := c.changeBuild(ctx, record, saved, found, draftedBeforeDrop(record, provenance))
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "read builds")
			return nil, errors.New("list changes: saved builds could not be read")
		}
		raw, err := genhighbay.EncodeChangeBuild(build)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "encode builds")
			return nil, errors.New("list changes: saved builds could not be encoded")
		}
		sum := sha256.Sum256(raw)
		buildVersions = append(buildVersions, hex.EncodeToString(sum[:]))
		item := &genhighbay.ChangeListItem{
			ChangeID:        record.card.ChangeID,
			Summary:         record.card.Summary,
			Status:          record.card.Status,
			RequestRevision: record.card.RequestRevision,
			SavedAt:         record.savedAt.UTC().Format(time.RFC3339Nano),
		}
		if run, ok := latest[record.card.ChangeID]; ok {
			item.LatestVerification = latestVerification(
				run, c.verificationInputsCurrent(record, saved, activeFound, run.Request.Plan),
			)
		}
		if record.proposed != nil {
			item.ProposedRevision = &record.proposed.card.RequestRevision
		}
		items = append(items, item)
	}
	// init_project holds the workspace for as long as it prepares the
	// starter, which takes minutes. The two reads below look at the
	// workspace and would wait for it, so the list would stop answering
	// just when the page shows the initialization's progress. No project
	// exists to approve or to evaluate a Story for until it ends. initGate,
	// held above, makes this look at the phase and those reads one step.
	initializing := c.initPhase.Load()
	approvalAvailable := false
	var (
		story      storyEvaluationVersion
		draftState *string
	)
	if initializing == nil {
		_, err = c.resolvePromotion(ctx)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "resolve promotion target")
		}
		// approve_merge refuses without verification, promotion target or not.
		_, noVerif := c.verifier.(noVerification)
		approvalAvailable = err == nil && !noVerif
		var storyCurrent bool
		story, storyCurrent = c.storyEvaluation(owner, provenance, madeFromFound)
		if len(items) == 0 {
			hasDraft := madeFromFound && c.draftOwner.Load() != nil && *c.draftOwner.Load() == owner
			initCompleted := c.initOwner.Load() != nil && *c.initOwner.Load() == owner
			storyState := storyRefreshStateNone
			if storyCurrent {
				storyState = story.State
			}
			draftState = firstChangeDraftState(storyState, hasDraft, initCompleted)
		}
	}
	// The team and its conversation are what the owner last observed, read
	// without calling herdr-core or Chorus, so a new message or a role's
	// changed state ends a wait the same way a saved card does.
	var team *genhighbay.TeamSnapshot
	var conversation *genhighbay.TeamConversation
	if c.team.Owner == owner {
		team, err = c.team.Service.ReadTeam(ctx)
		if err != nil {
			return nil, err
		}
		conversation, err = c.team.Service.ReadConversation(ctx)
		if err != nil {
			return nil, err
		}
	}
	raw, err := json.Marshal(struct {
		Changes           []*genhighbay.ChangeListItem `json:"changes"`
		Builds            []string                     `json:"builds"`
		ApprovalAvailable bool                         `json:"approval_available"`
		Team              *genhighbay.TeamSnapshot     `json:"team"`
		Conversation      *genhighbay.TeamConversation `json:"conversation"`
		StoryEvaluation   storyEvaluationVersion       `json:"story_evaluation"`
		LiveTarget        liveTargetVersion            `json:"live_target"`
		Initializing      *string                      `json:"initializing,omitempty"`
		DraftState        *string                      `json:"draft_state,omitempty"`
	}{items, buildVersions, approvalAvailable, team, conversation, story, c.liveTargetVersion(), initializing, draftState})
	if err != nil {
		err = fmt.Errorf("encode change list version: %w", err)
		span.RecordError(err)
		span.SetStatus(codes.Error, "encode change list")
		return nil, err
	}
	sum := sha256.Sum256(raw)
	return &genhighbay.ChangeList{
		Changes:           items,
		Version:           hex.EncodeToString(sum[:]),
		ApprovalAvailable: approvalAvailable,
		InitializingPhase: initializing,
		DraftState:        draftState,
	}, nil
}

// firstChangeDraftState reports the control plane's own progress toward a
// project's first Change Card while no card is saved and init_project is idle.
func firstChangeDraftState(storyState string, hasDraft, initCompleted bool) *string {
	var state string
	switch {
	case storyState == storyRefreshStateRunning:
		state = "evaluating_story"
	case storyState == storyRefreshStateSucceeded:
		state = "story_ready"
	case storyState == storyRefreshStateFailed:
		state = "story_failed"
	case hasDraft:
		state = "editing"
	case initCompleted:
		state = "initialized"
	default:
		return nil
	}
	return &state
}

// latestVerification reduces a run to the state the change list shows; a
// completed run reports its report's verdict. It relies on verifyrun
// reporting only the three kinds below: no encoder between here and the
// caller checks the state against the design's values.
func latestVerification(run verifyrun.Run, inputsCurrent bool) *genhighbay.LatestVerificationItem {
	var state string
	switch run.State.Kind {
	case verifyrun.StateRunning:
		state = "running"
	case verifyrun.StateCompleted:
		state = "failed"
		if run.State.Report.Passed {
			state = "passed"
		}
	case verifyrun.StateNotCompleted:
		state = "not_completed"
	}
	return &genhighbay.LatestVerificationItem{
		RunID: run.Request.RunID, State: state, InputsCurrent: inputsCurrent,
	}
}

// verificationInputsCurrent compares only the agreed revision and selected
// tests. Written tests use the build already read by the owning operation;
// stored evaluator inputs remain independent of build assignments.
// A matching run remains historical evidence of its recorded candidate; it
// does not establish the state of a working tree, deployment or approval.
func (c *ControlPlane) verificationInputsCurrent(
	record storedChangeCard,
	build buildFile,
	found bool,
	plan verify.Plan,
) bool {
	if agreed := record.agreedRevision(); agreed == "" || plan.RequestRevision != agreed {
		return false
	}
	switch v := c.verifier.(type) {
	case storedVerification:
		selected := v.cfg.Plan
		return plan.Tests == verify.TestsEvaluator &&
			plan.SuiteRepo == selected.SuiteRepo && plan.SuiteCommit == selected.SuiteCommit &&
			plan.SuiteModule == selected.SuiteModule && plan.SuiteCommand == selected.SuiteCommand
	case writtenTestVerification:
		return found && build.ChangeID == record.card.ChangeID && c.usableBuild(build) &&
			buildInForce(build, record) && plan.Tests == verify.TestsWritten && build.TestWriter.State == testsAccepted &&
			plan.SuiteCommit == build.TestWriter.Commit
	default:
		return false
	}
}
