package selfupdate

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReleaseVersions keeps published versions separate from development builds.
func TestReleaseVersions(t *testing.T) {
	for _, test := range []struct {
		raw, version string
	}{
		{"v0.5.3", "0.5.3"},
		{"0.5.3", "0.5.3"},
		{"highbay v0.5.3\n", "0.5.3"},
		{"(devel)", ""},
		{"v0.5.3+dirty", ""},
		{"v0.5.3-0.20261126000000-deadbeef", ""},
	} {
		got, stable := NormalizeBuildVersion(test.raw)
		assert.Equal(t, test.version, got, test.raw)
		assert.Equal(t, test.version != "", stable, test.raw)
	}
	for _, bad := range []string{"", "0.1", "0.1.0.0", "01.0.0", "+1.0.0", "-0.0.0", "1.0.0-rc1", "1.0.0\n"} {
		_, err := ParseVersion(bad)
		assert.Error(t, err, bad)
	}
	assert.True(t, VersionBefore("1.2.3", "1.10.0"))
	assert.False(t, VersionBefore("1.10.0", "1.2.3"))
	assert.False(t, VersionBefore("(devel)", "1.2.3"))
}

// TestCanAutoUpdate reports the installer that actually owns each live binary.
func TestCanAutoUpdate(t *testing.T) {
	for _, test := range []struct {
		version, launcher, manual string
	}{
		{"v0.5.3", "", ""},
		{"(devel)", "", "go install github.com/goadesign/highbay/cmd/highbay@latest"},
		{"v0.5.3", "/cache/_npx/one/bin/highbay.js", "npx @goadesign/highbay@latest"},
		{"v0.5.3", "/packages/highbay/bin/highbay.js", "npm install -g @goadesign/highbay@latest"},
	} {
		ok, command := CanAutoUpdate("/bin/highbay", test.version, func(name string) string {
			assert.Equal(t, "HIGHBAY_NPM_LAUNCHER", name)
			return test.launcher
		})
		assert.Equal(t, test.manual == "", ok, test)
		assert.Equal(t, test.manual, command, test)
	}
}

// TestLatest reads GitHub metadata through the same HTTPS path as installs.
func TestLatest(t *testing.T) {
	for _, test := range []struct{ tag, version string }{
		{"v1.10.0", "1.10.0"},
		{"1.10.0", ""},
		{"v1.10.0-rc1", ""},
	} {
		t.Run(test.tag, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/latest", r.URL.Path)
				fmt.Fprintf(w, `{"tag_name":%q}`, test.tag)
			}))
			defer server.Close()
			src := ReleaseSource{Client: server.Client(), LatestURL: server.URL + "/latest"}
			got, err := src.Latest(t.Context())
			if test.version == "" {
				assert.ErrorIs(t, err, ErrReleaseUnavailable)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, Release{Version: test.version, URL: releasesURL + "/tag/" + test.tag}, got)
		})
	}
}

// TestInstallSerializesAndPreserves verifies publication, version races, and
// cleanup using the same executable path that a restarted process will use.
func TestInstallSerializesAndPreserves(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HIGHBAY_NPM_LAUNCHER", "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "highbay")
	require.NoError(t, os.WriteFile(bin, versionProgram("v1.0.0"), 0o755))
	symlink := filepath.Join(t.TempDir(), "highbay")
	require.NoError(t, os.Symlink(bin, symlink))

	src := fixtureSource(t, "1.2.0", "v1.2.0", false)
	next, unlock, err := Install(t.Context(), src, "1.2.0", symlink, io.Discard)
	require.NoError(t, err)
	realBin, err := filepath.EvalSymlinks(bin)
	require.NoError(t, err)
	assert.Equal(t, realBin, next)
	got, err := BinaryVersion(t.Context(), symlink)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", got)

	blocked, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, _, err = Install(blocked, src, "1.2.0", symlink, io.Discard)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	require.NoError(t, unlock())

	_, _, err = Install(t.Context(), ReleaseSource{}, "1.0.0", symlink, io.Discard)
	assert.ErrorIs(t, err, ErrWouldDowngrade)
	_, unlock, err = Install(t.Context(), ReleaseSource{}, "1.2.0", symlink, io.Discard)
	require.NoError(t, err)
	require.NoError(t, unlock())
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "the update leaves its directory clean")
}

