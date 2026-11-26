// This file proves typed refusal disclosure at the real worker MCP boundary
// and preserves the ownership of independently joined failures.
package controlplane

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"
)

// TestMCPWorkerRefusalKeepsItsNameAndFacts drives the generated MCP adapter,
// preserving the exact operation, branches, paths and total count authored by
// the copy owner. Starting or dropping a build remains a user HTTP operation.
func TestMCPWorkerRefusalKeepsItsNameAndFacts(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	branch, own := "'topic'", "highbay/build-2"
	message := "Your working copy is on 'topic'; change exports is built on highbay/build-2. " +
		"A merge is in progress. Uncommitted: 'a.go', 'b.go', 'c.go', 'd.go', 'e.go' (7 paths). " +
		"Put your work on highbay/build-2, commit it, then call get_builder_assignment again."
	refusal := &genhighbay.CopyOffBranch{
		Name: "copy_off_branch", Message: message, Branch: &branch, BuildBranch: &own,
		Operation: "merge", Uncommitted: []string{"'a.go'", "'b.go'", "'c.go'", "'d.go'", "'e.go'"},
		UncommittedCount: 7,
	}
	for _, wrapped := range []bool{false, true} {
		t.Run(map[bool]string{false: "direct", true: "wrapped"}[wrapped], func(t *testing.T) {
			var failure error = refusal
			if wrapped {
				failure = fmt.Errorf("private wrapper context: %w", refusal)
			}
			service := refusalContractService{Service: cp, refusal: failure}
			server := httptest.NewServer(NewHTTPHandler(service,
				func(*http.Request) (Principal, error) {
					return Principal{ID: "alice@example.com", Kind: CallerBuilder}, nil
				}))
			defer server.Close()
			shown, result := mcpToolCall(t, server.URL, "fixture", "get_builder_assignment", `{"change_id":"exports"}`)
			assert.Empty(t, result)
			assert.Equal(t, "copy_off_branch: "+message, shown)
			assert.NotContains(t, shown, "private wrapper context")
			listed := listToolNames(t, server.URL, "fixture")
			assert.False(t, listed["start_build"])
			assert.False(t, listed["drop_build"])
		})
	}
}

// TestMCPRefusalMappingKeepsIndependentFailureOwnership prevents the mapper
// from selecting one typed child of a joined storage or cleanup failure.
func TestMCPRefusalMappingKeepsIndependentFailureOwnership(t *testing.T) {
	refusal := &genhighbay.CopyOffBranch{Name: "copy_off_branch", Message: "Put your work on your build's branch."}
	for _, failure := range []error{
		errors.New("storage failed"),
		&goa.ServiceError{Name: "fault", Message: "storage failed", Fault: true},
		errors.Join(refusal, errors.New("cleanup failed")),
		errors.Join(refusal, &goa.ServiceError{Name: "fault", Message: "storage failed", Fault: true}),
	} {
		assert.Same(t, failure, mapMCPRefusal(failure), "independent failures keep their original identity")
	}
	for _, failure := range []error{refusal, fmt.Errorf("read: %w", refusal), errors.Join(refusal)} {
		mapped := mapMCPRefusal(failure)
		var disclosed *goa.ServiceError
		require.ErrorAs(t, mapped, &disclosed)
		assert.Equal(t, refusal.Name, disclosed.Name)
		assert.Equal(t, refusal.Name+": "+refusal.Message, disclosed.Message)
		assert.False(t, disclosed.Fault)
	}
	open := &genhighbay.OpenBuildRefusal{Name: "previous_change_open", Message: "Merge exports or drop it."}
	assert.EqualError(t, mapMCPRefusal(open), "previous_change_open: Merge exports or drop it.")
}
