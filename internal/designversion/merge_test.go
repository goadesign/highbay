// This file tests merging a design change made outside Surla into the draft's
// text of one design file.
package designversion

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMergeFileKeepsBothSidesMarksConflictsAndRepeatsWithoutChange merges the
// cases of git merge-file the draft meets: changes to separate lines, to the
// same lines, the same change on both sides, a file both sides added, and a
// clean result merged again. Each merge runs where a user's global
// configuration and the repository around the temporary folder both ask for
// zdiff3 markers, and checks git's plain markers with Surla's labels, and
// that the temporary folder is gone.
func TestMergeFileKeepsBothSidesMarksConflictsAndRepeatsWithoutChange(t *testing.T) {
	const before = "package design\n\nvar A = 1\n\nvar B = 2\n\nvar C = 3\n\nvar D = 4\n\nvar E = 5\n"
	draftA := strings.Replace(before, "A = 1", "A = 10", 1)
	projectE := strings.Replace(before, "E = 5", "E = 50", 1)
	bothAE := strings.Replace(draftA, "E = 5", "E = 50", 1)
	cases := []struct {
		name     string
		draft    string
		base     string
		project  string
		want     string
		conflict bool
	}{
		{name: "separate lines", draft: draftA, base: before, project: projectE, want: bothAE},
		{
			name:  "same lines",
			draft: draftA, base: before, project: strings.Replace(before, "A = 1", "A = 11", 1),
			want: "package design\n\n<<<<<<< draft\nvar A = 10\n=======\nvar A = 11\n>>>>>>> project folder\n" +
				"\nvar B = 2\n\nvar C = 3\n\nvar D = 4\n\nvar E = 5\n",
			conflict: true,
		},
		{name: "same change on both sides", draft: draftA, base: before, project: bothAE, want: bothAE},
		{
			name:  "added on both sides",
			draft: "package design\n\nvar A = 1\n", base: "", project: "package design\n\nvar A = 2\n",
			want:     "package design\n\n<<<<<<< draft\nvar A = 1\n=======\nvar A = 2\n>>>>>>> project folder\n",
			conflict: true,
		},
		{name: "clean result merged again", draft: bothAE, base: before, project: projectE, want: bothAE},
	}
	temp := zdiff3Everywhere(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merged, conflict, err := MergeFile(context.Background(), tc.draft, tc.base, tc.project)

			require.NoError(t, err)
			assert.Equal(t, tc.want, merged)
			assert.Equal(t, tc.conflict, conflict)
			if tc.conflict {
				_, err := Canonical("design/types.go", merged)
				assert.Error(t, err, "Go cannot parse the markers")
			}
			left, err := os.ReadDir(temp)
			require.NoError(t, err)
			assert.Empty(t, left, "the temporary folder is removed")
		})
	}
}

// zdiff3Everywhere makes the zdiff3 conflict style the user's global setting
// and the setting of a repository around the temporary folder, where
// MergeFile's git runs, and returns that folder.
func zdiff3Everywhere(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "merge.conflictStyle", "zdiff3"}} {
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = mergeEnv(repo)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	temp := filepath.Join(repo, "tmp")
	require.NoError(t, os.Mkdir(temp, 0o700))
	t.Setenv("TMPDIR", temp)
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".gitconfig"), []byte("[merge]\n\tconflictStyle = zdiff3\n"), 0o600))
	t.Setenv("HOME", home)
	return temp
}
