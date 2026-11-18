// This file proves explicit link delivery through a running project owner.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrintedProjectLinksStartsAndReattaches exercises server startup, minted
// links and actual browser exchange without starting Herdr, a browser or a model.
func TestPrintedProjectLinksStartsAndReattaches(t *testing.T) {
	t.Chdir(t.TempDir())
	isolateProviderFamilies(t)
	runtime := projectDispatchRuntime(t)
	runtime.preflight = func(openBrowser bool) error {
		assert.False(t, openBrowser)
		return nil
	}
	var previous string
	for index := range 2 {
		var output, diagnostics bytes.Buffer
		require.NoError(t, runtime.printLink(&output, &diagnostics))
		var link genproject.BrowserLinkResult
		require.NoError(t, json.Unmarshal(output.Bytes(), &link))
		assert.NotEqual(t, previous, link.URL)
		previous = link.URL
		expires, err := time.Parse(time.RFC3339Nano, link.ExpiresAt)
		require.NoError(t, err)
		assert.WithinDuration(t, time.Now().Add(2*time.Minute), expires, 5*time.Second)
		assert.Contains(t, diagnostics.String(), "Surla is running for")
		assert.NotContains(t, diagnostics.String(), "#link=")
		if index == 0 {
			assert.Contains(t, diagnostics.String(), "Created a new, empty Surla project")
		} else {
			assert.NotContains(t, diagnostics.String(), "Created a new, empty Surla project")
		}
		address, err := url.Parse(link.URL)
		require.NoError(t, err)
		origin := address.Scheme + "://" + address.Host
		body := `{"link":"` + strings.TrimPrefix(address.Fragment, "link=") + `"}`
		for attempt, want := range []int{http.StatusCreated, http.StatusUnprocessableEntity} {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, origin+"/project/browser-session", strings.NewReader(body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", origin)
			request.Header.Set("Sec-Fetch-Site", "same-origin")
			response, err := http.DefaultClient.Do(request)
			require.NoError(t, err)
			assert.Equal(t, want, response.StatusCode, "exchange attempt %d", attempt)
			require.NoError(t, response.Body.Close())
		}
	}
	// Team failure preserves the usable page link and the existing nonzero result.
	startTeam := runtime.startTeamPrograms
	runtime.startTeamPrograms = func(context.Context) error { return errors.New("team fixture unavailable") }
	var partial, diagnostics bytes.Buffer
	assert.ErrorContains(t, runtime.printLink(&partial, &diagnostics), "team fixture unavailable")
	var usable genproject.BrowserLinkResult
	require.NoError(t, json.Unmarshal(partial.Bytes(), &usable))
	assert.Contains(t, usable.URL, "/views#link=")
	runtime.startTeamPrograms = startTeam
	// The original route remains available and opens one new link on the same server.
	runtime.preflight = func(openBrowser bool) error { assert.True(t, openBrowser); return nil }
	var output bytes.Buffer
	require.NoError(t, runtime.start(&output))
	assert.NotContains(t, output.String(), "#link=")
}

// TestPrintedLinkNeedsNoOpener reaches project discovery with only a Git stand-in
// on PATH. The browser route's existing test proves refusal before state creation.
func TestPrintedLinkNeedsNoOpener(t *testing.T) {
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexit 0\n"), 0o700))
	t.Setenv("PATH", bin)
	runtime := testProjectRuntime()
	runtime.preflight = func(openBrowser bool) error {
		return preflightProjectStart(func() (string, error) { return "owned-hc", nil }, openBrowser)
	}
	runtime.configRoot = func() (string, error) { return "", assert.AnError }
	var output bytes.Buffer
	err := runWithRuntime([]string{"--print-link"}, &output, runtime)
	assert.ErrorIs(t, err, assert.AnError)
	assert.Empty(t, output.String())
	// A preflight rejection never delivers a credential.
	runtime.preflight = func(bool) error { return context.Canceled }
	assert.ErrorIs(t, runtime.printLink(&output, &bytes.Buffer{}), context.Canceled)
	assert.Empty(t, output.String())
}
