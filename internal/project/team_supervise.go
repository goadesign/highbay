// This file keeps the team observation current for the server's lifetime: it
// follows this project's terminals in herdr-core, reads the project's Chorus
// swarm, and reads the open build's assignments, telling a worker in Chorus
// when it has one to read.
package project

import (
	"context"
	"errors"
	"sync"
	"time"

	genchorus "github.com/goadesign/chorus/gen/view"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/hc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	goa "goa.design/goa/v3/pkg"
)

const (
	// teamRetryDelay is the pause before herdr-core or Chorus is tried again
	// after a failure. The loops retry for as long as the server runs.
	teamRetryDelay = 2 * time.Second
	// teamAssignmentInterval is how often assignments are read when nothing
	// else prompts a read.
	teamAssignmentInterval = 30 * time.Second
	// chorusViewWait is how long one Chorus read waits for the swarm to
	// change, in seconds.
	chorusViewWait = 60
)

// Supervise keeps the team observation current until ctx or the server's
// lifetime ends, then returns nil. Losing herdr-core or Chorus is not a
// reason to return: each loop marks its facts unknown and tries again.
func (s *Server) Supervise(ctx context.Context, assignments controlplane.AssignmentReader) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.team.dependencies.Lifetime, cancel)
	defer stop()
	// changed asks for a fresh assignment read after the terminals or the
	// swarm changed; one pending request is enough.
	changed := make(chan struct{}, 1)
	var loops sync.WaitGroup
	loops.Go(func() { s.followTerminals(ctx, changed) })
	loops.Go(func() { s.followChorus(ctx, changed) })
	s.followAssignments(ctx, assignments, changed)
	loops.Wait()
	return nil
}

// assignmentNotice is the fixed text that tells a worker to read its
// assignment. The guide has none.
func assignmentNotice(role string) string {
	switch role {
	case "builder":
		return "Highbay: you have an unread assignment. Use list_builder_assignments and get_builder_assignment to read it. Highbay reads no replies."
	case "test_writer":
		return "Highbay: you have an unread assignment. Use list_test_writer_assignments and get_test_writer_assignment to read it. Highbay reads no replies."
	}
	return ""
}

// teamOutage records that herdr-core or Chorus could not be used, without
// the dependency's own text, which may hold paths or peer output.
func teamOutage(ctx context.Context, name, dependency string, err error) {
	_, span := otel.Tracer("highbay/project").Start(ctx, name)
	defer span.End()
	span.SetAttributes(attribute.String("team.dependency", dependency), attribute.Bool("team.connection_closed", errors.Is(err, hc.ErrClosed)))
	span.RecordError(errors.New(dependency + " is unavailable"))
	span.SetStatus(codes.Error, "team dependency unavailable")
}

// notModified reports Chorus's answer that the swarm did not change within
// the wait.
func notModified(err error) bool {
	var refusal *goa.ServiceError
	return errors.As(err, &refusal) && refusal.Name == "not_modified"
}

// signalChange asks for a fresh assignment read unless one is already asked
// for.
func signalChange(changed chan<- struct{}) {
	select {
	case changed <- struct{}{}:
	default:
	}
}

