// This file proves check helpers bind and reserve the current builder copy.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gensurlaclient "github.com/goadesign/surla/gen/http/surla/client"
	genmcpclient "github.com/goadesign/surla/gen/jsonrpc/mcp_surla/client"
	genmcpsurla "github.com/goadesign/surla/gen/mcp_surla"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/verify/candidate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	goahttp "goa.design/goa/v3/http"
)

// TestBuilderCopyCheckPublishesOnlyInItsCopy exercises both public service
// entries through the real helper, with root and nested module copies.
func TestBuilderCopyCheckPublishesOnlyInItsCopy(t *testing.T) {
	installFakeDocker(t)
	for _, prefix := range []string{".", "apps/shop"} {
		t.Run(prefix, func(t *testing.T) {
			project, cp, card, assignment := buildGenerationFixture(t, prefix)
			configureBuilderCopyCheck(t, cp)
			// An agreed revision may advance while the builder repairs files
			// in its existing assignment; these tools do not generate design.
			if prefix != "." {
				record := loadMetadataRecord(t, cp, card.ChangeID)
				record.card.Prompt += " More agreed work."
				var err error
				record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
				require.NoError(t, err)
				saveMetadataRecord(t, cp, record)
			}
			daemon := newFakeDockerDaemon(t)
			t.Setenv("DOCKER_HOST", daemon.hostURI)
			working := assignment.WorkingFolder
			_, _, grant, err := builderCopyFolder(t.Context(), project)
			require.NoError(t, err)
			moduleRel, err := filepath.Rel(grant, working)
			require.NoError(t, err)
			writeBuildDesignFixture(t, project, "builder-marker", "user project\n")
			writeBuildDesignFixture(t, project, "go.sum", "user sum\n")
			writeBuildDesignFixture(t, project, "go.work", "user workspace\n")
			writeBuildDesignFixture(t, working, "builder-marker", "builder copy\n")
			writeBuildDesignFixture(t, working, "go.sum", "builder sum\n")
			var starts atomic.Int64
			daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
				starts.Add(1)
				mount := findMount(t, c, grant)
				assert.Equal(t, working, c.WorkDir)
				assertFileContent(t, filepath.Join(mount.Source, moduleRel, "builder-marker"), "builder copy\n")
				assert.NoFileExists(t, filepath.Join(mount.Source, moduleRel, "go.work"))
				if slices.Equal(c.Args, []string{"mod", "tidy"}) {
					assert.NoError(t, os.WriteFile(filepath.Join(mount.Source, moduleRel, "go.sum"), []byte("repaired builder sum\n"), 0o644))
				}
				return "checked builder copy\n", "", 0
			})
			ctx := asCaller("alice@example.com", CallerBuilder)
			checked, err := cp.CheckProject(ctx)
			require.NoError(t, err)
			assert.True(t, checked.Passed)
			repaired, err := cp.UpdateDependencies(ctx)
			require.NoError(t, err)
			assert.Equal(t, []string{"go.sum"}, repaired.FilesWritten)
			assert.Equal(t, int64(3), starts.Load())
			assertFileContent(t, filepath.Join(working, "go.sum"), "repaired builder sum\n")
			assertFileContent(t, filepath.Join(project, "go.sum"), "user sum\n")
			assertFileContent(t, filepath.Join(project, "go.work"), "user workspace\n")
			assert.Empty(t, cp.builderCopyTurn)
		})
	}
}

