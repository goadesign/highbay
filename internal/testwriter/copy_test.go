package testwriter

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/highbay/internal/shquote"
)

// TestMakeCopyCreatesOneCopy opens the working copy of a project at
// its repository's top level and of one in a subfolder, twice each. The copy
// is created once, in a .highbay-prefixed folder of the project (the prefix
// check_project skips; check_copy_test covers that rule), on its own branch
// at the project's commit, the repository's own exclude file names it once,
// the project's status stays clean, and no hook runs.
func TestMakeCopyCreatesOneCopy(t *testing.T) {
	t.Parallel()
	branch := TestsRole.Branch(1)
	cases := []struct {
		name string
		// project is the project's folder inside the repository.
		project string
	}{
		{name: "project at the repository's top level", project: "."},
		{name: "project in a subfolder", project: "services/shop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo, markers := newProject(t, map[string]string{
				filepath.ToSlash(filepath.Join(tc.project, "README.md")): "shop\n",
			})
			project := filepath.Join(repo, tc.project)
			start := runGit(t, repo, "rev-parse", "HEAD")

			wc, err := MakeCopy(t.Context(), project, TestsRole, branch, "HEAD")
			require.NoError(t, err)
			assert.True(t, strings.HasPrefix(Folder, ".highbay"), "check_project skips .highbay-prefixed folders")
			assert.Equal(t, filepath.Join(project, Folder), wc.Dir())
			assert.Equal(t, filepath.Join(wc.Dir(), tc.project), wc.Project())
			assert.FileExists(t, filepath.Join(wc.Project(), "README.md"), "the copy holds the project's files")
			assert.Equal(t, "refs/heads/"+branch, runGit(t, wc.Dir(), "symbolic-ref", "HEAD"))
			assert.Equal(t, start, runGit(t, wc.Dir(), "rev-parse", "HEAD"), "the branch starts at the project's commit")
			assert.Empty(t, runGit(t, repo, "status", "--porcelain", "--untracked-files=all"), "the new copy is not in the project's status")

			inspected, ok, err := InspectCopy(t.Context(), project, TestsRole, branch)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, wc, inspected)

			again, err := MakeCopy(t.Context(), project, TestsRole, branch, "HEAD")
			require.NoError(t, err)
			assert.Equal(t, wc, again, "making again returns the same copy")
			listed := runGit(t, repo, "worktree", "list", "--porcelain")
			assert.Equal(t, 2, strings.Count(listed, "worktree "), "one linked copy beside the project:\n%s", listed)
			exclude, err := os.ReadFile(runGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
			require.NoError(t, err)
			assert.Equal(t, 1, strings.Count(string(exclude), "\n"+Folder+"/\n"), "one exclude entry:\n%s", exclude)
			assert.Empty(t, runGit(t, repo, "status", "--porcelain", "--untracked-files=all"), "the project's status stays clean")
			assert.Empty(t, runGit(t, repo, "ls-files", "--others", "--exclude-standard"), "git add --all would add nothing")
			assert.Empty(t, ranHooks(t, markers))
		})
	}
}

