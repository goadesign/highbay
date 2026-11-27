// These tests cover who a request is, as the project server decides it from
// its credential, and the browser session's two operations.
package project

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// browserSession exchanges a fresh link for a session and returns it.
func browserSession(t *testing.T, server *Server) *genproject.BrowserSessionResult {
	t.Helper()
	link, err := server.newBrowserLink()
	require.NoError(t, err)
	session, err := server.ExchangeBrowserLink(t.Context(), &genproject.ExchangeBrowserLinkPayload{Link: link.URL[len(server.state.Origin()+"/views#link="):]})
	require.NoError(t, err)
	return session
}

// TestAuthenticateBrowserSession checks which requests a browser session's
// credential authenticates: only the project page's own, and one that
// changes something only when it also names that page as its origin.
func TestAuthenticateBrowserSession(t *testing.T) {
	server, _ := teamServer(t)
	session := browserSession(t, server)
	origin := server.state.Origin()
	for _, test := range []struct {
		name          string
		method        string
		token         string
		site, origin  string
		authenticated bool
	}{
		{name: "the page reads", method: http.MethodGet, token: session.Session, site: "same-origin", authenticated: true},
		{name: "the page reads and names its origin", method: http.MethodGet, token: session.Session, site: "same-origin", origin: origin, authenticated: true},
		{name: "the page checks a resource's headers", method: http.MethodHead, token: session.Session, site: "same-origin", authenticated: true},
		{name: "the page changes something", method: http.MethodPost, token: session.Session, site: "same-origin", origin: origin, authenticated: true},
		{name: "the page deletes something", method: http.MethodDelete, token: session.Session, site: "same-origin", origin: origin, authenticated: true},
		{name: "a change that names no origin", method: http.MethodPost, token: session.Session, site: "same-origin"},
		{name: "a change from another origin", method: http.MethodPost, token: session.Session, site: "same-origin", origin: "http://127.0.0.1:1"},
		{name: "a read from another site", method: http.MethodGet, token: session.Session, site: "cross-site"},
		{name: "a read from the same site, another port", method: http.MethodGet, token: session.Session, site: "same-site"},
		{name: "a read that is not from a page", method: http.MethodGet, token: session.Session},
		{name: "a credential this server never issued", method: http.MethodGet, token: "0000", site: "same-origin"},
		{name: "no credential", method: http.MethodGet, site: "same-origin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, origin+"/changes", nil)
			if test.token != "" {
				request.Header.Set("Authorization", "Bearer "+test.token)
			}
			if test.site != "" {
				request.Header.Set("Sec-Fetch-Site", test.site)
			}
			if test.origin != "" {
				request.Header.Set("Origin", test.origin)
			}
			principal, err := server.Authenticate(request)
			if !test.authenticated {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, controlplane.CallerUser, principal.Kind)
			assert.Equal(t, server.state.Record.Owner, principal.ID)
		})
	}
}

// TestGetBrowserSession checks what a reloaded page or a sibling tab learns
// about the credential it stored.
func TestGetBrowserSession(t *testing.T) {
	server, _ := teamServer(t)
	session := browserSession(t, server)
	request := httptest.NewRequest(http.MethodGet, server.state.Origin()+"/project/browser-session", nil)
	request.Header.Set("Authorization", "Bearer "+session.Session)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	principal, err := server.Authenticate(request)
	require.NoError(t, err)

	restored, err := server.GetBrowserSession(controlplane.WithPrincipal(t.Context(), principal))
	require.NoError(t, err)
	assert.Equal(t, session, restored, "the stored credential is still the session's")

	for _, test := range []struct {
		name      string
		principal *controlplane.Principal
	}{
		{name: "no caller"},
		{name: "the highbay command", principal: &controlplane.Principal{ID: principal.ID, CallerID: "management", Kind: controlplane.CallerManagement}},
		{name: "an agent", principal: &controlplane.Principal{ID: principal.ID, CallerID: "guide-1", Kind: controlplane.CallerGuide}},
		{name: "a session of an earlier server run", principal: &controlplane.Principal{ID: principal.ID, CallerID: "user-gone", Kind: controlplane.CallerUser}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			if test.principal != nil {
				ctx = controlplane.WithPrincipal(ctx, *test.principal)
			}
			_, err := server.GetBrowserSession(ctx)
			assert.Equal(t, "unauthorized", errorName(err))
		})
	}
}

// TestBrowserSessionsPassToTheNextServer follows a page across a server that
// replaces itself: the credential the page stored authenticates on the next
// server and names the same session, and a set of sessions this package
// could not have issued restores nothing.
func TestBrowserSessionsPassToTheNextServer(t *testing.T) {
	first, _ := teamServer(t)
	session := browserSession(t, first)
	other := browserSession(t, first)
	sessions := first.BrowserSessions()
	require.Len(t, sessions, 2)
	sessions[session.Session] = "changed in the copy"
	assert.NotEqual(t, sessions[session.Session], first.BrowserSessions()[session.Session], "the result is a copy")
	sessions = first.BrowserSessions()

	_, dependencies := fixtureTeam(t.Context())
	next, err := NewServer(first.state, func() {}, dependencies)
	require.NoError(t, err)
	// request is a page's read with credential.
	request := func(credential string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, next.state.Origin()+"/changes", nil)
		r.Header.Set("Authorization", "Bearer "+credential)
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		return r
	}
	_, err = next.Authenticate(request(session.Session))
	require.Error(t, err, "a new server knows no session until it is given them")

	require.NoError(t, next.RestoreBrowserSessions(sessions))
	for _, credential := range []string{session.Session, other.Session} {
		principal, err := next.Authenticate(request(credential))
		require.NoError(t, err)
		assert.Equal(t, controlplane.Principal{ID: next.state.Record.Owner, CallerID: sessions[credential], Kind: controlplane.CallerUser}, principal)
		restored, err := next.GetBrowserSession(controlplane.WithPrincipal(t.Context(), principal))
		require.NoError(t, err)
		assert.Equal(t, credential, restored.Session)
	}
	assert.Equal(t, sessions, next.BrowserSessions())

	credential, id := strings.Repeat("a", 64), "user-"+strings.Repeat("b", 64)
	for _, refused := range []struct {
		name     string
		sessions map[string]string
	}{
		{name: "a credential that is too short", sessions: map[string]string{"abc": id}},
		{name: "a credential that is not lowercase hexadecimal", sessions: map[string]string{strings.Repeat("G", 64): id}},
		{name: "a session id without its prefix", sessions: map[string]string{credential: strings.Repeat("b", 64)}},
		{name: "a session id that is not random", sessions: map[string]string{credential: "user-1"}},
		{name: "another caller's id", sessions: map[string]string{credential: "management"}},
		{name: "two credentials for one session", sessions: map[string]string{credential: id, strings.Repeat("c", 64): id}},
		{name: "one bad entry among good ones", sessions: map[string]string{credential: id, "abc": "user-" + strings.Repeat("d", 64)}},
	} {
		t.Run("refused/"+refused.name, func(t *testing.T) {
			fresh, err := NewServer(first.state, func() {}, dependencies)
			require.NoError(t, err)
			err = fresh.RestoreBrowserSessions(refused.sessions)
			require.Error(t, err)
			assert.NotContains(t, err.Error(), credential)
			assert.Empty(t, fresh.BrowserSessions(), "a refused set restores nothing")
		})
	}
}
