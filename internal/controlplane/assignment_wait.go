// This file holds the workers' waiting reads of their assignment lists. A
// builder waiting for the tests to be accepted, or a test writer waiting for
// Highbay's verdict, keeps one such read open in place of repeated list calls;
// the reads depend on no Chorus reminder.
package controlplane

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
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
	waitForBuilderAssignmentSpan    = "highbay.controlplane.wait_for_builder_assignment"
	waitForTestWriterAssignmentSpan = "highbay.controlplane.wait_for_test_writer_assignment"
)

// WaitForBuilderAssignment returns the builder's assignment list with the
// version that names it: at once when since_version is absent or differs
// from it, otherwise when the list changes, the bounded wait ends or the
// server stops. It records nothing and marks no assignment read.
func (c *ControlPlane) WaitForBuilderAssignment(
	ctx context.Context, p *genhighbay.WaitForBuilderAssignmentPayload,
) (*genhighbay.BuilderAssignmentWait, error) {
	ctx, span := c.tracer("highbay/controlplane").Start(ctx, waitForBuilderAssignmentSpan)
	defer span.End()
	list, version, changed, err := waitForAssignments(ctx, c, p.SinceVersion, c.ListBuilderAssignments, genhighbay.EncodeBuilderAssignmentList)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.Bool("highbay.changed", changed))
	return &genhighbay.BuilderAssignmentWait{Assignments: list.Assignments, Version: version}, nil
}

// WaitForTestWriterAssignment is WaitForBuilderAssignment for the test
// writer's assignment list.
func (c *ControlPlane) WaitForTestWriterAssignment(
	ctx context.Context, p *genhighbay.WaitForTestWriterAssignmentPayload,
) (*genhighbay.TestWriterAssignmentWait, error) {
	ctx, span := c.tracer("highbay/controlplane").Start(ctx, waitForTestWriterAssignmentSpan)
	defer span.End()
	list, version, changed, err := waitForAssignments(ctx, c, p.SinceVersion, c.ListTestWriterAssignments, genhighbay.EncodeTestWriterAssignmentList)
	if err != nil {
		return nil, err
	}
	span.SetAttributes(attribute.Bool("highbay.changed", changed))
	return &genhighbay.TestWriterAssignmentWait{Assignments: list.Assignments, Version: version}, nil
}

// waitForAssignments reads a role's assignment list until its version, the
// SHA-256 of the list's generated encoding, differs from since. It returns
// the newest list with changed false when the bounded wait ends or the
// server begins to stop first, so neither a stopping server nor a slow read
// holds a worker's call, and the context's error when the caller went away.
// A nil or empty since never waits for a change.
//
// Each read runs beside the wait, not inside it: a read can wait for the
// workspace or for Git, and the bounded wait covers that time too. A read
// still running when the wait ends is abandoned with its context canceled.
// Without any list to return, the wait fails.
func waitForAssignments[T any](
	ctx context.Context, c *ControlPlane, since *string,
	read func(context.Context) (T, error), encode func(T) ([]byte, error),
) (list T, version string, changed bool, err error) {
	type result struct {
		// list and err are one read's outcome.
		list T
		err  error
	}
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	start := func() <-chan result {
		done := make(chan result, 1)
		go func() {
			list, err := read(readCtx)
			done <- result{list, err}
		}()
		return done
	}
	deadline := time.NewTimer(cmp.Or(c.assignmentWait, defaultAssignmentWait))
	defer deadline.Stop()
	ticker := time.NewTicker(cmp.Or(c.assignmentPoll, defaultAssignmentPoll))
	defer ticker.Stop()
	var (
		have    bool
		tick    <-chan time.Time
		pending = start()
	)
	// unchanged ends the wait with the newest list read, or fails when no
	// read has finished yet.
	unchanged := func(why string) (T, string, bool, error) {
		if !have {
			var none T
			return none, "", false, errors.New("the assignment list could not be read: " + why + "; call again")
		}
		return list, version, false, nil
	}
	for {
		select {
		case got := <-pending:
			if got.err != nil {
				return list, "", false, got.err
			}
			raw, err := encode(got.list)
			if err != nil {
				return list, "", false, fmt.Errorf("encode assignment list version: %w", err)
			}
			sum := sha256.Sum256(raw)
			list, version, have = got.list, hex.EncodeToString(sum[:]), true
			if since == nil || *since == "" || *since != version {
				return list, version, true, nil
			}
			pending, tick = nil, ticker.C
		case <-tick:
			pending, tick = start(), nil
		case <-ctx.Done():
			// The worker's client ended the call; nobody reads a result.
			return list, "", false, ctx.Err()
		case <-c.stopping:
			return unchanged("the server is stopping")
		case <-c.serverCtx.Done():
			return unchanged("the server is stopping")
		case <-deadline.C:
			return unchanged("the read did not finish in time")
		}
	}
}
