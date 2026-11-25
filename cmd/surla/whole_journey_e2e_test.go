//go:build e2e

// This file drives the whole team journey through the real project server:
// the server `surla serve` runs, with the in-memory Herdr stand-in and the
// launcher roster Chorus reads in temporary state, and scripted programs in
// place of the guide, the builder and the test writer, each connected with its
// own role's credential exactly as the roster hands it to an agent. The user
// acts through a views-page session obtained the way the page obtains one.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"
	goa "goa.design/goa/v3/pkg"

	genprojectclient "github.com/goadesign/surla/gen/http/project/client"
	genhttpclient "github.com/goadesign/surla/gen/http/surla/client"
	genmcpsurla "github.com/goadesign/surla/gen/mcp_surla"
	genproject "github.com/goadesign/surla/gen/project"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/project"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/goadesign/surla/internal/verify"
)

type (
	// wholeJourney carries what each step of the journey hands the next. Its
	// steps run as subtests of one test; everything that outlives a step (the
	// folders, the working directory, the environment and the server) belongs
	// to that test, t, so a step's own test only asserts.
	wholeJourney struct {
		// t is the journey's test, which owns every resource the steps share.
		// Steps call only its Cleanup and Chdir, whose effects must outlive
		// the step; they assert with their own test.
		t *testing.T
		// workspace is the empty folder the user creates the project in.
		workspace string
		// dir is the project folder, as the server stores it: symlink-free.
		dir string
		// builderFolder is the working folder returned by the builder's
		// assignment, where generation and authored candidate commits belong.
		builderFolder string
		// projectCommit is the starter's first commit, where the build's
		// working copy for the test writer starts.
		projectCommit string
		// state is the project's private state, which the server owns.
		state *project.State
		// rosters is the fixture team effects' launcher roster folder: the
		// server writes there the roster Chorus's launcher starts agents from.
		rosters string
		// space is the Herdr space the server prepared for the team.
		space string
		// user is the user's views-page session.
		user *gensurla.Client
		// guide, builder and testWriter are the scripted agents' connections.
		guide, builder, testWriter *genmcpsurla.Client
		// testsFolder is the test writer's working copy, as its roster row
		// names it.
		testsFolder string
		// draftFolder is the guide's draft of the project's design, which its
		// first Story refresh makes and where it drafts every flow.
		draftFolder string
		// proposed and confirmed are the first change's revisions before and
		// after the user confirmed it; confirmed is the agreed revision build 1
		// is for.
		proposed, confirmed string
		// startedAt is when the user started build 1, as every later read of
		// that build reports it.
		startedAt string
		// testsCommit is the commit Surla recorded the accepted tests at that
		// the run in passed used: the first acceptance's, then the later
		// acceptance's once the builder verified again.
		testsCommit string
		// passed is the newest verification run of the builder's commit against
		// the tests at testsCommit, as it finished with its passed report.
		passed *gensurla.VerificationStatusResult
		// muffin and bagel are the agreed revisions of the second and the third
		// change.
		muffin, bagel string
		// merged is the project branch's commit once the user merged build 1's
		// passed run: the base of builds 2 and 3.
		merged string
		// leftBehind is the tip of build 2's builder branch when the user drops
		// that build: the builder's one commit for the second change.
		leftBehind string
	}
)

const (
	// journeyChangeID is the first change the guide proposes, the one build 1
	// builds, verifies and the user merges.
	journeyChangeID = "add-scone-price"
	// journeyDraftFolder is the guide's draft folder inside the project
	// folder, as the control plane names it (draftFolderName).
	journeyDraftFolder = ".surla-draft"
	// journeyCookieFlow ends the finished flow the starter's design/story.go
	// ships, prices-cookie, after which the guide adds its own flow.
	journeyCookieFlow = `		Outcome("cookie-priced", "customer", "The helper answers that a cookie costs $1.25")
	})
`
	// journeySconeFlow is the finished flow the guide adds to the shipped
	// Story, so a build requires two tests: prices_cookie and prices_scone.
	journeySconeFlow = `	Flow("prices-scone", "Price a scone", func() {
		StartsWith("ask-scone-price", "customer", "The customer asks how much a scone is")
		Outcome("scone-priced", "customer", "The helper answers that a scone costs $2.75")
	})
`
	// journeyCookieHook ends the starter's PricesCookie hook in
	// eval/acceptance/acceptance.go, after which the test writer adds its own.
	journeyCookieHook = `	return h.ask(ctx, "How much is cookie?", "[scripted] Cookie costs $1.25.", true)
}
`
	// journeySconeHook is the hook the test writer writes for prices_scone.
	journeySconeHook = `
// PricesScone is the test the Story's finished flow prices-scone requires.
func (h *Hooks) PricesScone(ctx context.Context) (eval.Result, error) {
	return h.ask(ctx, "How much is scone?", "[scripted] Scone costs $2.75.", true)
}
`
	// journeyCookiePrice is the starter's catalog line after which the builder
	// prices scones.
	journeyCookiePrice = `	"cookie": 125,
`
	// journeyStepWait bounds a Story evaluation, a build start, or the test
	// writer's first read of a build, which prepares the required tests. The
	// control plane allows each 5 minutes of work (defaultStoryRefreshTimeout,
	// defaultBuildStartTimeout), followed by up to 20 seconds of evaluator
	// cleanup and, for a build start, 45 seconds for the team's window phase
	// (teamStartTimeout). The remaining time lets the owner report its result
	// before this wait ends.
	journeyStepWait = 7 * time.Minute
	// journeyGenerateWait bounds one generate_code call. The server sets no
	// limit of its own on it, so this is the journey's: generous for a slow
	// machine, and short enough that a hung generation fails under its name.
	journeyGenerateWait = 3 * time.Minute
	// journeyCheckWait bounds one check of the test writer's report, from the
	// report to the verdict. The control plane gives the whole check 15
	// minutes (checkLimit in internal/controlplane), within which its
	// build has 10 minutes and its listing 60 seconds, and reaches
	// could_not_run itself at that limit; one more minute lets that verdict
	// arrive before this wait ends. The verification run's wait is
	// journeyOwnerWait, the server's own run limit plus its stop path.
	journeyCheckWait = 16 * time.Minute
	// journeyWrittenTestsNotice is the fixed notice every run of written tests,
	// and every approval of one, carries.
	journeyWrittenTestsNotice = "These tests were written by a separate agent on this machine " +
		"and are not isolated from the builder."
)

