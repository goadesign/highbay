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

	"github.com/goadesign/surla/internal/testmodule"
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

// prepareOfflineStoryFixture completes this exact graph from cached bytes.
// Its error names missing fixture inputs instead of a remote setup timeout.
func prepareOfflineStoryFixture(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod",
		"GOENV=off", "GOPROXY=off", "GOSUMDB=off", "GONOPROXY=none")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fixture input incomplete: prepare offline Story module from the root module cache: %w\n%s", err, output)
	}
	return nil
}

// succeededStoryInput keeps the original pinned requirements and exact design.
// The root sum supplies checksums without importing unrelated requirements.
func succeededStoryInput(t *testing.T) map[string]string {
	t.Helper()
	seed, err := os.ReadFile(filepath.Join("..", "..", "go.sum"))
	require.NoError(t, err)
	files := map[string]string{
		"go.mod": "module example.com/story-design\n\ngo 1.27.3\n\nrequire (\n" +
			"\tgithub.com/goadesign/surla/story v0.0.0-20261111183703-4568fcf5bb85\n" +
			"\tgoa.design/goa/v3 " + testmodule.SurlaVersion(t, "goa.design/goa/v3") + "\n)\n",
		"design/design.go": `package design

import . "github.com/goadesign/surla/story/dsl"

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
