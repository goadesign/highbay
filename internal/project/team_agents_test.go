// These tests cover starting, stopping and interrupting the team's agents in
// herdr-core terminals, and attaching the user's own session as the guide.
package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errorName returns the generated error name of err, or "" when it has none.
func errorName(err error) string {
	var named interface{ GoaErrorName() string }
	if errors.As(err, &named) {
		return named.GoaErrorName()
	}
	return ""
}

// buildTeam registers a build's two workers in a scratch folder each and
// gives both work.
func buildTeam(t *testing.T, server *Server) (builder, tests string) {
	t.Helper()
	builder, tests = t.TempDir(), t.TempDir()
	require.NoError(t, server.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{BuilderFolder: builder, TestWriterFolder: tests}))
	fixtureAssignments(server, true)
	return builder, tests
}

// TestStartAgentsStartsMissingWorkers follows one start: each worker with
// work gets a terminal of its own, in its folder, with the labels the team
// and Chorus follow, the chosen agent's command line and the login
// environment; a second start leaves the running agents alone.
func TestStartAgentsStartsMissingWorkers(t *testing.T) {
	server, fakes := teamServer(t)
	builder, tests := buildTeam(t, server)
	record, _ := server.currentTeam()

	team, err := server.StartAgents(userContext(server.state), nil)
	require.NoError(t, err)
	assert.Equal(t, record.Team, team.Team)
	require.Len(t, fakes.core.runs, 2)
	for i, want := range []struct{ role, folder, program string }{
		{"builder", builder, "/bin/codex"},
		{"test_writer", tests, "/bin/claude"},
	} {
		run := fakes.core.runs[i]
		handle := teamName(want.role, record.ID)
		assert.Equal(t, want.folder, run.Cwd)
		assert.Equal(t, map[string]string{
			labelProject: record.ID, labelRole: want.role, labelManaged: "true",
			"chorus.project": record.Directory, "chorus.swarm": record.Team, "chorus.role": want.role, "chorus.handle": handle,
		}, run.Labels)
		assert.Equal(t, []string{want.program, "--", roleFirstMessage(teamRole{Role: want.role, Handle: handle, Folder: want.folder}, record.Team)}, run.Argv)
		assert.Equal(t, map[string]string{"LOGIN": "yes"}, run.Env)
		assert.True(t, run.NoParent && run.OutliveParent && run.NoNotify, "an agent's terminal has no parent and outlives the server")
	}
	assert.Len(t, fakes.trust, 2, "trust is prepared for each agent about to start")
	assert.Equal(t, record.Team, server.state.Record.Team, "the team name is saved at its first use")
	assert.Equal(t, roleChoice{Client: "codex", Program: "/bin/codex"}, server.state.Record.SavedChoices["builder"])

	_, err = server.StartAgents(userContext(server.state), nil)
	require.NoError(t, err)
	assert.Len(t, fakes.core.runs, 2, "running agents are not started again")
}

// TestStartAgentsReplacesWhatIsLeftOfARole checks that an ended agent's
// terminal is removed before the role starts again, and that a terminal the
// user attached is only relabeled, never ended.
func TestStartAgentsReplacesWhatIsLeftOfARole(t *testing.T) {
	server, fakes := teamServer(t)
	buildTeam(t, server)
	record, _ := server.currentTeam()
	labels := func(role string, managed bool) map[string]string {
		labels := map[string]string{labelProject: record.ID, labelRole: role}
		if managed {
			labels[labelManaged] = "true"
		}
		return labels
	}
	fakes.core.add(hc.Terminal{Address: "old-finished", State: hc.StateFinished, Labels: labels("builder", true)})
	fakes.core.add(hc.Terminal{
		Address: "old-shell", State: hc.StateLive, Labels: labels("builder", true), Foreground: &hc.Foreground{Argv: []string{"-zsh"}},
	})
	fakes.core.add(hc.Terminal{
		Address: "attached", State: hc.StateLive, Labels: labels("test_writer", false), Foreground: &hc.Foreground{Argv: []string{"-zsh"}},
	})

	_, err := server.StartAgents(userContext(server.state), nil)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"old-shell"}, fakes.core.kills)
	assert.ElementsMatch(t, []string{"old-finished", "old-shell"}, fakes.core.removes)
	attached, found := fakes.core.record("attached")
	require.True(t, found, "a terminal the user attached keeps running")
	assert.Equal(t, hc.StateLive, attached.State)
	assert.Empty(t, attached.Labels, "it no longer stands for the role")
	assert.Len(t, fakes.core.runs, 2)
}

