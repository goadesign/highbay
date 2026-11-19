//go:build e2e

// This file holds how the whole-journey test's callers act: the credential
// each one presents on its requests, the launcher roster a scripted agent
// connects from, the in-process project server's lifetime, and the calls and
// reads the journey's legs share.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"

	genmcpclient "github.com/goadesign/surla/gen/jsonrpc/mcp_surla/client"
	genmcpsurla "github.com/goadesign/surla/gen/mcp_surla"
	gensurla "github.com/goadesign/surla/gen/surla"
	gensurlatools "github.com/goadesign/surla/gen/surla/toolsets/surla"
	"github.com/goadesign/surla/internal/project"
	"github.com/goadesign/surla/internal/testwriter"
)

type (
	// journeyRoster is the role and placement contract Chorus uses to launch workers.
	journeyRoster struct {
		// Roles lists one row per registered role whose folder exists.
		Roles []journeyRosterRole `json:"roles"`
	}

	// journeyRosterRole is one role's row of the launcher roster.
	journeyRosterRole struct {
		// Role is the trusted role: guide, builder or test_writer.
		Role string `json:"role"`
		// Handle is the role's stable Chorus handle.
		Handle string `json:"handle"`
		// Folder is the folder the agent works in.
		Folder string `json:"folder"`
		// Connections are the MCP servers the agent connects to.
		Connections []json.RawMessage `json:"connections"`
	}

	// credentialDoer sends every request with exactly one caller's headers: a
	// scripted agent's bearer credential, the user's page session with the
	// same-origin headers a browser adds to the page's own requests, or the
	// management credential the CLI presents.
	credentialDoer struct {
		// client sends the requests; contexts bound each call.
		client *http.Client
		// header sets one value for each of its names on every request.
		header http.Header
	}
)

// Do sends r with the caller's headers.
func (d credentialDoer) Do(r *http.Request) (*http.Response, error) {
	for name := range d.header {
		r.Header.Set(name, d.header.Get(name))
	}
	return d.client.Do(r)
}

// journeyChangeArgs returns the tool arguments naming change, which
// get_build, list_verifications and each worker's get_*_assignment take.
func journeyChangeArgs(change string) []byte {
	return []byte(`{"change_id":"` + change + `"}`)
}

// journeyReportArgs returns report_tests_finished's arguments for revision
// of change.
func journeyReportArgs(t *testing.T, change, revision string) []byte {
	t.Helper()
	raw, err := gensurlatools.MarshalReportTestsFinishedPayload(&gensurlatools.ReportTestsFinishedPayload{
		ChangeID: change, RequestRevision: revision, CoverageStatements: []*gensurlatools.TestCoverageStatement{},
	})
	require.NoError(t, err)
	return raw
}

// journeyRefusal calls the tool name and returns the text of the refusal it
// must answer with: a refused operation reaches an agent as a failed tool
// result, not as a transport error.
func journeyRefusal(t *testing.T, ctx context.Context, client *genmcpsurla.Client,
	name string, args []byte,
) string {
	t.Helper()
	result, err := client.ToolsCall(ctx, &genmcpsurla.ToolsCallPayload{Name: name, Arguments: args})
	require.NoError(t, err)
	complete, ok := result.Outcome.AsComplete()
	require.True(t, ok, "%s completes in the same call", name)
	require.True(t, complete.IsError != nil && *complete.IsError,
		"%s must be refused; it returned %s", name, complete.StructuredContent)
	return toolErrorText(complete.Content)
}

// readBuild has client read change's build with get_build, which every role
// may call, and returns the started build.
func readBuild(
	t *testing.T, ctx context.Context, client *genmcpsurla.Client, change string,
) *gensurla.StartedBuild {
	t.Helper()
	read, err := gensurla.DecodeChangeBuild(
		trustedSuiteCall(t, ctx, client, "get_build", journeyChangeArgs(change)))
	require.NoError(t, err)
	started, ok := read.Build.AsStarted()
	require.True(t, ok, "the change has a build: %s", read.Build.Kind())
	return started
}

// journeyVerifyArgs returns verify_change's arguments for candidateCommit at
// revision of change.
func journeyVerifyArgs(t *testing.T, change, revision, candidateCommit string) []byte {
	t.Helper()
	raw, err := gensurla.EncodeVerifyChangePayload(&gensurla.VerifyChangePayload{
		ChangeID: change, RequestRevision: revision, CandidateCommit: candidateCommit,
	})
	require.NoError(t, err)
	return raw
}

// listRuns has client list the verification runs of change.
func listRuns(
	t *testing.T, ctx context.Context, client *genmcpsurla.Client, change string,
) []*gensurla.VerificationStatusResult {
	t.Helper()
	listed, err := gensurla.DecodeVerificationListResult(
		trustedSuiteCall(t, ctx, client, "list_verifications", journeyChangeArgs(change)))
	require.NoError(t, err)
	return listed.Runs
}

