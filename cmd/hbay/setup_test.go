package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/goadesign/highbay/internal/project"
	"github.com/goadesign/highbay/skills"
)

// testExecutable is the hbay path setup tests register.
const testExecutable = "/opt/highbay/bin/hbay"

// testSetupEnvironment points setup at home with no environment overrides and
// no commands on PATH, so tests never read or write the user's configuration.
func testSetupEnvironment(home string) setupEnvironment {
	return setupEnvironment{
		home:       home,
		executable: testExecutable,
		getenv:     func(string) string { return "" },
		onPath:     func(string) bool { return false },
		goos:       "darwin",
	}
}

// writeTestFile creates path with content and any missing folders.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// readTestJSON decodes the JSON object stored at path.
func readTestJSON(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var root map[string]any
	require.NoError(t, json.Unmarshal(raw, &root))
	return root
}

func TestSetupRegistersEveryDetectedClientOnce(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{
		".claude", ".codex", ".cursor", ".gemini/config", ".gemini/skills", ".codeium/windsurf", ".copilot",
		"Library/Application Support/Code/User",
	} {
		require.NoError(t, os.MkdirAll(filepath.Join(home, dir), 0o755))
	}
	herdrHome := filepath.Join(home, ".config", "herdr-app")
	writeTestFile(t, filepath.Join(herdrHome, "keys.json"), "{\n  \"global\": {\n    \"cmd-k\": \"app.palette\"\n  }\n}\n")

	var out bytes.Buffer
	require.NoError(t, setupGlobal(testSetupEnvironment(home), &out))

	stdio := map[string]any{"command": testExecutable, "args": []any{"stdio"}}
	geminiStdio := map[string]any{"command": testExecutable, "args": []any{"stdio"}, "trust": true}
	jsonConfigs := []struct {
		path, serversKey string
		want             map[string]any
	}{
		{".claude.json", "mcpServers", stdio},
		{".cursor/mcp.json", "mcpServers", stdio},
		{".gemini/config/mcp_config.json", "mcpServers", stdio},
		{".gemini/settings.json", "mcpServers", geminiStdio},
		{".codeium/windsurf/mcp_config.json", "mcpServers", stdio},
		{".copilot/mcp-config.json", "mcpServers", map[string]any{"type": "local", "command": testExecutable, "args": []any{"stdio"}, "tools": []any{"*"}}},
		{"Library/Application Support/Code/User/mcp.json", "servers", map[string]any{"type": "stdio", "command": testExecutable, "args": []any{"stdio"}}},
	}
	for _, c := range jsonConfigs {
		servers, _ := readTestJSON(t, filepath.Join(home, c.path))[c.serversKey].(map[string]any)
		assert.Equal(t, c.want, servers["highbay"], c.path)
	}
	claudePerms, _ := readTestJSON(t, filepath.Join(home, ".claude", "settings.json"))["permissions"].(map[string]any)
	assert.Equal(t, []any{"mcp__highbay__*", "mcp__chorus__*"}, claudePerms["allow"])
	_, cursorConfigErr := os.Stat(filepath.Join(home, ".cursor", "cli-config.json"))
	assert.ErrorIs(t, cursorConfigErr, os.ErrNotExist, "Cursor cli-config.json is updated only when already present")
	codex, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	require.NoError(t, err)
	assert.Equal(t, "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\nenv_vars = [\"HC_TERMINAL_ID\"]\n", string(codex))
	for _, dir := range []string{".agents", ".claude", ".codex", ".cursor", ".gemini/config", ".gemini", ".codeium/windsurf", ".copilot"} {
		skill, err := os.ReadFile(filepath.Join(home, dir, "skills", "highbay", "SKILL.md"))
		require.NoError(t, err, dir)
		assert.Equal(t, skills.HighbaySkillMD, skill, dir)
	}

	modDir := filepath.Join(herdrHome, "mods", "highbay")
	entries, err := os.ReadDir(modDir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.Equal(t, []string{"main.js", "mod.json"}, names, "only runtime files are installed")
	keys := readTestJSON(t, filepath.Join(herdrHome, "keys.json"))
	assert.Equal(t, map[string]any{"cmd-k": "app.palette", "cmd-alt-shift-h": "mod.open:highbay"}, keys["global"])
	assert.Contains(t, out.String(), "Configured Highbay tool permissions in "+filepath.Join(home, ".claude", "settings.json"))
	assert.Contains(t, out.String(), "Added Herdr shortcut cmd-alt-shift-h")

	// A second run finds everything in place and writes nothing.
	before := snapshotTree(t, home)
	out.Reset()
	require.NoError(t, setupGlobal(testSetupEnvironment(home), &out))
	assert.Equal(t, before, snapshotTree(t, home))
	assert.Equal(t, 8, strings.Count(out.String(), "is already registered in"), out.String())
	assert.Contains(t, out.String(), "Highbay tool permissions are already configured in "+filepath.Join(home, ".claude", "settings.json"))
	assert.NotContains(t, out.String(), "Installed")
	assert.Contains(t, out.String(), "Herdr shortcut cmd-alt-shift-h already opens the Highbay companion.")
}

// snapshotTree records every file's bytes and modification time under root.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	require.NoError(t, filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[path] = info.ModTime().String() + "\n" + string(content)
		return nil
	}))
	return files
}

