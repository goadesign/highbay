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
			})
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
