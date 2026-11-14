// This file owns the numbered project-wide build record introduced by builder copies.
package controlplane

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/designversion"
)

type (
	// openBuildFile is the project's numbered build, including a snapshot of
	// the agreed revision. Legacy per-change records stay separate until cutover.
	openBuildFile struct {
		// ChangeID and RequestRevision identify the change and agreed revision built.
		ChangeID        string `json:"change_id"`
		RequestRevision string `json:"request_revision"`
		// StartedAt distinguishes repeated starts of the same numbered build.
		StartedAt time.Time `json:"started_at"`
		// Roles is the exact builder-only or builder-and-test-writer choice at start.
		Roles string `json:"roles"`
		// Stories and DesignSource are immutable agreed inputs copied at start.
		Stories      []designeval.StorySummary `json:"stories"`
		DesignSource string                    `json:"design_source"`
		// Answers, Examples and PreservedBehaviors are the terms tests must respect.
		Answers            []*gensurla.ConfirmedAnswer `json:"answers"`
		Examples           []*gensurla.ExampleItem     `json:"examples"`
		PreservedBehaviors []string                    `json:"preserved_behaviors"`
		// Builder and TestWriter retain assignment reads and the current verdict.
		Builder    builderAssignment        `json:"builder"`
		TestWriter openTestWriterAssignment `json:"test_writer"`
		// ReportNumber distinguishes test reports within this start.
		ReportNumber int `json:"report_number"`
		// Number names this build's surla/build-N and surla/tests-N branches.
		Number int `json:"number"`
		// Base is the project commit from which this build's branches begin.
		Base string `json:"base"`
		// GoMod is the module text evaluated with the agreed design.
		GoMod string `json:"go_mod"`
		// MadeFrom is the immutable provenance evaluated with the agreed design.
		MadeFrom madeFromSnapshot `json:"made_from"`
	}

	// openTestWriterAssignment derives its folder from the numbered build,
	// rather than storing a path from an older checkout.
	openTestWriterAssignment struct {
		// State selects exactly the verdict fields permitted below.
		State string `json:"state"`
		// Read records that the writer read this assignment in full.
		Read bool `json:"read"`
		// Findings is nonempty exactly when State is needs_changes.
		Findings []string `json:"findings"`
		// Commit identifies accepted tests exactly when State is accepted.
		Commit string `json:"commit"`
		// Reason and Step are populated exactly when State is could_not_run.
		Reason string `json:"reason,omitzero"`
		Step   string `json:"step,omitzero"`
		// CheckedAt is nonzero exactly in one of the three verdict states.
		CheckedAt time.Time `json:"checked_at,omitzero"`
	}
)

const (
	// openBuildRecordFile is excluded from the card store's hashed-card enumeration.
	openBuildRecordFile = "build.json"
	// openBuildStagingFile is not shared with cards, provenance or version text.
	openBuildStagingFile = "pending-open-build"
)

// decodeOpenBuild rejects unknown fields, trailing data and inconsistent build
// states. It does not apply the legacy filename or stored-folder contract.
func decodeOpenBuild(data []byte) (openBuildFile, error) {
	var saved openBuildFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&saved); err != nil {
		return openBuildFile{}, fmt.Errorf("decode open build: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return openBuildFile{}, errors.New("decode open build: trailing data")
	}
	if err := validateOpenBuild(saved); err != nil {
		return openBuildFile{}, err
	}
	return saved, nil
}

