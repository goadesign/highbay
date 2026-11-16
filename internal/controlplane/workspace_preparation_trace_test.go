// These tests bind real preparation failures to the control-plane error recorder.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/goadesign/surla/internal/shquote"
	"github.com/goadesign/surla/internal/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestWorkspacePreparationFailuresKeepSecretsOutOfSpans passes the actual
// returned StageError through the existing recorder, including an additional
// caller wrapper. It proves telemetry preservation, not a hosted verification.
func TestWorkspacePreparationFailuresKeepSecretsOutOfSpans(t *testing.T) {
	const proxyMarker = "FAKE_PROXY_CREDENTIAL_94"
	const flagsMarker = "FAKE_GOFLAGS_CREDENTIAL_94"
	for _, mode := range []string{"lookup", "prefill"} {
		t.Run(mode, func(t *testing.T) {
			repo, workspace := productionBuildProject(t, ".", reviewEval)
			writeBuildDesignFixture(t, workspace, "go.work", "go 1.27.3\nuse .\n")
			commit := commitAllForLayoutTest(t, repo)
			fake := t.TempDir()
			goroot, cache := filepath.Join(fake, "goroot"), filepath.Join(fake, "cache")
			require.NoError(t, os.MkdirAll(filepath.Join(goroot, "bin"), 0o700))
			require.NoError(t, os.Mkdir(cache, 0o700))
			settings, err := json.Marshal(map[string]string{"GOROOT": goroot, "GOMODCACHE": cache,
				"GOPROXY": "https://fixture:" + proxyMarker + "@proxy.example.invalid"})
			require.NoError(t, err)
			lookupExit := ""
			if mode == "lookup" {
				lookupExit = "exit 7\n"
			}
			script := "#!/bin/sh\nset -eu\ncase \"$1\" in\nenv) printf '%s\\n' " + shquote.Quote(string(settings)) + "; printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + " >&2\n" + lookupExit + ";;\n" +
				"list) printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + "; printf '%s\\n' " + shquote.Quote(proxyMarker+flagsMarker) + " >&2; exit 8 ;;\n*) exit 9 ;;\nesac\n"
			require.NoError(t, os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte(script), 0o700))
			t.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GOPROXY", "https://fixture:"+proxyMarker+"@proxy.example.invalid")
			t.Setenv("GOFLAGS", "-tags="+flagsMarker)
			_, err = verify.Prepare(t.Context(), verify.Plan{
				CandidateRepo: repo, CandidateCommit: commit, CandidateModule: ".", Binaries: map[string]string{"app": "."},
				SuiteRepo: repo, SuiteCommit: commit, SuiteModule: "eval", SuiteCommand: ".", Tests: verify.TestsWritten,
				WorkDir: filepath.Join(t.TempDir(), "run"),
			}, t.TempDir())
			var stage *verify.StageError
			require.ErrorAs(t, err, &stage)
			assert.Equal(t, verify.StageBuild, stage.Stage)
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
			_, span := provider.Tracer("test").Start(t.Context(), "surla.controlplane.prepare_verification")
			wrapped := fmt.Errorf("prepare verification: %w", err)
			assert.ErrorIs(t, recordBuildError(span, wrapped, "prepare verification"), err)
			span.End()
			assert.NotContains(t, wrapped.Error(), proxyMarker)
			assert.NotContains(t, wrapped.Error(), flagsMarker)
			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, codes.Error, spans[0].Status.Code)
			require.NotEmpty(t, spans[0].Events, "the operational failure remains recorded")
			for _, attribute := range spans[0].Attributes {
				assert.NotContains(t, attribute.Value.Emit(), proxyMarker)
				assert.NotContains(t, attribute.Value.Emit(), flagsMarker)
			}
			for _, event := range spans[0].Events {
				for _, attribute := range event.Attributes {
					assert.NotContains(t, attribute.Value.Emit(), proxyMarker)
					assert.NotContains(t, attribute.Value.Emit(), flagsMarker)
				}
			}
		})
	}
}

