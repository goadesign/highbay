package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	goa "goa.design/goa/v3/pkg"

	genproject "github.com/goadesign/surla/gen/project"
)

// TestRegisteredCredentialFailuresNameTheirRepair breaks a registered role's
// saved credential in each way registerAgent can meet it and checks the page's
// repair: the exact file, a permission change on that file alone, or a restore
// for a missing or damaged one, never the credential's bytes. The typed cause
// stays project_state for every other boundary.
func TestRegisteredCredentialFailuresNameTheirRepair(t *testing.T) {
	const garbage = "not-a-credential-value"
	for _, tc := range []struct {
		name   string
		damage func(t *testing.T, path string)
		want   string
	}{
		{"permissions", func(t *testing.T, path string) { require.NoError(t, os.Chmod(path, 0o644)) }, "because of its permissions or owner; make it a file of this account that only this account can use (chmod 600 %s"},
		{"damaged", func(t *testing.T, path string) { require.NoError(t, os.WriteFile(path, []byte(garbage), 0o600)) }, "%s is damaged; restore it from a backup of this project's Surla state, then try again"},
		{"missing", func(t *testing.T, path string) { require.NoError(t, os.Remove(path)) }, "%s is missing; restore it from a backup of this project's Surla state, then try again"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := testState(t)
			s, err := NewServer(state, func() {}, fixtureTeamDependencies(state))
			require.NoError(t, err)
			handle, err := s.registerAgent("guide-example", agentRecord{Role: "guide", Folder: state.Record.Directory})
			require.NoError(t, err)
			path := state.AgentFile(handle)
			tc.damage(t, path)

			_, err = s.registerAgent("guide-example", agentRecord{Role: "guide", Folder: state.Record.Directory})
			require.Error(t, err)
			page := userTeamError(stateUnavailable(context.Background(), "register_guide", err))
			var failure *goa.ServiceError
			require.ErrorAs(t, page, &failure)
			assert.Equal(t, "team_unavailable", failure.Name)
			assert.Contains(t, failure.Message, strings.ReplaceAll(tc.want, "%s", path))
			assert.NotContains(t, failure.Message, garbage)
			assert.NotContains(t, failure.Message, filepath.Dir(state.Dir)+" ", "no blanket change on the operator folder")
			var typed *genproject.TeamUnavailable
			require.True(t, errors.As(page, &typed))
			assert.Equal(t, "project_state", typed.Cause)
		})
	}
}

// TestStateRepairForFailuresWithoutAFile covers a project_state failure that
// names no state file, such as an unreadable build assignment: it is retried
// as it is, without a permission change.
func TestStateRepairForFailuresWithoutAFile(t *testing.T) {
	assert.Equal(t, "this project's saved build state could not be read; try again", stateRepair(errors.New("assignment reader failed")))
}
