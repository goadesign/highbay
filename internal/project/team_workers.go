// This file starts and stops the team's coding agents in herdr-core
// terminals: the guide and a build's workers.
package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// StartAgents starts the agents that are missing: the guide when the caller
// names a choice for it, and each worker role that has work on the open
// build. It never replaces a running agent: a guide choice for a running
// guide is refused, and a running worker is left alone. The control plane
// authorizes the user once. A disconnected request can repeat the operation.
func (s *Server) StartAgents(ctx context.Context, requested map[string]*genhighbay.RoleAgentChoice) (*genhighbay.ProjectTeam, error) {
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.start_agents")
	defer span.End()
	s.team.Lock()
	defer s.team.Unlock()
	if s.team.closed || ctx.Err() != nil {
		return nil, userTeamError(teamUnavailable(ctx, "lifetime", "stopping"))
	}
	record, roles := s.currentTeam()
	assignments, err := s.readAssignments(ctx)
	if err != nil {
		return nil, userTeamError(stateUnavailable(ctx, "assignments", err))
	}
	var wanted []teamRole
	choice, explicitGuide := requested["guide"]
	if explicitGuide {
		if choice == nil {
			return nil, invalidWorkerChoice()
		}
		wanted = append(wanted, s.guide(record, roles))
	}
	if assignments.BuildIdentity != nil && !assignments.Merged && !assignments.Dropped {
		for _, role := range roles {
			if workerRole(role.Role) && assignments.HasWork(role.Role) {
				wanted = append(wanted, role)
			}
		}
	}
	choices, err := s.resolveChoices(ctx, wanted, requested)
	if err != nil {
		return nil, err
	}
	if len(wanted) == 0 {
		return s.teamResult(record, roles, "No build workers need work."), nil
	}
	if err := s.startRoles(ctx, record, wanted, choices, explicitGuide); err != nil {
		return nil, err
	}
	record, roles = s.currentTeam()
	return s.teamResult(record, roles, "The team's agents are starting. Follow them on the Highbay page."), nil
}

// StopWorkers ends the worker terminals of a build that was merged or
// dropped. It does nothing for any other build, for a build whose workers it
// already stopped, or while a worker still has work.
func (s *Server) StopWorkers(ctx context.Context, build controlplane.BuildIdentity) error {
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.stop_workers")
	defer span.End()
	s.team.Lock()
	defer s.team.Unlock()
	if s.team.closed || sameBuild(s.team.lastStopped, build) {
		return nil
	}
	assignments, err := s.readAssignments(ctx)
	if err != nil {
		return stateUnavailable(ctx, "assignments", err)
	}
	current := assignments.BuildIdentity
	if current == nil || !sameBuild(*current, build) || !assignments.Merged && !assignments.Dropped ||
		assignments.BuilderHasWork || assignments.TestWriterHasWork {
		return nil
	}
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return teamUnavailable(ctx, "dial_terminals", "hc")
	}
	for _, role := range []string{"builder", "test_writer"} {
		if _, _, err := s.removeManagedRoleTerminals(ctx, client, role); err != nil {
			return teamUnavailable(ctx, "stop_workers", "hc")
		}
	}
	s.team.lastStopped = build
	return nil
}

// InterruptRole presses the stop key in the role's running terminal. It
// returns role_not_running when the role has no running terminal.
func (s *Server) InterruptRole(ctx context.Context, role string) error {
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.interrupt_role")
	defer span.End()
	span.SetAttributes(attribute.String("team.role", role))
	if s.teamClosed() || ctx.Err() != nil {
		return userTeamError(teamUnavailable(ctx, "lifetime", "stopping"))
	}
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "dial_terminals", "hc"))
	}
	terminals, err := s.listRoleTerminals(ctx, client, role)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "list_terminals", "hc"))
	}
	for _, terminal := range terminals {
		if terminal.State != hc.StateLive {
			continue
		}
		_, err := client.Interrupt(ctx, terminal.Address)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, hc.ErrFinished), errors.Is(err, hc.ErrNotFound):
			// It ended between the list and the key press.
		default:
			return userTeamError(teamUnavailable(ctx, "interrupt", "hc"))
		}
	}
	return genhighbay.MakeRoleNotRunning(errors.New("that role has no running terminal to interrupt"))
}

