// This file owns the repeatable team-start operation and the Chorus roster contract.
package project

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	// TeamHost supplies typed Herdr effects; the executable resolves programs and configuration.
	TeamHost interface {
		Spaces(context.Context) ([]TeamSpace, error)
		CreateSpace(context.Context, string, string) (TeamSpace, error)
		Terminals(context.Context, string, string, []string) ([]TeamTerminal, error)
		Focus(context.Context, TeamSpace) error
		Overlays(context.Context) ([]string, error)
		ReadTerminal(context.Context, string) (TeamTerminal, error)
		LabelGuide(ctx context.Context, address, root, swarm, client, handle, space, tab string) error
		OpenCompanion(context.Context) error
		LaunchWorkers(context.Context, string, map[string]*gensurla.RoleAgentChoice) error
		ClosePane(context.Context, string) error
	}
	// TeamDependencies binds every external team effect and the server's accepted-work lifetime.
	TeamDependencies struct {
		// Lifetime ends accepted work only when the owning server shuts down.
		Lifetime context.Context
		// Host is the root-injected Herdr command adapter.
		Host TeamHost
		// HerdrAvailable reports whether local worker panes can be launched.
		HerdrAvailable bool
		// CompanionInstalled enables the first-attachment companion window.
		CompanionInstalled bool
		// DiscoverAgents checks installed programs on explicit option and launch requests.
		DiscoverAgents func(context.Context) ([]*gensurla.InstalledAgentOption, error)
		// WorkerEnv is the original server login environment for headless CLI sign-in.
		WorkerEnv []string
		// RosterDirectory is Herdr's local launcher roster directory.
		RosterDirectory string
		// Shell is the server login shell used by the shared running predicate.
		Shell string
		// Connect resolves the running Chorus owner at each accepted team start.
		// Failure happens before any team state or window changes.
		Connect func(context.Context) (TeamConnection, error)
	}
	// TeamConnection binds an immutable observer/sender pair to one Chorus endpoint.
	// The executable constructs it from Chorus status; it is never persisted.
	TeamConnection struct {
		// Endpoint identifies the server whose watcher facts permit reminders.
		Endpoint string
		// Observer follows only the registered selection; nil leaves facts unknown.
		Observer TeamObserver
		// Sender posts fixed direct reminders; nil sends nothing. It never types into a terminal.
		Sender TeamSender
	}
	// TeamSpace is a local space and its tab-to-terminal mapping reported by Herdr.
	TeamSpace struct {
		ID      string    `json:"id"`
		Name    string    `json:"name"`
		Folder  string    `json:"folder"`
		Machine *string   `json:"machine"`
		Tabs    []TeamTab `json:"tabs"`
	}
	// TeamTab identifies the terminals belonging to a Herdr tab.
	TeamTab struct {
		ID        string     `json:"id"`
		Terminals []TeamPane `json:"terminals"`
	}
	// TeamPane supplies the terminal identifier accepted by pane.focus.
	TeamPane struct {
		Terminal string `json:"terminal"`
		Pane     string `json:"pane"`
	}
	// TeamTerminal contains only the trusted observations needed to identify a running role.
	TeamTerminal struct {
		Address    string            `json:"address"`
		State      string            `json:"state"`
		Labels     map[string]string `json:"labels"`
		Foreground *TeamForeground   `json:"foreground"`
		Reported   bool              `json:"reported"`
		Status     *TeamStatus       `json:"status"`
		CreatedMS  int64             `json:"created_ms"`
		Reason     string            `json:"reason"`
	}
	// TeamForeground preserves argv rather than Herdr's display name (which may be a version).
	TeamForeground struct {
		Argv []string `json:"argv"`
	}
	// teamOperation serializes accepted work without tying its lifetime to the HTTP caller.
	teamOperation struct {
		sync.Mutex
		dependencies TeamDependencies
		// assignments reads which worker roles currently have work on the open build.
		assignments controlplane.AssignmentReader
		// headless owns the one-shot worker processes for the accepted build.
		headless headlessWorkers
		// lastStopped is the successfully drained generation, held under the team lock.
		lastStopped controlplane.BuildIdentity
		// closed forbids new work once shutdown has begun.
		closed bool
		// withTimeout privately injects the accepted-operation deadline; nil uses context.WithTimeout.
		withTimeout func(context.Context, time.Duration) (context.Context, context.CancelFunc)
		// observationMu protects the immutable connection and small public snapshot.
		observationMu sync.RWMutex
		observed      map[string]roleObservation
		connection    TeamConnection
	}
	// launcherRoster mirrors Chorus load_roster's exact closed schema.
	launcherRoster struct {
		Root  string         `json:"root"`
		Swarm string         `json:"swarm"`
		Roles []launcherRole `json:"roles"`
	}
	// launcherRole binds one trusted role to its existing folder and first message.
	launcherRole struct {
		Role         string         `json:"role"`
		Title        string         `json:"title"`
		Handle       string         `json:"handle"`
		Folder       string         `json:"folder"`
		FirstMessage string         `json:"first_message"`
		Connections  [0]struct{}    `json:"connections"`
		Place        *launcherPlace `json:"place"`
	}
	// launcherPlace places each worker relative to an earlier role.
	launcherPlace struct {
		Beside    string `json:"beside"`
		Direction string `json:"direction"`
	}
)

