// Package skills embeds the /highbay agent skill, so `highbay setup -global`
// installs it without downloading anything.
package skills

import _ "embed"

// HighbaySkillMD holds skills/highbay/SKILL.md, the /highbay ($highbay) agent skill
// that setup installs next to each detected coding agent.
//
//go:embed highbay/SKILL.md
var HighbaySkillMD []byte
