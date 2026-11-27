// conversation.tsx renders the persistent Chorus Team Conversation Rail and the
// Describe-stage prompt composer so users drive guide, builder, and test_writer
// directly from the browser without copying prompts between windows.

import React, { useEffect, useRef, useState } from "react";
import { roleChoiceError } from "../agent-choices";
import type {
  InstalledAgentOption,
  RoleAgentChoice,
  SendTeamMessagePayload,
  TeamConversation,
  TeamMessage,
  TeamRoleState,
  TeamSnapshot,
} from "../types";
import { RoleChoiceFields } from "./agent-picker";
import { Alert, Badge, Button, Card } from "./shadcn";

// ComposerContext is the optional structured stage context attached to a message.
export type ComposerContext = NonNullable<SendTeamMessagePayload["context"]>;

// ComposerRecipient is a valid target role or the shared team channel.
export type ComposerRecipient = SendTeamMessagePayload["to"];

// ComposerDraft holds the user's unsent message text, target, reply link, and
// stage context across background refreshes and stage switches.
export interface ComposerDraft {
  to: ComposerRecipient;
  text: string;
  replyTo: number | null;
  context: ComposerContext | null;
}

// ConversationRailProps supplies the shared Chorus conversation, team state,
// and page-owned send/terminal actions.
export interface ConversationRailProps {
  open: boolean;
  conversation: TeamConversation | null;
  snapshot: TeamSnapshot | null;
  loading: boolean;
  sending: boolean;
  error: string | null;
  sendError: string | null;
  disabled: boolean;
  draft: ComposerDraft;
  guideChoice: RoleAgentChoice | undefined;
  agentOptions: readonly InstalledAgentOption[] | null;
  agentOptionsLoading: boolean;
  agentOptionsError: string | null;
  openTerminalRole: TeamRoleState["role"] | null;
  onToggleOpen: () => void;
  onRefresh: () => void;
  onDraftChange: (patch: Partial<ComposerDraft>) => void;
  onGuideChoiceChange: (choice: RoleAgentChoice) => void;
  onLoadAgentOptions: () => void;
  onRefreshAgentOptions: () => void;
  onSend: () => void;
  onOpenTerminal: (role: TeamRoleState["role"], invoker: HTMLElement | null) => void;
}

// DescribeComposerProps supplies the first-prompt composer shown on an empty
// project's Describe view.
export interface DescribeComposerProps {
  disabled: boolean;
  sending: boolean;
  sendError: string | null;
  guideNeedsLaunch: boolean;
  guideChoice: RoleAgentChoice | undefined;
  agentOptions: readonly InstalledAgentOption[] | null;
  agentOptionsLoading: boolean;
  agentOptionsError: string | null;
  draftText: string;
  onDraftTextChange: (text: string) => void;
  onGuideChoiceChange: (choice: RoleAgentChoice) => void;
  onLoadAgentOptions: () => void;
  onRefreshAgentOptions: () => void;
  onSubmit: () => void;
}

const senderLabels: Record<TeamMessage["sender_role"], string> = {
  you: "You",
  guide: "Guide",
  builder: "Builder",
  test_writer: "Test writer",
  service: "Highbay",
  peer: "Peer",
};

const senderBadgeVariants: Record<
  TeamMessage["sender_role"],
  "default" | "accent" | "success" | "warning" | "outline"
> = {
  you: "accent",
  guide: "success",
  builder: "default",
  test_writer: "outline",
  service: "warning",
  peer: "default",
};

const recipientOrder = ["guide", "team", "builder", "test_writer"] satisfies readonly ComposerRecipient[];

const recipientLabels: Record<ComposerRecipient, string> = {
  guide: "Guide (coding agent)",
  team: "Team channel (#channel)",
  builder: "Builder",
  test_writer: "Test writer",
};

const roleButtonLabels: Record<TeamRoleState["role"], string> = {
  guide: "Guide",
  builder: "Builder",
  test_writer: "Test writer",
};

// messageDomId builds a collision-free DOM identifier from Chorus target and sequence.
export function messageDomId(target: string, seq: number): string {
  return "highbay-msg-" + encodeURIComponent(target) + "-" + String(seq);
}

