// Package testwriter prepares, checks and records the work of the test
// writer, and opens, makes, inspects and joins the linked git working copies
// of both the builder (.surla-build) and the test writer (.surla-tests).
// Surla, not the agent, decides whether that work counts. MakeCopy creates a
// role's copy on its build branch, OpenCopy opens an existing copy on
// whatever branch or commit it holds, WorkingCopy.Facts reads its branch,
// operation and uncommitted paths, WorkingCopy.Join moves it onto a new
// build's branch after saving any uncommitted work left on an earlier build
// branch, Checker.Prepare writes the required tests into the test writer's
// copy from a Story, Checker.Check judges it, and Record commits an accepted
// result on the copy's branch, which the user merges.
//
// Nothing here trusts the copy's author. Its files are compiled and run only
// in the verify/candidate sandbox (macOS sandbox-exec or Linux Landlock and
// Seccomp-BPF): where the sandbox is unavailable, Prepare and Check fail with
// candidate.ErrIsolationUnavailable before any of the author's code runs. No
// file inside the working copy can make git run a
// program: git is always pointed at the copy's repository through the folder
// the project's repository keeps for it, never through the copy's own .git,
// and it runs with hooks and the file system monitor disabled. A program the
// project's or the user's own git configuration names, such as a filter
// driver, still runs, as it would for the user. Files are read and written in
// the copy only through real folders, never through a symbolic link.
package testwriter

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goadesign/surla/internal/abspath"
	"github.com/goadesign/surla/internal/shquote"
	"github.com/goadesign/surla/internal/verify"
)

type (
	// listedCopy is one entry of the project repository's list of working
	// copies (git worktree list), the project's own folder included.
	listedCopy struct {
		// path is the working copy's folder as git recorded it.
		path string
		// branch is the full name of the branch checked out there, such as
		// refs/heads/main, or empty when none is.
		branch string
		// locked is set when the registration is locked (git worktree lock),
		// so git worktree remove refuses it until it is unlocked.
		locked bool
	}

	// roleError wraps a sentinel error with a role-specific message so
	// errors.Is matches the sentinel while Error names the role's folder or
	// branch.
	roleError struct {
		sentinel error
		msg      string
	}

	// WorkingCopy is a worker's linked git working copy of one project.
	// Only OpenCopy and MakeCopy (and OpenWorkingCopy) create one, so its
	// folders are always a working copy Surla created or verified.
	WorkingCopy struct {
		// dir is the absolute, symlink-free root of the linked working copy: a
		// checkout of the whole repository at <project>/<role.Folder>.
		dir string
		// project is the project's folder inside dir: dir itself for a project
		// at the repository's top level, dir/services/shop for one at
		// services/shop.
		project string
		// gitDir is the administrative folder the project's repository keeps
		// for this linked working copy (<common git folder>/worktrees/<name>),
		// found from the project's side. Every git command on the copy names
		// it, so the copy's own .git, which its author can rewrite, is never
		// read.
		gitDir string
	}

	// RefreshResult reports the working copy's commit after a start tried to
	// bring it to the project's commit. Every state carries the actual
	// retained HEAD.
	RefreshResult struct {
		State  RefreshState
		Commit string
	}

	// RefreshState is the reason Refresh retained or advanced the working copy.
	RefreshState string
)

const (
	// RefreshAtProjectCommit means HEAD equals the project's commit, whether
	// the copy was already there or this start advanced it.
	RefreshAtProjectCommit RefreshState = "at_project_commit"
	// RefreshNotContained preserves tests not merged into the project's branch.
	RefreshNotContained RefreshState = "not_contained"
	// RefreshDirty preserves the test writer's uncommitted or untracked files.
	RefreshDirty RefreshState = "dirty"
	// Folder is the test writer's working copy folder name inside the project.
	// Its ".surla" prefix makes the project's own checks (check_project) skip
	// it, and the local exclude entry keeps it out of the project's git status.
	Folder = ".surla-tests"
	// Branch is the legacy branch name earlier versions of Surla checked out
	// in the test writer's working copy.
	Branch = "surla/tests"
)

