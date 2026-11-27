// These tests cover what the team observation derives from herdr-core's
// terminal records, Chorus's swarm and the open build's assignments.
package project

import (
	"testing"
	"time"

	genchorus "github.com/goadesign/chorus/gen/view"
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
	"github.com/stretchr/testify/assert"
)

// TestRoleState checks the state, attention and terminal flag derived for a
// role from each kind of observation.
func TestRoleState(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	created := uint64(now.Add(-10 * time.Second).UnixMilli())
	agent := &hc.Foreground{Argv: []string{"/bin/claude"}}
	live := func(change func(*hc.Terminal)) hc.Terminal {
		terminal := hc.Terminal{
			Address: "t1", State: hc.StateLive, Labels: map[string]string{labelRole: "builder"},
			CreatedMS: created, Foreground: agent, Reported: true, Status: &hc.Status{State: "working"},
		}
		change(&terminal)
		return terminal
	}
	finished := hc.Terminal{Address: "t0", State: hc.StateFinished, Labels: map[string]string{labelRole: "builder"}}
	type want struct {
		state, attention string
		terminal         bool
	}
	for _, test := range []struct {
		name      string
		terminals []hc.Terminal
		unknown   bool
		want      want
	}{
		{name: "never started", want: want{state: "not_started", attention: "none"}},
		{name: "herdr-core unreachable", unknown: true, want: want{state: "unknown", attention: "none"}},
		{name: "only a finished terminal", terminals: []hc.Terminal{finished}, want: want{state: "ended", attention: "none"}},
		{name: "working", terminals: []hc.Terminal{live(func(*hc.Terminal) {})}, want: want{"working", "none", true}},
		{name: "idle", terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Status.State = "idle" })}, want: want{"waiting", "none", true}},
		{name: "done", terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Status.State = "done" })}, want: want{"waiting", "none", true}},
		{name: "error", terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Status.State = "error" })}, want: want{"error", "none", true}},
		{
			name:      "blocked on a permission",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Status = &hc.Status{State: "blocked", Kind: "permission"} })},
			want:      want{"needs_you", "permission", true},
		},
		{
			name:      "blocked on a question",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Status = &hc.Status{State: "blocked", Kind: "question"} })},
			want:      want{"needs_you", "question", true},
		},
		{
			name:      "blocked on sign-in",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Status = &hc.Status{State: "blocked", Kind: "auth"} })},
			want:      want{"needs_you", "auth", true},
		},
		{
			name:      "blocked without a reason",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Status = &hc.Status{State: "blocked"} })},
			want:      want{"needs_you", "unknown", true},
		},
		{
			name:      "a status word this version does not know",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Status.State = "dreaming" })},
			want:      want{"unknown", "none", true},
		},
		{
			name:      "program not spawned yet",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Reason = "spawn_pending" })},
			want:      want{"starting", "none", true},
		},
		{
			name: "no status reported just under the bound",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) {
				t.Reported, t.Status = false, nil
				t.CreatedMS = uint64(now.Add(-teamSilentBound + time.Millisecond).UnixMilli())
			})},
			want: want{"starting", "none", true},
		},
		{
			name: "no status reported at the bound",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) {
				t.Reported, t.Status = false, nil
				t.CreatedMS = uint64(now.Add(-teamSilentBound).UnixMilli())
			})},
			want: want{"needs_you", "silent", true},
		},
		{
			name:      "agent exited to its shell",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Foreground = &hc.Foreground{Argv: []string{"-zsh"}} })},
			want:      want{"ended", "none", true},
		},
		{
			name:      "no foreground reported counts as running",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Foreground = nil })},
			want:      want{"working", "none", true},
		},
		{
			name:      "a finished terminal beside the live one",
			terminals: []hc.Terminal{finished, live(func(*hc.Terminal) {})},
			want:      want{"working", "none", true},
		},
		{
			name:      "two live terminals claim the role",
			terminals: []hc.Terminal{live(func(*hc.Terminal) {}), live(func(t *hc.Terminal) { t.Address = "t2" })},
			want:      want{"unknown", "none", true},
		},
		{
			name:      "a terminal of another role",
			terminals: []hc.Terminal{live(func(t *hc.Terminal) { t.Labels = map[string]string{labelRole: "test_writer"} })},
			want:      want{state: "not_started", attention: "none"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var observed teamObservation
			if !test.unknown {
				observed.applyTerminals(hc.Snapshot{Terminals: test.terminals}, "project")
			}
			got := observed.roleState(now, teamRole{Role: "builder", Handle: "builder-1"}, "/bin/zsh")
			assert.Equal(t, &genhighbay.TeamRoleState{
				Role: "builder", Handle: "builder-1", HasTerminal: test.want.terminal,
				State: test.want.state, Attention: test.want.attention, Assignment: "unknown",
			}, got)
		})
	}
}

