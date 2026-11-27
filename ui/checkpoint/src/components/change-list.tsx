// Package components lists the project's saved Change Cards and provides the
// browser-first Describe prompt composer when the project has no changes yet.

import React from "react";
import { ChangeList, ChangeListItem, LatestVerificationItem, TeamRoleState } from "../types";
import { Alert, Badge, Button, Card } from "./shadcn";

// ChangeListViewProps supplies the last list the server returned, the Describe
// prompt composer slot, and actions to open a change or terminal.
export interface ChangeListViewProps {
  list: ChangeList | null;
  loading: boolean;
  error: string | null;
  openError: string | null;
  openChangeId: string | null;
  busy: boolean;
  guideConnected: boolean;
  guideHasTerminal: boolean;
  guideState: TeamRoleState["state"] | null;
  disabled: boolean;
  describeComposer: React.ReactNode;
  onOpenGuideTerminal: (invoker: HTMLElement | null) => void;
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
// most recently saved first, or the Describe welcome workspace when empty.
export function ChangeListView({
  list,
  loading,
  error,
  openError,
  openChangeId,
  busy,
  guideConnected,
  guideHasTerminal,
  guideState,
  disabled,
  describeComposer,
  onOpenGuideTerminal,
  onOpen,
  onRetry,
}: ChangeListViewProps) {
  if (list !== null && list.changes.length === 0 && error === null && openError === null) {
    const empty = emptyChangeState(list, guideConnected, guideHasTerminal, guideState);
    return (
      <Card
        id="highbay-change-list"
        className="highbay-card highbay-welcome-card"
        aria-labelledby="highbay-change-list-title"
        aria-busy={loading ? "true" : undefined}
      >
        <h2 id="highbay-change-list-title">{empty.title}</h2>
        <p id="highbay-change-list-empty" role={empty.liveStatus ? "status" : undefined}>
          {empty.body}
        </p>
        {empty.showComposer ? describeComposer : null}
        {guideConnected && guideHasTerminal ? (
          <div className="highbay-field-row">
            <Button
              id="highbay-empty-open-guide-terminal"
              variant={empty.primaryTerminal ? "primary" : "default"}
              disabled={disabled}
              onClick={(event) => onOpenGuideTerminal(event.currentTarget)}
            >
              Open Guide terminal
            </Button>
          </div>
        ) : null}
        {empty.showSteps ? (
          <ol className="highbay-welcome-steps">
            <li>
              <strong>Confirm intent</strong> Answer open questions and lock concrete examples before
              code is written.
            </li>
            <li>
              <strong>Build &amp; verify</strong> Highbay coordinates implementation and independent
              checks in parallel.
            </li>
            <li>
              <strong>Approve &amp; merge</strong> Review the verified evidence and merge the change
              into your branch.
            </li>
          </ol>
        ) : null}
      </Card>
    );
  }

  return (
    <Card
      id="highbay-change-list"
      className="highbay-card"
      aria-labelledby="highbay-change-list-title"
      aria-busy={loading ? "true" : undefined}
    >
      <h2 id="highbay-change-list-title">Changes</h2>
      {list?.changes.some((item) => item.latest_verification !== undefined) ? (
        <p id="highbay-change-run-history" className="highbay-muted">
          Run results describe their recorded candidate. The server compares each latest run with
          the agreed revision and selected tests. Open a change, then Check, to inspect it.
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
              busy={busy}
              onOpen={onOpen}
            />
          ))}
        </ul>
      ) : null}
    </Card>
  );
}

