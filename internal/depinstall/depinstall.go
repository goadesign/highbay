// Package depinstall checks the tools Highbay needs and installs compatible
// Herdr Core and Chorus binaries before the CLI starts either service.
package depinstall

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/version"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

type (
	// Options gives the CLI's install location and permission to write there.
	Options struct {
		// Input and Output are the terminal streams used for a confirmation.
		Input  io.Reader
		Output io.Writer
		// Interactive means Input can answer a prompt.
		Interactive bool
		// Yes authorizes installing missing or incompatible companions.
		Yes bool
		// HighbayPath names the executable running setup.
		HighbayPath string
		// InstallDir is the CLI's user-writable executable directory.
		InstallDir string
	}

	// Tools names the verified executables that the caller can start.
	Tools struct {
		// HC is the executable used for the Herdr Core command.
		HC string
		// Chorus is the executable used for the Chorus command.
		Chorus string
	}

	// installer owns the file and process operations used by Ensure.
	installer struct {
		client    *http.Client
		find      func(string) (string, error)
		run       func(context.Context, string, ...string) ([]byte, error)
		goos      string
		goarch    string
		herdrURL  string
		chorusURL string
	}

	// requirement describes how a versioned companion identifies itself.
	requirement struct {
		name    string
		minimum string
		formula string
	}

	// ReleaseArchive names the exact bytes and executable member to stage.
	ReleaseArchive struct {
		URL    string
		Digest [sha256.Size]byte
		Member string
	}
)

const (
	// The minima describe the published versions whose CLI and API Highbay uses.
	herdrMinimum  = "v0.1.22"
	chorusMinimum = "v1.2.0"
	goMinimum     = "go1.27.3"

	// The published manifests and archives are small enough to check locally.
	maxMetadata = 1 << 20
	maxArchive  = 256 << 20
	maxBinary   = 256 << 20

	// commandTimeout bounds probes of locally installed programs.
	commandTimeout = 15 * time.Second
	// downloadTimeout covers one HTTPS archive download.
	downloadTimeout = 2 * time.Minute

	// The standalone executable carries the release publisher's Developer ID.
	herdrSigner = "=identifier \"dev.herdr.core\" and anchor apple generic and certificate leaf[subject.OU] = \"2W9652Q7QR\""
)

var (
	herdrRequirement  = requirement{name: "hc", minimum: herdrMinimum}
	chorusRequirement = requirement{name: "chorus", minimum: chorusMinimum, formula: "goadesign/tap/chorus"}
)

// Ensure checks Go, Git, Docker, Herdr Core, and Chorus. It installs a missing
// or older companion only with authorization from opts, and returns the actual
// binary paths even when the installation directory is outside PATH.
func Ensure(ctx context.Context, opts Options) (Tools, error) {
	i := installer{
		client: NewReleaseClient(),
		find:   exec.LookPath,
		run:    commandOutput,
		goos:   runtime.GOOS, goarch: runtime.GOARCH,
		herdrURL:  "https://releases.herdr.dev",
		chorusURL: "https://github.com/goadesign/chorus/releases/download",
	}
	return i.ensure(ctx, opts)
}

// CheckProjectGit verifies the Git capability a project reader needs. Callers
// can check it during setup or before starting project work, without a repository.
func CheckProjectGit(ctx context.Context) error {
	return (installer{find: exec.LookPath, run: commandOutput}).checkProjectGit(ctx)
}

// HomebrewFor returns the brew executable when Homebrew owns the resolved
// Highbay binary. Both companion installation and Highbay updates use it.
func HomebrewFor(ctx context.Context, highbay string) string {
	return (installer{find: exec.LookPath, run: commandOutput}).homebrew(ctx, highbay)
}

// NewReleaseClient supplies the HTTP timeout shared by release asset reads.
func NewReleaseClient() *http.Client {
	return &http.Client{Timeout: downloadTimeout}
}

// ReadReleaseMetadata reads a bounded HTTPS manifest or checksum list.
func ReadReleaseMetadata(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	body, err := openRelease(ctx, client, endpoint, maxMetadata)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, maxMetadata+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMetadata {
		return nil, errors.New("release manifest is too large")
	}
	return data, nil
}

