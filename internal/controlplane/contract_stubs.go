// This file keeps the builder-copy integration's new operations unavailable
// until their owning implementation slices replace these methods.
package controlplane

import (
	"context"
	"errors"

	gensurla "github.com/goadesign/surla/gen/surla"
)

// UpdateDraft refuses without changing the draft until the draft owner is ready.
func (c *ControlPlane) UpdateDraft(ctx context.Context) (*gensurla.DraftUpdateResult, error) {
	return nil, gensurla.MakeNotImplemented(errors.New(
		"not available until the builder-copy change is complete",
	))
}
