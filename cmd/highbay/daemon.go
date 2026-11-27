// This file launches a project server when no Herdr terminal is available.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"github.com/goadesign/highbay/internal/project"
)

// projectDaemon holds the launch's particular log file through the readiness
// probe, even if a concurrent start later replaces the directory entry.
type projectDaemon struct {
	log     *os.File
	process *os.Process
	exited  <-chan error
}

// startProjectDaemon starts the same private-state serve entry as Herdr, with a
// detached process session and no inherited terminal or standard I/O pipes.
// The caller holds the project's start lock and closes the returned log.
func startProjectDaemon(state *project.State, self string) (*projectDaemon, error) {
	log, err := os.CreateTemp(state.Dir, ".serve-startup-*")
	if err != nil {
		return nil, fmt.Errorf("create project server startup log: %w", err)
	}
	// Replace the old file instead of opening and truncating any existing
	// link. The new regular file is created with private permissions.
	if err := os.Rename(log.Name(), filepath.Join(state.Dir, "serve-startup.log")); err != nil {
		return nil, errors.Join(fmt.Errorf("publish project server startup log: %w", err), log.Close(), os.Remove(log.Name()))
	}
	command := exec.Command(self, "serve", "-state", state.Dir)
	command.Dir = state.Record.Directory
	command.Env = append(os.Environ(), "HC_TERMINAL_ID=")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	command.Stdout = log
	command.Stderr = log
	if err := command.Start(); err != nil {
		return nil, errors.Join(fmt.Errorf("start background project server: %w", err), log.Close())
	}
	exited := make(chan error, 1)
	go func() {
		exited <- command.Wait()
		close(exited)
	}()
	return &projectDaemon{log: log, process: command.Process, exited: exited}, nil
}

// wait observes exit during readiness. Cancellation stops only the observer.
func (d *projectDaemon) wait(ctx context.Context) (bool, error) {
	select {
	case err := <-d.exited:
		return true, err
	case <-ctx.Done():
		// Prefer an exit that the reaper has already reported.
		select {
		case err := <-d.exited:
			return true, err
		default:
			return false, nil
		}
	}
}

// startupOutput reads only the bounded tail of our ended daemon's private log.
func (d *projectDaemon) startupOutput() (string, error) {
	info, err := d.log.Stat()
	if err != nil {
		return "", err
	}
	size := min(info.Size(), startOutputBytes)
	text := make([]byte, size)
	n, err := d.log.ReadAt(text, info.Size()-size)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return string(text[:n]), nil
}
