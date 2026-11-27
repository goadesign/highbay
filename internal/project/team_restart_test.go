// These tests cover restarting one role, and the rules that keep Highbay
// from ending or relabeling a session it did not start, from mistaking a
// guide that is gone for one at work, and from reaching herdr-core after the
// server closed.
package project

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genchorus "github.com/goadesign/chorus/gen/view"
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/hc"
)

// guidePresentInChorus makes Chorus show the project's guide as last active
// at seen, without a watch.
func guidePresentInChorus(server *Server, seen time.Time) {
	record, roles := server.currentTeam()
	guide := server.guide(record, roles)
	server.team.observed.chorusRead(&genchorus.SwarmView{
		Agents: []*genchorus.Agent{{Handle: guide.Handle, LastSeenUnix: seen.Unix()}},
	}, true)
}

// observeCore copies herdr-core's terminals of this project into what the
// server has observed, as the supervisor's subscription does.
func observeCore(t *testing.T, server *Server, fakes *teamFakes) {
	t.Helper()
	terminals, err := fakes.core.List(t.Context(), hc.ListRequest{All: true, Labels: map[string]string{labelProject: server.state.Record.ID}})
	require.NoError(t, err)
	server.team.observed.applyTerminals(hc.Snapshot{Terminals: terminals}, server.state.Record.ID)
}

// TestAttachGuideFromItsOwnManagedTerminal follows the guide Highbay started:
// its first Highbay tool call attaches it from inside the terminal that
// already stands for the guide. That terminal keeps running and stays one
// Highbay started; an earlier guide terminal is replaced.
func TestAttachGuideFromItsOwnManagedTerminal(t *testing.T) {
	server, fakes := teamServer(t)
	user := userContext(server.state)
	_, err := server.SendMessage(user, &genhighbay.SendTeamMessagePayload{To: "guide", Text: "Hello"})
	require.NoError(t, err)
	started, found := fakes.core.record("t1")
	require.True(t, found, "sending the first message started the guide")
	require.Equal(t, "true", started.Labels[labelManaged])
	record, _ := server.currentTeam()
	fakes.core.add(hc.Terminal{Address: "old", State: hc.StateFinished, Labels: map[string]string{
		labelProject: record.ID, labelRole: "guide", labelManaged: "true",
	}})

	attached, err := server.AttachGuide(teamContext(server.state), &genproject.AttachGuidePayload{HcTerminalID: &started.Address})
	require.NoError(t, err)
	assert.True(t, attached.HasTerminal)
	kept, found := fakes.core.record("t1")
	require.True(t, found, "the guide's own terminal was removed")
	assert.Equal(t, hc.StateLive, kept.State)
	assert.Equal(t, "true", kept.Labels[labelManaged], "it is still a terminal Highbay started")
	assert.Equal(t, "guide", kept.Labels[labelRole])
	assert.NotContains(t, fakes.core.kills, "t1")
	_, found = fakes.core.record("old")
	assert.False(t, found, "the earlier guide terminal is gone")
}

// TestAttachGuideFromOutsideForgetsEndedTerminals checks that a guide that
// attaches from an editor is not shadowed by guide terminals whose program
// ended, while a running one is left alone.
func TestAttachGuideFromOutsideForgetsEndedTerminals(t *testing.T) {
	server, fakes := teamServer(t)
	record, _ := server.currentTeam()
	labels := func(managed bool) map[string]string {
		labels := map[string]string{labelProject: record.ID, labelRole: "guide"}
		if managed {
			labels[labelManaged] = "true"
		}
		return labels
	}
	fakes.core.add(hc.Terminal{Address: "ended-managed", State: hc.StateFinished, Labels: labels(true)})
	fakes.core.add(hc.Terminal{Address: "ended-attached", State: hc.StateFinished, Labels: labels(false)})
	fakes.core.add(hc.Terminal{Address: "running", State: hc.StateLive, Labels: labels(true)})

	attached, err := server.AttachGuide(teamContext(server.state), &genproject.AttachGuidePayload{})
	require.NoError(t, err)
	assert.False(t, attached.HasTerminal)
	_, found := fakes.core.record("ended-managed")
	assert.False(t, found, "an ended terminal Highbay started is removed")
	theirs, found := fakes.core.record("ended-attached")
	require.True(t, found, "the user's ended terminal is not removed")
	assert.NotContains(t, theirs.Labels, labelRole, "it only stops standing for the guide")
	running, found := fakes.core.record("running")
	require.True(t, found)
	assert.Equal(t, "guide", running.Labels[labelRole])
	assert.Empty(t, fakes.core.kills)
}

