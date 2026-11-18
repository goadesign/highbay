// This file implements bound-workspace Story refresh operations: refresh_story
// reads the guide's draft and provenance snapshot, starts or reuses an
// in-memory evaluation per project owner and workspace identity,
// and get_story_refresh reads the current operation state. Every caller acts
// for the owner, so the user, the guide and the builder share one operation.
package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	goa "goa.design/goa/v3/pkg"
)

type (
	// refreshedSnapshot contains the immutable facts retained by a succeeded
	// operation. Revision producers save these facts without reading design files.
	refreshedSnapshot struct {
		// designSource is the canonical encoded design actually evaluated.
		designSource string
		// stories and architecture are the evaluation's domain and preview facts.
		stories      []designeval.StorySummary
		architecture *savedArchitecture
		// goMod is the evaluated draft's module text.
		goMod string
		// madeFrom is the provenance read alongside that exact evaluated draft.
		madeFrom *madeFromSnapshot
	}
	// storyRefreshKey identifies the owner's current Story refresh slot for a
	// verified bound workspace directory identity.
	storyRefreshKey struct {
		// owner is the project owner every caller acts for (Principal.ID).
		owner string
		// workspaceDir is the bound physical workspace path.
		workspaceDir string
		// workspaceDev is the device ID of workspaceDir.
		workspaceDev uint64
		// workspaceIno is the inode number of workspaceDir.
		workspaceIno uint64
	}

	// storyEvaluationVersion is what the change list's version covers of the
	// owner's current Story evaluation: enough to change whenever the
	// evaluation starts, finishes or is replaced, so a page waiting on the
	// list reads the evaluation again. All fields are empty when no
	// workspace is bound; State is storyRefreshStateNone when no evaluation
	// has run in this server process.
	storyEvaluationVersion struct {
		// OpID identifies the current operation; a replacement on the same
		// snapshot changes it even when its state and digest do not.
		OpID string `json:"op_id"`
		// State is the operation's state.
		State string `json:"state"`
		// SnapshotDigest identifies the evaluated workspace snapshot.
		SnapshotDigest string `json:"snapshot_digest"`
	}

	// storyRefreshOp holds the in-memory state of one Story refresh operation.
	// All reads and mutations after creation occur under ControlPlane.storyRefreshMu.
	storyRefreshOp struct {
		// id is the 32-character lowercase hex operation identifier.
		id string
		// state is storyRefreshStateRunning, storyRefreshStateSucceeded, or
		// storyRefreshStateFailed.
		state string
		// snapshotDigest is designeval.SnapshotDigest(files) for the evaluated snapshot.
		snapshotDigest string
		// stories holds the evaluated Story summaries when state is
		// storyRefreshStateSucceeded.
		stories []designeval.StorySummary
		// designSource is the canonical encoded design actually evaluated.
		designSource string
		// goMod is the evaluated draft module, retained for revision adoption.
		goMod string
		// architecture holds the successful evaluation's preview facts.
		architecture *savedArchitecture
		// listWrite identifies every persisted list change, including take recovery.
		listWrite uint64
		// madeFrom is the immutable list read alongside the evaluated draft.
		madeFrom *madeFromSnapshot
		// dropNote remains visible until a revision saves this generation.
		dropNote bool
		// diagnostic holds the safe failure or timeout message when state is
		// storyRefreshStateFailed.
		diagnostic string
		// cancel ends this operation's evaluation context when superseded.
		cancel context.CancelFunc
	}
)

