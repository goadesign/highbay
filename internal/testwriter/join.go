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
	"time"

	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/shquote"
)

type (
	// CopyFacts describes a working copy's state as Surla shows it in a join
	// refusal or a worker tool refusal: its branch or detached HEAD, any Git
	// operation in progress in its administrative folder, and the uncommitted
	// or untracked paths in its working tree.
	CopyFacts struct {
		// Branch is the checked-out branch's name when Detached is false,
		// formatted via CleanName (plain when printable, or "a name that is not
		// shown" when unprintable); empty when Detached is true.
		Branch string
		// Detached reports that the working copy's HEAD is not a symbolic ref.
		Detached bool
		// Operation is the Git operation in progress in the working copy's
		// administrative folder: "none", "merge", "rebase", "am",
		// "cherry_pick", or "revert". A bisect is "none".
		Operation string
		// IndexLock is the absolute path of index.lock in the working copy's
		// administrative folder when the index is locked, or empty otherwise.
		IndexLock string
		// Step is the recovery instruction Join populates when refusing a
		// working copy whose index.lock is present.
		Step string
		// Uncommitted lists up to the first five uncommitted or untracked
		// paths in byte order, relative to the copy's root, each formatted via
		// CleanName. It is an empty slice when UncommittedCount is 0.
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
		// writeTimeout overrides copyWriteTimeout when positive so tests can
		// exercise timeout-killed copy-changing git commands.
		writeTimeout time.Duration
	}

	// treeEntry is the destination mode and object ID of one path in git
	// diff-tree output.
	treeEntry struct {
		mode string
		oid  string
	}
)

