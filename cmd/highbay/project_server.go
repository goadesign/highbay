// This file composes the per-project server and its management client.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	genhighbayclient "github.com/goadesign/highbay/gen/http/highbay/client"
	genprojectclient "github.com/goadesign/highbay/gen/http/project/client"
	genprojectserver "github.com/goadesign/highbay/gen/http/project/server"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/modelsettings"
	"github.com/goadesign/highbay/internal/project"
	"go.opentelemetry.io/otel"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

// projectRuntime binds project state and process effects at the CLI composition root.
// Tests provide an owned state root and stand-ins without starting terminals or browsers.
type projectRuntime struct {
	// configRoot is supplied by the process composition root, never a package global.
	configRoot     func() (string, error)
	preflight      func(openBrowser bool) error
	startTerminal  func(context.Context, *project.State) (string, error)
	startDaemon    func(*project.State) (*projectDaemon, error)
	openViews      func(context.Context, string) error
	waitTerminal   func(context.Context, string) (bool, error)
	readTerminal   func(context.Context, string) (string, error) // an ended terminal's text, before removal
	removeTerminal func(context.Context, string) error
	// listen binds only the saved project address; tests observe the actual close boundary.
	listen func(string, string) (net.Listener, error)
	// teamDependencies binds the server login environment and accepted-work lifetime; its
	// []string holds the login environment for herdr-core and the agents it runs.
	teamDependencies func(context.Context, []string) (project.TeamDependencies, error)
	// updates binds how the server reports its version and installs a newer
	// release; serve adds the step that replaces the server. Nil serves a
	// development build that installs nothing.
	updates func() (project.UpdateDependencies, error)
	// replaceProcess replaces this process with the program at its first
	// argument, as syscall.Exec does. It returns only when that failed.
	replaceProcess func(program string, argv, env []string) error
}

// ownedProjectListener ends management authority before every underlying listener close,
// including the close deferred inside http.Server.Serve on an accept failure.
type ownedProjectListener struct {
	net.Listener
	management *project.Server
	// once prevents an old owner's repeated Close from removing its successor's record.
	once sync.Once
	// err preserves the first revocation and underlying-close result for every caller.
	err error
}

const (
	// projectDrainWait is how long requests in progress get to finish once
	// the server is asked to stop.
	projectDrainWait = 5 * time.Second
	// startOutputLines bounds how many of an ended server's last lines a start error shows:
	// its final error, including a few joined lines, and what led to it, in one screen.
	startOutputLines = 20
	// startOutputBytes bounds the same lines in bytes so one runaway line cannot flood the
	// user's terminal, while a long final error still fits whole.
	startOutputBytes = 4096
)

// productionProjectRuntime is selected only by the executable's composition root.
func productionProjectRuntime() projectRuntime {
	return projectRuntime{
		configRoot:       os.UserConfigDir,
		preflight:        preflightProjectStart,
		teamDependencies: projectTeamDependencies,
		startTerminal: func(ctx context.Context, state *project.State) (string, error) {
			return startProjectTerminal(ctx, state, projectHerdrProgram)
		},
		startDaemon: func(state *project.State) (*projectDaemon, error) {
			self, err := os.Executable()
			if err != nil {
				return nil, err
			}
			return startProjectDaemon(state, self)
		},
		readTerminal: func(ctx context.Context, terminal string) (string, error) {
			return readProjectTerminal(ctx, terminal, projectHerdrProgram)
		},
		openViews:      openProjectViews,
		waitTerminal:   waitProjectTerminal,
		removeTerminal: removeProjectTerminal,
		listen:         net.Listen,
		updates:        releaseUpdates,
		replaceProcess: syscall.Exec,
	}
}

// start serializes discovery and starts at most one server, then opens a one-use views link.
func (runtime projectRuntime) start(stdout io.Writer) error {
	return runtime.startProject(stdout, stdout, true)
}

// printLink delivers one fresh browser link as JSON, leaving all startup notices
// on the diagnostic writer so an automation client can consume stdout directly.
func (runtime projectRuntime) printLink(stdout, diagnostics io.Writer) error {
	return runtime.startProject(stdout, diagnostics, false)
}

// startProject shares server startup between the two link delivery routes.
// Only the final delivery and its opener prerequisite differ.
func (runtime projectRuntime) startProject(stdout, diagnostics io.Writer, openBrowser bool) error {
	if err := runtime.preflight(openBrowser); err != nil {
		return err
	}
	state, err := runtime.discover()
	if err != nil {
		return err
	}
	client, created, err := runtime.attachProject(context.Background(), state)
	if created {
		err = errors.Join(err, writeProjectCreatedNotice(diagnostics))
	}
	if err != nil {
		return err
	}

	// The server answered for this project, so this shell's model provider
	// settings go to it before anything else; a refusal stops the start.
	settingsCtx, stopSettings := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopSettings()
	if err := handOverModelSettings(settingsCtx, state, modelsettings.Select(os.Environ()), diagnostics); err != nil {
		return err
	}

	linkCtx, stopLink := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopLink()
	link, err := client.BrowserLink(linkCtx)
	if err != nil {
		return err
	}
	if err := validateProjectViewsLink(state, link.URL); err != nil {
		return err
	}
	if openBrowser {
		if err := runtime.openViews(linkCtx, link.URL); err != nil {
			return fmt.Errorf("open project views: %w", err)
		}
	} else if err := json.NewEncoder(stdout).Encode(genprojectserver.NewBrowserLinkResponseBody(link)); err != nil {
		return fmt.Errorf("write project views link: %w", err)
	}
	_, err = fmt.Fprintf(diagnostics, "Highbay is running for %s at %s\n", state.Record.Directory, state.Origin())
	return err
}

