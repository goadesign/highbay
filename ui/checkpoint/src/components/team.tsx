// team.tsx presents the server's registered team facts, terminal attachment
// controls, and role lifecycle actions.

import React, { useCallback, useLayoutEffect, useRef } from "react";
import type { ProjectTeam, TeamRoleState, TeamSnapshot } from "../types";
import { Alert, Badge, Button, Card } from "./shadcn";

// Role is a registered role from the generated team contract.
type Role = TeamRoleState["role"];

// TeamFactsError says why the page cannot show current team facts.
export interface TeamFactsError {
  readFailed: boolean;
  message: string;
}

// TeamStageProps supplies the shared team read and page-owned role actions.
export interface TeamStageProps {
  snapshot: TeamSnapshot | null;
  loading: boolean;
  error: TeamFactsError | null;
  starting: boolean;
  canStartWorkers: boolean;
  agentPicker: React.ReactNode;
  agentsReady: boolean;
  disabled: boolean;
  interruptingRoles: readonly Role[];
  stoppingRoles: readonly Role[];
  restartingRoles: readonly Role[];
  openTerminalRole: Role | null;
  startReceipt: { at: string; result: ProjectTeam } | null;
  actionError: string | null;
  onRefresh: () => void;
  onStartWorkers: () => void;
  onOpenTerminal: (role: Role, invoker: HTMLElement | null) => void;
  onInterruptRole: (role: Role) => void;
  onStopRole: (role: Role) => void;
  onRestartRole: (role: Role) => void;
  onMessageRole: (role: Role) => void;
  onFocusedControlRemoved: () => void;
}

// GlobalTeamAttentionProps uses the retained snapshot to surface roles needing
// user input on every stage.
export interface GlobalTeamAttentionProps {
  snapshot: TeamSnapshot | null;
  lastRead: boolean;
  disabled: boolean;
  openTerminalRole: Role | null;
  onOpenTerminal: (role: Role, invoker: HTMLElement | null) => void;
  onMessageRole: (role: Role) => void;
  onFocusedControlRemoved: () => void;
}

const roleLabels: Record<Role, string> = {
  guide: "Coding agent (guide)",
  builder: "Builder",
  test_writer: "Test writer",
};

const shortRoleLabels: Record<Role, string> = {
  guide: "Guide",
  builder: "Builder",
  test_writer: "Test writer",
};

const stateLabels: Record<TeamRoleState["state"], string> = {
  not_started: "Not started",
  ended: "Ended",
  starting: "Starting",
  needs_you: "Needs you",
  working: "Working",
  waiting: "Waiting",
  error: "Error",
  unknown: "Status unknown",
};

const attentionLabels: Record<TeamRoleState["attention"], string> = {
  none: "",
  permission: "permission prompt",
  question: "waiting on your answer",
  auth: "sign-in required",
  silent: "no status reported (may be waiting at a startup or folder-trust prompt)",
  unknown: "reason unknown",
};

const assignmentLabels: Record<TeamRoleState["assignment"], string> = {
  unread: "Assignment unread",
  nothing_owed: "No unread assignment",
  no_work: "No work now",
  unknown: "Assignment status unknown",
};

// teamAttentionText formats a concise human-readable summary for a needs_you role.
export function teamAttentionText(role: TeamRoleState): string {
  const reason = attentionLabels[role.attention];
  return shortRoleLabels[role.role] + " needs you" + (reason !== "" ? ": " + reason : "");
}

