// This file checks Chorus's status result before it becomes a client, and that
// the client reaches Chorus's generated server.
package main

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	genchorusserver "github.com/goadesign/chorus/gen/http/view/server"
	genchorus "github.com/goadesign/chorus/gen/view"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

// TestChorusManagedAddressRefusals keeps malformed output and command failures
// from becoming a client.
func TestChorusManagedAddressRefusals(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		err       error
	}{
		{name: "command failure", err: assert.AnError},
		{name: "invalid JSON", raw: "private invalid reply"},
		{name: "null", raw: "null"},
		{name: "missing origin", raw: `{}`},
		{name: "unknown field", raw: `{"base_url":"http://localhost:9550","private":"value"}`},
		{name: "second value", raw: `{"base_url":"http://localhost:9550"} {}`},
		{name: "invalid host", raw: `{"base_url":"http://["}`},
		{name: "credential", raw: `{"base_url":"http://private:secret@localhost:9550"}`},
		{name: "path", raw: `{"base_url":"http://localhost:9550/mcp"}`},
		{name: "empty query", raw: `{"base_url":"http://localhost:9550?"}`},
		{name: "empty fragment", raw: `{"base_url":"http://localhost:9550#"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			commands := teamCommands{run: func(context.Context, []string, string, ...string) ([]byte, error) {
				return []byte(test.raw), test.err
			}}
			connection, err := commands.connectChorus(t.Context())
			require.Error(t, err)
			assert.NotContains(t, err.Error(), "private")
			assert.Nil(t, connection)
		})
	}
}

// TestChorusManagedAddressAutoStartsWhenStopped starts Chorus with
// `CHORUS_NO_SETUP=1 chorus up` when the initial `status -json` fails so
// `/highbay` recovers a stopped daemon without repeating one-time client setup.
func TestChorusManagedAddressAutoStartsWhenStopped(t *testing.T) {
	var seen [][]string
	var envs [][]string
	running := false
	commands := teamCommands{
		chorus:    "chorus",
		chorusEnv: []string{"PATH=/usr/bin", "CHORUS_NO_SETUP=0"},
		run: func(_ context.Context, env []string, program string, args ...string) ([]byte, error) {
			envs = append(envs, append([]string(nil), env...))
			seen = append(seen, append([]string{program}, args...))
			if len(args) == 1 && args[0] == "up" {
				running = true
				return []byte("ready\n"), nil
			}
			if !running {
				return nil, assert.AnError
			}
			return []byte(`{"base_url":"http://localhost:9550"}`), nil
		},
	}
	connection, err := commands.connectChorus(t.Context())
	require.NoError(t, err)
	assert.Equal(t, [][]string{
		{"chorus", "status", "-json"},
		{"chorus", "up"},
		{"chorus", "status", "-json"},
	}, seen)
	assert.Equal(t, [][]string{
		{"PATH=/usr/bin", "CHORUS_NO_SETUP=0"},
		{"PATH=/usr/bin", "CHORUS_NO_SETUP=1"},
		{"PATH=/usr/bin", "CHORUS_NO_SETUP=0"},
	}, envs)
	assert.NotNil(t, connection)
}

// TestChorusAutoStartAndServerCompanionFallbackUseLocalBin verifies that a
// project server started with a stripped GUI PATH still finds standalone chorus
// in ~/.local/bin and passes ~/.local/bin on PATH when auto-starting Chorus so
// Chorus's Herdr Core driver finds hc.
func TestChorusAutoStartAndServerCompanionFallbackUseLocalBin(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	require.NoError(t, os.MkdirAll(localBin, 0o755))
	chorusPath := filepath.Join(localBin, "chorus")
	hcPath := filepath.Join(localBin, "hc")
	require.NoError(t, os.WriteFile(chorusPath, []byte("#!/bin/sh\nprintf '%s\\n' \"$PATH\"\n"), 0o755))
	require.NoError(t, os.WriteFile(hcPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin")

	out, err := runServerTeamCommand(t.Context(), []string{"PATH=/usr/bin"}, "chorus", "version")
	require.NoError(t, err)
	assert.Equal(t, "/usr/bin\n", string(out))

	var upEnv []string
	running := false
	commands := teamCommands{
		chorus:    "chorus",
		resolveHC: projectHerdrProgram,
		chorusEnv: []string{"PATH=/usr/bin"},
		run: func(_ context.Context, env []string, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 && args[0] == "up" {
				upEnv = append([]string(nil), env...)
				running = true
				return []byte("ready\n"), nil
			}
			if !running {
				return nil, assert.AnError
			}
			return []byte(`{"base_url":"http://localhost:9550"}`), nil
		},
	}
	connection, err := commands.connectChorus(t.Context())
	require.NoError(t, err)
	assert.NotNil(t, connection)
	assert.Equal(t, []string{
		"PATH=" + localBin + string(os.PathListSeparator) + "/usr/bin",
		"CHORUS_NO_SETUP=1",
	}, upEnv)
}

// chorusService stands in for Chorus behind its own generated server.
type chorusService struct {
	// posted records the last message posted.
	posted *genchorus.PostMessagePayload
	// viewed records the last view request.
	viewed *genchorus.ViewPayload
}

func (c *chorusService) Swarms(context.Context) (*genchorus.SwarmsResult, error) {
	return &genchorus.SwarmsResult{Swarms: []string{}}, nil
}

// View answers "not_modified" to a caller that already has snapshot "e1".
func (c *chorusService) View(_ context.Context, p *genchorus.ViewPayload) (*genchorus.SwarmView, error) {
	c.viewed = p
	if p.IfNoneMatch != nil && *p.IfNoneMatch == "e1" {
		return nil, genchorus.MakeNotModified(errors.New("not modified"))
	}
	watching := "claude-opus"
	return &genchorus.SwarmView{
		Swarm: p.Swarm, Etag: "e1",
		Agents:   []*genchorus.Agent{{Handle: "guide-1", Model: &watching, LastSeenUnix: 7, Channels: []string{"#refunds"}, WatchingOnThisServer: true}},
		Channels: []*genchorus.Channel{{Name: "#refunds", HeadSeq: 1, Positions: []*genchorus.Position{}, Messages: []*genchorus.Message{{Seq: 1, Sender: "guide-1", Text: "Hello", SentAtMs: 9}}}},
		Inboxes:  []*genchorus.Inbox{},
	}, nil
}

func (c *chorusService) PostMessage(_ context.Context, p *genchorus.PostMessagePayload) (*genchorus.PostMessageResult, error) {
	c.posted = p
	if p.Sender == "taken" {
		return nil, genchorus.MakeHandleInUse(errors.New("handle is bound to another swarm"))
	}
	human := true
	return &genchorus.PostMessageResult{Target: p.To, Message: &genchorus.Message{Seq: 2, Sender: p.Sender, Text: p.Text, ReplyTo: p.ReplyTo, SentAtMs: 10, Human: &human, Model: &p.Kind}}, nil
}

// TestChorusClientSpeaksChorusContract runs Highbay's Chorus client against
// Chorus's own generated server, so both ends use Chorus's codecs: reading a
// swarm, waiting on an unchanged one, posting a message, and a refused post.
func TestChorusClientSpeaksChorusContract(t *testing.T) {
	service := &chorusService{}
	mux := goahttp.NewMuxer()
	genchorusserver.Mount(mux, genchorusserver.New(genchorus.NewEndpoints(service), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil))
	server := httptest.NewServer(mux)
	defer server.Close()
	client := chorusClient(server.Listener.Addr().String())

	view, err := client.View(t.Context(), &genchorus.ViewPayload{Swarm: "refunds", Wait: 60})
	require.NoError(t, err)
	assert.Equal(t, "e1", view.Etag)
	require.Len(t, view.Agents, 1)
	assert.True(t, view.Agents[0].WatchingOnThisServer)
	require.Len(t, view.Channels, 1)
	assert.Equal(t, "Hello", view.Channels[0].Messages[0].Text)
	assert.Equal(t, 60, service.viewed.Wait)

	etag := "e1"
	_, err = client.View(t.Context(), &genchorus.ViewPayload{Swarm: "refunds", IfNoneMatch: &etag, Wait: 60})
	var refusal *goa.ServiceError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "not_modified", refusal.Name, "an unchanged swarm is told apart from a failure")

	role, reply := "You", 1
	posted, err := client.PostMessage(t.Context(), &genchorus.PostMessagePayload{
		Swarm: "refunds", Sender: "you-1", Kind: "human", Role: &role, To: "#refunds", Text: "@guide-1 Build refunds", ReplyTo: &reply,
	})
	require.NoError(t, err)
	assert.Equal(t, "#refunds", posted.Target)
	assert.Equal(t, 2, posted.Message.Seq)
	assert.Equal(t, &genchorus.PostMessagePayload{
		Swarm: "refunds", Sender: "you-1", Kind: "human", Role: &role, To: "#refunds", Text: "@guide-1 Build refunds", ReplyTo: &reply,
	}, service.posted)

	_, err = client.PostMessage(t.Context(), &genchorus.PostMessagePayload{Swarm: "refunds", Sender: "taken", Kind: "human", To: "#refunds", Text: "Hello"})
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, "handle_in_use", refusal.Name)
}
