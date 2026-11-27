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

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/verifyrun"
	"go.opentelemetry.io/otel/codes"
)

type (
	// Team is the project server's sole in-memory team owner. Authorization belongs
	// to the control plane's generated endpoint policy, never these internal calls.
	// BindAssignments installs the owner's read-only assignment reader before the
	// server listens. Supervise returns nil when either of its owning lifetimes
	// ends; a non-nil error means unexpected failure and permits a bounded retry.
	//
	// ReadTeam and ReadConversation return what the owner last observed and
	// start no call to herdr-core or Chorus, so the change list may read both
	// on every poll. SendMessage posts the user's message to the project's
	// Chorus channel and starts a missing guide first when the message is for
	// the guide or the whole team. InterruptRole presses a running role's stop
	// key; StopRole ends and removes the role's managed terminal.
	// CreateTerminalTicket mints the single-use value AttachTerminal consumes;
	// AttachTerminal relays one viewer's frames until either side ends the
	// stream. The owner returns the generated errors each operation declares.
	Team interface {
		BindAssignments(AssignmentReader)
		ReadTeam(context.Context) (*genhighbay.TeamSnapshot, error)
		ReadConversation(context.Context) (*genhighbay.TeamConversation, error)
		SendMessage(context.Context, *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error)
		StartAgents(context.Context, map[string]*genhighbay.RoleAgentChoice) (*genhighbay.ProjectTeam, error)
		InterruptRole(ctx context.Context, role string) error
		StopRole(ctx context.Context, role string) error
		// RestartRole ends the terminal Highbay started for role, if any,
		// and starts the role again with choice, else its saved choice, else
		// an installed default. It refuses a role that runs in a session
		// Highbay did not start.
		RestartRole(ctx context.Context, role string, choice *genhighbay.RoleAgentChoice) error
		CreateTerminalTicket(ctx context.Context, role string) (*genhighbay.TerminalTicketResult, error)
		AttachTerminal(context.Context, *genhighbay.AttachRoleTerminalPayload, genhighbay.AttachRoleTerminalServerStream) error
		ListAgentOptions(context.Context) ([]*genhighbay.InstalledAgentOption, error)
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
	// for BuilderOnly. It starts no terminal; StartAgents owns that phase.
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
	// PolicyRefusalKind identifies which explicit client setting blocked worker startup.
	PolicyRefusalKind string
	// TeamPolicyRefusal is the Team owner's bounded refusal to start non-Yolo
	// agents when an explicit setting in a coding client's configuration
	// blocks required Highbay/Chorus MCP operations or folder trust. It omits raw
	// filesystem paths so StartTeamAgents and StartBuild's BuildStartStep
	// (<= 512 characters) present the exact setting and retry step.
	TeamPolicyRefusal struct {
		// Kind selects the bounded recovery message template.
		Kind PolicyRefusalKind
		// Client names the coding-agent client ("codex", "claude", "gemini", "cursor").
		Client string
		// Server names the blocked MCP server ("highbay" or "chorus") when applicable.
		Server string
		// List names the blocking permission or filter list ("deny", "ask", "excludeTools", "includeTools").
		List string
		// Detail holds a bounded setting or rule value such as "prompt" or "Mcp(*:get_builder_assignment)".
		Detail string
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
		// LatestCompletedRunID identifies the latest finished run, including a run
		// that could not produce a report. LatestRunPassed is false for that failure.
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

const (
	// PolicyCodexServerDisabled reports `[mcp_servers.<srv>] enabled = false` in Codex config.toml.
	PolicyCodexServerDisabled PolicyRefusalKind = "codex_server_disabled"
	// PolicyCodexServerApproval reports a non-`approve` `default_tools_approval_mode` in Codex config.toml.
	PolicyCodexServerApproval PolicyRefusalKind = "codex_server_approval"
	// PolicyCodexProjectUntrusted reports a non-`trusted` `trust_level` in Codex config.toml.
	PolicyCodexProjectUntrusted PolicyRefusalKind = "codex_project_untrusted"
	// PolicyClaudePermission reports a blocking rule in Claude `settings.json` `permissions.deny` or `ask`.
	PolicyClaudePermission PolicyRefusalKind = "claude_permission"
	// PolicyCursorPermission reports a blocking rule in Cursor `cli-config.json` `permissions.deny` or `ask`.
	PolicyCursorPermission PolicyRefusalKind = "cursor_permission"
	// PolicyGeminiServerUntrusted reports `mcpServers.<srv>.trust == false` in Gemini `settings.json`.
	PolicyGeminiServerUntrusted PolicyRefusalKind = "gemini_server_untrusted"
	// PolicyGeminiToolFiltered reports `excludeTools` or `includeTools` blocking a required tool in Gemini `settings.json`.
	PolicyGeminiToolFiltered PolicyRefusalKind = "gemini_tool_filtered"
	// PolicyGeminiFolderUntrusted reports `DO_NOT_TRUST` in Gemini `trustedFolders.json`.
	PolicyGeminiFolderUntrusted PolicyRefusalKind = "gemini_folder_untrusted"
	// PolicyConfigUnusable reports a malformed, unreadable, or worktree-aliased client configuration file.
	PolicyConfigUnusable PolicyRefusalKind = "config_unusable"
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
func (c *ControlPlane) GetTeam(ctx context.Context) (*genhighbay.TeamSnapshot, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	return team.ReadTeam(ctx)
}

// GetTeamConversation returns the project's Chorus conversation as the team
// owner last read it.
func (c *ControlPlane) GetTeamConversation(ctx context.Context) (*genhighbay.TeamConversation, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	return team.ReadConversation(ctx)
}

// SendTeamMessage posts the user's message through the team owner, which
// starts a missing guide first, and turns a client-policy refusal of that
// start into the user's next step.
func (c *ControlPlane) SendTeamMessage(ctx context.Context, p *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	message, err := team.SendMessage(ctx, p)
	var policy *TeamPolicyRefusal
	if errors.As(err, &policy) {
		return nil, genhighbay.MakeUnprocessable(errors.New(policy.Step("then send your message again.")))
	}
	return message, err
}

// InterruptTeamRole presses the stop key in the role's terminal for the user.
func (c *ControlPlane) InterruptTeamRole(ctx context.Context, p *genhighbay.InterruptTeamRolePayload) error {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return err
	}
	return team.InterruptRole(ctx, p.Role)
}

// StopTeamRole ends and removes the role's managed terminal for the user.
func (c *ControlPlane) StopTeamRole(ctx context.Context, p *genhighbay.StopTeamRolePayload) error {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return err
	}
	return team.StopRole(ctx, p.Role)
}

// RestartTeamRole ends the role's managed terminal and starts the role
// again for the user, and maps a coding client's setting that blocks the
// start to an actionable error.
func (c *ControlPlane) RestartTeamRole(ctx context.Context, p *genhighbay.RestartTeamRolePayload) error {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return err
	}
	err = team.RestartRole(ctx, p.Role, p.Choice)
	var policy *TeamPolicyRefusal
	if errors.As(err, &policy) {
		return genhighbay.MakeUnprocessable(errors.New(policy.Step("then choose Restart again.")))
	}
	return err
}

// CreateTerminalTicket mints the single-use value the page presents when it
// opens the role's terminal stream.
func (c *ControlPlane) CreateTerminalTicket(ctx context.Context, p *genhighbay.CreateTerminalTicketPayload) (*genhighbay.TerminalTicketResult, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	return team.CreateTerminalTicket(ctx, p.Role)
}

// AttachRoleTerminal hands the upgraded connection to the team owner, which
// consumes the ticket and relays frames until the viewer or the terminal ends
// the stream.
func (c *ControlPlane) AttachRoleTerminal(ctx context.Context, p *genhighbay.AttachRoleTerminalPayload, stream genhighbay.AttachRoleTerminalServerStream) error {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return err
	}
	conn, ok := ctx.Value(terminalConnKey{}).(*terminalConn)
	if !ok {
		// Only NewHTTPHandler's terminal route opens this stream.
		return team.AttachTerminal(ctx, p, stream)
	}
	// However the relay ends, the viewer's connection is closed with it.
	err = team.AttachTerminal(ctx, p, &boundedTerminalStream{AttachRoleTerminalServerStream: stream, conn: conn})
	return errors.Join(err, conn.close())
}

