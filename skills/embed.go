// Package skills embeds the /surla agent skill and the Surla Herdr companion
// mod, so `surla setup -global` installs them without downloading anything.
package skills

import "embed"

// SurlaSkillMD holds skills/surla/SKILL.md, the /surla ($surla) agent skill
// that setup installs next to each detected coding agent.
//
//go:embed surla/SKILL.md
var SurlaSkillMD []byte

// HerdrMod holds the runtime files of the Surla companion mod that setup
// copies into Herdr's mods folder. Its tests (test_mod.mjs) stay in the
// repository and are not embedded.
//
//go:embed herdr/surla/mod.json herdr/surla/main.js
var HerdrMod embed.FS
