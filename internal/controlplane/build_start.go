// This file owns build admission through the copy, saved assignments and team.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/shquote"
	"github.com/goadesign/surla/internal/testwriter"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	goa "goa.design/goa/v3/pkg"
)

// defaultBuildStartTimeout bounds opening and refreshing one copy and one
// generation while the start holds the copy turn. The team owner's
// window phase has its own 45-second bound after that turn is released.
const defaultBuildStartTimeout = 5 * time.Minute

// StartBuild saves the agreed revision and its snapshots before registering
// roles or generating tests. The copy turn is nonblocking and covers both test
// sources; team window work starts only after it is released. Accepted work
// survives a client disconnect and drains before Close releases its owners.
func (c *ControlPlane) StartBuild(
	ctx context.Context, p *gensurla.StartBuildPayload,
) (*gensurla.StartBuildResult, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	principal, _ := PrincipalFromContext(ctx) // ownedTeam established the principal and owner.
	accepted, finish, err := c.admitBuildStart(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	accepted, span := c.tracer("surla/controlplane").Start(accepted, "surla.controlplane.start_build", trace.WithAttributes(
		attribute.String("surla.change_id", p.ChangeID),
		attribute.String("surla.request_revision", p.RequestRevision),
	))
	defer span.End()
	select {
	case c.builderCopyTurn <- struct{}{}:
	default:
		return nil, gensurla.MakeWorkingCopyBusy(errors.New("Another build is starting or the builder is reading its assignment. Start the build again when that has ended."))
	}
	builderTurnHeld := true
	defer func() {
		if builderTurnHeld {
			<-c.builderCopyTurn
		}
	}()
	select {
	case c.workingCopyTurn <- struct{}{}:
	default:
		message := "Another build is starting or a test check is using the working copy. "
		if c.storedEvaluator() {
			message = "Another build is starting. "
		}
		return nil, gensurla.MakeWorkingCopyBusy(errors.New(message + "Start the build again when that has ended."))
	}
	heldTurn := true
	defer func() {
		if heldTurn {
			<-c.workingCopyTurn
		}
	}()
	work, cancel := c.buildStartDeadline(accepted)
	defer cancel()

	// Check the agreed revision before any working-copy or team effect.
	c.mu.RLock()
	record, err := c.buildableCard(principal, p.ChangeID, p.RequestRevision)
	if err == nil {
		err = c.checkDraftAfterDrop(record)
	}
	c.mu.RUnlock()
	if err != nil {
		return nil, recordBuildError(span, err, "check change for build")
	}
	if _, _, err := designeval.StoryScenarios(record.stories); err != nil {
		return nil, gensurla.MakeUnprocessable(fmt.Errorf("the agreed Story cannot name its required tests: %w", err))
	}
	// The user decides against the exact open start before any copy effect.
	c.mu.RLock()
	open, openFound, err := c.cards.loadOpenBuild()
	var openCard storedChangeCard
	if err == nil && openFound {
		openCard, err = c.ownedCard(principal, open.ChangeID)
	}
	c.mu.RUnlock()
	if err != nil {
		return nil, recordBuildError(span, err, "read the open build")
	}
	source, layout, workspace, err := c.projectTestLayout(work)
	if err != nil {
		return nil, recordBuildError(span, buildStartError(work, "", err), "read test source")
	}
	if workspace == "" {
		return nil, gensurla.MakeNotImplemented(errors.New("No project working folder is bound. Start this project with surla to build it."))
	}
	repo, prefix, err := resolveStarterRepo(work, workspace)
	if err != nil {
		return nil, recordBuildError(span, buildStartError(work, workspace, err), "read the project repository")
	}
	head, err := runGitRead(work, repo, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			err = testwriter.ErrNoCommit
		}
		return nil, recordBuildError(span, buildStartError(work, workspace, err), "read the build base")
	}
	base := strings.TrimSpace(string(head))
	var openProgress *gensurla.StartedBuild
	if openFound {
		openProgress, err = c.startedBuildAt(work, principal.ID, heldBuild{
			file: open, inForce: buildInForce(open, openCard), storedEvaluator: c.storedEvaluator(),
		}, base)
		if err != nil {
			return nil, recordBuildError(span, err, "read the open build's passed runs")
		}
		if err := checkBuildReplacement(p, open, openProgress); err != nil {
			if refusal, ok := err.(*gensurla.OpenBuildRefusal); ok && refusal.Name == "previous_change_open" {
				if passed, ok := openProgress.Taking.AsPassed(); ok {
					safe, readErr := dropSafeGuidance(work, repo, open.Base, base, passed.CandidateCommit)
					if readErr != nil {
						return nil, readErr
					}
					if safe {
						refusal.Message += fmt.Sprintf(" If your branch already holds what you want from %s, Drop is safe: the next change starts from your branch as it is.", open.ChangeID)
					}
				} else {
					refusal.Message += fmt.Sprintf(" No verification of %s has passed, so there is nothing to merge yet: let the builder finish, or drop it.", open.ChangeID)
				}
			}
			return nil, err
		}
	}
	reuseBase := openFound && openProgress.Taking.Kind() != gensurla.BuildTakingKindMerged && openProgress.Taking.Kind() != gensurla.BuildTakingKindDropped
	if reuseBase {
		base = open.Base
	}
	// Check exactly the immutable provenance and base this build will save,
	// before assigning a number or touching either working copy.
	checkedHistory := cloneMadeFromSnapshot(record.madeFrom)
	if err := checkDesignBase(work, workspace, base, checkedHistory); err != nil {
		return nil, recordBuildError(span, err, "check the design base")
	}
	number := 0
	if reuseBase {
		number, base = open.Number, open.Base
	} else {
		last := 0
		if openFound {
			last = open.Number
		}
		number, err = testwriter.NextNumber(work, workspace, last)
		if err != nil {
			return nil, recordBuildError(span, err, "number the build")
		}
	}
	if source != testSourceStoredEvaluator && source != testSourceStarterLayout {
		return nil, gensurla.MakeNoTestLayout(errors.New("This project has neither stored verification settings nor Surla's test layout. Store verification settings, then build again."))
	}
	identity := c.buildIdentityCheck
	if identity == nil {
		identity = testwriter.CheckIdentity
	}
	if err := identity(work, workspace); err != nil {
		return nil, recordBuildError(span, buildStartError(work, workspace, err), "check Git identity")
	}
	var (
		copy           buildCopy
		builderExisted bool
		testsExisted   bool
	)
	if c.buildCopyFactory != nil {
		if source != testSourceStoredEvaluator {
			copy, err = c.openBuildCopy(work, workspace, layout)
			if err != nil {
				return nil, recordBuildError(span, buildStartError(work, workspace, err), "open test-writer copy")
			}
		}
	} else {
		_, builderExisted, err = testwriter.InspectCopy(
			work, workspace, testwriter.BuilderRole, testwriter.BuilderRole.Branch(number),
		)
		if err != nil {
			return nil, recordBuildError(span, buildStartError(work, workspace, err), "inspect builder copy")
		}
		if source != testSourceStoredEvaluator {
			_, testsExisted, err = testwriter.InspectCopy(
				work, workspace, testwriter.TestsRole, testwriter.TestsRole.Branch(number),
			)
			if err != nil {
				return nil, recordBuildError(span, buildStartError(work, workspace, err), "inspect test-writer copy")
			}
		}
	}

	// Save only after the second agreed-revision check. Clone this snapshot,
	// never the proposal or the earlier read used by the preflight.
	c.mu.Lock()
	record, err = c.buildableCard(principal, p.ChangeID, p.RequestRevision)
	if err == nil {
		err = work.Err()
	}
	if err == nil && !reflect.DeepEqual(record.madeFrom, checkedHistory) {
		err = gensurla.MakeConflict(errors.New("The agreed revision's saved design provenance changed while this build was being prepared. Read the change and build again."))
	}
	if err != nil {
		c.mu.Unlock()
		return nil, recordBuildError(span, buildStartError(work, workspace, err), "check change before save")
	}
	current, currentFound, err := c.cards.loadOpenBuild()
	if err != nil {
		c.mu.Unlock()
		return nil, recordBuildError(span, err, "check the open build before save")
	}
	if currentFound != openFound || currentFound && (current.ChangeID != open.ChangeID || !current.StartedAt.Equal(open.StartedAt)) {
		c.mu.Unlock()
		return nil, gensurla.MakeConflict(errors.New("The open build changed while this start was being prepared. Read get_build and decide again."))
	}
	provenance, hasProvenance, err := c.cards.loadMadeFrom()
	if err != nil {
		c.mu.Unlock()
		return nil, err
	}
	if hasProvenance && provenance.Start.Dropped != nil && record.madeFrom.Start.Write < provenance.Start.Write {
		c.mu.Unlock()
		return nil, gensurla.MakeDraftedBeforeDrop(errors.New("A build was dropped after this revision was drafted. Ask the guide to call refresh_story and revise_change, adopt the revision, then build."))
	}
	if currentFound && hasProvenance && buildDropped(provenance, current) != (openProgress.Taking.Kind() == gensurla.BuildTakingKindDropped) {
		c.mu.Unlock()
		return nil, gensurla.MakeConflict(errors.New("The open build changed while this start was being prepared. Read get_build and decide again."))
	}
	if currentFound {
		currentProgress := startedBuild(heldBuild{file: current, inForce: buildInForce(current, record), storedEvaluator: c.storedEvaluator()}, openProgress.Taking)
		if err := checkBuildReplacement(p, current, currentProgress); err != nil {
			c.mu.Unlock()
			return nil, err
		}
	}
	roles := buildRolesBuilderOnly
	if source != testSourceStoredEvaluator {
		roles = buildRolesBuilderAndTestWriter
	}
	saved := buildFile{
		ChangeID: p.ChangeID, RequestRevision: p.RequestRevision, StartedAt: time.Now().UTC(),
		Number: number, Base: base, GoMod: record.goMod, MadeFrom: *record.madeFrom,
		Roles: roles, Stories: cloneStorySummaries(record.stories),
		DesignSource: record.designSource, TestWriter: testWriterAssignment{State: testsAssigned},
		Answers: confirmedAnswers(record.card), Examples: record.card.Examples,
		PreservedBehaviors: append([]string{}, record.card.PreservedBehaviors...),
	}
	err = c.cards.saveOpenBuild(saved)
	c.mu.Unlock()
	if err != nil {
		return nil, recordBuildError(span, err, "save build")
	}
	result := &gensurla.StartBuildResult{
		Build: startedBuild(heldBuild{file: saved, inForce: true, storedEvaluator: c.storedEvaluator()}, gensurla.NewBuildTakingBuilding(&gensurla.BuildBuilding{})),
	}
	builderFolder := filepath.Join(workspace, testwriter.BuilderRole.Folder, filepath.FromSlash(prefix))
	testsFolder := filepath.Join(workspace, testwriter.TestsRole.Folder, filepath.FromSlash(prefix))
	var makeErr error
	if c.buildCopyFactory != nil {
		builderExisted = true
		if copy != nil {
			testsExisted = true
			testsFolder = copy.Project()
		}
	} else {
		builderBranch := testwriter.BuilderRole.Branch(saved.Number)
		if !builderExisted {
			bWC, bErr := testwriter.MakeCopy(work, workspace, testwriter.BuilderRole, builderBranch, saved.Base)
			if bErr != nil {
				makeErr = bErr
			} else {
				builderExisted = true
				builderFolder = bWC.Project()
			}
		}
		if source != testSourceStoredEvaluator && !testsExisted {
			testsBranch := testwriter.TestsRole.Branch(saved.Number)
			tWC, tErr := testwriter.MakeCopy(work, workspace, testwriter.TestsRole, testsBranch, saved.Base)
			if tErr != nil {
				if makeErr == nil {
					makeErr = tErr
				}
			} else {
				testsExisted = true
				testsFolder = tWC.Project()
			}
		}
	}
	if builderExisted {
		var folders BuildFolders = BuilderOnly{BuilderFolder: builderFolder}
		if source != testSourceStoredEvaluator && testsExisted {
			folders = BuilderAndTestWriter{BuilderFolder: builderFolder, TestWriterFolder: testsFolder}
		}
		// Registration is durable local work and is never starved by a generation
		// or window deadline. The project owner completes it even after cancellation.
		if err := team.RegisterBuildRoles(accepted, folders); err != nil {
			span.RecordError(errors.New("saved build roles could not be registered"))
			span.SetStatus(codes.Error, "register saved build roles")
			result.Team = buildAgainTeamOutcome()
			return result, nil
		}
	}
	if makeErr != nil {
		span.RecordError(makeErr)
		span.SetStatus(codes.Error, "make working copy")
		result.Team = buildAgainTeamOutcome()
		return result, nil
	}
	<-c.workingCopyTurn
	heldTurn = false
	<-c.builderCopyTurn
	builderTurnHeld = false
	cancel()
	if accepted.Err() != nil {
		result.Team = buildAgainTeamOutcome()
		return result, nil
	}
	prepared, err := team.StartAgents(accepted)
	result.Team = buildTeamOutcome(prepared, err)
	return result, nil
}

