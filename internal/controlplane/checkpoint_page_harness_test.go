// This file serves the built views page and the real generated HTTP API to
// jsdom tests, which drive the page through the requests it actually sends.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	goahttp "goa.design/goa/v3/http"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/goadesign/highbay/internal/verifyrun"
)

type (
	// pageServer stands in for the project server only in how the page gets
	// its session: pageLink exchanges once for pageSession. Every other
	// request reaches NewHTTPHandler with real generated codecs. Test-only
	// /__test endpoints let a script hold a request before or after the
	// server handles it, answer one with raw bytes, or run a named server-side
	// action, to observe busy states, interleavings and malformed answers.
	pageServer struct {
		// t reports answers the server could not write.
		t *testing.T
		// server listens on loopback; its URL is the page's origin.
		server *httptest.Server
		// mu guards usedLinks, gates, replies and actions.
		mu sync.Mutex
		// usedLinks records the links already exchanged; pageLink and
		// pageLink-<n> each exchange once, so a test can open several pages.
		usedLinks map[string]bool
		// gates hold requests by "METHOD /path" until released.
		gates map[string]*pageGate
		// replies answers the next requests by "METHOD /path" with raw bytes.
		replies map[string][]pageReply
		// actions are the server-side changes a script may ask for by name.
		actions map[string]func()
		// fixtures, when set, runs an action named "<verb> <arguments>" that
		// actions does not name; it reports whether it knew the verb.
		fixtures func(key string) bool
	}

	// pageGate holds matching requests until a script releases it.
	pageGate struct {
		// open closes on release.
		open chan struct{}
		// answered is true when the server handles the request first and
		// only its answer waits.
		answered bool
	}

	// pageReply is one raw answer a test queues for a request.
	pageReply struct {
		// Key is "METHOD /path" of the request to answer.
		Key string `json:"key"`
		// Status is the HTTP status written.
		Status int `json:"status"`
		// Body is the exact JSON body written.
		Body json.RawMessage `json:"body"`
	}
)

const (
	// pageOwner owns every card the page tests create.
	pageOwner = "alice@example.com"
	// pageLink is the one-use link the test page opens with.
	pageLink = "page-test-link"
	// pageSession is the session pageLink exchanges for.
	pageSession = "page-test-session"
	// pageUserID is the caller identity of the page's session.
	pageUserID = "user-page-test"
	// pageGuide is the guide's credential for changes made by another caller.
	pageGuide = "page-test-guide"
	// pageBuilder is the builder's credential, for live targets and drafts
	// the builder agent starts while a page watches.
	pageBuilder = "page-test-builder"
	// pageTestWriter is the test writer's credential, for refusals.
	pageTestWriter = "page-test-test-writer"
)

// requirePageTools returns the node binary the views page tests run. They
// also need ui/checkpoint's installed packages (npm ci --prefix
// ui/checkpoint), which tests never install themselves: installing writes
// the source tree and races other test binaries reading it. Under CI=true a
// missing tool fails the test, because CI installs both before running Go
// tests; elsewhere the test is skipped with the step that enables it.
func requirePageTools(t *testing.T) string {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		missingPageTool(t, "node is required to exercise the views page")
	}
	if _, err := os.Stat("../../ui/checkpoint/node_modules/jsdom"); err != nil {
		missingPageTool(t, "ui/checkpoint/node_modules is missing; run npm ci --prefix ui/checkpoint")
	}
	return node
}

