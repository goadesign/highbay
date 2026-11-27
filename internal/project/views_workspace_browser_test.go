//go:build browser && (darwin || linux)

// This file exercises the browser-centered workspace controls in real Headless
// Chrome: UpdateBanner (GET/POST /project/update + session-preserving restart
// polling + draft-safe Reload), ConversationRail (POST /team/messages +
// Markdown rendering), and TerminalDrawer (@xterm/xterm WebSocket streaming,
// 256 KiB credit replenishment, Ctrl+Shift+Escape keyboard focus escape, and
// one-click POST /team/roles/{role}/restart).
package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genprojectserver "github.com/goadesign/highbay/gen/http/project/server"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
)

type (
	// workspaceBrowserFacts captures Headless Chrome observations from workspaceScenario.
	workspaceBrowserFacts struct {
		ReleaseHref                   string `json:"release_href"`
		UpdateReady                   bool   `json:"update_ready"`
		DraftSurvivedUpdate           bool   `json:"draft_survived_update"`
		ReplyRendered                 bool   `json:"reply_rendered"`
		TerminalConnected             bool   `json:"terminal_connected"`
		PlainEscapeInsideKeptOpen     bool   `json:"plain_escape_inside_kept_open"`
		FocusEscaped                  bool   `json:"focus_escaped"`
		BuilderKeptAcrossGuideRestart bool   `json:"builder_kept_across_guide_restart"`
		TerminalRestarted             bool   `json:"terminal_restarted"`
		PlainEscapeOutsideClosed      bool   `json:"plain_escape_outside_closed"`
		BrowserCookies                int    `json:"browser_cookies"`
	}

	// workspaceBrowserFixture implements the conversation, terminal WebSocket,
	// role restart, and update endpoints against a real project server session.
	workspaceBrowserFixture struct {
		genhighbay.Service
		t             *testing.T
		mu            sync.Mutex
		version       string
		uiBuild       string
		messages      []*genhighbay.TeamMessage
		sentPayloads  []*genhighbay.SendTeamMessagePayload
		restartedRole []string
		tickets       map[string]string
		ticketCounts  map[string]int
		attachCount   atomic.Int32
		credits       atomic.Int64
		inputs        [][]byte
		appliedUpdate string
	}
)

