// This file checks build branch names, build numbers and the checks on a
// commit of the open build against real temporary git repositories. Each case
// replays a probe of the builder-copy design: R and N cases number builds, J
// and G cases are agents' git, A cases are a reviewer's attacks.
package testwriter

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type (
	// checkCase is one CheckBuildCommit case, replaying one probe.
	checkCase struct {
		// name starts with the probe case it replays.
		name string
		// arrange runs the probe's git in project, whose first commit is b0,
		// and returns the call to make for build 2 and the facts it must find.
		arrange func(t *testing.T, project, b0 string) (checkInput, CommitCheck)
	}

	// checkInput is the CheckBuildCommit call a case makes for build 2.
	checkInput struct {
		// commit is the commit checked.
		commit string
		// role owns the branch commit must be on: BuilderRole for a
		// candidate, TestsRole for the tip of the tests branch.
		role Role
		// base is build 2's base B.
		base string
		// recorded are the tips Drop recorded when the user dropped build 1.
		recorded []BranchTip
		// folder is the folder CheckBuildCommit is given, the project folder
		// when empty.
		folder string
		// markers are files a program the repository configures writes when
		// it runs; the call must leave none.
		markers []string
	}
)

// openBuild is the number of the build every CheckBuildCommit case checks:
// build 1 is the earlier one, merged or dropped.
const openBuild = 2

// TestParseBuildBranch checks which names are build branches: only the names
// Role.Branch gives, and the legacy surla/tests.
func TestParseBuildBranch(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		branch string
		// role and n are the role and number expected when ok is set.
		role Role
		n    int
		ok   bool
	}{
		{name: "the builder's branch of build 3", branch: "surla/build-3", role: BuilderRole, n: 3, ok: true},
		{name: "the test writer's branch of build 12", branch: "surla/tests-12", role: TestsRole, n: 12, ok: true},
		{name: "legacy surla/tests, numbered 0", branch: "surla/tests", role: TestsRole, n: 0, ok: true},
		{name: "G3 surla/build-2-backup is an agent's", branch: "surla/build-2-backup"},
		{name: "G3 surla/build-x is an agent's", branch: "surla/build-x"},
		{name: "review note 3 a number too large for an int", branch: "surla/build-99999999999999999999"},
		{name: "a leading zero Branch never writes", branch: "surla/build-007"},
		{name: "build 0, which no build has", branch: "surla/tests-0"},
		{name: "a branch outside surla/", branch: "build-3"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			role, n, ok := ParseBuildBranch(tc.branch)
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.role, role)
			assert.Equal(t, tc.n, n)
			if ok && n >= 1 {
				assert.Equal(t, tc.branch, role.Branch(n), "Branch gives the name back")
			}
		})
	}
}