var (
	// ErrIdentityMissing reports that Git cannot resolve the author or committer
	// identity Record and Join use, including the user's configuration and
	// environment.
	ErrIdentityMissing = errors.New("git cannot resolve the user's author and committer name and address: configure Git's name and address, then try again")
	// ErrNotRepository refuses a project folder that is in no git working
	// tree. Its message is the step to take.
	ErrNotRepository = errors.New("the project is not in a git repository: run git init in the project folder and commit its files, then try again")
	// ErrNoCommit refuses a project whose repository has no commit to start
	// the working copy's branch from. Its message is the step to take.
	ErrNoCommit = errors.New("the project's git repository has no commit yet: commit the project's files, then try again")
	// ErrFolderInUse refuses a project whose working-copy entry (.surla-build
	// or .surla-tests) is not the working copy Surla created. Its message is
	// the step to take.
	ErrFolderInUse = errors.New("the project's " + Folder + " entry is not Surla's working copy: move it out of the project, then try again")
	// ErrBranchInUse refuses to create a working copy while another working
	// copy of the repository holds its build branch: git checks a branch out
	// in one folder at a time. The error wrapping it names that folder and the
	// step to take, which touches only that registration; branchInUse states
	// the step for each case.
	ErrBranchInUse = errors.New("branch " + Branch + ", which Surla reserves for the test writer's working copy, " +
		"is held by another working copy")
)

// CheckIdentity reads the author and committer identity exactly as Git
// commit-tree does in Record and Join, with the user's configuration and
// identity environment and the same redirect filtering. It writes no file and
// creates no working copy. ErrIdentityMissing names the user's recovery step;
// cancellation and failures to start Git retain their original causes.
func CheckIdentity(ctx context.Context, projectDir string) error {
	for _, name := range []string{"GIT_AUTHOR_IDENT", "GIT_COMMITTER_IDENT"} {
		if _, err := git(ctx, projectDir, nil, "var", name); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if exitCode(err) >= 0 {
				return ErrIdentityMissing
			}
			return err
		}
	}
	return nil
}

// OpenCopy opens the role's existing linked working copy of the project in
// projectDir at <project>/<role.Folder>, on whatever branch or detached commit
// it holds. When <project>/<role.Folder> does not exist it returns false, nil
// without creating anything or writing info/exclude. A project in no git
// working tree is refused with ErrNotRepository, one whose repository has no
// commit with ErrNoCommit, and one whose <role.Folder> entry exists and is not
// a linked working copy of the project's repository with ErrFolderInUse.
func OpenCopy(ctx context.Context, projectDir string, role Role) (WorkingCopy, bool, error) {
	project, rel, err := resolveProject(ctx, projectDir)
	if err != nil {
		return WorkingCopy{}, false, err
	}
	dir := filepath.Join(project, role.Folder)
	wc := WorkingCopy{dir: dir, project: filepath.Join(dir, rel)}
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return WorkingCopy{}, false, nil
	case err != nil:
		return WorkingCopy{}, false, fmt.Errorf("inspect %s: %w", dir, err)
	case !info.IsDir():
		return WorkingCopy{}, false, folderInUse(role, dir+" is not a folder")
	}
	if wc.gitDir, err = registeredCopy(ctx, project, dir, role); err != nil {
		return WorkingCopy{}, false, err
	}
	return wc, true, nil
}