const (
	// storyRefreshStateNone reports that no Story refresh has run yet for the
	// owner and bound workspace in this server process.
	storyRefreshStateNone = "none"
	// storyRefreshStateRunning reports that Story evaluation is in progress.
	storyRefreshStateRunning = "running"
	// storyRefreshStateSucceeded reports that Story evaluation completed.
	storyRefreshStateSucceeded = "succeeded"
	// storyRefreshStateFailed reports that Story evaluation failed or timed out.
	storyRefreshStateFailed = "failed"
	// defaultStoryRefreshTimeout bounds one Story refresh operation in the
	// control plane, sized for a cold module download and compilation.
	defaultStoryRefreshTimeout = 5 * time.Minute
	// completeSourcesDiagnosticTrailer is the generate_code-only guidance line
	// appended by designeval.GenerationDiagnostic on build failures.
	completeSourcesDiagnosticTrailer = "\nIf the compiler reports that generator.CompleteSources is undefined,"
)

// RefreshStory reads the guide draft and provenance together, then starts
// or reuses the owner's evaluation without holding the draft lock.
func (c *ControlPlane) RefreshStory(ctx context.Context) (*gensurla.StoryRefreshResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	wsDir, wsDev, wsIno := c.currentWorkspaceTarget()
	files, madeFrom, err := c.readDraftSnapshot(ctx, wsDir, wsDev, wsIno)
	if err != nil {
		return nil, err
	}
	digest := designeval.SnapshotDigest(files)
	designSource, err := designSourceFromModuleSnapshot(files)
	if err != nil {
		return nil, err
	}

	c.workspaceMu.RLock()
	if c.serverCtx.Err() != nil {
		c.workspaceMu.RUnlock()
		return nil, gensurla.MakeUnavailable(errors.New(
			"the control plane is shutting down and is no longer starting Story refresh operations",
		))
	}
	if c.activeWorkspaceDir != wsDir || c.boundWorkspaceDev != wsDev || c.boundWorkspaceIno != wsIno {
		c.workspaceMu.RUnlock()
		return nil, gensurla.MakeUnprocessable(errors.New(
			"bound workspace changed while reading the module snapshot; call refresh_story again",
		))
	}

	key := storyRefreshKey{
		owner:        principal.ID,
		workspaceDir: wsDir,
		workspaceDev: wsDev,
		workspaceIno: wsIno,
	}

	// Keep the saved-revision check and operation installation in the same
	// card-read interval. A concurrent save clears an already-installed note.
	c.mu.RLock()
	showDropNote, err := c.draftDropNote(madeFrom.Start)
	if err != nil {
		c.mu.RUnlock()
		c.workspaceMu.RUnlock()
		return nil, err
	}
	c.storyRefreshMu.Lock()
	current := c.storyRefreshes[key]
	if current != nil && current.matchesDraft(digest, madeFrom) &&
		(current.state == storyRefreshStateRunning || current.state == storyRefreshStateSucceeded) {
		res := storyRefreshResultFromOp(current, wsDir)
		c.storyRefreshMu.Unlock()
		c.mu.RUnlock()
		c.workspaceMu.RUnlock()
		return res, nil
	}

	opID, err := newStoryRefreshOpID()
	if err != nil {
		c.storyRefreshMu.Unlock()
		c.mu.RUnlock()
		c.workspaceMu.RUnlock()
		return nil, err
	}
	evalCtx, cancel := c.storyRefreshDeadline(c.serverCtx)
	op := &storyRefreshOp{
		id:             opID,
		state:          storyRefreshStateRunning,
		snapshotDigest: digest,
		cancel:         cancel,
		designSource:   designSource,
		goMod:          files["go.mod"],
		madeFrom:       &madeFromSnapshot{Start: madeFrom.Start, Entries: madeFrom.Entries},
		listWrite:      madeFrom.Write,
		dropNote:       showDropNote,
	}
	if c.storyRefreshes == nil {
		c.storyRefreshes = make(map[storyRefreshKey]*storyRefreshOp)
	}
	c.storyRefreshes[key] = op
	var prevCancel context.CancelFunc
	if current != nil {
		prevCancel = current.cancel
	}
	res := storyRefreshResultFromOp(op, wsDir)
	c.storyRefreshOps.Add(1)
	c.storyRefreshMu.Unlock()
	c.mu.RUnlock()
	c.workspaceMu.RUnlock()

	if prevCancel != nil {
		prevCancel()
	}
	go c.runStoryRefresh(evalCtx, cancel, defaultStoryRefreshTimeout, key, op, files)
	return res, nil
}