// guideRoleNeedsStart reports whether sending a message to guide or team will
// auto-start a new guide session in herdr-core (hc).
export function guideRoleNeedsStart(snapshot: TeamSnapshot | null): boolean {
  if (snapshot === null) {
    return false;
  }
  const guide = snapshot.roles.find((role) => role.role === "guide");
  return guide === undefined || guide.state === "not_started" || guide.state === "ended";
}

// describeComposerContext summarizes an attached stage context chip in plain English.
export function describeComposerContext(ctx: ComposerContext): string {
  switch (ctx.type) {
    case "revise_answers":
      return (
        "Revise change " +
        ctx.value.change_id +
        " (" +
        Object.keys(ctx.value.answers).length +
        (Object.keys(ctx.value.answers).length === 1 ? " answer" : " answers") +
        ")"
      );
    case "architecture_element":
      return "Architecture element: " + ctx.value.element_name + " (" + ctx.value.element_id + ")";
    case "story_flow":
      return (
        "Story: " +
        ctx.value.story +
        (ctx.value.flow_key !== undefined ? " · flow " + ctx.value.flow_key : "")
      );
  }
}

// ConversationRail displays the team's Chorus channel, inline role-attention
// actions, and the message composer.
export function ConversationRail({
  open,
  conversation,
  snapshot,
  loading,
  sending,
  error,
  sendError,
  disabled,
  draft,
  guideChoice,
  agentOptions,
  agentOptionsLoading,
  agentOptionsError,
  openTerminalRole,
  onToggleOpen,
  onRefresh,
  onDraftChange,
  onGuideChoiceChange,
  onLoadAgentOptions,
  onRefreshAgentOptions,
  onSend,
  onOpenTerminal,
}: ConversationRailProps) {
  const listEndRef = useRef<HTMLDivElement | null>(null);
  const submitButtonRef = useRef<HTMLButtonElement | null>(null);
  const lastMessageCount = useRef(0);
  const lastCompositionEndAt = useRef<number | null>(null);
  const [customizeGuide, setCustomizeGuide] = useState(false);

  const needsGuideStart =
    (draft.to === "guide" || draft.to === "team") && guideRoleNeedsStart(snapshot);

  useEffect(() => {
    if (needsGuideStart) {
      onLoadAgentOptions();
    }
  }, [needsGuideStart, onLoadAgentOptions]);

  const messages = conversation?.messages ?? [];
  useEffect(() => {
    if (open && messages.length > lastMessageCount.current) {
      listEndRef.current?.scrollIntoView({ block: "nearest" });
    }
    lastMessageCount.current = messages.length;
  }, [open, messages.length]);

  const needsYouRoles = (snapshot?.roles ?? []).filter((role) => role.state === "needs_you");
  const terminalRoles = (snapshot?.roles ?? []).filter((role) => role.has_terminal);
  const chorusUnavailable =
    conversation?.chorus_status === "unavailable" || snapshot?.chorus_status === "unavailable";
  const selectedGuideOption = (agentOptions ?? []).find(
    (opt) => opt.client === guideChoice?.client && opt.program === guideChoice.program
  );
  const guideInvalid =
    needsGuideStart &&
    (agentOptions === null ||
      guideChoice === undefined ||
      roleChoiceError(guideChoice, agentOptions) !== null);
  const canSend =
    !disabled &&
    !sending &&
    !chorusUnavailable &&
    draft.text.trim() !== "" &&
    !guideInvalid;

  if (!open) {
    return (
      <div className="highbay-conversation-collapsed">
        <Button
          id="highbay-conversation-toggle"
          aria-expanded={false}
          aria-controls="highbay-conversation-panel"
          variant={needsYouRoles.length > 0 ? "primary" : "default"}
          onClick={onToggleOpen}
        >
          Team chat ({messages.length})
          {needsYouRoles.length > 0 ? " · Needs you" : ""}
        </Button>
      </div>
    );
  }

  return (
    <Card
      id="highbay-conversation-panel"
      className="highbay-card highbay-conversation-card"
      aria-labelledby="highbay-conversation-title"
    >
      <div className="highbay-region-header">
        <div className="highbay-region-title-group">
          <h2 id="highbay-conversation-title">Team conversation</h2>
          {conversation?.channel ? <code>{conversation.channel}</code> : null}
        </div>
        <div className="highbay-toolbar-actions">
          <Button
            id="highbay-conversation-refresh"
            disabled={disabled || loading}
            onClick={onRefresh}
          >
            {loading ? "Refreshing…" : "Refresh"}
          </Button>
          <Button
            id="highbay-conversation-toggle"
            aria-expanded={true}
            aria-controls="highbay-conversation-panel"
            variant="ghost"
            onClick={onToggleOpen}
          >
            Hide
          </Button>
        </div>
      </div>

      {terminalRoles.length > 0 ? (
        <div className="highbay-conversation-terminals" role="group" aria-label="Live role terminals">
          {terminalRoles.map((role) => (
            <Button
              key={role.role}
              id={"highbay-rail-terminal-" + role.role}
              variant={role.state === "needs_you" ? "primary" : "pill"}
              aria-pressed={openTerminalRole === role.role}
              disabled={disabled}
              onClick={(event) => onOpenTerminal(role.role, event.currentTarget)}
            >
              {roleButtonLabels[role.role]} terminal
              {role.state === "working"
                ? " · Working"
                : role.state === "needs_you"
                ? " · Needs you"
                : ""}
            </Button>
          ))}
        </div>
      ) : null}

      {chorusUnavailable ? (
        <Alert id="highbay-conversation-unavailable" variant="warning" role="status">
          Chorus cannot be reached right now. Start or check Chorus with <code>highbay up</code> to
          exchange messages with your team.
        </Alert>
      ) : null}

      {error !== null ? (
        <Alert id="highbay-conversation-error" variant="destructive" role="alert">
          <strong>Could not read team conversation. </strong>
          {error}
        </Alert>
      ) : null}

      {needsYouRoles.map((role) => (
        <Alert
          key={role.role}
          id={"highbay-rail-attention-" + role.role}
          variant="warning"
          role="status"
        >
          <div className="highbay-conversation-attention-row">
            <span>
              <strong>{roleButtonLabels[role.role]} needs you</strong>
              {role.attention !== "none" ? " (" + role.attention + ")" : ""}.
            </span>
            {role.has_terminal ? (
              <Button
                id={"highbay-rail-unblock-" + role.role}
                variant="primary"
                disabled={disabled}
                onClick={(event) => onOpenTerminal(role.role, event.currentTarget)}
              >
                Open {roleButtonLabels[role.role]} terminal
              </Button>
            ) : null}
          </div>
        </Alert>
      ))}

      {conversation?.pin !== undefined ? (
        <details className="highbay-details highbay-conversation-pin" id="highbay-conversation-pin">
          <summary>Pinned team board (#{conversation.pin.seq})</summary>
          <div className="highbay-message-body">{renderMessageMarkdown(conversation.pin.text)}</div>
        </details>
      ) : null}

      <div
        id="highbay-conversation-messages"
        className="highbay-conversation-messages"
        role="log"
        aria-label="Messages"
        aria-live="polite"
      >
        {conversation === null && loading ? (
          <p className="highbay-muted">Loading conversation…</p>
        ) : messages.length === 0 ? (
          <p id="highbay-conversation-empty" className="highbay-muted">
            No messages yet. Send a message below to guide your team or describe a change.
          </p>
        ) : (
          <ol className="highbay-message-list" role="list">
            {messages.map((msg) => {
              const isPrimaryChannel =
                conversation !== null && msg.target === conversation.channel;
              return (
                <li
                  key={msg.id}
                  id={messageDomId(msg.target, msg.seq)}
                  className={
                    "highbay-message-item" +
                    (msg.sender_role === "you" ? " highbay-message-you" : "")
                  }
                  data-sender-role={msg.sender_role}
                  data-target={msg.target}
                  data-seq={String(msg.seq)}
                >
                  <div className="highbay-message-meta">
                    <span>
                      <Badge variant={senderBadgeVariants[msg.sender_role]}>
                        {senderLabels[msg.sender_role]}
                      </Badge>
                      <code>@{msg.sender}</code>
                      {!isPrimaryChannel ? (
                        <>
                          {" → "}
                          <code>{msg.target}</code>
                        </>
                      ) : null}
                    </span>
                    <span className="highbay-muted">
                      #{msg.seq} ·{" "}
                      <time dateTime={new Date(msg.sent_at_ms).toISOString()}>
                        {formatMessageTime(msg.sent_at_ms)}
                      </time>
                    </span>
                  </div>
                  {msg.reply_to !== undefined ? (
                    <p className="highbay-message-reply-ref">
                      Replying to{" "}
                      <a href={"#" + messageDomId(msg.target, msg.reply_to)}>
                        message #{msg.reply_to}
                      </a>
                    </p>
                  ) : null}
                  <div className="highbay-message-body">{renderMessageMarkdown(msg.text)}</div>
                  {isPrimaryChannel ? (
                    <div className="highbay-message-actions">
                      <Button
                        variant="ghost"
                        disabled={disabled}
                        aria-label={"Reply to message #" + msg.seq + " from " + msg.sender}
                        onClick={() => {
                          const nextTo: ComposerRecipient =
                            msg.sender_role === "builder"
                              ? "builder"
                              : msg.sender_role === "test_writer"
                              ? "test_writer"
                              : msg.sender_role === "guide"
                              ? "guide"
                              : draft.to;
                          onDraftChange({ replyTo: msg.seq, to: nextTo });
                          document.getElementById("highbay-composer-input")?.focus();
                        }}
                      >
                        Reply
                      </Button>
                    </div>
                  ) : null}
                </li>
              );
            })}
          </ol>
        )}
        <div ref={listEndRef} />
      </div>

      <form
        id="highbay-composer-form"
        className="highbay-composer-form"
        method="post"
        action="/team/messages"
        aria-labelledby="highbay-conversation-title"
        onSubmit={(event) => {
          event.preventDefault();
          if (!canSend) {
            return;
          }
          onSend();
        }}
      >
        {draft.replyTo !== null ? (
          <div className="highbay-composer-chip" id="highbay-composer-reply-chip">
            <span>Replying to #{draft.replyTo}</span>
            <Button
              variant="ghost"
              disabled={disabled || sending}
              onClick={() => onDraftChange({ replyTo: null })}
            >
              Clear reply
            </Button>
          </div>
        ) : null}

        {draft.context !== null ? (
          <div className="highbay-composer-chip" id="highbay-composer-context-chip">
            <span>Attached: {describeComposerContext(draft.context)}</span>
            <Button
              variant="ghost"
              disabled={disabled || sending}
              onClick={() => onDraftChange({ context: null })}
            >
              Remove attachment
            </Button>
          </div>
        ) : null}

        <div className="highbay-composer-controls">
          <label className="highbay-field-label" htmlFor="highbay-composer-to">
            Send to
            <select
              id="highbay-composer-to"
              name="to"
              className="highbay-input"
              value={draft.to}
              disabled={disabled || sending}
              onChange={(event) => {
                const value = event.currentTarget.value;
                if (
                  value === "guide" ||
                  value === "team" ||
                  value === "builder" ||
                  value === "test_writer"
                ) {
                  onDraftChange({ to: value });
                }
              }}
            >
              {recipientOrder.map((key) => (
                <option key={key} value={key}>
                  {recipientLabels[key]}
                </option>
              ))}
            </select>
          </label>
        </div>

        {needsGuideStart ? (
          <div className="highbay-guide-launch-box" id="highbay-rail-guide-choice">
            <div className="highbay-conversation-attention-row">
              <span className="highbay-muted">
                Guide starts automatically on send:{" "}
                <strong>
                  {selectedGuideOption !== undefined
                    ? selectedGuideOption.title
                    : agentOptionsLoading
                    ? "Detecting coding agents…"
                    : "Choose coding agent"}
                </strong>
              </span>
              <Button
                variant="ghost"
                aria-expanded={customizeGuide}
                aria-controls="highbay-rail-guide-fields"
                disabled={disabled || sending}
                onClick={() => setCustomizeGuide((prev) => !prev)}
              >
                {customizeGuide ? "Hide agent options" : "Change agent"}
              </Button>
            </div>
            {agentOptionsError !== null ? (
              <Alert variant="destructive" role="alert">
                Could not detect installed agents: {agentOptionsError}{" "}
                <Button disabled={disabled || agentOptionsLoading} onClick={onRefreshAgentOptions}>
                  Retry
                </Button>
              </Alert>
            ) : null}
            {customizeGuide || selectedGuideOption === undefined ? (
              <div id="highbay-rail-guide-fields">
                {agentOptions !== null && agentOptions.length > 0 ? (
                  <RoleChoiceFields
                    idPrefix="highbay-rail-guide"
                    role="guide"
                    legend="Guide coding agent"
                    options={agentOptions}
                    choice={guideChoice}
                    disabled={disabled || sending}
                    compact={true}
                    onChange={onGuideChoiceChange}
                  />
                ) : !agentOptionsLoading && agentOptions !== null ? (
                  <p className="highbay-muted">
                    No supported coding agent found on PATH. Install Claude Code, Codex, Gemini CLI,
                    Cursor, or Antigravity, then{" "}
                    <Button disabled={disabled} onClick={onRefreshAgentOptions}>
                      refresh agents
                    </Button>
                    .
                  </p>
                ) : null}
              </div>
            ) : null}
          </div>
        ) : null}

        <label className="highbay-field-label" htmlFor="highbay-composer-input">
          Message
          <textarea
            id="highbay-composer-input"
            name="text"
            className="shadcn-textarea highbay-input"
            rows={3}
            placeholder="Ask your Guide or team… (Enter to send, Shift+Enter for a new line)"
            value={draft.text}
            disabled={disabled || sending}
            enterKeyHint="send"
            onCompositionEnd={(event) => {
              lastCompositionEndAt.current = event.nativeEvent.timeStamp;
            }}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey) {
                if (event.nativeEvent.isComposing) {
                  return;
                }
                if (
                  lastCompositionEndAt.current !== null &&
                  Math.abs(event.nativeEvent.timeStamp - lastCompositionEndAt.current) < 50
                ) {
                  return;
                }
                event.preventDefault();
                if (!canSend) {
                  return;
                }
                event.currentTarget.form?.requestSubmit(submitButtonRef.current ?? undefined);
              }
            }}
            onChange={(event) => onDraftChange({ text: event.currentTarget.value })}
          />
        </label>

        {sendError !== null ? (
          <Alert id="highbay-composer-error" variant="destructive" role="alert">
            {sendError}
          </Alert>
        ) : null}

        <div className="highbay-composer-footer">
          <button
            ref={submitButtonRef}
            id="highbay-composer-send"
            type="submit"
            className="shadcn-btn shadcn-btn-primary highbay-btn highbay-btn-primary"
            disabled={!canSend}
          >
            {sending ? "Sending…" : needsGuideStart ? "Start Guide & send" : "Send message"}
          </button>
        </div>
      </form>
    </Card>
  );
}

