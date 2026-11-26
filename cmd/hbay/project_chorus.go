// This file binds team observations and reminders to Chorus's managed address.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/goadesign/highbay/internal/project"
)

// connectChorus resolves the healthy managed server at an accepted team start,
// starting Chorus with `CHORUS_NO_SETUP=1 chorus up` (without model provider
// settings or repeating global client setup) when it is not already running so
// users never have to run `hbay up` or `chorus up` before invoking `/highbay`.
// Both adapters keep that immutable origin until the next explicit team start;
// no command runs at server construction or during watcher polling.
func (c teamCommands) connectChorus(ctx context.Context) (project.TeamConnection, error) {
	raw, err := c.run(ctx, c.chorusEnv, c.chorus, "status", "-json")
	if err != nil {
		upEnv := make([]string, 0, len(c.chorusEnv)+1)
		for _, entry := range c.chorusEnv {
			if !strings.HasPrefix(entry, "CHORUS_NO_SETUP=") {
				upEnv = append(upEnv, entry)
			}
		}
		upEnv = append(upEnv, "CHORUS_NO_SETUP=1")
		if _, upErr := c.run(ctx, upEnv, c.chorus, "up"); upErr != nil {
			return project.TeamConnection{}, errors.New("Chorus managed address is unavailable")
		}
		raw, err = c.run(ctx, c.chorusEnv, c.chorus, "status", "-json")
		if err != nil {
			return project.TeamConnection{}, errors.New("Chorus managed address is unavailable")
		}
	}
	var status struct {
		// BaseURL is the HTTP origin returned by Chorus v0.14.0 status -json.
		BaseURL string `json:"base_url"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil {
		return project.TeamConnection{}, errors.New("Chorus returned invalid managed-address JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return project.TeamConnection{}, errors.New("Chorus returned trailing managed-address data")
	}
	origin, err := url.Parse(status.BaseURL)
	if err != nil || origin.Scheme != "http" || origin.Hostname() == "" || status.BaseURL != "http://"+origin.Host {
		return project.TeamConnection{}, errors.New("Chorus returned an invalid managed HTTP origin")
	}
	return project.TeamConnection{
		Endpoint: status.BaseURL,
		Observer: teamObservations{
			commands: c, stream: runTeamStream, chorusURL: status.BaseURL,
			client: &http.Client{Timeout: 65 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		},
		Sender: newTeamChorusSender(status.BaseURL),
	}, nil
}