// Error returns the bounded policy refusal text for internal callers and tests.
func (e *TeamPolicyRefusal) Error() string {
	return e.Step("then start the build workers again.")
}

// Step formats the bounded recovery instruction naming the client configuration
// file, exact setting to update, and caller-supplied retry action. Every result
// fits within BuildStartStep's 512-character contract without suggesting Yolo.
func (e *TeamPolicyRefusal) Step(retry string) string {
	server := boundedPolicyText(e.Server, 16)
	list := boundedPolicyText(e.List, 16)
	detail := boundedPolicyText(e.Detail, 48)
	var step string
	switch e.Kind {
	case PolicyCodexServerDisabled:
		step = fmt.Sprintf(
			"Codex disables MCP server %q in config.toml ([mcp_servers.%s] enabled = false). Set enabled = true on [mcp_servers.%s] in your Codex config.toml, %s",
			server, server, server, retry,
		)
	case PolicyCodexServerApproval:
		step = fmt.Sprintf(
			"Codex requires interactive approval for MCP server %q in config.toml ([mcp_servers.%s] default_tools_approval_mode = %q). Set default_tools_approval_mode = \"approve\" on [mcp_servers.%s] in your Codex config.toml, %s",
			server, server, detail, server, retry,
		)
	case PolicyCodexProjectUntrusted:
		step = fmt.Sprintf(
			"Codex marks this project folder (or an ancestor folder) as %q in config.toml. Set trust_level = \"trusted\" for it in your Codex config.toml, %s",
			detail, retry,
		)
	case PolicyClaudePermission:
		step = fmt.Sprintf(
			"Claude Code blocks required %s MCP tools via permissions.%s (%q) in settings.json. Remove or narrow that entry in permissions.%s in your Claude settings.json, %s",
			server, list, detail, list, retry,
		)
	case PolicyCursorPermission:
		step = fmt.Sprintf(
			"Cursor blocks required %s MCP tools via permissions.%s (%q) in cli-config.json. Remove or narrow that entry in permissions.%s in ~/.cursor/cli-config.json, %s",
			server, list, detail, list, retry,
		)
	case PolicyGeminiServerUntrusted:
		step = fmt.Sprintf(
			"Gemini CLI marks MCP server %q as untrusted in settings.json (mcpServers.%s.trust is false). Set mcpServers.%s.trust to true in your Gemini settings.json, %s",
			server, server, server, retry,
		)
	case PolicyGeminiToolFiltered:
		if list == "includeTools" {
			step = fmt.Sprintf(
				"Gemini CLI omits required %s tool %q from mcpServers.%s.includeTools in settings.json. Include %q (or remove includeTools) in your Gemini settings.json, %s",
				server, detail, server, detail, retry,
			)
		} else {
			step = fmt.Sprintf(
				"Gemini CLI excludes required %s tool %q via mcpServers.%s.excludeTools in settings.json. Remove %q from excludeTools in your Gemini settings.json, %s",
				server, detail, server, detail, retry,
			)
		}
	case PolicyGeminiFolderUntrusted:
		step = fmt.Sprintf(
			"Gemini CLI marks this project folder (or an ancestor folder) as DO_NOT_TRUST in trustedFolders.json. Set it to \"TRUST_FOLDER\" in your Gemini trustedFolders.json, %s",
			retry,
		)
	case PolicyConfigUnusable:
		clientName := policyClientDisplayName(e.Client)
		switch {
		case clientName != "" && detail != "":
			step = fmt.Sprintf(
				"%s configuration file %s could not be read or safely used. Fix its syntax, permissions, or path/symlink outside .highbay-build and .highbay-tests, %s",
				clientName, detail, retry,
			)
		case clientName != "":
			step = fmt.Sprintf(
				"%s configuration could not be read or safely used. Fix its syntax, permissions, or path/symlink outside .highbay-build and .highbay-tests, %s",
				clientName, retry,
			)
		default:
			step = "A coding-agent configuration file could not be read or safely used. Fix its syntax, permissions, or path/symlink outside .highbay-build and .highbay-tests, " + retry
		}
	default:
		step = "A coding-agent permission setting prevented starting the workers; update that client setting, " + retry
	}
	return boundedPolicyText(step, 512)
}

