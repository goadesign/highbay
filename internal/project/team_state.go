// This file names the private project state a team operation could not use,
// so the views page gives a repair for that one file instead of a blanket
// permission change across the project's state folder.
package project

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type (
	// teamStateFailure is a failure to use one private state path: the
	// project record, an agent credential or the agents folder. It carries the
	// path and whether Surla was writing it, never the file's contents.
	teamStateFailure struct {
		// path is the file or folder that could not be used.
		path string
		// dir is true when path is a folder.
		dir bool
		// write is true when Surla was writing path rather than reading it.
		write bool
		// err is the underlying failure; it stays out of user text.
		err error
	}

	// teamStateUnavailable is a project_state team failure together with the
	// views page's repair for it. It unwraps to the typed TeamUnavailable, so
	// every other boundary still sees only the cause.
	teamStateUnavailable struct {
		// unavailable is the typed project_state failure.
		unavailable error
		// repair is the page's step: the affected path and what fixes it.
		repair string
	}
)

// Error names the path and the underlying failure for logs and spans.
func (f *teamStateFailure) Error() string { return f.path + ": " + f.err.Error() }

// Unwrap exposes the underlying failure for classification.
func (f *teamStateFailure) Unwrap() error { return f.err }

// Error is the typed failure's text.
func (e *teamStateUnavailable) Error() string { return e.unavailable.Error() }

// Unwrap exposes the typed TeamUnavailable.
func (e *teamStateUnavailable) Unwrap() error { return e.unavailable }

// stateUnavailable records a project_state failure of step and keeps the page's
// repair for err, which names the affected path when err is a teamStateFailure.
func stateUnavailable(ctx context.Context, step string, err error) error {
	return &teamStateUnavailable{unavailable: teamUnavailable(ctx, step, "project_state"), repair: stateRepair(err)}
}

// recordWriteFailure names the project record as the path a failed save could
// not write.
func (s *Server) recordWriteFailure(err error) error {
	return &teamStateFailure{path: filepath.Join(s.state.Dir, "project.json"), write: true, err: err}
}

// stateRepair is the page's step for a project_state failure. A failed write
// names the folder to make writable. A path that failed Surla's own privacy
// check is fixed on that path alone; a read the system refused, for example
// through a folder this account cannot open, names the folders that hold it.
// A missing or damaged file is restored from a backup. A failure that names no
// state file, such as an unreadable build assignment, is retried as it is.
func stateRepair(err error) string {
	var failure *teamStateFailure
	if !errors.As(err, &failure) {
		return "this project's saved build state could not be read; try again"
	}
	mode, kind := "600", "file"
	if failure.dir {
		mode, kind = "700", "folder"
	}
	switch {
	case failure.write:
		return fmt.Sprintf("Surla could not write %s; make sure this account can write the folder %s and the disk has free space, then try again", failure.path, filepath.Dir(failure.path))
	case errors.Is(err, errNotPrivateFile), errors.Is(err, errNotPrivateFolder):
		return fmt.Sprintf("Surla could not use %s because of its permissions or owner; make it a %s of this account that only this account can use (chmod %s %s, and chown it to this account if another account owns it), then try again", failure.path, kind, mode, failure.path)
	case errors.Is(err, os.ErrPermission):
		return fmt.Sprintf("Surla could not open %s; make sure this account can read it and open the folders that hold it, starting at %s, then try again", failure.path, filepath.Dir(failure.path))
	case errors.Is(err, os.ErrNotExist):
		return fmt.Sprintf("%s is missing; restore it from a backup of this project's Surla state, then try again", failure.path)
	default:
		return fmt.Sprintf("%s is damaged; restore it from a backup of this project's Surla state, then try again", failure.path)
	}
}