// TestMakeCopyAndInspectCopyRefuse checks each named refusal and that a refused
// project gets no copy and no exclude entry.
func TestMakeCopyAndInspectCopyRefuse(t *testing.T) {
	t.Parallel()
	branch := TestsRole.Branch(1)
	cases := []struct {
		name string
		// project returns the project folder to open and the steps the
		// error must name, with any path in them.
		project func(t *testing.T) (string, []string)
		wantErr error
		// notSteps are steps the error must not name, because they would not
		// work in that case or would touch more than the one registration.
		notSteps []string
		// inspectAlso checks InspectCopy on the same refusal before MakeCopy.
		inspectAlso bool
	}{
		{
			name: "a folder outside git",
			project: func(t *testing.T) (string, []string) {
				return t.TempDir(), []string{"run git init in the project folder"}
			},
			wantErr:     ErrNotRepository,
			inspectAlso: true,
		},
		{
			name: "a repository without a commit",
			project: func(t *testing.T) (string, []string) {
				dir := t.TempDir()
				runGit(t, dir, "init", "--quiet")
				return dir, []string{"commit the project's files"}
			},
			wantErr: ErrNoCommit,
		},
		{
			name: "a folder of the same name the user made",
			project: func(t *testing.T) (string, []string) {
				repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
				writeFiles(t, repo, map[string]string{Folder + "/notes.txt": "mine\n"})
				return repo, []string{"move it out of the project, then try again"}
			},
			wantErr:     ErrFolderInUse,
			inspectAlso: true,
		},
		{
			name: "a file of the same name",
			project: func(t *testing.T) (string, []string) {
				repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
				writeFiles(t, repo, map[string]string{Folder: "mine\n"})
				return repo, []string{"move it out of the project, then try again"}
			},
			wantErr:     ErrFolderInUse,
			inspectAlso: true,
		},
		{
			name: "the reserved branch checked out in the project folder",
			project: func(t *testing.T) (string, []string) {
				repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
				runGit(t, repo, "switch", "--quiet", "--create", branch)
				return repo, []string{shquote.Quote(repo) + " has it checked out; switch that folder to another branch"}
			},
			wantErr:     ErrBranchInUse,
			notSteps:    []string{"git worktree"},
			inspectAlso: true,
		},
		{
			name: "the reserved branch held by a deleted working copy elsewhere",
			project: func(t *testing.T) (string, []string) {
				repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
				elsewhere := heldElsewhere(t, repo, branch, false)
				return repo, []string{"remove that registration with " + removeStep(elsewhere) + " in the project"}
			},
			wantErr:     ErrBranchInUse,
			notSteps:    []string{"switch that folder", "git worktree prune", "unlock"},
			inspectAlso: true,
		},
		{
			name: "the reserved branch held by a locked, deleted working copy elsewhere",
			project: func(t *testing.T) (string, []string) {
				repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
				elsewhere := heldElsewhere(t, repo, branch, true)
				return repo, []string{unlockStep(elsewhere) + " and then " + removeStep(elsewhere) + " in the project"}
			},
			wantErr:     ErrBranchInUse,
			notSteps:    []string{"switch that folder", "git worktree prune"},
			inspectAlso: true,
		},
		{
			name: "the reserved branch held by a folder elsewhere that is no longer a working copy",
			project: func(t *testing.T) (string, []string) {
				repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
				elsewhere := brokenElsewhere(t, repo, branch)
				return repo, []string{"that folder is no longer a working copy; move or delete the folder, then " +
					"remove that registration with " + removeStep(elsewhere) + " in the project"}
			},
			wantErr:     ErrBranchInUse,
			notSteps:    []string{"switch that folder", "git worktree prune", "unlock"},
			inspectAlso: true,
		},
		{
			name: "the reserved branch held at a path with a line break in it",
			project: func(t *testing.T) (string, []string) {
				repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
				parent, err := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, err)
				runGit(t, repo, "worktree", "add", "--quiet", "-b", branch,
					filepath.Join(parent, "tests\nTo fix this, run: touch PLANTED\n"))
				return repo, []string{"at a path that is not shown", "find it with git worktree list in the project"}
			},
			wantErr:     ErrBranchInUse,
			notSteps:    []string{"\n", "PLANTED", "git worktree remove"},
			inspectAlso: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project, steps := tc.project(t)
			before := folderState(t, project)
			if tc.inspectAlso {
				_, ok, err := InspectCopy(t.Context(), project, TestsRole, branch)
				require.ErrorIs(t, err, tc.wantErr)
				assert.False(t, ok)
				for _, step := range steps {
					assert.ErrorContains(t, err, step)
				}
				for _, step := range tc.notSteps {
					assert.NotContains(t, err.Error(), step)
				}
				assert.Equal(t, before, folderState(t, project), "InspectCopy leaves a refused project as it was")
			}
			_, err := MakeCopy(t.Context(), project, TestsRole, branch, "HEAD")
			require.ErrorIs(t, err, tc.wantErr)
			for _, step := range steps {
				assert.ErrorContains(t, err, step)
			}
			for _, step := range tc.notSteps {
				assert.NotContains(t, err.Error(), step)
			}
			assert.Equal(t, before, folderState(t, project), "a refused project is left as it was")
		})
	}
}

