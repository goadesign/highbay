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
	Attribute("draft_folder", String, "Absolute path intended for the guide's draft, .highbay-draft in the project folder, whose go.mod, go.sum and design/ refresh_story reads. When state is none, this path does not mean the folder exists; refresh_story makes it when needed.")
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

// DraftCorrection is one file to correct before update_draft can bring a
// change forward, with the folder that says who corrects it.
var DraftCorrection = Type("DraftCorrection", func() {
	Description("A file to correct before update_draft can bring a change forward, and the folder that holds it. When folder is draft_folder, the guide corrects the file in its draft. When folder is project_folder, the file is the user's own, in the project folder: the guide tells the user what to correct there and waits, and never edits it. The guide calls update_draft again until none is listed, then refresh_story.")
	Attribute("path", String, "Path relative to the folder that folder names, such as design/types.go or go.mod.")
	Attribute("folder", String, "Which folder holds the file to correct: draft_folder is the guide's draft, at the result's draft_folder; project_folder is the user's project folder, which holds the draft.", func() {
		Enum("draft_folder", "project_folder")
	})
	Attribute("problem", String, "What to correct. In the draft: conflict markers where the draft and the project folder changed the same lines; a file removed on one side and changed on the other; a draft file Go cannot parse, with its line; a file whose project change Highbay took without merging, or was writing when it stopped, to check that it holds the intended edits once; or a module the project's go.mod requires at another version than the draft's, or that the draft's lacks, including differing replace directives and a differing module path. In the project folder: a design file or go.mod there that cannot be parsed, with its line, or a go.mod that declares no module.")
	Required("path", "folder", "problem")
})

// DraftUpdateResult reports what update_draft brought into the draft.
var DraftUpdateResult = Type("DraftUpdateResult", func() {
	Description("What update_draft brought from the project folder into the guide's draft: design files the draft took as they are there, design files merged with the draft's own edits, and files to correct, each with the folder that holds it. The lists describe what this call applied and what remains to correct; empty lists do not prove that every outside design change is present.")
	Attribute("draft_folder", String, "Absolute path of the guide's draft folder.")
	Attribute("taken", ArrayOf(String), "Design files the draft took from the project folder because it had not changed them, in path order.")
	Attribute("merged", ArrayOf(String), "Design files where the project folder's change and the draft's edits merged without conflict, in path order.")
	Attribute("to_correct", ArrayOf(DraftCorrection), "Files to correct before refreshing, each in the folder it names, in path order; empty when none.")
	Required("draft_folder", "taken", "merged", "to_correct")
})

