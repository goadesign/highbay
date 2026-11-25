// These tests call update_draft through the generated HTTP and MCP routes a
// guide uses, and prove what it receives when the update is refused or fails.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	goa "goa.design/goa/v3/pkg"
)

type (
	// updateDraftEntry is a draft fixture reached as alice's guide through the
	// generated routes, whose control plane records its spans.
	updateDraftEntry struct {
		*draftUpdateFixture
		// spans receives every span the control plane ends.
		spans *tracetest.InMemoryExporter
		// url is the base address of the generated HTTP and MCP server.
		url string
	}

	// updateDraftAnswer is what an HTTP client received from update_draft.
	updateDraftAnswer struct {
		// status is the response's HTTP status.
		status int
		// name is the goa-error response header, the refusal's declared name.
		name string
		// failure is the decoded body of a response that is not 200.
		failure goa.ServiceError
		// body is the raw response body.
		body string
	}

	// updateDraftCorrection is one to_correct entry as a client decodes it.
	updateDraftCorrection struct {
		Path    string `json:"path"`
		Folder  string `json:"folder"`
		Problem string `json:"problem"`
	}

	// projectCommitGate holds one of Highbay's reads of the project's commit,
	// which UpdateDraft makes without its draft lock.
	projectCommitGate struct {
		// started exists once the held read began.
		started string
		// release, once it exists, lets the held read and every later one run.
		release string
	}
)

const (
	// updateDraftRequestSpan is the operation policy's span of one call.
	updateDraftRequestSpan = "highbay.request.update_draft"
	// updateDraftOwnerSpan is UpdateDraft's own span.
	updateDraftOwnerSpan = "highbay.controlplane.update_draft"
	// updateGuideToken is the fixture credential of alice's guide.
	updateGuideToken = "alice-token-guide"
	// updateBaseDesign is the design the refusal fixtures start from.
	updateBaseDesign = "package design\n\nvar A = 0\n"
)

// TestUpdateDraftUnavailableAfterServerStops refuses a bound workspace before
// reading its files or creating a draft when the server has closed.
func TestUpdateDraftUnavailableAfterServerStops(t *testing.T) {
	workspace := realTempDir(t)
	cp := newControlPlane(t, Options{WorkspaceDir: workspace})
	require.NoError(t, cp.Close())
	_, err := cp.UpdateDraft(asCaller("alice@example.com", CallerGuide))
	assertServiceError(t, err, "unavailable")
	assert.ErrorContains(t, err, "the control plane is shutting down")
	assert.NoDirExists(t, filepath.Join(workspace, draftFolderName))
}

// TestUpdateDraftAnswersConflictWhenItsInputsChange changes each input
// UpdateDraft rechecks while it reads the project's commit without its lock.
// The guide receives the declared conflict, nothing is brought into the
// draft, and the next call succeeds.
func TestUpdateDraftAnswersConflictWhenItsInputsChange(t *testing.T) {
	const inputsChanged = "The draft, project files or design provenance changed while preparing the update; call update_draft again."
	cases := []struct {
		name string
		// nth is the commit read to hold: the first precedes the plan, the
		// second follows it.
		nth int
		// during changes one input while that read is held.
		during func(f *draftUpdateFixture) error
		// message is the refusal's whole message.
		message string
	}{
		{"project commit", 2, func(f *draftUpdateFixture) error { return emptyProjectCommit(f.dir) },
			"The project's commit changed while preparing the draft update; call update_draft again."},
		{"draft file", 1, func(f *draftUpdateFixture) error {
			return os.WriteFile(filepath.Join(f.dir, draftFolderName, updateDesignFile), []byte("package design\n\nvar A = 7\n"), 0o600)
		}, inputsChanged},
		{"project design file", 1, func(f *draftUpdateFixture) error {
			return os.WriteFile(filepath.Join(f.dir, updateDesignFile), []byte("package design\n\nvar A = 8\n"), 0o600)
		}, inputsChanged},
		{"design provenance", 1, func(f *draftUpdateFixture) error {
			f.cp.draftMu.Lock()
			defer f.cp.draftMu.Unlock()
			list, _, err := f.cp.cards.loadMadeFrom()
			if err != nil {
				return err
			}
			list.Write++
			return f.cp.cards.saveMadeFrom(list)
		}, inputsChanged},
	}
	for _, tc := range cases {
		for _, transport := range []string{"HTTP", "MCP"} {
			t.Run(tc.name+"/"+transport, func(t *testing.T) {
				e := newUpdateDraftEntry(t, updateBaseDesign, updateBaseDesign, updateBaseDesign)
				e.project("package design\n\nvar A = 1\n")
				before, taken := e.draft(), e.list().Taken
				gate := holdProjectCommitRead(t, tc.nth)
				finish := gate.change(t, func() error { return tc.during(e.draftUpdateFixture) })
				if transport == "HTTP" {
					answer := postUpdateDraft(t, e.url)
					finish()
					assert.Equal(t, http.StatusConflict, answer.status, answer.body)
					assert.Equal(t, "conflict", answer.name)
					assert.Equal(t, "conflict", answer.failure.Name)
					assert.Equal(t, tc.message, answer.failure.Message)
					assert.False(t, answer.failure.Fault)
				} else {
					shown, result := mcpToolCall(t, e.url, updateGuideToken, "update_draft", `{}`)
					finish()
					assert.Empty(t, result)
					assert.Equal(t, tc.message, shown, "the guide reads what to do next")
				}
				assert.Equal(t, map[string]bool{updateDraftRequestSpan: false, updateDraftOwnerSpan: false},
					endedUpdateDraftSpans(e.spans), "an expected refusal records no span error")
				if tc.name != "draft file" {
					assert.Equal(t, before, e.draft(), "the refused update brought nothing into the draft")
				}
				assert.Nil(t, e.list().Pending)
				assert.Equal(t, taken, e.list().Taken, "no take was recorded")
				assert.NotNil(t, e.update(), "calling update_draft again succeeds")
			})
		}
	}
}

