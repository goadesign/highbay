// This file starts and stops only the workers belonging to an accepted build.
package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

type (
	// liveRole identifies an owned terminal and its actual model pane in one space.
	liveRole struct {
		tab  string
		pane string
	}
)

// EnsureTeam is the existing management view of saved team metadata. It
// performs no preparation; the guide attaches and users start workers explicitly.
func (s *Server) EnsureTeam(ctx context.Context) (*genproject.ProjectTeam, error) {
	if err := managementOnly(ctx, "ensure_team"); err != nil {
		return nil, err
	}
	result, err := s.ReadTeam(ctx)
	if err != nil {
		return nil, err
	}
	selection := s.teamSelection()
	roles := make([]*genproject.ProjectTeamRole, 0, len(result.Roles))
	for _, role := range result.Roles {
		roles = append(roles, &genproject.ProjectTeamRole{
			Role: role.Role, Handle: role.Handle, Running: activeTeamState(role.State),
		})
	}
	return &genproject.ProjectTeam{
		Team: result.Team, Space: localSpace(selection.Space),
		HerdrMode: result.HerdrMode, Roles: roles, Step: workersStep,
	}, nil
}

// StartAgents snapshots the current build and admits only workers with work.
// The control plane authorizes the user once. A disconnected request can
// repeat the operation without replacing a running guide or worker.
func (s *Server) StartAgents(ctx context.Context, requested map[string]*gensurla.RoleAgentChoice) (*gensurla.ProjectTeam, error) {
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("surla/project").Start(ctx, "surla.project.start_agents")
	defer span.End()
	s.team.Lock()
	defer s.team.Unlock()
	if s.team.closed || ctx.Err() != nil {
		return nil, userTeamError(teamUnavailable(ctx, "lifetime", "stopping"))
	}
	record, roles := s.currentTeam()
	assignments, err := s.readAssignments(ctx)
	if err != nil {
		return nil, userTeamError(teamUnavailable(ctx, "assignments", "project_state"))
	}
	var requestedRoles []launcherRole
	if choice, explicit := requested["guide"]; explicit {
		if !s.team.dependencies.HerdrAvailable || choice == nil {
			return nil, invalidWorkerChoice()
		}
		guide, found := guideRole(roles)
		if !found {
			title, message := roleFirstMessage("guide")
			guide = launcherRole{Role: "guide", Handle: teamName("guide", record.ID),
				Folder: record.Directory, Title: title, FirstMessage: message}
			roles = append([]launcherRole{guide}, roles...)
		}
		requestedRoles = append(requestedRoles, guide)
	}
	if assignments.BuildIdentity != nil && !assignments.Merged && !assignments.Dropped {
		for _, role := range roles {
			if assignments.HasWork(role.Role) {
				requestedRoles = append(requestedRoles, role)
			}
		}
	}
	choices, err := s.resolveChoices(ctx, requestedRoles, requested)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.Int("team.launch_roles", len(requestedRoles)))
	if len(requestedRoles) == 0 {
		return startResult(record, s.herdrMode(record.Tab), roles, nil, "No build workers need work."), nil
	}
	if !s.team.dependencies.HerdrAvailable {
		if err := s.saveChoices(ctx, choices); err != nil {
			return nil, err
		}
		if err := s.startHeadless(ctx, requestedRoles, choices, assignments); err != nil {
			return nil, userTeamError(err)
		}
		result := startResult(record, herdrUnavailable, roles, nil, "Build workers run in the background.")
		for _, role := range result.Roles {
			role.Running = s.team.headless.running(role.Role)
		}
		return result, nil
	}
	return s.startHerdrWorkers(ctx, record, roles, requestedRoles, choices)
}

// StopWorkers rechecks the current owner's terminal build and closes only the
// two owned worker roles. A missed or delayed call cannot stop a newer build.
func (s *Server) StopWorkers(ctx context.Context, build controlplane.BuildIdentity) error {
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("surla/project").Start(ctx, "surla.project.stop_workers")
	defer span.End()
	s.team.Lock()
	defer s.team.Unlock()
	if s.team.closed || ctx.Err() != nil || sameBuild(s.team.lastStopped, build) {
		return nil
	}
	current, err := s.readAssignments(ctx)
	if err != nil {
		return teamUnavailable(ctx, "read_assignments", "project_state")
	}
	if current.BuildIdentity == nil || !sameBuild(*current.BuildIdentity, build) ||
		!current.Merged && !current.Dropped || current.BuilderHasWork || current.TestWriterHasWork {
		return nil
	}
	if !s.team.dependencies.HerdrAvailable {
		err = s.stopHeadless(build)
	} else {
		err = s.stopHerdrWorkers(ctx)
	}
	if err != nil {
		cause := "herdr"
		if !s.team.dependencies.HerdrAvailable {
			cause = "project_state"
		}
		return teamUnavailable(ctx, "stop_workers", cause)
	}
	s.team.lastStopped = build
	return nil
}

