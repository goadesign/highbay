package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"goa.design/goa-ai/eval"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
)

// artifactTextFixture is a control plane that saves its operation history,
// with one completed run whose scenarios each failed the artifact check
// because of text planted in their record of the binaries they ran.
type artifactTextFixture struct {
	// cp is the control plane; alice owns its confirmed card "chg".
	cp *ControlPlane
	// cfg names the runs and report folders the run was saved in.
	cfg *VerificationConfig
	// runID is the completed run.
	runID string
	// report is the full report saved for the run.
	report verify.Report
}

// plantedArtifactText stands for text a test program could put in a
// scenario's artifact name or digest, such as a holdout scenario's name.
const plantedArtifactText = "FAKE_HOLDOUT_refund_over_limit_417"

func TestVerifyChangeAcceptsOnlyTheOwnersCurrentConfirmedRevision(t *testing.T) {
	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser})
	bob := WithPrincipal(context.Background(), Principal{ID: "bob@example.com", CallerID: "bob@example.com", Kind: CallerUser})
	cp, card, cfg := verificationSetup(t)
	commit := commitBuilderCandidateFixture(t, cp, card)

	start := func(ctx context.Context, revision string) (*gensurla.VerificationStatusResult, error) {
		return cp.VerifyChange(ctx, &gensurla.VerifyChangePayload{ChangeID: "chg", RequestRevision: revision, CandidateCommit: commit})
	}
	for _, tc := range []struct {
		name    string
		ctx     context.Context
		rev     string
		wantErr string
	}{
		{"another principal", bob, card.RequestRevision, "is not the owner"},
		{"stale revision", alice, "rev-old", "request_revision mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := start(tc.ctx, tc.rev)
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
	t.Run("unknown change", func(t *testing.T) {
		_, err := cp.VerifyChange(alice, &gensurla.VerifyChangePayload{ChangeID: "nope", RequestRevision: "r", CandidateCommit: commit})
		assert.ErrorContains(t, err, `unknown change_id "nope"`)
	})
	t.Run("no verification configured", func(t *testing.T) {
		unconfigured := newControlPlane(t, Options{})
		_, err := unconfigured.VerifyChange(alice, &gensurla.VerifyChangePayload{ChangeID: "chg", RequestRevision: "r", CandidateCommit: commit})
		assert.ErrorIs(t, err, errVerificationUnavailable)
		_, err = unconfigured.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: verifyrun.NewRunID()})
		assert.ErrorIs(t, err, errVerificationUnavailable)
	})

	t.Run("accepted run carries the operator's suite and the caller's commit and revision", func(t *testing.T) {
		started, err := start(alice, card.RequestRevision)
		require.NoError(t, err)
		assert.Equal(t, "chg", started.ChangeID)
		assert.Equal(t, card.RequestRevision, started.RequestRevision)
		assert.Equal(t, commit, started.CandidateCommit)
		assert.Equal(t, cfg.Plan.SuiteCommit, started.SuiteCommit)
		assert.Equal(t, gensurla.StateKindRunning, started.State.Kind())
		assert.True(t, started.InputsCurrent)

		saved, err := os.ReadFile(filepath.Join(cfg.RunsDir, started.RunID, "request.json"))
		require.NoError(t, err)
		var req verifyrun.Request
		require.NoError(t, json.Unmarshal(saved, &req))
		assert.Equal(t, "alice@example.com", req.Owner)
		assert.Equal(t, cfg.Plan.SuiteRepo, req.Plan.SuiteRepo, "the suite comes from the operator, never the caller")
		assert.Equal(t, cfg.Plan.Binaries, req.Plan.Binaries)
		assert.Equal(t, verify.TestsEvaluator, req.Plan.Tests, "the stored evaluator keeps the operator's environment")
		assert.Equal(t, verifyrun.StoryRecorded, req.StoryContext.State)
		assert.Positive(t, req.StoryContext.BuildNumber)
		assert.False(t, req.StoryContext.BuildStartedAt.IsZero())
		assert.Equal(t, gensurla.NewTestSourceEvaluator(&gensurla.OperatorEvaluatorTests{}), started.TestSource)

		_, err = cp.GetVerification(bob, &gensurla.GetVerificationPayload{RunID: started.RunID})
		assert.ErrorContains(t, err, "is not the owner of run_id")
	})
}

