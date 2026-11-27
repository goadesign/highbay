// This file declares how callers find the project's saved Change Cards and
// how the views page learns that they changed.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// LatestVerificationItem summarizes the newest verification run of a Change Card.
var LatestVerificationItem = Type("LatestVerificationItem", func() {
	Description("The most recently accepted Checkpoint 4 verification run of a Change Card.")
	Attribute("run_id", String, "Verification run identifier; get_verification takes it.")
	Attribute("state", String, "running while the run builds or evaluates the candidate; passed or failed when its report's verdict is known; not_completed when it stopped without a report.", func() {
		Enum("running", "passed", "failed", "not_completed")
	})
	Attribute("inputs_current", Boolean, "Whether this run names the agreed request_revision and the tests selected now: the accepted test commit of the usable build in force, or the evaluator suite in the stored verification settings. It does not say whether the candidate working tree, deployment or approval is current.")
	Required("run_id", "state", "inputs_current")
})

// ChangeListItem summarizes one saved Change Card for finding work.
var ChangeListItem = Type("ChangeListItem", func() {
	Description("One saved Change Card of the project as list_changes reports it.")
	Attribute("change_id", String, "Change Card identifier; get_change takes it.")
	Attribute("summary", String, "Plain-English summary of the current revision.")
	Attribute("status", String, "Checkpoint status of the current revision.", func() {
		Enum("needs_confirmation", "confirmed")
	})
	Attribute("request_revision", String, "Current request_revision of the Change Card.")
	Attribute("latest_verification", LatestVerificationItem, "The Change Card's most recently accepted verification run; absent when none was started.")
	Attribute("proposed_revision", String, "request_revision of the revision the guide proposed beside this confirmed change, while it waits for the user to adopt or decline it; absent otherwise. status and request_revision describe the revision in force.")
	Attribute("saved_at", String, "When the Change Card was last saved. It is not part of request_revision.", func() {
		Format(FormatDateTime)
	})
	Required("change_id", "summary", "status", "request_revision", "saved_at")
})

// ChangeList is the project's change list at one moment.
var ChangeList = Type("ChangeList", func() {
	Description("The project's saved Change Cards, most recently saved first (tie-broken by change_id).")
	Attribute("changes", ArrayOf(ChangeListItem), "Saved Change Cards, most recently saved first.")
	Attribute("version", String, "Opaque value for change detection. It changes whenever the listed cards, a card's get_build view (including the builder's branch head_commit and latest check), approval_available, initializing_phase, the owner's Story evaluation, get_team, get_team_conversation, or get_live_target changes; list_changes items carry no build or branch fields, so read get_build for those facts. Compare it only for equality.")
	Attribute("approval_available", Boolean, "Whether approve_merge can record approvals on this server: true when the server runs Checkpoint 4 verification, as every server highbay starts does, and any stored promotion settings could be resolved; false when the server runs no verification or resolving the promotion target fails.")
	Attribute("initializing_phase", String, "What init_project is doing now, as a short sentence for the user, such as \"Resolving the service's Go modules\"; absent when no initialization is running. A first initialization downloads Go modules and can take a few minutes. version changes with it, so wait_for_changes reports each phase.")
	Attribute("draft_state", String, "Progress of preparing the project's first Change Card while changes is empty and initializing_phase is absent: initialized after init_project published the starter in this server run, editing once update_draft prepared the guide's draft, evaluating_story while refresh_story is checking the Story and Goa design, story_ready after refresh_story succeeded and before propose_change saves the card, or story_failed when the latest refresh_story failed. Absent once a Change Card exists, while initializing_phase is set, or before starter initialization and draft preparation begin. version changes with it.", func() {
		Enum("initialized", "editing", "evaluating_story", "story_ready", "story_failed")
	})
	Required("changes", "version", "approval_available")
})

// ChangeListVersion names the change list current when a wait ends.
var ChangeListVersion = Type("ChangeListVersion", func() {
	Description("The version of the change list when the wait ended.")
	Attribute("version", String, "Version of the current change list; it equals since when nothing changed during the wait.")
	Required("version")
})

// changeListMethods exposes the card store owner's project list and its
// waiting read. The waiting read is HTTP-only: only the views page uses it.
func changeListMethods() {
	Method("list_changes", func() {
		Description("List the owner's saved Change Cards for this project, most recently saved first, with each card's summary, status, the request_revision in force, the request_revision of a revision the guide proposed beside a confirmed card while it waits for the user, the state of its most recently accepted verification run and whether its revision and selected tests are current, and when it was last saved, and say whether this server can record approvals. The views page opens on this list; agents use it to find a change_id. Reading changes nothing.")
		Result(ChangeList)
		Tool("list_changes", "List this project's Change Cards, most recently saved first, with each card's summary, status and request_revision in force, its proposed_revision (the request_revision of a revision proposed beside a confirmed card, while it waits for the user to adopt or decline it), latest verification run state and whether its revision and selected tests are current, and last save time. Read one card, with any proposed revision, with get_change(change_id).")
		HTTP(func() {
			GET("/changes")
			Response(StatusOK)
		})
	})

	Method("wait_for_changes", func() {
		Description("Return the current change list's version as soon as it differs from since, the version of the list the caller last read, or after a bounded wait of a few seconds when nothing changed. The version also changes when a card's get_build view changes (including the builder's branch head_commit or latest check), the owner's Story evaluation starts, finishes or is replaced, or a public fact in get_team or get_live_target changes. The views page keeps one such read open and, only when the version changed, calls list_changes again and, while it shows the Story, get_story_refresh. Each call is traced but never saved in the diagnostics history list_traces reads, so an open page does not push real operations out of view.")
		Payload(func() {
			Attribute("since", String, "version of the change list the caller last read.", func() {
				MinLength(1)
				MaxLength(128)
			})
			Required("since")
		})
		Result(ChangeListVersion)
		HTTP(func() {
			GET("/change-updates")
			Param("since")
			Response(StatusOK)
		})
	})
}
