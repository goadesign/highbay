// These served-page tests use retained run and binary fixtures to check
// promotion guidance. They do not execute candidate code or a model.
package controlplane

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goadesign/surla/internal/verify"
	"github.com/goadesign/surla/internal/verifyrun"
	"github.com/stretchr/testify/require"
)

// binariesPageFixture retains two executions with the same report digest and
// distinct output folders outside the default run layout. Its configured
// promotion target measures different release files, so approval is refused.
type binariesPageFixture struct {
	cp       *ControlPlane
	older    string
	newer    string
	olderDir string
	newerDir string
	digest   string
	// disablePromotion changes the embedding host's current target through
	// its normal resolver, without mutating a target that a read is using.
	disablePromotion func()
}

// TestViewsPageBinariesGuidanceUsesTheSelectedRun follows saved source paths
// through Test details, owner preflight refusal, and selection between equivalent
// reports. Without promotion settings, neither view exposes a private path.
func TestViewsPageBinariesGuidanceUsesTheSelectedRun(t *testing.T) {
	for _, promotion := range []bool{true, false} {
		name := "without promotion settings"
		if promotion {
			name = "with promotion settings"
		}
		t.Run(name, func(t *testing.T) {
			f := newBinariesPageFixture(t, promotion)
			ps := startPageServer(t, f.cp)
			if promotion {
				ps.act("remove-promotion", f.disablePromotion)
			}
			runPageScript(t, ps, map[string]any{
				"promotion": promotion, "older": f.older, "newer": f.newer,
				"olderDir": f.olderDir, "newerDir": f.newerDir, "digest": f.digest,
			}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#surla-build-review-check") && !page.$("#surla-build-review-check").disabled, "the saved current check did not settle");
page.$("#surla-build-review-check").click();
const settled = (id) => page.$("#surla-result-view").dataset.runId === id && !page.$("#surla-cp5-observation-status") &&
  (input.promotion ? page.$("#surla-cp5-approve-blocked") : page.$("#surla-cp5-approve") && !page.$("#surla-cp5-approve").disabled);
await until(() => settled(input.newer) && page.$("#surla-cp4-run-" + input.older), "the latest result and selection did not settle");
const folder = () => page.$("#surla-cp4-binaries-dir code")?.textContent;
if (input.promotion) expect(folder() === input.newerDir, "Test details did not show the latest run's recorded output folder");
else expect(!page.$("#surla-cp4-binaries-dir"), "a project without promotion settings exposed a private output folder");
page.$("#surla-cp4-run-" + input.older).click();
await until(() => settled(input.older), "the older selected result did not settle");
if (!input.promotion) {
  expect(!page.$("#surla-cp4-binaries-dir"), "selection exposed an output folder without promotion settings");
  page.close();
  return;
}
expect(folder() === input.olderDir && !page.text("#surla-cp4-binaries-dir").includes(input.newerDir), "an equivalent report selected another execution's folder");
expect(page.text("#surla-cp5-approve-blocked").includes("whose folder the run's Proof view shows") && !page.$("#surla-cp5-approve"), "owner preflight did not explain the changed release binaries before approval");
expect(page.sent("POST", "/changes/approve").length === 0, "blocked owner preflight submitted approval");
page.$("#surla-cp4-run-" + input.newer).click();
await until(() => settled(input.newer), "the newer run did not settle after the refusal");
expect(folder() === input.newerDir && !page.$("#surla-cp5-approve-error"), "selection carried another run's refusal or output folder");
page.$("#surla-cp4-run-" + input.older).click();
await until(() => settled(input.older), "returning to the selected refusal lost its guidance");
expect(folder() === input.olderDir, "returning to saved evidence mixed execution paths");
const verdict = page.text("#surla-cp4-title");
const listPath = "/changes/chg-selected-inputs/verifications";
const lists = page.sent("GET", listPath).length;
await control(origin, "act", { key: "remove-promotion" });
page.$("#surla-load-verifications").click();
await until(() => page.sent("GET", listPath).slice(lists).some((r) => r.status === 200) && !page.$("#surla-load-verifications").disabled && !page.$("#surla-cp4-binaries-dir") && page.$("#surla-cp5-approve") && !page.$("#surla-cp5-approve").disabled, "a fresh owner read did not remove promotion-only metadata and restore availability");
expect(page.text("#surla-cp4-title") === verdict && page.$("#surla-result-view").dataset.runId === input.older, "metadata refresh replaced the selected report");
expect(page.sent("POST", "/changes/approve").length === 0, "reading source guidance attempted approval");
page.close();
`)
		})
	}
}

// newBinariesPageFixture uses the ordinary persisted-run fixture and the real
// control-plane projections. Only the promotion target's measurements are
// simulated; no binary is executed or published.
func newBinariesPageFixture(t *testing.T, promotion bool) binariesPageFixture {
	t.Helper()
	cp, card, runsDir, candidate := selectedInputsFixture(t)
	f := binariesPageFixture{cp: cp, older: strings.Repeat("1", 32), newer: strings.Repeat("2", 32)}
	for i, runID := range []string{f.older, f.newer} {
		writeListedRunAt(t, runsDir, pageOwner, card, runID,
			time.Date(2026, 11, 13, 9+i, 0, 0, 0, time.UTC), true, candidate)
		setListedRunSource(t, runsDir, runID, verify.TestsWritten)
		workDir := filepath.Join(t.TempDir(), "Retained outputs with spaces", runID)
		editVerificationRequest(t, runsDir, runID, func(req *verifyrun.Request) {
			req.Plan.WorkDir = workDir
		})
		dir := verify.CandidateBinariesDir(workDir)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "orders"), []byte("fixture binary; never executed"), 0o600))
		if i > 0 {
			// Both executions reference one saved report. Generating another
			// fixture report would introduce fresh evidence IDs and a new digest.
			result, err := os.ReadFile(filepath.Join(runsDir, f.older, "result.json"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(runsDir, runID, "result.json"), result, 0o600))
		}
		_, state, err := verifyrun.Read(runsDir, runID)
		require.NoError(t, err)
		if i == 0 {
			f.olderDir, f.digest = dir, state.Report.Digest
		} else {
			f.newerDir = dir
			require.Equal(t, f.digest, state.Report.Digest, "execution identity is not the report digest")
			if promotion {
				target := &promotionTargetStub{
					report: state.Report, artifacts: map[string]string{"orders": strings.Repeat("b", 64)},
				}
				var active atomic.Bool
				active.Store(true)
				cp.resolvePromotionTarget = func(context.Context, string) (PromotionTarget, error) {
					if active.Load() {
						return target, nil
					}
					return nil, nil
				}
				f.disablePromotion = func() { active.Store(false) }
			}
		}
	}
	return f
}
