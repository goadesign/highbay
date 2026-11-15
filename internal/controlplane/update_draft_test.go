// These tests replay design changes through real draft, Git and storage owners.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designversion"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
)

const updateDesignFile = "design/design.go"

// draftUpdateFixture owns only test files and real Git commits. Saved cards
// are explicit storage fixtures, not claims of a native evaluator journey.
type draftUpdateFixture struct {
	t         *testing.T
	cp        *ControlPlane
	dir       string
	branch    string
	candidate string
	// candidateOutside is the user's live text already incorporated in R.
	candidateOutside string
	saves            int
}

// TestUpdateDraftRule5Recovery replays all fourteen corrected-rule cases with
// canonical Go files, real Git history, actual pending writes and UpdateDraft.
func TestUpdateDraftRule5Recovery(t *testing.T) {
	base, guide, user := updateGoTexts(t)
	q := func(s string) string { return strings.Replace(s, `Error("invalid")`, `Error("rejected")`, 1) }
	w := func(s string) string {
		return strings.Replace(s, "Method(\"m76\", func() {\n\t\tResult(Order)\n", "Method(\"m76\", func() {\n\t\tDescription(\"Update an order.\")\n\t\tResult(Order)\n", 1)
	}
	q2 := func(s string) string {
		return strings.Replace(s, "Method(\"m21\", func() {\n\t\tNoSecurity()", "Method(\"m21\", func() {\n\t\tSecurity(JWTAuth)", 1)
	}
	cases := []struct {
		name      string
		guideAdds bool
		plain     bool
		run       func(*draftUpdateFixture)
		want      int
	}{
		{"after file write", false, false, func(f *draftUpdateFixture) { f.stop(true); f.update() }, 1},
		{"plain Go comments after write", false, true, func(f *draftUpdateFixture) {
			f.stop(true)
			before := f.draft()
			f.update()
			assert.Equal(t, before, f.draft())
		}, 1},
		{"second uncommitted outside edit", false, false, func(f *draftUpdateFixture) { f.update(); f.project(q(f.projectText())); f.update() }, 1},
		{"before file write", false, false, func(f *draftUpdateFixture) { f.stop(false); f.update() }, 1},
		{"after write then guide edit", false, false, func(f *draftUpdateFixture) {
			f.stop(true)
			f.guide(w(f.draft()))
			r := f.update()
			assert.Len(t, r.ToCorrect, 1)
		}, 1},
		{"guide between outside edits", false, false, func(f *draftUpdateFixture) {
			f.update()
			f.guide(w(f.draft()))
			f.project(q(f.projectText()))
			f.update()
		}, 1},
		{"uncommitted revert residual", false, false, func(f *draftUpdateFixture) { f.update(); f.project(base); f.update() }, 1},
		{"committed edit and revert", false, false, func(f *draftUpdateFixture) { f.commit(); f.update(); f.project(base); f.commit(); f.update() }, 0},
		{"remake older saved revision", false, false, func(f *draftUpdateFixture) {
			f.project(base)
			f.save()
			f.project(user)
			f.update()
			f.remake()
			assert.NotContains(t, f.draft(), `Method("m92"`)
			f.update()
			f.commit()
		}, 1},
		{"remake from folder", false, false, func(f *draftUpdateFixture) {
			f.project(q(base))
			f.update()
			f.project(q(user))
			f.remake()
			f.guide(w(f.draft()))
			f.project(q2(f.projectText()))
			f.update()
		}, 1},
		{"revert then another edit", false, false, func(f *draftUpdateFixture) { f.update(); f.project(base); f.update(); f.project(q(base)); f.update() }, 0},
		{"build merged then outside edit", true, false, func(f *draftUpdateFixture) {
			f.update()
			f.save()
			f.merge()
			f.update()
			f.project(q(f.projectText()))
			f.update()
		}, 1},
		{"merged build reverted", true, false, func(f *draftUpdateFixture) {
			f.commit()
			f.update()
			f.save()
			prior := f.head()
			f.merge()
			text := gitForLayoutTest(t, f.dir, "show", prior+":"+updateDesignFile)
			f.project(text)
			f.commit()
			f.update()
			f.save()
		}, 0},
		{"drop after take", false, false, func(f *draftUpdateFixture) {
			f.project(q(base))
			f.update()
			f.project(user)
			publishDroppedDraftStartForTest(t, f.cp)
			f.remake()
			f.guide(w(f.draft()))
			f.project(q2(f.projectText()))
			f.update()
		}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, d, p := base, guide, user
			marker := `Method("m92"`
			if tc.guideAdds {
				d, p = user, guide
			}
			if tc.plain {
				b = "package design\n\n// A\n// B\n\n// C\n// D\n// E\n"
				d = "package design\n\n// A\n// B\n\n// C2\n// D2\n\n// E\n"
				p = "package design\n\n// A\n\n// B2\n// X\n\n// C\n// D\n// E\n"
				marker = "// X\n"
			}
			f := newDraftUpdateFixture(t, b, b, d)
			f.project(p)
			tc.run(f)
			assert.Equal(t, tc.want, strings.Count(f.draft(), marker))
			assert.NotContains(t, f.draft(), "<<<<<<<", "corrected rows have no accidental remerge")
		})
	}
}

