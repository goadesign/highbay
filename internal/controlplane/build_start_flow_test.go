// This file proves ordered build admission and its pre-save/post-save boundary.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/designeval"
	"github.com/goadesign/highbay/internal/scaffold"
	"github.com/goadesign/highbay/internal/shquote"
	"github.com/goadesign/highbay/internal/testwriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestStartBuildOrdersItsOwners observes the saved record at role registration,
// the saved Story at generation, and the released turn at the window boundary.
func TestStartBuildOrdersItsOwners(t *testing.T) {
	f := &teamServiceFixture{}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: "alice@example.com", Service: f}})
	card := saveBuildStoryCard(t, cp, "chg", StatusConfirmed, refundStory("Agreed flow"))
	cp.mu.Lock()
	record, _, err := cp.cards.load("chg")
	require.NoError(t, err)
	record.designSource = `{"design/design.go":"package design // agreed\n"}`
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
	require.NoError(t, err)
	err = cp.cards.save(record)
	cp.mu.Unlock()
	require.NoError(t, err)
	card = attachBuildSnapshot(t, cp, "chg")
	record = loadMetadataRecord(t, cp, "chg")
	var events []string
	copy := &buildCopyFixture{folder: fixtureTestWriterFolder(t, cp)}
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		if len(events) == 0 {
			assertNoBuildSaved(t, cp, "chg")
		}
		events = append(events, "open")
		return copy, nil
	}
	f.register = func(_ context.Context, folders BuildFolders) error {
		events = append(events, "register")
		workspace, _, _ := cp.currentWorkspaceTarget()
		assert.Equal(t, BuilderAndTestWriter{
			BuilderFolder:    filepath.Join(workspace, testwriter.BuilderRole.Folder),
			TestWriterFolder: copy.folder,
		}, folders)
		saved, found, err := cp.cards.loadOpenBuild()
		require.NoError(t, err)
		require.True(t, found, "registration follows the durable save")
		assert.Equal(t, record.designSource, saved.DesignSource)
		assert.Equal(t, refundStory("Agreed flow"), saved.Stories)
		assert.Equal(t, testsAssigned, saved.TestWriter.State)
		return nil
	}
	copy.prepare = func(_ context.Context, stories []designeval.StorySummary) error {
		events = append(events, "prepare")
		assert.Equal(t, refundStory("Agreed flow"), stories)
		return nil
	}
	f.start = func(ctx context.Context) (*genhighbay.ProjectTeam, error) {
		events = append(events, "start")
		select {
		case cp.workingCopyTurn <- struct{}{}:
			<-cp.workingCopyTurn
		default:
			t.Error("window work retained the copy turn")
		}
		_, err := cp.GetBuild(ctx, &genhighbay.GetBuildPayload{ChangeID: "chg"})
		assert.NoError(t, err, "window work retains neither the card lock nor the phase deadline")
		_, bounded := ctx.Deadline()
		assert.False(t, bounded, "StartAgents receives its own server-owned lifetime")
		return &genhighbay.ProjectTeam{Team: "team", Roles: []*genhighbay.ProjectTeamRole{}, Step: "Read the fixture assignments."}, nil
	}
	result, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	assert.Equal(t, []string{"open", "register", "start"}, events)
	assert.Equal(t, 1, result.Build.RequiredTestCount)
	assert.Equal(t, genhighbay.TeamKindReady, result.Team.Kind())
	assignment, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &genhighbay.GetTestWriterAssignmentPayload{ChangeID: "chg"})
	require.NoError(t, err)
	assert.Equal(t, []string{"open", "register", "start", "open", "prepare"}, events)
	assert.Equal(t, record.designSource, assignment.DesignSource)
	assert.Equal(t, copy.folder, assignment.WorkingFolder)
}