func TestSetupDetectsClientsByEnvironmentAndPath(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(t.TempDir(), "claude")
	codexDir := filepath.Join(t.TempDir(), "codex")
	geminiHome := filepath.Join(t.TempDir(), "gemini-home")
	env := testSetupEnvironment(home)
	env.goos = "linux"
	env.getenv = func(name string) string {
		return map[string]string{
			"CLAUDE_CONFIG_DIR": claudeDir,
			"CODEX_HOME":        codexDir,
			"GEMINI_CLI_HOME":   geminiHome,
		}[name]
	}
	env.onPath = func(name string) bool { return name == "claude" || name == "codex" || name == "gemini" }

	jsonTargets, tomlTargets, permTargets, skillTargets := setupTargets(env)
	var paths []string
	for _, target := range jsonTargets {
		paths = append(paths, target.path)
	}
	assert.Equal(t, []string{
		filepath.Join(claudeDir, ".claude.json"),
		filepath.Join(geminiHome, ".gemini", "settings.json"),
	}, paths)
	assert.Equal(t, []string{filepath.Join(codexDir, "config.toml")}, tomlTargets)
	require.Len(t, permTargets, 1)
	assert.Equal(t, filepath.Join(claudeDir, "settings.json"), permTargets[0].path)
	assert.Equal(t, []string{
		filepath.Join(home, ".agents", "skills", "highbay", "SKILL.md"),
		filepath.Join(claudeDir, "skills", "highbay", "SKILL.md"),
		filepath.Join(codexDir, "skills", "highbay", "SKILL.md"),
		filepath.Join(geminiHome, ".gemini", "skills", "highbay", "SKILL.md"),
	}, skillTargets)

	// Antigravity's ~/.gemini/config alone does not make Gemini CLI a target.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".gemini", "config"), 0o755))
	jsonTargets, _, _, _ = setupTargets(testSetupEnvironment(home))
	require.Len(t, jsonTargets, 1)
	assert.Equal(t, filepath.Join(home, ".gemini", "config", "mcp_config.json"), jsonTargets[0].path)
}

func TestSetupWithoutClientsOrHerdrInstallsOnlyTheUniversalSkill(t *testing.T) {
	home := t.TempDir()
	var out bytes.Buffer
	require.NoError(t, setupGlobal(testSetupEnvironment(home), &out))
	assert.Contains(t, out.String(), "No coding agent was found")
	assert.NotContains(t, out.String(), "companion")
	files := snapshotTree(t, home)
	require.Len(t, files, 1)
	_, ok := files[filepath.Join(home, ".agents", "skills", "highbay", "SKILL.md")]
	assert.True(t, ok)
}

func TestRunSetupRequiresGlobal(t *testing.T) {
	err := runSetup(nil, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `run "hbay setup -global"`)
	assert.ErrorContains(t, runSetup([]string{"-global", "extra"}, &bytes.Buffer{}), `unexpected arguments ["extra"]`)
}

