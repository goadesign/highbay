// build.tsx presents the saved build and explicit start controls beside a
// Change Card. The page owns requests, revision correlation and receipts.

import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ChangeBuild, ChangeCardResult, OpenBuildRefusal, ReplacedBuild, StartBuildResult, StartedBuild, VerificationStatusResult } from "../types";
import { Alert, Badge, Button, Card } from "./shadcn";
import { TestWriterFacts } from "./build-test-review";

// BuildQuestion binds a read or confirmation to the owner observation it describes.
type BuildQuestion = { kind: "reading" | "rebuild" | "drop"; identity: string };

// BuildStartReceipt records one start's team answer, rather than current health.
// A later Start agents action supersedes this receipt.
export interface BuildStartReceipt {
  at: string;
  team: StartBuildResult["team"];
}

// BuildSectionProps keeps transport, saved-card and findings ownership in the
// page. Null reads are unread; only the generated not_started case means absent.
export interface BuildSectionProps {
  card: ChangeCardResult;
  currentRevision: string | null;
  build: ChangeBuild["build"] | null;
  draftedBeforeDrop: boolean | null;
  loading: boolean;
  error: string | null;
  // actionError retains a failed start separately from saved-build read errors.
  actionError: string | null;
  starting: boolean;
  dropping: boolean;
  openBuildRefusal: OpenBuildRefusal | null;
  disabled: boolean;
  dirty: boolean;
  startReceipt: BuildStartReceipt | null;
  findingTexts: readonly string[] | null;
  findingsLoading: boolean;
  findingsError: string | null;
  // check is the owner's exact latest saved run for this change.
  check: VerificationStatusResult | null;
  checkKnown: boolean;
  checkError: string | null;
  approvalAvailable: boolean;
  onRefresh: () => void;
  onReadFindings: () => void;
  onPrepareBuild: (ready: (build: ChangeBuild | null) => void) => void;
  onBuild: (replaces?: ReplacedBuild) => void;
  onDrop: (build: ReplacedBuild) => void;
  onOpenTeam: () => void;
  onOpenChange: (changeId: string) => void;
  onReviewCheck: () => void;
}

