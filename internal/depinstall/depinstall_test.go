package depinstall

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnsureRequiresConsent proves a missing tool cannot cause writes while
// setup is non-interactive or the user declines.
func TestEnsureRequiresConsent(t *testing.T) {
	for _, tc := range []struct {
		name, answer, want string
		interactive        bool
	}{
		{"noninteractive", "", "highbay up --yes", false},
		{"declined", "n\n", "installation declined", true},
		{"closed terminal", "", "installation was not confirmed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "bin")
			i := testInstaller()
			_, err := i.ensure(t.Context(), Options{
				InstallDir: dir, Interactive: tc.interactive,
				Input: strings.NewReader(tc.answer), Output: &bytes.Buffer{},
			})
			require.ErrorContains(t, err, tc.want)
			assert.NoDirExists(t, dir)
		})
	}
}

// TestCheckSystem checks readiness using each tool's own version or capability.
func TestCheckSystem(t *testing.T) {
	for _, tc := range []struct {
		name, missing, goVersion, gitVersion, want string
		gitError, dockerError                      error
	}{
		{name: "future Go release candidate", goVersion: "go1.28rc1", gitVersion: "2.47.1.2"},
		{name: "Git backport", goVersion: goMinimum, gitVersion: "2.39.5 (Apple Git-154)"},
		{name: "Git release candidate", goVersion: goMinimum, gitVersion: "2.46.0.rc1"},
		{name: "missing Go", missing: "go", want: "install go"},
		{name: "missing Git", missing: "git", goVersion: goMinimum, want: "Git is required"},
		{name: "missing Docker", missing: "docker", goVersion: goMinimum, want: "install docker"},
		{name: "old Go", goVersion: "go1.27.2", want: "Highbay needs go1.27.3"},
		{
			name: "Git without required option", goVersion: goMinimum, gitVersion: "2.39.5",
			gitError: &exec.ExitError{Stderr: []byte("unknown option: --no-lazy-fetch")},
			want:     "Git on PATH reports \"git version 2.39.5\" and does not support --no-lazy-fetch",
		},
		{
			name: "Git process failed", goVersion: goMinimum,
			gitError: errors.New("process failed"), want: "checking Git support for --no-lazy-fetch",
		},
		{
			name: "Docker process failed", goVersion: goMinimum,
			dockerError: errors.New("process failed"), want: "check docker",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := testInstaller()
			find := i.find
			i.find = func(name string) (string, error) {
				if name == tc.missing {
					return "", os.ErrNotExist
				}
				return find(name)
			}
			i.run = func(_ context.Context, path string, args ...string) ([]byte, error) {
				switch path {
				case "/tools/go":
					assert.Equal(t, []string{"version"}, args)
					return []byte("go version " + tc.goVersion + " darwin/arm64"), nil
				case "/tools/git":
					if tc.gitError != nil && len(args) == 2 {
						assert.Equal(t, []string{"--no-lazy-fetch", "version"}, args)
						return nil, tc.gitError
					}
					if tc.gitError == nil {
						assert.Equal(t, []string{"--no-lazy-fetch", "version"}, args)
					} else {
						assert.Equal(t, []string{"--version"}, args)
					}
					return []byte("git version " + tc.gitVersion), nil
				case "/tools/docker":
					assert.Equal(t, []string{"--version"}, args)
					return []byte("Docker version 28.0.0"), tc.dockerError
				default:
					t.Fatalf("unexpected command %s", path)
					return nil, os.ErrNotExist
				}
			}
			err := i.checkSystem(t.Context())
			if tc.want == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.want)
			}
		})
	}
}

// TestCommandOutput keeps warnings out of probe values but retains error details.
func TestCommandOutput(t *testing.T) {
	out, err := commandOutput(t.Context(), "/bin/sh", "-c",
		"printf 'note from launcher\\n' >&2; printf 'hc 0.1.22\\n'")
	require.NoError(t, err)
	assert.Equal(t, "0.1.22", versionToken(out, "hc "))
	out, err = commandOutput(t.Context(), "/bin/sh", "-c",
		"printf 'signature mismatch\\n' >&2; exit 1")
	require.Error(t, err)
	assert.Equal(t, "signature mismatch", commandDiagnostics(out, err))
}

