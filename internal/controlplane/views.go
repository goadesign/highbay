// This file serves the views page: the fixed browser shell that holds no
// project data and talks only to the address it was loaded from.
package controlplane

import (
	_ "embed"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	"go.opentelemetry.io/otel"
)

// viewsPageHTML is the views page built from ui/checkpoint into checkpoint.html.
// It is the same for every project and caller: data arrives only through the
// requests the page sends with the session it obtains.
//
//go:embed checkpoint.html
var viewsPageHTML string

// ViewsPageScriptSHA256 is the base64 SHA-256 digest of the views page's one
// inline script, as the build recorded it. It identifies the page this
// server serves: the project server reports it to an open page, which
// compares it with its own to notice that a newer server has replaced the
// one it was loaded from.
var ViewsPageScriptSHA256 = strings.TrimSuffix(strings.TrimPrefix(viewsPageScriptSource, "'sha256-"), "'")

// ViewsPage returns the handler for the views page. The composition root
// mounts it at exact GET /views without a credential, behind the project
// server's address and browser-mark checks, which also mark every response
// as no-sniff and no-store. The page's security policy travels as a response
// header so it covers the document before any of it is parsed.
func ViewsPage() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", viewsPagePolicy(r.Host))
		if _, err := io.WriteString(w, viewsPageHTML); err != nil {
			otel.Handle(err)
		}
	})
}

// viewsPagePolicy is the views page's Content-Security-Policy for a page
// loaded from host. The page may run only the script whose digest the build
// recorded, so no other inline, injected or loaded script runs; it may use
// inline styles, send requests only to the address it was loaded from, load
// nothing else, never be shown inside another page, submit no form anywhere,
// and never change its base address.
//
// The terminal drawer opens a WebSocket to the same address. Not every
// browser lets 'self' cover the ws scheme, so the policy also names that one
// WebSocket address when host is a loopback IP address with a port, the only
// kind of address a project server listens on. Any other host gets 'self'
// alone.
func viewsPagePolicy(host string) string {
	connect := "'self'"
	if loopbackAddress(host) {
		connect += " ws://" + host
	}
	return "default-src 'none'; script-src " + viewsPageScriptSource + "; " +
		"style-src 'unsafe-inline'; connect-src " + connect + "; frame-ancestors 'none'; form-action 'none'; " +
		"base-uri 'none'"
}

// loopbackAddress reports whether host is a loopback IP address followed by a
// port number, written exactly as a browser sends it in the Host header.
func loopbackAddress(host string) bool {
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(name)
	if err != nil || !addr.IsLoopback() {
		return false
	}
	number, err := strconv.ParseUint(port, 10, 16)
	return err == nil && number > 0 && strconv.FormatUint(number, 10) == port
}
