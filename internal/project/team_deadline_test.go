// This file verifies explicitly accepted team launch lifetime and deadline.
package project

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// deadlineTeamHost advances only the test's virtual clock at the first host read.
	deadlineTeamHost struct {
		TeamHost
		beforeRead func(context.Context)
		readError  error
	}
	// teamDeadlineContextKey checks call values without exposing a public input.
	teamDeadlineContextKey struct{}
)

func TestTeamStartDeadlineBoundary(t *testing.T) {
	for _, elapsed := range []time.Duration{teamStartTimeout - time.Nanosecond, teamStartTimeout, teamStartTimeout + time.Nanosecond} {
		t.Run(elapsed.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				state := testState(t)
				deps := fixtureTeamDependencies(state)
				host := &deadlineTeamHost{TeamHost: deps.Host}
				deps.Host = host
				server, err := NewServer(state, func() {}, deps)
				require.NoError(t, err)
				fixtureAssignments(server, false)
				require.NoError(t, server.RegisterBuildRoles(t.Context(), controlplane.BuilderOnly{BuilderFolder: state.Record.Directory}))
				var accepted context.Context
				server.team.withTimeout = func(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
					assert.Equal(t, teamStartTimeout, limit)
					assert.NoError(t, ctx.Err())
					assert.Equal(t, "kept", ctx.Value(teamDeadlineContextKey{}))
					var cancel context.CancelFunc
					accepted, cancel = context.WithTimeout(ctx, limit)
					return accepted, cancel
				}
				host.beforeRead = func(ctx context.Context) {
					time.Sleep(elapsed)
					synctest.Wait()
					assert.Equal(t, "kept", ctx.Value(teamDeadlineContextKey{}))
				}
				caller, cancel := context.WithCancel(context.WithValue(teamContext(state), teamDeadlineContextKey{}, "kept"))
				cancel()
				_, err = server.StartAgents(caller, nil)
				if elapsed < teamStartTimeout {
					assert.NoError(t, err)
					assert.NoError(t, host.readError)
					assert.Len(t, host.TeamHost.(*teamFixture).choices, 1)
					assert.ErrorIs(t, accepted.Err(), context.Canceled)
					return
				}
				var unavailable interface{ GoaErrorName() string }
				require.ErrorAs(t, err, &unavailable)
				assert.Equal(t, "team_unavailable", unavailable.GoaErrorName())
				assert.ErrorIs(t, host.readError, context.DeadlineExceeded)
				assert.ErrorIs(t, accepted.Err(), context.DeadlineExceeded)
				assert.Empty(t, host.TeamHost.(*teamFixture).calls)
			})
		})
	}
}

func TestTeamStartServerLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := testState(t)
		deps := fixtureTeamDependencies(state)
		lifetime, stop := context.WithCancel(t.Context())
		defer stop()
		deps.Lifetime = lifetime
		host := &deadlineTeamHost{TeamHost: deps.Host}
		deps.Host = host
		host.beforeRead = func(context.Context) {
			stop()
			synctest.Wait()
		}
		server, err := NewServer(state, func() {}, deps)
		require.NoError(t, err)
		fixtureAssignments(server, false)
		require.NoError(t, server.RegisterBuildRoles(t.Context(), controlplane.BuilderOnly{BuilderFolder: state.Record.Directory}))
		_, err = server.StartAgents(t.Context(), nil)
		assert.Error(t, err)
		assert.ErrorIs(t, host.readError, context.Canceled)
		assert.Empty(t, host.TeamHost.(*teamFixture).calls)
	})
}

func (h *deadlineTeamHost) Spaces(ctx context.Context) ([]TeamSpace, error) {
	if beforeRead := h.beforeRead; beforeRead != nil {
		h.beforeRead = nil
		beforeRead(ctx)
	}
	h.readError = ctx.Err()
	if h.readError != nil {
		return nil, h.readError
	}
	return h.TeamHost.Spaces(ctx)
}