// StopRole ends and removes the terminal Highbay started for role, so the
// role can be started again. It returns role_not_running when Highbay runs no
// terminal for the role; a session the user attached is theirs to end, and
// is left exactly as it is.
func (s *Server) StopRole(ctx context.Context, role string) error {
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.stop_role")
	defer span.End()
	span.SetAttributes(attribute.String("team.role", role))
	s.team.Lock()
	defer s.team.Unlock()
	if s.team.closed || ctx.Err() != nil {
		return userTeamError(teamUnavailable(ctx, "lifetime", "stopping"))
	}
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "dial_terminals", "hc"))
	}
	removed, _, err := s.removeManagedRoleTerminals(ctx, client, role)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "stop_role", "hc"))
	}
	if removed == 0 {
		return genhighbay.MakeRoleNotRunning(errors.New("Highbay runs no terminal for that role"))
	}
	return nil
}

// RestartRole ends the terminal Highbay started for role, if any, and starts
// the role again in a new one, keeping its handle and credential. The coding
// agent is requested, else the role's saved choice, else an installed
// default. A role whose terminal already ended, or that never started, is
// simply started.
//
// It returns role_not_running, and changes nothing, when the role runs in a
// session Highbay did not start (a terminal the user attached, or a guide
// that is present in Chorus without a terminal) or is a worker no build has
// registered. The choice is resolved and the coding client's settings are
// checked before the running terminal is ended, so a refusal leaves the
// role running.
func (s *Server) RestartRole(ctx context.Context, role string, requested *genhighbay.RoleAgentChoice) error {
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.restart_role")
	defer span.End()
	span.SetAttributes(attribute.String("team.role", role))
	s.team.Lock()
	defer s.team.Unlock()
	if s.team.closed || ctx.Err() != nil {
		return userTeamError(teamUnavailable(ctx, "lifetime", "stopping"))
	}
	record, roles := s.currentTeam()
	var target teamRole
	if role == "guide" {
		target = s.guide(record, roles)
	} else if at := slices.IndexFunc(roles, func(registered teamRole) bool { return registered.Role == role }); at >= 0 {
		target = roles[at]
	} else {
		return genhighbay.MakeRoleNotRunning(errors.New("that role is not part of this project's team yet; it joins when a build starts"))
	}
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "dial_terminals", "hc"))
	}
	terminals, err := s.listRoleTerminals(ctx, client, role)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "list_terminals", "hc"))
	}
	attached := slices.ContainsFunc(terminals, func(terminal hc.Terminal) bool {
		return terminal.Labels[labelManaged] != "true" && terminal.State == hc.StateLive
	})
	if attached || role == "guide" && len(terminals) == 0 && s.team.observed.guidePresent(s.now(), target.Handle) {
		return genhighbay.MakeRoleNotRunning(errors.New("that role runs in a session Highbay did not start; restart it there"))
	}
	explicit := map[string]*genhighbay.RoleAgentChoice{}
	if requested != nil {
		explicit[role] = requested
	}
	choices, err := s.resolveChoices(ctx, []teamRole{target}, explicit)
	if err != nil {
		return err
	}
	if err := s.prepareWorkerTrust(ctx, record, []teamRole{target}, choices, true); err != nil {
		return err
	}
	if _, _, err := s.removeManagedRoleTerminals(ctx, client, role); err != nil {
		return userTeamError(teamUnavailable(ctx, "stop_role", "hc"))
	}
	return s.startRoles(ctx, record, []teamRole{target}, choices, false)
}

// Close ends the connection to herdr-core and refuses further team work. The
// agents keep running in their terminals: they belong to the user's session
// with herdr-core, not to this server process.
func (s *Server) Close() error {
	s.team.Lock()
	defer s.team.Unlock()
	s.team.closed = true
	return s.team.terminals.close()
}

// roleLabels are the labels that make a terminal stand for role: Highbay's
// own, which the supervisor follows, and Chorus's, which let Chorus keep the
// agent present and wake it at a turn boundary.
func roleLabels(record Record, role teamRole) map[string]string {
	return map[string]string{
		labelProject:     record.ID,
		labelRole:        role.Role,
		"chorus.project": record.Directory,
		"chorus.swarm":   record.Team,
		"chorus.role":    role.Role,
		"chorus.handle":  role.Handle,
	}
}

// clearedRoleLabels removes every label roleLabels sets: herdr-core deletes a
// label that is set to the empty string.
func clearedRoleLabels() map[string]string {
	return map[string]string{
		labelProject: "", labelRole: "", "chorus.project": "", "chorus.swarm": "", "chorus.role": "", "chorus.handle": "",
	}
}

// guideAlreadyRunning preserves the running guide session.
func guideAlreadyRunning() error {
	return genhighbay.MakeUnprocessable(errors.New("the current guide session is already running"))
}