// TestStartAgentsGuide covers the explicit guide choice: it starts a guide
// that is not running and is refused for one that is.
func TestStartAgentsGuide(t *testing.T) {
	server, fakes := teamServer(t)
	fixtureAssignments(server, false)
	choice := &genhighbay.RoleAgentChoice{Client: "claude", Program: "/bin/claude", Yolo: true}
	requested := map[string]*genhighbay.RoleAgentChoice{"guide": choice}

	team, err := server.StartAgents(userContext(server.state), requested)
	require.NoError(t, err)
	require.Len(t, fakes.core.runs, 1)
	assert.Equal(t, server.state.Record.Directory, fakes.core.runs[0].Cwd, "the guide works in the project folder")
	assert.Equal(t, "guide", fakes.core.runs[0].Labels[labelRole])
	require.Len(t, team.Roles, 1)
	assert.Equal(t, teamName("guide", server.state.Record.ID), team.Roles[0].Handle)
	assert.Contains(t, server.state.Record.Agents, team.Roles[0].Handle, "starting the guide registers it")
	assert.Equal(t, roleChoice{Client: "claude", Program: "/bin/claude", Yolo: true}, server.state.Record.SavedChoices["guide"])

	_, err = server.StartAgents(userContext(server.state), requested)
	assert.Equal(t, "unprocessable", errorName(err), "a running guide is never replaced")
	assert.Len(t, fakes.core.runs, 1)
}

// TestStartAgentsRefusals covers every way a start is refused, each before a
// terminal is started.
func TestStartAgentsRefusals(t *testing.T) {
	for _, test := range []struct {
		name    string
		arrange func(t *testing.T, server *Server, fakes *teamFakes) map[string]*genhighbay.RoleAgentChoice
		want    string
		cause   string
	}{
		{
			name: "the server is stopping",
			arrange: func(t *testing.T, server *Server, _ *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				require.NoError(t, server.Close())
				return nil
			},
			want: "team_unavailable", cause: "stopping",
		},
		{
			name: "assignments cannot be read",
			arrange: func(_ *testing.T, server *Server, _ *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				server.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) {
					return controlplane.TeamAssignments{}, errors.New("store locked")
				})
				return nil
			},
			want: "team_unavailable", cause: "project_state",
		},
		{
			name: "herdr-core cannot be reached",
			arrange: func(_ *testing.T, _ *Server, fakes *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				fakes.core.dialErr = errors.New("hc is not installed")
				return nil
			},
			want: "team_unavailable", cause: "hc",
		},
		{
			name: "herdr-core refuses the call",
			arrange: func(_ *testing.T, _ *Server, fakes *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				fakes.core.callErr = hc.ErrClosed
				return nil
			},
			want: "team_unavailable", cause: "hc",
		},
		{
			name: "a role's folder is gone",
			arrange: func(t *testing.T, server *Server, _ *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				require.NoError(t, os.Remove(server.state.Record.Agents[teamName("builder", server.state.Record.ID)].Folder))
				return nil
			},
			want: "team_unavailable", cause: "role_folder",
		},
		{
			name: "a choice names a program that is not installed",
			arrange: func(*testing.T, *Server, *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				return map[string]*genhighbay.RoleAgentChoice{"builder": {Client: "codex", Program: "/bin/other"}}
			},
			want: "unprocessable",
		},
		{
			name: "a choice asks for an option its client lacks",
			arrange: func(*testing.T, *Server, *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				return map[string]*genhighbay.RoleAgentChoice{"builder": {Client: "codex", Program: "/bin/codex", Yolo: true}}
			},
			want: "unprocessable",
		},
		{
			name: "a guide choice without a client",
			arrange: func(*testing.T, *Server, *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				return map[string]*genhighbay.RoleAgentChoice{"guide": nil}
			},
			want: "unprocessable",
		},
		{
			name: "the command line cannot be built",
			arrange: func(_ *testing.T, server *Server, _ *teamFakes) map[string]*genhighbay.RoleAgentChoice {
				server.team.dependencies.AgentCommand = func(*genhighbay.RoleAgentChoice, string) ([]string, error) {
					return nil, errors.New("unknown client")
				}
				return nil
			},
			want: "unprocessable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, fakes := teamServer(t)
			buildTeam(t, server)
			requested := test.arrange(t, server, fakes)
			_, err := server.StartAgents(userContext(server.state), requested)
			require.Error(t, err)
			assert.Equal(t, test.want, errorName(err))
			var unavailable *genproject.TeamUnavailable
			if test.cause != "" && assert.ErrorAs(t, err, &unavailable) {
				assert.Equal(t, test.cause, unavailable.Cause)
			}
			assert.Empty(t, fakes.core.runs)
		})
	}

	t.Run("a client's own settings block the agent", func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		fakes.trustErr = &controlplane.TeamPolicyRefusal{Kind: controlplane.PolicyCodexServerDisabled, Client: "codex", Server: "highbay"}
		_, err := server.StartAgents(userContext(server.state), nil)
		var policy *controlplane.TeamPolicyRefusal
		require.ErrorAs(t, err, &policy, "the control plane phrases the refusal")
		assert.Equal(t, controlplane.PolicyCodexServerDisabled, policy.Kind)
		assert.Empty(t, fakes.core.runs)
	})
}