// TestUpdateDraftRefusesWhatItCannotRead verifies that, once the draft
// exists, a file of the draft or the project folder that Highbay cannot read
// is the declared unprocessable refusal naming it and who corrects it, over
// HTTP and MCP, without a span error and without changing the draft.
func TestUpdateDraftRefusesWhatItCannotRead(t *testing.T) {
	invalid := "package design\n\n// \xff\n"
	inDraft := func(f *draftUpdateFixture, rel string) string {
		return filepath.Join(f.dir, draftFolderName, filepath.FromSlash(rel))
	}
	cases := []struct {
		name string
		// arrange changes the folders before the call.
		arrange func(t *testing.T, f *draftUpdateFixture)
		// during, when set, changes them while Highbay reads the project's commit.
		during func(f *draftUpdateFixture) error
		// names lists what the refusal's message holds.
		names []string
	}{
		{name: "project go.mod missing", arrange: func(t *testing.T, f *draftUpdateFixture) {
			require.NoError(t, os.Remove(filepath.Join(f.dir, "go.mod")))
		}, names: []string{"project folder", "read go.mod", "ask the user"}},
		{name: "project design file not valid UTF-8", arrange: func(t *testing.T, f *draftUpdateFixture) {
			f.project(invalid)
		}, names: []string{"project folder", updateDesignFile, "not valid UTF-8", "ask the user"}},
		{name: "project design file unreadable", arrange: func(t *testing.T, f *draftUpdateFixture) {
			if os.Geteuid() == 0 {
				t.Skip("the superuser reads a file without read permission")
			}
			require.NoError(t, os.Chmod(filepath.Join(f.dir, updateDesignFile), 0))
		}, names: []string{"project folder", updateDesignFile, "permission denied", "ask the user"}},
		{name: "project design file is a link", arrange: func(t *testing.T, f *draftUpdateFixture) {
			require.NoError(t, os.Symlink("design.go", filepath.Join(f.dir, "design", "linked.go")))
		}, names: []string{"project folder", "design/linked.go", "symbolic link", "ask the user"}},
		{name: "draft go.mod missing", arrange: func(t *testing.T, f *draftUpdateFixture) {
			require.NoError(t, os.Remove(inDraft(f, "go.mod")))
		}, names: []string{"draft ", draftFolderName, "read go.mod", "in the draft"}},
		{name: "draft design file not valid UTF-8", arrange: func(t *testing.T, f *draftUpdateFixture) {
			f.guide(invalid)
		}, names: []string{"draft ", draftFolderName, updateDesignFile, "not valid UTF-8", "in the draft"}},
		{name: "committed design file Go cannot parse", arrange: func(t *testing.T, f *draftUpdateFixture) {
			f.project("package design\n\nvar = broken\n")
			f.commit()
		}, names: []string{updateDesignFile, "the project's commit", "ask the user", "commit"}},
		{name: "committed design link the folder no longer holds", arrange: func(t *testing.T, f *draftUpdateFixture) {
			link := filepath.Join(f.dir, "design", "linked.go")
			require.NoError(t, os.Symlink("design.go", link))
			commitProjectDesign(t, f.dir, "design/linked.go")
			require.NoError(t, os.Remove(link))
		}, names: []string{"design/linked.go", "link", "the project's commit", "ask the user"}},
		{name: "project design file not valid UTF-8 while preparing", during: func(f *draftUpdateFixture) error {
			return os.WriteFile(filepath.Join(f.dir, updateDesignFile), []byte(invalid), 0o600)
		}, names: []string{"project folder", updateDesignFile, "not valid UTF-8", "ask the user"}},
		{name: "draft marker replaced while preparing", during: func(f *draftUpdateFixture) error {
			marker := inDraft(f, ".gitignore")
			return errors.Join(os.Remove(marker), os.Mkdir(marker, 0o700))
		}, names: []string{"draft ", draftFolderName, ".gitignore", "restore it or move it aside"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newUpdateDraftEntry(t, updateBaseDesign, updateBaseDesign, updateBaseDesign)
			e.project("package design\n\nvar A = 1\n")
			e.guide("package design\n\nvar A = 0\n\nvar Guide = 1\n")
			before := e.draft()
			finish := func() {}
			if tc.arrange != nil {
				tc.arrange(t, e.draftUpdateFixture)
				// A draft case replaces the draft's design text on purpose.
				before = readFileOrEmpty(inDraft(e.draftUpdateFixture, updateDesignFile))
			}
			if tc.during != nil {
				finish = holdProjectCommitRead(t, 1).change(t, func() error { return tc.during(e.draftUpdateFixture) })
			}
			answer := postUpdateDraft(t, e.url)
			finish()
			assert.Equal(t, http.StatusUnprocessableEntity, answer.status, answer.body)
			assert.Equal(t, "unprocessable", answer.name)
			assert.Equal(t, "unprocessable", answer.failure.Name)
			assert.False(t, answer.failure.Fault)
			for _, name := range tc.names {
				assert.Contains(t, answer.failure.Message, name)
			}
			assert.Equal(t, map[string]bool{updateDraftRequestSpan: false, updateDraftOwnerSpan: false},
				endedUpdateDraftSpans(e.spans), "an expected refusal records no span error")
			assert.Equal(t, before, readFileOrEmpty(inDraft(e.draftUpdateFixture, updateDesignFile)),
				"the refused update leaves the draft's design as it was")
			assert.Nil(t, e.list().Pending)
			if tc.during == nil {
				shown, result := mcpToolCall(t, e.url, updateGuideToken, "update_draft", `{}`)
				assert.Empty(t, result)
				assert.Equal(t, answer.failure.Message, shown, "the guide reads the same message over MCP")
			}
		})
	}
}

