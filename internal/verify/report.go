// Package verify turns an evaluator suite's eval.Report into Highbay's CP4
// verification report and checks, at CP5, that the report still describes the
// change being promoted.
//
// The report is bound to the exact inputs that were tested: the candidate
// commit and request revision, the evaluator suite and its commit, the digest
// of every candidate binary the suite actually executed, and the environment
// profile. Every scenario the suite declares is accounted for in the
// denominator, and holdout scenarios are reduced to anonymous outcomes in the
// view returned to builders.
//
// Precondition: the candidate's prompts and configuration are fixed by its
// binaries, whose digests the report binds, and by what the trusted evaluator
// suite applies when it launches them. A report says nothing about prompt
// overrides a running target loads at runtime (a goa-ai prompt.Store): a
// promotion target with such overrides active must refuse to report its
// current inputs. The prompt references a candidate attaches to its model
// requests are the candidate's own claims; an evaluator can use them to
// corroborate what it applied, never to establish it.
// Compiler inputs, including retained packages, the module cache and toolchain,
// are trusted. Same-user tampering can make the measured binary or test program
// differ from what the selected source would produce; a digest does not
// authenticate that source correspondence.
package verify

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"goa.design/goa-ai/eval"

	suitecmd "github.com/goadesign/highbay/verify/suite"
)

type (
	// Binding is the exact set of inputs a verification report describes.
	Binding struct {
		// CandidateCommit is the commit the candidate binaries were built
		// from.
		CandidateCommit string `json:"candidate_commit"`
		// RequestRevision identifies the confirmed request (CP1) the change
		// implements.
		RequestRevision string `json:"request_revision"`
		// SuiteID names the evaluator suite.
		SuiteID string `json:"suite_id"`
		// SuiteCommit is the commit of the evaluator-owned suite.
		SuiteCommit string `json:"suite_commit"`
		// Artifacts maps each candidate binary name to the SHA-256 digest of
		// the binary verify built and expects the suite to execute.
		Artifacts map[string]string `json:"artifacts"`
		// Environment is the dependency profile the suite declares it runs
		// candidates with, such as "sim".
		Environment string `json:"environment"`
	}

	// Suite describes the scenarios the evaluator declares. It comes from the
	// evaluator's own generated suite, never from the candidate.
	Suite struct {
		// ID is the suite identifier.
		ID string `json:"id"`
		// Environment is the dependency profile the suite runs candidates
		// with.
		Environment string `json:"environment"`
		// Scenarios lists every declared scenario in declaration order.
		Scenarios []Scenario `json:"scenarios"`
	}

	// Scenario is one declared scenario.
	Scenario struct {
		// ID is the scenario identifier.
		ID string `json:"id"`
		// Holdout marks scenarios whose details builders must never see.
		Holdout bool `json:"holdout"`
	}

	// Outcome is the result of one required scenario.
	Outcome string

	// ScenarioResult is one scenario's outcome in the report.
	ScenarioResult struct {
		// ID is the scenario identifier.
		ID string `json:"id"`
		// Holdout marks a holdout scenario.
		Holdout bool `json:"holdout"`
		// Outcome is how the scenario ended.
		Outcome Outcome `json:"outcome"`
		// Detail is the evaluator's scenario report, kept for operators and
		// removed from the builder view for holdout scenarios.
		Detail *eval.ScenarioReport `json:"detail,omitempty"`
	}

	// Counts tallies scenario outcomes.
	Counts struct {
		// Required is the number of declared scenarios.
		Required int `json:"required"`
		// Passed counts scenarios whose checks all passed.
		Passed int `json:"passed"`
		// Failed counts scenarios that ran and failed a check or claim.
		Failed int `json:"failed"`
		// NotRun counts declared scenarios with no result.
		NotRun int `json:"not_run"`
		// HarnessError counts scenarios whose environment broke.
		HarnessError int `json:"harness_error"`
	}

	// artifactFaultKind names which of checkArtifacts' five checks failed.
	// It is never stored: Assemble and ForBuilders each derive it from the
	// binding and the scenario's reported artifacts.
	artifactFaultKind string

	// artifactFault is the first failed check of a scenario's record of the
	// candidate binaries it ran, in checkArtifacts' fixed order. The zero
	// value means every check passed.
	artifactFault struct {
		// kind is the check that failed; empty when none did.
		kind artifactFaultKind
		// detail is the full description with the names and digests the
		// scenario reported. Assemble keeps it in the saved report for the
		// operator; ForBuilders never shows it.
		detail string
	}

	// Report is the CP4 verification report. Suite and Evidence are the
	// inputs it was derived from; every other field is recomputed from them
	// whenever the report is checked, so a stored claim is never trusted.
	Report struct {
		// Binding is what was tested.
		Binding Binding `json:"binding"`
		// Suite is the evaluator's declaration the denominator comes from.
		Suite Suite `json:"suite"`
		// Evidence is the suite's original eval.Report, kept unchanged.
		Evidence eval.Report `json:"evidence"`
		// SuiteError is the suite-level error reported by the evaluation run,
		// such as judge calibration failure or cancellation. A report with a
		// suite error never passes.
		SuiteError string `json:"suite_error,omitempty"`
		// SuiteExit is the suite command's exit status: 0 passed, 1 ran and
		// failed, anything else could not run cleanly. Only 0 can pass.
		SuiteExit int `json:"suite_exit"`
		// Counts covers every declared scenario.
		Counts Counts `json:"counts"`
		// Scenarios lists every declared scenario in declaration order.
		Scenarios []ScenarioResult `json:"scenarios"`
		// Passed is true only when every declared scenario passed.
		Passed bool `json:"passed"`
		// Digest identifies this exact report: the SHA-256 of its canonical
		// JSON with Digest empty.
		Digest string `json:"digest"`
	}
)

