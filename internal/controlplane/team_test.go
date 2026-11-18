// This file tests the generated user boundary, assignment owner and supervisor shutdown.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// teamServiceFixture models the narrow owner without any outside effect.
type teamServiceFixture struct {
	mu        sync.Mutex
	snapshot  gensurla.TeamSnapshot
	calls     []string
	bound     AssignmentReader
	supervise func(context.Context, AssignmentReader) error
	// Action hooks return owner refusals without a window or external process.
	show     func(context.Context, string) error
	register func(context.Context, BuildFolders) error
	start    func(context.Context) (*gensurla.ProjectTeam, error)
}

// ReadTeam returns a copied snapshot and records a read, never an assignment mutation.
func (f *teamServiceFixture) ReadTeam(context.Context) (*gensurla.TeamSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "read")
	copy := f.snapshot
	return &copy, nil
}

// ShowPane records the authorized requested role.
func (f *teamServiceFixture) ShowPane(ctx context.Context, role string) error {
	f.mu.Lock()
	f.calls = append(f.calls, "show:"+role)
	show := f.show
	f.mu.Unlock()
	if show != nil {
		return show(ctx, role)
	}
	return nil
}

// StartAgents records the authorized action without starting any process.
func (f *teamServiceFixture) StartAgents(ctx context.Context) (*gensurla.ProjectTeam, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "start")
	start := f.start
	f.mu.Unlock()
	if start != nil {
		return start(ctx)
	}
	return &gensurla.ProjectTeam{Team: "team", Space: "space", Dialog: "opened", Step: "Choose the fixture's builder in its open team dialog.", Roles: []*gensurla.ProjectTeamRole{}}, nil
}

// RegisterBuildRoles records registration without changing a window or starting any process.
func (f *teamServiceFixture) RegisterBuildRoles(ctx context.Context, folders BuildFolders) error {
	f.mu.Lock()
	f.calls = append(f.calls, "register")
	register := f.register
	f.mu.Unlock()
	if register != nil {
		return register(ctx, folders)
	}
	return nil
}

// BindAssignments records the assignment reader before supervision starts.
func (f *teamServiceFixture) BindAssignments(read AssignmentReader) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bound = read
}

// Supervise is a bounded stand-in that ends when its owner does.
func (f *teamServiceFixture) Supervise(ctx context.Context, read AssignmentReader) error {
	if f.supervise != nil {
		return f.supervise(ctx, read)
	}
	<-ctx.Done()
	return nil
}

// testTeamBinding supplies an independent team with no roles assigned yet.
func testTeamBinding() *TeamBinding {
	return &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{
		snapshot: gensurla.TeamSnapshot{Team: "test-team", Roles: []*gensurla.TeamRoleState{}},
	}}
}

// TestTeamUserTransport enforces user-only access before the owner sees a call,
// including management, and validates the role with the generated decoder.
func TestTeamUserTransport(t *testing.T) {
	for _, kind := range []CallerKind{CallerUser, CallerGuide, CallerBuilder, CallerTestWriter, CallerManagement} {
		t.Run(string(kind), func(t *testing.T) {
			f := &teamServiceFixture{snapshot: gensurla.TeamSnapshot{Team: "team", Roles: []*gensurla.TeamRoleState{}}}
			cp := newControlPlane(t, Options{Team: &TeamBinding{Owner: "owner", Service: f}})
			handler := NewHTTPHandler(cp, func(*http.Request) (Principal, error) { return Principal{ID: "owner", Kind: kind}, nil })
			for _, route := range []struct{ method, path, body string }{{"GET", "/team", ""}, {"POST", "/team/pane", `{"role":"builder"}`}, {"POST", "/team/agents", ""}} {
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(route.method, route.path, strings.NewReader(route.body)))
				if kind == CallerUser {
					assert.Less(t, response.Code, 300, response.Body.String())
				} else {
					assert.Equal(t, 403, response.Code, response.Body.String())
				}
			}
			if kind == CallerUser {
				assert.Equal(t, []string{"read", "show:builder", "start"}, f.calls)
			} else {
				assert.Empty(t, f.calls)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest("POST", "/team/pane", strings.NewReader(`{"role":"other"}`)))
			assert.Equal(t, 400, response.Code)
		})
	}
	for _, tc := range []struct {
		name, path, body, call string
		status                 int
		owner                  *teamServiceFixture
	}{
		{
			name: "window_busy", path: "/team/pane", body: `{"role":"builder"}`,
			call: "show:builder", status: http.StatusConflict,
			owner: &teamServiceFixture{show: func(context.Context, string) error {
				return gensurla.MakeWindowBusy(errors.New("close the open Herdr dialog before showing a pane"))
			}},
		},
		{
			name: "team_unavailable", path: "/team/agents", call: "start", status: http.StatusServiceUnavailable,
			owner: &teamServiceFixture{start: func(context.Context) (*gensurla.ProjectTeam, error) {
				return nil, gensurla.MakeTeamUnavailable(errors.New("open the Herdr app, then try again"))
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := newControlPlane(t, Options{Team: &TeamBinding{Owner: "owner", Service: tc.owner}})
			handler := NewHTTPHandler(cp, func(*http.Request) (Principal, error) {
				return Principal{ID: "owner", Kind: CallerUser}, nil
			})
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			assert.Equal(t, tc.status, response.Code, response.Body.String())
			assert.Equal(t, tc.name, response.Header().Get("goa-error"))
			var refusal struct{ Name string }
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &refusal))
			assert.Equal(t, tc.name, refusal.Name)
			assert.Equal(t, []string{tc.call}, tc.owner.calls)
		})
	}
}

