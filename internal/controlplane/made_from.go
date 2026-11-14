// This file owns persisted design provenance and content-addressed design text.
package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/goadesign/surla/internal/designversion"
	"github.com/goadesign/surla/internal/testwriter"
	"golang.org/x/sys/unix"
)

type (
	// madeFromSnapshot is the immutable provenance saved with a revision or build.
	// Mutable draft recovery state belongs only to madeFromRecord.
	madeFromSnapshot struct {
		// Start identifies the list generation in which this revision was drafted.
		Start madeFromStart `json:"start"`
		// Entries lists each file's distinct versions in entry order.
		Entries map[string][]madeFromVersion `json:"entries"`
	}

	// madeFromRecord is the guide draft's current provenance and recovery state.
	madeFromRecord struct {
		// Write is the last completed or pending list write's number.
		Write uint64 `json:"write"`
		// Start begins the current generation, including a drop when one caused it.
		Start madeFromStart `json:"start"`
		// Entries contains versions entered from the project, revisions or takes.
		Entries map[string][]madeFromVersion `json:"entries"`
		// Taken records the outside version each file most recently incorporated;
		// its Entered is the take time, not a later revision re-entry time.
		Taken map[string]madeFromVersion `json:"taken"`
		// Pending describes the single file write interrupted between list saves.
		Pending *pendingDraftWrite `json:"pending,omitempty"`
	}

	// madeFromStart identifies a provenance generation independently of later writes.
	madeFromStart struct {
		// Write is the list write that began this generation, starting at one.
		Write uint64 `json:"write"`
		// ProjectCommit is the starting project commit, or empty before its first commit.
		ProjectCommit string `json:"project_commit"`
		// Dropped names the build whose drop began this generation, when applicable.
		Dropped *droppedBuild `json:"dropped,omitempty"`
	}

	// droppedBuild preserves the identity and retained branch tips of one drop.
	droppedBuild struct {
		// ChangeID names the dropped build's change.
		ChangeID string `json:"change_id"`
		// StartedAt distinguishes a replacement build of the same change.
		StartedAt time.Time `json:"started_at"`
		// Number is the dropped build's positive branch number.
		Number int `json:"number"`
		// BranchTips records only this build's branches that existed at the drop.
		BranchTips map[string]string `json:"branch_tips"`
	}

	// madeFromVersion names one file version and when it last entered the list.
	madeFromVersion struct {
		// Version is a canonical text's digest, or none for an absent file.
		Version string `json:"version"`
		// Entered is the list write that most recently entered this version.
		Entered uint64 `json:"entered"`
	}

	// pendingDraftWrite lets a stopped update distinguish before, result and later edits.
	pendingDraftWrite struct {
		// File is the design-relative path whose replacement was prepared.
		File string `json:"file"`
		// OutsideVersion is the project version being taken, possibly absent.
		OutsideVersion string `json:"outside_version"`
		// Before is the draft version before the write, or none before an addition.
		Before string `json:"before"`
		// Result is the prepared replacement version, or none after a deletion.
		Result string `json:"result"`
	}
)

const (
	// absentDesignVersion represents absence without a content-addressed text file.
	absentDesignVersion = "none"
	// madeFromFileName and madeFromStagingFile are separate from card/build staging.
	madeFromFileName    = "made-from.json"
	madeFromStagingFile = "pending-made-from"
	// designVersionsDirectory holds immutable canonical texts named by their digest.
	designVersionsDirectory = "made-from"
	// designVersionStagingFile is private to serialized version-text writes.
	designVersionStagingFile = "pending-version"
)

// decodeMadeFrom strictly decodes a current provenance record and its ordering.
func decodeMadeFrom(data []byte) (madeFromRecord, error) {
	var saved madeFromRecord
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return madeFromRecord{}, fmt.Errorf("decode design provenance: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return madeFromRecord{}, errors.New("decode design provenance: trailing data")
	}
	if err := validateMadeFromRecord(saved); err != nil {
		return madeFromRecord{}, err
	}
	return saved, nil
}

