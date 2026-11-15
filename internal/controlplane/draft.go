// This file owns the guide's draft folder and the provenance read together
// with it. Evaluation happens after releasing the draft's lock.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/designversion"
)

// committedDesignCanonicalError preserves a failed canonicalization's actual
// Git path separately from parser positions, which //line directives can alter.
type committedDesignCanonicalError struct {
	// file is the authoritative module-relative path selected by the walk.
	file string
	// cause retains the original parser/formatter error for existing callers.
	cause error
}

const (
	// draftFolderName is the guide's module, outside the project's Git files.
	draftFolderName = ".surla-draft"
	// draftStagingName is owned only when its first-written marker identifies it.
	draftStagingName = ".surla-draft-staging"
	// draftMarkerPrefix distinguishes Surla's folders from unrelated user files.
	draftMarkerPrefix = "# surla draft "
)

// Error preserves the original diagnostic text for existing draft consumers.
func (e *committedDesignCanonicalError) Error() string { return e.cause.Error() }

// Unwrap preserves typed parser causes without trusting their reported paths.
func (e *committedDesignCanonicalError) Unwrap() error { return e.cause }

// readDraftSnapshot reads the evaluator's canonical snapshot and its provenance
// through the same materialization and lock owner used by UpdateDraft.
func (c *ControlPlane) readDraftSnapshot(
	ctx context.Context, wsDir string, wsDev, wsIno uint64,
) (files map[string]string, saved madeFromRecord, err error) {
	err = c.withDraftSnapshot(ctx, wsDir, wsDev, wsIno, func(_ *os.Root, draft *os.Root, list madeFromRecord) error {
		var readErr error
		files, readErr = designversion.ReadDraft(filepath.Join(wsDir, draftFolderName), draft)
		saved = list
		if readErr != nil {
			return gensurla.MakeUnprocessable(readErr)
		}
		return nil
	})
	return files, saved, err
}

