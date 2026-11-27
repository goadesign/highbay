// This file reports the running server's version and installs a newer
// release in place: the page and the highbay update command ask, the
// composition root's bindings download, verify and install, and the server
// then shuts down in order and is replaced by the new executable.
package project

import (
	"context"
	"errors"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/selfupdate"
)

type (
	// UpdateDependencies binds what the server needs to report its version
	// and replace itself with a newer release. The composition root supplies
	// them with BindUpdates before the server listens; a server without
	// them reports a development build and can install nothing.
	UpdateDependencies struct {
		// Version is the version this executable reports, such as v0.5.0,
		// or (devel) for a build that is not a release.
		Version string
		// Latest reads the newest stable release.
		Latest func(context.Context) (selfupdate.Release, error)
		// CanAutoUpdate reports whether this installation can be replaced
		// in place and, when it cannot, the command that updates it.
		CanAutoUpdate func() (can bool, manualCommand string)
		// Install installs exactly version (three numbers, no prefix) and
		// returns the executable to run next. The installer's lock on that
		// executable's folder is held until release is called.
		Install func(ctx context.Context, version string) (next string, release func() error, err error)
		// Prepare runs the new executable's own setup, which refreshes the
		// programs it works with and its registrations with coding agents.
		Prepare func(ctx context.Context, next string) error
		// Replace starts the server's ordered shutdown, after which the
		// composition root replaces this process with next and the lock is
		// released. It returns at once.
		Replace func(next string, release func() error)
	}
)

// developmentVersion is the version of a build that is not a release.
const developmentVersion = "(devel)"

// BindUpdates supplies the release bindings before the server listens.
func (s *Server) BindUpdates(updates UpdateDependencies) {
	s.updates = updates
}

// GetUpdateStatus tells the page whether a newer stable release exists and
// whether this server can install it in place. It returns
// release_unavailable when the newest release cannot be read.
func (s *Server) GetUpdateStatus(ctx context.Context) (*genproject.UpdateStatusResult, error) {
	if err := operationOnly(ctx, "get_update_status"); err != nil {
		return nil, err
	}
	if s.updates.Latest == nil || s.updates.CanAutoUpdate == nil {
		return nil, genproject.MakeReleaseUnavailable(errors.New("this Highbay build does not check for releases"))
	}
	release, err := s.updates.Latest(ctx)
	if err != nil {
		// The error may quote what the release server answered.
		return nil, genproject.MakeReleaseUnavailable(errors.New("the latest Highbay release could not be read; check the network connection and try again"))
	}
	result := &genproject.UpdateStatusResult{
		CurrentVersion: s.version(),
		LatestVersion:  "v" + release.Version,
		ReleaseURL:     release.URL,
	}
	if current, stable := selfupdate.NormalizeBuildVersion(s.version()); stable {
		result.UpdateAvailable = selfupdate.VersionBefore(current, release.Version)
	}
	can, manual := s.updates.CanAutoUpdate()
	result.CanAutoUpdate = can
	if !can {
		result.ManualCommand = &manual
	}
	return result, nil
}

