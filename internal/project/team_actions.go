// This file exposes the team owner's non-creating read and the user's explicit actions.
package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	goa "goa.design/goa/v3/pkg"
)

// ReadTeam returns registered roles only and never creates a space or agent.
// Missing observations are unknown; expected absence before team preparation is a value.
func (s *Server) ReadTeam(ctx context.Context) (*genhighbay.TeamSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	selection, record := s.teamState()
	result := &genhighbay.TeamSnapshot{
		Team: selection.Team, HerdrMode: s.herdrMode(record.Tab),
		Roles:            make([]*genhighbay.TeamRoleState, 0, len(selection.Roles)),
		SavedChoices:     make(map[string]*genhighbay.RoleAgentChoice, len(record.SavedChoices)),
		DismissedNotices: append([]string{}, record.DismissedNotices...),
	}
	for role, saved := range record.SavedChoices {
		choice := genhighbay.RoleAgentChoice(saved)
		choice.Model = cloneChoiceOption(choice.Model)
		choice.Thinking = cloneChoiceOption(choice.Thinking)
		result.SavedChoices[role] = &choice
	}
	s.team.observationMu.RLock()
	defer s.team.observationMu.RUnlock()
	// Workers outside Herdr never use Chorus; in Herdr the supervisor's last
	// view read for this space and endpoint settles it, and nothing is said
	// before one has.
	if notUsed := "not_used"; !s.team.dependencies.HerdrAvailable {
		result.ChorusStatus = &notUsed
	} else if chorus := s.team.chorus; chorus.status != "" && chorus.space == selection.Space && chorus.endpoint == selection.connection.Endpoint {
		result.ChorusStatus = &chorus.status
	}
	for _, role := range selection.Roles {
		value := genhighbay.TeamRoleState{Role: role.Role, Handle: role.Handle, State: "unknown", Attention: "none", Watcher: "unknown", Assignment: "unknown"}
		if role.Role == "guide" {
			value.Assignment = "nothing_owed"
		}
		if observation, ok := s.team.observed[role.Role]; ok && observation.handle == role.Handle && observation.space == selection.Space && observation.endpoint == selection.connection.Endpoint {
			value = observation.public
		}
		if s.team.headless.running(role.Role) {
			value.State = "working"
		}
		// A registered guide that no Herdr tab ever adopted has no pane in
		// the workers' space; Herdr cannot say what it is doing.
		if role.Role == "guide" && record.Tab == "" && value.State == "not_started" {
			value.State = "unknown"
		}
		if role.Role == "guide" && (value.State == "ended" || value.State == "not_started") {
			result.HerdrMode = s.herdrMode("")
		}
		result.Roles = append(result.Roles, &value)
	}
	return result, nil
}

// RegisterBuildRoles durably records the accepted build's exact worker roles and folders.
// Registration completes even when the post-save phase context has ended: the next
// StartAgents must recover the saved build without starting it again. The context
// carries tracing only; this method performs no window work or external commands.
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