// journeyFile returns the text of the file at path, which must exist.
func journeyFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(raw)
}

// journeyBranchTip returns the commit branch points to in the repository of
// the folder dir.
func journeyBranchTip(t *testing.T, dir, branch string) string {
	t.Helper()
	return strings.TrimSpace(runTestGit(t, dir, "rev-parse", "refs/heads/"+branch))
}

// journeyCommitAll commits everything in the working copy at dir the way the
// journey's builder commits its work, git add -A then git commit with
// message, checks that this leaves nothing uncommitted or untracked there,
// and returns the new commit.
func journeyCommitAll(t *testing.T, dir, message string) string {
	t.Helper()
	runTestGit(t, dir, "add", "-A")
	runTestGit(t, dir, "commit", "-q", "-m", message)
	assert.Empty(t, runTestGit(t, dir, "status", "--porcelain", "--untracked-files=all"),
		"git add -A and the commit leave the copy clean")
	return strings.TrimSpace(runTestGit(t, dir, "rev-parse", "HEAD"))
}

// replaceOnce edits the file at path the way an agent edits a file in its
// own folder: old must occur in it exactly once and becomes replacement.
func replaceOnce(t *testing.T, path, old, replacement string) {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(raw), old),
		"%s holds the text to replace once", filepath.Base(path))
	edited := strings.Replace(string(raw), old, replacement, 1)
	require.NoError(t, os.WriteFile(path, []byte(edited), info.Mode().Perm()))
}

// approvePayload returns what the page's Approve control sends for the first
// change's newest passing run: the change and its agreed revision, and the
// run's candidate commit and report digest.
func (j *wholeJourney) approvePayload(t *testing.T) *gensurla.ApproveMergePayload {
	t.Helper()
	completed, ok := j.passed.State.AsCompleted()
	require.True(t, ok, "only a finished run is approved")
	return &gensurla.ApproveMergePayload{
		ChangeID:        journeyChangeID,
		RequestRevision: j.confirmed,
		CandidateCommit: j.passed.CandidateCommit,
		ReportDigest:    completed.Report.ReportDigest,
	}
}

// passRun has the builder verify commit for the first change's agreed
// revision and follows the run until it finishes. The run must pass every
// scenario of the tests accepted at testsCommit and say that they are the
// written tests of that commit. It returns the finished run, which stays in
// the project's runs folder until the journey's cleanup drains it.
func (j *wholeJourney) passRun(
	t *testing.T, ctx context.Context, commit, testsCommit string,
) *gensurla.VerificationStatusResult {
	t.Helper()
	run, err := gensurla.DecodeVerificationStatusResult(trustedSuiteCall(t, ctx, j.builder, "verify_change",
		journeyVerifyArgs(t, journeyChangeID, j.confirmed, commit)))
	require.NoError(t, err)
	require.NotEmpty(t, run.RunID)
	// configureProjectWorkspace keeps the project's runs in its state folder.
	runsDir := filepath.Join(j.state.Dir, "runs")
	j.t.Cleanup(func() { drainTrustedSuiteRun(runsDir, run.RunID, j.state.Dir, j.dir) })
	args, err := gensurla.EncodeGetVerificationPayload(&gensurla.GetVerificationPayload{RunID: run.RunID})
	require.NoError(t, err)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for run.State.Kind() == gensurla.StateKindRunning {
		select {
		case <-ctx.Done():
			t.Fatalf("the verification run did not finish: %v", ctx.Err())
		case <-ticker.C:
		}
		run, err = gensurla.DecodeVerificationStatusResult(
			trustedSuiteCall(t, ctx, j.builder, "get_verification", args))
		require.NoError(t, err)
	}
	if stopped, ended := run.State.AsNotCompleted(); ended {
		t.Fatalf("the run ended without a report: %s at stage %s: %s", stopped.Kind, stopped.Stage, stopped.Summary)
	}
	completed, ok := run.State.AsCompleted()
	require.True(t, ok, "the run ends with a report: %s", run.State.Kind())
	require.True(t, completed.Report.Passed,
		"the builder's commit passes the accepted tests: %+v", completed.Report)
	scenarios := make([]string, 0, len(completed.Report.Scenarios))
	for _, s := range completed.Report.Scenarios {
		scenarios = append(scenarios, s.ID)
		assert.Equal(t, "passed", s.Outcome, "scenario %s", s.ID)
	}
	assert.ElementsMatch(t, journeyScenarios, scenarios, "the starter's scenarios and one per finished flow")
	assert.Equal(t, commit, run.CandidateCommit)
	assert.Equal(t, j.confirmed, run.RequestRevision)
	assert.Equal(t, testsCommit, run.SuiteCommit, "the run uses the accepted tests")
	written, ok := run.TestSource.AsWrittenTests()
	require.True(t, ok, "the run's tests are the written ones: %s", run.TestSource.Kind())
	assert.Equal(t, testsCommit, written.Commit)
	assert.Equal(t, journeyWrittenTestsNotice, written.Notice)
	return run
}

