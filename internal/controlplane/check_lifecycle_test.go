// This file exercises the end-to-end check_project, update_dependencies, and
// read_check_output lifecycle through the public ControlPlane service and
// generated HTTP/MCP handlers using the scripted fake docker executable in
// check_lifecycle_fake_test.go.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genmcphighbay "github.com/goadesign/highbay/gen/mcp_highbay"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
)

// pinnedCheck holds the retained state from a CheckProject call whose rm failed.
type pinnedCheck struct {
	// cp is the open ControlPlane that ran the pinned check.
	cp *ControlPlane
	// name and opDir identify the unremoved container and retained directory.
	name, opDir string
}

// lifecycleWaitTimeout bounds synchronization waits in concurrent lifecycle tests
// so a regression that ends a request early fails promptly instead of hanging.
const lifecycleWaitTimeout = 15 * time.Second

// TestCheckFailuresReturnReadableOutput verifies that both tools expose their
// saved output ID through HTTP and MCP on engine and cleanup failures, while
// preserving named HTTP statuses and directories pinned by containers.
func TestCheckFailuresReturnReadableOutput(t *testing.T) {
	installFakeDocker(t)
	daemon := newFakeDockerDaemon(t)
	idPattern := regexp.MustCompile(`read_check_output\(op_id: "([0-9a-f]{32})"\)`)
	output := "output before cleanup failed\n" + strings.Repeat("x", maxCheckMessageBytes) + "\nlast saved output\n"
	for _, tool := range []string{"check_project", "update_dependencies"} {
		for _, transport := range []string{"http", "mcp"} {
			for _, failure := range []struct {
				name, message string
				status        int
			}{
				{"container_cleanup", "daemon blocked removal", http.StatusInternalServerError},
				{"unavailable", "needs a local Docker engine", http.StatusServiceUnavailable},
				{"unprocessable", "no Go module", http.StatusUnprocessableEntity},
			} {
				t.Run(tool+"/"+transport+"/"+failure.name, func(t *testing.T) {
					t.Setenv("DOCKER_HOST", daemon.hostURI)
					workspace := newShopModule(t, nil)
					clients := newCheckTestServer(t, Options{WorkspaceDir: workspace})
					if failure.name == "unprocessable" {
						require.NoError(t, os.Remove(filepath.Join(clients.workingFolder, "go.mod")))
						require.NoError(t, os.Remove(filepath.Join(clients.workingFolder, "eval", "go.mod")))
					}
					if failure.name == "unavailable" {
						t.Setenv("DOCKER_HOST", "tcp://remote.example.test:2375")
					}
					var pinned atomic.Value
					daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
						pinned.Store(c.Name)
						daemon.setFailRemove(c.Name, true)
						return output, "", 0
					})
					// Let the server's existing shutdown recovery remove a pinned
					// container after the assertions finish.
					t.Cleanup(func() {
						if name, ok := pinned.Load().(string); ok {
							daemon.setFailRemove(name, false)
						}
					})
					var message string
					if transport == "http" {
						route := "/workspace/check"
						if tool == "update_dependencies" {
							route = "/workspace/dependencies"
						}
						req := httptest.NewRequest(http.MethodPost, route, nil)
						req.Header.Set("Authorization", "Bearer generation-token-builder")
						response := httptest.NewRecorder()
						NewHTTPHandler(clients.cp, workflowAuthenticator(map[string]Principal{
							"generation-token": {ID: "alice@example.com"},
						})).ServeHTTP(response, req)
						require.Equal(t, failure.status, response.Code)
						var body goahttp.ErrorResponse
						require.NoError(t, json.Unmarshal(response.Body.Bytes(), &body))
						message = body.Message
						if failure.name != "container_cleanup" {
							assert.Equal(t, failure.name, body.Name)
							assert.False(t, body.Fault)
						}
					} else {
						call, err := clients.mcpClient.ToolsCall(t.Context(), &genmcphighbay.ToolsCallPayload{
							Name: tool, Arguments: json.RawMessage(`{}`),
						})
						require.NoError(t, err)
						complete, ok := call.Outcome.AsComplete()
						require.True(t, ok)
						require.NotNil(t, complete.IsError)
						require.True(t, *complete.IsError)
						require.Len(t, complete.Content, 1)
						require.NotNil(t, complete.Content[0].Text)
						message = *complete.Content[0].Text
					}
					assert.Contains(t, message, failure.message)
					assert.Contains(t, message, "output may be incomplete")
					match := idPattern.FindStringSubmatch(message)
					require.Len(t, match, 2, message)
					opID := match[1]
					var saved strings.Builder
					payload := &genhighbay.ReadCheckOutputPayload{OpID: opID}
					for pages := 0; ; pages++ {
						require.Less(t, pages, 3, "saved output must finish within three pages")
						var page *genhighbay.CheckOutputPage
						if transport == "http" {
							var err error
							page, err = clients.httpClient.ReadCheckOutput(t.Context(), payload)
							require.NoError(t, err)
						} else {
							args, err := genhighbay.EncodeReadCheckOutputPayload(payload)
							require.NoError(t, err)
							page = callMCPTool(t, clients.mcpClient, "read_check_output", string(args), genhighbay.DecodeCheckOutputPage)
						}
						saved.WriteString(page.Text)
						if page.NextCursor == nil {
							break
						}
						payload.Cursor = page.NextCursor
					}
					assert.Contains(t, saved.String(), "==> modules:")
					opDir := filepath.Join(clients.cp.checkWorkDir, opID)
					if failure.name == "container_cleanup" {
						name, ok := pinned.Load().(string)
						require.True(t, ok)
						assert.Contains(t, saved.String(), output)
						assert.DirExists(t, filepath.Join(opDir, "src"))
						assert.Equal(t, []ownedContainer{{Name: name, State: stateCreationAnswered}}, readOwnerRecord(t, opDir).Containers)
						assert.Equal(t, 1, daemon.removeCount(name))
					} else {
						assert.NoDirExists(t, filepath.Join(opDir, "src"))
					}
				})
			}
		}
	}
}

