// This file declares builds: the user's decision to build a confirmed change
// at one revision, and the builder's and the test writer's assignments for
// that build.
package design

import (
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
)

// testsNotJudged is the sentence every accepted result of the test writer's
// tests carries: what Surla's check established and what it left to the
// user. The control plane writes it from its own constant of the same name,
// and a test there compares the two through the generated contract.
const testsNotJudged = "Surla checked that these tests build into the generated test program and declare every required scenario; it did not judge what they assert."

// TestsNotUsed is how a build reads when no test writer is used.
var TestsNotUsed = Type("TestsNotUsed", func() {
	Description("No test writer is used: the project verifies with the evaluator stored with surla verification set. Every read of a build in such a project shows this, whatever the build recorded.")
})

// TestsAssigned is the test writer's state before it reports its tests finished.
var TestsAssigned = Type("TestsAssigned", func() {
	Description("Surla assigned the tests for this build; the test writer has not reported them finished.")
})

// TestsReported is the test writer's state after it reported its tests finished.
var TestsReported = Type("TestsReported", func() {
	Description("The test writer reported its tests finished at this build's revision. Surla checks them right after each report; its verdict replaces this state.")
})

// TestsNeedChanges is the test writer's state after Surla found problems.
var TestsNeedChanges = Type("TestsNeedChanges", func() {
	Description("Surla checked the reported tests and found problems the test writer must fix before reporting again, including a working copy that is off its build's branch or holds another build's work. Their text is in get_test_writer_assignment and, for the user, in get_test_findings.")
	Attribute("finding_count", Int, "Number of problems Surla found.", func() { Minimum(1) })
	Attribute("checked_at", String, "When Surla's check reached this verdict.", func() { Format(FormatDateTime) })
	Required("finding_count", "checked_at")
})

// TestsAccepted is the test writer's state after Surla accepted its tests.
var TestsAccepted = Type("TestsAccepted", func() {
	Description("Surla checked the reported tests and recorded them as a commit.")
	Attribute("commit", String, "Full commit ID Surla recorded the accepted tests at (40 or 64 lowercase hex characters).", func() {
		Pattern("^([0-9a-f]{40}|[0-9a-f]{64})$")
	})
	Attribute("checked_at", String, "When Surla's check reached this verdict.", func() { Format(FormatDateTime) })
	Attribute("not_judged", String, "What Surla's check did not judge, always the same sentence.", func() { Enum(testsNotJudged) })
	Required("commit", "checked_at", "not_judged")
})

// TestsCouldNotRun is the test writer's state when Surla's check could not run.
var TestsCouldNotRun = Type("TestsCouldNotRun", func() {
	Description("Surla's check of the reported tests could not run, for a cause the test writer cannot fix; the user's step makes it possible.")
	Attribute("reason", String, "Why the check could not run.", func() {
		MinLength(1)
		MaxLength(1024)
	})
	Attribute("step", String, "What the user does so the check can run.", func() {
		MinLength(1)
		MaxLength(512)
	})
	Attribute("checked_at", String, "When Surla's check stopped.", func() { Format(FormatDateTime) })
	Required("reason", "step", "checked_at")
})

// TestsNotYetAccepted is the builder's view of a build with no accepted tests.
var TestsNotYetAccepted = Type("TestsNotYetAccepted", func() {
	Description("The build records no accepted tests.")
})

// TestWriterProgress is the test writer's state and whether it read its assignment.
var TestWriterProgress = Type("TestWriterProgress", func() {
	Description("Where the test writer's assignment for one build stands.")
	OneOf("tests", "The tests' state: not_used when the project has stored verification settings; otherwise assigned, reported, needs_changes with the number of Surla's findings, accepted with the commit Surla recorded, or could_not_run with the reason and the user's step. Each verdict carries the time Surla reached it.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "state")
		Attribute("not_used", TestsNotUsed)
		Attribute("assigned", TestsAssigned)
		Attribute("reported", TestsReported)
		Attribute("needs_changes", TestsNeedChanges)
		Attribute("accepted", TestsAccepted)
		Attribute("could_not_run", TestsCouldNotRun)
	})
	Attribute("read", Boolean, "True once the test writer read this assignment in full with get_test_writer_assignment.")
	Attribute("report", Int, "Number of the test writer's latest report of this build: 0 until its first report, and one more with each report_tests_finished.", func() {
		Minimum(0)
	})
	Required("tests", "read", "report")
})

// BuilderProgress says whether the builder read its assignment and what it
// has recorded since: its branch's commit and its latest check.
var BuilderProgress = Type("BuilderProgress", func() {
	Description("The builder's assignment for one build. It exists from the moment the user starts the build; verification of the build's revision, not the assignment, judges the builder's work. branch, head_commit and the last check are three independent recorded facts: none says that a check or a verification ran on head_commit.")
	Attribute("read", Boolean, "True once the builder read this assignment in full with get_builder_assignment.")
	Attribute("branch", String, "The build's builder branch, surla/build-<n>.")
	Attribute("head_commit", String, "The commit the builder's branch points at, returned by get_build and reflected in list_changes.version (start_build omits it before any branch commit exists, and list_changes items carry no branch field): present while the build is still building, once the branch exists and points at another commit than the one the build started from. Omitted in other states. A passed build records its verified candidate_commit in taking.", func() {
		Pattern("^([0-9a-f]{40}|[0-9a-f]{64})$")
	})
	Attribute("last_check_passed", Boolean, "Whether the builder's latest completed check_project in this build passed; absent until one completed. The check covered the builder's working folder as it was then, committed or not.")
	Attribute("last_check_at", String, "When that check_project completed; absent with last_check_passed.", func() { Format(FormatDateTime) })
	Required("read")
})

