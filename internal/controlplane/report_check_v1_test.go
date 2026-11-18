// These tests exercise report acceptance's Git ownership and Drop boundaries
// without generating, compiling or executing candidate code.
package controlplane

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

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestReportV1DropDuringCheckSavesNothing covers both an accepted check and a
// check with findings. Drop keeps the open build's identity but forbids either
// result from committing or changing its saved report after the stop.
func TestReportV1DropDuringCheckSavesNothing(t *testing.T) {
	for _, accepts := range []bool{false, true} {
		name := "findings"
		if accepts {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			checking := make(chan int, 1)
			release := make(chan struct{})
			tests := &standInCopy{check: func(ctx context.Context, call int) (testwriter.Verdict, error) {
				checking <- call
				select {
				case <-release:
				case <-ctx.Done():
					return testwriter.Verdict{}, ctx.Err()
				}
				if !accepts {
					return needsChanges("Fix the authored tests"), nil
				}
				return testwriter.Verdict{}, nil
			}}
			exporter := tracetest.NewInMemoryExporter()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
			t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
			cp, revision := newReportFixture(t, Options{TracerProvider: provider}, tests)
			readBothAssignments(t, cp)
			reportTests(t, cp, revision)
			require.Equal(t, 1, receive(t, checking))
			build := mustOpenBuildFile(t, cp)
			_, err := cp.DropBuild(asCaller("alice@example.com", CallerUser), &gensurla.DropBuildPayload{
				ChangeID: build.ChangeID, StartedAt: verdictTime(build.StartedAt),
			})
			require.NoError(t, err)
			before := readBuildFile(t, cp, build.ChangeID)
			close(release)
			span := waitForSpan(t, exporter, "surla.controlplane.check_reported_tests")
			require.Len(t, span.Events, 1)
			assert.Equal(t, replacedEvent, span.Events[0].Name)
			assert.Equal(t, before, readBuildFile(t, cp, build.ChangeID))
			assert.Zero(t, tests.recordCalls(), "Drop must win over an accepted check")
			assert.Empty(t, cp.reports.reported(t.Context()), "restart selection must not pick a dropped report")
			cp.reports.judge(t.Context(), build.ChangeID)
			assert.Equal(t, 1, tests.checked(), "direct judgment selection must skip the dropped start")
		})
	}
}

