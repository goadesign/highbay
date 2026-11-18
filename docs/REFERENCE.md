# Surla Reference

**[← Back to README](../README.md)** · **[← How Surla Works](HOW_IT_WORKS.md)** · **[← Golden Path Architecture](ARCHITECTURE_PLAYBOOK.md)**

---

This page is for a user or an agent who needs one exact contract: a command,
an operation, who may call it, what it returns and how it refuses.
[How Surla Works](HOW_IT_WORKS.md) explains the journey step by step.

## Terminology at a Glance

| Term | Meaning |
| :--- | :--- |
| **The user** | The person who runs `surla` in a project folder. Only the user confirms a change, starts a build and approves a result, all on the views page: the page the server serves to the user's browser, which the README and How Surla Works call the page. |
| **The server** | The project's Surla server: one per project folder, started by `surla`. It keeps the Change Cards, builds, verification runs and approvals, and it serves the operations below over HTTP and MCP. |
| **Herdr** | The terminal program the server and the agents run in. Its core service is the Herdr core (the commands `herdr-core` and `hc`); its window is the Herdr app (`herdr-app`). A *Herdr space* is the set of tabs that holds one project's terminals. |
| **Chorus** | The messaging service the agents use to talk to each other. Its dialog in Herdr, **the Chorus dialog**, is where the user picks which installed agent app and model play each role. The list of roles the dialog shows is the *roster*. An agent's *watcher* is its background wait for Chorus messages. |
| **The guide** | The agent the user talks to. It writes the Story and proposes and revises changes. |
| **The builder** | The agent that writes the application's code, tries prompt wording on a live target and asks the server to verify its commit. |
| **The test writer** | The agent that writes one test per finished flow, in its own working copy of the project, and reports when they are finished. |
| **Story** | A description, in the project's Go design, of what the system does, written with the [`story`](../story/README.md) language. |
| **Flow** | One path through a Story: who does what, step by step, ending in an outcome. A flow with a start, an outcome and no open question is *finished*; each finished flow requires one test. |
| **Change Card** | The saved record of one change: the request, the guide's summary, its questions and the user's answers, examples, preserved behaviors, the Story and design it was made from, and any approval. |
| **`request_revision`** | The SHA-256 hex digest of a Change Card's content, including its design source and Story. Previews, builds, verification runs and approvals name the revision they are for, so editing the agreed answers or examples, or adopting another revision, leaves them attached to the earlier revision. |
| **Agreed revision** | The confirmed revision of a change, the one *in force* (`in_force: true` on builds and assignments). It stays in force while the guide's proposed revision waits beside it. |
| **Proposed revision** | A revision the guide saved beside an agreed change. The user adopts or declines it on the views page. |
| **Build** | The user's decision to build one agreed revision as the project's one open build (numbered `n`, with `taking` state `building`, `passed`, `merged` or `dropped`). It saves that revision's Story, design, base commit and confirmed answers, examples and preserved behaviors, and gives the builder and the test writer their assignments. |
| **The build working copies** | Linked Git working copies of the project at `.surla-build` on `surla/build-<n>` (where the builder works and commits) and `.surla-tests` on `surla/tests-<n>` (where the test writer works and where Surla commits accepted tests). |
| **Evaluator** | A test program that follows the [evaluator suite command contract](#evaluator-suite-command-contract): it declares its scenarios, runs them against the application's binaries and prints a report. Surla generates the evaluator of a Surla test module, such as the one the test writer works in, from that module's tests; stored verification settings name an evaluator the user maintains. |
| **Scenario** | One named test in an evaluator, such as `prices_tea`. A report gives each scenario's outcome: passed, failed, not run or a harness error. |
| **Tests commit** | The commit at which Surla accepted a build's tests, or the evaluator commit named in stored verification settings. Verification runs those tests. |
| **Stored verification settings** | An evaluator the user stores with `surla verification set`. With them, every change verifies with that evaluator and builds add no test writer. |
| **Live target** | A running copy of the application, built from a commit by the server, on which the builder tries prompt wording. Surla never changes an application the user runs or deployed. |
| **Hidden scenario** | A scenario whose name, inputs, checks and logs are never shown to the builder. An evaluator marks it with the tag `holdout`; the builder sees it only as `holdout-N`. Tests written by the test writer may not use the tag. |
| **Run directory** | The private folder that holds one verification run: its request, its state, its report digest and its full output (`executor.log`). |
| **`CP1` – `CP5`** | The checkpoint labels used in operation descriptions and the server's status lines: **CP1** agreeing the change, **CP2** previewing the architecture, **CP3** trying prompt wording on a live target, **CP4** verification, and **CP5** approval. |
| **`input_binding`** | For one tool an agent in the application uses: its generated input schemas and the fields the compiler marks as injected. It describes the declared choices, not what the running application installed. |

---

## Prerequisites

| Need | Why | How to get it |
| --- | --- | --- |
| **macOS or Linux** | Surla runs the code the agents write (the design at each refresh, candidate builds, the candidate service, test listing, and the test program) as ordinary processes with your file and network access. Verification gives the test writer's accepted test program only `PATH` and `TMPDIR`; a live target also gives it a target key and a model gateway, without provider settings (see [`verify_change`](#verify_change) and [`start_live_target`](#start_live_target)). With stored verification settings, the operator's evaluator receives provider settings. Running as you, these programs can still read keys your other programs hold. `check_project` runs the project's tests in Linux containers (see **Docker** below). | — |

| **Go 1.27.3 or later** | `surla` is a Go program, and the project's checks use Go. Surla runs Goa with each module's own versions, so no `goa` binary is needed. | [go.dev/dl](https://go.dev/dl/) |
| **Surla** | The command itself. | `go install github.com/goadesign/surla/cmd/surla@latest` |
| **Herdr core** (`herdr-core` and `hc`) | The server and every agent run in Herdr terminals. | `curl -fsSL https://releases.herdr.dev/core/install.sh \| sh` ([herdr.dev/core](https://herdr.dev/core/)) |
| **Herdr app** (`herdr-app`) | The window that shows the team's panes. | `curl -fsSL https://releases.herdr.dev/app/install.sh \| sh` ([herdr.dev](https://herdr.dev/)) |
| **Chorus v0.12.0 or later** | Agent messaging and the Chorus dialog. | `curl -fsSL https://raw.githubusercontent.com/goadesign/chorus/main/scripts/install.sh \| sh`; update with `chorus update -yes` |
| **A coding agent** that the Chorus dialog or terminal command can start | An agent app plays each role: the user picks one per role in the dialog or supplies terminal choices. | — |
| **Git 2.45 or newer**, with a name and address | Startup requires support for `--no-lazy-fetch`, so Surla's Git reads do not download missing objects. Every build needs the project in a Git repository with a first commit and a Git name and address, with or without stored verification settings, and so does verification, which needs the build. Without stored verification settings, live targets need the repository too. Surla records accepted tests, and any uncommitted files a working copy holds when it joins a new build, as commits under your Git identity. | Install Git 2.45 or newer on `PATH`; `git config --global user.name …` and `user.email …` configure its identity. |
| **Docker** (Docker Desktop, or another local engine on a `unix://` socket) | `check_project` and `update_dependencies` run Go in Linux containers. The first check pulls `golang:1.27.3`. | [docker.com](https://www.docker.com/) |
| **Temporal CLI** | Only to run the Orders evaluator, which starts a local Temporal server for its scenarios: when Surla verifies the Orders project or starts a live target of it, when `surla verify` runs that evaluator, and in the full-suite tests that run it (the `examples/orders` acceptance suite and the `e2e` tests of `./internal/verify` and `./cmd/surla`). | `brew install temporal` |

Before it creates any state, `surla` checks for `herdr-core`, `herdr-app` and
`chorus` on the path and names the install command of the one that is
missing. When that program is in `~/.local/bin`, where the installers put it,
it says instead `<program> is installed in <folder>, which is not on PATH;
add <folder> to PATH, then run surla again`. It then checks that Chorus is
v0.12.0 or later (`chorus update -yes` updates it), that `hc` is on the path
or in `~/.local/bin`, and, for ordinary startup, that the browser opener (`open` on macOS or
`xdg-open` on Linux) is on the path. `--print-link` skips that opener check. A
missing opener is named without an install command. Surla then probes the Git
on `PATH` with `git --no-lazy-fetch version`, without reading a repository.
If that option is unknown, startup names the installed `git --version` line
and says `Install Git 2.45 or newer and run surla again.` Missing Git has its
own refusal. No project state, server, window or agent starts on these
refusals. Git builds with the capability backported are accepted regardless
of their version label.

---

## Terminal and browser automation

Run `surla --print-link` in the project folder to start or attach and receive one
JSON object on stdout: `{ "url": "...", "expires_at": "..." }`. Startup notices,
team instructions and the nonsecret origin go to stderr. This route does not
require or call `open`/`xdg-open`; ordinary `surla` keeps opening the browser.
The URL grants user-page access to whoever exchanges it first within two
minutes. Avoid shared terminal/log captures and open it in the browser you
control. After use, expiry or page reload, repeat the command for a fresh link.
Server/auth/link-validation failure prints no link and exits nonzero. A team-only
failure can still print a usable link while exiting nonzero with its reason on
stderr: user decisions on the page do not require an agent.

The shared team next step names the dialog and copyable Chorus commands with
the actual space ID. Run the printed `chorus team inspect --space ...` to see
roles, then `chorus team launch --space ... --choices choices.json`, or
`--choices -` to read the same JSON from stdin. Choices are keyed by role:

```json
{"guide":{"client":"codex","program":"codex","model":"gpt-6.1-sol","thinking":null,"yolo":false}}
```

All fields are required; null model/thinking use client defaults. Name the
program and model explicitly: the command has no Herdr agent catalog or saved
preferences. The dialog retains those features. The client family identifies
its existing flags and connection support; program can name a wrapper. Every
missing role needs a choice; running roles may be omitted or repeated with
matching settings. Success prints `{ "tab": "...", "created": 1, "reused": 0 }`.
A refusal prints a reason and exits nonzero; inspect and retry after partial
failure to reuse completed panes. See `chorus team launch --help` and the
[Chorus launcher reference](https://github.com/goadesign/chorus/blob/main/skills/herdr/chorus-launcher/README.md#choose-roles-from-a-terminal)
for the owning contract. Surla requires Chorus v0.12.0 or newer.

## Commands

`surla help`, `surla -h` and `surla --help` print the command list to standard
output and exit 0. `surla help <command>` and `surla <command> -h` print that
command's flags to standard error and exit 0. Every command refuses
unexpected positional arguments: `surla stop` with `surla: stop takes no
arguments`, `surla verification` and `surla promotion` with their usage line
(`surla: usage: surla verification set <file> | show | remove`), and the
others with `surla: unexpected arguments [...]`. An unknown command, such as
`surla build`, answers `surla: unknown command "build"; run "surla help" to
list the commands`.

<a id="surla-serve"></a>
### `surla` and `surla stop`

**Input:** `surla`, run in a project folder. **Who acts:** the command starts
the folder's server, or attaches to the one already running, then prepares the
team and opens the views page. **What the user sees**, in order:

```text
Created a new, empty Surla project. Moving or renaming this folder starts a new project.
Team shop-3f9c1a2b4d5e.
Choose an agent for each role in the open Chorus dialog in Herdr.
Surla is running for /path/to/shop at http://127.0.0.1:52011
```

The first line appears only when the folder had no project. The second names
the team, after the folder and the first 12 characters of the project's
identifier. The third is the user's next step, chosen by the server:

| Chorus dialog | Step printed |
| --- | --- |
| Opened for this project | `Choose an agent for each role in the open Chorus dialog in Herdr.` |
| Already open | `If the open Chorus dialog is for this project, click "Reload team", then choose the agents. If it is for another project, close it, then start the agents again (Start agents on the views page, or run surla).` |
| Not needed | `Every listed role is running.` |

Then the browser opens the views page, and the last line prints. When the
browser cannot be opened, `surla` stops with `open project views: …` instead
of the last line; the server and the team keep running. When the team cannot
be prepared, `surla` prints no team lines but still opens the views page,
because confirming and approving there need no agent, prints the last line,
and then fails with `surla: the project team is not ready: ` followed by
what failed and the step that repairs it (see
[`ensure_team`](#ensure_team-management)). When Chorus or the Herdr app could not be started, that step is `run chorus up, then run surla again` or `open Herdr, then run surla again`; otherwise **Start agents** on the views
page tries again.

<a id="model-provider-settings"></a>

**Model provider settings.** Once the server answers for the project, and
before the team step, `surla` hands it the request settings of each model
provider whose key is set in the shell where `surla` runs
([`hand_over_model_settings`](#hand_over_model_settings)), and, before the
team lines, prints one line naming them, never a value, for example:

```text
Model settings held by the project server: ANTHROPIC_API_KEY, ANTHROPIC_BASE_URL (from this shell); not sent: ANTHROPIC_ADMIN_KEY. Live targets use anthropic/claude-sonnet-4-6; change it on the views page. surla stop makes the project server forget them.
```

A shell without a key sends nothing and keeps what an earlier `surla` handed
over, marked `(from an earlier surla)`. A family variable without its key is
named as not sent, such as `ANTHROPIC_BASE_URL (ANTHROPIC_API_KEY is not
set)`. With nothing held and nothing left unsent, no line prints. A refused
hand-over stops `surla` before the team step; a value longer than 4,096
characters is refused with that limit named.

`surla` leaves its own environment unchanged and gives it unchanged to
Herdr's programs (`hc`, `herdr-app`). When `surla` is what starts the Herdr
core, the core keeps that shell's environment, provider variables included,
for as long as it runs, and every terminal it opens inherits it, the agents'
included, exactly as if you had started Herdr in that shell; `surla stop` does
not change that. Chorus and the browser opener receive the environment
without any `ANTHROPIC_` or `OPENAI_` variable. Programs you started from that
shell keep what they inherited.

**The team.** Once the server answers, `surla` starts the Chorus service
(`chorus up`) and then the Herdr app when either is not running. The server
owns the team: its stable Chorus name, each role's handle and credential, the
Herdr space, and the roster the Chorus dialog reads. Before any build, the
roster holds only the guide; a build adds the builder and, unless
verification settings are stored, the test writer. When the guide, or a worker
role that has work on the open build (or any listed role when the assignment
read fails), has no agent running or its pane holds only an idle shell, Surla
focuses this project's space and opens the Chorus dialog, where the user picks
an agent app and a model for each role. A worker role without work stays in the
roster, and when every role that has work is running, no dialog opens. The
guide's pane is on the left of the team's tab; the builder's pane
is to its right, and the test writer's below the builder's. Starting again
reuses the guide and the saved space. If the space was closed, the server
adopts the one local space that matches its team name and folder, or creates
one; when several match, it refuses with their count, and the user closes the
duplicates. Team preparation needs no Git repository; startup still requires
the Git executable and capability listed under [Prerequisites](#prerequisites).

<a id="idle-shell"></a>

**Idle shell detection.** Surla treats a pane's foreground program as an *idle
shell* when both hold:

- Its executable name, with leading dashes removed, is `sh`, `bash`, `zsh`,
  `fish`, `dash`, `ksh`, `csh`, `tcsh`, `nu`, `pwsh`, `xonsh` or `elvish`, or
  the name of the user's own `$SHELL`.
- Neither of the first two arguments after the program is `-c` or a
  single-dash flag group containing `c`, such as `-lic`.

A shell started with such a command flag counts as running. A live pane that
reports no foreground program also counts as running.

<a id="project-state-folder"></a>

**The project and its state folder.** A project is a folder, with or without
Git. Surla resolves symlinks in the path and uses the nearest project record
in that folder or its parents, so running `surla` inside a project the guide
created with `init_project` finds the original project. Moving or renaming the
folder starts a different, empty project. Each project's private state lives
outside the folder, in
`<UserConfigDir>/surla/operator/project-<digest>/`, where `<UserConfigDir>` is
the user's configuration folder (on macOS, `~/Library/Application Support`).
It holds the owner's account identity, one saved `127.0.0.1` port, the Change
Cards and builds (`cards/`), verification runs (`runs/`), reports
(`reports/`), the Go build cache (`build-cache/`), Docker dependency inputs
(`check-modcache/`), agent credentials
(`agents/`), stored settings, and the running server's record. A new project
saves a free port that no other project of this account saved, running or not,
and keeps it. No caller chooses an
identity, address, token or storage path.

Through the project server, Story evaluation and generation during refreshes
and initialization,
test-report checks, live starts and verifications share this project's compiled
Go packages in `build-cache/`. Every operation still processes its current
inputs and runs its checks; a cache hit is never a passed check. Live starts
and verifications still check out the selected commits, build new output
binaries and measure their digests, and verification runs its test program.
Scratch, staging, checkouts, outputs and
evidence stay in each operation's work directory; reports are published in
the project's shared `reports/` folder. The cache survives Stop, server restart
and pruning individual runs. Go maintains its cache entries. Use it on a local
filesystem on one machine; sharing it across machines or a network filesystem
is unsupported. When none of these compiler operations is running, you can delete
`build-cache/`; the next operation recreates it and builds cold.

The retained packages are trusted compiler inputs, like the Go module cache,
toolchain, executable search path and Surla's files. Go checks a cached file's
existence and recorded size; it does not check compiled contents against the
selected source. Code running as you can plant a compiled package that a later
build links into the candidate or test program. Its report can bind a binary
the selected source would not produce; the binary digest does not authenticate
that correspondence. Deleting
this cache forces recompilation but does not authenticate the other inputs.

Before starting the server or accepting `surla verification set` or
`surla promotion set`, Surla asks Git for the project's enclosing working tree
so private files stay outside it. Each read has a 30-second allowance. A read
that times out stops the operation, names `git rev-parse --show-toplevel`, and
asks you to check that Git can read the workspace before retrying the command.
A folder without a Git repository remains a valid project folder.

`surla` finds that folder from the shell where it runs and starts the server
with it. The server never looks the folder up itself, because Herdr starts it
with the environment of the Herdr core, not the environment of your shell.
For the same reason, a variable the server reads, such as a Docker setting,
must be in the Herdr core's environment. A model provider's settings are the
exception: `surla` hands them over from your shell (see
[Model provider settings](#model-provider-settings)), and the server removes
every `ANTHROPIC_` and `OPENAI_` variable it inherits from the Herdr core. The server
refuses a state folder that is not private to your account, holds no project,
belongs to another project folder, lies inside the project folder, or is not
named by its physical path.

When `surla`, `surla stop` or a settings command cannot use a project's
record or state folder, it names them and the repair. `<record>` is the
`project.json` in the project's state folder, and the folder that holds
every project's state folder is `<UserConfigDir>/surla/operator`:

| What cannot be used | The error |
| --- | --- |
| The record cannot be read, for example because it is not a private file of this account | `the record of the Surla project in <project folder> cannot be read: <record>: <cause>; make it a regular file of this account that only this account can read and write (chmod 600 <record>, and chown it to this account if another account owns it), then run surla again` |
| The record is damaged | `the record of the Surla project in <project folder> is damaged: <record>: <cause>; restore it from a backup and run surla again, or move the folder <state folder> out of <UserConfigDir>/surla/operator and run surla in <project folder> to start that project folder again as a new, empty Surla project` |
| `<UserConfigDir>/surla/operator` exists but is not a folder of this account that only this account can open; it is checked before any record is read | `prepare operator storage root <folder>: <cause>; make it a folder of this account that only this account can open (chmod 700 <folder>, and chown -R it to this account if another account owns it), then run surla again` |
| A project's state folder is closed to this account or owned by another, or a new project cannot list the folder that holds them | `the Surla state folder <folder> cannot be used: <cause>; make it a folder of this account that only this account can open (chmod 700 <folder>, and chown -R it to this account if another account owns it), then run surla again` |
| The project's state folder cannot be created | `project state folder <folder> could not be created: <cause>; make <UserConfigDir>/surla/operator a folder this account can write, then run surla again` |

**Starting, attaching and failures.** A start lock serializes simultaneous
commands. A running server is attached to only after it proves the project's
identity and reports the build it started from, the SHA-256 of its
executable, which must be that of the `surla` attaching. A server another
build started, such as the one still running after a new `surla` is
installed, is refused with `surla: this project's server was started by
another build of surla; run surla stop, then surla`; `surla stop` stops a
server of any build. A record left by a server that died is stale and is
removed by the next start. The server runs in its own Herdr terminal, labeled
`surla_project=<project id>`, which outlives the shell that ran `surla`. That
terminal shows the server's status lines: the address, the Change Card folder
and, for each checkpoint, whether it is on. The `CP4` line says whether
verification uses the evaluator in stored verification settings or the tests
accepted for each change's build.

Before it starts the server's terminal, `surla` checks that the saved port is
free. When it is taken, `surla` starts nothing and names what holds it: the
running server of another Surla project that saved the same port, with
`surla: project port 52011 is held by the Surla server of the project in
<folder>, which saved the same port, so only one of the two can run at a
time; no server was started; run surla stop in that folder, then run surla
here`, or otherwise another program, with `surla: another program is
listening on 127.0.0.1:52011, the address saved for this project; no server
was started; stop that program, then run surla (lsof -nP -iTCP:52011
-sTCP:LISTEN names it)`. When the server's terminal ends before the server
answers, for example because it cannot read its stored settings, or because
another program took the port after that check, `surla` reads what that
terminal printed, removes the terminal, and shows the last lines (at most 20
lines and 4 KiB, with control characters removed):

```text
surla: the project server stopped while starting, and its last lines were:
  surla: project port 52011 is occupied: listen tcp 127.0.0.1:52011: bind: address already in use; stop the program listening on it, then run surla
```

If those lines cannot be read, the error says so and keeps the original
failure. A server whose terminal still runs but has not answered within the
command's deadline keeps its terminal, and the error names it so you can read
the server's output there. A taken port leaves the saved port and credentials
in place.

**`surla stop`.** Asks the server to shut down in order and waits until it has
released the project, then prints `Surla stopped.`, or `Surla is stopped.`
when no server was running. Before its listener closes, the server revokes its
management credential and withdraws its running record. Interrupt,
termination and hangup signals cause the same shutdown. A killed server is
recovered by the next `surla`.

**Credentials and browser access.** Each server creates a management
credential in memory and publishes it in its private running record after
binding its port; `surla` and `surla stop` read it there for each request,
use the literal local address without a proxy, refuse redirects, and never
print it. Management can check the project's identity, prepare the team,
stop the server, mint a browser link and hand over the shell's model provider
settings; it cannot confirm, build or approve.
Agent credentials survive restarts and are removed with their agent; agent
connection settings and rosters name only their own credential files.

`surla` opens only the saved views address with a single-use link in the
address fragment. The link expires after two minutes and is exchanged once
for a browser session (see [Views page](#views-page)). Every response
disables MIME sniffing, and every request must use the saved host and, when
present, the server's own origin.

All processes run as the same operating system account. File permissions keep
other accounts out, not other programs of that account: a deliberate program
could read another role's credential or run `surla` to obtain a browser link.
The roles are a separation of duties, not isolation from a malicious agent.

### `surla init`

Creates a starter project in `<dir>`: a Goa and Goa-AI service with an agent
and a `goa.design/model` architecture, and a test module in `<dir>/eval`. It
resolves dependencies, generates each module's code with the generator
[`surla gen`](#surla-gen) uses, runs `go vet ./...` and
`go test ./...` in both modules, and prints the commands that make the first
commit and start `surla` in the project. It builds the project in protected
temporary storage on the destination's file system and moves it to `<dir>` in
one step only when everything succeeded; a failure or interrupt leaves `<dir>`
absent, and it never replaces an existing entry.

- `-dir <path>`: the folder to create; it must not exist *(required)*.
- `-module <path>`: the service's Go module path, such as `example.com/shop`
  *(required)*. Its last element names the service and must be a lowercase Go
  identifier (`^[a-z][a-z0-9]*$`, not a Go keyword or `main`).
- `-surla-version <version>`: the exact semantic version of
  `github.com/goadesign/surla` that `eval/go.mod` requires. It defaults to the
  version the `surla` binary was built from and is required for a `(devel)` or
  `+dirty` build; `latest` and branch names are refused.

**What the starter holds.** `cmd/<name>` runs a `helper` agent that prices
items through a tool and reaches a model through `-model-gateway <host:port>`;
it registers the prompt `<name>.helper.system`.

`design/story.go` declares the service's Story with one finished flow,
`prices-cookie`. The service's `go.mod` requires the Story language's module,
`github.com/goadesign/surla/story`, at the version Surla itself requires.

`eval/` is the test module. Its suite declares three scenarios, all tagged
`repair` (`prices_tea`, `unknown_item` and `prompt_override_reaches_model`),
and its Story table, `eval/design/story_tests.go`, adds `prices_cookie`, the
test the Story's finished flow requires; `surla init` writes the table from
`design/story.go`. The module runs these scenarios against a scripted model
and implements the
[live target prompt contract](#live-target-prompt-contract). Its hand-written
parts are `eval/design` (its own copy of the service's public contract and
the scenarios, apart from the Story table), `eval/acceptance` (the
scenarios' implementation and the program's entry points) and
`eval/scripted` (the scripted model); its
program, `eval/gen/cmd/<name>-evals`, is generated with the rest of
`eval/gen/`. `eval/acceptance` also holds the test the Story's finished flow
requires, the method `PricesCookie`, so the first build of a change needs
only the tests of the flows the guide adds. This test module is the layout
builds and verification rely on.

### `surla verification` and `surla promotion`

Each takes `set <file>`, `show` or `remove` and works with or without a
running server. `set` reads a JSON file outside the project folder and its
Git working tree, refuses unknown fields and trailing data, resolves relative
paths against the file's folder, and stores a private copy; a refused file
leaves the earlier settings in place. `show` prints the stored settings;
`remove` deletes them. The server reads them when it starts, so a change takes
effect after `surla stop` and `surla`.

**Verification settings** name an evaluator Surla runs instead of tests
written by the test writer:

| Field | Meaning |
| --- | --- |
| `candidate_repo`, `candidate_module` | The repository and module folder of the application. |
| `binaries` | Binary name to package path, such as `{"orders": "./cmd/orders"}`. |
| `suite_repo`, `suite_commit`, `suite_module`, `suite_command` | The evaluator's repository, full commit ID, module folder and program package. |
| `timeout` | A positive duration for one run, such as `"30m"`. |

With stored verification settings, every change verifies with that evaluator,
builds add only the builder, and live targets run that evaluator. Without
them, verification and live targets run the tests Surla accepted for the
change's current build, alongside the candidate's own programs. Runs and
reports always go to the project's state folder.

**Promotion settings** are optional: approval needs none. When they are
stored, each approval also checks the release binaries the user published:

| Field | Meaning |
| --- | --- |
| `release_dir` | A folder outside the project folder and its Git working tree. Publish each `<release_dir>/<candidate_commit>/` complete, in one rename, and do not edit it afterwards. |
| `binaries` | Distinct plain names of the release binaries in that folder. |

Approval hashes those binaries again in `<release_dir>/<candidate_commit>/`,
through one folder handle. A missing, non-regular or escaping file, or one
that differs from the binary the run verified, prevents approval; the
refusal names no folder path and ends with the step "publish the binaries
the run built, whose folder the run's Proof view shows, into the release
folder that the promotion settings name, in a folder named after the commit,
or remove the promotion settings". The Check view shows the selected run's
actual source folder under **Binaries built by this run**, as selectable code;
the approval error panel repeats it. Without promotion settings, an approval
records no release binaries. A stored file the server cannot accept, such
as one with any other field (`suite_id`), stops the server while it starts;
the shown error ends by naming
`surla promotion show` to see the settings, `surla promotion set <file>` to
store them again and `surla promotion remove` to remove them, then `surla`.
Approval never merges or deploys.

### `surla preview`

Reads a design module into a snapshot (`go.mod`, `go.sum` and non-test `.go`
files, never following symlinks; run `go mod tidy` first if `go.sum` is
missing), evaluates it, and prints the architecture as
indented JSON to standard output: `system_name`, `declarations`, C4
`containers` and `edges`, `agent_policies`, `tools`, and `stories` when the
design declares Stories. Then it writes one
summary line to standard error, such as
`surla: "orders": 3 container(s), 2 boundary edge(s), 1 agent policy(s), 4 tool contract(s)`.
It needs no server, no Change Card and no model key. Interrupt and termination
signals cancel it; it removes its temporary folders before exiting, or names
one it could not remove.

- `-dir <path>`: the module's root, holding `go.mod`, `go.sum` and `design/`
  (default `.`).

### `surla diff`

Evaluates two design modules, as `surla preview` does, at their own
dependency versions, and prints the structural `DiffResult` as JSON:
declarations added, removed and changed, dependency version changes and a
summary. It gives no compatibility verdict.

- `--old-dir <path>`: the earlier module *(required)*.
- `--new-dir <path>`: the later module *(required)*.

### `surla gen`

Regenerates a design module's `gen/` from its `design/` and
replaces `gen/` in one atomic rename, so `gen/` is never missing or
half-written. Generated subfolders the design does not produce disappear,
and top-level files in `gen/` that Surla did not generate are kept. On success
it prints `gen/ updated`, the files added, changed and removed, and a reminder
that it did not build the application against the regenerated code; after
the swap, problems are only warnings. Before the swap, any failure or
interrupt leaves `gen/` untouched and exits non-zero. It never changes
`go.mod` or `go.sum`, and says to run `go mod tidy` when generated code
imports a module `go.mod` lacks. Leftover `.surla-gen-*` folders from an
interrupted run are listed, never deleted, because another run's staging
folder looks the same.

- `-dir <path>`: the module's root (default `.`).

| The module | What generation also writes |
| --- | --- |
| Declares agents | `AGENTS_QUICKSTART.md`, Goa-AI's guide to the design's agents and tools. Surla replaces its parts that name commands to run with steps that use Surla's operations, and replaces its evaluation section when the design declares no evaluation suite. |
| Is a Surla test module: its path is `<service module>/eval`, its design declares exactly one evaluation suite, and it has an `acceptance` package | The program `gen/cmd/<service>-evals/main.go`, built from the generated tests and the `acceptance` package's `Name`, `Hooks`, `New`, `Live` and `LiveThroughGateway`. A missing scenario method then fails to compile. Every run declares the environment `scripted-model`. `AGENTS_QUICKSTART.md` gains a section naming those five names. |

Generation needs `goa.design/goa/v3` `v3.35.1-0.20261103205041-35e789e8f9b1`
or later; with an older Goa the build fails and the error names the Goa
version in use. The named-section rewrite of `AGENTS_QUICKSTART.md` needs
`goa.design/goa-ai` `v0.90.1-0.20261110011046-f32bd628f2bf` or later. A design
with gRPC transports is refused, because `surla gen` runs Goa's generator
without protoc; generate it with the module's own `go run goa.design/goa/v3/cmd/goa gen`.
`generate_code` writes the same files. A Surla test module's Story table,
`design/story_tests.go`, is a source file of its design, so regenerating the
module keeps the tests the table requires (see
[builds and assignments](#builds-and-assignments)).

### `surla verify`

An operator tool that checks a commit without a server. It clones the
committed application and evaluator into a private work folder, builds the
application's binaries and the evaluator's program, runs
the evaluator, writes the full report to `<report-dir>/<digest>.json`, and
prints the builder view of the report (hidden scenarios shown only as
`holdout-N`) to standard output. Then it writes one summary line to standard
error, such as
`surla: verification report did not pass: 1/3 scenarios passed; failed: 1, not run: 1, harness errors: 0; not passing: "refund_over_limit", "slow_refund" (report 4f2c…)`.
A report that did not pass is written and printed before the command exits
non-zero; a setup or build failure prints no report.

| Flag | Meaning |
| --- | --- |
| `-candidate-repo <path\|url>` *(required)* | Repository holding the application. |
| `-candidate-commit <sha>` *(required)* | Full commit ID to build. |
| `-candidate-module <dir>` | Module folder inside the repository (default `.`). |
| `-binary <name=package>` *(repeatable, at least one)* | Binary name and package path, `.` or `./…`, such as `orders=./cmd/orders`. |
| `-suite-repo <path\|url>` *(required)* | Repository holding the evaluator. |
| `-suite-commit <sha>` *(required)* | Full commit ID of the evaluator. |
| `-suite-module <dir>` | Evaluator module folder (default `.`). |
| `-suite-command <package>` *(required)* | The evaluator program's package, such as `./cmd/orders_acceptance-evals`. |
| `-request-revision <digest>` *(required)* | The `request_revision` recorded in the report. Without a Change Card, any label works if `surla approve` gets the same one. |
| `-work-dir <path>` *(required)* | A missing or empty operator-owned folder for this operation's checkouts, scratch, outputs, evidence and Go build cache. Suite evidence can name hidden scenarios: keep this folder separate from builders' working copies and do not show it to builders. Programs running as you retain your file access. Relative paths resolve from the caller's directory. |
| `-report-dir <path>` *(required)* | Where the full report is written; keep it from the builder when scenarios are hidden. |
| `-timeout <duration>` | Limit for the run (default `0`, no limit). A stopped run prints any partial report the evaluator saved and exits non-zero; a partial report can pass. |
| `-builder-view` | Default `true`. `-builder-view=false` prints the full report and names hidden scenarios in the summary. Failures go to standard error in either view; do not show them to the builder. |

Failures during checkout, standalone dependency downloads and toolchain
lookups, and the evaluator protocol name the failed operation, its process
status when present, and the private evidence file. Their raw standard output
and standard error remain under `<work-dir>/evidence/`, including after the
command ends, so the operator can inspect the named file and retry. Workspace
preparation diagnostics are discarded. Compiler failures still include bounded
build-output excerpts.

#### Evaluator suite command contract

An evaluator program implements this contract with
[`suite.Main(suite.Command{...})`](../verify/suite/suite.go). Surla's own test
layout generates it; an evaluator named in stored verification settings
implements it itself:

```go
func main() {
    suite.Main(suite.Command{
        Environment:    "sim",
        Binaries:       []string{"orders", "orders-worker"},
        MaxConcurrency: 4,
        Declare:        declare,
        Suite:          newSuite,
    })
}
```

1. **`--describe`.** The program prints its declaration and exits 0, without
   candidate binaries:
   ```json
   {
     "id": "orders_acceptance",
     "environment": "sim",
     "scenarios": [
       {"id": "store_credit_within_limit", "tags": ["repair"]},
       {"id": "other_customers_order_refused", "tags": ["holdout"]}
     ]
   }
   ```
   A scenario tagged `holdout` is hidden in every builder view.
2. **`--artifact <name>=<path> ...`.** One flag per bound binary. The program
   checks the flags against `Binaries`, pins each binary into a read-only
   store with [`candidate.Pin`](../verify/candidate/candidate.go), runs the
   scenarios, and prints a `goa.design/goa-ai/eval.Report`. Each scenario
   starts the application with
   [`candidate.Exec`](../verify/candidate/candidate.go) and
   attaches the evidence of every binary it used, such as
   `pinned["orders"].Evidence("orders")`, which `surla verify` compares with the
   digest it measured; missing or different evidence makes the scenario a
   `harness_error`. The builder view then says in one fixed sentence which
   check failed; the names and digests the scenario reported stay in the
   saved report. Exit codes: `0` all passed; `1` the suite ran and a
   scenario failed; `2` the suite could not run or finish cleanly. The program
   also accepts `--scenario <id>`, or `--tag <tag>`, and `--max-concurrency <n>`
   for direct runs.
3. **`--live --artifact <name>=<path> ...`** *(optional)*. Starts the
   application for a live target and is never used by verification. The
   program reads the live key from `SURLA_LIVE_TARGET_KEY`, calls
   `Command.Live`, prints one line `{"url":"http://127.0.0.1:<port>"}`, and
   serves until interrupted or until the target ends. A program without
   `Live` exits 2 with `this evaluator suite does not support --live`.

[`examples/orders-eval/cmd/orders_acceptance-evals/main.go`](../examples/orders-eval/cmd/orders_acceptance-evals/main.go)
and [`examples/orders-eval/acceptance/hooks.go`](../examples/orders-eval/acceptance/hooks.go)
are a complete example.

### `surla approve`

An operator tool that checks a saved full report against explicit promotion
facts and prints a JSON summary (`report_digest`, `binding`, `counts`). It
exits 0 when the report passed and matches every flag, and 1 otherwise. It
records nothing, contacts no server, and merges and deploys nothing.

| Flag *(all required)* | Meaning |
| --- | --- |
| `-report <path>` | A full report written by `surla verify`. |
| `-candidate-commit <sha>` | The commit being promoted. |
| `-request-revision <digest>` | The revision the report must name. |
| `-suite-id <id>`, `-suite-commit <sha>`, `-environment <env>` | The evaluator and environment the report must name. |
| `-artifact <name=sha256>` *(repeatable)* | Each binary's 64-hex-digit SHA-256 digest. |

### `surla version`

Prints the module version the binary was built from, or `(devel)`.

---

<a id="mcp-tools--resources"></a>
## Callers and refusals

The server serves every operation as an HTTP route and, for agents, most of
them as MCP tools at `POST /mcp`, on its `127.0.0.1` address. Each request
carries a credential, and the credential fixes the caller's kind:

| Caller | Credential | What it is trusted with |
| --- | --- | --- |
| The user | A browser session from the views page | Agreeing changes, building, approving, the live target's model, and the team's panes |
| The guide | Its agent credential | Writing the Story and proposing and revising changes, and creating the project |
| The builder | Its agent credential | Code, checks, live targets and prompt drafts, and asking for verification |
| The test writer | Its agent credential | Reading its test assignments and reporting them finished |
| Management | The server's management credential, used by `surla` and `surla stop` | The server and its team, never a change |

One table decides which caller may call each operation. It governs HTTP
calls, MCP calls and MCP discovery, so an agent's tool list shows only what
its role may call. Every resource belongs to the project's owner, the
operating system account that created the project; the user and the agents
all act for that owner.

| Operation | User | Guide | Builder | Test writer | Management | Offered as |
| --- | :---: | :---: | :---: | :---: | :---: | --- |
| [`list_changes`](#list_changes) | ✓ | ✓ | ✓ | | | MCP tool and HTTP |
| [`wait_for_changes`](#wait_for_changes) | ✓ | | | | | HTTP |
| [`get_change`](#get_change) | ✓ | ✓ | ✓ | | | MCP tool and HTTP |
| [`propose_change`](#propose_change) | | ✓ | | | | MCP tool and HTTP |
| [`revise_change`](#revise_change) | | ✓ | | | | MCP tool and HTTP |
| [`confirm_intent`](#confirm_intent) | ✓ | | | | | MCP tool and HTTP |
| [`decline_revision`](#decline_revision) | ✓ | | | | | MCP tool and HTTP |
| [`refresh_story`](#refresh_story) | | ✓ | | | | MCP tool and HTTP |
| [`get_story_refresh`](#get_story_refresh) | ✓ | ✓ | ✓ | | | MCP tool and HTTP |
| [`update_draft`](#update_draft) | | ✓ | | | | MCP tool and HTTP |
| [`surla://golden-path`](#mcp-resources) (`golden_path`) | ✓ | ✓ | ✓ | ✓ | | MCP resource and HTTP |
| [`init_project`](#init_project) | | ✓ | | | | MCP tool and HTTP |
| [`generate_code`](#generate_code) | | | ✓ | | | MCP tool and HTTP |
| [`check_project`](#check_project) | | | ✓ | | | MCP tool and HTTP |
| [`update_dependencies`](#update_dependencies) | | | ✓ | | | MCP tool and HTTP |
| [`read_check_output`](#read_check_output) | ✓ | ✓ | ✓ | | | MCP tool and HTTP |
| [`preview_architecture`](#preview_architecture) | ✓ | ✓ | ✓ | | | MCP tool and HTTP |
| [`diff`](#diff) | ✓ | ✓ | ✓ | | | MCP tool and HTTP |
| [`start_build`](#start_build) | ✓ | | | | | HTTP |
| [`drop_build`](#drop_build) | ✓ | | | | | HTTP |
| [`get_build`](#get_build) | ✓ | ✓ | ✓ | ✓ | | MCP tool and HTTP |
| [`get_test_findings`](#get_test_findings) | ✓ | | | | | HTTP |
| [`list_builder_assignments`](#list_builder_assignments-and-get_builder_assignment) | | | ✓ | | | MCP tool and HTTP |
| [`get_builder_assignment`](#list_builder_assignments-and-get_builder_assignment) | | | ✓ | | | MCP tool and HTTP |
| [`list_test_writer_assignments`](#list_test_writer_assignments-and-get_test_writer_assignment) | | | | ✓ | | MCP tool and HTTP |
| [`get_test_writer_assignment`](#list_test_writer_assignments-and-get_test_writer_assignment) | | | | ✓ | | MCP tool and HTTP |
| [`report_tests_finished`](#report_tests_finished) | | | | ✓ | | MCP tool and HTTP |
| [`start_live_target`](#start_live_target) | | | ✓ | | | MCP tool and HTTP |
| [`stop_live_target`](#stop_live_target) | ✓ | | ✓ | | | MCP tool and HTTP |
| [`get_live_target`](#get_live_target) | ✓ | | ✓ | | | MCP tool and HTTP |
| [`draft_override`](#draft_override) | | | ✓ | | | MCP tool and HTTP |
| [`configure_model`](#configure_model) | ✓ | | | | | MCP tool and HTTP |
| [`get_model_connection`](#get_model_connection) | ✓ | | | | | HTTP |
| [`hand_over_model_settings`](#hand_over_model_settings) | | | | | ✓ | HTTP |
| [`verify_change`](#verify_change) | | | ✓ | | | MCP tool and HTTP |
| [`get_verification`](#get_verification) | ✓ | ✓ | ✓ | ✓ | | MCP tool and HTTP |
| [`review_verification`](#review_verification) | ✓ | | | | | MCP tool and HTTP |
| [`list_verifications`](#list_verifications) | ✓ | ✓ | ✓ | ✓ | | MCP tool and HTTP |
| [`approve_merge`](#approve_merge) | ✓ | | | | | MCP tool and HTTP |
| [`list_traces`](#list_traces) | ✓ | ✓ | ✓ | | | MCP tool and HTTP |
| [`get_team`](#get_team) | ✓ | | | | | HTTP |
| [`show_team_pane`](#show_team_pane) | ✓ | | | | | HTTP |
| [`start_team_agents`](#start_team_agents) | ✓ | | | | | HTTP |
| `identity` (`GET /project/identity`) | | | | | ✓ | HTTP |
| [`ensure_team`](#ensure_team-management) (`POST /project/team`) | | | | | ✓ | HTTP |
| `stop` (`POST /project/stop`) | | | | | ✓ | HTTP |
| `browser_link` (`POST /project/browser-link`) | | | | | ✓ | HTTP |

The user's browser session sends HTTP only, so the user-only MCP tools
(`confirm_intent`, `decline_revision`, `configure_model`, `review_verification`,
`approve_merge`)
appear in no agent's tool list. `surla` and `surla stop` call the management
operations. The views page exchanges its single-use link with
`POST /project/browser-session`, which needs no credential because the link
is one.

### How a call is refused

A call refused for its credential, its caller or its fields changes nothing,
and so does a refusal made before the operation's work starts, such as an
unknown `change_id` or a stale `request_revision`. Each operation's entry
says what a later refusal may leave. For example, `start_live_target` stops
the running target only once it has accepted the change, tests, commit and model,
so a start refused for any of them, its repository or its folder leaves the running target and its
prompt drafts in place, while one whose build or start then fails has already
stopped it. `update_not_confirmed` means the live target may or may not have
applied the text. Refusals take these shapes:

| Cause | HTTP | MCP |
| --- | --- | --- |
| No credential, or one the server does not hold (for example after a restart) | `401` with the message `open the project views with surla; session or credential is no longer valid` | The same `401` on `POST /mcp` |
| The caller's kind may not call the operation | `403` `forbidden`, message `caller may not call <operation>` | A tool error result (`isError: true`) with the same message |
| A field the route does not declare, at any depth, or a structurally invalid value | `400`, such as `decode_payload` naming the field (`json: unknown field "id"`), `missing_field` or `invalid_pattern` | A tool error starting with `invalid arguments:` that names the field |
| A named domain refusal, listed with each operation | The status the route declares, such as `404` `not_found`, `409` `conflict` or `422` `unprocessable` | A tool error result with the same message |
| An unexpected failure | `500`, with `fault: true` | A JSON-RPC error when the server marks the failure as a fault; otherwise a tool error result |

The messages in the first two rows are those of the operations agents and the
views page call. The management operations answer the same statuses with
messages of their own.

An HTTP error body is Goa's error result:

```json
{"name": "conflict", "id": "…", "message": "…", "temporary": false, "timeout": false, "fault": false}
```

`name` is the refusal's name. `message` says what went wrong. Many messages
end with the step that recovers and who takes it; each operation's entry
below lists its named refusals and when they happen.
Each route's declared refusals are in [`design/`](../design/) and in the
generated OpenAPI description, [`gen/http/openapi3.yaml`](../gen/http/openapi3.yaml).

---

<a id="mcp-tools"></a>
## Operations

Each operation lists its input and output types, what the server does, and
its named refusals. The types and every field's description are in
[`design/`](../design/) and in the MCP input schemas agents receive.

### Changes

#### `list_changes`

`GET /changes`. **Input:** none. **Output:** `ChangeList` (`changes`,
`version`, `approval_available`).

Lists the project's Change Cards, most recently saved first (ties by
`change_id`). Each item has `change_id`, `summary`, `status`
(`needs_confirmation` or `confirmed`), the `request_revision` in force,
`proposed_revision` while a proposed revision waits, `saved_at`, and
`latest_verification`, absent when no run was started. `approval_available` is
`false` when the server runs no verification, even with a promotion target, the
release check that stored promotion settings configure. It is also `false`
when resolving that target fails: the list still answers, and the error is
recorded on the read's span
(`surla.controlplane.list_changes`, or `surla.controlplane.wait_for_changes`
on each of its polls). A server started by `surla` always runs verification,
with or without promotion settings, so there it is `true`.
`version` changes whenever a listed value changes, and also when a build, the
team, the Story evaluation or the live target changes. A card, build or run
that cannot be read fails the call instead of being left out.

`latest_verification` describes the change's most recently accepted run:

| Field | Meaning |
| --- | --- |
| `run_id` | The run, as [`get_verification`](#get_verification) reads it. |
| `state` | `running`, `passed`, `failed` or `not_completed`. |
| `inputs_current` | `true` when the run names the agreed revision and the tests a run of that revision would use. Without stored verification settings, the change's build must be of the agreed revision and its accepted tests commit must be the run's; a later report, or the acceptance of another commit, makes the fact `false`. With stored verification settings, the run must have used the evaluator those settings name: the same repository, commit, module and command. |

The latest run stays readable as history whatever `inputs_current` says. The
fact compares the revision and the tests only: it says nothing about working
files, a deployment or an approval.

#### `wait_for_changes`

`GET /change-updates?since=<version>`. **Output:** `ChangeListVersion`
(`version`).

The views page's waiting read. It answers as soon as the change list's
`version` differs from `since`, or after a few seconds with the same version.
It is traced but never saved in the diagnostics history.

#### `get_change`

`GET /changes/{change_id}`. **Output:** `ChangeCardResult`.

Reads one Change Card and changes nothing. The card's fields describe the
agreed revision, or the only revision while none is confirmed; `answers`
holds the user's answers to its questions. A proposed revision waiting beside
an agreed change is returned in `proposed_revision` with its `prompt`,
`summary`, `ambiguities`, `examples`, `preserved_behaviors`, `stories`,
`request_revision`, `ui_composition` and `changed_design_files` (the
`design/*.go` files that differ from the ones the agreed revision was made
from). The server checks each saved revision's digest when it reads the file.
`not_found` (`404`) means no card has this `change_id`.

A saved approval is returned in `approval` only while the tests chosen for
its revision are still at the approval's `suite_commit` and, when promotion
settings are stored, the release binaries still have the approved digests. A
later report of the tests, the acceptance of other tests, a build after which
Surla does not accept the same commit again, or changed release binaries
hide it; the card file keeps it. After the user rebuilds an unmerged,
undropped open build of the same change, Surla's check of a report accepts the
same commit again only while that build's branch `surla/tests-<n>` still
points at it and the files it checked are exactly the files that commit holds.
A build started after a merge or drop is a new numbered build on a new
`surla/tests-<n>` branch at the project's current commit, so the earlier
approval stays hidden.
When the tests still match and promotion settings are stored but the
release binaries cannot be read, `unverified_approval` carries the saved
approval and this `reason`: "Saved approval for candidate
commit <commit> could not be verified because the release binaries could not
be read. Publish the binaries the run built into the release folder that the
promotion settings name, in a folder named after the commit, and press Check
current approval; or remove the promotion settings with surla promotion
remove and restart Surla; or press Approve this result on a newly verified
run." The read records why on its `controlplane.check_approval` span: the
event `approval.hidden_tests_changed` when the tests changed, and the error
when the release binaries cannot be read.

#### `propose_change`

`POST /changes/propose`. **Input:** `change_id`, `prompt`,
`story_refresh_op_id?`, `analysis`. **Output:** `ChangeCardResult`.

Saves the guide's analysis of a request as a Change Card waiting for the
user. Surla runs no model to propose or revise, so these operations need no
model key. The guide reads [`surla://golden-path`](#mcp-resources), drafts only
in the `.surla-draft` folder [`refresh_story`](#refresh_story) returns, and
sends `analysis`:

| Field | Content |
| --- | --- |
| `summary` | The change in plain words. |
| `ambiguities` | The questions only the user can answer, each a `question` with at least two distinct `options`; empty when nothing is open. |
| `examples` | At least one concrete example: `scenario`, `input_summary`, `expected_outcome`, and `verification_kind` (`deterministic` or `model_judged`). |
| `preserved_behaviors` | The behaviors the change must keep, possibly none. |
| `ui_composition` *(optional)* | An illustration of the decision for the views page (see [MCP resources](#mcp-resources)). It is never checked, never part of `request_revision`, and is dropped when the user changes the answers or examples. |

Surla numbers the questions `<change_id>-ambiguity-NN` and the examples
`check_01`, `check_02`, …, keeps every text as sent, computes
`request_revision`, and saves the card as `needs_confirmation`, even with no
question. With `story_refresh_op_id`, it saves what that succeeded
[`refresh_story`](#refresh_story) operation evaluated from the draft—its
design, Stories, architecture, `go.mod` and made-from record—without reading
any file. A project folder with a `go.mod` requires `story_refresh_op_id` for a
new card; with neither a `go.mod` nor `story_refresh_op_id`, the card records no
design.

| Refusal | When |
| --- | --- |
| `conflict` (`409`) | `change_id` already has a card. |
| `drafted_before_drop` (`409`) | The user dropped a build after `story_refresh_op_id` was evaluated; call `refresh_story` to remake and evaluate the draft from the project folder, then propose again. |
| `unprocessable` (`422`) | `change_id` or `prompt` is blank; the analysis breaks a rule that spans fields (message starting `invalid analysis:`, such as a question that repeats an option); `story_refresh_op_id` does not name the owner's current succeeded `refresh_story` operation (the message names `refresh_story`); or the project folder holds a `go.mod` and `story_refresh_op_id` is omitted. |

Surla cannot judge whether an analysis is complete or right; the user does
that when confirming.

#### `revise_change`

`POST /changes/revise`. **Input:** `change_id`, `request_revision`, `prompt`,
`story_refresh_op_id?`, `analysis` (as for `propose_change`, with
`examples[].prior_id` and `removed_example_ids`). **Output:**
`ChangeCardResult`.

Saves the next revision of a change. `request_revision` names the latest
revision the guide read: the waiting proposed revision's, when one waits,
otherwise the card's. It is checked when the call starts and again before the
save.

| The change is | The revision |
| --- | --- |
| `needs_confirmation` | Replaces it. |
| `confirmed` | Is saved as the proposed revision beside the agreed one, replacing any earlier proposed revision. The agreed revision stays in force with its answers, approval, Story and design source, and previews, builds, verification and approval keep working on it until the user adopts or declines the proposed revision. |

The guide keeps or edits each current example by naming it in `prior_id`,
lists each dropped one in `removed_example_ids`, and restates the decisions
that still hold, because the revision has no answers of its own. Kept and
edited examples keep their IDs; added ones get the lowest free `check_NN`.
With `story_refresh_op_id`, the revision saves what that succeeded
[`refresh_story`](#refresh_story) operation evaluated from the draft—its
design, Stories, architecture, `go.mod` and made-from record—without reading
any file; without `story_refresh_op_id`, it keeps every saved snapshot field of
the revision it revises (design, Stories, architecture, `go.mod` and made-from
record), even with zero Stories, and a project folder with a `go.mod` refuses
an omitted `story_refresh_op_id` when the revision being revised has no design.

| Refusal | When |
| --- | --- |
| `not_found` (`404`) | No card has this `change_id`. |
| `conflict` (`409`) | `request_revision` is not the latest revision, including the agreed revision while a proposed revision waits. |
| `drafted_before_drop` (`409`) | The user dropped a build after `story_refresh_op_id` was evaluated; call `refresh_story` to remake and evaluate the draft from the project folder, then revise again. |
| `unprocessable` (`422`) | Blank text, an example not accounted for exactly once, `story_refresh_op_id` does not name the owner's current succeeded `refresh_story` operation, or the project folder holds a `go.mod` and `story_refresh_op_id` is omitted when the revision being revised has no design. |

#### `confirm_intent`

`POST /changes/confirm`. **Input:** `change_id`, `request_revision`,
`answers`, `examples`. **Output:** `ChangeCardResult`.

The user's **Confirm Intent & Lock Examples**, **Refine Confirmed Answers &
Examples** and **Adopt this revision** on the views page. It
confirms a waiting card, refines an agreed card, or adopts the proposed
revision named by `request_revision`. `answers` maps every open question's
`id` to exactly one of its `options`, character for character. Adopting makes
the proposed revision the agreed one, records the user in `confirmed_by`, and
carries over no approval. Changed answers or examples give the card another
`request_revision` and clear its approval; a submission that changes nothing
keeps both and returns the approval as [`get_change`](#get_change) does. A
decision no option expresses goes to the guide, which records it
with `revise_change`.

| Refusal | When |
| --- | --- |
| `not_found` (`404`) | No card has this `change_id`. |
| `conflict` (`409`) | A stale `request_revision`, or refining the agreed revision while a proposed revision waits (the message names **Adopt** and **Decline**). |
| `unprocessable` (`422`) | One message naming every missing answer, every answer that is not an option, and every unknown question `id`. |

#### `decline_revision`

`POST /changes/decline`. **Input:** `change_id`, `request_revision` (the
proposed revision's, 64 lowercase hex characters). **Output:**
`ChangeCardResult`.

The user's **Decline**. The proposed revision is removed; the agreed revision
stays exactly as saved.

| Refusal | When |
| --- | --- |
| `400` | `change_id` is empty or `request_revision` is malformed. |
| `not_found` (`404`) | No card has this `change_id`. |
| `conflict` (`409`) | No proposed revision waits, or `request_revision` is not the waiting one. |

### The Story

#### `refresh_story`

`POST /workspace/story/refresh`. **Input:** none. **Output:**
`StoryRefreshResult` (`op_id`, `state`, `snapshot_digest`, `draft_folder`,
`stories`, `diagnostic`, `message`).

Reads the guide's draft Go module in `.surla-draft` (`go.mod`, `go.sum` and
the non-test `.go` files under `design/`; on the first `refresh_story` after
the user drops a build with [`drop_build`](#drop_build), Surla remakes
`.surla-draft` from the project folder and notes in `message` that edits made
before the drop are gone until a revision is saved),
computes its `snapshot_digest`, and starts, or reuses, an evaluation
of its Stories. It answers at once with `state` `running` or `succeeded`.
There is one current evaluation per project, kept in memory only: refreshing
unchanged files returns it, and refreshing changed files, or after a failure,
replaces it. An evaluation has 5 minutes and survives the caller
disconnecting. A design that does not use the Story language succeeds with
`stories: []`.

| Refusal | When |
| --- | --- |
| `unprocessable` (`422`) | The project folder has no `go.mod` or was replaced; a draft file is not valid UTF-8, cannot be parsed as Go, or imports a package of the project outside `design/` (the message names the file and line); or `.surla-draft` is a link or a file, not a folder. |
| `unavailable` (`503`) | The server is stopping. |

#### `get_story_refresh`

`GET /workspace/story/refresh`. **Output:** `StoryRefreshResult`.

Reads the current evaluation without starting one. `state` is `none` (none
since the server started; call `refresh_story`), `running`, `succeeded` with
`stories` (each Story's `name`, `description`, `actors` and `flows`, with
`complete`, `steps` and open `questions`), or `failed` with a `diagnostic`.
Pass a succeeded `op_id` as `story_refresh_op_id` to
[`propose_change`](#propose_change) or [`revise_change`](#revise_change).

When a design file imports the Story language but the draft's `go.mod`
does not require `github.com/goadesign/surla/story`, the `diagnostic` of the
failed evaluation also names the step: “`In the folder that holds that
go.mod, run: go get github.com/goadesign/surla/story@<version>, then try
again.`” The version is the one Surla's own `go.mod` requires. The guide runs
the command in its draft, whose `go.mod` was evaluated. A project made by
`surla init` or `init_project` already requires the module.

#### `update_draft`

`POST /workspace/draft/update`. **Input:** none. **Output:**
`DraftUpdateResult` (`draft_folder`, `taken`, `merged`, `to_correct`).

The guide brings design changes from the project folder into `.surla-draft`
(making `.surla-draft` first when missing, as [`refresh_story`](#refresh_story)
does) without running an evaluation. Call it before drafting each new change,
such as after the user merges a passed build, and whenever a design file
changed in the project folder outside Surla (after committing an outside design
change when a build or verification reports `design_changed_outside`).

For each Go file under `design/` (test files excluded) in the recorded history,
`.surla-draft` or the project folder, Surla compares the project folder's
canonical Go version and the draft's version over a comparison base: whichever
was recorded later between the history base (the latest recorded version in the
project's committed history since the draft's start, or the starting folder
version when that history supplies none) and the outside version the draft last
took. A project folder version equal to that comparison base, or already
current in the recorded history, changes nothing. Otherwise, unless a
`to_correct` case below applies:

- When the draft did not change the file from the comparison base, Surla writes
  the project folder's version into `.surla-draft` (or removes the file when the
  project folder removed it) and lists its path in `taken`.
- When the draft already holds the project folder's version, or when both
  changed the file and `git merge-file` merges them without conflict into valid
  Go, Surla writes the canonical Go result into `.surla-draft` and lists its
  path in `merged`.
- When a file needs correction, it is listed in `to_correct`
  (`path`, `folder`, `problem`), in path order. `path` is relative to the folder
  that `folder` names: `draft_folder` for the guide's draft, or `project_folder`
  for the user's project folder:
  - the draft file cannot be parsed as Go, or the project folder's file cannot
    be parsed as Go; `folder` identifies which file needs correction;
  - the project's committed history gained a recorded version from another line
    after the file was last taken, so Surla wrote the project folder's live file
    into `.surla-draft` without merging (`The project's history gained design
    changes from another line after this file was taken. Surla took the live
    project file without merging; check that it holds your intended draft edits
    once.`);
  - one side removed the file while the other changed it (`One side removed
    this file while the other changed it; correct the draft to keep the
    intended design.`);
  - both sides changed the same lines, leaving conflict markers in the draft
    file (`The draft and project folder changed the same lines; resolve the
    conflict markers in this draft file.`), or the merged text does not parse as
    Go;
  - a previous `update_draft` stopped while writing the file and the draft
    changed since (`Surla stopped while bringing the project folder's change
    into your draft, and the draft changed since: make sure it holds that
    change once.`); when the draft file is still as it was before the write or
    already holds its result, `update_draft` finishes the interrupted update and
    never brings that change in a second time;
  - `go.mod`, which Surla never merges or rewrites, cannot be parsed in the
    project or draft, the project's `go.mod` declares no module, the draft
    declares a different module path (`The project and draft
    must declare the same module.`), lacks or differs on a version the project's
    `go.mod` requires (`The project requires <module> <version>; the draft
    requires <version>.`), or has different `replace` directives (`The project
    and draft go.mod have differing replace directives; correct the draft's
    replacements.`).

The guide fixes entries whose `folder` is `draft_folder` inside the result's
`draft_folder`. For an entry whose `folder` is `project_folder`, it tells the
user what to correct there and waits; it never edits the project folder. It
calls `update_draft` again until `to_correct` is empty, then calls
[`refresh_story`](#refresh_story).

| Refusal | When |
| --- | --- |
| `unprocessable` (`422`) | The project folder was replaced; `.surla-draft` is a link or a file rather than a folder, its `.gitignore` has no Surla draft marker, or its provenance is missing; the draft or provenance changed while Surla first made the draft; the draft's or project folder's `go.mod` is missing, or a `go.mod`, `go.sum` or design file there is not regular, cannot be read or is not valid UTF-8; or a design file in the project's current commit cannot be parsed as Go or is a link or submodule. The message names the file or folder and who corrects it. The guide corrects its draft, or asks the user to correct the project file (and commit a correction to committed design), then calls again. |
| `not_implemented` (`501`) | No project folder is bound: `no workspace directory is bound to this control plane; start Surla from a project directory`. |
| `conflict` (`409`) | The project's commit, the draft, the project files or the design provenance changed while preparing the update (`The project's commit changed while preparing the draft update; call update_draft again.` or `The draft, project files or design provenance changed while preparing the update; call update_draft again.`). |
| `unavailable` (`503`) | The server is stopping. |

### Project files and checks

#### `init_project`

`POST /workspace/init`. **Input:** `module_path`, `dir?`. **Output:**
`InitProjectResult` (`dir`, `module_path`, `service_name`, `commit`).

Creates the [`surla init`](#surla-init) starter in a child folder of an empty
project folder, runs `go mod tidy`, `go vet ./...` and `go test ./...` in both
modules, records the first commit `Initial scaffold` with the server's Git
identity, and makes that child folder the server's project folder without a
restart. A restart keeps it. `dir` defaults to the service name and must be a
single folder name (`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`, not `.surla…`). With an
MCP `progressToken`, it reports each phase.

| Refusal | When |
| --- | --- |
| `unprocessable` (`422`) | `module_path` or `dir` is invalid. |
| `conflict` (`409`) | The project folder already holds a project (`go.mod`, `design`, `gen`, `internal`, `cmd` or `eval`), a commit, verification runs or Change Cards made from its design files, or `<dir>` exists. |
| `not_implemented` (`501`) | The `surla` binary was built without an exact module version; install a tagged or pseudo-versioned release. |

#### `generate_code`

`POST /workspace/generate`. **Input:** `target?` (`candidate`, the default, or
`eval`). **Output:** `GenerateCodeResult` (`target`, `files_written`,
`warnings`).

The builder's generation in its `.surla-build` working copy on `surla/build-<n>`
(never in the project folder). For `candidate` (the default), it writes the open
build's saved agreed design into `design/` of the builder's working folder,
checks that `go.mod` there requires the modules that design imports, compiles
the design, and replaces that module's `gen/` exactly as [`surla gen`](#surla-gen)
does. With `{"target":"eval"}`, it regenerates the `eval/` module of the
builder's working folder from that module's own design. In a build that runs
with a test writer (without stored verification settings), `eval/`,
`eval/design/` included, belongs to the test writer and the builder leaves it
untouched; Surla regenerates `eval/gen/` in `.surla-tests` when
`get_test_writer_assignment` marks an assigned build's assignment read and on
each report. Both targets require an open, undropped build in force with the
builder's assignment read. It compiles only; call
[`check_project`](#check_project) to build and test before committing. An error
means `gen/` was not replaced; a warning means it was.

| Refusal | When |
| --- | --- |
| `no_build` (`409`) | No build is open, or the open build was dropped. |
| `conflict` (`409`) | The open build is of an earlier revision (`in_force: false`); the user builds the agreed revision first. |
| `assignment_unread` (`409`) | The builder has not read the open build's assignment; call `get_builder_assignment` first. |
| `copy_off_branch` (`409`) | `.surla-build` is not on `surla/build-<n>` or has a Git operation in progress. |
| `working_folder_missing` (`409`) | The builder's working folder in `.surla-build` is missing or not a directory. |
| `working_copy_busy` (`409`) | Another operation is using the builder's working copy; call again when it ends. |
| `missing_modules` (`422`) | `.surla-build/go.mod` lacks a module the agreed design requires, requires an older version, or has a differing `replace` directive. |
| `unprocessable` (`422`) | The module is absent, replaced or invalid. |
| A server fault (`500`) | The design does not compile or generation fails; the message states the failed phase and says to call `generate_code` again. |

#### `check_project`

`POST /workspace/check`. **Input:** none. **Output:** `CheckProjectResult`
(`op_id`, `passed`, `message`).

Runs `go test ./...` in every Go module of the builder's working folder in
`.surla-build` on `surla/build-<n>` (skipping
`testdata`, `vendor` and folders starting with `.` or `_`, and leaving out
`eval` without stored verification settings when the project has the starter
test layout `go.mod` and `eval/go.mod`: `Left test module eval out (tests
belong to the test writer)`) on the current files in `.surla-build`, uncommitted
edits included, without changing them. The server copies
the files (leaving out `.git` and `.surla*`, never following a link) into a
private folder, then runs each module in two Linux containers from
`golang:1.27.3`: first preparing dependencies with your `GOPROXY`,
`GOSUMDB`, `GOPRIVATE`, `GONOPROXY`, `GONOSUMDB` and `GOINSECURE` and no
credentials, then testing with no network. Dependency preparation omits package
names so they cannot displace Go's errors from the failure message. A helper process, `surla
check-run`, owns the containers and finishes removing them even when the call
is canceled or the server stops. `passed` is true only when every module's
tests pass. `message` (at most 16 KiB) names the first failure; read the full
output with [`read_check_output`](#read_check_output). When the message says
`go.sum` or `go.work.sum` needs entries or `go.mod` needs an update, call
[`update_dependencies`](#update_dependencies).

The project retains downloaded modules and toolchains in its private
`check-modcache/` directory across checks, dependency updates and server
restarts. Only dependency preparation and tidy may write this cache. Test
containers mount it read-only, with `GOPROXY=off` and no network; their compiler
cache, home and temporary files stay private to each command. Every call still
loads the current module/workspace manifests and runs the tests. Cached downloads
do not supply missing or replace incorrect `go.sum` or `go.work.sum` entries.
Go checks recorded download identity; reuse does not rehash extracted cached
files or authenticate changes made by other same-user host programs. Operation
cleanup never removes the retained directory. Projects keep separate caches.

| Refusal | When |
| --- | --- |
| `no_build` (`409`), `assignment_unread` (`409`), `copy_off_branch` (`409`), `working_folder_missing` (`409`), `working_copy_busy` (`409`) | As for `generate_code`. |
| `unprocessable` (`422`) | The builder's working folder has no Go module. |
| `unavailable` (`503`) | `docker` is missing, the engine does not answer or is not local, or the image cannot be pulled. Start Docker and call again: the server looks Docker up on every call. An engine other than the default needs `DOCKER_HOST` or the Docker context in the server's environment. |
| An error naming a path or a container | The files changed while they were copied, or a container's removal could not be confirmed (`docker rm -f <name>`). The server checks again at every stop and start. |

#### `update_dependencies`

`POST /workspace/dependencies`. **Input:** none. **Output:**
`UpdateDependenciesResult` (`op_id`, `files_written`, `message`).

Runs `go mod tidy` for every module of the builder's working folder in
`.surla-build` on `surla/build-<n>` in the same kind of private copy and
containers, with network, then writes back into `.surla-build` only the `go.mod`,
`go.sum` and `go.work.sum` files whose bytes changed. Without stored verification
settings, when the project has the starter test layout (`go.mod` and `eval/go.mod`
for `<module>/eval`), `eval` is left out (`Left test module eval out (tests belong
to the test writer)`). A file that is a symlink, or that
someone edited during the call, is not written, and `message` says why.
Modules and files are written one at a time, so a stopped call can leave a
module with its updated `go.mod` and its earlier `go.sum`; `message` lists
each file written and not written. Refusals are those of `check_project`.

#### `read_check_output`

`POST /workspace/check/output`. **Input:** `op_id`, `cursor?`. **Output:**
`CheckOutputPage` (`text`, `next_cursor`, `captured_bytes`,
`dropped_bytes`).

Returns one page, at most 16 KiB of valid UTF-8, of the output a
`check_project` or `update_dependencies` operation captured. Omit `cursor`
for the first page and pass `next_cursor` for the next; it is absent at the
end. Each operation keeps at most 64 MiB; `dropped_bytes` counts the rest.

| Refusal | When |
| --- | --- |
| `not_found` (`404`) | No output exists for `op_id`. |
| `unprocessable` (`422`) | The cursor is malformed or belongs to another operation. |

### Architecture

#### `preview_architecture`

`POST /changes/preview`. **Input:** `change_id`, `request_revision`.
**Output:** `ArchitecturePreviewResult`.

For a confirmed change, returns the architecture saved with the
agreed revision, as the [`refresh_story`](#refresh_story) operation the
guide proposed or revised with evaluated it: the C4 `containers` and
`edges`, `agent_policies`, and `tools` with their `input_binding`. It reads
and evaluates no file. It reports what the design declares; it does not
compare it with the change. A tool's `requires_confirmation` shows a design
declares a confirmation gate, not that the application delivers it; see
[`examples/orders`](../examples/orders/README.md) for an implemented one.

| Refusal | When |
| --- | --- |
| `not_found` (`404`) | No card has this `change_id`. |
| `conflict` (`409`) | The card is not confirmed, or `request_revision` is stale. |
| `unprocessable` (`422`) | The agreed revision holds no saved architecture (the guide calls `refresh_story`, then `revise_change` with its `op_id`), the design declares no Goa API or service container, or an agent declares no positive `TimeBudget` and `DefaultCaps(MaxToolCalls)`. |

#### `diff`

`POST /diff`. **Input:** `old` and `new` (`DesignSnapshot`). **Output:**
`DiffResult`. Compares two design snapshots as [`surla diff`](#surla-diff)
does.

### Live target and model connection

A live target is the one running copy of the application the server builds
from a commit, so the builder can try prompt wording with
[`draft_override`](#draft_override). Its program follows the
[live target prompt contract](#live-target-prompt-contract).

#### `start_live_target`

`POST /live-target`. **Input:** `change_id`, `candidate_commit` (a full commit ID).
**Output:** `LiveTargetResult` (`candidate_commit`, `url`, `message`).

Checks the owner's confirmed change and builds the candidate as verification
does. Without stored verification settings, the current revision must have a
build whose tests Surla accepted, and a model must be chosen in the Live target
view. The candidate's programs come from `candidate_commit`; the generated test
program comes from the accepted tests commit and starts them with `--live`.
Builder edits to its own test module never become that live test program.
With stored verification settings, the named evaluator starts the target and
needs no build or accepted tests; the change must still be owned and confirmed.
`url` is the application's loopback address.

The test program and the application run on this computer as ordinary
processes, with your access to files and the network. For
written tests, Surla gives the program no provider setting. With a direct
provider, the executor serves the selected model through a loopback gateway;
with a chosen gateway, the program uses that gateway. The operator's stored
evaluator keeps its provider settings. Running as you, a test program can
still read keys your other programs hold.

There is one live target per server. Card, plan and model checks run before
waiting for a start in progress, and again before replacing the old target.
A refusal leaves the running target and its prompt drafts, or its retained
ended observation, in place. Once a replacement is accepted and the old
target is confirmed stopped, its drafts are discarded, even if the new build
or start fails. Prompt text survives only when committed. A target runs until
`stop_live_target`, a later accepted start, its own exit or the server's stop;
building continues if the caller disconnects.

| Refusal | When |
| --- | --- |
| `not_found` (`404`), `forbidden` (`403`) | The change does not exist or belongs to another owner. Take `change_id` from the builder's assignments. |
| `conflict` (`409`) | The change is not confirmed, the commit is not in the repository, or the project folder was replaced. Without stored verification settings, a candidate that changes hand-written files of the test module or shares no history with the accepted tests is also refused. Follow the named user or builder step; after restoring and committing the test module, retry the intended verification or live-target start. |
| `no_build` (`409`), `tests_not_accepted` (`409`) | Without stored settings, the current revision has no build or its tests are not accepted. The message names who acts; the builder waits for that step before verifying or starting again. |
| `no_test_layout` (`409`) | The required starter test layout is missing; follow the same layout step as verification. |
| `live_target_failed` (`422`) | Building or starting failed. For written tests, fix the candidate's startup or tell the user so the test writer can correct and report its acceptance package. If the earlier target's processes could not be confirmed stopped, stop and start Surla before another start. |
| `live_target_unavailable` (`501`) | The server is stopping, or a written-tests project has no model choice. The message names whether to start Surla again or choose a model in the Live target view. |

#### `stop_live_target`

`DELETE /live-target`. **Output:** `StopLiveTargetResult` (`stopped`,
`message`).

Stops the live target and discards its drafts; `stopped` is `false` when none
ran. If an ended observation remains, it clears that notice and says no
process was running. A process stop is attempted once.

| Refusal | When |
| --- | --- |
| `live_target_stop_failed` (`500`) | Its processes or folder could not be confirmed stopped; stop and start Surla. A later stop reports the same. |

#### `get_live_target`

`GET /live-target`. **Output:** `LiveTargetStatus`.

`live_target.state` is `none`; `starting` (`candidate_commit`, `since`),
which cannot be stopped until the start finishes; `running`
(`candidate_commit`, `url`, `started_at`, and `prompts`: each accepted
draft's `prompt_id`, `version` and `accepted_at`, never its text);
`ended` (`candidate_commit`, `started_at`, `ended_at`, `message`); or
`not_confirmed_stopped` (`candidate_commit` and a `message` naming the step:
stop and start Surla). An unexpected ending is published only after confirmed
process cleanup. Its message states that the exact cause is unavailable and
names the builder's recovery step; it exposes no address, key, provider
settings, prompt drafts or logs. That one observation survives reads and page
reloads until a successful explicit Stop, an accepted replacement, or the
server's restart. It is not durable history. A read changes and saves nothing.

#### `draft_override`

`POST /sandbox/draft`. **Input:** `target_kind` (`prompt_template` or
`compiled_rule`), `prompt_id?`, `new_content`. **Output:**
`DraftOverrideResult`.

Sends prompt text to the running live target. An accepted `prompt_template`
draft answers `applied_in_store: true` with the `prompt_version` the target
returned. A `compiled_rule` change is never applied and needs no target: it
answers `applied_in_store: false` and `requires_rebuild: true`, with a
`message` saying that the rule needs a Go rebuild and verification.

| Refusal | When |
| --- | --- |
| `no_live_target` (`409`) | No live target runs; call `start_live_target` first. Nothing changed. |
| `prompt_not_found` (`404`), `invalid_prompt` (`422`) | The target refused the text; its prompt is unchanged. |
| `target_rejected_credential` (`502`) | The target refused the server's key; start it again. |
| `live_target_ended` (`409`) | The target ended unexpectedly before this call, or stopped during the update. The former changes nothing and names the builder's recovery step; during an update it may or may not have applied the text. |
| `update_not_confirmed` (`502`) | The target did not answer, so it may or may not have applied the text; send it again. |

#### `configure_model`

`POST /cp1/model`. **Input:** `provider` (`anthropic` or `openai`) with
`model_name`, or `gateway_addr` alone. **Output:** `ModelConfigResult`.

The user's **Apply** in the Live target view. It chooses, without a restart,
the model that live targets started afterwards receive: a direct provider,
whose request settings `surla` handed to the server from the shell where it
ran ([`hand_over_model_settings`](#hand_over_model_settings)), or a plaintext
gRPC `goa-ai` model gateway at a hostname or IP address and port 1–65535,
such as `127.0.0.1:53114` or `[::1]:53114`, without a URL scheme or path.
Surla validates its syntax without trying to connect. A running target keeps its model. Keys never
travel in this request or through MCP. The choice and the held settings last
until the server stops; after a restart no model is chosen, so the next
`surla` that hands settings over selects that provider's default model again.
Verification does not use the choice.

| Refusal | When |
| --- | --- |
| `400` | An unknown provider or another schema violation. |
| `unprocessable` (`422`) | An invalid combination or gateway address, or the server holds no request settings for the chosen provider: `provider anthropic needs ANTHROPIC_API_KEY: export it, with any ANTHROPIC_ request settings it needs, in a shell and run surla there; the project server keeps running. Or choose a model gateway.` The previous choice stays in force. |

#### `get_model_connection`

`GET /cp1/model`. **Output:** `ModelConnection`, one of `none`, `direct`
(`provider`, `model_name`) or `gateway` (`gateway_addr`).

The choice live targets started afterwards receive. It starts as `none`.
While no model is chosen, a hand-over that holds a provider's settings selects
that provider's default model; every accepted `configure_model` replaces the
choice. It never returns a key or any other setting value.

#### `hand_over_model_settings`

`POST /cp1/model/settings`, the management credential only, never an MCP
tool. **Input:** `anthropic`, `openai`, each optional, each a map from
variable name to value. **Output:** `ModelSettingsResult` (`held`: each
provider's held variable names, sorted; `connection`: the choice, as
`get_model_connection` reports it). It never returns a value.

`surla` calls it after the server answers for the project, with the request
settings of each provider whose key is set in its own environment. A
provider's request settings are the variables of its family (every name
starting with its prefix) that cross; any other family variable, such as
`ANTHROPIC_ADMIN_KEY`, is never sent:

| Provider | Key | Request settings that cross | Default model |
| --- | --- | --- | --- |
| `anthropic` | `ANTHROPIC_API_KEY` | `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `ANTHROPIC_CUSTOM_HEADERS` | `claude-sonnet-4-6` |
| `openai` | `OPENAI_API_KEY` | `OPENAI_API_KEY`, `OPENAI_BASE_URL`, `OPENAI_ORG_ID`, `OPENAI_PROJECT_ID`, `OPENAI_CUSTOM_HEADERS` | `gpt-5.4-mini` |

The pinned Anthropic SDK uses the API key and base URL. When an API key is
held, it ignores `ANTHROPIC_AUTH_TOKEN` and `ANTHROPIC_CUSTOM_HEADERS`; handing
them over does not make them apply to a written-tests live target's provider.
Surla preserves that SDK precedence rather than parsing those values itself.

Choose a base URL whose endpoint you control. If a held base URL names a
loopback port where nothing listens, another local process can bind that port
before the next provider request and receive its credentials. The live test
program and the application it starts can do this too.

An absent provider keeps what the server holds for it. A present one replaces
everything held for it and must include its key, so an empty map is refused.
While no model is chosen, live targets then use the default model of the
first provider held, `anthropic` before `openai`; any other choice stays.

The server holds the settings in memory only and forgets them when it stops.
They are its one source of provider settings: it removes every `ANTHROPIC_`
and `OPENAI_` variable from its own environment when it starts, so none that
the Herdr core holds reaches a live target, a verification or a check; only
Herdr's own programs, which the server runs with the environment it started
with, still receive them. A live target using stored verification settings receives the chosen
provider's held settings; a verification with that evaluator receives every
held provider's settings, as held when it is accepted. The test writer's
accepted program receives none, in verification or a live target. For a live
target with a direct provider, the server instead hands settings over standard
input to its executor, after that process starts; the executor constructs a
provider after preparation and serves it through a loopback model gateway.
The settings are absent from the executor's starting environment and the test
program's environment. The gateway ends with that executor; a gateway panic
ends the target rather than the project server. The check helper also receives
none. A live target or verification already running keeps the settings it
started with.

| Refusal | When |
| --- | --- |
| `400` | An unknown provider or field, a variable that does not cross, or a value longer than 4,096 characters. |
| `unprocessable` (`422`) | Settings without their key, or a value containing a NUL character. The message names the provider and the variable, never a value; nothing held changed. |
| `forbidden` (`403`) | Any caller but the management credential. |

### Verification and approval

#### Go workspace policy

| Built source | Workspace selection for toolchain lookup, dependency download and compilation |
| --- | --- |
| Application binaries and tests, and suites from stored verification settings | Use the checkout's internal `go.work` when present; ignore a workspace above the checkout. |
| Surla-written tests, both report checks and verification suites | Use `GOWORK=off`, including when `go.work` exists in the checkout root or the test module. The module's `go.mod` and `go.sum` own its dependencies. |

Both policies keep the full checkout, including sibling files used by local
module replacements.

#### `verify_change`

`POST /changes/{change_id}/verifications`. **Input:** `change_id`,
`request_revision`, `candidate_commit`. **Output:**
`VerificationStatusResult`.

The builder asks the server to verify its commit for the agreed revision. The
server accepts the run into a run directory and answers at once with
`state.type == "running"`. A separate supervised process carries the run out
and outlives the call and server restarts; read it with
[`get_verification`](#get_verification).

Which tests run depends on the project:

| The project has | Tests that run |
| --- | --- |
| No stored verification settings | The tests Surla accepted for the user's build of this revision, at their tests commit (`get_build` shows `tests.state == "accepted"`). The run builds the application's programs from `candidate_commit` and the test program `eval/gen/cmd/<service>-evals` from the tests commit, both read from Git, never from working files. Both commits need the layout `surla init` makes: `go.mod`, `eval/go.mod` for `<module>/eval`, `cmd/<service>` in the candidate and the generated test program in the tests commit. |
| Stored verification settings | The evaluator the settings name, at their `suite_commit`. The change's open, undropped build of the agreed revision is still required for the builder's working copy and branch. |

Tests a separate agent wrote run, like the application, as ordinary processes
with your file and network access. Their program starts with
only `PATH` and `TMPDIR` (a folder inside the run directory): no model key
and no other variable of the server. An evaluator named in stored
verification settings runs with the server's environment, plus every held
provider's settings.

| Refusal (all `409`) | When and the step |
| --- | --- |
| `no_build` | No build is open, the open build belongs to another change or is of another revision (named), the user dropped the build, or the build was started without a test writer while verification settings were stored and they have since been removed. The user builds again. |
| `assignment_unread` | The builder has not read the open build's assignment; call `get_builder_assignment` first. |
| `copy_off_branch` | `.surla-build` is not on `surla/build-<n>` or has a Git operation in progress. |
| `working_folder_missing` | The builder's working folder in `.surla-build` is missing or not a directory. |
| `build_branch_mismatch` | `candidate_commit` is not on the open build's branch `surla/build-<n>`, does not descend from the build's base commit, is the base commit itself, or holds commits from another build's branch. |
| `design_changed_outside` | A design file at `candidate_commit`'s merge base with the project's commit was changed outside Surla. |
| `tests_not_accepted` | Surla has not accepted the build's tests. The message names their state (assigned, reported, needs changes, could not run) and who acts next, and ends with the builder's own step, for example `the tests of this build are not accepted; their state is reported: Surla is checking the test writer's report; your step: keep your Chorus watcher running; Surla messages you there when the tests are accepted; then read get_builder_assignment and verify or call start_live_target again`. |
| `no_test_layout` | A commit lacks the layout, naming what is missing: the builder fixes its commit, or the test writer reports again for the tests commit. Also an accepted tests commit the repository does not hold (the test writer reports again, or the user builds again), and a project folder in no Git repository (make it one and commit). |
| `conflict` | The card is not confirmed or `request_revision` is stale; Surla is using the builder's working copy (call `verify_change` again when that operation has ended); `candidate_commit` is not in the repository (commit it and try again), shares no history with the project's current commit, or its `design/` differs from the open build's agreed design (run `generate_code` to restore the agreed design, commit on `surla/build-<n>`, and verify again); without stored verification settings, `candidate_commit` changes hand-written files of the test module since the last commit it shares with the accepted tests commit (`eval/gen/...` and `eval/AGENTS_QUICKSTART.md` excluded; the message names up to 20 files, notes on `eval/go.mod` changes that renaming the service module is the user's to do on their own branch between changes, and gives the `git restore` or `git rm -f` command to restore or remove them and commit again) or shares no commit with the accepted tests commit (build the change again from the project's current commit, then read the new assignments and verify again); or the project folder was replaced after the server started (stop and start Surla). |

`not_found` (`404`) means no card has this `change_id`.

#### `get_verification`

`GET /verifications/{run_id}`. **Output:** `VerificationStatusResult`.

Reads one run, whatever happened to the card since. It returns
`change_id`, `request_revision`, `candidate_commit`, `suite_commit`,
`accepted_at`, `test_source`, `inputs_current`, and `state`. The state's
`type` names the outcome and its `value` holds these fields:

| `state.type` | Fields of `state.value` |
| --- | --- |
| `running` | `started_at` |
| `completed` | `suite_id`, `environment` and the builder view of the `report`, in which hidden scenarios appear only as `holdout-N` |
| `not_completed` | `kind` (`execution_failed` or `interrupted`), `stage` (`checkout`, `build`, `describe`, `suite`, `report` or `supervise`) and a `summary`; the full output stays in the run directory's `executor.log` |

`test_source`, recorded when the run started, is `evaluator`, or
`written_tests` with the tests' `commit` and the `notice` `These tests were
written by a separate agent on this machine and are not isolated from the
builder.` The test writer may read only the runs of a change it has an
assignment for; any other run is refused as `forbidden` (`403`). `not_found`
(`404`) means no such run.

For a completed run, get and list responses also include optional
`candidate_binaries_dir` when the caller is the user and the server has
promotion settings. It names the actual folder of binaries built by that
run, read from its recorded plan, rather than a reconstructed run layout.
Agents never receive this path. The Check view shows it under **Binaries
built by this run**, as selectable code, and repeats it in the approval
error panel for that selected run.

Every `VerificationStatusResult`, including the start and list responses,
carries required `inputs_current`. It uses the [change list
comparison](#list_changes) for that individual run: the agreed revision and
the tests a run of it would use. This observation may change while the saved
verdict and report stay fixed; it proves neither the current working tree
nor deployment or approval. Start observes the match before accepting work;
get and list observe it while reading current owned card and build records.
An authorized historical get with no current owned card still returns the
report with `inputs_current: false`. Unreadable current records fail the read
instead of returning a false match.

#### `review_verification`

`GET /verifications/{run_id}/review`. **Output:** `VerificationReviewResult`.
Only the user may call this read; agents use `get_verification`.

Returns the same run status and builder-safe report fields as
[`get_verification`](#get_verification), plus required `review`:

| Field | Meaning |
| --- | --- |
| `story_context.state` | `recorded`, with the accepted build's number, start time and agreed `stories`; or `not_recorded` when the run has no saved Story context. A recorded empty Story remains `recorded`. Later revisions never replace this snapshot. |
| Recorded context's `mapping.state` | `mapped`, with `required_tests` naming each finished flow's exact generated scenario and hook; or `not_recorded_for_evaluator`, because an operator evaluator records no relation between its tests and Story flows. Unknown mapping does not prove an untested flow. Draft flows remain in the Story and require no test. |
| `approval.state` | `available`, `blocked` with the owner's `reason`, `recorded` with the current saved approval, or `unverified` with the saved approval and the reason its release artifacts could not be checked. |

`available` observes the same approval requirements the write checks, including
the current build, selected tests, report, candidate membership and configured
release artifacts. It is not a reservation: `approve_merge` rechecks and may
refuse a change that happens after the read. A failed observation withholds
approval and explains recovery. If current promotion settings cannot be read,
this read retains the saved run evidence and omits promotion-only binary-folder
metadata; the shared `get_verification` read retains its existing failure
behavior. Reading records no approval, merge or deployment and changes no
assignment or build state. Run IDs and authorization are checked
by the same owner as `get_verification`; unknown runs return `not_found` (`404`).

#### `list_verifications`

`GET /changes/{change_id}/verifications`. **Output:**
`VerificationListResult` (`change_id`, `runs`).

Every run of the change, newest first by `accepted_at` (ties by `run_id`),
including runs of earlier revisions, each as `get_verification` returns it.
The test writer may list only the runs of a change it has an assignment for;
any other change is refused as `forbidden` (`403`). `not_found` (`404`) means
no card has this `change_id`.

#### `approve_merge`

`POST /changes/approve`. **Input:** `change_id`, `request_revision`,
`candidate_commit`, `report_digest`. **Output:** `ChangeCardResult` with
`approval`.

The user's **Approve this result** for a passed run. The server takes the
tests commit from the plan for that revision: the commit Surla accepted for
the build, or the `suite_commit` of the stored verification settings. It
loads the full report from the project's report store, derives it again from
its evidence, and checks that it passed and ran those tests against
`candidate_commit` for `request_revision`. When promotion settings are
stored, it also measures the release binaries in
`<release_dir>/<candidate_commit>/`; without them, the approval records no
release binaries. Then it saves the approval, after checking that the
revision and its accepted tests did not change meanwhile. The approval names
two commits: `candidate_commit`, the builder's commit on `surla/build-<n>`,
and `suite_commit`, the tests commit on `surla/tests-<n>` (or the stored
evaluator's commit), with `test_source`. Because the builder commits in
`.surla-build` rather than in the project folder, while the open build shows
`passed` the page gives the `git merge <commit>` commands to run in the project
folder: first `git merge <candidate_commit>` (verified on `surla/build-<n>`),
and then, for written tests, `git merge <suite_commit>` (verified on
`surla/tests-<n>`); with stored verification settings, only
`git merge <candidate_commit>` is shown. Surla merges and deploys nothing.

| Refusal | When and the step |
| --- | --- |
| `not_found` (`404`) | No card has this `change_id`. |
| `conflict` (`409`) | The card is not confirmed, `request_revision` is stale, or the accepted tests changed while approving (approve again). |
| `no_build` (`409`) | The change has no open, undropped build of the agreed revision, or a builder-only build lost stored verification settings; the user builds again. |
| `build_branch_mismatch` (`409`) | `candidate_commit` is not on the open build's branch `surla/build-<n>`, does not descend from the build's base commit, is the base commit itself, or holds commits from another build's branch. |
| `tests_not_accepted` (`409`) | Surla has not accepted the build's tests. The message, which the user reads, names their state and who acts next, naming the builder rather than instructing it, for example `the tests of this build are not accepted; their state is reported: Surla is checking the test writer's report; the builder verifies again after its verdict`. |
| `unprocessable` (`422`) | The report did not pass, does not follow from its own evidence, was made for another candidate commit or another revision than the ones named, or ran tests at another commit than the tests chosen for the revision. For other tests the message names both commits and ends with the step `the builder verifies the candidate again; then approve that run`. |
| `unprocessable` (`422`) | Promotion settings are stored and a release binary is missing, unreadable or differs from the run's. The message names no folder path and ends with the step `publish the binaries the run built, whose folder the run's Proof view shows, into the release folder that the promotion settings name, in a folder named after the commit, or remove the promotion settings`: that folder is `<release_dir>/<candidate_commit>/`, and removing the settings is `surla promotion remove`, then `surla stop` and `surla`. |
| `not_implemented` (`501`) | The server runs no verification. A server started by `surla` always does. |

### Diagnostics

#### `list_traces`

`GET /traces`. **Input:** `status?` (`all`, `error` or `ok`; default `all`),
`limit?` (1–100, default 20), `cursor?`. **Output:** `TraceList` (`spans`,
`has_more`, `next_cursor`).

Reads the project's saved operation diagnostics, newest first. For example,
`{"status":"error"}` lists recorded failures. Each span has its operation
name, a safe summary, timestamps, duration, status and selected identifiers
and results; raw errors, events, prompts and credentials are left out, and
`shortened: true` marks shortened text. Each call examines at most 256 KiB of
history: an empty page with `has_more: true` means older history remains; pass
`next_cursor` with the same filter. `ok` selects spans without an error
status, including `unset`, which does not prove success. `unprocessable`
(`422`) means the cursor is invalid; read again without it. See
[Operation diagnostics](#operation-diagnostics).

---

## Builds and assignments

**Input:** the user's start button in Build, such as **Build this change**
for a first build.
**Who acts:** the server saves the agreed revision's Story and design, with
the answers, examples and preserved behaviors the user confirmed for it,
creates any missing `.surla-build` and `.surla-tests` working copies, gives the
builder and the test writer their assignments, and adds them to the team; when
each worker first reads its assignment, Surla joins its working copy to the
build's branch, and `get_test_writer_assignment` writes the required tests when
marking the assignment read. **What the user sees:** the Build section shows the
build, each
role's assignment and the test writer's progress, and the Chorus dialog asks
for an agent for each role it adds.

A project has one open build, numbered `n`, whose `taking.state` is `building`,
`passed`, `merged` or `dropped`. Building another change is refused with
`previous_change_open` until the open build is `merged` or `dropped`. Building
the open build's own change again before it is `merged` or `dropped` keeps its
number `n` and base commit (requiring `replaces` while `taking.state` is
`building`) and makes both assignments unread; building it again after a merge
starts a new numbered build from the project's current `HEAD` commit, and so
does a build after a drop once the revision to build was drafted after that
drop; a revision drafted before it is refused with `drafted_before_drop`. A
proposed revision is built only after the user adopts
it. With stored verification settings, a build adds only the builder.

**The build working copies.** Surla creates linked Git working copies inside
the project folder at `.surla-build` on `surla/build-<n>` for the builder and,
when no verification settings are stored (the project then needs the test
layout `surla init` makes), at `.surla-tests` on `surla/tests-<n>` for the
test writer, each starting at the build's base commit. It adds `# Surla's
working copies.`,
`.surla-build/` and `.surla-tests/` to the repository's own `.git/info/exclude`
file, never to a tracked file, so `git status` stays clean. Starting a build
creates any missing working copy and leaves existing copies where they are;
when each worker first reads its assignment for a new numbered build, Surla
switches its copy to `surla/build-<n>` or `surla/tests-<n>` at the build's base
commit (first committing any uncommitted files left on an earlier build branch
of that role onto that earlier branch with `Left uncommitted when change <id>
started`). Accepted tests are commits on `surla/tests-<n>`, so deleting a
working copy loses only uncommitted work: the next build recreates the folder.

**Required tests.** For a build with a test writer, Surla writes the test
module's Story table, `eval/design/story_tests.go`, which declares one scenario
per finished flow of the saved Story, and generates the module from its design.
Flow `refund-over-limit` requires scenario `refund_over_limit` and the hook
method `RefundOverLimit`. Unfinished flows require none; with no finished flow,
the test program must still build. Surla writes the table and the test module's
generated code when `get_test_writer_assignment` marks an assigned build's
assignment read (at report 0) and again at each report, generating with the
table it renders in place of whatever the test writer left at that path; the
test writer leaves that file as it is. Because the required tests are part of
the module's design, regenerating the module keeps them. A generation that
fails writes neither the table nor the generated code, so the copy stays as it
was; only a symbolic link at the table's path is replaced by the table first.
When that marking read cannot write them, a cause the test
writer can fix, such as a test module design that does not compile, sets the
tests to `needs_changes` with that finding, at report 0. Any other cause sets
`could_not_run`, with a reason that says what failed (the preparation's time
limit or a server stop, a download of the module's dependencies that fails
outside the test writer's changes, the generator and its phase, or writing
the generated folder into the copy; for any other cause, the reason points
at the server's trace) and the user's step, which ends with
building the change again.

**Limits and recovery.** A build start's saving and copy-making, and the
writing of the required tests on the test writer's read that marks its
assignment read, each have five minutes and continue if the page closes. One
operation at a time uses each working copy. A build start does not wait: it
answers `working_copy_busy` while another start, a worker's assignment read,
one of the builder's tools (`generate_code`, `check_project`,
`update_dependencies`, `verify_change`) or a check of a report is running. A
worker's assignment read and the builder's tools are refused too while their
copy is in use (`working_copy_busy`, or `conflict` for `verify_change`); only
Surla's check of a report waits for the copy. After a start has saved the
build and made the copies,
bringing the team up has 45 seconds. A team problem after saving leaves the
build and its roles in place: the result says what to do, and **Start agents**
or `surla` finishes it. If the server stops after saving, the saved build
stays; building again makes any missing working copy and registers the roles.
Required tests that a stopped read did not finish writing are written again at
the test writer's next report.

### `start_build`

`POST /changes/{change_id}/build`. **Input:** `change_id`, `request_revision`
(the agreed revision), `replaces?` (`ReplacedBuild`: `change_id`,
`started_at`). **Output:** `StartBuildResult` (`build`, `team`).

- `build` is the saved `StartedBuild`, as `get_build` returns it.
- `team.state` is `ready` (the team result with its `step`), `window_busy`,
  `ambiguous_space` (with the `count`) or `unavailable`, each with a `step`.
  A team warning still means the build was saved.

Every refusal below saves no build and registers no role.

| Refusal | HTTP | Recovery |
| --- | --- | --- |
| `not_found` | 404 | No card has this `change_id`. |
| `conflict` | 409 | The card is not confirmed, `request_revision` is not its agreed revision, or the agreed revision records no design. |
| `previous_change_open` | 409 | Another change's open build (`OpenBuildRefusal`: `name`, `message`, `change_id` and `build`, the open build as `get_build` reports it, with its `started_at` and `taking`) is still `building` or `passed`; merge or drop it first. |
| `unfinished_build_open` | 409 | This change's open build (`OpenBuildRefusal`) is still `building`, and `replaces` was omitted or did not match its `change_id` and `started_at`. |
| `drafted_before_drop` | 409 | The agreed revision was made from a draft before a build was dropped; the guide refreshes the Story and revises the change, and the user adopts the revision before building. |
| `design_changed_outside` | 409 | A design file at the build's base commit is not a version the agreed revision was made from: it was changed outside Surla. The message names the file and the commit. |
| `unprocessable` | 422 | Two finished flows would have the same test name, or one has an empty name; the guide revises the Story. |
| `not_repository` | 409 | Make the project folder a Git repository and commit its files. |
| `no_commit` | 409 | Commit the project's files. |
| `no_test_layout` | 409 | The project has neither stored verification settings nor the test layout; store verification settings, then build again. |
| `git_identity_missing` | 409 | Configure Git's name and address, then build again. |
| `copy_folder_in_use` | 409 | Move the project's `.surla-build` or `.surla-tests` entry, which is not Surla's working copy, out of the project. |
| `tests_branch_in_use` | 409 | Another working copy holds `surla/build-<n>` or `surla/tests-<n>`; the message names it and the step. |
| `working_copy_busy` | 409 | Build again when the current build start, assignment read or report check has ended. |
| `not_implemented` | 501 | The server has no team; start the project with `surla`. |
| `unavailable` | 503 | The server is stopping or the start ran out of time; build again. |

### `drop_build`

`POST /changes/{change_id}/build/drop`. **Input:** `change_id`, `started_at`
(RFC3339). **Output:** `ChangeBuild` (`change_id`, `drafted_before_drop`,
`build` with `taking.state == "dropped"` and `kept_branches`).

The user's **Drop change** confirmation on the views page. Marks the open build
`dropped` when its `change_id` and `started_at` match (the page offers the
action while `taking.state` is `building` or `passed`; a `merged` build may be
dropped too), records the branches (`surla/build-<n>` and
`surla/tests-<n>`) that already hold commits beyond the build's base in
`kept_branches`, stops worker tools on that build with `no_build`, and causes
the next [`refresh_story`](#refresh_story) to remake `.surla-draft` from the
project folder. The dropped build no longer blocks starting another build,
but an agreement drafted before the drop still cannot be built. The guide
refreshes the draft and revises the change, then the user adopts that revision;
[`get_build`](#get_build) reports this condition as `drafted_before_drop`. Other
start checks still apply.

| Refusal | HTTP | Recovery |
| --- | --- | --- |
| `not_found` | 404 | No card has this `change_id`, or the project has no open build. |
| `build_changed` | 409 | The open build (`OpenBuildRefusal`) belongs to another change or has a different `started_at` than named. |
| `conflict` | 409 | The build was already dropped. |
| `not_implemented` | 501 | The server has no team; start the project with `surla`. |

### Judging reports

Surla, not the test writer, decides whether the tests count. Each
[`report_tests_finished`](#report_tests_finished) raises the build's report
number and wakes the server's report checker, which judges the working copy
against the build's saved Story:

1. It writes the required tests into the copy again from the Story. Like the
   test writer's read that marks its assignment read, it first removes any
   `.surla-gen-*` staging folder that a
   write cut short by a server's death left in the test module. An entry with
   such a name that is a link, a file, or a folder holding anything but a
   generated `gen` folder is the test writer's and is left alone.
2. It checks that `.surla-tests` is on `surla/tests-<n>` with no Git operation
   in progress and that only files inside the test module changed since the
   build's base, regenerates the generated folder in a private snapshot,
   downloads the module's dependencies, builds the test program
   `./gen/cmd/<name>-evals` (10 minutes), and lists its
   scenarios (60 seconds). The build and the listing run as ordinary
   processes with your file and network access, each in its own process
   group that the check stops; a process that starts its own session leaves
   its group and keeps running. The whole judgment has 15 minutes. Before
   recording an acceptance, it also checks that the tip of `surla/tests-<n>`
   descends from the build's base without commits from other builds' branches.
3. Its verdict replaces `reported` and marks the test writer's assignment
   unread:
   - `needs_changes`, with findings, such as a missing hook method, a file
     changed outside the test module, a scenario tagged `holdout`, a module
     that does not declare exactly one evaluation suite, or a build or
     listing that ran out of time (when building hits the 10-minute limit, the
     finding says `The test program did not finish building within 10 minutes. Report again: the retained build cache can reuse compilation that completed. If it still does not finish, make the test module build faster, for example with fewer or smaller dependencies.`);
   - `accepted`: Surla commits the tests on `surla/tests-<n>` and marks the
     builder's assignment unread too;
   - `could_not_run`: a cause the test writer cannot fix, with the user's
     step.

Surla never shows the output of the dependency download, which may hold
proxy settings, so it decides whose failure a failed download is from what it
can compare. When the test module's `go.mod` and `go.sum` are those of the
copy's base, or the base's own cannot be downloaded either, the check could
not run, and the step has the user run the download in the terminal `surla`
was started from, where its output can be read. When the base's lists
download and only the test writer's changed ones do not, it is the test
writer's finding, which names the base commit. A module the test writer
added that exists but cannot be reached from where Surla runs reads the same
as one that does not exist, so that finding also says to tell the user when
the download works in the copy. The writing of the required tests on that read
decides the same way when generation fails while loading the module's
dependencies.

Every acceptance carries `not_judged`, always the sentence `Surla checked that
these tests build into the generated test program and declare every required
scenario; it did not judge what they assert.` The user reads the tests before
merging them.

One judgment runs at a time, in the project's turn. A report that arrives
during a judgment makes the server discard that judgment's verdict and judge
the latest report next. Stopping the server ends a judgment without saving
it, and the next server judges every reported build when it starts. A
verdict that could not be saved is judged again a minute later, and when a
build could not be read at all, at a start or before a judgment, the checker
reads the builds again a minute later. Each is tried up to three times in a
row; after that the build stays `reported` until the next report or server
start. Findings contain no absolute path.

### `get_build`

`GET /changes/{change_id}/build`. **Output:** `ChangeBuild` (`change_id`,
`drafted_before_drop`, `build`).

`drafted_before_drop` reports one precise admission condition for the current
agreed revision: its saved record of how the design was drafted predates the
current actual drop, so `start_build` would refuse it with that error. It stays true while a
proposal waits, including a refreshed proposal, and becomes false once the
user adopts a revision drafted after the drop, even if `request_revision`
stays the same. To recover, the guide calls `refresh_story` and `revise_change`
from that refresh, then the user adopts the revision. It is false without an
agreed revision or saved record of its draft. False does not guarantee the
other start checks will pass; `start_build` rechecks concurrent changes. `drop_build`
returns the same required fact in its `ChangeBuild` result.

`build.state` is `not_started` when `change_id` is not the project's open build
(or its builder-only build lost stored verification settings), otherwise
`started` with:

| Field | Meaning |
| --- | --- |
| `request_revision`, `started_at` | The revision built and when. |
| `in_force` | `true` while that revision is the agreed one, including while a proposed revision waits; `false` when adoption or confirmation changes the agreed `request_revision`. Adopting a refreshed draft with unchanged content can keep it `true`. The build is not redone by itself. |
| `taking` | How the open build stands with the project folder's branch: `building`, `passed` (with `run_id`, `candidate_commit`, `candidate_branch` `surla/build-<n>`, `tests`—`evaluator` or `written_tests` with `commit` and `branch` `surla/tests-<n>`—and `base_in_branch`), `merged`, or `dropped` (with `kept_branches`). |
| `required_test_count` | Finished flows in the saved Story; `0` means no test was required. It counts entries; it does not judge what the tests assert. |
| `required_tests` | Required for every caller: one `RequiredTest` per finished flow in the saved build Story, in declaration order (`story`, `flow_key`, `scenario`, `hook`); empty when none. Later agreement edits do not replace this list. It names requirements, not a mapping from findings to tests or proof of evaluator coverage. |
| `builder.read` | Whether the builder read its assignment. |
| `test_writer` | `read`, `report` (0 until the first report, one more with each) and `tests`. |

`required_tests` also appears in other responses carrying `StartedBuild`,
including build start and drop. Regenerate validating clients for this required
field and update the server and embedded page together. Existing saved builds
need no conversion; the server derives the list from their saved Story.

`taking.state` becomes `merged` when the project folder's checked-out branch
holds the commits of one passed run: the builder's commit and, for written
tests, the tests commit. Stored evaluator tests are never merged. Each commit
counts when it is in the branch's history or a squash commit holds every file
it changed as that commit has it. A merged build no longer blocks starting
another build; `start_build` still applies its other checks.

`tests.state` is `not_used` (stored verification settings), `assigned`,
`reported`, `needs_changes` (`finding_count`, never the text), `accepted`
(`commit`, `not_judged`) or `could_not_run` (`reason`, `step`); each verdict
carries `checked_at`. A build started with only the builder is usable while
verification settings are stored; once they are removed, it reads as
`not_started` and the user builds again.

### `get_test_findings`

`GET /changes/{change_id}/build/findings`. **Output:** `TestFindings`
(`change_id`, `request_revision`, `started_at`, `report`, `checked_at`,
`findings`).

The text of Surla's findings while the tests are `needs_changes`, for the
views page, with the build and the report they belong to (`report` is 0 when
the problems were found while writing the required tests, before any report).
`conflict` (`409`) in any other state, naming it; `not_found` when there is no
usable build.

### `list_builder_assignments` and `get_builder_assignment`

`GET /assignments/builder` and `GET /assignments/builder/{change_id}`.
**Output:** `BuilderAssignmentList` (the open build's assignment while that
build is open, not dropped, and needs the builder—until a verification of it
passes with current inputs; empty otherwise) or one `BuilderAssignment`
(`change_id`, `request_revision`, `in_force`, `working_folder`, `gowork_off`,
`read`, `written_tests`).

The builder works in `working_folder`, inside `.surla-build` on `surla/build-<n>`
(never in the project folder), and runs Go with `GOWORK=off` when `gowork_off`
is `true`. On the first `get_builder_assignment` of a new numbered build, Surla
joins `.surla-build` to `surla/build-<n>` at the build's base commit (first
committing any uncommitted files left on an earlier `surla/build-<k>` branch
onto that earlier branch). `written_tests.state` is `not_used`,
`not_yet_accepted`, or `accepted` with the tests commit and the verdict's time.
Reading one assignment marks it read; listing changes nothing. `get_builder_assignment`
refuses with `not_found` (`404`) when there is no usable open build of that
change, and with `no_build` (`409`, when the open build was dropped),
`copy_off_branch` (`409`), `working_copy_busy` (`409`) or
`working_folder_missing` (`409`).

### `list_test_writer_assignments` and `get_test_writer_assignment`

`GET /assignments/test-writer` and `GET /assignments/test-writer/{change_id}`.
**Output:** the list (the open build's assignment while that build is open, not
dropped, uses a test writer, and its tests are not yet `accepted`; empty
otherwise), or one `TestWriterAssignment`:

| Field | Meaning |
| --- | --- |
| `request_revision`, `in_force`, `progress` | As in `get_build`. |
| `working_folder` | The project's folder inside the test writer's `.surla-tests` working copy on `surla/tests-<n>`. |
| `design_source`, `stories` | The design and Story as agreed at the built revision; later revisions do not change them. |
| `answers` | Each question of the agreed revision with the option the user confirmed (`id`, `question`, `answer`), in question order; empty when the revision asked none. |
| `examples`, `preserved_behaviors` | The revision's confirmation examples and preserved behaviors, exactly as the user confirmed them. |
| `required_tests` | One entry per finished flow: `story`, `flow_key`, `scenario`, `hook`. |
| `findings` | The text of Surla's findings while `progress` is `needs_changes`; empty otherwise. The list carries only their number. |

The build saves `answers`, `examples` and `preserved_behaviors` when it
starts, so a revision proposed or adopted later does not change them. The
test writer may not read the change with `get_change`, so these fields are
how its tests learn what the user agreed: the tests must hold the service to
them as well as to the Story's flows. For example, when the user answered
"Without tax" to "Does the $50 limit include tax?" and the limit includes $50.00 itself, a test checks that an
order of $50.00 before tax is refunded at once. Surla's check of a report
confirms the required tests, not what they assert. Reading the assignment of
a build that holds no example, such as one saved by a `surla` without these
fields, fails with `the build of change "<change_id>" holds none of the
agreed answers, examples and preserved behaviors; the user builds this change
again on the views page`.

On the first `get_test_writer_assignment` of a new numbered build, Surla joins
`.surla-tests` to `surla/tests-<n>` at the build's base commit (first committing
any uncommitted files left on an earlier `surla/tests-<k>` branch onto that
earlier branch). The test writer aligns the service interface in its copy with
the agreed design, writes the tests in `eval/` of `working_folder`, checks them
with `go build ./...` in `eval/`, which names any required method still missing,
and then reports. It leaves the Story table, `eval/design/story_tests.go`,
as it is: Surla writes it from the agreed Story and generates `eval/gen/` when
`get_test_writer_assignment` marks an assigned build's assignment read, and
again at each report. Because the required tests are part of the module's
design, regenerating the module with `surla gen -dir eval` keeps them. After
changing the test module's design, the test writer reports, and Surla generates
the module again; it can also run
`surla gen -dir eval` in `working_folder` first. Reading an assignment marks
it read. `get_test_writer_assignment` refuses with `not_found` (`404`) when
there is no usable test assignment for this change, and with `no_build` (`409`,
when the open build was dropped), `copy_off_branch` (`409`),
`working_copy_busy` (`409`) or `working_folder_missing` (`409`).

### `report_tests_finished`

`POST /assignments/test-writer/{change_id}/finished`. **Input:** `change_id`,
`request_revision`. **Output:** `TestWriterAssignmentItem`.

Records `reported` from any state, raises `report` by one, clears an earlier
verdict, returns at once and wakes the report checker.

| Refusal | When |
| --- | --- |
| `no_build` (`409`) | The open build was dropped. |
| `assignment_unread` (`409`) | The test writer has not read the open build's assignment; call `get_test_writer_assignment` first. |
| `copy_off_branch` (`409`) | `.surla-tests` is not on `surla/tests-<n>` or has a Git operation in progress. |
| `working_folder_missing` (`409`) | The test writer's working folder in `.surla-tests` is missing or not a directory. |
| `conflict` (`409`) | Another revision than the assignment's, or the project stores verification settings and uses no test writer. |
| `not_found` (`404`) | No usable test assignment for this change. |

---

## Team supervision

The server rebuilds the team's picture in memory from Herdr and Chorus and
saves none of it. The user's views page reads it and acts on it with
`get_team`, `show_team_pane` and `start_team_agents`; agents and management
cannot call these three, and none is an MCP tool. The `surla` command
prepares the same team with the management operation `ensure_team`.

If no team is bound to the server, all three user operations return
`not_implemented` (`501`) with the enabling step:
`Start this project with surla to use its team.`

### `get_team`

`GET /team`. **Output:** `TeamSnapshot` (`team`, `roles`). Registered roles
in guide, builder, test writer order; empty before any is registered. Reading
creates nothing and marks nothing read. Each role has:

| Field | Values |
| --- | --- |
| `state` | `not_started`, `ended`, `starting`, `needs_you`, `working`, `waiting`, `error` or `unknown` |
| `attention` | Why a role needs the user: `none`, `permission`, `question`, `auth`, `silent` (a running agent that reported nothing for 60 seconds), `assignment` (a role `waiting` with an unread assignment for 90 seconds; see **Reminders** below) or `unknown`. `silent` and `assignment` change only what is shown. |
| `watcher` | `held` when Chorus reports the agent's watcher, `gone` after 90 seconds of known absence, or `unknown` |
| `assignment` | `unread` (the role has work on the open build and owes an assignment read), `nothing_owed` (the role has work and owes no read; the guide always reads `nothing_owed`, shown on the page as `No assignments`), `no_work` (the open build needs nothing from that role: no build is open, the open build was dropped, the test writer's tests are `accepted`, or a verification of the builder's build passed with current inputs) or `unknown` |

When Herdr sends no heartbeat or event for 45 seconds, every role reads
`unknown` until fresh facts arrive. A failed assignment read never reads as
`nothing_owed` or `no_work`. `wait_for_changes` also covers these facts.
For an unbound team, see [the shared refusal](#team-supervision).

### `show_team_pane`

`POST /team/pane`. **Input:** `role`. Shows this project's space and focuses
that role's pane, only when the user asks.

| Refusal | When |
| --- | --- |
| `not_implemented` (`501`) | No team is bound; see [the shared refusal](#team-supervision). |
| `window_busy` (`409`) | A Herdr dialog is open; close it first. Nothing in the window changed. |
| `pane_unavailable` (`409`) | The role has no pane Surla can identify. |
| `team_unavailable` (`503`) | Herdr could not establish the pane: `Herdr did not complete a team command; open the Herdr app, then try again`. |

### `start_team_agents`

`POST /team/agents`. **Output:** `ProjectTeam` (`team`, `space`, `roles`,
`dialog`, `step`). The **Start agents** action: the same team preparation
`surla` runs, opening the Chorus dialog when the guide, or a worker role that
has work on the open build (or any listed role when the assignment read fails),
is not running.
Refusals: `not_implemented` (`501`, no team is bound; see
[the shared refusal](#team-supervision)), `ambiguous_space` (`409`, close the
duplicate Herdr spaces, then start the agents), `window_busy` (`409`) and
`team_unavailable` (`503`), whose message is the views page's step for its cause (see
[`ensure_team`](#ensure_team-management)). `team_unavailable` can come after
the server saved the team's name and space, registered the guide and wrote
the roster; they stay saved, and the next start reuses them.

### `ensure_team` (management)

`POST /project/team`, called by `surla`. It prepares the same team and
returns `team`, `space`, `roles` (`role`, `handle`, `running`), `dialog`
(`opened`, `already_open` or `not_needed`; a worker role without work on the
open build stays in the roster without opening the dialog) and `step`. `surla`
prints `step` unchanged. Its refusals, `ambiguous_space` (with the count), `window_busy`
and `team_unavailable`, are shown with fixed recovery text. As for
`start_team_agents`, `team_unavailable` may leave the team's name, space,
guide and roster saved for the next start.

`team_unavailable` names its `cause`, and `surla` and the views page each
show their own fixed step for it, never the failure's own text. `surla`
adds the HTTP status, and its retry is **Start agents** on the views page,
which opens anyway:

| `cause` | What could not be used | `surla` prints | The views page shows |
| --- | --- | --- | --- |
| `stopping` | The server is shutting down. | `the project server is stopping; run surla again` | `the project server is stopping; run surla in the project folder to start it again` |
| `herdr` | A Herdr command did not complete. | `Herdr did not complete a team command; open the Herdr app, then click Start agents on the views page` | `Herdr did not complete a team command; open the Herdr app, then try again` |
| `roster_folder` | Herdr's launcher roster folder could not be written. | `Herdr's launcher roster folder could not be written; make data/chorus-launcher/rosters in Herdr's app folder (~/.config/herdr-app unless HERDR_APP_HOME is set) writable by this account, then click Start agents on the views page` | The same, ending `then start the agents again` |
| `project_state` | The project's record or an agent credential could not be read or written. | `this project's private state in <state folder> could not be read or written; make that folder and the folders in it private to this account (chmod 700) and its files too (chmod 600), then click Start agents on the views page` | `this project's private state could not be read or written; run surla in the project folder to see which folder to repair` |
| `role_folder` | A role's working folder could not be read. | `a team role's working folder could not be read; make sure this account can open the project folder <project folder> and its build working folders, then click Start agents on the views page` | `a team role's working folder could not be read; make sure this account can open the project folder and its build working folders, then start the agents again` |

A team request from `surla` that runs out of time prints `project team
request did not finish because Herdr did not answer in time; open the Herdr
app, then click Start agents on the views page: context deadline exceeded`.

**Reminders.** Each version of an assignment that a worker role with work on
the open build has not read is a generation: a new build gives each of its
roles one while that role has work, every non-accepting verdict gives the test
writer one, and an acceptance ends the test writer's work on that build and
gives the builder one while no verification of the build has passed with
current inputs. The server
sends the role one fixed Chorus direct message for all its generations not yet
tried, so at most one per generation, for example `Surla: you have an unread
assignment. Use list_builder_assignments and get_builder_assignment to read
it. Surla reads no replies.` It first registers its own handle, `surla-`
followed by the first 12 characters of the project ID, in the team's swarm
with the role `Surla server. Sends reminders only. Reads no replies.`

A message is sent only after a complete assignment read, and only when the
last read of Chorus's agent list succeeded and has listed the role's handle
without a break for 60 seconds, so a role that has just joined can act on its
first message. Neither the watcher fact nor the terminal state gates a
message: Chorus keeps it until the role's next watcher, `read` or `send`. The
message names nothing about the work and never changes what is owed.

Each post is bounded at 5 seconds, a response longer than 2 MiB fails, and no
byte of a response is kept. Every attempt records one `surla.team.reminder`
span with outcome `sent` or `failed`. A failed one has error status,
`team.post` (`who` or `send`) and `team.failure` (`connection`, `timeout`,
`http_status`, `rpc_error`, `tool_error` or `response`); no caller sees an
error. The one exception is an attempt cut short because the server is
stopping: it is not a Chorus failure and records nothing. A generation is
never tried twice, whatever the outcome. Nothing about reminders is saved, so
after a restart a role that still has an unread assignment gets one message
again 60 seconds after its handle is listed. A `sent` outcome means Chorus
accepted the message, not that the agent read it.

When the agent stays idle with the assignment unread for 90 seconds, the role
reads `needs_you` with attention `assignment`, and the views page asks the
user to show its pane and tell it to read its assignments and to keep its
Chorus watcher running. Supervision never reads screen text, never types into
a terminal and never takes focus without the user's action.

---

## Live target prompt contract

A live target serves one operation for `draft_override`. Surla calls it with
a client it generates from [`design/targetprompt`](../design/targetprompt/design.go);
the target declares the same method in its own Goa design and implements it:

- `PUT /prompts/{prompt_id}` with body `{"template": "<Go text/template>"}`
  and header `X-Live-Target-Key: <key>` answers `200` with
  `{"prompt_id": "...", "version": "..."}`. The version is an opaque,
  non-blank identifier of at most 256 characters, such as
  `sha256:<64 hex digits>`; any other answer is an update Surla cannot
  confirm.
- `401` (`unauthorized`) for a missing or wrong key, `404` (`unknown_prompt`)
  for an unregistered prompt, and `422` (`invalid_prompt`) for a template
  that does not render. The target renders before it saves, so a refused
  template leaves the text in use unchanged.
- The server mints the key for each start. It reaches the program in
  `SURLA_LIVE_TARGET_KEY`, and the program's live entry point passes it to the
  application through `candidate.Spec.Env`.
- The test writer's accepted test program receives only `PATH`, `TMPDIR`,
  `SURLA_LIVE_TARGET_KEY` and `SURLA_LIVE_MODEL_GATEWAY`. It receives no
  provider setting or `SURLA_LIVE_MODEL_PROVIDER`/`SURLA_LIVE_MODEL_NAME`.
  For a direct provider, the executor receives that provider's held settings
  over standard input, applies them after preparation, constructs the
  provider and serves an executor-owned model gateway on `127.0.0.1:<port>`.
  This gateway has no authentication: any local process, including one run by
  another user, can use it while the target runs without receiving the provider
  key. For a chosen
  gateway, its address passes through unchanged. The program uses
  `LiveThroughGateway` in either case.
- An evaluator named in stored verification settings keeps its existing
  environment: the server's environment, without inherited `ANTHROPIC_` or
  `OPENAI_` variables, plus the chosen provider's held settings and
  `SURLA_LIVE_MODEL_PROVIDER`/`SURLA_LIVE_MODEL_NAME`, or
  `SURLA_LIVE_MODEL_GATEWAY`. No model variable is set when no model is
  chosen; the evaluator in the stored verification settings may supply its own model. This profile opens
  no executor-owned gateway. Both profiles build and prepare without any
  provider setting.
- The target's `executor.log`, in its folder under `<cards>/live/`, is the
  program's own output: whatever it prints, a setting included, stays there
  until the folder is removed. Stopping the target removes the folder once its
  processes are gone; a folder left after a failed stop or a server crash is
  named at the next start and kept until you remove it. When a start fails,
  the server exports a protected excerpt only to its diagnostics span, bounded
  to 8 KiB **after** protection. It starts with the last 8 KiB of the raw log
  and reads up to 16 KiB before that excerpt as well. It finds every exact
  occurrence of a held API key, Anthropic auth token, custom-headers or base
  URL value the target was given, including overlapping ones, ones the
  excerpt's start cuts, and the end of the log when it stops partway through
  such a value. It exports each
  run of those bytes that reaches the excerpt once, as `[redacted]`, or, when
  the run is exactly one occurrence of the base URL, as the URL's scheme and
  host (`[redacted]` when none can be read), and every other byte as printed,
  apart from what keeps the excerpt valid UTF-8, as the span exporter
  requires: a character the excerpt's start cuts, or one left unfinished at
  the end of the log, is left out, and any other invalid UTF-8 is exported as
  U+FFFD. If replacement expands the excerpt beyond 8 KiB, it keeps only the
  tail of the protected text, beginning at a UTF-8 character boundary. This
  final bound applies to protected text, not the unprotected raw excerpt.
  So, apart from a base URL's scheme and host, no byte of an exact
  occurrence of those protected values, or of one whose writing stopped at
  the end of the log, is exported. Organization and project identifiers stay
  visible. The same protected value printed in another form, such as quoted,
  escaped, or rewritten by a URL parser or an
  SDK, is not recognized and is exported as printed; a custom-headers value
  is recognized only as a whole, not header by header.
- The starter's test module implements this contract: `override_prompt`
  replaces `<name>.helper.system`. As accepted written tests, its
  `LiveThroughGateway` function uses the model gateway, keeping provider
  settings out of the test program's and application's environments. Its
  `Live` function still serves a direct provider when an operator stores that module as the
  evaluator; that is the stored-evaluator profile above. It passes the
  gateway address it is given, such as `127.0.0.1:9090` or `localhost:9090`,
  to the application.
- [`examples/orders`](../examples/orders/README.md) implements it in its
  `prompts` service and replaces `orders.refund.system`; its evaluator's
  `Live` function uses a scripted model and needs no provider key.

Changing this contract breaks every target that implements it.

---

## MCP resources

- **`surla://golden-path`** (`text/markdown` through MCP `resources/read`;
  a JSON string from `GET /golden-path`). The
  [Golden Path playbook](../internal/goldenpath/GOLDEN_PATH.md) for Goa and
  Goa-AI platforms, followed by the decision components an analysis's
  `ui_composition` may use, generated from the views page's own components
  into [`decision_catalog.md`](../internal/goldenpath/decision_catalog.md), and
  how a composition refers to the analysis's questions and examples by
  1-based position.

---

## Views page

The server serves the views page at `GET /views` on its loopback address.
`surla` opens it with a single-use link in the address fragment
(`/views#link=<link>`), which a browser never sends to a server. On load the
page exchanges the link once for a session, removes it from the address, and
keeps the session only in memory: never in browser storage or a cookie. The
exchange also returns `project_name`, the base name of the project's
physical folder, without its path. The tab's title is the next step and that
name, such as `Confirm change: shop`, from the facts the page last read;
after a failed read of the changes or the team, a lost update connection or
an ended session, it names the recovery instead, such as
`Reconnecting: shop`. Before the exchange succeeds, the title is `Surla`.
Reloading or closing the tab ends the session; run `surla` for a fresh link.
A used or expired link is reported as such. When the server answers `401`, as
it does after a restart, the page keeps everything typed visible and
copyable and says to run `surla` again. Leaving with unsaved edits asks the
browser to confirm.

**Every view.** One line per role that needs the user (for example
`Builder needs you: permission`), with a **Show builder pane** action. The
page keeps one `wait_for_changes` read open and reads again only what
changed. When the server does not answer, the status line says so, every
edit is kept, and the page retries after 5 seconds, doubling up to 30.
Hiding the tab pauses this. A failed read keeps the last facts with a
last-read label. An incoming revision never replaces typed edits: the page
holds it beside them (**Switch to Incoming Card**).

| View | What it shows and does |
| --- | --- |
| **Describe** | The project's changes ([`list_changes`](#list_changes)), opening directly on the only one. With none: `This project has no changes yet. Ask the guide to propose a change; it appears here by itself.` A change with a waiting proposed revision is marked **New revision proposed**. Each change shows its latest saved run, such as **Latest saved run: passed**, or **No saved runs**, and under a run what `inputs_current` says: `Matches the agreed revision and selected tests.` or `Earlier inputs or no current test selection. Ask the builder for a check of the agreed revision and selected tests.` |
| **Team** | Each registered role's state, its Chorus watcher, and its assignment status (**No assignments** for the guide, and **Assignment unread**, **No unread assignment**, **No work now** or **Assignment status unknown** for a worker role), with a **Show … pane** action per role, such as **Show guide pane**, and **Start agents**, which says what it returned and when. With no role: `No agents are registered. Start agents prepares the guide; building adds the required roles.` Nothing runs on a refresh. |
| **Story** | The Story saved with the shown revision, one row of step cards per flow: a finished flow is marked **will be tested end to end**, others **draft: not tested** with the reasons. Below it, the current Story evaluation ([`get_story_refresh`](#get_story_refresh)): none, running, failed, or succeeded, and how it differs from the change's Story; a draft is not part of the change until the guide revises the change with it and the user confirms. |
| **Live target** | Whether a live target is starting, running, ended unexpectedly or not confirmed stopped. A running target shows its commit, address, accepted prompt versions (never the text), and **Stop live target**, which asks first. An ended target shows its commit, launch and observed-end times, and the builder's recovery step. Below it, the model live targets started afterwards use and a form whose **Apply** sends [`configure_model`](#configure_model); a refusal names a missing key's variable. |
| **Agree** | The Change Card: questions to answer, examples, preserved behaviors, and **Confirm Intent & Lock Examples** (on an agreed card, **Refine Confirmed Answers & Examples**). A proposed revision appears above the agreed one with everything adopting it would confirm (changed request and summary, questions and examples marked **New**, **Edited**, **Kept** or **Removed**, Story and design changes), **Adopt this revision** and **Decline**. A stale submission offers **Load Latest Card**. On a confirmed card the **Build** section comes first, with the saved agreement below it under **Agreed:** and the request. **Agreement details** and **Refine agreed answers and examples** remain available for review and refinement; an active edit stays open. **Review saved approval** opens the record in Approve. **Build** opens the same saved build section directly. |
| **Architecture** | [`preview_architecture`](#preview_architecture) for the revision shown. |
| **Check** | The change's verification runs and each report's scenario outcomes, hidden scenarios anonymous. **Check started** shows the request's start time in local time, not a completion time; the exact recorded timestamp stays in details. A single saved run needs no selector. With promotion settings, a completed run shows its actual binary folder under **Binaries built by this run**, as selectable code; the approval error panel repeats the selected run's folder. A run of written tests shows the `notice` of its `test_source`. **Candidate and run details** shows the run's candidate commit and its tests commit, labeled **Accepted tests commit** for written tests and **Evaluator commit** for an evaluator named in stored verification settings. |
| **Approve** | The exact selected run’s recorded Story flows, required tests and outcomes; excluded drafts and unknown mapping; test source, human judgment limits and verified commits. **Test details and saved runs** discloses the safe report and history. **Verified inputs and digests** discloses full bindings. Next owns the single approval action, based on [`review_verification`](#review_verification); blocked or unverified records retain the owner’s reason. Unsaved Agree edits remain available on return. |
| **Diagnostics** | A drawer listing recorded errors, with **All statuses** and **Without error status**, older pages, and **Retry from newest** after a failed read. |

**The Build section.** It appears once the change is confirmed or has a
saved build, and then always offers **View team**. While the agreed
revision has no open build in `building`, `passed` or `merged` (and
`drafted_before_drop` is `false`), it offers one start button: **Build this
change** before any build, **Build current agreed revision** when the change
has a saved build (an earlier revision's build or a `dropped` build), and
**Build agreed revision** in either case while a proposed revision waits.
When replacing an earlier revision's open build whose `taking.state` is
`building`, that button asks first (**Replace the earlier build with the agreed
revision?**, with **Build again** and **Cancel**) and sends `replaces`. While
the agreed revision's open build is `building` or `passed`, it offers **Build
again…** instead, which asks first (**Build the agreed revision again?**, with
**Build again** and **Cancel**, sending `replaces` when `building`). A merged
build of the current agreement keeps completion guidance (`Merged.`, `It is in
your branch. Build another change or refine this one when there is more to do.
No role has work on this build.`) and offers **Build again…** as a secondary
action; confirmation (**Build the agreed revision again?**, with **Build again**
and **Cancel**) starts a new numbered build from the branch as it is now, with
fresh unread assignments for the same agreement. Earlier reports do not
establish that the new build passed. A merged build of an earlier agreement
keeps the primary **Build current agreed revision** action; while a proposed
revision waits, the button reads **Build agreed revision** and is no longer the
primary action. When `drafted_before_drop` is
`true`, the page instead explains refresh (`refresh_story`), revision
(`revise_change`) and adoption before building; a proposal already waiting may
predate the drop. Before any build and inside the rebuild confirmation, it
explains which agents start: the builder and, unless the project uses
configured tests, a test writer. Beside every control that starts a build,
including the start button of a change that already has a saved build, it says
that, from Build on, the tests and the builder's code run on this machine as
ordinary programs, with your access to files and the network, that nothing
confines them, and that approval decides only what merges. While the change's
open build is `building` or `passed`, it also offers **Drop…**, which asks
**Drop change `<change_id>`?** with **Drop change** and **Keep building** (when
`building`) or **Keep this build** (when `passed`), calling
[`drop_build`](#drop_build). When another change's open build blocks starting a
build (`previous_change_open`), a blocking panel names that change, shows its
`git merge` commands when its build is `passed`, and offers **Open change
`<change_id>`** and **Drop `<change_id>`’s build…**. Rebuild and drop questions
are asked one at a time, and opening the rebuild question first re-reads the
saved build. It refuses to start from an
unconfirmed change (`Confirm the change before building it.`) or an older
revision, and builds the agreed revision even while the editor holds unsaved
edits. It shows the open build's `taking` state (`building`, `passed`, `merged`
or `dropped`, with any kept branches), when the build started, the required
test count, whether
each assignment is read, the test writer's state, the accepted tests commit,
the `not_judged` sentence, and, when tests need changes, the findings read
with [`get_test_findings`](#get_test_findings). While the open build's
`taking.state` is `passed`, it shows **To take this change, run in the project
folder:** with `git merge <candidate_commit>` (verified on `surla/build-<n>`)
first and then, for written tests, `git merge <tests_commit>` (verified on
`surla/tests-<n>`). A start's team outcome is shown with the time of that
start, not as current health.

When tests need changes, the test writer is the actor that fixes and reports
them; **Read findings** opens **What Surla told the test writer**. A matching
passed check gives the build **Review result**, opening that exact run.
The selected run refreshes its input match when the agreement or build
changes, including when it is older than the latest run. A stale, pending or
unreadable match disables approval while keeping the saved report and any
unsaved edits visible. A written-tests run shows its isolation notice directly
under the outcome.

**Before the agreed revision changes.** When the agreed revision has a build,
the page says what a different agreed revision does to it, above **Adopt this
revision** and, once an answer or an example is edited, above **Refine
Confirmed Answers & Examples**: the build stays on the earlier revision, and
a build of the changed revision replaces both assignments and clears the test
writer's report and verdict, so its tests must be reported, checked and
accepted again before verification. With stored verification settings it
says instead that such a build updates the builder's assignment and that
verification keeps using those settings. When the page could not read the
build, it gives the same warning for any existing build. A confirmation that
changes nothing keeps the revision and its build.

**Approval on the page.** The frame shows the project, journey, agreed change,
what needs you and one Next action. On a phone, Next moves to a bottom surface
with reserved document space. Approve presents the run’s recorded evidence;
**Refresh result** reads the owner’s current requirements for that same run.
When a newer check is available, **Review latest check** opens it for review;
the shown result stays selected until you choose it. Earlier results remain
selectable in **Test details and saved runs**, and a saved approval stays bound
to its own verified inputs. Only an `available`
observation enables **Approve this result**. Pending or failed reads keep the
evidence and disable submission. A refusal stays on the same stage with its
reason; success shows the recorded approval and moves focus to it. A saved
approval without a readable run remains available through **Review saved
approval**, with **Check current approval** to read the card again.

After the approved result's current build is merged, Next points to describing
the next change. The selected approval keeps its own evidence. An unrelated
historical result does not borrow the current build's completion; a pending or
failed build read withholds merge/completion guidance, and **Read current
build** retries that owner read.

The recorded approval shows **Builder's commit** and **Tests' commit**. While
the current agreement’s open build has `taking.state: passed` and the run is
an eligible result of that build, it shows **To take this change, run in the
project folder:** with `git merge <candidate_commit>` first and, for written
tests, a separate `git merge <tests_commit>`. The branch labels show where
those commits were verified. An earlier agreement’s build receives no merge
instruction on Approve; its commit bindings remain readable. Existing Check
history preserves older results. Approval records permission and never merges
or deploys. An unverified saved record remains explicitly unverified.

**What the page never shows or does.** It never shows prompt text or
structural `diff` results, and it starts no live target and sends no prompt
draft: the builder does those. A guide's `ui_composition` is an
illustration, not the Story.

**Browser policy.** The page is a fixed shell with no project data, built
from [`ui/checkpoint/`](../ui/checkpoint/). It sends only the operations
listed in `PAGE_OPERATIONS` in [`ui/checkpoint/build.mjs`](../ui/checkpoint/build.mjs),
whose request types and result validators are generated from the OpenAPI
description, and refuses an answer that does not match. A top-level browser
navigation to `GET /views` may come from any site when marked
`Sec-Fetch-Mode: navigate` and `Sec-Fetch-Dest: document`, including a browser
extension opening the link. This serves only the fixed shell, with `no-store`;
the one-use credential stays in the fragment. Cross-site iframe and fetch
requests to the shell are refused. The address/Origin checks remain in force,
and exchanging the link or using a browser session still requires
`Sec-Fetch-Site: same-origin`. The server sends this `Content-Security-Policy`:

| Directive | What the browser allows |
| --- | --- |
| `script-src 'sha256-<digest>'` | Only the page's one inline script; nothing is evaluated from a string. |
| `style-src 'unsafe-inline'` | Inline styles only; no style sheet loads. |
| `connect-src 'self'` | Requests only to the page's own address. |
| `default-src 'none'` | Nothing else: no images, fonts or frames. |
| `frame-ancestors 'none'` | The page is never framed. |
| `form-action 'none'` | No form submission. |
| `base-uri 'none'` | The base address cannot change. |

---

## Operation diagnostics

The server records safe summaries of the operations callers make, with no
collector to set up. On the views page, open **Traces**; agents use
[`list_traces`](#list_traces).

**What is saved.** Every call by the guide, the builder or the test writer,
every operation that changes something, and the user's reads sent with `POST`
(`preview_architecture`, `diff`, `read_check_output`). The user's `GET` reads,
which the page repeats, are saved only when they fail with an error status or
a panic; refused reads, such as `forbidden` or `not_found`, are expected
answers and are not saved. `wait_for_changes` is never saved. Each HTTP or
MCP call has a `surla.request.<operation>` span; a refusal is recorded
without error status, and an unexpected fault records an error. Calls refused
before authentication or decoding, verification processes, the check helper
and outside services are absent. An `unset` status does not prove success.

**Where it is kept.** The history is `traces.jsonl` in the state folder's
`cards/` folder, created when the first span finishes, append-only, readable
only by your account (`0600`), and read again after a restart. Each record is
at most 64 KiB, with names, summaries and values shortened when needed. The
file holds a SHA-256 hash of each caller for read isolation; neither the
caller nor the hash appears in results or exported spans. The hash is not
anonymization: keep the file private. To discard the history, stop Surla,
then remove the file.

**The server's terminal.** When a `propose_change` or `revise_change` span
ends with an error status, such as a storage fault, the server's Herdr
terminal prints one line with its summary and error messages. That line can
hold private evidence. An analysis refused as `unprocessable` prints nothing.

**Exporting.** Set `OTEL_EXPORTER_OTLP_ENDPOINT` (the collector's base URL) or
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` (the full trace URL, which wins) in the
server's environment to send full spans to an OpenTelemetry collector over
OTLP/HTTP. Exported spans keep their original fields and status and carry no
caller identity; restrict access to the collector. The check helper exports
its own spans the same way, as children of the call's span. With neither
variable set, Surla sends nothing.

---

## Reference platform, repository checks and layout

### Reference platform: `examples/orders` and `examples/orders-eval`

An order-refund service and its separate evaluator, which checks payment
effects, manager approval, worker recovery and token budgets.

[`examples/orders`](../examples/orders/), the application:

- [`design/`](../examples/orders/design/): the public `orders` HTTP service
  (`chat`, `run_status`, `approve_refund`), the `prompts` service that
  follows the [live target prompt contract](#live-target-prompt-contract),
  internal `commerce` and `payments` services, the `refund` agent
  (`Inject("tenant_id", "customer_id")`, `BoundedResult`, `Confirmation`),
  and the C4 model in [`gen/structurizr/model.json`](../examples/orders/gen/structurizr/model.json).
- [`AGENTS_QUICKSTART.md`](../examples/orders/AGENTS_QUICKSTART.md): the
  generated guide for coding agents.
- [`internal/orders`](../examples/orders/internal/orders/): the refund rule.
  An order total up to \$50.00, tax and shipping included, is refunded as
  store credit at once; a larger total is refunded to the card after a
  manager approves. Prompt changes are accepted only with the live key.
- [`internal/assistant`](../examples/orders/internal/assistant/): runs the
  `refund` agent, resumes after the manager's decision, and keeps runtime
  prompt overrides in memory.
- [`internal/refundflow`](../examples/orders/internal/refundflow/): a durable
  Temporal workflow that refunds each order exactly once.
- [`internal/runstore`](../examples/orders/internal/runstore/): a
  transactional SQLite store for `goa-ai` runs.

[`examples/orders-eval`](../examples/orders-eval/), the evaluator: its
[acceptance tests](../examples/orders-eval/acceptance/acceptance_test.go)
boot the application against
[commerce and payment simulators](../examples/orders-eval/sim/) and a
[scripted model](../examples/orders-eval/scriptedmodel/), and run five
scenarios (`store_credit_within_limit`, `card_refund_needs_manager`,
`refund_survives_worker_crash`, `pending_approval_survives_api_restart`, and
the hidden `other_customers_order_refused`) plus a token-budget regression.
See [`examples/orders/README.md`](../examples/orders/README.md) for a
walkthrough.

### Repository checks

The [CI workflow](../.github/workflows/ci.yml) runs 15 Linux jobs on every
pull request. It executes complete ordinary tests, without `-short`, plus
product linking, every added build profile and generated-contract checks.
[Integration](../.github/workflows/integration.yml) adds one macOS job on pull
requests into `team-start` and `main`, exercising native process and filesystem
contracts. Pushes to `main`, `team-start` and `builder-copy` run the complete
pipeline: all 14 macOS ordinary jobs, the Linux checks, tagged journeys,
browser/Herdr suites and Docker checks. Nightly runs at 07:17 UTC and manual
full runs execute that same pipeline once the workflow is on the default branch.

| Linux PR owner | Work |
| --- | --- |
| Eleven root groups | Two complementary control-plane page groups; two complementary groups of other control-plane tests; selected compiler tests from the test writer and design evaluator, plus each package's remaining tests; the Story-table generator CLI test and its complement; other packages outside/inside the concurrency-owner list. |
| Story and Orders evaluator | All ordinary tests of both modules, selecting each module's own Go toolchain. |
| Orders | All ordinary Orders tests with its module toolchain. |
| Build profiles | Ordinary product linking, then build and full vet for `e2e`, `scaffold`, `herdr`, `browser` and `e2e,docker`. The combined Docker profile includes its container tests; `docker` alone adds none. |
| Generated contracts | Formatting, concurrency-owner checks, TypeScript, root/target-prompt/example Goa regeneration, page consistency and Node consumer tests. |

[The root selector](../.github/scripts/test-root.sh) owns the complete Linux
partitions and the full macOS push/nightly/manual partitions. New roots, examples and
fuzz seeds belong to complementary groups. [The native selector](../.github/scripts/test-native.sh)
owns the macOS integration PR cases: process groups and signals, executable and descriptor
identity, physical paths and aliases, permissions, atomic rename, locks, staging,
and project-server start/stop. Its package owners and exact names keep portable
rendering, design, Story, test-writing and prompt logic on Linux for PRs; full
macOS push/nightly/manual execution still covers them. When a native boundary gains or
renames a test, update that selector in the same change. New packages retain an
owner in the complete suites, including packages without tests. Project page tests stay with the project package; control-plane page
names belong only to the page groups. Separating the other plain packages
from concurrency owners removes the old serial job phase without reducing
race coverage. Every command uses `-count=1` and full vet. No job reruns the
same ordinary roots under an added tag.

Selected test runs must emit at least one named Go test, example or fuzz-seed
event. The [output guard](../.github/scripts/testcheck/main.go) preserves the original JSON stream and fails
when a stale selector runs nothing; packages without tests may still share a
run with tested packages. Explicit Bash pipeline failure handling preserves
the test command's own failures. Compiler-input preparation intentionally
compiles with no test execution and does not use this guard.

The integration workflow reuses the Linux workflow on pushes, nightly and
manual runs; integration PRs already receive those Linux jobs from their PR
workflow. The selected native macOS job runs before integration, including
Darwin-selected process, filesystem-alias and browser-opener behavior. Complete native root groups,
both nested-module jobs, generated checks and browser-profile vet run on pushes,
nightly and on manual full runs. Those events run only the complete native
owners, keeping execution separate from the selected PR job.

The trusted native compiler producers run only outside pull requests. Native
integration PR checks use restored archives directly; they do not wait for a
producer skipped by that event. Push/nightly/manual consumers wait for successful
producers. Parent and reused-workflow concurrency groups are distinct, so the
Linux workflow cannot cancel its integration caller.

One **Full / compiler-inputs** job per operating system prepares and publishes
module and compiler archives without running tests. Outside pull requests,
full-root groups wait for these producers, then restore their inputs; PR checks
remain independent read-only consumers. A missing module or compiler archive
triggers preparation of the declared race/plain compilation,
CGO-free product dependencies and pinned Goa generator, then compiles Story,
Orders and Orders Eval with their own dependency versions and plain/race
selections, including the tagged acceptance tests. Only trusted non-pull-request
runs publish missing archives. A later test failure or timeout does not
invalidate these compiler inputs or prevent their reuse; checks still run
in their existing jobs. Producers have a separate 20-minute backstop; the full
root jobs keep their 30-minute backstop and package alarms. Preparation and
tests no longer consume one shared job budget. These bounds are not measured
completion times. The archive version changes when this declared closure
changes; an immutable archive cannot gain missing inputs after an exact hit.
No arbitrary first-finishing shard can save an incomplete common
closure. Archive keys include the exact Go toolchain, operating system and
architecture, module inputs, starter templates and published fixture versions;
compiler keys also include the hosted OS image family and race profile. Module
archives remain separate from compiled-package archives. Base-branch pushes
populate archives that pull requests can restore; private project state and
credentials are never cached.

The producer's macOS and Linux dependency lists contain only standard-library
native inputs: `net`, `os/user`, `runtime/cgo` and the race runtime. Orders'
`modernc.org/sqlite` dependency is pure Go. Go's own action keys include the
Go toolchain, compiler options and C compiler identity. The archive uses the
OS image family (`ImageOS`), allowing concurrently served weekly image builds
with the same system ABI to share it. Go does not detect arbitrary external
C-library changes; adding such a dependency requires reviewing its native
profile and archive key before sharing compiled inputs across image builds.

Each job summary reports module and build archive hits separately. Missing
inputs, dependency/toolchain/template/image changes, expiry or eviction can
cause a cold or partial restore; those durations are reported separately.
Cache restoration never skips a check. Both pull-request Linux checks and
full suites use `-count=1`, while retaining Go's compiled-package cache.

Race instrumentation follows
[the production concurrency owners](../.github/race-packages.txt).
The wrapper uses Go's active `TestGoFiles` and `XTestGoFiles` metadata to
instrument listed packages with tests. A listed package without tests still
builds and vets in the plain group; adding its first test automatically
selects race instrumentation. The cold producer uses the same selection.
[The syntax check](../.github/scripts/racecheck/main.go) rejects handwritten
production goroutines, synchronization imports or channel operations outside
that list, and rejects listed owners that no longer contain them. It checks
platform-specific files too, excluding generated code, test programs and
fixtures. Other packages run without instrumentation. Generation, starter,
whole-journey and Docker e2e jobs retain `-race` until comparable warm
measurements justify a change. Verification's orchestration package has no
production concurrency; its supervisor, candidate and suite owners retain
race coverage.

The five-minute warm target includes setup, restore, tests and scheduling
rather than test output alone. The full-root 20-minute package alarms and
30-minute job bounds remain cold/hang backstops; generated checks retain their
five-minute bound and profile compilation its ten-minute bound. These are
execution limits, not measured completion times. Cold measurements and warm
runs must be reported separately; a restored archive never substitutes for a
check or proves five-minute feedback.

To run one full-root group locally:

```sh
CI=true bash .github/scripts/test-root.sh cli-first </dev/null
```

The **Full / docker-check** job stays in the integration workflow on trusted
pushes, nightly and manual runs. On Ubuntu with its Docker engine, it runs the
intact dependency-repair journey through one server, then the container owner's
`TestContainerEngineEndToEnd` and `TestLocalEngineContainerLifecycleFacts`.
It keeps `-tags=e2e,docker`, race instrumentation, the journey's 40-minute
package alarm, the container owner's 20-minute alarm and the 50-minute job
backstop. Pull requests do not execute this job automatically. To run the
journey locally with a running Docker engine:

```sh
go test -race -count=1 -timeout 40m -tags=e2e,docker -run '^TestCheckProjectRepairsDependenciesOnOneServer$' ./cmd/surla </dev/null
```

### Committed test module profiles

CI downloads the root module requirements and these committed profiles before
running their respective full-suite partitions:

- `internal/project/testdata/story-fixture` pins the Story and Goa target graph
  for the succeeded Story page test.
- `internal/project/testdata/story-evaluator` pins the evaluator graph for that
  Story target, with a relative replacement pointing to the target profile.
- `internal/testmodule/testdata/goa-goaai-dsl` pins the target graph for a
  design importing the Goa and Goa-AI DSLs, used by the architecture preview
  test.
- `internal/testmodule/testdata/goa-goaai-evaluator` pins the evaluator graph
  for that target and replaces it with the adjacent target profile.

Each test copies its target into a private workspace, checks the design and
module list with fetching disabled, and runs the real evaluator. Missing inputs
fail with a fixture-input diagnostic. These profiles describe the fixed graphs
rather than copying unrelated root requirements.

After changing the root Story, Goa, Goa-AI, or Model requirements, update the
matching versions in the profiles and refresh their checksums. For example,
with the root requirements currently in this repository, run:

```sh
GOWORK=off go -C internal/project/testdata/story-fixture mod edit -require=github.com/goadesign/surla/story@v0.0.0-20261111183703-4568fcf5bb85 -require=goa.design/goa/v3@v3.35.1-0.20261114184515-dee88e893eb3 </dev/null
GOWORK=off go -C internal/project/testdata/story-evaluator mod edit -require=github.com/goadesign/surla/story@v0.0.0-20261111183703-4568fcf5bb85 -require=goa.design/goa/v3@v3.35.1-0.20261114184515-dee88e893eb3 -require=goa.design/goa-ai@v0.90.1-0.20261111075239-0114fb9d40d3 -require=goa.design/model@v1.16.11-0.20261030164016-5e17c2e932fb </dev/null
GOWORK=off go -C internal/testmodule/testdata/goa-goaai-dsl mod edit -require=goa.design/goa/v3@v3.35.1-0.20261114184515-dee88e893eb3 -require=goa.design/goa-ai@v0.90.1-0.20261111075239-0114fb9d40d3 </dev/null
GOWORK=off go -C internal/project/testdata/story-fixture mod download all </dev/null
GOWORK=off go -C internal/project/testdata/story-evaluator mod download all </dev/null
GOWORK=off go -C internal/testmodule/testdata/goa-goaai-evaluator mod edit -require=goa.design/goa/v3@v3.35.1-0.20261114184515-dee88e893eb3 -require=goa.design/goa-ai@v0.90.1-0.20261111075239-0114fb9d40d3 -require=goa.design/model@v1.16.11-0.20261030164016-5e17c2e932fb </dev/null
GOWORK=off go -C internal/testmodule/testdata/goa-goaai-dsl mod download all </dev/null
GOWORK=off go -C internal/testmodule/testdata/goa-goaai-evaluator mod download all </dev/null
```

Replace the versions above with the new root requirements and commit the
updated module files and checksums. Run both tests with fetching disabled to
check the actual evaluators against the updated inputs.

All tagged full suites run on integration-branch pushes, nightly at 07:17 UTC
and on a manual **Run workflow** request (`CI=true gh workflow run integration.yml --ref <branch> </dev/null`).
Manual dispatch requires `integration.yml` to exist on the repository's default
branch, even when `--ref` names another branch. Scheduled runs also require the
workflow on that branch and execute its latest commit. Until then, full coverage
comes from an integration-branch push or a rerun of that push run.
This runs the verifier, generation, starter, whole-project journey and Orders
acceptance suites alongside complete native ordinary coverage. Once manual
dispatch is available, it can check a candidate head before merging. Tagged jobs
select only their named roots: ordinary CLI, verifier and scaffold tests belong to the full
root partitions, and ordinary acceptance budget and hook tests belong to
the ordinary Orders evaluator owner. The separate journey job owns the
whole-project journey; the verifier retains its macOS and Linux profiles. The workflow lists
each selected set explicitly, so adding a build tag does not rerun ordinary
tests in another job. **Full / generation** uses a
60-minute package alarm and a 70-minute job backstop for the suite's project
compilations on slow runners, including cold builds. The job limit also
covers setup and downloads. These are execution bounds, not measured
completion times or a reduction in compilation work.
Passing ordinary checks or compiling tagged files does not prove the tagged
paths that build and execute candidate code. Changes to candidate execution
or starter generation need the applicable full suite. On macOS, with the
Temporal CLI for the first three:

```bash
(cd examples/orders-eval && go test -race -tags e2e ./acceptance)
go test -race -tags e2e ./internal/verify
go test -race -count=1 -tags e2e ./cmd/surla
go test -race -count=1 -timeout 60m -tags scaffold ./internal/scaffold
```

The **Full / opt-in (Linux)** job runs the named native browser tests and the
isolated Herdr server lifecycle on integration-branch pushes, nightly and on
manual full runs. The Linux PR profile job compiles and vets both tagged profiles. The scheduled job requires
hosted Chrome, Node 26 and digest-checked Herdr Core 0.1.21; missing tools fail setup.
Its explicit browser roots cover page policy and sessions, phone layout, build
and test review, keyboard access, and browser cleanup after its parent ends.
They execute independently of ordinary project tests. The Herdr root, `TestHC`,
runs the real headless Core and Surla server with isolated state and fixture
window and team effects; it starts no coding agent or model. Each package command
has a five-minute alarm and the job has a ten-minute backstop. These bounds are
not measured completion times. The complete selected set is in
[the integration workflow](../.github/workflows/integration.yml).
The page-policy journey also checks that a session ends on reload, browser
storage stays empty, long Story rows scroll within the Story view, and Live
fits a 375-pixel-wide screen.

Local native checks need Chrome or Chromium and Node 22 or later. Set
`SURLA_TEST_BROWSER` when the browser is not installed in a standard place.
For example, run the phone layout contract with:

```bash
go test -race -count=1 -timeout 5m -tags browser -run '^TestTeamAndBuildAtPhoneWidth$' ./internal/project </dev/null
```

### Project layout

```text
.
├── .github/workflows/ci.yml     # Complete Linux PR checks, reused by integration
├── .github/workflows/integration.yml # Native PR coverage and full push/nightly/manual journeys
├── cmd/surla/                   # The surla command: project start and stop, settings, init, preview, diff, gen, verify, approve
├── design/                      # Goa design of the server's contract
│   ├── design.go                # Change Cards, checks, live target, verification, approval
│   ├── changes.go               # The change list and the views page's waiting read
│   ├── builds.go                # Builds and the builder's and test writer's assignments
│   ├── build_start.go           # A build start's saved build, team outcome and refusals
│   ├── team.go                  # The user's team read and actions
│   ├── project.go               # Management: identity, team, stop, browser link
│   ├── story_refresh.go, traces.go, cp1_analysis.go
│   └── targetprompt/            # The live target prompt contract
├── docs/                        # How it works, reference, architecture playbook, product direction, Story test design
├── examples/
│   ├── orders/                  # Reference order-refund platform
│   └── orders-eval/             # Its evaluator, simulators and acceptance scenarios
├── gen/                         # Generated HTTP, JSON-RPC and MCP code (never edited by hand)
├── internal/
│   ├── controlplane/            # Change Cards, builds, judging reports, checks, live target, verification, approval, operation policy, views page
│   ├── project/                 # Project state, credentials, browser sessions, the team and its supervision
│   ├── testwriter/              # The test writer's working copy, the check of its tests, and their commit
│   ├── verify/                  # Verification runs, the builder view of reports, and approval checks
│   ├── verifyrun/               # Supervised verification and live-target processes and run directories
│   ├── designeval/              # Design evaluation and the C4 extractor
│   ├── diff/                    # Structural design and dependency diff
│   ├── goldenpath/              # The Golden Path playbook and decision component catalog
│   ├── scaffold/                # The surla init starter and its templates
│   ├── traceview/               # The saved operation diagnostics
│   ├── genpublish/              # Atomic replacement of generated folders
│   ├── targetprompt/            # Generated client of the live target prompt contract
│   ├── procgroup/               # Process groups for the commands Surla starts
│   ├── abspath/, renameat/, shquote/   # Path resolution, atomic renames, shell quoting
│   └── testmodule/              # Go module fixtures for tests
├── story/                       # The Story language, a separate Go module
├── ui/checkpoint/               # Views page source; build.mjs writes checkpoint.html, its types and the decision catalog
└── verify/
    ├── candidate/               # Builds and runs agent-written code: pinning, environment, process groups
    └── suite/                   # The evaluator suite command contract (suite.Main)
```