// TestTeamUserStartConflictTransport maps the owner's numeric conflict into the
// ordinary user error with one actionable suffix, never peer-authored text.
func TestTeamUserStartConflictTransport(t *testing.T) {
	f := &teamServiceFixture{start: func(context.Context) (*gensurla.ProjectTeam, error) {
		return nil, fmt.Errorf("private peer output: %w", &genproject.TeamSpaceConflict{
			Name: "ambiguous_space", Count: 2,
		})
	}}
	cp := newControlPlane(t, Options{Team: &TeamBinding{Owner: "owner", Service: f}})
	handler := NewHTTPHandler(cp, func(*http.Request) (Principal, error) {
		return Principal{ID: "owner", Kind: CallerUser}, nil
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/team/agents", nil))
	assert.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	var body struct {
		Name    string `json:"name"`
		Message string `json:"message"`
	}
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
	assert.Equal(t, "ambiguous_space", body.Name)
	assert.Equal(t, "2 Herdr spaces match this project. Close the duplicate Herdr spaces, then start the agents.", body.Message)
	assert.True(t, strings.HasSuffix(body.Message, duplicateTeamSpacesStep()))
	assert.NotContains(t, response.Body.String(), "private peer output")
}

// TestTeamAgentChangeListDescription exposes only readable facts to an agent;
// its opaque version must not instruct the agent to read the user-only team API.
func TestTeamAgentChangeListDescription(t *testing.T) {
	cp := newControlPlane(t, Options{})
	server := httptest.NewServer(NewHTTPHandler(cp, fixtureCredentials(map[string]Principal{"builder": {ID: "owner", Kind: CallerBuilder}})))
	defer server.Close()
	raw := mcpRequest(t, server.URL, "builder", "tools/list", "", `{}`)
	var catalog struct {
		Tools []struct {
			Name         string          `json:"name"`
			Description  string          `json:"description"`
			OutputSchema json.RawMessage `json:"outputSchema"`
		} `json:"tools"`
	}
	require.NoError(t, json.Unmarshal(raw, &catalog))
	found := false
	for _, tool := range catalog.Tools {
		assert.NotContains(t, []string{"get_team", "show_team_pane", "start_team_agents"}, tool.Name)
		if tool.Name == "list_changes" {
			found = true
			assert.NotContains(t, tool.Description, "get_team")
			assert.NotContains(t, string(tool.OutputSchema), "team snapshot")
			assert.Contains(t, string(tool.OutputSchema), "Compare it only for equality")
		}
	}
	assert.True(t, found)
}

// TestTeamUserResourceOwner refuses another user principal before the single
// bound project owner sees any read or action, independently of caller purpose.
func TestTeamUserResourceOwner(t *testing.T) {
	f := &teamServiceFixture{}
	cp := newControlPlane(t, Options{Team: &TeamBinding{Owner: "alice", Service: f}})
	handler := NewHTTPHandler(cp, func(*http.Request) (Principal, error) {
		return Principal{ID: "bob", Kind: CallerUser}, nil
	})
	for _, request := range []struct{ method, path, body string }{
		{"GET", "/team", ""}, {"POST", "/team/pane", `{"role":"builder"}`}, {"POST", "/team/agents", ""},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(request.method, request.path, strings.NewReader(request.body)))
		assert.Equal(t, http.StatusForbidden, response.Code)
		assert.NotContains(t, response.Body.String(), "alice")
		assert.NotContains(t, response.Body.String(), "bob")
	}
	assert.Empty(t, f.calls)
}

// TestTeamVersionBelongsToResourceOwner keeps other owners' change-list waits
// independent of the bound team's activity and never reads that team for them.
func TestTeamVersionBelongsToResourceOwner(t *testing.T) {
	f := &teamServiceFixture{snapshot: gensurla.TeamSnapshot{Team: "team", Roles: []*gensurla.TeamRoleState{}}}
	cp := newControlPlane(t, Options{Team: &TeamBinding{Owner: "alice", Service: f}})
	cp.changesWait = time.Millisecond
	alice, bob := asCaller("alice", CallerUser), asCaller("bob", CallerUser)
	a, err := cp.ListChanges(alice)
	require.NoError(t, err)
	b, err := cp.ListChanges(bob)
	require.NoError(t, err)
	assert.Equal(t, []string{"read"}, f.calls)
	f.mu.Lock()
	f.snapshot.Roles = []*gensurla.TeamRoleState{{Role: "builder", Handle: "builder", State: "waiting", Attention: "none", Watcher: "gone", Assignment: "unread"}}
	f.calls = nil
	f.mu.Unlock()
	bobWait, err := cp.WaitForChanges(bob, &gensurla.WaitForChangesPayload{Since: b.Version})
	require.NoError(t, err)
	assert.Equal(t, b.Version, bobWait.Version)
	bobList, err := cp.ListChanges(bob)
	require.NoError(t, err)
	assert.Equal(t, b.Version, bobList.Version)
	assert.Empty(t, f.calls, "other owners never read the bound team's state")
	aliceWait, err := cp.WaitForChanges(alice, &gensurla.WaitForChangesPayload{Since: a.Version})
	require.NoError(t, err)
	assert.NotEqual(t, a.Version, aliceWait.Version)
}

// TestTeamAssignmentGenerations reads only the binding's owner, preserves stale
// revision debt, and proves neither inspection nor a failed read marks it read.
func TestTeamAssignmentGenerations(t *testing.T) {
	f := &teamServiceFixture{}
	opts := Options{CardsDir: t.TempDir(), WorkspaceDir: t.TempDir(),
		Team: &TeamBinding{Owner: "alice@example.com", Service: f}}
	cp := newBuildControlPlane(t, opts)
	card := buildableChange(t, cp, "alice@example.com")
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	require.NoError(t, cp.Close())
	cp = newBuildControlPlane(t, opts)
	first, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, first.BuilderHasWork)
	assert.True(t, first.TestWriterHasWork)
	require.Len(t, first.Unread, 2)
	assert.Equal(t, "builder", first.Unread[0].Role)
	assert.Len(t, first.Unread[0].Generation, 64)
	second, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.Equal(t, first, second)
	builds, err := cp.ownedBuilds("alice@example.com")
	require.NoError(t, err)
	assert.False(t, builds[0].file.Builder.Read)
	assert.False(t, builds[0].file.TestWriter.Read)
	// A later request revision does not automatically erase an earlier assignment.
	cp.mu.Lock()
	record, found, err := cp.cards.load("alice@example.com")
	require.NoError(t, err)
	require.True(t, found)
	record.card.Prompt = "Changed request"
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
	require.NoError(t, err)
	require.NoError(t, cp.cards.save(record))
	cp.mu.Unlock()
	stale, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.Equal(t, first, stale)
	makeBuildFixtureBuilderCopy(t, cp)
	_, err = cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder), &gensurla.GetBuilderAssignmentPayload{ChangeID: "alice@example.com"})
	require.NoError(t, err)
	owed, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, owed.BuilderHasWork, "reading an assignment leaves the builder with work until a run passes")
	assert.True(t, owed.TestWriterHasWork)
	require.Len(t, owed.Unread, 1)
	assert.Equal(t, "test_writer", owed.Unread[0].Role)
	// Owner read errors remain errors, rather than an empty successful owed set.
	name := filepath.Join(cp.cards.dir, openBuildRecordFile)
	original, err := os.ReadFile(name)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(name, []byte("broken"), 0600))
	_, err = cp.assignmentGenerations(t.Context())
	assert.Error(t, err)
	require.NoError(t, os.WriteFile(name, original, 0600))
}