// TestStartBuildKeepsPostSaveOutcomes distinguishes author findings, operational
// failures and reports made while required tests are being generated on the
// marking read.
func TestStartBuildKeepsPostSaveOutcomes(t *testing.T) {
	for _, scenario := range []string{"author", "operational", "reported during generation"} {
		t.Run(scenario, func(t *testing.T) {
			cp := newBuildControlPlane(t, Options{})
			card := buildableChange(t, cp, "chg")
			copy := &buildCopyFixture{folder: fixtureTestWriterFolder(t, cp)}
			cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) { return copy, nil }
			copy.prepare = func(context.Context, []designeval.StorySummary) error {
				if scenario == "operational" {
					return errors.New("private setup failure")
				}
				if scenario == "reported during generation" {
					_, err := cp.ReportTestsFinished(asCaller("alice@example.com", CallerTestWriter), &genhighbay.ReportTestsFinishedPayload{ChangeID: "chg", RequestRevision: card.RequestRevision, CoverageStatements: []*genhighbay.TestCoverageStatement{}})
					require.NoError(t, err)
				}
				return &testwriter.AuthorError{Finding: testwriter.Finding{Kind: testwriter.FindingGeneration, Message: "The test design does not compile."}}
			}
			_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
			require.NoError(t, err)
			assignment, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &genhighbay.GetTestWriterAssignmentPayload{ChangeID: "chg"})
			require.NoError(t, err, "a preparation failure on the marking read never refuses the read")
			saved := mustStartedBuild(t, cp, asCaller("alice@example.com", CallerUser), "chg")
			assert.Equal(t, assignment.Progress, saved.TestWriter)
			switch scenario {
			case "author":
				finding, ok := saved.TestWriter.Tests.AsNeedsChanges()
				require.True(t, ok)
				assert.Equal(t, 1, finding.FindingCount)
				assert.NotEmpty(t, finding.CheckedAt)
				require.Len(t, assignment.Findings, 1, "the test writer reads the finding's text")
				assert.True(t, strings.HasSuffix(assignment.Findings[0], "Correct the problem in the test module, then report_tests_finished."))
			case "operational":
				failure, ok := saved.TestWriter.Tests.AsCouldNotRun()
				require.True(t, ok)
				assert.NotEmpty(t, failure.CheckedAt)
				assert.NotContains(t, failure.Reason, "private setup failure")
				assert.NotContains(t, failure.Reason, "Code generation failed", "no generator failed")
				assert.Equal(t, "Build this change again on the views page.", failure.Step)
			case "reported during generation":
				assert.Equal(t, genhighbay.TestsKindReported, saved.TestWriter.Tests.Kind())
				assert.Equal(t, 1, saved.TestWriter.Report)
			}
		})
	}
}