// teamStartTimeout bounds one accepted roster/window transition so an unresponsive
// Herdr command cannot hold the serialized team operation indefinitely. This budget
// is independent of a build's working-copy and generation phase.
const teamStartTimeout = 45 * time.Second

// errAssignmentsUnavailable keeps assignment read failures free of private evidence.
var errAssignmentsUnavailable = errors.New("team assignments could not be read")

// BindAssignments installs the control plane's read-only assignment reader
// before the server listens so StartAgents and StopWorkers know which worker
// roles currently have work on the open build.
func (s *Server) BindAssignments(assignments controlplane.AssignmentReader) {
	s.team.Lock()
	defer s.team.Unlock()
	s.team.assignments = assignments
}

// readAssignments calls the bound assignment reader, treating an unbound reader
// and a failed read identically. Unknown assignments never authorize worker effects.
func (s *Server) readAssignments(ctx context.Context) (controlplane.TeamAssignments, error) {
	if s.team.assignments == nil {
		return controlplane.TeamAssignments{}, errAssignmentsUnavailable
	}
	assignments, err := s.team.assignments(ctx)
	if err != nil {
		return controlplane.TeamAssignments{}, errAssignmentsUnavailable
	}
	return assignments, nil
}

// registeredTeamRoles derives current launcher text from each saved role and folder.
func registeredTeamRoles(agents map[string]agentRecord) []launcherRole {
	byRole := make(map[string]string, len(agents))
	for actor, agent := range agents {
		byRole[agent.Role] = actor
	}
	roles := make([]launcherRole, 0, len(agents))
	for _, role := range []string{"guide", "builder", "test_writer"} {
		handle, exists := byRole[role]
		if !exists {
			continue
		}
		title, message := roleFirstMessage(role)
		roles = append(roles, launcherRole{Role: role, Title: title, Handle: handle, Folder: agents[handle].Folder, FirstMessage: message})
	}
	return roles
}

