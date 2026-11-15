// This file declares the guide draft Story refresh and status operations.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// StoryQuestionItem describes an unresolved decision attached to a Story flow or step.
var StoryQuestionItem = Type("StoryQuestionItem", func() {
	Description("Unresolved decision attached to a Story flow or step in declaration order.")
	Attribute("key", String, "Flow-scoped question key.")
	Attribute("prompt", String, "Human-facing question text.")
	Attribute("description", String, "Optional longer context for the question.")
	Attribute("options", ArrayOf(String), "Suggested choices in declaration order.")
	Required("key", "prompt", "options")
})

// StoryStepItem describes one starting event, intermediate action, or outcome in a Story flow.
var StoryStepItem = Type("StoryStepItem", func() {
	Description("Starting event, intermediate action, or outcome in a Story flow in declaration order.")
	Attribute("key", String, "Flow-scoped step key.")
	Attribute("actor_key", String, "Story-scoped key of the actor performing or observing the step.")
	Attribute("summary", String, "Timeline sticky-note sentence for the step.")
	Attribute("description", String, "Optional longer explanation of the step.")
	Attribute("kind", String, "Step position in the flow: start, step, or outcome.", func() {
		Enum("start", "step", "outcome")
	})
	Attribute("questions", ArrayOf(StoryQuestionItem), "Unresolved step-level decisions in declaration order.")
	Required("key", "actor_key", "summary", "kind", "questions")
})

// StoryFlowItem describes one ordered timeline path through a domain Story.
var StoryFlowItem = Type("StoryFlowItem", func() {
	Description("Ordered timeline path through a domain Story in declaration order.")
	Attribute("key", String, "Story-scoped flow key.")
	Attribute("title", String, "Human-facing flow title.")
	Attribute("description", String, "Optional longer explanation of the flow.")
	Attribute("complete", Boolean, "True when the flow has a start, at least one outcome, and no open questions on the flow or any of its steps.")
	Attribute("steps", ArrayOf(StoryStepItem), "Starting, intermediate, and outcome steps in declaration order.")
	Attribute("questions", ArrayOf(StoryQuestionItem), "Unresolved flow-level decisions in declaration order.")
	Required("key", "title", "complete", "steps", "questions")
})

// StoryActorItem describes one domain role declared in a Story.
var StoryActorItem = Type("StoryActorItem", func() {
	Description("Domain role declared in a Story in declaration order.")
	Attribute("key", String, "Story-scoped actor key.")
	Attribute("title", String, "Human-facing actor label.")
	Attribute("description", String, "Optional longer explanation of the actor role.")
	Required("key", "title")
})

// StorySummaryItem describes one evaluated domain Story in declaration order.
var StorySummaryItem = Type("StorySummaryItem", func() {
	Description("Evaluated domain Story extracted from the bound workspace design in declaration order.")
	Attribute("name", String, "Human-facing Story title.")
	Attribute("description", String, "Optional longer explanation of the Story.")
	Attribute("actors", ArrayOf(StoryActorItem), "Domain roles declared in the Story in declaration order.")
	Attribute("flows", ArrayOf(StoryFlowItem), "Timeline paths declared in the Story in declaration order.")
	Required("name", "actors", "flows")
})