// TestReportV1TipGateChecksTheRealCopy reports through HTTP, changes the real
// copy during a paused check, then reads actual saved findings over HTTP/MCP.
// IsBase remains valid for tests, as do own-build branches and work in B.
func TestReportV1TipGateChecksTheRealCopy(t *testing.T) {
	for _, scenario := range []string{"wrong branch", "detached", "operation", "foreign", "zero-file foreign", "unrelated tip", "tests at B", "same build branch", "other branch at B"} {
		t.Run(scenario, func(t *testing.T) {
			checking := make(chan int, 1)
			release := make(chan struct{})
			tests := &standInCopy{check: func(ctx context.Context, call int) (testwriter.Verdict, error) {
				checking <- call
				select {
				case <-release:
					return testwriter.Verdict{}, nil
				case <-ctx.Done():
					return testwriter.Verdict{}, ctx.Err()
				}
			}}
			cp, revision := newReportFixture(t, Options{}, tests)
			readBothAssignments(t, cp)
			build := mustOpenBuildFile(t, cp)
			workspace := cp.currentWorkspaceDir()
			folder := filepath.Join(workspace, testwriter.TestsRole.Folder)
			writeWorkspaceFiles(t, folder, map[string]string{"eval/own_test.txt": "Own test work, not committed.\n"})
			server := roleServer(t, cp)
			status, body := httpCall(t, server.URL, "tests", http.MethodPost, "/assignments/test-writer/chg/finished", fmt.Sprintf(`{"request_revision":%q,"coverage_statements":[]}`, revision))
			require.Equal(t, http.StatusOK, status, body)
			require.Equal(t, 1, receive(t, checking))
			want := ""
			foreign := ""
			checkedTip := build.Base
			switch scenario {
			case "wrong branch":
				productionBuildGit(t, folder, "switch", "-q", "-c", "side-work")
				want = "side-work"
			case "detached":
				productionBuildGit(t, folder, "switch", "-q", "--detach")
				want = "detached HEAD"
			case "operation":
				gitDir := productionBuildGit(t, folder, "rev-parse", "--absolute-git-dir")
				require.NoError(t, os.WriteFile(filepath.Join(gitDir, "MERGE_HEAD"), []byte(build.Base+"\n"), 0o600))
				want = "abort it"
			case "foreign", "zero-file foreign", "same build branch":
				other := testwriter.BuilderRole.Branch(2)
				otherWork := workspace
				projectBranch := ""
				if scenario == "same build branch" {
					copy, found, err := testwriter.OpenCopy(t.Context(), workspace, testwriter.BuilderRole)
					require.NoError(t, err)
					require.True(t, found)
					otherWork = copy.Project()
				} else {
					projectBranch = productionBuildGit(t, workspace, "symbolic-ref", "--short", "HEAD")
					productionBuildGit(t, workspace, "checkout", "-q", "-b", other, build.Base)
				}
				if scenario != "zero-file foreign" {
					files := map[string]string{"eval/a copy.txt": "Other branch's tests.\n"}
					for i := range 5 {
						files[fmt.Sprintf("eval/file%d.txt", i)] = "Other branch's tests.\n"
					}
					writeWorkspaceFiles(t, otherWork, files)
					productionBuildGit(t, otherWork, "add", "eval")
				}
				productionBuildGit(t, otherWork, "commit", "-q", "--allow-empty", "-m", "Other branch work")
				foreign = productionBuildGit(t, otherWork, "rev-parse", "HEAD")
				if projectBranch != "" {
					productionBuildGit(t, workspace, "checkout", "-q", projectBranch)
				}
				productionBuildGit(t, folder, "merge", "--no-edit", foreign)
				checkedTip = productionBuildGit(t, workspace, "rev-parse", "refs/heads/surla/tests-1")
				if scenario != "same build branch" {
					want = "merges work the user has not taken"
				}
			case "unrelated tip":
				tip := productionBuildGit(t, workspace, "commit-tree", build.Base+"^{tree}", "-m", "Unrelated root")
				productionBuildGit(t, folder, "reset", "--hard", tip)
				want = "does not descend from this build's base " + build.Base
			case "other branch at B":
				productionBuildGit(t, workspace, "branch", "surla/build-2", build.Base)
			}
			before := productionBuildGit(t, workspace, "rev-parse", "refs/heads/surla/tests-1")
			close(release)
			if want == "" {
				accepted := waitForTests(t, cp, gensurla.TestsKindAccepted)
				assert.Equal(t, 1, accepted.TestWriter.Report)
				assert.Equal(t, []string{checkedTip}, tests.recordedParents())
				assert.Equal(t, 1, tests.recordCalls())
				return
			}
			buildResult := waitForTests(t, cp, gensurla.TestsKindNeedsChanges)
			assert.Equal(t, 1, buildResult.TestWriter.Report)
			assert.Zero(t, tests.recordCalls())
			assert.Equal(t, before, productionBuildGit(t, workspace, "rev-parse", "refs/heads/surla/tests-1"))
			status, body = httpCall(t, server.URL, "user", http.MethodGet, "/changes/chg/build/findings", "")
			require.Equal(t, http.StatusOK, status, body)
			assert.Contains(t, body, want)
			assert.NotContains(t, body, workspace)
			if foreign != "" {
				assert.Contains(t, body, foreign)
				assert.Contains(t, body, "hold neither the other builds' commits nor their changes to the named files")
				assert.Contains(t, body, "Make your own tests again")
				if scenario == "foreign" {
					assert.Contains(t, body, "6 files")
					assert.Contains(t, body, "'eval/a copy.txt'")
					assert.NotContains(t, body, "eval/file4.txt", "L1 still lists only its first five files")
				} else {
					assert.NotContains(t, body, "0 files")
				}
			}
			// Satisfy the branch/operation goal so a marking read can return the
			// saved finding through the real test-writer MCP tool.
			if scenario == "operation" {
				gitDir := productionBuildGit(t, folder, "rev-parse", "--absolute-git-dir")
				require.NoError(t, os.Remove(filepath.Join(gitDir, "MERGE_HEAD")))
			} else if scenario == "wrong branch" || scenario == "detached" {
				productionBuildGit(t, folder, "switch", "-q", "surla/tests-1")
			}
			failure, result := mcpToolCall(t, server.URL, "tests", "get_test_writer_assignment", `{"change_id":"chg"}`)
			require.Empty(t, failure)
			assert.JSONEq(t, jsonAt(t, []byte(body), "findings"), jsonAt(t, result, "findings"))
			assert.Zero(t, tests.recordCalls())
		})
	}
}

