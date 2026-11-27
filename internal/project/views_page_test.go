// This file drives the views page against the real project server: the page
// shell and its policy, the one-use link, the in-memory session, the browser
// marks the page's reads need, and what happens when the server restarts.
package project

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genprojectserver "github.com/goadesign/highbay/gen/http/project/server"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
)

type (
	// viewsProject is one running project composed as Highbay's composition root
	// composes it: a control plane and a project server behind the project's
	// saved address, with a test-only restart that replaces both, as starting
	// Highbay again does.
	viewsProject struct {
		// state is the project's private state.
		state *State
		// opts configures every control plane the project starts; CardsDir is
		// the project's.
		opts controlplane.Options
		// cp is the control plane currently answering.
		cp atomic.Pointer[controlplane.ControlPlane]
		// server is the project server currently answering.
		server atomic.Pointer[Server]
		// handler is server's guarded handler.
		handler atomic.Pointer[http.Handler]
	}

	// viewsWorkspacePageFixture overrides team, ticket, restart, and update
	// answers so jsdom tests can verify UI digest probing and per-role restarts.
	viewsWorkspacePageFixture struct {
		genhighbay.Service
		mu            sync.Mutex
		version       string
		uiBuild       string
		ticketCounts  map[string]int
		restartedRole []string
	}
)