// StoryRefreshResult reports the project owner's current Story refresh operation for the guide's draft.
var StoryRefreshResult = Type("StoryRefreshResult", func() {
	Description("Current Story refresh operation of the project's owner for the guide's draft: the user, the guide and the builder all act for the owner, so they read the same one. state is none when no refresh has run yet in this server process for the guide's draft; running while evaluation is in progress; succeeded with the evaluated stories (empty when the design does not load the Story module); or failed with a safe diagnostic.")
	Attribute("draft_folder", String, "Absolute path intended for the guide's draft, .surla-draft in the project folder, whose go.mod, go.sum and design/ refresh_story reads. When state is none, this path does not mean the folder exists; refresh_story makes it when needed.")
	Attribute("op_id", String, "Identifier of the owner's current Story refresh operation for the guide's draft; present when state is running, succeeded, or failed. Pass it as story_refresh_op_id to propose_change or revise_change once state is succeeded to adopt the evaluated Story into a Change Card.")
	Attribute("state", String, "Current operation state: none, running, succeeded, or failed.", func() {
		Enum("none", "running", "succeeded", "failed")
	})
	Attribute("snapshot_digest", String, "Canonical SHA-256 digest of the draft snapshot read when this refresh started; present when state is running, succeeded, or failed.")
	Attribute("stories", ArrayOf(StorySummaryItem), "Evaluated domain Stories in declaration order when state is succeeded (empty when the design declares no Stories or when state is none, running, or failed).")
	Attribute("diagnostic", String, "Safe failure or timeout diagnostic when state is failed; omitted otherwise.")
	Attribute("message", String, "Summary of the current Story refresh state and next action. After a dropped build remakes the draft and until a revision is saved from it, begins by naming the dropped change and saying that edits made before then are gone.")
	Required("state", "stories", "message", "draft_folder")
})

// DraftCorrection is one file update_draft could not bring forward.
var DraftCorrection = Type("DraftCorrection", func() {
	Description("A file of the draft that update_draft could not bring forward from the project folder: the guide corrects it in the draft and calls update_draft again until none is listed, then refresh_story.")
	Attribute("path", String, "Path relative to the draft folder, such as design/types.go or go.mod.")
	Attribute("problem", String, "What to correct: conflict markers where the draft and the project folder changed the same lines; a file removed on one side and changed on the other; a draft file Go cannot parse, with its line; or a module the project's go.mod requires at another version than the draft's, or that the draft's lacks, including differing replace directives.")
	Required("path", "problem")
})

// DraftUpdateResult reports what update_draft brought into the draft.
var DraftUpdateResult = Type("DraftUpdateResult", func() {
	Description("What update_draft brought from the project folder into the guide's draft: design files the draft took as they are there, design files merged with the draft's own edits, and files to correct. The lists describe what this call applied and what remains to correct; empty lists do not prove that every outside design change is present.")
	Attribute("draft_folder", String, "Absolute path of the guide's draft folder.")
	Attribute("taken", ArrayOf(String), "Design files the draft took from the project folder because it had not changed them, in path order.")
	Attribute("merged", ArrayOf(String), "Design files where the project folder's change and the draft's edits merged without conflict, in path order.")
	Attribute("to_correct", ArrayOf(DraftCorrection), "Files to correct in the draft before refreshing, in path order; empty when none.")
	Required("draft_folder", "taken", "merged", "to_correct")
})

