// These tests bind each test source's missing-build goal to its real transports.
package controlplane

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuilderCopyV1NoBuildRefusals keeps stored-evaluator recovery independent
// of test-writer acceptance. Both owning entries refuse before accepting a run
// or approval, and preserve another change's open record.
func TestBuilderCopyV1NoBuildRefusals(t *testing.T) {
	for _, stored := range []bool{false, true} {
		profile := "written"
		if stored {
			profile = "stored"
		}
		for _, missing := range []string{"no open build", "another change's build"} {
			t.Run(profile+"/"+missing, func(t *testing.T) {
				cp, card, build, candidate, _ := builderCopyV1Fixture(t, stored)
				suite := build.TestWriter.Commit
				if stored {
					suite = verifierRuns(cp.verifier).Plan.SuiteCommit
				}
				target := &promotionTargetStub{artifacts: map[string]string{"shop": strings.Repeat("a", 64)}}
				target.report = passingReport(t, card.RequestRevision, candidate, reportSpec{
					SuiteID: "shop", SuiteCommit: suite, Environment: "test", Artifacts: target.artifacts,
				})
				cp.promotionTarget = target
				open := filepath.Join(cp.cards.dir, openBuildRecordFile)
				var savedOpen []byte
				if missing == "no open build" {
					require.NoError(t, os.Remove(open))
				} else {
					other := buildableChange(t, cp, "other")
					build.ChangeID, build.RequestRevision = other.ChangeID, other.RequestRevision
					require.NoError(t, cp.cards.saveOpenBuild(build))
					var err error
					savedOpen, err = os.ReadFile(open)
					require.NoError(t, err)
				}
				before, err := os.ReadFile(cp.cards.path(card.ChangeID))
				require.NoError(t, err)
				want := fmt.Sprintf("change %q has no build with a test writer, so no tests are accepted for revision %s: the user starts a build on the views page", card.ChangeID, card.RequestRevision)
				if stored {
					want = fmt.Sprintf("change %q has no build for revision %s: the user starts a build on the views page", card.ChangeID, card.RequestRevision)
				}
				server := roleServer(t, cp)
				for _, operation := range []struct{ tool, caller, path, body string }{
					{"verify_change", "builder", "/changes/" + card.ChangeID + "/verifications", fmt.Sprintf(`{"change_id":%q,"request_revision":%q,"candidate_commit":%q}`, card.ChangeID, card.RequestRevision, candidate)},
					{"approve_merge", "user", "/changes/approve", fmt.Sprintf(`{"change_id":%q,"request_revision":%q,"candidate_commit":%q,"report_digest":%q}`, card.ChangeID, card.RequestRevision, candidate, target.report.Digest)},
				} {
					t.Run(operation.tool, func(t *testing.T) {
						// HTTP's verify body has no change_id: it belongs in the path.
						httpBody := operation.body
						if operation.tool == "verify_change" {
							httpBody = fmt.Sprintf(`{"request_revision":%q,"candidate_commit":%q}`, card.RequestRevision, candidate)
						}
						status, body := httpCall(t, server.URL, operation.caller, http.MethodPost, operation.path, httpBody)
						assert.Equal(t, http.StatusConflict, status, body)
						var refusal struct{ Name, Message string }
						require.NoError(t, json.Unmarshal([]byte(body), &refusal))
						assert.Equal(t, "no_build", refusal.Name)
						assert.Equal(t, want, refusal.Message)
						failure, _ := mcpToolCall(t, server.URL, operation.caller, operation.tool, operation.body)
						assert.Contains(t, failure, want)
						if stored {
							assert.NotContains(t, body+failure, "test writer")
							assert.NotContains(t, body+failure, "accepted")
						}
						after, err := os.ReadFile(cp.cards.path(card.ChangeID))
						require.NoError(t, err)
						assert.Equal(t, before, after, "refusals save no approval or card changes")
						entries, err := os.ReadDir(cp.runsDir)
						require.NoError(t, err)
						assert.Empty(t, entries, "refusals accept no run")
						if savedOpen == nil {
							_, err := os.Stat(open)
							assert.ErrorIs(t, err, os.ErrNotExist)
						} else {
							after, err := os.ReadFile(open)
							require.NoError(t, err)
							assert.Equal(t, savedOpen, after)
						}
					})
				}
			})
		}
	}
}