const (
	// OutcomePassed means every check and claim of the scenario passed.
	OutcomePassed Outcome = "passed"
	// OutcomeFailed means the scenario ran and a check or claim failed.
	OutcomeFailed Outcome = "failed"
	// OutcomeNotRun means the suite produced no result for the scenario.
	OutcomeNotRun Outcome = "not_run"
	// OutcomeHarnessError means the scenario's environment broke before it
	// could judge the candidate.
	OutcomeHarnessError Outcome = "harness_error"

	// artifactPrefix marks a scenario artifact that records the digest of a
	// candidate binary the scenario executed: name "candidate:<binary>",
	// URI "sha256:<digest>".
	artifactPrefix = "candidate:"

	// artifactDigestUnreadable means a candidate artifact's URI is not a
	// "sha256:" digest, so what the scenario ran cannot be compared.
	artifactDigestUnreadable artifactFaultKind = "digest_unreadable"
	// artifactReportedTwice means the scenario recorded the same binary name
	// more than once, so which digest it ran is ambiguous.
	artifactReportedTwice artifactFaultKind = "reported_twice"
	// artifactMissing means the scenario recorded nothing for a bound binary.
	artifactMissing artifactFaultKind = "missing"
	// artifactDigestMismatch means the scenario recorded a bound binary with
	// a digest other than the bound one.
	artifactDigestMismatch artifactFaultKind = "digest_mismatch"
	// artifactUndeclared means the scenario recorded a binary the binding
	// does not name.
	artifactUndeclared artifactFaultKind = "undeclared"

	// artifactSentenceStart and artifactSentenceEnd frame every sentence in
	// artifactFaultSentences; the words between them say which check failed.
	artifactSentenceStart = "The test program's record of the binaries it ran does not match what Highbay built ("
	artifactSentenceEnd   = "), so no check of this scenario counts; " +
		"the full detail is in the saved report named by report_digest."
)

var (
	// ErrStaleReport reports that a report no longer describes the change
	// being promoted.
	ErrStaleReport = errors.New("verification report does not match the change being promoted")

	// artifactFaultSentences holds, for each failed artifact check, the one
	// sentence ForBuilders shows instead of the check's detail. Each is fixed
	// text: it names the kind of mismatch and where the detail is kept, never
	// a binary name or digest, because those come from the test program's
	// report.
	artifactFaultSentences = map[artifactFaultKind]string{
		artifactDigestUnreadable: artifactSentenceStart +
			"it recorded a binary without a SHA-256 digest" + artifactSentenceEnd,
		artifactReportedTwice: artifactSentenceStart +
			"it recorded the same binary more than once" + artifactSentenceEnd,
		artifactMissing: artifactSentenceStart +
			"it did not record running a binary Highbay built" + artifactSentenceEnd,
		artifactDigestMismatch: artifactSentenceStart +
			"it recorded a binary whose digest is not that of the binary Highbay built" + artifactSentenceEnd,
		artifactUndeclared: artifactSentenceStart +
			"it recorded running a binary Highbay did not build" + artifactSentenceEnd,
	}
)