// TestCheckProjectLifecycle verifies context-inspect resolution when DOCKER_HOST
// is unset, digest pull on cache miss, Phase 1 (networked go list, read-only
// source, writable modcache) before Phase 2 (offline go test, writable source
// copy, read-only modcache), check -> update_dependencies -> check repair, live
// workspace preservation, and UTF-8/invalid-byte output budgeting over MCP/HTTP.
func TestCheckProjectLifecycle(t *testing.T) {
	ctx := t.Context()
	dockerBin := installFakeDocker(t)
	daemon := newFakeDockerDaemon(t)
	require.NoError(t, os.WriteFile(filepath.Join(filepath.Dir(dockerBin), "context_endpoint.txt"), []byte(daemon.serverURL+"\n"), 0o600))
	t.Setenv("DOCKER_HOST", "")
	t.Setenv("HIGHBAY_OPERATOR_SECRET", "must-not-leak-to-docker")

	workspace := newShopModule(t, map[string]string{
		"go.sum":                      "repaired-sum\n",
		"eval/go.mod":                 "module example.com/shop/eval\n\ngo 1.24\n",
		"eval/eval_test.go":           "package eval\n",
		"testdata/fixture/go.mod":     "module example.com/ignored\n",
		".highbay/cards/c1.json":      "{}\n",
		"reports/public-fixture.json": "public fixture\n",
	})
	runGitForTest(t, workspace, "init", "-q")
	moduleCache := newCheckModuleCache(t)
	expectedModuleCache := moduleCache
	clients := newCheckTestServer(t, Options{BuildCacheDir: t.TempDir(), CheckModuleCacheDir: moduleCache,
		WorkspaceDir: workspace,
		Verification: &VerificationConfig{RunsDir: realTempDir(t)},
		GoProxyEnv:   []string{"GOPROXY=https://proxy.example.test", "GOPRIVATE=example.com/*"},
	})
	project := workspace
	workspace = clients.workingFolder

	tools, err := clients.mcpClient.ToolsList(ctx, &genmcphighbay.ToolsListPayload{})
	require.NoError(t, err)
	for _, want := range []string{"check_project", "update_dependencies", "read_check_output"} {
		assert.True(t, slices.ContainsFunc(tools.Tools, func(ti *genmcphighbay.ToolInfo) bool {
			return ti.Name == want
		}), want)
	}

	evalDir := filepath.Join(workspace, "eval")
	var failEvalTest atomic.Bool
	daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
		srcMount, modMount := findMount(t, c, workspace), findMount(t, c, containerModCache)
		if expectedModuleCache != "" {
			assert.Equal(t, expectedModuleCache, modMount.Source)
		} else {
			assert.Equal(t, filepath.Join(filepath.Dir(srcMount.Source), "modcache"), modMount.Source)
		}
		work := findMount(t, c, containerWork).Source
		assert.Equal(t, filepath.Dir(srcMount.Source), filepath.Dir(work))
		assert.Subset(t, c.Env, []string{"HOME=/work", "TMPDIR=/work/tmp", "GOCACHE=/work/gocache", "GOPATH=/work/gopath"})
		assert.False(t, findMount(t, c, containerWork).ReadOnly)
		assert.NoFileExists(t, filepath.Join(srcMount.Source, ".git", "HEAD"))
		assert.NoFileExists(t, filepath.Join(srcMount.Source, ".highbay", "cards", "c1.json"))
		assert.FileExists(t, filepath.Join(srcMount.Source, "reports", "public-fixture.json"))
		assert.FileExists(t, filepath.Join(srcMount.Source, "shop_test.go"))

		isEval := c.WorkDir == evalDir
		marker := filepath.Base(c.WorkDir) + "-cached"
		switch {
		case slices.Equal(c.Args, []string{"mod", "tidy"}):
			if !isEval {
				assert.NoError(t, os.WriteFile(filepath.Join(srcMount.Source, "go.sum"), []byte("repaired-sum\n"), 0o644))
			}
			return "tidied\n", "", 0
		case len(c.Args) > 0 && c.Args[0] == "list":
			assert.Equal(t, "bridge", c.Network)
			assert.True(t, srcMount.ReadOnly)
			assert.False(t, modMount.ReadOnly)
			assert.Subset(t, c.Env, []string{"GOPROXY=https://proxy.example.test", "GOPRIVATE=example.com/*"})
			if !isEval {
				sum, rerr := os.ReadFile(filepath.Join(srcMount.Source, "go.sum"))
				if rerr != nil || string(sum) != "repaired-sum\n" {
					// Go reports errors before printing dependency package names.
					// Only an explicitly empty template prevents those names from
					// pushing the error outside the bounded failure message.
					stdout := ""
					if !slices.Contains(c.Args, `-f={{""}}`) {
						stdout = strings.Repeat("example.com/shop/dependency\n", 1500)
					}
					return stdout, "go: updates to go.mod needed; to update it:\n\tgo mod tidy\n", 1
				}
			}
			assert.NoError(t, os.WriteFile(filepath.Join(modMount.Source, marker), []byte("ok"), 0o600))
			return "prepared " + marker + "\n", "", 0
		default:
			assert.Equal(t, []string{"test", "./..."}, c.Args)
			assert.Equal(t, "none", c.Network)
			assert.False(t, srcMount.ReadOnly)
			assert.True(t, modMount.ReadOnly)
			assert.Contains(t, c.Env, "GOPROXY=off")
			assert.NotContains(t, c.Env, "GOPRIVATE=example.com/*")
			assert.FileExists(t, filepath.Join(modMount.Source, marker))
			assert.NoError(t, os.WriteFile(filepath.Join(srcMount.Source, "test_scratch.tmp"), []byte("ephemeral"), 0o600))
			assert.NoError(t, os.WriteFile(filepath.Join(srcMount.Source, "reports", "public-fixture.json"), []byte("mutated\n"), 0o600))
			if isEval && failEvalTest.Load() {
				return strings.Repeat("log-✓-", 3200) + strings.Repeat("\xff", 6000) + "\n--- FAIL: TestEvalScenario (0.01s)\n", "", 1
			}
			return "ok  \t" + c.WorkDir + "\n", "", 0
		}
	})

	// 1. Passing check_project over MCP pulls CheckImage once, runs Phase 1 ->
	// Phase 2 per module, leaves live files untouched, and cleans scratch.
	res := callMCPTool(t, clients.mcpClient, "check_project", `{}`, genhighbay.DecodeCheckProjectResult)
	assert.True(t, res.Passed)
	assert.Equal(t, "tested 2 module(s): ., eval. All passed.", res.Message)
	assert.Equal(t, int64(1), daemon.pulls.Load())
	assert.GreaterOrEqual(t, daemon.contextInspects.Load(), int64(1))
	created := daemon.createdContainers()
	require.Len(t, created, 4)
	for i, wantDir := range []string{workspace, workspace, evalDir, evalDir} {
		assert.Equal(t, wantDir, created[i].WorkDir)
		assert.True(t, created[i].Removed)
	}
	assert.Equal(t, []string{"list", "-deps", "-test", `-f={{""}}`, "./..."}, created[0].Args)
	assert.Equal(t, []string{"test", "./..."}, created[1].Args)
	assert.NoFileExists(t, filepath.Join(workspace, "test_scratch.tmp"))
	assertFileContent(t, filepath.Join(workspace, "reports", "public-fixture.json"), "public fixture\n")
	assert.ElementsMatch(t, []string{outputFile, outputMetaFile, ownerFile}, dirEntryNames(t, filepath.Join(clients.cp.checkWorkDir, res.OpID)))
	assert.FileExists(t, filepath.Join(moduleCache, filepath.Base(workspace)+"-cached"))
	assert.NotEqual(t, findMount(t, &created[0], containerWork).Source, findMount(t, &created[1], containerWork).Source)

	// 2. Stale go.sum fails Phase 1 on ".", skips Phase 2 for "." while still
	// testing "eval", then repairs via update_dependencies on the same server.
	daemon.resetContainers()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.sum"), []byte("stale-sum\n"), 0o644))
	resDownloadFail, err := clients.httpClient.CheckProject(ctx)
	require.NoError(t, err)
	assert.False(t, resDownloadFail.Passed)
	assert.Contains(t, resDownloadFail.Message, "Dependencies for . could not be prepared (go list exited 1)")
	assert.Contains(t, resDownloadFail.Message, "call update_dependencies and check again")
	assert.Contains(t, resDownloadFail.Message, "go: updates to go.mod needed; to update it:")
	assert.NotContains(t, resDownloadFail.Message, "example.com/shop/dependency")
	assert.Equal(t, int64(1), daemon.pulls.Load())
	assert.GreaterOrEqual(t, daemon.contextInspects.Load(), int64(2))
	created = daemon.createdContainers()
	require.Len(t, created, 3)
	assert.Equal(t, []string{workspace, evalDir, evalDir}, []string{created[0].WorkDir, created[1].WorkDir, created[2].WorkDir})

	repairRes := callMCPTool(t, clients.mcpClient, "update_dependencies", `{}`, genhighbay.DecodeUpdateDependenciesResult)
	assert.Equal(t, []string{"go.sum"}, repairRes.FilesWritten)
	assert.True(t, callMCPTool(t, clients.mcpClient, "check_project", `{}`, genhighbay.DecodeCheckProjectResult).Passed)
	assertFileContent(t, filepath.Join(project, "go.sum"), "repaired-sum\n")
	assertFileContent(t, filepath.Join(project, "reports", "public-fixture.json"), "public fixture\n")

	// 3. Multi-page UTF-8 and invalid-byte failure output stays <= 16 KiB per
	// decoded MCP Message and read_check_output page without splitting valid runes.
	daemon.resetContainers()
	failEvalTest.Store(true)
	resTestFail := callMCPTool(t, clients.mcpClient, "check_project", `{}`, genhighbay.DecodeCheckProjectResult)
	assert.False(t, resTestFail.Passed)
	assert.LessOrEqual(t, len(resTestFail.Message), maxCheckMessageBytes)
	assert.True(t, utf8.ValidString(resTestFail.Message))
	assert.Contains(t, resTestFail.Message, "eval failed: go test exited 1.")
	assert.Contains(t, resTestFail.Message, "\uFFFD")
	assert.Contains(t, resTestFail.Message, "--- FAIL: TestEvalScenario")

	var combined strings.Builder
	pages := 0
	for cursor := (*string)(nil); ; {
		args, err := genhighbay.EncodeReadCheckOutputPayload(&genhighbay.ReadCheckOutputPayload{OpID: resTestFail.OpID, Cursor: cursor})
		require.NoError(t, err)
		page := callMCPTool(t, clients.mcpClient, "read_check_output", string(args), genhighbay.DecodeCheckOutputPage)
		assert.LessOrEqual(t, len(page.Text), maxCheckMessageBytes)
		assert.True(t, utf8.ValidString(page.Text))
		combined.WriteString(page.Text)
		assert.Zero(t, page.DroppedBytes)
		pages++
		if page.NextCursor == nil {
			assert.Equal(t, int64(combined.Len()-2*6000), page.CapturedBytes)
			break
		}
		require.NotEqual(t, cursor, page.NextCursor)
		cursor = page.NextCursor
	}
	assert.GreaterOrEqual(t, pages, 3)
	assert.Contains(t, combined.String(), "==> modules: ., eval")
	assert.Contains(t, combined.String(), strings.Repeat("log-✓-", 3200))
	assert.Contains(t, combined.String(), "--- FAIL: TestEvalScenario (0.01s)")

	httpPage, err := clients.httpClient.ReadCheckOutput(ctx, &genhighbay.ReadCheckOutputPayload{OpID: resTestFail.OpID})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(httpPage.Text), maxCheckMessageBytes)
	assert.NotEmpty(t, httpPage.Text)

	// Retained dependency inputs survive Close and are selected again after
	// restart; the same persisted build and builder assignment remain usable.
	require.NoError(t, clients.cp.Close())
	assert.DirExists(t, moduleCache)
	reopened, err := New(Options{
		Team:     testTeamBinding(),
		CardsDir: clients.cp.cards.dir, WorkspaceDir: project, CheckModuleCacheDir: moduleCache,
		Checker: clients.cp.checker, CheckHelper: testCheckHelper(t), CheckTimeout: 30 * time.Second,
		GoProxyEnv: clients.cp.goProxyEnv, Verification: verifierRuns(clients.cp.verifier),
		BuildCacheDir: clients.cp.buildCacheDir,
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, reopened.Close()) })
	failEvalTest.Store(false)
	restarted, err := reopened.CheckProject(WithPrincipal(ctx, Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser}))
	require.NoError(t, err)
	assert.True(t, restarted.Passed)
	assert.DirExists(t, moduleCache)
	require.NoError(t, reopened.Close())
	failEvalTest.Store(true)
	expectedModuleCache = ""

	// 4. On a server that runs written tests (WrittenTestRuns), check_project
	// leaves the starter test module eval out and says so in its result and
	// captured output, so check_project -> update_dependencies -> check_project
	// repairs "." without entering eval (even while failEvalTest is true).
	writtenClients := newCheckTestServer(t, Options{BuildCacheDir: t.TempDir(),
		WorkspaceDir:    project,
		RunsDir:         realTempDir(t),
		WrittenTestRuns: &WrittenTestRuns{ReportDir: realTempDir(t), Timeout: time.Minute},
		GoProxyEnv:      []string{"GOPROXY=https://proxy.example.test", "GOPRIVATE=example.com/*"},
	})
	workspace = writtenClients.workingFolder
	evalDir = filepath.Join(workspace, "eval")
	daemon.resetContainers()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.sum"), []byte("stale-sum\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "eval", "go.sum"), []byte("broken-eval-sum\n"), 0o644))

	writtenFail := callMCPTool(t, writtenClients.mcpClient, "check_project", `{}`, genhighbay.DecodeCheckProjectResult)
	assert.False(t, writtenFail.Passed)
	assert.Contains(t, writtenFail.Message, "tested 1 module(s): .. Left test module eval out (tests belong to the test writer).")
	assert.Contains(t, writtenFail.Message, "Dependencies for . could not be prepared (go list exited 1)")
	assert.Contains(t, writtenFail.Message, "call update_dependencies and check again")
	created = daemon.createdContainers()
	require.Len(t, created, 1, "eval is not entered during check_project")
	assert.Equal(t, workspace, created[0].WorkDir)

	writtenRepair := callMCPTool(t, writtenClients.mcpClient, "update_dependencies", `{}`, genhighbay.DecodeUpdateDependenciesResult)
	assert.Equal(t, []string{"go.sum"}, writtenRepair.FilesWritten)
	assert.Equal(t, "Wrote 1 file(s): go.sum. Left test module eval out (tests belong to the test writer).", writtenRepair.Message)
	assertFileContent(t, filepath.Join(workspace, "eval", "go.sum"), "broken-eval-sum\n")

	daemon.resetContainers()
	writtenPass := callMCPTool(t, writtenClients.mcpClient, "check_project", `{}`, genhighbay.DecodeCheckProjectResult)
	assert.True(t, writtenPass.Passed)
	assert.Equal(t, "tested 1 module(s): .. Left test module eval out (tests belong to the test writer). All passed.", writtenPass.Message)
	created = daemon.createdContainers()
	require.Len(t, created, 2, "only the root module runs list and test")
	assert.Equal(t, []string{workspace, workspace}, []string{created[0].WorkDir, created[1].WorkDir})
	writtenOut, err := writtenClients.httpClient.ReadCheckOutput(ctx, &genhighbay.ReadCheckOutputPayload{OpID: writtenPass.OpID})
	require.NoError(t, err)
	assert.Contains(t, writtenOut.Text, "note: Left test module eval out (tests belong to the test writer)\n")
}

