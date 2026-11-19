// This file binds the project-owned team to user operations and read-only assignment facts.
package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/verifyrun"
	"go.opentelemetry.io/otel/codes"
)

type (
	// Team is the project server's sole in-memory team owner. Authorization belongs
	// to the control plane's generated endpoint policy, never these internal calls.
	// BindAssignments installs the owner's read-only assignment reader before the
	// server listens. Supervise returns nil when either of its owning lifetimes
	// ends; a non-nil error means unexpected failure and permits a bounded retry.
	Team interface {
		BindAssignments(AssignmentReader)
		ReadTeam(context.Context) (*gensurla.TeamSnapshot, error)
		ShowPane(context.Context, string) error
		StartAgents(context.Context, map[string]*gensurla.RoleAgentChoice) (*gensurla.ProjectTeam, error)
		ListAgentOptions(context.Context) ([]*gensurla.InstalledAgentOption, error)
		DismissNotice(context.Context, string) error
		StopWorkers(context.Context, BuildIdentity) error
		Close() error
		RegisterBuildRoles(context.Context, BuildFolders) error
		Supervise(context.Context, AssignmentReader) error
	}
	// BuildFolders selects the roles a build adds to the project-owned team.
	// Its two cases carry only working folders: admission cannot name the guide,
	// duplicate a role, or supply credentials or first messages. Registration saves
	// exactly these roles and folders, removing an old test writer and credential
	// for BuilderOnly. It performs no window operation; StartAgents owns that phase.
	BuildFolders interface {
		buildFolders()
	}
	// BuilderOnly admits the builder when stored verification settings supply tests.
	BuilderOnly struct {
		BuilderFolder string
	}
	// BuilderAndTestWriter admits both roles with their independently owned folders.
	BuilderAndTestWriter struct {
		BuilderFolder    string
		TestWriterFolder string
	}
	// TeamBinding associates the one team owner with its immutable resource owner.
	// The composition root binds it once before New; it never creates user credentials.
	TeamBinding struct {
		Owner   string
		Service Team
	}
	// TeamAssignments reports which worker roles currently have work on the open
	// build and which unread assignment generations they owe. Unread holds a role
	// only while that role has work and has not read its current assignment.
	TeamAssignments struct {
		// BuildIdentity selects the currently observed saved build, including a dropped one.
		BuildIdentity *BuildIdentity
		// Merged and Dropped end workers only for the exact observed build.
		Merged  bool
		Dropped bool
		// ReportCheckInFlight prevents another test-writer invocation while its report is checked.
		ReportCheckInFlight bool
		// VerificationInFlight prevents a builder invocation while verification runs.
		VerificationInFlight bool
		// LatestCompletedRunID and LatestRunPassed identify the latest completed result for this build.
		LatestCompletedRunID string
		LatestRunPassed      bool
		// BuilderHasWork reports whether the builder currently has work on the open build.
		BuilderHasWork bool
		// TestWriterHasWork reports whether the test writer currently has work on the open build.
		TestWriterHasWork bool
		// Unread holds the owed assignment generation for each role with work that has not read it.
		Unread []AssignmentGeneration
	}
	// BuildIdentity distinguishes rebuilds of the same change without exposing work content.
	BuildIdentity struct {
		ChangeID  string
		StartedAt time.Time
	}
	// AssignmentGeneration names one currently owed read. Generation is opaque to
	// the team owner and contains no work text, identifiers or findings.
	AssignmentGeneration struct {
		Role       string
		Generation string
	}
	// AssignmentReader returns the bound owner's current worker work and unread
	// assignment generations. An error means unknown, neither no_work nor an
	// empty owed set. Reading never marks work read.
	AssignmentReader func(context.Context) (TeamAssignments, error)
)

// HasWork reports whether the named worker role ("builder" or "test_writer")
// currently has work on the open build.
func (a TeamAssignments) HasWork(role string) bool {
	switch role {
	case "builder":
		return a.BuilderHasWork
	case "test_writer":
		return a.TestWriterHasWork
	default:
		return false
	}
}

// GetTeam reads only the project owner's current snapshot and creates no team.
func (c *ControlPlane) GetTeam(ctx context.Context) (*gensurla.TeamSnapshot, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	return team.ReadTeam(ctx)
}

