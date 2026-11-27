// These tests run the supervisor's loops against the stand-ins, on a virtual
// clock, to cover what they keep current and how they recover.
package project

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	genchorus "github.com/goadesign/chorus/gen/view"
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assignmentSource is an assignment reader a test can change while the
// supervisor runs.
type assignmentSource struct {
	mu          sync.Mutex
	assignments controlplane.TeamAssignments
	err         error
}

// read is the controlplane.AssignmentReader.
func (a *assignmentSource) read(context.Context) (controlplane.TeamAssignments, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.assignments, a.err
}

// set replaces what the reader returns.
func (a *assignmentSource) set(assignments controlplane.TeamAssignments, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.assignments, a.err = assignments, err
}

// supervise runs the supervisor until the test ends and checks that it then
// returns nil. It must be called inside a synctest bubble.
func supervise(t *testing.T, server *Server, source *assignmentSource) {
	t.Helper()
	server.BindAssignments(source.read)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- server.Supervise(ctx, source.read) }()
	t.Cleanup(func() {
		cancel()
		assert.NoError(t, <-done)
	})
	synctest.Wait()
}

// roleOf reads the team and returns one role's state.
func roleOf(t *testing.T, server *Server, role string) *genhighbay.TeamRoleState {
	t.Helper()
	team, err := server.ReadTeam(t.Context())
	require.NoError(t, err)
	for _, state := range team.Roles {
		if state.Role == role {
			return state
		}
	}
	require.FailNow(t, "role is not registered", role)
	return nil
}

// TestSuperviseFollowsTerminals checks that a role's state follows its
// terminal, turns unknown while herdr-core is unreachable, and recovers from
// a fresh snapshot once the connection is back.
func TestSuperviseFollowsTerminals(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		source := &assignmentSource{assignments: controlplane.TeamAssignments{
			BuildIdentity: &controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(10, 0)}, BuilderHasWork: true,
		}}
		supervise(t, server, source)
		assert.Equal(t, "not_started", roleOf(t, server, "builder").State)

		_, err := server.StartAgents(userContext(server.state), nil)
		require.NoError(t, err)
		synctest.Wait()
		assert.Equal(t, "starting", roleOf(t, server, "builder").State)
		assert.True(t, roleOf(t, server, "builder").HasTerminal)
		assert.Equal(t, "nothing_owed", roleOf(t, server, "builder").Assignment)

		terminal, found := fakes.core.record("t1")
		require.True(t, found)
		terminal.Reported, terminal.Status = true, &hc.Status{State: "blocked", Kind: "permission"}
		fakes.core.change(terminal, hc.StatusChanged{Status: terminal.Status, Reported: true})
		synctest.Wait()
		assert.Equal(t, "needs_you", roleOf(t, server, "builder").State)
		assert.Equal(t, "permission", roleOf(t, server, "builder").Attention)

		fakes.core.drop()
		synctest.Wait()
		assert.Equal(t, "unknown", roleOf(t, server, "builder").State, "nothing is known while herdr-core is unreachable")
		time.Sleep(teamRetryDelay)
		synctest.Wait()
		assert.Equal(t, 2, fakes.core.dials, "the connection is dialed again")
		assert.Equal(t, "needs_you", roleOf(t, server, "builder").State, "the fresh snapshot restores the state")
	})
}

// TestSuperviseFollowsChorus checks the Chorus status and the conversation as
// the swarm changes, Chorus fails and comes back.
func TestSuperviseFollowsChorus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, fakes := teamServer(t)
		supervise(t, server, &assignmentSource{})
		team, err := server.ReadTeam(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "connected", team.ChorusStatus)

		_, err = server.SendMessage(userContext(server.state), &genhighbay.SendTeamMessagePayload{To: "team", Text: "Hello"})
		require.NoError(t, err)
		time.Sleep(time.Second)
		synctest.Wait()
		conversation, err := server.ReadConversation(t.Context())
		require.NoError(t, err)
		require.Len(t, conversation.Messages, 1, "the posted message is read back from Chorus")
		assert.Equal(t, "Hello", conversation.Messages[0].Text)

		fakes.chorus.fail(errors.New("connection refused"))
		time.Sleep(time.Second)
		synctest.Wait()
		conversation, err = server.ReadConversation(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "unavailable", conversation.ChorusStatus)
		assert.Len(t, conversation.Messages, 1, "the last conversation read stays readable")

		fakes.chorus.fail(nil)
		time.Sleep(teamRetryDelay)
		synctest.Wait()
		team, err = server.ReadTeam(t.Context())
		require.NoError(t, err)
		assert.Equal(t, "connected", team.ChorusStatus)
		assert.GreaterOrEqual(t, fakes.chorus.connects, 2, "Chorus is resolved again after a failed read")
	})
}

