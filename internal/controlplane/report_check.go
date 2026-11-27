// This file judges the test writer's reports: one worker per project checks
// each reported build's working copy against the Story saved with the build,
// records accepted tests on the tests branch, and saves the verdict, so a
// verdict follows report_tests_finished without another call.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/goadesign/highbay/internal/designeval"
	"github.com/goadesign/highbay/internal/scaffold"
	"github.com/goadesign/highbay/internal/testwriter"
)

type (
	// reportChecker is the one worker per project that judges the test
	// writer's reports. New starts its run on the server's context, and
	// Close ends it before the stores close. run judges every
	// build found reported when it starts, then those each report wakes it
	// for. Each judgment opens the test writer's working copy as a build
	// start does (openReportCopy), runs in the project's working-copy turn
	// and is bounded by limit. Its last step (confirm that the build still
	// answers to the report checked, record accepted tests, save the
	// verdict) holds reportMu, so a report cannot fall between the
	// confirmation and the commit. When ctx ends, the steps that run the
	// author's code end with it and nothing is saved: the build stays
	// reported, and the next
	// run judges it.
	reportChecker struct {
		// cp owns the builds, the turn, reportMu, reportWake and the copy.
		cp *ControlPlane
		// limit bounds one report's judgment; checkLimit outside tests.
		limit time.Duration
		// retry is how long after a failed last step or a failed read the
		// checker wakes to judge or read again; retryAfterFailure outside
		// tests.
		retry time.Duration
		// failures counts, per report, the last steps that failed in a row,
		// which bounds the checker's own retries (maxRetriesAfterFailure).
		// Only the checker's goroutine reads and writes it.
		failures map[reportIdentity]int
		// readFailed records that a card or a build could not be read in the
		// round in progress (run), and readFailures counts the rounds in a
		// row in which one could not, which bounds the retries of reads the
		// same way. Only the checker's goroutine reads and writes them.
		readFailed   bool
		readFailures int
		// now gives each verdict its time.
		now func() time.Time
	}

	// reportIdentity names the report a judgment answers: the build (its
	// change, revision and start) and the report's number. A new start
	// changes the start time and a new report the number, so a verdict is
	// saved only while the build still answers to the identity it was
	// checked for. Only the report checker saves a verdict, so a build that
	// answers to the identity is still reported.
	reportIdentity struct {
		// changeID, revision and startedAt identify the build.
		changeID, revision string
		startedAt          time.Time
		// report is the build's report number.
		report int
	}

	// openCopyError is a failure to open the test writer's working copy.
	openCopyError struct {
		// err is openReportCopy's error.
		err error
	}
)

