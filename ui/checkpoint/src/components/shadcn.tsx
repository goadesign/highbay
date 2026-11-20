// Package shadcn provides accessible, composable shadcn/ui React components
// tailored for the Surla Architecture Workspace.

import React from "react";

export interface CardProps extends React.HTMLAttributes<HTMLDivElement> {
  children?: React.ReactNode;
}

// Card renders a bordered shadcn/ui surface container.
export function Card({ className = "", children, ...rest }: CardProps) {
  const cls = ["shadcn-card", className].filter(Boolean).join(" ");
  return (
    <div className={cls} {...rest}>
      {children}
    </div>
  );
}

// CardHeader renders the top heading block of a Card.
export function CardHeader({ className = "", children, ...rest }: CardProps) {
  const cls = ["shadcn-card-header", className].filter(Boolean).join(" ");
  return (
    <div className={cls} {...rest}>
      {children}
    </div>
  );
}

// CardTitle renders the primary heading inside a CardHeader.
export function CardTitle({ className = "", children, ...rest }: React.HTMLAttributes<HTMLHeadingElement>) {
  const cls = ["shadcn-card-title", className].filter(Boolean).join(" ");
  return (
    <h3 className={cls} {...rest}>
      {children}
    </h3>
  );
}

// CardDescription renders secondary supporting text inside a CardHeader.
export function CardDescription({ className = "", children, ...rest }: React.HTMLAttributes<HTMLParagraphElement>) {
  const cls = ["shadcn-card-desc", className].filter(Boolean).join(" ");
  return (
    <p className={cls} {...rest}>
      {children}
    </p>
  );
}

// CardContent renders the body slot of a Card.
export function CardContent({ className = "", children, ...rest }: CardProps) {
  return (
    <div className={className} {...rest}>
      {children}
    </div>
  );
}

// CardFooter groups the actions that complete the card's task.
export function CardFooter({ className = "", ...rest }: CardProps) {
  return <div className={["shadcn-card-footer", className].filter(Boolean).join(" ")} {...rest} />;
}

export interface BadgeProps extends React.HTMLAttributes<HTMLSpanElement> {
  variant?: "default" | "accent" | "success" | "warning" | "destructive" | "outline";
  children?: React.ReactNode;
}

// Badge renders a compact pill label for status, verification kind, or active policy outcomes.
export function Badge({ variant = "default", className = "", children, ...rest }: BadgeProps) {
  const variantCls = variant === "default" ? "" : "shadcn-badge-" + variant;
  const cls = ["shadcn-badge", "surla-badge", variantCls, className].filter(Boolean).join(" ");
  return (
    <span className={cls} {...rest}>
      {children}
    </span>
  );
}

export interface ButtonProps extends React.ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: "default" | "primary" | "secondary" | "ghost" | "pill";
  children?: React.ReactNode;
}

// Button renders an accessible action button or toggle pill.
export function Button({ variant = "default", className = "", type = "button", children, ...rest }: ButtonProps) {
  const variantCls =
    variant === "pill" ? "surla-pill" : variant === "default" ? "surla-btn"
      : "surla-btn shadcn-btn-" + variant + " surla-btn-" + variant;
  const cls = ["shadcn-btn", variantCls, className].filter(Boolean).join(" ");
  return (
    <button type={type} className={cls} {...rest}>
      {children}
    </button>
  );
}

// ChoiceGroupProps binds visible options to the single saved or draft answer.
interface ChoiceGroupProps {
  label: string;
  options: readonly string[];
  selected: string;
  disabled?: boolean;
  onSelect: (option: string) => void;
  optionAttributes?: (option: string) => { [name: `data-${string}`]: string };
}

// ChoiceGroup gives one Tab stop to a set of exclusive choices. Arrow keys
// move focus and select within the group, like a native single-choice field.
export function ChoiceGroup({ label, options, selected, disabled, onSelect, optionAttributes }: ChoiceGroupProps) {
  const selectedIndex = Math.max(0, options.indexOf(selected));
  function moveChoice(event: React.KeyboardEvent<HTMLDivElement>) {
    const direction = event.key === "ArrowRight" || event.key === "ArrowDown" ? 1
      : event.key === "ArrowLeft" || event.key === "ArrowUp" ? -1 : 0;
    if (direction === 0 || disabled) return;
    const choices = Array.from(event.currentTarget.querySelectorAll<HTMLButtonElement>(".shadcn-choice-option"));
    const index = choices.findIndex((choice) => choice === event.target);
    if (index < 0) return;
    event.preventDefault();
    const next = choices[(index + direction + choices.length) % choices.length];
    next.focus();
    next.click();
  }
  return <div className="shadcn-choice-group" role="group" aria-label={label} onKeyDown={moveChoice}>
    {options.map((option, index) => <ChoiceOption key={option} {...optionAttributes?.(option)}
      selected={selected === option} tabIndex={index === selectedIndex ? 0 : -1}
      disabled={disabled} onClick={() => onSelect(option)}>{option}</ChoiceOption>)}
  </div>;
}

// ChoiceOption pairs a persistent selection indicator with the full option.
function ChoiceOption({ selected, children, ...rest }: ButtonProps & { selected: boolean }) {
  return <Button {...rest} variant="pill" className="shadcn-choice-option" aria-pressed={selected}>
    <span className="shadcn-choice-indicator" aria-hidden="true" />
    <span>{children}</span>
  </Button>;
}

// Separator places a quiet boundary between related sections.
export function Separator() {
  return <hr className="shadcn-separator" />;
}

// Label associates a field with its visible name.
export function Label({ className = "", ...rest }: React.LabelHTMLAttributes<HTMLLabelElement>) {
  return <label className={["shadcn-label", className].filter(Boolean).join(" ")} {...rest} />;
}

// Textarea keeps multiline fields on the same typography and focus system.
export function Textarea({ className = "", ...rest }: React.ComponentPropsWithRef<"textarea">) {
  return <textarea className={["shadcn-textarea", className].filter(Boolean).join(" ")} {...rest} />;
}

export interface AlertProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: "default" | "warning" | "destructive";
  children?: React.ReactNode;
}

// Alert renders a callout banner for warnings, errors, or preserved drafts.
export function Alert({ variant = "default", className = "", children, ...rest }: AlertProps) {
  const variantCls =
    variant === "destructive"
      ? "shadcn-alert-destructive surla-error"
      : variant === "warning"
      ? "shadcn-alert-warning"
      : "";
  const cls = ["shadcn-alert", variantCls, className].filter(Boolean).join(" ");
  return (
    <div className={cls} {...rest}>
      {children}
    </div>
  );
}

export interface ResponsiveTableProps extends React.TableHTMLAttributes<HTMLTableElement> {
  ariaLabel: string;
  children?: React.ReactNode;
}

// ResponsiveTable wraps a semantic HTML table in a keyboard-scrollable container-query region.
export function ResponsiveTable({ ariaLabel, children, ...rest }: ResponsiveTableProps) {
  return (
    <div className="surla-table-wrap" role="region" aria-label={ariaLabel} tabIndex={0}>
      <table className="shadcn-table" role="table" {...rest}>
        {children}
      </table>
    </div>
  );
}
