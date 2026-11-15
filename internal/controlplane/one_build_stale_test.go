// This file checks that late work cannot change another build's durable record.
package controlplane

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOneBuildLateConsumersPreserveTheNewOpenRecord simulates only the newer
// owner's durable save. The merge/drop transition that permits it is B2's
// responsibility; these late consumers must be harmless after that save.
func TestOneBuildLateConsumersPreserveTheNewOpenRecord(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	x := buildableChange(t, cp, "x")
	y := buildableChange(t, cp, "y")
	user := asCaller("alice@example.com", CallerUser)
	_, err := cp.StartBuild(user, &gensurla.StartBuildPayload{ChangeID: "x", RequestRevision: x.RequestRevision})
	require.NoError(t, err)
	var old buildFile
	func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		var found bool
		old, found, err = cp.cards.loadOpenBuild()
		require.NoError(t, err)
		require.True(t, found)
		old.ReportNumber = 1
		old.TestWriter.State = testsReported
		require.NoError(t, cp.cards.saveOpenBuild(old))
		newer := old
		newer.ChangeID, newer.RequestRevision = "y", y.RequestRevision
		newer.StartedAt = old.StartedAt.Add(time.Second)
		newer.Number++
		newer.ReportNumber = 0
		newer.TestWriter = testWriterAssignment{State: testsAssigned}
		require.NoError(t, cp.cards.saveOpenBuild(newer))
	}()
	before := readBuildFile(t, cp, "y")
	checker := newReportChecker(cp)
	oldReport := reportIdentityOf(old)
	cases := []struct {
		name string
		run  func(t *testing.T)
	}{
		{name: "builder read", run: func(t *testing.T) {
			_, err := cp.GetBuilderAssignment(asCaller("alice@example.com", CallerBuilder), &gensurla.GetBuilderAssignmentPayload{ChangeID: "x"})
			assertRefused(t, err, "not_found")
		}},
		{name: "test writer read", run: func(t *testing.T) {
			_, err := cp.GetTestWriterAssignment(asCaller("alice@example.com", CallerTestWriter), &gensurla.GetTestWriterAssignmentPayload{ChangeID: "x"})
			assertRefused(t, err, "not_found")
		}},
		{name: "test report", run: func(t *testing.T) {
			_, err := cp.ReportTestsFinished(asCaller("alice@example.com", CallerTestWriter), &gensurla.ReportTestsFinishedPayload{ChangeID: "x", RequestRevision: x.RequestRevision})
			assertRefused(t, err, "not_found")
		}},
		{name: "report still belongs", run: func(t *testing.T) {
			answers, err := checker.answersTo(oldReport)
			require.NoError(t, err)
			assert.False(t, answers)
		}},
		{name: "late verdict", run: func(t *testing.T) {
			cp.reportMu.Lock()
			_, saved, err := checker.saveVerdict(oldReport, testWriterAssignment{State: testsNeedChanges, Findings: []string{"A test was missing."}})
			cp.reportMu.Unlock()
			require.NoError(t, err)
			assert.False(t, saved)
		}},
		{name: "late preparation", run: func(t *testing.T) {
			_, err := cp.saveBuildPreparation(context.Background(), old, errors.New("old generation stopped"))
			assert.ErrorContains(t, err, "replaced")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t)
			assert.Equal(t, before, readBuildFile(t, cp, "y"))
		})
	}
	oldRead, err := cp.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "x"})
	require.NoError(t, err)
	assert.Equal(t, gensurla.BuildKindNotStarted, oldRead.Build.Kind())
	assignments, err := cp.ListBuilderAssignments(asCaller("alice@example.com", CallerBuilder))
	require.NoError(t, err)
	require.Len(t, assignments.Assignments, 1)
	assert.Equal(t, "y", assignments.Assignments[0].ChangeID)
}

// TestOneBuildLeavesLegacyPerChangeFilesUnread proves even a corrupt old
// per-change file cannot become the new project's open build or break reads.
func TestOneBuildLeavesLegacyPerChangeFilesUnread(t *testing.T) {
	cp := newBuildControlPlane(t, Options{})
	card := confirmedChange(t, cp, "old")
	legacy := filepath.Join(cp.cards.dir, fmt.Sprintf("%x.build.json", sha256.Sum256([]byte("old"))))
	require.NoError(t, os.WriteFile(legacy, []byte("{"), 0o600))
	read, err := cp.GetBuild(asCaller("alice@example.com", CallerUser), &gensurla.GetBuildPayload{ChangeID: "old"})
	require.NoError(t, err)
	assert.Equal(t, gensurla.BuildKindNotStarted, read.Build.Kind())
	listed, err := cp.ListChanges(asCaller("alice@example.com", CallerGuide))
	require.NoError(t, err)
	require.Len(t, listed.Changes, 1)
	assert.Equal(t, card.RequestRevision, listed.Changes[0].RequestRevision)
	raw, err := os.ReadFile(legacy)
	require.NoError(t, err)
	assert.Equal(t, "{", string(raw))
}