const (
	// checkLimit bounds one report's whole judgment: writing the required
	// tests, the check with its build (ten minutes) and listing (sixty
	// seconds), and recording. The five minutes over the two inner limits
	// cover generation, dependency downloads and git, so it is reached only
	// when something other than the author's build or listing hangs: a cause
	// the test writer cannot fix.
	checkLimit = 15 * time.Minute

	// retryAfterFailure is how long the checker waits before judging again
	// when its last step failed (the build could not be read to confirm it,
	// or the verdict could not be saved), and before reading again when a
	// card or a build could not be read at all. A build left reported then
	// has no check running, and without a later report nothing else would
	// wake the checker. A minute lets a passing cause, such as a full disk
	// freed by the user, clear without judging in a tight loop.
	retryAfterFailure = time.Minute
	// maxRetriesAfterFailure bounds those retries, for one report's last
	// step and for reads: the checker retries after each of the first three
	// failures in a row, and after the fourth it stops waking itself, and the
	// build waits, still reported, for the next report or the next server
	// start, both of which judge every reported build. A judgment takes
	// minutes, so three retries cover a passing cause without repeating a
	// lasting one forever.
	maxRetriesAfterFailure = 3

	// recordLockRetries is how many times a judgment repeats Record alone when
	// it fails because of index.lock: Git's own commands hold that lock for
	// milliseconds to seconds, whereas a lock still there after these retries
	// is a stale lock.
	recordLockRetries = 5
	// recordLockRetryPause bounds the wait between Record attempts on
	// index.lock: 500 ms lets a running Git command finish without holding
	// reportMu and the working-copy turn for more than a few seconds across
	// recordLockRetries attempts.
	recordLockRetryPause = 500 * time.Millisecond

	// stepOpenCopy, stepLimit and stepFailure
	// are the user's steps of a check that could not run, one per cause; each
	// fits the design's 512 characters.
	stepOpenCopy = "Build the change again: starting a build opens the test writer's working copy " +
		"and names the step when it cannot."
	stepLimit = "Check that git and Go module downloads work in the project folder, " +
		"then ask the test writer to report again."
	stepFailure = "Make sure the project's git repository works (git status in the project folder) " +
		"and the disk has free space, then ask the test writer to report again."

	// stepDependencies is what the user does when the test module's
	// dependencies could not be downloaded where Highbay runs for a cause
	// outside the test writer's changes. Highbay withholds the download's
	// output, which may hold proxy settings, so the step has the user run
	// the same download, with the settings Highbay's go commands use, where
	// its output can be read. A check and a build start each end it with
	// their own way to run again.
	stepDependencies = "In the terminal highbay was started from, run " +
		"GOTOOLCHAIN=local GOWORK=off GOFLAGS= go mod download in the project's " + scaffold.SuiteDir +
		" folder and fix what it reports, such as no network, proxy or private-module settings " +
		"missing there, or a Go older than the module requires"

	// stepWriteCopy is what the user does when Highbay could not write the
	// required tests into the test writer's working copy. A check and a
	// build start each end it with their own way to run again.
	stepWriteCopy = "Make sure the test module in the test writer's working copy " +
		"(the .highbay-tests folder of the project) can be written and the disk has free space"

	// replacedEvent is the span event of a judgment whose report a newer
	// report or build replaced before its verdict was saved.
	replacedEvent = "a newer report or build replaced the one checked; nothing saved"
)

// Error returns the failure's text.
func (e *openCopyError) Error() string {
	return e.err.Error()
}

// Unwrap returns openReportCopy's error.
func (e *openCopyError) Unwrap() error {
	return e.err
}

// startReportChecker starts the report checker on the server's context for a
// project; Close ends it before the stores close.
func (c *ControlPlane) startReportChecker() {
	c.reports = newReportChecker(c)
	c.reportWorker.Go(func() { c.reports.run(c.serverCtx) })
}

// newReportChecker returns the report checker of cp, judging within
// checkLimit and trying again retryAfterFailure after a failed last step or
// read.
func newReportChecker(cp *ControlPlane) *reportChecker {
	return &reportChecker{
		cp: cp, limit: checkLimit, retry: retryAfterFailure, failures: map[reportIdentity]int{}, now: time.Now,
	}
}

// run judges reported builds until ctx ends, one round after another: first
// every build found reported when it starts, as after a restart, then every
// reported build again each time report_tests_finished or one of the
// checker's own retries wakes it. A report made during a judgment leaves its
// wake waiting, so it is judged after the judgment in progress. A round in
// which a card or a build could not be read may have missed a reported build,
// so the checker wakes itself to read again (retryReads). Once ctx ends no
// judgment starts, even when a wake is waiting too.
func (r *reportChecker) run(ctx context.Context) {
	for ctx.Err() == nil {
		r.readFailed = false
		for _, changeID := range r.reported(ctx) {
			if ctx.Err() != nil {
				return
			}
			r.judge(ctx, changeID)
		}
		r.retryReads(ctx)
		select {
		case <-ctx.Done():
		case <-r.cp.reportWake:
		}
	}
}

