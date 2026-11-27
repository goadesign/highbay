# Highbay Agent Guide

Highbay is a Goa and Goa-AI software factory control plane that takes a feature
request through intent (CP1), architecture preview (CP2), live prompt drafts
(CP3), verification (CP4), and promotion checks (CP5). Reason top-down from the
user's goal, architectural boundaries, trust edges, and true resource limits—never
bottom-up from whichever file is open, adjacent local hooks, or historical test
shapes. Optimize for strong contracts, clear ownership, conceptual correctness,
and less code: the best code is deleted or never written, never at the expense
of correctness, elegance, exactness, robustness, performance, or security. Do
not add fallbacks, coercions, or defensive code that hides bugs.

These instructions are self-contained and apply throughout this repository;
nested `AGENTS.md` files specialize their directories. Explicit user
instructions govern the task within platform safety. Explain material conflicts
before editing and apply each review or design requirement once, without
duplicate approval gates.

This repository is public. Never publish private projects, customers, internal
services or repositories, credentials, or telemetry in any repository or GitHub
artifact.

## 1. Find the contract and active work

Use the relevant entry below rather than preloading every document:

| Question | Authoritative starting point |
| --- | --- |
| What is Highbay and how does a user start? | [README.md](README.md) |
| How does the user journey work? | [How it works](docs/HOW_IT_WORKS.md) |
| What do commands and contracts accept/return? | [Reference](docs/REFERENCE.md) |
| Which component owns a concern? | [Architecture playbook](docs/ARCHITECTURE_PLAYBOOK.md), then owning source |
| Which built-in product rules govern a design? | [Golden Path](internal/goldenpath/GOLDEN_PATH.md) |
| Which checks apply? | [CI workflow](.github/workflows/ci.yml) and Section 5 below |

For sustained team work, the channel pin points to one long-term work index
outside the pin's size limit. The index links requirements, interaction
specifications, engineering designs, current milestones, and verification
evidence; each subject has one authoritative home. Keep the pin a short
lead/ownership/next-action summary rather than a competing plan. Update the
work index at milestone transitions, scope/decision changes, or real blockers
(in place, without diaries, and without making tracker edits a prerequisite for
ready work). Shared docs must not contain machine-local paths or private
evidence.

### Plain-English contract (replies, design gates, comments, docs, PRs, release notes)

- Always state the concrete caller/input, owner action (downstream calls and
  state changes), and observable/returned result (and error meaning) before any
  category label or abstraction.
- Define domain terms (`Change Card`, `request_revision`, `holdout`, `run
  directory`) on first use. For example, `get_change` reads the Change Card for
  the caller's `change_id` and returns it only to its creator.
- Never rely on unexplained session shorthand or metaphors (`CAS`, `sidecar`,
  `fence`, `receipt`, `aggregate`, `shell`, `hydration`, `projection`, `bridge`,
  `canonical path`, `provider boundary`). For example, write *"a revision check
  ensures only the current Change Card revision is updated"* instead of *"CAS
  fences card writes."* Include a concrete example whenever a label could hide
  accepted input, a decision, or changed output.

### Work, necessity, pre-edit gate, and adversarial review

- **Deliverable and communication:** Identify the outcome, completion
  conditions, unresolved decisions, and next step. Explain outcome, steps, and
  risks before changing three or more files. Answer questions visibly before
  mutations; then continue authorized work through implementation, verification,
  and authorized publication. Ask only for missing facts, material user
  decisions (with a concrete example, explicit Pros/Cons, and a recommendation),
  or required permission; never re-ask settled choices. A blocked activity
  pauses only that activity—unblock teammates first and pursue independent
  ready work.
- **Read the owner:** Source, schemas, and contracts establish implementation
  truth; summaries, trackers, and peer notes only locate work. Distinguish
  positive product requirements from negative rejection fixtures and accidental
  validation loopholes.
- **Five automatic-"No" symptom-management patterns & Hypothesis 0:** Treat
  every added or retained state, copy, hop, wrapper, serialization step, size
  cap, cache, coordination step, retry, or setting as a reason to recheck the
  premise. Automatically reject:
  1. Eviction/LRU/TTL/numeric caps on retained in-memory state,
  2. In-process caches or overflow stashes,
  3. Copy/clone helpers or duplicate decoded representations alongside raw bytes,
  4. Intermediate filters or suppressors on truthful spans, errors, or payloads
     (when a downstream consumer or alert misfires on truthful data $x$, fix the
     consumer predicate or resource lifecycle rather than distorting $x$),
  5. Repeated internal checks, wrappers, or mode flags inside an already
     validated flow.
  First enumerate every caller across `origin/main` and test **Hypothesis 0
  (deleting the offending state, copy helper, hop, or scan outright)**. Map any
  surviving field, record, dependency, or limit to the exact physical resource
  or decision it protects at its narrowest invariant lifetime, and test a valid
  counterexample at the next wider lifetime.
