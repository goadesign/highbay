// These tests own admission of retained Docker dependency inputs. Container
// mount/lifetime and Go checksum behavior belong to their existing test owners.
package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheckModuleCacheAdmission checks configuration before operation storage
// allocation and the helper's private stdin contract before Docker admission.
func TestCheckModuleCacheAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(*testing.T, string, string) string
		want string
	}{
		{name: "private", path: func(t *testing.T, _, _ string) string { return newCheckModuleCache(t) }},
		{name: "missing", path: func(t *testing.T, _, _ string) string { return filepath.Join(realTempDir(t), "missing") }, want: "no such file"},
		{name: "permissions", path: func(t *testing.T, _, _ string) string {
			dir := realTempDir(t)
			require.NoError(t, os.Chmod(dir, 0o755))
			return dir
		}, want: "mode 0700"},
		{name: "symlink", path: func(t *testing.T, _, _ string) string {
			link := filepath.Join(realTempDir(t), "link")
			require.NoError(t, os.Symlink(realTempDir(t), link))
			return link
		}, want: "symlink"},
		{name: "file", path: func(t *testing.T, _, _ string) string {
			file := filepath.Join(realTempDir(t), "file")
			require.NoError(t, os.WriteFile(file, nil, 0o600))
			return file
		}, want: "open check module cache"},
		{name: "workspace", path: func(t *testing.T, workspace, _ string) string {
			dir := filepath.Join(workspace, "cache")
			require.NoError(t, os.Mkdir(dir, 0o700))
			return dir
		}, want: "separate from source"},
		{name: "operation storage", path: func(t *testing.T, _, cards string) string {
			dir := filepath.Join(cards, "check-work", "cache")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			return dir
		}, want: "separate from source"},
		{name: "source ancestor", path: func(_ *testing.T, workspace, _ string) string {
			return filepath.Dir(workspace)
		}, want: "separate from source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newCheckModuleCache(t)
			workspace := filepath.Join(root, "workspace")
			require.NoError(t, os.Mkdir(workspace, 0o700))
			cards := filepath.Join(realTempDir(t), "cards")
			cache := tc.path(t, workspace, cards)
			cp, err := New(Options{CardsDir: cards, WorkspaceDir: workspace, CheckModuleCacheDir: cache})
			if tc.want != "" {
				assert.ErrorContains(t, err, tc.want)
				if tc.name != "operation storage" {
					assert.NoDirExists(t, cards, "invalid cache input is rejected before card allocation")
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, cache, cp.checkModuleCacheDir)
			require.NoError(t, cp.Close())
			assert.DirExists(t, cache)
		})
	}

	grant, operations := newCheckModuleCache(t), newCheckModuleCache(t)
	spec := checkHelperSpec{
		Tool: checkToolProject, OpID: strings.Repeat("ab", 16),
		GrantRoot: grant, WorkspaceRel: ".", CheckTimeout: time.Minute,
	}
	spec.Dir = filepath.Join(operations, spec.OpID)
	for _, tc := range []struct{ cache, want string }{
		{grant, "separate from source"},
		{operations, "separate from source"},
		{filepath.Join(operations, "missing"), "resolve check module cache"},
		{"relative", "resolve check module cache"},
	} {
		spec.ModuleCacheDir = tc.cache
		assert.ErrorContains(t, spec.validate(), tc.want, tc.cache)
	}
	spec.ModuleCacheDir = newCheckModuleCache(t)
	assert.NoError(t, spec.validate())
	spec.ModuleCacheDir = ""
	assert.NoError(t, spec.validate(), "embedded checkers may retain the operation-local default")
}

// newCheckModuleCache allocates a private fixture input rather than relying on
// testing.TempDir's platform-dependent directory permissions.
func newCheckModuleCache(t *testing.T) string {
	t.Helper()
	cache := filepath.Join(realTempDir(t), "cache")
	require.NoError(t, os.Mkdir(cache, 0o700))
	return cache
}
