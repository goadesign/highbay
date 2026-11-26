// This file brings outside design edits into the guide's owned draft.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"go/scanner"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/designeval"
	"github.com/goadesign/highbay/internal/designversion"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/mod/modfile"
	"golang.org/x/sys/unix"
)

// draftFileUpdate is a prepared write or completion of an interrupted write.
// The actual list and file owner applies it only after revalidation.
type draftFileUpdate struct {
	pending pendingDraftWrite
	outside string
	text    string
	write   bool
	kind    string
}

const (
	// correctInDraft is DraftCorrection.Folder for a file of the guide's
	// draft, which the guide corrects itself.
	correctInDraft = "draft_folder"
	// correctInProject is DraftCorrection.Folder for a file of the user's
	// project folder, which only the user corrects.
	correctInProject = "project_folder"
)

// UpdateDraft brings the project folder's changes into the draft, preserving
// merge conflicts for the guide. Evaluation remains a separate refresh action.
func (c *ControlPlane) UpdateDraft(ctx context.Context) (*genhighbay.DraftUpdateResult, error) {
	principal, ok := PrincipalFromContext(ctx)
	if !ok {
		return nil, errors.New("authenticated principal is required")
	}
	wsDir, dev, ino := c.currentWorkspaceTarget()
	ctx, span := c.tracer("highbay/controlplane").Start(ctx, "highbay.controlplane.update_draft")
	defer span.End()
	result := &genhighbay.DraftUpdateResult{DraftFolder: filepath.Join(wsDir, draftFolderName), Taken: []string{}, Merged: []string{}, ToCorrect: []*genhighbay.DraftCorrection{}}
	err := c.withDraftSnapshot(ctx, wsDir, dev, ino, func(project, draft *os.Root, list madeFromRecord) error {
		draftFiles, err := readUpdateFolder(correctInDraft, result.DraftFolder, draft)
		if err != nil {
			return err
		}
		projectFiles, err := readUpdateFolder(correctInProject, wsDir, project)
		if err != nil {
			return err
		}
		info, err := draft.Stat(".")
		if err != nil {
			return err
		}
		marker, err := designeval.ReadRegularFile(draft, ".gitignore", maxLayoutFileBytes)
		if err != nil {
			return err
		}
		// Git and merge-file never run under either state lock. Captured Go
		// text is rechecked together with the list before the first effect.
		c.draftMu.Unlock()
		_, _, head, readErr := currentProjectCommit(ctx, wsDir)
		var updates []draftFileUpdate
		if readErr == nil {
			updates, readErr = c.planDraftUpdates(ctx, wsDir, head, list, draftFiles, projectFiles, result)
		}
		if readErr == nil {
			_, _, after, err := currentProjectCommit(ctx, wsDir)
			readErr = err
			if err == nil && after != head {
				readErr = genhighbay.MakeConflict(errors.New("The project's commit changed while preparing the draft update; call update_draft again."))
			}
		}
		c.draftMu.Lock()
		if readErr != nil {
			return readErr
		}
		_, verified, err := openModuleRootWithIdentity(wsDir, dev, ino)
		if err != nil {
			return err
		}
		if err := verified.Close(); err != nil {
			return err
		}
		current, found, err := c.cards.loadMadeFrom()
		if err != nil {
			return err
		}
		same, err := draftStateMatches(project, info, marker, false)
		if err != nil {
			// The draft folder or its marker stopped being readable while Git
			// ran: the next call refuses the same state before reading it.
			return genhighbay.MakeUnprocessable(fmt.Errorf(
				"draft %s could no longer be read while preparing the update: %w; restore it or move it aside, then call update_draft again",
				result.DraftFolder, err,
			))
		}
		nowDraft, err := readUpdateFolder(correctInDraft, result.DraftFolder, draft)
		if err != nil {
			return err
		}
		nowProject, err := readUpdateFolder(correctInProject, wsDir, project)
		if err != nil {
			return err
		}
		if !found || !same || !reflect.DeepEqual(current, list) || !maps.Equal(nowDraft, draftFiles) || !maps.Equal(nowProject, projectFiles) {
			return genhighbay.MakeConflict(errors.New("The draft, project files or design provenance changed while preparing the update; call update_draft again."))
		}
		for _, update := range updates {
			if err := c.applyDraftUpdate(draft, &list, update); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if !expectedRequestRefusal(err) {
			span.RecordError(err)
			span.SetStatus(codes.Error, "update draft")
		}
		return nil, err
	}
	owner := principal.ID
	c.draftOwner.Store(&owner)
	slices.Sort(result.Taken)
	slices.Sort(result.Merged)
	slices.SortStableFunc(result.ToCorrect, func(a, b *genhighbay.DraftCorrection) int { return strings.Compare(a.Path, b.Path) })
	return result, nil
}

// planDraftUpdates performs immutable reads and single merges outside the lock.
func (c *ControlPlane) planDraftUpdates(
	ctx context.Context, dir, head string, original madeFromRecord, draft, project map[string]string,
	result *genhighbay.DraftUpdateResult,
) ([]draftFileUpdate, error) {
	list := cloneDraftRecord(original)
	design := maps.Clone(draft)
	var updates []draftFileUpdate
	skip := map[string]bool{}
	if pending := list.Pending; pending != nil {
		file := pending.File
		version := updateFileVersion(design, file)
		update := draftFileUpdate{pending: *pending}
		if version == pending.Before && version != pending.Result {
			outside, _, err := c.cards.loadDesignVersion(pending.OutsideVersion)
			if err != nil {
				return nil, err
			}
			oldHistory, err := readDesignHistory(ctx, dir, pending.ProjectCommit,
				madeFromSnapshot{Start: list.Start, Entries: list.Entries}, list.Taken)
			if err != nil {
				return nil, err
			}
			prepared, problem, err := c.mergeDraftFile(ctx, file, design, outside, pending.OutsideVersion, pending.ProjectCommit, list, oldHistory)
			if err != nil {
				return nil, err
			}
			if prepared == nil || prepared.pending.Result != pending.Result {
				return nil, errors.New("interrupted draft write no longer reproduces its saved result")
			}
			update = *prepared
			if problem != "" {
				addDraftCorrection(result, correctInDraft, file, problem)
			}
		} else if version != pending.Result {
			addDraftCorrection(result, correctInDraft, file, "Highbay stopped while bringing the project folder's change into your draft, and the draft changed since: make sure it holds that change once.")
			skip[file] = true
		}
		updates = append(updates, update)
		if update.write {
			setUpdateText(design, file, update.text, update.pending.Result)
			list.Write++
		}
		completeDraftTake(&list, update.pending)
	}
	history, err := readDesignHistory(ctx, dir, head, madeFromSnapshot{Start: list.Start, Entries: list.Entries}, list.Taken)
	if err != nil {
		return nil, committedDesignRefusal(head, err)
	}
	for _, file := range designPaths(list.Entries, draftDesignFiles(design), draftDesignFiles(project)) {
		if skip[file] {
			continue
		}
		if text, found := design[file]; found {
			canonical, err := designversion.Canonical(file, text)
			if err != nil {
				addDraftCorrection(result, correctInDraft, file, err.Error())
				continue
			}
			design[file] = canonical
		}
		outside := project[file]
		outsideVersion := absentDesignVersion
		if text, found := project[file]; found {
			canonical, err := designversion.Canonical(file, text)
			if err != nil {
				// The draft holds nothing to correct here: the user's own
				// file must parse before its change can be compared.
				addDraftCorrection(result, correctInProject, file, err.Error())
				continue
			}
			outside, outsideVersion = canonical, designversion.Digest(canonical)
		}
		if outsideVersion == history.base(file, list) || history.current(file, outsideVersion, list.Entries[file]) {
			continue
		}
		update, problem, err := c.mergeDraftFile(ctx, file, design, outside, outsideVersion, head, list, history)
		if err != nil {
			return nil, err
		}
		if problem != "" {
			addDraftCorrection(result, correctInDraft, file, problem)
		}
		if update == nil {
			continue
		}
		updates = append(updates, *update)
		setUpdateText(design, file, update.text, update.pending.Result)
		// Reserve the pending write and its completion in the same order as
		// the persistence owner. Only completed takes enter provenance.
		list.Write++
		completeDraftTake(&list, update.pending)
		switch update.kind {
		case "taken":
			result.Taken = append(result.Taken, file)
		case "merged":
			result.Merged = append(result.Merged, file)
		}
	}
	compareDraftModules(project["go.mod"], draft["go.mod"], result)
	return updates, nil
}

// mergeDraftFile chooses one base and never merges a possibly incorporated edit twice.
func (c *ControlPlane) mergeDraftFile(
	ctx context.Context, file string, draft map[string]string, outside, outsideVersion, commit string,
	list madeFromRecord, history designHistory,
) (*draftFileUpdate, string, error) {
	baseVersion := history.base(file, list)
	base, _, err := c.cards.loadDesignVersion(baseVersion)
	if err != nil {
		return nil, "", err
	}
	before := updateFileVersion(draft, file)
	text, kind, problem := outside, "taken", ""
	if history.gained[file] && baseVersion == list.Taken[file].Version {
		kind, problem = "correct", "The project's history gained design changes from another line after this file was taken. Highbay took the live project file without merging; check that it holds your intended draft edits once."
	} else if before == outsideVersion {
		kind = "merged"
	} else if before != baseVersion {
		if before == absentDesignVersion || outsideVersion == absentDesignVersion {
			return nil, "One side removed this file while the other changed it; correct the draft to keep the intended design.", nil
		}
		var conflict bool
		text, conflict, err = designversion.MergeFile(ctx, draft[file], base, outside)
		if err != nil {
			return nil, "", err
		}
		kind = "merged"
		if conflict {
			kind, problem = "correct", "The draft and project folder changed the same lines; resolve the conflict markers in this draft file."
		} else {
			canonical, parseErr := designversion.Canonical(file, text)
			if parseErr != nil {
				kind, problem = "correct", parseErr.Error()
			} else {
				text = canonical
			}
		}
	}
	version := absentDesignVersion
	if text != "" {
		version = designversion.Digest(text)
	}
	return &draftFileUpdate{pending: pendingDraftWrite{File: file, OutsideVersion: outsideVersion, Before: before, Result: version, ProjectCommit: commit}, outside: outside, text: text, write: true, kind: kind}, problem, nil
}

// applyDraftUpdate saves the write-ahead entry, replaces the file once, and
// completes the take. A stop at any boundary leaves enough state for recovery.
func (c *ControlPlane) applyDraftUpdate(draft *os.Root, list *madeFromRecord, update draftFileUpdate) error {
	if update.write {
		if update.pending.OutsideVersion != absentDesignVersion {
			version, err := c.cards.saveDesignVersion(update.outside)
			if err != nil {
				return err
			}
			if version != update.pending.OutsideVersion {
				return errors.New("prepared outside version changed")
			}
		}
		list.Write++
		list.Pending = &update.pending
		if err := c.cards.saveMadeFrom(*list); err != nil {
			return err
		}
		if err := writeUpdatedDraftFile(draft, update.pending.File, update.text, update.pending.Result); err != nil {
			return err
		}
	}
	// A previous attempt may have renamed the file but failed its directory
	// sync. Completion-only recovery must establish durability again.
	if !update.write {
		if err := syncDraftUpdateParent(draft, update.pending.File); err != nil {
			return err
		}
	}
	completeDraftTake(list, update.pending)
	return c.cards.saveMadeFrom(*list)
}

// completeDraftTake enters only the outside version after its file write.
func completeDraftTake(list *madeFromRecord, pending pendingDraftWrite) {
	list.Write++
	if list.Entries == nil {
		list.Entries = map[string][]madeFromVersion{}
	}
	entries := slices.DeleteFunc(list.Entries[pending.File], func(v madeFromVersion) bool { return v.Version == pending.OutsideVersion })
	list.Entries[pending.File] = append(entries, madeFromVersion{Version: pending.OutsideVersion, Entered: list.Write})
	if list.Taken == nil {
		list.Taken = map[string]madeFromTake{}
	}
	list.Taken[pending.File] = madeFromTake{Version: pending.OutsideVersion, Entered: list.Write, ProjectCommit: pending.ProjectCommit}
	list.Pending = nil
}

// writeUpdatedDraftFile reuses the existing no-follow atomic/fsync owner.
// Every file or directory effect is durable before Pending can be completed.
func writeUpdatedDraftFile(root *os.Root, file, text, version string) (retErr error) {
	base, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, base.Close()) }()
	dir, err := draftUpdateDirectory(int(base.Fd()), path.Dir(file), version != absentDesignVersion)
	if errors.Is(err, unix.ENOENT) && version == absentDesignVersion {
		return syncDraftUpdateParent(root, file)
	}
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, unix.Close(dir)) }()
	if version == absentDesignVersion {
		if err := unix.Unlinkat(dir, path.Base(file), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
		return fsyncDir(dir)
	}
	_, exists, err := readAtNoFollow(dir, path.Base(file))
	if err != nil {
		return err
	}
	_, err = writeAtomic(dir, path.Base(file), []byte(text), !exists)
	return err
}