// TestBuilderCopyCheckRefusesBeforeStarting preserves the builder and project
// when readiness, branch ownership or exclusive use fails.
func TestBuilderCopyCheckRefusesBeforeStarting(t *testing.T) {
	for _, state := range []string{"unread", "dropped", "off branch", "busy", "nested link", "missing folder"} {
		t.Run(state, func(t *testing.T) {
			project, cp, card, assignment := buildGenerationFixture(t, "apps/shop")
			configureBuilderCopyCheck(t, cp)
			writeBuildDesignFixture(t, project, "go.sum", "user sum\n")
			writeBuildDesignFixture(t, assignment.WorkingFolder, "go.sum", "builder sum\n")
			var launches atomic.Int64
			cp.checkHelper = func() *exec.Cmd {
				launches.Add(1)
				return exec.Command(filepath.Join(t.TempDir(), "must-not-start"))
			}
			want := "working_copy_busy"
			switch state {
			case "unread":
				build, found, err := cp.cards.loadOpenBuild()
				require.NoError(t, err)
				require.True(t, found)
				build.Builder.Read = false
				require.NoError(t, cp.cards.saveOpenBuild(build))
				want = "assignment_unread"
			case "dropped":
				build, found, err := cp.cards.loadOpenBuild()
				require.NoError(t, err)
				require.True(t, found)
				_, err = cp.DropBuild(asCaller("alice@example.com", CallerUser), &gensurla.DropBuildPayload{
					ChangeID: card.ChangeID, StartedAt: build.StartedAt.UTC().Format(time.RFC3339Nano),
				})
				require.NoError(t, err)
				want = "no_build"
			case "off branch":
				runGitForTest(t, assignment.WorkingFolder, "switch", "--quiet", "--detach")
				want = "copy_off_branch"
			case "busy":
				cp.builderCopyTurn <- struct{}{}
				t.Cleanup(func() { <-cp.builderCopyTurn })
			case "nested link":
				parent := filepath.Dir(assignment.WorkingFolder)
				require.NoError(t, os.Rename(parent, filepath.Join(t.TempDir(), "saved-apps")))
				require.NoError(t, os.Symlink(filepath.Dir(project), parent))
				want = "unprocessable"
			case "missing folder":
				require.NoError(t, os.RemoveAll(assignment.WorkingFolder))
				want = "working_folder_missing"
			}
			ctx := asCaller("alice@example.com", CallerBuilder)
			_, err := cp.CheckProject(ctx)
			_, updateErr := cp.UpdateDependencies(ctx)
			refusals := []error{err, updateErr}
			if state == "missing folder" {
				_, generationErr := cp.GenerateCode(ctx, &gensurla.GenerateCodePayload{})
				refusals = append(refusals, generationErr)
			}
			for _, refusal := range refusals {
				if want == "copy_off_branch" {
					var offBranch *gensurla.CopyOffBranch
					require.ErrorAs(t, refusal, &offBranch)
					assert.Equal(t, want, offBranch.Name)
				} else {
					assertServiceError(t, refusal, want)
				}
			}
			assert.Zero(t, launches.Load())
			assertFileContent(t, filepath.Join(project, "go.sum"), "user sum\n")
			if state != "busy" {
				assert.Empty(t, cp.builderCopyTurn)
			}
		})
	}
}

// TestBuilderCopyCheckTurnCoversCleanup holds the real helper during container
// cleanup; neither another tool nor cancellation can release its builder turn.
func TestBuilderCopyCheckTurnCoversCleanup(t *testing.T) {
	installFakeDocker(t)
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled=%t", canceled), func(t *testing.T) {
			_, cp, _, _ := buildGenerationFixture(t, ".")
			configureBuilderCopyCheck(t, cp)
			daemon := newFakeDockerDaemon(t)
			t.Setenv("DOCKER_HOST", daemon.hostURI)
			held, release := make(chan struct{}), make(chan struct{})
			releaseOnce := sync.OnceFunc(func() { close(release) })
			defer releaseOnce()
			var first atomic.Bool
			daemon.setBeforeCall(func(ctx context.Context, verb string) {
				if verb != "rm" || !first.CompareAndSwap(false, true) {
					return
				}
				close(held)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
			ctx, cancel := context.WithCancel(asCaller("alice@example.com", CallerBuilder))
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := cp.CheckProject(ctx)
				done <- err
			}()
			wait, stop := context.WithTimeout(t.Context(), lifecycleWaitTimeout)
			defer stop()
			select {
			case <-held:
			case <-wait.Done():
				require.FailNow(t, "helper did not reach cleanup")
			}
			if canceled {
				cancel()
			}
			_, err := cp.UpdateDependencies(asCaller("alice@example.com", CallerBuilder))
			assertServiceError(t, err, "working_copy_busy")
			assert.Len(t, cp.builderCopyTurn, 1)
			select {
			case err := <-done:
				require.FailNowf(t, "helper returned before cleanup", "%v", err)
			default:
			}
			releaseOnce()
			select {
			case err := <-done:
				if canceled {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			case <-wait.Done():
				require.FailNow(t, "helper did not finish cleanup")
			}
			assert.Empty(t, cp.builderCopyTurn)
		})
	}
}

