// This file tests read-only client policy checks, user-scoped folder trust
// pre-population, concurrent Surla writers, and worktree destination refusal.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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

	repo, buildDir, testsDir := initTrustGitRepoWithWorktrees(t)

	initialCodex := "# user comment\n" +
		"[mcp_servers.surla]\ncommand = \"/bin/surla\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\n\n" +
		"[mcp_servers.github]\ncommand = \"npx\"\nargs = [\"github-mcp\"]\n"
	writeTestFile(t, filepath.Join(codexHome, "config.toml"), initialCodex)
	writeTestFile(t, filepath.Join(claudeDir, ".claude.json"),
		`{"numStartups":12345678901234567890,"projects":{"/other":{"hasTrustDialogAccepted":false}}}`)
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
		{ProjectDir: repo, Folder: buildDir, Client: "codex", RequireChorus: true},
		{ProjectDir: repo, Folder: testsDir, Client: "claude", RequireChorus: true},
		{ProjectDir: repo, Folder: buildDir, Client: "gemini", RequireChorus: true},
		{ProjectDir: repo, Folder: testsDir, Client: "cursor", RequireChorus: true},
	}

	require.NoError(t, prepareWorkerTrust(env, targets))

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
	assert.Equal(t, true, claudeProjects[testsDir].(map[string]any)["hasTrustDialogAccepted"])
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
	require.NoError(t, prepareWorkerTrust(customEnv, []project.WorkerTrustTarget{
		{ProjectDir: repo, Folder: buildDir, Client: "gemini"},
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
	require.NoError(t, prepareWorkerTrust(env, targets))
	assert.Equal(t, before, snapshotTree(t, home))
}

func TestPrepareWorkerTrustPreservesExplicitRestrictionsAndRefusesNonYolo(t *testing.T) {
	repo, buildDir, _ := initTrustGitRepoWithWorktrees(t)

	cases := []struct {
		name          string
		client        string
		requireChorus bool
		setup         func(t *testing.T, home string) (env []string, verifyPath, initialContent string)
		wantErrSubstr string
	}{
		{
			name:   "codex explicitly untrusted project",
			client: "codex",
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".codex", "config.toml")
				content := "[projects." + fmt.Sprintf("%q", repo) + "]\ntrust_level = \"untrusted\"\n"
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantErrSubstr: `set trust_level = "trusted"`,
		},
		{
			name:   "codex surla prompt approval mode",
			client: "codex",
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".codex", "config.toml")
				content := "[mcp_servers.surla]\ncommand = \"/bin/surla\"\ndefault_tools_approval_mode = \"prompt\"\n"
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantErrSubstr: `set default_tools_approval_mode = "approve" on [mcp_servers.surla]`,
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
			wantErrSubstr: `set default_tools_approval_mode = "approve" on [mcp_servers.chorus]`,
		},
		{
			name:   "gemini surla trust false",
			client: "gemini",
			setup: func(t *testing.T, home string) ([]string, string, string) {
				geminiHome := filepath.Join(home, "g-home")
				path := filepath.Join(geminiHome, ".gemini", "settings.json")
				content := `{"mcpServers":{"surla":{"command":"/bin/surla","trust":false}}}`
				writeTestFile(t, path, content)
				return []string{"HOME=" + home, "GEMINI_CLI_HOME=" + geminiHome}, path, content
			},
			wantErrSubstr: "set mcpServers.surla.trust to true",
		},
		{
			name:   "gemini DO_NOT_TRUST parent folder",
			client: "gemini",
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".gemini", "trustedFolders.json")
				content := fmt.Sprintf("{%q:\"DO_NOT_TRUST\"}", repo)
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantErrSubstr: "DO_NOT_TRUST",
		},
		{
			name:   "claude permissions deny on surla",
			client: "claude",
			setup: func(t *testing.T, home string) ([]string, string, string) {
				path := filepath.Join(home, ".claude", "settings.json")
				content := `{"permissions":{"deny":["mcp__surla__*"]}}`
				writeTestFile(t, path, content)
				return []string{"HOME=" + home}, path, content
			},
			wantErrSubstr: `remove "mcp__surla__*" from permissions.deny`,
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
			wantErrSubstr: `remove "Mcp(chorus:*)" from permissions.deny`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			env, verifyPath, initialContent := tc.setup(t, home)

			err := prepareWorkerTrust(env, []project.WorkerTrustTarget{{
				ProjectDir:    repo,
				Folder:        buildDir,
				Client:        tc.client,
				Yolo:          false,
				RequireChorus: tc.requireChorus,
			}})
			require.Error(t, err)
			var policyErr *project.WorkerPolicyError
			require.ErrorAs(t, err, &policyErr)
			assert.Contains(t, policyErr.Error(), tc.wantErrSubstr)
			assert.Contains(t, policyErr.Error(), verifyPath)
			lowerMsg := strings.ToLower(policyErr.Error())
			assert.NotContains(t, lowerMsg, "yolo")
			assert.NotContains(t, lowerMsg, "dangerously-skip-permissions")

			raw, readErr := os.ReadFile(verifyPath)
			require.NoError(t, readErr)
			assert.Equal(t, initialContent, string(raw), "explicit policy must remain untouched")

			// With Yolo: true, startup is permitted and still leaves the explicit policy file untouched.
			require.NoError(t, prepareWorkerTrust(env, []project.WorkerTrustTarget{{
				ProjectDir:    repo,
				Folder:        buildDir,
				Client:        tc.client,
				Yolo:          true,
				RequireChorus: tc.requireChorus,
			}}))
			rawAfterYolo, readErr := os.ReadFile(verifyPath)
			require.NoError(t, readErr)
			assert.Equal(t, initialContent, string(rawAfterYolo))
		})
	}

	// Headless launch (RequireChorus: false) is not blocked by a Chorus-only restriction.
	t.Run("chorus restriction ignored when RequireChorus is false", func(t *testing.T) {
		home := t.TempDir()
		codexPath := filepath.Join(home, ".codex", "config.toml")
		writeTestFile(t, codexPath, "[mcp_servers.chorus]\ndefault_tools_approval_mode = \"prompt\"\n")
		require.NoError(t, prepareWorkerTrust([]string{"HOME=" + home}, []project.WorkerTrustTarget{{
			ProjectDir:    repo,
			Folder:        buildDir,
			Client:        "codex",
			Yolo:          false,
			RequireChorus: false,
		}}))
	})
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
				errs <- prepareWorkerTrust(env, []project.WorkerTrustTarget{
					{ProjectDir: dir, Folder: dir, Client: "codex"},
					{ProjectDir: dir, Folder: dir, Client: "claude"},
					{ProjectDir: dir, Folder: dir, Client: "gemini"},
				})
			}(folder)
		} else {
			go func(dir string) {
				defer wg.Done()
				cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestWorkerTrustSubprocessHelper$")
				cmd.Stdin = nil
				cmd.Env = append(os.Environ(),
					"SURLA_WORKER_TRUST_HELPER=1",
					"SURLA_WORKER_TRUST_HOME="+home,
					"SURLA_WORKER_TRUST_FOLDER="+dir,
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
	require.NoError(t, prepareWorkerTrust(env, []project.WorkerTrustTarget{
		{ProjectDir: extraFolder, Folder: extraFolder, Client: "claude"},
	}))
	assert.True(t, injected)
	updatedClaude := readTestJSON(t, filepath.Join(home, ".claude.json"))
	assert.Equal(t, "saved-mid-flight", updatedClaude["externalClientSetting"])
	assert.Equal(t, true, updatedClaude["projects"].(map[string]any)[extraFolder].(map[string]any)["hasTrustDialogAccepted"])
}

func TestWorkerTrustSubprocessHelper(t *testing.T) {
	if os.Getenv("SURLA_WORKER_TRUST_HELPER") != "1" {
		return
	}
	home := os.Getenv("SURLA_WORKER_TRUST_HOME")
	folder := os.Getenv("SURLA_WORKER_TRUST_FOLDER")
	require.NoError(t, prepareWorkerTrust([]string{"HOME=" + home}, []project.WorkerTrustTarget{
		{ProjectDir: folder, Folder: folder, Client: "codex"},
		{ProjectDir: folder, Folder: folder, Client: "claude"},
		{ProjectDir: folder, Folder: folder, Client: "gemini"},
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
				err := prepareWorkerTrust(tc.env, []project.WorkerTrustTarget{
					{ProjectDir: repo, Folder: worktree, Client: tc.client},
				})
				require.Error(t, err, "expected refusal for %s with %v", tc.client, tc.env)
				var policyErr *project.WorkerPolicyError
				assert.False(t, errors.As(err, &policyErr), "destination refusal is not a user policy error")
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
				err := prepareWorkerTrust([]string{"HOME=" + symlinkHome}, []project.WorkerTrustTarget{
					{ProjectDir: repo, Folder: worktree, Client: client},
				})
				require.Error(t, err, "expected symlink refusal for %s", client)
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
	err := prepareWorkerTrust([]string{"HOME=" + home}, []project.WorkerTrustTarget{
		{ProjectDir: folder, Folder: folder, Client: "codex"},
		{ProjectDir: folder, Folder: folder, Client: "claude"},
	})
	require.Error(t, err)

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
	writeTestFile(t, filepath.Join(repo, ".git", "info", "exclude"), ".surla-build/\n.surla-tests/\n")

	buildDir = filepath.Join(repo, ".surla-build")
	testsDir = filepath.Join(repo, ".surla-tests")
	runTrustGit(t, repo, "worktree", "add", "-q", "-b", "surla/build-1", buildDir)
	runTrustGit(t, repo, "worktree", "add", "-q", "-b", "surla/tests-1", testsDir)
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
