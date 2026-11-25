// This file checks that the Build page assigns the next step to the right role.
package controlplane

import "testing"

// TestViewsPageBuildSentBackBelongsToTheTestWriter follows a preparation
// finding through the served page. A finding is an instruction to the writer,
// never a primary user action or evidence the builder has begun working.
func TestViewsPageBuildSentBackBelongsToTheTestWriter(t *testing.T) {
	ps, _ := pageGenerationFindings(t)
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-build-start") && !page.$("#highbay-build-start").disabled, "Build did not become ready");
expect(page.text("#highbay-build-state") === "Not built yet.", "the initial state must describe a build that has not started");
expect(!page.$("#highbay-build-view-team").classList.contains("highbay-btn-primary"), "team navigation must not be the primary action");
page.$("#highbay-build-start").click();
await until(() => page.sent("POST", "/changes/chg/build").some((r) => r.status === 200) && !page.$("#highbay-build-loading"), "the build start did not settle");
expect(page.text("#highbay-build-builder").includes("assignment unread") && page.text("#highbay-build-test-writer").includes("assignment unread"), "the page must not infer work from a saved assignment");
const writer = await api(origin, "GET", "/assignments/test-writer/chg", undefined, input.testWriter);
expect(writer.status === 200, "the test writer could not read its assignment");
await until(() => page.text("#highbay-build-findings").includes("Finding from build 1.") && !page.$("#highbay-build-loading"), "the saved finding and build read did not settle");
page.$("#highbay-nav-build").click();
await until(() => page.text("#highbay-frame-next").includes("The test writer corrects the tests and reports again."), "Next did not identify who fixes the tests");
expect(!page.$("#highbay-build-state"), "the build card repeats the next-step instruction");
const build = page.$("#highbay-build-section");
expect(page.text(".highbay-frame-attention dd") === "Nothing now.", "the verdict invented a user task");
expect(page.text("#highbay-build-findings").startsWith("What Highbay told the test writer"), "writer instructions must be clearly attributed");
expect(build.querySelectorAll("button.highbay-btn-primary").length === 0, "a sent-back test report has no primary user action");
expect(page.text("#highbay-build-builder").includes("assignment unread") && page.text("#highbay-build-test-writer").includes("assignment read; findings to address"), "the marking read updates only the test writer's assignment");
const saved = JSON.parse(page.sent("POST", "/changes/chg/build").find((r) => r.status === 200).answer).build;
expect(page.text("#highbay-build-details").includes(saved.started_at), "the exact start timestamp must remain in Build details");
expect(page.sent("POST", "/changes/chg/build").length === 1, "reading a finding must not rebuild the change");
page.close();
`)
}