// reported returns the changes whose usable builds (usableBuild) are
// reported, in the order the builds started. A project that verifies with its
// stored evaluator uses no test writer, so it has none. A card or build that cannot be read is left
// out and recorded on a span (recordReadFailure), which makes the round read
// again later.
func (r *reportChecker) reported(ctx context.Context) []string {
	if r.cp.storedEvaluator() {
		return nil
	}
	r.cp.mu.RLock()
	defer r.cp.mu.RUnlock()
	saved, found, err := r.cp.cards.loadOpenBuild()
	if err != nil {
		r.recordReadFailure(ctx, err)
		return nil
	}
	if !found || !r.cp.usableBuild(saved) || saved.TestWriter.State != testsReported {
		return nil
	}
	provenance, hasProvenance, err := r.cp.cards.loadMadeFrom()
	if err != nil {
		r.recordReadFailure(ctx, err)
		return nil
	}
	if hasProvenance && buildDropped(provenance, saved) {
		return nil
	}
	return []string{saved.ChangeID}
}

// judge judges the report of changeID's build in the working-copy turn.
//
// Phase 1 takes the turn, waiting for it, or returns when ctx ends. Phase 2
// reads the build and keeps the identity of its report; a build that is no
// longer reported is left alone, and one that cannot be read is read again
// later (retryReads). Phase 3 writes the required tests into the
// copy from the build's Story and checks the copy, within limit. Phase 4,
// under reportMu, confirms the build still answers to the identity, records
// accepted tests and saves the verdict, marking the test writer's assignment
// unread, and the builder's too for an acceptance. When ctx ends first,
// or a newer report or build replaced the one checked, nothing is recorded
// or saved. When the last step fails, the failure is an error on the
// judgment's span and the checker judges again after r.retry, at most
// maxRetriesAfterFailure times in a row (retryLater).
func (r *reportChecker) judge(ctx context.Context, changeID string) {
	// Phase 1: take the turn. A free turn and an ended ctx may both be ready.
	select {
	case r.cp.workingCopyTurn <- struct{}{}:
	case <-ctx.Done():
		return
	}
	defer func() { <-r.cp.workingCopyTurn }()
	if ctx.Err() != nil {
		return
	}

	// Phase 2: read the build.
	r.cp.mu.RLock()
	saved, found, err := r.cp.cards.loadOpenBuild()
	var provenance madeFromRecord
	var hasProvenance bool
	if err == nil && found {
		provenance, hasProvenance, err = r.cp.cards.loadMadeFrom()
	}
	r.cp.mu.RUnlock()
	if err != nil {
		r.recordReadFailure(ctx, err)
		return
	}
	if !found || saved.ChangeID != changeID || !r.cp.usableBuild(saved) || saved.TestWriter.State != testsReported ||
		hasProvenance && buildDropped(provenance, saved) {
		return
	}
	id := reportIdentityOf(saved)
	ctx, span := r.cp.tracer("highbay/controlplane").Start(ctx, "highbay.controlplane.check_reported_tests", trace.WithAttributes(
		attribute.String("highbay.change_id", id.changeID),
		attribute.String("highbay.request_revision", id.revision),
		attribute.Int("highbay.report_number", id.report),
	))
	defer span.End()

	// Phase 3: prepare and check within the limit.
	deadline := time.Now().Add(r.limit)
	checkCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	branch := testwriter.TestsRole.Branch(saved.Number)
	copy, v, err := r.check(checkCtx, branch, saved.Base, saved.Stories)
	if err == nil && v.Accepted() {
		v.Findings = coverageScenarioFindings(saved.TestWriter.CoverageStatements, v.DeclaredScenarios)
	}
	if ctx.Err() != nil {
		span.AddEvent("stopped before a verdict; the build stays reported")
		return
	}
	verdict := r.verdictOf(v, err, deadline)
	failure := err

	// Phase 4: confirm, record and save.
	r.cp.reportMu.Lock()
	defer r.cp.reportMu.Unlock()
	if verdict.State == testsAccepted {
		checkedTip, tipVerdict, tipErr := r.checkReportedTestsTip(checkCtx, saved)
		if ctx.Err() != nil {
			span.AddEvent("stopped before the tests were recorded; the build stays reported")
			return
		}
		switch {
		case tipErr != nil:
			failure = tipErr
			verdict = r.couldNotRun(tipErr, deadline, "Highbay could not read the tests branch before recording")
		case !tipVerdict.Accepted():
			verdict = r.verdictOf(tipVerdict, nil, deadline)
		default:
			message := fmt.Sprintf("Add the tests Highbay accepted for change %s, report %d", id.changeID, id.report)
			commit, current, confirmErr, recordErr := r.recordAccepted(checkCtx, id, copy, v, branch, checkedTip, message)
			if confirmErr != nil {
				recordJudgeFailure(span, confirmErr, "confirm build")
				r.retryLater(span, id)
				return
			}
			switch {
			case ctx.Err() != nil:
				span.AddEvent("stopped before a verdict; the build stays reported")
				return
			case !current:
				span.AddEvent(replacedEvent)
				return
			case recordErr != nil:
				failure = recordErr
				var author *testwriter.AuthorError
				if commit != "" {
					// A published commit is a durable fact even if the check's
					// deadline elapsed before its index could be published.
					verdict = testWriterAssignment{State: testsCouldNotRun,
						Reason: boundReason(testwriter.WithoutAbsolutePaths(fmt.Sprintf(
							"Tests were recorded as commit %s, but Highbay could not finish recording: %v", commit, recordErr))),
						Step: stepFailure,
					}
				} else if errors.As(recordErr, &author) {
					verdict = r.verdictOf(testwriter.Verdict{}, recordErr, deadline)
				} else {
					verdict = r.couldNotRun(recordErr, deadline, "Highbay could not record the accepted tests")
				}
			default:
				verdict.Commit = commit
			}
		}
	}
	saved, current, err := r.saveVerdict(id, verdict)
	if err != nil {
		recordJudgeFailure(span, err, "save verdict")
		r.retryLater(span, id)
		return
	}
	delete(r.failures, id)
	if !current {
		span.AddEvent(replacedEvent)
		return
	}
	span.SetAttributes(attribute.String("highbay.tests_state", saved.TestWriter.State))
	if saved.TestWriter.State == testsCouldNotRun {
		if failure == nil {
			failure = errors.New(saved.TestWriter.Reason)
		}
		span.RecordError(failure)
		span.SetStatus(codes.Error, "the check could not run")
	}
}

