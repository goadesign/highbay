// This file drives the views page's Live target view through the real page
// harness: the live target is the fake target started through the real
// `verify-run live` path, the builder agent starts it and sends drafts over
// HTTP with its own credential, and the page follows through its one waiting
// read.
package controlplane

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

// liveScriptHelpers are the page-script helpers the Live target view's tests
// share: opening the view, and the builder agent's start and draft calls.
const liveScriptHelpers = `
const commitA = "a".repeat(40);
async function openLive(page) {
  await until(() => page.$("#highbay-nav-live"), "the Live target tab is missing");
  page.$("#highbay-nav-live").click();
  await until(() => page.state() === "live", "the Live target view did not open");
  await until(() => page.$("#highbay-live-target") && page.$("#highbay-live-target").dataset.state !== "unread", "the live target was never read");
}
async function startTarget(commit) {
  const out = await api(origin, "POST", "/live-target", { change_id: "chg-live", candidate_commit: commit }, input.builder);
  expect(out.status === 200, "the builder could not start a live target: " + JSON.stringify(out));
  return out.body;
}
async function draftPrompt(text) {
  const out = await api(origin, "POST", "/sandbox/draft", { target_kind: "prompt_template", prompt_id: "homes.helper.system", new_content: text }, input.builder);
  expect(out.status === 200, "the builder's draft was refused: " + JSON.stringify(out));
  return out.body;
}
function liveState(page) {
  return page.$("#highbay-live-target").dataset.state;
}
`