// Close drains server-managed workers before the composition root closes the
// assignment/store owner. Interactive Herdr panes have their own lifetime.
func (s *Server) Close() error {
	s.team.Lock()
	defer s.team.Unlock()
	return s.closeHeadless()
}

// workersStep is the management read's stable next action.
const workersStep = "Start build workers from the Surla page."

// startHerdrWorkers rereads the active team, uses an attached guide as a
// reference, and passes only missing worker choices to Chorus.
func (s *Server) startHerdrWorkers(
	ctx context.Context, record Record, roles, workers []launcherRole,
	choices map[string]*gensurla.RoleAgentChoice,
) (*gensurla.ProjectTeam, error) {
	host := s.team.dependencies.Host
	connection, err := s.team.dependencies.Connect(ctx)
	if err != nil {
		return nil, userTeamError(teamUnavailable(ctx, "chorus_status", "chorus"))
	}
	explicitGuide := choices["guide"] != nil
	if explicitGuide {
		guide, _ := guideRole(roles)
		if connection.Observer == nil {
			return nil, userTeamError(teamUnavailable(ctx, "guide_watcher", "chorus"))
		}
		watchers, err := connection.Observer.ReadWatchers(ctx, record.Team, "", []TeamRole{{Role: "guide", Handle: guide.Handle}})
		if err != nil {
			return nil, userTeamError(teamUnavailable(ctx, "guide_watcher", "chorus"))
		}
		if watchers.Held[guide.Handle] {
			return nil, guideAlreadyRunning()
		}
	}
	space, err := s.workerSpace(ctx, record)
	if err != nil {
		return nil, userTeamError(err)
	}
	live, err := s.liveTeam(ctx, record, space, roles)
	if err != nil {
		return nil, userTeamError(teamUnavailable(ctx, "terminals", "herdr"))
	}
	var rosterRoles []launcherRole
	tab := ""
	if guide, ok := live["guide"]; ok {
		if explicitGuide {
			return nil, guideAlreadyRunning()
		}
		tab = guide.tab
		role, _ := guideRole(roles)
		rosterRoles = append(rosterRoles, role)
	}
	if explicitGuide {
		guide, _ := guideRole(roles)
		if _, err := s.registerAgent(guide.Handle, agentRecord{Role: "guide", Folder: guide.Folder}); err != nil {
			return nil, userTeamError(teamUnavailable(ctx, "register_guide", "project_state"))
		}
	}
	if err := s.saveChoices(ctx, choices); err != nil {
		return nil, err
	}
	rosterRoles = append(rosterRoles, workers...)
	roster, err := teamRoster(record, rosterRoles)
	if err != nil {
		return nil, userTeamError(teamUnavailable(ctx, "roster", "role_folder"))
	}
	if err := s.writeTeamRoster(ctx, space.ID, roster); err != nil {
		return nil, userTeamError(err)
	}
	if err := s.saveTeamLocation(record.Team, space.ID, tab); err != nil {
		return nil, userTeamError(teamUnavailable(ctx, "save_team", "project_state"))
	}
	if safeSpaceID(record.Space) && record.Space != space.ID {
		oldPath := filepath.Join(s.team.dependencies.RosterDirectory, record.Space+".json")
		if err := os.Remove(oldPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, userTeamError(teamUnavailable(ctx, "remove_roster", "roster_folder"))
		}
	}
	s.setTeamConnection(connection)
	missing := make(map[string]*gensurla.RoleAgentChoice, len(choices))
	for role, choice := range choices {
		if _, running := live[role]; !running {
			missing[role] = choice
		}
	}
	if len(missing) > 0 {
		if err := host.LaunchWorkers(ctx, space.ID, missing); err != nil {
			return nil, userTeamError(teamUnavailable(ctx, "launch_workers", "chorus"))
		}
		// Fresh observations confirm which processes reached a live model pane.
		if spaces, err := host.Spaces(ctx); err == nil {
			for _, actual := range spaces {
				if actual.ID == space.ID && actual.Machine == nil {
					if observed, err := s.liveTeam(ctx, record, actual, roles); err == nil {
						live = observed
					}
					break
				}
			}
		}
	}
	if guide, ok := live["guide"]; ok && explicitGuide {
		tab = guide.tab
		if err := s.saveTeamLocation(record.Team, space.ID, tab); err != nil {
			return nil, userTeamError(teamUnavailable(ctx, "save_guide_tab", "project_state"))
		}
	}
	record.Space, record.Tab = space.ID, tab
	return startResult(record, s.herdrMode(tab), roles, live, "Build workers were requested in Herdr."), nil
}

