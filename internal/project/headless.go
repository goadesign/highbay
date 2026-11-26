// This file owns the lifetime and turn budget of non-interactive project workers.
package project

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/procgroup"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	// headlessWorkers holds the current build's roles under teamOperation's effect lock.
	// Snapshot readers use the published build and turn pointers without that lock.
	headlessWorkers struct {
		current atomic.Pointer[headlessBuild]
	}
	// headlessBuild retains the last observed assignment facts and turn counts for one build.
	headlessBuild struct {
		id                controlplane.BuildIdentity
		builder           headlessWorker
		testWriter        headlessWorker
		testsPending      bool
		latestCompletedID string
		stopped           bool
	}
	// headlessWorker remembers assignment changes while its current invocation drains.
	headlessWorker struct {
		role       string
		launch     headlessLaunch
		attempts   int
		unread     string
		pending    bool
		invocation atomic.Pointer[headlessTurn]
	}
	// headlessLaunch is argv derived from the validated choice and private role registration.
	headlessLaunch struct {
		role    string
		folder  string
		program string
		args    []string
	}
	// headlessTurn transfers completion and cleanup result from the command reaper.
	headlessTurn struct {
		cancel  context.CancelFunc
		done    chan struct{}
		stopErr error // Set before closing done.
	}
)

const (
	// headlessTurnLimit bounds the model invocations one role may consume in an open build.
	headlessTurnLimit = 3
	// headlessTurnTimeout limits one invocation, including agent tool calls.
	headlessTurnTimeout = 30 * time.Minute
	// headlessTermGrace permits a signaled command to finish before killing it.
	headlessTermGrace = 2 * time.Second
	// headlessCleanupTimeout bounds waiting for a killed process group to disappear.
	headlessCleanupTimeout = 5 * time.Second
)

// startHeadless applies explicit choices to the current build and makes one turn
// available per idle worker. The parent team operation owns the effect lock and
// calls this with freshly read assignment facts.
func (s *Server) startHeadless(
	ctx context.Context,
	roles []launcherRole,
	choices map[string]*genhighbay.RoleAgentChoice,
	assignments controlplane.TeamAssignments,
) error {
	workers := &s.team.headless
	if s.team.closed || s.team.dependencies.Lifetime.Err() != nil {
		return errors.New("the project is stopping")
	}
	if assignments.BuildIdentity == nil || assignments.Merged || assignments.Dropped {
		return nil
	}
	launches := make([]headlessLaunch, 0, len(roles))
	for _, role := range roles {
		if role.Role == "guide" || !assignments.HasWork(role.Role) {
			continue
		}
		choice := choices[role.Role]
		if choice == nil || choice.Program == "" {
			return errors.New("the worker needs an installed agent choice")
		}
		args, err := headlessArgs(choice, headlessPrompt(role.Role))
		if err != nil {
			return err
		}
		launches = append(launches, headlessLaunch{
			role: role.Role, folder: role.Folder, program: choice.Program, args: args,
		})
	}

	build := workers.current.Load()
	if build == nil || !build.matches(assignments.BuildIdentity) {
		// No process from a previous generation may overlap a new worker using
		// the same working copy.
		if build != nil {
			if err := build.stop(); err != nil {
				return err
			}
		}
		build = &headlessBuild{
			id:         *assignments.BuildIdentity,
			builder:    headlessWorker{role: "builder"},
			testWriter: headlessWorker{role: "test_writer"},
		}
		workers.current.Store(build)
	}
	for _, launch := range launches {
		worker := build.worker(launch.role)
		worker.launch = launch
		if !worker.running() {
			worker.pending = true
		}
	}
	return build.reconcile(ctx, s.team.dependencies, assignments)
}

// reconcileHeadless advances an admitted headless build from the control plane's
// latest assignment facts. It runs under the team operation's effect lock.
func (s *Server) reconcileHeadless(ctx context.Context, assignments controlplane.TeamAssignments) {
	build := s.team.headless.current.Load()
	if build != nil && build.matches(assignments.BuildIdentity) {
		// startTurn reports bounded launch failures on its own span. The supervisor
		// keeps observing assignments after a refusal to start an individual turn.
		build.reconcile(ctx, s.team.dependencies, assignments)
	}
}

