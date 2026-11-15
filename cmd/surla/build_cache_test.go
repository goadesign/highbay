// This file checks the execution-only argument contract, without starting a child.
package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVerifyRunRequiresBuildCacheArgument covers each role before child setup.
func TestVerifyRunRequiresBuildCacheArgument(t *testing.T) {
	for _, role := range []string{"supervise", "execute", "live"} {
		t.Run(role, func(t *testing.T) {
			assert.ErrorContains(t, runVerifyRun([]string{role, t.TempDir()}), "build cache directory")
			assert.ErrorContains(t, runVerifyRun([]string{role, t.TempDir(), ""}), "build cache directory")
		})
	}
}