func TestSetupJSONConfigMerge(t *testing.T) {
	entry := map[string]any{"command": testExecutable, "args": []any{"stdio"}}
	cases := []struct {
		name, content string
		changed       bool
		wantErr       string
		check         func(t *testing.T, root map[string]any, raw string)
	}{
		{name: "missing file", changed: true},
		{name: "empty file", content: " \n", changed: true},
		{
			name:    "keeps other keys, servers and exact numbers",
			content: `{"numStartups": 12345678901234567890, "tip": "<b>&</b>", "mcpServers": {"chorus": {"type": "http", "url": "http://127.0.0.1:9440/mcp"}, "highbay": {"type": "http", "url": "http://old"}}}`,
			changed: true,
			check: func(t *testing.T, root map[string]any, raw string) {
				assert.Contains(t, raw, "12345678901234567890")
				assert.Contains(t, raw, `"<b>&</b>"`)
				servers := root["mcpServers"].(map[string]any)
				assert.Equal(t, map[string]any{"type": "http", "url": "http://127.0.0.1:9440/mcp"}, servers["chorus"])
			},
		},
		{name: "already registered", content: `{"mcpServers": {"highbay": {"args": ["stdio"], "command": "/opt/highbay/bin/hbay"}}}`, changed: false},
		{name: "null", content: "null", wantErr: "must contain a JSON object, not null"},
		{name: "array", content: "[]", wantErr: "parse JSON"},
		{name: "invalid", content: "{", wantErr: "parse JSON"},
		{name: "trailing data", content: "{} {}", wantErr: "must contain one JSON object"},
		{name: "servers not an object", content: `{"mcpServers": []}`, wantErr: "mcpServers must be a JSON object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config", "mcp.json")
			if c.content != "" {
				writeTestFile(t, path, c.content)
			}
			changed, err := mergeMCPJSONConfig(mcpJSONTarget{path: path, serversKey: "mcpServers", entry: entry})
			if c.wantErr != "" {
				assert.ErrorContains(t, err, c.wantErr)
				raw, readErr := os.ReadFile(path)
				require.NoError(t, readErr)
				assert.Equal(t, c.content, string(raw), "a refused file is not written")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.changed, changed)
			raw, err := os.ReadFile(path)
			require.NoError(t, err)
			if !c.changed {
				assert.Equal(t, c.content, string(raw))
			}
			root := readTestJSON(t, path)
			assert.Equal(t, entry, root["mcpServers"].(map[string]any)["highbay"])
			if c.check != nil {
				c.check(t, root, string(raw))
			}
		})
	}
}

func TestSetupJSONConfigFollowsSymlinkAndKeepsPermissions(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "dotfiles", "claude.json")
	writeTestFile(t, target, `{"theme": "dark"}`)
	require.NoError(t, os.Chmod(target, 0o600))
	link := filepath.Join(dir, ".claude.json")
	require.NoError(t, os.Symlink(target, link))

	changed, err := mergeMCPJSONConfig(mcpJSONTarget{path: link, serversKey: "mcpServers", entry: map[string]any{"command": testExecutable}})
	require.NoError(t, err)
	assert.True(t, changed)
	resolved, err := os.Readlink(link)
	require.NoError(t, err)
	assert.Equal(t, target, resolved, "the link still points at the dotfile")
	info, err := os.Stat(target)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.Equal(t, "dark", readTestJSON(t, target)["theme"])
}