// TestUpdateDependenciesLifecycle verifies that update_dependencies runs
// go mod tidy with network and writable source mounts, publishes only changed
// manifests of modules whose tidy succeeded without touching unchanged or
// failed modules' files, and when go.work is present in an enclosing grant,
// also runs go list -deps -test ./... and publishes go.work.sum.
func TestUpdateDependenciesLifecycle(t *testing.T) {
	ctx := t.Context()
	installFakeDocker(t)
	daemon := newFakeDockerDaemon(t)
	t.Setenv("DOCKER_HOST", daemon.hostURI)

	t.Run("multi_module_without_gowork_publishes_changed_passing_modules", func(t *testing.T) {
		workspace := newShopModule(t, map[string]string{
			"go.sum":        "old-root-sum\n",
			"eval/go.mod":   "module example.com/shop/eval\n\ngo 1.24\n",
			"broken/go.mod": "module example.com/shop/broken\n\ngo 1.24\n",
			"broken/go.sum": "keep-broken-sum\n",
		})
		clients := newCheckTestServer(t, Options{BuildCacheDir: t.TempDir(),
			WorkspaceDir: workspace,
			Verification: &VerificationConfig{RunsDir: realTempDir(t)},
			GoProxyEnv:   []string{"GOPROXY=https://proxy.example.test"},
		})
		project := workspace
		workspace = clients.workingFolder
		daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
			srcMount := findMount(t, c, workspace)
			assert.Equal(t, "bridge", c.Network)
			assert.False(t, srcMount.ReadOnly)
			assert.False(t, findMount(t, c, containerModCache).ReadOnly)
			assert.Contains(t, c.Env, "GOPROXY=https://proxy.example.test")
			assert.Equal(t, []string{"mod", "tidy"}, c.Args)
			rel, err := filepath.Rel(workspace, c.WorkDir)
			assert.NoError(t, err)
			stagedSum := filepath.Join(srcMount.Source, rel, "go.sum")
			switch filepath.ToSlash(rel) {
			case ".":
				assert.NoError(t, os.WriteFile(stagedSum, []byte("new-root-sum\n"), 0o644))
				return "tidied root\n", "", 0
			case "eval":
				assert.NoError(t, os.WriteFile(stagedSum, []byte("created-eval-sum\n"), 0o644))
				return "tidied eval\n", "", 0
			default:
				assert.NoError(t, os.WriteFile(stagedSum, []byte("corrupted-by-failed-tidy\n"), 0o644))
				return "", "example.com/shop/broken: missing package\n", 1
			}
		})

		res := callMCPTool(t, clients.mcpClient, "update_dependencies", `{}`, genhighbay.DecodeUpdateDependenciesResult)
		assert.Equal(t, []string{"eval/go.sum", "go.sum"}, res.FilesWritten)
		assert.Contains(t, res.Message, "Wrote 2 file(s): eval/go.sum, go.sum.")
		assert.Contains(t, res.Message, "go tidy failed for broken (exit 1); its files were not written.")
		assertFileContent(t, filepath.Join(workspace, "go.sum"), "new-root-sum\n")
		assertFileContent(t, filepath.Join(workspace, "eval", "go.sum"), "created-eval-sum\n")
		assertFileContent(t, filepath.Join(workspace, "broken", "go.sum"), "keep-broken-sum\n")
		assertFileContent(t, filepath.Join(project, "go.sum"), "old-root-sum\n")
		assert.NoFileExists(t, filepath.Join(project, "eval", "go.sum"))
	})

	t.Run("written_test_verification_leaves_starter_test_module_untouched", func(t *testing.T) {
		workspace := newShopModule(t, map[string]string{
			"go.sum":        "old-root-sum\n",
			"eval/go.mod":   "module example.com/shop/eval\n\ngo 1.24\n",
			"eval/go.sum":   "keep-eval-sum\n",
			"broken/go.mod": "module example.com/shop/broken\n\ngo 1.24\n",
			"broken/go.sum": "keep-broken-sum\n",
		})
		clients := newCheckTestServer(t, Options{BuildCacheDir: t.TempDir(),
			WorkspaceDir:    workspace,
			RunsDir:         realTempDir(t),
			WrittenTestRuns: &WrittenTestRuns{ReportDir: realTempDir(t), Timeout: time.Minute},
			GoProxyEnv:      []string{"GOPROXY=https://proxy.example.test"},
		})
		project := workspace
		workspace = clients.workingFolder
		daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
			srcMount := findMount(t, c, workspace)
			rel, err := filepath.Rel(workspace, c.WorkDir)
			assert.NoError(t, err)
			assert.NotEqual(t, "eval", filepath.ToSlash(rel), "go mod tidy must not run in eval")
			stagedSum := filepath.Join(srcMount.Source, rel, "go.sum")
			switch filepath.ToSlash(rel) {
			case ".":
				assert.NoError(t, os.WriteFile(stagedSum, []byte("new-root-sum\n"), 0o644))
				// Even if a container mutated eval/go.sum in the copy,
				// publishManifests must leave eval/ out.
				assert.NoError(t, os.WriteFile(filepath.Join(srcMount.Source, "eval", "go.sum"), []byte("mutated-eval-sum\n"), 0o644))
				return "tidied root\n", "", 0
			default:
				assert.NoError(t, os.WriteFile(stagedSum, []byte("corrupted-by-failed-tidy\n"), 0o644))
				return "", "example.com/shop/broken: missing package\n", 1
			}
		})

		res := callMCPTool(t, clients.mcpClient, "update_dependencies", `{}`, genhighbay.DecodeUpdateDependenciesResult)
		assert.Equal(t, []string{"go.sum"}, res.FilesWritten)
		assert.Contains(t, res.Message, "Wrote 1 file(s): go.sum. Left test module eval out (tests belong to the test writer).")
		assert.Contains(t, res.Message, "go tidy failed for broken (exit 1); its files were not written.")
		assertFileContent(t, filepath.Join(workspace, "go.sum"), "new-root-sum\n")
		assertFileContent(t, filepath.Join(workspace, "eval", "go.mod"), "module example.com/shop/eval\n\ngo 1.24\n")
		assertFileContent(t, filepath.Join(workspace, "eval", "go.sum"), "keep-eval-sum\n")
		assertFileContent(t, filepath.Join(workspace, "broken", "go.sum"), "keep-broken-sum\n")
		assertFileContent(t, filepath.Join(project, "go.sum"), "old-root-sum\n")
		assertFileContent(t, filepath.Join(project, "eval", "go.sum"), "keep-eval-sum\n")
	})

	t.Run("enclosing_grant_with_gowork_runs_list_and_publishes_gowork_sum", func(t *testing.T) {
		grant := realTempDir(t)
		runGitForTest(t, grant, "init", "-q")
		workspace := filepath.Join(grant, "services", "shop")
		writeTree(t, grant, map[string]string{
			"go.work":               "go 1.24\n\nuse ./services/shop\n",
			"services/shop/go.mod":  "module example.com/shop\n\ngo 1.24\n",
			"services/shop/go.sum":  "old-sum\n",
			"services/shop/main.go": "package main\n",
		})
		clients := newCheckTestServer(t, Options{
			WorkspaceDir: workspace,
			GoProxyEnv:   []string{"GOPROXY=https://proxy.example.test"},
		})
		project, projectGrant := workspace, grant
		_, _, grant, err := builderCopyFolder(t.Context(), project)
		require.NoError(t, err)
		workspace = clients.workingFolder
		daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
			srcMount := findMount(t, c, grant)
			assert.Equal(t, "bridge", c.Network)
			assert.False(t, srcMount.ReadOnly)
			assert.Equal(t, workspace, c.WorkDir)
			if slices.Equal(c.Args, []string{"mod", "tidy"}) {
				assert.NoError(t, os.WriteFile(filepath.Join(srcMount.Source, "services", "shop", "go.sum"), []byte("tidied-shop-sum\n"), 0o644))
				return "", "", 0
			}
			assert.Equal(t, []string{"list", "-deps", "-test", `-f={{""}}`, "./..."}, c.Args)
			assert.NoError(t, os.WriteFile(filepath.Join(srcMount.Source, "go.work.sum"), []byte("work-sum-from-list\n"), 0o644))
			return "", "", 0
		})

		res, err := clients.httpClient.UpdateDependencies(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"../../go.work.sum", "go.sum"}, res.FilesWritten)
		assert.Contains(t, res.Message, "Wrote 2 file(s): ../../go.work.sum, go.sum.")
		assertFileContent(t, filepath.Join(workspace, "go.sum"), "tidied-shop-sum\n")
		assertFileContent(t, filepath.Join(grant, "go.work.sum"), "work-sum-from-list\n")
		assertFileContent(t, filepath.Join(project, "go.sum"), "old-sum\n")
		assert.NoFileExists(t, filepath.Join(projectGrant, "go.work.sum"))

		// A later workspace preparation failure keeps the reason visible and
		// leaves the manifests unchanged, even with a large package graph.
		daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
			if slices.Equal(c.Args, []string{"mod", "tidy"}) {
				return "", "", 0
			}
			stdout := ""
			if !slices.Contains(c.Args, `-f={{""}}`) {
				stdout = strings.Repeat("example.com/shop/dependency\n", 1500)
			}
			return stdout, "go: example.com/dependency: module proxy unavailable\n", 1
		})
		failed, err := clients.httpClient.UpdateDependencies(ctx)
		require.NoError(t, err)
		assert.Empty(t, failed.FilesWritten)
		assert.Contains(t, failed.Message, "go: example.com/dependency: module proxy unavailable")
		assert.NotContains(t, failed.Message, "example.com/shop/dependency")
		assertFileContent(t, filepath.Join(workspace, "go.sum"), "tidied-shop-sum\n")
		assertFileContent(t, filepath.Join(grant, "go.work.sum"), "work-sum-from-list\n")
	})

	// Direct callers use the same required-array contract as the generated
	// codecs, including when tidy changes nothing or an editor wins a write.
	for _, tc := range []struct {
		name string
		edit bool
	}{
		{name: "direct_no_change"},
		{name: "direct_refused_write", edit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := newShopModule(t, nil)
			clients := newCheckTestServer(t, Options{WorkspaceDir: workspace})
			project := workspace
			workspace = clients.workingFolder
			liveManifest := filepath.Join(workspace, "go.mod")
			before, err := os.ReadFile(liveManifest)
			require.NoError(t, err)
			daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
				if tc.edit {
					copied := findMount(t, c, workspace)
					assert.NoError(t, os.WriteFile(filepath.Join(copied.Source, "go.mod"),
						[]byte(string(before)+"\n// tidied\n"), 0o600))
					assert.NoError(t, os.WriteFile(liveManifest,
						[]byte(string(before)+"\n// editor\n"), 0o600))
				}
				return "tidied\n", "", 0
			})
			result, err := clients.cp.UpdateDependencies(WithPrincipal(ctx, Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser}))
			require.NoError(t, err)
			assert.NotNil(t, result.FilesWritten)
			assert.Empty(t, result.FilesWritten)
			assert.NoError(t, genhighbay.ValidateUpdateDependenciesResultValue(result))
			encoded, err := genhighbay.EncodeUpdateDependenciesResult(result)
			require.NoError(t, err)
			assert.Contains(t, string(encoded), `"files_written":[]`)
			if tc.edit {
				assert.Contains(t, result.Message, "was not written")
				assertFileContent(t, liveManifest, string(before)+"\n// editor\n")
			} else {
				assertFileContent(t, liveManifest, string(before))
			}
			assertFileContent(t, filepath.Join(project, "go.mod"), string(before))
		})
	}
}

