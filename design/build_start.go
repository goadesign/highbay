// This file declares a build start's saved build, team outcome and refusals.
package design

import . "goa.design/goa/v3/dsl"

// BuildStartStep gives the user one bounded action after a refusal or warning.
var BuildStartStep = Type("BuildStartStep", String, func() {
	MinLength(1)
	MaxLength(512)
})

// BuildTeamWindowBusy leaves the saved build intact while the user closes a dialog.
var BuildTeamWindowBusy = Type("BuildTeamWindowBusy", func() {
	Attribute("step", BuildStartStep, "Close the open dialog, then start the team agents.")
	Required("step")
})

// BuildTeamAmbiguousSpace leaves the saved build intact while the user closes
// the duplicate spaces.
var BuildTeamAmbiguousSpace = Type("BuildTeamAmbiguousSpace", func() {
	Attribute("count", Int, "Number of spaces matching this project.", func() { Minimum(2) })
	Attribute("step", BuildStartStep, "Close the duplicate Herdr spaces, then start the agents.")
	Required("count", "step")
})

// BuildTeamUnavailable leaves the saved build intact when team admission cannot finish.
var BuildTeamUnavailable = Type("BuildTeamUnavailable", func() {
	Attribute("step", BuildStartStep, "What the user does to make the team ready for the saved build.")
	Required("step")
})

// StartBuildResult separates the saved build from this start's team outcome.
var StartBuildResult = Type("StartBuildResult", func() {
	Description("The saved build and this start's team outcome. A team warning never means the build was refused; get_build returns the build without it. A start moves no working copy: each copy joins the build's own branch when its worker first reads the assignment.")
	Attribute("build", StartedBuild, "The build saved before Surla made any missing working copy and admitted the team.")
	OneOf("team", "The team's immediate outcome after the build was saved.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "state")
		Attribute("ready", ProjectTeam)
		Attribute("window_busy", BuildTeamWindowBusy)
		Attribute("ambiguous_space", BuildTeamAmbiguousSpace)
		Attribute("unavailable", BuildTeamUnavailable)
	})
	Required("build", "team")
})

// ReplacedBuild names the open build the user agreed to build again.
var ReplacedBuild = Type("ReplacedBuild", func() {
	Description("The open build of this same change that the page showed the user when the user agreed to build the change again before any verification of it passed. A start whose replaces does not name the open build exactly was decided on a stale page and is refused with the open build's facts.")
	Attribute("change_id", String, "change_id of the open build the page showed.", func() { MinLength(1) })
	Attribute("started_at", String, "started_at of the open build the page showed, exactly as get_build reported it.", func() { Format(FormatDateTime) })
	Required("change_id", "started_at")
})

// OpenBuildRefusal names the project's one open build in a refusal.
var OpenBuildRefusal = Type("OpenBuildRefusal", func() {
	Description("A refusal of start_build or drop_build that names the project's one open build, so the page shows what blocks the request and offers Drop for exactly that build. Nothing changed.")
	Attribute("name", String, "Error name.", func() { Meta("struct:error:name") })
	Attribute("message", String, "What was refused, why, and the user's step, worded for the page.")
	Attribute("change_id", String, "Change the open build is for; drop_build takes it.")
	Attribute("build", StartedBuild, "The open build as get_build reports it, with the commits to merge once a verification of it passed; drop_build takes its started_at.")
	Required("name", "message", "change_id", "build")
})

// buildStartRefusals declares the conditions that refuse a start before it saves.
func buildStartRefusals() {
	Error("not_repository", ErrorResult, "The project folder is not in a Git repository, which every build's working copies need; nothing was saved. The message ends with the step to create or select a repository.")
	Error("no_commit", ErrorResult, "The project repository has no commit for the build's working copies to start from; nothing was saved. The message ends with the step to make its first commit.")
	Error("no_test_layout", ErrorResult, "This project has neither stored verification settings nor Surla's test layout; nothing was saved. Store verification settings, then build again.")
	Error("git_identity_missing", ErrorResult, "Git has no configured name and address, which Surla uses to commit what a working copy left when it joins a new build and to record accepted tests; nothing was saved. Configure Git's name and address, then build again.")
	Error("copy_folder_in_use", ErrorResult, "A working copy's folder in the project folder, .surla-build or .surla-tests, is a file or a folder that is not a linked working copy of the project's repository; nothing was saved. The message names the folder and ends with the step to move it out of the project.")
	Error("tests_branch_in_use", ErrorResult, "The branch Surla checks out in a missing working copy, surla/build-<n> or surla/tests-<n>, is checked out in another folder; nothing was saved. The message names that folder, quoted, and ends with the step that frees the branch.")
	Error("working_copy_busy", ErrorResult, "Another build is starting, a worker is reading its assignment into its working copy, or a test check holds the test writer's working copy; nothing was saved. Start the build again when that has ended.")
	Error("previous_change_open", OpenBuildRefusal, "Another change's build is open, and the user has neither merged a passed run of it into the branch checked out in the project folder nor dropped it; nothing was saved. The body names that build, with the commits to merge once a run passed, so the page offers the merge commands and Drop.")
	Error("unfinished_build_open", OpenBuildRefusal, "Building this change again replaces its open build, which no verification has passed, and replaces does not name that build; nothing was saved. The page asks the user first, then sends replaces naming the build it showed.")
	Error("drafted_before_drop", ErrorResult, "The agreed revision was drafted before the user dropped another change, so its design can hold that change's design; nothing was saved. The guide revises the change from its draft; the user adopts the revision, then builds.")
	Error("design_changed_outside", ErrorResult, "A design file at the project's commit is not a version the agreed revision was made from: it was changed outside Surla; nothing was saved. The message names the file and the commit. When the change is uncommitted, commit the design change in the project folder first; then the guide calls update_draft, refresh_story and revise_change, and the user adopts the revision and builds.")
}

// buildStartRefusalResponses maps each project-state refusal to a conflict.
func buildStartRefusalResponses() {
	Response("not_repository", StatusConflict)
	Response("no_commit", StatusConflict)
	Response("no_test_layout", StatusConflict)
	Response("git_identity_missing", StatusConflict)
	Response("copy_folder_in_use", StatusConflict)
	Response("tests_branch_in_use", StatusConflict)
	Response("working_copy_busy", StatusConflict)
	Response("previous_change_open", StatusConflict)
	Response("unfinished_build_open", StatusConflict)
	Response("drafted_before_drop", StatusConflict)
	Response("design_changed_outside", StatusConflict)
}