// TestTeamAssignmentGenerationsMatchBuildWork uses the saved build's actual
// projection: a stored evaluator has no test-writer work, but still owes the
// builder's read. Ordinary projects continue to owe both unread assignments.
func TestTeamAssignmentGenerationsMatchBuildWork(t *testing.T) {
	for _, stored := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary project", true: "stored evaluator"}[stored], func(t *testing.T) {
			opts := Options{Team: &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{}}}
			if stored {
				opts.BuildCacheDir = t.TempDir()
				opts.Verification = &VerificationConfig{RunsDir: t.TempDir()}
			}
			cp := newBuildControlPlane(t, opts)
			card := buildableChange(t, cp, "chg")
			user := asCaller("alice@example.com", CallerUser)
			started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
			require.NoError(t, err)
			read, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "chg"})
			require.NoError(t, err)
			build, ok := read.Build.AsStarted()
			require.True(t, ok)
			assert.Equal(t, started.Build, build)
			wantTests := gensurla.TestsKindAssigned
			wantRoles := []string{"builder", "test_writer"}
			if stored {
				wantTests = gensurla.TestsKindNotUsed
				wantRoles = []string{"builder"}
			}
			assert.Equal(t, wantTests, build.TestWriter.Tests.Kind())
			owed, err := cp.assignmentGenerations(t.Context())
			require.NoError(t, err)
			assert.True(t, owed.BuilderHasWork)
			assert.Equal(t, !stored, owed.TestWriterHasWork)
			roles := make([]string, 0, len(owed.Unread))
			for _, item := range owed.Unread {
				roles = append(roles, item.Role)
			}
			assert.Equal(t, wantRoles, roles)
			saved, err := cp.ownedBuilds("alice@example.com")
			require.NoError(t, err)
			require.Len(t, saved, 1)
			assert.False(t, saved[0].file.Builder.Read)
			assert.False(t, saved[0].file.TestWriter.Read, "unused is derived, never a fabricated read")
			makeBuildFixtureBuilderCopy(t, cp)
			_, err = cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder), &gensurla.GetBuilderAssignmentPayload{ChangeID: "chg"})
			require.NoError(t, err)
			remaining, err := cp.assignmentGenerations(t.Context())
			require.NoError(t, err)
			assert.True(t, remaining.BuilderHasWork)
			assert.Equal(t, !stored, remaining.TestWriterHasWork)
			if stored {
				assert.Empty(t, remaining.Unread)
			} else {
				require.Len(t, remaining.Unread, 1)
				assert.Equal(t, "test_writer", remaining.Unread[0].Role)
			}
		})
	}
}

