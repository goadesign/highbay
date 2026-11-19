// This file proves worker choices through the served page and generated API.
package controlplane

import (
	"context"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestViewsPageWorkerDraftSurvivesUpdates exercises discovery, edits and the
// exact launch request without spawning real coding agents.
func TestViewsPageWorkerDraftSurvivesUpdates(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := buildableChange(t, cp, "chg-picker")
	ps := startPageServer(t, cp)
	ps.act("team-update", func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.snapshot = pageTeamSnapshot("needs_you")
	})
	ps.act("card-save", func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		record, ok, err := cp.cards.load(card.ChangeID)
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, cp.cards.save(record))
	})
	runPageScript(t, ps, map[string]any{"revision": card.RequestRevision}, `
const page = await openPage(origin, { link: input.link });
const discovery = "/team/agent-options", path = "/changes/chg-picker/build";
await until(() => page.$("#surla-build-agents-builder-client") && !page.$("#surla-build-start").disabled, "worker defaults did not become ready");
const client = (role) => JSON.parse(page.$("#surla-build-agents-" + role + "-client").value)[0];
expect(client("builder") === "codex" && client("test_writer") === "claude", "default workers did not use different installed families");
expect(!page.$("#surla-build-agents-builder-yolo").checked && !page.$("#surla-build-agents-test_writer-yolo").checked, "discovery granted permissions");
const model = page.$("#surla-build-agents-builder-model");
Object.getOwnPropertyDescriptor(page.win.HTMLInputElement.prototype, "value").set.call(model, "custom-model-id");
model.dispatchEvent(new page.win.Event("input", { bubbles: true }));
const effort = page.$("#surla-build-agents-builder-thinking"); effort.value = "high";
effort.dispatchEvent(new page.win.Event("change", { bubbles: true }));
const editor = page.$('textarea[data-example-id="check_01"][data-field="scenario"]');
page.type(editor, "Keep my unsaved Story text");
const reads = page.sent("GET", discovery).length;
page.$("#surla-nav-team").click();
await until(() => page.$("#surla-team-agents-builder-model"), "Team worker draft did not open");
expect(page.$("#surla-team-agents-builder-model").value === "custom-model-id", "navigation replaced the worker draft");
await control(origin, "act", { key: "team-update" });
await until(() => page.$("#surla-team-role-builder")?.dataset.state === "needs_you", "Team update did not arrive");
expect(page.$("#surla-team-agents-builder-model").value === "custom-model-id", "Team polling replaced edited choices");
page.$("#surla-nav-build").click();
await until(() => page.$("#surla-build-agents-builder-model"), "Build picker did not return");
expect(page.$("#surla-build-agents-builder-model").value === "custom-model-id" && page.$("#surla-build-agents-builder-thinking").value === "high", "Build navigation replaced edited worker settings");
const cardReads = page.sent("GET", "/changes/chg-picker").length;
await control(origin, "act", { key: "card-save" });
await until(() => page.sent("GET", "/changes/chg-picker").length > cardReads && !page.$("#surla-build-loading"), "saved card update did not arrive");
expect(page.$("#surla-build-agents-builder-model").value === "custom-model-id" && page.$("#surla-build-agents-builder-thinking").value === "high", "saved card polling replaced worker drafts");
expect(page.sent("GET", discovery).length === reads, "navigation or polling repeated executable discovery");
expect(page.$('textarea[data-example-id="check_01"][data-field="scenario"]').value === "Keep my unsaved Story text", "worker navigation discarded the Story draft");
expect(page.sent("POST", path).length === 0 && page.sent("POST", "/team/agents").length === 0, "reads or edits launched a role");
page.$("#surla-build-start").click();
await until(() => page.sent("POST", path).length === 1, "explicit Build did not submit");
const request = page.sent("POST", path)[0].body;
expect(request.request_revision === input.revision, "Build changed the selected agreement");
expect(JSON.stringify(Object.keys(request.choices).sort()) === JSON.stringify(["builder", "test_writer"]), "Build sent a guide or omitted a needed worker");
expect(request.choices.builder.client === "codex" && request.choices.builder.program === "/fixture/codex" && request.choices.builder.model === "custom-model-id" && request.choices.builder.thinking === "high" && request.choices.builder.yolo === false, "Build did not send the edited builder choice");
expect(request.choices.test_writer.client === "claude" && request.choices.test_writer.yolo === false, "Build did not preserve the test-writer choice");
page.close();
`)
}