// DescribeComposer lets the user describe their first feature directly in the
// Describe welcome card, launching guide automatically if none is running.
export function DescribeComposer({
  disabled,
  sending,
  sendError,
  guideNeedsLaunch,
  guideChoice,
  agentOptions,
  agentOptionsLoading,
  agentOptionsError,
  draftText,
  onDraftTextChange,
  onGuideChoiceChange,
  onLoadAgentOptions,
  onRefreshAgentOptions,
  onSubmit,
}: DescribeComposerProps) {
  const submitButtonRef = useRef<HTMLButtonElement | null>(null);
  const lastCompositionEndAt = useRef<number | null>(null);
  const [customizeAgent, setCustomizeAgent] = useState(false);

  useEffect(() => {
    if (guideNeedsLaunch) {
      onLoadAgentOptions();
    }
  }, [guideNeedsLaunch, onLoadAgentOptions]);

  const selectedOption = (agentOptions ?? []).find(
    (opt) => opt.client === guideChoice?.client && opt.program === guideChoice.program
  );
  const choiceInvalid =
    guideNeedsLaunch &&
    (agentOptions === null ||
      guideChoice === undefined ||
      roleChoiceError(guideChoice, agentOptions) !== null);
  const canSubmit = !disabled && !sending && draftText.trim() !== "" && !choiceInvalid;

  return (
    <form
      id="highbay-describe-form"
      className="highbay-describe-form"
      method="post"
      action="/team/messages"
      aria-labelledby="highbay-change-list-title"
      onSubmit={(event) => {
        event.preventDefault();
        if (!canSubmit) {
          return;
        }
        onSubmit();
      }}
    >
      <label className="highbay-field-label" htmlFor="highbay-describe-input">
        What would you like to build or change?
        <textarea
          id="highbay-describe-input"
          name="describe_text"
          className="shadcn-textarea highbay-input"
          rows={4}
          placeholder="Example: Refunds up to $50 receive instant store credit; larger refunds require manager approval."
          value={draftText}
          disabled={disabled || sending}
          enterKeyHint="send"
          onCompositionEnd={(event) => {
            lastCompositionEndAt.current = event.nativeEvent.timeStamp;
          }}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey) {
              if (event.nativeEvent.isComposing) {
                return;
              }
              if (
                lastCompositionEndAt.current !== null &&
                Math.abs(event.nativeEvent.timeStamp - lastCompositionEndAt.current) < 50
              ) {
                return;
              }
              event.preventDefault();
              if (!canSubmit) {
                return;
              }
              event.currentTarget.form?.requestSubmit(submitButtonRef.current ?? undefined);
            }
          }}
          onChange={(event) => onDraftTextChange(event.currentTarget.value)}
        />
      </label>

      {guideNeedsLaunch ? (
        <div className="highbay-guide-launch-box" id="highbay-describe-guide-choice">
          <div className="highbay-conversation-attention-row">
            <span>
              Guide:{" "}
              <strong>
                {selectedOption !== undefined
                  ? selectedOption.title
                  : agentOptionsLoading
                  ? "Finding installed coding agents…"
                  : "Select an installed coding agent"}
              </strong>
            </span>
            <Button
              variant="ghost"
              aria-expanded={customizeAgent}
              aria-controls="highbay-describe-guide-fields"
              disabled={disabled || sending}
              onClick={() => setCustomizeAgent((prev) => !prev)}
            >
              {customizeAgent ? "Hide agent settings" : "Choose coding agent"}
            </Button>
          </div>
          {agentOptionsError !== null ? (
            <Alert variant="destructive" role="alert">
              Could not read installed agents: {agentOptionsError}{" "}
              <Button disabled={disabled || agentOptionsLoading} onClick={onRefreshAgentOptions}>
                Retry
              </Button>
            </Alert>
          ) : null}
          {customizeAgent || selectedOption === undefined ? (
            <div id="highbay-describe-guide-fields">
              {agentOptions !== null && agentOptions.length > 0 ? (
                <RoleChoiceFields
                  idPrefix="highbay-describe-guide"
                  role="guide"
                  legend="Guide coding agent"
                  options={agentOptions}
                  choice={guideChoice}
                  disabled={disabled || sending}
                  compact={true}
                  onChange={onGuideChoiceChange}
                />
              ) : !agentOptionsLoading && agentOptions !== null ? (
                <p className="highbay-muted">
                  No supported coding agent is installed yet. Install Claude Code, Codex, Gemini
                  CLI, Cursor, or Antigravity, then{" "}
                  <Button disabled={disabled} onClick={onRefreshAgentOptions}>
                    refresh installed agents
                  </Button>
                  .
                </p>
              ) : null}
            </div>
          ) : null}
        </div>
      ) : null}

      {sendError !== null ? (
        <Alert id="highbay-describe-error" variant="destructive" role="alert">
          {sendError}
        </Alert>
      ) : null}

      <div className="highbay-field-row">
        <button
          ref={submitButtonRef}
          id="highbay-describe-submit"
          type="submit"
          className="shadcn-btn shadcn-btn-primary highbay-btn highbay-btn-primary"
          disabled={!canSubmit}
        >
          {sending
            ? "Sending to Guide…"
            : guideNeedsLaunch
            ? "Start Guide & draft Change Card"
            : "Send to Guide"}
        </button>
      </div>
    </form>
  );
}