// check opens the working copy, writes the required tests for stories into
// it, and checks it, returning the copy, which records an accepted verdict.
// A failure to open the copy is an *openCopyError, and an error from Prepare
// that the author caused an *testwriter.AuthorError; any other error says
// nothing about the author's work.
func (r *reportChecker) check(
	ctx context.Context, branch, base string, stories []designeval.StorySummary,
) (buildCopy, testwriter.Verdict, error) {
	copy, err := r.cp.openReportCopy(ctx)
	if err != nil {
		return nil, testwriter.Verdict{}, &openCopyError{err: err}
	}
	if err := copy.Prepare(ctx, branch, base, stories); err != nil {
		return nil, testwriter.Verdict{}, err
	}
	v, err := copy.Check(ctx, branch, base, stories)
	return copy, v, err
}

// openReportCopy opens the test writer's working copy of the bound project
// as a build start does: the project must still have Highbay's test layout,
// whose suite module and generated program the check uses.
func (c *ControlPlane) openReportCopy(ctx context.Context) (buildCopy, error) {
	source, layout, workspace, err := c.projectTestLayout(ctx)
	if err != nil {
		return nil, err
	}
	if source != testSourceStarterLayout || workspace == "" {
		return nil, errors.New("the project no longer has Highbay's test layout")
	}
	return c.openBuildCopy(ctx, workspace, layout)
}

