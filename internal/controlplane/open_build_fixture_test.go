// This file supplies explicit saved evaluation snapshots for build-unit tests.
// It replaces no runtime producer: native workflow tests use refresh_story.
package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/designversion"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/stretchr/testify/require"
)

// buildableChange confirms a unit fixture whose saved evaluation snapshot is
// supplied explicitly before confirmation. confirmedChange remains the helper
// for legacy cards without metadata and tests of producer behavior.
func buildableChange(t *testing.T, cp *ControlPlane, changeID string) *gensurla.ChangeCardResult {
	t.Helper()
	proposed := saveProposedCardFixture(t, cp, "alice@example.com", changeID,
		"Refunds up to $50 are instant.", refundAnalysis("Refunds up to $50 are instant."))
	proposed = attachBuildSnapshot(t, cp, proposed.ChangeID)
	confirmed, err := cp.ConfirmIntent(asCaller("alice@example.com", CallerUser), &gensurla.ConfirmIntentPayload{
		ChangeID: changeID, RequestRevision: proposed.RequestRevision, Answers: map[string]string{}, Examples: proposed.Examples,
	})
	require.NoError(t, err)
	return confirmed
}

// saveBuildStoryCard supplies the same explicit snapshot for an already
// constructed Story unit fixture; it does not claim to exercise evaluation.
func saveBuildStoryCard(
	t *testing.T, cp *ControlPlane, changeID, status string, stories []designeval.StorySummary,
) *gensurla.ChangeCardResult {
	t.Helper()
	saveStoryCard(t, cp, changeID, status, stories)
	return attachBuildSnapshot(t, cp, changeID)
}

// attachBuildSnapshot saves a canonical authored design, module and provenance
// with the card. Story-only fixture designs have an empty evaluated topology;
// their digest names the actual snapshot rather than a sentinel value.
func attachBuildSnapshot(t *testing.T, cp *ControlPlane, changeID string) *gensurla.ChangeCardResult {
	t.Helper()
	workspace, _, _ := cp.currentWorkspaceTarget()
	require.NotEmpty(t, workspace, "a build fixture must bind its real project folder")
	module, err := os.ReadFile(filepath.Join(workspace, "go.mod"))
	require.NoError(t, err)
	base := productionBuildGit(t, workspace, "rev-parse", "HEAD")
	record := loadMetadataRecord(t, cp, changeID)
	files := map[string]string{}
	if record.designSource != "" {
		require.NoError(t, json.Unmarshal([]byte(record.designSource), &files))
	} else {
		files["design/build_fixture.go"] = buildStorySource(t, record.stories)
	}
	entries := make(map[string][]madeFromVersion, len(files))
	snapshot := map[string]string{"go.mod": string(module)}
	if sum, err := os.ReadFile(filepath.Join(workspace, "go.sum")); err == nil {
		snapshot["go.sum"] = string(sum)
	} else {
		require.ErrorIs(t, err, os.ErrNotExist)
	}
	for path, source := range files {
		canonical, err := designversion.Canonical(path, source)
		require.NoError(t, err)
		files[path], snapshot[path] = canonical, canonical
		entries[path] = []madeFromVersion{{Version: designversion.Digest(canonical), Entered: 1}}
	}
	record.designSource, err = encodeDesignSource(files)
	require.NoError(t, err)
	record.goMod = string(module)
	record.madeFrom = &madeFromSnapshot{
		Start: madeFromStart{Write: 1, ProjectCommit: base}, Entries: entries,
	}
	if record.architecture == nil {
		record.architecture = &savedArchitecture{}
	}
	record.architecture.DesignDigest = designeval.SnapshotDigest(snapshot)
	record.card.RequestRevision, err = computeCanonicalRevision(record.card, record.owner, record.designSource)
	require.NoError(t, err)
	saveMetadataRecord(t, cp, record)
	return record.card
}

// fixtureOpenBuild constructs the record of an explicitly snapshotted card at
// the fixture repository's actual base. Tests then vary the field they own.
func fixtureOpenBuild(
	t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult, roles string,
) buildFile {
	t.Helper()
	record := loadMetadataRecord(t, cp, card.ChangeID)
	require.NotNil(t, record.madeFrom)
	workspace, _, _ := cp.currentWorkspaceTarget()
	return buildFile{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, StartedAt: time.Now().UTC(),
		Number: 1, Base: productionBuildGit(t, workspace, "rev-parse", "HEAD"),
		GoMod: record.goMod, MadeFrom: *record.madeFrom, Roles: roles,
		Stories: cloneStorySummaries(record.stories), DesignSource: record.designSource,
		Answers: confirmedAnswers(card), Examples: card.Examples, PreservedBehaviors: card.PreservedBehaviors,
		TestWriter: testWriterAssignment{State: testsAssigned},
	}
}

// saveStoredCardBuildFixture supplies authored card and build records for
// persistence/approval consumer tests, without claiming a runtime refresh.
func saveStoredCardBuildFixture(
	t *testing.T, cp *ControlPlane, approval *gensurla.ApprovalItem,
) *gensurla.ChangeCardResult {
	t.Helper()
	saveCard(t, cp, approval)
	card := attachBuildSnapshot(t, cp, "chg")
	saveStoredBuildFixture(t, cp, card)
	return card
}

