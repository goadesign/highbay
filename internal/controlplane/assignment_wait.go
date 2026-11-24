// This file holds the workers' waiting reads of their assignment lists. A
// builder waiting for the tests to be accepted, or a test writer waiting for
// Surla's verdict, keeps one such read open in place of repeated list calls;
// the reads depend on no Chorus reminder.
package controlplane

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"go.opentelemetry.io/otel/attribute"
)

const (
	// defaultAssignmentWait bounds one waiting assignment read when the list
	// does not change. It stays under the tool-call limits of the coding
	// clients, which end a call that runs for about a minute.
	defaultAssignmentWait = 30 * time.Second
	// defaultAssignmentPoll is how often a waiting call reads the list again.
	// A worker learns of an accepted report or a dropped build within it.
	defaultAssignmentPoll = time.Second
	// waitForBuilderAssignmentSpan and waitForTestWriterAssignmentSpan name
	// the waiting reads' spans, which the diagnostics history never saves.
	waitForBuilderAssignmentSpan    = "surla.controlplane.wait_for_builder_assignment"
	waitForTestWriterAssignmentSpan = "surla.controlplane.wait_for_test_writer_assignment"
)

// WaitForBuilderAssignment returns the builder's assignment list with the
// version that names it: at once when since_version is absent or differs
// from it, otherwise when the list changes, the bounded wait ends or the
// server stops. It records nothing and marks no assignment read.
func (c *ControlPlane) WaitForBuilderAssignment(
	ctx context.Context, p *gensurla.WaitForBuilderAssignmentPayload,
) (*gensurla.BuilderAssignmentWait, error) {
	ctx, span := c.tracer("surla/controlplane").Start(ctx, waitForBuilderAssignmentSpan)
	defer span.End()
	list, version, changed, err := waitForAssignments(ctx, c, p.SinceVersion, c.ListBuilderAssignments, gensurla.EncodeBuilderAssignmentList)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.Bool("surla.changed", changed))
	return &gensurla.BuilderAssignmentWait{Assignments: list.Assignments, Version: version}, nil
}

// WaitForTestWriterAssignment is WaitForBuilderAssignment for the test
// writer's assignment list.
func (c *ControlPlane) WaitForTestWriterAssignment(
	ctx context.Context, p *gensurla.WaitForTestWriterAssignmentPayload,
) (*gensurla.TestWriterAssignmentWait, error) {
	ctx, span := c.tracer("surla/controlplane").Start(ctx, waitForTestWriterAssignmentSpan)
	defer span.End()
	list, version, changed, err := waitForAssignments(ctx, c, p.SinceVersion, c.ListTestWriterAssignments, gensurla.EncodeTestWriterAssignmentList)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.Bool("surla.changed", changed))
	return &gensurla.TestWriterAssignmentWait{Assignments: list.Assignments, Version: version}, nil
}

// waitForAssignments reads a role's assignment list until its version, the
// SHA-256 of the list's generated encoding, differs from since. It returns
// the newest list with changed false when the bounded wait ends or the
// server stops first, so a stopping server never holds a worker's call, and
// the context's error when the caller went away. A nil or empty since never
// waits.
func waitForAssignments[T any](
	ctx context.Context, c *ControlPlane, since *string,
	read func(context.Context) (T, error), encode func(T) ([]byte, error),
) (list T, version string, changed bool, err error) {
	deadline := time.NewTimer(cmp.Or(c.assignmentWait, defaultAssignmentWait))
	defer deadline.Stop()
	ticker := time.NewTicker(cmp.Or(c.assignmentPoll, defaultAssignmentPoll))
	defer ticker.Stop()
	for {
		if list, err = read(ctx); err != nil {
			return list, "", false, err
		}
		raw, err := encode(list)
		if err != nil {
			return list, "", false, fmt.Errorf("encode assignment list version: %w", err)
		}
		sum := sha256.Sum256(raw)
		version = hex.EncodeToString(sum[:])
		if since == nil || *since == "" || *since != version {
			return list, version, true, nil
		}
		select {
		case <-ctx.Done():
			// The worker's client ended the call; nobody reads a result.
			return list, "", false, ctx.Err()
		case <-c.serverCtx.Done():
			return list, version, false, nil
		case <-deadline.C:
			return list, version, false, nil
		case <-ticker.C:
		}
	}
}
