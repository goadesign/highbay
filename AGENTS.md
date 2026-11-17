# Surla Agent Guide

Surla is a Goa and goa-ai control plane that takes a feature request through
intent (CP1), architecture preview (CP2), live prompt drafts (CP3),
verification (CP4), and promotion checks (CP5). Make the smallest correct
change while preserving strong contracts, clear ownership, and required
capabilities.

These instructions are self-contained and apply throughout this repository;
nested `AGENTS.md` files specialize their directories. Personal/global rules
may add applicable defaults when available; this guide does not require a
private instruction file. Explicit user instructions govern the task within
platform safety. Explain material conflicts before editing and apply each
review or design requirement once, without duplicate approval gates.

This repository is public. Never publish private projects, customers,
internal services/repositories, credentials, or telemetry in any repository
or GitHub artifact.

## 1. Find the contract and active work

Use the relevant entry below, not the entire document set before each task:

| Question | Authoritative starting point |
| --- | --- |
| What is Surla and how does a user start? | [README.md](README.md) |
| How does the user journey work? | [How it works](docs/HOW_IT_WORKS.md) |
| What do commands and contracts accept/return? | [Reference](docs/REFERENCE.md) |
| Which component owns a concern? | [Architecture playbook](docs/ARCHITECTURE_PLAYBOOK.md), then owning source |
| Which built-in product rules govern a design? | [Golden Path](internal/goldenpath/GOLDEN_PATH.md) |
| Which checks apply? | [CI workflow](.github/workflows/ci.yml) and Section 5 below |

For sustained team work, the channel pin points to one long-term work index
outside the pin's size limit. The index links requirements, interaction
specifications/exports, engineering designs, current milestones, and evidence.
Each subject has one authoritative home. Keep the pin a short lead/ownership/
next-action summary; do not create competing plans in the pin and documents.
For work without a channel, make the existing index discoverable from its
owning documentation or task handoff. Create one only when sustained work
needs it and none exists, in a location appropriate to its audience.

The work index identifies the active milestone, owner, completion conditions,
unresolved decisions, dependencies, next ready tasks, and deferred requests.
Record user requests and decisions when received, including deferred work.
Label product requirements,
interaction specifications, engineering designs, code, and verification
separately; a visual export or draft is not proof that implementation is ready
or tested. Private operational reports stay outside the public repository;
shared docs must not contain machine-local paths or private evidence.

### Work and review rules

- Identify the deliverable, completion conditions, unresolved decisions, and
  next step. Explain outcome, steps, and risks before changing three or more
  files. Answer questions before mutations; then continue authorized work
  through implementation, verification, and authorized publication. Ask only
  for missing facts, material user decisions, or required permission. Explain
  choices with a concrete example and Pros/Cons; do not re-ask settled ones.
- Resolve findings that prevent completion; record independent improvements
  without broadening the task. A blocked activity pauses that activity, not
  independent ready work. Give concise updates at meaningful milestones,
  findings, or blockers. Explain failures and recovery before repairing them;
  never claim an outcome from an assignment or successful command alone.
- Read owning source, affected callers, and tests before technical claims or
  edits. Trackers, summaries, and peer notes locate work and decisions, not
  implementation truth. Distinguish positive requirements from rejection
  fixtures. Verify the specific claim in a question; stop investigating when
  required decisions have evidence and acceptance checks are defined.
- Fix mechanical corrections directly. For bounded behavior changes, state
  before/after behavior, owner, affected callers, and proof. Before ownership,
  schema/public-contract, auth, persistence, lifecycle, or external-semantic
  changes, trace all affected `origin/main` callers from input through
  storage/output, check composition roots and owning contracts, compare
  deletion/correcting the owner/adding a mechanism, and state journey,
  compatibility, migration, rollback, and proof where relevant. Revisit only
  decisions affected by new evidence; do not impose the full gate on status
  questions or factual corrections.
- For each new field, endpoint, dependency, record, helper, retry, setting,
  limit, or caller step, identify the required case existing mechanisms
  cannot serve. Delete unnecessary state, copies, scans, and hops before
  managing them with caps, caches, wrappers, or modes. Helpers enforce an
  invariant, own a contract, remove meaningful duplication, or add domain
  language. Correct consumers instead of hiding truthful errors or telemetry.
  Limits belong to their protected resource and narrowest invariant lifetime.