// emptyChangeState selects the empty-project heading, live progress copy, and
// whether the browser Describe composer is shown.
function emptyChangeState(
  list: ChangeList,
  guideConnected: boolean,
  guideHasTerminal: boolean,
  guideState: TeamRoleState["state"] | null
): {
  title: string;
  body: React.ReactNode;
  liveStatus: boolean;
  primaryTerminal: boolean;
  showComposer: boolean;
  showSteps: boolean;
} {
  if (list.initializing_phase !== undefined) {
    return {
      title: "Preparing your project",
      body: list.initializing_phase,
      liveStatus: true,
      primaryTerminal: false,
      showComposer: false,
      showSteps: false,
    };
  }
  if (!guideConnected) {
    return {
      title: "Describe what you want to build",
      body: "Type your goal below to launch Guide automatically in this project, or run /highbay ($highbay in Codex) from an existing coding agent session. Your Change Card will appear here automatically.",
      liveStatus: false,
      primaryTerminal: false,
      showComposer: true,
      showSteps: true,
    };
  }
  const draftState = list.draft_state;
  if (draftState === "evaluating_story") {
    return {
      title: "Drafting your Change Card",
      body: "Checking the Story and Goa design. Your Change Card will open here automatically when it’s ready.",
      liveStatus: true,
      primaryTerminal: false,
      showComposer: false,
      showSteps: false,
    };
  }
  if (guideState === "needs_you") {
    return {
      title: "Guide needs you",
      body: guideHasTerminal
        ? "Open Guide’s terminal to answer its prompt, or reply in Team conversation."
        : "Reply to Guide in Team conversation or in your attached editor session.",
      liveStatus: true,
      primaryTerminal: guideHasTerminal,
      showComposer: false,
      showSteps: false,
    };
  }
  if (guideState === "starting") {
    return {
      title: "Starting Guide",
      body: "Waiting for your Guide coding agent to start. Your Change Card will open here when ready.",
      liveStatus: true,
      primaryTerminal: false,
      showComposer: false,
      showSteps: false,
    };
  }
  if (guideState === "working") {
    const body =
      draftState === "story_ready"
        ? "Story and design checked — Guide is preparing the Change Card. It will open here automatically."
        : draftState === "story_failed"
        ? "The last Story check reported an issue — Guide is updating the draft. Your Change Card will open here automatically."
        : draftState === "editing" || draftState === "initialized"
        ? "Starter project ready — Guide is writing the service design and Story. Your Change Card will open here automatically."
        : "Guide is working on your request. Progress and your Change Card will appear here automatically.";
    return {
      title: draftState !== undefined ? "Drafting your Change Card" : "Working on your request",
      body,
      liveStatus: true,
      primaryTerminal: false,
      showComposer: false,
      showSteps: false,
    };
  }
  if (guideState === "error") {
    return {
      title: "Check Guide status",
      body: "Guide reported an error before proposing a Change Card. Send a follow-up below or open Guide’s terminal to inspect it.",
      liveStatus: true,
      primaryTerminal: guideHasTerminal,
      showComposer: true,
      showSteps: false,
    };
  }
  if (draftState !== undefined) {
    if (draftState === "story_failed") {
      return {
        title: "Story check needs attention",
        body: "The last Story check reported an issue. Ask Guide below to fix the Story and propose the Change Card.",
        liveStatus: true,
        primaryTerminal: false,
        showComposer: true,
        showSteps: false,
      };
    }
    return {
      title: "Drafting your Change Card",
      body:
        draftState === "story_ready"
          ? "Story and design checked — waiting for Guide to propose the Change Card, or send a note below."
          : "Starter project ready — Guide drafts the service design, Story, and Change Card next. It will open here automatically.",
      liveStatus: true,
      primaryTerminal: false,
      showComposer: guideState === "waiting",
      showSteps: false,
    };
  }
  return {
    title: "Describe what you want to build",
    body: "Send your request to Guide below. Your Change Card will open here when it’s ready.",
    liveStatus: false,
    primaryTerminal: false,
    showComposer: true,
    showSteps: true,
  };
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
  const openLabel = "Open: " + item.summary;
  const inputsDescriptionId = React.useId();
  return (
    <li data-change-id={item.change_id}>
      <Button
        id={"highbay-open-change-" + item.change_id}
        className="highbay-change-open"
        aria-label={openLabel + " (" + item.change_id + ")"}
        aria-current={open ? "true" : undefined}
        disabled={busy}
        onClick={() => onOpen(item.change_id)}
      >
        {openLabel}
      </Button>{" "}
      <Badge variant={item.status === "confirmed" ? "success" : "warning"}>
        {item.status === "confirmed" ? "Confirmed" : "Needs confirmation"}
      </Badge>{" "}
      {item.proposed_revision ? (
        <>
          <Badge variant="warning" className="highbay-proposal-badge">
            New revision proposed
          </Badge>{" "}
        </>
      ) : null}
      {item.latest_verification ? (
        <>
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
          <p
            id={inputsDescriptionId}
            className="highbay-muted"
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
      <span className="highbay-muted">
        Saved <time dateTime={item.saved_at}>{new Date(item.saved_at).toLocaleString()}</time>
      </span>
      <p className="highbay-muted">
        {item.status === "confirmed" ? "Agreed revision" : "Draft revision"}{" "}
        <code>{item.request_revision.slice(0, 12)}</code>
        {item.proposed_revision ? (
          <>
            {" "}
            · Proposed revision <code>{item.proposed_revision.slice(0, 12)}</code>
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
