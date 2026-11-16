// These tests distinguish standalone written-test builds from checkout builds.
package candidate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildModuleWorkspacePolicy inspects actual prepared commands and shared
// publication without compiling or executing the placeholder artifacts. The
// same checkout holds both workspace files and a sibling replacement.
func TestBuildModuleWorkspacePolicy(t *testing.T) {
	root := writeModule(t, map[string]string{
		"go.work":      "go 1.27.3\nuse (\n ./app\n ./lib\n)\n",
		"app/go.mod":   "module example.invalid/app\ngo 1.27.3\n",
		"app/main.go":  "package main\nfunc main() {}\n",
		"eval/go.work": "go 1.27.3\nuse ./missing\n",
		"eval/go.mod":  "module example.invalid/eval\ngo 1.27.3\nrequire example.invalid/lib v0.0.0\nreplace example.invalid/lib => ../lib\n",
		"eval/main.go": "package main\nimport _ \"example.invalid/lib\"\nfunc main() {}\n",
		"lib/go.mod":   "module example.invalid/lib\ngo 1.27.3\n",
		"lib/lib.go":   "package lib\n",
	})
	physical, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	for _, entry := range []string{"module", "application", "application tests"} {
		t.Run(entry, func(t *testing.T) {
			module, want := "eval", "off"
			if entry != "module" {
				module, want = "app", filepath.Join(physical, "go.work")
			}
			out := filepath.Join(t.TempDir(), "artifact")
			spec := BuildSpec{BuildCacheDir: t.TempDir(), SourceDir: root, ModuleDir: filepath.Join(root, module), Package: ".",
				Out: out, GOROOT: t.TempDir(), ModCache: t.TempDir(), WorkDir: t.TempDir(), LogFile: filepath.Join(t.TempDir(), "build.log")}
			calls := 0
			run := func(cmd *exec.Cmd) error {
				calls++
				assert.Equal(t, filepath.Join(physical, module), cmd.Dir)
				var workspace []string
				for _, value := range cmd.Env {
					if strings.HasPrefix(value, "GOWORK=") {
						workspace = append(workspace, value)
					}
				}
				assert.Equal(t, []string{"GOWORK=" + want}, workspace)
				assert.Contains(t, cmd.Env, "GOPROXY=off")
				assert.Contains(t, cmd.Env, "GOFLAGS=-mod=readonly")
				// Full checkout contents stay available even with workspaces off.
				assert.FileExists(t, filepath.Join(cmd.Dir, "..", "lib", "lib.go"))
				for i, arg := range cmd.Args {
					if arg == "-o" {
						assert.NotEqual(t, out, cmd.Args[i+1])
						return os.WriteFile(cmd.Args[i+1], []byte("placeholder, never executed"), 0o700)
					}
				}
				return errors.New("missing staged output")
			}
			if entry == "module" {
				require.NoError(t, BuildModule(t.Context(), spec, run))
			} else if entry == "application" {
				require.NoError(t, Build(t.Context(), spec, run))
			} else {
				written, err := BuildTest(t.Context(), spec, run)
				require.NoError(t, err)
				assert.True(t, written)
			}
			assert.Equal(t, 1, calls)
			assert.FileExists(t, out)
		})
	}
	for _, file := range []string{"go.work", "eval/go.work"} {
		assert.FileExists(t, filepath.Join(root, file))
	}
}

// TestBuildModulePreservesValidationAndFailure keeps path validation before
// launch and shares Build's staging, original error cause and existing output.
func TestBuildModulePreservesValidationAndFailure(t *testing.T) {
	root := writeModule(t, map[string]string{"eval/go.mod": "module example.invalid/eval\n"})
	out := filepath.Join(t.TempDir(), "suite")
	require.NoError(t, os.WriteFile(out, []byte("previous"), 0o600))
	spec := BuildSpec{BuildCacheDir: t.TempDir(), SourceDir: root, ModuleDir: filepath.Join(root, "eval"), Package: ".", Out: out,
		GOROOT: t.TempDir(), ModCache: t.TempDir(), WorkDir: t.TempDir(), LogFile: filepath.Join(t.TempDir(), "build.log")}
	cause := errors.New("compiler unavailable")
	calls := 0
	run := func(*exec.Cmd) error { calls++; return cause }
	assert.ErrorIs(t, BuildModule(t.Context(), spec, run), cause)
	assert.Equal(t, 1, calls)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	assert.Equal(t, "previous", string(data))
	left, err := filepath.Glob(filepath.Join(spec.WorkDir, ".surla-stage-*"))
	require.NoError(t, err)
	assert.Empty(t, left)
	spec.SourceDir = t.TempDir()
	assert.ErrorContains(t, BuildModule(t.Context(), spec, run), "outside source directory")
	assert.Equal(t, 1, calls, "invalid paths never reach the runner")
}

// TestBuildModuleNativeSiblingReplace is hosted native proof that both
// workspace files are ignored and a local replacement outside eval still builds.
func TestBuildModuleNativeSiblingReplace(t *testing.T) {
	if testing.Short() {
		t.Skip("standalone native build runs in the hosted full profile")
	}
	root := writeModule(t, map[string]string{
		"go.work":      "go 1.27.3\nuse ./unavailable\n",
		"eval/go.work": "go 1.27.3\nuse ./unavailable\n",
		"eval/go.mod":  "module example.invalid/eval\ngo 1.27.3\nrequire example.invalid/lib v0.0.0\nreplace example.invalid/lib => ../lib\n",
		"eval/main.go": "package main\nimport \"example.invalid/lib\"\nfunc main() { _ = lib.Value }\n",
		"lib/go.mod":   "module example.invalid/lib\ngo 1.27.3\n",
		"lib/lib.go":   "package lib\nconst Value = 42\n",
	})
	log := filepath.Join(t.TempDir(), "build.log")
	err := BuildModule(t.Context(), testBuildSpec(t, root, filepath.Join(root, "eval"), filepath.Join(t.TempDir(), "suite"), log), (*exec.Cmd).Run)
	require.NoError(t, err, logText(log))
}
