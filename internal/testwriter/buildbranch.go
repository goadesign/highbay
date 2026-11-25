// This file names the branches of Highbay's builds and reads, with git, the
// facts Highbay checks before it uses a commit of the open build.
//
// Each build Highbay saves has a number n and a base B, the project's commit
// when Build saved it. The builder's working copy works on highbay/build-<n>
// and the test writer's on highbay/tests-<n>, both started at B. These are
// build branches: the names Role.Branch gives, and highbay/tests, the one
// branch earlier versions of Highbay gave the test writer. Any other branch is
// an agent's, even one named under highbay/. Another build's commit is a commit
// that a build branch other than the open build's two reaches and B does not
// hold; a commit Highbay uses must be on its build's branch, descend from B and
// hold no such commit.
//
// The checks read the branches of the repository the copies share with the
// project, which any program of the user can move or delete, so they keep
// builds apart for agents that follow their instructions; they are not
// isolation. CheckBuildCommit states what they cannot see.
package testwriter

import (
	"context"
	"fmt"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"
)

type (
	// Role is one worker's working copy: its folder in the project and the
	// names of its build branches. BuilderRole and TestsRole are the roles.
	Role struct {
		// Folder is the copy's folder name inside the project folder.
		Folder string
		// Prefix begins the name of each of the role's build branches; the
		// build's number ends it.
		Prefix string
		// Legacy is the one branch earlier versions of Highbay gave the role, a
		// build branch numbered 0, or empty when there was none.
		Legacy string
	}

	// BranchTip is a build branch's commit as Highbay recorded it. Drop records
	// the dropped build's two tips, so CheckBuildCommit still finds that
	// build's commits after an agent moved or deleted its branches.
	BranchTip struct {
		// Branch is the branch's short name, such as highbay/build-1.
		Branch string
		// Commit is the full object name of the commit Branch pointed at.
		Commit string
	}

	// ForeignWork is another build's work that a checked commit holds: a
	// best common ancestor of the commit and another build's branch that the
	// build's base does not hold, and the files of the commits it reaches
	// beyond the base.
	ForeignWork struct {
		// MergeBase is the full object name of that common ancestor, a commit
		// of the other build.
		MergeBase string
		// Branch is the other build's branch, as git lists it or as a
		// recorded BranchTip names it.
		Branch string
		// Files are the first five, in byte order, of the paths that the
		// commits MergeBase reaches and the base does not changed: a moved
		// file is its old and its new path, and a merge commit counts the
		// paths where it differs from every parent. Each is slash-separated
		// from the repository's top folder and raw, as an agent chose it:
		// callers quote it, or withhold one that is not printable, before
		// showing it.
		Files []string
		// FileCount is how many such paths there are, at least len(Files).
		FileCount int
	}

	// CommitCheck is what CheckBuildCommit found for one commit of the open
	// build. Highbay uses the commit only when OnBranch and FromBase hold and
	// Foreign is empty, and uses a candidate only when IsBase does not hold.
	CommitCheck struct {
		// OnBranch reports that the build branch the commit must be on
		// exists and that the commit is its tip or an ancestor of that tip.
		OnBranch bool
		// FromBase reports that the commit is the build's base or descends
		// from it.
		FromBase bool
		// IsBase reports that the commit is the base itself: a candidate
		// then holds no work, while the tests branch may still be there.
		IsBase bool
		// Foreign lists the other builds' work the commit holds, nil when it
		// holds none.
		Foreign []ForeignWork
	}

	// highbayBranch is a branch under refs/heads/highbay/ and its commit, as one
	// listing showed them.
	highbayBranch struct {
		// name is the branch's short name, such as highbay/build-1.
		name string
		// tip is the full object name of the commit it points at.
		tip string
	}
)

const (
	// highbayBranchRefs holds every build branch, and any branch an agent
	// names under highbay/.
	highbayBranchRefs = "refs/heads/highbay/"
	// maxWorkFiles is the most paths one ForeignWork names; FileCount counts
	// them all.
	maxWorkFiles = 5
)

var (
	// BuilderRole is the builder's copy, .highbay-build, which works on
	// highbay/build-<n>.
	BuilderRole = Role{Folder: ".highbay-build", Prefix: "highbay/build-"}
	// TestsRole is the test writer's copy, .highbay-tests, which works on
	// highbay/tests-<n>, and worked on highbay/tests before builds were numbered.
	TestsRole = Role{Folder: ".highbay-tests", Prefix: "highbay/tests-", Legacy: "highbay/tests"}
)