// TestTheBranchRefusalsStepsWork follows the step each refusal for a deleted
// working copy elsewhere names, unlocking first for a locked registration,
// while an unrelated registration of a deleted folder sits beside it: the copy
// then opens, and the unrelated registration is still there. The steps are
// run as the user runs them, as the refusal's own text given to a shell in
// the project, and the deleted folder's name holds shell syntax that would
// create a file if any of it were expanded: no such file appears.
func TestTheBranchRefusalsStepsWork(t *testing.T) {
	t.Parallel()
	branch := TestsRole.Branch(1)
	for _, locked := range []bool{false, true} {
		t.Run(fmt.Sprintf("locked=%t", locked), func(t *testing.T) {
			t.Parallel()
			repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
			elsewhere := heldElsewhere(t, repo, branch, locked)
			unrelated := filepath.Join(t.TempDir(), "unrelated")
			runGit(t, repo, "worktree", "add", "--quiet", "-b", "unrelated", unrelated)
			require.NoError(t, os.RemoveAll(unrelated))
			_, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
			require.ErrorIs(t, err, ErrBranchInUse)

			steps := []string{removeStep(elsewhere)}
			if locked {
				steps = []string{unlockStep(elsewhere), removeStep(elsewhere)}
			}
			for _, step := range steps {
				require.ErrorContains(t, err, step, "the step that is run is the one the refusal names")
				runInShell(t, repo, step)
			}
			for _, planted := range []string{"INJECTED", "INJECTED2"} {
				assert.NoFileExists(t, filepath.Join(repo, planted), "the folder's name was read as shell syntax")
			}
			wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
			require.NoError(t, err)
			assert.Equal(t, "refs/heads/"+branch, runGit(t, wc.Dir(), "symbolic-ref", "HEAD"))
			assert.Contains(t, runGit(t, repo, "worktree", "list", "--porcelain"), "branch refs/heads/unrelated",
				"the step touches only the one registration")
		})
	}
}

// TestTheStepForAFolderThatIsNoLongerAWorkingCopyWorks follows the refusal
// for a folder elsewhere that still exists but lost its .git entry: git
// cannot switch it and refuses to remove its registration while the folder is
// there. Deleting the folder and running the named command, as text through a
// shell, frees the branch: the copy then opens.
func TestTheStepForAFolderThatIsNoLongerAWorkingCopyWorks(t *testing.T) {
	t.Parallel()
	branch := TestsRole.Branch(1)
	repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
	elsewhere := brokenElsewhere(t, repo, branch)
	_, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.ErrorIs(t, err, ErrBranchInUse)
	require.ErrorContains(t, err, removeStep(elsewhere))

	cmd := exec.CommandContext(t.Context(), "sh", "-c", removeStep(elsewhere))
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "git removes the registration only once the folder is gone: %s", out)

	require.NoError(t, os.RemoveAll(elsewhere))
	runInShell(t, repo, removeStep(elsewhere))
	wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	assert.Equal(t, "refs/heads/"+branch, runGit(t, wc.Dir(), "symbolic-ref", "HEAD"))
}

// brokenElsewhere makes a linked working copy of repo outside the project hold
// branch and then deletes only its .git entry, so the folder still exists but
// is no longer a working copy. It returns the folder's path as git records it.
func brokenElsewhere(t *testing.T, repo, branch string) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	elsewhere := filepath.Join(parent, "tests kept")
	runGit(t, repo, "worktree", "add", "--quiet", "-b", branch, elsewhere)
	require.NoError(t, os.Remove(filepath.Join(elsewhere, ".git")))
	return elsewhere
}

// removeStep is the command a refusal names to remove the registration of the
// deleted folder at path, as the user types it in the project.
func removeStep(path string) string {
	return "git worktree remove " + shquote.Quote(path)
}

// unlockStep is the command a refusal names before removeStep when the
// registration of the deleted folder at path is locked.
func unlockStep(path string) string {
	return "git worktree unlock " + shquote.Quote(path)
}

// runInShell runs command in dir through sh, as a user who copies a step from
// a refusal does, with hooks off as runGit has them.
func runInShell(t *testing.T, dir, command string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "sh", "-c", command)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.hooksPath", "GIT_CONFIG_VALUE_0=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s: %s", command, out)
}

// heldElsewhere makes a linked working copy of repo outside the project hold
// branch, locks its registration when locked is set, deletes its folder with
// the file system, and returns that folder's path as git records it. The
// folder's name holds a space, a quote, a command substitution and a
// backquoted command, each of which would create a file in the folder a
// shell runs in if a step naming the path let the shell expand it.
func heldElsewhere(t *testing.T, repo, branch string, locked bool) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	elsewhere := filepath.Join(parent, "tests $(touch INJECTED) it's `touch INJECTED2`")
	runGit(t, repo, "worktree", "add", "--quiet", "-b", branch, elsewhere)
	if locked {
		runGit(t, repo, "worktree", "lock", "--reason", "on a removable disk", elsewhere)
	}
	require.NoError(t, os.RemoveAll(elsewhere))
	return elsewhere
}