// TestViewsPageLiveTargetFollowsAStartDraftsAndAnEnd opens the Live target
// view with nothing running, then follows, without a reload and through the
// one waiting read, the builder agent starting a target, two drafts of the
// same prompt, and the target ending by itself. It checks the sentences,
// the link, the announcements, that the second draft replaces the first
// entry, that no prompt text appears, that text typed in the model form
// survives every refresh, that the live target is read only after the
// version moved, and that the diagnostics history holds no waiting read.
func TestViewsPageLiveTargetFollowsAStartDraftsAndAnEnd(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	cp := pageLiveControlPlane(t, "serve", Options{TracerProvider: provider})
	ps := startPageServer(t, cp)
	ps.act("refresh-live-observation", func() {
		saveStoryCard(t, cp, "chg-live", StatusConfirmed, refundStory("A revised visible flow"))
	})
	runPageScript(t, ps, nil, liveScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await openLive(page);
expect(liveState(page) === "none", "a fresh server shows " + liveState(page));
expect(page.text("#highbay-live-none").includes("The builder agent starts one from a commit to try prompt wording on it."), "the empty state does not say what starts a live target: " + page.text("#highbay-live-none"));
expect(!page.text("#highbay-live-stage").includes("start_live_target"), "a sentence names an operation");
expect(!page.$("#highbay-live-stop"), "Stop is offered with nothing running");
await until(() => page.$("#highbay-model-current"), "the model connection was never read");
expect(page.text("#highbay-model-current").includes("Without stored verification settings, choose a model") && page.text("#highbay-model-current").includes("With stored settings, the evaluator may supply its own model"), "the missing-model explanation must distinguish both verification profiles");
const typed = page.$("#highbay-model-name");
page.type(typed, "typed-but-not-applied");

const started = await startTarget(commitA);
await until(() => liveState(page) === "running", "a started target did not appear without a reload");
expect(page.text("#highbay-live-running").includes(commitA), "the running target does not name its commit");
const link = page.$("#highbay-live-address");
expect(link.tagName === "A" && link.getAttribute("href") === new URL(started.url).origin, "the local address is not a link to its origin: " + link.outerHTML);
expect(link.getAttribute("target") === "_blank" && link.getAttribute("rel") === "noopener noreferrer", "the link does not open a new tab safely: " + link.outerHTML);
expect(page.$("#highbay-live-running time").getAttribute("datetime").length > 0, "the start time is not machine-readable");
expect(page.$("#highbay-live-no-prompts"), "a target with no drafts does not say so");
expect(page.text("#highbay-live-drafts-note").includes("discarded when this live target stops") && page.text("#highbay-live-drafts-note").includes("committed"), "the drafts note is missing");
await until(() => page.text("#highbay-live-announcement").includes("is running"), "the start was not announced: " + page.text("#highbay-live-announcement"));
expect(page.$("#highbay-live-announcement").getAttribute("role") === "status", "the announcements are not in a polite live region");

const first = await draftPrompt("Answer in French.");
await until(() => page.text("#highbay-live-prompts").includes(first.prompt_version), "an accepted draft did not appear without a reload");
await until(() => page.text("#highbay-live-announcement").includes("homes.helper.system now uses version " + first.prompt_version), "the draft was not announced: " + page.text("#highbay-live-announcement"));
const second = await draftPrompt("Answer in Spanish.");
await until(() => page.text("#highbay-live-prompts").includes(second.prompt_version), "a second draft did not appear without a reload");
expect(page.$$("#highbay-live-prompts li").length === 1, "a second draft of the same prompt added an entry");
expect(!page.text("#highbay-live-prompts").includes(first.prompt_version), "the first draft's version is still shown");
expect(!page.text("#highbay-live-stage").includes("Answer in"), "prompt text appeared on the page");
expect(page.$("#highbay-model-name") === typed && typed.value === "typed-but-not-applied", "a refresh lost the model form's typed text");

const endings = [];
const announcements = new page.win.MutationObserver(() => endings.push(page.text("#highbay-live-announcement")));
announcements.observe(page.$("#highbay-live-announcement"), { childList: true, characterData: true, subtree: true });
typed.focus();
await fetch(started.url + "/exit", { method: "POST" }).catch(() => {});
await until(() => liveState(page) === "ended", "a target that ended by itself did not leave an ended observation", 30000);
await until(() => page.text("#highbay-live-announcement") === "The live target for commit aaaaaaa ended unexpectedly.", "the end was not announced: " + page.text("#highbay-live-announcement"));
expect(page.text("#highbay-live-ended").includes(commitA), "the ended observation lost its commit");
expect(page.$$("#highbay-live-ended time").length === 2 && page.$$("#highbay-live-ended time").every((time) => time.getAttribute("datetime")), "the ended launch and observation times are missing");
expect(page.text("#highbay-live-message").includes("exact cause is unavailable") && page.text("#highbay-live-message").includes("builder"), "the ended observation must name its uncertainty and recovery");
expect(!page.$("#highbay-live-stop") && !page.$("#highbay-live-address") && !page.$("#highbay-live-prompts"), "an ended target retains controls or live facts");
expect(page.$("#highbay-model-name") === typed && typed.value === "typed-but-not-applied", "a refresh lost the model form's typed text");
expect(page.doc.activeElement === typed, "the end stole focus from the unsent model form");
const endedReads = page.sent("GET", "/live-target").length;
await control(origin, "act", { key: "refresh-live-observation" });
await until(() => page.sent("GET", "/live-target").length > endedReads && !page.$("#highbay-live-stage").getAttribute("aria-busy"), "the page did not refresh the retained observation");
expect(endings.filter((text) => text === "The live target for commit aaaaaaa ended unexpectedly.").length === 1, "the same ending was announced more than once: " + JSON.stringify(endings));
announcements.disconnect();

const reloaded = await openPage(origin, { link: input.link + "-2" });
await openLive(reloaded);
expect(liveState(reloaded) === "ended" && reloaded.text("#highbay-live-ended") === page.text("#highbay-live-ended"), "reload lost the ended observation");
expect(reloaded.text("#highbay-live-announcement") === "", "reload announced an old ending as a new event");
reloaded.close();

const reads = page.sent("GET", "/live-target").length;
const lists = page.sent("GET", "/changes").length;
expect(reads <= lists + 1, "the live target was read " + reads + " times for " + lists + " list reads: it is read only when the version moves");
const others = page.requests.filter((r) => !["/changes", "/change-updates", "/changes/chg-live", "/changes/chg-live/build", "/live-target", "/cp1/model", "/project/browser-session", "/team"].includes(r.path));
expect(others.length === 0, "the page sent other requests: " + JSON.stringify(others.map((r) => r.method + " " + r.path)));
const history = await api(origin, "GET", "/traces?status=all&limit=100", undefined, input.guide);
expect(history.status === 200, "diagnostics could not be read: " + JSON.stringify(history));
expect(!history.body.spans.some((span) => span.name.includes("wait_for_changes")), "a waiting read was saved in the diagnostics history");
page.close();
`)
}

// TestViewsPageLiveTargetShowsAStartInProgress follows a start that the first
// change list already covers, even if the Live view read an earlier state.
func TestViewsPageLiveTargetShowsAStartInProgress(t *testing.T) {
	cp := pageLiveControlPlane(t, "prepare-hangs", Options{})
	ps := startPageServer(t, cp)
	// Cancel the pending start before the test server waits for its request.
	defer func() { assert.NoError(t, cp.Close()) }()
	runPageScript(t, ps, nil, liveScriptHelpers+`
await control(origin, "hold", { key: "GET /changes" });
const page = await openPage(origin, { link: input.link });
await openLive(page);
expect(liveState(page) === "none", "the early Live view did not read the empty state");
void api(origin, "POST", "/live-target", { change_id: "chg-live", candidate_commit: commitA }, input.builder);
let started = false;
for (const deadline = performance.now() + 5000; performance.now() < deadline; ) {
  const observed = await api(origin, "GET", "/live-target", undefined, input.builder);
  expect(observed.status === 200, "the builder could not read the live target");
  if (observed.body.live_target.state === "starting") {
    started = true;
    break;
  }
  await sleep(10);
}
expect(started, "the server did not begin the start before the first list read");
await control(origin, "release", { key: "GET /changes" });
await until(() => liveState(page) === "starting", "a start in progress was not drawn");
expect(page.text("#highbay-live-starting").includes(commitA) && page.text("#highbay-live-starting").includes("cannot be stopped until the start finishes"), "the starting state is not described: " + page.text("#highbay-live-starting"));
expect(page.$("#highbay-live-starting time"), "the start time is missing");
expect(!page.$("#highbay-live-stop"), "Stop is offered while the start builds");
await until(() => page.text("#highbay-live-announcement").includes("is starting from commit"), "the start was not announced");
page.close();
`)
}

// TestViewsPageStopsTheLiveTargetAfterConfirming checks the stop control
// from the keyboard's side: opening the confirmation moves focus to it,
// Escape and "Keep it running" return focus to Stop and send nothing, and
// "Stop it" sends exactly one stop request, shows the server's message, and
// leaves the panel saying none is running.
func TestViewsPageStopsTheLiveTargetAfterConfirming(t *testing.T) {
	cp := pageLiveControlPlane(t, "serve", Options{})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, liveScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await openLive(page);
await startTarget(commitA);
await until(() => liveState(page) === "running", "the started target did not appear");
const stop = () => page.$("#highbay-live-stop");
expect(stop() && stop().tagName === "BUTTON" && stop().textContent === "Stop live target", "the Stop control is missing");

stop().click();
await until(() => page.$("#highbay-live-stop-confirm"), "Stop did not ask first");
const confirm = page.$("#highbay-live-stop-confirm");
await until(() => page.doc.activeElement === confirm, "opening the confirmation did not move focus to it");
expect(page.text("#highbay-live-stop-question").includes("It ends now, and every prompt draft applied to it is discarded"), "the confirmation does not say what stopping does: " + page.text("#highbay-live-stop-question"));
expect(!stop(), "Stop is still offered beside its confirmation");
confirm.dispatchEvent(new page.win.KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
await until(() => !page.$("#highbay-live-stop-confirm") && stop() && page.doc.activeElement === stop(), "Escape did not close the confirmation and return focus to Stop");

stop().click();
await until(() => page.$("#highbay-live-stop-keep"), "Stop did not ask again");
page.$("#highbay-live-stop-keep").click();
await until(() => !page.$("#highbay-live-stop-confirm") && stop() && page.doc.activeElement === stop(), "Keep it running did not return focus to Stop");
expect(page.sent("DELETE", "/live-target").length === 0, "a stop was sent before the user confirmed");

stop().click();
await until(() => page.$("#highbay-live-stop-yes"), "Stop did not ask a third time");
page.$("#highbay-live-stop-yes").click();
await until(() => page.$("#highbay-live-stop-outcome"), "the stop's answer was not shown");
expect(page.text("#highbay-live-stop-outcome") === "The live target is stopped; its prompt drafts are discarded and draft_override is no longer bound to it.", "the operation's message is not shown as it is: " + page.text("#highbay-live-stop-outcome"));
await until(() => liveState(page) === "none", "the stopped target is still shown");
expect(page.sent("DELETE", "/live-target").length === 1, "the stop was sent " + page.sent("DELETE", "/live-target").length + " times");
expect(page.$("#highbay-live-stop-outcome"), "the stop's message went before the state changed");
await startTarget(commitA);
await until(() => liveState(page) === "running" && !page.$("#highbay-live-stop-outcome"), "the stop's message stayed beside a target started afterwards");
page.close();
`)
}

