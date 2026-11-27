// The inline agent picker presents controlled drafts for guide and build worker
// roles running in herdr-core (hc). Its page owns discovery, persistence, launch
// requests, and draft preservation across navigation.
import React, { useEffect } from "react";
import { roleChoiceError } from "../agent-choices";
import type { WorkerChoices, WorkerRole } from "../agent-choices";
import type { InstalledAgentOption, RoleAgentChoice, TeamRoleState } from "../types";
import { Alert, Button } from "./shadcn";

// AgentPickerProps supplies page-owned discovery, draft values, and actions.
export interface AgentPickerProps {
  idPrefix: string;
  roles: readonly WorkerRole[];
  options: readonly InstalledAgentOption[] | null;
  choices: WorkerChoices;
  loading: boolean;
  error: string | null;
  disabled: boolean;
  onChoiceChange: (role: WorkerRole, choice: RoleAgentChoice) => void;
  onRefresh: () => void;
  onLoad: () => void;
}

// RoleChoiceFieldsProps configures one role's client, model, reasoning effort,
// and permission switch.
export interface RoleChoiceFieldsProps {
  idPrefix: string;
  role: TeamRoleState["role"];
  legend: string;
  options: readonly InstalledAgentOption[];
  choice: RoleAgentChoice | undefined;
  disabled: boolean;
  compact?: boolean;
  onChange: (choice: RoleAgentChoice) => void;
}

// Human role names remain separate from the generated wire values.
const roleLabels: Record<TeamRoleState["role"], string> = {
  guide: "Guide",
  builder: "Builder",
  test_writer: "Test writer",
};

// AgentPickerView requests discovery on mount through the stable page callback.
// Refresh never replaces user choices, and no permission is enabled implicitly.
export function AgentPickerView({
  idPrefix,
  roles,
  options,
  choices,
  loading,
  error,
  disabled,
  onChoiceChange,
  onRefresh,
  onLoad,
}: AgentPickerProps) {
  useEffect(() => {
    let mounted = true;
    queueMicrotask(() => {
      if (mounted) {
        onLoad();
      }
    });
    return () => {
      mounted = false;
    };
  }, [onLoad]);
  const installed = options ?? [];
  return (
    <section
      id={idPrefix}
      className="highbay-agent-picker"
      aria-labelledby={idPrefix + "-title"}
      aria-busy={loading}
    >
      <div className="highbay-region-header">
        <h3 id={idPrefix + "-title"}>Choose workers</h3>
        <Button id={idPrefix + "-refresh"} disabled={disabled || loading} onClick={onRefresh}>
          {error !== null ? "Retry agent discovery" : "Refresh installed agents"}
        </Button>
      </div>
      {error !== null ? (
        <Alert id={idPrefix + "-error"} variant="destructive" role="alert">
          Could not refresh installed agents. {error} Retry agent discovery to check available workers.
        </Alert>
      ) : null}
      {loading ? (
        <p role="status">
          {options === null ? "Finding installed coding agents…" : "Refreshing installed coding agents…"}
        </p>
      ) : null}
      {options === null && !loading && error === null ? (
        <p>Read installed agents to choose your workers.</p>
      ) : null}
      {options !== null && installed.length === 0 ? (
        <p id={idPrefix + "-empty"}>
          No supported coding agent is installed. Install a coding agent on this computer, then refresh installed agents.
        </p>
      ) : null}
      {roles.length === 0 && options !== null && !loading && error === null ? (
        <p>No workers need to be started.</p>
      ) : null}
      {roles.map((role) => (
        <RoleChoiceFields
          key={role}
          idPrefix={idPrefix + "-" + role}
          role={role}
          legend={roleLabels[role]}
          options={installed}
          choice={choices[role]}
          disabled={disabled || options === null}
          onChange={(next) => onChoiceChange(role, next)}
        />
      ))}
    </section>
  );
}