// GetStoryRefresh returns the owner's current in-memory Story refresh
// operation for the bound workspace, or state "none" when none has run.
func (c *ControlPlane) GetStoryRefresh(ctx context.Context) (*gensurla.StoryRefreshResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	wsDir, wsDev, wsIno := c.currentWorkspaceTarget()
	key := storyRefreshKey{
		owner:        principal.ID,
		workspaceDir: wsDir,
		workspaceDev: wsDev,
		workspaceIno: wsIno,
	}
	c.storyRefreshMu.Lock()
	op := c.storyRefreshes[key]
	res := storyRefreshResultFromOp(op, wsDir)
	c.storyRefreshMu.Unlock()
	return res, nil
}

// storyEvaluation returns what the change list's version covers of owner's
// current Story evaluation for the bound workspace. It reads the same slot
// GetStoryRefresh reads, without starting anything.
func (c *ControlPlane) storyEvaluation(owner string) storyEvaluationVersion {
	wsDir, wsDev, wsIno := c.currentWorkspaceTarget()
	key := storyRefreshKey{owner: owner, workspaceDir: wsDir, workspaceDev: wsDev, workspaceIno: wsIno}
	c.storyRefreshMu.Lock()
	defer c.storyRefreshMu.Unlock()
	op := c.storyRefreshes[key]
	if op == nil {
		return storyEvaluationVersion{State: storyRefreshStateNone}
	}
	return storyEvaluationVersion{OpID: op.id, State: op.state, SnapshotDigest: op.snapshotDigest}
}

// runStoryRefresh executes one design evaluation for op on a
// server-owned context and publishes its result only if the server is still
// running and op is still the current operation for key.
func (c *ControlPlane) runStoryRefresh(
	evalCtx context.Context,
	cancel context.CancelFunc,
	timeout time.Duration,
	key storyRefreshKey,
	op *storyRefreshOp,
	files map[string]string,
) {
	defer c.storyRefreshOps.Done()
	defer cancel()

	res, err := c.evaluator.Evaluate(evalCtx, files)
	timedOut := errors.Is(context.Cause(evalCtx), context.DeadlineExceeded) ||
		errors.Is(err, context.DeadlineExceeded)

	c.storyRefreshMu.Lock()
	defer c.storyRefreshMu.Unlock()
	if c.serverCtx.Err() != nil || c.storyRefreshes[key] != op {
		return
	}
	if err == nil && evalCtx.Err() != nil {
		err = context.Cause(evalCtx)
		timedOut = timedOut || errors.Is(err, context.DeadlineExceeded)
	}
	op.cancel = nil
	if err != nil {
		op.state = storyRefreshStateFailed
		op.stories = nil
		op.diagnostic = storyRefreshDiagnostic(err, timeout, timedOut)
		return
	}
	op.state = storyRefreshStateSucceeded
	op.stories = cloneStorySummaries(res.Stories)
	op.architecture = &savedArchitecture{
		DesignDigest: op.snapshotDigest, SystemName: res.SystemName,
		Containers: res.Containers, Edges: res.Edges, AgentPolicies: res.AgentPolicies, Tools: res.Tools,
	}
	op.diagnostic = ""
}

// closeStoryRefreshes waits for all admitted Story refresh evaluations to
// finish after c.cancelServer() and removes any interrupted running entries so
// shutdown never leaves an operation running or publishes after Close.
func (c *ControlPlane) closeStoryRefreshes() {
	c.storyRefreshOps.Wait()
	c.storyRefreshMu.Lock()
	for k, op := range c.storyRefreshes {
		if op.state == storyRefreshStateRunning {
			delete(c.storyRefreshes, k)
		}
	}
	c.storyRefreshMu.Unlock()
}

