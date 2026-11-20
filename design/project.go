// This file defines the HTTP-only project management and browser exchange boundary.
package design

import . "goa.design/goa/v3/dsl"

// ProjectIdentity identifies the project reported by the answering server.
var ProjectIdentity = Type("ProjectIdentity", func() {
	Attribute("project_id", String, "Stable private project identifier.")
	Attribute("directory", String, "Physical project folder.")
	Attribute("owner", String, "Immutable operating-system account identity.")
	Attribute("build", String, "SHA-256 of the surla executable the server started from, read when it started; it tells any two builds apart, including two local builds of one commit. The CLI attaches only to its own build. Servers built before this field omit it, and surla stop must still reach them, so it is not required.", func() {
		Pattern("^[0-9a-f]{64}$")
	})
	Required("project_id", "directory", "owner")
})

// ProjectTeamRole is the observed state of one server-owned role.
var ProjectTeamRole = Type("ProjectTeamRole", func() {
	Attribute("role", String, "Trusted role.", func() { Enum("guide", "builder", "test_writer") })
	Attribute("handle", String, "Stable Chorus handle and Surla actor identity.")
	Attribute("running", Boolean, "The matching live terminal is not reported as an idle shell; missing foreground information counts as running.")
	Required("role", "handle", "running")
})

// ProjectTeam describes the server-owned team and its execution environment.
var ProjectTeam = Type("ProjectTeam", func() {
	Attribute("team", String, "Stable Chorus team name.", func() { Pattern("^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$") })
	Attribute("space", String, "Local Herdr space identifier, empty when Herdr is unavailable.", func() {
		Pattern(`^(?:[A-Za-z0-9_-][A-Za-z0-9_.-]{0,63}|\.[A-Za-z0-9_-][A-Za-z0-9_.-]{0,62}|\.\.[A-Za-z0-9_.-]{1,62})?$`)
		MaxLength(64)
	})
	Attribute("roles", ArrayOf(ProjectTeamRole))
	Attribute("herdr_mode", String, "Whether Herdr hosts this project, is available, or is unavailable.", func() { Enum("attached", "available", "unavailable") })
	Attribute("step", String, "What the user does next for the prepared team.", func() { MinLength(1); MaxLength(512) })
	Required("team", "space", "roles", "herdr_mode", "step")
})

// ProjectStatus is the read-only project state consumed by CLI and companion.
var ProjectStatus = Type("ProjectStatus", func() {
	Attribute("project_name", String, "Physical project folder's base name.")
	Attribute("directory", String, "Physical project folder.")
	Attribute("team", String, "Stable project team name.")
	Attribute("herdr_mode", String, "Current desktop attachment.", func() { Enum("attached", "available", "unavailable") })
	Attribute("stage", String, "Current saved stage.", func() { Enum("describe", "agree", "build", "check", "approve", "merged") })
	Attribute("change_id", String, "Current change identifier, empty when none exists.")
	Attribute("change_title", String, "Current change title, empty when none exists.")
	Attribute("attention", String, "Action currently needed, empty when none.")
	Attribute("roles", ArrayOf(TeamRoleState), "Observed role states, without terminal addresses or credentials.")
	Required("project_name", "directory", "team", "herdr_mode", "stage", "change_id", "change_title", "attention", "roles")
})

// TeamSpaceConflict carries a safe count independently of peer-authored error messages.
var TeamSpaceConflict = Type("TeamSpaceConflict", func() {
	Attribute("name", String, "Error name.", func() { Meta("struct:error:name") })
	Attribute("count", Int, "Number of local spaces matching the team's name and physical folder.", func() { Minimum(2) })
	Required("name", "count")
})

// TeamUnavailable names which part of team preparation failed, so the CLI can give
// that part's own recovery step without printing peer-authored error text.
var TeamUnavailable = Type("TeamUnavailable", func() {
	Attribute("name", String, "Error name.", func() { Meta("struct:error:name") })
	Attribute("cause", String, "What could not be used: stopping (the server is shutting down), chorus (the managed Chorus server or its settings are unavailable), herdr (a Herdr command failed), saved_space (the project's saved Herdr space has an ID this version of Surla no longer accepts and is still open), roster_folder (Herdr's launcher roster folder could not be written), project_state (the project's private record or an agent credential could not be read or written), or role_folder (a role's working folder could not be read).", func() {
		Enum("stopping", "chorus", "herdr", "saved_space", "roster_folder", "project_state", "role_folder")
	})
	Required("name", "cause")
})

