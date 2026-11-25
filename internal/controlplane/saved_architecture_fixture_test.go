// This file supplies saved evaluated facts for snapshot-adoption and page tests.
// Compiler extraction remains covered by the real design-evaluation journeys.
package controlplane

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

// seedArchitectureRefreshForTest binds authored architectureTestDesign facts to
// the real draft's digest, source, module and provenance. Normal adoption saves
// them; no caller bypasses the card's persistence or revision checks.
func seedArchitectureRefreshForTest(
	t *testing.T, cp *ControlPlane, caller context.Context, systemName string,
) *genhighbay.StoryRefreshResult {
	t.Helper()
	principal, ok := PrincipalFromContext(caller)
	require.True(t, ok, "the saved refresh belongs to an authenticated caller")
	seedStoryRefreshForTest(t, cp, principal.ID, storyRefreshStateSucceeded, nil, "")
	workspace, device, inode := cp.currentWorkspaceTarget()
	key := storyRefreshKey{
		owner: principal.ID, workspaceDir: workspace, workspaceDev: device, workspaceIno: inode,
	}
	cp.storyRefreshMu.Lock()
	op := cp.storyRefreshes[key]
	op.architecture = architectureSnapshotForTest(op.snapshotDigest, systemName)
	cp.storyRefreshMu.Unlock()
	refreshed, err := cp.GetStoryRefresh(caller)
	require.NoError(t, err)
	return refreshed
}

// architectureSnapshotForTest supplies the saved consumer fixture. The retained
// real evaluation journey compares every field against the exact design input.
func architectureSnapshotForTest(digest, systemName string) *savedArchitecture {
	serviceID := "service:" + systemName
	agentID := "agent:" + systemName + ".refund_agent"
	return &savedArchitecture{
		DesignDigest: digest, SystemName: systemName,
		Containers: []*genhighbay.C4ContainerItem{
			{ID: serviceID, Name: systemName, Kind: "service", Description: "Public Orders HTTP and MCP service"},
			{ID: "toolset:refund_tools", Name: "refund_tools", Kind: "toolset", Description: "Order lookup and payment refund tools"},
			{ID: agentID, Name: "refund_agent", Kind: "agent", Description: "Executes durable order refund workflows"},
		},
		Edges: []*genhighbay.C4EdgeItem{
			{FromID: serviceID, ToID: agentID, Protocol: "Goa-AI Agent Workflow"},
			{FromID: agentID, ToID: "toolset:refund_tools", Protocol: "Goa-AI Toolset"},
		},
		AgentPolicies: []*genhighbay.AgentPolicyItem{{
			AgentID: agentID, MaxExecutionSeconds: 120, MaxToolCalls: 6, MaxRecoveryTurns: 2,
		}},
		Tools: []*genhighbay.ToolContractItem{{
			ID: systemName + ".refund_agent/refund_tools.issue_refund", Toolset: systemName + ".refund_tools",
			Name: "issue_refund", Description: "Issue a payment refund",
			ArgsSchema:     "{\n          \"$schema\": \"https://json-schema.org/draft/2020-12/schema\",\n          \"additionalProperties\": false,\n          \"properties\": {\n            \"order_id\": {\n              \"type\": \"string\"\n            }\n          },\n          \"required\": [\n            \"order_id\"\n          ],\n          \"type\": \"object\"\n        }",
			ReturnSchema:   "{\n          \"$schema\": \"https://json-schema.org/draft/2020-12/schema\",\n          \"additionalProperties\": false,\n          \"properties\": {\n            \"status\": {\n              \"type\": \"string\"\n            }\n          },\n          \"required\": [\n            \"status\"\n          ],\n          \"type\": \"object\"\n        }",
			InjectedFields: []string{"tenant_id"}, RequiresConfirmation: true,
		}},
	}
}