// TestBrowserWorkspaceConversationTerminalAndUpdateInChromium verifies the
// browser-centered workspace controls in Headless Chrome: UpdateBanner with
// draft-safe session-preserving restart polling, ConversationRail message
// submission and Markdown rendering, and TerminalDrawer WebSocket PTY output,
// credit flow control, Ctrl+Shift+Escape focus escape, and role Restart.
func TestBrowserWorkspaceConversationTerminalAndUpdateInChromium(t *testing.T) {
	executable := findBrowser(t)
	node, err := exec.LookPath("node")
	require.NoError(t, err)
	skipWithoutNodeWebSocket(t, node)
	harness, err := filepath.Abs("../../ui/checkpoint/testing/browser_harness.cjs")
	require.NoError(t, err)

	vp := startViewsProject(t, controlplane.Options{})
	server := vp.server.Load()
	fixture := &workspaceBrowserFixture{
		Service:      vp.cp.Load(),
		t:            t,
		version:      "v0.4.0",
		uiBuild:      controlplane.ViewsPageScriptSHA256,
		tickets:      map[string]string{},
		ticketCounts: map[string]int{},
	}

	appMux := http.NewServeMux()
	appMux.Handle("GET /views", controlplane.ViewsPage())
	cpHandler := controlplane.NewHTTPHandler(fixture, func(r *http.Request) (controlplane.Principal, error) {
		if websocket.IsWebSocketUpgrade(r) && strings.HasPrefix(r.URL.Path, "/team/roles/") {
			if r.Header.Get("Origin") != vp.state.Origin() {
				return controlplane.Principal{}, errors.New("cross-origin websocket refused")
			}
			return controlplane.Principal{
				ID:       vp.state.Record.Owner,
				CallerID: "workspace-ws",
				Kind:     controlplane.CallerUser,
			}, nil
		}
		return server.Authenticate(r)
	})
	appMux.Handle("/", cpHandler)
	baseHandler := server.Handler(appMux)

	// Intercept /project/browser-session and /project/update so the test proves
	// release-gated UpdateBanner discovery and session-preserving version transition
	// without contacting api.github.com or replacing the test process binary.
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/project/browser-session" && r.Method == http.MethodPost:
			var req struct {
				Link string `json:"link"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			res, err := server.ExchangeBrowserLink(r.Context(), &genproject.ExchangeBrowserLinkPayload{Link: req.Link})
			if err != nil {
				http.Error(w, err.Error(), http.StatusUnprocessableEntity)
				return
			}
			fixture.mu.Lock()
			version, uiBuild := fixture.version, fixture.uiBuild
			fixture.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			assert.NoError(t, json.NewEncoder(w).Encode(&genprojectserver.ExchangeBrowserLinkResponseBody{
				Session:     res.Session,
				ProjectName: res.ProjectName,
				Version:     version,
				UIBuild:     uiBuild,
			}))
			return
		case r.URL.Path == "/project/browser-session" && r.Method == http.MethodGet:
			if _, err := server.Authenticate(r); err != nil {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]string{"name": "unauthorized", "message": err.Error()}))
				return
			}
			token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			fixture.mu.Lock()
			version, uiBuild := fixture.version, fixture.uiBuild
			fixture.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			assert.NoError(t, json.NewEncoder(w).Encode(&genprojectserver.GetBrowserSessionResponseBody{
				Session:     token,
				ProjectName: filepath.Base(vp.state.Record.Directory),
				Version:     version,
				UIBuild:     uiBuild,
			}))
			return
		case r.URL.Path == "/project/update" && r.Method == http.MethodGet:
			if _, err := server.Authenticate(r); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			fixture.mu.Lock()
			version := fixture.version
			fixture.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			assert.NoError(t, json.NewEncoder(w).Encode(&genprojectserver.GetUpdateStatusResponseBody{
				CurrentVersion:  version,
				LatestVersion:   "v0.5.0",
				UpdateAvailable: version != "v0.5.0",
				ReleaseURL:      "https://github.com/goadesign/highbay/releases/tag/v0.5.0",
				CanAutoUpdate:   true,
			}))
			return
		case r.URL.Path == "/project/update" && r.Method == http.MethodPost:
			if _, err := server.Authenticate(r); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			var req genprojectserver.ApplyUpdateRequestBody
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Version == nil {
				http.Error(w, "invalid update payload", http.StatusUnprocessableEntity)
				return
			}
			fixture.mu.Lock()
			fixture.appliedUpdate = *req.Version
			fixture.version = "v0.5.0"
			fixture.uiBuild = "updated-ui-build-sha256"
			fixture.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			assert.NoError(t, json.NewEncoder(w).Encode(&genprojectserver.ApplyUpdateResponseBody{
				Status:  "restarting",
				Version: "v0.5.0",
			}))
			return
		default:
			baseHandler.ServeHTTP(w, r)
		}
	})
	var handler http.Handler = wrapped
	vp.handler.Store(&handler)

	link, err := vp.link()
	require.NoError(t, err)
	browser := startBrowser(t, executable, t.TempDir())
	facts := driveBrowser(t, node, harness, browserInput{
		DevTools:  browser.devtools,
		Origin:    vp.state.Origin(),
		Link:      link,
		ChangeIDs: []string{},
		Workspace: true,
	})
	require.NotNil(t, facts.Workspace)
	assert.Equal(t, "https://github.com/goadesign/highbay/releases/tag/v0.5.0", facts.Workspace.ReleaseHref)
	assert.True(t, facts.Workspace.UpdateReady, "UpdateBanner did not poll GET /project/browser-session to ready state")
	assert.True(t, facts.Workspace.DraftSurvivedUpdate, "unsaved ConversationRail input was lost during Update & Restart")
	assert.True(t, facts.Workspace.ReplyRendered, "ConversationRail did not post message and render Markdown reply")
	assert.True(t, facts.Workspace.TerminalConnected, "TerminalDrawer did not attach over WebSocket and render PTY output")
	assert.True(t, facts.Workspace.PlainEscapeInsideKeptOpen, "plain Escape inside xterm closed TerminalDrawer instead of forwarding to the PTY")
	assert.True(t, facts.Workspace.FocusEscaped, "Ctrl+Shift+Escape did not move focus out of xterm to Close terminal")
	assert.True(t, facts.Workspace.BuilderKeptAcrossGuideRestart, "restarting guide disconnected the open builder TerminalDrawer")
	assert.True(t, facts.Workspace.TerminalRestarted, "Restart button did not restart role and reconnect TerminalDrawer")
	assert.True(t, facts.Workspace.PlainEscapeOutsideClosed, "plain Escape on a drawer header control outside xterm did not close TerminalDrawer")
	assert.Zero(t, facts.Workspace.BrowserCookies, "browser cookie store must remain empty")

	assert.GreaterOrEqual(t, fixture.credits.Load(), int64(len("Allow tool? [y/N] ")), "xterm did not replenish stream byte credit")
	fixture.mu.Lock()
	applied := fixture.appliedUpdate
	restartedRoles := append([]string{}, fixture.restartedRole...)
	ticketCounts := map[string]int{}
	for role, count := range fixture.ticketCounts {
		ticketCounts[role] = count
	}
	sentCount := len(fixture.sentPayloads)
	inputs := append([][]byte{}, fixture.inputs...)
	fixture.mu.Unlock()

	assert.Equal(t, "v0.5.0", applied)
	assert.Equal(t, []string{"guide", "builder"}, restartedRoles)
	assert.Equal(t, map[string]int{"builder": 2}, ticketCounts, "guide restart must not request another builder terminal ticket")
	assert.Equal(t, 1, sentCount)
	assert.NotEmpty(t, inputs, "keystrokes typed in xterm were not delivered over WebSocket")
	var flatInputs []byte
	for _, chunk := range inputs {
		flatInputs = append(flatInputs, chunk...)
	}
	assert.Contains(t, string(flatInputs), "\x1b", "plain Escape inside xterm was not forwarded as ESC (0x1b) over WebSocket")
	browser.stopAndCheck(t)
}

// GetTeam returns a connected project team with a waiting Guide and a blocked Builder terminal.
func (f *workspaceBrowserFixture) GetTeam(context.Context) (*genhighbay.TeamSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &genhighbay.TeamSnapshot{
		Team:         "refunds-team",
		Channel:      "#refunds-team",
		ChorusStatus: "connected",
		Roles: []*genhighbay.TeamRoleState{
			{
				Role:        "guide",
				Handle:      "guide-refunds",
				HasTerminal: true,
				State:       "waiting",
				Attention:   "none",
				Assignment:  "no_work",
			},
			{
				Role:        "builder",
				Handle:      "builder-refunds",
				HasTerminal: true,
				State:       "needs_you",
				Attention:   "permission",
				Assignment:  "nothing_owed",
			},
		},
		SavedChoices:     map[string]*genhighbay.RoleAgentChoice{},
		DismissedNotices: []string{},
	}, nil
}

// GetTeamConversation returns the recorded Chorus messages for the workspace test.
func (f *workspaceBrowserFixture) GetTeamConversation(context.Context) (*genhighbay.TeamConversation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msgs := append([]*genhighbay.TeamMessage{}, f.messages...)
	return &genhighbay.TeamConversation{
		Team:         "refunds-team",
		Channel:      "#refunds-team",
		ChorusStatus: "connected",
		Messages:     msgs,
	}, nil
}

// SendTeamMessage records the user's message and appends a Markdown Guide reply.
func (f *workspaceBrowserFixture) SendTeamMessage(
	_ context.Context, p *genhighbay.SendTeamMessagePayload,
) (*genhighbay.TeamMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sentPayloads = append(f.sentPayloads, p)
	now := time.Now().UnixMilli()
	userSeq := len(f.messages) + 1
	userMsg := &genhighbay.TeamMessage{
		ID:         "#refunds-team:1",
		Target:     "#refunds-team",
		Seq:        userSeq,
		Sender:     "you",
		SenderRole: "you",
		Text:       p.Text,
		SentAtMs:   now,
	}
	replySeq := userSeq + 1
	replyMsg := &genhighbay.TeamMessage{
		ID:         "#refunds-team:2",
		Target:     "#refunds-team",
		Seq:        replySeq,
		Sender:     "guide-refunds",
		SenderRole: "guide",
		Text:       "**Draft ready** for review in Agree.",
		ReplyTo:    &userSeq,
		SentAtMs:   now + 5,
	}
	f.messages = append(f.messages, userMsg, replyMsg)
	return userMsg, nil
}

// CreateTerminalTicket mints a single-use ticket bound to the requested role.
func (f *workspaceBrowserFixture) CreateTerminalTicket(
	_ context.Context, p *genhighbay.CreateTerminalTicketPayload,
) (*genhighbay.TerminalTicketResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ticket := "workspace-terminal-ticket-" + p.Role + "-" + time.Now().UTC().Format("150405.000000000")
	f.tickets[ticket] = p.Role
	f.ticketCounts[p.Role]++
	return &genhighbay.TerminalTicketResult{
		Ticket:    ticket,
		ExpiresAt: time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339Nano),
	}, nil
}

// RestartTeamRole records a one-click role restart request from the browser.
func (f *workspaceBrowserFixture) RestartTeamRole(
	_ context.Context, p *genhighbay.RestartTeamRolePayload,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartedRole = append(f.restartedRole, p.Role)
	return nil
}

// AttachRoleTerminal streams a simulated PTY banner over the Goa WebSocket and
// records returned flow-control credit and input frames.
func (f *workspaceBrowserFixture) AttachRoleTerminal(
	ctx context.Context,
	p *genhighbay.AttachRoleTerminalPayload,
	stream genhighbay.AttachRoleTerminalServerStream,
) error {
	f.mu.Lock()
	wantRole, ok := f.tickets[p.Ticket]
	delete(f.tickets, p.Ticket)
	f.mu.Unlock()
	if !ok || wantRole != p.Role {
		return genhighbay.MakeUnprocessable(errors.New("invalid or already-consumed terminal ticket"))
	}
	defer func() {
		if closeErr := stream.Close(); closeErr != nil && !errors.Is(closeErr, websocket.ErrCloseSent) {
			f.t.Logf("close workspace terminal stream: %v", closeErr)
		}
	}()
	attaches := f.attachCount.Add(1)
	banner := []byte("Allow tool? [y/N] ")
	if attaches >= 2 {
		banner = []byte("Restarted builder session\r\n")
	}
	if err := stream.SendWithContext(ctx, &genhighbay.TerminalServerFrame{
		ServerFrame: genhighbay.NewServerFrameOutput(&genhighbay.TerminalOutputFrame{Data: banner}),
	}); err != nil {
		return err
	}
	for {
		frame, err := stream.RecvWithContext(ctx)
		if err != nil {
			return nil
		}
		if credit, ok := frame.ClientFrame.AsCredit(); ok {
			f.credits.Add(int64(credit.Bytes))
		}
		if input, ok := frame.ClientFrame.AsInput(); ok {
			f.mu.Lock()
			f.inputs = append(f.inputs, append([]byte{}, input.Data...))
			f.mu.Unlock()
		}
	}
}
