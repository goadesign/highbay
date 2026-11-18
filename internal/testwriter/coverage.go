// This file validates the relationships the writer states, not assertion meaning.
package testwriter

import (
	"fmt"

	gensurla "github.com/goadesign/surla/gen/surla"
)

// ValidateCoverageReferences checks cross-field references against one built
// agreement. The caller owns structural decoding; empty statements are valid.
// Scenario existence is checked against the program listed by Checker.Check.
func ValidateCoverageReferences(
	statements []*gensurla.TestCoverageStatement,
	answers []*gensurla.ConfirmedAnswer, examples []*gensurla.ExampleItem,
) error {
	questions := make(map[gensurla.TestCoverageReference]bool, len(answers))
	for _, answer := range answers {
		questions[gensurla.TestCoverageReference(answer.ID)] = true
	}
	exampleIDs := make(map[gensurla.TestCoverageReference]bool, len(examples))
	for _, example := range examples {
		exampleIDs[gensurla.TestCoverageReference(example.ID)] = true
	}
	scenarios := make(map[gensurla.TestCoverageReference]bool, len(statements))
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
			ids   []gensurla.TestCoverageReference
			known map[gensurla.TestCoverageReference]bool
		}{
			{"question", statement.QuestionIds, questions},
			{"example", statement.ExampleIds, exampleIDs},
		} {
			seen := make(map[gensurla.TestCoverageReference]bool, len(group.ids))
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
