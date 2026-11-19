// Surla companion: a floating window for the Surla project of the shown
// Herdr space. It shows the project's stage and change, what needs the user,
// each role's state with a button that focuses its pane, and a button that
// opens (or starts) the project's views page. It reads only `surla status
// --json` and Herdr's own terminal labels; it holds no Surla credential.

import { View, div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { spaces, terminals, panes, focused, theme, exec, run, closeOverlay, props, checking } from "herdr";

// POLL_MS is how often the window asks `surla status --json` again.
const POLL_MS = 4000;
// STATUS_TIMEOUT_MS bounds one status read so a stuck command cannot pile up.
const STATUS_TIMEOUT_MS = 10000;
// OPEN_TIMEOUT_MS bounds `surla`, which may start the project's server first.
const OPEN_TIMEOUT_MS = 60000;

// STAGES names each stage of ProjectStatus.stage for people.
const STAGES = {
  describe: "Describe",
  agree: "Agree",
  build: "Build",
  check: "Check",
  approve: "Approve",
  merged: "Merged",
};

// ROLE_TITLES names each registered role for people.
const ROLE_TITLES = { guide: "Guide", builder: "Builder", test_writer: "Test writer" };

// ROLE_STATES describes each TeamRoleState.state for people.
const ROLE_STATES = {
  not_started: "not started",
  ended: "ended",
  starting: "starting",
  needs_you: "needs you",
  working: "working",
  waiting: "waiting",
  error: "error",
  unknown: "unknown",
};

// projectFolder returns the folder whose Surla project the window shows for
// space, or null when there is none to ask about. A space on another machine
// has none: `surla` runs on this computer. The first live terminal in the
// space labeled with a Surla project names it; otherwise the focused pane's
// folder when it is in this space, then the first pane's folder, then the
// space's own folder.
export function projectFolder(space, terms, paneList, focus) {
  if (!space || space.machine) return null;
  for (const term of terms ?? []) {
    const labels = term?.labels ?? {};
    if (term?.state === "live" && labels["herdr.space"] === space.id && typeof labels["chorus.project"] === "string" && labels["chorus.project"]) {
      return labels["chorus.project"];
    }
  }
  if (focus?.space === space.id && focus.cwd) return focus.cwd;
  const tabs = new Set((space.tabs ?? []).map(tab => tab.id));
  const pane = (paneList ?? []).find(p => tabs.has(p.tab) && p.cwd);
  if (pane) return pane.cwd;
  return space.folder || null;
}

// STATUS_FIELDS are the string fields every running `surla status --json`
// answer carries; ROLE_FIELDS are those of each roles[] entry.
const STATUS_FIELDS = ["project_name", "directory", "team", "herdr_mode", "stage", "change_id", "change_title", "attention"];
const ROLE_FIELDS = ["role", "handle", "state", "attention", "watcher", "assignment"];

// parseStatus turns the result of `surla status --json` into what the window
// shows: { kind: "running", status }, { kind: "stopped" } when no server runs
// for the folder, or { kind: "error", message } for a failed command or an
// answer missing a required field. An incomplete answer is reported, never
// filled in.
export function parseStatus(result) {
  if (!result || result.code !== 0) {
    const message = (result?.stderr || result?.stdout || "").trim();
    return { kind: "error", message: message || "surla status failed." };
  }
  let value;
  try {
    value = JSON.parse(result.stdout);
  } catch {
    return { kind: "error", message: "surla status printed something other than JSON." };
  }
  if (!value || typeof value !== "object" || typeof value.running !== "boolean") {
    return { kind: "error", message: "surla status did not say whether a server is running." };
  }
  if (!value.running) return { kind: "stopped" };
  const missing = STATUS_FIELDS.find(field => typeof value[field] !== "string");
  if (missing) return { kind: "error", message: `surla status answered without ${missing}.` };
  if (!Array.isArray(value.roles)) return { kind: "error", message: "surla status answered without roles." };
  for (const role of value.roles) {
    const field = ROLE_FIELDS.find(name => typeof role?.[name] !== "string");
    if (field) return { kind: "error", message: `surla status answered a role without ${field}.` };
  }
  const status = {
    projectName: value.project_name,
    directory: value.directory,
    stage: value.stage,
    changeTitle: value.change_title,
    attention: value.attention,
    roles: value.roles,
  };
  return { kind: "running", status };
}

// roleTerminal returns the address of the live terminal Herdr labels as
// handle's pane in the project at directory, or null when none is open.
export function roleTerminal(terms, directory, handle) {
  if (!directory || !handle) return null;
  const term = (terms ?? []).find(t => t?.state === "live" && t.labels?.["chorus.project"] === directory && t.labels?.["chorus.handle"] === handle);
  return term?.address ?? null;
}

// stageLine is the window's one-line summary of the project's stage and change.
export function stageLine(status) {
  const stage = STAGES[status.stage] ?? "Unknown stage";
  return status.changeTitle ? `${stage} · ${status.changeTitle}` : stage;
}

// roleLine says what one role is doing, including why it needs the user.
export function roleLine(role) {
  const title = ROLE_TITLES[role.role] ?? role.role;
  const state = ROLE_STATES[role.state] ?? "unknown";
  const why = role.state === "needs_you" && role.attention && role.attention !== "none" ? ` (${role.attention})` : "";
  return `${title}: ${state}${why}`;
}

export default class SurlaCompanion extends View {
  init(_props, cx) {
    this.result = { kind: "loading" };
    this.folder = null;
    this.reading = false;
    this.opening = false;
    this.openError = "";
    // Herdr loads the mod once for `herdr-app check`; start no commands then.
    if (checking()) return;
    this.refresh(cx);
    cx.timer.every(POLL_MS, cx => this.refresh(cx));
  }

  // shownSpace is the space this window describes: the shown one.
  shownSpace() {
    return spaces().find(space => space.active) ?? null;
  }

  // refresh reads the shown space's project status once; a read still in
  // progress makes later ticks wait for it.
  refresh(cx) {
    if (this.reading) return;
    const folder = projectFolder(this.shownSpace(), terminals(), panes(), focused());
    this.folder = folder;
    if (!folder) {
      this.result = { kind: "none" };
      cx.notify();
      return;
    }
    this.reading = true;
    cx.spawn(async cx => {
      try {
        const answer = await exec("surla", ["status", "--json"], { cwd: folder, timeout: STATUS_TIMEOUT_MS });
        this.result = parseStatus(answer);
      } catch (err) {
        this.result = { kind: "error", message: `Could not run surla: ${err?.message ?? err}` };
      } finally {
        this.reading = false;
        cx.notify();
      }
    });
  }

  // openPage runs `surla` in the project folder, which starts the project's
  // server when needed and opens its views page in the browser.
  openPage(cx) {
    if (this.opening || !this.folder) return;
    const folder = this.folder;
    this.opening = true;
    this.openError = "";
    cx.notify();
    cx.spawn(async cx => {
      try {
        const answer = await exec("surla", [], { cwd: folder, timeout: OPEN_TIMEOUT_MS });
        if (answer.code !== 0) this.openError = (answer.stderr || answer.stdout || "surla failed.").trim();
      } catch (err) {
        this.openError = `Could not run surla: ${err?.message ?? err}`;
      } finally {
        this.opening = false;
        cx.notify();
        this.refresh(cx);
      }
    });
  }

  // button is a control that stops the press from dragging the window.
  button(id, label, t, onClick) {
    return div()
      .id(id)
      .px(10)
      .py(5)
      .rounded(5)
      .bg(t.raised)
      .text_size(12)
      .text_color(t.foreground)
      .cursor_pointer()
      .hover(s => s.bg(t.hover))
      .on_mouse_down("left", (_event, cx) => cx.stop_propagation())
      .on_click((_event, cx) => onClick(cx))
      .child(label);
  }

  render(cx) {
    const t = theme();
    const overlay = props()?.overlay;
    const header = h_flex()
      .items_center()
      .gap(8)
      .child(div().flex_1().font_semibold().text_size(13).text_color(t.foreground).child(this.title()))
      .when(Boolean(overlay), el => el.child(this.button("surla-close", "×", t, () => closeOverlay(overlay))));
    const body = v_flex().gap(6).text_size(12).text_color(t.muted);
    const r = this.result;
    if (r.kind === "loading") {
      body.child("Reading the project…");
    } else if (r.kind === "none") {
      body.child("Show a space on this computer to see its Surla project.");
    } else if (r.kind === "error") {
      body.child(div().text_color(t.blocked).child(r.message));
    } else if (r.kind === "stopped") {
      body.child("No Surla server is running for this folder.");
    } else {
      const s = r.status;
      body.child(div().text_color(t.foreground).child(stageLine(s)));
      if (s.attention) body.child(div().text_color(t.blocked).child(s.attention));
      const terms = terminals();
      for (const role of s.roles) {
        const terminal = roleTerminal(terms, s.directory, role.handle);
        body.child(
          h_flex()
            .items_center()
            .gap(8)
            .child(div().flex_1().truncate().child(roleLine(role)))
            .when(Boolean(terminal), el => el.child(this.button(`surla-show-${role.role}`, "Show", t, () => run("pane.focus", { terminal })))),
        );
      }
    }
    const canOpen = Boolean(this.folder) && r.kind !== "none";
    const footer = h_flex()
      .gap(8)
      .items_center()
      .when(canOpen, el => el.child(this.button("surla-open", this.opening ? "Opening…" : r.kind === "running" ? "Open Page" : "Start Surla", t, cx => this.openPage(cx))))
      .when(Boolean(this.openError), el => el.child(div().flex_1().truncate().text_size(11).text_color(t.blocked).child(this.openError)));
    return v_flex()
      .id("surla-companion")
      .size_full()
      .p(14)
      .gap(10)
      .bg(t.popover)
      .rounded(8)
      .child(header)
      .child(body)
      .child(footer);
  }

  // title names the project when its status is known.
  title() {
    const r = this.result;
    if (r.kind === "running" && r.status.projectName) return `Surla · ${r.status.projectName}`;
    return "Surla";
  }
}
