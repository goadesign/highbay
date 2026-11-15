// This file exercises build-owned generation before any native compiler work.
package controlplane

import (
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	gensurlaclient "github.com/goadesign/surla/gen/http/surla/client"
	genmcpclient "github.com/goadesign/surla/gen/jsonrpc/mcp_surla/client"
	genmcpsurla "github.com/goadesign/surla/gen/mcp_surla"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designversion"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
)

// TestBuildGenerationWritesAgreedDesignBeforeDependencyRefusal reaches the
// generated HTTP consumer, preserving the user's project and prior gen while
// the builder receives actionable dependency commands for its own copy.
func TestBuildGenerationWritesAgreedDesignBeforeDependencyRefusal(t *testing.T) {
	project, cp, _, assignment := buildGenerationFixture(t, ".")
	working := assignment.WorkingFolder
	writeBuildDesignFixture(t, project, "design/design.go", "package design // user's later edit\n")
	writeBuildDesignFixture(t, working, "design/design.go", "package design // builder's stale design\n")
	writeBuildDesignFixture(t, working, "design/design_test.go", "package design // builder test\n")
	writeBuildDesignFixture(t, working, "gen/keep.go", "package gen // prior successful generation\n")
	module := "module example.com/shop\n\ngo 1.27.3\n"
	writeBuildDesignFixture(t, working, "go.mod", module)
	srv := httptest.NewServer(NewHTTPHandler(cp, workflowAuthenticator(map[string]Principal{
		"generation-token": {ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerBuilder},
	})))
	t.Cleanup(srv.Close)
	origin, err := url.Parse(srv.URL)
	require.NoError(t, err)
	transport := gensurlaclient.NewClient(origin.Scheme, origin.Host,
		generationDoer{client: srv.Client()}, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	client := &gensurla.Client{GenerateCodeEndpoint: transport.GenerateCode()}
	_, err = client.GenerateCode(t.Context(), &gensurla.GenerateCodePayload{})
	require.Error(t, err)
	assertServiceError(t, err, "missing_modules")
	assert.Contains(t, err.Error(), "GOWORK=off go get goa.design/goa/v3@v3.35.0")
	assert.Contains(t, err.Error(), "GOWORK=off go mod tidy")
	assert.Contains(t, err.Error(), "generate_code again")
	mcpTransport := genmcpclient.NewClient(origin.Scheme, origin.Host,
		generationDoer{client: srv.Client()}, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	mcpClient := &genmcpsurla.Client{ToolsCallEndpoint: mcpTransport.ToolsCall()}
	args, err := gensurla.EncodeGenerateCodePayload(&gensurla.GenerateCodePayload{})
	require.NoError(t, err)
	call, err := mcpClient.ToolsCall(t.Context(), &genmcpsurla.ToolsCallPayload{Name: "generate_code", Arguments: args})
	require.NoError(t, err)
	complete, ok := call.Outcome.AsComplete()
	require.True(t, ok)
	require.NotNil(t, complete.IsError)
	assert.True(t, *complete.IsError)
	require.NotEmpty(t, complete.Content)
	require.NotNil(t, complete.Content[0].Text)
	assert.Contains(t, *complete.Content[0].Text, "GOWORK=off go get goa.design/goa/v3@v3.35.0")
	agreed, err := designversion.Canonical("design/design.go", buildProbeDesign)
	require.NoError(t, err)
	for name, want := range map[string]string{
		"design/design.go":      agreed,
		"design/design_test.go": "package design // builder test\n",
		"gen/keep.go":           "package gen // prior successful generation\n",
		"go.mod":                module,
	} {
		got, readErr := os.ReadFile(filepath.Join(working, filepath.FromSlash(name)))
		require.NoError(t, readErr)
		assert.Equal(t, want, string(got), name)
	}
	got, err := os.ReadFile(filepath.Join(project, "design/design.go"))
	require.NoError(t, err)
	assert.Equal(t, "package design // user's later edit\n", string(got))
}

// TestBuildGenerationRefusesBeforeWriting checks real worker readiness and
// stale-revision refusals against a previously generated builder file.
func TestBuildGenerationRefusesBeforeWriting(t *testing.T) {
	for _, state := range []string{"unread", "dropped", "off branch", "stale revision", "busy"} {
		t.Run(state, func(t *testing.T) {
			_, cp, card, assignment := buildGenerationFixture(t, ".")
			working := assignment.WorkingFolder
			writeBuildDesignFixture(t, working, "design/design.go", "package design // must stay\n")
			switch state {
			case "unread":
				cp.mu.Lock()
				build, found, err := cp.cards.loadOpenBuild()
				require.NoError(t, err)
				require.True(t, found)
				build.Builder.Read = false
				require.NoError(t, cp.cards.saveOpenBuild(build))
				cp.mu.Unlock()
			case "dropped":
				build, found, err := cp.cards.loadOpenBuild()
				require.NoError(t, err)
				require.True(t, found)
				_, err = cp.DropBuild(asCaller("alice@example.com", CallerUser), &gensurla.DropBuildPayload{
					ChangeID: card.ChangeID, StartedAt: build.StartedAt.UTC().Format(time.RFC3339Nano),
				})
				require.NoError(t, err)
			case "off branch":
				runGitForTest(t, working, "switch", "--quiet", "--detach")
			case "stale revision":
				record := loadMetadataRecord(t, cp, card.ChangeID)
				record.card.Prompt += " More agreed work."
				var err error
				record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
				require.NoError(t, err)
				saveMetadataRecord(t, cp, record)
			case "busy":
				cp.builderCopyTurn <- struct{}{}
				t.Cleanup(func() { <-cp.builderCopyTurn })
			}
			_, err := cp.GenerateCode(asCaller("alice@example.com", CallerBuilder), &gensurla.GenerateCodePayload{})
			require.Error(t, err)
			switch state {
			case "unread":
				assertServiceError(t, err, "assignment_unread")
			case "dropped":
				assertServiceError(t, err, "no_build")
			case "off branch":
				var refusal *gensurla.CopyOffBranch
				require.ErrorAs(t, err, &refusal)
				assert.Equal(t, "copy_off_branch", refusal.Name)
			default:
				assertServiceError(t, err, "conflict")
			}
			got, err := os.ReadFile(filepath.Join(working, "design/design.go"))
			require.NoError(t, err)
			assert.Equal(t, "package design // must stay\n", string(got))
		})
	}
}

// TestBuildGenerationRefusesNestedPathLinks preserves both design and gen
// when an ordinary uncommitted directory link points at the user's project.
func TestBuildGenerationRefusesNestedPathLinks(t *testing.T) {
	project, cp, _, assignment := buildGenerationFixture(t, "apps/shop")
	writeBuildDesignFixture(t, project, "design/design.go", "package design // user's source\n")
	writeBuildDesignFixture(t, project, "gen/keep.go", "package gen // user's generated files\n")
	parent := filepath.Dir(assignment.WorkingFolder)
	require.NoError(t, os.Rename(parent, filepath.Join(t.TempDir(), "saved-apps")))
	require.NoError(t, os.Symlink(filepath.Dir(project), parent))
	_, err := cp.GenerateCode(asCaller("alice@example.com", CallerBuilder), &gensurla.GenerateCodePayload{})
	assertServiceError(t, err, "unprocessable")
	for name, want := range map[string]string{
		"design/design.go": "package design // user's source\n",
		"gen/keep.go":      "package gen // user's generated files\n",
	} {
		got, err := os.ReadFile(filepath.Join(project, filepath.FromSlash(name)))
		require.NoError(t, err)
		assert.Equal(t, want, string(got))
	}
}

// buildGenerationFixture creates a real numbered builder copy whose agreed
// design needs Goa, without invoking evaluation or generation during setup.
func buildGenerationFixture(t *testing.T, prefix string) (
	string, *ControlPlane, *gensurla.ChangeCardResult, *gensurla.BuilderAssignment,
) {
	t.Helper()
	repo, project := productionBuildProject(t, prefix, reviewEval)
	cp := newControlPlane(t, Options{
		WorkspaceDir: project,
		Team:         &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{}},
	})
	cp.buildSandboxCheck = func() error { return nil }
	writeBuildDesignFixture(t, project, "go.mod", "module example.com/shop\n\ngo 1.27.3\n\nrequire goa.design/goa/v3 v3.35.0\n")
	writeBuildDesignFixture(t, project, "design/design.go", buildProbeDesign)
	runGitForTest(t, project, "add", "go.mod", "design/design.go")
	runGitForTest(t, repo, "commit", "--quiet", "-m", "Save fixture design")
	proposed := saveProposedCardFixture(t, cp, "alice@example.com", "generation",
		"Build the shop.", refundAnalysis("Build the shop."))
	record := loadMetadataRecord(t, cp, proposed.ChangeID)
	var err error
	record.designSource, err = encodeDesignSource(map[string]string{"design/design.go": buildProbeDesign})
	require.NoError(t, err)
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
	require.NoError(t, err)
	saveMetadataRecord(t, cp, record)
	proposed = attachBuildSnapshot(t, cp, proposed.ChangeID)
	card, err := cp.ConfirmIntent(asCaller("alice@example.com", CallerUser), &gensurla.ConfirmIntentPayload{
		ChangeID: proposed.ChangeID, RequestRevision: proposed.RequestRevision,
		Answers: map[string]string{}, Examples: proposed.Examples,
	})
	require.NoError(t, err)
	_, err = cp.StartBuild(asCaller("alice@example.com", CallerUser), &gensurla.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	assignment, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder),
		&gensurla.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	return project, cp, card, assignment
}
