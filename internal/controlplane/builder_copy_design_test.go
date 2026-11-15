// These tests exercise Verify's canonical design and immutable merge-base gates
// through real Git and ingress owners without executing candidate programs.
package controlplane

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuilderCopyV1CandidateDesign compares canonical design sets, retaining
// equivalent formatting and the design walk's excluded files in both profiles.
func TestBuilderCopyV1CandidateDesign(t *testing.T) {
	for _, stored := range []bool{false, true} {
		profile := "written"
		if stored {
			profile = "stored"
		}
		for _, scenario := range []string{"changed", "added", "removed", "canonical", "excluded", "merged"} {
			t.Run(profile+"/"+scenario, func(t *testing.T) {
				cp, card, build, candidate, builder := builderCopyV1Fixture(t, stored)
				file := "design/build_fixture.go"
				want := ""
				switch scenario {
				case "changed":
					writeWorkspaceFiles(t, builder, map[string]string{file: "package design\nvar Changed = true\n"})
					want = file
				case "added":
					want = "design/extra.go"
					writeWorkspaceFiles(t, builder, map[string]string{want: "package design\n"})
				case "removed":
					require.NoError(t, os.Remove(filepath.Join(builder, file)))
					want = file
				case "canonical":
					files, err := decodeDesignSource(build.DesignSource)
					require.NoError(t, err)
					writeWorkspaceFiles(t, builder, map[string]string{file: strings.ReplaceAll(files[file], "\n", "\r\n") + "\r\n"})
				case "excluded":
					writeWorkspaceFiles(t, builder, map[string]string{
						"design/extra_test.go": "invalid excluded test source",
						"design/notes.txt":     "not Go", "design/.hidden/x.go": "invalid hidden source",
						"design/vendor/x.go": "invalid vendor source", "design/testdata/x.go": "invalid test data",
						"design/nested/go.mod": "module example.com/nested\n", "design/nested/x.go": "invalid nested source",
					})
				case "merged":
					productionBuildGit(t, cp.currentWorkspaceDir(), "merge", "--ff-only", candidate)
				}
				if scenario != "merged" {
					candidate = commitAllForLayoutTest(t, builder)
				}
				payload := &gensurla.VerifyChangePayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, CandidateCommit: candidate}
				result, err := cp.VerifyChange(asCaller("alice@example.com", CallerBuilder), payload)
				if want == "" {
					require.NoError(t, err)
					assert.Equal(t, candidate, result.CandidateCommit)
					return
				}
				assert.Nil(t, result)
				assertRefusalSays(t, err, "conflict", want)
				assert.ErrorContains(t, err, "call generate_code, commit the agreed design, and call verify_change again")
				assertVerifyDesignTransportRefusal(t, cp, payload, "conflict", want, "generate_code")
			})
		}
	}
}

// TestBuilderCopyV1CandidateBase refuses an outside fix and an outside revert
// at merge-base(C, project HEAD), even after the mutable draft has taken it.
func TestBuilderCopyV1CandidateBase(t *testing.T) {
	for _, stored := range []bool{false, true} {
		profile := "written"
		if stored {
			profile = "stored"
		}
		for _, revert := range []bool{false, true} {
			name := "fix"
			if revert {
				name = "revert"
			}
			t.Run(profile+"/"+name, func(t *testing.T) {
				cp, card, build, builder := builderDesignBaseFixture(t, stored)
				workspace := cp.currentWorkspaceDir()
				file := updateDesignFile
				outside := "package design\nvar A = 3\n"
				if revert {
					outside = "package design\nvar A = 0\n"
				}
				writeWorkspaceFiles(t, workspace, map[string]string{file: outside})
				project := commitAllForLayoutTest(t, workspace)
				// Keep C valid for the agreed design while giving it the outside
				// project commit as an ancestor, and therefore its merge base.
				productionBuildGit(t, builder, "reset", "--hard", project)
				writeBuilderDesignFixture(t, builder, build.DesignSource)
				writeWorkspaceFiles(t, builder, map[string]string{"implementation.go": "package shop\n"})
				candidate := commitAllForLayoutTest(t, builder)
				_, err := cp.UpdateDraft(asCaller("alice@example.com", CallerGuide))
				require.NoError(t, err)
				list, found, err := cp.cards.loadMadeFrom()
				require.NoError(t, err)
				require.True(t, found)
				assert.NoError(t, checkDesignBase(t.Context(), workspace, project, &madeFromSnapshot{Start: list.Start, Entries: list.Entries}), "the live draft has taken this project version")
				payload := &gensurla.VerifyChangePayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, CandidateCommit: candidate}
				result, err := cp.VerifyChange(asCaller("alice@example.com", CallerBuilder), payload)
				assert.Nil(t, result)
				assertRefusalSays(t, err, "design_changed_outside", file+" in commit "+project)
				assert.ErrorContains(t, err, "update_draft, refresh_story and revise_change")
				assertVerifyDesignTransportRefusal(t, cp, payload, "design_changed_outside", file, "revise_change")
			})
		}
	}
}