// storyRefreshMethods exposes guide draft Story evaluation over HTTP and MCP.
func storyRefreshMethods() {
	Method("refresh_story", func() {
		Description("Read the guide's draft, .highbay-draft in the project folder: its go.mod, go.sum and the Go files under design/ in gofmt form, never the rest of the project. When the draft is missing, Highbay makes it first, from the newest design a Change Card holds whose versions are all on the draft's made-from list, with the project's go.mod and go.sum, or else from the project folder; after the user dropped a build, it makes the draft again from the project folder and says so in message until a revision is saved. Compute the canonical snapshot_digest and start or reuse the project owner's in-memory Story refresh operation, which every caller acting for the owner reads; it is reused only while the draft and its made-from list are unchanged. Return promptly with op_id, state, snapshot_digest and draft_folder while evaluation continues on a server-owned context that survives client disconnects. A succeeded operation keeps the design, Stories, architecture, the draft's go.mod and the made-from list it read; propose_change and revise_change save them with the revision. A design file Go cannot parse, or one that imports a package of the project outside design/, is refused with its file and line.")
		Result(StoryRefreshResult)
		Error("unprocessable", ErrorResult, "The project folder holds no go.mod (run init_project or create go.mod), a draft file Go cannot parse or that imports the project outside design/ (the message names the file and line), or the draft is a link or a file rather than a folder.")
		Error("unavailable", ErrorResult, "The control plane is shutting down and is no longer starting Story refresh operations.")
		Tool("refresh_story", "Start or reuse a Story evaluation of your draft and return op_id, state, snapshot_digest and draft_folder promptly. The design runs as an ordinary process with the user's file and network access. Takes no input. Write the next revision's design only in draft_folder, never in the project folder, and run go there as GOWORK=off go …. Poll get_story_refresh until state is succeeded or failed.")
		HTTP(func() {
			POST("/workspace/story/refresh")
			Response(StatusOK)
			Response("unprocessable", StatusUnprocessableEntity)
			Response("unavailable", StatusServiceUnavailable)
		})
	})

	Method("get_story_refresh", func() {
		Description("Return the project owner's current in-memory Story refresh operation for the guide's draft, the same one for every caller acting for the owner: op_id, state (none, running, succeeded, or failed), snapshot_digest, draft_folder, and the evaluated stories (empty when the design does not load the Story module) or a failure diagnostic. When no refresh has been run yet in this server process for the guide's draft, state is none with the intended draft_folder and a message instructing the caller to run refresh_story; this read succeeds before a go.mod or draft folder exists.")
		Result(StoryRefreshResult)
		Tool("get_story_refresh", "Read the current Story refresh operation for the guide's draft: op_id, state (none, running, succeeded, or failed), snapshot_digest, draft_folder, stories, or diagnostic. Takes no input. When state is none, call refresh_story first.")
		HTTP(func() {
			GET("/workspace/story/refresh")
			Response(StatusOK)
		})
	})

	Method("update_draft", func() {
		Description("The guide brings design changes made outside Highbay into its draft, before drafting a new change and when told a design file changed in the project folder. Under the draft's lock Highbay compares each design file's draft and live versions over a recorded base. The history base is the latest recorded version present in the project's committed history since the draft's start, or the starting folder version when that history supplies none. The comparison base is whichever of that history base and the outside version the draft last took was recorded later; with no previous take, it is the history base. A live version equal to the comparison base brings nothing. If the project's committed history gained a recorded version since the last take, Highbay takes and reports the live version without merging it. Otherwise an outside change is taken when the draft did not change that file, merged with git merge-file when both changed it, and listed to correct when both changed the same lines or one side removed what the other changed. A draft file Go cannot parse, and a module the project's go.mod requires at another version or with another replace directive than the draft's, are listed to correct; Highbay does not merge go.mod. Each listed file names the folder that holds it: the guide corrects a file of its draft, while a design file or go.mod of the project folder that cannot be parsed is the user's to correct there and the guide never edits it. When the project's commit, the draft, the project's files or the draft's made-from list change while Highbay prepares the update, Highbay brings nothing and answers conflict, and the guide calls update_draft again. Highbay records an interrupted update so a later call can finish it without merging its result again; if the guide changed the file in between, the file is listed to correct. An uncommitted revert to an older version may bring nothing; when a build reports design_changed_outside for an uncommitted design change, the user commits the change in the project folder, then asks the guide to call update_draft, refresh_story and revise_change. Otherwise call refresh_story after this update. Takes no input.")
		Result(DraftUpdateResult)
		Error("unprocessable", ErrorResult, "Highbay could not read what the update needs; the message names the file or folder. The project folder holds no go.mod, so there is no draft (run init_project or create go.mod), or was replaced after startup; the draft is a link or a file rather than a folder, its .gitignore lacks Highbay's draft marker, or its made-from list is missing; the draft or its made-from list changed while Highbay was first making the draft; the go.mod of the draft or of the project folder is missing, or a go.mod, go.sum or design file there is not a regular file, cannot be read or is not valid UTF-8; or a design file in the project's current commit cannot be parsed as Go or is a link. The guide corrects a file of its draft and asks the user to correct one of the project folder or of its commit, then calls update_draft again.")
		Error("conflict", ErrorResult, "The project's commit, the draft, the project's go.mod, go.sum or design files, or the draft's made-from list changed while Highbay prepared the update. Highbay brought no change into the draft; call update_draft again.")
		Error("unavailable", ErrorResult, "The control plane is shutting down.")
		Tool("update_draft", "Bring design changes made outside Highbay, in the project folder, into your draft. Call it before drafting each new change and whenever you are told a design file changed. Takes no input. It returns the files it took, the files it merged with your edits, and files to correct, each with the folder that holds it. Correct a file whose folder is draft_folder yourself, in your draft. For a file whose folder is project_folder, tell the user what to correct in the project folder and wait: never edit it. Call update_draft again until no file is listed, then call refresh_story. When it answers that the project or the draft changed while it prepared the update, it brought nothing: call update_draft again. When it answers that it could not read a file, the message names the file and says who corrects it.")
		HTTP(func() {
			POST("/workspace/draft/update")
			Response(StatusOK)
			Response("unprocessable", StatusUnprocessableEntity)
			Response("conflict", StatusConflict)
			Response("unavailable", StatusServiceUnavailable)
		})
	})
}
