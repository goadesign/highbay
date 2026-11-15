// This file checks that generate_code keeps the tests a test module's Story
// table requires.
package controlplane

import (
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/testmodule"
)

// storySuiteDesign is a test module's design with one service and one
// evaluation suite, to which a Story table adds its scenarios.
const storySuiteDesign = `package design

import (
	. "goa.design/goa-ai/eval/dsl"
	. "goa.design/goa/v3/dsl"
)

var _ = Service("orders", func() {
	Method("get_order", func() {
		Payload(String)
		Result(String)
		HTTP(func() { GET("/orders/{id}") })
	})
})

var _ = Suite("orders_acceptance", func() {
	Description("Acceptance suite.")
	Timeout("30s")
	Scenario("gets_order", func() {
		Description("Gets an order.")
	})
})
`

// TestGenerateCodeKeepsTheStoryTablesTests regenerates the project's eval/
// module, whose design holds a Story table with one finished flow, with
// generate_code and target eval: the generated suite still requires the
// flow's hook.
func TestGenerateCodeKeepsTheStoryTablesTests(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the sandboxed Go toolchain; the full suite (go test without -short) covers it")
	}
	stories := []designeval.StorySummary{{
		Name:  "Refunds",
		Flows: []designeval.StoryFlow{{Key: "refund-over-limit", Title: "A refund over the limit.", Complete: true}},
	}}
	table, _, err := designeval.StoryTests(fstest.MapFS{"design/design.go": {Data: []byte(storySuiteDesign)}}, stories)
	require.NoError(t, err)
	workspace := t.TempDir()
	writeGenerationFiles(t, workspace, testmodule.Snapshot(t, "", generationDesign))
	writeGenerationFiles(t, filepath.Join(workspace, "eval"), testmodule.Module(t, "example.com/design/eval", "", map[string]string{
		"design/design.go":        storySuiteDesign,
		designeval.StoryTestsPath: string(table),
	}))
	projectEval := generationFiles(t, workspace, "eval")
	commitGenerationProject(t, workspace)
	cp := newControlPlane(t, Options{
		WorkspaceDir: workspace, CardsDir: t.TempDir(),
		Evaluator: designeval.NewEvaluatorInDir(t.TempDir()),
		Team:      &TeamBinding{Owner: "developer@example.com", Service: &teamServiceFixture{}},
	})
	_, assignment := generationNativeBuild(t, cp)
	working := assignment.WorkingFolder
	eval := "eval"
	res, err := cp.GenerateCode(
		WithPrincipal(generationRequest(t), Principal{ID: "developer@example.com", CallerID: "developer@example.com", Kind: CallerBuilder}),
		&gensurla.GenerateCodePayload{Target: &eval},
	)
	require.NoError(t, err)
	const suite = "gen/evals/orders_acceptance/suite.go"
	assert.Contains(t, res.FilesWritten, suite)
	assert.Contains(t, generationFiles(t, filepath.Join(working, "eval"), "gen")[suite],
		"RefundOverLimit(context.Context) (eval.Result, error)", "the table's flow stays a scenario of the suite")
	assert.Equal(t, string(table), generationFiles(t, filepath.Join(working, "eval"), "design")[designeval.StoryTestsPath],
		"generation leaves the table as it is")
	assert.Equal(t, projectEval, generationFiles(t, workspace, "eval"), "the original test module is untouched")
}