// Assemble builds the CP4 report for binding from the evaluator's suite
// declaration and the suite's original eval.Report, which it keeps as
// evidence. Each declared scenario counts once. A scenario passes only when
// the suite says it passed and it reports executing exactly the binding's
// candidate binaries; a scenario that reports a missing, extra or different
// binary is a harness error whose detail carries checkArtifacts' description
// of the first failed check, so the same inputs always give the same report.
// A suite-level error fails the report while keeping every scenario result.
// Malformed input (an empty suite, empty or duplicate declared IDs, duplicate
// or undeclared result IDs) is rejected.
func Assemble(binding Binding, suite Suite, evidence eval.Report, suiteExit int) (Report, error) {
	if err := validate(binding, suite, evidence); err != nil {
		return Report{}, err
	}
	byID := make(map[string]eval.ScenarioReport, len(evidence.Scenarios))
	for _, s := range evidence.Scenarios {
		byID[s.ID] = s
	}
	r := Report{Binding: binding, Suite: suite, Evidence: evidence, SuiteError: evidence.Error,
		SuiteExit: suiteExit, Counts: Counts{Required: len(suite.Scenarios)}}
	for _, declared := range suite.Scenarios {
		res := ScenarioResult{ID: declared.ID, Holdout: declared.Holdout}
		s, ok := byID[declared.ID]
		switch {
		case !ok:
			res.Outcome = OutcomeNotRun
		case s.StartedAt.IsZero():
			res.Outcome, res.Detail = OutcomeNotRun, &s
		case s.Error != "" || s.Result == nil:
			res.Outcome, res.Detail = OutcomeHarnessError, &s
		default:
			res.Detail = &s
			res.Outcome = OutcomeFailed
			if s.Passed {
				res.Outcome = OutcomePassed
			}
			// The suite reported no error of its own here, so a detail error
			// on a started scenario with a result is always this check's.
			// ForBuilders relies on that to recognise it.
			if fault := checkArtifacts(binding.Artifacts, s.Result.Artifacts); fault.kind != "" {
				res.Outcome = OutcomeHarnessError
				s.Error = fault.detail
			}
		}
		r.Counts.add(res.Outcome)
		r.Scenarios = append(r.Scenarios, res)
	}
	r.Passed = r.SuiteError == "" && r.SuiteExit == suitecmd.ExitPassed && r.Counts.Passed == r.Counts.Required
	digest, err := r.computeDigest()
	if err != nil {
		return Report{}, err
	}
	r.Digest = digest
	return r, nil
}

// BuilderReport is the only verification result builders receive. It carries
// counts, the verdict and, for repair scenarios only, their checks and
// diagnostics. Holdout scenarios appear only as anonymous outcomes; their
// names, declarations, evidence and artifacts never leave the evaluator.
type BuilderReport struct {
	// ReportDigest identifies the full report this view was derived from.
	ReportDigest string `json:"report_digest"`
	// Counts covers every declared scenario.
	Counts Counts `json:"counts"`
	// Passed is the report's verdict.
	Passed bool `json:"passed"`
	// SuiteFailed reports that the evaluation run itself failed or ended
	// abnormally, without its details.
	SuiteFailed bool `json:"suite_failed"`
	// Scenarios lists every declared scenario in declaration order.
	Scenarios []BuilderScenario `json:"scenarios"`
}

// BuilderScenario is one scenario in the builder view.
type BuilderScenario struct {
	// ID is the scenario name for repair scenarios and "holdout-<n>" for
	// holdouts.
	ID string `json:"id"`
	// Outcome is how the scenario ended.
	Outcome Outcome `json:"outcome"`
	// Checks are the repair scenario's checks with their diagnostics; empty
	// for holdouts.
	Checks []eval.Check `json:"checks,omitempty"`
	// Error is the repair scenario's environment error as the suite reported
	// it, or, when the scenario's record of the binaries it ran failed
	// Highbay's artifact check, the fixed sentence for the check that failed;
	// empty for holdouts.
	Error string `json:"error,omitempty"`
}

