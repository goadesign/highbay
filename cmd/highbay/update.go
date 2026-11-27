// This file checks Highbay releases, confirms the exact update, and observes
// the project's authenticated server through an in-place restart.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/project"
	"github.com/goadesign/highbay/internal/selfupdate"
)

// updateOptions records what the user asked the CLI to check or apply.
type updateOptions struct {
	// check reports what would happen without applying a change.
	check bool
	// yes authorizes the update without prompting.
	yes bool
	// version pins an exact release, or is empty to select the latest release.
	version string
}

const (
	// cliUpdateTimeout leaves time for the server's installation and restart.
	cliUpdateTimeout = 12 * time.Minute
	// updateRestartTimeout bounds reconnecting once the server accepts the update.
	updateRestartTimeout = time.Minute
)

// parseUpdateOptions keeps an explicit approval tied to the exact stable
// version the caller supplied.
func parseUpdateOptions(args []string) (updateOptions, error) {
	var opts updateOptions
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.BoolVar(&opts.check, "check", false, "Report whether an update or restart is available")
	fs.BoolVar(&opts.yes, "yes", false, "Apply the update without asking for confirmation")
	fs.BoolVar(&opts.yes, "y", false, "Shorthand for -yes")
	fs.Func("version", "Install exactly this release (MAJOR.MINOR.PATCH)", func(version string) error {
		if _, err := selfupdate.ParseVersion(version); err != nil {
			return err
		}
		opts.version = version
		return nil
	})
	if err := fs.Parse(args); err != nil {
		return updateOptions{}, err
	}
	if fs.NArg() != 0 {
		return updateOptions{}, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if opts.version != "" && opts.check {
		return updateOptions{}, errors.New("-check cannot be combined with -version")
	}
	return opts, nil
}

// updateProjectIdentity verifies the card-store owner before selecting the
// running installation. A missing record or an idle store needs no RPC.
func updateProjectIdentity(ctx context.Context, state *project.State) (*genproject.ProjectIdentity, error) {
	if state == nil {
		return nil, nil
	}
	running, err := controlplane.CardStoreInUse(state.CardsDir())
	if err != nil || !running {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	identity, err := confirmProject(ctx, state, projectClient(state))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", projectHolder(state), err)
	}
	return identity, nil
}

// reportUpdate writes the installed and running versions that inform approval.
// The returned target is empty when there is no compatible action to take.
func reportUpdate(
	out io.Writer, current string, identity *genproject.ProjectIdentity,
	release selfupdate.Release, exact bool,
) (string, error) {
	running, display := "", ""
	if identity != nil {
		running, display = runningRelease(identity)
	}
	if exact {
		if selfupdate.VersionBefore(release.Version, current) {
			return "", fmt.Errorf("v%s is older than this Highbay binary v%s", release.Version, current)
		}
		if selfupdate.VersionBefore(release.Version, running) {
			return "", fmt.Errorf("v%s is older than the running project server v%s", release.Version, running)
		}
	}
	if selfupdate.VersionBefore(release.Version, running) && !selfupdate.VersionBefore(running, current) {
		_, err := fmt.Fprintf(out, "Highbay v%s is running, newer than the latest release v%s\n", running, release.Version)
		return "", err
	}
	switch {
	case selfupdate.VersionBefore(current, release.Version):
		suffix := ""
		if release.URL != "" {
			suffix = " (" + release.URL + ")"
		}
		_, err := fmt.Fprintf(out, "Update available: highbay v%s -> v%s%s\n", current, release.Version, suffix)
		return release.Version, err
	case identity != nil && running != current:
		_, err := fmt.Fprintf(out, "Restart needed: highbay binary is v%s, running server is %s\n", current, display)
		return current, err
	default:
		_, err := fmt.Fprintf(out, "Highbay v%s is up to date\n", current)
		return "", err
	}
}

// runningRelease keeps development and older server builds readable but only
// compares a validated stable release as a version.
func runningRelease(identity *genproject.ProjectIdentity) (release, display string) {
	if identity.Version == nil {
		return "", "(unknown version)"
	}
	if release, ok := selfupdate.NormalizeBuildVersion(*identity.Version); ok {
		return release, "v" + release
	}
	return "", *identity.Version
}

// applyCLIUpdate rechecks the project after confirmation. The start lock
// keeps another CLI from starting the project during an offline upgrade.
func applyCLIUpdate(
	ctx context.Context, state *project.State, source selfupdate.ReleaseSource,
	target, exe string, out io.Writer,
) (err error) {
	if state != nil {
		lock, lockErr := state.StartLock(ctx)
		if lockErr != nil {
			return lockErr
		}
		defer func() { err = errors.Join(err, lock.Close()) }()
	}
	identity, err := updateProjectIdentity(ctx, state)
	if err != nil {
		return err
	}
	if identity != nil {
		if identity.Version == nil {
			return errors.New("the project server does not report its version. Run highbay stop, then highbay, and retry highbay update")
		}
		current, _ := runningRelease(identity)
		if selfupdate.VersionBefore(target, current) {
			return fmt.Errorf("v%s is older than the running project server v%s", target, current)
		}
		installLocally, err := updateProjectServer(ctx, state, projectClient(state), target, out)
		if err != nil || !installLocally {
			return err
		}
	}
	next, unlock, err := selfupdate.Install(ctx, source, target, exe, out)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock()) }()
	if err := setupUpdatedHighbay(ctx, next, cliUpdateTimeout, out); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Highbay v%s is installed\n", target)
	return err
}

