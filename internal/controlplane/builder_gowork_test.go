// This file checks builder workspace selection through the candidate owner.
package controlplane

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	gensurlaclient "github.com/goadesign/surla/gen/http/surla/client"
	genmcpclient "github.com/goadesign/surla/gen/jsonrpc/mcp_surla/client"
	genmcpsurla "github.com/goadesign/surla/gen/mcp_surla"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/shquote"
	"github.com/goadesign/surla/verify/candidate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
)

// TestBuilderGoWorkDependencyCommands compares the public assignment and real
// HTTP/MCP dependency refusal with one owning selection for root/nested copies.
func TestBuilderGoWorkDependencyCommands(t *testing.T) {
	for _, prefix := range []string{".", "app"} {
		for _, internal := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/internal=%t", prefix, internal), func(t *testing.T) {
				project, cp, card, assignment := buildGenerationFixture(t, prefix)
				_, _, copyRoot, err := builderCopyFolder(t.Context(), project)
				require.NoError(t, err)
				outer := filepath.Join(project, "go.work")
				const outerBytes = "go 1.27.3\n\nuse .\n// user's untracked workspace\n"
				require.NoError(t, os.WriteFile(outer, []byte(outerBytes), 0o600))
				want := "off"
				if internal {
					moduleRel, err := filepath.Rel(copyRoot, assignment.WorkingFolder)
					require.NoError(t, err)
					writeBuildDesignFixture(t, copyRoot, "go.work", "go 1.27.3\n\nuse ./"+filepath.ToSlash(moduleRel)+"\n")
					runGitForTest(t, copyRoot, "add", "go.work")
					runGitForTest(t, copyRoot, "commit", "--quiet", "-m", "Add the build workspace")
					want = filepath.Join(copyRoot, "go.work")
				}
				read, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder),
					&gensurla.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
				require.NoError(t, err)
				assert.Equal(t, !internal, read.GoworkOff)
				cp.builderCopyTurn <- struct{}{}
				ready, err := cp.builderCopyReady(asCaller("alice@example.com", CallerBuilder), project, "generate_code")
				<-cp.builderCopyTurn
				require.NoError(t, err)
				assert.Equal(t, want, ready.goWork)
				writeBuildDesignFixture(t, assignment.WorkingFolder, "go.mod", "module example.com/shop\n\ngo 1.27.3\n")
				writeBuildDesignFixture(t, assignment.WorkingFolder, "gen/keep.go", "package gen // kept\n")
				srv := httptest.NewServer(NewHTTPHandler(cp, workflowAuthenticator(map[string]Principal{
					"generation-token": {ID: "alice@example.com"},
				})))
				t.Cleanup(srv.Close)
				origin, err := url.Parse(srv.URL)
				require.NoError(t, err)
				doer := generationDoer{client: srv.Client()}
				httpTransport := gensurlaclient.NewClient(origin.Scheme, origin.Host,
					doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
				client := &gensurla.Client{GenerateCodeEndpoint: httpTransport.GenerateCode()}
				_, err = client.GenerateCode(t.Context(), &gensurla.GenerateCodePayload{})
				assertServiceError(t, err, "missing_modules")
				prefix := "GOWORK=off "
				if internal {
					prefix = "GOWORK=" + shquote.Quote(want) + " "
				}
				assert.Contains(t, err.Error(), prefix+"go get 'goa.design/goa/v3@v3.35.0'")
				assert.Contains(t, err.Error(), prefix+"go mod tidy")
				mcpTransport := genmcpclient.NewClient(origin.Scheme, origin.Host,
					doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
				mcpClient := &genmcpsurla.Client{ToolsCallEndpoint: mcpTransport.ToolsCall()}
				call, err := mcpClient.ToolsCall(t.Context(), &genmcpsurla.ToolsCallPayload{
					Name: "generate_code", Arguments: []byte(`{}`),
				})
				require.NoError(t, err)
				complete, ok := call.Outcome.AsComplete()
				require.True(t, ok)
				require.NotNil(t, complete.IsError)
				assert.True(t, *complete.IsError)
				require.NotEmpty(t, complete.Content)
				require.NotNil(t, complete.Content[0].Text)
				assert.Contains(t, *complete.Content[0].Text, prefix+"go mod tidy")
				assertFileContent(t, outer, outerBytes)
				assertFileContent(t, filepath.Join(assignment.WorkingFolder, "gen", "keep.go"), "package gen // kept\n")
			})
		}
	}
}

// TestBuilderGoWorkUsesTheOwnersPathRules retains the assignment's boolean
// shape while excluding an enclosing file, ignoring a directory, and refusing
// a module that resolves outside the source root.
func TestBuilderGoWorkUsesTheOwnersPathRules(t *testing.T) {
	parent := realTempDir(t)
	root := filepath.Join(parent, "copy")
	module := filepath.Join(root, "app")
	require.NoError(t, os.MkdirAll(module, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(parent, "go.work"), []byte("outside\n"), 0o600))
	require.NoError(t, os.Mkdir(filepath.Join(root, "go.work"), 0o700))
	folder, off, err := builderWorkingFolder(module, root)
	require.NoError(t, err)
	assert.Equal(t, module, folder)
	assert.True(t, off, "a directory is not a Go workspace file")
	_, _, err = builderWorkingFolder(parent, root)
	assert.ErrorContains(t, err, "outside source directory")
	_, _, err = builderWorkingFolder("", root)
	assert.EqualError(t, err, "no builder working folder is bound")
}