// TestTeamSupervisorOwnedShutdown proves startup admission, cancel and drain
// happen even without Live configured, and failures have a bounded retry delay.
func TestTeamSupervisorOwnedShutdown(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain", true: "early return"}[early], func(t *testing.T) {
			started := make(chan struct{}, 2)
			stopped := make(chan struct{})
			f := &teamServiceFixture{}
			f.supervise = func(ctx context.Context, read AssignmentReader) error {
				if early {
					started <- struct{}{}
					return assert.AnError
				}
				_, err := read(ctx)
				assert.NoError(t, err)
				started <- struct{}{}
				<-ctx.Done()
				close(stopped)
				return nil
			}
			cp, err := New(Options{CardsDir: t.TempDir(), WorkspaceDir: realTempDir(t), Team: &TeamBinding{Owner: "owner", Service: f}})
			require.NoError(t, err)
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("supervisor did not start")
			}
			require.NoError(t, cp.Close())
			if !early {
				select {
				case <-stopped:
				default:
					t.Fatal("close returned before supervisor drained")
				}
			}
			assert.Empty(t, started, "unexpected immediate retry")
		})
	}
}

// TestTeamSupervisorNormalOwnerCompletion ends the worker without a failure
// or a retry when the project's independent lifetime finishes before CP.Close.
func TestTeamSupervisorNormalOwnerCompletion(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	started, lifetime := make(chan struct{}, 2), make(chan struct{})
	f := &teamServiceFixture{supervise: func(context.Context, AssignmentReader) error {
		started <- struct{}{}
		<-lifetime
		return nil
	}}
	cp := newControlPlane(t, Options{TracerProvider: provider, Team: &TeamBinding{Owner: "owner", Service: f}})
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	close(lifetime)
	drained := make(chan struct{})
	go func() { cp.teamWorker.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("normal project completion did not drain CP worker")
	}
	assert.NoError(t, cp.serverCtx.Err(), "the control plane remains live")
	assert.Empty(t, started, "normal completion must not restart the owner")
	assert.Empty(t, exporter.GetSpans(), "normal completion must not fabricate an error span")
}

// TestTeamWaitingVersionAndHistory proves the existing wait covers every public
// team fact and neither its request nor its inner span reaches bounded history.
func TestTeamWaitingVersionAndHistory(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	f := &teamServiceFixture{snapshot: gensurla.TeamSnapshot{Team: "team", Roles: []*gensurla.TeamRoleState{{Role: "builder", Handle: "builder", State: "working", Attention: "none", Watcher: "held", Assignment: "nothing_owed"}}}}
	cp := newControlPlane(t, Options{TracerProvider: provider, Team: &TeamBinding{Owner: "owner", Service: f}})
	cp.changesWait = time.Millisecond
	user := asCaller("owner", CallerUser)
	list, err := cp.ListChanges(user)
	require.NoError(t, err)
	endpoints := gensurla.NewEndpoints(cp)
	applyOperationPolicy(endpoints, cp.tracer("test"))
	for _, field := range []string{"state", "attention", "watcher", "assignment"} {
		f.mu.Lock()
		old := f.snapshot.Roles[0]
		next := *old
		switch field {
		case "state":
			next.State = "needs_you"
		case "attention":
			next.Attention = "permission"
		case "watcher":
			next.Watcher = "gone"
		case "assignment":
			next.Assignment = "unread"
		}
		f.snapshot.Roles = []*gensurla.TeamRoleState{&next}
		f.mu.Unlock()
		result, err := endpoints.WaitForChanges(user, &gensurla.WaitForChangesPayload{Since: list.Version})
		require.NoError(t, err)
		assert.NotEqual(t, list.Version, result.(*gensurla.ChangeListVersion).Version)
		list, err = cp.ListChanges(user)
		require.NoError(t, err)
	}
	traces, err := cp.traces.List("owner", "all", 100, nil)
	require.NoError(t, err)
	raw, err := json.Marshal(traces)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "wait_for_changes")
	var names []string
	for _, span := range exporter.GetSpans() {
		names = append(names, span.Name)
	}
	assert.Contains(t, names, waitForChangesSpan)
	assert.Contains(t, names, "surla.request.wait_for_changes")
}