// roleFirstMessage owns the fixed product instructions; messages never enter saved state.
func roleFirstMessage(role string) (string, string) {
	switch role {
	case "guide":
		return "Guide", "Work through the Story and the change with the user in this project's pane. Use your surla connection, starting with the surla://golden-path guidance resource and list_changes/get_change. If the user has not yet said what they want to build or change, ask one short, plain question and wait; never ask for a Go module path, folder name or requirements checklist. If the project folder holds no project yet, call init_project with dir \".\" and no module path unless the user gave one: Surla derives it from the folder name. Write the user's goal straight into a Story and a Change Card instead of interviewing them in chat: put open product questions, such as target users, thresholds or edge cases, into the analysis's ambiguities with concrete options, which the user answers on the views page. After initializing the project, call update_draft before each new change and whenever told a design file changed. Work only in the draft folder returned by update_draft or refresh_story; never edit the project folder or copy design files between it and the draft. Correct the files it reports as needing correction in your draft; when one is in the project folder, tell the user what to correct there and wait. Call update_draft again until that list is empty, then call refresh_story and poll get_story_refresh until it succeeds or fails. Use the succeeded refresh when calling propose_change or revise_change. When the design needs a module, run GOWORK=off go get and GOWORK=off go mod tidy in the draft. To see how Surla reads your draft, call refresh_story and read the result with get_story_refresh. To discard unsaved edits, delete the draft and refresh. After the user drops a build, refresh before drafting the next change. If the builder reports that a hook reformatted a design file, run the same formatter in the draft, refresh and revise. Only the user confirms intent and approves results, on the views page."
	case "builder":
		return "Builder", "Use your surla connection, starting with the surla://golden-path guidance resource. Use list_builder_assignments to find the build. An empty list means no build needs you: say so and stop. Work only in this working folder, your copy of the project (working_folder in your assignment), on the branch checked out there. Read each new assignment with get_builder_assignment before any other work: it puts your folder on the build's own branch. Then run git status: implementation files you authored or generated for this build are expected current work. If you find unexpected files or work from an earlier build, stop and tell the user before carrying it forward. When a Surla call says a build was dropped or a new one started, stop: make no more edits or commits for the old work. Call generate_code: it writes the agreed design into design/ and regenerates gen/; never edit either. If the design needs a change, ask the guide to propose a revision for the user to adopt. When generation names go get, run what it names here, then call it again. Commit on your branch. Never name, move or delete a surla/ branch. Never merge, cherry-pick or copy files from another surla/ branch: they hold work the user has not taken. When a Surla call says your copy is on another branch, put your work where it says with git and call again; when it names another build's work, remove its commits and its changes to the files named, by any git you choose. When your assignment's gowork_off is true, run every go command as GOWORK=off go .... Use check_project before verify_change. Read the assignment before verification: when a test writer is used, verify_change is refused until its tests are accepted. Only the user confirms intent and approves results on the views page."
	case "test_writer":
		return "Test writer", "Use your surla connection, starting with the surla://golden-path guidance resource. Use list_test_writer_assignments to find the build. An empty list means no build needs you: say so and stop. Read each new assignment with get_test_writer_assignment before any other work: it puts your folder on the build's own branch. Then run git status: the Story table and generated test-module files Surla just prepared, and tests you already authored for this build, are expected current work. Leave Surla's prepared files unchanged. If you find unexpected files or work from an earlier build, stop and tell the user before carrying it forward. When a Surla call says a build was dropped or a new one started, stop: make no more edits or commits for the old work. Stay on the branch checked out in your working copy and do not commit: Surla commits what it accepts. Never name, move or delete a surla/ branch. Never bring files or commits from another surla/ branch. Run every go command as GOWORK=off go ..., as Surla's tests check does. Implement one test per required entry in the test module, eval/ in the working folder the assignment names, and change nothing outside that module. Your tests must also hold the service to every answer the user confirmed, every confirmation example and every preserved behavior the assignment lists. Surla writes eval/design/story_tests.go, which declares the required tests, from the agreed Story and generates the test module at each assignment read that marks it read and again at each report; leave that file as it is. After changing the module's design, report to have it generated again or run surla gen -dir eval in the working folder first; regenerating keeps the required tests. To check your work, run GOWORK=off go build ./... in eval/: it fails naming any required method you have not written. Then call report_tests_finished. When you call report_tests_finished, include coverage_statements. For each scenario whose coverage you state, give its exact scenario ID, question_ids from this assignment's answers[].id, example_ids from examples[].id, and an explanation of the interaction or assertion that exercises them. Use an empty array when you state none; Surla will show Not stated. These are your statements, not Surla's judgment of what your tests assert. The tests must still hold the service to every confirmed answer, example and preserved behavior. Surla validates supplied references while checking your report. Read get_test_writer_assignment for the verdict and the statements you submitted; correct any stated-reference findings and report again. Do not commit: Surla records the tests it accepts. Surla's check of your report is the judge: When its verdict is in, read get_test_writer_assignment again, with any findings to fix. Only the user confirms intent and approves results on the views page."
	}
	panic("unregistered team role")
}

