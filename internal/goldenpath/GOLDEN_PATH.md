# Golden Path

Rules for agents building services with Goa, Goa-AI, Model, Pulse and Clue. Rules under "Never" are hard invariants: if a request would break one, say which and why before acting. Every other rule is the default; an explicit task, or the contract of the service that owns the behavior, overrides it.

## 0. Loop

1. The guide changes the design in its draft, refreshes it and proposes a revision for the user to adopt. The builder reads its assignment and calls `generate_code` in its working copy: Highbay writes the agreed `design/` and regenerates `gen/`; the builder edits neither directory. Ask the guide about design changes. Implement in the hand-written code, then build and test with `check_project`. While a build runs with a test writer (without stored verification settings), `eval/` belongs to the test writer, which reports to regenerate it or runs `highbay gen -dir eval` from a shell; outside such a build, the builder can regenerate `eval/` with `generate_code` and `target: "eval"`. The writer leaves Highbay's Story table unchanged, runs every Go command with `GOWORK=off` and does not commit: Highbay commits accepted tests. An operator can regenerate a local module with `highbay gen -dir <module root>` from a shell, using that module's own dependency versions. Generation runs as an ordinary process with the user's file and network access.
2. Never edit `gen/`. A compiler error in `gen/` means the design or the application code is wrong.
3. `goa example` writes scaffolding once and never updates it. Port design changes into existing implementations by hand.
4. `highbay gen` refuses a design with gRPC transports, because it runs Goa's generator without protoc. Generate such a design with the `goa` command pinned to the module version, `go run goa.design/goa/v3/cmd/goa gen <module>/design`, never a separately installed `goa`.
5. After a signature change, regenerate, compile, and fix every caller the compiler reports. Then add behavior tests. Compiling proves types, not behavior.

## 1. Contract

- Describe every method once, independent of transport, then map it to the transports its callers need (`HTTP`, `GRPC`, `JSONRPC`). One method with one set of types serves all of them. Not every streaming shape is valid on every transport; `goa gen` rejects invalid combinations.
- Put structural validation in the design: `Required`, `MinLength`, `MaxLength`, `Pattern`, `Format`, `Minimum`, `Maximum`, `Enum`. Generated decoders enforce it. Never validate it again in handlers.
- Keep authorization and business rules in the service that owns them, not in the design.
- `Required` accepts `[]`. Add `MinLength(1)` to forbid an empty collection. Use `ArrayOfRequired` to forbid null elements.
- Never let nil and empty collections mean different operations. Model the operation explicitly.
- Mark a `OneOf` `Required` when the caller must choose a branch.
- Give gRPC fields stable, unique `Field(n, …)` numbers. Never renumber them.
- Give every list over unbounded data a bounded page: a default size, a maximum size, an opaque cursor, and a typed page result. Prefer cursors to offsets.
- Share a type with `Meta("struct:pkg:path", "<pkg>")`. Never write a second copy of a generated type.
- Adding a required result field breaks callers. Ship it with the code that always sets it. Never fix a broken contract by making the field optional.
- Removing an enum value, field or method breaks deployed callers. Migrate every caller first.
- URLs use plural resource nouns. The HTTP method names the action: `GET /orders`, never `/list-orders`.

## 2. Services

- Name systems and services after their domain capability. Use the same words in the design, code and docs.
- Edge services take external HTTP/SSE/MCP traffic, authenticate, start traces, and route. Domain services expose internal gRPC and own logic and state.
- Exactly one service owns each piece of durable state. Other services call its API and never read or write its tables, collections or keys.
- The service struct implements the generated interface. Pass every dependency into its constructor as an interface.
- Construct clients, connections and timeouts in `main` and pass them in. Runtime packages never take addresses or transport settings.
- Call downstream services through their generated clients, behind the narrow interface the caller needs. Add a wrapper package only when it owns behavior or translation.
- Set a timeout on every outbound call. Pass `ctx` through every call.
- When work spans more than one service or transaction, finish it on the server in a durable workflow. Never depend on a client to retry for correctness.

## 3. Errors

