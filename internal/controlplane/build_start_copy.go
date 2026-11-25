// This file binds build starts and the report checker to the working-copy
// and generation owners.
package controlplane

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/goadesign/highbay/internal/designeval"
	"github.com/goadesign/highbay/internal/scaffold"
	"github.com/goadesign/highbay/internal/testwriter"
)

type (
	// buildCopy is the narrow execution boundary of the test writer's working
	// copy: an assignment read prepares the copy on its branch, and the
	// report checker opens, prepares, checks and records it. The production
	// owner is testwriter; deterministic stand-ins exercise ordering and races.
	buildCopy interface {
		// Project returns the project's folder inside the copy.
		Project() string
		// Prepare writes the required tests for stories into the copy on branch
		// at base.
		Prepare(ctx context.Context, branch, base string, stories []designeval.StorySummary) error
		// Check judges the copy against stories on branch at base.
		Check(ctx context.Context, branch, base string, stories []designeval.StorySummary) (testwriter.Verdict, error)
		// Record commits an accepted verdict only on checkedTip of branch.
		Record(ctx context.Context, v testwriter.Verdict, branch, checkedTip, message string) (string, error)
	}
	// writerBuildCopy keeps one opened copy and the suite checked before opening.
	writerBuildCopy struct {
		copy    testwriter.WorkingCopy
		checker *testwriter.Checker
		suite   testwriter.Suite
	}
)

// openBuildCopy creates no second owner or stored copy of working-copy facts.
func (c *ControlPlane) openBuildCopy(
	ctx context.Context, workspace string, layout scaffold.Layout,
) (buildCopy, error) {
	if c.buildCopyFactory != nil {
		return c.buildCopyFactory(ctx, workspace, layout)
	}
	wc, ok, err := testwriter.OpenCopy(ctx, workspace, testwriter.TestsRole)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("the test writer's working copy does not exist")
	}
	return c.newWriterBuildCopy(wc, layout)
}

// newWriterBuildCopy binds an opened test-writer working copy to its checker
// and suite layout.
func (c *ControlPlane) newWriterBuildCopy(
	wc testwriter.WorkingCopy, layout scaffold.Layout,
) (buildCopy, error) {
	workDir := filepath.Join(c.cards.dir, "test-writer")
	var checker *testwriter.Checker
	if c.buildCacheDir != "" {
		var err error
		checker, err = testwriter.NewCheckerWithCache(c.evaluator, workDir, c.buildCacheDir)
		if err != nil {
			return nil, err
		}
	} else {
		checker = testwriter.NewChecker(c.evaluator, workDir)
	}
	return writerBuildCopy{
		copy:    wc,
		checker: checker,
		suite:   testwriter.Suite{Module: scaffold.SuiteDir, Command: layout.GeneratedSuiteCommand()},
	}, nil
}

// Project returns the project inside the copy, including a repository subfolder.
func (w writerBuildCopy) Project() string { return w.copy.Project() }

// Prepare writes the required tests from the saved Story through its owner.
func (w writerBuildCopy) Prepare(
	ctx context.Context, branch, base string, stories []designeval.StorySummary,
) error {
	_, err := w.checker.Prepare(ctx, w.copy, w.suite, branch, base, stories)
	return err
}

// Check judges the copy against the saved Story through its owner.
func (w writerBuildCopy) Check(
	ctx context.Context, branch, base string, stories []designeval.StorySummary,
) (testwriter.Verdict, error) {
	return w.checker.Check(ctx, w.copy, w.suite, branch, base, stories)
}

// Record commits an accepted verdict through its owner.
func (w writerBuildCopy) Record(
	ctx context.Context, v testwriter.Verdict, branch, checkedTip, message string,
) (string, error) {
	return testwriter.Record(ctx, v, branch, checkedTip, message)
}