// adoptStoryRefresh selects the owner's succeeded operation for the bound
// workspace identity. It reads no file: later draft/project edits cannot change
// the snapshot the guide selected. The producer checks Drop again under draftMu
// at the actual card-save boundary.
func (c *ControlPlane) adoptStoryRefresh(
	principal Principal,
	rawOpID string,
) (refreshedSnapshot, error) {
	opID := strings.TrimSpace(rawOpID)
	if opID == "" {
		return refreshedSnapshot{}, gensurla.MakeUnprocessable(errors.New(
			"story_refresh_op_id must name a succeeded refresh_story operation; call refresh_story and pass its op_id",
		))
	}
	wsDir, wsDev, wsIno := c.currentWorkspaceTarget()
	c.workspaceMu.RLock()
	defer c.workspaceMu.RUnlock()
	key := storyRefreshKey{
		owner:        principal.ID,
		workspaceDir: wsDir,
		workspaceDev: wsDev,
		workspaceIno: wsIno,
	}
	c.storyRefreshMu.Lock()
	defer c.storyRefreshMu.Unlock()
	op := c.storyRefreshes[key]
	if c.activeWorkspaceDir != wsDir || c.boundWorkspaceDev != wsDev ||
		c.boundWorkspaceIno != wsIno || op == nil || op.id != opID {
		return refreshedSnapshot{}, gensurla.MakeUnprocessable(fmt.Errorf(
			"story_refresh_op_id %q is not the current Story refresh operation for this project and workspace; call refresh_story and pass the latest succeeded op_id",
			opID,
		))
	}
	if op.state != storyRefreshStateSucceeded {
		return refreshedSnapshot{}, gensurla.MakeUnprocessable(fmt.Errorf(
			"story_refresh_op_id %q has state %q, not succeeded; wait for get_story_refresh to report succeeded or call refresh_story again",
			opID, op.state,
		))
	}
	return refreshedSnapshot{
		designSource: op.designSource, stories: cloneStorySummaries(op.stories),
		architecture: op.architecture, goMod: op.goMod, madeFrom: cloneMadeFromSnapshot(op.madeFrom),
	}, nil
}

// requireRefreshForModule keeps pre-initialization intent available but never
// invents a snapshot for an initialized module. Only go.mod's existence is read.
func (c *ControlPlane) requireRefreshForModule() error {
	wsDir, wsDev, wsIno := c.currentWorkspaceTarget()
	_, root, err := openModuleRootWithIdentity(wsDir, wsDev, wsIno)
	if err != nil {
		return err
	}
	_, statErr := root.Lstat("go.mod")
	if err := root.Close(); err != nil {
		return err
	}
	if errors.Is(statErr, fs.ErrNotExist) {
		return nil
	}
	if statErr != nil {
		return gensurla.MakeUnprocessable(statErr)
	}
	return gensurla.MakeUnprocessable(errors.New(
		"story_refresh_op_id is required for a Go module; call refresh_story, wait for succeeded, and pass its op_id",
	))
}

// cloneMadeFromSnapshot lets a revision own its history independently of the
// retained operation and later list writes. Older cards may have no snapshot.
func cloneMadeFromSnapshot(saved *madeFromSnapshot) *madeFromSnapshot {
	if saved == nil {
		return nil
	}
	cloned := &madeFromSnapshot{Start: saved.Start, Entries: maps.Clone(saved.Entries)}
	cloned.Start.FolderVersions = maps.Clone(saved.Start.FolderVersions)
	for file, versions := range cloned.Entries {
		cloned.Entries[file] = slices.Clone(versions)
	}
	if saved.Start.Dropped != nil {
		dropped := *saved.Start.Dropped
		dropped.BranchTips = maps.Clone(dropped.BranchTips)
		cloned.Start.Dropped = &dropped
	}
	return cloned
}

