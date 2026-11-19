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

	"github.com/goadesign/surla/skills"
)

// testExecutable is the surla path setup tests register.
const testExecutable = "/opt/surla/bin/surla"

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
	jsonConfigs := []struct {
		path, serversKey string
		want             map[string]any
	}{
		{".claude.json", "mcpServers", stdio},
		{".cursor/mcp.json", "mcpServers", stdio},
		{".gemini/config/mcp_config.json", "mcpServers", stdio},
		{".gemini/settings.json", "mcpServers", stdio},
		{".codeium/windsurf/mcp_config.json", "mcpServers", stdio},
		{".copilot/mcp-config.json", "mcpServers", map[string]any{"type": "local", "command": testExecutable, "args": []any{"stdio"}, "tools": []any{"*"}}},
		{"Library/Application Support/Code/User/mcp.json", "servers", map[string]any{"type": "stdio", "command": testExecutable, "args": []any{"stdio"}}},
	}
	for _, c := range jsonConfigs {
		servers, _ := readTestJSON(t, filepath.Join(home, c.path))[c.serversKey].(map[string]any)
		assert.Equal(t, c.want, servers["surla"], c.path)
	}
	codex, err := os.ReadFile(filepath.Join(home, ".codex", "config.toml"))
	require.NoError(t, err)
	assert.Equal(t, "[mcp_servers.surla]\ncommand = \"/opt/surla/bin/surla\"\nargs = [\"stdio\"]\n", string(codex))
	for _, dir := range []string{".agents", ".claude", ".codex", ".cursor", ".gemini/config", ".gemini", ".codeium/windsurf", ".copilot"} {
		skill, err := os.ReadFile(filepath.Join(home, dir, "skills", "surla", "SKILL.md"))
		require.NoError(t, err, dir)
		assert.Equal(t, skills.SurlaSkillMD, skill, dir)
	}

	modDir := filepath.Join(herdrHome, "mods", "surla")
	entries, err := os.ReadDir(modDir)
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	assert.Equal(t, []string{"main.js", "mod.json"}, names, "only runtime files are installed")
	keys := readTestJSON(t, filepath.Join(herdrHome, "keys.json"))
	assert.Equal(t, map[string]any{"cmd-k": "app.palette", "cmd-alt-shift-s": "mod.open:surla"}, keys["global"])
	assert.Contains(t, out.String(), "Added Herdr shortcut cmd-alt-shift-s")

	// A second run finds everything in place and writes nothing.
	before := snapshotTree(t, home)
	out.Reset()
	require.NoError(t, setupGlobal(testSetupEnvironment(home), &out))
	assert.Equal(t, before, snapshotTree(t, home))
	assert.Equal(t, 8, strings.Count(out.String(), "is already registered in"), out.String())
	assert.NotContains(t, out.String(), "Installed")
	assert.Contains(t, out.String(), "Herdr shortcut cmd-alt-shift-s already opens the Surla companion.")
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
	env := testSetupEnvironment(home)
	env.goos = "linux"
	env.getenv = func(name string) string {
		return map[string]string{"CLAUDE_CONFIG_DIR": claudeDir, "CODEX_HOME": codexDir}[name]
	}
	env.onPath = func(name string) bool { return name == "claude" || name == "codex" || name == "gemini" }

	jsonTargets, tomlTargets, skillTargets := setupTargets(env)
	var paths []string
	for _, target := range jsonTargets {
		paths = append(paths, target.path)
	}
	assert.Equal(t, []string{filepath.Join(claudeDir, ".claude.json"), filepath.Join(home, ".gemini", "settings.json")}, paths)
	assert.Equal(t, []string{filepath.Join(codexDir, "config.toml")}, tomlTargets)
	assert.Equal(t, []string{
		filepath.Join(home, ".agents", "skills", "surla", "SKILL.md"),
		filepath.Join(claudeDir, "skills", "surla", "SKILL.md"),
		filepath.Join(codexDir, "skills", "surla", "SKILL.md"),
		filepath.Join(home, ".gemini", "skills", "surla", "SKILL.md"),
	}, skillTargets)

	// Antigravity's ~/.gemini/config alone does not make Gemini CLI a target.
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".gemini", "config"), 0o755))
	jsonTargets, _, _ = setupTargets(testSetupEnvironment(home))
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
	_, ok := files[filepath.Join(home, ".agents", "skills", "surla", "SKILL.md")]
	assert.True(t, ok)
}

