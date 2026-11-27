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
      Merging the approved commits into the project’s current branch… Once Git changes the project,
      closing this page does not interrupt the transaction.
    </p> : null}
    {receipt !== null ? <div id={`${idPrefix}-receipt`} role="status">
      <p>Merged into <code>{receipt.branch}</code> at <code>{receipt.head_commit.slice(0, 12)}</code>
        {" on "}<time dateTime={receipt.at}>{new Date(receipt.at).toLocaleString(undefined, {
          year: "numeric", month: "short", day: "numeric", hour: "numeric", minute: "2-digit",
        })}</time>.</p>
      <p>Verification covered the approved commits before the merge, not the resulting branch. Nothing was deployed.</p>
      <details className="highbay-details" id={`${idPrefix}-receipt-details`}>
        <summary>Merge receipt</summary>
        <dl><dt>Branch</dt><dd><code>{receipt.branch}</code></dd>
          <dt>Resulting commit</dt><dd><code>{receipt.head_commit}</code></dd>
          <dt>Recorded</dt><dd><time dateTime={receipt.at}>{receipt.at}</time></dd></dl>
      </details>
    </div> : null}
    {error !== null && recovery !== null ? <Alert id={`${idPrefix}-error`} variant="destructive" role="alert" tabIndex={-1}>
      <p><strong>{recovery.title}</strong></p>
      <p>{recovery.step}</p>
      <details className="highbay-details"><summary>Details returned by Highbay</summary>
        <p>{error.message}</p>
      </details>
    </Alert> : null}
    {controls.reason !== null ? <p id={`${idPrefix}-reason`} role="status">{controls.reason}</p> : null}
    <div className="highbay-field-row">
      <Button id={`${idPrefix}-refresh`} disabled={controls.pending} onClick={controls.onRefresh}>Refresh merge state</Button>
    </div>
    <p>Refresh reads the current build and approval. It does not merge or retry a merge.</p>
  </section>;
}

// MergedBranchVerification binds a separate terminal check to the exact
// build and known test inputs. The project settings remain operator inputs.
export function MergedBranchVerification({
  id, requestRevision, suiteCommit, approvedSuite, mergedCommit, storedEvaluator,
}: {
  id: string;
  requestRevision: string;
  suiteCommit: string | null;
  approvedSuite: boolean;
  mergedCommit: string | null;
  storedEvaluator: boolean;
}) {
  const candidate = mergedCommit !== null ? shellWord(mergedCommit) : '"$(git rev-parse --verify HEAD)"';
  const suite = shellWord(suiteCommit ?? "REPLACE_WITH_SUITE_COMMIT");
  const candidateModule = storedEvaluator ? "REPLACE_WITH_CANDIDATE_MODULE" : ".";
  const binary = storedEvaluator ? "REPLACE_WITH_BINARY_NAME=REPLACE_WITH_BINARY_PACKAGE" : "SERVICE=./cmd/SERVICE";
  const suiteRepo = storedEvaluator ? "REPLACE_WITH_SUITE_REPO" : ".";
  const suiteModule = storedEvaluator ? "REPLACE_WITH_SUITE_MODULE" : "eval";
  const suiteCommand = storedEvaluator ? "REPLACE_WITH_SUITE_COMMAND" : "./gen/cmd/SERVICE-evals";
  const command = 'VERIFY_DIR="$(mktemp -d)" &&\n' + [
    "highbay verify",
    "-candidate-repo . -candidate-commit " + candidate,
    "-candidate-module " + shellWord(candidateModule) + " -binary " + shellWord(binary),
    "-suite-repo " + shellWord(suiteRepo) + " -suite-commit " + suite,
    "-suite-module " + shellWord(suiteModule) + " -suite-command " + shellWord(suiteCommand),
    "-request-revision " + shellWord(requestRevision),
    '-work-dir "$VERIFY_DIR/work" -report-dir "$VERIFY_DIR/reports" -builder-view',
  ].join(" \\\n  ");

  return <details id={id} className="highbay-details">
    <summary>Verify the merged branch from the terminal</summary>
    <p>{mergedCommit !== null
      ? "Run from the project's repository root. This template checks the resulting commit from the merge receipt."
      : "Check out the merged branch and run from its repository root. This template checks that branch's current commit."}
      {" The command creates a separate verification report."}</p>
    <p>{storedEvaluator
      ? <>Use the settings shown by <code>highbay verification show</code> to fill in the evaluator and candidate fields. Include a <code>-binary</code> flag for each candidate binary.</>
      : <>Replace <code>SERVICE</code> with your service name. Adjust the module paths if your project or tests are outside the default directories.</>}</p>
    <p>{approvedSuite ? "The suite commit comes from the saved approval."
      : suiteCommit !== null ? "The suite commit identifies the currently accepted tests for this build."
      : "Fill in the suite commit from the verification evidence you intend to rerun."}</p>
    <pre tabIndex={0}><code>{command}</code></pre>
  </details>;
}

// shellWord preserves an exact value as one literal shell argument.
function shellWord(value: string): string {
  return "'" + value.replace(/'/g, "'\\''") + "'";
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
