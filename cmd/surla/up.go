// This file implements "surla up": the one command a person runs after
// installing surla. It runs `surla setup -global` and starts Chorus when it
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

// runUp implements `surla up` for the CLI: global setup unless SURLA_NO_SETUP
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
	if os.Getenv("SURLA_NO_SETUP") != "1" {
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
// reported, not an error: Surla works without it, but its agents cannot
// message each other. A failed setup or Chorus start is an error naming how
// to continue.
func upWith(
	env *setupEnvironment, chorus string, start func(context.Context, string, io.Writer) error, out io.Writer,
) error {
	if env != nil {
		if err := setupGlobal(*env, out); err != nil {
			return fmt.Errorf("%w (fix that file and run surla up again, or set SURLA_NO_SETUP=1 to skip setup)", err)
		}
	}
	if chorus == "" {
		fmt.Fprintln(out, "Chorus is not installed, so Surla's agents will work without messaging each other.")
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), chorusUpTimeout)
		defer cancel()
		if err := start(ctx, chorus, out); err != nil {
			return fmt.Errorf("start Chorus: %w", err)
		}
	}
	_, err := fmt.Fprintln(out, "Surla is ready: open your coding agent in a project folder and type /surla.")
	return err
}

// startChorus runs `chorus up` with no input and its output on out. Chorus
// starts its server in the background and returns once it answers.
func startChorus(ctx context.Context, chorus string, out io.Writer) error {
	command := exec.CommandContext(ctx, chorus, "up")
	command.Stdin = nil
	command.Stdout = out
	command.Stderr = out
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("chorus up did not finish within %s", chorusUpTimeout)
	}
	return err
}