// TestBuilderCopyCheckReleasesAfterLaunchFailure covers failures after the
// operation directory exists but before the helper takes ownership of it.
func TestBuilderCopyCheckReleasesAfterLaunchFailure(t *testing.T) {
	_, cp, _, _ := buildGenerationFixture(t, ".")
	configureBuilderCopyCheck(t, cp)
	cp.checkHelper = func() *exec.Cmd { return exec.Command(filepath.Join(t.TempDir(), "missing-helper")) }
	for _, tool := range []string{checkToolProject, checkToolDependencies} {
		_, err := cp.runCheckHelper(asCaller("alice@example.com", CallerBuilder), tool)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "start the check helper")
		assert.Empty(t, cp.builderCopyTurn)
		entries, err := os.ReadDir(cp.checkWorkDir)
		require.NoError(t, err)
		assert.Empty(t, entries)
	}
}

// TestBuilderCopyCheckBusyHTTPMCP proves the generated HTTP error contract and
// MCP tool result preserve the busy-copy refusal before any helper starts.
func TestBuilderCopyCheckBusyHTTPMCP(t *testing.T) {
	_, cp, _, assignment := buildGenerationFixture(t, ".")
	configureBuilderCopyCheck(t, cp)
	design, err := os.ReadFile(filepath.Join(assignment.WorkingFolder, "design", "design.go"))
	require.NoError(t, err)
	var launches atomic.Int64
	cp.checkHelper = func() *exec.Cmd {
		launches.Add(1)
		return exec.Command(filepath.Join(t.TempDir(), "must-not-start"))
	}
	cp.builderCopyTurn <- struct{}{}
	t.Cleanup(func() { <-cp.builderCopyTurn })
	handler := NewHTTPHandler(cp, workflowAuthenticator(map[string]Principal{
		"generation-token": {ID: "alice@example.com"},
	}))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	origin, err := url.Parse(srv.URL)
	require.NoError(t, err)
	doer := generationDoer{client: srv.Client()}
	httpTransport := gensurlaclient.NewClient(origin.Scheme, origin.Host,
		doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	client := &gensurla.Client{
		CheckProjectEndpoint: httpTransport.CheckProject(), UpdateDependenciesEndpoint: httpTransport.UpdateDependencies(),
		GenerateCodeEndpoint: httpTransport.GenerateCode(),
	}
	mcpTransport := genmcpclient.NewClient(origin.Scheme, origin.Host,
		doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	mcpClient := &genmcpsurla.Client{ToolsCallEndpoint: mcpTransport.ToolsCall()}
	for _, tool := range []struct{ name, route string }{
		{checkToolProject, "/workspace/check"},
		{checkToolDependencies, "/workspace/dependencies"},
		{"generate_code", "/workspace/generate"},
	} {
		t.Run(tool.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, tool.route, strings.NewReader(`{}`))
			request.Header.Set("Authorization", "Bearer generation-token-builder")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			require.Equal(t, http.StatusConflict, response.Code)
			var body goahttp.ErrorResponse
			require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
			assert.Equal(t, "working_copy_busy", body.Name)
			assert.Contains(t, body.Message, "call "+tool.name+" again")
			var err error
			switch tool.name {
			case checkToolProject:
				_, err = client.CheckProject(t.Context())
			case checkToolDependencies:
				_, err = client.UpdateDependencies(t.Context())
			case "generate_code":
				_, err = client.GenerateCode(t.Context(), &gensurla.GenerateCodePayload{})
			}
			assertServiceError(t, err, "working_copy_busy")
			result, err := mcpClient.ToolsCall(t.Context(), &genmcpsurla.ToolsCallPayload{
				Name: tool.name, Arguments: json.RawMessage(`{}`),
			})
			require.NoError(t, err)
			complete, ok := result.Outcome.AsComplete()
			require.True(t, ok)
			require.NotNil(t, complete.IsError)
			assert.True(t, *complete.IsError)
			require.NotEmpty(t, complete.Content)
			require.NotNil(t, complete.Content[0].Text)
			assert.Contains(t, *complete.Content[0].Text, "call "+tool.name+" again")
		})
	}
	assert.Zero(t, launches.Load())
	assert.Len(t, cp.builderCopyTurn, 1)
	after, err := os.ReadFile(filepath.Join(assignment.WorkingFolder, "design", "design.go"))
	require.NoError(t, err)
	assert.Equal(t, design, after)
}

