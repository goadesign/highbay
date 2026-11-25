// This file verifies the workers' waiting reads of their assignment lists:
// when they return, what they return, and that the history does not save them.
package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gensurla "github.com/goadesign/surla/gen/surla"
)

// TestAssignmentWaitReturnsTheListAndItsVersion follows both roles' waits
// through an open build: the first call returns the list at once with the
// hash of its encoding, an unchanged list ends the bounded wait with the same
// version, and dropping the build ends an open wait with the empty list.
func TestAssignmentWaitReturnsTheListAndItsVersion(t *testing.T) {
	type wait struct {
		// assignments is how many assignments the wait returned.
		assignments int
		// version names the returned list.
		version string
	}
	for _, role := range []struct {
		// name is the worker role.
		name string
		// wait calls the role's waiting read as that role.
		wait func(cp *ControlPlane, ctx context.Context, since *string) (wait, error)
		// listVersion hashes the role's plain list read.
		listVersion func(t *testing.T, cp *ControlPlane) string
	}{
		{
			name: "builder",
			wait: func(cp *ControlPlane, ctx context.Context, since *string) (wait, error) {
				got, err := cp.WaitForBuilderAssignment(WithPrincipal(ctx, Principal{ID: agreedOwner, CallerID: "builder", Kind: CallerBuilder}),
					&gensurla.WaitForBuilderAssignmentPayload{SinceVersion: since})
				if err != nil {
					return wait{}, err
				}
				return wait{len(got.Assignments), got.Version}, nil
			},
			listVersion: func(t *testing.T, cp *ControlPlane) string {
				list, err := cp.ListBuilderAssignments(agreedBuilder)
				require.NoError(t, err)
				raw, err := gensurla.EncodeBuilderAssignmentList(list)
				require.NoError(t, err)
				sum := sha256.Sum256(raw)
				return hex.EncodeToString(sum[:])
			},
		},
		{
			name: "test writer",
			wait: func(cp *ControlPlane, ctx context.Context, since *string) (wait, error) {
				got, err := cp.WaitForTestWriterAssignment(WithPrincipal(ctx, Principal{ID: agreedOwner, CallerID: "tests", Kind: CallerTestWriter}),
					&gensurla.WaitForTestWriterAssignmentPayload{SinceVersion: since})
				if err != nil {
					return wait{}, err
				}
				return wait{len(got.Assignments), got.Version}, nil
			},
			listVersion: func(t *testing.T, cp *ControlPlane) string {
				list, err := cp.ListTestWriterAssignments(agreedTestWriter)
				require.NoError(t, err)
				raw, err := gensurla.EncodeTestWriterAssignmentList(list)
				require.NoError(t, err)
				sum := sha256.Sum256(raw)
				return hex.EncodeToString(sum[:])
			},
		},
	} {
		t.Run(role.name, func(t *testing.T) {
			cp := newBuildControlPlane(t, Options{})
			agreed := confirmAgreedTerms(t, cp, "chg")
			_, err := cp.StartBuild(agreedUser, &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: agreed.RequestRevision})
			require.NoError(t, err)
			makeBuildFixtureBuilderCopy(t, cp)
			started := mustStartedBuild(t, cp, agreedUser, "chg").StartedAt
			cp.assignmentPoll = 5 * time.Millisecond

			// No since_version, and an empty one, return at once.
			cp.assignmentWait = time.Minute
			first, err := role.wait(cp, t.Context(), nil)
			require.NoError(t, err)
			assert.Equal(t, 1, first.assignments)
			assert.Equal(t, role.listVersion(t, cp), first.version)
			empty := ""
			again, err := role.wait(cp, t.Context(), &empty)
			require.NoError(t, err)
			assert.Equal(t, first, again)
			stale := "an earlier version"
			again, err = role.wait(cp, t.Context(), &stale)
			require.NoError(t, err)
			assert.Equal(t, first, again)

			// An unchanged list ends the bounded wait with the same version.
			cp.assignmentWait = 40 * time.Millisecond
			begun := time.Now()
			unchanged, err := role.wait(cp, t.Context(), &first.version)
			require.NoError(t, err)
			assert.Equal(t, first, unchanged)
			assert.GreaterOrEqual(t, time.Since(begun), 40*time.Millisecond)

			// A caller that goes away ends its wait with its context's error.
			cp.assignmentWait = time.Minute
			gone, cancel := context.WithCancel(t.Context())
			cancel()
			_, err = role.wait(cp, gone, &first.version)
			require.ErrorIs(t, err, context.Canceled)

			// Dropping the build ends an open wait with the list it left.
			done := make(chan wait, 1)
			failed := make(chan error, 1)
			go func() {
				got, err := role.wait(cp, t.Context(), &first.version)
				if err != nil {
					failed <- err
					return
				}
				done <- got
			}()
			_, err = cp.DropBuild(agreedUser, &gensurla.DropBuildPayload{ChangeID: "chg", StartedAt: started})
			require.NoError(t, err)
			select {
			case got := <-done:
				assert.Zero(t, got.assignments)
				assert.NotEqual(t, first.version, got.version)
				assert.Equal(t, role.listVersion(t, cp), got.version)
			case err := <-failed:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the wait did not end when the build was dropped")
			}
		})
	}
}