// sameBuild compares the saved change and timestamp, regardless of time location.
func sameBuild(a, b controlplane.BuildIdentity) bool {
	return a.ChangeID == b.ChangeID && a.StartedAt.Equal(b.StartedAt)
}

// guide returns the registered guide, or the guide this project would
// register: its handle is derived from the project, and it works in the
// project folder.
func (s *Server) guide(record Record, roles []teamRole) teamRole {
	if guide, found := guideRole(roles); found {
		return guide
	}
	return teamRole{Role: "guide", Handle: teamName("guide", record.ID), Folder: record.Directory}
}

// startRoles starts a terminal for each wanted role that has no running
// agent. choices names the coding agent for each wanted role. With
// refuseRunningGuide, a running guide is an error instead of being left
// alone. The team lock is held.
func (s *Server) startRoles(
	ctx context.Context, record Record, wanted []teamRole,
	choices map[string]*genhighbay.RoleAgentChoice, refuseRunningGuide bool,
) error {
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "dial_terminals", "hc"))
	}
	var missing []teamRole
	for _, role := range wanted {
		terminals, err := s.listRoleTerminals(ctx, client, role.Role)
		if err != nil {
			return userTeamError(teamUnavailable(ctx, "list_terminals", "hc"))
		}
		running := slices.ContainsFunc(terminals, func(terminal hc.Terminal) bool {
			return hc.RoleRunning(terminal, s.team.dependencies.Shell)
		})
		// A guide session outside herdr-core has no terminal; Chorus is
		// the only place it shows.
		running = running || role.Role == "guide" && len(terminals) == 0 && s.team.observed.guidePresent(s.now(), role.Handle)
		switch {
		case running && role.Role == "guide" && refuseRunningGuide:
			return guideAlreadyRunning()
		case !running:
			missing = append(missing, role)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if err := s.prepareWorkerTrust(ctx, record, missing, choices, true); err != nil {
		return err
	}
	if err := s.saveTeamName(ctx, record.Team); err != nil {
		return userTeamError(err)
	}
	started := make(map[string]*genhighbay.RoleAgentChoice, len(missing))
	for _, role := range missing {
		if role.Role == "guide" {
			if _, err := s.registerAgent(role.Handle, agentRecord{Role: role.Role, Folder: role.Folder}); err != nil {
				return userTeamError(stateUnavailable(ctx, "register_guide", err))
			}
		}
		started[role.Role] = choices[role.Role]
	}
	if err := s.saveChoices(ctx, started); err != nil {
		return err
	}
	for _, role := range missing {
		if err := s.startRole(ctx, client, record, role, choices[role.Role]); err != nil {
			return err
		}
	}
	return nil
}

// startRole clears what is left of the role's earlier terminals and starts a
// new one running the chosen coding agent in the role's folder. The terminal
// has no parent and outlives this server, so the agent keeps working across a
// server restart.
func (s *Server) startRole(
	ctx context.Context, client TeamTerminals, record Record, role teamRole, choice *genhighbay.RoleAgentChoice,
) error {
	info, err := os.Stat(role.Folder)
	if err != nil || !filepath.IsAbs(role.Folder) || !info.IsDir() {
		return userTeamError(teamUnavailable(ctx, "role_folder", "role_folder"))
	}
	if err := s.replaceRoleTerminals(ctx, client, role.Role, ""); err != nil {
		return userTeamError(teamUnavailable(ctx, "clear_role", "hc"))
	}
	argv, err := s.team.dependencies.AgentCommand(choice, roleFirstMessage(role, record.Team))
	if err != nil {
		return invalidWorkerChoice()
	}
	labels := roleLabels(record, role)
	labels[labelManaged] = "true"
	_, err = client.Run(ctx, hc.RunRequest{
		Argv:          argv,
		Cwd:           role.Folder,
		Env:           s.team.dependencies.AgentEnv,
		Labels:        labels,
		NoParent:      true,
		OutliveParent: true,
		NoNotify:      true,
	})
	if err != nil {
		trace.SpanFromContext(ctx).SetAttributes(attribute.String("team.role", role.Role))
		return userTeamError(teamUnavailable(ctx, "run_terminal", "hc"))
	}
	if role.Role == "guide" {
		s.team.observed.guideBegan()
	}
	return nil
}

// listRoleTerminals asks herdr-core for this project's terminals labeled for
// role, finished ones included. It asks the core rather than the supervisor's
// view so a start or stop decides on the core's state at that moment.
func (s *Server) listRoleTerminals(ctx context.Context, client TeamTerminals, role string) ([]hc.Terminal, error) {
	return client.List(ctx, hc.ListRequest{All: true, Labels: map[string]string{
		labelProject: s.state.Record.ID,
		labelRole:    role,
	}})
}

// replaceRoleTerminals makes the terminal at keep the only one that stands
// for role, or none when keep is empty: every other terminal Highbay started
// for the role is ended and removed, and a terminal the user attached keeps
// running but loses the role's labels. The terminal at keep is not touched.
func (s *Server) replaceRoleTerminals(ctx context.Context, client TeamTerminals, role, keep string) error {
	terminals, err := s.listRoleTerminals(ctx, client, role)
	if err != nil {
		return err
	}
	for _, terminal := range terminals {
		switch {
		case terminal.Address == keep:
		case terminal.Labels[labelManaged] == "true":
			if err := s.removeTerminal(ctx, client, terminal); err != nil {
				return err
			}
		default:
			if _, err := client.Label(ctx, terminal.Address, clearedRoleLabels()); err != nil && !errors.Is(err, hc.ErrNotFound) {
				return err
			}
		}
	}
	return nil
}

// removeManagedRoleTerminals ends and removes the terminals Highbay started
// for role and reports how many it removed. It touches no other terminal: a
// session the user attached keeps running and keeps its labels. attached
// reports whether such a session is live. Ending the guide's terminal is
// recorded, so the guide's last activity in Chorus stops counting.
func (s *Server) removeManagedRoleTerminals(
	ctx context.Context, client TeamTerminals, role string,
) (removed int, attached bool, err error) {
	terminals, err := s.listRoleTerminals(ctx, client, role)
	if err != nil {
		return 0, false, err
	}
	for _, terminal := range terminals {
		if terminal.Labels[labelManaged] != "true" {
			attached = attached || terminal.State == hc.StateLive
			continue
		}
		if err := s.removeTerminal(ctx, client, terminal); err != nil {
			return removed, attached, err
		}
		removed++
	}
	if role == "guide" && removed > 0 {
		s.team.observed.guideEnded(s.now())
	}
	return removed, attached, nil
}

// removeTerminal ends terminal's program when it still runs and removes the
// terminal. One that is already gone is not an error.
func (s *Server) removeTerminal(ctx context.Context, client TeamTerminals, terminal hc.Terminal) error {
	if terminal.State == hc.StateLive {
		if _, err := client.Kill(ctx, terminal.Address); err != nil && !errors.Is(err, hc.ErrNotFound) {
			return err
		}
	}
	if err := client.Remove(ctx, terminal.Address); err != nil && !errors.Is(err, hc.ErrNotFound) {
		return err
	}
	return nil
}

// teamClosed reports whether the server closed its team.
func (s *Server) teamClosed() bool {
	s.team.Lock()
	defer s.team.Unlock()
	return s.team.closed
}

// prepareWorkerTrust checks explicit client restrictions and pre-populates
// missing user-scoped folder trust for the project root and each role folder
// about to launch. Any explicit policy restriction or unusable client
// configuration returns a typed refusal before any terminal is started.
func (s *Server) prepareWorkerTrust(
	ctx context.Context,
	record Record,
	roles []teamRole,
	choices map[string]*genhighbay.RoleAgentChoice,
	requireChorus bool,
) error {
	prepare := s.team.dependencies.PrepareWorkerTrust
	if prepare == nil || len(roles) == 0 {
		return nil
	}
	var targets []WorkerTrustTarget
	for _, role := range roles {
		choice := choices[role.Role]
		if choice == nil || choice.Client == "" || role.Folder == "" {
			continue
		}
		target := WorkerTrustTarget{
			Role:          role.Role,
			ProjectDir:    record.Directory,
			Folder:        role.Folder,
			Client:        choice.Client,
			Yolo:          choice.Yolo,
			RequireChorus: requireChorus,
		}
		if !slices.Contains(targets, target) {
			targets = append(targets, target)
		}
	}
	if len(targets) == 0 {
		return nil
	}
	if err := prepare(ctx, targets); err != nil {
		var policyErr *controlplane.TeamPolicyRefusal
		if errors.As(err, &policyErr) {
			return policyErr
		}
		if ctx.Err() != nil || s.team.closed {
			return userTeamError(teamUnavailable(ctx, "worker_trust", "stopping"))
		}
		trace.SpanFromContext(ctx).AddEvent("team.worker_trust_failed")
		return &controlplane.TeamPolicyRefusal{Kind: controlplane.PolicyConfigUnusable}
	}
	return nil
}