// workerSpace reuses the saved local space or finds its unique named replacement.
func (s *Server) workerSpace(ctx context.Context, record Record) (TeamSpace, error) {
	host := s.team.dependencies.Host
	spaces, err := host.Spaces(ctx)
	if err != nil {
		return TeamSpace{}, teamUnavailable(ctx, "spaces", "herdr")
	}
	var matched TeamSpace
	matches := 0
	for _, space := range spaces {
		if space.Machine != nil {
			continue
		}
		if record.Space != "" && space.ID == record.Space {
			if !safeSpaceID(space.ID) {
				return TeamSpace{}, teamUnavailable(ctx, "saved_space", "saved_space")
			}
			return space, nil
		}
		if safeSpaceID(space.ID) && space.Name == record.Team && space.Folder == record.Directory {
			matched, matches = space, matches+1
		}
	}
	if matches > 1 {
		return TeamSpace{}, &controlplane.TeamSpaceConflict{Count: matches}
	}
	if matches == 1 {
		return matched, nil
	}
	created, err := host.CreateSpace(ctx, record.Team, record.Directory)
	if err != nil || !safeSpaceID(created.ID) || created.Machine != nil {
		return TeamSpace{}, teamUnavailable(ctx, "create_space", "herdr")
	}
	return created, nil
}

// liveTeam accepts only exact registered role labels on model panes and a
// single team tab. Unclear terminal ownership prevents launching a duplicate.
func (s *Server) liveTeam(ctx context.Context, record Record, space TeamSpace, roles []launcherRole) (map[string]liveRole, error) {
	handles := make([]string, 0, len(roles))
	for _, role := range roles {
		handles = append(handles, role.Handle)
	}
	terminals, err := s.team.dependencies.Host.Terminals(ctx, record.Directory, space.ID, handles)
	if err != nil {
		return nil, err
	}
	live := make(map[string]liveRole, len(roles))
	teamTab := ""
	for _, terminal := range terminals {
		if terminal.Labels["chorus.project"] != record.Directory || terminal.Labels["herdr.space"] != space.ID {
			continue
		}
		if terminal.State != "live" {
			continue
		}
		tab, pane := modelPane(space, terminal)
		if pane == "" {
			continue
		}
		if terminal.Labels["chorus.swarm"] != record.Team || teamTab != "" && teamTab != tab {
			return nil, errors.New("Herdr labels identify a conflicting team")
		}
		teamTab = tab
		if !roleRunning(terminal, s.team.dependencies.Shell) {
			continue
		}
		for _, role := range roles {
			if terminal.Labels["chorus.handle"] != role.Handle {
				continue
			}
			if terminal.Labels["chorus.role"] != role.Role || terminal.Labels["chorus.agent"] == "" || live[role.Role].pane != "" {
				return nil, errors.New("Herdr labels do not identify one registered role")
			}
			live[role.Role] = liveRole{tab: tab, pane: pane}
		}
	}
	return live, nil
}

// stopHerdrWorkers resolves exact pane identities from owned labels. It
// rechecks fresh terminal labels immediately before asking Herdr to close
// each pane and never includes the guide in the lookup.
func (s *Server) stopHerdrWorkers(ctx context.Context) error {
	record, roles := s.currentTeam()
	if !safeSpaceID(record.Space) {
		return nil
	}
	workers := slices.DeleteFunc(roles, func(role launcherRole) bool { return !workerRole(role.Role) })
	if len(workers) == 0 {
		return nil
	}
	host := s.team.dependencies.Host
	spaces, err := host.Spaces(ctx)
	if err != nil {
		return err
	}
	var space TeamSpace
	for _, candidate := range spaces {
		if candidate.ID == record.Space && candidate.Machine == nil {
			space = candidate
			break
		}
	}
	if space.ID == "" {
		return nil
	}
	handles := make([]string, 0, len(workers))
	for _, worker := range workers {
		handles = append(handles, worker.Handle)
	}
	terminals, err := host.Terminals(ctx, record.Directory, space.ID, handles)
	if err != nil {
		return err
	}
	teamTab := record.Tab
	for _, terminal := range terminals {
		for _, worker := range workers {
			if !ownsWorker(record, terminal, worker) {
				continue
			}
			tab, pane := modelPane(space, terminal)
			if pane == "" {
				continue
			}
			if teamTab != "" && tab != teamTab {
				return errors.New("worker panes span several team tabs")
			}
			teamTab = tab
		}
	}
	var failures error
	for _, terminal := range terminals {
		for _, worker := range workers {
			if !ownsWorker(record, terminal, worker) {
				continue
			}
			tab, pane := modelPane(space, terminal)
			if pane == "" || tab != teamTab {
				continue
			}
			fresh, err := host.ReadTerminal(ctx, terminal.Address)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			if fresh.Address == terminal.Address && ownsWorker(record, fresh, worker) && fresh.Labels["herdr.tab"] == tab {
				failures = errors.Join(failures, host.ClosePane(ctx, pane))
			}
		}
	}
	return failures
}