// TestTeamBindAssignmentsBeforeSupervise proves that controlplane.New binds
// its assignment reader synchronously before starting the background
// supervision worker, so a team request arriving before Supervise runs still
// reads the control plane's assignments.
func TestTeamBindAssignmentsBeforeSupervise(t *testing.T) {
	superviseGate := make(chan struct{})
	f := &teamServiceFixture{}
	f.supervise = func(ctx context.Context, _ AssignmentReader) error {
		<-superviseGate
		f.mu.Lock()
		f.calls = append(f.calls, "supervise")
		f.mu.Unlock()
		<-ctx.Done()
		return nil
	}
	cp, err := New(Options{CardsDir: t.TempDir(), WorkspaceDir: realTempDir(t), Team: &TeamBinding{Owner: "alice@example.com", Service: f}})
	require.NoError(t, err)
	t.Cleanup(func() {
		close(superviseGate)
		assert.NoError(t, cp.Close())
	})

	f.mu.Lock()
	bound := f.bound
	calls := append([]string(nil), f.calls...)
	f.mu.Unlock()
	require.NotNil(t, bound, "BindAssignments must run synchronously before New returns")
	assert.Empty(t, calls, "BindAssignments must not wait for Supervise")
	got, err := bound(t.Context())
	require.NoError(t, err)
	assert.False(t, got.BuilderHasWork)
	assert.False(t, got.TestWriterHasWork)
	assert.Empty(t, got.Unread)
}