// TestNextNumber checks that a new build's number is one more than the
// larger of the saved number and the largest number of a build branch, so no
// number is given twice, and that none follows the largest int.
func TestNextNumber(t *testing.T) {
	t.Parallel()
	largest := "surla/build-" + strconv.Itoa(math.MaxInt)
	cases := []struct {
		name string
		// branches are made at the project's commit before the call.
		branches []string
		// saved is the open build's number.
		saved int
		// want is the number expected when wantErr is empty.
		want    int
		wantErr string
	}{
		{
			name:     "R4 the largest number for-each-ref lists",
			branches: []string{"surla/tests", "surla/build-2", "surla/tests-7"},
			saved:    2,
			want:     8,
		},
		{
			name:     "N1 a saved number whose branches were never made",
			branches: []string{"surla/build-1", "surla/tests-1"},
			saved:    2,
			want:     3,
		},
		{
			name:     "review note 3 a number too large for an int is not a build branch",
			branches: []string{"surla/build-1", "surla/tests-99999999999999999999"},
			saved:    1,
			want:     2,
		},
		{
			name:     "G3 surla/build-2-backup and surla/build-x are not build branches",
			branches: []string{"surla/build-1", "surla/build-2-backup", "surla/build-x"},
			saved:    1,
			want:     2,
		},
		{
			name:     "legacy surla/tests counts as build 0",
			branches: []string{"surla/tests"},
			want:     1,
		},
		{
			name:    "review N8 a negative saved number, which no build has",
			saved:   -5,
			wantErr: "the open build's number -5 is negative",
		},
		{
			name:     "review note 3 no number follows the largest int",
			branches: []string{"surla/build-1", largest},
			saved:    1,
			wantErr:  "branch " + largest + " is numbered " + strconv.Itoa(math.MaxInt),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project, _ := newProject(t, map[string]string{"go.mod": "module example.com/shop\n"})
			for _, branch := range tc.branches {
				runGit(t, project, "branch", branch)
			}
			got, err := NextNumber(t.Context(), project, tc.saved)
			if tc.wantErr != "" {
				assert.ErrorContains(t, err, tc.wantErr)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestCheckBuildCommitRefuses checks the commits of build 2 that hold
// another build's commit, are off their branch, or do not start from B: each
// is refused, and the other build's work is named with its files.
func TestCheckBuildCommitRefuses(t *testing.T) {
	t.Parallel()
	runCheckCases(t, []checkCase{
		{
			name: "J1 the builder commits on surla/build-1, where git switch - took it: off its branch",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, t1 := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				writeFiles(t, build, map[string]string{"svc/half.go": "package svc // half\n"})
				joinBuild(t, project, BuilderRole, b0)
				joinBuild(t, project, TestsRole, b0)
				runGit(t, build, "switch", "--quiet", "-")
				x := commitAll(t, build, "C2 on the earlier branch",
					map[string]string{"svc/a.go": "package svc // exports edit\n"})
				in := asCandidate(x, b0)
				in.recorded = droppedBuild1(c1, t1)
				return in, CommitCheck{
					FromBase: true,
					Foreign:  []ForeignWork{work(x, "surla/build-1", "svc/a.go", "svc/half.go", "svc/refund.go")},
				}
			},
		},
		{
			name: "J2 the builder merges surla/build-1, where its join committed the leftovers",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, t1 := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				writeFiles(t, build, map[string]string{"svc/half.go": "package svc // half\n"})
				left := joinBuild(t, project, BuilderRole, b0)
				joinBuild(t, project, TestsRole, b0)
				commitAll(t, build, "C2", map[string]string{"svc/a.go": "package svc // exports edit\n"})
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				in := asCandidate(revision(t, project, "surla/build-2"), b0)
				in.recorded = droppedBuild1(c1, t1)
				return in, CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(left, "surla/build-1", "svc/half.go", "svc/refund.go")},
				}
			},
		},
		{
			name: "J4 refunds merged by the user, then the builder merges surla/build-1 with its leftover commit",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, t1 := commitBuild1(t, project, false)
				runGit(t, project, "merge", "--quiet", "--no-edit", "--ff", c1)
				runGit(t, project, "merge", "--quiet", "--no-edit", "--ff", t1)
				base := revision(t, project, "HEAD")
				build := filepath.Join(project, BuilderRole.Folder)
				writeFiles(t, build, map[string]string{"svc/half.go": "package svc // half\n"})
				left := joinBuild(t, project, BuilderRole, base)
				commitAll(t, build, "C2", map[string]string{"svc/x.go": "package svc // exports\n"})
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				return asCandidate(revision(t, project, "surla/build-2"), base), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(left, "surla/build-1", "svc/half.go")},
				}
			},
		},
		{
			name: "J5 refunds squashed by the user, then the builder merges surla/build-1",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, _ := commitBuild1(t, project, false)
				runGit(t, project, "merge", "--quiet", "--squash", "surla/build-1")
				runGit(t, project, "commit", "--quiet", "-m", "S1")
				runGit(t, project, "merge", "--quiet", "--squash", "surla/tests-1")
				runGit(t, project, "commit", "--quiet", "-m", "S2")
				base := revision(t, project, "HEAD")
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, base)
				commitAll(t, build, "C2", map[string]string{"svc/x.go": "package svc // exports\n"})
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				return asCandidate(revision(t, project, "surla/build-2"), base), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "svc/refund.go")},
				}
			},
		},
		{
			name: "J7, G2 the test writer resets surla/tests-2 onto surla/tests-1",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				_, t1 := commitBuild1(t, project, true)
				tests := filepath.Join(project, TestsRole.Folder)
				joinBuild(t, project, TestsRole, b0)
				writeFiles(t, tests, map[string]string{"eval/export.go": "package eval // export\n"})
				runGit(t, tests, "reset", "--quiet", "--hard", "surla/tests-1")
				return asTestsTip(revision(t, project, "surla/tests-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(t1, "surla/tests-1", "eval/refund.go")},
				}
			},
		},
		{
			name: "J9 the builder merges its side branch try, made from surla/build-1",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, _ := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				runGit(t, build, "switch", "--quiet", "-c", "try")
				commitAll(t, build, "try1", map[string]string{"svc/try.go": "package svc // try\n"})
				joinBuild(t, project, BuilderRole, b0)
				commitAll(t, build, "C2", map[string]string{"svc/x.go": "package svc // exports\n"})
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "try")
				return asCandidate(revision(t, project, "surla/build-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "svc/refund.go")},
				}
			},
		},
		{
			name: "G1 the builder merges the dropped surla/build-1: its commit and files named once",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, t1 := commitBuild1(t, project, true)
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, b0)
				commitAll(t, build, "C2", map[string]string{"svc/export.go": "package svc // export\n"})
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				in := asCandidate(revision(t, project, "surla/build-2"), b0)
				in.recorded = droppedBuild1(c1, t1)
				return in, CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "svc/a.go", "svc/refund.go")},
				}
			},
		},
		{
			name: "G1 with seven files, one changed twice: the first five named, seven counted",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				build := filepath.Join(project, BuilderRole.Folder)
				files := map[string]string{}
				for i := range 7 {
					files["svc/refund"+strconv.Itoa(i)+".go"] = "package svc\n"
				}
				commitAll(t, build, "C1", files)
				c1 := commitAll(t, build, "C1b", map[string]string{"svc/refund0.go": "package svc // again\n"})
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				return asCandidate(revision(t, project, "surla/build-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign: []ForeignWork{{
						MergeBase: c1,
						Branch:    "surla/build-1",
						Files: []string{
							"svc/refund0.go", "svc/refund1.go", "svc/refund2.go", "svc/refund3.go", "svc/refund4.go",
						},
						FileCount: 7,
					}},
				}
			},
		},
		{
			name: "G1 with a moved file: its old and new paths named",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				build := filepath.Join(project, BuilderRole.Folder)
				runGit(t, build, "mv", "svc/a.go", "svc/moved.go")
				runGit(t, build, "commit", "--quiet", "-m", "C1")
				c1 := revision(t, build, "HEAD")
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				return asCandidate(revision(t, project, "surla/build-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "svc/a.go", "svc/moved.go")},
				}
			},
		},
		{
			name: "G4 the agent's own commit on the earlier surla/build-1, brought in by a fast-forward",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, _ := commitBuild1(t, project, true)
				runGit(t, project, "merge", "--quiet", "--ff-only", c1)
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, c1)
				runGit(t, build, "switch", "--quiet", "-")
				e := commitAll(t, build, "E-exports",
					map[string]string{"svc/export.go": "package svc // export\n"})
				runGit(t, build, "switch", "--quiet", "surla/build-2")
				runGit(t, build, "merge", "--quiet", "--ff-only", "surla/build-1")
				return asCandidate(revision(t, project, "surla/build-2"), c1), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(e, "surla/build-1", "svc/export.go")},
				}
			},
		},
		{
			name: "G6 a criss-cross: the files of both merge bases named",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, true)
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, b0)
				x2 := commitAll(t, build, "X2", map[string]string{"svc/x.go": "x\n"})
				runGit(t, build, "switch", "--quiet", "surla/build-1")
				runGit(t, build, "switch", "--quiet", "-c", "side")
				y1 := commitAll(t, build, "Y1", map[string]string{"svc/y.go": "y\n"})
				for _, step := range [][2]string{
					{"surla/build-1", "surla/build-2"}, {"surla/build-2", "side"}, {"surla/build-1", "side"},
				} {
					runGit(t, build, "switch", "--quiet", step[0])
					runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", step[1])
				}
				runGit(t, build, "switch", "--quiet", "surla/build-2")
				return asCandidate(revision(t, project, "surla/build-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign: []ForeignWork{
						work(y1, "surla/build-1", "svc/a.go", "svc/refund.go", "svc/y.go"),
						work(x2, "surla/build-1", "svc/x.go"),
					},
				}
			},
		},
		{
			name: "review note 4 the candidate is B itself",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, true)
				joinBuild(t, project, BuilderRole, b0)
				return asCandidate(b0, b0), CommitCheck{OnBranch: true, FromBase: true, IsBase: true}
			},
		},
		{
			name: "an ancestor of B does not descend from B",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, true)
				base := commitAll(t, project, "B1", map[string]string{"svc/b1.go": "package svc // b1\n"})
				joinBuild(t, project, BuilderRole, base)
				return asCandidate(b0, base), CommitCheck{OnBranch: true}
			},
		},
		{
			name: "A6 a file only the dropped build's merge resolution added, named (Q7)",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				runGit(t, build, "switch", "--quiet", "-c", "side", b0)
				commitAll(t, build, "S", map[string]string{"svc/side.go": "package svc // side\n"})
				runGit(t, build, "switch", "--quiet", "surla/build-1")
				runGit(t, build, "merge", "--quiet", "--no-commit", "--no-ff", "side")
				m := commitAll(t, build, "Merge side with evil.go",
					map[string]string{"svc/evil.go": "package svc // evil\n"})
				runGit(t, project, "branch", "--quiet", "-D", "side")
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "merge", "--quiet", "--ff-only", "surla/build-1")
				return asCandidate(revision(t, project, "surla/build-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign: []ForeignWork{
						work(m, "surla/build-1", "svc/evil.go", "svc/refund.go", "svc/side.go"),
					},
				}
			},
		},
		{
			name: "A2 a tip recorded at the Drop, after the agent moved surla/build-1 back to B (Q6)",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, t1 := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, b0)
				commitAll(t, build, "C2", map[string]string{"svc/export.go": "package svc // export\n"})
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				runGit(t, project, "branch", "--force", "surla/build-1", b0)
				in := asCandidate(revision(t, project, "surla/build-2"), b0)
				in.recorded = droppedBuild1(c1, t1)
				return in, CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "svc/refund.go")},
				}
			},
		},
		{
			name: "A07 an agent's info/grafts file drops C1 from the merge's parents: ignored",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, t1 := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, b0)
				c2 := commitAll(t, build, "C2", map[string]string{"svc/export.go": "package svc // export\n"})
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				x := revision(t, project, "surla/build-2")
				grafts := filepath.Join(commonDir(t, project), "info", "grafts")
				require.NoError(t, os.MkdirAll(filepath.Dir(grafts), 0o750))
				require.NoError(t, os.WriteFile(grafts, []byte(x+" "+c2+"\n"), 0o600))
				in := asCandidate(x, b0)
				in.recorded = droppedBuild1(c1, t1)
				return in, CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "svc/refund.go")},
				}
			},
		},
		{
			name: "A10 log.showRoot=false: the files of another build's root commit still named",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				build := filepath.Join(project, BuilderRole.Folder)
				runGit(t, build, "switch", "--quiet", "--orphan", "surla/build-7")
				root := commitAll(t, build, "notes", map[string]string{"notes.txt": "n\n"})
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "--allow-unrelated-histories",
					"surla/build-7")
				runGit(t, project, "config", "log.showRoot", "false")
				return asCandidate(revision(t, project, "surla/build-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(root, "surla/build-7", "notes.txt")},
				}
			},
		},
		{
			name: "A11 a merge driver and a textconv program the repository configures never run",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				// Both sides change svc/a.go apart, so merging it runs the
				// file's merge driver; the test merges before configuring one.
				build := filepath.Join(project, BuilderRole.Folder)
				source := func(a, z string) map[string]string {
					text := "package svc\n\nfunc A() {" + a + "}\n\n\n\n\n\nfunc Z() {" + z + "}\n"
					return map[string]string{"svc/a.go": text}
				}
				commitAll(t, build, "C0", source("", ""))
				runGit(t, build, "switch", "--quiet", "-c", "side")
				commitAll(t, build, "S", source(" side() ", ""))
				runGit(t, build, "switch", "--quiet", "surla/build-1")
				commitAll(t, build, "C1", source("", " one() "))
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "side")
				m := revision(t, project, "surla/build-1")
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "merge", "--quiet", "--ff-only", "surla/build-1")
				probe, marker := probeProgram(t)
				attributes := filepath.Join(commonDir(t, project), "info", "attributes")
				require.NoError(t, os.WriteFile(attributes, []byte("*.go diff=probe merge=probe\n"), 0o600))
				runGit(t, project, "config", "merge.probe.driver", probe+" %A")
				runGit(t, project, "config", "diff.probe.textconv", probe)
				in := asCandidate(revision(t, project, "surla/build-2"), b0)
				in.markers = []string{marker}
				return in, CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(m, "surla/build-1", "svc/a.go")},
				}
			},
		},
		{
			name: "A10b log.showSignature with a configured gpg: signatures are not verified",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, _ := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				body := filepath.Join(t.TempDir(), "commit")
				require.NoError(t, os.WriteFile(body, []byte("tree "+
					runGit(t, project, "rev-parse", c1+"^{tree}")+"\nparent "+c1+
					"\nauthor A <a@example.com> 1700000000 +0000\ncommitter A <a@example.com> 1700000000 +0000\n"+
					"gpgsig -----BEGIN PGP SIGNATURE-----\n \n iQEzBAABCAAdFiEE\n -----END PGP SIGNATURE-----\n"+
					"\nsigned on top of C1\n"), 0o600))
				signed := runGit(t, project, "hash-object", "-t", "commit", "-w", body)
				runGit(t, build, "switch", "--quiet", "--detach")
				runGit(t, project, "update-ref", "refs/heads/surla/build-1", signed)
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				probe, marker := probeProgram(t)
				runGit(t, project, "config", "log.showSignature", "true")
				runGit(t, project, "config", "gpg.program", probe)
				in := asCandidate(revision(t, project, "surla/build-2"), b0)
				in.markers = []string{marker}
				return in, CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(signed, "surla/build-1", "svc/refund.go")},
				}
			},
		},
		{
			name: "A26 a project folder below the repository's top with diff.relative=true: paths from the top",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, _ := commitBuild1(t, project, true)
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, filepath.Join(project, BuilderRole.Folder), "merge", "--quiet", "--no-edit", "--no-ff",
					"surla/build-1")
				runGit(t, project, "config", "diff.relative", "true")
				in := asCandidate(revision(t, project, "surla/build-2"), b0)
				in.folder = filepath.Join(project, "svc")
				return in, CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "svc/a.go", "svc/refund.go")},
				}
			},
		},
		{
			name: "diff.ignoreSubmodules=all: a submodule commit of the other build still named",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				build := filepath.Join(project, BuilderRole.Folder)
				runGit(t, build, "update-index", "--add", "--cacheinfo", "160000,"+b0+",vendor/lib")
				runGit(t, build, "commit", "--quiet", "-m", "C1 adds a submodule")
				// An empty folder, as checking out a submodule leaves, keeps the
				// copy clean, so its join commits nothing.
				require.NoError(t, os.MkdirAll(filepath.Join(build, "vendor", "lib"), 0o750))
				c1 := revision(t, build, "HEAD")
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				runGit(t, project, "config", "diff.ignoreSubmodules", "all")
				return asCandidate(revision(t, project, "surla/build-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "vendor/lib")},
				}
			},
		},
		{
			name: "A16 build 2's branch not made yet: off its branch, not an error",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, false)
				return asCandidate(b0, b0), CommitCheck{FromBase: true, IsBase: true}
			},
		},
		{
			name: "A17 legacy surla/tests merged into surla/tests-2: another build's branch",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				tests := filepath.Join(project, TestsRole.Folder)
				runGit(t, tests, "switch", "--quiet", "-c", "surla/tests")
				t0 := commitAll(t, tests, "T0", map[string]string{"eval/old.go": "package eval // old\n"})
				joinBuild(t, project, TestsRole, b0)
				runGit(t, tests, "merge", "--quiet", "--no-edit", "--no-ff", "surla/tests")
				return asTestsTip(revision(t, project, "surla/tests-2"), b0), CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(t0, "surla/tests", "eval/old.go")},
				}
			},
		},
		{
			name: "A3 a tip recorded at the Drop, after the agent deleted surla/build-1 (Q6)",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, t1 := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				runGit(t, project, "branch", "--quiet", "-D", "surla/build-1")
				in := asCandidate(revision(t, project, "surla/build-2"), b0)
				in.recorded = droppedBuild1(c1, t1)
				return in, CommitCheck{
					OnBranch: true,
					FromBase: true,
					Foreign:  []ForeignWork{work(c1, "surla/build-1", "svc/refund.go")},
				}
			},
		},
	})
}

