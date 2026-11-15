// This file checks verification workspace command shapes without compiling or
// executing any checked-out application or evaluator code.
package verify

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/surla/internal/shquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrepareGoWorkUsesCheckoutRoot drives real checkout and Prepare owners
// with a trusted fake Go executable. It proves the download, toolchain lookup
// and offline build receive the same internal workspace and nested module.
// Placeholder artifacts are never executed or represented as native proof.
func TestPrepareGoWorkUsesCheckoutRoot(t *testing.T) {
	for _, internal := range []bool{false, true} {
		t.Run(fmt.Sprintf("internal=%t", internal), func(t *testing.T) {
			repo, commit := goWorkVerificationFixture(t, internal)
			outer := t.TempDir()
			const outside = "go 1.27.3\n\nuse ./untracked-app\n"
			require.NoError(t, os.WriteFile(filepath.Join(outer, "go.work"), []byte(outside), 0o600))
			fake := t.TempDir()
			goroot, cache, calls := filepath.Join(fake, "goroot"), filepath.Join(fake, "modcache"), filepath.Join(fake, "calls")
			require.NoError(t, os.MkdirAll(filepath.Join(goroot, "bin"), 0o700))
			require.NoError(t, os.Mkdir(cache, 0o700))
			script := "#!/bin/sh\nset -eu\n" +
				"printf '%s|%s|%s\\n' \"$GOWORK\" \"$PWD\" \"$1\" >> " + shquote.Quote(calls) + "\n" +
				"case \"$1\" in\n" +
				"mod) exit 0 ;;\n" +
				"env) printf '%s\\n%s\\n' " + shquote.Quote(goroot) + " " + shquote.Quote(cache) + ";;\n" +
				"build) out=''; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = '-o' ]; then shift; out=$1; fi; shift; done; " +
				"[ -n \"$out\" ]; printf 'placeholder, never executed' > \"$out\" ;;\n" +
				"*) exit 2 ;;\nesac\n"
			require.NoError(t, os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte(script), 0o700))
			t.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GOWORK", "off")
			work := filepath.Join(outer, "run")
			prepared, err := Prepare(t.Context(), goWorkVerificationPlan(repo, commit, work))
			require.NoError(t, err)
			assert.Len(t, prepared.Binding.Artifacts, 1)
			physical, err := filepath.EvalSymlinks(work)
			require.NoError(t, err)
			data, err := os.ReadFile(calls)
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			require.Len(t, lines, 6)
			for i, line := range lines {
				checkout, module := "candidate", "app"
				if i >= 3 {
					checkout, module = "suite", "eval"
				}
				selected := "off"
				if internal {
					selected = filepath.Join(physical, checkout, "go.work")
				}
				verb := []string{"mod", "env", "build"}[i%3]
				assert.Equal(t, selected+"|"+filepath.Join(physical, checkout, module)+"|"+verb, line)
			}
			outsideAfter, err := os.ReadFile(filepath.Join(outer, "go.work"))
			require.NoError(t, err)
			assert.Equal(t, outside, string(outsideAfter))
		})
	}
}

// goWorkVerificationFixture commits an application that imports a sibling
// module whose version cannot be resolved without the checked-out workspace.
// Both command-shape tests and hosted native preparation use this same input.
func goWorkVerificationFixture(t *testing.T, internal bool) (string, string) {
	t.Helper()
	repo := t.TempDir()
	files := map[string]string{
		"app/go.mod":   "module example.invalid/app\n\ngo 1.27.3\n\nrequire example.invalid/lib v0.0.0\n",
		"app/main.go":  "package main\nimport \"example.invalid/lib\"\nfunc main() { _ = lib.Value }\n",
		"lib/go.mod":   "module example.invalid/lib\n\ngo 1.27.3\n",
		"lib/lib.go":   "package lib\nconst Value = 42\n",
		"eval/go.mod":  "module example.invalid/eval\n\ngo 1.27.3\n",
		"eval/main.go": "package main\nfunc main() {}\n",
	}
	if internal {
		files["go.work"] = "go 1.27.3\n\nuse (\n\t./app\n\t./lib\n\t./eval\n)\n"
	}
	paths := []string{"add", "--"}
	for name, content := range files {
		file := filepath.Join(repo, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(file), 0o700))
		require.NoError(t, os.WriteFile(file, []byte(content), 0o600))
		paths = append(paths, name)
	}
	git(t, repo, "init", "--quiet")
	git(t, repo, "config", "commit.gpgsign", "false")
	git(t, repo, "config", "core.hooksPath", "/dev/null")
	git(t, repo, paths...)
	git(t, repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "-m", "Commit workspace fixture")
	return repo, strings.TrimSpace(git(t, repo, "rev-parse", "HEAD"))
}

// goWorkVerificationPlan preserves the candidate's nested module and uses the
// same repository's independent evaluator module without executing its binary.
func goWorkVerificationPlan(repo, commit, work string) Plan {
	return Plan{
		CandidateRepo: repo, CandidateCommit: commit, CandidateModule: "app", Binaries: map[string]string{"app": "."},
		SuiteRepo: repo, SuiteCommit: commit, SuiteModule: "eval", SuiteCommand: ".",
		RequestRevision: "workspace-proof", WorkDir: work, Tests: TestsEvaluator,
	}
}
