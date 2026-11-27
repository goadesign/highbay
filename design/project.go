// This file defines the HTTP-only project management and browser session boundary.
package design

import . "goa.design/goa/v3/dsl"

// ProjectIdentity identifies the project reported by the answering server.
var ProjectIdentity = Type("ProjectIdentity", func() {
	Description("Identity of the local project and highbay executable served by this running project server.")
	Attribute("project_id", String, "Stable private project identifier.")
	Attribute("directory", String, "Physical project folder.")
	Attribute("owner", String, "Immutable operating-system account identity.")
	Attribute("build", String, "SHA-256 of the highbay executable the server started from, read when it started; it tells any two builds apart, including two local builds of one commit. The CLI attaches only to its own build. Servers built before this field omit it, and highbay stop must still reach them, so it is not required.", func() {
		Pattern("^[0-9a-f]{64}$")
	})
	Attribute("version", String, "Version reported by the running highbay executable (for example v0.5.0 or (devel)). Servers built before this field omit it so highbay stop and highbay update can still reach them.", func() {
		MinLength(1)
	})
	Required("project_id", "directory", "owner")
})

// ProjectTeamRole is the observed state of one server-owned role.
var ProjectTeamRole = Type("ProjectTeamRole", func() {
	Description("Observed identity and terminal liveness of one registered project role.")
	Attribute("role", String, "Trusted role.", func() { Enum("guide", "builder", "test_writer") })
	Attribute("handle", String, "Stable Chorus handle and Highbay actor identity.")
	Attribute("has_terminal", Boolean, "True when this role is backed by a live or owned herdr-core (hc) terminal.")
	Attribute("running", Boolean, "True when the role has a live hc agent terminal or active external Chorus presence.")
	Required("role", "handle", "has_terminal", "running")
})

// ProjectTeam describes the server-owned team and its execution environment.
var ProjectTeam = Type("ProjectTeam", func() {
	Description("Prepared project team name, role states, and next user step.")
	Attribute("team", String, "Stable Chorus team name.", func() { Pattern("^[A-Za-z0-9][A-Za-z0-9_-]{0,31}$") })
	Attribute("roles", ArrayOf(ProjectTeamRole), "Observed state of each registered project role.")
	Attribute("step", String, "What the user does next for the prepared team.", func() { MinLength(1); MaxLength(512) })
	Required("team", "roles", "step")
})

// ProjectStatus is the read-only project state consumed by the CLI.
var ProjectStatus = Type("ProjectStatus", func() {
	Description("Read-only summary of the project folder, team, current Change Card stage, and role states.")
	Attribute("project_name", String, "Physical project folder's base name.")
	Attribute("directory", String, "Physical project folder.")
	Attribute("team", String, "Stable project team name.")
	Attribute("stage", String, "Current saved stage.", func() { Enum("describe", "agree", "build", "check", "approve", "merged") })
	Attribute("change_id", String, "Current change identifier, empty when none exists.")
	Attribute("change_title", String, "Current change title, empty when none exists.")
	Attribute("attention", String, "Action currently needed, empty when none.")
	Attribute("roles", ArrayOf(TeamRoleState), "Observed role states, without terminal addresses or credentials.")
	Required("project_name", "directory", "team", "stage", "change_id", "change_title", "attention", "roles")
})

// BrowserSessionResult returns the origin-scoped Bearer session credential, project name, and UI/server build metadata.
var BrowserSessionResult = Type("BrowserSessionResult", func() {
	Description("Authenticated browser session credential, project folder name, server version, and inline UI script digest for this origin.")
	Attribute("session", String, "Bearer session credential stored in origin-scoped localStorage and sent in Authorization: Bearer.", func() {
		MinLength(16)
	})
	Attribute("project_name", String, "Base name of the physical project folder, without its path, for this page session.", func() {
		MinLength(1)
	})
	Attribute("version", String, "Version reported by this running highbay server (for example v0.5.0 or (devel)).", func() {
		MinLength(1)
	})
	Attribute("ui_build", String, "Base64 SHA-256 digest of the inline /views bundle served by this running server, allowing open tabs to detect UI changes across server restarts.", func() {
		MinLength(1)
	})
	Required("session", "project_name", "version", "ui_build")
})