// updateProjectServer sends only the approved version, then reads fresh
// project identity through the same authenticated client. If the server
// already ran the release, the caller still needs to update its local binary.
func updateProjectServer(
	ctx context.Context, state *project.State, client *genproject.Client, target string, out io.Writer,
) (installLocally bool, err error) {
	result, err := client.ApplyUpdate(ctx, &genproject.ApplyUpdatePayload{Version: target})
	if err != nil {
		return false, fmt.Errorf("update the project server to v%s: %w", target, err)
	}
	if version, ok := selfupdate.NormalizeBuildVersion(result.Version); !ok || version != target {
		return false, errors.New("the project server reported a different update target")
	}
	ctx, cancel := context.WithTimeout(ctx, updateRestartTimeout)
	defer cancel()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		identity, err := confirmProject(ctx, state, client)
		if err == nil {
			version, _ := runningRelease(identity)
			switch {
			case version == target:
				if result.Status == "up_to_date" {
					return true, nil
				}
				_, err := fmt.Fprintf(out, "Highbay server is running v%s\n", target)
				return false, err
			case selfupdate.VersionBefore(target, version):
				return false, fmt.Errorf("the project server reports v%s after the update to v%s", version, target)
			}
		} else if errors.Is(err, errWrongProject) {
			return false, err
		}
		select {
		case <-ctx.Done():
			return false, fmt.Errorf("the project server did not report v%s after updating: %w", target, ctx.Err())
		case <-tick.C:
		}
	}
}

// setupUpdatedHighbay refreshes dependencies and global registrations using
// the installed executable within each caller's command budget. Starting
// shared services is a separate action. A nil output discards command output.
func setupUpdatedHighbay(
	ctx context.Context, next string, wait time.Duration, out io.Writer,
) error {
	for _, args := range [][]string{{"up", "--yes"}, {"setup", "-global"}} {
		commandCtx, cancel := context.WithTimeout(ctx, wait)
		cmd := exec.CommandContext(commandCtx, next, args...)
		cmd.Env = append(os.Environ(), "HIGHBAY_NO_START=1")
		cmd.WaitDelay = 2 * time.Second
		cmd.Stdout = out
		cmd.Stderr = out
		err := cmd.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("highbay %s after updating: %w", strings.Join(args, " "), err)
		}
	}
	return nil
}

// update compares this CLI and any running project to the chosen release.
// A read-only check never creates project state or installs tools.
func (runtime projectRuntime) update(args []string, in io.Reader, out io.Writer) error {
	opts, err := parseUpdateOptions(args)
	if err != nil {
		return err
	}
	build := buildVersion()
	if build == "" {
		build = "(devel)"
	}
	current, stable := selfupdate.NormalizeBuildVersion(build)
	if !stable {
		_, manual := selfupdate.CanAutoUpdate("", build, os.Getenv)
		if opts.check {
			_, err := fmt.Fprintf(out, "Highbay %s is a development build. Update with %s.\n", build, manual)
			return err
		}
		return fmt.Errorf("update Highbay with %s", manual)
	}

	state, err := runtime.updateProject()
	if err != nil {
		return err
	}
	identity, err := updateProjectIdentity(context.Background(), state)
	if err != nil {
		return err
	}
	source := selfupdate.DefaultReleaseSource()
	release := selfupdate.Release{Version: opts.version}
	if opts.version == "" {
		release, err = source.Latest(context.Background())
		if err != nil {
			return err
		}
	}
	target, err := reportUpdate(out, current, identity, release, opts.version != "")
	if err != nil || target == "" || opts.check {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate Highbay executable: %w", err)
	}
	if ok, manual := selfupdate.CanAutoUpdate(exe, current, os.Getenv); !ok {
		return fmt.Errorf("update Highbay with %s", manual)
	}
	if !opts.yes {
		verb := "Update Highbay to"
		if target == current {
			verb = "Restart the project server on Highbay"
		}
		if _, err := fmt.Fprintf(out, "%s v%s? [y/N] ", verb, target); err != nil {
			return err
		}
		line, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read update confirmation: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
		default:
			_, err := fmt.Fprintln(out, "Update cancelled")
			return err
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cliUpdateTimeout)
	defer cancel()
	return applyCLIUpdate(ctx, state, source, target, exe, out)
}

// updateProject discovers a previously registered project without creating
// operator storage when checking outside a project.
func (runtime projectRuntime) updateProject() (*project.State, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	// The CLI is reading project records as the operator, without granting
	// access to the current directory as an agent workspace.
	root, err := runtime.operatorRootCandidate("")
	if err != nil {
		return nil, err
	}
	if err := validateOperatorPrivateDir(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read operator storage root %s: %w", root, err)
	}
	state, err := project.Discover(root, cwd)
	if err != nil || state.Record.ID == "" {
		return nil, err
	}
	return state, nil
}
