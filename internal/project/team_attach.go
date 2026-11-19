// This file attaches a guide without creating or replacing its coding session.
package project

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	genproject "github.com/goadesign/surla/gen/project"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

const (
	// These modes distinguish an adopted guide from worker panes and headless execution.
	herdrAttached    = "attached"
	herdrAvailable   = "available"
	herdrUnavailable = "unavailable"
)

type (
	// guidePane is a fresh local model-pane selection with compatible ownership labels.
	guidePane struct {
		space    string
		tab      string
		terminal string
		client   string
		swarm    string
	}
)

// AttachGuide registers the existing session as guide and adopts its model pane
// when fresh Herdr facts establish a compatible local tab. Registration also
// works with no Herdr session and never creates a second guide pane.
func (s *Server) AttachGuide(ctx context.Context, p *genproject.AttachGuidePayload) (*genproject.AttachGuideResult, error) {
	if err := managementOnly(ctx, "attach_guide"); err != nil {
		return nil, err
	}
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("surla/project").Start(ctx, "surla.project.attach_guide")
	defer span.End()
	s.team.Lock()
	defer s.team.Unlock()
	if ctx.Err() != nil || s.team.closed {
		return nil, teamUnavailable(ctx, "lifetime", "stopping")
	}

	s.state.mu.Lock()
	record := s.state.Record
	s.state.mu.Unlock()
	handle, err := s.registerAgent(teamName("guide", record.ID), agentRecord{Role: "guide", Folder: record.Directory})
	if err != nil {
		return nil, teamUnavailable(ctx, "register_guide", "project_state")
	}
	swarm := record.Team
	if swarm == "" {
		swarm = teamName(filepath.Base(record.Directory), record.ID)
	}
	space, tab := record.Space, record.Tab
	var adopted bool
	if s.team.dependencies.HerdrAvailable && p.HcTerminalID != nil && *p.HcTerminalID != "" {
		pane := s.findGuidePane(ctx, record, handle, *p.HcTerminalID, p.Client)
		if pane.terminal != "" {
			err := s.team.dependencies.Host.LabelGuide(ctx, pane.terminal, record.Directory, pane.swarm, pane.client, handle, pane.space, pane.tab)
			if err == nil {
				adopted = true
				space, tab, swarm = pane.space, pane.tab, pane.swarm
			}
		}
	}
	if err := s.saveTeamLocation(swarm, space, tab); err != nil {
		return nil, teamUnavailable(ctx, "save_team", "project_state")
	}
	mode := s.herdrMode(tab)
	if adopted {
		// A Chorus outage must not replace or disconnect the attached guide.
		if connection, err := s.team.dependencies.Connect(ctx); err == nil {
			s.setTeamConnection(connection)
		}
		s.openCompanion(ctx)
	} else if p.HcTerminalID != nil && *p.HcTerminalID != "" && mode != herdrUnavailable {
		// This caller has no adopted tab even if another guide already does.
		mode = herdrAvailable
	}
	span.SetAttributes(attribute.Bool("team.adopted_herdr", adopted), attribute.String("team.herdr_mode", mode))
	return &genproject.AttachGuideResult{Team: swarm, Handle: handle, AdoptedHerdr: adopted, HerdrMode: mode}, nil
}

// findGuidePane applies the live pane, local tab and conflict checks before any labeling.
func (s *Server) findGuidePane(ctx context.Context, record Record, handle, address string, client *string) guidePane {
	host := s.team.dependencies.Host
	spaces, err := host.Spaces(ctx)
	if err != nil {
		return guidePane{}
	}
	for _, space := range spaces {
		if space.Machine != nil || !safeSpaceID(space.ID) {
			continue
		}
		for _, tab := range space.Tabs {
			for _, pane := range tab.Terminals {
				if pane.Terminal == address && pane.Pane != "" && safeSpaceID(tab.ID) {
					return s.validateGuideTab(ctx, record, space, tab, handle, address, client)
				}
			}
		}
	}
	return guidePane{}
}

