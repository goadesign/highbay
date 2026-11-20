// Package components lists the project's saved Change Cards so the user can
// find work and open one change.

import React from "react";
import { ChangeList, ChangeListItem, LatestVerificationItem } from "../types";
import { Alert, Badge, Button, Card } from "./shadcn";

// ChangeListViewProps supplies the last list the server returned and the
// user's way to open one change or read the list again.
export interface ChangeListViewProps {
  // list is the last change list read, or null before the first read ends.
  list: ChangeList | null;
  // loading reflects an outstanding list_changes request.
  loading: boolean;
  // error describes the last failed list read, or null.
  error: string | null;
  // openError describes why the change the user last tried to open could
  // not be read, or null.
  openError: string | null;
  // openChangeId is the change currently open in the editor, if any.
  openChangeId: string | null;
  // busy disables opening while a Change Card request is outstanding.
  busy: boolean;
  // guideConnected and guideInHerdr describe the guide the project reports.
  guideConnected: boolean;
  guideInHerdr: boolean;
  // disabled and showingGuide prevent a duplicate or expired pane request.
  disabled: boolean;
  showingGuide: boolean;
  onShowGuidePane: () => void;
  // onOpen opens one change by its change_id.
  onOpen: (changeId: string) => void;
  // onRetry reads the list again after a failure.
  onRetry: () => void;
}

// verificationLabels describes saved history without claiming current proof.
const verificationLabels: Record<LatestVerificationItem["state"], string> = {
  running: "Latest saved run: in progress",
  passed: "Latest saved run: passed",
  failed: "Latest saved run: did not pass",
  not_completed: "Latest saved run: did not finish",
};

// ChangeListView renders every listed change as one keyboard-operable
// button, most recently saved first, or the empty state that explains how a
// change appears.
export function ChangeListView({
  list,
  loading,
  error,
  openError,
  openChangeId,
  busy,
  guideConnected,
  guideInHerdr,
  disabled,
  showingGuide,
  onShowGuidePane,
  onOpen,
  onRetry,
}: ChangeListViewProps) {
  if (list !== null && list.changes.length === 0 && error === null && openError === null) {
    return (
      <Card id="surla-change-list" className="surla-card surla-welcome-card"
        aria-labelledby="surla-change-list-title" aria-busy={loading ? "true" : undefined}>
        <h2 id="surla-change-list-title">
          {guideConnected ? "Describe what you want to build in your coding agent" : "Start in your coding agent with /surla"}
        </h2>
        <p id="surla-change-list-empty">
          {guideConnected
            ? "Your coding agent drafts the Change Card. It opens here when it’s ready."
            : <>Open your coding agent in this folder and invoke <code>/surla</code> (<code>$surla</code> in Codex)
                to connect it, then describe what you want to build. Your Change Card will appear here automatically.</>}
        </p>
        {guideConnected && guideInHerdr ? (
          <div className="surla-field-row">
            <Button id="surla-empty-show-guide" variant="primary" disabled={disabled || showingGuide}
              aria-busy={showingGuide} onClick={onShowGuidePane}>
              {showingGuide ? "Showing coding agent pane…" : "Show coding agent pane"}
            </Button>
          </div>
        ) : null}
        <ol className="surla-welcome-steps">
          <li><strong>Confirm intent</strong> Answer open questions and lock concrete examples before code is written.</li>
          <li><strong>Build &amp; verify</strong> Surla coordinates implementation and independent checks.</li>
          <li><strong>Approve &amp; merge</strong> Review the verified evidence and merge the change into your branch.</li>
        </ol>
      </Card>
    );
  }
  return (
    <Card
      id="surla-change-list"
      className="surla-card"
      aria-labelledby="surla-change-list-title"
      aria-busy={loading ? "true" : undefined}
    >
      <h2 id="surla-change-list-title">Changes</h2>
      {list?.changes.some((item) => item.latest_verification !== undefined) ? (
        <p id="surla-change-run-history" className="surla-muted">
          Run results describe their recorded candidate. The server compares each latest run
          with the agreed revision and selected tests. Open a change, then Check, to inspect it.
        </p>
      ) : null}
      {error !== null ? (
        <Alert id="surla-change-list-error" variant="destructive" role="alert">
          <strong>Could not read this project's changes. </strong>
          {error}
          <div className="surla-field-row">
            <Button id="surla-change-list-retry" disabled={loading} onClick={onRetry}>
              Read the changes again
            </Button>
          </div>
        </Alert>
      ) : null}
      {openError !== null ? (
        <Alert id="surla-change-open-error" variant="destructive" role="alert">
          {openError}
        </Alert>
      ) : null}
      {list === null ? (
        error === null ? <p role="status">Reading this project's changes...</p> : null
      ) : list.changes.length > 0 ? (
        <ul className="surla-compact-list" aria-label="Project changes">
          {list.changes.map((item) => (
            <ChangeListRow
              key={item.change_id}
              item={item}
              open={item.change_id === openChangeId}
              busy={busy}
              onOpen={onOpen}
            />
          ))}
        </ul>
      ) : null}
    </Card>
  );
}

// ChangeListRow shows one change's summary and state before its identifiers.
function ChangeListRow({
  item,
  open,
  busy,
  onOpen,
}: {
  item: ChangeListItem;
  open: boolean;
  busy: boolean;
  onOpen: (changeId: string) => void;
}) {
  // IDs are application data and may contain whitespace; React owns this
  // accessibility reference so it remains one valid, unique ID token.
  const inputsDescriptionId = React.useId();
  return (
    <li data-change-id={item.change_id}>
      <Button
        id={"surla-open-change-" + item.change_id}
        className="surla-change-open"
        aria-current={open ? "true" : undefined}
        disabled={busy}
        onClick={() => onOpen(item.change_id)}
      >
        {"Open: " + item.summary}
      </Button>{" "}
      <Badge variant={item.status === "confirmed" ? "success" : "warning"}>
        {item.status === "confirmed" ? "Confirmed" : "Needs confirmation"}
      </Badge>{" "}
      {item.proposed_revision ? (
        <>
          <Badge variant="warning" className="surla-proposal-badge">
            New revision proposed
          </Badge>{" "}
        </>
      ) : null}
      {item.latest_verification ? (
        <>
          <Badge
            aria-describedby={"surla-change-run-history " + inputsDescriptionId}
            variant={item.latest_verification.inputs_current && item.latest_verification.state === "passed"
              ? "success" : "default"}
          >
            {verificationLabels[item.latest_verification.state]}
          </Badge>
          <p
            id={inputsDescriptionId}
            className="surla-muted"
            data-inputs-current={String(item.latest_verification.inputs_current)}
          >
            {item.latest_verification.inputs_current
              ? "Matches the agreed revision and selected tests."
              : "Earlier inputs or no current test selection. Ask the builder for a check of the agreed revision and selected tests."}
          </p>
        </>
      ) : (
        <Badge>No saved runs</Badge>
      )}{" "}
      <span className="surla-muted">
        Saved <time dateTime={item.saved_at}>{new Date(item.saved_at).toLocaleString()}</time>
      </span>
      <details className="surla-details">
        <summary>Details</summary>
        <p>
          change_id: <code>{item.change_id}</code> · request_revision:{" "}
          <code>{item.request_revision}</code>
          {item.proposed_revision ? (
            <>
              {" "}
              · proposed revision: <code>{item.proposed_revision}</code>
            </>
          ) : null}
          {item.latest_verification ? (
            <>
              {" "}
              · latest run: <code>{item.latest_verification.run_id}</code>
            </>
          ) : null}
        </p>
      </details>
    </li>
  );
}