// attachProject waits for the project's authenticated owner. It observes only the
// server this command launches and leaves a working server under its own lifetime.
func (runtime projectRuntime) attachProject(
	parent context.Context, state *project.State,
) (client *genproject.Client, created bool, err error) {
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	type launchOutcome struct {
		ended bool
		err   error
	}
	var finished chan launchOutcome
	watch := func(wait func(context.Context) (bool, error)) {
		finished = make(chan launchOutcome, 1)
		go func() {
			ended, err := wait(ctx)
			finished <- launchOutcome{ended: ended, err: err}
			if ended || err != nil {
				cancel()
			}
		}()
	}

	var terminal string
	var daemon *projectDaemon
	client, created, err = attachOrStartProject(ctx, state, func(ctx context.Context, state *project.State) (string, error) {
		var err error
		terminal, err = runtime.startTerminal(ctx, state)
		if errors.Is(err, errProjectHerdrMissing) {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			daemon, err = runtime.startDaemon(state)
			if err != nil {
				return "", err
			}
			watch(daemon.wait)
			return fmt.Sprintf("the background project server (PID %d)", daemon.process.Pid), nil
		}
		if err != nil {
			return "", err
		}
		watch(func(ctx context.Context) (bool, error) { return runtime.waitTerminal(ctx, terminal) })
		return "the project server in Herdr terminal " + terminal, nil
	})
	if finished == nil {
		return client, created, err
	}
	// Reap the launch observer on readiness, failure, or deadline.
	cancel()
	outcome := <-finished
	if daemon != nil {
		defer func() { err = errors.Join(err, daemon.log.Close()) }()
	}
	if err == nil {
		return client, created, nil
	}
	err = errors.Join(err, outcome.err)
	if !outcome.ended {
		return nil, created, err
	}
	if daemon != nil {
		output, readErr := daemon.startupOutput()
		return nil, created, endedStartError(err, "startup log", output, readErr)
	}

	// The read and removal each get their own deadline so a hung read cannot
	// leave the removal with a deadline that has already passed.
	readCtx, stopRead := context.WithTimeout(context.Background(), 5*time.Second)
	output, readErr := runtime.readTerminal(readCtx, terminal)
	stopRead()
	err = endedStartError(err, "terminal output", output, readErr)
	removeCtx, stopRemove := context.WithTimeout(context.Background(), 5*time.Second)
	defer stopRemove()
	return nil, created, errors.Join(err, runtime.removeTerminal(removeCtx, terminal))
}

// validateProjectViewsLink confines the browser opener to the project's exact views
// address and opaque link format, without returning an untrusted URL in an error.
func validateProjectViewsLink(state *project.State, address string) error {
	token, ok := strings.CutPrefix(address, state.Origin()+"/views#link=")
	if !ok || len(token) != 64 {
		return errors.New("project server returned an invalid views link; run highbay again")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return errors.New("project server returned an invalid views link; run highbay again")
	}
	return nil
}

// writeProjectCreatedNotice explains the folder-identity rule whenever the CLI creates a project.
func writeProjectCreatedNotice(stdout io.Writer) error {
	_, err := fmt.Fprintln(stdout, "Started a Highbay project for this folder. Moving or renaming this folder starts a new project.")
	return err
}

