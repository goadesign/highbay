// This file exercises the builder-copy contract at its HTTP and request-policy
// boundaries before later slices activate its new operations.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genclient "github.com/goadesign/highbay/gen/http/highbay/client"
	"github.com/goadesign/highbay/internal/testwriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"
)

// refusalContractService supplies declared failures through real generated
// transports without pretending the later build/drop implementation exists.
type refusalContractService struct {
	genhighbay.Service
	refusal error
}

// StartBuild returns the test's named refusal without changing a build.
func (s refusalContractService) StartBuild(
	context.Context, *genhighbay.StartBuildPayload,
) (*genhighbay.StartBuildResult, error) {
	return nil, s.refusal
}

// DropBuild returns the test's stale-page refusal without changing a build.
func (s refusalContractService) DropBuild(
	context.Context, *genhighbay.DropBuildPayload,
) (*genhighbay.ChangeBuild, error) {
	return nil, s.refusal
}

// GetBuilderAssignment returns the test's working-copy refusal without marking a read.
func (s refusalContractService) GetBuilderAssignment(
	context.Context, *genhighbay.GetBuilderAssignmentPayload,
) (*genhighbay.BuilderAssignment, error) {
	return nil, s.refusal
}

// TestBuilderCopyRefusalsRoundTripThroughHTTP preserves typed recovery facts,
// including the exact build identity a stale page must show before Drop.
func TestBuilderCopyRefusalsRoundTripThroughHTTP(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := buildableChange(t, cp, "chg")
	started, err := cp.StartBuild(asCaller("alice@example.com", CallerUser),
		&genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	branch, own := "'work'", "highbay/build-2"
	cases := []struct {
		name, operation string
		role            CallerKind
		refusal         error
	}{
		{name: "previous_change_open", operation: "start", role: CallerUser},
		{name: "unfinished_build_open", operation: "start", role: CallerUser},
		{name: "build_changed", operation: "drop", role: CallerUser},
		{name: "drafted_before_drop", operation: "start", role: CallerUser,
			refusal: genhighbay.MakeDraftedBeforeDrop(errors.New("A build was dropped; refresh the draft."))},
		{name: "design_changed_outside", operation: "start", role: CallerUser,
			refusal: genhighbay.MakeDesignChangedOutside(errors.New("Ask the user to commit, then refresh."))},
		{name: "copy_off_branch", operation: "read", role: CallerBuilder,
			refusal: &genhighbay.CopyOffBranch{
				Name: "copy_off_branch", Message: "Your copy is on 'work'; put your work on highbay/build-2 and read again.",
				Branch: &branch, BuildBranch: &own, Operation: "none",
				Uncommitted: []string{"'service.go'"}, UncommittedCount: 1,
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			refusal := tc.refusal
			if refusal == nil {
				refusal = &genhighbay.OpenBuildRefusal{
					Name: tc.name, Message: "The shown build changed; read its facts before dropping it.",
					ChangeID: "chg", Build: started.Build,
				}
			}
			service := refusalContractService{Service: cp, refusal: refusal}
			server := httptest.NewServer(NewHTTPHandler(service,
				func(*http.Request) (Principal, error) {
					return Principal{ID: "alice@example.com", Kind: tc.role}, nil
				}))
			defer server.Close()
			client := genclient.NewClient("http", strings.TrimPrefix(server.URL, "http://"),
				server.Client(), goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
			switch tc.operation {
			case "start":
				_, err = client.StartBuild()(t.Context(), &genhighbay.StartBuildPayload{
					ChangeID: "chg", RequestRevision: card.RequestRevision,
				})
			case "drop":
				_, err = client.DropBuild()(t.Context(), &genhighbay.DropBuildPayload{
					ChangeID: "chg", StartedAt: started.Build.StartedAt,
				})
			case "read":
				_, err = client.GetBuilderAssignment()(t.Context(),
					&genhighbay.GetBuilderAssignmentPayload{ChangeID: "chg"})
			}
			require.Error(t, err)
			switch wanted := refusal.(type) {
			case *genhighbay.OpenBuildRefusal:
				var actual *genhighbay.OpenBuildRefusal
				require.ErrorAs(t, err, &actual)
				assert.Equal(t, wanted, actual)
			case *genhighbay.CopyOffBranch:
				var actual *genhighbay.CopyOffBranch
				require.ErrorAs(t, err, &actual)
				assert.Equal(t, wanted, actual)
			case *goa.ServiceError:
				var actual *goa.ServiceError
				require.ErrorAs(t, err, &actual)
				assert.Equal(t, wanted.Name, actual.Name)
				assert.Equal(t, wanted.ID, actual.ID)
				assert.Equal(t, wanted.Field, actual.Field)
				assert.Equal(t, wanted.Message, actual.Message)
				assert.Equal(t, wanted.Timeout, actual.Timeout)
				assert.Equal(t, wanted.Temporary, actual.Temporary)
				assert.Equal(t, wanted.Fault, actual.Fault)
				assert.Nil(t, actual.Unwrap(), "the HTTP error carries no private cause")
			}
		})
	}
}

// TestTypedRequestRefusalsRemainExpected keeps ordinary typed conflicts out
// of failure telemetry while preserving real faults, including joined faults.
func TestTypedRequestRefusalsRemainExpected(t *testing.T) {
	copyRefusal := &genhighbay.CopyOffBranch{Name: "copy_off_branch", Message: "Read again."}
	openRefusal := &genhighbay.OpenBuildRefusal{Name: "previous_change_open", Message: "Merge or drop."}
	cases := []struct {
		name  string
		err   error
		fault bool
	}{
		{name: "copy refusal", err: copyRefusal},
		{name: "wrapped copy refusal", err: fmt.Errorf("read assignment: %w", copyRefusal)},
		{name: "single joined copy refusal", err: errors.Join(copyRefusal)},
		{name: "open build refusal", err: openRefusal},
		{name: "wrapped open build refusal", err: fmt.Errorf("start build: %w", openRefusal)},
		{name: "standard refusal", err: genhighbay.MakeConflict(errors.New("Read again."))},
		{name: "wrapped standard refusal", err: fmt.Errorf("read assignment: %w",
			genhighbay.MakeConflict(errors.New("Read again.")))},
		{name: "joined storage failure", err: errors.Join(copyRefusal, errors.New("storage failed")), fault: true},
		{name: "joined cleanup failure", err: fmt.Errorf("read assignment: %w",
			errors.Join(openRefusal, errors.New("cleanup failed"))), fault: true},
		{name: "joined standard refusal and storage failure", err: errors.Join(
			genhighbay.MakeConflict(errors.New("Read again.")), errors.New("storage failed")), fault: true},
		{name: "unexpected failure", err: errors.New("storage failed"), fault: true},
		{name: "service fault", err: &goa.ServiceError{Name: "fault", Message: "storage failed", Fault: true}, fault: true},
		{name: "joined fault", err: errors.Join(copyRefusal,
			&goa.ServiceError{Name: "fault", Message: "storage failed", Fault: true}), fault: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			defer func() { assert.NoError(t, provider.Shutdown(t.Context())) }()
			endpoint := authorizeOperation("get_builder_assignment",
				func(context.Context, any) (any, error) { return nil, tc.err }, provider.Tracer("test"))
			_, err := endpoint(asCaller("alice@example.com", CallerBuilder), nil)
			assert.ErrorIs(t, err, tc.err)
			spans := exporter.GetSpans()
			require.Len(t, spans, 1)
			assert.Equal(t, tc.fault, spans[0].Status.Code == codes.Error)
			assert.Equal(t, tc.fault, len(spans[0].Events) > 0)
		})
	}
}

// TestBuilderAssignmentNamesItsCurrentFolder derives the Go workspace advice
// from files in the accepted source root without moving or marking a listed copy.
func TestBuilderAssignmentNamesItsCurrentFolder(t *testing.T) {
	for _, hasWorkspace := range []bool{false, true} {
		t.Run(map[bool]string{false: "no workspace", true: "own workspace"}[hasWorkspace], func(t *testing.T) {
			cp := newBuildControlPlane(t, Options{})
			folder, _, _ := cp.currentWorkspaceTarget()
			copyFolder := filepath.Join(folder, testwriter.BuilderRole.Folder)
			card := buildableChange(t, cp, "chg")
			_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser),
				&genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
			require.NoError(t, err)
			makeBuildFixtureBuilderCopy(t, cp)
			if hasWorkspace {
				require.NoError(t, os.WriteFile(filepath.Join(copyFolder, "go.work"),
					[]byte("go 1.27.3\nuse .\n"), 0o600))
			}
			listed, err := cp.ListBuilderAssignments(asCaller("alice@example.com", CallerBuilder))
			require.NoError(t, err)
			require.Len(t, listed.Assignments, 1)
			assert.Equal(t, copyFolder, listed.Assignments[0].WorkingFolder)
			assert.Equal(t, !hasWorkspace, listed.Assignments[0].GoworkOff)
			assert.False(t, listed.Assignments[0].Read)
			read, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder),
				&genhighbay.GetBuilderAssignmentPayload{ChangeID: "chg"})
			require.NoError(t, err)
			assert.Equal(t, copyFolder, read.WorkingFolder)
			assert.Equal(t, !hasWorkspace, read.GoworkOff)
			assert.True(t, read.Read)
		})
	}
}

