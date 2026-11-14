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
	head string
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
	h := designHistory{head: head, versions: map[string]map[string]string{}, gained: map[string]bool{}}
	if head == "" {
		return h, nil
	}
	repo, prefix, err := resolveStarterRepo(ctx, dir)
	if err != nil {
		return h, err
	}
	out, err := runGitRead(ctx, repo, "rev-list", "--parents", head)
	if err != nil {
		return h, err
	}
	parents := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			parents[fields[0]] = fields[1:]
		}
	}
	excluded := commitAncestors(parents, saved.Start.ProjectCommit)
	if saved.Start.ProjectCommit != "" {
		// S can be outside the new head after a rewrite; its ancestry must
		// still be excluded rather than silently treating it as unborn.
		old, err := runGitRead(ctx, repo, "rev-list", saved.Start.ProjectCommit)
		if err != nil {
			return h, err
		}
		for _, commit := range strings.Fields(string(old)) {
			excluded[commit] = true
		}
	}
	for _, commit := range slices.Sorted(maps.Keys(parents)) {
		if excluded[commit] && commit != head {
			continue
		}
		design, err := committedDesignAt(ctx, dir, repo, prefix, commit)
		if err != nil {
			return h, err
		}
		h.versions[commit] = design
	}
	for file, take := range taken {
		before := commitAncestors(parents, take.ProjectCommit)
		if _, present := parents[take.ProjectCommit]; take.ProjectCommit != "" && !present {
			old, err := runGitRead(ctx, repo, "rev-list", take.ProjectCommit)
			if err != nil {
				return h, err
			}
			for _, commit := range strings.Fields(string(old)) {
				before[commit] = true
			}
		}
		// Cache this file's versions independently: older history can hold
		// the taken version even when S no longer holds it. A historical text
		// that cannot parse cannot equal any canonical listed version.
		fileVersions := map[string]string{}
		for _, commit := range slices.Sorted(maps.Keys(parents)) {
			text, found, err := readGitBlob(ctx, repo, commit, prefix+file)
			if err != nil {
				return h, err
			}
			version := absentDesignVersion
			if found {
				canonical, parseErr := designversion.Canonical(file, string(text))
				version = "unparsable"
				if parseErr == nil {
					version = designversion.Digest(canonical)
				}
			}
			fileVersions[commit] = version
		}
		for commit, design := range h.versions {
			if excluded[commit] || before[commit] {
				continue
			}
			version := designFileVersion(design, file)
			entered, listed := enteredVersion(saved.Entries[file], version)
			if !listed || version == take.Version || entered >= take.Entered {
				continue
			}
			descendsTake := false
			for ancestor := range commitAncestors(parents, commit) {
				if fileVersions[ancestor] == take.Version {
					descendsTake = true
					break
				}
			}
			if !descendsTake {
				h.gained[file] = true
				break
			}
		}
	}
	// Currentness excludes S's history, even though head's snapshot is needed
	// by the base check when head == S. Keep that distinction explicit.
	h.design = h.versions[head]
	if excluded[head] {
		delete(h.versions, head)
	}
	return h, nil
}

// current compares entry order, including a committed revert to an older version.
func (h designHistory) current(file, version string, entries []madeFromVersion) bool {
	entered, found := enteredVersion(entries, version)
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
	entered, _ := enteredVersion(list.Entries[file], base)
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

// commitAncestors derives reachability from one bounded --parents result.
func commitAncestors(parents map[string][]string, commit string) map[string]bool {
	seen := map[string]bool{}
	queue := []string{commit}
	for len(queue) > 0 {
		last := len(queue) - 1
		current := queue[last]
		queue = queue[:last]
		if current == "" || seen[current] {
			continue
		}
		seen[current] = true
		queue = append(queue, parents[current]...)
	}
	return seen
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
