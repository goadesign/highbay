// This file declares the browser workspace's team state, Chorus conversation,
// role lifecycle controls, and interactive herdr-core terminal WebSocket stream.
package design

import . "goa.design/goa/v3/dsl"

// TeamRoleState contains the observed runtime state of one project role.
var TeamRoleState = Type("TeamRoleState", func() {
	Description("Observed terminal, Chorus liveness, attention, and assignment state of one registered project role.")
	Attribute("role", String, "Registered role.", func() { Enum("guide", "builder", "test_writer") })
	Attribute("handle", String, "Registered Chorus handle.")
	Attribute("has_terminal", Boolean, "True when this role has a managed or attached herdr-core terminal that can be opened in the browser terminal drawer.")
	Attribute("state", String, "Current role state; unknown means herdr-core and Chorus cannot establish it.", func() {
		Enum("not_started", "ended", "starting", "needs_you", "working", "waiting", "error", "unknown")
	})
	Attribute("attention", String, "Reported block kind when the role needs the user (for example permission, question, auth, or silent after 60 seconds without progress).", func() {
		Enum("none", "permission", "question", "auth", "silent", "unknown")
	})
	Attribute("assignment", String, "unread when this worker owes an assignment read; nothing_owed when it has work and owes no read; no_work when the open build needs nothing from it; unknown when assignments could not be read. The guide has no assignments (no_work).", func() {
		Enum("unread", "nothing_owed", "no_work", "unknown")
	})
	Attribute("choice", RoleAgentChoice, "Configured coding-agent CLI selection for this role when saved.")
	Required("role", "handle", "has_terminal", "state", "attention", "assignment")
})

// TeamSnapshot is rebuilt in memory from the project registry, herdr-core, and Chorus.
var TeamSnapshot = Type("TeamSnapshot", func() {
	Description("Current project team roles, saved CLI choices, dismissed notices, and Chorus connectivity.")
	Attribute("team", String, "Stable Chorus swarm name for this project.")
	Attribute("channel", String, "Primary Chorus #channel for this project.")
	Attribute("roles", ArrayOf(TeamRoleState), "Registered roles in guide, builder, test_writer order; empty before registration.")
	Attribute("saved_choices", MapOf(String, RoleAgentChoice, roleAgentChoices), "Saved client choices by role.")
	Attribute("dismissed_notices", ArrayOf(String, func() { Enum("single_agent_family") }), "Dismissed project notices.")
	Attribute("chorus_status", String, "Whether Highbay's local Chorus connection is healthy.", func() {
		Enum("connected", "unavailable")
	})
	Required("team", "channel", "roles", "saved_choices", "dismissed_notices", "chorus_status")
})

// TeamMessage is one structured message in the project's Chorus conversation.
var TeamMessage = Type("TeamMessage", func() {
	Description("One message in the project's Chorus channel or direct-message thread.")
	Attribute("id", String, "Unique message key combining target and sequence (for example #refunds-8f3a1c902b4e:12).")
	Attribute("target", String, "Chorus target (#channel or @handle) carrying this message.")
	Attribute("seq", Int, "Monotonic sequence number within target.", func() { Minimum(1) })
	Attribute("sender", String, "Chorus handle of the sender.")
	Attribute("sender_role", String, "Project role of the sender for UI presentation.", func() {
		Enum("you", "guide", "builder", "test_writer", "service", "peer")
	})
	Attribute("model", String, "Reported model or participant kind (for example human, service, or model name).")
	Attribute("text", String, "Markdown message text.")
	Attribute("reply_to", Int, "Optional sequence number in target that this message replies to.", func() { Minimum(1) })
	Attribute("sent_at_ms", Int64, "Unix timestamp in milliseconds when Chorus recorded the message.")
	Required("id", "target", "seq", "sender", "sender_role", "text", "sent_at_ms")
})

// TeamConversation is the browser workspace's live Chorus conversation snapshot.
var TeamConversation = Type("TeamConversation", func() {
	Description("Ordered messages and pinned collaboration manifest for the project's Chorus team.")
	Attribute("team", String, "Stable Chorus swarm name.")
	Attribute("channel", String, "Primary Chorus #channel.")
	Attribute("chorus_status", String, "Whether Chorus responded to the latest conversation read.", func() {
		Enum("connected", "unavailable")
	})
	Attribute("pin", TeamMessage, "Pinned collaboration manifest in the primary #channel when set.")
	Attribute("messages", ArrayOf(TeamMessage), "Chronological messages across the project's #channel and @you direct messages.")
	Required("team", "channel", "chorus_status", "messages")
})

