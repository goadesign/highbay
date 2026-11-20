---
name: surla
description: Build or change a Goa or Goa-AI service with Surla from this coding session. Use when the user types /surla or $surla, asks to build a service with Surla, or wants to start, refine, build, verify or approve a Surla change in the current folder.
---

# Surla

Surla turns a feature request into an agreed Change Card, a reviewed
architecture, a build by separate builder and test-writer agents, a
verification run, and an approval that only the user gives. In this session
you shape the Story and the Change Card with the user. Surla registers this
coding session under its internal `guide` role (for `surla status --json` and
Chorus), while you speak to the user in plain language without calling yourself
"the guide". Your tools come from the `surla` MCP server that
`surla setup -global` registered.

## Start

1. Call `list_changes` from the folder the user is working in, and call
   `open_views` right away so the Surla window opens in the user's browser and
   comes to the front. Your first Surla tool call attaches this session to that
   folder's project (under the internal `guide` role) and starts the project's
   server if it is not running. You never start, stop, initialize, verify or
   approve with the `surla` command, and never ask it for a page link; the one
   command you may run is the read-only `surla status --json` described under
   Team.
2. In your initial reply to the user after `open_views`, make the handoff from
   this chat to the browser window crystal-clear and warm:
   - Explicitly tell the user that **you just opened the Surla window in their
     browser**, and explain the flow in one plain sentence: they describe what
     they want here in chat, and they review options, start the build, and
     approve in that browser window.
   - Tell them how to bring the browser window back anytime: click **Open Surla
     Window** in the Herdr Surla panel (`Cmd+Alt+Shift+S` on macOS) or ask you
     here in chat (`"open the Surla window"`). Never print or guess a
     `127.0.0.1` URL (each launch uses a fresh private session).
   - If the user has not yet said what they want to build or change (for
     example, they invoked `/surla` or `$surla` alone), ask one short, plain
     question—such as `"What would you like to build?"` (or `"What would you
     like to change?"` in an existing project)—and wait. Do not ask for a Go
     module path, folder name or requirements checklist.
3. Once the user states a goal, if the folder holds no project yet and is empty
   (agent settings folders such as `.claude` or `.agents` are fine), call
   `init_project` with `dir: "."` right away to create the starter in place.
   Omit `module_path` unless the user already gave one: Surla derives a default
   Go module path from the folder name. Ask where the project should go only
   when unrelated existing files block in-place initialization.
4. Read the `surla://golden-path` resource, plus `get_change` for any existing
   Change Card, to see where the project stands.

## Work on a change with the user

- Write the user's goal straight into a Story and a Change Card instead of
  interviewing them in chat first. Put open product questions—such as target
  users, policy thresholds or edge cases—into the analysis's `ambiguities` with
  concrete options so the user answers and confirms them in the Surla browser
  window.
- Call `update_draft` before each new change and whenever you are told a
  design file changed. Work only in the draft folder it returns; never edit
  the project folder or copy design files between it and the draft.
- When `update_draft` reports files needing correction, correct them in the
  draft (or, when one is in the project folder, tell the user what to fix
  there and wait), and call it again until the list is empty.
- Call `refresh_story` and poll `get_story_refresh` until it succeeds or
  fails. Pass the succeeded refresh's `op_id` as `story_refresh_op_id` to
  `propose_change` or `revise_change`.
- A revision's `prompt` is the change's complete request as it now stands;
  put what changed and why in `revision_note`.
- When the design needs a module, run `GOWORK=off go get` and
  `GOWORK=off go mod tidy` in the draft.
- To see how Surla reads your draft, call `refresh_story` and read the result
  with `get_story_refresh`. To discard unsaved edits, delete the draft and
  refresh.
- If the builder reports that a hook reformatted a design file, run the same
  formatter in the draft, refresh and revise.

## Follow the build

- `get_build` reads a change's build: the revision built, whether each role
  read its assignment and the test writer's state. When it reports
  `drafted_before_drop` after the user dropped a build, call `refresh_story`
  and `revise_change` before the next build.
- `list_verifications` and `get_verification` read the verification runs
  started for a change and their results.

## Show the user the page

Call `open_views` whenever the user asks to open or reopen the Surla browser
window. It opens a fresh private session in the user's browser and brings it to
the front, returning only whether the window opened. Never ask for, print,
guess or pass on a `127.0.0.1` link or session token.

Only the user confirms the Change Card, starts a build, approves a result
and merges it, in the Surla browser window. Do not do those steps for them, and
do not run `git merge` on their behalf unless they ask you to after approving.

## Team

Building adds a builder and a test writer, which Surla starts in their own
working copies and connects through Chorus. After your first Surla tool call,
run `surla status --json` in the project folder: `team` names the project's
Chorus swarm and channel, and the `roles` entry whose `role` is `guide`
gives your `handle`.

When Chorus tools are available, join swarm `<team>` on channel `#<team>` as
agent `<handle>`, then keep `chorus watch <handle> -wait 24h` running in the
background as the Chorus skill describes, restarting it each time it ends.
Surla and the other roles reach you there. When Chorus is not available,
skip this and keep working through the Surla tools.