// TestEnsureInstallsVerifiedCompanions exercises the manifest and checksum
// formats over HTTPS, the Darwin signature gate, and verified publication.
func TestEnsureInstallsVerifiedCompanions(t *testing.T) {
	hc := makeArchive(t, "herdr-core", "Herdr executable")
	chorus := makeArchive(t, "chorus", "Chorus executable")
	hcWant := strings.TrimPrefix(herdrMinimum, "v")
	chWant := strings.TrimPrefix(chorusMinimum, "v")
	chFilename := "chorus_" + chWant + "_darwin_arm64.tar.gz"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/" + hcWant + "/manifest.json":
			json.NewEncoder(w).Encode(map[string]any{
				"version": hcWant,
				"targets": map[string]any{
					"macos-aarch64": map[string]string{
						"url":    "https://" + r.Host + "/herdr.tar.gz",
						"sha256": fmt.Sprintf("%x", sha256.Sum256(hc)),
					},
				},
			})
		case "/herdr.tar.gz":
			w.Write(hc)
		case "/v" + chWant + "/checksums.txt":
			fmt.Fprintf(w, "%x  %s\n", sha256.Sum256(chorus), chFilename)
		case "/v" + chWant + "/" + chFilename:
			w.Write(chorus)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	i := testInstaller()
	i.client, i.herdrURL, i.chorusURL = srv.Client(), srv.URL, srv.URL
	i.goos, i.goarch = "darwin", "arm64"
	dir := t.TempDir()
	var signer []string
	probe := i.run
	i.run = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		if path == "/usr/bin/codesign" {
			signer = args
			return nil, nil
		}
		if _, err := os.Stat(path); err == nil {
			switch filepath.Base(path) {
			case "hc":
				return []byte("hc " + hcWant), nil
			case "chorus":
				return []byte("chorus v" + chWant), nil
			}
		}
		return probe(ctx, path, args...)
	}
	var prompt bytes.Buffer
	got, err := i.ensure(t.Context(), Options{
		Interactive: true, Input: strings.NewReader("\n"), Output: &prompt, InstallDir: dir,
	})
	require.NoError(t, err)
	assert.Contains(t, prompt.String(), "[Y/n]")
	assert.Equal(t, Tools{HC: filepath.Join(dir, "hc"), Chorus: filepath.Join(dir, "chorus")}, got)
	require.Len(t, signer, 5)
	assert.Equal(t, []string{"--verify", "--strict", "-R", herdrSigner}, signer[:4])
	hcPath := got.HC
	hcContent, err := os.ReadFile(hcPath)
	require.NoError(t, err)
	assert.Equal(t, "Herdr executable", string(hcContent))
	chorusContent, err := os.ReadFile(got.Chorus)
	require.NoError(t, err)
	assert.Equal(t, "Chorus executable", string(chorusContent))

	// A subsequent setup finds the installed paths even outside PATH.
	gotAgain, err := i.ensure(t.Context(), Options{InstallDir: dir})
	require.NoError(t, err)
	assert.Equal(t, got, gotAgain)
}

// TestFailedArchivePreservesExistingExecutable reaches both verification
// refusals before a published destination can be replaced.
func TestFailedArchivePreservesExistingExecutable(t *testing.T) {
	data := makeArchive(t, "herdr-core", "Bad replacement")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write(data) }))
	defer srv.Close()
	for _, tc := range []struct {
		name, want string
		digest     [sha256.Size]byte
	}{
		{"checksum mismatch", "SHA-256", [sha256.Size]byte{}},
		{"signature refusal", "signature verification failed", sha256.Sum256(data)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := testInstaller()
			i.goos, i.client = "darwin", srv.Client()
			i.run = func(context.Context, string, ...string) ([]byte, error) {
				return nil, errors.New("unsigned executable")
			}
			dir := t.TempDir()
			dst := filepath.Join(dir, "hc")
			require.NoError(t, os.WriteFile(dst, []byte("Existing executable"), 0o755))
			_, err := i.installArchive(t.Context(), dir, herdrRequirement, ReleaseArchive{
				URL: srv.URL, Digest: tc.digest, Member: "herdr-core",
			})
			require.ErrorContains(t, err, tc.want)
			current, err := os.ReadFile(dst)
			require.NoError(t, err)
			assert.Equal(t, "Existing executable", string(current))
			entries, err := os.ReadDir(dir)
			require.NoError(t, err)
			assert.Len(t, entries, 1)
		})
	}
}