func TestGetVerificationReadsTheRunFromDiskInEveryState(t *testing.T) {
	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser})
	cp, card, cfg := verificationSetup(t)
	commit := strings.Repeat("a", 40)
	spec := reportSpec{SuiteID: "orders_acceptance", SuiteCommit: cfg.Plan.SuiteCommit, Environment: "sim",
		Artifacts: map[string]string{"orders": "d1"}}

	// writeRun saves a finished run as its supervisor would.
	writeRun := func(t *testing.T, result string) string {
		t.Helper()
		runID := verifyrun.NewRunID()
		dir := filepath.Join(cfg.RunsDir, runID)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		plan := cfg.Plan
		plan.CandidateCommit, plan.RequestRevision, plan.Tests = commit, card.RequestRevision, verify.TestsEvaluator
		req, err := json.Marshal(verifyrun.Request{RunID: runID, Owner: "alice@example.com", ChangeID: "chg",
			AcceptedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), Plan: plan})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), req, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600))
		if result != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "result.json"), []byte(result), 0o600))
		}
		return runID
	}
	get := func(t *testing.T, runID string) *gensurla.VerificationStatusResult {
		t.Helper()
		res, err := cp.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: runID})
		require.NoError(t, err)
		assert.Equal(t, card.RequestRevision, res.RequestRevision)
		assert.Equal(t, commit, res.CandidateCommit)
		assert.True(t, res.InputsCurrent)
		return res
	}

	t.Run("completed returns only the builder view", func(t *testing.T) {
		report := failingReport(t, card.RequestRevision, commit, spec)
		b, err := json.Marshal(report)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Plan.ReportDir, report.Digest+".json"), b, 0o600))
		res := get(t, writeRun(t, `{"kind":"report","report_digest":"`+report.Digest+`"}`))
		completed, ok := res.State.AsCompleted()
		require.True(t, ok)
		assert.Equal(t, "orders_acceptance", completed.SuiteID)
		assert.Equal(t, "sim", completed.Environment)
		assert.False(t, completed.Report.Passed)
		assert.Equal(t, report.Digest, completed.Report.ReportDigest)
		assert.Equal(t, &gensurla.VerificationCounts{Required: 2, Passed: 1, Failed: 1}, completed.Report.Counts)
		require.Len(t, completed.Report.Scenarios, 2)
		assert.Equal(t, "a", completed.Report.Scenarios[0].ID)
		assert.Equal(t, "holdout-1", completed.Report.Scenarios[1].ID, "holdout names never reach builders")
		assert.Empty(t, completed.Report.Scenarios[1].Checks)
		wire, err := json.Marshal(res)
		require.NoError(t, err)
		assert.NotContains(t, string(wire), `"h"`)
	})

	t.Run("a failed artifact check reaches the builder as a fixed sentence only", func(t *testing.T) {
		fx := newArtifactTextFixture(t)
		res, err := fx.cp.GetVerification(asCaller("alice@example.com", CallerBuilder), &gensurla.GetVerificationPayload{RunID: fx.runID})
		require.NoError(t, err)
		completed, ok := res.State.AsCompleted()
		require.True(t, ok)
		assertArtifactCheckSentences(t, completed.Report)
		assertArtifactTextStaysInSavedReport(t, fx, res, "surla.controlplane.get_verification")
	})

	t.Run("not completed returns where it stopped", func(t *testing.T) {
		res := get(t, writeRun(t, `{"kind":"no_report","failure":"execution_failed","stage":"build","summary":"The candidate did not build."}`))
		failed, ok := res.State.AsNotCompleted()
		require.True(t, ok)
		assert.Equal(t, &gensurla.VerificationNotCompleted{Kind: "execution_failed", Stage: "build", Summary: "The candidate did not build."}, failed)
	})

	t.Run("a run whose processes ended without a result is interrupted", func(t *testing.T) {
		res := get(t, writeRun(t, ""))
		failed, ok := res.State.AsNotCompleted()
		require.True(t, ok)
		assert.Equal(t, "interrupted", failed.Kind)
	})

	t.Run("a new control plane reads runs started by an earlier one", func(t *testing.T) {
		runID := writeRun(t, `{"kind":"no_report","failure":"interrupted","stage":"supervise","summary":"The verification was canceled."}`)
		restarted := newControlPlane(t, Options{BuildCacheDir: t.TempDir(), Verification: cfg})
		res, err := restarted.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: runID})
		require.NoError(t, err, "the run does not depend on the saved Change Card")
		assert.Equal(t, "chg", res.ChangeID)
		assert.False(t, res.InputsCurrent, "a fresh card directory preserves historical access without a current match")
	})

	t.Run("unknown run", func(t *testing.T) {
		_, err := cp.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: verifyrun.NewRunID()})
		assert.ErrorContains(t, err, "unknown run_id")
	})
}

