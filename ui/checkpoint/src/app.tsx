// Package main implements the Surla views page. The page exchanges the
// one-use link `surla` opened it with for a session held only in this
// script's memory, then reads and acts on the project's changes by calling
// the Surla server at the address it was loaded from. It coordinates the
// stage navigation bar (#surla-checkpoint-nav), the displayed-subject context
// strip (#surla-workspace-context), the two-region layout (#surla-cp1-editor
// and #surla-result-view), the change list, and synchronous React 19
// rendering.

// zod-jitless stays the first import: it configures zod before any imported
// module builds a schema.
import "./zod-jitless";
import React from "react";
import { flushSync } from "react-dom";
import { createRoot, Root } from "react-dom/client";
import { ArchitecturePreviewView, IncomingChangeCardView } from "./components/c4-graph";
import { ChangeListView } from "./components/change-list";
import { CopyPrompt } from "./components/copy-prompt";
import { CP1EditorView } from "./components/cp1-editor";
import { BuildMergeInstructions, BuildSectionView, BuildStartReceipt, runBelongsToBuild } from "./components/build";
import { AgentPickerView } from "./components/agent-picker";
import { choicesForRoles, proposeWorkerChoices, workerChoicesReady } from "./agent-choices";
import type { WorkerChoices, WorkerRole } from "./agent-choices";
import { MergeFeedback, MergePrimaryAction, MergedBranchVerification } from "./components/merge-action";
import type { MergeControls } from "./components/merge-action";
import { VerificationView } from "./components/cp4-verification";
import { ProposedRevisionView } from "./components/proposal";
import { ApprovalStage } from "./components/approval-stage";
import { WorkspaceFrame } from "./components/workspace-frame";
import { SessionEndedNotice, SignedOutReason, SignedOutView } from "./components/session";
import { StoryStageView } from "./components/story";
import { GlobalTeamAttention, roleHasPane, TeamFactsError, TeamStageProps, TeamStageView, teamAttentionText } from "./components/team";
import { LiveTargetStageView, ModelDraft, ServerAnswer, describeApplied } from "./components/live-target";
import { Alert, Button, Card } from "./components/shadcn";
import { TraceDrawer, TraceFilter } from "./components/trace-drawer";
import { answersNeedingRevision, isDraftDirty, PreservedDraft } from "./context/cp1-draft";
import {
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
  ProposedRevisionItem,
  Operations,
  ROUTES,
  ServerFailure,
  StartedBuild,
  StoryRefreshResult,
  TraceList,
  TeamRoleState,
  TeamSnapshot,
  TestFindings,
  VerificationListResult,
  VerificationReview,
  VerificationStatusResult,
  isPlainObject,
} from "./types";

// WAIT_RETRY_MS is how long the page waits before asking again after the
// first failed round of its waiting read: the server did not answer, or
// answered that the list changed but then refused to send it. Each further
// failed round doubles the wait, up to WAIT_RETRY_MAX_MS.
const WAIT_RETRY_MS = 5000;
// WAIT_RETRY_MAX_MS bounds the wait between failed rounds.
const WAIT_RETRY_MAX_MS = 30000;

// Failure describes one request the server refused or never answered.
interface Failure {
  // kind separates a session the server no longer accepts, a refusal or
  // malformed answer, an unreachable server, and a request the page stopped.
  kind: "session" | "server" | "network" | "aborted";
  // status is the HTTP status, or 0 when no answer arrived.
  status: number;
  // message is the server's own error message when it sent one.
  message: string;
  // body retains only a validated response, including typed refusal facts.
  body?: ServerFailure;
}

// Outcome is the page's view of one finished request.
type Outcome<T> = { ok: true; result: T } | ({ ok: false } & Failure);

// PendingCall records one outstanding request so views derive their waiting
// state from the requests actually in flight.
interface PendingCall {
  // op is the operation sent.
  op: OperationName;
  // subject is the change_id or run_id the request is about, or "".
  subject: string;
}

// ActiveEditorState keeps received saved values separate from editable drafts.
interface ActiveEditorState {
  // card is the last saved snapshot received from the server.
  card: ChangeCardResult;
  // priorCard is the immediately prior saved snapshot for this change_id
  // when the editor transitioned between distinct revisions of the same card.
  priorCard: ChangeCardResult | null;
  // incomingCard retains a newer revision that arrived while the editor had
  // unsaved state or an outstanding request, until the user switches to it
  // or a card is mounted directly.
  incomingCard: ChangeCardResult | null;
  // draftAnswers contains the user's current choices, including unsaved ones.
  draftAnswers: Record<string, string>;
  // draftExamples contains the exact text the user is editing.
  draftExamples: ExampleItem[];
  // preservedDraft retains unsaved draft edits from an earlier revision.
  preservedDraft: PreservedDraft | null;
  // formError describes the latest failed editor request or invalid input.
  formError: { message: string; source: "input" | "request" } | null;
  // proposalAnswers holds the user's choices for the waiting proposal's
  // questions; a re-read keeps those that still fit the proposal.
  proposalAnswers: Record<string, string>;
  // proposalError describes the last refused adopt or decline, or null.
  proposalError: string | null;
}

// ActiveResultState selects the view displayed in #surla-result-view.
type ActiveResultState =
  | { kind: "changes" }
  | { kind: "story" }
  | { kind: "live" }
  | { kind: "team" }
  | { kind: "cp2-architecture-preview" }
  | { kind: "cp4-verification" }
  | { kind: "cp5-approval" }
  | { kind: "cp1-incoming-card" };

// SessionState is the page's session lifecycle: starting while the link is
// exchanged, active with a session, signed out without one, or ended when
// the server stopped accepting the session.
type SessionState = "starting" | "active" | "signed-out" | "ended";

// formatChangedDraftLines formats only the ambiguity answers and acceptance
// examples that differ from the saved Change Card, including cleared values,
// so unchanged saved content is not relabeled as unsaved draft edits.
function formatChangedDraftLines(ed: ActiveEditorState): string[] {
  const rev = ed.card.request_revision;
  const ambiguities = Array.isArray(ed.card.ambiguities) ? ed.card.ambiguities : [];
  const savedAnswers = isPlainObject(ed.card.answers) ? ed.card.answers : {};
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
    const savedVal = Object.prototype.hasOwnProperty.call(savedAnswers, k)
      ? savedAnswers[k]
      : "";
    const draftVal = Object.prototype.hasOwnProperty.call(draftAnswers, k)
      ? draftAnswers[k]
      : "";
    if (draftVal !== savedVal) {
      const q = questionByID.get(k);
      lines.push("[" + rev + "] " + k + (q ? " (" + q + ")" : "") + "=" + draftVal);
    }
  }

  const savedExamples = Array.isArray(ed.card.examples) ? ed.card.examples : [];
  const draftExamples = Array.isArray(ed.draftExamples) ? ed.draftExamples : [];
  for (let i = 0; i < draftExamples.length; i++) {
    const ex = draftExamples[i];
    const savedEx = savedExamples.find((s) => s.id === ex.id);
    if (
      !savedEx ||
      savedEx.scenario !== ex.scenario ||
      savedEx.input_summary !== ex.input_summary ||
      savedEx.expected_outcome !== ex.expected_outcome ||
      savedEx.verification_kind !== ex.verification_kind
    ) {
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
  }
  return lines;
}

