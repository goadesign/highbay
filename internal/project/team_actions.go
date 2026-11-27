// This file reads the team for the page and the CLI, records a build's worker
// roles, and turns team failures into the user's next step.
package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
)

// workersStep is the management read's stable next action.
const workersStep = "Start build workers from the Highbay page."

// ReadTeam returns the registered roles with the state last observed for
// each. It starts no terminal and calls neither herdr-core nor Chorus: a fact
// that could not be observed is reported as unknown, and a role that was
// never started is a value, not_started.
func (s *Server) ReadTeam(ctx context.Context) (*genhighbay.TeamSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, roles := s.currentTeam()
	result := &genhighbay.TeamSnapshot{
		Team:             record.Team,
		Channel:          "#" + record.Team,
		Roles:            make([]*genhighbay.TeamRoleState, 0, len(roles)),
		SavedChoices:     make(map[string]*genhighbay.RoleAgentChoice, len(record.SavedChoices)),
		DismissedNotices: append([]string{}, record.DismissedNotices...),
		ChorusStatus:     s.team.observed.chorusStatus(),
	}
	for role, saved := range record.SavedChoices {
		choice := genhighbay.RoleAgentChoice(saved)
		choice.Model = cloneChoiceOption(choice.Model)
		choice.Thinking = cloneChoiceOption(choice.Thinking)
		result.SavedChoices[role] = &choice
	}
	now := s.now()
	for _, role := range roles {
		state := s.team.observed.roleState(now, role, s.team.dependencies.Shell)
		state.Choice = result.SavedChoices[role.Role]
		result.Roles = append(result.Roles, state)
	}
	return result, nil
}

// EnsureTeam is the existing management view of saved team metadata. It
// performs no preparation; the guide attaches and users start workers explicitly.
func (s *Server) EnsureTeam(ctx context.Context) (*genproject.ProjectTeam, error) {
	if err := managementOnly(ctx, "ensure_team"); err != nil {
		return nil, err
	}
	team, err := s.ReadTeam(ctx)
	if err != nil {
		return nil, err
	}
	roles := make([]*genproject.ProjectTeamRole, 0, len(team.Roles))
	for _, role := range team.Roles {
		roles = append(roles, &genproject.ProjectTeamRole{
			Role: role.Role, Handle: role.Handle, HasTerminal: role.HasTerminal, Running: activeTeamState(role.State),
		})
	}
	return &genproject.ProjectTeam{Team: team.Team, Roles: roles, Step: workersStep}, nil
}

// RegisterBuildRoles durably records the accepted build's exact worker roles and folders.
// Registration completes even when the post-save phase context has ended: the next
// StartAgents must recover the saved build without starting it again. The context
// carries tracing only; this method starts no terminal and runs no external command.
func (s *Server) RegisterBuildRoles(ctx context.Context, folders controlplane.BuildFolders) error {
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.register_build_roles")
	defer span.End()
	var roles []agentRecord
	switch value := folders.(type) {
	case controlplane.BuilderOnly:
		roles = []agentRecord{{Role: "builder", Folder: value.BuilderFolder}}
	case controlplane.BuilderAndTestWriter:
		roles = []agentRecord{{Role: "builder", Folder: value.BuilderFolder}, {Role: "test_writer", Folder: value.TestWriterFolder}}
	default:
		err := errors.New("build registration requires builder working folders")
		span.RecordError(err)
		span.SetStatus(codes.Error, "invalid build role folders")
		return err
	}
	span.SetAttributes(attribute.Int("team.worker_roles", len(roles)))
	// Validate the complete constructed input before changing the registry.
	for _, agent := range roles {
		if err := validateAgent(teamName(agent.Role, s.state.Record.ID), agent); err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "invalid build role folders")
			return err
		}
	}
	s.team.Lock()
	defer s.team.Unlock()
	for _, agent := range roles {
		if _, err := s.registerAgent(teamName(agent.Role, s.state.Record.ID), agent); err != nil {
			return teamUnavailable(ctx, "register_role", "project_state")
		}
	}
	if len(roles) == 1 {
		if err := s.removeTestWriter(); err != nil {
			return teamUnavailable(ctx, "remove_test_writer", "project_state")
		}
	}
	return nil
}

// activeTeamState describes the states that establish a running agent.
func activeTeamState(state string) bool {
	switch state {
	case "starting", "needs_you", "working", "waiting":
		return true
	}
	return false
}