// TestReportV1RetainsADeletedDroppedBranch uses actual Drop and draft/adoption
// owners. Deleting its old ref cannot hide the retained work from build 2.
func TestReportV1RetainsADeletedDroppedBranch(t *testing.T) {
	tests := &standInCopy{check: accepting}
	cp, _ := newReportFixture(t, Options{}, tests)
	readBothAssignments(t, cp)
	first := mustOpenBuildFile(t, cp)
	workspace := cp.currentWorkspaceDir()
	folder := filepath.Join(workspace, testwriter.TestsRole.Folder)
	writeWorkspaceFiles(t, folder, map[string]string{"eval/retained_test.txt": "Dropped build's tests.\n"})
	foreign := commitAllForLayoutTest(t, folder)
	user := asCaller("alice@example.com", CallerUser)
	_, err := cp.DropBuild(user, &gensurla.DropBuildPayload{ChangeID: first.ChangeID, StartedAt: verdictTime(first.StartedAt)})
	require.NoError(t, err)
	readDraftForTest(t, cp)
	writeBuilderDesignFixture(t, filepath.Join(workspace, draftFolderName), first.DesignSource)
	op := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
	card, err := cp.ProposeChange(asCaller("alice@example.com", CallerGuide), &gensurla.ProposeChangePayload{
		ChangeID: "next", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"), StoryRefreshOpID: &op,
	})
	require.NoError(t, err)
	card = mustConfirmNoop(t, cp, user, card)
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	_, err = cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &gensurla.GetTestWriterAssignmentPayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	productionBuildGit(t, workspace, "branch", "-D", "surla/tests-1")
	productionBuildGit(t, folder, "merge", "--no-edit", foreign)
	before := productionBuildGit(t, workspace, "rev-parse", "surla/tests-2")
	_, err = cp.ReportTestsFinished(asCaller("alice@example.com", CallerTestWriter), &gensurla.ReportTestsFinishedPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, CoverageStatements: []*gensurla.TestCoverageStatement{}})
	require.NoError(t, err)
	waitForNamedTests(t, cp, card.ChangeID, gensurla.TestsKindNeedsChanges)
	findings, err := cp.GetTestFindings(user, &gensurla.GetTestFindingsPayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	assert.Contains(t, strings.Join(findings.Findings, "\n"), "commit "+foreign+" on surla/tests-1 merges work the user has not taken")
	assert.Contains(t, strings.Join(findings.Findings, "\n"), "eval/retained_test.txt")
	assert.Zero(t, tests.recordCalls())
	assert.Equal(t, before, productionBuildGit(t, workspace, "rev-parse", "surla/tests-2"))
}

// TestReportV1DropDuringRecordRetryStopsPublication proves c.mu is released
// during the retry wait and the next attempt sees Drop before calling Record.
func TestReportV1DropDuringRecordRetryStopsPublication(t *testing.T) {
	firstAttempt := make(chan struct{})
	tests := &standInCopy{check: accepting, record: func(_ context.Context, call int, _ string) (string, error) {
		if call == 1 {
			close(firstAttempt)
		}
		return "", &testwriter.IndexLockError{Path: "index.lock", Err: fmt.Errorf("index.lock is held")}
	}}
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	cp, revision := newReportFixture(t, Options{TracerProvider: provider}, tests)
	readBothAssignments(t, cp)
	reportTests(t, cp, revision)
	receive(t, firstAttempt)
	build := mustOpenBuildFile(t, cp)
	_, err := cp.DropBuild(asCaller("alice@example.com", CallerUser), &gensurla.DropBuildPayload{
		ChangeID: build.ChangeID, StartedAt: verdictTime(build.StartedAt),
	})
	require.NoError(t, err)
	callsAtDrop := tests.recordCalls()
	require.Less(t, callsAtDrop, recordLockRetries+1, "the fixture dropped while a retry was still pending")
	before := readBuildFile(t, cp, build.ChangeID)
	waitForSpan(t, exporter, "surla.controlplane.check_reported_tests")
	assert.Equal(t, callsAtDrop, tests.recordCalls(), "no Record attempt follows accepted Drop")
	assert.Equal(t, before, readBuildFile(t, cp, build.ChangeID))
	for _, parent := range tests.recordedParents() {
		assert.Equal(t, build.Base, parent)
	}
}