func TestSetupCodexConfigMerge(t *testing.T) {
	const table = "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\nenv_vars = [\"HC_TERMINAL_ID\"]\n"
	cases := []struct {
		name, content, want string
		changed             bool
		wantErr             string
	}{
		{name: "missing file", want: table, changed: true},
		{
			name:    "appends after other settings and pre-approves chorus table",
			content: "# my settings\nmodel = \"gpt\"\n\n[mcp_servers.chorus]\nurl = \"http://127.0.0.1:9440/mcp\"\n\n",
			want:    "# my settings\nmodel = \"gpt\"\n\n[mcp_servers.chorus]\ndefault_tools_approval_mode = \"approve\"\nurl = \"http://127.0.0.1:9440/mcp\"\n\n" + table,
			changed: true,
		},
		{
			name:    "keeps existing chorus approval mode when already set",
			content: "[mcp_servers.chorus]\ndefault_tools_approval_mode = \"prompt\"\nurl = \"http://127.0.0.1:9440/mcp\"\n\n" + table,
			changed: false,
		},
		{
			name:    "adds chorus approval mode even when highbay already matches",
			content: "[mcp_servers.chorus]\nurl = \"http://127.0.0.1:9440/mcp\"\n\n" + table,
			want:    "[mcp_servers.chorus]\ndefault_tools_approval_mode = \"approve\"\nurl = \"http://127.0.0.1:9440/mcp\"\n\n" + table,
			changed: true,
		},
		{
			name:    "replaces command and args while preserving explicit approval mode and other keys",
			content: "[mcp_servers.highbay]\ncommand = 'old/hbay' # pinned\nargs = [\"serve\", \"-x\"]\ndefault_tools_approval_mode = \"prompt\"\nenv_vars = [\"CUSTOM_VAR\"]\nstartup_timeout_sec = 30\n\n[mcp_servers.highbay.env]\nA = \"1\"\n",
			want:    "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\" # pinned\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"prompt\"\nenv_vars = [\"CUSTOM_VAR\", \"HC_TERMINAL_ID\"]\nstartup_timeout_sec = 30\n\n[mcp_servers.highbay.env]\nA = \"1\"\n",
			changed: true,
		},
		{
			name:    "splices HC_TERMINAL_ID into structured env_vars array without overwriting tables",
			content: "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\nenv_vars = [{ name = \"FOO\", source = \"local\" }]\n",
			want:    "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\nenv_vars = [{ name = \"FOO\", source = \"local\" }, \"HC_TERMINAL_ID\"]\n",
			changed: true,
		},
		{
			name:    "splices HC_TERMINAL_ID into multiline structured env_vars with comments",
			content: "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"writes\"\nenv_vars = [\n  { name = \"FOO\", source = \"remote\" } # keep comment\n]\n",
			want:    "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"writes\"\nenv_vars = [\n  { name = \"FOO\", source = \"remote\" }, # keep comment\n  \"HC_TERMINAL_ID\"\n]\n",
			changed: true,
		},
		{
			name:    "recognizes structured HC_TERMINAL_ID entry without rewriting",
			content: "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"prompt\"\nenv_vars = [{ name = \"HC_TERMINAL_ID\", source = \"local\" }]\n",
			changed: false,
		},
		{
			name:    "keeps user env_vars untouched when HC_TERMINAL_ID already present",
			content: "[mcp_servers.highbay]\ncommand = '/opt/highbay/bin/hbay'\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\nenv_vars = ['CUSTOM_VAR', 'HC_TERMINAL_ID']\n",
			changed: false,
		},
		{
			name:    "adds missing keys after the header",
			content: "[mcp_servers.highbay]\nstartup_timeout_sec = 30\n",
			want:    "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\nenv_vars = [\"HC_TERMINAL_ID\"]\nstartup_timeout_sec = 30\n",
			changed: true,
		},
		{
			name:    "adds a key after a header at the end of the file",
			content: "[mcp_servers.highbay]",
			want:    "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\ndefault_tools_approval_mode = \"approve\"\nenv_vars = [\"HC_TERMINAL_ID\"]\n",
			changed: true,
		},
		{
			name:    "replaces dotted keys",
			content: "[mcp_servers]\nhighbay.command = \"old\"\nhighbay.args = []\n",
			want:    "[mcp_servers]\nhighbay.command = \"/opt/highbay/bin/hbay\"\nhighbay.args = [\"stdio\"]\nhighbay.default_tools_approval_mode = \"approve\"\nhighbay.env_vars = [\"HC_TERMINAL_ID\"]\n",
			changed: true,
		},
		{
			name:    "replaces a multiline array with comments",
			content: "[mcp_servers.highbay]\nargs = [\n  \"serve\", # old ]\n  \"-x\",\n] # end\ncommand = \"/opt/highbay/bin/hbay\"\n",
			want:    "[mcp_servers.highbay]\ndefault_tools_approval_mode = \"approve\"\nenv_vars = [\"HC_TERMINAL_ID\"]\nargs = [\"stdio\"] # end\ncommand = \"/opt/highbay/bin/hbay\"\n",
			changed: true,
		},
		{name: "already registered", content: "# keep\n" + table, changed: false},
		{name: "dotted keys missing args", content: "[mcp_servers]\nhighbay.command = \"old\"\n", wantErr: "dotted keys missing command or args"},
		{name: "inline table", content: "[mcp_servers]\nhighbay = { command = \"old\" }\n", wantErr: "inline table"},
		{name: "array of tables", content: "[[mcp_servers.highbay]]\ncommand = \"old\"\n", wantErr: "array of tables"},
		{name: "servers as an array of tables", content: "[[mcp_servers]]\nname = \"x\"\n", wantErr: "mcp_servers is not a table"},
		{name: "servers as a string", content: "mcp_servers = \"x\"\n", wantErr: "mcp_servers is not a table"},
		{name: "url transport", content: "[mcp_servers.highbay]\nurl = \"http://old\"\n", wantErr: "has a url"},
		{name: "command not a string", content: "[mcp_servers.highbay]\ncommand = 3\n", wantErr: "command is not a string"},
		{name: "env_vars not an array", content: "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\nenv_vars = \"HC_TERMINAL_ID\"\n", wantErr: "env_vars is not an array"},
		{name: "env_vars non-string item", content: "[mcp_servers.highbay]\ncommand = \"/opt/highbay/bin/hbay\"\nargs = [\"stdio\"]\nenv_vars = [1]\n", wantErr: "env_vars must contain strings"},
		{name: "invalid TOML", content: "[mcp_servers.highbay\n", wantErr: "is not valid TOML"},
		{name: "duplicate key", content: "a = 1\na = 2\n", wantErr: "is not valid TOML"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), ".codex", "config.toml")
			if c.content != "" {
				writeTestFile(t, path, c.content)
			}
			changed, err := mergeCodexConfig(path, testExecutable)
			raw, readErr := os.ReadFile(path)
			if c.content != "" || c.wantErr == "" {
				require.NoError(t, readErr)
			}
			if c.wantErr != "" {
				assert.ErrorContains(t, err, c.wantErr)
				assert.ErrorContains(t, err, `set [mcp_servers.highbay] command = "/opt/highbay/bin/hbay" and args = ["stdio"] by hand`)
				assert.Equal(t, c.content, string(raw), "a refused file is not written")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, c.changed, changed)
			if c.changed {
				assert.Equal(t, c.want, string(raw))
			} else {
				assert.Equal(t, c.content, string(raw))
			}
		})
	}
}