// withDraftSnapshot owns materialization, workspace identity and the draft/list
// lock. Its consumer may read malformed Go text without weakening refresh's
// canonical validation. Git queries release draftMu and revalidate before effects.
func (c *ControlPlane) withDraftSnapshot(
	ctx context.Context, wsDir string, wsDev, wsIno uint64,
	use func(project, draft *os.Root, list madeFromRecord) error,
) (retErr error) {
	c.workspaceMu.RLock()
	defer c.workspaceMu.RUnlock()
	if c.serverCtx.Err() != nil {
		return gensurla.MakeUnavailable(errors.New("the control plane is shutting down"))
	}
	if c.activeWorkspaceDir != wsDir || c.boundWorkspaceDev != wsDev || c.boundWorkspaceIno != wsIno {
		return gensurla.MakeUnprocessable(errors.New("bound workspace changed; call refresh_story again"))
	}
	c.draftMu.Lock()
	defer c.draftMu.Unlock()
	_, project, err := openModuleRootWithIdentity(wsDir, wsDev, wsIno)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, project.Close()) }()
	list, exists, err := c.cards.loadMadeFrom()
	if err != nil {
		return err
	}
	if !exists {
		list = madeFromRecord{Start: madeFromStart{Write: 1}, Entries: map[string][]madeFromVersion{}}
	}
	draft, err := openDraftFolder(project, draftFolderName)
	missing := errors.Is(err, fs.ErrNotExist)
	if err != nil && !missing {
		return gensurla.MakeUnprocessable(err)
	}
	if draft != nil && !exists {
		// Publication always saves provenance before the folder rename. Without
		// that record, even a marker cannot establish ownership of this folder.
		refusal := gensurla.MakeUnprocessable(errors.New("The draft folder already exists but this project's draft provenance is missing. Preserve it by moving .surla-draft aside, or restore this project's draft records, then call refresh_story again."))
		return errors.Join(refusal, draft.Close())
	}
	remake := missing || !exists
	// Drop publishes only the new start. Its first materialization must read
	// P/S even when the previous draft has already been deleted.
	unmaterializedDrop := list.Start.Dropped != nil && list.Write == list.Start.Write && len(list.Entries) == 0
	fromProject := !exists || missing && unmaterializedDrop
	var draftInfo fs.FileInfo
	var marker []byte
	var markerMissing bool
	if draft != nil {
		var readErr error
		draftInfo, readErr = draft.Stat(".")
		if readErr != nil {
			return errors.Join(readErr, draft.Close())
		}
		marker, readErr = designeval.ReadRegularFile(draft, ".gitignore", maxLayoutFileBytes)
		markerMissing = errors.Is(readErr, fs.ErrNotExist)
		switch {
		case errors.Is(readErr, fs.ErrNotExist):
			if unmaterializedDrop {
				// Keep the old folder unmarked until it is removed. Stamping it
				// with this start would hide a stopped remake after the list save.
				remake, fromProject, readErr = true, true, nil
			} else {
				readErr = writeDraftMarker(draft, list.Start.Write)
				if readErr == nil {
					marker = []byte(fmt.Sprintf("%s%d\n*\n", draftMarkerPrefix, list.Start.Write))
					markerMissing = false
				}
			}
		case readErr == nil:
			start, parseErr := draftMarkerStart(string(marker))
			readErr = parseErr
			if parseErr == nil && start != list.Start.Write {
				if start > list.Start.Write {
					readErr = errors.New("draft marker is newer than its design provenance")
				} else {
					remake, fromProject = true, true
				}
			}
		}
		if err := errors.Join(readErr, draft.Close()); err != nil {
			return gensurla.MakeUnprocessable(err)
		}
	}
	if remake {
		var restored *storedRevision
		if !fromProject {
			selected, found, err := c.newestDraftDesign(list.Start, list.Entries)
			if err != nil {
				return err
			}
			if found {
				restored = &selected
			}
		}
		var commit string
		var committed map[string]string
		{
			// Repository commands run outside draftMu. A concurrent refresh or
			// Drop wins rather than being overwritten by this prepared read.
			c.draftMu.Unlock()
			if fromProject {
				commit, committed, err = committedDraftDesign(ctx, wsDir)
			} else {
				_, _, commit, err = currentProjectCommit(ctx, wsDir)
			}
			c.draftMu.Lock()
			if err != nil {
				return gensurla.MakeUnprocessable(err)
			}
			// The open root still names the original inode even if its path
			// was replaced while Git resolved that path. Check the binding again
			// before pairing Git's commit with any files or publishing effects.
			_, verified, err := openModuleRootWithIdentity(wsDir, wsDev, wsIno)
			if err != nil {
				return err
			}
			if err := verified.Close(); err != nil {
				return err
			}
			current, currentExists, err := c.cards.loadMadeFrom()
			if err != nil {
				return err
			}
			sameDraft, err := draftStateMatches(project, draftInfo, marker, markerMissing)
			if err != nil {
				return gensurla.MakeUnprocessable(err)
			}
			if currentExists != exists || exists && !reflect.DeepEqual(current, list) || !sameDraft {
				return gensurla.MakeUnprocessable(errors.New(
					"draft or design provenance changed while reading the project's commit; call refresh_story again",
				))
			}
		}
		if err := c.makeDraft(wsDir, project, &list, restored, fromProject, commit, committed,
			draftInfo, markerMissing); err != nil {
			return gensurla.MakeUnprocessable(err)
		}
	}
	draft, err = openDraftFolder(project, draftFolderName)
	if err != nil {
		return gensurla.MakeUnprocessable(err)
	}
	return errors.Join(use(project, draft, list), draft.Close())
}

