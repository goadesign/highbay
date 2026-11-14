// This file verifies the numbered build's strict and atomic persistence contract.
package controlplane

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// codecOpenBuild supplies a fresh complete record for each independent case.
func codecOpenBuild() buildFile {
	return buildFile{
		ChangeID: "refund", RequestRevision: "revision", StartedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Roles: buildRolesBuilderAndTestWriter, Number: 2, Base: strings.Repeat("a", 40),
		GoMod:      "module example.com/shop\n\ngo 1.27.3\n",
		TestWriter: testWriterAssignment{State: testsAssigned},
		MadeFrom:   madeFromSnapshot{Start: madeFromStart{Write: 1}},
	}
}

// TestOpenBuildRecordStrictDecode preserves every state/report/time invariant
// while excluding paths and filename rules of the superseded per-change record.
func TestOpenBuildRecordStrictDecode(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*buildFile)
		valid bool
	}{
		{name: "assigned", valid: true},
		{name: "builder only has no saved folder", valid: true, edit: func(b *buildFile) { b.Roles = buildRolesBuilderOnly }},
		{name: "reported", valid: true, edit: func(b *buildFile) { b.TestWriter.State, b.ReportNumber = testsReported, 1 }},
		{name: "needs changes", valid: true, edit: func(b *buildFile) {
			b.TestWriter = testWriterAssignment{State: testsNeedChanges, Findings: []string{"Fix hook."}, CheckedAt: b.StartedAt}
		}},
		{name: "accepted", valid: true, edit: func(b *buildFile) {
			b.TestWriter = testWriterAssignment{State: testsAccepted, Commit: strings.Repeat("b", 64), CheckedAt: b.StartedAt}
		}},
		{name: "could not run", valid: true, edit: func(b *buildFile) {
			b.TestWriter = testWriterAssignment{State: testsCouldNotRun, Reason: "Disk full.", Step: "Free space.", CheckedAt: b.StartedAt}
		}},
		{name: "no change", edit: func(b *buildFile) { b.ChangeID = "" }},
		{name: "no revision", edit: func(b *buildFile) { b.RequestRevision = "" }},
		{name: "no start", edit: func(b *buildFile) { b.StartedAt = time.Time{} }},
		{name: "no number", edit: func(b *buildFile) { b.Number = 0 }},
		{name: "negative number", edit: func(b *buildFile) { b.Number = -1 }},
		{name: "invalid base", edit: func(b *buildFile) { b.Base = "HEAD" }},
		{name: "no module", edit: func(b *buildFile) { b.GoMod = "" }},
		{name: "invalid module", edit: func(b *buildFile) { b.GoMod = "module (" }},
		{name: "no provenance", edit: func(b *buildFile) { b.MadeFrom = madeFromSnapshot{} }},
		{name: "unknown role", edit: func(b *buildFile) { b.Roles = "automatic" }},
		{name: "negative report", edit: func(b *buildFile) { b.ReportNumber = -1 }},
		{name: "assigned report", edit: func(b *buildFile) { b.ReportNumber = 1 }},
		{name: "reported without report", edit: func(b *buildFile) { b.TestWriter.State = testsReported }},
		{name: "assigned findings", edit: func(b *buildFile) { b.TestWriter.Findings = []string{"unexpected"} }},
		{name: "assigned commit", edit: func(b *buildFile) { b.TestWriter.Commit = strings.Repeat("b", 40) }},
		{name: "assigned reason", edit: func(b *buildFile) { b.TestWriter.Reason = "unexpected" }},
		{name: "assigned verdict time", edit: func(b *buildFile) { b.TestWriter.CheckedAt = b.StartedAt }},
		{name: "unknown state", edit: func(b *buildFile) { b.TestWriter.State = "waiting" }},
		{name: "empty findings", edit: func(b *buildFile) { b.TestWriter.State, b.TestWriter.CheckedAt = testsNeedChanges, b.StartedAt }},
		{name: "invalid accepted commit", edit: func(b *buildFile) {
			b.TestWriter = testWriterAssignment{State: testsAccepted, Commit: "HEAD", CheckedAt: b.StartedAt}
		}},
		{name: "missing verdict time", edit: func(b *buildFile) {
			b.TestWriter = testWriterAssignment{State: testsAccepted, Commit: strings.Repeat("b", 40)}
		}},
		{name: "reason character bound", valid: true, edit: func(b *buildFile) {
			b.TestWriter = testWriterAssignment{State: testsCouldNotRun,
				Reason: strings.Repeat("é", maxCheckReason), Step: strings.Repeat("s", maxCheckStep), CheckedAt: b.StartedAt}
		}},
		{name: "reason too long", edit: func(b *buildFile) {
			b.TestWriter = testWriterAssignment{State: testsCouldNotRun,
				Reason: strings.Repeat("é", maxCheckReason+1), Step: "Repair.", CheckedAt: b.StartedAt}
		}},
		{name: "step too long", edit: func(b *buildFile) {
			b.TestWriter = testWriterAssignment{State: testsCouldNotRun,
				Reason: "Disk full.", Step: strings.Repeat("s", maxCheckStep+1), CheckedAt: b.StartedAt}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			saved := codecOpenBuild()
			if tc.edit != nil {
				tc.edit(&saved)
			}
			raw, err := json.Marshal(saved)
			require.NoError(t, err)
			_, err = decodeOpenBuild(raw)
			if tc.valid {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
	valid, err := json.Marshal(codecOpenBuild())
	require.NoError(t, err)
	for _, raw := range [][]byte{
		[]byte(strings.TrimSuffix(string(valid), "}") + `,"unknown":true}`),
		[]byte(strings.Replace(string(valid), `"test_writer":{`, `"test_writer":{"working_folder":"/unused",`, 1)),
		append(append([]byte{}, valid...), []byte(" {}")...),
	} {
		_, err := decodeOpenBuild(raw)
		assert.Error(t, err)
	}
}

// TestOpenBuildRecordSurvivesReload preserves the previous publication on a
// failed staging write and independently verifies the state after reopening.
func TestOpenBuildRecordSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	store, err := openCardStore(dir)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, store.close()) })
	_, found, err := store.loadOpenBuild()
	require.NoError(t, err)
	assert.False(t, found)
	saved := codecOpenBuild()
	saved.StartedAt = saved.StartedAt.In(time.FixedZone("offset", 2*60*60))
	require.NoError(t, store.saveOpenBuild(saved))
	guard := filepath.Join(t.TempDir(), "guard")
	require.NoError(t, os.WriteFile(guard, []byte("unchanged"), 0o600))
	require.NoError(t, os.Symlink(guard, filepath.Join(store.dir, openBuildStagingFile)))
	replacement := saved
	replacement.Number++
	assert.Error(t, store.saveOpenBuild(replacement))
	guardText, err := os.ReadFile(guard)
	require.NoError(t, err)
	assert.Equal(t, "unchanged", string(guardText))
	require.NoError(t, store.close())
	_, _, err = store.loadOpenBuild()
	assert.ErrorIs(t, err, errCardsClosed)
	reopened, err := openCardStore(dir)
	require.NoError(t, err)
	defer func() { assert.NoError(t, reopened.close()) }()
	actual, found, err := reopened.loadOpenBuild()
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, saved.Number, actual.Number)
	assert.True(t, saved.StartedAt.Equal(actual.StartedAt))
	assert.Equal(t, time.UTC, actual.StartedAt.Location())
	assert.Equal(t, saved.MadeFrom, actual.MadeFrom)
}
