// This file cleans up only test-owned retained dependency inputs after the
// server and its containers have stopped. Production Close never changes them.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckModuleCacheFixtureCleanup covers Go's nested read-only module and
// toolchain directories without starting Docker or compiling a fixture.
func TestCheckModuleCacheFixtureCleanup(t *testing.T) {
	var root string
	t.Run("read-only cached inputs", func(t *testing.T) {
		root = t.TempDir()
		cache := filepath.Join(root, "check-modcache")
		module := filepath.Join(cache, "example.com", "mod@v1.0.0")
		nested := filepath.Join(module, "bin")
		require.NoError(t, os.MkdirAll(nested, 0o700))
		registerCheckModuleCacheCleanup(t, cache)
		require.NoError(t, os.WriteFile(filepath.Join(nested, "go"), []byte("fixture"), 0o555))
		for _, dir := range []string{nested, module} {
			require.NoError(t, os.Chmod(dir, 0o555))
		}
	})
	_, err := os.Stat(root)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// registerCheckModuleCacheCleanup restores directory write access solely for a
// fixture cache under t.TempDir. Register after allocation and before server
// cleanup, so LIFO cleanup stops every container before touching its mounts.
func registerCheckModuleCacheCleanup(t *testing.T, cache string) {
	t.Helper()
	t.Cleanup(func() {
		require.NoError(t, filepath.WalkDir(cache, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return nil
		}))
	})
}
