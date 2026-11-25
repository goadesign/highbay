// This file tests read-only client policy checks, role-specific operation
// matching, context-bounded locking, concurrent Highbay writers, and worktree
// destination refusal.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genmcp "github.com/goadesign/highbay/gen/mcp_highbay"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/goadesign/highbay/internal/project"
)

func testCatalogHighbayTools(t *testing.T) []string {
	t.Helper()
	res, err := genmcp.NewMCPAdapter(&genhighbay.Endpoints{}, nil).ToolsList(
		t.Context(), &genmcp.ToolsListPayload{},
	)
	require.NoError(t, err)
	require.NotNil(t, res)
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		if tool != nil && tool.Name != "" {
			names = append(names, tool.Name)
		}
	}
	return names
}

func TestPrepareWorkerTrustAcrossClientsLeavesGitWorktreesClean(t *testing.T) {
	home := t.TempDir()
	codexHome := filepath.Join(home, "custom-codex")
	claudeDir := filepath.Join(home, "custom-claude")
	geminiHome := filepath.Join(home, "custom-gemini")

	repo, buildDir, testsDir := initTrustGitRepoWithWorktrees(t)

	initialCodex := "# user comment\n" +
		"[mcp_servers.highbay]\ncommand = \"/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\n\n" +
		"[mcp_servers.github]\ncommand = \"npx\"\nargs = [\"github-mcp\"]\n"
	writeTestFile(t, filepath.Join(codexHome, "config.toml"), initialCodex)
	// The tests copy's entry already answers the trust question with no; it
	// is kept, while the project folder's missing answer is filled in.
	writeTestFile(t, filepath.Join(claudeDir, ".claude.json"),
		`{"numStartups":12345678901234567890,"projects":{"/other":{"hasTrustDialogAccepted":false},`+
			fmt.Sprintf("%q", testsDir)+`:{"hasTrustDialogAccepted":false,"allowedTools":[]},`+
			fmt.Sprintf("%q", repo)+`:{"allowedTools":["Bash(git status)"]}}}`)
	initialClaudeSettings := `{"permissions":{"allow":["Bash(git status)"],"deny":["Bash(rm -rf *)"]}}`
	writeTestFile(t, filepath.Join(claudeDir, "settings.json"), initialClaudeSettings)
	writeTestFile(t, filepath.Join(geminiHome, ".gemini", "trustedFolders.json"),
		`{"`+repo+`":"TRUST_PARENT"}`)
	initialCursorCLI := `{"version":1,"permissions":{"allow":["Shell(git)"],"deny":[]}}`
	writeTestFile(t, filepath.Join(home, ".cursor", "cli-config.json"), initialCursorCLI)

	env := []string{
		"HOME=" + home,
		"CODEX_HOME=" + codexHome,
		"CLAUDE_CONFIG_DIR=" + claudeDir,
		"GEMINI_CLI_HOME=" + geminiHome,
	}
	targets := []project.WorkerTrustTarget{
		{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "codex", RequireChorus: true},
		{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: "claude", RequireChorus: true},
		{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "gemini", RequireChorus: true},
		{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: "cursor", RequireChorus: true},
	}

	require.NoError(t, prepareWorkerTrust(t.Context(), env, targets))

	// Git working copies must contain zero untracked or modified files.
	for _, dir := range []string{repo, buildDir, testsDir} {
		out := runTrustGit(t, dir, "status", "--porcelain", "-z", "--no-renames", "--untracked-files=all")
		assert.Empty(t, out, "unexpected git status in %s", dir)
	}

	// Codex config preserves comments and unrelated MCP servers without touching [mcp_servers.*].
	codexRaw, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	require.NoError(t, err)
	assert.Contains(t, string(codexRaw), "# user comment")
	var codexDecoded map[string]any
	require.NoError(t, toml.Unmarshal(codexRaw, &codexDecoded))
	codexProjects := codexDecoded["projects"].(map[string]any)
	assert.Equal(t, "trusted", codexProjects[repo].(map[string]any)["trust_level"])
	assert.Equal(t, "trusted", codexProjects[buildDir].(map[string]any)["trust_level"])
	codexServers := codexDecoded["mcp_servers"].(map[string]any)
	_, githubApproved := codexServers["github"].(map[string]any)["default_tools_approval_mode"]
	assert.False(t, githubApproved, "unrelated MCP server must not receive approval overrides")

	// Claude .claude.json records folder trust while settings.json is never modified.
	claudeRaw, err := os.ReadFile(filepath.Join(claudeDir, ".claude.json"))
	require.NoError(t, err)
	assert.Contains(t, string(claudeRaw), "12345678901234567890")
	claudeRoot := readTestJSON(t, filepath.Join(claudeDir, ".claude.json"))
	claudeProjects := claudeRoot["projects"].(map[string]any)
	assert.Equal(t, true, claudeProjects[repo].(map[string]any)["hasTrustDialogAccepted"])
	assert.Equal(t, []any{"Bash(git status)"}, claudeProjects[repo].(map[string]any)["allowedTools"])
	assert.Equal(t, false, claudeProjects[testsDir].(map[string]any)["hasTrustDialogAccepted"], "an existing answer is kept")
	assert.Equal(t, false, claudeProjects["/other"].(map[string]any)["hasTrustDialogAccepted"])
	claudeSettingsRaw, err := os.ReadFile(filepath.Join(claudeDir, "settings.json"))
	require.NoError(t, err)
	assert.Equal(t, initialClaudeSettings, string(claudeSettingsRaw))

	// Gemini resolves under $GEMINI_CLI_HOME/.gemini and honors ancestor TRUST_PARENT without redundant writes.
	geminiTrustedPath := filepath.Join(geminiHome, ".gemini", "trustedFolders.json")
	geminiRoot := readTestJSON(t, geminiTrustedPath)
	assert.Equal(t, map[string]any{repo: "TRUST_PARENT"}, geminiRoot)

	// Custom GEMINI_CLI_TRUSTED_FOLDERS_PATH is honored when set.
	customTrustedPath := filepath.Join(home, "custom-trusted-folders.json")
	customEnv := append(append([]string(nil), env...), "GEMINI_CLI_TRUSTED_FOLDERS_PATH="+customTrustedPath)
	require.NoError(t, prepareWorkerTrust(t.Context(), customEnv, []project.WorkerTrustTarget{
		{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "gemini"},
	}))
	customTrusted := readTestJSON(t, customTrustedPath)
	assert.Equal(t, "TRUST_FOLDER", customTrusted[repo])
	assert.Equal(t, "TRUST_FOLDER", customTrusted[buildDir])

	// Cursor creates per-folder .workspace-trusted markers without rewriting cli-config.json.
	for _, dir := range []string{repo, testsDir} {
		markerPath := filepath.Join(home, ".cursor", "projects", cursorWorkspaceSlug(dir), ".workspace-trusted")
		marker := readTestJSON(t, markerPath)
		assert.Equal(t, dir, marker["workspacePath"])
		assert.NotEmpty(t, marker["trustedAt"])
	}
	cursorCLIRaw, err := os.ReadFile(filepath.Join(home, ".cursor", "cli-config.json"))
	require.NoError(t, err)
	assert.Equal(t, initialCursorCLI, string(cursorCLIRaw))

	// Second run is strictly idempotent and writes nothing.
	before := snapshotTree(t, home)
	require.NoError(t, prepareWorkerTrust(t.Context(), env, targets))
	assert.Equal(t, before, snapshotTree(t, home))
}

