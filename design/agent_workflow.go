// This file declares guide-to-browser handoff and the user's approved merge.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// OpenViewsResult returns the browser handoff outcome and the project's Chorus coordinates.
var OpenViewsResult = Type("OpenViewsResult", func() {
	Description("Result of opening Highbay's browser workspace, including the project's Chorus team, channel, and guide handle so an attached guide session can join Chorus in one step.")
	Attribute("opened", Boolean, "Whether the browser opener completed successfully.")
	Attribute("message", String, "Safe handoff result without a URL or credential.")
	Attribute("team", String, "Stable Chorus swarm name for this project.")
	Attribute("channel", String, "Primary Chorus #channel for this project (for example #refunds-8f3a1c902b4e).")
	Attribute("handle", String, "Registered Chorus handle for this project's guide (empty when no guide is registered yet).")
	Required("opened", "message", "team", "channel", "handle")
})

// agentWorkflowMethods keeps browser credentials inside their project owner.
func agentWorkflowMethods() {
	Method("open_views", func() {
		Description("Open Highbay's browser workspace at the user's request and return the project's Chorus team, #channel, and guide handle. The project owner mints and opens the private browser link locally; no browser credential is returned to the caller.")
		Result(OpenViewsResult)
		Tool("open_views", "Open Highbay's browser workspace for the user and return team, channel, and handle so you can join the project's Chorus channel immediately. No browser URL or credential is returned.")
		HTTP(func() { POST("/views/open"); Response(StatusOK) })
	})
	Method("merge_change", func() {
		Description("Merge the user's current live-approved candidate and accepted tests into the clean checked-out local project branch. Rechecks approval, report and build identity before mutation. On failure restores the original branch and tracked state while preserving untracked files. After success stops only the merged build's workers. Repeating the exact already-taken approval is safe.")
		Payload(func() {
			Attribute("change_id", String, "Approved change identifier.", func() { MinLength(1) })
			Attribute("request_revision", String, "Approved revision.", func() { MinLength(1) })
			Attribute("candidate_commit", String, "Approved candidate commit.", func() { Pattern("^([0-9a-f]{40}|[0-9a-f]{64})$") })
			Attribute("report_digest", String, "Approved report digest.", func() { MinLength(1) })
			Required("change_id", "request_revision", "candidate_commit", "report_digest")
		})
		Result(func() {
			Attribute("branch", String, "Checked-out local branch that received the approved work.")
			Attribute("head_commit", String, "Resulting local branch commit.")
			Required("branch", "head_commit")
		})
		Error("not_found", ErrorResult, "The owned Change Card does not exist.")
		Error("not_approved", ErrorResult, "The current live approval, verification report or open build does not match this request.")
		Error("detached_head", ErrorResult, "Check out a local branch before merging.")
		Error("dirty_worktree", ErrorResult, "Commit or remove tracked changes before merging; untracked files may remain.")
		Error("merge_conflict", ErrorResult, "Git could not merge the approved commits; the original tracked state was restored, or the error reports that restoration failed.")
		Error("working_copy_busy", ErrorResult, "A project operation currently owns a working-copy turn; try again when it finishes.")
		HTTP(func() {
			POST("/changes/{change_id}/merge")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
			Response("not_approved", StatusConflict)
			Response("detached_head", StatusConflict)
			Response("dirty_worktree", StatusConflict)
			Response("merge_conflict", StatusConflict)
			Response("working_copy_busy", StatusConflict)
		})
	})
}