const (
	// AlreadyJoined means the build's branch for this role already exists and
	// the copy is on it with no Git operation in progress.
	AlreadyJoined JoinOutcome = iota + 1
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

	// copyWriteTimeout bounds the non-cancellable context used for
	// copy-changing git commands so a caller cancellation mid-write does not
	// SIGKILL git and leave index.lock or a half-unpacked working tree behind.
	copyWriteTimeout = 2 * time.Minute

	// emptyBlobOID is the SHA-1 object ID of an empty Git blob, which appears
	// if a process is killed between creating a destination file and writing
	// its contents during an unpack.
	emptyBlobOID = "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"
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

// CleanName returns name unchanged when printable, or "a name that is not
// shown" when it contains a line break, control character, or invalid UTF-8.
func CleanName(name string) string {
	if !printableName(name) {
		return "a name that is not shown"
	}
	return name
}

// ShownName formats a branch or file name chosen by an agent for inclusion as a
// shell word in a refusal message: one POSIX shell word via shquote.Quote when
// printable, or "a name that is not shown" when it contains a line break,
// control character, or invalid UTF-8.
func ShownName(name string) string {
	if !printableName(name) {
		return "a name that is not shown"
	}
	return shquote.Quote(name)
}

// ShownBranch formats a checked-out branch name for prose in a refusal message
// or finding: raw when it is a Surla build branch, "a name that is not shown"
// when unprintable, and shell-quoted via shquote.Quote when it was chosen by an
// agent.
func ShownBranch(branch string) string {
	if !printableName(branch) || branch == "a name that is not shown" {
		return "a name that is not shown"
	}
	if _, _, ok := ParseBuildBranch(branch); ok {
		return branch
	}
	return shquote.Quote(branch)
}

// IndexLockStep formats the recovery instruction when a working copy's
// index.lock is present at lockPath and the caller will retry tool.
func IndexLockStep(tool, lockPath string) string {
	shown := lockPath
	if !printableName(lockPath) {
		shown = ShownName(lockPath)
	}
	return fmt.Sprintf(
		"Your working folder's git index is locked (index.lock): %s. If no git command is running in your working folder, delete %s, then call %s again.",
		shown,
		shown,
		tool,
	)
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
	baseCommit, err := w.requireCommit(ctx, base)
	if err != nil {
		return 0, CopyFacts{}, err
	}
	ownBranch := role.Branch(n)
	_, exists, err := w.lookupCommit(ctx, "refs/heads/"+ownBranch)
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
		if facts.IndexLock != "" {
			facts.Step = indexLockStep(role, facts.IndexLock)
		}
		return Refused, facts, nil
	}
	if facts.Operation != "none" {
		if facts.IndexLock != "" {
			facts.Step = indexLockStep(role, facts.IndexLock)
		}
		return Refused, facts, nil
	}
	parsedRole, k, isBuildBranch := ParseBuildBranch(rawBranch)
	onEarlier := !facts.Detached && isBuildBranch && parsedRole == role && k < n
	if !onEarlier {
		if facts.UncommittedCount > 0 {
			if facts.IndexLock != "" {
				facts.Step = indexLockStep(role, facts.IndexLock)
			}
			return Refused, facts, nil
		}
	}
	if facts.IndexLock != "" {
		facts.Step = indexLockStep(role, facts.IndexLock)
		return Refused, facts, nil
	}
	timeout := copyWriteTimeout
	if hooks.writeTimeout > 0 {
		timeout = hooks.writeTimeout
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if onEarlier {
		stopped, err := w.recoverStoppedSwitch(writeCtx, baseCommit)
		if err != nil {
			return w.handleJoinWriteError(writeCtx, role, err)
		}
		if !stopped {
			if role == TestsRole {
				if err := w.restoreTestsGenerated(writeCtx); err != nil {
					return w.handleJoinWriteError(writeCtx, role, err)
				}
			}
			if err := w.commitLeftoversOnEarlier(writeCtx, rawBranch, changeID, hooks); err != nil {
				return w.handleJoinWriteError(writeCtx, role, err)
			}
		}
	}
	if hooks.beforeSwitch != nil {
		if err := hooks.beforeSwitch(); err != nil {
			return 0, CopyFacts{}, err
		}
	}
	if _, err := w.git(writeCtx, w.dir, nil, "switch", "--quiet", "-c", ownBranch, baseCommit); err != nil {
		if writeCtx.Err() == nil && exitCode(err) == 1 {
			_, branchNowExists, lErr := w.lookupCommit(writeCtx, "refs/heads/"+ownBranch)
			afterFacts, fErr := w.Facts(writeCtx)
			if lErr == nil && !branchNowExists && fErr == nil && afterFacts.IndexLock == "" && afterFacts.UncommittedCount > 0 {
				return ChangedDuringJoin, afterFacts, nil
			}
		}
		return w.handleJoinWriteError(writeCtx, role, fmt.Errorf("switch the working copy to %s: %w", ownBranch, err))
	}
	joinedFacts, err := w.Facts(writeCtx)
	if err != nil {
		return 0, CopyFacts{}, err
	}
	return Joined, joinedFacts, nil
}

// indexLockStep formats the recovery instruction when a working copy's
// index.lock is present at lockPath.
func indexLockStep(role Role, lockPath string) string {
	tool := "get_builder_assignment"
	if role == TestsRole {
		tool = "get_test_writer_assignment"
	}
	return IndexLockStep(tool, lockPath)
}

// handleJoinWriteError handles an error from a copy-changing git command in
// join: if writeCtx did not time out and index.lock is present (held by
// another process that acquired the lock during join), it returns Refused with
// the lock's recovery step. Surla never deletes index.lock; if a timeout kills
// a copy-changing git command while it holds index.lock, the next Join refuses
// with the lock's recovery step.
func (w WorkingCopy) handleJoinWriteError(writeCtx context.Context, role Role,
	err error) (JoinOutcome, CopyFacts, error) {
	if writeCtx.Err() != nil {
		return 0, CopyFacts{}, err
	}
	lockPath := filepath.Join(w.gitDir, "index.lock")
	if _, lErr := os.Lstat(lockPath); lErr == nil {
		_, afterFacts, fErr := w.readFacts(writeCtx)
		if fErr == nil {
			afterFacts.IndexLock = lockPath
			afterFacts.Step = indexLockStep(role, lockPath)
			return Refused, afterFacts, nil
		}
	}
	return 0, CopyFacts{}, err
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
		facts.Branch = CleanName(rawBranch)
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
	lockPath := filepath.Join(w.gitDir, "index.lock")
	if _, err := os.Lstat(lockPath); err == nil {
		facts.IndexLock = lockPath
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", CopyFacts{}, fmt.Errorf("inspect index.lock in the working copy's git folder: %w", err)
	}
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
// paths in byte order, each formatted via CleanName, and the total count of
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
		shown = append(shown, CleanName(p))
	}
	return shown, len(paths), nil
}

