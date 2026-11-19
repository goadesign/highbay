// Merge controls present the page owner's exact approval admission and outcome.
// Only the page sends requests or correlates saved runs with live approval.
import React from "react";
import { Alert, Button } from "./shadcn";

// MergeControls keeps mutation admission, request state and receipts in the page.
// Refresh reads current facts; it never retries a merge.
export interface MergeControls {
  eligible: boolean;
  pending: boolean;
  reason: string | null;
  error: { name: string | null; message: string } | null;
  receipt: { at: string; branch: string; head_commit: string } | null;
  onMerge: () => void;
  onRefresh: () => void;
}

// MergePrimaryAction belongs in the fixed frame's one primary-action slot.
// Its ID is assigned by the caller so mounted surfaces cannot duplicate it.
export function MergePrimaryAction({ id, controls }: { id: string; controls: MergeControls }) {
  return <Button id={id} variant="primary" disabled={!controls.eligible || controls.pending}
    onClick={controls.onMerge}>
    {controls.pending ? "Merging…" : "Merge into current branch"}
  </Button>;
}

// MergeFeedback preserves receipts and safe owner errors without adding a
// second merge action. Unknown outcomes recover through reads and inspection.
export function MergeFeedback({ idPrefix, controls }: { idPrefix: string; controls: MergeControls }) {
  const { error, receipt } = controls;
  const recovery = error === null ? null : mergeRecovery(error.name);
  return <section id={idPrefix} aria-label="Merge into current branch" aria-busy={controls.pending}>
    {controls.pending ? <p id={`${idPrefix}-pending`} role="status">
      Merging the approved commits into the project’s current branch… This continues if you close the page.
    </p> : null}
    {receipt !== null ? <div id={`${idPrefix}-receipt`} role="status">
      <p>Merged into <code>{receipt.branch}</code> at <code>{receipt.head_commit.slice(0, 12)}</code>
        {" on "}<time dateTime={receipt.at}>{new Date(receipt.at).toLocaleString(undefined, {
          year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit",
        })}</time>.</p>
      <p>Verification covered the approved commits before the merge, not the resulting branch. Nothing was deployed.</p>
      <details className="surla-details" id={`${idPrefix}-receipt-details`}>
        <summary>Merge receipt</summary>
        <dl><dt>Branch</dt><dd><code>{receipt.branch}</code></dd>
          <dt>Resulting commit</dt><dd><code>{receipt.head_commit}</code></dd>
          <dt>Recorded</dt><dd><time dateTime={receipt.at}>{receipt.at}</time></dd></dl>
      </details>
    </div> : null}
    {error !== null && recovery !== null ? <Alert id={`${idPrefix}-error`} variant="destructive" role="alert" tabIndex={-1}>
      <p><strong>{recovery.title}</strong></p>
      <p>{recovery.step}</p>
      <details className="surla-details"><summary>Details returned by Surla</summary>
        <p>{error.message}</p>
      </details>
    </Alert> : null}
    {controls.reason !== null ? <p id={`${idPrefix}-reason`} role="status">{controls.reason}</p> : null}
    <div className="surla-field-row">
      <Button id={`${idPrefix}-refresh`} disabled={controls.pending} onClick={controls.onRefresh}>Refresh merge state</Button>
    </div>
    <p>Refresh reads the current build and approval. It does not merge or retry a merge.</p>
  </section>;
}

// mergeRecovery explains only named server refusals. An unknown failure may
// have lost its response after Git changed, so it never promises a rollback.
function mergeRecovery(name: string | null): { title: string; step: string } {
  switch (name) {
    case "dirty_worktree":
      return { title: "Tracked changes need your attention.",
        step: "Commit or set aside your tracked edits in the project folder, then refresh merge state before choosing Merge again." };
    case "detached_head":
      return { title: "Choose the branch that should receive this result.",
        step: "Check out a local branch in the project folder, then refresh merge state before choosing Merge again." };
    case "merge_conflict":
      return { title: "Git could not merge this result.",
        step: "Read the details and inspect the project’s branch, index and working tree, including any warning about restoration. Resolve the reported issue; if Git attributes, hooks or configuration require manual merging, review them first and use the manual instructions. Then refresh merge state." };
    case "not_approved":
      return { title: "This result no longer has current approval.",
        step: "Refresh the result, review its current agreement, tests and approval requirements, and approve the result before choosing Merge again." };
    case "working_copy_busy":
      return { title: "Another project operation is using the working copy.",
        step: "Wait for that operation to finish, then refresh merge state before choosing Merge again." };
    default:
      return { title: "The merge outcome could not be confirmed.",
        step: "Refresh merge state to read the current build and approval. Inspect the project branch and working tree before choosing Merge again; the request may have completed even though its response was unavailable." };
  }
}