// MakeCopy returns the role's linked working copy of the project in
// projectDir at <project>/<role.Folder>, creating it on branch (started at
// base when branch does not yet exist) when the folder is missing. An existing
// registered copy is returned on whatever branch or commit it holds without
// moving it. Before returning or creating a copy it ensures both
// ".surla-build/" and ".surla-tests/" are in the repository's own
// info/exclude file, never a tracked file, so the project's git status stays
// clean.
//
// A project in no git working tree is refused with ErrNotRepository, one whose
// repository has no commit with ErrNoCommit, one whose <role.Folder> entry is
// not a linked working copy of the repository with ErrFolderInUse, and one
// where another working copy holds branch when the copy must be created with
// ErrBranchInUse; nothing is created in these cases.
func MakeCopy(ctx context.Context, projectDir string, role Role, branch, base string) (WorkingCopy, error) {
	project, rel, err := resolveProject(ctx, projectDir)
	if err != nil {
		return WorkingCopy{}, err
	}
	dir := filepath.Join(project, role.Folder)
	wc := WorkingCopy{dir: dir, project: filepath.Join(dir, rel)}
	info, err := os.Lstat(dir)
	switch {
	case err == nil && !info.IsDir():
		return WorkingCopy{}, folderInUse(role, dir+" is not a folder")
	case err == nil:
		if wc.gitDir, err = registeredCopy(ctx, project, dir, role); err != nil {
			return WorkingCopy{}, err
		}
		if err := excludeFolders(ctx, project); err != nil {
			return WorkingCopy{}, err
		}
		return wc, nil
	case !errors.Is(err, fs.ErrNotExist):
		return WorkingCopy{}, fmt.Errorf("inspect %s: %w", dir, err)
	}
	if _, err := requireCommit(ctx, project, base); err != nil {
		return WorkingCopy{}, err
	}
	listed, err := listCopies(ctx, project)
	if err != nil {
		return WorkingCopy{}, err
	}
	for _, c := range listed {
		if same, err := samePath(c.path, dir); c.branch != "refs/heads/"+branch || err == nil && same {
			// Another branch, or Surla's own copy, whose stale registration
			// addCopy drops.
			continue
		}
		return WorkingCopy{}, branchInUse(c, role, branch)
	}
	if err := excludeFolders(ctx, project); err != nil {
		return WorkingCopy{}, err
	}
	if err := addCopy(ctx, project, dir, role, branch, base, listed); err != nil {
		return WorkingCopy{}, err
	}
	if wc.gitDir, err = registeredCopy(ctx, project, dir, role); err != nil {
		return WorkingCopy{}, err
	}
	return wc, nil
}

// OpenWorkingCopy returns the test writer's working copy of the project in
// projectDir, creating it on Branch at HEAD when missing, and refusing an
// existing copy that does not have Branch checked out with ErrFolderInUse. It
// stays as a wrapper for existing callers until slice W1 moves them to
// OpenCopy and MakeCopy.
func OpenWorkingCopy(ctx context.Context, projectDir string) (WorkingCopy, error) {
	wc, ok, err := OpenCopy(ctx, projectDir, TestsRole)
	if err != nil {
		return WorkingCopy{}, err
	}
	if ok {
		head, err := wc.git(ctx, wc.dir, nil, "symbolic-ref", "--quiet", "HEAD")
		if err != nil || head != "refs/heads/"+Branch {
			return WorkingCopy{}, folderInUse(TestsRole, fmt.Sprintf("%s does not have branch %s checked out", wc.dir, Branch))
		}
		if err := excludeFolders(ctx, filepath.Dir(wc.dir)); err != nil {
			return WorkingCopy{}, err
		}
		return wc, nil
	}
	return MakeCopy(ctx, projectDir, TestsRole, Branch, "HEAD")
}

// Dir returns the absolute root of the working copy, a checkout of the whole
// repository. Project returns the folder the worker works in, which may be
// below this root when the project is in a repository subfolder.
func (w WorkingCopy) Dir() string {
	return w.dir
}

// Project returns the project's folder inside the working copy, where the
// project's modules, such as its evaluator module, are found.
func (w WorkingCopy) Project() string {
	return w.project
}