// checkReportedTestsTip checks the actual tests copy and its branch tip before
// recording. The caller holds workingCopyTurn; this gate never joins a copy,
// marks a read, or rejects tests at B. Only the caller records accepted work.
func (r *reportChecker) checkReportedTestsTip(
	ctx context.Context, saved buildFile,
) (string, testwriter.Verdict, error) {
	work, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	r.cp.workspaceMu.RLock()
	defer r.cp.workspaceMu.RUnlock()
	workspace := r.cp.activeWorkspaceDir
	copy, found, err := testwriter.OpenCopy(work, workspace, testwriter.TestsRole)
	if err != nil {
		return "", testwriter.Verdict{}, &openCopyError{err: err}
	}
	if !found {
		return "", testwriter.Verdict{}, &openCopyError{err: errors.New("the test writer's working copy does not exist")}
	}
	facts, err := copy.Facts(work)
	if err != nil {
		return "", testwriter.Verdict{}, err
	}
	branch := testwriter.TestsRole.Branch(saved.Number)
	if facts.Detached || facts.Branch != branch || facts.Operation != "none" {
		refusal := copyOffBranchRefusal(testwriter.TestsRole, saved.ChangeID, saved.Number, true, facts, "report_tests_finished")
		return "", testwriter.Verdict{Findings: []testwriter.Finding{{
			Kind: testwriter.FindingWorkingCopy, Message: testwriter.WithoutAbsolutePaths(refusal.Message),
		}}}, nil
	}
	// On-branch index.lock is left to Record's existing typed retry. Read the
	// exact ref in the project repository, never the copy's authored .git.
	out, err := runGitRead(work, workspace, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch+"^{commit}")
	if err != nil {
		return "", testwriter.Verdict{}, err
	}
	tip := strings.TrimSpace(string(out))
	check, err := r.cp.checkBuildCommit(work, workspace, saved, tip, branch)
	if err != nil {
		return "", testwriter.Verdict{}, err
	}
	var reasons []string
	if !check.OnBranch {
		reasons = append(reasons, fmt.Sprintf("tests commit %s is not on %s", tip, branch))
	}
	if !check.FromBase {
		reasons = append(reasons, fmt.Sprintf("tests commit %s does not descend from this build's base %s", tip, saved.Base))
	}
	reasons = append(reasons, foreignBuildWorkReasons(check.Foreign)...)
	if len(reasons) > 0 {
		message := fmt.Sprintf("%s. Make %s and your working copy hold neither the other builds' commits nor their changes to the named files. Make your own tests again on %s from base %s, uncommitted (Highbay commits what it accepts), then report again.", strings.Join(reasons, "; "), branch, branch, saved.Base)
		return "", testwriter.Verdict{Findings: []testwriter.Finding{{
			Kind: testwriter.FindingWorkingCopy, Message: testwriter.WithoutAbsolutePaths(message),
		}}}, nil
	}
	return tip, testwriter.Verdict{}, nil
}

// answersTo reports whether id's build still answers to id, before accepted
// tests are recorded for it. Callers hold c.mu; the checker also holds
// reportMu through its bounded Record attempt so reports and Drop cannot
// cross publication.
func (r *reportChecker) answersTo(id reportIdentity) (bool, error) {
	saved, found, err := r.cp.cards.loadOpenBuild()
	if err != nil || !found || reportIdentityOf(saved) != id {
		return false, err
	}
	provenance, hasProvenance, err := r.cp.cards.loadMadeFrom()
	if err != nil {
		return false, err
	}
	return !hasProvenance || !buildDropped(provenance, saved), nil
}

// recordAccepted records v on branch with message, repeating Record alone up to
// recordLockRetries times when it fails because of index.lock, waiting at most
// recordLockRetryPause between attempts while checkCtx is live. Any other
// error, or checkCtx ending, stops immediately. Confirmation read failures
// return separately so the existing bounded judgment retry remains unchanged.
func (r *reportChecker) recordAccepted(
	checkCtx context.Context, id reportIdentity, copy buildCopy, v testwriter.Verdict,
	branch, checkedTip, message string,
) (commit string, current bool, confirmErr, recordErr error) {
	pause := min(r.retry, recordLockRetryPause)
	for attempt := 0; ; attempt++ {
		// Drop shares c.mu with publication, but never waits for a whole
		// check or retry sequence. Each attempt uses the same L1-checked tip.
		r.cp.mu.Lock()
		current, confirmErr = r.answersTo(id)
		if confirmErr != nil || !current {
			r.cp.mu.Unlock()
			return "", current, confirmErr, nil
		}
		commit, recordErr = copy.Record(checkCtx, v, branch, checkedTip, message)
		r.cp.mu.Unlock()
		var lockErr *testwriter.IndexLockError
		if commit != "" || recordErr == nil || !errors.As(recordErr, &lockErr) || attempt >= recordLockRetries || checkCtx.Err() != nil {
			return commit, true, nil, recordErr
		}
		timer := time.NewTimer(pause)
		select {
		case <-checkCtx.Done():
			timer.Stop()
			return "", true, nil, checkCtx.Err()
		case <-timer.C:
		}
	}
}

