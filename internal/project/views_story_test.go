// This file proves across roles, through the real project server, that the
// user's views page shows the Story evaluation the guide starts with its own
// credential: while it runs, when it fails or succeeds, and after a restart.
package project

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/goadesign/surla/internal/controlplane"
)

// storyRefreshWait follows the control plane's five-minute Story refresh
// allowance, plus process cleanup and observation, as its real refresh tests do.
const storyRefreshWait = 5*time.Minute + 30*time.Second

// TestViewsPageShowsTheGuidesStoryEvaluation has the guide start a Story
// evaluation with its own credential while the user's page shows the Story
// stage. A stand-in Go toolchain holds the evaluation until the test lets
// it fail. The page shows it running, then failed with the server's
// diagnostic, without a reload; the user's session cannot start one; an
// idle page adds nothing to the diagnostics history; and after a restart
// the stage shows that no evaluation has run.
func TestViewsPageShowsTheGuidesStoryEvaluation(t *testing.T) {
	node := requireViewsPageTools(t)
	ws := storyWorkspace(t, map[string]string{
		"go.mod":           "module example.com/orders\n\ngo 1.24\n",
		"go.sum":           "",
		"design/design.go": "package design\n",
	})
	release := filepath.Join(t.TempDir(), "release")
	installHeldToolchain(t, node, release)
	provider := sdktrace.NewTracerProvider()
	t.Cleanup(func() { require.NoError(t, provider.Shutdown(t.Context())) })
	vp := startViewsProject(t, controlplane.Options{WorkspaceDir: ws, TracerProvider: provider})
	guide := vp.agent(t, "guide")
	link, err := vp.link()
	require.NoError(t, err)
	runViewsPageScript(t, node, map[string]any{
		"origin": vp.state.Origin(), "link": link, "guide": guide, "release": release,
	}, `
  const page = await openPage(origin, { link: input.link });
  await until(() => page.$("#surla-change-list-empty"), "the page did not open the empty project");
  page.$("#surla-nav-story").click();
  await until(() => page.$("#surla-story-draft") && page.$("#surla-story-draft").dataset.state === "none", "the Story stage did not show that no evaluation has run");

  const session = page.sent("GET", "/changes")[0].headers.Authorization;
  const byUser = await fetch(origin + "/workspace/story/refresh", { method: "POST", headers: { Authorization: session, "Sec-Fetch-Site": "same-origin", Origin: origin } });
  expect(byUser.status === 403, "the user's session started a Story evaluation: " + byUser.status);

  const started = await api(origin, "POST", "/workspace/story/refresh", undefined, input.guide);
  expect(started.status === 200 && started.body.state === "running", "the guide could not start an evaluation: " + JSON.stringify(started));
  await until(() => page.$("#surla-story-draft").dataset.state === "running", "the user's page did not show the guide's running evaluation without a reload", 15000);
  expect(page.text("#surla-story-draft-running").includes(started.body.snapshot_digest), "the page shows another evaluation than the guide's");

  fs.writeFileSync(input.release, "");
  await until(() => page.$("#surla-story-draft").dataset.state === "failed", "the user's page did not show the failed evaluation without a reload", 20000);
  const read = await api(origin, "GET", "/workspace/story/refresh", undefined, input.guide);
  expect(read.body.state === "failed" && read.body.op_id === started.body.op_id, "the guide reads another evaluation: " + JSON.stringify(read.body));
  expect(page.text("#surla-story-draft-diagnostic") === read.body.diagnostic, "the page does not show the server's diagnostic: " + page.text("#surla-story-draft-diagnostic"));

  const history = async () => {
    const out = await api(origin, "GET", "/traces?status=all&limit=100", undefined, input.guide);
    expect(out.status === 200, "diagnostics could not be read: " + JSON.stringify(out));
    return out.body.spans;
  };
  const before = await history();
  const waits = page.sent("GET", "/change-updates").length;
  await until(() => page.sent("GET", "/change-updates").length >= waits + 2, "the idle page stopped waiting", 20000);
  const after = await history();
  const added = after.slice(0, after.length - before.length).map((span) => span.name).sort();
  expect(JSON.stringify(added) === JSON.stringify(["surla.controlplane.list_traces", "surla.request.list_traces"]), "an idle page added diagnostics: " + JSON.stringify(added));

  page.close();
  await control(origin, "restart");
  const fresh = await (await fetch(origin + "/__test/link", { method: "POST" })).text();
  const again = await openPage(origin, { link: fresh });
  await until(() => again.$("#surla-change-list-empty"), "the page did not open after the restart");
  again.$("#surla-nav-story").click();
  await until(() => again.$("#surla-story-draft") && again.$("#surla-story-draft").dataset.state === "none", "after a restart the Story stage did not show that no evaluation has run");
  again.close();
`)
}

