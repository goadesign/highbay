package candidate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerFixtureCleanup verifies private read-only Go cache directories
// are removed by testing cleanup without starting a container.
func TestContainerFixtureCleanup(t *testing.T) {
	var root string
	t.Run("read_only_module_cache", func(t *testing.T) {
		root = t.TempDir()
		cache := filepath.Join(root, "cache")
		module := filepath.Join(cache, "golang.org", "toolchain@v0.0.1")
		nested := filepath.Join(module, "bin")
		require.NoError(t, os.MkdirAll(nested, 0o700))
		registerContainerCacheCleanup(t, cache)
		require.NoError(t, os.WriteFile(filepath.Join(module, "PATENTS"), []byte("fixture"), 0o444))
		require.NoError(t, os.WriteFile(filepath.Join(nested, "go"), []byte("fixture"), 0o555))
		for _, dir := range []string{nested, module} {
			require.NoError(t, os.Chmod(dir, 0o555))
		}
	})
	_, err := os.Stat(root)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// registerContainerCacheCleanup restores write permission on private Go cache
// directories. Call after creating the cache under t.TempDir so LIFO cleanup
// runs this before testing removes the directory. Containers use the host UID.
func registerContainerCacheCleanup(t *testing.T, cache string) {
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
