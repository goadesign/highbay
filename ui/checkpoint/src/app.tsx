// Package main implements the Highbay Studio views page (/views). The page
// exchanges the one-use link `highbay` opened it with for an origin-scoped
// Bearer session, then coordinates the sticky Studio header
// (#highbay-checkpoint-nav), global notices (#highbay-checkpoint-notices),
// single-column main stage (main#highbay-checkpoint-view[data-stage=...]),
// stage-docked terminal drawer (section#highbay-terminal-region), full-height
// Chorus conversation dock (aside#highbay-conversation-rail), and diagnostics
// drawer (aside#highbay-trace-region).

// zod-jitless stays the first import: it configures zod before any imported
// module builds a schema.
import "./zod-jitless";
import React, { useEffect } from "react";
import { flushSync } from "react-dom";
import { createRoot, Root } from "react-dom/client";
import { ArchitecturePreviewView, IncomingChangeCardView } from "./components/c4-graph";
import { ChangeListView } from "./components/change-list";
import {
  ComposerContext,
  ComposerDraft,
  ComposerRecipient,
  ConversationRail,
  contextRequiresGuide,
  guideRoleNeedsStart,
} from "./components/conversation";
import { DraftChangeWorkspace, ProposalReviewQueue } from "./components/draft-workspace";
import { CP1EditorView } from "./components/cp1-editor";
import {
  BuildMergeInstructions,
  BuildSectionView,
  BuildStartReceipt,
  buildTimestampNanos,
  runBelongsToBuild,
} from "./components/build";
import { AgentPickerView } from "./components/agent-picker";
import {
  choicesForRoles,
  proposeGuideChoice,
  proposeWorkerChoices,
  roleChoiceError,
  workerChoicesReady,
} from "./agent-choices";
import type { WorkerChoices, WorkerRole } from "./agent-choices";
import { MergeFeedback, MergePrimaryAction, MergedBranchVerification } from "./components/merge-action";
import type { MergeControls } from "./components/merge-action";
import { VerificationView } from "./components/cp4-verification";
import { ProposedRevisionView } from "./components/proposal";
import { ApprovalStage } from "./components/approval-stage";
import { JourneyStep, WorkspaceFrame } from "./components/workspace-frame";
import { SessionEndedNotice, SignedOutReason, SignedOutView } from "./components/session";
import { StoryStageView } from "./components/story";
import {
  GlobalTeamAttention,
  TeamFactsError,
  TeamStageProps,
  TeamStageView,
  teamAttentionText,
} from "./components/team";
import {
  TerminalDrawer,
  TerminalDrawerSize,
  TerminalTicketOutcome,
} from "./components/terminal-drawer";
import { UpdateBanner, UpdateProgress } from "./components/update-banner";
import {
  LiveTargetStageView,
  ModelDraft,
  ServerAnswer,
  describeApplied,
} from "./components/live-target";
import { Alert, Badge, Button, Card } from "./components/shadcn";
import { TraceDrawer, TraceFilter } from "./components/trace-drawer";
import { answersNeedingRevision, isDraftDirty, PreservedDraft } from "./context/cp1-draft";
import {
  AmbiguityQuestion,
  ArchitecturePreviewResult,
  ApprovalItem,
  ChangeCardResult,
  ChangeBuild,
  ChangeList,
  ConfigureModelRequest,
  ExampleItem,
  LiveTargetStatus,
  ModelConnection,
  OperationName,
  OpenBuildRefusal,
  ReplacedBuild,
  RoleAgentChoice,
  ProposedRevisionItem,
  Operations,
  ROUTES,
  SendTeamMessagePayload,
  ServerFailure,
  StartedBuild,
  StoryRefreshResult,
  TeamConversation,
  TraceList,
  TeamRoleState,
  TeamSnapshot,
  TestFindings,
  UpdateStatusResult,
  VerificationListResult,
  VerificationReview,
  VerificationStatusResult,
  isPlainObject,
} from "./types";

// WAIT_RETRY_MS is how long the page waits before asking again after the
// first failed round of its waiting read. Each further failed round doubles
// the wait, up to WAIT_RETRY_MAX_MS.
const WAIT_RETRY_MS = 5000;
// WAIT_RETRY_MAX_MS bounds the wait between failed rounds.
const WAIT_RETRY_MAX_MS = 30000;

// Failure describes one request the server refused or never answered.
interface Failure {
  kind: "session" | "server" | "network" | "aborted";
  status: number;
  message: string;
  body?: ServerFailure;
}

// Outcome is the page's view of one finished request.
type Outcome<T> = { ok: true; result: T } | ({ ok: false } & Failure);

// PendingCall records one outstanding request so views derive their waiting
// state from the requests actually in flight.
interface PendingCall {
  op: OperationName;
  subject: string;
}

// ActiveEditorState keeps received saved values separate from editable drafts
// for one specific Change Card in editorsByChangeId.
interface ActiveEditorState {
  card: ChangeCardResult;
  priorCard: ChangeCardResult | null;
  incomingCard: ChangeCardResult | null;
  draftAnswers: Record<string, string>;
  draftExamples: ExampleItem[];
  preservedDraft: PreservedDraft | null;
  formError: { message: string; source: "input" | "request" } | null;
  proposalAnswers: Record<string, string>;
  proposalError: string | null;
}

// ChangeStageName names one of the 5 stages in the Change Stepper.
type ChangeStageName = "agree" | "arch" | "build" | "verify" | "approve";

// WorkspaceView names the single stage rendered in main#highbay-checkpoint-view.
type WorkspaceView =
  | { kind: "changes" }
  | { kind: "new" }
  | { kind: "change"; changeId: string; stage: ChangeStageName }
  | { kind: "team" }
  | { kind: "story" }
  | { kind: "live" };

// MobileSurface selects the single visible surface below 1080px.
type MobileSurface = "stage" | "conversation" | "terminal";

// HistoryViewState stores opaque navigation IDs in window.history.state on
// exact /views without ever storing user-typed prompt or draft text.
interface HistoryViewState {
  view: "changes" | "new" | "change" | "team" | "story" | "live";
  changeId?: string;
  stage?: ChangeStageName;
  runId?: string;
  mobileSurface?: MobileSurface;
  newDraft?: { sentMessageId?: string };
}

// SessionState is the page's session lifecycle.
type SessionState = "starting" | "active" | "signed-out" | "ended";

// WorkspaceSubjectDescriptor describes the displayed subject in Current view details.
interface WorkspaceSubjectDescriptor {
  role: "editor" | "incoming" | "result";
  label: string;
  stage: string;
  changeId?: string;
  requestRevision?: string;
  detail?: string;
}

// DraftRevisionAidLoader triggers initial agent-option discovery inside a React
// effect rather than mutating state during render.
function DraftRevisionAidLoader({
  active,
  onLoad,
}: {
  active: boolean;
  onLoad: () => void;
}) {
  useEffect(() => {
    if (active) {
      onLoad();
    }
  }, [active, onLoad]);
  return null;
}

// StagePrerequisiteCard explains calmly when Architecture or Build requires
// confirming the Change Card in Agree first, without firing a 409 RPC.
function StagePrerequisiteCard({
  stageTitle,
  explanation,
  primaryGoAgree,
  onGoAgree,
}: {
  stageTitle: string;
  explanation: string;
  primaryGoAgree: boolean;
  onGoAgree: () => void;
}) {
  return (
    <Card
      id="highbay-stage-prerequisite"
      className="highbay-card"
      role="status"
      aria-labelledby="highbay-prerequisite-title"
    >
      <h2 id="highbay-prerequisite-title" tabIndex={-1}>
        {stageTitle + " follows Agree"}
      </h2>
      <p>{explanation}</p>
      <div className="highbay-field-row">
        <Button
          id="highbay-prerequisite-go-agree"
          variant={primaryGoAgree ? "primary" : "secondary"}
          onClick={onGoAgree}
        >
          Go to Agree
        </Button>
      </div>
    </Card>
  );
}

