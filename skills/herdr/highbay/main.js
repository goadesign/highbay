// Highbay companion: a floating window for the Highbay project of the shown
// Herdr space. It shows the project's stage and change, what needs the user,
// each role's state with a button that focuses its pane, and a button that
// opens (or starts) the project's views page. It reads only `hbay status
// --json` and Herdr's own terminal labels; it holds no Highbay credential.

import { View, div } from "gpui-kit";
import { h_flex, v_flex } from "gpui-base";
import { spaces, terminals, panes, focused, theme, exec, run, closeOverlay, props, checking } from "herdr";

// POLL_MS is how often the window asks `hbay status --json` again.
const POLL_MS = 4000;
// STATUS_TIMEOUT_MS bounds one status read so a stuck command cannot pile up.
const STATUS_TIMEOUT_MS = 10000;
// OPEN_TIMEOUT_MS bounds `hbay`, which may start the project's server first.
const OPEN_TIMEOUT_MS = 60000;
// FOLLOW_MS is how often the window checks, without running anything,
// whether the shown space now points at another folder.
const FOLLOW_MS = 500;
// HBAY_COMMAND is replaced with the installed executable path by
// `hbay setup -global` so GUI-launched Herdr finds hbay outside login PATH.
const HBAY_COMMAND = "hbay";

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
const ROLE_TITLES = { guide: "Coding agent", builder: "Builder", test_writer: "Test writer" };

// ROLE_STATES describes each TeamRoleState.state for people.
const ROLE_STATES = {
  not_started: "not started",
  ended: "ended",
  starting: "starting",
  needs_you: "needs you",
  working: "working",
  waiting: "waiting",
  error: "error",
  unknown: "status unknown",
};

// projectFolder returns the folder whose Highbay project the window shows for
// space, or null when there is none to ask about. A space on another machine
// has none: `hbay` runs on this computer. The first live terminal in the
// space labeled with a Highbay project names it; otherwise the focused
// terminal's folder when it is in this space, then the folder of the active
// tab's focused pane, of the active tab's first pane, of the space's first
// pane, and finally the space's own folder.
export function projectFolder(space, terms, paneList, focus) {
  if (!space || space.machine) return null;
  for (const term of terms ?? []) {
    const labels = term?.labels ?? {};
    if (term?.state === "live" && labels["herdr.space"] === space.id && typeof labels["chorus.project"] === "string" && labels["chorus.project"]) {
      return labels["chorus.project"];
    }
  }
  if (focus?.space === space.id && focus.cwd) return focus.cwd;
  const tabs = space.tabs ?? [];
  const activeTab = tabs.find(tab => tab.active)?.id;
  const inSpace = (paneList ?? []).filter(p => p.cwd && tabs.some(tab => tab.id === p.tab));
  const inActiveTab = inSpace.filter(p => p.tab === activeTab);
  const pane = inActiveTab.find(p => p.focused) ?? inActiveTab[0] ?? inSpace[0];
  if (pane) return pane.cwd;
  return space.folder || null;
}

// STATUS_FIELDS are the string fields every running `hbay status --json`
// answer carries; ROLE_FIELDS are those of each roles[] entry.
const STATUS_FIELDS = ["project_name", "directory", "team", "herdr_mode", "stage", "change_id", "change_title", "attention"];
const ROLE_FIELDS = ["role", "handle", "state", "attention", "watcher", "assignment"];

