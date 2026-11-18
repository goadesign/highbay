// These tests carry private process diagnostics through their actual consumers.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/verify"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// processDiagnosticCase selects one independently planted child-output branch.
type processDiagnosticCase struct {
	Name    string
	Stage   string
	Summary string
	File    string
	Exit    int
}

// processDiagnosticSmokeCases selects the two forwarding shapes; verify's
// TestProcessDiagnosticsStayPrivate owns the complete failure matrix.
func processDiagnosticSmokeCases(t *testing.T) []processDiagnosticCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(processDiagnosticData(t), "process_diagnostics.json"))
	require.NoError(t, err)
	var cases []processDiagnosticCase
	require.NoError(t, json.Unmarshal(data, &cases))
	cases = slices.DeleteFunc(cases, func(tc processDiagnosticCase) bool {
		return tc.Name != "clone-candidate" && tc.Name != "go-env-malformed-candidate"
	})
	require.Len(t, cases, 2, "both forwarding cases must remain in the owner's inventory")
	return cases
}

// processDiagnosticData resolves trusted stand-ins, never candidate source.
func processDiagnosticData(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../verify/testdata")
	require.NoError(t, err)
	return path
}

// processDiagnosticPlan creates isolated repositories and trusted process stand-ins.
func processDiagnosticPlan(t *testing.T, mode string) verify.Plan {
	t.Helper()
	root := t.TempDir()
	script := filepath.Join(processDiagnosticData(t), "process_diagnostics.sh")
	cmd := exec.Command("/bin/sh", script, "setup", root)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	commit, err := os.ReadFile(filepath.Join(root, "commit"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "case"), []byte(mode), 0o600))
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+filepath.Join(root, "goroot", "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SURLA_TEST_SERVER_VALUE", "FAKE_SERVER_VALUE_521")
	t.Setenv("ANTHROPIC_API_KEY", "FAKE_PROVIDER_VALUE_521")
	return verify.Plan{
		CandidateRepo: filepath.Join(root, "repo"), CandidateCommit: strings.TrimSpace(string(commit)), CandidateModule: "app", Binaries: map[string]string{"app": "."},
		SuiteRepo: filepath.Join(root, "repo"), SuiteCommit: strings.TrimSpace(string(commit)), SuiteModule: "eval", SuiteCommand: ".",
		Tests: verify.TestsEvaluator, RequestRevision: "rev-1", WorkDir: filepath.Join(t.TempDir(), "work"), ReportDir: t.TempDir(),
	}
}

// assertProcessDiagnosticText rejects all planted bytes and value-bearing decoder errors.
func assertProcessDiagnosticText(t *testing.T, text string) {
	t.Helper()
	assert.NotContains(t, text, "FAKE_")
	assert.NotContains(t, text, "invalid character")
	assert.NotContains(t, text, "cannot unmarshal")
	assert.NotContains(t, text, "parsing time")
}

// assertProcessDiagnosticFiles allows planted bytes only in this command's private evidence.
func assertProcessDiagnosticFiles(t *testing.T, plan verify.Plan, tc processDiagnosticCase) {
	t.Helper()
	base := strings.TrimSuffix(strings.TrimSuffix(tc.File, ".stdout"), ".stderr")
	private := filepath.Join(plan.WorkDir, "evidence", base)
	data, err := os.ReadFile(private + filepath.Ext(tc.File))
	require.NoError(t, err)
	assert.Contains(t, string(data), "FAKE_", "the actual failing child wrote the planted value")

	for _, folder := range []string{plan.WorkDir, plan.ReportDir} {
		require.NoError(t, filepath.WalkDir(folder, func(name string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if name != private+".stdout" && name != private+".stderr" {
				assertProcessDiagnosticText(t, string(data))
			}
			return nil
		}))
	}
	info, err := os.Stat(filepath.Join(plan.WorkDir, "evidence"))
	require.NoError(t, err)
	assert.Equal(t, fs.FileMode(0o700), info.Mode().Perm())
}

// TestProcessDiagnosticsStayPrivateInSpans records actual errors without suppressing failure status.
func TestProcessDiagnosticsStayPrivateInSpans(t *testing.T) {
	for _, tc := range processDiagnosticSmokeCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			plan := processDiagnosticPlan(t, tc.Name)
			_, err := verify.Run(t.Context(), plan, os.Environ(), t.TempDir())
			var stage *verify.StageError
			require.ErrorAs(t, err, &stage)
			assert.Equal(t, tc.Stage, string(stage.Stage))
			assert.Equal(t, tc.Summary, stage.Summary)
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
			_, span := provider.Tracer("test").Start(t.Context(), "surla.controlplane.prepare_verification")
			wrapped := fmt.Errorf("prepare verification: %w", err)
			assert.ErrorIs(t, recordBuildError(span, wrapped, "prepare verification"), err)
			span.End()
			assertProcessDiagnosticText(t, wrapped.Error())
			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, codes.Error, spans[0].Status.Code)
			require.NotEmpty(t, spans[0].Events)
			encoded, err := json.Marshal(spans)
			require.NoError(t, err)
			assertProcessDiagnosticText(t, string(encoded))
			assert.Contains(t, string(encoded), filepath.Join(plan.WorkDir, "evidence", tc.File))
			assertProcessDiagnosticFiles(t, plan, tc)
		})
	}
}

// TestProcessDiagnosticsStayPrivateInLiveEvents uses real Prepare and the live log-tail event.
func TestProcessDiagnosticsStayPrivateInLiveEvents(t *testing.T) {
	for _, tc := range processDiagnosticSmokeCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			plan := processDiagnosticPlan(t, tc.Name)
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
			cards := t.TempDir()
			commit := plan.CandidateCommit
			plan.CandidateCommit, plan.RequestRevision, plan.WorkDir = "", "", ""
			cp := newControlPlane(t, buildFixtureOptions(t, Options{BuildCacheDir: t.TempDir(),
				CardsDir: cards, TracerProvider: provider,
				Verification: &VerificationConfig{RunsDir: t.TempDir(), Plan: plan},
				Live:         &LiveConfig{Command: []string{os.Args[0]}, Env: append(os.Environ(), liveTargetModeEnv+"=process-diagnostics"), StartTimeout: 20 * time.Second},
			}))
			seedLiveChange(t, cp)
			_, err := cp.StartLiveTarget(asCaller("alice@example.com", CallerBuilder), &gensurla.StartLiveTargetPayload{ChangeID: liveTestChangeID, CandidateCommit: commit})
			assertServiceError(t, err, "live_target_failed")
			assertProcessDiagnosticText(t, err.Error())
			var tail string
			var failed bool
			for _, span := range recorder.Ended() {
				if span.Status().Code == codes.Error {
					failed = true
				}
				for _, kv := range span.Attributes() {
					assertProcessDiagnosticText(t, kv.Value.Emit())
				}
				for _, event := range span.Events() {
					for _, kv := range event.Attributes {
						assertProcessDiagnosticText(t, kv.Value.Emit())
						if kv.Key == "executor_log_tail" {
							tail = kv.Value.AsString()
						}
					}
				}
			}
			assert.True(t, failed)
			assert.Contains(t, tail, tc.File)
			if tc.Exit != 0 {
				assert.Contains(t, tail, "exit status 7")
			}
			assert.Contains(t, tail, "diagnostic output is private")
		})
	}
}
