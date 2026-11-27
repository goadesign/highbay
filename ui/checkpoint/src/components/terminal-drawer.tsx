// terminal-drawer.tsx attaches @xterm/xterm over Goa's typed WebSocket stream
// (GET /team/roles/{role}/terminal) to inspect or unblock a live herdr-core
// (hc) role terminal directly in the browser, with 256 KiB byte-credit flow
// control and Ctrl+Shift+Escape keyboard focus escape.

import React, { useEffect, useRef, useState } from "react";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import {
  TerminalClientFrame,
  TeamRoleState,
  WEBSOCKET_ROUTES,
} from "../types";
import { Alert, Badge, Button, Card } from "./shadcn";

// TerminalTicketOutcome returns a single-use WebSocket ticket or refusal message.
export type TerminalTicketOutcome =
  | { ok: true; ticket: string }
  | { ok: false; message: string };

// TerminalDrawerProps supplies the active role, its observed state, and server
// lifecycle callbacks.
export interface TerminalDrawerProps {
  role: TeamRoleState["role"] | null;
  roleState: TeamRoleState | null;
  disabled: boolean;
  interrupting: boolean;
  stopping: boolean;
  restarting: boolean;
  restartSeq: number;
  actionError: string | null;
  onCreateTicket: (role: TeamRoleState["role"], signal: AbortSignal) => Promise<TerminalTicketOutcome>;
  onInterrupt: (role: TeamRoleState["role"]) => void;
  onStop: (role: TeamRoleState["role"]) => void;
  onRestart: (role: TeamRoleState["role"]) => void;
  onClose: () => void;
}

type ConnectionStatus = "connecting" | "connected" | "closed" | "error";

interface ClosedDetails {
  reason: string;
  exitCode?: number;
  signal?: number;
}

const roleTitles: Record<TeamRoleState["role"], string> = {
  guide: "Guide terminal",
  builder: "Builder terminal",
  test_writer: "Test writer terminal",
};

// clampCols restricts terminal width to the Goa contract bounds [20, 500].
function clampCols(cols: number): number {
  return Math.max(20, Math.min(500, Math.trunc(cols)));
}

// clampRows restricts terminal height to the Goa contract bounds [5, 250].
function clampRows(rows: number): number {
  return Math.max(5, Math.min(250, Math.trunc(rows)));
}

// bytesToBase64 encodes raw PTY input bytes for TerminalClientFrame JSON transport.
export function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunk = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunk) {
    const slice = bytes.subarray(offset, offset + chunk);
    for (let i = 0; i < slice.length; i++) {
      binary += String.fromCharCode(slice[i]);
    }
  }
  return btoa(binary);
}

// base64ToBytes decodes base64 PTY output frames from TerminalServerFrame.
export function base64ToBytes(encoded: string): Uint8Array {
  const binary = atob(encoded);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

// binaryStringToBytes converts xterm's onBinary string (code points 0..255) to bytes.
function binaryStringToBytes(raw: string): Uint8Array {
  const bytes = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) {
    bytes[i] = raw.charCodeAt(i) & 0xff;
  }
  return bytes;
}

