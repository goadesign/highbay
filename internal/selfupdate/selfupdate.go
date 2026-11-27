// Package selfupdate checks published Highbay releases and installs an exact
// approved version. Callers own confirmation, global setup, and server restart.
package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/goadesign/highbay/internal/depinstall"
	"golang.org/x/sys/unix"
)

type (
	// ReleaseSource finds and verifies Highbay release assets. Its zero value
	// uses the official GitHub endpoints and a bounded HTTPS client.
	ReleaseSource struct {
		Client      *http.Client
		LatestURL   string
		DownloadURL string
	}

	// Release identifies a stable release and its public release notes.
	Release struct {
		Version string
		URL     string
	}
)

const (
	releasesURL    = "https://github.com/goadesign/highbay/releases"
	latestURL      = "https://api.github.com/repos/goadesign/highbay/releases/latest"
	formula        = "goadesign/tap/highbay"
	checkTimeout   = 15 * time.Second
	installTimeout = 10 * time.Minute
	lockPoll       = 100 * time.Millisecond
	commandWait    = 2 * time.Second
)

var (
	// ErrReleaseUnavailable means the selected publisher asset is unavailable
	// or does not contain the approved version.
	ErrReleaseUnavailable = errors.New("release unavailable")
	// ErrWouldDowngrade means a newer binary was already installed on disk.
	ErrWouldDowngrade = errors.New("would downgrade")
)

// DefaultReleaseSource uses the published releases from github.com/goadesign/highbay.
func DefaultReleaseSource() ReleaseSource {
	return (ReleaseSource{}).withDefaults()
}

// ParseVersion accepts exactly three canonical decimal components.
func ParseVersion(value string) ([3]int, error) {
	var version [3]int
	parts := strings.Split(value, ".")
	if len(parts) != len(version) {
		return version, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", value)
	}
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || strconv.Itoa(n) != part {
			return version, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", value)
		}
		version[i] = n
	}
	return version, nil
}

// NewerVersion compares two stable release versions without a leading "v".
func NewerVersion(a, b string) (bool, error) {
	left, err := ParseVersion(a)
	if err != nil {
		return false, err
	}
	right, err := ParseVersion(b)
	if err != nil {
		return false, err
	}
	return slices.Compare(left[:], right[:]) > 0, nil
}

// VersionBefore reports whether both stable versions are known and a is older.
func VersionBefore(a, b string) bool {
	newer, err := NewerVersion(b, a)
	return err == nil && newer
}

// NormalizeBuildVersion returns a stable version without a prefix. Local
// builds, pre-releases, and pseudo-versions are not eligible for auto-update.
func NormalizeBuildVersion(raw string) (string, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(raw), "highbay ")
	value = strings.TrimPrefix(value, "v")
	if _, err := ParseVersion(value); err != nil {
		return "", false
	}
	return value, true
}

// CanAutoUpdate checks whether this executable is managed by Highbay.
// npm installs keep the launcher, binary, and dependent packages in sync.
func CanAutoUpdate(exe, currentVersion string, getenv func(string) string) (bool, string) {
	if getenv != nil {
		launcher := getenv("HIGHBAY_NPM_LAUNCHER")
		if launcher != "" {
			for _, component := range strings.Split(filepath.Clean(launcher), string(filepath.Separator)) {
				if component == "_npx" {
					return false, "npx @goadesign/highbay@latest"
				}
			}
			return false, "npm install -g @goadesign/highbay@latest"
		}
	}
	if _, stable := NormalizeBuildVersion(currentVersion); !stable || !filepath.IsAbs(exe) {
		return false, "go install github.com/goadesign/highbay/cmd/highbay@latest"
	}
	return true, ""
}

// BinaryVersion runs the supported version command and returns a stable version
// from stdout. An unreadable or development binary is reported as an error.
func BinaryVersion(ctx context.Context, exe string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	out, err := updateCommand(ctx, exe, "version").Output()
	if err != nil {
		return "", fmt.Errorf("read version of %s: %w", exe, err)
	}
	value, stable := NormalizeBuildVersion(string(out))
	if !stable {
		return "", fmt.Errorf("%s reports non-release version %q", exe, strings.TrimSpace(string(out)))
	}
	return value, nil
}

