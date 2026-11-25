// This file declares the Checkpoint 1 analysis a caller sends with
// propose_change and revise_change: the summary, open questions, examples and
// preserved behaviors that Highbay checks, numbers and saves as a Change Card.
package design

import . "goa.design/goa/v3/dsl"

// nonBlank is the pattern every required analysis text matches: it contains at
// least one character that is not whitespace. Highbay saves the text exactly as
// sent, surrounding whitespace included.
//
// Whitespace is exactly what Go's unicode.IsSpace reports, the characters
// strings.TrimSpace removes. The class writes them out instead of using \s,
// whose meaning differs between Go's regexp, which the generated validators
// use, and ECMA-262, which JSON Schema consumers of the MCP tool schemas and
// the OpenAPI document use; written out, both accept and reject the same
// strings. Zero-width characters such as U+200B are not whitespace.
const nonBlank = "[^\\t\\n\\v\\f\\r \u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]"

// CP1AnalysisAmbiguity is one open question the requester must answer.
var CP1AnalysisAmbiguity = Type("CP1AnalysisAmbiguity", func() {
	Description("One product, domain, or operational choice only the requester can make. Highbay gives it the ID <change_id>-ambiguity-NN, numbered in list order.")
	Attribute("question", String, "The decision in plain language, saying what the choice changes.", func() {
		Pattern(nonBlank)
	})
	Attribute("options", ArrayOfRequired(String, func() {
		Pattern(nonBlank)
	}), "At least two distinct, self-contained choices; each states the behavior it selects and what follows from it. The requester's answer must be exactly one of these strings.", func() {
		MinLength(2)
	})
	Attribute("draft_option", String, "The one string in options, character for character, that the draft design, Story, examples and preserved behaviors you send with this proposal already implement. Set it whenever choosing another option would require changing any of them. The user can then confirm only this option on the page; when they want another, the page sends them back to you, and you revise the change (revise_change) so the draft implements their choice before they confirm. Omit it only when every option works with the draft exactly as it stands.", func() {
		Pattern(nonBlank)
	})
	Required("question", "options")
})

// CP1AnalysisExample is one acceptance example of a proposal, before Highbay
// assigns its ID.
var CP1AnalysisExample = Type("CP1AnalysisExample", func() {
	Description("One concrete acceptance example that becomes a check the change must pass. Highbay gives it the ID check_01, check_02, ... in list order.")
	Attribute("scenario", String, "Short name of the situation, in the requester's domain terms.", func() {
		Pattern(nonBlank)
	})
	Attribute("input_summary", String, "The actual request, inputs, and starting state in this scenario, with real values from the request and the design, never placeholders.", func() {
		Pattern(nonBlank)
	})
	Attribute("expected_outcome", String, "The observable result or refusal that must hold.", func() {
		Pattern(nonBlank)
	})
	Attribute("verification_kind", String, "deterministic when code can check the outcome directly; model_judged when checking it needs judgment.", func() {
		Enum("deterministic", "model_judged")
	})
	Required("scenario", "input_summary", "expected_outcome", "verification_kind")
})

// CP1RevisionAnalysisExample is one acceptance example of a revision, kept from
// the current card through prior_id or new.
var CP1RevisionAnalysisExample = Type("CP1RevisionAnalysisExample", func() {
	Description("One concrete acceptance example of the revised card: a current example kept or edited in place (prior_id set), or a new one.")
	Attribute("prior_id", String, "ID of the current card's example this one keeps or edits, exactly as the card shows it; the example keeps that ID. Omit it for a new example, which gets the lowest check_NN ID the current and revised cards do not use.", func() {
		MinLength(1)
	})
	Attribute("scenario", String, "Short name of the situation, in the requester's domain terms.", func() {
		Pattern(nonBlank)
	})
	Attribute("input_summary", String, "The actual request, inputs, and starting state in this scenario, with real values from the request and the design, never placeholders.", func() {
		Pattern(nonBlank)
	})
	Attribute("expected_outcome", String, "The observable result or refusal that must hold.", func() {
		Pattern(nonBlank)
	})
	Attribute("verification_kind", String, "deterministic when code can check the outcome directly; model_judged when checking it needs judgment.", func() {
		Enum("deterministic", "model_judged")
	})
	Required("scenario", "input_summary", "expected_outcome", "verification_kind")
})

