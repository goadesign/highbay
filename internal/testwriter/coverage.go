// This file validates the relationships the writer states, not assertion meaning.
package testwriter

import (
	"fmt"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

// ValidateCoverageReferences checks cross-field references against one built
// agreement. The caller owns structural decoding; empty statements are valid.
// Scenario existence is checked against the program listed by Checker.Check.
func ValidateCoverageReferences(
	statements []*genhighbay.TestCoverageStatement,
	answers []*genhighbay.ConfirmedAnswer, examples []*genhighbay.ExampleItem,
) error {
	questions := make(map[genhighbay.TestCoverageReference]bool, len(answers))
	for _, answer := range answers {
		questions[genhighbay.TestCoverageReference(answer.ID)] = true
	}
	exampleIDs := make(map[genhighbay.TestCoverageReference]bool, len(examples))
	for _, example := range examples {
		exampleIDs[genhighbay.TestCoverageReference(example.ID)] = true
	}
	scenarios := make(map[genhighbay.TestCoverageReference]bool, len(statements))
	for _, statement := range statements {
		if scenarios[statement.Scenario] {
			return fmt.Errorf("scenario %q has more than one coverage statement", statement.Scenario)
		}
		scenarios[statement.Scenario] = true
		if len(statement.QuestionIds)+len(statement.ExampleIds) == 0 {
			return fmt.Errorf("scenario %q states no question or example reference", statement.Scenario)
		}
		for _, group := range []struct {
			name  string
			ids   []genhighbay.TestCoverageReference
			known map[genhighbay.TestCoverageReference]bool
		}{
			{"question", statement.QuestionIds, questions},
			{"example", statement.ExampleIds, exampleIDs},
		} {
			seen := make(map[genhighbay.TestCoverageReference]bool, len(group.ids))
			for _, id := range group.ids {
				if !group.known[id] {
					return fmt.Errorf("scenario %q names %s %q outside the built agreement", statement.Scenario, group.name, id)
				}
				if seen[id] {
					return fmt.Errorf("scenario %q repeats %s %q", statement.Scenario, group.name, id)
				}
				seen[id] = true
			}
		}
	}
	return nil
}
