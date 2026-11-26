// Package components lists the project's saved Change Cards so the user can
// find work and open one change.

import React from "react";
import { ChangeList, ChangeListItem, LatestVerificationItem, TeamRoleState } from "../types";
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
  // guideConnected, guideInHerdr and guideState describe the guide the project reports.
  guideConnected: boolean;
  guideInHerdr: boolean;
  guideState: TeamRoleState["state"] | null;
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
  guideState,
  disabled,
  showingGuide,
  onShowGuidePane,
  onOpen,
  onRetry,
}: ChangeListViewProps) {
  if (list !== null && list.changes.length === 0 && error === null && openError === null) {
    const empty = emptyChangeState(list, guideConnected, guideInHerdr, guideState);
    return (
      <Card id="highbay-change-list" className="highbay-card highbay-welcome-card"
        aria-labelledby="highbay-change-list-title" aria-busy={loading ? "true" : undefined}>
        <h2 id="highbay-change-list-title">{empty.title}</h2>
        <p id="highbay-change-list-empty" role={empty.liveStatus ? "status" : undefined}>
          {empty.body}
        </p>
        {guideConnected && guideInHerdr ? (
          <div className="highbay-field-row">
            <Button id="highbay-empty-show-guide" variant={empty.primaryShowPane ? "primary" : "default"}
              disabled={disabled || showingGuide} aria-busy={showingGuide} onClick={onShowGuidePane}>
              {showingGuide ? "Showing coding agent pane…" : "Show coding agent pane"}
            </Button>
          </div>
        ) : null}
        {empty.showSteps ? <ol className="highbay-welcome-steps">
          <li><strong>Confirm intent</strong> Answer open questions and lock concrete examples before code is written.</li>
          <li><strong>Build &amp; verify</strong> Highbay coordinates implementation and independent checks.</li>
          <li><strong>Approve &amp; merge</strong> Review the verified evidence and merge the change into your branch.</li>
        </ol> : null}
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
          Run results describe their recorded candidate. The server compares each latest run
          with the agreed revision and selected tests. Open a change, then Check, to inspect it.
        </p>
      ) : null}
      {error !== null ? (
        <Alert id="highbay-change-list-error" variant="destructive" role="alert">
          <strong>Could not read this project's changes. </strong>
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
        error === null ? <p role="status">Reading this project's changes...</p> : null
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
// button emphasis from Highbay's initialization/draft facts and observed guide state.
function emptyChangeState(
  list: ChangeList,
  guideConnected: boolean,
  guideInHerdr: boolean,
  guideState: TeamRoleState["state"] | null,
): {
  title: string;
  body: React.ReactNode;
  liveStatus: boolean;
  primaryShowPane: boolean;
  showSteps: boolean;
} {
  if (list.initializing_phase !== undefined) {
    return {
      title: "Preparing your project",
      body: list.initializing_phase,
      liveStatus: true,
      primaryShowPane: false,
      showSteps: false,
    };
  }
  if (!guideConnected) {
    return {
      title: "Start in your coding agent with /highbay",
      body: (
        <>
          Open your coding agent in this folder and invoke <code>/highbay</code> (<code>$highbay</code> in Codex)
          to connect it, then describe what you want to build. Your Change Card will appear here automatically.
        </>
      ),
      liveStatus: false,
      primaryShowPane: false,
      showSteps: true,
    };
  }
  const draftState = list.draft_state;
  if (draftState === "evaluating_story") {
    return {
      title: "Drafting your Change Card",
      body: "Checking the Story and Goa design. Your Change Card will open here automatically when it’s ready.",
      liveStatus: true,
      primaryShowPane: false,
      showSteps: false,
    };
  }
  if (guideInHerdr && guideState === "needs_you") {
    return {
      title: "Your coding agent needs you",
      body: "Show your coding agent pane to answer its prompt so it can continue.",
      liveStatus: true,
      primaryShowPane: true,
      showSteps: false,
    };
  }
  if (guideInHerdr && guideState === "starting") {
    return {
      title: "Starting your coding agent",
      body: "Waiting for your coding agent pane to start. Your Change Card will open here when ready.",
      liveStatus: true,
      primaryShowPane: false,
      showSteps: false,
    };
  }
  if (guideState === "working") {
    const body = draftState === "story_ready"
      ? "Story and design checked — your coding agent is preparing the Change Card. It will open here automatically."
      : draftState === "story_failed"
      ? "The last Story check reported an issue — your coding agent is updating the draft. Your Change Card will open here automatically."
      : draftState === "editing" || draftState === "initialized"
      ? "Starter project ready — your coding agent is writing the service design and Story. Your Change Card will open here automatically."
      : "Your coding agent is working on your request. Progress and your Change Card will appear here automatically.";
    return {
      title: draftState !== undefined ? "Drafting your Change Card" : "Working on your request",
      body,
      liveStatus: true,
      primaryShowPane: false,
      showSteps: false,
    };
  }
  if (guideInHerdr && guideState === "error") {
    return {
      title: "Check your coding agent",
      body: "Your coding agent reported an error before proposing a Change Card. Show its pane to check its status.",
      liveStatus: true,
      primaryShowPane: true,
      showSteps: false,
    };
  }
  if (draftState !== undefined) {
    if (draftState === "story_failed") {
      return {
        title: "Story check needs attention",
        body: guideInHerdr
          ? "The last Story check reported an issue, and your coding agent is waiting. Show its pane so it can fix the draft and propose the Change Card."
          : "The last Story check reported an issue. Return to your coding agent so it can fix the draft and propose the Change Card.",
        liveStatus: true,
        primaryShowPane: true,
        showSteps: false,
      };
    }
    if (guideInHerdr && guideState === "waiting") {
      return {
        title: "Your coding agent is waiting",
        body: draftState === "story_ready"
          ? "Story and design checked, and no Change Card has been proposed yet. Show your coding agent pane to check its message or ask it to propose the Change Card."
          : "Starter project ready, and no Change Card has been proposed yet. Show your coding agent pane to answer any question or ask it to propose the Change Card.",
        liveStatus: true,
        primaryShowPane: true,
        showSteps: false,
      };
    }
    return {
      title: "Drafting your Change Card",
      body: draftState === "story_ready"
        ? "Story and design checked — waiting for your coding agent to propose the Change Card. It will open here automatically."
        : "Starter project ready — your coding agent drafts the service design, Story, and Change Card next. Keep an eye on your coding agent in case it asks a question; the card will open here automatically.",
      liveStatus: true,
      primaryShowPane: false,
      showSteps: false,
    };
  }
  return {
    title: "Describe what you want to build in your coding agent",
    body: "Your coding agent drafts the Change Card. It opens here when it’s ready.",
    liveStatus: false,
    primaryShowPane: true,
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
  // IDs are application data and may contain whitespace; React owns this
  // accessibility reference so it remains one valid, unique ID token.
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
            variant={item.latest_verification.inputs_current && item.latest_verification.state === "passed"
              ? "success" : "default"}
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
        {item.status === "confirmed" ? "Agreed revision" : "Draft revision"} <code>{item.request_revision.slice(0, 12)}</code>
        {item.proposed_revision ? <> · Proposed revision <code>{item.proposed_revision.slice(0, 12)}</code></> : null}
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
