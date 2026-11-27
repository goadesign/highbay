// Package components explains how to open the views page with a session and
// what to do when the session ends.

import React from "react";
import { Alert, Card } from "./shadcn";

// SessionRecovery explains the two ways to open or restore a project session:
// running `highbay` in the project folder or invoking `/highbay` in a coding agent.
function SessionRecovery() {
  return (
    <>
      <p>
        In your project folder, run <code>highbay</code> to open this workspace in your browser, or run{" "}
        <code>/highbay</code> (<code>$highbay</code> in Codex) inside your coding agent.
      </p>
      <details className="highbay-details">
        <summary>Print a one-use link without opening a browser automatically</summary>
        <p>
          In the project folder, run <code>highbay --print-link</code> and open the printed link within two minutes.
        </p>
      </details>
    </>
  );
}

// SignedOutReason says why the page has no session.
export type SignedOutReason = "no-link" | "link-used" | "link-failed";

// SignedOutView names the routes that open a fresh views session. It never
// offers a way to type or paste a credential.
export function SignedOutView({ reason, message }: { reason: SignedOutReason; message: string }) {
  return (
    <Card id="highbay-signed-out" className="highbay-card" aria-labelledby="highbay-signed-out-title">
      <h2 id="highbay-signed-out-title">
        {reason === "link-used"
          ? "This link was already used"
          : reason === "link-failed"
          ? "Highbay could not open this page"
          : "Open this project’s views"}
      </h2>
      {reason === "link-used" ? (
        <Alert id="highbay-link-used" variant="warning" role="alert">
          This link was already used or has expired, so it cannot open a session. Each link opens a
          browser session once. If you already opened this project in this browser, open{" "}
          <code>/views</code> without the <code>#link=</code> fragment.
        </Alert>
      ) : null}
      {reason === "link-failed" ? (
        <Alert id="highbay-link-failed" variant="destructive" role="alert">
          {message}
        </Alert>
      ) : null}
      <SessionRecovery />
      <p className="highbay-muted">
        Once signed in, reloading this page or opening another tab in the same browser keeps your
        session active while the project server is running.
      </p>
    </Card>
  );
}

// SessionEndedNotice keeps the user's unsaved edits in view and copyable after
// the server stopped accepting the page's session, for example after a
// restart.
export function SessionEndedNotice({ unsavedLines }: { unsavedLines: string[] }) {
  return (
    <Alert id="highbay-session-ended" variant="warning" role="alert">
      <strong>This page’s Highbay session has ended. </strong>
      The Highbay server no longer accepts it, for example because the project server restarted.
      <SessionRecovery />
      {unsavedLines.length > 0 ? (
        <>
          <p>
            Your unsaved edits are still in the form below. Copy them before you open a new session:
          </p>
          <pre id="highbay-session-ended-edits" tabIndex={0}>
            {unsavedLines.join("\n")}
          </pre>
        </>
      ) : null}
    </Alert>
  );
}