func TestPrepareWorkerTrustPreservesExplicitRestrictionsAndRefusesNonYolo(t *testing.T) {
	repo, buildDir, _ := initTrustGitRepoWithWorktrees(t)

	cases := []struct {
		name          string
		client        string
		requireChorus bool
		setup         func(t *testing.T, home string) (env []string, verifyPath, initialContent string)
		wantStepPart  string
		// folderRule is true when the restriction marks a folder untrusted,
		// which refuses a role that skips permission prompts as well.
		folderRule bool
	}{
		{
			name:       "codex explicitly untrusted project",
			client:     "codex",
			folderRule: true,
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".codex", "config.toml")
				content := "[projects." + fmt.Sprintf("%q", repo) + "]\ntrust_level = \"untrusted\"\n"
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantStepPart: `Set trust_level = "trusted" for it in your Codex config.toml`,
		},
		{
			name:   "codex highbay prompt approval mode",
			client: "codex",
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".codex", "config.toml")
				content := "[mcp_servers.highbay]\ncommand = \"/bin/hbay\"\ndefault_tools_approval_mode = \"prompt\"\n"
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantStepPart: `Set default_tools_approval_mode = "approve" on [mcp_servers.highbay] in your Codex config.toml`,
		},
		{
			name:          "codex chorus writes approval mode when Chorus is required",
			client:        "codex",
			requireChorus: true,
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".codex", "config.toml")
				content := "[mcp_servers.chorus]\nurl = \"http://127.0.0.1:9440/mcp\"\ndefault_tools_approval_mode = \"writes\"\n"
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantStepPart: `Set default_tools_approval_mode = "approve" on [mcp_servers.chorus] in your Codex config.toml`,
		},
		{
			name:   "gemini highbay trust false",
			client: "gemini",
			setup: func(t *testing.T, home string) ([]string, string, string) {
				geminiHome := filepath.Join(home, "g-home")
				path := filepath.Join(geminiHome, ".gemini", "settings.json")
				content := `{"mcpServers":{"highbay":{"command":"/bin/hbay","trust":false}}}`
				writeTestFile(t, path, content)
				return []string{"HOME=" + home, "GEMINI_CLI_HOME=" + geminiHome}, path, content
			},
			wantStepPart: "Set mcpServers.highbay.trust to true in your Gemini settings.json",
		},
		{
			name:       "gemini DO_NOT_TRUST parent folder",
			client:     "gemini",
			folderRule: true,
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".gemini", "trustedFolders.json")
				content := fmt.Sprintf("{%q:\"DO_NOT_TRUST\"}", repo)
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantStepPart: "DO_NOT_TRUST in trustedFolders.json",
		},
		{
			name:   "claude permissions deny on highbay",
			client: "claude",
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".claude", "settings.json")
				content := `{"permissions":{"deny":["mcp__highbay__*"]}}`
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantStepPart: "permissions.deny (\"mcp__highbay__*\") in settings.json",
		},
		{
			name:          "cursor permissions deny on chorus when Chorus is required",
			client:        "cursor",
			requireChorus: true,
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".cursor", "cli-config.json")
				content := `{"permissions":{"deny":["Mcp(chorus:*)"]}}`
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantStepPart: "permissions.deny (\"Mcp(chorus:*)\") in cli-config.json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env, verifyPath, initialContent := tc.setup(t, home)

			err := prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{{
				Role:          "builder",
				ProjectDir:    repo,
				Folder:        buildDir,
				Client:        tc.client,
				Yolo:          false,
				RequireChorus: tc.requireChorus,
			}})
			require.Error(t, err)
			var policyErr *controlplane.TeamPolicyRefusal
			require.ErrorAs(t, err, &policyErr)
			step := policyErr.Step("then choose Start workers again.")
			assert.Contains(t, step, tc.wantStepPart)
			lowerMsg := strings.ToLower(step)
			assert.NotContains(t, lowerMsg, "yolo")
			assert.NotContains(t, lowerMsg, "dangerously-skip-permissions")

			raw, readErr := os.ReadFile(verifyPath)
			require.NoError(t, readErr)
			assert.Equal(t, initialContent, string(raw), "explicit policy must remain untouched")

			// With Yolo: true, a tool-policy restriction permits startup, an
			// untrusted folder still refuses it, and neither changes the file.
			yoloErr := prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{{
				Role:          "builder",
				ProjectDir:    repo,
				Folder:        buildDir,
				Client:        tc.client,
				Yolo:          true,
				RequireChorus: tc.requireChorus,
			}})
			if tc.folderRule {
				var yoloRefusal *controlplane.TeamPolicyRefusal
				require.ErrorAs(t, yoloErr, &yoloRefusal)
				assert.Contains(t, yoloRefusal.Step("then choose Start workers again."), tc.wantStepPart)
			} else {
				require.NoError(t, yoloErr)
			}
			rawAfterYolo, readErr := os.ReadFile(verifyPath)
			require.NoError(t, readErr)
			if tc.folderRule {
				assert.Equal(t, initialContent, string(rawAfterYolo))
			} else {
				// Codex keeps folder trust in the same file as its tool policy.
				assert.True(t, strings.HasPrefix(string(rawAfterYolo), initialContent), "the explicit policy is kept as written")
			}
		})
	}

	// Headless launch (RequireChorus: false) is not blocked by a Chorus-only restriction.
	t.Run("chorus restriction ignored when RequireChorus is false", func(t *testing.T) {
		home := t.TempDir()
		codexPath := filepath.Join(home, ".codex", "config.toml")
		writeTestFile(t, codexPath, "[mcp_servers.chorus]\ndefault_tools_approval_mode = \"prompt\"\n")
		require.NoError(t, prepareWorkerTrust(t.Context(), []string{"HOME=" + home}, []project.WorkerTrustTarget{{
			Role:          "builder",
			ProjectDir:    repo,
			Folder:        buildDir,
			Client:        "codex",
			Yolo:          false,
			RequireChorus: false,
		}}))
	})
}

