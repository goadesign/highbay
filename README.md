<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/img/highbay-banner-dark.png">
    <img alt="Highbay" src="docs/img/highbay-banner.png" width="960">
  </picture>
</p>

<p align="center">
  <a href="https://github.com/goadesign/highbay/releases/latest"><img alt="Release" src="https://img.shields.io/github/v/release/goadesign/highbay"></a>
  <a href="https://www.npmjs.com/package/@goadesign/highbay"><img alt="npm" src="https://img.shields.io/npm/v/%40goadesign%2Fhighbay"></a>
  <a href="https://glama.ai/mcp/servers/goadesign/highbay"><img alt="Glama MCP Server" src="https://glama.ai/mcp/servers/goadesign/highbay/badges/score.svg"></a>
  <a href="https://serverdex.io/server/io-github-goadesign-highbay/score"><img alt="Highbay on Serverdex" src="https://serverdex.io/server/io-github-goadesign-highbay/badge.svg" height="20"></a>
  <a href="https://pkg.go.dev/github.com/goadesign/highbay"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/goadesign/highbay.svg"></a>
  <a href="https://modelcontextprotocol.io"><img alt="MCP" src="https://img.shields.io/badge/MCP-2026--07--28-6E56CF"></a>
  <a href="https://github.com/goadesign/highbay/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Highbay

**Consistent architecture across the platform your coding agent builds. Services, MCP servers, and AI agents — ~70% of the code generated, not prompted.**

Left alone, coding agents wire every repo and feature differently — turning a
growing platform into mismatched services. Your agent drafts a short contract;
you confirm it. Highbay compiles it into servers, validation, and tool schemas —
no model in the loop. Your agent writes only the handlers, in a plain Go service
your Python, TypeScript, or web apps can call.

Highbay is an open-source (MIT) local workspace and MCP server that runs the
coding agents you already use — Claude Code, Codex, Gemini CLI, Cursor,
Antigravity, Windsurf, Copilot, and VS Code:

