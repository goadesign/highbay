// These tests drive written-suite workspace policy through real Prepare owners
// and a trusted fake Go command; placeholder artifacts are never executed.
package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/surla/internal/shquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	// The application needs the root workspace; the evaluator is deliberately
	// absent, and its own workspace points at a missing module.
	writtenRootWorkspace   = "go 1.27.3\n\nuse (\n\t./app\n\t./lib\n)\n"
	writtenModuleWorkspace = "go 1.27.3\n\nuse ./missing\n"
)

// TestPrepareWrittenSuiteBuildsAsAModule keeps the application's root workspace
// while ignoring both workspace files for written-suite download, lookup and
// compile. The full checkout and local replacement remain present.
func TestPrepareWrittenSuiteBuildsAsAModule(t *testing.T) {
	repo, commit := writtenWorkspaceVerificationFixture(t)
	fake := t.TempDir()
	goroot, cache, calls := filepath.Join(fake, "goroot"), filepath.Join(fake, "modcache"), filepath.Join(fake, "calls")
	require.NoError(t, os.MkdirAll(filepath.Join(goroot, "bin"), 0o700))
	require.NoError(t, os.Mkdir(cache, 0o700))
	script := "#!/bin/sh\nset -eu\n" +
		"printf '%s|%s|%s\\n' \"$GOWORK\" \"$PWD\" \"$1\" >> " + shquote.Quote(calls) + "\n" +
		"case \"$PWD\" in */eval) [ \"$GOWORK\" = off ]; [ -f ../lib/lib.go ] ;; esac\n" +
		"case \"$1\" in\nmod) exit 0 ;;\n" +
		"env) printf '%s\\n%s\\n' " + shquote.Quote(goroot) + " " + shquote.Quote(cache) + ";;\n" +
		"build) out=''; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = '-o' ]; then shift; out=$1; fi; shift; done; " +
		"[ -n \"$out\" ]; printf 'placeholder, never executed' > \"$out\" ;;\n*) exit 2 ;;\nesac\n"
	require.NoError(t, os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte(script), 0o700))
	t.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOWORK", filepath.Join(repo, "go.work"))
	work := filepath.Join(t.TempDir(), "run")
	plan := goWorkVerificationPlan(repo, commit, work)
	plan.Tests = TestsWritten
	prepared, err := Prepare(t.Context(), plan)
	require.NoError(t, err)
	assert.Len(t, prepared.Binding.Artifacts, 1)
	physical, err := filepath.EvalSymlinks(work)
	require.NoError(t, err)
	data, err := os.ReadFile(calls)
	require.NoError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 6)
	for i, line := range lines {
		checkout, module, selected := "candidate", "app", filepath.Join(physical, "candidate", "go.work")
		if i >= 3 {
			checkout, module, selected = "suite", "eval", "off"
		}
		assert.Equal(t, selected+"|"+filepath.Join(physical, checkout, module)+"|"+[]string{"mod", "env", "build"}[i%3], line)
	}
	for _, checkout := range []string{repo, filepath.Join(work, "candidate"), filepath.Join(work, "suite")} {
		rootWork, err := os.ReadFile(filepath.Join(checkout, "go.work"))
		require.NoError(t, err)
		assert.Equal(t, writtenRootWorkspace, string(rootWork))
		moduleWork, err := os.ReadFile(filepath.Join(checkout, "eval", "go.work"))
		require.NoError(t, err)
		assert.Equal(t, writtenModuleWorkspace, string(moduleWork))
	}
}

// writtenWorkspaceVerificationFixture is shared by shape and hosted native
// proof, so off policy cannot conceal a lost sibling replacement or checkout.
func writtenWorkspaceVerificationFixture(t *testing.T) (string, string) {
	t.Helper()
	repo, _ := goWorkVerificationFixture(t, true)
	files := map[string]string{
		"go.work":      writtenRootWorkspace,
		"eval/go.work": writtenModuleWorkspace,
		"eval/go.mod":  "module example.invalid/eval\n\ngo 1.27.3\n\nrequire example.invalid/lib v0.0.0\n\nreplace example.invalid/lib => ../lib\n",
		"eval/main.go": "package main\nimport \"example.invalid/lib\"\nfunc main() { _ = lib.Value }\n",
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(repo, name), []byte(content), 0o600))
	}
	git(t, repo, "add", "--", "go.work", "eval")
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "Give written tests independent module dependencies")
	return repo, strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
}