// RoleChoiceFields preserves unavailable selections visibly until the user
// replaces them. Capability metadata alone controls which client settings are
// offered.
export function RoleChoiceFields({
  idPrefix,
  role,
  legend,
  options,
  choice,
  disabled,
  compact = false,
  onChange,
}: RoleChoiceFieldsProps) {
  const option = options.find(
    (candidate) => candidate.client === choice?.client && candidate.program === choice.program
  );
  const unavailable = choice !== undefined && option === undefined;
  const choiceError = choice === undefined ? null : roleChoiceError(choice, options);
  return (
    <fieldset id={idPrefix} disabled={disabled} className={compact ? "highbay-role-choice-compact" : undefined}>
      <legend>{legend}</legend>
      <div className="highbay-field-row">
        <label className="highbay-field-label" htmlFor={idPrefix + "-client"}>
          Coding agent
          <select
            id={idPrefix + "-client"}
            name={idPrefix + "-client"}
            className="highbay-input"
            value={choice === undefined ? "" : optionKey(choice)}
            aria-describedby={choiceError !== null ? idPrefix + "-choice-error" : undefined}
            onChange={(event) => {
              const selected = options.find((candidate) => optionKey(candidate) === event.currentTarget.value);
              if (selected !== undefined) {
                onChange({ client: selected.client, program: selected.program, yolo: false });
              }
            }}
          >
            <option value="" disabled>
              Choose an installed agent
            </option>
            {choice !== undefined && option === undefined ? (
              <option value={optionKey(choice)} disabled>
                {choice.client} — previous selection unavailable
              </option>
            ) : null}
            {options.map((candidate) => (
              <option key={optionKey(candidate)} value={optionKey(candidate)}>
                {candidate.title}
                {options.filter((other) => other.client === candidate.client).length > 1
                  ? " — " + candidate.program
                  : ""}
              </option>
            ))}
          </select>
        </label>
        {choice !== undefined && option?.model_supported ? (
          <label className="highbay-field-label" htmlFor={idPrefix + "-model"}>
            Model
            <input
              id={idPrefix + "-model"}
              name={idPrefix + "-model"}
              className="highbay-input"
              type="text"
              list={idPrefix + "-models"}
              value={choice.model ?? ""}
              placeholder="Client default"
              autoCorrect="off"
              autoCapitalize="none"
              spellCheck={false}
              disabled={unavailable}
              aria-describedby={
                idPrefix + "-model-note" + (choiceError !== null ? " " + idPrefix + "-choice-error" : "")
              }
              onChange={(event) => onChange({ ...choice, model: event.currentTarget.value || undefined })}
            />
            <datalist id={idPrefix + "-models"}>
              {option.models.map((model) => (
                <option key={model} value={model} />
              ))}
            </datalist>
            {!compact ? <span id={idPrefix + "-model-note"}>Choose a suggestion or enter a custom model ID.</span> : (
              <span id={idPrefix + "-model-note"} className="highbay-visually-hidden">
                Choose a suggestion or enter a custom model ID.
              </span>
            )}
          </label>
        ) : null}
        {choice !== undefined && option?.thinking_supported ? (
          <label className="highbay-field-label" htmlFor={idPrefix + "-thinking"}>
            Reasoning effort
            <select
              id={idPrefix + "-thinking"}
              name={idPrefix + "-thinking"}
              className="highbay-input"
              value={choice.thinking ?? ""}
              disabled={unavailable}
              onChange={(event) => onChange({ ...choice, thinking: event.currentTarget.value || undefined })}
            >
              <option value="">Client default</option>
              {choice.thinking !== undefined && !option.efforts.includes(choice.thinking) ? (
                <option value={choice.thinking} disabled>
                  {choice.thinking} — unavailable
                </option>
              ) : null}
              {option.efforts.map((effort) => (
                <option key={effort} value={effort}>
                  {effort}
                </option>
              ))}
            </select>
          </label>
        ) : null}
      </div>
      {choiceError !== null ? <p id={idPrefix + "-choice-error"}>{choiceError}</p> : null}
      {choice !== undefined && option !== undefined && !option.model_supported && choice.model !== undefined ? (
        <p>
          Previous model: <code>{choice.model}</code>. This client no longer supports choosing a model.{" "}
          <Button disabled={unavailable} onClick={() => onChange({ ...choice, model: undefined })}>
            Use client default model
          </Button>
        </p>
      ) : null}
      {choice !== undefined && option !== undefined && !option.thinking_supported && choice.thinking !== undefined ? (
        <p>
          Previous reasoning effort: <code>{choice.thinking}</code>. This client no longer supports choosing an effort.{" "}
          <Button disabled={unavailable} onClick={() => onChange({ ...choice, thinking: undefined })}>
            Use client default effort
          </Button>
        </p>
      ) : null}
      {choice !== undefined && option?.yolo_supported ? (
        <div>
          <label htmlFor={idPrefix + "-yolo"}>
            <input
              id={idPrefix + "-yolo"}
              name={idPrefix + "-yolo"}
              type="checkbox"
              checked={choice.yolo}
              disabled={unavailable}
              aria-describedby={
                !compact
                  ? idPrefix + "-trust-note" + (option.yolo_effect !== undefined ? " " + idPrefix + "-yolo-effect" : "")
                  : option.yolo_effect !== undefined
                  ? idPrefix + "-yolo-effect"
                  : undefined
              }
              onChange={(event) => onChange({ ...choice, yolo: event.currentTarget.checked })}
            />{" "}
            Skip permission prompts (YOLO)
            <span className="highbay-visually-hidden"> for {roleLabels[role]}</span>
          </label>
          {option.yolo_effect !== undefined && !compact ? <p id={idPrefix + "-yolo-effect"}>{option.yolo_effect}</p> : null}
          {option.yolo_effect !== undefined && compact ? (
            <span id={idPrefix + "-yolo-effect"} className="highbay-visually-hidden">
              {option.yolo_effect}
            </span>
          ) : null}
          {!compact ? (
            <p id={idPrefix + "-trust-note"}>
              Highbay sets missing folder trust for supported coding agents. If a folder is explicitly untrusted, review that setting before starting.
            </p>
          ) : null}
        </div>
      ) : null}
      {choice !== undefined && option !== undefined && !option.yolo_supported && !compact ? (
        <p>
          This client uses configured permissions; no separate Highbay YOLO switch is available.
          {choice.yolo ? (
            <>
              {" "}
              The previous permission switch is unsupported.{" "}
              <Button disabled={unavailable} onClick={() => onChange({ ...choice, yolo: false })}>
                Use configured permissions
              </Button>
            </>
          ) : null}
        </p>
      ) : null}
      {choice !== undefined && !compact ? (
        <details className="highbay-details">
          <summary>{roleLabels[role]} program details</summary>
          <p className="highbay-live-wrap">
            Client: <code>{choice.client}</code>
            <br />
            Program: <code>{choice.program}</code>
          </p>
          {option === undefined && choice.model !== undefined ? (
            <p className="highbay-live-wrap">
              Previous model: <code>{choice.model}</code>
            </p>
          ) : null}
          {option === undefined && choice.thinking !== undefined ? (
            <p>
              Previous reasoning effort: <code>{choice.thinking}</code>
            </p>
          ) : null}
          {option === undefined ? <p>Previous permission switch: {choice.yolo ? "enabled" : "disabled"}.</p> : null}
        </details>
      ) : null}
    </fieldset>
  );
}

// The client/program pair identifies an installed executable without collapsing
// two separately installed programs belonging to the same client family.
function optionKey(option: Pick<RoleAgentChoice, "client" | "program">): string {
  return JSON.stringify([option.client, option.program]);
}
