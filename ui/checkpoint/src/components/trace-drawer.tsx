// The diagnostic view uses the checkpoint's existing controls and typography.
// Reading a page never takes ownership of the Change Card's editable state.
import React from "react";
import { TraceList, TraceSpan } from "../types";
import { Alert, Badge, Button } from "./shadcn";

// TraceFilter adds the API's all-status option to the generated span statuses.
export type TraceFilter = "all" | Exclude<TraceSpan["status"], "unset">;

// TraceDrawerProps carries only this view's current page and request actions.
interface TraceDrawerProps {
  open: boolean;
  filter: TraceFilter;
  page: TraceList | null;
  error: string | null;
  busy: boolean;
  onToggle: () => void;
  onFilter: (filter: TraceFilter) => void;
  onRefresh: () => void;
  onOlder: () => void;
}

// TraceDrawer renders a nonmodal, keyboard-accessible view beside the editor.
// Unset remains visible as unset; a missing error status never proves success.
export function TraceDrawer(props: TraceDrawerProps) {
  return (
    <div className="highbay-trace-view">
      <section
        id="highbay-trace-drawer"
        hidden={!props.open}
        aria-labelledby="highbay-trace-title"
        aria-busy={props.busy}
        className="highbay-trace-panel"
      >
        <h2 id="highbay-trace-title">Operation diagnostics</h2>
        <Button id="highbay-trace-close" onClick={props.onToggle}>Close diagnostics</Button>
        <p className="highbay-muted">
          Saved diagnostics for your instrumented operations, newest first.
          Raw errors and inputs stay with the server operator.
        </p>
        <div className="highbay-trace-actions">
          <div role="group" aria-label="Trace status filter" className="highbay-trace-actions">
            <Button variant="pill" aria-pressed={props.filter === "error"} disabled={props.busy} onClick={() => props.onFilter("error")}>
              Errors
            </Button>
            <Button variant="pill" aria-pressed={props.filter === "all"} disabled={props.busy} onClick={() => props.onFilter("all")}>
              All statuses
            </Button>
            <Button variant="pill" aria-pressed={props.filter === "ok"} disabled={props.busy} onClick={() => props.onFilter("ok")}>
              Without error status
            </Button>
          </div>
          <Button id="highbay-trace-refresh" disabled={props.busy} onClick={props.onRefresh}>Refresh</Button>
        </div>
        {props.busy && <p role="status">Loading diagnostics…</p>}
        {props.error && (
          <Alert role="alert" variant="destructive">
            <p>{props.error}</p>
            <Button disabled={props.busy} onClick={props.onRefresh}>Retry from newest</Button>
          </Alert>
        )}
        {props.page && (
          <>
            {props.page.spans.length === 0 ? (
              <p role="status">
                {props.page.has_more
                  ? "No matching diagnostics in this part of history. Load older traces to continue."
                  : "No matching diagnostics in the remaining history. Refresh after another operation."}
              </p>
            ) : (
              <ol className="highbay-trace-list" aria-label="Completed operation spans">
                {props.page.spans.map((span) => <TraceRow key={span.trace_id + ":" + span.span_id} span={span} />)}
              </ol>
            )}
            {props.page.has_more && (
              <Button id="highbay-trace-load-more" disabled={props.busy} onClick={props.onOlder}>
                Load older traces
              </Button>
            )}
            <p className="highbay-muted">Each page replaces the previous page. Refresh returns to the newest records.</p>
          </>
        )}
      </section>
    </div>
  );
}

// TraceRow puts the operation and safe summary before expandable identifiers.
function TraceRow({ span }: { span: TraceSpan }) {
  return (
    <li>
      <div className="highbay-trace-heading">
        <strong>{span.name || "Unnamed operation"}</strong>
        <span>
          <Badge>{callerLabel(span.attributes["highbay.caller_kind"])}</Badge>{" "}
          {span.status === "error" ? "Error" : span.status === "ok" ? "OK" : "Status unset"}
        </span>
      </div>
      <p>{span.summary}</p>
      <p className="highbay-muted">
        <time dateTime={span.end_time}>{new Date(span.end_time).toLocaleString()}</time>
        {" · "}{span.duration_ms.toLocaleString()} ms
      </p>
      {span.shortened && <p className="highbay-muted">Some text was shortened. Ask the operator for full telemetry if needed.</p>}
      <details className="highbay-details">
        <summary>Identifiers and details</summary>
        <dl className="highbay-trace-details">
          <dt>Trace</dt><dd><code>{span.trace_id}</code></dd>
          <dt>Span</dt><dd><code>{span.span_id}</code></dd>
          {span.parent_span_id && <><dt>Parent span</dt><dd><code>{span.parent_span_id}</code></dd></>}
          {Object.entries(span.attributes).map(([key, value]) => (
            <React.Fragment key={key}><dt>{key}</dt><dd><code>{value}</code></dd></React.Fragment>
          ))}
        </dl>
      </details>
    </li>
  );
}

// callerLabel translates the authenticated role recorded with the diagnostic.
function callerLabel(kind: string | undefined): string {
  switch (kind) {
    case "builder": return "Builder";
    case "guide": return "Coding agent";
    case "test_writer": return "Test writer";
    case "user": return "You";
    case "management": return "Project management";
    default: return "Caller unknown";
  }
}
