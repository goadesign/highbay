//go:build scaffold

package scaffold

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/eval"

	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/testmodule"
	"github.com/goadesign/surla/internal/testwriter"
)

// TestInitBuildsForNamesThatCollideWithTemplatesOrGo creates real example
// projects (network needed) for names that match the example's template
// identifiers or that Goa generates under another package name; Init vets
// both modules. The blank project's templates name nothing after the service
// but its command, and TestInitCreatesAProjectWhoseSuitePasses covers its
// renamed package.
func TestInitBuildsForNamesThatCollideWithTemplatesOrGo(t *testing.T) {
	for _, name := range []string{"helper", "service", "catalog", "errors", "string"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			_, err := Init(t.Context(), Options{Dir: dir, WorkDir: t.TempDir(), Module: "example.com/" + name, SurlaVersion: published, Example: true}, RunCommand, starterGenerator(t))
			require.NoError(t, err, "Init runs go vet on both modules")
		})
	}
}

// TestInitCreatesAProjectWhoseSuitePasses creates real projects of both
// variants with the published Surla version (it needs the network and macOS
// or Linux), builds the service and its evaluator suite, and runs the suite
// against the service: every scenario must pass. For the blank project that
// is the one scenario that starts the program; for the example it includes
// the one the Story table requires for its finished flow, through the method
// the example ships for it. It then does what the first build of a change
// does with the project's Story: it evaluates the design and prepares a test
// writer's working copy from the evaluated Story. The Story table rendered
// from the evaluated Story is the one the project ships, and preparing
// changes no file of the copy, so the project's generated test program is
// what a build prepares.
//
// Each case creates one real project, which resolves, generates, vets and
// tests two modules; no cheaper layer runs Goa's generator on the templates.
func TestInitCreatesAProjectWhoseSuitePasses(t *testing.T) {
	prices := []designeval.StoryScenario{
		{Story: "Prices", Key: "prices-cookie", Scenario: "prices_cookie", Hook: "PricesCookie"},
	}
	// errors is generated as package errors_, so the example's second case
	// also proves its imports follow the package goa gen produced. The blank
	// project imports no generated package, so one case covers it.
	for _, tc := range []struct {
		name      string
		example   bool
		service   string
		scenarios []string
		stories   []string
		required  []designeval.StoryScenario
	}{
		{name: "blank", service: "shop", scenarios: []string{"starts_and_listens"}},
		{name: "example", example: true, service: "shop", stories: []string{"Prices"}, required: prices,
			scenarios: []string{"prices_tea", "prompt_override_reaches_model", "unknown_item", "prices_cookie"}},
		{name: "example renamed package", example: true, service: "errors", stories: []string{"Prices"}, required: prices,
			scenarios: []string{"prices_tea", "prompt_override_reaches_model", "unknown_item", "prices_cookie"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, scenarios := tc.service, tc.scenarios
			dir := filepath.Join(t.TempDir(), service)
			name, err := Init(t.Context(), Options{
				Dir: dir, WorkDir: t.TempDir(), Module: "example.com/" + service, SurlaVersion: published, Example: tc.example,
			}, RunCommand, starterGenerator(t))
			require.NoError(t, err)
			// With the real generator, the project holds exactly what its
			// first commit stages: for instance, no guide for coding agents
			// when the design declares no agent.
			assertOnly(t, dir, FirstCommitPaths(tc.example)...)
			assertReadmeStagesTheProject(t, dir)
			bin := t.TempDir()
			build := func(moduleDir, out, pkg string) {
				cmd := exec.CommandContext(t.Context(), "go", "build", "-o", filepath.Join(bin, out), pkg)
				cmd.Dir = moduleDir
				cmd.Env = append(cmd.Environ(), "GOWORK=off")
				b, err := cmd.CombinedOutput()
				require.NoError(t, err, string(b))
			}
			test := exec.CommandContext(t.Context(), "go", "test", "./...")
			test.Dir = filepath.Join(dir, "eval")
			test.Env = append(test.Environ(), "GOWORK=off")
			testOut, err := test.CombinedOutput()
			require.NoError(t, err, "the generated evaluator's own tests pass:\n%s", testOut)
			build(dir, name, "./cmd/"+name)

			runSuite := func(want []string) {
				build(filepath.Join(dir, "eval"), name+"-evals", "./gen/cmd/"+name+"-evals")
				out, err := exec.CommandContext(t.Context(), filepath.Join(bin, name+"-evals"),
					"--artifact", name+"="+filepath.Join(bin, name)).Output()
				require.NoError(t, err, string(out))
				var report eval.Report
				require.NoError(t, json.Unmarshal(out, &report))
				assert.True(t, report.Passed)
				ids := make([]string, 0, len(report.Scenarios))
				for _, s := range report.Scenarios {
					ids = append(ids, s.ID)
				}
				assert.ElementsMatch(t, want, ids)
				for _, s := range report.Scenarios {
					require.NotNil(t, s.Result, s.ID)
					assert.True(t, s.Passed, "%s: %+v", s.ID, s.Result.Checks)
				}
			}
			runSuite(scenarios)
			if !tc.example {
				// The blank project's one scenario must fail for a program
				// that exits before it binds an address. It dials only the
				// address the program itself reports, so another program's
				// listener can never make it pass.
				quits := filepath.Join(t.TempDir(), "main.go")
				require.NoError(t, os.WriteFile(quits, []byte("package main\n\nfunc main() {}\n"), 0o600))
				build(filepath.Dir(quits), "quits", quits)
				out, _ := exec.CommandContext(t.Context(), filepath.Join(bin, name+"-evals"),
					"--artifact", name+"="+filepath.Join(bin, "quits")).Output()
				var report eval.Report
				require.NoError(t, json.Unmarshal(out, &report), string(out))
				assert.False(t, report.Passed)
				require.Len(t, report.Scenarios, 1)
				require.NotNil(t, report.Scenarios[0].Result)
				for _, c := range report.Scenarios[0].Result.Checks {
					assert.False(t, c.Passed, "%s passed for a program that never listened", c.Name)
				}
			}

			ev, err := designeval.NewEvaluatorWithCache(t.TempDir(), testmodule.CompilerCache(t))
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, ev.Close()) })
			design, err := designeval.ReadModuleSnapshot(dir)
			require.NoError(t, err)
			evaluated, err := ev.Evaluate(t.Context(), design)
			require.NoError(t, err, "the starter's design, with its Story, evaluates")
			var stories []string
			for _, s := range evaluated.Stories {
				stories = append(stories, s.Name)
			}
			assert.Equal(t, tc.stories, stories)
			table, required, err := designeval.StoryTests(os.DirFS(filepath.Join(dir, "eval")), evaluated.Stories)
			require.NoError(t, err)
			assert.Equal(t, tc.required, required)
			shipped, err := os.ReadFile(filepath.Join(dir, "eval", filepath.FromSlash(designeval.StoryTestsPath)))
			require.NoError(t, err)
			assert.Equal(t, string(table), string(shipped), "the starter ships the table its evaluated Story gives")

			// The user makes the first commit, as the README says.
			for _, args := range [][]string{
				{"init", "--quiet"}, {"config", "user.name", "Test User"}, {"config", "user.email", "test@example.com"},
				{"config", "commit.gpgsign", "false"}, {"add", "--all"}, {"commit", "--quiet", "-m", "Start from surla init"},
			} {
				out, err := exec.CommandContext(t.Context(), "git", append([]string{"-C", dir}, args...)...).CombinedOutput()
				require.NoError(t, err, "git %v: %s", args, out)
			}
			wc, err := testwriter.MakeCopy(t.Context(), dir, testwriter.TestsRole, testwriter.TestsRole.Branch(1), "HEAD")
			require.NoError(t, err)
			prepared, err := testwriter.NewChecker(ev, t.TempDir()).Prepare(t.Context(), wc,
				testwriter.Suite{Module: SuiteDir, Command: designeval.SuiteCommandPackage(name)},
				testwriter.TestsRole.Branch(1), "HEAD", evaluated.Stories)
			require.NoError(t, err)
			assert.Equal(t, required, prepared)
			status := exec.CommandContext(t.Context(), "git", "-C", wc.Dir(), "status", "--porcelain", "--untracked-files=all")
			out, err := status.Output()
			require.NoError(t, err)
			assert.Empty(t, string(out), "preparing the first build changes no file of the starter")
		})
	}
}

// starterGenerator keeps each Init's scratch private while sharing only the
// compiler cache selected by trusted test setup. Init owns closing the generator.
func starterGenerator(t *testing.T) *designeval.Evaluator {
	t.Helper()
	ev, err := designeval.NewEvaluatorWithCache(t.TempDir(), testmodule.CompilerCache(t))
	require.NoError(t, err)
	return ev
}