- Declare every error a caller must tell apart with `Error("descriptive_name", …)` and a `Description`.
- Use `ErrorResult` unless the error needs extra fields. Set `Temporary()`, `Timeout()` or `Fault()` only when that fact is true: a permanent business rejection is none of them.
- Map each error per transport: `Response("name", StatusX)` in `HTTP`, `Response("name", CodeX)` in `GRPC`.
- Return `ErrorResult` errors with the generated `Make<Name>(err)` helpers, and custom error types as generated values. Map dependency errors to designed errors in the service layer.
- Wrap with `%w` and inspect with `errors.Is` or `errors.As`. Never discard an error with `_`.
- Never put internal detail in an error response. Record it on the trace.

## 4. Security

- Declare `Security(...)` at the API level and mark public methods explicitly with `NoSecurity()`.
- Implement the generated `Auther` methods: verify the credential, call `scheme.Validate(scopes)`, and return a context carrying the principal.
- Authenticate and authorize once, where a request or workflow enters the system. Never re-check inside internal loops.
- Deny by default. Check each requested action against the owning service's policy for that resource. Ownership is the rule only where the domain says so; delegated and shared access can be valid.
- Use `JWTSecurity` only for JWTs. Use `BearerSecurity` for opaque tokens.
- Read secrets at runtime from a secret store and fail at startup if one is missing. Never log secrets, tokens or personal data.
- Never build a query from field names or operators the user controls.

## 5. Agents and tools

- Prefer binding a tool to the method that already holds its logic: `BindTo("Method")` or `BindTo("Service", "Method")`. Use a custom executor when there is no such method, for example to call an external MCP server or orchestrate several calls.
- Reuse service types in `Args` and `Return`. Describe when to use each tool, what it returns, and every field. Never maintain a second tool schema.
- Code supplies the authenticated caller, its scope and the run context: inject them with `Inject(...)` (required strings) or `BindField`/`BindInput` for structured values. The model may choose which authorized resource to act on, such as a target ID or a lookup key.
- In a hand-written executor, decode with the generated `Decode<Tool>(payload, meta, labels)`. The raw JSON codec leaves injected fields empty.
- Read the runtime's run, session, turn and call IDs from `ToolCallMeta`, never from `context.Context`.
- Mark large, paged or truncated results with `BoundedResult()`. The service sets `Returned` and `Truncated` on every success, and sets `Total` only when it is known.
- Keep `Return` to domain fields. Never add `returned`, `total`, `truncated` or `next_cursor` to it.
- Prefer `BoundedResult(func() { ContinueWith("continue_x", "cursor"); NextCursor("next_cursor") })`. The runtime holds the cursor and the model calls the continuation with `{}`.
- Keep domain facts in `Return`, including facts a UI also renders. Put auxiliary or presentation data the model does not need in `ServerData`. Exception: `ServerData` declared with `NativeImage()` reaches the model as an image.
- Declare `Confirmation(...)` on a tool when the domain requires a person to approve each call before it runs. Do not add it where the action is already authorized. Key the UI on the pending confirmation, never on a tool name.
- Report expected tool failures as a classified `Failure` on the tool result (`RecoveryCorrectCall`, `RecoveryReplan` or `RecoveryFinish`), so the planner can recover. Return ordinary errors for unexpected failures; the runtime classifies them. Never panic.
- Give every agent a `RunPolicy` with `DefaultCaps(MaxToolCalls(n), MaxRecoveryTurns(n))` and a `TimeBudget`, or a `Timing` block.
- Register every toolset and agent, then call `rt.Seal(ctx)` before the first run.
- Create the session in the host service before `Start`. Let the runtime own conversation history; keep no second copy.
- Decisions code can derive belong in code: the authenticated caller, authorization, runtime IDs, cursors, pagination, valid state transitions. Leave semantic choices to the model.
- Default acceptance check IDs to `check_01`, `check_02`, ... in `lower_snake_case` matching `goa-ai/eval` `validateID` so examples map directly to evaluator scenarios without identifier translation.
- Write Change Card fields (`summary`, `ambiguities`, `examples`, `preserved_behaviors`) in concise plain domain language for a requester who knows only their goal, omitting repository file paths (`design/design.go`), Highbay candidate runner flags (`-listen`), and Goa DSL symbols unless the request itself is about an API or CLI. A new project starts blank (a service with no methods and one test that the program starts); never mention or ask about its starting files on the card, and send `preserved_behaviors: []` until the user has confirmed real product behavior. When the project was created as Highbay's worked example (`init_project` with `worked_example: true`) and the request is for the user's own product, replace the example silently.
- A revision's `prompt` is the change's complete request as it now stands: every requirement that still applies, with additions, changes and removals applied, never only the difference. `revision_note` says what the revision changes and why.
- Account for every prior example when revising intent (`revise_change`): the analysis links surviving or edited checks via `prior_id` and lists dropped checks in `removed_example_ids`. Code preserves surviving IDs, prevents collision with removed IDs, and allocates brand-new checks to the lowest unused `check_NN` ID.
- The user answers each ambiguity question on the views page by choosing one of its declared options, then confirms the card with the page's Confirm action (`confirm_intent`), which a card without questions also needs; no agent confirms a card. When a domain decision requires a choice the options do not list, the guide calls `revise_change` with that guidance in `revision_note` and the complete request, restated with the decision, in `prompt`, and the user confirms the revised card.
- When the options of an ambiguity differ in what the design, Story, examples or preserved behaviors must be, the analysis names in `draft_option` the one option the draft already implements, exactly as written in `options`, and never inside an option's text. `confirm_intent` then accepts only that answer: for any other option the guide first calls `revise_change` so the draft implements the user's choice, and the user confirms the revised card. Omit `draft_option` only when every option works with the draft as it stands.