// TestUpdateDraftMainJourneyPairs preserves one added method across all 24
// guide/user pairs; only genuine same-line conflicts are reported.
func TestUpdateDraftMainJourneyPairs(t *testing.T) {
	base, guide, user := updateGoTexts(t)
	edit := func(from, to string) func(string) string {
		return func(s string) string {
			require.Equal(t, 1, strings.Count(s, from))
			return strings.Replace(s, from, to, 1)
		}
	}
	guides := []struct {
		name string
		edit func(string) string
	}{
		{"none", func(s string) string { return s }},
		{"m44 result", edit("HTTP(func() { POST(\"/orders\") })\n\t\tResult(Order)\n\t})", "HTTP(func() { POST(\"/orders\") })\n\t\tResult(OrderView)\n\t})")},
		{"m92 meta", edit("Payload(UpdatePayload)\n\t\tMeta(\"swagger:summary\", \"x\")\n\t})\n\n\tMethod(\"m21\"", "Payload(UpdatePayload)\n\t})\n\n\tMethod(\"m21\"")},
		{"m21 blank", edit("NoSecurity()\n\n\t\tHTTP(func() { GET", "NoSecurity()\n\t\tHTTP(func() { GET")},
		{"adds m50", edit("\tMethod(\"m21\", func() {", "\tMethod(\"m50\", func() {\n\t\tResult(Order)\n\t})\n\n\tMethod(\"m21\", func() {")},
		{"m76", edit("Method(\"m76\", func() {\n\t\tResult(Order)\n", "Method(\"m76\", func() {\n\t\tDescription(\"Update an order.\")\n\t\tResult(Order)\n")},
	}
	users := updateUserEdits(t)
	conflicts := 0
	for _, g := range guides {
		for _, u := range users {
			t.Run(g.name+"/"+u.name, func(t *testing.T) {
				f := newDraftUpdateFixture(t, base, base, user)
				f.project(guide)
				f.update()
				f.save()
				f.merge()
				f.guide(g.edit(f.draft()))
				f.project(u.edit(f.projectText()))
				r := f.update()
				assert.Equal(t, 1, strings.Count(f.draft(), `Method("m92"`))
				if len(r.ToCorrect) > 0 {
					conflicts++
					assert.Contains(t, f.draft(), "<<<<<<<")
				}
			})
		}
	}
	assert.Equal(t, 2, conflicts)
}

// TestUpdateDraftCrissCross uses a real side-branch candidate and a user merge,
// so C5 is decided by ancestry rather than by a model's merged-in flag.
func TestUpdateDraftCrissCross(t *testing.T) {
	base, guide, user := updateGoTexts(t)
	for _, u := range updateUserEdits(t)[1:] {
		t.Run(u.name, func(t *testing.T) {
			f := newDraftUpdateFixture(t, base, base, user)
			f.project(guide)
			f.commit()
			f.update()
			f.save()
			f.project(strings.Replace(f.projectText(), `Error("invalid")`, `Error("rejected")`, 1))
			f.commit()
			f.update()
			f.merge()
			f.guide(strings.Replace(f.draft(), "Method(\"m76\", func() {\n\t\tResult(Order)\n", "Method(\"m76\", func() {\n\t\tDescription(\"Guide edit.\")\n\t\tResult(Order)\n", 1))
			f.project(u.edit(f.projectText()))
			r := f.update()
			require.Len(t, r.ToCorrect, 1)
			assert.Equal(t, canonicalUpdateText(t, f.projectText()), f.draft())
			assert.NotContains(t, f.draft(), "<<<<<<<")
			again := f.update()
			assert.Empty(t, again.Taken)
			assert.Empty(t, again.Merged)
			assert.Empty(t, again.ToCorrect)
			f.commit()
			list := f.list()
			assert.NoError(t, checkDesignBase(t.Context(), f.dir, f.head(), &madeFromSnapshot{Start: list.Start, Entries: list.Entries}))
		})
	}
}