// recordRefreshedSnapshot checks Drop at the card-save boundary and enters only
// this evaluated design's versions. The live list retains intervening work; the
// card keeps the operation's history plus its own saved versions at this write.
// draftMu and c.mu are held, in that order. Blobs and list precede the card.
func (c *ControlPlane) recordRefreshedSnapshot(
	span trace.Span, snapshot refreshedSnapshot,
) (err error) {
	defer func() {
		if err == nil {
			return
		}
		var refusal *goa.ServiceError
		if errors.As(err, &refusal) {
			span.SetAttributes(attribute.String("surla.public_summary",
				"The selected Story refresh cannot be saved; call refresh_story and retry with its succeeded op_id."))
			return
		}
		span.SetAttributes(attribute.String("surla.public_summary", "Could not record the evaluated design snapshot."))
		span.RecordError(err)
		span.SetStatus(codes.Error, "record evaluated design snapshot")
	}()
	current, exists, err := c.cards.loadMadeFrom()
	if err != nil {
		return err
	}
	if !exists {
		return gensurla.MakeUnprocessable(errors.New("Story refresh provenance is missing; call refresh_story again"))
	}
	if snapshot.madeFrom.Start.Write < current.Start.Write && current.Start.Dropped != nil {
		return gensurla.MakeDraftedBeforeDrop(fmt.Errorf(
			"the user dropped change %q after that Story refresh; call refresh_story to remake and evaluate the draft",
			current.Start.Dropped.ChangeID,
		))
	}
	if snapshot.madeFrom.Start.Write != current.Start.Write {
		return gensurla.MakeUnprocessable(errors.New("Story refresh provenance no longer matches the draft start; call refresh_story again"))
	}
	design, err := decodeDesignSource(snapshot.designSource)
	if err != nil {
		return err
	}
	paths := map[string]bool{}
	for file := range snapshot.madeFrom.Entries {
		paths[file] = true
	}
	for file := range design {
		paths[file] = true
	}
	current.Write++
	if current.Entries == nil {
		current.Entries = map[string][]madeFromVersion{}
	}
	if snapshot.madeFrom.Entries == nil {
		snapshot.madeFrom.Entries = map[string][]madeFromVersion{}
	}
	for _, file := range slices.Sorted(maps.Keys(paths)) {
		version, err := c.enterDraftVersion(&current, file, design)
		if err != nil {
			return err
		}
		// A later unrelated version may be in current, but never enters this
		// frozen card's history. Re-entry moves only this design's own version.
		versions := slices.DeleteFunc(snapshot.madeFrom.Entries[file], func(v madeFromVersion) bool {
			return v.Version == version
		})
		snapshot.madeFrom.Entries[file] = append(versions, madeFromVersion{Version: version, Entered: current.Write})
	}
	return c.cards.saveMadeFrom(current)
}

// draftDropNote checks once when a refresh is installed, so restarting the
// server does not restore a note already resolved by a saved revision. c.mu is
// held before storyRefreshMu, as it is when the change list reads evaluation.
func (c *ControlPlane) draftDropNote(start madeFromStart) (bool, error) {
	if start.Dropped == nil {
		return false, nil
	}
	records, err := c.cards.all()
	if err != nil {
		return false, err
	}
	for _, record := range records {
		if record.madeFrom != nil && record.madeFrom.Start.Write == start.Write {
			return false, nil
		}
		if record.proposed != nil && record.proposed.madeFrom != nil && record.proposed.madeFrom.Start.Write == start.Write {
			return false, nil
		}
	}
	return true, nil
}

// suppressDraftDropNote runs after a card/proposal save while c.mu is held.
// It changes presentation only; the operation's immutable provenance survives.
func (c *ControlPlane) suppressDraftDropNote(saved *madeFromSnapshot) {
	if saved == nil {
		return
	}
	c.storyRefreshMu.Lock()
	defer c.storyRefreshMu.Unlock()
	for _, op := range c.storyRefreshes {
		if op.madeFrom != nil && op.madeFrom.Start.Write == saved.Start.Write {
			op.dropNote = false
		}
	}
}

