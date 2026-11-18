// This file declares the test writer's statements, distinct from verified outcomes.
package design

import . "goa.design/goa/v3/dsl"

// TestCoverageReference names a scenario, confirmed question or example exactly.
var TestCoverageReference = Type("TestCoverageReference", String, func() {
	MinLength(1)
})

// TestCoverageStatement is the writer's account, never Surla's assertion judgment.
var TestCoverageStatement = Type("TestCoverageStatement", func() {
	Description("The test writer's statement that one declared scenario exercises particular agreed answers or examples. Surla validates references, not whether the tests assert the stated behavior.")
	Attribute("scenario", TestCoverageReference, "Exact declared scenario identifier.")
	Attribute("question_ids", ArrayOfRequired(TestCoverageReference), "Question IDs from the assignment's answers[].id; empty when this statement names only examples.")
	Attribute("example_ids", ArrayOfRequired(TestCoverageReference), "Example IDs from the assignment's examples[].id; empty when this statement names only answers.")
	Attribute("explanation", String, "Interaction or assertion the writer says exercises these requirements.", func() { MinLength(1) })
	Required("scenario", "question_ids", "example_ids", "explanation")
})

// RecordedTestCoverage preserves one accepted report's basis for run review.
var RecordedTestCoverage = Type("RecordedTestCoverage", func() {
	Description("The accepted writer report's statements and built answers/examples, copied with the exact tests selection for this run. Later reports and revisions cannot replace this evidence.")
	Attribute("report", Int, "Accepted test-writer report number.", func() { Minimum(1) })
	Attribute("answers", ArrayOfRequired(ConfirmedAnswer), "Answers saved for the built agreement, in question order.")
	Attribute("examples", ArrayOfRequired(ExampleItem), "Examples saved for the built agreement, in confirmation order.")
	Attribute("coverage_statements", ArrayOfRequired(TestCoverageStatement), "Writer statements for this report; empty means Not stated.")
	Required("report", "answers", "examples", "coverage_statements")
})

// TestCoverageNotRecorded preserves missing old evidence and operator ownership.
var TestCoverageNotRecorded = Type("TestCoverageNotRecorded", func() {
	Description("No test-writer coverage snapshot was recorded. Never infer it from today's agreement or accepted report.")
})