// passedTaking returns how build 1 reads once the run in j.passed passed and
// before the user merges it: the two commits the user merges, the builder's
// and the tests at j.testsCommit, each with build 1's branch it was verified
// on, and the build's base still in the project's branch.
func (j *wholeJourney) passedTaking() gensurla.BuildTaking {
	return gensurla.NewBuildTakingPassed(&gensurla.BuildPassed{
		RunID:           j.passed.RunID,
		CandidateCommit: j.passed.CandidateCommit,
		CandidateBranch: testwriter.BuilderRole.Branch(1),
		Tests: gensurla.NewBuildPassedTestSourceWrittenTests(&gensurla.BuildPassedTests{
			Commit: j.testsCommit, Branch: testwriter.TestsRole.Branch(1),
		}),
		BaseInBranch: true,
	})
}

// assertPassedTaking checks that the user's page and an agent both read build
// 1 as passedTaking describes it.
func (j *wholeJourney) assertPassedTaking(t *testing.T, ctx context.Context) {
	t.Helper()
	for who, build := range j.buildReads(t, ctx, journeyChangeID) {
		assert.Equal(t, j.passedTaking(), build.Taking,
			"the %s reads the passed run's commits to merge and their branches", who)
	}
}

// buildReads reads change's build as the user's page does and as an agent
// does, the guide through get_build, and returns the started build each sees.
func (j *wholeJourney) buildReads(
	t *testing.T, ctx context.Context, change string,
) map[string]*gensurla.StartedBuild {
	t.Helper()
	page, err := j.user.GetBuild(ctx, &gensurla.GetBuildPayload{ChangeID: change})
	require.NoError(t, err)
	shown, ok := page.Build.AsStarted()
	require.True(t, ok, "the user's page reads a build of %s: %s", change, page.Build.Kind())
	return map[string]*gensurla.StartedBuild{"user": shown, "guide": readBuild(t, ctx, j.guide, change)}
}

// builderWork returns the assignments list_builder_assignments gives the
// builder: the open build's while it needs the builder, none otherwise.
func (j *wholeJourney) builderWork(t *testing.T, ctx context.Context) []*gensurla.BuilderAssignment {
	t.Helper()
	listed, err := gensurla.DecodeBuilderAssignmentList(
		trustedSuiteCall(t, ctx, j.builder, "list_builder_assignments", []byte(`{}`)))
	require.NoError(t, err)
	return listed.Assignments
}

// testWriterWork returns the assignments list_test_writer_assignments gives
// the test writer: the open build's while it needs the test writer, none
// otherwise.
func (j *wholeJourney) testWriterWork(
	t *testing.T, ctx context.Context,
) []*gensurla.TestWriterAssignmentItem {
	t.Helper()
	listed, err := gensurla.DecodeTestWriterAssignmentList(
		trustedSuiteCall(t, ctx, j.testWriter, "list_test_writer_assignments", []byte(`{}`)))
	require.NoError(t, err)
	return listed.Assignments
}