// TestUpdateDraftKeepsOriginalFolderIdentity proves that identical later
// Entries/Taken cannot reconstruct the original folder after version re-entry.
func TestUpdateDraftKeepsOriginalFolderIdentity(t *testing.T) {
	v0 := "package design\nvar A=0\nvar B=0\n"
	v1 := "package design\nvar A=1\nvar B=0\n"
	v2 := "package design\nvar A=2\nvar B=0\n"
	v3 := "package design\nvar A=3\nvar B=1\n"
	for _, initial := range []string{v1, v0} {
		t.Run(fmt.Sprint(initial == v1), func(t *testing.T) {
			f := newDraftUpdateFixture(t, v0, initial, initial)
			start := f.list().Start
			f.project(v2)
			f.update()
			f.guide(v1)
			f.save()
			f.project(v3)
			r := f.update()
			if initial == v1 {
				assert.Equal(t, canonicalUpdateText(t, v3), f.draft())
				assert.Empty(t, r.ToCorrect)
			} else {
				assert.NotEmpty(t, r.ToCorrect)
				assert.Contains(t, f.draft(), "<<<<<<<")
			}
			assert.Equal(t, start, f.list().Start)
		})
	}
}

// TestPendingDraftRecoveryKeepsTheOriginalProjectCommit advances HEAD after
// the pending write; completion retains the commit observed before the stop.
func TestPendingDraftRecoveryKeepsTheOriginalProjectCommit(t *testing.T) {
	base, guide, user := updateGoTexts(t)
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprint(after), func(t *testing.T) {
			f := newDraftUpdateFixture(t, base, base, guide)
			f.project(user)
			f.commit()
			original := f.head()
			f.stop(after)
			gitForLayoutTest(t, f.dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "Advance HEAD after interrupted take")
			f.update()
			assert.Equal(t, original, f.list().Taken[updateDesignFile].ProjectCommit)
			assert.Nil(t, f.list().Pending)
			assert.Equal(t, 1, strings.Count(f.draft(), `Method("m92"`))
		})
	}
}

// newDraftUpdateFixture commits the base before materializing the actual draft.
func newDraftUpdateFixture(t *testing.T, base, initial, draft string) *draftUpdateFixture {
	t.Helper()
	dir := t.TempDir()
	writeWorkspaceFiles(t, dir, validWorkspaceStub(base))
	gitForLayoutTest(t, dir, "init", "-q")
	gitForLayoutTest(t, dir, "add", "go.mod", "go.sum", updateDesignFile)
	gitForLayoutTest(t, dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "Initial design")
	writeWorkspaceFiles(t, dir, map[string]string{updateDesignFile: initial})
	cp := newControlPlane(t, Options{WorkspaceDir: dir})
	readDraftForTest(t, cp)
	f := &draftUpdateFixture{t: t, cp: cp, dir: dir, branch: strings.TrimSpace(gitForLayoutTest(t, dir, "symbolic-ref", "--short", "HEAD"))}
	f.guide(draft)
	return f
}

// update calls the real public owner and returns its result without evaluation.
func (f *draftUpdateFixture) update() *gensurla.DraftUpdateResult {
	f.t.Helper()
	r, err := f.cp.UpdateDraft(asCaller("alice@example.com", CallerGuide))
	require.NoError(f.t, err)
	return r
}

// project writes only the test-owned project design.
func (f *draftUpdateFixture) project(text string) {
	f.t.Helper()
	writeWorkspaceFiles(f.t, f.dir, map[string]string{updateDesignFile: text})
}

