// The workspace frame keeps orientation and the stage's next action together.
import React, { useEffect, useRef, useState } from "react";
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
  agreed: string;
  onReadFullSummary: (() => void) | null;
  attention: string;
  next: React.ReactNode;
  details: React.ReactNode;
  onToggleDiagnostics: () => void;
  diagnosticsOpen: boolean;
}

// WorkspaceFrame implements Layout B: two orienting rows and a single Next slot.
// The same action element moves to the phone's bottom surface through CSS.
export function WorkspaceFrame(props: WorkspaceFrameProps) {
  const [phone, setPhone] = useState(() => window.matchMedia("(max-width: 720px)").matches);
  const frame = useRef<HTMLDivElement>(null);
  const next = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const query = window.matchMedia("(max-width: 720px)");
    const changed = () => setPhone(query.matches);
    query.addEventListener("change", changed);
    return () => query.removeEventListener("change", changed);
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
  }, []);
  const references = <div className="surla-frame-references" role="group" aria-label="Project references">
    {props.references.map((item) => <Button key={item.id} id={item.id}
      aria-current={item.active ? "page" : undefined} disabled={item.disabled}
      onClick={item.onClick}>{item.label}</Button>)}
    <Button id="surla-trace-drawer-toggle" aria-expanded={props.diagnosticsOpen}
      aria-controls="surla-trace-drawer" onClick={props.onToggleDiagnostics}>Diagnostics</Button>
  </div>;
  return <div className="surla-fixed-frame" ref={frame}>
    <div className="surla-frame-where">
      <h1 className="surla-frame-project"><span>Surla</span><span aria-hidden="true">/</span>{props.project}</h1>
      <div className="surla-frame-journey" role="group" aria-label="Change journey">
        {props.journey.map((step) => <Button key={step.id} id={step.id}
          aria-current={step.active ? "step" : undefined} disabled={step.disabled}
          onClick={step.onClick}>{step.label}</Button>)}
      </div>
      {phone ? <details className="surla-frame-project-menu"><summary>Project</summary>{references}</details> : references}
    </div>
    <dl className="surla-frame-slots">
      <div className="surla-frame-agreed"><dt>Agreed</dt><dd>
        <span className="surla-frame-summary">{props.agreed}</span>
        {props.onReadFullSummary !== null ? <Button id="surla-read-full-summary"
          onClick={props.onReadFullSummary}>Read full summary</Button> : null}
      </dd></div>
      <div className="surla-frame-attention"><dt>Needs you</dt><dd>{props.attention}</dd></div>
      <div className="surla-frame-next" id="surla-frame-next" ref={next}><dt>Next</dt><dd>{props.next}</dd></div>
    </dl>
    <details className="surla-frame-context"><summary>Current view details</summary>{props.details}</details>
  </div>;
}
