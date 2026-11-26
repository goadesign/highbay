# Golden Path Architecture & the Goa Design Suite

**[← Back to README](../README.md)** · **[← How Highbay Works](HOW_IT_WORKS.md)** · **[CLI & MCP Reference →](REFERENCE.md)**

When coding agents build a platform from scratch without architectural guardrails, they default to fragile shortcuts: every service exposes ad-hoc HTTP JSON routes, every package calls LLM provider SDKs directly with no central rate limiting or cost tracking, `tenant_id` is passed inside LLM prompt arguments, and multi-step workflows run in fragile in-memory goroutines that lose state on deploy.

Highbay embeds a single-source-of-truth **Golden Path Playbook** ([`internal/goldenpath/GOLDEN_PATH.md`](../internal/goldenpath/GOLDEN_PATH.md), 12 numbered sections) and serves it as an MCP resource (`highbay://golden-path`) to every agent on the project's team. The guide, the coding agent the user talks to, reads it before it writes the analysis it sends with `propose_change` or `revise_change`. The same resource ends with the decision component catalog for that analysis's optional `ui_composition`.

This page has two readers. A platform author follows [the seven rules](#the-7-golden-path-rules) and [the tools behind them](#how-the-goa-design-suite-fits-together). A contributor to Highbay finds [who owns what in Highbay](#who-owns-what-in-highbay).

---

## The 7 Golden Path Rules

### 1. Design Is the Single Source of Truth & Standardized Layout (`design/*.go` → `goa gen`)
Every Highbay project shares the same two-module Go layout (`design/`, `gen/`, `cmd/`, `internal/`, and `eval/`), and every service, method, payload, error contract, agent, toolset, MCP resource, and C4 container is declared in `design/*.go` first. Running `goa gen` deterministically generates transport codecs, OpenAPI 3 specs, gRPC protobufs (when compiled via standalone `goa gen` with `protoc`), workflow registrations, and `AGENTS_QUICKSTART.md` at **0 LLM tokens** (in the [`examples/orders`](../examples/orders/gen) reference platform and [`Highbay's own gen/`](../gen), generated Go accounts for over 70% of non-test `.go` code). Highbay's built-in `hbay gen` and candidate `generate_code` generate HTTP, JSON-RPC, and MCP transports and refuse designs declaring `GRPC` transports because `protoc` is not bundled; see [`hbay gen`](REFERENCE.md#hbay-gen). Files under `gen/` are never edited by hand.

### 2. Single HTTP/SSE/MCP Edge Gateway, Internal gRPC Services
- **One Edge Service**: Expose a single stateless HTTP/SSE/MCP edge service that terminates TLS, validates authentication tokens (`JWTAuth`, `OAuth2`, `APIKeyAuth`), enforces CORS, and streams real-time updates to clients.
- **Internal gRPC Topology**: All internal microservices, domain backends, and model gateways communicate over generated Goa `gRPC` clients and servers with deadline propagation and strict schema validation.

### 3. Central Stateless LLM Model Gateway
No domain service or agent worker holds raw LLM provider API keys or calls external provider endpoints directly:
- **Gateway Service**: Front all model calls with [`goa.design/goa-ai/features/model/gateway`](https://github.com/goadesign/goa-ai) (`RegisterGRPCServer` and `NewGRPCClient`) over `Complete`, `Stream`, and `CountTokens`.
- **Distributed Token Rate Limiting**: Wrap the gateway with `middleware.NewAdaptiveRateLimiter` backed by a [Pulse](https://github.com/goadesign/pulse) replicated map (`rmap.Join`) so token-per-minute budgets and AIMD backpressure are coordinated across replicas.
- **Fail-Closed Usage Attribution**: Goa-AI's runtime and model gateway emit OpenTelemetry `gen_ai.*` spans (exported via [Clue](https://github.com/goadesign/clue)'s `clue.ConfigureOpenTelemetry`) and record `TokenUsage` on every call. Treat a call whose usage is missing or only partly reported as unknown, never as zero, so budget gates fail closed (the `examples/orders-eval` gateway counts such calls in `Usage.Unreported` and fails the budget check).

### 4. Security & Identity by Construction (`Inject`, `BindField`, `ServerData`)
Never trust an LLM to supply or preserve security principals, tenant IDs, or credentials:
- **Edge Extraction & `Inject`**: Extract caller identity from verified tokens at the HTTP/MCP edge, store it in a typed Go context, and declare `Inject("tenant_id", "user_id")` on tools so those fields are stripped from model-visible JSON schemas and populated server-side by `ToolInterceptor.Inject`.
- **Consumer-Side `BindField`**: When an agent consumes a broader internal service method, use `BindField` to narrow the model-facing input schema to only the arguments the LLM is allowed to choose.
- **Off-Wire `ServerData`**: Keep audit metadata, citations, and UI artifacts in `ServerData` so they never bloat model context windows (only `ServerData` explicitly bridged via `NativeImage()` is attached to the model turn).

### 5. Deterministic Service Toolsets (`BindTo`) vs. Agent-as-Tool (`Use(AgentToolset(...))`)
- **Service Toolsets (`BindTo`)**: Keep CRUD operations, state transitions, and external API calls as deterministic Goa service methods bound via `BindTo`.
- **Agent-as-Tool (`Use(AgentToolset(...))`)**: When a sub-task requires multi-step reasoning over noisy data, encapsulate it in a dedicated child `Agent` (`Export`) and consume it via `Use(AgentToolset(...))` so the child runs in its own isolated context window and returns a bounded summary to the parent.
- **Generated Agent Guardrails (`AGENTS_QUICKSTART.md`)**: `goa gen` automatically documents every tool's `BindTo` target, server-injected fields, `BoundedResult` pagination, `Confirmation` gates, and `specs/tool_schemas.json` (`input_binding`) in `AGENTS_QUICKSTART.md`.

### 6. Durable Temporal Workflows & Zero-Wait Human Suspension
- **Deterministic Replay**: Multi-step agent runs execute on the Goa-AI Temporal engine with a durable run log (`runtime/agent/runlog`; [`examples/orders`](../examples/orders/) stores runs in SQLite through `internal/runstore`), keeping workflow definitions pure and isolating I/O inside registered activities.
- **Zero-Wait Human Approvals**: High-consequence tools declare design-time `Confirmation(...)`. The run then suspends and saves its suspension, holding no worker thread, until an authorized principal decides through the public API, which resumes it by calling `PrepareContinuation` and then `StartPrepared` (as `examples/orders/internal/assistant` does).
- **Payload & Context Bounds**: Paginate collection tools with `BoundedResult`, configure `RunPolicy` (`MaxToolCalls`, `TimeBudget`, `Cache`, `History` `Compress`), and keep each tool and activity payload small: Goa-AI rejects any single encoded value that crosses one workflow-engine call above 1 MiB (`engine.MaxPayloadBytes`), well under [Temporal's 2 MB per-request payload limit](https://docs.temporal.io/evaluate/cloud/limits#transaction-payload-size-limit). Reduce large domain results to durable references before they reach that boundary.

### 7. Customizing Generated Artifacts via Goa Plugins (`codegen.RegisterPluginLast`)
When a platform needs custom generated files or wants to tailor artifacts produced by `goa` or `goa-ai`, register a Goa plugin via `codegen.RegisterPluginLast("my-plugin", "gen", nil, generate)` (`goa.design/goa/v3/codegen`). Your `GenerateFunc` receives the evaluated `eval.Root` roots and the complete `[]*codegen.File` list, allowing you to inspect or transform generated sections cleanly during `goa gen` without post-processing scripts.

---

## How the Goa Design Suite Fits Together

A Golden Path platform is built with these tools:

- **[Goa](https://github.com/goadesign/goa)** — Compiles `design/*.go` into HTTP, gRPC, OpenAPI, CLI, and validation packages (`goa gen`)—plus in-memory `generator.Sources` for design evaluation—at **0 LLM tokens**.
- **[Goa-AI](https://github.com/goadesign/goa-ai)** — Adds `Agent`, `Toolset`, `MCP`, durable Temporal workflows, `eval` suites, gRPC model gateways, and runtime token controls (`Inject`, `BindField`, `BoundedResult`, `ServerData`, `Cache`, `Compress`).
- **[Pulse](https://github.com/goadesign/pulse)** — Provides Redis-backed event streams (`pulse/streaming` with fan-out `Readers` and competing consumer-group `Sinks`), consistent-hash background job queues and worker pools (`pulse/pool` with automatic dead-worker failover and rebalancing), and atomic Lua-replicated state maps (`pulse/rmap` `rmap.Join` for feature flags and distributed rate limits).
- **[Clue](https://github.com/goadesign/clue)** — Configures OpenTelemetry trace and metric exporters (`clue.ConfigureOpenTelemetry`), stream trace propagation (`clue/interceptors`), dependency readiness probes (`clue/health`), and internal runtime diagnostics (`clue/debug`), while Goa-AI emits `gen_ai.*` model spans.
- **[Model](https://github.com/goadesign/model)** — Declares C4 architecture models (`SoftwareSystem`, `Container`, `Views`) alongside `design/*.go` and renders Structurizr JSON (`gen/structurizr/model.json`).

The team of coding agents that builds it runs on two more:

- **[Chorus](https://github.com/goadesign/chorus)** — Lets coding agents and people talk in real time through channels and direct messages, with `0`-token background waiting (`chorus watch`). It also launches the project's agent panes in Herdr.
- **[Herdr](https://herdr.dev/)** — An optional terminal workspace for side-by-side agents. Highbay adopts the current guide's compatible space and pane and places workers beside it. Without Herdr, workers run in background processes.

---

## Who owns what in Highbay

One-time `hbay up` registers the `hbay stdio` MCP connection and `/highbay`
(`$highbay`) skill with installed coding agents. The guide's first Highbay tool
call in a project folder starts or attaches to that project's server (from
here on, the server). Bare `hbay` remains a browser-opening operator
entrypoint. The server serves the views page in the user's browser and one
HTTP and MCP endpoint to the project's team of coding agents:

- **The guide** is the agent the user talks to. Working in its `.highbay-draft` copy inside the project folder (never in the project folder itself), it writes the Story and the rest of the Go source in `design/` that describes the business's flows, evaluates that draft with `refresh_story`, and proposes changes. A flow is one path of who does what, from the event that starts it to its outcomes.
- **The builder** writes the application code and asks Highbay to verify a commit.
- **The test writer** writes the tests a build requires, in its own working copy of the project.

Each change is saved as a Change Card: the record of the request, its open questions and the user's answers, the examples that become checks, the Story and the design source. Its `request_revision` identifies one exact saved revision. Only the user confirms a change, starts a build, approves a result and authorizes the merge operation. One table, `operationRoles` in [`operation_policy.go`](../internal/controlplane/operation_policy.go), says which caller may call each operation; HTTP, MCP and the tool list each caller sees all follow it.

| Owner | Source | What it owns |
| --- | --- | --- |
| The `hbay` command | `cmd/hbay` | Global MCP/skill and companion setup, lazy stdio role binding, finding the project's private state folder, server startup and shutdown, authenticated CLI status, stored settings |
| The server | `internal/project` | The project's identity and port, credentials and browser sessions, the team record, the Chorus roster, launch preferences, worker processes and observed role state |
| The control plane | `internal/controlplane` | Change Cards, builds and assignments, the report checker, the verification plan, the live target and operation diagnostics |
| The build working copies | `internal/testwriter` | The `.highbay-build` and `.highbay-tests` folders, the numbered `highbay/build-<n>` and `highbay/tests-<n>` branches, the required tests, the check of a report and the commit of accepted tests |
| Verification runs | `internal/verifyrun`, `internal/verify` | One folder per verification run (its run directory) and the processes that build, run and report it |
| Herdr | outside Highbay | Terminals, panes and spaces |
| Chorus | outside Highbay | Agent messaging and launch of role panes in Herdr |
| The companion | `skills/herdr/highbay` | A floating Herdr window reading project status and focusing panes or opening the browser; no browser-session credential |

### The server and its state

The executable binds the project's owner and team service and its physical
workspace directory before constructing the control plane or opening the
listener. Construction rejects either missing binding before opening the card
store. Team operations and build operations therefore always have their
project owner; the server can run build workers in the background when Herdr is unavailable. An empty project folder and a team with no registered or running
agents are valid states.

A project is one physical folder. `hbay` looks for the nearest folder that has a project record and otherwise creates a new, empty project, so moving or renaming the folder starts a new project. Each project has one private state folder under the user's configuration folder, outside the project. It holds the project record (its ID, folder, owning operating-system account, port, team name and Herdr space, and each registered agent's role and working folder), one credential file per agent, the Change Card folder, verification runs and reports, one retained Go build cache, and the stored verification and promotion settings. A new project saves a free loopback port that no other project of the account saved, and keeps it, so its agents can reuse their connection settings. When `hbay` cannot use a project's record, it names the project folder, the record's path and the repair; when it cannot use a state folder, it names that folder and the repair.

When a guide first calls through `hbay stdio`, or an operator runs `hbay`:

1. A start lock lets one caller start the server at a time. The folder lock, an operating-system file lock on the Change Card folder that a running server holds for its whole life, is the only proof that a server is running.
2. The CLI passes the state folder explicitly to `hbay serve -state <dir>`. It starts a parentless Herdr terminal when `hc` is available or a detached process with a private startup log otherwise. Foreground `hbay serve` uses the same explicit state folder.
3. After binding its port, the server publishes a fresh management credential in a private record. `hbay` reads that credential for each request, confirms that the answering server names the same project, folder and owner, and that the same build of `hbay` started it (the server reports the SHA-256 of its executable, read when it started), and only then continues. `hbay status` reads compatible servers and `hbay stop` accepts a server of any build. Before the listener closes, the server revokes the credential and withdraws the record, even when accepting connections failed.
4. If the server exits before answering, `hbay` reads the last lines from the terminal or startup log and includes them in the error. It removes an exited server's startup terminal.

The management credential lets the CLI read project status, attach a guide,
stop the server and ask for a browser link. User decisions require a separate
browser session. The server issues a single-use link that expires after two
minutes. The page exchanges it for a session held in memory in the server
and the open page. Each agent authenticates with its role's credential file.

Bare `hbay` opens a fresh link after handing over the shell's model settings.
`hbay --print-link` prints the link and expiry instead. In `open_views`, the
control plane checks the guide or user role, then the project server mints
the link and passes it directly to the browser opener. The operation returns
only an opening result and a message.

`hbay status` reads the existing card-store lock and verifies the answering
server's identity using the management credential. The project server
delegates the status read to the control plane, which selects the change and
derives the stage from the saved build and verification evidence. It may
read Git to detect a manual merge. It reads role state from the team cache.

In-place `init_project` stages and checks the starter before publication.
The control plane records the expected identities of the directory and the
staged entries. It moves entries without replacing existing names, publishing
`.git` last. On restart the persisted publication record lets the control
plane verify already moved entries and finish an interrupted initialization.

`hbay verification set` and `hbay promotion set` validate a settings file and copy it into the state folder. The server reads both at start, so a change applies when Highbay is started again.

### The stdio connection and global setup

`cmd/hbay/setup.go` owns registration of the installed `hbay stdio`
executable and bundled skill in detected coding-agent configuration. It
preserves unrelated settings and leaves identical content untouched. Setup
also installs the floating Herdr companion after Herdr has created its app
folder. On macOS it adds the free `cmd-alt-shift-h` shortcut in `keys.json`,
without changing workspace layouts. `hbay up` runs setup and starts Chorus
when installed; the project server itself does not restart Chorus.

The stdio server answers initialization, ping and catalogs from generated
MCP definitions without creating state or starting a project server. The
first tool call or resource read binds a role and authenticates the relay to
the project's HTTP MCP endpoint. A guide binding can start the server, hand
over provider settings from its process environment and attach the current
session. Provider hand-over and live Herdr adoption depend on the client
forwarding the corresponding settings and terminal context to stdio; that
forwarding has not been live-verified with real clients.
Worker bindings require an existing server, matching role registration and
credential; assignment validity is checked by the assignment operations.

Role binding follows physical subtree containment: `.highbay-build` and its
children are builder-only, and `.highbay-tests` and its children are
test-writer-only, including nested modules and leftover worker folders. A
missing worker registration or credential never grants guide access.
Generated schemas and the control plane's operation policy still enforce each caller's tools.

The Herdr companion reads `hbay status --json` for the shown local project
and Herdr labels for pane focus. Its **Open Highbay Window** action runs the ordinary
browser-opening entrypoint. It holds no user credential and changes no
shipped workspace layout.

### The team: Herdr and Chorus

The project server owns the role registry, launch choices and notice
preferences. The control plane owns which workers have work and reports the
current build's identity, assignments and check results to the team
supervisor. A team read returns cached observations and project preferences
without starting discovery or terminal commands. Installed agent discovery
runs on demand for the user and when resolving choices for a launch. The
browser's inline Build and Team controls use those options and submit the
needed roles' choices. The project saves choices and dismissed recommendations;
those preferences do not change role permissions or required assignments.

`hbay stdio` registers the current coding session as guide through the local
management `attach_guide` operation.
The server checks the supplied terminal is a running model pane in a local
tab with compatible project and team labels before adopting it. It keeps the
live guide session in place and opens no launcher. A user can
request an explicit guide launch in Herdr through `start_team_agents` if
there is no running guide pane or watcher. Build starts request workers only.

To launch workers in Herdr, the server resolves the managed Chorus endpoint
and identifies its owned live panes. It writes the roster and passes missing
roles' choices to `chorus team launch`. The roster uses a running guide as the
placement reference when there is one. Otherwise it starts with the workers.
The first worker is placed to the right of a running guide and the test
writer below the builder. A change to the managed Chorus endpoint replaces the
observer and sender, clears stale watcher facts and carries forward the
supervisor's assignment-attempt tracking.

Without Herdr the supervisor starts finite worker invocations in dedicated
process groups with the preserved launch environment. Workers use the globally
registered `hbay stdio` connection and their role credentials. The browser
requires a supported non-interactive client and explicit permission to run
without interactive prompts. The supervisor
advances workers when a new assignment or verification result calls for
work and waits while Highbay checks a test report or runs
verification. During a server lifetime, each build allows three invocations
per role and each invocation has a 30-minute deadline. The project server
drains those groups before the control plane releases the card-store lock.

For a Herdr team, the server watches each registered role's terminal through Herdr and whether Chorus reports a watcher for each agent: the wait through which the agent receives its messages. It keeps the terminal's state and reported status, the foreground program's arguments and the watcher facts, never screen text, titles or messages. Herdr sends a heartbeat every 15 seconds; when three are missed, every role is unknown until fresh snapshots arrive. Chorus answers are read only up to their agent list, at most 256 KiB, before any conversation data. The control plane gives the server a read-only callback that reports which worker roles have work on the open build and which unread assignment generations they owe; calling it never marks work read. Highbay types nothing into a pane. It reminds a worker role that has work on the open build and owes an unread assignment with two tool calls posted to Chorus's `/mcp` endpoint, written as Chorus's dashboard writes them: `who` registers Highbay's own handle in the team, then `send` hands the role one fixed direct message. The sender has its own client, bounded at 5 seconds per post; it fails a response longer than 2 MiB and decodes only whether each call failed. The supervisor alone keeps, in memory, which generations of an unread assignment it has already tried. [Team supervision](REFERENCE.md#team-supervision) gives the user's view of this.

### Change Cards and builds

The control plane keeps Change Cards in the Change Card folder, under the folder lock the running server holds. Only the guide proposes or revises a change. Once the user confirms a change, its agreed revision stays in force while a revision the guide proposes waits beside it, until the user adopts or declines that proposed revision.

A build is the user's decision to build the agreed revision. The control plane saves one open build per project in `build.json` beside the cards, by writing a staging file (`pending-open-build`), renaming it and syncing the folder, under the card lock, and records dropped builds in `made-from.json`. The card lock (`ControlPlane.mu`) is an in-process lock that guards every read and write of a card or a build inside the server; it is not the folder lock. The build file's reader refuses unknown fields, trailing data, an unknown role selection, a negative report number and a test writer state that contradicts its other fields. A build holds:

| Field | Meaning |
| --- | --- |
| `change_id`, `request_revision`, `started_at` | The change, the agreed revision the build started from, and when the user started the build |
| `number`, `base` | The build's number `n` (`highbay/build-<n>` and `highbay/tests-<n>`) and the project folder's commit the build started from |
| `stories`, `design_source`, `go_mod`, `made_from` | The Story and design as agreed, the text of the `go.mod` the design was evaluated with, and the record of what the revision was made from (the project commit its draft started from and the versions of each design file), copied when the build started; later revisions do not change them |
| `answers`, `examples`, `preserved_behaviors` | Each question with the answer the user confirmed, the confirmation examples and the preserved behaviors, as agreed, copied at the same time; the test writer, which may not read the change, receives them in its assignment |
| `roles` | `builder_and_test_writer`, or `builder_only` when stored verification settings supply the tests |
| `builder`, `test_writer` | Each assignment; the test writer's carries its state, findings and accepted tests commit |
| `report_number` | The test writer's latest report, 0 before the first |

The server's agent record maps each role to its folder and credential file. The build file holds no credential, and neither store writes the other's data.

When the user presses **Build this change** on the views page, `start_build`:

1. checks that the caller is the project's owner, then takes the builder's copy turn and the test writer's working-copy turn without waiting; while another start, an assignment read or a report check holds a turn, the start is refused and nothing is saved;
2. checks the agreed revision, checks open-build replacement (`previous_change_open` and `unfinished_build_open` with `replaces`), checks `drafted_before_drop` and `design_changed_outside`, numbers a new build (or keeps the unmerged, undropped open build's number and base), and checks the revision again;
3. saves the open build in `build.json`, with the Story, design, answers, examples and preserved behaviors of the revision that second check read, creates any missing `.highbay-build` (`highbay/build-<n>`) and `.highbay-tests` (`highbay/tests-<n>`) working copies at the build's base commit without moving existing copies, and registers its roles with the server; when a copy cannot be made or the server stops first, the build stays saved, and building again makes the copy and registers the roles;
4. releases the turns and asks the server to start the build's missing workers; each existing copy joins its build branch (`highbay/build-<n>` or `highbay/tests-<n>`), and `.highbay-tests` receives the required tests, when its worker first reads the assignment.

The work under the turns has one five-minute limit; starting the agents has its own 45-second limit. Accepted work belongs to the server and survives the page closing. A build started with only the builder stays usable while stored verification settings supply its tests; when those settings are removed, the user builds again.

The control plane derives `ChangeBuild.drafted_before_drop` from the agreed
revision's saved design provenance and the current actual drop. One predicate
serves build admission before preparation and before save, `get_build`,
`drop_build` and the build rendering included in change-list versions. The
fact describes only this refusal, not overall start eligibility; no private
provenance generation leaves the owner and no new persisted value is added.
The page uses that fact for recovery guidance and includes it in the identity
of any pending Build question. A merged build of the current agreement has a
secondary replay question; confirmation starts a new number from current
project HEAD with fresh assignments, rather than reusing the merged build's
number or passed result.

The user-only `merge_change` operation checks that the live approval still
matches the open build, revision, candidate commit, report digest and
selected tests. The control plane holds the working-copy turns and rechecks
those bindings before writing to the project repository. It merges the
candidate and accepted written tests into the checked-out local branch with
clean tracked files. It protects untracked files. If a merge step fails, it
attempts to restore the branch, index and tracked files. The team owner rechecks the
build's change ID and start time before stopping its workers. The same cleanup
follows a drop or a manual merge observed by the supervisor. Build reads
report state without performing cleanup.

Before a Git worktree read or content merge, the merge owner checks attribute
and configuration sources. Attribute files, custom default merge drivers,
command hooks and configuration that candidate commits can replace require
manual merge. This prevents content handling during a server merge from
selecting those commands. Unused filter definitions, external configuration
includes, trusted filesystem hooks and signing keep their existing behavior.

The views page keeps one waiting read (`wait_for_changes`) open. Its answer is a version of the change list that changes whenever a card, a change's latest verification run, a build as `get_build` shows it, the team, the Story evaluation or the live target changes, and the page then reads again. The page sends a build or a window action only when the user acts.

### The test writer's working copy and the report checker

`internal/testwriter` owns the builder's and the test writer's working copies—linked Git working copies of the project at `.highbay-build` on `highbay/build-<n>` and `.highbay-tests` on `highbay/tests-<n>`—joining each to its build branch on the first assignment read, checking build-branch lineage, and preparing, checking and recording accepted tests as a commit on `highbay/tests-<n>`. The control plane runs one report checker per project: it starts when the server binds the team and drains before the stores close. Two working-copy turns—the builder's copy turn (`ControlPlane.builderCopyTurn`) and the test writer's working-copy turn (`ControlPlane.workingCopyTurn`)—guard the working copies, and in-process locks are always taken in this order: the builder's copy turn, the working-copy turn, the report lock, the draft lock (`ControlPlane.draftMu`, which serializes the guide's draft and its made-from list), the card lock. A build start takes the builder's copy turn and then the working-copy turn and releases both after registering the roles, without writing tests; the test writer's marking assignment read and each report check hold the working-copy turn and write the required tests, and either can take minutes. The report lock (`ControlPlane.reportMu`) makes a newly arriving report wait while the checker confirms the build is the one it checked, records the accepted tests and saves the verdict. Git and the commands that build and run the test writer's code never run under the card lock. [Story flows and the tests a build requires](STORY_TEST_DESIGN.md) describes [the copy](STORY_TEST_DESIGN.md#the-test-writers-working-copy) and [the check and its findings](STORY_TEST_DESIGN.md#how-highbay-judges-a-report).

### Verification runs and the live target

The test writer submits stated scenario coverage of the built answers and
examples with its report. The control plane validates those requirement IDs
before report mutation, checks scenario IDs using the program's existing
listing, and saves statements with the matching report and accepted commit.
Build exposes accepted statements only. Verification selects the report and
its tests commit in the same locked read and copies the agreed basis and
statements into its immutable request. Approve reads that snapshot; it never
substitutes today's card or newer report. Old-run absence and configured
evaluators remain explicitly not recorded. These are writer statements, not
Highbay's judgment of assertion meaning; see
[the report contract](REFERENCE.md#report_tests_finished).

One function, `testsChoice` in [`verification_plan.go`](../internal/controlplane/verification_plan.go), chooses which tests judge a change: the suite named in the stored verification settings, or the tests accepted for the change's build at its revision. Verification runs those tests, `approve_merge` requires the approved report to have run them, and a saved approval is shown only while they stay the same; `verificationPlan` builds a run's configuration from that choice. The builder names only the change, the revision and its commit.

The build read returns `required_tests` for every caller, derived from its
saved Story with `designeval.StoryScenarios`. Build, assignment and run-review
reads share one conversion to the generated `RequiredTest` type; the count is
the list length. The page presents these requirements beside the independently
correlated findings strings, without inferring a relation between them. The
list is derived on read, so existing saved build records need no new field.

At acceptance, the control plane copies the open build's agreed Story and build
identity into the immutable request owned by `internal/verifyrun`. This evidence
survives later revisions and builds; runs without recorded context never borrow
the current Story. The user-only `review_verification` read returns that snapshot
and an approval observation without changing workflow state. Its required-test
mapping comes from `designeval.StoryScenarios`, the same owner used by generation;
operator-evaluator mapping remains explicitly unknown. The read and
`approve_merge` share `readApprovalInputs`, `checkApprovalReport` and
`recheckApproval`; the write holds the final lock through persistence. A readable
run alone does not imply approval, and an observation grants no lease. Shared
agent reads and history listing keep their existing builder-safe contracts.

`internal/verifyrun` runs each verification in its run directory, one folder per run under the state folder's runs folder that holds the run's request, lock, result and logs. A supervisor process, started for the run, holds the run's lock for its whole life, so a run outlives the server that accepted it and its state is read back from the lock and the result file alone. `internal/verify` builds the candidate (the builder's commit under verification) and the test program, then runs the test program, which starts the candidate's programs; they all run as ordinary processes with the user's file and network access, in the run's one process group, which a process that starts its own session leaves.

The control plane runs at most one live target for an owned, confirmed change. `start_live_target` names the change and the candidate commit. It uses `verificationPlan` too: without stored settings, the current build's accepted tests start the candidate's programs with `--live`; a stored evaluator needs no build. The card and plan are checked before waiting for another start, then checked again with the model snapshot under the start lock before the running target is stopped. A refusal preserves the current target and its prompt drafts.

`internal/verifyrun` owns the live executor's process group and temporary directory under the Change Card folder. For written tests with a direct provider, `LaunchLive` passes the held settings over standard input rather than in the executor's starting environment. After preparation, the executor constructs the provider and serves a loopback model gateway; the accepted test program gets the gateway address and target key, without provider settings. The SDK constructor belongs to the command composition root, while `internal/verifyrun` owns gateway and process lifetime. A chosen gateway passes through, and evaluators in stored verification settings keep their existing provider-settings path. Gateway answers preserve structured model error types with server-authored messages instead of provider error text; the pinned Anthropic SDK's API-key precedence remains unchanged. See [model provider settings](REFERENCE.md#model-provider-settings) for that setting's limits.

Highbay sends prompt drafts to a running target with a key minted for it. An unexpected executor ending unbinds drafts and stops the entire group. Only after confirmed cleanup does the control plane retain one in-memory `ended` value containing the commit, ready time, observed-end time and fixed recovery message, without the process, key, provider settings or draft map. Failed cleanup keeps the target owned and `not_confirmed_stopped`. Explicit successful Stop or an accepted replacement clears the observation, and restarting the server loses it. Closing the control plane stops the target and gateway; an open model stream never delays that cleanup.

The project composition root supplies one required `BuildCacheDir` in
`controlplane.Options` whenever either test source is configured. It lives
beside runs and reports, outside the workspace, and travels as an explicit
argument through the internal supervisor, executor and live roles. Saved
requests, live records and verification plans contain no cache path.
`verify.Prepare` passes it as `candidate.BuildSpec.BuildCacheDir` for candidate
and test-program compilation; `WorkDir` keeps only operation-owned scratch and
staging. The standalone `hbay verify` composition root instead supplies a cache
under its own work directory. The control-plane composition also supplies the
project cache to its design evaluator, each fresh initialization generator and
the test-report checker. Their scratch and outputs remain independently owned;
closing an evaluator or removing check scratch never removes the project cache.
Direct evaluator and checker constructors retain their private cache defaults.

The project cache survives Stop, Close, restart and run pruning. Go maintains
entries; canceled compiler scratch stays with the operation's cleanup owner.
Concurrent processes may share the cache on one machine using a local
filesystem. The user may delete it while no operation is running; the next
build recreates it. No cache entry skips checkout, output publication, digest
measurement or test execution.
A cache hit is never a passed check: refreshes still evaluate the design,
initialization still generates and checks the starter, and report checking still
builds and lists the test program against its required tests.

Cache ownership provides no confinement. Go checks existence and recorded size
when reading cached package files, not compiled contents against selected
source. Same-user code can plant a compiled package that a later build links
into the candidate or test program, so its report can bind a binary the
selected source would not produce. This joins the trusted module cache, toolchain, executable search path and
Highbay's files. Deleting the build cache forces recompilation but does not
authenticate those other inputs.

For Docker `check_project` and `update_dependencies`, the project composition
derives a separate private `check-modcache/` under project state and injects it
as `controlplane.Options.CheckModuleCacheDir`. The control plane validates its
ownership, permissions and separation from source and operation storage, then
sends it only in the check helper's transient stdin specification. No model
input or durable operation record contains the path. The helper validates that
process input before Docker admission. Dependency preparation and tidy mount
the cache writable; test containers mount it read-only, with network disabled
and `GOPROXY=off`. Compiler cache, HOME, TMPDIR and work remain fresh for each
command. The retained directory survives cancellation, cleanup and restart;
recovery removes only operation-owned scratch. Embedded checkers that omit the
option keep an operation-local module cache and need no `BuildCacheDir`.
Go's recorded download checks do not rehash extracted cached source. The
read-only test mount preserves the dependency write boundary; it does not
confine other same-user host processes.

### Operation diagnostics

The control plane records each of its operations as an OpenTelemetry span and saves a bounded, safe view of each one in one history file, `traces.jsonl` in the Change Card folder. The history belongs to the project's owner: every caller (the user's page, the management credential and each agent) acts for that owner, and each record names its caller and the caller's kind, such as `user`, `management` or the agent's role. The server's composition root, `projectRuntime.serve` in [`project_server.go`](../cmd/hbay/project_server.go), owns the tracer provider, and exports full spans over OTLP when an OTLP endpoint is set in the server's environment. The exported spans carry no added identity. The user's page reads that succeed or are refused, and its waiting read, are not saved, so the page's repeated reads do not fill the history. Spans that end after the control plane closes are not saved. [Operation diagnostics](REFERENCE.md#operation-diagnostics) gives the caller's view.