// TestBuilderCopyOperationsKeepTheirRoles proves that Drop reports no open
// build while only the guide may update the real draft through HTTP.
func TestBuilderCopyOperationsKeepTheirRoles(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	buildableChange(t, cp, "chg")
	server := roleServer(t, cp)
	for _, role := range []string{"user", "guide", "builder", "tests"} {
		t.Run(role, func(t *testing.T) {
			listed := listToolNames(t, server.URL, role)
			assert.False(t, listed["drop_build"])
			assert.Equal(t, role == "guide", listed["update_draft"])
			for _, op := range []struct{ path, body, caller string }{
				{path: "/changes/chg/build/drop", body: `{"started_at":"2026-01-02T03:04:05Z"}`, caller: "user"},
				{path: "/workspace/draft/update", caller: "guide"},
			} {
				status, body := httpCall(t, server.URL, role, http.MethodPost, op.path, op.body)
				if role == op.caller && op.caller == "user" {
					assert.Equal(t, http.StatusNotFound, status, body)
					assert.Contains(t, body, "no open build")
				} else if role == op.caller {
					assert.Equal(t, http.StatusOK, status, body)
					assert.Contains(t, body, "draft_folder")
				} else {
					assert.Equal(t, http.StatusForbidden, status, body)
				}
			}
			assertNoBuildSaved(t, cp, "chg")
		})
	}
}

