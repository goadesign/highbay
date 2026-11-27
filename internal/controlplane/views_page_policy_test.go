// This file checks the Content-Security-Policy the views page is served with.
package controlplane

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestServedPagePolicyNamesOnlyALoopbackWebSocket serves the views page for
// each Host header and reads the policy it answers with. The page may always
// connect to the address it was loaded from ('self'). The policy also names
// that address's WebSocket, ws://<host>, only when the Host is a loopback IP
// address followed by a port number written the way a browser sends it; a
// host name, another address, a missing port, port 0, a port above 65535 and
// a port with a leading zero get 'self' alone. The rest of the policy is the
// same for every Host.
func TestServedPagePolicyNamesOnlyALoopbackWebSocket(t *testing.T) {
	for _, tc := range []struct {
		host string
		// webSocket reports whether the policy names ws://<host>.
		webSocket bool
	}{
		{host: "127.0.0.1:54120", webSocket: true},
		{host: "[::1]:54120", webSocket: true},
		{host: "localhost:54120"},
		{host: "example.com:80"},
		{host: "127.0.0.1"},
		{host: "127.0.0.1:0"},
		{host: "127.0.0.1:99999"},
		{host: "127.0.0.1:054120"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/views", nil)
			request.Host = tc.host
			response := httptest.NewRecorder()
			ViewsPage().ServeHTTP(response, request)
			policy := response.Header().Get("Content-Security-Policy")
			assert.Equal(t, viewsPagePolicy(tc.host), policy, "the page was served with another policy than its Host's")

			connect := "connect-src 'self'"
			if tc.webSocket {
				connect += " ws://" + tc.host
			}
			assert.Equal(t, "default-src 'none'; script-src "+viewsPageScriptSource+"; style-src 'unsafe-inline'; "+
				connect+"; frame-ancestors 'none'; form-action 'none'; base-uri 'none'", policy)
			assert.Equal(t, tc.webSocket, strings.Contains(policy, "ws:"))
		})
	}
}