// TestStartAgentsWithoutWork checks that nothing starts when the open build
// needs no worker, or there is no open build.
func TestStartAgentsWithoutWork(t *testing.T) {
	for _, test := range []struct {
		name        string
		assignments controlplane.TeamAssignments
	}{
		{name: "no build"},
		{name: "merged build", assignments: controlplane.TeamAssignments{
			BuildIdentity: &controlplane.BuildIdentity{ChangeID: "change"}, Merged: true, BuilderHasWork: true,
		}},
		{name: "dropped build", assignments: controlplane.TeamAssignments{
			BuildIdentity: &controlplane.BuildIdentity{ChangeID: "change"}, Dropped: true, BuilderHasWork: true,
		}},
		{name: "open build without work", assignments: controlplane.TeamAssignments{BuildIdentity: &controlplane.BuildIdentity{ChangeID: "change"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, fakes := teamServer(t)
			buildTeam(t, server)
			server.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) { return test.assignments, nil })
			team, err := server.StartAgents(userContext(server.state), nil)
			require.NoError(t, err)
			assert.Equal(t, "No build workers need work.", team.Step)
			assert.Empty(t, fakes.core.runs)
			assert.Zero(t, fakes.core.dials, "nothing to start asks herdr-core nothing")
		})
	}
}

// TestStopWorkers checks that a build's workers are ended only once that
// exact build is merged or dropped and leaves them no work.
func TestStopWorkers(t *testing.T) {
	build := controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(10, 0)}
	for _, test := range []struct {
		name        string
		assignments controlplane.TeamAssignments
		stopped     bool
	}{
		{name: "merged", assignments: controlplane.TeamAssignments{BuildIdentity: &build, Merged: true}, stopped: true},
		{name: "dropped", assignments: controlplane.TeamAssignments{BuildIdentity: &build, Dropped: true}, stopped: true},
		{name: "still open", assignments: controlplane.TeamAssignments{BuildIdentity: &build}},
		{name: "merged but a worker still has work", assignments: controlplane.TeamAssignments{BuildIdentity: &build, Merged: true, BuilderHasWork: true}},
		{
			name: "another build",
			assignments: controlplane.TeamAssignments{
				BuildIdentity: &controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Unix(11, 0)}, Merged: true,
			},
		},
		{name: "no build", assignments: controlplane.TeamAssignments{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, fakes := teamServer(t)
			buildTeam(t, server)
			_, err := server.StartAgents(userContext(server.state), map[string]*genhighbay.RoleAgentChoice{
				"guide": {Client: "claude", Program: "/bin/claude"},
			})
			require.NoError(t, err)
			require.Len(t, fakes.core.runs, 3)
			server.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) { return test.assignments, nil })

			require.NoError(t, server.StopWorkers(t.Context(), build))
			if !test.stopped {
				assert.Empty(t, fakes.core.kills)
				return
			}
			assert.Len(t, fakes.core.kills, 2, "both workers end")
			assert.Len(t, fakes.core.removes, 2)
			guide, err := fakes.core.List(t.Context(), hc.ListRequest{Labels: map[string]string{labelRole: "guide"}})
			require.NoError(t, err)
			assert.Len(t, guide, 1, "the guide keeps running")
			require.NoError(t, server.StopWorkers(t.Context(), build))
			assert.Len(t, fakes.core.kills, 2, "a build's workers are stopped once")
		})
	}

	t.Run("herdr-core cannot be reached", func(t *testing.T) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		server.BindAssignments(func(context.Context) (controlplane.TeamAssignments, error) {
			return controlplane.TeamAssignments{BuildIdentity: &build, Merged: true}, nil
		})
		fakes.core.dialErr = errors.New("hc is not installed")
		var unavailable *genproject.TeamUnavailable
		require.ErrorAs(t, server.StopWorkers(t.Context(), build), &unavailable)
		assert.Equal(t, "hc", unavailable.Cause)
	})
}

