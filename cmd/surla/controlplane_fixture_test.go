// This file binds the control plane to owned project and workflow test fixtures.
package main

import (
	"context"

	genproject "github.com/goadesign/surla/gen/project"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/project"
)

// fixtureProjectOptions binds the workspace and owner from the same state the CLI attaches to.
func fixtureProjectOptions(state *project.State, team controlplane.Team) controlplane.Options {
	return controlplane.Options{
		CardsDir:     state.CardsDir(),
		WorkspaceDir: state.Record.Directory,
		Team:         &controlplane.TeamBinding{Owner: state.Record.Owner, Service: team},
	}
}

// openFixtureProjectCards holds the real writer lock when a scripted HTTP peer
// stands in for the project server. The caller owns and releases the lock.
func openFixtureProjectCards(state *project.State) (*controlplane.ControlPlane, error) {
	return controlplane.New(fixtureProjectOptions(state, &workflowTeamFixture{}))
}

// fixtureWorkflowTeam binds the team stand-in to the workflow's authenticated owner.
func fixtureWorkflowTeam(owner string) *controlplane.TeamBinding {
	return &controlplane.TeamBinding{Owner: owner, Service: &workflowTeamFixture{}}
}

// attachFixtureGuide issues a role through the project owner without a desktop pane.
func attachFixtureGuide(
	ctx context.Context, state *project.State, server *project.Server,
) (*genproject.AttachGuideResult, error) {
	management := controlplane.WithPrincipal(ctx, controlplane.Principal{
		ID: state.Record.Owner, CallerID: "management", Kind: controlplane.CallerManagement,
	})
	return server.AttachGuide(management, &genproject.AttachGuidePayload{})
}