// TestCheckBuildCommitAccepts checks the commits of build 2 that hold no
// commit of another build, including the stated limit: files brought without
// their commits.
func TestCheckBuildCommitAccepts(t *testing.T) {
	t.Parallel()
	accepted := CommitCheck{OnBranch: true, FromBase: true}
	runCheckCases(t, []checkCase{
		{
			name: "J3 the stated limit: the builder squashes surla/build-1's files into a commit of its own",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				c1, t1 := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, b0)
				commitAll(t, build, "C2", map[string]string{"svc/a.go": "package svc // exports edit\n"})
				runGit(t, build, "merge", "--quiet", "--squash", "surla/build-1")
				runGit(t, build, "commit", "--quiet", "-m", "C2squash")
				x := revision(t, project, "surla/build-2")
				runGit(t, project, "cat-file", "-e", x+":svc/refund.go")
				in := asCandidate(x, b0)
				in.recorded = droppedBuild1(c1, t1)
				return in, accepted
			},
		},
		{
			name: "J10 the user rewrote the history the dropped build started from",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				old := commitAll(t, project, "Bold", map[string]string{"svc/b1.go": "package svc // b1\n"})
				runGit(t, filepath.Join(project, BuilderRole.Folder), "reset", "--quiet", "--hard", old)
				c1, t1 := commitBuild1(t, project, false)
				runGit(t, project, "commit", "--quiet", "--amend", "-m", "Bnew")
				base := revision(t, project, "HEAD")
				joinBuild(t, project, BuilderRole, base)
				x := commitAll(t, filepath.Join(project, BuilderRole.Folder), "C2",
					map[string]string{"svc/x.go": "package svc // exports\n"})
				in := asCandidate(x, base)
				in.recorded = droppedBuild1(c1, t1)
				return in, accepted
			},
		},
		{
			name: "G3 the agent's own surla/build-2-backup and surla/build-x are not build branches",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, true)
				joinBuild(t, project, BuilderRole, b0)
				x := commitAll(t, filepath.Join(project, BuilderRole.Folder), "C2",
					map[string]string{"svc/export.go": "package svc // export\n"})
				runGit(t, project, "branch", "surla/build-2-backup", x)
				runGit(t, project, "branch", "surla/build-x", x)
				return asCandidate(x, b0), accepted
			},
		},
		{
			name: "G5 an orphan surla/build-7 shares no commit: merge-base exits 1, which passes",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, true)
				build := filepath.Join(project, BuilderRole.Folder)
				joinBuild(t, project, BuilderRole, b0)
				runGit(t, build, "switch", "--quiet", "--orphan", "surla/build-7")
				commitAll(t, build, "notes", map[string]string{"notes.txt": "n\n"})
				runGit(t, build, "switch", "--quiet", "surla/build-2")
				x := commitAll(t, build, "C2", map[string]string{"svc/export.go": "package svc // export\n"})
				return asCandidate(x, b0), accepted
			},
		},
		{
			name: "A7 the test writer moves surla/tests-2 onto this build's surla/build-2",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, false)
				joinBuild(t, project, BuilderRole, b0)
				commitAll(t, filepath.Join(project, BuilderRole.Folder), "Cwip",
					map[string]string{"svc/wip.go": "package svc // wip\n"})
				joinBuild(t, project, TestsRole, b0)
				runGit(t, filepath.Join(project, TestsRole.Folder), "reset", "--quiet", "--hard", "surla/build-2")
				return asTestsTip(revision(t, project, "surla/tests-2"), b0), accepted
			},
		},
		{
			name: "a candidate the user already merged",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				commitBuild1(t, project, true)
				joinBuild(t, project, BuilderRole, b0)
				x := commitAll(t, filepath.Join(project, BuilderRole.Folder), "C2",
					map[string]string{"svc/export.go": "package svc // export\n"})
				runGit(t, project, "merge", "--quiet", "--no-edit", "--ff", x)
				return asCandidate(x, b0), accepted
			},
		},
		{
			name: "a recorded tip the repository no longer holds: no commit can hold it",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				gone := runGit(t, project, "commit-tree", "--no-gpg-sign", b0+"^{tree}", "-p", b0,
					"-m", "dropped, then pruned")
				runGit(t, project, "prune", "--expire=now")
				_, err := git(t.Context(), project, nil, "cat-file", "-e", gone)
				assert.Error(t, err, "git pruned the recorded commit")
				joinBuild(t, project, BuilderRole, b0)
				x := commitAll(t, filepath.Join(project, BuilderRole.Folder), "C2",
					map[string]string{"svc/export.go": "package svc // export\n"})
				in := asCandidate(x, b0)
				in.recorded = []BranchTip{{Branch: "surla/build-1", Commit: gone}}
				return in, accepted
			},
		},
		{
			name: "A23 a recorded tip a partial clone lacks: skipped without asking its promisor remote",
			arrange: func(t *testing.T, project, b0 string) (checkInput, CommitCheck) {
				joinBuild(t, project, BuilderRole, b0)
				x := commitAll(t, filepath.Join(project, BuilderRole.Folder), "C2",
					map[string]string{"svc/export.go": "package svc // export\n"})
				// A lazy fetch would run the remote's ext:: command, the probe.
				probe, marker := probeProgram(t)
				for _, kv := range [][2]string{
					{"core.repositoryformatversion", "1"}, {"extensions.partialClone", "origin"},
					{"remote.origin.url", "ext::" + probe}, {"remote.origin.promisor", "true"},
					{"protocol.ext.allow", "always"},
				} {
					runGit(t, project, "config", kv[0], kv[1])
				}
				in := asCandidate(x, b0)
				in.recorded = []BranchTip{{Branch: "surla/build-1", Commit: strings.Repeat("e", len(x))}}
				in.markers = []string{marker}
				return in, accepted
			},
		},
	})
}

