// This file runs git for the working copy: the commands that read and write
// it, the private snapshot Check builds from, and the commit Record makes.
// Every command on the copy goes through WorkingCopy.git, which names the
// administrative folder the project's repository keeps for the copy.
package testwriter

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/goadesign/surla/internal/gitenv"
)

type (
	// capBuffer keeps the first limit bytes written to it and counts the
	// rest, never failing a write, so a command's output is drained to the
	// end.
	capBuffer struct {
		// buf holds the kept bytes.
		buf bytes.Buffer
		// limit is the most bytes buf keeps.
		limit int
		// dropped counts the bytes written past limit.
		dropped int
	}

	// changedPath is one path a working copy's snapshot adds, changes or
	// deletes compared with the copy's base.
	changedPath struct {
		// path is relative to the copy's root, slash-separated.
		path string
		// added reports that the base has no file at path, so undoing the
		// change means deleting it; otherwise the base's file is restored.
		added bool
	}

	// IndexLockError reports that updating the working copy's index failed
	// because Git could not create index.lock at Path.
	IndexLockError struct {
		// Path is the absolute path of index.lock in the working copy's git folder.
		Path string
		// Err is the underlying git read-tree failure.
		Err error
	}
)

const (
	// maxGitStdout bounds the standard output git may print for one command;
	// every command here prints at most a few paths or object IDs.
	maxGitStdout = 1 << 20
	// maxGitStderr bounds the standard error kept for an error message.
	maxGitStderr = 4 << 10
)

// gitFlags prevent optional read-side index refreshes and turn off what git
// would otherwise run on the repository's or the
// working copy's say: hooks (a repository's core.hooksPath may name a folder
// of the working copy, which its author writes) and the file system monitor.
// They also make git ignore replace refs, which the copy's author can add to
// the repository the copy shares with the project: with them, a commit could
// stand for another one, and a base compared with the copy could show the
// author's own changes as already there.
var gitFlags = []string{"--no-optional-locks", "--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false"}

// Error returns the underlying failure's text.
func (e *IndexLockError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the underlying failure.
func (e *IndexLockError) Unwrap() error {
	return e.Err
}

// Write keeps what fits in the limit and counts the rest.
func (c *capBuffer) Write(p []byte) (int, error) {
	keep := min(len(p), max(c.limit-c.buf.Len(), 0))
	c.buf.Write(p[:keep])
	c.dropped += len(p) - keep
	return len(p), nil
}

// git runs one git command from dir with gitFlags and gitEnv plus env, with no
// standard input, and returns its standard output without the final newline.
// A failed command's error wraps its *exec.ExitError and quotes the start of
// its standard error.
func git(ctx context.Context, dir string, env []string, args ...string) (string, error) {
	full := append(append([]string{"-C", dir}, gitFlags...), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(gitEnv(), env...)
	stdout := &capBuffer{limit: maxGitStdout}
	stderr := &capBuffer{limit: maxGitStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.buf.String()))
	}
	if stdout.dropped > 0 {
		return "", fmt.Errorf("git %s printed more than %d bytes", strings.Join(args, " "), maxGitStdout)
	}
	return strings.TrimSuffix(stdout.buf.String(), "\n"), nil
}

// stageCopy records the working copy's files, as `git add --all` would commit
// them on top of its HEAD, in the private index file index, and writes those
// files under dst, which must not exist. The copy's own index and files are
// only read.
func stageCopy(ctx context.Context, wc WorkingCopy, index, dst string) error {
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := wc.git(ctx, wc.dir, env, "read-tree", "HEAD"); err != nil {
		return fmt.Errorf("snapshot the working copy: %w", err)
	}
	if _, err := wc.git(ctx, wc.dir, env, "add", "--all"); err != nil {
		return fmt.Errorf("snapshot the working copy: %w", err)
	}
	prefix := "--prefix=" + dst + string(filepath.Separator)
	if _, err := wc.git(ctx, wc.dir, env, "checkout-index", "--all", prefix); err != nil {
		return fmt.Errorf("write the working copy's snapshot: %w", err)
	}
	return nil
}

