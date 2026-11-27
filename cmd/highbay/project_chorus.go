// This file resolves the local Chorus server and binds Chorus's generated
// client to it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	genchorusclient "github.com/goadesign/chorus/gen/http/view/client"
	genchorus "github.com/goadesign/chorus/gen/view"
	"github.com/goadesign/highbay/internal/project"
	goahttp "goa.design/goa/v3/http"
)

// chorusRequestTimeout bounds one request to Chorus. Reading the swarm waits
// up to 60 seconds for a change, so the bound leaves room after that wait.
const chorusRequestTimeout = 65 * time.Second

// chorusClient binds Chorus's generated view client to the server at host,
// over a private transport that uses no proxy and follows no redirect.
func chorusClient(host string) *genchorus.Client {
	doer := &http.Client{
		Timeout:       chorusRequestTimeout,
		Transport:     &http.Transport{Proxy: nil},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	client := genchorusclient.NewClient("http", host, doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	return genchorus.NewClient(client.Swarms(), client.View(), client.PostMessage())
}

// connectChorus resolves the healthy local Chorus server and returns Chorus's
// generated client for it. When Chorus is not running it starts it with
// `CHORUS_NO_SETUP=1 chorus up` (without model provider settings or repeating
// global client setup), so users never have to run `chorus up` themselves.
// The team calls it when it first needs Chorus and again after a failed call.
func (c teamCommands) connectChorus(ctx context.Context) (project.TeamChorus, error) {
	raw, err := c.run(ctx, c.chorusEnv, c.chorus, "status", "-json")
	if err != nil {
		if _, upErr := c.run(ctx, c.chorusUpEnv(), c.chorus, "up"); upErr != nil {
			return nil, errors.New("Chorus managed address is unavailable")
		}
		raw, err = c.run(ctx, c.chorusEnv, c.chorus, "status", "-json")
		if err != nil {
			return nil, errors.New("Chorus managed address is unavailable")
		}
	}
	var status struct {
		// BaseURL is the HTTP origin returned by chorus status -json.
		BaseURL string `json:"base_url"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return nil, errors.New("Chorus returned invalid managed-address JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("Chorus returned trailing managed-address data")
	}
	origin, err := url.Parse(status.BaseURL)
	if err != nil || origin.Scheme != "http" || origin.Hostname() == "" || status.BaseURL != "http://"+origin.Host {
		return nil, errors.New("Chorus returned an invalid managed HTTP origin")
	}
	return chorusClient(origin.Host), nil
}

// chorusUpEnv builds the environment for auto-starting Chorus (`CHORUS_NO_SETUP=1`)
// and prepends the resolved hc directory to PATH when outside the server's PATH
// so Chorus's Herdr Core driver stays enabled across Homebrew and desktop-IDE runs.
func (c teamCommands) chorusUpEnv() []string {
	var hcDir string
	if c.resolveHC != nil {
		if hcPath, err := c.resolveHC(); err == nil {
			hcDir = filepath.Dir(hcPath)
		}
	}
	upEnv := make([]string, 0, len(c.chorusEnv)+2)
	sawPath := false
	for _, entry := range c.chorusEnv {
		if strings.HasPrefix(entry, "CHORUS_NO_SETUP=") {
			continue
		}
		if hcDir != "" && strings.HasPrefix(entry, "PATH=") {
			sawPath = true
			existing := filepath.SplitList(strings.TrimPrefix(entry, "PATH="))
			if !slices.Contains(existing, hcDir) {
				existing = append([]string{hcDir}, existing...)
			}
			upEnv = append(upEnv, "PATH="+strings.Join(existing, string(os.PathListSeparator)))
			continue
		}
		upEnv = append(upEnv, entry)
	}
	if hcDir != "" && !sawPath {
		upEnv = append(upEnv, "PATH="+hcDir)
	}
	return append(upEnv, "CHORUS_NO_SETUP=1")
}
