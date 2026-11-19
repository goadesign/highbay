<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/img/surla-banner-dark.png">
    <img alt="Surla — one compiled contract every coding agent builds to and an independent verifier checks against." src="docs/img/surla-banner.png" width="960">
  </picture>
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/goadesign/surla"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/goadesign/surla.svg"></a>
  <a href="https://modelcontextprotocol.io"><img alt="MCP" src="https://img.shields.io/badge/MCP-2026--07--28-6E56CF"></a>
  <a href="https://github.com/goadesign/surla/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Surla

**An agentic software factory that builds, verifies, and evolves production services at a fraction of the token cost.**

A coding agent left alone writes the code and also the tests that judge it, so
its mistakes pass. Hand-written routing, auth middleware and workflow plumbing
drift with every feature, and each later edit costs more tokens.

Surla organizes the coding agents you already use into a team with separate
jobs, and pairs them with a deterministic compiler. Agents write a compact
service design and the business logic. [Goa](https://github.com/goadesign/goa)
and [Goa-AI](https://github.com/goadesign/goa-ai) generate the transports,
schemas, validation, durable workflows and tool guardrails from that design at
**0 LLM tokens**. One agent writes the code; a different agent writes the
tests; Surla itself judges those tests and runs them against the code, which
it builds and runs as ordinary processes on your machine, with your file and
network access. You agree what is built
and approve the result in your browser. You choose when to merge the
approved commits into your local branch. The project server reuses compiled
Go packages for Story refreshes, starter initialization, test-report checks,
live starts and verification runs.
Each operation still builds its outputs and runs its checks; a cache hit is never a
passed check. The cache is a trusted compiler input that programs running as
you can change; see
[project storage and cache trust](docs/REFERENCE.md#project-state-folder).

## One change from start to finish

These words appear throughout:

| Term | Meaning |
| --- | --- |
| **The server** | The project's Surla server. `surla` starts one per project folder. It keeps what you agreed, who was asked to do what, and what was accepted, and it runs the checks. |
| **The page** | The server's page in your browser, which the reference calls the views page. You agree on changes and approve results there. |
| **Herdr** | An optional terminal workspace that shows the team's agents side by side. |
| **Chorus** | The messaging and pane-launch service for teams in Herdr. |
| **The guide** | The agent you talk to. It writes the Story and proposes changes. |
| **The builder** | The agent that writes the application code. |
| **The test writer** | The agent that writes the change's tests, in its own working copy of the project. |
| **Story** | A plain description of what the system does, written as Go in the project's design, one flow at a time. |
| **Flow** | One path through a Story: who starts it, the steps by role, and the outcome. For example, "a customer asks the price of tea and gets it". A flow with a start, an outcome and no open question is *finished*; any other flow is a draft. |
| **Change Card** | One change the guide proposes: the request, the Story, open questions, concrete examples and the behavior to preserve. |
| **`request_revision`** | The digest of one saved revision of a Change Card. Previews, verification runs and approvals name the revision they belong to. |

Run `surla --print-link` to start or attach and print a one-use browser link
as JSON. Run `surla status --json` to read the existing server's stage and
team state. See [terminal operation](docs/REFERENCE.md#terminal-and-browser-automation).

Here is one change, for example a refund assistant whose rule is "refunds up
to $50 get instant store credit; larger ones wait for a manager":

1. **Start.** Run `surla` in your project folder to start or attach to its
   server and open a fresh views page. The server runs in a Herdr terminal
   when `hc` is available or as a detached background process otherwise.
2. **Connect a guide.** Use a coding agent with an authenticated Surla
   MCP connection for the guide role. The local management API can attach
   an existing guide and adopt its validated Herdr pane. In Herdr, the
   user API can also start a missing guide with an explicit choice. See
   [team setup](docs/REFERENCE.md#team-supervision).
3. **Describe.** Tell the guide what you want. It writes the Story and
   proposes a Change Card, which appears on the page by itself. The guide
   can call `open_views` to open a fresh page for you. For the refund rule,
   the card might ask “Does $50 mean the item subtotal, or the total with
   tax and shipping?” and “Does exactly $50.00 get instant credit?”
4. **Agree.** On the page, answer the questions, check the examples and
   confirm. No agent can confirm for you. If the guide later revises the
   change, its proposed revision waits beside the agreed revision until you
   adopt or decline it; the agreed revision stays in force until then.
5. **Review the architecture.** Open **Architecture** on the page to inspect
   the agreed revision's saved design. If it needs a change, ask the guide to
   revise the change, then review and adopt its proposal before building.
6. **Build.** Press **Build this change** on the page. Surla saves the agreed
   revision and requests the required workers using saved choices or installed
   defaults. When selecting defaults, it prefers different coding-agent families for the
   two worker roles and can use one installed family for both. Workers run
   in Herdr panes where available or in background processes. The builder and
   test writer work in separate Git working copies, `.surla-build` and
   `.surla-tests`. Surla gives the test writer one required test per finished
   Story flow, plus the confirmed answers, examples and preserved behaviors.
7. **Tests.** The test writer writes those tests and reports. Surla does not
   take its word for it: it writes the required tests into the copy again,
   checks that nothing changed outside the test module (the project's `eval/`
   folder), and builds and lists the test program. Problems go
   back to the test writer as findings. Build shows them beside the saved
   required tests; Next names the test writer’s next step.
   When the tests pass the check, Surla accepts them at one commit on
   `surla/tests-<n>`. Build shows which agreed answers and examples the test
   writer says each scenario covers. Surla validates those references but does
   not judge the statements; anything without one says **Not stated**.
8. **Code and verify.** Meanwhile the builder writes the code, builds and
   tests it, and commits. If the application calls a model, the builder can
   try prompt wording after Surla accepts the tests, using a *live target*:
   a running copy of the application
   that the server starts from a commit. The builder then asks Surla to
   verify its commit. Surla builds that commit and runs the
   tests accepted for that exact revision against it. It refuses to verify
   until those tests are accepted. The result appears in the page's **Check**
   view.
9. **Approve and merge.** Open the run in **Approve** to review its recorded
   Story flows, test outcomes, excluded drafts and unknown coverage. When the
   server says approval is available, Next offers **Approve this result**.
   Surla checks that the run passed and ran the tests accepted
   for the agreed revision, then records the approval with the builder's
   commit, the tests commit and the report. Follow the page's Git merge
   instructions in the project folder, or call
   [`merge_change`](docs/REFERENCE.md#merge_change) with that approval through
   the user API. The server operation rechecks the approval and merges into
   the clean checked-out local branch. Once your branch holds the verified
   work, the build shows **Merged** and Surla stops that build's workers.
   Verification covered the commits in the passed run. To test the resulting
   branch commit, see [`surla verify`](docs/REFERENCE.md#surla-verify).
   Approval needs no settings. A project that stores
   [promotion settings](docs/REFERENCE.md#surla-verification-and-surla-promotion)
   (a release folder and the names of its binaries) gets one more check at
   approval: the files in the release folder must be the binaries the run
   built.

Only you confirm a change, start a build and approve a result, and you do it on
the page. Each agent has its own connection to the server with only its own
role's permissions. While the team works, the page's **Team** view shows each
agent's state. If a Herdr agent needs you, the page shows an action such as
**Show guide pane**. The server can send a Chorus message when a worker owes
an assignment read. After 90 seconds of idling with that assignment unread,
the page says what to tell it.

You can go back at any point: adopt a revised change, build again, or the test
writer reports again after findings. Adopting changed content gives it a new
`request_revision`, which starts without an approval, and verification then
needs a build of that revision: before you adopt, the page says that the
existing build stays on the earlier revision. Building again replaces the
build and requires the tests to be reported, judged and accepted again before
verification or a new live target. After merging the current agreement, build
another change or refine this one when there is more to do. **Build again…**
remains available as a secondary action: it asks before starting a new build
number from your branch as it is now, for the same agreement, with fresh
assignments. Earlier reports do not establish that the new build passed.

Two limits hold. All agents run as you on one machine, so they are separated
by job, not walled off from each other. Surla runs the code the agents write
(the design at each refresh, the builder's programs and the test writer's
test program) as ordinary processes with your file and network access. The
page states the second limit next to **Build this change**. Each verification
run records where its tests came from. For the test writer's tests, the
**Check** view shows with the run, and the agents read in its result, the
server's notice that the tests were written by a separate agent on this
machine and are not isolated from the builder.

## Install

Surla needs:

- **macOS or Linux.** Surla runs the code the agents write (the design at
  each refresh, candidate builds, the candidate service, test listing, and the
  test program) as ordinary processes with your file and network access.
- **Go 1.27.3** or newer.
- **Git 2.45 or newer**, with your name and address configured (`git config --global
  user.name` and `user.email`). Surla records accepted tests as commits under
  your identity, and a build is refused until Git has one.
- **Docker Desktop**, or another local Docker engine. The builder builds and
  tests its work in Linux containers. Each project retains downloaded
  dependencies across checks; tests mount them read-only and run offline with
  fresh compiler scratch. See [`check_project`](docs/REFERENCE.md#check_project).
- A coding agent that can connect to Surla over MCP.
- For side-by-side agent panes, **Herdr core** (`herdr-core` and `hc`), the
  **Herdr app** (`herdr-app`), and **Chorus v0.14.0** or newer. Without
  Herdr, Surla can run the build workers in the background without Chorus.

To install the Surla described by this README, clone this repository and check
out the version you are reading. From the repository root, run:

```bash
go install ./cmd/surla
```

`go install github.com/goadesign/surla/cmd/surla@latest` installs the latest
module version available to Go, which may be older than this README and lack
commands described here. When installing that version, follow its README.

To use the Herdr workspace, install Herdr and Chorus.

```bash
curl -fsSL https://releases.herdr.dev/core/install.sh | sh
curl -fsSL https://releases.herdr.dev/app/install.sh | sh
curl -fsSL https://raw.githubusercontent.com/goadesign/chorus/main/scripts/install.sh | sh
```

Before startup, `surla` checks for Git's `--no-lazy-fetch` capability and
the browser opener. See [Prerequisites](docs/REFERENCE.md#prerequisites).

Surla's design and generation steps need no model key; each coding agent
uses its own sign-in. An application that calls a model needs a key when
the builder tries prompts on a live target. Export the provider's key, such as
`ANTHROPIC_API_KEY` or `OPENAI_API_KEY`, with any request settings it needs,
in the shell where you run `surla`: `surla` hands them to the server, which
holds them in memory until `surla stop`, and while no model is chosen, live
targets then use `claude-sonnet-4-6` or `gpt-5.4-mini`. The page's
**Live target** view changes that model to another provider whose settings
were handed over, or to a model gateway; see
[`surla` and `surla stop`](docs/REFERENCE.md#surla-serve).

## Start a project

Start from a starter service, or from an empty folder.

**From a starter.** `surla init` creates a service designed with Goa, Goa-AI
and [Model](https://github.com/goadesign/model) (architecture diagrams as Go
code). The service has a first Story, in `design/story.go`, with one finished
flow, and a test module in `eval/` that already holds the test that flow
requires. `surla init` then prints the commands that make the first commit
and start Surla:

```bash
surla init -dir shop -module example.com/shop
cd shop
git init
git add .gitignore README.md AGENTS_QUICKSTART.md go.mod go.sum design gen internal cmd eval
git commit -m "Start shop from surla init"
surla
```

**From an empty folder.** Run `surla` and ask a connected guide to initialize
a new service in the folder. The guide can call `init_project` with
`dir: "."` to create the starter and its first Git commit in place.
[Initialization](docs/REFERENCE.md#init_project) lists the agent configuration
the operation preserves and the folders it can initialize.

The page keeps its session while open. After a reload or close, run `surla`
again or ask the guide to call [`open_views`](docs/REFERENCE.md#open_views).
You can open a fresh link in the same tab. If there are unsaved edits, the
page keeps them and offers **Open link** after you save or discard them.
The link expires two minutes after issue.

Running `surla` from a subfolder finds the same project. Check its current
state with `surla status --json`. After installing another build of Surla,
run `surla stop`, then `surla`. See
[the startup reference](docs/REFERENCE.md#surla-serve) for project settings
and recovery.

## Use the tools without a team

These commands need no server, no team and no model key. `surla init`,
`surla preview` and `surla gen` run Goa with the module's own versions, so no
separately installed `goa` is needed. They compile and run the design as an
ordinary process with your file and network access.

```bash
# Scaffold a starter service and its test module:
surla init -dir shop -module example.com/shop

# Compile shop/design at 0 LLM tokens and print its
# containers, boundary edges, agent policies and tool contracts:
surla preview -dir shop

# List all commands; "surla help <command>" prints a command's flags:
surla help
```

Or clone this repository and look at the [`examples/orders`](examples/orders/README.md)
reference platform:

```bash
git clone https://github.com/goadesign/surla.git
cd surla

# Preview the Orders platform's architecture and tool guardrails:
surla preview -dir examples/orders

# Compare two Goa design modules structurally:
surla diff --old-dir examples/orders --new-dir examples/orders
```

From a checkout you can also run `go run ./cmd/surla`; `go run ./cmd/surla init`
then needs `-surla-version` with a published version, such as
`-surla-version v0.0.0-20261118203415-bec57123a61f`.

After you edit a module's `design/`, `surla gen -dir <module>` regenerates its
`gen/` and replaces it in one step. It needs `goa.design/goa/v3`
`v3.35.1-0.20261103205041-35e789e8f9b1` or later and refuses a design with gRPC
transports; see [`surla gen`](docs/REFERENCE.md#surla-gen). The operator tools
`surla verify` and `surla approve` build a commit, run a test suite from a
Git repository you name against it and check a saved report, outside any
server; see
[`surla verify`](docs/REFERENCE.md#surla-verify).

## Why engineers use Surla

### 1. Agents write the contract; the compiler writes the plumbing

Why pay a model to write thousands of lines of HTTP handlers, gRPC stubs,
OpenAPI specs, MCP schemas and JSON validators? In Surla, agents express your
API, agent tools and security rules in a compact, strongly typed design
(`design/*.go`). Goa generates the transports, OpenAPI specs, MCP schemas and
validators from it with no model inference (**0 LLM tokens**), which removes
whole classes of glue-code bugs and the tokens spent writing them.

### 2. A built-in Golden Path that stays cheap to evolve

AI-built codebases decay when every prompt invents a new folder layout, a new
way to call models or a new auth pattern, and later agents spend tokens
reverse-engineering the repository. Surla serves every agent the same
**Golden Path** rules:

- **Predictable layout and generated agent maps.** Every service follows the
  same structure and ships a generated `AGENTS_QUICKSTART.md`, so an agent can
  extend the platform months later with little context.
- **Production patterns by default.** One HTTP/MCP edge service, internal gRPC
  services, a central model gateway with rate limiting and usage accounting,
  durable Temporal workflows, and tool fields such as `tenant_id` and
  `user_id` that the server injects and the model can never set.

### 3. Independent verification of each commit

The agent that writes the code never decides whether it works. Tests come
from the test writer and count only after Surla's own check accepts them.
Verification builds the builder's exact commit (`verify/candidate`) and runs
the accepted tests against it; the code under test and the tests run as
ordinary processes with your file and network access.

- **Hidden scenarios.** A project can instead store verification settings
  (`surla verification set <file>`, applied when Surla starts again) that
  name a test suite of your own at one commit of a Git repository you choose;
  its builds then add only the builder. The suite's scenarios tagged
  `holdout` are hidden: [`Report.ForBuilders()`](internal/verify/report.go)
  removes their names, inputs and logs from the report the builder sees. The
  builder's programs run with your file access, so while a run executes they
  could read the suite's files on this machine.
- **Real crash and budget proof.** The [`examples/orders-eval`](examples/orders-eval/)
  reference suite kills the Temporal worker in the middle of a refund
  (`kill -9`), restarts the API while an approval is paused, and fails any
  scenario whose model token use exceeds its budget
  (`TestSeededPayloadBloatFailsTheBudget` proves it with a seeded payload
  bloat).

### 4. Prompt changes without rebuilding

Prompt wording and compiled business rules are kept apart. The builder names
the confirmed change and a commit to start a live target. Without stored
verification settings, the current build's accepted tests start that copy
using the chosen model; they receive no provider settings. The builder sends
new prompt wording to it with
`draft_override`, without rebuilding Go code. A draft aimed at a compiled
business rule changes nothing and says that the rule needs a rebuild and
verification. A draft lasts as long as the live target: to
keep it, the builder commits the text and verifies that commit. The page's
**Live target** view shows which prompts changed and the model live targets
use. An unexpected ending leaves the commit, launch and observed-end times,
and the builder's recovery step after cleanup, including after a page reload.
See [`start_live_target`](docs/REFERENCE.md#start_live_target),
[`draft_override`](docs/REFERENCE.md#draft_override) and the
[live target prompt contract](docs/REFERENCE.md#live-target-prompt-contract).

### 5. Your decisions on one page

The page lists the project's changes and shows each one's Story, questions,
build, team, verification runs and approval, and updates itself without
losing what you typed. The frame names Describe, Agree, Build, Check and
Approve. The approval decision appears in Next. The **Diagnostics** drawer
shows safe summaries of what the agents and the server did. See the
[views page](docs/REFERENCE.md#views-page) and [operation diagnostics](docs/REFERENCE.md#operation-diagnostics).

> **Why the name "Surla"?** Named after **Tambdi Surla** in Goa, home to the
> carved basalt Mahadeva temple beside the Surla River. In Surla
> (*SOOR-lah*), `design/*.go` is that enduring stone contract: one compiled
> specification every coding agent builds to, and an independent verifier
> checks against.

## Dive deeper

- **[How Surla works](docs/HOW_IT_WORKS.md)**: the journey step by step, by
  who acts.
- **[Reference](docs/REFERENCE.md)**: commands, MCP tools, builds, the page and
  the server's contracts.
- **[Story language](story/README.md)**: the declarations a Story is written
  with.
- **[Golden Path architecture](docs/ARCHITECTURE_PLAYBOOK.md)**: the rules that
  keep Surla platforms maintainable, and who owns what in Surla.
- **[Story flows and the tests a build requires](docs/STORY_TEST_DESIGN.md)**:
  for contributors, how a Story becomes tests and how Surla judges them.
- **[Orders reference platform](examples/orders/README.md)**: a complete Golden
  Path platform with its test suite.
- **[Product direction](docs/PRODUCT_DIRECTION.md)**: what Surla is meant to be
  and for whom.

## License

Surla is released under the [MIT License](LICENSE).