// TestViewsPageAgentDiscoveryRequiresExplicitRecovery keeps failure and empty
// inventory actionable while refusing automatic role launches or probe loops.
func TestViewsPageAgentDiscoveryRequiresExplicitRecovery(t *testing.T) {
	f := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
	buildableChange(t, cp, "chg-discovery")
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, `
const discovery = "/team/agent-options";
await control(origin, "reply", { key: "GET " + discovery, status: 503, body: { name: "unavailable", message: "Agent discovery unavailable." } });
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-agents-error"), "discovery failure was not visible");
expect(page.$("#surla-build-start").disabled && page.text("#surla-build-agents-error").includes("Retry agent discovery"), "failed discovery did not block launch with recovery");
await sleep(100);
expect(page.sent("GET", discovery).length === 1, "failed discovery retried automatically");
await control(origin, "reply", { key: "GET " + discovery, status: 200, body: { agents: [], build_roles: ["builder", "test_writer"] } });
page.$("#surla-build-agents-refresh").click();
await until(() => page.$("#surla-build-agents-empty") && !page.$("#surla-build-agents-error"), "empty discovery did not settle");
expect(page.$("#surla-build-start").disabled && page.text("#surla-build-agents-empty").includes("Install a coding agent"), "empty inventory offered a launch");
page.$("#surla-build-agents-refresh").click();
await until(() => page.$("#surla-build-start") && !page.$("#surla-build-start").disabled, "explicit discovery retry did not recover");
expect(page.sent("GET", discovery).length === 3, "retry performed additional discovery");
expect(page.sent("POST", "/changes/chg-discovery/build").length === 0 && page.sent("POST", "/team/agents").length === 0, "discovery recovery launched workers");
page.close();
`)
}

// TestViewsPageSingleFamilyAndBuilderOnly uses the owner-returned role set so
// an evaluator never launches an unnecessary test writer or recommends one.
func TestViewsPageSingleFamilyAndBuilderOnly(t *testing.T) {
	for _, stored := range []bool{false, true} {
		t.Run(map[bool]string{false: "written tests", true: "stored evaluator"}[stored], func(t *testing.T) {
			f := &teamServiceFixture{snapshot: pageTeamSnapshot("working"), options: func(context.Context) ([]*gensurla.InstalledAgentOption, error) {
				return fixtureAgentOptions()[:1], nil
			}}
			opts := Options{Team: &TeamBinding{Owner: pageOwner, Service: f}}
			if stored {
				opts.BuildCacheDir = t.TempDir()
				opts.Verification = &VerificationConfig{RunsDir: t.TempDir()}
			}
			cp := newBuildControlPlane(t, opts)
			buildableChange(t, cp, "chg-single")
			ps := startPageServer(t, cp)
			runPageScript(t, ps, map[string]any{"stored": stored}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-agents-builder-client") && !page.$("#surla-build-start").disabled, "single installed family was not usable");
expect(JSON.parse(page.$("#surla-build-agents-builder-client").value)[0] === "codex", "builder did not use installed family");
expect(Boolean(page.$("#surla-build-agents-test_writer-client")) === !input.stored, "picker ignored the owner-returned build roles");
if (!input.stored) expect(JSON.parse(page.$("#surla-build-agents-test_writer-client").value)[0] === "codex", "single family was not reused for test writer");
expect(Boolean(page.$("#surla-build-agents-notice-single_agent_family")) === !input.stored, "single-family advice ignored whether two roles are required");
page.$("#surla-build-start").click();
await until(() => page.sent("POST", "/changes/chg-single/build").length === 1, "single-family Build did not submit");
const choices = page.sent("POST", "/changes/chg-single/build")[0].body.choices;
expect(JSON.stringify(Object.keys(choices).sort()) === JSON.stringify(input.stored ? ["builder"] : ["builder", "test_writer"]), "Build sent an unnecessary worker role");
page.close();
`)
		})
	}
}

// TestViewsPageHeadlessNeedsPermissionAndSeparateConsent tests the user-facing
// permission boundary for both switch-capable clients and configured permissions.
func TestViewsPageHeadlessNeedsPermissionAndSeparateConsent(t *testing.T) {
	for _, openCode := range []bool{false, true} {
		t.Run(map[bool]string{false: "YOLO switches", true: "configured permissions"}[openCode], func(t *testing.T) {
			snapshot := pageTeamSnapshot("working")
			snapshot.HerdrMode = "unavailable"
			f := &teamServiceFixture{snapshot: snapshot}
			if openCode {
				f.options = func(context.Context) ([]*gensurla.InstalledAgentOption, error) {
					return []*gensurla.InstalledAgentOption{{Client: "opencode", Program: "/fixture/opencode", Title: "OpenCode", Models: []string{}, Efforts: []string{}, ModelSupported: true, HeadlessSupported: true}}, nil
				}
			}
			cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
			buildableChange(t, cp, "chg-background")
			ps := startPageServer(t, cp)
			runPageScript(t, ps, map[string]any{"openCode": openCode}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-agents-background-consent") && page.$("#surla-build-agents-builder-client").value, "background picker did not settle");
expect(page.$("#surla-build-start").disabled && !page.$("#surla-build-agents-background-consent").checked, "headless mode implicitly granted background consent");
if (input.openCode) {
  expect(!page.$("#surla-build-agents-builder-yolo") && page.text("#surla-build-agents-builder").includes("configured permissions"), "OpenCode invented a YOLO switch");
} else {
  for (const role of ["builder", "test_writer"]) page.$("#surla-build-agents-" + role + "-yolo").click();
  expect(page.$("#surla-build-start").disabled, "permission switches were treated as background consent");
}
page.$("#surla-build-agents-background-consent").click();
await until(() => !page.$("#surla-build-start").disabled, "explicit consent and valid permissions did not enable background workers");
page.$("#surla-build-start").click();
await until(() => page.sent("POST", "/changes/chg-background/build").length === 1, "background Build did not submit");
const choices = page.sent("POST", "/changes/chg-background/build")[0].body.choices;
expect(choices.builder.yolo === !input.openCode && choices.test_writer.yolo === !input.openCode, "permission choice did not match actual supported switches");
expect(page.sent("POST", "/team/agents").length === 0, "picker mounted a separate worker launch");
page.close();
`)
		})
	}
}

