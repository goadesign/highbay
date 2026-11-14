// This file checks immutable revision provenance against actual Git history.
package controlplane

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designversion"
)

// designHistory holds bounded immutable Git reads prepared outside state locks.
type designHistory struct {
	// start is a complete immutable original-folder snapshot; missing paths
	// were absent at the start, including paths first added in the draft.
	start madeFromStart
	// design is the head's design, even when head belongs to Start's history.
	design map[string]string
	// versions contains the canonical snapshots in head's history after Start.
	versions map[string]map[string]string
	// gained records C5's incomparable history for each existing take.
	gained map[string]bool
}

// checkDesignBase refuses a commit that would replace an outside fix or revert.
// Callers pass only the revision/build's immutable history, never the live list.
func checkDesignBase(ctx context.Context, dir, commit string, saved *madeFromSnapshot) error {
	if saved == nil {
		return gensurla.MakeConflict(errors.New("The agreed revision records no design, so there is nothing to build from. Ask the guide to propose a revision from its draft, adopt it, then build."))
	}
	history, err := readDesignHistory(ctx, dir, commit, *saved, nil)
	if err != nil {
		return err
	}
	paths := designPaths(saved.Entries, history.design)
	for _, file := range paths {
		version := designFileVersion(history.design, file)
		if !history.current(file, version, saved.Entries[file]) {
			return gensurla.MakeDesignChangedOutside(fmt.Errorf("%s in commit %s is not a version this revision was made from: it was changed outside Surla. Commit the design change in the project folder, then ask the guide to call update_draft, refresh_story and revise_change; adopt the revision, then build.", file, commit))
		}
	}
	return nil
}

// readDesignHistory reads one fixed head and its graph. Nothing executes from
// the repository. The existing Git owner bounds each output and the whole phase.
func readDesignHistory(
	ctx context.Context, dir, head string, saved madeFromSnapshot, taken map[string]madeFromTake,
) (designHistory, error) {
	ctx, cancel := context.WithTimeout(ctx, layoutReadTimeout)
	defer cancel()
	h := designHistory{start: saved.Start, versions: map[string]map[string]string{}, gained: map[string]bool{}}
	if head == "" {
		return h, nil
	}
	repo, prefix, err := resolveStarterRepo(ctx, dir)
	if err != nil {
		return h, err
	}
	h.design, err = committedDesignAt(ctx, dir, repo, prefix, head)
	if err != nil {
		return h, err
	}
	// No commit can be gained when P is still S. In particular, do not read
	// the potentially enormous ancestry that this generation excludes.
	if head == saved.Start.ProjectCommit {
		return h, nil
	}
	args := []string{"rev-list", head}
	if saved.Start.ProjectCommit != "" {
		args = append(args, "--not", saved.Start.ProjectCommit)
	}
	out, err := runGitRead(ctx, repo, args...)
	if err != nil {
		return h, err
	}
	for _, commit := range strings.Fields(string(out)) {
		if commit == head {
			h.versions[commit] = h.design
			continue
		}
		design, err := committedHistoryDesign(ctx, dir, repo, prefix, commit)
		if err != nil {
			return h, err
		}
		h.versions[commit] = design
	}
	var boundary map[string]string
	if saved.Start.ProjectCommit != "" && len(taken) > 0 {
		boundary, err = committedHistoryDesign(ctx, dir, repo, prefix, saved.Start.ProjectCommit)
		if err != nil {
			return h, err
		}
	}
	for file, take := range taken {
		args := []string{"rev-list", head}
		if saved.Start.ProjectCommit != "" {
			args = append(args, "--not", saved.Start.ProjectCommit)
		}
		if take.ProjectCommit != "" {
			args = append(args, "--not", take.ProjectCommit)
		}
		gained, err := runGitRead(ctx, repo, args...)
		if err != nil {
			return h, err
		}
		// Only commits in the required post-S set can contribute a listed
		// version. Ancestry queries ask Git directly instead of fetching and
		// retaining all ancestors of S or of the take.
		holding := []string{}
		for commit, design := range h.versions {
			if designFileVersion(design, file) == take.Version {
				holding = append(holding, commit)
			}
		}
		if boundary != nil && designFileVersion(boundary, file) == take.Version {
			holding = append(holding, saved.Start.ProjectCommit)
		}
		for _, commit := range strings.Fields(string(gained)) {
			design, found := h.versions[commit]
			if !found {
				continue
			}
			version := designFileVersion(design, file)
			at, listed := enteredVersion(saved.Entries[file], version)
			if !listed || version == take.Version || at >= take.Entered {
				continue
			}
			descends := false
			for _, ancestor := range holding {
				yes, err := buildCommitInHistory(ctx, repo, ancestor, commit)
				if err != nil {
					return h, err
				}
				if yes {
					descends = true
					break
				}
			}
			if !descends {
				h.gained[file] = true
				break
			}
		}
	}
	return h, nil
}

// committedHistoryDesign leaves malformed historical Go as a nonmatching raw
// version. A repaired current commit remains usable; real Git/safety errors
// still fail the read. The current commit uses strict committedDesignAt.
func committedHistoryDesign(ctx context.Context, dir, repo, prefix, commit string) (map[string]string, error) {
	files, err := committedDesignFiles(ctx, dir, repo, prefix, commit)
	if err != nil {
		return nil, err
	}
	for file, text := range files {
		if canonical, parseErr := designversion.Canonical(file, text); parseErr == nil {
			files[file] = canonical
		}
	}
	return files, nil
}

// current compares entry order, including a committed revert to an older version.
func (h designHistory) current(file, version string, entries []madeFromVersion) bool {
	entered, found := enteredVersion(entries, version)
	if !found && version == absentDesignVersion && h.start.FolderVersions[file] == "" {
		entered, found = h.start.Write, true
	}
	if !found {
		return false
	}
	for _, design := range h.versions {
		other, listed := enteredVersion(entries, designFileVersion(design, file))
		if listed && other > entered {
			return false
		}
	}
	return true
}

// base follows C1: the latest entered history version, else the immutable
// original folder version, compared with the latest outside take.
func (h designHistory) base(file string, list madeFromRecord) string {
	base := list.Start.FolderVersions[file]
	if base == "" {
		base = absentDesignVersion
	}
	entered, listed := enteredVersion(list.Entries[file], base)
	if !listed && base == absentDesignVersion {
		entered = list.Start.Write
	}
	found := false
	for _, design := range h.versions {
		version := designFileVersion(design, file)
		at, listed := enteredVersion(list.Entries[file], version)
		if listed && (!found || at > entered) {
			base, entered, found = version, at, true
		}
	}
	if take, ok := list.Taken[file]; ok && take.Entered > entered {
		base = take.Version
	}
	return base
}

// enteredVersion reads the current entry of one distinct listed version.
func enteredVersion(entries []madeFromVersion, version string) (uint64, bool) {
	for _, entry := range entries {
		if entry.Version == version {
			return entry.Entered, true
		}
	}
	return 0, false
}

// designFileVersion preserves absence separately from an empty (invalid) Go file.
func designFileVersion(design map[string]string, file string) string {
	if text, ok := design[file]; ok {
		return designversion.Digest(text)
	}
	return absentDesignVersion
}

// designPaths gives every known file a deterministic place in updates/refusals.
func designPaths(entries map[string][]madeFromVersion, designs ...map[string]string) []string {
	paths := map[string]bool{}
	for file := range entries {
		paths[file] = true
	}
	for _, design := range designs {
		for file := range design {
			paths[file] = true
		}
	}
	return slices.Sorted(maps.Keys(paths))
}
