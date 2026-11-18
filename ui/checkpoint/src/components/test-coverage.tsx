// Coverage statements preserve the writer's attribution and frozen agreement.
import React from "react";
import type { BuilderVerificationReport, ConfirmedAnswer, ExampleItem, TestCoverageStatement } from "../types";

// TestCoverage shows explicit relations only; a scenario result does not prove
// the writer's explanation. Callers supply the same build or run's whole basis.
export function TestCoverage({ id, heading, answers, examples, statements, report }: {
  id: string;
  heading: "h3" | "h5";
  answers: readonly ConfirmedAnswer[];
  examples: readonly ExampleItem[];
  statements: readonly TestCoverageStatement[];
  report: BuilderVerificationReport | null;
}) {
  const Heading = heading;
  // statedRelations never guesses links from question, example or test text.
  function statedRelations(rows: readonly TestCoverageStatement[]) {
    if (rows.length === 0) return <p>Not stated</p>;
    return <ul>{rows.map((row) => {
      const result = report?.scenarios.find((scenario) => scenario.id === row.scenario);
      const outcome = result?.outcome === "passed" ? "Passed" :
        result?.outcome === "failed" ? "Failed" : result?.outcome === "not_run" ? "Not run" :
        result?.outcome === "harness_error" ? "Could not evaluate" : "Not reported";
      return <li key={row.scenario}>
        <p>Scenario <code>{row.scenario}</code>{report !== null ? " · Run result: " + outcome : null}</p>
        <p>{row.explanation}</p>
      </li>;
    })}</ul>;
  }
  return <section id={id} className="surla-test-coverage" aria-labelledby={id + "-title"}>
    <Heading id={id + "-title"}>Test writer's stated coverage</Heading>
    <p>These statements come from the test writer. Surla did not judge them.</p>
    {answers.length + examples.length === 0 ? <p>This agreement has no confirmed answers or examples.</p> : <dl>
      {answers.map((answer) => <React.Fragment key={"question-" + answer.id}>
        <dt>{answer.question}</dt>
        <dd><p>Agreed answer: {answer.answer}</p>
          {statedRelations(statements.filter((row) => row.question_ids.includes(answer.id)))}
        </dd>
      </React.Fragment>)}
      {examples.map((example) => <React.Fragment key={"example-" + example.id}>
        <dt>{example.scenario}</dt>
        <dd><p>{example.verification_kind === "model_judged" ? "Model-judged check" : "Deterministic check"}.
          {" "}This label describes the check needed, not a recorded outcome.</p>
          <p>Input: {example.input_summary}</p><p>Expected: {example.expected_outcome}</p>
          {statedRelations(statements.filter((row) => row.example_ids.includes(example.id)))}
        </dd>
      </React.Fragment>)}
    </dl>}
  </section>;
}