func TestGetVerificationSafeErrors(t *testing.T) {
	prevProvider := otel.GetTracerProvider()
	recorder := &recordingTracerProvider{}
	otel.SetTracerProvider(recorder)
	t.Cleanup(func() { otel.SetTracerProvider(prevProvider) })

	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser})
	cp, card, cfg := verificationSetup(t)
	commit := strings.Repeat("a", 40)

	t.Run("foreign run with malformed accepted_at still reports non-owner without leaking validation error", func(t *testing.T) {
		recorder.reset()
		runID := verifyrun.NewRunID()
		dir := filepath.Join(cfg.RunsDir, runID)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		plan := cfg.Plan
		plan.CandidateCommit, plan.RequestRevision, plan.Tests = commit, card.RequestRevision, verify.TestsEvaluator
		raw, err := json.Marshal(verifyrun.Request{
			RunID:    runID,
			Owner:    "bob@example.com",
			ChangeID: "chg",
			Plan:     plan,
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), raw, 0o600))

		_, err = cp.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: runID})
		require.Error(t, err)
		assert.ErrorContains(t, err, "is not the owner of run_id")
		assert.NotContains(t, err.Error(), "accepted_at")
		assert.NotContains(t, err.Error(), cfg.RunsDir)
	})

	t.Run("empty object request.json records raw error on span and returns safe public error", func(t *testing.T) {
		recorder.reset()
		runID := verifyrun.NewRunID()
		dir := filepath.Join(cfg.RunsDir, runID)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), []byte(`{}`), 0o600))

		_, err := cp.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: runID})
		require.Error(t, err)
		assert.Equal(t, `read verification "`+runID+`": saved verification record could not be read`, err.Error())
		assert.NotContains(t, err.Error(), cfg.RunsDir)

		spans := recorder.matching("surla.controlplane.get_verification")
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].statusCode)
		require.Len(t, spans[0].errors, 1)
		assert.ErrorContains(t, spans[0].errors[0], "does not match directory")
	})

	t.Run("missing report file records evaluator path on span and returns safe public error", func(t *testing.T) {
		recorder.reset()
		runID := verifyrun.NewRunID()
		dir := filepath.Join(cfg.RunsDir, runID)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		plan := cfg.Plan
		plan.CandidateCommit, plan.RequestRevision, plan.Tests = commit, card.RequestRevision, verify.TestsEvaluator
		raw, err := json.Marshal(verifyrun.Request{
			RunID:      runID,
			Owner:      "alice@example.com",
			ChangeID:   "chg",
			AcceptedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC),
			Plan:       plan,
		})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), raw, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600))
		missingDigest := strings.Repeat("f", 64)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"kind":"report","report_digest":"`+missingDigest+`"}`), 0o600))

		_, err = cp.GetVerification(alice, &gensurla.GetVerificationPayload{RunID: runID})
		require.Error(t, err)
		assert.Equal(t, `read verification "`+runID+`": saved verification record could not be read`, err.Error())
		assert.NotContains(t, err.Error(), cfg.Plan.ReportDir)

		spans := recorder.matching("surla.controlplane.get_verification")
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].statusCode)
		require.Len(t, spans[0].errors, 1)
		assert.ErrorContains(t, spans[0].errors[0], missingDigest)
	})
}

func TestListVerifications(t *testing.T) {
	prevProvider := otel.GetTracerProvider()
	recorder := &recordingTracerProvider{}
	otel.SetTracerProvider(recorder)
	t.Cleanup(func() { otel.SetTracerProvider(prevProvider) })

	alice := WithPrincipal(context.Background(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser})
	bob := WithPrincipal(context.Background(), Principal{ID: "bob@example.com", CallerID: "bob@example.com", Kind: CallerUser})
	cp, card, cfg := verificationSetup(t)
	commit := strings.Repeat("a", 40)
	spec := reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: cfg.Plan.SuiteCommit, Environment: "sim",
		Artifacts: map[string]string{"orders": "d1"},
	}

	t.Run("authorization and configuration guards", func(t *testing.T) {
		_, err := cp.ListVerifications(context.Background(), &gensurla.ListVerificationsPayload{ChangeID: "chg"})
		assert.ErrorContains(t, err, "authenticated principal is required")

		unconfigured := newControlPlane(t, Options{})
		_, err = unconfigured.ListVerifications(alice, &gensurla.ListVerificationsPayload{ChangeID: "chg"})
		assert.ErrorIs(t, err, errVerificationUnavailable)

		_, err = cp.ListVerifications(alice, &gensurla.ListVerificationsPayload{ChangeID: "unknown-card"})
		assert.ErrorContains(t, err, `unknown change_id "unknown-card"`)

		_, err = cp.ListVerifications(bob, &gensurla.ListVerificationsPayload{ChangeID: "chg"})
		assert.ErrorContains(t, err, `principal "bob@example.com" is not the owner of change_id "chg"`)
	})

	t.Run("empty list when no runs exist yet", func(t *testing.T) {
		res, err := cp.ListVerifications(alice, &gensurla.ListVerificationsPayload{ChangeID: "chg"})
		require.NoError(t, err)
		assert.Equal(t, "chg", res.ChangeID)
		assert.NotNil(t, res.Runs)
		assert.Empty(t, res.Runs)
	})

	t.Run("lists owner runs newest-first with sub-second accepted_at, builder projection, and a waiting proposal", func(t *testing.T) {
		report := failingReport(t, card.RequestRevision, commit, spec)
		b, err := json.Marshal(report)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(cfg.Plan.ReportDir, report.Digest+".json"), b, 0o600))

		writeCompletedRun := func(runID string, acceptedAt time.Time) {
			dir := filepath.Join(cfg.RunsDir, runID)
			require.NoError(t, os.MkdirAll(dir, 0o700))
			plan := cfg.Plan
			plan.CandidateCommit, plan.RequestRevision, plan.Tests = commit, card.RequestRevision, verify.TestsEvaluator
			raw, err := json.Marshal(verifyrun.Request{
				RunID:      runID,
				Owner:      "alice@example.com",
				ChangeID:   "chg",
				AcceptedAt: acceptedAt,
				Plan:       plan,
			})
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), raw, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"kind":"report","report_digest":"`+report.Digest+`"}`), 0o600))
		}

		runEarly := "10000000000000000000000000000001"
		runLate := "20000000000000000000000000000002"
		writeCompletedRun(runEarly, time.Date(2026, 10, 31, 12, 0, 0, 100_000_000, time.UTC))
		writeCompletedRun(runLate, time.Date(2026, 10, 31, 12, 0, 0, 900_000_000, time.UTC))

		// Revise the card into needs_confirmation; prior R1 runs must remain discoverable.
		priorID := "check_01"
		revised, err := cp.ReviseChange(alice, &gensurla.ReviseChangePayload{
			ChangeID:        "chg",
			RequestRevision: card.RequestRevision,
			Prompt:          "Require a refund reason.",
			Analysis: &gensurla.CP1RevisionAnalysis{
				Summary: "Refunds up to $50 require a reason.",
				Ambiguities: []*gensurla.CP1AnalysisAmbiguity{{
					Question: "Which reasons are allowed?",
					Options:  []string{"damaged_item", "any_reason"},
				}},
				Examples: []*gensurla.CP1RevisionAnalysisExample{{
					PriorID:          &priorID,
					Scenario:         "Refund at the limit",
					InputSummary:     "Order total $50.00",
					ExpectedOutcome:  "Instant store credit",
					VerificationKind: "deterministic",
				}},
				RemovedExampleIds:  []string{},
				PreservedBehaviors: []string{},
			},
		})
		require.NoError(t, err)
		require.Equal(t, StatusConfirmed, revised.Status, "the agreed revision stays in force")
		require.NotNil(t, revised.ProposedRevision, "the revision waits as a proposal")

		res, err := cp.ListVerifications(alice, &gensurla.ListVerificationsPayload{ChangeID: "chg"})
		require.NoError(t, err)
		require.Len(t, res.Runs, 2)
		assert.Equal(t, runLate, res.Runs[0].RunID)
		assert.Equal(t, "2026-10-31T12:00:00.9Z", res.Runs[0].AcceptedAt)
		assert.Equal(t, card.RequestRevision, res.Runs[0].RequestRevision)
		assert.Equal(t, runEarly, res.Runs[1].RunID)
		assert.True(t, res.Runs[0].InputsCurrent)
		assert.True(t, res.Runs[1].InputsCurrent)
		assert.Equal(t, "2026-10-31T12:00:00.1Z", res.Runs[1].AcceptedAt)

		completed, ok := res.Runs[0].State.AsCompleted()
		require.True(t, ok)
		require.Len(t, completed.Report.Scenarios, 2)
		assert.Equal(t, "holdout-1", completed.Report.Scenarios[1].ID)
		assert.Empty(t, completed.Report.Scenarios[1].Checks)
	})

	t.Run("a failed artifact check reaches the builder as a fixed sentence only", func(t *testing.T) {
		fx := newArtifactTextFixture(t)
		res, err := fx.cp.ListVerifications(asCaller("alice@example.com", CallerBuilder), &gensurla.ListVerificationsPayload{ChangeID: "chg"})
		require.NoError(t, err)
		require.Len(t, res.Runs, 1)
		completed, ok := res.Runs[0].State.AsCompleted()
		require.True(t, ok)
		assertArtifactCheckSentences(t, completed.Report)
		assertArtifactTextStaysInSavedReport(t, fx, res, "surla.controlplane.list_verifications")
	})

	t.Run("corrupt published request.json records raw error on span and returns safe public error", func(t *testing.T) {
		recorder.reset()
		corruptID := "30000000000000000000000000000003"
		corruptDir := filepath.Join(cfg.RunsDir, corruptID)
		require.NoError(t, os.MkdirAll(corruptDir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(corruptDir, "request.json"), []byte(`{"secret_host_path":"/var/lib/secret`), 0o600))

		_, err := cp.ListVerifications(alice, &gensurla.ListVerificationsPayload{ChangeID: "chg"})
		require.Error(t, err)
		assert.Equal(t, `list verifications for "chg": saved verification records could not be read`, err.Error())
		assert.NotContains(t, err.Error(), cfg.RunsDir)
		assert.NotContains(t, err.Error(), "secret_host_path")

		spans := recorder.matching("surla.controlplane.list_verifications")
		require.Len(t, spans, 1)
		assert.Equal(t, codes.Error, spans[0].statusCode)
		require.Len(t, spans[0].errors, 1)
		assert.ErrorContains(t, spans[0].errors[0], "decode run request")
	})
}

