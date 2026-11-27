// The approval stage presents the selected run's Story and the owner's verdict.
// The fixed frame owns its action; this stage never infers approval admission.
import React from "react";
import {
  ApprovalItem, BuilderVerificationReport, StartedBuild, StorySummaryItem,
  VerificationReview, VerificationScenario, VerificationStatusResult,
} from "../types";
import { SavedResultCommits } from "./build";
import { TestCoverage } from "./test-coverage";
import { RunEvidenceLimits, RunTestBinding } from "./run-evidence";
import { Alert, Button } from "./shadcn";

// ApprovalStageProps binds evidence and currentness to one selected run.
export interface ApprovalStageProps {
  run: VerificationStatusResult | null;
  newerCheck: boolean;
  review: VerificationReview | null;
  fresh: boolean;
  loading: boolean;
  busy: boolean;
  error: string | null;
  historyLoading: boolean;
  historyError: string | null;
  approvalError: string | null;
  unavailableReason: string | null;
  rawDetails: React.ReactNode;
  mergeAction: React.ReactNode;
  build: StartedBuild | null;
  unsaved: boolean;
  savedRecord: { approval: ApprovalItem; requestRevision: string; reason: string | null } | null;
  onRefresh: () => void;
  onOpenProof: () => void;
  onOpenAgreement: () => void;
}

// outcomeLabels separates assertion failure, missing execution and harness failure.
const outcomeLabels: Record<VerificationScenario["outcome"], string> = {
  passed: "Passed", failed: "Failed", not_run: "Not run", harness_error: "Could not evaluate",
};