// journeyScenarios are the scenarios a run of the first change's accepted
// tests reports: the starter suite's own and one per finished flow of the
// agreed Story.
var journeyScenarios = []string{
	"prices_tea", "prompt_override_reaches_model", "unknown_item", "prices_cookie", "prices_scone",
}

// TestWholeJourneyThroughTheProjectServer walks three changes through the
// team in journey order. The first goes the whole way: the starter project,
// which ships a Story, is committed; the real project server starts with its
// team and gives the user a page session; the guide adds a finished flow to
// the Story in its draft and proposes the change with it, and the user
// confirms it; the user starts build 1, and the builder and the test writer,
// each with the credential that start created, read their assignments in
// their own working copies. Then: a report for the change's earlier revision
// is refused; the builder's verification before any tests are accepted is
// refused with the test writer's step; a report that changed a file outside
// the test module is sent back with findings only the test writer and the
// user can read; a report without the hook the new flow requires is sent back
// with the compiler's finding naming it; the report with the hook is accepted
// and recorded on the tests branch, and the builder's assignment names that
// commit; the builder's commit passes those tests in a run that says where
// the tests came from and that the test writer may read, and the change list
// shows it as current; the user approves that run on the page without
// promotion settings, and the approval names the builder's commit and the
// tests' commit; a later acceptance of changed tests leaves that run the
// change's latest, passed, but no longer current, hides its approval, and
// refuses approving it again; and the builder verifies again with the later
// tests and the user approves that run.
//
// The second and third changes show what one open build per project means
// (whole_journey_builds_e2e_test.go): the second is agreed while build 1 is
// open and its build is refused with the commits to merge; the user merges
// them with git and the build reads merged; the second change is then built
// as build 2 from the merged branch, each copy joining it at its worker's
// read; the user drops build 2 with work left in the builder's copy, which
// Drop leaves alone; and the third change, drafted in the draft Surla made
// again, is built as build 3, where the builder's read keeps the leftovers on
// build 2's branch and the next candidate holds none of the dropped change.
// Builds 2 and 3 are not verified. Each step stops the journey when it fails,
// because every later step builds on it.
func TestWholeJourneyThroughTheProjectServer(t *testing.T) {
	j := newWholeJourney(t)
	for _, step := range []struct {
		name string
		run  func(*testing.T)
	}{
		{"the starter project is committed", j.commitStarter},
		{"the project server runs its team and opens the user's session", j.serveProject},
		{"the guide adds a flow, proposes the change and the user confirms it", j.proposeAndConfirm},
		{"the user starts the build and each worker reads its assignment", j.startBuild},
		{"a report for an earlier revision is refused", j.refuseEarlierReport},
		{"verification before accepted tests is refused with the test writer's step",
			j.refuseUnacceptedVerification},
		{"a change outside the test module is sent back to the test writer", j.sendBackOutsideChange},
		{"tests without the required hook are sent back with the compiler's finding", j.sendBackMissingHook},
		{"the fixed tests are accepted and recorded on the tests branch", j.acceptTests},
		{"the builder's commit passes the accepted tests", j.verifyAcceptedTests},
		{"the user approves the passing run on the page", j.approvePassedRun},
		{"a later acceptance leaves the run passed but not current and its approval not shown",
			j.acceptLaterTests},
		{"the builder verifies again with the later tests and the user approves that run",
			j.verifyLaterTests},
		{"a second change agreed while the build is open is refused its build", j.refuseSecondBuild},
		{"the user merges the passed run's commits and the build reads merged", j.mergePassedRun},
		{"the second change is built from the merged branch and each copy joins at its worker's read",
			j.buildSecondChange},
		{"the user drops the second build and the builder's copy keeps its uncommitted work",
			j.dropSecondBuild},
		{"a third change is built and the leftovers stay on the dropped build's branch",
			j.buildThirdChange},
	} {
		if !t.Run(step.name, step.run) {
			return
		}
	}
}