// TestBrewOwnedInstall verifies that Homebrew-owned Highbay updates formula-backed
// Chorus through brew while staging verified Herdr Core into InstallDir.
func TestBrewOwnedInstall(t *testing.T) {
	hc := makeArchive(t, "herdr-core", "Herdr executable")
	hcWant := strings.TrimPrefix(herdrMinimum, "v")
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/core/" + hcWant + "/manifest.json":
			if err := json.NewEncoder(w).Encode(map[string]any{
				"version": hcWant,
				"targets": map[string]any{
					"macos-aarch64": map[string]string{
						"url":    "https://" + r.Host + "/herdr.tar.gz",
						"sha256": fmt.Sprintf("%x", sha256.Sum256(hc)),
					},
				},
			}); err != nil {
				t.Errorf("encode manifest: %v", err)
			}
		case "/herdr.tar.gz":
			if _, err := w.Write(hc); err != nil {
				t.Errorf("write archive: %v", err)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	prefix := t.TempDir()
	installDir := t.TempDir()
	cell := filepath.Join(prefix, "Cellar", "highbay", "0.1.0", "bin")
	require.NoError(t, os.MkdirAll(cell, 0o755))
	brewHighbay := filepath.Join(cell, "highbay")
	require.NoError(t, os.WriteFile(brewHighbay, nil, 0o755))
	link := filepath.Join(t.TempDir(), "highbay")
	require.NoError(t, os.Symlink(brewHighbay, link))
	standalone := filepath.Join(prefix, "bin", "highbay")
	require.NoError(t, os.MkdirAll(filepath.Dir(standalone), 0o755))
	require.NoError(t, os.WriteFile(standalone, nil, 0o755))

	i := testInstaller()
	i.client, i.herdrURL = srv.Client(), srv.URL
	i.goos, i.goarch = "darwin", "arm64"
	find, probe := i.find, i.run
	i.find = func(name string) (string, error) {
		if name == "brew" {
			return "/brew/bin/brew", nil
		}
		return find(name)
	}
	var actions []string
	i.run = func(ctx context.Context, path string, args ...string) ([]byte, error) {
		switch {
		case path == "/usr/bin/codesign":
			return nil, nil
		case path == "/brew/bin/brew":
			switch args[0] {
			case "--cellar":
				return []byte(filepath.Join(prefix, "Cellar") + "\n"), nil
			case "--prefix":
				return []byte("/brew/opt/" + filepath.Base(args[1]) + "\n"), nil
			case "list":
				if args[2] == chorusRequirement.formula {
					return []byte("chorus 1.1.0\n"), nil
				}
				return nil, errors.New("formula missing")
			case "install", "upgrade":
				actions = append(actions, strings.Join(args, " "))
				return nil, nil
			}
		case filepath.Base(path) == "hc":
			if _, err := os.Stat(path); err == nil {
				return []byte("hc " + hcWant), nil
			}
		case path == "/brew/opt/chorus/bin/chorus":
			return []byte("chorus " + chorusMinimum), nil
		}
		return probe(ctx, path, args...)
	}
	assert.Empty(t, i.homebrew(t.Context(), standalone))
	tools, err := i.ensure(t.Context(), Options{Yes: true, InstallDir: installDir, HighbayPath: link})
	require.NoError(t, err)
	assert.Equal(t, []string{"upgrade " + chorusRequirement.formula}, actions)
	assert.Equal(t, Tools{HC: filepath.Join(installDir, "hc"), Chorus: "/brew/opt/chorus/bin/chorus"}, tools)
}

// TestReleaseValidation rejects ambiguous metadata and non-executable members.
func TestReleaseValidation(t *testing.T) {
	for _, endpoint := range []string{"http://releases.example/archive", "https://user@releases.example/archive"} {
		target, err := url.Parse(endpoint)
		require.NoError(t, err)
		assert.Error(t, requireHTTPS(target))
	}
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/archive", http.StatusFound)
	}))
	defer redirect.Close()
	_, err := ReadReleaseMetadata(t.Context(), redirect.Client(), redirect.URL)
	assert.ErrorContains(t, err, "a release URL must be HTTPS")
	entry := strings.Repeat("a", 64) + "  archive.tar.gz\n"
	_, err = ArchiveChecksum([]byte(entry+entry), "archive.tar.gz")
	assert.ErrorContains(t, err, "duplicate checksum")
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	member := tar.NewWriter(gz)
	require.NoError(t, member.WriteHeader(&tar.Header{Name: "hc", Typeflag: tar.TypeSymlink, Linkname: "target"}))
	require.NoError(t, member.Close())
	require.NoError(t, gz.Close())
	var executable bytes.Buffer
	assert.ErrorContains(t, extractBinary(&raw, "hc", &executable), "invalid archive executable")
	assert.Empty(t, executable.Bytes())
}

// testInstaller provides the system tools and leaves companions uninstalled.
func testInstaller() installer {
	return installer{
		find: func(name string) (string, error) {
			switch name {
			case "go", "git", "docker":
				return "/tools/" + name, nil
			default:
				return "", os.ErrNotExist
			}
		},
		run: func(_ context.Context, path string, _ ...string) ([]byte, error) {
			switch path {
			case "/tools/go":
				return []byte("go version go1.27.3 darwin/arm64"), nil
			case "/tools/git":
				return []byte("git version 2.45.0"), nil
			case "/tools/docker":
				return []byte("Docker version 28.0.0"), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	}
}

// makeArchive builds the same single binary member used by published assets.
func makeArchive(t *testing.T, member, content string) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	archiveWriter := tar.NewWriter(gz)
	require.NoError(t, archiveWriter.WriteHeader(&tar.Header{Name: member, Mode: 0o755, Size: int64(len(content))}))
	_, err := io.WriteString(archiveWriter, content)
	require.NoError(t, err)
	require.NoError(t, archiveWriter.Close())
	require.NoError(t, gz.Close())
	return out.Bytes()
}