// ReviseAnswersContext carries selected ambiguity answers when asking the guide to revise a Change Card.
var ReviseAnswersContext = Type("ReviseAnswersContext", func() {
	Description("Selected Checkpoint 1 ambiguity answers that differ from the current draft_option and require a revised Change Card.")
	Attribute("change_id", String, "Change Card identifier being revised.", func() { MinLength(1) })
	Attribute("request_revision", String, "Current request_revision shown on the Agree view.", func() { MinLength(1) })
	Attribute("answers", MapOf(String, String), "Map of question ID to the user's chosen option.")
	Required("change_id", "request_revision", "answers")
})

// ArchitectureElementContext identifies a C4 diagram element the user is asking about or changing.
var ArchitectureElementContext = Type("ArchitectureElementContext", func() {
	Description("Selected C4 architecture element referenced by the user's message to the guide.")
	Attribute("change_id", String, "Change Card identifier when viewing a specific card's architecture.")
	Attribute("element_id", String, "Stable C4 model element identifier.", func() { MinLength(1) })
	Attribute("element_name", String, "Human-readable C4 element label.", func() { MinLength(1) })
	Required("element_id", "element_name")
})

// StoryFlowContext identifies a Story flow referenced by the user's message to the guide.
var StoryFlowContext = Type("StoryFlowContext", func() {
	Description("Selected Story and flow referenced by the user's message to the guide.")
	Attribute("change_id", String, "Change Card identifier when viewing a saved card's Story.")
	Attribute("story", String, "Story name.", func() { MinLength(1) })
	Attribute("flow_key", String, "Optional flow key within the Story.")
	Required("story")
})

// SendTeamMessagePayload is the user's browser request to message the team or a specific role.
var SendTeamMessagePayload = Type("SendTeamMessagePayload", func() {
	Description("User-authored message posted from the browser workspace to the project's Chorus channel or a specific role, starting the guide automatically when needed.")
	Attribute("to", String, "Recipient target: team posts to the primary #channel; guide, builder, or test_writer mentions that role in the primary #channel.", func() {
		Enum("team", "guide", "builder", "test_writer")
		Default("guide")
	})
	Attribute("text", String, "Plain-English or Markdown message text.", func() {
		MinLength(1)
		MaxLength(3500)
	})
	Attribute("reply_to", Int, "Optional sequence number in the primary #channel being replied to.", func() { Minimum(1) })
	Attribute("guide_choice", RoleAgentChoice, "Optional coding-agent CLI selection used if Highbay starts a missing guide for this message.")
	OneOf("context", "Optional structured workspace context formatted into the Chorus message by the server.", func() {
		Attribute("revise_answers", ReviseAnswersContext)
		Attribute("architecture_element", ArchitectureElementContext)
		Attribute("story_flow", StoryFlowContext)
	})
	Required("to", "text")
})

// TerminalTicketResult is a short-lived single-use credential authorizing one WebSocket terminal upgrade.
var TerminalTicketResult = Type("TerminalTicketResult", func() {
	Description("Single-use short-lived ticket passed on the attach_role_terminal WebSocket upgrade query.")
	Attribute("ticket", String, "Opaque single-use ticket valid for at most 30 seconds.", func() { MinLength(16) })
	Attribute("expires_at", String, "UTC expiration timestamp of the ticket.", func() { Format(FormatDateTime) })
	Required("ticket", "expires_at")
})

// TerminalResize carries terminal grid dimensions in columns and rows.
var TerminalResize = Type("TerminalResize", func() {
	Description("Terminal viewport dimensions in character cells.")
	Attribute("cols", Int, "Terminal width in columns.", func() { Minimum(20); Maximum(500) })
	Attribute("rows", Int, "Terminal height in rows.", func() { Minimum(5); Maximum(250) })
	Required("cols", "rows")
})

// TerminalClosed reports why an attached herdr-core terminal stream ended.
var TerminalClosed = Type("TerminalClosed", func() {
	Description("Reason a herdr-core terminal attach stream ended, including process exit code or signal when finished.")
	Attribute("reason", String, "Stream end reason reported by herdr-core (for example detached, exited, killed, or lost).")
	Attribute("exit_code", Int, "Process exit status when the terminal process exited normally.", func() { Minimum(0); Maximum(255) })
	Attribute("signal", Int, "Terminating signal number when the terminal process was ended by a signal.", func() { Minimum(1); Maximum(255) })
	Required("reason")
})

// TerminalInputFrame carries raw terminal input bytes typed by the user.
var TerminalInputFrame = Type("TerminalInputFrame", func() {
	Description("Raw UTF-8 or escape-sequence bytes typed by the user in xterm.js.")
	Attribute("data", Bytes, "Raw terminal input bytes.")
	Required("data")
})

// TerminalFocusFrame carries terminal drawer focus state changes.
var TerminalFocusFrame = Type("TerminalFocusFrame", func() {
	Description("Browser terminal drawer focus change.")
	Attribute("focused", Boolean, "Whether the terminal drawer currently holds focus.")
	Required("focused")
})

