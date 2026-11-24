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
// worker's open call: once the server's lifetime ends, the wait returns the
// unchanged list without its bounded wait or another read.
func TestAssignmentWaitEndsWhenTheServerStops(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	agreed := confirmAgreedTerms(t, cp, "chg")
	_, err := cp.StartBuild(agreedUser, &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: agreed.RequestRevision})
	require.NoError(t, err)
	makeBuildFixtureBuilderCopy(t, cp)
	cp.assignmentWait, cp.assignmentPoll = time.Minute, time.Minute
	first, err := cp.WaitForBuilderAssignment(agreedBuilder, &gensurla.WaitForBuilderAssignmentPayload{})
	require.NoError(t, err)
	done := make(chan *gensurla.BuilderAssignmentWait, 1)
	go func() {
		got, err := cp.WaitForBuilderAssignment(agreedBuilder, &gensurla.WaitForBuilderAssignmentPayload{SinceVersion: &first.Version})
		if err == nil {
			done <- got
		}
	}()
	cp.cancelServer()
	select {
	case got := <-done:
		assert.Equal(t, first.Version, got.Version)
	case <-time.After(10 * time.Second):
		t.Fatal("the wait outlived the server")
	}
}
