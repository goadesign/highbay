// This file verifies private browser handoff at the generated caller boundary.
package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenViewsKeepsBrowserAuthorityPrivate covers allowed and refused callers
// plus an opener error containing a private link, through the owning HTTP consumer.
func TestOpenViewsKeepsBrowserAuthorityPrivate(t *testing.T) {
	for _, kind := range []CallerKind{CallerUser, CallerGuide, CallerBuilder, CallerTestWriter, CallerManagement} {
		t.Run(string(kind), func(t *testing.T) {
			for _, failed := range []bool{false, true} {
				called := false
				cp := newControlPlane(t, Options{OpenViews: func(context.Context) error {
					called = true
					if failed {
						return errors.New("http://127.0.0.1/views#link=private-browser-authority")
					}
					return nil
				}})
				handler := NewHTTPHandler(cp, func(*http.Request) (Principal, error) { return Principal{ID: "alice@example.com", Kind: kind}, nil })
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/views/open", nil))
				allowed := kind == CallerUser || kind == CallerGuide
				assert.Equal(t, allowed, called)
				assert.NotContains(t, response.Body.String(), "private-browser-authority")
				if allowed {
					assert.Equal(t, http.StatusOK, response.Code)
					if failed {
						assert.Contains(t, response.Body.String(), `"opened":false`)
					} else {
						assert.Contains(t, response.Body.String(), `"opened":true`)
					}
				} else {
					assert.Equal(t, http.StatusForbidden, response.Code)
				}
			}
		})
	}
}

// TestOpenViewsNamesTheConversation checks what open_views answers a guide
// session that attached itself: whether the browser opened, with the step to
// tell the user, and always the project's Chorus team, channel and the guide's
// registered handle, which the session needs to join the conversation. The
// handle is empty while no guide is registered.
func TestOpenViewsNamesTheConversation(t *testing.T) {
	guide := &genhighbay.TeamRoleState{
		Role: "guide", Handle: "views-guide", State: "working", Attention: "none", Assignment: "no_work",
	}
	builder := &genhighbay.TeamRoleState{
		Role: "builder", Handle: "views-builder", State: "not_started", Attention: "none", Assignment: "no_work",
	}
	for _, opener := range []struct {
		name string
		// open is Options.OpenViews; nil leaves the server without an opener.
		open   func(context.Context) error
		opened bool
		// says lists what the message must tell the session.
		says []string
	}{
		{
			name: "opened", open: func(context.Context) error { return nil }, opened: true,
			says: []string{
				"Opened Highbay's browser workspace in the user's default browser.",
				"the conversation with the team continues on that page",
				"asking you to open Highbay views opens it again",
			},
		},
		{
			name: "opener failed", open: func(context.Context) error { return assert.AnError },
			says: []string{
				"The Highbay browser window could not be opened.",
				"ask the user to run highbay in the project folder",
			},
		},
		{
			name: "no opener configured",
			says: []string{
				"This Highbay server has no browser opener configured.",
				"The user can run highbay in the project folder",
			},
		},
	} {
		for _, roster := range []struct {
			name   string
			roles  []*genhighbay.TeamRoleState
			handle string
		}{
			{name: "guide registered", roles: []*genhighbay.TeamRoleState{guide, builder}, handle: "views-guide"},
			{name: "no guide registered", roles: []*genhighbay.TeamRoleState{builder}},
		} {
			t.Run(opener.name+"/"+roster.name, func(t *testing.T) {
				team := &teamServiceFixture{snapshot: genhighbay.TeamSnapshot{
					Team: "views-team", Channel: "#views-team-8f3a", Roles: roster.roles,
				}}
				cp := newControlPlane(t, Options{
					OpenViews: opener.open,
					Team:      &TeamBinding{Owner: "alice@example.com", Service: team},
				})
				result, err := cp.OpenViews(asCaller("alice@example.com", CallerGuide))
				require.NoError(t, err)
				assert.Equal(t, opener.opened, result.Opened)
				for _, text := range opener.says {
					assert.Contains(t, result.Message, text)
				}
				assert.NotContains(t, result.Message, "Herdr", "the desktop app is no longer a way to open the page")
				assert.Equal(t, "views-team", result.Team)
				assert.Equal(t, "#views-team-8f3a", result.Channel)
				assert.Equal(t, roster.handle, result.Handle)
			})
		}
	}
}