// ApprovalStage is Highbay-authored and preserves readable evidence during refresh.
export function ApprovalStage(props: ApprovalStageProps) {
  const { run, review } = props;
  if (run === null) return <section className="highbay-approval-stage" id="highbay-approval-stage" aria-labelledby="highbay-cp5-title">
    <h2 id="highbay-cp5-title" tabIndex={-1}>Review a verification result</h2>
    <p>Select a saved run in Verify to review its agreed Story, test results and approval requirements.</p>
    {props.savedRecord !== null ? <>
      <p>No verification run is selected. The saved approval remains readable below.</p>
      <SavedApprovalRecord record={props.savedRecord} />
    </> : null}
    {props.historyLoading ? <p role="status">Reading saved verification runs. Current build membership is unknown.</p> : null}
    {props.historyError !== null ? <Alert variant="warning" role="status">
      Saved verification history unavailable: {props.historyError} Current build membership is unknown.
    </Alert> : null}
    {props.error !== null ? <Alert id="highbay-cp5-read-error" variant="destructive" role="alert">{props.error}</Alert> : null}
    <Button id="highbay-cp5-refresh-approval" disabled={props.busy} onClick={props.onRefresh}>Check current approval</Button>
    <Button onClick={props.onOpenProof}>Open saved runs</Button>
    {props.mergeAction}
  </section>;
  const report = run.state.type === "completed" ? run.state.value.report : null;
  const approval = review?.approval;
  const saved = approval?.state === "recorded" ? approval :
    approval?.state === "unverified" ? approval.approval : null;
  const heading = approval?.state === "recorded" ? "Approval recorded" :
    approval?.state === "unverified" ? "Saved approval needs checking" :
    run.state.type === "running" ? "The check is running" :
    run.state.type === "not_completed" ? "The check could not finish" :
    report?.passed ? "The check passed" : "The check did not pass";
  const context = review?.story_context;
  const stories = context?.state === "recorded" ? context.stories : null;
  const mapping = context?.state === "recorded" ? context.mapping : null;
  const coverage = context?.state === "recorded" ? context.test_coverage : null;
  const names = new Set(mapping?.state === "mapped" ? mapping.required_tests.map((test) => test.scenario) : []);
  const other = report?.scenarios.filter((test) => !names.has(test.id)) ?? [];

  return <section className="highbay-approval-stage" id="highbay-approval-stage"
    aria-labelledby="highbay-cp5-title" aria-busy={props.loading || props.busy ? "true" : undefined}
    data-run-id={run.run_id} data-request-revision={run.request_revision}>
    <div className="highbay-approval-heading">
      <div><h2 id="highbay-cp5-title" tabIndex={-1}>{heading}</h2>
        <p>{approval?.state === "blocked" ? "Approval is unavailable for this result. Its saved evidence remains below." :
          approval?.state === "recorded" ? "Your permission is recorded for these verified inputs." :
          approval?.state === "unverified" ? "The saved approval cannot currently be verified." :
          "Review this saved result before deciding whether to approve it."}</p></div>
      <div className="highbay-field-row">
        <Button id="highbay-cp5-refresh-approval" disabled={props.loading || props.busy}
          onClick={props.onRefresh}>Refresh result</Button>
      </div>
    </div>
    {props.newerCheck ? <p role="status">A newer check is available. Review the latest check to see its result;
      this saved result remains selected until you do.</p> : null}
    <dl className="highbay-approval-binding">
      <div><dt>Agreement</dt><dd><code>{run.request_revision.slice(0, 12)}</code></dd></div>
      <div><dt>Builder’s commit</dt><dd><code>{run.candidate_commit.slice(0, 12)}</code></dd></div>
      <div><dt>Selected tests</dt><dd><RunTestBinding run={run} review={review} /></dd></div>
      <div><dt>Run</dt><dd><code>{run.run_id.slice(0, 12)}</code></dd></div>
    </dl>
    {props.unsaved ? <Alert id="highbay-cp5-approval-dirty-warning" variant="warning" role="status">
      Your unsaved agreement edits are kept. This saved run describes its recorded agreement.
      Saving different answers creates a new revision and clears its approval.
      <Button onClick={props.onOpenAgreement}>Return to your edits</Button>
    </Alert> : null}
    {!props.fresh ? <p className="highbay-approval-notice" id="highbay-cp5-observation-status" role="status">
      {props.loading ? "Reading the owner’s current approval requirements…" :
        "This evidence is retained. Refresh the result to check current approval requirements."}
    </p> : null}
    {props.error !== null ? <Alert id="highbay-cp5-read-error" variant="destructive" role="alert">
      <strong>Could not refresh this result. </strong>{props.error}
    </Alert> : null}
    {props.unavailableReason !== null ? <Alert id="highbay-cp5-unavailable" variant="warning" role="status">
      <strong>Approval is unavailable on this server. </strong>{props.unavailableReason}
      <p>Start Highbay from a project directory so verification is configured.</p>
    </Alert> : null}
    {props.approvalError !== null ? <Alert id="highbay-cp5-approve-error" variant="destructive" role="alert" tabIndex={-1}>
      <strong>Approval was not recorded. </strong>{props.approvalError}
      {run.candidate_binaries_dir !== undefined ? <p>Binaries built by this run: <code>{run.candidate_binaries_dir}</code></p> : null}
    </Alert> : null}
    {approval?.state === "blocked" ? <Alert id="highbay-cp5-approve-blocked" variant="warning" role="status" tabIndex={-1}>
      <strong>Approval is unavailable for this result. </strong>{approval.reason}
    </Alert> : null}
    {approval?.state === "unverified" ? <Alert id="highbay-cp5-unverified-approval-reason" variant="warning" role="status">
      {approval.reason}
    </Alert> : null}
    {run.state.type === "not_completed" ? <Alert variant="warning" role="status">
      <strong>{run.state.value.kind === "interrupted" ? "Verification was interrupted. " : "Verification stopped. "}</strong>
      {run.state.value.summary} <span>Stage: {run.state.value.stage}.</span>
      <p>No completed report is available; individual flow outcomes are unknown.</p>
    </Alert> : null}
    <div className="highbay-approval-evidence">
      <div>
        <section aria-labelledby="highbay-approval-flows-title">
          <h3 id="highbay-approval-flows-title">Agreed flows and their tests</h3>
          {stories === null ? <p>{context?.state === "not_recorded"
            ? "Story context was not recorded for this run. Its saved test results remain available in Test details."
            : "Reading the Story recorded for this run…"}</p> : stories.length === 0 ?
            <p>This agreement recorded no Story flows. The declared tests below do not establish Story coverage.</p> :
            stories.map((story) => <StoryEvidence key={story.name} story={story} report={report} mapping={mapping} />)}
        </section>
        {review !== null ? coverage?.state === "recorded" ? <TestCoverage id="highbay-approval-coverage" heading="h3"
          answers={coverage.answers} examples={coverage.examples} statements={coverage.coverage_statements} report={report} /> :
          <section id="highbay-approval-coverage" className="highbay-test-coverage" aria-labelledby="highbay-approval-coverage-title">
            <h3 id="highbay-approval-coverage-title">Test writer's stated coverage</h3>
            <p>{run.test_source.source === "written_tests"
              ? "Agreed answers and examples were not recorded for this run. Coverage: Not stated."
              : "This run uses the configured evaluator. Test-writer coverage: Not stated."}</p>
          </section> : null}
        <RunEvidenceLimits id="highbay-approval-exclusions" run={run} review={review} />
        {other.length > 0 ? <section aria-labelledby="highbay-approval-other-tests-title">
          <h3 id="highbay-approval-other-tests-title">{mapping?.state === "mapped" ? "Other declared tests" : "Declared test results"}</h3>
          <ul className="highbay-approval-test-list">{other.map((test) => <li key={test.id}>
            <code>{test.id}</code><span data-outcome={test.outcome}>{outcomeLabels[test.outcome]}</span>
          </li>)}</ul>
        </section> : null}
      </div>
      <aside className="highbay-approval-basis" aria-labelledby="highbay-approval-basis-title">
        <h3 id="highbay-approval-basis-title">The basis of this result</h3>
        <p>{run.test_source.source === "written_tests" ? "Agent-written tests" : "Operator’s evaluator"}</p>
        {report !== null ? <p>{report.counts.passed} of {report.counts.required} declared tests passed.
          {report.suite_failed ? " The suite itself failed." : ""}</p> : <p>No completed report yet.</p>}
        <p>The run’s evidence and commit bindings stay fixed when you change the agreement or choose new tests.</p>
        {saved !== null ? <section id="highbay-cp5-approval" data-approval-state={approval?.state} tabIndex={-1} aria-label="Recorded promotion approval">
          <h4>{approval?.state === "unverified" ? "Saved approval, unverified" : "Approval recorded"}</h4>
          <p>Approval records permission to promote these verified inputs.</p>
          <details><summary>Approval record and artifacts</summary>
            <dl><dt>Environment</dt><dd><code>{saved.environment}</code></dd>
              <dt>Suite</dt><dd><code>{saved.suite_id}</code></dd>
              <dt>Scenarios passed</dt><dd>{saved.scenario_count}</dd></dl>
            <ul>{Object.entries(saved.artifacts).map(([name, digest]) => <li key={name}><code>{name}</code>: <code>{digest}</code></li>)}</ul>
          </details>
        </section> : <p>Approval records permission to promote this result.</p>}
        {props.build?.in_force === false ? <p>This build belongs to an earlier agreement. Review the current agreement before taking its commits.</p> : null}
        <SavedResultCommits candidateCommit={run.candidate_commit}
          testsCommit={run.test_source.source === "written_tests" ? run.suite_commit : null}
          build={props.build} />
        {props.mergeAction}
        <details className="highbay-details" id="highbay-cp5-approval-details">
          <summary>Verified inputs and digests</summary>
          <dl>
            <dt>Change Card</dt><dd><code>{run.change_id}</code></dd>
            <dt>Agreement revision</dt><dd><code>{run.request_revision}</code></dd>
            <dt>Builder’s commit</dt><dd><code>{run.candidate_commit}</code></dd>
            <dt>Tests’ commit</dt><dd><code>{run.suite_commit}</code></dd>
            <dt>Run</dt><dd><code>{run.run_id}</code></dd>
            <dt>Accepted</dt><dd><time dateTime={run.accepted_at}>{run.accepted_at}</time></dd>
            {run.state.type === "completed" ? <>
              <dt>Suite</dt><dd><code>{run.state.value.suite_id}</code></dd>
              <dt>Environment</dt><dd><code>{run.state.value.environment}</code></dd>
              <dt>Report digest</dt><dd><code>{run.state.value.report.report_digest}</code></dd>
            </> : null}
          </dl>
        </details>
      </aside>
    </div>
    <details className="highbay-details highbay-approval-proof" id="highbay-approval-proof">
      <summary>Test details and saved runs</summary>
      {props.rawDetails}
    </details>
  </section>;
}

