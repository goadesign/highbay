package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/scaffold"
	"github.com/goadesign/surla/internal/shquote"
)

// TestInitNextStepsGiveAWorkingFirstRunPath verifies the CLI handoff for the
// blank project and the worked example: the first line says which was
// created, the printed commands stage the entries the scaffold says that
// project has (the scaffold's tests hold that list to what Init writes),
// commit them and end by starting surla in it, the operator tools are named
// only after that, and the text points to the documented change steps.
func TestInitNextStepsGiveAWorkingFirstRunPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		example bool
		created string
		first   string
		staged  string
	}{
		{name: "blank", created: initCreatedBlank,
			first:  "Created the bakery service as a blank project: no methods yet,",
			staged: "git add .gitignore README.md go.mod go.sum design gen cmd eval"},
		{name: "worked example", example: true, created: initCreatedExample,
			first:  "Created the bakery service as Surla's worked example:",
			staged: "git add .gitignore README.md AGENTS_QUICKSTART.md go.mod go.sum design gen internal cmd eval"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			staged := strings.Join(scaffold.FirstCommitPaths(tc.example), " ")
			out := fmt.Sprintf(initNextSteps, shquote.Quote("/tmp/my shop"), "bakery", fmt.Sprintf(tc.created, "bakery"), staged)
			assert.True(t, strings.HasPrefix(out, tc.first), out)
			var commands []string
			for line := range strings.Lines(out) {
				if command, ok := strings.CutPrefix(line, "  "); ok {
					commands = append(commands, strings.TrimSuffix(command, "\n"))
				}
			}
			assert.Equal(t, []string{
				`cd '/tmp/my shop'`,
				"git init",
				tc.staged,
				`git commit -m "Start bakery from surla init"`,
				"surla",
			}, commands)
			_, after, ok := strings.Cut(out, "\n  surla\n")
			require.True(t, ok)
			assert.Contains(t, after, "views page")
			assert.Contains(t, after, "Operator tools: surla verify and surla approve")
			assert.Contains(t, out, "check_project before committing")
			assert.Contains(t, out, "confirmed Change Card's request_revision")
			assert.Contains(t, out, "surla://golden-path")
			assert.Contains(t, out, "docs/REFERENCE.md#mcp-tools")
		})
	}
}

// TestRunVersionPrintsModuleVersionAndRejectsExtraArguments verifies `surla
// version`, `surla version -h`, `surla help version`, and `surla help`.
func TestRunVersionPrintsModuleVersionAndRejectsExtraArguments(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, run([]string{"version"}, &out))
	want := buildVersion()
	if want == "" {
		want = "(devel)"
	}
	assert.Equal(t, want+"\n", out.String())

	out.Reset()
	require.NoError(t, run([]string{"version", "-h"}, &out))

	out.Reset()
	require.NoError(t, run([]string{"help", "version"}, &out))

	out.Reset()
	require.NoError(t, run([]string{"help"}, &out))
	assert.Contains(t, out.String(), "version         print the module version this surla binary was built from")

	err := run([]string{"version", "extra"}, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `unexpected arguments ["extra"]`)
}
