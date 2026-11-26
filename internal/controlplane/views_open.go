// This file delegates browser handoff to the project credential owner.
package controlplane

import (
	"context"
	"errors"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"go.opentelemetry.io/otel/codes"
)

// OpenViews opens a new user session without exposing its credential or URL.
func (c *ControlPlane) OpenViews(ctx context.Context) (*genhighbay.OpenViewsResult, error) {
	if _, err := c.ownedTeam(ctx); err != nil {
		return nil, err
	}
	if c.openViews == nil {
		return &genhighbay.OpenViewsResult{Opened: false, Message: "This Highbay server has no browser opener configured. " +
			"The host must enable its browser-opening integration, or open Highbay with Open Highbay Window in the Herdr companion."}, nil
	}
	ctx, span := c.tracer("highbay/controlplane").Start(ctx, "highbay.views.open")
	defer span.End()
	if err := c.openViews(ctx); err != nil {
		// An opener failure may contain a private browser link. Record only safe owned text.
		safe := errors.New("the project views could not be opened")
		span.RecordError(safe)
		span.SetStatus(codes.Error, "open project views")
		return &genhighbay.OpenViewsResult{Opened: false, Message: "The Highbay browser window could not be opened. " +
			"Ask your coding agent to try opening Highbay views again, or choose Open Highbay Window in the Herdr companion."}, nil
	}
	return &genhighbay.OpenViewsResult{Opened: true, Message: "Opened Highbay views in the user's default browser. " +
		"Tell the user clearly that the Highbay browser page has been opened alongside this chat (it may open in a new tab or background window), " +
		"and that they can reopen it anytime with Open Highbay Window in the Herdr companion (Cmd+Alt+Shift+H on macOS) or by asking you to reopen Highbay views."}, nil
}