// ShowTeamPane delegates the user's role after purpose and resource authorization.
func (c *ControlPlane) ShowTeamPane(ctx context.Context, p *gensurla.ShowTeamPanePayload) error {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return err
	}
	return team.ShowPane(ctx, p.Role)
}

// StartTeamAgents uses the project owner's existing repeatable team preparation
// and maps its typed space conflict to the user transport's actionable error.
func (c *ControlPlane) StartTeamAgents(ctx context.Context, p *gensurla.StartTeamAgentsPayload) (*gensurla.ProjectTeam, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	result, err := team.StartAgents(ctx, p.Choices)
	var conflict *genproject.TeamSpaceConflict
	if errors.As(err, &conflict) {
		return nil, gensurla.MakeAmbiguousSpace(fmt.Errorf(
			"%d Herdr spaces match this project. %s", conflict.Count, duplicateTeamSpacesStep(),
		))
	}
	return result, err
}

// ListTeamAgentOptions discovers installed options only on the user's explicit read.
func (c *ControlPlane) ListTeamAgentOptions(ctx context.Context) (*gensurla.ListTeamAgentOptionsResult, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	agents, err := team.ListAgentOptions(ctx)
	if err != nil {
		return nil, err
	}
	return &gensurla.ListTeamAgentOptionsResult{Agents: agents}, nil
}

// DismissTeamNotice persists a project notice preference through its team owner.
func (c *ControlPlane) DismissTeamNotice(ctx context.Context, p *gensurla.DismissTeamNoticePayload) error {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return err
	}
	return team.DismissNotice(ctx, p.Notice)
}

