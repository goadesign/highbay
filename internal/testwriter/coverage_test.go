// This file checks stated relationships without compiling or interpreting tests.
package testwriter

import (
	"testing"

	"github.com/stretchr/testify/assert"

	gensurla "github.com/goadesign/surla/gen/surla"
)

// TestCoverageReferences checks membership and multiplicity at their pure owner.
func TestCoverageReferences(t *testing.T) {
	row := func(scenario string, questions, examples []gensurla.TestCoverageReference) *gensurla.TestCoverageStatement {
		return &gensurla.TestCoverageStatement{Scenario: gensurla.TestCoverageReference(scenario),
			QuestionIds: questions, ExampleIds: examples, Explanation: "The test exercises the stated interaction."}
	}
	q, e := []gensurla.TestCoverageReference{"tax"}, []gensurla.TestCoverageReference{"check_01"}
	for _, tc := range []struct {
		name string
		rows []*gensurla.TestCoverageStatement
		want string
	}{
		{name: "not stated", rows: []*gensurla.TestCoverageStatement{}},
		{name: "question", rows: []*gensurla.TestCoverageStatement{row("refund", q, nil)}},
		{name: "example", rows: []*gensurla.TestCoverageStatement{row("refund", nil, e)}},
		{name: "many scenarios per requirement", rows: []*gensurla.TestCoverageStatement{row("refund", q, e), row("tax_boundary", q, nil)}},
		{name: "unknown question", rows: []*gensurla.TestCoverageStatement{row("refund", []gensurla.TestCoverageReference{"other"}, nil)}, want: `question "other" outside`},
		{name: "unknown example", rows: []*gensurla.TestCoverageStatement{row("refund", nil, []gensurla.TestCoverageReference{"other"})}, want: `example "other" outside`},
		{name: "duplicate scenario", rows: []*gensurla.TestCoverageStatement{row("refund", q, nil), row("refund", nil, e)}, want: "more than one"},
		{name: "duplicate question", rows: []*gensurla.TestCoverageStatement{row("refund", []gensurla.TestCoverageReference{"tax", "tax"}, nil)}, want: `repeats question "tax"`},
		{name: "duplicate example", rows: []*gensurla.TestCoverageStatement{row("refund", nil, []gensurla.TestCoverageReference{"check_01", "check_01"})}, want: `repeats example "check_01"`},
		{name: "no requirement", rows: []*gensurla.TestCoverageStatement{row("refund", nil, nil)}, want: "no question or example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCoverageReferences(tc.rows,
				[]*gensurla.ConfirmedAnswer{{ID: "tax", Question: "Does the limit include tax?", Answer: "No"}},
				[]*gensurla.ExampleItem{{ID: "check_01"}})
			if tc.want != "" {
				assert.ErrorContains(t, err, tc.want)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