// Refresh brings a clean copy forward only when its tests branch is already
// contained in the project's current commit. Untracked files count as dirty.
// Only a build start calls it, under the project's working-copy turn. Git's
// fast-forward merge preserves/refuses later editor changes instead of resetting
// them; hooks and automatic stashing are disabled. Every copy command uses the
// trusted administrative directory, never the copy author's .git file.
func (w WorkingCopy) Refresh(ctx context.Context) (RefreshResult, error) {
	return w.refresh(ctx, nil)
}

// Error returns the role-specific refusal message.
func (e roleError) Error() string {
	return e.msg
}

// Unwrap returns the sentinel error for errors.Is matching.
func (e roleError) Unwrap() error {
	return e.sentinel
}

// copyName returns the prose name of the role's working copy in error messages.
func (r Role) copyName() string {
	if r == BuilderRole {
		return "the builder's working copy"
	}
	return "the test writer's working copy"
}

// resolveProject resolves projectDir to a physical path, verifies that it lies
// in a git working tree whose repository has at least one commit, and returns
// the resolved project path and its path relative to the enclosing worktree
// root.
func resolveProject(ctx context.Context, projectDir string) (string, string, error) {
	project, err := abspath.Eval(projectDir)
	if err != nil {
		return "", "", fmt.Errorf("resolve the project folder: %w", err)
	}
	top, err := verify.EnclosingWorktreeRoot(ctx, project)
	if err != nil {
		return "", "", fmt.Errorf("find the project's git repository: %w", err)
	}
	if top == "" {
		return "", "", ErrNotRepository
	}
	if _, err := git(ctx, project, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		if exitCode(err) == 1 {
			return "", "", ErrNoCommit
		}
		return "", "", err
	}
	rel, err := filepath.Rel(top, project)
	if err != nil || !filepath.IsLocal(rel) {
		return "", "", fmt.Errorf("project %s is not inside its git working tree %s", project, top)
	}
	return project, rel, nil
}

// folderInUse returns an error matching ErrFolderInUse that names role.Folder
// and detail.
func folderInUse(role Role, detail string) error {
	return roleError{
		sentinel: ErrFolderInUse,
		msg: fmt.Sprintf("the project's %s entry is not Surla's working copy: move it out of the project, then try again (%s)",
			role.Folder, detail),
	}
}

// refresh's seam models an editor writing after the clean observation. Git,
// rather than that earlier observation, owns safe checkout at the transition.
func (w WorkingCopy) refresh(ctx context.Context, beforeAdvance func()) (RefreshResult, error) {
	if err := w.opened(); err != nil {
		return RefreshResult{}, err
	}
	head, err := w.git(ctx, w.dir, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return RefreshResult{}, err
	}
	target, err := git(ctx, filepath.Dir(w.dir), nil, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return RefreshResult{}, err
	}
	if _, err := w.git(ctx, w.dir, nil, "merge-base", "--is-ancestor", head, target); err != nil {
		if exitCode(err) == 1 {
			return RefreshResult{State: RefreshNotContained, Commit: head}, nil
		}
		return RefreshResult{}, err
	}
	status, err := w.git(ctx, w.dir, nil, "status", "--porcelain", "-z", "--untracked-files=all")
	if err != nil {
		return RefreshResult{}, err
	}
	if status != "" {
		return RefreshResult{State: RefreshDirty, Commit: head}, nil
	}
	if head == target {
		return RefreshResult{State: RefreshAtProjectCommit, Commit: head}, nil
	}
	if beforeAdvance != nil {
		beforeAdvance()
	}
	if _, err := w.git(ctx, w.dir, nil, "merge", "--ff-only", "--no-edit", "--no-autostash", target); err != nil {
		// A late author edit may prevent the fast-forward. Confirm that
		// condition before calling it dirty; other Git failures stay failures.
		status, statusErr := w.git(ctx, w.dir, nil, "status", "--porcelain", "-z", "--untracked-files=all")
		if statusErr == nil && status != "" {
			head, headErr := w.git(ctx, w.dir, nil, "rev-parse", "--verify", "HEAD^{commit}")
			if headErr == nil {
				return RefreshResult{State: RefreshDirty, Commit: head}, nil
			}
		}
		return RefreshResult{}, fmt.Errorf("advance the test writer's working copy: %w", err)
	}
	return RefreshResult{State: RefreshAtProjectCommit, Commit: target}, nil
}

