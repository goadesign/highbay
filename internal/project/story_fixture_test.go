// This file prepares only the fixed succeeded-Story fixture from pinned
// checksums and cached modules, without proxy or checksum-server access.
package project

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/mod/modfile"

	"github.com/goadesign/highbay/internal/testmodule"
)

// TestStoryFixtureNeedsCachedModules makes missing module bytes fail at setup
// with a fixture-input diagnosis, rather than waiting for the network.
func TestStoryFixtureNeedsCachedModules(t *testing.T) {
	disableStoryFixtureFetching(t)
	t.Setenv("GOMODCACHE", t.TempDir())
	dir := storyWorkspace(t, succeededStoryInput(t))
	err := prepareOfflineStoryFixture(t.Context(), dir)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fixture input incomplete")
	assert.Contains(t, err.Error(), "GOPROXY=off")
}

// disableStoryFixtureFetching applies to preparation and its real evaluator.
// GONOPROXY=none prevents private-module settings from bypassing proxy off.
func disableStoryFixtureFetching(t *testing.T) {
	t.Helper()
	for name, value := range map[string]string{
		"GOPROXY": "off", "GOSUMDB": "off", "GONOPROXY": "none",
		"GOENV": "off", "GOTOOLCHAIN": "local",
	} {
		t.Setenv(name, value)
	}
}

// succeededStoryFixture returns complete immutable module values; every
// workspace gets its own files and no temporary directory is retained.
func succeededStoryFixture(t *testing.T) map[string]string {
	t.Helper()
	files := succeededStoryInput(t)
	dir := storyWorkspace(t, files)
	require.NoError(t, prepareOfflineStoryFixture(t.Context(), dir))
	for _, name := range []string{"go.mod", "go.sum"} {
		content, err := os.ReadFile(filepath.Join(dir, name))
		require.NoError(t, err)
		files[name] = string(content)
	}
	return files
}

// prepareOfflineStoryFixture checks this exact graph from cached bytes.
// Its error names missing fixture inputs instead of a remote setup timeout.
func prepareOfflineStoryFixture(ctx context.Context, dir string) error {
	for _, args := range [][]string{
		{"list", "-mod=readonly", "-deps", "./design/..."},
		{"list", "-mod=readonly", "-m", "all"},
	} {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=",
			"GOENV=off", "GOPROXY=off", "GOSUMDB=off", "GONOPROXY=none")
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("fixture input incomplete: prepare offline Story module from the root module cache: %w\n%s", err, output)
		}
	}
	return nil
}

// succeededStoryInput keeps the original pinned requirements and exact design.
// The committed module pair supplies its checksums without unrelated requirements.
func succeededStoryInput(t *testing.T) map[string]string {
	t.Helper()
	profilePath := filepath.Join("testdata", "story-fixture", "go.mod")
	profile, err := os.ReadFile(profilePath)
	require.NoError(t, err)
	module, err := modfile.Parse(profilePath, profile, nil)
	require.NoError(t, err)
	require.Len(t, module.Require, 2, "fixture input incomplete: retain the Story and Goa profile")
	for _, dependency := range module.Require {
		require.Equal(t, testmodule.HighbayVersion(t, dependency.Mod.Path), dependency.Mod.Version,
			"fixture input incomplete: update the prepared Story profile after changing root requirements")
	}
	seed, err := os.ReadFile(filepath.Join("testdata", "story-fixture", "go.sum"))
	require.NoError(t, err)
	files := map[string]string{
		"go.mod": string(profile),
		"design/design.go": `package design

import . "github.com/goadesign/highbay/story/dsl"

var _ = Story("Order Refunds", func() {
	Actor("customer", "Customer")
	Actor("manager", "Support Manager")

	Flow("instant-refund", "Instant refund under the limit", func() {
		StartsWith("request-refund", "customer", "Customer requests a $25 refund")
		Outcome("refund-paid", "customer", "Store credit is issued immediately")
	})

	Flow("high-value-refund", "Refund over the limit", func() {
		StartsWith("request-high-refund", "customer", "Customer requests a $120 refund")
		Step("review-refund", "manager", "Manager reviews the refund request")
	})
})
`,
	}
	files["go.sum"] = string(seed)
	return files
}
