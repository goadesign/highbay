---
name: highbay
description: Build or change a Goa or Goa-AI service with Highbay from this coding session. Use when the user types /highbay or $highbay, asks to build a service with Highbay, or wants to start, refine, build, verify or approve a Highbay change in the current folder.
---

# Highbay

Highbay turns a feature request into an agreed Change Card, a reviewed
architecture, a build by separate builder and test-writer agents, a
verification run, and an approval that only the user gives. In this session
you shape the Story and the Change Card with the user. Highbay registers this
coding session under its internal `guide` role (its name in the project's
Chorus conversation), while you speak to the user in plain language without
calling yourself "the guide". Your tools come from the `highbay` MCP server that
`highbay setup -global` registered.

## Start

1. If the `highbay` MCP tools (`list_changes`, `open_views`) are not available
   in this session yet (for example, `highbay` was installed or moved before
   `highbay setup -global` ran for this client), run `highbay setup -global` once
   (when `highbay` is on `PATH`) and ask the user to reload MCP servers or start a
   new session. Otherwise, call `list_changes` from the folder the user is
   working in, and call `open_views` right away so Highbay opens the project's
   page in the user's default browser. Your first Highbay tool call attaches
   this session to that folder's project (under the internal `guide` role) and
   starts the project's server if it is not running. Keep the `team`,
   `channel` and `handle` that `open_views` returns: Team below uses them.
   Never run `highbay` to start, stop, initialize, verify, approve or print a
   page link.
2. Branch your initial reply to the user on the `open_views` result:
   - When `opened` is `true`: tell the user clearly that **Highbay opened the
     project's browser page in their default browser** (noting that it may open
     in a new tab or background window), and explain the flow in one plain
     sentence: they describe what they want here in chat, and they review
     options, start the build, and approve on that browser page. Tell them how
     to reopen it anytime: run `highbay` in the project folder, or ask you here
     in chat (`"open the Highbay page"`).
   - When `opened` is `false`: do **not** claim the page opened. Share the
     returned `message` and tell the user they can open it by running `highbay`
     in the project folder, or ask you to try `open_views` again.
   - Never print or guess a `127.0.0.1` URL (each launch uses a fresh private
     session).
   - If the user has not yet said what they want to build or change (for
     example, they invoked `/highbay` or `$highbay` alone), ask one short, plain
     question—such as `"What would you like to build?"` (or `"What would you
     like to change?"` in an existing project)—and wait. Do not ask for a Go
     module path, folder name or requirements checklist.
3. Once the user states a goal, if the folder holds no project yet and is empty
   (agent settings folders such as `.claude` or `.agents` are fine), call
   `init_project` with `dir: "."` right away to create the project in place.
   Omit `module_path` unless the user already gave one: Highbay derives a default
   Go module path from the folder name. Omit `worked_example` too: the project
   starts blank, ready for what the user asked to build. Pass
   `worked_example: true` only when the user asks to see Highbay's worked
   example. Ask where the project should go only
   when unrelated existing files block in-place initialization. Before the
   call, tell the user in one sentence that creating the project downloads Go
   modules and can take a few minutes the first time, and that the Highbay page
   shows its progress.
4. Read the `highbay://golden-path` resource, plus `get_change` for any existing
   Change Card, to see where the project stands.

## Work on a change with the user

- Write the user's goal straight into a Story and a Change Card instead of
  interviewing them in chat first. Put open product questions—such as target
  users, policy thresholds or edge cases—into the analysis's `ambiguities` with
  concrete options so the user answers and confirms them in the Highbay browser
  window.
- A new project's starting files (a service with no methods and one test
  that the program starts) are plumbing the user never asked about. Never
  mention or ask about them in chat or on the Change Card (`summary`,
  `ambiguities`, `examples` or `preserved_behaviors`, which must be `[]` on a
  brand-new product). The same holds for Highbay's worked example (`ask`,
  `price`, `override_prompt`, `helper` agent, `prices_cookie`) when the
  project was created with it and the user then asks for their own product:
  replace it silently in `.highbay-draft`.
- Write every Change Card field for the user, not a code reviewer: keep
  `summary` to 2–4 plain sentences describing what the product or change does
  for users and the key product defaults your draft chose. Never mention
  repository file paths (such as `design/design.go`), Highbay's `-listen` runner
  flag, or internal Goa/Goa-AI symbol names unless the user's own request is
  explicitly about an API or CLI flag.
- Set an ambiguity's `draft_option` to the option your draft already
  implements whenever another option would change the design, Story, examples
  or preserved behaviors. Never write that inside an option's text. The page
  lets the user confirm only that option; when they want another, they ask
  you, and you revise the change (`revise_change`) so the draft implements
  their choice before they confirm.
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
- To see how Highbay reads your draft, call `refresh_story` and read the result
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

Call `open_views` whenever the user asks to open or reopen the Highbay browser
page. It dispatches a fresh private session link to the user's default browser
opener and returns `opened` and `message` without exposing the URL, along with
the project's `team`, `channel` and your `handle`. Branch on `opened` before
telling the user whether the browser opened. Never ask for,
print, guess or pass on a `127.0.0.1` link or session token.

Only the user confirms the Change Card, starts a build, approves a result
and merges it, in the Highbay browser window. Do not do those steps for them, and
do not run `git merge` on their behalf unless they ask you to after approving.

## Team

Building adds a builder and a test writer, which Highbay starts in their own
working copies and connects through Chorus. The project has one Chorus
conversation, which the Highbay page shows to the user and lets them write in.
Every `open_views` result names it: `team` is the project's Chorus swarm,
`channel` is its channel (for example `#ops-8f3a1c902b4e`), and `handle`
is your name there.

When Chorus tools are available and `handle` is not empty, join swarm `<team>`
on channel `<channel>` as agent `<handle>` right after your first `open_views`
call, then keep `chorus watch <handle> -wait 24h` running in the background as
the Chorus skill describes, restarting it each time it ends. Use exactly the
returned names: another handle would not be recognized as this project's
guide. The user, Highbay and the other roles reach you there. A message the
user writes on the page arrives marked `(human)`; answer it with a Chorus
reply in the same channel, because that is where the user is reading, and
carry on the work it asks for as you would in chat.

When Chorus is not available or joining fails, skip this and keep working
through the Highbay tools: do not retry in a loop, and do not inspect, start,
stop or reconfigure Chorus or any other service on the user's computer. The
page still shows the user when the builder or the test writer needs them.
