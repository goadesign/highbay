// This file checks Highbay's dependencies, registers its coding-agent entry
// points, and starts the shared services used by project servers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/goadesign/highbay/internal/depinstall"
)

const (
	// dependencySetupTimeout bounds system checks and both companion installations.
	dependencySetupTimeout = 10 * time.Minute
	// companionStartTimeout bounds one service command through readiness.
	companionStartTimeout = time.Minute
)

// runUp installs missing companions with the user's consent before registering
// Highbay and starting the services. The dependency-only path is also used
// when updating an installation whose services are already running.
func runUp(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("up", flag.ContinueOnError)
	var yes bool
	fs.BoolVar(&yes, "yes", false, "Install missing companion tools without asking")
	fs.BoolVar(&yes, "y", false, "Shorthand for -yes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	return up(yes, stdout, depinstall.Ensure, processSetupEnvironment)
}

// up owns the order of installation, registration, and startup. ensure returns
// the verified companion paths, and setup reads the installed MCP executable
// and the user's configuration paths only if registration is needed.
func up(
	yes bool,
	out io.Writer,
	ensure func(context.Context, depinstall.Options) (depinstall.Tools, error),
	setup func() (setupEnvironment, error),
) error {
	noDeps := os.Getenv("HIGHBAY_NO_DEPS") == "1"
	noStart := os.Getenv("HIGHBAY_NO_START") == "1"
	if noDeps && noStart {
		return nil
	}
	installDir, err := upInstallDir()
	if err != nil {
		return err
	}

	var tools depinstall.Tools
	if !noDeps {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("locate Highbay executable: %w", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), dependencySetupTimeout)
		tools, err = ensureUpDependencies(ctx, depinstall.Options{
			Input:       os.Stdin,
			Output:      out,
			Interactive: upInteractive(os.Stdin),
			Yes:         yes,
			HighbayPath: exe,
			InstallDir:  installDir,
		}, ensure)
		cancel()
		if err != nil {
			return err
		}
	}
	if noStart {
		return nil
	}

	if noDeps {
		tools.HC, err = upCompanion("hc", installDir)
		if err != nil {
			return err
		}
		tools.Chorus, err = upCompanion("chorus", installDir)
		if err != nil {
			return err
		}
	}
	if os.Getenv("HIGHBAY_NO_SETUP") != "1" {
		env, err := setup()
		if err != nil {
			return err
		}
		if err := setupGlobal(env, out); err != nil {
			return fmt.Errorf("%w (fix that file and run highbay up again, or set HIGHBAY_NO_SETUP=1 to skip setup)", err)
		}
	}
	if err := startUpCompanion(tools.HC, os.Environ(), out, "server", "start"); err != nil {
		return fmt.Errorf("start Herdr Core: %w", err)
	}
	// Chorus can use the verified hc even when it lives outside the shell's PATH.
	chorusPath := append([]string{filepath.Dir(tools.HC)}, filepath.SplitList(os.Getenv("PATH"))...)
	chorusEnv := append(withoutProviderFamilies(os.Environ()),
		"CHORUS_NO_SETUP=1", "PATH="+strings.Join(chorusPath, string(os.PathListSeparator)),
	)
	if err := startUpCompanion(tools.Chorus, chorusEnv, out, "up"); err != nil {
		return fmt.Errorf("start Chorus: %w", err)
	}
	_, err = fmt.Fprintln(out, "Highbay is ready. Run highbay in your project folder.")
	return err
}

// upInstallDir resolves the directory used by the standalone installer.
func upInstallDir() (string, error) {
	dir := os.Getenv("HIGHBAY_INSTALL_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dir = filepath.Join(home, ".local", "bin")
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("HIGHBAY_INSTALL_DIR must be an absolute path")
	}
	return dir, nil
}

// upInteractive permits a prompt on a terminal. Non-interactive callers
// commonly close input with the null device, which is also a character device.
func upInteractive(input *os.File) bool {
	info, err := input.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	null, err := os.Stat(os.DevNull)
	return err == nil && !os.SameFile(info, null)
}

// ensureUpDependencies filters temporary npm shims out of discovery in this
// CLI process, including the .bin directory npx added for its cached install.
// Global npm shims remain available for a persistent launcher.
func ensureUpDependencies(
	ctx context.Context,
	opts depinstall.Options,
	ensure func(context.Context, depinstall.Options) (depinstall.Tools, error),
) (tools depinstall.Tools, err error) {
	launcher := strings.ReplaceAll(os.Getenv("HIGHBAY_NPM_LAUNCHER"), "\\", "/")
	companionDir := os.Getenv("HIGHBAY_COMPANION_BIN_DIR")
	cacheRoot, cacheInstall, transient := strings.Cut(launcher, "/_npx/")
	if !transient && (companionDir == "" || launcher != "") {
		return ensure(ctx, opts)
	}
	var npmBin string
	if key, _, ok := strings.Cut(cacheInstall, "/"); transient && ok && key != "" {
		npmBin = cacheRoot + "/_npx/" + key + "/node_modules/.bin"
	}
	path, hadPath := os.LookupEnv("PATH")
	if !hadPath {
		return ensure(ctx, opts)
	}
	temporary := make([]string, 0, 4)
	for _, bin := range []string{companionDir, filepath.FromSlash(npmBin)} {
		if bin == "" {
			continue
		}
		clean := filepath.Clean(bin)
		temporary = append(temporary, clean)
		if physical, err := filepath.EvalSymlinks(clean); err == nil && physical != clean {
			temporary = append(temporary, physical)
		}
	}
	dirs := slices.DeleteFunc(filepath.SplitList(path), func(dir string) bool {
		clean := filepath.Clean(dir)
		if slices.Contains(temporary, clean) {
			return true
		}
		physical, err := filepath.EvalSymlinks(clean)
		return err == nil && slices.Contains(temporary, physical)
	})
	if err := os.Setenv("PATH", strings.Join(dirs, string(os.PathListSeparator))); err != nil {
		return depinstall.Tools{}, fmt.Errorf("exclude temporary npm companions from PATH: %w", err)
	}
	defer func() { err = errors.Join(err, os.Setenv("PATH", path)) }()
	return ensure(ctx, opts)
}

// upCompanion locates a tool to start when the caller skipped dependency checks.
func upCompanion(name, dir string) (string, error) {
	program, err := exec.LookPath(name)
	if err == nil {
		return program, nil
	}
	program, err = exec.LookPath(filepath.Join(dir, name))
	if err != nil {
		return "", fmt.Errorf("find %s: %w (run highbay up to check and install dependencies)", name, err)
	}
	return program, nil
}

// startUpCompanion launches a shared service with no stdin and bounds waiting
// for its output, including when a background service retains the output pipe.
func startUpCompanion(exe string, env []string, out io.Writer, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), companionStartTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, exe, args...)
	command.WaitDelay = 2 * time.Second
	command.Env = env
	command.Stdout = out
	command.Stderr = out
	err := command.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%s %s did not finish within %s", filepath.Base(exe), strings.Join(args, " "), companionStartTimeout)
	}
	return err
}