// TestViewsPageRecommendationsDismissOnlyAfterSave exercises the generated
// dismissal API and failed-save recovery without starting workers.
func TestViewsPageRecommendationsDismissOnlyAfterSave(t *testing.T) {
	for _, notice := range []string{"single_agent_family", "outside_herdr", "herdr_unavailable"} {
		t.Run(notice, func(t *testing.T) {
			snapshot := pageTeamSnapshot("working")
			if notice == "outside_herdr" {
				snapshot.HerdrMode = "available"
			} else if notice == "herdr_unavailable" {
				snapshot.HerdrMode = "unavailable"
			}
			f := &teamServiceFixture{snapshot: snapshot, options: func(context.Context) ([]*gensurla.InstalledAgentOption, error) {
				return fixtureAgentOptions()[:1], nil
			}}
			cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: f}})
			buildableChange(t, cp, "chg-notice")
			ps := startPageServer(t, cp)
			runPageScript(t, ps, map[string]any{"notice": notice}, `
const page = await openPage(origin, { link: input.link });
const id = "#surla-build-agents-notice-" + input.notice, path = "/team/notices/dismiss";
await until(() => page.$(id + "-dismiss"), "recommendation did not appear");
await control(origin, "reply", { key: "POST " + path, status: 503, body: { name: "unavailable", message: "Preference owner unavailable." } });
page.$(id + "-dismiss").click();
await until(() => page.text(id).includes("Could not save this dismissal"), "failed dismissal hid the recommendation");
expect(page.text(id + "-dismiss") === "Retry dismissal", "dismissal refusal omitted an actionable retry");
page.$(id + "-dismiss").click();
await until(() => !page.$(id), "successful dismissal did not remove the recommendation");
expect(page.sent("POST", path).length === 2 && page.sent("POST", path).every((request) => request.body.notice === input.notice), "dismissal changed notice identity or retried automatically");
page.$("#surla-nav-team").click();
await until(() => page.$("#surla-team-agents"), "Team did not open");
expect(!page.$("#surla-team-agents-notice-" + input.notice), "saved dismissal did not follow navigation");
expect(page.sent("POST", "/changes/chg-notice/build").length === 0 && page.sent("POST", "/team/agents").length === 0, "notice dismissal launched workers");
page.close();
`)
			f.mu.Lock()
			assert.Contains(t, f.snapshot.DismissedNotices, notice)
			f.mu.Unlock()
		})
	}
}