func TestSetupCodexConfigIsPrivateWhenCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	_, err := mergeCodexConfig(path, testExecutable)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSetupHerdrShortcut(t *testing.T) {
	cases := []struct {
		name, keys, want, output string
		wantErr                  string
	}{
		{
			name:   "adds to the global section",
			keys:   "{\n  \"global\": {\n    \"cmd-k\": \"app.palette\"\n  }\n}\n",
			want:   "{\n  \"global\": {\n    \"cmd-k\": \"app.palette\",\n    \"cmd-alt-shift-h\": \"mod.open:highbay\"\n  }\n}\n",
			output: "Added Herdr shortcut cmd-alt-shift-h",
		},
		{
			name:   "creates the global section",
			keys:   "{\"terminal\": {\"ctrl-c\": \"terminal.copy\"}}",
			want:   "{\"terminal\": {\"ctrl-c\": \"terminal.copy\"},\"global\": {\"cmd-alt-shift-h\": \"mod.open:highbay\"}}",
			output: "Added Herdr shortcut",
		},
		{name: "keeps a conflicting binding", keys: `{"terminal": {"super-option-H": "terminal.split"}}`, output: `is taken by "terminal.split" in "terminal"`},
		{name: "keeps a sequence starting with the key", keys: `{"global": {"cmd-alt-shift-h g": "app.help"}}`, output: `is taken by "app.help"`},
		{name: "already opens the companion", keys: `{"global": {"cmd-alt-shift-h": {"command": "mod.open", "args": {"name": "highbay"}}}}`, output: "already opens the Highbay companion"},
		{name: "another key is free", keys: `{"global": {"cmd-alt-s": "app.help", "cmd-alt-shift-c": "mod.open:chorus-launcher"}}`, want: `{"global": {"cmd-alt-s": "app.help", "cmd-alt-shift-c": "mod.open:chorus-launcher","cmd-alt-shift-h": "mod.open:highbay"}}`, output: "Added"},
		{name: "malformed", keys: `{"global": []}`, wantErr: "global must contain a JSON object"},
		{name: "two documents", keys: `{} {}`, wantErr: "must contain one JSON object"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			herdrHome := t.TempDir()
			path := filepath.Join(herdrHome, "keys.json")
			writeTestFile(t, path, c.keys)
			var out bytes.Buffer
			err := setupHerdrShortcut(herdrHome, &out)
			raw, readErr := os.ReadFile(path)
			require.NoError(t, readErr)
			if c.wantErr != "" {
				assert.ErrorContains(t, err, c.wantErr)
				assert.Equal(t, c.keys, string(raw))
				return
			}
			require.NoError(t, err)
			assert.Contains(t, out.String(), c.output)
			want := c.want
			if want == "" {
				want = c.keys
			}
			assert.Equal(t, want, string(raw))
		})
	}
}