// UpdateStatusResult reports whether a newer Highbay release is available and whether this server can install it in place.
var UpdateStatusResult = Type("UpdateStatusResult", func() {
	Description("Release update status for this running Highbay server and how an available update can be installed.")
	Attribute("current_version", String, "Version reported by this running highbay server (for example v0.5.0 or (devel)).", func() {
		MinLength(1)
	})
	Attribute("latest_version", String, "Newest stable Highbay release version published on GitHub (with leading v, for example v0.5.1).", func() {
		MinLength(1)
	})
	Attribute("update_available", Boolean, "True when latest_version is newer than current_version.")
	Attribute("release_url", String, "HTTPS URL of the latest GitHub release notes.", func() {
		MinLength(1)
	})
	Attribute("can_auto_update", Boolean, "True when this running server can install the release binary or Homebrew upgrade and restart in place.")
	Attribute("manual_command", String, "Shell command to update manually when can_auto_update is false (for example go install or npm install -g).", func() {
		MinLength(1)
	})
	Required("current_version", "latest_version", "update_available", "release_url", "can_auto_update")
})

// ApplyUpdateResult reports the outcome of an in-place server self-update request.
var ApplyUpdateResult = Type("ApplyUpdateResult", func() {
	Description("Outcome of installing an approved Highbay release version and scheduling an in-place server restart.")
	Attribute("status", String, "restarting when the new binary was installed and the server is re-executing in place; up_to_date when the running server already matches version.", func() {
		Enum("restarting", "up_to_date")
	})
	Attribute("version", String, "Target release version with leading v (for example v0.5.1).", func() {
		MinLength(1)
	})
	Required("status", "version")
})

// TeamUnavailable names which part of team preparation failed, so the CLI and page
// can show that part's recovery step without printing peer-authored error text.
var TeamUnavailable = Type("TeamUnavailable", func() {
	Description("Structured failure cause returned when project team preparation or agent launch cannot proceed.")
	Attribute("name", String, "Error name.", func() { Meta("struct:error:name") })
	Attribute("cause", String, "What could not be used: stopping (the server is shutting down), chorus (the local Chorus server is unavailable), hc (herdr-core is unavailable or a terminal command failed), project_state (the project's private record or an agent credential could not be read or written), or role_folder (a role's working folder could not be prepared).", func() {
		Enum("stopping", "chorus", "hc", "project_state", "role_folder")
	})
	Required("name", "cause")
})