// TestViewsPageLiveTargetEndingClosesTheConfirmation lets a target end by
// itself twice: once with the stop confirmation open, which closes, and once
// with focus on the Stop control and no confirmation. Each time nothing is
// sent and focus goes to the view's heading, not the page body.
func TestViewsPageLiveTargetEndingClosesTheConfirmation(t *testing.T) {
	cp := pageLiveControlPlane(t, "serve", Options{})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, liveScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await openLive(page);
const started = await startTarget(commitA);
await until(() => liveState(page) === "running", "the started target did not appear");
page.$("#highbay-live-stop").click();
await until(() => page.doc.activeElement === page.$("#highbay-live-stop-confirm"), "the confirmation did not take focus");
await fetch(started.url + "/exit", { method: "POST" }).catch(() => {});
await until(() => liveState(page) === "ended", "a target that ended by itself did not leave an ended observation", 30000);
await until(() => !page.$("#highbay-live-stop-confirm"), "the confirmation stayed open for a target that ended");
await until(() => page.doc.activeElement === page.$("#highbay-live-target-title"), "focus did not go to the view's heading: " + page.doc.activeElement.outerHTML.slice(0, 80));

const again = await startTarget(commitA);
await until(() => liveState(page) === "running" && page.$("#highbay-live-stop"), "the second target did not appear");
page.$("#highbay-live-stop").focus();
expect(page.doc.activeElement === page.$("#highbay-live-stop"), "the Stop control did not take focus");
await fetch(again.url + "/exit", { method: "POST" }).catch(() => {});
await until(() => liveState(page) === "ended", "the second target that ended did not leave an ended observation", 30000);
await until(() => page.doc.activeElement === page.$("#highbay-live-target-title"), "focus left on Stop did not go to the view's heading: " + page.doc.activeElement.outerHTML.slice(0, 80));
expect(page.sent("DELETE", "/live-target").length === 0, "a stop was sent although the user never confirmed");
page.close();
`)
}

// TestViewsPageClearsARefusedStopWhenTheStateChanges has the server refuse
// a stop, shows the refusal, keeps it while the target's state is the one
// the refusal left, and clears it once the target ends.
func TestViewsPageClearsARefusedStopWhenTheStateChanges(t *testing.T) {
	cp := pageLiveControlPlane(t, "serve", Options{})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, liveScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await openLive(page);
const started = await startTarget(commitA);
await until(() => liveState(page) === "running", "the started target did not appear");
await control(origin, "reply", { key: "DELETE /live-target", status: 500, body: { name: "live_target_stop_failed", id: "x", message: "The stop could not be completed for this test.", temporary: false, timeout: false, fault: false } });
page.$("#highbay-live-stop").click();
await until(() => page.$("#highbay-live-stop-yes"), "Stop did not ask first");
page.$("#highbay-live-stop-yes").click();
await until(() => page.text("#highbay-live-stop-outcome") === "The stop could not be completed for this test.", "the refusal was not shown");
await until(() => page.sent("GET", "/live-target").length >= 2, "the page did not read the live target after the refusal");
await sleep(600);
expect(page.$("#highbay-live-stop-outcome"), "the refusal went while the state stayed the same");
await fetch(started.url + "/exit", { method: "POST" }).catch(() => {});
await until(() => liveState(page) === "ended", "a target that ended by itself did not leave an ended observation", 30000);
await until(() => !page.$("#highbay-live-stop-outcome"), "the refusal stayed after the state changed");
page.close();
`)
}

