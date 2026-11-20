// This file tests user-scoped folder trust pre-population across coding clients
// while proving git working copies remain free of untracked files.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/surla/internal/project"
)

func TestPrepareWorkerTrustAcrossClientsLeavesGitWorktreesClean(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "custom-codex")
	claudeDir := filepath.Join(home, "custom-claude")
	geminiHome := filepath.Join(home, "custom-gemini")

	repo := filepath.Join(t.TempDir(), "service")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	runTrustGit(t, repo, "init", "-q")
	writeTestFile(t, filepath.Join(repo, "README.md"), "# service\n")
	runTrustGit(t, repo, "add", "README.md")
	runTrustGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "init")
	writeTestFile(t, filepath.Join(repo, ".git", "info", "exclude"), ".surla-build/\n.surla-tests/\n")

	buildDir := filepath.Join(repo, ".surla-build")
	testsDir := filepath.Join(repo, ".surla-tests")
	runTrustGit(t, repo, "worktree", "add", "-q", "-b", "surla/build-1", buildDir)
	runTrustGit(t, repo, "worktree", "add", "-q", "-b", "surla/tests-1", testsDir)

	writeTestFile(t, filepath.Join(codexHome, "config.toml"),
		"# user comment\n[mcp_servers.surla]\ncommand = \"/bin/surla\"\nargs = [\"stdio\"]\n\n"+
			"[mcp_servers.chorus]\nurl = \"http://127.0.0.1:9440/mcp\"\n\n"+
			"[projects.\""+repo+"\"]\ntrust_level = \"untrusted\" # upgrade\n")
	writeTestFile(t, filepath.Join(claudeDir, ".claude.json"),
		`{"numStartups":12345678901234567890,"projects":{"/other":{"hasTrustDialogAccepted":false}}}`)
	writeTestFile(t, filepath.Join(geminiHome, "trustedFolders.json"),
		`{"`+repo+`":"TRUST_PARENT"}`)
	writeTestFile(t, filepath.Join(home, ".cursor", "cli-config.json"),
		`{"version":1,"permissions":{"allow":["Shell(git)"],"deny":[]}}`)

	env := []string{
		"HOME=" + home,
		"CODEX_HOME=" + codexHome,
		"CLAUDE_CONFIG_DIR=" + claudeDir,
		"GEMINI_CLI_HOME=" + geminiHome,
	}
	targets := []project.WorkerTrustTarget{
		{Folder: repo, Client: "codex"},
		{Folder: buildDir, Client: "codex"},
		{Folder: repo, Client: "claude"},
		{Folder: testsDir, Client: "claude"},
		{Folder: repo, Client: "gemini"},
		{Folder: buildDir, Client: "gemini"},
		{Folder: testsDir, Client: "cursor"},
	}

	require.NoError(t, prepareWorkerTrust(env, targets))

	// Git working copies must contain zero untracked or modified files.
	for _, dir := range []string{repo, buildDir, testsDir} {
		out := runTrustGit(t, dir, "status", "--porcelain", "-z", "--no-renames", "--untracked-files=all")
		assert.Empty(t, out, "unexpected git status in %s", dir)
	}

	// Verify Codex config preserved comments, upgraded trust_level, appended buildDir, and approved MCP servers.
	codexRaw, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(codexRaw), "# user comment")
	assert.Contains(t, string(codexRaw), "# upgrade")
	var codexDecoded map[string]any
	require.NoError(t, toml.Unmarshal(codexRaw, &codexDecoded))
	codexProjects := codexDecoded["projects"].(map[string]any)
	assert.Equal(t, "trusted", codexProjects[repo].(map[string]any)["trust_level"])
	assert.Equal(t, "trusted", codexProjects[buildDir].(map[string]any)["trust_level"])
	codexServers := codexDecoded["mcp_servers"].(map[string]any)
	assert.Equal(t, "approve", codexServers["surla"].(map[string]any)["default_tools_approval_mode"])
	assert.Equal(t, "approve", codexServers["chorus"].(map[string]any)["default_tools_approval_mode"])

	// Verify Claude .claude.json and settings.json.
	claudeRaw, err := os.ReadFile(filepath.Join(claudeDir, ".claude.json"))
	require.NoError(t, err)
	assert.Contains(t, string(claudeRaw), "12345678901234567890")
	claudeRoot := readTestJSON(t, filepath.Join(claudeDir, ".claude.json"))
	claudeProjects := claudeRoot["projects"].(map[string]any)
	assert.Equal(t, true, claudeProjects[repo].(map[string]any)["hasTrustDialogAccepted"])
	assert.Equal(t, true, claudeProjects[testsDir].(map[string]any)["hasTrustDialogAccepted"])
	assert.Equal(t, false, claudeProjects["/other"].(map[string]any)["hasTrustDialogAccepted"])
	claudePerms := readTestJSON(t, filepath.Join(claudeDir, "settings.json"))["permissions"].(map[string]any)
	assert.Equal(t, []any{"mcp__surla__*", "mcp__chorus__*"}, claudePerms["allow"])

	// Verify Gemini trustedFolders.json preserved TRUST_PARENT and added TRUST_FOLDER.
	geminiRoot := readTestJSON(t, filepath.Join(geminiHome, "trustedFolders.json"))
	assert.Equal(t, "TRUST_PARENT", geminiRoot[repo])
	assert.Equal(t, "TRUST_FOLDER", geminiRoot[buildDir])

	// Verify Cursor .workspace-trusted and cli-config.json.
	markerPath := filepath.Join(home, ".cursor", "projects", cursorWorkspaceSlug(testsDir), ".workspace-trusted")
	marker := readTestJSON(t, markerPath)
	assert.Equal(t, testsDir, marker["workspacePath"])
	assert.NotEmpty(t, marker["trustedAt"])
	cursorPerms := readTestJSON(t, filepath.Join(home, ".cursor", "cli-config.json"))["permissions"].(map[string]any)
	assert.Equal(t, []any{"Shell(git)", "Mcp(surla:*)", "Mcp(chorus:*)"}, cursorPerms["allow"])

	// Second run is strictly idempotent and writes nothing.
	before := snapshotTree(t, home)
	require.NoError(t, prepareWorkerTrust(env, targets))
	assert.Equal(t, before, snapshotTree(t, home))
}

func TestPrepareWorkerTrustRefusesMalformedUserConfigWithoutOverwriting(t *testing.T) {
	home := t.TempDir()
	badToml := "[projects\n"
	codexPath := filepath.Join(home, ".codex", "config.toml")
	writeTestFile(t, codexPath, badToml)

	err := prepareWorkerTrust([]string{"HOME=" + home}, []project.WorkerTrustTarget{
		{Folder: "/work/service/.surla-build", Client: "codex"},
		{Folder: "/work/service/.surla-tests", Client: "claude"},
	})
	require.Error(t, err)
	raw, readErr := os.ReadFile(codexPath)
	require.NoError(t, readErr)
	assert.Equal(t, badToml, string(raw), "malformed config.toml must not be overwritten")

	// Independent client configuration still succeeded.
	claudeProjects := readTestJSON(t, filepath.Join(home, ".claude.json"))["projects"].(map[string]any)
	assert.Equal(t, true, claudeProjects["/work/service/.surla-tests"].(map[string]any)["hasTrustDialogAccepted"])
}

func runTrustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, string(out))
	return string(out)
}
