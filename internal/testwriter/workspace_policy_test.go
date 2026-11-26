// These tests use the real snapshot/download/build owners with a trusted fake
// Go command. The placeholder suite is never executed.
package testwriter

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/highbay/internal/shquote"
	"github.com/goadesign/highbay/verify/candidate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWrittenCheckUsesStandaloneModulePolicy proves lookup, dependency
// download and the default build ignore root and module workspaces. A new
// unfetchable dependency still belongs to the author when base lists download.
func TestWrittenCheckUsesStandaloneModulePolicy(t *testing.T) {
	for _, tc := range []struct {
		badDependency, sharedCache bool
	}{{}, {badDependency: true}, {sharedCache: true}} {
		badDependency := tc.badDependency
		name := "both workspaces ignored"
		if badDependency {
			name = "new dependency is the author's finding"
		}
		if tc.sharedCache {
			name = "shared cache keeps standalone workspace policy"
		}
		t.Run(name, func(t *testing.T) {
			const rootWork = "go 1.27.3\nuse ./app\n"
			const moduleWork = "go 1.27.3\nuse ./missing\n"
			repo, _ := newProject(t, map[string]string{
				"go.work": rootWork, "eval/go.work": moduleWork,
				"eval/go.mod":  "module example.invalid/eval\ngo 1.27.3\n",
				"eval/main.go": "package main\nfunc main() {}\n",
				"lib/go.mod":   "module example.invalid/lib\ngo 1.27.3\n",
				"lib/lib.go":   "package lib\n",
			})
			base := runGit(t, repo, "rev-parse", "HEAD")
			wc, err := MakeCopy(t.Context(), repo, TestsRole, TestsRole.Branch(1), base)
			require.NoError(t, err)
			if !badDependency {
				writeFiles(t, wc.Project(), map[string]string{"eval/go.mod": "module example.invalid/eval\ngo 1.27.3\nrequire example.invalid/lib v0.0.0\nreplace example.invalid/lib => ../lib\n"})
			}
			if badDependency {
				writeFiles(t, wc.Project(), map[string]string{"eval/go.mod": "module example.invalid/eval\ngo 1.27.3\nrequire example.invalid/unfetchable v1.0.0\n"})
			}
			tmp := t.TempDir()
			src := filepath.Join(tmp, "src")
			require.NoError(t, stageCopy(t.Context(), wc, filepath.Join(tmp, "index"), src))
			fake := t.TempDir()
			goroot, cache, calls := filepath.Join(fake, "goroot"), filepath.Join(fake, "cache"), filepath.Join(fake, "calls")
			require.NoError(t, os.MkdirAll(filepath.Join(goroot, "bin"), 0o700))
			require.NoError(t, os.Mkdir(cache, 0o700))
			script := "#!/bin/sh\nset -eu\n" +
				"printf '%s|%s\\n' \"$1\" \"$GOWORK\" >> " + shquote.Quote(calls) + "\n" +
				"[ \"$GOWORK\" = off ]\ncase \"$1\" in\n" +
				"env) printf '%s\\n%s\\n' " + shquote.Quote(goroot) + " " + shquote.Quote(cache) + ";;\n" +
				"mod) if /usr/bin/grep -q example.invalid/unfetchable go.mod; then exit 2; fi ;;\n" +
				"build) [ -f ../lib/lib.go ]; out=''; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = '-o' ]; then shift; out=$1; fi; shift; done; " +
				"[ -n \"$out\" ]; printf 'placeholder, never executed' > \"$out\"; printf 'compiled' > \"$GOCACHE/cache-probe\" ;;\n*) exit 3 ;;\nesac\n"
			require.NoError(t, os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte(script), 0o700))
			t.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GOWORK", filepath.Join(repo, "go.work"))
			checker := NewChecker(nil, t.TempDir())
			if tc.sharedCache {
				checker, err = NewCheckerWithCache(nil, checker.workDir, filepath.Join(t.TempDir(), "packages"))
				require.NoError(t, err)
			}
			listed := false
			checker.list = func(context.Context, candidate.Artifact, candidate.Spec, int, func(*exec.Cmd) error) ([]byte, error) {
				listed = true
				return []byte(`{"id":"shop_acceptance","environment":"sim","scenarios":[{"id":"prices_cookie","tags":[]}]}`), nil
			}
			t.Cleanup(func() { assert.NoError(t, candidate.RemoveStore(filepath.Join(tmp, "store"))) })
			var keep bool
			scenarios, findings, err := checker.listScenarios(t.Context(), wc, base, tmp, src, "eval", ".", &keep)
			require.NoError(t, err)
			assert.False(t, keep)
			if badDependency {
				assert.False(t, listed)
				assert.Empty(t, scenarios)
				require.Len(t, findings, 1)
				assert.Equal(t, FindingDependencies, findings[0].Kind)
				assert.Contains(t, findings[0].Message, base)
			} else {
				assert.True(t, listed)
				assert.Empty(t, findings)
				require.Len(t, scenarios, 1)
				assert.Equal(t, "prices_cookie", scenarios[0].ID)
				cache := checker.buildCacheDir
				if cache == "" {
					cache = filepath.Join(checker.workDir, "build-home", "gocache")
				}
				assert.FileExists(t, filepath.Join(cache, "cache-probe"), "the compiler wrote in its selected cache")
				if tc.sharedCache {
					require.NoError(t, os.RemoveAll(checker.workDir))
					assert.FileExists(t, filepath.Join(cache, "cache-probe"), "removing checker scratch preserves the shared cache")
				}
			}
			data, err := os.ReadFile(calls)
			require.NoError(t, err)
			want := []string{"env|off", "mod|off", "build|off"}
			if badDependency {
				want = []string{"env|off", "mod|off", "mod|off"}
			}
			assert.Equal(t, want, strings.Split(strings.TrimSpace(string(data)), "\n"))
			for file, expected := range map[string]string{"go.work": rootWork, "eval/go.work": moduleWork} {
				for _, checkout := range []string{repo, wc.Dir(), src} {
					got, err := os.ReadFile(filepath.Join(checkout, file))
					require.NoError(t, err)
					assert.Equal(t, expected, string(got))
				}
			}
		})
	}
}