// snapshotUnsavedDraft preserves unsaved local edits and any earlier unsaved
// draft lines before a different saved revision replaces the editable form.
function snapshotUnsavedDraft(ed: ActiveEditorState): PreservedDraft | null {
  const priorLines = ed.preservedDraft ? ed.preservedDraft.lines : [];
  const currentLines = formatChangedDraftLines(ed);
  if (currentLines.length === 0 && priorLines.length === 0) {
    return null;
  }
  return {
    fromRevision:
      currentLines.length > 0
        ? ed.card.request_revision
        : ed.preservedDraft!.fromRevision,
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
  const before = new Map((previous.ambiguities ?? []).map((question) => [question.id, question.question]));
  const kept: Record<string, string> = {};
  for (const question of next.ambiguities ?? []) {
    const chosen = answers[question.id];
    if (chosen !== undefined && before.get(question.id) === question.question && question.options.includes(chosen)) {
      kept[question.id] = chosen;
    }
  }
  return kept;
}

// readLinkFromFragment returns the one-use link carried in the address
// fragment, which a browser never sends to a server.
function readLinkFromFragment(hash: string): string | null {
  const params = new URLSearchParams(hash.startsWith("#") ? hash.slice(1) : hash);
  const link = params.get("link");
  return link !== null && link !== "" ? link : null;
}

// validateVerificationListPayload checks that a VerificationListResult carries
// a non-empty change_id, matches expectedChangeId, and only contains runs
// belonging to that same change_id.
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

// WorkspaceSubjectDescriptor describes the exact displayed subject of an editor
// or result region without retaining or caching a duplicate copy of session state.
interface WorkspaceSubjectDescriptor {
  // role identifies whether this descriptor describes the Change Card editor,
  // an incoming Change Card banner, or the active result view.
  role: "editor" | "incoming" | "result";
  // label is the human-readable prefix shown in the workspace context strip.
  label: string;
  // stage names the workspace stage being shown.
  stage: string;
  // changeId is set only when the displayed payload itself is bound to a Change Card.
  changeId?: string;
  // requestRevision is set only when the displayed payload itself carries a request_revision.
  requestRevision?: string;
  // detail summarizes secondary state (such as status, design digest, or run ID).
  detail?: string;
}

// liveTargetKey names a live target read's case, commit, start and end, so the
// page can tell when the state a message described has changed.
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

// describeActiveResultSubject derives the subject of #surla-result-view strictly
// from the displayed payload and selected verification run, never falling
// back to the editor's card. The Story stage shows the Story saved with
// storyCard, the change open in the editor, and the owner's current
// evaluation.
function describeActiveResultSubject(
  activeResult: ActiveResultState | null,
  previewState: ArchitecturePreviewResult | null,
  displayedRun: VerificationStatusResult | null,
  selectedVerificationChangeId: string | null,
  bothRegionsShown: boolean,
  storyCard: ChangeCardResult | null,
  storyEvaluation: StoryRefreshResult | null
): WorkspaceSubjectDescriptor | null {
  if (
    !activeResult ||
    activeResult.kind === "cp1-incoming-card" ||
    activeResult.kind === "live" ||
    activeResult.kind === "team"
  ) {
    return null;
  }
  const label = bothRegionsShown ? "Displayed view" : "Showing";
  if (activeResult.kind === "changes") {
    return bothRegionsShown ? { role: "result", label, stage: "Project changes" } : null;
  }
  if (activeResult.kind === "story") {
    let draft = "current draft not read yet";
    if (storyEvaluation !== null) {
      draft = "current draft " + storyEvaluation.state;
      if (storyEvaluation.snapshot_digest) {
        draft += " · snapshot " + storyEvaluation.snapshot_digest.slice(0, 15);
      }
    }
    return {
      role: "result",
      label,
      stage: "Story",
      changeId: storyCard?.change_id,
      requestRevision: storyCard?.request_revision,
      detail: draft,
    };
  }
  if (activeResult.kind === "cp2-architecture-preview") {
    if (!previewState) {
      return null;
    }
    return {
      role: "result",
      label,
      stage: "Architecture · " + previewState.system_name,
      changeId: previewState.change_id,
      requestRevision: previewState.request_revision,
      detail: "design " + previewState.design_digest.slice(0, 15),
    };
  }
  if (displayedRun) {
    return {
      role: "result",
      label,
      stage: activeResult.kind === "cp5-approval" ? "Approve" : "Check",
      changeId: displayedRun.change_id,
      requestRevision: displayedRun.request_revision,
      detail:
        "run " +
        displayedRun.run_id.slice(0, 8) +
        " · commit " +
        displayedRun.candidate_commit.slice(0, 7),
    };
  }
  return {
    role: "result",
    label,
    stage: activeResult.kind === "cp5-approval" ? "Approve" : "Check",
    changeId: selectedVerificationChangeId || undefined,
    detail: "saved verification runs",
  };
}

// bootstrapViewsPage mounts the views page, obtains its session, and starts
// reading the project's changes.
export function bootstrapViewsPage() {
  const rawRootEl = document.getElementById("surla-checkpoint-root");
  const rawStatusEl = document.getElementById("surla-checkpoint-status");
  const rawViewEl = document.getElementById("surla-checkpoint-view");
  const rawNavEl = document.getElementById("surla-checkpoint-nav");
  if (!rawRootEl || !rawStatusEl || !rawViewEl || !rawNavEl) {
    return;
  }
  const rootEl: HTMLElement = rawRootEl;
  const statusEl: HTMLElement = rawStatusEl;
  const viewEl: HTMLElement = rawViewEl;
  const navEl: HTMLElement = rawNavEl;

  const noticeEl = document.createElement("div");
  noticeEl.setAttribute("id", "surla-session-notice");
  statusEl.after(noticeEl);
  const editorRegionEl = document.createElement("div");
  editorRegionEl.setAttribute("id", "surla-cp1-editor");
  editorRegionEl.setAttribute("tabindex", "-1");
  editorRegionEl.setAttribute("aria-label", "Change decisions");
  const resultRegionEl = document.createElement("div");
  resultRegionEl.setAttribute("id", "surla-result-view");
  viewEl.appendChild(editorRegionEl);
  viewEl.appendChild(resultRegionEl);
  const traceRegionEl = document.createElement("aside");
  traceRegionEl.setAttribute("aria-label", "Operation diagnostics");
  viewEl.before(traceRegionEl);

  const navRoot: Root = createRoot(navEl);
  const noticeRoot: Root = createRoot(noticeEl);
  const editorRoot: Root = createRoot(editorRegionEl);
  const resultRoot: Root = createRoot(resultRegionEl);
  const traceRoot: Root = createRoot(traceRegionEl);

  // session is the credential the server issued for this page. It lives only
  // in this closure: never in browser storage or a cookie, so a reload or a
  // closed tab ends it.
  let session: string | null = null;
  // projectName is the physical folder's base name from the authenticated
  // exchange, fixed for this page session and never derived from a team ID.
  let projectName: string | null = null;
  let sessionState: SessionState = "starting";
  let signedOutReason: SignedOutReason = "no-link";
  let signedOutMessage = "";
  const pendingCalls = new Set<PendingCall>();

  let changeList: ChangeList | null = null;
  let listError: string | null = null;
  let listRequest: Promise<boolean> | null = null;
  let waitController: AbortController | null = null;
  let waitLoopRunning = false;
  // waitTroubleShown is true while the status line reports that the waiting
  // read is retrying after a failed round.
  let waitTroubleShown = false;
  // cardRefresh is the outstanding background read of the open card;
  // cardRefreshAgain asks for one more read after it because the list
  // changed again meanwhile.
  let cardRefresh: Promise<void> | null = null;
  let cardRefreshAgain = false;
  // cardRefreshAfterEditor names the open card to read again once the
  // page's own Change Card request finishes. Reading it during that request
  // could return the page's own save and announce it as another caller's.
  let cardRefreshAfterEditor: string | null = null;

  let traceOpen = false;
  let traceFilter: TraceFilter = "error";
  let tracePage: TraceList | null = null;
  let traceError: string | null = null;

  // editorRequest owns the editor until the card request finishes. Reads
  // lock editing too, but only writes can leave a save pending on unload.
  let editorRequest: OperationName | null = null;
  let activeEditor: ActiveEditorState | null = null;
  // editorOpen tracks whether #surla-cp1-editor is mounted. Another stage
  // keeps activeEditor in memory while the editor is not shown.
  let editorOpen = false;
  // editorStage tracks navigation intent while Agree and Build share the
  // existing editor content during this first stage's rollout.
  let editorStage: "agree" | "build" = "agree";
  let previewState: ArchitecturePreviewResult | null = null;
  const previewErrors = new Map<string, string>();
  // storyEvaluation is the owner's current Story evaluation as last read, or
  // null before the first read; storyEvaluationError is its last failed read.
  let storyEvaluation: StoryRefreshResult | null = null;
  let storyEvaluationError: string | null = null;
  // storyEvaluationStale is true when the change list's version moved since
  // the evaluation was last read while the Story stage was not shown, so
  // opening the stage reads it again.
  let storyEvaluationStale = true;
  // storyReadAgain asks for one more read after the outstanding one, because
  // the version moved while it was in flight.
  let storyReadAgain = false;
  // liveTarget is the live target as last read, or null before the first
  // read; liveTargetError is its last failed read. liveTargetStale and
  // liveReadAgain follow the change list's version as the Story's do.
  let liveTarget: LiveTargetStatus | null = null;
  let liveTargetError: string | null = null;
  let liveTargetStale = true;
  let liveReadAgain = false;
  // liveStopOutcome is the server's answer to the last stop request. It is
  // shown until the live target's state next changes: liveStopBasis is the
  // state the first read issued after the answer found, and only reads
  // numbered above liveStopAfterRead, liveReadCount when the answer came,
  // count, so a read already in flight cannot clear it.
  let liveStopOutcome: ServerAnswer | null = null;
  let liveStopBasis: string | null = null;
  let liveStopAfterRead = 0;
  let liveReadCount = 0;
  // modelConnection is the model connection as last read, or null before
  // the first read; it is not part of the change list's version, so the
  // page reads it when the Live target view opens and after it changes it.
  let modelConnection: ModelConnection | null = null;
  let modelConnectionError: string | null = null;
  // modelApplyOutcome is the server's answer to the last change of model.
  let modelApplyOutcome: ServerAnswer | null = null;
  // modelDraft is the model form's unsent input, kept here so it survives
  // refreshes and leaving the Live target view.
  let modelDraft: ModelDraft = { kind: null, provider: "anthropic", modelName: "", gatewayAddr: "" };
  // modelDraftDirty records edits since the last successful Apply or local discard.
  let modelDraftDirty = false;
  // modelDraftReset remounts only the uncontrolled model form on local discard.
  let modelDraftReset = 0;
  // pendingLinkFragment stays in memory until all unsaved input is resolved.
  let pendingLinkFragment: string | null = null;
  // teamSnapshot is shared by the Team stage and notices on every stage.
  // Failed reads retain facts, visibly labeled as last read.
  let teamSnapshot: TeamSnapshot | null = null;
  // teamReadError keeps the last read error and whether its message needs
  // the failed-read label; session and update notices stand alone.
  let teamReadError: TeamFactsError | null = null;
  let teamReadAgain = false;
  let teamStartReceipt: TeamStageProps["startReceipt"] = null;
  let teamActionError: string | null = null;
  // Installed options are read on demand. Local choices survive team polling,
  // navigation and failed discovery; only an explicit launch persists them.
  let agentOptions: Operations["list_team_agent_options"]["result"] | null = null;
  let agentOptionsError: string | null = null;
  let workerChoices: WorkerChoices = {};
  const editedWorkerChoices = new Set<WorkerRole>();
  let backgroundConsent = false;
  // Successful dismissal receipts prevent an earlier team read restoring a
  // notice. Reload still reads the preference from its project owner.
  type TeamNotice = TeamSnapshot["dismissed_notices"][number];
  const noticeDismissed = new Set<TeamNotice>();
  const noticeErrors = new Map<TeamNotice, string>();
  // Merge outcomes belong to one exact approval and build, never a latest run.
  let mergeOutcome: {
    identity: string;
    receipt: MergeControls["receipt"];
    error: MergeControls["error"];
  } | null = null;
  // buildChangeId identifies the single retained build read. The generation
  // rejects replies begun before a start, its successful answer, or a change
  // of selected card.
  let buildChangeId: string | null = null;
  let buildSnapshot: ChangeBuild["build"] | null = null;
  // The owner reports this narrow admission fact with each guarded build read.
  let buildDraftedBeforeDrop: boolean | null = null;
  let buildReadError: string | null = null;
  let buildStartError: string | null = null;
  let buildReadAgain = false;
  let buildGeneration = 0;
  let buildStartReceipt: BuildStartReceipt | null = null;
  // A validated refusal may describe another change, whose build cannot be
  // obtained from the selected card's get_build result.
  let openBuildRefusal: OpenBuildRefusal | null = null;
  let buildConfirmationRead: ((build: ChangeBuild | null) => void) | null = null;
  // Findings belong to one build/report/verdict, rather than only a change ID.
  let buildFindings: TestFindings | null = null;
  let findingsBasis: string | null = null;
  let findingsError: string | null = null;
  let findingsReadAgain = false;
  const runsById = new Map<string, VerificationStatusResult>();
  // Verification observations follow the page's owner updates independently
  // of immutable terminal reports. Request order also covers overlapping
  // list/get reads of the same run within one update generation.
  let verificationGeneration = 0;
  let verificationReadOrder = 0;
  const runObservationGeneration = new Map<string, number>();
  const runObservationOrder = new Map<string, number>();
  const runReadAgain = new Set<string>();
  const lastLoadedListByChangeId = new Map<string, string[]>();
  let selectedVerificationChangeId: string | null = null;
  let selectedRunId: string | null = null;
  // Only the selected run retains a review; history keeps its compact statuses.
  let selectedReview: {
    runId: string; generation: number; order: number; review: VerificationReview;
  } | null = null;
  const selectionTouchedDuringList = new Map<string, boolean>();
  const runRequestErrors = new Map<string, string>();
  const listRequestErrors = new Map<string, string>();
  const cardLoadErrors = new Map<string, string>();
  const approveErrors = new Map<string, string>();
  // approvalUnavailable is the server's reason it cannot record approvals,
  // learned from its answer to an approval request. The change list says
  // the same before any request; this answer covers a page that has not
  // read the list, or a server whose answer changed since.
  let approvalUnavailable: string | null = null;
  // lastOpenAttempt is the change the user last asked to open, whose read
  // failure the change list shows.
  let lastOpenAttempt: string | null = null;
  let activeResult: ActiveResultState | null = null;

  // send encodes one request with the generated route, sends it to the
  // page's own address with the session, and validates the answer with the
  // generated result validator. It is the page's only network call.
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
    if (session !== null) {
      headers.Authorization = "Bearer " + session;
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
        message: "The Surla server did not answer. Check that surla is still running.",
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
    return {
      ok: false,
      kind: status === 401 ? "session" : "server",
      status,
      message: body !== undefined
        ? body.message
        : "The server refused the request (HTTP " + status + ").",
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

  // callServer sends one request whose waiting state views derive from
  // pendingCalls, then hands its outcome to exactly one callback and renders.
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

  // callEditorServer owns the editable form for one Change Card request;
  // only these requests disable the form.
  function callEditorServer<K extends OperationName>(
    op: K,
    request: Operations[K]["request"],
    subject: string,
    onSuccess: (result: Operations[K]["result"]) => void,
    onError: (message: string, failure: Failure) => void
  ) {
    editorRequest = op;
    // readDeferredCard runs the background read the request held back.
    const readDeferredCard = () => {
      const deferred = cardRefreshAfterEditor;
      cardRefreshAfterEditor = null;
      if (deferred !== null) {
        refreshOpenCard(deferred);
      }
    };
    callServer(
      op,
      request,
      subject,
      (result) => {
        editorRequest = null;
        onSuccess(result);
        readDeferredCard();
      },
      (message, failure) => {
        editorRequest = null;
        onError(message, failure);
        readDeferredCard();
      }
    );
  }

  // sessionEndedMessage tells the user what to do once the session is gone.
  function sessionEndedMessage(): string {
    return "This page's Surla session has ended. Ask your coding agent to open Surla views, or choose Open Surla Window in the Herdr companion.";
  }

  // start exchanges the link from the address fragment for a session, removes
  // the link from the address, and reads the project's changes.
  async function start() {
    const link = readLinkFromFragment(window.location.hash);
    if (window.location.hash !== "") {
      window.history.replaceState(null, "", window.location.pathname + window.location.search);
    }
    if (link === null) {
      signOut("no-link", "");
      return;
    }
    statusEl.textContent = "Opening your Surla session...";
    const outcome = await send("exchange_browser_link", { link });
    if (!outcome.ok) {
      signOut(outcome.status === 422 ? "link-used" : "link-failed", outcome.message);
      return;
    }
    session = outcome.result.session;
    projectName = outcome.result.project_name;
    sessionState = "active";
    rootEl.dataset.session = "active";
    activeResult = { kind: "changes" };
    rootEl.dataset.state = "changes";
    await loadChanges();
  }

  // signOut shows how to open the page with `surla`; the page then sends no
  // further requests.
  function signOut(reason: SignedOutReason, message: string) {
    sessionState = "signed-out";
    signedOutReason = reason;
    signedOutMessage = message;
    rootEl.dataset.session = "none";
    rootEl.dataset.state = reason === "link-used" ? "link-used" : "signed-out";
    statusEl.textContent =
      reason === "link-used"
        ? "This link was already used. Ask your coding agent to open Surla views, or choose Open Surla Window in the Herdr companion."
        : "Ask your coding agent to open Surla views, or choose Open Surla Window in the Herdr companion.";
    syncAllRegions();
  }

  // endSession forgets a session the server no longer accepts, stops the
  // waiting read, and keeps every view and draft on the page.
  function endSession() {
    if (sessionState !== "active") {
      return;
    }
    session = null;
    sessionState = "ended";
    buildStartReceipt = null;
    buildStartError = null;
    openBuildRefusal = null;
    buildConfirmationRead = null;
    teamStartReceipt = null;
    buildGeneration++;
    rootEl.dataset.session = "ended";
    if (waitController !== null) {
      waitController.abort();
    }
    statusEl.textContent = sessionEndedMessage();
    syncAllRegions();
  }

  // loadChanges reads the change list once at a time, then applies what
  // changed to the open change and its runs. It resolves to whether the list
  // was read; a failure is shown on the list and kept in listError.
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
        if (activeResult?.kind === "changes") {
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
        if (activeEditor !== null) {
          readBuild(activeEditor.card.change_id);
          refreshVerificationObservations(activeEditor.card.change_id);
        }
        // Story and Live can open before the first list arrives. Refresh
        // their observations after each new version, including the first.
        if (activeResult?.kind === "story") {
          readStoryEvaluation();
        } else {
          storyEvaluationStale = true;
        }
        // It also covers the live target: its case, commit, start time and
        // the versions in force.
        if (activeResult?.kind === "live") {
          readLiveTarget();
        } else {
          liveTargetStale = true;
        }
      }
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

  // applyListChanges opens the only change on the first read or when the
  // first proposal arrives on the empty Changes view. Later reads preserve
  // the current editor and reconcile its saved facts by change ID.
  function applyListChanges(previous: ChangeList | null, next: ChangeList) {
    if (activeEditor === null && next.changes.length === 1 &&
      (previous === null || (previous.changes.length === 0 && activeResult?.kind === "changes"))) {
      openChange(next.changes[0].change_id);
      return;
    }
    const initializing = next.initializing_phase !== undefined;
    const wasInitializing = previous?.initializing_phase !== undefined;
    const drafting = next.draft_state !== undefined;
    const wasDrafting = previous?.draft_state !== undefined;
    if (activeResult?.kind === "changes" &&
      (previous === null || initializing !== wasInitializing || drafting !== wasDrafting)) {
      statusEl.textContent = initializing ? "Preparing the starter project."
        : drafting ? "Drafting the project's first Change Card."
        : next.changes.length === 0 ? "This project has no changes yet."
        : "Showing this project's " + next.changes.length + " changes.";
    }
    if (previous === null || activeEditor === null) {
      return;
    }
    const openId = activeEditor.card.change_id;
    const before = previous.changes.find((item) => item.change_id === openId);
    const after = next.changes.find((item) => item.change_id === openId);
    if (!after) {
      return;
    }
    if (
      !before ||
      before.request_revision !== after.request_revision ||
      before.saved_at !== after.saved_at
    ) {
      refreshOpenCard(openId);
    }
    const runBefore = before?.latest_verification;
    const runAfter = after.latest_verification;
    if (
      lastLoadedListByChangeId.has(openId) &&
      (runBefore?.run_id !== runAfter?.run_id || runBefore?.state !== runAfter?.state)
    ) {
      refreshVerificationsInBackground(openId);
    }
  }

  // startWaiting keeps one waiting read open while the page has a session
  // and a list version and the tab is visible.
  function startWaiting() {
    if (waitLoopRunning || session === null || changeList === null || document.hidden) {
      return;
    }
    waitLoopRunning = true;
    void waitLoop().finally(() => {
      waitLoopRunning = false;
    });
  }

  // waitLoop asks the server to answer when the list differs from the last
  // one read, and reads the list again only then. Hiding the tab stops the
  // open request; showing it again resumes from the same version. A round
  // fails when the waiting read gets no answer or the list read that follows
  // it fails; the loop then reports the failure once and waits longer after
  // each failed round before asking again, because a list that keeps
  // failing would otherwise be asked for again at once.
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
            statusEl.textContent = "This page is up to date with the Surla server again.";
            syncAllRegions();
          }
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
          "Lost contact with the Surla server; trying again. Your edits stay on this page. " +
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

  // refreshOpenCard reads the open card again because the list says it
  // changed, without taking the editor's request slot. While the page's own
  // Change Card request is in flight, the read waits for it to finish.
  function refreshOpenCard(changeId: string) {
    if (editorRequest !== null) {
      cardRefreshAfterEditor = changeId;
      return;
    }
    if (cardRefresh !== null) {
      cardRefreshAgain = true;
      return;
    }
    cardRefresh = (async () => {
      do {
        cardRefreshAgain = false;
        const outcome = await send("get_change", { change_id: changeId });
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
      } while (cardRefreshAgain && session !== null);
    })();
    void cardRefresh.finally(() => {
      cardRefresh = null;
      syncAllRegions();
    });
    syncAllRegions();
  }

  // receiveLatestCard applies a background read of the open card. The same
  // revision updates saved metadata such as approval and keeps drafts; a new
  // revision is held as an incoming card while the user has unsaved edits or
  // a request in flight, and replaces a clean editor otherwise. It never
  // moves focus and announces a new revision once.
  function receiveLatestCard(latest: ChangeCardResult) {
    if (!activeEditor || activeEditor.card.change_id !== latest.change_id) {
      return;
    }
    cardLoadErrors.delete(latest.change_id);
    const ed = activeEditor;
    if (latest.request_revision !== ed.card.request_revision || latest.status !== ed.card.status) {
      verificationGeneration++;
      refreshVerificationObservations(latest.change_id);
    }
    if (latest.request_revision === ed.card.request_revision) {
      const before = ed.card.proposed_revision;
      const after = latest.proposed_revision;
      activeEditor = {
        ...ed,
        card: latest,
        proposalAnswers: keepProposalAnswers(ed.proposalAnswers, before, after),
        proposalError: before?.request_revision === after?.request_revision ? ed.proposalError : null,
      };
      if ((ed.card.approval ?? ed.card.unverified_approval?.approval)?.report_digest !==
          (latest.approval ?? latest.unverified_approval?.approval)?.report_digest) {
        refreshVerificationObservations(latest.change_id);
      }
      if (after !== undefined && before?.request_revision !== after.request_revision) {
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
      activeEditor = { ...ed, incomingCard: latest };
      syncAllRegions();
      return;
    }
    if (editorRequest !== null || hasUnsavedEditorState()) {
      activeEditor = { ...ed, incomingCard: latest };
      if (editorOpen && (!activeResult || activeResult.kind === "cp1-incoming-card")) {
        activeResult = { kind: "cp1-incoming-card" };
      }
      statusEl.textContent =
        "A newer revision of this Change Card was saved. Your unsaved edits are kept; use Switch to Incoming Card to review it.";
      syncAllRegions();
      return;
    }
    activeEditor = {
      card: latest,
      priorCard: nextPriorCard(ed, latest),
      incomingCard: null,
      ...editorDraftFrom(latest),
      preservedDraft: null,
      formError: null,
    };
    if (activeResult?.kind === "cp1-incoming-card") {
      activeResult = null;
    }
    statusEl.textContent = "A newer revision of this Change Card was saved and is now shown.";
    syncAllRegions();
  }

  // refreshVerificationsInBackground reads the open change's runs again
  // because the list says its latest run changed, keeping the user's view
  // and selected run.
  function refreshVerificationsInBackground(changeId: string) {
    if (hasPending("list_verifications", changeId)) {
      return;
    }
    const generation = verificationGeneration;
    const order = ++verificationReadOrder;
    for (const run of runsById.values()) {
      if (run.change_id === changeId) markRunReadPending(run.run_id, order);
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
        if (selectedVerificationChangeId === changeId && selectedRunId === null && loadedIds.length > 0) {
          const card = activeEditor?.card;
          const approval = card?.approval ?? card?.unverified_approval?.approval;
          selectedRunId = activeResult?.kind === "cp5-approval" && card?.change_id === changeId && approval !== undefined
            ? savedApprovalEvidenceRun(card, approval)?.run_id ?? null : loadedIds[0];
        }
        if (selectedVerificationChangeId === changeId && selectedRunId !== null && isViewingSelectedRun(selectedRunId)) {
          readApprovalReview(selectedRunId, changeId);
        }
      },
      (message) => {
        listRequestErrors.set(changeId, message);
      }
    );
  }

  // resolveDisplayedVerificationRun returns the selected VerificationStatusResult
  // snapshot when one is currently selected in runsById.
  function resolveDisplayedVerificationRun(): VerificationStatusResult | null {
    return selectedRunId !== null ? runsById.get(selectedRunId) ?? null : null;
  }

  // runInputsKnown means the displayed owner observation survived the latest
  // update and all reads on which its action depends. Saved evidence remains
  // visible while any of these facts are unknown.
  function runInputsKnown(run: VerificationStatusResult): boolean {
    const listed = changeList?.changes.find((item) => item.change_id === run.change_id);
    const ed = activeEditor?.card.change_id === run.change_id ? activeEditor : null;
    // A confirmation can reach this page before its change-list notification.
    // A retained dirty editor may instead have the fresh card waiting beside it.
    const cardMatchesList = ed === null || [ed.card, ed.incomingCard].some((card) =>
      card !== null && card.request_revision === listed?.request_revision && card.status === listed.status);
    return sessionState === "active" && !document.hidden && changeList !== null && listError === null &&
      !hasPending("list_changes", "") && cardMatchesList &&
      !waitTroubleShown && editorRequest === null && cardRefresh === null &&
      runObservationGeneration.get(run.run_id) === verificationGeneration &&
      !hasPending("get_verification", run.run_id) &&
      !hasPending("review_verification", run.run_id) &&
      !hasPending("list_verifications", run.change_id) &&
      !hasPending("get_build", run.change_id) &&
      !hasPending("start_build", run.change_id) &&
      !cardLoadErrors.has(run.change_id) &&
      (buildChangeId !== run.change_id || buildReadError === null);
  }

  // refreshVerificationObservations reads exact owner-supplied identities.
  // An older selected run is independent of the latest run's match.
  function refreshVerificationObservations(changeId: string) {
    const latest = changeList?.changes.find((item) => item.change_id === changeId)?.latest_verification;
    if (latest !== undefined) readVerification(latest.run_id, changeId);
    if (selectedVerificationChangeId === changeId && selectedRunId !== null &&
        selectedRunId !== latest?.run_id) readVerification(selectedRunId, changeId);
    const card = activeEditor?.card;
    const approval = card?.approval ?? card?.unverified_approval?.approval;
    if (card?.change_id === changeId && approval !== undefined && knownTakingBuild(changeId) !== null &&
        !listRequestErrors.has(changeId) && approvalRunFor(card, approval) === null) {
      refreshVerificationsInBackground(changeId);
    }
  }

  // knownTakingBuild excludes retained facts while the owner observation is
  // stale, failed or being replaced. Saved results remain readable throughout.
  function knownTakingBuild(changeId: string): StartedBuild | null {
    return sessionState === "active" && !waitTroubleShown && buildChangeId === changeId &&
      buildSnapshot?.state === "started" && buildReadError === null &&
      !hasPending("get_build", changeId) && !hasPending("start_build", changeId) &&
      !Array.from(pendingCalls).some((call) => call.op === "drop_build")
      ? buildSnapshot : null;
  }

  // approvalRunFor correlates every recorded input to an eligible owner run;
  // neither recency nor equal commits alone establishes this build's identity.
  function approvalRunFor(card: ChangeCardResult, approval: ApprovalItem): VerificationStatusResult | null {
    const build = knownTakingBuild(card.change_id);
    if (build === null) return null;
    for (const run of runsById.values()) {
      if (runInputsKnown(run) && runBelongsToBuild(card.change_id, build, run) &&
          run.request_revision === card.request_revision && run.candidate_commit === approval.candidate_commit &&
          run.suite_commit === approval.suite_commit && run.test_source.source === approval.test_source.source &&
          run.state.type === "completed" && run.state.value.report.report_digest === approval.report_digest) return run;
    }
    return null;
  }

  // savedApprovalEvidenceRun selects only an unambiguous recorded binding.
  // This locates evidence; the owner's new review still decides admission.
  function savedApprovalEvidenceRun(card: ChangeCardResult, approval: ApprovalItem): VerificationStatusResult | null {
    const matches = Array.from(runsById.values()).filter((run) => run.change_id === card.change_id &&
      run.request_revision === card.request_revision && run.candidate_commit === approval.candidate_commit &&
      run.suite_commit === approval.suite_commit && run.test_source.source === approval.test_source.source &&
      run.state.type === "completed" && run.state.value.report.report_digest === approval.report_digest);
    return matches.length === 1 ? matches[0] : approvalRunFor(card, approval);
  }

  // latestVerificationFor uses the run identity supplied by the change owner.
  function latestVerificationFor(changeId: string): VerificationStatusResult | null {
    const id = changeList?.changes.find((item) => item.change_id === changeId)?.latest_verification?.run_id;
    return id === undefined ? null : runsById.get(id) ?? null;
  }

  // newerCheckFor offers only the latest identity agreed by both owner reads.
  // Their ordered history must place the displayed run after it; a delayed
  // list cannot describe a more recently selected run as older.
  function newerCheckFor(run: VerificationStatusResult): VerificationStatusResult | null {
    const latest = latestVerificationFor(run.change_id);
    const history = lastLoadedListByChangeId.get(run.change_id);
    return latest !== null && history?.[0] === latest.run_id && history.indexOf(run.run_id) > 0
      ? latest : null;
  }

  // reviewSavedCheck opens the exact result whose action the user selected.
  function reviewSavedCheck(run: VerificationStatusResult) {
    selectedVerificationChangeId = run.change_id;
    selectedRunId = run.run_id;
    editorOpen = false;
    activeResult = { kind: "cp5-approval" };
    rootEl.dataset.state = "cp5-approval";
    statusEl.textContent = "Review the saved check before approving it.";
    if (!lastLoadedListByChangeId.has(run.change_id)) refreshVerificationsInBackground(run.change_id);
    readApprovalReview(run.run_id, run.change_id);
    syncAllRegions();
    focusResult("#surla-cp5-title");
  }

  // openApproval preserves an explicitly selected run; otherwise it opens the
  // change owner's current result. An empty stage names the required selection.
  function openApproval() {
    const selected = activeResult?.kind === "cp4-verification" || activeResult?.kind === "cp5-approval"
      ? resolveDisplayedVerificationRun() : null;
    const run = selected ?? (activeEditor !== null ? latestVerificationFor(activeEditor.card.change_id) : null);
    if (run !== null) {
      reviewSavedCheck(run);
      return;
    }
    selectedRunId = null;
    selectedVerificationChangeId = activeEditor?.card.change_id ?? null;
    editorOpen = false;
    activeResult = { kind: "cp5-approval" };
    rootEl.dataset.state = "cp5-approval";
    if (selectedVerificationChangeId !== null) refreshVerificationsInBackground(selectedVerificationChangeId);
    syncAllRegions();
    focusResult("#surla-cp5-title");
  }

  // openRecordedApproval retains the exact saved binding rather than choosing
  // another run's approval by recency. The record stays visible without a run.
  function openRecordedApproval() {
    const card = activeEditor?.card;
    if (card === undefined) return;
    const approval = card.approval ?? card.unverified_approval?.approval;
    const run = approval !== undefined ? savedApprovalEvidenceRun(card, approval) : null;
    if (run !== null) { reviewSavedCheck(run); return; }
    selectedVerificationChangeId = card.change_id;
    selectedRunId = null;
    editorOpen = false;
    activeResult = { kind: "cp5-approval" };
    rootEl.dataset.state = "cp5-approval";
    if (!lastLoadedListByChangeId.has(card.change_id) && !listRequestErrors.has(card.change_id)) {
      refreshVerificationsInBackground(card.change_id);
    }
    syncAllRegions();
    focusResult("#surla-cp5-title");
  }

  // displayedApprovalReview never borrows another selected run's observation.
  function displayedApprovalReview(): VerificationReview | null {
    return selectedReview !== null && selectedReview.runId === selectedRunId ? selectedReview.review : null;
  }

  // isViewingSelectedRun binds evidence reads to the explicit Check/Approve selection.
  function isViewingSelectedRun(runId: string): boolean {
    return selectedRunId === runId &&
      (activeResult?.kind === "cp4-verification" || activeResult?.kind === "cp5-approval" ||
        editorOpen && editorStage === "build" && activeEditor?.card.change_id === runsById.get(runId)?.change_id);
  }

  // approvalReviewFresh validates the observation's identity and transport
  // freshness, not its admission predicates; only the server decides those.
  function approvalReviewFresh(run: VerificationStatusResult): boolean {
    return runInputsKnown(run) && !hasPending("review_verification", run.run_id) &&
      selectedReview !== null && selectedReview.runId === run.run_id &&
      selectedReview.generation === verificationGeneration &&
      selectedReview.order >= (runObservationOrder.get(run.run_id) ?? -1);
  }

  // mergeIdentity correlates receipts with the approved inputs and saved build.
  // It deliberately does not bind the user to the newest passed run.
  function mergeIdentity(run: VerificationStatusResult): string | null {
    const build = buildChangeId === run.change_id && buildSnapshot?.state === "started" ? buildSnapshot : null;
    return build !== null && run.state.type === "completed"
      ? JSON.stringify([run.change_id, build.started_at, run.request_revision,
        run.candidate_commit, run.state.value.report.report_digest]) : null;
  }

  // buildApprovalRun locates the card's exact saved approval, keeping a user
  // selection when it carries that binding. Branch tips and recency are not IDs.
  function buildApprovalRun(card: ChangeCardResult): VerificationStatusResult | null {
    const approval = card.approval;
    if (approval === undefined) return null;
    const selected = resolveDisplayedVerificationRun();
    if (selected !== null && selected.change_id === card.change_id &&
      selected.request_revision === card.request_revision && selected.candidate_commit === approval.candidate_commit &&
      selected.state.type === "completed" && selected.state.value.report.report_digest === approval.report_digest) return selected;
    return savedApprovalEvidenceRun(card, approval);
  }

  // refreshMergeState follows the exact approval through existing read owners.
  // It may resolve an unknown outcome but never repeats the mutation.
  function refreshMergeState(run: VerificationStatusResult) {
    if (sessionState !== "active") return;
    selectedRunId = run.run_id;
    selectedVerificationChangeId = run.change_id;
    readBuild(run.change_id);
    readApprovalReview(run.run_id, run.change_id);
    refreshOpenCard(run.change_id);
    readTeam();
  }

  // mergeControls admits only a current owner-recorded approval for this build.
  // Retained evidence remains visible while reads or the update chain are stale.
  function mergeControls(run: VerificationStatusResult | null): MergeControls {
    const build = run !== null ? knownTakingBuild(run.change_id) : null;
    const review = run !== null && selectedReview?.runId === run.run_id ? selectedReview.review : null;
    const anyMergePending = Array.from(pendingCalls).some((call) => call.op === "merge_change");
    const pending = run !== null && hasPending("merge_change", run.change_id);
    const identity = run !== null ? mergeIdentity(run) : null;
    const outcome = identity !== null && mergeOutcome?.identity === identity ? mergeOutcome : null;
    let reason: string | null = null;
    if (run === null) reason = "Read the exact saved approval before merging.";
    else if (sessionState !== "active") reason = "Open a fresh Surla page through your coding agent to check current approval.";
    else if (anyMergePending && !pending) reason = "Another change is being merged. Wait for that operation to finish.";
    else if (build === null) reason = "Read the current build before merging.";
    else if (build.taking.state === "merged") reason = "This build is already merged. Describe the next change when you are ready.";
    else if (build.taking.state === "dropped") reason = "This build was dropped; it cannot be merged through Surla.";
    else if (!build.in_force || !runBelongsToBuild(run.change_id, build, run)) reason = "This result does not belong to the current agreed build.";
    else if (!approvalReviewFresh(run)) reason = "Refresh merge state to read current approval and build facts.";
    else if (review?.approval.state !== "recorded") reason = "Review and approve this result before merging.";
    else if (build.taking.state !== "passed") reason = "The current build has no passed result to merge.";
    else if (run.state.type !== "completed" || !run.state.value.report.passed ||
      review.approval.candidate_commit !== run.candidate_commit ||
      review.approval.report_digest !== run.state.value.report.report_digest ||
      review.approval.suite_commit !== run.suite_commit) reason = "The saved approval does not match this result. Refresh merge state.";
    return { eligible: reason === null && !pending, pending, reason,
      receipt: outcome?.receipt ?? null, error: outcome?.error ?? null,
      onMerge: () => { if (run !== null) mergeApprovedRun(run); },
      onRefresh: () => { if (run !== null) refreshMergeState(run); } };
  }

  // mergeApprovedRun captures the exact tuple before any asynchronous read can
  // change selection. Lost responses recover through reads, never automatic retry.
  function mergeApprovedRun(run: VerificationStatusResult) {
    if (!mergeControls(run).eligible || run.state.type !== "completed") return;
    const identity = mergeIdentity(run);
    if (identity === null) return;
    const request: Operations["merge_change"]["request"] = {
      change_id: run.change_id, request_revision: run.request_revision,
      candidate_commit: run.candidate_commit, report_digest: run.state.value.report.report_digest,
    };
    mergeOutcome = { identity, receipt: null, error: null };
    // Reads begun before the mutation cannot establish its outcome or restore
    // approval admission. A failure needs a fresh owner read before another try.
    buildGeneration++;
    verificationGeneration++;
    callServer("merge_change", request, run.change_id, (result) => {
      if (sessionState !== "active") return;
      mergeOutcome = { identity, receipt: { at: new Date().toISOString(), ...result }, error: null };
      statusEl.textContent = "Merged approved change " + run.change_id + " into " + result.branch + " at " + result.head_commit.slice(0, 12) + ".";
      readBuild(run.change_id);
      refreshOpenCard(run.change_id);
      readTeam();
      void loadChanges();
    }, (message, failure) => {
      if (sessionState !== "active") return;
      mergeOutcome = { identity, receipt: null, error: {
        name: failure.kind === "server" ? failure.body?.name ?? null : null, message,
      } };
      statusEl.textContent = "Merge was not confirmed. Refresh merge state before trying again.";
    });
  }

  // mergePanel keeps manual merge commands secondary. A fresh merged build
  // can also supply inputs for a separate check of the resulting commit.
  function mergePanel(run: VerificationStatusResult | null, idPrefix: string, observedBuild: StartedBuild | null = null): React.ReactNode {
    const controls = mergeControls(run);
    const build = run !== null ? knownTakingBuild(run.change_id) : observedBuild;
    const review = run !== null && selectedReview?.runId === run.run_id ? selectedReview.review : null;
    const approvedBuild = run !== null && build !== null && build.in_force && runBelongsToBuild(run.change_id, build, run) &&
      (build.taking.state === "passed" || build.taking.state === "merged") && review?.approval.state === "recorded";
    const tests = build?.test_writer.tests;
    const postMerge = build?.taking.state === "merged" && (approvedBuild || observedBuild !== null)
      ? <MergedBranchVerification id={idPrefix + "-verify-merged"} requestRevision={build.request_revision}
        suiteCommit={approvedBuild ? run.suite_commit : tests?.state === "accepted" ? tests.commit : null}
        approvedSuite={approvedBuild} mergedCommit={approvedBuild ? controls.receipt?.head_commit ?? null : null}
        storedEvaluator={tests?.state === "not_used"} />
      : null;
    if (run === null || !approvedBuild && !controls.pending && controls.receipt === null && controls.error === null) return postMerge;
    let manualInstructions: React.ReactNode = null;
    if (controls.eligible && build !== null && build.taking.state === "passed" &&
      review?.approval.state === "recorded" && approvalReviewFresh(run) &&
      runBelongsToBuild(run.change_id, build, run) && run.state.type === "completed") {
      manualInstructions = <BuildMergeInstructions taking={build.taking} candidateCommit={run.candidate_commit}
        testsCommit={run.test_source.source === "written_tests" ? run.test_source.commit : null}
        onFocusedControlRemoved={focusVerificationHeading} />;
    }
    return <><MergeFeedback idPrefix={idPrefix} controls={controls} />
      {manualInstructions}{postMerge}</>;
  }

  // isViewingChangeResult reports whether the active result view currently
  // displays a CP2 preview or CP4 verification view for changeId.
  function isViewingChangeResult(changeId: string): boolean {
    if (activeResult?.kind === "cp2-architecture-preview") {
      return activeEditor !== null && activeEditor.card.change_id === changeId;
    }
    if (activeResult?.kind === "cp4-verification" || activeResult?.kind === "cp5-approval") {
      const displayedRun = resolveDisplayedVerificationRun();
      const effectiveId = displayedRun
        ? displayedRun.change_id
        : selectedVerificationChangeId || "";
      return effectiveId === changeId;
    }
    return activeResult?.kind === "changes";
  }

  // hasUnsavedEdits reports typed edits the page would lose on unload.
  function hasUnsavedEdits(): boolean {
    if (!activeEditor) {
      return false;
    }
    return (
      isDraftDirty(activeEditor.card, activeEditor.draftAnswers, activeEditor.draftExamples) ||
      activeEditor.preservedDraft !== null
    );
  }

  // hasPendingEditorSave excludes card reads, which lock the editor but have
  // no unsaved request to lose. The generated route owns the read/write fact.
  function hasPendingEditorSave(): boolean {
    return editorRequest !== null && ROUTES[editorRequest].method !== "GET";
  }

  // hasUnsavedPageInput protects all input that reloading this document loses.
  // Agreement warnings continue to use the narrower CP1 draft predicate.
  function hasUnsavedPageInput(): boolean {
    return hasUnsavedEdits() || hasPendingEditorSave() || modelDraftDirty ||
      (activeEditor !== null && Object.keys(activeEditor.proposalAnswers).length > 0);
  }

  // openPendingLink re-enters the normal bootstrap after saving or discarding.
  // replaceState avoids queuing another hashchange before the reload.
  function openPendingLink() {
    if (pendingLinkFragment === null || hasUnsavedPageInput()) return;
    history.replaceState(null, "", window.location.pathname + window.location.search + pendingLinkFragment);
    window.location.reload();
  }

  // hasUnsavedEditorState identifies work that a different stage must leave
  // available for the user to recover or continue editing.
  function hasUnsavedEditorState(): boolean {
    return hasUnsavedEdits() || (activeEditor !== null && activeEditor.formError !== null);
  }

  // shouldRetainEditor keeps drafts and the destination of a pending editor
  // response visible when another stage is shown.
  function shouldRetainEditor(): boolean {
    return activeEditor !== null && ((editorOpen && editorRequest !== null) || hasUnsavedEditorState());
  }

  // clearEditorDataset removes DOM metadata when no card is displayed.
  function clearEditorDataset() {
    delete editorRegionEl.dataset.state;
    delete editorRegionEl.dataset.changeId;
    delete editorRegionEl.dataset.requestRevision;
  }

  // clearResultDataset prevents a new result from inheriting old DOM metadata.
  function clearResultDataset() {
    delete resultRegionEl.dataset.state;
    delete resultRegionEl.dataset.changeId;
    delete resultRegionEl.dataset.requestRevision;
    delete resultRegionEl.dataset.designDigest;
    delete resultRegionEl.dataset.runId;
  }

  // showChanges switches to the change list, reading it when none is loaded.
  function showChanges() {
    editorOpen = shouldRetainEditor();
    activeResult = { kind: "changes" };
    rootEl.dataset.state = "changes";
    statusEl.textContent =
      changeList === null
        ? "Reading this project's changes..."
        : changeList.changes.length === 0
        ? "This project has no changes yet."
        : "Showing this project's " + changeList.changes.length + " changes.";
    if (changeList === null) {
      void loadChanges();
    }
    syncAllRegions();
  }

  // openStory shows the Story stage: the open change's saved Story, if a
  // change is open, and the owner's current Story evaluation, which it reads
  // when it was never read or the change list's version moved since.
  function openStory() {
    editorOpen = shouldRetainEditor();
    activeResult = { kind: "story" };
    rootEl.dataset.state = "story";
    statusEl.textContent =
      activeEditor !== null
        ? "Showing the Story of Change Card " + activeEditor.card.change_id + "."
        : "Showing the current Story draft.";
    if (storyEvaluation === null || storyEvaluationStale) {
      readStoryEvaluation();
    }
    syncAllRegions();
  }

  // readStoryEvaluation reads the owner's current Story evaluation. A read
  // asked for while one is in flight runs once more after it, so the page
  // never keeps an answer older than the last version it saw.
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
        if (activeResult?.kind === "story") {
          statusEl.textContent = "Could not read the current Story draft.";
        }
        done();
      }
    );
  }

  // openTeam shows the shared team snapshot without dropping an edited card.
  function openTeam() {
    editorOpen = shouldRetainEditor();
    activeResult = { kind: "team" };
    rootEl.dataset.state = "team";
    statusEl.textContent = "Showing the project team.";
    if (teamSnapshot === null || teamReadError !== null || waitTroubleShown) readTeam();
    syncAllRegions();
    document.getElementById("surla-team-title")?.focus();
  }

  // readTeam follows the existing single version chain. One extra read is
  // retained when another change arrives during an outstanding request.
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
      "get_team", {}, "",
      (snapshot) => {
        if (sessionState !== "active") return;
        teamSnapshot = snapshot;
        teamReadError = null;
        proposeMissingWorkerChoices();
        done();
      },
      (message) => {
        if (sessionState !== "active") return;
        const firstFailure = teamReadError === null;
        teamReadError = { readFailed: true, message };
        if (firstFailure) {
          statusEl.textContent = "Could not read the project team. Open Team to see the error and retry.";
        }
        done();
      }
    );
  }

  // buildWorkerRoles is the owner's plan for a new build, including the
  // builder-only case before any working copy or role has been registered.
  function buildWorkerRoles(): readonly WorkerRole[] {
    return agentOptions?.build_roles ?? [];
  }

  // teamWorkerRoles selects only registered workers with current assignments.
  function teamWorkerRoles(): WorkerRole[] {
    return (teamSnapshot?.roles ?? []).flatMap((role) => role.role !== "guide" &&
      (role.assignment === "unread" || role.assignment === "nothing_owed") ? [role.role] : []);
  }

  // proposeMissingWorkerChoices refreshes only automatic proposals. A user's
  // input remains unchanged even when saved preferences or discovery change.
  function proposeMissingWorkerChoices() {
    if (agentOptions === null || teamSnapshot === null) return;
    const saved = { ...teamSnapshot.saved_choices };
    for (const role of editedWorkerChoices) {
      const choice = workerChoices[role];
      if (choice !== undefined) saved[role] = choice;
    }
    const roles = Array.from(new Set([...buildWorkerRoles(), ...teamWorkerRoles()]));
    const proposed = proposeWorkerChoices(roles, agentOptions.agents, saved, teamSnapshot.herdr_mode);
    for (const role of roles) {
      if (!editedWorkerChoices.has(role)) workerChoices[role] = proposed[role];
    }
  }

  // loadAgentOptions is a stable mount callback; the owner is not probed by
  // wait_for_changes or routine renders once discovery has completed.
  function loadAgentOptions() {
    if (agentOptions === null && agentOptionsError === null) readAgentOptions();
  }

  // readAgentOptions is the explicit discovery and retry path. Failed reads
  // retain drafts but cannot authorize a new launch from stale capabilities.
  function readAgentOptions() {
    if (sessionState !== "active" || hasPending("list_team_agent_options", "")) return;
    if (teamSnapshot === null || teamFactsError() !== null) readTeam();
    callServer("list_team_agent_options", {}, "", (result) => {
      if (sessionState !== "active") return;
      agentOptions = result;
      agentOptionsError = null;
      proposeMissingWorkerChoices();
    }, (message) => {
      if (sessionState === "active") agentOptionsError = message;
    });
  }

  // workersReady shares the same admission facts between visible controls
  // and their callbacks; empty or stale discovery never silently uses defaults.
  function workersReady(roles: readonly WorkerRole[]): boolean {
    return agentOptions !== null && agentOptionsError === null &&
      !hasPending("list_team_agent_options", "") && teamFactsError() === null &&
      workerChoicesReady(roles, workerChoices, agentOptions.agents,
        teamSnapshot?.herdr_mode ?? null, backgroundConsent);
  }

  // dismissTeamNotice saves only the named recommendation, without launching
  // an agent or changing a capability. A refusal leaves the notice visible.
  function dismissTeamNotice(notice: TeamNotice) {
    if (sessionState !== "active" || hasPending("dismiss_team_notice", notice)) return;
    noticeErrors.delete(notice);
    callServer("dismiss_team_notice", { notice }, notice, () => {
      if (sessionState !== "active") return;
      noticeDismissed.add(notice);
      readTeam();
    }, (message) => {
      if (sessionState === "active") noticeErrors.set(notice, message);
    });
  }

  // workerRecommendations appear beside worker selection, when their advice
  // is relevant. They share the owner's persisted dismissals across views.
  function workerRecommendations(idPrefix: string): React.ReactNode {
    const snapshot = teamSnapshot;
    if (snapshot === null || teamFactsError() !== null) return null;
    const families = new Set((agentOptions?.agents ?? []).filter((option) =>
      snapshot.herdr_mode !== "unavailable" || option.headless_supported).map((option) => option.client));
    const notices: { notice: TeamNotice; title: string; text: string }[] = [];
    if (buildWorkerRoles().includes("test_writer") && families.size === 1) notices.push({
      notice: "single_agent_family", title: "One coding-agent family is installed.",
      text: "Surla can use it for both workers. A second model family can give the builder and test writer different perspectives.",
    });
    if (snapshot.herdr_mode === "available") notices.push({
      notice: "outside_herdr", title: "No Herdr pane linked.",
      text: "To link your coding agent’s pane, open a project tab in Herdr and run /surla there. Starting workers opens their panes in Herdr without bringing its window to the foreground.",
    });
    if (snapshot.herdr_mode === "unavailable") notices.push({
      notice: "herdr_unavailable", title: "Workers will run in the background.",
      text: "Herdr provides a live workspace with agent panes. You can continue here with server-managed background workers instead.",
    });
    return notices.filter(({ notice }) => !noticeDismissed.has(notice) &&
      !snapshot.dismissed_notices.includes(notice)).map(({ notice, title, text }) =>
      <Alert key={notice} id={idPrefix + "-notice-" + notice}>
        <p><strong>{title}</strong></p><p>{text}</p>
        {noticeErrors.has(notice) ? <p role="alert">Could not save this dismissal. {noticeErrors.get(notice)}</p> : null}
        <Button id={idPrefix + "-notice-" + notice + "-dismiss"}
          disabled={sessionState !== "active" || hasPending("dismiss_team_notice", notice)}
          onClick={() => dismissTeamNotice(notice)}>
          {hasPending("dismiss_team_notice", notice) ? "Saving…" : noticeErrors.has(notice) ? "Retry dismissal" : "Dismiss recommendation"}
        </Button>
      </Alert>);
  }

  // workerPicker renders controlled values for this action's required roles.
  function workerPicker(idPrefix: string, roles: readonly WorkerRole[]): React.ReactNode {
    return <>{workerRecommendations(idPrefix)}<AgentPickerView idPrefix={idPrefix} roles={roles}
      options={agentOptions?.agents ?? null} choices={workerChoices}
      loading={hasPending("list_team_agent_options", "")} error={agentOptionsError}
      herdrMode={teamSnapshot?.herdr_mode ?? null} disabled={sessionState !== "active" ||
        hasPending("start_team_agents", "") || Array.from(pendingCalls).some((call) => call.op === "start_build")}
      backgroundConsent={backgroundConsent} onBackgroundConsentChange={(value) => {
        backgroundConsent = value;
        syncAllRegions();
      }} onChoiceChange={(role, choice) => {
        workerChoices = { ...workerChoices, [role]: choice };
        editedWorkerChoices.add(role);
        proposeMissingWorkerChoices();
        syncAllRegions();
      }} onRefresh={readAgentOptions} onLoad={loadAgentOptions} /></>;
  }

  // teamFactsError distinguishes retained observations from currently followed
  // facts when the read, the update chain or the session is unavailable.
  function teamFactsError(): TeamFactsError | null {
    if (teamReadError !== null) return teamReadError;
    if (sessionState !== "active") return { readFailed: false, message: "The session ended; these are last-read facts." };
    if (waitTroubleShown) return { readFailed: false, message: "Updates are unavailable; these are last-read facts." };
    return null;
  }

  // connectedGuide finds a registered guide that can still follow the project,
  // including a guide whose terminal state Herdr cannot observe.
  function connectedGuide(): TeamRoleState | undefined {
    return teamSnapshot?.roles.find((role) =>
      role.role === "guide" && role.state !== "not_started" && role.state !== "ended"
    );
  }

  // showingTeamRoles derives pending controls from the registered snapshot,
  // preserving the generated role type instead of casting arbitrary subjects.
  function showingTeamRoles(): TeamRoleState["role"][] {
    return (teamSnapshot?.roles ?? [])
      .filter((role) => hasPending("show_team_pane", role.role))
      .map((role) => role.role);
  }

  // focusCurrentViewHeading recovers focus only when a focused view control
  // disappears. Each rendered region starts with its current view's heading.
  function focusCurrentViewHeading() {
    const region = activeResult !== null && activeResult.kind !== "cp1-incoming-card"
      ? resultRegionEl : editorRegionEl;
    const heading = region.querySelector<HTMLElement>("h2");
    if (heading !== null) {
      heading.tabIndex = -1;
      heading.focus();
    }
  }

  // focusChangeDecisions is a stable removal callback for approval commands,
  // including when their editor remains beside another view.
  function focusChangeDecisions() {
    editorRegionEl.focus();
  }

  // focusVerificationHeading keeps a removed result command in its own view.
  // Its stable identity avoids ref cleanup during unrelated renders.
  function focusVerificationHeading() {
    focusResult(activeResult?.kind === "cp5-approval" ? "#surla-cp5-title" : "#surla-cp4-title");
  }

  // teamWorkersHaveWork reads the current work facts for registered workers.
  function teamWorkersHaveWork() {
    return teamSnapshot?.roles.some((role) => role.role !== "guide" &&
      (role.assignment === "unread" || role.assignment === "nothing_owed")) ?? false;
  }

  // startTeamWorkers requests the project's default worker choices. The dated
  // receipt records this request; readTeam supplies current role health.
  function startTeamWorkers() {
    if (sessionState !== "active" || !teamWorkersHaveWork() ||
        !workersReady(teamWorkerRoles()) || hasPending("start_team_agents", "")) return;
    const begun = new Date().toISOString();
    teamActionError = null;
    callServer(
      "start_team_agents", { choices: choicesForRoles(teamWorkerRoles(), workerChoices) }, "",
      (result) => {
        if (sessionState !== "active") return;
        teamStartReceipt = { at: begun, result };
        buildStartReceipt = null;
        statusEl.textContent = "This start (begun " + new Date(begun).toLocaleString() +
          ") returned: " + result.step;
        readTeam();
      },
      (message) => {
        if (sessionState !== "active") return;
        teamActionError = message;
        statusEl.textContent = "Could not start workers. " + message;
      }
    );
  }

  // readBuild retains one selected change's user projection. The update chain
  // coalesces a later read instead of creating an independent polling loop.
  function readBuild(changeId: string) {
    if (sessionState !== "active" || activeEditor?.card.change_id !== changeId) return;
    if (buildChangeId !== changeId) {
      buildChangeId = changeId;
      buildSnapshot = null;
      buildDraftedBeforeDrop = null;
      buildReadError = null;
      buildStartReceipt = null;
      buildStartError = null;
      openBuildRefusal = null;
      buildConfirmationRead = null;
      followBuildFindings();
      buildGeneration++;
    }
    if (hasPending("get_build", changeId)) {
      buildReadAgain = true;
      return;
    }
    buildReadAgain = false;
    const generation = buildGeneration;
    const relevant = () => sessionState === "active" && buildChangeId === changeId &&
      activeEditor?.card.change_id === changeId;
    const done = () => {
      if (relevant() && buildReadAgain) readBuild(changeId);
    };
    callServer(
      "get_build", { change_id: changeId }, changeId,
      (result) => {
        if (relevant() && generation === buildGeneration) {
          if (result.change_id !== changeId) {
            buildReadError = "The server returned a build for a different change.";
          } else {
            const before = buildSnapshot;
            buildSnapshot = result.build;
            buildDraftedBeforeDrop = result.drafted_before_drop;
            buildReadError = null;
            const replaced = before?.state === "started"
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
            // An unchanged build still settles the dependency that held
            // approval history back while this read was pending.
            refreshVerificationObservations(changeId);
            followBuildFindings();
            readBlockingBuild();
            const ready = buildConfirmationRead;
            buildConfirmationRead = null;
            ready?.(result);
          }
        }
        done();
      },
      (message) => {
        if (relevant() && generation === buildGeneration) {
          buildReadError = message;
          const ready = buildConfirmationRead;
          buildConfirmationRead = null;
          ready?.(null);
        }
        done();
      }
    );
  }

  // readBlockingBuild follows the known blocking change through the existing
  // build-read chain. A read of the selected change alone cannot clear it.
  // A different open start invalidates the existing question's exact identity.
  function readBlockingBuild() {
    const refused = openBuildRefusal;
    if (refused === null || refused.change_id === activeEditor?.card.change_id ||
        sessionState !== "active" || hasPending("get_build", refused.change_id)) return;
    const selected = activeEditor?.card.change_id;
    callServer("get_build", { change_id: refused.change_id }, refused.change_id,
      (result) => {
        if (sessionState !== "active" || activeEditor?.card.change_id !== selected ||
            openBuildRefusal !== refused || result.change_id !== refused.change_id) return;
        if (result.build.state !== "started" ||
            (result.build.taking.state !== "building" && result.build.taking.state !== "passed")) {
          openBuildRefusal = null;
          buildStartError = null;
        } else {
          openBuildRefusal = { ...refused, build: result.build };
        }
      },
      () => { /* Keep the last owner-reported blocker until a successful read. */ });
  }

  // currentFindingsBasis names the exact saved verdict whose text is useful.
  // Assignment reads can change independently without invalidating that text.
  function currentFindingsBasis(): string | null {
    if (buildChangeId === null || buildSnapshot?.state !== "started" ||
        (buildSnapshot.taking.state !== "building" && buildSnapshot.taking.state !== "passed") ||
        buildSnapshot.test_writer.tests.state !== "needs_changes" ||
        hasPending("start_build", buildChangeId)) return null;
    return JSON.stringify([
      buildChangeId, buildSnapshot.request_revision, buildSnapshot.started_at,
      buildSnapshot.test_writer.report, buildSnapshot.test_writer.tests.checked_at,
      buildSnapshot.test_writer.tests.finding_count,
    ]);
  }

  // followBuildFindings starts one user read per new verdict. Read failures
  // remain explicit; an unchanged snapshot never becomes a retry loop.
  function followBuildFindings() {
    const next = currentFindingsBasis();
    if (next === findingsBasis) return;
    findingsBasis = next;
    buildFindings = null;
    findingsError = null;
    if (next !== null) readBuildFindings();
  }

  // readBuildFindings accepts text only when every owner-provided correlation
  // field still matches. A late answer never selects a newer or earlier build.
  function readBuildFindings() {
    const expected = buildSnapshot;
    const changeId = buildChangeId;
    const basis = currentFindingsBasis();
    if (sessionState !== "active" || changeId === null || basis === null ||
        expected?.state !== "started" || expected.test_writer.tests.state !== "needs_changes" ||
        activeEditor?.card.change_id !== changeId) return;
    if (hasPending("get_test_findings", changeId)) {
      findingsReadAgain = true;
      return;
    }
    findingsReadAgain = false;
    findingsError = null;
    const generation = buildGeneration;
    const verdict = expected.test_writer.tests;
    const relevant = () => sessionState === "active" && buildGeneration === generation &&
      activeEditor?.card.change_id === changeId && findingsBasis === basis &&
      currentFindingsBasis() === basis;
    const done = () => {
      if (findingsReadAgain) {
        findingsReadAgain = false;
        readBuildFindings();
      }
    };
    callServer(
      "get_test_findings", { change_id: changeId }, changeId,
      (result) => {
        if (relevant()) {
          if (result.change_id !== changeId || result.request_revision !== expected.request_revision ||
              result.started_at !== expected.started_at || result.report !== expected.test_writer.report ||
              result.checked_at !== verdict.checked_at || result.findings.length !== verdict.finding_count) {
            findingsError = "The findings no longer match this build. Refresh the build to read its current verdict.";
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

  // prepareBuildConfirmation reads the saved build before the control asks
  // the user to replace it. A failed or invalidated read cannot ask.
  function prepareBuildConfirmation(ready: (build: ChangeBuild | null) => void) {
    if (activeEditor === null || sessionState !== "active") return;
    buildConfirmationRead = ready;
    readBuild(activeEditor.card.change_id);
  }

  // retainOpenBuild uses only a failure body validated by the generated route.
  function retainOpenBuild(failure: Failure) {
    const body = failure.body;
    if (body !== undefined && "build" in body &&
        (body.name === "previous_change_open" || body.name === "unfinished_build_open" || body.name === "build_changed")) {
      openBuildRefusal = body;
      if (body.change_id === buildChangeId) {
        buildSnapshot = { ...body.build, state: "started" };
        buildDraftedBeforeDrop = null;
        buildReadError = null;
      }
    }
  }

  // startBuild sends the revision displayed by the editor only after its
  // Build control has obtained any required replacement confirmation.
  function startBuild(replaces?: ReplacedBuild) {
    const ed = activeEditor;
    if (ed === null || sessionState !== "active" || editorRequest !== null ||
        ed.card.status !== "confirmed" || ed.incomingCard !== null || !workersReady(buildWorkerRoles()) ||
        buildChangeId !== ed.card.change_id || buildSnapshot === null ||
        buildReadError !== null || buildDraftedBeforeDrop !== false || hasPending("start_build", ed.card.change_id) ||
        (openBuildRefusal !== null && openBuildRefusal.change_id !== ed.card.change_id)) return;
    const changeId = ed.card.change_id;
    const revision = ed.card.request_revision;
    if (buildSnapshot.state === "started" && buildSnapshot.in_force &&
        buildSnapshot.request_revision !== revision) return;
    const listedRevision = changeList?.changes.find((item) => item.change_id === changeId)?.request_revision;
    if (listedRevision !== undefined && listedRevision !== revision) return;
    const request: Operations["start_build"]["request"] = {
      change_id: changeId,
      request_revision: revision,
      choices: choicesForRoles(buildWorkerRoles(), workerChoices),
    };
    // The Build control binds its confirmation to this exact saved start;
    // synchronous rendering invalidates that question when the start changes.
    if (buildSnapshot.state === "started" && buildSnapshot.taking.state === "building") {
      if (replaces?.change_id !== changeId || replaces.started_at !== buildSnapshot.started_at) return;
      request.replaces = replaces;
    }
    const begun = new Date().toISOString();
    buildGeneration++;
    verificationGeneration++;
    buildStartReceipt = null;
    buildStartError = null;
    openBuildRefusal = null;
    buildConfirmationRead = null;
    buildFindings = null;
    findingsBasis = null;
    findingsError = null;
    findingsReadAgain = false;
    callServer(
      "start_build", request, changeId,
      (result) => {
        if (sessionState !== "active" || activeEditor?.card.change_id !== changeId ||
            activeEditor.card.request_revision !== revision) return;
        // A read begun during the start may still describe its predecessor.
        // The successful answer supersedes it, including transient receipts.
        buildGeneration++;
        verificationGeneration++;
        if (result.build.request_revision !== revision) {
          buildStartError = "The server returned a build for a different revision. Refresh the saved build.";
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
        if (sessionState === "active" && activeEditor?.card.change_id === changeId) {
          buildStartError = (failure.kind === "server"
            ? "Build did not start. " : "Could not confirm whether the build started. ") + message;
          retainOpenBuild(failure);
          statusEl.textContent = openBuildRefusal !== null && openBuildRefusal.change_id !== changeId
            ? "" : buildStartError;
          readBuild(changeId);
        }
      }
    );
  }

  // dropBuild submits only the start shown by the separate Drop question.
  // The owner refuses stale consent and returns the current build's facts.
  function dropBuild(target: ReplacedBuild) {
    if (sessionState !== "active" || editorRequest !== null || activeEditor === null ||
        hasPending("drop_build", target.change_id)) return;
    const shown = target.change_id === buildChangeId && buildSnapshot?.state === "started"
      ? buildSnapshot : openBuildRefusal?.change_id === target.change_id ? openBuildRefusal.build : null;
    if (shown === null || shown.started_at !== target.started_at ||
        (shown.taking.state !== "building" && shown.taking.state !== "passed")) return;
    const selected = activeEditor.card.change_id;
    buildGeneration++;
    buildConfirmationRead = null;
    buildStartError = null;
    callServer("drop_build", target, target.change_id,
      (result) => {
        if (sessionState !== "active" || activeEditor?.card.change_id !== selected) return;
        buildGeneration++;
        openBuildRefusal = null;
        if (result.change_id === buildChangeId) {
          buildSnapshot = result.build;
          buildDraftedBeforeDrop = result.drafted_before_drop;
        }
        statusEl.textContent = "The build was dropped. Ask your coding agent to refresh its draft and propose a revision. Adopt the revision before building again.";
        readBuild(selected);
        refreshOpenCard(selected);
        refreshVerificationObservations(selected);
        readTeam();
      },
      (message, failure) => {
        if (sessionState !== "active" || activeEditor?.card.change_id !== selected) return;
        buildStartError = "Could not drop the build. " + message;
        retainOpenBuild(failure);
        statusEl.textContent = openBuildRefusal !== null && openBuildRefusal.change_id !== selected
          ? "" : buildStartError;
        readBuild(selected);
      });
  }

  // showTeamPane sends only the registered role the user selected; the server
  // rechecks ownership and open dialogs before it changes the window.
  function showTeamPane(role: TeamRoleState["role"]) {
    if (sessionState !== "active" || !roleHasPane(role, teamSnapshot?.herdr_mode) ||
        hasPending("show_team_pane", role)) return;
    teamActionError = null;
    const label = role === "guide" ? "coding agent" : role.replaceAll("_", " ");
    callServer(
      "show_team_pane", { role }, role,
      () => {
        if (sessionState !== "active") return;
        statusEl.textContent = "The " + label + "'s pane is shown in Herdr.";
        readTeam();
      },
      (message) => {
        if (sessionState !== "active") return;
        teamActionError = message;
        statusEl.textContent = "Could not show the " + label + "'s pane. " + message;
      }
    );
  }

  // openLive shows the Live target view: the live target, read when it was
  // never read or the change list's version moved since, and the model
  // connection, read each time the view opens because no version covers it.
  function openLive() {
    editorOpen = shouldRetainEditor();
    activeResult = { kind: "live" };
    rootEl.dataset.state = "live";
    statusEl.textContent = "Showing the live target and the model connection.";
    if (liveTarget === null || liveTargetStale) {
      readLiveTarget();
    }
    readModelConnection();
    syncAllRegions();
  }

  // readLiveTarget reads the live target. A read asked for while one is in
  // flight runs once more after it, so the page never keeps an answer older
  // than the last version it saw.
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
        // A stop's message describes the state its answer left; it goes once
        // that state changes.
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
        if (activeResult?.kind === "live") {
          statusEl.textContent = "Could not read the live target.";
        }
        done();
      }
    );
  }

  // readModelConnection reads the model live targets started afterwards use.
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

  // stopLiveTarget sends the one stop request the user confirmed, shows the
  // server's message, and reads the live target again.
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

  // showStopAnswer shows a stop's answer and reads the live target again;
  // the first read issued from now on records the state the answer left.
  function showStopAnswer(answer: ServerAnswer) {
    liveStopOutcome = answer;
    liveStopBasis = null;
    liveStopAfterRead = liveReadCount;
    readLiveTarget();
  }

  // updateModelDraft records input and refreshes discard and pending-link
  // controls. The uncontrolled text fields retain focus and selection.
  function updateModelDraft(change: Partial<ModelDraft>) {
    const redraw = !modelDraftDirty || change.kind !== undefined;
    modelDraft = { ...modelDraft, ...change };
    modelDraftDirty = true;
    if (redraw) {
      syncResultRegion();
      syncNoticeRegion();
    }
  }

  // discardModelDraft clears unsent model input locally, including after the
  // session ends, without changing the server's model connection.
  function discardModelDraft() {
    if (hasPending("configure_model", "")) return;
    modelDraft = { kind: null, provider: "anthropic", modelName: "", gatewayAddr: "" };
    modelDraftDirty = false;
    modelDraftReset++;
    syncAllRegions();
    document.getElementById("surla-model-title")?.focus();
  }

  // applyModel asks the server to use choice for live targets started
  // afterwards, shows its answer or refusal, and reads the connection again.
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

  // openArchitecture shows the open change's architecture stage and asks the
  // server for the architecture of the shown revision when none is loaded.
  function openArchitecture() {
    if (!activeEditor) {
      return;
    }
    const card = activeEditor.card;
    editorOpen = shouldRetainEditor();
    activeResult = { kind: "cp2-architecture-preview" };
    rootEl.dataset.state = "cp2-architecture-preview";
    if (
      previewState === null ||
      previewState.change_id !== card.change_id ||
      previewState.request_revision !== card.request_revision
    ) {
      requestArchitecture(card);
    } else {
      statusEl.textContent = "Showing architecture preview for " + previewState.system_name + ".";
    }
    syncAllRegions();
  }

  // requestArchitecture asks the server to evaluate the bound design for the
  // card's exact revision.
  function requestArchitecture(card: ChangeCardResult) {
    if (hasPending("preview_architecture", card.change_id)) {
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
        if (activeResult?.kind === "cp2-architecture-preview") {
          statusEl.textContent = "Showing architecture preview for " + preview.system_name + ".";
        }
      },
      (message) => {
        previewErrors.set(card.change_id, message);
        if (activeResult?.kind === "cp2-architecture-preview") {
          statusEl.textContent = "Could not preview the architecture.";
        }
      }
    );
  }

  // openProof shows the open change's saved verification runs.
  function openProof() {
    const changeId = activeEditor ? activeEditor.card.change_id : selectedVerificationChangeId;
    if (!changeId) {
      return;
    }
    if (selectedVerificationChangeId === changeId && lastLoadedListByChangeId.has(changeId)) {
      editorOpen = shouldRetainEditor();
      activeResult = { kind: "cp4-verification" };
      rootEl.dataset.state = "cp4-verification";
      statusEl.textContent =
        selectedRunId !== null
          ? "Showing the saved verification result."
          : "Showing saved verification runs for Change Card " + changeId + ".";
      if (selectedRunId !== null) readVerification(selectedRunId, changeId);
      syncAllRegions();
      return;
    }
    handleListVerifications(changeId);
  }

  // syncNavRegion renders the stage navigation bar for an active session
  // along with the displayed-subject context strip (#surla-workspace-context).
  function syncNavRegion() {
    const approvalWasFocused = document.activeElement?.id === "surla-cp5-approve";
    if (sessionState === "starting" || sessionState === "signed-out") {
      navEl.hidden = true;
      flushSync(() => {
        navRoot.render(null);
      });
      return;
    }

    const tabs: {
      id: string;
      label: string;
      active: boolean;
      onClick: () => void;
    }[] = [];
    const onDescribe = activeResult?.kind === "changes";
    const changesLabel = changeList !== null && (changeList.changes.length > 0 || activeEditor !== null)
      ? "Changes" : "Describe";
    if (!onDescribe || changeList === null || changeList.changes.length > 0 || activeEditor !== null) {
      tabs.push({ id: "surla-nav-changes", label: changesLabel, active: onDescribe, onClick: showChanges });
    }
    tabs.push({
      id: "surla-nav-team",
      label: "Team",
      active: activeResult?.kind === "team",
      onClick: openTeam,
    });
    tabs.push({
      id: "surla-nav-story",
      label: "Story",
      active: activeResult?.kind === "story",
      onClick: openStory,
    });
    tabs.push({
      id: "surla-nav-live",
      label: "Live target",
      active: activeResult?.kind === "live",
      onClick: openLive,
    });
    const cp1Active =
      editorOpen && (!activeResult || activeResult.kind === "cp1-incoming-card");
    if (activeEditor !== null) {
      tabs.push({
        id: "surla-nav-cp1",
        label: "Agree",
        active: cp1Active && editorStage === "agree",
        onClick: handleReturnToChangeCard,
      });
      tabs.push({
        id: "surla-nav-cp2",
        label: "Architecture",
        active: activeResult?.kind === "cp2-architecture-preview",
        onClick: openArchitecture,
      });
      tabs.push({
        id: "surla-nav-build", label: "Build", active: cp1Active && editorStage === "build",
        onClick: () => { showChangeEditor("build"); focusResult("#surla-build-title"); },
      });
    }
    if (activeEditor !== null || selectedVerificationChangeId !== null) {
      tabs.push({
        id: "surla-nav-cp4",
        label: "Check",
        active: activeResult?.kind === "cp4-verification",
        onClick: openProof,
      });
    }

    if (activeEditor !== null || selectedVerificationChangeId !== null) {
      tabs.push({ id: "surla-nav-cp5", label: "Approve", active: activeResult?.kind === "cp5-approval", onClick: openApproval });
    }

    const bothRegionsShown =
      activeEditor !== null &&
      editorOpen &&
      activeResult !== null &&
      activeResult.kind !== "cp1-incoming-card";

    const subjects: WorkspaceSubjectDescriptor[] = [];
    if (activeEditor !== null && editorOpen) {
      subjects.push({
        role: "editor",
        label: bothRegionsShown ? "Protected editor" : "Showing",
        stage: editorStage === "build" ? "Build" : "Agree",
        changeId: activeEditor.card.change_id,
        requestRevision: activeEditor.card.request_revision,
        detail:
          activeEditor.card.status === "confirmed" ? "confirmed" : "needs confirmation",
      });
      if (activeResult?.kind === "cp1-incoming-card" && activeEditor.incomingCard) {
        subjects.push({
          role: "incoming",
          label: "Incoming card",
          stage: "Updated Change Card",
          changeId: activeEditor.incomingCard.change_id,
          requestRevision: activeEditor.incomingCard.request_revision,
          detail:
            activeEditor.incomingCard.status === "confirmed"
              ? "confirmed"
              : "needs confirmation",
        });
      }
    }
    const shownPreview =
      previewState !== null &&
      activeEditor !== null &&
      previewState.change_id === activeEditor.card.change_id
        ? previewState
        : null;
    const resultSubject = describeActiveResultSubject(
      activeResult,
      shownPreview,
      resolveDisplayedVerificationRun(),
      selectedVerificationChangeId,
      bothRegionsShown,
      activeEditor !== null ? activeEditor.card : null,
      storyEvaluation
    );
    if (resultSubject !== null) {
      subjects.push(resultSubject);
    }
    if (activeResult?.kind === "team") {
      subjects.push({
        role: "result",
        label: bothRegionsShown ? "Displayed view" : "Showing",
        stage: "Project team",
        detail: teamSnapshot?.team,
      });
    }

    navEl.hidden = false;
    const journeyIds = new Set([
      "surla-nav-changes", "surla-nav-cp1", "surla-nav-cp2", "surla-nav-build", "surla-nav-cp4", "surla-nav-cp5",
    ]);
    const run = resolveDisplayedVerificationRun();
    const review = displayedApprovalReview();
    const onApproval = activeResult?.kind === "cp5-approval";
    const fresh = run !== null && approvalReviewFresh(run);
    const teamError = teamFactsError();
    const teamLoading = hasPending("get_team", "");
    const teamStarting = hasPending("start_team_agents", "");
    const teamRoles = teamSnapshot?.roles ?? [];
    const herdrMode = teamSnapshot?.herdr_mode;
    const guideInHerdr = herdrMode === "attached";
    const herdrWorkerAvailable = herdrMode !== "unavailable";
    const guide = connectedGuide();
    const teamNeedsYou = teamRoles.find((role) => role.state === "needs_you");
    const teamStatusUnknown = teamRoles.some((role) => role.state === "error" ||
      (role.state === "unknown" && (role.role !== "guide" || guideInHerdr)));
    const proposal = activeEditor?.card.proposed_revision;
    const revisionTarget = activeEditor === null ? null : proposal !== undefined
      ? answersNeedingRevision(proposal.ambiguities, activeEditor.proposalAnswers).length > 0
        ? "#surla-proposal-revision-needed" : null
      : answersNeedingRevision(activeEditor.card.ambiguities, activeEditor.draftAnswers).length > 0
        ? "#surla-confirm-revision-needed" : null;
    const showingInitialization = onDescribe && !editorOpen && listError === null &&
      changeList?.initializing_phase !== undefined && changeList.changes.length === 0;
    const storyDraftAttention = changeList?.draft_state === "story_failed" &&
      (!guideInHerdr || guide?.state !== "working");
    const showSlots = !showingInitialization && !(onDescribe && !editorOpen && listError === null &&
      (lastOpenAttempt === null || !cardLoadErrors.has(lastOpenAttempt)) &&
      changeList !== null && changeList.changes.length === 0 && !storyDraftAttention && teamError === null &&
      !teamLoading && !teamStarting && teamSnapshot !== null && teamNeedsYou === undefined &&
      !teamStatusUnknown && guide !== undefined && (!guideInHerdr || guide.state !== "starting"));
    let attention = "Nothing now.";
    let next: React.ReactNode = null;
    if (onApproval) {
      attention = review?.approval.state === "blocked" ? "Approval unavailable."
        : review?.approval.state === "unverified" ? "Saved approval needs checking." : review?.approval.state === "recorded"
        ? "Your approval is recorded."
        : fresh ? "Decide whether this verified result may be promoted." : "Check the owner’s current approval requirements.";
      next = review?.approval.state === "available" && run !== null
        ? <><Button id="surla-cp5-approve" variant="primary" disabled={!fresh || editorRequest !== null}
          aria-describedby={!fresh ? "surla-cp5-observation-status" : undefined}
          onClick={() => handleApprove(run)}>{editorRequest !== null ? "Approving…" : "Approve this result"}</Button>
          <span className="surla-frame-action-binding">Run {run.run_id.slice(0, 12)}</span></>
        : review?.approval.state === "recorded" ? <span>Review the current build before merging.</span>
        : review?.approval.state === "blocked"
        ? <Button onClick={() => focusResult("#surla-cp5-approve-blocked")}>Review requirements</Button>
        : review?.approval.state === "unverified" ? <span>Check current approval.</span> : <span>Read current requirements.</span>;
      if (review?.approval.state === "recorded" && run !== null) {
        const build = knownTakingBuild(run.change_id);
        if (build === null) {
          next = <Button disabled={editorRequest !== null || hasPending("get_build", run.change_id)}
            onClick={() => readBuild(run.change_id)}>Read current build</Button>;
        } else if (build.in_force && runBelongsToBuild(run.change_id, build, run)) {
          switch (build.taking.state) {
            case "merged":
              attention = "The approved build is merged.";
              next = <span>Describe the next change when you are ready.</span>;
              break;
            case "passed":
              next = <MergePrimaryAction id="surla-cp5-merge" controls={mergeControls(run)} />;
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
        if (review?.approval.state === "blocked") attention = "A newer check is available.";
        next = <><Button id="surla-cp5-review-latest"
          variant={review?.approval.state === "available" || review?.approval.state === "recorded" ? "default" : "primary"}
          disabled={editorRequest !== null} onClick={() => reviewSavedCheck(newer)}>Review latest check</Button>{next}</>;
      }
      if (run !== null && hasPending("merge_change", run.change_id)) {
        attention = "Merging the approved result.";
        next = <MergePrimaryAction id="surla-cp5-merge" controls={mergeControls(run)} />;
      }
    } else if (cp1Active && activeEditor !== null &&
        (revisionTarget !== null || proposal !== undefined && activeEditor.card.status === "confirmed") &&
        activeEditor.incomingCard === null &&
        sessionState === "active" && editorRequest === null && !cardLoadErrors.has(activeEditor.card.change_id) &&
        !hasPending("merge_change", activeEditor.card.change_id)) {
      if (revisionTarget !== null) {
        attention = "Ask your coding agent to revise the draft for your selected answers.";
        next = <Button id="surla-review-revision-request" variant="primary"
          onClick={() => focusResult(revisionTarget)}>Review revision request</Button>;
      } else {
        attention = "Adopt or decline the proposed revision.";
        next = <Button id="surla-review-proposal" variant="primary"
          onClick={() => focusResult("#surla-proposal")}>Review proposed revision</Button>;
      }
    } else if (cp1Active && editorStage === "build" && activeEditor !== null) {
      const build = buildChangeId === activeEditor.card.change_id && buildSnapshot?.state === "started"
        ? buildSnapshot : null;
      if (buildReadError !== null && (activeEditor.card.status === "confirmed" || build !== null)) {
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
          attention = teamError !== null || teamSnapshot === null || teamStatusUnknown
            ? "Team status unknown." : "Nothing now.";
          next = <span>The test writer corrects the tests and reports again.</span>;
        } else if (tests.state === "could_not_run") {
          attention = "The tests could not be checked.";
          next = <span>{tests.step}</span>;
        } else if (build.taking.state === "passed") {
          const approved = buildApprovalRun(activeEditor.card);
          attention = approved !== null ? "Your approval is recorded." : "A verification of this build passed.";
          next = approved !== null && mergeControls(approved).eligible
            ? <MergePrimaryAction id="surla-build-merge" controls={mergeControls(approved)} />
            : approved !== null ? <Button onClick={() => refreshMergeState(approved)}>Check current approval</Button>
            : <span>Review the saved result below before approving it.</span>;
        } else if (tests.state === "reported") {
          attention = "Surla is checking the reported tests.";
          next = <span>Wait for Surla's verdict.</span>;
        } else {
          const assignmentsRead = build.builder.read &&
            (build.test_writer.read || tests.state === "accepted" || tests.state === "not_used");
          attention = teamError !== null || teamSnapshot === null || teamStatusUnknown
            ? "Team status unknown." : assignmentsRead ? "Nothing now." : "A worker assignment is unread.";
          next = <span>{tests.state === "accepted" || tests.state === "not_used"
            ? "The builder implements and verifies the change." : "The test writer prepares and reports the tests."}</span>;
        }
      }
      if (hasPending("merge_change", activeEditor.card.change_id)) {
        attention = "Merging the approved result.";
        next = <MergePrimaryAction id="surla-build-merge" controls={mergeControls(buildApprovalRun(activeEditor.card))} />;
      }
      if (next !== null && teamError === null && teamNeedsYou !== undefined) {
        attention = teamRoles.filter((role) => role.state === "needs_you")
          .map((role) => teamAttentionText(role, roleHasPane(role.role, herdrMode), teamSnapshot?.chorus_status)).join(". ");
      }
    } else if (activeResult?.kind === "cp4-verification") {
      next = <Button variant="primary" onClick={openApproval}>Review result</Button>;
      attention = "Read the saved test evidence, then review the result for approval.";
    }
    if (next === null) {
      // Recovery takes precedence over guidance based on retained observations.
      const connectGuide = "Connect your coding agent to this project’s Surla MCP server.";
      attention = teamNeedsYou !== undefined ? teamAttentionText(teamNeedsYou, roleHasPane(teamNeedsYou.role, herdrMode), teamSnapshot?.chorus_status)
        : teamLoading || teamSnapshot === null ? "Waiting for Team status."
        : teamStatusUnknown ? "Team status unknown."
        : "Nothing now.";
      let step: string | React.ReactElement;
      if (sessionState !== "active") {
        attention = "The session ended.";
        step = "Ask your coding agent to open Surla views, or choose Open Surla Window in the Herdr companion.";
      } else if (listError !== null) {
        attention = "The changes could not be read.";
        step = onDescribe ? "Choose Read the changes again below." : "Open " + changesLabel + " to retry reading changes.";
      } else if (waitTroubleShown) {
        attention = "Updates are unavailable.";
        step = "Wait for Surla to reconnect.";
      } else if (teamError !== null) {
        attention = "Team status could not be read.";
        step = activeResult?.kind === "team" ? "Choose Retry team read below." : "Open Team to retry reading roles.";
      } else if (lastOpenAttempt !== null && lastOpenAttempt !== activeEditor?.card.change_id && cardLoadErrors.has(lastOpenAttempt)) {
        attention = "The Change Card could not be opened.";
        step = onDescribe ? "Try opening the change again below." : "Open " + changesLabel + " and try opening the change again.";
      } else if (activeEditor !== null && cardLoadErrors.has(activeEditor.card.change_id)) {
        attention = "The latest saved Change Card could not be read.";
        step = editorRequest !== null ? "Wait for the current Change Card request."
          : cp1Active && activeEditor.formError !== null
          ? "Choose Load Latest Card to review the saved card alongside your edits."
          : <Button onClick={handleLoadLatestCard}>Load Latest Card</Button>;
      } else if (onDescribe) {
        if (editorRequest === "get_change") {
          step = "Wait for the Change Card to open.";
        } else if (changeList === null) {
          step = "Wait for Surla to read the changes.";
        } else if (changeList.changes.length > 0) {
          step = "Open a change from the list below.";
        } else if (changeList.initializing_phase !== undefined) {
          step = "Wait for Surla to finish preparing the project.";
        } else if (teamLoading || teamSnapshot === null) {
          step = "Wait for Team status.";
        } else if (guide === undefined) {
          if (teamNeedsYou === undefined) attention = "Coding agent needed.";
          step = connectGuide + " Then describe your change.";
        } else if (!guideInHerdr) {
          if (guide.state === "needs_you") {
            step = "Return to your coding agent to address its request.";
          } else if (guide.state === "error") {
            step = "Return to your coding agent to check its error.";
          } else if (teamStatusUnknown && teamNeedsYou === undefined) {
            step = "Open Team to check role status.";
          } else if (changeList.draft_state === "story_failed") {
            if (teamNeedsYou === undefined) attention = "Story check needs attention.";
            step = "Return to your coding agent so it can fix the Story and propose the Change Card.";
          } else if (changeList.draft_state !== undefined) {
            step = "Wait for your coding agent to propose the Change Card.";
          } else {
            step = "Describe what you want to build in your coding agent.";
          }
        } else if (guide.state === "starting") {
          step = "Wait for your coding agent to start.";
        } else if (guide.state === "unknown" || guide.state === "error") {
          if (teamNeedsYou === undefined) {
            attention = guide.state === "unknown" ? "Coding agent status unknown." : "Your coding agent reported an error.";
          }
          step = "Open Team and refresh to check your coding agent’s status.";
        } else if (guide.state === "needs_you") {
          step = "In Team, choose Show coding agent pane to address its request.";
        } else if (guide.state === "working" || changeList.draft_state === "evaluating_story") {
          step = "Wait for your coding agent to propose the Change Card.";
        } else if (changeList.draft_state === "story_failed") {
          if (teamNeedsYou === undefined) attention = "Story check needs attention.";
          step = "Choose Show coding agent pane below so it can fix the Story and propose the Change Card.";
        } else if (changeList.draft_state !== undefined) {
          step = "Choose Show coding agent pane below to check on your coding agent.";
        } else {
          step = "Choose Show coding agent pane below to describe your change.";
        }
      } else if (cp1Active && activeEditor !== null) {
        const ed = activeEditor;
        if (editorRequest !== null && editorRequest !== "confirm_intent") {
          step = "Wait for the current Change Card request.";
        } else if (ed.incomingCard !== null) {
          attention = "A newer saved revision is available.";
          step = "Choose Switch to Incoming Card to review it and keep your unsaved edits.";
        } else if (editorStage === "agree" && ed.card.proposed_revision !== undefined) {
          attention = "A proposed revision is waiting for your decision.";
          step = "Review the proposal below, then choose Adopt or Decline.";
        } else if (ed.formError?.source === "request") {
          attention = "The Change Card request failed.";
          step = "Choose Load Latest Card to review the saved card alongside your edits.";
        } else if (ed.card.status === "needs_confirmation" && editorStage === "agree") {
          const unanswered = unansweredQuestions(ed);
          const question = unanswered[0];
          const exampleIssue = firstIncompleteExample(ed);
          const confirming = editorRequest === "confirm_intent";
          if (question !== undefined) {
            attention = unanswered.length === 1 ? "1 question to answer." : unanswered.length + " questions to answer.";
            step = <Button id="surla-cp1-next-question" variant="primary"
              onClick={() => focusDraftQuestion(question.id)}>Jump to unanswered question</Button>;
          } else if (exampleIssue !== null) {
            attention = "An example needs " + exampleIssue.label + ".";
            step = <Button id="surla-cp1-next-example" variant="primary"
              onClick={() => focusDraftExample(exampleIssue)}>Review example</Button>;
          } else {
            attention = confirming ? "Saving your agreement." : "Ready to confirm intent.";
            step = <Button id="surla-cp1-next-confirm" variant="primary" disabled={confirming}
              onClick={handleConfirmSubmit}>{confirming ? "Confirming…" : "Confirm intent"}</Button>;
          }
        } else if (ed.card.status === "needs_confirmation") {
          attention = "This change needs confirmation.";
          step = "Review the questions and examples below, then confirm the change.";
        } else if (editorStage === "build") {
          step = "Review the Build controls below for this change.";
        } else if (ed.preservedDraft !== null) {
          attention = "You have edits from an earlier revision.";
          step = "Review the saved card and reapply any of the preserved edits you still need.";
        } else if (hasUnsavedEdits()) {
          attention = "You have unsaved refinements.";
          step = "Save or discard your refinements below.";
        } else {
          step = "Review Agreement details, then open Build when you’re ready.";
        }
      } else if (activeResult?.kind === "cp2-architecture-preview" && activeEditor !== null) {
        const card = activeEditor.card;
        if (hasPending("preview_architecture", card.change_id)) {
          step = "Wait for Surla to evaluate the design.";
        } else if (previewErrors.has(card.change_id)) {
          attention = "The architecture preview could not be loaded.";
          step = card.status === "needs_confirmation" ? "Open Agree to confirm the change."
            : "Use the Evaluate the design control below to try again.";
        } else if (shownPreview?.request_revision === card.request_revision) {
          step = "Review the architecture preview for this revision.";
        } else {
          step = "Evaluate the design below for the current revision.";
        }
      } else if (activeResult?.kind === "team") {
        step = teamStarting ? "Wait for workers to start."
          : teamLoading || teamSnapshot === null ? "Wait for Team status."
          : teamNeedsYou === undefined && teamWorkersHaveWork() ? "Use Start workers to bring up missing build workers."
          : guide === undefined ? connectGuide
          : guideInHerdr || (herdrWorkerAvailable && teamRoles.some((role) => role.role !== "guide"))
            ? "Review role status and use Show pane to contact an agent."
          : "Review role status below.";
      } else if (activeResult?.kind === "story") {
        if (storyEvaluationError !== null) {
          attention = "The Story draft could not be read.";
          step = "Choose Read the draft again below.";
        } else {
          step = storyEvaluation === null ? "Wait for Surla to read the Story draft."
            : storyEvaluation.state === "none" ? "Ask your coding agent to refresh the Story."
            : storyEvaluation.state === "running" ? "Wait for the Story evaluation."
            : storyEvaluation.state === "failed" ? "Ask your coding agent to fix the Story and refresh it."
            : "Review the current draft and any saved Story below.";
        }
      } else if (activeResult?.kind === "live") {
        if (liveTargetError !== null || modelConnectionError !== null) {
          attention = liveTargetError !== null ? "The live target could not be read." : "The model connection could not be read.";
          step = liveTargetError !== null ? "Choose Read the live target again below."
            : "Choose Read the model connection again below.";
        } else {
          const target = liveTarget?.live_target;
          step = target === undefined ? "Wait for Surla to read the live target."
            : target.state === "running" ? "Review the running service and its prompt drafts below."
            : target.state === "starting" ? "Wait for the live target to start."
            : target.state === "none" ? "Ask the builder to start a live target to try prompts."
            : "Review the last live target’s status below.";
        }
      } else {
        step = "Open " + changesLabel + " to find a change.";
      }
      next = typeof step === "string" ? <span>{step}</span> : step;
    }

    const agreedCard = (!onDescribe || editorOpen) && activeEditor?.card.status === "confirmed"
      ? activeEditor.card : null;
    flushSync(() => {
      navRoot.render(
        <WorkspaceFrame project={projectName ?? "Project"}
          journey={tabs.filter((tab) => journeyIds.has(tab.id))}
          references={tabs.filter((tab) => !journeyIds.has(tab.id))}
          agreed={agreedCard?.summary ?? null}
          onReadFullSummary={agreedCard !== null ? handleReadFullSummary : null}
          showSlots={showSlots} attention={attention} next={next}
          diagnosticsOpen={traceOpen} onToggleDiagnostics={() => {
            traceOpen = !traceOpen;
            if (traceOpen && tracePage === null) loadTracePage();
            syncAllRegions();
          }}
          details={subjects.length >= 1 ? (

            <div
              id="surla-workspace-context"
              className="surla-workspace-context"
              role="region"
              aria-label="Displayed workspace subject"
            >
              {subjects.map((subj) => (
                <div
                  key={subj.role}
                  id={"surla-context-" + subj.role}
                  className="surla-subject-item"
                  data-subject-role={subj.role}
                  data-change-id={subj.changeId}
                  data-request-revision={subj.requestRevision}
                >
                  <span className="surla-subject-label">{subj.label}</span>{" "}
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
          ) : null}
        />
      );
    });
    if (approvalWasFocused && document.getElementById("surla-cp5-approve") === null) {
      focusResult("#surla-cp5-title");
    }
  }

  // projectTitle owns the tab's next-step contract using only page facts.
  // Recovery outranks retained observations; saved run history does not prove
  // that the current change can be built or approved.
  function projectTitle(): string {
    if (projectName === null) return "Surla";
    let step: string;
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
      const attention = ["guide", "builder", "test_writer"].map((role) =>
        teamSnapshot?.roles.find((item) => item.role === role && item.state === "needs_you")
      ).find((role) => role !== undefined);
      if (attention !== undefined) {
        step = teamAttentionText(attention, roleHasPane(attention.role, teamSnapshot?.herdr_mode), teamSnapshot?.chorus_status);
      } else if (activeEditor !== null) {
        const ed = activeEditor;
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
      } else if (changeList.changes.length === 0) {
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

  // syncNoticeRegion keeps the tab title, session notice and live role attention
  // visible on every view.
  function syncNoticeRegion() {
    document.title = projectTitle();
    flushSync(() => {
      noticeRoot.render(
        <>
        {pendingLinkFragment !== null ? (
          <Alert id="surla-pending-link" role="status">
            <p>You opened a new Surla link in this tab. Save or discard your edits, then choose Open link.</p>
            {hasPendingEditorSave() ? <p>Wait for the current save to finish.</p> : null}
            {activeEditor !== null && Object.keys(activeEditor.proposalAnswers).length > 0 ? (
              <p>For proposal answers, choose Adopt this revision or Discard proposal answers. Discard clears your answers without declining the proposal.</p>
            ) : null}
            {modelDraftDirty ? <p>For the model choice, choose Apply or Discard model edits.</p> : null}
            <p>The link expires two minutes after it was printed, even while you finish your edits.</p>
            <Button id="surla-open-pending-link" disabled={hasUnsavedPageInput()} onClick={openPendingLink}>
              Open link
            </Button>
          </Alert>
        ) : null}
        {sessionState === "ended" ? (
          <SessionEndedNotice
            unsavedLines={
              activeEditor ? snapshotUnsavedDraft(activeEditor)?.lines ?? [] : []
            }
          />
        ) : null}
        {sessionState === "active" || sessionState === "ended" ? (
          <GlobalTeamAttention
            snapshot={teamSnapshot}
            lastRead={teamFactsError() !== null}
            disabled={sessionState !== "active"}
            showing={showingTeamRoles()}
            onShowPane={showTeamPane}
            onFocusedControlRemoved={focusCurrentViewHeading}
          />
        ) : null}
        {teamActionError !== null && activeResult?.kind !== "team" ? (
          <Alert id="surla-team-action-error" variant="destructive" role="alert">{teamActionError}</Alert>
        ) : null}
        </>
      );
    });
  }

  // revisionBuildNotice shares the build consequence beside both revision
  // actions. An unread or failed read cannot establish that no build exists.
  function revisionBuildNotice(card: ChangeCardResult): string | null {
    if (card.status !== "confirmed") return null;
    const known = buildChangeId === card.change_id && buildSnapshot !== null &&
      buildReadError === null && !waitTroubleShown;
    if (!known || buildSnapshot === null) {
      return "Changing the agreed revision leaves any existing build on the earlier revision. " +
        "Starting a new build replaces its assignments; when it uses a test writer, its report " +
        "and verdict reset and its tests need acceptance again before verification.";
    }
    if (buildSnapshot.state !== "started" || !buildSnapshot.in_force ||
        buildSnapshot.request_revision !== card.request_revision) return null;
    if (buildSnapshot.test_writer.tests.state === "not_used") {
      return "Changing the agreed revision leaves this build on the earlier revision. " +
        "Start a new build to update the builder’s assignment. " +
        "Verification keeps using the stored verification settings.";
    }
    return "Changing the agreed revision leaves this build on the earlier revision. " +
      "A new build replaces both assignments and clears the test writer’s report and verdict. " +
      "Its tests must be reported, checked and accepted again before verification.";
  }

  // draftRevisionAid gives the guide the exact decisions to incorporate. It
  // leaves the saved revision and the user's selections in place for review.
  function draftRevisionAid(id: string, changeId: string, choices: ReturnType<typeof answersNeedingRevision>) {
    if (choices.length === 0) return null;
    const prompt = [
      "Please revise change " + changeId + " so the draft implements these answers.",
      ...choices.map(({ question, answer }) => question + "\n" + answer),
      "Update the design, Story, examples, and preserved behaviors as needed. Propose the revised change for my review.",
    ].join("\n\n");
    const guidePane = teamFactsError() === null && roleHasPane("guide", teamSnapshot?.herdr_mode) &&
      connectedGuide() !== undefined;
    return <Alert id={id} tabIndex={-1} variant="warning" role="region" aria-labelledby={id + "-label"}>
      <p id={id + "-label"}>
        Your selected answers need a revision. Send this request to your coding agent, then review its revised draft here.
      </p>
      <div className="surla-field-row">
        <CopyPrompt text={prompt} label="Copy request for coding agent" />
        {guidePane ? <Button disabled={hasPending("show_team_pane", "guide")}
          onClick={() => showTeamPane("guide")}>Show coding agent pane</Button> : null}
      </div>
    </Alert>;
  }

  // syncEditorRegion renders saved values and drafts together when editorOpen
  // is true, with editing disabled while a Change Card request is outstanding.
  function syncEditorRegion() {
    if (!activeEditor || !editorOpen) {
      clearEditorDataset();
      flushSync(() => {
        editorRoot.render(null);
      });
      return;
    }
    const ed = activeEditor;
    editorRegionEl.dataset.state = "cp1-change-card";
    editorRegionEl.dataset.changeId = ed.card.change_id;
    editorRegionEl.dataset.requestRevision = ed.card.request_revision;
    const proposal = ed.card.proposed_revision;
    const buildRevisionNotice = revisionBuildNotice(ed.card);
    const latestCheck = latestVerificationFor(ed.card.change_id);
    flushSync(() => {
      editorRoot.render(
        <>
        {proposal !== undefined ? (
          <ProposedRevisionView
            card={ed.card}
            proposal={proposal}
            answers={ed.proposalAnswers}
            busy={editorRequest !== null}
            error={ed.proposalError}
            revisionAid={draftRevisionAid("surla-proposal-revision-needed", ed.card.change_id,
              answersNeedingRevision(proposal.ambiguities, ed.proposalAnswers))}
            buildRevisionNotice={buildRevisionNotice}
            onAnswer={(questionId, option) => {
              if (!activeEditor || editorRequest !== null) {
                return;
              }
              activeEditor = {
                ...activeEditor,
                proposalAnswers: { ...activeEditor.proposalAnswers, [questionId]: option },
              };
              syncAllRegions();
            }}
            onAdopt={handleAdopt}
            onDecline={handleDecline}
            onDiscardAnswers={() => {
              if (activeEditor === null || editorRequest !== null) return;
              activeEditor = { ...activeEditor, proposalAnswers: {} };
              syncAllRegions();
              focusResult("#surla-proposal");
            }}
          />
        ) : null}
        <CP1EditorView
          card={ed.card}
          buildRevisionNotice={buildRevisionNotice}
          buildSection={
            <BuildSectionView
              key={activeResult?.kind ?? "decisions"}
              card={ed.card}
              currentRevision={changeList?.changes.find((item) => item.change_id === ed.card.change_id)?.request_revision ??
                ed.incomingCard?.request_revision ?? ed.card.request_revision}
              build={buildChangeId === ed.card.change_id ? buildSnapshot : null}
              draftedBeforeDrop={buildChangeId === ed.card.change_id ? buildDraftedBeforeDrop : null}
              loading={hasPending("get_build", ed.card.change_id)}
              error={buildReadError}
              actionError={buildStartError}
              starting={hasPending("start_build", ed.card.change_id)}
              dropping={Array.from(pendingCalls).some((call) => call.op === "drop_build")}
              openBuildRefusal={openBuildRefusal}
              disabled={sessionState !== "active" || editorRequest !== null}
              dirty={isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples)}
              startReceipt={buildStartReceipt}
              findingTexts={buildFindings?.findings ?? null}
              findingsLoading={hasPending("get_test_findings", ed.card.change_id)}
              findingsError={findingsError}
              check={latestCheck}
              checkKnown={latestCheck !== null && runInputsKnown(latestCheck)}
              checkError={cardLoadErrors.get(ed.card.change_id) ??
                (latestCheck !== null ? runRequestErrors.get(latestCheck.run_id) ?? null : null)}
              approvalAvailable={approvalUnavailable === null && changeList?.approval_available === true}
              agentPicker={workerPicker("surla-build-agents", buildWorkerRoles())}
              agentsReady={workersReady(buildWorkerRoles())}
              mergeAction={mergePanel(buildApprovalRun(ed.card), "surla-build-merge-feedback", knownTakingBuild(ed.card.change_id))}
              onRefresh={() => readBuild(ed.card.change_id)}
              onReadFindings={readBuildFindings}
              onBuild={startBuild}
              onPrepareBuild={prepareBuildConfirmation}
              onDrop={dropBuild}
              onOpenTeam={openTeam}
              onOpenChange={openChange}
              onReviewCheck={() => { if (latestCheck !== null) reviewSavedCheck(latestCheck); }}
            />
          }
          proposalWaiting={proposal !== undefined}
          revisionAid={proposal === undefined ? draftRevisionAid("surla-confirm-revision-needed", ed.card.change_id,
            answersNeedingRevision(ed.card.ambiguities, ed.draftAnswers)) : null}
          priorCard={ed.priorCard}
          draftAnswers={ed.draftAnswers}
          draftExamples={ed.draftExamples}
          rpcInFlight={editorRequest !== null}
          loadingVerifications={hasPending("list_verifications", ed.card.change_id)}
          cp4Open={activeResult?.kind === "cp4-verification"}
          preservedDraft={ed.preservedDraft}
          formError={ed.formError?.message ?? null}
          onSetAnswer={(ambId, opt) => {
            if (!activeEditor || editorRequest !== null) {
              return;
            }
            activeEditor = {
              ...activeEditor,
              draftAnswers: { ...activeEditor.draftAnswers, [ambId]: opt },
              formError: activeEditor.formError?.source === "input" ? null : activeEditor.formError,
            };
            syncAllRegions();
          }}
          onSetExampleField={(exId, field, value) => {
            if (!activeEditor || editorRequest !== null) {
              return;
            }
            activeEditor = {
              ...activeEditor,
              draftExamples: activeEditor.draftExamples.map((item) =>
                item.id === exId ? { ...item, [field]: value } : item
              ),
              formError: activeEditor.formError?.source === "input" ? null : activeEditor.formError,
            };
            syncAllRegions();
          }}
          onSubmitConfirm={handleConfirmSubmit}
          onDiscardDraft={handleDiscardDraft}
          onLoadLatestCard={ed.formError?.source === "input" ? null : handleLoadLatestCard}
          onLoadVerifications={() => handleListVerifications(ed.card.change_id)}
          onOpenStory={openStory}
          onReviewApproval={openRecordedApproval}
        />

        </>
      );
    });
  }

  // verificationDetailsView is the single owner of raw report/history wiring,
  // shown in Check or in the approval stage's explicit detail disclosure.
  function verificationDetailsView(displayedRun: VerificationStatusResult | null, effectiveChangeId: string) {
    const build = knownTakingBuild(effectiveChangeId);
    const listedIds = lastLoadedListByChangeId.get(effectiveChangeId);
    const listedRuns = listedIds !== undefined
      ? listedIds.map((id) => runsById.get(id)).filter((r): r is VerificationStatusResult => Boolean(r))
      : null;
    const loadedCard = activeEditor ? activeEditor.card : null;
    const current = changeList?.changes.find((item) => item.change_id === effectiveChangeId);
    return (
      <VerificationView
        changeId={effectiveChangeId}
        build={activeResult?.kind === "cp5-approval" && build?.in_force === false ? null : build}
        run={displayedRun}
        inputsKnown={displayedRun !== null && runInputsKnown(displayedRun)}
        review={displayedRun !== null && displayedRun.run_id === selectedRunId ? displayedApprovalReview() : null}
        currentAgreementRevision={current?.status === "confirmed" ? current.request_revision : null}
        listedRuns={listedRuns}
        loadedCard={loadedCard}
        checking={displayedRun ? hasPending("get_verification", displayedRun.run_id) || hasPending("review_verification", displayedRun.run_id) : false}
        loadingList={hasPending("list_verifications", effectiveChangeId)}
        loadingCard={editorRequest !== null || cardRefresh !== null}
        requestError={displayedRun ? runRequestErrors.get(displayedRun.run_id) ?? null : null}
        listError={listRequestErrors.get(effectiveChangeId) ?? null}
        cardLoadError={effectiveChangeId ? cardLoadErrors.get(effectiveChangeId) ?? null : null}
        onCheckStatus={handleCheckVerificationStatus}
        onLoadVerifications={() => handleListVerifications(effectiveChangeId)}
        onSelectRun={handleSelectVerificationRun}
        onOpenChangeCard={() => {
          if (activeEditor?.card.change_id === effectiveChangeId) handleLoadLatestCard();
          else openChange(effectiveChangeId);
        }}
        onReturnToChangeCard={handleReturnToChangeCard}
      />
    );
  }

  // syncResultRegion replaces only the stage view beside the editor.
  function syncResultRegion() {
    clearResultDataset();
    if (sessionState === "signed-out") {
      resultRegionEl.dataset.state = "signed-out";
      flushSync(() => {
        resultRoot.render(<SignedOutView reason={signedOutReason} message={signedOutMessage} />);
      });
      return;
    }
    if (!activeResult) {
      flushSync(() => {
        resultRoot.render(null);
      });
      return;
    }
    const res = activeResult;
    resultRegionEl.dataset.state = res.kind;
    if (res.kind === "changes") {
      flushSync(() => {
        resultRoot.render(
          <ChangeListView
            list={changeList}
            loading={hasPending("list_changes", "")}
            error={listError}
            openError={lastOpenAttempt !== null ? cardLoadErrors.get(lastOpenAttempt) ?? null : null}
            openChangeId={activeEditor ? activeEditor.card.change_id : null}
            busy={editorRequest !== null}
            guideConnected={connectedGuide() !== undefined}
            guideInHerdr={teamSnapshot?.herdr_mode === "attached"}
            guideState={connectedGuide()?.state ?? null}
            disabled={sessionState !== "active"}
            showingGuide={hasPending("show_team_pane", "guide")}
            onShowGuidePane={() => showTeamPane("guide")}
            onOpen={openChange}
            onRetry={() => void loadChanges()}
          />
        );
      });
      return;
    }
    if (res.kind === "story") {
      flushSync(() => {
        resultRoot.render(
          <StoryStageView
            card={activeEditor !== null ? activeEditor.card : null}
            evaluation={storyEvaluation}
            loading={hasPending("get_story_refresh", "")}
            error={storyEvaluationError}
            onRetry={readStoryEvaluation}
          />
        );
      });
      if (activeEditor !== null) {
        resultRegionEl.dataset.changeId = activeEditor.card.change_id;
        resultRegionEl.dataset.requestRevision = activeEditor.card.request_revision;
      }
      return;
    }
    if (res.kind === "team") {
      flushSync(() => {
        resultRoot.render(
          <TeamStageView
            snapshot={teamSnapshot}
            loading={hasPending("get_team", "")}
            error={teamFactsError()}
            starting={hasPending("start_team_agents", "")}
            canStartWorkers={teamWorkersHaveWork()}
            agentPicker={teamWorkersHaveWork() ? workerPicker("surla-team-agents", teamWorkerRoles()) : workerRecommendations("surla-team-agents")}
            agentsReady={workersReady(teamWorkerRoles())}
            disabled={sessionState !== "active"}
            showing={showingTeamRoles()}
            startReceipt={teamStartReceipt}
            actionError={teamActionError}
            onRefresh={readTeam}
            onStartWorkers={startTeamWorkers}
            onShowPane={showTeamPane}
            onFocusedControlRemoved={focusCurrentViewHeading}
          />
        );
      });
      return;
    }
    if (res.kind === "live") {
      flushSync(() => {
        resultRoot.render(
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
            onDiscardDraft={discardModelDraft}
          />
        );
      });
      return;
    }
    if (res.kind === "cp2-architecture-preview") {
      const card = activeEditor ? activeEditor.card : null;
      const changeId = card ? card.change_id : "";
      const preview =
        previewState !== null && previewState.change_id === changeId ? previewState : null;
      const loading = hasPending("preview_architecture", changeId);
      const error = previewErrors.get(changeId) ?? null;
      if (preview) {
        resultRegionEl.dataset.changeId = preview.change_id;
        resultRegionEl.dataset.requestRevision = preview.request_revision;
        resultRegionEl.dataset.designDigest = preview.design_digest;
      }
      flushSync(() => {
        resultRoot.render(
          <>
            {preview ? (
              <ArchitecturePreviewView
                preview={preview}
                loadedCard={card}
                loadingCard={editorRequest !== null}
                cardLoadError={cardLoadErrors.get(preview.change_id) ?? null}
                onOpenChangeCard={() => openChange(preview.change_id)}
              />
            ) : null}
            {loading ? (
              <Card id="surla-cp2-loading" className="surla-card" role="status" aria-busy="true">
                Evaluating the design. This can take a little while.
              </Card>
            ) : null}
            {error !== null ? (
              <Alert id="surla-cp2-error" variant="destructive" role="alert">
                <strong>Could not preview the architecture. </strong>
                {error}
              </Alert>
            ) : null}
            {card !== null ? (
              <div className="surla-field-row">
                <Button
                  id="surla-cp2-refresh"
                  disabled={loading}
                  onClick={() => requestArchitecture(card)}
                >
                  {preview ? "Evaluate the design again" : "Evaluate the design"}
                </Button>
              </div>
            ) : null}
          </>
        );
      });
      return;
    }
    if (res.kind === "cp5-approval") {
      const run = resolveDisplayedVerificationRun();
      const recordCard = activeEditor?.card;
      const recordApproval = recordCard?.approval ?? recordCard?.unverified_approval?.approval;
      if (run !== null) {
        resultRegionEl.dataset.runId = run.run_id;
        resultRegionEl.dataset.changeId = run.change_id;
        resultRegionEl.dataset.requestRevision = run.request_revision;
      }
      flushSync(() => resultRoot.render(<ApprovalStage
        rawDetails={verificationDetailsView(run, selectedVerificationChangeId ?? "")}
        run={run} newerCheck={run !== null && newerCheckFor(run) !== null}
        review={displayedApprovalReview()} fresh={run !== null && approvalReviewFresh(run)}
        loading={run !== null && hasPending("review_verification", run.run_id)} busy={editorRequest !== null}
        error={run !== null ? runRequestErrors.get(run.run_id) ?? null : cardLoadErrors.get(selectedVerificationChangeId ?? "") ?? null}
        historyLoading={hasPending("list_verifications", selectedVerificationChangeId ?? "")}
        historyError={listRequestErrors.get(selectedVerificationChangeId ?? "") ?? null}
        approvalError={run !== null ? approveErrors.get(run.run_id) ?? null : null}
        unavailableReason={approvalUnavailable}
        build={run !== null ? knownTakingBuild(run.change_id) : null} unsaved={hasUnsavedEdits()}
        mergeAction={mergePanel(run, "surla-cp5-merge-feedback")}
        savedRecord={recordCard !== undefined && recordCard.change_id === selectedVerificationChangeId && recordApproval !== undefined
          ? { approval: recordApproval, requestRevision: recordCard.request_revision, reason: recordCard.unverified_approval?.reason ?? null } : null}
        onRefresh={() => { if (run !== null) readApprovalReview(run.run_id, run.change_id); else handleLoadLatestCard(); }}
        onOpenProof={openProof} onOpenAgreement={handleReturnToChangeCard}
      />));
      return;
    }
    if (res.kind === "cp4-verification") {
      const changeId = selectedVerificationChangeId || "";
      const displayedRun = resolveDisplayedVerificationRun();
      if (displayedRun) {
        resultRegionEl.dataset.runId = displayedRun.run_id;
        resultRegionEl.dataset.changeId = displayedRun.change_id;
        resultRegionEl.dataset.requestRevision = displayedRun.request_revision;
      } else if (changeId) {
        resultRegionEl.dataset.changeId = changeId;
      }
      const effectiveChangeId = displayedRun ? displayedRun.change_id : changeId;
      flushSync(() => resultRoot.render(verificationDetailsView(displayedRun, effectiveChangeId)));
      return;
    }
    const incoming = activeEditor ? activeEditor.incomingCard : null;
    if (!incoming) {
      flushSync(() => {
        resultRoot.render(null);
      });
      return;
    }
    resultRegionEl.dataset.changeId = incoming.change_id;
    resultRegionEl.dataset.requestRevision = incoming.request_revision;
    flushSync(() => {
      resultRoot.render(
        <IncomingChangeCardView
          incomingCard={incoming}
          onSwitch={() => {
            if (editorRequest !== null) {
              statusEl.textContent =
                "Wait for the pending CP1 Change Card request to finish before switching cards.";
              return;
            }
            const preserved = activeEditor ? snapshotUnsavedDraft(activeEditor) : null;
            const prior = nextPriorCard(activeEditor, incoming);
            activeResult = null;
            mountChangeCard(incoming, preserved, prior);
          }}
        />
      );
    });
  }

  // syncAllRegions synchronizes the notice, editor, result, nav, and trace regions.
  function syncAllRegions() {
    statusEl.toggleAttribute("data-connection-warning", waitTroubleShown);
    viewEl.classList.toggle(
      "surla-split-workspace",
      Boolean(activeEditor !== null && editorOpen && activeResult !== null)
    );
    syncNoticeRegion();
    syncEditorRegion();
    syncResultRegion();
    syncNavRegion();
    syncTraceRegion();
    syncOpenedCheckStatus();
    statusEl.hidden = (activeResult?.kind === "cp5-approval" ||
      editorOpen && editorStage === "build" && (!activeResult || activeResult.kind === "cp1-incoming-card")) && [
      "Review the build and its next step. The agreed details stay available below.",
      "Review the saved check before approving it.",
      "Showing the saved verification result.",
    ].includes(statusEl.textContent ?? "");
  }

  // syncOpenedCheckStatus names the user's decision on the opened change,
  // without replacing an agent's attention or an operation's failure message.
  function syncOpenedCheckStatus() {
    const ed = activeEditor;
    if (sessionState !== "active" || activeResult !== null || ed?.card.status !== "confirmed" ||
        editorRequest !== null || buildStartError !== null || buildReadError !== null ||
        listError !== null || waitTroubleShown || cardLoadErrors.has(ed.card.change_id) ||
        (teamSnapshot?.roles.some((role) => role.state === "needs_you") ?? false) ||
        isDraftDirty(ed.card, ed.draftAnswers, ed.draftExamples)) return;
    const run = latestVerificationFor(ed.card.change_id);
    if (run === null || !runInputsKnown(run)) return;
    let next: string;
    if (!run.inputs_current) {
      next = "The saved check describes earlier inputs. The builder checks the current agreement and tests again before you approve.";
    } else if (run.state.type === "running") {
      next = "Surla is checking the builder’s code. Your next decision is approval after the check passes.";
    } else if (run.state.type === "completed" && run.state.value.report.passed) {
      next = ed.card.approval?.report_digest === run.state.value.report.report_digest
        ? "The check passed and your approval is recorded."
        : "The check passed. Review the result.";
    } else {
      next = "The check needs the builder’s attention. Review its saved result.";
    }
    if (statusEl.textContent !== next) statusEl.textContent = next;
  }

  // loadTracePage reads one diagnostics page without taking the editor's
  // request slot. Filtering and paging are serialized by their own pending call.
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

  // syncTraceRegion updates only diagnostics; the CP1 editor stays mounted.
  function syncTraceRegion() {
    if (sessionState === "starting" || sessionState === "signed-out") {
      flushSync(() => traceRoot.render(null));
      return;
    }
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

  // markRunReadPending makes every older response unable to restore a match.
  function markRunReadPending(runId: string, order: number) {
    runObservationOrder.set(runId, order);
    runObservationGeneration.delete(runId);
  }

  // upsertRunSnapshot preserves a terminal report while accepting only a
  // current, non-older observation of its input match.
  function upsertRunSnapshot(
    run: VerificationStatusResult,
    generation: number,
    order: number
  ): VerificationStatusResult {
    const existing = runsById.get(run.run_id);
    const observed = generation === verificationGeneration &&
      order >= (runObservationOrder.get(run.run_id) ?? -1);
    const evidence = existing && existing.state.type !== "running" ? existing : run;
    const snapshot = {
      ...evidence,
      inputs_current: observed ? run.inputs_current : (existing?.inputs_current ?? false),
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

  // handleSelectVerificationRun switches the displayed run inside CP4.
  function handleSelectVerificationRun(runId: string) {
    const target = runsById.get(runId);
    if (!target) {
      return;
    }
    if (hasPending("list_verifications", target.change_id)) {
      selectionTouchedDuringList.set(target.change_id, true);
    }
    selectedVerificationChangeId = target.change_id;
    selectedRunId = target.run_id;
    const reviewing = activeResult?.kind === "cp5-approval";
    const stage = reviewing ? "cp5-approval" : "cp4-verification";
    activeResult = { kind: stage };
    rootEl.dataset.state = stage;
    statusEl.textContent = "Showing the saved verification result.";
    if (reviewing || !runInputsKnown(target) || displayedApprovalReview() === null) {
      readVerification(target.run_id, target.change_id);
    }
    syncAllRegions();
  }

  // handleCheckVerificationStatus reads the displayed run and never starts a
  // new verification. A late answer belongs only to that run, even if a
  // different run is displayed by then.
  function handleCheckVerificationStatus() {
    const run = resolveDisplayedVerificationRun();
    if ((activeResult?.kind === "cp4-verification" || activeResult?.kind === "cp5-approval") && run !== null) {
      readVerification(run.run_id, run.change_id);
    }
  }

  // readVerification coalesces a read invalidated while pending into one
  // replacement. A same-generation failure waits for an explicit retry.
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
      if (again && sessionState === "active" &&
          (activeEditor?.card.change_id === changeId || selectedVerificationChangeId === changeId)) {
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
          if (generation === verificationGeneration && order >= (runObservationOrder.get(runID) ?? -1)) {
            runRequestErrors.set(runID, "The server returned a different verification run.");
          }
          finish();
          return;
        }
        upsertRunSnapshot(run, generation, order);
        if (generation === verificationGeneration && activeResult?.kind === "cp4-verification" && selectedRunId === runID) {
          statusEl.textContent = "Showing the saved verification result.";
        }
        finish();
      },
      (message) => {
        if (generation === verificationGeneration && order >= (runObservationOrder.get(runID) ?? -1)) {
          runRequestErrors.set(runID, message);
          if (activeResult?.kind === "cp4-verification" && selectedRunId === runID) {
            statusEl.textContent = "Could not check verification status; the last result is preserved.";
          }
        }
        finish();
      }
    );
  }

  // readApprovalReview coalesces invalidated reads and accepts a response only
  // for its exact requested run, selection, generation and request order.
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
      if (again && sessionState === "active" && isViewingSelectedRun(runId)) {
        readApprovalReview(runId, changeId);
      }
    };
    callServer("review_verification", { run_id: runId }, runId, (result) => {
      if (result.run_id !== runId || result.change_id !== changeId) {
        if (generation === verificationGeneration) runRequestErrors.set(runId, "The server returned a different verification run.");
        finish();
        return;
      }
      if (result.review.approval.state === "available" &&
          (result.state.type !== "completed" || !result.state.value.report.passed)) {
        if (generation === verificationGeneration) runRequestErrors.set(runId, "The owner returned approval availability without a passed report. Refresh this result.");
        finish();
        return;
      }
      upsertRunSnapshot(result, generation, order);
      if (generation === verificationGeneration && order >= (runObservationOrder.get(runId) ?? -1) && selectedRunId === runId) {
        approvalUnavailable = null;
        selectedReview = { runId, generation, order, review: result.review };
      }
      finish();
    }, (message, failure) => {
      if (generation === verificationGeneration && order >= (runObservationOrder.get(runId) ?? -1)) {
        if (failure.status === 501 && selectedRunId === runId) approvalUnavailable = message;
        runRequestErrors.set(runId, message);
      }
      finish();
    });
  }

  // handleListVerifications loads the saved verification runs for requestedChangeId,
  // enforcing at most one in-flight list_verifications request per change_id.
  function handleListVerifications(requestedChangeId: string) {
    if (!requestedChangeId || hasPending("list_verifications", requestedChangeId)) {
      return;
    }
    const reviewing = activeResult?.kind === "cp5-approval";
    selectionTouchedDuringList.set(requestedChangeId, false);
    listRequestErrors.delete(requestedChangeId);
    if (!shouldRetainEditor()) {
      editorOpen = false;
    }
    const nextRunId =
      selectedRunId !== null &&
      runsById.get(selectedRunId)?.change_id !== requestedChangeId
        ? (lastLoadedListByChangeId.get(requestedChangeId)?.[0] ?? null)
        : selectedRunId;
    selectedVerificationChangeId = requestedChangeId;
    selectedRunId = nextRunId;
    const stage = reviewing ? "cp5-approval" : "cp4-verification";
    activeResult = { kind: stage };
    rootEl.dataset.state = stage;
    statusEl.textContent = "Loading saved verification runs...";
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
            (activeResult?.kind === "cp4-verification" || activeResult?.kind === "cp5-approval") &&
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
          (activeResult?.kind === "cp4-verification" || activeResult?.kind === "cp5-approval") &&
          selectedVerificationChangeId === requestedChangeId
        ) {
          if (
            touched &&
            selectedRunId !== null &&
            runsById.get(selectedRunId)?.change_id === requestedChangeId
          ) {
            // Preserve the run explicitly selected during the list read.
          } else if (selectedRunId !== null && loadedIds.includes(selectedRunId)) {
            // Preserve the selected run when it remains in the loaded list.
          } else {
            selectedRunId = loadedIds.length > 0 ? loadedIds[0] : null;
          }
          statusEl.textContent =
            loadedIds.length > 0
              ? "Showing saved verification runs for Change Card " + requestedChangeId + "."
              : "No saved verification runs found for Change Card " + requestedChangeId + ".";
        }
        if (selectedRunId !== null && (generation !== verificationGeneration || isViewingSelectedRun(selectedRunId))) {
          readVerification(selectedRunId, requestedChangeId);
        }
      },
      (message) => {
        selectionTouchedDuringList.delete(requestedChangeId);
        listRequestErrors.set(requestedChangeId, message);
        if (
          (activeResult?.kind === "cp4-verification" || activeResult?.kind === "cp5-approval") &&
          selectedVerificationChangeId === requestedChangeId
        ) {
          statusEl.textContent = "Could not load saved verification runs.";
        }
      }
    );
  }

  // handleReturnToChangeCard switches the view back to the retained Change Card
  // and restores any pending incoming Change Card banner.
  function handleReturnToChangeCard() {
    showChangeEditor("agree");
  }

  // handleReadFullSummary reveals the saved text in its existing owner without
  // replacing the retained editor or submitting its unsaved refinement.
  function handleReadFullSummary() {
    showChangeEditor("agree");
    const details = document.getElementById("surla-agreement-details");
    if (details instanceof HTMLDetailsElement) details.open = true;
    focusResult("#surla-agreement-summary");
    document.getElementById("surla-agreement-summary")?.scrollIntoView({ block: "start" });
  }

  // showChangeEditor restores retained form state and names the chosen stage.
  function showChangeEditor(stage: "agree" | "build") {
    if (!activeEditor) {
      return;
    }
    editorStage = stage;
    editorOpen = true;
    activeResult = activeEditor.incomingCard ? { kind: "cp1-incoming-card" } : null;
    rootEl.dataset.state = "cp1-change-card";
    const card = activeEditor.card;
    statusEl.textContent = activeEditor.incomingCard
      ? "A newer Change Card arrived while you were editing; your unsaved draft is preserved."
      : card.proposed_revision !== undefined
      ? "Review the proposed revision, then choose whether to adopt or decline it."
      : card.status === "confirmed"
      ? card.approval
        ? "Showing the recorded approval for these inputs."
        : card.unverified_approval
        ? "Showing the saved approval record (current promotion inputs could not be verified)."
        : stage === "build" ? "Review the build and its next step. The agreed details stay available below."
        : "Review the current agreement."
      : "Review the open questions and examples below, then confirm your intent.";
    syncAllRegions();
  }

  // openChange shows requestedChangeId, reading it with get_change when it is
  // not the card already open. A reply that arrives after the user moved to
  // another view, or that names another card, never replaces what is shown.
  function openChange(requestedChangeId: string) {
    if (!requestedChangeId || editorRequest !== null) {
      return;
    }
    if (activeEditor && activeEditor.card.change_id === requestedChangeId) {
      handleReturnToChangeCard();
      return;
    }
    const initialActiveResult = activeResult;
    lastOpenAttempt = requestedChangeId;
    cardLoadErrors.delete(requestedChangeId);
    statusEl.textContent = "Loading Change Card " + requestedChangeId + "...";
    callEditorServer(
      "get_change",
      { change_id: requestedChangeId },
      requestedChangeId,
      (loadedCard) => {
        if (loadedCard.change_id !== requestedChangeId) {
          cardLoadErrors.set(
            requestedChangeId,
            "The server returned Change Card " +
              loadedCard.change_id +
              " instead of " +
              requestedChangeId +
              "."
          );
          if (activeResult === initialActiveResult && isViewingChangeResult(requestedChangeId)) {
            statusEl.textContent = "Could not load the requested Change Card.";
          }
          return;
        }
        cardLoadErrors.delete(requestedChangeId);
        if (
          initialActiveResult !== null &&
          (activeResult !== initialActiveResult || !isViewingChangeResult(requestedChangeId))
        ) {
          return;
        }
        let preserved = activeEditor ? snapshotUnsavedDraft(activeEditor) : null;
        // Different-card navigation keeps unsent proposal choices as existing
        // preserved text. Adoption does not run this navigation-only capture.
        if (activeEditor !== null && activeEditor.card.change_id !== loadedCard.change_id) {
          const proposal = activeEditor.card.proposed_revision;
          const lines: string[] = [];
          if (proposal !== undefined) for (const question of proposal.ambiguities ?? []) {
            const answer = activeEditor.proposalAnswers[question.id];
            if (answer !== undefined) lines.push("[Proposed " + proposal.request_revision + "] " +
              question.id + " (" + question.question + ")=" + answer);
          }
          if (lines.length > 0) preserved = {
            fromRevision: preserved?.fromRevision ?? activeEditor.card.request_revision,
            lines: [...(preserved?.lines ?? []), ...lines],
          };
        }
        const prior = nextPriorCard(activeEditor, loadedCard);
        if (activeEditor) {
          activeEditor = {
            ...activeEditor,
            incomingCard: null,
          };
        }
        activeResult = null;
        mountChangeCard(loadedCard, preserved, prior);
      },
      (message) => {
        cardLoadErrors.set(requestedChangeId, "Could not load Change Card: " + message);
        if (activeResult === initialActiveResult && isViewingChangeResult(requestedChangeId)) {
          statusEl.textContent = "Could not load the requested Change Card.";
        }
      }
    );
  }

  // showFormError reports a failed read, save or input check without changing
  // the saved snapshot or discarding draft text.
  function showFormError(message: string, source: "input" | "request" = "request") {
    if (!activeEditor) {
      return;
    }
    statusEl.textContent = source === "input" ? message : "Change Card request failed; unsaved edits preserved.";
    activeEditor = {
      ...activeEditor,
      formError: { message, source },
    };
    syncAllRegions();
  }

  // focusResult moves keyboard focus to the element that shows an action's
  // result, after the regions rendered it.
  function focusResult(selector: string) {
    const target = document.querySelector(selector);
    if (target instanceof HTMLElement) {
      target.focus();
    }
  }

  // unansweredQuestions is shared by Next's progress and confirmation
  // validation, including questions rendered by a decision instrument.
  function unansweredQuestions(editor: ActiveEditorState) {
    return (editor.card.ambiguities ?? []).filter((question) =>
      !Object.prototype.hasOwnProperty.call(editor.draftAnswers, question.id) || !editor.draftAnswers[question.id]
    );
  }

  // focusDraftQuestion locates whichever renderer owns the current question.
  function focusDraftQuestion(questionId: string) {
    Array.from(editorRegionEl.querySelectorAll<HTMLButtonElement>("button[data-ambiguity-id]"))
      .find((option) => option.dataset.ambiguityId === questionId)?.focus();
  }

  // firstIncompleteExample locates the field blocking confirmation in the
  // same order that the user reads the example.
  function firstIncompleteExample(editor: ActiveEditorState) {
    for (const [index, example] of editor.draftExamples.entries()) {
      const blank = !example.scenario.trim() ? { field: "scenario", label: "a scenario" }
        : !example.input_summary.trim() ? { field: "input_summary", label: "an input" }
        : !example.expected_outcome.trim() ? { field: "expected_outcome", label: "an expected result" } : null;
      if (blank !== null) return { ...blank, exampleId: example.id, number: index + 1 };
    }
    return null;
  }

  // focusDraftExample locates a bound example field without interpolating its
  // owner-provided ID into a selector.
  function focusDraftExample(issue: { exampleId: string; field: string }) {
    Array.from(editorRegionEl.querySelectorAll<HTMLTextAreaElement>("textarea[data-example-id]"))
      .find((field) => field.dataset.exampleId === issue.exampleId && field.dataset.field === issue.field)?.focus();
  }

  // handleConfirmSubmit sends complete answers and examples for the displayed
  // saved revision after checking that the user filled every required value.
  function handleConfirmSubmit() {
    if (!activeEditor || editorRequest !== null) {
      return;
    }
    if (answersNeedingRevision(activeEditor.card.ambiguities, activeEditor.draftAnswers).length > 0) {
      focusResult("#surla-confirm-revision-needed");
      return;
    }
    const unanswered = unansweredQuestions(activeEditor)[0];
    if (unanswered !== undefined) {
      showFormError("Choose an answer for “" + unanswered.question + "” before confirming.", "input");
      focusDraftQuestion(unanswered.id);
      return;
    }
    const exampleIssue = firstIncompleteExample(activeEditor);
    if (exampleIssue !== null) {
      showFormError("Enter " + exampleIssue.label + " for example " + exampleIssue.number + ".", "input");
      focusDraftExample(exampleIssue);
      return;
    }
    activeEditor = {
      ...activeEditor,
      formError: null,
    };
    statusEl.textContent = "Saving your confirmed answers and examples...";

    const submittedCard = activeEditor.card;
    const submittedAnswers = { ...activeEditor.draftAnswers };
    const submittedExamples = activeEditor.draftExamples.map((item) => ({
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
        mountChangeCard(updatedCard, null, nextPriorCard(activeEditor, updatedCard));
        focusResult("#surla-cp1-editor");
      },
      (message) => {
        showFormError(message);
        focusResult("#surla-confirm-error");
      }
    );
  }

  // handleAdopt confirms the waiting proposal with the user's answers and its
  // examples: the proposal becomes the agreed revision and the earlier agreed
  // revision is shown as the prior one. Unsaved edits made on the earlier
  // agreed revision are kept as preserved lines, as switching to an incoming
  // revision keeps them. A refused adoption, such as one the guide's newer
  // proposal overtook, reads the change again.
  function handleAdopt() {
    if (!activeEditor || editorRequest !== null) {
      return;
    }
    const card = activeEditor.card;
    const proposal = card.proposed_revision;
    if (proposal === undefined) {
      return;
    }
    if (answersNeedingRevision(proposal.ambiguities, activeEditor.proposalAnswers).length > 0) {
      focusResult("#surla-proposal-revision-needed");
      return;
    }
    const answers: Record<string, string> = {};
    for (const question of proposal.ambiguities ?? []) {
      answers[question.id] = activeEditor.proposalAnswers[question.id];
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
        const preserved = activeEditor ? snapshotUnsavedDraft(activeEditor) : null;
        mountChangeCard(adopted, preserved, nextPriorCard(activeEditor, adopted));
        statusEl.textContent = "Adopted the proposed revision; it is now the agreed revision.";
        focusResult("#surla-cp1-editor");
      },
      (message, failure) => {
        failProposal(message, failure, card.change_id);
      }
    );
  }

  // handleDecline removes the waiting proposal; the agreed revision and the
  // user's drafts on it stay as they are.
  function handleDecline() {
    if (!activeEditor || editorRequest !== null) {
      return;
    }
    const card = activeEditor.card;
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
        if (activeEditor && activeEditor.card.request_revision === declined.request_revision) {
          activeEditor = { ...activeEditor, card: declined, proposalAnswers: {}, proposalError: null };
        } else {
          mountChangeCard(declined, null, nextPriorCard(activeEditor, declined));
        }
        statusEl.textContent = "Declined the proposed revision; the agreed revision stays in force.";
        syncAllRegions();
        focusResult("#surla-cp1-editor");
      },
      (message, failure) => {
        failProposal(message, failure, card.change_id);
      }
    );
  }

  // failProposal shows a refused adopt or decline beside the proposal and,
  // when the change moved on meanwhile, reads it again.
  function failProposal(message: string, failure: Failure, changeId: string) {
    if (activeEditor) {
      activeEditor = { ...activeEditor, proposalError: message };
    }
    statusEl.textContent = "The proposed revision was not changed.";
    syncAllRegions();
    focusResult("#surla-proposal-error");
    if (failure.status === 409) {
      refreshOpenCard(changeId);
    }
  }

  // handleApprove submits the selected owner's available observation, bound to
  // its exact run. Agreement/test/build admission is never recomputed here.
  function handleApprove(run: VerificationStatusResult) {
    const current = runsById.get(run.run_id);
    const review = displayedApprovalReview();
    if (current === undefined || current.run_id !== selectedRunId || editorRequest !== null ||
        !approvalReviewFresh(current) || review?.approval.state !== "available" ||
        current.state.type !== "completed") return;
    const report = current.state.value.report;
    approveErrors.delete(current.run_id);
    statusEl.textContent = "Recording approval of this result…";
    callEditorServer("approve_merge", {
      change_id: current.change_id, request_revision: current.request_revision,
      candidate_commit: current.candidate_commit, report_digest: report.report_digest,
    }, current.change_id, (approved) => {
      // Preserve unrelated editors and their drafts; a saved approval is an
      // owner fact, not permission to mount another card over the user's work.
      if (activeEditor?.card.change_id === approved.change_id) receiveLatestCard(approved);
      if (selectedReview?.runId === current.run_id && approved.approval !== undefined) {
        selectedReview = { ...selectedReview, review: {
          ...selectedReview.review, approval: { ...approved.approval, state: "recorded" },
        } };
      }
      statusEl.textContent = "Approval recorded. Surla did not merge or deploy anything.";
      syncAllRegions();
      if (activeResult?.kind === "cp5-approval" && selectedRunId === current.run_id) focusResult("#surla-cp5-approval");
    }, (message, failure) => {
      approveErrors.set(current.run_id, message);
      if (failure.status === 501) {
        approvalUnavailable = message;
        runObservationGeneration.delete(current.run_id);
        if (selectedReview?.runId === current.run_id) {
          selectedReview = { ...selectedReview, review: {
            ...selectedReview.review, approval: { state: "blocked", reason: message },
          } };
        }
      }
      statusEl.textContent = "Approval was not recorded.";
      syncAllRegions();
      if (activeResult?.kind === "cp5-approval" && selectedRunId === current.run_id) {
        if (failure.status !== 501) readApprovalReview(current.run_id, current.change_id);
        focusResult("#surla-cp5-approve-error");
      }
      if (failure.status === 409) refreshOpenCard(current.change_id);
    });
  }

  // handleDiscardDraft restores saved values on the retained card and closes
  // the split-screen editor when another stage is displayed beside it.
  function handleDiscardDraft() {
    if (!activeEditor || editorRequest !== null) {
      return;
    }
    const savedCard = activeEditor.card;
    if (activeResult && activeResult.kind !== "cp1-incoming-card") {
      activeEditor = {
        card: savedCard,
        priorCard: activeEditor.priorCard,
        incomingCard: activeEditor.incomingCard,
        ...editorDraftFrom(savedCard),
        preservedDraft: null,
        formError: null,
      };
      editorOpen = false;
      rootEl.dataset.state = activeResult.kind;
      syncAllRegions();
      return;
    }
    mountChangeCard(savedCard, null, activeEditor.priorCard);
  }

  // handleLoadLatestCard refreshes saved values and approval through get_change.
  // The same revision keeps editable drafts; a different revision preserves
  // their text for reconciliation instead of applying it to changed examples.
  function handleLoadLatestCard() {
    if (!activeEditor || editorRequest !== null) {
      return;
    }
    const changeId = activeEditor.card.change_id;
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
          showFormError(message);
          return;
        }
        cardLoadErrors.delete(changeId);
        if (
          activeEditor &&
          latestCard.change_id === activeEditor.card.change_id &&
          latestCard.request_revision === activeEditor.card.request_revision
        ) {
          activeEditor = {
            ...activeEditor,
            card: latestCard,
            formError: null,
          };
          if (!activeResult && activeEditor.incomingCard) {
            activeResult = { kind: "cp1-incoming-card" };
          }
          statusEl.textContent = latestCard.approval
            ? "Showing the recorded approval for these inputs."
            : latestCard.unverified_approval
            ? "Showing the saved approval record (current promotion inputs could not be verified)."
            : "Showing the latest card without a current approval.";
          refreshVerificationObservations(changeId);
          return;
        }
        const preserved = activeEditor ? snapshotUnsavedDraft(activeEditor) : null;
        const prior = nextPriorCard(activeEditor, latestCard);
        mountChangeCard(latestCard, preserved, prior);
      },
      (message) => {
        const error = "Could not load the current card and approval: " + message;
        cardLoadErrors.set(changeId, error);
        showFormError(error);
      }
    );
  }

  // mountChangeCard starts an editor from a received saved card and optionally
  // shows draft text or the immediately prior saved revision. Any distinct
  // incomingCard that arrived while an editor request was in flight remains.
  function mountChangeCard(
    card: ChangeCardResult,
    preservedDraft: PreservedDraft | null,
    priorCard: ChangeCardResult | null = null
  ) {
    if (activeEditor?.card.change_id !== card.change_id ||
        activeEditor.card.request_revision !== card.request_revision) verificationGeneration++;
    cardLoadErrors.delete(card.change_id);
    const existingIncoming = activeEditor ? activeEditor.incomingCard : null;
    const keepIncoming =
      existingIncoming !== null &&
      (existingIncoming.change_id !== card.change_id ||
        existingIncoming.request_revision !== card.request_revision)
        ? existingIncoming
        : null;
    if (!keepIncoming && activeResult?.kind === "cp1-incoming-card") {
      activeResult = null;
    }
    activeEditor = {
      card,
      priorCard,
      incomingCard: keepIncoming,
      ...editorDraftFrom(card),
      preservedDraft,
      formError: null,
    };
    editorOpen = true;
    editorStage = card.status === "confirmed" ? "build" : "agree";
    statusEl.textContent =
      card.proposed_revision !== undefined
        ? "Review the proposed revision, then choose whether to adopt or decline it."
      : card.status === "confirmed"
        ? card.approval
          ? "Showing the recorded approval for these inputs."
          : card.unverified_approval
          ? "Showing the saved approval record (current promotion inputs could not be verified)."
          : "Review the build and its next step. The agreed details stay available below."
        : "Review the open questions and examples below, then confirm your intent.";
    rootEl.dataset.state = "cp1-change-card";
    readBuild(card.change_id);
    refreshVerificationObservations(card.change_id);
    syncAllRegions();
  }

  // The tab's visibility pauses the waiting read; a hidden tab asks nothing.
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      verificationGeneration++;
      if (waitController !== null) {
        waitController.abort();
      }
      syncAllRegions();
      return;
    }
    if (activeEditor !== null) refreshVerificationObservations(activeEditor.card.change_id);
    startWaiting();
  });

  // Leaving the page loses edits the server never saved, so the browser asks first.
  window.addEventListener("beforeunload", (event) => {
    if (hasUnsavedPageInput()) {
      event.preventDefault();
      event.returnValue = "";
    }
  });

  // Pasting another one-use link in this tab must run the same first-load
  // exchange, but never discard unsaved input or leave a deferred link in history.
  window.addEventListener("hashchange", () => {
    const fragment = window.location.hash;
    if (readLinkFromFragment(fragment) === null) return;
    if (!hasUnsavedPageInput()) {
      window.location.reload();
      return;
    }
    history.replaceState(null, "", window.location.pathname + window.location.search);
    pendingLinkFragment = fragment;
    statusEl.textContent = "You opened a new Surla link. Save or discard your edits, or wait for the save to finish, then choose Open link.";
    syncNoticeRegion();
    document.getElementById("surla-pending-link")?.scrollIntoView({ block: "nearest" });
  });

  syncAllRegions();
  void start();
}

bootstrapViewsPage();
