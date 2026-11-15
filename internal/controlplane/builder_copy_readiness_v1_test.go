// These tests bind the shared builder readiness refusals to Verify's declared
// transport without generating or running candidate code.
package controlplane

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/codes"
)

// TestBuilderCopyV1OutsideModuleReadiness rejects an intermediate directory
// link that resolves the builder's nested module outside its copy. Both test
// sources receive conflict over HTTP/MCP, accept no run, and work after repair.
func TestBuilderCopyV1OutsideModuleReadiness(t *testing.T) {
	for _, stored := range []bool{false, true} {
		profile := "written"
		if stored {
			profile = "stored"
		}
		t.Run(profile, func(t *testing.T) {
			repo := realTempDir(t)
			workspace := filepath.Join(repo, "apps", "shop")
			gitForLayoutTest(t, repo, "init", "-q")
			writeStarterAt(t, workspace, "example.com/shop")
			stories := refundStory("Refund over the limit waits")
			writeTree(t, workspace, map[string]string{"design/build_fixture.go": buildStorySource(t, stories)})
			commitAllForLayoutTest(t, repo)
			recorder := &recordingTracerProvider{}
			opts := verificationSourceOptions(verificationConfig(t), stored)
			opts.WorkspaceDir, opts.TracerProvider = workspace, recorder
			cp := newBuildControlPlane(t, opts)
			card := saveBuildStoryCard(t, cp, "chg", StatusConfirmed, stories)
			roles := buildRolesBuilderOnly
			if !stored {
				roles = buildRolesBuilderAndTestWriter
			}
			build := fixtureOpenBuild(t, cp, card, roles)
			require.NoError(t, cp.cards.saveOpenBuild(build))
			if !stored {
				acceptTests(t, cp, card.ChangeID, mustOpenBuildFile(t, cp).Base)
			}
			candidate := commitBuilderCandidateFixture(t, cp, card)
			assignment, _ := readBuilderFixture(t, cp, card)
			parent := filepath.Dir(assignment.WorkingFolder)
			saved := filepath.Join(t.TempDir(), "saved-apps")
			require.NoError(t, os.Rename(parent, saved))
			require.NoError(t, os.Symlink(filepath.Dir(workspace), parent))
			before := readBuildFile(t, cp, card.ChangeID)
			payload := &gensurla.VerifyChangePayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, CandidateCommit: candidate,
			}
			result, err := cp.VerifyChange(asCaller("alice@example.com", CallerBuilder), payload)
			assert.Nil(t, result)
			assertRefusalSays(t, err, "conflict", "outside source directory")
			const recovery = "Restore the builder's module folder inside its assigned working copy, then call verify_change again."
			assert.ErrorContains(t, err, recovery)
			assert.ErrorContains(t, err, workspace)
			server := roleServer(t, cp)
			status, body := httpCall(t, server.URL, "builder", http.MethodPost,
				"/changes/chg/verifications", fmt.Sprintf(`{"request_revision":%q,"candidate_commit":%q}`, card.RequestRevision, candidate))
			assert.Equal(t, http.StatusConflict, status, body)
			assert.Equal(t, `"conflict"`, jsonAt(t, []byte(body), "name"))
			assert.Contains(t, body, "outside source directory")
			assert.Contains(t, body, recovery)
			arguments, err := gensurla.EncodeVerifyChangePayload(payload)
			require.NoError(t, err)
			message, _ := mcpToolCall(t, server.URL, "builder", "verify_change", string(arguments))
			assert.Contains(t, message, "outside source directory")
			assert.Contains(t, message, recovery)
			entries, err := os.ReadDir(cp.runsDir)
			require.NoError(t, err)
			assert.Empty(t, entries, "no refused call accepts a verification run")
			assert.Equal(t, before, readBuildFile(t, cp, card.ChangeID))
			spans := recorder.matching("surla.controlplane.start_verification")
			require.Len(t, spans, 3)
			for _, span := range spans {
				assert.Equal(t, codes.Unset, span.statusCode, "a declared refusal is not an operational failure")
				assert.Empty(t, span.errors)
			}
			require.NoError(t, os.Remove(parent))
			require.NoError(t, os.Rename(saved, parent))
			result, err = cp.VerifyChange(asCaller("alice@example.com", CallerBuilder), payload)
			require.NoError(t, err, "the unchanged candidate verifies after restoring the actual module folder")
			assert.Equal(t, candidate, result.CandidateCommit)
		})
	}
}