// excludeFolders adds BuilderRole's and TestsRole's folder patterns to the
// repository's own info/exclude file unless lines already hold them. The
// patterns are not anchored, so they also cover a project in a subfolder of
// its repository.
func excludeFolders(ctx context.Context, project string) error {
	path, err := git(ctx, project, nil, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("read the repository's exclude file: %w", err)
	}
	lines := strings.Split(string(raw), "\n")
	var missing []string
	for _, role := range []Role{BuilderRole, TestsRole} {
		pattern := role.Folder + "/"
		if !slices.Contains(lines, pattern) {
			missing = append(missing, pattern)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	entry := "# Surla's working copies.\n" + strings.Join(missing, "\n") + "\n"
	if len(raw) > 0 && !strings.HasSuffix(string(raw), "\n") {
		entry = "\n" + entry
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create the repository's info folder: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open the repository's exclude file: %w", err)
	}
	_, writeErr := f.WriteString(entry)
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return fmt.Errorf("add %s to the repository's exclude file: %w", strings.Join(missing, " and "), err)
	}
	return nil
}

// registeredCopy returns the administrative folder the project's repository
// keeps for a linked working copy at dir, and ErrFolderInUse when it keeps
// none. It reads only the project's repository: the folder under its common
// git folder's worktrees/ whose gitdir file names dir/.git. The copy's own
// .git is never read, and the copy may be on any branch or detached commit.
func registeredCopy(ctx context.Context, project, dir string, role Role) (string, error) {
	common, err := git(ctx, project, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	admins := filepath.Join(common, "worktrees")
	entries, err := os.ReadDir(admins)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("list the repository's linked working copies: %w", err)
	}
	var found []string
	for _, e := range entries {
		admin := filepath.Join(admins, e.Name())
		raw, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("read the repository's linked working copies: %w", err)
		}
		// git records the copy's .git path, relative to the administrative
		// folder when worktree.useRelativePaths is set.
		target := strings.TrimSpace(string(raw))
		if !filepath.IsAbs(target) {
			target = filepath.Join(admin, target)
		}
		if same, err := samePath(filepath.Dir(target), dir); err == nil && same {
			found = append(found, admin)
		}
	}
	switch len(found) {
	case 0:
		return "", folderInUse(role, dir+" is not a linked working copy of the project's repository")
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("the project's repository registers %s as %d linked working copies: %s",
			dir, len(found), strings.Join(found, ", "))
	}
}

// branchInUse returns the refusal for c, another working copy that holds
// branch, with the step that frees the branch for what is true of c:
//
//   - its folder is a working copy: switch that folder to another branch;
//   - its folder is gone: remove that one registration with git worktree
//     remove;
//   - its folder exists but is no longer a working copy (its .git entry is
//     gone), so it cannot be switched and git refuses to remove the
//     registration while the folder is there: move or delete the folder, then
//     remove the registration.
//
// A locked registration is unlocked first. The path is someone else's choice
// and the user runs these commands in a shell, so it is always written as
// one quoted shell word, and a path that holds a line break or any other
// character that is not printed is not shown at all: it could make the
// refusal read as other instructions.
func branchInUse(c listedCopy, role Role, branch string) error {
	prefix := fmt.Sprintf("branch %s, which Surla reserves for %s, is held by another working copy",
		branch, role.copyName())
	if !printableName(c.path) {
		return roleError{
			sentinel: ErrBranchInUse,
			msg: prefix + ", at a path that is not shown because it holds a line break or another character " +
				"that is not printed; find it with git worktree list in the project and free the branch there, " +
				"then try again",
		}
	}
	word := shquote.Quote(c.path)
	remove := "remove that registration with git worktree remove " + word
	if c.locked {
		remove = "unlock and remove that registration, which is locked, with git worktree unlock " + word +
			" and then git worktree remove " + word
	}
	if _, err := os.Lstat(c.path); errors.Is(err, fs.ErrNotExist) {
		return roleError{
			sentinel: ErrBranchInUse,
			msg: fmt.Sprintf("%s: git still registers %s, a folder that no longer exists, as holding it; "+
				"%s in the project, then try again", prefix, word, remove),
		}
	}
	if _, err := os.Lstat(filepath.Join(c.path, ".git")); errors.Is(err, fs.ErrNotExist) {
		return roleError{
			sentinel: ErrBranchInUse,
			msg: fmt.Sprintf("%s: git still registers %s as holding it, but that folder is no longer a working copy; "+
				"move or delete the folder, then %s in the project, then try again", prefix, word, remove),
		}
	}
	return roleError{
		sentinel: ErrBranchInUse,
		msg:      fmt.Sprintf("%s: %s has it checked out; switch that folder to another branch, then try again", prefix, word),
	}
}