// TestWhoHasWorkAndAssignmentLists checks who has work across no build, a
// dropped build, every test-writer state, builder verification runs (before
// start, another change or revision, failed, not completed, passed, and
// unreadable), approval with and without promotion settings, building again,
// and a written-tests build read while stored verification settings are
// configured. List operations and Unread hold only roles with work, while
// single assignment reads by change_id stay readable after acceptance or a
// passed run.
func TestWhoHasWorkAndAssignmentLists(t *testing.T) {
	runsDir := t.TempDir()
	reportDir := t.TempDir()
	cp := newBuildControlPlane(t, Options{BuildCacheDir: t.TempDir(),
		RunsDir: runsDir,
		WrittenTestRuns: &WrittenTestRuns{
			ReportDir: reportDir,
			Command:   []string{"/bin/sh", "-c", "exit 0", "sh"},
			Timeout:   time.Hour,
		},
	})
	commit := productionBuildGit(t, cp.activeWorkspaceDir, "rev-parse", "HEAD")
	suiteCommit := commit
	user := asCaller("alice@example.com", CallerUser)
	builder := asCaller("alice@example.com", CallerBuilder)
	writer := asCaller("alice@example.com", CallerTestWriter)

	// 1. Before any build starts, neither role has work.
	none, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.False(t, none.BuilderHasWork)
	assert.False(t, none.TestWriterHasWork)
	assert.False(t, none.HasWork("builder"))
	assert.False(t, none.HasWork("test_writer"))
	assert.False(t, none.HasWork("guide"))
	assert.Empty(t, none.Unread)
	bList, err := cp.ListBuilderAssignments(builder)
	require.NoError(t, err)
	assert.Empty(t, bList.Assignments)
	wList, err := cp.ListTestWriterAssignments(writer)
	require.NoError(t, err)
	assert.Empty(t, wList.Assignments)

	// 2. Starting a build gives both roles work; dropping it clears both and
	// refuses single reads with not_found.
	dropCard := buildableChange(t, cp, "dropped-chg")
	droppedStart, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: dropCard.ChangeID, RequestRevision: dropCard.RequestRevision,
	})
	require.NoError(t, err)
	active, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, active.BuilderHasWork)
	assert.True(t, active.TestWriterHasWork)
	require.Len(t, active.Unread, 2)

	_, err = cp.DropBuild(user, &gensurla.DropBuildPayload{
		ChangeID: dropCard.ChangeID, StartedAt: droppedStart.Build.StartedAt,
	})
	require.NoError(t, err)
	afterDrop, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.False(t, afterDrop.BuilderHasWork)
	assert.False(t, afterDrop.TestWriterHasWork)
	assert.Empty(t, afterDrop.Unread)
	bList, err = cp.ListBuilderAssignments(builder)
	require.NoError(t, err)
	assert.Empty(t, bList.Assignments)
	wList, err = cp.ListTestWriterAssignments(writer)
	require.NoError(t, err)
	assert.Empty(t, wList.Assignments)
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: dropCard.ChangeID})
	assertRefused(t, err, "no_build")
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: dropCard.ChangeID})
	assertRefused(t, err, "no_build")

	// 3. Start a new build after the drop and check every test-writer state:
	// assigned, reported, needs_changes, and could_not_run all count as work;
	// accepted does not.
	card := buildableChange(t, cp, "chg")
	provenance, found, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	require.True(t, found)
	meta := loadMetadataRecord(t, cp, "chg")
	meta.madeFrom.Start = provenance.Start
	meta.madeFrom.Entries = nil
	saveMetadataRecord(t, cp, meta)
	started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	makeBuildFixtureBuilderCopy(t, cp)
	startedAt, err := time.Parse(time.RFC3339Nano, started.Build.StartedAt)
	require.NoError(t, err)

	// Reading the test writer's assignment clears Unread for test_writer while
	// keeping TestWriterHasWork and ListTestWriterAssignments non-empty.
	_, err = cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "chg"})
	require.NoError(t, err)
	afterWriterRead, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, afterWriterRead.TestWriterHasWork)
	require.Len(t, afterWriterRead.Unread, 1)
	assert.Equal(t, "builder", afterWriterRead.Unread[0].Role)
	wList, err = cp.ListTestWriterAssignments(writer)
	require.NoError(t, err)
	require.Len(t, wList.Assignments, 1)

	checkedAt := startedAt.Add(time.Second).UTC()
	for _, tc := range []struct {
		name string
		tw   testWriterAssignment
		work bool
	}{
		{
			name: "assigned",
			tw:   testWriterAssignment{State: testsAssigned, Read: false},
			work: true,
		},
		{
			name: "reported",
			tw:   testWriterAssignment{State: testsReported, Read: true},
			work: true,
		},
		{
			name: "needs_changes",
			tw:   testWriterAssignment{State: testsNeedChanges, Read: false, Findings: []string{"missing hook"}, CheckedAt: checkedAt},
			work: true,
		},
		{
			name: "could_not_run",
			tw:   testWriterAssignment{State: testsCouldNotRun, Read: false, Reason: "git failed", Step: stepFailure, CheckedAt: checkedAt},
			work: true,
		},
		{
			name: "accepted",
			tw:   testWriterAssignment{State: testsAccepted, Read: false, Findings: []string{}, Commit: suiteCommit, CheckedAt: checkedAt},
			work: false,
		},
	} {
		t.Run("test_writer_"+tc.name, func(t *testing.T) {
			saveTestWriter(t, cp, "chg", tc.tw)
			got, err := cp.assignmentGenerations(t.Context())
			require.NoError(t, err)
			assert.Equal(t, tc.work, got.TestWriterHasWork)
			assert.Equal(t, tc.work, got.HasWork("test_writer"))
			var unreadRoles []string
			for _, item := range got.Unread {
				unreadRoles = append(unreadRoles, item.Role)
			}
			if tc.work && !tc.tw.Read {
				assert.Contains(t, unreadRoles, "test_writer")
			} else {
				assert.NotContains(t, unreadRoles, "test_writer")
			}
			listed, err := cp.ListTestWriterAssignments(writer)
			require.NoError(t, err)
			if tc.work {
				require.Len(t, listed.Assignments, 1)
				assert.Equal(t, "chg", listed.Assignments[0].ChangeID)
			} else {
				assert.Empty(t, listed.Assignments)
			}
			full, err := cp.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "chg"})
			require.NoError(t, err, "single read by change_id stays readable in %s", tc.name)
			assert.Equal(t, "chg", full.ChangeID)
		})
	}

	// Leave tests accepted and mark the builder's assignment read first, then
	// check that reading leaves the builder with work until a verification run
	// of the open build passes.
	acceptTests(t, cp, "chg", suiteCommit)
	_, err = cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "chg"})
	require.NoError(t, err)
	builderTree := productionBuildGit(t, cp.activeWorkspaceDir, "write-tree")
	commit = productionBuildGit(
		t, cp.activeWorkspaceDir, "commit-tree", "--no-gpg-sign", builderTree, "-p", suiteCommit, "-m", "Builder candidate",
	)
	productionBuildGit(t, cp.activeWorkspaceDir, "update-ref", "refs/heads/surla/build-2", commit)
	afterBuilderRead, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, afterBuilderRead.BuilderHasWork)
	assert.False(t, afterBuilderRead.TestWriterHasWork)
	assert.Empty(t, afterBuilderRead.Unread)
	bList, err = cp.ListBuilderAssignments(builder)
	require.NoError(t, err)
	require.Len(t, bList.Assignments, 1)

	spec := reportSpec{
		SuiteID: "shop_acceptance", SuiteCommit: suiteCommit, Environment: "sim",
		Artifacts: map[string]string{"shop": strings.Repeat("a", 64)},
	}
	saveRun := func(changeID, revision string, acceptedAt time.Time, resultJSON string) string {
		runID := verifyrun.NewRunID()
		dir := filepath.Join(runsDir, runID)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		plan := verify.Plan{
			CandidateRepo: "/repos/candidate", CandidateCommit: commit, CandidateModule: ".",
			Binaries:  map[string]string{"shop": "./cmd/shop"},
			SuiteRepo: "/repos/suite", SuiteCommit: suiteCommit, SuiteModule: "eval",
			SuiteCommand: "./gen/cmd/shop-evals", RequestRevision: revision,
			ReportDir: reportDir, WorkDir: filepath.Join(dir, "work"), Tests: verify.TestsWritten,
		}
		raw, err := json.Marshal(verifyrun.Request{
			RunID: runID, Owner: "alice@example.com", ChangeID: changeID,
			AcceptedAt: acceptedAt.UTC(), Timeout: time.Hour, Plan: plan,
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), raw, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600))
		if resultJSON != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "result.json"), []byte(resultJSON), 0o600))
		}
		return runID
	}

	passBefore := passingReport(t, card.RequestRevision, commit, spec)
	writeReportFile(t, reportDir, passBefore)
	// A passing run accepted before StartedAt does not finish the open build.
	saveRun("chg", card.RequestRevision, startedAt.Add(-time.Second), `{"kind":"report","report_digest":"`+passBefore.Digest+`"}`)
	// A passing run for another change does not finish the open build.
	saveRun("other", card.RequestRevision, startedAt.Add(time.Millisecond), `{"kind":"report","report_digest":"`+passBefore.Digest+`"}`)
	// A passing run for another revision does not finish the open build.
	otherRev := strings.Repeat("e", 64)
	passOtherRev := passingReport(t, otherRev, commit, spec)
	writeReportFile(t, reportDir, passOtherRev)
	saveRun("chg", otherRev, startedAt.Add(2*time.Millisecond), `{"kind":"report","report_digest":"`+passOtherRev.Digest+`"}`)
	// A failed report and an execution_failed run for the open build still leave the builder with work.
	failRep := failingReport(t, card.RequestRevision, commit, spec)
	writeReportFile(t, reportDir, failRep)
	saveRun("chg", card.RequestRevision, startedAt.Add(3*time.Millisecond), `{"kind":"report","report_digest":"`+failRep.Digest+`"}`)
	saveRun("chg", card.RequestRevision, startedAt.Add(4*time.Millisecond), `{"kind":"no_report","failure":"execution_failed","stage":"build","summary":"build failed"}`)

	stillWorking, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, stillWorking.BuilderHasWork, "runs before StartedAt, for another change/revision, or failed leave the builder with work")
	bList, err = cp.ListBuilderAssignments(builder)
	require.NoError(t, err)
	require.Len(t, bList.Assignments, 1)

	// Mark the builder's assignment unread on disk, then record a passing run
	// accepted at/after StartedAt: the builder now has no work, its list is
	// empty, Unread omits it even though Read is false, and GetBuilderAssignment
	// by change_id stays readable.
	editBuildFile(t, cp, "chg", func(f map[string]any) map[string]any {
		f["builder"].(map[string]any)["read"] = false
		return f
	})
	saveRun("chg", card.RequestRevision, startedAt.Add(5*time.Millisecond), `{"kind":"report","report_digest":"`+passBefore.Digest+`"}`)

	afterPass, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.False(t, afterPass.BuilderHasWork)
	assert.False(t, afterPass.TestWriterHasWork)
	assert.Empty(t, afterPass.Unread, "a role without work is never in Unread")
	bList, err = cp.ListBuilderAssignments(builder)
	require.NoError(t, err)
	assert.Empty(t, bList.Assignments)
	bFull, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "chg"})
	require.NoError(t, err, "GetBuilderAssignment by change_id stays readable after a passed run")
	assert.Equal(t, "chg", bFull.ChangeID)
	assert.Equal(t, card.RequestRevision, bFull.RequestRevision)
	assert.True(t, bFull.Read)

	// Approval changes nothing with or without promotion settings.
	_, err = cp.ApproveMerge(user, &gensurla.ApproveMergePayload{
		ChangeID: "chg", RequestRevision: card.RequestRevision, CandidateCommit: commit, ReportDigest: passBefore.Digest,
	})
	require.NoError(t, err)
	cp.promotionTarget = &promotionTargetStub{report: passBefore, artifacts: spec.Artifacts}
	afterApproval, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.False(t, afterApproval.BuilderHasWork)
	assert.False(t, afterApproval.TestWriterHasWork)
	cp.promotionTarget = nil

	// Accepting later tests after a pass makes the earlier pass's inputs no
	// longer current (InputsCurrent=false): the builder has work again, its
	// assignment is listed and unread again, and Unread carries its new
	// generation; then a pass on the new inputs restores no_work.
	tree := productionBuildGit(t, cp.activeWorkspaceDir, "write-tree")
	laterSuiteCommit := productionBuildGit(
		t, cp.activeWorkspaceDir, "commit-tree", "--no-gpg-sign", tree, "-p", commit, "-m", "Accept later tests",
	)
	require.NotEqual(t, suiteCommit, laterSuiteCommit)
	saveTestWriter(t, cp, "chg", testWriterAssignment{
		State: testsAccepted, Read: true, Findings: []string{}, Commit: laterSuiteCommit, CheckedAt: checkedAt.Add(time.Second),
	})
	editBuildFile(t, cp, "chg", func(f map[string]any) map[string]any {
		f["builder"].(map[string]any)["read"] = false
		return f
	})
	afterLaterTests, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, afterLaterTests.BuilderHasWork, "earlier pass ran older tests, so builder has work again")
	assert.False(t, afterLaterTests.TestWriterHasWork)
	require.Len(t, afterLaterTests.Unread, 1)
	assert.Equal(t, "builder", afterLaterTests.Unread[0].Role)
	bList, err = cp.ListBuilderAssignments(builder)
	require.NoError(t, err)
	require.Len(t, bList.Assignments, 1)
	assert.False(t, bList.Assignments[0].Read)
	acceptedView, ok := bList.Assignments[0].WrittenTests.AsAccepted()
	require.True(t, ok)
	assert.Equal(t, laterSuiteCommit, acceptedView.Commit)

	suiteCommit = laterSuiteCommit
	laterSpec := spec
	laterSpec.SuiteCommit = laterSuiteCommit
	passLaterTests := passingReport(t, card.RequestRevision, commit, laterSpec)
	writeReportFile(t, reportDir, passLaterTests)
	saveRun("chg", card.RequestRevision, startedAt.Add(6*time.Millisecond), `{"kind":"report","report_digest":"`+passLaterTests.Digest+`"}`)
	afterSecondPass, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.False(t, afterSecondPass.BuilderHasWork, "a pass on the newly accepted tests finishes the builder's work")
	assert.False(t, afterSecondPass.TestWriterHasWork)
	assert.Empty(t, afterSecondPass.Unread)
	bList, err = cp.ListBuilderAssignments(builder)
	require.NoError(t, err)
	assert.Empty(t, bList.Assignments)

	// Building again starts a new build after the passed run's AcceptedAt,
	// giving both roles work and unread assignments again (even while the card
	// still holds its saved approval).
	time.Sleep(15 * time.Millisecond)
	rebuilt, err := startBuildAgain(t, cp, user, "chg", card.RequestRevision)
	require.NoError(t, err)
	assert.False(t, rebuilt.Build.Builder.Read)
	assert.False(t, rebuilt.Build.TestWriter.Read)
	afterRebuild, err := cp.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, afterRebuild.BuilderHasWork)
	assert.True(t, afterRebuild.TestWriterHasWork)
	require.Len(t, afterRebuild.Unread, 2)
	bList, err = cp.ListBuilderAssignments(builder)
	require.NoError(t, err)
	require.Len(t, bList.Assignments, 1)
	wList, err = cp.ListTestWriterAssignments(writer)
	require.NoError(t, err)
	require.Len(t, wList.Assignments, 1)

	// Reading that written-tests build while stored verification settings are
	// configured never gives the test writer work in either the team read or
	// list_test_writer_assignments.
	cardsDir := cp.cards.dir
	workspaceDir := cp.activeWorkspaceDir
	require.NoError(t, cp.Close())
	configured := newControlPlane(t, Options{BuildCacheDir: t.TempDir(),
		CardsDir:     cardsDir,
		WorkspaceDir: workspaceDir,
		RunsDir:      runsDir,
		Verification: &VerificationConfig{RunsDir: runsDir},
		Team:         &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{}},
	})
	configuredTeam, err := configured.assignmentGenerations(t.Context())
	require.NoError(t, err)
	assert.True(t, configuredTeam.BuilderHasWork)
	assert.False(t, configuredTeam.TestWriterHasWork)
	assert.True(t, configuredTeam.HasWork("builder"))
	assert.False(t, configuredTeam.HasWork("test_writer"))
	require.Len(t, configuredTeam.Unread, 1)
	assert.Equal(t, "builder", configuredTeam.Unread[0].Role)
	bList, err = configured.ListBuilderAssignments(builder)
	require.NoError(t, err)
	require.Len(t, bList.Assignments, 1)
	assert.Equal(t, gensurla.WrittenTestsKindNotUsed, bList.Assignments[0].WrittenTests.Kind())
	wList, err = configured.ListTestWriterAssignments(writer)
	require.NoError(t, err)
	assert.Empty(t, wList.Assignments)

	// An unreadable run record makes both ListBuilderAssignments and
	// assignmentGenerations fail rather than reporting no work.
	corruptDir := filepath.Join(runsDir, verifyrun.NewRunID())
	require.NoError(t, os.MkdirAll(corruptDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(corruptDir, "request.json"), []byte("{"), 0o600))
	_, err = configured.ListBuilderAssignments(builder)
	assert.Error(t, err)
	_, err = configured.assignmentGenerations(t.Context())
	assert.Error(t, err)
}
