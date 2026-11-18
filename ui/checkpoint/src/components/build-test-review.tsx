// Build test evidence uses the saved build's requirements and exact user findings read.
import React, { useLayoutEffect, useRef } from "react";
import type { StartedBuild } from "../types";
import { Alert, Button } from "./shadcn";
import { TestCoverage } from "./test-coverage";

// TestWriterFacts separates assignment observations, test acceptance and verification.
// Finding text is already correlated with the exact build/verdict by the page.
export function TestWriterFacts({ build, findingTexts, loading, error, disabled, onRead }: {
  build: StartedBuild;
  findingTexts: readonly string[] | null;
  loading: boolean;
  error: string | null;
  disabled: boolean;
  onRead: () => void;
}) {
  const tests = build.test_writer.tests;
  const open = build.taking.state === "building" || build.taking.state === "passed";
  const heading = useRef<HTMLHeadingElement>(null);
  const findings = useRef<HTMLDivElement>(null);
  const restoreFocus = useRef(false);
  useLayoutEffect(() => {
    if (restoreFocus.current && document.activeElement === document.body) heading.current?.focus();
    restoreFocus.current = false;
    const previous = findings.current;
    return () => {
      restoreFocus.current = previous?.contains(document.activeElement) ?? false;
    };
  }, [tests.state, open, loading, error, findingTexts]);
  let progress: string;
  switch (tests.state) {
    case "not_used": progress = "not needed; this project uses its configured tests"; break;
    case "assigned": progress = build.test_writer.read
      ? "assignment read, no tests reported yet" : "assignment unread, no tests reported yet"; break;
    case "reported": progress = build.test_writer.read
      ? "assignment read; Surla is checking its reported tests" : "assignment unread; its reported tests are being checked"; break;
    case "needs_changes": progress = build.test_writer.read
      ? "assignment read; findings to address" : "assignment unread; new findings ready to read"; break;
    case "accepted": progress = build.test_writer.read
      ? "assignment read; accepted tests" : "assignment unread; acceptance ready to read"; break;
    case "could_not_run": progress = build.test_writer.read
      ? "assignment read; check could not run" : "assignment unread; reason ready to read"; break;
  }
  const title = tests.state === "needs_changes" ? "Tests need changes" :
    tests.state === "could_not_run" ? "The tests could not be checked" :
    tests.state === "accepted" ? "Tests accepted" :
    tests.state === "reported" ? "Checking the reported tests" : "Tests for this build";
  const findingCount = tests.state === "needs_changes" ? tests.finding_count : 0;
  return <section id="surla-build-test-writer" className="surla-build-test-review" data-state={tests.state}
    aria-labelledby="surla-build-tests-title">
    <h4 id="surla-build-tests-title" tabIndex={-1} ref={heading}>{title}</h4>
    <p>Test writer: {progress}</p>
    {!build.in_force ? <p>This build belongs to an earlier agreement.</p> : null}
    <p className="surla-build-test-binding">Built agreement <code>{build.request_revision.slice(0, 12)}</code>
      {" · started "}<time dateTime={build.started_at}>{new Date(build.started_at).toLocaleString()}</time>
      {" · report "}{build.test_writer.report}</p>
    {tests.state === "could_not_run" ? <Alert variant="warning">{tests.reason} {tests.step}</Alert> : null}
    {tests.state === "accepted" ? <p id="surla-build-not-judged">{tests.not_judged}</p> : null}
    {tests.state !== "not_used" ? <div className="surla-build-test-evidence">
      <section aria-labelledby="surla-build-required-title">
        <h5 id="surla-build-required-title">Required tests</h5>
        <p>{build.required_test_count} finished Story {build.required_test_count === 1 ? "flow requires a test" : "flows require tests"}.</p>
        {build.required_tests.length === 0 ? <p>No finished Story flow requires a test. Draft flows require none.</p> :
          <ul id="surla-build-required-tests" className="surla-build-required-tests">
            {build.required_tests.map((test) => <li key={test.scenario}>
              <strong>{test.story}</strong><span>Flow <code>{test.flow_key}</code></span>
              <span>Scenario <code>{test.scenario}</code></span>
            </li>)}
          </ul>}
      </section>
      {tests.state === "needs_changes" ? <div id="surla-build-findings" ref={findings} aria-busy={open && loading}>
        <h5>What Surla told the test writer</h5>
        {!open || loading || error !== null || findingTexts === null ?
          <p>{findingCount} {findingCount === 1 ? "finding" : "findings"}</p> : null}
        {open && findingCount > 0 ? <>
          {loading ? <p role="status">Reading findings…</p> : null}
          {error !== null ? <Alert id="surla-build-findings-error" variant="destructive" role="alert">
            Could not read findings. {error}
          </Alert> : null}
          {!loading && (error !== null || findingTexts === null) ?
            <Button id="surla-build-findings-retry" disabled={disabled} onClick={onRead}>
              {error !== null ? "Retry findings read" : "Read findings"}
            </Button> : null}
          {!loading && error === null && findingTexts !== null ? <details className="surla-details" open>
            <summary>{findingTexts.length} {findingTexts.length === 1 ? "finding" : "findings"}</summary>
            <div className="surla-build-finding-text" tabIndex={0} role="region" aria-label="Surla's findings">
              <ul>{findingTexts.map((text, index) => <li key={index}>{text}</li>)}</ul>
            </div>
          </details> : null}
        </> : null}
      </div> : null}
    </div> : null}
    {tests.state === "accepted" ? <TestCoverage id="surla-build-coverage" heading="h5"
      answers={build.answers} examples={build.examples} statements={build.coverage_statements} report={null} /> :
      tests.state !== "not_used" ? <p>No accepted tests for this report yet.</p> : null}
  </section>;
}