// TestReportV1RecordRetriesKeepTheCheckedParent composes the checker with a
// simulated publication boundary; the real Record owner has separate Git tests.
func TestReportV1RecordRetriesKeepTheCheckedParent(t *testing.T) {
	for _, duringRetry := range []bool{false, true} {
		name := "before publication"
		if duringRetry {
			name = "during lock retry"
		}
		t.Run(name, func(t *testing.T) {
			var cp *ControlPlane
			tests := &standInCopy{check: accepting}
			tests.record = func(_ context.Context, call int, checkedTip string) (string, error) {
				workspace := cp.activeWorkspaceDir // Record already holds c.mu.
				if call == 1 {
					foreign := productionBuildGit(t, workspace, "commit-tree", checkedTip+"^{tree}", "-p", checkedTip, "-m", "Concurrent other build")
					productionBuildGit(t, workspace, "update-ref", "refs/heads/surla/build-2", foreign)
					productionBuildGit(t, workspace, "update-ref", "refs/heads/surla/tests-1", foreign, checkedTip)
					if duringRetry {
						return "", &testwriter.IndexLockError{Path: "index.lock", Err: fmt.Errorf("index.lock is held")}
					}
				}
				current := productionBuildGit(t, workspace, "rev-parse", "surla/tests-1")
				require.NotEqual(t, checkedTip, current, "Record must keep the earlier L1-checked parent")
				return "", &testwriter.AuthorError{Finding: testwriter.Finding{
					Kind: testwriter.FindingWorkingCopy, Message: "The tests branch moved from checked commit " + checkedTip + "; report again.",
				}}
			}
			var revision string
			cp, revision = newReportFixture(t, Options{}, tests)
			cp.reports.retry = 20 * time.Millisecond
			readBothAssignments(t, cp)
			base := mustOpenBuildFile(t, cp).Base
			reportTests(t, cp, revision)
			waitForTests(t, cp, gensurla.TestsKindNeedsChanges)
			wantCalls := 1
			if duringRetry {
				wantCalls = 2
			}
			assert.Equal(t, wantCalls, tests.recordCalls())
			assert.Equal(t, 1, tests.checked(), "no recheck during a Record retry")
			for _, parent := range tests.recordedParents() {
				assert.Equal(t, base, parent)
			}
		})
	}
}

