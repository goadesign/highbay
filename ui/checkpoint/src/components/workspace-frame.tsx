// workspace-frame.tsx renders the sticky top Studio header (#highbay-checkpoint-nav):
// project identity, Changes / + New change navigation, the 5-step Change Stepper
// (collapsing cleanly into Stage: <Current> ▾ below 900px with a single ID set),
// narrow-viewport Conversation/Terminal sheet toggles (< 1080px), the Inspect
// reference menu, and the contextual Agreed / Next bar.

import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Button } from "./shadcn";

// FrameDestination names an existing destination without duplicating its owner.
export interface FrameDestination {
  id: string;
  label: string;
  active: boolean;
  disabled?: boolean;
  onClick: () => void;
}

// JourneyStep describes one stage button in the 5-step Change Stepper.
export interface JourneyStep extends FrameDestination {
  status: "done" | "attention" | "neutral";
  glyph?: string;
  unsaved?: boolean;
}

// MobileConversationToggle describes the narrow-viewport Conversation sheet trigger.
export interface MobileConversationToggle {
  active: boolean;
  unreadCount: number;
  messageCount: number;
  onClick: (invoker: HTMLElement) => void;
}

// MobileTerminalToggle describes the narrow-viewport Terminal sheet trigger.
export interface MobileTerminalToggle {
  active: boolean;
  label: string;
  primary: boolean;
  onClick: (invoker: HTMLElement) => void;
}

// WorkspaceFrameProps gives the fixed frame current page facts and one Next slot.
export interface WorkspaceFrameProps {
  project: string;
  changesNav: FrameDestination | null;
  newChangeNav: { active: boolean; disabled: boolean; primary: boolean; onClick: () => void } | null;
  journey: JourneyStep[];
  references: FrameDestination[];
  mobileConversation: MobileConversationToggle | null;
  mobileTerminal: MobileTerminalToggle | null;
  agreed: string | null;
  onReadFullSummary: (() => void) | null;
  showSlots: boolean;
  hideNextForSheet: boolean;
  attention: string;
  next: React.ReactNode;
  details: React.ReactNode;
  onToggleDiagnostics: () => void;
  diagnosticsOpen: boolean;
}

// useMediaQuery subscribes to a CSS media query, returning false in layout-free DOM harnesses.
function useMediaQuery(query: string): boolean {
  const [matches, setMatches] = useState<boolean>(() =>
    typeof window !== "undefined" && typeof window.matchMedia === "function"
      ? window.matchMedia(query).matches
      : false
  );
  useEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
      return;
    }
    const mql = window.matchMedia(query);
    const update = () => setMatches(mql.matches);
    update();
    mql.addEventListener("change", update);
    return () => mql.removeEventListener("change", update);
  }, [query]);
  return matches;
}

