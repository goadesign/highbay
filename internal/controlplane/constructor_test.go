// These tests exercise the control plane's required project inputs at startup.
package controlplane

import (
	"os"
	"path/filepath"
	"testing"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/designeval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewRefusesMissingOrInvalidProjectInputs checks that a rejected
// configuration creates no card store and closes its supplied evaluator.
func TestNewRefusesMissingOrInvalidProjectInputs(t *testing.T) {
	root := realTempDir(t)
	workspace := realTempDir(t)
	file := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(workspace, link))
	missing := filepath.Join(root, "missing")

	for _, tc := range []struct {
		name   string
		change func(*Options)
		want   string
	}{
		{"missing team", func(o *Options) { o.Team = nil }, "Team is required"},
		{"missing owner", func(o *Options) { o.Team.Owner = "" }, "Team requires an owner and service"},
		{"missing service", func(o *Options) { o.Team.Service = nil }, "Team requires an owner and service"},
		{"missing workspace input", func(o *Options) { o.WorkspaceDir = "" }, "WorkspaceDir is required"},
		{"missing workspace directory", func(o *Options) { o.WorkspaceDir = missing }, "stat workspace"},
		{"file instead of workspace", func(o *Options) { o.WorkspaceDir = file }, "is not a directory"},
		{"symbolic link instead of workspace", func(o *Options) { o.WorkspaceDir = link }, "must not be a symbolic link"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			team := &teamServiceFixture{}
			eval := designeval.NewEvaluatorInDir(t.TempDir())
			t.Cleanup(func() { assert.NoError(t, eval.Close()) })
			opts := Options{
				CardsDir:     filepath.Join(t.TempDir(), "cards"),
				WorkspaceDir: workspace,
				Team:         &TeamBinding{Owner: "private-owner", Service: team},
				Evaluator:    eval,
			}
			tc.change(&opts)
			cp, err := New(opts)
			if cp != nil {
				t.Cleanup(func() { assert.NoError(t, cp.Close()) })
			}
			assert.Nil(t, cp)
			require.ErrorContains(t, err, tc.want)
			assert.NotContains(t, err.Error(), "private-owner")
			assert.NoDirExists(t, opts.CardsDir)
			team.mu.Lock()
			assert.Nil(t, team.bound, "a refused construction must not bind the team")
			team.mu.Unlock()
			_, err = eval.Evaluate(t.Context(), nil)
			assert.ErrorContains(t, err, "design evaluator is closed")
		})
	}
}

// TestNewKeepsItsTeamBinding proves edits to the caller's options do not
// change the project owner or the service of a running control plane.
func TestNewKeepsItsTeamBinding(t *testing.T) {
	snapshot := genhighbay.TeamSnapshot{HerdrMode: "attached", SavedChoices: map[string]*genhighbay.RoleAgentChoice{}, DismissedNotices: []string{}, Team: "original-team", Roles: []*genhighbay.TeamRoleState{}}
	binding := &TeamBinding{Owner: "alice@example.com", Service: &teamServiceFixture{snapshot: snapshot}}
	cp, err := New(Options{CardsDir: t.TempDir(), WorkspaceDir: realTempDir(t), Team: binding})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, cp.Close()) })

	binding.Owner = "bob@example.com"
	binding.Service = &teamServiceFixture{}
	got, err := cp.GetTeam(asCaller("alice@example.com", CallerUser))
	require.NoError(t, err)
	assert.Equal(t, &snapshot, got)
	_, err = cp.GetTeam(asCaller("bob@example.com", CallerUser))
	assertServiceError(t, err, "forbidden")
}
