// This file passes a project server's memory-only state to the process that
// replaces it after an update. The state travels through an anonymous pipe
// the next process inherits, so no session credential or provider setting is
// ever written to a file, an argument or the environment.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/modelsettings"
	"github.com/goadesign/highbay/internal/project"
	"github.com/goadesign/highbay/internal/selfupdate"
)

type (
	// serverHandoff is what a project server holds in memory only and the
	// server replacing it needs so that open pages and model use continue:
	// the browser sessions and the control plane's model state. Everything
	// else the next server reads from the project's state folder or derives
	// from the environment it starts with, as on any start.
	serverHandoff struct {
		// Sessions maps each open browser session's credential to its
		// session id.
		Sessions map[string]string `json:"sessions"`
		// ModelSettings maps each provider whose request settings are held
		// to those settings, by variable name.
		ModelSettings map[string]map[string]string `json:"model_settings"`
		// ModelConnection is the model choice; nil when none was chosen.
		ModelConnection *genhighbay.Connection `json:"model_connection,omitempty"`
	}
)

// serverReplacement is an accepted update that has installed and prepared
// the next executable: what the server becomes once it has shut down.
type serverReplacement struct {
	// next is the installed executable to run.
	next string
	// release gives up the installer's lock on next's folder. Replacing
	// the process releases it too, so it is called only when that fails.
	release func() error
	// env is the environment the next server starts with.
	env []string
	// handoff is the old server's memory-only state, collected after its
	// last request finished.
	handoff serverHandoff
}

const (
	// handoffVariable names, in the next server's environment, the number
	// of the inherited descriptor that holds the handoff.
	handoffVariable = "HIGHBAY_HANDOFF_FD"
	// handoffLimit bounds what the next server reads. A handoff is a few
	// kilobytes; anything near this size is not one.
	handoffLimit = 1 << 20
	// handoffWriteWait bounds the one write of the handoff. The pipe has no
	// reader until the next process starts, so a handoff larger than the
	// pipe holds can never be written; it fails after this wait instead of
	// blocking the restart.
	handoffWriteWait = 2 * time.Second
	// nextSetupWait bounds each setup command of a newly installed build.
	// Installing a missing companion program downloads it.
	nextSetupWait = 5 * time.Minute
)

// newServerHandoff collects what the next server needs from this one.
func newServerHandoff(sessions map[string]string, model controlplane.ModelState) serverHandoff {
	handoff := serverHandoff{Sessions: sessions, ModelSettings: map[string]map[string]string{}}
	if handoff.Sessions == nil {
		handoff.Sessions = map[string]string{}
	}
	for _, settings := range model.Settings {
		values := map[string]string{}
		for _, entry := range settings.Environ() {
			name, value, _ := strings.Cut(entry, "=")
			values[name] = value
		}
		handoff.ModelSettings[settings.Provider()] = values
	}
	if kind := model.Connection.Kind(); kind != "" && kind != genhighbay.ConnectionKindNone {
		handoff.ModelConnection = &model.Connection
	}
	return handoff
}

// takeServerHandoff reads the handoff a replaced server left for this
// process, and removes its name from the environment so that no program this
// server starts sees it. It returns the zero handoff, and found false, when
// the process was started in the ordinary way. The descriptor is read to its
// end and closed exactly once.
func takeServerHandoff() (handoff serverHandoff, found bool, err error) {
	value, declared := os.LookupEnv(handoffVariable)
	if !declared {
		return serverHandoff{}, false, nil
	}
	if err := os.Unsetenv(handoffVariable); err != nil {
		return serverHandoff{}, false, fmt.Errorf("remove %s from the environment: %w", handoffVariable, err)
	}
	fd, err := strconv.ParseUint(value, 10, 31)
	if err != nil || fd < 3 {
		return serverHandoff{}, false, fmt.Errorf("%s does not name an inherited descriptor", handoffVariable)
	}
	file := os.NewFile(uintptr(fd), "server handoff")
	if file == nil {
		return serverHandoff{}, false, fmt.Errorf("%s does not name an open descriptor", handoffVariable)
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, handoffLimit+1))
	if err := errors.Join(readErr, file.Close()); err != nil {
		return serverHandoff{}, false, fmt.Errorf("read the server handoff: %w", err)
	}
	if len(raw) > handoffLimit {
		return serverHandoff{}, false, errors.New("the server handoff is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&handoff); err != nil {
		// A decoding error may quote a credential.
		return serverHandoff{}, false, errors.New("the server handoff is not valid")
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return serverHandoff{}, false, errors.New("the server handoff has trailing data")
	}
	return handoff, true, nil
}