// draftUpdateDirectory creates each missing parent without following links,
// syncing its parent before descending. Existing parents are synced too: a
// prior attempt may have created them and stopped before that sync completed.
func draftUpdateDirectory(base int, rel string, create bool) (int, error) {
	current, err := openDirAt(base, ".")
	if err != nil {
		return -1, err
	}
	if rel == "." {
		return current, nil
	}
	for _, part := range strings.Split(rel, "/") {
		next, err := unix.Openat(current, part, openDirFlags, 0)
		if errors.Is(err, unix.ENOENT) && create {
			err = unix.Mkdirat(current, part, 0o700)
			if err == nil || errors.Is(err, unix.EEXIST) {
				next, err = unix.Openat(current, part, openDirFlags, 0)
			}
		}
		if err != nil {
			return -1, errors.Join(err, unix.Close(current))
		}
		syncErr := fsyncDir(current)
		closeErr := unix.Close(current)
		if err := errors.Join(syncErr, closeErr); err != nil {
			return -1, errors.Join(err, unix.Close(next))
		}
		current = next
	}
	return current, nil
}

// syncDraftUpdateParent makes a visible recovered result durable before its
// take is completed, without rewriting the guide's file or merging it again.
func syncDraftUpdateParent(root *os.Root, file string) (retErr error) {
	base, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, base.Close()) }()
	rel := path.Dir(file)
	dir, err := draftUpdateDirectory(int(base.Fd()), rel, false)
	for errors.Is(err, unix.ENOENT) && rel != "." {
		rel = path.Dir(rel)
		dir, err = draftUpdateDirectory(int(base.Fd()), rel, false)
	}
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, unix.Close(dir)) }()
	return fsyncDir(dir)
}

