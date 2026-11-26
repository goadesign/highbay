// This file checks stated relationships without compiling or interpreting tests.
package testwriter

import (
	"testing"

	"github.com/stretchr/testify/assert"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

// TestCoverageReferences checks membership and multiplicity at their pure owner.
func TestCoverageReferences(t *testing.T) {
	row := func(scenario string, questions, examples []genhighbay.TestCoverageReference) *genhighbay.TestCoverageStatement {
		return &genhighbay.TestCoverageStatement{Scenario: genhighbay.TestCoverageReference(scenario),
			QuestionIds: questions, ExampleIds: examples, Explanation: "The test exercises the stated interaction."}
	}
	q, e := []genhighbay.TestCoverageReference{"tax"}, []genhighbay.TestCoverageReference{"check_01"}
	for _, tc := range []struct {
		name string
		rows []*genhighbay.TestCoverageStatement
		want string
	}{
		{name: "not stated", rows: []*genhighbay.TestCoverageStatement{}},
		{name: "question", rows: []*genhighbay.TestCoverageStatement{row("refund", q, nil)}},
		{name: "example", rows: []*genhighbay.TestCoverageStatement{row("refund", nil, e)}},
		{name: "many scenarios per requirement", rows: []*genhighbay.TestCoverageStatement{row("refund", q, e), row("tax_boundary", q, nil)}},
		{name: "unknown question", rows: []*genhighbay.TestCoverageStatement{row("refund", []genhighbay.TestCoverageReference{"other"}, nil)}, want: `question "other" outside`},
		{name: "unknown example", rows: []*genhighbay.TestCoverageStatement{row("refund", nil, []genhighbay.TestCoverageReference{"other"})}, want: `example "other" outside`},
		{name: "duplicate scenario", rows: []*genhighbay.TestCoverageStatement{row("refund", q, nil), row("refund", nil, e)}, want: "more than one"},
		{name: "duplicate question", rows: []*genhighbay.TestCoverageStatement{row("refund", []genhighbay.TestCoverageReference{"tax", "tax"}, nil)}, want: `repeats question "tax"`},
		{name: "duplicate example", rows: []*genhighbay.TestCoverageStatement{row("refund", nil, []genhighbay.TestCoverageReference{"check_01", "check_01"})}, want: `repeats example "check_01"`},
		{name: "no requirement", rows: []*genhighbay.TestCoverageStatement{row("refund", nil, nil)}, want: "no question or example"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCoverageReferences(tc.rows,
				[]*genhighbay.ConfirmedAnswer{{ID: "tax", Question: "Does the limit include tax?", Answer: "No"}},
				[]*genhighbay.ExampleItem{{ID: "check_01"}})
			if tc.want != "" {
				assert.ErrorContains(t, err, tc.want)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
