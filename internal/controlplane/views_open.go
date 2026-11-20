// This file delegates browser handoff to the project credential owner.
package controlplane

import (
	"context"
	"errors"

	gensurla "github.com/goadesign/surla/gen/surla"
	"go.opentelemetry.io/otel/codes"
)

// OpenViews opens a new user session without exposing its credential or URL.
func (c *ControlPlane) OpenViews(ctx context.Context) (*gensurla.OpenViewsResult, error) {
	if _, err := c.ownedTeam(ctx); err != nil {
		return nil, err
	}
	if c.openViews == nil {
		return &gensurla.OpenViewsResult{Opened: false, Message: "This Surla server has no browser opener configured. " +
			"The host must enable its browser-opening integration, or open Surla with Open Surla Window in the Herdr Surla panel."}, nil
	}
	ctx, span := c.tracer("surla/controlplane").Start(ctx, "surla.views.open")
	defer span.End()
	if err := c.openViews(ctx); err != nil {
		// An opener failure may contain a private browser link. Record only safe owned text.
		safe := errors.New("the project views could not be opened")
		span.RecordError(safe)
		span.SetStatus(codes.Error, "open project views")
		return &gensurla.OpenViewsResult{Opened: false, Message: "The Surla browser window could not be opened. " +
			"Ask your coding agent to try opening Surla views again, or choose Open Surla Window in the Herdr Surla panel."}, nil
	}
	return &gensurla.OpenViewsResult{Opened: true, Message: "Opened the Surla window in the user's browser and brought it to the front. " +
		"Tell the user clearly that the Surla browser window is now open alongside this chat, and that they can bring it back anytime " +
		"with Open Surla Window in the Herdr Surla panel (Cmd+Alt+Shift+S) or by asking you to reopen it."}, nil
}