// ArchiveChecksum finds exactly one SHA-256 entry for the complete filename.
func ArchiveChecksum(data []byte, name string) ([sha256.Size]byte, error) {
	var want [sha256.Size]byte
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		if found {
			return want, fmt.Errorf("duplicate checksum for %s", name)
		}
		var err error
		want, err = decodeDigest(fields[0])
		if err != nil {
			return want, fmt.Errorf("checksum for %s: %w", name, err)
		}
		found = true
	}
	if !found {
		return want, fmt.Errorf("no checksum for %s", name)
	}
	return want, nil
}

// StageReleaseExecutable downloads and verifies the archive before writing
// its executable to staged. The caller owns the private staging directory,
// removes it on failure, and publishes the file only after checking it.
func StageReleaseExecutable(
	ctx context.Context,
	client *http.Client,
	bundle ReleaseArchive,
	staged string,
) error {
	archivePath := filepath.Join(filepath.Dir(staged), "archive.tar.gz")
	if err := downloadRelease(ctx, client, bundle, archivePath); err != nil {
		return err
	}
	input, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	writeErr := extractBinary(input, bundle.Member, output)
	if writeErr == nil {
		writeErr = output.Sync()
	}
	return errors.Join(writeErr, output.Close())
}

// commandOutput keeps process diagnostics out of values parsed from stdout.
// On failure, Output includes the stderr excerpt in *exec.ExitError.
func commandOutput(ctx context.Context, executable string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, executable, args...).Output()
}

// commandDiagnostics returns the details of a failed install or signer check.
func commandDiagnostics(out []byte, err error) string {
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(exit.Stderr) > 0 {
		return strings.TrimSpace(string(out) + "\n" + string(exit.Stderr))
	}
	return strings.TrimSpace(string(out))
}

// requireHTTPS rejects non-release URL schemes and credentials before a read.
func requireHTTPS(target *url.URL) error {
	if target.Scheme != "https" || target.Hostname() == "" || target.User != nil || target.Fragment != "" {
		return errors.New("a release URL must be HTTPS and contain a host")
	}
	return nil
}

