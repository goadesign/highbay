# How Surla Works

**[← Back to README](../README.md)** · **[Golden Path Architecture →](ARCHITECTURE_PLAYBOOK.md)** · **[CLI & MCP Reference →](REFERENCE.md)**

This page follows one change through Surla from the first command to the
merge. For each step it says what you do and see, what each agent does, and
what the server records. The [reference](REFERENCE.md) holds the exact
contract of every command, tool and field.

## Who does what

A change involves you, three coding agents and one server per project folder.
Each has its own credential, so the server always knows who is calling.

| Who or what | What it is | Trusted for |
| --- | --- | --- |
| You, the user | The person who runs `surla` in the project folder. You act on the page in your browser. | The only one who confirms a change, starts a build and approves a result. |
| The guide | The coding agent you talk to about what should change. | Writing the Story (described below) and proposing the change. Surla checks the proposal's shape, not whether it is right: you judge that when you confirm it. |
| The builder | The coding agent that writes the application's code, in its own working copy of the project (`.surla-build`). | Nothing on its word: its work counts only when Surla verifies a commit of it. |
| The test writer | The coding agent that writes the tests for a build, in its own working copy of the project. | Nothing on its word: its tests count only when Surla's check accepts them. |
| The server | The project's Surla server. `surla` starts one for each project folder. It keeps the record of what was agreed, built, accepted and approved, and it runs every check. | Deciding, from the files the agents produce, whether work is done. |
| The page | A page in your browser that the server serves; the reference calls it the views page. It talks to the server as you. | Carrying your decisions. No agent has your identity. |
| Herdr | The terminal program the agents and the server run in, one pane per agent. Its core, installed as `herdr-core` with the `hc` command, runs the terminals; the Herdr app shows them. | Running and showing the terminals. |
| Chorus | The messaging service the agents use to talk to each other. It also provides the dialog where you choose which installed agent plays each role. | Conversation only: a message never makes work count as done. |