// TestStopAndInterruptRole covers the user's two controls over one role.
func TestStopAndInterruptRole(t *testing.T) {
	started := func(t *testing.T) (*Server, *teamFakes) {
		server, fakes := teamServer(t)
		buildTeam(t, server)
		_, err := server.StartAgents(userContext(server.state), nil)
		require.NoError(t, err)
		return server, fakes
	}

	t.Run("stop ends and removes the role's terminal", func(t *testing.T) {
		server, fakes := started(t)
		require.NoError(t, server.StopRole(userContext(server.state), "builder"))
		assert.Equal(t, []string{"t1"}, fakes.core.kills)
		assert.Equal(t, []string{"t1"}, fakes.core.removes)
		assert.Equal(t, "role_not_running", errorName(server.StopRole(userContext(server.state), "builder")), "nothing is left to stop")
	})

	t.Run("stop leaves a session the user attached", func(t *testing.T) {
		server, fakes := teamServer(t)
		record, _ := server.currentTeam()
		fakes.core.add(hc.Terminal{Address: "mine", State: hc.StateLive, Labels: map[string]string{labelProject: record.ID, labelRole: "guide"}})
		assert.Equal(t, "role_not_running", errorName(server.StopRole(userContext(server.state), "guide")))
		assert.Empty(t, fakes.core.kills)
	})

	t.Run("interrupt presses the stop key", func(t *testing.T) {
		server, fakes := started(t)
		require.NoError(t, server.InterruptRole(userContext(server.state), "test_writer"))
		assert.Equal(t, []string{"t2"}, fakes.core.interrupts)
	})

	t.Run("interrupt finds no running terminal", func(t *testing.T) {
		server, _ := teamServer(t)
		assert.Equal(t, "role_not_running", errorName(server.InterruptRole(userContext(server.state), "builder")))
	})

	for _, operation := range []string{"stop", "interrupt"} {
		t.Run(operation+" when herdr-core cannot be reached", func(t *testing.T) {
			server, fakes := started(t)
			fakes.core.callErr = hc.ErrClosed
			err := server.StopRole(userContext(server.state), "builder")
			if operation == "interrupt" {
				err = server.InterruptRole(userContext(server.state), "builder")
			}
			assert.Equal(t, "team_unavailable", errorName(err))
		})
	}
}