// readUpdateFolder reads the files update_draft compares in dir, the guide's
// draft (correctInDraft) or the project folder (correctInProject). A file
// there that is missing, not a regular file, unreadable or not valid UTF-8
// is the declared unprocessable refusal, as it is when refresh_story reads
// the draft: its message names the folder and the file, and says who
// corrects it, because the guide never edits the project folder.
func readUpdateFolder(folder, dir string, root *os.Root) (map[string]string, error) {
	files, err := readDraftUpdateFiles(dir, root)
	switch {
	case err == nil:
		return files, nil
	case folder == correctInProject:
		return nil, genhighbay.MakeUnprocessable(fmt.Errorf(
			"project folder %s: %w; ask the user to restore or correct it in the project folder, then call update_draft again", dir, err,
		))
	default:
		return nil, genhighbay.MakeUnprocessable(fmt.Errorf(
			"draft %s: %w; restore or correct it in the draft, then call update_draft again", dir, err,
		))
	}
}

// committedDesignRefusal classifies a failed read of the design at commit,
// the project's current commit. A design file there that Go cannot parse, or
// that is a link or submodule, is the user's to correct and commit, so it is
// the declared unprocessable refusal naming the file and the commit. Every
// other failure (Git could not run or read the repository) is returned as it
// is and stays a fault.
func committedDesignRefusal(commit string, err error) error {
	const next = "ask the user to correct it in the project folder and commit, then call update_draft again"
	var syntax scanner.ErrorList
	var source *committedDesignCanonicalError
	switch {
	case errors.As(err, &syntax) && errors.As(err, &source):
		return genhighbay.MakeUnprocessable(fmt.Errorf(
			"%s in the project's commit %s cannot be parsed as Go: %w; %s", source.file, commit, err, next,
		))
	case errors.Is(err, errCommittedDesignNotRegular):
		return genhighbay.MakeUnprocessable(fmt.Errorf("the project's commit %s: %w; %s", commit, err, next))
	default:
		return err
	}
}