// guide writes only the test-owned draft design.
func (f *draftUpdateFixture) guide(text string) {
	f.t.Helper()
	writeWorkspaceFiles(f.t, filepath.Join(f.dir, draftFolderName), map[string]string{updateDesignFile: text})
}

// draft reads the result including any intentional conflict markers.
func (f *draftUpdateFixture) draft() string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, draftFolderName, updateDesignFile))
	require.NoError(f.t, err)
	return string(b)
}

// projectText reads the actual live design supplied to the owner.
func (f *draftUpdateFixture) projectText() string {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir, updateDesignFile))
	require.NoError(f.t, err)
	return string(b)
}

// head returns the real project commit.
func (f *draftUpdateFixture) head() string {
	return strings.TrimSpace(gitForLayoutTest(f.t, f.dir, "rev-parse", "HEAD"))
}

// commit records only this fixture's project design.
func (f *draftUpdateFixture) commit() {
	f.t.Helper()
	gitForLayoutTest(f.t, f.dir, "add", updateDesignFile)
	gitForLayoutTest(f.t, f.dir, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-qm", "Project design edit")
}

// list reads through the real strict provenance store.
func (f *draftUpdateFixture) list() madeFromRecord {
	f.t.Helper()
	f.cp.draftMu.Lock()
	defer f.cp.draftMu.Unlock()
	list, found, err := f.cp.cards.loadMadeFrom()
	require.NoError(f.t, err)
	require.True(f.t, found)
	return list
}

// remake removes only the test-owned draft, then uses real materialization.
func (f *draftUpdateFixture) remake() {
	f.t.Helper()
	require.NoError(f.t, os.RemoveAll(filepath.Join(f.dir, draftFolderName)))
	readDraftForTest(f.t, f.cp)
}

// save authors a complete stored revision through the existing provenance
// writer; it deliberately avoids claiming a native refresh/evaluation.
func (f *draftUpdateFixture) save() {
	f.t.Helper()
	files, history := readDraftForTest(f.t, f.cp)
	source, err := designSourceFromModuleSnapshot(files)
	require.NoError(f.t, err)
	snapshot := refreshedSnapshot{designSource: source, goMod: files["go.mod"], architecture: &savedArchitecture{DesignDigest: strings.Repeat("a", 64)}, madeFrom: cloneMadeFromSnapshot(&history)}
	f.cp.draftMu.Lock()
	f.cp.mu.Lock()
	err = f.cp.recordRefreshedSnapshot(trace.SpanFromContext(context.Background()), snapshot)
	f.cp.mu.Unlock()
	f.cp.draftMu.Unlock()
	require.NoError(f.t, err)
	f.saves++
	id := fmt.Sprintf("revision-%d", f.saves)
	saveProposedCardFixture(f.t, f.cp, "alice@example.com", id, "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
	record := loadMetadataRecord(f.t, f.cp, id)
	record.designSource = source
	record.architecture = snapshot.architecture
	record.goMod = snapshot.goMod
	record.madeFrom = snapshot.madeFrom
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, source)
	require.NoError(f.t, err)
	saveMetadataRecord(f.t, f.cp, record)
	// A real candidate side branch makes C5's ancestry observable. Restore
	// the user's branch and uncommitted folder text after authoring it.
	live := f.projectText()
	f.candidateOutside = live
	branch := fmt.Sprintf("fixture-candidate-%d", f.saves)
	gitForLayoutTest(f.t, f.dir, "checkout", "-qb", branch)
	f.project(files[updateDesignFile])
	f.commit()
	f.candidate = branch
	gitForLayoutTest(f.t, f.dir, "checkout", "-q", f.branch)
	f.project(live)
}

// merge uses Git's actual graph and merge result, not a model history flag.
func (f *draftUpdateFixture) merge() {
	f.t.Helper()
	f.commit()
	cmd := exec.Command("git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "-c", "core.hooksPath=/dev/null", "merge", "--no-edit", f.candidate)
	cmd.Dir = f.dir
	cmd.Env = append(cleanGitEnv(), "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	var exit *exec.ExitError
	require.True(f.t, errors.As(err, &exit), string(output))
	require.Equal(f.t, 1, exit.ExitCode(), string(output))
	// The model's main journey says the user took R. Git can require a
	// resolution on these repeated Goa lines even though R already holds
	// the complete earlier outside edit. Resolve to R only in that case;
	// criss-crosses with a later outside edit must merge without this step.
	current := gitForLayoutTest(f.t, f.dir, "show", "HEAD:"+updateDesignFile)
	require.Equal(f.t, canonicalUpdateText(f.t, f.candidateOutside), canonicalUpdateText(f.t, current))
	resolved := gitForLayoutTest(f.t, f.dir, "show", f.candidate+":"+updateDesignFile)
	f.project(resolved)
	f.commit()
}

// stop writes actual owner-prepared pending state and optionally its atomic
// file replacement, then leaves completion to the public recovery path.
func (f *draftUpdateFixture) stop(after bool) {
	f.t.Helper()
	list := f.list()
	projectRoot, err := os.OpenRoot(f.dir)
	require.NoError(f.t, err)
	defer func() { assert.NoError(f.t, projectRoot.Close()) }()
	draftRoot, err := openDraftFolder(projectRoot, draftFolderName)
	require.NoError(f.t, err)
	defer func() { assert.NoError(f.t, draftRoot.Close()) }()
	draft, err := readDraftUpdateFiles(filepath.Join(f.dir, draftFolderName), draftRoot)
	require.NoError(f.t, err)
	project, err := readDraftUpdateFiles(f.dir, projectRoot)
	require.NoError(f.t, err)
	updates, err := f.cp.planDraftUpdates(f.t.Context(), f.dir, f.head(), list, draft, project, &gensurla.DraftUpdateResult{})
	require.NoError(f.t, err)
	require.NotEmpty(f.t, updates)
	u := updates[0]
	f.cp.draftMu.Lock()
	defer f.cp.draftMu.Unlock()
	if u.pending.OutsideVersion != absentDesignVersion {
		_, err = f.cp.cards.saveDesignVersion(u.outside)
		require.NoError(f.t, err)
	}
	list.Write++
	list.Pending = &u.pending
	require.NoError(f.t, f.cp.cards.saveMadeFrom(list))
	if after {
		require.NoError(f.t, writeUpdatedDraftFile(draftRoot, u.pending.File, u.text, u.pending.Result))
	}
}

// updateGoTexts is the original Go-shaped three-way merge counterexample.
func updateGoTexts(t *testing.T) (string, string, string) {
	t.Helper()
	texts := []string{}
	for _, name := range []string{"base", "draft", "project"} {
		b, err := os.ReadFile(filepath.Join("testdata", "draft_update", name+".go.txt"))
		require.NoError(t, err)
		texts = append(texts, canonicalUpdateText(t, string(b)))
	}
	return texts[0], texts[1], texts[2]
}

// canonicalUpdateText delegates version identity to the production owner.
func canonicalUpdateText(t *testing.T, text string) string {
	t.Helper()
	canonical, err := designversion.Canonical(updateDesignFile, text)
	require.NoError(t, err)
	return canonical
}

// updateUserEdits supplies the four distinct outside edits in the 24-pair proof.
func updateUserEdits(t *testing.T) []struct {
	name string
	edit func(string) string
} {
	edit := func(from, to string) func(string) string {
		return func(s string) string {
			require.Equal(t, 1, strings.Count(s, from))
			return strings.Replace(s, from, to, 1)
		}
	}
	return []struct {
		name string
		edit func(string) string
	}{
		{"m1 error", edit(`Error("invalid")`, `Error("rejected")`)},
		{"m21 security", edit("Method(\"m21\", func() {\n\t\tNoSecurity()", "Method(\"m21\", func() {\n\t\tSecurity(JWTAuth)")},
		{"m44 error", edit("Result(Order)\n\t})\n\n\tMethod(\"m92\"", "Result(Order)\n\t\tError(\"not_found\")\n\t})\n\n\tMethod(\"m92\"")},
		{"m92 description", edit("Method(\"m92\", func() {\n\t\tResult(OrderView)", "Method(\"m92\", func() {\n\t\tDescription(\"x\")\n\t\tResult(OrderView)")},
	}
}
