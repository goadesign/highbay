// These tests exercise retained compiler storage and evaluator cleanup.
package designeval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/testmodule"
)

// TestOwnerCacheSurvivesEvaluatorCleanup exercises actual compilation through
// two evaluator lifetimes while keeping each owner's scratch independent.
func TestOwnerCacheSurvivesEvaluatorCleanup(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles real design programs")
	}
	parent, cache := t.TempDir(), filepath.Join(t.TempDir(), "packages")
	files := testmodule.Snapshot(t, completeSourcesGoa, `package design
import . "goa.design/goa/v3/dsl"
var _ = Service("orders", func() { Method("get", func() { Result(String) }) })
`)
	for range 2 {
		ev, err := NewEvaluatorWithCache(parent, cache)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, ev.Close()) })
		result, err := ev.Evaluate(t.Context(), files)
		require.NoError(t, err)
		assert.Equal(t, "orders", result.SystemName)
		assert.NoDirExists(t, filepath.Join(ev.workDir, "gocache"))
		cached, err := os.ReadDir(cache)
		require.NoError(t, err)
		assert.NotEmpty(t, cached, "the selected cache received real compiled packages")
		require.NoError(t, ev.Close())
		assert.NoDirExists(t, ev.workDir)
		assert.DirExists(t, cache, "closing the evaluator preserves the owner's cache")
	}
	assert.Empty(t, entries(t, parent))
}

// TestOwnerCacheIsRequiredBeforeEvaluatorAllocation rejects incomplete
// explicit-cache construction without creating compiler scratch.
func TestOwnerCacheIsRequiredBeforeEvaluatorAllocation(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "scratch")
	for _, cache := range []string{"", "relative/cache"} {
		ev, err := NewEvaluatorWithCache(parent, cache)
		assert.ErrorContains(t, err, "must be absolute")
		assert.Nil(t, ev)
	}
	assert.NoDirExists(t, parent)
}