- Before changing/removing a capability, cite the requiring request or
  product contract. Preserve affected configured, persisted, and observed
  callers, including valid cases outside a failure, with positive tests in
  the owning layer (Section 5).
  Test thresholds below/at/above with units/alignment/inclusivity; verify migrations
  before removal. Give temporary compatibility paths removal conditions.
  Search/delete downstream branches with no remaining producer. Do not treat
  accidental validation loopholes or negative fixtures as capabilities.
  Verify preserved outcomes after rollout; stop promotion on new rejections,
  missing data, routing changes, or material latency regressions.
- Explain delegation before assigning it. Give one owner, scope/files,
  inputs, dependencies, and completion conditions; preserve others' edits.
  A lead may also implement, but unblocks teammates first. Settle shared
  contracts before dependent work; use independent fact-finding while they
  are unsettled. Necessary waiting beats invented work or duplicate checks.
- One non-author peer explicitly approves a non-trivial change before final
  publication/merge. A design-touching change starts with a fresh read-only
  reviewer given the original outcome, contracts/callers, and diff without
  author defenses. Check the workflow first, then challenge simpler designs
  and net deletion. The same reviewer may verify scoped fixes and approve
  the resulting head; changed code never inherits approval automatically.
  Use a fresh reviewer for fundamental redesign or unavailability. Concrete
  correctness/security/compatibility/outcome defects block; preferences do
  not. Resolve or explain final review comments and state uncovered files.
- Prefer direct tools and `rg`; batch independent reads and inspect every
  result, keeping dependencies/mutations sequential. Close stdin and use
  non-interactive flags for package runners and CLIs. Bound
  waits by success/failure/unexpected-state/timeout exits and verify actual
  client wake-up support; a background session ID alone is not monitoring.
- Before parsing costly/transient diagnostics, privately save bounded
  stdout/stderr, including partial failure/timeout output, with command,
  target, timestamps, exit status, truncation/capture errors, and cleanup.
  Capture only authorized data, never credentials/customer content; share
  redacted evidence. Reuse complete captures and fix readers before reruns.
  Verify consumer-visible success, including save/reload persistence when
  claimed; exit zero or file readback alone does not prove that outcome.

## 2. Preserve Surla's user journey

- For a new surface or substantial redesign, establish the user, value,
  built-in rules, and scope from the README, Golden Path, and user decisions.
  Ask only about genuinely unknown product purpose/users before designing.
- An LLM drives the full lifecycle through MCP with one running server.
  Derive known paths, targets, and inputs from the workspace; do not require
  file/state uploads the server can read, phase-specific restarts, or CLI
  escape steps. Each unavailable capability names the step that enables it.
- Preserve in-place refinement, replays, and backward loops on the same
  identity. State what stays valid and what needs reconfirmation; do not
  silently reuse stale decisions. Managed and user-attached agents share
  the execution boundary. Developers build/test before committing.
- Before splitting a usability report into fixes, identify shared flaws
  across the whole journey. Verify onboarding or multistep changes with
  `surla-ux-review` or an equivalent end-to-end walkthrough and record the
  findings; a walkthrough is evidence for the change, not a test to repeat
  in every package. Keep examples/starters focused on CP1–CP5 and validate
  them through the real CLI/MCP entrypoints in the named journey tests,
  using existing test support, not parallel harnesses.
- Explain the concrete input, owner action, and returned result. Define
  Change Card, `request_revision`, holdout, and run directory on first use.
  For example, `get_change` reads the card for the caller's `change_id` and
  returns it only to its creator. Separate current behavior from proposals.

## 3. Ownership and model-facing contracts

- `internal/controlplane` owns Change Card files. `internal/verifyrun` owns
  verification run directories; the project server that `surla` starts and
  its `surla verify-run` processes share them through that package as one
  logical service. Distinct
  services do not share private storage. Ask the owner through its generated
  API rather than adding shared persistence or copying running facts into
  configuration.
- The project server binds `PromotionTarget` from the promotion settings
  stored with `surla promotion set`; Surla never fabricates the promotion
  inputs it reports. Accepted verification work
  runs in a server-owned process and survives client disconnects; correctness
  must not depend on client retries except fresh client-held credentials.
- Structural HTTP/MCP validation belongs in Goa design. Reject unknown
  fields and trailing data in stored files. Validate/authorize at owning
  boundaries and trust constructed invariants internally; fail fast rather
  than hide missing guaranteed values. Authorize once at entry, not in
  internal loops/pages, and never infer authority from role/privilege names.
  Handwritten validation covers cross-field rules, access, storage loads,
  model semantics, security, or documented generator gaps. Internal service
  calls use generated typed clients and gRPC/Protobuf; domain APIs expose
  resources/operations, while UI code owns layout.
