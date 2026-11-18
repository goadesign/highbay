// This file binds writer statements to the project's built agreement and report.
package controlplane

import (
	"fmt"
	"slices"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/goadesign/surla/internal/verifyrun"
)

// cloneCoverageStatements transfers mutable request arrays to the report owner.
// Empty and historical absent statements have the same public value, [].
func cloneCoverageStatements(in []*gensurla.TestCoverageStatement) []*gensurla.TestCoverageStatement {
	out := make([]*gensurla.TestCoverageStatement, len(in))
	for i, statement := range in {
		copy := *statement
		copy.QuestionIds = slices.Clone(statement.QuestionIds)
		copy.ExampleIds = slices.Clone(statement.ExampleIds)
		out[i] = &copy
	}
	return out
}

// acceptedCoverageStatements never presents reported or refused rows as accepted.
func acceptedCoverageStatements(held heldBuild) []*gensurla.TestCoverageStatement {
	if held.storedEvaluator || held.file.TestWriter.State != testsAccepted {
		return []*gensurla.TestCoverageStatement{}
	}
	return cloneCoverageStatements(held.file.TestWriter.CoverageStatements)
}

// validateStoredCoverage uses generated structure checks at the persistence
// read, followed by membership checks against that same stored build's basis.
func validateStoredCoverage(saved buildFile) error {
	for _, statement := range saved.TestWriter.CoverageStatements {
		if _, err := gensurla.EncodeTestCoverageStatement(statement); err != nil {
			return fmt.Errorf("stored coverage statement: %w", err)
		}
	}
	if len(saved.TestWriter.CoverageStatements) == 0 {
		return nil
	}
	for _, answer := range saved.Answers {
		if _, err := gensurla.EncodeConfirmedAnswer(answer); err != nil {
			return fmt.Errorf("stored coverage answer: %w", err)
		}
	}
	for _, example := range saved.Examples {
		if _, err := gensurla.EncodeExampleItem(example); err != nil {
			return fmt.Errorf("stored coverage example: %w", err)
		}
	}
	return testwriter.ValidateCoverageReferences(saved.TestWriter.CoverageStatements, saved.Answers, saved.Examples)
}

// coverageScenarioFindings joins statements only to IDs from the checked tree.
// Extra declared public scenarios are valid; no second listing is performed.
func coverageScenarioFindings(
	statements []*gensurla.TestCoverageStatement, declared []string,
) []testwriter.Finding {
	var findings []testwriter.Finding
	for _, statement := range statements {
		if !slices.Contains(declared, string(statement.Scenario)) {
			findings = append(findings, testwriter.Finding{Kind: testwriter.FindingCoverageReference,
				Message: fmt.Sprintf("Your coverage statement names undeclared scenario %q. Correct coverage_statements in report_tests_finished and report again.", statement.Scenario)})
		}
	}
	return findings
}

// testCoverageSnapshot captures the accepted report at the tests-selection read.
// Its commit is carried by the same selection, rather than repeated in each row.
func testCoverageSnapshot(saved buildFile) *verifyrun.TestCoverageSnapshot {
	return &verifyrun.TestCoverageSnapshot{
		Report: saved.ReportNumber, Answers: saved.Answers, Examples: saved.Examples,
		CoverageStatements: cloneCoverageStatements(saved.TestWriter.CoverageStatements),
	}
}