- **One consistent layout & Golden Path playbook.** Every generated Highbay
  project starts with the same two-module structure (`design/`, `gen/`, `cmd/`,
  `internal/`, and `eval/`, or an external suite registered via
  `highbay verification set` such as `examples/orders-eval`) and built-in
  `highbay://golden-path` rules for auth, OpenTelemetry tracing & readiness checks
  ([Clue](https://github.com/goadesign/clue)), Redis Streams, job queues, &
  worker pools ([Pulse](https://github.com/goadesign/pulse)), and durable
  workflows — so engineers and coding agents move across services without
  re-learning conventions or maintaining `.cursorrules` files.
- **~70% generated at zero LLM tokens.** From one confirmed contract, Highbay
  compiles HTTP, JSON-RPC, and MCP servers, OpenAPI 3 specs (callable from
  Python, TypeScript, Go, or web apps), request validation, `Inject`
  server-supplied run labels, `BoundedResult` cursor contracts, and
  `Confirmation` pause gates (~70% of non-test Go code in the
  [Orders reference platform](examples/orders/README.md)) — leaving clean typed
  interfaces in `internal/` for your coding agent to implement.
- **Confirm open choices upfront, verify in a separate copy before merge.** Your
  coding agent drafts the contract and opens a plain-language Change Card and
  compiled C4 architecture diagram in your browser. Once confirmed, a second
  coding agent in `.highbay-tests` writes scenario checks in `eval/` from that
  agreement, blocking approval until checks pass.

```bash
brew install goadesign/tap/highbay
highbay up
```

Run `highbay up` once, then run `highbay` in a new service folder (or existing
Goa repo) to open the browser workspace — or invoke `/highbay` (`$highbay` in
Codex) inside an already open coding agent. [Install](#install) covers Homebrew,
npm (`npx @goadesign/highbay up`), the shell installer, and `go install`.

## One change, start to finish

After [installing Highbay](#install), run `highbay up` once. In an empty project
folder, run `highbay` to open your browser, choose a coding agent, and send it a
goal in **Describe**:

> Build a multi-tenant incident platform: a triage agent and remediation agent
> with MCP tools to search pod logs, correlate deploys, and roll back services
> with on-call approval.

If you already have a coding agent open in the project, give it the same goal
with `/highbay` (`$highbay` in Codex). Highbay attaches that session as the
project's guide and opens the browser workspace.

The prompt leaves an architectural boundary open: which actions may
`RemediationAgent` execute autonomously versus pause for human approval?

| Cluster action | Autonomous staging + gated production | Approval required everywhere |
| --- | --- | --- |
| Pod logs & deploy correlation | Autonomous (`BoundedResult` pages) | Autonomous (`BoundedResult` pages) |
| Staging rollback (`rollback_staging`) | Autonomous self-healing | Pauses at `Confirmation` |
| Production rollback (`rollback_production`) | Pauses at `Confirmation` for on-call | Pauses at `Confirmation` for on-call |

A freehand coding agent silently guesses the staging-versus-production rule (and
whether a `viewer` JWT may call `/runs/{id}/approval`), returns unbounded log
slices that pass on 20 lines and blow prompt caps on 50 MB crash logs, and locks
its own assumptions into green unit tests. Highbay surfaces open choices to you,
compiles architectural and runtime guardrails into Go, and checks the agreed
behavior in a separate working copy:

1. **Describe.** In an empty folder, the guide creates a blank Go starter and
   its first commit (~2 minutes). It then writes a *Story* (the system's actors
   and flows in Go) and proposes a *Change Card*: your request, open questions,
   concrete examples, and behaviors to preserve.
2. **Agree.** Answer open questions and confirm the Change Card in your browser.
   If you pick an option that differs from the draft, one click asks your coding
   agent to revise the card before you lock the agreement.
3. **Review the architecture.** Inspect the live C4 topology — services, agents,
   toolsets, tool-call budgets, server-injected fields (`Inject`), and
   `Confirmation` gates — compiled directly from `design/` before handler code
   exists.
4. **Build.** Pick a coding agent and model for each worker role inline and
   press **Build this change**. The *builder* implements handlers in
   `.highbay-build`. When no external verification suite is stored, the *test
   writer* implements one scenario test per finished flow in `.highbay-tests`;
   Highbay regenerates the Story test table and `eval/gen/`, verifies that
   nothing outside `eval/` changed, and compiles the test binary before
   accepting those tests.
5. **Verify.** The builder asks Highbay to verify its commit. Highbay rejects
   any candidate commit whose `design/` differs from the agreed design or (when
   using `.highbay-tests` rather than a stored suite) that changes handwritten
   files in `eval/` (`eval/gen/` and `eval/AGENTS_QUICKSTART.md` excluded),
   builds the candidate, and runs the accepted test commit against it. When
   `Confirmation` pauses `rollback_production` and the builder's
   `/runs/{id}/approval` endpoint forgets to enforce the `oncall` role
   (`expected 403 Forbidden, got 200 OK`), verification fails and blocks
   approval until fixed.
6. **Approve and merge.** Review the scenario report, press **Approve this
   result**, and press **Merge into current branch**.

Only you can confirm a change, start a build, approve a result, or merge. Each
agent connects to the server with role-scoped permissions. You can go back at
any step: revise the change, build again, or submit updated tests.
[How Highbay works](docs/HOW_IT_WORKS.md) covers every step, and
[Going back](docs/HOW_IT_WORKS.md#going-back) explains what stays valid when you
do.

In a moderated 8-flow Go build on a sample application, real coding agents did
the work: **Codex** built the service and **Claude Code** wrote the scenario
tests (an AI participant played the developer directing them, and the service's
own model calls in tests were scripted). The new scenario had 32 named checks;
Codex's first commit failed two of them, so seven of eight scenarios passed.
Highbay blocked approval, Codex fixed the handler, and the locked suite passed
eight of eight about three minutes later.

## The agreement is code

What you confirm becomes three compact pieces of Go. These excerpts illustrate
the incident-platform walkthrough above; they are not files in this repository
(for a checked-in platform, see [`examples/orders`](examples/orders/README.md)).

```go
// design/agents.go (illustrative excerpt)
// 1. The toolset and agent contract drafted by your coding agent and confirmed by you.
var opsTools = Toolset("ops", func() {
	Tool("rollback_staging", "Roll back a staging service.", func() {
		BindTo("ops", "rollback_staging")
		Inject("org_id")
	})
	Tool("rollback_production", "Roll back a production service after on-call approval.", func() {
		BindTo("ops", "rollback_production")
		// Inject omits org_id from model-visible tool JSON and binds the trusted run label.
		Inject("org_id")
		// Confirmation pauses every call to rollback_production until approved.
		Confirmation(func() {
			Title("Approve production rollback")
			PromptTemplate(`Roll back {{ .service }} in production?`)
			DeniedResultTemplate(`{"status":"denied"}`)
		})
	})
})

var _ = Service("ops", func() {
	// ... service methods ...
	Export(opsTools)
	Agent("remediation", "Safe service rollbacks.", func() {
		Use(opsTools)
		RunPolicy(func() {
			DefaultCaps(MaxToolCalls(4), MaxRecoveryTurns(1))
			TimeBudget("45s")
		})
	})
})
```

```go
// eval/acceptance/incidents.go
// 2. The scenario test written in .highbay-tests by a separate coding agent.
func (h *Hooks) ProductionRollbackRequiresApproval(ctx context.Context) (eval.Result, error) {
	run, err := h.startRollback(ctx, "production", "api-gateway")
	if err != nil {
		return eval.Result{}, err
	}
	resp, err := h.approveAs(ctx, run.ID, "viewer") // caller lacks oncall role
	if err != nil {
		return eval.Result{}, err
	}
	return eval.Result{Checks: []eval.Check{
		check("suspended_for_oncall_confirmation", run.PendingConfirmation),
		check("non_oncall_approval_rejected", resp.StatusCode == http.StatusForbidden),
		check("zero_unapproved_rollbacks", h.cluster.Rollbacks() == 0),
	}}, nil
}
```

```go
// internal/ops/service.go
// 3. Plain Go business logic written by the builder against generated types;
//    the service verifies tenant access on the server-injected org_id.
func (s *Service) RollbackProduction(
	ctx context.Context, req *genops.RollbackArgs,
) (*genops.RollbackOutcome, error) {
	if err := s.authz.RequireTenantAccess(ctx, req.OrgID); err != nil {
		return nil, err
	}
	return s.rollouts.RollbackProduction(ctx, req.OrgID, req.Service)
}
```

## Generated plumbing, written by no model

Agents author a compact `design/` package and plain Go business logic; Goa and
Goa-AI generate the transport, validation, OpenAPI, and tool-schema code. This
method comes from the [Orders reference platform](examples/orders/README.md) in
this repository:

```go
// examples/orders/design/orders.go: what an agent writes.
Method("chat", func() {
	Description("Send a message to the refund assistant and start a run.")
	Payload(func() {
		Token("token", String, "Customer bearer token.")
		// MinLength becomes a check in the generated HTTP request decoder.
		Attribute("message", String, "What the customer says.", func() { MinLength(1) })
		Required("token", "message")
	})
	Result(RunView)
	HTTP(func() {
		POST("/chat")
		Response(StatusAccepted)
	})
})
```

```go
// examples/orders/gen/orders/service.go: what Goa generates for it.
type Service interface {
	// Send a message to the refund assistant and start a run.
	Chat(context.Context, *ChatPayload) (res *RunView, err error)
	// ...
}
```

From that declaration Goa generates the HTTP route, request decoder and
validation, typed client, and OpenAPI 3 descriptions. For LLM-assisted
development across multiple services, this has four practical effects:

- **Zero model output tokens spent on generated files.** In the
  [Orders reference platform](examples/orders/gen) (and
  [Highbay's own `gen/`](gen)), Goa generates over 70% of non-test Go code —
  routers, codecs, OpenAPI 3, validation, and tool JSON schemas — keeping each
  agent's read/edit surface to compact `design/` files and `internal/`
  interfaces. Highbay spends a second agent session on independent `eval/`
  verification rather than boilerplate; net token and wall-clock totals depend
  on your task and model.
- **Identical layout and generated shape across projects and models.** Projects
  initialized with Highbay share the same `design/`, `gen/`, `cmd/`, `internal/`,
  and `eval/` layout (existing Goa projects may also register a separate suite
  module via `highbay verification set`), plus a generated `AGENTS_QUICKSTART.md`
  map, and generated code stays identical whether Claude, Codex, or Gemini wrote
  the surrounding business logic.
- **Code-enforced guardrails at every boundary.** Changing a result type in
  `design/` and regenerating fails `go build` on stale handlers, builder checks
  compile and unit-test inside a Linux Docker container, and Highbay refuses a
  test report that touches files outside `eval/` or a builder commit whose
  `design/` differs from the agreed design or (without stored verification
  settings) alters handwritten files in `eval/` (`eval/gen/` and
  `eval/AGENTS_QUICKSTART.md` excluded).
- **Built-in architectural playbook.** Highbay serves a 12-section playbook over
  MCP at `highbay://golden-path` ([`GOLDEN_PATH.md`](internal/goldenpath/GOLDEN_PATH.md)),
  pairing Goa, Goa-AI, Story, and [Model](https://github.com/goadesign/model) C4
  diagrams with production patterns for Temporal workflows and `gen_ai.*` model
  spans (`Goa-AI`), [Pulse](https://github.com/goadesign/pulse) event streams
  (`pulse/streaming`), job queues & consistent-hash worker pools (`pulse/pool`),
  and replicated rate-limit maps (`pulse/rmap` `rmap.Join`), plus
  [Clue](https://github.com/goadesign/clue) OpenTelemetry exporter setup
  (`clue.ConfigureOpenTelemetry`) and dependency readiness probes
  (`clue/health`).

Any Python, TypeScript, Go, or web client can call the generated OpenAPI 3,
HTTP, JSON-RPC, and MCP endpoints. Highbay's built-in `highbay gen` generates
HTTP, JSON-RPC, and MCP transports and refuses designs declaring `GRPC`
transports (`protoc` is not bundled in `highbay gen`; standalone `goa gen` with
`protoc` compiles gRPC); see [`highbay gen`](docs/REFERENCE.md#highbay-gen).

## Words you will meet

| Term | Meaning |
| --- | --- |
| **The server** | The project's Highbay server, one per project folder. It keeps what you agreed, who was asked to do what and what was accepted, and it runs the checks. |
| **The page** | The server's page in your browser. You agree on changes and approve results there. |
| **Story** | A description of what the system does, written as Go in the project's design, one flow at a time. |
| **Flow** | One path through a Story: who starts it, the steps, and the outcome. A flow with a start, an outcome and no open question is *finished*. |
| **Change Card** | One proposed change: the request, the Story, open questions, concrete examples and the behavior to keep. |
| **The guide** | The role of the coding agent you talk to. It writes the Story and proposes changes. |
| **The builder** | The agent that writes the application code in `.highbay-build`. |
| **The test writer** | The agent that writes the change's scenario tests in `.highbay-tests`. |
| **Herdr Core** | The local runtime that starts interactive agent terminals. Open a role's terminal in Highbay. |
| **Chorus** | The local messaging service that carries the project conversation between the browser and agents. |

## What Highbay does not do

- **It is not a sandbox.** All agents run as you on one machine. Highbay runs
  the code they write as ordinary programs with your access to files and the
  network. Roles limit what each agent can call in Highbay, not what its
  programs can reach. The page says so next to **Build this change**. One
  check, the builder's compile and unit test run, uses a local Docker
  container; see [`check_project`](docs/REFERENCE.md#check_project).
- **It does not judge what a test asserts.** Highbay checks that every
  finished flow has a scenario test and that the tests build, then runs them.
  Whether the assertions are strong enough remains your review. The page
  shows which answers and examples the test writer says each scenario covers.
- **It operates on Go modules structured with Goa.** You can scaffold a new Go
  service or MCP gateway alongside an existing Python, TypeScript, or Go stack
  in ~2 minutes via `highbay` or `/highbay`, or adopt Highbay in an existing Goa
  repository; Highbay does not generate non-Go servers or patch arbitrary
  Express, FastAPI, or raw `net/http` routers in place.

## Install

Highbay needs:

- **macOS or Linux.**
- **Go 1.27.3** or newer.
- **Git 2.45** or newer, with your name and address configured. Highbay
  records accepted tests as commits under your identity.
- **Docker Desktop**, or another local Docker engine, for the builder's
  checks.
- **A coding agent that supports MCP.** The
  [setup reference](docs/REFERENCE.md#highbay-setup--global) lists the clients
  that setup registers.

Choose an installation method.

```bash
# Homebrew.
brew install goadesign/tap/highbay
highbay up

# npm, with Node.js 20 or later.
npx @goadesign/highbay up

# Shell installer. It verifies the release and runs highbay up.
curl -fsSL https://raw.githubusercontent.com/goadesign/highbay/main/scripts/install.sh | sh
```

Run `highbay` from your project folder. If you installed through npm, use
`npx @goadesign/highbay`.

`highbay up` checks Go, Git, and Docker, offers to install compatible Herdr Core
and Chorus executables when needed, and registers Highbay with the coding
agents it detects. Herdr Core runs the agents' terminals. Chorus carries
the messages you see in **Conversation**. Reload an already open coding
agent to discover the `/highbay` skill, and rerun `highbay up` after installing
another agent. See [prerequisites and install options](docs/REFERENCE.md#prerequisites).

You can also install from a published Go release with
`go install github.com/goadesign/highbay/cmd/highbay@latest`, then run `highbay up`.
For a development build, see [`highbay init`](docs/REFERENCE.md#highbay-init) to
select the published Highbay module a new project will use.

**Highbay needs no account and no model key.** Each coding agent uses its own
sign-in. The tests a test writer wrote receive no provider key; a test of an
agent uses a scripted model kept in the test module. A provider key or a
model gateway matters in two cases. The first is when the
builder tries prompt wording on a *live target*, a running copy of the
application that the server starts from a commit. The second is when you
register a test program of your own that calls a live model. For a live
target, export the provider's key, such as `ANTHROPIC_API_KEY` or
`OPENAI_API_KEY`, in the environment that starts your coding agent. Your
coding agent must pass that environment on to Highbay; see
[stdio integration](docs/REFERENCE.md#highbay-stdio). While no model is chosen,
live targets then use `claude-sonnet-4-6` or `gpt-5.4-mini`, and the page's
**Live target** view changes the model.

## Update Highbay

Run `highbay update` or choose **Update & Restart** when the browser offers a
new release. Highbay installs the version you approve and restarts the
project server in place. The browser session and running agent terminals
can continue. Choose **Reload** when you are ready to load the updated
workspace.

Run `highbay update -check` to see the available release without installing
it. For npm installations, use `npx @goadesign/highbay@latest` or update
the global package with `npm install -g @goadesign/highbay@latest`. See the
[update command](docs/REFERENCE.md#highbay-update).

## Start a project

**In an empty folder,** run `highbay`, choose a coding agent, and describe your
goal in **Describe**. The guide creates a blank Goa and Goa-AI starter with an
architecture diagram, an empty Story, and a test module with one startup test.
See [`init_project`](docs/REFERENCE.md#init_project).

**In a project you already have,** Highbay needs a Goa or Goa-AI project with
at least one Git commit and tests it can run. Those tests are either a test
module in Highbay's layout or a test program of your own that you register
with `highbay verification set`. With your own program, Highbay uses it for
every change and starts no test writer. See
[what a build adds](docs/HOW_IT_WORKS.md#what-a-build-adds).

The browser stays signed in during a reload while the project server is
running. Run `highbay` to reopen the page, or ask your connected coding agent
to open it. Use **Conversation** to send messages and open a role's
**Terminal** when it needs an answer or permission.
[Terminal operation](docs/REFERENCE.md#terminal-and-browser-automation)
covers `highbay --print-link`, `highbay status` and `highbay stop` for operators and
browser automation.

## Use the tools without a team

These commands need no server, no agents and no model key. They run Goa with
the module's own versions, so no separately installed `goa` is needed.

```bash
# Create a blank service and its test module.
# Add -example for a worked example, a price-answering service.
highbay init -dir shop -module example.com/shop

# Compile shop/design and print its containers, boundary edges,
# agent policies and tool contracts.
highbay preview -dir shop

# Regenerate shop/gen after you edit shop/design.
highbay gen -dir shop

# List all commands. "highbay help <command>" prints a command's flags.
highbay help
```

The [Orders example](examples/orders/README.md) is a complete platform with
its own test suite (including `SIGKILL` worker crash recovery, API restart
during pending confirmation, redacted holdouts, and a token-budget gate). From a
checkout, `highbay preview -dir examples/orders` prints its architecture. The
[reference](docs/REFERENCE.md#highbay-verify) covers `highbay verify` and
`highbay approve`, which check a commit against a test suite outside any server.

## Dive deeper

- **[How Highbay works](docs/HOW_IT_WORKS.md)**: the journey step by step, by
  who acts.
- **[Reference](docs/REFERENCE.md)**: commands, MCP tools, builds, the page and
  the server's contracts.
- **[Story language](story/README.md)**: the declarations a Story is written
  with.
- **[Golden Path architecture](docs/ARCHITECTURE_PLAYBOOK.md)**: the built-in
  rules every agent follows, and who owns what in Highbay.
- **[Story flows and the tests a build requires](docs/STORY_TEST_DESIGN.md)**:
  for contributors, how a Story becomes tests and how Highbay checks them.
- **[Orders example](examples/orders/README.md)**: a complete platform with
  its test suite.
- **[Product direction](docs/PRODUCT_DIRECTION.md)**: what Highbay is meant to
  be and for whom.

> **Why the name?** A high bay is a tall factory hall where large things are
> assembled and inspected before they leave. Highbay does the same for a
> change: it is built, checked against what you agreed, and only then merged.

## License

Highbay is released under the [MIT License](LICENSE).