// printedFirstCommit returns the commands surla init printed to make the
// first commit: its first block of indented lines, which ends with surla,
// the command that starts the project's server.
func printedFirstCommit(t *testing.T, printed string) []string {
	t.Helper()
	var commands []string
	for _, line := range strings.Split(printed, "\n") {
		command, indented := strings.CutPrefix(line, "  ")
		if !indented && len(commands) > 0 {
			break
		}
		if indented {
			commands = append(commands, command)
		}
	}
	require.NotEmpty(t, commands, "surla init prints the first commit's commands:\n%s", printed)
	require.Equal(t, "surla", commands[len(commands)-1], "the commands end by starting Surla")
	return commands
}

// newWholeJourney isolates the journey from the user's Surla configuration,
// the Git identity of the commits the test and the server make, model keys,
// trace export and the caller's Herdr terminal, and moves into an empty
// workspace. The product's own read-only git calls still read the user's Git
// configuration, as they do for every user.
func newWholeJourney(t *testing.T) *wholeJourney {
	t.Helper()
	workspace := isolateServeWorkspace(t)
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "Surla Test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "Surla Test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	// The in-process serve removes every model provider variable from this
	// process; isolating them first restores the caller's own afterwards.
	isolateProviderFamilies(t)
	for _, name := range []string{
		verify.LiveModelGatewayEnv, verify.LiveModelProviderEnv, verify.LiveModelNameEnv,
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		// The in-process server would otherwise record the caller's own Herdr
		// terminal in the project's running record.
		"HC_TERMINAL_ID",
	} {
		t.Setenv(name, "")
	}
	return &wholeJourney{t: t, workspace: workspace}
}

// commitStarter creates the worked example with surla init -example, which
// ships its Story and the helper agent this journey's changes extend, and
// runs, in a POSIX shell from the workspace, exactly the commands surla
// init prints to make the first commit: its first block of indented lines,
// up to the closing surla, which starts the project's server and which the
// next leg replaces with the in-process server. Those commands must commit
// every file the starter has, or every user's first commit leaves one out.
func (j *wholeJourney) commitStarter(t *testing.T) {
	dir := filepath.Join(j.workspace, "shop")
	var printed strings.Builder
	require.NoError(t, runInit([]string{
		"-dir", dir, "-module", "example.com/shop", "-surla-version", starterSurlaVersion, "-example",
	}, &printed))
	commands := printedFirstCommit(t, printed.String())
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	shell := exec.CommandContext(ctx, "/bin/sh", "-c", strings.Join(commands[:len(commands)-1], " && "))
	shell.Dir = j.workspace
	shell.Env = testGitEnv()
	out, err := shell.CombinedOutput()
	require.NoError(t, err, "the printed commands %q: %s", commands, out)
	commit := strings.TrimSpace(runTestGit(t, dir, "rev-parse", "HEAD"))
	assert.Regexp(t, "^[0-9a-f]{40}$", commit)
	assert.Empty(t, runTestGit(t, dir, "status", "--porcelain", "--untracked-files=all"),
		"the printed commands commit every file the starter has")
	physical, err := filepath.EvalSymlinks(dir)
	require.NoError(t, err)
	j.dir, j.projectCommit = physical, commit
}