// BuildSectionView shows the owner's saved facts without starting work on a
// refresh. Replacing a current build requires a separate explicit confirmation.
export function BuildSectionView({
  card,
  currentRevision,
  build,
  draftedBeforeDrop,
  loading,
  error,
  actionError,
  starting,
  dropping,
  openBuildRefusal,
  disabled,
  dirty,
  startReceipt,
  findingTexts,
  findingsLoading,
  findingsError,
  check,
  checkKnown,
  checkError,
  approvalAvailable,
  onRefresh,
  onReadFindings,
  onBuild,
  onPrepareBuild,
  onDrop,
  onOpenTeam,
  onOpenChange,
  onReviewCheck,
}: BuildSectionProps) {
  const saved = build !== null && build.state === "started" ? build : null;
  const open = saved !== null && (saved.taking.state === "building" || saved.taking.state === "passed");
  const current = saved !== null && saved.in_force;
  const replay = current && saved.taking.state === "merged";
  const requiresConfirmation = (current && open) || saved?.taking.state === "building" || replay;
  const displayedCurrent = currentRevision === card.request_revision &&
    (!current || saved.request_revision === card.request_revision);
  const confirmed = card.status === "confirmed";
  const eligible = confirmed && displayedCurrent && build !== null && error === null && draftedBeforeDrop === false;
  const blockingBuild = openBuildRefusal !== null && openBuildRefusal.change_id !== card.change_id
    ? openBuildRefusal : null;
  const blocked = disabled || starting || dropping || loading || !eligible || blockingBuild !== null;
  const otherPrimary = !confirmed || !displayedCurrent || dirty || card.proposed_revision !== undefined;
  const report = check?.state.type === "completed" ? check.state.value.report : null;
  const matchedCheck = checkKnown && check?.inputs_current === true &&
    check.change_id === card.change_id && check.request_revision === card.request_revision;
  const recordedCheck = report !== null && card.approval?.report_digest === report.report_digest;
  const sentBack = open && current && saved.test_writer.tests.state === "needs_changes";
  const reviewPrimary = open && approvalAvailable && !otherPrimary && !sentBack && matchedCheck &&
    report?.passed === true && !recordedCheck;
  // identity binds the question to the exact subject and start it described.
  const identity = JSON.stringify([card.change_id, card.request_revision, saved?.request_revision ?? null, saved?.started_at ?? null, saved?.taking.state ?? null, draftedBeforeDrop]);
  const [question, setQuestion] = useState<BuildQuestion | null>(null);
  const confirming = question?.kind === "rebuild" && question.identity === identity && requiresConfirmation && eligible && !disabled && !starting && !dropping && blockingBuild === null;
  const questionBusy = question !== null;
  const dropTarget = blockingBuild?.build ?? saved;
  const dropChange = blockingBuild?.change_id ?? card.change_id;
  const dropIdentity = dropTarget === null ? null : JSON.stringify([
    card.change_id, card.request_revision, dropChange, dropTarget.request_revision,
    dropTarget.started_at, dropTarget.taking.state,
  ]);
  const headingRef = useRef<HTMLHeadingElement>(null);
  const triggerRef = useRef<HTMLSpanElement>(null);
  const questionRef = useRef<HTMLDivElement | null>(null);
  const removedFocus = useRef(false);
  // A cancelled question owns its focus return only while its subject and
  // the user's focus stay here; a pending read may temporarily block it.
  const returnFocus = useRef<string | null>(null);
  // A whole taking panel can disappear when a stale Drop names another build.
  const removedTakingFocus = useRef(false);
  const takingFocusRemoved = useCallback(() => { removedTakingFocus.current = true; }, []);
  // sent prevents a second click before the page's pending state is rendered.
  const sent = useRef(false);
  const reading = useRef(false);

  // attachQuestion observes focus before removal, following the Live control
  // pattern. Refreshes never pull focus away from an editor outside this row.
  const attachQuestion = useCallback((node: HTMLDivElement | null) => {
    questionRef.current = node;
    if (node === null) {
      return;
    }
    return () => {
      removedFocus.current = node.contains(document.activeElement);
      questionRef.current = null;
    };
  }, []);

  useLayoutEffect(() => {
    if (confirming) {
      removedFocus.current = false;
      questionRef.current?.focus();
    } else if (removedFocus.current && returnFocus.current === null) {
      removedFocus.current = false;
      headingRef.current?.focus();
    }
  }, [confirming, identity]);

  // A refresh may disable the trigger when Escape removes the question.
  // Keep focus on the heading until that same trigger is usable; a later
  // render must not reclaim focus the user deliberately moved elsewhere.
  useLayoutEffect(() => {
    const cancelled = returnFocus.current;
    if (cancelled === null || confirming) return;
    const ownsFocus = removedFocus.current || document.activeElement === headingRef.current;
    removedFocus.current = false;
    if (!ownsFocus) {
      returnFocus.current = null;
      return;
    }
    const trigger = triggerRef.current?.querySelector("button") ?? null;
    if (cancelled !== identity || trigger === null || !eligible) {
      returnFocus.current = null;
      headingRef.current?.focus();
    } else if (!trigger.disabled) {
      returnFocus.current = null;
      trigger.focus();
    } else {
      headingRef.current?.focus();
    }
  });

  // Only whole-panel removal needs an unconditional fallback. An unchanged
  // rebuild question never takes focus during an unrelated background render.
  useLayoutEffect(() => {
    if (removedTakingFocus.current) {
      removedTakingFocus.current = false;
      headingRef.current?.focus();
    }
  });

  // Once invalidated, the old question cannot return when a session/revision
  // becomes eligible again. An outstanding request owns the submission latch.
  useEffect(() => {
    if (question === null) { reading.current = false; return; }
    if (disabled || starting || dropping || (question.kind !== "drop" &&
        (question.identity !== identity || !eligible || blockingBuild !== null)) ||
        (question.kind === "drop" && question.identity !== dropIdentity)) {
      setQuestion(null);
    }
  }, [question, identity, dropIdentity, eligible, disabled, starting, dropping, blockingBuild]);
  useEffect(() => {
    if (!starting) {
      sent.current = false;
    }
  }, [starting, identity]);

  // cancel sends nothing and restores the separate question trigger.
  function cancel() {
    returnFocus.current = identity;
    setQuestion(null);
  }

  // start is called only by the direct first/earlier action or final confirm.
  function start() {
    if (blocked || sent.current || (requiresConfirmation && !confirming)) {
      return;
    }
    sent.current = true;
    setQuestion(null);
    onBuild(saved?.taking.state === "building"
      ? { change_id: card.change_id, started_at: saved.started_at }
      : undefined);
  }

  // ask reads the owner again before describing the build that will be replaced.
  function ask() {
    if (blocked || questionBusy || reading.current) return;
    reading.current = true;
    setQuestion({ kind: "reading", identity });
    onPrepareBuild((fresh) => {
      if (fresh === null) {
        setQuestion((previous) => previous?.kind === "reading" && previous.identity === identity ? null : previous);
        return;
      }
      const freshIdentity = JSON.stringify([
        card.change_id, card.request_revision, fresh.build.state === "started" ? fresh.build.request_revision : null,
        fresh.build.state === "started" ? fresh.build.started_at : null, fresh.build.state === "started" ? fresh.build.taking.state : null,
        fresh.drafted_before_drop,
      ]);
      setQuestion((previous) => previous?.kind === "reading" && previous.identity === identity
        ? freshIdentity === identity ? { kind: "rebuild", identity } : null : previous);
    });
  }

  if (!confirmed && saved === null) {
    return null;
  }

  const directLabel = card.proposed_revision !== undefined
    ? "Build agreed revision"
    : saved !== null ? "Build current agreed revision" : "Build this change";
  const showDirect = (!current || !open) && !replay && draftedBeforeDrop === false && confirmed && displayedCurrent && build !== null;
  const executionExplanation = saved?.test_writer.tests.state === "not_used"
    ? "From Build on, those tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges."
    : "From Build on, the tests and the builder’s code run on this machine as ordinary programs, with your access to files and the network; nothing confines them. Approval decides only what merges.";
  const startExplanation = (saved?.test_writer.tests.state === "not_used"
    ? "Build starts the builder. This project uses its configured tests. "
    : saved !== null
      ? "Build starts the builder and the test writer. Surla uses the test writer’s accepted tests to check the builder’s work and to start live targets. "
      : "Build starts the builder. If this project needs a test writer, it starts that writer too. Surla uses that writer’s accepted tests to check the builder’s work and to start live targets. ") + executionExplanation;

  const roleFacts = saved !== null ? <ul className="surla-build-roles">
    <li id="surla-build-builder">Builder: {saved.builder.read
      ? "assignment read"
      : saved.test_writer.tests.state === "accepted"
        ? "assignment unread; accepted tests are ready to read"
        : "assignment unread"}</li>
    <li><TestWriterFacts build={saved} findingTexts={findingTexts} loading={findingsLoading}
      error={findingsError} disabled={disabled || starting} onRead={onReadFindings} /></li>
  </ul> : null;
  const progress = <>
    {saved !== null && saved.taking.state !== "building" ? <BuildProgress build={saved} /> :
      check !== null && !sentBack ? (
        <p id="surla-build-state" data-inputs-current={checkKnown ? String(check.inputs_current) : "unknown"}>
          {!checkKnown
            ? "A saved check is available. Its current agreement and tests have not been confirmed."
            : !matchedCheck
            ? "The saved check used an earlier agreement or test selection. The builder checks the current agreement and tests again; then you can approve."
            : check.state.type === "running"
            ? "Surla is checking the builder’s code. Your next decision is approval after the check passes."
            : report?.passed
            ? recordedCheck
              ? "The check passed and your approval is recorded."
              : `The check passed: ${report.counts.passed} of ${report.counts.required} scenarios. Review the result.`
            : check.state.type === "not_completed"
            ? "The check could not finish. The builder reads the result and addresses its next step."
            : "The check did not pass. The builder reviews the result before asking for another check."}
          {" Check started "}<time dateTime={check.accepted_at}>{localBuildTime(check.accepted_at)}</time>.
        </p>
      ) : saved !== null ? <BuildProgress build={saved} /> :
        build?.state === "not_started" ? <p id="surla-build-state">Not built yet.</p> : null}
    {saved?.taking.state === "building" ? roleFacts : null}
  </>;
  const primaryBuild = showDirect ? <span ref={triggerRef}>
      <Button id="surla-build-start" variant={otherPrimary || sentBack || reviewPrimary ? "default" : "primary"}
        disabled={blocked || questionBusy} aria-describedby={blockingBuild !== null ? "surla-build-start-explanation surla-build-action-error" : "surla-build-start-explanation"}
        onClick={requiresConfirmation ? ask : start}>{directLabel}</Button>
    </span> : null;
  const actions = <>
    {blockingBuild !== null || saved === null ? primaryBuild : null}
    {check !== null ? <Button id="surla-build-review-check" variant={reviewPrimary ? "primary" : "default"}
      disabled={disabled || starting} onClick={onReviewCheck}>
      {reviewPrimary ? "Review result" : "Review saved check"}
    </Button> : null}
    <Button id="surla-build-view-team" disabled={disabled || starting} onClick={onOpenTeam}>View team</Button>
    {current && (open || replay) && draftedBeforeDrop === false && confirmed && displayedCurrent ? <span ref={triggerRef}>
      <Button id="surla-build-again" disabled={blocked || questionBusy}
        aria-describedby={blockingBuild !== null ? "surla-build-action-error" : undefined}
        onClick={ask}>Build again…</Button>
    </span> : null}
    {blockingBuild !== null ? <Button id="surla-build-open-blocking-change" disabled={disabled || starting}
      onClick={() => onOpenChange(blockingBuild.change_id)}>Open change {blockingBuild.change_id}</Button> : null}
  </>;
  const explanation = showDirect
    ? <p id="surla-build-start-explanation">{saved === null ? startExplanation : executionExplanation}</p> : null;
  const header = <>
    <h3 id="surla-build-title" tabIndex={-1} ref={headingRef}>Build</h3>
    {saved !== null ? <div className="surla-build-badges">
      <Badge id="surla-build-taking-state" variant={saved.taking.state === "building" ? "accent" :
        saved.taking.state === "passed" || saved.taking.state === "merged" ? "success" : "default"}>
        {saved.taking.state === "building" ? "Building" : saved.taking.state === "passed" ? "Passed" :
          saved.taking.state === "merged" ? "Merged" : "Dropped"}
      </Badge>
      <Badge variant={current ? "accent" : "default"}>{current ? "Agreed revision" : "Earlier revision"}</Badge>
    </div> : null}
  </>;
  const observations = <>
    {error !== null ? <Alert id="surla-build-error" variant="destructive" role="alert">
      <strong>Could not read the build. </strong>{error}
      <div className="surla-field-row"><Button id="surla-build-refresh"
        disabled={disabled || starting || loading} onClick={onRefresh}>Retry build read</Button></div>
    </Alert> : null}
    {loading ? <p id="surla-build-loading" role="status">
      {build === null ? "Reading this change’s build…" : "Updating build…"}
    </p> : null}
    {build === null && !loading && error === null ? <p>This change’s build has not been read.</p> : null}
    {build !== null && error !== null ? <p>Last-read build facts; they could not be refreshed.</p> : null}
    {actionError !== null && blockingBuild === null ? <Alert id="surla-build-action-error" variant="destructive" role="alert">{actionError}</Alert> : null}
    {checkError !== null ? <p id="surla-build-check-error">Could not refresh the saved check. {checkError}</p> : null}
  </>;

  return (
    <Card
      id="surla-build-section"
      className="surla-card surla-live-wrap"
      role="region"
      aria-labelledby="surla-build-title"
      aria-busy={loading || starting}
      data-state={build?.state ?? "unread"}
    >
      {saved === null || blockingBuild !== null ? <div className="surla-region-header surla-build-header">{header}</div> : null}
      {blockingBuild !== null ? (
        <>{observations}
        <section id="surla-build-blocking-change" aria-label="Open build blocking this change">
          <BuildTakingView changeId={blockingBuild.change_id} build={blockingBuild.build}
            disabled={disabled || starting} dropping={dropping} onDrop={onDrop}
            question={question} setQuestion={setQuestion} questionBusy={questionBusy} identity={dropIdentity}
            onFocusedControlRemoved={takingFocusRemoved} actions={actions} header={null}
            dropLabel={"Drop " + blockingBuild.change_id + "’s build…"}
            progress={<Alert id="surla-build-action-error" variant="destructive" role="alert">
              {blockingBuild.name === "previous_change_open"
                ? <>Build did not start: change {blockingBuild.change_id} has an open build. {blockingBuild.build.taking.state === "building"
                  ? "Nothing of it has passed its check yet: let its builder finish, or drop it."
                  : blockingBuild.build.taking.state === "passed"
                    ? "Its builder’s commit passed the accepted tests: merge it, or drop it."
                    : "Review its current state before building this change."}</>
                : actionError ?? blockingBuild.message}
            </Alert>}>{explanation}</BuildTakingView>
        </section>
        </>
      ) : null}
      {saved !== null && blockingBuild === null ? <BuildTakingView changeId={card.change_id} build={saved}
        disabled={disabled || starting || error !== null} dropping={dropping} onDrop={onDrop}
        question={question} setQuestion={setQuestion} questionBusy={questionBusy} identity={dropIdentity}
        onFocusedControlRemoved={takingFocusRemoved} progress={<>{observations}{progress}</>}
        actions={actions} header={header} dropLabel="Drop…">
        {explanation}{showDirect ? <div className="surla-field-row">{primaryBuild}</div> : null}
      </BuildTakingView> : null}
      {saved === null && blockingBuild === null ? <>
        {observations}{progress}{explanation}<div className="surla-field-row surla-build-actions">{actions}</div>
      </> : null}
      {draftedBeforeDrop === true ? <p id="surla-build-enabling-step">
        This agreement was drafted before a build was dropped; ask the guide to refresh its draft with <code>refresh_story</code>
        {" and propose a revision from that refresh with "}<code>revise_change</code>, then adopt that revision before building.
        {" A proposal already waiting may predate the drop; adopting it alone may not enable Build."}
      </p> : null}
      {!confirmed ? <p>Confirm the change before building it.</p> : !displayedCurrent ? (
        <p>A newer agreed revision is available. Review it before starting a build.</p>
      ) : dirty ? (
        <p>The build uses the agreed revision; refine the change first to include your edits.</p>
      ) : null}
      {starting ? (
        <p id="surla-build-starting" role="status">
          Starting build… This can take a few minutes and continues if you close the page.
        </p>
      ) : null}
      {confirming ? (
        <div
          id="surla-build-question"
          ref={attachQuestion}
          role="group"
          aria-labelledby="surla-build-question-title"
          tabIndex={-1}
          onKeyDown={(event) => {
            if (event.key === "Escape") {
              event.preventDefault();
              cancel();
            }
          }}
        >
          <Alert>
            <p id="surla-build-question-title"><strong>{current
              ? "Build the agreed revision again?"
              : "Replace the earlier build with the agreed revision?"}</strong></p>
            <p>
              {replay
                ? saved?.test_writer.tests.state === "not_used"
                  ? "This starts a new build number from your branch as it is now, for the same agreed revision, with a fresh unread builder assignment and this project’s configured tests. Earlier reports do not establish that the new build passed."
                  : "This starts a new build number from your branch as it is now, for the same agreed revision, with fresh unread builder and test writer assignments. The test writer must report its tests and have them accepted again. Earlier reports do not establish that the new build passed."
                : saved?.test_writer.tests.state === "not_used"
                ? "Building again replaces this build and makes the builder’s assignment unread again."
                : "Building again replaces this build, makes both assignments unread, sets the test writer’s work to Assigned, clears its report and verdict, and requires its tests to be reported, judged and accepted again before verification or a new live target."}
            </p>
            <p id="surla-build-confirm-explanation">{startExplanation}</p>
            <div className="surla-field-row">
              <Button id="surla-build-confirm" disabled={blocked} onClick={start}>Build again</Button>
              <Button id="surla-build-cancel" disabled={disabled || starting} onClick={cancel}>Cancel</Button>
            </div>
          </Alert>
        </div>
      ) : null}
      <BuildDetails card={card} build={saved}>{saved !== null && saved.taking.state !== "building" ? roleFacts : null}</BuildDetails>
      {startReceipt !== null ? <StartReceipts receipt={startReceipt} /> : null}
    </Card>
  );
}