// guideRole selects the registered guide.
func guideRole(roles []teamRole) (teamRole, bool) {
	for _, role := range roles {
		if role.Role == "guide" {
			return role, true
		}
	}
	return teamRole{}, false
}

// userTeamError sanitizes a team failure before the control plane chooses its
// user-facing transport. A typed team failure becomes the page's step for its
// cause and stays its error's cause, so a build start can give its own step
// for the same cause. A client-policy refusal passes through for the control
// plane to phrase.
func userTeamError(err error) error {
	var policy *controlplane.TeamPolicyRefusal
	if errors.As(err, &policy) {
		return err
	}
	var unavailable *genproject.TeamUnavailable
	if !errors.As(err, &unavailable) {
		return genhighbay.MakeTeamUnavailable(errors.New("project team could not be prepared; try again"))
	}
	// Wrapping only the typed failure keeps any peer text around it out. A
	// project_state failure from this server's own state carries its repair.
	failure := genhighbay.MakeTeamUnavailable(unavailable)
	failure.Message = userTeamStep(unavailable.Cause)
	var state *teamStateUnavailable
	if errors.As(err, &state) {
		failure.Message = state.repair
	}
	return failure
}

// userTeamStep gives the page's fixed sentence for one team failure cause. A
// project_state failure from this server's own state replaces it with a repair
// naming the affected path (stateRepair); this sentence covers the rest.
func userTeamStep(cause string) string {
	switch cause {
	case "stopping":
		return "the project server is stopping; run highbay in the project folder, or ask your coding agent to open Highbay views, which starts it again and opens a fresh page, then try again there"
	case "chorus":
		return "Chorus is not running or its settings are invalid. Run chorus up (or chorus status to see the error), then try again."
	case "hc":
		return "herdr-core (hc), which runs each agent's terminal, did not complete a terminal command; run hc server status to see the error (highbay up installs and starts it), then try again"
	case "project_state":
		return "this project's private state could not be read or written; try again, and if it keeps failing, restore this project's Highbay state from a backup"
	case "role_folder":
		return "a team role's working folder could not be read; make sure this account can open the project folder and its build working folders, then start the agents again"
	}
	return "project team could not be prepared; try again"
}

// currentTeam copies the saved record and its roles while concurrent
// authentication and preference operations may be active. The team name is
// the saved one, or the one the project will save at its first team start.
func (s *Server) currentTeam() (Record, []teamRole) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	record := s.state.Record
	if record.Team == "" {
		record.Team = teamName(filepath.Base(record.Directory), record.ID)
	}
	return record, registeredTeamRoles(record.Agents)
}

// saveTeamName saves the project's Chorus team name the first time the team
// is used, so it stays the same if the project folder is later renamed.
func (s *Server) saveTeamName(ctx context.Context, team string) error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.Record.Team == team {
		return nil
	}
	before := s.state.Record.Team
	s.state.Record.Team = team
	if err := s.state.save(); err != nil {
		s.state.Record.Team = before
		return stateUnavailable(ctx, "save_team", s.recordWriteFailure(err))
	}
	return nil
}

// teamResult reports the registered roles as the latest observation has them,
// with step as the user's next action.
func (s *Server) teamResult(record Record, roles []teamRole, step string) *genhighbay.ProjectTeam {
	result := &genhighbay.ProjectTeam{Team: record.Team, Step: step, Roles: make([]*genhighbay.ProjectTeamRole, 0, len(roles))}
	now := s.now()
	for _, role := range roles {
		state := s.team.observed.roleState(now, role, s.team.dependencies.Shell)
		result.Roles = append(result.Roles, &genhighbay.ProjectTeamRole{
			Role: role.Role, Handle: role.Handle, HasTerminal: state.HasTerminal, Running: activeTeamState(state.State),
		})
	}
	return result
}

// chorusStatus says whether the latest Chorus read succeeded.
func (o *teamObservation) chorusStatus() string {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.chorusConnected {
		return "connected"
	}
	return "unavailable"
}

// removeTestWriter revokes an unused test writer before withdrawing its saved role.
// The operation lock serializes this exact-role update with starting agents.
func (s *Server) removeTestWriter() error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	for handle, agent := range s.state.Record.Agents {
		if agent.Role != "test_writer" {
			continue
		}
		if err := os.Remove(s.state.AgentFile(handle)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(s.state.Record.Agents, handle)
		return s.state.save()
	}
	return nil
}