// TestAssignmentWaitEndsWhenTheServerStops keeps surla stop from waiting on a
// worker's open call: when the host signals that the stop has begun, or the
// server's own lifetime ends, an open wait returns the unchanged list at
// once, without its bounded wait or another read.
func TestAssignmentWaitEndsWhenTheServerStops(t *testing.T) {
	for _, signal := range []string{"the host begins to stop", "the server's lifetime ends"} {
		t.Run(signal, func(t *testing.T) {
			stopping, stop := context.WithCancel(context.Background())
			defer stop()
			cp := newBuildControlPlane(t, Options{Stopping: stopping})
			agreed := confirmAgreedTerms(t, cp, "chg")
			_, err := cp.StartBuild(agreedUser, &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: agreed.RequestRevision})
			require.NoError(t, err)
			makeBuildFixtureBuilderCopy(t, cp)
			cp.assignmentWait, cp.assignmentPoll = time.Minute, time.Minute
			first, err := cp.WaitForBuilderAssignment(agreedBuilder, &gensurla.WaitForBuilderAssignmentPayload{})
			require.NoError(t, err)
			done := make(chan *gensurla.BuilderAssignmentWait, 1)
			failed := make(chan error, 1)
			go func() {
				got, err := cp.WaitForBuilderAssignment(agreedBuilder, &gensurla.WaitForBuilderAssignmentPayload{SinceVersion: &first.Version})
				if err != nil {
					failed <- err
					return
				}
				done <- got
			}()
			// The wait has read the list once and now waits out its minute.
			time.Sleep(200 * time.Millisecond)
			if signal == "the host begins to stop" {
				stop()
			} else {
				cp.cancelServer()
			}
			select {
			case got := <-done:
				assert.Equal(t, first.Version, got.Version)
				assert.Len(t, got.Assignments, 1)
			case err := <-failed:
				require.NoError(t, err)
			case <-time.After(10 * time.Second):
				t.Fatal("the wait outlived the stop")
			}
		})
	}
}

