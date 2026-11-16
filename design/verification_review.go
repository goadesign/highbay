// This file declares the user's read of a saved run before promotion approval.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// RecordedRunStory is the immutable agreement copied when a run was accepted.
var RecordedRunStory = Type("RecordedRunStory", func() {
	Description("The Story copied from this run's accepted build, independent of later revisions or builds.")
	Attribute("build_number", Int, "Number of the build this run was accepted for.", func() { Minimum(1) })
	Attribute("build_started_at", String, "Accepted build's saved start time.", func() { Format(FormatDateTime) })
	Attribute("stories", ArrayOf(StorySummaryItem), "Stories as agreed for this run, in declaration order; empty when the agreement declares none.")
	OneOf("mapping", "The owner's exact required-test mapping for written tests, or the explicit absence of such a mapping for an operator evaluator.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "state")
		Attribute("mapped", RunStoryMapping)
		Attribute("not_recorded_for_evaluator", RunEvaluatorMappingUnknown)
	})
	Required("build_number", "build_started_at", "stories", "mapping")
})

// RunStoryMapping names the tests generation requires for the recorded flows.
var RunStoryMapping = Type("RunStoryMapping", func() {
	Description("Exact mapping produced by the same owner that generates Story tests; unfinished flows require no test.")
	Attribute("required_tests", ArrayOf(RequiredTest), "One test per finished flow, in declaration order.")
	Required("required_tests")
})

// RunEvaluatorMappingUnknown preserves unknown coverage for operator tests.
var RunEvaluatorMappingUnknown = Type("RunEvaluatorMappingUnknown", func() {
	Description("The operator evaluator has no recorded relation between its declared tests and the Story flows. This does not prove the flows were untested.")
})

// RunStoryNotRecorded distinguishes old evidence from a recorded empty Story.
var RunStoryNotRecorded = Type("RunStoryNotRecorded", func() {
	Description("No Story context was recorded for this run. Never substitute the current agreement's Story.")
})

// RunApprovalAvailable is an observation, never a lease to write approval.
var RunApprovalAvailable = Type("RunApprovalAvailable", func() {
	Description("The owner checked this run against current approval requirements. approve_merge rechecks them and may refuse a later change; this read saves nothing.")
})

// RunApprovalBlocked gives the owner's next step without client inference.
var RunApprovalBlocked = Type("RunApprovalBlocked", func() {
	Description("Approval cannot currently be offered for this run.")
	Attribute("reason", String, "Owner's safe explanation and enabling step.", func() { MinLength(1) })
	Required("reason")
})

// VerificationReview contains the user's evidence and owner admission observation.
var VerificationReview = Type("VerificationReview", func() {
	Description("The user's review of one selected run; no hidden scenario names, checks or logs are exposed.")
	OneOf("story_context", "The run's immutable Story context, or the explicit fact it was not recorded.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "state")
		Attribute("recorded", RecordedRunStory)
		Attribute("not_recorded", RunStoryNotRecorded)
	})
	OneOf("approval", "The owner's current observation for this exact report, independent of inputs_current.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "state")
		Attribute("available", RunApprovalAvailable)
		Attribute("blocked", RunApprovalBlocked)
		Attribute("recorded", ApprovalItem)
		Attribute("unverified", UnverifiedApprovalItem)
	})
	Required("story_context", "approval")
})

// VerificationReviewResult extends the run status only for the user's selected read.
var VerificationReviewResult = Type("VerificationReviewResult", func() {
	Description("A selected verification run and its required user review. get_verification and list_verifications retain their existing report contracts.")
	Extend(VerificationStatusResult)
	Attribute("review", VerificationReview, "Immutable Story evidence and the owner's current approval observation.")
	Required("review")
})

// verificationReviewMethod registers the user-only read in the Surla service.
func verificationReviewMethod() {
	Method("review_verification", func() {
		Description("Read one saved run for the authenticated user before approval: its existing builder-safe result, the Story recorded at acceptance and exact required-test mapping when known, and the owner's current approval observation. This read records no approval and changes no workflow state. Agents use get_verification instead.")
		Payload(GetVerificationPayload)
		Result(VerificationReviewResult)
		Error("not_found", ErrorResult, "The selected run does not exist.")
		Error("not_implemented", ErrorResult, "Verification is not configured.")
		Tool("review_verification", "Read a selected run's Story evidence and current approval observation as the user; does not approve, merge or deploy.")
		HTTP(func() {
			GET("/verifications/{run_id}/review")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
			Response("not_implemented", StatusNotImplemented)
		})
	})
}
