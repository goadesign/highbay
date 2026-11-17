// These tests exercise the required project compilation input at construction.
package controlplane

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/testmodule"
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

// TestProjectCompilerCacheOutlivesEvaluators exercises the default server's
// evaluator and its fresh init generator against one retained compiler cache.
func TestProjectCompilerCacheOutlivesEvaluators(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles real design programs")
	}
	cache := filepath.Join(t.TempDir(), "packages")
	cp := newControlPlane(t, Options{
		CardsDir: t.TempDir(), WorkspaceDir: t.TempDir(), BuildCacheDir: cache,
	})
	files := testmodule.Snapshot(t, "", architectureTestDesign)
	result, err := cp.evaluator.Evaluate(t.Context(), files)
	require.NoError(t, err)
	assert.NotEmpty(t, result.SystemName)
	before, err := os.ReadDir(cache)
	require.NoError(t, err)
	assert.NotEmpty(t, before, "the server selected the project cache for real compilation")
	parent := filepath.Join(cp.cards.dir, "designeval")
	gen, err := cp.makeInitGenerator(parent)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, gen.Close()) })
	generated, err := gen.Generate(t.Context(), files)
	require.NoError(t, err)
	assert.NotEmpty(t, generated)
	require.NoError(t, gen.Close())
	assert.DirExists(t, cache)
	require.NoError(t, cp.Close())
	assert.DirExists(t, cache, "server cleanup never deletes retained compiler inputs")
	scratch, err := os.ReadDir(parent)
	require.NoError(t, err)
	assert.Empty(t, scratch, "both evaluator owners released only their scratch")
}