// TestViewsPageSessionLifecycle walks the page's session: the served shell
// and its policy, no link, a link exchanged once and removed from the
// address, origin-scoped localStorage without cookies, a reload staying signed
// in while the server runs, a second use of the link, the browser marks reads
// carry, and a server restart without handoff ending the session while keeping
// typed edits and clearing rejected localStorage.
func TestViewsPageSessionLifecycle(t *testing.T) {
	node := requireViewsPageTools(t)
	vp := startViewsProject(t, controlplane.Options{})
	guide := vp.agent(t, "guide")
	link := func() string {
		minted, err := vp.link()
		require.NoError(t, err)
		return minted
	}

	shell, err := http.Get(vp.state.Origin() + "/views")
	require.NoError(t, err)
	require.NoError(t, shell.Body.Close())
	policy := shell.Header.Get("Content-Security-Policy")
	assert.Equal(t, http.StatusOK, shell.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", shell.Header.Get("Content-Type"))
	assert.Equal(t, "nosniff", shell.Header.Get("X-Content-Type-Options"))
	for _, directive := range []string{"default-src 'none'", "connect-src 'self'", "frame-ancestors 'none'", "form-action 'none'", "base-uri 'none'"} {
		assert.Contains(t, policy, directive, "the page's policy, as sent by the server")
	}

	input := map[string]any{
		"origin": vp.state.Origin(), "first": link(), "guide": string(guide),
	}
	runViewsPageScript(t, node, input, `
  // Recovery names both highbay and /highbay without retired companion or hbay copy.
  const expectRecovery = (page, selector) => {
    const text = page.text(selector);
    expect(text.includes("highbay") && text.includes("/highbay") && text.includes("coding agent"), "session recovery omitted highbay or /highbay: " + text);
    expect(!/\bhbay\b/.test(text) && !text.includes("Herdr companion") && !text.includes("Open Highbay Window"), "session recovery still contains retired CLI or companion text: " + text);
  };
  // Without a link or stored session the page explains how to open it and asks the server nothing.
  const bare = await openPage(origin);
  await until(() => bare.$("#highbay-signed-out"), "a page without a link did not say how to open it");
  expectRecovery(bare, "#highbay-signed-out");
  expect(!bare.text("#highbay-signed-out-title").includes("with hbay"), "the no-link heading still directs the user to hbay");
  const lifetime = bare.text("#highbay-signed-out .highbay-muted");
  expect(lifetime.includes("reloading this page") && lifetime.includes("keeps your session active"), "the session-lifetime note omitted reload persistence: " + lifetime);
  await sleep(200);
  expect(bare.requests.length === 0, "a page without a link sent requests: " + JSON.stringify(bare.requests));
  bare.close();
  for (const path of ["/changes", "/change-updates?since=x", "/changes/chg", "/traces"]) {
    const refused = await api(origin, "GET", path);
    expect(refused.status === 401, "a data request without a credential answered " + refused.status + " for " + path);
  }

  // The link becomes an origin-scoped localStorage Bearer session with zero cookies, and leaves the address.
  const page = await openPage(origin, { link: input.first });
  await until(() => page.$("#highbay-change-list-empty"), "the page did not obtain a session and read the empty project");
  expect(page.win.location.href === origin + "/views" && page.win.location.hash === "", "the link stayed in the address: " + page.win.location.href);
  const exchange = page.sent("POST", "/project/browser-session");
  expect(exchange.length === 1 && exchange[0].body.link === input.first && !exchange[0].headers.Authorization, "the link was not exchanged exactly once");
  const reads = page.requests.filter((r) => r.method === "GET");
  expect(reads.length > 0 && reads.every((r) => !("Origin" in r.headers) && r.headers.Authorization === reads[0].headers.Authorization), "reads did not carry the session without stating the page's address");
  expect(page.win.localStorage.length === 1 && page.win.localStorage.getItem("highbay.session") !== null && page.win.sessionStorage.length === 0 && page.doc.cookie === "", "the session was not stored solely in origin-scoped localStorage");
  const session = reads[0].headers.Authorization;

  // The server refuses the session from another page or from a program.
  const fromOtherPage = await fetch(origin + "/changes", { headers: { Authorization: session, "Sec-Fetch-Site": "same-site", Origin: "http://127.0.0.1:1" } });
  expect(fromOtherPage.status === 403, "a read from another address answered " + fromOtherPage.status);
  const replayed = await api(origin, "GET", "/changes", undefined, session.replace("Bearer ", ""));
  expect(replayed.status === 401, "a replay without the page's mark answered " + replayed.status);

  // A change the guide proposes appears by itself; the user types into it.
  const proposed = await api(origin, "POST", "/changes/propose", {
    change_id: "chg-session", prompt: "Route refunds to a manager.",
    analysis: { summary: "Route refunds to a manager.", ambiguities: [], examples: [{ scenario: "Refund", input_summary: "$40", expected_outcome: "Approved", verification_kind: "deterministic" }], preserved_behaviors: [] },
  }, input.guide);
  expect(proposed.status === 200, "the guide could not propose: " + JSON.stringify(proposed));
  await until(() => page.$("#highbay-open-change-chg-session"), "the proposed change did not appear");
  page.$("#highbay-open-change-chg-session").click();
  await until(() => page.$('textarea[data-field="expected_outcome"]'), "the change did not open");
  const field = page.$('textarea[data-field="expected_outcome"]');
  page.type(field, "Typed before the restart");

  // Reloading or opening another tab on the same origin stays signed in while the server runs.
  const reloaded = await openPage(origin, { storage: page.win.localStorage });
  await until(() => reloaded.$("#highbay-open-change-chg-session") || reloaded.$('textarea[data-field="expected_outcome"]'), "a reloaded page did not stay signed in via GET /project/browser-session");
  expect(reloaded.sent("GET", "/project/browser-session").length === 1, "reload did not validate its stored session via GET /project/browser-session");
  reloaded.close();

  // A second use of the same link is refused and reported without erasing the active tab's stored session.
  const reused = await openPage(origin, { link: input.first, storage: page.win.localStorage });
  await until(() => reused.$("#highbay-link-used"), "a used link was not reported");
  expect(reused.text("#highbay-link-used").includes("already used"), "the used link was not reported plainly");
  expectRecovery(reused, "#highbay-signed-out");
  expect(reused.win.location.hash === "", "a refused link stayed in the address");
  expect(reused.win.localStorage.getItem("highbay.session") !== null, "a refused link erased an active stored session");
  reused.close();

  // (f) A non-422 link exchange failure renders #highbay-link-failed with the server's message, never #highbay-link-used.
  await fetch(origin + "/__test/fail-next-exchange", { method: "POST" });
  const failedLinkTab = await openPage(origin, { link: "transient-link", storage: page.win.localStorage });
  await until(() => failedLinkTab.$("#highbay-link-failed"), "a 503 link exchange failure did not render #highbay-link-failed");
  expect(!failedLinkTab.$("#highbay-link-used") && failedLinkTab.text("#highbay-link-failed").includes("Session store temporarily unavailable."), "non-422 link failure rendered wrong alert: " + failedLinkTab.text("#highbay-signed-out"));
  expect(failedLinkTab.win.localStorage.getItem("highbay.session") !== null, "a 503 link exchange failure erased an active stored session");
  failedLinkTab.close();

  // (h) A stale tab receiving 401 must not erase a newer session written to localStorage by a sibling tab.
  const staleTab = await openPage(origin, { storage: page.win.localStorage });
  await until(() => (staleTab.$("#highbay-open-change-chg-session") || staleTab.$('textarea[data-field="expected_outcome"]')) && staleTab.requests.every((r) => r.path === "/change-updates" || r.status !== undefined), "staleTab did not settle initial session reads");
  // Pause both tabs' waiting loops before invalidating their session so restart cannot trigger an early 401 race.
  page.setHidden(true);
  staleTab.setHidden(true);
  await control(origin, "restart");
  const postRestartLink = await (await fetch(origin + "/__test/link", { method: "POST" })).text();
  const siblingFresh = await openPage(origin, { link: postRestartLink });
  await until(() => siblingFresh.win.localStorage.getItem("highbay.session") !== null && (siblingFresh.$("#highbay-open-change-chg-session") || siblingFresh.$('textarea[data-field="expected_outcome"]')), "siblingFresh did not establish a newer post-restart session");
  const newerToken = siblingFresh.win.localStorage.getItem("highbay.session");
  staleTab.win.localStorage.setItem("highbay.session", newerToken);
  const teamReadsBefore = staleTab.sent("GET", "/team").length;
  staleTab.$("#highbay-nav-team").click();
  await until(() => staleTab.$("#highbay-team-refresh"), "staleTab did not open Team");
  staleTab.$("#highbay-team-refresh").click();
  await until(() => staleTab.sent("GET", "/team").slice(teamReadsBefore).some((r) => r.status === 401) && staleTab.$("#highbay-session-ended"), "staleTab did not receive 401 and end its rejected in-memory session");
  expect(staleTab.win.localStorage.getItem("highbay.session") === newerToken, "staleTab 401 erased the sibling tab's newer stored session");
  staleTab.close();
  siblingFresh.close();

  // After an unhanded-off restart without a newer sibling token, 401 on GET /project/browser-session clears localStorage.
  await control(origin, "restart");
  expect(page.win.localStorage.getItem("highbay.session") !== null, "hidden page lost its pre-restart stored session before reload test");
  const restartedReload = await openPage(origin, { storage: page.win.localStorage });
  await until(() => restartedReload.sent("GET", "/project/browser-session").some((r) => r.status === 401) && restartedReload.$("#highbay-signed-out"), "a reload after restart did not receive 401 and sign out cleanly");
  expect(restartedReload.win.localStorage.length === 0, "401 on GET /project/browser-session did not clear localStorage");
  restartedReload.close();
  page.setHidden(false);

  const fresh = await (await fetch(origin + "/__test/link", { method: "POST" })).text();
  field.focus();
  field.setSelectionRange(2, 8);
  page.win.location.hash = "link=" + fresh;
  await until(() => page.$("#highbay-pending-link") && page.win.location.hash === "", "a new same-tab link was not scrubbed and deferred");
  expect(page.$("#highbay-open-pending-link").disabled && page.beforeUnload(), "unsaved CP1 input did not guard both link opening and unload");
  expect(page.$('textarea[data-field="expected_outcome"]') === field && field.value === "Typed before the restart" && page.doc.activeElement === field && field.selectionStart === 2 && field.selectionEnd === 8, "deferring a link changed the draft, focus or selection");
  expect(page.sent("POST", "/project/browser-session").length === 1, "a deferred link was exchanged before edits were resolved");
  expect(page.text("#highbay-pending-link").includes("two minutes after it was printed"), "the deferred notice did not explain the link's original expiry");
  expect(page.status().includes("You opened a new Highbay link") && page.status().includes("Open link"), "the existing live status did not announce the deferred link");
  page.$("#highbay-confirm-submit").click();
  await until(() => page.$("#highbay-session-ended"), "the page did not say its session ended");
  expectRecovery(page, "#highbay-session-ended");
  expect(page.$('textarea[data-field="expected_outcome"]').value === "Typed before the restart", "typed edits were lost");
  expect(page.text("#highbay-session-ended-edits").includes("Typed before the restart"), "the unsaved edits are not shown for copying");
  expect(page.$("#highbay-open-pending-link").disabled && page.beforeUnload(), "a failed save released unsaved input");
  page.$("#highbay-discard-draft").click();
  await until(() => !page.$("#highbay-open-pending-link").disabled, "local discard after session end did not enable the pending link");
  expect(!page.beforeUnload(), "discarded CP1 input still blocked leaving");
  expect(page.win.localStorage.length === 0 && page.win.sessionStorage.length === 0, "the ended session or pending link remained in browser storage");
  const afterRestart = await fetch(origin + "/changes", { headers: { Authorization: session, "Sec-Fetch-Site": "same-origin" } });
  expect(afterRestart.status === 401, "a session from before the restart answered " + afterRestart.status);
  page.close();
`)
}

// TestViewsPageShellNavigation allows top-level navigation to the data-free
// shell while refusing cross-site frames, fetches and session exchanges.
func TestViewsPageShellNavigation(t *testing.T) {
	vp := startViewsProject(t, controlplane.Options{})
	for _, tc := range []struct {
		name, method, path, site, mode, destination string
		want                                        int
	}{
		{"bare address", "GET", "/", "none", "navigate", "document", http.StatusForbidden},
		{"extension navigation", "GET", "/views", "cross-site", "navigate", "document", http.StatusOK},
		{"address bar", "GET", "/views", "none", "navigate", "document", http.StatusOK},
		{"same-site navigation", "GET", "/views", "same-site", "navigate", "document", http.StatusOK},
		{"same-origin shell", "GET", "/views", "same-origin", "", "", http.StatusOK},
		{"cross-site frame", "GET", "/views", "cross-site", "navigate", "iframe", http.StatusForbidden},
		{"cross-site fetch", "GET", "/views", "cross-site", "cors", "empty", http.StatusForbidden},
		{"wrong mode", "GET", "/views", "cross-site", "no-cors", "document", http.StatusForbidden},
		{"missing destination", "GET", "/views", "cross-site", "navigate", "", http.StatusForbidden},
		{"other method", "POST", "/views", "cross-site", "navigate", "document", http.StatusForbidden},
		{"other route", "GET", "/project/identity", "cross-site", "navigate", "document", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), tc.method, vp.state.Origin()+tc.path, nil)
			require.NoError(t, err)
			request.Header.Set("Sec-Fetch-Site", tc.site)
			request.Header.Set("Sec-Fetch-Mode", tc.mode)
			request.Header.Set("Sec-Fetch-Dest", tc.destination)
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			assert.Equal(t, tc.want, response.StatusCode)
			assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
			if tc.want == http.StatusOK {
				assert.Contains(t, response.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
				assert.Equal(t, "nosniff", response.Header.Get("X-Content-Type-Options"))
			}
			body, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.NoError(t, response.Body.Close())
			if tc.name == "bare address" {
				assert.Contains(t, string(body), "request must come from the project's own page")
				assert.Contains(t, string(body), "run highbay in the project folder, or ask your coding agent to open Highbay views")
				assert.NotContains(t, string(body), "run hbay")
			}
		})
	}
	link, err := vp.link()
	require.NoError(t, err)
	body, err := json.Marshal(&genprojectserver.ExchangeBrowserLinkRequestBody{Link: &link})
	require.NoError(t, err)
	for _, site := range []string{"cross-site", "same-site", "none", "same-origin"} {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, vp.state.Origin()+"/project/browser-session", bytes.NewReader(body))
		require.NoError(t, err)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Sec-Fetch-Site", site)
		request.Header.Set("Sec-Fetch-Mode", "navigate")
		request.Header.Set("Sec-Fetch-Dest", "document")
		response, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		want := http.StatusForbidden
		if site == "same-origin" {
			want = http.StatusCreated // Refused requests never consumed the link.
		}
		assert.Equal(t, want, response.StatusCode, "exchange from %s", site)
		_, err = io.Copy(io.Discard, response.Body)
		require.NoError(t, err)
		require.NoError(t, response.Body.Close())
	}
}