// TerminalDrawer mounts a non-modal docked terminal panel with live PTY I/O,
// resize synchronization, byte-credit flow control, and keyboard focus escape.
export function TerminalDrawer({
  role,
  roleState,
  disabled,
  interrupting,
  stopping,
  restarting,
  restartSeq,
  actionError,
  onCreateTicket,
  onInterrupt,
  onStop,
  onRestart,
  onClose,
}: TerminalDrawerProps) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const closeBtnRef = useRef<HTMLButtonElement | null>(null);
  const [status, setStatus] = useState<ConnectionStatus>("connecting");
  const [connectError, setConnectError] = useState<string | null>(null);
  const [closedDetails, setClosedDetails] = useState<ClosedDetails | null>(null);
  const [dimensions, setDimensions] = useState<{ cols: number; rows: number }>({ cols: 100, rows: 28 });
  const [reconnectNonce, setReconnectNonce] = useState(0);

  const hasTerminal = roleState?.has_terminal ?? false;

  useEffect(() => {
    if (role === null) {
      return;
    }
    if (!hasTerminal) {
      closeBtnRef.current?.focus();
      return;
    }

    const host = hostRef.current;
    if (host === null) {
      return;
    }

    setStatus("connecting");
    setConnectError(null);
    setClosedDetails(null);

    const abortController = new AbortController();
    let disposed = false;
    let applyingServerResize = false;
    let socket: WebSocket | null = null;
    let term: Terminal | null = null;
    let fitAddon: FitAddon | null = null;
    let resizeObserver: ResizeObserver | null = null;
    const encoder = new TextEncoder();

    const sendFrame = (frame: TerminalClientFrame) => {
      if (socket !== null && socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify(frame));
      }
    };

    try {
      const dark =
        document.documentElement.dataset.theme === "dark" ||
        (document.documentElement.dataset.theme !== "light" &&
          typeof window.matchMedia === "function" &&
          window.matchMedia("(prefers-color-scheme: dark)").matches);

      term = new Terminal({
        cursorBlink: true,
        fontSize: 13,
        fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
        theme: dark
          ? { background: "#09090b", foreground: "#fafafa", cursor: "#3b82f6" }
          : { background: "#0f172a", foreground: "#f8fafc", cursor: "#38bdf8" },
      });
      fitAddon = new FitAddon();
      term.loadAddon(fitAddon);
      term.open(host);
      try {
        fitAddon.fit();
      } catch {
        // Layout-free DOM test environments keep default dimensions.
      }
      const initialCols = clampCols(term.cols || 100);
      const initialRows = clampRows(term.rows || 28);
      setDimensions({ cols: initialCols, rows: initialRows });

      // WCAG 2.1.2 No Keyboard Trap: Ctrl+Shift+Escape leaves terminal capture
      // and moves focus directly to the Close terminal button.
      term.attachCustomKeyEventHandler((event) => {
        if (event.type === "keydown" && event.ctrlKey && event.shiftKey && event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          closeBtnRef.current?.focus();
          return false;
        }
        return true;
      });

      term.onData((data) => {
        const bytes = encoder.encode(data);
        if (bytes.byteLength > 0) {
          sendFrame({ client_frame: { kind: "input", data: bytesToBase64(bytes) } });
        }
      });

      term.onBinary((raw) => {
        const bytes = binaryStringToBytes(raw);
        if (bytes.byteLength > 0) {
          sendFrame({ client_frame: { kind: "input", data: bytesToBase64(bytes) } });
        }
      });

      term.onResize(({ cols, rows }) => {
        const nextCols = clampCols(cols);
        const nextRows = clampRows(rows);
        setDimensions({ cols: nextCols, rows: nextRows });
        if (!applyingServerResize) {
          sendFrame({ client_frame: { kind: "resize", cols: nextCols, rows: nextRows } });
        }
      });

      if (typeof ResizeObserver !== "undefined") {
        resizeObserver = new ResizeObserver(() => {
          if (!disposed && fitAddon !== null) {
            try {
              fitAddon.fit();
            } catch {
              // Ignore transient zero-size layout passes.
            }
          }
        });
        resizeObserver.observe(host);
      }

      term.focus();
    } catch {
      // If a headless test DOM cannot initialize xterm's renderer, continue
      // establishing the ticket/WebSocket contract and focus Close.
      closeBtnRef.current?.focus();
    }

    const handleFocusIn = () => sendFrame({ client_frame: { kind: "focus", focused: true } });
    const handleFocusOut = () => sendFrame({ client_frame: { kind: "focus", focused: false } });
    host.addEventListener("focusin", handleFocusIn);
    host.addEventListener("focusout", handleFocusOut);

    void (async () => {
      const ticketOutcome = await onCreateTicket(role, abortController.signal);
      if (disposed || abortController.signal.aborted) {
        return;
      }
      if (!ticketOutcome.ok) {
        setStatus("error");
        setConnectError(ticketOutcome.message);
        return;
      }
      if (typeof WebSocket === "undefined") {
        setStatus("error");
        setConnectError("WebSocket is not supported in this browser environment.");
        return;
      }

      const cols = clampCols(term?.cols ?? 100);
      const rows = clampRows(term?.rows ?? 28);
      const encoded = WEBSOCKET_ROUTES.attach_role_terminal.encode({
        role,
        ticket: ticketOutcome.ticket,
        cols,
        rows,
        observe: false,
      });
      const wsProtocol = window.location.protocol === "https:" ? "wss://" : "ws://";
      const ws = new WebSocket(wsProtocol + window.location.host + encoded.path);
      socket = ws;

      ws.addEventListener("open", () => {
        if (disposed) {
          return;
        }
        setStatus("connected");
        const focused = host.contains(document.activeElement);
        if (focused) {
          sendFrame({ client_frame: { kind: "focus", focused: true } });
        }
      });

      ws.addEventListener("message", (event) => {
        if (disposed || typeof event.data !== "string") {
          return;
        }
        let parsed: unknown;
        try {
          parsed = JSON.parse(event.data);
        } catch {
          return;
        }
        if (!WEBSOCKET_ROUTES.attach_role_terminal.decodeFrame(parsed)) {
          return;
        }
        const frame = parsed.server_frame;
        if (frame.kind === "output") {
          const bytes = base64ToBytes(frame.data);
          if (term !== null) {
            term.write(bytes, () => {
              if (!disposed && bytes.byteLength > 0) {
                sendFrame({ client_frame: { kind: "credit", bytes: bytes.byteLength } });
              }
            });
          } else if (bytes.byteLength > 0) {
            sendFrame({ client_frame: { kind: "credit", bytes: bytes.byteLength } });
          }
        } else if (frame.kind === "resize") {
          const nextCols = clampCols(frame.cols);
          const nextRows = clampRows(frame.rows);
          setDimensions({ cols: nextCols, rows: nextRows });
          if (term !== null && (term.cols !== nextCols || term.rows !== nextRows)) {
            applyingServerResize = true;
            try {
              term.resize(nextCols, nextRows);
            } finally {
              applyingServerResize = false;
            }
          }
        } else if (frame.kind === "closed") {
          setStatus("closed");
          setClosedDetails({
            reason: frame.reason,
            exitCode: frame.exit_code,
            signal: frame.signal,
          });
        }
      });

      ws.addEventListener("error", () => {
        if (disposed) {
          return;
        }
        setStatus((prev) => (prev === "closed" ? prev : "error"));
        setConnectError((prev) => prev ?? "Terminal stream disconnected.");
      });

      ws.addEventListener("close", () => {
        if (disposed) {
          return;
        }
        setStatus((prev) => (prev === "error" ? prev : "closed"));
      });
    })();

    return () => {
      disposed = true;
      abortController.abort();
      host.removeEventListener("focusin", handleFocusIn);
      host.removeEventListener("focusout", handleFocusOut);
      resizeObserver?.disconnect();
      if (socket !== null && (socket.readyState === WebSocket.OPEN || socket.readyState === WebSocket.CONNECTING)) {
        socket.close();
      }
      term?.dispose();
    };
  }, [role, hasTerminal, reconnectNonce, restartSeq, onCreateTicket]);

  if (role === null) {
    return null;
  }

  return (
    <Card
      id="highbay-terminal-drawer"
      className="highbay-card highbay-terminal-drawer"
      role="region"
      aria-labelledby="highbay-terminal-title"
      aria-describedby="highbay-terminal-escape-hint"
      data-role={role}
      data-status={hasTerminal ? status : "unavailable"}
      onKeyDown={(event) => {
        // Pressing Escape outside the xterm input surface dismisses the drawer;
        // inside xterm, Escape goes to the terminal program and Ctrl+Shift+Esc
        // moves focus to Close terminal.
        if (
          event.key === "Escape" &&
          !event.defaultPrevented &&
          !(event.target instanceof Node && hostRef.current !== null && hostRef.current.contains(event.target))
        ) {
          event.preventDefault();
          onClose();
        }
      }}
    >
      <div className="highbay-region-header">
        <div className="highbay-region-title-group">
          <h2 id="highbay-terminal-title">{roleTitles[role]}</h2>
          {roleState !== null ? <code>@{roleState.handle}</code> : null}
          <Badge
            variant={
              !hasTerminal
                ? "outline"
                : status === "connected"
                ? "success"
                : status === "error"
                ? "destructive"
                : "default"
            }
          >
            {!hasTerminal
              ? "No attached terminal"
              : status === "connecting"
              ? "Connecting…"
              : status === "connected"
              ? dimensions.cols + "×" + dimensions.rows
              : status === "error"
              ? "Disconnected"
              : "Ended"}
          </Badge>
        </div>
        <div className="highbay-toolbar-actions">
          {hasTerminal ? (
            <>
              <Button
                id="highbay-terminal-interrupt"
                disabled={disabled || interrupting || stopping || restarting || status !== "connected"}
                onClick={() => onInterrupt(role)}
              >
                {interrupting ? "Interrupting…" : "Interrupt (Ctrl-C)"}
              </Button>
              <Button
                id="highbay-terminal-restart"
                disabled={disabled || restarting || stopping}
                onClick={() => onRestart(role)}
              >
                {restarting ? "Restarting…" : "Restart"}
              </Button>
              <Button
                id="highbay-terminal-stop"
                disabled={disabled || stopping || restarting}
                onClick={() => onStop(role)}
              >
                {stopping ? "Stopping…" : "Stop"}
              </Button>
              {status === "error" || status === "closed" ? (
                <Button
                  id="highbay-terminal-reconnect"
                  disabled={disabled || restarting}
                  onClick={() => setReconnectNonce((n) => n + 1)}
                >
                  Reconnect
                </Button>
              ) : null}
            </>
          ) : roleState?.state === "not_started" || roleState?.state === "ended" || roleState?.state === "error" ? (
            <Button
              id="highbay-terminal-restart"
              variant="primary"
              disabled={disabled || restarting}
              onClick={() => onRestart(role)}
            >
              {restarting ? "Starting…" : "Restart"}
            </Button>
          ) : null}
          <button
            ref={closeBtnRef}
            id="highbay-terminal-close"
            type="button"
            className="shadcn-btn highbay-btn"
            onClick={onClose}
          >
            Close terminal
          </button>
        </div>
      </div>

      <p id="highbay-terminal-escape-hint" className="highbay-muted highbay-terminal-hint">
        Press <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>Esc</kbd> to leave terminal keyboard capture and
        focus <strong>Close terminal</strong>.
      </p>

      {actionError !== null ? (
        <Alert id="highbay-terminal-action-error" variant="destructive" role="alert">
          {actionError}
        </Alert>
      ) : null}

      {!hasTerminal ? (
        <p id="highbay-terminal-unavailable">
          {roleState?.state === "not_started" || roleState?.state === "ended"
            ? "This role does not have a running terminal right now. Send a message to Guide or start workers to launch a terminal in herdr-core."
            : "This session was attached from an external editor without a herdr-core terminal. Communicate with it in Team conversation."}
        </p>
      ) : (
        <>
          {connectError !== null ? (
            <Alert id="highbay-terminal-error" variant="destructive" role="alert">
              {connectError}
            </Alert>
          ) : null}
          {closedDetails !== null ? (
            <Alert id="highbay-terminal-closed" variant="warning" role="status">
              Terminal stream closed ({closedDetails.reason}
              {closedDetails.exitCode !== undefined ? ", exit code " + closedDetails.exitCode : ""}
              {closedDetails.signal !== undefined ? ", signal " + closedDetails.signal : ""}).
            </Alert>
          ) : null}
          <div
            id="highbay-terminal-viewport"
            ref={hostRef}
            className="highbay-terminal-viewport"
          />
        </>
      )}
    </Card>
  );
}