// versionToken keeps the token following the executable's own version label.
func versionToken(out []byte, label string) string {
	value, ok := strings.CutPrefix(strings.TrimSpace(string(out)), label)
	if !ok {
		return ""
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// decodeDigest accepts exactly one hexadecimal SHA-256 value.
func decodeDigest(value string) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if len(value) != hex.EncodedLen(len(digest)) {
		return digest, errors.New("invalid SHA-256 digest")
	}
	if _, err := hex.Decode(digest[:], []byte(value)); err != nil {
		return digest, fmt.Errorf("invalid SHA-256 digest: %w", err)
	}
	return digest, nil
}

// extractBinary copies only the expected regular file from a verified archive.
// Other members never become filesystem paths.
func extractBinary(in io.Reader, member string, out io.Writer) error {
	gz, err := gzip.NewReader(in)
	if err != nil {
		return err
	}
	defer gz.Close()
	reader := tar.NewReader(gz)
	for {
		h, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("archive has no %s executable", member)
		}
		if err != nil {
			return err
		}
		if strings.TrimPrefix(h.Name, "./") != member {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size <= 0 || h.Size > maxBinary {
			return fmt.Errorf("invalid archive executable %s", member)
		}
		_, err = io.Copy(out, reader)
		return err
	}
}

// authorizeInstall keeps dependency writes behind explicit CLI consent.
func authorizeInstall(opts Options, missing []string) error {
	needs := strings.Join(missing, ", ")
	if !opts.Yes && !opts.Interactive {
		return fmt.Errorf("Highbay needs %s. Run highbay up --yes", needs)
	}
	if opts.Yes {
		return nil
	}
	if _, err := fmt.Fprintf(opts.Output, "Install %s? [Y/n] ", needs); err != nil {
		return err
	}
	response, err := bufio.NewReader(opts.Input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(response) == "" && err == io.EOF {
		return errors.New("installation was not confirmed")
	}
	switch strings.ToLower(strings.TrimSpace(response)) {
	case "", "y", "yes":
		return nil
	default:
		return errors.New("installation declined. Run highbay up --yes when ready")
	}
}

// ensure checks tools before offering to install only the missing companions.
func (i installer) ensure(ctx context.Context, opts Options) (Tools, error) {
	if err := i.checkSystem(ctx); err != nil {
		return Tools{}, err
	}
	hc := i.readyCompanion(ctx, herdrRequirement, opts.InstallDir)
	chorus := i.readyCompanion(ctx, chorusRequirement, opts.InstallDir)
	var missing []string
	if hc == "" {
		missing = append(missing, "hc "+herdrMinimum+" or later")
	}
	if chorus == "" {
		missing = append(missing, "chorus "+chorusMinimum+" or later")
	}
	if len(missing) == 0 {
		return Tools{HC: hc, Chorus: chorus}, nil
	}
	if err := authorizeInstall(opts, missing); err != nil {
		return Tools{}, err
	}

	brew := i.homebrew(ctx, opts.HighbayPath)
	if hc == "" {
		var err error
		hc, err = i.install(ctx, opts.InstallDir, brew, herdrRequirement)
		if err != nil {
			return Tools{}, fmt.Errorf("install Herdr Core: %w", err)
		}
	}
	if chorus == "" {
		var err error
		chorus, err = i.install(ctx, opts.InstallDir, brew, chorusRequirement)
		if err != nil {
			return Tools{}, fmt.Errorf("install Chorus: %w", err)
		}
	}
	return Tools{HC: hc, Chorus: chorus}, nil
}

// checkSystem reports the system tool that prevents setup.
func (i installer) checkSystem(ctx context.Context) error {
	out, err := i.probeTool(ctx, "go", "version")
	if err != nil {
		return err
	}
	got := versionToken(out, "go version ")
	if !version.IsValid(got) || version.Compare(got, goMinimum) < 0 {
		return fmt.Errorf("Highbay needs %s or later; found %s", goMinimum, strings.TrimSpace(string(out)))
	}
	if err := i.checkProjectGit(ctx); err != nil {
		return err
	}
	_, err = i.probeTool(ctx, "docker", "--version")
	return err
}

// probeTool checks the executable on PATH and names the tool when it fails.
func (i installer) probeTool(ctx context.Context, name string, args ...string) ([]byte, error) {
	program, err := i.find(name)
	if err != nil {
		return nil, fmt.Errorf("install %s and rerun highbay up: %w", name, err)
	}
	out, err := i.probe(ctx, program, args...)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w\n%s", name, err, commandDiagnostics(out, err))
	}
	return out, nil
}

// checkProjectGit accepts backports and distinguishes an unsupported option
// from an execution failure. The fallback version is diagnostic only.
func (i installer) checkProjectGit(ctx context.Context) error {
	program, err := i.find("git")
	if err != nil {
		return errors.New("Git is required to start Highbay. Install Git 2.45 or newer and run highbay again.")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := i.run(ctx, program, "--no-lazy-fetch", "version"); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) || !strings.Contains(string(exit.Stderr), "unknown option: --no-lazy-fetch") {
			return fmt.Errorf("checking Git support for --no-lazy-fetch: %w", err)
		}
		out, err := i.run(ctx, program, "--version")
		if err != nil {
			return fmt.Errorf("Git does not support --no-lazy-fetch and its version could not be read. Install Git 2.45 or newer and run highbay again: %w", err)
		}
		return fmt.Errorf("Git on PATH reports %q and does not support --no-lazy-fetch. Install Git 2.45 or newer and run highbay again.", strings.TrimSpace(string(out)))
	}
	return nil
}

// readyCompanion accepts a compatible PATH executable or a prior local install.
func (i installer) readyCompanion(ctx context.Context, dep requirement, dir string) string {
	path, err := i.find(dep.name)
	if err == nil && i.compatible(ctx, dep, path) {
		return path
	}
	if dir != "" {
		local := filepath.Join(dir, dep.name)
		if local != path && i.compatible(ctx, dep, local) {
			return local
		}
	}
	return ""
}