// TestBuilderGoWorkRefusesOutsideModule preserves the existing typed consumer
// refusal when selection resolves an intermediate link outside the copy.
func TestBuilderGoWorkRefusesOutsideModule(t *testing.T) {
	project, cp, _, assignment := buildGenerationFixture(t, "apps/shop")
	writeBuildDesignFixture(t, project, "design/design.go", "package design // user's source\n")
	writeBuildDesignFixture(t, project, "gen/keep.go", "package gen // user's output\n")
	parent := filepath.Dir(assignment.WorkingFolder)
	require.NoError(t, os.Rename(parent, filepath.Join(t.TempDir(), "saved-apps")))
	require.NoError(t, os.Symlink(filepath.Dir(project), parent))
	srv := httptest.NewServer(NewHTTPHandler(cp, workflowAuthenticator(map[string]Principal{
		"generation-token": {ID: "alice@example.com"},
	})))
	t.Cleanup(srv.Close)
	origin, err := url.Parse(srv.URL)
	require.NoError(t, err)
	doer := generationDoer{client: srv.Client()}
	httpTransport := gensurlaclient.NewClient(origin.Scheme, origin.Host,
		doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	client := &gensurla.Client{GenerateCodeEndpoint: httpTransport.GenerateCode()}
	_, err = client.GenerateCode(t.Context(), &gensurla.GenerateCodePayload{})
	assertServiceError(t, err, "unprocessable")
	assert.ErrorContains(t, err, "outside source directory")
	mcpTransport := genmcpclient.NewClient(origin.Scheme, origin.Host,
		doer, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	mcp := &genmcpsurla.Client{ToolsCallEndpoint: mcpTransport.ToolsCall()}
	call, err := mcp.ToolsCall(t.Context(), &genmcpsurla.ToolsCallPayload{
		Name: "generate_code", Arguments: []byte(`{}`),
	})
	require.NoError(t, err)
	complete, ok := call.Outcome.AsComplete()
	require.True(t, ok)
	require.NotNil(t, complete.IsError)
	assert.True(t, *complete.IsError)
	require.NotEmpty(t, complete.Content)
	require.NotNil(t, complete.Content[0].Text)
	assert.Contains(t, *complete.Content[0].Text, "outside source directory")
	assertFileContent(t, filepath.Join(project, "design", "design.go"), "package design // user's source\n")
	assertFileContent(t, filepath.Join(project, "gen", "keep.go"), "package gen // user's output\n")
	assert.Empty(t, cp.builderCopyTurn)
}

// TestBuilderGoWorkBuildsOnlyWithInternalWorkspace is hosted native proof of
// builder commands and the offline candidate build using the same workspace.
// Local focused checks use -short and execute none of these Go commands.
func TestBuilderGoWorkBuildsOnlyWithInternalWorkspace(t *testing.T) {
	if testing.Short() {
		t.Skip("native workspace builds run in the hosted full profile")
	}
	project, cp, _, assignment := buildGenerationFixture(t, "app")
	_, _, root, err := builderCopyFolder(t.Context(), project)
	require.NoError(t, err)
	working := assignment.WorkingFolder
	writeBuildDesignFixture(t, working, "go.mod", "module example.com/shop\n\ngo 1.27.3\n\nrequire example.invalid/lib v0.0.0\n")
	writeBuildDesignFixture(t, working, "main.go", "package main\nimport \"example.invalid/lib\"\nfunc main() { _ = lib.Value }\n")
	writeBuildDesignFixture(t, root, "lib/go.mod", "module example.invalid/lib\n\ngo 1.27.3\n")
	writeBuildDesignFixture(t, root, "lib/lib.go", "package lib\nconst Value = 42\n")
	writeBuildDesignFixture(t, root, "go.work", "go 1.27.3\n\nuse (\n\t./app\n\t./lib\n)\n")
	outer := filepath.Join(project, "go.work")
	const outerBytes = "go 1.27.3\n\nuse .\n// untouched outer workspace\n"
	require.NoError(t, os.WriteFile(outer, []byte(outerBytes), 0o600))
	runGitForTest(t, root, "add", "go.work", "app/go.mod", "app/main.go", "lib/go.mod", "lib/lib.go")
	runGitForTest(t, root, "commit", "--quiet", "-m", "Use the committed sibling module")
	for _, internal := range []bool{true, false} {
		t.Run(fmt.Sprintf("internal=%t", internal), func(t *testing.T) {
			if !internal {
				runGitForTest(t, root, "rm", "go.work")
				runGitForTest(t, root, "commit", "--quiet", "-m", "Remove the committed workspace")
			}
			selected, err := candidate.GoWork(root, working)
			require.NoError(t, err)
			cmd := exec.CommandContext(t.Context(), "go", "mod", "download")
			cmd.Dir = working
			cmd.Env = append(os.Environ(), "GOWORK="+selected, "GOPROXY=off", "GOTOOLCHAIN=local")
			output, downloadErr := cmd.CombinedOutput()
			work := t.TempDir()
			buildErr := candidate.Build(t.Context(), candidate.BuildSpec{
				SourceDir: root, ModuleDir: working, Package: ".", Out: filepath.Join(work, "app"),
				GOROOT: runtime.GOROOT(), ModCache: t.TempDir(), WorkDir: work,
				LogFile: filepath.Join(work, "build.log"),
			}, (*exec.Cmd).Run)
			if internal {
				assert.NoError(t, downloadErr, "%s", output)
				assert.NoError(t, buildErr)
			} else {
				assert.Error(t, downloadErr, "%s", output)
				assert.Error(t, buildErr)
			}
			assertFileContent(t, outer, outerBytes)
		})
	}
	assert.Empty(t, cp.builderCopyTurn)
}