- Derive mechanical context from typed results/runtime state; do not ask
  the model to repeat identity, proofs, or handles code can correlate.
  goa-ai `Inject` is for required string context fixed at run start, not
  changing result history. Never choose a heuristic latest result across
  concurrent calls. Preserve original model arguments separately from
  injected values and reject invalid model output rather than repairing it
  with heuristics. Semantic interpretation remains model-driven.
- Author prompts and tool descriptions against the rendered task, visible
  schemas, and actual results. Include only facts that change valid actions,
  arguments, or stopping; specialize facts known at generation/render time.
  Describe that tool's contract, name returned IDs needed next, and keep
  cross-tool routing in the caller. Never reference invisible context/tools;
  tool-only prompts produce calls, not post-loop synthesis. Review model
  controls through schema, codec, executor, correction, prompt, UI,
  persistence, and tests rather than adding a prompt protocol for missing APIs.
- Before relying on deployed external behavior, compare its public contract,
  actual target configuration, and observed execution. Defaults/comments/tests
  alone do not prove it. Stop the affected change on disagreement and disclose
  inaccessible evidence; for greenfield work, prove against proposed contracts
  without claiming live evidence. Deployment resources belong with their
  owning service, not another service's configuration.
- A goa-ai `BoundedResult` page bounds one model-visible tool result; an
  operator's verification `timeout` bounds one CP4 run. Apply limits before
  expensive reads. Use generated continuation, with code owning cursors and
  progress per independent call chain and the model choosing whether more
  evidence is useful. Each continuation progresses or explains its blocker.
  Keep counts/summaries separate from detail pages; report coverage and more
  remaining. No unbounded pagination, identical retries, or claims of full
  coverage from partial/empty evidence.
- Builder-facing output goes through `Report.ForBuilders()`. Never expose
  holdout scenario names, checks, or logs to a coding agent.
- Build and start agent-written candidate binaries only through
  `verify/candidate` (digest-pinned, explicit environment, own state folder),
  inside a process group an operation owns and stops: verification and live
  targets use `internal/verifyrun`'s supervised group; any other caller
  passes a runner that gives the command its own group. A process that starts
  its own session leaves the group and is not stopped. These are ordinary
  processes with the user's file and network access; never describe them as
  confined.

## 4. Generated code and UI

Use the toolchain in the affected module's `go.mod`, not another repository's
version policy. Format Go with `go fmt ./...`, separate standard-library
imports, and prefix generated-package aliases with `gen`. Use short lowercase
packages, `lower_snake_case.go`, and new files under 1000 lines; do not move
unchanged code/tests in bugfixes merely to meet a size guideline. Use `any`,
`%w` with `errors.Is/As`, `len(x) == 0` without nil checks, and `min`/`max`/`clear`;
never discard errors with `_ =`.

Order types, consts, vars, public functions/methods, then private functions/
methods, keeping main logic before helpers. Prefer named concurrency helpers,
grouped type declarations, combined adjacent same-type parameters, multiline
blocks/trailing-comma literals, and signatures within 100 columns. Unused
parameters are only for interface satisfaction. Composition roots read
flags/environment and inject clients; runtime libraries do not construct
clients from deployment addresses/settings.

Comment every non-trivial declaration, including private functions and fields.
Exported GoDoc explains purpose, callers, invariants, ownership/lifetime, and
errors; private comments state contracts rather than repeat names. Comment
multistep entries/phase transitions. Re-read changed files end to end.

Expected absence is a value; `not_found` means a required resource failed to
load. Do not encode operations with nil versus empty collections or weaken
required fields to repair one caller. Keep required fields, enums, formats,
and lengths in the owning design. Normalize strings only at external
boundaries where whitespace is insignificant.

Never hand-edit these generated files/directories:

- `gen/`
- `internal/targetprompt/gen/`
- `ui/checkpoint/src/types.ts`
- `internal/controlplane/checkpoint.html`
- `internal/controlplane/views_page_script_gen.go`
- `internal/goldenpath/decision_catalog.md`

After changing `design/`, run the module-pinned generator:

```sh
go run goa.design/goa/v3/cmd/goa gen github.com/goadesign/surla/design </dev/null
```

After changing `design/targetprompt/`:

```sh
go run goa.design/goa/v3/cmd/goa gen github.com/goadesign/surla/design/targetprompt -o internal/targetprompt </dev/null
```