// matchesDraft compares both input owners; a take/recovery list write invalidates
// reuse even if it left the canonical draft and immutable history unchanged.
// storyRefreshMu protects op while callers compare it.
func (op *storyRefreshOp) matchesDraft(digest string, list madeFromRecord) bool {
	return op.snapshotDigest == digest && op.listWrite == list.Write &&
		reflect.DeepEqual(op.madeFrom, &madeFromSnapshot{Start: list.Start, Entries: list.Entries})
}

// storyRefreshDeadline bounds one Story refresh evaluation.
func (c *ControlPlane) storyRefreshDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	withTimeout := c.storyRefreshWithTimeout
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	return withTimeout(ctx, defaultStoryRefreshTimeout)
}

// storyRefreshResultFromOp projects op (or nil when no refresh has run) into a
// fresh gensurla.StoryRefreshResult. ControlPlane.storyRefreshMu must be held.
func storyRefreshResultFromOp(op *storyRefreshOp, folder string) *gensurla.StoryRefreshResult {
	folder = filepath.Join(folder, draftFolderName)
	if op == nil {
		return &gensurla.StoryRefreshResult{
			State:       storyRefreshStateNone,
			DraftFolder: folder,
			Stories:     []*gensurla.StorySummaryItem{},
			Message:     "No Story refresh has run for this workspace yet; call refresh_story to evaluate the draft design.",
		}
	}
	id := op.id
	digest := op.snapshotDigest
	res := &gensurla.StoryRefreshResult{
		OpID:           &id,
		DraftFolder:    folder,
		State:          op.state,
		SnapshotDigest: &digest,
		Stories:        []*gensurla.StorySummaryItem{},
	}
	switch op.state {
	case storyRefreshStateRunning:
		res.Message = fmt.Sprintf(
			"Story refresh %s is running for workspace snapshot %s; call get_story_refresh to check its result.",
			op.id, op.snapshotDigest,
		)
	case storyRefreshStateSucceeded:
		res.Stories = storySummaryItems(op.stories)
		if len(res.Stories) == 0 {
			res.Message = fmt.Sprintf(
				"Story refresh %s succeeded for workspace snapshot %s: the design declares no Stories.",
				op.id, op.snapshotDigest,
			)
		} else {
			res.Message = fmt.Sprintf(
				"Story refresh %s succeeded for workspace snapshot %s: %d Story declaration(s).",
				op.id, op.snapshotDigest, len(res.Stories),
			)
		}
	case storyRefreshStateFailed:
		diag := op.diagnostic
		res.Diagnostic = &diag
		res.Message = fmt.Sprintf(
			"Story refresh %s failed for workspace snapshot %s.",
			op.id, op.snapshotDigest,
		)
	}
	if op.dropNote {
		res.Message += fmt.Sprintf(" Surla made this draft again from the project folder when the user dropped change %s; edits made before then are gone.", op.madeFrom.Start.Dropped.ChangeID)
	}
	return res
}

// storyRefreshDiagnostic formats a caller-safe diagnostic from err with private
// filesystem paths replaced and operator proxy credentials withheld.
func storyRefreshDiagnostic(err error, timeout time.Duration, timedOut bool) string {
	diag := designeval.GenerationDiagnostic(err)
	if idx := strings.Index(diag, completeSourcesDiagnosticTrailer); idx >= 0 {
		diag = diag[:idx]
	}
	if timedOut {
		if rest, ok := strings.CutPrefix(diag, "Code generation failed while "); ok {
			return fmt.Sprintf("Story refresh timed out after %s while %s", timeout, rest)
		}
		return fmt.Sprintf("Story refresh timed out after %s.", timeout)
	}
	if rest, ok := strings.CutPrefix(diag, "Code generation failed while "); ok {
		return "Story evaluation failed while " + rest
	}
	return diag
}