// SavedApprovalRecord preserves the owner's record when no saved run can be
// loaded; it does not invent Story context or infer current build membership.
function SavedApprovalRecord({ record }: { record: NonNullable<ApprovalStageProps["savedRecord"]> }) {
  const approval = record.approval;
  return <section id="highbay-cp5-approval" data-approval-state={record.reason === null ? "recorded" : "unverified"} tabIndex={-1} aria-label="Saved promotion approval">
    <h3>{record.reason !== null ? "Saved approval, unverified" : "Approval recorded"}</h3>
    {record.reason !== null ? <Alert id="highbay-cp5-unverified-approval-reason" variant="warning" role="status">{record.reason}</Alert> : null}
    <p>Approval records permission to promote the inputs below. Approval alone does not merge or deploy anything.</p>
    <dl>
      <dt>Agreement</dt><dd><code>{record.requestRevision}</code></dd>
      <dt>Builder’s commit</dt><dd><code>{approval.candidate_commit}</code></dd>
      <dt>Tests’ commit</dt><dd><code>{approval.suite_commit}</code></dd>
      <dt>Report digest</dt><dd><code>{approval.report_digest}</code></dd>
      <dt>Suite</dt><dd><code>{approval.suite_id}</code></dd>
      <dt>Environment</dt><dd><code>{approval.environment}</code></dd>
      <dt>Scenarios passed</dt><dd>{approval.scenario_count}</dd>
    </dl>
    <ul>{Object.entries(approval.artifacts).map(([name, digest]) => <li key={name}><code>{name}</code>: <code>{digest}</code></li>)}</ul>
    <p>Read the current build’s taking state before merging these commits.</p>
  </section>;
}