// TestAttachGuide covers which herdr-core terminal, if any, the calling
// session is found in.
func TestAttachGuide(t *testing.T) {
	terminalID := func(id string) *string { return &id }
	for _, test := range []struct {
		name      string
		terminals func(directory string) []hc.Terminal
		payload   genproject.AttachGuidePayload
		dialErr   error
		labeled   string
	}{
		{name: "a session outside herdr-core", payload: genproject.AttachGuidePayload{}},
		{
			name: "named by its terminal ID, in the project folder",
			terminals: func(directory string) []hc.Terminal {
				return []hc.Terminal{{Address: "t7", State: hc.StateLive, Cwd: directory}}
			},
			payload: genproject.AttachGuidePayload{HcTerminalID: terminalID("t7")},
			labeled: "t7",
		},
		{
			name: "named by its terminal ID, working below the project folder",
			terminals: func(directory string) []hc.Terminal {
				return []hc.Terminal{{Address: "t7", State: hc.StateLive, Cwd: "/elsewhere", Foreground: &hc.Foreground{Cwd: filepath.Join(directory, "service")}}}
			},
			payload: genproject.AttachGuidePayload{HcTerminalID: terminalID("t7")},
			labeled: "t7",
		},
		{
			name: "named by its terminal ID, in another folder",
			terminals: func(directory string) []hc.Terminal {
				return []hc.Terminal{{Address: "t7", State: hc.StateLive, Cwd: filepath.Dir(directory)}}
			},
			payload: genproject.AttachGuidePayload{HcTerminalID: terminalID("t7")},
		},
		{
			name: "a folder that only shares the project folder's prefix",
			terminals: func(directory string) []hc.Terminal {
				return []hc.Terminal{{Address: "t7", State: hc.StateLive, Cwd: directory + "-copy"}}
			},
			payload: genproject.AttachGuidePayload{HcTerminalID: terminalID("t7")},
		},
		{
			name: "found by the terminal's own process",
			terminals: func(directory string) []hc.Terminal {
				return []hc.Terminal{
					{Address: "t3", State: hc.StateLive, PID: 400, Cwd: directory},
					{Address: "t4", State: hc.StateLive, PID: 900, Cwd: directory},
				}
			},
			payload: genproject.AttachGuidePayload{AncestorPids: []int{402, 401, 400}},
			labeled: "t3",
		},
		{
			name: "found by the terminal's foreground process",
			terminals: func(directory string) []hc.Terminal {
				return []hc.Terminal{{Address: "t3", State: hc.StateLive, PID: 400, Cwd: directory, Foreground: &hc.Foreground{PID: 401, Cwd: directory}}}
			},
			payload: genproject.AttachGuidePayload{AncestorPids: []int{402, 401}},
			labeled: "t3",
		},
		{
			name: "two terminals match and cannot be told apart",
			terminals: func(directory string) []hc.Terminal {
				return []hc.Terminal{
					{Address: "t3", State: hc.StateLive, PID: 400, Cwd: directory},
					{Address: "t4", State: hc.StateLive, PID: 401, Cwd: directory},
				}
			},
			payload: genproject.AttachGuidePayload{AncestorPids: []int{401, 400}},
		},
		{
			name: "a finished terminal",
			terminals: func(directory string) []hc.Terminal {
				return []hc.Terminal{{Address: "t7", State: hc.StateFinished, Cwd: directory}}
			},
			payload: genproject.AttachGuidePayload{HcTerminalID: terminalID("t7")},
		},
		{
			name:    "herdr-core cannot be reached",
			payload: genproject.AttachGuidePayload{HcTerminalID: terminalID("t7")},
			dialErr: errors.New("hc is not installed"),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, fakes := teamServer(t)
			record, _ := server.currentTeam()
			if test.terminals != nil {
				for _, terminal := range test.terminals(record.Directory) {
					fakes.core.add(terminal)
				}
			}
			fakes.core.dialErr = test.dialErr
			handle := teamName("guide", record.ID)

			result, err := server.AttachGuide(teamContext(server.state), &test.payload)
			require.NoError(t, err)
			assert.Equal(t, &genproject.AttachGuideResult{Team: record.Team, Handle: handle, HasTerminal: test.labeled != ""}, result)
			assert.Equal(t, agentRecord{Role: "guide", Folder: record.Directory}, server.state.Record.Agents[handle])
			assert.Equal(t, record.Team, server.state.Record.Team)
			if test.labeled == "" {
				assert.Empty(t, fakes.core.labels)
				return
			}
			labeled, found := fakes.core.record(test.labeled)
			require.True(t, found)
			assert.Equal(t, map[string]string{
				labelProject: record.ID, labelRole: "guide",
				"chorus.project": record.Directory, "chorus.swarm": record.Team, "chorus.role": "guide", "chorus.handle": handle,
			}, labeled.Labels, "an attached terminal is the user's: it is not marked as Highbay's")
		})
	}

	t.Run("replaces the guide Highbay started and the one attached before", func(t *testing.T) {
		server, fakes := teamServer(t)
		record, _ := server.currentTeam()
		fakes.core.add(hc.Terminal{Address: "managed", State: hc.StateLive, Labels: map[string]string{labelProject: record.ID, labelRole: "guide", labelManaged: "true"}})
		fakes.core.add(hc.Terminal{Address: "before", State: hc.StateLive, Labels: map[string]string{labelProject: record.ID, labelRole: "guide"}})
		fakes.core.add(hc.Terminal{Address: "t7", State: hc.StateLive, Cwd: record.Directory})
		id := "t7"
		result, err := server.AttachGuide(teamContext(server.state), &genproject.AttachGuidePayload{HcTerminalID: &id})
		require.NoError(t, err)
		assert.True(t, result.HasTerminal)
		assert.Equal(t, []string{"managed"}, fakes.core.removes, "the guide Highbay started ends")
		before, found := fakes.core.record("before")
		require.True(t, found, "the session attached before keeps running")
		assert.Empty(t, before.Labels)
	})

	t.Run("refusals", func(t *testing.T) {
		server, _ := teamServer(t)
		_, err := server.AttachGuide(userContext(server.state), &genproject.AttachGuidePayload{})
		assert.Equal(t, "forbidden", errorName(err), "only the highbay command attaches a guide")
		require.NoError(t, server.Close())
		_, err = server.AttachGuide(teamContext(server.state), &genproject.AttachGuidePayload{})
		var unavailable *genproject.TeamUnavailable
		require.ErrorAs(t, err, &unavailable)
		assert.Equal(t, "stopping", unavailable.Cause)
	})
}