- **Proportionate pre-edit gate (`MECHANICAL` vs. `DESIGN-TOUCHING`):** Fix
  mechanical corrections (formatting, ordering, tests of established behavior,
  factual doc drift) directly. For bounded local changes, state before/after
  behavior, owner, affected callers, and focused proof. Before changing
  ownership, Goa schemas/public contracts, auth/trust, persistence, lifecycle,
  or external-system semantics, read the owning contract and all `origin/main`
  callers and state concisely before editing:
  1. **Workflow & caller inventory:** Actor, input, owner action, visible
     result, and every `origin/main` caller across ingress, storage, and UI/CLI.
  2. **Before/after contract & strength:** What becomes required, derived,
     rejected, removed, or preserved (optional fields, dual modes,
     presence-dependent semantics, and wider enums weaken contracts; two honest
     methods beat one method with modes).
  3. **Owning layer & Hypothesis 0:** Best from-scratch design across `origin/main`
     flows, concrete proof of why deletion or the existing owner cannot serve
     the required case, and domain-only API boundaries (APIs expose domain
     resources, capabilities, state, and stable IDs; UI code owns layout).
  4. **Journey, compatibility, and proof:** First-use, replay/iteration,
     recovery, migration/cutover (with explicit removal conditions for any
     temporary bridge), and verification checks.
- **Zero-producer vertical-slice cleanup & helpers:** Whenever removing or
  replacing a producer (tool, event, enum value, route, or workflow branch),
  search repo-wide across state, RPCs, routes, UI, CLI, skills, fixtures, and
  docs, and delete every branch with no remaining live producer plus superseded
  helpers in the same change. Helpers must enforce an invariant, own a boundary
  contract, remove meaningful duplication, or add domain language—never wrap a
  lookup, field access, literal, or pass-through call.
- **Zero-sunk-cost adversarial review:** Every non-trivial change requires one
  non-author peer's explicit approval before final publication/merge (changed
  code never inherits prior approval). Design-touching changes start with a
  fresh read-only reviewer given only the original outcome, owning `origin/main`
  contracts/callers, and diff without author defenses, starting from *"Is this
  the simplest, most elegant from-scratch approach given all `origin/main`
  flows? Assume No until proven."* When a review identifies high-priority or
  design-touching defects, apply the fixes and automatically run a follow-up
  read-only review pass until zero high-priority or design-touching findings
  remain. Block on concrete correctness, security, compatibility, or outcome
  defects; resolve or explain every comment and state any uncovered files.
- **Direct, non-interactive execution & bounded captures:** Prefer direct tools
  and `rg`; batch independent reads and keep mutations sequential. Close stdin
  (`</dev/null`) and pass non-interactive flags to CLIs and test runners. Bound
  background waits with explicit completion/failure/timeout exits and verified
  wake-ups. Privately capture costly/transient diagnostics with bounded
  credential-free output and metadata so readers can be fixed without rerunning.

## 2. Preserve Highbay's user journey

- Establish the user, problem, value, built-in rules, and scope from
  [README.md](README.md), [Golden Path](internal/goldenpath/GOLDEN_PATH.md), and
  user decisions; ask only about genuinely unknown product purpose or users.
- An LLM drives the full CP1–CP5 lifecycle through MCP with one running server,
  while the user reviews, converses with the team, and approves checkpoints in
  the `/views` browser workspace. Derive known paths, targets, and inputs from
  the bound workspace and credentials—never require manual file/state uploads
  the server can read, phase-specific restarts, or CLI escape steps. Each
  unavailable capability names the step that enables it.
- Preserve in-place refinement, replays, and backward loops on the same Change
  Card identity, stating what stays valid and what requires reconfirmation;
  never silently reuse stale decisions. Managed `hc` terminals and user-attached
  agent sessions share the same MCP/Chorus execution boundary. Developers build
  and test working-tree changes before committing.
- Synthesize shared journey flaws before splitting usability reports into
  patches. Verify onboarding or multistep changes end to end or with
  `highbay-ux-review` once as journey evidence rather than repeating full
  walkthroughs in every package test. Keep examples and starters focused on
  CP1–CP5 through the real CLI/MCP entrypoints.

## 3. Ownership, Goa contracts, and Goa-AI runtime rules