// TestViewsPageLiveTargetNotConfirmedStopped makes a stop fail and checks
// that the page shows the server's message, draws the target as not
// confirmed stopped with the server's step, and offers no Stop: a target's
// stop runs once, so a retry could never succeed. Making the stop fail needs
// macOS's chflags, as TestAFailedStopKeepsTheOwnerAndStartsNoOtherTarget
// does.
func TestViewsPageLiveTargetNotConfirmedStopped(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("making a stop fail needs macOS's chflags")
	}
	self, err := os.Executable()
	require.NoError(t, err)
	cardsDir := t.TempDir()
	var pinned string
	// Registered first so it runs last: after Close, unpin the file and
	// remove the cards directory the failed stop left behind.
	t.Cleanup(func() {
		if pinned != "" {
			require.NoError(t, exec.Command("chflags", "nouchg", pinned).Run())
		}
		require.NoError(t, os.RemoveAll(cardsDir))
	})
	cp, err := New(buildFixtureOptions(t, Options{BuildCacheDir: t.TempDir(),
		CardsDir:     cardsDir,
		Verification: &VerificationConfig{RunsDir: t.TempDir()},
		Live: &LiveConfig{
			Command: []string{self}, Env: append(os.Environ(), liveTargetModeEnv+"=serve"),
			StartTimeout: time.Minute,
		},
	}))
	require.NoError(t, err)
	card := saveBuildStoryCard(t, cp, "chg-live", StatusConfirmed, refundStory("Start a live target"))
	saveStoredBuildFixture(t, cp, card)
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	t.Cleanup(func() { assert.Error(t, cp.Close(), "Close reports the target it could not confirm stopped") })
	ps := startPageServer(t, cp)
	ps.act("pin", func() {
		lt := boundTargetForTest(cp)
		require.NotNil(t, lt)
		pinned = filepath.Join(lt.proc.Dir(), "tmp", "immutable")
		require.NoError(t, os.WriteFile(pinned, []byte("x"), 0o600))
		require.NoError(t, exec.Command("chflags", "uchg", pinned).Run())
	})
	runPageScript(t, ps, map[string]any{"step": msgStopFailed}, liveScriptHelpers+`
const page = await openPage(origin, { link: input.link });
await openLive(page);
await startTarget(commitA);
await until(() => liveState(page) === "running", "the started target did not appear");
await control(origin, "act", { key: "pin" });
page.$("#highbay-live-stop").click();
await until(() => page.$("#highbay-live-stop-yes"), "Stop did not ask first");
page.$("#highbay-live-stop-yes").click();
await until(() => page.$("#highbay-live-stop-outcome"), "the stop's refusal was not shown");
expect(page.text("#highbay-live-stop-outcome") === input.step, "the server's message is not shown as it is: " + page.text("#highbay-live-stop-outcome"));
expect(page.$("#highbay-live-stop-outcome").getAttribute("role") === "alert", "the refusal is not an alert");
await until(() => liveState(page) === "not_confirmed_stopped", "the target is not drawn as not confirmed stopped: " + liveState(page));
expect(page.text("#highbay-live-message") === input.step, "the read's step is not shown: " + page.text("#highbay-live-message"));
expect(page.text("#highbay-live-target").includes(commitA), "the stuck target's commit is not shown");
expect(!page.$("#highbay-live-stop") && !page.$("#highbay-live-stop-confirm"), "Stop is offered for a target whose stop already ran");
await until(() => page.text("#highbay-live-announcement") === "The live target could not be confirmed stopped.", "the failed stop was not announced");
page.close();
`)
}

