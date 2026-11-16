// This file owns immutable Story context on accepted verification requests.
package verifyrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/goadesign/surla/internal/designeval"
)

type (
	// StoryContext records the agreement of the build accepted for a run.
	// The zero value means not recorded, including requests written before this
	// context existed and standalone CLI runs without a Change Card build.
	StoryContext struct {
		// State distinguishes absent historical evidence from a recorded empty Story.
		State StoryContextState `json:"state"`
		// BuildNumber identifies the accepted numbered build when recorded.
		BuildNumber int `json:"build_number,omitempty"`
		// BuildStartedAt distinguishes later starts of the same agreement.
		BuildStartedAt time.Time `json:"build_started_at,omitzero"`
		// Stories is the immutable agreed Story; an empty recorded list is valid.
		Stories []designeval.StorySummary `json:"stories,omitempty"`
	}

	// StoryContextState is a closed persistence enum; zero has explicit meaning.
	StoryContextState uint8
)

const (
	// StoryNotRecorded means the run carries no Story snapshot.
	StoryNotRecorded StoryContextState = iota
	// StoryRecorded means the run carries the accepted build's Story snapshot.
	StoryRecorded
)

// MarshalJSON always writes the explicit state, including for a zero context.
func (s StoryContextState) MarshalJSON() ([]byte, error) {
	switch s {
	case StoryNotRecorded:
		return []byte(`"not_recorded"`), nil
	case StoryRecorded:
		return []byte(`"recorded"`), nil
	default:
		return nil, fmt.Errorf("unknown Story context state %d", s)
	}
}

// UnmarshalJSON rejects undeclared storage states rather than guessing evidence.
func (s *StoryContextState) UnmarshalJSON(raw []byte) error {
	var state string
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	switch state {
	case "not_recorded":
		*s = StoryNotRecorded
	case "recorded":
		*s = StoryRecorded
	default:
		return fmt.Errorf("unknown Story context state %q", state)
	}
	return nil
}

// UnmarshalJSON validates explicitly present context at the storage boundary.
// An absent request field leaves the zero value; a present malformed value fails.
func (s *StoryContext) UnmarshalJSON(raw []byte) error {
	type plain StoryContext
	value := struct {
		*plain
		State *StoryContextState `json:"state"`
	}{plain: new(plain)}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Story context has trailing data")
	}
	if value.State == nil {
		return errors.New("Story context state is required")
	}
	value.plain.State = *value.State
	*s = StoryContext(*value.plain)
	return s.check()
}

// check enforces build binding and forbids evidence on a not-recorded context.
func (s StoryContext) check() error {
	switch s.State {
	case StoryNotRecorded:
		if s.BuildNumber != 0 || !s.BuildStartedAt.IsZero() || len(s.Stories) != 0 {
			return errors.New("not-recorded Story context must not carry build evidence")
		}
	case StoryRecorded:
		if s.BuildNumber < 1 || s.BuildStartedAt.IsZero() {
			return errors.New("recorded Story context requires its build number and start")
		}
		if _, _, err := designeval.StoryScenarios(s.Stories); err != nil {
			return fmt.Errorf("recorded Story context: %w", err)
		}
	default:
		return fmt.Errorf("unknown Story context state %d", s.State)
	}
	return nil
}