// TerminalCreditFrame replenishes the herdr-core stream's 256 KiB flow-control window.
var TerminalCreditFrame = Type("TerminalCreditFrame", func() {
	Description("Byte count rendered by xterm.js, replenishing the herdr-core stream's 256 KiB flow-control window.")
	Attribute("bytes", Int, "Number of output bytes consumed by xterm.js.", func() { Minimum(1); Maximum(16777216) })
	Required("bytes")
})

// TerminalOutputFrame carries raw PTY output bytes emitted by herdr-core.
var TerminalOutputFrame = Type("TerminalOutputFrame", func() {
	Description("Raw VT output bytes emitted by herdr-core for xterm.js.")
	Attribute("data", Bytes, "Raw terminal output bytes.")
	Required("data")
})

// TerminalClientFrame is one browser-to-server frame on the terminal WebSocket.
var TerminalClientFrame = Type("TerminalClientFrame", func() {
	Description("Client-to-server WebSocket message carrying user keystrokes, viewport resize, focus state, or flow-control credit.")
	OneOf("client_frame", "Exactly one client terminal event.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "kind")
		Attribute("input", TerminalInputFrame)
		Attribute("resize", TerminalResize)
		Attribute("focus", TerminalFocusFrame)
		Attribute("credit", TerminalCreditFrame)
	})
	Required("client_frame")
})

// TerminalServerFrame is one server-to-browser frame on the terminal WebSocket.
var TerminalServerFrame = Type("TerminalServerFrame", func() {
	Description("Server-to-client WebSocket message carrying raw PTY output bytes, external grid resizes, or stream termination.")
	OneOf("server_frame", "Exactly one server terminal event.", func() {
		Meta("oneof:json:flatten")
		Meta("oneof:type:field", "kind")
		Attribute("output", TerminalOutputFrame)
		Attribute("resize", TerminalResize)
		Attribute("closed", TerminalClosed)
	})
	Required("server_frame")
})

