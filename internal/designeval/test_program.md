
---

## The test program Highbay builds

Highbay writes this test module's program, `{{ .Command }}`, each time it generates
the module, and builds only that program when it checks your tests or verifies a
change. The program builds the evaluation suite `{{ .Suite }}` from the generated
tests in `gen/evals/{{ .Suite }}` and from your package `acceptance`. Write only
that package; a `main` package of your own is never built. Every run declares the
environment `{{ .Environment }}`.

After you change `design/`, generate this module again. When a build runs with a
test writer (without stored verification settings), this module belongs to the
test writer, and Highbay regenerates its `gen/` at build start and on each report:
the test writer reports with `report_tests_finished` to have Highbay generate it
again or runs `highbay gen -dir eval` in its
working folder first. Outside such a build, the builder calls `generate_code`
with `target: "eval"`.
{{- if .StoryTests }}

This module's `design/story_tests.go` declares one scenario per finished flow of
the project's Story, each run by the `Hooks` method of the same name.
Regenerating this module keeps those scenarios. The file is generated from the
Story: do not edit it by hand.
{{- end }}

The program calls exactly these names in `acceptance`:

| Name | What it must be |
| --- | --- |
| `Name` | A string constant: the name the program under test is bound as, such as `{{ .Service }}`. |
| `Hooks` | A type whose pointer implements the generated `Hooks` interface, one method per scenario: `func (h *Hooks) <Scenario>(ctx context.Context) (eval.Result, error)`. A missing method does not compile. Listing the tests uses a zero `Hooks`, so it must not need the program under test. |
| `New` | `func New(binary candidate.Artifact) *Hooks`: hooks that run every scenario against the pinned program under test. |
| `Live` | `func Live(ctx context.Context, binary candidate.Artifact, key string, provider model.Provider) (suite.LiveTarget, error)`: starts the program under test for a live copy with a model provider. |
| `LiveThroughGateway` | `func LiveThroughGateway(ctx context.Context, binary candidate.Artifact, key, gatewayAddr string) (suite.LiveTarget, error)`: the same, through a model gateway. |

The test writer reports `coverage_statements` with `report_tests_finished`: When you call report_tests_finished, include coverage_statements. For each scenario whose coverage you state, give its exact scenario ID, question_ids from this assignment's answers[].id, example_ids from examples[].id, and an explanation of the interaction or assertion that exercises them. Use an empty array when you state none; Highbay will show Not stated. These are your statements, not Highbay's judgment of what your tests assert. The tests must still hold the service to every confirmed answer, example and preserved behavior. Highbay validates supplied references while checking your report. Read get_test_writer_assignment for the verdict and the statements you submitted; correct any stated-reference findings and report again. Do not commit: Highbay records the tests it accepts.