// startPageServer serves svc behind the page session for one test.
func startPageServer(t *testing.T, svc genhighbay.Service) *pageServer {
	t.Helper()
	ps := &pageServer{t: t, usedLinks: map[string]bool{}, gates: map[string]*pageGate{}, replies: map[string][]pageReply{}, actions: map[string]func(){}}
	mux := http.NewServeMux()
	mux.Handle("GET /views", ViewsPage())
	mux.HandleFunc("POST /project/browser-session", ps.exchange)
	mux.HandleFunc("POST /__test/hold", ps.control(func(r pageReply) { ps.gates[r.Key] = &pageGate{open: make(chan struct{})} }))
	mux.HandleFunc("POST /__test/answer-later", ps.control(func(r pageReply) {
		ps.gates[r.Key] = &pageGate{open: make(chan struct{}), answered: true}
	}))
	mux.HandleFunc("POST /__test/release", ps.control(func(r pageReply) {
		if gate, ok := ps.gates[r.Key]; ok {
			close(gate.open)
			delete(ps.gates, r.Key)
		}
	}))
	mux.HandleFunc("POST /__test/reply", ps.control(func(r pageReply) { ps.replies[r.Key] = append(ps.replies[r.Key], r) }))
	mux.HandleFunc("POST /__test/act", ps.runAction)
	mux.Handle("/", ps.intercept(NewHTTPHandler(svc, ps.authenticate)))
	ps.server = httptest.NewServer(mux)
	t.Cleanup(func() {
		ps.mu.Lock()
		for key, gate := range ps.gates {
			close(gate.open)
			delete(ps.gates, key)
		}
		ps.mu.Unlock()
		ps.server.Close()
	})
	return ps
}

// act registers a named server-side change a script runs with
// control(origin, "act", {key}). Actions run without ps.mu, so one may wait
// for a request the page has sent; an action that installs a gate uses
// answerLater.
func (ps *pageServer) act(name string, action func()) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.actions[name] = action
}

// answerLater installs an answer-later gate for key and runs arm under the
// same lock, so interception cannot see the gate before arm has run.
func (ps *pageServer) answerLater(key string, arm func()) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.gates[key] = &pageGate{open: make(chan struct{}), answered: true}
	arm()
}

// pageControlPlane returns a control plane whose waiting read answers
// quickly, so tests observe several waits and changes within seconds.
func pageControlPlane(t *testing.T, opts Options) *ControlPlane {
	t.Helper()
	if opts.Verification == nil && opts.WrittenTestRuns == nil {
		cfg := verificationConfig(t)
		cfg.Plan.SuiteCommit = strings.Repeat("2", 40)
		opts.BuildCacheDir = t.TempDir()
		opts.Verification = cfg
	}
	cp := newControlPlane(t, opts)
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	return cp
}

// runPageScript runs body, an async JavaScript function body, in node with
// harness (ui/checkpoint/testing/page_harness.cjs), origin, and input in
// scope. It fails the test with the script's output when the script throws.
func runPageScript(t *testing.T, ps *pageServer, input map[string]any, body string) {
	t.Helper()
	runPageScriptWithTimeout(t, ps, input, body, 2*time.Minute)
}

// runPageScriptWithTimeout gives the one real native report journey its own
// process bound. Ordinary page tests keep runPageScript's two-minute bound.
// Successful script diagnostics reach Go's JSON output as well as failures.
func runPageScriptWithTimeout(
	t *testing.T, ps *pageServer, input map[string]any, body string, timeout time.Duration,
) {
	t.Helper()
	node := requirePageTools(t)
	harness, err := filepath.Abs("../../ui/checkpoint/testing/page_harness.cjs")
	require.NoError(t, err)
	if input == nil {
		input = map[string]any{}
	}
	input["origin"] = ps.server.URL
	input["link"] = pageLink
	input["guide"] = pageGuide
	input["builder"] = pageBuilder
	input["testWriter"] = pageTestWriter
	input["session"] = pageSession
	input["owner"] = pageOwner
	raw, err := json.Marshal(input)
	require.NoError(t, err)
	harnessLiteral, err := json.Marshal(harness)
	require.NoError(t, err)
	inputPath := filepath.Join(t.TempDir(), "page-input.json")
	require.NoError(t, os.WriteFile(inputPath, raw, 0o600))
	script := `
const fs = require("fs");
const harness = require(` + string(harnessLiteral) + `);
const { api, control, expect, openPage, sleep, until } = harness;
const input = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
const origin = input.origin;
async function main() {
` + body + `
}
main().then(() => { process.stdout.write("PAGE-PASSED"); process.exit(0); }, (err) => { console.error(err && err.stack || err); process.exit(1); });
`
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "-e", script, inputPath)
	cmd.Env = append(os.Environ(), "CI=true")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "views page script failed:\n%s", out)
	require.True(t, strings.HasSuffix(string(out), "PAGE-PASSED"), "views page script output:\n%s", out)
	if diagnostics := strings.TrimSpace(strings.TrimSuffix(string(out), "PAGE-PASSED")); diagnostics != "" {
		t.Logf("views page script diagnostics:\n%s", diagnostics)
	}
}