// parseStatus turns the result of `hbay status --json` into what the window
// shows: { kind: "running", status }, { kind: "stopped" } when no server runs
// for the folder, or { kind: "error", message } for a failed command or an
// answer missing a required field. An incomplete answer is reported, never
// filled in.
export function parseStatus(result) {
  if (!result || result.code !== 0) {
    const message = (result?.stderr || result?.stdout || "").trim();
    return { kind: "error", message: message || "hbay status failed." };
  }
  let value;
  try {
    value = JSON.parse(result.stdout);
  } catch {
    return { kind: "error", message: "hbay status printed something other than JSON." };
  }
  if (!value || typeof value !== "object" || typeof value.running !== "boolean") {
    return { kind: "error", message: "hbay status did not say whether a server is running." };
  }
  if (!value.running) return { kind: "stopped" };
  const missing = STATUS_FIELDS.find(field => typeof value[field] !== "string");
  if (missing) return { kind: "error", message: `hbay status answered without ${missing}.` };
  if (!Array.isArray(value.roles)) return { kind: "error", message: "hbay status answered without roles." };
  for (const role of value.roles) {
    const field = ROLE_FIELDS.find(name => typeof role?.[name] !== "string");
    if (field) return { kind: "error", message: `hbay status answered a role without ${field}.` };
  }
  const status = {
    projectName: value.project_name,
    directory: value.directory,
    herdrMode: value.herdr_mode,
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
// An unobserved or disconnected role is reported truthfully as status unknown.
export function roleLine(role) {
  const title = ROLE_TITLES[role.role] ?? role.role;
  const state = ROLE_STATES[role.state] ?? "status unknown";
  const why = role.state === "needs_you" && role.attention && role.attention !== "none" ? ` (${role.attention})` : "";
  return `${title}: ${state}${why}`;
}

export default class HighbayCompanion extends View {
  init(_props, cx) {
    // result is the last status read and resultFolder the folder it is for;
    // render shows it only while that folder is still the shown one.
    this.result = null;
    this.resultFolder = null;
    // trackedFolder is the shown folder the last read was started for.
    this.trackedFolder = null;
    this.reading = false;
    // opening, opened, and openError belong to the folder named in them.
    this.opening = null;
    this.opened = null;
    this.openError = null;
    // Herdr loads the mod once for `herdr-app check`; start no commands then.
    if (checking()) return;
    this.refresh(cx);
    cx.timer.every(POLL_MS, cx => this.refresh(cx));
    // A space switch reads the new folder at once rather than at the next poll.
    cx.timer.every(FOLLOW_MS, cx => {
      if (this.shownFolder() !== this.trackedFolder) this.refresh(cx);
    });
  }

  // shownFolder is the project folder of the space Herdr shows now, or null.
  shownFolder() {
    const space = spaces().find(space => space.active) ?? null;
    return projectFolder(space, terminals(), panes(), focused());
  }

  // refresh reads the shown folder's status once. A read still in progress
  // makes this call wait for it; when that read ends, a read starts for the
  // folder shown then. An answer for a folder no longer shown is dropped.
  refresh(cx) {
    const folder = this.shownFolder();
    this.trackedFolder = folder;
    if (!folder || this.reading) {
      cx.notify();
      return;
    }
    this.reading = true;
    cx.spawn(async cx => {
      let result;
      try {
        result = parseStatus(await exec(HBAY_COMMAND, ["status", "--json"], { cwd: folder, timeout: STATUS_TIMEOUT_MS }));
      } catch (err) {
        result = { kind: "error", message: `Could not run hbay: ${err?.message ?? err}` };
      }
      this.reading = false;
      if (this.shownFolder() === folder) {
        this.result = result;
        this.resultFolder = folder;
        const target = result.kind === "running" ? result.status.directory : folder;
        if (this.opened && (this.opened.folder === folder || this.opened.folder === target)) {
          if (this.opened.fresh) this.opened.fresh = false;
          else this.opened = null;
        }
        cx.notify();
      } else {
        this.refresh(cx);
      }
    });
  }

  // openPage runs `hbay` in folder, which starts the project's server when
  // needed and opens its browser window.
  openPage(cx, folder) {
    if (this.opening === folder) return;
    this.opening = folder;
    this.opened = null;
    this.openError = null;
    cx.notify();
    cx.spawn(async cx => {
      let message = "";
      try {
        const answer = await exec(HBAY_COMMAND, [], { cwd: folder, timeout: OPEN_TIMEOUT_MS });
        if (answer.code !== 0) message = (answer.stderr || answer.stdout || "hbay failed.").trim();
      } catch (err) {
        message = `Could not run hbay: ${err?.message ?? err}`;
      }
      if (this.opening === folder) this.opening = null;
      if (message) this.openError = { folder, message };
      else this.opened = { folder, fresh: true };
      cx.notify();
      this.refresh(cx);
    });
  }

  // button is a control that stops the press from dragging the window.
  button(id, label, t, onClick) {
    return div()
      .id(id)
      .flex_none()
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
    const folder = checking() ? null : this.shownFolder();
    let r;
    if (checking()) r = { kind: "loading" };
    else if (!folder) r = { kind: "none" };
    else if (this.resultFolder === folder) r = this.result;
    else r = { kind: "loading" };
    const title = r.kind === "running" && r.status.projectName ? `Highbay · ${r.status.projectName}` : "Highbay";
    const header = h_flex()
      .items_center()
      .gap(8)
      .child(div().flex_1().min_w(0).truncate().font_semibold().text_size(13).text_color(t.foreground).child(title))
      .when(Boolean(overlay), el => el.child(this.button("highbay-close", "×", t, () => closeOverlay(overlay))));
    const body = v_flex().gap(6).text_size(12).text_color(t.muted);
    if (r.kind === "loading") {
      body.child("Reading the project…");
    } else if (r.kind === "none") {
      body.child("Show a space on this computer to see its Highbay project.");
    } else if (r.kind === "error") {
      body.child(div().text_color(t.blocked).child(r.message));
    } else if (r.kind === "stopped") {
      body.child("No Highbay server is running for this folder.");
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
            .child(div().flex_1().min_w(0).truncate().child(roleLine(role)))
            .when(Boolean(terminal), el => el.child(this.button(`highbay-show-${role.role}`, "Show", t, () => run("pane.focus", { terminal })))),
        );
      }
    }
    // A running project opens from its own folder; otherwise from the shown one.
    const target = r.kind === "running" ? r.status.directory : folder;
    const canOpen = Boolean(target) && r.kind !== "loading";
    const openError = this.openError?.folder === target ? this.openError.message : "";
    const openSuccess = !openError && this.opened?.folder === target ? "Opened in browser" : "";
    const label = this.opening === target ? "Opening…" : r.kind === "running" ? "Open Highbay Window" : "Start Highbay";
    const footer = h_flex()
      .gap(8)
      .items_center()
      .when(canOpen, el => el.child(this.button("highbay-open", label, t, cx => this.openPage(cx, target))))
      .when(Boolean(openError), el => el.child(div().flex_1().min_w(0).truncate().text_size(11).text_color(t.blocked).child(openError)))
      .when(Boolean(openSuccess), el => el.child(div().flex_1().min_w(0).truncate().text_size(11).text_color(t.muted).child(openSuccess)));
    return v_flex()
      .id("highbay-companion")
      .w_full()
      .p(14)
      .gap(10)
      .bg(t.popover)
      .rounded(8)
      .child(header)
      .child(body)
      .child(footer);
  }
}