// startServer runs the real serve composition until the journey's test ends.
// Cleanup stops it through the CLI's management client and waits until it
// has drained its work; a server that does not stop keeps the folders it may
// still use by ending the test binary.
func (j *wholeJourney) startServer(t *testing.T, runtime projectRuntime) {
	served := make(chan error, 1)
	starting, cancelStart := context.WithTimeout(t.Context(), time.Minute)
	defer cancelStart()
	state := j.state
	go func() {
		err := runtime.serve(state.Dir)
		cancelStart()
		served <- err
	}()
	j.t.Cleanup(func() {
		select {
		case err := <-served:
			assert.NoError(j.t, err, "the project server ended early")
			return
		default:
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		stopErr := projectClient(state).Stop(ctx)
		select {
		case err := <-served:
			assert.NoError(j.t, err, "the project server stopped with an error")
		case <-ctx.Done():
			retainAndExit(errors.Join(errors.New("the project server did not stop"), stopErr),
				state.Dir, j.dir)
		}
	})
	_, err := waitForRunning(starting, state)
	require.NoError(t, err, "the project server did not publish its management access")
}

// scriptedAgent binds the registered worker folder and role credential to a
// fixture MCP client, independent of window placement and agent configuration.
func (j *wholeJourney) scriptedAgent(t *testing.T, role string) (*genmcpsurla.Client, string) {
	t.Helper()
	state, err := project.Discover(filepath.Dir(j.state.Dir), j.state.Record.Directory)
	require.NoError(t, err)
	for handle, row := range state.Record.Agents {
		if row.Role != role {
			continue
		}
		address, err := url.Parse(state.Origin())
		require.NoError(t, err)
		path := state.AgentFile(handle)
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "only the owner reads a credential")
		credential, err := os.ReadFile(path)
		require.NoError(t, err)
		doer := credentialDoer{
			client: &http.Client{},
			header: http.Header{"Authorization": {"Bearer " + string(credential)}},
		}
		transport := genmcpclient.NewClient(address.Scheme, address.Host, doer,
			goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
		return &genmcpsurla.Client{ToolsCallEndpoint: transport.ToolsCall()}, row.Folder
	}
	t.Fatalf("the project has no registered %s", role)
	return nil, ""
}

// reportTests has the test writer report its tests finished for change at
// its agreed revision, checks that the report returns at once as reported
// with the number report, and waits, reading get_build as the test writer,
// until Surla's check replaces reported with its verdict on that report. It
// returns the build with the verdict.
func (j *wholeJourney) reportTests(
	t *testing.T, ctx context.Context, change, revision string, report int,
) *gensurla.StartedBuild {
	t.Helper()
	item, err := gensurla.DecodeTestWriterAssignmentItem(trustedSuiteCall(t, ctx, j.testWriter,
		"report_tests_finished", journeyReportArgs(t, change, revision)))
	require.NoError(t, err)
	require.Equal(t, gensurla.TestsKindReported, item.Progress.Tests.Kind(), "a report returns at once")
	require.Equal(t, report, item.Progress.Report)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		build := readBuild(t, ctx, j.testWriter, change)
		if build.TestWriter.Tests.Kind() != gensurla.TestsKindReported {
			require.Equal(t, report, build.TestWriter.Report, "the verdict answers report %d", report)
			return build
		}
		select {
		case <-ctx.Done():
			t.Fatalf("Surla's check of report %d reached no verdict: %v", report, ctx.Err())
		case <-ticker.C:
		}
	}
}

// latestVerifications reads the change list as the builder, through
// list_changes, and as the user's page does, and returns the latest
// verification each sees for change, which must be the only change listed:
// the journey's first change, until the guide proposes the second.
func (j *wholeJourney) latestVerifications(
	t *testing.T, ctx context.Context, change string,
) map[string]*gensurla.LatestVerificationItem {
	t.Helper()
	builder, err := gensurla.DecodeChangeList(
		trustedSuiteCall(t, ctx, j.builder, "list_changes", []byte(`{}`)))
	require.NoError(t, err)
	page, err := j.user.ListChanges(ctx)
	require.NoError(t, err)
	latest := map[string]*gensurla.LatestVerificationItem{}
	for who, list := range map[string]*gensurla.ChangeList{"builder": builder, "user": page} {
		require.Len(t, list.Changes, 1)
		require.Equal(t, change, list.Changes[0].ChangeID)
		require.NotNil(t, list.Changes[0].LatestVerification, "the %s sees a latest verification", who)
		latest[who] = list.Changes[0].LatestVerification
	}
	return latest
}

// testsBranch returns the commit the tests branch of the build numbered build
// points to in the project's repository.
func (j *wholeJourney) testsBranch(t *testing.T, build int) string {
	t.Helper()
	return journeyBranchTip(t, j.dir, testwriter.TestsRole.Branch(build))
}

// readBuilderAssignment has the builder read its assignment for change in
// full.
func (j *wholeJourney) readBuilderAssignment(
	t *testing.T, ctx context.Context, change string,
) *gensurla.BuilderAssignment {
	t.Helper()
	assignment, err := gensurla.DecodeBuilderAssignment(
		trustedSuiteCall(t, ctx, j.builder, "get_builder_assignment", journeyChangeArgs(change)))
	require.NoError(t, err)
	return assignment
}

// readTestWriterAssignment has the test writer read its assignment for change
// in full.
func (j *wholeJourney) readTestWriterAssignment(
	t *testing.T, ctx context.Context, change string,
) *gensurla.TestWriterAssignment {
	t.Helper()
	assignment, err := gensurla.DecodeTestWriterAssignment(
		trustedSuiteCall(t, ctx, j.testWriter, "get_test_writer_assignment", journeyChangeArgs(change)))
	require.NoError(t, err)
	return assignment
}

// roster reads the launcher roster the server last wrote for the team's space.
func (j *wholeJourney) roster(t *testing.T) journeyRoster {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(j.rosters, j.space+".json"))
	require.NoError(t, err)
	var roster journeyRoster
	require.NoError(t, json.Unmarshal(raw, &roster))
	return roster
}

// pageHeader returns the headers a browser adds to the views page's own
// requests to the project server.
func (j *wholeJourney) pageHeader() http.Header {
	return http.Header{"Sec-Fetch-Site": {"same-origin"}, "Origin": {j.state.Origin()}}
}