var _ = Service("project", func() {
	Description("Owns one local project's server identity, agent credentials, self-update lifecycle, and origin-scoped browser session. Management credentials last for one listening server and never authorize application decisions; besides this service, they may only hand over the model provider request settings of the shell running highbay (the highbay service's hand_over_model_settings). This service is not exposed as agent tools.")
	Error("forbidden", ErrorResult, "The trusted credential or browser session cannot perform this operation.")
	Error("unprocessable", ErrorResult, "The requested credential exchange does not exist or is invalid.")
	HTTP(func() {
		Path("/project")
		Response("forbidden", StatusForbidden)
		Response("unprocessable", StatusUnprocessableEntity)
	})
	Method("identity", func() {
		Description("The CLI checks that the answering server reports its expected project identity using the current server-lifetime management credential.")
		Result(ProjectIdentity)
		HTTP(func() { GET("/identity"); Response(StatusOK) })
	})
	Method("ensure_team", func() {
		Description("Read existing project team metadata with the management credential. Creates no team, credential, or agent. This management operation is never an MCP tool.")
		Result(ProjectTeam)
		HTTP(func() {
			POST("/team")
			Response(StatusOK)
		})
	})
	Method("attach_guide", func() {
		Description("Attach the invoking coding agent session as this project's guide. When the invoking session runs inside a live herdr-core (hc) terminal in the project folder, Highbay labels that terminal for team supervision and Chorus turn-boundary wakes; external IDE sessions register without a managed terminal.")
		Payload(func() {
			Attribute("hc_terminal_id", String, "Invoking local herdr-core terminal identity when present in the environment.", func() { MinLength(1) })
			Attribute("client", String, "Invoking coding-agent client.", func() { Enum("claude", "codex", "antigravity", "cursor", "gemini", "opencode") })
			Attribute("ancestor_pids", ArrayOf(Int, func() { Minimum(1) }), "Invoking process and ancestor OS process IDs.", func() { MaxLength(64) })
		})
		Result(func() {
			Attribute("team", String, "Stable project team name.")
			Attribute("handle", String, "Guide's registered Chorus handle.")
			Attribute("has_terminal", Boolean, "Whether a live herdr-core terminal was matched and labeled for this guide.")
			Required("team", "handle", "has_terminal")
		})
		Error("team_unavailable", TeamUnavailable, "The guide could not be attached to this project.")
		HTTP(func() {
			POST("/attach-guide")
			Response(StatusOK)
			Response("team_unavailable", StatusServiceUnavailable)
		})
	})
	Method("status", func() {
		Description("Read existing project, saved change stage, and observed role state. Creates nothing and starts no agent. Merge status uses the build owner's read-only Git checks, including a manual merge into the project branch.")
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
		Description("The browser exchanges a single-use launch link once for an origin-scoped Bearer session credential and project folder name.")
		Payload(func() {
			Attribute("link", String, "Unexpired single-use link value.", func() { MinLength(1); MaxLength(256) })
			Required("link")
		})
		Result(BrowserSessionResult)
		HTTP(func() { POST("/browser-session"); Response(StatusCreated) })
	})
	Method("get_browser_session", func() {
		Description("Validate an existing origin-scoped Bearer browser session credential across page reloads and sibling tabs and return its project folder name, server version, and UI bundle digest.")
		Result(BrowserSessionResult)
		Error("unauthorized", ErrorResult, "The browser session credential is missing or no longer valid.")
		HTTP(func() {
			GET("/browser-session")
			Response(StatusOK)
			Response("unauthorized", StatusUnauthorized)
		})
	})
	Method("get_update_status", func() {
		Description("Check GitHub Releases for a newer stable Highbay version and report whether this running server can self-update and restart in place.")
		Result(UpdateStatusResult)
		Error("release_unavailable", ErrorResult, "The latest Highbay release metadata could not be fetched from GitHub.")
		HTTP(func() {
			GET("/update")
			Response(StatusOK)
			Response("release_unavailable", StatusBadGateway)
		})
	})
	Method("apply_update", func() {
		Description("Install the exact approved Highbay release version (via Homebrew or SHA-256-verified release archive), refresh companions and global MCP registrations, and re-execute highbay serve in place while preserving the active browser session and in-memory model settings.")
		Payload(func() {
			Attribute("version", String, "Approved target release version (for example 0.5.1 or v0.5.1).", func() {
				MinLength(1)
				MaxLength(64)
			})
			Required("version")
		})
		Result(ApplyUpdateResult)
		Error("conflict", ErrorResult, "An update or conflicting operation is already running, auto-update is unsupported for this installation, or the requested version would downgrade the installed binary.")
		Error("release_unavailable", ErrorResult, "The requested Highbay release archive, checksums, or Homebrew formula could not be fetched or verified.")
		HTTP(func() {
			POST("/update")
			Response(StatusOK)
			Response("conflict", StatusConflict)
			Response("release_unavailable", StatusBadGateway)
		})
	})
})