// saveBuildPreparation preserves a report made while generation ran. A verdict
// changes only this start's still-assigned report generation, under the lock;
// the saved file, including any assignment read, is always the update's base.
func (c *ControlPlane) saveBuildPreparation(
	ctx context.Context, saved buildFile, prepareErr error,
) (heldBuild, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, found, err := c.cards.loadOpenBuild()
	if err != nil {
		return heldBuild{}, err
	}
	if !found || current.ChangeID != saved.ChangeID {
		return heldBuild{}, errors.New("the saved build was replaced during preparation")
	}
	if prepareErr != nil && current.RequestRevision == saved.RequestRevision &&
		current.StartedAt.Equal(saved.StartedAt) && current.ReportNumber == saved.ReportNumber &&
		current.TestWriter.State == testsAssigned {
		tw := &current.TestWriter
		tw.CheckedAt = time.Now().UTC()
		var author *testwriter.AuthorError
		if errors.As(prepareErr, &author) {
			tw.State = testsNeedChanges
			msg := author.Finding.Message
			if author.Finding.Kind != testwriter.FindingWorkingCopy {
				msg += "\nCorrect the problem in the test module, then report_tests_finished."
			}
			tw.Findings = []string{msg}
		} else {
			tw.State = testsCouldNotRun
			reason, step := preparationFailure(ctx, prepareErr)
			tw.Reason, tw.Step = boundedBuildReason(reason), step
		}
		if err := c.cards.saveOpenBuild(current); err != nil {
			return heldBuild{}, err
		}
	}
	record, ok, err := c.cards.load(saved.ChangeID)
	if err != nil {
		return heldBuild{}, err
	}
	if !ok {
		return heldBuild{}, fmt.Errorf("unknown change_id %q", saved.ChangeID)
	}
	return heldBuild{file: current, inForce: buildInForce(current, record), storedEvaluator: c.storedEvaluator()}, nil
}