// TestPrepareWorkerTrustWritesMissingFolderTrustUnderYolo covers the roles
// that skip permission prompts: their clients still ask for folder trust at
// startup, so the missing entries are written and tool policies are not read.
func TestPrepareWorkerTrustWritesMissingFolderTrustUnderYolo(t *testing.T) {
	home := t.TempDir()
	repo, buildDir, testsDir := initTrustGitRepoWithWorktrees(t)
	claudeSettings := `{"permissions":{"deny":["mcp__highbay__*"]}}`
	writeTestFile(t, filepath.Join(home, ".claude", "settings.json"), claudeSettings)
	env := []string{"HOME=" + home}
	targets := []project.WorkerTrustTarget{
		{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "codex", Yolo: true, RequireChorus: true},
		{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: "claude", Yolo: true, RequireChorus: true},
	}
	require.NoError(t, prepareWorkerTrust(t.Context(), env, targets))

	codexRaw, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	require.NoError(t, err)
	var codexDecoded map[string]any
	require.NoError(t, toml.Unmarshal(codexRaw, &codexDecoded))
	codexProjects := codexDecoded["projects"].(map[string]any)
	assert.Equal(t, "trusted", codexProjects[repo].(map[string]any)["trust_level"])
	assert.Equal(t, "trusted", codexProjects[buildDir].(map[string]any)["trust_level"])
	assert.NotContains(t, codexDecoded, "mcp_servers", "no MCP server entry is written")

	claudeProjects := readTestJSON(t, filepath.Join(home, ".claude.json"))["projects"].(map[string]any)
	assert.Equal(t, true, claudeProjects[repo].(map[string]any)["hasTrustDialogAccepted"])
	assert.Equal(t, true, claudeProjects[testsDir].(map[string]any)["hasTrustDialogAccepted"])
	settingsRaw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	require.NoError(t, err)
	assert.Equal(t, claudeSettings, string(settingsRaw))

	for _, dir := range []string{repo, buildDir, testsDir} {
		out := runTrustGit(t, dir, "status", "--porcelain", "-z", "--no-renames", "--untracked-files=all")
		assert.Empty(t, out, "unexpected git status in %s", dir)
	}
}

