// Package components provides the Change Card decision and approval editor,
// combining the OpenUI Decision Workspace with the always-available canonical
// shadcn/ui inspector over a single shared draft context.

import React, { useEffect, useRef } from "react";
import { flushSync } from "react-dom";
import {
  areExamplesModified,
  CP1DraftContext,
  CP1DraftContextValue,
  isDraftDirty,
  PreservedDraft,
} from "../context/cp1-draft";
import {
  Renderer,
  highbayDecisionLibrary,
  validateCompletedOpenUI,
} from "../openui/library";
import { ChangeCardResult, ExampleItem } from "../types";
import { Alert, Badge, Button, Card, CardFooter, ChoiceGroup, Label, Separator, Textarea } from "./shadcn";
import { countsPhrase, storyCounts } from "./story";

export interface CP1EditorProps {
  card: ChangeCardResult;
  // buildSection leads a confirmed card so the current work appears first.
  buildSection: React.ReactNode;
  // buildRevisionNotice explains the effect of changing the agreed revision.
  buildRevisionNotice: string | null;
  // proposalWaiting is true while a proposed revision waits beside the
  // agreed card; refining the agreed card is then refused, so it is offered
  // only after the user adopts or declines the proposal.
  proposalWaiting: boolean;
  priorCard: ChangeCardResult | null;
  draftAnswers: Record<string, string>;
  draftExamples: ExampleItem[];
  rpcInFlight: boolean;
  loadingVerifications: boolean;
  cp4Open: boolean;
  preservedDraft: PreservedDraft | null;
  formError: string | null;
  // revisionAid explains and resolves a choice the draft does not implement.
  revisionAid: React.ReactNode | null;
  onSetAnswer: (ambiguityId: string, option: string) => void;
  onSetExampleField: (
    exampleId: string,
    field: "scenario" | "input_summary" | "expected_outcome" | "verification_kind",
    value: string
  ) => void;
  onSubmitConfirm: () => void;
  onDiscardDraft: () => void;
  onLoadLatestCard: (() => void) | null;
  onLoadVerifications: () => void;
  // onOpenStory shows the Story stage for this change.
  onOpenStory: () => void;
  // onReviewApproval opens the saved record in the approval stage.
  onReviewApproval: () => void;
}

interface FieldTextareaProps {
  exampleId: string;
  field: "scenario" | "input_summary" | "expected_outcome";
  label: string;
  value: string;
  disabled: boolean;
  onChangeValue: (next: string) => void;
}

// FieldTextarea renders a controlled multiline textarea that preserves exact
// whitespace and newlines while rejecting mutations when an RPC is in flight.
function FieldTextarea({
  exampleId,
  field,
  label,
  value,
  disabled,
  onChangeValue,
}: FieldTextareaProps) {
  const elRef = useRef<HTMLTextAreaElement | null>(null);
  const valueRef = useRef(value);
  const disabledRef = useRef(disabled);
  const onChangeRef = useRef(onChangeValue);
  valueRef.current = value;
  disabledRef.current = disabled;
  onChangeRef.current = onChangeValue;

  useEffect(() => {
    const node = elRef.current;
    if (!node) {
      return;
    }
    const handleInput = () => {
      if (disabledRef.current) {
        node.value = valueRef.current;
        return;
      }
      if (node.value !== valueRef.current) {
        flushSync(() => {
          onChangeRef.current(node.value);
        });
      }
    };
    node.addEventListener("input", handleInput);
    return () => {
      node.removeEventListener("input", handleInput);
    };
  }, []);

  useEffect(() => {
    if (elRef.current && elRef.current.value !== value) {
      elRef.current.value = value;
    }
  }, [value]);

  return (
    <Label className="highbay-field-label">
      <span>{label}</span>
      <Textarea
        ref={elRef}
        rows={3}
        className="highbay-input"
        data-example-id={exampleId}
        data-field={field}
        disabled={disabled}
        value={value}
        onChange={(e) => {
          if (disabled) {
            e.target.value = value;
            return;
          }
          onChangeValue(e.target.value);
        }}
      />
    </Label>
  );
}