// makeDraft prepares a complete module with its ignore written first, saves
// all new blobs and one list write, then publishes the folder. draftMu is held.
func (c *ControlPlane) makeDraft(
	wsDir string, project *os.Root, list *madeFromRecord, restored *storedRevision, fromProject bool,
	commit string, committed map[string]string,
	draftInfo fs.FileInfo, markerMissing bool,
) error {
	var design map[string]string
	var err error
	if restored != nil {
		design, err = decodeDesignSource(restored.designSource)
		if err != nil {
			return err
		}
		list.Entries = cloneMadeFromSnapshot(restored.madeFrom).Entries
	}
	var files map[string]string
	usingProject := design == nil
	if usingProject {
		files, err = designversion.ReadDraft(wsDir, project)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return fmt.Errorf("read project module; run init_project or create go.mod before calling refresh_story: %w", err)
			}
			return err
		}
		design = draftDesignFiles(files)
	} else {
		files = map[string]string{}
		for _, file := range []string{"go.mod", "go.sum"} {
			data, readErr := designeval.ReadRegularFile(project, file, maxLayoutFileBytes)
			if file == "go.sum" && errors.Is(readErr, fs.ErrNotExist) {
				continue
			}
			if readErr != nil {
				return fmt.Errorf("read project %s: %w", file, readErr)
			}
			files[file] = string(data)
		}
	}
	if fromProject {
		list.Start.ProjectCommit = commit
		list.Start.FolderVersions = map[string]string{}
		for file := range design {
			list.Start.FolderVersions[file] = designFileVersion(design, file)
		}
	}
	if err := removeOwnedDraft(project, draftStagingName); err != nil {
		return err
	}
	if err := project.Mkdir(draftStagingName, 0o700); err != nil {
		return fmt.Errorf("make draft staging: %w", err)
	}
	stage, err := openDraftFolder(project, draftStagingName)
	if err != nil {
		return err
	}
	if err := writeDraftMarker(stage, list.Start.Write); err != nil {
		return errors.Join(err, stage.Close())
	}
	staged := maps.Clone(design)
	staged["go.mod"] = files["go.mod"]
	if sum, ok := files["go.sum"]; ok {
		staged["go.sum"] = sum
	}
	for _, file := range slices.Sorted(maps.Keys(staged)) {
		if err := stage.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			return errors.Join(err, stage.Close())
		}
		f, err := stage.OpenFile(filepath.FromSlash(file), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return errors.Join(err, stage.Close())
		}
		_, writeErr := f.WriteString(staged[file])
		if err := errors.Join(writeErr, f.Sync(), f.Close()); err != nil {
			return errors.Join(err, stage.Close())
		}
	}
	if err := stage.Close(); err != nil {
		return err
	}
	list.Write++
	if list.Write < list.Start.Write {
		list.Write = list.Start.Write
	}
	if list.Entries == nil {
		list.Entries = map[string][]madeFromVersion{}
	}
	// Include absence across both initial snapshots and existing history. All
	// versions of the initial folder and S enter in this one write.
	paths := map[string]bool{}
	for _, source := range []map[string]string{design, committed} {
		for file := range source {
			paths[file] = true
		}
	}
	for file := range list.Entries {
		paths[file] = true
	}
	for _, file := range slices.Sorted(maps.Keys(paths)) {
		if fromProject && list.Start.ProjectCommit != "" {
			if _, err := c.enterDraftVersion(list, file, committed); err != nil {
				return err
			}
		}
		if usingProject {
			if _, err := c.enterDraftVersion(list, file, design); err != nil {
				return err
			}
		}
	}
	list.Taken = map[string]madeFromTake{}
	for file := range paths {
		version := absentDesignVersion
		if text, ok := design[file]; ok {
			version = designversion.Digest(text)
		}
		for _, entered := range list.Entries[file] {
			if entered.Version == version {
				list.Taken[file] = madeFromTake{Version: entered.Version, Entered: entered.Entered, ProjectCommit: commit}
				break
			}
		}
	}
	list.Pending = nil
	if markerMissing && draftInfo != nil {
		// Only the unmaterialized Drop case retains a missing marker here.
		// Remove its exact old folder before publishing the list: a stop then
		// leaves a missing draft, never dropped text stamped as a new draft.
		if err := removeUnmarkedDroppedDraft(project, draftInfo); err != nil {
			return err
		}
	}
	if err := c.cards.saveMadeFrom(*list); err != nil {
		return err
	}
	if err := removeOwnedDraft(project, draftFolderName); err != nil {
		return err
	}
	if err := project.Rename(draftStagingName, draftFolderName); err != nil {
		return fmt.Errorf("publish draft: %w", err)
	}
	return nil
}

// enterDraftVersion saves a canonical text before entering its version once,
// moving an existing version to this write's end when a remake re-enters it.
func (c *ControlPlane) enterDraftVersion(
	list *madeFromRecord, file string, design map[string]string,
) (string, error) {
	version := absentDesignVersion
	if text, ok := design[file]; ok {
		var err error
		version, err = c.cards.saveDesignVersion(text)
		if err != nil {
			return "", err
		}
	}
	versions := slices.DeleteFunc(list.Entries[file], func(v madeFromVersion) bool { return v.Version == version })
	list.Entries[file] = append(versions, madeFromVersion{Version: version, Entered: list.Write})
	return version, nil
}