// BuildTakingView renders owner-reported taking facts and the section-owned
// Drop question, which binds consent to the exact observed build.
export function BuildTakingView({ changeId, build, disabled, dropping, onDrop, onFocusedControlRemoved, progress, actions, header, dropLabel, children, question, setQuestion, questionBusy, identity }: {
  changeId: string;
  build: StartedBuild;
  disabled: boolean;
  dropping: boolean;
  onDrop: (build: ReplacedBuild) => void;
  onFocusedControlRemoved: () => void;
  progress: React.ReactNode;
  actions: React.ReactNode;
  header: React.ReactNode;
  dropLabel: string;
  children: React.ReactNode;
  question: BuildQuestion | null;
  setQuestion: React.Dispatch<React.SetStateAction<BuildQuestion | null>>;
  questionBusy: boolean;
  identity: string | null;
}) {
  const taking = build.taking;
  const open = taking.state === "building" || taking.state === "passed";
  const confirming = question?.kind === "drop" && question.identity === identity && open && !disabled && !dropping;
  const trigger = useRef<HTMLSpanElement>(null);
  const heading = useRef<HTMLDivElement>(null);
  const questionNode = useRef<HTMLDivElement | null>(null);
  const lostFocus = useRef(false);
  const canceled = useRef(false);
  const sent = useRef(false);
  // Whole-panel removal needs a heading outside this subtree. Unrelated editor
  // focus remains untouched, including when a background read removes a panel.
  const attachTaking = useCallback((node: HTMLDivElement | null) => {
    heading.current = node;
    if (node === null) return;
    return () => {
      if (node.contains(document.activeElement)) onFocusedControlRemoved();
      heading.current = null;
    };
  }, [onFocusedControlRemoved]);
  const attach = useCallback((node: HTMLDivElement | null) => {
    questionNode.current = node;
    if (node === null) return;
    return () => {
      lostFocus.current = node.contains(document.activeElement);
      questionNode.current = null;
    };
  }, []);
  useLayoutEffect(() => {
    if (confirming) questionNode.current?.focus();
    else if (canceled.current) {
      canceled.current = false;
      lostFocus.current = false;
      trigger.current?.querySelector("button")?.focus();
    } else if (lostFocus.current) {
      lostFocus.current = false;
      heading.current?.focus();
    }
  }, [confirming, identity]);
  useEffect(() => {
    if (!confirming && question?.kind === "drop") setQuestion(null);
  }, [confirming, question, setQuestion]);
  useEffect(() => { if (!dropping) sent.current = false; }, [dropping, identity]);
  function cancel() {
    canceled.current = true;
    setQuestion(null);
  }
  const actionRow = <div className="surla-field-row surla-build-actions">{actions}
    {open ? <span ref={trigger}><Button id="surla-build-drop" disabled={disabled || dropping || questionBusy}
      onClick={() => { if (identity !== null) setQuestion({ kind: "drop", identity }); }}>{dropLabel}</Button></span> : null}
  </div>;
  return (
    <div id="surla-build-taking" data-state={taking.state} ref={attachTaking} tabIndex={-1}>
      {header !== null ? <div className="surla-region-header surla-build-header">{header}{actionRow}</div> : null}
      {progress}
      {taking.state === "passed" ? <BuildMergeInstructions taking={taking}
        candidateCommit={taking.candidate_commit}
        testsCommit={taking.tests.source === "written_tests" ? taking.tests.commit : null}
        onFocusedControlRemoved={onFocusedControlRemoved} /> : null}
      {taking.state === "merged" ? <p>It is in your branch. {build.test_writer.tests.state === "not_used"
        ? "The passed run checked the builder's commit; Surla did not check the merged result, which also includes anything else merged into your branch since this build started."
        : "The passed run checked the builder's commit with the accepted tests; Surla did not run the tests on the merged result, which also includes anything else merged into your branch since this build started."} {build.in_force ? "Build another change or refine this one when there is more to do." : "Build the current agreed revision to implement the new agreement."} No role has work on this build.</p> : null}
      {taking.state === "dropped" ? <>
        {taking.kept_branches.length > 0 ? <ul>{taking.kept_branches.map((branch) => <li key={branch}><code>{branch}</code> — <code>git log {branch}</code> shows it.</li>)}</ul> : null}
        <p>Surla builds nothing on it, and it no longer blocks another build. No role has work on this build.</p>
      </> : null}
      {children}
      {header === null ? actionRow : null}
      {dropping ? <p role="status">Dropping build…</p> : null}
      {confirming ? <div id="surla-build-drop-question" role="group" aria-label={"Drop change " + changeId + "?"}
        tabIndex={-1} ref={attach} onKeyDown={(event) => {
          if (event.key === "Escape") { event.preventDefault(); cancel(); }
        }}>
        <Alert>
          <p><strong>Drop change {changeId}?</strong></p>
          <p>Surla will not build on it. Its commits stay on this build’s branches, your branch is unchanged, and the guide’s draft starts again from your branch; edits it has not proposed are lost.</p>
          <div className="surla-field-row">
            <Button id="surla-build-drop-confirm" disabled={disabled || dropping} onClick={() => {
              if (sent.current || !confirming) return;
              sent.current = true;
              onFocusedControlRemoved();
              setQuestion(null);
              onDrop({ change_id: changeId, started_at: build.started_at });
            }}>Drop change</Button>
            <Button id="surla-build-drop-cancel" onClick={cancel}>{taking.state === "passed" ? "Keep this build" : "Keep building"}</Button>
          </div>
        </Alert>
      </div> : null}
    </div>
  );
}