// BuildBuilding says no verification of the open build has passed yet.
var BuildBuilding = Type("BuildBuilding", func() {
	Description("No verification of this build has passed yet, so there is nothing to merge: the builder finishes it, or the user drops it.")
})

// BuildPassedTests names the tests' commit a passed run used and its branch.
var BuildPassedTests = Type("BuildPassedTests", func() {
	Description("The accepted tests' commit a passed run used, which the user merges after the builder's commit, and the build's test-writer branch, where this commit was verified.")
	Attribute("commit", String, "Commit Surla recorded the accepted tests at.", func() { Pattern("^([0-9a-f]{40}|[0-9a-f]{64})$") })
	Attribute("branch", String, "The build's test-writer branch, surla/tests-<n>, where this commit was verified.")
	Required("commit", "branch")
})

// BuildPassed names what the user merges to take the open build.
var BuildPassed = Type("BuildPassed", func() {
	Description("A verification of this build passed and the user has not merged it: the commits of its newest passed run, which the user merges into the branch checked out in the project folder, the builder's first, then the tests'.")
	Attribute("run_id", String, "The newest passed run; get_verification takes it.")
	Attribute("candidate_commit", String, "The builder's commit the run passed.", func() { Pattern("^([0-9a-f]{40}|[0-9a-f]{64})$") })
	Attribute("candidate_branch", String, "The build's builder branch, surla/build-<n>, where this commit was verified.")
	OneOf("tests", "written_tests with the accepted tests' commit to merge after the builder's; evaluator when the project verifies with its stored evaluator, whose tests are not merged.", func() {
		TypeName("BuildPassedTestSource")
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "source")
		Attribute("evaluator", OperatorEvaluatorTests)
		Attribute("written_tests", BuildPassedTests)
	})
	Attribute("base_in_branch", Boolean, "False when the branch checked out in the project folder no longer holds the commit this build started from, because its history was rewritten: merging these commits brings that history back, and dropping the build and building again starts from the branch as it is.")
	Required("run_id", "candidate_commit", "candidate_branch", "tests", "base_in_branch")
})

// BuildMerged says the user took the open build.
var BuildMerged = Type("BuildMerged", func() {
	Description("The branch checked out in the project folder holds the commits of one passed run of this build: the builder's and, for written tests, the tests'. Each is held by merge or by a squash commit that holds every file it changed as that commit has it. This build no longer blocks starting another build; start_build's other checks still apply.")
})

// BuildDropped says the user dropped the open build.
var BuildDropped = Type("BuildDropped", func() {
	Description("The user dropped this build with drop_build: Surla builds nothing on it, no role has work for it, and it no longer blocks starting another build. An agreement drafted before the drop still cannot be built: get_build's drafted_before_drop reports it for each change, and start_build refuses it with drafted_before_drop until the guide refreshes the draft and revises the change and the user adopts the revision.")
	Attribute("kept_branches", ArrayOf(String), "The build's own branches that exist and hold a commit made after the build's base, where its work stays (git log <branch> shows it); empty when nothing was committed for it.")
	Required("kept_branches")
})

// StartedBuild is a change's build as the user started it.
var StartedBuild = Type("StartedBuild", func() {
	Description("The usable build the user started for a change, with the builder's assignment and the test writer's progress when one is used. A build started with only the builder remains usable while stored verification settings supply its tests.")
	Attribute("request_revision", String, "Change Card revision the user built.")
	Attribute("started_at", String, "When the user started this build.", func() { Format(FormatDateTime) })
	Attribute("in_force", Boolean, "True when request_revision is the change's agreed revision, the one in force, as get_build reports it. It stays true while a proposed revision waits and when a refreshed draft is adopted without changing request_revision. If adoption or confirmation changes the agreed request_revision, it becomes false; the assignments stay on the earlier revision until the user builds again.")
	Attribute("builder", BuilderProgress, "The builder's assignment.")
	Attribute("test_writer", TestWriterProgress, "The test writer's assignment.")
	Attribute("required_test_count", Int, "Number of finished flows in the agreed Story. Zero means no test of this change's Story was required.", func() { Minimum(0) })
	Attribute("required_tests", ArrayOf(RequiredTest), "Tests required by this build's saved Story, in declaration order; unfinished flows require none. Empty when no finished flow requires a test.")
	Attribute("answers", ArrayOfRequired(ConfirmedAnswer), "Confirmed answers saved when this build started; later revisions do not replace them.")
	Attribute("examples", ArrayOfRequired(ExampleItem), "Examples saved when this build started; later revisions do not replace them.")
	Attribute("coverage_statements", ArrayOfRequired(TestCoverageStatement), "The accepted report's writer statements; empty before acceptance or when none were stated. Surla did not judge them.")
	OneOf("taking", "Whether the user has taken this build: building until a verification of it passes; passed with the commits to merge; merged once the branch checked out in the project folder holds the commits of one passed run, the builder's and, for written tests, the tests'; dropped once the user dropped it. A project has one open build, and another change is built only once it is merged or dropped.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "state")
		TypeName("BuildTaking")
		Attribute("building", BuildBuilding)
		Attribute("passed", BuildPassed)
		Attribute("merged", BuildMerged)
		Attribute("dropped", BuildDropped)
	})
	Required("request_revision", "started_at", "in_force", "builder", "test_writer", "required_test_count", "required_tests", "answers", "examples", "coverage_statements", "taking")
})