// CP1Analysis is the caller's analysis of a new feature request.
var CP1Analysis = Type("CP1Analysis", func() {
	Description("Product-level Change Card shown to the requester on the Highbay browser page to confirm intent before code is written. Write every field for a requester who knows only their own goal, not the repository: use plain domain language about observable user behavior, never repository file paths (such as design/design.go), Highbay candidate runner flags (such as -listen), or internal Goa/Goa-AI symbol names unless the requester explicitly asked about a CLI or API surface. When the project was created with Highbay's worked example (init_project with worked_example: true) and the requester is building their own product, replace that example silently in your draft: never mention or ask about it on the Change Card. Highbay checks structure and cross-field rules; it cannot judge whether the analysis is complete or correct.")
	Attribute("summary", String, "Concise plain-English description (2–4 sentences) of what the product or change will do for its users and the key product defaults your draft settles. Never describe file paths, candidate runner flags, or removal of Highbay's worked example.", func() {
		Pattern(nonBlank)
	})
	Attribute("ambiguities", ArrayOfRequired(CP1AnalysisAmbiguity), "Open questions only the requester can answer: product, domain, or operational choices that change user-visible behavior. Never ask whether to replace Highbay's worked example or ask about internal Go/Goa plumbing. Do not ask about details the request already settles; state settled product defaults in summary, or in examples when they change an observable outcome. Send an empty list when nothing is open.")
	Attribute("examples", ArrayOfRequired(CP1AnalysisExample), "Concrete acceptance examples in plain domain terms, never placeholder text or leftovers of Highbay's worked example; each becomes a check the change must pass.", func() {
		MinLength(1)
	})
	Attribute("preserved_behaviors", ArrayOfRequired(String, func() {
		Pattern(nonBlank)
	}), "Existing user-facing behaviors of an established product that this change must keep, each stated in plain domain terms; send an empty list ([]) on a new product (including the first change after init_project: a blank project has no behavior to keep, and Highbay's worked example is replaced rather than preserved) or when none apply.")
	Attribute("ui_composition", String, "Optional OpenUI Lang program that helps the requester decide the open questions, built only from the decision components the highbay://golden-path resource lists. Refer to an ambiguity or example only by its 1-based position in this analysis (1 is the first); never invent identifiers. Highbay saves it unchanged without checking it; the views page validates and renders it. Omit it when a plain card is clearer.")
	Required("summary", "ambiguities", "examples", "preserved_behaviors")
})

// CP1RevisionAnalysis is the caller's analysis of a revised feature request
// against the current Change Card.
var CP1RevisionAnalysis = Type("CP1RevisionAnalysis", func() {
	Description("Updated product-level Change Card shown to the requester on the Highbay browser page, analyzed against the current Change Card (as get_change returns it at request_revision), the draft design, and highbay://golden-path. Write every field for a requester who knows their own goal, not the repository: use plain domain language without file paths (such as design/design.go), Highbay candidate runner flags (such as -listen), references to Highbay's worked example, or internal Goa/Goa-AI symbol names unless the requester explicitly asked about a CLI or API surface. A confirmed answer stays settled unless the revised request or design contradicts it: do not ask it again, and restate it in summary, examples or preserved_behaviors, because the revised card drops every answer. An explicit instruction in the revised request or design replaces the decisions, examples and preserved behaviors it affects. An unanswered question stays in ambiguities unless the revised request or design answers it or removes its scope. Everything else on the current card stays unless the revised request or design changes it. Highbay checks the structure and the example accounting; it cannot judge whether the analysis is complete or correct.")
	Attribute("summary", String, "Concise plain-English description (2–4 sentences) of the revised product behavior and settled product defaults, without file paths, candidate runner flags, or references to Highbay's worked example.", func() {
		Pattern(nonBlank)
	})
	Attribute("ambiguities", ArrayOfRequired(CP1AnalysisAmbiguity), "Open questions only the requester can answer: product, domain, or operational choices that change user-visible behavior. Never ask about Highbay's worked example or internal Go/Goa plumbing. Do not ask about details the request, the design or a confirmed answer already settle; state settled product defaults in summary, or in examples when they change an observable outcome. Send an empty list when nothing is open.")
	Attribute("examples", ArrayOfRequired(CP1RevisionAnalysisExample), "Concrete acceptance examples in plain domain terms. Give prior_id for each current example you keep or edit; omit it for new ones.", func() {
		MinLength(1)
	})
	Attribute("removed_example_ids", ArrayOfRequired(String, func() {
		MinLength(1)
	}), "IDs of the current card's examples this revision drops. Every current example ID appears exactly once, either as an examples[].prior_id or here; send an empty list when none are dropped.")
	Attribute("preserved_behaviors", ArrayOfRequired(String, func() {
		Pattern(nonBlank)
	}), "Existing user-facing behaviors of the product that this revision must keep, each stated in plain domain terms; empty when none apply.")
	Attribute("ui_composition", String, "Optional OpenUI Lang program that helps the requester decide the open questions, built only from the decision components the highbay://golden-path resource lists. Refer to an ambiguity or example only by its 1-based position in this analysis (1 is the first); never invent identifiers. Highbay saves it unchanged without checking it; the views page validates and renders it. Omit it when a plain card is clearer.")
	Required("summary", "ambiguities", "examples", "removed_example_ids", "preserved_behaviors")
})