// WorkspaceFrame keeps secondary views in Inspect and shows a single Next
// slot, which moves to the phone's bottom surface through CSS when actionable.
export function WorkspaceFrame(props: WorkspaceFrameProps) {
  const frame = useRef<HTMLDivElement>(null);
  const next = useRef<HTMLDivElement>(null);
  const inspect = useRef<HTMLDetailsElement>(null);
  const inspectToggle = useRef<HTMLElement>(null);
  const stageMenu = useRef<HTMLDetailsElement>(null);
  const stageMenuToggle = useRef<HTMLElement>(null);

  const compactStepper = useMediaQuery("(max-width: 899px)");
  const narrowWorkspace = useMediaQuery("(max-width: 1079px)");

  useEffect(() => {
    const dismiss = (event: PointerEvent) => {
      if (inspect.current?.open && event.target instanceof Node && !inspect.current.contains(event.target)) {
        inspect.current.open = false;
      }
      if (stageMenu.current?.open && event.target instanceof Node && !stageMenu.current.contains(event.target)) {
        stageMenu.current.open = false;
      }
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) {
        return;
      }
      if (stageMenu.current?.open) {
        event.preventDefault();
        stageMenu.current.open = false;
        stageMenuToggle.current?.focus();
        return;
      }
      if (inspect.current?.open) {
        event.preventDefault();
        inspect.current.open = false;
        inspectToggle.current?.focus();
      }
    };
    document.addEventListener("pointerdown", dismiss);
    document.addEventListener("keydown", escape);
    return () => {
      document.removeEventListener("pointerdown", dismiss);
      document.removeEventListener("keydown", escape);
    };
  }, []);

  useLayoutEffect(() => {
    if (next.current !== null) {
      next.current.inert = props.hideNextForSheet;
    }
  }, [props.hideNextForSheet, props.showSlots]);

  useLayoutEffect(() => {
    const navEl = frame.current?.closest("#highbay-checkpoint-nav") ?? frame.current;
    const frameHeight = Math.ceil(navEl?.getBoundingClientRect().height ?? 0);
    const nextHeight = props.hideNextForSheet
      ? 0
      : Math.ceil(next.current?.getBoundingClientRect().height ?? 0);
    if (frameHeight > 0) {
      document.documentElement.style.setProperty("--highbay-frame-height", `${frameHeight}px`);
    }
    document.documentElement.style.setProperty("--highbay-next-height", `${nextHeight}px`);
  });

  useEffect(() => {
    if (typeof ResizeObserver === "undefined") {
      return;
    }
    const measure = () => {
      const navEl = frame.current?.closest("#highbay-checkpoint-nav") ?? frame.current;
      document.documentElement.style.setProperty(
        "--highbay-frame-height",
        `${Math.ceil(navEl?.getBoundingClientRect().height ?? 0)}px`
      );
      document.documentElement.style.setProperty(
        "--highbay-next-height",
        `${props.hideNextForSheet ? 0 : Math.ceil(next.current?.getBoundingClientRect().height ?? 0)}px`
      );
    };
    const observer = new ResizeObserver(measure);
    const navEl = frame.current?.closest("#highbay-checkpoint-nav") ?? frame.current;
    if (navEl !== null) {
      observer.observe(navEl);
    }
    if (next.current !== null) {
      observer.observe(next.current);
    }
    measure();
    return () => observer.disconnect();
  }, [props.showSlots, props.hideNextForSheet]);

  function selectReference(open: () => void) {
    if (inspect.current?.open) {
      const restoreFocus = inspect.current.contains(document.activeElement);
      inspect.current.open = false;
      if (restoreFocus) {
        inspectToggle.current?.focus();
      }
    }
    open();
  }

  function selectStage(open: () => void) {
    if (stageMenu.current?.open) {
      const restoreFocus = stageMenu.current.contains(document.activeElement);
      stageMenu.current.open = false;
      if (restoreFocus) {
        stageMenuToggle.current?.focus();
      }
    }
    open();
  }

  const activeReference = props.diagnosticsOpen
    ? "Diagnostics"
    : props.references.find((item) => item.active)?.label;
  const activeStep = props.journey.find((step) => step.active);

  const journeyGroup =
    props.journey.length > 0 ? (
      <div className="highbay-frame-journey" role="group" aria-label="Change journey">
        {props.journey.map((step) => (
          <Button
            key={step.id}
            id={step.id}
            aria-current={step.active ? "step" : undefined}
            data-step-status={step.status}
            data-unsaved={step.unsaved ? "true" : undefined}
            disabled={step.disabled}
            onClick={() => selectStage(step.onClick)}
          >
            <span>{step.label}</span>
            {step.glyph !== undefined ? (
              <span className="highbay-step-glyph" aria-hidden="true">
                {step.glyph}
              </span>
            ) : null}
          </Button>
        ))}
      </div>
    ) : null;

  return (
    <div className="highbay-fixed-frame" ref={frame}>
      <div className="highbay-frame-where">
        <div className="highbay-frame-brand-group">
          <h1 className="highbay-frame-project">
            <span>Highbay</span>
            <span aria-hidden="true">/</span>
            {props.project}
          </h1>
          {props.changesNav !== null ? (
            <Button
              id={props.changesNav.id}
              variant="pill"
              aria-current={props.changesNav.active ? "page" : undefined}
              disabled={props.changesNav.disabled}
              onClick={props.changesNav.onClick}
            >
              {props.changesNav.label}
            </Button>
          ) : null}
          {props.newChangeNav !== null ? (
            <Button
              id="highbay-new-change"
              variant={props.newChangeNav.primary ? "primary" : props.newChangeNav.active ? "pill" : "secondary"}
              aria-current={props.newChangeNav.active ? "page" : undefined}
              disabled={props.newChangeNav.disabled}
              onClick={props.newChangeNav.onClick}
            >
              + New change
            </Button>
          ) : null}
        </div>

        {journeyGroup !== null ? (
          compactStepper ? (
            <details id="highbay-stage-menu" className="highbay-stage-menu" ref={stageMenu}>
              <summary ref={stageMenuToggle}>
                {"Stage: " + (activeStep?.label ?? "Agree") + " ▾"}
              </summary>
              {journeyGroup}
            </details>
          ) : (
            journeyGroup
          )
        ) : null}

        <div className="highbay-frame-end-actions">
          {narrowWorkspace && props.mobileConversation !== null ? (
            <Button
              id="highbay-mobile-conversation-toggle"
              variant="pill"
              aria-pressed={props.mobileConversation.active}
              onClick={(event) => props.mobileConversation!.onClick(event.currentTarget)}
            >
              {"Chat (" +
                (props.mobileConversation.unreadCount > 0
                  ? props.mobileConversation.unreadCount + " new"
                  : props.mobileConversation.messageCount) +
                ")"}
            </Button>
          ) : null}
          {narrowWorkspace && props.mobileTerminal !== null ? (
            <Button
              id="highbay-mobile-terminal-toggle"
              variant={props.mobileTerminal.primary ? "primary" : "pill"}
              aria-pressed={props.mobileTerminal.active}
              onClick={(event) => props.mobileTerminal!.onClick(event.currentTarget)}
            >
              {props.mobileTerminal.label}
            </Button>
          ) : null}
          <details className="highbay-frame-project-menu" ref={inspect}>
            <summary
              ref={inspectToggle}
              data-active={activeReference !== undefined ? "true" : undefined}
            >
              {activeReference === undefined ? "Inspect" : "Inspect · " + activeReference}
            </summary>
            <div className="highbay-frame-references" role="group" aria-label="Project references">
              {props.references.map((item) => (
                <Button
                  key={item.id}
                  id={item.id}
                  aria-current={item.active ? "page" : undefined}
                  disabled={item.disabled}
                  onClick={() => selectReference(item.onClick)}
                >
                  {item.label}
                </Button>
              ))}
              <Button
                id="highbay-trace-drawer-toggle"
                aria-expanded={props.diagnosticsOpen}
                aria-controls="highbay-trace-drawer"
                onClick={() => selectReference(props.onToggleDiagnostics)}
              >
                Diagnostics
              </Button>
            </div>
          </details>
        </div>
      </div>

      {props.showSlots ? (
        <dl className="highbay-frame-slots">
          {props.agreed !== null ? (
            <div className="highbay-frame-agreed">
              <dt>Agreed</dt>
              <dd>
                <span className="highbay-frame-summary">{props.agreed}</span>
                {props.onReadFullSummary !== null ? (
                  <Button id="highbay-read-full-summary" onClick={props.onReadFullSummary}>
                    Read full summary
                  </Button>
                ) : null}
              </dd>
            </div>
          ) : null}
          <div className="highbay-frame-attention">
            <dt>Status</dt>
            <dd>{props.attention}</dd>
          </div>
          <div
            className="highbay-frame-next"
            id="highbay-frame-next"
            ref={next}
            hidden={props.hideNextForSheet}
          >
            <dt>Next</dt>
            <dd>{props.next}</dd>
          </div>
        </dl>
      ) : null}

      {props.details !== null ? (
        <details className="highbay-frame-context">
          <summary>Current view details</summary>
          {props.details}
        </details>
      ) : null}
    </div>
  );
}