var _ = Service("project", func() {
	Description("Owns one local project's server identity, agent credentials, and one-use browser access. Management credentials last for one listening server and never authorize application decisions; besides this service, they may only hand over the model provider request settings of the shell running surla (the surla service's hand_over_model_settings). This service is not exposed as agent tools.")
	Error("forbidden", ErrorResult, "The trusted credential cannot perform this operation.")
	Error("unprocessable", ErrorResult, "The requested credential exchange does not exist or is invalid.")
	HTTP(func() {
		Path("/project")
		Response("forbidden", StatusForbidden)
		Response("unprocessable", StatusUnprocessableEntity)
	})
	Method("identity", func() {
		Description("The CLI checks the answering server reports its expected project identity using the current server-lifetime management credential.")
		Result(ProjectIdentity)
		HTTP(func() { GET("/identity"); Response(StatusOK) })
	})
	Method("ensure_team", func() {
		Description("Read existing project team metadata with the management credential. Creates no team, credential, space, roster or agent and opens no window. This management operation is never an MCP tool.")
		Result(ProjectTeam)
		HTTP(func() {
			POST("/team")
			Response(StatusOK)
		})
	})
	Method("attach_guide", func() {
		Description("Attach the invoking coding agent as this project's guide. A live Herdr terminal is adopted only when session lineage, model pane, local project directory, client family and labels match. Omit terminal identity outside Herdr. Never opens a launcher.")
		Payload(func() {
			Attribute("hc_terminal_id", String, "Invoking local Herdr terminal identity.", func() { MinLength(1) })
			Attribute("client", String, "Invoking coding-agent client.", func() { Enum("claude", "codex", "antigravity", "cursor", "gemini", "opencode") })
			Attribute("ancestor_pids", ArrayOf(Int, func() { Minimum(1) }), "Invoking process and ancestor OS process IDs.", func() { MaxLength(64) })
			Attribute("daemon_client_pid", Int, "Unique local client OS process ID in this project directory when stdio was spawned by a background daemon.", func() { Minimum(1) })
		})
		Result(func() {
			Attribute("team", String, "Stable project team name.")
			Attribute("handle", String, "Guide's registered Chorus handle.")
			Attribute("adopted_herdr", Boolean, "Whether the invoking terminal was adopted.")
			Attribute("herdr_mode", String, "Current desktop attachment.", func() { Enum("attached", "available", "unavailable") })
			Required("team", "handle", "adopted_herdr", "herdr_mode")
		})
		Error("team_unavailable", TeamUnavailable, "The guide could not be attached to this project.")
		HTTP(func() {
			POST("/attach-guide")
			Response(StatusOK)
			Response("team_unavailable", StatusServiceUnavailable)
		})
	})
	Method("status", func() {
		Description("Read existing project, saved change stage and observed role state. Creates nothing and starts no agent. Merge status uses the build owner's read-only Git checks, including a manual merge into the project branch.")
		Result(ProjectStatus)
		HTTP(func() { GET("/status"); Response(StatusOK) })
	})
	Method("stop", func() {
		Description("The CLI requests ordered server shutdown. Before its listener closes, the server revokes management access and withdraws its running record; an authorized stop response can finish.")
		HTTP(func() { POST("/stop"); Response(StatusAccepted) })
	})
	Method("browser_link", func() {
		Description("The CLI mints a short-lived single-use browser link. The URL is the only credential allowed in a browser-launch command line.")
		Result(func() {
			Attribute("url", String, "Same-address views URL carrying the single-use link in its fragment.")
			Attribute("expires_at", String, "Time after which exchange is refused.", func() { Format(FormatDateTime) })
			Required("url", "expires_at")
		})
		HTTP(func() { POST("/browser-link"); Response(StatusCreated) })
	})
	Method("exchange_browser_link", func() {
		Description("The browser exchanges the single-use link once for an in-memory user session. The session is never a cookie or stored on disk and is sent only to this server address.")
		Payload(func() {
			Attribute("link", String, "Unexpired single-use link value.", func() { MinLength(1); MaxLength(256) })
			Required("link")
		})
		Result(func() {
			Attribute("session", String, "User session kept only in the page's memory.")
			Attribute("project_name", String, "Base name of the physical project folder, without its path, for this page session.", func() { MinLength(1) })
			Required("session", "project_name")
		})
		HTTP(func() { POST("/browser-session"); Response(StatusCreated) })
	})
})