// TestBuilderCopyCheckSpanOutcomes preserves expected worker refusals as
// outcomes, while an actual helper launch failure remains an error span.
func TestBuilderCopyCheckSpanOutcomes(t *testing.T) {
	for _, state := range []string{"busy", "unread", "dropped", "off branch", "helper failed"} {
		t.Run(state, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
			cp := newControlPlane(t, Options{
				WorkspaceDir: newShopModule(t, nil), TracerProvider: provider,
				Checker: candidate.NewContainerRunner(), CheckHelper: testCheckHelper(t), CheckTimeout: time.Minute,
			})
			working := readCheckBuilderFixture(t, cp)
			switch state {
			case "busy":
				cp.builderCopyTurn <- struct{}{}
				defer func() { <-cp.builderCopyTurn }()
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
					ChangeID: build.ChangeID, StartedAt: build.StartedAt.UTC().Format(time.RFC3339Nano),
				})
				require.NoError(t, err)
			case "off branch":
				runGitForTest(t, working, "switch", "--quiet", "--detach")
			case "helper failed":
				cp.checkHelper = func() *exec.Cmd { return exec.Command(filepath.Join(t.TempDir(), "missing-helper")) }
			}
			_, checkErr := cp.CheckProject(asCaller("alice@example.com", CallerBuilder))
			_, updateErr := cp.UpdateDependencies(asCaller("alice@example.com", CallerBuilder))
			require.Error(t, checkErr)
			require.Error(t, updateErr)
			spans := slices.DeleteFunc(exporter.GetSpans(), func(span tracetest.SpanStub) bool {
				return span.Name != "surla.controlplane.check_project" && span.Name != "surla.controlplane.update_dependencies"
			})
			require.Len(t, spans, 2)
			for _, span := range spans {
				var exceptions int
				for _, event := range span.Events {
					if event.Name == "exception" {
						exceptions++
					}
				}
				if state == "helper failed" {
					assert.Equal(t, codes.Error, span.Status.Code)
					assert.Equal(t, 1, exceptions)
				} else {
					assert.Equal(t, codes.Unset, span.Status.Code)
					assert.Zero(t, exceptions)
				}
			}
		})
	}
}

// configureBuilderCopyCheck adds the production helper configuration to the
// build fixture without rebuilding its already-read numbered assignment.
func configureBuilderCopyCheck(t *testing.T, cp *ControlPlane) {
	t.Helper()
	cp.checker = candidate.NewContainerRunner()
	cp.checkHelper = testCheckHelper(t)
	cp.checkTimeout = time.Minute
	cp.checkWorkDir = filepath.Join(cp.cards.dir, "check-work")
	require.NoError(t, os.Mkdir(cp.checkWorkDir, 0o700))
}
