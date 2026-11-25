// This file implements "hbay up": the one command a person runs after
// installing hbay. It runs `hbay setup -global` and starts Chorus when it
// is installed, so agents of the same project can message each other.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// chorusUpTimeout bounds `chorus up`, which returns once its server answers.
const chorusUpTimeout = time.Minute

// runUp implements `hbay up` for the CLI: global setup unless HIGHBAY_NO_SETUP
// is 1, then `chorus up` when chorus is on PATH.
func runUp(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	var env *setupEnvironment
	if os.Getenv("HIGHBAY_NO_SETUP") != "1" {
		processEnv, err := processSetupEnvironment()
		if err != nil {
			return err
		}
		env = &processEnv
	}
	chorus, err := exec.LookPath("chorus")
	if err != nil {
		chorus = ""
	}
	return upWith(env, chorus, startChorus, stdout)
}

// upWith runs global setup with env (skipped when env is nil), then starts
// Chorus with start when chorus names its executable. Missing Chorus is
// reported, not an error: Highbay works without it, but its agents cannot
// message each other. A failed setup or Chorus start is an error naming how
// to continue.
func upWith(
	env *setupEnvironment, chorus string, start func(context.Context, string, io.Writer) error, out io.Writer,
) error {
	if env != nil {
		if err := setupGlobal(*env, out); err != nil {
			return fmt.Errorf("%w (fix that file and run hbay up again, or set HIGHBAY_NO_SETUP=1 to skip setup)", err)
		}
	}
	if chorus == "" {
		fmt.Fprintln(out, "Chorus is not installed, so Highbay's agents will work without messaging each other.")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), chorusUpTimeout)
		defer cancel()
		if err := start(ctx, chorus, out); err != nil {
			return fmt.Errorf("start Chorus: %w", err)
		}
	}
	_, err := fmt.Fprintln(out, "Highbay is ready: open your coding agent in a project folder and type /highbay.")
	return err
}

// startChorus runs `chorus up` with no input and its output on out. Chorus
// starts its server in the background and returns once it answers. The
// long-lived Chorus server gets this shell's environment without its model
// provider settings, which only the project server may hold.
func startChorus(ctx context.Context, chorus string, out io.Writer) error {
	command := exec.CommandContext(ctx, chorus, "up")
	command.Env = withoutProviderFamilies(os.Environ())
	command.Stdin = nil
	command.Stdout = out
	command.Stderr = out
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("chorus up did not finish within %s", chorusUpTimeout)
	}
	return err
}