// modelPane ties label claims to Herdr's actual space/tab/pane membership.
func modelPane(space TeamSpace, terminal TeamTerminal) (string, string) {
	for _, tab := range space.Tabs {
		if tab.ID != terminal.Labels["herdr.tab"] {
			continue
		}
		for _, pane := range tab.Terminals {
			if pane.Terminal == terminal.Address && pane.Pane != "" {
				return tab.ID, pane.Pane
			}
		}
	}
	return "", ""
}

// ownsWorker requires all owner labels that Chorus writes for a roster role.
func ownsWorker(record Record, terminal TeamTerminal, worker launcherRole) bool {
	labels := terminal.Labels
	return terminal.Address != "" && labels["chorus.project"] == record.Directory &&
		labels["chorus.swarm"] == record.Team && labels["herdr.space"] == record.Space &&
		labels["chorus.handle"] == worker.Handle && labels["chorus.role"] == worker.Role &&
		labels["chorus.agent"] != ""
}

// currentTeam copies persisted role and routing facts while concurrent auth
// and preference operations may be active.
func (s *Server) currentTeam() (Record, []launcherRole) {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	record := s.state.Record
	if record.Team == "" {
		record.Team = teamName(filepath.Base(record.Directory), record.ID)
	}
	return record, registeredTeamRoles(record.Agents)
}

// teamRoster constructs Chorus's closed schema with globally resolved MCP
// registration. It does not place a worker to the right when no guide is running.
func teamRoster(record Record, roles []launcherRole) (launcherRoster, error) {
	roster := launcherRoster{Root: record.Directory, Swarm: record.Team, Roles: make([]launcherRole, 0, len(roles))}
	for _, role := range roles {
		info, err := os.Stat(role.Folder)
		if err != nil || !filepath.IsAbs(role.Folder) || !info.IsDir() {
			return launcherRoster{}, errors.New("a role working folder cannot be used")
		}
		if workerRole(role.Role) {
			role.FirstMessage += " Keep your Chorus watcher running. Surla messages you there when you have an unread assignment; read your assignments when asked."
		}
		if i := len(roster.Roles); i > 0 {
			direction := "down"
			if i == 1 && roster.Roles[0].Role == "guide" && role.Role == "builder" {
				direction = "right"
			}
			role.Place = &launcherPlace{Beside: roster.Roles[i-1].Handle, Direction: direction}
		}
		roster.Roles = append(roster.Roles, role)
	}
	return roster, nil
}

// writeTeamRoster publishes one whole roster for the exact local space.
func (s *Server) writeTeamRoster(ctx context.Context, space string, roster launcherRoster) error {
	dir := s.team.dependencies.RosterDirectory
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return teamUnavailable(ctx, "roster_directory", "roster_folder")
	}
	if err := writeJSON(filepath.Join(dir, space+".json"), roster); err != nil {
		return teamUnavailable(ctx, "write_roster", "roster_folder")
	}
	return nil
}

// startResult projects registered actors and current launch observations.
func startResult(record Record, mode string, roles []launcherRole, live map[string]liveRole, step string) *gensurla.ProjectTeam {
	result := &gensurla.ProjectTeam{
		Team: record.Team, Space: localSpace(record.Space), HerdrMode: mode, Step: step,
		Roles: make([]*gensurla.ProjectTeamRole, 0, len(roles)),
	}
	for _, role := range roles {
		_, running := live[role.Role]
		result.Roles = append(result.Roles, &gensurla.ProjectTeamRole{Role: role.Role, Handle: role.Handle, Running: running})
	}
	return result
}

// localSpace sanitizes empty or obsolete IDs that cannot name a local space.
func localSpace(space string) string {
	if !safeSpaceID(space) {
		return ""
	}
	return space
}

// guideRole selects the sole registered or explicitly requested guide row.
func guideRole(roles []launcherRole) (launcherRole, bool) {
	for _, role := range roles {
		if role.Role == "guide" {
			return role, true
		}
	}
	return launcherRole{}, false
}

// guideAlreadyRunning preserves the active guide pane or external session.
func guideAlreadyRunning() error {
	return gensurla.MakeUnprocessable(errors.New("the current guide session is already running"))
}

// activeTeamState describes snapshots whose states establish a running agent.
func activeTeamState(state string) bool {
	switch state {
	case "starting", "needs_you", "working", "waiting":
		return true
	}
	return false
}

// sameBuild compares the saved change and timestamp, regardless of time location.
func sameBuild(a, b controlplane.BuildIdentity) bool {
	return a.ChangeID == b.ChangeID && a.StartedAt.Equal(b.StartedAt)
}
