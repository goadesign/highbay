// These tests keep the cold module-cache primer aligned with testmodule's
// synthetic fixture versions and sumdb-priming requirements.
package main

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/modfile"

	"github.com/goadesign/highbay/internal/testmodule"
)

// TestPrimeProfilesCoverAllFixtureVersionsWithoutInitialGoSum verifies that
// every synthetic primer profile parses as valid Go and go.mod source, omits
// an initial go.sum so go mod tidy populates sumdb tiles, and includes every
// pinned fixture module version.
func TestPrimeProfilesCoverAllFixtureVersionsWithoutInitialGoSum(t *testing.T) {
	profiles, err := testmodule.PrimeProfiles()
	require.NoError(t, err)
	require.NotEmpty(t, profiles)

	var allGoMods strings.Builder
	for _, p := range profiles {
		require.NotEmpty(t, p.Name)
		assert.NotContains(t, p.Files, "go.sum", "%s must omit go.sum so go mod tidy populates sumdb", p.Name)
		rawMod, ok := p.Files["go.mod"]
		require.True(t, ok, "%s must include go.mod", p.Name)
		_, err := modfile.Parse("go.mod", []byte(rawMod), nil)
		require.NoError(t, err, "%s go.mod", p.Name)
		allGoMods.WriteString(rawMod)
		allGoMods.WriteByte('\n')

		rawGo, ok := p.Files["design/design.go"]
		require.True(t, ok, "%s must include design/design.go", p.Name)
		_, err = parser.ParseFile(token.NewFileSet(), "design.go", rawGo, parser.ImportsOnly)
		require.NoError(t, err, "%s design.go", p.Name)
	}

	combined := allGoMods.String()
	for _, want := range []string{
		"github.com/goadesign/highbay " + testmodule.PublishedHighbayVersion,
		"github.com/goadesign/highbay/story " + testmodule.HighbayVersion(t, "github.com/goadesign/highbay/story"),
		"goa.design/goa/v3 " + testmodule.HighbayVersion(t, "goa.design/goa/v3"),
		"goa.design/goa/v3 " + testmodule.CompleteSourcesGoaVersion,
		"goa.design/goa/v3 " + testmodule.StarterPreCompleteSourcesGoaVersion,
		"goa.design/goa/v3 " + testmodule.MinimumGoaAIGoaVersion,
		"goa.design/goa/v3 " + testmodule.OlderThanGoaAIGoaVersion,
		"goa.design/goa-ai " + testmodule.HighbayVersion(t, "goa.design/goa-ai"),
		"goa.design/model " + testmodule.HighbayVersion(t, "goa.design/model"),
		"rsc.io/binaryregexp " + testmodule.BinaryRegexpVersion,
		"rsc.io/qr " + testmodule.QRVersion,
		"github.com/kr/text " + testmodule.KRTextVersion,
	} {
		assert.Contains(t, combined, want)
	}
}