// formatMessageTime shows concise local hours and minutes for today's messages
// and adds the calendar date for older messages.
function formatMessageTime(sentAtMs: number): string {
  const date = new Date(sentAtMs);
  return date.toLocaleString(undefined, {
    ...(date.toDateString() === new Date().toDateString()
      ? {}
      : { month: "short", day: "numeric" }),
    hour: "numeric",
    minute: "2-digit",
  });
}

// isMarkdownBlockStart reports whether line starts a fenced block, non-empty
// heading, bulleted list, or numbered list, using the exact same rules as
// renderMessageMarkdown so paragraph scanning always agrees with block parsing.
function isMarkdownBlockStart(line: string): boolean {
  return (
    line.startsWith("```") ||
    /^(#{1,4})\s+\S/.test(line) ||
    /^[-*]\s+/.test(line) ||
    /^\d+\.\s+/.test(line)
  );
}

// renderMessageMarkdown renders safe React elements for common Markdown blocks
// (fenced code blocks, headings, lists, and inline code/emphasis) without
// injecting raw HTML. Every iteration unconditionally advances index.
export function renderMessageMarkdown(text: string): React.ReactNode {
  const normalized = text.replace(/\r\n/g, "\n");
  const blocks: React.ReactNode[] = [];
  const lines = normalized.split("\n");
  let index = 0;

  while (index < lines.length) {
    const line = lines[index];
    if (line.trim() === "") {
      index++;
      continue;
    }

    if (line.startsWith("```")) {
      const codeLines: string[] = [];
      index++;
      while (index < lines.length && !lines[index].startsWith("```")) {
        codeLines.push(lines[index]);
        index++;
      }
      if (index < lines.length && lines[index].startsWith("```")) {
        index++;
      }
      blocks.push(
        <pre key={"code-" + index}>
          <code>{codeLines.join("\n")}</code>
        </pre>
      );
      continue;
    }

    const headingMatch = /^(#{1,4})\s+(\S.*)$/.exec(line);
    if (headingMatch !== null) {
      blocks.push(
        <p key={"h-" + index}>
          <strong>{renderInlineMarkdown(headingMatch[2])}</strong>
        </p>
      );
      index++;
      continue;
    }

    if (/^[-*]\s+/.test(line)) {
      const items: string[] = [];
      while (index < lines.length && /^[-*]\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^[-*]\s+/, ""));
        index++;
      }
      blocks.push(
        <ul key={"ul-" + index}>
          {items.map((item, itemIdx) => (
            <li key={String(itemIdx)}>{renderInlineMarkdown(item)}</li>
          ))}
        </ul>
      );
      continue;
    }

    if (/^\d+\.\s+/.test(line)) {
      const items: string[] = [];
      while (index < lines.length && /^\d+\.\s+/.test(lines[index])) {
        items.push(lines[index].replace(/^\d+\.\s+/, ""));
        index++;
      }
      blocks.push(
        <ol key={"ol-" + index}>
          {items.map((item, itemIdx) => (
            <li key={String(itemIdx)}>{renderInlineMarkdown(item)}</li>
          ))}
        </ol>
      );
      continue;
    }

    const paragraphLines: string[] = [lines[index++]];
    while (
      index < lines.length &&
      lines[index].trim() !== "" &&
      !isMarkdownBlockStart(lines[index])
    ) {
      paragraphLines.push(lines[index++]);
    }
    blocks.push(
      <p key={"p-" + index}>
        {paragraphLines.map((pLine, pIdx) => (
          <React.Fragment key={String(pIdx)}>
            {pIdx > 0 ? <br /> : null}
            {renderInlineMarkdown(pLine)}
          </React.Fragment>
        ))}
      </p>
    );
  }

  return blocks.length > 0 ? blocks : <p>{text}</p>;
}

// renderInlineMarkdown turns inline `code` and **bold** spans into React nodes.
function renderInlineMarkdown(line: string): React.ReactNode[] {
  const nodes: React.ReactNode[] = [];
  const tokenPattern = /(`[^`]+`|\*\*[^*]+\*\*)/g;
  let lastIndex = 0;
  let match: RegExpExecArray | null;

  while ((match = tokenPattern.exec(line)) !== null) {
    if (match.index > lastIndex) {
      nodes.push(line.slice(lastIndex, match.index));
    }
    const token = match[0];
    if (token.startsWith("`") && token.endsWith("`")) {
      nodes.push(<code key={"c-" + match.index}>{token.slice(1, -1)}</code>);
    } else if (token.startsWith("**") && token.endsWith("**")) {
      nodes.push(<strong key={"b-" + match.index}>{token.slice(2, -2)}</strong>);
    } else {
      nodes.push(token);
    }
    lastIndex = match.index + token.length;
  }
  if (lastIndex < line.length) {
    nodes.push(line.slice(lastIndex));
  }
  return nodes;
}