// TestViewsPageLiveTargetLinksOnlyALocalAddressAndWrapsLongText answers the
// live target read with raw server answers: an address on another host, a
// loopback address with a path, and a plain loopback origin; only the last
// is a link. Long prompt identifiers, versions and a long server message are
// set to wrap inside the card, so they cannot widen the page.
func TestViewsPageLiveTargetLinksOnlyALocalAddressAndWrapsLongText(t *testing.T) {
	cp := pageLiveControlPlane(t, "serve", Options{})
	ps := startPageServer(t, cp)
	longID := "orders.refund." + strings.Repeat("system", 40)
	longVersion := "sha256:" + strings.Repeat("0123456789abcdef", 16)
	running := func(url string) string {
		return `{"live_target":{"state":"running","candidate_commit":"` + strings.Repeat("a", 40) + `","url":"` + url +
			`","started_at":"2026-11-13T14:00:00Z","prompts":[{"prompt_id":"` + longID + `","version":"` + longVersion +
			`","accepted_at":"2026-11-13T14:05:00Z"}]}}`
	}
	longMessage := strings.Repeat("The live target's processes could not be confirmed stopped. ", 12)
	runPageScript(t, ps, map[string]any{
		"answers": []string{
			running("http://example.com:8080"),
			running("http://127.0.0.1:53114/admin"),
			running("http://127.0.0.1:53114"),
			`{"live_target":{"state":"not_confirmed_stopped","candidate_commit":"` + strings.Repeat("b", 40) + `","message":"` + longMessage + `"}}`,
		},
		"longID": longID, "longVersion": longVersion,
	}, liveScriptHelpers+`
for (const body of input.answers) {
  await control(origin, "reply", { key: "GET /live-target", status: 200, body: JSON.parse(body) });
}
const links = ["", "-2", "-3", "-4"];
const pages = [];
for (const suffix of links) {
  const page = await openPage(origin, { link: input.link + suffix });
  await until(() => page.sent("GET", "/change-updates").length > 0, "the initial change list was not read");
  await openLive(page);
  pages.push(page);
}
const address = (page) => page.$("#highbay-live-address");
expect(address(pages[0]).tagName === "CODE", "an address on another host is a link: " + address(pages[0]).outerHTML);
expect(address(pages[1]).tagName === "CODE", "a loopback address with a path is a link: " + address(pages[1]).outerHTML);
expect(address(pages[2]).tagName === "A" && address(pages[2]).getAttribute("href") === "http://127.0.0.1:53114", "a plain loopback origin is not a link: " + address(pages[2]).outerHTML);
const wraps = (page, element, label) => {
  expect(element, label + " is missing");
  expect(page.win.getComputedStyle(element).overflowWrap === "anywhere", label + " does not wrap inside the card");
};
wraps(pages[0], pages[0].$(".highbay-live-prompt-id"), "a long prompt identifier");
expect(pages[0].text(".highbay-live-prompt-id") === input.longID, "the prompt identifier is not shown");
wraps(pages[0], pages[0].$(".highbay-live-version"), "a long version");
wraps(pages[0], address(pages[0]), "an address shown as text");
wraps(pages[2], address(pages[2]), "an address shown as a link");
expect(pages[3].$("#highbay-live-target").dataset.state === "not_confirmed_stopped", "the answer was not drawn as not confirmed stopped");
wraps(pages[3], pages[3].$("#highbay-live-message"), "a long server message");
for (const page of pages) {
  page.close();
}
`)
}