After changing `design/` or `ui/checkpoint/src/`, rebuild (this also writes
the views page script's digest to
`internal/controlplane/views_page_script_gen.go`) and check consistency:

```sh
node ui/checkpoint/build.mjs </dev/null
node ui/checkpoint/build.mjs --check </dev/null
```

Examples under `examples/` are separate Go modules with their own design and
generated code. Run each affected module's pinned generator from its directory
using its design import path. Repair contracts through design/generation,
never generated edits, casts, duplicate transport types, or handwritten schema
walks. Keep raw schemas/payloads raw until their owning generated codec needs
them. Service/method descriptions explain domain role, calling flow, and result.

In `ui/checkpoint/`, reuse existing components, tokens, and generated client/
types. Use explicit types without `any`, casts, augmentation, duplicate enums,
or handwritten parallel transports; narrow untyped external `unknown`
immediately. Preserve the visual language for scoped fixes. Use one clear primary
action, visible state, expandable ID/digest details, semantic keyboard-accessible
controls, and actionable empty/loading/error states. Never lose unsaved edits
on conflict or refresh. Check responsive behavior, focus, and long/error
content; meet WCAG AA and Core Web Vitals budgets, and keep state/hook
ownership small and explicit.

Use OpenTelemetry spans for meaningful application operations, with parent/
child links, bounded attributes, and events for state/results. Actual failures
record both error and error status; expected negative states do not. Use spans
instead of app logs/custom metrics where they carry the evidence, and query
spans for app alerts. Never suppress truthful spans; fix alert predicates or
resource lifecycles. Missing telemetry is unknown; periodic observed-state
spans can establish continuity. CPU, memory, and restarts use infrastructure
metrics.

## 5. Verification

Choose the smallest checks exercising the changed contract, then applicable
module/CI checks at the integration gate. The CI workflow is the command
reference; locally, use its partitions and time limits rather than a broader
or narrower aggregate. From the root, use applicable checks below; run
example-module commands in their named directories. Close stdin on all
invocations.

| Changed area | Check |
| --- | --- |
| Root Go module | `bash .github/scripts/go-test.sh -count=1 -- ./...` and `go run ./.github/scripts/racecheck` |
| Portable Story language | `bash ../.github/scripts/go-test.sh -vet=all -- ./...` from `story` |
| Verification flow | `go test -count=1 -tags e2e ./internal/verify` |
| `surla gen` / `surla init` generation | `go test -race -count=1 -tags e2e ./cmd/surla` |
| `examples/orders` | `bash ../../.github/scripts/go-test.sh -- ./...` from `examples/orders` |
| `examples/orders` or `examples/orders-eval` | `go test -race -tags e2e ./acceptance` from `examples/orders-eval` |
| Starter generation | `go test -race -count=1 -timeout 60m -tags scaffold ./internal/scaffold` |
| Project server with Herdr (opt-in) | `go test -race -count=1 -tags herdr -run TestHC ./cmd/surla` |
| Views page policy, session, or layout (opt-in) | `go test -race -count=1 -tags browser ./internal/project` |
| Views page TypeScript | `npm --prefix ui/checkpoint run typecheck` |

`go-test.sh` applies the race detector to the packages in
`.github/race-packages.txt`, which own production concurrency; `racecheck`
fails when an unlisted package gains goroutines, channels, or `sync`. The
Herdr test needs `hc` and skips without it. The browser test needs Node 22
or newer and Chrome or Chromium (set `SURLA_TEST_BROWSER` when it is not in
a standard location).

Each contract has one owning test, at the cheapest layer that can observe
it. Before adding a test, find that owner and extend it; answer a reviewer's
request for proof with the owning test when one exists. Real compilation and
code generation belong in their owners (`verify/candidate`,
`internal/designeval`, `internal/testwriter`, `internal/scaffold`) and the
named tagged journeys. Control-plane, project, page, and CLI tests start from
saved fixtures or snapshots, or the control plane's build-copy seam, unless
their own contract is that compilation. A forwarding layer (`verifyrun`,
`controlplane`, `cmd/surla`) tests one passing and one refused case, not its
owner's table. Every declared refusal has a test that triggers it.

Tests share expensive immutable inputs and isolate mutable state:
`t.TempDir()` holds scratch and outputs, while compiled packages come from
the job's shared cache (`SURLA_TEST_BUILD_CACHE`, or the project build
cache). A fresh cache belongs only in tests about the cache. Each CI job runs
an explicit set of tests that no other job runs; a build tag adds files and
does not select tests, so a tagged job names its tests with `-run`. An opt-in
suite needs a scheduled run, or it rots.

