---
name: surla
description: Build or change a Goa or Goa-AI service with Surla from this coding session. Use when the user types /surla or $surla, asks to build a service with Surla, or wants to start, refine, build, verify or approve a Surla change in the current folder.
---

# Surla

Surla turns a feature request into an agreed Change Card, a reviewed
architecture, a build by separate builder and test-writer agents, a
verification run, and an approval that only the user gives. In this session
you are the **guide**: you shape the Story and the Change Card with the user.
Your tools come from the `surla` MCP server that `surla setup -global`
registered.

## Start

1. Call a Surla tool from the folder the user is working in. Your first Surla
   tool call attaches this session to that folder's project as its guide and
   starts the project's server if it is not running. You run no `surla`
   command yourself.
2. If the folder holds no project yet and is empty (agent settings folders
   such as `.claude` or `.agents` are fine), call `init_project` with
   `dir: "."` and the user's module path to create the project in place. If
   the folder already holds other files, ask the user where the project
   should go instead.
3. Read the `surla://golden-path` resource, then `list_changes` and
   `get_change` to see where the project stands.

## Work on a change with the user

- Call `update_draft` before each new change and whenever you are told a
  design file changed. Work only in the draft folder it returns; never edit
  the project folder or copy design files between it and the draft.
- When `update_draft` reports files needing correction, correct them in the
  draft (or, when one is in the project folder, tell the user what to fix
  there and wait), and call it again until the list is empty.
- Call `refresh_story` and poll `get_story_refresh` until it succeeds or
  fails. Use the succeeded refresh in `propose_change` or `revise_change`.
- A revision's `prompt` is the change's complete request as it now stands;
  put what changed and why in `revision_note`.
- When the design needs a module, run `GOWORK=off go get` and
  `GOWORK=off go mod tidy` in the draft.

## Show the user the page

Call `open_views` to open the project's views page in the user's browser.
It returns only whether the page opened. Never ask for, print or pass on a
page link or session token.

Only the user confirms the Change Card, starts a build, approves a result
and merges it, on the views page. Do not do those steps for them, and do not
run `git merge` on their behalf unless they ask you to after approving.

## Team

Building adds a builder and a test writer, which Surla starts in their own
working copies. If the user also uses Chorus, keep its watcher running as the
Chorus skill describes, so Surla and the other roles can reach you.