- **Storage and process ownership:** `internal/controlplane` owns Change Card
  files. `internal/verifyrun` owns verification run directories (shared only
  between the `highbay` project server and its `highbay verify-run` child processes as
  one logical service). Distinct services never share files, tables, keys, or
  storage packages; query the owner through its generated typed API rather than
  copying running facts into configuration.
- **Server-owned completion & promotion:** The project server binds
  `PromotionTarget` from settings stored with `highbay promotion set`; Highbay
  never fabricates promotion inputs or accepts caller-claimed digests. Accepted
  verification and check operations run in server-owned processes and complete
  independently of client disconnects (correctness never depends on client
  retries except fresh client-held credentials).
- **Candidate execution:** Build and start agent-written candidate binaries only
  through `verify/candidate` (digest-pinned, explicit environment, isolated
  state directory) inside a supervised process group owned and stopped by the
  operation. These are ordinary OS processes with the user's file and network
  access; never describe them as sandboxed or confined. Builder-facing output
  always goes through `Report.ForBuilders()`, never exposing holdout scenario
  names, checks, or logs to coding agents.
- **Service-lifetime loops, fault isolation, and replay keys:** Long-lived
  supervision and observation loops (`hc` event subscriptions, Chorus watchers)
  retry transient downstream failures for the server's lifetime rather than
  stopping after finite attempts, while isolating per-operation errors onto
  OpenTelemetry spans without crashing sibling work. Separate episode identity
  from payload-strict `IdempotencyKey`s, pair durable start operations with
  stop/cancel operations, and scale coupled heartbeat/staleness intervals
  together in tests.
- **Goa design & generated clients:**
  - Treat `design/` as the single source of truth for payloads, results,
    structural validation (`Required`, `Enum`, `Format`, `Pattern`, `Minimum`,
    `Maximum`, `MinLength`, `MaxLength`), transport mappings, and OpenAPI. Avoid
    `Any`. Validate and authorize once at entry boundaries; trust constructed
    invariants internally and fail fast without nil/empty fallbacks. Normalize
    whitespace only at external edges where insignificant.
  - Every `Service`, `Method`, `Type`, `ResultType`, and `Attribute`/`Field`
    carries a domain `Description(...)` stating its role, calling flow, and
    result. Model `Error(...)` at the narrowest Goa scope (method vs. service
    vs. API—never list method-specific errors at service scope merely for
    constructor convenience).
  - Required arrays accept `[]` unless `MinLength(1)` forbids empty; never
    encode domain meaning via `nil` vs. `[]`, and require exactly one active
    variant on `OneOf` sum types. Expected absence is a typed value; reserve
    `not_found` for required resources that failed to load. When defining gRPC
    messages, use unique literal `Field(n, ...)` tags across full `Extend`
    chains and never renumber or reuse released tags.
  - Call downstream Goa services (such as Chorus and CP3 target prompt servers)
    through their generated Goa HTTP/gRPC clients behind narrow caller-owned
    interfaces—never handwritten `http.NewRequest`, URL concatenation, or
    duplicate JSON structs.
- **Codegen-time specialization (partial evaluation):** In generators
  (`internal/scaffold`, `ui/checkpoint/build.mjs`, `story`, and Goa/Goa-AI
  templates), decide every static branch, name, type ref (`NameScope`), import,
  and metadata artifact at generation time. Emit only the selected code—never
  emit runtime type reflection, JSON Schema walks, string-concatenated type
  refs, or generator mode flags for facts known during generation.
- **Goa-AI agent, tool, prompt, and budget contracts:**
  - **Mechanical vs. semantic ownership:** Code, schemas, and runtime own
    derivable facts (caller identity, auth scope, membership, correlation,
    cursors, pagination, valid transitions); models own semantic judgment
    (intent, ambiguity framing, tradeoffs). Never ask the model to repeat
    ambient context or opaque handles/proofs code can correlate, never pick a
    heuristic "latest" result across concurrent calls, and never repair invalid
    model arguments with heuristics—reject them at the schema/codec boundary.
  - **Runtime & schema invariants:** Keep tool schemas, projections, and
    examples as `rawjson.Message` / `tools.RawJSON` (never `map[string]any`
    outside provider SDK adapters), and consume generated `tool_specs` codecs
    and `tools.TypeSpec.Fields` rather than parsing JSON Schema or `docs.json`.
    Read `RunID`, `SessionID`, `TurnID`, and `ToolCallID` from `ToolCallMeta`
    (never `context.Context`). Use `Inject` only for required string context
    fixed at run start. Register all toolsets and agents before calling
    `rt.Seal(ctx)`, run agent-as-tool via `agenttools.NewRegistration`
    (`ExecuteAgentChild`), and choose exactly one streaming path
    (`PlannerModelClient` vs. `ModelClient` + `planner.ConsumeStream`).
  - **Prompts & tool descriptions:** Specialize templates at render/codegen
    time; include only facts that change valid next actions, arguments, or stop
    conditions, without internal orchestration jargon or references to invisible
    tools. Tool descriptions state only that tool's contract and returned IDs
    needed next; caller prompts own cross-tool routing.
  - **Physical size budgets & `BoundedResult`:** Match limits to their physical
    boundary (model-visible tool result budget vs. `1 MiB` durable workflow
    record vs. `4 MiB` gRPC frame; never choke internal RPCs at a model-output
    cap). Bound expensive reads *before* fetching, separate total counts from
    detail pages, and use `BoundedResult` + `ContinueWith` so code owns cursors
    and each continuation either advances or reports its specific blocker
    (partial/empty windows never imply zero matches or full coverage).