// changedSince returns the paths, relative to the copy's root and in git's
// order, that the snapshot recorded in the private index file index adds,
// changes or deletes compared with the commit base, each with whether the
// base lacks it. Renames are not detected, so a moved file is one path
// deleted and one added.
func changedSince(ctx context.Context, wc WorkingCopy, index, base string) ([]changedPath, error) {
	tree, err := wc.git(ctx, wc.dir, []string{"GIT_INDEX_FILE=" + index}, "write-tree")
	if err != nil {
		return nil, fmt.Errorf("record the working copy's files: %w", err)
	}
	out, err := wc.git(ctx, wc.dir, nil, "diff-tree", "-r", "-z", "--name-status", "--no-renames", base, tree)
	if err != nil {
		return nil, fmt.Errorf("compare the working copy with its base: %w", err)
	}
	if out == "" {
		return nil, nil
	}
	// Each change is a status letter and a path, both ended by a NUL.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	if len(fields)%2 != 0 {
		return nil, fmt.Errorf("compare the working copy with its base: git printed %d fields, not status and path pairs", len(fields))
	}
	paths := make([]changedPath, 0, len(fields)/2)
	for i := 0; i < len(fields); i += 2 {
		paths = append(paths, changedPath{path: fields[i+1], added: fields[i] == "A"})
	}
	return paths, nil
}

// writeTree records every file under src, the snapshot stageCopy wrote and
// the check then changed and compiled, in the private index, and writes that
// index as a tree into the working copy's repository. It returns the tree's
// ID. Ignore rules do not apply here: a file the author's ignore file names,
// such as the regenerated gen/ folder, was compiled, so it is recorded too.
// (Files those rules kept out of the copy's snapshot never reached src.)
func writeTree(ctx context.Context, wc WorkingCopy, index, src string) (string, error) {
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := wc.git(ctx, src, env, "add", "--all", "--force"); err != nil {
		return "", fmt.Errorf("record the checked files: %w", err)
	}
	tree, err := wc.git(ctx, src, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("record the checked files: %w", err)
	}
	return tree, nil
}

// commitTree records an accepted tree only on the exact tip the caller
// checked. Its index reservation spans the expected-tip reference CAS; the
// real index is published only after that CAS succeeds. Join retains its
// separate commitTreeWithHook ordering and crash-recovery boundary below.
func commitTree(
	ctx context.Context, wc WorkingCopy, ref, tree, checkedTip, message string,
) (commit string, err error) {
	branch := strings.TrimPrefix(ref, "refs/heads/")
	head, err := wc.git(ctx, wc.dir, nil, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if head != checkedTip {
		return "", &AuthorError{Finding: Finding{Kind: FindingWorkingCopy, Message: fmt.Sprintf(
			"The tests branch %s moved from checked commit %s to %s before recording. Put your own tests on %s, uncommitted (Surla commits what it accepts), and report again.",
			ShownBranch(branch), checkedTip, head, ShownBranch(branch))}}
	}
	headTree, err := wc.git(ctx, wc.dir, nil, "rev-parse", "--verify", checkedTip+"^{tree}")
	if err != nil {
		return "", err
	}
	if headTree == tree {
		return checkedTip, nil
	}
	created, err := wc.git(ctx, wc.dir, nil, "commit-tree", "--no-gpg-sign", tree, "-p", checkedTip, "-m", message)
	if err != nil {
		return "", fmt.Errorf("commit the working copy's changes: %w", err)
	}

	// Reserve the ordinary Git index lock while preparing a replacement
	// privately. A rejected reference CAS must never alter the real index.
	lockPath := filepath.Join(wc.gitDir, "index.lock")
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		wrapped := fmt.Errorf("lock the working copy's index: %w", err)
		if errors.Is(err, os.ErrExist) {
			return "", &IndexLockError{Path: lockPath, Err: wrapped}
		}
		return "", wrapped
	}
	defer func() {
		// Preserve a lock another actor replaced while this call ran.
		owned, ownedErr := lock.Stat()
		current, currentErr := os.Lstat(lockPath)
		var removeErr error
		switch {
		case errors.Is(currentErr, os.ErrNotExist):
		case ownedErr != nil:
			removeErr = ownedErr
		case currentErr != nil:
			removeErr = currentErr
		case os.SameFile(owned, current):
			removeErr = os.Remove(lockPath)
		default:
			removeErr = errors.New("the working copy's index lock was replaced; it was left intact")
		}
		err = errors.Join(err, lock.Close(), removeErr)
		if commit != "" && err != nil {
			err = fmt.Errorf("recorded tests as commit %s on %s, but could not finish updating the working copy's index: %w", commit, ShownBranch(branch), err)
		}
	}()
	tmp, err := os.MkdirTemp(wc.gitDir, "surla-record-*")
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(tmp)) }()
	index := filepath.Join(tmp, "index")
	if _, err := wc.git(ctx, wc.dir, []string{"GIT_INDEX_FILE=" + index}, "read-tree", created); err != nil {
		return "", fmt.Errorf("prepare the working copy's index: %w", err)
	}
	if _, err := wc.git(ctx, wc.dir, nil, "update-ref", ref, created, checkedTip); err != nil {
		return "", fmt.Errorf("move %s to the new commit: %w", branch, err)
	}
	commit = created
	if err := os.Rename(index, filepath.Join(wc.gitDir, "index")); err != nil {
		return commit, err
	}
	return commit, nil
}