// ReadOnlyAgreementSummary renders the folded Agreement details disclosure on
// Build so users can check the agreed decisions and saved approval without leaving Build.
function ReadOnlyAgreementSummary({
  card,
  onOpenAgree,
  onReviewApproval,
}: {
  card: ChangeCardResult;
  onOpenAgree: () => void;
  onReviewApproval: () => void;
}) {
  const ambiguities = Array.isArray(card.ambiguities) ? card.ambiguities : [];
  return (
    <>
      <Card className="highbay-card">
        <h2>{"Agreed: " + card.prompt}</h2>
        <details className="highbay-details" id="highbay-agreement-details">
          <summary>Agreement details</summary>
          {card.status === "confirmed" && ambiguities.length > 0 ? (
            <div id="highbay-confirmed-decisions" role="region" aria-label="Confirmed Decisions">
              <p>
                <strong>Confirmed Decisions:</strong>
              </p>
              <ul>
                {ambiguities.map((amb) => (
                  <li key={amb.id}>
                    <strong>{amb.question}</strong> <code>{card.answers?.[amb.id] ?? ""}</code>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
          <p id="highbay-agreement-summary" tabIndex={-1}>
            <strong>
              {card.status === "confirmed" && ambiguities.length > 0
                ? "Initial Proposal Summary: "
                : "Summary: "}
            </strong>
            {card.summary}
          </p>
          {card.confirmed_by ? (
            <p>
              <strong>
                {card.confirmed_by.startsWith("uid:")
                  ? "Agreed by (operating system user): "
                  : "Agreed by (recorded user identity): "}
              </strong>
              <code>{card.confirmed_by}</code>
            </p>
          ) : null}
          <details className="highbay-details">
            <summary>Agreement identifiers</summary>
            <p>
              change_id: <code>{card.change_id}</code> · status: <code>{card.status}</code> ·
              request_revision: <code>{card.request_revision}</code>
            </p>
          </details>
          <div className="highbay-field-row">
            <Button variant="secondary" onClick={onOpenAgree}>
              Open in Agree
            </Button>
          </div>
        </details>
      </Card>
      {card.approval !== undefined || card.unverified_approval !== undefined ? (
        <section className="highbay-approval-summary" aria-label="Saved approval">
          <p>
            <strong>
              {card.unverified_approval !== undefined
                ? "Saved approval needs checking."
                : "Approval recorded."}
            </strong>{" "}
            Read its evidence, verified commits and current status in Approve.
          </p>
          <Button id="highbay-review-approval" onClick={onReviewApproval}>
            Review saved approval
          </Button>
        </section>
      ) : null}
    </>
  );
}

// isHistoryViewState narrows unknown browser history state to HistoryViewState.
function isHistoryViewState(value: unknown): value is HistoryViewState {
  if (!isPlainObject(value)) {
    return false;
  }
  const view = value.view;
  if (
    view !== "changes" &&
    view !== "new" &&
    view !== "change" &&
    view !== "team" &&
    view !== "story" &&
    view !== "live"
  ) {
    return false;
  }
  if (value.changeId !== undefined && typeof value.changeId !== "string") {
    return false;
  }
  if (
    value.stage !== undefined &&
    value.stage !== "agree" &&
    value.stage !== "arch" &&
    value.stage !== "build" &&
    value.stage !== "verify" &&
    value.stage !== "approve"
  ) {
    return false;
  }
  if (value.runId !== undefined && typeof value.runId !== "string") {
    return false;
  }
  if (
    value.mobileSurface !== undefined &&
    value.mobileSurface !== "stage" &&
    value.mobileSurface !== "conversation" &&
    value.mobileSurface !== "terminal"
  ) {
    return false;
  }
  if (value.newDraft !== undefined) {
    if (!isPlainObject(value.newDraft)) {
      return false;
    }
    if (
      value.newDraft.sentMessageId !== undefined &&
      typeof value.newDraft.sentMessageId !== "string"
    ) {
      return false;
    }
  }
  return true;
}

// formatChangedDraftLines formats only the ambiguity answers and acceptance
// examples that differ from the saved Change Card, optionally excluding values
// already satisfied by an adopted revision.
function formatChangedDraftLines(
  ed: ActiveEditorState,
  adopted?: ChangeCardResult
): string[] {
  const rev = ed.card.request_revision;
  const ambiguities = Array.isArray(ed.card.ambiguities) ? ed.card.ambiguities : [];
  const savedAnswers = isPlainObject(ed.card.answers) ? ed.card.answers : {};
  const adoptedAnswers = adopted && isPlainObject(adopted.answers) ? adopted.answers : null;
  const draftAnswers = ed.draftAnswers || {};
  const questionByID = new Map<string, string>();
  const answerIDs: string[] = [];
  const seenIDs = new Set<string>();
  for (let i = 0; i < ambiguities.length; i++) {
    const amb = ambiguities[i];
    if (amb && typeof amb.id === "string") {
      if (typeof amb.question === "string") {
        questionByID.set(amb.id, amb.question);
      }
      if (!seenIDs.has(amb.id)) {
        seenIDs.add(amb.id);
        answerIDs.push(amb.id);
      }
    }
  }
  for (const k of Object.keys(savedAnswers).concat(Object.keys(draftAnswers))) {
    if (!seenIDs.has(k)) {
      seenIDs.add(k);
      answerIDs.push(k);
    }
  }

  const lines: string[] = [];
  for (let i = 0; i < answerIDs.length; i++) {
    const k = answerIDs[i];
    const savedVal = Object.prototype.hasOwnProperty.call(savedAnswers, k) ? savedAnswers[k] : "";
    const draftVal = Object.prototype.hasOwnProperty.call(draftAnswers, k) ? draftAnswers[k] : "";
    if (draftVal !== savedVal) {
      if (
        adoptedAnswers !== null &&
        Object.prototype.hasOwnProperty.call(adoptedAnswers, k) &&
        adoptedAnswers[k] === draftVal
      ) {
        continue;
      }
      const q = questionByID.get(k);
      lines.push("[" + rev + "] " + k + (q ? " (" + q + ")" : "") + "=" + draftVal);
    }
  }

  const savedExamples = Array.isArray(ed.card.examples) ? ed.card.examples : [];
  const adoptedExamples = adopted && Array.isArray(adopted.examples) ? adopted.examples : null;
  const draftExamples = Array.isArray(ed.draftExamples) ? ed.draftExamples : [];
  for (let i = 0; i < draftExamples.length; i++) {
    const ex = draftExamples[i];
    const savedEx = savedExamples.find((s) => s.id === ex.id);
    const differsFromSaved =
      !savedEx ||
      savedEx.scenario !== ex.scenario ||
      savedEx.input_summary !== ex.input_summary ||
      savedEx.expected_outcome !== ex.expected_outcome ||
      savedEx.verification_kind !== ex.verification_kind;
    if (!differsFromSaved) {
      continue;
    }
    if (adoptedExamples !== null) {
      const adoptedEx = adoptedExamples.find((a) => a.id === ex.id);
      if (
        adoptedEx &&
        adoptedEx.scenario === ex.scenario &&
        adoptedEx.input_summary === ex.input_summary &&
        adoptedEx.expected_outcome === ex.expected_outcome &&
        adoptedEx.verification_kind === ex.verification_kind
      ) {
        continue;
      }
    }
    lines.push(
      "[" +
        rev +
        "] " +
        ex.id +
        ": " +
        ex.scenario +
        " | " +
        ex.input_summary +
        " -> " +
        ex.expected_outcome +
        " [" +
        ex.verification_kind +
        "]"
    );
  }
  return lines;
}

// snapshotUnsavedDraft preserves unsaved local edits for one specific Change Card.
function snapshotUnsavedDraft(
  ed: ActiveEditorState,
  adopted?: ChangeCardResult
): PreservedDraft | null {
  const priorLines = ed.preservedDraft ? ed.preservedDraft.lines : [];
  const currentLines = formatChangedDraftLines(ed, adopted);
  if (currentLines.length === 0 && priorLines.length === 0) {
    return null;
  }
  return {
    fromRevision:
      currentLines.length > 0 ? ed.card.request_revision : ed.preservedDraft!.fromRevision,
    lines: priorLines.concat(currentLines),
  };
}

// nextPriorCard returns the immediately prior saved card when transitioning
// between distinct revisions of the same change_id, preserves the existing
// priorCard when refreshing the same revision, and returns null when
// mounting an initial card or switching to a different change_id.
function nextPriorCard(
  current: ActiveEditorState | null,
  next: ChangeCardResult
): ChangeCardResult | null {
  if (!current || current.card.change_id !== next.change_id) {
    return null;
  }
  if (current.card.request_revision === next.request_revision) {
    return current.priorCard;
  }
  return current.card;
}

// editorDraftFrom starts editable drafts from a saved card's own values, with
// no answers chosen yet for a waiting proposal.
function editorDraftFrom(card: ChangeCardResult): {
  draftAnswers: Record<string, string>;
  draftExamples: ExampleItem[];
  proposalAnswers: Record<string, string>;
  proposalError: string | null;
} {
  return {
    proposalAnswers: {},
    proposalError: null,
    draftAnswers: isPlainObject(card.answers) ? { ...card.answers } : {},
    draftExamples: card.examples.map((ex) => ({
      id: ex.id,
      scenario: ex.scenario,
      input_summary: ex.input_summary,
      expected_outcome: ex.expected_outcome,
      verification_kind: ex.verification_kind,
    })),
  };
}

// keepProposalAnswers keeps the user's choices that still fit next: same
// question ID, same question, and the option still offered. A re-read of the
// same proposal therefore keeps every choice.
function keepProposalAnswers(
  answers: Record<string, string>,
  previous: ProposedRevisionItem | undefined,
  next: ProposedRevisionItem | undefined
): Record<string, string> {
  if (previous === undefined || next === undefined) {
    return {};
  }
  const before = new Map((previous.ambiguities ?? []).map((q) => [q.id, q.question]));
  const kept: Record<string, string> = {};
  for (const question of next.ambiguities ?? []) {
    const chosen = answers[question.id];
    if (
      chosen !== undefined &&
      before.get(question.id) === question.question &&
      question.options.includes(chosen)
    ) {
      kept[question.id] = chosen;
    }
  }
  return kept;
}

// readLinkFromFragment returns the one-use link carried in the URL fragment.
function readLinkFromFragment(hash: string): string | null {
  const params = new URLSearchParams(hash.startsWith("#") ? hash.slice(1) : hash);
  const link = params.get("link");
  return link !== null && link !== "" ? link : null;
}

// validateVerificationListPayload verifies change_id correlation on list_verifications.
function validateVerificationListPayload(
  payload: VerificationListResult,
  expectedChangeId: string
): string | null {
  if (!payload.change_id || payload.change_id.trim().length === 0) {
    return "The server returned a verification list without a change_id.";
  }
  if (payload.change_id !== expectedChangeId) {
    return (
      "The server returned verification runs for Change Card " +
      payload.change_id +
      " instead of " +
      expectedChangeId +
      "."
    );
  }
  for (let i = 0; i < payload.runs.length; i++) {
    if (payload.runs[i].change_id !== payload.change_id) {
      return (
        "The server returned a verification run for Change Card " +
        payload.runs[i].change_id +
        " inside the list for " +
        payload.change_id +
        "."
      );
    }
  }
  return null;
}

// liveTargetKey names a live target read's case, commit, start and end.
function liveTargetKey(target: LiveTargetStatus): string {
  const live = target.live_target;
  switch (live.state) {
    case "none":
      return "none";
    case "starting":
      return "starting " + live.candidate_commit + " " + live.since;
    case "running":
      return "running " + live.candidate_commit + " " + live.started_at;
    case "ended":
      return "ended " + live.candidate_commit + " " + live.started_at + " " + live.ended_at;
    case "not_confirmed_stopped":
      return "not_confirmed_stopped " + live.candidate_commit;
  }
}

// stripSeededBoilerplate removes action-seeded boilerplate when switching
// recipients, preserving any additional text typed by the user.
function stripSeededBoilerplate(text: string, seeded: string | undefined): string {
  if (seeded === undefined || seeded === "") {
    return text;
  }
  if (text === seeded) {
    return "";
  }
  if (text.endsWith("\n\n" + seeded)) {
    return text.slice(0, text.length - ("\n\n" + seeded).length);
  }
  if (text.startsWith(seeded + "\n\n")) {
    return text.slice((seeded + "\n\n").length);
  }
  return text;
}

// isNarrowViewport reports whether the viewport is below the 1080px 2-column desktop breakpoint.
function isNarrowViewport(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(max-width: 1079px)").matches
  );
}

// bootstrapViewsPage mounts the Highbay Studio views page, exchanges or restores
// the origin-scoped session, and drives reactive updates.
export function bootstrapViewsPage() {
  const rawRootEl = document.getElementById("highbay-checkpoint-root");
  const rawSkipEl = document.getElementById("highbay-skip-link");
  const rawStatusEl = document.getElementById("highbay-checkpoint-status");
  const rawNavEl = document.getElementById("highbay-checkpoint-nav");
  const rawNoticesEl = document.getElementById("highbay-checkpoint-notices");
  const rawColumnsEl = document.getElementById("highbay-workspace-columns");
  const rawViewEl = document.getElementById("highbay-checkpoint-view");
  const rawTerminalEl = document.getElementById("highbay-terminal-region");
  const rawConversationEl = document.getElementById("highbay-conversation-rail");
  const rawTraceEl = document.getElementById("highbay-trace-region");

  if (
    !rawRootEl ||
    !rawStatusEl ||
    !rawNavEl ||
    !rawNoticesEl ||
    !rawColumnsEl ||
    !rawViewEl ||
    !rawTerminalEl ||
    !rawConversationEl ||
    !rawTraceEl
  ) {
    return;
  }

  const rootEl: HTMLElement = rawRootEl;
  const skipEl: HTMLAnchorElement | null = rawSkipEl instanceof HTMLAnchorElement ? rawSkipEl : null;
  const statusEl: HTMLElement = rawStatusEl;
  const navEl: HTMLElement = rawNavEl;
  const noticesEl: HTMLElement = rawNoticesEl;
  const columnsEl: HTMLElement = rawColumnsEl;
  const viewEl: HTMLElement = rawViewEl;
  const terminalRegionEl: HTMLElement = rawTerminalEl;
  const conversationRegionEl: HTMLElement = rawConversationEl;
  const traceRegionEl: HTMLElement = rawTraceEl;

  const navRoot: Root = createRoot(navEl);
  const noticeRoot: Root = createRoot(noticesEl);
  const viewRoot: Root = createRoot(viewEl);
  const terminalRoot: Root = createRoot(terminalRegionEl);
  const conversationRoot: Root = createRoot(conversationRegionEl);
  const traceRoot: Root = createRoot(traceRegionEl);

  const SESSION_STORAGE_KEY = "highbay.session";
  let session: string | null = null;
  let projectName: string | null = null;
  let sessionState: SessionState = "starting";
  let signedOutReason: SignedOutReason = "no-link";
  let signedOutMessage = "";
  const pendingCalls = new Set<PendingCall>();

  // Re-entrancy guard around flushSync so effects triggered on mount never
  // invoke nested flushSync inside a React lifecycle commit.
  let syncingRegions = false;
  let syncAgain = false;

  let currentView: WorkspaceView = { kind: "changes" };
  let activeChangeId: string | null = null;
  let navigationSeq = 0;
  let handlingPopState = false;
  let restoringInitialHistory: HistoryViewState | null = null;
  let mobileSurface: MobileSurface = "stage";
  let mobileSurfaceInvoker: HTMLElement | null = null;
  let terminalReturnSurface: MobileSurface = "stage";

  let changeList: ChangeList | null = null;
  let listError: string | null = null;
  let listRequest: Promise<boolean> | null = null;
  let waitController: AbortController | null = null;
  let waitLoopRunning = false;
  let waitTroubleShown = false;

  // Per-change isolated CP1 editors and strict request/generation tracking.
  const editorsByChangeId = new Map<string, ActiveEditorState>();
  const editorRequestsByChangeId = new Map<string, OperationName>();
  const editorGenerationByChangeId = new Map<string, number>();
  const cardRefreshInFlight = new Map<string, Promise<void>>();
  const cardRefreshAgain = new Set<string>();
  const cardRefreshAfterEditor = new Set<string>();
  const seenCardRevisionsByChangeId = new Map<string, Set<string>>();
  const knownAmbiguitiesByRevision = new Map<string, readonly AmbiguityQuestion[]>();
  const pendingOpenByChangeId = new Map<
    string,
    {
      openSeq: number;
      explicitStage?: ChangeStageName;
      historyMode: "push" | "replace";
      mobileSurface: MobileSurface;
    }
  >();
  const pendingDefaultVerifyCheck = new Map<string, number>();

  let traceOpen = false;
  let traceFilter: TraceFilter = "error";
  let tracePage: TraceList | null = null;
  let traceError: string | null = null;

  let previewState: ArchitecturePreviewResult | null = null;
  const previewErrors = new Map<string, string>();

  let storyEvaluation: StoryRefreshResult | null = null;
  let storyEvaluationError: string | null = null;
  let storyEvaluationStale = true;
  let storyReadAgain = false;

  let liveTarget: LiveTargetStatus | null = null;
  let liveTargetError: string | null = null;
  let liveTargetStale = true;
  let liveReadAgain = false;
  let liveStopOutcome: ServerAnswer | null = null;
  let liveStopBasis: string | null = null;
  let liveStopAfterRead = 0;
  let liveReadCount = 0;

  let modelConnection: ModelConnection | null = null;
  let modelConnectionError: string | null = null;
  let modelApplyOutcome: ServerAnswer | null = null;
  let modelDraft: ModelDraft = { kind: null, provider: "anthropic", modelName: "", gatewayAddr: "" };
  let modelDraftDirty = false;
  let modelDraftReset = 0;

  let pendingLinkFragment: string | null = null;

  let teamSnapshot: TeamSnapshot | null = null;
  let teamReadError: TeamFactsError | null = null;
  let teamReadAgain = false;
  let teamStartReceipt: TeamStageProps["startReceipt"] = null;
  let teamActionError: string | null = null;

  let agentOptions: Operations["list_team_agent_options"]["result"] | null = null;
  let agentOptionsError: string | null = null;
  let workerChoices: WorkerChoices = {};
  const editedWorkerChoices = new Set<WorkerRole>();
  let guideChoice: RoleAgentChoice | undefined = undefined;
  let guideChoiceEdited = false;

  // Chorus conversation dock, unread tracking, and + New change receipt state.
  let conversationOpen = true;
  let userCollapsedConversation = false;
  let seenMessageCount = 0;
  let teamConversation: TeamConversation | null = null;
  let conversationError: string | null = null;
  let conversationReadAgain = false;
  let composerDraft: ComposerDraft = { to: "guide", text: "", replyTo: null, context: null };
  let activeComposerScope = "draft";
  const composerDraftsByScope = new Map<string, ComposerDraft>();
  let composerSendError: string | null = null;
  let composerDetachedNotice: string | null = null;

  let describeText = "";
  let describeSendError: string | null = null;
  let describeSendUncertain = false;
  let draftSentMessageId: string | undefined = undefined;

  // Stage-docked terminal state.
  let openTerminalRole: TeamRoleState["role"] | null = null;
  let terminalObserveMode = true;
  let terminalSize: TerminalDrawerSize = "default";
  let terminalInvoker: HTMLElement | null = null;
  let terminalActionError: string | null = null;
  const terminalRestartSeq: Record<TeamRoleState["role"], number> = {
    guide: 0,
    builder: 0,
    test_writer: 0,
  };

  // Server release update and UI bundle build tracking.
  let initialUiBuild: string | null = null;
  let uiBuildChanged = false;
  let uiBuildProbeInFlight = false;
  let uiBuildProbeAgain = false;
  let updateStatus: UpdateStatusResult | null = null;
  let updateProgress: UpdateProgress | null = null;

  type TeamNotice = TeamSnapshot["dismissed_notices"][number];
  const noticeDismissed = new Set<TeamNotice>();
  const noticeErrors = new Map<TeamNotice, string>();

  let mergeOutcome: {
    identity: string;
    receipt: MergeControls["receipt"];
    error: MergeControls["error"];
  } | null = null;

  let buildChangeId: string | null = null;
  let buildSnapshot: ChangeBuild["build"] | null = null;
  let buildDraftedBeforeDrop: boolean | null = null;
  let buildReadError: string | null = null;
  let buildStartError: string | null = null;
  let buildReadAgain = false;
  let buildGeneration = 0;
  let buildStartReceipt: BuildStartReceipt | null = null;
  let openBuildRefusal: OpenBuildRefusal | null = null;
  const buildReadWaiters = new Map<string, Array<(build: ChangeBuild | null) => void>>();

  let buildFindings: TestFindings | null = null;
  let findingsBasis: string | null = null;
  let findingsError: string | null = null;
  let findingsReadAgain = false;

  const runsById = new Map<string, VerificationStatusResult>();
  let verificationGeneration = 0;
  let verificationReadOrder = 0;
  const runObservationGeneration = new Map<string, number>();
  const runObservationOrder = new Map<string, number>();
  const runReadAgain = new Set<string>();
  const reviewsByRunId = new Map<
    string,
    { generation: number; order: number; review: VerificationReview }
  >();
  const lastLoadedListByChangeId = new Map<string, string[]>();
  let selectedVerificationChangeId: string | null = null;
  let selectedRunId: string | null = null;
  const pendingRestoredRunByChangeId = new Map<string, string>();
  const selectionTouchedDuringList = new Map<string, boolean>();
  const runRequestErrors = new Map<string, string>();
  const listRequestErrors = new Map<string, string>();
  const cardLoadErrors = new Map<string, string>();
  const approveErrors = new Map<string, string>();
  let approvalUnavailable: string | null = null;
  let lastOpenAttempt: string | null = null;

  // recordObservedCard records seen revisions and question definitions for card
  // so stale-context guards reconcile by question identity and detect unseen revisions.
  function recordObservedCard(card: ChangeCardResult) {
    let seen = seenCardRevisionsByChangeId.get(card.change_id);
    if (seen === undefined) {
      seen = new Set<string>();
      seenCardRevisionsByChangeId.set(card.change_id, seen);
    }
    seen.add(card.request_revision);
    knownAmbiguitiesByRevision.set(
      card.change_id + ":" + card.request_revision,
      card.ambiguities ?? []
    );
    if (card.proposed_revision !== undefined) {
      seen.add(card.proposed_revision.request_revision);
      knownAmbiguitiesByRevision.set(
        card.change_id + ":" + card.proposed_revision.request_revision,
        card.proposed_revision.ambiguities ?? []
      );
    }
  }

  // composerScopeFor isolates per-change composer drafts from + New change / general drafts.
  function composerScopeFor(viewKind: WorkspaceView["kind"], changeId: string | null): string {
    if (
      changeId !== null &&
      (viewKind === "change" || viewKind === "story" || viewKind === "live")
    ) {
      return "change:" + changeId;
    }
    return "draft";
  }

  // switchComposerScope saves the current scope's unsent composer draft and restores nextScope's.
  function switchComposerScope(nextScope: string) {
    if (nextScope === activeComposerScope) {
      return;
    }
    composerDraftsByScope.set(activeComposerScope, composerDraft);
    activeComposerScope = nextScope;
    composerDraft = composerDraftsByScope.get(nextScope) ?? {
      to: "guide",
      text: "",
      replyTo: null,
      context: null,
    };
    composerDetachedNotice = null;
    composerSendError = null;
  }

  // isFocusableVisible reports whether el is attached and neither hidden nor inert.
  function isFocusableVisible(el: HTMLElement | null): el is HTMLElement {
    if (el === null || !document.contains(el)) {
      return false;
    }
    if (el.closest("[hidden]") !== null || el.closest("[inert]") !== null) {
      return false;
    }
    if (isNarrowViewport() && el.getClientRects().length === 0) {
      return false;
    }
    return true;
  }

  // getActiveEditor returns the isolated editor state for activeChangeId, if any.
  function getActiveEditor(): ActiveEditorState | null {
    return activeChangeId !== null ? editorsByChangeId.get(activeChangeId) ?? null : null;
  }

  // getEditorRequest returns the active Change Card RPC for changeId, or null.
  function getEditorRequest(changeId: string | null): OperationName | null {
    return changeId !== null ? editorRequestsByChangeId.get(changeId) ?? null : null;
  }

  // bumpEditorGeneration advances changeId's request generation so older reads are ignored.
  function bumpEditorGeneration(changeId: string): number {
    const next = (editorGenerationByChangeId.get(changeId) ?? 0) + 1;
    editorGenerationByChangeId.set(changeId, next);
    return next;
  }

  // isEditorDirtyState reports whether ed holds unsaved answers, examples, or preserved lines.
  function isEditorDirtyState(ed: ActiveEditorState): boolean {
    return isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples) || ed.preservedDraft !== null;
  }

  // currentHistoryState derives opaque IDs for window.history.state on /views.
  function currentHistoryState(): HistoryViewState {
    const base: HistoryViewState = {
      view: currentView.kind,
      ...(mobileSurface !== "stage" ? { mobileSurface } : {}),
      ...(draftSentMessageId !== undefined ? { newDraft: { sentMessageId: draftSentMessageId } } : {}),
    };
    if (currentView.kind === "change") {
      base.changeId = currentView.changeId;
      base.stage = currentView.stage;
      if ((currentView.stage === "verify" || currentView.stage === "approve") && selectedRunId !== null) {
        base.runId = selectedRunId;
      }
    } else if (activeChangeId !== null && (currentView.kind === "story" || currentView.kind === "live")) {
      base.changeId = activeChangeId;
    }
    return base;
  }

  // syncHistoryState updates window.history.state on exact /views without mutating pathname/search/hash.
  function syncHistoryState(mode: "push" | "replace") {
    if (
      handlingPopState ||
      sessionState !== "active" ||
      readLinkFromFragment(window.location.hash) !== null
    ) {
      return;
    }
    const next = currentHistoryState();
    const prev = window.history.state;
    if (isHistoryViewState(prev) && JSON.stringify(prev) === JSON.stringify(next) && window.location.hash === "") {
      return;
    }
    const targetUrl = window.location.pathname + window.location.search;
    if (mode === "push") {
      window.history.pushState(next, "", targetUrl);
    } else {
      window.history.replaceState(next, "", targetUrl);
    }
  }

  // send encodes one request with the generated route, sends it to the page's
  // own address with the session, and validates the answer with the generated
  // result validator. It is the page's only HTTP network call.
  async function send<K extends OperationName>(
    op: K,
    request: Operations[K]["request"],
    signal?: AbortSignal
  ): Promise<Outcome<Operations[K]["result"]>> {
    const route = ROUTES[op];
    const encoded = route.encode(request);
    const headers: Record<string, string> = { Accept: "application/json" };
    if (encoded.body !== undefined) {
      headers["Content-Type"] = "application/json";
    }
    const sentSession = session;
    if (sentSession !== null) {
      headers.Authorization = "Bearer " + sentSession;
    }
    let status = 0;
    let text = "";
    try {
      const response = await fetch(encoded.path, {
        method: route.method,
        headers,
        body: encoded.body,
        signal,
        cache: "no-store",
        credentials: "omit",
        redirect: "error",
      });
      status = response.status;
      text = await response.text();
    } catch {
      if (signal?.aborted) {
        return { ok: false, kind: "aborted", status: 0, message: "The request was stopped." };
      }
      return {
        ok: false,
        kind: "network",
        status: 0,
        message: "The Highbay server did not answer. Check that highbay is still running.",
      };
    }
    let parsed: unknown = undefined;
    try {
      parsed = text === "" ? undefined : JSON.parse(text);
    } catch {
      parsed = undefined;
    }
    if (status === route.success) {
      if ((text === "" || parsed !== undefined) && route.decode(parsed)) {
        return { ok: true, result: parsed };
      }
      return {
        ok: false,
        kind: "server",
        status,
        message: "The server's answer to " + op + " did not match its declared result.",
      };
    }
    const body = route.decodeFailure(status, parsed) ? parsed : undefined;
    if (status === 401) {
      clearStoredSession(sentSession);
    }
    return {
      ok: false,
      kind: status === 401 ? "session" : "server",
      status,
      message:
        body !== undefined ? body.message : "The server refused the request (HTTP " + status + ").",
      body,
    };
  }

  // hasPending reports whether a request of op about subject is in flight.
  function hasPending(op: OperationName, subject: string): boolean {
    for (const call of pendingCalls) {
      if (call.op === op && call.subject === subject) {
        return true;
      }
    }
    return false;
  }

  // callServer sends one request whose waiting state views derive from pendingCalls.
  function callServer<K extends OperationName>(
    op: K,
    request: Operations[K]["request"],
    subject: string,
    onSuccess: (result: Operations[K]["result"]) => void,
    onError: (message: string, failure: Failure) => void
  ) {
    if (session === null) {
      onError(sessionEndedMessage(), {
        kind: "session",
        status: 0,
        message: sessionEndedMessage(),
      });
      syncAllRegions();
      return;
    }
    const call: PendingCall = { op, subject };
    pendingCalls.add(call);
    syncAllRegions();
    void send(op, request).then((outcome) => {
      pendingCalls.delete(call);
      if (outcome.ok) {
        onSuccess(outcome.result);
      } else {
        if (outcome.kind === "session") {
          endSession();
        }
        onError(outcome.message, outcome);
      }
      syncAllRegions();
    });
  }

  // callEditorServer owns the editable form for one specific Change Card;
  // late responses whose generation was superseded are ignored.
  function callEditorServer<K extends OperationName>(
    op: K,
    request: Operations[K]["request"],
    changeId: string,
    onSuccess: (result: Operations[K]["result"]) => void,
    onError: (message: string, failure: Failure) => void
  ) {
    const reqGen = bumpEditorGeneration(changeId);
    editorRequestsByChangeId.set(changeId, op);
    const readDeferredCard = () => {
      if (cardRefreshAfterEditor.delete(changeId)) {
        refreshOpenCard(changeId);
      }
    };
    callServer(
      op,
      request,
      changeId,
      (result) => {
        if (editorRequestsByChangeId.get(changeId) === op) {
          editorRequestsByChangeId.delete(changeId);
        }
        if ((editorGenerationByChangeId.get(changeId) ?? 0) === reqGen) {
          onSuccess(result);
        }
        readDeferredCard();
      },
      (message, failure) => {
        if (editorRequestsByChangeId.get(changeId) === op) {
          editorRequestsByChangeId.delete(changeId);
        }
        if ((editorGenerationByChangeId.get(changeId) ?? 0) === reqGen) {
          onError(message, failure);
        }
        readDeferredCard();
      }
    );
  }

  function sessionEndedMessage(): string {
    return "This page’s Highbay session has ended. Run highbay in your project folder or invoke /highbay in your coding agent to open Highbay views.";
  }

  function saveStoredSession(token: string) {
    try {
      window.localStorage.setItem(SESSION_STORAGE_KEY, token);
    } catch {
      // Storage-restricted browser profiles still retain the in-memory session.
    }
  }

  function readStoredSession(): string | null {
    try {
      const stored = window.localStorage.getItem(SESSION_STORAGE_KEY);
      return stored !== null && stored !== "" ? stored : null;
    } catch {
      return null;
    }
  }

  function clearStoredSession(rejectedToken: string | null) {
    if (rejectedToken === null) {
      return;
    }
    try {
      if (window.localStorage.getItem(SESSION_STORAGE_KEY) === rejectedToken) {
        window.localStorage.removeItem(SESSION_STORAGE_KEY);
      }
    } catch {
      // Ignore storage access errors on teardown.
    }
  }

  // start exchanges #link=<token> or validates the stored origin-scoped session.
  async function start() {
    const initialHistory = isHistoryViewState(window.history.state) ? window.history.state : null;
    const link = readLinkFromFragment(window.location.hash);
    if (window.location.hash !== "") {
      window.history.replaceState(
        link !== null ? null : initialHistory,
        "",
        window.location.pathname + window.location.search
      );
    }
    if (link !== null) {
      statusEl.textContent = "Opening your Highbay session...";
      const outcome = await send("exchange_browser_link", { link });
      if (!outcome.ok) {
        signOut(outcome.status === 422 ? "link-used" : "link-failed", outcome.message);
        return;
      }
      session = outcome.result.session;
      projectName = outcome.result.project_name;
      initialUiBuild = outcome.result.ui_build;
      saveStoredSession(session);
      maybeCheckUpdateStatus(outcome.result.version);
    } else {
      const stored = readStoredSession();
      if (stored === null) {
        signOut("no-link", "");
        return;
      }
      statusEl.textContent = "Restoring your Highbay session...";
      session = stored;
      const outcome = await send("get_browser_session", {});
      if (!outcome.ok) {
        session = null;
        if (outcome.kind === "session") {
          clearStoredSession(stored);
          signOut("no-link", "");
        } else {
          signOut("link-failed", outcome.message);
        }
        return;
      }
      session = outcome.result.session;
      projectName = outcome.result.project_name;
      initialUiBuild = outcome.result.ui_build;
      maybeCheckUpdateStatus(outcome.result.version);
    }
    sessionState = "active";
    rootEl.dataset.session = "active";
    currentView = { kind: "changes" };
    restoringInitialHistory = link === null ? initialHistory : null;
    readConversation();
    const loaded = await loadChanges();
    const historyToRestore = restoringInitialHistory;
    restoringInitialHistory = null;
    if (loaded && historyToRestore !== null) {
      applyHistoryViewState(historyToRestore);
    } else {
      syncHistoryState("replace");
    }
  }

  function signOut(reason: SignedOutReason, message: string) {
    sessionState = "signed-out";
    signedOutReason = reason;
    signedOutMessage = message;
    rootEl.dataset.session = "none";
    if (window.location.hash === "") {
      window.history.replaceState(null, "", window.location.pathname + window.location.search);
    }
    statusEl.textContent =
      reason === "link-used"
        ? "This link was already used. Run highbay in your project folder or invoke /highbay in your coding agent."
        : "Run highbay in your project folder or invoke /highbay in your coding agent to open Highbay views.";
    syncAllRegions();
  }

  function isReleaseBuildVersion(version: string): boolean {
    return /^v?\d+\.\d+\.\d+$/.test(version);
  }

  function maybeCheckUpdateStatus(currentVersion: string) {
    if (!isReleaseBuildVersion(currentVersion)) {
      return;
    }
    void (async () => {
      const outcome = await send("get_update_status", {});
      if (!outcome.ok) {
        if (outcome.kind === "session") {
          endSession();
          syncAllRegions();
        }
        return;
      }
      updateStatus = outcome.result.update_available ? outcome.result : null;
      syncNoticeRegion();
    })();
  }

  function normalizeVersionTag(version: string): string {
    return version.startsWith("v") ? version : "v" + version;
  }

  function probeUiBuildAfterReconnect() {
    if (session === null || sessionState !== "active" || uiBuildChanged) {
      return;
    }
    if (uiBuildProbeInFlight) {
      uiBuildProbeAgain = true;
      return;
    }
    uiBuildProbeInFlight = true;
    uiBuildProbeAgain = false;
    void (async () => {
      try {
        const outcome = await send("get_browser_session", {});
        if (!outcome.ok) {
          if (outcome.kind === "session") {
            endSession();
            syncAllRegions();
          }
          return;
        }
        let changed = false;
        if (initialUiBuild !== null && outcome.result.ui_build !== initialUiBuild) {
          uiBuildChanged = true;
          changed = true;
        }
        if (
          updateStatus !== null &&
          normalizeVersionTag(outcome.result.version) ===
            normalizeVersionTag(updateStatus.latest_version)
        ) {
          updateStatus = null;
          changed = true;
        }
        if (changed) {
          syncNoticeRegion();
        }
      } finally {
        uiBuildProbeInFlight = false;
        if (uiBuildProbeAgain && session !== null && sessionState === "active" && !uiBuildChanged) {
          probeUiBuildAfterReconnect();
        }
      }
    })();
  }

  async function waitForUpdatedServer(targetVersion: string) {
    const normalizedTarget = targetVersion.startsWith("v") ? targetVersion : "v" + targetVersion;
    updateProgress = { kind: "restarting", targetVersion: normalizedTarget };
    syncNoticeRegion();
    const controller = new AbortController();
    const timer = window.setTimeout(() => controller.abort(), 30000);
    try {
      while (!controller.signal.aborted && session !== null) {
        const outcome = await send("get_browser_session", {}, controller.signal);
        if (outcome.ok) {
          const observed = outcome.result.version.startsWith("v")
            ? outcome.result.version
            : "v" + outcome.result.version;
          if (observed === normalizedTarget) {
            if (initialUiBuild !== null && outcome.result.ui_build !== initialUiBuild) {
              uiBuildChanged = true;
            }
            updateStatus = null;
            updateProgress = { kind: "ready", version: normalizedTarget };
            syncNoticeRegion();
            void loadChanges();
            return;
          }
        } else if (outcome.kind === "session") {
          endSession();
          syncAllRegions();
          return;
        }
        if (!controller.signal.aborted) {
          await new Promise<void>((resolve) => window.setTimeout(resolve, 250));
        }
      }
      if (session !== null) {
        updateProgress = {
          kind: "restart_failed",
          targetVersion: normalizedTarget,
          message:
            "Highbay " +
            normalizedTarget +
            " has not finished restarting yet. Check highbay status, then check again.",
        };
        syncNoticeRegion();
      }
    } finally {
      window.clearTimeout(timer);
    }
  }

  function applyServerUpdate() {
    if (sessionState !== "active" || session === null) {
      return;
    }
    const target =
      updateProgress?.kind === "error"
        ? updateProgress.targetVersion
        : updateStatus?.latest_version;
    if (target === undefined || target === "") {
      return;
    }
    if (updateProgress?.kind === "installing" || updateProgress?.kind === "restarting") {
      return;
    }
    updateProgress = { kind: "installing", targetVersion: target };
    syncNoticeRegion();
    void (async () => {
      const outcome = await send("apply_update", { version: target });
      if (!outcome.ok) {
        if (outcome.kind === "session") {
          endSession();
          syncAllRegions();
          return;
        }
        if (outcome.status === 409) {
          updateProgress = {
            kind: "conflict",
            message: outcome.message + " Reload when ready to check the current version.",
          };
        } else {
          updateProgress = {
            kind: "error",
            targetVersion: target,
            message: "Could not complete the Highbay update: " + outcome.message,
          };
        }
        syncNoticeRegion();
        return;
      }
      if (outcome.result.status === "up_to_date") {
        updateStatus = null;
        updateProgress = { kind: "ready", version: outcome.result.version };
        syncNoticeRegion();
        return;
      }
      await waitForUpdatedServer(outcome.result.version);
    })();
  }

  function endSession() {
    if (sessionState !== "active") {
      return;
    }
    const expiredSession = session;
    session = null;
    clearStoredSession(expiredSession);
    sessionState = "ended";
    openTerminalRole = null;
    if (mobileSurface === "terminal") {
      mobileSurface = "stage";
    }
    buildStartReceipt = null;
    buildStartError = null;
    openBuildRefusal = null;
    for (const waiters of buildReadWaiters.values()) {
      for (const ready of waiters) {
        ready(null);
      }
    }
    buildReadWaiters.clear();
    teamStartReceipt = null;
    buildGeneration++;
    rootEl.dataset.session = "ended";
    if (waitController !== null) {
      waitController.abort();
    }
    statusEl.textContent = sessionEndedMessage();
    syncAllRegions();
  }

  // reconcileConversationDefaultOpen collapses Conversation only on idle Moment 0
  // unless the user explicitly collapsed it.
  function reconcileConversationDefaultOpen() {
    if (changeList === null || teamSnapshot === null || teamConversation === null) {
      return;
    }
    const isIdleMomentZero =
      changeList.changes.length === 0 &&
      changeList.initializing_phase === undefined &&
      changeList.draft_state === undefined &&
      connectedGuide() === undefined &&
      teamConversation.messages.length === 0 &&
      !teamSnapshot.roles.some((role) => role.state === "needs_you") &&
      currentView.kind === "changes";
    if (isIdleMomentZero) {
      conversationOpen = false;
    } else if (!userCollapsedConversation) {
      conversationOpen = true;
    }
  }

  function loadChanges(): Promise<boolean> {
    if (listRequest !== null) {
      return listRequest;
    }
    const pending = (async () => {
      const call: PendingCall = { op: "list_changes", subject: "" };
      pendingCalls.add(call);
      syncAllRegions();
      const outcome = await send("list_changes", {});
      pendingCalls.delete(call);
      if (!outcome.ok) {
        if (outcome.kind === "session") {
          endSession();
        }
        listError = outcome.message;
        if (currentView.kind === "changes") {
          statusEl.textContent = "Could not read this project's changes.";
        }
        syncAllRegions();
        return false;
      }
      const previous = changeList;
      changeList = outcome.result;
      listError = null;
      if (previous === null || previous.version !== outcome.result.version) {
        verificationGeneration++;
      }
      applyListChanges(previous, outcome.result);
      if (previous === null || previous.version !== outcome.result.version) {
        readTeam();
        readConversation();
        const activeEd = getActiveEditor();
        if (activeEd !== null) {
          readBuild(activeEd.card.change_id);
          refreshVerificationObservations(activeEd.card.change_id);
        }
        if (currentView.kind === "story" || currentView.kind === "new") {
          readStoryEvaluation();
        } else {
          storyEvaluationStale = true;
        }
        if (currentView.kind === "live") {
          readLiveTarget();
        } else {
          liveTargetStale = true;
        }
      }
      reconcileConversationDefaultOpen();
      syncAllRegions();
      startWaiting();
      return true;
    })();
    listRequest = pending;
    void pending.finally(() => {
      listRequest = null;
    });
    return pending;
  }

  // loadCardIntoCache loads changeId into editorsByChangeId without altering currentView.
  function loadCardIntoCache(changeId: string) {
    if (
      !changeId ||
      editorsByChangeId.has(changeId) ||
      editorRequestsByChangeId.has(changeId) ||
      cardRefreshInFlight.has(changeId)
    ) {
      return;
    }
    callEditorServer(
      "get_change",
      { change_id: changeId },
      changeId,
      (loadedCard) => {
        if (loadedCard.change_id === changeId && !editorsByChangeId.has(changeId)) {
          recordObservedCard(loadedCard);
          editorsByChangeId.set(changeId, {
            card: loadedCard,
            priorCard: null,
            incomingCard: null,
            ...editorDraftFrom(loadedCard),
            preservedDraft: null,
            formError: null,
          });
          if (activeChangeId === null) {
            activeChangeId = changeId;
          }
          syncAllRegions();
        }
      },
      () => {}
    );
  }

  // applyListChanges auto-opens the sole Change Card on 0 -> 1 transitions when
  // the user has no unsaved input, and refreshes any retained editors whose
  // saved revision or verification moved.
  function applyListChanges(previous: ChangeList | null, next: ChangeList) {
    if (
      editorsByChangeId.size === 0 &&
      activeChangeId === null &&
      next.changes.length === 1
    ) {
      const onlyId = next.changes[0].change_id;
      if (
        !hasUnsavedPageInput() &&
        ((previous === null &&
          restoringInitialHistory === null &&
          currentView.kind === "changes" &&
          navigationSeq === 0) ||
          (previous !== null &&
            previous.changes.length === 0 &&
            (currentView.kind === "changes" || currentView.kind === "new")))
      ) {
        draftSentMessageId = undefined;
        describeSendUncertain = false;
        describeSendError = null;
        openChange(onlyId, undefined, {
          historyMode: previous === null ? "replace" : "push",
        });
        return;
      }
      if (previous === null && restoringInitialHistory?.view !== "change") {
        loadCardIntoCache(onlyId);
      }
    }
    const initializing = next.initializing_phase !== undefined;
    const wasInitializing = previous?.initializing_phase !== undefined;
    const drafting = next.draft_state !== undefined;
    const wasDrafting = previous?.draft_state !== undefined;
    if (
      currentView.kind === "changes" &&
      (previous === null || initializing !== wasInitializing || drafting !== wasDrafting)
    ) {
      statusEl.textContent = initializing
        ? "Preparing the starter project."
        : drafting
        ? "Drafting the project's first Change Card."
        : next.changes.length === 0
        ? "This project has no changes yet."
        : "Showing this project's " + next.changes.length + " changes.";
    }
    if (previous === null) {
      return;
    }
    for (const changeId of editorsByChangeId.keys()) {
      const before = previous.changes.find((item) => item.change_id === changeId);
      const after = next.changes.find((item) => item.change_id === changeId);
      if (!after) {
        continue;
      }
      if (
        !before ||
        before.request_revision !== after.request_revision ||
        before.proposed_revision !== after.proposed_revision ||
        before.saved_at !== after.saved_at
      ) {
        refreshOpenCard(changeId);
      }
      const runBefore = before?.latest_verification;
      const runAfter = after.latest_verification;
      if (
        changeId === activeChangeId &&
        lastLoadedListByChangeId.has(changeId) &&
        (runBefore?.run_id !== runAfter?.run_id || runBefore?.state !== runAfter?.state)
      ) {
        refreshVerificationsInBackground(changeId);
      }
    }
  }

  function startWaiting() {
    if (waitLoopRunning || session === null || changeList === null || document.hidden) {
      return;
    }
    waitLoopRunning = true;
    void waitLoop().finally(() => {
      waitLoopRunning = false;
    });
  }

  async function waitLoop() {
    let failedRounds = 0;
    while (session !== null && changeList !== null && !document.hidden) {
      const controller = new AbortController();
      waitController = controller;
      const since = changeList.version;
      const outcome = await send("wait_for_changes", { since }, controller.signal);
      waitController = null;
      let trouble: string;
      if (outcome.ok) {
        const changed = changeList !== null && outcome.result.version !== changeList.version;
        if (!changed || (await loadChanges())) {
          failedRounds = 0;
          if (waitTroubleShown) {
            waitTroubleShown = false;
            statusEl.textContent = "This page is up to date with the Highbay server again.";
            syncAllRegions();
          }
          probeUiBuildAfterReconnect();
          continue;
        }
        if (session === null) {
          return;
        }
        trouble =
          "Could not read this project's changes; trying again. Your edits stay on this page. " +
          (listError ?? "");
      } else if (outcome.kind === "aborted") {
        continue;
      } else if (outcome.kind === "session") {
        endSession();
        return;
      } else {
        trouble =
          "Lost contact with the Highbay server; trying again. Your edits stay on this page. " +
          outcome.message;
      }
      if (!waitTroubleShown) {
        waitTroubleShown = true;
        statusEl.textContent = trouble;
        syncAllRegions();
      }
      failedRounds += 1;
      const delay = Math.min(WAIT_RETRY_MS * 2 ** (failedRounds - 1), WAIT_RETRY_MAX_MS);
      await new Promise<void>((resume) => window.setTimeout(resume, delay));
    }
  }

  // refreshOpenCard refreshes changeId's Change Card in the background without
  // clobbering an in-flight write or superseding a newer generation.
  function refreshOpenCard(changeId: string) {
    if (editorRequestsByChangeId.has(changeId)) {
      cardRefreshAfterEditor.add(changeId);
      return;
    }
    if (cardRefreshInFlight.has(changeId)) {
      cardRefreshAgain.add(changeId);
      return;
    }
    const task = (async () => {
      do {
        cardRefreshAgain.delete(changeId);
        const startGen = editorGenerationByChangeId.get(changeId) ?? 0;
        const outcome = await send("get_change", { change_id: changeId });
        if (editorRequestsByChangeId.has(changeId)) {
          cardRefreshAfterEditor.add(changeId);
          return;
        }
        if ((editorGenerationByChangeId.get(changeId) ?? 0) !== startGen) {
          continue;
        }
        if (!outcome.ok) {
          if (outcome.kind === "session") {
            endSession();
          }
          cardLoadErrors.set(changeId, outcome.message);
          return;
        }
        if (outcome.result.change_id !== changeId) {
          cardLoadErrors.set(changeId, "The server returned a different Change Card.");
          return;
        }
        cardLoadErrors.delete(changeId);
        receiveLatestCard(outcome.result);
      } while (cardRefreshAgain.has(changeId) && session !== null);
    })();
    cardRefreshInFlight.set(changeId, task);
    void task.finally(() => {
      cardRefreshInFlight.delete(changeId);
      syncAllRegions();
    });
    syncAllRegions();
  }

  // receiveLatestCard reconciles a background read of latest.change_id inside
  // editorsByChangeId, checking dirty state strictly on that card.
  function receiveLatestCard(latest: ChangeCardResult) {
    recordObservedCard(latest);
    const ed = editorsByChangeId.get(latest.change_id);
    if (!ed) {
      return;
    }
    cardLoadErrors.delete(latest.change_id);
    const isActive = activeChangeId === latest.change_id;
    if (latest.request_revision !== ed.card.request_revision || latest.status !== ed.card.status) {
      verificationGeneration++;
      if (isActive) {
        refreshVerificationObservations(latest.change_id);
      }
    }
    if (latest.request_revision === ed.card.request_revision) {
      const before = ed.card.proposed_revision;
      const after = latest.proposed_revision;
      editorsByChangeId.set(latest.change_id, {
        ...ed,
        card: latest,
        proposalAnswers: keepProposalAnswers(ed.proposalAnswers, before, after),
        proposalError: before?.request_revision === after?.request_revision ? ed.proposalError : null,
      });
      if (
        isActive &&
        (ed.card.approval ?? ed.card.unverified_approval?.approval)?.report_digest !==
          (latest.approval ?? latest.unverified_approval?.approval)?.report_digest
      ) {
        refreshVerificationObservations(latest.change_id);
      }
      if (isActive && after !== undefined && before?.request_revision !== after.request_revision) {
        statusEl.textContent =
          (before === undefined
            ? "Your coding agent proposed a new revision of this change."
            : "Your coding agent replaced its proposed revision of this change.") +
          " Adopt or decline it in Agree; the agreed revision stays in force until then.";
      }
      syncAllRegions();
      return;
    }
    if (ed.incomingCard && ed.incomingCard.request_revision === latest.request_revision) {
      editorsByChangeId.set(latest.change_id, { ...ed, incomingCard: latest });
      syncAllRegions();
      return;
    }
    const cardBusyOrDirty =
      editorRequestsByChangeId.has(latest.change_id) ||
      isEditorDirtyState(ed) ||
      ed.formError !== null ||
      Object.keys(ed.proposalAnswers).length > 0;
    if (cardBusyOrDirty) {
      editorsByChangeId.set(latest.change_id, { ...ed, incomingCard: latest });
      if (isActive) {
        statusEl.textContent =
          "A newer revision of this Change Card was saved. Your unsaved edits are kept; use Switch to Incoming Card to review it.";
      }
      syncAllRegions();
      return;
    }
    editorsByChangeId.set(latest.change_id, {
      card: latest,
      priorCard: nextPriorCard(ed, latest),
      incomingCard: null,
      ...editorDraftFrom(latest),
      preservedDraft: null,
      formError: null,
    });
    if (isActive) {
      statusEl.textContent = "A newer revision of this Change Card was saved and is now shown.";
    }
    syncAllRegions();
  }

  function refreshVerificationsInBackground(changeId: string) {
    if (hasPending("list_verifications", changeId)) {
      return;
    }
    const generation = verificationGeneration;
    const order = ++verificationReadOrder;
    for (const run of runsById.values()) {
      if (run.change_id === changeId) {
        markRunReadPending(run.run_id, order);
      }
    }
    callServer(
      "list_verifications",
      { change_id: changeId },
      changeId,
      (payload) => {
        if (validateVerificationListPayload(payload, changeId) !== null) {
          return;
        }
        for (let i = 0; i < payload.runs.length; i++) {
          upsertRunSnapshot(payload.runs[i], generation, order);
        }
        const loadedIds = payload.runs.map((r) => r.run_id);
        lastLoadedListByChangeId.set(changeId, loadedIds);
        listRequestErrors.delete(changeId);
        const restoredRun = pendingRestoredRunByChangeId.get(changeId);
        if (restoredRun !== undefined) {
          pendingRestoredRunByChangeId.delete(changeId);
          if (selectedVerificationChangeId === changeId && selectedRunId === restoredRun) {
            if (!loadedIds.includes(restoredRun)) {
              const card = editorsByChangeId.get(changeId)?.card;
              const approval = card?.approval ?? card?.unverified_approval?.approval;
              const onApproveStage =
                currentView.kind === "change" &&
                currentView.changeId === changeId &&
                currentView.stage === "approve";
              selectedRunId =
                onApproveStage && card !== undefined && approval !== undefined
                  ? savedApprovalEvidenceRun(card, approval)?.run_id ?? loadedIds[0] ?? null
                  : loadedIds[0] ?? null;
              syncHistoryState("replace");
            }
          }
        } else if (
          selectedVerificationChangeId === changeId &&
          selectedRunId === null &&
          loadedIds.length > 0
        ) {
          const card = editorsByChangeId.get(changeId)?.card;
          const approval = card?.approval ?? card?.unverified_approval?.approval;
          const onApproveStage =
            currentView.kind === "change" &&
            currentView.changeId === changeId &&
            currentView.stage === "approve";
          selectedRunId =
            onApproveStage && card !== undefined && approval !== undefined
              ? savedApprovalEvidenceRun(card, approval)?.run_id ?? null
              : loadedIds[0];
        }
        if (
          selectedVerificationChangeId === changeId &&
          selectedRunId !== null &&
          isViewingSelectedRun(selectedRunId)
        ) {
          readApprovalReview(selectedRunId, changeId);
        }
      },
      (message) => {
        listRequestErrors.set(changeId, message);
      }
    );
  }

  function resolveDisplayedVerificationRun(): VerificationStatusResult | null {
    return selectedRunId !== null ? runsById.get(selectedRunId) ?? null : null;
  }

  function runInputsKnown(run: VerificationStatusResult): boolean {
    const listed = changeList?.changes.find((item) => item.change_id === run.change_id);
    const ed = editorsByChangeId.get(run.change_id) ?? null;
    const cardMatchesList =
      ed === null ||
      [ed.card, ed.incomingCard].some(
        (card) =>
          card !== null &&
          card.request_revision === listed?.request_revision &&
          card.status === listed.status
      );
    return (
      sessionState === "active" &&
      !document.hidden &&
      changeList !== null &&
      listError === null &&
      !hasPending("list_changes", "") &&
      cardMatchesList &&
      !waitTroubleShown &&
      !editorRequestsByChangeId.has(run.change_id) &&
      !cardRefreshInFlight.has(run.change_id) &&
      runObservationGeneration.get(run.run_id) === verificationGeneration &&
      !hasPending("get_verification", run.run_id) &&
      !hasPending("review_verification", run.run_id) &&
      !hasPending("list_verifications", run.change_id) &&
      !hasPending("get_build", run.change_id) &&
      !hasPending("start_build", run.change_id) &&
      !cardLoadErrors.has(run.change_id) &&
      (buildChangeId !== run.change_id || buildReadError === null)
    );
  }

  function refreshVerificationObservations(changeId: string) {
    const latest = changeList?.changes.find((item) => item.change_id === changeId)?.latest_verification;
    if (latest !== undefined) {
      readVerification(latest.run_id, changeId);
    }
    if (
      selectedVerificationChangeId === changeId &&
      selectedRunId !== null &&
      selectedRunId !== latest?.run_id
    ) {
      readVerification(selectedRunId, changeId);
    }
    const card = editorsByChangeId.get(changeId)?.card;
    const approval = card?.approval ?? card?.unverified_approval?.approval;
    if (
      card !== undefined &&
      approval !== undefined &&
      knownTakingBuild(changeId) !== null &&
      !listRequestErrors.has(changeId) &&
      approvalRunFor(card, approval) === null
    ) {
      refreshVerificationsInBackground(changeId);
    }
  }

  function knownTakingBuild(changeId: string): StartedBuild | null {
    return sessionState === "active" &&
      !waitTroubleShown &&
      buildChangeId === changeId &&
      buildSnapshot?.state === "started" &&
      buildReadError === null &&
      !hasPending("get_build", changeId) &&
      !hasPending("start_build", changeId) &&
      !Array.from(pendingCalls).some((call) => call.op === "drop_build")
      ? buildSnapshot
      : null;
  }

  // currentInForceBuild returns the active in-force non-dropped StartedBuild
  // matching card.request_revision when all build reads are fresh.
  function currentInForceBuild(card: ChangeCardResult): StartedBuild | null {
    const build = knownTakingBuild(card.change_id);
    if (
      build === null ||
      !build.in_force ||
      build.taking.state === "dropped" ||
      build.request_revision !== card.request_revision
    ) {
      return null;
    }
    return build;
  }

  // runStartedInCurrentBuild attributes both passing and failed/incomplete runs
  // to the current build without changing runBelongsToBuild's merge admission.
  function runStartedInCurrentBuild(
    card: ChangeCardResult,
    build: StartedBuild,
    run: VerificationStatusResult
  ): boolean {
    return (
      run.change_id === card.change_id &&
      run.request_revision === build.request_revision &&
      buildTimestampNanos(run.accepted_at) >= buildTimestampNanos(build.started_at)
    );
  }

  function approvalRunFor(
    card: ChangeCardResult,
    approval: ApprovalItem
  ): VerificationStatusResult | null {
    const build = knownTakingBuild(card.change_id);
    if (build === null) {
      return null;
    }
    for (const run of runsById.values()) {
      if (
        runInputsKnown(run) &&
        runBelongsToBuild(card.change_id, build, run) &&
        run.request_revision === card.request_revision &&
        run.candidate_commit === approval.candidate_commit &&
        run.suite_commit === approval.suite_commit &&
        run.test_source.source === approval.test_source.source &&
        run.state.type === "completed" &&
        run.state.value.report.report_digest === approval.report_digest
      ) {
        return run;
      }
    }
    return null;
  }

  function savedApprovalEvidenceRun(
    card: ChangeCardResult,
    approval: ApprovalItem
  ): VerificationStatusResult | null {
    const matches = Array.from(runsById.values()).filter(
      (run) =>
        run.change_id === card.change_id &&
        run.request_revision === card.request_revision &&
        run.candidate_commit === approval.candidate_commit &&
        run.suite_commit === approval.suite_commit &&
        run.test_source.source === approval.test_source.source &&
        run.state.type === "completed" &&
        run.state.value.report.report_digest === approval.report_digest
    );
    return matches.length === 1 ? matches[0] : approvalRunFor(card, approval);
  }

  function latestVerificationFor(changeId: string): VerificationStatusResult | null {
    const id = changeList?.changes.find((item) => item.change_id === changeId)?.latest_verification
      ?.run_id;
    return id === undefined ? null : runsById.get(id) ?? null;
  }

  function newerCheckFor(run: VerificationStatusResult): VerificationStatusResult | null {
    const latest = latestVerificationFor(run.change_id);
    const history = lastLoadedListByChangeId.get(run.change_id);
    return latest !== null && history?.[0] === latest.run_id && history.indexOf(run.run_id) > 0
      ? latest
      : null;
  }

  function reviewForRun(runId: string | null): VerificationReview | null {
    return runId !== null ? reviewsByRunId.get(runId)?.review ?? null : null;
  }

  function displayedApprovalReview(): VerificationReview | null {
    return reviewForRun(selectedRunId);
  }

  function isViewingSelectedRun(runId: string): boolean {
    const runChangeId = runsById.get(runId)?.change_id;
    return (
      selectedRunId === runId &&
      currentView.kind === "change" &&
      (currentView.stage === "verify" ||
        currentView.stage === "approve" ||
        (currentView.stage === "build" && currentView.changeId === runChangeId))
    );
  }

  function approvalReviewFresh(run: VerificationStatusResult): boolean {
    const entry = reviewsByRunId.get(run.run_id);
    return (
      runInputsKnown(run) &&
      !hasPending("review_verification", run.run_id) &&
      entry !== undefined &&
      entry.generation === verificationGeneration &&
      entry.order >= (runObservationOrder.get(run.run_id) ?? -1)
    );
  }

  function mergeIdentity(run: VerificationStatusResult): string | null {
    const build =
      buildChangeId === run.change_id && buildSnapshot?.state === "started" ? buildSnapshot : null;
    return build !== null && run.state.type === "completed"
      ? JSON.stringify([
          run.change_id,
          build.started_at,
          run.request_revision,
          run.candidate_commit,
          run.state.value.report.report_digest,
        ])
      : null;
  }

  function buildApprovalRun(card: ChangeCardResult): VerificationStatusResult | null {
    const approval = card.approval;
    if (approval === undefined) {
      return null;
    }
    const selected = resolveDisplayedVerificationRun();
    if (
      selected !== null &&
      selected.change_id === card.change_id &&
      selected.request_revision === card.request_revision &&
      selected.candidate_commit === approval.candidate_commit &&
      selected.state.type === "completed" &&
      selected.state.value.report.report_digest === approval.report_digest
    ) {
      return selected;
    }
    return savedApprovalEvidenceRun(card, approval);
  }

  function refreshMergeState(run: VerificationStatusResult) {
    if (sessionState !== "active") {
      return;
    }
    selectedRunId = run.run_id;
    selectedVerificationChangeId = run.change_id;
    readBuild(run.change_id);
    readApprovalReview(run.run_id, run.change_id);
    refreshOpenCard(run.change_id);
    readTeam();
  }

  function mergeControls(run: VerificationStatusResult | null): MergeControls {
    const build = run !== null ? knownTakingBuild(run.change_id) : null;
    const review = run !== null ? reviewForRun(run.run_id) : null;
    const anyMergePending = Array.from(pendingCalls).some((call) => call.op === "merge_change");
    const pending = run !== null && hasPending("merge_change", run.change_id);
    const identity = run !== null ? mergeIdentity(run) : null;
    const outcome = identity !== null && mergeOutcome?.identity === identity ? mergeOutcome : null;
    let reason: string | null = null;
    if (run === null) reason = "Read the exact saved approval before merging.";
    else if (sessionState !== "active")
      reason = "Open a fresh Highbay page through your coding agent to check current approval.";
    else if (anyMergePending && !pending)
      reason = "Another change is being merged. Wait for that operation to finish.";
    else if (build === null) reason = "Read the current build before merging.";
    else if (build.taking.state === "merged")
      reason = "This build is already merged. Describe the next change when you are ready.";
    else if (build.taking.state === "dropped")
      reason = "This build was dropped; it cannot be merged through Highbay.";
    else if (!build.in_force || !runBelongsToBuild(run.change_id, build, run))
      reason = "This result does not belong to the current agreed build.";
    else if (!approvalReviewFresh(run))
      reason = "Refresh merge state to read current approval and build facts.";
    else if (review?.approval.state !== "recorded")
      reason = "Review and approve this result before merging.";
    else if (build.taking.state !== "passed")
      reason = "The current build has no passed result to merge.";
    else if (
      run.state.type !== "completed" ||
      !run.state.value.report.passed ||
      review.approval.candidate_commit !== run.candidate_commit ||
      review.approval.report_digest !== run.state.value.report.report_digest ||
      review.approval.suite_commit !== run.suite_commit
    )
      reason = "The saved approval does not match this result. Refresh merge state.";
    return {
      eligible: reason === null && !pending,
      pending,
      reason,
      receipt: outcome?.receipt ?? null,
      error: outcome?.error ?? null,
      onMerge: () => {
        if (run !== null) mergeApprovedRun(run);
      },
      onRefresh: () => {
        if (run !== null) refreshMergeState(run);
      },
    };
  }

  function mergeApprovedRun(run: VerificationStatusResult) {
    if (!mergeControls(run).eligible || run.state.type !== "completed") {
      return;
    }
    const identity = mergeIdentity(run);
    if (identity === null) {
      return;
    }
    const request: Operations["merge_change"]["request"] = {
      change_id: run.change_id,
      request_revision: run.request_revision,
      candidate_commit: run.candidate_commit,
      report_digest: run.state.value.report.report_digest,
    };
    mergeOutcome = { identity, receipt: null, error: null };
    buildGeneration++;
    verificationGeneration++;
    callServer(
      "merge_change",
      request,
      run.change_id,
      (result) => {
        if (sessionState !== "active") return;
        mergeOutcome = { identity, receipt: { at: new Date().toISOString(), ...result }, error: null };
        statusEl.textContent =
          "Merged approved change " +
          run.change_id +
          " into " +
          result.branch +
          " at " +
          result.head_commit.slice(0, 12) +
          ".";
        readBuild(run.change_id);
        refreshOpenCard(run.change_id);
        readTeam();
        void loadChanges();
      },
      (message, failure) => {
        if (sessionState !== "active") return;
        mergeOutcome = {
          identity,
          receipt: null,
          error: {
            name: failure.kind === "server" ? failure.body?.name ?? null : null,
            message,
          },
        };
        statusEl.textContent = "Merge was not confirmed. Refresh merge state before trying again.";
      }
    );
  }

  function mergePanel(
    run: VerificationStatusResult | null,
    idPrefix: string,
    observedBuild: StartedBuild | null = null
  ): React.ReactNode {
    const controls = mergeControls(run);
    const build = run !== null ? knownTakingBuild(run.change_id) : observedBuild;
    const review = run !== null ? reviewForRun(run.run_id) : null;
    const approvedBuild =
      run !== null &&
      build !== null &&
      build.in_force &&
      runBelongsToBuild(run.change_id, build, run) &&
      (build.taking.state === "passed" || build.taking.state === "merged") &&
      review?.approval.state === "recorded";
    const tests = build?.test_writer.tests;
    const postMerge =
      build?.taking.state === "merged" && (approvedBuild || observedBuild !== null) ? (
        <MergedBranchVerification
          id={idPrefix + "-verify-merged"}
          requestRevision={build.request_revision}
          suiteCommit={
            approvedBuild ? run.suite_commit : tests?.state === "accepted" ? tests.commit : null
          }
          approvedSuite={approvedBuild}
          mergedCommit={approvedBuild ? controls.receipt?.head_commit ?? null : null}
          storedEvaluator={tests?.state === "not_used"}
        />
      ) : null;
    if (
      run === null ||
      (!approvedBuild && !controls.pending && controls.receipt === null && controls.error === null)
    ) {
      return postMerge;
    }
    let manualInstructions: React.ReactNode = null;
    if (
      controls.eligible &&
      build !== null &&
      build.taking.state === "passed" &&
      review?.approval.state === "recorded" &&
      approvalReviewFresh(run) &&
      runBelongsToBuild(run.change_id, build, run) &&
      run.state.type === "completed"
    ) {
      manualInstructions = (
        <BuildMergeInstructions
          taking={build.taking}
          candidateCommit={run.candidate_commit}
          testsCommit={run.test_source.source === "written_tests" ? run.test_source.commit : null}
          onFocusedControlRemoved={focusVerificationHeading}
        />
      );
    }
    return (
      <>
        <MergeFeedback idPrefix={idPrefix} controls={controls} />
        {manualInstructions}
        {postMerge}
      </>
    );
  }

  // reviewSavedCheck opens Approve on the exact run whose action the user selected.
  function reviewSavedCheck(run: VerificationStatusResult) {
    navigationSeq++;
    activeChangeId = run.change_id;
    selectedVerificationChangeId = run.change_id;
    selectedRunId = run.run_id;
    mobileSurface = "stage";
    currentView = { kind: "change", changeId: run.change_id, stage: "approve" };
    statusEl.textContent = "Review the saved check before approving it.";
    if (!lastLoadedListByChangeId.has(run.change_id)) {
      refreshVerificationsInBackground(run.change_id);
    }
    readApprovalReview(run.run_id, run.change_id);
    syncHistoryState("push");
    syncAllRegions();
    focusResult("#highbay-cp5-title");
  }

  function openApproval() {
    navigationSeq++;
    const changeId = activeChangeId ?? selectedVerificationChangeId;
    if (changeId === null) {
      return;
    }
    const onVerifyOrApprove =
      currentView.kind === "change" &&
      (currentView.stage === "verify" || currentView.stage === "approve");
    const displayed = resolveDisplayedVerificationRun();
    const selected =
      displayed !== null &&
      displayed.change_id === changeId &&
      (onVerifyOrApprove ||
        (displayed.state.type === "completed" && displayed.state.value.report.passed))
        ? displayed
        : null;
    const ed = editorsByChangeId.get(changeId);
    const savedApproval = ed?.card.approval ?? ed?.card.unverified_approval?.approval;
    const savedRun =
      ed !== undefined && savedApproval !== undefined
        ? savedApprovalEvidenceRun(ed.card, savedApproval)
        : null;
    const run = selected ?? savedRun ?? latestVerificationFor(changeId);
    if (run !== null) {
      reviewSavedCheck(run);
      return;
    }
    selectedRunId = null;
    selectedVerificationChangeId = changeId;
    mobileSurface = "stage";
    switchComposerScope(composerScopeFor("change", changeId));
    currentView = { kind: "change", changeId, stage: "approve" };
    refreshVerificationsInBackground(changeId);
    syncHistoryState("push");
    syncAllRegions();
    focusResult("#highbay-cp5-title");
  }

  function openRecordedApproval() {
    const ed = getActiveEditor();
    if (ed === null) {
      return;
    }
    const card = ed.card;
    const approval = card.approval ?? card.unverified_approval?.approval;
    const run = approval !== undefined ? savedApprovalEvidenceRun(card, approval) : null;
    if (run !== null) {
      reviewSavedCheck(run);
      return;
    }
    navigationSeq++;
    selectedVerificationChangeId = card.change_id;
    selectedRunId = null;
    mobileSurface = "stage";
    currentView = { kind: "change", changeId: card.change_id, stage: "approve" };
    if (!lastLoadedListByChangeId.has(card.change_id) && !listRequestErrors.has(card.change_id)) {
      refreshVerificationsInBackground(card.change_id);
    }
    syncHistoryState("push");
    syncAllRegions();
    focusResult("#highbay-cp5-title");
  }

  // dirtyChangeIds lists all changeIds in editorsByChangeId with unsaved edits or proposal answers.
  function dirtyChangeIds(): Set<string> {
    const out = new Set<string>();
    for (const [id, ed] of editorsByChangeId.entries()) {
      if (isEditorDirtyState(ed) || Object.keys(ed.proposalAnswers).length > 0) {
        out.add(id);
      }
    }
    return out;
  }

  function hasAnyUnsavedEdits(): boolean {
    for (const ed of editorsByChangeId.values()) {
      if (isEditorDirtyState(ed)) {
        return true;
      }
    }
    return false;
  }

  function hasPendingEditorSave(): boolean {
    for (const op of editorRequestsByChangeId.values()) {
      if (ROUTES[op].method !== "GET") {
        return true;
      }
    }
    return false;
  }

  function hasUnsavedPageInput(): boolean {
    if (
      hasAnyUnsavedEdits() ||
      hasPendingEditorSave() ||
      modelDraftDirty ||
      composerDraft.text.trim() !== "" ||
      describeText.trim() !== ""
    ) {
      return true;
    }
    for (const [scope, draft] of composerDraftsByScope.entries()) {
      if (scope !== activeComposerScope && draft.text.trim() !== "") {
        return true;
      }
    }
    for (const ed of editorsByChangeId.values()) {
      if (Object.keys(ed.proposalAnswers).length > 0) {
        return true;
      }
    }
    return false;
  }

  function discardAllPageDrafts() {
    if (hasPendingEditorSave()) {
      return;
    }
    for (const [id, ed] of editorsByChangeId.entries()) {
      editorsByChangeId.set(id, {
        card: ed.card,
        priorCard: ed.priorCard,
        incomingCard: ed.incomingCard,
        ...editorDraftFrom(ed.card),
        preservedDraft: null,
        formError: null,
      });
    }
    composerDraft = { ...composerDraft, text: "", seededText: undefined };
    for (const [scope, draft] of composerDraftsByScope.entries()) {
      composerDraftsByScope.set(scope, { ...draft, text: "", seededText: undefined });
    }
    describeText = "";
    if (!hasPending("configure_model", "")) {
      modelDraft = { kind: null, provider: "anthropic", modelName: "", gatewayAddr: "" };
      modelDraftDirty = false;
      modelDraftReset++;
    }
    syncAllRegions();
  }

  function openPendingLink() {
    if (pendingLinkFragment === null || hasUnsavedPageInput()) {
      return;
    }
    window.history.replaceState(
      null,
      "",
      window.location.pathname + window.location.search + pendingLinkFragment
    );
    window.location.reload();
  }

  function showChanges(options?: { historyMode?: "push" | "replace"; mobileSurface?: MobileSurface }) {
    navigationSeq++;
    mobileSurface = options?.mobileSurface ?? "stage";
    switchComposerScope("draft");
    currentView = { kind: "changes" };
    statusEl.textContent =
      changeList === null
        ? "Reading this project's changes..."
        : changeList.changes.length === 0
        ? "This project has no changes yet."
        : "Showing this project's " + changeList.changes.length + " changes.";
    if (changeList === null) {
      void loadChanges();
    }
    syncHistoryState(options?.historyMode ?? (handlingPopState ? "replace" : "push"));
    syncAllRegions();
  }

  function openNewChange(options?: { historyMode?: "push" | "replace"; mobileSurface?: MobileSurface }) {
    navigationSeq++;
    mobileSurface = options?.mobileSurface ?? "stage";
    switchComposerScope("draft");
    if (changeList !== null && changeList.changes.length === 0) {
      currentView = { kind: "changes" };
    } else {
      currentView = { kind: "new" };
    }
    statusEl.textContent = "Describe the next change for Guide to draft.";
    if (storyEvaluation === null || storyEvaluationStale) {
      readStoryEvaluation();
    }
    syncHistoryState(options?.historyMode ?? (handlingPopState ? "replace" : "push"));
    syncAllRegions();
    focusResult("#highbay-describe-input");
  }

  function openStory(options?: { historyMode?: "push" | "replace"; mobileSurface?: MobileSurface }) {
    navigationSeq++;
    mobileSurface = options?.mobileSurface ?? "stage";
    switchComposerScope(composerScopeFor("story", activeChangeId));
    currentView = { kind: "story" };
    const ed = getActiveEditor();
    statusEl.textContent =
      ed !== null
        ? "Showing the Story of Change Card " + ed.card.change_id + "."
        : "Showing the current Story draft.";
    if (storyEvaluation === null || storyEvaluationStale) {
      readStoryEvaluation();
    }
    syncHistoryState(options?.historyMode ?? (handlingPopState ? "replace" : "push"));
    syncAllRegions();
  }

  function readStoryEvaluation() {
    if (hasPending("get_story_refresh", "")) {
      storyReadAgain = true;
      return;
    }
    storyReadAgain = false;
    storyEvaluationStale = false;
    const done = () => {
      if (storyReadAgain) {
        readStoryEvaluation();
      }
    };
    callServer(
      "get_story_refresh",
      {},
      "",
      (evaluation) => {
        storyEvaluation = evaluation;
        storyEvaluationError = null;
        done();
      },
      (message) => {
        storyEvaluationError = message;
        storyEvaluationStale = true;
        if (currentView.kind === "story") {
          statusEl.textContent = "Could not read the current Story draft.";
        }
        done();
      }
    );
  }

  function openTeam(options?: { historyMode?: "push" | "replace"; mobileSurface?: MobileSurface }) {
    navigationSeq++;
    mobileSurface = options?.mobileSurface ?? "stage";
    switchComposerScope("draft");
    currentView = { kind: "team" };
    statusEl.textContent = "Showing the project team.";
    if (teamSnapshot === null || teamReadError !== null || waitTroubleShown) {
      readTeam();
    }
    syncHistoryState(options?.historyMode ?? (handlingPopState ? "replace" : "push"));
    syncAllRegions();
    document.getElementById("highbay-team-title")?.focus();
  }

  function readTeam() {
    if (sessionState !== "active") return;
    if (hasPending("get_team", "")) {
      teamReadAgain = true;
      return;
    }
    teamReadAgain = false;
    const done = () => {
      if (teamReadAgain && sessionState === "active") readTeam();
    };
    callServer(
      "get_team",
      {},
      "",
      (snapshot) => {
        if (sessionState !== "active") return;
        teamSnapshot = snapshot;
        teamReadError = null;
        proposeMissingWorkerChoices();
        reconcileConversationDefaultOpen();
        done();
      },
      (message) => {
        if (sessionState !== "active") return;
        const firstFailure = teamReadError === null;
        teamReadError = { readFailed: true, message };
        if (firstFailure) {
          statusEl.textContent =
            "Could not read the project team. Open Team to see the error and retry.";
        }
        done();
      }
    );
  }

  function buildWorkerRoles(): readonly WorkerRole[] {
    return agentOptions?.build_roles ?? [];
  }

  function teamWorkerRoles(): WorkerRole[] {
    return (teamSnapshot?.roles ?? []).flatMap((role) =>
      role.role !== "guide" && (role.assignment === "unread" || role.assignment === "nothing_owed")
        ? [role.role]
        : []
    );
  }

  function readConversation() {
    if (sessionState !== "active") return;
    if (hasPending("get_team_conversation", "")) {
      conversationReadAgain = true;
      return;
    }
    conversationReadAgain = false;
    const done = () => {
      if (conversationReadAgain && sessionState === "active") readConversation();
    };
    callServer(
      "get_team_conversation",
      {},
      "",
      (conv) => {
        if (sessionState !== "active") return;
        teamConversation = conv;
        conversationError = null;
        reconcileConversationDefaultOpen();
        const visibleOnScreen = isNarrowViewport()
          ? mobileSurface === "conversation"
          : conversationOpen;
        if (visibleOnScreen) {
          seenMessageCount = conv.messages.length;
        }
        done();
      },
      (message) => {
        if (sessionState !== "active") return;
        conversationError = message;
        done();
      }
    );
  }

  function proposeMissingWorkerChoices() {
    if (agentOptions === null) return;
    const saved = { ...(teamSnapshot?.saved_choices ?? {}) };
    if (!guideChoiceEdited) {
      guideChoice = proposeGuideChoice(agentOptions.agents, saved);
    }
    if (teamSnapshot === null) return;
    for (const role of editedWorkerChoices) {
      const choice = workerChoices[role];
      if (choice !== undefined) saved[role] = choice;
    }
    const roles = Array.from(new Set([...buildWorkerRoles(), ...teamWorkerRoles()]));
    const proposed = proposeWorkerChoices(roles, agentOptions.agents, saved);
    for (const role of roles) {
      if (!editedWorkerChoices.has(role)) workerChoices[role] = proposed[role];
    }
  }

  // loadAgentOptions starts initial discovery directly when options are unread.
  function loadAgentOptions() {
    if (agentOptions === null && agentOptionsError === null) {
      readAgentOptions();
    }
  }

  function readAgentOptions() {
    if (sessionState !== "active" || hasPending("list_team_agent_options", "")) return;
    if (teamSnapshot === null || teamFactsError() !== null) readTeam();
    callServer(
      "list_team_agent_options",
      {},
      "",
      (result) => {
        if (sessionState !== "active") return;
        agentOptions = result;
        agentOptionsError = null;
        proposeMissingWorkerChoices();
      },
      (message) => {
        if (sessionState === "active") agentOptionsError = message;
      }
    );
  }

  function workersReady(roles: readonly WorkerRole[]): boolean {
    return (
      agentOptions !== null &&
      agentOptionsError === null &&
      !hasPending("list_team_agent_options", "") &&
      teamFactsError() === null &&
      workerChoicesReady(roles, workerChoices, agentOptions.agents)
    );
  }

  function dismissTeamNotice(notice: TeamNotice) {
    if (sessionState !== "active" || hasPending("dismiss_team_notice", notice)) return;
    noticeErrors.delete(notice);
    callServer(
      "dismiss_team_notice",
      { notice },
      notice,
      () => {
        if (sessionState !== "active") return;
        noticeDismissed.add(notice);
        readTeam();
      },
      (message) => {
        if (sessionState === "active") noticeErrors.set(notice, message);
      }
    );
  }

  function workerRecommendations(idPrefix: string): React.ReactNode {
    const snapshot = teamSnapshot;
    if (snapshot === null || teamFactsError() !== null) return null;
    const families = new Set((agentOptions?.agents ?? []).map((option) => option.client));
    const notices: { notice: TeamNotice; title: string; text: string }[] = [];
    if (buildWorkerRoles().includes("test_writer") && families.size === 1) {
      notices.push({
        notice: "single_agent_family",
        title: "One coding-agent family is installed.",
        text: "Highbay can use it for both workers. A second model family can give the builder and test writer different perspectives.",
      });
    }
    return notices
      .filter(
        ({ notice }) => !noticeDismissed.has(notice) && !snapshot.dismissed_notices.includes(notice)
      )
      .map(({ notice, title, text }) => (
        <Alert key={notice} id={idPrefix + "-notice-" + notice}>
          <p>
            <strong>{title}</strong>
          </p>
          <p>{text}</p>
          {noticeErrors.has(notice) ? (
            <p role="alert">Could not save this dismissal. {noticeErrors.get(notice)}</p>
          ) : null}
          <Button
            id={idPrefix + "-notice-" + notice + "-dismiss"}
            disabled={sessionState !== "active" || hasPending("dismiss_team_notice", notice)}
            onClick={() => dismissTeamNotice(notice)}
          >
            {hasPending("dismiss_team_notice", notice)
              ? "Saving…"
              : noticeErrors.has(notice)
              ? "Retry dismissal"
              : "Dismiss recommendation"}
          </Button>
        </Alert>
      ));
  }

  function workerPicker(idPrefix: string, roles: readonly WorkerRole[]): React.ReactNode {
    return (
      <>
        {workerRecommendations(idPrefix)}
        <AgentPickerView
          idPrefix={idPrefix}
          roles={roles}
          options={agentOptions?.agents ?? null}
          choices={workerChoices}
          loading={hasPending("list_team_agent_options", "")}
          error={agentOptionsError}
          disabled={
            sessionState !== "active" ||
            hasPending("start_team_agents", "") ||
            Array.from(pendingCalls).some((call) => call.op === "start_build")
          }
          onChoiceChange={(role, choice) => {
            workerChoices = { ...workerChoices, [role]: choice };
            editedWorkerChoices.add(role);
            proposeMissingWorkerChoices();
            syncAllRegions();
          }}
          onRefresh={readAgentOptions}
          onLoad={loadAgentOptions}
        />
      </>
    );
  }

  function teamFactsError(): TeamFactsError | null {
    if (teamReadError !== null) return teamReadError;
    if (sessionState !== "active") {
      return { readFailed: false, message: "The session ended; these are last-read facts." };
    }
    if (waitTroubleShown) {
      return { readFailed: false, message: "Updates are unavailable; these are last-read facts." };
    }
    return null;
  }

  function connectedGuide(): TeamRoleState | undefined {
    return teamSnapshot?.roles.find(
      (role) => role.role === "guide" && role.state !== "not_started" && role.state !== "ended"
    );
  }

  function pendingRoleOperations(
    op: "interrupt_team_role" | "stop_team_role" | "restart_team_role"
  ): TeamRoleState["role"][] {
    return (teamSnapshot?.roles ?? [])
      .filter((role) => hasPending(op, role.role))
      .map((role) => role.role);
  }

  function focusCurrentViewHeading() {
    const heading = viewEl.querySelector<HTMLElement>("h2, h3");
    if (heading !== null) {
      heading.tabIndex = -1;
      heading.focus();
    } else {
      viewEl.focus();
    }
  }

  function focusVerificationHeading() {
    focusResult(
      currentView.kind === "change" && currentView.stage === "approve"
        ? "#highbay-cp5-title"
        : "#highbay-cp4-title"
    );
  }

  function teamWorkersHaveWork(): boolean {
    return (
      teamSnapshot?.roles.some(
        (role) =>
          role.role !== "guide" &&
          (role.assignment === "unread" || role.assignment === "nothing_owed")
      ) ?? false
    );
  }

  function startTeamWorkers() {
    if (
      sessionState !== "active" ||
      !teamWorkersHaveWork() ||
      !workersReady(teamWorkerRoles()) ||
      hasPending("start_team_agents", "")
    ) {
      return;
    }
    const begun = new Date().toISOString();
    teamActionError = null;
    callServer(
      "start_team_agents",
      { choices: choicesForRoles(teamWorkerRoles(), workerChoices) },
      "",
      (result) => {
        if (sessionState !== "active") return;
        teamStartReceipt = { at: begun, result };
        buildStartReceipt = null;
        statusEl.textContent =
          "This start (begun " + new Date(begun).toLocaleString() + ") returned: " + result.step;
        readTeam();
      },
      (message) => {
        if (sessionState !== "active") return;
        teamActionError = message;
        statusEl.textContent = "Could not start workers. " + message;
      }
    );
  }

  // resolveBuildWaiters notifies any callbacks awaiting changeId's build read.
  function resolveBuildWaiters(changeId: string, build: ChangeBuild | null) {
    const waiters = buildReadWaiters.get(changeId);
    if (!waiters) {
      return;
    }
    buildReadWaiters.delete(changeId);
    for (const ready of waiters) {
      ready(build);
    }
  }

  function readBuild(changeId: string, onSettled?: (build: ChangeBuild | null) => void) {
    if (onSettled !== undefined) {
      const existing = buildReadWaiters.get(changeId) ?? [];
      existing.push(onSettled);
      buildReadWaiters.set(changeId, existing);
    }
    if (sessionState !== "active" || activeChangeId !== changeId) {
      resolveBuildWaiters(changeId, null);
      return;
    }
    if (buildChangeId !== changeId) {
      buildChangeId = changeId;
      buildSnapshot = null;
      buildDraftedBeforeDrop = null;
      buildReadError = null;
      buildStartReceipt = null;
      buildStartError = null;
      openBuildRefusal = null;
      followBuildFindings();
      buildGeneration++;
    }
    if (hasPending("get_build", changeId)) {
      buildReadAgain = true;
      return;
    }
    buildReadAgain = false;
    const generation = buildGeneration;
    const relevant = () =>
      sessionState === "active" && buildChangeId === changeId && activeChangeId === changeId;
    const done = () => {
      if (relevant() && buildReadAgain) readBuild(changeId);
    };
    callServer(
      "get_build",
      { change_id: changeId },
      changeId,
      (result) => {
        if (relevant() && generation === buildGeneration) {
          if (result.change_id !== changeId) {
            buildReadError = "The server returned a build for a different change.";
            resolveBuildWaiters(changeId, null);
          } else {
            const before = buildSnapshot;
            buildSnapshot = result.build;
            buildDraftedBeforeDrop = result.drafted_before_drop;
            buildReadError = null;
            const replaced =
              before?.state === "started"
                ? result.build.state !== "started" || before.started_at !== result.build.started_at
                : result.build.state === "started";
            if (replaced) {
              buildStartReceipt = null;
              buildStartError = null;
            }
            if (before === null || JSON.stringify(before) !== JSON.stringify(result.build)) {
              verificationGeneration++;
              if (before !== null) refreshOpenCard(changeId);
            }
            refreshVerificationObservations(changeId);
            followBuildFindings();
            readBlockingBuild();
            resolveBuildWaiters(changeId, result);
          }
        } else if (!relevant()) {
          resolveBuildWaiters(changeId, null);
        }
        done();
      },
      (message) => {
        if (relevant() && generation === buildGeneration) {
          buildReadError = message;
          resolveBuildWaiters(changeId, null);
        } else if (!relevant()) {
          resolveBuildWaiters(changeId, null);
        }
        done();
      }
    );
  }

  function readBlockingBuild() {
    const refused = openBuildRefusal;
    if (
      refused === null ||
      refused.change_id === activeChangeId ||
      sessionState !== "active" ||
      hasPending("get_build", refused.change_id)
    ) {
      return;
    }
    const selected = activeChangeId;
    callServer(
      "get_build",
      { change_id: refused.change_id },
      refused.change_id,
      (result) => {
        if (
          sessionState !== "active" ||
          activeChangeId !== selected ||
          openBuildRefusal !== refused ||
          result.change_id !== refused.change_id
        ) {
          return;
        }
        if (
          result.build.state !== "started" ||
          (result.build.taking.state !== "building" && result.build.taking.state !== "passed")
        ) {
          openBuildRefusal = null;
          buildStartError = null;
        } else {
          openBuildRefusal = { ...refused, build: result.build };
        }
      },
      () => {
        /* Keep last owner-reported blocker until a successful read. */
      }
    );
  }

  function currentFindingsBasis(): string | null {
    if (
      buildChangeId === null ||
      buildSnapshot?.state !== "started" ||
      (buildSnapshot.taking.state !== "building" && buildSnapshot.taking.state !== "passed") ||
      buildSnapshot.test_writer.tests.state !== "needs_changes" ||
      hasPending("start_build", buildChangeId)
    ) {
      return null;
    }
    return JSON.stringify([
      buildChangeId,
      buildSnapshot.request_revision,
      buildSnapshot.started_at,
      buildSnapshot.test_writer.report,
      buildSnapshot.test_writer.tests.checked_at,
      buildSnapshot.test_writer.tests.finding_count,
    ]);
  }

  function followBuildFindings() {
    const next = currentFindingsBasis();
    if (next === findingsBasis) return;
    findingsBasis = next;
    buildFindings = null;
    findingsError = null;
    if (next !== null) readBuildFindings();
  }

  function readBuildFindings() {
    const expected = buildSnapshot;
    const changeId = buildChangeId;
    const basis = currentFindingsBasis();
    if (
      sessionState !== "active" ||
      changeId === null ||
      basis === null ||
      expected?.state !== "started" ||
      expected.test_writer.tests.state !== "needs_changes" ||
      activeChangeId !== changeId
    ) {
      return;
    }
    if (hasPending("get_test_findings", changeId)) {
      findingsReadAgain = true;
      return;
    }
    findingsReadAgain = false;
    findingsError = null;
    const generation = buildGeneration;
    const verdict = expected.test_writer.tests;
    const relevant = () =>
      sessionState === "active" &&
      buildGeneration === generation &&
      activeChangeId === changeId &&
      findingsBasis === basis &&
      currentFindingsBasis() === basis;
    const done = () => {
      if (findingsReadAgain) {
        findingsReadAgain = false;
        readBuildFindings();
      }
    };
    callServer(
      "get_test_findings",
      { change_id: changeId },
      changeId,
      (result) => {
        if (relevant()) {
          if (
            result.change_id !== changeId ||
            result.request_revision !== expected.request_revision ||
            result.started_at !== expected.started_at ||
            result.report !== expected.test_writer.report ||
            result.checked_at !== verdict.checked_at ||
            result.findings.length !== verdict.finding_count
          ) {
            findingsError =
              "The findings no longer match this build. Refresh the build to read its current verdict.";
            readBuild(changeId);
          } else {
            buildFindings = result;
          }
        }
        done();
      },
      (message, failure) => {
        if (relevant()) {
          findingsError = message;
          if (failure.kind === "server" && (failure.status === 404 || failure.status === 409)) {
            readBuild(changeId);
          }
        }
        done();
      }
    );
  }

  function prepareBuildConfirmation(ready: (build: ChangeBuild | null) => void) {
    const ed = getActiveEditor();
    if (ed === null || sessionState !== "active") return;
    readBuild(ed.card.change_id, ready);
  }

  function retainOpenBuild(failure: Failure) {
    const body = failure.body;
    if (
      body !== undefined &&
      "build" in body &&
      (body.name === "previous_change_open" ||
        body.name === "unfinished_build_open" ||
        body.name === "build_changed")
    ) {
      openBuildRefusal = body;
      if (body.change_id === buildChangeId) {
        buildSnapshot = { ...body.build, state: "started" };
        buildDraftedBeforeDrop = null;
        buildReadError = null;
      }
    }
  }

  function startBuild(replaces?: ReplacedBuild) {
    const ed = getActiveEditor();
    if (
      ed === null ||
      sessionState !== "active" ||
      editorRequestsByChangeId.has(ed.card.change_id) ||
      ed.card.status !== "confirmed" ||
      ed.incomingCard !== null ||
      !workersReady(buildWorkerRoles()) ||
      buildChangeId !== ed.card.change_id ||
      buildSnapshot === null ||
      buildReadError !== null ||
      buildDraftedBeforeDrop !== false ||
      hasPending("start_build", ed.card.change_id) ||
      (openBuildRefusal !== null && openBuildRefusal.change_id !== ed.card.change_id)
    ) {
      return;
    }
    const changeId = ed.card.change_id;
    const revision = ed.card.request_revision;
    if (
      buildSnapshot.state === "started" &&
      buildSnapshot.in_force &&
      buildSnapshot.request_revision !== revision
    ) {
      return;
    }
    const listedRevision = changeList?.changes.find((item) => item.change_id === changeId)
      ?.request_revision;
    if (listedRevision !== undefined && listedRevision !== revision) {
      return;
    }
    const request: Operations["start_build"]["request"] = {
      change_id: changeId,
      request_revision: revision,
      choices: choicesForRoles(buildWorkerRoles(), workerChoices),
    };
    if (buildSnapshot.state === "started" && buildSnapshot.taking.state === "building") {
      if (replaces?.change_id !== changeId || replaces.started_at !== buildSnapshot.started_at) {
        return;
      }
      request.replaces = replaces;
    }
    const begun = new Date().toISOString();
    buildGeneration++;
    verificationGeneration++;
    buildStartReceipt = null;
    buildStartError = null;
    openBuildRefusal = null;
    resolveBuildWaiters(changeId, null);
    buildFindings = null;
    findingsBasis = null;
    findingsError = null;
    findingsReadAgain = false;
    callServer(
      "start_build",
      request,
      changeId,
      (result) => {
        const currentEd = editorsByChangeId.get(changeId);
        if (
          sessionState !== "active" ||
          activeChangeId !== changeId ||
          currentEd?.card.request_revision !== revision
        ) {
          return;
        }
        buildGeneration++;
        verificationGeneration++;
        if (result.build.request_revision !== revision) {
          buildStartError =
            "The server returned a build for a different revision. Refresh the saved build.";
          statusEl.textContent = buildStartError;
          readBuild(changeId);
          return;
        }
        buildSnapshot = { ...result.build, state: "started" };
        buildDraftedBeforeDrop = null;
        buildReadError = null;
        buildStartReceipt = { at: begun, team: result.team };
        followBuildFindings();
        statusEl.textContent = "The build started for the agreed revision.";
        readBuild(changeId);
        refreshVerificationObservations(changeId);
        refreshOpenCard(changeId);
        readTeam();
      },
      (message, failure) => {
        if (sessionState === "active" && activeChangeId === changeId) {
          buildStartError =
            (failure.kind === "server"
              ? "Build did not start. "
              : "Could not confirm whether the build started. ") + message;
          retainOpenBuild(failure);
          statusEl.textContent =
            openBuildRefusal !== null && openBuildRefusal.change_id !== changeId
              ? ""
              : buildStartError;
          readBuild(changeId);
        }
      }
    );
  }

  function dropBuild(target: ReplacedBuild) {
    const ed = getActiveEditor();
    if (
      sessionState !== "active" ||
      ed === null ||
      editorRequestsByChangeId.has(ed.card.change_id) ||
      hasPending("drop_build", target.change_id)
    ) {
      return;
    }
    const shown =
      target.change_id === buildChangeId && buildSnapshot?.state === "started"
        ? buildSnapshot
        : openBuildRefusal?.change_id === target.change_id
        ? openBuildRefusal.build
        : null;
    if (
      shown === null ||
      shown.started_at !== target.started_at ||
      (shown.taking.state !== "building" && shown.taking.state !== "passed")
    ) {
      return;
    }
    const selected = ed.card.change_id;
    buildGeneration++;
    resolveBuildWaiters(selected, null);
    buildStartError = null;
    callServer(
      "drop_build",
      target,
      target.change_id,
      (result) => {
        if (sessionState !== "active" || activeChangeId !== selected) return;
        buildGeneration++;
        openBuildRefusal = null;
        if (result.change_id === buildChangeId) {
          buildSnapshot = result.build;
          buildDraftedBeforeDrop = result.drafted_before_drop;
        }
        statusEl.textContent =
          "The build was dropped. Ask your coding agent to refresh its draft and propose a revision. Adopt the revision before building again.";
        readBuild(selected);
        refreshOpenCard(selected);
        refreshVerificationObservations(selected);
        readTeam();
      },
      (message, failure) => {
        if (sessionState !== "active" || activeChangeId !== selected) return;
        buildStartError = "Could not drop the build. " + message;
        retainOpenBuild(failure);
        statusEl.textContent =
          openBuildRefusal !== null && openBuildRefusal.change_id !== selected
            ? ""
            : buildStartError;
        readBuild(selected);
      }
    );
  }

  // openRoleTerminal opens the stage-docked terminal in observe=true passive
  // watch mode unless the role is in needs_you or explicitObserve === false.
  function openRoleTerminal(
    role: TeamRoleState["role"],
    invoker: HTMLElement | null,
    explicitObserve?: boolean
  ) {
    if (sessionState !== "active") return;
    const roleState = teamSnapshot?.roles.find((r) => r.role === role) ?? null;
    const nextObserve =
      explicitObserve !== undefined ? explicitObserve : roleState?.state !== "needs_you";
    openTerminalRole = role;
    terminalObserveMode = nextObserve;
    if (terminalSize === "minimized") {
      terminalSize = "default";
    }
    terminalInvoker = invoker;
    terminalActionError = null;
    if (isNarrowViewport()) {
      terminalReturnSurface = mobileSurface === "conversation" ? "conversation" : "stage";
      mobileSurface = "terminal";
      if (terminalReturnSurface === "stage") {
        mobileSurfaceInvoker = invoker;
      }
    }
    syncHistoryState("replace");
    syncAllRegions();
    if (isNarrowViewport()) {
      if (nextObserve) {
        (
          document.getElementById("highbay-terminal-interactive") ??
          document.getElementById("highbay-terminal-close")
        )?.focus();
      }
    } else {
      document.getElementById("highbay-terminal-drawer")?.scrollIntoView({ block: "nearest" });
    }
  }

  function closeRoleTerminal() {
    const returnTo = terminalInvoker ?? mobileSurfaceInvoker;
    openTerminalRole = null;
    terminalInvoker = null;
    terminalActionError = null;
    if (mobileSurface === "terminal") {
      mobileSurface = isNarrowViewport() ? terminalReturnSurface : "stage";
      terminalReturnSurface = "stage";
      if (mobileSurface === "stage") {
        mobileSurfaceInvoker = null;
      }
    }
    syncHistoryState("replace");
    syncAllRegions();
    if (isFocusableVisible(returnTo)) {
      returnTo.focus();
      return;
    }
    if (isNarrowViewport()) {
      const mobileToggle =
        mobileSurface === "conversation"
          ? document.getElementById("highbay-conversation-title")
          : document.getElementById("highbay-mobile-terminal-toggle") ??
            document.getElementById("highbay-mobile-conversation-toggle");
      if (isFocusableVisible(mobileToggle)) {
        mobileToggle.focus();
        return;
      }
    }
    focusCurrentViewHeading();
  }

  async function createTerminalTicket(
    role: TeamRoleState["role"],
    signal: AbortSignal
  ): Promise<TerminalTicketOutcome> {
    if (session === null) {
      return { ok: false, message: sessionEndedMessage() };
    }
    const outcome = await send("create_terminal_ticket", { role }, signal);
    if (!outcome.ok) {
      if (outcome.kind === "session") {
        endSession();
      }
      return { ok: false, message: outcome.message };
    }
    return { ok: true, ticket: outcome.result.ticket };
  }

  function interruptTeamRole(role: TeamRoleState["role"]) {
    if (sessionState !== "active" || hasPending("interrupt_team_role", role)) return;
    teamActionError = null;
    terminalActionError = null;
    callServer(
      "interrupt_team_role",
      { role },
      role,
      () => {
        if (sessionState !== "active") return;
        statusEl.textContent = "Sent interrupt signal to " + role.replaceAll("_", " ") + ".";
        readTeam();
      },
      (message) => {
        if (sessionState !== "active") return;
        teamActionError = message;
        terminalActionError = message;
      }
    );
  }

  function stopTeamRole(role: TeamRoleState["role"]) {
    if (sessionState !== "active" || hasPending("stop_team_role", role)) return;
    teamActionError = null;
    terminalActionError = null;
    callServer(
      "stop_team_role",
      { role },
      role,
      () => {
        if (sessionState !== "active") return;
        statusEl.textContent = "Stopped " + role.replaceAll("_", " ") + " terminal.";
        readTeam();
      },
      (message) => {
        if (sessionState !== "active") return;
        teamActionError = message;
        terminalActionError = message;
      }
    );
  }

  function restartTeamRole(role: TeamRoleState["role"]) {
    if (sessionState !== "active" || hasPending("restart_team_role", role)) return;
    teamActionError = null;
    terminalActionError = null;
    let choice: RoleAgentChoice | undefined = undefined;
    if (role === "guide") {
      if (guideChoiceEdited && guideChoice !== undefined) {
        choice = guideChoice;
      }
    } else if (editedWorkerChoices.has(role) && workerChoices[role] !== undefined) {
      choice = workerChoices[role];
    }
    callServer(
      "restart_team_role",
      choice !== undefined ? { role, choice } : { role },
      role,
      () => {
        if (sessionState !== "active") return;
        terminalRestartSeq[role]++;
        statusEl.textContent = "Restarted " + role.replaceAll("_", " ") + " terminal.";
        readTeam();
      },
      (message) => {
        if (sessionState !== "active") return;
        teamActionError = message;
        terminalActionError = message;
      }
    );
  }

  function mergeStagePrompt(existingText: string, stageText: string): string {
    const trimmedExisting = existingText.trimEnd();
    if (trimmedExisting.trim() === "") {
      return stageText;
    }
    if (stageText === "" || trimmedExisting.includes(stageText)) {
      return existingText;
    }
    return trimmedExisting + "\n\n" + stageText;
  }

  // openConversationSurface reveals the Conversation dock/sheet and clears unread badges.
  function openConversationSurface(invoker: HTMLElement | null = null) {
    conversationOpen = true;
    userCollapsedConversation = false;
    seenMessageCount = teamConversation?.messages.length ?? seenMessageCount;
    if (isNarrowViewport()) {
      mobileSurface = "conversation";
      mobileSurfaceInvoker = invoker;
    }
    syncHistoryState("replace");
  }

  // focusComposerForRole opens the conversation dock/sheet targeting role,
  // detaching Guide-only contexts if switching away from Guide or starting an explicit reply.
  function focusComposerForRole(
    role: ComposerRecipient,
    context: ComposerContext | null = null,
    suggestedText = "",
    explicitReplyTo?: number | null
  ) {
    openConversationSurface();
    composerDetachedNotice = null;
    const switchingAwayFromGuide = role !== "guide" && composerDraft.to === "guide";
    const stripSeeded = switchingAwayFromGuide || explicitReplyTo !== undefined;
    const baseText = stripSeeded
      ? stripSeededBoilerplate(composerDraft.text, composerDraft.seededText)
      : composerDraft.text;
    const nextContext =
      explicitReplyTo !== undefined
        ? context
        : context !== null
        ? context
        : role !== "guide" && contextRequiresGuide(composerDraft.context)
        ? null
        : composerDraft.context;
    if (
      explicitReplyTo === undefined &&
      context === null &&
      role !== "guide" &&
      (contextRequiresGuide(composerDraft.context) || composerDraft.replyTo !== null)
    ) {
      composerDetachedNotice =
        "Detached the previous Guide attachment when switching to " +
        role.replaceAll("_", " ") +
        ". Your typed message is preserved.";
    }
    composerDraft = {
      to: role,
      text: suggestedText !== "" ? mergeStagePrompt(baseText, suggestedText) : baseText,
      replyTo:
        explicitReplyTo !== undefined
          ? explicitReplyTo
          : context !== null || switchingAwayFromGuide
          ? null
          : composerDraft.replyTo,
      context: nextContext,
      seededText:
        suggestedText !== ""
          ? suggestedText
          : stripSeeded
          ? undefined
          : composerDraft.seededText,
    };
    syncAllRegions();
    document.getElementById("highbay-composer-input")?.focus();
  }

  // handleReplyToConversationMessage attaches msg.seq in ConversationRail while
  // detaching Guide-only contexts and seeded boilerplate when replying to another role.
  function handleReplyToConversationMessage(
    seq: number,
    senderRole: TeamConversation["messages"][number]["sender_role"]
  ) {
    const nextTo: ComposerRecipient =
      senderRole === "builder"
        ? "builder"
        : senderRole === "test_writer"
        ? "test_writer"
        : senderRole === "guide"
        ? "guide"
        : composerDraft.to;
    let detached = false;
    let nextContext = composerDraft.context;
    let nextText = composerDraft.text;
    let nextSeeded = composerDraft.seededText;
    if (nextTo !== "guide") {
      if (contextRequiresGuide(nextContext)) {
        nextContext = null;
        detached = true;
      }
      if (nextSeeded !== undefined) {
        nextText = stripSeededBoilerplate(nextText, nextSeeded);
        nextSeeded = undefined;
        detached = true;
      }
    }
    composerDetachedNotice = detached
      ? "Detached the Guide-only revision attachment when switching recipient. Your message text is preserved."
      : null;
    composerDraft = {
      to: nextTo,
      text: nextText,
      replyTo: seq,
      context: nextContext,
      seededText: nextSeeded,
    };
    syncAllRegions();
    document.getElementById("highbay-composer-input")?.focus();
  }

  // handleSwitchComposerRecipient detaches Guide-only contexts/replies and
  // action-seeded boilerplate while preserving user-typed text and informative contexts.
  function handleSwitchComposerRecipient(nextTo: ComposerRecipient) {
    if (nextTo === composerDraft.to) {
      return;
    }
    let detached = false;
    let nextContext = composerDraft.context;
    let nextReplyTo = composerDraft.replyTo;
    let nextText = composerDraft.text;
    let nextSeeded = composerDraft.seededText;

    if (nextTo !== "guide") {
      if (contextRequiresGuide(nextContext)) {
        nextContext = null;
        detached = true;
      }
      if (nextReplyTo !== null) {
        const replied = (teamConversation?.messages ?? []).find((m) => m.seq === nextReplyTo);
        if (replied === undefined || replied.sender_role === "guide") {
          nextReplyTo = null;
          detached = true;
        }
      }
      if (nextSeeded !== undefined) {
        nextText = stripSeededBoilerplate(nextText, nextSeeded);
        nextSeeded = undefined;
        detached = true;
      }
    }

    composerDetachedNotice = detached
      ? "Detached the Guide-only revision attachment when switching recipient. Your message text is preserved."
      : null;
    composerDraft = {
      to: nextTo,
      text: nextText,
      replyTo: nextReplyTo,
      context: nextContext,
      seededText: nextSeeded,
    };
    syncAllRegions();
  }

  // latestRevisionTargetForChange returns the newest observed revision + ambiguities
  // for changeId, preferring incomingCard and unseen server-listed revisions over stale proposals.
  function latestRevisionTargetForChange(
    changeId: string
  ): {
    revision: string;
    ambiguities: readonly AmbiguityQuestion[] | undefined;
    knownAmbiguities: boolean;
  } | null {
    const listed = changeList?.changes.find((c) => c.change_id === changeId);
    const listedRev =
      listed !== undefined ? listed.proposed_revision ?? listed.request_revision : null;
    const seen = seenCardRevisionsByChangeId.get(changeId);
    const ed = editorsByChangeId.get(changeId);
    if (ed !== undefined) {
      if (listedRev !== null && !(seen?.has(listedRev) ?? false)) {
        return {
          revision: listedRev,
          ambiguities: undefined,
          knownAmbiguities: false,
        };
      }
      const newestCard = ed.incomingCard ?? ed.card;
      if (newestCard.proposed_revision !== undefined) {
        return {
          revision: newestCard.proposed_revision.request_revision,
          ambiguities: newestCard.proposed_revision.ambiguities,
          knownAmbiguities: true,
        };
      }
      return {
        revision: newestCard.request_revision,
        ambiguities: newestCard.ambiguities,
        knownAmbiguities: true,
      };
    }
    if (listedRev !== null) {
      return {
        revision: listedRev,
        ambiguities: undefined,
        knownAmbiguities: false,
      };
    }
    return null;
  }

  // submitTeamMessage posts a message to Chorus through Highbay, separating
  // known pre-post 4xx refusals from uncertain transport/5xx/invalid-2xx outcomes.
  function submitTeamMessage(
    source: "rail" | "describe" | "agree",
    payload: SendTeamMessagePayload
  ) {
    if (
      sessionState !== "active" ||
      hasPending("send_team_message", source) ||
      payload.text.trim() === ""
    ) {
      return;
    }
    const needsGuide =
      (payload.to === "guide" || payload.to === "team") && guideRoleNeedsStart(teamSnapshot);
    if (
      needsGuide &&
      (agentOptions === null ||
        guideChoice === undefined ||
        roleChoiceError(guideChoice, agentOptions.agents) !== null)
    ) {
      return;
    }
    const fullPayload: SendTeamMessagePayload = {
      ...payload,
      ...(needsGuide && guideChoice !== undefined ? { guide_choice: guideChoice } : {}),
    };
    if (source === "describe") {
      describeSendError = null;
      describeSendUncertain = false;
    } else {
      composerSendError = null;
      composerDetachedNotice = null;
    }
    callServer(
      "send_team_message",
      fullPayload,
      source,
      (posted) => {
        if (sessionState !== "active") return;
        if (source === "describe") {
          describeText = "";
          describeSendUncertain = false;
          describeSendError = null;
          draftSentMessageId = posted.id;
          syncHistoryState("replace");
        } else if (source === "rail") {
          composerDraft = { to: composerDraft.to, text: "", replyTo: null, context: null };
        }
        if (
          teamConversation !== null &&
          !teamConversation.messages.some((m) => m.id === posted.id)
        ) {
          teamConversation = {
            ...teamConversation,
            messages: [...teamConversation.messages, posted],
          };
        }
        statusEl.textContent = "Message sent to " + payload.to.replaceAll("_", " ") + ".";
        readConversation();
        readTeam();
      },
      (message, failure) => {
        if (sessionState !== "active") return;
        const knownPrePostRefusal = failure.status >= 400 && failure.status < 500;
        if (source === "describe") {
          describeSendUncertain = !knownPrePostRefusal;
          describeSendError = message;
          if (!knownPrePostRefusal) {
            readConversation();
            readTeam();
          }
        } else {
          if (source === "agree") {
            composerDraft = {
              to: payload.to,
              text: payload.text,
              replyTo: payload.reply_to ?? null,
              context: payload.context ?? null,
              seededText: payload.text,
            };
          }
          openConversationSurface();
          composerSendError = knownPrePostRefusal
            ? message
            : "Send not confirmed — check Conversation before sending again. " + message;
          if (!knownPrePostRefusal) {
            readConversation();
            readTeam();
          }
        }
        statusEl.textContent = knownPrePostRefusal
          ? "Could not send message. " + message
          : "Send not confirmed — check Conversation before sending again.";
      }
    );
  }

  function openLive(options?: { historyMode?: "push" | "replace"; mobileSurface?: MobileSurface }) {
    navigationSeq++;
    mobileSurface = options?.mobileSurface ?? "stage";
    switchComposerScope(composerScopeFor("live", activeChangeId));
    currentView = { kind: "live" };
    statusEl.textContent = "Showing the live target and the model connection.";
    if (liveTarget === null || liveTargetStale) {
      readLiveTarget();
    }
    readModelConnection();
    syncHistoryState(options?.historyMode ?? (handlingPopState ? "replace" : "push"));
    syncAllRegions();
  }

  function readLiveTarget() {
    if (hasPending("get_live_target", "")) {
      liveReadAgain = true;
      return;
    }
    liveReadAgain = false;
    liveTargetStale = false;
    liveReadCount += 1;
    const read = liveReadCount;
    const done = () => {
      if (liveReadAgain) {
        readLiveTarget();
      }
    };
    callServer(
      "get_live_target",
      {},
      "",
      (target) => {
        liveTarget = target;
        liveTargetError = null;
        if (liveStopOutcome !== null && read > liveStopAfterRead) {
          const key = liveTargetKey(target);
          if (liveStopBasis === null) {
            liveStopBasis = key;
          } else if (liveStopBasis !== key) {
            liveStopOutcome = null;
            liveStopBasis = null;
          }
        }
        done();
      },
      (message) => {
        liveTargetError = message;
        liveTargetStale = true;
        if (currentView.kind === "live") {
          statusEl.textContent = "Could not read the live target.";
        }
        done();
      }
    );
  }

  function readModelConnection() {
    if (hasPending("get_model_connection", "")) {
      return;
    }
    callServer(
      "get_model_connection",
      {},
      "",
      (connection) => {
        modelConnection = connection;
        modelConnectionError = null;
      },
      (message) => {
        modelConnectionError = message;
      }
    );
  }

  function stopLiveTarget() {
    if (hasPending("stop_live_target", "")) {
      return;
    }
    liveStopOutcome = null;
    callServer(
      "stop_live_target",
      {},
      "",
      (result) => {
        showStopAnswer({ message: result.message, refused: false });
      },
      (message) => {
        showStopAnswer({ message, refused: true });
      }
    );
  }

  function showStopAnswer(answer: ServerAnswer) {
    liveStopOutcome = answer;
    liveStopBasis = null;
    liveStopAfterRead = liveReadCount;
    readLiveTarget();
  }

  function updateModelDraft(change: Partial<ModelDraft>) {
    const redraw = !modelDraftDirty || change.kind !== undefined;
    modelDraft = { ...modelDraft, ...change };
    modelDraftDirty = true;
    if (redraw) {
      syncViewRegion();
      syncNoticeRegion();
    }
  }

  function discardModelDraft() {
    if (hasPending("configure_model", "")) return;
    modelDraft = { kind: null, provider: "anthropic", modelName: "", gatewayAddr: "" };
    modelDraftDirty = false;
    modelDraftReset++;
    syncAllRegions();
    document.getElementById("highbay-model-title")?.focus();
  }

  function applyModel(choice: ConfigureModelRequest) {
    if (hasPending("configure_model", "")) {
      return;
    }
    modelApplyOutcome = null;
    callServer(
      "configure_model",
      choice,
      "",
      (result) => {
        modelDraftDirty = false;
        modelApplyOutcome = { message: describeApplied(result), refused: false };
        readModelConnection();
      },
      (message) => {
        modelApplyOutcome = { message, refused: true };
      }
    );
  }

  // openArchitecture opens Architecture: if unconfirmed, shows the Prerequisite
  // Card with zero 409 POST /changes/preview RPC; if confirmed, previews the design.
  function openArchitecture() {
    const ed = getActiveEditor();
    if (!ed) {
      return;
    }
    navigationSeq++;
    mobileSurface = "stage";
    const card = ed.card;
    currentView = { kind: "change", changeId: card.change_id, stage: "arch" };
    if (card.status !== "confirmed") {
      statusEl.textContent = "Confirm the Change Card in Agree before previewing its architecture.";
    } else if (
      previewState === null ||
      previewState.change_id !== card.change_id ||
      previewState.request_revision !== card.request_revision
    ) {
      requestArchitecture(card);
    } else {
      statusEl.textContent = "Showing architecture preview for " + previewState.system_name + ".";
    }
    syncHistoryState("push");
    syncAllRegions();
  }

  function requestArchitecture(card: ChangeCardResult) {
    if (card.status !== "confirmed" || hasPending("preview_architecture", card.change_id)) {
      return;
    }
    previewErrors.delete(card.change_id);
    statusEl.textContent = "Evaluating the design for Change Card " + card.change_id + "...";
    callServer(
      "preview_architecture",
      { change_id: card.change_id, request_revision: card.request_revision },
      card.change_id,
      (preview) => {
        if (preview.change_id !== card.change_id) {
          previewErrors.set(
            card.change_id,
            "The server returned the architecture of Change Card " + preview.change_id + "."
          );
          return;
        }
        previewState = preview;
        if (currentView.kind === "change" && currentView.stage === "arch") {
          statusEl.textContent = "Showing architecture preview for " + preview.system_name + ".";
        }
      },
      (message) => {
        previewErrors.set(card.change_id, message);
        if (currentView.kind === "change" && currentView.stage === "arch") {
          statusEl.textContent = "Could not preview the architecture.";
        }
      }
    );
  }

  function openProof() {
    const changeId = activeChangeId ?? selectedVerificationChangeId;
    if (!changeId) {
      return;
    }
    navigationSeq++;
    mobileSurface = "stage";
    if (selectedVerificationChangeId === changeId && lastLoadedListByChangeId.has(changeId)) {
      currentView = { kind: "change", changeId, stage: "verify" };
      statusEl.textContent =
        selectedRunId !== null
          ? "Showing the saved verification result."
          : "Showing saved verification runs for Change Card " + changeId + ".";
      if (selectedRunId !== null) readVerification(selectedRunId, changeId);
      syncHistoryState("push");
      syncAllRegions();
      return;
    }
    handleListVerifications(changeId);
  }

  // resolvePrimaryAttentionRole orders needs_you roles by stage affinity, then
  // guide -> builder -> test_writer, returning the first role whose interactive
  // terminal is not yet open.
  function resolvePrimaryAttentionRole(): TeamRoleState["role"] | null {
    const roles = teamSnapshot?.roles ?? [];
    const buildCentric =
      currentView.kind === "live" ||
      (currentView.kind === "change" &&
        (currentView.stage === "build" ||
          currentView.stage === "verify" ||
          currentView.stage === "approve"));
    const order: readonly TeamRoleState["role"][] = buildCentric
      ? ["builder", "test_writer", "guide"]
      : ["guide", "builder", "test_writer"];
    for (const roleName of order) {
      const role = roles.find((item) => item.role === roleName && item.state === "needs_you");
      if (
        role !== undefined &&
        !(role.has_terminal && openTerminalRole === role.role && !terminalObserveMode)
      ) {
        return role.role;
      }
    }
    return null;
  }

  // resolvePrimaryActionId computes the single page-wide control ID permitted
  // to carry variant="primary" (data-primary-action="true").
  function resolvePrimaryActionId(): {
    attentionRole: TeamRoleState["role"] | null;
    actionId: string | null;
  } {
    const narrow = isNarrowViewport();
    const attentionRole = resolvePrimaryAttentionRole();
    if (attentionRole !== null) {
      if (
        openTerminalRole === attentionRole &&
        terminalObserveMode &&
        (!narrow || mobileSurface === "terminal")
      ) {
        return { attentionRole: null, actionId: "highbay-terminal-interactive" };
      }
      return { attentionRole, actionId: "attention:" + attentionRole };
    }
    if (narrow && mobileSurface === "conversation") {
      return { attentionRole: null, actionId: "highbay-composer-send" };
    }
    if (narrow && mobileSurface === "terminal") {
      const roleState =
        openTerminalRole !== null
          ? teamSnapshot?.roles.find((r) => r.role === openTerminalRole) ?? null
          : null;
      const canRestart =
        roleState !== null &&
        !roleState.has_terminal &&
        (roleState.state === "not_started" ||
          roleState.state === "ended" ||
          roleState.state === "error");
      return {
        attentionRole: null,
        actionId: canRestart ? "highbay-terminal-restart" : null,
      };
    }

    if (currentView.kind === "new" || (currentView.kind === "changes" && changeList !== null && changeList.changes.length === 0 && listError === null && (lastOpenAttempt === null || !cardLoadErrors.has(lastOpenAttempt)))) {
      const isFirst = currentView.kind === "changes";
      const guide = teamSnapshot?.roles.find((r) => r.role === "guide");
      const sentMsg =
        draftSentMessageId !== undefined
          ? (teamConversation?.messages ?? []).find((m) => m.id === draftSentMessageId) ?? null
          : null;
      const hasLinkedReply =
        guide?.state === "waiting" &&
        sentMsg !== null &&
        teamConversation !== null &&
        teamConversation.messages.some(
          (m) =>
            m.target === teamConversation!.channel &&
            m.sender_role === "guide" &&
            m.reply_to === sentMsg.seq
        );
      if (!describeSendUncertain && hasLinkedReply) {
        return { attentionRole: null, actionId: "highbay-draft-reply" };
      }
      if (
        !describeSendUncertain &&
        draftSentMessageId !== undefined &&
        !hasPending("get_team", "") &&
        teamSnapshot !== null &&
        (guide === undefined ||
          guide.state === "ended" ||
          guide.state === "error" ||
          guide.state === "not_started")
      ) {
        return { attentionRole: null, actionId: "highbay-draft-restart-guide" };
      }
      if (!describeSendUncertain && isFirst && changeList?.draft_state === "story_failed") {
        return { attentionRole: null, actionId: "highbay-draft-reply" };
      }
      return { attentionRole: null, actionId: "highbay-describe-submit" };
    }

    if (currentView.kind === "changes") {
      return {
        attentionRole: null,
        actionId: changeList !== null && changeList.changes.length > 0 ? "highbay-new-change" : null,
      };
    }

    if (currentView.kind === "team") {
      const err = teamFactsError();
      if (err !== null) {
        return { attentionRole: null, actionId: "highbay-team-refresh" };
      }
      const needsStart =
        teamSnapshot !== null &&
        teamSnapshot.roles.some(
          (r) => r.role !== "guide" && (r.state === "not_started" || r.state === "ended")
        );
      return {
        attentionRole: null,
        actionId: needsStart && teamWorkersHaveWork() ? "highbay-team-start" : null,
      };
    }

    if (currentView.kind === "live") {
      return { attentionRole: null, actionId: "highbay-model-apply" };
    }

    if (currentView.kind === "story") {
      return { attentionRole: null, actionId: null };
    }

    // currentView.kind === "change"
    const ed = getActiveEditor();
    if (currentView.stage === "agree" && ed !== null) {
      const proposal = ed.card.proposed_revision;
      if (proposal !== undefined) {
        const needsProposalRev =
          answersNeedingRevision(proposal.ambiguities, ed.proposalAnswers).length > 0;
        return {
          attentionRole: null,
          actionId: needsProposalRev
            ? "highbay-proposal-revision-needed-send"
            : "highbay-proposal-adopt",
        };
      }
      if (answersNeedingRevision(ed.card.ambiguities, ed.draftAnswers).length > 0) {
        return { attentionRole: null, actionId: "highbay-confirm-revision-needed-send" };
      }
      if (ed.card.status === "needs_confirmation") {
        if (unansweredQuestions(ed).length > 0) {
          return { attentionRole: null, actionId: "highbay-cp1-next-question" };
        }
        if (firstIncompleteExample(ed) !== null) {
          return { attentionRole: null, actionId: "highbay-cp1-next-example" };
        }
        return { attentionRole: null, actionId: "highbay-cp1-next-confirm" };
      }
      if (isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples)) {
        return { attentionRole: null, actionId: "highbay-confirm-submit" };
      }
      return { attentionRole: null, actionId: "highbay-agree-continue-build" };
    }

    if (currentView.stage === "arch" && ed !== null) {
      return {
        attentionRole: null,
        actionId:
          ed.card.status !== "confirmed"
            ? "highbay-prerequisite-go-agree"
            : "highbay-arch-continue-build",
      };
    }

    if (currentView.stage === "build" && ed !== null) {
      const saved =
        buildChangeId === ed.card.change_id && buildSnapshot?.state === "started"
          ? buildSnapshot
          : null;
      if (
        ed.card.status !== "confirmed" &&
        saved === null &&
        !hasPending("get_build", ed.card.change_id) &&
        buildReadError === null
      ) {
        return { attentionRole: null, actionId: "highbay-prerequisite-go-agree" };
      }
      if (hasPending("merge_change", ed.card.change_id)) {
        return { attentionRole: null, actionId: "highbay-build-merge" };
      }
      if (
        ed.incomingCard === null &&
        sessionState === "active" &&
        getEditorRequest(ed.card.change_id) === null &&
        !cardLoadErrors.has(ed.card.change_id)
      ) {
        const proposal = ed.card.proposed_revision;
        const revNeeded =
          proposal !== undefined
            ? answersNeedingRevision(proposal.ambiguities, ed.proposalAnswers).length > 0
            : answersNeedingRevision(ed.card.ambiguities, ed.draftAnswers).length > 0;
        if (revNeeded) {
          return { attentionRole: null, actionId: "highbay-review-revision-request" };
        }
        if (proposal !== undefined && ed.card.status === "confirmed") {
          return { attentionRole: null, actionId: "highbay-review-proposal" };
        }
      }
      if (saved !== null && saved.in_force && saved.taking.state === "merged") {
        return { attentionRole: null, actionId: "highbay-new-change" };
      }
      if (saved !== null && saved.in_force && saved.taking.state === "passed") {
        const approved = buildApprovalRun(ed.card);
        if (approved !== null && mergeControls(approved).eligible) {
          return { attentionRole: null, actionId: "highbay-build-merge" };
        }
      }
      const latestCheck = latestVerificationFor(ed.card.change_id);
      const report =
        latestCheck?.state.type === "completed" ? latestCheck.state.value.report : null;
      const matchedCheck =
        latestCheck !== null &&
        runInputsKnown(latestCheck) &&
        latestCheck.inputs_current &&
        latestCheck.change_id === ed.card.change_id &&
        latestCheck.request_revision === ed.card.request_revision;
      const recordedCheck =
        report !== null && ed.card.approval?.report_digest === report.report_digest;
      const open =
        saved !== null && (saved.taking.state === "building" || saved.taking.state === "passed");
      const current = saved !== null && saved.in_force;
      const replay = current && saved.taking.state === "merged";
      const currentRev =
        changeList?.changes.find((item) => item.change_id === ed.card.change_id)?.request_revision ??
        ed.incomingCard?.request_revision ??
        ed.card.request_revision;
      const displayedCurrent =
        currentRev === ed.card.request_revision &&
        (!current || saved.request_revision === ed.card.request_revision);
      const dirty = isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples);
      const otherPrimary =
        ed.card.status !== "confirmed" ||
        !displayedCurrent ||
        dirty ||
        ed.card.proposed_revision !== undefined;
      const sentBack = open && current && saved.test_writer.tests.state === "needs_changes";
      const approvalAvail = approvalUnavailable === null && changeList?.approval_available === true;
      const reviewPrimary =
        open &&
        approvalAvail &&
        !otherPrimary &&
        !sentBack &&
        matchedCheck &&
        report?.passed === true &&
        !recordedCheck;
      if (reviewPrimary) {
        return { attentionRole: null, actionId: "highbay-build-review-check" };
      }
      const showDirect =
        (!current || !open) &&
        !replay &&
        buildDraftedBeforeDrop === false &&
        ed.card.status === "confirmed" &&
        displayedCurrent &&
        buildSnapshot !== null;
      if (showDirect && !otherPrimary && !sentBack) {
        return { attentionRole: null, actionId: "highbay-build-start" };
      }
      return { attentionRole: null, actionId: null };
    }

    if (currentView.stage === "verify") {
      const displayedRun = resolveDisplayedVerificationRun();
      if (
        displayedRun !== null &&
        (displayedRun.state.type === "running" ||
          runRequestErrors.has(displayedRun.run_id) ||
          !runInputsKnown(displayedRun))
      ) {
        return { attentionRole: null, actionId: "highbay-cp4-check-status" };
      }
      return { attentionRole: null, actionId: "highbay-verify-review-result" };
    }

    if (currentView.stage === "approve") {
      const run = resolveDisplayedVerificationRun();
      const review = displayedApprovalReview();
      if (run !== null && hasPending("merge_change", run.change_id)) {
        return { attentionRole: null, actionId: "highbay-cp5-merge" };
      }
      if (review?.approval.state === "available" && run !== null) {
        return { attentionRole: null, actionId: "highbay-cp5-approve" };
      }
      if (review?.approval.state === "recorded" && run !== null) {
        const build = knownTakingBuild(run.change_id);
        if (build !== null && build.in_force && runBelongsToBuild(run.change_id, build, run)) {
          if (build.taking.state === "passed") {
            return { attentionRole: null, actionId: "highbay-cp5-merge" };
          }
          if (build.taking.state === "merged") {
            return { attentionRole: null, actionId: "highbay-new-change" };
          }
        }
      }
      const newer = run !== null ? newerCheckFor(run) : null;
      if (newer !== null && review?.approval.state !== "available" && review?.approval.state !== "recorded") {
        return { attentionRole: null, actionId: "highbay-cp5-review-latest" };
      }
      return { attentionRole: null, actionId: null };
    }

    return { attentionRole: null, actionId: null };
  }

  // computeJourneySteps computes the 5-step Change Stepper statuses and glyphs.
  function computeJourneySteps(): JourneyStep[] {
    const ed = getActiveEditor();
    const targetChangeId = ed?.card.change_id ?? selectedVerificationChangeId;
    if (targetChangeId === null) {
      return [];
    }
    const activeStage =
      currentView.kind === "change" && currentView.changeId === targetChangeId
        ? currentView.stage
        : null;
    const agreeUnsaved = ed !== null && isEditorDirtyState(ed);
    const agreeAttention =
      ed !== null &&
      (ed.card.proposed_revision !== undefined ||
        ed.incomingCard !== null ||
        answersNeedingRevision(ed.card.ambiguities, ed.draftAnswers).length > 0 ||
        ed.formError !== null);
    const agreeDone = ed !== null && ed.card.status === "confirmed" && !agreeAttention;

    const inForce = ed !== null ? currentInForceBuild(ed.card) : null;
    const savedBuild =
      ed !== null && buildChangeId === ed.card.change_id && buildSnapshot?.state === "started"
        ? buildSnapshot
        : null;
    const buildDone =
      inForce !== null && (inForce.taking.state === "passed" || inForce.taking.state === "merged");
    const buildAttention =
      !buildDone &&
      ed !== null &&
      (buildReadError !== null ||
        ed.card.proposed_revision !== undefined ||
        (inForce !== null && inForce.test_writer.tests.state === "could_not_run"));

    const latestRun = latestVerificationFor(targetChangeId);
    const verifyDone =
      ed !== null &&
      inForce !== null &&
      latestRun !== null &&
      runInputsKnown(latestRun) &&
      latestRun.inputs_current &&
      runBelongsToBuild(ed.card.change_id, inForce, latestRun);
    const verifyAttention =
      !verifyDone &&
      ed !== null &&
      inForce !== null &&
      latestRun !== null &&
      runInputsKnown(latestRun) &&
      latestRun.inputs_current &&
      runStartedInCurrentBuild(ed.card, inForce, latestRun) &&
      (latestRun.state.type === "not_completed" ||
        (latestRun.state.type === "completed" && !latestRun.state.value.report.passed));

    const latestReview = latestRun !== null ? reviewForRun(latestRun.run_id) : null;
    const approveDone =
      verifyDone &&
      latestRun !== null &&
      latestReview !== null &&
      approvalReviewFresh(latestRun) &&
      latestReview.approval.state === "recorded";
    const merged = inForce?.taking.state === "merged";

    const steps: JourneyStep[] = [];
    if (ed !== null) {
      steps.push({
        id: "highbay-nav-cp1",
        label: "Agree",
        active: activeStage === "agree",
        status: agreeAttention ? "attention" : agreeDone ? "done" : "neutral",
        glyph: agreeAttention ? "!" : agreeUnsaved ? "Unsaved" : agreeDone ? "✓" : undefined,
        srStatus: agreeAttention
          ? "needs attention"
          : agreeUnsaved
          ? "unsaved edits"
          : agreeDone
          ? "completed"
          : undefined,
        unsaved: agreeUnsaved,
        onClick: handleReturnToChangeCard,
      });
      steps.push({
        id: "highbay-nav-cp2",
        label: "Architecture",
        active: activeStage === "arch",
        status: "neutral",
        glyph: ed.card.status !== "confirmed" ? "(After Agree)" : "Optional",
        srStatus: ed.card.status !== "confirmed" ? "available after Agree" : "optional",
        onClick: openArchitecture,
      });
      steps.push({
        id: "highbay-nav-build",
        label: "Build",
        active: activeStage === "build",
        status: buildAttention ? "attention" : buildDone ? "done" : "neutral",
        glyph:
          ed.card.status !== "confirmed" && savedBuild === null
            ? "(After Agree)"
            : buildAttention
            ? "!"
            : buildDone
            ? "✓"
            : undefined,
        srStatus:
          ed.card.status !== "confirmed" && savedBuild === null
            ? "available after Agree"
            : buildAttention
            ? "needs attention"
            : buildDone
            ? "completed"
            : undefined,
        onClick: () => {
          showChangeEditor("build");
          focusResult("#highbay-build-title, #highbay-prerequisite-title");
        },
      });
    }
    steps.push({
      id: "highbay-nav-cp4",
      label: "Verify",
      active: activeStage === "verify",
      status: verifyAttention ? "attention" : verifyDone ? "done" : "neutral",
      glyph: verifyAttention ? "!" : verifyDone ? "✓" : undefined,
      srStatus: verifyAttention ? "needs attention" : verifyDone ? "completed" : undefined,
      onClick: openProof,
    });
    steps.push({
      id: "highbay-nav-cp5",
      label: "Approve",
      active: activeStage === "approve",
      status: approveDone || merged ? "done" : "neutral",
      glyph: merged ? "Merged" : approveDone ? "✓" : undefined,
      srStatus: merged ? "merged" : approveDone ? "completed" : undefined,
      onClick: openApproval,
    });
    return steps;
  }

  // describeDisplayedSubjects summarizes the single active stage for Current view details.
  function describeDisplayedSubjects(): WorkspaceSubjectDescriptor[] {
    const ed = getActiveEditor();
    const out: WorkspaceSubjectDescriptor[] = [];
    if (currentView.kind === "change") {
      if (currentView.stage === "agree" && ed !== null) {
        out.push({
          role: "editor",
          label: "Showing",
          stage: "Agree",
          changeId: ed.card.change_id,
          requestRevision: ed.card.request_revision,
          detail: ed.card.status === "confirmed" ? "confirmed" : "needs confirmation",
        });
        if (ed.incomingCard !== null) {
          out.push({
            role: "incoming",
            label: "Incoming card",
            stage: "Updated Change Card",
            changeId: ed.incomingCard.change_id,
            requestRevision: ed.incomingCard.request_revision,
            detail: ed.incomingCard.status === "confirmed" ? "confirmed" : "needs confirmation",
          });
        }
        return out;
      }
      if (currentView.stage === "build" && ed !== null) {
        out.push({
          role: "editor",
          label: "Showing",
          stage: "Build",
          changeId: ed.card.change_id,
          requestRevision: ed.card.request_revision,
          detail: ed.card.status === "confirmed" ? "confirmed" : "needs confirmation",
        });
        return out;
      }
      if (currentView.stage === "arch") {
        const shownPreview =
          previewState !== null && ed !== null && previewState.change_id === ed.card.change_id
            ? previewState
            : null;
        if (shownPreview !== null) {
          out.push({
            role: "result",
            label: "Showing",
            stage: "Architecture · " + shownPreview.system_name,
            changeId: shownPreview.change_id,
            requestRevision: shownPreview.request_revision,
            detail: "design " + shownPreview.design_digest.slice(0, 15),
          });
        }
        return out;
      }
      const displayedRun = resolveDisplayedVerificationRun();
      const stageLabel = currentView.stage === "approve" ? "Approve" : "Verify";
      if (displayedRun !== null) {
        out.push({
          role: "result",
          label: "Showing",
          stage: stageLabel,
          changeId: displayedRun.change_id,
          requestRevision: displayedRun.request_revision,
          detail:
            "run " +
            displayedRun.run_id.slice(0, 8) +
            " · commit " +
            displayedRun.candidate_commit.slice(0, 7),
        });
      } else {
        out.push({
          role: "result",
          label: "Showing",
          stage: stageLabel,
          changeId: selectedVerificationChangeId || undefined,
          detail: "saved verification runs",
        });
      }
      return out;
    }
    if (currentView.kind === "story") {
      let draft = "current draft not read yet";
      if (storyEvaluation !== null) {
        draft = "current draft " + storyEvaluation.state;
        if (storyEvaluation.snapshot_digest) {
          draft += " · snapshot " + storyEvaluation.snapshot_digest.slice(0, 15);
        }
      }
      out.push({
        role: "result",
        label: "Showing",
        stage: "Story",
        changeId: ed?.card.change_id,
        requestRevision: ed?.card.request_revision,
        detail: draft,
      });
      return out;
    }
    if (currentView.kind === "team") {
      out.push({
        role: "result",
        label: "Showing",
        stage: "Project team",
        detail: teamSnapshot?.team,
      });
      return out;
    }
    return out;
  }

  function syncNavRegion(primaryActionId: string | null) {
    const approvalWasFocused = document.activeElement?.id === "highbay-cp5-approve";
    if (sessionState === "starting" || sessionState === "signed-out") {
      navEl.hidden = true;
      flushSync(() => {
        navRoot.render(null);
      });
      return;
    }

    const ed = getActiveEditor();
    const activeEditorReq = getEditorRequest(ed?.card.change_id ?? null);
    const onChanges = currentView.kind === "changes";
    const hasChangesOrEditor =
      changeList !== null && (changeList.changes.length > 0 || editorsByChangeId.size > 0);
    const changesLabel = hasChangesOrEditor ? "Changes" : "Describe";
    const changesNav =
      !onChanges || changeList === null || hasChangesOrEditor
        ? {
            id: "highbay-nav-changes",
            label: changesLabel,
            active: onChanges,
            onClick: showChanges,
          }
        : null;

    const newChangeNav =
      changeList !== null && changeList.changes.length > 0
        ? {
            active: currentView.kind === "new",
            disabled: sessionState !== "active",
            primary: primaryActionId === "highbay-new-change",
            onClick: openNewChange,
          }
        : null;

    // Team stays first inside Inspect so Tab from Inspect lands on #highbay-nav-team.
    const references = [
      {
        id: "highbay-nav-team",
        label: "Team",
        active: currentView.kind === "team",
        onClick: openTeam,
      },
      {
        id: "highbay-nav-story",
        label: "Story",
        active: currentView.kind === "story",
        onClick: openStory,
      },
      {
        id: "highbay-nav-live",
        label: "Live target",
        active: currentView.kind === "live",
        onClick: openLive,
      },
    ];

    const journey = currentView.kind !== "new" ? computeJourneySteps() : [];

    const subjects = describeDisplayedSubjects();
    const run = resolveDisplayedVerificationRun();
    const review = displayedApprovalReview();
    const onApproval = currentView.kind === "change" && currentView.stage === "approve";
    const onAgree = currentView.kind === "change" && currentView.stage === "agree";
    const onBuild = currentView.kind === "change" && currentView.stage === "build";
    const onArch = currentView.kind === "change" && currentView.stage === "arch";
    const onVerify = currentView.kind === "change" && currentView.stage === "verify";

    const fresh = run !== null && approvalReviewFresh(run);
    const teamError = teamFactsError();
    const teamLoading = hasPending("get_team", "");
    const teamStarting = hasPending("start_team_agents", "");
    const teamRoles = teamSnapshot?.roles ?? [];
    const guide = connectedGuide();
    const guideHasTerminal = guide?.has_terminal ?? false;
    const teamNeedsYou = teamRoles.find((role) => role.state === "needs_you");
    const teamStatusUnknown = teamRoles.some(
      (role) => role.state === "error" || (role.state === "unknown" && (role.role !== "guide" || guideHasTerminal))
    );

    const proposal = ed?.card.proposed_revision;
    const revisionTarget =
      ed === null
        ? null
        : proposal !== undefined
        ? answersNeedingRevision(proposal.ambiguities, ed.proposalAnswers).length > 0
          ? "#highbay-proposal-revision-needed"
          : null
        : answersNeedingRevision(ed.card.ambiguities, ed.draftAnswers).length > 0
        ? "#highbay-confirm-revision-needed"
        : null;

    const edHasUnsaved =
      ed !== null &&
      (isEditorDirtyState(ed) ||
        ed.formError !== null ||
        (activeEditorReq !== null && ROUTES[activeEditorReq].method !== "GET"));
    const showingInitialization =
      onChanges &&
      !edHasUnsaved &&
      listError === null &&
      changeList?.initializing_phase !== undefined &&
      changeList.changes.length === 0;
    const storyDraftAttention =
      changeList?.draft_state === "story_failed" && guide?.state !== "working";
    const hideEmptyWelcomeSlots =
      (onChanges || currentView.kind === "new") &&
      !edHasUnsaved &&
      listError === null &&
      (lastOpenAttempt === null || !cardLoadErrors.has(lastOpenAttempt)) &&
      (currentView.kind === "new" ||
        (changeList !== null &&
          changeList.changes.length === 0 &&
          !storyDraftAttention &&
          teamError === null &&
          !teamLoading &&
          !teamStarting &&
          teamSnapshot !== null &&
          teamNeedsYou === undefined &&
          !teamStatusUnknown &&
          (guide === undefined || guide.state !== "starting")));
    const showSlots = !showingInitialization && !hideEmptyWelcomeSlots;

    let attention = "Nothing now.";
    let next: React.ReactNode = null;
    if (onApproval) {
      attention =
        review?.approval.state === "blocked"
          ? "Approval unavailable."
          : review?.approval.state === "unverified"
          ? "Saved approval needs checking."
          : review?.approval.state === "recorded"
          ? "Your approval is recorded."
          : fresh
          ? "Decide whether this verified result may be promoted."
          : "Check the owner’s current approval requirements.";
      next =
        review?.approval.state === "available" && run !== null ? (
          <>
            <Button
              id="highbay-cp5-approve"
              variant={primaryActionId === "highbay-cp5-approve" ? "primary" : "secondary"}
              disabled={!fresh || activeEditorReq !== null}
              aria-describedby={!fresh ? "highbay-cp5-observation-status" : undefined}
              onClick={() => handleApprove(run)}
            >
              {activeEditorReq !== null ? "Approving…" : "Approve this result"}
            </Button>
            <span className="highbay-frame-action-binding">Run {run.run_id.slice(0, 12)}</span>
          </>
        ) : review?.approval.state === "recorded" ? (
          <span>Review the current build before merging.</span>
        ) : review?.approval.state === "blocked" ? (
          <Button onClick={() => focusResult("#highbay-cp5-approve-blocked")}>
            Review requirements
          </Button>
        ) : review?.approval.state === "unverified" ? (
          <span>Check current approval.</span>
        ) : (
          <span>Read current requirements.</span>
        );

      if (review?.approval.state === "recorded" && run !== null) {
        const build = knownTakingBuild(run.change_id);
        if (build === null) {
          next = (
            <Button
              disabled={activeEditorReq !== null || hasPending("get_build", run.change_id)}
              onClick={() => readBuild(run.change_id)}
            >
              Read current build
            </Button>
          );
        } else if (build.in_force && runBelongsToBuild(run.change_id, build, run)) {
          switch (build.taking.state) {
            case "merged":
              attention = "The approved build is merged.";
              next = <span>Describe the next change when you are ready.</span>;
              break;
            case "passed":
              next = (
                <MergePrimaryAction
                  id="highbay-cp5-merge"
                  primary={primaryActionId === "highbay-cp5-merge"}
                  controls={mergeControls(run)}
                />
              );
              break;
            case "dropped":
              attention = "This build was dropped. Your saved approval remains readable.";
              next = <span>Review the current agreement before building again.</span>;
              break;
          }
        }
      }
      const newer = run !== null ? newerCheckFor(run) : null;
      if (newer !== null) {
        if (review?.approval.state === "blocked") {
          attention = "A newer check is available.";
        }
        next = (
          <>
            <Button
              id="highbay-cp5-review-latest"
              variant={
                primaryActionId === "highbay-cp5-review-latest" ? "primary" : "default"
              }
              disabled={activeEditorReq !== null}
              onClick={() => reviewSavedCheck(newer)}
            >
              Review latest check
            </Button>
            {next}
          </>
        );
      }
      if (run !== null && hasPending("merge_change", run.change_id)) {
        attention = "Merging the approved result.";
        next = (
          <MergePrimaryAction
            id="highbay-cp5-merge"
            primary={primaryActionId === "highbay-cp5-merge"}
            controls={mergeControls(run)}
          />
        );
      }
    } else if (
      (onAgree || onBuild) &&
      ed !== null &&
      (revisionTarget !== null || (proposal !== undefined && ed.card.status === "confirmed")) &&
      ed.incomingCard === null &&
      sessionState === "active" &&
      activeEditorReq === null &&
      !cardLoadErrors.has(ed.card.change_id) &&
      !hasPending("merge_change", ed.card.change_id)
    ) {
      if (revisionTarget !== null) {
        attention = "Ask your coding agent to revise the draft for your selected answers.";
        next = (
          <Button
            id="highbay-review-revision-request"
            variant={
              primaryActionId === "highbay-review-revision-request" ? "primary" : "secondary"
            }
            onClick={() => {
              if (!onAgree) showChangeEditor("agree");
              focusResult(revisionTarget);
            }}
          >
            Review revision request
          </Button>
        );
      } else {
        attention = "Adopt or decline the proposed revision.";
        next = (
          <Button
            id="highbay-review-proposal"
            variant={primaryActionId === "highbay-review-proposal" ? "primary" : "secondary"}
            onClick={() => {
              if (!onAgree) showChangeEditor("agree");
              focusResult("#highbay-proposal");
            }}
          >
            Review proposed revision
          </Button>
        );
      }
    } else if (onBuild && ed !== null) {
      const build =
        buildChangeId === ed.card.change_id && buildSnapshot?.state === "started"
          ? buildSnapshot
          : null;
      if (buildReadError !== null && (ed.card.status === "confirmed" || build !== null)) {
        attention = "Current build facts could not be read.";
        next = <span>Refresh Build below to read its current state.</span>;
      } else if (build !== null) {
        const tests = build.test_writer.tests;
        if (!build.in_force) {
          attention = "This build belongs to an earlier agreement.";
          next = <span>Review the current agreement before deciding to build again.</span>;
        } else if (build.taking.state === "dropped") {
          attention = "This build was dropped.";
          next = <span>Your coding agent prepares a fresh revision before another build.</span>;
        } else if (build.taking.state === "merged") {
          attention = "The verified commits are merged.";
          next = <span>Describe the next change when you are ready.</span>;
        } else if (tests.state === "needs_changes") {
          attention =
            teamError !== null || teamSnapshot === null || teamStatusUnknown
              ? "Team status unknown."
              : "Nothing now.";
          next = <span>The test writer corrects the tests and reports again.</span>;
        } else if (tests.state === "could_not_run") {
          attention = "The tests could not be checked.";
          next = <span>{tests.step}</span>;
        } else if (build.taking.state === "passed") {
          const approved = buildApprovalRun(ed.card);
          attention =
            approved !== null ? "Your approval is recorded." : "A verification of this build passed.";
          next =
            approved !== null && mergeControls(approved).eligible ? (
              <MergePrimaryAction
                id="highbay-build-merge"
                primary={primaryActionId === "highbay-build-merge"}
                controls={mergeControls(approved)}
              />
            ) : approved !== null ? (
              <Button onClick={() => refreshMergeState(approved)}>Check current approval</Button>
            ) : (
              <span>Review the saved result below before approving it.</span>
            );
        } else if (tests.state === "reported") {
          attention = "Highbay is checking the reported tests.";
          next = <span>Wait for Highbay's verdict.</span>;
        } else {
          const assignmentsRead =
            build.builder.read &&
            (build.test_writer.read || tests.state === "accepted" || tests.state === "not_used");
          attention =
            teamError !== null || teamSnapshot === null || teamStatusUnknown
              ? "Team status unknown."
              : assignmentsRead
              ? "Nothing now."
              : "A worker assignment is unread.";
          next = (
            <span>
              {tests.state === "accepted" || tests.state === "not_used"
                ? "The builder implements and verifies the change."
                : "The test writer prepares and reports the tests."}
            </span>
          );
        }
      }
      if (hasPending("merge_change", ed.card.change_id)) {
        attention = "Merging the approved result.";
        next = (
          <MergePrimaryAction
            id="highbay-build-merge"
            primary={primaryActionId === "highbay-build-merge"}
            controls={mergeControls(buildApprovalRun(ed.card))}
          />
        );
      }
      if (next !== null && teamError === null && teamNeedsYou !== undefined) {
        attention = teamRoles
          .filter((role) => role.state === "needs_you")
          .map((role) => teamAttentionText(role))
          .join(". ");
      }
    } else if (onVerify) {
      attention = "Read the saved test evidence, then review the result for approval.";
      next = (
        <Button
          id="highbay-verify-review-result"
          variant={primaryActionId === "highbay-verify-review-result" ? "primary" : "secondary"}
          onClick={openApproval}
        >
          Review result
        </Button>
      );
    }

    if (next === null) {
      attention =
        teamNeedsYou !== undefined
          ? teamRoles
              .filter((role) => role.state === "needs_you")
              .map((role) => teamAttentionText(role))
              .join(". ")
          : teamLoading || teamSnapshot === null
          ? "Waiting for Team status."
          : teamStatusUnknown
          ? "Team status unknown."
          : "Nothing now.";
      let step: string | React.ReactElement;
      if (sessionState !== "active") {
        attention = "The session ended.";
        step =
          "Run highbay in your project folder or invoke /highbay in your coding agent to open Highbay views.";
      } else if (listError !== null) {
        attention = "The changes could not be read.";
        step = onChanges
          ? "Choose Read the changes again below."
          : "Open " + changesLabel + " to retry reading changes.";
      } else if (waitTroubleShown) {
        attention = "Updates are unavailable.";
        step = "Wait for Highbay to reconnect.";
      } else if (teamError !== null) {
        attention = "Team status could not be read.";
        step =
          currentView.kind === "team"
            ? "Choose Retry team read below."
            : "Open Team to retry reading roles.";
      } else if (
        lastOpenAttempt !== null &&
        lastOpenAttempt !== ed?.card.change_id &&
        cardLoadErrors.has(lastOpenAttempt)
      ) {
        attention = "The Change Card could not be opened.";
        step = onChanges
          ? "Try opening the change again below."
          : "Open " + changesLabel + " and try opening the change again.";
      } else if (ed !== null && cardLoadErrors.has(ed.card.change_id)) {
        attention = "The latest saved Change Card could not be read.";
        step =
          activeEditorReq !== null ? (
            "Wait for the current Change Card request."
          ) : (onAgree || onBuild) && ed.formError !== null ? (
            "Choose Load Latest Card to review the saved card alongside your edits."
          ) : (
            <Button onClick={handleLoadLatestCard}>Load Latest Card</Button>
          );
      } else if (onChanges) {
        if (activeEditorReq === "get_change") {
          step = "Wait for the Change Card to open.";
        } else if (changeList === null) {
          step = "Wait for Highbay to read the changes.";
        } else if (changeList.changes.length > 0) {
          step = "Open a change from the list below, or start a new change.";
        } else if (changeList.initializing_phase !== undefined) {
          step = "Wait for Highbay to finish preparing the project.";
        } else if (teamLoading || teamSnapshot === null) {
          step = "Wait for Team status.";
        } else if (guide === undefined) {
          step = "Describe what you want to build below to start Guide.";
        } else if (guide.state === "starting") {
          step = "Wait for Guide to start.";
        } else if (guide.state === "needs_you") {
          step = guideHasTerminal
            ? "Open Guide terminal to answer its prompt, or reply in Team conversation."
            : "Reply to Guide in Team conversation.";
        } else if (guide.state === "error" || (guideHasTerminal && guide.state === "unknown")) {
          if (teamNeedsYou === undefined) {
            attention = guide.state === "unknown" ? "Guide status unknown." : "Guide reported an error.";
          }
          step = "Open Team or Guide terminal to check Guide’s status.";
        } else if (guide.state === "working" || changeList.draft_state === "evaluating_story") {
          step = "Wait for Guide to propose the Change Card.";
        } else if (changeList.draft_state === "story_failed") {
          if (teamNeedsYou === undefined) attention = "Story check needs attention.";
          step = "Ask Guide below or in Team conversation to fix the Story and propose the Change Card.";
        } else if (changeList.draft_state !== undefined) {
          step = "Wait for Guide to propose the Change Card, or message Guide in Team conversation.";
        } else {
          step = "Describe what you want to build below.";
        }
      } else if (onAgree && ed !== null) {
        if (activeEditorReq !== null && activeEditorReq !== "confirm_intent") {
          step = "Wait for the current Change Card request.";
        } else if (ed.incomingCard !== null) {
          attention = "A newer saved revision is available.";
          step = "Choose Switch to Incoming Card to review it and keep your unsaved edits.";
        } else if (ed.card.proposed_revision !== undefined) {
          attention = "A proposed revision is waiting for your decision.";
          step = "Review the proposal below, then choose Adopt or Decline.";
        } else if (ed.formError?.source === "request") {
          attention = "The Change Card request failed.";
          step = "Choose Load Latest Card to review the saved card alongside your edits.";
        } else if (ed.card.status === "needs_confirmation") {
          const unanswered = unansweredQuestions(ed);
          const question = unanswered[0];
          const exampleIssue = firstIncompleteExample(ed);
          const confirming = activeEditorReq === "confirm_intent";
          if (question !== undefined) {
            attention =
              unanswered.length === 1
                ? "1 question to answer."
                : unanswered.length + " questions to answer.";
            step = (
              <Button
                id="highbay-cp1-next-question"
                variant={primaryActionId === "highbay-cp1-next-question" ? "primary" : "secondary"}
                onClick={() => focusDraftQuestion(question.id)}
              >
                Jump to unanswered question
              </Button>
            );
          } else if (exampleIssue !== null) {
            attention = "An example needs " + exampleIssue.label + ".";
            step = (
              <Button
                id="highbay-cp1-next-example"
                variant={primaryActionId === "highbay-cp1-next-example" ? "primary" : "secondary"}
                onClick={() => focusDraftExample(exampleIssue)}
              >
                Review example
              </Button>
            );
          } else {
            attention = confirming ? "Saving your agreement." : "Ready to confirm intent.";
            step = (
              <Button
                id="highbay-cp1-next-confirm"
                variant={primaryActionId === "highbay-cp1-next-confirm" ? "primary" : "secondary"}
                disabled={confirming}
                onClick={handleConfirmSubmit}
              >
                {confirming ? "Confirming…" : "Confirm intent"}
              </Button>
            );
          }
        } else if (ed.preservedDraft !== null) {
          attention = "You have edits from an earlier revision.";
          step = "Review the saved card and reapply any of the preserved edits you still need.";
        } else if (isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples)) {
          attention = "You have unsaved refinements.";
          step = "Save or discard your refinements below.";
        } else {
          step = (
            <Button
              id="highbay-agree-continue-build"
              variant={primaryActionId === "highbay-agree-continue-build" ? "primary" : "secondary"}
              onClick={() => {
                showChangeEditor("build");
                focusResult("#highbay-build-title");
              }}
            >
              Review Agreement details, then Continue to Build
            </Button>
          );
        }
      } else if (onBuild && ed !== null) {
        if (ed.card.status !== "confirmed") {
          attention = "This change needs confirmation.";
          step = "Confirm the Change Card in Agree before starting a build.";
        } else {
          step = "Review the Build controls below for this change.";
        }
      } else if (onArch && ed !== null) {
        const card = ed.card;
        if (card.status !== "confirmed") {
          attention = "This change needs confirmation.";
          step = "Confirm the Change Card in Agree to preview its architecture.";
        } else if (hasPending("preview_architecture", card.change_id)) {
          step = "Wait for Highbay to evaluate the design.";
        } else if (previewErrors.has(card.change_id)) {
          attention = "The architecture preview could not be loaded.";
          step = "Use Evaluate the design below to try again.";
        } else {
          step = (
            <Button
              id="highbay-arch-continue-build"
              variant={primaryActionId === "highbay-arch-continue-build" ? "primary" : "secondary"}
              onClick={() => {
                showChangeEditor("build");
                focusResult("#highbay-build-title");
              }}
            >
              Continue to Build
            </Button>
          );
        }
      } else if (currentView.kind === "team") {
        step = teamStarting
          ? "Wait for workers to start."
          : teamLoading || teamSnapshot === null
          ? "Wait for Team status."
          : teamNeedsYou === undefined && teamWorkersHaveWork()
          ? "Use Start workers to bring up missing build workers."
          : guide === undefined
          ? "Send a message in Team conversation to start Guide."
          : teamRoles.some((role) => role.has_terminal)
          ? "Review role status and open a role terminal or message an agent."
          : "Review role status below.";
      } else if (currentView.kind === "story") {
        if (storyEvaluationError !== null) {
          attention = "The Story draft could not be read.";
          step = "Choose Read the draft again below.";
        } else {
          step =
            storyEvaluation === null
              ? "Wait for Highbay to read the Story draft."
              : storyEvaluation.state === "none"
              ? "Ask your coding agent to refresh the Story."
              : storyEvaluation.state === "running"
              ? "Wait for the Story evaluation."
              : storyEvaluation.state === "failed"
              ? "Ask your coding agent to fix the Story and refresh it."
              : "Review the current draft and any saved Story below.";
        }
      } else if (currentView.kind === "live") {
        if (liveTargetError !== null || modelConnectionError !== null) {
          attention =
            liveTargetError !== null
              ? "The live target could not be read."
              : "The model connection could not be read.";
          step =
            liveTargetError !== null
              ? "Choose Read the live target again below."
              : "Choose Read the model connection again below.";
        } else {
          const target = liveTarget?.live_target;
          step =
            target === undefined
              ? "Wait for Highbay to read the live target."
              : target.state === "running"
              ? "Review the running service and its prompt drafts below."
              : target.state === "starting"
              ? "Wait for the live target to start."
              : target.state === "none"
              ? "Ask the builder to start a live target to try prompts."
              : "Review the last live target’s status below.";
        }
      } else {
        step = "Open " + changesLabel + " to find a change.";
      }
      next = typeof step === "string" ? <span>{step}</span> : step;
    }

    const agreedCard =
      ed !== null &&
      ed.card.status === "confirmed" &&
      ((currentView.kind !== "changes" && currentView.kind !== "new") || edHasUnsaved)
        ? ed.card
        : null;

    const messages = teamConversation?.messages ?? [];
    const unreadCount = Math.max(0, messages.length - seenMessageCount);
    const anyTerminalRole =
      openTerminalRole ??
      teamRoles.find((r) => r.state === "needs_you" && r.has_terminal)?.role ??
      teamRoles.find((r) => r.has_terminal)?.role ??
      null;

    navEl.hidden = false;
    const hideNextForSheet = isNarrowViewport() && mobileSurface !== "stage";

    flushSync(() => {
      navRoot.render(
        <WorkspaceFrame
          project={projectName ?? "Project"}
          changesNav={changesNav}
          newChangeNav={newChangeNav}
          journey={journey}
          references={references}
          mobileConversation={{
            active: mobileSurface === "conversation",
            unreadCount,
            messageCount: messages.length,
            onClick: (invoker) => {
              if (mobileSurface === "conversation") {
                mobileSurface = "stage";
                syncHistoryState("replace");
                syncAllRegions();
                invoker.focus();
              } else {
                openConversationSurface(invoker);
                syncAllRegions();
                document.getElementById("highbay-composer-input")?.focus();
              }
            },
          }}
          mobileTerminal={
            anyTerminalRole !== null
              ? {
                  active: mobileSurface === "terminal",
                  label:
                    mobileSurface === "terminal"
                      ? "Back to stage"
                      : (anyTerminalRole === "guide"
                          ? "Guide"
                          : anyTerminalRole === "builder"
                          ? "Builder"
                          : "Test writer") + " terminal",
                  onClick: (invoker) => {
                    if (mobileSurface === "terminal") {
                      mobileSurface = "stage";
                      syncHistoryState("replace");
                      syncAllRegions();
                      invoker.focus();
                    } else {
                      openRoleTerminal(anyTerminalRole, invoker);
                    }
                  },
                }
              : null
          }
          agreed={agreedCard?.summary ?? null}
          onReadFullSummary={agreedCard !== null ? handleReadFullSummary : null}
          showSlots={showSlots}
          hideNextForSheet={hideNextForSheet}
          attention={attention}
          next={next}
          diagnosticsOpen={traceOpen}
          onToggleDiagnostics={() => {
            traceOpen = !traceOpen;
            if (traceOpen && tracePage === null) loadTracePage();
            syncAllRegions();
          }}
          details={
            subjects.length >= 1 ? (
              <div
                id="highbay-workspace-context"
                className="highbay-workspace-context"
                role="region"
                aria-label="Displayed workspace subject"
              >
                {subjects.map((subj) => (
                  <div
                    key={subj.role}
                    id={"highbay-context-" + subj.role}
                    className="highbay-subject-item"
                    data-subject-role={subj.role}
                    data-change-id={subj.changeId}
                    data-request-revision={subj.requestRevision}
                  >
                    <span className="highbay-subject-label">{subj.label}</span>{" "}
                    <strong>{subj.stage}</strong>
                    {subj.changeId ? (
                      <>
                        {" · "}
                        <code>{subj.changeId}</code>
                      </>
                    ) : null}
                    {subj.requestRevision ? (
                      <>
                        {" · "}
                        <code>{subj.requestRevision.slice(0, 12)}</code>
                      </>
                    ) : null}
                    {subj.detail ? (
                      <>
                        {" · "}
                        <span>{subj.detail}</span>
                      </>
                    ) : null}
                  </div>
                ))}
              </div>
            ) : null
          }
        />
      );
    });
    if (approvalWasFocused && document.getElementById("highbay-cp5-approve") === null) {
      focusResult("#highbay-cp5-title");
    }
  }

  function projectTitle(): string {
    if (projectName === null) return "Highbay";
    let step: string;
    const ed = getActiveEditor();
    if (sessionState !== "active") {
      step = "Reopen project";
    } else if (waitTroubleShown) {
      step = "Reconnecting";
    } else if (listError !== null) {
      step = "Retry reading changes";
    } else if (teamReadError !== null) {
      step = "Refresh Team";
    } else if (lastOpenAttempt !== null && cardLoadErrors.has(lastOpenAttempt)) {
      step = "Retry opening change";
    } else {
      const attention = (["guide", "builder", "test_writer"] as const)
        .map((role) =>
          teamSnapshot?.roles.find((item) => item.role === role && item.state === "needs_you")
        )
        .find((role) => role !== undefined);
      if (attention !== undefined) {
        step = teamAttentionText(attention);
      } else if (ed !== null && currentView.kind !== "new") {
        if (ed.incomingCard !== null || ed.card.proposed_revision !== undefined) {
          step = "Review proposed change";
        } else if (ed.card.status === "needs_confirmation") {
          step = "Confirm change";
        } else if (isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples)) {
          step = "Refine change";
        } else {
          step = "Review change";
        }
      } else if (changeList === null) {
        step = "Opening project";
      } else if (changeList.changes.length === 0 || currentView.kind === "new") {
        const guide = connectedGuide();
        if (changeList.initializing_phase !== undefined) {
          step = "Preparing project";
        } else if (changeList.draft_state === "story_failed" && guide?.state !== "working") {
          step = "Story check failed";
        } else if (guide?.state === "working" || changeList.draft_state !== undefined) {
          step = "Drafting Change Card";
        } else {
          step = "Describe your change";
        }
      } else {
        step = "Open a change";
      }
    }
    return step + ": " + projectName;
  }

  // collectAllUnsavedLines aggregates unsaved edits across all editorsByChangeId
  // plus unsent composer/describe text for SessionEndedNotice.
  function collectAllUnsavedLines(): string[] {
    const lines: string[] = [];
    for (const ed of editorsByChangeId.values()) {
      const snap = snapshotUnsavedDraft(ed);
      if (snap !== null) {
        lines.push(...snap.lines);
      }
    }
    if (describeText.trim() !== "") {
      lines.push("[New change draft] " + describeText);
    }
    if (composerDraft.text.trim() !== "") {
      lines.push("[Conversation draft to " + composerDraft.to + "] " + composerDraft.text);
    }
    for (const [scope, draft] of composerDraftsByScope.entries()) {
      if (scope !== activeComposerScope && draft.text.trim() !== "") {
        lines.push("[Conversation draft (" + scope + ") to " + draft.to + "] " + draft.text);
      }
    }
    return lines;
  }

  // waitingProposalItems lists Change Cards with unconfirmed intent or a waiting
  // proposed_revision for the independent ProposalReviewQueue.
  function waitingProposalItems(excludeChangeId: string | null) {
    return (changeList?.changes ?? []).filter(
      (item) =>
        item.change_id !== excludeChangeId &&
        (item.status === "needs_confirmation" || item.proposed_revision !== undefined)
    );
  }

  function syncNoticeRegion(primaryAttentionRole: TeamRoleState["role"] | null = resolvePrimaryAttentionRole()) {
    document.title = projectTitle();
    const dirtyIds = Array.from(dirtyChangeIds());
    const showGlobalProposalQueue =
      sessionState === "active" &&
      currentView.kind !== "new" &&
      currentView.kind !== "changes";
    const globalQueueItems = showGlobalProposalQueue
      ? waitingProposalItems(currentView.kind === "change" ? currentView.changeId : null)
      : [];

    flushSync(() => {
      noticeRoot.render(
        <>
          {sessionState === "active" || sessionState === "ended" ? (
            <UpdateBanner
              available={updateStatus}
              progress={updateProgress}
              uiBuildChanged={uiBuildChanged}
              hasUnsavedInput={hasUnsavedPageInput()}
              disabled={sessionState !== "active"}
              onInstall={applyServerUpdate}
              onCheckRestart={(targetVersion) => {
                void waitForUpdatedServer(targetVersion);
              }}
              onReload={() => window.location.reload()}
            />
          ) : null}
          {pendingLinkFragment !== null ? (
            <Alert id="highbay-pending-link" role="status">
              <p>
                You opened a new Highbay link in this tab. Save or discard your edits, then choose
                Open link.
              </p>
              {hasPendingEditorSave() ? <p>Wait for the current save to finish.</p> : null}
              {Array.from(editorsByChangeId.values()).some(
                (entry) => Object.keys(entry.proposalAnswers).length > 0
              ) ? (
                <p>
                  For proposal answers, choose Adopt this revision or Discard proposal answers.
                  Discard clears your answers without declining the proposal.
                </p>
              ) : null}
              {dirtyIds.length > 0 ? (
                <div className="highbay-field-row">
                  <span>Unsaved Change Card drafts:</span>
                  {dirtyIds.map((id) => (
                    <Button key={id} variant="ghost" onClick={() => openChange(id, "agree")}>
                      {"Open " + id}
                    </Button>
                  ))}
                </div>
              ) : null}
              {modelDraftDirty ? (
                <p>For the model choice, choose Apply or Discard model edits.</p>
              ) : null}
              <p>
                The link expires two minutes after it was printed, even while you finish your edits.
              </p>
              <div className="highbay-field-row">
                <Button
                  id="highbay-open-pending-link"
                  disabled={hasUnsavedPageInput()}
                  onClick={openPendingLink}
                >
                  Open link
                </Button>
                {hasUnsavedPageInput() && !hasPendingEditorSave() ? (
                  <Button
                    id="highbay-discard-all-drafts"
                    variant="ghost"
                    onClick={discardAllPageDrafts}
                  >
                    Discard all unsaved drafts
                  </Button>
                ) : null}
              </div>
            </Alert>
          ) : null}
          {sessionState === "ended" ? (
            <SessionEndedNotice unsavedLines={collectAllUnsavedLines()} />
          ) : null}
          {sessionState === "active" || sessionState === "ended" ? (
            <GlobalTeamAttention
              snapshot={teamSnapshot}
              lastRead={teamFactsError() !== null}
              disabled={sessionState !== "active"}
              openTerminalRole={terminalObserveMode ? null : openTerminalRole}
              primaryRole={primaryAttentionRole}
              onOpenTerminal={(role, invoker) => openRoleTerminal(role, invoker, false)}
              onMessageRole={(role) => focusComposerForRole(role)}
              onFocusedControlRemoved={focusCurrentViewHeading}
            />
          ) : null}
          {globalQueueItems.length > 0 ? (
            <ProposalReviewQueue
              items={globalQueueItems}
              disabled={sessionState !== "active"}
              onOpenChange={(id) => openChange(id, "agree")}
            />
          ) : null}
          {teamActionError !== null && currentView.kind !== "team" ? (
            <Alert id="highbay-team-action-error" variant="destructive" role="alert">
              {teamActionError}
            </Alert>
          ) : null}
        </>
      );
    });
  }

  function revisionBuildNotice(card: ChangeCardResult): string | null {
    if (card.status !== "confirmed") return null;
    const known =
      buildChangeId === card.change_id &&
      buildSnapshot !== null &&
      buildReadError === null &&
      !waitTroubleShown;
    if (!known || buildSnapshot === null) {
      return (
        "Changing the agreed revision leaves any existing build on the earlier revision. " +
        "Starting a new build replaces its assignments; when it uses a test writer, its report " +
        "and verdict reset and its tests need acceptance again before verification."
      );
    }
    if (
      buildSnapshot.state !== "started" ||
      !buildSnapshot.in_force ||
      buildSnapshot.request_revision !== card.request_revision
    ) {
      return null;
    }
    if (buildSnapshot.test_writer.tests.state === "not_used") {
      return (
        "Changing the agreed revision leaves this build on the earlier revision. " +
        "Start a new build to update the builder’s assignment. " +
        "Verification keeps using the stored verification settings."
      );
    }
    return (
      "Changing the agreed revision leaves this build on the earlier revision. " +
      "A new build replaces both assignments and clears the test writer’s report and verdict. " +
      "Its tests must be reported, checked and accepted again before verification."
    );
  }

  function draftRevisionAid(
    id: string,
    changeId: string,
    requestRevision: string,
    choices: ReturnType<typeof answersNeedingRevision>,
    primarySend: boolean
  ) {
    if (choices.length === 0) return null;
    const needsGuide = guideRoleNeedsStart(teamSnapshot);
    const sending = hasPending("send_team_message", "agree");
    const guideInvalid =
      needsGuide &&
      (agentOptions === null ||
        guideChoice === undefined ||
        roleChoiceError(guideChoice, agentOptions.agents) !== null);
    const changedAnswers: Record<string, string> = {};
    for (const item of choices) {
      changedAnswers[item.id] = item.answer;
    }
    const context: ComposerContext = {
      type: "revise_answers",
      value: { change_id: changeId, request_revision: requestRevision, answers: changedAnswers },
    };
    const summaryText =
      "Please revise change " +
      changeId +
      " so the draft implements my attached answers, updating the design, Story, examples, and preserved behaviors as needed, then propose the revised change.";
    return (
      <Alert id={id} tabIndex={-1} variant="warning" role="region" aria-labelledby={id + "-label"}>
        <DraftRevisionAidLoader
          active={needsGuide && agentOptions === null && agentOptionsError === null}
          onLoad={loadAgentOptions}
        />
        <p id={id + "-label"}>
          Your selected answers differ from what this draft currently implements. Ask Guide to revise
          the draft for your answers, then review its proposed revision here.
        </p>
        <div className="highbay-field-row">
          <Button
            id={id + "-send"}
            variant={primarySend ? "primary" : "secondary"}
            disabled={sessionState !== "active" || sending || guideInvalid}
            onClick={() =>
              submitTeamMessage("agree", {
                to: "guide",
                text: summaryText,
                context,
              })
            }
          >
            {sending ? "Sending to Guide…" : "Revise draft for my answers"}
          </Button>
          <Button
            id={id + "-compose"}
            disabled={sessionState !== "active" || sending}
            onClick={() => focusComposerForRole("guide", context, summaryText)}
          >
            Edit message first
          </Button>
        </div>
      </Alert>
    );
  }

  // renderDraftWorkspace builds DraftChangeWorkspace for Moment 0 or + New change.
  function renderDraftWorkspace(isFirstProjectChange: boolean, primaryActionId: string | null) {
    const queueItems = isFirstProjectChange ? [] : waitingProposalItems(null);
    return (
      <DraftChangeWorkspace
        isFirstProjectChange={isFirstProjectChange}
        list={changeList}
        loadingList={hasPending("list_changes", "")}
        storyEvaluation={storyEvaluation}
        snapshot={teamSnapshot}
        teamLoading={hasPending("get_team", "")}
        conversation={teamConversation}
        sentMessageId={draftSentMessageId}
        sendUncertain={describeSendUncertain}
        sendError={describeSendError}
        sending={hasPending("send_team_message", "describe")}
        restartingGuide={hasPending("restart_team_role", "guide")}
        disabled={sessionState !== "active"}
        primarySubmit={primaryActionId === "highbay-describe-submit"}
        primaryRestartGuide={primaryActionId === "highbay-draft-restart-guide"}
        primaryReply={primaryActionId === "highbay-draft-reply"}
        guideChoice={guideChoice}
        agentOptions={agentOptions?.agents ?? null}
        agentOptionsLoading={hasPending("list_team_agent_options", "")}
        agentOptionsError={agentOptionsError}
        draftText={describeText}
        proposalsQueue={
          queueItems.length > 0 ? (
            <ProposalReviewQueue
              items={queueItems}
              disabled={sessionState !== "active"}
              onOpenChange={(id) => openChange(id, "agree")}
            />
          ) : null
        }
        onDraftTextChange={(next) => {
          describeText = next;
          if (describeSendError !== null && !describeSendUncertain) {
            describeSendError = null;
          }
          syncAllRegions();
        }}
        onGuideChoiceChange={(choice) => {
          guideChoice = choice;
          guideChoiceEdited = true;
          syncAllRegions();
        }}
        onLoadAgentOptions={loadAgentOptions}
        onRefreshAgentOptions={readAgentOptions}
        onSubmit={() => {
          if (describeText.trim() === "") return;
          submitTeamMessage("describe", {
            to: "guide",
            text: describeText,
            context: { type: "new_change", value: { scope: "separate_card" } },
          });
        }}
        onReviewConversation={() => {
          openConversationSurface();
          syncAllRegions();
          document.getElementById("highbay-conversation-title")?.focus();
        }}
        onReplyToGuide={(replySeq) => {
          focusComposerForRole("guide", null, "", replySeq);
        }}
        onMessageGuide={() => {
          focusComposerForRole("guide", null, "", null);
        }}
        onRestartGuide={() => restartTeamRole("guide")}
        onResetRequest={() => {
          draftSentMessageId = undefined;
          describeSendUncertain = false;
          describeSendError = null;
          syncHistoryState("replace");
          syncAllRegions();
        }}
        onWatchGuideTerminal={(invoker) => openRoleTerminal("guide", invoker)}
      />
    );
  }

  function verificationDetailsView(
    displayedRun: VerificationStatusResult | null,
    effectiveChangeId: string,
    primaryActionId: string | null
  ) {
    const build = knownTakingBuild(effectiveChangeId);
    const onApproveStage = currentView.kind === "change" && currentView.stage === "approve";
    const listedIds = lastLoadedListByChangeId.get(effectiveChangeId);
    const listedRuns =
      listedIds !== undefined
        ? listedIds
            .map((id) => runsById.get(id))
            .filter((r): r is VerificationStatusResult => Boolean(r))
        : null;
    const loadedCard = editorsByChangeId.get(effectiveChangeId)?.card ?? getActiveEditor()?.card ?? null;
    const current = changeList?.changes.find((item) => item.change_id === effectiveChangeId);
    const cardForBuild = editorsByChangeId.get(effectiveChangeId)?.card ?? null;
    const inForce = cardForBuild !== null ? currentInForceBuild(cardForBuild) : null;
    const runNeedsBuilderFix =
      !onApproveStage &&
      displayedRun !== null &&
      cardForBuild !== null &&
      inForce !== null &&
      runInputsKnown(displayedRun) &&
      displayedRun.inputs_current &&
      runStartedInCurrentBuild(cardForBuild, inForce, displayedRun) &&
      (displayedRun.state.type === "not_completed" ||
        (displayedRun.state.type === "completed" && !displayedRun.state.value.report.passed));

    return (
      <VerificationView
        changeId={effectiveChangeId}
        build={onApproveStage && build?.in_force === false ? null : build}
        run={displayedRun}
        inputsKnown={displayedRun !== null && runInputsKnown(displayedRun)}
        review={
          displayedRun !== null && displayedRun.run_id === selectedRunId
            ? displayedApprovalReview()
            : null
        }
        currentAgreementRevision={current?.status === "confirmed" ? current.request_revision : null}
        listedRuns={listedRuns}
        loadedCard={loadedCard}
        checking={
          displayedRun
            ? hasPending("get_verification", displayedRun.run_id) ||
              hasPending("review_verification", displayedRun.run_id)
            : false
        }
        loadingList={hasPending("list_verifications", effectiveChangeId)}
        loadingCard={
          editorRequestsByChangeId.has(effectiveChangeId) || cardRefreshInFlight.has(effectiveChangeId)
        }
        requestError={displayedRun ? runRequestErrors.get(displayedRun.run_id) ?? null : null}
        listError={listRequestErrors.get(effectiveChangeId) ?? null}
        cardLoadError={effectiveChangeId ? cardLoadErrors.get(effectiveChangeId) ?? null : null}
        primaryCheckStatus={!onApproveStage && primaryActionId === "highbay-cp4-check-status"}
        onMessageBuilder={
          runNeedsBuilderFix && displayedRun !== null
            ? () =>
                focusComposerForRole(
                  "builder",
                  null,
                  "Verification run " +
                    displayedRun.run_id.slice(0, 12) +
                    " on candidate " +
                    displayedRun.candidate_commit.slice(0, 12) +
                    " did not pass. Please inspect the verification report and run another check."
                )
            : null
        }
        onCheckStatus={handleCheckVerificationStatus}
        onLoadVerifications={() => handleListVerifications(effectiveChangeId)}
        onSelectRun={handleSelectVerificationRun}
        onOpenChangeCard={() => {
          if (activeChangeId === effectiveChangeId && editorsByChangeId.has(effectiveChangeId)) {
            handleLoadLatestCard();
          } else {
            openChange(effectiveChangeId);
          }
        }}
        onReturnToChangeCard={handleReturnToChangeCard}
      />
    );
  }

  // syncViewRegion mounts the single active stage inside main#highbay-checkpoint-view[data-stage=...].
  function syncViewRegion(primaryActionId: string | null = resolvePrimaryActionId().actionId) {
    const hideForMobileSheet = isNarrowViewport() && mobileSurface !== "stage";
    viewEl.hidden = hideForMobileSheet;
    viewEl.inert = hideForMobileSheet;
    if (currentView.kind === "change" && sessionState !== "starting" && sessionState !== "signed-out") {
      const stageEd = editorsByChangeId.get(currentView.changeId) ?? null;
      viewEl.dataset.changeId = currentView.changeId;
      if (stageEd !== null) {
        viewEl.dataset.requestRevision = stageEd.card.request_revision;
      } else {
        delete viewEl.dataset.requestRevision;
      }
    } else {
      delete viewEl.dataset.changeId;
      delete viewEl.dataset.requestRevision;
    }

    if (sessionState === "starting") {
      viewEl.dataset.stage = "starting";
      flushSync(() => {
        viewRoot.render(null);
      });
      return;
    }

    if (sessionState === "signed-out") {
      viewEl.dataset.stage = signedOutReason === "link-used" ? "link-used" : "signed-out";
      flushSync(() => {
        viewRoot.render(
          <div id="highbay-result-view">
            <SignedOutView reason={signedOutReason} message={signedOutMessage} />
          </div>
        );
      });
      return;
    }

    if (currentView.kind === "changes") {
      viewEl.dataset.stage = "changes";
      flushSync(() => {
        viewRoot.render(
          <div id="highbay-result-view">
            <ChangeListView
              list={changeList}
              loading={hasPending("list_changes", "")}
              error={listError}
              openError={lastOpenAttempt !== null ? cardLoadErrors.get(lastOpenAttempt) ?? null : null}
              openChangeId={activeChangeId}
              dirtyChangeIds={dirtyChangeIds()}
              loadingChangeIds={
                new Set(
                  Array.from(editorRequestsByChangeId.entries())
                    .filter(([id, op]) => op === "get_change" && !editorsByChangeId.has(id))
                    .map(([id]) => id)
                )
              }
              disabled={sessionState !== "active"}
              launchWorkspace={renderDraftWorkspace(true, primaryActionId)}
              onNewChange={() => openNewChange()}
              onOpen={(id) => openChange(id)}
              onRetry={() => void loadChanges()}
            />
          </div>
        );
      });
      return;
    }

    if (currentView.kind === "new") {
      viewEl.dataset.stage = "new";
      flushSync(() => {
        viewRoot.render(
          <div id="highbay-result-view">
            {renderDraftWorkspace(false, primaryActionId)}
          </div>
        );
      });
      return;
    }

    if (currentView.kind === "story") {
      viewEl.dataset.stage = "story";
      const openCard = getActiveEditor()?.card ?? null;
      flushSync(() => {
        viewRoot.render(
          <div
            id="highbay-result-view"
            data-change-id={openCard?.change_id}
            data-request-revision={openCard?.request_revision}
          >
            <StoryStageView
              card={openCard}
              evaluation={storyEvaluation}
              loading={hasPending("get_story_refresh", "")}
              error={storyEvaluationError}
              onRetry={readStoryEvaluation}
              onAskAboutFlow={(storyName, flowKey) =>
                focusComposerForRole("guide", {
                  type: "story_flow",
                  value: {
                    ...(openCard !== null ? { change_id: openCard.change_id } : {}),
                    story: storyName,
                    ...(flowKey !== undefined ? { flow_key: flowKey } : {}),
                  },
                })
              }
            />
          </div>
        );
      });
      return;
    }

    if (currentView.kind === "team") {
      viewEl.dataset.stage = "team";
      flushSync(() => {
        viewRoot.render(
          <div id="highbay-result-view">
            <TeamStageView
              snapshot={teamSnapshot}
              loading={hasPending("get_team", "")}
              error={teamFactsError()}
              starting={hasPending("start_team_agents", "")}
              canStartWorkers={teamWorkersHaveWork()}
              primaryStart={primaryActionId === "highbay-team-start"}
              primaryRefresh={primaryActionId === "highbay-team-refresh"}
              agentPicker={
                teamWorkersHaveWork()
                  ? workerPicker("highbay-team-agents", teamWorkerRoles())
                  : workerRecommendations("highbay-team-agents")
              }
              agentsReady={workersReady(teamWorkerRoles())}
              disabled={sessionState !== "active"}
              interruptingRoles={pendingRoleOperations("interrupt_team_role")}
              stoppingRoles={pendingRoleOperations("stop_team_role")}
              restartingRoles={pendingRoleOperations("restart_team_role")}
              openTerminalRole={openTerminalRole}
              startReceipt={teamStartReceipt}
              actionError={teamActionError}
              onRefresh={readTeam}
              onStartWorkers={startTeamWorkers}
              onOpenTerminal={(role, invoker) => openRoleTerminal(role, invoker)}
              onInterruptRole={interruptTeamRole}
              onStopRole={stopTeamRole}
              onRestartRole={restartTeamRole}
              onMessageRole={(role) => focusComposerForRole(role)}
              onFocusedControlRemoved={focusCurrentViewHeading}
            />
          </div>
        );
      });
      return;
    }

    if (currentView.kind === "live") {
      viewEl.dataset.stage = "live";
      flushSync(() => {
        viewRoot.render(
          <div id="highbay-result-view">
            <LiveTargetStageView
              target={liveTarget}
              targetLoading={hasPending("get_live_target", "")}
              targetError={liveTargetError}
              onRetryTarget={readLiveTarget}
              stopping={hasPending("stop_live_target", "")}
              stopOutcome={liveStopOutcome}
              onStop={stopLiveTarget}
              model={modelConnection}
              modelLoading={hasPending("get_model_connection", "")}
              modelError={modelConnectionError}
              onRetryModel={readModelConnection}
              applying={hasPending("configure_model", "")}
              applyOutcome={modelApplyOutcome}
              onApply={applyModel}
              draft={modelDraft}
              onDraft={updateModelDraft}
              draftDirty={modelDraftDirty}
              draftReset={modelDraftReset}
              primaryApply={primaryActionId === "highbay-model-apply"}
              onDiscardDraft={discardModelDraft}
            />
          </div>
        );
      });
      return;
    }

    // currentView.kind === "change"
    viewEl.dataset.stage = currentView.stage;
    const ed = editorsByChangeId.get(currentView.changeId) ?? null;
    const editorReq = getEditorRequest(currentView.changeId);

    if (currentView.stage === "agree") {
      if (ed === null) {
        flushSync(() => {
          viewRoot.render(
            <Card className="highbay-card" role="status">
              Reading Change Card…
            </Card>
          );
        });
        return;
      }
      const proposal = ed.card.proposed_revision;
      const buildRevisionNotice = revisionBuildNotice(ed.card);
      flushSync(() => {
        viewRoot.render(
          <div
            id="highbay-cp1-editor"
            data-change-id={ed.card.change_id}
            data-request-revision={ed.card.request_revision}
            tabIndex={-1}
            aria-label="Change decisions"
          >
            {ed.incomingCard !== null ? (
              <IncomingChangeCardView
                incomingCard={ed.incomingCard}
                onSwitch={() => {
                  const currentEd = editorsByChangeId.get(ed.card.change_id);
                  if (!currentEd?.incomingCard) return;
                  if (editorRequestsByChangeId.has(ed.card.change_id)) {
                    statusEl.textContent =
                      "Wait for the pending CP1 Change Card request to finish before switching cards.";
                    return;
                  }
                  const preserved = snapshotUnsavedDraft(currentEd);
                  const prior = nextPriorCard(currentEd, currentEd.incomingCard);
                  mountChangeCard(currentEd.incomingCard, preserved, prior, "agree", {
                    forceActivate: true,
                  });
                }}
              />
            ) : null}
            {proposal !== undefined ? (
              <ProposedRevisionView
                card={ed.card}
                proposal={proposal}
                answers={ed.proposalAnswers}
                busy={editorReq !== null}
                error={ed.proposalError}
                revisionAid={draftRevisionAid(
                  "highbay-proposal-revision-needed",
                  ed.card.change_id,
                  proposal.request_revision,
                  answersNeedingRevision(proposal.ambiguities, ed.proposalAnswers),
                  primaryActionId === "highbay-proposal-revision-needed-send"
                )}
                buildRevisionNotice={buildRevisionNotice}
                primaryAdopt={primaryActionId === "highbay-proposal-adopt"}
                onAnswer={(questionId, option) => {
                  const currentEd = editorsByChangeId.get(ed.card.change_id);
                  if (!currentEd || editorRequestsByChangeId.has(ed.card.change_id)) {
                    return;
                  }
                  editorsByChangeId.set(ed.card.change_id, {
                    ...currentEd,
                    proposalAnswers: { ...currentEd.proposalAnswers, [questionId]: option },
                  });
                  syncAllRegions();
                }}
                onAdopt={handleAdopt}
                onDecline={handleDecline}
                onAskGuideToRevise={() =>
                  focusComposerForRole("guide", {
                    type: "refine_change",
                    value: {
                      change_id: ed.card.change_id,
                      request_revision: proposal.request_revision,
                    },
                  })
                }
                onDiscardAnswers={() => {
                  const currentEd = editorsByChangeId.get(ed.card.change_id);
                  if (!currentEd || editorRequestsByChangeId.has(ed.card.change_id)) return;
                  editorsByChangeId.set(ed.card.change_id, { ...currentEd, proposalAnswers: {} });
                  syncAllRegions();
                  focusResult("#highbay-proposal");
                }}
              />
            ) : null}
            <CP1EditorView
              card={ed.card}
              buildRevisionNotice={buildRevisionNotice}
              proposalWaiting={proposal !== undefined}
              primaryConfirm={primaryActionId === "highbay-confirm-submit"}
              revisionAid={
                proposal === undefined
                  ? draftRevisionAid(
                      "highbay-confirm-revision-needed",
                      ed.card.change_id,
                      ed.card.request_revision,
                      answersNeedingRevision(ed.card.ambiguities, ed.draftAnswers),
                      primaryActionId === "highbay-confirm-revision-needed-send"
                    )
                  : null
              }
              priorCard={ed.priorCard}
              draftAnswers={ed.draftAnswers}
              draftExamples={ed.draftExamples}
              rpcInFlight={editorReq !== null}
              loadingVerifications={hasPending("list_verifications", ed.card.change_id)}
              preservedDraft={ed.preservedDraft}
              formError={ed.formError?.message ?? null}
              onSetAnswer={(ambId, opt) => {
                const currentEd = editorsByChangeId.get(ed.card.change_id);
                if (!currentEd || editorRequestsByChangeId.has(ed.card.change_id)) {
                  return;
                }
                editorsByChangeId.set(ed.card.change_id, {
                  ...currentEd,
                  draftAnswers: { ...currentEd.draftAnswers, [ambId]: opt },
                  formError: currentEd.formError?.source === "input" ? null : currentEd.formError,
                });
                syncAllRegions();
              }}
              onSetExampleField={(exId, field, value) => {
                const currentEd = editorsByChangeId.get(ed.card.change_id);
                if (!currentEd || editorRequestsByChangeId.has(ed.card.change_id)) {
                  return;
                }
                editorsByChangeId.set(ed.card.change_id, {
                  ...currentEd,
                  draftExamples: currentEd.draftExamples.map((item) =>
                    item.id === exId ? { ...item, [field]: value } : item
                  ),
                  formError: currentEd.formError?.source === "input" ? null : currentEd.formError,
                });
                syncAllRegions();
              }}
              onSubmitConfirm={handleConfirmSubmit}
              onDiscardDraft={handleDiscardDraft}
              onLoadLatestCard={ed.formError?.source === "input" ? null : handleLoadLatestCard}
              onLoadVerifications={() => handleListVerifications(ed.card.change_id)}
              onAskGuideToRevise={() =>
                focusComposerForRole("guide", {
                  type: "refine_change",
                  value: {
                    change_id: ed.card.change_id,
                    request_revision: ed.card.request_revision,
                  },
                })
              }
              onContinueToBuild={() => {
                showChangeEditor("build");
                focusResult("#highbay-build-title");
              }}
              onOpenStory={() => openStory()}
              onReviewApproval={openRecordedApproval}
            />
          </div>
        );
      });
      return;
    }

    if (currentView.stage === "build") {
      if (ed === null) {
        flushSync(() => {
          viewRoot.render(
            <Card className="highbay-card" role="status">
              Reading Change Card…
            </Card>
          );
        });
        return;
      }
      const saved =
        buildChangeId === ed.card.change_id && buildSnapshot?.state === "started"
          ? buildSnapshot
          : null;
      const showPrerequisite =
        ed.card.status !== "confirmed" &&
        saved === null &&
        !hasPending("get_build", ed.card.change_id) &&
        buildReadError === null;
      const latestCheck = latestVerificationFor(ed.card.change_id);
      flushSync(() => {
        viewRoot.render(
          <div
            id="highbay-result-view"
            data-change-id={ed.card.change_id}
            data-request-revision={ed.card.request_revision}
          >
            {showPrerequisite ? (
              <StagePrerequisiteCard
                stageTitle="Build"
                explanation="Confirm the open questions and concrete examples in Agree before starting a build."
                primaryGoAgree={primaryActionId === "highbay-prerequisite-go-agree"}
                onGoAgree={handleReturnToChangeCard}
              />
            ) : (
              <>
                <BuildSectionView
                  card={ed.card}
                  currentRevision={
                    changeList?.changes.find((item) => item.change_id === ed.card.change_id)
                      ?.request_revision ??
                    ed.incomingCard?.request_revision ??
                    ed.card.request_revision
                  }
                  build={buildChangeId === ed.card.change_id ? buildSnapshot : null}
                  draftedBeforeDrop={
                    buildChangeId === ed.card.change_id ? buildDraftedBeforeDrop : null
                  }
                  loading={hasPending("get_build", ed.card.change_id)}
                  error={buildReadError}
                  actionError={buildStartError}
                  starting={hasPending("start_build", ed.card.change_id)}
                  dropping={Array.from(pendingCalls).some((call) => call.op === "drop_build")}
                  openBuildRefusal={openBuildRefusal}
                  disabled={sessionState !== "active" || editorReq !== null}
                  dirty={isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples)}
                  startReceipt={buildStartReceipt}
                  findingTexts={buildFindings?.findings ?? null}
                  findingsLoading={hasPending("get_test_findings", ed.card.change_id)}
                  findingsError={findingsError}
                  check={latestCheck}
                  checkKnown={latestCheck !== null && runInputsKnown(latestCheck)}
                  checkError={
                    cardLoadErrors.get(ed.card.change_id) ??
                    (latestCheck !== null ? runRequestErrors.get(latestCheck.run_id) ?? null : null)
                  }
                  approvalAvailable={
                    approvalUnavailable === null && changeList?.approval_available === true
                  }
                  primaryBuildAction={primaryActionId === "highbay-build-start"}
                  primaryReviewAction={primaryActionId === "highbay-build-review-check"}
                  agentPicker={workerPicker("highbay-build-agents", buildWorkerRoles())}
                  agentsReady={workersReady(buildWorkerRoles())}
                  mergeAction={mergePanel(
                    buildApprovalRun(ed.card),
                    "highbay-build-merge-feedback",
                    knownTakingBuild(ed.card.change_id)
                  )}
                  onRefresh={() => readBuild(ed.card.change_id)}
                  onReadFindings={readBuildFindings}
                  onBuild={startBuild}
                  onPrepareBuild={prepareBuildConfirmation}
                  onDrop={dropBuild}
                  onOpenTeam={() => openTeam()}
                  onOpenAgree={handleReturnToChangeCard}
                  onOpenArchitecture={openArchitecture}
                  onOpenLive={() => openLive()}
                  onOpenChange={(id) => openChange(id)}
                  onReviewCheck={() => {
                    if (latestCheck !== null) reviewSavedCheck(latestCheck);
                  }}
                />
                <ReadOnlyAgreementSummary
                  card={ed.card}
                  onOpenAgree={handleReturnToChangeCard}
                  onReviewApproval={openRecordedApproval}
                />
              </>
            )}
          </div>
        );
      });
      return;
    }

    if (currentView.stage === "arch") {
      const card = ed ? ed.card : null;
      const changeId = card ? card.change_id : currentView.changeId;
      if (card !== null && card.status !== "confirmed") {
        flushSync(() => {
          viewRoot.render(
            <div
              id="highbay-result-view"
              data-change-id={card.change_id}
              data-request-revision={card.request_revision}
            >
              <StagePrerequisiteCard
                stageTitle="Architecture"
                explanation="Architecture previews the saved Goa design of an agreed revision. Confirm your intent in Agree first."
                primaryGoAgree={primaryActionId === "highbay-prerequisite-go-agree"}
                onGoAgree={handleReturnToChangeCard}
              />
            </div>
          );
        });
        return;
      }
      const preview =
        previewState !== null && previewState.change_id === changeId ? previewState : null;
      const loading = hasPending("preview_architecture", changeId);
      const error = previewErrors.get(changeId) ?? null;
      flushSync(() => {
        viewRoot.render(
          <div
            id="highbay-result-view"
            data-change-id={preview?.change_id}
            data-request-revision={preview?.request_revision}
            data-design-digest={preview?.design_digest}
          >
            {preview ? (
              <ArchitecturePreviewView
                preview={preview}
                loadedCard={card}
                loadingCard={editorReq !== null}
                cardLoadError={cardLoadErrors.get(preview.change_id) ?? null}
                onOpenChangeCard={() => openChange(preview.change_id)}
                onAskAboutElement={(elementId, elementName) =>
                  focusComposerForRole("guide", {
                    type: "architecture_element",
                    value: {
                      change_id: preview.change_id,
                      element_id: elementId,
                      element_name: elementName,
                    },
                  })
                }
              />
            ) : null}
            {loading ? (
              <Card id="highbay-cp2-loading" className="highbay-card" role="status" aria-busy="true">
                Evaluating the design. This can take a little while.
              </Card>
            ) : null}
            {error !== null ? (
              <Alert id="highbay-cp2-error" variant="destructive" role="alert">
                <strong>Could not preview the architecture. </strong>
                {error}
              </Alert>
            ) : null}
            {card !== null ? (
              <div className="highbay-field-row">
                <Button
                  id="highbay-cp2-refresh"
                  disabled={loading}
                  onClick={() => requestArchitecture(card)}
                >
                  {preview ? "Evaluate the design again" : "Evaluate the design"}
                </Button>
              </div>
            ) : null}
          </div>
        );
      });
      return;
    }

    if (currentView.stage === "approve") {
      const run = resolveDisplayedVerificationRun();
      const recordCard = ed?.card;
      const recordApproval = recordCard?.approval ?? recordCard?.unverified_approval?.approval;
      const effectiveChangeId = selectedVerificationChangeId ?? currentView.changeId;
      flushSync(() =>
        viewRoot.render(
          <div
            id="highbay-result-view"
            data-run-id={run?.run_id}
            data-change-id={run?.change_id}
            data-request-revision={run?.request_revision}
          >
            <ApprovalStage
              rawDetails={verificationDetailsView(run, effectiveChangeId, primaryActionId)}
              run={run}
              newerCheck={run !== null && newerCheckFor(run) !== null}
              review={displayedApprovalReview()}
              fresh={run !== null && approvalReviewFresh(run)}
              loading={run !== null && hasPending("review_verification", run.run_id)}
              busy={editorReq !== null}
              error={
                run !== null
                  ? runRequestErrors.get(run.run_id) ?? null
                  : cardLoadErrors.get(effectiveChangeId) ?? null
              }
              historyLoading={hasPending("list_verifications", effectiveChangeId)}
              historyError={listRequestErrors.get(effectiveChangeId) ?? null}
              approvalError={run !== null ? approveErrors.get(run.run_id) ?? null : null}
              unavailableReason={approvalUnavailable}
              build={run !== null ? knownTakingBuild(run.change_id) : null}
              unsaved={ed !== null && isEditorDirtyState(ed)}
              mergeAction={mergePanel(run, "highbay-cp5-merge-feedback")}
              savedRecord={
                recordCard !== undefined &&
                recordCard.change_id === effectiveChangeId &&
                recordApproval !== undefined
                  ? {
                      approval: recordApproval,
                      requestRevision: recordCard.request_revision,
                      reason: recordCard.unverified_approval?.reason ?? null,
                    }
                  : null
              }
              onRefresh={() => {
                if (run !== null) readApprovalReview(run.run_id, run.change_id);
                else handleLoadLatestCard();
              }}
              onOpenProof={openProof}
              onOpenAgreement={handleReturnToChangeCard}
            />
          </div>
        )
      );
      return;
    }

    // currentView.stage === "verify"
    const changeId = selectedVerificationChangeId || currentView.changeId;
    const displayedRun = resolveDisplayedVerificationRun();
    const effectiveChangeId = displayedRun ? displayedRun.change_id : changeId;
    flushSync(() =>
      viewRoot.render(
        <div
          id="highbay-result-view"
          data-run-id={displayedRun?.run_id}
          data-change-id={displayedRun ? displayedRun.change_id : changeId || undefined}
          data-request-revision={displayedRun?.request_revision}
        >
          {verificationDetailsView(displayedRun, effectiveChangeId, primaryActionId)}
        </div>
      )
    );
  }

  // buildStaleContextGuard checks whether composerDraft.context targets a
  // superseded request_revision (e.g. R2 -> R3), holding Send until reviewed.
  function buildStaleContextGuard(): {
    staleHold: boolean;
    banner: React.ReactNode;
  } {
    const ctx = composerDraft.context;
    if (ctx === null || (ctx.type !== "refine_change" && ctx.type !== "revise_answers")) {
      return { staleHold: false, banner: null };
    }
    const latest = latestRevisionTargetForChange(ctx.value.change_id);
    if (latest === null || latest.revision === ctx.value.request_revision) {
      return { staleHold: false, banner: null };
    }

    if (ctx.type === "refine_change") {
      const updatedContext: ComposerContext = {
        type: "refine_change",
        value: { change_id: ctx.value.change_id, request_revision: latest.revision },
      };
      return {
        staleHold: true,
        banner: (
          <Alert id="highbay-composer-stale-context" variant="warning" role="status">
            <p>
              Change <code>{ctx.value.change_id}</code> moved to revision{" "}
              <code>{latest.revision.slice(0, 12)}</code> while you were writing.
            </p>
            <div className="highbay-field-row">
              <Button
                id="highbay-composer-update-send"
                variant="secondary"
                disabled={
                  sessionState !== "active" ||
                  hasPending("send_team_message", "rail") ||
                  composerDraft.text.trim() === ""
                }
                onClick={() => {
                  composerDraft = { ...composerDraft, context: updatedContext };
                  submitTeamMessage("rail", {
                    to: composerDraft.to,
                    text: composerDraft.text,
                    ...(composerDraft.replyTo !== null ? { reply_to: composerDraft.replyTo } : {}),
                    context: updatedContext,
                  });
                }}
              >
                Update target &amp; send
              </Button>
              <Button
                id="highbay-composer-update-pill"
                variant="ghost"
                onClick={() => {
                  composerDraft = { ...composerDraft, context: updatedContext };
                  syncAllRegions();
                }}
              >
                {"Target " + latest.revision.slice(0, 12)}
              </Button>
            </div>
          </Alert>
        ),
      };
    }

    // ctx.type === "revise_answers": reconcile selected answers against latest.ambiguities
    // by exact question text (never positional <changeId>-ambiguity-NN index alone) and
    // omit choices the new revision's draft_option already satisfies.
    const reconciled: Record<string, string> = {};
    const sourceAmbiguities = knownAmbiguitiesByRevision.get(
      ctx.value.change_id + ":" + ctx.value.request_revision
    );
    const chosenByQuestionText = new Map<string, string>();
    if (sourceAmbiguities !== undefined) {
      for (const srcQ of sourceAmbiguities) {
        const chosen = ctx.value.answers[srcQ.id];
        if (chosen !== undefined) {
          chosenByQuestionText.set(srcQ.question, chosen);
        }
      }
    }
    if (latest.knownAmbiguities) {
      for (const q of latest.ambiguities ?? []) {
        const chosen =
          sourceAmbiguities !== undefined
            ? chosenByQuestionText.get(q.question)
            : ctx.value.answers[q.id];
        if (
          chosen !== undefined &&
          q.options.includes(chosen) &&
          (q.draft_option !== undefined && q.draft_option !== chosen)
        ) {
          reconciled[q.id] = chosen;
        }
      }
    }
    const keptCount = Object.keys(reconciled).length;
    const reconciledContext: ComposerContext = {
      type: "revise_answers",
      value: {
        change_id: ctx.value.change_id,
        request_revision: latest.revision,
        answers: reconciled,
      },
    };
    return {
      staleHold: true,
      banner: (
        <Alert id="highbay-composer-stale-context" variant="warning" role="status">
          <p id="highbay-composer-review-reconciled">
            Revision <code>{latest.revision.slice(0, 12)}</code> arrived while composing.{" "}
            {!latest.knownAmbiguities
              ? "Open Agree to load the new revision's questions before sending."
              : keptCount > 0
              ? keptCount +
                (keptCount === 1 ? " compatible answer was" : " compatible answers were") +
                " kept; removed questions or options were dropped."
              : "None of your earlier answer selections exist on the new revision."}
          </p>
          <div className="highbay-field-row">
            {latest.knownAmbiguities && keptCount > 0 ? (
              <Button
                id="highbay-composer-send-reconciled"
                variant="secondary"
                disabled={
                  sessionState !== "active" ||
                  hasPending("send_team_message", "rail") ||
                  composerDraft.text.trim() === ""
                }
                onClick={() => {
                  composerDraft = { ...composerDraft, context: reconciledContext };
                  submitTeamMessage("rail", {
                    to: composerDraft.to,
                    text: composerDraft.text,
                    ...(composerDraft.replyTo !== null ? { reply_to: composerDraft.replyTo } : {}),
                    context: reconciledContext,
                  });
                }}
              >
                Send reconciled answers
              </Button>
            ) : null}
            <Button
              id="highbay-composer-open-agree"
              variant="ghost"
              onClick={() => {
                if (latest.knownAmbiguities && keptCount > 0) {
                  composerDraft = { ...composerDraft, context: reconciledContext };
                } else {
                  composerDraft = { ...composerDraft, context: null, seededText: undefined };
                }
                openChange(ctx.value.change_id, "agree");
              }}
            >
              Review on Agree
            </Button>
          </div>
        </Alert>
      ),
    };
  }

  function syncConversationRegion(primaryActionId: string | null = resolvePrimaryActionId().actionId) {
    if (sessionState === "starting" || sessionState === "signed-out") {
      conversationRegionEl.hidden = true;
      columnsEl.dataset.rail = "hidden";
      flushSync(() => conversationRoot.render(null));
      return;
    }
    const narrow = isNarrowViewport();
    const visibleInNarrow = mobileSurface === "conversation";
    if (narrow && !visibleInNarrow) {
      conversationRegionEl.hidden = true;
      columnsEl.dataset.rail = "collapsed";
      return;
    }
    conversationRegionEl.hidden = false;
    const effectiveOpen = narrow ? true : conversationOpen;
    columnsEl.dataset.rail = effectiveOpen ? "open" : "collapsed";
    const messages = teamConversation?.messages ?? [];
    if (effectiveOpen) {
      seenMessageCount = messages.length;
    }
    const unreadCount = effectiveOpen ? 0 : Math.max(0, messages.length - seenMessageCount);
    const staleGuard = buildStaleContextGuard();

    flushSync(() =>
      conversationRoot.render(
        <ConversationRail
          open={effectiveOpen}
          unreadCount={unreadCount}
          primarySend={primaryActionId === "highbay-composer-send"}
          staleHold={staleGuard.staleHold}
          staleContextBanner={staleGuard.banner}
          detachedNotice={composerDetachedNotice}
          conversation={teamConversation}
          snapshot={teamSnapshot}
          loading={hasPending("get_team_conversation", "")}
          sending={
            hasPending("send_team_message", "rail") || hasPending("send_team_message", "agree")
          }
          error={conversationError}
          sendError={composerSendError}
          disabled={sessionState !== "active"}
          draft={composerDraft}
          guideChoice={guideChoice}
          agentOptions={agentOptions?.agents ?? null}
          agentOptionsLoading={hasPending("list_team_agent_options", "")}
          agentOptionsError={agentOptionsError}
          openTerminalRole={openTerminalRole}
          onToggleOpen={() => {
            if (isNarrowViewport()) {
              const returnTo = mobileSurfaceInvoker;
              mobileSurface = "stage";
              mobileSurfaceInvoker = null;
              syncHistoryState("replace");
              syncAllRegions();
              if (returnTo !== null && document.contains(returnTo)) {
                returnTo.focus();
              } else {
                document.getElementById("highbay-mobile-conversation-toggle")?.focus();
              }
              return;
            }
            conversationOpen = !conversationOpen;
            userCollapsedConversation = !conversationOpen;
            if (conversationOpen) {
              seenMessageCount = teamConversation?.messages.length ?? seenMessageCount;
            }
            syncAllRegions();
          }}
          onRefresh={readConversation}
          onDraftChange={(patch) => {
            composerDraft = { ...composerDraft, ...patch };
            if (patch.text !== undefined) {
              composerSendError = null;
              composerDetachedNotice = null;
            }
            syncAllRegions();
          }}
          onSwitchRecipient={handleSwitchComposerRecipient}
          onReplyToMessage={handleReplyToConversationMessage}
          onGuideChoiceChange={(choice) => {
            guideChoice = choice;
            guideChoiceEdited = true;
            syncAllRegions();
          }}
          onLoadAgentOptions={loadAgentOptions}
          onRefreshAgentOptions={readAgentOptions}
          onSend={() => {
            if (composerDraft.text.trim() === "" || staleGuard.staleHold) return;
            submitTeamMessage("rail", {
              to: composerDraft.to,
              text: composerDraft.text,
              ...(composerDraft.replyTo !== null ? { reply_to: composerDraft.replyTo } : {}),
              ...(composerDraft.context !== null ? { context: composerDraft.context } : {}),
            });
          }}
          onOpenTerminal={(role, invoker) => openRoleTerminal(role, invoker)}
        />
      )
    );
  }

  // syncTerminalRegion keeps a single TerminalDrawer instance mounted across
  // viewport width transitions so resizing never drops a WebSocket or mints a second ticket.
  function syncTerminalRegion(primaryActionId: string | null = resolvePrimaryActionId().actionId) {
    if (
      sessionState === "starting" ||
      sessionState === "signed-out" ||
      openTerminalRole === null
    ) {
      terminalRegionEl.hidden = true;
      flushSync(() => terminalRoot.render(null));
      return;
    }
    const role = openTerminalRole;
    const hideOnMobile = isNarrowViewport() && mobileSurface !== "terminal";
    terminalRegionEl.hidden = hideOnMobile;
    const roleState = teamSnapshot?.roles.find((r) => r.role === role) ?? null;
    flushSync(() =>
      terminalRoot.render(
        <TerminalDrawer
          role={role}
          roleState={roleState}
          observeMode={terminalObserveMode}
          size={terminalSize}
          primaryInteractive={primaryActionId === "highbay-terminal-interactive"}
          primaryRestart={primaryActionId === "highbay-terminal-restart"}
          disabled={sessionState !== "active"}
          interrupting={hasPending("interrupt_team_role", role)}
          stopping={hasPending("stop_team_role", role)}
          restarting={hasPending("restart_team_role", role)}
          restartSeq={terminalRestartSeq[role]}
          actionError={terminalActionError}
          onCreateTicket={createTerminalTicket}
          onSwitchInteractive={() => {
            terminalObserveMode = false;
            syncAllRegions();
          }}
          onChangeSize={(next) => {
            terminalSize = next;
            syncAllRegions();
          }}
          onInterrupt={interruptTeamRole}
          onStop={stopTeamRole}
          onRestart={restartTeamRole}
          onClose={closeRoleTerminal}
        />
      )
    );
  }

  function syncSkipLink() {
    if (skipEl === null) return;
    if (isNarrowViewport() && mobileSurface === "conversation") {
      skipEl.href = "#highbay-conversation-panel";
    } else if (isNarrowViewport() && mobileSurface === "terminal") {
      skipEl.href = "#highbay-terminal-drawer";
    } else {
      skipEl.href = "#highbay-checkpoint-view";
    }
  }

  function syncAllRegions() {
    if (syncingRegions) {
      syncAgain = true;
      return;
    }
    syncingRegions = true;
    try {
      do {
        syncAgain = false;
        statusEl.toggleAttribute("data-connection-warning", waitTroubleShown);
        syncSkipLink();
        const { attentionRole, actionId } = resolvePrimaryActionId();
        syncNoticeRegion(attentionRole);
        syncViewRegion(actionId);
        syncConversationRegion(actionId);
        syncTerminalRegion(actionId);
        syncNavRegion(actionId);
        syncTraceRegion();
        syncOpenedCheckStatus();
        statusEl.hidden =
          currentView.kind === "change" &&
          (currentView.stage === "approve" || currentView.stage === "build") &&
          [
            "Review the build and its next step. The agreed details stay available below.",
            "Review the saved check before approving it.",
            "Showing the saved verification result.",
          ].includes(statusEl.textContent ?? "");
      } while (syncAgain);
    } finally {
      syncingRegions = false;
    }
  }

  function syncOpenedCheckStatus() {
    const ed = getActiveEditor();
    if (
      sessionState !== "active" ||
      currentView.kind !== "change" ||
      (currentView.stage !== "agree" && currentView.stage !== "build") ||
      ed?.card.status !== "confirmed" ||
      editorRequestsByChangeId.has(ed.card.change_id) ||
      buildStartError !== null ||
      buildReadError !== null ||
      listError !== null ||
      waitTroubleShown ||
      cardLoadErrors.has(ed.card.change_id) ||
      (teamSnapshot?.roles.some((role) => role.state === "needs_you") ?? false) ||
      isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples)
    ) {
      return;
    }
    const run = latestVerificationFor(ed.card.change_id);
    if (run === null || !runInputsKnown(run)) return;
    let next: string;
    if (!run.inputs_current) {
      next =
        "The saved check describes earlier inputs. The builder checks the current agreement and tests again before you approve.";
    } else if (run.state.type === "running") {
      next =
        "Highbay is checking the builder’s code. Your next decision is approval after the check passes.";
    } else if (run.state.type === "completed" && run.state.value.report.passed) {
      next =
        ed.card.approval?.report_digest === run.state.value.report.report_digest
          ? "The check passed and your approval is recorded."
          : "The check passed. Review the result.";
    } else {
      next = "The check needs the builder’s attention. Review its saved result.";
    }
    if (statusEl.textContent !== next) statusEl.textContent = next;
  }

  function loadTracePage(cursor?: string) {
    if (hasPending("list_traces", "")) return;
    traceError = null;
    callServer(
      "list_traces",
      cursor ? { status: traceFilter, limit: 20, cursor } : { status: traceFilter, limit: 20 },
      "",
      (page) => {
        tracePage = page;
      },
      (message) => {
        traceError = message;
      }
    );
  }

  function syncTraceRegion() {
    if (sessionState === "starting" || sessionState === "signed-out") {
      traceRegionEl.hidden = true;
      flushSync(() => traceRoot.render(null));
      return;
    }
    traceRegionEl.hidden = !traceOpen;
    flushSync(() =>
      traceRoot.render(
        <TraceDrawer
          open={traceOpen}
          filter={traceFilter}
          page={tracePage}
          error={traceError}
          busy={hasPending("list_traces", "")}
          onToggle={() => {
            traceOpen = !traceOpen;
            if (traceOpen && tracePage === null) loadTracePage();
            syncAllRegions();
          }}
          onFilter={(filter) => {
            traceFilter = filter;
            tracePage = null;
            loadTracePage();
          }}
          onRefresh={() => loadTracePage()}
          onOlder={() => loadTracePage(tracePage?.next_cursor)}
        />
      )
    );
  }

  function markRunReadPending(runId: string, order: number) {
    runObservationOrder.set(runId, order);
    runObservationGeneration.delete(runId);
  }

  function upsertRunSnapshot(
    run: VerificationStatusResult,
    generation: number,
    order: number
  ): VerificationStatusResult {
    const existing = runsById.get(run.run_id);
    const observed =
      generation === verificationGeneration && order >= (runObservationOrder.get(run.run_id) ?? -1);
    const evidence = existing && existing.state.type !== "running" ? existing : run;
    const snapshot = {
      ...evidence,
      inputs_current: observed ? run.inputs_current : existing?.inputs_current ?? false,
      candidate_binaries_dir: observed
        ? run.candidate_binaries_dir
        : existing?.candidate_binaries_dir,
    };
    runsById.set(run.run_id, snapshot);
    if (observed) {
      runObservationGeneration.set(run.run_id, generation);
      runObservationOrder.set(run.run_id, order);
      runRequestErrors.delete(run.run_id);
    }
    return snapshot;
  }

  function handleSelectVerificationRun(runId: string) {
    const target = runsById.get(runId);
    if (!target) {
      return;
    }
    navigationSeq++;
    if (hasPending("list_verifications", target.change_id)) {
      selectionTouchedDuringList.set(target.change_id, true);
    }
    activeChangeId = target.change_id;
    selectedVerificationChangeId = target.change_id;
    selectedRunId = target.run_id;
    const reviewing = currentView.kind === "change" && currentView.stage === "approve";
    currentView = {
      kind: "change",
      changeId: target.change_id,
      stage: reviewing ? "approve" : "verify",
    };
    statusEl.textContent = "Showing the saved verification result.";
    if (reviewing || !runInputsKnown(target) || displayedApprovalReview() === null) {
      readVerification(target.run_id, target.change_id);
    }
    syncHistoryState("push");
    syncAllRegions();
  }

  function handleCheckVerificationStatus() {
    const run = resolveDisplayedVerificationRun();
    if (
      currentView.kind === "change" &&
      (currentView.stage === "verify" || currentView.stage === "approve") &&
      run !== null
    ) {
      readVerification(run.run_id, run.change_id);
    }
  }

  function readVerification(runID: string, changeId: string) {
    if (isViewingSelectedRun(runID)) {
      readApprovalReview(runID, changeId);
      return;
    }
    if (sessionState !== "active") return;
    if (hasPending("get_verification", runID)) {
      runReadAgain.add(runID);
      return;
    }
    runReadAgain.delete(runID);
    const generation = verificationGeneration;
    const order = ++verificationReadOrder;
    markRunReadPending(runID, order);
    const finish = () => {
      const again = runReadAgain.delete(runID) || generation !== verificationGeneration;
      if (
        again &&
        sessionState === "active" &&
        (activeChangeId === changeId || selectedVerificationChangeId === changeId)
      ) {
        readVerification(runID, changeId);
      }
    };
    runRequestErrors.delete(runID);
    callServer(
      "get_verification",
      { run_id: runID },
      runID,
      (run) => {
        if (run.run_id !== runID || run.change_id !== changeId) {
          if (
            generation === verificationGeneration &&
            order >= (runObservationOrder.get(runID) ?? -1)
          ) {
            runRequestErrors.set(runID, "The server returned a different verification run.");
          }
          finish();
          return;
        }
        upsertRunSnapshot(run, generation, order);
        if (
          generation === verificationGeneration &&
          currentView.kind === "change" &&
          currentView.stage === "verify" &&
          selectedRunId === runID
        ) {
          statusEl.textContent = "Showing the saved verification result.";
        }
        const pendingOpenSeq = pendingDefaultVerifyCheck.get(changeId);
        if (
          pendingOpenSeq !== undefined &&
          navigationSeq === pendingOpenSeq &&
          sessionState === "active" &&
          currentView.kind === "change" &&
          currentView.changeId === changeId &&
          currentView.stage === "build"
        ) {
          pendingDefaultVerifyCheck.delete(changeId);
          const latestEd = editorsByChangeId.get(changeId);
          const currentBuild = buildChangeId === changeId ? buildSnapshot : null;
          if (latestEd !== undefined && currentBuild !== null) {
            const nextStage = computeDefaultStageForConfirmed(latestEd, currentBuild);
            if (nextStage !== "build") {
              activateChangeStage(changeId, nextStage, "replace");
            }
          }
        }
        finish();
      },
      (message) => {
        if (pendingRestoredRunByChangeId.get(changeId) === runID) {
          pendingRestoredRunByChangeId.delete(changeId);
          const fallback = lastLoadedListByChangeId.get(changeId)?.[0] ?? null;
          if (selectedVerificationChangeId === changeId && selectedRunId === runID && fallback !== null) {
            selectedRunId = fallback;
            syncHistoryState("replace");
          }
        }
        const pendingOpenSeq = pendingDefaultVerifyCheck.get(changeId);
        if (pendingOpenSeq !== undefined) {
          pendingDefaultVerifyCheck.delete(changeId);
          if (
            navigationSeq === pendingOpenSeq &&
            sessionState === "active" &&
            currentView.kind === "change" &&
            currentView.changeId === changeId &&
            currentView.stage === "build"
          ) {
            const latestEd = editorsByChangeId.get(changeId);
            const currentBuild = buildChangeId === changeId ? buildSnapshot : null;
            if (latestEd !== undefined && currentBuild !== null) {
              const nextStage = computeDefaultStageForConfirmed(latestEd, currentBuild);
              if (nextStage !== "build") {
                activateChangeStage(changeId, nextStage, "replace");
              }
            }
          }
        }
        if (
          generation === verificationGeneration &&
          order >= (runObservationOrder.get(runID) ?? -1)
        ) {
          runRequestErrors.set(runID, message);
          if (
            currentView.kind === "change" &&
            currentView.stage === "verify" &&
            selectedRunId === runID
          ) {
            statusEl.textContent =
              "Could not check verification status; the last result is preserved.";
          }
        }
        finish();
      }
    );
  }

  function readApprovalReview(runId: string, changeId: string) {
    if (sessionState !== "active") return;
    if (hasPending("review_verification", runId)) {
      runReadAgain.add(runId);
      return;
    }
    runReadAgain.delete(runId);
    const generation = verificationGeneration;
    const order = ++verificationReadOrder;
    markRunReadPending(runId, order);
    runRequestErrors.delete(runId);
    const finish = () => {
      const again = runReadAgain.delete(runId) || generation !== verificationGeneration;
      if (
        again &&
        sessionState === "active" &&
        (isViewingSelectedRun(runId) || activeChangeId === changeId)
      ) {
        readApprovalReview(runId, changeId);
      }
    };
    callServer(
      "review_verification",
      { run_id: runId },
      runId,
      (result) => {
        if (result.run_id !== runId || result.change_id !== changeId) {
          if (generation === verificationGeneration) {
            runRequestErrors.set(runId, "The server returned a different verification run.");
          }
          finish();
          return;
        }
        if (
          result.review.approval.state === "available" &&
          (result.state.type !== "completed" || !result.state.value.report.passed)
        ) {
          if (generation === verificationGeneration) {
            runRequestErrors.set(
              runId,
              "The owner returned approval availability without a passed report. Refresh this result."
            );
          }
          finish();
          return;
        }
        upsertRunSnapshot(result, generation, order);
        if (
          generation === verificationGeneration &&
          order >= (runObservationOrder.get(runId) ?? -1)
        ) {
          approvalUnavailable = null;
          reviewsByRunId.set(runId, { generation, order, review: result.review });
        }
        finish();
      },
      (message, failure) => {
        if (pendingRestoredRunByChangeId.get(changeId) === runId) {
          pendingRestoredRunByChangeId.delete(changeId);
          const fallback = lastLoadedListByChangeId.get(changeId)?.[0] ?? null;
          if (selectedVerificationChangeId === changeId && selectedRunId === runId && fallback !== null) {
            selectedRunId = fallback;
            syncHistoryState("replace");
          }
        }
        if (
          generation === verificationGeneration &&
          order >= (runObservationOrder.get(runId) ?? -1)
        ) {
          if (failure.status === 501 && selectedRunId === runId) {
            approvalUnavailable = message;
          }
          runRequestErrors.set(runId, message);
        }
        finish();
      }
    );
  }

  function handleListVerifications(requestedChangeId: string) {
    if (!requestedChangeId || hasPending("list_verifications", requestedChangeId)) {
      return;
    }
    navigationSeq++;
    mobileSurface = "stage";
    const reviewing = currentView.kind === "change" && currentView.stage === "approve";
    selectionTouchedDuringList.set(requestedChangeId, false);
    listRequestErrors.delete(requestedChangeId);
    const nextRunId =
      selectedRunId !== null && runsById.get(selectedRunId)?.change_id !== requestedChangeId
        ? lastLoadedListByChangeId.get(requestedChangeId)?.[0] ?? null
        : selectedRunId;
    activeChangeId = requestedChangeId;
    selectedVerificationChangeId = requestedChangeId;
    selectedRunId = nextRunId;
    const stage: ChangeStageName = reviewing ? "approve" : "verify";
    currentView = { kind: "change", changeId: requestedChangeId, stage };
    statusEl.textContent = "Loading saved verification runs...";
    syncHistoryState("push");
    const generation = verificationGeneration;
    const order = ++verificationReadOrder;
    for (const run of runsById.values()) {
      if (run.change_id === requestedChangeId) markRunReadPending(run.run_id, order);
    }

    callServer(
      "list_verifications",
      { change_id: requestedChangeId },
      requestedChangeId,
      (payload) => {
        const touched = Boolean(selectionTouchedDuringList.get(requestedChangeId));
        selectionTouchedDuringList.delete(requestedChangeId);
        const mismatchErr = validateVerificationListPayload(payload, requestedChangeId);
        if (mismatchErr !== null) {
          listRequestErrors.set(requestedChangeId, mismatchErr);
          if (
            currentView.kind === "change" &&
            (currentView.stage === "verify" || currentView.stage === "approve") &&
            selectedVerificationChangeId === requestedChangeId
          ) {
            statusEl.textContent = "Could not load saved verification runs.";
          }
          return;
        }
        for (let i = 0; i < payload.runs.length; i++) {
          upsertRunSnapshot(payload.runs[i], generation, order);
        }
        const loadedIds = payload.runs.map((r) => r.run_id);
        lastLoadedListByChangeId.set(requestedChangeId, loadedIds);
        listRequestErrors.delete(requestedChangeId);

        if (
          currentView.kind === "change" &&
          (currentView.stage === "verify" || currentView.stage === "approve") &&
          selectedVerificationChangeId === requestedChangeId
        ) {
          if (
            touched &&
            selectedRunId !== null &&
            runsById.get(selectedRunId)?.change_id === requestedChangeId
          ) {
            // Preserve run explicitly selected during list read.
          } else if (selectedRunId !== null && loadedIds.includes(selectedRunId)) {
            // Preserve selected run when still in loaded list.
          } else {
            selectedRunId = loadedIds.length > 0 ? loadedIds[0] : null;
          }
          statusEl.textContent =
            loadedIds.length > 0
              ? "Showing saved verification runs for Change Card " + requestedChangeId + "."
              : "No saved verification runs found for Change Card " + requestedChangeId + ".";
          syncHistoryState("replace");
        }
        if (
          selectedRunId !== null &&
          (generation !== verificationGeneration || isViewingSelectedRun(selectedRunId))
        ) {
          readVerification(selectedRunId, requestedChangeId);
        }
      },
      (message) => {
        selectionTouchedDuringList.delete(requestedChangeId);
        listRequestErrors.set(requestedChangeId, message);
        if (
          currentView.kind === "change" &&
          (currentView.stage === "verify" || currentView.stage === "approve") &&
          selectedVerificationChangeId === requestedChangeId
        ) {
          statusEl.textContent = "Could not load saved verification runs.";
        }
      }
    );
  }

  function handleReturnToChangeCard() {
    showChangeEditor("agree");
  }

  function handleReadFullSummary() {
    showChangeEditor("agree");
    const details = document.getElementById("highbay-agreement-details");
    if (details instanceof HTMLDetailsElement) details.open = true;
    focusResult("#highbay-agreement-summary");
    document.getElementById("highbay-agreement-summary")?.scrollIntoView({ block: "start" });
  }

  function showChangeEditor(stage: "agree" | "build") {
    const ed = getActiveEditor();
    if (!ed) {
      return;
    }
    navigationSeq++;
    mobileSurface = "stage";
    currentView = { kind: "change", changeId: ed.card.change_id, stage };
    const card = ed.card;
    statusEl.textContent = ed.incomingCard
      ? "A newer Change Card arrived while you were editing; your unsaved draft is preserved."
      : card.proposed_revision !== undefined
      ? "Review the proposed revision, then choose whether to adopt or decline it."
      : card.status === "confirmed"
      ? card.approval
        ? "Showing the recorded approval for these inputs."
        : card.unverified_approval
        ? "Showing the saved approval record (current promotion inputs could not be verified)."
        : stage === "build"
        ? "Review the build and its next step. The agreed details stay available below."
        : "Review the current agreement."
      : "Review the open questions and examples below, then confirm your intent.";
    syncHistoryState("push");
    syncAllRegions();
  }

  // computeDefaultStageForConfirmed chooses the truthful opening stage after
  // get_change and get_build have settled, checking accepted_at >= started_at
  // on the fetched run so an older build's failure is never misattributed and a
  // newer current-build failure opens Verify ahead of an unmerged prior pass.
  function computeDefaultStageForConfirmed(
    ed: ActiveEditorState,
    build: ChangeBuild["build"] | null
  ): ChangeStageName {
    if (
      ed.card.status !== "confirmed" ||
      ed.card.proposed_revision !== undefined ||
      ed.incomingCard !== null ||
      isEditorDirtyState(ed)
    ) {
      return "agree";
    }
    if (
      build !== null &&
      build.state === "started" &&
      build.in_force &&
      build.request_revision === ed.card.request_revision
    ) {
      if (build.taking.state === "merged") {
        return "approve";
      }
      const latestRun = latestVerificationFor(ed.card.change_id);
      if (
        latestRun !== null &&
        latestRun.inputs_current &&
        runStartedInCurrentBuild(ed.card, build, latestRun) &&
        (latestRun.state.type === "not_completed" ||
          (latestRun.state.type === "completed" && !latestRun.state.value.report.passed))
      ) {
        return "verify";
      }
      if (build.taking.state === "passed" && !pendingDefaultVerifyCheck.has(ed.card.change_id)) {
        return "approve";
      }
    }
    return "build";
  }

  // activateChangeStage enters the chosen stage for an already-stored editor.
  function activateChangeStage(
    changeId: string,
    stage: ChangeStageName,
    historyMode: "push" | "replace",
    targetMobileSurface: MobileSurface = "stage"
  ) {
    activeChangeId = changeId;
    mobileSurface = targetMobileSurface;
    switchComposerScope(composerScopeFor("change", changeId));
    const ed = editorsByChangeId.get(changeId);
    const restoredRun = pendingRestoredRunByChangeId.get(changeId) ?? null;
    if (stage === "approve") {
      selectedVerificationChangeId = changeId;
      const approval = ed?.card.approval ?? ed?.card.unverified_approval?.approval;
      const savedRun =
        ed !== undefined && approval !== undefined
          ? savedApprovalEvidenceRun(ed.card, approval)
          : null;
      const currentSelectedForChange =
        restoredRun ??
        (selectedRunId !== null && runsById.get(selectedRunId)?.change_id === changeId
          ? selectedRunId
          : null);
      const run = savedRun ?? latestVerificationFor(changeId);
      selectedRunId = currentSelectedForChange ?? run?.run_id ?? null;
      currentView = { kind: "change", changeId, stage: "approve" };
      statusEl.textContent = "Review the saved check before approving it.";
      if (!lastLoadedListByChangeId.has(changeId)) {
        refreshVerificationsInBackground(changeId);
      }
      if (selectedRunId !== null) {
        readApprovalReview(selectedRunId, changeId);
      }
      syncHistoryState(historyMode);
      syncAllRegions();
      return;
    }
    if (stage === "verify") {
      selectedVerificationChangeId = changeId;
      const currentSelectedForChange =
        restoredRun ??
        (selectedRunId !== null && runsById.get(selectedRunId)?.change_id === changeId
          ? selectedRunId
          : null);
      const run = latestVerificationFor(changeId);
      selectedRunId =
        currentSelectedForChange ??
        run?.run_id ??
        lastLoadedListByChangeId.get(changeId)?.[0] ??
        null;
      currentView = { kind: "change", changeId, stage: "verify" };
      statusEl.textContent =
        selectedRunId !== null
          ? "Showing the saved verification result."
          : "Loading saved verification runs...";
      if (!lastLoadedListByChangeId.has(changeId)) {
        refreshVerificationsInBackground(changeId);
      }
      if (selectedRunId !== null) {
        readVerification(selectedRunId, changeId);
      }
      syncHistoryState(historyMode);
      syncAllRegions();
      return;
    }
    if (stage === "arch") {
      currentView = { kind: "change", changeId, stage: "arch" };
      if (ed !== undefined && ed.card.status === "confirmed") {
        if (
          previewState === null ||
          previewState.change_id !== changeId ||
          previewState.request_revision !== ed.card.request_revision
        ) {
          requestArchitecture(ed.card);
        }
      }
      syncHistoryState(historyMode);
      syncAllRegions();
      return;
    }
    currentView = { kind: "change", changeId, stage };
    if (ed !== undefined) {
      const card = ed.card;
      statusEl.textContent = ed.incomingCard
        ? "A newer Change Card arrived while you were editing; your unsaved draft is preserved."
        : card.proposed_revision !== undefined
        ? "Review the proposed revision, then choose whether to adopt or decline it."
        : card.status === "confirmed"
        ? card.approval
          ? "Showing the recorded approval for these inputs."
          : card.unverified_approval
          ? "Showing the saved approval record (current promotion inputs could not be verified)."
          : stage === "build"
          ? "Review the build and its next step. The agreed details stay available below."
          : "Review the current agreement."
        : "Review the open questions and examples below, then confirm your intent.";
    }
    syncHistoryState(historyMode);
    syncAllRegions();
  }

  // armPendingVerifyCheck records openSeq when a confirmed card's latest listed
  // verification failed/stopped and its full run snapshot is still loading.
  function armPendingVerifyCheck(changeId: string, openSeq: number) {
    const listedVer = changeList?.changes.find((c) => c.change_id === changeId)?.latest_verification;
    if (
      listedVer !== undefined &&
      listedVer.inputs_current &&
      (listedVer.state === "failed" || listedVer.state === "not_completed") &&
      !runsById.has(listedVer.run_id)
    ) {
      pendingDefaultVerifyCheck.set(changeId, openSeq);
    } else {
      pendingDefaultVerifyCheck.delete(changeId);
    }
  }

  // openChange opens requestedChangeId in editorsByChangeId, preserving any
  // unsaved drafts on other cards and awaiting get_build before mounting a
  // confirmed card's default stage so it never flashes Agree then jumps.
  function openChange(
    requestedChangeId: string,
    explicitStage?: ChangeStageName,
    options?: {
      historyMode?: "push" | "replace";
      mobileSurface?: MobileSurface;
      restoreRunId?: string;
    }
  ) {
    if (!requestedChangeId) {
      return;
    }
    const initialHistoryMode = options?.historyMode ?? (handlingPopState ? "replace" : "push");
    const targetMobileSurface = options?.mobileSurface ?? "stage";
    if (options?.restoreRunId !== undefined) {
      pendingRestoredRunByChangeId.set(requestedChangeId, options.restoreRunId);
      selectedRunId = options.restoreRunId;
      selectedVerificationChangeId = requestedChangeId;
    }

    const existing = editorsByChangeId.get(requestedChangeId);
    if (existing !== undefined) {
      const openSeq = ++navigationSeq;
      activeChangeId = requestedChangeId;
      refreshOpenCard(requestedChangeId);
      if (explicitStage !== undefined) {
        pendingDefaultVerifyCheck.delete(requestedChangeId);
        readBuild(requestedChangeId);
        refreshVerificationObservations(requestedChangeId);
        activateChangeStage(
          requestedChangeId,
          explicitStage,
          initialHistoryMode,
          targetMobileSurface
        );
        return;
      }
      if (
        existing.card.status !== "confirmed" ||
        existing.card.proposed_revision !== undefined ||
        existing.incomingCard !== null ||
        isEditorDirtyState(existing)
      ) {
        pendingDefaultVerifyCheck.delete(requestedChangeId);
        readBuild(requestedChangeId);
        refreshVerificationObservations(requestedChangeId);
        activateChangeStage(requestedChangeId, "agree", initialHistoryMode, targetMobileSurface);
        return;
      }
      if (buildChangeId === requestedChangeId && buildSnapshot !== null && buildReadError === null) {
        armPendingVerifyCheck(requestedChangeId, openSeq);
        refreshVerificationObservations(requestedChangeId);
        activateChangeStage(
          requestedChangeId,
          computeDefaultStageForConfirmed(existing, buildSnapshot),
          initialHistoryMode,
          targetMobileSurface
        );
        return;
      }
      armPendingVerifyCheck(requestedChangeId, openSeq);
      refreshVerificationObservations(requestedChangeId);
      activateChangeStage(requestedChangeId, "build", initialHistoryMode, targetMobileSurface);
      readBuild(requestedChangeId, (settled) => {
        if (navigationSeq !== openSeq || sessionState !== "active") {
          return;
        }
        const latestEd = editorsByChangeId.get(requestedChangeId) ?? existing;
        const target = computeDefaultStageForConfirmed(latestEd, settled?.build ?? null);
        if (target !== "build") {
          activateChangeStage(requestedChangeId, target, "replace", mobileSurface);
        }
      });
      return;
    }

    const openSeq = ++navigationSeq;
    pendingOpenByChangeId.set(requestedChangeId, {
      openSeq,
      explicitStage,
      historyMode: initialHistoryMode,
      mobileSurface: targetMobileSurface,
    });
    if (editorRequestsByChangeId.has(requestedChangeId)) {
      return;
    }
    lastOpenAttempt = requestedChangeId;
    cardLoadErrors.delete(requestedChangeId);
    statusEl.textContent = "Loading Change Card " + requestedChangeId + "...";
    callEditorServer(
      "get_change",
      { change_id: requestedChangeId },
      requestedChangeId,
      (loadedCard) => {
        const pendingOpen = pendingOpenByChangeId.get(requestedChangeId);
        pendingOpenByChangeId.delete(requestedChangeId);
        const effectiveSeq = pendingOpen?.openSeq ?? openSeq;
        const effectiveStage = pendingOpen !== undefined ? pendingOpen.explicitStage : explicitStage;
        const effectiveHistoryMode = pendingOpen?.historyMode ?? initialHistoryMode;
        const effectiveMobileSurface = pendingOpen?.mobileSurface ?? targetMobileSurface;
        if (loadedCard.change_id !== requestedChangeId) {
          cardLoadErrors.set(
            requestedChangeId,
            "The server returned Change Card " +
              loadedCard.change_id +
              " instead of " +
              requestedChangeId +
              "."
          );
          if (navigationSeq === effectiveSeq) {
            statusEl.textContent = "Could not load the requested Change Card.";
          }
          return;
        }
        recordObservedCard(loadedCard);
        cardLoadErrors.delete(requestedChangeId);
        const newEd: ActiveEditorState = {
          card: loadedCard,
          priorCard: null,
          incomingCard: null,
          ...editorDraftFrom(loadedCard),
          preservedDraft: null,
          formError: null,
        };
        editorsByChangeId.set(loadedCard.change_id, newEd);
        if (navigationSeq !== effectiveSeq) {
          return;
        }
        activeChangeId = loadedCard.change_id;
        if (
          effectiveStage !== undefined ||
          loadedCard.status !== "confirmed" ||
          loadedCard.proposed_revision !== undefined
        ) {
          pendingDefaultVerifyCheck.delete(loadedCard.change_id);
          const targetStage = effectiveStage ?? "agree";
          readBuild(loadedCard.change_id);
          refreshVerificationObservations(loadedCard.change_id);
          activateChangeStage(
            loadedCard.change_id,
            targetStage,
            effectiveHistoryMode,
            effectiveMobileSurface
          );
          return;
        }
        armPendingVerifyCheck(loadedCard.change_id, effectiveSeq);
        refreshVerificationObservations(loadedCard.change_id);
        activateChangeStage(
          loadedCard.change_id,
          "build",
          effectiveHistoryMode,
          effectiveMobileSurface
        );
        readBuild(loadedCard.change_id, (settled) => {
          if (navigationSeq !== effectiveSeq || sessionState !== "active") {
            return;
          }
          const latestEd = editorsByChangeId.get(loadedCard.change_id) ?? newEd;
          const targetStage = computeDefaultStageForConfirmed(latestEd, settled?.build ?? null);
          if (targetStage !== "build") {
            activateChangeStage(loadedCard.change_id, targetStage, "replace", mobileSurface);
          }
        });
      },
      (message) => {
        const pendingOpen = pendingOpenByChangeId.get(requestedChangeId);
        pendingOpenByChangeId.delete(requestedChangeId);
        cardLoadErrors.set(requestedChangeId, "Could not load Change Card: " + message);
        if (navigationSeq === (pendingOpen?.openSeq ?? openSeq)) {
          statusEl.textContent = "Could not load the requested Change Card.";
        }
      }
    );
  }

  // applyHistoryViewState restores opaque navigation state on Back/Forward or reload
  // strictly via replace mode without mutating Forward history or clobbering mobileSurface.
  function applyHistoryViewState(state: HistoryViewState) {
    handlingPopState = true;
    try {
      const restoredSurface = state.mobileSurface ?? "stage";
      draftSentMessageId = state.newDraft?.sentMessageId;
      switch (state.view) {
        case "changes":
          showChanges({ historyMode: "replace", mobileSurface: restoredSurface });
          break;
        case "new":
          openNewChange({ historyMode: "replace", mobileSurface: restoredSurface });
          break;
        case "team":
          openTeam({ historyMode: "replace", mobileSurface: restoredSurface });
          break;
        case "story":
          if (
            state.changeId !== undefined &&
            (editorsByChangeId.has(state.changeId) ||
              (changeList?.changes.some((c) => c.change_id === state.changeId) ?? false))
          ) {
            activeChangeId = state.changeId;
            loadCardIntoCache(state.changeId);
          }
          openStory({ historyMode: "replace", mobileSurface: restoredSurface });
          break;
        case "live":
          if (
            state.changeId !== undefined &&
            (editorsByChangeId.has(state.changeId) ||
              (changeList?.changes.some((c) => c.change_id === state.changeId) ?? false))
          ) {
            activeChangeId = state.changeId;
            loadCardIntoCache(state.changeId);
          }
          openLive({ historyMode: "replace", mobileSurface: restoredSurface });
          break;
        case "change":
          if (state.changeId !== undefined) {
            openChange(state.changeId, state.stage, {
              historyMode: "replace",
              mobileSurface: restoredSurface,
              restoreRunId: state.runId,
            });
          } else {
            showChanges({ historyMode: "replace", mobileSurface: restoredSurface });
          }
          break;
      }
    } finally {
      handlingPopState = false;
    }
  }

  function isViewingChangeOnAgree(changeId: string): boolean {
    return (
      activeChangeId === changeId &&
      currentView.kind === "change" &&
      currentView.changeId === changeId &&
      currentView.stage === "agree"
    );
  }

  function showFormError(
    changeId: string,
    message: string,
    source: "input" | "request" = "request"
  ) {
    const ed = editorsByChangeId.get(changeId);
    if (!ed) {
      return;
    }
    editorsByChangeId.set(changeId, {
      ...ed,
      formError: { message, source },
    });
    if (isViewingChangeOnAgree(changeId)) {
      statusEl.textContent =
        source === "input" ? message : "Change Card request failed; unsaved edits preserved.";
    }
    syncAllRegions();
  }

  function focusResult(selector: string) {
    const target = document.querySelector(selector);
    if (target instanceof HTMLElement) {
      target.focus();
    }
  }

  function unansweredQuestions(editor: ActiveEditorState) {
    return (editor.card.ambiguities ?? []).filter(
      (question) =>
        !Object.prototype.hasOwnProperty.call(editor.draftAnswers, question.id) ||
        !editor.draftAnswers[question.id]
    );
  }

  function focusDraftQuestion(questionId: string) {
    Array.from(viewEl.querySelectorAll<HTMLButtonElement>("button[data-ambiguity-id]"))
      .find((option) => option.dataset.ambiguityId === questionId)
      ?.focus();
  }

  function firstIncompleteExample(editor: ActiveEditorState) {
    for (const [index, example] of editor.draftExamples.entries()) {
      const blank = !example.scenario.trim()
        ? { field: "scenario", label: "a scenario" }
        : !example.input_summary.trim()
        ? { field: "input_summary", label: "an input" }
        : !example.expected_outcome.trim()
        ? { field: "expected_outcome", label: "an expected result" }
        : null;
      if (blank !== null) return { ...blank, exampleId: example.id, number: index + 1 };
    }
    return null;
  }

  function focusDraftExample(issue: { exampleId: string; field: string }) {
    Array.from(viewEl.querySelectorAll<HTMLTextAreaElement>("textarea[data-example-id]"))
      .find(
        (field) =>
          field.dataset.exampleId === issue.exampleId && field.dataset.field === issue.field
      )
      ?.focus();
  }

  function handleConfirmSubmit() {
    const ed = getActiveEditor();
    if (!ed || editorRequestsByChangeId.has(ed.card.change_id)) {
      return;
    }
    if (answersNeedingRevision(ed.card.ambiguities, ed.draftAnswers).length > 0) {
      focusResult("#highbay-confirm-revision-needed");
      return;
    }
    const unanswered = unansweredQuestions(ed)[0];
    if (unanswered !== undefined) {
      showFormError(
        ed.card.change_id,
        "Choose an answer for “" + unanswered.question + "” before confirming.",
        "input"
      );
      focusDraftQuestion(unanswered.id);
      return;
    }
    const exampleIssue = firstIncompleteExample(ed);
    if (exampleIssue !== null) {
      showFormError(
        ed.card.change_id,
        "Enter " + exampleIssue.label + " for example " + exampleIssue.number + ".",
        "input"
      );
      focusDraftExample(exampleIssue);
      return;
    }
    editorsByChangeId.set(ed.card.change_id, {
      ...ed,
      formError: null,
    });
    statusEl.textContent = "Saving your confirmed answers and examples...";

    const submittedCard = ed.card;
    const submittedAnswers = { ...ed.draftAnswers };
    const submittedExamples = ed.draftExamples.map((item) => ({
      id: item.id,
      scenario: item.scenario,
      input_summary: item.input_summary,
      expected_outcome: item.expected_outcome,
      verification_kind: item.verification_kind,
    }));

    callEditorServer(
      "confirm_intent",
      {
        change_id: submittedCard.change_id,
        request_revision: submittedCard.request_revision,
        answers: submittedAnswers,
        examples: submittedExamples,
      },
      submittedCard.change_id,
      (updatedCard) => {
        const currentEd = editorsByChangeId.get(submittedCard.change_id) ?? null;
        mountChangeCard(
          updatedCard,
          null,
          nextPriorCard(currentEd, updatedCard),
          "agree"
        );
        if (isViewingChangeOnAgree(submittedCard.change_id)) {
          focusResult("#highbay-cp1-editor");
        }
      },
      (message, failure) => {
        showFormError(submittedCard.change_id, message);
        if (isViewingChangeOnAgree(submittedCard.change_id)) {
          focusResult("#highbay-confirm-error");
        }
        if (failure.status === 0) {
          refreshOpenCard(submittedCard.change_id);
        }
      }
    );
  }

  // handleAdopt confirms the waiting proposal and subtracts fields already
  // matching adopted.answers / adopted.examples before snapshotUnsavedDraft.
  function handleAdopt() {
    const ed = getActiveEditor();
    if (!ed || editorRequestsByChangeId.has(ed.card.change_id)) {
      return;
    }
    const card = ed.card;
    const proposal = card.proposed_revision;
    if (proposal === undefined) {
      return;
    }
    if (answersNeedingRevision(proposal.ambiguities, ed.proposalAnswers).length > 0) {
      focusResult("#highbay-proposal-revision-needed");
      return;
    }
    const answers: Record<string, string> = {};
    for (const question of proposal.ambiguities ?? []) {
      answers[question.id] = ed.proposalAnswers[question.id];
    }
    statusEl.textContent = "Adopting the proposed revision...";
    callEditorServer(
      "confirm_intent",
      {
        change_id: card.change_id,
        request_revision: proposal.request_revision,
        answers,
        examples: proposal.examples,
      },
      card.change_id,
      (adopted) => {
        const currentEd = editorsByChangeId.get(card.change_id) ?? null;
        const preserved = currentEd ? snapshotUnsavedDraft(currentEd, adopted) : null;
        mountChangeCard(adopted, preserved, nextPriorCard(currentEd, adopted), "agree");
        if (isViewingChangeOnAgree(card.change_id)) {
          statusEl.textContent = "Adopted the proposed revision; it is now the agreed revision.";
          focusResult("#highbay-cp1-editor");
        }
      },
      (message, failure) => {
        failProposal(message, failure, card.change_id);
      }
    );
  }

  function handleDecline() {
    const ed = getActiveEditor();
    if (!ed || editorRequestsByChangeId.has(ed.card.change_id)) {
      return;
    }
    const card = ed.card;
    const proposal = card.proposed_revision;
    if (proposal === undefined) {
      return;
    }
    statusEl.textContent = "Declining the proposed revision...";
    callEditorServer(
      "decline_revision",
      { change_id: card.change_id, request_revision: proposal.request_revision },
      card.change_id,
      (declined) => {
        recordObservedCard(declined);
        const currentEd = editorsByChangeId.get(card.change_id);
        if (currentEd && currentEd.card.request_revision === declined.request_revision) {
          editorsByChangeId.set(card.change_id, {
            ...currentEd,
            card: declined,
            proposalAnswers: {},
            proposalError: null,
          });
        } else {
          mountChangeCard(
            declined,
            null,
            nextPriorCard(currentEd ?? null, declined),
            "agree"
          );
        }
        syncAllRegions();
        if (isViewingChangeOnAgree(card.change_id)) {
          statusEl.textContent =
            "Declined the proposed revision; the agreed revision stays in force.";
          focusResult("#highbay-cp1-editor");
        }
      },
      (message, failure) => {
        failProposal(message, failure, card.change_id);
      }
    );
  }

  function failProposal(message: string, failure: Failure, changeId: string) {
    const currentEd = editorsByChangeId.get(changeId);
    if (currentEd) {
      editorsByChangeId.set(changeId, { ...currentEd, proposalError: message });
    }
    if (isViewingChangeOnAgree(changeId)) {
      statusEl.textContent = "The proposed revision was not changed.";
    }
    syncAllRegions();
    if (isViewingChangeOnAgree(changeId)) {
      focusResult("#highbay-proposal-error");
    }
    if (failure.status === 0 || failure.status === 409) {
      refreshOpenCard(changeId);
    }
  }

  function handleApprove(run: VerificationStatusResult) {
    const current = runsById.get(run.run_id);
    const review = displayedApprovalReview();
    if (
      current === undefined ||
      current.run_id !== selectedRunId ||
      editorRequestsByChangeId.has(current.change_id) ||
      !approvalReviewFresh(current) ||
      review?.approval.state !== "available" ||
      current.state.type !== "completed"
    ) {
      return;
    }
    const report = current.state.value.report;
    approveErrors.delete(current.run_id);
    statusEl.textContent = "Recording approval of this result…";
    callEditorServer(
      "approve_merge",
      {
        change_id: current.change_id,
        request_revision: current.request_revision,
        candidate_commit: current.candidate_commit,
        report_digest: report.report_digest,
      },
      current.change_id,
      (approved) => {
        if (editorsByChangeId.has(approved.change_id)) {
          receiveLatestCard(approved);
        }
        const existingReview = reviewsByRunId.get(current.run_id);
        if (existingReview !== undefined && approved.approval !== undefined) {
          reviewsByRunId.set(current.run_id, {
            ...existingReview,
            review: {
              ...existingReview.review,
              approval: { ...approved.approval, state: "recorded" },
            },
          });
        }
        statusEl.textContent = "Approval recorded. Highbay did not merge or deploy anything.";
        syncAllRegions();
        if (
          currentView.kind === "change" &&
          currentView.stage === "approve" &&
          selectedRunId === current.run_id
        ) {
          focusResult("#highbay-cp5-approval");
        }
      },
      (message, failure) => {
        approveErrors.set(current.run_id, message);
        if (failure.status === 501) {
          approvalUnavailable = message;
          runObservationGeneration.delete(current.run_id);
          const existingReview = reviewsByRunId.get(current.run_id);
          if (existingReview !== undefined) {
            reviewsByRunId.set(current.run_id, {
              ...existingReview,
              review: {
                ...existingReview.review,
                approval: { state: "blocked", reason: message },
              },
            });
          }
        }
        statusEl.textContent = "Approval was not recorded.";
        syncAllRegions();
        if (
          currentView.kind === "change" &&
          currentView.stage === "approve" &&
          selectedRunId === current.run_id
        ) {
          if (failure.status !== 501) readApprovalReview(current.run_id, current.change_id);
          focusResult("#highbay-cp5-approve-error");
        }
        if (failure.status === 409) refreshOpenCard(current.change_id);
      }
    );
  }

  function handleDiscardDraft() {
    const ed = getActiveEditor();
    if (!ed || editorRequestsByChangeId.has(ed.card.change_id)) {
      return;
    }
    const savedCard = ed.card;
    editorsByChangeId.set(savedCard.change_id, {
      card: savedCard,
      priorCard: ed.priorCard,
      incomingCard: ed.incomingCard,
      ...editorDraftFrom(savedCard),
      preservedDraft: null,
      formError: null,
    });
    syncAllRegions();
  }

  function handleLoadLatestCard() {
    const ed = getActiveEditor();
    if (!ed || editorRequestsByChangeId.has(ed.card.change_id)) {
      return;
    }
    const changeId = ed.card.change_id;
    listRequestErrors.delete(changeId);
    statusEl.textContent = "Loading the latest saved Change Card...";
    callEditorServer(
      "get_change",
      { change_id: changeId },
      changeId,
      (latestCard) => {
        if (latestCard.change_id !== changeId) {
          const message = "The server returned a different Change Card.";
          cardLoadErrors.set(changeId, message);
          showFormError(changeId, message);
          return;
        }
        recordObservedCard(latestCard);
        cardLoadErrors.delete(changeId);
        const currentEd = editorsByChangeId.get(changeId);
        if (currentEd && latestCard.request_revision === currentEd.card.request_revision) {
          editorsByChangeId.set(changeId, {
            ...currentEd,
            card: latestCard,
            formError: null,
          });
          if (activeChangeId === changeId) {
            statusEl.textContent = latestCard.approval
              ? "Showing the recorded approval for these inputs."
              : latestCard.unverified_approval
              ? "Showing the saved approval record (current promotion inputs could not be verified)."
              : "Showing the latest card without a current approval.";
            refreshVerificationObservations(changeId);
          }
          syncAllRegions();
          return;
        }
        const preserved = currentEd ? snapshotUnsavedDraft(currentEd) : null;
        const prior = nextPriorCard(currentEd ?? null, latestCard);
        mountChangeCard(latestCard, preserved, prior, "agree");
      },
      (message) => {
        const error = "Could not load the current card and approval: " + message;
        cardLoadErrors.set(changeId, error);
        showFormError(changeId, error);
      }
    );
  }

  function mountChangeCard(
    card: ChangeCardResult,
    preservedDraft: PreservedDraft | null,
    priorCard: ChangeCardResult | null = null,
    explicitStage?: ChangeStageName,
    options?: { forceActivate?: boolean }
  ) {
    recordObservedCard(card);
    const existing = editorsByChangeId.get(card.change_id);
    if (!existing || existing.card.request_revision !== card.request_revision) {
      verificationGeneration++;
    }
    cardLoadErrors.delete(card.change_id);
    const existingIncoming = existing?.incomingCard ?? null;
    const keepIncoming =
      existingIncoming !== null &&
      existingIncoming.change_id === card.change_id &&
      existingIncoming.request_revision !== card.request_revision
        ? existingIncoming
        : null;

    const nextEd: ActiveEditorState = {
      card,
      priorCard,
      incomingCard: keepIncoming,
      ...editorDraftFrom(card),
      preservedDraft,
      formError: null,
    };
    editorsByChangeId.set(card.change_id, nextEd);
    const shouldActivate =
      options?.forceActivate === true ||
      (activeChangeId === card.change_id &&
        currentView.kind === "change" &&
        currentView.changeId === card.change_id);
    if (!shouldActivate) {
      syncAllRegions();
      return;
    }
    activeChangeId = card.change_id;
    switchComposerScope(composerScopeFor("change", card.change_id));
    const stage: ChangeStageName =
      explicitStage ??
      (card.status === "confirmed" && card.proposed_revision === undefined ? "build" : "agree");
    currentView = { kind: "change", changeId: card.change_id, stage };
    statusEl.textContent =
      card.proposed_revision !== undefined
        ? "Review the proposed revision, then choose whether to adopt or decline it."
        : card.status === "confirmed"
        ? card.approval
          ? "Showing the recorded approval for these inputs."
          : card.unverified_approval
          ? "Showing the saved approval record (current promotion inputs could not be verified)."
          : stage === "build"
          ? "Review the build and its next step. The agreed details stay available below."
          : "Review the current agreement."
        : "Review the open questions and examples below, then confirm your intent.";
    readBuild(card.change_id);
    refreshVerificationObservations(card.change_id);
    syncHistoryState("replace");
    syncAllRegions();
  }

  // Move focus programmatically on skip-link activation so exact /views URL and
  // window.history.state are never mutated by native fragment navigation.
  if (skipEl !== null) {
    skipEl.addEventListener("click", (event) => {
      event.preventDefault();
      if (isNarrowViewport() && mobileSurface === "conversation") {
        (
          document.getElementById("highbay-conversation-title") ??
          document.getElementById("highbay-conversation-panel")
        )?.focus();
        return;
      }
      if (isNarrowViewport() && mobileSurface === "terminal") {
        (
          document.getElementById("highbay-terminal-title") ??
          document.getElementById("highbay-terminal-drawer")
        )?.focus();
        return;
      }
      focusCurrentViewHeading();
    });
  }

  // Re-sync regions cleanly when crossing the 1080px responsive breakpoint.
  if (typeof window !== "undefined" && typeof window.matchMedia === "function") {
    const narrowMql = window.matchMedia("(max-width: 1079px)");
    narrowMql.addEventListener("change", () => {
      if (!narrowMql.matches) {
        mobileSurface = "stage";
      }
      syncAllRegions();
    });
  }

  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      verificationGeneration++;
      if (waitController !== null) {
        waitController.abort();
      }
      syncAllRegions();
      return;
    }
    probeUiBuildAfterReconnect();
    if (activeChangeId !== null) {
      refreshVerificationObservations(activeChangeId);
    }
    startWaiting();
  });

  window.addEventListener("beforeunload", (event) => {
    if (hasUnsavedPageInput()) {
      event.preventDefault();
      event.returnValue = "";
    }
  });

  // Navigating to /views#link=<fresh> fires popstate before hashchange:
  // return immediately whenever a #link= fragment is present or session !== active,
  // and never call pushState inside popstate.
  window.addEventListener("popstate", (event) => {
    if (readLinkFromFragment(window.location.hash) !== null) {
      return;
    }
    if (sessionState !== "active") {
      if (window.location.hash === "") {
        window.history.replaceState(null, "", window.location.pathname + window.location.search);
      }
      return;
    }
    if (isHistoryViewState(event.state)) {
      applyHistoryViewState(event.state);
    }
  });

  // Defer #link= when unsaved page input exists, preserving currentHistoryState(),
  // active focus, and text selection.
  window.addEventListener("hashchange", () => {
    const fragment = window.location.hash;
    if (readLinkFromFragment(fragment) === null) return;
    if (!hasUnsavedPageInput()) {
      window.location.reload();
      return;
    }
    window.history.replaceState(
      currentHistoryState(),
      "",
      window.location.pathname + window.location.search
    );
    pendingLinkFragment = fragment;
    statusEl.textContent =
      "You opened a new Highbay link. Save or discard your edits, or wait for the save to finish, then choose Open link.";
    syncNoticeRegion();
    document.getElementById("highbay-pending-link")?.scrollIntoView({ block: "nearest" });
  });

  syncAllRegions();
  void start();
}

bootstrapViewsPage();
