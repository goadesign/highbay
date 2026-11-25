// This file checks the Git capability project readers require before startup effects.
package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// preflightProjectGit checks the Git on PATH without opening a repository. It
// accepts backports of --no-lazy-fetch and keeps execution failures distinct
// from an unsupported option; project readers retain their runtime checks.
func preflightProjectGit() error {
	program, err := exec.LookPath("git")
	if err != nil {
		return errors.New("Git is required to start Highbay. Install Git 2.45 or newer and run hbay again.")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := exec.CommandContext(ctx, program, "--no-lazy-fetch", "version").Output(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || !strings.Contains(string(exit.Stderr), "unknown option: --no-lazy-fetch") {
			return fmt.Errorf("checking Git support for --no-lazy-fetch: %w", err)
		}
		version, err := exec.CommandContext(ctx, program, "--version").Output()
		if err != nil {
			return fmt.Errorf("Git does not support --no-lazy-fetch and its version could not be read. Install Git 2.45 or newer and run hbay again: %w", err)
		}
		return fmt.Errorf("Git on PATH reports %q and does not support --no-lazy-fetch. Install Git 2.45 or newer and run hbay again.", strings.TrimSpace(string(version)))
	}
	return nil
}