// releaseUpdates binds the server's version and the published releases of
// github.com/goadesign/highbay for the executable this process runs.
func releaseUpdates() (project.UpdateDependencies, error) {
	exe, err := os.Executable()
	if err != nil {
		return project.UpdateDependencies{}, fmt.Errorf("locate the running executable: %w", err)
	}
	version := buildVersion()
	source := selfupdate.DefaultReleaseSource()
	return project.UpdateDependencies{
		Version: version,
		Latest:  source.Latest,
		CanAutoUpdate: func() (bool, string) {
			return selfupdate.CanAutoUpdate(exe, version, os.Getenv)
		},
		Install: func(ctx context.Context, target string) (string, func() error, error) {
			return selfupdate.Install(ctx, source, target, exe, io.Discard)
		},
		Prepare: func(ctx context.Context, next string) error {
			// Setup output can name private paths. Keep it out of the server output.
			return setupUpdatedHighbay(ctx, next, nextSetupWait, nil)
		},
	}, nil
}

// modelState returns the model state the handoff carries, checking each
// provider's settings as a hand-over from the highbay command is checked.
func (h serverHandoff) modelState() (controlplane.ModelState, error) {
	var state controlplane.ModelState
	for _, provider := range modelsettings.Providers() {
		values, held := h.ModelSettings[provider.Name]
		if !held {
			continue
		}
		settings, err := modelsettings.New(provider.Name, values)
		if err != nil {
			// The error may quote a value.
			return controlplane.ModelState{}, fmt.Errorf("the handed-over %s settings are not valid", provider.Name)
		}
		state.Settings = append(state.Settings, settings)
	}
	if len(state.Settings) != len(h.ModelSettings) {
		return controlplane.ModelState{}, errors.New("the handoff holds settings of an unknown model provider")
	}
	if h.ModelConnection != nil {
		state.Connection = *h.ModelConnection
	}
	return state, nil
}

// offer writes the handoff into a new pipe and returns the pipe's read end,
// which the next process inherits, and the environment entry that names it.
// The write end is closed before it returns, so the reader sees the end of
// the handoff. The caller keeps the returned file open until it replaces
// this process, and closes it if it does not.
func (h serverHandoff) offer() (*os.File, string, error) {
	raw, err := json.Marshal(h)
	if err != nil {
		return nil, "", errors.New("encode the server handoff")
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, "", fmt.Errorf("open the server handoff pipe: %w", err)
	}
	fail := func(err error) (*os.File, string, error) {
		return nil, "", errors.Join(err, write.Close(), read.Close())
	}
	if err := write.SetWriteDeadline(time.Now().Add(handoffWriteWait)); err != nil {
		return fail(fmt.Errorf("bound the server handoff write: %w", err))
	}
	if _, err := write.Write(raw); err != nil {
		return fail(fmt.Errorf("the server handoff (%d bytes) does not fit in a pipe: %w", len(raw), err))
	}
	if err := write.Close(); err != nil {
		return nil, "", errors.Join(fmt.Errorf("close the server handoff pipe: %w", err), read.Close())
	}
	// Fd puts the descriptor in blocking mode, as the next process expects of
	// one it inherits; clearing close-on-exec is what makes it inherited.
	fd := read.Fd()
	if _, err := unix.FcntlInt(fd, unix.F_SETFD, 0); err != nil {
		return nil, "", errors.Join(fmt.Errorf("pass the server handoff pipe on: %w", err), read.Close())
	}
	return read, handoffVariable + "=" + strconv.FormatUint(uint64(fd), 10), nil
}

// become replaces this process with the next executable serving stateDir,
// through replace, and passes the handoff on. It returns only when that
// failed: the installer's lock is then released and the process ends as a
// stopped server would, on the new build's next start.
func (r *serverReplacement) become(stateDir string, replace func(program string, argv, env []string) error) error {
	read, entry, err := r.handoff.offer()
	if err != nil {
		return errors.Join(err, r.release())
	}
	env := []string{entry}
	for _, kv := range r.env {
		if !strings.HasPrefix(kv, handoffVariable+"=") {
			env = append(env, kv)
		}
	}
	err = replace(r.next, []string{r.next, "serve", "-state", stateDir}, env)
	// Reached only when the process was not replaced. Until then the pipe
	// and the installer's lock must stay open.
	runtime.KeepAlive(read)
	runtime.KeepAlive(r.release)
	return errors.Join(fmt.Errorf("replace the project server with %s: %w", r.next, err), read.Close(), r.release())
}
