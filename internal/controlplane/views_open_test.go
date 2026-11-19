// This file verifies private browser handoff at the generated caller boundary.
package controlplane

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
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