// waitTeamRetry pauses before a retry. It reports false when ctx ended.
func waitTeamRetry(ctx context.Context) bool {
	timer := time.NewTimer(teamRetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// followTerminals keeps a subscription to this project's terminals open,
// dialing herdr-core again whenever the connection ends.
func (s *Server) followTerminals(ctx context.Context, changed chan<- struct{}) {
	project := s.state.Record.ID
	outage := false
	for ctx.Err() == nil {
		err := s.subscribeTerminals(ctx, project, changed)
		established := s.team.observed.terminalsSeen()
		s.team.observed.terminalsLost()
		signalChange(changed)
		if ctx.Err() != nil {
			return
		}
		if established || !outage {
			// One span per outage, not one per retry.
			teamOutage(ctx, "highbay.team.terminals_unavailable", "hc", err)
		}
		outage = true
		if !waitTeamRetry(ctx) {
			return
		}
	}
}

// subscribeTerminals follows the project's terminals over the shared
// connection until that connection or ctx ends.
func (s *Server) subscribeTerminals(ctx context.Context, project string, changed chan<- struct{}) error {
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return err
	}
	return client.SubscribeEvents(ctx, map[string]string{labelProject: project}, func(update hc.StreamUpdate) {
		s.team.observed.applyTerminals(update, project)
		signalChange(changed)
	})
}

// followChorus reads the project's swarm from Chorus, waiting for each
// change, and resolves the Chorus server again after a failed read.
func (s *Server) followChorus(ctx context.Context, changed chan<- struct{}) {
	etag := ""
	connected := true
	for ctx.Err() == nil {
		view, err := s.readChorus(ctx, etag)
		switch {
		case err == nil:
			etag, connected = view.Etag, true
			s.team.observed.chorusRead(view, true)
			signalChange(changed)
			continue
		case notModified(err):
			connected = true
			s.team.observed.chorusRead(nil, true)
			continue
		case ctx.Err() != nil:
			return
		}
		s.team.observed.chorusRead(nil, false)
		signalChange(changed)
		if connected {
			teamOutage(ctx, "highbay.team.chorus_unavailable", "chorus", err)
		}
		etag, connected = "", false
		if !waitTeamRetry(ctx) {
			return
		}
	}
}

// readChorus reads the project's swarm, waiting up to chorusViewWait for it
// to differ from the view named by etag.
func (s *Server) readChorus(ctx context.Context, etag string) (*genchorus.SwarmView, error) {
	chorus, err := s.team.chorus.client(ctx, s.team.dependencies.ConnectChorus)
	if err != nil {
		return nil, err
	}
	record, _ := s.currentTeam()
	payload := &genchorus.ViewPayload{Swarm: record.Team, Wait: chorusViewWait}
	if etag != "" {
		payload.IfNoneMatch = &etag
	}
	view, err := chorus.View(ctx, payload)
	if err != nil && !notModified(err) {
		s.team.chorus.forget(chorus)
	}
	return view, err
}

// followAssignments reads the open build's assignments at start, whenever
// the terminals or the swarm changed, and every teamAssignmentInterval.
func (s *Server) followAssignments(ctx context.Context, read controlplane.AssignmentReader, changed <-chan struct{}) {
	ticker := time.NewTicker(teamAssignmentInterval)
	defer ticker.Stop()
	for {
		s.refreshAssignments(ctx, read)
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-ticker.C:
		}
	}
}

// refreshAssignments records the open build's assignments, ends the workers
// of a build that was merged or dropped, and tells each worker in Chorus
// about an assignment it has not read.
func (s *Server) refreshAssignments(ctx context.Context, read controlplane.AssignmentReader) {
	assignments, err := read(ctx)
	if err != nil {
		s.team.observed.assignmentsRead(controlplane.TeamAssignments{}, false)
		return
	}
	s.team.observed.assignmentsRead(assignments, true)
	if build := assignments.BuildIdentity; build != nil && (assignments.Merged || assignments.Dropped) {
		if err := s.StopWorkers(ctx, *build); err != nil && ctx.Err() == nil {
			teamOutage(ctx, "highbay.team.stop_workers_failed", "hc", err)
		}
	}
	s.notifyAssignments(ctx, assignments)
}

// notifyAssignments posts one Chorus message per unread assignment
// generation, mentioning the worker that owes the read. Chorus wakes an idle
// agent for a mention. A worker that has not joined the channel yet is told
// on a later pass: a post made before it joins would never reach it.
func (s *Server) notifyAssignments(ctx context.Context, assignments controlplane.TeamAssignments) {
	record, roles := s.currentTeam()
	channel := "#" + record.Team
	for _, owed := range assignments.Unread {
		var worker teamRole
		for _, role := range roles {
			if role.Role == owed.Role {
				worker = role
			}
		}
		text := assignmentNotice(owed.Role)
		if worker.Handle == "" || text == "" || s.team.observed.notifiedOf(owed) || !s.team.observed.listening(worker.Handle, channel) {
			continue
		}
		chorus, err := s.team.chorus.client(ctx, s.team.dependencies.ConnectChorus)
		if err != nil {
			return
		}
		title := "Highbay"
		_, err = chorus.PostMessage(ctx, &genchorus.PostMessagePayload{
			Swarm: record.Team, Sender: highbayHandle(record), Kind: "service", Role: &title,
			To: channel, Text: "@" + worker.Handle + " " + text,
		})
		_, span := otel.Tracer("highbay/project").Start(ctx, "highbay.team.assignment_notice")
		span.SetAttributes(attribute.String("team.role", owed.Role))
		if err != nil {
			s.team.chorus.forget(chorus)
			// Chorus's own text stays out of the span.
			span.RecordError(errors.New("assignment notice could not be posted"))
			span.SetStatus(codes.Error, "post assignment notice")
			span.End()
			return
		}
		span.End()
		s.team.observed.markNotified(owed)
	}
}

// terminalsSeen reports whether the terminal subscription is established.
func (o *teamObservation) terminalsSeen() bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.terminalsKnown
}

// notifiedOf reports whether Highbay already posted a notice for owed.
func (o *teamObservation) notifiedOf(owed controlplane.AssignmentGeneration) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.notified[owed.Role] == owed.Generation
}

// markNotified records that Highbay posted a notice for owed.
func (o *teamObservation) markNotified(owed controlplane.AssignmentGeneration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.notified == nil {
		o.notified = make(map[string]string)
	}
	o.notified[owed.Role] = owed.Generation
}