// ForBuilders returns the builder view of the report: what get_verification,
// list_verifications and `hbay verify` show by default. A holdout keeps only
// its position and outcome. A repair scenario keeps its name, its checks and
// the error the suite reported for it. An error Assemble wrote because the
// scenario's record of the binaries it ran failed checkArtifacts is not the
// suite's: it quotes names and digests from the test program's report, so
// the view shows the fixed sentence for the failed check instead, and the
// detail stays in the saved report. Such an error is recognised without a
// stored mark: the scenario is a harness error with a result, and the suite's
// own entry for it in Evidence reports no error.
func (r Report) ForBuilders() BuilderReport {
	b := BuilderReport{ReportDigest: r.Digest, Counts: r.Counts, Passed: r.Passed,
		SuiteFailed: r.SuiteError != "" || r.SuiteExit != 0}
	// suiteErrors holds each scenario's error as the suite itself reported
	// it, before Assemble added its own.
	suiteErrors := make(map[string]string, len(r.Evidence.Scenarios))
	for _, s := range r.Evidence.Scenarios {
		suiteErrors[s.ID] = s.Error
	}
	holdouts := 0
	for _, s := range r.Scenarios {
		bs := BuilderScenario{ID: s.ID, Outcome: s.Outcome}
		if s.Holdout {
			holdouts++
			bs.ID = fmt.Sprintf("holdout-%d", holdouts)
		} else if s.Detail != nil {
			bs.Error = s.Detail.Error
			if s.Detail.Result != nil {
				bs.Checks = s.Detail.Result.Checks
				if s.Outcome == OutcomeHarnessError && suiteErrors[s.ID] == "" {
					// Assemble wrote this error: show which check failed,
					// never the check's own words.
					fault := checkArtifacts(r.Binding.Artifacts, s.Detail.Result.Artifacts)
					bs.Error = artifactFaultSentences[fault.kind]
				}
			}
		}
		b.Scenarios = append(b.Scenarios, bs)
	}
	return b
}

// Approve checks, at promotion time, that report still describes current. It
// re-derives the whole report from its binding, suite declaration and
// evidence, so a stored outcome, count or verdict that does not follow from
// the evidence is rejected; the re-derived report must have passed; and every
// binding field must equal the change being promoted. Any difference fails
// with ErrStaleReport; the report stays readable but cannot authorize the
// changed operation.
func Approve(current Binding, report Report) error {
	rebuilt, err := Assemble(report.Binding, report.Suite, report.Evidence, report.SuiteExit)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrStaleReport, err)
	}
	supplied, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	derived, err := json.Marshal(rebuilt)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if !bytes.Equal(supplied, derived) {
		return fmt.Errorf("%w: report content does not match what its evidence derives", ErrStaleReport)
	}
	if !rebuilt.Passed {
		return fmt.Errorf("%w: report did not pass", ErrStaleReport)
	}
	if diff := report.Binding.differences(current); len(diff) > 0 {
		return fmt.Errorf("%w: changed %s", ErrStaleReport, strings.Join(diff, ", "))
	}
	return nil
}

// DecodeReport reads one report written by Run (or `hbay verify`) from r. It
// rejects unknown fields and anything after the report, so a file that is not
// exactly one report fails instead of being partly read. Decoding proves
// nothing about the report's content; Approve checks that.
func DecodeReport(r io.Reader) (Report, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var report Report
	if err := dec.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("decode verification report: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Report{}, errors.New("decode verification report: data after the report")
	}
	return report, nil
}

