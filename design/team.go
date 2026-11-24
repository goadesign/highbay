// This file declares the user's read and actions for the server-owned team.
package design

import . "goa.design/goa/v3/dsl"

// TeamRoleState contains only the facts the user needs to decide whether to open a pane.
var TeamRoleState = Type("TeamRoleState", func() {
	Attribute("role", String, "Registered role.", func() { Enum("guide", "builder", "test_writer") })
	Attribute("handle", String, "Registered Chorus handle.")
	Attribute("state", String, "Current terminal state; unknown means Herdr cannot establish it.", func() {
		Enum("not_started", "ended", "starting", "needs_you", "working", "waiting", "error", "unknown")
	})
	Attribute("attention", String, "Reported block kind, or why a running agent needs the user: silent when it reports nothing for 60 seconds, assignment when it has waited with an unread assignment for 90 seconds, or for 15 seconds while Chorus is not known to be reachable and so no reminder can arrive.", func() {
		Enum("none", "permission", "question", "auth", "silent", "assignment", "unknown")
	})
	Attribute("watcher", String, "held when Chorus reports a watcher, gone after its absence grace, unknown otherwise.", func() { Enum("held", "gone", "unknown") })
	Attribute("assignment", String, "unread when this role owes an assignment read; nothing_owed when it has work and owes no read; no_work when the open build needs nothing from it (no build is open, the build was dropped, the test writer's tests are accepted, or a verification of the builder's build passed); unknown when the owner could not read assignments. The guide has no assignments.", func() { Enum("unread", "nothing_owed", "no_work", "unknown") })
	Required("role", "handle", "state", "attention", "watcher", "assignment")
})

// TeamSnapshot is rebuilt in memory from the project registry and outside observations.
var TeamSnapshot = Type("TeamSnapshot", func() {
	Attribute("team", String, "Stable team name, also returned before a team is prepared.")
	Attribute("roles", ArrayOf(TeamRoleState), "Registered roles in guide, builder, test_writer order; empty before registration.")
	Attribute("herdr_mode", String, "Current desktop attachment.", func() { Enum("attached", "available", "unavailable") })
	Attribute("saved_choices", MapOf(String, RoleAgentChoice, roleAgentChoices), "Saved client choices by role.")
	Attribute("dismissed_notices", ArrayOf(String, func() { Enum("single_agent_family", "outside_herdr", "herdr_unavailable") }), "Dismissed project notices.")
	Attribute("chorus_status", String, "Whether Chorus serves this team: not_used when workers run outside Herdr and never use it; connected after Surla's latest read of the team's Chorus view succeeded; unavailable after that read failed or when the team's space has no Chorus connection. While unavailable no Chorus reminder reaches a worker: workers wait on Surla with wait_for_builder_assignment and wait_for_test_writer_assignment, and a worker that stopped needs the user's prompt. Absent before a team space exists or before the first read settled it.", func() {
		Enum("connected", "unavailable", "not_used")
	})
	Required("team", "roles", "herdr_mode", "saved_choices", "dismissed_notices")
})

// teamMethods deliberately exposes no agent MCP tools and no second waiting read.
func teamMethods() {
	Method("get_team", func() {
		Description("Read the project team's registered roles and current terminal, watcher and unread-assignment facts. Reading creates nothing and marks no assignment read. Only the user's browser session may call it. wait_for_changes covers this snapshot as well as the change list.")
		Result(TeamSnapshot)
		HTTP(func() {
			GET("/team")
			Response(StatusOK)
		})
	})
	Method("show_team_pane", func() {
		Description("Show this project's space and focus the requested registered role's pane, only at the user's request. Any open Herdr dialog prevents all window changes. Fresh project, handle and space labels must still identify the pane.")
		Payload(func() {
			Attribute("role", String, "Registered role whose pane the user requests.", func() { Enum("guide", "builder", "test_writer") })
			Required("role")
		})
		Error("window_busy", ErrorResult, "Close the open Herdr dialog before showing a pane.")
		Error("pane_unavailable", ErrorResult, "The requested registered role has no owned pane to show.")
		Error("team_unavailable", ErrorResult, "Herdr could not establish the owned pane.")
		HTTP(func() {
			POST("/team/pane")
			Response(StatusNoContent)
			Response("window_busy", StatusConflict)
			Response("pane_unavailable", StatusConflict)
			Response("team_unavailable", StatusServiceUnavailable)
		})
	})
	Method("start_team_agents", func() {
		Description("Start missing project agents using explicit choices, otherwise saved choices and installed cross-family defaults. Never replace a running guide. Only the user may call it. Workers run in Herdr when available, otherwise as finite server-owned headless processes. Opens no launcher.")
		Payload(func() {
			Attribute("choices", MapOf(String, RoleAgentChoice, roleAgentChoices), "Choices for missing roles; guide is accepted only when not running. Omitted and empty choices both use saved choices and installed defaults.")
		})
		Result(ProjectTeam)
		Error("ambiguous_space", ErrorResult, "Several spaces match the project; close the duplicates before starting agents again.")
		Error("window_busy", ErrorResult, "Close the open Herdr dialog before starting agents.")
		Error("team_unavailable", ErrorResult, "The project team could not be prepared.")
		Error("unprocessable", ErrorResult, "A selected client is not installed, does not support the requested options, or the supplied guide choice would replace a running guide.")
		HTTP(func() {
			POST("/team/agents")
			Response(StatusOK)
			Response("ambiguous_space", StatusConflict)
			Response("window_busy", StatusConflict)
			Response("team_unavailable", StatusServiceUnavailable)
			Response("unprocessable", StatusUnprocessableEntity)
		})
	})
	Method("list_team_agent_options", func() {
		Description("Discover installed coding-agent executables and their supported options on demand. Only the user may call it; ordinary team reads never run discovery subprocesses.")
		Result(func() {
			Attribute("agents", ArrayOf(InstalledAgentOption), "Installed coding agents.")
			Attribute("build_roles", ArrayOf(String, func() { Enum("builder", "test_writer") }), "Worker roles a new build requires with the project's current verification settings.", func() { MinLength(1); MaxLength(2) })
			Required("agents", "build_roles")
		})
		Error("team_unavailable", ErrorResult, "Installed coding-agent discovery could not be completed.")
		HTTP(func() {
			GET("/team/agent-options")
			Response(StatusOK)
			Response("team_unavailable", StatusServiceUnavailable)
		})
	})
	Method("dismiss_team_notice", func() {
		Description("Save the user's dismissal of one project notice without changing agent capability or startup behavior.")
		Payload(func() {
			Attribute("notice", String, "Notice to dismiss.", func() { Enum("single_agent_family", "outside_herdr", "herdr_unavailable") })
			Required("notice")
		})
		Error("team_unavailable", ErrorResult, "The project notice preference could not be saved.")
		HTTP(func() {
			POST("/team/notices/dismiss")
			Response(StatusNoContent)
			Response("team_unavailable", StatusServiceUnavailable)
		})
	})
}