// TestAssignmentWaitBoundsASlowRead holds the workspace so the list read
// cannot finish, as a long operation on the workspace does: a wait that
// already has a list returns it unchanged when its bounded wait ends, and a
// wait whose first read never finishes fails then, instead of either
// waiting for the workspace.
func TestAssignmentWaitBoundsASlowRead(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	agreed := confirmAgreedTerms(t, cp, "chg")
	_, err := cp.StartBuild(agreedUser, &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: agreed.RequestRevision})
	require.NoError(t, err)
	makeBuildFixtureBuilderCopy(t, cp)
	cp.assignmentWait, cp.assignmentPoll = time.Minute, 5*time.Millisecond
	first, err := cp.WaitForBuilderAssignment(agreedBuilder, &gensurla.WaitForBuilderAssignmentPayload{})
	require.NoError(t, err)

	cp.assignmentWait = 400 * time.Millisecond
	done := make(chan *gensurla.BuilderAssignmentWait, 1)
	failed := make(chan error, 1)
	go func() {
		got, err := cp.WaitForBuilderAssignment(agreedBuilder, &gensurla.WaitForBuilderAssignmentPayload{SinceVersion: &first.Version})
		if err != nil {
			failed <- err
			return
		}
		done <- got
	}()
	time.Sleep(100 * time.Millisecond)
	cp.workspaceMu.Lock()
	held := true
	defer func() {
		if held {
			cp.workspaceMu.Unlock()
		}
	}()
	select {
	case got := <-done:
		assert.Equal(t, first.Version, got.Version, "the list read before the workspace was taken")
	case err := <-failed:
		require.NoError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("the wait waited for the workspace past its bound")
	}

	// A first read that cannot finish fails at the bound.
	cp.assignmentWait = 100 * time.Millisecond
	begun := time.Now()
	_, err = cp.WaitForBuilderAssignment(agreedBuilder, &gensurla.WaitForBuilderAssignmentPayload{})
	require.ErrorContains(t, err, "the read did not finish in time")
	assert.Less(t, time.Since(begun), 5*time.Second)
	cp.workspaceMu.Unlock()
	held = false
}

// TestTestWriterWaitEndsOnEachVerdict follows a test writer's wait through
// Surla's verdicts on its reports: findings keep the assignment in the list
// with its new state, and acceptance removes it, so the wait returns an empty
// list and the full assignment read says the tests were accepted.
func TestTestWriterWaitEndsOnEachVerdict(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	agreed := confirmAgreedTerms(t, cp, "chg")
	_, err := cp.StartBuild(agreedUser, &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: agreed.RequestRevision})
	require.NoError(t, err)
	cp.assignmentWait, cp.assignmentPoll = time.Minute, 5*time.Millisecond
	wait := func(since string, verdict map[string]any) *gensurla.TestWriterAssignmentWait {
		t.Helper()
		done := make(chan *gensurla.TestWriterAssignmentWait, 1)
		failed := make(chan error, 1)
		go func() {
			got, err := cp.WaitForTestWriterAssignment(agreedTestWriter, &gensurla.WaitForTestWriterAssignmentPayload{SinceVersion: &since})
			if err != nil {
				failed <- err
				return
			}
			done <- got
		}()
		editBuildFile(t, cp, "chg", savedTestWriter(verdict))
		select {
		case got := <-done:
			return got
		case err := <-failed:
			require.NoError(t, err)
			return nil
		case <-time.After(10 * time.Second):
			t.Fatal("the wait did not end on the verdict")
			return nil
		}
	}
	first, err := cp.WaitForTestWriterAssignment(agreedTestWriter, &gensurla.WaitForTestWriterAssignmentPayload{})
	require.NoError(t, err)
	require.Len(t, first.Assignments, 1)

	at := "2026-11-13T09:30:00Z"
	found := wait(first.Version, map[string]any{"state": "needs_changes", "findings": []string{"RefundOverLimit is not implemented"}, "checked_at": at})
	require.Len(t, found.Assignments, 1)
	needs, ok := found.Assignments[0].Progress.Tests.AsNeedsChanges()
	require.True(t, ok)
	assert.Equal(t, 1, needs.FindingCount)

	accepted := wait(found.Version, map[string]any{"state": "accepted", "findings": []string{}, "commit": "cccccccccccccccccccccccccccccccccccccccc", "checked_at": at})
	assert.Empty(t, accepted.Assignments, "accepted tests leave the list")
	assert.NotEqual(t, found.Version, accepted.Version)
	full, err := cp.GetTestWriterAssignment(agreedTestWriter, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "chg"})
	require.NoError(t, err)
	_, ok = full.Progress.Tests.AsAccepted()
	assert.True(t, ok, "the full read gives the verdict the empty list does not")
}