// BuildNotStarted says the change has no usable build for its current test source.
var BuildNotStarted = Type("BuildNotStarted", func() {
	Description("This change has no usable build: the project's open build is another change's or there is none, or its builder-only build lost the stored verification settings that supplied its tests. Start a new build on the views page.")
})

// ChangeBuild is a change's build, or the fact that it has none.
var ChangeBuild = Type("ChangeBuild", func() {
	Description("A change's build as get_build reports it.")
	Attribute("change_id", String, "Change Card identifier.")
	Attribute("drafted_before_drop", Boolean, "True only when the current agreed revision’s saved record of how its design was drafted predates the current actual drop: start_build would refuse it with drafted_before_drop. It becomes true after that drop and false once the user adopts a revision drafted after the drop, even if its request_revision is unchanged. A waiting proposal does not change this fact; ask the guide to refresh_story and revise_change from that refresh, then adopt the revision before building. False says only that this refusal does not apply, not that every other build check will pass; it is also false without an agreed revision or saved record of its draft. start_build rechecks concurrent changes.")
	OneOf("build", "not_started when the change has no usable build for the current test source; start a new build on the views page. Otherwise started with the usable build.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "state")
		Attribute("not_started", BuildNotStarted)
		Attribute("started", StartedBuild)
	})
	Required("change_id", "drafted_before_drop", "build")
})

// CopyOffBranch states where a worker's working copy is when Surla will not use or move it.
var CopyOffBranch = Type("CopyOffBranch", func() {
	Description("A worker's working copy is not on its build's own branch, holds uncommitted work Surla does not move, or has a Git operation in progress. Surla moves no copy here: the facts and the goal say where the work belongs, and the worker puts it there with git, then calls again.")
	Attribute("name", String, "Error name.", func() { Meta("struct:error:name") })
	Attribute("message", String, "The facts and the goal in words, ending with the call to make again. A name the agent chose is shown shell-quoted when it is printable, and as \"a name that is not shown\" otherwise.")
	Attribute("branch", String, "The branch checked out in the copy, as message shows it; absent when the copy is detached.")
	Attribute("build_branch", String, "The build's own branch, surla/build-<n> or surla/tests-<n>; absent before the copy joined the build.")
	Attribute("operation", String, "The Git operation in progress in the copy, read from the folder the project's repository keeps for it. A bisect is none.", func() {
		Enum("none", "merge", "rebase", "am", "cherry_pick", "revert")
	})
	Attribute("uncommitted", ArrayOf(String), "The first five uncommitted paths of the copy (changes and untracked files, not ignored ones), as message shows them.", func() { MaxLength(5) })
	Attribute("uncommitted_count", Int, "How many paths of the copy are uncommitted in all.", func() { Minimum(0) })
	Required("name", "message", "operation", "uncommitted", "uncommitted_count")
})

// BuilderAssignment is the builder's assignment for one build.
var BuilderAssignment = Type("BuilderAssignment", func() {
	Description("The builder's assignment for the project's open build: build this change at this revision in working_folder, your own working copy of the project on the build's own branch, where generate_code, check_project and update_dependencies act and where verify_change looks for the candidate.")
	Attribute("change_id", String, "Change Card to build; get_change takes it.")
	Attribute("request_revision", String, "Change Card revision to build; verify_change takes it.")
	Attribute("in_force", Boolean, "True when request_revision is the change's agreed revision, the one in force, as get_build reports it. It stays true while a proposed revision waits and when a refreshed draft is adopted without changing request_revision. If adoption or confirmation changes the agreed request_revision, it becomes false; verify_change refuses this earlier revision, and the assignment stays as it is until the user builds again.")
	Attribute("read", Boolean, "True once you read this assignment in full with get_builder_assignment.")
	OneOf("written_tests", "Where the test writer's tests for this build stand: not_used when no test writer is used because the project has stored verification settings; otherwise accepted, with the commit and time, when the build records accepted tests, and not_yet_accepted when it records none.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "state")
		Attribute("not_used", TestsNotUsed)
		Attribute("not_yet_accepted", TestsNotYetAccepted)
		Attribute("accepted", TestsAccepted)
	})
	Attribute("working_folder", String, "Absolute path of the folder to build in: the project's folder inside your working copy .surla-build, on the build's branch surla/build-<n>. Run git and go there, never in the project folder.")
	Attribute("gowork_off", Boolean, "True when no go.work lies in working_folder or in a folder between it and the working copy's root: run every go command there as GOWORK=off go …, so a go.work of the project folder around your copy does not take it over.")
	Required("change_id", "request_revision", "in_force", "read", "working_folder", "gowork_off", "written_tests")
})

// BuilderAssignmentList lists the builder's assignments.
var BuilderAssignmentList = Type("BuilderAssignmentList", func() {
	Description("The builder's assignment for the project's open build while that build needs the builder; empty otherwise.")
	Attribute("assignments", ArrayOf(BuilderAssignment), "The open build's assignment while it needs this role; empty otherwise.")
	Required("assignments")
})

// BuilderAssignmentWait is the builder's assignment list when a wait ends.
var BuilderAssignmentWait = Type("BuilderAssignmentWait", func() {
	Description("The builder's assignment list when wait_for_builder_assignment returned, with the version that names it.")
	Attribute("assignments", ArrayOf(BuilderAssignment), "The open build's assignment while it needs this role, exactly as list_builder_assignments returns it; empty otherwise.")
	Attribute("version", String, "Opaque value naming this list. Pass it as since_version to wait until the list differs; compare it only for equality.")
	Required("assignments", "version")
})