func TestRunSetupRequiresGlobal(t *testing.T) {
	err := runSetup(nil, &bytes.Buffer{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `run "surla setup -global"`)
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
			content: `{"numStartups": 12345678901234567890, "tip": "<b>&</b>", "mcpServers": {"chorus": {"type": "http", "url": "http://127.0.0.1:9440/mcp"}, "surla": {"type": "http", "url": "http://old"}}}`,
			changed: true,
			check: func(t *testing.T, root map[string]any, raw string) {
				assert.Contains(t, raw, "12345678901234567890")
				assert.Contains(t, raw, `"<b>&</b>"`)
				servers := root["mcpServers"].(map[string]any)
				assert.Equal(t, map[string]any{"type": "http", "url": "http://127.0.0.1:9440/mcp"}, servers["chorus"])
			},
		},
		{name: "already registered", content: `{"mcpServers": {"surla": {"args": ["stdio"], "command": "/opt/surla/bin/surla"}}}`, changed: false},
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
			assert.Equal(t, entry, root["mcpServers"].(map[string]any)["surla"])
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
	const table = "[mcp_servers.surla]\ncommand = \"/opt/surla/bin/surla\"\nargs = [\"stdio\"]\n"
	cases := []struct {
		name, content, want string
		changed             bool
		wantErr             string
	}{
		{name: "missing file", want: table, changed: true},
		{
			name:    "appends after other settings",
			content: "# my settings\nmodel = \"gpt\"\n\n[mcp_servers.chorus]\nurl = \"http://127.0.0.1:9440/mcp\"\n\n",
			want:    "# my settings\nmodel = \"gpt\"\n\n[mcp_servers.chorus]\nurl = \"http://127.0.0.1:9440/mcp\"\n\n" + table,
			changed: true,
		},
		{
			name:    "replaces values in place and keeps other keys",
			content: "[mcp_servers.surla]\ncommand = 'old/surla' # pinned\nargs = [\"serve\", \"-x\"]\nstartup_timeout_sec = 30\n\n[mcp_servers.surla.env]\nA = \"1\"\n",
			want:    "[mcp_servers.surla]\ncommand = \"/opt/surla/bin/surla\" # pinned\nargs = [\"stdio\"]\nstartup_timeout_sec = 30\n\n[mcp_servers.surla.env]\nA = \"1\"\n",
			changed: true,
		},
		{
			name:    "adds missing keys after the header",
			content: "[mcp_servers.surla]\nstartup_timeout_sec = 30\n",
			want:    "[mcp_servers.surla]\ncommand = \"/opt/surla/bin/surla\"\nargs = [\"stdio\"]\nstartup_timeout_sec = 30\n",
			changed: true,
		},
		{
			name:    "adds a key after a header at the end of the file",
			content: "[mcp_servers.surla]",
			want:    "[mcp_servers.surla]\ncommand = \"/opt/surla/bin/surla\"\nargs = [\"stdio\"]\n",
			changed: true,
		},
		{
			name:    "replaces dotted keys",
			content: "[mcp_servers]\nsurla.command = \"old\"\nsurla.args = []\n",
			want:    "[mcp_servers]\nsurla.command = \"/opt/surla/bin/surla\"\nsurla.args = [\"stdio\"]\n",
			changed: true,
		},
		{
			name:    "replaces a multiline array with comments",
			content: "[mcp_servers.surla]\nargs = [\n  \"serve\", # old ]\n  \"-x\",\n] # end\ncommand = \"/opt/surla/bin/surla\"\n",
			want:    "[mcp_servers.surla]\nargs = [\"stdio\"] # end\ncommand = \"/opt/surla/bin/surla\"\n",
			changed: true,
		},
		{name: "already registered", content: "# keep\n" + table, changed: false},
		{name: "dotted keys missing args", content: "[mcp_servers]\nsurla.command = \"old\"\n", wantErr: "dotted keys missing command or args"},
		{name: "inline table", content: "[mcp_servers]\nsurla = { command = \"old\" }\n", wantErr: "inline table"},
		{name: "array of tables", content: "[[mcp_servers.surla]]\ncommand = \"old\"\n", wantErr: "array of tables"},
		{name: "servers as an array of tables", content: "[[mcp_servers]]\nname = \"x\"\n", wantErr: "mcp_servers is not a table"},
		{name: "servers as a string", content: "mcp_servers = \"x\"\n", wantErr: "mcp_servers is not a table"},
		{name: "url transport", content: "[mcp_servers.surla]\nurl = \"http://old\"\n", wantErr: "has a url"},
		{name: "command not a string", content: "[mcp_servers.surla]\ncommand = 3\n", wantErr: "command is not a string"},
		{name: "invalid TOML", content: "[mcp_servers.surla\n", wantErr: "is not valid TOML"},
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
				assert.ErrorContains(t, err, `set [mcp_servers.surla] command = "/opt/surla/bin/surla" and args = ["stdio"] by hand`)
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
			want:   "{\n  \"global\": {\n    \"cmd-k\": \"app.palette\",\n    \"cmd-alt-shift-s\": \"mod.open:surla\"\n  }\n}\n",
			output: "Added Herdr shortcut cmd-alt-shift-s",
		},
		{
			name:   "creates the global section",
			keys:   "{\"terminal\": {\"ctrl-c\": \"terminal.copy\"}}",
			want:   "{\"terminal\": {\"ctrl-c\": \"terminal.copy\"},\"global\": {\"cmd-alt-shift-s\": \"mod.open:surla\"}}",
			output: "Added Herdr shortcut",
		},
		{name: "keeps a conflicting binding", keys: `{"terminal": {"super-option-S": "terminal.split"}}`, output: `is taken by "terminal.split" in "terminal"`},
		{name: "keeps a sequence starting with the key", keys: `{"global": {"cmd-alt-shift-s g": "app.help"}}`, output: `is taken by "app.help"`},
		{name: "already opens the companion", keys: `{"global": {"cmd-alt-shift-s": {"command": "mod.open", "args": {"name": "surla"}}}}`, output: "already opens the Surla companion"},
		{name: "another key is free", keys: `{"global": {"cmd-alt-s": "app.help", "cmd-alt-shift-c": "mod.open:chorus-launcher"}}`, want: `{"global": {"cmd-alt-s": "app.help", "cmd-alt-shift-c": "mod.open:chorus-launcher","cmd-alt-shift-s": "mod.open:surla"}}`, output: "Added"},
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
	assert.Equal(t, filepath.Join(herdrHome, "mods", "surla"), modDir)
	keys, err := os.ReadFile(filepath.Join(herdrHome, "keys.json"))
	require.NoError(t, err)
	assert.Equal(t, "{}", string(keys))
	manifest, err := os.ReadFile(filepath.Join(modDir, "mod.json"))
	require.NoError(t, err)
	assert.Contains(t, string(manifest), `"kind": "window"`)
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
		{name: "under the temporary folder", executable: filepath.Join(temp, "go-build1", "exe", "surla"), tempDirs: []string{temp}, want: true},
		{name: "reached through a symlinked temporary folder", executable: filepath.Join(temp, "exe", "surla"), tempDirs: []string{link}, want: true},
		{name: "under GOTMPDIR", executable: filepath.Join(goTemp, "exe", "surla"), tempDirs: []string{temp, goTemp}, want: true},
		{name: "installed binary", executable: filepath.Join(installed, "bin", "surla"), tempDirs: []string{temp, ""}, want: false},
		{name: "the temporary folder's sibling", executable: temp + "-other/surla", tempDirs: []string{temp}, want: false},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, temporaryExecutable(c.executable, c.tempDirs...), c.name)
	}
}