// TeamStageView lists registered roles, their live herdr-core terminal state,
// and direct terminal/message controls.
export function TeamStageView({
  snapshot,
  loading,
  error,
  starting,
  canStartWorkers,
  agentPicker,
  agentsReady,
  disabled,
  interruptingRoles,
  stoppingRoles,
  restartingRoles,
  openTerminalRole,
  startReceipt,
  actionError,
  onRefresh,
  onStartWorkers,
  onOpenTerminal,
  onInterruptRole,
  onStopRole,
  onRestartRole,
  onMessageRole,
  onFocusedControlRemoved,
}: TeamStageProps) {
  const needsStart =
    error === null &&
    snapshot !== null &&
    snapshot.roles.some(
      (role) => role.role !== "guide" && (role.state === "not_started" || role.state === "ended")
    ) &&
    !snapshot.roles.some((role) => role.state === "needs_you");

  return (
    <Card id="highbay-team-stage" className="highbay-card" role="region" aria-labelledby="highbay-team-title">
      <div className="highbay-region-header">
        <h2 id="highbay-team-title" tabIndex={-1}>
          Project team
        </h2>
        <div className="highbay-field-row">
          {canStartWorkers || starting ? (
            <Button
              id="highbay-team-start"
              variant={needsStart ? "primary" : "default"}
              disabled={disabled || starting || !agentsReady}
              aria-busy={starting}
              onClick={onStartWorkers}
            >
              {starting ? "Starting workers…" : "Start workers"}
            </Button>
          ) : null}
          <Button
            id="highbay-team-refresh"
            variant={error !== null ? "primary" : "default"}
            disabled={disabled || loading}
            onClick={onRefresh}
          >
            {error !== null ? "Retry team read" : "Refresh team"}
          </Button>
        </div>
      </div>

      {error !== null ? (
        <Alert id="highbay-team-read-error" variant="destructive" role="alert">
          {error.readFailed ? <strong>Could not read the project team. </strong> : null}
          {error.message}
        </Alert>
      ) : null}

      {loading ? (
        <p id="highbay-team-loading">
          {snapshot === null ? "Reading the project team…" : "Updating team…"}
        </p>
      ) : null}

      {agentPicker}
      {canStartWorkers ? (
        <p>Workers already running keep their current sessions. These choices apply when a worker starts.</p>
      ) : null}

      {snapshot === null ? (
        !loading && error === null ? <p>The project team has not been read.</p> : null
      ) : (
        <div id="highbay-team-facts" aria-busy={loading} data-stale={error !== null}>
          {error !== null ? <p id="highbay-team-stale">Last-read team facts; they could not be refreshed.</p> : null}
          <p id="highbay-team-name">
            Team: <strong>{snapshot.team}</strong> · Channel: <code>{snapshot.channel}</code>
          </p>
          {snapshot.roles.length === 0 ? (
            <p id="highbay-team-empty">
              No agents are running yet. Send a message in Team conversation or Describe to start Guide.
            </p>
          ) : (
            <ul id="highbay-team-roles" className="highbay-trace-list">
              {snapshot.roles.map((role) => {
                const externalGuide =
                  role.role === "guide" &&
                  !role.has_terminal &&
                  role.state !== "not_started" &&
                  role.state !== "ended";
                const interrupting = interruptingRoles.includes(role.role);
                const stopping = stoppingRoles.includes(role.role);
                const restarting = restartingRoles.includes(role.role);
                const canRestart =
                  !externalGuide &&
                  (role.has_terminal || role.state === "ended" || role.state === "error");
                return (
                  <li key={role.role} id={"highbay-team-role-" + role.role} data-state={role.state}>
                    <div className="highbay-trace-heading">
                      <strong>{roleLabels[role.role]}</strong>
                      <Badge
                        variant={
                          role.state === "needs_you"
                            ? "warning"
                            : role.state === "error"
                            ? "destructive"
                            : role.state === "working"
                            ? "success"
                            : "default"
                        }
                      >
                        {externalGuide
                          ? "External session · Chorus connected"
                          : stateLabels[role.state]}
                        {role.state === "needs_you" && attentionLabels[role.attention] !== ""
                          ? ": " + attentionLabels[role.attention]
                          : ""}
                      </Badge>
                    </div>
                    <p>{role.role === "guide" ? "Guides intent, Story, and revisions" : assignmentLabels[role.assignment]}</p>
                    <div className="highbay-field-row">
                      {role.has_terminal ? (
                        <TerminalActionButton
                          role={role}
                          disabled={disabled}
                          active={openTerminalRole === role.role}
                          global={false}
                          onOpenTerminal={onOpenTerminal}
                          onFocusedControlRemoved={onFocusedControlRemoved}
                        />
                      ) : null}
                      <Button
                        id={"highbay-team-message-" + role.role}
                        disabled={disabled}
                        onClick={() => onMessageRole(role.role)}
                      >
                        Message {shortRoleLabels[role.role]}
                      </Button>
                      {role.has_terminal && role.state !== "not_started" && role.state !== "ended" ? (
                        <>
                          <Button
                            id={"highbay-team-interrupt-" + role.role}
                            disabled={disabled || interrupting || stopping || restarting}
                            onClick={() => onInterruptRole(role.role)}
                          >
                            {interrupting ? "Interrupting…" : "Interrupt"}
                          </Button>
                          <Button
                            id={"highbay-team-stop-" + role.role}
                            disabled={disabled || stopping || restarting}
                            onClick={() => onStopRole(role.role)}
                          >
                            {stopping ? "Stopping…" : "Stop"}
                          </Button>
                        </>
                      ) : null}
                      {canRestart ? (
                        <Button
                          id={"highbay-team-restart-" + role.role}
                          disabled={disabled || restarting || stopping}
                          onClick={() => onRestartRole(role.role)}
                        >
                          {restarting ? "Restarting…" : "Restart"}
                        </Button>
                      ) : null}
                    </div>
                    <details className="highbay-details">
                      <summary>{roleLabels[role.role]} details</summary>
                      <p>
                        Registered handle: <code>{role.handle}</code> · Terminal attached:{" "}
                        <code>{String(role.has_terminal)}</code>
                      </p>
                      {role.choice !== undefined ? (
                        <p>
                          Client: <code>{role.choice.client}</code> ({role.choice.program})
                        </p>
                      ) : null}
                    </details>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}

      {startReceipt !== null ? (
        <p id="highbay-team-start-receipt">
          This start (begun <time dateTime={startReceipt.at}>{startReceipt.at}</time>) returned: «
          {startReceipt.result.step}»
        </p>
      ) : null}

      {actionError !== null ? (
        <Alert id="highbay-team-action-error" variant="destructive" tabIndex={-1}>
          {actionError}
        </Alert>
      ) : null}
    </Card>
  );
}

// GlobalTeamAttention renders one line per needs_you role with direct terminal
// or conversation controls.
export function GlobalTeamAttention({
  snapshot,
  lastRead,
  disabled,
  openTerminalRole,
  onOpenTerminal,
  onMessageRole,
  onFocusedControlRemoved,
}: GlobalTeamAttentionProps) {
  const roles = snapshot?.roles.filter((role) => role.state === "needs_you") ?? [];
  const chorusUnavailable = snapshot?.chorus_status === "unavailable";
  return (
    <div id="highbay-team-attention" role="status" aria-live="polite">
      {chorusUnavailable ? (
        <p id="highbay-team-chorus-status">
          {lastRead ? "Last read: " : ""}Chorus cannot be reached for this team. Running workers can
          still wait for assignments through Highbay.
        </p>
      ) : null}
      {roles.map((role) => (
        <div key={role.role} className="highbay-field-row" id={"highbay-team-attention-" + role.role}>
          <span className="highbay-live-wrap">
            {lastRead ? "Last read: " : ""}
            {teamAttentionText(role)}
          </span>
          {role.has_terminal ? (
            <TerminalActionButton
              role={role}
              disabled={disabled}
              active={openTerminalRole === role.role}
              global={true}
              onOpenTerminal={onOpenTerminal}
              onFocusedControlRemoved={onFocusedControlRemoved}
            />
          ) : (
            <Button
              id={"highbay-team-attention-message-" + role.role}
              variant="primary"
              disabled={disabled}
              onClick={() => onMessageRole(role.role)}
            >
              Message {shortRoleLabels[role.role]}
            </Button>
          )}
        </div>
      ))}
    </div>
  );
}

// TerminalActionButton keeps focus-restoration semantics consistent when a
// dynamic role attention button unmounts after the role resumes working.
function TerminalActionButton({
  role,
  disabled,
  active,
  global,
  onOpenTerminal,
  onFocusedControlRemoved,
}: {
  role: TeamRoleState;
  disabled: boolean;
  active: boolean;
  global: boolean;
  onOpenTerminal: (role: Role, invoker: HTMLElement | null) => void;
  onFocusedControlRemoved: () => void;
}) {
  const notifyRemoval = useRef(onFocusedControlRemoved);
  useLayoutEffect(() => {
    notifyRemoval.current = onFocusedControlRemoved;
  }, [onFocusedControlRemoved]);

  const attachControl = useCallback((node: HTMLSpanElement | null) => {
    if (node === null) {
      return;
    }
    return () => {
      if (node.contains(document.activeElement)) {
        notifyRemoval.current();
      }
    };
  }, []);

  const label = shortRoleLabels[role.role];
  return (
    <span ref={attachControl}>
      <Button
        id={(global ? "highbay-team-attention-terminal-" : "highbay-team-terminal-") + role.role}
        variant={role.state === "needs_you" ? "primary" : "default"}
        aria-pressed={active}
        disabled={disabled}
        onClick={(event) => onOpenTerminal(role.role, event.currentTarget)}
      >
        {active ? label + " terminal open" : "Open " + label + " terminal"}
      </Button>
    </span>
  );
}