// BuildMergeInstructions names the passed commits, never movable branch tips.
// The branch labels describe where those commits were verified.
export function BuildMergeInstructions({ taking, candidateCommit, testsCommit, onFocusedControlRemoved }: {
  taking: Extract<StartedBuild["taking"], { state: "passed" }>;
  candidateCommit: string;
  testsCommit: string | null;
  onFocusedControlRemoved: () => void;
}) {
  // The owner supplies its stable fallback when a focused scrollport is
  // removed. Unfocused command subtrees never reclaim another control's focus.
  const attachCommands = useCallback((node: HTMLPreElement | null) => {
    if (node === null) return;
    return () => {
      if (node.contains(document.activeElement)) onFocusedControlRemoved();
    };
  }, [onFocusedControlRemoved]);
  return <div className="surla-build-merge">
    <h4>To take this change, run in the project folder:</h4>
    <ol>
      <li><pre ref={attachCommands} tabIndex={0} role="region" aria-label="Merge the builder’s commit"><code>git merge {candidateCommit}</code></pre>
        <p>The builder’s commit, verified on <code>{taking.candidate_branch}</code>.</p></li>
      {testsCommit !== null && taking.tests.source === "written_tests" ? <li>
        <pre ref={attachCommands} tabIndex={0} role="region" aria-label="Merge the tests’ commit"><code>git merge {testsCommit}</code></pre>
        <p>Then the tests’ commit, verified on <code>{taking.tests.branch}</code>.</p></li> : null}
    </ol>
    <p>Build another change once you have, or drop this one. Surla does not merge or deploy anything.</p>
    {!taking.base_in_branch ? <Alert variant="warning">
      Your branch no longer holds the commit where this build started (its history was rewritten), so these merges bring that history back. To start from your branch as it is, drop this change and build it again.
    </Alert> : null}
  </div>;
}