// compatible checks the version through the executable's documented command.
func (i installer) compatible(ctx context.Context, dep requirement, path string) bool {
	flag := "--version"
	if dep.name == "chorus" {
		flag = "version"
	}
	// npm launchers may download their own verified executable on first use.
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	out, err := i.run(ctx, path, flag)
	got := "v" + strings.TrimPrefix(versionToken(out, dep.name+" "), "v")
	return err == nil && semver.Compare(got, dep.minimum) >= 0
}

// probe bounds commands that read local state and never need standard input.
func (i installer) probe(ctx context.Context, executable string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	return i.run(ctx, executable, args...)
}

// homebrew returns the package manager only for a Highbay install it owns.
func (i installer) homebrew(ctx context.Context, highbay string) string {
	if highbay == "" {
		return ""
	}
	brew, err := i.find("brew")
	if err != nil {
		return ""
	}
	out, err := i.probe(ctx, brew, "--cellar")
	if err != nil {
		return ""
	}
	cellar := strings.TrimSpace(string(out))
	if !filepath.IsAbs(cellar) {
		return ""
	}
	cellar, err = filepath.EvalSymlinks(cellar)
	if err != nil {
		return ""
	}
	highbay, err = filepath.EvalSymlinks(highbay)
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(filepath.Join(cellar, "highbay"), highbay)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	return brew
}

// install delegates formula-backed updates to Homebrew or stages a verified archive.
func (i installer) install(ctx context.Context, dir, brew string, dep requirement) (string, error) {
	if brew != "" && dep.formula != "" {
		return i.installBrew(ctx, brew, dep)
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("dependency installation requires an absolute install directory")
	}
	bundle, err := i.releaseArchive(ctx, dep)
	if err != nil {
		return "", err
	}
	return i.installArchive(ctx, dir, dep, bundle)
}

// installBrew updates a formula Homebrew already owns or installs a missing one.
func (i installer) installBrew(ctx context.Context, brew string, dep requirement) (string, error) {
	action := "install"
	if out, err := i.probe(ctx, brew, "list", "--versions", dep.formula); err == nil && len(strings.Fields(string(out))) > 0 {
		action = "upgrade"
	}
	installCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if out, err := i.run(installCtx, brew, action, dep.formula); err != nil {
		return "", fmt.Errorf("brew %s %s: %w\n%s", action, dep.formula, err, commandDiagnostics(out, err))
	}
	out, err := i.probe(ctx, brew, "--prefix", dep.formula)
	if err != nil {
		return "", fmt.Errorf("locate installed formula: %w", err)
	}
	path := filepath.Join(strings.TrimSpace(string(out)), "bin", dep.name)
	if !i.compatible(ctx, dep, path) {
		return "", fmt.Errorf("Homebrew did not install %s %s or later", dep.name, dep.minimum)
	}
	return path, nil
}

// releaseArchive reads each publisher's owning distribution format.
func (i installer) releaseArchive(ctx context.Context, dep requirement) (ReleaseArchive, error) {
	if (i.goos != "darwin" && i.goos != "linux") || (i.goarch != "amd64" && i.goarch != "arm64") {
		return ReleaseArchive{}, fmt.Errorf("no %s release for %s/%s", dep.name, i.goos, i.goarch)
	}
	if dep.name == "hc" {
		return i.herdrArchive(ctx)
	}
	version := strings.TrimPrefix(dep.minimum, "v")
	base := i.chorusURL + "/v" + version
	name := fmt.Sprintf("chorus_%s_%s_%s.tar.gz", version, i.goos, i.goarch)
	data, err := ReadReleaseMetadata(ctx, i.client, base+"/checksums.txt")
	if err != nil {
		return ReleaseArchive{}, err
	}
	digest, err := ArchiveChecksum(data, name)
	return ReleaseArchive{URL: base + "/" + name, Digest: digest, Member: "chorus"}, err
}