// TestStopLeavesAnAttachedSessionAsItIs checks that stopping a role whose
// terminal the user attached changes nothing about that terminal: not its
// program and not its labels.
func TestStopLeavesAnAttachedSessionAsItIs(t *testing.T) {
	server, fakes := teamServer(t)
	record, _ := server.currentTeam()
	labels := map[string]string{labelProject: record.ID, labelRole: "guide", "chorus.handle": "guide-x"}
	fakes.core.add(hc.Terminal{Address: "mine", State: hc.StateLive, Labels: labels})
	assert.Equal(t, "role_not_running", errorName(server.StopRole(userContext(server.state), "guide")))
	mine, found := fakes.core.record("mine")
	require.True(t, found)
	assert.Equal(t, labels, mine.Labels)
	assert.Empty(t, fakes.core.kills)
	assert.Empty(t, fakes.core.removes)
}

// TestEndedGuideIsNotAtWork follows a guide Highbay started after it is
// gone. Chorus keeps showing an agent as recently active for minutes, which
// must neither show the guide as working nor keep a message from starting a
// new one.
func TestEndedGuideIsNotAtWork(t *testing.T) {
	t.Run("its program exited", func(t *testing.T) {
		for _, test := range []struct {
			name string
			exit int32
			want string
		}{
			{name: "cleanly", exit: 0, want: "ended"},
			{name: "with a failure", exit: 2, want: "error"},
		} {
			t.Run(test.name, func(t *testing.T) {
				server, fakes := teamServer(t)
				user := userContext(server.state)
				_, err := server.SendMessage(user, &genhighbay.SendTeamMessagePayload{To: "guide", Text: "Hello"})
				require.NoError(t, err)
				guide, _ := fakes.core.record("t1")
				guide.State, guide.ExitCode = hc.StateFinished, &test.exit
				fakes.core.add(guide)
				observeCore(t, server, fakes)
				guidePresentInChorus(server, server.now())

				state := roleOf(t, server, "guide")
				assert.Equal(t, test.want, state.State, "Chorus still shows it as just active")
				assert.False(t, state.HasTerminal, "an ended terminal cannot be opened")

				_, err = server.SendMessage(user, &genhighbay.SendTeamMessagePayload{To: "guide", Text: "Are you there?"})
				require.NoError(t, err)
				_, restarted := fakes.core.record("t2")
				assert.True(t, restarted, "the next message starts a new guide")
			})
		}
	})

	t.Run("the user stopped it", func(t *testing.T) {
		server, fakes := teamServer(t)
		user := userContext(server.state)
		_, err := server.SendMessage(user, &genhighbay.SendTeamMessagePayload{To: "guide", Text: "Hello"})
		require.NoError(t, err)
		guidePresentInChorus(server, server.now())
		require.NoError(t, server.StopRole(user, "guide"))
		observeCore(t, server, fakes)

		assert.Equal(t, "not_started", roleOf(t, server, "guide").State, "its activity before the stop does not count")
		_, err = server.SendMessage(user, &genhighbay.SendTeamMessagePayload{To: "guide", Text: "Are you there?"})
		require.NoError(t, err)
		_, restarted := fakes.core.record("t2")
		assert.True(t, restarted, "the next message starts a new guide")

		// A guide that attaches from an editor afterwards counts again.
		require.NoError(t, server.StopRole(user, "guide"))
		observeCore(t, server, fakes)
		_, err = server.AttachGuide(teamContext(server.state), &genproject.AttachGuidePayload{})
		require.NoError(t, err)
		guidePresentInChorus(server, server.now())
		assert.Equal(t, "working", roleOf(t, server, "guide").State)
	})
}

