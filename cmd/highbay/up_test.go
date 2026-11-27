package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/highbay/internal/depinstall"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpStartsRegisteredCompanionsInOrder(t *testing.T) {
	dir := upTestEnvironment(t)
	require.NoError(t, os.Mkdir(filepath.Join(dir, ".cursor"), 0o700))
	t.Setenv("ANTHROPIC_API_KEY", "provider-sentinel")
	hc := filepath.Join(dir, "hc")
	chorus := filepath.Join(dir, "chorus")
	require.NoError(t, os.WriteFile(hc, []byte("#!/bin/sh\nprintf 'hc %s\\n' \"$*\"\n"), 0o755))
	require.NoError(t, os.WriteFile(chorus, []byte(`#!/bin/sh
printf 'chorus %s no_setup=%s key=%s hc=%s\n' "$*" "$CHORUS_NO_SETUP" "${ANTHROPIC_API_KEY:-unset}" "$(command -v hc)"
`), 0o755))
	var out bytes.Buffer
	require.NoError(t, up(true, &out, func(_ context.Context, opts depinstall.Options) (depinstall.Tools, error) {
		assert.True(t, opts.Yes)
		assert.Same(t, os.Stdin, opts.Input)
		assert.Equal(t, dir, opts.InstallDir)
		assert.True(t, filepath.IsAbs(opts.HighbayPath))
		return depinstall.Tools{HC: hc, Chorus: chorus}, nil
	}, func() (setupEnvironment, error) {
		return testSetupEnvironment(dir), nil
	}))
	lines := []string{
		"Installed the /highbay skill", "hc server start",
		"chorus up no_setup=1 key=unset hc=" + hc, "Highbay is ready",
	}
	last := -1
	for _, line := range lines {
		at := strings.Index(out.String(), line)
		assert.Greater(t, at, last, "%q in order in %s", line, out.String())
		last = at
	}
}

func TestUpDependencyFailureAndDependencyOnlySkipSetup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noStart string
		refusal error
	}{
		{name: "installation declined", refusal: errors.New("installation declined")},
		{name: "dependency-only", noStart: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := upTestEnvironment(t)
			t.Setenv("HIGHBAY_NO_START", tc.noStart)
			called := false
			err := up(false, io.Discard, func(_ context.Context, opts depinstall.Options) (depinstall.Tools, error) {
				called = true
				assert.False(t, opts.Yes)
				return depinstall.Tools{}, tc.refusal
			}, func() (setupEnvironment, error) {
				t.Error("setup ran before dependency consent or with HIGHBAY_NO_START")
				return setupEnvironment{}, errors.New("unexpected setup")
			})
			assert.True(t, called)
			assert.ErrorIs(t, err, tc.refusal)
			_, err = os.Stat(filepath.Join(dir, ".agents"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestUpSkipsSetupAndServicesWhenExplicitlyDisabled(t *testing.T) {
	upTestEnvironment(t)
	t.Setenv("HIGHBAY_NO_DEPS", "1")
	t.Setenv("HIGHBAY_NO_START", "1")
	// No install directory is needed when both phases are skipped.
	t.Setenv("HIGHBAY_INSTALL_DIR", "relative")
	require.NoError(t, runUp([]string{"--yes"}, io.Discard))
	require.NoError(t, runUp([]string{"-y"}, io.Discard))
}

func TestUpNamesTheSkipWhenSetupFails(t *testing.T) {
	dir := upTestEnvironment(t)
	config := filepath.Join(dir, ".cursor", "mcp.json")
	writeTestFile(t, config, "{")
	err := up(false, io.Discard, func(context.Context, depinstall.Options) (depinstall.Tools, error) {
		return depinstall.Tools{}, nil
	}, func() (setupEnvironment, error) {
		return testSetupEnvironment(dir), nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse JSON in "+config)
	assert.Contains(t, err.Error(), "set HIGHBAY_NO_SETUP=1 to skip setup")
}

func TestUpDependencyDiscoveryUsesOnlyDurableNPMCompanions(t *testing.T) {
	cacheRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	npxInstall := filepath.Join(cacheRoot, "_npx", "key")
	npmBin := filepath.Join(npxInstall, "node_modules", ".bin")
	require.NoError(t, os.MkdirAll(npmBin, 0o700))
	launcher := filepath.Join(npxInstall, "node_modules", "@goadesign", "highbay", "bin", "highbay.js")
	alias := filepath.Join(t.TempDir(), "npx-cache")
	require.NoError(t, os.Symlink(cacheRoot, alias))
	aliasBin := filepath.Join(alias, "_npx", "key", "node_modules", ".bin")
	for _, tc := range []struct {
		name, launcher, bin string
		persistent          bool
		removeNPMBin        bool
	}{
		{name: "global", launcher: "/installed/highbay.js", bin: npmBin, persistent: true},
		{name: "npx", launcher: launcher, bin: npmBin, removeNPMBin: true},
		{name: "symlinked npx cache", launcher: launcher, bin: aliasBin, removeNPMBin: true},
		{name: "Windows npx", launcher: `C:\cache\_npx\key\highbay.js`, bin: npmBin},
		{name: "absent", bin: npmBin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "companions")
			other := filepath.Join(t.TempDir(), "native")
			sep := string(os.PathListSeparator)
			path := strings.Join([]string{cache, tc.bin, other, cache}, sep)
			t.Setenv("HIGHBAY_NPM_LAUNCHER", tc.launcher)
			t.Setenv("HIGHBAY_COMPANION_BIN_DIR", cache)
			t.Setenv("PATH", path)
			want := tc.bin + sep + other
			switch {
			case tc.persistent:
				want = path
			case tc.removeNPMBin:
				want = other
			}

			refused := errors.New("installation declined")
			_, err := ensureUpDependencies(t.Context(), depinstall.Options{},
				func(context.Context, depinstall.Options) (depinstall.Tools, error) {
					assert.Equal(t, want, os.Getenv("PATH"))
					return depinstall.Tools{}, refused
				})
			assert.ErrorIs(t, err, refused)
			assert.Equal(t, path, os.Getenv("PATH"))
		})
	}
}

func TestUpClosedInputCannotPrompt(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	require.NoError(t, err)
	assert.False(t, upInteractive(devNull))
	require.NoError(t, devNull.Close())
}

// upTestEnvironment isolates the real setup path from local coding-agent files.
func upTestEnvironment(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	for _, name := range []string{
		"CLAUDE_CONFIG_DIR", "CODEX_HOME", "GEMINI_CLI_HOME", "COPILOT_HOME", "HERDR_APP_HOME", "XDG_CONFIG_HOME",
		"HIGHBAY_NO_START", "HIGHBAY_NO_SETUP", "HIGHBAY_NO_DEPS", "HIGHBAY_NPM_LAUNCHER",
		"HIGHBAY_COMPANION_BIN_DIR",
	} {
		t.Setenv(name, "")
	}
	// The subprocess start test proves the installed hc does not need to be on PATH.
	t.Setenv("PATH", "")
	t.Setenv("HIGHBAY_INSTALL_DIR", dir)
	return dir
}