// authenticate accepts the page session only with the same-origin mark a
// browser adds, and the guide's, builder's and test writer's credentials from
// any program.
func (ps *pageServer) authenticate(r *http.Request) (Principal, error) {
	switch r.Header.Get("Authorization") {
	case "Bearer " + pageSession:
		if r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			return Principal{}, errors.New("browser session requires a same-origin page request")
		}
		return Principal{ID: pageOwner, CallerID: pageUserID, Kind: CallerUser}, nil
	case "Bearer " + pageGuide:
		return Principal{ID: pageOwner, CallerID: "guide-page-test", Kind: CallerGuide}, nil
	case "Bearer " + pageBuilder:
		return Principal{ID: pageOwner, CallerID: "builder-page-test", Kind: CallerBuilder}, nil
	case "Bearer " + pageTestWriter:
		return Principal{ID: pageOwner, CallerID: "test-writer-page-test", Kind: CallerTestWriter}, nil
	}
	return Principal{}, errors.New("session or credential is no longer valid")
}

// exchange trades pageLink once for pageSession, answering like the project
// server's generated exchange operation.
func (ps *pageServer) exchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Link string `json:"link"`
	}
	ps.mu.Lock()
	ok := json.NewDecoder(r.Body).Decode(&body) == nil &&
		(body.Link == pageLink || strings.HasPrefix(body.Link, pageLink+"-")) && !ps.usedLinks[body.Link]
	if ok {
		ps.usedLinks[body.Link] = true
	}
	ps.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !ok {
		serviceErr := genhighbay.MakeUnprocessable(errors.New("browser link is used or expired; open the project's views with hbay"))
		w.WriteHeader(http.StatusUnprocessableEntity)
		if err := goahttp.ResponseEncoder(r.Context(), w).Encode(goahttp.NewErrorResponse(r.Context(), serviceErr)); err != nil {
			ps.t.Logf("write exchange refusal: %v", err)
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
	if err := json.NewEncoder(w).Encode(map[string]string{"session": pageSession, "project_name": "Refunds café"}); err != nil {
		ps.t.Logf("write exchange answer: %v", err)
	}
}

// control decodes one test control request and applies it under ps.mu.
func (ps *pageServer) control(apply func(pageReply)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var reply pageReply
		if err := json.NewDecoder(r.Body).Decode(&reply); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		ps.mu.Lock()
		apply(reply)
		ps.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

// runAction runs the named server-side action without holding ps.mu. A
// request the page sent reaches the owner only after intercept takes ps.mu,
// so an action that waits for that request under the lock would stall it
// until the action gave up.
func (ps *pageServer) runAction(w http.ResponseWriter, r *http.Request) {
	var reply pageReply
	if err := json.NewDecoder(r.Body).Decode(&reply); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ps.mu.Lock()
	action, ok := ps.actions[reply.Key]
	fixtures := ps.fixtures
	ps.mu.Unlock()
	switch {
	case ok:
		action()
	case fixtures == nil || !fixtures(reply.Key):
		ps.t.Errorf("no test action %q", reply.Key)
	}
	w.WriteHeader(http.StatusNoContent)
}

// intercept holds or answers a request when a test asked for it, and
// otherwise passes it to the real handler.
func (ps *pageServer) intercept(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		ps.mu.Lock()
		gate := ps.gates[key]
		ps.mu.Unlock()
		if gate != nil && gate.answered {
			// The server does the work now; only its answer waits.
			recorded := httptest.NewRecorder()
			next.ServeHTTP(recorded, r)
			select {
			case <-gate.open:
			case <-r.Context().Done():
				return
			}
			for name, values := range recorded.Header() {
				w.Header()[name] = values
			}
			w.WriteHeader(recorded.Code)
			if _, err := w.Write(recorded.Body.Bytes()); err != nil {
				ps.t.Logf("write delayed answer for %s: %v", key, err)
			}
			return
		}
		if gate != nil {
			select {
			case <-gate.open:
			case <-r.Context().Done():
				return
			}
		}
		ps.mu.Lock()
		queued := ps.replies[key]
		var reply *pageReply
		if len(queued) > 0 {
			reply = &queued[0]
			ps.replies[key] = queued[1:]
		}
		ps.mu.Unlock()
		if reply != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(reply.Status)
			if _, err := w.Write(reply.Body); err != nil {
				ps.t.Logf("write queued reply for %s: %v", key, err)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

type (
	// pageVerifications serves verification runs from fixtures through the
	// real generated encoders, standing in for runs a builder started. Every
	// other operation reaches the embedded control plane.
	pageVerifications struct {
		*ControlPlane
		// mu guards runs and lists, which test actions change while the
		// server answers.
		mu sync.Mutex
		// runs holds each run's current snapshot by run_id.
		runs map[string]*genhighbay.VerificationStatusResult
		// lists holds each change's run_ids, newest first.
		lists map[string][]string
	}

	// pageTarget is a promotion target whose staged artifacts a test action
	// changes while the server answers; absent makes the project have none.
	pageTarget struct {
		// mu guards every field.
		mu sync.Mutex
		// report is the report LoadReport returns.
		report verify.Report
		// artifacts is what CurrentArtifacts returns.
		artifacts map[string]string
		// artifactsErr, when set, is what CurrentArtifacts fails with.
		artifactsErr error
		// absent makes resolve report no promotion target.
		absent bool
	}
)

// newPageVerifications wraps cp with an empty run fixture set.
func newPageVerifications(cp *ControlPlane) *pageVerifications {
	return &pageVerifications{ControlPlane: cp, runs: map[string]*genhighbay.VerificationStatusResult{}, lists: map[string][]string{}}
}

// set stores each run's snapshot, replacing an earlier one with its run_id.
func (s *pageVerifications) set(runs ...*genhighbay.VerificationStatusResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range runs {
		s.runs[run.RunID] = run
	}
}

// list makes changeID's saved runs exactly runIDs, newest first.
func (s *pageVerifications) list(changeID string, runIDs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lists[changeID] = runIDs
}

// GetVerification returns the run's current fixture snapshot.
func (s *pageVerifications) GetVerification(
	_ context.Context,
	p *genhighbay.GetVerificationPayload,
) (*genhighbay.VerificationStatusResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[p.RunID]
	if !ok {
		return nil, genhighbay.MakeNotFound(errors.New("unknown run_id"))
	}
	return run, nil
}

// ReviewVerification exposes the fixture run through the new user contract.
// Fixture runs have no recorded Story; admission still uses the real control
// plane's approval owner and the full saved report rather than inputs_current.
func (s *pageVerifications) ReviewVerification(
	ctx context.Context, p *genhighbay.GetVerificationPayload,
) (*genhighbay.VerificationReviewResult, error) {
	if _, unavailable := s.verifier.(noVerification); unavailable {
		return nil, genhighbay.MakeNotImplemented(errVerificationUnavailable)
	}
	run, err := s.GetVerification(ctx, p)
	if err != nil {
		return nil, err
	}
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("page review requires its authenticated user")
	}
	req := verifyrun.Request{RunID: run.RunID, Owner: principal.ID, ChangeID: run.ChangeID,
		Plan: verify.Plan{CandidateCommit: run.CandidateCommit, RequestRevision: run.RequestRevision,
			SuiteCommit: run.SuiteCommit, Tests: verify.TestsEvaluator}}
	if _, written := run.TestSource.AsWrittenTests(); written {
		req.Plan.Tests = verify.TestsWritten
	}
	state := verifyrun.State{Kind: verifyrun.StateRunning}
	approval := blockedRunApproval("This fixture has no full saved report for approval.")
	if completed, ok := run.State.AsCompleted(); ok {
		target, err := s.resolvePromotion(ctx)
		var report verify.Report
		if err == nil && target != nil {
			report, err = target.LoadReport(ctx, completed.Report.ReportDigest)
		} else if err == nil {
			if runs := verifierRuns(s.verifier); runs != nil {
				report, err = verifyrun.LoadReport(runs.Plan.ReportDir, completed.Report.ReportDigest)
			}
		}
		if err == nil && report.Digest == completed.Report.ReportDigest {
			state = verifyrun.State{Kind: verifyrun.StateCompleted, Report: report}
			approval, err = s.reviewRunApproval(ctx, principal, req, state)
			if err != nil {
				approval = blockedRunApproval(err.Error())
			}
		}
	} else {
		approval, err = s.reviewRunApproval(ctx, principal, req, state)
		if err != nil {
			return nil, err
		}
	}
	return &genhighbay.VerificationReviewResult{
		RunID: run.RunID, ChangeID: run.ChangeID, RequestRevision: run.RequestRevision,
		CandidateCommit: run.CandidateCommit, SuiteCommit: run.SuiteCommit,
		AcceptedAt: run.AcceptedAt, InputsCurrent: run.InputsCurrent,
		CandidateBinariesDir: run.CandidateBinariesDir, TestSource: run.TestSource, State: run.State,
		Review: &genhighbay.VerificationReview{Approval: approval,
			StoryContext: genhighbay.NewStoryContextNotRecorded(&genhighbay.RunStoryNotRecorded{})},
	}, nil
}

// ListVerifications returns the change's fixture runs, newest first.
func (s *pageVerifications) ListVerifications(
	_ context.Context,
	p *genhighbay.ListVerificationsPayload,
) (*genhighbay.VerificationListResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := make([]*genhighbay.VerificationStatusResult, 0, len(s.lists[p.ChangeID]))
	for _, id := range s.lists[p.ChangeID] {
		runs = append(runs, s.runs[id])
	}
	return &genhighbay.VerificationListResult{ChangeID: p.ChangeID, Runs: runs}, nil
}

// LoadReport returns the report the test made.
func (p *pageTarget) LoadReport(context.Context, string) (verify.Report, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.report, nil
}

// CurrentArtifacts returns the binary digests, or the failure, a test action chose.
func (p *pageTarget) CurrentArtifacts(context.Context, string) (map[string]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.artifacts, p.artifactsErr
}

// resolve is Options.ResolvePromotionTarget: the target, or none while absent.
func (p *pageTarget) resolve(context.Context, string) (PromotionTarget, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.absent {
		return nil, nil
	}
	return p, nil
}

// update changes the target under its lock.
func (p *pageTarget) update(change func(*pageTarget)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	change(p)
}

// passedRun returns a completed, passed run of card whose report digest is
// digest, as a builder's verification would report it.
func passedRun(card *genhighbay.ChangeCardResult, runID, commit, digest string) *genhighbay.VerificationStatusResult {
	return &genhighbay.VerificationStatusResult{
		RunID: runID, ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
		CandidateCommit: commit, SuiteCommit: strings.Repeat("2", 40), AcceptedAt: "2026-10-31T12:34:56.123Z",
		TestSource: genhighbay.NewTestSourceEvaluator(&genhighbay.OperatorEvaluatorTests{}),
		State: genhighbay.NewStateCompleted(&genhighbay.VerificationCompleted{
			SuiteID: "orders_acceptance", Environment: "sim",
			Report: &genhighbay.BuilderVerificationReport{
				ReportDigest: digest, Passed: true,
				Counts: &genhighbay.VerificationCounts{Required: 2, Passed: 2},
				Scenarios: []*genhighbay.VerificationScenario{
					{ID: "a", Outcome: "passed", Checks: []*genhighbay.VerificationCheck{{Name: "check", Passed: true}}},
					{ID: "holdout-1", Outcome: "passed"},
				},
			},
		}),
	}
}

// missingPageTool fails the test under CI=true and otherwise skips it with
// reason, which names the step that provides the missing tool.
func missingPageTool(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("CI") == "true" {
		t.Fatal(reason)
	}
	t.Skip(reason)
}