// TestViewsPageReadsNeedTheSameOriginMark sends the page's two reads with a
// browser session straight to the project server, without a browser: a
// browser's same-origin mark is enough without an Origin header (browsers
// omit Origin on same-origin GET requests), and a missing mark, another
// site's mark, or another page's Origin is refused.
func TestViewsPageReadsNeedTheSameOriginMark(t *testing.T) {
	vp := startViewsProject(t, controlplane.Options{})
	management := controlplane.WithPrincipal(context.Background(), controlplane.Principal{ID: vp.state.Record.Owner, CallerID: "management", Kind: controlplane.CallerManagement})
	minted, err := vp.server.Load().BrowserLink(management)
	require.NoError(t, err)
	u, err := url.Parse(minted.URL)
	require.NoError(t, err)
	body, err := json.Marshal(&genproject.ExchangeBrowserLinkPayload{Link: strings.TrimPrefix(u.Fragment, "link=")})
	require.NoError(t, err)
	exchange, err := http.NewRequest(http.MethodPost, vp.state.Origin()+"/project/browser-session", strings.NewReader(string(body)))
	require.NoError(t, err)
	exchange.Header.Set("Content-Type", "application/json")
	exchange.Header.Set("Sec-Fetch-Site", "same-origin")
	exchange.Header.Set("Origin", vp.state.Origin())
	exchanged, err := http.DefaultClient.Do(exchange)
	require.NoError(t, err)
	var session genproject.BrowserSessionResult
	require.NoError(t, json.NewDecoder(exchanged.Body).Decode(&session))
	require.NoError(t, exchanged.Body.Close())
	require.Equal(t, http.StatusCreated, exchanged.StatusCode)

	cases := []struct {
		name   string
		site   string
		origin string
		want   int
	}{
		{name: "same-origin mark without Origin", site: "same-origin", want: http.StatusOK},
		{name: "same-origin mark with the project's Origin", site: "same-origin", origin: vp.state.Origin(), want: http.StatusOK},
		{name: "no mark and no Origin", want: http.StatusUnauthorized},
		{name: "cross-site mark", site: "cross-site", want: http.StatusForbidden},
		{name: "same-site mark", site: "same-site", want: http.StatusForbidden},
		{name: "same-origin mark with another page's Origin", site: "same-origin", origin: "http://127.0.0.1:1", want: http.StatusForbidden},
	}
	for _, path := range []string{"/changes", "/change-updates?since=other"} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				req, err := http.NewRequest(http.MethodGet, vp.state.Origin()+path, nil)
				require.NoError(t, err)
				req.Header.Set("Authorization", "Bearer "+session.Session)
				if tc.site != "" {
					req.Header.Set("Sec-Fetch-Site", tc.site)
				}
				if tc.origin != "" {
					req.Header.Set("Origin", tc.origin)
				}
				resp, err := http.DefaultClient.Do(req)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				assert.Equal(t, tc.want, resp.StatusCode)
			})
		}
	}
}