// validateOpenBuild preserves the legacy state/report/time invariants and adds
// the numbered build's base and immutable provenance.
func validateOpenBuild(saved openBuildFile) error {
	if saved.ChangeID == "" || saved.RequestRevision == "" || saved.StartedAt.IsZero() {
		return errors.New("open build has no change, revision or start time")
	}
	if saved.Number < 1 || !commitIDPattern.MatchString(saved.Base) {
		return errors.New("open build has no positive number or valid base commit")
	}
	if _, err := designversion.Requirements(saved.GoMod); err != nil {
		return fmt.Errorf("open build has invalid agreed module text: %w", err)
	}
	if err := validateMadeFromSnapshot(saved.MadeFrom); err != nil {
		return fmt.Errorf("open build: %w", err)
	}
	if saved.ReportNumber < 0 {
		return errors.New("open build has a negative report number")
	}
	if saved.Roles != buildRolesBuilderOnly && saved.Roles != buildRolesBuilderAndTestWriter {
		return errors.New("open build has unknown roles")
	}
	tw := saved.TestWriter
	noVerdictFields := tw.Reason == "" && tw.Step == ""
	var valid, verdict bool
	switch tw.State {
	case testsAssigned, testsReported:
		valid = len(tw.Findings) == 0 && tw.Commit == "" && noVerdictFields
	case testsNeedChanges:
		valid, verdict = len(tw.Findings) > 0 && tw.Commit == "" && noVerdictFields, true
	case testsAccepted:
		valid, verdict = len(tw.Findings) == 0 && commitIDPattern.MatchString(tw.Commit) && noVerdictFields, true
	case testsCouldNotRun:
		valid = len(tw.Findings) == 0 && tw.Commit == "" &&
			boundedText(tw.Reason, maxCheckReason) && boundedText(tw.Step, maxCheckStep)
		verdict = true
	default:
		return errors.New("open build has an unknown test-writer state")
	}
	if !valid || verdict == tw.CheckedAt.IsZero() {
		return errors.New("open build test-writer fields or verdict time do not match its state")
	}
	if tw.State == testsAssigned && saved.ReportNumber != 0 ||
		tw.State == testsReported && saved.ReportNumber == 0 {
		return errors.New("open build report number does not match its test-writer state")
	}
	return nil
}

// loadOpenBuild returns absence as a value; malformed or unreadable records fail.
func (s *cardStore) loadOpenBuild() (openBuildFile, bool, error) {
	if s.lock == nil {
		return openBuildFile{}, false, errCardsClosed
	}
	f, err := os.OpenFile(filepath.Join(s.dir, openBuildRecordFile), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return openBuildFile{}, false, nil
	}
	if err != nil {
		return openBuildFile{}, false, fmt.Errorf("read open build: %w", err)
	}
	info, statErr := f.Stat()
	if statErr != nil || !info.Mode().IsRegular() {
		return openBuildFile{}, false, errors.Join(errors.New("record is not a regular file"), statErr, f.Close())
	}
	data, readErr := io.ReadAll(f)
	if err := errors.Join(readErr, f.Close()); err != nil {
		return openBuildFile{}, false, fmt.Errorf("read open build: %w", err)
	}
	saved, err := decodeOpenBuild(data)
	return saved, err == nil, err
}

// saveOpenBuild atomically replaces the numbered record. Callers hold c.mu;
// errors before rename preserve the previous record, and a directory-sync error
// explicitly reports that publication happened but crash durability is unknown.
func (s *cardStore) saveOpenBuild(saved openBuildFile) error {
	if s.lock == nil {
		return errCardsClosed
	}
	if err := validateOpenBuild(saved); err != nil {
		return err
	}
	saved.StartedAt = saved.StartedAt.UTC()
	saved.TestWriter.CheckedAt = saved.TestWriter.CheckedAt.UTC()
	saved.MadeFrom.Start = utcMadeFromStart(saved.MadeFrom.Start)
	data, err := json.Marshal(saved)
	if err != nil {
		return fmt.Errorf("encode open build: %w", err)
	}
	staging := filepath.Join(s.dir, openBuildStagingFile)
	f, err := os.OpenFile(staging, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, 0o600)
	if err == nil {
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
	}
	if err == nil {
		err = os.Rename(staging, filepath.Join(s.dir, openBuildRecordFile))
	}
	if err != nil {
		return fmt.Errorf("save open build: %w", err)
	}
	if err := syncDir(s.dir); err != nil {
		return fmt.Errorf("open build is saved but may not survive a crash: %w", err)
	}
	return nil
}
