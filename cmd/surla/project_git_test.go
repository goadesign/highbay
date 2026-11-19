// This file exercises the production startup gate with Git executables on PATH.
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/surla/internal/project"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProductionProjectGitPreflight checks capability rather than the version
// label with only Git and a browser opener installed on the test PATH.
func TestProductionProjectGitPreflight(t *testing.T) {
	for _, tc := range []struct {
		name       string
		probe      string
		version    string
		wantError  string
		wantCalls  []string
		canProceed bool
	}{
		{
			name:      "accepted",
			probe:     "printf 'git version 2.45.0\\n'",
			wantCalls: []string{"--no-lazy-fetch version"}, canProceed: true,
		},
		{
			name:      "older label with capability backport",
			probe:     "printf 'git version 2.39.4 (custom backport)\\n'",
			wantCalls: []string{"--no-lazy-fetch version"}, canProceed: true,
		},
		{
			name:      "unknown option",
			probe:     "printf 'unknown option: --no-lazy-fetch\\n' >&2; exit 129",
			version:   "printf 'git version 2.39.5 (Apple Git-154)\\n'",
			wantError: "Git on PATH reports \"git version 2.39.5 (Apple Git-154)\" and does not support --no-lazy-fetch. Install Git 2.45 or newer and run surla again.",
			wantCalls: []string{"--no-lazy-fetch version", "--version"},
		},
		{
			name:      "missing Git",
			wantError: "Git is required to start Surla. Install Git 2.45 or newer and run surla again.",
		},
		{
			name:      "Git execution failure",
			probe:     "printf 'private launcher diagnostic\\n' >&2; exit 70",
			wantError: "checking Git support for --no-lazy-fetch: exit status 70",
			wantCalls: []string{"--no-lazy-fetch version"},
		},
		{
			name:      "unsupported Git version lookup fails",
			probe:     "printf 'unknown option: --no-lazy-fetch\\n' >&2; exit 129",
			version:   "exit 71",
			wantError: "Git does not support --no-lazy-fetch and its version could not be read. Install Git 2.45 or newer and run surla again: exit status 71",
			wantCalls: []string{"--no-lazy-fetch version", "--version"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin := t.TempDir()
			calls := filepath.Join(bin, "git-calls")
			for _, name := range []string{"open", "xdg-open"} {
				require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o700))
			}
			if tc.probe != "" {
				script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SURLA_TEST_GIT_CALLS\"\ncase \"$*\" in\n'--no-lazy-fetch version') " + tc.probe + ";;\n'--version') " + tc.version + ";;\n*) exit 88;;\nesac\n"
				require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
			}
			t.Setenv("PATH", bin)
			t.Setenv("SURLA_TEST_GIT_CALLS", calls)
			t.Chdir(t.TempDir())
			runtime := productionProjectRuntime()
			reachedDiscovery := errors.New("accepted Git reached project discovery")
			discoveries := 0
			runtime.configRoot = func() (string, error) { discoveries++; return "", reachedDiscovery }
			runtime.startTerminal = func(context.Context, *project.State) (string, error) {
				t.Fatal("startup must not launch a terminal in this fixture")
				return "", nil
			}
			runtime.startDaemon = func(*project.State) (*projectDaemon, error) {
				t.Fatal("startup must not launch a daemon in this fixture")
				return nil, nil
			}
			runtime.openViews = func(context.Context, string) error {
				t.Fatal("startup must not open a browser in this fixture")
				return nil
			}
			var output bytes.Buffer
			err := runWithRuntime(nil, &output, runtime)
			if tc.canProceed {
				assert.ErrorIs(t, err, reachedDiscovery)
				assert.Equal(t, 1, discoveries)
			} else {
				assert.EqualError(t, err, tc.wantError)
				assert.Zero(t, discoveries)
			}
			assert.Empty(t, output.String())
			logged, err := os.ReadFile(calls)
			if len(tc.wantCalls) == 0 {
				assert.ErrorIs(t, err, os.ErrNotExist)
			} else {
				require.NoError(t, err)
				assert.Equal(t, strings.Join(tc.wantCalls, "\n")+"\n", string(logged))
			}
		})
	}
}
