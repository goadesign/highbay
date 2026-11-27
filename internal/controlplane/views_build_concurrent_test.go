// This file exercises build reads that overlap a replacement through the served
// page. Working-copy and window effects use the existing simulated boundaries.
package controlplane

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/stretchr/testify/assert"
)

// pagePendingBuildRead observes the real owner's next build answer before the
// HTTP harness delays its delivery. All other service methods remain real.
type pagePendingBuildRead struct {
	*ControlPlane
	// armed selects the first owner answer after its delivery gate is installed.
	armed atomic.Bool
	// captured proves the old answer existed while replacement was pending,
	// before the owner handles its held POST.
	captured chan *genhighbay.ChangeBuild
}

// TestViewsPageBuildIgnoresAReadCapturedDuringPendingReplacement preserves a
// successful replacement's saved start and team receipt when a delayed
// list update reads the previous build while the replacement POST is held. A
// later Team update must coalesce with that read without blocking its own facts.
// This proves page/HTTP behavior, not candidate execution or real window effects.
func TestViewsPageBuildIgnoresAReadCapturedDuringPendingReplacement(t *testing.T) {
	team := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	team.start = func(context.Context) (*genhighbay.ProjectTeam, error) {
		return &genhighbay.ProjectTeam{
			Team:  "page-team",
			Step:  "Build workers were requested.",
			Roles: []*genhighbay.ProjectTeamRole{{Role: "builder", Handle: "page-builder", Running: false}},
		}, nil
	}
	cp := newBuildControlPlane(t, Options{Team: &TeamBinding{Owner: pageOwner, Service: team}})
	cp.changesWait = 400 * time.Millisecond
	cp.changesPoll = 20 * time.Millisecond
	buildableChange(t, cp, "chg")
	service := &pagePendingBuildRead{ControlPlane: cp, captured: make(chan *genhighbay.ChangeBuild, 1)}
	ps := startPageServer(t, service)
	ps.act("hold-old-build-answer", func() {
		// Interception cannot see the gate until the observer is also armed.
		// The owner's next answer will be held.
		ps.answerLater("GET /changes/chg/build", func() { service.armed.Store(true) })
	})
	ps.act("new-attention", func() {
		team.mu.Lock()
		team.snapshot = pageTeamSnapshot("needs_you")
		team.mu.Unlock()
	})
	ps.act("await-old-build-answer", func() {
		select {
		case captured := <-service.captured:
			_, started := captured.Build.AsStarted()
			assert.True(t, started, "the delayed owner answer must contain the previous saved build")
		case <-time.After(10 * time.Second):
			t.Error("the delayed list update did not capture a build answer before replacement")
		}
	})

	runPageScript(t, ps, nil, `
const path = "/changes/chg/build";
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "first Build did not become available");
// Keep the first build's version update behind its direct successful answer.
// Releasing it during replacement forces the background-read interleaving.
await control(origin, "answer-later", { key: "GET /changes" });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "first Build did not remain ready after delaying its list update");
page.$("#highbay-build-start").click();
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled && page.$("#highbay-build-start-team-receipt") && page.sent("GET", path).every((r) => r.status === 200), "the first saved build and its read did not finish");
expect(!("replaces" in page.sent("POST", path)[0].body), "the first Build sent a replacement identity");
const oldStarted = JSON.parse(page.sent("POST", path)[0].answer).build.started_at;
expect(page.text("#highbay-build-details").includes(oldStarted), "the first saved start is missing");

// Hold before the owner handles replacement; its old build remains readable.
await control(origin, "hold", { key: "POST " + path });
await until(() => page.$("#highbay-build-again") && !page.$("#highbay-build-again").disabled, "the first build's background read did not settle before replacement");
page.$("#highbay-build-again").click();
await until(() => page.$("#highbay-build-confirm") && !page.$("#highbay-build-confirm").disabled, "the replacement confirmation did not become ready");
expect(page.sent("POST", path).length === 1, "opening the question sent a replacement");
page.$("#highbay-build-confirm").click();
await until(() => page.sent("POST", path).length === 2 && page.$("#highbay-build-starting"), "the replacement POST was not held pending");
const replacement = page.sent("POST", path)[1];
expect(replacement.status === undefined, "replacement completed before the race was armed");
expect(replacement.body.replaces?.change_id === "chg" && replacement.body.replaces.started_at === oldStarted && Object.keys(replacement.body.replaces).length === 2, "the held replacement did not name exactly the observed prior build");

// Run the delayed list's normal get_build handler during replacement, holding
// only its answer. Capture is armed before that list can dispatch the read.
await control(origin, "act", { key: "hold-old-build-answer" });
const readsBeforeList = page.sent("GET", path).length;
await until(() => page.sent("GET", "/changes").some((r) => r.status === undefined), "the first build's list update was not held");
await control(origin, "release", { key: "GET /changes" });
await until(() => page.sent("GET", path).length > readsBeforeList && page.sent("GET", path).at(-1).status === undefined, "the first build's delayed list update did not start a held read during replacement");
const reads = page.sent("GET", path).length - 1;
const oldRead = page.sent("GET", path)[reads];
expect(page.requests.indexOf(oldRead) > page.requests.indexOf(replacement), "the held build read began before replacement was pending");
await control(origin, "act", { key: "await-old-build-answer" });
// This real Team mutation follows wait/list updates. Its build refresh may
// coalesce with oldRead; another GET is neither required nor safe to await.
await control(origin, "act", { key: "new-attention" });
await until(() => page.$("#highbay-team-attention-builder"), "the real Team update did not reach the page while its build read was held");
expect(oldRead.status === undefined && replacement.status === undefined, "the captured old read or replacement escaped its gate");

await control(origin, "release", { key: "POST " + path });
await until(() => replacement.status === 200 && !page.$("#highbay-build-starting") && page.$("#highbay-build-start-team-receipt"), "the replacement did not display its successful receipt");
const newStarted = JSON.parse(replacement.answer).build.started_at;
expect(newStarted !== oldStarted && page.text("#highbay-build-details").includes(newStarted), "the replacement did not display its new saved start");
const teamReceipt = page.text("#highbay-build-start-team-receipt");
expect(teamReceipt.includes("returned: «") && !teamReceipt.includes("the team was ready"), "the replacement team receipt did not quote the owner's start outcome");
const startTeam = JSON.parse(replacement.answer).team;
expect(startTeam.state === "ready" && startTeam.roles.find((role) => role.role === "builder")?.running === false, "the fixture did not return a successful launch request without a running builder");
expect(teamReceipt.includes("«" + startTeam.step + "»"), "the receipt inferred worker readiness instead of quoting the owner's launch outcome");
const regressions = [];
const observe = () => {
  if (page.text("#highbay-build-details").includes(oldStarted)) regressions.push("previous started_at flashed");
  if (page.text("#highbay-build-start-team-receipt") !== teamReceipt) regressions.push("replacement team receipt cleared");
};
const observer = new page.win.MutationObserver(observe);
observer.observe(page.$("#highbay-build-section"), { childList: true, attributes: true, subtree: true });
await control(origin, "release", { key: "GET " + path });
await until(() => oldRead.status === 200, "the delayed old answer did not arrive");
expect(JSON.parse(oldRead.answer).build.started_at === oldStarted, "the delayed read did not actually contain the previous build");
await until(() => page.sent("GET", path).slice(reads + 1).some((r) => r.status === 200 && JSON.parse(r.answer).build.started_at === newStarted), "the coalesced current read did not finish");
// Flush the delivered answer's promise, render and observer before asserting.
await sleep(0);
observe();
observer.disconnect();
expect(regressions.length === 0, "a read captured during replacement overwrote successful Build facts: " + JSON.stringify(regressions));
expect(page.text("#highbay-build-details").includes(newStarted), "the replacement saved start was lost");
expect(page.text("#highbay-build-start-team-receipt") === teamReceipt, "the replacement team receipt was lost");
expect(page.sent("POST", path).length === 2, "the delayed read repeated the replacement effect");
page.close();
`)
}

// GetBuild preserves the owner's result and signals only the armed read. The
// surrounding answer-later gate then holds delivery while StartBuild completes.
func (s *pagePendingBuildRead) GetBuild(
	ctx context.Context,
	p *genhighbay.GetBuildPayload,
) (*genhighbay.ChangeBuild, error) {
	result, err := s.ControlPlane.GetBuild(ctx, p)
	if err == nil && s.armed.CompareAndSwap(true, false) {
		s.captured <- result
	}
	return result, err
}