// TestAPreparationThatCouldNotRunSaysWhatFailed makes the preparation of a
// saved build fail on the marking read for causes the test writer cannot fix.
// Only a failure of the generator is worded as one. A generated folder that
// could not be written says so, with the cause and a step that clears it; a
// failure no case names gives no private text, and its cause is on the read's
// span. Each reason holds no absolute path and each step fits the contract.
func TestAPreparationThatCouldNotRunSaysWhatFailed(t *testing.T) {
	const buildAgain = "Build this change again on the views page."
	cases := []struct {
		name string
		// prepareErr is what the preparation returns.
		prepareErr error
		// wantReason and notReason are fragments the saved reason holds and
		// does not hold.
		wantReason, notReason []string
		wantStep              string
	}{
		{
			name: "the generated folder cannot be written",
			prepareErr: fmt.Errorf("%w: create staging directory: mkdirat /Users/someone/shop/.highbay-tests/eval/.highbay-gen-abc: no space left on device",
				testwriter.ErrWriteGenerated),
			wantReason: []string{"could not write them into the test writer's working copy", "create staging directory", "no space left on device"},
			notReason:  []string{"Code generation failed", "/Users/someone"},
			wantStep:   stepWriteCopy + ", then build this change again on the views page.",
		},
		{
			name: "the dependencies cannot be downloaded where Highbay runs",
			prepareErr: fmt.Errorf("%w, and its go.mod and go.sum are those of the project's commit %s, so the cause is not in the test writer's changes",
				testwriter.ErrDependencies, strings.Repeat("b", 40)),
			wantReason: []string{"could not prepare the required tests", "dependencies could not be downloaded where Highbay runs", "commit " + strings.Repeat("b", 40)},
			notReason:  []string{"Code generation failed"},
			wantStep:   stepDependencies + ", then build this change again on the views page.",
		},
		{
			name:       "the generator fails before it reaches the module",
			prepareErr: fmt.Errorf("%w: create design evaluator workspace: mkdir /private/var/highbay: permission denied", testwriter.ErrGeneration),
			wantReason: []string{"Code generation failed while starting the design compiler."},
			notReason:  []string{"/private/var"},
			wantStep:   buildAgain,
		},
		{
			name:       "a failure no case names",
			prepareErr: errors.New("private setup failure in /Users/someone/shop"),
			wantReason: []string{"could not prepare the required tests", "server trace of the test writer's assignment read"},
			notReason:  []string{"Code generation failed", "private setup failure", "/Users/someone"},
			wantStep:   buildAgain,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
			cp := newBuildControlPlane(t, Options{TracerProvider: provider})
			card := buildableChange(t, cp, "chg")
			copy := &buildCopyFixture{folder: fixtureTestWriterFolder(t, cp)}
			cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) { return copy, nil }
			copy.prepare = func(context.Context, []designeval.StorySummary) error { return tc.prepareErr }

			_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
			require.NoError(t, err)
			assignment, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &genhighbay.GetTestWriterAssignmentPayload{ChangeID: "chg"})
			require.NoError(t, err, "a failed preparation never refuses the marking read")
			failure, ok := assignment.Progress.Tests.AsCouldNotRun()
			require.True(t, ok)
			for _, want := range tc.wantReason {
				assert.Contains(t, failure.Reason, want)
			}
			for _, not := range tc.notReason {
				assert.NotContains(t, failure.Reason, not)
			}
			assert.LessOrEqual(t, utf8.RuneCountInString(failure.Reason), maxCheckReason)
			assert.Equal(t, tc.wantStep, failure.Step)
			assert.True(t, boundedText(failure.Step, maxCheckStep), "the step fits the contract")
			span := waitForSpan(t, exporter, "highbay.controlplane.get_test_writer_assignment")
			assert.Equal(t, codes.Error, span.Status.Code, "the failure is an error on the read's span")
			require.NotEmpty(t, span.Events)
			var recorded string
			for _, event := range span.Events {
				for _, attr := range event.Attributes {
					if attr.Key == "exception.message" {
						recorded = attr.Value.AsString()
					}
				}
			}
			assert.Equal(t, tc.prepareErr.Error(), recorded, "the trace keeps the cause as it was")
		})
	}
}