// newestDraftDesign considers waiting proposals before their confirmed card,
// and ignores declined revisions because the card owner no longer stores them.
func (c *ControlPlane) newestDraftDesign(
	start madeFromStart, entries map[string][]madeFromVersion,
) (storedRevision, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	records, err := c.cards.all()
	if err != nil {
		return storedRevision{}, false, err
	}
	slices.SortStableFunc(records, func(a, b storedChangeCard) int { return b.savedAt.Compare(a.savedAt) })
	for _, record := range records {
		revisions := []storedRevision{record.latest()}
		if record.proposed != nil {
			revisions = append(revisions, storedRevision{designSource: record.designSource, madeFrom: record.madeFrom})
		}
		for _, revision := range revisions {
			if revision.madeFrom == nil || revision.madeFrom.Start.Write != start.Write {
				continue
			}
			design, err := decodeDesignSource(revision.designSource)
			if err != nil {
				return storedRevision{}, false, err
			}
			paths := map[string]bool{}
			for file := range revision.madeFrom.Entries {
				paths[file] = true
			}
			for file, text := range design {
				canonical, err := designversion.Canonical(file, text)
				if err != nil {
					return storedRevision{}, false, err
				}
				design[file], paths[file] = canonical, true
			}
			eligible := true
			for file := range paths {
				version := designFileVersion(design, file)
				eligible = eligible && slices.ContainsFunc(entries[file], func(v madeFromVersion) bool { return v.Version == version })
			}
			if eligible {
				return revision, true, nil
			}
		}
	}
	return storedRevision{}, false, nil
}

// draftDesignFiles selects only the canonical design entries from ReadDraft.
func draftDesignFiles(files map[string]string) map[string]string {
	design := make(map[string]string)
	for file, text := range files {
		if strings.HasPrefix(file, "design/") {
			design[file] = text
		}
	}
	return design
}

// openDraftFolder refuses links and verifies that the opened folder is the
// directory checked through the identity-verified project root.
func openDraftFolder(project *os.Root, name string) (*os.Root, error) {
	info, err := project.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("draft folder %s is not a directory (symbolic links are refused)", name)
	}
	root, err := project.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.Join(fmt.Errorf("draft folder %s changed while opening it", name), err, root.Close())
	}
	return root, nil
}

// draftStateMatches checks the folder identity and marker captured before an
// unlocked committed-design read. Missing and replaced folders are distinct.
func draftStateMatches(
	project *os.Root, before fs.FileInfo, marker []byte, missing bool,
) (bool, error) {
	root, err := openDraftFolder(project, draftFolderName)
	if errors.Is(err, fs.ErrNotExist) {
		return before == nil, nil
	}
	if err != nil {
		return false, err
	}
	info, statErr := root.Stat(".")
	after, readErr := designeval.ReadRegularFile(root, ".gitignore", maxLayoutFileBytes)
	if err := errors.Join(statErr, root.Close()); err != nil {
		return false, err
	}
	if before == nil || !os.SameFile(before, info) {
		return false, nil
	}
	if errors.Is(readErr, fs.ErrNotExist) {
		return missing, nil
	}
	if readErr != nil {
		return false, readErr
	}
	return !missing && string(marker) == string(after), nil
}