// commitTreeWithHook commits tree onto ref (a full refs/heads/<branch> name)
// in wc, updating wc's index first when wc has ref checked out, calling
// afterCopyIndex if non-nil, and then updating ref with compare-and-swap on
// its previous head commit.
func commitTreeWithHook(ctx context.Context, wc WorkingCopy, ref, tree, message string,
	afterCopyIndex func() error) (string, error) {
	branch := strings.TrimPrefix(ref, "refs/heads/")
	head, err := wc.git(ctx, wc.dir, nil, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	headTree, err := wc.git(ctx, wc.dir, nil, "rev-parse", "--verify", head+"^{tree}")
	if err != nil {
		return "", err
	}
	if headTree == tree {
		return head, nil
	}
	commit, err := wc.git(ctx, wc.dir, nil, "commit-tree", "--no-gpg-sign", tree, "-p", head, "-m", message)
	if err != nil {
		return "", fmt.Errorf("commit the working copy's changes: %w", err)
	}
	checkedOut, err := wc.git(ctx, wc.dir, nil, "symbolic-ref", "--quiet", "HEAD")
	if err != nil && exitCode(err) != 1 {
		return "", err
	}
	if checkedOut == ref {
		if _, err := wc.git(ctx, wc.dir, nil, "read-tree", commit); err != nil {
			wrapped := fmt.Errorf("update the working copy's index: %w", err)
			if isIndexLockFailure(err) {
				return "", &IndexLockError{
					Path: filepath.Join(wc.gitDir, "index.lock"),
					Err:  wrapped,
				}
			}
			return "", wrapped
		}
	}
	if afterCopyIndex != nil {
		if err := afterCopyIndex(); err != nil {
			return "", err
		}
	}
	if _, err := wc.git(ctx, wc.dir, nil, "update-ref", ref, commit, head); err != nil {
		return "", fmt.Errorf("move %s to the new commit: %w", branch, err)
	}
	return commit, nil
}

// isIndexLockFailure reports whether err came from Git failing to create
// index.lock in its captured stderr (under LC_ALL=C).
func isIndexLockFailure(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "Unable to create '") && strings.Contains(msg, "/index.lock': File exists")
}

// gitEnv is the server's environment without the variables that would make
// git act on another repository, work tree, index or object store than the
// one named on its command line (gitenv.WithoutRedirects), with terminal
// prompts off, with grafts off (GIT_GRAFT_FILE=/dev/null) so an info/grafts
// file in the repository cannot rewrite commit parents, and with LC_ALL=C so
// Git's diagnostic messages are in English: the inherited GIT_TERMINAL_PROMPT,
// GIT_GRAFT_FILE, LC_ALL and LANGUAGE are dropped and replaced. The user's
// identity and configuration-file settings stay, though commitTree never signs.
func gitEnv() []string {
	return gitEnvFrom(os.Environ())
}

// gitEnvFrom builds gitEnv from environ.
func gitEnvFrom(environ []string) []string {
	return append(
		gitenv.WithoutRedirects(environ, "GIT_TERMINAL_PROMPT", "GIT_GRAFT_FILE", "LC_ALL", "LANGUAGE"),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_GRAFT_FILE=/dev/null",
		"LC_ALL=C",
	)
}

// git runs one git command on the working copy's repository, with worktree
// (the copy, or a snapshot of it) as the work tree: --git-dir names the
// administrative folder found from the project's side, so git never reads the
// copy's own .git, whose configuration the copy's author could write.
func (w WorkingCopy) git(ctx context.Context, worktree string, env []string, args ...string) (string, error) {
	repo := []string{"--git-dir=" + w.gitDir, "--work-tree=" + worktree}
	return git(ctx, worktree, env, append(repo, args...)...)
}