// readDraftUpdateFiles reuses the canonical walk's safety without requiring Go
// to parse: malformed draft files belong in the result's correction list.
func readDraftUpdateFiles(dir string, root *os.Root) (map[string]string, error) {
	files, err := designeval.ReadDesignFilesFromRoot(dir, root)
	if err != nil {
		return nil, err
	}
	if files == nil {
		files = map[string]string{}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		data, err := designeval.ReadRegularFile(root, name, maxLayoutFileBytes)
		if name == "go.sum" && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		files[name] = string(data)
	}
	return files, designeval.ValidateUTF8Snapshot(files)
}

// updateFileVersion compares canonical Go when possible and raw conflict text otherwise.
func updateFileVersion(files map[string]string, file string) string {
	text, found := files[file]
	if !found {
		return absentDesignVersion
	}
	if canonical, err := designversion.Canonical(file, text); err == nil {
		text = canonical
	}
	return designversion.Digest(text)
}

// setUpdateText mirrors one prepared file replacement in the local plan only.
func setUpdateText(files map[string]string, file, text, version string) {
	if version == absentDesignVersion {
		delete(files, file)
	} else {
		files[file] = text
	}
}

// cloneDraftRecord separates the planning simulation from captured persisted state.
func cloneDraftRecord(saved madeFromRecord) madeFromRecord {
	copy := saved
	copy.Entries = cloneMadeFromSnapshot(&madeFromSnapshot{Start: saved.Start, Entries: saved.Entries}).Entries
	copy.Taken = maps.Clone(saved.Taken)
	if saved.Pending != nil {
		pending := *saved.Pending
		copy.Pending = &pending
	}
	return copy
}