// runBelongsToBuild mirrors the owner's eligibleBuildRuns predicate. A newer
// pass does not disqualify an older pass from this same saved start.
export function runBelongsToBuild(changeId: string, build: StartedBuild, run: VerificationStatusResult): boolean {
  return run.change_id === changeId && run.request_revision === build.request_revision &&
    run.state.type === "completed" && run.state.value.report.passed &&
    buildTimestampNanos(run.accepted_at) >= buildTimestampNanos(build.started_at);
}

// buildTimestampNanos preserves the owner's nanosecond boundary. Date.parse
// supplies the epoch milliseconds; the validated RFC3339 fraction supplies the
// remaining six digits that JavaScript's Date would otherwise discard.
function buildTimestampNanos(at: string): bigint {
  const fraction = at.match(/\.(\d{1,9})/)?.[1] ?? "";
  return BigInt(Date.parse(at)) * 1_000_000n + BigInt(fraction.padEnd(9, "0").slice(3));
}

// SavedResultCommits keeps historical verification and approval evidence bound
// to its own inputs. Only an eligible run of the open build adds guidance.
export function SavedResultCommits({ changeId, candidateCommit, testsCommit, build, run, onFocusedControlRemoved }: {
  changeId: string;
  candidateCommit: string;
  testsCommit: string | null;
  build: StartedBuild | null;
  run: VerificationStatusResult | null;
  onFocusedControlRemoved: () => void;
}) {
  const taking = build?.taking;
  const runMatchesBuild = run !== null && build !== null &&
    runBelongsToBuild(changeId, build, run) && run.candidate_commit === candidateCommit &&
    (run.test_source.source === "written_tests" ? run.suite_commit === testsCommit : testsCommit === null);
  if (taking?.state === "passed" && runMatchesBuild) {
    return <BuildMergeInstructions taking={taking} candidateCommit={candidateCommit} testsCommit={testsCommit}
      onFocusedControlRemoved={onFocusedControlRemoved} />;
  }
  if (taking?.state === "merged" && runMatchesBuild) {
    return <p>
      Merged into your branch. This run verified the builder's commit <code>{candidateCommit}</code>
      {testsCommit !== null ? <>
        {" with the tests' commit "}<code>{testsCommit}</code>
        {". Merging them creates a merge commit that combines the builder's changes outside "}<code>eval/</code>
        {" with the tests in "}<code>eval/</code>
        {". Surla did not run the tests on that merge commit, or on anything else merged into your branch since this build started."}
      </> : ". Surla did not check anything else merged into your branch since this build started."}
    </p>;
  }
  return <p>
    This saved result names the builder’s commit <code>{candidateCommit}</code>
    {testsCommit !== null ? <> and the tests’ commit <code>{testsCommit}</code></> : null}.
    {" Review the current build’s taking state before merging."}
  </p>;
}