// confirmedAnswers pairs each question of the confirmed card with the answer
// the user confirmed for it, in question order. confirm_intent confirmed the
// card only with one declared option for every question and no other answer.
func confirmedAnswers(card *gensurla.ChangeCardResult) []*gensurla.ConfirmedAnswer {
	answers := make([]*gensurla.ConfirmedAnswer, len(card.Ambiguities))
	for i, q := range card.Ambiguities {
		answers[i] = &gensurla.ConfirmedAnswer{ID: q.ID, Question: q.Question, Answer: card.Answers[q.ID]}
	}
	return answers
}

// preparationFailure returns the reason and the user's step saved when a
// build's required tests could not be prepared for a cause the test writer
// cannot fix. ctx is the start's limit and err what the preparation returned.
// The reason says which step failed: the limit or a server stop, a download
// of the test module's dependencies that fails for a cause outside the test
// writer's changes (testwriter.ErrDependencies, whose step has the user run
// the download where its output can be read), the generator (worded by
// designeval.GenerationDiagnostic, which names its phase), or writing the
// generated folder, whose cause is quoted without absolute paths and whose
// step names what to repair before building again.
// Any other failure has no text safe to show: its reason points at the server
// trace, where StartBuild records the cause.
func preparationFailure(ctx context.Context, err error) (reason, step string) {
	const buildAgain = "Build this change again on the views page."
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "The build-start time limit ended before the required tests could be prepared.", buildAgain
	case errors.Is(ctx.Err(), context.Canceled):
		return "The server stopped before the required tests could be prepared.", buildAgain
	case errors.Is(err, testwriter.ErrDependencies):
		return "Surla could not prepare the required tests: " + testwriter.WithoutAbsolutePaths(err.Error()),
			stepDependencies + ", then build this change again on the views page."
	case errors.Is(err, testwriter.ErrGeneration):
		return designeval.GenerationDiagnostic(err), buildAgain
	case errors.Is(err, testwriter.ErrWriteGenerated):
		return "Surla generated the required tests but could not write them into the test writer's working copy: " +
			testwriter.WithoutAbsolutePaths(err.Error()), stepWriteCopy + ", then build this change again on the views page."
	default:
		return "Surla could not prepare the required tests, for a cause outside the test module. " +
			"The server trace of this build start has the details.", buildAgain
	}
}

