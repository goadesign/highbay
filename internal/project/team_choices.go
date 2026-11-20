// This file owns worker option admission and durable team preferences.
package project

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"unicode"

	gensurla "github.com/goadesign/surla/gen/surla"
)

type (
	// roleChoice is the project's persisted launch preference for one worker.
	roleChoice struct {
		Client   string  `json:"client"`
		Program  string  `json:"program"`
		Model    *string `json:"model,omitempty"`
		Thinking *string `json:"thinking,omitempty"`
		Yolo     bool    `json:"yolo"`
	}
)

// ListAgentOptions asks the installed clients for their current options on
// demand. Team snapshot reads never invoke discovery.
func (s *Server) ListAgentOptions(ctx context.Context) ([]*gensurla.InstalledAgentOption, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	discover := s.team.dependencies.DiscoverAgents
	if discover == nil {
		return nil, gensurla.MakeTeamUnavailable(errors.New("coding agent discovery is unavailable"))
	}
	options, err := discover(ctx)
	if err != nil {
		return nil, gensurla.MakeTeamUnavailable(errors.New("could not read installed coding agents"))
	}
	if options == nil {
		return []*gensurla.InstalledAgentOption{}, nil
	}
	return options, nil
}

// DismissNotice remembers one known advisory for this project. A repeated
// dismissal is a successful no-op.
func (s *Server) DismissNotice(ctx context.Context, notice string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !knownNotice(notice) {
		return gensurla.MakeUnprocessable(errors.New("unknown team recommendation"))
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	record := &s.state.Record
	if slices.Contains(record.DismissedNotices, notice) {
		return nil
	}
	before := record.DismissedNotices
	record.DismissedNotices = append(slices.Clone(before), notice)
	if err := s.state.save(); err != nil {
		record.DismissedNotices = before
		return userTeamError(stateUnavailable(ctx, "dismiss_notice", s.recordWriteFailure(err)))
	}
	return nil
}

// resolveChoices validates explicit requests against the current installed
// inventory and fills each required role from its saved or installed default.
func (s *Server) resolveChoices(
	ctx context.Context, roster []launcherRole, requested map[string]*gensurla.RoleAgentChoice,
) (map[string]*gensurla.RoleAgentChoice, error) {
	for role, choice := range requested {
		if !choiceRole(role) || choice == nil || !validChoice(roleChoice(*choice)) {
			return nil, invalidWorkerChoice()
		}
	}
	if len(roster) == 0 && len(requested) == 0 {
		return nil, nil
	}
	options, err := s.ListAgentOptions(ctx)
	if err != nil {
		return nil, err
	}
	headless := !s.team.dependencies.HerdrAvailable
	for _, choice := range requested {
		if !installedChoice(choice, options, headless) {
			return nil, invalidWorkerChoice()
		}
	}

	s.state.mu.Lock()
	saved := s.state.Record.SavedChoices
	s.state.mu.Unlock()
	choices := make(map[string]*gensurla.RoleAgentChoice, len(roster))
	for _, role := range roster {
		selected, explicit := requested[role.Role]
		if !explicit {
			preference := gensurla.RoleAgentChoice(saved[role.Role])
			if installedChoice(&preference, options, headless) {
				selected = &preference
			} else {
				selected = defaultChoice(role.Role, options, choices, headless)
			}
		}
		if selected == nil {
			return nil, gensurla.MakeTeamUnavailable(errors.New("install a supported coding agent, then start the workers again"))
		}
		choices[role.Role] = selected
	}
	return choices, nil
}

// saveChoices stores the selected roles without dropping the preference
// for a test writer when a build uses stored evaluator tests.
func (s *Server) saveChoices(ctx context.Context, choices map[string]*gensurla.RoleAgentChoice) error {
	if len(choices) == 0 {
		return nil
	}
	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	record := &s.state.Record
	before := record.SavedChoices
	next := make(map[string]roleChoice, len(before)+len(choices))
	maps.Copy(next, before)
	for role, choice := range choices {
		value := roleChoice(*choice)
		value.Model = cloneChoiceOption(value.Model)
		value.Thinking = cloneChoiceOption(value.Thinking)
		next[role] = value
	}
	record.SavedChoices = next
	if err := s.state.save(); err != nil {
		record.SavedChoices = before
		return userTeamError(stateUnavailable(ctx, "save_choices", s.recordWriteFailure(err)))
	}
	return nil
}

// cloneChoiceOption never shares a mutable generated payload pointer with
// a durable preference or a caller reading the snapshot.
func cloneChoiceOption(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

// installedChoice verifies trusted inventory membership and supported option
// flags. Model IDs may be supplied by the user even when absent from a
// client's discovery list. The installed client resolves access at launch.
func installedChoice(choice *gensurla.RoleAgentChoice, options []*gensurla.InstalledAgentOption, headless bool) bool {
	if choice.Client == "antigravity" && choice.Model != nil && choice.Thinking != nil {
		// Chorus treats a suffixed Antigravity model as an effort selection.
		model := *choice.Model
		if separator := strings.LastIndexByte(model, '-'); separator >= 0 {
			switch model[separator+1:] {
			case "low", "medium", "high", "xhigh", "max":
				return false
			}
		}
	}
	for _, option := range options {
		if choice.Client == option.Client && choice.Program == option.Program {
			return (!headless || option.HeadlessSupported) &&
				(choice.Model == nil || option.ModelSupported) &&
				(choice.Thinking == nil || option.ThinkingSupported && slices.Contains(option.Efforts, *choice.Thinking)) &&
				(!choice.Yolo || option.YoloSupported)
		}
	}
	return false
}

// defaultChoice prefers separate families when available and reuses the sole
// installed agent for both worker roles.
func defaultChoice(
	role string, options []*gensurla.InstalledAgentOption,
	selected map[string]*gensurla.RoleAgentChoice, headless bool,
) *gensurla.RoleAgentChoice {
	preferred := "codex"
	var already string
	if role == "test_writer" {
		preferred = "claude"
		if builder := selected["builder"]; builder != nil {
			already = builder.Client
		}
	}
	var first, distinct *gensurla.InstalledAgentOption
	for _, option := range options {
		if headless && !option.HeadlessSupported {
			continue
		}
		if first == nil {
			first = option
		}
		if option.Client != already {
			if option.Client == preferred {
				distinct = option
				break
			}
			if distinct == nil {
				distinct = option
			}
		}
	}
	if distinct != nil {
		first = distinct
	}
	if first == nil {
		return nil
	}
	return &gensurla.RoleAgentChoice{Client: first.Client, Program: first.Program}
}

// validateTeamPreferences refuses malformed persisted settings before loading
// them into a process capable of starting tools.
func validateTeamPreferences(record Record) error {
	for role, choice := range record.SavedChoices {
		if !choiceRole(role) || !validChoice(choice) {
			return errors.New("invalid saved team choice")
		}
	}
	notices := map[string]bool{}
	for _, notice := range record.DismissedNotices {
		if !knownNotice(notice) || notices[notice] {
			return errors.New("invalid dismissed team recommendation")
		}
		notices[notice] = true
	}
	return nil
}

// validChoice validates the literal fields shared by the transport and
// persisted record before matching a real installed program.
func validChoice(choice roleChoice) bool {
	switch choice.Client {
	case "claude", "codex", "antigravity", "cursor", "gemini", "opencode":
	default:
		return false
	}
	return literalChoiceValue(choice.Program) &&
		(choice.Model == nil || literalChoiceValue(*choice.Model)) &&
		(choice.Thinking == nil || literalChoiceValue(*choice.Thinking))
}

// literalChoiceValue excludes control delimiters and flag-shaped option values.
func literalChoiceValue(value string) bool {
	return value != "" && value[0] != '-' &&
		strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}

// workerRole closes the picker and teardown set over the two build roles.
func workerRole(role string) bool {
	return role == "builder" || role == "test_writer"
}

// choiceRole handles the optional explicit guide choice and both worker roles.
func choiceRole(role string) bool {
	return workerRole(role) || role == "guide"
}

// knownNotice closes the persisted dismissal set.
func knownNotice(notice string) bool {
	switch notice {
	case "single_agent_family", "outside_herdr", "herdr_unavailable":
		return true
	}
	return false
}

// invalidWorkerChoice names a refused role option without echoing caller input.
func invalidWorkerChoice() error {
	return gensurla.MakeUnprocessable(errors.New("choose a supported installed coding agent and options for each requested role"))
}