Pull-request checks finish within five minutes of a push, queue included.
A new test that compiles, starts processes, or waits states its cost and why
a cheaper layer cannot prove its contract. When a package or job nears its
time limit, fix the slow tests at their owner; a raised limit is a disclosed
stopgap with a follow-up, never the fix.

Use fast deterministic table-driven `testify/assert` tests, with `require`
only when continuation is impossible. Tests write inside `t.TempDir()`, never
the source tree. Use bounded concurrency and construct expensive immutable
fixtures once per package; do not turn functional tests into
GC/memory/timing sweeps. Update affected Go/TypeScript tests and fixtures
when contracts change; do not add dummy values or rewrite assertions merely
to match new output. Diagnose model evaluations from actual visible calls and
results before changing prompts, tools, or assertions.
Trace incomplete/failed/timed-out claims to their producer and raw stop
condition, including child calls. Test code-owned invariants deterministically
and model judgment with focused evaluations and valid counterexamples; call
ordering/count alone is not correctness. Never replace semantic judgment with
keyword rules or tailor assertions merely to make one evaluation pass.

For text-only instructions/docs, check accuracy, references, links, and
applicable doc checks; do not regenerate or run runtime/browser suites.
Required generated-consistency and other CI still apply to the final PR head.
Reuse completed evidence until changed inputs, incomplete results, or failure
justify another run. Disclose skipped/setup-blocked cases and model settings;
never call a mocked or credential-blocked path verified end to end, or change
credentials/access policy to make an evaluation run. That rule limits what a
result may claim; it does not move fixture-level contracts into end-to-end
tests.

## 6. Documentation and repository safety

Update affected behavior documentation in the same change: `README.md`,
`docs/HOW_IT_WORKS.md`, `docs/REFERENCE.md`, `docs/ARCHITECTURE_PLAYBOOK.md`, and
affected `examples/*/README.md`. Link authoritative contracts instead of
copying them. Preserve anchors, distinguish proposals from current behavior,
and keep current docs free of incident/PR/migration diaries. Write active,
connected plain English: input, owner action, visible result/error; define
terms and add examples where labels hide decisions. Put dense field/type/key
references in tables or examples. Keep customer guidance separate from
internal engineering/operations details and read changed pages end to end.

Use `author-pull-request` when available. A PR explains the affected feature,
failure, consequence, resulting behavior, and why the change solves it before
implementation details. Make title/body understandable without the chat or
diff, including follow-ups. Distinguish test/live examples and include material
compatibility, migration, deployment, rollback, and verification gaps; omit
routine file/command lists. Release notes describe shipped changes since the
prior release and required upgrade actions, explicitly stating when none are
needed. Do not announce unfinished behavior.

Keep the primary checkout clean on `origin/main`. Unless the user explicitly
asks for in-place edits, use an isolated clone under `~/src` and a feature
branch from `origin/main`, with a PR by default. One writer per tree; read-only
reviewers may inspect it. Never switch/reset/restore/clean/stash/rewrite refs
or indexes in the primary. Stop on unexpected modifications and preserve
unrelated work. Stage explicit paths only, never `git add .`/`-u`, discard
work, or force-push. Check `git diff --stat origin/main...HEAD` before pushes,
PR creation, and merges. Read both PRs' context before resolving conflicts;
preserve compatible intentions rather than choose ours/theirs wholesale.

Use locally authenticated `gh` for GitHub writes. Preserve contributor
authorship and verify user authorship; no assistant/generated-by attribution
or model-prefixed branch names. Use body files for multiline descriptions
and quote shell data safely. Never repurpose `HOME`, `CODEX_HOME`, or shell
reserved parameters. Before deleting a completed clone, verify its exact
target, nested repositories, clean status, and no unpushed commits. Preserve
unrelated QA projects/captures. Fast-forward the clean primary after verified
merge/cleanup, without discarding local work. Never run `go clean -cache`.

Before destructive filesystem/database/cloud work, resolve the exact target
read-only, explain impact/recovery, and verify session authorization. Use the
smallest reset, preserving unrelated clusters/images/volumes/services. Before
production writes, migrations, rollout, or traffic changes, state the action,
evidence, stop condition, and rollback. Authorized read-only probes must be
bounded and credential-safe; they do not authorize repairs or policy changes.
Read active sandbox permissions, use supported escalation when required,
and report rejected/unavailable access without bypassing it. `kubectl` always
names `--context` and one resource kind per positional command. Explain the
need before adding a dependency.
