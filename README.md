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
  <a href="https://mcprush.com/to/goadesign-highbay-mcp?ref=badge"><img alt="Highbay MCP on mcprush" src="https://mcprush.com/badge/goadesign-highbay-mcp/score.svg"></a>
  <a href="https://serverdex.io/server/io-github-goadesign-highbay/score"><img alt="Highbay on Serverdex" src="https://serverdex.io/server/io-github-goadesign-highbay/badge.svg" height="20"></a>
  <a href="https://pkg.go.dev/github.com/goadesign/highbay"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/goadesign/highbay.svg"></a>
  <a href="https://github.com/goadesign/highbay/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Highbay

**Consistent architecture across the platform your coding agent builds. Services, MCP servers, and AI agents — ~70% of the code generated, not prompted.**

Left alone, coding agents wire every repo and feature differently — turning a
growing platform into mismatched services. Your agent drafts a short contract;
you confirm it. Highbay compiles it into servers, validation, and tool schemas —
no model in the loop. Your agent writes only the handlers, in a plain Go service
your Python, TypeScript, or web apps can call.

Highbay is an open-source (MIT) local MCP server that plugs into the coding
agents you already run — Claude Code, Codex, Gemini CLI, Cursor, Antigravity,
Windsurf, Copilot, and VS Code:

