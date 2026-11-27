// Agent choices are local suggestions until the user explicitly starts a role.
// The page owns their lifetime; these helpers perform no reads or writes.
import type { InstalledAgentOption, RoleAgentChoice, TeamRoleState } from "./types";

// WorkerRole limits build-worker launch choices to the generated worker roles.
export type WorkerRole = Exclude<TeamRoleState["role"], "guide">;
// WorkerChoices retains partial page drafts while discovery is pending.
export type WorkerChoices = Partial<Record<WorkerRole, RoleAgentChoice>>;

// proposeGuideChoice suggests a default installed coding agent for the guide
// role when no guide is running, honoring any saved preference first.
export function proposeGuideChoice(
  options: readonly InstalledAgentOption[],
  savedChoices: Readonly<Record<string, RoleAgentChoice>>,
): RoleAgentChoice | undefined {
  const saved = savedChoices.guide;
  if (saved !== undefined && roleChoiceError(saved, options) === null) {
    return { ...saved };
  }
  const preferred: RoleAgentChoice["client"][] = ["claude", "codex", "antigravity", "cursor", "gemini", "opencode"];
  for (const client of preferred) {
    const match = options.find((candidate) => candidate.client === client);
    if (match !== undefined) {
      return { client: match.client, program: match.program, yolo: false };
    }
  }
  const first = options[0];
  return first === undefined ? undefined : { client: first.client, program: first.program, yolo: false };
}

// proposeWorkerChoices suggests installed defaults without granting permissions
// or replacing an existing page draft. Saved custom model IDs remain valid.
export function proposeWorkerChoices(
  roles: readonly WorkerRole[],
  options: readonly InstalledAgentOption[],
  savedChoices: Readonly<Record<string, RoleAgentChoice>>,
): WorkerChoices {
  const choices: WorkerChoices = {};
  // Builder selection precedes test-writer selection even if the caller's role
  // order differs, so a second family is preferred consistently.
  for (const role of ["builder", "test_writer"] satisfies WorkerRole[]) {
    if (!roles.includes(role)) {
      continue;
    }
    const saved = savedChoices[role];
    if (saved !== undefined && roleChoiceError(saved, options) === null) {
      choices[role] = { ...saved };
      continue;
    }
    const preferred = role === "builder" ? "codex" : "claude";
    const builderFamily = role === "test_writer" ? choices.builder?.client : undefined;
    const distinct = options.filter((option) => option.client !== builderFamily);
    const candidates = distinct.length > 0 ? distinct : options;
    const option = candidates.find((candidate) => candidate.client === preferred) ?? candidates[0];
    if (option !== undefined) {
      choices[role] = { client: option.client, program: option.program, yolo: false };
    }
  }
  return choices;
}

// workerChoicesReady checks the selected roles against current discovery.
export function workerChoicesReady(
  roles: readonly WorkerRole[],
  choices: WorkerChoices,
  options: readonly InstalledAgentOption[] | null,
): boolean {
  if (roles.length === 0) {
    return true;
  }
  if (options === null) {
    return false;
  }
  return roles.every((role) => {
    const choice = choices[role];
    return choice !== undefined && roleChoiceError(choice, options) === null;
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
    if (choice !== undefined) {
      selected[role] = { ...choice };
    }
  }
  return selected;
}

// roleChoiceError explains a local readiness failure without rewriting input.
// Discovery models are suggestions; supported effort values and capability flags
// constrain choices. The server remains the authority when agents are started.
export function roleChoiceError(
  choice: RoleAgentChoice,
  options: readonly InstalledAgentOption[],
): string | null {
  if (
    !literalChoiceValue(choice.program) ||
    (choice.model !== undefined && !literalChoiceValue(choice.model)) ||
    (choice.thinking !== undefined && !literalChoiceValue(choice.thinking))
  ) {
    return "Use option values without surrounding whitespace, control characters, or a leading dash.";
  }
  const option = options.find(
    (candidate) => candidate.client === choice.client && candidate.program === choice.program
  );
  if (option === undefined) {
    return "This selection is unavailable in the installed agent list. Choose another installed agent or refresh installed agents.";
  }
  if (choice.model !== undefined && !option.model_supported) {
    return "Use the client default model or choose an agent that supports model selection.";
  }
  if (choice.thinking !== undefined && (!option.thinking_supported || !option.efforts.includes(choice.thinking))) {
    return "Use the client default effort setting or choose an available reasoning effort.";
  }
  if (choice.yolo && !option.yolo_supported) {
    return "This client has no separate Highbay YOLO switch. Use configured permissions.";
  }
  // Chorus interprets these exact Antigravity suffixes as an effort selection.
  if (
    choice.client === "antigravity" &&
    choice.model !== undefined &&
    choice.thinking !== undefined &&
    /-(low|medium|high|xhigh|max)$/.test(choice.model)
  ) {
    return "This model name already includes reasoning effort. Use the default effort setting.";
  }
  return null;
}

// Literal option values cannot be flags or contain control delimiters. This
// matches the owner's admission rule without restricting custom model names.
function literalChoiceValue(value: string): boolean {
  return value !== "" && !value.startsWith("-") && value.trim() === value && !/\p{Cc}/u.test(value);
}