// TestViewsProjectRestartReportsStartupFailure preserves the constructor error
// instead of proceeding with nil servers or acknowledging a failed HTTP restart.
func TestViewsProjectRestartReportsStartupFailure(t *testing.T) {
	t.Run("initial construction", func(t *testing.T) {
		vp := &viewsProject{state: testState(t), opts: controlplane.Options{HighbayVersion: "invalid"}}
		err := vp.restart()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be a canonical semantic version")
		assert.Nil(t, vp.cp.Load())
		assert.Nil(t, vp.server.Load())
		assert.Nil(t, vp.handler.Load())
	})
	t.Run("HTTP restart", func(t *testing.T) {
		vp := startViewsProject(t, controlplane.Options{})
		vp.opts.HighbayVersion = "invalid"
		resp, err := http.Post(vp.state.Origin()+"/__test/restart", "text/plain", nil)
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, resp.Body.Close())
		require.NoError(t, err)
		assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
		assert.Contains(t, string(body), "must be a canonical semantic version")
	})
}

// TestViewsPageDetectsExternalUpdateAndScopesRoleRestart verifies that (1) a
// hidden tab shown after an external update probes GET /project/browser-session
// and offers Reload even when wait_for_changes succeeds immediately, (2) an
// already-visible tab detects an external handoff between two successful
// wait_for_changes rounds, and (3) restarting guide leaves an open builder
// TerminalDrawer untouched without requesting a second builder ticket.
func TestViewsPageDetectsExternalUpdateAndScopesRoleRestart(t *testing.T) {
	node := requireViewsPageTools(t)
	vp := startViewsProject(t, controlplane.Options{})
	guide := vp.agent(t, "guide")
	server := vp.server.Load()
	fixture := &viewsWorkspacePageFixture{
		Service:      vp.cp.Load(),
		version:      "v0.4.0",
		uiBuild:      controlplane.ViewsPageScriptSHA256,
		ticketCounts: map[string]int{},
	}

	appMux := http.NewServeMux()
	appMux.Handle("GET /views", controlplane.ViewsPage())
	appMux.Handle("/", controlplane.NewHTTPHandler(fixture, server.Authenticate))
	baseHandler := server.Handler(appMux)

	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/__test/external-update" && r.Method == http.MethodPost:
			var req struct {
				Version string `json:"version"`
				UIBuild string `json:"ui_build"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			fixture.mu.Lock()
			fixture.version = req.Version
			fixture.uiBuild = req.UIBuild
			fixture.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
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
		case r.URL.Path == "/project/browser-session" && r.Method == http.MethodGet:
			if _, err := server.Authenticate(r); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
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
		default:
			baseHandler.ServeHTTP(w, r)
		}
	})
	var handler http.Handler = wrapped
	vp.handler.Store(&handler)

	firstLink, err := vp.link()
	require.NoError(t, err)
	secondLink, err := vp.link()
	require.NoError(t, err)

	runViewsPageScript(t, node, map[string]any{
		"origin": vp.state.Origin(),
		"first":  firstLink,
		"second": secondLink,
		"guide":  guide,
	}, `
  // (1) Hidden-tab external update: tab hides, server updates to v0.5.0 with a
  // new UI digest, tab resumes, wait_for_changes succeeds, and Reload appears.
  const hiddenTab = await openPage(origin, { link: input.first });
  await until(() => hiddenTab.$("#highbay-update-install"), "initial UpdateBanner did not render");
  hiddenTab.setHidden(true);
  const updateHidden = await fetch(origin + "/__test/external-update", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ version: "v0.5.0", ui_build: "external-hidden-ui-build" }),
  });
  expect(updateHidden.status === 204, "external-update failed: " + updateHidden.status);
  const probesBeforeShow = hiddenTab.sent("GET", "/project/browser-session").length;
  hiddenTab.setHidden(false);
  await until(() => hiddenTab.$("#highbay-update-reload") && !hiddenTab.$("#highbay-update-install"), "resumed tab did not probe ui_build and replace Update & Restart with Reload");
  expect(hiddenTab.sent("GET", "/project/browser-session").length > probesBeforeShow, "resumed tab did not send GET /project/browser-session");
  hiddenTab.close();

  // Reset server to v0.5.0 baseline for a second already-visible tab.
  const visibleTab = await openPage(origin, { link: input.second });
  await until(() => visibleTab.$("#highbay-change-list-empty") || visibleTab.$(".highbay-welcome-card"), "visible tab did not finish initial load");

  // (2) Role-scoped restart: open builder TerminalDrawer, restart guide from
  // Team, verify zero extra builder tickets, then restart builder from drawer.
  await until(() => visibleTab.$("#highbay-team-attention-terminal-builder"), "builder attention terminal action did not render");
  visibleTab.$("#highbay-team-attention-terminal-builder").click();
  await until(() => visibleTab.sent("POST", "/team/roles/builder/terminal-ticket").length === 1, "opening builder drawer did not request one builder ticket");
  visibleTab.$("#highbay-nav-team").click();
  await until(() => visibleTab.$("#highbay-team-restart-guide"), "guide restart button did not render");
  visibleTab.$("#highbay-team-restart-guide").click();
  await until(() => visibleTab.sent("POST", "/team/roles/guide/restart").length === 1, "guide restart request was not sent");
  await sleep(150);
  expect(visibleTab.sent("POST", "/team/roles/builder/terminal-ticket").length === 1, "restarting guide requested another builder terminal ticket");
  visibleTab.$("#highbay-terminal-restart").click();
  await until(() => visibleTab.sent("POST", "/team/roles/builder/restart").length === 1 && visibleTab.sent("POST", "/team/roles/builder/terminal-ticket").length === 2, "restarting builder did not request a replacement builder ticket");

  // (3) Already-visible external handoff between two successful wait_for_changes reads.
  const updateVisible = await fetch(origin + "/__test/external-update", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ version: "v0.6.0", ui_build: "external-visible-ui-build" }),
  });
  expect(updateVisible.status === 204, "second external-update failed");
  const proposed = await api(origin, "POST", "/changes/propose", {
    change_id: "chg-after-handoff", prompt: "Verify visible handoff digest probe.",
    analysis: { summary: "Verify visible handoff digest probe.", ambiguities: [], examples: [{ scenario: "S", input_summary: "I", expected_outcome: "O", verification_kind: "deterministic" }], preserved_behaviors: [] },
  }, input.guide);
  expect(proposed.status === 200, "propose after visible handoff failed: " + JSON.stringify(proposed));
  await until(() => visibleTab.$("#highbay-update-reload"), "already-visible tab did not offer Reload after successful wait_for_changes");
  visibleTab.close();
