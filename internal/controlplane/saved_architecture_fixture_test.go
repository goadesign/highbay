// This file supplies saved evaluated facts for snapshot-adoption and page tests.
// Compiler extraction remains covered by the real design-evaluation journeys.
package controlplane

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	gensurla "github.com/goadesign/surla/gen/surla"
)

// seedArchitectureRefreshForTest binds authored architectureTestDesign facts to
// the real draft's digest, source, module and provenance. Normal adoption saves
// them; no caller bypasses the card's persistence or revision checks.
func seedArchitectureRefreshForTest(
	t *testing.T, cp *ControlPlane, caller context.Context, systemName string,
) *gensurla.StoryRefreshResult {
	t.Helper()
	principal, ok := PrincipalFromContext(caller)
	require.True(t, ok, "the saved refresh belongs to an authenticated caller")
	seedStoryRefreshForTest(t, cp, principal.ID, storyRefreshStateSucceeded, nil, "")
	workspace, device, inode := cp.currentWorkspaceTarget()
	key := storyRefreshKey{
		owner: principal.ID, workspaceDir: workspace, workspaceDev: device, workspaceIno: inode,
	}
	serviceID := "service:" + systemName
	agentID := "agent:" + systemName + ".refund_agent"
	cp.storyRefreshMu.Lock()
	op := cp.storyRefreshes[key]
	op.architecture = &savedArchitecture{
		DesignDigest: op.snapshotDigest, SystemName: systemName,
		Containers: []*gensurla.C4ContainerItem{
			{ID: serviceID, Name: systemName, Kind: "service", Description: "Public Orders HTTP and MCP service"},
			{ID: agentID, Name: "refund_agent", Kind: "agent", Description: "Executes durable order refund workflows"},
			{ID: "toolset:refund_tools", Name: "refund_tools", Kind: "toolset", Description: "Order lookup and payment refund tools"},
		},
		Edges: []*gensurla.C4EdgeItem{
			{FromID: serviceID, ToID: agentID, Protocol: "Goa-AI Agent Workflow"},
			{FromID: agentID, ToID: "toolset:refund_tools", Protocol: "Goa-AI Toolset"},
		},
		AgentPolicies: []*gensurla.AgentPolicyItem{{
			AgentID: agentID, MaxExecutionSeconds: 120, MaxToolCalls: 6, MaxRecoveryTurns: 2,
		}},
		Tools: []*gensurla.ToolContractItem{{
			ID: systemName + ".refund_agent/refund_tools.issue_refund", Toolset: "refund_agent.refund_tools",
			Name: "issue_refund", Description: "Issue a payment refund",
			ArgsSchema:     `{"type":"object","properties":{"order_id":{"type":"string"}},"required":["order_id"]}`,
			ReturnSchema:   `{"type":"object","properties":{"status":{"type":"string"}},"required":["status"]}`,
			InjectedFields: []string{"tenant_id"}, RequiresConfirmation: true,
		}},
	}
	cp.storyRefreshMu.Unlock()
	refreshed, err := cp.GetStoryRefresh(caller)
	require.NoError(t, err)
	return refreshed
}
