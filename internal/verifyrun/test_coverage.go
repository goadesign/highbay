// This file owns the generated-codec representation of frozen coverage evidence.
package verifyrun

import (
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/testwriter"
)

// TestCoverageSnapshot is one accepted report and its built agreement, encoded
// with the generated contract. Absence on StoryContext means not recorded.
// It is distinct from today's build and never changes after run acceptance.
type TestCoverageSnapshot genhighbay.RecordedTestCoverage

// MarshalJSON uses the owning generated codec rather than a second wire shape.
func (s TestCoverageSnapshot) MarshalJSON() ([]byte, error) {
	return genhighbay.EncodeRecordedTestCoverage((*genhighbay.RecordedTestCoverage)(&s))
}

// UnmarshalJSON validates stored structure and built-requirement membership.
func (s *TestCoverageSnapshot) UnmarshalJSON(raw []byte) error {
	record, err := genhighbay.DecodeRecordedTestCoverage(raw)
	if err != nil {
		return err
	}
	if err := testwriter.ValidateCoverageReferences(record.CoverageStatements, record.Answers, record.Examples); err != nil {
		return err
	}
	*s = TestCoverageSnapshot(*record)
	return nil
}