// TestStartBuildRefusalsReachTheHTTPConsumer exercises all seven ordinary named
// errors, their status, actionable suffix, and the absence of saved/team effects.
func TestStartBuildRefusalsReachTheHTTPConsumer(t *testing.T) {
	for _, name := range []string{"not_repository", "no_commit", "no_test_layout", "git_identity_missing", "copy_folder_in_use", "tests_branch_in_use", "working_copy_busy"} {
		t.Run(name, func(t *testing.T) {
			cp := newBuildControlPlane(t, Options{})
			card := buildableChange(t, cp, "chg")
			workspace, _, _ := cp.currentWorkspaceTarget()
			step := ""
			var branchMessage string
			var openErr error
			switch name {
			case "not_repository":
				openErr = testwriter.ErrNotRepository
				step = "run git init in the project folder and commit its files, then try again"
			case "no_commit":
				openErr = testwriter.ErrNoCommit
				step = "commit the project's files, then try again"
			case "no_test_layout":
				require.NoError(t, os.Remove(filepath.Join(workspace, scaffold.SuiteDir, "go.mod")))
				step = "Store verification settings, then build again."
			case "git_identity_missing":
				cp.buildIdentityCheck = func(context.Context, string) error { return testwriter.ErrIdentityMissing }
				step = "configure Git's name and address, then try again"
			case "copy_folder_in_use":
				openErr = fmt.Errorf("the project's %s entry is not Highbay's working copy (%s is not a folder): move it out of the project, then try again: %w",
					testwriter.Folder, shquote.Quote(filepath.Join(workspace, testwriter.Folder)), testwriter.ErrFolderInUse)
				step = "move it out of the project, then try again"
			case "tests_branch_in_use":
				openErr = fmt.Errorf("%w: %s has it checked out; switch that folder to another branch, then try again", testwriter.ErrBranchInUse, shquote.Quote(filepath.Join(workspace, "other working copy")))
				branchMessage = openErr.Error()
				step = "switch that folder to another branch, then try again"
			case "working_copy_busy":
				cp.workingCopyTurn <- struct{}{}
				defer func() { <-cp.workingCopyTurn }()
				step = "Start the build again when that has ended."
			}
			cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
				if openErr == nil {
					t.Fatal("a refused preflight opened the working copy")
				}
				return nil, openErr
			}
			server := httptest.NewServer(NewHTTPHandler(cp, func(*http.Request) (Principal, error) {
				return Principal{ID: "alice@example.com", Kind: CallerUser}, nil
			}))
			defer server.Close()
			status, body := httpCall(t, server.URL, "fixture", http.MethodPost, "/changes/chg/build", `{"request_revision":"`+card.RequestRevision+`"}`)
			assert.Equal(t, http.StatusConflict, status, body)
			var refusal struct{ Name, Message string }
			require.NoError(t, json.Unmarshal([]byte(body), &refusal))
			assert.Equal(t, name, refusal.Name)
			assert.True(t, strings.HasSuffix(refusal.Message, step), refusal.Message)
			if name == "copy_folder_in_use" {
				assert.Contains(t, refusal.Message, shquote.Quote(filepath.Join(workspace, testwriter.Folder)))
			}
			if name == "tests_branch_in_use" {
				assert.Equal(t, branchMessage, refusal.Message, "the library's recovery text is propagated unchanged")
			}
			assertNoBuildSaved(t, cp, "chg")
			assert.Empty(t, cp.team.Service.(*teamServiceFixture).calls)
		})
	}
}