// TestCheckOperationsConcurrentIsolation verifies same-builder exclusion,
// distinct operation snapshots/cursors, and concurrent independent servers.
func TestCheckOperationsConcurrentIsolation(t *testing.T) {
	installFakeDocker(t)
	daemon := newFakeDockerDaemon(t)
	t.Setenv("DOCKER_HOST", daemon.hostURI)
	t.Run("same_builder_waits_for_its_active_helper", func(t *testing.T) {
		testConcurrentCheckAndUpdate(t, daemon)
	})
	t.Run("independent_servers_keep_their_own_copies", func(t *testing.T) {
		testIndependentServerUpdates(t, daemon)
	})
}

// TestCheckOperationsRecoveryAndRetainedOwner verifies retained-owner scratch
// preservation on container-removal failure, exact recorded-endpoint retry on
// Close and next startup, unavailable refusal after Close, non-interference
// across distinct CardsDir servers on the same workspace, and startup cleanup
// of ownerless pre-container scratch.
func TestCheckOperationsRecoveryAndRetainedOwner(t *testing.T) {
	authCtx := WithPrincipal(t.Context(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser})
	dockerBin := installFakeDocker(t)

	t.Run("failed_close_isolates_peer_server_and_recovers_on_restart", func(t *testing.T) {
		recordedDaemon, otherDaemon := newFakeDockerDaemon(t), newFakeDockerDaemon(t)
		workspace := newShopModule(t, nil)
		cardsA, cardsB := filepath.Join(realTempDir(t), "cards-a"), filepath.Join(realTempDir(t), "cards-b")
		moduleCache := newCheckModuleCache(t)
		assert.NoError(t, os.WriteFile(filepath.Join(moduleCache, "retained"), []byte("input"), 0o600))
		cachedModule := filepath.Join(moduleCache, "example.com", "module@v1.0.0")
		require.NoError(t, os.MkdirAll(cachedModule, 0o700))
		require.NoError(t, os.Chmod(cachedModule, 0o555))
		t.Cleanup(func() { assert.NoError(t, os.Chmod(cachedModule, 0o700)) })

		t.Setenv("DOCKER_HOST", recordedDaemon.hostURI)
		pA := runPinned(t, workspace, cardsA, recordedDaemon, moduleCache)
		opID := filepath.Base(pA.opDir)
		for _, sub := range []string{"src", "work-0", "docker-config"} {
			assert.DirExists(t, filepath.Join(pA.opDir, sub))
		}
		assert.Equal(t, checkOwner{Version: ownerVersion, Docker: dockerBin, Host: recordedDaemon.hostURI, Containers: []ownedContainer{{Name: pA.name, State: stateCreationAnswered}}}, readOwnerRecord(t, pA.opDir))

		// A separate server on cardsB sharing workspace never retries pA.cp's container.
		cpB := openCheckCP(t, cardsB, workspace)
		readCheckBuilderFixture(t, cpB)
		resB, err := cpB.CheckProject(authCtx)
		require.NoError(t, err)
		assert.True(t, resB.Passed)
		require.NoError(t, cpB.Close())
		assert.Equal(t, 1, recordedDaemon.removeCount(pA.name))

		// Switch DOCKER_HOST to otherDaemon while recordedDaemon still blocks rm:
		// Close retries recordedDaemon, returns the cleanup error, keeps scratch,
		// and refuses subsequent check_project and update_dependencies calls.
		t.Setenv("DOCKER_HOST", otherDaemon.hostURI)
		closeErr := pA.cp.Close()
		require.Error(t, closeErr)
		assert.Contains(t, closeErr.Error(), pA.name)
		assert.Zero(t, otherDaemon.removes.Load())
		assert.Equal(t, 2, recordedDaemon.removeCount(pA.name))
		assert.DirExists(t, filepath.Join(pA.opDir, "src"))
		assertFileContent(t, filepath.Join(moduleCache, "retained"), "input")
		cacheInfo, err := os.Stat(cachedModule)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o555), cacheInfo.Mode().Perm(), "cleanup never chmods retained inputs")

		_, err = pA.cp.CheckProject(authCtx)
		assertServiceError(t, err, "unavailable")
		_, err = pA.cp.UpdateDependencies(authCtx)
		assertServiceError(t, err, "unavailable")
		assert.Equal(t, []string{opID}, dirEntryNames(t, filepath.Join(cardsA, "check-work")))

		// Unblock recordedDaemon and start cpA2 with DOCKER_HOST still on
		// otherDaemon: startup recovery contacts recordedDaemon, cleans scratch,
		// clears owner.Containers, and preserves ReadCheckOutput.
		recordedDaemon.setFailRemove(pA.name, false)
		cpA2 := openCheckCP(t, cardsA, workspace, moduleCache)
		defer func() { assert.NoError(t, cpA2.Close()) }()
		assert.Zero(t, otherDaemon.removes.Load())
		assert.Equal(t, 3, recordedDaemon.removeCount(pA.name))
		assert.ElementsMatch(t, []string{outputFile, outputMetaFile, ownerFile}, dirEntryNames(t, pA.opDir))
		assert.Empty(t, readOwnerRecord(t, pA.opDir).Containers)
		assertFileContent(t, filepath.Join(moduleCache, "retained"), "input")

		page, err := cpA2.ReadCheckOutput(authCtx, &genhighbay.ReadCheckOutputPayload{OpID: opID})
		require.NoError(t, err)
		assert.Contains(t, page.Text, "pinned check output")
	})

	t.Run("close_cleans_scratch_via_recorded_endpoint", func(t *testing.T) {
		recordedDaemon, otherDaemon := newFakeDockerDaemon(t), newFakeDockerDaemon(t)
		t.Setenv("DOCKER_HOST", recordedDaemon.hostURI)
		p := runPinned(t, newShopModule(t, nil), filepath.Join(realTempDir(t), "cards"), recordedDaemon)
		recordedDaemon.setFailRemove(p.name, false)
		t.Setenv("DOCKER_HOST", otherDaemon.hostURI)
		require.NoError(t, p.cp.Close())
		assert.Zero(t, otherDaemon.removes.Load())
		assert.ElementsMatch(t, []string{outputFile, outputMetaFile, ownerFile}, dirEntryNames(t, p.opDir))
		assert.Empty(t, readOwnerRecord(t, p.opDir).Containers)
	})

	t.Run("ownerless_pre_container_operation_cleans_scratch_and_keeps_output", func(t *testing.T) {
		daemon := newFakeDockerDaemon(t)
		t.Setenv("DOCKER_HOST", daemon.hostURI)
		cardsDir := filepath.Join(realTempDir(t), "cards")
		opID := strings.Repeat("cd", 16)
		opDir := filepath.Join(cardsDir, "check-work", opID)
		const savedText = "note: socket skipped before crash\n"
		writeTree(t, opDir, map[string]string{
			"src/main.go":                          "package main\n",
			"modcache/example.com/mod@v1.0.0/m.go": "package mod\n",
			outputFile:                             savedText,
			outputMetaFile:                         fmt.Sprintf(`{"captured":%d,"dropped":0}`, len(savedText)),
		})
		require.NoError(t, os.Chmod(filepath.Join(opDir, "modcache/example.com/mod@v1.0.0"), 0o500))

		cp := openCheckCP(t, cardsDir, newShopModule(t, nil))
		defer func() { assert.NoError(t, cp.Close()) }()
		assert.Zero(t, daemon.removes.Load())
		assert.ElementsMatch(t, []string{outputFile, outputMetaFile}, dirEntryNames(t, opDir))

		page, err := cp.ReadCheckOutput(authCtx, &genhighbay.ReadCheckOutputPayload{OpID: opID})
		require.NoError(t, err)
		assert.Equal(t, savedText, page.Text)
	})
}