// TestModuleDownloadKeepsDiagnosticsOutOfSpans records actual standalone and
// written-suite StageErrors. Private evidence remains; events keep only the
// fixed operation, process status and private filename.
func TestModuleDownloadKeepsDiagnosticsOutOfSpans(t *testing.T) {
	const user = "FAKE_PROXY_USER_302"
	const path = "FAKE_PROXY_PATH_302"
	const flag = "-FAKE_INVALID_GOFLAGS_302"
	for _, target := range []string{"standalone", "written"} {
		for _, problem := range []string{"proxy", "flags"} {
			t.Run(target+"/"+problem, func(t *testing.T) {
				repo, workspace := productionBuildProject(t, ".", reviewEval)
				writeBuildDesignFixture(t, workspace, "main.go", "package main\nfunc main() {}\n")
				writeBuildDesignFixture(t, workspace, "go.mod", "module example.com/shop\ngo 1.27.3\nrequire example.invalid/missing v1.0.0\n")
				writeBuildDesignFixture(t, workspace, "eval/go.mod", "module example.com/shop/eval\ngo 1.27.3\nrequire example.invalid/missing v1.0.0\n")
				if target == "written" {
					writeBuildDesignFixture(t, workspace, "go.work", "go 1.27.3\nuse .\n")
				}
				commit := commitAllForLayoutTest(t, repo)
				fake := t.TempDir()
				goroot, cache := filepath.Join(fake, "goroot"), filepath.Join(fake, "cache")
				require.NoError(t, os.MkdirAll(filepath.Join(goroot, "bin"), 0o700))
				require.NoError(t, os.Mkdir(cache, 0o700))
				proxy := "http://" + user + ":fake-password@proxy.example.invalid/" + path
				flags := ""
				diagnostic := "go: refusing to pass credentials to insecure URL: " + proxy + "/example.invalid/missing/@v/v1.0.0.mod"
				if problem == "flags" {
					flags = flag
					diagnostic = "go: parsing $GOFLAGS: unknown flag " + flag
				}
				settings, err := json.Marshal(map[string]string{"GOROOT": goroot, "GOMODCACHE": cache, "GOPROXY": proxy})
				require.NoError(t, err)
				script := "#!/bin/sh\nset -eu\ncase \"$1\" in\nenv) [ \"$2\" = -json ]; printf '%s\\n' " + shquote.Quote(string(settings)) + " ;;\nlist) exit 0 ;;\n" +
					"build) [ \"$GOPROXY\" = off ]; [ \"$GOFLAGS\" = -mod=readonly ]; out=''; while [ \"$#\" -gt 0 ]; do if [ \"$1\" = -o ]; then shift; out=$1; fi; shift; done; printf 'placeholder, never executed' > \"$out\" ;;\n" +
					"mod) [ \"$GOWORK\" = off ]; printf '%s\\n' " + shquote.Quote(diagnostic) + "; printf '%s\\n' " + shquote.Quote(diagnostic) + " >&2; exit 7 ;;\n*) exit 9 ;;\nesac\n"
				require.NoError(t, os.WriteFile(filepath.Join(goroot, "bin", "go"), []byte(script), 0o700))
				t.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("GOPROXY", proxy)
				t.Setenv("GOFLAGS", flags)
				work := filepath.Join(t.TempDir(), "run")
				_, err = verify.Prepare(t.Context(), verify.Plan{CandidateRepo: repo, CandidateCommit: commit, CandidateModule: ".", Binaries: map[string]string{"app": "."},
					SuiteRepo: repo, SuiteCommit: commit, SuiteModule: "eval", SuiteCommand: ".", Tests: verify.TestsWritten, WorkDir: work}, t.TempDir())
				var stage *verify.StageError
				require.ErrorAs(t, err, &stage)
				assert.Equal(t, verify.StageBuild, stage.Stage)
				label, summary := "candidate", "The candidate did not build."
				if target == "written" {
					label, summary = "suite", "The evaluator suite did not build."
				}
				assert.Equal(t, summary, stage.Summary)
				private := filepath.Join(work, "evidence", "go-mod-download-"+label+".stderr")
				assert.ErrorContains(t, err, "diagnostic output is private in "+private)
				data, readErr := os.ReadFile(private)
				require.NoError(t, readErr)
				assert.Equal(t, diagnostic+"\n", string(data))
				exporter := tracetest.NewInMemoryExporter()
				provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
				t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
				_, span := provider.Tracer("test").Start(t.Context(), "surla.controlplane.prepare_verification")
				wrapped := fmt.Errorf("prepare verification: %w", err)
				assert.ErrorIs(t, recordBuildError(span, wrapped, "prepare verification"), err)
				span.End()
				for _, marker := range []string{user, path, flag} {
					assert.NotContains(t, wrapped.Error(), marker)
				}
				spans := exporter.GetSpans()
				require.Len(t, spans, 1)
				assert.Equal(t, codes.Error, spans[0].Status.Code)
				require.NotEmpty(t, spans[0].Events)
				for _, attribute := range spans[0].Attributes {
					for _, marker := range []string{user, path, flag} {
						assert.NotContains(t, attribute.Value.Emit(), marker)
					}
				}
				for _, event := range spans[0].Events {
					for _, attribute := range event.Attributes {
						for _, marker := range []string{user, path, flag} {
							assert.NotContains(t, attribute.Value.Emit(), marker)
						}
					}
				}
			})
		}
	}
}
