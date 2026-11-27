// This file delegates browser handoff to the project credential owner.
package controlplane

import (
	"context"
	"errors"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"go.opentelemetry.io/otel/codes"
)

// OpenViews opens a new user session without exposing its credential or URL.
// Whether or not the browser opened, the result names the project's Chorus
// team, channel and guide handle, so a guide session that attached itself can
// join the project's conversation with this one call.
func (c *ControlPlane) OpenViews(ctx context.Context) (*genhighbay.OpenViewsResult, error) {
	team, err := c.ownedTeam(ctx)
	if err != nil {
		return nil, err
	}
	snapshot, err := team.ReadTeam(ctx)
	if err != nil {
		return nil, err
	}
	result := &genhighbay.OpenViewsResult{Team: snapshot.Team, Channel: snapshot.Channel}
	for _, role := range snapshot.Roles {
		if role.Role == "guide" {
			result.Handle = role.Handle
		}
	}
	if c.openViews == nil {
		result.Message = "This Highbay server has no browser opener configured. " +
			"The user can run highbay in the project folder to open Highbay's browser workspace."
		return result, nil
	}
	ctx, span := c.tracer("highbay/controlplane").Start(ctx, "highbay.views.open")
	defer span.End()
	if err := c.openViews(ctx); err != nil {
		// An opener failure may contain a private browser link. Record only safe owned text.
		safe := errors.New("the project views could not be opened")
		span.RecordError(safe)
		span.SetStatus(codes.Error, "open project views")
		result.Message = "The Highbay browser window could not be opened. " +
			"Try opening Highbay views again, or ask the user to run highbay in the project folder."
		return result, nil
	}
	result.Opened = true
	result.Message = "Opened Highbay's browser workspace in the user's default browser. " +
		"Tell the user clearly that the Highbay page is open (it may be in a new tab or a background window), " +
		"that the conversation with the team continues on that page, " +
		"and that asking you to open Highbay views opens it again."
	return result, nil
}
