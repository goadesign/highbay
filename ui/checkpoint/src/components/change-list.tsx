// change-list.tsx lists the project's saved Change Cards (with an explicit
// + New change CTA when cards exist) or delegates to the Moment-0 Launch
// workspace when the project has no saved changes yet.

import React from "react";
import { ChangeList, ChangeListItem, LatestVerificationItem } from "../types";
import { Alert, Badge, Button, Card } from "./shadcn";

// ChangeListViewProps supplies the last list the server returned, the Moment-0
// launch slot, and actions to open an existing change or start a new one.
export interface ChangeListViewProps {
  list: ChangeList | null;
  loading: boolean;
  error: string | null;
  openError: string | null;
  openChangeId: string | null;
  dirtyChangeIds: ReadonlySet<string>;
  loadingChangeIds: ReadonlySet<string>;
  disabled: boolean;
  launchWorkspace: React.ReactNode;
  onNewChange: () => void;
  onOpen: (changeId: string) => void;
  onRetry: () => void;
}

// verificationLabels describes saved history without claiming current proof.
const verificationLabels: Record<LatestVerificationItem["state"], string> = {
  running: "Latest saved run: in progress",
  passed: "Latest saved run: passed",
  failed: "Latest saved run: did not pass",
  not_completed: "Latest saved run: did not finish",
};

// ChangeListView renders every listed change as one keyboard-operable button,
// most recently saved first, or the Moment-0 launch workspace when empty.
export function ChangeListView({
  list,
  loading,
  error,
  openError,
  openChangeId,
  dirtyChangeIds,
  loadingChangeIds,
  disabled,
  launchWorkspace,
  onNewChange,
  onOpen,
  onRetry,
}: ChangeListViewProps) {
  if (list !== null && list.changes.length === 0 && error === null && openError === null) {
    return <>{launchWorkspace}</>;
  }

  return (
    <Card
      id="highbay-change-list"
      className="highbay-card"
      aria-labelledby="highbay-change-list-title"
      aria-busy={loading ? "true" : undefined}
    >
      <div className="highbay-region-header">
        <h2 id="highbay-change-list-title" tabIndex={-1}>
          Changes
        </h2>
        <div className="highbay-toolbar-actions">
          <Button
            id="highbay-list-new-change"
            variant="secondary"
            disabled={disabled}
            onClick={onNewChange}
          >
            + New change
          </Button>
        </div>
      </div>

      {list?.changes.some((item) => item.latest_verification !== undefined) ? (
        <p id="highbay-change-run-history" className="highbay-muted">
          Run results describe their recorded candidate. The server compares each latest run with
          the agreed revision and selected tests. Open a change, then Verify, to inspect it.
        </p>
      ) : null}

      {error !== null ? (
        <Alert id="highbay-change-list-error" variant="destructive" role="alert">
          <strong>Could not read this project’s changes. </strong>
          {error}
          <div className="highbay-field-row">
            <Button id="highbay-change-list-retry" disabled={loading} onClick={onRetry}>
              Read the changes again
            </Button>
          </div>
        </Alert>
      ) : null}

      {openError !== null ? (
        <Alert id="highbay-change-open-error" variant="destructive" role="alert">
          {openError}
        </Alert>
      ) : null}

      {list === null ? (
        error === null ? <p role="status">Reading this project’s changes...</p> : null
      ) : list.changes.length > 0 ? (
        <ul className="highbay-compact-list" aria-label="Project changes">
          {list.changes.map((item) => (
            <ChangeListRow
              key={item.change_id}
              item={item}
              open={item.change_id === openChangeId}
              unsaved={dirtyChangeIds.has(item.change_id)}
              busy={disabled || loadingChangeIds.has(item.change_id)}
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
  unsaved,
  busy,
  onOpen,
}: {
  item: ChangeListItem;
  open: boolean;
  unsaved: boolean;
  busy: boolean;
  onOpen: (changeId: string) => void;
}) {
  const openLabel = "Open: " + item.summary;
  const inputsDescriptionId = React.useId();
  return (
    <li data-change-id={item.change_id}>
      <div className="highbay-change-row-header">
        <Button
          id={"highbay-open-change-" + item.change_id}
          className="highbay-change-open"
          aria-label={openLabel + " (" + item.change_id + ")"}
          aria-current={open ? "true" : undefined}
          disabled={busy}
          onClick={() => onOpen(item.change_id)}
        >
          {openLabel}
        </Button>
        <Badge variant={item.status === "confirmed" ? "success" : "warning"}>
          {item.status === "confirmed" ? "Confirmed" : "Needs confirmation"}
        </Badge>
        {item.proposed_revision ? (
          <Badge variant="warning" className="highbay-proposal-badge">
            New revision proposed
          </Badge>
        ) : null}
        {unsaved ? <Badge variant="accent">Unsaved edits</Badge> : null}
        {item.latest_verification ? (
          <Badge
            aria-describedby={"highbay-change-run-history " + inputsDescriptionId}
            variant={
              item.latest_verification.inputs_current && item.latest_verification.state === "passed"
                ? "success"
                : "default"
            }
          >
            {verificationLabels[item.latest_verification.state]}
          </Badge>
        ) : (
          <Badge>No saved runs</Badge>
        )}
      </div>
      {item.latest_verification ? (
        <p
          id={inputsDescriptionId}
          className="highbay-muted"
          data-inputs-current={String(item.latest_verification.inputs_current)}
        >
          {item.latest_verification.inputs_current
            ? "Matches the agreed revision and selected tests."
            : "Earlier inputs or no current test selection. Ask the builder for a check of the agreed revision and selected tests."}
        </p>
      ) : null}
      <p className="highbay-muted">
        Saved <time dateTime={item.saved_at}>{new Date(item.saved_at).toLocaleString()}</time>
        {" · "}
        {item.status === "confirmed" ? "Agreed revision" : "Draft revision"}{" "}
        <code>{item.request_revision.slice(0, 12)}</code>
        {item.proposed_revision ? (
          <>
            {" · Proposed revision "}
            <code>{item.proposed_revision.slice(0, 12)}</code>
          </>
        ) : null}
      </p>
      <details className="highbay-details">
        <summary>Details</summary>
        <p>
          change_id: <code>{item.change_id}</code> · request_revision:{" "}
          <code>{item.request_revision}</code>
          {item.proposed_revision ? (
            <>
              {" · proposed revision: "}
              <code>{item.proposed_revision}</code>
            </>
          ) : null}
          {item.latest_verification ? (
            <>
              {" · latest run: "}
              <code>{item.latest_verification.run_id}</code>
            </>
          ) : null}
        </p>
      </details>
    </li>
  );
}