// serveProject creates the project the way surla does and runs the real
// surla serve composition in the project folder, with the fixture team
// effects; ensures the team through the CLI's management client; and turns a
// browser link into the user's page session.
func (j *wholeJourney) serveProject(t *testing.T) {
	state, err := fixtureProjectState(j.dir)
	require.NoError(t, err)
	j.state = state
	j.t.Chdir(j.dir)
	runtime := testProjectRuntime()
	bindFixtureTeamEffects(&runtime)
	root, err := runtime.configRoot()
	require.NoError(t, err)
	// bindFixtureTeamEffects writes the launcher's rosters here.
	j.rosters = filepath.Join(root, "fixture-rosters")
	j.startServer(t, runtime)

	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	management := projectClient(state)
	attached, err := management.AttachGuide(ctx, &genproject.AttachGuidePayload{})
	require.NoError(t, err)
	assert.NotEmpty(t, attached.Handle)
	assert.False(t, attached.AdoptedHerdr, "the fixture guide connects outside Herdr")
	team, err := management.EnsureTeam(ctx)
	require.NoError(t, err)
	assert.Empty(t, team.Space)
	require.Len(t, team.Roles, 1, "only the guide is registered before a build")
	assert.Equal(t, "guide", team.Roles[0].Role)

	link, err := management.BrowserLink(ctx)
	require.NoError(t, err)
	require.NoError(t, validateProjectViewsLink(state, link.URL))
	page := genprojectclient.NewClient("http", state.Address(),
		credentialDoer{client: &http.Client{}, header: j.pageHeader()},
		goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	exchanged, err := page.ExchangeBrowserLink()(ctx, &genproject.ExchangeBrowserLinkPayload{
		Link: strings.TrimPrefix(link.URL, state.Origin()+"/views#link="),
	})
	require.NoError(t, err)
	header := j.pageHeader()
	header.Set("Authorization", "Bearer "+exchanged.(*genproject.ExchangeBrowserLinkResult).Session)
	transport := genhttpclient.NewClient("http", state.Address(),
		credentialDoer{client: &http.Client{}, header: header},
		goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	j.user = &gensurla.Client{
		GetTeamEndpoint:         transport.GetTeam(),
		ConfirmIntentEndpoint:   transport.ConfirmIntent(),
		StartBuildEndpoint:      transport.StartBuild(),
		GetBuildEndpoint:        transport.GetBuild(),
		DropBuildEndpoint:       transport.DropBuild(),
		GetTestFindingsEndpoint: transport.GetTestFindings(),
		ListChangesEndpoint:     transport.ListChanges(),
		GetChangeEndpoint:       transport.GetChange(),
		ApproveMergeEndpoint:    transport.ApproveMerge(),
	}
	seen, err := j.user.GetTeam(ctx)
	require.NoError(t, err, "the user's session reads the team")
	require.Len(t, seen.Roles, 1)
	assert.Equal(t, team.Roles[0].Handle, seen.Roles[0].Handle)

	var folder string
	j.guide, folder = j.scriptedAgent(t, "guide")
	assert.Equal(t, j.dir, folder, "the guide works in the project")
}

// proposeAndConfirm has the guide make its draft with a first Story refresh,
// add the finished flow prices-scone to the Story the starter shipped there,
// evaluate it and propose the change with it, then the user confirm the
// proposal on the page, which makes the confirmed revision the change's agreed
// one. The guide's work stays in its draft: the project folder's own design
// file and its git status are as the starter's commit left them.
func (j *wholeJourney) proposeAndConfirm(t *testing.T) {
	story := filepath.Join(j.dir, "design", "story.go")
	shipped, err := os.ReadFile(story)
	require.NoError(t, err)
	seedCtx, seedCancel := context.WithTimeout(t.Context(), journeyStepWait)
	seed := trustedSuiteRefresh(t, seedCtx, j.guide)
	seedCancel()
	require.NotEmpty(t, seed.DraftFolder)
	require.NotEqual(t, j.dir, seed.DraftFolder)
	assert.Equal(t, filepath.Join(j.dir, journeyDraftFolder), seed.DraftFolder,
		"the guide drafts in its own folder inside the project folder")
	j.draftFolder = seed.DraftFolder
	j.proposed, j.confirmed = j.agreeChange(t, journeySconeProposal)

	assert.Empty(t, runTestGit(t, j.dir, "status", "--porcelain", "--untracked-files=all"),
		"the draft and the agreed change leave the project's git status empty")
	after, err := os.ReadFile(story)
	require.NoError(t, err)
	assert.Equal(t, string(shipped), string(after),
		"the guide's flow is in its draft, not in the project's design")
}

// startBuild has the user start the build of the confirmed revision, build 1,
// then connects the builder and the test writer with the roles and credentials
// that start registered, and has each read its assignment. The start makes
// both working copies on build 1's branches without showing in the project's
// git status, and the test writer's first read, not the start, writes the
// required tests into its copy.
func (j *wholeJourney) startBuild(t *testing.T) {
	started, err := j.user.StartBuild(stepContext(t, journeyStepWait), &gensurla.StartBuildPayload{
		ChangeID: journeyChangeID, RequestRevision: j.confirmed,
	})
	require.NoError(t, err)
	assert.Equal(t, j.confirmed, started.Build.RequestRevision)
	assert.True(t, started.Build.InForce)
	assert.Equal(t, 2, started.Build.RequiredTestCount, "one test per finished flow")
	assert.Equal(t, gensurla.TestsKindAssigned, started.Build.TestWriter.Tests.Kind())
	j.startedAt = started.Build.StartedAt
	team, ok := started.Team.AsReady()
	require.True(t, ok, "the team is ready: %s", started.Team.Kind())
	assert.Equal(t, "available", team.HerdrMode)
	require.NotEmpty(t, team.Space)
	j.space = team.Space
	handles := map[string]string{}
	for _, role := range team.Roles {
		handles[role.Role] = role.Handle
	}

	buildCopyDir := filepath.Join(j.dir, testwriter.BuilderRole.Folder)
	assert.Equal(t, j.projectCommit, strings.TrimSpace(runTestGit(t, buildCopyDir, "rev-parse", "HEAD")))
	assert.Equal(t, testwriter.BuilderRole.Branch(1),
		strings.TrimSpace(runTestGit(t, buildCopyDir, "symbolic-ref", "--short", "HEAD")))
	copyDir := filepath.Join(j.dir, testwriter.Folder)
	assert.Equal(t, j.projectCommit, strings.TrimSpace(runTestGit(t, copyDir, "rev-parse", "HEAD")))
	assert.Equal(t, testwriter.TestsRole.Branch(1),
		strings.TrimSpace(runTestGit(t, copyDir, "symbolic-ref", "--short", "HEAD")))
	assert.Empty(t, runTestGit(t, j.dir, "status", "--porcelain", "--untracked-files=all"),
		"the working copies do not show in the project's git status")

	roster := j.roster(t)
	require.Len(t, roster.Roles, 2)
	for _, role := range roster.Roles {
		assert.Equal(t, handles[role.Role], role.Handle, "the roster lists the team's %s", role.Role)
		assert.Empty(t, role.Connections)
	}
	saved, err := project.Discover(filepath.Dir(j.state.Dir), j.state.Record.Directory)
	require.NoError(t, err)
	credentials := map[string]string{}
	for handle, role := range saved.Record.Agents {
		raw, err := os.ReadFile(saved.AgentFile(handle))
		require.NoError(t, err)
		credentials[role.Role] = string(raw)
	}
	require.Len(t, credentials, 3, "the start registers both workers alongside the guide")
	assert.NotEqual(t, credentials["guide"], credentials["builder"])
	assert.NotEqual(t, credentials["guide"], credentials["test_writer"])
	assert.NotEqual(t, credentials["builder"], credentials["test_writer"])

	var folder string
	j.builder, folder = j.scriptedAgent(t, "builder")
	assert.Equal(t, buildCopyDir, folder, "the builder works in its own copy")
	j.testWriter, j.testsFolder = j.scriptedAgent(t, "test_writer")
	require.Equal(t, copyDir, j.testsFolder, "the test writer works in its own copy")

	// The start has ended: the lists and the builder's read, whose copy is
	// already on build 1's branch, are quick reads with their own wait.
	ctx := stepContext(t, journeyQuickWait)
	listed, err := gensurla.DecodeBuilderAssignmentList(
		trustedSuiteCall(t, ctx, j.builder, "list_builder_assignments", []byte(`{}`)))
	require.NoError(t, err)
	require.Len(t, listed.Assignments, 1)
	assert.Equal(t, journeyChangeID, listed.Assignments[0].ChangeID)
	built := j.readBuilderAssignment(t, ctx, journeyChangeID)
	j.builderFolder = built.WorkingFolder
	require.Equal(t, buildCopyDir, j.builderFolder)
	assert.Equal(t, j.confirmed, built.RequestRevision)
	assert.True(t, built.InForce)
	assert.True(t, built.Read)
	assert.Equal(t, gensurla.WrittenTestsKindNotYetAccepted, built.WrittenTests.Kind(),
		"the builder sees that no tests are accepted yet")

	tests, err := gensurla.DecodeTestWriterAssignmentList(
		trustedSuiteCall(t, ctx, j.testWriter, "list_test_writer_assignments", []byte(`{}`)))
	require.NoError(t, err)
	require.Len(t, tests.Assignments, 1)
	assert.Equal(t, journeyChangeID, tests.Assignments[0].ChangeID)
	// The Story table declares the required tests, and the suite generated
	// from it the hook each one calls.
	table := filepath.Join(copyDir, "eval", "design", "story_tests.go")
	suite := filepath.Join(copyDir, "eval", "gen", "evals", "shop_acceptance", "suite.go")
	const (
		sconeRow  = `{name: "prices_scone", description: `
		sconeHook = "PricesScone(context.Context) (eval.Result, error)"
	)
	assert.NotContains(t, journeyFile(t, table), sconeRow, "the start writes no required test into the copy")
	assert.NotContains(t, journeyFile(t, suite), sconeHook, "the start generates nothing in the copy")
	// The first read prepares the required tests under the server's own
	// build-start limit, so it waits as long as a start does.
	assignment := j.readTestWriterAssignment(t, stepContext(t, journeyStepWait), journeyChangeID)
	assert.Contains(t, journeyFile(t, table), sconeRow,
		"the test writer's first read writes the Story's required tests into its copy")
	assert.Contains(t, journeyFile(t, suite), sconeHook,
		"the test writer's first read generates the test module with the new flow's hook")
	assert.Equal(t, j.confirmed, assignment.RequestRevision)
	assert.Equal(t, copyDir, assignment.WorkingFolder)
	assert.Equal(t, []*gensurla.RequiredTest{
		{Story: "Prices", FlowKey: "prices-cookie", Scenario: "prices_cookie", Hook: "PricesCookie"},
		{Story: "Prices", FlowKey: "prices-scone", Scenario: "prices_scone", Hook: "PricesScone"},
	}, assignment.RequiredTests, "each finished flow requires one test, in declaration order")
	assert.Equal(t, gensurla.TestsKindAssigned, assignment.Progress.Tests.Kind())
	assert.True(t, assignment.Progress.Read)
	assert.Empty(t, runTestGit(t, j.dir, "status", "--porcelain", "--untracked-files=all"),
		"the workers' reads leave the project's git status empty")
}

// refuseEarlierReport has the test writer report its tests for the proposal's
// revision, which the build is not for; the refusal leaves its assignment as
// it was.
func (j *wholeJourney) refuseEarlierReport(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	refused := journeyRefusal(t, ctx, j.testWriter, "report_tests_finished",
		journeyReportArgs(t, journeyChangeID, j.proposed))
	assert.Contains(t, refused, fmt.Sprintf("is for request_revision %q, not %q", j.confirmed, j.proposed))
	assignment := j.readTestWriterAssignment(t, ctx, journeyChangeID)
	assert.Equal(t, gensurla.TestsKindAssigned, assignment.Progress.Tests.Kind(),
		"a refused report changes nothing")
}

// refuseUnacceptedVerification has the builder generate the agreed design in
// its copy, commit everything there with git add -A and verify that commit
// before the test writer reported: the refusal names the tests' state and the
// test writer's step, and starts no run.
func (j *wholeJourney) refuseUnacceptedVerification(t *testing.T) {
	commit := j.generateAndCommit(t, "Generate the agreed scone design")
	require.NotEqual(t, j.projectCommit, commit)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	refused := journeyRefusal(t, ctx, j.builder, "verify_change",
		journeyVerifyArgs(t, journeyChangeID, j.confirmed, commit))
	assert.Contains(t, refused, "the tests of this build are not accepted; their state is assigned")
	assert.Contains(t, refused, "the test writer writes the required tests and reports them finished")
	assert.Empty(t, listRuns(t, ctx, j.builder, journeyChangeID), "a refused verification starts no run")
}

// sendBackOutsideChange has the test writer change the README at the top of
// its working copy, outside the test module, and report. Surla's check sends
// the report back: every role that reads the build sees how many findings it
// has, only the test writer and the user read their text, and the tests
// branch stays where the build started it.
func (j *wholeJourney) sendBackOutsideChange(t *testing.T) {
	readme := filepath.Join(j.testsFolder, "README.md")
	raw, err := os.ReadFile(readme)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(readme, append(raw, "\nThe test writer's notes.\n"...), 0o644))

	build := j.reportTests(t, stepContext(t, journeyCheckWait), journeyChangeID, j.confirmed, 1)
	ctx := stepContext(t, journeyQuickWait)
	needs, ok := build.TestWriter.Tests.AsNeedsChanges()
	require.True(t, ok, "the report is sent back: %s", build.TestWriter.Tests.Kind())
	assert.Equal(t, 1, needs.FindingCount, "one file changed outside the test module")
	for role, client := range map[string]*genmcpsurla.Client{
		"guide": j.guide, "builder": j.builder, "test writer": j.testWriter,
	} {
		seen := readBuild(t, ctx, client, journeyChangeID)
		assert.Equal(t, build.TestWriter.Tests, seen.TestWriter.Tests, "the %s sees the count", role)
	}
	page, err := j.user.GetBuild(ctx, &gensurla.GetBuildPayload{ChangeID: journeyChangeID})
	require.NoError(t, err)
	shown, ok := page.Build.AsStarted()
	require.True(t, ok)
	assert.Equal(t, build.TestWriter.Tests, shown.TestWriter.Tests, "the user's page sees the count")

	findings, err := j.user.GetTestFindings(ctx, &gensurla.GetTestFindingsPayload{ChangeID: journeyChangeID})
	require.NoError(t, err)
	assert.Equal(t, j.confirmed, findings.RequestRevision)
	assert.Equal(t, 1, findings.Report)
	require.Len(t, findings.Findings, 1)
	assert.Contains(t, findings.Findings[0], "README.md is changed outside the test module eval")
	assignment := j.readTestWriterAssignment(t, ctx, journeyChangeID)
	assert.Equal(t, findings.Findings, assignment.Findings, "the test writer reads the user's findings")
	for role, client := range map[string]*genmcpsurla.Client{"guide": j.guide, "builder": j.builder} {
		raw := trustedSuiteCall(t, ctx, client, "get_build", journeyChangeArgs(journeyChangeID))
		assert.NotContains(t, string(raw), "README.md", "the %s's build holds no finding text", role)
		refused := journeyRefusal(t, ctx, client, "get_test_writer_assignment",
			journeyChangeArgs(journeyChangeID))
		assert.Contains(t, refused, "caller may not call get_test_writer_assignment", "the %s", role)
		_, err := client.ToolsCall(ctx, &genmcpsurla.ToolsCallPayload{
			Name: "get_test_findings", Arguments: journeyChangeArgs(journeyChangeID),
		})
		assert.ErrorContains(t, err, "unknown tool", "no tool gives the %s the findings", role)
	}
	assert.Equal(t, j.projectCommit, j.testsBranch(t, 1), "a report sent back records nothing")
}

// sendBackMissingHook has the test writer restore the README and report
// again before it wrote the hook the new flow requires. Surla's check builds
// the test program, which does not compile without that hook, and sends the
// report back with the compiler's finding naming it, for the test writer and
// the user; the tests branch stays where the build started it.
func (j *wholeJourney) sendBackMissingHook(t *testing.T) {
	runTestGit(t, j.testsFolder, "checkout", "--", "README.md")

	build := j.reportTests(t, stepContext(t, journeyCheckWait), journeyChangeID, j.confirmed, 2)
	ctx := stepContext(t, journeyQuickWait)
	needs, ok := build.TestWriter.Tests.AsNeedsChanges()
	require.True(t, ok, "a test program without the hook is sent back: %s", build.TestWriter.Tests.Kind())
	assert.Equal(t, 1, needs.FindingCount, "the build is the one step that failed")
	findings, err := j.user.GetTestFindings(ctx, &gensurla.GetTestFindingsPayload{ChangeID: journeyChangeID})
	require.NoError(t, err)
	assert.Equal(t, 2, findings.Report)
	require.Len(t, findings.Findings, 1)
	assert.True(t, strings.HasPrefix(findings.Findings[0], "The suite command does not compile:"),
		"the finding is the compiler's: %s", findings.Findings[0])
	assert.Contains(t, findings.Findings[0], "PricesScone", "the compiler names the missing hook")
	assignment := j.readTestWriterAssignment(t, ctx, journeyChangeID)
	assert.Equal(t, findings.Findings, assignment.Findings, "the test writer reads the compiler's finding")
	assert.Equal(t, j.projectCommit, j.testsBranch(t, 1), "a report sent back records nothing")
}

// acceptTests has the test writer write the hook the new flow requires and
// report again. Surla accepts the tests and records them on the tests branch,
// and the builder's assignment, unread again, names that commit.
func (j *wholeJourney) acceptTests(t *testing.T) {
	replaceOnce(t, filepath.Join(j.testsFolder, "eval", "acceptance", "acceptance.go"),
		journeyCookieHook, journeyCookieHook+journeySconeHook)

	build := j.reportTests(t, stepContext(t, journeyCheckWait), journeyChangeID, j.confirmed, 3)
	ctx := stepContext(t, journeyQuickWait)
	accepted, ok := build.TestWriter.Tests.AsAccepted()
	require.True(t, ok, "the tests are accepted: %s", build.TestWriter.Tests.Kind())
	require.Regexp(t, "^[0-9a-f]{40}$", accepted.Commit)
	assert.NotEqual(t, j.projectCommit, accepted.Commit, "the accepted tests are a new commit")
	assert.Equal(t, accepted.Commit, j.testsBranch(t, 1), "the accepted commit is the tests branch's tip")
	assignment := j.readTestWriterAssignment(t, ctx, journeyChangeID)
	assert.Equal(t, build.TestWriter.Tests, assignment.Progress.Tests)
	assert.Empty(t, assignment.Findings)

	listed, err := gensurla.DecodeBuilderAssignmentList(
		trustedSuiteCall(t, ctx, j.builder, "list_builder_assignments", []byte(`{}`)))
	require.NoError(t, err)
	require.Len(t, listed.Assignments, 1)
	assert.False(t, listed.Assignments[0].Read, "an acceptance makes the builder's assignment unread")
	built := j.readBuilderAssignment(t, ctx, journeyChangeID)
	shown, ok := built.WrittenTests.AsAccepted()
	require.True(t, ok, "the builder sees the accepted tests: %s", built.WrittenTests.Kind())
	assert.Equal(t, accepted, shown)
	j.testsCommit = accepted.Commit
}

// verifyAcceptedTests has the builder price scones in its assigned copy,
// commit everything there with git add -A beside the agreed design it already
// generated, and verify that commit for the agreed revision, then follows the
// run until it finishes. The run passes every scenario of the accepted tests
// and says where they came from; the test writer reads it, a caller the policy
// does not allow is refused, and the builder and the user's page see it as the
// change's latest verification, passed, with current inputs. After the pass
// the project's branch is where the starter's commit left it, the build reads
// passed with the two commits to merge and the branches they were verified
// on, and the builder has no assignment left to work on.
func (j *wholeJourney) verifyAcceptedTests(t *testing.T) {
	replaceOnce(t, filepath.Join(j.builderFolder, "internal", "service", "service.go"),
		journeyCookiePrice, journeyCookiePrice+journeySconePrice)
	commit := journeyCommitAll(t, j.builderFolder, "Price scones at $2.75")
	assert.Equal(t, j.projectCommit, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", "HEAD")),
		"the builder never moves the user's project branch")
	assert.Empty(t, runTestGit(t, j.dir, "diff", "--", "design/story.go", "internal/service/service.go"),
		"guide and builder work leave the user's source unchanged")

	run := j.passRun(t, stepContext(t, journeyOwnerWait), commit, j.testsCommit)
	ctx := stepContext(t, journeyQuickWait)
	args, err := gensurla.EncodeGetVerificationPayload(&gensurla.GetVerificationPayload{RunID: run.RunID})
	require.NoError(t, err)
	read, err := gensurla.DecodeVerificationStatusResult(
		trustedSuiteCall(t, ctx, j.testWriter, "get_verification", args))
	require.NoError(t, err)
	assert.Equal(t, run, read, "the test writer reads the run of its assignment's change")
	runs := listRuns(t, ctx, j.testWriter, journeyChangeID)
	require.Len(t, runs, 1)
	assert.Equal(t, run.RunID, runs[0].RunID)
	running, err := j.state.ReadRunning()
	require.NoError(t, err)
	management := genhttpclient.NewClient("http", j.state.Address(), credentialDoer{
		client: &http.Client{},
		header: http.Header{"Authorization": {"Bearer " + running.ManagementCredential}},
	}, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	_, err = management.GetVerification()(ctx, &gensurla.GetVerificationPayload{RunID: run.RunID})
	var refused *goa.ServiceError
	require.ErrorAs(t, err, &refused, "the management credential may not read a run")
	assert.Equal(t, "forbidden", refused.Name)
	for who, latest := range j.latestVerifications(t, ctx, journeyChangeID) {
		assert.Equal(t, &gensurla.LatestVerificationItem{RunID: run.RunID, State: "passed", InputsCurrent: true},
			latest, "the %s sees the run passed, for the revision and tests selected now", who)
	}
	j.passed = run

	assert.Equal(t, j.projectCommit, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", "HEAD")),
		"a passed run does not move the user's project branch")
	j.assertPassedTaking(t, ctx)
	assert.Empty(t, j.builderWork(t, ctx), "a build with a current passed run needs the builder no more")
}

// approvePassedRun has the user approve the passing run on the page, with the
// call the page's Approve control makes. The project has no promotion
// settings, so the page offers approval and the approval records no binary
// digests; it names the builder's commit, the accepted tests' commit, the
// suite and environment the run's report declared, and where the tests came
// from. The user's next read of the change returns the same approval.
func (j *wholeJourney) approvePassedRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	list, err := j.user.ListChanges(ctx)
	require.NoError(t, err)
	assert.True(t, list.ApprovalAvailable, "the page offers approval without promotion settings")

	approved, err := j.user.ApproveMerge(ctx, j.approvePayload(t))
	require.NoError(t, err)
	completed, ok := j.passed.State.AsCompleted()
	require.True(t, ok)
	want := &gensurla.ApprovalItem{
		ApprovedBy:      j.state.Record.Owner,
		ReportDigest:    completed.Report.ReportDigest,
		CandidateCommit: j.passed.CandidateCommit,
		SuiteID:         completed.SuiteID,
		SuiteCommit:     j.testsCommit,
		Environment:     completed.Environment,
		Artifacts:       map[string]string{},
		ScenarioCount:   len(journeyScenarios),
		TestSource: gensurla.NewApprovalTestSourceWrittenTests(&gensurla.AgentWrittenTests{
			Commit: j.testsCommit, Notice: journeyWrittenTestsNotice,
		}),
	}
	assert.Equal(t, j.confirmed, approved.RequestRevision, "approval does not change the revision")
	assert.Equal(t, want, approved.Approval)
	read, err := j.user.GetChange(ctx, &gensurla.GetChangePayload{ChangeID: journeyChangeID})
	require.NoError(t, err)
	assert.Equal(t, want, read.Approval, "the user's next read of the change returns the approval")
	assert.Nil(t, read.UnverifiedApproval)
}

// acceptLaterTests has the test writer change a comment in its test module
// after the passing run was approved and report again. Surla accepts and
// records the changed tests at a new commit, and the builder's assignment
// names it; the earlier run, which ran the earlier tests, stays the change's
// latest verification, still passed, and the builder and the user's page see
// that its inputs are no longer current. The user's read of the change no
// longer shows the approval of the earlier tests, and approving that run
// again is refused with the builder's step.
func (j *wholeJourney) acceptLaterTests(t *testing.T) {
	replaceOnce(t, filepath.Join(j.testsFolder, "eval", "acceptance", "acceptance.go"),
		"// PricesScone is the test the Story's finished flow prices-scone requires.\n",
		"// PricesScone is the test the Story's finished flow prices-scone requires:\n"+
			"// the helper answers a scone's price from the catalog.\n")

	build := j.reportTests(t, stepContext(t, journeyCheckWait), journeyChangeID, j.confirmed, 4)
	ctx := stepContext(t, journeyQuickWait)
	accepted, ok := build.TestWriter.Tests.AsAccepted()
	require.True(t, ok, "the changed tests are accepted: %s", build.TestWriter.Tests.Kind())
	assert.NotEqual(t, j.testsCommit, accepted.Commit, "Surla records the changed tests anew")
	assert.Equal(t, accepted.Commit, j.testsBranch(t, 1))
	assert.Equal(t, j.testsCommit, strings.TrimSpace(runTestGit(t, j.dir, "rev-parse", accepted.Commit+"^")),
		"the new tests follow the earlier ones on the tests branch")

	listed, err := gensurla.DecodeBuilderAssignmentList(
		trustedSuiteCall(t, ctx, j.builder, "list_builder_assignments", []byte(`{}`)))
	require.NoError(t, err)
	require.Len(t, listed.Assignments, 1)
	assert.False(t, listed.Assignments[0].Read, "the new acceptance makes the builder's assignment unread")
	shown, ok := j.readBuilderAssignment(t, ctx, journeyChangeID).WrittenTests.AsAccepted()
	require.True(t, ok)
	assert.Equal(t, accepted.Commit, shown.Commit, "the builder sees the new tests")

	args, err := gensurla.EncodeGetVerificationPayload(&gensurla.GetVerificationPayload{RunID: j.passed.RunID})
	require.NoError(t, err)
	earlier, err := gensurla.DecodeVerificationStatusResult(
		trustedSuiteCall(t, ctx, j.builder, "get_verification", args))
	require.NoError(t, err)
	completed, ok := earlier.State.AsCompleted()
	require.True(t, ok)
	assert.True(t, completed.Report.Passed, "the earlier run still reads passed")
	assert.Equal(t, j.testsCommit, earlier.SuiteCommit, "it ran the earlier tests")
	notCurrent := &gensurla.LatestVerificationItem{RunID: j.passed.RunID, State: "passed", InputsCurrent: false}
	for who, latest := range j.latestVerifications(t, ctx, journeyChangeID) {
		assert.Equal(t, notCurrent, latest, "the %s sees the earlier run passed, for tests no longer selected", who)
	}

	read, err := j.user.GetChange(ctx, &gensurla.GetChangePayload{ChangeID: journeyChangeID})
	require.NoError(t, err)
	assert.Nil(t, read.Approval, "the approval of the earlier tests is no longer shown")
	assert.Nil(t, read.UnverifiedApproval)
	_, err = j.user.ApproveMerge(ctx, j.approvePayload(t))
	var refused *goa.ServiceError
	require.ErrorAs(t, err, &refused, "approving the run of the earlier tests is refused")
	assert.Equal(t, "unprocessable", refused.Name)
	assert.Contains(t, refused.Message, "the report ran the tests at "+j.testsCommit)
	assert.Contains(t, refused.Message, accepted.Commit, "the refusal names the accepted tests")
	assert.Contains(t, refused.Message, "the builder verifies the candidate again")
}