// TestViewsPageModelConnectionForm shows the choice the hand-over of
// anthropic's settings selected, with the form open on its kind, only that
// kind's fields displayed and the shared field styles, keeps typed text when
// the user leaves the view and comes back, has a provider whose settings are
// not held refused verbatim with its key variable named and the choice
// unchanged, applies a gateway and shows the new choice, opens a new page on
// the gateway kind, and checks that no answer the page received, nothing it
// shows and nothing in the diagnostics history holds the provider key.
func TestViewsPageModelConnectionForm(t *testing.T) {
	const secret = "sk-page-test-secret-value"
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
	cp := pageLiveControlPlane(t, "serve", Options{TracerProvider: provider})
	handOverSettings(t, cp, &genhighbay.HandOverModelSettingsPayload{Anthropic: map[string]string{"ANTHROPIC_API_KEY": secret}})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, map[string]any{"secret": secret}, liveScriptHelpers+`
// Keep the automatic card read pending while the independently loaded model
// form becomes usable. A read has no unsaved input to lose on leaving.
await control(origin, "hold", { key: "GET /changes/chg-live" });
const page = await openPage(origin, { link: input.link });
await openLive(page);
await until(() => page.text("#highbay-model-current").includes("anthropic / claude-sonnet-4-6"), "the start-up choice is not shown: " + page.text("#highbay-model-current"));
await until(() => page.sent("GET", "/changes/chg-live").some((r) => r.status === undefined), "the initial card read was not held pending");
expect(page.text("#highbay-model-current").startsWith("Live targets started from now on use"), "the choice does not say what it applies to");
expect(page.text("#highbay-model-note").includes("live targets started afterwards") && page.text("#highbay-model-note").includes("verification is not affected"), "the note does not say what the choice changes: " + page.text("#highbay-model-note"));
expect(page.text("#highbay-model-note").includes("run on this computer as ordinary processes") && page.text("#highbay-model-note").includes("test writer’s tests Highbay accepted") && page.text("#highbay-model-note").includes("Highbay gives it no provider setting") && page.text("#highbay-model-note").includes("can still read keys your other programs hold"), "the model note must explain the live program's actual access and accepted-test boundary");
const shown = (p, id) => p.win.getComputedStyle(p.$(id)).display !== "none";
expect(page.$("input[name='highbay-model-kind'][value='direct']").checked, "the form does not open on the current choice's kind");
expect(shown(page, "#highbay-model-direct-fields") && !shown(page, "#highbay-model-gateway-fields"), "the unused kind's fields are displayed");
for (const id of ["#highbay-model-provider", "#highbay-model-name", "#highbay-model-gateway"]) {
  expect(page.$(id).classList.contains("highbay-input") && page.$(id).closest("label").classList.contains("highbay-field-label"), id + " does not use the shared field styles");
}
const apply = page.$("#highbay-model-apply");
expect(!page.beforeUnload(), "a loaded model form without edits blocked leaving");
await control(origin, "release", { key: "GET /changes/chg-live" });
await until(() => page.sent("GET", "/changes/chg-live").some((r) => r.status === 200), "the initial card read did not settle");
expect(apply.tagName === "BUTTON" && apply.type === "submit" && apply.classList.contains("shadcn-btn-primary"), "Apply is not the view's primary submit button");
const select = page.$("#highbay-model-provider");
select.value = "openai";
select.dispatchEvent(new page.win.Event("change", { bubbles: true }));
page.type(page.$("#highbay-model-name"), "gpt-5.4-mini");
page.win.location.hash = "link=pending-model-link";
await until(() => page.$("#highbay-pending-link") && page.win.location.hash === "", "a model draft did not defer and scrub a same-tab link");
expect(page.beforeUnload() && page.$("#highbay-open-pending-link").disabled, "model input did not protect unload and pending-link opening");
page.$("#highbay-nav-story").click();
await until(() => page.state() === "story" && !page.$("#highbay-model-form"), "the Story view did not open");
page.$("#highbay-nav-live").click();
await until(() => page.$("#highbay-model-name"), "the Live target view did not open again");
expect(page.$("#highbay-model-name").value === "gpt-5.4-mini" && page.$("#highbay-model-provider").value === "openai", "leaving the view lost the typed text: " + page.$("#highbay-model-name").value + " / " + page.$("#highbay-model-provider").value);
page.$("#highbay-model-apply").click();
await until(() => page.$("#highbay-model-outcome"), "the refusal was not shown");
expect(page.text("#highbay-model-outcome").includes("provider openai needs OPENAI_API_KEY: export it, with any OPENAI_ request settings it needs, in the shell your coding agent starts from and restart the agent, whose first Highbay tool call hands the settings over, or in a shell where you run hbay; the project server keeps running. Or choose a model gateway."), "the refusal is not shown verbatim: " + page.text("#highbay-model-outcome"));
expect(page.$("#highbay-model-outcome").getAttribute("role") === "alert", "the refusal is not an alert");
expect(page.text("#highbay-model-current").includes("anthropic / claude-sonnet-4-6"), "a refused change replaced the shown choice");
expect(page.beforeUnload() && page.$("#highbay-open-pending-link").disabled, "a refused model Apply cleared its unsaved guard");

page.$("input[name='highbay-model-kind'][value='gateway']").click();
await until(() => shown(page, "#highbay-model-gateway-fields"), "the gateway fields were not displayed");
expect(!shown(page, "#highbay-model-direct-fields"), "the provider fields are still displayed beside the gateway's");
const gateway = page.$("#highbay-model-gateway");
page.type(gateway, "127.0.0.1:65536");
page.$("#highbay-model-apply").click();
await until(() => page.requests.some((r) => r.method === "POST" && r.path === "/cp1/model" && r.body.gateway_addr === "127.0.0.1:65536" && r.status === 422), "the invalid gateway was not refused over HTTP");
await until(() => page.$("#highbay-model-outcome") && page.$("#highbay-model-outcome").getAttribute("role") === "alert" && !page.$("#highbay-model-apply").disabled, "the invalid gateway refusal was not shown");
expect(page.text("#highbay-model-current").includes("anthropic / claude-sonnet-4-6"), "an invalid gateway replaced the chosen model");
expect(page.$("#highbay-model-gateway") === gateway && gateway.value === "127.0.0.1:65536", "the refusal lost the typed gateway address");
expect(!page.text("#highbay-model-outcome").includes("127.0.0.1:65536"), "the refusal echoed the submitted address");
page.type(page.$("#highbay-model-gateway"), "127.0.0.1:9090");
page.$("#highbay-model-apply").click();
await until(() => page.text("#highbay-model-outcome") === "Live targets started from now on use the model gateway at 127.0.0.1:9090.", "the accepted change was not shown: " + page.text("#highbay-model-outcome"));
await until(() => page.text("#highbay-model-current").includes("the model gateway at 127.0.0.1:9090"), "the new choice was not read again");
expect(page.sent("POST", "/cp1/model").length === 3, "each Apply should send one request");
expect(!page.beforeUnload() && !page.$("#highbay-open-pending-link").disabled, "a successful Apply did not release its saved model input");
page.type(page.$("#highbay-model-gateway"), "127.0.0.1:9876");
expect(page.beforeUnload() && page.$("#highbay-open-pending-link").disabled, "editing after Apply did not guard the next draft");
page.$("#highbay-model-discard").click();
expect(!page.beforeUnload() && !page.$("#highbay-open-pending-link").disabled && page.$("#highbay-model-gateway").value === "", "local Model discard did not resolve only unsent input");
expect(page.doc.activeElement === page.$("#highbay-model-title"), "discarding a remounted model form lost keyboard focus");
expect(page.text("#highbay-model-current").includes("127.0.0.1:9090") && page.sent("POST", "/cp1/model").length === 3, "local discard changed the owner's connection");

const second = await openPage(origin, { link: input.link + "-2" });
await openLive(second);
await until(() => second.text("#highbay-model-current").includes("127.0.0.1:9090"), "a new page did not read the gateway choice");
expect(second.$("input[name='highbay-model-kind'][value='gateway']").checked && shown(second, "#highbay-model-gateway-fields") && !shown(second, "#highbay-model-direct-fields"), "a new page does not open on the current choice's kind");

const answered = [...page.requests, ...second.requests].filter((r) => r.answer !== undefined);
expect(answered.some((r) => r.path === "/cp1/model" && r.method === "GET") && answered.some((r) => r.path === "/cp1/model" && r.method === "POST" && r.status === 422), "the scan did not see the model reads and the refusal");
for (const r of answered) {
  expect(!r.answer.includes(input.secret), "the answer to " + r.method + " " + r.path + " holds the provider key");
}
expect(!page.doc.body.textContent.includes(input.secret) && !second.doc.body.textContent.includes(input.secret), "a page shows the provider key");
second.close();
const history = await api(origin, "GET", "/traces?status=all&limit=100", undefined, input.guide);
expect(history.status === 200 && !JSON.stringify(history.body).includes(input.secret), "the diagnostics history holds the provider key");
page.close();
`)
	for _, span := range exporter.GetSpans().Snapshots() {
		assertSpanNeverContainsValue(t, span, secret)
	}
}