// BuildProgress describes the saved step without treating a read as proof of
// work, or an earlier revision's tests as progress on the current agreement.
function BuildProgress({ build }: { build: StartedBuild }) {
  switch (build.taking.state) {
    case "passed": return <p id="surla-build-state">The builder’s commit passed the accepted tests.</p>;
    case "merged": return <p id="surla-build-state">Merged.</p>;
    case "dropped": return <p id="surla-build-state">{build.taking.kept_branches.length === 0
      ? "Dropped. Nothing was committed for it." : "Dropped. Its work stays on:"}</p>;
  }
  const tests = build.test_writer.tests;
  if (!build.in_force) {
    return <p id="surla-build-state">Earlier build, started <time dateTime={build.started_at}>{localBuildTime(build.started_at)}</time>. The agreed revision has changed.</p>;
  }
  switch (tests.state) {
    case "needs_changes":
      return null;
    case "accepted":
      return <p id="surla-build-state">Tests accepted. The builder finishes the code and asks Surla to check it. Your next decision: approve, after the check passes.</p>;
    case "reported":
      return <p id="surla-build-state">The test writer reported its tests. Surla checks them before the builder can use them.</p>;
    case "could_not_run":
      return <p id="surla-build-state">Surla could not check the test writer’s tests. Read the reason and next step below.</p>;
    case "assigned":
    case "not_used":
      return <p id="surla-build-state">Building since <time dateTime={build.started_at}>{localBuildTime(build.started_at)}</time>. {tests.state === "not_used" ? "The builder uses this project’s configured tests." : "Next: the test writer prepares tests for the builder’s code."} Your next decision: approve, after the builder’s check passes.</p>;
  }
}