// saveVerdict saves verdict on id's build, with its time, when the build
// still answers to id, and marks the test writer's assignment unread, and the
// builder's too for an acceptance. It returns the saved build, or false and
// saves nothing when a newer report/build replaced id or the user dropped
// that start. Callers hold reportMu.
func (r *reportChecker) saveVerdict(id reportIdentity, verdict testWriterAssignment) (buildFile, bool, error) {
	r.cp.mu.Lock()
	defer r.cp.mu.Unlock()
	saved, found, err := r.cp.cards.loadOpenBuild()
	if err != nil || !found || reportIdentityOf(saved) != id {
		return buildFile{}, false, err
	}
	provenance, hasProvenance, err := r.cp.cards.loadMadeFrom()
	if err != nil {
		return buildFile{}, false, err
	}
	if hasProvenance && buildDropped(provenance, saved) {
		return buildFile{}, false, nil
	}
	verdict.CheckedAt = r.now().UTC()
	verdict.CoverageStatements = cloneCoverageStatements(saved.TestWriter.CoverageStatements)
	saved.TestWriter = verdict
	if verdict.State == testsAccepted {
		saved.Builder.Read = false
	}
	if err := r.cp.cards.saveOpenBuild(saved); err != nil {
		return buildFile{}, false, err
	}
	return saved, true, nil
}

// retryLater wakes the checker after r.retry, as a report would, so the build
// whose judgment of id was lost is judged again, unless id's last step has
// already failed more than maxRetriesAfterFailure times in a row: then it
// records on span that the build waits for the next report or server start.
func (r *reportChecker) retryLater(span trace.Span, id reportIdentity) {
	r.failures[id]++
	if r.failures[id] > maxRetriesAfterFailure {
		span.AddEvent("the last step failed too often in a row; the build waits for the next report or server start")
		return
	}
	r.wakeLater()
}

// retryReads ends a round of run. When a card or a build could not be read in
// it, a reported build may have been left without its check, so the checker
// wakes itself after r.retry to read again, as after a failed last step, at
// most maxRetriesAfterFailure rounds in a row: after that the builds wait for
// the next report or server start, which is recorded on a span. A round that
// read everything starts the count again.
func (r *reportChecker) retryReads(ctx context.Context) {
	if !r.readFailed {
		r.readFailures = 0
		return
	}
	r.readFailures++
	if r.readFailures > maxRetriesAfterFailure {
		_, span := r.cp.tracer("highbay/controlplane").Start(ctx, "highbay.controlplane.retry_reported_tests")
		defer span.End()
		span.AddEvent("reading failed too often in a row; reported builds wait for the next report or server start")
		return
	}
	r.wakeLater()
}

// wakeLater wakes the checker after r.retry, as a report would. A wake already
// waiting stands for it, and one sent after the checker stopped is never read.
func (r *reportChecker) wakeLater() {
	time.AfterFunc(r.retry, func() {
		select {
		case r.cp.reportWake <- struct{}{}:
		default:
		}
	})
}

// recordReadFailure records on its own span that the report checker could not
// read a card or a build, and marks the round in progress as having failed a
// read (retryReads).
func (r *reportChecker) recordReadFailure(ctx context.Context, err error) {
	r.readFailed = true
	_, span := r.cp.tracer("highbay/controlplane").Start(ctx, "highbay.controlplane.read_reported_tests")
	defer span.End()
	recordJudgeFailure(span, err, "read builds")
}