// TestSuperviseNotifiesUnreadAssignments checks the one message Highbay
// posts for each assignment a worker has not read: only once the worker has
// joined the channel, once per generation, and again after a failed post.
func TestSuperviseNotifiesUnreadAssignments(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		record, _ := server.currentTeam()
		builder, channel := teamName("builder", record.ID), "#"+record.Team
		owed := func(generation string) controlplane.TeamAssignments {
			return controlplane.TeamAssignments{
				BuildIdentity:  &controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(10, 0)},
				BuilderHasWork: true, Unread: []controlplane.AssignmentGeneration{{Role: "builder", Generation: generation}},
			}
		}
		source := &assignmentSource{assignments: owed("g1")}
		supervise(t, server, source)
		assert.Equal(t, "unread", roleOf(t, server, "builder").Assignment)
		time.Sleep(2 * teamAssignmentInterval)
		synctest.Wait()
		assert.Empty(t, fakes.chorus.posted(), "a worker that has not joined the channel is not told yet")

		fakes.chorus.set(func(view *genchorus.SwarmView) {
			view.Agents = []*genchorus.Agent{{Handle: builder, Channels: []string{channel}}}
		})
		time.Sleep(time.Second)
		synctest.Wait()
		posts := fakes.chorus.posted()
		require.Len(t, posts, 1)
		title := "Highbay"
		assert.Equal(t, &genchorus.PostMessagePayload{
			Swarm: record.Team, Sender: teamName("highbay", record.ID), Kind: "service", Role: &title, To: channel,
			Text: "@" + builder + " Highbay: you have an unread assignment. Use list_builder_assignments and get_builder_assignment to read it. Highbay reads no replies.",
		}, posts[0])

		time.Sleep(2 * teamAssignmentInterval)
		synctest.Wait()
		assert.Len(t, fakes.chorus.posted(), 1, "one notice per assignment generation")

		fakes.chorus.fail(errors.New("connection refused"))
		source.set(owed("g2"), nil)
		time.Sleep(teamAssignmentInterval)
		synctest.Wait()
		assert.Len(t, fakes.chorus.posted(), 1, "the failed post is not counted as told")
		fakes.chorus.fail(nil)
		time.Sleep(teamAssignmentInterval + teamRetryDelay)
		synctest.Wait()
		assert.Len(t, fakes.chorus.posted(), 2, "the new generation is told once Chorus is back")

		source.set(controlplane.TeamAssignments{}, errors.New("store locked"))
		time.Sleep(teamAssignmentInterval)
		synctest.Wait()
		assert.Equal(t, "unknown", roleOf(t, server, "builder").Assignment, "a failed read is unknown, not no work")
	})
}

// TestSuperviseStopsWorkersOfAFinishedBuild checks that the workers of a
// merged build are ended without anyone asking.
func TestSuperviseStopsWorkersOfAFinishedBuild(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		build := controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(10, 0)}
		source := &assignmentSource{assignments: controlplane.TeamAssignments{BuildIdentity: &build, BuilderHasWork: true, TestWriterHasWork: true}}
		supervise(t, server, source)
		_, err := server.StartAgents(userContext(server.state), nil)
		require.NoError(t, err)
		synctest.Wait()
		require.Len(t, fakes.core.runs, 2)

		source.set(controlplane.TeamAssignments{BuildIdentity: &build, Merged: true}, nil)
		time.Sleep(teamAssignmentInterval)
		synctest.Wait()
		assert.Len(t, fakes.core.kills, 2)
		assert.Equal(t, "not_started", roleOf(t, server, "builder").State, "the removed terminal leaves nothing behind")
	})
}

// TestSuperviseEndsWithTheServer checks that the supervisor returns when the
// server's lifetime ends, even though its caller's context lives on.
func TestSuperviseEndsWithTheServer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		state := testState(t)
		lifetime, stop := context.WithCancel(t.Context())
		_, dependencies := fixtureTeam(lifetime)
		server, err := NewServer(state, func() {}, dependencies)
		require.NoError(t, err)
		done := make(chan error, 1)
		go func() { done <- server.Supervise(t.Context(), (&assignmentSource{}).read) }()
		synctest.Wait()
		stop()
		assert.NoError(t, <-done)
	})
}
