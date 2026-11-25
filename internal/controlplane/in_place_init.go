// This file owns publication and restart recovery for initialization of an
// existing workspace directory.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/abspath"
	"github.com/goadesign/highbay/internal/renameat"
)

// workspaceEntry records the identity of one generated entry across a move.
// Every entry shares the device of the staged and bound directories.
type workspaceEntry struct {
	// Name is a single component used in both the stage and the workspace.
	Name string `json:"name"`
	// Ino is the staged file or directory inode.
	Ino uint64 `json:"ino"`
	// Type is the file type from lstat, independent of access permissions.
	Type uint32 `json:"type"`
}

// inPlaceAllowedEntries may coexist with a starter when they do not collide
// with a generated entry. Initialization leaves their contents untouched.
var inPlaceAllowedEntries = []string{
	".claude", ".codex", ".cursor", ".gemini", ".agents", ".vscode", ".mcp.json", ".DS_Store",
}

// prepareInPlaceRecord snapshots the staged names and identities and checks
// again for collisions before accepting publication.
func (c *ControlPlane) prepareInPlaceRecord(
	ctx context.Context, workspaceFD, stageFD int, wf workspaceFile,
) (workspaceFile, error) {
	workspace := c.initialWorkspaceRoot
	if err := verifyInPlaceDirs(workspaceFD, stageFD, workspace, filepath.Join(wf.StageDir, wf.Staging)); err != nil {
		return workspaceFile{}, err
	}
	if err := checkWorkspaceUninitialized(ctx, workspaceFD, workspace, "."); err != nil {
		return workspaceFile{}, err
	}
	entries, err := readDirEntriesFD(stageFD, wf.Staging, -1)
	if err != nil {
		return workspaceFile{}, err
	}
	wf.InPlace = true
	wf.WSDev, wf.WSIno = c.initialWorkspaceDev, c.initialWorkspaceIno
	wf.StageDev, wf.StageIno = wf.Dev, wf.Ino
	wf.Dev, wf.Ino = 0, 0
	var git workspaceEntry
	for _, entry := range entries {
		name := entry.Name()
		var st unix.Stat_t
		// Stat the destination even for allowed entries, so a case-folded
		// alias on a case-insensitive volume is treated as a collision.
		err := unix.Fstatat(workspaceFD, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if err == nil {
			return workspaceFile{}, genhighbay.MakeConflict(fmt.Errorf(
				"workspace %s already contains generated entry %q", workspace, name,
			))
		}
		if !errors.Is(err, unix.ENOENT) {
			return workspaceFile{}, fmt.Errorf("check publication destination %q: %w", name, err)
		}
		if err := unix.Fstatat(stageFD, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return workspaceFile{}, fmt.Errorf("stat staged entry %q: %w", name, err)
		}
		if uint64(st.Dev) != wf.StageDev {
			return workspaceFile{}, fmt.Errorf("staged entry %q is on another filesystem", name)
		}
		item := workspaceEntry{Name: name, Ino: st.Ino, Type: uint32(st.Mode) & unix.S_IFMT}
		if name == ".git" {
			git = item
		} else {
			wf.Entries = append(wf.Entries, item)
		}
	}
	slices.SortFunc(wf.Entries, func(a, b workspaceEntry) int { return strings.Compare(a.Name, b.Name) })
	wf.Entries = append(wf.Entries, git)
	if err := c.validateInPlaceWorkspaceRecord(wf); err != nil {
		return workspaceFile{}, err
	}
	return wf, nil
}

// restoreInPlaceWorkspace checks the original workspace identity, then
// resumes pending moves using the same publication path as InitProject.
func (c *ControlPlane) restoreInPlaceWorkspace(wf workspaceFile, workspaceFD, stageParentFD int) (err error) {
	if wf.Staging == "" {
		return nil
	}
	stageFD := -1
	if stageParentFD >= 0 {
		fd, err := unix.Openat(stageParentFD, wf.Staging, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("open in-place staging directory %q: %w", wf.Staging, err)
		}
		if err == nil {
			stageFD = fd
		}
	}
	if stageFD >= 0 {
		defer func() { err = errors.Join(err, unix.Close(stageFD)) }()
	}
	return c.finishInPlaceWorkspace(wf, workspaceFD, stageParentFD, stageFD)
}

// finishInPlaceWorkspace rolls forward a durable record. Once the record has
// been written, errors leave it and all remaining entries for startup
// recovery. A missing staging directory is valid when every move is complete.
func (c *ControlPlane) finishInPlaceWorkspace(wf workspaceFile, workspaceFD, stageParentFD, stageFD int) error {
	stagePath := filepath.Join(wf.StageDir, wf.Staging)
	if stageFD >= 0 {
		var st unix.Stat_t
		if err := unix.Fstat(stageFD, &st); err != nil {
			return fmt.Errorf("stat in-place staging directory: %w", err)
		}
		if uint64(st.Dev) != wf.StageDev || st.Ino != wf.StageIno {
			return fmt.Errorf("in-place staging directory %s was replaced", stagePath)
		}
	}
	workspace := c.initialWorkspaceRoot
	if err := checkInPlaceWorkspaceNames(workspaceFD, workspace, wf.Entries); err != nil {
		return err
	}
	if stageFD >= 0 {
		if err := checkInPlaceNames(stageFD, stagePath, nil, wf.Entries); err != nil {
			return err
		}
	}

	// A replay of an already published Git directory must find the complete
	// starter before moving anything else.
	git := wf.Entries[len(wf.Entries)-1]
	gitPublished, err := inPlaceEntryPresent(workspaceFD, wf.WSDev, git)
	if err != nil {
		return err
	}
	if gitPublished {
		if err := verifyInPlaceEntries(workspaceFD, wf.WSDev, wf.Entries); err != nil {
			return err
		}
	}
	for i, entry := range wf.Entries {
		if err := verifyInPlaceDirs(workspaceFD, stageFD, workspace, stagePath); err != nil {
			return err
		}
		staged, err := inPlaceEntryPresent(stageFD, wf.StageDev, entry)
		if err != nil {
			return err
		}
		published, err := inPlaceEntryPresent(workspaceFD, wf.WSDev, entry)
		if err != nil {
			return err
		}
		if staged == published {
			return fmt.Errorf("in-place entry %q must exist in exactly one of staging and the workspace", entry.Name)
		}
		if published {
			continue
		}
		if entry.Name == ".git" {
			// Make all preceding moves durable before exposing the Git
			// repository that makes this folder discoverable as a project.
			if err := checkInPlaceWorkspaceNames(workspaceFD, workspace, wf.Entries); err != nil {
				return err
			}
			if err := checkInPlaceNames(stageFD, stagePath, nil, wf.Entries[i:]); err != nil {
				return err
			}
			if err := verifyInPlaceEntries(workspaceFD, wf.WSDev, wf.Entries[:i]); err != nil {
				return err
			}
			if err := errors.Join(unix.Fsync(workspaceFD), unix.Fsync(stageFD)); err != nil {
				return fmt.Errorf("sync in-place entries before publishing .git: %w", err)
			}
		}
		if err := renameat.NoReplace(stageFD, entry.Name, workspaceFD, entry.Name); err != nil {
			return fmt.Errorf("publish in-place entry %q: %w", entry.Name, err)
		}
		if c.initStepHook != nil {
			c.initStepHook("after_in_place_move")
		}
	}
	if err := errors.Join(unix.Fsync(workspaceFD), syncInPlaceDir(stageFD)); err != nil {
		return fmt.Errorf("sync published in-place workspace: %w", err)
	}
	if c.initStepHook != nil {
		c.initStepHook("after_rename")
	}
	if err := verifyInPlaceDirs(workspaceFD, stageFD, workspace, stagePath); err != nil {
		return err
	}
	if err := checkInPlaceWorkspaceNames(workspaceFD, workspace, wf.Entries); err != nil {
		return err
	}
	if err := verifyInPlaceEntries(workspaceFD, wf.WSDev, wf.Entries); err != nil {
		return err
	}

	// Remove only the verified empty staging directory. An unexpected
	// remaining entry makes removal fail instead of deleting that entry.
	if stageFD >= 0 {
		if err := unix.Unlinkat(stageParentFD, wf.Staging, unix.AT_REMOVEDIR); err != nil {
			return fmt.Errorf("remove empty in-place staging directory: %w", err)
		}
	}
	if err := syncInPlaceDir(stageParentFD); err != nil {
		return fmt.Errorf("sync in-place staging parent: %w", err)
	}
	if c.initStepHook != nil {
		c.initStepHook("after_in_place_stage_removal")
	}
	return c.writeWorkspaceRecord(workspaceFile{
		Dir: wf.Dir, InPlace: true, WSDev: wf.WSDev, WSIno: wf.WSIno,
	})
}

// validateInPlaceWorkspaceRecord validates the schema and the binding to the
// workspace inode observed at startup. A finalized record drops the manifest
// so normal project edits need not preserve starter entry identities.
func (c *ControlPlane) validateInPlaceWorkspaceRecord(wf workspaceFile) error {
	if wf.Dir != "." || wf.Dev != 0 || wf.Ino != 0 {
		return errors.New("in-place workspace.json has child publication fields")
	}
	if wf.WSDev == 0 || wf.WSIno == 0 || wf.WSDev != c.initialWorkspaceDev || wf.WSIno != c.initialWorkspaceIno {
		return errors.New("in-place workspace no longer matches its recorded (dev, ino) identity")
	}
	if wf.Staging == "" {
		if wf.StageDev != 0 || wf.StageIno != 0 || len(wf.Entries) != 0 {
			return errors.New("finalized in-place workspace.json has pending publication fields")
		}
		return nil
	}
	if wf.StageDev != wf.WSDev || wf.StageIno == 0 || wf.StageIno == wf.WSIno {
		return errors.New("in-place workspace.json has an invalid staging identity")
	}
	seen := make(map[string]bool, len(wf.Entries))
	for i, entry := range wf.Entries {
		name := entry.Name
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") ||
			seen[name] || entry.Ino == 0 || (entry.Type != unix.S_IFDIR && entry.Type != unix.S_IFREG) {
			return fmt.Errorf("invalid in-place manifest entry %q in workspace.json", name)
		}
		if name == ".git" && (i != len(wf.Entries)-1 || entry.Type != unix.S_IFDIR) {
			return errors.New("in-place manifest must publish a .git directory last")
		}
		seen[name] = true
	}
	if !seen[".git"] {
		return errors.New("in-place manifest is missing its staged .git directory")
	}
	return nil
}

// checkInPlaceWorkspaceNames admits only preserved dot entries and names
// from a manifest when resuming a pending publication.
func checkInPlaceWorkspaceNames(workspaceFD int, workspace string, manifest []workspaceEntry) error {
	return checkInPlaceNames(workspaceFD, workspace, inPlaceAllowedEntries, manifest)
}

// checkInPlaceNames checks directory entries without reading their contents
// or following links. One more than the number of allowed names suffices to
// find an unexpected entry even in a large directory.
func checkInPlaceNames(dirFD int, dir string, preserved []string, manifest []workspaceEntry) error {
	entries, err := readDirEntriesFD(dirFD, dir, len(preserved)+len(manifest)+1)
	if err != nil {
		return fmt.Errorf("read in-place directory %s: %w", dir, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if slices.Contains(preserved, name) || slices.ContainsFunc(manifest, func(e workspaceEntry) bool { return e.Name == name }) {
			continue
		}
		return genhighbay.MakeConflict(fmt.Errorf("directory %s already contains %q; in-place initialization would conflict", dir, name))
	}
	return nil
}

// inPlaceEntryPresent treats a missing name as absent and refuses any other
// entry that does not match the manifest identity without following symlinks.
func inPlaceEntryPresent(fd int, dev uint64, entry workspaceEntry) (bool, error) {
	if fd < 0 {
		return false, nil
	}
	var st unix.Stat_t
	err := unix.Fstatat(fd, entry.Name, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat in-place entry %q: %w", entry.Name, err)
	}
	if uint64(st.Dev) != dev || st.Ino != entry.Ino || uint32(st.Mode)&unix.S_IFMT != entry.Type {
		return false, fmt.Errorf("in-place entry %q no longer matches its recorded identity", entry.Name)
	}
	return true, nil
}

// verifyInPlaceEntries ensures the published entries still match the
// manifest, including those already present at the start of replay.
func verifyInPlaceEntries(workspaceFD int, dev uint64, entries []workspaceEntry) error {
	for _, entry := range entries {
		found, err := inPlaceEntryPresent(workspaceFD, dev, entry)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("in-place workspace is missing published entry %q", entry.Name)
		}
	}
	return nil
}

// verifyInPlaceDirs refuses to publish to or from a renamed or replaced
// directory. FDPath checks the path against its open descriptor identity.
func verifyInPlaceDirs(workspaceFD, stageFD int, workspace, stage string) error {
	if path, err := abspath.FDPath(workspaceFD); err != nil || path != workspace {
		return fmt.Errorf("in-place workspace directory %s was moved or replaced", workspace)
	}
	if stageFD >= 0 {
		if path, err := abspath.FDPath(stageFD); err != nil || path != stage {
			return fmt.Errorf("in-place staging directory %s was moved or replaced", stage)
		}
	}
	return nil
}

// syncInPlaceDir syncs a directory when it is still present during recovery.
func syncInPlaceDir(fd int) error {
	if fd < 0 {
		return nil
	}
	return unix.Fsync(fd)
}
