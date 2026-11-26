<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/img/highbay-banner-dark.png">
    <img alt="Highbay" src="docs/img/highbay-banner.png" width="960">
  </picture>
</p>

<p align="center">
  <a href="https://pkg.go.dev/github.com/goadesign/highbay"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/goadesign/highbay.svg"></a>
  <a href="https://modelcontextprotocol.io"><img alt="MCP" src="https://img.shields.io/badge/MCP-2026--07--28-6E56CF"></a>
  <a href="https://github.com/goadesign/highbay/blob/main/LICENSE"><img alt="MIT License" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
</p>

# Highbay

**Your agent's tests pass. Did it build what you meant?**

Highbay builds Go services and AI agents with the coding agents you already
use, such as Claude Code, Codex or Cursor. It is free, open source, and runs
on your machine.

A coding agent working alone answers the questions your request left open,
then writes tests that agree with its own answers. The tests pass, and nobody
checked the answers.

Highbay splits that job. You choose the behavior on a page in your browser.
[Goa](https://github.com/goadesign/goa) and
[Goa-AI](https://github.com/goadesign/goa-ai) generate the transports, schemas
and validation from a Go design, so no model writes them. One agent writes the
service code, a different agent writes the scenario tests, and Highbay runs
the accepted tests against the exact commit you then approve and merge.

```bash
go install github.com/goadesign/highbay/cmd/hbay@latest
hbay up
```

Run `hbay up` once. After that, open a project folder in your coding agent and
say `/highbay` (`$highbay` in Codex). That call starts the project's Highbay
server when one is needed; there is nothing to start per session.
[Install](#install) lists what Highbay needs.

## One change, start to finish

Open an empty folder in your coding agent and say:

> `/highbay` — build a refund assistant: refunds up to $50 get instant store
> credit; larger ones wait for a manager.

The request leaves one thing unsaid: does $50 count sales tax? One order tells
the two readings apart.

| Order | If $50 means the items, before tax | If $50 means the total charged |
| --- | --- | --- |
| $48.00 items + $4.32 tax = $52.32 | Instant $52.32 credit | Waits for a manager |

An agent working alone picks one reading, then tests a $40 order and a $60
order. Both tests pass under either reading, so they cannot tell you which one
it built. Highbay asks you, then tests the $48 order.

Here is how the change reaches your branch:

1. **Describe.** Your coding agent starts or joins the project's Highbay
   server and opens its page in your browser. In an empty folder it first
   creates a blank Go project and its first commit, which takes a couple of
   minutes. It then writes a *Story*, a description of the system's flows in
   Go, and proposes a *Change Card*: your request, the questions it leaves
   open, concrete examples, and the behavior to keep.
2. **Agree.** On the page, answer the questions and confirm. For the refund
   rule, the card shows the $48 order under each answer. If your answer
   differs from the draft, your coding agent revises the card before you
   confirm.
3. **Review the architecture.** The page draws the services, agents and tools
   that the agreed design declares, before any of them is implemented.
4. **Build.** Choose a coding agent and a model for two roles, then press
   **Build this change**. The *builder* writes the service. The *test writer*
   writes one scenario test for each finished flow, in its own working copy.
   Highbay accepts those tests only after it compiles them and confirms that
   nothing outside the test module changed.
5. **Verify.** The builder asks Highbay to verify its commit. Highbay builds
   that commit and runs the accepted tests against it. A failed run goes back
   to the builder, and nothing can be approved until a run passes.
6. **Approve and merge.** Read the report, press **Approve this result**, then
   **Merge into current branch**.

Only you can confirm a change, start a build, approve a result or merge. Each
agent connects to the server with the permissions of its own role. You can go
back at any step: revise the change, build again, or have the tests reported
again. [How Highbay works](docs/HOW_IT_WORKS.md) covers every step, and
[Going back](docs/HOW_IT_WORKS.md#going-back) says what stays valid when you
do.

In one moderated trial, an AI agent played a first-time developer on a sample
property-listing app. The test writer's scenario for withdrawing a listing had
32 named checks, and the builder's first commit failed two of them. Highbay
reported seven of eight scenarios passing, the builder fixed it, and the same
tests passed eight of eight about three minutes later. That was a trial with a
scripted model, not a production incident.

## The agreement is code

What you agree to becomes three small pieces of Go that use the same order and
the same numbers. These excerpts illustrate the refund example; they are not
files in this repository.

```go
// design/story.go
// 1. The flow you confirmed, drafted by your coding agent.
package design

import . "github.com/goadesign/highbay/story/dsl"

var _ = Story("Refunds", func() {
	Actor("customer", "Customer")
	// A flow with a start, an outcome and no open question is finished.
	// Every finished flow needs a scenario test before a build counts.
	Flow("instant_credit", "Up to $50 subtotal", func() {
		StartsWith("ask", "customer", "$48.00 + $4.32 tax")
		Outcome("credit", "customer", "Instant $52.32 credit")
	})
})
```

```go
// eval/acceptance/refunds.go
// 2. The scenario, written by the test writer from your answer.
func (h *Hooks) InstantCredit(ctx context.Context) (eval.Result, error) {
	// $48.00 + $4.32 tax = $52.32: the order that tells the readings apart.
	out, err := h.refund(ctx, 4800, 432)
	if err != nil {
		return eval.Result{}, err
	}
	return eval.Result{Checks: []eval.Check{
		check("status", out.Status == "credit"),
		check("cents", out.Cents == 5232),
	}}, nil
}
```

```go
// internal/refunds/service.go
// 3. The rule, written by the builder against the payload and result types
//    that Goa generated from the design.
func (s *Service) RequestRefund(
	ctx context.Context, req *genrefunds.RequestPayload,
) (*genrefunds.RefundResult, error) {
	total := req.SubtotalCents + req.TaxCents
	if req.SubtotalCents <= 5000 { // the limit counts the items, as you chose
		return s.issueCredit(ctx, req.OrderID, total)
	}
	return s.queueReview(ctx, req.OrderID, total)
}
```

If the builder had compared `total` instead, the scenario would fail on the
$48 order and the run could not be approved.

## Generated plumbing, written by no model

Agents write a compact Go design and the business logic. Goa and Goa-AI
generate the rest from the design. This method comes from the
[Orders example](examples/orders/README.md) in this repository:

```go
// examples/orders/design/orders.go: what an agent writes.
Method("chat", func() {
	Description("Send a message to the refund assistant and start a run.")
	Payload(func() {
		Token("token", String, "Customer bearer token.")
		// MinLength becomes a generated check on every request.
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

From that one method Goa also generates the HTTP route, the request decoder
with its length check, a typed client and the OpenAPI description. For a
coding agent this has three effects:

- **No model writes the generated files.** The generator does. Agents still
  spend tokens on the design and the logic, so the overall saving depends on
  the change. Highbay does not claim a measured figure.
- **The same shape from any model.** Generated code looks the same whether
  Claude, Codex or Gemini wrote the logic around it.
- **A changed contract breaks the build.** Change a method's payload and
  regenerate: a handler with the old signature no longer compiles.

Highbay generates the transports a design declares: HTTP, JSON-RPC and MCP. It
refuses a design with gRPC transports; see
[`hbay gen`](docs/REFERENCE.md#hbay-gen).

## Words you will meet

| Term | Meaning |
| --- | --- |
| **The server** | The project's Highbay server, one per project folder. It keeps what you agreed, who was asked to do what and what was accepted, and it runs the checks. |
| **The page** | The server's page in your browser. You agree on changes and approve results there. |
| **Story** | A description of what the system does, written as Go in the project's design, one flow at a time. |
| **Flow** | One path through a Story: who starts it, the steps, and the outcome. A flow with a start, an outcome and no open question is *finished*. |
| **Change Card** | One proposed change: the request, the Story, open questions, concrete examples and the behavior to keep. |
| **The guide** | The role of the coding agent you talk to. It writes the Story and proposes changes. |
| **The builder** | The agent that writes the application code. |
| **The test writer** | The agent that writes the change's scenario tests, in its own working copy. |
| **Herdr** | An optional terminal workspace that shows the agents side by side. |
| **Chorus** | The messaging service the agents use when they run in Herdr. |

## What Highbay does not do

- **It is not a sandbox.** All agents run as you on one machine. Highbay runs
  the code they write as ordinary programs with your access to files and the
  network. Roles limit what each agent can do in Highbay, not what its
  programs can reach. The page says so next to **Build this change**. One
  check, the builder's compile and unit test run, uses a local Docker
  container; see [`check_project`](docs/REFERENCE.md#check_project).
- **It does not judge what a test asserts.** Highbay checks that every
  finished flow has a scenario test and that the tests build, then runs them.
  Whether the assertions are strong enough remains your review. The page
  shows which answers and examples the test writer says each scenario covers.
- **It builds Goa projects only.** Highbay works on Go services and agents
  designed with Goa and Goa-AI. Other languages and frameworks are out of
  scope.

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
go install github.com/goadesign/highbay/cmd/hbay@latest
hbay up
```

From a checkout of this repository, `go install ./cmd/hbay` installs the
version you are reading.

`hbay up` registers Highbay as an MCP server with the coding agents it
detects and installs the `/highbay` skill. It leaves unrelated settings
alone. Reload your agent's MCP configuration or start a new session if
Highbay does not appear. Run `hbay up` again after you add a coding agent or
move the `hbay` executable.

**Side-by-side panes are optional.** With Herdr and Chorus v0.14.0 or newer
installed, Highbay opens the builder and the test writer beside your coding
agent. Without Herdr, it runs them in the background once you agree to that
on the page. Install both, open Herdr once, then run `hbay up` again:

```bash
curl -fsSL https://releases.herdr.dev/core/install.sh | sh
curl -fsSL https://releases.herdr.dev/app/install.sh | sh
curl -fsSL https://raw.githubusercontent.com/goadesign/chorus/main/scripts/install.sh | sh
```

`hbay up` starts Chorus when it is installed. If Chorus stops later, for
example after a reboot, run `chorus up` before you start a build in Herdr.
[Prerequisites](docs/REFERENCE.md#prerequisites) and
[setup details](docs/REFERENCE.md#hbay-setup--global) cover the rest.

**Highbay needs no account and no model key.** Each coding agent uses its own
sign-in, and the tests a test writer wrote run without a model key. A key
matters only when your own application calls a model and the builder tries
prompt wording on a *live target*, a running copy of the application that the
server starts from a commit. In that case, export the provider's key, such as
`ANTHROPIC_API_KEY` or `OPENAI_API_KEY`, in the environment that starts your
coding agent. Your coding agent must pass that environment on to Highbay; see
[stdio integration](docs/REFERENCE.md#hbay-stdio). While no model is chosen,
live targets then use `claude-sonnet-4-6` or `gpt-5.4-mini`, and the page's
**Live target** view changes the model.

## Start a project

**In an empty folder,** `/highbay` creates a blank starter in place: a Goa and
Goa-AI service with no methods yet, an architecture diagram, an empty Story,
and a test module with one test that the program starts. Nothing in it is
about someone else's product. See
[`init_project`](docs/REFERENCE.md#init_project).

**In a project you already have,** Highbay needs a Goa or Goa-AI project with
at least one Git commit and tests it can run. Those tests are either a test
module in Highbay's layout or a test program of your own that you register
with `hbay verification set`. With your own program, Highbay uses it for
every change and starts no test writer. See
[what a build adds](docs/HOW_IT_WORKS.md#what-a-build-adds).

If you close the page, ask your coding agent to open Highbay again.
[Terminal operation](docs/REFERENCE.md#terminal-and-browser-automation)
covers `hbay --print-link`, `hbay status` and `hbay stop` for operators and
browser automation.

## Use the tools without a team

These commands need no server, no agents and no model key. They run Goa with
the module's own versions, so no separately installed `goa` is needed.

```bash
# Create a blank service and its test module.
# Add -example for a worked example, a price-answering service.
hbay init -dir shop -module example.com/shop

# Compile shop/design and print its containers, boundary edges,
# agent policies and tool contracts.
hbay preview -dir shop

# Regenerate shop/gen after you edit shop/design.
hbay gen -dir shop

# List all commands. "hbay help <command>" prints a command's flags.
hbay help
```

The [Orders example](examples/orders/README.md) is a complete platform with
its own test suite. From a checkout, `hbay preview -dir examples/orders`
prints its architecture. The [reference](docs/REFERENCE.md#hbay-verify) covers
`hbay verify` and `hbay approve`, which check a commit against a test suite
outside any server.

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