// recoverStoppedSwitch detects whether an earlier Join on w stopped inside
// git switch -c while unpacking baseCommit's tree into the working copy
// (before moving HEAD off the earlier build branch). It runs before
// restoreTestsGenerated so differing generated files in baseCommit do not
// obscure the stopped switch, and it recognizes both a completed unpack and a
// partial unpack interrupted mid-flight. When a stopped switch is detected, it
// finishes resetting the copy's index and working tree to baseCommit so git
// switch -c can complete without committing baseCommit's files onto the
// earlier build branch.
func (w WorkingCopy) recoverStoppedSwitch(ctx context.Context, baseCommit string) (bool, error) {
	headTree, err := w.git(ctx, w.dir, nil, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return false, err
	}
	baseTree, err := w.git(ctx, w.dir, nil, "rev-parse", "--verify", baseCommit+"^{tree}")
	if err != nil {
		return false, err
	}
	if headTree == baseTree {
		return false, nil
	}
	tree, err := w.snapshotTree(ctx, nil)
	if err != nil {
		return false, err
	}
	if tree == headTree {
		return false, nil
	}
	stopped, err := w.isStoppedSwitch(ctx, headTree, baseTree, tree)
	if err != nil || !stopped {
		return false, err
	}
	if _, err := w.git(ctx, w.dir, nil, "read-tree", "-u", "--reset", baseCommit); err != nil {
		return false, fmt.Errorf("complete the interrupted switch in the working copy: %w", err)
	}
	return true, nil
}

// isStoppedSwitch reports whether tree (which differs from headTree) is a full
// or partial unpack of baseTree on top of headTree: every path that differs
// between headTree and tree also differs between headTree and baseTree and has
// baseTree's target mode and blob OID (allowing at most one empty-blob file
// interrupted between open and write), with at least one path matching
// baseTree. An agent's uncommitted edit that empties a file baseTree also
// changes, alongside another path matching baseTree, is indistinguishable from
// a file interrupted between open and write and is not committed as a
// leftover; any non-empty edit to a file baseTree changes is preserved and
// committed as a leftover.
func (w WorkingCopy) isStoppedSwitch(ctx context.Context, headTree, baseTree, tree string) (bool, error) {
	if tree == baseTree {
		return true, nil
	}
	baseOut, err := w.git(ctx, w.dir, nil, "diff-tree", "-r", "-z", "--no-renames", headTree, baseTree)
	if err != nil {
		return false, err
	}
	workOut, err := w.git(ctx, w.dir, nil, "diff-tree", "-r", "-z", "--no-renames", headTree, tree)
	if err != nil {
		return false, err
	}
	baseTargets, err := parseDiffTreeTargets(baseOut)
	if err != nil {
		return false, err
	}
	workTargets, err := parseDiffTreeTargets(workOut)
	if err != nil {
		return false, err
	}
	if len(workTargets) == 0 {
		return false, nil
	}
	matched := 0
	empty := 0
	for path, we := range workTargets {
		be, ok := baseTargets[path]
		if !ok {
			return false, nil
		}
		switch {
		case we == be:
			matched++
		case be.mode != "000000" && we.mode == be.mode && we.oid == emptyBlobOID:
			empty++
			if empty > 1 {
				return false, nil
			}
		default:
			return false, nil
		}
	}
	return matched > 0, nil
}

// parseDiffTreeTargets parses the NUL-delimited output of git diff-tree -r -z
// --no-renames into a map from slash-separated path to destination treeEntry.
func parseDiffTreeTargets(out string) (map[string]treeEntry, error) {
	if out == "" {
		return nil, nil
	}
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("parse diff-tree output: expected header and path pairs, got %d fields", len(fields))
	}
	targets := make(map[string]treeEntry, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		parts := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(parts) < 5 {
			return nil, fmt.Errorf("parse diff-tree header %q", fields[i])
		}
		targets[fields[i+1]] = treeEntry{mode: parts[1], oid: parts[3]}
	}
	return targets, nil
}

// restoreTestsGenerated restores and cleans Surla's generated paths in the
// test writer's working copy (eval/gen, eval/AGENTS_QUICKSTART.md, and
// eval/design/story_tests.go) so generated files and unmerged entries on them
// never enter a leftover commit. It never follows a symbolic link in a leading
// path component out of the working copy.
func (w WorkingCopy) restoreTestsGenerated(ctx context.Context) error {
	relProject, err := filepath.Rel(w.dir, w.project)
	if err != nil {
		return err
	}
	for _, sub := range testsGeneratedPaths {
		p := filepath.ToSlash(filepath.Join(relProject, sub))
		allExist, hasNonDir, err := realParentDirs(w.dir, p)
		if err != nil {
			return err
		}
		out, err := w.git(ctx, w.dir, nil, "ls-tree", "HEAD", "--", p)
		if err != nil {
			return fmt.Errorf("inspect %s in HEAD of the test writer's working copy: %w", p, err)
		}
		if _, err := w.git(ctx, w.dir, nil, "reset", "--quiet", "HEAD", "--", p); err != nil {
			return fmt.Errorf("reset %s in the test writer's working copy: %w", p, err)
		}
		if hasNonDir {
			// A leading component of p (such as eval or eval/design) is a
			// symbolic link or regular file, not a real folder: never follow
			// links out of the copy.
			continue
		}
		if allExist {
			target := filepath.Join(w.dir, filepath.FromSlash(p))
			if err := os.RemoveAll(target); err != nil {
				return fmt.Errorf("remove %s in the test writer's working copy: %w", p, err)
			}
		}
		if out != "" {
			if _, err := w.git(ctx, w.dir, nil, "restore", "--source=HEAD", "--staged", "--worktree", "--", p); err != nil {
				return fmt.Errorf("restore %s in the test writer's working copy: %w", p, err)
			}
		}
	}
	return nil
}