// CP1EditorView renders the active CP1 Change Card, its OpenUI Decision Studio
// workspace (when present), and the canonical shadcn/ui CP1 inspector.
export function CP1EditorView({
  card,
  buildSection,
  buildRevisionNotice,
  proposalWaiting,
  priorCard,
  draftAnswers,
  draftExamples,
  rpcInFlight,
  loadingVerifications,
  cp4Open,
  preservedDraft,
  formError,
  revisionAid,
  onSetAnswer,
  onSetExampleField,
  onSubmitConfirm,
  onDiscardDraft,
  onLoadLatestCard,
  onLoadVerifications,
  onOpenStory,
  onReviewApproval,
}: CP1EditorProps) {
  const ambiguities = Array.isArray(card.ambiguities) ? card.ambiguities : [];
  const stories = card.stories ?? [];
  const localExamplesEdited = areExamplesModified(card.examples, draftExamples);
  const draftDirty = isDraftDirty(card, draftAnswers, draftExamples);
  const priorWasDraft = priorCard?.status === "needs_confirmation" && card.status === "confirmed";
  const ctxValue: CP1DraftContextValue = {
    card,
    draftAnswers,
    draftExamples,
    rpcInFlight,
    localExamplesEdited,
    setAnswer: onSetAnswer,
  };

  let openUINode: React.ReactNode = null;
  let composedQuestionIndices: ReadonlySet<number> | null = null;
  if (card.ui_composition) {
    const check = validateCompletedOpenUI(card.ui_composition.program, card);
    if (check.ok) {
      composedQuestionIndices = check.questionIndices;
      openUINode = (
        <div
          id="highbay-openui-workspace"
          data-composed-for-revision={card.ui_composition.composed_for_revision}
        >
          <Renderer
            library={highbayDecisionLibrary}
            response={card.ui_composition.program}
            isStreaming={false}
            publishObservability={false}
          />
        </div>
      );
    } else {
      openUINode = (
        <Alert id="highbay-openui-error" variant="destructive" role="alert">
          <strong>Interactive decision preview unavailable: </strong>
          You can still answer every open question and edit or confirm every example directly in the
          form below.
          <details className="highbay-details">
            <summary>Details</summary>
            <p>{check.error}</p>
          </details>
        </Alert>
      );
    }
  }
  const formQuestions = ambiguities.map((question, index) => ({ question, number: index + 1 }))
    .filter(({ number }) => !composedQuestionIndices?.has(number));

  const savedAgreement = (
    <>
      {card.status === "confirmed" && ambiguities.length > 0 ? (
        <div id="highbay-confirmed-decisions" role="region" aria-label="Confirmed Decisions">
          <p>
            <strong>Confirmed Decisions:</strong>
          </p>
          <ul>
            {ambiguities.map((amb) => (
              <li key={amb.id}>
                <strong>{amb.question}</strong>{" "}
                <code>{card.answers?.[amb.id] ?? ""}</code>
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
      {card.status !== "confirmed" && card.prompt.trim() !== card.summary.trim() ? <p>
        <strong>Requested change: </strong>
        {card.prompt}
      </p> : null}
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
    </>
  );

  return (
    <CP1DraftContext.Provider value={ctxValue}>
      {card.status === "confirmed" ? buildSection : null}

      <Card className="highbay-meta">
        <div className="highbay-region-header">
          <div className="highbay-region-title-group">
            <h2>
              {card.status === "confirmed"
                ? card.prompt.trim() === card.summary.trim() ? "Agreement" : "Agreed: " + card.prompt
                : "Review & Confirm Change Intent"}
            </h2>
          </div>
          <div className="highbay-toolbar-actions">
            <Badge variant={card.status === "confirmed" ? "success" : "warning"}>
              {card.status === "confirmed" ? "Agreed" : "Needs confirmation"}
            </Badge>
            {card.approval ? <Badge variant="success">Approval recorded</Badge> : null}
            {card.unverified_approval ? <Badge variant="warning">Approval unverified</Badge> : null}
            {!cp4Open ? (
              <Button
                id="highbay-load-verifications"
                disabled={loadingVerifications}
                onClick={onLoadVerifications}
              >
                {loadingVerifications ? "Loading saved verifications..." : "View saved verifications"}
              </Button>
            ) : null}
          </div>
        </div>
        <p id="highbay-agreement-revision">
          {card.status === "confirmed" ? "Agreed revision" : "Draft revision"} <code>{card.request_revision.slice(0, 12)}</code>
        </p>
        {card.revision_note ? (
          <p id="highbay-agreement-revision-note"><strong>Revision note: </strong>{card.revision_note}</p>
        ) : null}
        {card.status === "confirmed" ? (
          <details className="highbay-details" id="highbay-agreement-details">
            <summary>Agreement details</summary>
            {savedAgreement}
          </details>
        ) : savedAgreement}
      </Card>

      {card.status !== "confirmed" ? buildSection : null}

      {card.approval !== undefined || card.unverified_approval !== undefined ? (
        <section className="highbay-approval-summary" aria-label="Saved approval">
          <p><strong>{card.unverified_approval !== undefined ? "Saved approval needs checking." : "Approval recorded."}</strong>
            {" Read its evidence, verified commits and current status in Approve."}</p>
          {draftDirty ? <Alert id="highbay-cp5-approval-dirty-warning" variant="warning" role="status">
            Saving different answers or examples creates a new revision and clears this approval.
          </Alert> : null}
          <Button id="highbay-review-approval" onClick={onReviewApproval}>Review saved approval</Button>
        </section>
      ) : null}

      {priorCard ? (
        <details
          id="highbay-prior-revision"
          className="highbay-card highbay-details"
          key={priorCard.request_revision}
          open={priorWasDraft ? undefined : true}
        >
          <summary>
            {priorWasDraft ? "Previous unconfirmed draft" : "Previous saved revision"}{" "}
            <code>{priorCard.request_revision.slice(0, 12)}</code>
          </summary>
          {priorWasDraft ? (
            <p>This is the proposal before confirmation. Your confirmed answers are saved in the current agreement.</p>
          ) : null}
          <p>
            <strong>Previous request: </strong>
            {priorCard.prompt}
          </p>
          {priorCard.revision_note ? (
            <p>
              <strong>Previous revision note: </strong>
              {priorCard.revision_note}
            </p>
          ) : null}
          <p>
            <strong>Previous summary: </strong>
            {priorCard.summary}
          </p>
          <h4>
            {"Previous Questions & Decisions (" +
              (Array.isArray(priorCard.ambiguities) ? priorCard.ambiguities.length : 0) +
              ")"}
          </h4>
          {Array.isArray(priorCard.ambiguities) && priorCard.ambiguities.length > 0 ? (
            <ul>
              {priorCard.ambiguities.map((amb) => {
                const ans =
                  priorCard.answers &&
                  Object.prototype.hasOwnProperty.call(priorCard.answers, amb.id)
                    ? priorCard.answers[amb.id]
                    : "";
                return (
                  <li key={amb.id}>
                    <strong>{amb.question}</strong>
                    {ans
                      ? " — Confirmed answer: " + ans
                      : " — Not yet answered in that proposal (options: " + amb.options.join(", ") + ")"}
                  </li>
                );
              })}
            </ul>
          ) : (
            <p>No prior domain questions.</p>
          )}
          <h4>{"Previous Examples (" + priorCard.examples.length + ")"}</h4>
          <ul>
            {priorCard.examples.map((ex) => {
              const currentEx = card.examples.find((c) => c.id === ex.id);
              let statusText = "Removed";
              let badgeVariant: "default" | "accent" | "destructive" = "destructive";
              if (currentEx) {
                const unchanged =
                  currentEx.scenario === ex.scenario &&
                  currentEx.input_summary === ex.input_summary &&
                  currentEx.expected_outcome === ex.expected_outcome &&
                  currentEx.verification_kind === ex.verification_kind;
                if (unchanged) {
                  statusText = "Kept";
                  badgeVariant = "default";
                } else {
                  statusText = "Edited";
                  badgeVariant = "accent";
                }
              }
              return (
                <li key={ex.id}>
                  <code>{ex.id}</code> <Badge variant={badgeVariant}>{statusText}</Badge>{" "}
                  <strong>{ex.scenario}</strong>
                  {" — Input: " +
                    ex.input_summary +
                    " -> Expected: " +
                    ex.expected_outcome +
                    " [" +
                    ex.verification_kind +
                    "]"}
                </li>
              );
            })}
          </ul>
          <h4>
            {"Previous Preserved Invariants (" + priorCard.preserved_behaviors.length + ")"}
          </h4>
          {priorCard.preserved_behaviors.length > 0 ? (
            <ul>
              {priorCard.preserved_behaviors.map((pb, idx) => (
                <li key={String(idx)}>{pb}</li>
              ))}
            </ul>
          ) : (
            <p>None declared.</p>
          )}
          <details className="highbay-details">
            <summary>Details</summary>
            <p>Previous saved revision: <code>{priorCard.request_revision}</code> · Status: <code>{priorCard.status}</code></p>
          </details>
        </details>
      ) : null}

      {preservedDraft && Array.isArray(preservedDraft.lines) ? (
        <Card
          id="highbay-preserved-draft"
          className="highbay-card"
          role="region"
          aria-label="Preserved Unsaved Draft Edits"
        >
          <h3>Preserved Unsaved Draft Edits</h3>
          <p>
            Review the latest saved Change Card below before re-applying any of your unsaved edits:
          </p>
          <ul>
            {preservedDraft.lines.map((line, idx) => (
              <li key={String(idx)}>{line}</li>
            ))}
          </ul>
          <details className="highbay-details">
            <summary>Details</summary>
            <p>
              Superseded revision: <code>{preservedDraft.fromRevision}</code>
            </p>
          </details>
        </Card>
      ) : null}

      <RefinementDisclosure
        confirmed={card.status === "confirmed"}
        keepOpen={draftDirty || preservedDraft !== null || formError !== null}
      >
        {openUINode}

        <Card
          id="highbay-cp1-form-card"
          className="highbay-card"
          aria-busy={rpcInFlight ? "true" : undefined}
        >
          {formQuestions.length > 0 ? <>
            <h3>{(card.status === "confirmed" ? "Your answers" : "Open Questions") + " (" + formQuestions.length + ")"}</h3>
            <div className="highbay-question-list">
              {formQuestions.map(({ question, number }) => {
                const selected = Object.prototype.hasOwnProperty.call(draftAnswers, question.id) ? draftAnswers[question.id] : "";
                const edited = card.status === "confirmed" && selected !== card.answers?.[question.id];
                return <section className="highbay-question" key={question.id}>
                  <h4>{question.question} {selected ? <Badge variant="outline">{edited ? "Edited" : "Selected"}</Badge> : null}</h4>
                  <ChoiceGroup label={"Question " + number + ". " + question.question}
                    options={question.options} selected={selected} matchingOption={question.draft_option} disabled={rpcInFlight}
                    onSelect={(option) => onSetAnswer(question.id, option)}
                    optionAttributes={(option) => ({ "data-ambiguity-id": question.id, "data-option": option })} />
                </section>;
              })}
            </div>
            <Separator />
          </> : null}

          <h3>Concrete examples</h3>
          {draftExamples.length > 0 ? <p className="highbay-muted" id="highbay-example-check-labels">
            Check labels guide test writing. Verification reports suite scenario results. Use scenario coverage to find the tests for an example.
            Starter tests use a scripted model and need no API key. Configure a model in Live target for live prompt trials.
          </p> : null}
          <div className="highbay-examples-list">
            {draftExamples.map((ex, idx) => (
              <section key={ex.id} className="highbay-example-card" aria-label={"Example " + (idx + 1)}
                data-example-index={String(idx)}>
                <div className="highbay-example-header">
                  <h4>{"Example " + (idx + 1)}</h4>
                  <Button
                    disabled={rpcInFlight}
                    data-example-id={ex.id}
                    data-field="verification_kind"
                    onClick={() => onSetExampleField(
                      ex.id, "verification_kind",
                      ex.verification_kind === "deterministic" ? "model_judged" : "deterministic"
                    )}
                  >
                    {ex.verification_kind === "deterministic" ? "Check: Deterministic" : "Check: Model-judged"}
                  </Button>
                </div>
                <FieldTextarea
                  exampleId={ex.id}
                  field="scenario"
                  label="Scenario"
                  value={ex.scenario}
                  disabled={rpcInFlight}
                  onChangeValue={(v) => onSetExampleField(ex.id, "scenario", v)}
                />
                <div className="highbay-example-fields">
                  <FieldTextarea
                    exampleId={ex.id}
                    field="input_summary"
                    label="Input"
                    value={ex.input_summary}
                    disabled={rpcInFlight}
                    onChangeValue={(v) => onSetExampleField(ex.id, "input_summary", v)}
                  />
                  <FieldTextarea
                    exampleId={ex.id}
                    field="expected_outcome"
                    label="Expected result"
                    value={ex.expected_outcome}
                    disabled={rpcInFlight}
                    onChangeValue={(v) => onSetExampleField(ex.id, "expected_outcome", v)}
                  />
                </div>
              </section>
            ))}
          </div>

          {card.preserved_behaviors.length > 0 ? <>
            <h3>Preserved behaviors</h3>
            <ul>
              {card.preserved_behaviors.map((pb, idx) => (
                <li key={String(idx)}>{pb}</li>
              ))}
            </ul>
          </> : null}
          {ambiguities.length > 0 || draftExamples.length > 0 ? <details className="highbay-details">
            <summary>Technical identifiers</summary>
            {ambiguities.length > 0 ? <p>Questions {ambiguities.map((amb) => <code key={amb.id}>{amb.id} </code>)}</p> : null}
            {draftExamples.length > 0 ? <p>Examples {draftExamples.map((ex) => <code key={ex.id}>{ex.id} </code>)}</p> : null}
          </details> : null}

          <div
            id="highbay-confirm-story"
            className="highbay-panel-section"
            role="region"
            aria-label="Story of this change"
          >
            <p>
              {stories.length === 0
                ? "This change has no Story."
                : "This change's Story has " + countsPhrase(storyCounts(stories)) + "."}{" "}
              Only complete flows are tested end to end; drafts do not block confirming.
            </p>
            <Button id="highbay-confirm-open-story" onClick={onOpenStory}>
              Show the Story
            </Button>
          </div>

          {proposalWaiting ? (
            <p id="highbay-confirm-blocked">
              A proposed revision waits above. Adopt or decline it before refining the agreed
              revision.
            </p>
          ) : null}
          {card.status === "confirmed" && draftDirty && !proposalWaiting && buildRevisionNotice !== null ? (
            <p id="highbay-confirm-build-consequence">{buildRevisionNotice}</p>
          ) : null}
          {revisionAid}
          <CardFooter>
            <Button
              id="highbay-confirm-submit"
              variant={card.status === "confirmed" && draftDirty ? "primary" : "secondary"}
              disabled={rpcInFlight || proposalWaiting || revisionAid !== null}
              aria-describedby={proposalWaiting ? "highbay-confirm-blocked" :
                revisionAid !== null ? "highbay-confirm-revision-needed-label" :
                card.status === "confirmed" && draftDirty && buildRevisionNotice !== null
                  ? "highbay-confirm-build-consequence" : undefined}
              onClick={onSubmitConfirm}
            >
              {card.status === "needs_confirmation"
                ? "Confirm Intent & Lock Examples"
                : "Refine Confirmed Answers & Examples"}
            </Button>
            <Button
              id="highbay-discard-draft"
              variant="ghost"
              disabled={rpcInFlight}
              onClick={onDiscardDraft}
            >
              Discard Unsaved Edits
            </Button>
          </CardFooter>

          <div id="highbay-confirm-feedback">
            {formError ? (
              <Alert id="highbay-confirm-error" variant="destructive" role="alert" tabIndex={-1}>
                {onLoadLatestCard !== null ? <strong>Change Card request failed: </strong> : null}
                <p>{formError}</p>
                {onLoadLatestCard !== null ? <>
                  <p>Your edited answers and examples remain in this form. Choose <strong>Load Latest Card</strong> to inspect the current saved Change Card alongside your unsaved edits.</p>
                  <div className="highbay-field-row">
                    <Button id="highbay-load-latest-card" disabled={rpcInFlight} onClick={onLoadLatestCard}>
                      Load Latest Card
                    </Button>
                  </div>
                </> : null}
              </Alert>
            ) : null}
          </div>
        </Card>
      </RefinementDisclosure>
    </CP1DraftContext.Provider>
  );
}

// RefinementDisclosure keeps confirmed edits out of the main reading path while
// preserving the native disclosure and its mounted fields across refreshes.
// Active drafts and recovery paths stay visible until the user resolves them.
function RefinementDisclosure({
  confirmed,
  keepOpen,
  children,
}: {
  confirmed: boolean;
  keepOpen: boolean;
  children: React.ReactNode;
}) {
  const detailsRef = useRef<HTMLDetailsElement | null>(null);

  useEffect(() => {
    if (keepOpen && detailsRef.current) {
      detailsRef.current.open = true;
    }
  }, [confirmed, keepOpen]);

  if (!confirmed) {
    return <>{children}</>;
  }
  return (
    <details
      ref={detailsRef}
      id="highbay-refine-agreement"
      onToggle={(event) => {
        if (keepOpen && !event.currentTarget.open) {
          event.currentTarget.open = true;
        }
      }}
    >
      <summary>Refine agreed answers and examples</summary>
      {children}
    </details>
  );
}
