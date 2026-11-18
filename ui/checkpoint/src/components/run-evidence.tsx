// Saved run evidence keeps the recorded agreement separate from current inputs.
import React from "react";
import type { VerificationReview, VerificationStatusResult } from "../types";

// RunTestBinding identifies this run's selected tests without guessing a report.
export function RunTestBinding({ run, review }: {
  run: VerificationStatusResult;
  review: VerificationReview | null;
}) {
  const context = review?.story_context;
  const coverage = context?.state === "recorded" ? context.test_coverage : null;
  return <span>{run.test_source.source === "written_tests" ? "Tests’ commit" : "Evaluator commit"}:
    {" "}<code>{run.suite_commit.slice(0, 12)}</code>
    {coverage?.state === "recorded" ? " · report " + coverage.report : null}
  </span>;
}

// RunEvidenceLimits renders only the selected run's frozen Story and examples.
// A classification or writer statement never becomes an example-level verdict.
export function RunEvidenceLimits({ id, run, review }: {
  id: string;
  run: VerificationStatusResult;
  review: VerificationReview | null;
}) {
  const context = review?.story_context;
  const stories = context?.state === "recorded" ? context.stories : null;
  const mapping = context?.state === "recorded" ? context.mapping : null;
  const coverage = context?.state === "recorded" ? context.test_coverage : null;
  const drafts = stories?.flatMap((story) => story.flows.filter((flow) => !flow.complete)
    .map((flow) => ({ story: story.name, flow }))) ?? [];
  const judged = coverage?.state === "recorded"
    ? coverage.examples.filter((example) => example.verification_kind === "model_judged") : [];
  return <section id={id} className="surla-approval-exclusions" aria-labelledby={id + "-title"}>
    <h3 id={id + "-title"}>What this run does not establish</h3>
    {drafts.length > 0 ? <><p>These draft flows were excluded from the required tests:</p>
      <ul>{drafts.map(({ story, flow }) => <li key={JSON.stringify([story, flow.key])}>
        <strong>{flow.title}</strong> · {story} · Draft, no required test
      </li>)}</ul></> : null}
    {mapping?.state === "not_recorded_for_evaluator" ? <p>
      Test mapping was not recorded for this evaluator. A passed test does not prove that a particular Story flow was covered.
    </p> : null}
    {judged.length > 0 ? <>
      <p>These agreed examples require judgment. This report records no separate example-level verdict for them:</p>
      <ul>{judged.map((example) => <li key={example.id}>
        <strong>{example.scenario}</strong> · <code>{example.id}</code> · Model-judged check
        <p>Expected: {example.expected_outcome}</p>
      </li>)}</ul>
    </> : coverage?.state !== "recorded" ? <p>
      {context === undefined ? "Reading this run’s saved agreement evidence…" :
        "Agreed examples were not recorded for this run. Their example-level coverage is unknown."}
    </p> : null}
    <p>Verification runs the selected suite’s declared scenarios; it does not separately execute the card’s examples.
      A test writer’s coverage statement does not establish that a model judgment was performed.</p>
    <p>No separate builder <code>check_project</code> or unit-test results are attached to this verification run.</p>
    <p>Passing the declared checks does not establish all product behavior, usability or requirements the tests do not express.</p>
    {run.test_source.source === "written_tests" ? <p>{run.test_source.notice}</p> : null}
  </section>;
}
