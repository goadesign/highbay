// This file supplies explicit saved cards for tests of downstream consumers.
// Public proposal and workspace-adoption tests use their real API producers.
package controlplane

import (
	"testing"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/require"
)

// saveProposedCardFixture stores an authored proposal without reading a
// workspace or claiming a refresh succeeded. Callers supply its owner and
// content; tests needing evaluated metadata add their explicit snapshot before
// confirmation. Existing cards are never overwritten.
func saveProposedCardFixture(
	t *testing.T, cp *ControlPlane, owner, changeID, prompt string, analysis *gensurla.CP1Analysis,
) *gensurla.ChangeCardResult {
	t.Helper()
	ambiguities, examples, preserved, err := proposedCardContent(changeID, analysis)
	require.NoError(t, err)
	card := &gensurla.ChangeCardResult{
		ChangeID: changeID, Status: StatusNeedsConfirmation, Prompt: prompt,
		Summary: analysis.Summary, Ambiguities: ambiguities, Examples: examples,
		PreservedBehaviors: preserved,
	}
	card.RequestRevision, err = computeCanonicalRevision(card, owner, "")
	require.NoError(t, err)
	if analysis.UIComposition != nil {
		card.UIComposition = &gensurla.UICompositionItem{
			Program: *analysis.UIComposition, ComposedForRevision: card.RequestRevision,
		}
	}
	cp.mu.Lock()
	defer cp.mu.Unlock()
	_, exists, err := cp.cards.load(changeID)
	require.NoError(t, err)
	require.False(t, exists, "the saved-card fixture must not replace another card")
	require.NoError(t, cp.cards.save(storedChangeCard{card: card, owner: owner}))
	return card
}