// buildStartError preserves named library steps and maps only pre-save refusals.
func buildStartError(ctx context.Context, workspace string, err error) error {
	if ctx.Err() != nil {
		return gensurla.MakeUnavailable(errors.New("The server stopped or the build-start limit ended before saving. Call again on a running server."))
	}
	switch {
	case errors.Is(err, testwriter.ErrNotRepository), errors.Is(err, errNoRepository):
		return gensurla.MakeNotRepository(fmt.Errorf("Project folder %s: %w", shquote.Quote(workspace), err))
	case errors.Is(err, testwriter.ErrNoCommit):
		return gensurla.MakeNoCommit(fmt.Errorf("Project folder %s: %w", shquote.Quote(workspace), err))
	case errors.Is(err, testwriter.ErrIdentityMissing):
		return gensurla.MakeGitIdentityMissing(err)
	case errors.Is(err, testwriter.ErrFolderInUse):
		return gensurla.MakeCopyFolderInUse(err)
	case errors.Is(err, testwriter.ErrBranchInUse):
		return gensurla.MakeTestsBranchInUse(err)
	default:
		return err
	}
}

// buildAgainTeamOutcome is the recovery for a saved start interrupted before its team phase.
func buildAgainTeamOutcome() gensurla.Team {
	return gensurla.NewTeamUnavailable(&gensurla.BuildTeamUnavailable{Step: "Build this change again on the views page."})
}

