// This file attaches the coding session that invoked Highbay as the
// project's guide.
package project

import (
	"context"
	"errors"
	"slices"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/abspath"
	"github.com/goadesign/highbay/internal/hc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

// AttachGuide registers the calling coding session as this project's guide
// and returns the team name and the guide's Chorus handle.
//
// When the session runs inside a herdr-core terminal in the project folder,
// named by its terminal ID or found by the session's process IDs, Highbay
// labels that terminal as the guide: the page can then open it, and Chorus
// can wake it. Such a terminal is the user's own; Highbay never ends it. A
// session anywhere else, such as an editor, is registered without a
// terminal, and the page learns that it is present from Chorus alone.
func (s *Server) AttachGuide(ctx context.Context, p *genproject.AttachGuidePayload) (*genproject.AttachGuideResult, error) {
	if err := managementOnly(ctx, "attach_guide"); err != nil {
		return nil, err
	}
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.attach_guide")
	defer span.End()
	s.team.Lock()
	defer s.team.Unlock()
	if s.team.closed || ctx.Err() != nil {
		return nil, teamUnavailable(ctx, "lifetime", "stopping")
	}
	record, roles := s.currentTeam()
	guide := s.guide(record, roles)
	if _, err := s.registerAgent(guide.Handle, agentRecord{Role: "guide", Folder: record.Directory}); err != nil {
		return nil, stateUnavailable(ctx, "register_guide", err)
	}
	if err := s.saveTeamName(ctx, record.Team); err != nil {
		return nil, err
	}
	attached := false
	if p.HcTerminalID != nil || len(p.AncestorPids) > 0 {
		var err error
		if attached, err = s.labelGuideTerminal(ctx, record, guide, p); err != nil {
			return nil, teamUnavailable(ctx, "label_guide", "hc")
		}
	}
	if !attached {
		s.forgetEndedGuideTerminals(ctx)
	}
	s.team.observed.guideBegan()
	span.SetAttributes(attribute.Bool("team.guide_terminal", attached))
	return &genproject.AttachGuideResult{Team: record.Team, Handle: guide.Handle, HasTerminal: attached}, nil
}

// guideSessionTerminal reports whether terminal is the one the calling
// session runs in: it is named by the session's terminal ID, or its program
// or foreground process is one of the session's processes, and it works in
// the project folder or below it.
func guideSessionTerminal(terminal hc.Terminal, directory string, p *genproject.AttachGuidePayload) bool {
	named := p.HcTerminalID != nil && (*p.HcTerminalID == terminal.Address || *p.HcTerminalID == terminal.Name)
	owned := slices.Contains(p.AncestorPids, int(terminal.PID)) && terminal.PID != 0 ||
		terminal.Foreground != nil && slices.Contains(p.AncestorPids, int(terminal.Foreground.PID))
	if p.HcTerminalID != nil && !named || p.HcTerminalID == nil && !owned {
		return false
	}
	cwd := terminal.Cwd
	if terminal.Foreground != nil && terminal.Foreground.Cwd != "" {
		cwd = terminal.Foreground.Cwd
	}
	// The two paths may spell one folder differently (a symbolic link such
	// as /tmp, or another letter case), so they are compared as resolved.
	within, err := abspath.Within(directory, cwd)
	return err == nil && within
}

// labelGuideTerminal finds the herdr-core terminal the calling session runs
// in and labels it as the guide. It reports false, without an error, when
// the session runs in no terminal of this project folder or herdr-core
// cannot be reached: the session is then a guide without a terminal. Other
// terminals that stood for the guide stop doing so.
func (s *Server) labelGuideTerminal(
	ctx context.Context, record Record, guide teamRole, p *genproject.AttachGuidePayload,
) (bool, error) {
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		// Without herdr-core the session cannot be inside one of its terminals.
		return false, nil
	}
	live, err := client.List(ctx, hc.ListRequest{})
	if err != nil {
		return false, err
	}
	var found []hc.Terminal
	for _, terminal := range live {
		if guideSessionTerminal(terminal, record.Directory, p) {
			found = append(found, terminal)
		}
	}
	if len(found) != 1 {
		// None, or several that cannot be told apart: label nothing.
		return false, nil
	}
	// The session's own terminal is kept: a guide Highbay started calls
	// this from inside the terminal that already stands for the guide.
	if err := s.replaceRoleTerminals(ctx, client, "guide", found[0].Address); err != nil {
		return false, err
	}
	if _, err := client.Label(ctx, found[0].Address, roleLabels(record, guide)); err != nil {
		return false, err
	}
	return true, nil
}

// forgetEndedGuideTerminals removes the guide's terminals whose program has
// ended, after a guide session attached from outside herdr-core. An ended
// terminal would otherwise make the guide look ended although the attached
// session is at work. It is best effort: when herdr-core cannot be asked,
// the terminals stay until the next start or attach.
func (s *Server) forgetEndedGuideTerminals(ctx context.Context) {
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return
	}
	terminals, err := s.listRoleTerminals(ctx, client, "guide")
	if err != nil {
		return
	}
	for _, terminal := range terminals {
		if terminal.State == hc.StateLive {
			continue
		}
		if terminal.Labels[labelManaged] == "true" {
			err = s.removeTerminal(ctx, client, terminal)
		} else {
			_, err = client.Label(ctx, terminal.Address, clearedRoleLabels())
		}
		if err != nil && !errors.Is(err, hc.ErrNotFound) {
			return
		}
	}
}