// storyRefreshMethods exposes guide draft Story evaluation over HTTP and MCP.
func storyRefreshMethods() {
	Method("refresh_story", func() {
		Description("Read the guide's draft, .surla-draft in the project folder: its go.mod, go.sum and the Go files under design/ in gofmt form, never the rest of the project. When the draft is missing, Surla makes it first, from the newest design a Change Card holds whose versions are all on the draft's made-from list, with the project's go.mod and go.sum, or else from the project folder; after the user dropped a build, it makes the draft again from the project folder and says so in message until a revision is saved. Compute the canonical snapshot_digest and start or reuse the project owner's in-memory Story refresh operation, which every caller acting for the owner reads; it is reused only while the draft and its made-from list are unchanged. Return promptly with op_id, state, snapshot_digest and draft_folder while evaluation continues on a server-owned context that survives client disconnects. A succeeded operation keeps the design, Stories, architecture, the draft's go.mod and the made-from list it read; propose_change and revise_change save them with the revision. A design file Go cannot parse, or one that imports a package of the project outside design/, is refused with its file and line.")
		Result(StoryRefreshResult)
		Error("unprocessable", ErrorResult, "The project folder holds no go.mod (run init_project or create go.mod), a draft file Go cannot parse or that imports the project outside design/ (the message names the file and line), or the draft is a link or a file rather than a folder.")
		Error("not_implemented", ErrorResult, "No workspace directory is bound to this control plane; start Surla from a project directory.")
		Error("unavailable", ErrorResult, "The control plane is shutting down and is no longer starting Story refresh operations.")
		Tool("refresh_story", "Start or reuse a Story evaluation of your draft and return op_id, state, snapshot_digest and draft_folder promptly. The design runs as an ordinary process with the user's file and network access. Takes no input. Write the next revision's design only in draft_folder, never in the project folder, and run go there as GOWORK=off go …. Poll get_story_refresh until state is succeeded or failed.")
		HTTP(func() {
			POST("/workspace/story/refresh")
			Response(StatusOK)
			Response("unprocessable", StatusUnprocessableEntity)
			Response("not_implemented", StatusNotImplemented)
			Response("unavailable", StatusServiceUnavailable)
		})
	})

	Method("get_story_refresh", func() {
		Description("Return the project owner's current in-memory Story refresh operation for the guide's draft, the same one for every caller acting for the owner: op_id, state (none, running, succeeded, or failed), snapshot_digest, draft_folder, and the evaluated stories (empty when the design does not load the Story module) or a failure diagnostic. When no refresh has been run yet in this server process for the guide's draft, state is none with the intended draft_folder and a message instructing the caller to run refresh_story; this read succeeds before a go.mod or draft folder exists.")
		Result(StoryRefreshResult)
		Error("not_implemented", ErrorResult, "No workspace directory is bound to this control plane; start Surla from a project directory.")
		Tool("get_story_refresh", "Read the current Story refresh operation for the guide's draft: op_id, state (none, running, succeeded, or failed), snapshot_digest, draft_folder, stories, or diagnostic. Takes no input. When state is none, call refresh_story first.")
		HTTP(func() {
			GET("/workspace/story/refresh")
			Response(StatusOK)
			Response("not_implemented", StatusNotImplemented)
		})
	})

	Method("update_draft", func() {
		Description("The guide brings design changes made outside Surla into its draft, before drafting a new change and when told a design file changed in the project folder. Under the draft's lock Surla compares each design file's draft and live versions over a recorded base. The history base is the latest recorded version present in the project's committed history since the draft's start, or the starting folder version when that history supplies none. The comparison base is whichever of that history base and the outside version the draft last took was recorded later; with no previous take, it is the history base. A live version equal to the comparison base brings nothing. If the project's committed history gained a recorded version since the last take, Surla takes and reports the live version without merging it. Otherwise an outside change is taken when the draft did not change that file, merged with git merge-file when both changed it, and listed to correct when both changed the same lines or one side removed what the other changed. A draft file Go cannot parse, and a module the project's go.mod requires at another version or with another replace directive than the draft's, are listed to correct; Surla does not merge go.mod. Surla records an interrupted update so a later call can finish it without merging its result again; if the guide changed the file in between, the file is listed to correct. An uncommitted revert to an older version may bring nothing; when a build reports design_changed_outside for an uncommitted design change, the user commits the change in the project folder, then asks the guide to call update_draft, refresh_story and revise_change. Otherwise call refresh_story after this update. Takes no input.")
		Result(DraftUpdateResult)
		Error("unprocessable", ErrorResult, "The project folder holds no go.mod, so there is no draft (run init_project or create go.mod), or a file there could not be read; the message names it.")
		Error("not_implemented", ErrorResult, "No workspace directory is bound to this control plane; start Surla from a project directory.")
		Error("unavailable", ErrorResult, "The control plane is shutting down.")
		Tool("update_draft", "Bring design changes made outside Surla, in the project folder, into your draft. Call it before drafting each new change and whenever you are told a design file changed. Takes no input. It returns the files it took, the files it merged with your edits, and files to correct: correct each in the draft and call update_draft again until none is listed, then call refresh_story.")
		HTTP(func() {
			POST("/workspace/draft/update")
			Response(StatusOK)
			Response("unprocessable", StatusUnprocessableEntity)
			Response("not_implemented", StatusNotImplemented)
			Response("unavailable", StatusServiceUnavailable)
		})
	})
}