`)

	fixture.mu.Lock()
	restarted := append([]string{}, fixture.restartedRole...)
	tickets := map[string]int{}
	for role, count := range fixture.ticketCounts {
		tickets[role] = count
	}
	fixture.mu.Unlock()
	assert.Equal(t, []string{"guide", "builder"}, restarted)
	assert.Equal(t, map[string]int{"builder": 2}, tickets)
}

// GetTeam returns a connected team with a waiting Guide and a blocked Builder terminal.
func (f *viewsWorkspacePageFixture) GetTeam(context.Context) (*genhighbay.TeamSnapshot, error) {
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

// GetTeamConversation returns an empty Chorus conversation for the workspace page test.
func (f *viewsWorkspacePageFixture) GetTeamConversation(context.Context) (*genhighbay.TeamConversation, error) {
	return &genhighbay.TeamConversation{
		Team:         "refunds-team",
		Channel:      "#refunds-team",
		ChorusStatus: "connected",
		Messages:     []*genhighbay.TeamMessage{},
	}, nil
}

// CreateTerminalTicket records per-role ticket requests for TerminalDrawer.
func (f *viewsWorkspacePageFixture) CreateTerminalTicket(
	_ context.Context, p *genhighbay.CreateTerminalTicketPayload,
) (*genhighbay.TerminalTicketResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ticketCounts[p.Role]++
	return &genhighbay.TerminalTicketResult{
		Ticket:    fmt.Sprintf("ticket-%s-%d", p.Role, f.ticketCounts[p.Role]),
		ExpiresAt: time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339Nano),
	}, nil
}

// RestartTeamRole records per-role restart requests from the browser.
func (f *viewsWorkspacePageFixture) RestartTeamRole(
	_ context.Context, p *genhighbay.RestartTeamRolePayload,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restartedRole = append(f.restartedRole, p.Role)
	return nil
}

// startViewsProject starts a project on its saved address with a control
// plane configured by opts. Test-only endpoints restart the project
// (POST /__test/restart) and mint a browser link from the server currently
// answering (POST /__test/link).
func startViewsProject(t *testing.T, opts controlplane.Options) *viewsProject {
	t.Helper()
	state := testState(t)
	vp := &viewsProject{state: state, opts: opts}
	require.NoError(t, vp.restart())
	t.Cleanup(func() {
		if cp := vp.cp.Load(); cp != nil {
			assert.NoError(t, cp.Close())
		}
	})
	var failNextExchange atomic.Bool
	api := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/__test/fail-next-exchange":
			failNextExchange.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/project/browser-session" && failNextExchange.CompareAndSwap(true, false):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, err := io.WriteString(w, `{"name":"unavailable","id":"exchange-503","message":"Session store temporarily unavailable.","temporary":true,"timeout":false,"fault":false}`)
			assert.NoError(t, err)
		case r.Method == http.MethodPost && r.URL.Path == "/__test/restart":
			if err := vp.restart(); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/__test/link":
			link, err := vp.link()
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if _, err := io.WriteString(w, link); err != nil {
				t.Errorf("write minted link: %v", err)
			}
		default:
			(*vp.handler.Load()).ServeHTTP(w, r)
		}
	}))
	require.NoError(t, api.Listener.Close())
	var err error
	api.Listener, err = net.Listen("tcp", state.Address())
	require.NoError(t, err)
	api.Start()
	t.Cleanup(api.Close)
	return vp
}

// restart stops the current control plane, if any, and starts a new control
// plane and project server on the same state, as starting Highbay again does:
// saved changes and agent credentials survive, while every session and
// everything the old control plane held only in memory, such as the Story
// evaluation, ends. It returns construction or cleanup failures to the caller,
// which must stop before using a project that failed to start.
func (vp *viewsProject) restart() error {
	if old := vp.cp.Load(); old != nil {
		if err := old.Close(); err != nil {
			return fmt.Errorf("stop the control plane: %w", err)
		}
	}
	server, err := NewServer(vp.state, func() {}, fixtureTeamDependencies(vp.state))
	if err != nil {
		return fmt.Errorf("start the project server: %w", err)
	}
	// Use the project's directory unless the test supplies a workspace with a Story.
	opts := vp.opts
	opts.CardsDir = vp.state.CardsDir()
	if opts.WorkspaceDir == "" {
		opts.WorkspaceDir = vp.state.Record.Directory
	}
	opts.Team = &controlplane.TeamBinding{Owner: vp.state.Record.Owner, Service: server}
	cp, err := controlplane.New(opts)
	if err != nil {
		return fmt.Errorf("start the control plane: %w", err)
	}
	application := http.NewServeMux()
	application.Handle("GET /views", controlplane.ViewsPage())
	application.Handle("/", controlplane.NewHTTPHandler(cp, server.Authenticate))
	handler := server.Handler(application)
	vp.cp.Store(cp)
	vp.server.Store(server)
	vp.handler.Store(&handler)
	return nil
}

// agent adds an agent in role to the project and returns its credential.
func (vp *viewsProject) agent(t *testing.T, role string) string {
	t.Helper()
	agent, err := registerTestAgent(vp.server.Load(), role, role, vp.server.Load().state.Record.Directory)
	require.NoError(t, err)
	credential, err := os.ReadFile(agent.CredentialFile)
	require.NoError(t, err)
	return string(credential)
}

// link mints a one-use browser link from the server currently answering and
// returns the value the page reads from its address fragment.
func (vp *viewsProject) link() (string, error) {
	minted, err := vp.server.Load().BrowserLink(vp.management())
	if err != nil {
		return "", err
	}
	u, err := url.Parse(minted.URL)
	if err != nil {
		return "", err
	}
	if origin := u.Scheme + "://" + u.Host; origin != vp.state.Origin() {
		return "", fmt.Errorf("link is for %s, not the project's %s", origin, vp.state.Origin())
	}
	return strings.TrimPrefix(u.Fragment, "link="), nil
}

// management is the context of the management caller `highbay` uses.
func (vp *viewsProject) management() context.Context {
	return controlplane.WithPrincipal(context.Background(), controlplane.Principal{
		ID: vp.state.Record.Owner, CallerID: "management", Kind: controlplane.CallerManagement,
	})
}

// requireViewsPageTools returns the node binary the views page tests run,
// which also need ui/checkpoint's installed packages.
func requireViewsPageTools(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		missingPageTool(t, "node is required to exercise the views page")
	}
	if _, err := os.Stat("../../ui/checkpoint/node_modules/jsdom"); err != nil {
		missingPageTool(t, "ui/checkpoint/node_modules is missing; run npm ci --prefix ui/checkpoint")
	}
	return node
}

// runViewsPageScript runs body, an async JavaScript function body, in node
// with the page harness (ui/checkpoint/testing/page_harness.cjs), origin and
// input in scope, and fails the test with the script's output when it
// throws.
func runViewsPageScript(t *testing.T, node string, input map[string]any, body string) {
	t.Helper()
	runViewsPageScriptWithTimeout(t, node, input, body, 3*time.Minute)
}

// runViewsPageScriptWithTimeout gives a real owner operation its full allowance
// without extending the shorter bound used by ordinary scripted page tests.
func runViewsPageScriptWithTimeout(
	t *testing.T, node string, input map[string]any, body string, timeout time.Duration,
) {
	t.Helper()
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	inputPath := filepath.Join(t.TempDir(), "input.json")
	require.NoError(t, os.WriteFile(inputPath, raw, 0o600))
	harness, err := filepath.Abs("../../ui/checkpoint/testing/page_harness.cjs")
	require.NoError(t, err)
	harnessLiteral, err := json.Marshal(harness)
	require.NoError(t, err)
	script := `
const fs = require("fs");
const { api, control, expect, openPage, sleep, until } = require(` + string(harnessLiteral) + `);
const input = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const origin = input.origin;
async function main() {
` + body + `
}
main().then(() => { process.stdout.write("PAGE-PASSED"); process.exit(0); }, (err) => { console.error(err && err.stack || err); process.exit(1); });
`
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", script, inputPath)
	cmd.Env = append(os.Environ(), "CI=true")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "views page script failed:\n%s", out)
	require.True(t, strings.HasSuffix(string(out), "PAGE-PASSED"), "views page script output:\n%s", out)
}

// missingPageTool fails the test under CI=true, because CI installs node and
// ui/checkpoint's packages before running Go tests, and otherwise skips it
// with reason, which names the step that provides the missing tool. Tests
// never install the packages themselves: that writes the source tree and
// races other test binaries reading it.
func missingPageTool(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("CI") == "true" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}