Surla runs the code the agents write (the design at each refresh, the
builder's programs and the test writer's tests) as ordinary processes on this
machine, with your file and network access. Through the project server, Story
refreshes, starter initialization, test-report checks, live starts and verifications
share the project's compiled Go packages. Each operation still builds its outputs and
runs its checks; a cache hit is never a passed check. Code running as you can
change retained compiler inputs
and affect later results. See [project storage and cache trust](REFERENCE.md#project-state-folder)
for lifetime and cleanup.

Three more words appear on every step:

- A **Story** is a plain description of what the system does, written as Go
  code in the project's design. It names the actors and their **flows**. A flow
  is one path from a starting event to an outcome, for example "a customer
  asks for a refund under the limit and receives store credit". A flow with a
  start, at least one outcome and no open question is **finished**; any other
  flow is a draft. See the [Story language](../story/README.md).
- A **Change Card** is the server's record of one change: your request, the
  guide's analysis of it, the questions only you can answer, your answers and
  the Story it was made from.
- A **`request_revision`** is the digest of one exact version of a Change
  Card. Building, verifying and approving each name it, so none of them can
  apply to a version you did not confirm.

The table below also names the **live target**: a running copy of your
application that the server builds from a commit, so the builder can try
prompt wording before committing it (see
[Trying prompt wording on a live target](#trying-prompt-wording-on-a-live-target)).

The server decides who may call what from one table. A call by anyone else is
refused with `forbidden` before anything changes. The agents call the server
through an MCP (Model Context Protocol) connection named `surla`. You and the
agents can read the Golden Path, Surla's built-in architecture playbook,
published as the resource `surla://golden-path`.

| Caller | Connects through | What it may do |
| --- | --- | --- |
| You | The page | Confirm a change, adopt or decline a proposed revision, start or drop a build, approve a result, choose the model live targets use, stop a live target, start the agents and bring a pane forward. You read everything the page shows. |
| The guide | Its `surla` MCP connection | Create a starter project, have the Story evaluated, and propose and revise changes. It reads the changes, builds and verification runs. |
| The builder | Its `surla` MCP connection | Read its assignments, generate code, check the project, update its dependencies, run a live target and send prompt drafts to it, and ask the server to verify a commit. It reads the changes, builds and verification runs. |
| The test writer | Its `surla` MCP connection | Read its test assignments and report its tests finished. It reads the build, and the verification runs of the changes it has an assignment for. |
| The `surla` command | The server's management credential | Prepare the team, open the page and stop the server. It makes no decision about the application. |

The reference lists [every operation and who may call it](REFERENCE.md#mcp-tools--resources)
and describes [each operation](REFERENCE.md#mcp-tools).

The roles separate duties; they do not wall the agents off from each other.
All of them run as you, on one machine, and every result that depends on that
says so.

## The journey at a glance

```mermaid
flowchart TD
    A["You run surla in the project folder"] --> B["You choose an agent for each role in the Chorus dialog"]
    B --> C["You describe the change to the guide"]
    C --> D["The guide writes the Story and proposes a Change Card"]
    D --> E["You answer its questions and confirm it on the page"]
    E --> F["You press Build on the page"]
    F --> G["The test writer writes one test per finished flow and reports"]
    G --> H{"Surla checks the report"}
    H -->|"findings"| G
    H -->|"accepted at one commit"| J["Surla builds the builder's commit and runs the accepted tests against it"]
    F --> I["The builder writes the code, commits and asks Surla to verify"]
    I --> J
    J --> K["You approve the result on the page"]
    K --> L["In the project folder, you merge the builder's commit and then the tests' commit into your branch"]
```

Tool descriptions and the server's terminal name these steps by checkpoint:

| Checkpoint | Step on this page |
| --- | --- |
| `CP1` | [Agreeing on a change](#agreeing-on-a-change) |
| `CP2` | [Previewing the architecture](#previewing-the-architecture) |
| `CP3` | [Trying prompt wording on a live target](#trying-prompt-wording-on-a-live-target) |
| `CP4` | [Verifying the builder's commit](#verifying-the-builders-commit) |
| `CP5` | [Approving and merging](#approving-and-merging) |

Surla deploys nothing and merges nothing. The journey ends with you merging
the builder's commit and then the tests' commit into your branch in the project
folder.

## Starting: `surla`, the team and the page

You type `surla` in the project folder. The [README](../README.md#install)
lists what to install first; when Herdr or Chorus is missing, `surla` stops
and prints the command that installs it, or the folder to add to your
`PATH` when the program is installed in `~/.local/bin`.
Before creating project state or starting a server, it also checks that the
Git on `PATH` supports `--no-lazy-fetch` (Git 2.45 or newer). An unsupported
Git is named by its version; install a newer Git and run `surla` again. This
check works in an empty folder, before you make a Git repository.

1. **The server starts.** The first time, `surla` prints `Started a Surla project
   for this folder. Moving or renaming this folder starts a new project.`
   It starts the server in its own Herdr terminal, labelled
   `surla_project=<id>`, and tells it the private state folder it found for
   this project. That folder belongs to your account and lies outside the
   project folder. If a server for this folder already runs, `surla` attaches
   to it instead of starting a second one, when the same build of `surla`
   started it. A server another build started, such as one still running
   after you install a new `surla`, is refused with `this project's server
   was started by another build of surla; run surla stop, then surla`. If the
   server stops while starting, `surla` prints `the project server stopped
   while starting, and its last lines were:` followed by those lines. Nothing
   else was started; fix what they name and run `surla` again.
2. **The team is prepared.** `surla` starts Chorus and the Herdr app if they
   are not running, gives the project a Herdr space (the set of tabs that
   holds the project's terminals) and registers the guide. It prints
   `Team <name>.` and your next step, for example `Choose an agent for each
   role in the open Chorus dialog in Herdr.` If the team cannot be prepared,
   `surla` still opens the page, because confirming and approving need no
   agent, and then prints `the project team is not ready:` with what failed
   and the step that repairs it. When Chorus or the Herdr app could not be started, that step is `run chorus up, then run surla again` or `open Herdr, then run surla again`; otherwise **Start agents** on the page tries again.
3. **The page opens.** `surla` opens the page in your browser through a
   single-use link and prints `Surla is running for <folder> at <address>`.
   To open the page again later, run `surla` again in the same folder.
4. **You choose the agents.** The Chorus dialog lists one row per role Surla
   registered. On the first start that is the guide alone. You pick which
   installed agent plays it. The guide's pane opens in the project's Herdr
   space with Surla's first message, which points it to the Golden Path and
   to the project's changes.

You can make the same choices through a terminal and browser automation. Run
`surla --print-link` to receive a one-use page URL and expiry as JSON, with
startup notices on stderr, then open it in your controlled browser. The team
next step gives `chorus team inspect` and `chorus team launch` commands with
your actual space ID. Name each missing role's program/model explicitly in the
choices JSON; the command shares the dialog's launch backend but shows no Herdr
catalog or saved preferences. On a later build, inspect again and choose the
new worker roles. See [terminal operation](REFERENCE.md#terminal-and-browser-automation)
for the exact input and recovery.

The frame names the journey Describe → Agree → Build → Check → Approve.
Team, Story, Live target, Architecture and Diagnostics remain available as
project references. Agreed, Needs you and Next keep the current context and
decision visible; on a phone, Next moves to the bottom of the viewport.
The frame previews at most two lines of the agreed summary. **Read full
summary** opens Agree at the complete saved summary, keeping unsaved edits.

| View | What it is for |
| --- | --- |
| Describe | The project's Change Cards, most recently saved first, each with its latest saved verification run. |
| Team | Each role's pane state, its Chorus watcher (the agent's background wait for messages) and its assignment status, with **Start agents** and an action per role that brings its pane forward, such as **Show guide pane**. |
| Story | The Story of the change shown, and the guide's latest Story draft. |
| Live target | The running copy of your application that the builder tries prompts on, and the model connection it uses. |
| Agree | The Change Card: its questions, your answers, confirmation and a proposed revision. Unsaved edits stay available when you move to another stage. |
| Build | The saved build and its next step, with agreement details available below. |
| Architecture | The architecture preview of the confirmed card. |
| Check | The change’s saved verification runs and their test details. |
| Approve | The selected run’s recorded Story, finished flows and required test outcomes, excluded drafts, unknown coverage, approval requirements and verified commits. The one **Approve this result** action appears in Next only when the owner says it is available. |

A **Diagnostics** drawer shows the server's [operation diagnostics](REFERENCE.md#operation-diagnostics).
The [views page reference](REFERENCE.md#views-page) describes each view in
full. `surla stop` stops the server in order; see
[the `surla` command](REFERENCE.md#surla-serve).

## Agreeing on a change

You type your request in the guide's pane, for example "Customers can get a
refund on an order. Under $50 it is store credit at once; above that a
manager approves it."

1. **The guide writes the Story.** It calls `refresh_story`, which makes the
   guide's `.surla-draft` copy in the project folder when needed, returns that
   `draft_folder` at once, and evaluates its design (`go.mod`, `go.sum` and
   `design/`). The guide edits the Story and design only in `.surla-draft`
   (never in the project folder) and calls `refresh_story` again to evaluate
   its edits. Before drafting a new change, or when a design file changed in the
   project folder outside Surla, the guide calls `update_draft` to bring the
   project folder's design changes into `.surla-draft`. Each `to_correct` entry
   names the folder holding the file: the guide corrects its draft, or tells
   you what to correct in the project folder and waits. It calls `update_draft`
   again until none remains, then `refresh_story`. `get_story_refresh` returns the Story the
   server found: actors, finished and draft flows, steps and open questions. The
   Story view shows that draft while the guide works. Where the Story starts
   from depends on the project:
   - A starter project already has a Story, in `design/story.go`, with one
     finished flow, `prices-cookie`, and the test that flow requires. The
     guide adds your flows to `design/story.go` in `.surla-draft`.
   - In a folder with no service, the guide first creates the starter
     project with `init_project`: a folder inside the project folder holding
     the service, its Story, its test module in `eval/` and a first commit.
     The server then works in that starter folder, and so do the builder's and
     the test writer's working copies (`.surla-build` and `.surla-tests`).
   - In a project made another way, the design can import the Story language
     only when the draft's `go.mod` requires its module. When it does not,
     the failed evaluation names the step: “`In the folder that holds that
     go.mod, run: go get github.com/goadesign/surla/story@<version>, then try
     again.`” The version is the one Surla's own `go.mod` requires. The guide
     runs the command in its draft, whose `go.mod` was evaluated.
2. **The guide proposes the change.** It calls `propose_change` with your
   request, its analysis and the Story refresh it made, which a project with a
   `go.mod` requires. Surla runs no model for this step. It checks the analysis's shape,
   assigns the IDs of its questions and examples, and saves it as a Change
   Card in the project's private state folder, where it waits for you. The
   card appears in the Describe view by itself.
3. **You answer and confirm.** In Agree you read the card,
   answer each question and press **Confirm Intent & Lock Examples**. The
   server marks the card `confirmed`, records you as the one who confirmed
   it, and computes its `request_revision`. That revision is the **agreed
   revision**.

A card for the refund request holds, for example:

```text
Change req-refunds · needs confirmation

Examples
  check_01  $35 item ($38.50 total)      → store credit at once, no approval   deterministic
  check_02  $75 item ($82.00 total)      → waits for a manager's approval      deterministic
  check_03  Manager declines a $75 refund → customer sees the reason            model_judged

Questions only you can answer
  req-refunds-ambiguity-01  Does "under $50" mean the item price or the total with tax and shipping?
                            (A $48 item with $6 shipping totals $54.)
  req-refunds-ambiguity-02  At exactly $50.00, store credit or a manager's approval?
```

`deterministic` means code can check the outcome directly; `model_judged`
means checking it needs judgment. Surla cannot tell whether the analysis is
complete or correct. You decide that when you confirm the card.

The guide can revise the change at any time with `revise_change`. Before you
confirm, the revision replaces the card's content and waits for your answers.
After you confirm, it waits beside the agreed revision as a **proposed
revision**; see [Going back](#going-back).

## Previewing the architecture

The Architecture view asks the server for
`preview_architecture` on the confirmed card. The server returns the
architecture that the guide's Story refresh evaluated for the agreed revision
and that was saved with it; it reads and evaluates no file, so a later edit of
the draft or of the project does not change it. It shows what the design
declares: its
containers and the calls between them, each agent's time and tool-call
limits, and each tool's schemas, including whether a tool declares a
design-time `Confirmation` gate.

The preview does not compare the design with the Change Card. A declared
`Confirmation` gate means the runtime pauses a run for approval before it
calls the tool's method; it does not show that the service delivers the
request to the right person and continues after the answer. The
[`examples/orders`](../examples/orders/README.md) platform implements that
workflow for a manager approving refunds. To read a design without a Change
Card, run `surla preview -dir <module>`.

## Building

After confirming, you press **Build this change** in Build. The saved
agreement stays below the build under **Agreed:** and your request. Open **Agreement details** to review it or **Refine agreed
answers and examples** to edit it. The build control explains which agents
start and that, from Build on, the tests and the builder's code run on this
machine as ordinary programs, with your access to files and the network;
nothing confines them, and approval decides only what merges. **Stored
verification settings**
name a test suite of your own, an **evaluator**, at
one exact commit; you save them with `surla verification set <file>`, and
they apply when the server starts again. Building always uses the agreed
revision, including while a proposed revision waits.
The reference gives the exact contract of
[builds and assignments](REFERENCE.md#builds-and-assignments).

### What a build adds

1. **The server checks first.** It refuses the start, and saves nothing, when
   another change's open build is not yet merged or dropped
   (`previous_change_open`), when rebuilding an unfinished open build without
   confirming replacement (`unfinished_build_open`), when the revision was
   drafted before a drop (`drafted_before_drop`) or a design file at the
   build's base commit was changed outside Surla (`design_changed_outside`), when
   the card is not confirmed or the revision is not the agreed one, or when
   the project lacks a Git repository with at least one commit, a Git name and
   address, the test module `surla init` makes (`eval/` in the project folder,
   unless verification settings are stored), `.surla-build` and `.surla-tests`
   folders and `surla/build-<n>` and `surla/tests-<n>` branches that nothing
   else uses, or when another build start, an assignment read, one of the
   builder's tool calls or a test check is in progress. Each refusal says what
   is missing and the step to take; the reference lists them under
   [`start_build`](REFERENCE.md#start_build).
2. **The server saves the open build and creates any missing working copies**
   (`.surla-build` on `surla/build-<n>`, and `.surla-tests` on `surla/tests-<n>`
   when written tests are used) at the build's base commit, leaving existing
   copies where they are. The saved build records the build's number `n` and
   base commit, the agreed revision, its Story and its design, and the answers,
   examples and preserved behaviors you confirmed for it.
3. **It registers the builder and the test writer**, each with its own
   credential and working copy. The builder works in `.surla-build`; the test
   writer works in `.surla-tests`.
4. **Each worker's first assignment read joins its copy and prepares the
   required tests.** When the builder or the test writer first reads its
   assignment for a new build, Surla joins its working copy to `surla/build-<n>`
   or `surla/tests-<n>` at the build's base commit. For each finished flow of
   the Story, `get_test_writer_assignment` generates one required test into the
   working copy's test module when marking an assigned build's assignment read.
   Draft flows require none. The Build section shows the count, for example
   "3 required Story tests."
5. **It brings up the team.** The Chorus dialog opens with rows for the
   builder and the test writer. If it was already open, click **Reload team**
   first. Surla asks for the builder's pane to the right of the guide's and
   the test writer's below the builder's, and gives each Surla's first
   message.

The work belongs to the server. Closing the page does not stop it, and the
page says the start can take a few minutes. Saving the build and making the
working copies have a five-minute limit, bringing up the team has its own
45-second limit, and writing the required tests when the test writer's
assignment is marked read has its own five-minute limit. Trouble with the
Herdr window after the build is saved
leaves the build and the roles in place; press **Start agents** in the Team
view when the window is free.

With stored verification settings, the project verifies with the evaluator
those settings name. Building then adds only the builder in its `.surla-build`
working copy (it uses no `.surla-tests` working copy), and the Build section
shows `Test writer: not needed; this project uses its configured tests`. See
[`surla verification`](REFERENCE.md#surla-verification-and-surla-promotion).

### What appears in the project folder

| What | Where | Why |
| --- | --- | --- |
| The builder's working copy | `.surla-build/` inside the project folder | A linked Git working copy (`git worktree`) of the repository, where the builder writes and commits the application's code. |
| The builder's branch | `surla/build-<n>` | The numbered branch for build `n` checked out in `.surla-build`, starting at the build's base commit. |
| The test writer's working copy | `.surla-tests/` inside the project folder | A linked Git working copy (`git worktree`) of the repository, where the test writer writes its tests. |
| The tests branch | `surla/tests-<n>` | The numbered branch for build `n` checked out in `.surla-tests`, starting at the build's base commit, where Surla commits the accepted tests. |
| Three lines in the repository's local exclude file | `# Surla's working copies.`, `.surla-build/` and `.surla-tests/` in `.git/info/exclude` | Keeps the working copies out of `git status`. No tracked file of the project changes. |

Starting a build creates any missing working copy at the build's base commit
and leaves existing copies where they are. When the builder or the test writer
first reads its assignment for a new numbered build, Surla switches its copy
to `surla/build-<n>` or `surla/tests-<n>` at the build's base commit: if the
copy is on an earlier build branch of that role (`surla/build-<k>` or
`surla/tests-<k>`), Surla first commits any uncommitted files left there onto
that earlier branch with the message `Left uncommitted when change <id>
started`; if uncommitted work sits on another branch or a Git operation is in
progress, the read is refused with `copy_off_branch`.

### The test writer writes the tests

The test writer reads its assignment with `get_test_writer_assignment`: the
working folder, the design and Story as agreed at the built revision, your
answers, examples and preserved behaviors as you confirmed them, and one
required test per finished flow. It may not read the change itself, so the
assignment is how its tests learn what you agreed, and they must hold the
service to it as well as to the Story's flows. It writes the tests inside the
test module, `eval/` in its working copy, and changes nothing outside it.
Surla declares the required tests in that module's Story table,
`eval/design/story_tests.go`, which it writes when `get_test_writer_assignment`
marks an assigned build's assignment read and again at each report; the test
writer leaves that file as it is, and regenerating the
module keeps the required tests. After changing the module's design, the
test writer reports to have it generated again or runs
`surla gen -dir eval` in its working copy first. It checks
its work with `go build ./...` in `eval/`, which fails naming any required
method not yet written.
Then it calls `report_tests_finished`, including which agreed answers and
examples each scenario is stated to cover and an explanation; an empty
statement list means **Not stated**. The report returns at once with the
report's number; the build shows the test writer as **Reported**. In a
starter project the test of the starter's own flow, the method `PricesCookie`
in `eval/acceptance`, is already written and shows the form to follow.

After acceptance, Build shows **Test writer's stated coverage** against your
agreed answers and examples. Surla validates the references and checks that the
tests build and declare their required scenarios; it does not judge the
writer's coverage statements. Approve shows the same recorded statements
beside the selected run's results, even after later changes. An answer or
example without a statement says **Not stated**, which does not establish
whether it was tested.

### Surla checks the tests

Surla, not the test writer, decides whether the tests count. Right after each
report, the server checks the working copy without trusting its author (the
reference gives the check's limits under
[judging reports](REFERENCE.md#judging-reports)):

1. It checks that `.surla-tests` is on `surla/tests-<n>` with no Git operation
   in progress, and compares the copy's files with the build's base commit, so
   the test writer's own commits on `surla/tests-<n>` count too. Any changed
   file outside the test module is a finding.
2. It writes the required tests again from the Story saved with the build, so
   the test writer cannot change what is required.
3. It builds the generated test program, `gen/cmd/<name>-evals` in the test
   module. A compiler error, such as a missing
   method, is a finding.
4. It runs the program's `--describe` to list the scenarios,
   the named tests, it declares. A required scenario that is missing is a
   finding, and so is a scenario tagged `holdout`, the tag that hides a
   scenario from what the server returns (see
   [hidden scenarios](#verifying-the-builders-commit)). Before recording an
   acceptance, Surla also checks that the tip of `surla/tests-<n>` descends
   from the build's base commit without commits from other builds' branches.

The build and the listing run as ordinary processes with your file and network
access, each in its own process group that the check stops. A process that
starts its own session leaves its group and keeps running.

The verdict replaces **Reported** and marks the test writer's assignment
unread, so it reads it again.

| Verdict on the page | What happened | Who acts |
| --- | --- | --- |
| Needs changes | Surla found problems, including when building the test program hits the 10-minute limit (`The test program did not finish building within 10 minutes. Report again: the retained build cache can reuse compilation that completed. If it still does not finish, make the test module build faster, for example with fewer or smaller dependencies.`) or listing hits the 60-second limit. Build shows the saved required tests beside Surla’s findings, including the exact scenario and hook for each finished flow. Next says that the test writer corrects and reports again; the verdict alone asks for no user decision. A failed findings read retains the count and offers **Retry findings read**. | The test writer reads them in its assignment, fixes them and reports again. |
| Accepted | Surla committed exactly the checked files on `surla/tests-<n>`, with your Git identity and the message `Add the tests Surla accepted for change <id>, report <n>`. The build details show that commit. The builder's assignment is marked unread too. | The builder can verify or start a live target with the chosen model. |
| Could not run | The check stopped for a cause the test writer cannot fix, such as the 15-minute whole-check time limit, Git trouble, or dependencies that cannot be downloaded where Surla runs. The page shows the reason and your step. | You take the step; it says whether the test writer then reports again or you build again. |

An acceptance always carries the same sentence, which the page shows: "Surla
checked that these tests build into the generated test program and declare
every required scenario; it did not judge what they assert." Whether the
tests check the right things is still yours to judge.

When findings are sent back, the page names the test writer as the next
actor. **Read findings** shows them under **What Surla told the test writer**;
the test writer fixes them and reports again. After a matching check passes,
**Review result** opens that exact saved result. An earlier check stays
readable, but the builder needs to verify the current revision and tests
before you can approve.

## Trying prompt wording on a live target

When the application calls an AI model, the builder can try prompt wording
before committing it. It calls `start_live_target` with the confirmed change's
ID and a commit. The server
builds that commit and starts it as the **live target**: a
running copy of your application that only Surla started. Surla never
changes prompts in an application you started or deployed. Without stored
verification settings, the test program at that build's accepted tests commit
starts the copy. The current revision must have a build with accepted tests,
and you choose a model before the builder starts it. With stored settings,
the operator's evaluator starts it; no build is needed, but the change must
still be owned and confirmed. A refused change, tests, commit or model check
keeps the target already running and its prompt drafts.

The test program and the application it starts run on this computer as
ordinary processes, with your access to files and the network.
The test writer's accepted program receives no provider settings. When you
choose a direct provider, Surla serves that model to it through a loopback
gateway in the live executor process. The evaluator in the stored verification settings still
receives the chosen provider's settings. Running as you, these programs can
still read keys your other programs hold.

The builder then calls `draft_override`. A prompt template change
(`target_kind: "prompt_template"`) goes to the live target in memory, without
rebuilding, and the result returns the version in force. A change to a
compiled business rule (`target_kind: "compiled_rule"`, such as the $50
limit) changes nothing: the result says `requires_rebuild: true`, because the
rule needs a Go rebuild and verification, so a hard rule never moves into
prompt text. A draft lasts as long as the live target: to keep wording, the
builder edits it into the prompt's source and commits it.

In the Live target view you see the target's state and commit, the prompts it
changed (never their text), and **Stop live target**. Under **Model
connection** you choose the model that live targets started afterwards use: a
provider and model name, whose key and request settings `surla` handed to the
server from the shell where you typed it, or a running model gateway. While no
model is chosen, a hand-over picks the default model of the provider whose
settings it holds, `anthropic` first when it holds both, in the order of the
reference's [provider table](REFERENCE.md#hand_over_model_settings). The
server runs in a Herdr terminal, which has the environment of the Herdr core
rather than of your shell, so `surla` hands the settings over itself; see
[model provider settings](REFERENCE.md#model-provider-settings) and the
[live target prompt contract](REFERENCE.md#live-target-prompt-contract).

If the target ends by itself, Surla confirms its processes are stopped before
showing **Ended unexpectedly**, its commit, when it started and when Surla
observed the ending. The message says the exact cause is unavailable and
names the builder's recovery step; it exposes no log or provider error. The
notice survives page reloads until a new start is accepted, an explicit stop
clears it, or Surla restarts. A refused new start keeps the notice. The page
offers no Stop button for a target already ended. A stop whose cleanup cannot
be confirmed remains **Not confirmed stopped**, and no replacement starts.

## Verifying the builder's commit

In a project that builds with a test writer (without stored verification
settings), the test module `eval/` (`eval/design/` included) belongs to the
test writer: the builder leaves `eval/` untouched, `check_project` and
`update_dependencies` leave `eval/` out and say so, and if the Story
(`design/story.go`) or the test module's contract needs to change, the builder
asks you so the guide can draft and you can confirm a revision.

Before committing, the builder calls `check_project` on its current working
files. Surla prepares dependencies, then runs its tests without network access
in Linux containers. Each project retains its own downloaded dependencies
between calls and server restarts; tests read those inputs through a read-only
mount and receive fresh compiler and temporary directories. Every call still
checks the current files. If module or workspace checksums need repair, the
builder calls `update_dependencies` and checks again; see
[`check_project`](REFERENCE.md#check_project).

The builder commits its code in `.surla-build` on `surla/build-<n>` and calls
`verify_change` with the change, the revision from its assignment and its
commit. The server refuses before running anything when the revision has no
open, undropped build (`no_build`), when the builder's assignment is unread or
`.surla-build` is missing or not on `surla/build-<n>` (`assignment_unread`,
`working_folder_missing`, `copy_off_branch`), when the candidate commit is not
on `surla/build-<n>`, does not descend from the build's base, is the base
itself, or holds commits from another build's branch (`build_branch_mismatch`),
when a design file at the candidate's merge base with the project's commit was
changed outside Surla (`design_changed_outside`), or when Surla is using the
builder's working copy, the candidate commit's `design/` differs from the
build's agreed design, or the candidate commit shares no history with the
project's commit (`conflict`). Without stored verification settings, it also
refuses when the build's tests are not accepted (`tests_not_accepted`, naming
the test writer's state), when a commit lacks the test layout
(`no_test_layout`), when the candidate commit shares no history with the
accepted tests' commit (`conflict`, naming the user's step: build the change
again from the project's current commit, then read the new assignments and
verify again), or when the candidate commit changes hand-written files in
`eval/` (`conflict`, naming the changed files and the `git restore` / `git rm`
step, and noting on `eval/go.mod` edits that renaming the service module is
yours to do on your own branch between changes). Each refusal names who acts
next; see [`verify_change`](REFERENCE.md#verify_change).

An accepted call returns a run ID at once. The run happens in its own
**run directory**: a folder the server keeps outside the project, holding the
run's request, its lock, its result and its logs. The run continues when the
builder disconnects or the server restarts. It:

1. checks out the builder's commit and builds the application;
2. takes the accepted tests at the exact commit Surla recorded for this
   build, and builds their generated test program;
3. runs that program, which starts the builder's programs.

The builds, the test program and the builder's programs run as ordinary
processes with your file and network access, in one process group that the
run owns and stops when it ends. A process that starts its own session leaves
that group and keeps running. The test program starts with only `PATH`
and a temporary folder in its environment: no model key and nothing else of
yours. Surla's test layout plans with a scripted model during verification.

Every run records where its tests came from. For tests the test writer wrote,
the run's result, as `verify_change`, `get_verification` and
`list_verifications` return it, names the tests commit and carries this
notice: "These tests were written by a separate agent on this machine and are
not isolated from the builder." The Check view lists the change's saved runs
and shows the selected run's scenarios and outcome. For a run of the test
writer's tests it shows that notice with the result. Under **Candidate and
run details** it shows the builder's commit as "Candidate commit" and the
tests commit as "Accepted tests commit", or as "Evaluator commit" when the
run used the evaluator that stored verification settings name.

The Describe view shows each change's latest saved run, for example "Latest
saved run: passed", and says under it whether that run counts for the change
as it stands. "Matches the agreed revision and selected tests." means the run
verified the agreed revision with the tests a run of that revision would
use: the tests accepted for the build of that revision or, with stored
verification settings, the evaluator they name. Otherwise the line reads
"Earlier inputs or no current test selection." and tells you to ask the
builder for a run of the agreed revision. A run that does not match stays
readable in the Check view.

The opened change and the selected run also refresh this match when the
agreement or selected tests change. While a read is pending or fails, the
report and your unsaved edits stay visible, but approval waits for a fresh
match. Check shows **Check started** in your local time: it is the time the
server accepted the run, not a completion time. Details retain the exact
recorded timestamp.

The server returns only the builder view of a report, to you and to the
agents alike. An evaluator named in stored verification settings can declare
**hidden scenarios**: scenarios tagged `holdout`, whose names, checks and logs
never appear in what the server returns; the builder view shows only an
anonymous outcome such as `holdout-1`. The builder's programs run with your
file access, so while a run executes they could read the evaluator's files on
this machine. The test writer's tests cannot be
hidden, because Surla's check refuses the tag.

With stored verification settings, a verification run uses the evaluator those
settings name and still requires the change's open, undropped build for the
builder's working copy and branch. Outside the journey, the
[`surla verify`](REFERENCE.md#surla-verify) command runs one verification from
the command line.

## Approving and merging

Open a saved result in Approve. It shows the Story recorded for that run,
each finished flow’s required test and outcome, and draft flows excluded from
the required tests. An operator evaluator may have no recorded flow mapping;
that means coverage is unknown, even when its declared tests passed. Later
Story edits never replace a run’s recorded context. Older runs without context
say so. Passing declared tests does not establish behavior those tests do not
express.

Only you can approve. Next offers **Approve this result** when the owner’s
current review says it is available. A blocked result keeps its evidence and
explains the enabling step. A read failure retains evidence and withholds
approval until recovery. Approval stays on this stage after it is recorded.
If another check arrives while you review an earlier result, **Review latest
check** opens it. **Refresh result** keeps reviewing the selected run; saved
results and approvals retain their own evidence.
The server reloads the run's report and derives it again from its evidence,
checks that it passed and that it verified the agreed revision with the tests
accepted for that revision, and records the approval on the Change Card:
the builder's commit, the tests commit and the report. Approval merges and
deploys nothing.

The approval names two commits: the builder's commit that the run verified (on
`surla/build-<n>`), and the tests commit that Surla accepted for the revision
(on `surla/tests-<n>`, or the evaluator's commit with stored verification
settings). The recorded approval lists them under **Verified inputs and
digests** as **Builder's commit** and **Tests' commit**. Because the builder
commits in `.surla-build` rather than in the project folder, while the open
build shows **Passed** for the current agreement the page gives you the
`git merge <commit>` commands under **To take this change, run in the project
folder:** first `git merge <candidate_commit>` (verified on `surla/build-<n>`),
and then, for the test writer's tests, `git merge <tests_commit>` (verified on
`surla/tests-<n>`); with stored verification settings, the tests commit is the
evaluator's and only `git merge <candidate_commit>` is shown. Once the branch
checked out in the project folder holds those commits, the build shows
**Merged**. On Approve, Next then points to describing the next change while
your selected approval and its evidence stay readable.
Before drafting the next change, the guide calls `update_draft` (and
then `refresh_story`) so `.surla-draft` brings in the design changes merged into
the project folder. Approval is refused when the run used other tests than the ones
accepted for the revision; the builder then verifies again and you approve that
run. A recorded approval stays shown only while the same tests commit is the
accepted one: after another report or another build, it reappears only if Surla
accepts that same commit again.

Approval needs no settings. A project can store promotion settings with
`surla promotion set <file>`: a release folder, where your own release step
publishes the builder's binaries for each commit, and the binaries' names.
Like verification settings, they apply when the server starts again.
Approval then makes one more check: the files in the release folder must be
the binaries the run built. When one is missing or differs, approval is
refused, and the refusal ends with your step: "publish the binaries the run
built, whose folder the run's Proof view shows, into the release folder that
the promotion settings name, in a folder named after the commit, or remove
the promotion settings". For a completed run, the Check view shows you its
actual binary folder under **Binaries built by this run**, as selectable
code. Use that folder for the selected run; the approval error panel repeats
it. Agents never receive this path. See
[`surla promotion`](REFERENCE.md#surla-verification-and-surla-promotion) and
[`approve_merge`](REFERENCE.md#approve_merge).

A change to the agreed revision drops its approval; see
[Going back](#going-back).

## When an agent needs you

Agents stop at questions and permission prompts. The server follows each
registered role's pane in Herdr and shows, on every view of the page, one line
per role that waits for you, for example "Builder needs you: permission",
with a **Show builder pane** action that brings that pane forward. You answer
in the pane.

While the builder or the test writer has work on the open build and has an
assignment it has not read, the server sends that agent one fixed Chorus direct
message asking it to read its assignments; nothing is typed into its pane. An
agent that stays idle with that unread assignment for 90 seconds needs you too,
for example "Builder needs you: idle with an unread assignment. Show its pane
and tell it to read its assignments and to keep its Chorus watcher running."

The Team view shows each role's pane state (Not started, Starting, Working,
Waiting, Needs you, Ended, Error or Status unknown), whether its Chorus
watcher is held, and its assignment status (No assignments for the guide, and
Assignment unread, No unread assignment, No work now—when the open build needs
nothing from that role—or Assignment status unknown for a worker role). **Start
agents** opens the Chorus dialog when the guide, or a worker role that has work
on the open build (or any listed role when the server cannot read who has work),
is not running. What the server cannot observe stays
"unknown"; it never guesses. Surla never types into an agent's pane. See
[team supervision](REFERENCE.md#team-supervision).

## Going back

Any step can be taken again on the same change. The server always says which
revision a build, a run or an approval belongs to.

| What happens | What stays valid | What must be redone |
| --- | --- | --- |
| The guide revises a confirmed change | The agreed revision stays in force with its answers, build, runs and approval. The page shows the proposed revision beside it, under "Waiting for you". Refining the agreed revision is blocked until you adopt or decline. | Nothing until you decide. |
| You decline the proposed revision | Everything, exactly as it was. | Nothing. |
| You adopt the proposed revision (answer its questions, then **Adopt this revision**) | Earlier runs stay readable for their revision. | When the agreed content changes, the adopted revision has another `request_revision` and no approval. The build is marked **Earlier revision** and the builder's `verify_change` for the old revision is refused: build again. Adopting a refreshed draft with unchanged content can keep the same digest. |
| You change answers or examples of the agreed revision (**Refine Confirmed Answers & Examples**) | A submit with no change keeps everything. | A change gives another `request_revision`, drops the approval, and needs a build of that revision. |
| You build the same revision again while its open build is **Building** or **Passed** (**Build again…**, which asks **Build the agreed revision again?** with **Build again** and **Cancel**) | The agreed revision, the build's number `n` and base commit, and any live target already running. | Both assignments become unread, the test writer's work returns to Assigned without report or verdict, and its tests must be reported, checked and accepted again before verification or a new live target. |
| You build the same revision again after its open build is **Merged** (secondary **Build again…**, which asks **Build the agreed revision again?** with **Build again** and **Cancel**) | The agreed revision, commits you already merged, and any live target already running. | Starts a new numbered build from your branch as it is now, with fresh unread assignments for the same agreement; earlier reports remain readable but do not establish that the new build passed, and written tests must be reported, checked and accepted again before verification or a new live target. |
| You build a newly confirmed or adopted revision of the same change while its earlier open build is **Building** (**Build current agreed revision**, or **Build agreed revision** while a proposed revision waits; asks **Replace the earlier build with the agreed revision?** and sends `replaces`) | The build's number `n` and base commit, and any live target already running. | Both assignments become unread for the new revision, the test writer's work returns to Assigned without report or verdict, and its tests must be reported, checked and accepted again before verification or a new live target. |
| You drop an open build (**Drop…** on the change's build, or **Drop `<change_id>`’s build…** when another change's open build blocks starting a build, then **Drop change**) | Any commits already on `surla/build-<n>` and `surla/tests-<n>`. | The build becomes **Dropped**, worker tools stop with `no_build`, and the next `refresh_story` remakes `.surla-draft` from the project folder. A revision drafted before the drop has `drafted_before_drop: true` (the page shows the recovery step instead of a Build button, and `start_build` refuses with `drafted_before_drop`): the guide refreshes the Story and revises the change, and you adopt the revision. Building it (**Build current agreed revision**, or **Build agreed revision** while a proposed revision waits), or building another change drafted after the drop, then starts a new numbered build from the project folder's current commit without asking first. |
| The test writer reports again | The build. | Its earlier verdict is cleared; Surla checks the report again. |
| Surla says the candidate commit shares no history with the accepted tests' commit | Commits you already merged. | The refusal names the step: build the change again, then the workers read the new assignments and the builder verifies again. |
| Surla says the candidate commit shares no history with the project's commit | Commits you already merged. | The refusal names the step: drop the build (**Drop…**), have the guide refresh its draft and revise the change, adopt the revision, build it, and have the builder read the new assignment and make the change again. |

When the agreed revision has a build, the page says what adopting or refining
does to it, above **Adopt this revision** and, once you have edited an answer
or an example, above **Refine Confirmed Answers & Examples**: the build stays
on the earlier revision, and a build of the changed revision replaces both
assignments and clears the test writer's report and verdict, so its tests
must be reported, checked and accepted again before verification. The same
acceptance is also required for a new live target; an existing one keeps
running. With stored verification settings it says instead that a build of the changed revision
updates the builder's assignment and that verification keeps using those
settings.

After a build of the current agreement is merged, the page keeps the completed
result and suggests building another change or refining this one. The secondary
**Build again…** asks before starting a new build number from your branch as
it is now, with fresh unread assignments for the same agreement. Earlier
reports remain readable but do not establish that this new build passed. With
written tests, the test writer must report and have its tests accepted again;
with configured tests, only the builder is assigned. If the merged build is
for an earlier agreement, **Build current agreed revision** remains the primary
next step.

After a build is dropped, the page tells you to ask the guide to refresh its
draft and propose a revision from that refresh, then adopt that revision
before building. This also applies to a never-built change whose agreed
revision was drafted before another change’s build was dropped. A proposal
already waiting can predate the drop; adopting that proposal alone may not
enable Build. A fresh adoption can enable Build even when the agreement's
content and revision digest stay the same.

For a changed agreed revision, the page asks before replacing an earlier build
that is still building (sending `replaces`). After any required refresh and
adoption, a first build starts without a question, and so does any build that
follows a dropped build. For a changed agreed revision, a build that follows a
passed or merged build of the earlier revision also starts without one.
Building the same agreed revision again asks first while its build is building,
passed or merged. Each assignment stays as it was, for its revision, until you
build again or drop the build.
