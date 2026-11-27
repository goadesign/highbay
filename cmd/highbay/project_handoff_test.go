// These tests cover the handoff of memory-only server state to the process
// that replaces a project server.
package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/modelsettings"
)

// handoffStage names, in a helper process's environment, which half of the
// replacement TestServerHandoffCrossesProcessReplacement asked it to play.
const handoffStage = "HIGHBAY_TEST_HANDOFF_STAGE"

// fixtureHandoff returns a handoff with two sessions, the settings of both
// providers and a direct model choice.
func fixtureHandoff(t *testing.T) (serverHandoff, controlplane.ModelState) {
	t.Helper()
	anthropic, err := modelsettings.New("anthropic", map[string]string{
		"ANTHROPIC_API_KEY": "sentinel-handoff-key", "ANTHROPIC_BASE_URL": "https://gateway.invalid/sentinel-handoff-path/",
	})
	require.NoError(t, err)
	openai, err := modelsettings.New("openai", map[string]string{"OPENAI_API_KEY": "sentinel-handoff-openai"})
	require.NoError(t, err)
	model := controlplane.ModelState{
		Settings:   []modelsettings.Settings{anthropic, openai},
		Connection: genhighbay.NewConnectionDirect(&genhighbay.ModelDirect{Provider: "openai", ModelName: "gpt-5.4-mini"}),
	}
	sessions := map[string]string{
		strings.Repeat("a", 64): "user-" + strings.Repeat("b", 64),
		strings.Repeat("c", 64): "user-" + strings.Repeat("d", 64),
	}
	return newServerHandoff(sessions, model), model
}

// assertModelState compares two model states by what they hold. The
// settings keep their values behind functions, which do not compare.
func assertModelState(t *testing.T, want, got controlplane.ModelState) {
	t.Helper()
	describe := func(state controlplane.ModelState) (map[string][]string, genhighbay.Connection) {
		settings := map[string][]string{}
		for _, held := range state.Settings {
			settings[held.Provider()] = held.Environ()
		}
		return settings, state.Connection
	}
	wantSettings, wantConnection := describe(want)
	gotSettings, gotConnection := describe(got)
	assert.Equal(t, wantSettings, gotSettings)
	assert.Equal(t, wantConnection, gotConnection)
	assert.Len(t, got.Settings, len(want.Settings))
}

// takeOffered reads back, in this process, a handoff offered on read. The
// reader owns the descriptor it is given, so it gets a duplicate and read
// is closed: one descriptor never has two owners in this process.
func takeOffered(t *testing.T, read *os.File, entry string) (serverHandoff, bool, error) {
	t.Helper()
	name, value, _ := strings.Cut(entry, "=")
	require.Equal(t, handoffVariable, name)
	require.Equal(t, strconv.FormatUint(uint64(read.Fd()), 10), value)
	inherited, err := unix.Dup(int(read.Fd()))
	require.NoError(t, err)
	require.NoError(t, read.Close())
	t.Setenv(handoffVariable, strconv.Itoa(inherited))
	handoff, found, err := takeServerHandoff()
	_, declared := os.LookupEnv(handoffVariable)
	assert.False(t, declared, "the variable is removed whatever the outcome, so no program started later sees it")
	return handoff, found, err
}

// TestServerHandoffRoundTrip checks that what one server offers the next one
// reads: the sessions, each provider's settings and the model choice, and
// that a server with nothing in memory hands over nothing.
func TestServerHandoffRoundTrip(t *testing.T) {
	t.Run("sessions, settings and a model choice", func(t *testing.T) {
		offered, model := fixtureHandoff(t)
		read, entry, err := offered.offer()
		require.NoError(t, err)
		taken, found, err := takeOffered(t, read, entry)
		require.NoError(t, err)
		require.True(t, found)
		assert.Equal(t, offered.Sessions, taken.Sessions)
		state, err := taken.modelState()
		require.NoError(t, err)
		assertModelState(t, model, state)
	})

	t.Run("nothing in memory", func(t *testing.T) {
		none := genhighbay.NewConnectionNone(&genhighbay.ModelNone{})
		offered := newServerHandoff(nil, controlplane.ModelState{Connection: none})
		read, entry, err := offered.offer()
		require.NoError(t, err)
		taken, found, err := takeOffered(t, read, entry)
		require.NoError(t, err)
		require.True(t, found)
		assert.Empty(t, taken.Sessions)
		state, err := taken.modelState()
		require.NoError(t, err)
		assert.Equal(t, controlplane.ModelState{}, state, "no choice is handed over as none, which a new control plane starts with")
	})

	t.Run("an ordinary start", func(t *testing.T) {
		require.NoError(t, os.Unsetenv(handoffVariable))
		_, found, err := takeServerHandoff()
		require.NoError(t, err)
		assert.False(t, found)
	})
}

