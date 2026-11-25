// These tests carry private process diagnostics through their actual consumers.
package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/goadesign/highbay/internal/verify"
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
	path, err := filepath.Abs("../../internal/verify/testdata")
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
	t.Setenv("HIGHBAY_TEST_SERVER_VALUE", "FAKE_SERVER_VALUE_521")
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

// TestProcessDiagnosticsStayPrivateInCLI preserves filename-only recovery after bookkeeping cleanup.
func TestProcessDiagnosticsStayPrivateInCLI(t *testing.T) {
	for _, tc := range processDiagnosticSmokeCases(t) {
		t.Run(tc.Name, func(t *testing.T) {
			plan := processDiagnosticPlan(t, tc.Name)
			err := runVerify([]string{
				"-candidate-repo", plan.CandidateRepo, "-candidate-commit", plan.CandidateCommit, "-candidate-module", "app", "-binary", "app=.",
				"-suite-repo", plan.SuiteRepo, "-suite-commit", plan.SuiteCommit, "-suite-module", "eval", "-suite-command", ".",
				"-request-revision", plan.RequestRevision, "-work-dir", plan.WorkDir, "-report-dir", plan.ReportDir, "-timeout", "20s",
			})
			require.Error(t, err)
			assert.ErrorContains(t, err, "at stage "+tc.Stage)
			assert.ErrorContains(t, err, tc.Summary)
			assert.ErrorContains(t, err, filepath.Join(plan.WorkDir, "evidence", tc.File))
			if tc.Exit != 0 {
				assert.ErrorContains(t, err, "exit status 7")
			}
			assertProcessDiagnosticText(t, err.Error())
			assertProcessDiagnosticFiles(t, plan, tc)
		})
	}
}