// TestRoleStateOfAGuideWithoutATerminal checks the guide session that runs
// outside herdr-core, known only through Chorus.
func TestRoleStateOfAGuideWithoutATerminal(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	guide := teamRole{Role: "guide", Handle: "guide-1"}
	for _, test := range []struct {
		name      string
		agent     *genchorus.Agent
		connected bool
		want      string
	}{
		{name: "holding a watch", agent: &genchorus.Agent{Handle: "guide-1", WatchingOnThisServer: true}, connected: true, want: "waiting"},
		{
			name:      "seen just inside the window",
			agent:     &genchorus.Agent{Handle: "guide-1", LastSeenUnix: now.Add(-chorusOnlineWindow + time.Second).Unix()},
			connected: true, want: "working",
		},
		{
			name:      "last seen at the window's edge",
			agent:     &genchorus.Agent{Handle: "guide-1", LastSeenUnix: now.Add(-chorusOnlineWindow).Unix()},
			connected: true, want: "not_started",
		},
		{name: "another handle", agent: &genchorus.Agent{Handle: "guide-2", WatchingOnThisServer: true}, connected: true, want: "not_started"},
		{name: "not in the swarm", connected: true, want: "not_started"},
		{name: "Chorus unreachable", agent: &genchorus.Agent{Handle: "guide-1", WatchingOnThisServer: true}, want: "not_started"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var observed teamObservation
			observed.applyTerminals(hc.Snapshot{}, "project")
			view := &genchorus.SwarmView{}
			if test.agent != nil {
				view.Agents = []*genchorus.Agent{test.agent}
			}
			observed.chorusRead(view, test.connected)
			got := observed.roleState(now, guide, "/bin/zsh")
			assert.Equal(t, test.want, got.State)
			assert.False(t, got.HasTerminal)
			assert.Equal(t, "no_work", got.Assignment, "the guide has no assignments")
		})
	}
}

// TestRoleAssignment checks a worker's assignment state for each outcome of
// the assignment read.
func TestRoleAssignment(t *testing.T) {
	for _, test := range []struct {
		name        string
		assignments controlplane.TeamAssignments
		known       bool
		want        string
	}{
		{name: "read failed", want: "unknown"},
		{name: "no work", known: true, want: "no_work"},
		{name: "work, nothing owed", assignments: controlplane.TeamAssignments{BuilderHasWork: true}, known: true, want: "nothing_owed"},
		{
			name: "work, read owed",
			assignments: controlplane.TeamAssignments{
				BuilderHasWork: true, Unread: []controlplane.AssignmentGeneration{{Role: "builder", Generation: "g1"}},
			},
			known: true, want: "unread",
		},
		{
			name: "another role owes a read",
			assignments: controlplane.TeamAssignments{
				BuilderHasWork: true, Unread: []controlplane.AssignmentGeneration{{Role: "test_writer", Generation: "g1"}},
			},
			known: true, want: "nothing_owed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var observed teamObservation
			observed.assignmentsRead(test.assignments, test.known)
			got := observed.roleState(time.Unix(0, 0), teamRole{Role: "builder", Handle: "builder-1"}, "/bin/zsh")
			assert.Equal(t, test.want, got.Assignment)
		})
	}
}