// ApplyUpdate installs exactly the release the caller approved and has the
// server replace itself with it, keeping open pages signed in. It answers
// "up_to_date" when this server already runs that version, and
// "restarting" once the new executable is installed and prepared; the
// server then finishes the requests in progress, shuts down in order and
// becomes the new version on the same address.
//
// It returns unprocessable for a version that is not three numbers,
// conflict while another update is in progress, when this installation
// cannot be updated in place, or when the installed executable is already
// newer, and release_unavailable when the release cannot be fetched or
// verified. Once accepted, the installation continues if the caller
// disconnects.
func (s *Server) ApplyUpdate(ctx context.Context, p *genproject.ApplyUpdatePayload) (*genproject.ApplyUpdateResult, error) {
	if err := operationOnly(ctx, "apply_update"); err != nil {
		return nil, err
	}
	target := strings.TrimPrefix(strings.TrimSpace(p.Version), "v")
	if _, err := selfupdate.ParseVersion(target); err != nil {
		return nil, genproject.MakeUnprocessable(errors.New("the version to install must be three numbers, such as 0.5.1"))
	}
	ctx, span := otel.Tracer("highbay/project").Start(context.WithoutCancel(ctx), "highbay.project.apply_update")
	defer span.End()
	span.SetAttributes(attribute.String("highbay.update.target", target))
	refuse := func(refusal error, reason string) (*genproject.ApplyUpdateResult, error) {
		span.SetAttributes(attribute.String("highbay.update.outcome", reason))
		return nil, refusal
	}
	if current, stable := selfupdate.NormalizeBuildVersion(s.version()); stable && current == target {
		span.SetAttributes(attribute.String("highbay.update.outcome", "up_to_date"))
		return &genproject.ApplyUpdateResult{Status: "up_to_date", Version: "v" + target}, nil
	}
	updates := s.updates
	if updates.CanAutoUpdate == nil || updates.Install == nil || updates.Prepare == nil || updates.Replace == nil {
		return refuse(genproject.MakeConflict(errors.New("this Highbay build cannot update itself")), "unsupported")
	}
	if !s.updating.TryLock() {
		return refuse(genproject.MakeConflict(errors.New("an update is already in progress; wait for Highbay to restart")), "in_progress")
	}
	// The lock is kept once the replacement is under way: no second update
	// may start in a server that is shutting down to be replaced.
	replacing := false
	defer func() {
		if !replacing {
			s.updating.Unlock()
		}
	}()
	if can, manual := updates.CanAutoUpdate(); !can {
		return refuse(genproject.MakeConflict(errors.New("this installation cannot update itself; update it with: "+manual)), "manual")
	}
	next, release, err := updates.Install(ctx, target)
	switch {
	case errors.Is(err, selfupdate.ErrReleaseUnavailable):
		return refuse(genproject.MakeReleaseUnavailable(errors.New("Highbay v"+target+" could not be downloaded or verified; check the network connection and try again")), "release_unavailable")
	case errors.Is(err, selfupdate.ErrWouldDowngrade):
		return refuse(genproject.MakeConflict(errors.New("the installed Highbay is already newer than v"+target+"; restart Highbay to run it")), "would_downgrade")
	case err != nil:
		// The installer's error may name paths and command output.
		span.RecordError(errors.New("install failed"))
		span.SetStatus(codes.Error, "install the update")
		return refuse(genproject.MakeConflict(errors.New("Highbay v"+target+" could not be installed here; update it from a terminal with highbay update")), "install_failed")
	}
	if err := updates.Prepare(ctx, next); err != nil {
		span.RecordError(errors.New("setup of the new version failed"))
		span.SetStatus(codes.Error, "prepare the update")
		if releaseErr := release(); releaseErr != nil {
			span.RecordError(errors.New("release the installer lock"))
		}
		return refuse(genproject.MakeConflict(errors.New("Highbay v"+target+" is installed but its setup did not finish; run highbay up in a terminal, then start Highbay again")), "prepare_failed")
	}
	replacing = true
	span.SetAttributes(attribute.String("highbay.update.outcome", "restarting"))
	updates.Replace(next, release)
	return &genproject.ApplyUpdateResult{Status: "restarting", Version: "v" + target}, nil
}

// operationOnly admits the caller kinds the one role table allows for
// operation, and answers every other caller as forbidden.
func operationOnly(ctx context.Context, operation string) error {
	principal, ok := controlplane.PrincipalFromContext(ctx)
	if !ok || !controlplane.OperationAllowed(principal.Kind, operation) {
		return genproject.MakeForbidden(errors.New("caller may not call " + operation))
	}
	return nil
}

// version returns the version this server reports.
func (s *Server) version() string {
	if s.updates.Version == "" {
		return developmentVersion
	}
	return s.updates.Version
}
