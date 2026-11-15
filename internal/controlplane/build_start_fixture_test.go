// This file supplies deterministic working-copy stand-ins for build contracts.
package controlplane

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/scaffold"
	"github.com/goadesign/surla/internal/testmodule"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/modfile"
)

// buildCopyFixture performs no Git or candidate execution. Hooks model the
// owner's preparation boundary, including concurrent request effects, and the
// report checker's check and record. Without a check hook, a check waits until
// its context ends, as one that never finishes would, so a report stays
// reported in tests that do not judge it.
type buildCopyFixture struct {
	folder  string
	prepare func(context.Context, []designeval.StorySummary) error
	check   func(context.Context, []designeval.StorySummary) (testwriter.Verdict, error)
	record  func(context.Context, testwriter.Verdict, string, string) (string, error)
}

// Project is the test writer's independently assigned project folder.
func (f *buildCopyFixture) Project() string { return f.folder }

// Prepare applies a generation outcome to the Story actually saved by the start.
func (f *buildCopyFixture) Prepare(
	ctx context.Context, _, _ string, stories []designeval.StorySummary,
) error {
	if f.prepare != nil {
		return f.prepare(ctx, stories)
	}
	return nil
}

// Check runs the check hook, or waits until ctx ends.
func (f *buildCopyFixture) Check(
	ctx context.Context, _, _ string, stories []designeval.StorySummary,
) (testwriter.Verdict, error) {
	if f.check != nil {
		return f.check(ctx, stories)
	}
	<-ctx.Done()
	return testwriter.Verdict{}, ctx.Err()
}

// Record runs the record hook, or returns a fixed commit.
func (f *buildCopyFixture) Record(
	ctx context.Context, v testwriter.Verdict, _, checkedTip, message string,
) (string, error) {
	if f.record != nil {
		return f.record(ctx, v, checkedTip, message)
	}
	return strings.Repeat("a", 40), nil
}

// newBuildControlPlane binds the explicitly simulated team, platform checks and
// working copy used by tests of cards/assignments rather than Git or generation.
// The real source decision still reads a starter layout from temporary files.
func newBuildControlPlane(t *testing.T, opts Options) *ControlPlane {
	t.Helper()
	if opts.Team == nil {
		opts.Team = &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{}}
	}
	opts = buildFixtureOptions(t, opts)
	cp := newControlPlane(t, opts)
	cp.buildIdentityCheck = func(context.Context, string) error { return nil }
	_, prefix, err := resolveStarterRepo(t.Context(), cp.activeWorkspaceDir)
	require.NoError(t, err)
	copy := &buildCopyFixture{folder: filepath.Join(cp.activeWorkspaceDir, testwriter.Folder, filepath.FromSlash(prefix))}
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		return copy, nil
	}
	return cp
}

// buildFixtureOptions supplies a real temporary Git project without installing
// a team, runtime seams, or cleanup. Stored consumer and lifecycle tests retain
// their own constructor, supervision and Close assertions.
func buildFixtureOptions(t *testing.T, opts Options) Options {
	t.Helper()
	if opts.WorkspaceDir == "" {
		opts.WorkspaceDir = t.TempDir()
	}
	goMod := filepath.Join(opts.WorkspaceDir, "go.mod")
	data, err := os.ReadFile(goMod)
	if os.IsNotExist(err) {
		data = []byte(fmt.Sprintf("module example.com/shop\n\ngo 1.27.3\n\nrequire github.com/goadesign/surla/story %s\n", testmodule.SurlaVersion(t, "github.com/goadesign/surla/story")))
		require.NoError(t, os.WriteFile(goMod, data, 0o600))
	} else {
		require.NoError(t, err)
	}
	evalMod := filepath.Join(opts.WorkspaceDir, scaffold.SuiteDir, "go.mod")
	if _, err := os.Stat(evalMod); os.IsNotExist(err) {
		require.NoError(t, os.MkdirAll(filepath.Dir(evalMod), 0o700))
		require.NoError(t, os.WriteFile(evalMod, []byte("module "+modfile.ModulePath(data)+"/eval\n\ngo 1.27.3\n"), 0o600))
	} else {
		require.NoError(t, err)
	}
	ensureBuildFixtureRepository(t, opts.WorkspaceDir)
	return opts
}

// makeBuildFixtureBuilderCopy supplies the real copy omitted by the simulated
// Build factory. Page consumers call it after Build and before an actual builder
// assignment read, using the saved number and base without replacing the reader.
func makeBuildFixtureBuilderCopy(t *testing.T, cp *ControlPlane) {
	t.Helper()
	cp.mu.RLock()
	build, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	require.True(t, found, "the page fixture must start its build before making its copy")
	workspace, _, _ := cp.currentWorkspaceTarget()
	_, err = testwriter.MakeCopy(t.Context(), workspace, testwriter.BuilderRole,
		testwriter.BuilderRole.Branch(build.Number), build.Base)
	require.NoError(t, err)
}