// TestViewsPageShowsTheGuidesSucceededStory has the guide evaluate a real
// Story design through the project server with the Go toolchain; the
// user's page shows the evaluation running and then its flows, without a
// reload.
func TestViewsPageShowsTheGuidesSucceededStory(t *testing.T) {
	if testing.Short() {
		t.Skip("runs the Go toolchain; the full suite (go test without -short) covers it")
	}
	node := requireViewsPageTools(t)
	disableStoryFixtureFetching(t)
	files := succeededStoryFixture(t)
	ws := storyWorkspace(t, files)
	vp := startViewsProject(t, controlplane.Options{WorkspaceDir: ws})
	guide := vp.agent(t, "guide")
	link, err := vp.link()
	require.NoError(t, err)
	runViewsPageScriptWithTimeout(t, node, map[string]any{
		"origin": vp.state.Origin(), "link": link, "guide": guide,
		"refreshWaitMs": storyRefreshWait.Milliseconds(),
	}, `
  const page = await openPage(origin, { link: input.link });
  await until(() => page.$("#surla-change-list-empty"), "the page did not open the empty project");
  page.$("#surla-nav-story").click();
  await until(() => page.$("#surla-story-draft") && page.$("#surla-story-draft").dataset.state === "none", "the Story stage did not open");
  const started = await api(origin, "POST", "/workspace/story/refresh", undefined, input.guide);
  expect(started.status === 200 && started.body.state === "running", "the guide could not start an evaluation: " + JSON.stringify(started));
  try {
  await until(() => page.$("#surla-story-draft").dataset.state === "running", "the user's page did not show the running evaluation without a reload", 15000);
  } catch (err) {
    let current;
    try {
      current = await api(origin, "GET", "/workspace/story/refresh", undefined, input.guide);
    } catch (readErr) {
      current = { readError: String(readErr) };
    }
    const draft = page.$("#surla-story-draft");
    throw new Error(String(err) + "; refresh read: " + JSON.stringify(current) + "; last draft state: " + draft?.dataset.state + "; draft: " + draft?.textContent);
  }
  try {
    await until(() => ["succeeded", "failed"].includes(page.$("#surla-story-draft")?.dataset.state), "the evaluation did not finish", input.refreshWaitMs);
  } catch (err) {
    const draft = page.$("#surla-story-draft");
    throw new Error(String(err) + "; last draft state: " + draft?.dataset.state + "; draft: " + draft?.textContent);
  }
  expect(page.$("#surla-story-draft").dataset.state === "succeeded", "the evaluation did not succeed: " + page.text("#surla-story-draft"));
  const flows = page.$$("#surla-story-draft .surla-story-flow");
  expect(JSON.stringify(flows.map((f) => [f.dataset.flowKey, f.querySelector(".surla-story-mark").textContent])) === JSON.stringify([["instant-refund", "will be tested end to end"], ["high-value-refund", "draft: not tested"]]), "the guide's Story is not drawn: " + page.text("#surla-story-draft"));
  page.close();
`, storyRefreshWait+time.Minute)
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(ws, filepath.FromSlash(name)))
		require.NoError(t, err)
		require.Equal(t, want, string(got), "evaluation preserves the prepared fixture input")
	}
}

// storyWorkspace writes files into a new workspace directory and returns it.
func storyWorkspace(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	}
	return dir
}

// installHeldToolchain puts a stand-in go command first on PATH, keeping
// node reachable. The stand-in waits until release exists (at most 30
// seconds), then fails, so a Story evaluation stays running until the test
// releases it and then fails with the server's diagnostic.
func installHeldToolchain(t *testing.T, node, release string) {
	t.Helper()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		"i=0\n" +
		"while [ ! -e '" + release + "' ] && [ $i -lt 600 ]; do sleep 0.05; i=$((i+1)); done\n" +
		"echo 'the stand-in toolchain stops here' >&2\n" +
		"exit 3\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o700))
	t.Setenv("PATH", bin+":"+filepath.Dir(node)+":/usr/bin:/bin")
}