// boundedBuildReason fits a safe owner diagnostic to the declared reason bound.
func boundedBuildReason(reason string) string {
	runes := []rune(reason)
	return string(runes[:min(len(runes), maxCheckReason)])
}

// admitBuildStart uses the workspace admission barrier so Close cannot miss
// accepted work. The caller's values and tracing survive, while cancellation
// belongs to the server and this start's limit. The returned finish must be
// called once after every effect, including post-save team admission, ends.
func (c *ControlPlane) admitBuildStart(ctx context.Context) (context.Context, func(), error) {
	c.workspaceMu.RLock()
	defer c.workspaceMu.RUnlock()
	if c.serverCtx.Err() != nil {
		return nil, nil, gensurla.MakeUnavailable(errors.New("the server is shutting down; call again on a running server"))
	}
	c.buildStartOps.Add(1)
	accepted, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(c.serverCtx, cancel)
	if c.serverCtx.Err() != nil {
		cancel()
	}
	return accepted, func() {
		cancel()
		stop()
		c.buildStartOps.Done()
	}, nil
}

// buildStartDeadline bounds only the work under the copy turn. Team start uses
// the server-owned admission context after this phase, with its own limit.
func (c *ControlPlane) buildStartDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	withTimeout := c.buildStartWithTimeout
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	return withTimeout(ctx, defaultBuildStartTimeout)
}