// verificationSetup returns a control plane with a verification
// configuration whose runs are supervised by a command that exits at once,
// and a confirmed card owned by alice, using the stored-evaluator profile.
func verificationSetup(t *testing.T) (*ControlPlane, *gensurla.ChangeCardResult, *VerificationConfig) {
	t.Helper()
	return verificationSetupForSource(t, true)
}

// verificationSetupForSource binds the source before New starts background work.
func verificationSetupForSource(
	t *testing.T, stored bool,
) (*ControlPlane, *gensurla.ChangeCardResult, *VerificationConfig) {
	t.Helper()
	cfg := verificationConfig(t)
	cp, card := verificationSetupWithOptions(t, verificationSourceOptions(cfg, stored))
	return cp, card, cfg
}

// verificationSetupWithOptions returns a control plane built from opts, which
// name the verification source, and its confirmed card "chg" owned by alice
// with a stored open build.
func verificationSetupWithOptions(t *testing.T, opts Options) (*ControlPlane, *gensurla.ChangeCardResult) {
	t.Helper()
	analysis := &gensurla.CP1Analysis{
		Summary:     "Refunds up to $50 are instant.",
		Ambiguities: []*gensurla.CP1AnalysisAmbiguity{},
		Examples: []*gensurla.CP1AnalysisExample{{
			Scenario: "Refund at the limit", InputSummary: "Order total $50.00",
			ExpectedOutcome: "Instant store credit", VerificationKind: "deterministic",
		}},
		PreservedBehaviors: []string{},
	}
	cp := newControlPlane(t, buildFixtureOptions(t, opts))
	card := saveProposedCardFixture(t, cp, "alice@example.com", "chg", "Refunds up to $50 are instant.", analysis)
	card = attachBuildSnapshot(t, cp, card.ChangeID)
	card = mustConfirmNoop(t, cp, WithPrincipal(t.Context(), Principal{ID: "alice@example.com", CallerID: "alice@example.com", Kind: CallerUser}), card)
	require.Equal(t, StatusConfirmed, card.Status)
	saveStoredBuildFixture(t, cp, card)
	return cp, card
}

