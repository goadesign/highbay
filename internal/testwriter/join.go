// This file reads a working copy's facts (its checked-out branch or detached
// state, any Git operation in progress in its administrative folder, and its
// uncommitted paths) and joins a working copy to a build's own branch when its
// worker first reads the build's assignment.
package testwriter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/shquote"
)

type (
	// CopyFacts describes a working copy's state as Surla shows it in a join
	// refusal or a worker tool refusal: its branch or detached HEAD, any Git
	// operation in progress in its administrative folder, and the uncommitted
	// or untracked paths in its working tree.
	CopyFacts struct {
		// Branch is the checked-out branch's name when Detached is false, raw
		// for a Surla build branch and shell-quoted via ShownName (or "a name
		// that is not shown" when unprintable) for an agent's branch; empty
		// when Detached is true.
		Branch string
		// Detached reports that the working copy's HEAD is not a symbolic ref.
		Detached bool
		// Operation is the Git operation in progress in the working copy's
		// administrative folder: "none", "merge", "rebase", "am",
		// "cherry_pick", or "revert". A bisect is "none".
		Operation string
		// Uncommitted lists up to the first five uncommitted or untracked
		// paths in byte order, relative to the copy's root, each formatted via
		// ShownName. It is an empty slice when UncommittedCount is 0.
		Uncommitted []string
		// UncommittedCount is the total number of uncommitted or untracked
		// paths in the working copy.
		UncommittedCount int
	}

	// JoinOutcome is the result of WorkingCopy.Join.
	JoinOutcome int

	// joinHooks holds optional test seams for interrupting or modifying the
	// working copy at intermediate steps of WorkingCopy.join.
	joinHooks struct {
		// afterPrivateIndex runs after the private index has been populated
		// and written as a tree, before commitTree updates the copy's index.
		afterPrivateIndex func() error
		// afterCopyIndex runs after commitTree updates the copy's own index
		// with read-tree and before update-ref advances the branch ref.
		afterCopyIndex func() error
		// beforeSwitch runs after any leftover commit on the earlier build
		// branch and immediately before git switch -c creates the new build's
		// branch.
		beforeSwitch func() error
	}
)

const (
	// AlreadyJoined means the build's branch for this role already exists and
	// the copy is on it with no Git operation in progress.
	AlreadyJoined JoinOutcome = iota
	// Joined means the build's branch did not exist and Join created and
	// switched the copy onto it at the build's base.
	Joined
	// Refused means the copy could not join because a Git operation is in
	// progress, the copy is on a side branch or detached HEAD with uncommitted
	// work, or the build's branch already exists while the copy is elsewhere.
	Refused
	// ChangedDuringJoin means a file changed in the copy after the leftover
	// commit and before git switch -c completed, so git switch -c refused
	// without creating the build's branch.
	ChangedDuringJoin
)

// testsGeneratedPaths are the paths inside the test module (eval/) that Surla
// writes during test preparation and checking. Join restores and cleans them in
// the test writer's copy before committing uncommitted leftovers on an earlier
// build branch.
var testsGeneratedPaths = []string{
	"eval/gen",
	"eval/AGENTS_QUICKSTART.md",
	"eval/" + designeval.StoryTestsPath,
}

// ShownName formats a branch or file name chosen by an agent for inclusion in
// a refusal message: one POSIX shell word via shquote.Quote when printable, or
// "a name that is not shown" when it contains a line break, control character,
// or invalid UTF-8.
func ShownName(name string) string {
	if !printableName(name) {
		return "a name that is not shown"
	}
	return shquote.Quote(name)
}

// Facts reads the working copy's checked-out branch or detached state, any Git
// operation in progress in its administrative folder, and the first five
// uncommitted or untracked paths with their total count.
func (w WorkingCopy) Facts(ctx context.Context) (CopyFacts, error) {
	_, facts, err := w.readFacts(ctx)
	return facts, err
}