// herdrArchive picks the validated target from Herdr Core's versioned manifest.
func (i installer) herdrArchive(ctx context.Context) (ReleaseArchive, error) {
	want := strings.TrimPrefix(herdrMinimum, "v")
	data, err := ReadReleaseMetadata(ctx, i.client, i.herdrURL+"/core/"+want+"/manifest.json")
	if err != nil {
		return ReleaseArchive{}, err
	}
	var manifest struct {
		Version string
		Targets map[string]struct{ URL, SHA256 string }
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ReleaseArchive{}, fmt.Errorf("read Herdr Core manifest: %w", err)
	}
	if manifest.Version != want {
		return ReleaseArchive{}, fmt.Errorf("Herdr Core manifest contains %q, expected %q", manifest.Version, want)
	}
	osName, arch := i.goos, i.goarch
	if osName == "darwin" {
		osName = "macos"
	}
	if arch == "amd64" {
		arch = "x86_64"
	} else {
		arch = "aarch64"
	}
	entry, ok := manifest.Targets[osName+"-"+arch]
	if !ok {
		return ReleaseArchive{}, fmt.Errorf("Herdr Core manifest has no %s-%s target", osName, arch)
	}
	digest, err := decodeDigest(entry.SHA256)
	return ReleaseArchive{URL: entry.URL, Digest: digest, Member: "herdr-core"}, err
}

// openRelease accepts only HTTPS content up to the given response size.
func openRelease(ctx context.Context, client *http.Client, endpoint string, limit int64) (io.ReadCloser, error) {
	target, err := url.Parse(endpoint)
	if err != nil {
		return nil, err
	}
	if err := requireHTTPS(target); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	guarded := *client
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("release download redirected too many times")
		}
		if err := requireHTTPS(req.URL); err != nil {
			return err
		}
		if client.CheckRedirect != nil {
			return client.CheckRedirect(req, via)
		}
		return nil
	}
	res, err := guarded.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK || res.ContentLength > limit {
		if closeErr := res.Body.Close(); closeErr != nil {
			return nil, closeErr
		}
		return nil, fmt.Errorf("release download %s returned HTTP %d or exceeded its size limit", target.Redacted(), res.StatusCode)
	}
	return res.Body, nil
}

// installArchive verifies the archive, signature, and version before replacing
// an executable. A failed download or extraction leaves the installed file intact.
func (i installer) installArchive(ctx context.Context, dir string, dep requirement, bundle ReleaseArchive) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	stage, err := os.MkdirTemp(dir, ".highbay-install-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)
	staged := filepath.Join(stage, dep.name)
	if err := StageReleaseExecutable(ctx, i.client, bundle, staged); err != nil {
		return "", err
	}
	if dep.name == "hc" && i.goos == "darwin" {
		if out, err := i.probe(ctx, "/usr/bin/codesign", "--verify", "--strict", "-R", herdrSigner, staged); err != nil {
			return "", fmt.Errorf("Herdr Core signature verification failed: %w\n%s", err, commandDiagnostics(out, err))
		}
	}
	if !i.compatible(ctx, dep, staged) {
		return "", fmt.Errorf("verified archive does not contain %s %s or later", dep.name, dep.minimum)
	}
	dst := filepath.Join(dir, dep.name)
	if err := os.Rename(staged, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// downloadRelease hashes the stored bytes before an archive can be opened.
func downloadRelease(ctx context.Context, client *http.Client, bundle ReleaseArchive, filename string) error {
	body, err := openRelease(ctx, client, bundle.URL, maxArchive)
	if err != nil {
		return err
	}
	defer body.Close()
	out, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(h, out), io.LimitReader(body, maxArchive+1))
	if err := errors.Join(copyErr, out.Close()); err != nil {
		return err
	}
	if size > maxArchive {
		return errors.New("release archive is too large")
	}
	if [sha256.Size]byte(h.Sum(nil)) != bundle.Digest {
		return errors.New("release archive failed SHA-256 verification")
	}
	return nil
}
