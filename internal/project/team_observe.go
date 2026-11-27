// This file keeps what the project server last observed of its team: this
// project's terminals as herdr-core reports them, the project's Chorus swarm,
// and the open build's assignments. It derives each role's public state from
// those facts.
package project

import (
	"sync"
	"time"

	genchorus "github.com/goadesign/chorus/gen/view"
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
)

type (
	// teamObservation is the supervisor's latest view of the team. Reads
	// never call herdr-core or Chorus: they see what was last observed, and
	// a fact that could not be observed is reported as unknown.
	teamObservation struct {
		// mu guards every field below.
		mu sync.RWMutex
		// terminals holds this project's terminals by address, as the
		// subscription's snapshot and events describe them.
		terminals map[string]hc.Terminal
		// terminalsKnown is true while the subscription to herdr-core is
		// established. False means terminals says nothing.
		terminalsKnown bool
		// view is the project's Chorus swarm as last read; nil before the
		// first read.
		view *genchorus.SwarmView
		// chorusConnected is true when the latest Chorus read succeeded.
		chorusConnected bool
		// assignments is the open build's work per role as last read.
		assignments controlplane.TeamAssignments
		// assignmentsKnown is false when the latest assignment read failed.
		assignmentsKnown bool
		// notified maps a worker role to the assignment generation Highbay
		// last told it about in Chorus.
		notified map[string]string
		// guideStopped is when Highbay last ended the guide's terminal;
		// zero when it has not, or since a guide started or attached again.
		// Chorus keeps showing the guide as recently active for minutes
		// after its program is gone, which must not count as a guide.
		guideStopped time.Time
	}
)

const (
	// labelProject, labelRole and labelManaged mark a terminal as one of
	// this project's roles. Only a terminal Highbay started carries
	// labelManaged; a session the user attached does not, and Highbay never
	// ends it.
	labelProject = "highbay.project"
	labelRole    = "highbay.role"
	labelManaged = "highbay.managed"
	// teamSilentBound is how long a started agent may run without reporting
	// any status before the user is told it may need them.
	teamSilentBound = 60 * time.Second
	// chorusOnlineWindow is how long after its last Chorus call an agent
	// without a terminal still counts as present. It equals Chorus's own
	// window for keeping a registration.
	chorusOnlineWindow = 5 * time.Minute
)

// applyTerminals applies one update from the terminal subscription, following
// herdr-core's rules for each event. A terminal whose labels no longer name
// project leaves the view.
func (o *teamObservation) applyTerminals(update hc.StreamUpdate, project string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch update := update.(type) {
	case hc.Snapshot:
		o.replaceTerminals(update.Terminals)
	case hc.Missed:
		o.replaceTerminals(update.Terminals)
	case hc.Event:
		terminal, known := o.terminals[update.Terminal]
		switch change := update.Change.(type) {
		case hc.Created:
			terminal, known = change.Terminal, true
		case hc.Restarted:
			terminal, known = change.Terminal, true
		case hc.Lost:
			terminal.Reason, terminal.ExitCode, terminal.Signal = change.End.Reason, change.End.ExitCode, change.End.Signal
			terminal.PID, terminal.Foreground = 0, nil
		case hc.Finished:
			terminal.State, terminal.FinishedMS = hc.StateFinished, change.End.FinishedMS
			terminal.Reason, terminal.ExitCode, terminal.Signal = change.End.Reason, change.End.ExitCode, change.End.Signal
			terminal.Foreground = nil
		case hc.Removed:
			delete(o.terminals, update.Terminal)
			return
		case hc.StatusChanged:
			terminal.Status, terminal.Records, terminal.Reported = change.Status, change.Records, change.Reported
		case hc.TurnEnded:
			terminal.Turn = change.Turn
		case hc.TitleChanged:
			terminal.Title = change.Title
		case hc.LabelsChanged:
			if change.Record != nil {
				terminal, known = *change.Record, true
			}
			terminal.Labels = change.Labels
			if change.Labels[labelProject] != project {
				delete(o.terminals, update.Terminal)
				return
			}
		case hc.SizeChanged:
			terminal.Cols, terminal.Rows = change.Cols, change.Rows
		case hc.ForegroundChanged:
			terminal.Foreground = change.Foreground
		}
		if known {
			o.terminals[update.Terminal] = terminal
		}
	}
}

// replaceTerminals installs a whole snapshot. o.mu is held.
func (o *teamObservation) replaceTerminals(terminals []hc.Terminal) {
	o.terminals = make(map[string]hc.Terminal, len(terminals))
	for _, terminal := range terminals {
		o.terminals[terminal.Address] = terminal
	}
	o.terminalsKnown = true
}

// terminalsLost records that the subscription ended: nothing is known about
// the terminals until the next snapshot.
func (o *teamObservation) terminalsLost() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.terminals, o.terminalsKnown = nil, false
}

// chorusRead records the outcome of a Chorus read. A nil view with connected
// true means nothing changed since the view already held.
func (o *teamObservation) chorusRead(view *genchorus.SwarmView, connected bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if view != nil {
		o.view = view
	}
	o.chorusConnected = connected
}

// assignmentsRead records the outcome of an assignment read.
func (o *teamObservation) assignmentsRead(assignments controlplane.TeamAssignments, known bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.assignments, o.assignmentsKnown = assignments, known
}