func TestPrepareWorkerTrustRoleSpecificOperationsAndWildcardRules(t *testing.T) {
	repo, buildDir, testsDir := initTrustGitRepoWithWorktrees(t)
	catalog := testCatalogHighbayTools(t)

	t.Run("required role tools exist in generated MCP catalog and match role authority", func(t *testing.T) {
		for _, tc := range []struct {
			role string
			kind controlplane.CallerKind
		}{
			{"guide", controlplane.CallerGuide},
			{"builder", controlplane.CallerBuilder},
			{"test_writer", controlplane.CallerTestWriter},
		} {
			tools := requiredHighbayTools(tc.role)
			require.NotEmpty(t, tools)
			for _, tool := range tools {
				assert.Contains(t, catalog, tool)
				assert.True(t, controlplane.OperationAllowed(tc.kind, tool), "%s must be allowed to call %s", tc.role, tool)
			}
		}
	})

	t.Run("denying open_views or optional start_live_target allows workers while open_views refuses guide", func(t *testing.T) {
		home := t.TempDir()
		writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
			`{"permissions":{"deny":["mcp__highbay__open_views","mcp__highbay__start_live_target"]}}`)
		writeTestFile(t, filepath.Join(home, ".cursor", "cli-config.json"),
			`{"permissions":{"deny":["Mcp(highbay:open_views)","Mcp(highbay:start_live_target)"]}}`)
		writeTestFile(t, filepath.Join(home, ".gemini", "settings.json"),
			`{"mcpServers":{"highbay":{"command":"/bin/hbay","args":["stdio"],"trust":true,"excludeTools":["open_views","start_live_target"]}}}`)

		env := []string{"HOME=" + home}
		for _, client := range []string{"claude", "cursor", "gemini"} {
			require.NoError(t, prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{
				{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: client},
				{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: client},
			}), "open_views and start_live_target restrictions must not block ordinary worker roles on %s", client)

			err := prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{
				{Role: "guide", ProjectDir: repo, Folder: repo, Client: client},
			})
			var policyErr *controlplane.TeamPolicyRefusal
			require.ErrorAs(t, err, &policyErr, "open_views restriction must refuse guide on %s", client)
		}
	})

	t.Run("server wildcard rules catch required role tools without blocking other roles", func(t *testing.T) {
		home := t.TempDir()
		writeTestFile(t, filepath.Join(home, ".cursor", "cli-config.json"),
			`{"permissions":{"deny":["Mcp(*:get_builder_assignment)"]}}`)
		writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
			`{"permissions":{"ask":["mcp__*__get_builder_assignment"]}}`)
		includedTools := slices.DeleteFunc(slices.Clone(catalog), func(name string) bool {
			return name == "get_builder_assignment"
		})
		includedJSON, err := json.Marshal(includedTools)
		require.NoError(t, err)
		writeTestFile(t, filepath.Join(home, ".gemini", "settings.json"),
			fmt.Sprintf(`{"mcpServers":{"highbay":{"command":"/bin/hbay","args":["stdio"],"trust":true,"includeTools":%s}}}`, includedJSON))

		env := []string{"HOME=" + home}
		for _, client := range []string{"cursor", "claude", "gemini"} {
			err := prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{
				{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: client},
			})
			var policyErr *controlplane.TeamPolicyRefusal
			require.ErrorAs(t, err, &policyErr, "expected builder refusal on %s", client)
			assert.Contains(t, policyErr.Error(), "get_builder_assignment")

			require.NoError(t, prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{
				{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: client},
			}), "builder-only restriction must allow test_writer on %s", client)
		}
	})

	t.Run("gemini filter checks match Gemini CLI literal excludeTools and parenthesized includeTools", func(t *testing.T) {
		// 1. excludeTools in Gemini CLI is exact literal matching, so "get_*" does not block get_builder_assignment.
		globHome := t.TempDir()
		writeTestFile(t, filepath.Join(globHome, ".gemini", "settings.json"),
			`{"mcpServers":{"highbay":{"command":"/bin/hbay","args":["stdio"],"trust":true,"excludeTools":["get_*","mcp__highbay__get_builder_assignment"]}}}`)
		require.NoError(t, prepareWorkerTrust(t.Context(), []string{"HOME=" + globHome}, []project.WorkerTrustTarget{
			{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "gemini"},
		}))

		// 2. Literal excludeTools entry blocks builder and preserves the exact entry in Detail.
		exactExcludeHome := t.TempDir()
		writeTestFile(t, filepath.Join(exactExcludeHome, ".gemini", "settings.json"),
			`{"mcpServers":{"highbay":{"command":"/bin/hbay","args":["stdio"],"trust":true,"excludeTools":["get_builder_assignment"]}}}`)
		err := prepareWorkerTrust(t.Context(), []string{"HOME=" + exactExcludeHome}, []project.WorkerTrustTarget{
			{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "gemini"},
		})
		var policyErr *controlplane.TeamPolicyRefusal
		require.ErrorAs(t, err, &policyErr)
		assert.Equal(t, "get_builder_assignment", policyErr.Detail)

		// 3. includeTools supports both exact names and parenthesized entries `tool(...)`.
		parenIncludeHome := t.TempDir()
		writeTestFile(t, filepath.Join(parenIncludeHome, ".gemini", "settings.json"),
			`{"mcpServers":{"highbay":{"command":"/bin/hbay","args":["stdio"],"trust":true,"includeTools":["list_builder_assignments","get_builder_assignment(foo)","generate_code","check_project","verify_change"]}}}`)
		require.NoError(t, prepareWorkerTrust(t.Context(), []string{"HOME=" + parenIncludeHome}, []project.WorkerTrustTarget{
			{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "gemini"},
		}))
	})
}