// Install installs the exact stable target and returns the path to use next.
// On success the caller holds the directory lock and must call unlock after
// setup, or replace its process, which closes the lock descriptor. Every
// failure releases the lock.
func Install(
	ctx context.Context,
	src ReleaseSource,
	target, exe string,
	out io.Writer,
) (next string, unlock func() error, err error) {
	if _, err := ParseVersion(target); err != nil {
		return "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	realExe, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", nil, fmt.Errorf("resolve Highbay executable: %w", err)
	}
	if !filepath.IsAbs(realExe) {
		return "", nil, errors.New("updating Highbay requires an absolute executable path")
	}
	brew := depinstall.HomebrewFor(ctx, realExe)
	next = realExe
	if brew != "" {
		prefix, err := updateCommand(ctx, brew, "--prefix").Output()
		if err != nil {
			return "", nil, fmt.Errorf("locate Homebrew's executable directory: %w", err)
		}
		dir := strings.TrimSpace(string(prefix))
		if !filepath.IsAbs(dir) {
			return "", nil, errors.New("brew --prefix did not report an absolute directory")
		}
		next = filepath.Join(dir, "bin", "highbay")
	}

	releaseLock, err := lockDir(ctx, filepath.Dir(next))
	if err != nil {
		return "", nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, releaseLock())
		}
	}()
	current, err := BinaryVersion(ctx, next)
	if err != nil {
		return "", nil, err
	}
	if ok, manual := CanAutoUpdate(next, current, os.Getenv); !ok {
		return "", nil, fmt.Errorf("update Highbay with %s", manual)
	}
	if VersionBefore(target, current) {
		return "", nil, fmt.Errorf("%w: %s already reports v%s, newer than v%s", ErrWouldDowngrade, next, current, target)
	}
	if current == target {
		return next, releaseLock, nil
	}
	if brew != "" {
		if err := installHomebrew(ctx, brew, target, next, out); err != nil {
			return "", nil, err
		}
	} else if err := installDirect(ctx, src.withDefaults(), target, next); err != nil {
		return "", nil, err
	}
	return next, releaseLock, nil
}

// Latest reads the stable release tag without installing to disk.
func (src ReleaseSource) Latest(ctx context.Context) (Release, error) {
	src = src.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	body, err := depinstall.ReadReleaseMetadata(ctx, src.Client, src.LatestURL)
	if err != nil {
		return Release{}, fmt.Errorf("%w: check the latest Highbay release: %w", ErrReleaseUnavailable, err)
	}
	var item struct {
		Tag string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &item); err != nil {
		return Release{}, fmt.Errorf("%w: read the latest Highbay release: %w", ErrReleaseUnavailable, err)
	}
	version, tagged := strings.CutPrefix(item.Tag, "v")
	if _, err := ParseVersion(version); !tagged || err != nil {
		return Release{}, fmt.Errorf("%w: unexpected Highbay release tag %q", ErrReleaseUnavailable, item.Tag)
	}
	return Release{Version: version, URL: releasesURL + "/tag/v" + version}, nil
}

// withDefaults lets tests substitute a release source without changing callers.
func (src ReleaseSource) withDefaults() ReleaseSource {
	if src.Client == nil {
		src.Client = depinstall.NewReleaseClient()
	}
	if src.LatestURL == "" {
		src.LatestURL = latestURL
	}
	if src.DownloadURL == "" {
		src.DownloadURL = releasesURL + "/download"
	}
	return src
}