// testConcurrentCheckAndUpdate refuses a second operation on the same builder
// until its helper has finished. Subsequent operations use distinct snapshots,
// preserve concurrent editor changes, and keep output cursors operation-bound.
func testConcurrentCheckAndUpdate(t *testing.T, daemon *fakeDockerDaemon) {
	t.Helper()
	project := newShopModule(t, map[string]string{"go.sum": "initial-sum\n"})
	clients := newCheckTestServer(t, Options{WorkspaceDir: project})
	workspace, httpClient := clients.workingFolder, clients.httpClient
	ctx, cancel := context.WithTimeout(t.Context(), lifecycleWaitTimeout)
	entered, release := make(chan struct{}), make(chan struct{})
	checkDone := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	var first sync.Once
	var checkRes *genhighbay.CheckProjectResult
	var checkErr error
	var wg sync.WaitGroup
	t.Cleanup(func() {
		cancel()
		releaseOnce()
		wg.Wait()
	})
	daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
		srcMount := findMount(t, c, workspace)
		first.Do(func() {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
			}
		})
		if slices.Equal(c.Args, []string{"mod", "tidy"}) {
			assert.NoError(t, os.WriteFile(filepath.Join(srcMount.Source, "go.sum"), []byte("tidy-staged-sum\n"), 0o644))
			assert.NoError(t, os.WriteFile(filepath.Join(workspace, "go.sum"), []byte("concurrent-editor-edit\n"), 0o644))
			return "tidy output " + strings.Repeat("x", maxCheckMessageBytes) + "\n", "", 0
		}
		assertFileContent(t, filepath.Join(srcMount.Source, "go.sum"), "initial-sum\n")
		return "check step " + strings.Join(c.Args, " ") + "\n", "", 0
	})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(checkDone)
		checkRes, checkErr = httpClient.CheckProject(ctx)
	}()
	select {
	case <-entered:
	case <-checkDone:
		require.FailNowf(t, "check_project exited before entering its copy", "res=%+v err=%v", checkRes, checkErr)
	case <-ctx.Done():
		require.FailNow(t, "timed out waiting for the check helper")
	}
	_, err := httpClient.UpdateDependencies(ctx)
	assertServiceError(t, err, "working_copy_busy")
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "go.sum"), []byte("later-builder-edit\n"), 0o644))
	releaseOnce()
	select {
	case <-checkDone:
	case <-ctx.Done():
		require.FailNow(t, "check helper did not finish")
	}
	require.NoError(t, checkErr)
	assert.True(t, checkRes.Passed)
	updateRes, err := httpClient.UpdateDependencies(ctx)
	require.NoError(t, err)
	assert.Empty(t, updateRes.FilesWritten)
	assert.Contains(t, updateRes.Message, "go.sum was not written: manifest conflict: go.sum changed during update_dependencies; call again")
	assertFileContent(t, filepath.Join(workspace, "go.sum"), "concurrent-editor-edit\n")
	assertFileContent(t, filepath.Join(project, "go.sum"), "initial-sum\n")
	created := daemon.createdContainers()
	require.Len(t, created, 3)
	assert.NotEqual(t, created[0].Label, created[2].Label)
	assert.NotEqual(t, created[0].DockerConfig, created[2].DockerConfig)
	for _, target := range []string{workspace, containerModCache, containerWork} {
		assert.NotEqual(t, findMount(t, &created[0], target).Source, findMount(t, &created[2], target).Source)
	}
	checkPage, err := httpClient.ReadCheckOutput(ctx, &genhighbay.ReadCheckOutputPayload{OpID: checkRes.OpID})
	require.NoError(t, err)
	assert.Contains(t, checkPage.Text, "check step test ./...")
	updatePage, err := httpClient.ReadCheckOutput(ctx, &genhighbay.ReadCheckOutputPayload{OpID: updateRes.OpID})
	require.NoError(t, err)
	assert.Contains(t, updatePage.Text, "tidy output")
	require.NotNil(t, updatePage.NextCursor)
	_, err = httpClient.ReadCheckOutput(ctx, &genhighbay.ReadCheckOutputPayload{OpID: checkRes.OpID, Cursor: updatePage.NextCursor})
	assertServiceError(t, err, "unprocessable")
}