// reportIdentityOf returns the identity of saved's latest report.
func reportIdentityOf(saved buildFile) reportIdentity {
	return reportIdentity{
		changeID:  saved.ChangeID,
		revision:  saved.RequestRevision,
		startedAt: saved.StartedAt,
		report:    saved.ReportNumber,
	}
}

// verdictOf returns the verdict to save for a judgment that returned v and err
// with the given deadline, without its time. Findings stand whenever the
// author can act on them, even at the deadline. Otherwise a judgment that
// returned at or after its deadline could not run, whatever it returned, so
// the limit is reached exactly at the deadline. Any other error could not
// run either; an accepted copy is accepted, with its commit set when it is
// recorded.
func (r *reportChecker) verdictOf(v testwriter.Verdict, err error, deadline time.Time) testWriterAssignment {
	if author := (*testwriter.AuthorError)(nil); errors.As(err, &author) {
		return testWriterAssignment{State: testsNeedChanges, Findings: []string{author.Finding.Message}}
	}
	if err == nil && !v.Accepted() {
		findings := make([]string, len(v.Findings))
		for i, f := range v.Findings {
			findings[i] = f.Message
		}
		return testWriterAssignment{State: testsNeedChanges, Findings: findings}
	}
	if err != nil || !time.Now().Before(deadline) {
		return r.couldNotRun(err, deadline, "Highbay's check could not run")
	}
	return testWriterAssignment{State: testsAccepted}
}

// couldNotRun returns the verdict of a check that could not run because of
// err, or because it reached its limit when the deadline has passed: a
// reason, without absolute paths and at most maxCheckReason characters, and
// the user's step for the cause. failed says what failed, for a cause no
// other case names.
func (r *reportChecker) couldNotRun(err error, deadline time.Time, failed string) testWriterAssignment {
	var reason, step string
	var open *openCopyError
	var lockErr *testwriter.IndexLockError
	switch {
	case !time.Now().Before(deadline):
		reason, step = fmt.Sprintf("Checking the tests did not finish within %s, "+
			"though building the test program and listing its tests each have their own shorter limit.", limitWords(r.limit)), stepLimit
	case errors.As(err, &open):
		reason, step = "Highbay could not open the test writer's working copy: "+err.Error(), stepOpenCopy
	case errors.Is(err, testwriter.ErrWriteGenerated):
		reason = "Highbay could not write the required tests into the test writer's working copy or its snapshot: " + err.Error()
		step = stepWriteCopy + ", then ask the test writer to report again."
	case errors.Is(err, testwriter.ErrDependencies):
		reason, step = failed+": "+err.Error(), stepDependencies+", then ask the test writer to report again."
	case errors.As(err, &lockErr):
		reason = failed + ": Git's index is locked."
		step = fmt.Sprintf(
			"If no Git command is running in the test writer's working copy, delete %s; then ask the test writer to report again.",
			lockErr.Path,
		)
	default:
		reason, step = failed+": "+err.Error(), stepFailure
	}
	return testWriterAssignment{State: testsCouldNotRun, Reason: boundReason(testwriter.WithoutAbsolutePaths(reason)), Step: step}
}

// limitWords words a limit for a reason: whole minutes as minutes, as for
// checkLimit, anything else as Go prints a duration.
func limitWords(d time.Duration) string {
	if d >= time.Minute && d%time.Minute == 0 {
		return fmt.Sprintf("%d minutes", int(d/time.Minute))
	}
	return d.String()
}

// boundReason shortens reason to at most maxCheckReason characters, ending a
// shortened one with an ellipsis.
func boundReason(reason string) string {
	if utf8.RuneCountInString(reason) <= maxCheckReason {
		return reason
	}
	runes := []rune(reason)
	return string(runes[:maxCheckReason-1]) + "…"
}

// recordJudgeFailure records err on span as a failure of the report checker.
func recordJudgeFailure(span trace.Span, err error, status string) {
	span.RecordError(err)
	span.SetStatus(codes.Error, status)
}