// teamMethods exposes browser-only team management, Chorus conversation, and live terminal streaming.
func teamMethods() {
	Method("get_team", func() {
		Description("Read the project team's registered roles and current herdr-core terminal, Chorus liveness, and unread-assignment facts. Reading creates nothing and marks no assignment read. Only the user's browser session may call it; wait_for_changes covers this snapshot.")
		Result(TeamSnapshot)
		HTTP(func() {
			GET("/team")
			Response(StatusOK)
		})
	})
	Method("get_team_conversation", func() {
		Description("Read the project's Chorus conversation messages and active pin for the browser workspace's Conversation Rail. Only the user's browser session may call it; wait_for_changes advances whenever new Chorus messages arrive.")
		Result(TeamConversation)
		HTTP(func() {
			GET("/team/conversation")
			Response(StatusOK)
		})
	})
	Method("send_team_message", func() {
		Description("Post a message from the user in the browser workspace to the project's Chorus channel (mentioning the target role when directed). If the message targets guide or team and no guide is currently active, Highbay starts a managed guide terminal in herdr-core first so the user never leaves the browser.")
		Payload(SendTeamMessagePayload)
		Result(TeamMessage)
		Error("team_unavailable", ErrorResult, "Chorus or herdr-core could not accept the message or start the guide.")
		Error("unprocessable", ErrorResult, "The selected guide client is not installed, does not support the requested options, or the referenced Change Card context is invalid.")
		HTTP(func() {
			POST("/team/messages")
			Response(StatusCreated)
			Response("team_unavailable", StatusServiceUnavailable)
			Response("unprocessable", StatusUnprocessableEntity)
		})
	})
	Method("start_team_agents", func() {
		Description("Start missing project agents in herdr-core using explicit choices, otherwise saved choices and installed cross-family defaults. Never replaces a running guide. Only the user's browser session may call it.")
		Payload(func() {
			Attribute("choices", MapOf(String, RoleAgentChoice, roleAgentChoices), "Choices for missing roles; guide is accepted only when not running. Omitted and empty choices both use saved choices and installed defaults.")
		})
		Result(ProjectTeam)
		Error("team_unavailable", ErrorResult, "The project team could not be prepared in herdr-core or Chorus.")
		Error("unprocessable", ErrorResult, "A selected client is not installed, does not support the requested options, or the supplied guide choice would replace a running guide.")
		HTTP(func() {
			POST("/team/agents")
			Response(StatusOK)
			Response("team_unavailable", StatusServiceUnavailable)
			Response("unprocessable", StatusUnprocessableEntity)
		})
	})
	Method("interrupt_team_role", func() {
		Description("Send an interrupt signal (SIGINT / Ctrl-C) to a running role's herdr-core terminal at the user's request.")
		Payload(func() {
			Attribute("role", String, "Registered role to interrupt.", func() { Enum("guide", "builder", "test_writer") })
			Required("role")
		})
		Error("role_not_running", ErrorResult, "The requested role has no running herdr-core terminal to interrupt.")
		Error("team_unavailable", ErrorResult, "herdr-core could not interrupt the terminal.")
		HTTP(func() {
			POST("/team/roles/{role}/interrupt")
			Response(StatusNoContent)
			Response("role_not_running", StatusConflict)
			Response("team_unavailable", StatusServiceUnavailable)
		})
	})
	Method("stop_team_role", func() {
		Description("Terminate and remove a role's managed herdr-core terminal at the user's request so it can be restarted cleanly.")
		Payload(func() {
			Attribute("role", String, "Registered role to stop.", func() { Enum("guide", "builder", "test_writer") })
			Required("role")
		})
		Error("role_not_running", ErrorResult, "The requested role has no managed herdr-core terminal.")
		Error("team_unavailable", ErrorResult, "herdr-core could not stop the terminal.")
		HTTP(func() {
			POST("/team/roles/{role}/stop")
			Response(StatusNoContent)
			Response("role_not_running", StatusConflict)
			Response("team_unavailable", StatusServiceUnavailable)
		})
	})
	Method("restart_team_role", func() {
		Description("Atomically stop and remove a role's existing managed herdr-core terminal (when present) and launch a fresh managed herdr-core terminal for that role using the supplied choice, saved choice, or installed default. External non-managed sessions are never replaced.")
		Payload(func() {
			Attribute("role", String, "Registered role whose managed herdr-core terminal is being restarted.", func() { Enum("guide", "builder", "test_writer") })
			Attribute("choice", RoleAgentChoice, "Optional coding-agent CLI selection for the restarted terminal; when omitted, uses the role's saved choice or installed default.")
			Required("role")
		})
		Error("role_not_running", ErrorResult, "The requested role is attached to an external non-managed session rather than a managed herdr-core terminal.")
		Error("unprocessable", ErrorResult, "The selected coding-agent client is not installed or does not support the requested options.")
		Error("team_unavailable", ErrorResult, "herdr-core or Chorus could not stop or start the role's terminal.")
		HTTP(func() {
			POST("/team/roles/{role}/restart")
			Response(StatusNoContent)
			Response("role_not_running", StatusConflict)
			Response("unprocessable", StatusUnprocessableEntity)
			Response("team_unavailable", StatusServiceUnavailable)
		})
	})
	Method("create_terminal_ticket", func() {
		Description("Mint a single-use 30-second WebSocket upgrade ticket for attaching the browser terminal drawer to a role's herdr-core terminal.")
		Payload(func() {
			Attribute("role", String, "Registered role whose terminal drawer is opening.", func() { Enum("guide", "builder", "test_writer") })
			Required("role")
		})
		Result(TerminalTicketResult)
		Error("terminal_unavailable", ErrorResult, "The requested role has no live herdr-core terminal to attach.")
		HTTP(func() {
			POST("/team/roles/{role}/terminal-ticket")
			Response(StatusCreated)
			Response("terminal_unavailable", StatusConflict)
		})
	})
	Method("attach_role_terminal", func() {
		Description("Upgrade to a bidirectional WebSocket attached to a role's live herdr-core PTY with 256 KiB credit flow control.")
		Payload(func() {
			Attribute("role", String, "Registered role whose herdr-core terminal is being attached.", func() { Enum("guide", "builder", "test_writer") })
			Attribute("ticket", String, "Single-use ticket returned by create_terminal_ticket.", func() { MinLength(16) })
			Attribute("cols", Int, "Initial terminal width in columns.", func() { Minimum(20); Maximum(500); Default(100) })
			Attribute("rows", Int, "Initial terminal height in rows.", func() { Minimum(5); Maximum(250); Default(30) })
			Attribute("observe", Boolean, "When true, attaches read-only without changing the terminal's grid size or sending input.", func() { Default(false) })
			Required("role", "ticket")
		})
		StreamingPayload(TerminalClientFrame)
		StreamingResult(TerminalServerFrame)
		Error("terminal_unavailable", ErrorResult, "The requested role has no live herdr-core terminal to attach.")
		Error("unprocessable", ErrorResult, "The supplied terminal ticket is expired, invalid, or already consumed.")
		HTTP(func() {
			GET("/team/roles/{role}/terminal")
			Param("ticket")
			Param("cols")
			Param("rows")
			Param("observe")
			Response(StatusOK)
			Response("terminal_unavailable", StatusConflict)
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
			Attribute("notice", String, "Notice to dismiss.", func() { Enum("single_agent_family") })
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
