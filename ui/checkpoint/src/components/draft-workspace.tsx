// draft-workspace.tsx implements the unified Moment-0 Launch and + New change
// workspace (DraftChangeWorkspace) plus the independent ProposalReviewQueue,
// correlating progress strictly from server-owned facts (initializing_phase,
// draft_state, storyEvaluation, TeamSnapshot, and TeamMessage seq/reply_to)
// without any baselineIds heuristic.

import React, { useEffect, useRef, useState } from "react";
import { roleChoiceError } from "../agent-choices";
import type {
  ChangeList,
  ChangeListItem,
  InstalledAgentOption,
  RoleAgentChoice,
  StoryRefreshResult,
  TeamConversation,
  TeamMessage,
  TeamSnapshot,
} from "../types";
import { RoleChoiceFields } from "./agent-picker";
import { guideRoleNeedsStart, renderMessageMarkdown } from "./conversation";
import { Alert, Badge, Button, Card } from "./shadcn";

// ProposalReviewQueueProps lists waiting proposals across the project without
// replacing or erasing an active + New change draft.
export interface ProposalReviewQueueProps {
  items: readonly ChangeListItem[];
  disabled: boolean;
  onOpenChange: (changeId: string) => void;
}

// ProposalReviewQueue renders a calm notice whenever Change Cards have
// unconfirmed intent or a waiting proposed_revision ready to review.
export function ProposalReviewQueue({ items, disabled, onOpenChange }: ProposalReviewQueueProps) {
  if (items.length === 0) {
    return null;
  }
  return (
    <Alert id="highbay-proposals-ready" role="status" aria-live="polite">
      <div className="highbay-conversation-attention-row">
        <strong>
          {items.length === 1
            ? "1 proposal waiting for review"
            : items.length + " proposals waiting for review"}
        </strong>
      </div>
      <ul className="highbay-compact-list">
        {items.map((item) => (
          <li key={item.change_id} className="highbay-change-row-header">
            <span>
              <strong>{item.summary}</strong>{" "}
              <Badge variant="warning">
                {item.proposed_revision !== undefined ? "New revision proposed" : "Needs confirmation"}
              </Badge>
            </span>
            <Button
              id={"highbay-review-ready-" + item.change_id}
              variant="secondary"
              disabled={disabled}
              onClick={() => onOpenChange(item.change_id)}
            >
              Review in Agree
            </Button>
          </li>
        ))}
      </ul>
    </Alert>
  );
}

// DraftChangeWorkspaceProps supplies server state and actions for both Moment 0
// (empty project) and + New change (1+ Change Cards).
export interface DraftChangeWorkspaceProps {
  isFirstProjectChange: boolean;
  list: ChangeList | null;
  loadingList: boolean;
  storyEvaluation: StoryRefreshResult | null;
  snapshot: TeamSnapshot | null;
  teamLoading: boolean;
  conversation: TeamConversation | null;
  sentMessageId: string | undefined;
  sendUncertain: boolean;
  sendError: string | null;
  sending: boolean;
  restartingGuide: boolean;
  disabled: boolean;
  primarySubmit: boolean;
  primaryRestartGuide: boolean;
  primaryReply: boolean;
  guideChoice: RoleAgentChoice | undefined;
  agentOptions: readonly InstalledAgentOption[] | null;
  agentOptionsLoading: boolean;
  agentOptionsError: string | null;
  draftText: string;
  proposalsQueue: React.ReactNode;
  onDraftTextChange: (text: string) => void;
  onGuideChoiceChange: (choice: RoleAgentChoice) => void;
  onLoadAgentOptions: () => void;
  onRefreshAgentOptions: () => void;
  onSubmit: () => void;
  onReviewConversation: () => void;
  onReplyToGuide: (replySeq: number) => void;
  onMessageGuide: () => void;
  onRestartGuide: () => void;
  onResetRequest: () => void;
  onWatchGuideTerminal: (invoker: HTMLElement | null) => void;
}