// TestStartBuildOwnerPrecedesTheTurn denies another owner even while the owner
// is busy, without saving a build.
func TestStartBuildOwnerPrecedesTheTurn(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := buildableChange(t, cp, "chg")
	cp.workingCopyTurn <- struct{}{}
	_, err := cp.StartBuild(asCaller("bob@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	assertRefused(t, err, "forbidden")
	<-cp.workingCopyTurn
	assertNoBuildSaved(t, cp, "chg")
}

// TestStartBuildStoredEvaluatorBypassesWriterPrerequisites starts with only the
// builder even when the test-writer copy prerequisites would fail, checks Git
// identity (which every build needs for its builder copy), and takes both copy
// turns.
func TestStartBuildStoredEvaluatorBypassesWriterPrerequisites(t *testing.T) {
	f := &teamServiceFixture{}
	cp := newBuildControlPlane(t, Options{BuildCacheDir: t.TempDir(),
		Verification: &VerificationConfig{RunsDir: t.TempDir()},
		Team:         &TeamBinding{Owner: "alice@example.com", Service: f},
	})
	card := buildableChange(t, cp, "chg")
	cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) {
		t.Error("stored evaluator opened a writer copy")
		return nil, testwriter.ErrNotRepository
	}
	f.register = func(_ context.Context, folders BuildFolders) error {
		workspace, _, _ := cp.currentWorkspaceTarget()
		assert.Equal(t, BuilderOnly{BuilderFolder: filepath.Join(workspace, testwriter.BuilderRole.Folder)}, folders)
		return nil
	}
	cp.buildIdentityCheck = func(context.Context, string) error {
		return testwriter.ErrIdentityMissing
	}
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	assertRefused(t, err, "git_identity_missing")
	assertNoBuildSaved(t, cp, "chg")
	cp.buildIdentityCheck = func(context.Context, string) error { return nil }

	cp.builderCopyTurn <- struct{}{}
	_, err = cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	assertRefused(t, err, "working_copy_busy")
	assert.ErrorContains(t, err, "builder is reading its assignment")
	assertNoBuildSaved(t, cp, "chg")
	<-cp.builderCopyTurn

	cp.workingCopyTurn <- struct{}{}
	_, err = cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	assertRefused(t, err, "working_copy_busy")
	assert.NotContains(t, err.Error(), "test check")
	assertNoBuildSaved(t, cp, "chg")
	<-cp.workingCopyTurn
	result, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	assert.Equal(t, genhighbay.TestsKindNotUsed, result.Build.TestWriter.Tests.Kind())
	assert.Equal(t, []string{"register", "start"}, f.calls)
}

// TestStartBuildTeamWarningsKeepTheSavedBuild checks that a team that cannot
// become ready after registration leaves the build saved, and that the
// owner's typed cause selects the step start_build returns.
func TestStartBuildTeamWarningsKeepTheSavedBuild(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		// step is the exact step expected, or empty when only the kind matters.
		step string
	}{
		{name: "unavailable", err: genhighbay.MakeTeamUnavailable(errors.New("fixture unavailable"))},
		{
			name: "unavailable herdr-core",
			err:  genhighbay.MakeTeamUnavailable(&genproject.TeamUnavailable{Name: "team_unavailable", Cause: "hc"}),
			step: buildTeamStep("hc"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &teamServiceFixture{start: func(context.Context) (*genhighbay.ProjectTeam, error) { return nil, tc.err }}
			cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: "alice@example.com", Service: f}})
			card := buildableChange(t, cp, "chg")
			result, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
			require.NoError(t, err)
			unavailable, ok := result.Team.AsUnavailable()
			require.True(t, ok, "the team outcome is %s", result.Team.Kind())
			if tc.step != "" {
				assert.Equal(t, tc.step, string(unavailable.Step))
			}
			assert.Equal(t, []string{"register", "start"}, f.calls)
			assert.Equal(t, result.Build, mustStartedBuild(t, cp, asCaller("alice@example.com", CallerUser), "chg"))
			assert.NotContains(t, string(readBuildFile(t, cp, "chg")), string(unavailable.Step),
				"the team's failure is answered, never saved with the build")
		})
	}
}

// TestStartBuildOwnsItsAcceptedLifetime drives the real start and marking read
// while the client disconnects or the server stops after saving. Close drains
// accepted preparation before stores close; reopening proves the interrupted
// verdict and role mode persisted.
func TestStartBuildOwnsItsAcceptedLifetime(t *testing.T) {
	for _, stopServer := range []bool{false, true} {
		t.Run(fmt.Sprint("server_stop=", stopServer), func(t *testing.T) {
			cards := t.TempDir()
			cp := newBuildControlPlane(t, Options{CardsDir: cards})
			card := buildableChange(t, cp, "chg")
			entered := make(chan context.Context, 1)
			release := make(chan struct{})
			copy := &buildCopyFixture{folder: fixtureTestWriterFolder(t, cp), prepare: func(ctx context.Context, _ []designeval.StorySummary) error {
				entered <- ctx
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-release:
					return nil
				case <-time.After(3 * time.Second):
					return errors.New("fixture generation was not released")
				}
			}}
			cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) { return copy, nil }
			started, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
			require.NoError(t, err)
			assert.Equal(t, genhighbay.TeamKindReady, started.Team.Kind())

			request, disconnect := context.WithCancel(asCaller("alice@example.com", CallerTestWriter))
			defer disconnect()
			type outcome struct {
				result *genhighbay.TestWriterAssignment
				err    error
			}
			finished := make(chan outcome, 1)
			go func() {
				result, err := cp.GetTestWriterAssignment(request, &genhighbay.GetTestWriterAssignmentPayload{ChangeID: "chg"})
				finished <- outcome{result: result, err: err}
			}()
			var accepted context.Context
			select {
			case accepted = <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("read did not reach generation")
			}
			disconnect()
			assert.NoError(t, accepted.Err(), "client disconnection does not cancel accepted generation")
			if stopServer {
				require.NoError(t, cp.Close())
			} else {
				close(release)
			}
			select {
			case got := <-finished:
				require.NoError(t, got.err)
				if stopServer {
					assert.Equal(t, genhighbay.TestsKindCouldNotRun, got.result.Progress.Tests.Kind())
					reopened := newBuildControlPlane(t, Options{CardsDir: cards})
					persisted := mustStartedBuild(t, reopened, asCaller("alice@example.com", CallerUser), "chg")
					assert.Equal(t, got.result.Progress, persisted.TestWriter)
				} else {
					assert.Equal(t, genhighbay.TestsKindAssigned, got.result.Progress.Tests.Kind())
				}
			case <-time.After(3 * time.Second):
				t.Fatal("accepted read did not drain")
			}
		})
	}
}