// TestViewsPageLiveAndModelReadsRefuseOtherRoles checks over the page's
// server that the guide and the test writer are refused both reads, and
// that the builder may read the live target but not the model connection.
func TestViewsPageLiveAndModelReadsRefuseOtherRoles(t *testing.T) {
	cp := pageLiveControlPlane(t, "serve", Options{})
	ps := startPageServer(t, cp)
	runPageScript(t, ps, nil, liveScriptHelpers+`
for (const [token, who] of [[input.guide, "the guide"], [input.testWriter, "the test writer"]]) {
  for (const path of ["/live-target", "/cp1/model"]) {
    const out = await api(origin, "GET", path, undefined, token);
    expect(out.status === 403, who + " was not refused GET " + path + ": " + JSON.stringify(out));
  }
}
const live = await api(origin, "GET", "/live-target", undefined, input.builder);
expect(live.status === 200 && live.body.live_target.state === "none", "the builder could not read the live target: " + JSON.stringify(live));
const model = await api(origin, "GET", "/cp1/model", undefined, input.builder);
expect(model.status === 403, "the builder was not refused the model connection: " + JSON.stringify(model));
`)
}

// pageLiveControlPlane returns a page control plane whose live targets are
// the fake target started through the real `verify-run live` path in mode.
// No model is chosen until a test hands over settings or configures one. Its
// waiting read answers quickly.
func pageLiveControlPlane(t *testing.T, mode string, opts Options) *ControlPlane {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	opts.BuildCacheDir = t.TempDir()
	opts.Verification = &VerificationConfig{RunsDir: t.TempDir()}
	opts.Live = &LiveConfig{
		Command:      []string{self},
		Env:          append(os.Environ(), liveTargetModeEnv+"="+mode),
		StartTimeout: time.Minute,
	}
	cp := pageControlPlane(t, buildFixtureOptions(t, opts))
	card := saveBuildStoryCard(t, cp, "chg-live", StatusConfirmed, refundStory("Start a live target"))
	saveStoredBuildFixture(t, cp, card)
	return cp
}