// validateMadeFromSnapshot checks immutable history without comparing it with
// the guide's newer generation. Distinct versions may enter in the same write.
func validateMadeFromSnapshot(saved madeFromSnapshot) error {
	if saved.Start.Write == 0 {
		return errors.New("design provenance has no positive start write")
	}
	if saved.Start.ProjectCommit != "" && !commitIDPattern.MatchString(saved.Start.ProjectCommit) {
		return errors.New("design provenance has an invalid starting project commit")
	}
	if dropped := saved.Start.Dropped; dropped != nil {
		if dropped.ChangeID == "" || dropped.StartedAt.IsZero() || dropped.Number < 1 {
			return errors.New("design provenance has an incomplete dropped-build identity")
		}
		for branch, commit := range dropped.BranchTips {
			_, number, ok := testwriter.ParseBuildBranch(branch)
			if !ok || number != dropped.Number || !commitIDPattern.MatchString(commit) {
				return errors.New("design provenance has an invalid dropped-build branch tip")
			}
		}
	}
	for file, versions := range saved.Entries {
		if !validProvenancePath(file) || len(versions) == 0 {
			return fmt.Errorf("design provenance has no versions for a valid design file: %q", file)
		}
		seen := make(map[string]bool, len(versions))
		previous := saved.Start.Write
		for _, version := range versions {
			if !validDesignVersion(version.Version) || version.Entered < previous || seen[version.Version] {
				return fmt.Errorf("design provenance has invalid or unordered versions for %q", file)
			}
			seen[version.Version] = true
			previous = version.Entered
		}
	}
	return nil
}

// validateMadeFromRecord adds the current write bound and interrupted-take state.
func validateMadeFromRecord(saved madeFromRecord) error {
	if err := validateMadeFromSnapshot(madeFromSnapshot{Start: saved.Start, Entries: saved.Entries}); err != nil {
		return err
	}
	if saved.Write < saved.Start.Write {
		return errors.New("design provenance start follows its last write")
	}
	for file, versions := range saved.Entries {
		for _, version := range versions {
			if version.Entered > saved.Write {
				return fmt.Errorf("design provenance version of %q follows its last write", file)
			}
		}
	}
	for file, taken := range saved.Taken {
		if !validProvenancePath(file) || !validDesignVersion(taken.Version) ||
			taken.Entered < saved.Start.Write || taken.Entered > saved.Write {
			return fmt.Errorf("design provenance has an invalid taken version for %q", file)
		}
		found := false
		for _, version := range saved.Entries[file] {
			found = found || version.Version == taken.Version && taken.Entered <= version.Entered
		}
		if !found {
			return fmt.Errorf("design provenance taken version of %q is absent from its entries", file)
		}
	}
	if pending := saved.Pending; pending != nil {
		if !validProvenancePath(pending.File) || !validDesignVersion(pending.OutsideVersion) ||
			!validDesignVersion(pending.Before) || !validDesignVersion(pending.Result) {
			return errors.New("design provenance has an invalid pending draft write")
		}
	}
	return nil
}

// loadMadeFrom reads the guide record, with absence as a value and corruption as an error.
func (s *cardStore) loadMadeFrom() (madeFromRecord, bool, error) {
	if s.lock == nil {
		return madeFromRecord{}, false, errCardsClosed
	}
	f, err := os.OpenFile(filepath.Join(s.dir, madeFromFileName), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return madeFromRecord{}, false, nil
	}
	if err != nil {
		return madeFromRecord{}, false, fmt.Errorf("read design provenance: %w", err)
	}
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		return madeFromRecord{}, false, errors.Join(errors.New("record is not a regular file"), statErr, f.Close())
	}
	data, readErr := io.ReadAll(f)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return madeFromRecord{}, false, fmt.Errorf("read design provenance: %w", err)
	}
	saved, err := decodeMadeFrom(data)
	return saved, err == nil, err
}

// saveMadeFrom publishes one complete list after callers have written any new
// version texts. Callers hold draftMu; Drop also holds c.mu. It never rereads
// all historical blobs, which their individual consumers verify when loading.
func (s *cardStore) saveMadeFrom(saved madeFromRecord) error {
	if s.lock == nil {
		return errCardsClosed
	}
	if err := validateMadeFromRecord(saved); err != nil {
		return err
	}
	saved.Start = utcMadeFromStart(saved.Start)
	data, err := json.Marshal(saved)
	if err != nil {
		return fmt.Errorf("encode design provenance: %w", err)
	}
	staging := filepath.Join(s.dir, madeFromStagingFile)
	f, err := openRegularStaging(unix.AT_FDCWD, staging)
	if err == nil {
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
	}
	if err == nil {
		err = os.Rename(staging, filepath.Join(s.dir, madeFromFileName))
	}
	if err != nil {
		return fmt.Errorf("save design provenance: %w", err)
	}
	if err := syncDir(s.dir); err != nil {
		return fmt.Errorf("design provenance is saved but may not survive a crash: %w", err)
	}
	return nil
}

