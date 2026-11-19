// The inline worker picker presents controlled drafts. Its page owns discovery,
// persistence, launch requests, and draft preservation across navigation.
import React, { useEffect } from "react";
import { workerChoiceError } from "../agent-choices";
import type { WorkerChoices, WorkerRole } from "../agent-choices";
import type { InstalledAgentOption, RoleAgentChoice, TeamSnapshot } from "../types";
import { Alert, Button } from "./shadcn";

// AgentPickerProps supplies page-owned discovery, draft values, and actions.
export interface AgentPickerProps {
  idPrefix: string;
  roles: readonly WorkerRole[];
  options: readonly InstalledAgentOption[] | null;
  choices: WorkerChoices;
  loading: boolean;
  error: string | null;
  herdrMode: TeamSnapshot["herdr_mode"] | null;
  disabled: boolean;
  backgroundConsent: boolean;
  onBackgroundConsentChange: (value: boolean) => void;
  onChoiceChange: (role: WorkerRole, choice: RoleAgentChoice) => void;
  onRefresh: () => void;
  onLoad: () => void;
}

// Human role names remain separate from the generated wire values.
const roleLabels: Record<WorkerRole, string> = { builder: "Builder", test_writer: "Test writer" };

// AgentPickerView requests discovery on mount through the stable page callback.
// Refresh never replaces user choices, and no permission is enabled implicitly.
export function AgentPickerView({
  idPrefix, roles, options, choices, loading, error, herdrMode, disabled,
  backgroundConsent, onBackgroundConsentChange, onChoiceChange, onRefresh, onLoad,
}: AgentPickerProps) {
  useEffect(() => {
    let mounted = true;
    queueMicrotask(() => { if (mounted) onLoad(); });
    return () => { mounted = false; };
  }, [onLoad]);
  const headless = herdrMode === "unavailable";
  const installed = options ?? [];
  const supported = installed.filter((option) => !headless || option.headless_supported);
  return (
    <section id={idPrefix} className="surla-agent-picker" aria-labelledby={idPrefix + "-title"} aria-busy={loading}>
      <div className="surla-region-header">
        <h3 id={idPrefix + "-title"}>Choose workers</h3>
        <Button id={idPrefix + "-refresh"} disabled={disabled || loading} onClick={onRefresh}>
          {error !== null ? "Retry agent discovery" : "Refresh installed agents"}
        </Button>
      </div>
      {error !== null ? <Alert id={idPrefix + "-error"} variant="destructive" role="alert">
        Could not refresh installed agents. {error} Retry agent discovery to check available workers.
      </Alert> : null}
      {loading ? <p role="status">{options === null ? "Finding installed coding agents…" : "Refreshing installed coding agents…"}</p> : null}
      {options === null && !loading && error === null ? <p>Read installed agents to choose your workers.</p> : null}
      {options !== null && supported.length === 0 ? <p id={idPrefix + "-empty"}>
        {installed.length === 0
          ? "No supported coding agent is installed. Install a coding agent on this computer, then refresh installed agents."
          : "No installed agent supports background workers. Install a client with background execution support, then refresh installed agents."}
      </p> : null}
      {herdrMode === null && roles.length > 0 ? <p>Read the project team to check how workers will run.</p> : null}
      {roles.length === 0 && options !== null && !loading && error === null ? <p>No workers need to be started.</p> : null}
      {roles.map((role) => (
        <WorkerFields key={role} idPrefix={idPrefix + "-" + role} role={role}
          options={installed} choice={choices[role]} herdrMode={herdrMode}
          disabled={disabled || options === null} onChoiceChange={onChoiceChange} />
      ))}
      {headless && roles.length > 0 ? <div id={idPrefix + "-background"}>
        <p id={idPrefix + "-background-note"}>
          Herdr is unavailable. Surla will run these workers in the background on this computer,
          with your access to files and the network. They cannot ask for permission in an interactive pane.
          Enable each available permission switch above before starting workers.
        </p>
        <label htmlFor={idPrefix + "-background-consent"}>
          <input id={idPrefix + "-background-consent"} type="checkbox" checked={backgroundConsent}
            disabled={disabled} aria-describedby={idPrefix + "-background-note"}
            onChange={(event) => onBackgroundConsentChange(event.currentTarget.checked)} />{" "}
          I agree to run these workers in the background.
        </label>
      </div> : null}
    </section>
  );
}