// validateGuideTab preserves any existing project/swarm and avoids replacing
// another live guide or splitting a single team across several tabs.
func (s *Server) validateGuideTab(
	ctx context.Context, record Record, space TeamSpace, tab TeamTab, handle, address string, client *string,
) guidePane {
	host := s.team.dependencies.Host
	target, err := host.ReadTerminal(ctx, address)
	if err != nil || target.Address != address || !roleRunning(target, s.team.dependencies.Shell) {
		return guidePane{}
	}
	if v := target.Labels["herdr.space"]; v != "" && v != space.ID {
		return guidePane{}
	}
	if v := target.Labels["herdr.tab"]; v != "" && v != tab.ID {
		return guidePane{}
	}
	if role := target.Labels["chorus.role"]; role != "" && role != "guide" {
		return guidePane{}
	}
	if owner := target.Labels["chorus.handle"]; owner != "" && owner != handle && target.Labels["chorus.project"] != "" {
		return guidePane{}
	}
	key := guideClient(target, client)
	if key == "" {
		return guidePane{}
	}
	swarm := record.Team
	if swarm == "" {
		swarm = target.Labels["chorus.swarm"]
		if swarm == "" {
			swarm = teamName(filepath.Base(record.Directory), record.ID)
		}
		if !validTeamName(swarm) {
			return guidePane{}
		}
	}
	if guideTabConflict(target, record.Directory, swarm) {
		return guidePane{}
	}

	for _, otherTab := range space.Tabs {
		for _, pane := range otherTab.Terminals {
			if pane.Terminal == address {
				continue
			}
			terminal, err := host.ReadTerminal(ctx, pane.Terminal)
			if err != nil || terminal.Address != pane.Terminal {
				return guidePane{}
			}
			if otherTab.ID != tab.ID {
				if terminal.Labels["chorus.project"] == record.Directory {
					return guidePane{}
				}
				continue
			}
			if guideTabConflict(terminal, record.Directory, swarm) ||
				terminal.Labels["chorus.handle"] == handle && roleRunning(terminal, s.team.dependencies.Shell) {
				return guidePane{}
			}
		}
	}
	if record.Space != "" && record.Space != space.ID && safeSpaceID(record.Space) {
		// Another space can still own this session or this build's workers.
		var handles []string
		for actor := range record.Agents {
			handles = append(handles, actor)
		}
		owned, err := host.Terminals(ctx, record.Directory, record.Space, handles)
		if err != nil {
			return guidePane{}
		}
		for _, terminal := range owned {
			if terminal.Labels["chorus.project"] == record.Directory &&
				terminal.Labels["herdr.space"] == record.Space && roleRunning(terminal, s.team.dependencies.Shell) {
				return guidePane{}
			}
		}
	}
	return guidePane{space: space.ID, tab: tab.ID, terminal: address, client: key, swarm: swarm}
}

// guideTabConflict protects the user's pane from a different project/swarm.
func guideTabConflict(t TeamTerminal, root, swarm string) bool {
	return t.Labels["chorus.project"] != "" && t.Labels["chorus.project"] != root ||
		t.Labels["chorus.swarm"] != "" && t.Labels["chorus.swarm"] != swarm
}

// guideClient normalizes the known local client aliases without guessing an
// unrecognized pane's agent program.
func guideClient(terminal TeamTerminal, client *string) string {
	var candidates []string
	if client != nil {
		candidates = append(candidates, *client)
	}
	candidates = append(candidates, terminal.Labels["chorus.agent"])
	if terminal.Foreground != nil && len(terminal.Foreground.Argv) > 0 {
		candidates = append(candidates, filepath.Base(terminal.Foreground.Argv[0]))
	}
	for _, candidate := range candidates {
		switch key := strings.ToLower(candidate); key {
		case "agy", "antigravity-cli":
			return "antigravity"
		case "agent", "cursor-agent":
			return "cursor"
		case "claude", "codex", "antigravity", "cursor", "gemini", "opencode":
			return key
		}
	}
	return ""
}

// openCompanion opens the installed overlay once. If the user already opened
// it, keep it visible and remember that it was shown. Later attaches respect a
// subsequent dismissal.
func (s *Server) openCompanion(ctx context.Context) {
	if !s.team.dependencies.CompanionInstalled {
		return
	}
	s.state.mu.Lock()
	shown := s.state.Record.CompanionShown
	s.state.mu.Unlock()
	if shown {
		return
	}
	host := s.team.dependencies.Host
	overlays, err := host.Overlays(ctx)
	if err != nil || (!slices.Contains(overlays, "surla") && host.OpenCompanion(ctx) != nil) {
		return
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	s.state.Record.CompanionShown = true
	if err := s.state.save(); err != nil {
		s.state.Record.CompanionShown = false
		trace.SpanFromContext(ctx).AddEvent("team.companion_preference_failed")
	}
}

// saveTeamLocation persists routing needed for the next launch or restart.
func (s *Server) saveTeamLocation(team, space, tab string) error {
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	record := &s.state.Record
	if record.Team == team && record.Space == space && record.Tab == tab {
		return nil
	}
	oldTeam, oldSpace, oldTab := record.Team, record.Space, record.Tab
	record.Team, record.Space, record.Tab = team, space, tab
	if err := s.state.save(); err != nil {
		record.Team, record.Space, record.Tab = oldTeam, oldSpace, oldTab
		return err
	}
	return nil
}

// herdrMode reports persisted attachment or the worker capability known at composition.
func (s *Server) herdrMode(tab string) string {
	if !s.team.dependencies.HerdrAvailable {
		return herdrUnavailable
	}
	if tab != "" {
		return herdrAttached
	}
	return herdrAvailable
}

// setTeamConnection publishes the same endpoint to the read-only supervisor.
func (s *Server) setTeamConnection(connection TeamConnection) {
	s.team.observationMu.Lock()
	defer s.team.observationMu.Unlock()
	s.team.connection = connection
}