## 6. Durability

- Agent turns and multi-step work run as durable workflows. Activities retry, so every side effect is idempotent. Key it by a stable ID such as the tool call ID or a domain key.
- Pass downstream calls an idempotency key derived from the domain, for example `rollback/<org>/<deploy>`.
- Never retry a whole workflow. That repeats completed side effects.
- Each complete workflow-engine input or output, including every tool call, result, transcript entry and metadata it carries, must fit the engine's payload limit (1 MiB). Keep large domain data in application storage and return a typed reference.
- One host service owns the runtime store. Workers reach it through that service's typed API.
- Never set a Temporal `DataConverter`. The engine installs its own.
- Version workers with immutable build IDs. Keep old versions running until the executions pinned to them finish.
- Before changing a generated contract, check compatibility with running executions and saved suspensions. A continued run starts a new workflow, so a saved checkpoint must still decode, or needs its own cutover.

## 7. Events and streams

- Use events when several consumers need the same fact, or when the producer must not wait for consumers. Never use events for a step that must commit or fail atomically.
- Define event payloads as typed types in the design. Decide up front which events must never be lost.
- Choose each consumer's delivery guarantee explicitly. Work that must not be lost needs retained events and acknowledged consumption with redelivery; make those handlers idempotent and acknowledge after processing.
- Pulse readers give each reader every event from its own position, without acknowledgement; sinks spread events across consumers; pools run jobs and move them off failed nodes. Never trim a stream by count if trimming could delete unfinished work.
- In `goa.design/pulse/streaming`, create streams with `streaming.NewStream` and publish events with `stream.Add`: use `stream.NewReader` (`WithReaderTopic` or `WithReaderTopicPattern`) for fan-out and `stream.NewSink` (acknowledging with `Sink.Ack`, tuning `WithSinkAckGracePeriod`, and reserving `WithSinkNoAck` for disposable telemetry) for competing consumer groups. `streaming.NewStream` trims at 1,000 events by default, and `WithStreamMaxLen` changes that count; both apply Redis `XADD MAXLEN` without checking pending `Sink` acknowledgments. For a stream whose unfinished work must not be trimmed, pass `WithUnboundedStream()` and remove settled events explicitly.
- In `goa.design/pulse/pool`, join consistent-hash worker pools with `pool.AddNode` (`WithMaxQueuedJobs`, `WithWorkerTTL`, `WithAckGracePeriod`): edge or API producers also pass `pool.WithClientOnly()` and dispatch jobs via `node.DispatchJob(ctx, key, payload)`, while worker processes attach idempotent `pool.JobHandler` (`Start` / `Stop`) implementations via `node.AddWorker` (`node.Close` stops this node's workers and requeues their jobs; `node.Shutdown` stops workers across all nodes).
- In `goa.design/pulse/rmap`, join replicated state maps with `rmap.Join(ctx, name, rdb, opts...)` (`Set`, `Inc`, `TestAndSet`, `Delete`, `Subscribe`) for small, read-heavy cluster state such as feature flags and Goa-AI's `middleware.NewAdaptiveRateLimiter`. Never use `rmap` as primary entity storage.

## 8. Observability

- Instrument from the first commit: server middleware, client transports, span names, and consistent attributes. Configure OpenTelemetry exporters with `goa.design/clue/clue` (`clue.ConfigureOpenTelemetry(ctx, cfg)`) and stream trace propagation with `clue/interceptors`; Goa-AI's runtime emits `gen_ai.*` spans.
- Create a span for each meaningful operation. Record state changes and results as span events, not log lines.
- On every failure path, including early returns, record the error and set the span's error status. Never set error status for an expected negative result.
- Log only startup, shutdown, background work outside a request, and unrecoverable errors. Never log in hot paths.
- Readiness checks critical dependencies with timeouts. Liveness checks only the process. Never expose pprof publicly. In `goa.design/clue/health`, `health.NewChecker` takes `health.Pinger` values (pass `health.WithTimeout` to the built-in HTTP `health.NewPinger`, and supply custom `Pinger` implementations for Redis, databases, or Temporal); mount `clue/debug` (`MountDebugLogEnabler`, `MountPprofHandlers`) only on an internal port.
- Alert on queries over truthful spans. Fix the alert's query; never filter or rewrite spans to quiet an alert.

## 9. Tests

- Unit tests cover pure logic: table-driven and deterministic, with no network or database.
- Every method has a component test for its success path and each designed error, with generated mocks and asserted calls.
- Integration tests use the generated clients, create and clean up their own state, and run in any order.
- Test conversions over every input variant, and assert the output, not just that no error occurred.
- Test agents with the in-memory engine. Fake models through `model.Provider`.
- Declare evals in the design with `Suite`, `Scenario` and `Input`. Check exact facts in code; send only claims that need judgment to a model judge. Never grade with regexes or keyword lists.
- Generation proves types only. Test authorization, idempotency and compatibility with deployed callers yourself.

## 10. Architecture model

- Keep the C4 model in the design as code, with `goa.design/model/dsl` (`Person`, `SoftwareSystem`, `Container`, `Uses`, `Views`). `goa gen` renders it.
- Each service and external dependency appears in the model with its real connections and technology. Update the model in the same change as the design.

## 11. Change and rollout

- Keep each change small and focused on one concern, and deployable. Update the design, code, model and docs together.
- Ship a minimal deployable service first, with the generated interface, stubbed results and instrumentation, and keep it deployable at every step.
- Pin every version; never use `latest`. Promote through each environment in order. Have a rollback before deploying.
- A change is done only when it runs in every target environment, not when it merges.

## Never

- Edit `gen/`.
- Duplicate a contract: no second schema, no copied generated type, no second validation pass.
- Let the model supply the authenticated caller, its scope or credentials.
- Read the runtime's run, session, turn or call IDs from `context.Context`, or fall back silently to a default value.
- Share durable state between services.
- Depend on client retries for correctness.
- Hide failures: no discarded errors, no unrecorded error paths, no filtered telemetry.
- Repeat auth checks inside an already authorized flow.

## Reference

`examples/orders` follows this path:
- `design/` holds the API, the toolset with `Inject`, `BindTo`, `BoundedResult`, `ContinueWith` and `Confirmation`, and the C4 model.
- `internal/refundflow` makes one idempotent refund per order.
- `internal/runstore` keeps the runtime store, owned by one service.