// ShowPane first checks overlays, then re-reads the registered role's owned terminals
// and space. It never changes a window for an unregistered or differently labelled pane.
func (s *Server) ShowPane(ctx context.Context, role string) error {
	s.team.Lock()
	defer s.team.Unlock()
	if !s.team.dependencies.HerdrAvailable {
		return genhighbay.MakePaneUnavailable(errors.New("this role runs without a Herdr pane"))
	}
	host := s.team.dependencies.Host
	overlays, err := host.Overlays(ctx)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "overlays", "herdr"))
	}
	if slices.ContainsFunc(overlays, func(name string) bool { return name != "highbay" }) {
		return genhighbay.MakeWindowBusy(errors.New("close the open Herdr dialog before showing a pane"))
	}
	selection := s.teamSelection()
	var handle string
	for _, registered := range selection.Roles {
		if registered.Role == role {
			handle = registered.Handle
		}
	}
	if handle == "" || selection.Space == "" {
		return genhighbay.MakePaneUnavailable(errors.New("this role has no owned pane to show; start its agent"))
	}
	// Discover through all three labels before reading any terminal facts.
	terminals, err := host.Terminals(ctx, selection.Root, selection.Space, []string{handle})
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "terminals", "herdr"))
	}
	var address string
	for _, terminal := range terminals {
		if !selection.owns(terminal, handle) || terminal.State != "live" {
			continue
		}
		if address != "" {
			return genhighbay.MakePaneUnavailable(errors.New("this role has several owned panes; choose one in Herdr"))
		}
		address = terminal.Address
	}
	if address == "" {
		return genhighbay.MakePaneUnavailable(errors.New("this role has no owned pane to show; start its agent"))
	}
	spaces, err := host.Spaces(ctx)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "spaces", "herdr"))
	}
	for _, space := range spaces {
		if space.ID != selection.Space || space.Machine != nil {
			continue
		}
		for _, tab := range space.Tabs {
			for _, pane := range tab.Terminals {
				if pane.Terminal == address {
					fresh, err := host.ReadTerminal(ctx, address)
					if err != nil || !selection.owns(fresh, handle) || fresh.State != "live" {
						return genhighbay.MakePaneUnavailable(errors.New("the requested pane changed; read the team again"))
					}
					space.Tabs = []TeamTab{{ID: tab.ID, Terminals: []TeamPane{{Terminal: address}}}}
					if err := host.Focus(ctx, space); err != nil {
						return userTeamError(teamUnavailable(ctx, "focus", "herdr"))
					}
					return nil
				}
			}
		}
	}
	return genhighbay.MakePaneUnavailable(errors.New("the requested pane is no longer in this project's space"))
}

// teamSelection copies only immutable selection facts, never credentials or external replies.
func (s *Server) teamSelection() TeamSelection {
	selection, _ := s.teamState()
	return selection
}

// teamState gives effects and pure readers one consistent record snapshot.
func (s *Server) teamState() (TeamSelection, Record) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	record := s.state.Record
	space := record.Space
	if !safeSpaceID(space) {
		space = ""
	}
	selection := TeamSelection{Root: record.Directory, Space: space, Team: record.Team}
	s.team.observationMu.RLock()
	selection.connection = s.team.connection
	s.team.observationMu.RUnlock()
	if selection.Team == "" {
		selection.Team = teamName(filepath.Base(record.Directory), record.ID)
	}
	for _, role := range []string{"guide", "builder", "test_writer"} {
		for handle, registered := range record.Agents {
			if registered.Role == role {
				selection.Roles = append(selection.Roles, TeamRole{Role: role, Handle: handle})
			}
		}
	}
	return selection, record
}

// userTeamError preserves the owner's typed space conflict and sanitizes other
// management failures before the control plane chooses its user-facing transport.
// A typed team failure becomes the page's step for its cause and stays its
// error's cause, so a build start can give its own step for the same cause.
func userTeamError(err error) error {
	var conflict *controlplane.TeamSpaceConflict
	if errors.As(err, &conflict) {
		return err
	}
	var domain *goa.ServiceError
	if errors.As(err, &domain) && domain.Name == "window_busy" {
		return genhighbay.MakeWindowBusy(errors.New("close the open Herdr dialog before showing a pane"))
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
		return "the project server is stopping; ask your coding agent to open Highbay views, or choose Start Highbay in the Herdr companion, which starts it again and opens a fresh page, then use Team → Start workers there"
	case "chorus":
		return "Chorus is not running or its settings are invalid. Run chorus up (or chorus status to see the error), then try again."
	case "herdr":
		// Starting agents and showing a pane both fail with this cause, so the step
		// retries whichever action the user took.
		return "Herdr did not complete a team command; open the Herdr app, then try again"
	case "saved_space":
		return "Close this project's obsolete Herdr space, then start the agents again."
	case "roster_folder":
		return "Herdr's launcher roster folder could not be written; make data/chorus-launcher/rosters in Herdr's app folder (~/.config/herdr-app unless HERDR_APP_HOME is set) writable by this account, then start the agents again"
	case "project_state":
		return "this project's private state could not be read or written; try again, and if it keeps failing, restore this project's Highbay state from a backup"
	case "role_folder":
		return "a team role's working folder could not be read; make sure this account can open the project folder and its build working folders, then start the agents again"
	}
	return "project team could not be prepared; try again"
}

// removeTestWriter revokes an unused test writer before withdrawing its saved role.
// The operation lock serializes this exact-role update with roster publication.
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
