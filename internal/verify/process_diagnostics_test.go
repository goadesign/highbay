// These tests carry private process diagnostics through their actual consumers.
package verify

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// processDiagnosticCase selects one independently planted child-output branch.
type processDiagnosticCase struct {
	Name    string
	Stage   string
	Summary string
	File    string
	Exit    int
}

// processDiagnosticCases reads the shared inventory used by every real consumer.
func processDiagnosticCases(t *testing.T) []processDiagnosticCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(processDiagnosticData(t), "process_diagnostics.json"))
	require.NoError(t, err)
	var cases []processDiagnosticCase
	require.NoError(t, json.Unmarshal(data, &cases))
	return cases
}

// processDiagnosticData resolves trusted stand-ins, never candidate source.
func processDiagnosticData(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("testdata")
	require.NoError(t, err)
	return path
}

// processDiagnosticPlan creates isolated repositories and trusted process stand-ins.
func processDiagnosticPlan(t *testing.T, mode string) Plan {
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
	return Plan{
		CandidateRepo: filepath.Join(root, "repo"), CandidateCommit: strings.TrimSpace(string(commit)), CandidateModule: "app", Binaries: map[string]string{"app": "."},
		SuiteRepo: filepath.Join(root, "repo"), SuiteCommit: strings.TrimSpace(string(commit)), SuiteModule: "eval", SuiteCommand: ".",
		Tests: TestsEvaluator, RequestRevision: "rev-1", WorkDir: filepath.Join(t.TempDir(), "work"), ReportDir: t.TempDir(),
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
func assertProcessDiagnosticFiles(t *testing.T, plan Plan, tc processDiagnosticCase) {
	t.Helper()
	base := strings.TrimSuffix(strings.TrimSuffix(tc.File, ".stdout"), ".stderr")
	private := filepath.Join(plan.WorkDir, "evidence", base)
	data, err := os.ReadFile(private + filepath.Ext(tc.File))
	require.NoError(t, err)
	assert.Contains(t, string(data), "FAKE_", "the actual failing child wrote the planted value")
	if base == "describe" && tc.Name == "describe" || base == "suite" {
		stderr, err := os.ReadFile(private + ".stderr")
		require.NoError(t, err)
		assert.Contains(t, string(stderr), "FAKE_SERVER_VALUE_521")
		assert.Contains(t, string(stderr), "FAKE_PROVIDER_VALUE_521", "stored evaluators retain the supplied provider environment")
	}

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

// TestProcessDiagnosticsStayPrivate preserves typed failures and private raw streams.
func TestProcessDiagnosticsStayPrivate(t *testing.T) {
	for _, tc := range processDiagnosticCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			plan := processDiagnosticPlan(t, tc.Name)
			report, err := Run(t.Context(), plan, os.Environ())
			var stage *StageError
			require.ErrorAs(t, err, &stage)
			assert.Equal(t, Stage(tc.Stage), stage.Stage)
			assert.Equal(t, tc.Summary, stage.Summary)
			assertProcessDiagnosticText(t, err.Error())
			assert.ErrorContains(t, err, filepath.Join(plan.WorkDir, "evidence", tc.File))
			var process *exec.ExitError
			if tc.Exit != 0 {
				require.ErrorAs(t, err, &process)
				assert.Equal(t, tc.Exit, process.ExitCode())
				assert.Empty(t, process.Stderr)
				assert.ErrorContains(t, err, "exit status 7")
			} else {
				assert.NotErrorAs(t, err, &process)
				if tc.Name == "declaration" || strings.Contains(tc.Name, "malformed") || strings.HasPrefix(tc.Name, "report-") {
					assert.ErrorContains(t, err, "malformed")
				}
			}
			public, encodeErr := json.Marshal(report.ForBuilders())
			require.NoError(t, encodeErr)
			assertProcessDiagnosticText(t, string(public))
			assertProcessDiagnosticFiles(t, plan, tc)
		})
	}
}

// TestProcessDiagnosticsPreserveReports keeps evidence despite failed or abnormal exits.
func TestProcessDiagnosticsPreserveReports(t *testing.T) {
	for _, source := range []TestsSource{TestsEvaluator, TestsWritten} {
		for _, mode := range []string{"success", "report-partial", "report-abnormal"} {
			t.Run(string(source)+"/"+mode, func(t *testing.T) {
				plan := processDiagnosticPlan(t, mode)
				plan.Tests = source
				report, err := Run(t.Context(), plan, os.Environ())
				require.NoError(t, err)
				assert.Equal(t, mode == "success", report.Passed)
				assert.Len(t, report.Evidence.Scenarios, 1)
				if mode == "report-partial" {
					assert.Equal(t, 7, report.SuiteExit)
				}
				if mode == "report-abnormal" {
					assert.Equal(t, -1, report.SuiteExit)
				}
				_, err = os.Stat(filepath.Join(plan.ReportDir, report.Digest+".json"))
				assert.NoError(t, err)
			})
		}
	}
}