// stopHeadless drains only the worker group for the supplied admitted build.
func (s *Server) stopHeadless(id controlplane.BuildIdentity) error {
	build := s.team.headless.current.Load()
	if build == nil || !build.matches(&id) {
		return nil
	}
	return build.stop()
}

// closeHeadless prevents further admission and drains every owned worker.
func (s *Server) closeHeadless() error {
	workers := &s.team.headless
	s.team.closed = true
	if build := workers.current.Load(); build != nil {
		return build.stop()
	}
	return nil
}

// running checks whether the role still has a command or descendants to reap.
func (h *headlessWorkers) running(role string) bool {
	build := h.current.Load()
	if build == nil {
		return false
	}
	worker := build.worker(role)
	return worker != nil && worker.running()
}

// matches uses the build identity's timestamp value, independent of its location encoding.
func (b *headlessBuild) matches(id *controlplane.BuildIdentity) bool {
	return id != nil && id.ChangeID == b.id.ChangeID && id.StartedAt.Equal(b.id.StartedAt)
}

// worker looks up the only two roles the headless coordinator may invoke.
func (b *headlessBuild) worker(role string) *headlessWorker {
	switch role {
	case "builder":
		return &b.builder
	case "test_writer":
		return &b.testWriter
	default:
		return nil
	}
}

// reconcile latches new work even while one of the roles is finishing a turn.
// Launches wait for the report and verification owners to finish their checks.
func (b *headlessBuild) reconcile(
	ctx context.Context, deps TeamDependencies, a controlplane.TeamAssignments,
) error {
	if b.stopped || a.Merged || a.Dropped || deps.Lifetime.Err() != nil {
		return nil
	}
	if b.testsPending && !a.TestWriterHasWork ||
		a.LatestCompletedRunID != "" && a.LatestCompletedRunID != b.latestCompletedID && !a.LatestRunPassed {
		b.builder.pending = true
	}
	b.testsPending = a.TestWriterHasWork
	b.latestCompletedID = a.LatestCompletedRunID
	for _, item := range a.Unread {
		worker := b.worker(item.Role)
		if worker != nil && worker.unread != item.Generation {
			worker.unread = item.Generation
			worker.pending = true
		}
	}
	if a.ReportCheckInFlight || a.VerificationInFlight {
		return nil
	}

	var result error
	for _, worker := range []*headlessWorker{&b.builder, &b.testWriter} {
		if !worker.pending || worker.launch.program == "" || !a.HasWork(worker.role) ||
			worker.attempts == headlessTurnLimit || worker.running() {
			continue
		}
		// A worker with descendants still alive cannot share a working copy
		// with a replacement.
		if turn := worker.invocation.Load(); turn != nil && turn.stopErr != nil {
			result = errors.Join(result, turn.stopErr)
			continue
		}
		worker.pending = false
		worker.attempts++
		turn, err := worker.launch.start(ctx, deps)
		if err != nil {
			result = errors.Join(result, err)
			continue
		}
		worker.invocation.Store(turn)
	}
	return result
}

// stop cancels both commands before joining either, so teardown shares one grace window.
func (b *headlessBuild) stop() error {
	b.stopped = true
	turns := []*headlessTurn{b.builder.invocation.Load(), b.testWriter.invocation.Load()}
	for _, turn := range turns {
		if turn != nil {
			turn.cancel()
		}
	}
	var result error
	for _, turn := range turns {
		if turn != nil {
			<-turn.done
			result = errors.Join(result, turn.stopErr)
		}
	}
	return result
}

// running reads completion only after the process group has been reaped or cleanup failed.
func (w *headlessWorker) running() bool {
	turn := w.invocation.Load()
	if turn == nil {
		return false
	}
	select {
	case <-turn.done:
		return false
	default:
		return true
	}
}

// headlessPrompt wraps the same role obligations as pane launch in a finite task.
func headlessPrompt(role string) string {
	_, instruction := roleFirstMessage(role)
	return "Work on your current Highbay assignment. " + instruction +
		" Exit after submitting your work or when the next step must wait on a test report or verification result." +
		" The project server starts another invocation when those results provide new work."
}

