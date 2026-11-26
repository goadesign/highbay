// This file proves agreed-design synchronization preserves the builder's other files.
package controlplane

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/goadesign/highbay/internal/designeval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// TestBuildDesignSynchronizesOnlyOwnedFiles covers addition, replacement,
// deletion and replay without modifying tests, nested modules or manifests.
func TestBuildDesignSynchronizesOnlyOwnedFiles(t *testing.T) {
	dir := t.TempDir()
	preserved := map[string]string{
		"design/design_test.go":      "package design // builder's test\n",
		"design/testdata/fixture.go": "not Go: fixture bytes\n",
		"design/vendor/lib/lib.go":   "package lib\n",
		"design/.hidden/keep.go":     "hidden source\n",
		"design/nested/go.mod":       "module example.com/nested\n",
		"design/nested/keep.go":      "package nested\n",
		"design/README.md":           "builder notes\n",
		"go.mod":                     "module example.com/app\n",
		"go.sum":                     "held checksums\n",
		"internal/app.go":            "package internal\n",
		"gen/previous.go":            "package gen\n",
	}
	for name, text := range preserved {
		writeBuildDesignFixture(t, dir, name, text)
	}
	writeBuildDesignFixture(t, dir, "design/obsolete.go", "package design // removed\n")
	writeBuildDesignFixture(t, dir, "design/current.go", "package design // prior\n")
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	design := map[string]string{
		"design/current.go":   "package design // agreed\n",
		"design/sub/added.go": "package sub // added\n",
	}
	for range 2 {
		require.NoError(t, writeBuildDesign(t.Context(), root, design))
		got, err := designeval.ReadDesignFilesFromRoot(dir, root)
		require.NoError(t, err)
		assert.Equal(t, design, got)
		for name, text := range preserved {
			got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
			require.NoError(t, err)
			assert.Equal(t, text, string(got), name)
		}
		_, err = root.Lstat("design/obsolete.go")
		assert.ErrorIs(t, err, fs.ErrNotExist)
	}
}

// TestBuildDesignDoesNotWriteThroughHardLinks keeps an alias outside the
// design unchanged when its owned name receives the agreed source.
func TestBuildDesignDoesNotWriteThroughHardLinks(t *testing.T) {
	dir := t.TempDir()
	module := filepath.Join(dir, "module")
	writeBuildDesignFixture(t, module, "design/design.go", "package design // original\n")
	alias := filepath.Join(dir, "alias.go")
	require.NoError(t, os.Link(filepath.Join(module, "design/design.go"), alias))
	root, err := os.OpenRoot(module)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	require.NoError(t, writeBuildDesign(t.Context(), root, map[string]string{
		"design/design.go": "package design // agreed\n",
	}))
	got, err := os.ReadFile(alias)
	require.NoError(t, err)
	assert.Equal(t, "package design // original\n", string(got))
	got, err = root.ReadFile("design/design.go")
	require.NoError(t, err)
	assert.Equal(t, "package design // agreed\n", string(got))
}

// TestBuildDesignRefusesPathsOutsideItsOwnership rejects corrupt saved paths
// before any existing design file can be replaced or removed.
func TestBuildDesignRefusesPathsOutsideItsOwnership(t *testing.T) {
	for _, name := range []string{
		"../outside.go", "/design/file.go", "./design/file.go", "go.mod",
		"design/design_test.go", "design/testdata/file.go", "design/.hidden/file.go",
		"design/vendor/file.go", "design/sub/../file.go",
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			writeBuildDesignFixture(t, dir, "design/current.go", "package design // kept\n")
			root, err := os.OpenRoot(dir)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, root.Close()) })
			assert.ErrorContains(t, writeBuildDesign(t.Context(), root, map[string]string{name: "changed"}), "outside the design walk")
			got, err := root.ReadFile("design/current.go")
			require.NoError(t, err)
			assert.Equal(t, "package design // kept\n", string(got))
		})
	}
}

// TestBuildDesignRefusesLinksAndNestedModules preserves linked targets and
// excluded module contents, including a special file that must never block.
func TestBuildDesignRefusesLinksAndNestedModules(t *testing.T) {
	for _, kind := range []string{"design link", "directory link", "file link", "nested module", "FIFO"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			module := filepath.Join(dir, "module")
			outside := filepath.Join(dir, "outside")
			writeBuildDesignFixture(t, outside, "file.go", "package outside // untouched\n")
			require.NoError(t, os.Mkdir(module, 0o700))
			name := "design/file.go"
			switch kind {
			case "design link":
				require.NoError(t, os.Symlink(outside, filepath.Join(module, "design")))
			case "directory link":
				require.NoError(t, os.Mkdir(filepath.Join(module, "design"), 0o700))
				require.NoError(t, os.Symlink(outside, filepath.Join(module, "design/sub")))
				name = "design/sub/file.go"
			case "file link":
				require.NoError(t, os.Mkdir(filepath.Join(module, "design"), 0o700))
				require.NoError(t, os.Symlink(filepath.Join(outside, "file.go"), filepath.Join(module, "design/file.go")))
			case "nested module":
				name = "design/sub/file.go"
				writeBuildDesignFixture(t, module, "design/sub/go.mod", "module example.com/sub\n")
				writeBuildDesignFixture(t, module, name, "package sub // untouched\n")
			case "FIFO":
				require.NoError(t, os.Mkdir(filepath.Join(module, "design"), 0o700))
				require.NoError(t, unix.Mkfifo(filepath.Join(module, "design/file.go"), 0o600))
			}
			root, err := os.OpenRoot(module)
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, root.Close()) })
			assert.Error(t, writeBuildDesign(t.Context(), root, map[string]string{name: "package design // agreed\n"}))
			got, err := os.ReadFile(filepath.Join(outside, "file.go"))
			require.NoError(t, err)
			assert.Equal(t, "package outside // untouched\n", string(got))
			if kind == "nested module" {
				got, err := root.ReadFile(name)
				require.NoError(t, err)
				assert.Equal(t, "package sub // untouched\n", string(got))
			}
		})
	}
}

// TestBuildDesignCanceledLeavesFiles proves cancellation precedes the design walk.
func TestBuildDesignCanceledLeavesFiles(t *testing.T) {
	dir := t.TempDir()
	writeBuildDesignFixture(t, dir, "design/current.go", "package design // kept\n")
	root, err := os.OpenRoot(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, root.Close()) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	assert.ErrorIs(t, writeBuildDesign(ctx, root, map[string]string{}), context.Canceled)
	got, err := root.ReadFile("design/current.go")
	require.NoError(t, err)
	assert.Equal(t, "package design // kept\n", string(got))
}

// writeBuildDesignFixture writes a regular test-owned file and its parent folders.
func writeBuildDesignFixture(t *testing.T, dir, name, text string) {
	t.Helper()
	file := filepath.Join(dir, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
	require.NoError(t, os.WriteFile(file, []byte(text), 0o600))
}
