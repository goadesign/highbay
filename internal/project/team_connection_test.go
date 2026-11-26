// This file tests team connection acceptance and endpoint-specific observations.
package project

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTeamConnectionFailureBeforeEffects preserves pure reads and retries the
// same prepared build once Chorus becomes healthy.
func TestTeamConnectionFailureBeforeEffects(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	host := deps.Host.(*teamFixture)
	fail, calls := true, 0
	deps.Connect = func(context.Context) (TeamConnection, error) {
		calls++
		if fail {
			return TeamConnection{}, assert.AnError
		}
		return TeamConnection{Endpoint: "http://chorus.test:9550"}, nil
	}
	server, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	fixtureAssignments(server, false)
	require.NoError(t, server.RegisterBuildRoles(t.Context(), controlplane.BuilderOnly{BuilderFolder: state.Record.Directory}))
	_, err = server.EnsureTeam(teamContext(state))
	require.NoError(t, err)
	assert.Zero(t, calls)
	before, err := json.Marshal(state.Record)
	require.NoError(t, err)
	var failure *genproject.TeamUnavailable
	_, err = server.StartAgents(t.Context(), nil)
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, "chorus", failure.Cause)
	assert.Equal(t, 1, calls)
	assert.Empty(t, host.calls)
	assert.NoDirExists(t, deps.RosterDirectory)
	after, err := json.Marshal(state.Record)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Empty(t, server.teamSelection().connection.Endpoint)
	fail = false
	_, err = server.StartAgents(t.Context(), nil)
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Equal(t, "http://chorus.test:9550", server.teamSelection().connection.Endpoint)
	roster, err := os.ReadFile(deps.RosterDirectory + "/" + state.Record.Space + ".json")
	require.NoError(t, err)
	assert.NotEmpty(t, roster)
}

// TestTeamEndpointFacts gates reminders on the new server's facts without
// forgetting which assignment generations the supervisor already attempted.
func TestTeamEndpointFacts(t *testing.T) {
	o, old, now := newSupervisionFixture(t)
	listed(o, now.Add(-teamJoinSettle), true, "builder-example")
	o.roles["builder"].owed["already-tried"] = true
	o.publish(t.Context(), now, false)
	require.False(t, o.syncSelection(o.server.teamSelection()), "same endpoint preserves facts")
	next := &supervisionFixture{terminal: old.terminal}
	o.server.team.observationMu.Lock()
	o.server.team.connection = TeamConnection{Endpoint: "http://chorus.test:9550", Observer: next, Sender: next}
	o.server.team.observationMu.Unlock()
	snapshot, err := o.server.ReadTeam(t.Context())
	require.NoError(t, err)
	require.Len(t, snapshot.Roles, 1)
	assert.Equal(t, "unknown", snapshot.Roles[0].Watcher, "old endpoint facts are not published as current")
	require.True(t, o.syncSelection(o.server.teamSelection()), "only the endpoint changed")
	assert.False(t, o.roles["builder"].watchKnown)
	assert.True(t, o.roles["builder"].listedSince.IsZero())
	assert.True(t, o.roles["builder"].owed["already-tried"])
	o.remind(t.Context(), now)
	assert.Empty(t, old.sends)
	assert.Empty(t, next.sends)
	listed(o, now, true, "builder-example")
	o.remind(t.Context(), now.Add(teamJoinSettle-time.Nanosecond))
	assert.Empty(t, next.sends)
	o.remind(t.Context(), now.Add(teamJoinSettle))
	assert.Empty(t, old.sends)
	require.Len(t, next.sends, 1)
}

// TestTeamEndpointReadersDrain runs the real supervisor's reader lifecycle.
// Two local goroutines per endpoint and a bounded owner tick are needed to
// prove cancellation/join ordering and a fresh ETag; no outside process starts.
func TestTeamEndpointReadersDrain(t *testing.T) {
	o, old, _ := newSupervisionFixture(t)
	oldStarted, oldJoined := make(chan struct{}), make(chan struct{})
	oldVersion := make(chan string, 1)
	old.follow = func(ctx context.Context, _ TeamSelection, emit func(TeamEvent)) error {
		close(oldStarted)
		<-ctx.Done()
		// A final buffered event must be drained before the new reader starts.
		emit(TeamEvent{Kind: "snapshot", Role: "builder", Sequence: 999})
		close(oldJoined)
		return ctx.Err()
	}
	watchJoined := make(chan struct{})
	old.watch = func(ctx context.Context, version string) (TeamWatcherView, error) {
		if version == "" {
			return TeamWatcherView{Version: "old-etag", Held: map[string]bool{"builder-example": true}}, nil
		}
		oldVersion <- version
		<-ctx.Done()
		close(watchJoined)
		return TeamWatcherView{}, ctx.Err()
	}
	next := &supervisionFixture{terminal: old.terminal}
	nextStarted := make(chan bool, 1)
	nextVersion := make(chan string, 1)
	next.watch = func(ctx context.Context, version string) (TeamWatcherView, error) {
		nextVersion <- version
		<-ctx.Done()
		return TeamWatcherView{}, ctx.Err()
	}
	next.follow = func(ctx context.Context, _ TeamSelection, emit func(TeamEvent)) error {
		joined := false
		select {
		case <-oldJoined:
			select {
			case <-watchJoined:
				joined = true
			default:
			}
		default:
		}
		nextStarted <- joined
		emit(TeamEvent{Kind: "snapshot", Role: "builder", Sequence: 1, Terminals: []TeamTerminal{next.terminal}})
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- o.server.Supervise(ctx, func(context.Context) (controlplane.TeamAssignments, error) {
			return controlplane.TeamAssignments{}, nil
		})
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("supervisor cleanup did not drain")
		}
	})
	select {
	case <-oldStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("old reader did not start")
	}
	select {
	case version := <-oldVersion:
		assert.Equal(t, "old-etag", version)
	case <-time.After(3 * time.Second):
		t.Fatal("old watcher did not continue with its ETag")
	}
	o.server.team.observationMu.Lock()
	o.server.team.connection = TeamConnection{Endpoint: "http://chorus.test:9550", Observer: next, Sender: next}
	o.server.team.observationMu.Unlock()
	select {
	case joined := <-nextStarted:
		assert.True(t, joined, "both old readers join before a new one starts")
	case <-time.After(3 * time.Second):
		t.Fatal("new reader did not start")
	}
	select {
	case version := <-nextVersion:
		assert.Empty(t, version, "a new endpoint must not receive the old endpoint's ETag")
	case <-time.After(3 * time.Second):
		t.Fatal("new watcher did not start")
	}
	assert.Eventually(t, func() bool {
		view, err := o.server.ReadTeam(t.Context())
		return err == nil && len(view.Roles) == 1 && view.Roles[0].State == "waiting"
	}, 3*time.Second, 10*time.Millisecond, "the final old snapshot must not override the new sequence")
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("new readers did not drain on shutdown")
	}
}
