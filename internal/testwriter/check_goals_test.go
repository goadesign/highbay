// These tests cover author-facing goals without generating or running code.
package testwriter

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestV1OutsideModuleGoals preserves file/base/absence facts while letting
// the test writer choose how to restore the required working-copy contents.
func TestV1OutsideModuleGoals(t *testing.T) {
	t.Parallel()
	base := strings.Repeat("b", 40)
	for _, tc := range []struct {
		name  string
		added bool
		goal  string
	}{
		{name: "modified or removed", goal: "make it match its version in commit " + base},
		{name: "added", added: true, goal: "has no such file: make your working copy match that absence"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			findings := outsideModule([]changedPath{{path: "docs/guide [copy].md", added: tc.added}}, "services/shop/eval", base)
			require.Len(t, findings, 1)
			assert.Equal(t, FindingOutsideModule, findings[0].Kind)
			assert.Contains(t, findings[0].Message, "docs/guide [copy].md")
			assert.Contains(t, findings[0].Message, "commit "+base)
			assert.Contains(t, findings[0].Message, tc.goal)
			assert.Contains(t, findings[0].Message, "keep your own tests inside services/shop/eval uncommitted")
			assert.Contains(t, findings[0].Message, "Surla commits what it accepts")
			assert.NotContains(t, findings[0].Message, "git restore")
			assert.NotContains(t, findings[0].Message, "git rm")
			assert.NotContains(t, findings[0].Message, "delete")
			assert.NotContains(t, findings[0].Message, "history was rewritten")
		})
	}
	changed := []changedPath{{path: "services/shop/eval/hooks.go"}}
	for i := range maxOutsideFindings + 1 {
		changed = append(changed, changedPath{path: fmt.Sprintf("outside/%02d.go", i)})
	}
	findings := outsideModule(changed, "services/shop/eval", base)
	require.Len(t, findings, maxOutsideFindings+1)
	assert.Contains(t, findings[maxOutsideFindings].Message, "1 more files outside the test module services/shop/eval")
	assert.NotContains(t, fmt.Sprint(findings), "hooks.go", "test-module files remain in the writer's scope")
}