// TestWriterAssignmentWait is the test writer's assignment list when a wait ends.
var TestWriterAssignmentWait = Type("TestWriterAssignmentWait", func() {
	Description("The test writer's assignment list when wait_for_test_writer_assignment returned, with the version that names it.")
	Attribute("assignments", ArrayOf(TestWriterAssignmentItem), "The open build's assignment while it needs this role, exactly as list_test_writer_assignments returns it; empty otherwise.")
	Attribute("version", String, "Opaque value naming this list. Pass it as since_version to wait until the list differs; compare it only for equality.")
	Required("assignments", "version")
})

// RequiredTest names one test a build's Story requires.
var RequiredTest = Type("RequiredTest", func() {
	Description("One test the agreed Story requires: the evaluation scenario Surla generates for one finished flow and the hook method the test writer implements for it.")
	Attribute("story", String, "Name of the Story the flow belongs to.")
	Attribute("flow_key", String, "Key of the finished flow.")
	Attribute("scenario", String, "Scenario name the evaluation suite declares for the flow.")
	Attribute("hook", String, "Hook method the test writer implements for the scenario.")
	Required("story", "flow_key", "scenario", "hook")
})

// ConfirmedAnswer is one question of the agreed revision with the answer the
// user confirmed for it.
var ConfirmedAnswer = Type("ConfirmedAnswer", func() {
	Description("One question of the change as agreed at the built revision, with the option the user confirmed for it.")
	Attribute("id", String, "Question identifier on the Change Card.")
	Attribute("question", String, "The question as the user answered it.")
	Attribute("answer", String, "The option the user confirmed, exactly as confirmed.")
	Required("id", "question", "answer")
})

// TestWriterAssignmentItem summarizes one of the test writer's assignments.
var TestWriterAssignmentItem = Type("TestWriterAssignmentItem", func() {
	Description("One of the test writer's assignments as list_test_writer_assignments reports it.")
	Attribute("change_id", String, "Change Card the tests are for; get_test_writer_assignment takes it.")
	Attribute("request_revision", String, "Change Card revision the tests are for.")
	Attribute("in_force", Boolean, "True when request_revision is the change's agreed revision, the one in force, as get_build reports it. It stays true while a proposed revision waits and when a refreshed draft is adopted without changing request_revision. If adoption or confirmation changes the agreed request_revision, it becomes false; the assignment stays on the earlier revision until the user builds again.")
	Attribute("progress", TestWriterProgress, "Where this assignment stands.")
	Required("change_id", "request_revision", "in_force", "progress")
})

// TestWriterAssignmentList lists the test writer's assignments.
var TestWriterAssignmentList = Type("TestWriterAssignmentList", func() {
	Description("The test writer's assignment for the project's open build while that build needs the test writer; empty otherwise.")
	Attribute("assignments", ArrayOf(TestWriterAssignmentItem), "The open build's assignment while it needs this role; empty otherwise.")
	Required("assignments")
})

// TestWriterAssignment is the test writer's assignment for one build in full.
var TestWriterAssignment = Type("TestWriterAssignment", func() {
	Description("The test writer's assignment for one build: write the required tests for the Story as agreed at the built revision, in the test module (eval/) of the working folder, and make them hold the service to the user's confirmed answers, confirmation examples and preserved behaviors of that revision, which the assignment carries exactly as confirmed. Surla writes eval/design/story_tests.go, which declares the required tests, from the agreed Story and generates the module at each read that marks the assignment read and again at each report; the test writer leaves that file as it is. After changing the module's design, the test writer reports to have it generated again or runs surla gen -dir eval in the working folder first; regenerating keeps the required tests. go build ./... in eval/ checks the work before a report, and Surla's check of each report is the judge.")
	Attribute("change_id", String, "Change Card the tests are for.")
	Attribute("request_revision", String, "Change Card revision the tests are for; report_tests_finished takes it.")
	Attribute("in_force", Boolean, "True when request_revision is the change's agreed revision, the one in force, as get_build reports it. It stays true while a proposed revision waits and when a refreshed draft is adopted without changing request_revision. If adoption or confirmation changes the agreed request_revision, it becomes false; the assignment stays on the earlier revision until the user builds again.")
	Attribute("progress", TestWriterProgress, "Where this assignment stands.")
	Attribute("findings", ArrayOf(String), "Surla's findings, one problem per item, with no absolute path: as many as progress gives in finding_count when its state is needs_changes, and none in any other state. Fix them in working_folder, then report again.")
	Attribute("working_folder", String, "Folder to write the tests in: your own working copy of the project, .surla-tests, on the build's branch surla/tests-<n>. Do not commit there: Surla commits what it accepts.")
	Attribute("design_source", String, "The canonical design source as agreed at request_revision, copied when this build started; later revisions do not change it.")
	Attribute("stories", ArrayOf(StorySummaryItem), "The Story as agreed at request_revision, in declaration order. It does not change when the change is revised later.")
	Attribute("required_tests", ArrayOf(RequiredTest), "One test per finished flow of stories, in declaration order; unfinished flows require none.")
	Attribute("answers", ArrayOf(ConfirmedAnswer), "Each question of the change as agreed at request_revision with the answer the user confirmed, in question order, copied when this build started; empty when the agreed revision asked none. The tests must hold the service to every answer.")
	Attribute("examples", ArrayOf(ExampleItem), "The confirmation examples as agreed at request_revision, exactly as the user confirmed them, copied when this build started. The tests must hold the service to each expected outcome.")
	Attribute("preserved_behaviors", ArrayOf(String), "The existing behaviors the change as agreed at request_revision keeps, copied when this build started; empty when it names none. The tests must hold the service to each.")
	Attribute("coverage_statements", ArrayOfRequired(TestCoverageStatement), "Statements submitted with the latest report, retained for correction after findings or a check that could not run; empty before any report or when none were stated.")
	Required("change_id", "request_revision", "in_force", "progress", "findings", "working_folder", "design_source", "stories", "required_tests", "answers", "examples", "preserved_behaviors", "coverage_statements")
})