// checkArtifacts requires the scenario to report exactly the binding's
// candidate binaries, each once, with the bound digests, and returns the
// first check that fails, or the zero artifactFault when none does. Assemble
// calls it to decide a scenario's outcome and ForBuilders to name the failed
// check.
//
// The checks run in a fixed order, so a record that fails several always
// yields the same fault and the same detail. A record that cannot be read
// comes first: an artifact without a sha256 digest, then a binary recorded
// twice, each the first such artifact in reported order. Then the bound
// binaries, which are what a report vouches for: one the scenario did not
// record, then one recorded with another digest, each the first by name. A
// recorded binary the binding does not name comes last, again the first by
// name: it matters only once every bound binary is accounted for.
func checkArtifacts(want map[string]string, reported []eval.Artifact) artifactFault {
	got := map[string]string{}
	var twice artifactFault
	for _, a := range reported {
		name, ok := strings.CutPrefix(a.Name, artifactPrefix)
		if !ok {
			continue
		}
		digest, ok := strings.CutPrefix(a.URI, "sha256:")
		if !ok {
			return artifactFault{
				kind:   artifactDigestUnreadable,
				detail: fmt.Sprintf("artifact %s has no sha256 URI", a.Name),
			}
		}
		if _, dup := got[name]; dup {
			if twice.kind == "" {
				twice = artifactFault{
					kind:   artifactReportedTwice,
					detail: fmt.Sprintf("scenario reported binary %q twice", name),
				}
			}
			continue
		}
		got[name] = digest
	}
	if twice.kind != "" {
		return twice
	}
	bound := slices.Sorted(maps.Keys(want))
	for _, name := range bound {
		if _, ok := got[name]; !ok {
			return artifactFault{
				kind:   artifactMissing,
				detail: fmt.Sprintf("scenario did not report executing bound binary %q", name),
			}
		}
	}
	for _, name := range bound {
		if got[name] != want[name] {
			return artifactFault{
				kind: artifactDigestMismatch,
				detail: fmt.Sprintf("scenario executed %s with digest %s, bound %s",
					name, got[name], want[name]),
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(got)) {
		if _, ok := want[name]; !ok {
			return artifactFault{
				kind:   artifactUndeclared,
				detail: fmt.Sprintf("scenario executed unbound binary %q", name),
			}
		}
	}
	return artifactFault{}
}

// validate rejects malformed assembly inputs.
func validate(binding Binding, suite Suite, evidence eval.Report) error {
	if evidence.SuiteID != suite.ID || binding.SuiteID != suite.ID {
		return fmt.Errorf("report suite %q, binding suite %q, declared suite %q differ",
			evidence.SuiteID, binding.SuiteID, suite.ID)
	}
	if suite.Environment == "" || binding.Environment != suite.Environment {
		return fmt.Errorf("binding environment %q does not match the declared environment %q",
			binding.Environment, suite.Environment)
	}
	if len(suite.Scenarios) == 0 {
		return errors.New("suite declares no scenarios")
	}
	if len(binding.Artifacts) == 0 {
		return errors.New("binding names no candidate binaries")
	}
	declared := map[string]bool{}
	for _, s := range suite.Scenarios {
		if s.ID == "" {
			return errors.New("suite declares a scenario with an empty ID")
		}
		if declared[s.ID] {
			return fmt.Errorf("suite declares scenario %q twice", s.ID)
		}
		declared[s.ID] = true
	}
	seen := map[string]bool{}
	for _, s := range evidence.Scenarios {
		if !declared[s.ID] {
			return fmt.Errorf("evidence reports undeclared scenario %q", s.ID)
		}
		if seen[s.ID] {
			return fmt.Errorf("evidence reports scenario %q twice", s.ID)
		}
		seen[s.ID] = true
	}
	return nil
}

// computeDigest hashes the report's canonical JSON with Digest cleared.
func (r Report) computeDigest() (string, error) {
	r.Digest = ""
	b, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("encode report: %w", err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// add tallies one outcome.
func (c *Counts) add(o Outcome) {
	switch o {
	case OutcomePassed:
		c.Passed++
	case OutcomeFailed:
		c.Failed++
	case OutcomeNotRun:
		c.NotRun++
	case OutcomeHarnessError:
		c.HarnessError++
	}
}

// differences lists the binding fields that differ from other.
func (b Binding) differences(other Binding) []string {
	var diff []string
	if b.CandidateCommit != other.CandidateCommit {
		diff = append(diff, "candidate commit")
	}
	if b.RequestRevision != other.RequestRevision {
		diff = append(diff, "request revision")
	}
	if b.SuiteID != other.SuiteID || b.SuiteCommit != other.SuiteCommit {
		diff = append(diff, "evaluator suite")
	}
	if !maps.Equal(b.Artifacts, other.Artifacts) {
		diff = append(diff, "candidate binaries")
	}
	if b.Environment != other.Environment {
		diff = append(diff, "environment")
	}
	return slices.Clip(diff)
}