// localBuildTime shows local minutes, adding the date for starts before today.
// Exact timestamps remain available in Build details.
function localBuildTime(at: string) {
  const date = new Date(at);
  return date.toLocaleString(undefined, {
    ...(date.toDateString() === new Date().toDateString() ? {} : {
      year: "numeric", month: "short", day: "numeric",
    }),
    hour: "numeric",
    minute: "2-digit",
  });
}

// BuildDetails places exact revision, report and verdict evidence behind the
// existing native disclosure, without relabeling acceptance as verification.
function BuildDetails({ card, build, children }: { card: ChangeCardResult; build: StartedBuild | null; children: React.ReactNode }) {
  const tests = build?.test_writer.tests;
  return (
    <details id="surla-build-details" className="surla-details">
      <summary>Build details</summary>
      {children}
      <p>Agreed revision: <code>{card.request_revision}</code></p>
      {build !== null ? (
        <>
          <p>Built revision: <code>{build.request_revision}</code></p>
          <p>Build started: <time dateTime={build.started_at}>{build.started_at}</time></p>
          {build.required_tests.length > 0 ? <dl id="surla-build-test-hooks">
            {build.required_tests.map((test) => <React.Fragment key={test.scenario}>
              <dt>Scenario <code>{test.scenario}</code></dt><dd>Hook <code>{test.hook}</code></dd>
            </React.Fragment>)}
          </dl> : null}
          <p>{build.required_test_count === 0 ? "No tests of this change’s Story were required."
            : build.required_test_count + " required Story " + (build.required_test_count === 1 ? "test." : "tests.")}</p>
          {tests?.state !== "not_used" ? <p>Test report number: {build.test_writer.report}</p> : null}
        </>
      ) : null}
      {tests !== undefined && "checked_at" in tests ? (
        <p>Verdict reached: <time dateTime={tests.checked_at}>{tests.checked_at}</time></p>
      ) : null}
      {tests?.state === "accepted" ? <p>Accepted tests commit: <code>{tests.commit}</code></p> : null}
    </details>
  );
}

// StartReceipts describes a historical launcher outcome in the past tense.
// Owner instructions remain quotations returned then, never current advice.
function StartReceipts({ receipt }: { receipt: BuildStartReceipt }) {
  const team = receipt.team;
  let teamText = "";
  switch (team.state) {
    case "ready": teamText = "the team was ready"; break;
    case "window_busy": teamText = "the build was saved and the team window was busy"; break;
    case "ambiguous_space": teamText = team.count + " matching team spaces"; break;
    case "unavailable": teamText = "the build was saved and team startup was unavailable"; break;
  }
  return (
    <div id="surla-build-start-receipts">
      <p id="surla-build-start-team-receipt">
        This start (begun <time dateTime={receipt.at}>{receipt.at}</time>) found {teamText}.
        {" "}The instruction returned then was: «{team.step}»
      </p>
    </div>
  );
}