// attachOrStartProject holds the start lock until one authenticated server answers.
func attachOrStartProject(
	ctx context.Context,
	state *project.State,
	launch func(context.Context, *project.State) (string, error),
) (client *genproject.Client, created bool, err error) {
	lock, err := state.StartLock(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	created, err = state.Initialize(fmt.Sprintf("uid:%d", os.Geteuid()))
	if err != nil {
		return nil, created, err
	}
	inUse, err := controlplane.CardStoreInUse(state.CardsDir())
	if err != nil {
		return nil, created, err
	}
	description := ""
	if !inUse {
		if err := state.RemoveRunning(); err != nil {
			return nil, created, err
		}
		listener, err := net.Listen("tcp", state.Address())
		if err != nil {
			return nil, created, takenPortError(state, err)
		}
		if err := listener.Close(); err != nil {
			return nil, created, err
		}
		if description, err = launch(ctx, state); err != nil {
			return nil, created, err
		}
	}
	client = projectClient(state)
	identity, err := confirmProject(ctx, state, client)
	if err != nil {
		if inUse {
			return nil, created, fmt.Errorf("%s; no second server was started: %w", projectHolder(state), err)
		}
		return nil, created, fmt.Errorf("%s did not start answering: %w", description, err)
	}
	if err := confirmBuild(identity); err != nil {
		return nil, created, err
	}
	return client, created, nil
}

// takenPortError names what holds a stopped project's saved port and the step that frees it.
// The holder is another project's server when a project sharing the port, saved before new
// projects avoided each other's ports, holds its card store; otherwise it is another program.
// The saved port remains stable for local management and active agents.
func takenPortError(state *project.State, listenErr error) error {
	port := state.Record.Port
	if !errors.Is(listenErr, syscall.EADDRINUSE) {
		return fmt.Errorf("project port %d could not be used; no server was started: %w", port, listenErr)
	}
	sharing, err := state.ProjectsSharingPort()
	var checkErrs []error
	if err != nil {
		checkErrs = append(checkErrs, err)
	}
	for _, other := range sharing {
		inUse, err := controlplane.CardStoreInUse(other.CardsDir())
		if err != nil {
			checkErrs = append(checkErrs, err)
			continue
		}
		if inUse {
			return fmt.Errorf("project port %d is held by the Highbay server of the project in %s, which saved the same port, so only one of the two can run at a time; no server was started; run highbay stop in that folder, then run highbay here", port, other.Record.Directory)
		}
	}
	taken := fmt.Errorf("another program is listening on %s, the address saved for this project; no server was started; stop that program, then run highbay (lsof -nP -iTCP:%d -sTCP:LISTEN names it)", state.Address(), port)
	if len(checkErrs) > 0 {
		return errors.Join(taken, fmt.Errorf("other Highbay projects could not be checked for this port: %w", errors.Join(checkErrs...)))
	}
	return taken
}

// endedStartError explains a launch that ended before the server answered.
// The server's own last lines replace the attach failure, which then only reports the end's
// consequence; without readable lines, the attach failure stays and says why none are shown.
func endedStartError(attachErr error, source, output string, readErr error) error {
	if readErr != nil {
		return errors.Join(attachErr, fmt.Errorf("the project server's %s could not be read: %w", source, readErr))
	}
	lines := lastServerLines(output)
	if lines == "" {
		return errors.Join(attachErr, fmt.Errorf("the project server's %s could not be read: it showed no printable text", source))
	}
	return fmt.Errorf("the project server stopped while starting, and its last lines were:\n%s", lines)
}

// lastServerLines keeps the end of a server terminal's text within startOutputLines and
// startOutputBytes, as printable text indented under the CLI's sentence. Blank rows around
// the text are dropped; a cut long line starts with an ellipsis. Empty means nothing to show.
func lastServerLines(output string) string {
	lines := strings.Split(printableTerminalText(output), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	lines = lines[len(lines)-min(len(lines), startOutputLines):]
	text := strings.TrimLeft(strings.Join(lines, "\n"), "\n")
	if len(text) > startOutputBytes {
		text = text[len(text)-startOutputBytes:]
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			text = strings.TrimLeft(text[newline+1:], "\n")
		} else {
			for len(text) > 0 && !utf8.RuneStart(text[0]) {
				text = text[1:]
			}
			text = "…" + text
		}
	}
	if text == "" {
		return ""
	}
	lines = strings.Split(text, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = "  " + line
		}
	}
	return strings.Join(lines, "\n")
}

// printableTerminalText keeps what a terminal shows as text: escape sequences, control
// characters and invalid bytes are dropped, newlines are kept and tabs become spaces.
func printableTerminalText(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		i += size
		switch {
		case r == '\x1b':
			i = skipEscapeSequence(text, i)
		case r == '\n':
			out.WriteByte('\n')
		case r == '\t':
			out.WriteByte(' ')
		case r == utf8.RuneError && size == 1:
			// An invalid byte is not text and is dropped.
		case unicode.IsPrint(r):
			out.WriteRune(r)
		}
	}
	return out.String()
}

// skipEscapeSequence returns the index just past the escape sequence whose ESC precedes
// index i: a control sequence (ESC [), a control string ended by BEL or ESC \ (ESC ] P X ^ _),
// or ESC with intermediates and a final byte. A newline always ends a sequence and is kept.
func skipEscapeSequence(text string, i int) int {
	if i >= len(text) {
		return i
	}
	switch text[i] {
	case '[':
		for i++; i < len(text) && text[i] >= 0x20 && text[i] <= 0x3f; i++ {
		}
		if i < len(text) && text[i] >= 0x40 && text[i] <= 0x7e {
			i++
		}
		return i
	case ']', 'P', 'X', '^', '_':
		for i++; i < len(text); i++ {
			switch {
			case text[i] == '\a':
				return i + 1
			case text[i] == '\x1b' && i+1 < len(text) && text[i+1] == '\\':
				return i + 2
			case text[i] == '\x1b', text[i] == '\n':
				return i
			}
		}
		return i
	}
	for i < len(text) && text[i] >= 0x20 && text[i] <= 0x2f {
		i++
	}
	if i < len(text) && text[i] >= 0x30 && text[i] <= 0x7e {
		i++
	}
	return i
}