// StartTeamAgents uses the project owner's existing repeatable team preparation
// and maps its typed client-policy refusal to an actionable user error.
func (c *ControlPlane) StartTeamAgents(ctx context.Context, p *genhighbay.StartTeamAgentsPayload) (*genhighbay.ProjectTeam, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	result, err := team.StartAgents(ctx, p.Choices)
	var policy *TeamPolicyRefusal
	if errors.As(err, &policy) {
		return nil, genhighbay.MakeUnprocessable(errors.New(policy.Step("then choose Start workers again.")))
	}
	return result, err
}

// ListTeamAgentOptions discovers installed options only on the user's explicit read.
func (c *ControlPlane) ListTeamAgentOptions(ctx context.Context) (*genhighbay.ListTeamAgentOptionsResult, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	agents, err := team.ListAgentOptions(ctx)
	if err != nil {
		return nil, err
	}
	roles := []string{"builder"}
	if !c.storedEvaluator() {
		roles = append(roles, "test_writer")
	}
	return &genhighbay.ListTeamAgentOptionsResult{Agents: agents, BuildRoles: roles}, nil
}

// DismissTeamNotice persists a project notice preference through its team owner.
func (c *ControlPlane) DismissTeamNotice(ctx context.Context, p *genhighbay.DismissTeamNoticePayload) error {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return err
	}
	return team.DismissNotice(ctx, p.Notice)
}

// policyClientDisplayName maps a client key to its user-facing product name.
func policyClientDisplayName(client string) string {
	switch client {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	case "gemini":
		return "Gemini CLI"
	case "cursor":
		return "Cursor"
	default:
		return ""
	}
}

// boundedPolicyText clamps s to at most maxRunes Unicode code points.
func boundedPolicyText(s string, maxRunes int) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes])
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
		return nil, genhighbay.MakeForbidden(errors.New("caller may not access this project team"))
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
		_, span := c.tracer("highbay/controlplane").Start(c.serverCtx, "highbay.team.supervisor_failed")
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
				case verifyrun.StateCompleted, verifyrun.StateNotCompleted:
					if result.LatestCompletedRunID == "" {
						result.LatestCompletedRunID = run.Request.RunID
						result.LatestRunPassed = run.State.Kind == verifyrun.StateCompleted && run.State.Report.Passed
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
		// A historical pass may be merged after agreement or test inputs change.
		// Merge completion belongs to the build, independently of current work.
		state, err := c.startedBuild(ctx, c.team.Owner, build)
		if err != nil {
			return TeamAssignments{}, err
		}
		result.Merged = state.Taking.Kind() == genhighbay.BuildTakingKindMerged
		if result.Merged {
			result.BuilderHasWork = false
			result.TestWriterHasWork = false
			result.Unread = nil
		}
	}
	return result, nil
}
