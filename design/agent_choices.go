// This file defines explicit client choices and installed-client capabilities.
package design

import . "goa.design/goa/v3/dsl"

// RoleAgentChoice is a validated client selection for one missing project role.
var RoleAgentChoice = Type("RoleAgentChoice", func() {
	Attribute("client", String, "Installed coding-agent client family.", func() {
		Enum("claude", "codex", "antigravity", "cursor", "gemini", "opencode")
	})
	Attribute("program", String, "Installed executable selected from list_team_agent_options.", func() { MinLength(1) })
	Attribute("model", String, "Optional client-supported model.")
	Attribute("thinking", String, "Optional client-supported reasoning effort.")
	Attribute("yolo", Boolean, "Whether to enable this client's advertised unattended permission mode.")
	Required("client", "program", "yolo")
})

// InstalledAgentOption describes one discovered local executable and its choices.
var InstalledAgentOption = Type("InstalledAgentOption", func() {
	Attribute("client", String, "Coding-agent client family.", func() {
		Enum("claude", "codex", "antigravity", "cursor", "gemini", "opencode")
	})
	Attribute("program", String, "Resolved installed executable.")
	Attribute("title", String, "Human-readable client name.")
	Attribute("models", ArrayOf(String), "Advertised model choices; empty when the client accepts free-form names.")
	Attribute("efforts", ArrayOf(String), "Advertised reasoning-effort choices.")
	Attribute("model_supported", Boolean, "Whether a model can be selected.")
	Attribute("thinking_supported", Boolean, "Whether reasoning effort can be selected.")
	Attribute("yolo_supported", Boolean, "Whether an unattended permission mode is supported.")
	Attribute("yolo_effect", String, "What this client's unattended permission mode permits.")
	Attribute("headless_supported", Boolean, "Whether Surla supports this client's finite noninteractive mode.")
	Required("client", "program", "title", "models", "efforts", "model_supported", "thinking_supported", "yolo_supported", "headless_supported")
})

// roleAgentChoices constrains map keys at the transport boundary.
func roleAgentChoices() {
	Key(func() { Enum("guide", "builder", "test_writer") })
}