// stop authenticates the current project and waits until ordered cleanup releases the store.
func (runtime projectRuntime) stop(args []string, stdout io.Writer) (err error) {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("stop takes no arguments")
	}
	state, err := runtime.discover()
	if err != nil {
		return err
	}
	if state.Record.ID == "" {
		return errors.New("this folder has no Highbay project")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	lock, err := state.StartLock(ctx)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	inUse, err := controlplane.CardStoreInUse(state.CardsDir())
	if err != nil {
		return err
	}
	if !inUse {
		_, err := fmt.Fprintln(stdout, "Highbay is stopped.")
		return err
	}
	client := projectClient(state)
	if _, err := confirmProject(ctx, state, client); err != nil {
		return fmt.Errorf("%s: %w", projectHolder(state), err)
	}
	if err := client.Stop(ctx); err != nil {
		return err
	}
	for {
		held, err := controlplane.CardStoreInUse(state.CardsDir())
		if err != nil {
			return err
		}
		if !held {
			_, err := fmt.Fprintln(stdout, "Highbay stopped.")
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("server has not finished stopping: %w", ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// projectHolder describes a held lock without claiming the listener is ready.
// Terminal metadata is optional; unavailable metadata does not change the lock's fact.
func projectHolder(state *project.State) string {
	if running, err := state.ReadRunning(); err == nil && running.Terminal != "" {
		return fmt.Sprintf("a server in Herdr terminal %s holds this project and may still be starting or stopping", running.Terminal)
	}
	return "a server holds this project and may still be starting or stopping"
}

// serve is the child server entry. stateDir is the private state folder the
// launching `highbay` found and passed with -state; project identity and address
// come from the record there, and serve never derives a folder from its own
// environment or working directory.
//
// A server asked to install a newer release shuts down exactly as on stop
// and then replaces its own process with the new executable, which serves
// the same state folder on the same address. The memory-only state of the
// old process reaches the new one through the handoff it takes first.
func (runtime projectRuntime) serve(stateDir string) error {
	handoff, _, err := takeServerHandoff()
	if err != nil {
		return err
	}
	replacement, err := runtime.serveUntilStopped(stateDir, handoff)
	if replacement == nil {
		return err
	}
	if err != nil {
		// The shutdown failed: end as a stopped server, on the old build.
		return errors.Join(err, replacement.release())
	}
	return replacement.become(stateDir, runtime.replaceProcess)
}

// serveUntilStopped serves the project until it is asked to stop and shuts
// down in order. handoff is what the server this one replaced held in memory;
// it is empty on an ordinary start. The result names the executable to
// become when the stop was an accepted update, and nil otherwise.
func (runtime projectRuntime) serveUntilStopped(stateDir string, handoff serverHandoff) (replacement *serverReplacement, err error) {
	// Provider settings reach the server only through hand_over_model_settings: none that
	// Herdr's core environment holds may supply a live target, a verification or a check.
	herdrEnv, err := takeProviderFamilies()
	if err != nil {
		return nil, err
	}
	state, err := project.Open(stateDir)
	if err != nil {
		return nil, err
	}
	provider, err := newTraceProvider(context.Background(), os.Stderr, os.Getenv)
	if err != nil {
		return nil, err
	}
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err = errors.Join(err, provider.Shutdown(ctx))
		otel.SetTracerProvider(previous)
	}()
	opts, err := projectServeOptions(state, os.Environ())
	if err != nil {
		return nil, err
	}
	opts.TracerProvider = provider
	if opts.Model, err = handoff.modelState(); err != nil {
		return nil, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()
	team, err := runtime.teamDependencies(ctx, herdrEnv)
	if err != nil {
		return nil, err
	}
	management, err := project.NewServer(state, stop, team)
	if err != nil {
		return nil, err
	}
	if err := management.RestoreBrowserSessions(handoff.Sessions); err != nil {
		return nil, err
	}
	// pending is set once, by an accepted update, before it starts the stop.
	var pending atomic.Pointer[serverReplacement]
	if runtime.updates != nil {
		updates, err := runtime.updates()
		if err != nil {
			return nil, err
		}
		updates.Replace = func(next string, release func() error) {
			// The next server starts with the environment this one started
			// with, so it finds the same programs and settings.
			pending.Store(&serverReplacement{next: next, release: release, env: herdrEnv})
			stop()
		}
		management.BindUpdates(updates)
	}
	var cp *controlplane.ControlPlane
	defer func() {
		stop()
		if cp != nil {
			// The control plane now owns the team and drains it before
			// releasing the writer lock.
			err = errors.Join(err, cp.Close())
			if replacement != nil {
				// The HTTP server and the control plane have both stopped,
				// so no request can change a session or a setting any more.
				replacement.handoff = newServerHandoff(management.BrowserSessions(), cp.ModelState())
			}
			return
		}
		err = errors.Join(err, management.Close())
	}()
	management.BindViewsOpener(runtime.openViews)
	opts.OpenViews = management.OpenViews
	opts.Team = &controlplane.TeamBinding{Owner: state.Record.Owner, Service: management}
	// A worker's open waiting read must end when the stop begins, not when
	// the HTTP server gives up waiting for it.
	opts.Stopping = ctx
	cp, err = controlplane.New(opts)
	if err != nil {
		return nil, err
	}
	management.BindStatusReader(cp.ProjectStatus)

	listener, err := runtime.listen("tcp", state.Address())
	if err != nil {
		return nil, fmt.Errorf("project port %d is occupied: %w; stop the program listening on it, then run highbay", state.Record.Port, err)
	}
	application := http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(cp, management.Authenticate))
	server := &http.Server{Handler: management.Handler(application), ReadHeaderTimeout: 10 * time.Second}
	writeServeStatus(os.Stderr, listener.Addr().(*net.TCPAddr), opts)
	err = serveProjectHTTP(ctx, server, &ownedProjectListener{Listener: listener, management: management})
	return pending.Load(), err
}

// Close irreversibly revokes management access and withdraws its record before
// closing the real listener, including on a failed withdrawal. The result is stable.
func (l *ownedProjectListener) Close() error {
	l.once.Do(func() {
		revokeErr := l.management.RevokeManagement()
		l.err = errors.Join(revokeErr, l.Listener.Close())
	})
	return l.err
}

// serveProjectHTTP publishes management access only for an owned listener and drains
// accepted requests during ordered shutdown so the authorized Stop response can finish.
func serveProjectHTTP(
	ctx context.Context,
	server *http.Server,
	listener *ownedProjectListener,
) error {
	return serveProjectHTTPWithin(ctx, server, listener, projectDrainWait)
}

// serveProjectHTTPWithin is serveProjectHTTP with the time accepted requests
// get to finish once the stop begins. A request still open after drain is cut
// off by closing its connection: the stop, or the replacement of the server
// by a newer build, was already answered and must not fail because another
// request is slow.
func serveProjectHTTPWithin(
	ctx context.Context,
	server *http.Server,
	listener *ownedProjectListener,
	drain time.Duration,
) (err error) {
	defer func() { err = errors.Join(err, listener.Close()) }()
	if err := listener.management.PublishRunning(os.Getpid(), os.Getenv("HC_TERMINAL_ID")); err != nil {
		return err
	}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	var serveErr error
	select {
	case err := <-result:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drain)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	if shutdownErr != nil && shutdownCtx.Err() != nil {
		shutdownErr = server.Close()
	}
	return errors.Join(serveErr, shutdownErr)
}

// parseServeArgs returns the state folder named by serve's one required flag,
// -state, which the project launcher supplies; serve is an internal
// entrypoint and accepts no other option or argument.
func parseServeArgs(args []string) (string, error) {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	stateDir := fs.String("state", "", "private project state folder, passed by highbay when it starts the server")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() != 0 {
		return "", fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if *stateDir == "" {
		return "", errors.New("serve requires -state; start the project with highbay")
	}
	return *stateDir, nil
}

// discover uses physical folders and nearest records, independent of the working tree root.
func (runtime projectRuntime) discover() (*project.State, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := runtime.operatorRoot(cwd)
	if err != nil {
		return nil, err
	}
	return project.Discover(root, cwd)
}

// projectClient uses the literal project address with a private, proxy-free transport.
// Each generated management request obtains the currently published credential.
func projectClient(state *project.State) *genproject.Client {
	client := projectHTTPClient()
	teamClient := teamHTTPClient()
	return genproject.NewClient(
		managementEndpoint(state, client, (*genprojectclient.Client).Identity),
		managementEndpoint(state, client, (*genprojectclient.Client).EnsureTeam),
		managementEndpoint(state, teamClient, (*genprojectclient.Client).AttachGuide),
		managementEndpoint(state, client, (*genprojectclient.Client).Status),
		managementEndpoint(state, client, (*genprojectclient.Client).Stop),
		managementEndpoint(state, client, (*genprojectclient.Client).BrowserLink),
		managementEndpoint(state, client, (*genprojectclient.Client).ExchangeBrowserLink),
		managementEndpoint(state, client, (*genprojectclient.Client).GetBrowserSession),
		managementEndpoint(state, teamClient, (*genprojectclient.Client).GetUpdateStatus),
		managementEndpoint(state, updateHTTPClient(), (*genprojectclient.Client).ApplyUpdate),
	)
}

// managementEndpoint preserves generated transport behavior without printing peer-authored
// error text for one project service operation (see managementCall).
func managementEndpoint(
	state *project.State,
	client *http.Client,
	endpoint func(*genprojectclient.Client) goa.Endpoint,
) goa.Endpoint {
	return managementCall(state, client, func(doer goahttp.Doer) goa.Endpoint {
		return endpoint(genprojectclient.NewClient("http", state.Address(), doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false))
	})
}

// modelSettingsEndpoint is the management call of the highbay service's
// hand_over_model_settings, with the same credential, retry and wording as
// the project service's management calls.
func modelSettingsEndpoint(state *project.State) goa.Endpoint {
	return managementCall(state, projectHTTPClient(), func(doer goahttp.Doer) goa.Endpoint {
		return genhighbayclient.NewClient("http", state.Address(), doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false, nil, nil).HandOverModelSettings()
	})
}

// managementCall preserves generated transport behavior without printing peer-authored
// error text. endpoint builds the generated operation on a fresh Doer for each invocation.
// Only declared error names select fixed wording; other replies name status. The CLI
// and highbay stdio's guide attachment share these errors, so a retry is worded "try
// again", which is running highbay again for one and calling the tool again for the other.
// Response status belongs to this invocation's Doer, including a changed-key retry.
func managementCall(
	state *project.State,
	client *http.Client,
	endpoint func(goahttp.Doer) goa.Endpoint,
) goa.Endpoint {
	return func(ctx context.Context, payload any) (any, error) {
		doer := &managementDoer{state: state, client: client}
		result, err := endpoint(doer)(ctx, payload)
		if err == nil {
			return result, nil
		}
		if doer.localErr != nil {
			return nil, doer.localErr
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, managementConnectionError(ctx, err)
		}
		if doer.lastStatus == 0 {
			return nil, err
		}
		var responseErr *goahttp.ClientError
		if errors.As(err, &responseErr) && responseErr.Name == "invalid_response" {
			if responseErr.Method == "hand_over_model_settings" && doer.lastStatus == http.StatusBadRequest {
				return nil, fmt.Errorf("the project server refused the model settings from this shell (HTTP %d); "+
					"each ANTHROPIC_ and OPENAI_ request setting must be at most %d characters", doer.lastStatus, modelsettings.MaxValueLength)
			}
			return nil, goahttp.ErrInvalidResponse(responseErr.Service, responseErr.Method, doer.lastStatus, "")
		}
		var unavailable *genproject.TeamUnavailable
		if errors.As(err, &unavailable) {
			return nil, fmt.Errorf("%s (HTTP %d)", teamUnavailableStep(state, unavailable.Cause), doer.lastStatus)
		}
		var serviceErr *goa.ServiceError
		if errors.As(err, &serviceErr) {
			switch serviceErr.Name {
			case "forbidden":
				return nil, fmt.Errorf("project management operation is not permitted (HTTP %d); try again", doer.lastStatus)
			case "unprocessable":
				return nil, fmt.Errorf("project management request could not be applied (HTTP %d)", doer.lastStatus)
			case "conflict":
				return nil, fmt.Errorf("the project server did not install the update (HTTP %d): another update is in progress, "+
					"this installation cannot update itself, or a newer version is already installed; "+
					"stop the server with highbay stop, then run highbay update again", doer.lastStatus)
			case "release_unavailable":
				return nil, fmt.Errorf("the project server could not download or verify that Highbay release (HTTP %d); "+
					"check the network connection and try again", doer.lastStatus)
			}
		}
		return nil, fmt.Errorf("project server response could not be used (HTTP %d); try again", doer.lastStatus)
	}
}

// teamUnavailableStep is the fixed text for one team failure cause of attach_guide,
// the only management operation that returns one. highbay stdio calls it when a
// guide's first Highbay tool call attaches the agent, so the text names what could
// not be used, its repair, and the retry: calling the Highbay tool again.
func teamUnavailableStep(state *project.State, cause string) string {
	const retry = "then call the Highbay tool again"
	switch cause {
	case "stopping":
		return "the project server is stopping; call the Highbay tool again to start it"
	case "chorus":
		return "Chorus is not running or its settings are invalid. Run chorus up (or chorus status to see the error), " + retry
	case "hc":
		return "herdr-core (hc), which runs each agent's terminal, did not complete a terminal command; run hc server status to see the error (highbay up installs and starts it), " + retry
	case "project_state":
		return fmt.Sprintf("a file in this project's private state %s could not be used: its record (project.json), its agents folder or the guide's credential in that folder. If that file lost its permissions or owner, make it this account's and usable only by it (chmod 600 on that file, or chmod 700 on the agents folder); if it is missing or damaged, restore it from a backup; %s", state.Dir, retry)
	case "role_folder":
		return fmt.Sprintf("a team role's working folder could not be read; make sure this account can open the project folder %s and its build working folders, %s", state.Record.Directory, retry)
	}
	return "project team could not be prepared; call the Highbay tool again"
}

// projectHTTPClient keeps management requests off proxies and refuses redirects,
// including any proxy defaults inherited from the process environment.
func projectHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DisableKeepAlives = true
	transport.ForceAttemptHTTP2 = false
	transport.Protocols = new(http.Protocols)
	transport.Protocols.SetHTTP1(true)
	return &http.Client{
		Transport:     transport,
		Timeout:       time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// teamHTTPClient gives bounded team operations enough time for herdr-core and Chorus calls.
func teamHTTPClient() *http.Client {
	client := projectHTTPClient()
	client.Timeout = 50 * time.Second
	return client
}

// updateHTTPClient has no time limit of its own: apply_update answers only
// after the server downloaded, verified and installed a release and ran the
// new build's setup, and the caller's context bounds that.
func updateHTTPClient() *http.Client {
	client := projectHTTPClient()
	client.Timeout = 0
	return client
}

// managementDoer reads current authority per request and allows only one retry after
// an explicit refusal and a changed credential. It never retries an unchanged credential.
type managementDoer struct {
	state  *project.State
	client *http.Client
	// lastStatus is private to one generated endpoint invocation, never shared across calls.
	lastStatus int
	// localErr retains a private-file failure without a peer-authored error wrapper.
	localErr error
}

// Do reads authority before opening a fresh connection for this invocation.
func (d *managementDoer) Do(r *http.Request) (*http.Response, error) {
	running, err := waitForRunning(r.Context(), d.state)
	if err != nil {
		d.localErr = err
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+running.ManagementCredential)
	d.lastStatus = 0
	response, err := d.client.Do(r)
	if response != nil {
		d.lastStatus = response.StatusCode
	}
	if err != nil {
		return nil, managementConnectionError(r.Context(), err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, err
	}
	current, readErr := d.state.ReadRunning()
	if readErr != nil || current.ManagementCredential == running.ManagementCredential {
		if readErr != nil {
			d.localErr = fmt.Errorf("project management access was refused; try again: %w", readErr)
		}
		return nil, errors.Join(errors.New("project management access was refused; try again"), d.localErr, managementConnectionError(r.Context(), response.Body.Close()))
	}
	if err := response.Body.Close(); err != nil {
		return nil, managementConnectionError(r.Context(), err)
	}
	retry := r.Clone(r.Context())
	if r.Body != nil && r.Body != http.NoBody {
		if r.GetBody == nil {
			return nil, errors.New("cannot retry management request body")
		}
		retry.Body, err = r.GetBody()
		if err != nil {
			return nil, err
		}
	}
	retry.Header.Set("Authorization", "Bearer "+current.ManagementCredential)
	d.lastStatus = 0
	response, err = d.client.Do(retry)
	if response != nil {
		d.lastStatus = response.StatusCode
	}
	if err != nil {
		return nil, managementConnectionError(r.Context(), err)
	}
	return response, err
}

// managementConnectionError keeps malformed HTTP and transport diagnostics out of
// CLI output while retaining cancellation/deadline identity without the raw peer error.
func managementConnectionError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return fmt.Errorf("project server request did not finish: %w", ctx.Err())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("project server request did not finish: %w", context.DeadlineExceeded)
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("project server request did not finish: %w", context.Canceled)
	}
	return errors.New("project server connection failed; try again")
}

// waitForRunning waits without network traffic while startup has not published access.
// Corrupt records fail immediately; absence ends at the caller's existing deadline.
func waitForRunning(ctx context.Context, state *project.State) (project.Running, error) {
	for {
		running, err := state.ReadRunning()
		if err == nil {
			return running, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return project.Running{}, err
		}
		select {
		case <-ctx.Done():
			return project.Running{}, fmt.Errorf("project server has not published management access; try again: %w", ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// errWrongProject ends attachment before any further management operation is sent.
var errWrongProject = errors.New("the saved port answered for another project")

// errOtherBuild refuses to attach to a server another highbay build started, such as the
// one still running after an upgrade: it would serve its own tools and page, and start
// the new executable for its child processes. highbay stop accepts any build.
var errOtherBuild = errors.New("this project's server was started by another build of highbay; run highbay stop, then highbay")

// confirmProject verifies the typed identity before trusting a saved port's answering process.
// It accepts a server of any build, so highbay stop reaches one an earlier build started.
func confirmProject(ctx context.Context, state *project.State, client *genproject.Client) (*genproject.ProjectIdentity, error) {
	identity, err := client.Identity(ctx)
	if err != nil {
		return nil, err
	}
	if identity.ProjectID != state.Record.ID || identity.Directory != state.Record.Directory || identity.Owner != state.Record.Owner {
		return nil, errWrongProject
	}
	return identity, nil
}

// confirmBuild attaches only to a server started by this build; a server that reports no
// build predates the field and is another build.
func confirmBuild(identity *genproject.ProjectIdentity) error {
	build, err := project.BuildIdentity()
	if err != nil {
		return err
	}
	if identity.Build == nil || *identity.Build != build {
		return errOtherBuild
	}
	return nil
}

// startProjectTerminal delegates process lifetime to a parentless Herdr terminal.
// The server gets the state folder this command found as -state, because a Herdr terminal runs with the
// environment of Herdr's core, not this shell's, and could otherwise resolve another configuration folder.
func startProjectTerminal(
	ctx context.Context,
	state *project.State,
	findHerdr func() (string, error),
) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	hc, err := findHerdr()
	if err != nil {
		return "", err
	}
	argv := []string{"run", "--json", "--no-parent", "--bg", "--label", "highbay_project=" + state.Record.ID, "--cwd", state.Record.Directory, "--", self, "serve", "-state", state.Dir}
	command := exec.CommandContext(ctx, hc, argv...)
	command.Stdin = nil
	var stderr strings.Builder
	command.Stderr = &stderr
	raw, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("start Herdr server terminal: %w: %s; ensure Herdr is installed and hc is available", err, strings.TrimSpace(stderr.String()))
	}
	var reply struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", fmt.Errorf("decode Herdr server terminal response: %w", err)
	}
	if reply.Address == "" {
		return "", errors.New("Herdr returned no server terminal address")
	}
	return reply.Address, nil
}

// errProjectHerdrMissing selects a detached launch when hc is unavailable.
var errProjectHerdrMissing = errors.New("Herdr hc is unavailable")

// projectHerdrProgram resolves the optional terminal executable without starting it.
func projectHerdrProgram() (string, error) {
	hc, err := resolveServerCompanion("hc")
	if err != nil {
		return "", errProjectHerdrMissing
	}
	return hc, nil
}

// resolveServerCompanion locates a companion executable on PATH or in the default
// standalone directory (~/.local/bin) when a desktop client starts the project
// server with a stripped GUI PATH.
func resolveServerCompanion(name string) (string, error) {
	resolved, err := exec.LookPath(name)
	if err == nil {
		return resolved, nil
	}
	if !strings.ContainsRune(name, filepath.Separator) {
		if home, homeErr := os.UserHomeDir(); homeErr == nil {
			if local, localErr := exec.LookPath(filepath.Join(home, ".local", "bin", name)); localErr == nil {
				return local, nil
			}
		}
	}
	return "", err
}

// preflightProjectStart refuses unavailable launch effects before creating project state.
func preflightProjectStart(openBrowser bool) error {
	if openBrowser {
		if _, err := projectBrowserProgram(); err != nil {
			return err
		}
	}
	return preflightProjectGit()
}

// projectBrowserProgram resolves the required platform opener without exposing a link.
func projectBrowserProgram() (string, error) {
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	program, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s is required to open Highbay views; install it and make it available: %w", name, err)
	}
	return program, nil
}

// openProjectViews is the only launch whose argument is allowed to contain a one-use browser credential.
// The opener never receives the shell's model provider settings.
func openProjectViews(ctx context.Context, url string) error {
	program, err := projectBrowserProgram()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, program, url)
	command.Env = withoutProviderFamilies(os.Environ())
	return command.Run()
}

// waitProjectTerminal observes the owned launch while startup waits for authenticated readiness.
// Canceling its context reaps the hc observer without terminating the server.
func waitProjectTerminal(ctx context.Context, terminal string) (bool, error) {
	hc, err := projectHerdrProgram()
	if err != nil {
		return false, err
	}
	command := exec.CommandContext(ctx, hc, "wait", "--json", "--timeout", "40", terminal)
	command.Stdin = nil
	var stderr strings.Builder
	command.Stderr = &stderr
	raw, err := command.Output()
	if ctx.Err() != nil {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("observe Herdr terminal %s: %w: %s", terminal, err, strings.TrimSpace(stderr.String()))
	}
	var result struct {
		Ended bool `json:"ended"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return false, fmt.Errorf("decode Herdr terminal %s status: %w", terminal, err)
	}
	return result.Ended, nil
}

// readProjectTerminal returns the text an ended startup terminal printed, standard output and
// error in order, asking Herdr for the screen plus the history lines a start error can show.
// The terminal must still exist, so start reads it before removing it.
func readProjectTerminal(
	ctx context.Context,
	terminal string,
	findHerdr func() (string, error),
) (string, error) {
	hc, err := findHerdr()
	if err != nil {
		return "", err
	}
	command := exec.CommandContext(ctx, hc, "read", "--json", "--lines", strconv.Itoa(startOutputLines), terminal)
	command.Stdin = nil
	var stderr strings.Builder
	command.Stderr = &stderr
	raw, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("read Herdr terminal %s: %w: %s", terminal, err, strings.TrimSpace(stderr.String()))
	}
	var reply struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return "", fmt.Errorf("decode Herdr terminal %s output: %w", terminal, err)
	}
	return reply.Text, nil
}

// removeProjectTerminal removes only a startup terminal whose end the observer confirmed.
func removeProjectTerminal(ctx context.Context, terminal string) error {
	hc, err := projectHerdrProgram()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, hc, "rm", "--json", terminal)
	command.Stdin = nil
	output, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("remove failed Herdr terminal %s: %w: %s", terminal, err, strings.TrimSpace(string(output)))
	}
	return nil
}