// cloneStorySummaries returns a deep copy of stories, normalizing empty slices to nil.
func cloneStorySummaries(stories []designeval.StorySummary) []designeval.StorySummary {
	if len(stories) == 0 {
		return nil
	}
	out := make([]designeval.StorySummary, len(stories))
	for i, s := range stories {
		var actors []designeval.StoryActor
		if len(s.Actors) > 0 {
			actors = slices.Clone(s.Actors)
		}
		out[i] = designeval.StorySummary{
			Name:        s.Name,
			Description: s.Description,
			Actors:      actors,
		}
		if len(s.Flows) > 0 {
			flows := make([]designeval.StoryFlow, len(s.Flows))
			for j, f := range s.Flows {
				flows[j] = designeval.StoryFlow{
					Key:         f.Key,
					Title:       f.Title,
					Description: f.Description,
					Complete:    f.Complete,
					Questions:   cloneStoryQuestions(f.Questions),
				}
				if len(f.Steps) > 0 {
					steps := make([]designeval.StoryStep, len(f.Steps))
					for k, st := range f.Steps {
						steps[k] = designeval.StoryStep{
							Key:         st.Key,
							ActorKey:    st.ActorKey,
							Summary:     st.Summary,
							Description: st.Description,
							Kind:        st.Kind,
							Questions:   cloneStoryQuestions(st.Questions),
						}
					}
					flows[j].Steps = steps
				}
			}
			out[i].Flows = flows
		}
	}
	return out
}

// cloneStoryQuestions returns a deep copy of questions, normalizing empty slices to nil.
func cloneStoryQuestions(questions []designeval.StoryQuestion) []designeval.StoryQuestion {
	if len(questions) == 0 {
		return nil
	}
	out := make([]designeval.StoryQuestion, len(questions))
	for i, q := range questions {
		var options []string
		if len(q.Options) > 0 {
			options = slices.Clone(q.Options)
		}
		out[i] = designeval.StoryQuestion{
			Key:         q.Key,
			Prompt:      q.Prompt,
			Description: q.Description,
			Options:     options,
		}
	}
	return out
}

// storySummaryItemsOrNil returns storySummaryItems(stories) when stories is
// non-empty, or nil so ChangeCardResult omits stories when none were adopted.
func storySummaryItemsOrNil(stories []designeval.StorySummary) []*gensurla.StorySummaryItem {
	if len(stories) == 0 {
		return nil
	}
	return storySummaryItems(stories)
}