// newArtifactTextFixture saves one completed run of alice's change "chg" on a
// control plane that records its operations. Its report declares three repair
// scenarios and one holdout; each scenario's record of the binaries it ran
// carries plantedArtifactText where the artifact check quotes it: "a" in an
// artifact name without a digest, "b" in a digest, "c" in a name recorded
// twice, and the holdout in a digest.
func newArtifactTextFixture(t *testing.T) artifactTextFixture {
	t.Helper()
	cfg := verificationConfig(t)
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	opts := verificationSourceOptions(cfg, true)
	opts.TracerProvider = provider
	cp, card := verificationSetupWithOptions(t, opts)

	commit := strings.Repeat("a", 40)
	ran := func(id string, artifacts ...eval.Artifact) eval.ScenarioReport {
		return eval.ScenarioReport{ID: id, StartedAt: time.Now(), Passed: true,
			Result: &eval.Result{Checks: []eval.Check{{Name: "check", Passed: true}}, Artifacts: artifacts}}
	}
	twice := eval.Artifact{Name: "candidate:" + plantedArtifactText + "_twice", URI: "sha256:d2"}
	report, err := verify.Assemble(
		verify.Binding{
			CandidateCommit: commit, RequestRevision: card.RequestRevision, SuiteID: "orders_acceptance",
			SuiteCommit: cfg.Plan.SuiteCommit, Artifacts: map[string]string{"orders": "d1"}, Environment: "sim",
		},
		verify.Suite{ID: "orders_acceptance", Environment: "sim", Scenarios: []verify.Scenario{
			{ID: "a"}, {ID: "b"}, {ID: "c"}, {ID: "h", Holdout: true},
		}},
		eval.Report{SuiteID: "orders_acceptance", Scenarios: []eval.ScenarioReport{
			ran("a", eval.Artifact{Name: "candidate:" + plantedArtifactText + "_name", URI: "md5:d1"}),
			ran("b", eval.Artifact{Name: "candidate:orders", URI: "sha256:" + plantedArtifactText + "_digest"}),
			ran("c", eval.Artifact{Name: "candidate:orders", URI: "sha256:d1"}, twice, twice),
			ran("h", eval.Artifact{Name: "candidate:orders", URI: "sha256:" + plantedArtifactText + "_holdout"}),
		}}, 0)
	require.NoError(t, err)
	writeReportFile(t, cfg.Plan.ReportDir, report)

	runID := verifyrun.NewRunID()
	dir := filepath.Join(cfg.RunsDir, runID)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	plan := cfg.Plan
	plan.CandidateCommit, plan.RequestRevision, plan.Tests = commit, card.RequestRevision, verify.TestsEvaluator
	req, err := json.Marshal(verifyrun.Request{RunID: runID, Owner: "alice@example.com", ChangeID: "chg",
		AcceptedAt: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC), Plan: plan})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), req, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"kind":"report","report_digest":"`+report.Digest+`"}`), 0o600))
	return artifactTextFixture{cp: cp, cfg: cfg, runID: runID, report: report}
}

// assertArtifactCheckSentences checks the builder view of the fixture's
// report: every scenario could not be evaluated, each repair scenario
// carries Surla's sentence about its record of binaries, and the holdout
// carries nothing.
func assertArtifactCheckSentences(t *testing.T, report *gensurla.BuilderVerificationReport) {
	t.Helper()
	require.Len(t, report.Scenarios, 4)
	for i, id := range []string{"a", "b", "c"} {
		scenario := report.Scenarios[i]
		assert.Equal(t, id, scenario.ID)
		assert.Equal(t, "harness_error", scenario.Outcome)
		if assert.NotNil(t, scenario.Error, id) {
			assert.Contains(t, *scenario.Error, "does not match what Surla built", id)
			assert.Contains(t, *scenario.Error, "report_digest", id)
		}
	}
	assert.Equal(t, &gensurla.VerificationScenario{ID: "holdout-1", Outcome: "harness_error"}, report.Scenarios[3])
}

// assertArtifactTextStaysInSavedReport checks every place the fixture's
// planted text could have reached after a read that returned result and
// recorded the operation span: it is absent from the marshalled result and
// from every file in the cards folder, including the saved operation
// history, and in the runs folder; in the report folder only the saved
// report holds it, in all four places it was planted.
func assertArtifactTextStaysInSavedReport(t *testing.T, fx artifactTextFixture, result any, span string) {
	t.Helper()
	wire, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), plantedArtifactText)
	assert.NotContains(t, string(wire), "candidate:")

	history, err := os.ReadFile(filepath.Join(fx.cp.cards.dir, "traces.jsonl"))
	require.NoError(t, err, "the read was saved in the operation history")
	assert.Contains(t, string(history), span)

	var holders []string
	for _, folder := range []string{fx.cp.cards.dir, fx.cfg.RunsDir, fx.cfg.Plan.ReportDir} {
		require.NoError(t, filepath.WalkDir(folder, func(name string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if strings.Contains(string(data), plantedArtifactText) {
				holders = append(holders, name)
			}
			return nil
		}))
	}
	saved := filepath.Join(fx.cfg.Plan.ReportDir, fx.report.Digest+".json")
	assert.Equal(t, []string{saved}, holders, "only the saved report holds the planted text")
	data, err := os.ReadFile(saved)
	require.NoError(t, err)
	for _, place := range []string{"_name", "_digest", "_twice", "_holdout"} {
		assert.Contains(t, string(data), plantedArtifactText+place)
	}
}

// verificationConfig returns a verification configuration whose runs are
// supervised by a command that exits at once.
func verificationConfig(t *testing.T) *VerificationConfig {
	t.Helper()
	return &VerificationConfig{
		RunsDir: t.TempDir(),
		Plan: verify.Plan{
			CandidateRepo: "/repos/candidate", CandidateModule: ".", Binaries: map[string]string{"orders": "./cmd/orders"},
			SuiteRepo: "/repos/suite", SuiteCommit: strings.Repeat("b", 40), SuiteModule: "eval",
			SuiteCommand: "./cmd/orders-evals", ReportDir: t.TempDir(),
		},
		// The supervisor exits at once; these tests cover acceptance and
		// reading, and verifyrun's own tests run the real lifecycle.
		Command: []string{"/bin/sh", "-c", "exit 0", "sh"},
		Timeout: time.Hour,
	}
}