// TestReportV1OperationalTipReadFailure retains a real Git fault as
// could_not_run, with public path redaction and the raw cause on its span.
func TestReportV1OperationalTipReadFailure(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	tests := &standInCopy{check: accepting}
	cp, revision := newReportFixture(t, Options{TracerProvider: provider}, tests)
	readBothAssignments(t, cp)
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := t.TempDir()
	script := "#!/bin/sh\nfor arg do\nif [ \"$arg\" = for-each-ref ]; then printf 'tip_read_fault /private/report-tip-details\\n' >&2; exit 42; fi\ndone\nexec \"$SURLA_REPORT_REAL_GIT\" \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
	t.Setenv("SURLA_REPORT_REAL_GIT", realGit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	reportTests(t, cp, revision)
	build := waitForTests(t, cp, gensurla.TestsKindCouldNotRun)
	failed, ok := build.TestWriter.Tests.AsCouldNotRun()
	require.True(t, ok)
	assert.Contains(t, failed.Reason, "tip_read_fault")
	assert.NotContains(t, failed.Reason, "/private/report-tip-details")
	assert.Equal(t, stepFailure, failed.Step)
	span := waitForSpan(t, exporter, "surla.controlplane.check_reported_tests")
	foundRaw := false
	for _, event := range span.Events {
		for _, attr := range event.Attributes {
			if strings.Contains(attr.Value.AsString(), "tip_read_fault /private/report-tip-details") {
				foundRaw = true
			}
		}
	}
	assert.True(t, foundRaw, "the actual read error remains on the private operation span")
	assert.Zero(t, tests.recordCalls())
}

// TestReportV1PublishedCommitFailureIsTruthful verifies the caller preserves
// a Record result that published its ref before an operational index failure.
func TestReportV1PublishedCommitFailureIsTruthful(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "before deadline"
		if expired {
			name = "after deadline"
		}
		t.Run(name, func(t *testing.T) {
			var cp *ControlPlane
			published := make(chan string, 1)
			tests := &standInCopy{check: accepting, record: func(ctx context.Context, _ int, checkedTip string) (string, error) {
				workspace := cp.activeWorkspaceDir
				commit := productionBuildGit(t, workspace, "commit-tree", checkedTip+"^{tree}", "-p", checkedTip, "-m", "Accepted tests")
				productionBuildGit(t, workspace, "update-ref", "refs/heads/surla/tests-1", commit, checkedTip)
				published <- commit
				if expired {
					<-ctx.Done()
				}
				return commit, fmt.Errorf("recorded tests as commit %s on surla/tests-1, but could not finish updating the working copy's index: rename /private/report-index-details: permission denied", commit)
			}}
			var revision string
			cp, revision = newReportFixture(t, Options{}, tests)
			if expired {
				cp.reports.limit = 5 * time.Second
			}
			readBothAssignments(t, cp)
			reportTests(t, cp, revision)
			commit := receive(t, published)
			build := waitForTests(t, cp, gensurla.TestsKindCouldNotRun)
			failed, ok := build.TestWriter.Tests.AsCouldNotRun()
			require.True(t, ok)
			assert.Contains(t, failed.Reason, "recorded tests as commit "+commit)
			assert.Contains(t, failed.Reason, "permission denied")
			assert.NotContains(t, failed.Reason, "/private/report-index-details")
			assert.NotContains(t, failed.Reason, "nothing recorded")
			assert.NotContains(t, failed.Reason, "did not finish within")
			assert.Equal(t, stepFailure, failed.Step)
			assert.Equal(t, 1, tests.recordCalls(), "a published reference is never retried against the stale parent")
			assert.Equal(t, commit, productionBuildGit(t, cp.currentWorkspaceDir(), "rev-parse", "surla/tests-1"))
		})
	}
}