// testIndependentServerUpdates keeps independent servers' copy reservations,
// scratch, containers and writes separate while their helpers run concurrently.
// Each server has its own project and numbered assignment: starting a second
// build on one physical copy would move the first assignment's branch.
func testIndependentServerUpdates(t *testing.T, daemon *fakeDockerDaemon) {
	t.Helper()
	projectA := newShopModule(t, map[string]string{"go.sum": "initial-sum\n"})
	projectB := newShopModule(t, map[string]string{"go.sum": "initial-sum\n"})
	cacheA, cacheB := newCheckModuleCache(t), newCheckModuleCache(t)
	srvA := newCheckTestServer(t, Options{WorkspaceDir: projectA, CheckModuleCacheDir: cacheA})
	srvB := newCheckTestServer(t, Options{WorkspaceDir: projectB, CheckModuleCacheDir: cacheB})
	ctx, cancel := context.WithTimeout(t.Context(), lifecycleWaitTimeout)
	bothTidying := make(chan struct{})
	srvADone, srvBDone := make(chan struct{}), make(chan struct{})
	var (
		tidyMu      sync.Mutex
		tidyEntered int
		resA, resB  *genhighbay.UpdateDependenciesResult
		errA, errB  error
		wg          sync.WaitGroup
	)
	t.Cleanup(func() {
		cancel()
		wg.Wait()
	})
	daemon.setOnStart(func(c *recordedContainer) (string, string, int) {
		tidyMu.Lock()
		tidyEntered++
		if tidyEntered == 2 {
			close(bothTidying)
		}
		tidyMu.Unlock()
		select {
		case <-bothTidying:
		case <-srvADone:
			return "", "srvA exited before tidy barrier\n", 1
		case <-srvBDone:
			return "", "srvB exited before tidy barrier\n", 1
		case <-ctx.Done():
			return "", "context canceled waiting for tidy barrier\n", 1
		}
		value := "sum-from-a\n"
		cache := cacheA
		if c.WorkDir == srvB.workingFolder {
			value = "sum-from-b\n"
			cache = cacheB
		}
		assert.Equal(t, cache, findMount(t, c, containerModCache).Source)
		mount := findMount(t, c, c.WorkDir)
		assert.NoError(t, os.WriteFile(filepath.Join(mount.Source, "go.sum"), []byte(value), 0o644))
		return "tidied own copy\n", "", 0
	})
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer close(srvADone)
		resA, errA = srvA.httpClient.UpdateDependencies(ctx)
	}()
	go func() {
		defer wg.Done()
		defer close(srvBDone)
		resB, errB = srvB.httpClient.UpdateDependencies(ctx)
	}()
	for _, done := range []<-chan struct{}{srvADone, srvBDone} {
		select {
		case <-done:
		case <-ctx.Done():
			require.FailNow(t, "independent update helpers did not finish")
		}
	}
	require.NoError(t, errA)
	require.NoError(t, errB)
	assert.Equal(t, []string{"go.sum"}, resA.FilesWritten)
	assert.Equal(t, []string{"go.sum"}, resB.FilesWritten)
	assert.NotEqual(t, resA.OpID, resB.OpID)
	assertFileContent(t, filepath.Join(srvA.workingFolder, "go.sum"), "sum-from-a\n")
	assertFileContent(t, filepath.Join(srvB.workingFolder, "go.sum"), "sum-from-b\n")
	assertFileContent(t, filepath.Join(projectA, "go.sum"), "initial-sum\n")
	assertFileContent(t, filepath.Join(projectB, "go.sum"), "initial-sum\n")
}

// runPinned starts a ControlPlane at cards and runs one CheckProject call whose
// first container fails docker rm -f, returning the retained pinnedCheck.
func runPinned(t *testing.T, ws, cards string, d *fakeDockerDaemon, moduleCache ...string) pinnedCheck {
	t.Helper()
	cp := openCheckCP(t, cards, ws, moduleCache...)
	readCheckBuilderFixture(t, cp)
	var pinned atomic.Value
	d.setOnStart(func(c *recordedContainer) (string, string, int) {
		if pinned.CompareAndSwap(nil, c.Name) {
			d.setFailRemove(c.Name, true)
		}
		return "pinned check output\n", "", 0
	})
	_, err := cp.CheckProject(WithPrincipal(t.Context(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser}))
	require.Error(t, err)
	name, ok := pinned.Load().(string)
	require.True(t, ok)
	assert.Contains(t, err.Error(), "because container "+name+" may still exist; remove it with docker rm -f "+name)
	names := dirEntryNames(t, filepath.Join(cards, "check-work"))
	require.Len(t, names, 1)
	return pinnedCheck{cp: cp, name: name, opDir: filepath.Join(cards, "check-work", names[0])}
}