// TestFindings is the text of Surla's findings on one build's tests.
var TestFindings = Type("TestFindings", func() {
	Description("The text of Surla's findings on a build's tests, with the build and the report they belong to, for the user's page.")
	Attribute("change_id", String, "Change Card identifier.")
	Attribute("request_revision", String, "Change Card revision the build is for.")
	Attribute("started_at", String, "When the user started the build, as get_build reports it.", func() { Format(FormatDateTime) })
	Attribute("report", Int, "Number of the test writer's report that Surla checked, as get_build reports it; 0 when Surla found the problems while writing the required tests as the build started, before any report.", func() { Minimum(0) })
	Attribute("checked_at", String, "When Surla's check reached this verdict.", func() { Format(FormatDateTime) })
	Attribute("findings", ArrayOf(String), "What Surla found, one problem per item, with no absolute path.", func() { MinLength(1) })
	Required("change_id", "request_revision", "started_at", "report", "checked_at", "findings")
})

// buildMethods exposes starting and reading builds and the role-limited
// assignment operations of the builder and the test writer.
func buildMethods() {
	Method("start_build", func() {
		Description("The user starts building a confirmed change at its agreed revision, the one in force, on the project owner's bound team. A project has one open build. Building another change is refused with previous_change_open until the user has merged a passed run of the open build into the branch checked out in the project folder or dropped it with drop_build. Building the open build's own change again keeps its number and base and replaces its assignments; while no verification of it has passed, replaces must name the open build the page showed, else unfinished_build_open; once that build is merged or dropped, building its change again starts a new build. Surla first checks, without effects, merge or drop, a revision drafted before a drop, and the agreed design against the project's commit; numbers a new build one more than any surla/build-<n> or surla/tests-<n> branch and the open build; and saves it with its base, the project's commit, by one rename. Then it makes a missing working copy (.surla-build, and .surla-tests when a test writer is used) on the build's own branch and registers the builder and the test writer in their copies. Build moves no existing copy: each joins the build's branch when its worker first reads the assignment. A refused start saves nothing; after the save, a server stop leaves the build saved, and building again makes a missing copy and registers the roles. After the start's turns are released, bringing the team up has its own 45-second limit. get_build reads the saved build without this start's transient team outcome.")
		Payload(func() {
			Attribute("change_id", String, "Confirmed Change Card identifier.", func() { MinLength(1) })
			Attribute("request_revision", String, "The Change Card's agreed revision, the one in force (not a waiting proposal's).", func() { MinLength(1) })
			Attribute("replaces", ReplacedBuild, "The open build of this same change the page showed, sent when the user agreed to build it again before any verification of it passed. Omit it otherwise.")
			Attribute("choices", MapOf(String, RoleAgentChoice, func() { Key(func() { Enum("builder", "test_writer") }) }), "Choices for missing workers. Omitted and empty both use saved choices then installed cross-family defaults. Never launches or changes the guide.")
			Required("change_id", "request_revision")
		})
		Result(StartBuildResult)
		buildStartRefusals()
		Error("unavailable", ErrorResult, "The server is stopping or the start's time limit ended before saving; call again on a running server. Nothing was saved.")
		Error("not_found", ErrorResult, "No Change Card exists with this change_id.")
		Error("conflict", ErrorResult, "The Change Card is not confirmed or request_revision is not its agreed revision (a waiting proposal's revision cannot be built), or the agreed revision records no design to build from; nothing changed.")
		Error("unprocessable", ErrorResult, "The agreed Story has a finished flow whose key gives its required test an empty name, or two finished flows whose required tests would have the same name; revise the Story. Nothing changed.")
		HTTP(func() {
			POST("/changes/{change_id}/build")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
			Response("conflict", StatusConflict)
			Response("unprocessable", StatusUnprocessableEntity)
			Response("unavailable", StatusServiceUnavailable)
			buildStartRefusalResponses()
		})
	})

	Method("drop_build", func() {
		Description("The user drops the project's open build on the views page because the change will not be taken: Surla builds nothing on it, and it no longer blocks starting another build. A change whose agreed revision was drafted before the drop is still refused with drafted_before_drop, which get_build's drafted_before_drop reports, until the guide refreshes the draft and revises the change and the user adopts the revision. The request names the build the page showed; a different open build (a second tab built or dropped meanwhile) is refused with its facts, and a build already dropped is refused. Drop writes one record, the start of the guide's made-from list naming the dropped build, by one rename: its only write. It changes no working copy, branch, draft or file of the user's: the build's commits stay on its branches, and the guide's next refresh_story makes the draft again from the project folder. From then on no role has work for the build, and the builder's and the test writer's tools tell them it was dropped. A merged build may be dropped too. The result is the dropped build as get_build reports it.")
		Payload(func() {
			Attribute("change_id", String, "change_id of the open build the page showed.", func() { MinLength(1) })
			Attribute("started_at", String, "started_at of the open build the page showed, exactly as get_build reported it.", func() { Format(FormatDateTime) })
			Required("change_id", "started_at")
		})
		Result(ChangeBuild)
		Error("not_found", ErrorResult, "No Change Card exists with this change_id, or the project has no open build.")
		Error("build_changed", OpenBuildRefusal, "The open build is not the one the page showed: a second tab built or dropped meanwhile. Nothing changed; the body names the open build.")
		Error("conflict", ErrorResult, "The build was already dropped; nothing changed.")
		HTTP(func() {
			POST("/changes/{change_id}/build/drop")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
			Response("build_changed", StatusConflict)
			Response("conflict", StatusConflict)
		})
	})

	Method("get_build", func() {
		Description("Return a change's build when it is the project's open build, with whether the user has taken it: building, passed with the commits to merge, merged, or dropped. not_started when it is not, including a builder-only build whose stored verification settings were removed; start a new build on the views page. Otherwise return the revision built, when, whether each role read its assignment, the builder's branch, while the build is still building its head_commit once the branch advances past the build's base, and its latest completed check_project verdict, the test writer's state with the number of Surla's findings (never their text), and whether the build is for the change's revision in force. Reading changes nothing; reading the branch tip and deciding merged read the project's repository.")
		Payload(func() {
			Attribute("change_id", String, "Change Card identifier.", func() { MinLength(1) })
			Required("change_id")
		})
		Result(ChangeBuild)
		Error("not_found", ErrorResult, "No Change Card exists with this change_id.")
		Tool("get_build", "Read a change's usable build for its current test source: the revision built, whether each role read its assignment, the builder's branch, its head_commit while the build is still building, its latest check_project verdict, the test writer's state with the number of Surla's findings, and whether the build is for the change's revision in force. not_started means there is no usable build; the user starts a new build on the views page.")
		HTTP(func() {
			GET("/changes/{change_id}/build")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
		})
	})

	Method("get_test_findings", func() {
		Description("Return the text of Surla's findings on the tests of a change's build, which get_build only counts, with the build's revision and start time, the number of the report Surla checked and when it reached the verdict. A build whose tests do not need changes is refused. Reading changes nothing.")
		Payload(func() {
			Attribute("change_id", String, "Change Card identifier.", func() { MinLength(1) })
			Required("change_id")
		})
		Result(TestFindings)
		Error("not_found", ErrorResult, "No Change Card exists with this change_id, or the change has no build.")
		Error("conflict", ErrorResult, "The build's tests do not need changes; get_build says what state they are in.")
		HTTP(func() {
			GET("/changes/{change_id}/build/findings")
			Response(StatusOK)
			Response("not_found", StatusNotFound)
			Response("conflict", StatusConflict)
		})
	})

	Method("list_builder_assignments", func() {
		Description("List the builder's assignment for the project's open build while that role has work; empty otherwise. Reading the list records nothing and moves no working copy.")
		Result(BuilderAssignmentList)
		Tool("list_builder_assignments", "List your assignment for the project's open build while it needs your work. An empty list means no build needs you: say so and stop. Read the assignment in full with get_builder_assignment(change_id) before working.")
		HTTP(func() {
			GET("/assignments/builder")
			Response(StatusOK)
		})
	})

	Method("wait_for_builder_assignment", func() {
		Description("Return the builder's assignment list, as list_builder_assignments does, with a version naming it. With since_version, the version an earlier call returned, the call waits until the list differs from it, for example because Surla accepted the tests, the user built the change again or dropped the build, or a verification of the build passed, and otherwise returns the unchanged list after a bounded wait of about 30 seconds. Without since_version it returns at once. It records nothing and moves no working copy. A builder uses it in place of repeated list calls while it waits, whether or not Chorus delivers reminders. Each call is traced but never saved in the diagnostics history list_traces reads, so a waiting builder does not push real operations out of view.")
		Payload(func() {
			Attribute("since_version", String, "version returned by your last wait_for_builder_assignment call. Omit it on the first call.", func() { MaxLength(128) })
		})
		Result(BuilderAssignmentWait)
		Tool("wait_for_builder_assignment", "Wait for your assignment list to change without reading it again and again. Call it once without since_version to get the list and its version; then pass that version as since_version. The call returns as soon as the list differs, for example when Surla accepts the tests (the state of written_tests becomes accepted) or the build no longer needs you (the list is empty: say so and stop), and otherwise after about 30 seconds with the same version: call it again with that version. Use it whenever you wait for the tests to be accepted. It needs no Chorus.")
		HTTP(func() {
			GET("/assignment-updates/builder")
			Param("since_version")
			Response(StatusOK)
		})
	})

	Method("get_builder_assignment", func() {
		Description("Return the builder's assignment for the open build's change in full and record that the builder read it at its revision. The first read of a new build joins the builder's working copy to the build's own branch, surla/build-<n> at the build's base: a copy with uncommitted work on an earlier build branch first gets that work committed there with the message \"Left uncommitted when change <change_id> started\"; a copy with nothing uncommitted joins from any branch. A copy with uncommitted work on any other branch, or a Git operation in progress, is refused with copy_off_branch, and a copy whose turn is held with working_copy_busy; nothing is marked read then. Building the change again makes the assignment unread.")
		Payload(func() {
			Attribute("change_id", String, "Change Card identifier from list_builder_assignments.", func() { MinLength(1) })
			Required("change_id")
		})
		Result(BuilderAssignment)
		Error("not_found", ErrorResult, "You have no build assignment for this change_id: it is not the change of the project's open build.")
		Error("no_build", ErrorResult, "The user dropped this change's build: stop work on it and make no more edits or commits for it. The user starts builds on the views page.")
		Error("copy_off_branch", CopyOffBranch, "Your working copy cannot join the build: it holds uncommitted work on a branch that is not an earlier build branch, or a Git operation is in progress. The message states the facts and the goal; put your work right with git, then read the assignment again.")
		Error("working_copy_busy", ErrorResult, "Surla is starting a build in your working folder, or files there changed while Surla moved it (nothing was lost); call get_builder_assignment again.")
		Error("working_folder_missing", ErrorResult, "Your working folder is missing; ask the user to build this change again on the views page.")
		Tool("get_builder_assignment", "Read your build assignment for a change before any other work: change_id and request_revision to build, and working_folder, your own working copy of the project. The first read of a new build puts your copy on the build's own branch. Then run git status in working_folder: a file it lists was left by earlier work, so do not commit it and tell the user. Verification of that revision judges the work: pass both to verify_change.")
		HTTP(func() {
			GET("/assignments/builder/{change_id}")
			Response(StatusOK)
			Response("no_build", StatusConflict)
			Response("copy_off_branch", StatusConflict)
			Response("working_copy_busy", StatusConflict)
			Response("working_folder_missing", StatusConflict)
			Response("not_found", StatusNotFound)
		})
	})

	Method("list_test_writer_assignments", func() {
		Description("List the test writer's assignment for the project's open build while that role has work; empty otherwise. Reading the list records nothing and moves no working copy.")
		Result(TestWriterAssignmentList)
		Tool("list_test_writer_assignments", "List your assignment for the project's open build while it needs your work. An empty list means no build needs you: say so and stop. Read the assignment in full with get_test_writer_assignment(change_id) before working.")
		HTTP(func() {
			GET("/assignments/test-writer")
			Response(StatusOK)
		})
	})

	Method("wait_for_test_writer_assignment", func() {
		Description("Return the test writer's assignment list, as list_test_writer_assignments does, with a version naming it. With since_version, the version an earlier call returned, the call waits until the list differs from it, for example because Surla's check of a report reached a verdict, the user built the change again or dropped the build, and otherwise returns the unchanged list after a bounded wait of about 30 seconds. The list holds the assignment only while the role has work: a check that accepts the tests removes it, as a drop does, so an empty list alone does not say which happened; get_test_writer_assignment does. Without since_version it returns at once. It records nothing and moves no working copy. A test writer uses it in place of repeated list calls while it waits, whether or not Chorus delivers reminders. Each call is traced but never saved in the diagnostics history list_traces reads, so a waiting test writer does not push real operations out of view.")
		Payload(func() {
			Attribute("since_version", String, "version returned by your last wait_for_test_writer_assignment call. Omit it on the first call.", func() { MaxLength(128) })
		})
		Result(TestWriterAssignmentWait)
		Tool("wait_for_test_writer_assignment", "Wait for your assignment list to change without reading it again and again. Call it once without since_version to get the list and its version; then pass that version as since_version. The call returns as soon as the list differs and otherwise after about 30 seconds with the same version. Use it after report_tests_finished while you wait for the verdict, and decide by what the list shows, not by whether it changed: Surla may reach its verdict before your first call. While the list still shows your assignment with the tests' state reported, call again with the version it returned. As soon as it shows anything else, another state or no assignment at all, call get_test_writer_assignment(change_id) for what happened: the list is empty after Surla accepted your tests and after the user dropped the build, and get_test_writer_assignment returns the accepted state, findings to fix, the no_build refusal after a drop, or a new build's assignment to read. It needs no Chorus.")
		HTTP(func() {
			GET("/assignment-updates/test-writer")
			Param("since_version")
			Response(StatusOK)
		})
	})

	Method("get_test_writer_assignment", func() {
		Description("Return the test writer's assignment for a change in full (the working folder, the design and Story as agreed at the built revision, the user's confirmed answers, confirmation examples and preserved behaviors of that revision, the tests it requires, and the state with the text of any findings) and record that the test writer read it at its revision. Later revisions do not change what the build saved. A new build of the change replaces the assignment, which then counts as unread, and so does each verdict of Surla's check. The first read of a new build joins your working copy to the build's branch, surla/tests-<n> at the build's base. A read that marks the assignment read also writes the required tests' generated folder into your copy. A copy with uncommitted work on an earlier tests build branch first gets that work committed there; a clean copy joins from any branch. Other uncommitted work or a Git operation is refused with copy_off_branch; nothing is marked read then.")
		Payload(func() {
			Attribute("change_id", String, "Change Card identifier from list_test_writer_assignments.", func() { MinLength(1) })
			Required("change_id")
		})
		Result(TestWriterAssignment)
		Error("not_found", ErrorResult, "You have no test assignment for this change_id.")
		Error("no_build", ErrorResult, "The user dropped this change's build: stop work on it and make no more edits or commits for it. The user starts builds on the views page.")
		Error("copy_off_branch", CopyOffBranch, "Your working copy cannot join or serve this build: the message states its branch, the build's branch, any Git operation and what is uncommitted, and the goal. Put your test files on the build's branch, uncommitted, because Surla commits what it accepts; then call again.")
		Error("working_folder_missing", ErrorResult, "Your working folder is missing; ask the user to build this change again on the views page.")
		Error("working_copy_busy", ErrorResult, "Surla is checking the tests in your working folder; call get_test_writer_assignment again when the check has ended.")
		Tool("get_test_writer_assignment", "Read your test assignment for a change: the working folder, the design and Story as agreed, the user's confirmed answers, examples and preserved behaviors, one required test per finished flow, and your state with any findings to fix. Your tests must hold the service to every answer, example and preserved behavior, not only to the Story's flows. Align the service interface in your copy with the agreed design and write tests inside its test module, eval/ in the working folder. Surla writes eval/design/story_tests.go, which declares the required tests, from the agreed Story and generates the module at each read that marks the assignment read and again at each report; leave that file as it is. After changing the module's design, report to have it generated again or run surla gen -dir eval in the working folder first; regenerating keeps the required tests. Check your work with GOWORK=off go build ./... in eval/, then call report_tests_finished with change_id, request_revision and coverage_statements; Surla's check is the judge, and its verdict appears here. Never build or start the application itself, which is every program under cmd/, and never run your tests against it: Surla's verification builds and runs the application. Stay on the branch checked out in your working copy and do not commit: Surla commits what it accepts. Run every go command as GOWORK=off go …, as Surla's tests check does. When you call report_tests_finished, include coverage_statements. For each scenario whose coverage you state, give its exact scenario ID, question_ids from this assignment's answers[].id, example_ids from examples[].id, and an explanation of the interaction or assertion that exercises them. Use an empty array when you state none; Surla will show Not stated. These are your statements, not Surla's judgment of what your tests assert. The tests must still hold the service to every confirmed answer, example and preserved behavior. Surla validates supplied references while checking your report. Read get_test_writer_assignment for the verdict and the statements you submitted; correct any stated-reference findings and report again. Do not commit: Surla records the tests it accepts.")
		HTTP(func() {
			GET("/assignments/test-writer/{change_id}")
			Response(StatusOK)
			Response("no_build", StatusConflict)
			Response("copy_off_branch", StatusConflict)
			Response("working_copy_busy", StatusConflict)
			Response("working_folder_missing", StatusConflict)
			Response("not_found", StatusNotFound)
		})
	})

	Method("report_tests_finished", func() {
		Description("The test writer reports that its tests for a change at the build's revision are finished. Every report raises the build's report number by one and makes the state reported, from any state, clearing an earlier verdict, and returns at once. Surla then checks the tests in the working folder against the build's Story; its verdict, needs_changes, accepted or could_not_run, replaces reported in the test writer's assignment, which then counts as unread. A report for another revision, or in a project with stored verification settings, where no test writer is used, is refused and changes nothing.")
		Payload(func() {
			Attribute("change_id", String, "Change Card identifier from your test assignment.", func() { MinLength(1) })
			Attribute("request_revision", String, "request_revision of your test assignment.", func() { MinLength(1) })
			Attribute("coverage_statements", ArrayOfRequired(TestCoverageStatement), "Your stated scenario coverage of this assignment's answers and examples; use [] when none is stated. Surla validates references but does not judge assertion meaning.")
			Required("change_id", "request_revision", "coverage_statements")
		})
		Result(TestWriterAssignmentItem)
		Error("not_found", ErrorResult, "You have no test assignment for this change_id.")
		Error("conflict", ErrorResult, "request_revision is not the revision of your test assignment (read it again with get_test_writer_assignment), or no test writer is used because the project has stored verification settings. Nothing changed.")
		Error("no_build", ErrorResult, "The user dropped this change's build: stop work on it and make no more edits or commits for it. The user starts builds on the views page.")
		Error("copy_off_branch", CopyOffBranch, "Your working copy cannot join or serve this build: the message states its branch, the build's branch, any Git operation and what is uncommitted, and the goal. Put your test files on the build's branch, uncommitted, because Surla commits what it accepts; then call again.")
		Error("working_folder_missing", ErrorResult, "Your working folder is missing; ask the user to build this change again on the views page.")
		Error("assignment_unread", ErrorResult, "A build started after your last read of your assignment: stop your current work, then call get_test_writer_assignment for its change.")
		Error("invalid_coverage", ErrorResult, "A supplied coverage statement repeats a scenario or reference, names no requirement, or names a question/example outside the built agreement. Nothing changed.")
		Tool("report_tests_finished", "Report that your tests for the assignment's change_id and request_revision are finished. It returns at once with state reported and the new report number. Surla then checks your tests; read its verdict, with any findings to fix, in get_test_writer_assignment. When you call report_tests_finished, include coverage_statements. For each scenario whose coverage you state, give its exact scenario ID, question_ids from this assignment's answers[].id, example_ids from examples[].id, and an explanation of the interaction or assertion that exercises them. Use an empty array when you state none; Surla will show Not stated. These are your statements, not Surla's judgment of what your tests assert. The tests must still hold the service to every confirmed answer, example and preserved behavior. Surla validates supplied references while checking your report. Read get_test_writer_assignment for the verdict and the statements you submitted; correct any stated-reference findings and report again. Do not commit: Surla records the tests it accepts.")
		HTTP(func() {
			POST("/assignments/test-writer/{change_id}/finished")
			Response(StatusOK)
			Response("invalid_coverage", StatusBadRequest)
			Response("no_build", StatusConflict)
			Response("copy_off_branch", StatusConflict)
			Response("working_folder_missing", StatusConflict)
			Response("assignment_unread", StatusConflict)
			Response("not_found", StatusNotFound)
			Response("conflict", StatusConflict)
		})
	})
}