## 4. Go, TypeScript, Observability, and Generated Artifacts

- **Go style & structure:** Use the module's `go.mod` toolchain and format with
  `go fmt ./...`. Group stdlib imports separately from external imports and
  prefix every generated-package alias with `gen` (`genhighbay`,
  `genhighbayhttp`, `genmcphighbay`). Use short lowercase package names,
  `lower_snake_case.go` files (new files <=1000 lines; do not move unchanged
  code in scoped fixes), `any`, `%w` with `errors.Is`/`errors.As`, `len(x) == 0`
  without nil checks, `min`/`max`/`clear`, multiline blocks, trailing-comma
  literals, combined adjacent same-type parameters, and signatures <=100
  columns. Never discard errors with `_ =`.
- **Declaration order & composition roots:** Order declarations top-down as
  `types`, `consts`, `vars`, exported functions, exported methods, unexported
  functions, unexported methods, with entry-point logic before helpers and named
  helpers for concurrency. Command entrypoints (`cmd/highbay`) parse flags/env,
  construct downstream clients, and inject interfaces; runtime packages never
  read env/flags or build external clients from raw addresses.
- **Comments:** Add plain-English header comments (Section 1) to packages,
  files, types, struct fields, constants/vars, functions, and methods, plus
  entry/phase comments on multistep flows. Exported GoDoc covers purpose,
  callers, invariants, ownership/lifetime, and error semantics. Re-read changed
  files end to end before finishing.
- **Never hand-edit generated files:**
  - `gen/`
  - `internal/targetprompt/gen/`
  - `ui/checkpoint/src/types.ts`
  - `internal/controlplane/checkpoint.html`
  - `internal/controlplane/views_page_script_gen.go`
  - `internal/goldenpath/decision_catalog.md`

  Regenerate with the module-pinned generator (closing stdin):

  ```sh
  go run goa.design/goa/v3/cmd/goa gen github.com/goadesign/highbay/design </dev/null
  go run goa.design/goa/v3/cmd/goa gen github.com/goadesign/highbay/design/targetprompt -o internal/targetprompt </dev/null
  node ui/checkpoint/build.mjs </dev/null
  node ui/checkpoint/build.mjs --check </dev/null
  ```

  Examples under `examples/` are separate Go modules; run each affected module's
  pinned generator from its own directory using its Go import path.
- **Browser UI (`ui/checkpoint/`):** Consume generated `types.ts` routes,
  request types, and closed validators directly—never use `any`, type casts,
  module augmentation, duplicate enums, or parallel handwritten `fetch`
  transports; narrow external `unknown` immediately. Reuse shared UI primitives
  and design tokens, give each view one clear primary action, preserve unsaved
  drafts across conflicts/refreshes, maintain semantic keyboard accessibility
  (including explicit focus escape from terminal widgets), and meet WCAG AA and
  Core Web Vitals budgets with small, explicit React state/hook ownership.
- **OpenTelemetry observability:** Emit spans per meaningful operation with
  parent/child links, bounded attributes, and state/result events. Record actual
  operation failures with both `span.RecordError(err)` and `codes.Error` status;
  never mark expected negative results (refusals, empty waits) as span errors,
  and never filter or suppress truthful spans. Use periodic observed-state spans
  for liveness continuity; reserve logs for startup, shutdown, and unrecoverable
  process errors.

## 5. Verification

Run the narrowest checks that prove the changed contract first, then the
applicable CI partitions from [`.github/workflows/ci.yml`](.github/workflows/ci.yml)
within the 5-minute PR budget. Close stdin (`</dev/null`) on all commands:

| Changed area | Check |
| --- | --- |
| Root Go module | `bash .github/scripts/go-test.sh -count=1 -- ./... </dev/null` and `go run ./.github/scripts/racecheck </dev/null` |
| Portable Story language | `(cd story && bash ../.github/scripts/go-test.sh -vet=all -- ./...) </dev/null` |
| Verification flow | `go test -count=1 -tags e2e ./internal/verify </dev/null` |
| `highbay gen` / `highbay init` generation | `go test -race -count=1 -tags e2e ./cmd/highbay </dev/null` |
| `examples/orders` | `(cd examples/orders && bash ../../.github/scripts/go-test.sh -- ./...) </dev/null` |
| `examples/orders` or `examples/orders-eval` | `(cd examples/orders-eval && go test -race -tags e2e ./acceptance) </dev/null` |
| Starter generation | `go test -race -count=1 -timeout 60m -tags scaffold ./internal/scaffold </dev/null` |
| `hc` integration (opt-in) | `go test -race -count=1 -tags herdr -run TestHC ./internal/hc ./cmd/highbay </dev/null` |
| Views page policy, session, or layout (opt-in) | `go test -race -count=1 -tags browser ./internal/project </dev/null` |
| Views page TypeScript | `npm --prefix ui/checkpoint run typecheck </dev/null` |

- **One owning test per contract:** Test each invariant once at the cheapest
  layer that observes it; forwarding layers (`verifyrun`, `controlplane`,
  `cmd/highbay`) test one passing and one refused case rather than duplicating
  their owner's table, and every declared error/refusal has a triggering test.
  Real compilers, containers, `hc`, and browsers run only in their owning
  packages and named tagged journeys; elsewhere use saved snapshots/fixtures.
- **Fast `-race` & shared immutable fixtures:** `go-test.sh` runs `-race` on
  packages in `.github/race-packages.txt`, enforced by `racecheck`. Share
  expensive immutable inputs (`sync.Once` fixtures and compiled packages).
  When `go env GOCACHE` reports an absolute path, `go-test.sh` uses it
  as the default for `HIGHBAY_TEST_BUILD_CACHE`.
  Isolate mutable scratch in `t.TempDir()` (never `sync.Pool` or GC/memory
  sweeps in functional tests). Use deterministic table-driven `testify/assert`
  tests (`require` only when continuation is impossible), update all affected
  callers/fixtures on contract changes, and test semantic model judgment with
  `goa-ai/eval` rather than keyword/regex grading. Disclose skipped or
  credential-blocked checks; never claim mocked paths as end-to-end proof.

## 6. Documentation, Positioning, and Repository Safety

- **Documentation & positioning:** Update affected docs in the same change:
  [README.md](README.md), [docs/HOW_IT_WORKS.md](docs/HOW_IT_WORKS.md),
  [docs/REFERENCE.md](docs/REFERENCE.md),
  [docs/ARCHITECTURE_PLAYBOOK.md](docs/ARCHITECTURE_PLAYBOOK.md), and affected
  `examples/*/README.md`. Put the **first successful user task before internal
  runtime details**, clearly distinguish *using coding agents* (Highbay, Chorus,
  `hc`) from *building product agents* (Goa-AI), ground capability claims in
  source and runnable examples, and never publish unmeasured productivity or
  token-savings numbers. Link one authoritative contract instead of copying it,
  preserve `#anchor` targets, exclude PR/migration diaries, keep user guides
  free of internal persistence/admin plumbing, and read changed pages end to
  end.
- **Pull requests & release notes:** Follow `author-pull-request` when
  available. Lead with the affected capability, concrete problem, why it
  matters, and before/after behavior for a reader unfamiliar with the chat or
  diff; cover ownership tradeoffs, compatibility/migration/rollback, and
  verification evidence without routine file/command boilerplate. Release notes
  cover shipped diffs since the prior tag grouped by user outcome and explicit
  upgrade actions (or state none are required). Never add AI co-author trailers,
  generated-by notices, or model-prefixed branch names (`codex/`, `claude/`,
  `gpt/`).
- **Git & operational safety:** Keep the primary checkout clean on
  `origin/main`. Use one isolated clone under `~/src` per writer on a feature
  branch from `origin/main` (never `/tmp`, never concurrent writers in one
  worktree). Never run `git clean`, `git stash`, `git reset`, `git checkout` (in
  primary), `git add .`, `git add -u`, force-push, or `go clean -cache`. Stage
  explicit paths only, inspect `git diff --stat origin/main...HEAD` before
  pushing/merging, and read both sides' context before resolving conflicts
  (preserving compatible outcomes rather than choosing ours/theirs wholesale).
  Explain new dependencies before adding them, and confirm destructive or
  external publishing actions first.