// installDirect verifies the archive and candidate version before replacing next.
func installDirect(ctx context.Context, src ReleaseSource, target, next string) error {
	name := fmt.Sprintf("highbay_%s_%s_%s.tar.gz", target, runtime.GOOS, runtime.GOARCH)
	base := strings.TrimRight(src.DownloadURL, "/") + "/v" + target
	checksums, err := depinstall.ReadReleaseMetadata(ctx, src.Client, base+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("%w: %w", ErrReleaseUnavailable, err)
	}
	digest, err := depinstall.ArchiveChecksum(checksums, name)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrReleaseUnavailable, err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(next), ".highbay-update-")
	if err != nil {
		return fmt.Errorf("stage new Highbay executable: %w", err)
	}
	defer os.RemoveAll(stage)
	candidate := filepath.Join(stage, "highbay")
	archive := depinstall.ReleaseArchive{URL: base + "/" + name, Digest: digest, Member: "highbay"}
	if err := depinstall.StageReleaseExecutable(ctx, src.Client, archive, candidate); err != nil {
		return fmt.Errorf("%w: verify %s: %w", ErrReleaseUnavailable, name, err)
	}
	installed, err := BinaryVersion(ctx, candidate)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrReleaseUnavailable, name, err)
	}
	if installed != target {
		return fmt.Errorf("%w: %s reports v%s, expected v%s", ErrReleaseUnavailable, name, installed, target)
	}
	if err := os.Rename(candidate, next); err != nil {
		return fmt.Errorf("replace %s: %w", next, err)
	}
	return nil
}

// installHomebrew updates metadata, checks the approved version is offered,
// then asks Homebrew to install and identifies the version it actually installed.
func installHomebrew(ctx context.Context, brew, target, next string, out io.Writer) error {
	if err := runBrew(ctx, out, brew, "update"); err != nil {
		return err
	}
	cmd := brewCommand(ctx, brew, "info", "--json=v2", formula)
	data, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("%w: brew info: %w", ErrReleaseUnavailable, err)
	}
	var info struct {
		Formulae []struct {
			Versions struct {
				Stable string `json:"stable"`
			} `json:"versions"`
		} `json:"formulae"`
	}
	if err := json.Unmarshal(data, &info); err != nil {
		return fmt.Errorf("%w: read Homebrew formula: %w", ErrReleaseUnavailable, err)
	}
	if len(info.Formulae) != 1 || info.Formulae[0].Versions.Stable != target {
		return fmt.Errorf("%w: Homebrew does not offer the approved Highbay v%s; check for updates again", ErrReleaseUnavailable, target)
	}
	if err := runBrew(ctx, out, brew, "upgrade", formula); err != nil {
		return err
	}
	got, err := BinaryVersion(ctx, next)
	if err != nil {
		return err
	}
	if got != target {
		return fmt.Errorf("brew upgrade installed Highbay v%s, expected v%s", got, target)
	}
	return nil
}

// runBrew forwards package-manager progress to the caller's output.
func runBrew(ctx context.Context, out io.Writer, brew string, args ...string) error {
	cmd := brewCommand(ctx, brew, args...)
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("brew %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// brewCommand permits tap refresh only when the requested command is update.
func brewCommand(ctx context.Context, brew string, args ...string) *exec.Cmd {
	cmd := updateCommand(ctx, brew, args...)
	cmd.Env = append(os.Environ(), "HOMEBREW_NO_AUTO_UPDATE=1")
	return cmd
}

// updateCommand bounds pipes still open after a process exits or is canceled.
func updateCommand(ctx context.Context, exe string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.WaitDelay = commandWait
	return cmd
}

// lockDir serializes upgrades across atomic replacement of the executable.
func lockDir(ctx context.Context, dir string) (func() error, error) {
	file, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	fd := int(file.Fd())
	for {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(fmt.Errorf("wait for another Highbay update in %s: %w", dir, err), file.Close())
		}
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() error {
				return errors.Join(unix.Flock(fd, unix.LOCK_UN), file.Close())
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			return nil, errors.Join(fmt.Errorf("lock update directory %s: %w", dir, err), file.Close())
		}
		select {
		case <-ctx.Done():
		case <-time.After(lockPoll):
		}
	}
}
