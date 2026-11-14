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

// TestMergeFileIsOneThreeWayMerge merges the cases of git merge-file a draft
// meets: changes to separate lines, to the same lines, one line changed alike
// on both sides, a file both sides added, and a text git refuses. Its last
// rows are the contract's counterexamples: a clean result merged again over
// the same base comes back unchanged where every line differs from the
// others, but where blank lines repeat it conflicts, holding the project's
// change both inside the markers and after them, or changes without a
// conflict, the project's change applied twice. Each merge runs where the
// user's global configuration and a repository around the temporary folder,
// which an inherited GIT_DIR also names, ask for zdiff3 markers; it checks
// git's plain markers with Surla's labels, and that the temporary folder is
// gone.
func TestMergeFileIsOneThreeWayMerge(t *testing.T) {
	const before = "package design\n\nvar A = 1\n\nvar B = 2\n\nvar C = 3\n\nvar D = 4\n\nvar E = 5\n"
	draftA := strings.Replace(before, "A = 1", "A = 10", 1)
	projectE := strings.Replace(before, "E = 5", "E = 50", 1)
	bothAE := strings.Replace(draftA, "E = 5", "E = 50", 1)
	cases := []struct {
		name    string
		draft   string
		base    string
		project string
		// once, when set, is the clean result of merging draft first: the
		// row then merges that result again over base and project.
		once     string
		want     string
		conflict bool
		wantErr  string
	}{
		{name: "separate lines", draft: draftA, base: before, project: projectE, want: bothAE},
		{
			name:  "same lines",
			draft: draftA, base: before, project: strings.Replace(before, "A = 1", "A = 11", 1),
			want: "package design\n\n<<<<<<< draft\nvar A = 10\n=======\nvar A = 11\n>>>>>>> project folder\n" +
				"\nvar B = 2\n\nvar C = 3\n\nvar D = 4\n\nvar E = 5\n",
			conflict: true,
		},
		{name: "one line changed alike, every line unique", draft: draftA, base: before, project: bothAE, want: bothAE},
		{
			name:  "added on both sides",
			draft: "package design\n\nvar A = 1\n", base: "", project: "package design\n\nvar A = 2\n",
			want:     "package design\n\n<<<<<<< draft\nvar A = 1\n=======\nvar A = 2\n>>>>>>> project folder\n",
			conflict: true,
		},
		{
			name:  "a text git refuses",
			draft: "package design\n\x00var A = 1\n", base: before, project: projectE,
			wantErr: "Cannot merge binary files",
		},
		{
			name:  "merged again, every line unique: unchanged",
			draft: draftA, base: before, project: projectE,
			once: bothAE,
			want: bothAE,
		},
		{
			// The review's plain-line case: the draft already holds the
			// project's B2 and X, and gets them a second time.
			name:  "merged again, blank lines repeat: a conflict",
			draft: "A\nB\n\nC2\nD2\n\nE\n", base: "A\nB\n\nC\nD\nE\n", project: "A\n\nB2\nX\n\nC\nD\nE\n",
			once:     "A\n\nB2\nX\n\nC2\nD2\n\nE\n",
			want:     "A\n<<<<<<< draft\n=======\n\nB2\nX\n>>>>>>> project folder\n\nB2\nX\n\nC2\nD2\n\nE\n",
			conflict: true,
		},
		{
			// The project removed one of three blank lines, which the draft
			// already lacks, and git removes another.
			name:  "merged again, blank lines repeat: no conflict",
			draft: "})\n\n\n\n", base: "\n\n\n", project: "\n\n",
			once: "})\n\n\n",
			want: "})\n\n",
		},
	}
	temp := zdiff3Everywhere(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			draft := tc.draft
			if tc.once != "" {
				first, conflict, err := MergeFile(ctx, tc.draft, tc.base, tc.project)
				require.NoError(t, err)
				require.False(t, conflict, "the first merge is clean")
				require.Equal(t, tc.once, first)
				draft = first
			}

			merged, conflict, err := MergeFile(ctx, draft, tc.base, tc.project)

			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, tc.want, merged)
			assert.Equal(t, tc.conflict, conflict)
			left, err := os.ReadDir(temp)
			require.NoError(t, err)
			assert.Empty(t, left, "the temporary folder is removed")
		})
	}
}

// TestMergeFileCanLeaveACleanResultOutOfGofmtForm merges three texts in gofmt
// form whose changes lie on separate lines of one aligned block, and checks
// that the clean result is not in gofmt form: the block's alignment was set
// by the lines each side shortened.
func TestMergeFileCanLeaveACleanResultOutOfGofmtForm(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	const (
		base    = "package design\n\nvar M = map[string]int{\n\t\"aaa\": 1,\n\t\"b\":   2,\n\t\"ccc\": 3,\n}\n"
		draft   = "package design\n\nvar M = map[string]int{\n\t\"a\":   1,\n\t\"b\":   2,\n\t\"ccc\": 3,\n}\n"
		project = "package design\n\nvar M = map[string]int{\n\t\"aaa\": 1,\n\t\"b\":   2,\n\t\"c\":   3,\n}\n"
		merged  = "package design\n\nvar M = map[string]int{\n\t\"a\":   1,\n\t\"b\":   2,\n\t\"c\":   3,\n}\n"
		gofmt   = "package design\n\nvar M = map[string]int{\n\t\"a\": 1,\n\t\"b\": 2,\n\t\"c\": 3,\n}\n"
	)
	for _, text := range []string{base, draft, project} {
		formatted, err := Canonical("design/types.go", text)
		require.NoError(t, err)
		require.Equal(t, text, formatted, "every side is in gofmt form")
	}

	got, conflict, err := MergeFile(context.Background(), draft, base, project)

	require.NoError(t, err)
	assert.False(t, conflict)
	assert.Equal(t, merged, got)
	formatted, err := Canonical("design/types.go", got)
	require.NoError(t, err)
	assert.Equal(t, gofmt, formatted)
}

// zdiff3Everywhere asks for the zdiff3 conflict style in the user's global
// configuration file (through XDG_CONFIG_HOME) and in a repository that is
// around the temporary folder, where MergeFile's git runs, and that an
// inherited GIT_DIR names; it returns that folder. Outside a repository,
// git merge-file reads the global file but not configuration given on the
// command line or in GIT_CONFIG_PARAMETERS or GIT_CONFIG_COUNT, so those
// need no case. The repository is made with mergeEnv's environment, before
// GIT_DIR is set.
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
	xdg := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(xdg, "git"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(xdg, "git", "config"), []byte("[merge]\n\tconflictStyle = zdiff3\n"), 0o600))
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("GIT_DIR", filepath.Join(repo, ".git"))
	return temp
}