// assignmentGeneration returns the hex SHA-256 of identity's JSON form.
func assignmentGeneration(identity any) (string, error) {
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// duplicateTeamSpacesStep owns the recovery shared by user starts and saved builds.
// Surla cannot select among matching spaces, so the duplicates must be closed first.
func duplicateTeamSpacesStep() string {
	return "Close the duplicate Herdr spaces, then start the agents."
}

// buildFolders seals the builder-only case to this package.
func (BuilderOnly) buildFolders() {}

// buildFolders seals the builder-and-test-writer case to this package.
func (BuilderAndTestWriter) buildFolders() {}

// ownedTeam checks resource ownership once at each operation's entry, independently
// of the generated endpoint's caller-purpose policy. Internal observation reads
// already belong to the fixed project owner and perform no repeated authorization.
func (c *ControlPlane) ownedTeam(ctx context.Context) (Team, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok || principal.ID != c.team.Owner {
		return nil, gensurla.MakeForbidden(errors.New("caller may not access this project team"))
	}
	return c.team.Service, nil
}

// startTeamSupervisor binds the assignment reader synchronously before New
// returns (and before the server listens) and admits the one lifetime worker
// only after all stores and fields are ready.
func (c *ControlPlane) startTeamSupervisor() {
	c.team.Service.BindAssignments(c.assignmentGenerations)
	c.teamWorker.Go(c.superviseTeam)
}

// superviseTeam prevents an unexpected dependency return from ending the server or spinning.
func (c *ControlPlane) superviseTeam() {
	for c.serverCtx.Err() == nil {
		err := c.team.Service.Supervise(c.serverCtx, c.assignmentGenerations)
		if err == nil || c.serverCtx.Err() != nil {
			return
		}
		_, span := c.tracer("surla/controlplane").Start(c.serverCtx, "surla.team.supervisor_failed")
		// Dependency details never carry external output or private paths into spans.
		span.RecordError(errors.New("team supervisor could not continue"))
		span.SetStatus(codes.Error, "team supervisor ended")
		span.End()
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-c.serverCtx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// assignmentGenerations deliberately uses the store owner's nonmutating read,
// including builds of earlier revisions. It reports which worker roles
// currently have work on the open build and, for each role with work that has
// not read its current assignment, the generation owed: its build (change,
// revision and start) and what the role's assignment shows of the verdicts.
// The builder's adds the accepted commit; the test writer's adds the report
// number and the verdict's time. Every non-accepting verdict gives the test
// writer a new generation, while an acceptance ends the test writer's work on
// that build and gives the builder a new unread generation while no run of the
// open build has passed.
func (c *ControlPlane) assignmentGenerations(ctx context.Context) (TeamAssignments, error) {
	if err := ctx.Err(); err != nil {
		return TeamAssignments{}, err
	}
	var builds []heldBuild
	result := TeamAssignments{
		Unread: make([]AssignmentGeneration, 0, 2),
	}
	// Dropped builds no longer appear in work lists, but their exact identity
	// must remain visible to lifecycle cleanup until a later build replaces it.
	c.mu.RLock()
	saved, found, loadErr := c.cards.loadOpenBuild()
	if loadErr == nil && found {
		var record storedChangeCard
		var exists bool
		record, exists, loadErr = c.cards.load(saved.ChangeID)
		if loadErr == nil && (!exists || record.owner != c.team.Owner) {
			loadErr = errors.New("open build has no owned change card")
		}
		if loadErr == nil {
			var provenance madeFromRecord
			var hasProvenance bool
			provenance, hasProvenance, loadErr = c.cards.loadMadeFrom()
			result.BuildIdentity = &BuildIdentity{ChangeID: saved.ChangeID, StartedAt: saved.StartedAt}
			result.Dropped = hasProvenance && buildDropped(provenance, saved)
			result.ReportCheckInFlight = !result.Dropped && saved.TestWriter.State == testsReported
			if !result.Dropped && c.usableBuild(saved) {
				builds = append(builds, heldBuild{file: saved, record: record, inForce: buildInForce(saved, record), storedEvaluator: c.storedEvaluator()})
			}
		}
	}
	c.mu.RUnlock()
	if loadErr != nil {
		return TeamAssignments{}, loadErr
	}
	for _, build := range builds {
		saved := build.file
		if c.runsDir != "" {
			runs, err := verifyrun.List(c.runsDir, c.team.Owner, saved.ChangeID)
			if err != nil {
				return TeamAssignments{}, err
			}
			for _, run := range runs {
				if run.Request.Plan.RequestRevision != saved.RequestRevision || run.Request.AcceptedAt.Before(saved.StartedAt) || !c.verificationInputsCurrent(build.record, saved, true, run.Request.Plan) {
					continue
				}
				switch run.State.Kind {
				case verifyrun.StateRunning:
					result.VerificationInFlight = true
				case verifyrun.StateCompleted:
					if result.LatestCompletedRunID == "" {
						result.LatestCompletedRunID = run.Request.RunID
						result.LatestRunPassed = run.State.Report.Passed
					}
				}
			}
		}
		builderWork, err := c.builderHasWork(c.team.Owner, build)
		if err != nil {
			return TeamAssignments{}, err
		}
		if builderWork {
			result.BuilderHasWork = true
			if !saved.Builder.Read {
				generation, err := assignmentGeneration(struct {
					Change, Revision string
					Started          time.Time
					Accepted         string
				}{saved.ChangeID, saved.RequestRevision, saved.StartedAt, saved.TestWriter.Commit})
				if err != nil {
					return TeamAssignments{}, err
				}
				result.Unread = append(result.Unread, AssignmentGeneration{Role: "builder", Generation: generation})
			}
		}
		if testWriterHasWork(build) {
			result.TestWriterHasWork = true
			if !saved.TestWriter.Read {
				generation, err := assignmentGeneration(struct {
					Change, Revision string
					Started          time.Time
					Report           int
					Checked          time.Time
				}{saved.ChangeID, saved.RequestRevision, saved.StartedAt, saved.ReportNumber, saved.TestWriter.CheckedAt})
				if err != nil {
					return TeamAssignments{}, err
				}
				result.Unread = append(result.Unread, AssignmentGeneration{Role: "test_writer", Generation: generation})
			}
		}
		if !result.BuilderHasWork {
			state, err := c.startedBuild(ctx, c.team.Owner, build)
			if err != nil {
				return TeamAssignments{}, err
			}
			result.Merged = state.Taking.Kind() == gensurla.BuildTakingKindMerged
			if result.Merged {
				result.TestWriterHasWork = false
				result.Unread = nil
			}
		}
	}
	return result, nil
}