// TestStartAgentsDoesNotDuplicateAnAttachedGuide checks that choosing a
// coding agent for the guide is refused while a guide session without a
// terminal is present in Chorus.
func TestStartAgentsDoesNotDuplicateAnAttachedGuide(t *testing.T) {
	server, fakes := teamServer(t)
	fixtureAssignments(server, false)
	_, err := server.AttachGuide(teamContext(server.state), &genproject.AttachGuidePayload{})
	require.NoError(t, err)
	guidePresentInChorus(server, server.now())
	_, err = server.StartAgents(userContext(server.state), map[string]*genhighbay.RoleAgentChoice{
		"guide": {Client: "claude", Program: "/bin/claude"},
	})
	assert.Equal(t, "unprocessable", errorName(err))
	assert.ErrorContains(t, err, "already running")
	assert.Empty(t, fakes.core.runs, "no second guide was started")
}

// TestRestartRole checks each outcome of restarting one role.
func TestRestartRole(t *testing.T) {
	t.Run("a running worker gets a new terminal and keeps its handle", func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		user := userContext(server.state)
		_, err := server.StartAgents(user, nil)
		require.NoError(t, err)
		before, _ := fakes.core.record("t1")
		_, roles := server.currentTeam()

		require.NoError(t, server.RestartRole(user, "builder", nil))
		assert.Equal(t, []string{"t1"}, fakes.core.kills)
		assert.Equal(t, []string{"t1"}, fakes.core.removes)
		after, found := fakes.core.record("t3")
		require.True(t, found, "the builder runs in a new terminal")
		assert.Equal(t, before.Labels, after.Labels, "same handle and labels")
		assert.Equal(t, before.Cwd, after.Cwd)
		_, rolesAfter := server.currentTeam()
		assert.Equal(t, roles, rolesAfter, "the role keeps its registration")
		other, found := fakes.core.record("t2")
		require.True(t, found, "the other worker is not touched")
		assert.Equal(t, hc.StateLive, other.State)
	})

	t.Run("with another coding agent, which is saved", func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		user := userContext(server.state)
		_, err := server.StartAgents(user, nil)
		require.NoError(t, err)
		require.NoError(t, server.RestartRole(user, "builder", &genhighbay.RoleAgentChoice{Client: "codex", Program: "/bin/codex"}))
		after, found := fakes.core.record("t3")
		require.True(t, found)
		assert.Equal(t, "/bin/codex", after.Argv[0])
		team, err := server.ReadTeam(t.Context())
		require.NoError(t, err)
		require.Contains(t, team.SavedChoices, "builder")
		assert.Equal(t, "codex", team.SavedChoices["builder"].Client)
	})

	t.Run("a role whose terminal ended is started", func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		user := userContext(server.state)
		_, err := server.StartAgents(user, nil)
		require.NoError(t, err)
		ended, _ := fakes.core.record("t1")
		ended.State = hc.StateFinished
		fakes.core.add(ended)
		require.NoError(t, server.RestartRole(user, "builder", nil))
		assert.Empty(t, fakes.core.kills, "nothing was running")
		assert.Equal(t, []string{"t1"}, fakes.core.removes)
		_, found := fakes.core.record("t3")
		assert.True(t, found)
	})

	t.Run("a guide that never started is started", func(t *testing.T) {
		server, fakes := teamServer(t)
		require.NoError(t, server.RestartRole(userContext(server.state), "guide", nil))
		guide, found := fakes.core.record("t1")
		require.True(t, found)
		assert.Equal(t, "guide", guide.Labels[labelRole])
		assert.Equal(t, "true", guide.Labels[labelManaged])
	})

	for _, refused := range []struct {
		name    string
		role    string
		arrange func(*testing.T, *Server, *teamFakes)
		choice  *genhighbay.RoleAgentChoice
		want    string
	}{
		{
			name: "a terminal the user attached", role: "guide",
			arrange: func(t *testing.T, server *Server, fakes *teamFakes) {
				record, _ := server.currentTeam()
				fakes.core.add(hc.Terminal{Address: "mine", State: hc.StateLive, Labels: map[string]string{labelProject: record.ID, labelRole: "guide"}})
			},
			want: "role_not_running",
		},
		{
			name: "a guide present in Chorus without a terminal", role: "guide",
			arrange: func(t *testing.T, server *Server, _ *teamFakes) {
				_, err := server.AttachGuide(teamContext(server.state), &genproject.AttachGuidePayload{})
				require.NoError(t, err)
				guidePresentInChorus(server, server.now())
			},
			want: "role_not_running",
		},
		{name: "a worker no build has registered", role: "builder", arrange: func(*testing.T, *Server, *teamFakes) {}, want: "role_not_running"},
		{
			name: "a coding agent that is not installed", role: "guide",
			arrange: func(t *testing.T, server *Server, _ *teamFakes) {
				_, err := server.SendMessage(userContext(server.state), &genhighbay.SendTeamMessagePayload{To: "guide", Text: "Hello"})
				require.NoError(t, err)
			},
			choice: &genhighbay.RoleAgentChoice{Client: "gemini", Program: "/bin/gemini"},
			want:   "unprocessable",
		},
		{
			name: "herdr-core cannot be reached", role: "guide",
			arrange: func(_ *testing.T, _ *Server, fakes *teamFakes) { fakes.core.callErr = hc.ErrClosed },
			want:    "team_unavailable",
		},
	} {
		t.Run("refused: "+refused.name, func(t *testing.T) {
			server, fakes := teamServer(t)
			refused.arrange(t, server, fakes)
			live, err := fakes.core.List(t.Context(), hc.ListRequest{})
			if fakes.core.callErr == nil {
				require.NoError(t, err)
			}
			kills, removes, runs := len(fakes.core.kills), len(fakes.core.removes), len(fakes.core.runs)
			err = server.RestartRole(userContext(server.state), refused.role, refused.choice)
			assert.Equal(t, refused.want, errorName(err))
			assert.Len(t, fakes.core.kills, kills, "a refused restart ends nothing")
			assert.Len(t, fakes.core.removes, removes)
			assert.Len(t, fakes.core.runs, runs, "and starts nothing")
			if fakes.core.callErr == nil {
				still, err := fakes.core.List(t.Context(), hc.ListRequest{})
				require.NoError(t, err)
				assert.ElementsMatch(t, live, still, "every terminal is as it was")
			}
		})
	}
}

// TestClosedServerLeavesHerdrCoreAlone checks that once the server closed
// its team, no operation opens a new connection to herdr-core: a connection
// opened then would start a client process that nothing ends.
func TestClosedServerLeavesHerdrCoreAlone(t *testing.T) {
	server, fakes := teamServer(t)
	buildTeam(t, server)
	user := userContext(server.state)
	_, err := server.StartAgents(user, nil)
	require.NoError(t, err)
	require.NoError(t, server.Close())
	dials := fakes.core.dials

	assert.Equal(t, "team_unavailable", errorName(server.StopRole(user, "builder")))
	assert.Equal(t, "team_unavailable", errorName(server.InterruptRole(user, "builder")))
	assert.Equal(t, "team_unavailable", errorName(server.RestartRole(user, "builder", nil)))
	_, err = server.CreateTerminalTicket(user, "builder")
	assert.Equal(t, "terminal_unavailable", errorName(err))
	assert.Equal(t, dials, fakes.core.dials, "an operation dialed herdr-core after the server closed")
	assert.Empty(t, fakes.core.kills)
}