// TestFailedInstallKeepsCurrent rejects corrupted or mislabeled releases.
func TestFailedInstallKeepsCurrent(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HIGHBAY_NPM_LAUNCHER", "")
	dir := t.TempDir()
	bin := filepath.Join(dir, "highbay")
	original := versionProgram("v1.0.0")
	require.NoError(t, os.WriteFile(bin, original, 0o755))
	for _, test := range []struct {
		name, candidate, message string
		corrupt                  bool
	}{
		{"corrupt", "v1.2.0", "SHA-256", true},
		{"mislabeled", "v1.1.0", "reports v1.1.0, expected v1.2.0", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			src := fixtureSource(t, "1.2.0", test.candidate, test.corrupt)
			_, _, err := Install(t.Context(), src, "1.2.0", bin, io.Discard)
			assert.ErrorIs(t, err, ErrReleaseUnavailable)
			assert.ErrorContains(t, err, test.message)
			data, err := os.ReadFile(bin)
			require.NoError(t, err)
			assert.Equal(t, original, data)
		})
	}
}

// TestInstallHomebrew checks the exact formula while Homebrew owns the binary.
func TestInstallHomebrew(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	cell := filepath.Join(root, "Cellar", "highbay")
	old, newExe := filepath.Join(cell, "1.0.0", "bin", "highbay"), filepath.Join(cell, "1.2.0", "bin", "highbay")
	for name, version := range map[string]string{old: "v1.0.0", newExe: "v1.2.0"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(name), 0o755))
		require.NoError(t, os.WriteFile(name, versionProgram(version), 0o755))
	}
	next := filepath.Join(binDir, "highbay")
	require.NoError(t, os.Symlink(old, next))
	t.Setenv("PATH", binDir)
	t.Setenv("HIGHBAY_NPM_LAUNCHER", "")
	t.Setenv("HIGHBAY_TEST_BREW_PREFIX", root)
	t.Setenv("HIGHBAY_TEST_BREW_STAGED", newExe)
	program := `#!/bin/sh
case "$1" in
  --cellar) printf '%s/Cellar\n' "$HIGHBAY_TEST_BREW_PREFIX" ;;
  --prefix) printf '%s\n' "$HIGHBAY_TEST_BREW_PREFIX" ;;
  update) test "$HOMEBREW_NO_AUTO_UPDATE" = 1 ;;
  info)
    test "$HOMEBREW_NO_AUTO_UPDATE" = 1 || exit 1
    printf '{"formulae":[{"versions":{"stable":"%s"}}]}\n' "$HIGHBAY_TEST_BREW_STABLE"
    ;;
  upgrade)
    test "$HOMEBREW_NO_AUTO_UPDATE" = 1 || exit 1
    test "$2" = goadesign/tap/highbay || exit 1
    /bin/ln -sf "$HIGHBAY_TEST_BREW_STAGED" "$HIGHBAY_TEST_BREW_PREFIX/bin/highbay"
    ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "brew"), []byte(program), 0o755))

	t.Setenv("HIGHBAY_TEST_BREW_STABLE", "1.3.0")
	_, _, err := Install(t.Context(), ReleaseSource{}, "1.2.0", old, io.Discard)
	assert.ErrorIs(t, err, ErrReleaseUnavailable)
	got, err := BinaryVersion(t.Context(), next)
	require.NoError(t, err)
	assert.Equal(t, "1.0.0", got)

	t.Setenv("HIGHBAY_TEST_BREW_STABLE", "1.2.0")
	installed, unlock, err := Install(t.Context(), ReleaseSource{}, "1.2.0", old, io.Discard)
	require.NoError(t, err)
	require.NoError(t, unlock())
	assert.Equal(t, next, installed)
	got, err = BinaryVersion(t.Context(), next)
	require.NoError(t, err)
	assert.Equal(t, "1.2.0", got)
}

// fixtureSource serves the same checksums and archive names as the release workflow.
func fixtureSource(t *testing.T, target, candidate string, corrupt bool) ReleaseSource {
	t.Helper()
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tarStream := tar.NewWriter(gz)
	program := versionProgram(candidate)
	require.NoError(t, tarStream.WriteHeader(&tar.Header{Name: "highbay", Mode: 0o755, Size: int64(len(program))}))
	_, err := tarStream.Write(program)
	require.NoError(t, err)
	require.NoError(t, tarStream.Close())
	require.NoError(t, gz.Close())
	assetName := fmt.Sprintf("highbay_%s_%s_%s.tar.gz", target, runtime.GOOS, runtime.GOARCH)
	base := "/download/v" + target + "/"
	sum := sha256.Sum256(archive.Bytes())
	payload := archive.Bytes()
	if corrupt {
		payload = []byte("broken archive")
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case base + "checksums.txt":
			fmt.Fprintf(w, "%x  %s\n", sum, assetName)
		case base + assetName:
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return ReleaseSource{Client: server.Client(), DownloadURL: server.URL + "/download"}
}

// versionProgram leaves stderr diagnostics out of the release version.
func versionProgram(version string) []byte {
	return []byte("#!/bin/sh\nprintf 'release probe\\n' >&2\nprintf '%s\\n' '" + version + "'\n")
}
