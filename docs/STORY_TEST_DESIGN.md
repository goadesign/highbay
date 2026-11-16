# Story flows and the tests a build requires

This page is for contributors to Surla. It describes how a Story becomes the
tests a build requires, where the builder and the test writer work, how Surla
judges what the test writer reports, how verification chooses the tests that
judge the builder's commit, and what the user merges or drops afterwards.
[How Surla works](HOW_IT_WORKS.md) tells the same journey from the user's
side, and the reference gives the exact contract of
[each tool](REFERENCE.md#mcp-tools) and of
[builds and assignments](REFERENCE.md#builds-and-assignments).

## Terms

| Term | Meaning |
| --- | --- |
| Story | Go source, among the design files under `design/`, that describes what happens in the business: its actors and its flows. |
| Flow | One path through a Story, from what starts it to an outcome. For example, a customer asks for a refund above the limit, a manager approves it, and the refund is paid. |
| Finished flow | A flow with a start, at least one outcome and no open question. Any other flow is unfinished: a draft. |
| Change Card | The saved record of one requested change: the request, its open questions and the user's answers, its examples, its Story and its design source. |
| `request_revision` | The identifier of one exact saved revision of a Change Card, including its Story. |
| Agreed revision | The revision of a Change Card the user confirmed. A revision the guide proposes later waits beside it until the user adopts or declines it. |
| The server | The project's Surla server, started by `surla` in the project folder. |
| The guide | The coding agent the user talks to. It writes the Story in its draft and proposes changes. |
| The guide's draft | The folder `.surla-draft` in the project folder: the copy of the design that the guide edits. `refresh_story` returns its path as `draft_folder`. |
| The builder | The coding agent that writes the application code in its own working copy, `.surla-build`, and asks the server to verify a commit. |
| The test writer | The coding agent that writes one test per finished flow in its own working copy, `.surla-tests`. |
| Open build | The project's one saved build: a change at one agreed revision, with a number `n` and a base. A project has one open build at a time. |
| Base | The commit checked out in the project folder when Surla first saved the build numbered `n`. The build's two branches, `surla/build-<n>` for the builder and `surla/tests-<n>` for the test writer, start there. |
| Required test | The evaluation scenario Surla generates for one finished flow, with the hook method the test writer implements for it. |
| Holdout | The scenario tag that hides a scenario's details from the builder. Tests the builder can read are never hidden, so Surla refuses the tag in written tests. |
| Run directory | The folder in which one verification run keeps its request, its lock, its result and its logs. |
| Stored verification settings | A settings file stored for the project with `surla verification set`. It names one evaluator suite at one commit, and every run uses that suite instead of written tests. |
| Tests commit | The commit of the tests a run uses: the commit of the accepted tests on `surla/tests-<n>`, or the suite commit the stored verification settings name. |

## What a Story is

The Story language is the nested Go module `github.com/goadesign/surla/story`.
It depends only on Goa and the standard library, so a Story can be written
before any service, agent or architecture exists. Its
[README](../story/README.md) lists every declaration. A short example:

```go
var _ = Story("Refunds", func() {
	Actor("customer", "Customer")
	Actor("manager", "Support manager")

	Flow("refund-over-limit", "Refund above the limit", func() {
		StartsWith("request-refund", "customer", "Requests a refund")
		Step("approve-refund", "manager", "Approves the refund")
		Outcome("refund-paid", "customer", "Receives the refund")
	})
})
```

Every actor, flow, step and question has a stable key. Titles and summaries
can change without changing a key: renaming "Approves the refund" to
"Support manager approves the refund" keeps `approve-refund`, and the
required test of `refund-over-limit` keeps its name. A missing start, a
missing outcome or an open question leaves a valid unfinished flow. A
duplicate key, an invalid key or a step that names an undeclared actor is an
error. Actor names are the user's business roles: `warehouse-worker` and
`insurance-agent` are valid, and no keyword rule refuses one.

A project's design imports the language, so the project's `go.mod` must
require the Story module. A starter project made by `surla init` requires it
at the version Surla itself requires. It ships a first Story,
`design/story.go`, with one finished flow, `prices-cookie`, and the hook
method that flow requires, `PricesCookie` in `eval/acceptance`.

The guide changes the Story by editing Go source in its draft, never in the
project folder. The draft is the folder `.surla-draft` in the project folder,
with its own `go.mod`, `go.sum` and `design/`; `refresh_story` and
`update_draft` return its path as `draft_folder`. When the draft is missing,
`refresh_story` makes it first, from a design a Change Card holds or from the
project folder, and after the user drops a build it makes the draft again
from the project folder; the reference describes this under
[`refresh_story`](REFERENCE.md#refresh_story). Before drafting a change, and
when told that a design file changed in the project folder, the guide calls
[`update_draft`](REFERENCE.md#update_draft), which brings design changes made
outside Surla into the draft and lists files to correct, each with the folder
that holds it. The guide corrects a file in its draft; for a file in the project
folder, it tells the user what to correct and waits. It calls `update_draft`
again until no file is listed, then asks the server to evaluate the draft:

1. `refresh_story` reads the draft's `go.mod`, `go.sum` and the Go files
   under `design/`, never the rest of the project. It starts their evaluation
   in the background and returns at once with an `op_id`.
2. `get_story_refresh` returns the current evaluation: `none`, `running`,
   `succeeded` with the evaluated Stories, or `failed` with a diagnostic.
   The server keeps one evaluation per project in memory. A refresh returns
   the running or succeeded evaluation while the draft and the server's
   record of what the draft was made from are unchanged; a change to either,
   or a failed evaluation, starts a new one. After a restart the state
   is `none`. When a design file imports the Story language and the
   evaluated `go.mod` does not require its module, the diagnostic names the
   step: “`In the folder that holds that go.mod, run: go get
   github.com/goadesign/surla/story@<version>, then try again.`” The version is
   the one Surla's own `go.mod` requires. The guide runs the command in its
   draft, whose `go.mod` was evaluated.
3. The guide passes a succeeded `op_id` as `story_refresh_op_id` to
   `propose_change` or `revise_change`. The server reads no file then: it
   saves in the Change Card what that evaluation kept, including the
   evaluated Stories and the draft's `design/*.go` source. Both are part of
   its `request_revision`. An `op_id` whose refresh read the draft before the
   user dropped a build is refused with `drafted_before_drop`, and the guide
   refreshes again.

From then on, nothing reruns the current source to find out what was agreed:
a later edit, a helper outside `design/` or a dependency update cannot
change the Story an agreed revision holds.

The agreed design reaches the project's own `design/` folder through a
build. The builder's `generate_code` writes the build's saved design source
into the `design/` folder of the builder's working copy, the builder commits
it with its code, and the user merges that commit (see
[Merging, dropping and building again](#merging-dropping-and-building-again)).

## From a Story to the required tests

When the user builds a change, the server copies the agreed revision's Story
and design source into the build. The test writer's work is judged against
what the build saved, so a revision the guide proposes later, or one the user
adopts, does not change the tests this build requires. The user builds again
to require the tests of a later revision.

Each finished flow requires one test. Unfinished flows require none: they
stay in the saved Story, editable in the source, and do not stop the
finished flows from being built and checked. The test writer's assignment
lists every flow of the saved Story and one required test per finished flow.
The build also saves, from the same revision, each question with the answer
the user confirmed, the confirmation examples and the preserved behaviors,
and the assignment returns them: the test writer may not read the change,
and its tests must hold the service to them as well as to the flows. Surla's
check of a report confirms the required tests, not what the tests assert.

One rule, `mapStoryFlows` in
[`internal/designeval/eval.go`](../internal/designeval/eval.go), names each
required test from its flow's key, both in the Story table (below) and in
`StoryScenarios`, which lists the required tests and the unfinished flows the
table leaves out without generating anything:

| Flow key | Scenario name (Goa `codegen.SnakeCase`) | Hook method (Goa `codegen.Goify`) |
| --- | --- | --- |
| `refund-over-limit` | `refund_over_limit` | `RefundOverLimit` |

Two finished flows whose keys give the same scenario name or hook method,
such as `a-b` and `a_b`, or the same key in two Stories, are refused, naming
both flows. `start_build` checks this before it saves anything and refuses
with `unprocessable`; the guide then revises the Story. A finished flow whose
scenario name repeats a scenario the test module's design declares by hand
cannot be generated either: in a starter project, a flow `prices-tea` would
repeat the suite's own `prices_tea`, and generating the required tests fails
naming the duplicate.

The required tests are declared in the project's test module, the `eval/`
module that `surla init` creates in the project folder, by its Story table:
`eval/design/story_tests.go`, a source file of the module's own design.
`StoryTests`, in
[`internal/designeval/story_table.go`](../internal/designeval/story_table.go),
renders the table from the saved Story. The table declares the package the
module's other `design/*.go` files declare and registers a Goa evaluation
root, `storyScenarioRoot` from `story_scenarios.go`, with one row per
finished flow; every string in a row is a quoted Go literal. The table holds
those rows, not the Story, so the module's design never imports the
project's Story. The root runs after Goa-AI's evaluation suites root and
attaches one scenario per row to the module's single evaluation suite before
Goa validates it. Generating the module, which `Evaluator.Generate` runs,
then writes:

- `gen/evals/<suite>/suite.go`, whose `Hooks` interface gains one method per
  required test;
- `gen/cmd/<service>-evals/main.go`, the test program, which builds the suite
  from those hooks and the module's hand-written `acceptance` package.

No stub is written. Until the test writer writes a method for every required
test, the test program does not compile, and the compiler names each missing
method. A design that declares no suite, or more than one, is refused with
the fix; a table with no finished flow registers no row and still requires
exactly one suite. When a later revision drops or renames a flow, its method
leaves `Hooks`, and an old hand-written method still compiles and is unused.

Because the required tests are part of the module's design, every generation
of the module keeps them, whoever runs it and with any tool: Surla's own
writing of the required tests and its check of a report, `generate_code` with
`target: "eval"`, `surla gen`, `surla init`, and `goa gen` run by hand. A
module without a Story table declares no Story scenario. The test writer
leaves the table as it is: Surla generates the module with the table it
renders from the build's saved Story in place of whatever the test writer
left at that path, so the test writer's version never counts. Surla does this
when `get_test_writer_assignment` marks the assignment read while the tests
are still `assigned`, which is the test writer's first read after a build
start, and again at each report. A build start itself writes nothing into
the test writer's working copy. The table is replaced by renaming a new file
over it, so a symbolic link at its path is replaced, not followed.
`surla init` writes the starter's table from the starter's own Story. A
project whose operator stored verification settings for another suite keeps
whatever table its test module last received: no test writer has work there
and a report is refused, and the operator may delete the table.

<a id="the-test-writers-working-copy"></a>

## The working copies of a build

The builder and the test writer each work in their own linked Git working
copy (`git worktree`) of the project's repository, a folder inside the
project folder. [`internal/testwriter`](../internal/testwriter/copy.go) owns
both:

| Role | Folder | Branch for build `n` |
| --- | --- | --- |
| The builder | `.surla-build` | `surla/build-<n>` |
| The test writer | `.surla-tests` | `surla/tests-<n>` |

Both branches start at the build's base. The builder commits its code on its
branch. The test writer does not commit: Surla commits the tests it accepts
on `surla/tests-<n>`. A project with stored verification settings uses no
test writer, so a build there uses only `.surla-build`. Without stored
settings a build needs Surla's test layout, the `eval/` test module that
`surla init` makes; a project with neither is refused with `no_test_layout`.

### What a build start does

The user starts a build on the views page, which calls `start_build`. The
start:

1. **Takes the turn of both working copies**, without waiting (see
   [One operation at a time](#one-operation-at-a-time)).
2. **Checks, without any effect,** the change, the project's open build, the
   agreed design, the repository and each working copy's folder and branch.
   The refusals are listed below.
3. **Saves the build**, by one rename, with its number and base, the agreed
   revision's Story, design source and `go.mod`, and the answers, examples
   and preserved behaviors the user confirmed. A new build is numbered one
   more than the largest number already used, by the open build or by any
   `surla/build-<n>` or `surla/tests-<n>` branch in the repository, and its
   base is the commit checked out in the project folder. Building the open
   build's own change again, while that build is neither merged nor dropped,
   keeps its number and base and replaces its assignments.
4. **Makes a missing working copy** on the build's branch: `.surla-build`,
   and `.surla-tests` when a test writer is used. Making a copy first adds
   `.surla-build/` and `.surla-tests/` to the repository's own `info/exclude`
   file, never to a tracked file, so the project's `git status` stays clean.
   It checks the branch out when the branch exists and creates it at the
   build's base otherwise. A copy that is already there stays on whatever
   branch or commit it holds.
5. **Registers the builder and the test writer** in their copies, releases
   both turns, and brings up the team.

The checks, the save and the making of copies share a five-minute limit, and
bringing up the team has its own 45-second limit. A start moves no existing
copy and writes no test into one. After the save, a failure to make a copy
or to register a role, or a server stop, leaves the build saved: the result's
step is to build the change again, which makes a missing copy and registers
the roles.

A refused start saves nothing. A start is refused when:

- the change is not confirmed, the revision is not its agreed revision, or
  the agreed revision records no design (`conflict`);
- the agreed revision was drafted before the user dropped a build
  (`drafted_before_drop`);
- two finished flows would give their required tests the same name, or one
  gives an empty name (`unprocessable`);
- another change's build is open, neither merged nor dropped
  (`previous_change_open`), or this change's open build has no passed
  verification and the request's `replaces` does not name it
  (`unfinished_build_open`);
- a design file at the build's base is not a version the agreed revision was
  made from, because it was changed outside Surla (`design_changed_outside`);
- the project folder is in no Git repository (`not_repository`), the
  repository has no commit (`no_commit`), or Git has no name and address to
  commit with (`git_identity_missing`);
- the project has neither stored verification settings nor Surla's test
  layout (`no_test_layout`);
- `.surla-build` or `.surla-tests` exists and is not a linked working copy of
  the project's repository (`copy_folder_in_use`), or a copy is missing and
  another folder has its branch checked out (`tests_branch_in_use`);
- another operation holds a working copy's turn (`working_copy_busy`);
- the change is unknown (`not_found`), the server was not started by `surla`
  in a project folder (`not_implemented`), or the server stops or the
  five-minute limit ends before the save (`unavailable`).

The reference gives each refusal's recovery under
[`start_build`](REFERENCE.md#start_build).

### A worker's first read joins its copy to the build

Each copy joins the build's branch when its worker first reads its assignment
for that build, with `get_builder_assignment` or
`get_test_writer_assignment`:

- When the build's branch does not exist yet, Surla creates it at the build's
  base and switches the copy to it. A copy with nothing uncommitted joins
  from any branch. A copy with uncommitted work on an earlier build branch of
  its role (`surla/build-<k>` or `surla/tests-<k>`, and for the test writer
  also `surla/tests`, the one branch earlier versions of Surla used) first
  gets that work committed there, with the message
  `Left uncommitted when change <change_id> started`. A copy with uncommitted
  work on any other branch, or with a Git operation in progress, is refused
  with `copy_off_branch`.
- When the build's branch exists, a copy that is on it with no Git operation
  in progress is used as it is, and any other copy is refused with
  `copy_off_branch`.

A refused read marks nothing read and moves no copy. The refusal states the
copy's branch, the build's branch, the Git operation in progress and the
uncommitted paths, and the goal; the worker puts its work right with git and
reads again. When the copy's folder is missing, the read is refused with
`working_folder_missing`, and the user builds the change again, which makes
the folder again on the build's branch. Accepted tests are commits on
`surla/tests-<n>`, so a deleted copy loses only uncommitted work.

### Writing the required tests

The read of `get_test_writer_assignment` that marks the assignment read while
the tests are still `assigned` then writes the required tests into the copy,
within five minutes. It generates the module with the Story table, then
writes the table and, after removing any `.surla-gen-*` staging folder that a
write cut short by a server's death left in the test module, the generated
folder. A generation that fails writes neither, so the copy stays as it was;
only a symbolic link at the table's path is replaced by the table first. A
problem the test writer can fix, such as a test module design that does not
compile, makes the test writer's state `needs_changes` with that finding, at
report number 0. A problem it cannot fix makes the state `could_not_run`,
with a reason that says what failed: the time limit or a server stop, a
download of the module's dependencies that fails outside the test writer's
changes (decided as for a report, below), the generator and its phase, or
writing the generated folder into the copy. Any other cause has no text safe
to show, so its reason points at the server's trace. The user's step ends
with building the change again. Required tests that a read did not finish
writing are written again at the test writer's next report.

### One operation at a time

One operation at a time uses each working copy, and each copy has its own
turn. A build start takes both turns and does not wait: it is refused with
`working_copy_busy` while another start, a worker's assignment read, one of
the builder's tools or a check of a report holds one. The builder's tools
(`get_builder_assignment`, `generate_code`, `check_project`,
`update_dependencies` and `verify_change`) take the builder's turn, and
`get_test_writer_assignment` takes the test writer's; they are refused, too,
while their copy's turn is held. Only the check of a report waits for the
test writer's turn.

## How Surla judges a report

The test writer calls `report_tests_finished` with the change and the
build's revision. A report is refused when the user dropped the build
(`no_build`), when the test writer has not read the build's assignment
(`assignment_unread`), when its copy is missing, is not on `surla/tests-<n>`
or has a Git operation in progress (`working_folder_missing`,
`copy_off_branch`), when it names another revision (`conflict`), and when
the project has stored verification settings: `conflict` for a build started
with a test writer, `not_found` for a build started without one. The
reference lists the refusals under
[`report_tests_finished`](REFERENCE.md#report_tests_finished). Any other
report raises the build's report number by one, sets the state to `reported`,
clears any earlier verdict, and returns at once. One report checker per
project then judges it, without trusting the copy's author:

1. It waits for the turn of the test writer's working copy.
2. It checks that the copy is on `surla/tests-<n>` with no Git operation in
   progress, then writes the required tests into the copy again from the
   build's saved Story, after removing, as the test writer's first read does,
   any `.surla-gen-*` staging folder left in the test module. Only a folder
   that holds nothing but a generated `gen` folder is removed; a link, a file
   or any other folder with such a name is the author's and stays.
3. It copies the copy's files, as a commit would hold them, into a private
   folder, and compares them with the build's base, so the test writer's own
   commits on `surla/tests-<n>` count as changes too.
4. It generates the test module again in that private copy, with the Story
   table rendered from the saved Story in place of whatever the author left
   at that path, and writes the table and the generated folder there, so the
   author cannot change what is required.
5. It downloads the module's dependencies, builds the test program, and runs
   the program's `--describe` to list the scenarios it declares, each as an
   ordinary process with the user's file and network access.
6. It compares those scenarios with the required tests by the generator's own
   names.
7. Before it records an acceptance, it checks again that the copy is on
   `surla/tests-<n>`, and that the branch's tip descends from the build's
   base and holds no commit of another build's branch that the base lacks.

The whole judgment has a 15-minute limit. Within it, building the test
program has 10 minutes and listing its scenarios 60 seconds.

The download in step 5 runs with the user's proxy
settings, so its output is withheld, and Surla decides whose failure it is
from what it can compare. A download reads only the module's `go.mod` and
`go.sum`. When they are those of the build's base, the author changed nothing
that explains the failure. When the author changed them, Surla downloads the
base's own lists, read from the base commit: if those fail too,
downloading does not work where Surla runs. Both cases make the check
`could_not_run`, with a step that has the user run the download where its
output can be read. When the base's lists download and the author's do not,
the failure is the author's finding. The author cannot hide a defect of its
own lists behind this comparison, because the base's lists come from the
base commit, read through the project's repository, not from the copy. A
generation, in step 2 or step 4, that fails while loading the module's
dependencies is decided the same way.

The verdict replaces `reported`:

| Verdict | When | What is saved |
| --- | --- | --- |
| `accepted` | Every step passed. | The commit of the accepted tests and the time of the check. |
| `needs_changes` | A step failed for a cause the test writer can fix. | The findings, one problem per item, with no absolute path. |
| `could_not_run` | The check failed for a cause the test writer cannot fix. | The reason and the user's step. |

Findings name the first step that failed. The test writer's assignment and
`get_test_findings` show each finding's message; the names below only sort
the causes and are not shown:

| Finding | Cause |
| --- | --- |
| `working_copy` | The copy is not on `surla/tests-<n>` or has a Git operation in progress, or the branch's tip does not descend from the build's base or holds another build's work. The finding states where the copy is and the goal: the test files on the build's branch, uncommitted. |
| `outside_module` | A file outside the test module differs from the build's base. At most 20 are named, and one more finding counts the rest. Each names the base commit, and each named file's finding says what the copy must hold: the file's version at that commit, or no such file. |
| `generation` | The required tests could not be generated, for example because the module's design does not compile or declares no suite or several. |
| `dependencies` | `go mod download` failed for the author's changed `go.mod` or `go.sum` while the base's own lists download, or no comparison could be made. |
| `build` | The test program does not compile (the compiler names a missing hook method) or did not build within its limit. |
| `listing` | `--describe` failed, did not finish within its limit, or did not print exactly one suite declaration. |
| `hidden_tag` | A declared scenario is tagged `holdout`. |
| `missing_scenario` | The program does not declare a required test. The finding names the scenario, its flow and its hook. |

An accepted copy is committed on `surla/tests-<n>`, on the tip Surla checked,
with the user's own Git identity, hooks disabled and no signature. Its
message is `Add the tests Surla accepted for change <change_id>, report <r>`,
where `<r>` is the report's number. Only the
files that were built and listed are committed; an edit made after the check
stays in the copy as a change. When the branch already holds those files,
its commit is reused. Every read of an accepted verdict adds one fixed
sentence: "Surla checked that these tests build into the generated test
program and declare every required scenario; it did not judge what they
assert."

The verdict is saved only while the build still answers to the report that
was checked: the same change, revision, start and report number. A newer
report or a new build replaces it, and a dropped build takes no verdict:
nothing is saved then. A verdict marks the test writer's assignment unread,
and an acceptance also marks the builder's. `get_build` counts the findings,
the test writer reads their text in its assignment, and the user reads them
with `get_test_findings`.

When the server stops during a judgment, nothing is saved and the build
stays `reported`; the next server start judges every reported build. A
verdict that could not be saved, or a build that could not be read at a
start or before a judgment, is tried again a minute later, up to three times
in a row; then the build waits for the next report or server start.

This is a separation of duties, not isolation. Every agent runs as the user
on one machine, so another program of the user's can move a branch or read
a file. The check makes sure the tests the builder is judged by are the
tests Surla checked and recorded.

## How verification chooses its tests

The builder commits its code in `.surla-build`, on `surla/build-<n>`, and
calls `verify_change` with the change, its agreed revision and its candidate
commit: the commit of the builder's code to verify. It never names the tests.

Before any run, and for both test sources, the server checks the change, the
build and the candidate, and refuses when one of these does not hold:

| Check | Refusal |
| --- | --- |
| The change is confirmed, and the revision is its agreed revision. | `conflict` |
| No other operation is using the builder's working copy. | `conflict` |
| The change's build is the project's open build, not dropped, and of that revision. | `no_build` |
| The builder has read its assignment since the build started. | `assignment_unread` |
| The builder's copy exists and is on `surla/build-<n>` with no Git operation in progress. | `working_folder_missing`, `copy_off_branch` |
| The repository holds the candidate commit. | `conflict` |
| The candidate is on `surla/build-<n>`, descends from the build's base without being that base, and holds no commit of another build's branch that the base lacks. | `build_branch_mismatch` |
| The candidate's `design/` files are the build's saved design source, and the candidate shares history with the project's commit. | `conflict` |
| Every design file at the candidate's merge base with the project's commit is a version the revision was made from. | `design_changed_outside` |

Then one function, `testsChoice` in
[`verification_plan.go`](../internal/controlplane/verification_plan.go),
chooses the tests, for a run and for its [approval](#approval) alike, and
`verificationPlan` builds the run from that choice:

- With stored verification settings, every run uses the suite they name, at
  the commit they name. No test writer is used.
- Otherwise the build's tests must be accepted. The run builds the
  candidate's programs from the candidate commit and runs the test program
  generated at the tests commit: the commit of the accepted tests on
  `surla/tests-<n>`.

Without stored verification settings, the choice and the plan refuse too:

| Refusal | Cause | Next step |
| --- | --- | --- |
| `tests_not_accepted` | The build's tests are `assigned`, `reported`, `needs_changes` or `could_not_run`. | The message names the state and who acts: the test writer reports or fixes its findings, Surla is still checking a report, or the user takes the check's step. It ends with the builder's step: keep its Chorus watcher running, and read its assignment and verify again once Surla messages it that the tests are accepted. |
| `no_test_layout` | A commit lacks the layout `surla init` makes, the tests commit is not in the repository, or the project folder is in no Git repository. | The message names who acts: the builder commits the layout, the test writer reports again so Surla records its tests anew, or the user builds again or makes the folder a repository. |
| `conflict` | The candidate changes hand-written files of the test module or shares no history with the tests commit, or the project folder was replaced. | The builder restores the test module's files and commits; for no shared history, the user builds the change again; for a replaced folder, the user stops and starts Surla in the project folder. |

Each refusal says what Surla found and who acts. The reference lists every
refusal of [`verify_change`](REFERENCE.md#verify_change) with its step.

The server starts the run in its own run directory and returns at once. Each
run records where its tests came from, as `test_source`: `evaluator` for the
stored settings' suite, or `written_tests` with the tests commit and the
notice "These tests were written by a separate agent on this machine and are
not isolated from the builder." The views page, the page the server serves
to the user's browser, shows that notice with the run in its Proof view. A
test program the test writer wrote runs with only `PATH` and a private
`TMPDIR`, and no model key; it and the candidate's programs it starts run as
ordinary processes with the user's file and network access. A run keeps its
revision and its tests after the change is revised or the server restarts.

The change list says, for each change's latest run, whether it names the
agreed revision and the tests a run of that revision would use
(`inputs_current`): the accepted tests commit of the build of that revision,
or the suite the stored settings name. The reference gives the contract of
[`list_changes`](REFERENCE.md#list_changes).

A live target, a copy of the project that the server builds from a commit
and runs so the builder can try prompt wording, takes its tests from the same
choice. With stored verification settings, the evaluator they name starts
it, and it needs no build. Otherwise the change's current revision must have
a build with accepted tests, and a model must be chosen: the server builds
the candidate's programs from the candidate commit and starts the test
program of the accepted tests commit in its `--live` mode, so the builder's
own edits to the test module never run. The reference gives the contract of
[`start_live_target`](REFERENCE.md#start_live_target).

## Approval

The user approves a passed run on the views page. Approval requires, as
verification does, the change's open build, not dropped and of the agreed
revision, and takes its tests commit from the same choice as verification:
the stored settings' suite commit, or the commit of the tests accepted for
the revision. A run of other tests is refused, and the builder verifies
again. Approval also checks again, with git, that the run's candidate is on
`surla/build-<n>`, descends from the build's base and holds no other build's
work; otherwise it is refused with `build_branch_mismatch`, and the builder
verifies again. A saved approval holds only while those same tests stay
accepted for its revision: a new report or a new build hides it until that
commit is accepted again. The approval names the builder's commit and the
tests commit. Surla merges nothing: the next section says what the user
merges. Approval needs no promotion settings; when a project stores them,
approval also checks the release binaries, as
[`approve_merge`](REFERENCE.md#approve_merge) describes.

## Merging, dropping and building again

A project has one open build. `get_build` reports how it stands with the
branch checked out in the project folder, as `taking.state`:

| State | Meaning |
| --- | --- |
| `building` | No verification of this build has passed yet, so there is nothing to merge. |
| `passed` | A verification of this build passed, and the user has not merged it. The state names the newest passed run's commits and their branches. |
| `merged` | The branch checked out in the project folder holds the commits of one passed run: the builder's and, for written tests, the tests'. Another change can be built. |
| `dropped` | The user dropped the build. Another change can be built. |

A run counts for the build when it passed for the build's revision and the
server accepted it at or after the build's start.

**Merging.** The builder commits in `.surla-build`, so its commit is not on
the branch checked out in the project folder until the user merges it. While
the build is `passed`, the page shows the commands under "To take this
change, run in the project folder:": first `git merge <candidate_commit>`,
the builder's commit, verified on `surla/build-<n>`, then, for the test
writer's tests, `git merge <tests commit>`, verified on `surla/tests-<n>`.
With stored verification settings the tests are the evaluator's and are not
merged, so the page shows only the first command. Surla merges and deploys
nothing. The build reads `merged` once the branch holds the builder's commit
and, for the test writer's tests, the same run's tests commit: each as an
ancestor, or through a commit, such as a squash, that holds every file the
commit changed exactly as the commit has it. When the branch no longer holds
the build's base, because its history was rewritten, the `passed` state says
so (`base_in_branch` is `false`) and the page warns that the merges bring
that history back; to start from the branch as it is, the user drops the
build and builds the change again.

**Dropping.** The user drops the open build on the views page (**Drop…**,
then **Drop change**), which calls `drop_build` with the build the page
showed. Drop writes one record and changes no working copy, branch, draft or
file of the user's: the build's commits stay on its branches, and
`kept_branches` lists the branches that hold a commit made after the build's
base. From then on Surla builds nothing on that build, no role has work for
it, and the builder's and the test writer's tools are refused with
`no_build`. The guide's next `refresh_story` makes the draft again from the
project folder, so edits the guide has not proposed are lost. A revision
drafted before the drop is not built: `get_build` reports
`drafted_before_drop: true` and `start_build` refuses it with
`drafted_before_drop` until the guide refreshes the Story, revises the change
from that refresh, and the user adopts the revision. The reference gives the
contract of [`drop_build`](REFERENCE.md#drop_build).

**Building again.** What a new start does depends on the open build:

| The open build is | Building its own change again | Building another change |
| --- | --- | --- |
| `building` | Keeps the build's number and base and replaces both assignments. The page asks the user first and sends `replaces` naming the build it showed; without it the start is refused with `unfinished_build_open`. | Refused with `previous_change_open`. |
| `passed` | Keeps the build's number and base and replaces both assignments. | Refused with `previous_change_open`. |
| `merged` or `dropped` | Starts a new build, with a new number and the commit checked out in the project folder as its base. | Starts a new build the same way. |

A replaced assignment is unread. When a test writer is used, its tests are
`assigned` again at report number 0: they must be reported, checked and
accepted again before the builder can verify.

**Who has work.** `list_builder_assignments` and
`list_test_writer_assignments` return the open build's assignment only while
the build needs that role, and an empty list otherwise:

| Role | Has work on the open build |
| --- | --- |
| The test writer | While the build uses a test writer and its tests are not `accepted`. |
| The builder | Until a verification of the build has passed and still names the agreed revision and the tests selected now. |
| Neither | Once the user dropped the build. |

`get_team` reports `assignment` as `no_work` for a role the open build needs
nothing from, and Surla's reminder to read an assignment goes only to a role
that has work and an unread assignment.

## Where the code lives

| Concern | Source |
| --- | --- |
| Story language | [`story/`](../story/README.md) |
| The guide's draft, Story evaluation and adoption | [`draft.go`](../internal/controlplane/draft.go), [`update_draft.go`](../internal/controlplane/update_draft.go), [`story_refresh.go`](../internal/controlplane/story_refresh.go), [`designeval/eval.go`](../internal/designeval/eval.go) (`Evaluator.Evaluate`) |
| Naming and generating the required tests | [`designeval/eval.go`](../internal/designeval/eval.go) (`Evaluator.Generate`, `mapStoryFlows`), [`story_table.go`](../internal/designeval/story_table.go) (`StoryTests`), [`story_names.go`](../internal/designeval/story_names.go) (`StoryScenarios`), [`story_scenarios.go`](../internal/designeval/story_scenarios.go), [`test_layout.go`](../internal/designeval/test_layout.go) |
| Starting a build | [`build_start.go`](../internal/controlplane/build_start.go), [`build_start_copy.go`](../internal/controlplane/build_start_copy.go), [`builds.go`](../internal/controlplane/builds.go) |
| The working copies, their branches and joining a build | [`testwriter/copy.go`](../internal/testwriter/copy.go), [`testwriter/buildbranch.go`](../internal/testwriter/buildbranch.go), [`testwriter/join.go`](../internal/testwriter/join.go), [`workers.go`](../internal/controlplane/workers.go) |
| The check and the record | [`testwriter/check.go`](../internal/testwriter/check.go), [`testwriter/git.go`](../internal/testwriter/git.go) |
| Judging reports | [`report_check.go`](../internal/controlplane/report_check.go) |
| Choosing a run's tests | [`verification_plan.go`](../internal/controlplane/verification_plan.go), [`verify/program_env.go`](../internal/verify/program_env.go) |
| Running verification | [`verification.go`](../internal/controlplane/verification.go), [`verifyrun`](../internal/verifyrun/verifyrun.go) |
| Merged, dropped and who has work | [`taking.go`](../internal/controlplane/taking.go), [`drop_build.go`](../internal/controlplane/drop_build.go), [`builds.go`](../internal/controlplane/builds.go) |