// addDraftCorrection lists a file once even when recovery also found invalid
// Go. folder is correctInDraft or correctInProject: the folder holding the
// file whose text has the problem, which decides who corrects it.
func addDraftCorrection(result *genhighbay.DraftUpdateResult, folder, file, problem string) {
	for _, correction := range result.ToCorrect {
		if correction.Path == file {
			return
		}
	}
	result.ToCorrect = append(result.ToCorrect, &genhighbay.DraftCorrection{Path: file, Folder: folder, Problem: problem})
}

// compareDraftModules names exact requirement/replacement differences. Highbay
// never merges or rewrites go.mod: the draft's is the guide's to correct, and
// a project go.mod that cannot be parsed or declares no module is the user's.
func compareDraftModules(project, draft string, result *genhighbay.DraftUpdateResult) {
	p, err := modfile.Parse("project go.mod", []byte(project), nil)
	if err != nil {
		addDraftCorrection(result, correctInProject, "go.mod", err.Error())
		return
	}
	if p.Module == nil {
		addDraftCorrection(result, correctInProject, "go.mod", "The project's go.mod declares no module.")
		return
	}
	d, err := modfile.Parse("draft go.mod", []byte(draft), nil)
	if err != nil {
		addDraftCorrection(result, correctInDraft, "go.mod", err.Error())
		return
	}
	if d.Module == nil || p.Module.Mod.Path != d.Module.Mod.Path {
		addDraftCorrection(result, correctInDraft, "go.mod", "The project and draft must declare the same module.")
		return
	}
	required := map[string]string{}
	for _, r := range d.Require {
		required[r.Mod.Path] = r.Mod.Version
	}
	var problems []string
	for _, r := range p.Require {
		if required[r.Mod.Path] != r.Mod.Version {
			problems = append(problems, fmt.Sprintf("The project requires %s %s; the draft requires %s.", r.Mod.Path, r.Mod.Version, required[r.Mod.Path]))
		}
	}
	replaces := func(file *modfile.File) map[string]string {
		out := map[string]string{}
		for _, r := range file.Replace {
			out[r.Old.String()] = r.New.String()
		}
		return out
	}
	if !maps.Equal(replaces(p), replaces(d)) {
		problems = append(problems, "The project and draft go.mod have differing replace directives; correct the draft's replacements.")
	}
	if len(problems) > 0 {
		addDraftCorrection(result, correctInDraft, "go.mod", strings.Join(problems, " "))
	}
}
