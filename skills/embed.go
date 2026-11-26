// Package skills embeds the /highbay agent skill and the Highbay Herdr companion
// mod, so `hbay setup -global` installs them without downloading anything.
package skills

import "embed"

// HighbaySkillMD holds skills/highbay/SKILL.md, the /highbay ($highbay) agent skill
// that setup installs next to each detected coding agent.
//
//go:embed highbay/SKILL.md
var HighbaySkillMD []byte

// HerdrMod holds the runtime files of the Highbay companion mod that setup
// copies into Herdr's mods folder. Its tests (test_mod.mjs) stay in the
// repository and are not embedded.
//
//go:embed herdr/highbay/mod.json herdr/highbay/main.js
var HerdrMod embed.FS