// TestServerHandoffRefusals checks every handoff the next server refuses,
// and that no refusal quotes what the handoff held.
func TestServerHandoffRefusals(t *testing.T) {
	// offerRaw names a pipe's read end in the environment and writes raw to
	// it from another goroutine, since raw may be more than a pipe holds
	// while nobody reads.
	offerRaw := func(t *testing.T, raw string) {
		t.Helper()
		read, write, err := os.Pipe()
		require.NoError(t, err)
		go func() {
			// The reader stops at its limit and closes the pipe, which
			// ends a longer write with an error.
			_, _ = write.WriteString(raw)
			_ = write.Close()
		}()
		// The next server owns the descriptor it is given, so it gets one
		// that no file of this test also owns.
		inherited, err := unix.Dup(int(read.Fd()))
		require.NoError(t, err)
		require.NoError(t, read.Close())
		t.Setenv(handoffVariable, strconv.Itoa(inherited))
	}
	for _, test := range []struct {
		name  string
		offer func(*testing.T)
		want  string
	}{
		{name: "a name that is not a number", offer: func(t *testing.T) { t.Setenv(handoffVariable, "three") }, want: "does not name an inherited descriptor"},
		{name: "a standard stream", offer: func(t *testing.T) { t.Setenv(handoffVariable, "0") }, want: "does not name an inherited descriptor"},
		{name: "a descriptor that is not open", offer: func(t *testing.T) { t.Setenv(handoffVariable, "1000000") }, want: "read the server handoff"},
		{name: "not JSON", offer: func(t *testing.T) { offerRaw(t, "sentinel-handoff-key") }, want: "is not valid"},
		{name: "an unknown field", offer: func(t *testing.T) {
			offerRaw(t, `{"sessions":{},"model_settings":{},"herdr_env":["sentinel-handoff-key"]}`)
		}, want: "is not valid"},
		{name: "a second value", offer: func(t *testing.T) { offerRaw(t, `{"sessions":{},"model_settings":{}} {}`) }, want: "trailing data"},
		{name: "more than a handoff holds", offer: func(t *testing.T) { offerRaw(t, strings.Repeat(" ", handoffLimit+1)) }, want: "too large"},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.offer(t)
			_, found, err := takeServerHandoff()
			require.ErrorContains(t, err, test.want)
			assert.False(t, found)
			assert.NotContains(t, err.Error(), "sentinel")
			_, declared := os.LookupEnv(handoffVariable)
			assert.False(t, declared)
		})
	}

	for _, test := range []struct {
		name    string
		handoff serverHandoff
		want    string
	}{
		{
			name:    "settings of an unknown provider",
			handoff: serverHandoff{ModelSettings: map[string]map[string]string{"other": {"OTHER_API_KEY": "sentinel-handoff-key"}}},
			want:    "unknown model provider",
		},
		{
			name:    "settings a hand-over would refuse",
			handoff: serverHandoff{ModelSettings: map[string]map[string]string{"anthropic": {"ANTHROPIC_BASE_URL": "sentinel-handoff-key"}}},
			want:    "anthropic settings are not valid",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.handoff.modelState()
			require.ErrorContains(t, err, test.want)
			assert.NotContains(t, err.Error(), "sentinel")
		})
	}
}

// TestServerHandoffTooLargeForAPipe checks that a handoff no pipe can hold
// fails within the write's bound instead of blocking the restart, and leaves
// no descriptor behind. It waits out that bound once (handoffWriteWait).
func TestServerHandoffTooLargeForAPipe(t *testing.T) {
	sessions := map[string]string{}
	for i := range 20000 {
		sessions[strings.Repeat("a", 56)+strconv.FormatInt(int64(i+0x10000000), 16)] = "user-" + strings.Repeat("b", 64)
	}
	read, _, err := newServerHandoff(sessions, controlplane.ModelState{}).offer()
	require.ErrorContains(t, err, "does not fit in a pipe")
	assert.Nil(t, read)
	assert.NotContains(t, err.Error(), strings.Repeat("a", 56))
}

// TestServerHandoffCrossesProcessReplacement proves the one thing a test in
// a single process cannot: the next program image inherits the handoff. A
// helper process offers a handoff and replaces itself, as an updated server
// does; the program it becomes takes the handoff and reports it.
//
// It starts this test executable once more (about 50 ms); nothing cheaper
// observes what survives the replacement of a process.
func TestServerHandoffCrossesProcessReplacement(t *testing.T) {
	switch os.Getenv(handoffStage) {
	case "offer":
		offered, _ := fixtureHandoff(t)
		read, entry, err := offered.offer()
		if err != nil {
			os.Exit(3)
		}
		// The first entry for a name wins, so the stage is replaced, not added.
		env := []string{handoffStage + "=take", entry}
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, handoffStage+"=") {
				env = append(env, kv)
			}
		}
		err = syscall.Exec(os.Args[0], os.Args, env)
		// Reached only when the replacement failed.
		_ = read
		os.Stderr.WriteString(err.Error())
		os.Exit(4)
	case "take":
		taken, found, err := takeServerHandoff()
		if err != nil || !found {
			os.Exit(5)
		}
		_, leaked := os.LookupEnv(handoffVariable)
		report, err := json.Marshal(struct {
			Handoff serverHandoff `json:"handoff"`
			Leaked  bool          `json:"leaked"`
		}{taken, leaked})
		if err != nil {
			os.Exit(6)
		}
		os.Stdout.Write(append([]byte("HANDOFF "), append(report, '\n')...))
		os.Exit(0)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	helper := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerHandoffCrossesProcessReplacement$")
	helper.Env = append([]string{handoffStage + "=offer"}, os.Environ()...)
	out, err := helper.Output()
	require.NoError(t, err, "the helper process failed: %s", out)
	_, line, reported := strings.Cut(string(out), "HANDOFF ")
	require.True(t, reported, "the replaced process reported nothing: %s", out)
	line, _, _ = strings.Cut(line, "\n")
	var report struct {
		Handoff serverHandoff `json:"handoff"`
		Leaked  bool          `json:"leaked"`
	}
	require.NoError(t, json.Unmarshal([]byte(line), &report))
	want, model := fixtureHandoff(t)
	assert.Equal(t, want.Sessions, report.Handoff.Sessions)
	state, err := report.Handoff.modelState()
	require.NoError(t, err)
	assertModelState(t, model, state)
	assert.False(t, report.Leaked, "the replaced process still had the handoff's name in its environment")
}
