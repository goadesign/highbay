// Agent choices are local suggestions until the user explicitly starts workers.
// The page owns their lifetime; these helpers perform no reads or writes.
import type { InstalledAgentOption, RoleAgentChoice, TeamRoleState, TeamSnapshot } from "./types";

// WorkerRole limits browser launch choices to the generated worker roles.
export type WorkerRole = Exclude<TeamRoleState["role"], "guide">;
// WorkerChoices retains partial page drafts while discovery is pending.
export type WorkerChoices = Partial<Record<WorkerRole, RoleAgentChoice>>;

// proposeWorkerChoices suggests installed defaults without granting permissions
// or replacing an existing page draft. Saved custom model IDs remain valid.
export function proposeWorkerChoices(
  roles: readonly WorkerRole[],
  options: readonly InstalledAgentOption[],
  savedChoices: Readonly<Record<string, RoleAgentChoice>>,
  herdrMode: TeamSnapshot["herdr_mode"] | null,
): WorkerChoices {
  const choices: WorkerChoices = {};
  const available = options.filter((option) => herdrMode !== "unavailable" || option.headless_supported);
  // Builder selection precedes test-writer selection even if the caller's role
  // order differs, so a second family is preferred consistently.
  for (const role of ["builder", "test_writer"] satisfies WorkerRole[]) {
    if (!roles.includes(role)) continue;
    const saved = savedChoices[role];
    if (saved !== undefined && workerChoiceError(saved, available, herdrMode) === null) {
      choices[role] = { ...saved };
      continue;
    }
    const preferred = role === "builder" ? "codex" : "claude";
    const builderFamily = role === "test_writer" ? choices.builder?.client : undefined;
    const distinct = available.filter((option) => option.client !== builderFamily);
    const candidates = distinct.length > 0 ? distinct : available;
    const option = candidates.find((candidate) => candidate.client === preferred) ?? candidates[0];
    if (option !== undefined) {
      choices[role] = { client: option.client, program: option.program, yolo: false };
    }
  }
  return choices;
}

// workerChoicesReady checks the selected roles against current discovery and
// requires explicit background consent and supported unattended permissions.
export function workerChoicesReady(
  roles: readonly WorkerRole[],
  choices: WorkerChoices,
  options: readonly InstalledAgentOption[] | null,
  herdrMode: TeamSnapshot["herdr_mode"] | null,
  backgroundConsent: boolean,
): boolean {
  if (roles.length === 0) return true;
  if (options === null || herdrMode === null) return false;
  const headless = herdrMode === "unavailable";
  if (headless && !backgroundConsent) return false;
  return roles.every((role) => {
    const choice = choices[role];
    if (choice === undefined || workerChoiceError(choice, options, herdrMode) !== null) return false;
    const option = options.find((candidate) => candidate.client === choice.client && candidate.program === choice.program);
    return option !== undefined && (!headless ||
      option.headless_supported && (!option.yolo_supported || choice.yolo));
  });
}

// choicesForRoles sends only the selected workers, never the guide or a saved
// preference for a role this action does not need.
export function choicesForRoles(
  roles: readonly WorkerRole[],
  choices: WorkerChoices,
): Record<string, RoleAgentChoice> {
  const selected: Record<string, RoleAgentChoice> = {};
  for (const role of roles) {
    const choice = choices[role];
    if (choice !== undefined) selected[role] = { ...choice };
  }
  return selected;
}

// workerChoiceError explains a local readiness failure without rewriting input.
// Discovery models are suggestions; supported effort values and capability flags
// constrain choices. The server remains the authority when workers are started.
export function workerChoiceError(
  choice: RoleAgentChoice,
  options: readonly InstalledAgentOption[],
  herdrMode: TeamSnapshot["herdr_mode"] | null,
): string | null {
  if (!literalChoiceValue(choice.program) ||
    choice.model !== undefined && !literalChoiceValue(choice.model) ||
    choice.thinking !== undefined && !literalChoiceValue(choice.thinking)) {
    return "Use option values without surrounding whitespace, control characters, or a leading dash.";
  }
  const option = options.find((candidate) => candidate.client === choice.client && candidate.program === choice.program);
  if (option === undefined) return "This selection is unavailable in the installed agent list. Choose another installed agent or refresh installed agents.";
  if (herdrMode === "unavailable" && !option.headless_supported) {
    return "This selection is unavailable for background execution. Choose another installed agent.";
  }
  if (choice.model !== undefined && !option.model_supported) return "Use the client default model or choose an agent that supports model selection.";
  if (choice.thinking !== undefined && (!option.thinking_supported || !option.efforts.includes(choice.thinking))) {
    return "Use the client default effort setting or choose an available reasoning effort.";
  }
  if (choice.yolo && !option.yolo_supported) return "This client has no separate Highbay YOLO switch. Use configured permissions.";
  // Chorus interprets these exact Antigravity suffixes as an effort selection.
  if (choice.client === "antigravity" && choice.model !== undefined && choice.thinking !== undefined &&
    /-(low|medium|high|xhigh|max)$/.test(choice.model)) {
    return "This model name already includes reasoning effort. Use the default effort setting.";
  }
  return null;
}

// Literal option values cannot be flags or contain control delimiters. This
// matches the owner's admission rule without restricting custom model names.
function literalChoiceValue(value: string): boolean {
  return value !== "" && !value.startsWith("-") && value.trim() === value && !/\p{Cc}/u.test(value);
}