// builderDesignBaseFixture lists A=0 and then A=1 through the draft owner
// before creating the immutable build of A=2. A later revert to A=0 must not
// replace the newer listed version, even though both texts occur in history.
func builderDesignBaseFixture(
	t *testing.T, stored bool,
) (*ControlPlane, *gensurla.ChangeCardResult, buildFile, string) {
	t.Helper()
	cfg := verificationConfig(t)
	cp := newBuildControlPlane(t, Options{Verification: cfg})
	if !stored {
		cp.verifier = writtenTestVerification{runs: cfg}
	}
	workspace := cp.currentWorkspaceDir()
	writeStarterAt(t, workspace, "example.com/shop")
	writeWorkspaceFiles(t, workspace, map[string]string{updateDesignFile: "package design\nvar A = 0\n"})
	commitAllForLayoutTest(t, workspace)
	readDraftForTest(t, cp)
	writeWorkspaceFiles(t, workspace, map[string]string{updateDesignFile: "package design\nvar A = 1\n"})
	commitAllForLayoutTest(t, workspace)
	guide := asCaller("alice@example.com", CallerGuide)
	_, err := cp.UpdateDraft(guide)
	require.NoError(t, err)
	writeWorkspaceFiles(t, filepath.Join(workspace, draftFolderName), map[string]string{updateDesignFile: "package design\nvar A = 2\n"})
	opID := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
	card, err := cp.ProposeChange(guide, &gensurla.ProposeChangePayload{
		ChangeID: "chg", Prompt: "Keep refunds bounded", Analysis: refundAnalysis("Keep refunds bounded"), StoryRefreshOpID: &opID,
	})
	require.NoError(t, err)
	user := asCaller("alice@example.com", CallerUser)
	card = mustConfirmNoop(t, cp, user, card)
	_, err = cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision})
	require.NoError(t, err)
	assignment, build := readBuilderFixture(t, cp, card)
	productionBuildGit(t, workspace, "branch", "surla/tests-1", build.Base)
	if !stored {
		acceptTests(t, cp, card.ChangeID, build.Base)
	}
	return cp, card, build, assignment.WorkingFolder
}

// assertVerifyDesignTransportRefusal checks the declared HTTP/MCP refusal and
// proves all three entry calls left the run store and open build untouched.
func assertVerifyDesignTransportRefusal(
	t *testing.T, cp *ControlPlane, payload *gensurla.VerifyChangePayload, name, file, recovery string,
) {
	t.Helper()
	before := readBuildFile(t, cp, payload.ChangeID)
	raw, err := gensurla.EncodeVerifyChangePayload(payload)
	require.NoError(t, err)
	server := roleServer(t, cp)
	bodyInput := fmt.Sprintf(`{"request_revision":%q,"candidate_commit":%q}`, payload.RequestRevision, payload.CandidateCommit)
	status, body := httpCall(t, server.URL, "builder", http.MethodPost,
		"/changes/"+payload.ChangeID+"/verifications", bodyInput)
	assert.Equal(t, http.StatusConflict, status)
	assert.Equal(t, `"`+name+`"`, jsonAt(t, []byte(body), "name"))
	assert.Contains(t, body, file)
	assert.Contains(t, body, recovery)
	message, _ := mcpToolCall(t, server.URL, "builder", "verify_change", string(raw))
	assert.Contains(t, message, file)
	assert.Contains(t, message, recovery)
	entries, err := os.ReadDir(cp.runsDir)
	if !os.IsNotExist(err) {
		require.NoError(t, err)
		assert.Empty(t, entries, "refused input must never accept a run")
	}
	assert.Equal(t, before, readBuildFile(t, cp, payload.ChangeID))
}
