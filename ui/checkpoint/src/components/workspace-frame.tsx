// The workspace frame keeps orientation and the stage's next action together.
import React, { useEffect, useRef } from "react";
import { Button } from "./shadcn";

// FrameDestination names an existing destination without duplicating its owner.
export interface FrameDestination {
  id: string;
  label: string;
  active: boolean;
  disabled?: boolean;
  onClick: () => void;
}

// WorkspaceFrameProps gives the fixed frame current page facts and one action.
export interface WorkspaceFrameProps {
  project: string;
  journey: FrameDestination[];
  references: FrameDestination[];
  agreed: string | null;
  onReadFullSummary: (() => void) | null;
  showSlots: boolean;
  attention: string;
  next: React.ReactNode;
  details: React.ReactNode;
  onToggleDiagnostics: () => void;
  diagnosticsOpen: boolean;
}

// WorkspaceFrame keeps secondary views in Inspect and shows a single Next
// action, which moves to the phone's bottom surface through CSS.
export function WorkspaceFrame(props: WorkspaceFrameProps) {
  const frame = useRef<HTMLDivElement>(null);
  const next = useRef<HTMLDivElement>(null);
  const inspect = useRef<HTMLDetailsElement>(null);
  const inspectToggle = useRef<HTMLElement>(null);

  useEffect(() => {
    const dismiss = (event: PointerEvent) => {
      if (inspect.current?.open && event.target instanceof Node && !inspect.current.contains(event.target)) {
        inspect.current.open = false;
      }
    };
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape" && !event.defaultPrevented && inspect.current?.open) {
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
  useEffect(() => {
    // The real browser measures wrapping, zoom and safe-area changes; the
    // layout-free DOM harness exercises the separate interaction contract.
    if (typeof ResizeObserver === "undefined") return;
    const measure = () => {
      document.documentElement.style.setProperty("--surla-frame-height", `${frame.current?.getBoundingClientRect().height ?? 0}px`);
      document.documentElement.style.setProperty("--surla-next-height", `${next.current?.getBoundingClientRect().height ?? 0}px`);
    };
    const observer = new ResizeObserver(measure);
    if (frame.current !== null) observer.observe(frame.current);
    if (next.current !== null) observer.observe(next.current);
    measure();
    return () => observer.disconnect();
  }, [props.showSlots]);

  // Restore focus if the selected control is about to be hidden. A destination
  // that moves focus to its heading can then take focus from Inspect.
  function selectReference(open: () => void) {
    if (inspect.current?.open) {
      const restoreFocus = inspect.current.contains(document.activeElement);
      inspect.current.open = false;
      if (restoreFocus) inspectToggle.current?.focus();
    }
    open();
  }

  const activeReference = props.diagnosticsOpen ? "Diagnostics" : props.references.find((item) => item.active)?.label;
  return <div className="surla-fixed-frame" ref={frame}>
    <div className="surla-frame-where">
      <h1 className="surla-frame-project"><span>Surla</span><span aria-hidden="true">/</span>{props.project}</h1>
      {props.journey.length > 0 ? <div className="surla-frame-journey" role="group" aria-label="Change journey">
        {props.journey.map((step) => <Button key={step.id} id={step.id}
          aria-current={step.active ? "step" : undefined} disabled={step.disabled}
          onClick={step.onClick}>{step.label}</Button>)}
      </div> : null}
      <details className="surla-frame-project-menu" ref={inspect}>
        <summary ref={inspectToggle} data-active={activeReference !== undefined ? "true" : undefined}>
          {activeReference === undefined ? "Inspect" : "Inspect · " + activeReference}
        </summary>
        <div className="surla-frame-references" role="group" aria-label="Project references">
          {props.references.map((item) => <Button key={item.id} id={item.id}
            aria-current={item.active ? "page" : undefined} disabled={item.disabled}
            onClick={() => selectReference(item.onClick)}>{item.label}</Button>)}
          <Button id="surla-trace-drawer-toggle" aria-expanded={props.diagnosticsOpen}
            aria-controls="surla-trace-drawer" onClick={() => selectReference(props.onToggleDiagnostics)}>
            Diagnostics
          </Button>
        </div>
      </details>
    </div>
    {props.showSlots ? <dl className="surla-frame-slots">
      {props.agreed !== null ? <div className="surla-frame-agreed"><dt>Agreed</dt><dd>
        <span className="surla-frame-summary">{props.agreed}</span>
        {props.onReadFullSummary !== null ? <Button id="surla-read-full-summary"
          onClick={props.onReadFullSummary}>Read full summary</Button> : null}
      </dd></div> : null}
      <div className="surla-frame-attention"><dt>Needs you</dt><dd>{props.attention}</dd></div>
      <div className="surla-frame-next" id="surla-frame-next" ref={next}><dt>Next</dt><dd>{props.next}</dd></div>
    </dl> : null}
    {props.details !== null ? <details className="surla-frame-context">
      <summary>Current view details</summary>{props.details}
    </details> : null}
  </div>;
}
