// This file checks the Git capability project readers require before startup effects.
package main

import (
	"context"

	"github.com/goadesign/highbay/internal/depinstall"
)

// preflightProjectGit checks the Git on PATH before creating project state.
func preflightProjectGit() error {
	return depinstall.CheckProjectGit(context.Background())
}