// Branch returns the role's branch for build n, a build's number and so at
// least 1: Prefix followed by n in decimal, such as highbay/build-3.
func (r Role) Branch(n int) string {
	return r.Prefix + strconv.Itoa(n)
}

// ParseBuildBranch reports whether name, a branch's short name, is a build
// branch, and returns its role and number. A build branch is a name
// Role.Branch gives for a number of at least 1, so its digits carry no sign
// or leading zero and fit an int, or TestsRole.Legacy, numbered 0. Any other
// name is an agent's: highbay/build-2-backup, highbay/build-x, highbay/build-007,
// or a number too large for an int.
func ParseBuildBranch(name string) (Role, int, bool) {
	if name == TestsRole.Legacy {
		return TestsRole, 0, true
	}
	for _, role := range []Role{BuilderRole, TestsRole} {
		digits, ok := strings.CutPrefix(name, role.Prefix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(digits); err == nil && n >= 1 && role.Branch(n) == name {
			return role, n, true
		}
	}
	return Role{}, 0, false
}

// NextNumber returns the number Build gives a new build of the project in the
// folder project: one more than the larger of saved, the open build's number
// or 0 when no build is saved, and the largest number of a build branch that
// git for-each-ref lists in the project's repository. So no number is given
// twice: a build dropped before either copy made its branches is counted
// through saved, every other through its branches. A branch an agent named
// like a build branch counts too, which only skips numbers.
//
// It fails when saved is negative, which no build is numbered, when that
// larger number is the largest int, which no number follows, naming the
// branch or the open build it came from, and when git fails.
func NextNumber(ctx context.Context, project string, saved int) (int, error) {
	if saved < 0 {
		return 0, fmt.Errorf("the open build's number %d is negative", saved)
	}
	branches, err := listHighbayBranches(ctx, project)
	if err != nil {
		return 0, err
	}
	largest, source := saved, "the open build"
	for _, b := range branches {
		if _, n, ok := ParseBuildBranch(b.name); ok && n > largest {
			largest, source = n, "branch "+b.name
		}
	}
	if largest == math.MaxInt {
		return 0, fmt.Errorf("%s is numbered %d, the largest number a build can have, so no build can follow it",
			source, largest)
	}
	return largest + 1, nil
}

// CheckBuildCommit reads, with git in the repository of the project folder
// project, whether commit belongs to the open build. Its callers check a
// commit where Highbay uses it: the candidate verify_change builds and
// approve_merge approves, and the tip of the test writer's branch before
// Highbay commits accepted tests on it. base is the build's base B,
// buildBranches are the build's two branches, its Role.Branch of BuilderRole
// and of TestsRole, and ownBranch is the one of them commit must be on.
// recorded are branch tips the caller saved earlier, the ones Drop recorded
// for the dropped build: they are checked as another build's branches too,
// so moving or deleting a dropped build's branch hides none of its commits.
//
// commit and base are resolved once, and the branches read in one listing of
// refs/heads/highbay/, so every fact describes one state of the repository:
//
//   - OnBranch: git merge-base --is-ancestor commit <ownBranch's tip> exits 0;
//   - FromBase: git merge-base --is-ancestor base commit exits 0, and IsBase:
//     base is commit;
//   - Foreign: one entry for each commit m that git merge-base --all prints
//     for commit and another build's tip and that base does not hold (git
//     merge-base --is-ancestor m base exits 1), with the files workFiles lists
//     for m. Another build's tips are those of the build branches other than
//     the build's two, and the recorded tips that no such branch of the same
//     name reaches; a recorded tip whose commit the repository does not hold
//     (git rev-parse exits 1) is skipped, as no commit can hold it. git
//     merge-base exits 1, printing nothing, only when the two share no
//     commit: then nothing is found.
//
// All facts are read, whichever of them fail. Foreign lists the branches by
// name, then the recorded tips in their order, each with its merge bases in
// git's order. It fails when ownBranch is not one of buildBranches, when
// commit or base is not a commit of the repository, and when git fails in any
// other way than the exits named above, such as on a missing or corrupt
// object, a canceled ctx, or a list of files longer than one git command may
// print here (maxGitStdout): a failure is never a pass.
//
// What no check sees, so it counts as the agent's own work: files brought
// from another build without its commits (cherry-picked, squashed, or checked
// out from its branch); a commit only a moved or deleted branch reached,
// unless recorded holds that branch's tip; and history a shallow file in the
// repository cuts short, which git then treats as having no parents, so a
// commit of another build below the cut is not found. An info/grafts file,
// like a replace ref, is ignored.
func CheckBuildCommit(ctx context.Context, project, commit, ownBranch, base string,
	buildBranches [2]string, recorded []BranchTip) (CommitCheck, error) {
	if !slices.Contains(buildBranches[:], ownBranch) {
		return CommitCheck{}, fmt.Errorf("branch %s is not one of the build's branches %s and %s",
			ownBranch, buildBranches[0], buildBranches[1])
	}
	x, err := requireCommit(ctx, project, commit)
	if err != nil {
		return CommitCheck{}, err
	}
	b, err := requireCommit(ctx, project, base)
	if err != nil {
		return CommitCheck{}, err
	}
	branches, err := listHighbayBranches(ctx, project)
	if err != nil {
		return CommitCheck{}, err
	}
	check := CommitCheck{IsBase: x == b}
	if check.FromBase, err = isAncestor(ctx, project, b, x); err != nil {
		return CommitCheck{}, err
	}
	for _, br := range branches {
		if br.name != ownBranch {
			continue
		}
		if check.OnBranch, err = isAncestor(ctx, project, x, br.tip); err != nil {
			return CommitCheck{}, err
		}
	}
	others, err := otherBuilds(ctx, project, branches, buildBranches, recorded)
	if err != nil {
		return CommitCheck{}, err
	}
	for _, other := range others {
		found, err := foreignWork(ctx, project, x, b, other)
		if err != nil {
			return CommitCheck{}, err
		}
		check.Foreign = append(check.Foreign, found...)
	}
	return check, nil
}

// listHighbayBranches lists every branch under refs/heads/highbay/ in the
// project's repository with its commit, sorted by name as git for-each-ref
// sorts them. Git refuses spaces and line breaks in a branch's name, so each
// line it prints is an object name, a space and a name.
func listHighbayBranches(ctx context.Context, project string) ([]highbayBranch, error) {
	out, err := readGit(ctx, project,
		"for-each-ref", "--format=%(objectname) %(refname:lstrip=2)", highbayBranchRefs)
	if err != nil {
		return nil, fmt.Errorf("list the build branches: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	lines := strings.Split(out, "\n")
	branches := make([]highbayBranch, 0, len(lines))
	for _, line := range lines {
		tip, name, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("list the build branches: git printed %q, not an object name and a branch", line)
		}
		branches = append(branches, highbayBranch{name: name, tip: tip})
	}
	return branches, nil
}

// otherBuilds returns the tips CheckBuildCommit looks for another build's work
// through: every listed build branch but the build's own two, then every
// recorded tip whose commit the repository still holds and that no branch of
// its name among those already reaches, so a branch an agent moved or deleted
// is checked where Drop recorded it and an unmoved one only once.
func otherBuilds(ctx context.Context, project string, branches []highbayBranch, own [2]string,
	recorded []BranchTip) ([]BranchTip, error) {
	var others []BranchTip
	checked := map[string]string{}
	for _, br := range branches {
		if _, _, ok := ParseBuildBranch(br.name); !ok || slices.Contains(own[:], br.name) {
			continue
		}
		others = append(others, BranchTip{Branch: br.name, Commit: br.tip})
		checked[br.name] = br.tip
	}
	for _, rec := range recorded {
		id, found, err := lookupCommit(ctx, project, rec.Commit)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		if tip, ok := checked[rec.Branch]; ok {
			reached, err := isAncestor(ctx, project, id, tip)
			if err != nil {
				return nil, err
			}
			if reached {
				continue
			}
		}
		others = append(others, BranchTip{Branch: rec.Branch, Commit: id})
	}
	return others, nil
}

// foreignWork returns the other build's work commit x holds through that
// build's tip: each commit git merge-base --all prints for x and the tip that
// base b does not hold, with the files of its work.
func foreignWork(ctx context.Context, project, x, b string,
	other BranchTip) ([]ForeignWork, error) {
	out, err := readGit(ctx, project, "merge-base", "--all", x, other.Commit)
	if exitCode(err) == 1 {
		// git merge-base exits 1, printing nothing, only when x and the tip
		// share no commit, so x holds none of that build's.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find the commits %s shares with %s: %w", x, other.Branch, err)
	}
	var found []ForeignWork
	for _, m := range strings.Fields(out) {
		held, err := isAncestor(ctx, project, m, b)
		if err != nil {
			return nil, err
		}
		if held {
			continue
		}
		files, count, err := workFiles(ctx, project, m, b)
		if err != nil {
			return nil, err
		}
		found = append(found, ForeignWork{MergeBase: m, Branch: other.Branch, Files: files, FileCount: count})
	}
	return found, nil
}

// workFiles returns the first maxWorkFiles, in byte order, of the distinct
// paths that the commits m reaches and base b does not changed, and how many
// there are. git log lists each commit's paths against its parent, a root
// commit's as all its files, and a merge's where the merge differs from
// every parent (--diff-merges=dense-combined): a file only the merge's own
// resolution added or changed is named, by comparing trees, so no merge
// driver or textconv program the repository configures runs. A merge that
// keeps one parent's version of a file is not named for it; that file is
// named only through a commit of the work that changed it.
//
// These options pin each setting of the user's or the repository's that
// changes the list: diff.renames (--no-renames: a moved file is its old and
// new path), diff.relative (--no-relative: paths from the repository's top
// folder), log.showRoot (--root), diff.ignoreSubmodules
// (--ignore-submodules=none: a submodule's commit change is named),
// core.quotePath (-z: raw paths ended by NUL) and log.showSignature
// (--no-show-signature, which would run gpg and print its output among the
// paths). Other settings only order the paths, which are sorted here.
func workFiles(ctx context.Context, project, m, b string) ([]string, int, error) {
	out, err := readGit(ctx, project, "log", "--format=", "--name-only", "-z", "--no-renames",
		"--no-relative", "--root", "--ignore-submodules=none", "--no-show-signature",
		"--diff-merges=dense-combined", m, "--not", b)
	if err != nil {
		return nil, 0, fmt.Errorf("list the files of the work of %s: %w", m, err)
	}
	var paths []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	return slices.Clone(paths[:min(len(paths), maxWorkFiles)]), len(paths), nil
}

// requireCommit returns the full object name of commit, which the project's
// repository must hold.
func requireCommit(ctx context.Context, project, commit string) (string, error) {
	id, found, err := lookupCommit(ctx, project, commit)
	if err == nil && !found {
		err = fmt.Errorf("%s is not a commit git can read in the project's repository", commit)
	}
	return id, err
}

// lookupCommit returns the full object name of the commit name resolves to in
// the project's repository, and false when git finds no commit it can read
// there: git rev-parse --verify --quiet exits 1, printing nothing, when the
// object is missing or unreadable, is not a commit, or an abbreviation is
// ambiguous. A corrupt object makes it exit 128, which is an error.
// --end-of-options keeps a name from being read as an option.
func lookupCommit(ctx context.Context, project, name string) (string, bool, error) {
	id, err := readGit(ctx, project, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}")
	switch {
	case err == nil:
		return id, true, nil
	case exitCode(err) == 1:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("resolve commit %s: %w", name, err)
	}
}

// isAncestor reports whether commit a is commit b or an ancestor of it: git
// merge-base --is-ancestor exits 0 when it is and 1 when it is not.
func isAncestor(ctx context.Context, project, a, b string) (bool, error) {
	_, err := readGit(ctx, project, "merge-base", "--is-ancestor", a, b)
	switch {
	case err == nil:
		return true, nil
	case exitCode(err) == 1:
		return false, nil
	default:
		return false, err
	}
}

// readGit runs one of this file's read-only git queries in the project's
// repository through git, with two more of the repository's say turned off,
// as gitFlags turns off replace refs: an info/grafts file, which could make
// a commit's parents stand for others and hide another build's commit
// (GIT_GRAFT_FILE names an empty file instead), and a partial clone's
// promisor remote, which git would otherwise ask for a missing object
// (--no-lazy-fetch, git 2.45 or later), so a check never reaches the network.
func readGit(ctx context.Context, project string, args ...string) (string, error) {
	env := []string{"GIT_GRAFT_FILE=" + os.DevNull}
	return git(ctx, project, env, append([]string{"--no-lazy-fetch"}, args...)...)
}
