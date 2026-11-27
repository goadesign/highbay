// update-banner.tsx renders Highbay's non-intrusive release update and UI-reload
// banner at the top of the workspace, modeled on Chorus's update banner while
// preserving unsaved Change Card and composer drafts until the user clicks Reload.

import React from "react";
import type { UpdateStatusResult } from "../types";
import { Alert, Badge, Button } from "./shadcn";

// UpdateProgress tracks an explicit user-initiated self-update across install,
// in-place server restart, readiness, conflict, and recoverable errors.
export type UpdateProgress =
  | { kind: "installing"; targetVersion: string }
  | { kind: "restarting"; targetVersion: string }
  | { kind: "ready"; version: string }
  | { kind: "restart_failed"; targetVersion: string; message: string }
  | { kind: "conflict"; message: string }
  | { kind: "error"; targetVersion: string; message: string };

// UpdateBannerProps supplies observed release metadata, UI build drift, and
// draft-safety state from app.tsx.
export interface UpdateBannerProps {
  available: UpdateStatusResult | null;
  progress: UpdateProgress | null;
  uiBuildChanged: boolean;
  hasUnsavedInput: boolean;
  disabled: boolean;
  onInstall: () => void;
  onCheckRestart: (targetVersion: string) => void;
  onReload: () => void;
}

// safeHttpsUrl returns url only when it parses as an https: URL.
function safeHttpsUrl(url: string | undefined): string | null {
  if (url === undefined || url === "") {
    return null;
  }
  try {
    const parsed = new URL(url);
    return parsed.protocol === "https:" ? parsed.toString() : null;
  } catch {
    return null;
  }
}

// formatVersion ensures release version labels start with a single leading "v".
function formatVersion(version: string): string {
  return version.startsWith("v") ? version : "v" + version;
}

// UpdateBanner announces available Highbay releases, guides in-place server
// updates, and offers a draft-safe Reload control once the updated server or UI
// bundle is live.
export function UpdateBanner({
  available,
  progress,
  uiBuildChanged,
  hasUnsavedInput,
  disabled,
  onInstall,
  onCheckRestart,
  onReload,
}: UpdateBannerProps) {
  const offerAvailable = available !== null && available.update_available ? available : null;
  if (progress === null && !uiBuildChanged && offerAvailable === null) {
    return null;
  }

  const busy = progress?.kind === "installing" || progress?.kind === "restarting";
  const ready = progress?.kind === "ready";
  const showReload =
    ready || progress?.kind === "conflict" || (progress === null && uiBuildChanged);
  const canAutoUpdate = offerAvailable?.can_auto_update ?? false;
  const releaseHref = safeHttpsUrl(offerAvailable?.release_url);

  const variant =
    progress?.kind === "error" || progress?.kind === "restart_failed" || progress?.kind === "conflict"
      ? "warning"
      : "default";

  let message: React.ReactNode;
  if (progress?.kind === "installing") {
    message = "Updating Highbay and its companions…";
  } else if (progress?.kind === "restarting") {
    message = "Waiting for Highbay " + formatVersion(progress.targetVersion) + " to restart…";
  } else if (progress?.kind === "ready") {
    message = "Highbay " + formatVersion(progress.version) + " is running. Reload when you are ready.";
  } else if (progress !== null) {
    message = progress.message;
  } else if (uiBuildChanged) {
    message = "Workspace UI updated on the running server. Reload when you are ready.";
  } else if (offerAvailable !== null) {
    const targetLabel = formatVersion(offerAvailable.latest_version);
    message = canAutoUpdate ? (
      <>
        Highbay <strong>{targetLabel}</strong> is available (running{" "}
        <code>{offerAvailable.current_version}</code>).
      </>
    ) : (
      <>
        Highbay <strong>{targetLabel}</strong> is available
        {offerAvailable.manual_command !== undefined ? (
          <>
            {" — update with "}
            <code>{offerAvailable.manual_command}</code>
          </>
        ) : null}
        .
      </>
    );
  } else {
    message = null;
  }

  return (
    <Alert
      id="highbay-update-banner"
      className="highbay-update-banner"
      variant={variant}
      role="status"
      aria-live="polite"
      aria-busy={busy}
    >
      <div className="highbay-update-banner-row">
        <div className="highbay-update-banner-copy">
          <Badge variant={ready ? "success" : "outline"}>
            {ready ? "Updated" : busy ? "Updating" : "Update"}
          </Badge>
          <span id="highbay-update-message">{message}</span>
          {releaseHref !== null && !showReload ? (
            <a
              id="highbay-update-release"
              href={releaseHref}
              target="_blank"
              rel="noopener noreferrer"
              className="highbay-update-link"
            >
              Release notes
            </a>
          ) : null}
        </div>
        <div className="highbay-toolbar-actions">
          {showReload ? (
            <Button id="highbay-update-reload" variant="primary" onClick={onReload}>
              Reload
            </Button>
          ) : progress?.kind === "restart_failed" ? (
            <Button
              id="highbay-update-install"
              variant="primary"
              disabled={disabled || busy}
              onClick={() => onCheckRestart(progress.targetVersion)}
            >
              Check again
            </Button>
          ) : progress?.kind === "error" || canAutoUpdate ? (
            <Button
              id="highbay-update-install"
              variant="primary"
              disabled={disabled || busy}
              onClick={onInstall}
            >
              {progress?.kind === "error"
                ? "Try again"
                : busy
                ? "Updating…"
                : "Update & Restart"}
            </Button>
          ) : null}
        </div>
      </div>
      {showReload && hasUnsavedInput ? (
        <p id="highbay-update-unsaved-hint" className="highbay-muted highbay-update-hint">
          Save or copy your unsaved edits on this page before reloading.
        </p>
      ) : null}
    </Alert>
  );
}
