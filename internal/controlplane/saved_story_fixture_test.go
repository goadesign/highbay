// This file binds Story consumer fixtures to verified source and module inputs.
package controlplane

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/testmodule"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/modfile"
)

// refundStoryFixtureForTest reads the source and full summaries verified by the
// existing designeval Story-only journey, with its committed dependency profile.
func refundStoryFixtureForTest(t *testing.T) (map[string]string, []designeval.StorySummary) {
	t.Helper()
	dataDir := filepath.Join("..", "designeval", "testdata")
	source, err := os.ReadFile(filepath.Join(dataDir, "refund_story.go.txt"))
	require.NoError(t, err)
	data, err := os.ReadFile(filepath.Join(dataDir, "refund_story.json"))
	require.NoError(t, err)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var stories []designeval.StorySummary
	require.NoError(t, decoder.Decode(&stories))
	require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)

	profileDir := filepath.Join("..", "project", "testdata", "story-fixture")
	files := map[string]string{"design/design.go": string(source)}
	for _, name := range []string{"go.mod", "go.sum"} {
		content, err := os.ReadFile(filepath.Join(profileDir, name))
		require.NoError(t, err)
		files[name] = string(content)
	}
	module, err := modfile.Parse("go.mod", []byte(files["go.mod"]), nil)
	require.NoError(t, err)
	require.Len(t, module.Require, 2, "the Story fixture retains only its Story and Goa requirements")
	for _, dependency := range module.Require {
		require.Equal(t, testmodule.SurlaVersion(t, dependency.Mod.Path), dependency.Mod.Version,
			"update the committed fixture profile after changing root requirements")
	}
	return files, stories
}