// headlessArgs translates one validated role choice to a non-interactive invocation.
func headlessArgs(choice *genhighbay.RoleAgentChoice, prompt string) ([]string, error) {
	var args []string
	if choice.Client == "codex" {
		args = append(args, "exec")
		if !choice.Yolo {
			args = append(args, "--sandbox", "workspace-write")
		}
	}
	if choice.Client == "opencode" {
		args = append(args, "run")
	}
	if choice.Model != nil {
		args = append(args, "--model", *choice.Model)
	}
	switch choice.Client {
	case "claude":
		if choice.Thinking != nil {
			args = append(args, "--effort", *choice.Thinking)
		}
		if choice.Yolo {
			args = append(args, "--dangerously-skip-permissions")
		} else {
			args = append(args, "--allowedTools", "mcp__highbay__*")
		}
		args = append(args, "--print")
	case "antigravity":
		if choice.Thinking != nil {
			args = append(args, "--effort", *choice.Thinking)
		}
		if choice.Yolo {
			args = append(args, "--dangerously-skip-permissions")
		}
		args = append(args, "--print")
	case "codex":
		if choice.Thinking != nil {
			args = append(args, "-c", "model_reasoning_effort="+strconv.Quote(*choice.Thinking))
		}
		if choice.Yolo {
			args = append(args, "--dangerously-bypass-approvals-and-sandbox")
		}
	case "cursor":
		if !choice.Yolo {
			args = append(args, "--trust")
		}
		args = append(args, "--print")
		if choice.Yolo {
			args = append(args, "--yolo")
		}
	case "gemini":
		if choice.Yolo {
			args = append(args, "--approval-mode=yolo")
		}
		args = append(args, "--prompt")
	case "opencode":
		// OpenCode uses the selected model's variant and configured permissions.
	default:
		return nil, errors.New("this agent does not support headless Highbay workers")
	}
	return append(args, prompt), nil
}

// start owns process admission and gives the background invocation its own
// deadline and its server's lifetime, independent of the requesting HTTP call.
func (launch headlessLaunch) start(ctx context.Context, deps TeamDependencies) (*headlessTurn, error) {
	ctx, span := otel.Tracer("highbay/project").Start(context.WithoutCancel(ctx),
		"highbay.project.headless_turn", trace.WithAttributes(attribute.String("team.role", launch.role)))
	ctx, cancel := context.WithTimeout(ctx, headlessTurnTimeout)
	unbind := context.AfterFunc(deps.Lifetime, cancel)
	if deps.Lifetime.Err() != nil {
		cancel()
	}

	cmd := exec.CommandContext(ctx, launch.program, launch.args...)
	cmd.Dir = launch.folder
	cmd.Env = append(append([]string{}, deps.WorkerEnv...), "PWD="+launch.folder)
	// Nil stdin, stdout and stderr attach the process to the null device.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = headlessTermGrace
	if err := cmd.Start(); err != nil {
		unbind()
		cancel()
		defer span.End()
		failure := errors.New("the headless agent process could not start")
		span.RecordError(failure)
		span.SetStatus(codes.Error, "start failed")
		return nil, failure
	}
	turn := &headlessTurn{cancel: cancel, done: make(chan struct{})}
	go turn.wait(ctx, cmd, unbind, span)
	return turn, nil
}

// wait joins the leader and kills any descendants still in its process group.
// The result contains fixed messages and never process output or credentials.
func (turn *headlessTurn) wait(
	ctx context.Context, cmd *exec.Cmd, unbind func() bool, span trace.Span,
) {
	defer close(turn.done)
	defer span.End()
	defer turn.cancel()
	defer unbind()
	runErr := cmd.Wait()
	if err := procgroup.Stop(cmd.Process.Pid, headlessCleanupTimeout); err != nil {
		turn.stopErr = errors.New("a headless worker process group could not be stopped")
		span.RecordError(turn.stopErr)
		span.SetStatus(codes.Error, "cleanup failed")
		return
	}
	var failure error
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		failure = errors.New("the headless agent reached its turn deadline")
	case ctx.Err() == nil && runErr != nil:
		failure = errors.New("the headless agent process failed")
	}
	if failure != nil {
		span.RecordError(failure)
		span.SetStatus(codes.Error, "execution failed")
	}
}
