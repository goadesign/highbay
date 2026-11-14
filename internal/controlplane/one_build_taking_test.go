// This file checks which durable verification runs finish one build start.
package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOneBuildSelectsTheNewestMatchingPassedRun exercises the real run reader,
// including passes hidden by a newer failure and the exact start boundary.
func TestOneBuildSelectsTheNewestMatchingPassedRun(t *testing.T) {
	start := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	// Named fixture records keep the scenarios readable without depending on
	// the run writer's report representation.
	type savedRun struct {
		id, owner, revision string
		after               time.Duration
		passed              bool
	}
	scenarios := []struct {
		name string
		runs []savedRun
		want string
	}{
		{name: "no runs"},
		{name: "pass before restart does not count", runs: []savedRun{{id: "a", after: -time.Nanosecond, passed: true}}},
		{name: "pass at exact start counts", runs: []savedRun{{id: "a", passed: true}}, want: "a"},
		{name: "later failure preserves pass", runs: []savedRun{{id: "a", passed: true}, {id: "b", after: time.Second}}, want: "a"},
		{name: "newest pass wins", runs: []savedRun{{id: "a", passed: true}, {id: "b", after: time.Second, passed: true}}, want: "b"},
		{name: "another revision does not count", runs: []savedRun{{id: "a", revision: "other", passed: true}}},
		{name: "another owner does not count", runs: []savedRun{{id: "a", owner: "bob@example.com", passed: true}}},
		{name: "equal time uses stable run order", runs: []savedRun{{id: "b", passed: true}, {id: "a", passed: true}}, want: "a"},
	}
	for _, tc := range scenarios {
		t.Run(tc.name, func(t *testing.T) {
			runs := t.TempDir()
			cp := newControlPlane(t, Options{RunsDir: runs})
			card := mustPropose(t, cp, asCaller("alice@example.com", CallerGuide), "x", "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
			for _, fixture := range tc.runs {
				snapshot := *card
				owner := "alice@example.com"
				if fixture.owner != "" {
					owner = fixture.owner
				}
				if fixture.revision != "" {
					snapshot.RequestRevision = fixture.revision
				}
				writeListedRun(t, runs, owner, &snapshot, strings.Repeat(fixture.id, 32), start.Add(fixture.after), fixture.passed)
			}
			saved := buildFile{ChangeID: "x", RequestRevision: card.RequestRevision, StartedAt: start}
			got, err := cp.newestPassedBuild("alice@example.com", saved)
			require.NoError(t, err)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, strings.Repeat(tc.want, 32), got.Request.RunID)
		})
	}
}

// TestOneBuildDoesNotTreatUnreadableEvidenceAsUnfinished ensures a damaged
// matching run fails the read instead of silently allowing a replacement.
func TestOneBuildDoesNotTreatUnreadableEvidenceAsUnfinished(t *testing.T) {
	runs := t.TempDir()
	cp := newControlPlane(t, Options{RunsDir: runs})
	card := mustPropose(t, cp, asCaller("alice@example.com", CallerGuide), "x", "Keep refunds bounded", refundAnalysis("Keep refunds bounded"))
	start := time.Now().UTC()
	id := strings.Repeat("a", 32)
	writeListedRun(t, runs, "alice@example.com", card, id, start, true)
	require.NoError(t, os.WriteFile(filepath.Join(runs, id, "result.json"), []byte("{"), 0o600))
	got, err := cp.newestPassedBuild("alice@example.com", buildFile{ChangeID: "x", RequestRevision: card.RequestRevision, StartedAt: start})
	assert.Error(t, err)
	assert.Nil(t, got)
}