// TestUpdateDraftFailureNoCallerCanCorrectStaysAFault keeps a failure of
// Highbay's own read of the repository a fault: status 500 and an error on both
// of the call's spans.
func TestUpdateDraftFailureNoCallerCanCorrectStaysAFault(t *testing.T) {
	e := newUpdateDraftEntry(t, updateBaseDesign, updateBaseDesign, updateBaseDesign)
	e.project("package design\n\nvar A = 1\n")
	before := e.draft()
	failProjectCommitRead(t)
	answer := postUpdateDraft(t, e.url)
	assert.Equal(t, http.StatusInternalServerError, answer.status, answer.body)
	assert.True(t, answer.failure.Fault)
	assert.Equal(t, map[string]bool{updateDraftRequestSpan: true, updateDraftOwnerSpan: true},
		endedUpdateDraftSpans(e.spans), "a real failure records its error")
	assert.Equal(t, before, e.draft())
}

// TestUpdateDraftSaysWhichFolderHoldsAProjectFileToCorrect verifies what a
// client decodes when the user's own design file cannot be parsed: an entry
// that names the project folder in a field, over HTTP and MCP, and a draft
// file left as it was.
func TestUpdateDraftSaysWhichFolderHoldsAProjectFileToCorrect(t *testing.T) {
	e := newUpdateDraftEntry(t, updateBaseDesign, updateBaseDesign, updateBaseDesign)
	e.project("package design\n\nvar = broken\n")
	before := e.draft()
	answer := postUpdateDraft(t, e.url)
	require.Equal(t, http.StatusOK, answer.status, answer.body)
	_, structured := mcpToolCall(t, e.url, updateGuideToken, "update_draft", `{}`)
	for transport, raw := range map[string]string{"HTTP": answer.body, "MCP": string(structured)} {
		var result struct {
			ToCorrect []updateDraftCorrection `json:"to_correct"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &result), transport)
		require.Len(t, result.ToCorrect, 1, transport)
		entry := result.ToCorrect[0]
		assert.Equal(t, updateDesignFile, entry.Path, transport)
		assert.Equal(t, "project_folder", entry.Folder, transport)
		assert.Contains(t, entry.Problem, "expected", transport)
		assert.NotContains(t, entry.Problem, "Project folder:", "%s: the folder is a field, not a prefix", transport)
	}
	assert.Equal(t, before, e.draft(), "the guide's draft file is not the one to correct")
}

// TestUpdateDraftCorrectionsNameTheFolderOfTheFileToCorrect reaches every
// place UpdateDraft lists a file to correct and checks the folder it names:
// the guide's draft for a file the guide corrects, the project folder for the
// user's own design file or go.mod that cannot be parsed.
func TestUpdateDraftCorrectionsNameTheFolderOfTheFileToCorrect(t *testing.T) {
	changed, other := "package design\n\nvar A = 1\n", "package design\n\nvar A = 2\n"
	broken := "package design\n\nvar = broken\n"
	module := "module example.com/orders\n\ngo 1.24\n"
	inDraft := func(f *draftUpdateFixture, rel string) string {
		return filepath.Join(f.dir, draftFolderName, filepath.FromSlash(rel))
	}
	cases := []struct {
		name string
		// draft is the guide's design text when the case starts.
		draft string
		// arrange makes the state the update finds.
		arrange func(t *testing.T, f *draftUpdateFixture)
		path    string
		folder  string
		// problem is a part of the entry's problem text.
		problem string
	}{
		{"draft design file Go cannot parse", updateBaseDesign, func(t *testing.T, f *draftUpdateFixture) {
			f.guide("package design\n<<<<<<< broken\n")
		}, updateDesignFile, correctInDraft, "expected"},
		{"project design file Go cannot parse", updateBaseDesign, func(t *testing.T, f *draftUpdateFixture) {
			f.project(broken)
		}, updateDesignFile, correctInProject, "expected"},
		{"both changed the same lines", changed, func(t *testing.T, f *draftUpdateFixture) {
			f.project(other)
		}, updateDesignFile, correctInDraft, "resolve the conflict markers"},
		{"project removed what the draft changed", changed, func(t *testing.T, f *draftUpdateFixture) {
			require.NoError(t, os.Remove(filepath.Join(f.dir, updateDesignFile)))
		}, updateDesignFile, correctInDraft, "One side removed this file"},
		{"interrupted write and a draft changed since", updateBaseDesign, func(t *testing.T, f *draftUpdateFixture) {
			f.project(changed)
			f.stop(true)
			f.guide(changed + "\nvar Guide = 1\n")
		}, updateDesignFile, correctInDraft, "Highbay stopped while bringing"},
		{"interrupted merge that conflicts", changed, func(t *testing.T, f *draftUpdateFixture) {
			f.project(other)
			f.stop(false)
		}, updateDesignFile, correctInDraft, "resolve the conflict markers"},
		{"history gained changes from another line", "", func(t *testing.T, f *draftUpdateFixture) {
			f.commit()
			f.update()
			f.save()
			f.project(strings.Replace(f.projectText(), `Error("invalid")`, `Error("rejected")`, 1))
			f.commit()
			f.update()
			f.merge()
			f.guide(strings.Replace(f.draft(), "Method(\"m76\", func() {\n\t\tResult(Order)\n",
				"Method(\"m76\", func() {\n\t\tDescription(\"Guide edit.\")\n\t\tResult(Order)\n", 1))
			f.project(updateUserEdits(t)[1].edit(f.projectText()))
		}, updateDesignFile, correctInDraft, "from another line"},
		{"project go.mod cannot be parsed", updateBaseDesign, func(t *testing.T, f *draftUpdateFixture) {
			writeWorkspaceFiles(t, f.dir, map[string]string{"go.mod": module + "require (\n"})
		}, "go.mod", correctInProject, "project go.mod"},
		{"project go.mod declares no module", updateBaseDesign, func(t *testing.T, f *draftUpdateFixture) {
			writeWorkspaceFiles(t, f.dir, map[string]string{"go.mod": "go 1.24\n"})
		}, "go.mod", correctInProject, "declares no module"},
		{"draft go.mod cannot be parsed", updateBaseDesign, func(t *testing.T, f *draftUpdateFixture) {
			require.NoError(t, os.WriteFile(inDraft(f, "go.mod"), []byte(module+"require (\n"), 0o600))
		}, "go.mod", correctInDraft, "draft go.mod"},
		{"draft go.mod declares another module", updateBaseDesign, func(t *testing.T, f *draftUpdateFixture) {
			require.NoError(t, os.WriteFile(inDraft(f, "go.mod"), []byte("module example.com/other\n\ngo 1.24\n"), 0o600))
		}, "go.mod", correctInDraft, "must declare the same module"},
		{"project go.mod requires a module the draft lacks", updateBaseDesign, func(t *testing.T, f *draftUpdateFixture) {
			writeWorkspaceFiles(t, f.dir, map[string]string{"go.mod": module + "\nrequire example.com/lib v1.2.0\n"})
		}, "go.mod", correctInDraft, "The project requires example.com/lib v1.2.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var f *draftUpdateFixture
			if tc.draft == "" {
				// The other-line case needs the Go-shaped texts whose candidate
				// branch and user merge give the history two lines.
				base, guide, user := updateGoTexts(t)
				f = newDraftUpdateFixture(t, base, base, user)
				f.project(guide)
			} else {
				f = newDraftUpdateFixture(t, updateBaseDesign, updateBaseDesign, tc.draft)
			}
			tc.arrange(t, f)
			draftBefore := readFileOrEmpty(inDraft(f, tc.path))
			r := f.update()
			require.Len(t, r.ToCorrect, 1)
			entry := r.ToCorrect[0]
			assert.Equal(t, tc.path, entry.Path)
			assert.Equal(t, tc.folder, entry.Folder)
			assert.Contains(t, entry.Problem, tc.problem)
			assert.NotContains(t, entry.Problem, "Project folder:", "the folder is a field, not a prefix")
			if tc.folder == correctInProject {
				assert.Equal(t, draftBefore, readFileOrEmpty(inDraft(f, tc.path)), "nothing in the draft is to correct")
			}
		})
	}
}

// TestUpdateDraftBringsTheChangeOnceTheUserCorrectedTheProjectFile follows a
// project-folder correction to its end: the guide's draft stays as it was
// while the user's file cannot be parsed, and takes the change once it can.
func TestUpdateDraftBringsTheChangeOnceTheUserCorrectedTheProjectFile(t *testing.T) {
	f := newDraftUpdateFixture(t, updateBaseDesign, updateBaseDesign, updateBaseDesign)
	f.project("package design\n\nvar = broken\n")
	before := f.draft()
	r := f.update()
	require.Len(t, r.ToCorrect, 1)
	assert.Equal(t, correctInProject, r.ToCorrect[0].Folder)
	assert.Empty(t, r.Taken)
	assert.Equal(t, before, f.draft())
	f.project("package design\n\nvar A = 1\n")
	r = f.update()
	assert.Empty(t, r.ToCorrect)
	assert.Equal(t, []string{updateDesignFile}, r.Taken)
	assert.Equal(t, f.projectText(), f.draft())
}

// newUpdateDraftEntry makes a draft fixture whose control plane records
// spans and serves the generated routes, and forgets the spans of its setup.
func newUpdateDraftEntry(t *testing.T, base, initial, draft string) *updateDraftEntry {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	f := newDraftUpdateFixtureWith(t, Options{TracerProvider: provider}, base, initial, draft)
	url := serveUpdateDraft(t, f.cp)
	exporter.Reset()
	return &updateDraftEntry{draftUpdateFixture: f, spans: exporter, url: url}
}

// serveUpdateDraft serves cp's generated HTTP and MCP routes to alice's
// fixture credentials until the test ends, and returns the server's address.
func serveUpdateDraft(t *testing.T, cp *ControlPlane) string {
	t.Helper()
	server := httptest.NewServer(NewHTTPHandler(cp, workflowAuthenticator(map[string]Principal{
		"alice-token": {ID: "alice@example.com"},
	})))
	t.Cleanup(server.Close)
	return server.URL
}

// postUpdateDraft calls update_draft over its generated HTTP route as alice's
// guide and returns the status, declared error name and body received.
func postUpdateDraft(t *testing.T, base string) updateDraftAnswer {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/workspace/draft/update", nil)
	require.NoError(t, err)
	request.Header.Set("Authorization", "Bearer "+updateGuideToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, errors.Join(err, response.Body.Close()))
	answer := updateDraftAnswer{status: response.StatusCode, name: response.Header.Get("goa-error"), body: string(body)}
	if answer.status != http.StatusOK {
		require.NoError(t, json.Unmarshal(body, &answer.failure), answer.body)
	}
	return answer
}

// endedUpdateDraftSpans returns, for each update_draft span that ended,
// whether it recorded a failure (error status or an exception event), and
// forgets every recorded span.
func endedUpdateDraftSpans(exporter *tracetest.InMemoryExporter) map[string]bool {
	failed := map[string]bool{}
	for _, span := range exporter.GetSpans() {
		if span.Name != updateDraftRequestSpan && span.Name != updateDraftOwnerSpan {
			continue
		}
		recorded := span.Status.Code == codes.Error
		for _, event := range span.Events {
			recorded = recorded || event.Name == "exception"
		}
		failed[span.Name] = failed[span.Name] || recorded
	}
	exporter.Reset()
	return failed
}

// holdProjectCommitRead puts a git first on PATH that runs every query with
// the real one but makes the nth read of the project's commit wait, counted
// from this call, until the gate is released. Install it after the fixture.
func holdProjectCommitRead(t *testing.T, nth int) *projectCommitGate {
	t.Helper()
	dir := t.TempDir()
	gate := &projectCommitGate{started: filepath.Join(dir, "started"), release: filepath.Join(dir, "release")}
	installProjectCommitGit(t, nth, ": > "+shellQuoteForTest(gate.started)+
		"\nwhile [ ! -f "+shellQuoteForTest(gate.release)+" ]; do sleep 0.01; done")
	t.Cleanup(func() { assert.NoError(t, os.WriteFile(gate.release, nil, 0o600)) })
	return gate
}

// failProjectCommitRead puts a git first on PATH that fails Highbay's next read
// of the project's commit as a repository git cannot read does.
func failProjectCommitRead(t *testing.T) {
	t.Helper()
	installProjectCommitGit(t, 1, "echo 'fatal: unable to read the object database' >&2\nexit 128")
}

// installProjectCommitGit writes the stand-in git: it counts the queries that
// read the project's commit, runs action in place of the nth, and passes
// every query on to the real git.
func installProjectCommitGit(t *testing.T, nth int, action string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := t.TempDir()
	count := shellQuoteForTest(filepath.Join(bin, "count"))
	script := "#!/bin/sh\nfor arg do\nif [ \"$arg\" = 'HEAD^{commit}' ]; then\n" +
		"n=0\n[ -f " + count + " ] && n=$(cat " + count + ")\nn=$((n + 1))\necho \"$n\" > " + count + "\n" +
		"if [ \"$n\" -eq " + strconv.Itoa(nth) + " ]; then\n" + action + "\nfi\nfi\ndone\n" +
		"exec " + shellQuoteForTest(realGit) + " \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// change runs during once Highbay waits at the gate, then releases the gate.
// The caller makes the gated call, then calls the returned function, which
// fails the test when the call never reached the gate or during failed.
func (g *projectCommitGate) change(t *testing.T, during func() error) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go g.changeWhenHeld(ctx, during, done)
	return func() {
		t.Helper()
		cancel()
		require.NoError(t, <-done)
	}
}

// changeWhenHeld waits for the held read, runs during and releases the gate,
// sending their joined error; when ctx ends first it reports that no read was
// held. It uses no test helper because it runs away from the test goroutine.
func (g *projectCommitGate) changeWhenHeld(ctx context.Context, during func() error, done chan<- error) {
	for {
		if _, err := os.Stat(g.started); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			done <- errors.New("update_draft never read the project's commit")
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
	done <- errors.Join(during(), os.WriteFile(g.release, nil, 0o600))
}

// emptyProjectCommit moves the project's commit without the test helpers, for
// a change made away from the test goroutine.
func emptyProjectCommit(dir string) error {
	cmd := exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "--allow-empty", "-qm", "Commit while Highbay prepares the update")
	cmd.Dir = dir
	cmd.Env = append(cleanGitEnv(), "GIT_CONFIG_GLOBAL="+os.DevNull)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

// readFileOrEmpty returns the file's text, or "" when it cannot be read, so a
// case can compare a file it made unreadable on purpose.
func readFileOrEmpty(path string) string {
	text, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(text)
}

// shellQuoteForTest quotes s as one word of a POSIX shell script.
func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