func TestSetupHerdrModOutsideMacOSAddsNoShortcut(t *testing.T) {
	home := t.TempDir()
	herdrHome := filepath.Join(home, "herdr")
	writeTestFile(t, filepath.Join(herdrHome, "keys.json"), "{}")
	env := testSetupEnvironment(home)
	env.goos = "linux"
	env.getenv = func(name string) string {
		return map[string]string{"HERDR_APP_HOME": herdrHome}[name]
	}
	var out bytes.Buffer
	modDir, err := setupHerdrMod(env, &out)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(herdrHome, "mods", "highbay"), modDir)
	keys, err := os.ReadFile(filepath.Join(herdrHome, "keys.json"))
	require.NoError(t, err)
	assert.Equal(t, "{}", string(keys))
	manifest, err := os.ReadFile(filepath.Join(modDir, "mod.json"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), `"kind": "window"`)
	script, err := os.ReadFile(filepath.Join(modDir, "main.js"))
	require.NoError(t, err)
	assert.Contains(t, string(script), `const HBAY_COMMAND = `+`"`+testExecutable+`";`)
	assert.NotContains(t, string(script), herdrModCommandPlaceholder)

	_, err = renderHerdrModFile("main.js", []byte("// missing placeholder\n"), testExecutable)
	assert.ErrorContains(t, err, "expected 1")
}