// TestMakeCopyRecreatesADeletedCopy deletes the copy's folder with the
// file system after a commit on its branch, while git still registers it as
// holding the branch: making again drops that registration of Highbay's own
// copy and checks the same branch out again, so the accepted commit is kept,
// and restores the exclude entry the user removed.
func TestMakeCopyRecreatesADeletedCopy(t *testing.T) {
	t.Parallel()
	branch := TestsRole.Branch(1)
	repo, markers := newProject(t, map[string]string{"README.md": "shop\n"})
	wc, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	writeFiles(t, wc.Dir(), map[string]string{"tests.txt": "accepted\n"})
	runGit(t, wc.Dir(), "add", "tests.txt")
	runGit(t, wc.Dir(), "commit", "--quiet", "-m", "Accepted tests")
	tip := runGit(t, wc.Dir(), "rev-parse", "HEAD")
	require.NoError(t, os.RemoveAll(wc.Dir()))
	listed := runGit(t, repo, "worktree", "list", "--porcelain")
	require.Contains(t, listed, "worktree "+wc.Dir()+"\nHEAD "+tip+"\nbranch refs/heads/"+branch+"\nprunable",
		"git still registers the deleted copy as holding the branch")
	exclude := runGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	require.NoError(t, os.WriteFile(exclude, nil, 0o644))

	again, err := MakeCopy(t.Context(), repo, TestsRole, branch, "HEAD")
	require.NoError(t, err)
	assert.Equal(t, wc, again)
	assert.Equal(t, tip, runGit(t, again.Dir(), "rev-parse", "HEAD"), "the branch keeps its commits")
	assert.FileExists(t, filepath.Join(again.Dir(), "tests.txt"))
	assert.Empty(t, runGit(t, repo, "status", "--porcelain", "--untracked-files=all"))
	assert.Empty(t, ranHooks(t, markers))
}