// findLinkedGuideReply walks conversation.messages forward on conversation.channel
// starting from sentMessage.seq, returning the latest unanswered Guide message in
// that reply chain (or null once the user replies or before Guide replies).
export function findLinkedGuideReply(
  conversation: TeamConversation | null,
  sentMessage: TeamMessage | null
): TeamMessage | null {
  if (conversation === null || sentMessage === null) {
    return null;
  }
  const threadSeqs = new Set<number>([sentMessage.seq]);
  let latestInThread: TeamMessage | null = null;
  for (const candidate of conversation.messages) {
    if (
      candidate.target === conversation.channel &&
      candidate.reply_to !== undefined &&
      threadSeqs.has(candidate.reply_to)
    ) {
      threadSeqs.add(candidate.seq);
      latestInThread = candidate;
    }
  }
  return latestInThread !== null && latestInThread.sender_role === "guide"
    ? latestInThread
    : null;
}

// DraftChangeWorkspace renders the single goal composer, live milestones,
// unconfirmed-send warning, post-receipt stop recovery, or linked Guide reply.
export function DraftChangeWorkspace({
  isFirstProjectChange,
  list,
  loadingList,
  storyEvaluation,
  snapshot,
  teamLoading,
  conversation,
  sentMessageId,
  sendUncertain,
  sendError,
  sending,
  restartingGuide,
  disabled,
  primarySubmit,
  primaryRestartGuide,
  primaryReply,
  guideChoice,
  agentOptions,
  agentOptionsLoading,
  agentOptionsError,
  draftText,
  proposalsQueue,
  onDraftTextChange,
  onGuideChoiceChange,
  onLoadAgentOptions,
  onRefreshAgentOptions,
  onSubmit,
  onReviewConversation,
  onReplyToGuide,
  onMessageGuide,
  onRestartGuide,
  onResetRequest,
  onWatchGuideTerminal,
}: DraftChangeWorkspaceProps) {
  const submitButtonRef = useRef<HTMLButtonElement | null>(null);
  const lastCompositionEndAt = useRef<number | null>(null);
  const [customizeAgent, setCustomizeAgent] = useState(false);

  const guide = snapshot?.roles.find((role) => role.role === "guide");
  const guideConnected =
    guide !== undefined && guide.state !== "not_started" && guide.state !== "ended";
  const guideHasTerminal = guide?.has_terminal ?? false;
  const guideState = guide?.state ?? null;
  const guideNeedsLaunch = guideRoleNeedsStart(snapshot);
  const chorusUnavailable =
    snapshot?.chorus_status === "unavailable" || conversation?.chorus_status === "unavailable";

  useEffect(() => {
    if (guideNeedsLaunch) {
      onLoadAgentOptions();
    }
  }, [guideNeedsLaunch, onLoadAgentOptions]);

  const sentMessage =
    sentMessageId !== undefined
      ? (conversation?.messages ?? []).find((msg) => msg.id === sentMessageId) ?? null
      : null;
  const guideReply = findLinkedGuideReply(conversation, sentMessage);

  const selectedOption = (agentOptions ?? []).find(
    (opt) => opt.client === guideChoice?.client && opt.program === guideChoice.program
  );
  const choiceInvalid =
    guideNeedsLaunch &&
    (agentOptions === null ||
      guideChoice === undefined ||
      roleChoiceError(guideChoice, agentOptions) !== null);
  const canSubmit =
    !disabled && !sending && !chorusUnavailable && draftText.trim() !== "" && !choiceInvalid;

  const cardId = isFirstProjectChange ? "highbay-change-list" : "highbay-new-change-stage";
  const titleId = isFirstProjectChange ? "highbay-change-list-title" : "highbay-new-change-title";

  // 1. Terminal blocker: #highbay-team-attention-terminal-guide owns the page's primary action,
  // unless an in-flight, uncertain, or refused send on this form requires user visibility/recovery.
  if (guideState === "needs_you" && !sending && !sendUncertain && sendError === null) {
    return (
      <Card
        id={cardId}
        className="highbay-card highbay-welcome-card"
        aria-labelledby={titleId}
        aria-busy={loadingList ? "true" : undefined}
      >
        {proposalsQueue}
        <h2 id={titleId} tabIndex={-1}>
          Guide needs you
        </h2>
        <p id="highbay-change-list-empty" role="status">
          {guideHasTerminal
            ? "Open Guide’s terminal above to answer its prompt, or reply in Team conversation."
            : "Reply to Guide in Team conversation or in your attached editor session."}
        </p>
        {guideHasTerminal ? (
          <div className="highbay-field-row">
            <Button
              id="highbay-empty-open-guide-terminal"
              disabled={disabled}
              onClick={(event) => onWatchGuideTerminal(event.currentTarget)}
            >
              Open Guide terminal
            </Button>
          </div>
        ) : null}
      </Card>
    );
  }

  // 2. Actively working: on Moment 0, show receipt/project milestones while Guide or
  // initialization runs; on + New change, enter working state only after this request
  // has a confirmed sentMessageId, and never mask an uncertain send banner.
  const firstChangeInitializing = isFirstProjectChange && list?.initializing_phase !== undefined;
  const firstChangeEvaluating = isFirstProjectChange && list?.draft_state === "evaluating_story";
  const activelyWorking =
    !sending &&
    !sendUncertain &&
    sendError === null &&
    (isFirstProjectChange
      ? firstChangeInitializing ||
        firstChangeEvaluating ||
        guideState === "starting" ||
        guideState === "working"
      : sentMessageId !== undefined &&
        (guideState === "starting" ||
          guideState === "working" ||
          storyEvaluation?.state === "running"));

  if (activelyWorking) {
    let heading = isFirstProjectChange ? "Working on your request" : "Drafting new Change Card";
    let statusBody =
      "Guide is working on your request. Progress and your Change Card will appear here automatically.";

    if (firstChangeInitializing && list?.initializing_phase !== undefined) {
      heading = "Preparing your project";
      statusBody = list.initializing_phase;
    } else if (firstChangeEvaluating) {
      heading = "Drafting your Change Card";
      statusBody =
        "Checking the Story and Goa design. Your Change Card will open here automatically when it’s ready.";
    } else if (guideState === "starting") {
      heading = "Starting Guide";
      statusBody =
        "Waiting for your Guide coding agent to start. Your Change Card will open here when ready.";
    } else if (isFirstProjectChange && list?.draft_state !== undefined) {
      heading = "Drafting your Change Card";
      if (list.draft_state === "story_ready") {
        statusBody =
          "Story and design checked — Guide is preparing the Change Card. It will open here automatically.";
      } else if (list.draft_state === "story_failed") {
        statusBody =
          "The last Story check reported an issue — Guide is updating the draft. Your Change Card will open here automatically.";
      } else {
        statusBody =
          "Starter project ready — Guide is writing the service design and Story. Your Change Card will open here automatically.";
      }
    } else if (!isFirstProjectChange && storyEvaluation?.state === "running") {
      statusBody =
        "Checking the Story and Goa design for your new Change Card. Ready proposals appear in Proposals waiting for review.";
    } else if (!isFirstProjectChange) {
      statusBody =
        "Guide is preparing a separate Change Card in the draft. Ready proposals appear in Proposals waiting for review.";
    }

    return (
      <Card
        id={cardId}
        className="highbay-card highbay-welcome-card"
        aria-labelledby={titleId}
        aria-busy="true"
      >
        {proposalsQueue}
        <h2 id={titleId} tabIndex={-1}>
          {heading}
        </h2>
        <p id="highbay-change-list-empty" role="status">
          {statusBody}
        </p>
        <div id="highbay-draft-receipt" className="highbay-panel-section">
          <p className="highbay-muted">
            <strong>
              {sentMessageId !== undefined ? "Sent request:" : "Current activity:"}
            </strong>{" "}
            {sentMessage !== null
              ? sentMessage.text
              : sentMessageId !== undefined
              ? "Message sent earlier in this session."
              : "Project activity in progress."}
          </p>
        </div>
        <div className="highbay-field-row">
          {guideHasTerminal ? (
            <Button
              id="highbay-empty-open-guide-terminal"
              disabled={disabled}
              onClick={(event) => onWatchGuideTerminal(event.currentTarget)}
            >
              Watch Guide terminal
            </Button>
          ) : null}
          {!isFirstProjectChange && sentMessageId !== undefined ? (
            <Button id="highbay-draft-start-another" variant="ghost" disabled={disabled} onClick={onResetRequest}>
              Start another request
            </Button>
          ) : null}
        </div>
      </Card>
    );
  }

  // 3. Post-send reply, post-receipt Guide stop/error, or 0-card story_failed (when not unconfirmed).
  if (!sendUncertain && (sentMessageId !== undefined || (isFirstProjectChange && list?.draft_state === "story_failed"))) {
    // 3a. Linked Chorus reply from Guide (reply_to === sentMessage.seq).
    if (guideState === "waiting" && guideReply !== null) {
      return (
        <Card
          id={cardId}
          className="highbay-card highbay-welcome-card"
          aria-labelledby={titleId}
        >
          {proposalsQueue}
          <h2 id={titleId} tabIndex={-1}>
            Guide replied to your request
          </h2>
          <p id="highbay-change-list-empty" role="status">
            Guide posted a follow-up question or note before proposing the Change Card.
          </p>
          <div id="highbay-draft-guide-reply" className="highbay-panel-section">
            <div className="highbay-message-body">{renderMessageMarkdown(guideReply.text)}</div>
          </div>
          <div className="highbay-field-row">
            <Button
              id="highbay-draft-reply-conversation"
              variant={primaryReply ? "primary" : "default"}
              disabled={disabled}
              onClick={() => onReplyToGuide(guideReply.seq)}
            >
              Reply in conversation
            </Button>
            <Button
              id="highbay-draft-start-another"
              variant="ghost"
              disabled={disabled}
              onClick={onResetRequest}
            >
              Start another request
            </Button>
          </div>
        </Card>
      );
    }

    // 3b. Confirmed receipt (sentMessageId !== undefined) while Guide is ended, failed, or not running.
    if (
      sentMessageId !== undefined &&
      !teamLoading &&
      snapshot !== null &&
      (guideState === "ended" || guideState === "error" || guideState === "not_started")
    ) {
      return (
        <Card
          id={cardId}
          className="highbay-card highbay-welcome-card"
          aria-labelledby={titleId}
        >
          {proposalsQueue}
          <h2 id={titleId} tabIndex={-1}>
            {isFirstProjectChange
              ? "Guide stopped before proposing a Change Card"
              : "Guide is not running"}
          </h2>
          <p id="highbay-change-list-empty" role="status">
            {isFirstProjectChange
              ? "Your request is already saved in the Chorus team channel. Restart Guide so it resumes from that message without posting a duplicate request."
              : "Your request is saved in the Chorus team channel. If Guide still needs to act on it, restart Guide so it resumes without posting a duplicate request, or start another request."}
          </p>
          <div id="highbay-draft-receipt" className="highbay-panel-section">
            <p className="highbay-muted">
              <strong>Posted request:</strong>{" "}
              {sentMessage !== null ? sentMessage.text : "Message sent earlier in this session."}
            </p>
          </div>
          <div className="highbay-field-row" id="highbay-draft-guide-stopped">
            <Button
              id="highbay-draft-restart-guide"
              variant={primaryRestartGuide ? "primary" : "default"}
              disabled={disabled || restartingGuide}
              onClick={onRestartGuide}
            >
              {restartingGuide ? "Restarting Guide…" : "Restart Guide"}
            </Button>
            <Button
              id="highbay-draft-start-another"
              variant="secondary"
              disabled={disabled}
              onClick={onResetRequest}
            >
              Start another request
            </Button>
            {guideHasTerminal ? (
              <Button
                id="highbay-empty-open-guide-terminal"
                variant="ghost"
                disabled={disabled}
                onClick={(event) => onWatchGuideTerminal(event.currentTarget)}
              >
                Watch Guide terminal
              </Button>
            ) : null}
          </div>
        </Card>
      );
    }

    // 3c. 0-card story_failed while Guide is idle/waiting.
    if (isFirstProjectChange && list?.draft_state === "story_failed") {
      return (
        <Card
          id={cardId}
          className="highbay-card highbay-welcome-card"
          aria-labelledby={titleId}
        >
          {proposalsQueue}
          <h2 id={titleId} tabIndex={-1}>
            Story check needs attention
          </h2>
          <p id="highbay-change-list-empty" role="status">
            The last Story check reported an issue. Message Guide in conversation to fix the Story and
            propose the Change Card.
          </p>
          <div className="highbay-field-row">
            <Button
              id="highbay-draft-message-guide"
              variant={primaryReply ? "primary" : "default"}
              disabled={disabled}
              onClick={onMessageGuide}
            >
              Message Guide in conversation
            </Button>
            {sentMessageId !== undefined ? (
              <Button
                id="highbay-draft-start-another"
                variant="ghost"
                disabled={disabled}
                onClick={onResetRequest}
              >
                Start another request
              </Button>
            ) : null}
          </div>
        </Card>
      );
    }

    // 3d. Sent receipt while Guide is waiting (unlinked) or team status is refreshing.
    if (sentMessageId !== undefined) {
      return (
        <Card
          id={cardId}
          className="highbay-card highbay-welcome-card"
          aria-labelledby={titleId}
        >
          {proposalsQueue}
          <h2 id={titleId} tabIndex={-1}>
            Request sent to Guide
          </h2>
          <p id="highbay-change-list-empty" role="status">
            {isFirstProjectChange && list?.draft_state === "story_ready"
              ? "Story and design checked — waiting for Guide to propose the Change Card, or open conversation."
              : "Your request was posted to Guide. Ready proposals appear automatically."}
          </p>
          <div id="highbay-draft-receipt" className="highbay-panel-section">
            <p className="highbay-muted">
              <strong>Posted request:</strong>{" "}
              {sentMessage !== null ? sentMessage.text : "Message sent earlier in this session."}
            </p>
          </div>
          <div className="highbay-field-row">
            <Button
              id="highbay-draft-open-conversation"
              variant="secondary"
              disabled={disabled}
              onClick={onReviewConversation}
            >
              Open conversation
            </Button>
            <Button
              id="highbay-draft-start-another"
              variant="ghost"
              disabled={disabled}
              onClick={onResetRequest}
            >
              Start another request
            </Button>
            {guideHasTerminal ? (
              <Button
                id="highbay-empty-open-guide-terminal"
                variant="ghost"
                disabled={disabled}
                onClick={(event) => onWatchGuideTerminal(event.currentTarget)}
              >
                Watch Guide terminal
              </Button>
            ) : null}
          </div>
        </Card>
      );
    }
  }

  // 4. Ready for Goal (or unconfirmed send with preserved text).
  const bodyCopy = isFirstProjectChange
    ? !guideConnected
      ? "Type your goal below to launch Guide automatically in this project, or run /highbay ($highbay in Codex) from an existing coding agent session. Your Change Card will appear here automatically."
      : list?.draft_state === "story_ready"
      ? "Story and design checked — waiting for Guide to propose the Change Card, or send your request to Guide below. Your Change Card will appear here automatically."
      : list?.draft_state !== undefined
      ? "Starter project ready — Guide drafts the service design, Story, and Change Card next, or send your request to Guide below. Your Change Card will appear here automatically."
      : guideState === "error"
      ? "Guide reported an error before proposing a Change Card. Send your request to Guide below or inspect Guide’s terminal. Your Change Card will appear here automatically."
      : "Send your request to Guide below. Your Change Card will appear here automatically."
    : "Describe the next capability or fix below. Guide prepares a separate Change Card in the draft without overwriting existing Change Cards.";

  return (
    <Card
      id={cardId}
      className="highbay-card highbay-welcome-card"
      aria-labelledby={titleId}
      aria-busy={loadingList ? "true" : undefined}
    >
      {proposalsQueue}
      <h2 id={titleId} tabIndex={-1}>
        {isFirstProjectChange
          ? guideState === "error"
            ? "Check Guide status"
            : "Describe what you want to build"
          : "Start a new change"}
      </h2>
      <p
        id="highbay-change-list-empty"
        role={list?.draft_state !== undefined || guideState === "error" ? "status" : undefined}
      >
        {bodyCopy}
      </p>

      {chorusUnavailable ? (
        <Alert id="highbay-describe-chorus-unavailable" variant="warning" role="status">
          Chorus cannot be reached right now. Run <code>highbay up</code> in your terminal so Highbay
          can deliver your request to Guide.
        </Alert>
      ) : null}

      {!isFirstProjectChange &&
      sentMessageId === undefined &&
      (guideState === "starting" ||
        guideState === "working" ||
        storyEvaluation?.state === "running") ? (
        <p id="highbay-new-change-activity" className="highbay-muted" role="status">
          Guide is currently working in this project. You can still describe a separate change below.
        </p>
      ) : null}

      {guideConnected && !guideHasTerminal ? (
        <p id="highbay-describe-guide-attached" className="highbay-muted">
          <Badge variant="success">Guide connected in your editor</Badge>{" "}
          Highbay will post your request to the team channel.
        </p>
      ) : null}

      <form
        id="highbay-describe-form"
        className="highbay-describe-form"
        method="post"
        action="/team/messages"
        aria-labelledby={titleId}
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
            placeholder="Example: Build a multi-tenant incident platform: a triage agent and remediation agent with MCP tools to search pod logs, correlate deploys, and roll back services with on-call approval."
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
            // Both onInput and onChange call syncText: React requires onChange on controlled
            // textareas, while programmatic DOM input events in tests bypass _valueTracker.
            onInput={(event) => {
              const next = event.currentTarget.value;
              if (next !== draftText) {
                onDraftTextChange(next);
              }
            }}
            onChange={(event) => {
              const next = event.currentTarget.value;
              if (next !== draftText) {
                onDraftTextChange(next);
              }
            }}
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

        {sendUncertain ? (
          <Alert id="highbay-describe-uncertain" variant="warning" role="alert">
            <p>
              <strong>Send not confirmed — check Conversation before sending again.</strong>
            </p>
            {sendError !== null ? <p>{sendError}</p> : null}
            <div className="highbay-field-row">
              <Button
                id="highbay-describe-review-conversation"
                variant="secondary"
                disabled={disabled}
                onClick={onReviewConversation}
              >
                Review conversation
              </Button>
              <Button
                id="highbay-describe-send-again"
                type="submit"
                variant={primarySubmit ? "primary" : "default"}
                disabled={!canSubmit}
              >
                {sending ? "Sending to Guide…" : "Send again"}
              </Button>
            </div>
          </Alert>
        ) : sendError !== null ? (
          <Alert id="highbay-describe-error" variant="destructive" role="alert">
            {sendError}
          </Alert>
        ) : null}

        {!sendUncertain ? (
          <div className="highbay-field-row">
            <Button
              ref={submitButtonRef}
              id="highbay-describe-submit"
              type="submit"
              variant={primarySubmit ? "primary" : "default"}
              disabled={!canSubmit}
            >
              {sending
                ? "Sending to Guide…"
                : guideNeedsLaunch
                ? "Start Guide & draft change"
                : "Draft change"}
            </Button>
            {guideConnected && guideHasTerminal ? (
              <Button
                id="highbay-empty-open-guide-terminal"
                variant="ghost"
                disabled={disabled}
                onClick={(event) => onWatchGuideTerminal(event.currentTarget)}
              >
                Watch Guide terminal
              </Button>
            ) : null}
          </div>
        ) : null}
      </form>

      {isFirstProjectChange && list?.draft_state === undefined && guideState !== "error" ? (
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