- **One consistent layout & Golden Path playbook.** Every service, MCP server,
  and multi-agent platform shares the same two-module structure (`design/`,
  `gen/`, `cmd/`, `internal/`, and `eval/`) and built-in `highbay://golden-path`
  rules for auth, OpenTelemetry tracing & error-flushed logging
  ([Clue](https://github.com/goadesign/clue)), Redis Streams, job queues, &
  worker pools ([Pulse](https://github.com/goadesign/pulse)), and durable
  workflows — so engineers and coding agents move across services without
  re-learning conventions or maintaining `.cursorrules` files.
- **~70% generated at zero LLM tokens.** From one confirmed contract, Highbay
  compiles HTTP, JSON-RPC, and MCP servers, OpenAPI 3 specs (callable from
  Python, TypeScript, Go, or web apps), request validation, `Inject`
  server-bound identity, `BoundedResult` pagination cursors, and `Confirmation`
  pause gates — leaving clean typed interfaces in `internal/` for your coding
  agent to implement.
- **Confirm open choices upfront, verify in a separate copy before merge.** Your
  coding agent drafts the contract and opens a plain-language Change Card and
  compiled C4 architecture diagram in your browser. Once confirmed, a second
  coding agent in `.highbay-tests` writes scenario checks in `eval/` from that
  agreement, blocking merge until checks pass.

```bash
# macOS or Linux (Homebrew)
brew install goadesign/tap/highbay
highbay up

# Or with Node.js >= 18 (Linux or macOS)
npx @goadesign/highbay up
```

Run `highbay up` once (`highbay update` updates the binary, skills, and MCP
registrations in place). After that, open a new service folder (or existing Goa
repo) in your coding agent and invoke `/highbay` (`$highbay` in Codex). That
first call starts or attaches to the project's Highbay server on demand; there
is nothing to start per session. [Install](#install) lists prerequisites and
`go install`.

## One change, start to finish

Open an empty folder in your coding agent and say:

> `/highbay` — build a multi-tenant incident platform: a triage agent and
> remediation agent with MCP tools to search pod logs, correlate deploys, and
> roll back services with on-call approval.

The prompt leaves an architectural boundary open: which actions may
`RemediationAgent` execute autonomously versus pause for human approval?

| Cluster action | Autonomous staging + gated production | Approval required everywhere |
| --- | --- | --- |
| Pod logs & deploy correlation | Autonomous (`BoundedResult` pages) | Autonomous (`BoundedResult` pages) |
| Staging rollback | Autonomous self-healing | Pauses at `Confirmation` |
| Production rollback | Pauses at `Confirmation` for on-call | Pauses at `Confirmation` for on-call |

A freehand coding agent silently guesses the staging-versus-production rule (and
whether a `viewer` JWT may call `/runs/{id}/approval`), returns unbounded log
slices that pass on 20 lines and blow prompt caps on 50 MB crash logs, and locks
its own assumptions into green unit tests. Highbay surfaces open choices to you,
compiles architectural and runtime guardrails into Go, and checks the agreed
behavior in a separate working copy:

1. **Describe.** Your coding agent starts or joins the project's Highbay server
   and opens its page in your browser. In an empty folder it first creates a
   blank Go starter and its first commit (~2 minutes). It then writes a *Story*
   (the system's actors and flows in Go) and proposes a *Change Card*: your
   request, open questions, concrete examples, and behaviors to preserve.
2. **Agree.** Answer open questions and confirm the Change Card in your browser.
   If you pick an option that differs from the draft, one click asks your coding
   agent to revise the card before you lock the agreement.
3. **Review the architecture.** Inspect the live C4 topology — services, agents,
   toolsets, tool-call budgets, server-injected fields (`Inject`), and
   `Confirmation` gates — compiled directly from `design/` before handler code
   exists.
4. **Build.** Pick a coding agent and model for each worker role inline and
   press **Build this change**. The *builder* implements handlers in
   `.highbay-build`. The *test writer* implements one scenario test per finished
   flow in `.highbay-tests`. Highbay accepts those tests only after confirming
   nothing outside `eval/` changed and compiling the test binary.
5. **Verify.** The builder asks Highbay to verify its commit. Highbay rejects
   any candidate commit that alters `design/` or written tests in `eval/`, builds
   the candidate, and runs the locked test commit against it. When `Confirmation`
   pauses the production rollback and the builder's `/runs/{id}/approval`
   endpoint forgets to enforce the `oncall` role (`expected 403 Forbidden, got
   200 OK`), verification fails and blocks approval until fixed.
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
// design/agents.go
// 1. The agent and tool contract drafted by your coding agent and confirmed by you.
Agent("remediation", "Safe service rollbacks", func() {
	Use("cluster")
	RunPolicy(MaxToolCalls(4))
})

Tool("rollback_service", func() {
	// Inject omits org_id from model-visible tool JSON and populates it in Go.
	Inject("org_id")
	// Confirmation pauses the agent run before RollbackService executes.
	Confirmation("On-call approval")
	BindTo("Ops", "RollbackService")
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
// 3. Plain Go business logic written by the builder against generated types.
func (s *Service) RollbackService(
	ctx context.Context, req *genops.RollbackPayload,
) (*genops.RollbackResult, error) {
	orgID := auth.VerifiedOrgID(ctx)
	return s.rollouts.Rollback(ctx, orgID, req.Env, req.Service)
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

- **Zero model output tokens spent on generated files.** Goa generates roughly
  70% of non-test Go code (see [`gen/`](gen) and [`examples/orders/gen/`](examples/orders/gen))
  — routers, codecs, OpenAPI 3, validation, and tool JSON schemas — keeping each
  agent's read/edit surface to compact `design/` files and `internal/`
  interfaces. Highbay spends a second agent session on independent `eval/`
  verification rather than boilerplate; net token and wall-clock totals depend
  on your task and model.
- **Identical layout and generated shape across projects and models.** Every
  Highbay project uses `design/`, `gen/`, `cmd/`, `internal/`, and `eval/` (plus
  a generated `AGENTS_QUICKSTART.md` map), and generated code stays identical
  whether Claude, Codex, or Gemini wrote the surrounding business logic.
- **Code-enforced guardrails at every boundary.** Changing a result type in
  `design/` and regenerating fails `go build` on stale handlers, builder checks
  compile and unit-test inside a Linux Docker container, and Highbay refuses a
  test report that touches files outside `eval/` or a builder commit that alters
  `design/` or handwritten files in `eval/`.
- **Built-in architectural playbook.** Highbay serves a 12-section playbook over
  MCP at `highbay://golden-path` ([`GOLDEN_PATH.md`](internal/goldenpath/GOLDEN_PATH.md)),
  pairing Goa, Goa-AI, Story, and [Model](https://github.com/goadesign/model) C4
  diagrams with production patterns for Temporal workflows,
  [Pulse](https://github.com/goadesign/pulse) event streams (`pulse/streaming`),
  job queues & consistent-hash worker pools (`pulse/pool`), and replicated
  rate-limit maps (`pulse/rmap`), plus [Clue](https://github.com/goadesign/clue)
  OpenTelemetry spans, error-flushed buffered logs (`clue/log`), and health
  probes (`clue/health`).

Any Python, TypeScript, Go, or web client can call the generated OpenAPI 3,
HTTP, JSON-RPC, and MCP endpoints. Highbay's built-in `highbay gen` generates
HTTP, JSON-RPC, and MCP transports and refuses designs declaring `GRPC`
transports (`protoc` is not bundled in `highbay gen`; standalone `goa gen` with
`protoc` compiles gRPC); see [`hbay gen`](docs/REFERENCE.md#hbay-gen).

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
| **Herdr** | An optional terminal workspace that shows the agents side by side. |
| **Chorus** | The messaging service the agents use when they run in Herdr. |

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
  in ~2 minutes via `/highbay`, or adopt Highbay in an existing Goa repository;
  Highbay does not generate non-Go servers or patch arbitrary Express, FastAPI,
  or raw `net/http` routers in place.

## Install

Highbay needs:

- **macOS or Linux.**
- **Go 1.27.3** or newer.
- **Git 2.45** or newer, with your name and address configured. Highbay
  records accepted tests as commits under your identity.
- **Docker Desktop**, or another local Docker engine, for the builder's
  checks.
- **A coding agent that supports MCP.** The
  [setup reference](docs/REFERENCE.md#hbay-setup--global) lists the clients
  that setup registers.

```bash
# macOS or Linux (Homebrew)
brew install goadesign/tap/highbay
highbay up

# Linux or macOS (Node.js >= 18)
npx @goadesign/highbay up

# Or with Go 1.27.3+
go install github.com/goadesign/highbay/cmd/highbay@latest
highbay up
```

Run `highbay update` anytime to update the binary, `/highbay` skills, MCP
registrations, and Herdr companion in place. From a checkout of this
repository, `go install ./cmd/highbay` installs the version you are reading. A
build that Go reports as `(devel)` or `+dirty` must be told which published
Highbay version a new project requires; see [`hbay init`](docs/REFERENCE.md#hbay-init).

`highbay up` registers Highbay as an MCP server with the coding agents it
detects and installs the `/highbay` skill. It leaves unrelated settings
alone. Reload your agent's MCP configuration or start a new session if
Highbay does not appear. Run `highbay up` again after you add a coding agent or
move the `highbay` executable.

**Side-by-side panes are optional.** With Herdr and Chorus v0.14.0 or newer
installed, Highbay opens the builder and the test writer beside your coding
agent. Without Herdr, it runs them in the background once you agree to that
on the page. Install both, open Herdr once, then run `highbay up` again:

```bash
curl -fsSL https://releases.herdr.dev/core/install.sh | sh
curl -fsSL https://releases.herdr.dev/app/install.sh | sh
curl -fsSL https://raw.githubusercontent.com/goadesign/chorus/main/scripts/install.sh | sh
```

`highbay up` starts Chorus when it is installed, and Highbay starts it again on
demand if it stops later (for example after a reboot).
[Prerequisites](docs/REFERENCE.md#prerequisites) and
[setup details](docs/REFERENCE.md#hbay-setup--global) cover the rest.

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
[stdio integration](docs/REFERENCE.md#hbay-stdio). While no model is chosen,
live targets then use `claude-sonnet-4-6` or `gpt-5.4-mini`, and the page's
**Live target** view changes the model.

## Start a project

**In an empty folder,** say `/highbay` and what you want to build. With
`/highbay` alone, your coding agent opens the page and asks. It then creates
a blank starter in place: a Goa and Goa-AI service with no methods yet, an
architecture diagram, an empty Story, and a test module with one test that
the program starts. Nothing in it is about someone else's product. See
[`init_project`](docs/REFERENCE.md#init_project).

**In a project you already have,** Highbay needs a Goa or Goa-AI project with
at least one Git commit and tests it can run. Those tests are either a test
module in Highbay's layout or a test program of your own that you register
with `highbay verification set`. With your own program, Highbay uses it for
every change and starts no test writer. See
[what a build adds](docs/HOW_IT_WORKS.md#what-a-build-adds).

If you close the page, ask your coding agent to open Highbay again.
[Terminal operation](docs/REFERENCE.md#terminal-and-browser-automation)
covers `highbay --print-link`, `highbay status` and `highbay stop` for operators
and browser automation.

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
[reference](docs/REFERENCE.md#hbay-verify) covers `highbay verify` and
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