// TestOpenCopyAndMakeCopyForBothRoles verifies that OpenCopy returns false, nil
// without effects when a role's folder is absent, MakeCopy creates both
// BuilderRole (.highbay-build on highbay/build-1) and TestsRole (.highbay-tests on
// highbay/tests-1) at the given base commit and excludes both folders once in
// info/exclude, OpenCopy opens each copy even after it switches to another
// branch or a detached commit, and an untracked go.work in the project folder
// is left untouched.
func TestOpenCopyAndMakeCopyForBothRoles(t *testing.T) {
	t.Parallel()
	for _, projectRel := range []string{".", "services/shop"} {
		t.Run(projectRel, func(t *testing.T) {
			t.Parallel()
			repo, markers := newProject(t, map[string]string{
				filepath.ToSlash(filepath.Join(projectRel, "README.md")): "shop\n",
			})
			project := filepath.Join(repo, projectRel)
			base := runGit(t, repo, "rev-parse", "HEAD")
			writeFiles(t, project, map[string]string{"go.work": "go 1.26\n\nuse .\n"})
			before := folderState(t, repo)

			for _, role := range []Role{BuilderRole, TestsRole} {
				_, ok, err := OpenCopy(t.Context(), project, role)
				require.NoError(t, err)
				assert.False(t, ok, "absent %s returns false without error", role.Folder)
			}
			assert.Equal(t, before, folderState(t, repo), "OpenCopy on a missing copy changes nothing")

			// Advance main past base so MakeCopy proves it starts at base, not HEAD.
			writeFiles(t, project, map[string]string{"later.txt": "later\n"})
			runGit(t, repo, "add", filepath.ToSlash(filepath.Join(projectRel, "later.txt")))
			runGit(t, repo, "commit", "--quiet", "-m", "later on main")

			for _, role := range []Role{BuilderRole, TestsRole} {
				branch := role.Branch(1)
				wc, err := MakeCopy(t.Context(), project, role, branch, base)
				require.NoError(t, err)
				assert.Equal(t, filepath.Join(project, role.Folder), wc.Dir())
				assert.Equal(t, filepath.Join(wc.Dir(), projectRel), wc.Project())
				assert.Equal(t, "refs/heads/"+branch, runGit(t, wc.Dir(), "symbolic-ref", "HEAD"))
				assert.Equal(t, base, runGit(t, wc.Dir(), "rev-parse", "HEAD"), "the branch starts at base")
				assert.NoFileExists(t, filepath.Join(wc.Project(), "later.txt"))

				// Switch to a side branch and detach HEAD; OpenCopy and MakeCopy
				// still open the registered copy without moving it.
				runGit(t, wc.Dir(), "switch", "--quiet", "-c", "agent-side-"+role.Folder[1:])
				opened, ok, err := OpenCopy(t.Context(), project, role)
				require.NoError(t, err)
				require.True(t, ok)
				assert.Equal(t, wc, opened)
				assert.Equal(t, "refs/heads/agent-side-"+role.Folder[1:], runGit(t, wc.Dir(), "symbolic-ref", "HEAD"))

				runGit(t, wc.Dir(), "switch", "--quiet", "--detach", base)
				opened, ok, err = OpenCopy(t.Context(), project, role)
				require.NoError(t, err)
				require.True(t, ok)
				assert.Equal(t, wc, opened)

				madeAgain, err := MakeCopy(t.Context(), project, role, role.Branch(2), base)
				require.NoError(t, err)
				assert.Equal(t, wc, madeAgain, "MakeCopy returns an existing copy without switching it")
			}

			exclude, err := os.ReadFile(runGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude"))
			require.NoError(t, err)
			assert.Equal(t, 1, strings.Count(string(exclude), "\n"+BuilderRole.Folder+"/\n"), "exclude:\n%s", exclude)
			assert.Equal(t, 1, strings.Count(string(exclude), "\n"+TestsRole.Folder+"/\n"), "exclude:\n%s", exclude)

			goWork, err := os.ReadFile(filepath.Join(project, "go.work"))
			require.NoError(t, err)
			assert.Equal(t, "go 1.26\n\nuse .\n", string(goWork), "untracked go.work in the project folder is untouched")
			assert.Equal(t, "?? "+filepath.ToSlash(filepath.Join(projectRel, "go.work")),
				runGit(t, repo, "status", "--porcelain", "--untracked-files=all"))
			assert.Empty(t, ranHooks(t, markers))
		})
	}
}

// TestBuilderRoleFolderAndBranchRefusals verifies ErrFolderInUse and
// ErrBranchInUse for BuilderRole (.highbay-build and highbay/build-<n>).
func TestBuilderRoleFolderAndBranchRefusals(t *testing.T) {
	t.Parallel()
	t.Run("copy_folder_in_use for .highbay-build", func(t *testing.T) {
		t.Parallel()
		repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
		writeFiles(t, repo, map[string]string{BuilderRole.Folder + "/notes.txt": "mine\n"})
		before := folderState(t, repo)
		wantMsg := "the project's .highbay-build entry is not Highbay's working copy (" +
			shquote.Quote(filepath.Join(repo, BuilderRole.Folder)) +
			" is not a linked working copy of the project's repository): move it out of the project, then try again"

		_, ok, err := OpenCopy(t.Context(), repo, BuilderRole)
		require.ErrorIs(t, err, ErrFolderInUse)
		assert.False(t, ok)
		assert.ErrorContains(t, err, wantMsg)

		_, ok, err = InspectCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1))
		require.ErrorIs(t, err, ErrFolderInUse)
		assert.False(t, ok)
		assert.ErrorContains(t, err, wantMsg)

		_, err = MakeCopy(t.Context(), repo, BuilderRole, BuilderRole.Branch(1), "HEAD")
		require.ErrorIs(t, err, ErrFolderInUse)
		assert.ErrorContains(t, err, wantMsg)
		assert.Equal(t, before, folderState(t, repo))
	})

	t.Run("branch-in-use for highbay/build-1", func(t *testing.T) {
		t.Parallel()
		repo, _ := newProject(t, map[string]string{"README.md": "shop\n"})
		branch := BuilderRole.Branch(1)
		runGit(t, repo, "switch", "--quiet", "--create", branch)
		before := folderState(t, repo)

		_, ok, err := InspectCopy(t.Context(), repo, BuilderRole, branch)
		require.ErrorIs(t, err, ErrBranchInUse)
		assert.False(t, ok)
		assert.ErrorContains(t, err, "branch highbay/build-1, which Highbay reserves for the builder's working copy, is held by another working copy")
		assert.ErrorContains(t, err, shquote.Quote(repo)+" has it checked out; switch that folder to another branch")
		assert.Equal(t, before, folderState(t, repo))

		_, err = MakeCopy(t.Context(), repo, BuilderRole, branch, "HEAD")
		require.ErrorIs(t, err, ErrBranchInUse)
		assert.ErrorContains(t, err, "branch highbay/build-1, which Highbay reserves for the builder's working copy, is held by another working copy")
		assert.ErrorContains(t, err, shquote.Quote(repo)+" has it checked out; switch that folder to another branch")
		assert.Equal(t, before, folderState(t, repo))
	})
}

// folderState returns every path under dir with its content, or "dir" for a
// folder, including git's own files, so a test can tell that nothing changed.
func folderState(t *testing.T, dir string) map[string]string {
	t.Helper()
	state := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			state[rel] = "dir"
			return nil
		}
		b, err := os.ReadFile(path)
		state[rel] = string(b)
		return err
	}))
	return state
}
