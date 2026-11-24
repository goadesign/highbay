// Copyable prompts give the user words to take to their coding agent.
import React, { useState } from "react";
import { Button } from "./shadcn";

// CopyPrompt offers clipboard feedback and a selectable fallback for the
// exact prompt. A changed prompt starts with an empty announcement.
export function CopyPrompt({ text, label, showText = false }: {
  text: string;
  label: string;
  showText?: boolean;
}) {
  const [result, setResult] = useState<{ text: string; copied: boolean } | null>(null);
  const current = result?.text === text ? result : null;

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setResult({ text, copied: true });
    } catch {
      setResult({ text, copied: false });
    }
  }

  return <div>
    {showText ? <><code>{text}</code>{" "}</> : null}
    <Button onClick={copy}>{label}</Button>{" "}
    <span role="status">{current === null ? "" : current.copied ? "Prompt copied."
      : showText ? "Could not copy. Select the prompt above and copy it."
      : "Could not copy. Select the prompt below and copy it."}</span>
    {current?.copied === false && !showText ? <pre tabIndex={0}>{text}</pre> : null}
  </div>;
}
