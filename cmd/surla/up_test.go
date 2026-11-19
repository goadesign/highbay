package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpRunsSetupThenChorus(t *testing.T) {
	cases := []struct {
		name     string
		setup    bool
		chorus   string
		startErr error
		want     []string
		wantErr  string
	}{
		{
			name:   "setup and Chorus",
			setup:  true,
			chorus: "/usr/local/bin/chorus",
			want:   []string{"Installed the /surla skill", "chorus up ran", "Surla is ready"},
		},
		{
			name:  "Chorus not installed",
			setup: true,
			want:  []string{"Installed the /surla skill", "Chorus is not installed", "Surla is ready"},
		},
		{
			name:   "setup skipped",
			chorus: "/usr/local/bin/chorus",
			want:   []string{"chorus up ran", "Surla is ready"},
		},
		{
			name:     "Chorus fails",
			chorus:   "/usr/local/bin/chorus",
			startErr: errors.New("exit status 1"),
			wantErr:  "start Chorus: exit status 1",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			var env *setupEnvironment
			if c.setup {
				setup := testSetupEnvironment(home)
				env = &setup
			}
			var started []string
			start := func(_ context.Context, chorus string, out io.Writer) error {
				started = append(started, chorus)
				if c.startErr != nil {
					return c.startErr
				}
				_, err := io.WriteString(out, "chorus up ran\n")
				return err
			}
			var out bytes.Buffer
			err := upWith(env, c.chorus, start, &out)
			if c.chorus != "" {
				assert.Equal(t, []string{c.chorus}, started)
			} else {
				assert.Empty(t, started)
			}
			if c.wantErr != "" {
				assert.EqualError(t, err, c.wantErr)
				return
			}
			require.NoError(t, err)
			last := -1
			for _, line := range c.want {
				at := bytes.Index(out.Bytes(), []byte(line))
				assert.Greater(t, at, last, "%q in order in %s", line, out.String())
				last = at
			}
			_, statErr := os.Stat(filepath.Join(home, ".agents", "skills", "surla", "SKILL.md"))
			assert.Equal(t, c.setup, statErr == nil, "setup ran only when asked")
		})
	}
}

func TestUpNamesTheSkipWhenSetupFails(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".cursor"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".cursor", "mcp.json"), []byte("{"), 0o644))
	env := testSetupEnvironment(home)
	err := upWith(&env, "", nil, io.Discard)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse JSON in "+filepath.Join(home, ".cursor", "mcp.json"))
	assert.Contains(t, err.Error(), "set SURLA_NO_SETUP=1 to skip setup")
}

func TestUpStartsChorusWithoutProviderSettings(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "provider-sentinel")
	t.Setenv("SURLA_UP_TEST_MARK", "kept")
	chorus := filepath.Join(t.TempDir(), "chorus")
	script := "#!/bin/sh\necho \"args=$* key=${ANTHROPIC_API_KEY:-unset} mark=${SURLA_UP_TEST_MARK:-unset}\"\n"
	require.NoError(t, os.WriteFile(chorus, []byte(script), 0o755))
	var out bytes.Buffer
	require.NoError(t, startChorus(t.Context(), chorus, &out))
	assert.Equal(t, "args=up key=unset mark=kept\n", out.String())
}