// buildTeamOutcome preserves a saved build when its team cannot become ready.
// Outside error messages never become user steps: the team returns typed
// conditions, and this boundary owns the action shown by start_build. A team
// failure keeps its typed cause, which selects the cause's own step.
func buildTeamOutcome(team *gensurla.ProjectTeam, err error) gensurla.Team {
	if err == nil {
		return gensurla.NewTeamReady(team)
	}
	var spaces *genproject.TeamSpaceConflict
	if errors.As(err, &spaces) {
		return gensurla.NewTeamAmbiguousSpace(&gensurla.BuildTeamAmbiguousSpace{
			Count: spaces.Count,
			Step:  gensurla.BuildStartStep(duplicateTeamSpacesStep()),
		})
	}
	var unavailable *goa.ServiceError
	if errors.As(err, &unavailable) && unavailable.Name == "window_busy" {
		return gensurla.NewTeamWindowBusy(&gensurla.BuildTeamWindowBusy{
			Step: "Close the open Herdr dialog, then start the team agents.",
		})
	}
	cause := ""
	var failure *genproject.TeamUnavailable
	if errors.As(err, &failure) {
		cause = failure.Cause
	}
	return gensurla.NewTeamUnavailable(&gensurla.BuildTeamUnavailable{
		Step: gensurla.BuildStartStep(buildTeamStep(cause)),
	})
}

// buildTeamStep is a saved build's step for one team failure cause, as the
// surla command and the views page each give one: what could not be used,
// its repair, and then Start agents, which starts the team for the saved
// build without building again. An unknown cause, including none, names
// surla, which prints the step for the cause. Each step fits BuildStartStep.
func buildTeamStep(cause string) string {
	const retry = "then start the team agents for this saved build."
	switch cause {
	case "stopping":
		return "The project server is stopping. Run surla in the project folder to start it again; " +
			"that also starts the team agents for this saved build."
	case "herdr":
		return "Herdr did not complete a team command. Open the Herdr app, " + retry
	case "roster_folder":
		return "Herdr's launcher roster folder could not be written. Make data/chorus-launcher/rosters " +
			"in Herdr's app folder (~/.config/herdr-app unless HERDR_APP_HOME is set) writable by this account, " + retry
	case "project_state":
		return "This project's private state could not be read or written. " +
			"Run surla in the project folder to see which folder to repair, " + retry
	case "role_folder":
		return "A team role's working folder could not be read. Make sure this account can open " +
			"the project folder and its build working folders, " + retry
	}
	return "The project team could not be prepared. Run surla in the project folder to see the step, " + retry
}

// checkBuildReplacement refuses another change while the open build remains,
// and an unfinished restart without consent naming exactly the observed start.
func checkBuildReplacement(p *gensurla.StartBuildPayload, saved buildFile, progress *gensurla.StartedBuild) error {
	name, message := "", ""
	if progress.Taking.Kind() == gensurla.BuildTakingKindMerged || progress.Taking.Kind() == gensurla.BuildTakingKindDropped {
		return nil
	}
	if saved.ChangeID != p.ChangeID {
		name = "previous_change_open"
		message = fmt.Sprintf("Change %s still has an open build. Merge a passed run or drop that build before building %s.", saved.ChangeID, p.ChangeID)
	} else if progress.Taking.Kind() == gensurla.BuildTakingKindBuilding &&
		(p.Replaces == nil || p.Replaces.ChangeID != saved.ChangeID || p.Replaces.StartedAt != saved.StartedAt.UTC().Format(time.RFC3339Nano)) {
		name = "unfinished_build_open"
		message = fmt.Sprintf("No verification of change %s's open build has passed. Decide whether to build it again, then send replaces naming the build shown.", saved.ChangeID)
	}
	if name == "" {
		return nil
	}
	return &gensurla.OpenBuildRefusal{Name: name, Message: message, ChangeID: saved.ChangeID, Build: progress}
}

// checkDraftAfterDrop refuses only revisions older than an actual drop.
// The caller holds c.mu, which serializes Drop's provenance publication.
func (c *ControlPlane) checkDraftAfterDrop(record storedChangeCard) error {
	provenance, found, err := c.cards.loadMadeFrom()
	if err != nil {
		return err
	}
	if found && provenance.Start.Dropped != nil && record.madeFrom.Start.Write < provenance.Start.Write {
		return gensurla.MakeDraftedBeforeDrop(errors.New("A build was dropped after this revision was drafted. Ask the guide to call refresh_story and revise_change, adopt the revision, then build."))
	}
	return nil
}
