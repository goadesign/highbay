// Package components explains how to open the views page with a session and
// what to do when the session ends.

import React from "react";
import { Alert, Card } from "./shadcn";

// SignedOutReason says why the page has no session.
export type SignedOutReason = "no-link" | "link-used" | "link-failed";

// SignedOutView names the agent and companion routes that open a fresh views
// session. It never offers a way to type or paste a credential.
export function SignedOutView({ reason, message }: { reason: SignedOutReason; message: string }) {
  return (
    <Card id="surla-signed-out" className="surla-card" aria-labelledby="surla-signed-out-title">
      <h2 id="surla-signed-out-title">
        {reason === "link-used"
          ? "This link was already used"
          : reason === "link-failed"
          ? "Surla could not open this page"
          : "Open this project’s views"}
      </h2>
      {reason === "link-used" ? (
        <Alert id="surla-link-used" variant="warning" role="alert">
          This link was already used or has expired, so it cannot open a session. Each link
          opens one page once. If you did not use it yourself, someone else may have.
        </Alert>
      ) : null}
      {reason === "link-failed" ? (
        <Alert id="surla-link-failed" variant="destructive" role="alert">
          {message}
        </Alert>
      ) : null}
      <p>
        Ask your coding agent to open Surla views, or choose Open Surla Window in the Herdr companion.
      </p>
      <p className="surla-muted">
        Reloading or reopening this page, or opening it in another tab, needs a fresh session
        through one of those routes.
      </p>
    </Card>
  );
}

// SessionEndedNotice keeps the user's unsaved edits in view and copyable after
// the server stopped accepting the page's session, for example after a
// restart.
export function SessionEndedNotice({ unsavedLines }: { unsavedLines: string[] }) {
  return (
    <Alert id="surla-session-ended" variant="warning" role="alert">
      <strong>This page's Surla session has ended. </strong>
      The Surla server no longer accepts it, for example because the server restarted.
      {" "}Ask your coding agent to open Surla views, or choose Open Surla Window in the Herdr companion.
      {unsavedLines.length > 0 ? (
        <>
          <p>
            Your unsaved edits are still in the form below. Copy them before you leave this
            page:
          </p>
          <pre id="surla-session-ended-edits" tabIndex={0}>
            {unsavedLines.join("\n")}
          </pre>
        </>
      ) : null}
    </Alert>
  );
}