// printableName reports whether path can be shown in a message: it is valid
// text, and every character in it is printed or is a plain space.
func printableName(path string) bool {
	return utf8.ValidString(path) && !strings.ContainsFunc(path, func(r rune) bool { return !unicode.IsPrint(r) })
}

// listCopies returns the project repository's list of working copies, read
// from the project's side.
func listCopies(ctx context.Context, project string) ([]listedCopy, error) {
	out, err := git(ctx, project, nil, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, err
	}
	var listed []listedCopy
	for _, field := range strings.Split(out, "\x00") {
		if path, ok := strings.CutPrefix(field, "worktree "); ok {
			listed = append(listed, listedCopy{path: path})
		} else if branch, ok := strings.CutPrefix(field, "branch "); ok && len(listed) > 0 {
			listed[len(listed)-1].branch = branch
		} else if (field == "locked" || strings.HasPrefix(field, "locked ")) && len(listed) > 0 {
			listed[len(listed)-1].locked = true
		}
	}
	return listed, nil
}

// addCopy creates the linked working copy at dir, which does not exist, given
// the repository's list of working copies. A registration git kept for dir
// after its folder was deleted is removed first; branch is checked out when
// it exists and otherwise created at base.
func addCopy(ctx context.Context, project, dir string, role Role, branch, base string,
	listed []listedCopy) error {
	for _, c := range listed {
		if same, err := samePath(c.path, dir); err == nil && same {
			if _, err := git(ctx, project, nil, "worktree", "remove", dir); err != nil {
				return fmt.Errorf("remove the registration of the deleted working copy: %w", err)
			}
		}
	}
	_, err := git(ctx, project, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", "refs/heads/"+branch)
	switch {
	case err == nil:
		_, err = git(ctx, project, nil, "worktree", "add", "--quiet", dir, branch)
	case exitCode(err) == 1:
		_, err = git(ctx, project, nil, "worktree", "add", "--quiet", "-b", branch, dir, base)
	}
	if err != nil {
		return fmt.Errorf("create %s: %w", role.copyName(), err)
	}
	return nil
}

// samePath reports whether a and b name the same location once symbolic
// links are resolved. Either may name a missing path.
func samePath(a, b string) (bool, error) {
	ra, err := abspath.EvalMissing(a)
	if err != nil {
		return false, err
	}
	rb, err := abspath.EvalMissing(b)
	if err != nil {
		return false, err
	}
	return filepath.Clean(ra) == filepath.Clean(rb), nil
}

// exitCode returns the exit status of a git command that ran and failed, or
// -1 for any other error.
func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// opened returns an error unless OpenCopy or MakeCopy made w: git run on a
// zero WorkingCopy's empty folder would act on the server's own folder
// instead.
func (w WorkingCopy) opened() error {
	if w.dir == "" {
		return errors.New("the working copy was not opened with OpenCopy or MakeCopy")
	}
	return nil
}
