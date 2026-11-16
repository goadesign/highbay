// These tests exercise the required project compilation input at construction.
package controlplane

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestNewRequiresBuildCacheForExecution refuses missing inputs without deriving
// an operation cache, and validates the supplied cache against the workspace.
func TestNewRequiresBuildCacheForExecution(t *testing.T) {
	for _, source := range []string{"stored", "written"} {
		t.Run(source, func(t *testing.T) {
			opts := Options{CardsDir: t.TempDir(), WorkspaceDir: t.TempDir(), RunsDir: t.TempDir()}
			if source == "stored" {
				opts.Verification = &VerificationConfig{RunsDir: opts.RunsDir}
			} else {
				opts.WrittenTestRuns = &WrittenTestRuns{}
			}
			_, err := New(opts)
			assert.ErrorContains(t, err, "require BuildCacheDir")
			opts.BuildCacheDir = filepath.Join(opts.WorkspaceDir, "cache")
			_, err = New(opts)
			assert.ErrorContains(t, err, "build cache directory")
			assert.ErrorContains(t, err, "must be outside workspace")
		})
	}
}