// writeDraftMarker creates only a missing ignore, before any other staged file.
func writeDraftMarker(root *os.Root, start uint64) error {
	f, err := root.OpenFile(".gitignore", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := fmt.Fprintf(f, "%s%d\n*\n", draftMarkerPrefix, start)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

// draftMarkerStart accepts only the owned marker format and its positive start.
func draftMarkerStart(marker string) (uint64, error) {
	line, _, _ := strings.Cut(marker, "\n")
	value, found := strings.CutPrefix(line, draftMarkerPrefix)
	start, err := strconv.ParseUint(value, 10, 64)
	if !found || err != nil || start == 0 {
		return 0, errors.New("draft .gitignore has no Surla draft marker; the folder was left unchanged")
	}
	return start, nil
}

// removeUnmarkedDroppedDraft removes only the real, still-unmarked old folder
// inspected for an unmaterialized Drop. Drop already discarded its draft edits.
func removeUnmarkedDroppedDraft(project *os.Root, before fs.FileInfo) error {
	root, err := openDraftFolder(project, draftFolderName)
	if err != nil {
		return err
	}
	info, statErr := root.Stat(".")
	_, markerErr := designeval.ReadRegularFile(root, ".gitignore", maxLayoutFileBytes)
	if err := errors.Join(statErr, root.Close()); err != nil {
		return err
	}
	if !os.SameFile(before, info) || !errors.Is(markerErr, fs.ErrNotExist) {
		return gensurla.MakeUnprocessable(errors.New("draft changed before its remake; call refresh_story again"))
	}
	return project.RemoveAll(draftFolderName)
}

// removeOwnedDraft removes only a real folder with Surla's first-written
// marker. An unrelated directory, link or special file is never replaced.
func removeOwnedDraft(project *os.Root, name string) error {
	root, err := openDraftFolder(project, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	marker, readErr := designeval.ReadRegularFile(root, ".gitignore", maxLayoutFileBytes)
	if name == draftStagingName && errors.Is(readErr, fs.ErrNotExist) {
		if err := root.Close(); err != nil {
			return err
		}
		// Mkdir can succeed just before the first marker write is interrupted.
		// Remove is nonrecursive: any unmarked data, even added concurrently,
		// prevents recovery from deleting the folder.
		if err := project.Remove(name); err != nil {
			return fmt.Errorf("refuse replacing unowned folder %s: %w", name, err)
		}
		return nil
	}
	if readErr == nil {
		_, readErr = draftMarkerStart(string(marker))
	}
	if err := errors.Join(readErr, root.Close()); err != nil {
		return fmt.Errorf("refuse replacing unowned folder %s: %w", name, err)
	}
	return project.RemoveAll(name)
}

// committedDraftDesign reads S with bounded, read-only Git queries. The design
// walk's hidden/vendor/testdata/nested-module exclusions apply before parsing;
// links on a walked path are refused, as they are by ReadDraft.
func committedDraftDesign(ctx context.Context, dir string) (string, map[string]string, error) {
	ctx, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	repo, prefix, commit, err := currentProjectCommit(ctx, dir)
	if err != nil || commit == "" {
		return commit, nil, err
	}
	design, err := committedDesignAt(ctx, dir, repo, prefix, commit)
	return commit, design, err
}

// currentProjectCommit reads HEAD without interpreting any project file. An
// uninitialized or unborn repository has no commit; other failures remain errors.
func currentProjectCommit(ctx context.Context, dir string) (string, string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	repo, prefix, err := resolveStarterRepo(ctx, dir)
	if errors.Is(err, errNoRepository) {
		return "", "", "", nil
	}
	if err != nil {
		return "", "", "", err
	}
	head, err := runGitRead(ctx, repo, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil {
		// An unborn repository has no S. Other query failures must not hide a
		// broken repository or a canceled request.
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", "", "", nil
		}
		return "", "", "", err
	}
	commit := strings.TrimSpace(string(head))
	return repo, prefix, commit, nil

}

// committedDesignAt applies the canonical design walk to any immutable commit.
func committedDesignAt(ctx context.Context, dir, repo, prefix, commit string) (map[string]string, error) {
	files, err := committedDesignFiles(ctx, dir, repo, prefix, commit)
	if err != nil {
		return nil, err
	}
	for file, text := range files {
		canonical, err := designversion.Canonical(filepath.Join(dir, file), text)
		if err != nil {
			return nil, &committedDesignCanonicalError{file: file, cause: err}
		}
		files[file] = canonical
	}
	return files, nil
}

// committedDesignFiles owns the same bounded design walk for strict current
// snapshots and historical version matching. It preserves raw Go for its consumer.
func committedDesignFiles(ctx context.Context, dir, repo, prefix, commit string) (map[string]string, error) {
	out, err := runGitRead(ctx, repo, "ls-tree", "-r", "-z", commit, "--", prefix+"design")
	if err != nil {
		return nil, err
	}
	entries := map[string]string{}
	for _, entry := range strings.Split(string(out), "\x00") {
		if entry == "" {
			continue
		}
		meta, name, ok := strings.Cut(entry, "\t")
		if !ok {
			return nil, errors.New("git ls-tree returned an invalid design entry")
		}
		entries[strings.TrimPrefix(name, prefix)] = strings.Fields(meta)[0]
	}
	design := map[string]string{}
	for _, file := range slices.Sorted(maps.Keys(entries)) {
		skip := false
		parts := strings.Split(file, "/")
		for i := 1; i < len(parts)-1; i++ {
			folder := strings.Join(parts[:i+1], "/")
			if strings.HasPrefix(parts[i], ".") || parts[i] == "vendor" || parts[i] == "testdata" || entries[folder+"/go.mod"] != "" {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if entries[file] == "120000" || entries[file] == "160000" {
			return nil, fmt.Errorf("committed design %s is a link or submodule", file)
		}
		if !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		text, found, err := readGitBlob(ctx, repo, commit, prefix+file)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("committed design %s disappeared", file)
		}
		design[file] = string(text)
	}
	return design, nil
}