// TestStartBuildDeadlineBoundaries advances virtual time one nanosecond below,
// at and above the production five-minute deadline. Real filesystem effects and
// the ordinary context timer still run; no real deadline sleep occurs.
func TestStartBuildDeadlineBoundaries(t *testing.T) {
	for _, elapsed := range []time.Duration{defaultBuildStartTimeout - time.Nanosecond, defaultBuildStartTimeout, defaultBuildStartTimeout + time.Nanosecond} {
		t.Run(elapsed.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				cp := newBuildControlPlane(t, Options{})
				cp.buildStartWithTimeout = func(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
					assert.Equal(t, 5*time.Minute, limit)
					return context.WithTimeout(ctx, limit)
				}
				card := buildableChange(t, cp, "chg")
				copy := &buildCopyFixture{folder: fixtureTestWriterFolder(t, cp)}
				cp.buildCopyFactory = func(context.Context, string, scaffold.Layout) (buildCopy, error) { return copy, nil }
				copy.prepare = func(ctx context.Context, _ []designeval.StorySummary) error {
					time.Sleep(elapsed)
					synctest.Wait()
					if ctx.Err() != nil {
						// A real subprocess may return an opaque exit error. The
						// phase context, rather than that error, owns the reason.
						return errors.New("generation exited")
					}
					return nil
				}
				_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
				require.NoError(t, err)
				assert.Equal(t, []string{"register", "start"}, cp.team.Service.(*teamServiceFixture).calls)
				assignment, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &genhighbay.GetTestWriterAssignmentPayload{ChangeID: "chg"})
				require.NoError(t, err)
				want := genhighbay.TestsKindCouldNotRun
				if elapsed < defaultBuildStartTimeout {
					want = genhighbay.TestsKindAssigned
				}
				assert.Equal(t, want, assignment.Progress.Tests.Kind())
				if failure, ok := assignment.Progress.Tests.AsCouldNotRun(); ok {
					assert.Equal(t, "The time limit for reading the test writer's assignment ended before the required tests could be prepared.", failure.Reason)
				}
			})
		})
	}
	// A phase that ends before saving is a retryable refusal and leaves nothing.
	cp := newBuildControlPlane(t, Options{})
	cp.buildStartWithTimeout = func(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
		assert.Equal(t, 5*time.Minute, limit)
		ended, cancel := context.WithCancel(ctx)
		cancel()
		return ended, cancel
	}
	card := buildableChange(t, cp, "chg")
	_, err := cp.StartBuild(asCaller("alice@example.com", CallerUser), &genhighbay.StartBuildPayload{ChangeID: "chg", RequestRevision: card.RequestRevision})
	assertRefused(t, err, "unavailable")
	assertNoBuildSaved(t, cp, "chg")
}