// TestApplyTerminals checks how each kind of change herdr-core reports
// updates the team's view of a terminal.
func TestApplyTerminals(t *testing.T) {
	exit := int32(3)
	labels := map[string]string{labelProject: "project", labelRole: "builder"}
	start := hc.Terminal{Address: "t1", State: hc.StateLive, PID: 7, Labels: labels, Foreground: &hc.Foreground{PID: 8}}
	other := hc.Terminal{Address: "t9", State: hc.StateLive, Labels: labels}
	for _, test := range []struct {
		name   string
		update hc.StreamUpdate
		want   map[string]hc.Terminal
	}{
		{
			name:   "created",
			update: hc.Event{Terminal: "t2", Change: hc.Created{Terminal: hc.Terminal{Address: "t2", State: hc.StateLive}}},
			want:   map[string]hc.Terminal{"t1": start, "t2": {Address: "t2", State: hc.StateLive}},
		},
		{
			name:   "restarted replaces the record",
			update: hc.Event{Terminal: "t1", Change: hc.Restarted{Terminal: hc.Terminal{Address: "t1", State: hc.StateLive, PID: 99}}},
			want:   map[string]hc.Terminal{"t1": {Address: "t1", State: hc.StateLive, PID: 99}},
		},
		{
			name:   "lost keeps it live without a program",
			update: hc.Event{Terminal: "t1", Change: hc.Lost{End: hc.CloseReason{Reason: "signaled"}}},
			want:   map[string]hc.Terminal{"t1": {Address: "t1", State: hc.StateLive, Labels: labels, Reason: "signaled"}},
		},
		{
			name:   "finished",
			update: hc.Event{Terminal: "t1", Change: hc.Finished{End: hc.CloseReason{Reason: "exited", ExitCode: &exit, FinishedMS: 5}}},
			want: map[string]hc.Terminal{"t1": {
				Address: "t1", State: hc.StateFinished, PID: 7, Labels: labels, Reason: "exited", ExitCode: &exit, FinishedMS: 5,
			}},
		},
		{name: "removed", update: hc.Event{Terminal: "t1", Change: hc.Removed{}}, want: map[string]hc.Terminal{}},
		{
			name:   "status",
			update: hc.Event{Terminal: "t1", Change: hc.StatusChanged{Status: &hc.Status{State: "idle"}, Reported: true}},
			want: map[string]hc.Terminal{"t1": {
				Address: "t1", State: hc.StateLive, PID: 7, Labels: labels, Foreground: start.Foreground,
				Status: &hc.Status{State: "idle"}, Reported: true,
			}},
		},
		{
			name:   "turn",
			update: hc.Event{Terminal: "t1", Change: hc.TurnEnded{Turn: 4, State: "done"}},
			want: map[string]hc.Terminal{"t1": {
				Address: "t1", State: hc.StateLive, PID: 7, Labels: labels, Foreground: start.Foreground, Turn: 4,
			}},
		},
		{
			name:   "title",
			update: hc.Event{Terminal: "t1", Change: hc.TitleChanged{Title: "builder"}},
			want: map[string]hc.Terminal{"t1": {
				Address: "t1", State: hc.StateLive, PID: 7, Labels: labels, Foreground: start.Foreground, Title: "builder",
			}},
		},
		{
			name:   "size",
			update: hc.Event{Terminal: "t1", Change: hc.SizeChanged{Cols: 120, Rows: 40}},
			want: map[string]hc.Terminal{"t1": {
				Address: "t1", State: hc.StateLive, PID: 7, Labels: labels, Foreground: start.Foreground, Cols: 120, Rows: 40,
			}},
		},
		{
			name:   "foreground cleared",
			update: hc.Event{Terminal: "t1", Change: hc.ForegroundChanged{}},
			want:   map[string]hc.Terminal{"t1": {Address: "t1", State: hc.StateLive, PID: 7, Labels: labels}},
		},
		{
			name:   "labels now name this project: the record arrives with them",
			update: hc.Event{Terminal: "t9", Change: hc.LabelsChanged{Labels: labels, Record: &other}},
			want:   map[string]hc.Terminal{"t1": start, "t9": other},
		},
		{
			name:   "labels no longer name this project",
			update: hc.Event{Terminal: "t1", Change: hc.LabelsChanged{Labels: map[string]string{}}},
			want:   map[string]hc.Terminal{},
		},
		{
			name:   "a change to a terminal never seen is ignored",
			update: hc.Event{Terminal: "t5", Change: hc.TitleChanged{Title: "stray"}},
			want:   map[string]hc.Terminal{"t1": start},
		},
		{name: "heartbeat", update: hc.Heartbeat{Seq: 9}, want: map[string]hc.Terminal{"t1": start}},
		{name: "missed replaces everything", update: hc.Missed{Terminals: []hc.Terminal{other}}, want: map[string]hc.Terminal{"t9": other}},
		{
			name:   "a kind this version does not decode changes nothing",
			update: hc.Event{Terminal: "t1", Change: hc.OtherChange{Type: "keep"}},
			want:   map[string]hc.Terminal{"t1": start},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var observed teamObservation
			observed.applyTerminals(hc.Snapshot{Terminals: []hc.Terminal{start}}, "project")
			observed.applyTerminals(test.update, "project")
			assert.Equal(t, test.want, observed.terminals)
			assert.True(t, observed.terminalsKnown)
		})
	}

	t.Run("a lost subscription leaves nothing known", func(t *testing.T) {
		var observed teamObservation
		observed.applyTerminals(hc.Snapshot{Terminals: []hc.Terminal{start}}, "project")
		observed.terminalsLost()
		assert.False(t, observed.terminalsKnown)
		assert.Empty(t, observed.terminals)
	})
}