// StoryEvidence joins only the owner's exact relation to safe report IDs.
function StoryEvidence({ story, report, mapping }: {
  story: StorySummaryItem;
  report: BuilderVerificationReport | null;
  mapping: Extract<VerificationReview["story_context"], { state: "recorded" }>["mapping"] | null;
}) {
  const finished = story.flows.filter((flow) => flow.complete);
  if (finished.length === 0) return <p><strong>{story.name}</strong> has no finished flows.</p>;
  return <div className="highbay-approval-story">
    <h4>{story.name}</h4>
    <table className="highbay-flow-evidence" role="table">
      <thead><tr><th scope="col">Finished flow</th><th scope="col">Required test</th><th scope="col">Run result</th></tr></thead>
      <tbody>{finished.map((flow) => {
        const test = mapping?.state === "mapped"
          ? mapping.required_tests.find((item) => item.story === story.name && item.flow_key === flow.key) : undefined;
        const result = test !== undefined ? report?.scenarios.find((item) => item.id === test.scenario) : undefined;
        const outcome = result !== undefined ? outcomeLabels[result.outcome] : report === null
          ? "No completed result" : test === undefined ? "Coverage not recorded" : "Not reported";
        return <tr key={flow.key} role="row">
          <th scope="row"><strong>{flow.title}</strong>
            {flow.steps.filter((step) => step.kind === "outcome").map((step) => <p key={step.key}>{step.summary}</p>)}
          </th>
          <td data-label="Required test">{test !== undefined ? <code>{test.scenario}</code> :
            mapping?.state === "not_recorded_for_evaluator" ? "Mapping not recorded for this evaluator" : "Required test mapping is missing"}</td>
          <td data-label="Run result" data-outcome={result?.outcome}>{outcome}</td>
        </tr>;
      })}</tbody>
    </table>
  </div>;
}