// Join joins the working copy to role's branch for build n at base when the
// worker first reads the build's assignment.
//
//   - When role.Branch(n) already exists in the repository: if the copy is on
//     that branch and no Git operation is in progress, Join returns
//     AlreadyJoined with the copy's current facts and changes nothing;
//     otherwise it returns Refused with the copy's current facts.
//   - When role.Branch(n) does not yet exist: if a Git operation is in
//     progress, Join returns Refused. If the copy is on an earlier build
//     branch of the same role (k < n, including legacy surla/tests for
//     TestsRole), Join first restores and cleans Surla's generated paths in
//     the tests copy, snapshots any remaining uncommitted or untracked work in
//     a private index, commits that tree on the earlier build branch with the
//     message "Left uncommitted when change <changeID> started" (updating the
//     copy's index before advancing the branch ref), and then switches the
//     copy to role.Branch(n) at base with git switch --quiet -c. If the copy
//     is on any other branch or detached HEAD, Join switches it to
//     role.Branch(n) at base when UncommittedCount == 0 and returns Refused
//     otherwise.
//   - If a file changes in the copy after the leftover commit and before git
//     switch -c completes so that git switch -c refuses without creating
//     role.Branch(n), Join returns ChangedDuringJoin with the updated facts.
func (w WorkingCopy) Join(ctx context.Context, role Role, n int,
	base, changeID string) (JoinOutcome, CopyFacts, error) {
	return w.join(ctx, role, n, base, changeID, joinHooks{})
}

// shownBranch formats a checked-out branch name for CopyFacts.Branch: raw when
// it is a Surla build branch, and ShownName(raw) when it was chosen by an
// agent.
func shownBranch(raw string) string {
	if _, _, ok := ParseBuildBranch(raw); ok {
		return raw
	}
	return ShownName(raw)
}

// join implements Join with optional test seams for interrupting or racing
// intermediate steps.
func (w WorkingCopy) join(ctx context.Context, role Role, n int, base, changeID string,
	hooks joinHooks) (JoinOutcome, CopyFacts, error) {
	if err := w.opened(); err != nil {
		return 0, CopyFacts{}, err
	}
	if n < 1 {
		return 0, CopyFacts{}, fmt.Errorf("build number %d must be at least 1", n)
	}
	baseCommit, err := requireCommit(ctx, w.project, base)
	if err != nil {
		return 0, CopyFacts{}, err
	}
	ownBranch := role.Branch(n)
	_, exists, err := lookupCommit(ctx, w.project, "refs/heads/"+ownBranch)
	if err != nil {
		return 0, CopyFacts{}, err
	}
	rawBranch, facts, err := w.readFacts(ctx)
	if err != nil {
		return 0, CopyFacts{}, err
	}
	if exists {
		if !facts.Detached && rawBranch == ownBranch && facts.Operation == "none" {
			return AlreadyJoined, facts, nil
		}
		return Refused, facts, nil
	}
	if facts.Operation != "none" {
		return Refused, facts, nil
	}
	parsedRole, k, isBuildBranch := ParseBuildBranch(rawBranch)
	onEarlier := !facts.Detached && isBuildBranch && parsedRole == role && k < n
	if !onEarlier {
		if facts.UncommittedCount > 0 {
			return Refused, facts, nil
		}
	} else {
		if role == TestsRole {
			if err := w.restoreTestsGenerated(ctx); err != nil {
				return 0, CopyFacts{}, err
			}
		}
		if err := w.commitLeftoversOnEarlier(ctx, rawBranch, baseCommit, changeID, hooks); err != nil {
			return 0, CopyFacts{}, err
		}
	}
	if hooks.beforeSwitch != nil {
		if err := hooks.beforeSwitch(); err != nil {
			return 0, CopyFacts{}, err
		}
	}
	if _, err := w.git(ctx, w.dir, nil, "switch", "--quiet", "-c", ownBranch, baseCommit); err != nil {
		if ctx.Err() == nil && exitCode(err) == 1 {
			_, branchNowExists, lErr := lookupCommit(ctx, w.project, "refs/heads/"+ownBranch)
			afterFacts, fErr := w.Facts(ctx)
			if lErr == nil && !branchNowExists && fErr == nil && afterFacts.UncommittedCount > 0 {
				return ChangedDuringJoin, afterFacts, nil
			}
		}
		return 0, CopyFacts{}, fmt.Errorf("switch the working copy to %s: %w", ownBranch, err)
	}
	joinedFacts, err := w.Facts(ctx)
	if err != nil {
		return 0, CopyFacts{}, err
	}
	return Joined, joinedFacts, nil
}

