// This file exercises the draft's publication, isolation and recovery through
// the owner that refresh_story uses, without running candidate code.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/designversion"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	goa "goa.design/goa/v3/pkg"
)

// TestDraftFirstUsePreservesAnExistingFolder rejects an unknown published
// folder before writing a marker, provenance or replacement design.
func TestDraftFirstUsePreservesAnExistingFolder(t *testing.T) {
	for _, marked := range []bool{false, true} {
		t.Run(fmt.Sprintf("marked=%t", marked), func(t *testing.T) {
			project := t.TempDir()
			writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar FromProject = 1\n"))
			folder := filepath.Join(project, draftFolderName)
			original := map[string]string{"notes.txt": "Keep this preexisting work.\n"}
			if marked {
				original[".gitignore"] = "# surla draft 1\n*\n"
			}
			writeWorkspaceFiles(t, folder, original)
			cp := newControlPlane(t, Options{WorkspaceDir: project})
			_, err := cp.RefreshStory(WithPrincipal(t.Context(), Principal{ID: "owner", Kind: CallerGuide}))
			assertServiceError(t, err, "unprocessable")
			assert.ErrorContains(t, err, "draft provenance is missing")
			for name, want := range original {
				got, err := os.ReadFile(filepath.Join(folder, name))
				require.NoError(t, err)
				assert.Equal(t, want, string(got))
			}
			if !marked {
				_, err = os.Stat(filepath.Join(folder, ".gitignore"))
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
			for _, name := range []string{madeFromFileName, "made-from"} {
				_, err = os.Stat(filepath.Join(cp.cards.dir, name))
				assert.ErrorIs(t, err, os.ErrNotExist)
			}
			_, err = os.Stat(filepath.Join(project, draftStagingName))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

// TestDraftFirstUsePublishesCanonicalFilesAndProvenance checks the public
// folder result, replacement of owned interrupted staging and restart read.
func TestDraftFirstUsePublishesCanonicalFilesAndProvenance(t *testing.T) {
	project := t.TempDir()
	files := validWorkspaceStub("package design\nvar  A=1\n")
	writeWorkspaceFiles(t, project, files)
	writeWorkspaceFiles(t, filepath.Join(project, draftStagingName), map[string]string{
		".gitignore": "# surla draft 1\n*\n", "leftover": "interrupted",
	})
	opts := Options{WorkspaceDir: project, CardsDir: t.TempDir()}
	cp := newControlPlane(t, opts)
	principal := WithPrincipal(context.Background(), Principal{ID: "owner"})
	none, err := cp.GetStoryRefresh(principal)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(cp.activeWorkspaceDir, draftFolderName), none.DraftFolder)
	assert.True(t, filepath.IsAbs(none.DraftFolder))
	_, err = os.Stat(none.DraftFolder)
	assert.ErrorIs(t, err, os.ErrNotExist)

	snapshot, madeFrom := readDraftForTest(t, cp)
	assert.Equal(t, "package design\n\nvar A = 1\n", snapshot["design/design.go"])
	assert.Equal(t, files["go.mod"], snapshot["go.mod"])
	assert.Equal(t, files["go.sum"], snapshot["go.sum"])
	marker, err := os.ReadFile(filepath.Join(none.DraftFolder, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "# surla draft 1\n*\n", string(marker))
	_, err = os.Stat(filepath.Join(none.DraftFolder, "leftover"))
	assert.ErrorIs(t, err, os.ErrNotExist)
	version := designversion.Digest(snapshot["design/design.go"])
	assert.Equal(t, []madeFromVersion{{Version: version, Entered: 1}}, madeFrom.Entries["design/design.go"])
	text, present, err := cp.cards.loadDesignVersion(version)
	require.NoError(t, err)
	assert.True(t, present)
	assert.Equal(t, snapshot["design/design.go"], text)

	// A restarted owner reads the same draft and persisted list.
	require.NoError(t, cp.Close())
	reopened := newControlPlane(t, opts)
	again, provenance := readDraftForTest(t, reopened)
	assert.Equal(t, snapshot, again)
	assert.Equal(t, madeFrom, provenance)
}

// TestExistingDraftDoesNotReadOrRepairTheProject proves that unrelated links,
// malformed source, and broken module text cannot contaminate a later refresh.
func TestExistingDraftDoesNotReadOrRepairTheProject(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar A = 1\n"))
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	want, provenance := readDraftForTest(t, cp)
	writeWorkspaceFiles(t, project, map[string]string{
		"go.mod": "not a go.mod", "unrelated.go": "not Go", "design/design.go": "broken design",
	})
	require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(project, "unrelated-link")))
	got, actual := readDraftForTest(t, cp)
	assert.Equal(t, want, got)
	assert.Equal(t, provenance, actual)
	mod, err := os.ReadFile(filepath.Join(project, "go.mod"))
	require.NoError(t, err)
	assert.Equal(t, "not a go.mod", string(mod))
	source, err := os.ReadFile(filepath.Join(project, "design/design.go"))
	require.NoError(t, err)
	assert.Equal(t, "broken design", string(source))

	require.NoError(t, os.Remove(filepath.Join(project, draftFolderName, ".gitignore")))
	got, _ = readDraftForTest(t, cp)
	assert.Equal(t, want, got, "repairing the ignore must not read the project")
	marker, err := os.ReadFile(filepath.Join(project, draftFolderName, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "# surla draft 1\n*\n", string(marker))
}

// TestDraftRefusesUnownedStagingAndLinkedFolders proves recovery never erases
// an unrelated folder or follows a link outside the project.
func TestDraftRefusesUnownedStagingAndLinkedFolders(t *testing.T) {
	for _, name := range []string{draftStagingName, draftFolderName} {
		t.Run(name, func(t *testing.T) {
			project := t.TempDir()
			writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
			outside := t.TempDir()
			writeWorkspaceFiles(t, outside, map[string]string{"keep": "user work"})
			require.NoError(t, os.Symlink(outside, filepath.Join(project, name)))
			cp := newControlPlane(t, Options{WorkspaceDir: project})
			ws, dev, ino := cp.currentWorkspaceTarget()
			_, _, err := cp.readDraftSnapshot(context.Background(), ws, dev, ino)
			require.Error(t, err)
			kept, err := os.ReadFile(filepath.Join(outside, "keep"))
			require.NoError(t, err)
			assert.Equal(t, "user work", string(kept))
		})
	}
	t.Run("unmarked staging", func(t *testing.T) {
		project := t.TempDir()
		writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
		writeWorkspaceFiles(t, filepath.Join(project, draftStagingName), map[string]string{"keep": "user work"})
		cp := newControlPlane(t, Options{WorkspaceDir: project})
		ws, dev, ino := cp.currentWorkspaceTarget()
		_, _, err := cp.readDraftSnapshot(context.Background(), ws, dev, ino)
		require.ErrorContains(t, err, "unowned")
		kept, err := os.ReadFile(filepath.Join(project, draftStagingName, "keep"))
		require.NoError(t, err)
		assert.Equal(t, "user work", string(kept))
	})
}

// TestDraftRetriesAStopBeforeTheFirstStagingMarker covers the only unmarked
// staging folder safe to remove: one still empty after its Mkdir succeeded.
func TestDraftRetriesAStopBeforeTheFirstStagingMarker(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
	require.NoError(t, os.Mkdir(filepath.Join(project, draftStagingName), 0o700))
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	files, _ := readDraftForTest(t, cp)
	assert.Equal(t, "package design\n", files["design/design.go"])
	_, err := os.Stat(filepath.Join(project, draftStagingName))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// TestDraftRevalidatesAfterUnlockedGit checks both concurrent writers' states
// and proves committed-design queries never hold the draft's lock.
func TestDraftRevalidatesAfterUnlockedGit(t *testing.T) {
	for _, change := range []string{"list", "draft", "workspace", "HTTP list"} {
		t.Run(change, func(t *testing.T) {
			project := filepath.Join(t.TempDir(), "project")
			writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
			gitForLayoutTest(t, project, "init", "-q")
			gitForLayoutTest(t, project, "add", "go.mod", "go.sum", "design/design.go")
			gitForLayoutTest(t, project, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "Initial")
			realGit, err := exec.LookPath("git")
			require.NoError(t, err)
			gate := t.TempDir()
			started, release := filepath.Join(gate, "started"), filepath.Join(gate, "release")
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
			script := "#!/bin/sh\nfor arg do\nif [ \"$arg\" = 'HEAD^{commit}' ]; then\n" +
				": > " + quote(started) + "\nwhile [ ! -f " + quote(release) + " ]; do sleep 0.01; done\nfi\ndone\n" +
				"exec " + quote(realGit) + " \"$@\"\n"
			bin := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o700))
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			cp := newControlPlane(t, Options{WorkspaceDir: project})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			t.Cleanup(func() { assert.NoError(t, os.WriteFile(release, nil, 0o600)) })
			type outcome struct {
				err    error
				status int
				body   []byte
			}
			var server *httptest.Server
			if change == "HTTP list" {
				server = httptest.NewServer(NewHTTPHandler(cp, workflowAuthenticator(map[string]Principal{
					"owner-token": {ID: "owner", Kind: CallerGuide},
				})))
				t.Cleanup(server.Close)
			}
			result := make(chan outcome, 1)
			go func() {
				if server != nil {
					request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/workspace/story/refresh", nil)
					if err != nil {
						result <- outcome{err: err}
						return
					}
					workflowAuthorization(request, "owner-token")
					request.Header.Set("Content-Type", "application/json")
					request.Header.Set("Accept", "application/json")
					response, err := http.DefaultClient.Do(request)
					if err != nil {
						result <- outcome{err: err}
						return
					}
					body, readErr := io.ReadAll(response.Body)
					result <- outcome{err: errors.Join(readErr, response.Body.Close()), status: response.StatusCode, body: body}
					return
				}
				ws, dev, ino := cp.currentWorkspaceTarget()
				_, _, err := cp.readDraftSnapshot(ctx, ws, dev, ino)
				result <- outcome{err: err}
			}()
			var early *outcome
			require.Eventually(t, func() bool {
				select {
				case got := <-result:
					early = &got
					return true
				default:
					_, err := os.Stat(started)
					return err == nil
				}
			}, 3*time.Second, 10*time.Millisecond)
			if early != nil {
				t.Fatalf("refresh ended before the Git gate: status=%d error=%v body=%s", early.status, early.err, early.body)
			}
			require.True(t, cp.draftMu.TryLock(), "Git must not hold draftMu")
			func() {
				defer cp.draftMu.Unlock()
				if change == "list" || change == "HTTP list" {
					require.NoError(t, cp.cards.saveMadeFrom(madeFromRecord{
						Write: 2, Start: madeFromStart{Write: 2}, Entries: map[string][]madeFromVersion{},
					}))
				} else if change == "draft" {
					writeWorkspaceFiles(t, filepath.Join(project, draftFolderName), map[string]string{
						".gitignore": "# surla draft 1\n*\n", "design/design.go": "user work",
					})
				}
			}()
			if change == "workspace" {
				require.NoError(t, os.Rename(project, project+"-moved"))
				writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar Replacement = 1\n"))
				gitForLayoutTest(t, project, "init", "-q")
				gitForLayoutTest(t, project, "add", "go.mod", "go.sum", "design/design.go")
				gitForLayoutTest(t, project, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "Replacement")
			}
			require.NoError(t, os.WriteFile(release, nil, 0o600))
			select {
			case got := <-result:
				err := got.err
				if change == "HTTP list" {
					require.NoError(t, err)
					assert.Equal(t, http.StatusUnprocessableEntity, got.status, string(got.body))
					var failure goa.ServiceError
					require.NoError(t, json.Unmarshal(got.body, &failure))
					assert.Equal(t, "unprocessable", failure.Name)
					assert.Contains(t, failure.Message, "call refresh_story again")
				} else if change == "workspace" {
					assertServiceError(t, err, "unprocessable")
					assert.ErrorContains(t, err, "was replaced or removed after startup")
				} else {
					assertServiceError(t, err, "unprocessable")
					assert.ErrorContains(t, err, "call refresh_story again")
				}
			case <-ctx.Done():
				t.Fatal("draft read did not finish after the Git query was released")
			}
			if change == "list" || change == "HTTP list" {
				list, _, err := cp.cards.loadMadeFrom()
				require.NoError(t, err)
				assert.Equal(t, uint64(2), list.Start.Write)
				assert.Equal(t, uint64(2), list.Write)
			} else if change == "draft" {
				text, err := os.ReadFile(filepath.Join(project, draftFolderName, "design/design.go"))
				require.NoError(t, err)
				assert.Equal(t, "user work", string(text))
			} else {
				_, exists, err := cp.cards.loadMadeFrom()
				require.NoError(t, err)
				assert.False(t, exists)
				for _, folder := range []string{project, project + "-moved"} {
					for _, name := range []string{draftFolderName, draftStagingName} {
						_, err := os.Stat(filepath.Join(folder, name))
						assert.ErrorIs(t, err, os.ErrNotExist, "neither folder may be changed")
					}
				}
			}
		})
	}
}

// TestDraftInitialCommittedAndUncommittedVersionsEnterTogether proves an
// uncommitted design neither hides S nor changes its initial entry ordering.
func TestDraftInitialCommittedAndUncommittedVersionsEnterTogether(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar A = 0\n"))
	gitForLayoutTest(t, project, "init", "-q")
	gitForLayoutTest(t, project, "add", "go.mod", "go.sum", "design/design.go")
	gitForLayoutTest(t, project, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "Initial")
	head := strings.TrimSpace(gitForLayoutTest(t, project, "rev-parse", "HEAD"))
	writeWorkspaceFiles(t, project, map[string]string{"design/design.go": "package design\nvar A = 1\n"})
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	_, list := readDraftForTest(t, cp)
	assert.Equal(t, head, list.Start.ProjectCommit)
	assert.Equal(t, []madeFromVersion{
		{Version: designversion.Digest("package design\n\nvar A = 0\n"), Entered: 1},
		{Version: designversion.Digest("package design\n\nvar A = 1\n"), Entered: 1},
	}, list.Entries["design/design.go"])
	assert.Equal(t, "M design/design.go", strings.TrimSpace(gitForLayoutTest(t, project, "status", "--porcelain", "--untracked-files=all")))
}

// TestDraftCommittedWalkUsesTheProjectPrefixAndDesignExclusions preserves the
// same file set as ReadDraft when the module is below the repository root.
func TestDraftCommittedWalkUsesTheProjectPrefixAndDesignExclusions(t *testing.T) {
	repo := t.TempDir()
	project := filepath.Join(repo, "app")
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
	writeWorkspaceFiles(t, project, map[string]string{
		"design/sub/design.go":   "package sub\nvar A = 1\n",
		"design/.hidden/bad.go":  "not Go",
		"design/vendor/bad.go":   "not Go",
		"design/testdata/bad.go": "not Go",
		"design/nested/go.mod":   "module example.com/nested\n",
		"design/nested/bad.go":   "not Go",
		"design/ignored_test.go": "not Go",
		"unrelated/bad.go":       "not Go",
	})
	gitForLayoutTest(t, repo, "init", "-q")
	gitForLayoutTest(t, repo, "add", "app")
	gitForLayoutTest(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "Initial")
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	snapshot, provenance := readDraftForTest(t, cp)
	assert.Len(t, provenance.Entries, 2)
	assert.Contains(t, provenance.Entries, "design/design.go")
	assert.Contains(t, provenance.Entries, "design/sub/design.go")
	assert.Len(t, snapshot, 4)
}

// TestRefreshReuseRequiresTheDraftAndTheList checks the real entry point and
// retained operation facts while the sandbox process is held outside all locks.
func TestRefreshReuseRequiresTheDraftAndTheList(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\n"))
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	owner := asCaller("alice@example.com", CallerGuide)
	opID := seedStoryRefreshForTest(t, cp, "alice@example.com", storyRefreshStateSucceeded, nil, "")
	// The project can move independently after the draft exists. Both reuse
	// and the transitional adoption path must continue to use the draft.
	writeWorkspaceFiles(t, project, map[string]string{
		"go.mod": "broken project module", "unrelated.go": "not Go",
		"design/design.go": "broken project design",
	})
	reused, err := cp.RefreshStory(owner)
	require.NoError(t, err)
	assert.Equal(t, opID, *reused.OpID)
	_, err = cp.ProposeChange(owner, &gensurla.ProposeChangePayload{
		ChangeID: "draft-only", Prompt: "Keep refunds bounded", StoryRefreshOpID: &opID,
		Analysis: refundAnalysis("Keep refunds bounded"),
	})
	require.NoError(t, err)

	// A list-only write must replace the succeeded operation too.
	cp.draftMu.Lock()
	list, exists, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	require.True(t, exists)
	list.Write++
	require.NoError(t, cp.cards.saveMadeFrom(list))
	cp.draftMu.Unlock()
	installFakeGo(t, "#!/bin/sh\nexec sleep 600\n")
	started, err := cp.RefreshStory(owner)
	require.NoError(t, err)
	assert.NotEqual(t, opID, *started.OpID)
	assert.Equal(t, filepath.Join(cp.activeWorkspaceDir, draftFolderName), started.DraftFolder)
	cp.storyRefreshMu.Lock()
	key := storyRefreshKey{owner: "alice@example.com", workspaceDir: cp.activeWorkspaceDir,
		workspaceDev: cp.boundWorkspaceDev, workspaceIno: cp.boundWorkspaceIno}
	op := cp.storyRefreshes[key]
	assert.Equal(t, list.Write, op.listWrite)
	assert.Equal(t, &madeFromSnapshot{Start: list.Start, Entries: list.Entries}, op.madeFrom)
	assert.Contains(t, op.designSource, "design/design.go")
	assert.Equal(t, "module example.com/orders\n\ngo 1.24\n", op.goMod)
	cp.storyRefreshMu.Unlock()
}

// TestMissingDraftRestoresTheLatestEligibleRevision exercises confirmed,
// waiting and declined stored revisions with real confirmation and decline.
func TestMissingDraftRestoresTheLatestEligibleRevision(t *testing.T) {
	for _, state := range []string{"confirmed", "waiting", "declined"} {
		t.Run(state, func(t *testing.T) {
			project := t.TempDir()
			writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar A=1\n"))
			cp := newControlPlane(t, Options{WorkspaceDir: project})
			readDraftForTest(t, cp)
			saveCompleteRemakeCard(t, cp, "chg")
			user := asCaller("alice@example.com", CallerUser)
			record := loadMetadataRecord(t, cp, "chg")
			card, err := cp.ConfirmIntent(user, &gensurla.ConfirmIntentPayload{ChangeID: "chg", RequestRevision: record.card.RequestRevision, Answers: map[string]string{}, Examples: record.card.Examples})
			require.NoError(t, err)
			if state != "confirmed" {
				writeWorkspaceFiles(t, filepath.Join(project, draftFolderName), map[string]string{"design/design.go": "package design\nvar A=2\n"})
				files, provenance := readDraftForTest(t, cp)
				source, err := designSourceFromModuleSnapshot(files)
				require.NoError(t, err)
				selected := refreshedSnapshot{designSource: source, goMod: files["go.mod"], architecture: &savedArchitecture{DesignDigest: strings.Repeat("a", 64)}, madeFrom: cloneMadeFromSnapshot(&provenance)}
				cp.draftMu.Lock()
				cp.mu.Lock()
				err = cp.recordRefreshedSnapshot(trace.SpanFromContext(t.Context()), selected)
				cp.mu.Unlock()
				cp.draftMu.Unlock()
				require.NoError(t, err)
				record = loadMetadataRecord(t, cp, "chg")
				proposal := *record.card
				proposal.Status = StatusNeedsConfirmation
				proposal.ConfirmedBy = nil
				proposal.Approval = nil
				proposal.Prompt = "Keep refunds bounded with review"
				proposal.RequestRevision, err = computeCanonicalRevision(&proposal, record.owner, source)
				require.NoError(t, err)
				record.proposed = &storedRevision{card: &proposal, designSource: source, architecture: selected.architecture, goMod: selected.goMod, madeFrom: selected.madeFrom}
				saveMetadataRecord(t, cp, record)
				if state == "declined" {
					_, err = cp.DeclineRevision(user, &gensurla.DeclineRevisionPayload{ChangeID: "chg", RequestRevision: proposal.RequestRevision})
					require.NoError(t, err)
				}
			}
			record = loadMetadataRecord(t, cp, "chg")
			expected := record.latest()
			require.NotNil(t, expected.madeFrom)
			cp.draftMu.Lock()
			list, _, err := cp.cards.loadMadeFrom()
			require.NoError(t, err)
			list.Write++
			version, err := cp.enterDraftVersion(&list, "design/design.go", map[string]string{"design/design.go": "package design\n\nvar A = 3\n"})
			require.NoError(t, err)
			list.Taken["design/design.go"] = madeFromTake{Version: version, Entered: list.Write}
			list.Pending = &pendingDraftWrite{File: "design/design.go", OutsideVersion: version, Before: version, Result: version}
			require.NoError(t, cp.cards.saveMadeFrom(list))
			cp.draftMu.Unlock()
			require.NoError(t, os.RemoveAll(filepath.Join(project, draftFolderName)))
			writeWorkspaceFiles(t, project, map[string]string{"design/design.go": "not Go", "go.sum": "new sum\n"})
			got, provenance := readDraftForTest(t, cp)
			want := "package design\n\nvar A = 1\n"
			if state == "waiting" {
				want = "package design\n\nvar A = 2\n"
			}
			assert.Equal(t, want, got["design/design.go"])
			assert.Equal(t, "new sum\n", got["go.sum"])
			assert.Equal(t, list.Start, provenance.Start)
			assert.Equal(t, expected.madeFrom.Entries, provenance.Entries, "remake restores exact immutable revision history, not later takes")
			restored, _, err := cp.cards.loadMadeFrom()
			require.NoError(t, err)
			assert.Greater(t, restored.Write, list.Write)
			assert.Nil(t, restored.Pending)
			at, found := enteredVersion(expected.madeFrom.Entries["design/design.go"], designversion.Digest(want))
			require.True(t, found)
			assert.Equal(t, madeFromTake{Version: designversion.Digest(want), Entered: at}, restored.Taken["design/design.go"])
			assert.Equal(t, card.RequestRevision, record.card.RequestRevision)
		})
	}
}

// saveCompleteRemakeCard authors storage metadata from the real draft/list
// owners. It is a recovery fixture, not a native evaluator-producer claim.
func saveCompleteRemakeCard(t *testing.T, cp *ControlPlane, id string) {
	t.Helper()
	files, provenance := readDraftForTest(t, cp)
	source, err := designSourceFromModuleSnapshot(files)
	require.NoError(t, err)
	selected := refreshedSnapshot{designSource: source, goMod: files["go.mod"], architecture: &savedArchitecture{DesignDigest: strings.Repeat("a", 64)}, madeFrom: cloneMadeFromSnapshot(&provenance)}
	cp.draftMu.Lock()
	cp.mu.Lock()
	err = cp.recordRefreshedSnapshot(trace.SpanFromContext(t.Context()), selected)
	cp.mu.Unlock()
	cp.draftMu.Unlock()
	require.NoError(t, err)
	saveProposedCardFixture(t, cp, "alice@example.com", id, "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
	record := loadMetadataRecord(t, cp, id)
	record.designSource = source
	record.architecture = selected.architecture
	record.goMod = selected.goMod
	record.madeFrom = selected.madeFrom
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, source)
	require.NoError(t, err)
	saveMetadataRecord(t, cp, record)
}

// TestOlderDraftMarkerRemakesFromProject proves a new drop generation ignores
// older eligible cards and clears the old take/recovery state before evaluation.
func TestOlderDraftMarkerRemakesFromProject(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar A = 1\n"))
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	readDraftForTest(t, cp)
	saveDraftSourceCardFixture(t, cp, "chg", map[string]string{"design/design.go": "package design\n\nvar A = 1\n"})
	cp.draftMu.Lock()
	list, _, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	list.Write++
	list.Start = madeFromStart{Write: list.Write, Dropped: &droppedBuild{
		ChangeID: "chg", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Number: 1,
	}}
	list.Entries = map[string][]madeFromVersion{}
	list.Taken = nil
	require.NoError(t, cp.cards.saveMadeFrom(list))
	cp.draftMu.Unlock()
	writeWorkspaceFiles(t, project, map[string]string{"design/design.go": "package design\nvar A = 3\n"})
	got, provenance := readDraftForTest(t, cp)
	assert.Equal(t, "package design\n\nvar A = 3\n", got["design/design.go"])
	expectedStart := list.Start
	expectedStart.FolderVersions = map[string]string{"design/design.go": designversion.Digest(got["design/design.go"])}
	assert.Equal(t, expectedStart, provenance.Start, "the new generation captures its original folder on first materialization")
	marker, err := os.ReadFile(filepath.Join(project, draftFolderName, ".gitignore"))
	require.NoError(t, err)
	assert.Equal(t, "# surla draft 2\n*\n", string(marker))
	result := storyRefreshResultFromOp(&storyRefreshOp{state: storyRefreshStateRunning,
		madeFrom: &provenance, dropNote: true}, project)
	assert.Contains(t, result.Message, "user dropped change chg; edits made before then are gone")
}

// TestMissingDraftAfterDropStillCapturesTheNewStartCommit covers a drop whose
// previous folder was deleted before refresh could see the older marker.
func TestMissingDraftAfterDropStillCapturesTheNewStartCommit(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar A = 0\n"))
	gitForLayoutTest(t, project, "init", "-q")
	gitForLayoutTest(t, project, "add", "go.mod", "go.sum", "design/design.go")
	gitForLayoutTest(t, project, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "Initial")
	head := strings.TrimSpace(gitForLayoutTest(t, project, "rev-parse", "HEAD"))
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	readDraftForTest(t, cp)
	require.NoError(t, os.RemoveAll(filepath.Join(project, draftFolderName)))
	cp.draftMu.Lock()
	list, _, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	list.Write++
	list.Start = madeFromStart{Write: list.Write, Dropped: &droppedBuild{
		ChangeID: "chg", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Number: 1,
	}}
	list.Entries, list.Taken = nil, nil
	require.NoError(t, cp.cards.saveMadeFrom(list))
	cp.draftMu.Unlock()
	writeWorkspaceFiles(t, project, map[string]string{"design/design.go": "package design\nvar A = 3\n"})
	_, provenance := readDraftForTest(t, cp)
	assert.Equal(t, head, provenance.Start.ProjectCommit)
	assert.Equal(t, list.Start.Write, provenance.Start.Write)
	assert.Equal(t, []madeFromVersion{
		{Version: designversion.Digest("package design\n\nvar A = 0\n"), Entered: list.Write + 1},
		{Version: designversion.Digest("package design\n\nvar A = 3\n"), Entered: list.Write + 1},
	}, provenance.Entries["design/design.go"])
}

// TestDroppedDraftWithMissingIgnoreDoesNotRetainOldEdits covers a deleted
// marker after Drop has published its start but before the draft was remade.
func TestDroppedDraftWithMissingIgnoreDoesNotRetainOldEdits(t *testing.T) {
	project := t.TempDir()
	writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar A = 1\n"))
	cp := newControlPlane(t, Options{WorkspaceDir: project})
	readDraftForTest(t, cp)
	require.NoError(t, os.Remove(filepath.Join(project, draftFolderName, ".gitignore")))
	cp.draftMu.Lock()
	list, _, err := cp.cards.loadMadeFrom()
	require.NoError(t, err)
	list.Write++
	list.Start = madeFromStart{Write: list.Write, Dropped: &droppedBuild{
		ChangeID: "chg", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Number: 1,
	}}
	list.Entries, list.Taken = nil, nil
	require.NoError(t, cp.cards.saveMadeFrom(list))
	cp.draftMu.Unlock()
	writeWorkspaceFiles(t, project, map[string]string{"design/design.go": "package design\nvar A = 3\n"})
	got, provenance := readDraftForTest(t, cp)
	assert.Equal(t, "package design\n\nvar A = 3\n", got["design/design.go"])
	assert.NotEmpty(t, provenance.Entries)
	assert.Equal(t, list.Start.Write, provenance.Start.Write)
}

// TestDroppedUnmarkedDraftPublicationRecoversAtEveryStop restarts the owner at
// each durable boundary, including the list-before-rename interval that must
// never relabel the old dropped text as a new-generation draft.
func TestDroppedUnmarkedDraftPublicationRecoversAtEveryStop(t *testing.T) {
	for _, phase := range []string{"before removal", "after removal", "after list", "after rename"} {
		t.Run(phase, func(t *testing.T) {
			project := t.TempDir()
			initial := validWorkspaceStub("package design\nvar A = 1\n")
			writeWorkspaceFiles(t, project, initial)
			opts := Options{WorkspaceDir: project, CardsDir: t.TempDir()}
			cp := newControlPlane(t, opts)
			readDraftForTest(t, cp)
			require.NoError(t, os.Remove(filepath.Join(project, draftFolderName, ".gitignore")))
			cp.draftMu.Lock()
			list, _, err := cp.cards.loadMadeFrom()
			require.NoError(t, err)
			list.Write++
			list.Start = madeFromStart{Write: list.Write, Dropped: &droppedBuild{
				ChangeID: "chg", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Number: 1,
			}}
			list.Entries, list.Taken = nil, nil
			require.NoError(t, cp.cards.saveMadeFrom(list))
			cp.draftMu.Unlock()
			next := validWorkspaceStub("package design\n\nvar A = 3\n")
			writeWorkspaceFiles(t, project, next)
			stage := validWorkspaceStub(next["design/design.go"])
			stage[".gitignore"] = "# surla draft 2\n*\n"
			writeWorkspaceFiles(t, filepath.Join(project, draftStagingName), stage)
			if phase != "before removal" {
				require.NoError(t, os.RemoveAll(filepath.Join(project, draftFolderName)))
			}
			if phase == "after list" || phase == "after rename" {
				func() {
					cp.draftMu.Lock()
					defer cp.draftMu.Unlock()
					list.Write++
					list.Entries = map[string][]madeFromVersion{}
					// A real list save at this boundary has already captured
					// the complete original folder for the new generation.
					list.Start.FolderVersions = map[string]string{"design/design.go": designversion.Digest(next["design/design.go"])}
					version, err := cp.enterDraftVersion(&list, "design/design.go", draftDesignFiles(next))
					require.NoError(t, err)
					list.Taken = map[string]madeFromTake{
						"design/design.go": {Version: version, Entered: list.Write},
					}
					require.NoError(t, cp.cards.saveMadeFrom(list))
				}()
			}
			if phase == "after rename" {
				require.NoError(t, os.Rename(filepath.Join(project, draftStagingName), filepath.Join(project, draftFolderName)))
				// A published draft must not consult the project on restart.
				writeWorkspaceFiles(t, project, map[string]string{"go.mod": "broken", "design/design.go": "not Go"})
			}
			require.NoError(t, cp.Close())
			reopened := newControlPlane(t, opts)
			got, provenance := readDraftForTest(t, reopened)
			assert.Equal(t, next, got)
			expectedStart := list.Start
			expectedStart.FolderVersions = map[string]string{"design/design.go": designversion.Digest(next["design/design.go"])}
			assert.Equal(t, expectedStart, provenance.Start)
			assert.NotEmpty(t, provenance.Entries)
			marker, err := os.ReadFile(filepath.Join(project, draftFolderName, ".gitignore"))
			require.NoError(t, err)
			assert.Equal(t, "# surla draft 2\n*\n", string(marker))
		})
	}
}

// TestDroppedUnmarkedDraftIsRemovedBeforeTheListWrite fails the real list
// writer after removal, proving the implementation's ordering rather than
// merely reconstructing the expected durable states.
func TestDroppedUnmarkedDraftIsRemovedBeforeTheListWrite(t *testing.T) {
	for _, start := range []uint64{1, 2} {
		t.Run(fmt.Sprint(start), func(t *testing.T) {
			project := t.TempDir()
			writeWorkspaceFiles(t, project, validWorkspaceStub("package design\nvar A = 1\n"))
			opts := Options{WorkspaceDir: project, CardsDir: t.TempDir()}
			cp := newControlPlane(t, opts)
			readDraftForTest(t, cp)
			require.NoError(t, os.Remove(filepath.Join(project, draftFolderName, ".gitignore")))
			list := madeFromRecord{Write: start, Start: madeFromStart{Write: start, Dropped: &droppedBuild{
				ChangeID: "chg", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), Number: 1,
			}}}
			func() {
				cp.draftMu.Lock()
				defer cp.draftMu.Unlock()
				require.NoError(t, cp.cards.saveMadeFrom(list))
			}()
			writeWorkspaceFiles(t, project, map[string]string{"design/design.go": "package design\nvar A = 3\n"})
			blockedWrite := filepath.Join(cp.cards.dir, madeFromStagingFile)
			require.NoError(t, os.Mkdir(blockedWrite, 0o700))
			ws, dev, ino := cp.currentWorkspaceTarget()
			_, _, err := cp.readDraftSnapshot(context.Background(), ws, dev, ino)
			require.Error(t, err)
			_, err = os.Stat(filepath.Join(project, draftFolderName))
			assert.ErrorIs(t, err, os.ErrNotExist, "the old dropped folder must already be gone")
			unchanged, _, err := cp.cards.loadMadeFrom()
			require.NoError(t, err)
			assert.Equal(t, list, unchanged, "the failed save must leave the drop's unmaterialized start")
			require.NoError(t, os.Remove(blockedWrite))
			require.NoError(t, cp.Close())
			reopened := newControlPlane(t, opts)
			got, _ := readDraftForTest(t, reopened)
			assert.Equal(t, "package design\n\nvar A = 3\n", got["design/design.go"])
		})
	}
}

// readDraftForTest reads the actual refresh owner without starting evaluation.
func readDraftForTest(t *testing.T, cp *ControlPlane) (map[string]string, madeFromSnapshot) {
	t.Helper()
	ws, dev, ino := cp.currentWorkspaceTarget()
	files, saved, err := cp.readDraftSnapshot(context.Background(), ws, dev, ino)
	require.NoError(t, err)
	require.NoError(t, designeval.ValidateUTF8Snapshot(files))
	return files, madeFromSnapshot{Start: saved.Start, Entries: saved.Entries}
}

// saveDraftSourceCardFixture supplies an explicit legacy design for draft/card
// recovery consumers; it does not claim a workspace proposal was evaluated.
func saveDraftSourceCardFixture(
	t *testing.T, cp *ControlPlane, changeID string, design map[string]string,
) *gensurla.ChangeCardResult {
	t.Helper()
	saveProposedCardFixture(t, cp, "alice@example.com", changeID, "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
	record := loadMetadataRecord(t, cp, changeID)
	var err error
	record.designSource, err = encodeDesignSource(design)
	require.NoError(t, err)
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
	require.NoError(t, err)
	saveMetadataRecord(t, cp, record)
	return record.card
}