func TestPrepareWorkerTrustCompletesIndependentPreflightChecks(t *testing.T) {
	repo, buildDir, testsDir := initTrustGitRepoWithWorktrees(t)

	t.Run("explicit refusal on second client wins over malformed settings on first client", func(t *testing.T) {
		home := t.TempDir()
		badClaude := "{malformed-json"
		claudeSettings := filepath.Join(home, ".claude", "settings.json")
		writeTestFile(t, claudeSettings, badClaude)
		codexConfig := filepath.Join(home, ".codex", "config.toml")
		initialCodex := "[mcp_servers.highbay]\nenabled = false\n"
		writeTestFile(t, codexConfig, initialCodex)

		err := prepareWorkerTrust(t.Context(), []string{"HOME=" + home}, []project.WorkerTrustTarget{
			{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: "claude"},
			{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "codex"},
		})
		var policyErr *controlplane.TeamPolicyRefusal
		require.ErrorAs(t, err, &policyErr)
		assert.Equal(t, controlplane.PolicyCodexServerDisabled, policyErr.Kind)
		assert.Equal(t, "codex", policyErr.Client)
		assert.Equal(t, "highbay", policyErr.Server)

		gotClaude, readErr := os.ReadFile(claudeSettings)
		require.NoError(t, readErr)
		assert.Equal(t, badClaude, string(gotClaude))
		gotCodex, readErr := os.ReadFile(codexConfig)
		require.NoError(t, readErr)
		assert.Equal(t, initialCodex, string(gotCodex))
	})

	t.Run("malformed client settings alone produces actionable PolicyConfigUnusable refusal", func(t *testing.T) {
		home := t.TempDir()
		writeTestFile(t, filepath.Join(home, ".claude", "settings.json"), "{malformed-json")

		err := prepareWorkerTrust(t.Context(), []string{"HOME=" + home}, []project.WorkerTrustTarget{
			{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "claude"},
		})
		var policyErr *controlplane.TeamPolicyRefusal
		require.ErrorAs(t, err, &policyErr)
		assert.Equal(t, controlplane.PolicyConfigUnusable, policyErr.Kind)
		assert.Equal(t, "claude", policyErr.Client)
		assert.Equal(t, "settings.json", policyErr.Detail)
		step := policyErr.Step("then choose Start workers again.")
		assert.Contains(t, step, "Claude Code configuration file settings.json could not be read or safely used.")
		assert.NotContains(t, step, home)
	})

	t.Run("directory at Codex config.toml aborts preflight before writing earlier Claude trust", func(t *testing.T) {
		home := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(home, ".codex", "config.toml"), 0o755))

		err := prepareWorkerTrust(t.Context(), []string{"HOME=" + home}, []project.WorkerTrustTarget{
			{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: "claude"},
			{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: "codex"},
		})
		var policyErr *controlplane.TeamPolicyRefusal
		require.ErrorAs(t, err, &policyErr)
		assert.Equal(t, controlplane.PolicyConfigUnusable, policyErr.Kind)
		assert.Equal(t, "codex", policyErr.Client)
		assert.Equal(t, "config.toml", policyErr.Detail)
		assert.NoFileExists(t, filepath.Join(home, ".claude.json"), "earlier client trust must not be written when another client fails preflight")
	})

	t.Run("wrong-shaped present policy and trust fields fail preflight without writing trust files", func(t *testing.T) {
		for _, tc := range []struct {
			name        string
			badClient   string
			cleanClient string
			wantDetail  string
			setup       func(t *testing.T, home string)
			verifyClean func(t *testing.T, home string)
		}{
			{
				name:        "claude string permissions.deny",
				badClient:   "claude",
				cleanClient: "codex",
				wantDetail:  "settings.json",
				setup: func(t *testing.T, home string) {
					writeTestFile(t, filepath.Join(home, ".claude", "settings.json"),
						`{"permissions":{"deny":"mcp__highbay__*"}}`)
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".claude.json"))
					assert.NoFileExists(t, filepath.Join(home, ".codex", "config.toml"))
				},
			},
			{
				name:        "claude non-object permissions",
				badClient:   "claude",
				cleanClient: "codex",
				wantDetail:  "settings.json",
				setup: func(t *testing.T, home string) {
					writeTestFile(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":[]}`)
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".claude.json"))
					assert.NoFileExists(t, filepath.Join(home, ".codex", "config.toml"))
				},
			},
			{
				name:        "claude non-bool hasTrustDialogAccepted",
				badClient:   "claude",
				cleanClient: "codex",
				wantDetail:  ".claude.json",
				setup: func(t *testing.T, home string) {
					writeTestFile(t, filepath.Join(home, ".claude.json"),
						fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":"true"}}}`, repo))
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".codex", "config.toml"))
				},
			},
			{
				name:        "cursor non-string rule in permissions.deny",
				badClient:   "cursor",
				cleanClient: "claude",
				wantDetail:  "cli-config.json",
				setup: func(t *testing.T, home string) {
					writeTestFile(t, filepath.Join(home, ".cursor", "cli-config.json"),
						`{"permissions":{"deny":[123]}}`)
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".claude.json"))
				},
			},
			{
				name:        "cursor directory at .workspace-trusted",
				badClient:   "cursor",
				cleanClient: "claude",
				wantDetail:  ".workspace-trusted",
				setup: func(t *testing.T, home string) {
					marker := filepath.Join(home, ".cursor", "projects", cursorWorkspaceSlug(repo), ".workspace-trusted")
					require.NoError(t, os.MkdirAll(marker, 0o755))
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".claude.json"))
				},
			},
			{
				name:        "gemini string trust value",
				badClient:   "gemini",
				cleanClient: "claude",
				wantDetail:  "settings.json",
				setup: func(t *testing.T, home string) {
					writeTestFile(t, filepath.Join(home, ".gemini", "settings.json"),
						`{"mcpServers":{"highbay":{"command":"/bin/hbay","args":["stdio"],"trust":"false"}}}`)
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".claude.json"))
					assert.NoFileExists(t, filepath.Join(home, ".gemini", "trustedFolders.json"))
				},
			},
			{
				name:        "gemini string excludeTools",
				badClient:   "gemini",
				cleanClient: "claude",
				wantDetail:  "settings.json",
				setup: func(t *testing.T, home string) {
					writeTestFile(t, filepath.Join(home, ".gemini", "settings.json"),
						`{"mcpServers":{"highbay":{"command":"/bin/hbay","args":["stdio"],"trust":true,"excludeTools":"get_builder_assignment"}}}`)
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".claude.json"))
					assert.NoFileExists(t, filepath.Join(home, ".gemini", "trustedFolders.json"))
				},
			},
			{
				name:        "gemini non-string trustedFolders value",
				badClient:   "gemini",
				cleanClient: "claude",
				wantDetail:  "trustedFolders.json",
				setup: func(t *testing.T, home string) {
					writeTestFile(t, filepath.Join(home, ".gemini", "trustedFolders.json"),
						fmt.Sprintf(`{%q:false}`, repo))
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".claude.json"))
				},
			},
			{
				name:        "codex string enabled value",
				badClient:   "codex",
				cleanClient: "claude",
				wantDetail:  "config.toml",
				setup: func(t *testing.T, home string) {
					writeTestFile(t, filepath.Join(home, ".codex", "config.toml"),
						"[mcp_servers.highbay]\nenabled = \"false\"\n")
				},
				verifyClean: func(t *testing.T, home string) {
					assert.NoFileExists(t, filepath.Join(home, ".claude.json"))
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				home := t.TempDir()
				tc.setup(t, home)
				// Place cleanClient FIRST in targets to prove preflight completes across all clients before writing.
				err := prepareWorkerTrust(t.Context(), []string{"HOME=" + home}, []project.WorkerTrustTarget{
					{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: tc.cleanClient},
					{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: tc.badClient},
				})
				var policyErr *controlplane.TeamPolicyRefusal
				require.ErrorAs(t, err, &policyErr)
				assert.Equal(t, controlplane.PolicyConfigUnusable, policyErr.Kind)
				assert.Equal(t, tc.badClient, policyErr.Client)
				assert.Equal(t, tc.wantDetail, policyErr.Detail)
				tc.verifyClean(t, home)
			})
		}
	})
}