func TestSetupHerdrModReportsMissingKeyboardFile(t *testing.T) {
	home := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".config", "herdr-app"), 0o755))
	var out bytes.Buffer
	_, err := setupHerdrMod(testSetupEnvironment(home), &out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "Herdr's keyboard file is missing")
	_, err = os.Stat(filepath.Join(home, ".config", "herdr-app", "keys.json"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestSetupRefusesTemporaryExecutable(t *testing.T) {
	temp := t.TempDir()
	link := filepath.Join(t.TempDir(), "tmp-link")
	require.NoError(t, os.Symlink(temp, link))
	goTemp := t.TempDir()
	installed := t.TempDir()
	cases := []struct {
		name, executable string
		tempDirs         []string
		want             bool
	}{
		{name: "under the temporary folder", executable: filepath.Join(temp, "go-build1", "exe", "hbay"), tempDirs: []string{temp}, want: true},
		{name: "reached through a symlinked temporary folder", executable: filepath.Join(temp, "exe", "hbay"), tempDirs: []string{link}, want: true},
		{name: "under GOTMPDIR", executable: filepath.Join(goTemp, "exe", "hbay"), tempDirs: []string{temp, goTemp}, want: true},
		{name: "installed binary", executable: filepath.Join(installed, "bin", "hbay"), tempDirs: []string{temp, ""}, want: false},
		{name: "the temporary folder's sibling", executable: temp + "-other/hbay", tempDirs: []string{temp}, want: false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, temporaryExecutable(c.executable, c.tempDirs...), c.name)
	}
}

func TestSetupPermissionsAllowJSON(t *testing.T) {
	t.Run("claude settings preserves existing keys and refuses malformed JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), ".claude", "settings.json")
		writeTestFile(t, path, `{"model":"opus","permissions":{"allow":["Bash(git status)"],"deny":["Bash(rm -rf *)"]}}`)
		target := permissionsJSONTarget{
			path:              path,
			rules:             []string{"mcp__highbay__*", "mcp__chorus__*"},
			createWhenMissing: true,
		}
		changed, present, err := mergePermissionsAllowJSON(target)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.True(t, present)
		root := readTestJSON(t, path)
		assert.Equal(t, "opus", root["model"])
		perms := root["permissions"].(map[string]any)
		assert.Equal(t, []any{"Bash(git status)", "mcp__highbay__*", "mcp__chorus__*"}, perms["allow"])
		assert.Equal(t, []any{"Bash(rm -rf *)"}, perms["deny"])

		changed, present, err = mergePermissionsAllowJSON(target)
		require.NoError(t, err)
		assert.False(t, changed)
		assert.True(t, present)

		for _, bad := range []struct{ content, wantErr string }{
			{`{`, "parse JSON"},
			{`{"permissions":[]}`, "permissions must be a JSON object"},
			{`{"permissions":{"allow":{}}}`, "permissions.allow must be a JSON array"},
			{`{"permissions":{"allow":[1]}}`, "permissions.allow must contain strings"},
		} {
			badPath := filepath.Join(t.TempDir(), "settings.json")
			writeTestFile(t, badPath, bad.content)
			_, _, err := mergePermissionsAllowJSON(permissionsJSONTarget{
				path: badPath, rules: []string{"mcp__highbay__*"}, createWhenMissing: true,
			})
			assert.ErrorContains(t, err, bad.wantErr)
			raw, readErr := os.ReadFile(badPath)
			require.NoError(t, readErr)
			assert.Equal(t, bad.content, string(raw))
		}
	})

	t.Run("gemini settings preserves explicit trust false and tool filter arrays across repeat setup", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, ".gemini", "settings.json")
		writeTestFile(t, path, `{"mcpServers":{"highbay":{"command":"/old/hbay","args":["stdio"],"trust":false,"includeTools":["list_changes","get_change"],"excludeTools":["open_views"],"timeout":15000},"other":{"command":"npx"}}}`)

		var out bytes.Buffer
		require.NoError(t, setupGlobal(testSetupEnvironment(home), &out))
		assert.Contains(t, out.String(), "Registered the highbay MCP server in "+path)

		servers := readTestJSON(t, path)["mcpServers"].(map[string]any)
		assert.Equal(t, map[string]any{
			"command":      testExecutable,
			"args":         []any{"stdio"},
			"trust":        false,
			"includeTools": []any{"list_changes", "get_change"},
			"excludeTools": []any{"open_views"},
			"timeout":      float64(15000),
		}, servers["highbay"])
		assert.Equal(t, map[string]any{"command": "npx"}, servers["other"])

		// Repeat setupGlobal run preserves everything untouched and reports already registered.
		before := snapshotTree(t, home)
		out.Reset()
		require.NoError(t, setupGlobal(testSetupEnvironment(home), &out))
		assert.Equal(t, before, snapshotTree(t, home))
		assert.Contains(t, out.String(), "The highbay MCP server is already registered in "+path)
	})

	t.Run("cursor cli-config updates existing files including deny-only configs and respects deny rules", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "cli-config.json")
		target := permissionsJSONTarget{
			path:              path,
			rules:             []string{"Mcp(highbay:*)", "Mcp(chorus:*)"},
			createWhenMissing: false,
		}
		changed, present, err := mergePermissionsAllowJSON(target)
		require.NoError(t, err)
		assert.False(t, changed)
		assert.False(t, present)
		_, statErr := os.Stat(path)
		assert.ErrorIs(t, statErr, os.ErrNotExist)

		writeTestFile(t, path, `{"version":1}`)
		changed, present, err = mergePermissionsAllowJSON(target)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.True(t, present)
		parsed := readTestJSON(t, path)
		assert.Equal(t, float64(1), parsed["version"])
		assert.Equal(t, []any{"Mcp(highbay:*)", "Mcp(chorus:*)"}, parsed["permissions"].(map[string]any)["allow"])

		writeTestFile(t, path, `{"version":1,"permissions":{"deny":["Shell(rm)","Mcp(chorus:*)"]}}`)
		changed, present, err = mergePermissionsAllowJSON(target)
		require.NoError(t, err)
		assert.True(t, changed)
		assert.True(t, present)
		perms := readTestJSON(t, path)["permissions"].(map[string]any)
		assert.Equal(t, []any{"Mcp(highbay:*)"}, perms["allow"])
		assert.Equal(t, []any{"Shell(rm)", "Mcp(chorus:*)"}, perms["deny"])
	})

	t.Run("tool-specific denials preserve deny scope while setupGlobal approves remaining worker tools", func(t *testing.T) {
		home := t.TempDir()
		claudeSettings := filepath.Join(home, ".claude", "settings.json")
		cursorConfig := filepath.Join(home, ".cursor", "cli-config.json")
		writeTestFile(t, claudeSettings,
			`{"permissions":{"deny":["mcp__highbay__open_views","mcp__highbay__start_live_target"]}}`)
		writeTestFile(t, cursorConfig,
			`{"version":1,"permissions":{"deny":["Mcp(highbay:open_views)","Mcp(highbay:start_live_target)"]}}`)

		var out bytes.Buffer
		require.NoError(t, setupGlobal(testSetupEnvironment(home), &out))

		claudePerms := readTestJSON(t, claudeSettings)["permissions"].(map[string]any)
		assert.Equal(t, []any{"mcp__highbay__*", "mcp__chorus__*"}, claudePerms["allow"])
		assert.Equal(t, []any{"mcp__highbay__open_views", "mcp__highbay__start_live_target"}, claudePerms["deny"])

		cursorPerms := readTestJSON(t, cursorConfig)["permissions"].(map[string]any)
		assert.Equal(t, []any{"Mcp(highbay:*)", "Mcp(chorus:*)"}, cursorPerms["allow"])
		assert.Equal(t, []any{"Mcp(highbay:open_views)", "Mcp(highbay:start_live_target)"}, cursorPerms["deny"])

		// Effective client precedence (deny > allow): denied tools stay blocked while required worker tools are approved.
		effectiveAllowed := func(client string, perms map[string]any, server, tool string) bool {
			for _, item := range perms["deny"].([]any) {
				if permissionRuleBlocksTool(client, item.(string), server, tool) {
					return false
				}
			}
			for _, item := range perms["allow"].([]any) {
				if permissionRuleBlocksTool(client, item.(string), server, tool) {
					return true
				}
			}
			return false
		}
		for _, tc := range []struct {
			client string
			perms  map[string]any
		}{
			{"claude", claudePerms},
			{"cursor", cursorPerms},
		} {
			assert.False(t, effectiveAllowed(tc.client, tc.perms, "highbay", "open_views"))
			assert.False(t, effectiveAllowed(tc.client, tc.perms, "highbay", "start_live_target"))
			for _, role := range []string{"builder", "test_writer"} {
				for _, tool := range requiredHighbayTools(role) {
					assert.True(t, effectiveAllowed(tc.client, tc.perms, "highbay", tool), "%s must approve %s for %s", tc.client, tool, role)
				}
			}
		}

		repo, buildDir, testsDir := initTrustGitRepoWithWorktrees(t)
		env := []string{"HOME=" + home}
		for _, client := range []string{"claude", "cursor"} {
			require.NoError(t, prepareWorkerTrust(t.Context(), env, []project.WorkerTrustTarget{
				{Role: "builder", ProjectDir: repo, Folder: buildDir, Client: client, RequireChorus: true},
				{Role: "test_writer", ProjectDir: repo, Folder: testsDir, Client: client, RequireChorus: true},
			}))
		}
	})
}