// WorkerFields preserves unavailable selections visibly until the user replaces
// them. Capability metadata alone controls which client settings are offered.
function WorkerFields({
  idPrefix, role, options, choice, herdrMode, disabled, onChoiceChange,
}: {
  idPrefix: string;
  role: WorkerRole;
  options: readonly InstalledAgentOption[];
  choice: RoleAgentChoice | undefined;
  herdrMode: TeamSnapshot["herdr_mode"] | null;
  disabled: boolean;
  onChoiceChange: AgentPickerProps["onChoiceChange"];
}) {
  const headless = herdrMode === "unavailable";
  const option = options.find((candidate) => candidate.client === choice?.client && candidate.program === choice.program);
  const unavailable = choice !== undefined && (option === undefined || headless && !option.headless_supported);
  const choiceError = choice === undefined ? null : workerChoiceError(choice, options, herdrMode);
  const changeChoice = (next: RoleAgentChoice) => onChoiceChange(role, next);
  return (
    <fieldset id={idPrefix} disabled={disabled}>
      <legend>{roleLabels[role]}</legend>
      <div className="surla-field-row">
        <label className="surla-field-label" htmlFor={idPrefix + "-client"}>
          Coding agent
          <select id={idPrefix + "-client"} className="surla-input" value={choice === undefined ? "" : optionKey(choice)}
            aria-describedby={choiceError !== null ? idPrefix + "-choice-error" : undefined}
            onChange={(event) => {
              const selected = options.find((candidate) => optionKey(candidate) === event.currentTarget.value);
              if (selected !== undefined && (!headless || selected.headless_supported)) {
                changeChoice({ client: selected.client, program: selected.program, yolo: false });
              }
            }}>
            <option value="" disabled>Choose an installed agent</option>
            {choice !== undefined && option === undefined ? <option value={optionKey(choice)} disabled>
              {choice.client} — previous selection unavailable
            </option> : null}
            {options.map((candidate) => <option key={optionKey(candidate)} value={optionKey(candidate)}
              disabled={headless && !candidate.headless_supported}>
              {candidate.title}{options.filter((other) => other.client === candidate.client).length > 1 ? " — " + candidate.program : ""}
              {headless && !candidate.headless_supported ? " — background execution unavailable" : ""}
            </option>)}
          </select>
        </label>
        {choice !== undefined && option?.model_supported ? <label className="surla-field-label" htmlFor={idPrefix + "-model"}>
          Model
          <input id={idPrefix + "-model"} className="surla-input" type="text" list={idPrefix + "-models"}
            value={choice.model ?? ""} placeholder="Client default" disabled={unavailable}
            aria-describedby={idPrefix + "-model-note" + (choiceError !== null ? " " + idPrefix + "-choice-error" : "")}
            onChange={(event) => changeChoice({ ...choice, model: event.currentTarget.value || undefined })} />
          <datalist id={idPrefix + "-models"}>{option.models.map((model) => <option key={model} value={model} />)}</datalist>
          <span id={idPrefix + "-model-note"}>Choose a suggestion or enter a custom model ID.</span>
        </label> : null}
        {choice !== undefined && option?.thinking_supported ? <label className="surla-field-label" htmlFor={idPrefix + "-thinking"}>
          Reasoning effort
          <select id={idPrefix + "-thinking"} className="surla-input" value={choice.thinking ?? ""} disabled={unavailable}
            onChange={(event) => changeChoice({ ...choice, thinking: event.currentTarget.value || undefined })}>
            <option value="">Client default</option>
            {choice.thinking !== undefined && !option.efforts.includes(choice.thinking)
              ? <option value={choice.thinking} disabled>{choice.thinking} — unavailable</option> : null}
            {option.efforts.map((effort) => <option key={effort} value={effort}>{effort}</option>)}
          </select>
        </label> : null}
      </div>
      {choiceError !== null ? <p id={idPrefix + "-choice-error"}>{choiceError}</p> : null}
      {choice !== undefined && option !== undefined && !option.model_supported && choice.model !== undefined ? <p>
        Previous model: <code>{choice.model}</code>. This client no longer supports choosing a model.{" "}
        <Button disabled={unavailable} onClick={() => changeChoice({ ...choice, model: undefined })}>Use client default model</Button>
      </p> : null}
      {choice !== undefined && option !== undefined && !option.thinking_supported && choice.thinking !== undefined ? <p>
        Previous reasoning effort: <code>{choice.thinking}</code>. This client no longer supports choosing an effort.{" "}
        <Button disabled={unavailable} onClick={() => changeChoice({ ...choice, thinking: undefined })}>Use client default effort</Button>
      </p> : null}
      {choice !== undefined && option?.yolo_supported ? <div>
        <label htmlFor={idPrefix + "-yolo"}>
          <input id={idPrefix + "-yolo"} type="checkbox" checked={choice.yolo} disabled={unavailable}
            aria-describedby={option.yolo_effect !== undefined ? idPrefix + "-yolo-effect" : undefined}
            onChange={(event) => changeChoice({ ...choice, yolo: event.currentTarget.checked })} />{" "}
          Skip permission prompts (YOLO)
        </label>
        {option.yolo_effect !== undefined ? <p id={idPrefix + "-yolo-effect"}>{option.yolo_effect}</p> : null}
        {headless && !choice.yolo ? <p>Enable this permission switch to run this worker in the background.</p> : null}
      </div> : null}
      {choice !== undefined && option !== undefined && !option.yolo_supported ? <p>
        This client uses configured permissions; no separate Surla YOLO switch is available.
        {headless ? " Those permissions must allow unattended work." : null}
        {choice.yolo ? <> The previous permission switch is unsupported.{" "}
          <Button disabled={unavailable} onClick={() => changeChoice({ ...choice, yolo: false })}>Use configured permissions</Button>
        </> : null}
      </p> : null}
      {choice !== undefined ? <details className="surla-details">
        <summary>{roleLabels[role]} program details</summary>
        <p className="surla-live-wrap">Client: <code>{choice.client}</code><br />Program: <code>{choice.program}</code></p>
        {option === undefined && choice.model !== undefined ? <p className="surla-live-wrap">Previous model: <code>{choice.model}</code></p> : null}
        {option === undefined && choice.thinking !== undefined ? <p>Previous reasoning effort: <code>{choice.thinking}</code></p> : null}
        {option === undefined ? <p>Previous permission switch: {choice.yolo ? "enabled" : "disabled"}.</p> : null}
      </details> : null}
    </fieldset>
  );
}

// The client/program pair identifies an installed executable without collapsing
// two separately installed programs belonging to the same client family.
function optionKey(option: Pick<RoleAgentChoice, "client" | "program">): string {
  return JSON.stringify([option.client, option.program]);
}