func TestPrepareWorkerTrustHonorsContextOnContendedLocks(t *testing.T) {
	home := t.TempDir()
	env := []string{"HOME=" + home}
	projectDir := filepath.Join(t.TempDir(), "service")
	require.NoError(t, os.MkdirAll(projectDir, 0o755))

	claudePath := filepath.Join(home, ".claude.json")
	resolvedClaude, err := validateConfigDestination(claudePath, nil)
	require.NoError(t, err)

	// 1. Contended in-process lock cancels promptly on ctx deadline.
	releaseLocal, err := acquireInProcessConfigLock(t.Context(), resolvedClaude)
	require.NoError(t, err)
	shortCtx, cancel := context.WithTimeout(t.Context(), 35*time.Millisecond)
	start := time.Now()
	err = prepareWorkerTrust(shortCtx, env, []project.WorkerTrustTarget{
		{Role: "builder", ProjectDir: projectDir, Folder: projectDir, Client: "claude"},
	})
	cancel()
	releaseLocal()
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)

	// 2. Contended cross-process flock cancels promptly on ctx deadline.
	lockDir := configLockDir(home, func(name string) string { return envLookup(env, name) }, runtime.GOOS)
	resolvedLockDir, err := validateConfigDestination(lockDir, nil)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(resolvedLockDir, 0o700))
	sum := sha256.Sum256([]byte(resolvedClaude))
	lockPath := filepath.Join(resolvedLockDir, hex.EncodeToString(sum[:])+".lock")
	heldFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(heldFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	t.Cleanup(func() {
		assert.NoError(t, syscall.Flock(int(heldFile.Fd()), syscall.LOCK_UN))
		assert.NoError(t, heldFile.Close())
	})

	flockCtx, flockCancel := context.WithTimeout(t.Context(), 45*time.Millisecond)
	defer flockCancel()
	start = time.Now()
	err = prepareWorkerTrust(flockCtx, env, []project.WorkerTrustTarget{
		{Role: "builder", ProjectDir: projectDir, Folder: projectDir, Client: "claude"},
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(start), time.Second)
}

func TestPrepareWorkerTrustConcurrentWritersAndExternalSaveRetry(t *testing.T) {
	home := t.TempDir()
	env := []string{"HOME=" + home}
	const workers = 8
	folders := make([]string, workers)
	for idx := range workers {
		folders[idx] = filepath.Join(t.TempDir(), fmt.Sprintf("project-%d", idx))
		require.NoError(t, os.MkdirAll(folders[idx], 0o755))
	}

	var wg sync.WaitGroup
	errs := make(chan error, workers*2)
	for idx, folder := range folders {
		wg.Add(1)
		if idx%2 == 0 {
			go func(dir string) {
				defer wg.Done()
				errs <- prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{
					{Role: "builder", ProjectDir: dir, Folder: dir, Client: "codex"},
					{Role: "builder", ProjectDir: dir, Folder: dir, Client: "claude"},
					{Role: "builder", ProjectDir: dir, Folder: dir, Client: "gemini"},
				})
			}(folder)
		} else {
			go func(dir string) {
				defer wg.Done()
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWorkerTrustSubprocessHelper$")
				cmd.Stdin = nil
				cmd.Env = append(os.Environ(),
					"HIGHBAY_WORKER_TRUST_HELPER=1",
					"HIGHBAY_WORKER_TRUST_HOME="+home,
					"HIGHBAY_WORKER_TRUST_FOLDER="+dir,
				)
				out, err := cmd.CombinedOutput()
				if err != nil {
					errs <- fmt.Errorf("subprocess helper failed: %w: %s", err, string(out))
					return
				}
				errs <- nil
			}(folder)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	codexRaw, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	require.NoError(t, err)
	var codexDecoded map[string]any
	require.NoError(t, toml.Unmarshal(codexRaw, &codexDecoded))
	codexProjects := codexDecoded["projects"].(map[string]any)

	claudeProjects := readTestJSON(t, filepath.Join(home, ".claude.json"))["projects"].(map[string]any)
	geminiTrusted := readTestJSON(t, filepath.Join(home, ".gemini", "trustedFolders.json"))

	for _, folder := range folders {
		require.Contains(t, codexProjects, folder)
		assert.Equal(t, "trusted", codexProjects[folder].(map[string]any)["trust_level"])
		require.Contains(t, claudeProjects, folder)
		assert.Equal(t, true, claudeProjects[folder].(map[string]any)["hasTrustDialogAccepted"])
		assert.Equal(t, "TRUST_FOLDER", geminiTrusted[folder])
	}

	// Simulate a non-cooperating external client saving between ReadFile and Rename.
	extraFolder := filepath.Join(t.TempDir(), "external-race-project")
	require.NoError(t, os.MkdirAll(extraFolder, 0o755))
	prevHook := testBeforeConfigReplace
	t.Cleanup(func() { testBeforeConfigReplace = prevHook })
	injected := false
	testBeforeConfigReplace = func(attempt int, resolvedTarget string) {
		if attempt == 0 && !injected && strings.HasSuffix(resolvedTarget, ".claude.json") {
			injected = true
			current := readTestJSON(t, resolvedTarget)
			current["externalClientSetting"] = "saved-mid-flight"
			encoded, encErr := encodeConfigJSONBytes(resolvedTarget, current)
			require.NoError(t, encErr)
			require.NoError(t, os.WriteFile(resolvedTarget, encoded, 0o600))
		}
	}
	require.NoError(t, prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{
		{Role: "builder", ProjectDir: extraFolder, Folder: extraFolder, Client: "claude"},
	}))
	assert.True(t, injected)
	updatedClaude := readTestJSON(t, filepath.Join(home, ".claude.json"))
	assert.Equal(t, "saved-mid-flight", updatedClaude["externalClientSetting"])
	assert.Equal(t, true, updatedClaude["projects"].(map[string]any)[extraFolder].(map[string]any)["hasTrustDialogAccepted"])
}

func TestWorkerTrustSubprocessHelper(t *testing.T) {
	if os.Getenv("HIGHBAY_WORKER_TRUST_HELPER") != "1" {
		return
	}
	home := os.Getenv("HIGHBAY_WORKER_TRUST_HOME")
	folder := os.Getenv("HIGHBAY_WORKER_TRUST_FOLDER")
	require.NoError(t, prepareWorkerTrust(t.Context(), []string{"HOME=" + home}, []project.WorkerTrustTarget{
		{Role: "builder", ProjectDir: folder, Folder: folder, Client: "codex"},
		{Role: "builder", ProjectDir: folder, Folder: folder, Client: "claude"},
		{Role: "builder", ProjectDir: folder, Folder: folder, Client: "gemini"},
	}))
}

func TestPrepareWorkerTrustRefusesWorktreeDestinationsAndSymlinks(t *testing.T) {
	repo, buildDir, testsDir := initTrustGitRepoWithWorktrees(t)

	for _, worktree := range []string{buildDir, testsDir} {
		t.Run(filepath.Base(worktree), func(t *testing.T) {
			safeHome := t.TempDir()
			for _, tc := range []struct {
				client string
				env    []string
			}{
				{"codex", []string{"HOME=" + safeHome, "CODEX_HOME=" + filepath.Join(worktree, "codex")}},
				{"claude", []string{"HOME=" + safeHome, "CLAUDE_CONFIG_DIR=" + filepath.Join(worktree, "claude")}},
				{"gemini", []string{"HOME=" + safeHome, "GEMINI_CLI_HOME=" + filepath.Join(worktree, "gemini")}},
				{"gemini", []string{"HOME=" + safeHome, "GEMINI_CLI_TRUSTED_FOLDERS_PATH=" + filepath.Join(worktree, "trusted.json")}},
				{"cursor", []string{"HOME=" + worktree}},
			} {
				err := prepareWorkerTrust(t.Context(), tc.env, []project.WorkerTrustTarget{
					{Role: "builder", ProjectDir: repo, Folder: worktree, Client: tc.client},
				})
				var policyErr *controlplane.TeamPolicyRefusal
				require.ErrorAs(t, err, &policyErr, "expected actionable destination refusal for %s with %v", tc.client, tc.env)
				assert.Equal(t, controlplane.PolicyConfigUnusable, policyErr.Kind)
				assert.NotContains(t, policyErr.Step("then retry."), worktree, "refusal step must omit raw filesystem paths")
			}

			// Symlinks inside a safe home resolving into the worktree must also be refused before writing.
			symlinkHome := t.TempDir()
			require.NoError(t, os.Symlink(filepath.Join(worktree, "linked-codex"), filepath.Join(symlinkHome, ".codex")))
			require.NoError(t, os.Symlink(filepath.Join(worktree, "linked-claude.json"), filepath.Join(symlinkHome, ".claude.json")))
			require.NoError(t, os.MkdirAll(filepath.Join(symlinkHome, ".gemini"), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(worktree, "linked-trusted.json"), filepath.Join(symlinkHome, ".gemini", "trustedFolders.json")))
			require.NoError(t, os.MkdirAll(filepath.Join(symlinkHome, ".cursor"), 0o755))
			require.NoError(t, os.Symlink(filepath.Join(worktree, "linked-cursor-projects"), filepath.Join(symlinkHome, ".cursor", "projects")))

			for _, client := range []string{"codex", "claude", "gemini", "cursor"} {
				err := prepareWorkerTrust(t.Context(), []string{"HOME=" + symlinkHome}, []project.WorkerTrustTarget{
					{Role: "builder", ProjectDir: repo, Folder: worktree, Client: client},
				})
				var policyErr *controlplane.TeamPolicyRefusal
				require.ErrorAs(t, err, &policyErr, "expected symlink refusal for %s", client)
				assert.Equal(t, controlplane.PolicyConfigUnusable, policyErr.Kind)
			}

			for _, dir := range []string{repo, buildDir, testsDir} {
				out := runTrustGit(t, dir, "status", "--porcelain", "-z", "--no-renames", "--untracked-files=all")
				assert.Empty(t, out, "unexpected git status in %s", dir)
			}
		})
	}
}

func TestPrepareWorkerTrustRefusesMalformedUserConfigWithoutOverwriting(t *testing.T) {
	home := t.TempDir()
	badToml := "[projects\n"
	codexPath := filepath.Join(home, ".codex", "config.toml")
	writeTestFile(t, codexPath, badToml)

	badJSON := "{not-json"
	claudePath := filepath.Join(home, ".claude.json")
	writeTestFile(t, claudePath, badJSON)

	folder := t.TempDir()
	err := prepareWorkerTrust(t.Context(), []string{"HOME=" + home}, []project.WorkerTrustTarget{
		{Role: "builder", ProjectDir: folder, Folder: folder, Client: "codex"},
		{Role: "builder", ProjectDir: folder, Folder: folder, Client: "claude"},
	})
	var policyErr *controlplane.TeamPolicyRefusal
	require.ErrorAs(t, err, &policyErr)
	assert.Equal(t, controlplane.PolicyConfigUnusable, policyErr.Kind)

	gotToml, err := os.ReadFile(codexPath)
	require.NoError(t, err)
	assert.Equal(t, badToml, string(gotToml))

	gotJSON, err := os.ReadFile(claudePath)
	require.NoError(t, err)
	assert.Equal(t, badJSON, string(gotJSON))
}

func initTrustGitRepoWithWorktrees(t *testing.T) (repo, buildDir, testsDir string) {
	t.Helper()
	repo = filepath.Join(t.TempDir(), "service")
	require.NoError(t, os.MkdirAll(repo, 0o755))
	runTrustGit(t, repo, "init", "-q")
	writeTestFile(t, filepath.Join(repo, "README.md"), "# service\n")
	runTrustGit(t, repo, "add", "README.md")
	runTrustGit(t, repo, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "init")
	writeTestFile(t, filepath.Join(repo, ".git", "info", "exclude"), ".highbay-build/\n.highbay-tests/\n")

	buildDir = filepath.Join(repo, ".highbay-build")
	testsDir = filepath.Join(repo, ".highbay-tests")
	runTrustGit(t, repo, "worktree", "add", "-q", "-b", "highbay/build-1", buildDir)
	runTrustGit(t, repo, "worktree", "add", "-q", "-b", "highbay/tests-1", testsDir)
	return repo, buildDir, testsDir
}

func runTrustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = nil
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, string(out))
	return string(out)
}