// TestCheckBuildCommitFailsWhenGitFails checks that a git failure other than
// the exit 1 each check expects fails the call, so a damaged repository, a
// canceled check or a caller's mistake is never taken for a pass.
func TestCheckBuildCommitFailsWhenGitFails(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		// arrange damages project, whose first commit is b0, and returns the
		// call to make.
		arrange func(t *testing.T, project, b0 string) checkInput
	}{
		{
			name: "A15 a commit missing inside surla/build-1's history: git merge-base --all exits 255",
			arrange: func(t *testing.T, project, b0 string) checkInput {
				c1, _ := commitBuild1(t, project, false)
				build := filepath.Join(project, BuilderRole.Folder)
				commitAll(t, build, "C1b", map[string]string{"svc/refund2.go": "package svc // refund 2\n"})
				joinBuild(t, project, BuilderRole, b0)
				x := commitAll(t, build, "C2", map[string]string{"svc/export.go": "package svc // export\n"})
				require.NoError(t, os.Remove(objectFile(t, project, c1)))
				return asCandidate(x, b0)
			},
		},
		{
			name: "A15b a commit missing inside B's history: git merge-base --is-ancestor exits 128",
			arrange: func(t *testing.T, project, b0 string) checkInput {
				// Build 1 started at B too, so merge-base --all with its
				// branch succeeds and only --is-ancestor <its commit> B walks
				// past B; the tests copy leaves surla/tests-1, whose walk
				// would also cross the missing commit.
				base := commitAll(t, project, "P", map[string]string{"svc/p.go": "package svc // p\n"})
				build := filepath.Join(project, BuilderRole.Folder)
				runGit(t, build, "reset", "--quiet", "--hard", base)
				commitAll(t, build, "C1", map[string]string{"svc/refund.go": "package svc // refund\n"})
				joinBuild(t, project, BuilderRole, base)
				commitAll(t, build, "C2", map[string]string{"svc/export.go": "package svc // export\n"})
				runGit(t, build, "merge", "--quiet", "--no-edit", "--no-ff", "surla/build-1")
				joinBuild(t, project, TestsRole, base)
				runGit(t, project, "branch", "--quiet", "-D", "surla/tests-1")
				require.NoError(t, os.Remove(objectFile(t, project, b0)))
				return asCandidate(revision(t, project, "surla/build-2"), base)
			},
		},
		{
			name: "a recorded tip whose object is corrupt: git rev-parse exits 128",
			arrange: func(t *testing.T, project, b0 string) checkInput {
				c1, t1 := commitBuild1(t, project, false)
				joinBuild(t, project, BuilderRole, b0)
				x := commitAll(t, filepath.Join(project, BuilderRole.Folder), "C2",
					map[string]string{"svc/export.go": "package svc // export\n"})
				runGit(t, project, "branch", "--quiet", "-D", "surla/build-1")
				object := objectFile(t, project, c1)
				require.NoError(t, os.Remove(object))
				require.NoError(t, os.WriteFile(object, []byte("not a zlib stream"), 0o444))
				in := asCandidate(x, b0)
				in.recorded = droppedBuild1(c1, t1)
				return in
			},
		},
		{
			name: "the branch the commit must be on is not one of the build's",
			arrange: func(t *testing.T, project, b0 string) checkInput {
				joinBuild(t, project, BuilderRole, b0)
				x := commitAll(t, filepath.Join(project, BuilderRole.Folder), "C2",
					map[string]string{"svc/export.go": "package svc // export\n"})
				in := asCandidate(x, b0)
				in.role = Role{Prefix: "main-"}
				return in
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project, b0 := newBuildRepo(t)
			got, err := checkBuild(t, project, tc.arrange(t, project, b0))
			assert.Error(t, err, "a git failure is an error")
			assert.Equal(t, CommitCheck{}, got, "never a pass")
		})
	}
}

