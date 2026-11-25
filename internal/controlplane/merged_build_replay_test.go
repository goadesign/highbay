// This file proves replay uses a new build after real repository merge recognition.
package controlplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/testwriter"
	"github.com/goadesign/highbay/internal/verify"
	"github.com/goadesign/highbay/internal/verifyrun"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMergedBuildReplayStartsFresh uses production build/copy/assignment owners
// and real Git history. The passed report is seeded, not executed verification;
// GetBuild must recognize the actual merge before StartBuild accepts replay.
func TestMergedBuildReplayStartsFresh(t *testing.T) {
	for _, configured := range []bool{false, true} {
		name := "written tests"
		if configured {
			name = "configured tests"
		}
		t.Run(name, func(t *testing.T) {
			cfg := verificationConfig(t)
			opts := Options{RunsDir: cfg.RunsDir}
			if configured {
				opts.BuildCacheDir = t.TempDir()
				opts.Verification = cfg
			}
			repo, project, cp, registered := reviewProject(t, opts)
			card := buildableChange(t, cp, "replay")
			user := asCaller("alice@example.com", CallerUser)
			first, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
			})
			require.NoError(t, err)
			builder, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder),
				&genhighbay.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
			require.NoError(t, err)
			assert.True(t, builder.Read)
			if !configured {
				writer, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter),
					&genhighbay.GetTestWriterAssignmentPayload{ChangeID: card.ChangeID})
				require.NoError(t, err)
				assert.True(t, writer.Progress.Read)
			}
			cp.mu.RLock()
			original, found, err := cp.cards.loadOpenBuild()
			cp.mu.RUnlock()
			require.NoError(t, err)
			require.True(t, found)
			writeWorkspaceFiles(t, builder.WorkingFolder, map[string]string{"candidate.txt": "merged implementation\n"})
			productionBuildGit(t, builder.WorkingFolder, "add", "--", "candidate.txt")
			productionBuildGit(t, builder.WorkingFolder, "commit", "--quiet", "-m", "Implement replay fixture")
			candidate := productionBuildGit(t, builder.WorkingFolder, "rev-parse", "HEAD")
			tests := original.Base
			writeReplayPassedRun(t, cfg.RunsDir, card, original, candidate, tests, configured)
			before, err := cp.GetBuild(user, &genhighbay.GetBuildPayload{ChangeID: card.ChangeID})
			require.NoError(t, err)
			beforeBuild, ok := before.Build.AsStarted()
			require.True(t, ok)
			assert.Equal(t, genhighbay.BuildTakingKindPassed, beforeBuild.Taking.Kind())
			productionBuildGit(t, repo, "merge", "--no-edit", candidate)
			writeWorkspaceFiles(t, project, map[string]string{"later.txt": "current branch after merge\n"})
			productionBuildGit(t, repo, "add", "--", "later.txt")
			productionBuildGit(t, repo, "commit", "--quiet", "-m", "Continue after merging")
			current := productionBuildGit(t, repo, "rev-parse", "HEAD")
			merged, err := cp.GetBuild(user, &genhighbay.GetBuildPayload{ChangeID: card.ChangeID})
			require.NoError(t, err)
			mergedBuild, ok := merged.Build.AsStarted()
			require.True(t, ok)
			require.Equal(t, genhighbay.BuildTakingKindMerged, mergedBuild.Taking.Kind())
			assert.True(t, mergedBuild.InForce)
			assert.False(t, merged.DraftedBeforeDrop)
			next, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
			})
			require.NoError(t, err, "merged replay requires no replaces payload")
			assert.NotEqual(t, first.Build.StartedAt, next.Build.StartedAt)
			assert.Equal(t, card.RequestRevision, next.Build.RequestRevision)
			assert.False(t, next.Build.Builder.Read)
			assert.False(t, next.Build.TestWriter.Read)
			assert.Zero(t, next.Build.TestWriter.Report)
			wantTests := genhighbay.TestsKindAssigned
			if configured {
				wantTests = genhighbay.TestsKindNotUsed
			}
			assert.Equal(t, wantTests, next.Build.TestWriter.Tests.Kind())
			public, err := cp.GetBuild(user, &genhighbay.GetBuildPayload{ChangeID: card.ChangeID})
			require.NoError(t, err)
			publicBuild, ok := public.Build.AsStarted()
			require.True(t, ok)
			assert.Equal(t, genhighbay.BuildTakingKindBuilding, publicBuild.Taking.Kind(), "old pass does not carry to the new build")
			cp.mu.RLock()
			saved, found, err := cp.cards.loadOpenBuild()
			cp.mu.RUnlock()
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, original.Number+1, saved.Number)
			assert.Equal(t, current, saved.Base)
			assert.Equal(t, original.RequestRevision, saved.RequestRevision)
			builder, err = cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder),
				&genhighbay.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
			require.NoError(t, err)
			assert.Equal(t, testwriter.BuilderRole.Branch(saved.Number), productionBuildGit(t, builder.WorkingFolder, "branch", "--show-current"))
			assert.Equal(t, current, productionBuildGit(t, builder.WorkingFolder, "rev-parse", "HEAD"))
			if !configured {
				writer, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter),
					&genhighbay.GetTestWriterAssignmentPayload{ChangeID: card.ChangeID})
				require.NoError(t, err)
				assert.Equal(t, testwriter.TestsRole.Branch(saved.Number), productionBuildGit(t, writer.WorkingFolder, "branch", "--show-current"))
				assert.Equal(t, current, productionBuildGit(t, writer.WorkingFolder, "rev-parse", "HEAD"))
			}
			assert.Len(t, *registered, 2)
		})
	}
}

// writeReplayPassedRun seeds a well-formed historical pass for the real commits
// and selected test source. It does not claim the candidate or tests were run.
func writeReplayPassedRun(
	t *testing.T, runs string, card *genhighbay.ChangeCardResult, build buildFile,
	candidate, tests string, configured bool,
) {
	t.Helper()
	spec := reportSpec{SuiteID: "orders_acceptance", SuiteCommit: tests,
		Environment: "sim", Artifacts: map[string]string{"orders": strings.Repeat("a", 64)}}
	report := passingReport(t, card.RequestRevision, candidate, spec)
	reports := filepath.Join(runs, "reports")
	require.NoError(t, os.MkdirAll(reports, 0o700))
	raw, err := json.Marshal(report)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(reports, report.Digest+".json"), raw, 0o600))
	id := strings.Repeat("a", 32)
	dir := filepath.Join(runs, id)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	source := verify.TestsWritten
	if configured {
		source = verify.TestsEvaluator
	}
	raw, err = json.Marshal(verifyrun.Request{
		RunID: id, Owner: "alice@example.com", ChangeID: card.ChangeID,
		AcceptedAt: build.StartedAt.Add(time.Nanosecond),
		Plan: verify.Plan{CandidateCommit: candidate, RequestRevision: card.RequestRevision,
			SuiteCommit: tests, ReportDir: reports, WorkDir: filepath.Join(dir, "work"), Tests: source},
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "request.json"), raw, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lock"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "result.json"), []byte(`{"kind":"report","report_digest":"`+report.Digest+`"}`), 0o600))
}