// TestReportV1DropDuringTipReadSavesNothing places Drop inside the new L1
// reads, proving the following locked recapture sees it before Record.
func TestReportV1DropDuringTipReadSavesNothing(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	tests := &standInCopy{check: accepting}
	cp, revision := newReportFixture(t, Options{TracerProvider: provider}, tests)
	readBothAssignments(t, cp)
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	gate := newPublishGate(t)
	defer func() { assert.NoError(t, gate.release()) }()
	bin := t.TempDir()
	script := "#!/bin/sh\nfor arg do\n" +
		"if [ \"$arg\" = for-each-ref ] && mkdir \"$SURLA_REPORT_GATE/claimed\" 2>/dev/null; then\n" +
		"exec 3<> \"$SURLA_REPORT_GATE/release\"\n: > \"$SURLA_REPORT_GATE/in-write\"\ndd bs=1 count=1 <&3 >/dev/null 2>&1\nexec 3>&-\nfi\ndone\n" +
		"exec \"$SURLA_REPORT_REAL_GIT\" \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
	t.Setenv("SURLA_REPORT_GATE", gate.dir)
	t.Setenv("SURLA_REPORT_REAL_GIT", realGit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	reportTests(t, cp, revision)
	select {
	case <-gate.held:
	case <-time.After(10 * time.Second):
		t.Fatal("report did not reach its L1 tip read")
	}
	build := mustOpenBuildFile(t, cp)
	_, err = cp.DropBuild(asCaller("alice@example.com", CallerUser), &gensurla.DropBuildPayload{
		ChangeID: build.ChangeID, StartedAt: verdictTime(build.StartedAt),
	})
	require.NoError(t, err)
	before := readBuildFile(t, cp, build.ChangeID)
	require.NoError(t, gate.release())
	waitForSpan(t, exporter, "surla.controlplane.check_reported_tests")
	assert.Zero(t, tests.recordCalls())
	assert.Equal(t, before, readBuildFile(t, cp, build.ChangeID))
}

// TestReportV1DropCapturesTipsAtPublication places a report after Drop has
// read its tests tip but before Drop persists. If the capture were outside
// c.mu, the contending Record can publish C and the retained snapshot loses C.
func TestReportV1DropCapturesTipsAtPublication(t *testing.T) {
	checking := make(chan int, 1)
	releaseCheck := make(chan struct{})
	published := make(chan string, 1)
	var cp *ControlPlane
	tests := &standInCopy{check: func(ctx context.Context, call int) (testwriter.Verdict, error) {
		checking <- call
		select {
		case <-releaseCheck:
			return testwriter.Verdict{}, nil
		case <-ctx.Done():
			return testwriter.Verdict{}, ctx.Err()
		}
	}, record: func(_ context.Context, _ int, checkedTip string) (string, error) {
		workspace := cp.activeWorkspaceDir
		commit := productionBuildGit(t, workspace, "commit-tree", checkedTip+"^{tree}", "-p", checkedTip, "-m", "Concurrent accepted tests")
		productionBuildGit(t, workspace, "update-ref", "refs/heads/surla/tests-1", commit, checkedTip)
		published <- commit
		return commit, nil
	}}
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	var revision string
	cp, revision = newReportFixture(t, Options{TracerProvider: provider}, tests)
	readBothAssignments(t, cp)
	build := mustOpenBuildFile(t, cp)
	reportTests(t, cp, revision)
	receive(t, checking)
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	gate := newPublishGate(t)
	defer func() { assert.NoError(t, gate.release()) }()
	bin := t.TempDir()
	script := "#!/bin/sh\nfor arg do\n" +
		"if [ \"$arg\" = 'refs/heads/surla/tests-1^{commit}' ] && mkdir \"$SURLA_DROP_GATE/claimed\" 2>/dev/null; then\n" +
		"\"$SURLA_DROP_REAL_GIT\" \"$@\" > \"$SURLA_DROP_GATE/tip\" || exit $?\ncat \"$SURLA_DROP_GATE/tip\"\n" +
		"exec 3<> \"$SURLA_DROP_GATE/release\"\n: > \"$SURLA_DROP_GATE/in-write\"\ndd bs=1 count=1 <&3 >/dev/null 2>&1\nexec 3>&-\nexit 0\nfi\ndone\n" +
		"exec \"$SURLA_DROP_REAL_GIT\" \"$@\"\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
	t.Setenv("SURLA_DROP_GATE", gate.dir)
	t.Setenv("SURLA_DROP_REAL_GIT", realGit)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	dropped := make(chan error, 1)
	go func() {
		_, err := cp.DropBuild(asCaller("alice@example.com", CallerUser), &gensurla.DropBuildPayload{
			ChangeID: build.ChangeID, StartedAt: verdictTime(build.StartedAt),
		})
		dropped <- err
	}()
	select {
	case <-gate.held:
	case <-time.After(10 * time.Second):
		t.Fatal("Drop did not reach its tip capture")
	}
	// This probe selects the schedule, not the expected result: an unsafe
	// outside-lock capture lets Record finish and reproduces the lost tip;
	// the serialized owner lets Drop win before that Record can publish.
	serialized := !cp.mu.TryRLock()
	if !serialized {
		cp.mu.RUnlock()
	}
	close(releaseCheck)
	if !serialized {
		receive(t, published)
	}
	require.NoError(t, gate.release())
	require.NoError(t, receive(t, dropped))
	waitForSpan(t, exporter, "surla.controlplane.check_reported_tests")
	actual := productionBuildGit(t, cp.currentWorkspaceDir(), "rev-parse", "surla/tests-1")
	provenance, found, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, provenance.Start.Dropped)
	assert.Equal(t, actual, provenance.Start.Dropped.BranchTips["surla/tests-1"], "Drop must retain the last durable Record tip")
	if serialized {
		assert.Zero(t, tests.recordCalls(), "the report cannot publish after Drop's captured tips")
	}
}