// roleTerminals returns the terminals labeled for role, and whether the
// terminals are known at all.
func (o *teamObservation) roleTerminals(role string) ([]hc.Terminal, bool) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	var terminals []hc.Terminal
	for _, terminal := range o.terminals {
		if terminal.Labels[labelRole] == role {
			terminals = append(terminals, terminal)
		}
	}
	return terminals, o.terminalsKnown
}

// roleState derives one role's public state at now. shell is the user's login
// shell, which tells a running agent from a terminal back at its prompt.
func (o *teamObservation) roleState(now time.Time, role teamRole, shell string) *genhighbay.TeamRoleState {
	terminals, known := o.roleTerminals(role.Role)
	o.mu.RLock()
	defer o.mu.RUnlock()
	state := &genhighbay.TeamRoleState{
		Role: role.Role, Handle: role.Handle, State: "unknown", Attention: "none", Assignment: o.assignment(role.Role),
	}
	var live []hc.Terminal
	for _, terminal := range terminals {
		if terminal.State == hc.StateLive {
			live = append(live, terminal)
		}
	}
	switch {
	case len(live) > 1:
		// Two live terminals claim the role: neither can be called its state.
		state.HasTerminal = true
	case len(live) == 1:
		state.HasTerminal = true
		state.State, state.Attention = terminalState(now, live[0], shell)
	case len(terminals) > 0:
		// The role's program ended. That outranks Chorus, which shows an
		// agent as recently active for minutes after it is gone.
		state.State = "ended"
		for _, terminal := range terminals {
			if terminal.ExitCode != nil && *terminal.ExitCode != 0 || terminal.Signal != nil {
				state.State = "error"
			}
		}
	case role.Role == "guide" && o.present(now, role.Handle):
		// A guide session outside herdr-core is known only through Chorus.
		state.State = "working"
		if o.watching(role.Handle) {
			state.State = "waiting"
		}
	case !known:
	default:
		state.State = "not_started"
	}
	return state
}

// assignment is the role's assignment state. The guide has no assignments.
// o.mu is held.
func (o *teamObservation) assignment(role string) string {
	switch {
	case role == "guide":
		return "no_work"
	case !o.assignmentsKnown:
		return "unknown"
	case !o.assignments.HasWork(role):
		return "no_work"
	}
	for _, owed := range o.assignments.Unread {
		if owed.Role == role {
			return "unread"
		}
	}
	return "nothing_owed"
}

// present reports whether Chorus shows the guide, handle, as registered and
// listening or recently active. Activity from before Highbay ended the
// guide's terminal does not count. o.mu is held.
func (o *teamObservation) present(now time.Time, handle string) bool {
	if o.view == nil || !o.chorusConnected {
		return false
	}
	for _, agent := range o.view.Agents {
		if agent.Handle != handle {
			continue
		}
		if agent.WatchingOnThisServer {
			return true
		}
		seen := time.Unix(agent.LastSeenUnix, 0)
		return now.Sub(seen) < chorusOnlineWindow && seen.After(o.guideStopped)
	}
	return false
}

// guideEnded records that Highbay ended the guide's terminal at now.
func (o *teamObservation) guideEnded(now time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.guideStopped = now
}

// guideBegan records that a guide started or attached, so its activity in
// Chorus counts again.
func (o *teamObservation) guideBegan() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.guideStopped = time.Time{}
}

// watching reports whether Chorus holds a watch for handle. o.mu is held.
func (o *teamObservation) watching(handle string) bool {
	for _, agent := range o.view.Agents {
		if agent.Handle == handle {
			return agent.WatchingOnThisServer
		}
	}
	return false
}

// listening reports whether handle has joined channel in Chorus, so a post
// that mentions it there reaches it. A post made before an agent joins is
// behind its read position and would never wake it.
func (o *teamObservation) listening(handle, channel string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.view == nil || !o.chorusConnected {
		return false
	}
	for _, agent := range o.view.Agents {
		if agent.Handle != handle {
			continue
		}
		for _, joined := range agent.Channels {
			if joined == channel {
				return true
			}
		}
	}
	return false
}

// terminalState derives a role's state and attention from its one live
// terminal. herdr-core never infers status: a program that reports none is
// starting until teamSilentBound passes, after which the user may be needed.
func terminalState(now time.Time, terminal hc.Terminal, shell string) (state, attention string) {
	switch {
	case terminal.Reason == "spawn_pending":
		return "starting", "none"
	case !hc.RoleRunning(terminal, shell):
		// The agent exited and left its terminal at a shell prompt.
		return "ended", "none"
	case !terminal.Reported || terminal.Status == nil:
		if now.Sub(time.UnixMilli(int64(terminal.CreatedMS))) < teamSilentBound {
			return "starting", "none"
		}
		return "needs_you", "silent"
	}
	switch terminal.Status.State {
	case "working":
		return "working", "none"
	case "idle", "done":
		return "waiting", "none"
	case "error":
		return "error", "none"
	case "blocked":
		switch terminal.Status.Kind {
		case "permission", "question", "auth":
			return "needs_you", terminal.Status.Kind
		}
		return "needs_you", "unknown"
	}
	return "unknown", "none"
}