// teamName uses the same project suffix for team and handles within Chorus's 32-character limit.
func teamName(prefix, id string) string {
	var clean strings.Builder
	for _, c := range prefix {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			clean.WriteRune(c)
		}
	}
	base := strings.TrimLeft(clean.String(), "_-")
	if base == "" {
		base = "project"
	}
	suffix := id[:12]
	return base[:min(len(base), 19)] + "-" + suffix
}

// localSpaceID restricts stored space names to local filenames without path separators
// so records written before the stricter space-ID rule still load.
func localSpaceID(id string) bool {
	return id != "" && id != "." && id != ".." && !strings.ContainsAny(id, "/\\\x00")
}

// safeSpaceID restricts managed space identifiers to 1–64 ASCII characters of
// [A-Za-z0-9_.-] other than "." and "..".
func safeSpaceID(id string) bool {
	if len(id) == 0 || len(id) > 64 || id == "." || id == ".." {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '_' && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// roleRunning reports whether a terminal is running an agent rather than an idle shell.
//
// Shared contract with Surla: a live terminal for which Herdr reports no
// foreground program counts as running; only a reported foreground that is an
// idle shell makes a role not running. An idle shell has the base name of
// argv[0] (leading dashes removed) in IDLE_SHELLS or the base name of $SHELL,
// and neither argv[1] nor argv[2] is -c or a single-dash flag group containing
// c (such as -lic).
func roleRunning(t TeamTerminal, shell string) bool {
	if t.State != "live" {
		return false
	}
	if t.Foreground == nil {
		return true
	}
	if len(t.Foreground.Argv) == 0 || t.Foreground.Argv[0] == "" {
		return true
	}
	argv := t.Foreground.Argv
	name := strings.TrimLeft(filepath.Base(strings.TrimLeft(argv[0], "-")), "-")
	idleShell := name == strings.TrimLeft(filepath.Base(strings.TrimLeft(shell, "-")), "-") && shell != ""
	for _, candidate := range []string{"sh", "bash", "zsh", "fish", "dash", "ksh", "csh", "tcsh", "nu", "pwsh", "xonsh", "elvish"} {
		if name == candidate {
			idleShell = true
		}
	}
	if !idleShell {
		return true
	}
	for _, arg := range argv[1:min(len(argv), 3)] {
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg[1:], "c") {
			return true
		}
	}
	return false
}

// validTeamName enforces the persisted launcher identifier contract at the storage boundary.
func validTeamName(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for i, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if i > 0 && (c == '_' || c == '-') {
			continue
		}
		return false
	}
	return true
}

// teamUnavailable records a fixed bounded failure without exporting private paths or command arguments.
// The failed step stays on the span; the returned cause is one of the design's
// TeamUnavailable values and selects the recovery step each caller shows.
func teamUnavailable(ctx context.Context, step, cause string) error {
	failure := errors.New("project team could not be prepared")
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attribute.String("team.failed_step", step), attribute.String("team.failed_cause", cause))
	span.RecordError(failure)
	span.SetStatus(codes.Error, "team start failed")
	return &genproject.TeamUnavailable{Name: "team_unavailable", Cause: cause}
}

// acceptedTeamContext retains caller values and tracing while the server owns
// cancellation and the one-operation deadline. Cleanup stops the lifetime callback.
func (s *Server) acceptedTeamContext(ctx context.Context) (context.Context, context.CancelFunc) {
	withTimeout := s.team.withTimeout
	if withTimeout == nil {
		withTimeout = context.WithTimeout
	}
	ctx, cancel := withTimeout(context.WithoutCancel(ctx), teamStartTimeout)
	stop := context.AfterFunc(s.team.dependencies.Lifetime, cancel)
	if s.team.dependencies.Lifetime.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
	}
}