// realParentDirs inspects every parent component of slashPath under root using
// os.Lstat (without following symbolic links). It reports whether all parent
// components currently exist as real directories (allExist) and whether any
// existing parent component is not a directory, such as a symbolic link or
// regular file (hasNonDir).
func realParentDirs(root, slashPath string) (allExist, hasNonDir bool, err error) {
	cur := root
	parts := strings.Split(slashPath, "/")
	allExist = true
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		info, lErr := os.Lstat(cur)
		if errors.Is(lErr, fs.ErrNotExist) {
			allExist = false
			continue
		}
		if lErr != nil {
			return false, false, fmt.Errorf("inspect %s in the test writer's working copy: %w", cur, lErr)
		}
		if !info.IsDir() {
			return false, true, nil
		}
	}
	return allExist, false, nil
}

// commitLeftoversOnEarlier snapshots any uncommitted or untracked files of w in
// a private index and commits them on rawBranch before Join switches the copy
// to the new build's branch.
func (w WorkingCopy) commitLeftoversOnEarlier(ctx context.Context,
	rawBranch, changeID string, hooks joinHooks) error {
	headTree, err := w.git(ctx, w.dir, nil, "rev-parse", "--verify", "HEAD^{tree}")
	if err != nil {
		return err
	}
	indexTree, indexErr := w.git(ctx, w.dir, nil, "write-tree")
	if indexErr != nil && exitCode(indexErr) < 0 {
		return indexErr
	}
	tree, err := w.snapshotTree(ctx, hooks.afterPrivateIndex)
	if err != nil {
		return err
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

// snapshotTree records w's working tree (as git add --all on top of HEAD would
// stage it) in a temporary private index inside w.gitDir and returns the
// resulting tree object ID.
func (w WorkingCopy) snapshotTree(ctx context.Context, afterPrivateIndex func() error) (tree string, err error) {
	tmp, err := os.MkdirTemp(w.gitDir, "surla-join-*")
	if err != nil {
		return "", fmt.Errorf("create a temporary folder for the join snapshot: %w", err)
	}
	defer func() {
		err = errors.Join(err, os.RemoveAll(tmp))
	}()
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	if _, err := w.git(ctx, w.dir, env, "read-tree", "HEAD"); err != nil {
		return "", fmt.Errorf("snapshot the working copy: %w", err)
	}
	if _, err := w.git(ctx, w.dir, env, "add", "--all"); err != nil {
		return "", fmt.Errorf("snapshot the working copy: %w", err)
	}
	tree, err = w.git(ctx, w.dir, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("snapshot the working copy: %w", err)
	}
	if afterPrivateIndex != nil {
		if err := afterPrivateIndex(); err != nil {
			return "", err
		}
	}
	return tree, nil
}

// requireCommit returns the full object name of commit in w's repository,
// reading through w.gitDir so the copy's own .git is never read.
func (w WorkingCopy) requireCommit(ctx context.Context, commit string) (string, error) {
	id, found, err := w.lookupCommit(ctx, commit)
	if err == nil && !found {
		err = fmt.Errorf("%s is not a commit git can read in the project's repository", commit)
	}
	return id, err
}

// lookupCommit returns the full object name of the commit name resolves to in
// w's repository, reading through w.gitDir so the copy's own .git is never
// read, and false when git finds no commit it can read there.
func (w WorkingCopy) lookupCommit(ctx context.Context, name string) (string, bool, error) {
	id, err := w.git(ctx, w.dir, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", name+"^{commit}")
	switch {
	case err == nil:
		return id, true, nil
	case exitCode(err) == 1:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("resolve commit %s: %w", name, err)
	}
}