// loadDesignVersion returns absence only for none. A named digest is required
// to load, be canonical Go text and match its file name; corruption is not absence.
func (s *cardStore) loadDesignVersion(version string) (text string, present bool, retErr error) {
	if s.lock == nil {
		return "", false, errCardsClosed
	}
	if !validDesignVersion(version) {
		return "", false, errors.New("invalid design version")
	}
	if version == absentDesignVersion {
		return "", false, nil
	}
	dir, err := s.openDesignVersions(false)
	if err != nil {
		return "", false, fmt.Errorf("open design versions: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, dir.Close()) }()
	text, err = readDesignVersionFile(dir, version)
	if err != nil {
		return "", false, err
	}
	return text, true, nil
}

// saveDesignVersion durably writes canonical text before a provenance list can
// name its digest. Existing valid text is reused; corrupt text is never repaired
// implicitly. Callers serialize this write with draftMu.
func (s *cardStore) saveDesignVersion(text string) (version string, retErr error) {
	if s.lock == nil {
		return "", errCardsClosed
	}
	canonical, err := designversion.Canonical("design/version.go", text)
	if err != nil {
		return "", err
	}
	if canonical != text {
		return "", errors.New("design version text is not in gofmt form")
	}
	version = designversion.Digest(text)
	dir, err := s.openDesignVersions(true)
	if err != nil {
		return "", fmt.Errorf("open design versions: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, dir.Close()) }()
	if _, err := readDesignVersionFile(dir, version); err == nil {
		return version, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	f, err := openRegularStaging(int(dir.Fd()), designVersionStagingFile)
	if err != nil {
		return "", fmt.Errorf("stage design version: %w", err)
	}
	_, writeErr := f.WriteString(text)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return "", fmt.Errorf("write design version: %w", err)
	}
	if err := unix.Renameat(int(dir.Fd()), designVersionStagingFile, int(dir.Fd()), version); err != nil {
		return "", fmt.Errorf("publish design version: %w", err)
	}
	if err := dir.Sync(); err != nil {
		return "", fmt.Errorf("design version is saved but may not survive a crash: %w", err)
	}
	return version, nil
}

// openDesignVersions opens the directory without following a link at either
// the store or version directory. The returned descriptor owns subsequent paths.
func (s *cardStore) openDesignVersions(create bool) (*os.File, error) {
	parent, err := unix.Open(s.dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if create {
		if err := unix.Mkdirat(parent, designVersionsDirectory, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, errors.Join(err, unix.Close(parent))
		}
		if err := unix.Fsync(parent); err != nil {
			return nil, errors.Join(err, unix.Close(parent))
		}
	}
	fd, openErr := unix.Openat(parent, designVersionsDirectory,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err := errors.Join(openErr, unix.Close(parent)); err != nil {
		if openErr == nil {
			err = errors.Join(err, unix.Close(fd))
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), designVersionsDirectory), nil
}

// openRegularStaging never waits for a FIFO reader and never writes or
// truncates a non-regular descriptor. dir is either the held version directory
// or AT_FDCWD with an absolute path inside the card store.
func openRegularStaging(dir int, name string) (*os.File, error) {
	fd, err := unix.Openat(dir, name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(errors.New("staging path is not a regular file"), statErr, f.Close())
	}
	if err := f.Truncate(0); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

// readDesignVersionFile validates only the requested immutable blob. Nonblocking
// open permits rejecting a planted FIFO/device without waiting for its writer.
func readDesignVersionFile(dir *os.File, version string) (string, error) {
	fd, err := unix.Openat(int(dir.Fd()), version, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return "", fmt.Errorf("read design version %s: %w", version, err)
	}
	f := os.NewFile(uintptr(fd), version)
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		return "", errors.Join(errors.New("design version is not a regular file"), statErr, f.Close())
	}
	data, readErr := io.ReadAll(f)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return "", fmt.Errorf("read design version %s: %w", version, err)
	}
	text := string(data)
	canonical, err := designversion.Canonical("design/version.go", text)
	if err != nil {
		return "", err
	}
	if canonical != text || designversion.Digest(text) != version {
		return "", errors.New("design version text does not match its canonical digest")
	}
	return text, nil
}

// validDesignVersion keeps absent files distinct from their content-addressed text.
func validDesignVersion(version string) bool {
	return version == absentDesignVersion || cardFileDigestPattern.MatchString(version)
}

// validProvenancePath accepts only files the design walk can include.
func validProvenancePath(file string) bool {
	if !fs.ValidPath(file) || !strings.HasPrefix(file, "design/") ||
		!strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
		return false
	}
	for _, dir := range strings.Split(path.Dir(file), "/") {
		if strings.HasPrefix(dir, ".") || dir == "vendor" || dir == "testdata" {
			return false
		}
	}
	return true
}

// utcMadeFromStart normalizes persisted time without mutating a shared snapshot.
func utcMadeFromStart(start madeFromStart) madeFromStart {
	if start.Dropped != nil {
		dropped := *start.Dropped
		dropped.StartedAt = dropped.StartedAt.UTC()
		start.Dropped = &dropped
	}
	return start
}