// saveStoredBuildFixture supplies an explicit builder-only numbered record for
// stored-evaluator unit tests. It exercises consumers, not build production.
func saveStoredBuildFixture(t *testing.T, cp *ControlPlane, card *gensurla.ChangeCardResult) {
	t.Helper()
	build := fixtureOpenBuild(t, cp, card, buildRolesBuilderOnly)
	cp.mu.Lock()
	err := cp.cards.saveOpenBuild(build)
	cp.mu.Unlock()
	require.NoError(t, err)
}

// startBuildAgain explicitly names the unfinished start the fixture just read,
// as a user's second Build confirmation does. It never bypasses a refusal.
func startBuildAgain(
	t *testing.T, cp *ControlPlane, caller context.Context, changeID, revision string,
) (*gensurla.StartBuildResult, error) {
	t.Helper()
	cp.mu.RLock()
	open, found, err := cp.cards.loadOpenBuild()
	cp.mu.RUnlock()
	require.NoError(t, err)
	payload := &gensurla.StartBuildPayload{ChangeID: changeID, RequestRevision: revision}
	if found && open.ChangeID == changeID {
		payload.Replaces = &gensurla.ReplacedBuild{ChangeID: changeID, StartedAt: open.StartedAt.Format(time.RFC3339Nano)}
	}
	return cp.StartBuild(caller, payload)
}

// fixtureTestWriterFolder derives the same project-relative copy path as the
// assignment owner, including projects below the repository root.
func fixtureTestWriterFolder(t *testing.T, cp *ControlPlane) string {
	t.Helper()
	workspace, _, _ := cp.currentWorkspaceTarget()
	_, prefix, err := resolveStarterRepo(t.Context(), workspace)
	require.NoError(t, err)
	return filepath.Join(workspace, testwriter.Folder, filepath.FromSlash(prefix))
}

// ensureBuildFixtureRepository creates only a missing test repository and its
// first commit. Existing repositories and their histories remain untouched.
func ensureBuildFixtureRepository(t *testing.T, workspace string) {
	t.Helper()
	probe := exec.CommandContext(t.Context(), "git", "-C", workspace, "rev-parse", "--show-toplevel")
	if err := probe.Run(); err == nil {
		return
	}
	initGitWorktreeForBoundaryTest(t, workspace)
	for _, setting := range []string{"user.name=Build Tester", "user.email=build@example.com", "commit.gpgsign=false", "core.hooksPath=/dev/null", "core.fsmonitor=false"} {
		key, value, _ := strings.Cut(setting, "=")
		runGitForTest(t, workspace, "config", key, value)
	}
	runGitForTest(t, workspace, "add", "--all")
	runGitForTest(t, workspace, "commit", "--quiet", "--allow-empty", "-m", "Create the build fixture")
}

// buildStorySource authors the linear Story declarations supplied by the unit
// fixture. The snapshot matches its saved actors, flows, steps and questions.
func buildStorySource(t *testing.T, stories []designeval.StorySummary) string {
	t.Helper()
	var source strings.Builder
	source.WriteString("package design\n")
	if len(stories) == 0 {
		return source.String()
	}
	source.WriteString("import . \"github.com/goadesign/surla/story/dsl\"\n")
	for _, story := range stories {
		fmt.Fprintf(&source, "var _ = Story(%q, func() {\n", story.Name)
		fmt.Fprintf(&source, "Description(%q)\n", story.Description)
		for _, actor := range story.Actors {
			fmt.Fprintf(&source, "Actor(%q, %q, func() { Description(%q) })\n", actor.Key, actor.Title, actor.Description)
		}
		for _, flow := range story.Flows {
			fmt.Fprintf(&source, "Flow(%q, %q, func() {\nDescription(%q)\n", flow.Key, flow.Title, flow.Description)
			writeFixtureQuestions(&source, flow.Questions)
			for _, step := range flow.Steps {
				name := map[string]string{"start": "StartsWith", "step": "Step", "outcome": "Outcome"}[step.Kind]
				require.NotEmpty(t, name, "fixture step kind %q", step.Kind)
				fmt.Fprintf(&source, "%s(%q, %q, %q, func() {\nDescription(%q)\n", name, step.Key, step.ActorKey, step.Summary, step.Description)
				writeFixtureQuestions(&source, step.Questions)
				source.WriteString("})\n")
			}
			source.WriteString("})\n")
		}
		source.WriteString("})\n")
	}
	canonical, err := designversion.Canonical("design/build_fixture.go", source.String())
	require.NoError(t, err)
	return canonical
}

// writeFixtureQuestions writes a fixture's unresolved choices without deciding them.
func writeFixtureQuestions(source *strings.Builder, questions []designeval.StoryQuestion) {
	for _, question := range questions {
		fmt.Fprintf(source, "Question(%q, %q, func() {\nDescription(%q)\n", question.Key, question.Prompt, question.Description)
		for _, option := range question.Options {
			fmt.Fprintf(source, "Option(%q)\n", option)
		}
		source.WriteString("})\n")
	}
}
