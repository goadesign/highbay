// This file binds writer statements to the project's built agreement and report.
package controlplane

import (
	"fmt"
	"slices"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/testwriter"
	"github.com/goadesign/highbay/internal/verifyrun"
)

// cloneCoverageStatements transfers mutable request arrays to the report owner.
// Empty and historical absent statements have the same public value, [].
func cloneCoverageStatements(in []*genhighbay.TestCoverageStatement) []*genhighbay.TestCoverageStatement {
	out := make([]*genhighbay.TestCoverageStatement, len(in))
	for i, statement := range in {
		copy := *statement
		copy.QuestionIds = slices.Clone(statement.QuestionIds)
		copy.ExampleIds = slices.Clone(statement.ExampleIds)
		out[i] = &copy
	}
	return out
}

// acceptedCoverageStatements never presents reported or refused rows as accepted.
func acceptedCoverageStatements(held heldBuild) []*genhighbay.TestCoverageStatement {
	if held.storedEvaluator || held.file.TestWriter.State != testsAccepted {
		return []*genhighbay.TestCoverageStatement{}
	}
	return cloneCoverageStatements(held.file.TestWriter.CoverageStatements)
}

// validateStoredCoverage uses generated structure checks at the persistence
// read, followed by membership checks against that same stored build's basis.
func validateStoredCoverage(saved buildFile) error {
	for _, statement := range saved.TestWriter.CoverageStatements {
		if _, err := genhighbay.EncodeTestCoverageStatement(statement); err != nil {
			return fmt.Errorf("stored coverage statement: %w", err)
		}
	}
	if len(saved.TestWriter.CoverageStatements) == 0 {
		return nil
	}
	for _, answer := range saved.Answers {
		if _, err := genhighbay.EncodeConfirmedAnswer(answer); err != nil {
			return fmt.Errorf("stored coverage answer: %w", err)
		}
	}
	for _, example := range saved.Examples {
		if _, err := genhighbay.EncodeExampleItem(example); err != nil {
			return fmt.Errorf("stored coverage example: %w", err)
		}
	}
	return testwriter.ValidateCoverageReferences(saved.TestWriter.CoverageStatements, saved.Answers, saved.Examples)
}

// coverageScenarioFindings joins statements only to IDs from the checked tree.
// Extra declared public scenarios are valid; no second listing is performed.
func coverageScenarioFindings(
	statements []*genhighbay.TestCoverageStatement, declared []string,
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