// canonicalStorySummariesFromItems converts generated transport Story items
// into canonical designeval.StorySummary values, normalizing empty slices to nil.
func canonicalStorySummariesFromItems(items []*gensurla.StorySummaryItem) []designeval.StorySummary {
	if len(items) == 0 {
		return nil
	}
	out := make([]designeval.StorySummary, 0, len(items))
	for _, s := range items {
		if s == nil {
			continue
		}
		var actors []designeval.StoryActor
		if len(s.Actors) > 0 {
			actors = make([]designeval.StoryActor, 0, len(s.Actors))
			for _, a := range s.Actors {
				if a == nil {
					continue
				}
				actors = append(actors, designeval.StoryActor{
					Key:         a.Key,
					Title:       a.Title,
					Description: derefString(a.Description),
				})
			}
			if len(actors) == 0 {
				actors = nil
			}
		}
		var flows []designeval.StoryFlow
		if len(s.Flows) > 0 {
			flows = make([]designeval.StoryFlow, 0, len(s.Flows))
			for _, f := range s.Flows {
				if f == nil {
					continue
				}
				var steps []designeval.StoryStep
				if len(f.Steps) > 0 {
					steps = make([]designeval.StoryStep, 0, len(f.Steps))
					for _, st := range f.Steps {
						if st == nil {
							continue
						}
						steps = append(steps, designeval.StoryStep{
							Key:         st.Key,
							ActorKey:    st.ActorKey,
							Summary:     st.Summary,
							Description: derefString(st.Description),
							Kind:        st.Kind,
							Questions:   canonicalStoryQuestionsFromItems(st.Questions),
						})
					}
					if len(steps) == 0 {
						steps = nil
					}
				}
				flows = append(flows, designeval.StoryFlow{
					Key:         f.Key,
					Title:       f.Title,
					Description: derefString(f.Description),
					Complete:    f.Complete,
					Steps:       steps,
					Questions:   canonicalStoryQuestionsFromItems(f.Questions),
				})
			}
			if len(flows) == 0 {
				flows = nil
			}
		}
		out = append(out, designeval.StorySummary{
			Name:        s.Name,
			Description: derefString(s.Description),
			Actors:      actors,
			Flows:       flows,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// canonicalStoryQuestionsFromItems converts generated transport Story question
// items into canonical designeval.StoryQuestion values.
func canonicalStoryQuestionsFromItems(items []*gensurla.StoryQuestionItem) []designeval.StoryQuestion {
	if len(items) == 0 {
		return nil
	}
	out := make([]designeval.StoryQuestion, 0, len(items))
	for _, q := range items {
		if q == nil {
			continue
		}
		var options []string
		if len(q.Options) > 0 {
			options = slices.Clone(q.Options)
		}
		out = append(out, designeval.StoryQuestion{
			Key:         q.Key,
			Prompt:      q.Prompt,
			Description: derefString(q.Description),
			Options:     options,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// derefString returns *s when s is non-nil or "".
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// storySummaryItems converts canonical Story summaries into generated transport items.
func storySummaryItems(stories []designeval.StorySummary) []*gensurla.StorySummaryItem {
	out := make([]*gensurla.StorySummaryItem, len(stories))
	for i, s := range stories {
		actors := make([]*gensurla.StoryActorItem, len(s.Actors))
		for j, a := range s.Actors {
			actors[j] = &gensurla.StoryActorItem{
				Key:         a.Key,
				Title:       a.Title,
				Description: optionalString(a.Description),
			}
		}
		flows := make([]*gensurla.StoryFlowItem, len(s.Flows))
		for j, f := range s.Flows {
			steps := make([]*gensurla.StoryStepItem, len(f.Steps))
			for k, st := range f.Steps {
				steps[k] = &gensurla.StoryStepItem{
					Key:         st.Key,
					ActorKey:    st.ActorKey,
					Summary:     st.Summary,
					Description: optionalString(st.Description),
					Kind:        st.Kind,
					Questions:   storyQuestionItems(st.Questions),
				}
			}
			flows[j] = &gensurla.StoryFlowItem{
				Key:         f.Key,
				Title:       f.Title,
				Description: optionalString(f.Description),
				Complete:    f.Complete,
				Steps:       steps,
				Questions:   storyQuestionItems(f.Questions),
			}
		}
		out[i] = &gensurla.StorySummaryItem{
			Name:        s.Name,
			Description: optionalString(s.Description),
			Actors:      actors,
			Flows:       flows,
		}
	}
	return out
}

// storyQuestionItems converts canonical Story questions into generated transport items.
func storyQuestionItems(questions []designeval.StoryQuestion) []*gensurla.StoryQuestionItem {
	out := make([]*gensurla.StoryQuestionItem, len(questions))
	for i, q := range questions {
		options := slices.Clone(q.Options)
		if options == nil {
			options = []string{}
		}
		out[i] = &gensurla.StoryQuestionItem{
			Key:         q.Key,
			Prompt:      q.Prompt,
			Description: optionalString(q.Description),
			Options:     options,
		}
	}
	return out
}

// optionalString returns nil when s is empty or a pointer to a copy of s.
func optionalString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// newStoryRefreshOpID returns a 32-character lowercase hex identifier.
func newStoryRefreshOpID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate story refresh operation id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
