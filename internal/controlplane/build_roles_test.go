// This file checks the durable role contract and the one usable-build decision.
package controlplane

import (
	"encoding/json"
	"path/filepath"
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuilderOnlyBuildRequiresStoredSettings reopens each project's one build
// after stored verification settings are removed. Only a writer build remains
// usable, with its folder derived from the project instead of saved in the record.
func TestBuilderOnlyBuildRequiresStoredSettings(t *testing.T) {
	for _, roles := range []string{buildRolesBuilderOnly, buildRolesBuilderAndTestWriter} {
		t.Run(roles, func(t *testing.T) {
			opts := Options{CardsDir: t.TempDir(), WorkspaceDir: t.TempDir(),
				Verification: &VerificationConfig{RunsDir: t.TempDir()},
				Team:         &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{}},
			}
			cp := newBuildControlPlane(t, opts)
			card := buildableChange(t, cp, "chg")
			saved := fixtureOpenBuild(t, cp, card, roles)
			require.NoError(t, cp.cards.saveOpenBuild(saved))
			assert.Equal(t, gensurla.TestsKindNotUsed,
				mustStartedBuild(t, cp, asCaller("alice@example.com", CallerUser), "chg").TestWriter.Tests.Kind())
			assert.NotContains(t, string(readBuildFile(t, cp, "chg")), "working_folder")
			if roles == buildRolesBuilderAndTestWriter {
				makeBuildFixtureBuilderCopy(t, cp)
			}
			require.NoError(t, cp.Close())

			opts.Verification = nil
			opts.Team = &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{}}
			reopened := newBuildControlPlane(t, opts)
			user := asCaller("alice@example.com", CallerUser)
			builder := asCaller("alice@example.com", CallerBuilder)
			writer := asCaller("alice@example.com", CallerTestWriter)
			read, err := reopened.GetBuild(user, &gensurla.GetBuildPayload{ChangeID: "chg"})
			require.NoError(t, err)
			builders, err := reopened.ListBuilderAssignments(builder)
			require.NoError(t, err)
			writers, err := reopened.ListTestWriterAssignments(writer)
			require.NoError(t, err)
			owed, err := reopened.assignmentGenerations(t.Context())
			require.NoError(t, err)
			if roles == buildRolesBuilderOnly {
				assert.Equal(t, gensurla.BuildKindNotStarted, read.Build.Kind())
				assert.Empty(t, builders.Assignments)
				assert.Empty(t, writers.Assignments)
				assert.Empty(t, owed)
				_, err = reopened.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: "chg"})
				assertRefused(t, err, "not_found")
				_, err = reopened.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "chg"})
				assertRefused(t, err, "not_found")
				_, err = reopened.ReportTestsFinished(writer, &gensurla.ReportTestsFinishedPayload{ChangeID: "chg"})
				assertRefused(t, err, "not_found")
				return
			}
			assert.Equal(t, gensurla.BuildKindStarted, read.Build.Kind())
			require.Len(t, builders.Assignments, 1)
			require.Len(t, writers.Assignments, 1)
			require.Len(t, owed, 2)
			assert.Equal(t, "builder", owed[0].Role)
			assert.Equal(t, "test_writer", owed[1].Role)
			assignment, err := reopened.GetTestWriterAssignment(writer, &gensurla.GetTestWriterAssignmentPayload{ChangeID: "chg"})
			require.NoError(t, err)
			workspace, _, _ := reopened.currentWorkspaceTarget()
			assert.Equal(t, filepath.Join(workspace, testwriter.Folder), assignment.WorkingFolder)
			assert.Equal(t, gensurla.TestsKindAssigned, assignment.Progress.Tests.Kind())
		})
	}
}

// TestBuildRoleRecordIsStrict preserves the two explicit modes without a saved
// folder and rejects the removed working_folder field rather than inferring a mode.
func TestBuildRoleRecordIsStrict(t *testing.T) {
	for _, tc := range []struct {
		name, roles string
		valid       bool
	}{
		{name: "builder only", roles: buildRolesBuilderOnly, valid: true},
		{name: "writer", roles: buildRolesBuilderAndTestWriter, valid: true},
		{name: "missing mode"}, {name: "unknown mode", roles: "automatic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			saved := codecOpenBuild()
			saved.Roles = tc.roles
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
	for _, roles := range []string{buildRolesBuilderOnly, buildRolesBuilderAndTestWriter} {
		for _, folder := range []string{"", "relative", t.TempDir()} {
			t.Run(roles+"/removed folder "+folder, func(t *testing.T) {
				saved := codecOpenBuild()
				saved.Roles = roles
				raw, err := json.Marshal(saved)
				require.NoError(t, err)
				var fields map[string]any
				require.NoError(t, json.Unmarshal(raw, &fields))
				fields["test_writer"].(map[string]any)["working_folder"] = folder
				raw, err = json.Marshal(fields)
				require.NoError(t, err)
				_, err = decodeOpenBuild(raw)
				assert.ErrorContains(t, err, "unknown field")
			})
		}
	}
}