// runCheckCases runs each case on a repository of its own and compares the
// facts CheckBuildCommit finds with the case's.
func runCheckCases(t *testing.T, cases []checkCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			project, b0 := newBuildRepo(t)
			in, want := tc.arrange(t, project, b0)
			got, err := checkBuild(t, project, in)
			require.NoError(t, err)
			assert.Equal(t, want.OnBranch, got.OnBranch, "on its branch")
			assert.Equal(t, want.FromBase, got.FromBase, "from B")
			assert.Equal(t, want.IsBase, got.IsBase, "B itself")
			assert.ElementsMatch(t, want.Foreign, got.Foreign, "another build's work")
			for _, marker := range in.markers {
				assert.NoFileExists(t, marker, "no program the repository configures ran")
			}
		})
	}
}

// checkBuild makes the CheckBuildCommit call in for build 2 of project.
func checkBuild(t *testing.T, project string, in checkInput) (CommitCheck, error) {
	t.Helper()
	folder := in.folder
	if folder == "" {
		folder = project
	}
	own := [2]string{BuilderRole.Branch(openBuild), TestsRole.Branch(openBuild)}
	return CheckBuildCommit(t.Context(), folder, in.commit, in.role.Branch(openBuild), in.base, own, in.recorded)
}

// objectFile returns the file that holds object id loose in the repository of
// project, which the tests' repositories never pack.
func objectFile(t *testing.T, project, id string) string {
	t.Helper()
	objects := runGit(t, project, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	return filepath.Join(objects, id[:2], id[2:])
}

// commonDir returns the repository's common folder, which every working copy
// of it shares.
func commonDir(t *testing.T, project string) string {
	t.Helper()
	return runGit(t, project, "rev-parse", "--path-format=absolute", "--git-common-dir")
}

// probeProgram writes, in a new folder, a shell script that records in a
// marker file that it ran, and returns the script and the marker.
func probeProgram(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	script, marker := filepath.Join(dir, "probe.sh"), filepath.Join(dir, "ran")
	text := "#!/bin/sh\necho ran >> '" + marker + "'\ncat \"$1\" 2>/dev/null\n"
	require.NoError(t, os.WriteFile(script, []byte(text), 0o700))
	return script, marker
}

// newBuildRepo makes a project repository as the probes' mkrepo does: B0, its
// first commit, with svc/a.go, design/d.go and eval/e.go, and both working
// copies at B0, .surla-build on surla/build-1 and .surla-tests on
// surla/tests-1, which the repository's exclude file keeps out of the
// project's status. It returns the project folder and B0's object name.
func newBuildRepo(t *testing.T) (string, string) {
	t.Helper()
	project, _ := newProject(t, map[string]string{
		"svc/a.go":    "package svc\n",
		"design/d.go": "package design // v0\n",
		"eval/e.go":   "package eval\n",
	})
	exclude := runGit(t, project, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	require.NoError(t, os.MkdirAll(filepath.Dir(exclude), 0o750))
	require.NoError(t, os.WriteFile(exclude, []byte(".surla-build/\n.surla-tests/\n"), 0o600))
	for _, role := range []Role{BuilderRole, TestsRole} {
		runGit(t, project, "worktree", "add", "--quiet", "-b", role.Branch(1), role.Folder)
	}
	return project, revision(t, project, "HEAD")
}

// commitBuild1 commits build 1's work: C1 adds svc/refund.go on
// surla/build-1, and also edits svc/a.go when editA is set, as goalprobe's C1
// does; T1, the tests Surla accepted, adds eval/refund.go on surla/tests-1.
func commitBuild1(t *testing.T, project string, editA bool) (string, string) {
	t.Helper()
	files := map[string]string{"svc/refund.go": "package svc // refund\n"}
	if editA {
		files["svc/a.go"] = "package svc\n\n// refund edit\n"
	}
	c1 := commitAll(t, filepath.Join(project, BuilderRole.Folder), "C1", files)
	t1 := commitAll(t, filepath.Join(project, TestsRole.Folder), "T1",
		map[string]string{"eval/refund.go": "package eval // refund\n"})
	return c1, t1
}

// joinBuild puts the copy of role on its branch for build 2, made at base, as
// a worker's first read does: what the copy left uncommitted is committed on
// the branch it is on, then git switch -c makes its branch. It returns that
// leftover commit, or "" when the copy was clean.
func joinBuild(t *testing.T, project string, role Role, base string) string {
	t.Helper()
	dir := filepath.Join(project, role.Folder)
	var left string
	if runGit(t, dir, "status", "--porcelain") != "" {
		left = commitAll(t, dir, "Left uncommitted when change exports started", nil)
	}
	runGit(t, dir, "switch", "--quiet", "-c", role.Branch(openBuild), base)
	return left
}

// commitAll writes files in the working tree dir, commits everything there
// with message, and returns the commit's object name.
func commitAll(t *testing.T, dir, message string, files map[string]string) string {
	t.Helper()
	writeFiles(t, dir, files)
	runGit(t, dir, "add", "--all")
	runGit(t, dir, "commit", "--quiet", "-m", message)
	return revision(t, dir, "HEAD")
}

// revision returns the object name of the commit name resolves to in the
// repository of dir.
func revision(t *testing.T, dir, name string) string {
	t.Helper()
	return runGit(t, dir, "rev-parse", "--verify", name+"^{commit}")
}

// asCandidate is the call for a candidate of build 2, which must be on
// surla/build-2.
func asCandidate(commit, base string) checkInput {
	return checkInput{commit: commit, role: BuilderRole, base: base}
}

// asTestsTip is the call for the tip of build 2's tests branch, surla/tests-2.
func asTestsTip(commit, base string) checkInput {
	return checkInput{commit: commit, role: TestsRole, base: base}
}

// droppedBuild1 is what Drop recorded for build 1: the tips of its two
// branches, C1 and T1.
func droppedBuild1(c1, t1 string) []BranchTip {
	return []BranchTip{{Branch: "surla/build-1", Commit: c1}, {Branch: "surla/tests-1", Commit: t1}}
}

// work is the ForeignWork of mergeBase through branch that names all of its
// files, five at most.
func work(mergeBase, branch string, files ...string) ForeignWork {
	return ForeignWork{MergeBase: mergeBase, Branch: branch, Files: files, FileCount: len(files)}
}