// readFacts returns the raw branch name (empty when detached) and the
// formatted CopyFacts of w.
func (w WorkingCopy) readFacts(ctx context.Context) (string, CopyFacts, error) {
	if err := w.opened(); err != nil {
		return "", CopyFacts{}, err
	}
	var rawBranch string
	var facts CopyFacts
	head, err := w.git(ctx, w.dir, nil, "symbolic-ref", "--quiet", "HEAD")
	switch {
	case err == nil:
		rawBranch = strings.TrimPrefix(head, "refs/heads/")
		facts.Branch = shownBranch(rawBranch)
	case exitCode(err) == 1:
		facts.Detached = true
	default:
		return "", CopyFacts{}, err
	}
	op, err := w.readOperation()
	if err != nil {
		return "", CopyFacts{}, err
	}
	facts.Operation = op
	uncommitted, count, err := w.readUncommitted(ctx)
	if err != nil {
		return "", CopyFacts{}, err
	}
	facts.Uncommitted = uncommitted
	facts.UncommittedCount = count
	return rawBranch, facts, nil
}

// readOperation inspects w.gitDir to report any Git operation in progress in
// the working copy. A bisect is reported as "none".
func (w WorkingCopy) readOperation() (string, error) {
	has := func(rel string) (bool, error) {
		_, err := os.Lstat(filepath.Join(w.gitDir, rel))
		if err == nil {
			return true, nil
		}
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect %s in the working copy's git folder: %w", rel, err)
	}
	if ok, err := has("rebase-apply"); err != nil || ok {
		if err != nil {
			return "", err
		}
		applying, err := has(filepath.Join("rebase-apply", "applying"))
		if err != nil {
			return "", err
		}
		if applying {
			return "am", nil
		}
		return "rebase", nil
	}
	if ok, err := has("rebase-merge"); err != nil || ok {
		return "rebase", err
	}
	if ok, err := has("CHERRY_PICK_HEAD"); err != nil || ok {
		return "cherry_pick", err
	}
	if ok, err := has("REVERT_HEAD"); err != nil || ok {
		return "revert", err
	}
	if ok, err := has("sequencer"); err != nil || ok {
		if err != nil {
			return "", err
		}
		todo, err := os.ReadFile(filepath.Join(w.gitDir, "sequencer", "todo"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read sequencer/todo in the working copy's git folder: %w", err)
		}
		for _, line := range strings.Split(string(todo), "\n") {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if strings.HasPrefix(trimmed, "revert ") || trimmed == "revert" {
				return "revert", nil
			}
			break
		}
		return "cherry_pick", nil
	}
	if ok, err := has("MERGE_HEAD"); err != nil || ok {
		return "merge", err
	}
	return "none", nil
}

// readUncommitted returns the first maxWorkFiles uncommitted or untracked
// paths in byte order, each formatted via ShownName, and the total count of
// uncommitted or untracked paths in w.
func (w WorkingCopy) readUncommitted(ctx context.Context) ([]string, int, error) {
	out, err := w.git(ctx, w.dir, nil, "status", "--porcelain", "-z", "--no-renames", "--untracked-files=all")
	if err != nil {
		return nil, 0, fmt.Errorf("read the working copy's status: %w", err)
	}
	if out == "" {
		return []string{}, 0, nil
	}
	var paths []string
	for _, entry := range strings.Split(out, "\x00") {
		if len(entry) > 3 {
			paths = append(paths, entry[3:])
		}
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	shown := make([]string, 0, min(len(paths), maxWorkFiles))
	for _, p := range paths[:min(len(paths), maxWorkFiles)] {
		shown = append(shown, ShownName(p))
	}
	return shown, len(paths), nil
}

// restoreTestsGenerated restores and cleans Surla's generated paths in the
// test writer's working copy (eval/gen, eval/AGENTS_QUICKSTART.md, and
// eval/design/story_tests.go) so generated files and unmerged entries on them
// never enter a leftover commit.
func (w WorkingCopy) restoreTestsGenerated(ctx context.Context) error {
	relProject, err := filepath.Rel(w.dir, w.project)
	if err != nil {
		return err
	}
	for _, sub := range testsGeneratedPaths {
		p := filepath.ToSlash(filepath.Join(relProject, sub))
		_, headErr := w.git(ctx, w.dir, nil, "cat-file", "-e", "HEAD:"+p)
		switch {
		case headErr == nil:
			if _, err := w.git(ctx, w.dir, nil, "reset", "--quiet", "HEAD", "--", p); err != nil {
				return fmt.Errorf("reset %s in the test writer's working copy: %w", p, err)
			}
			if _, err := w.git(ctx, w.dir, nil, "restore", "--source=HEAD", "--staged", "--worktree", "--", p); err != nil {
				return fmt.Errorf("restore %s in the test writer's working copy: %w", p, err)
			}
			if _, err := w.git(ctx, w.dir, nil, "clean", "-fdq", "--", p); err != nil {
				return fmt.Errorf("clean %s in the test writer's working copy: %w", p, err)
			}
		case exitCode(headErr) >= 0:
			if _, err := w.git(ctx, w.dir, nil, "reset", "--quiet", "HEAD", "--", p); err != nil {
				return fmt.Errorf("unstage %s in the test writer's working copy: %w", p, err)
			}
			if err := os.RemoveAll(filepath.Join(w.dir, filepath.FromSlash(p))); err != nil {
				return fmt.Errorf("remove %s in the test writer's working copy: %w", p, err)
			}
		default:
			return headErr
		}
	}
	return nil
}

// commitLeftoversOnEarlier snapshots any uncommitted or untracked files of w in
// a private index and commits them on rawBranch before Join switches the copy
// to the new build's branch. If the copy's index and working tree already match
// baseCommit's tree while HEAD's tree differs (from a previous Join that
// stopped inside git switch -c after unpacking baseCommit's tree), no commit is
// made.
func (w WorkingCopy) commitLeftoversOnEarlier(ctx context.Context,
	rawBranch, baseCommit, changeID string, hooks joinHooks) (err error) {
	headTree, err := w.git(ctx, w.dir, nil, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return err
	}
	baseTree, err := w.git(ctx, w.dir, nil, "rev-parse", "--verify", baseCommit+"^{tree}")
	if err != nil {
		return err
	}
	indexTree, indexErr := w.git(ctx, w.dir, nil, "write-tree")
	if indexErr != nil && exitCode(indexErr) < 0 {
		return indexErr
	}
	tmp, err := os.MkdirTemp("", "surla-join-*")
	if err != nil {
		return fmt.Errorf("create a temporary folder for the join snapshot: %w", err)
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(tmp))
	}()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := w.git(ctx, w.dir, env, "read-tree", "HEAD"); err != nil {
		return fmt.Errorf("snapshot the working copy: %w", err)
	}
	if _, err := w.git(ctx, w.dir, env, "add", "--all"); err != nil {
		return fmt.Errorf("snapshot the working copy: %w", err)
	}
	tree, err := w.git(ctx, w.dir, env, "write-tree")
	if err != nil {
		return fmt.Errorf("snapshot the working copy: %w", err)
	}
	if hooks.afterPrivateIndex != nil {
		if err := hooks.afterPrivateIndex(); err != nil {
			return err
		}
	}
	if indexErr == nil && indexTree == baseTree && tree == baseTree && headTree != baseTree {
		// An earlier Join stopped inside git switch -c after unpacking
		// baseCommit's tree into the copy's index and working tree and before
		// moving HEAD. Skip the leftover commit so rawBranch keeps its tip.
		return nil
	}
	if tree == headTree {
		if indexErr != nil || indexTree != headTree {
			// The working tree matches HEAD while the copy's index holds a
			// staged or unmerged entry; align the copy's index with HEAD so
			// git switch -c succeeds.
			if _, err := w.git(ctx, w.dir, nil, "read-tree", "HEAD"); err != nil {
				return fmt.Errorf("update the working copy's index: %w", err)
			}
		}
		return nil
	}
	message := "Left uncommitted when change " + changeID + " started"
	_, err = commitTreeWithHook(ctx, w, "refs/heads/"+rawBranch, tree, message, hooks.afterCopyIndex)
	return err
}