// TestBuilderAssignmentChecksOwnershipBeforeFolderFacts moves a nested module
// outside its copy so unauthorized calls must refuse before inspecting it, and an owner's failed
// fact read must leave the assignment unread for a later successful retry.
func TestBuilderAssignmentChecksOwnershipBeforeFolderFacts(t *testing.T) {
	_, workspace := productionBuildProject(t, "apps/shop", reviewEval)
	cp := newBuildControlPlane(t, Options{WorkspaceDir: workspace})
	card := buildableChange(t, cp, "chg")
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser),
		&genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	before := readBuildFile(t, cp, "chg")
	makeBuildFixtureBuilderCopy(t, cp)
	_, folder, _, err := builderCopyFolder(t.Context(), workspace)
	require.NoError(t, err)
	parent := filepath.Dir(folder)
	saved := filepath.Join(t.TempDir(), "saved-apps")
	require.NoError(t, os.Rename(parent, saved))
	require.NoError(t, os.Symlink(filepath.Dir(workspace), parent))

	for _, tc := range []struct {
		name    string
		caller  context.Context
		refusal string
	}{
		{name: "missing principal", caller: t.Context(), refusal: "authenticated principal is required"},
		{name: "another owner", caller: asCaller("bob@example.com", CallerBuilder), refusal: "forbidden"},
		{name: "owner with unavailable facts", caller: asCaller("alice@example.com", CallerBuilder),
			refusal: "outside source directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cp.GetBuilderAssignment(tc.caller,
				&genhighbay.GetBuilderAssignmentPayload{ChangeID: "chg"})
			if tc.refusal == "forbidden" {
				assertRefused(t, err, tc.refusal)
			} else {
				assert.ErrorContains(t, err, tc.refusal)
			}
			assert.Equal(t, before, readBuildFile(t, cp, "chg"),
				"a refused read must not consume the assignment")
		})
	}
	listed, err := cp.ListBuilderAssignments(asCaller("bob@example.com", CallerBuilder))
	require.NoError(t, err, "another owner's empty list must not inspect the folder")
	assert.Empty(t, listed.Assignments)

	require.NoError(t, os.Remove(parent))
	require.NoError(t, os.Rename(saved, parent))
	read, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder),
		&genhighbay.GetBuilderAssignmentPayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.True(t, read.Read, "the same assignment can be read after the filesystem is repaired")
}
