// This file implements "surla setup -global". It registers `surla stdio` as
// the surla MCP server in the global configuration of every coding agent it
// detects and installs the /surla agent skill next to each. When Herdr is
// present it also installs the Surla companion window. "surla up" runs the
// same setup before it starts.
package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/goadesign/surla/skills"
)

type (
	// setupEnvironment holds the machine facts setup reads. runSetup fills it
	// from the running process; tests point it at a temporary home so they
	// never touch the user's own configuration.
	setupEnvironment struct {
		// home is the user's home directory. Every default location is under it.
		home string
		// executable is the absolute path of the surla binary that agents start
		// as `<executable> stdio`.
		executable string
		// getenv reads a variable that moves a client's files, such as
		// CLAUDE_CONFIG_DIR or HERDR_APP_HOME.
		getenv func(string) string
		// onPath reports whether a client's command is installed, so a client
		// that has not created its configuration folder yet is still set up.
		onPath func(string) bool
		// goos selects VS Code's settings folder and the macOS-only Herdr shortcut.
		goos string
	}

	// mcpJSONTarget is one JSON configuration file and the surla entry its
	// client expects.
	mcpJSONTarget struct {
		// path is the configuration file to create or update.
		path string
		// serversKey names the top-level object holding server entries:
		// "mcpServers" for most clients, "servers" for VS Code.
		serversKey string
		// entry is the object written at serversKey["surla"].
		entry map[string]any
	}

	// codexEntryEdit says where mergeCodexConfig writes mcp_servers.surla in a
	// Codex config.toml document. Zero-length ranges mean the key is absent.
	codexEntryEdit struct {
		// command is the byte range of an existing command string, quotes included.
		command unstable.Range
		// args is the byte range of an existing args array.
		args unstable.Range
		// afterHeader is the offset just past the [mcp_servers.surla] header
		// line, or 0 when the document has no such table.
		afterHeader int
		// dotted is true when a dotted key such as surla.command under
		// [mcp_servers] defines part of the entry.
		dotted bool
	}

	// herdrShortcutBinding describes an existing assignment of the companion's
	// key. Command arguments stay private to the keyboard file.
	herdrShortcutBinding struct {
		// section is the keyboard context that already uses the key.
		section string
		// command names the action without its arguments.
		command string
		// opensCompanion is true only for a single keypress that opens this mod.
		opensCompanion bool
	}
)

const (
	// surlaServerName is the MCP server name and skill folder setup writes.
	surlaServerName = "surla"
	// herdrModName is the companion's folder under Herdr's mods.
	herdrModName = "surla"
	// herdrShortcut opens the companion without taking a common terminal key.
	herdrShortcut = "cmd-alt-shift-s"
	// herdrShortcutAction is Herdr's command shorthand that toggles the companion.
	herdrShortcutAction = "mod.open:surla"
)

// runSetup implements `surla setup -global` for the CLI. It reads the
// process's home, executable and environment, then runs setupGlobal.
// Without -global it explains the flag instead of writing anything.
func runSetup(args []string, stdout io.Writer) error {
	var global bool
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.BoolVar(&global, "global", false, "Register Surla with every coding agent on this computer and install the Herdr companion")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if !global {
		return errors.New(`surla setup changes the configuration of every coding agent on this computer; run "surla setup -global" to do that`)
	}
	env, err := processSetupEnvironment()
	if err != nil {
		return err
	}
	return setupGlobal(env, stdout)
}

// setupGlobal registers `<executable> stdio` as the surla MCP server in each
// detected coding agent's global configuration, installs the /surla skill
// for each, and installs the Herdr companion when Herdr's folder exists. It
// writes one line per change to out. Files that already hold the same entry
// or content are left untouched. A configuration file that is not valid, or
// that defines the surla entry in a form setup cannot edit, stops setup with
// an error naming the file; files written before it keep their changes.
func setupGlobal(env setupEnvironment, out io.Writer) error {
	jsonTargets, tomlTargets, skillTargets := setupTargets(env)
	if len(jsonTargets) == 0 && len(tomlTargets) == 0 {
		fmt.Fprintln(out, "No coding agent was found on this computer; install one, then run surla setup -global again.")
	}
	for _, target := range jsonTargets {
		changed, err := mergeMCPJSONConfig(target)
		if err != nil {
			return err
		}
		writeRegistration(out, target.path, changed)
	}
	for _, path := range tomlTargets {
		changed, err := mergeCodexConfig(path, env.executable)
		if err != nil {
			return err
		}
		writeRegistration(out, path, changed)
	}
	for _, path := range skillTargets {
		changed, err := writeIntegrationFile(path, skills.SurlaSkillMD)
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintf(out, "Installed the /surla skill in %s\n", path)
		}
	}
	modDir, err := setupHerdrMod(env, out)
	if err != nil {
		return fmt.Errorf("install the Surla companion in Herdr: %w", err)
	}
	if modDir != "" {
		fmt.Fprintf(out, "Surla companion is in %s; open it with herdr-app do mod.open --name %s\n", modDir, herdrModName)
	}
	return nil
}

// processSetupEnvironment reads the facts setup needs from this process. The
// executable must be an installed binary: a `go run` build lives in a
// temporary folder that disappears, and agents would fail to start it later.
func processSetupEnvironment() (setupEnvironment, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return setupEnvironment{}, fmt.Errorf("resolve home directory: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return setupEnvironment{}, fmt.Errorf("resolve the surla executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return setupEnvironment{}, fmt.Errorf("resolve the surla executable: %w", err)
	}
	if rel, err := filepath.Rel(os.TempDir(), executable); err == nil && filepath.IsLocal(rel) {
		return setupEnvironment{}, fmt.Errorf("surla is running from the temporary file %s; install it (go install github.com/goadesign/surla/cmd/surla@latest) and run that surla setup -global", executable)
	}
	return setupEnvironment{
		home:       home,
		executable: executable,
		getenv:     os.Getenv,
		onPath: func(name string) bool {
			_, err := exec.LookPath(name)
			return err == nil
		},
		goos: runtime.GOOS,
	}, nil
}

// setupTargets lists the configuration and skill files of the coding agents
// installed under env.home: Claude Code, OpenAI Codex, Cursor, Antigravity,
// Gemini CLI, Windsurf, GitHub Copilot CLI and VS Code. The universal
// ~/.agents skill folder is always included.
func setupTargets(env setupEnvironment) ([]mcpJSONTarget, []string, []string) {
	var (
		home         = env.home
		jsonTargets  []mcpJSONTarget
		tomlTargets  []string
		skillTargets = []string{skillPath(filepath.Join(home, ".agents"))}
		stdioEntry   = func() map[string]any {
			return map[string]any{"command": env.executable, "args": []any{"stdio"}}
		}
		isDir = func(path string) bool {
			info, err := os.Stat(path)
			return err == nil && info.IsDir()
		}
		exists = func(path string) bool {
			_, err := os.Stat(path)
			return err == nil
		}
	)

	// Claude Code keeps user servers in .claude.json and skills in skills/,
	// both under CLAUDE_CONFIG_DIR when it is set.
	claudeDir := filepath.Join(home, ".claude")
	claudeConfig := filepath.Join(home, ".claude.json")
	if dir := env.getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		claudeDir = dir
		claudeConfig = filepath.Join(dir, ".claude.json")
	}
	if isDir(claudeDir) || exists(claudeConfig) || env.onPath("claude") {
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: claudeConfig, serversKey: "mcpServers", entry: stdioEntry()})
		skillTargets = append(skillTargets, skillPath(claudeDir))
	}
	codexDir := cmp.Or(env.getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
	if isDir(codexDir) || env.onPath("codex") {
		tomlTargets = append(tomlTargets, filepath.Join(codexDir, "config.toml"))
		skillTargets = append(skillTargets, skillPath(codexDir))
	}
	for _, dir := range []string{
		filepath.Join(home, ".cursor"),
		filepath.Join(home, ".gemini", "config"),
		filepath.Join(home, ".codeium", "windsurf"),
	} {
		if !isDir(dir) {
			continue
		}
		config := filepath.Join(dir, "mcp.json")
		if filepath.Base(dir) != ".cursor" {
			config = filepath.Join(dir, "mcp_config.json")
		}
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: config, serversKey: "mcpServers", entry: stdioEntry()})
		skillTargets = append(skillTargets, skillPath(dir))
	}
	// Gemini CLI shares ~/.gemini with Antigravity's ~/.gemini/config, so it
	// counts as installed only through its own settings, skills or command.
	geminiDir := filepath.Join(home, ".gemini")
	geminiSettings := filepath.Join(geminiDir, "settings.json")
	if exists(geminiSettings) || isDir(filepath.Join(geminiDir, "skills")) || env.onPath("gemini") {
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: geminiSettings, serversKey: "mcpServers", entry: stdioEntry()})
		skillTargets = append(skillTargets, skillPath(geminiDir))
	}
	copilotDir := cmp.Or(env.getenv("COPILOT_HOME"), filepath.Join(home, ".copilot"))
	if isDir(copilotDir) || env.onPath("copilot") {
		entry := stdioEntry()
		entry["type"] = "local"
		entry["tools"] = []any{"*"}
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: filepath.Join(copilotDir, "mcp-config.json"), serversKey: "mcpServers", entry: entry})
		skillTargets = append(skillTargets, skillPath(copilotDir))
	}
	if dir := vscodeUserDir(home, env.goos); dir != "" && isDir(dir) {
		entry := stdioEntry()
		entry["type"] = "stdio"
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: filepath.Join(dir, "mcp.json"), serversKey: "servers", entry: entry})
	}
	return jsonTargets, tomlTargets, skillTargets
}

// skillPath is where a client whose folder is dir reads the /surla skill.
func skillPath(dir string) string {
	return filepath.Join(dir, "skills", surlaServerName, "SKILL.md")
}

// vscodeUserDir returns VS Code's global User settings folder on macOS and
// Linux, or "" on other systems.
func vscodeUserDir(home, goos string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Code", "User")
	case "linux":
		return filepath.Join(home, ".config", "Code", "User")
	default:
		return ""
	}
}

// writeRegistration reports whether setup changed a client's configuration.
func writeRegistration(out io.Writer, path string, changed bool) {
	if changed {
		fmt.Fprintf(out, "Registered the surla MCP server in %s\n", path)
		return
	}
	fmt.Fprintf(out, "The surla MCP server is already registered in %s\n", path)
}

// mergeMCPJSONConfig sets target.serversKey["surla"] to target.entry in a JSON
// configuration file, keeping every other key and server. Numbers keep their
// exact text. A missing or empty file starts from an empty object. It writes
// nothing and reports false when the entry is already equal. It returns an
// error, writing nothing, when the file is not one JSON object or its server
// map is not an object, so a broken file is reported instead of replaced.
func mergeMCPJSONConfig(target mcpJSONTarget) (bool, error) {
	path := target.path
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	root := make(map[string]any)
	if len(bytes.TrimSpace(raw)) > 0 {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&root); err != nil {
			return false, fmt.Errorf("parse JSON in %s: %w", path, err)
		}
		if root == nil {
			return false, fmt.Errorf("%s must contain a JSON object, not null", path)
		}
		if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("%s must contain one JSON object", path)
		}
	}
	servers := make(map[string]any)
	if existing, present := root[target.serversKey]; present {
		var ok bool
		if servers, ok = existing.(map[string]any); !ok || servers == nil {
			return false, fmt.Errorf("%s: %s must be a JSON object", path, target.serversKey)
		}
	}
	if sameJSON(servers[surlaServerName], target.entry) {
		return false, nil
	}
	servers[surlaServerName] = target.entry
	root[target.serversKey] = servers

	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(root); err != nil {
		return false, fmt.Errorf("encode JSON for %s: %w", path, err)
	}
	if err := replaceConfigFile(path, encoded.Bytes(), 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// sameJSON reports whether existing, decoded from a file, already equals
// want once both are in JSON's decoded form.
func sameJSON(existing any, want map[string]any) bool {
	if existing == nil {
		return false
	}
	encoded, err := json.Marshal(want)
	if err != nil {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err := decoder.Decode(&normalized); err != nil {
		return false
	}
	return reflect.DeepEqual(existing, normalized)
}

// mergeCodexConfig sets mcp_servers.surla to command = executable and
// args = ["stdio"] in an OpenAI Codex config.toml, leaving every other byte
// unchanged so comments, formatting, other servers and other surla keys such
// as env survive. An existing value is replaced in place, a missing key is
// added after the [mcp_servers.surla] header, and a missing entry is appended
// as a new table. It reports false, writing nothing, when the entry already
// matches. It returns an error, writing nothing, when the file is not valid
// TOML or defines the entry in a form it cannot edit in place (an inline
// table, an array of tables, dotted keys missing command or args, a url
// transport, or values of the wrong type); the message names the setting to
// write by hand.
func mergeCodexConfig(path, executable string) (bool, error) {
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	commandValue := strconv.Quote(executable)
	const argsValue = `["stdio"]`
	byHand := fmt.Sprintf("set [mcp_servers.surla] command = %s and args = %s by hand", commandValue, argsValue)

	// Decoding checks what the position-aware parser does not: duplicate keys,
	// redefined tables and type conflicts, which Codex itself refuses.
	var decoded map[string]any
	if err := toml.Unmarshal(content, &decoded); err != nil {
		return false, fmt.Errorf("%s is not valid TOML: %w; %s", path, err, byHand)
	}
	servers, _ := decoded["mcp_servers"].(map[string]any)
	if entry, ok := servers[surlaServerName].(map[string]any); ok {
		if _, ok := entry["url"]; ok {
			return false, fmt.Errorf("%s: mcp_servers.surla has a url; remove it and %s", path, byHand)
		}
		if entry["command"] == executable && reflect.DeepEqual(entry["args"], []any{"stdio"}) {
			return false, nil
		}
	}
	edit, err := locateCodexSurlaEntry(content)
	if err != nil {
		return false, fmt.Errorf("%s: %w; %s", path, err, byHand)
	}

	var out []byte
	switch {
	case edit.afterHeader == 0 && !edit.dotted:
		out = append(out, bytes.TrimRight(content, " \t\r\n")...)
		if len(out) > 0 {
			out = append(out, "\n\n"...)
		}
		out = append(out, "[mcp_servers.surla]\ncommand = "+commandValue+"\nargs = "+argsValue+"\n"...)
	case edit.afterHeader == 0 && (edit.command.Length == 0 || edit.args.Length == 0):
		return false, fmt.Errorf("%s: mcp_servers.surla is defined with dotted keys missing command or args; %s", path, byHand)
	default:
		type replacement struct {
			at, length int
			text       string
		}
		var edits []replacement
		var missing string
		if edit.command.Length > 0 {
			edits = append(edits, replacement{int(edit.command.Offset), int(edit.command.Length), commandValue})
		} else {
			missing += "command = " + commandValue + "\n"
		}
		if edit.args.Length > 0 {
			edits = append(edits, replacement{int(edit.args.Offset), int(edit.args.Length), argsValue})
		} else {
			missing += "args = " + argsValue + "\n"
		}
		if missing != "" {
			edits = append(edits, replacement{edit.afterHeader, 0, missing})
		}
		// Apply from the end so earlier offsets stay valid.
		slices.SortFunc(edits, func(a, b replacement) int { return b.at - a.at })
		out = slices.Clone(content)
		for _, r := range edits {
			text := r.text
			if r.length == 0 && r.at > 0 && out[r.at-1] != '\n' {
				text = "\n" + text
			}
			out = slices.Concat(out[:r.at], []byte(text), out[r.at+r.length:])
		}
	}
	if err := replaceConfigFile(path, out, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

// locateCodexSurlaEntry parses a Codex config.toml and reports where
// mergeCodexConfig writes mcp_servers.surla. It returns an error for invalid
// TOML and for entries it cannot edit in place. Tables nested under the entry,
// such as [mcp_servers.surla.env], are left as they are.
func locateCodexSurlaEntry(doc []byte) (codexEntryEdit, error) {
	var (
		edit    codexEntryEdit
		table   []string
		parser  unstable.Parser
		entry   = []string{"mcp_servers", surlaServerName}
		command = []string{"mcp_servers", surlaServerName, "command"}
		args    = []string{"mcp_servers", surlaServerName, "args"}
	)
	parser.Reset(doc)
	for parser.NextExpression() {
		expr := parser.Expression()
		switch expr.Kind {
		case unstable.Table, unstable.ArrayTable:
			var last *unstable.Node
			table, last = tomlKeyPath(nil, expr.Key())
			if !slices.Equal(table, entry) {
				continue
			}
			if expr.Kind == unstable.ArrayTable {
				return edit, errors.New("mcp_servers.surla is defined as an array of tables")
			}
			end := int(last.Raw.Offset + last.Raw.Length)
			if nl := bytes.IndexByte(doc[end:], '\n'); nl >= 0 {
				edit.afterHeader = end + nl + 1
			} else {
				edit.afterHeader = len(doc)
			}
		case unstable.KeyValue:
			key, last := tomlKeyPath(slices.Clone(table), expr.Key())
			value := expr.Value()
			switch {
			case slices.Equal(key, command):
				if value.Kind != unstable.String {
					return edit, errors.New("mcp_servers.surla.command is not a string")
				}
				edit.command = tomlValueRange(doc, expr, last)
			case slices.Equal(key, args):
				if value.Kind != unstable.Array {
					return edit, errors.New("mcp_servers.surla.args is not an array")
				}
				edit.args = tomlValueRange(doc, expr, last)
			case slices.Equal(key, entry) || slices.Equal(key, entry[:1]):
				return edit, errors.New("mcp_servers or mcp_servers.surla is an inline table")
			}
			if len(key) > 2 && slices.Equal(key[:2], entry) && len(table) < 2 {
				edit.dotted = true
			}
		}
	}
	if err := parser.Error(); err != nil {
		return edit, fmt.Errorf("not valid TOML: %w", err)
	}
	return edit, nil
}

// tomlValueRange returns the bytes of a key/value expression's value: from
// the first non-blank byte after the '=' that follows the key's last segment
// to the end of the expression. The parser records no range for arrays, and
// the expression's own range ends where its value ends, before any comment.
func tomlValueRange(doc []byte, expr *unstable.Node, lastKey *unstable.Node) unstable.Range {
	start := int(lastKey.Raw.Offset + lastKey.Raw.Length)
	start += bytes.IndexByte(doc[start:], '=') + 1
	for start < len(doc) && (doc[start] == ' ' || doc[start] == '\t') {
		start++
	}
	end := int(expr.Raw.Offset + expr.Raw.Length)
	return unstable.Range{Offset: uint32(start), Length: uint32(end - start)}
}

// tomlKeyPath appends the decoded segments of a TOML key (bare, quoted or
// dotted) to prefix and returns the path with the last segment's node, whose
// Raw range ends the key in the input.
func tomlKeyPath(prefix []string, it unstable.Iterator) ([]string, *unstable.Node) {
	var last *unstable.Node
	for it.Next() {
		last = it.Node()
		prefix = append(prefix, string(last.Data))
	}
	return prefix, last
}

// setupHerdrMod installs the companion's runtime files into Herdr's existing
// home folder (HERDR_APP_HOME, else ~/.config/herdr-app), which Herdr creates
// on its first start. Only mod.json and main.js are written, manifest first;
// identical files are left alone because Herdr reloads a mod whenever its
// files are written. On macOS the shortcut is added when its key is free. It
// returns the mod folder, or "" when Herdr has no home folder.
func setupHerdrMod(env setupEnvironment, out io.Writer) (string, error) {
	herdrHome := cmp.Or(env.getenv("HERDR_APP_HOME"), filepath.Join(env.home, ".config", "herdr-app"))
	info, err := os.Stat(herdrHome)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("inspect Herdr configuration %s: %w", herdrHome, err)
	}
	if !info.IsDir() {
		return "", nil
	}
	modDir := filepath.Join(herdrHome, "mods", herdrModName)
	for _, name := range []string{"mod.json", "main.js"} {
		content, err := skills.HerdrMod.ReadFile("herdr/surla/" + name)
		if err != nil {
			return "", fmt.Errorf("read bundled %s: %w", name, err)
		}
		if _, err := writeIntegrationFile(filepath.Join(modDir, name), content); err != nil {
			return "", err
		}
	}
	if env.goos == "darwin" {
		if err := setupHerdrShortcut(herdrHome, out); err != nil {
			return "", err
		}
	}
	return modDir, nil
}

// setupHerdrShortcut adds cmd-alt-shift-s to Herdr's global keyboard section
// when no section binds that key yet. A missing or read-only keyboard file is
// reported and left alone; a malformed one is an error and is not written.
// Editing the file makes Herdr treat it as the user's own on later upgrades.
func setupHerdrShortcut(herdrHome string, out io.Writer) error {
	path := filepath.Join(herdrHome, "keys.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(out, "Herdr's keyboard file is missing; open Herdr once and run surla setup -global again to add the shortcut.")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Herdr keyboard settings %s: %w", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("inspect Herdr keyboard settings %s: %w", path, err)
	}
	if info.Mode().Perm()&0o222 == 0 {
		fmt.Fprintf(out, "Herdr keyboard settings %s are read-only; kept them unchanged.\n", path)
		return nil
	}
	updated, existing, err := addHerdrShortcut(raw)
	if err != nil {
		return fmt.Errorf("parse Herdr keyboard settings %s: %w", path, err)
	}
	if existing != nil {
		if existing.opensCompanion {
			fmt.Fprintf(out, "Herdr shortcut %s already opens the Surla companion.\n", herdrShortcut)
		} else {
			fmt.Fprintf(out, "Herdr shortcut %s is taken by %q in %q; kept that binding.\n", herdrShortcut, existing.command, existing.section)
		}
		return nil
	}
	if err := replaceConfigFile(path, updated, info.Mode().Perm()); err != nil {
		return err
	}
	fmt.Fprintf(out, "Added Herdr shortcut %s to open the Surla companion. Herdr now treats keys.json as your own.\n", herdrShortcut)
	return nil
}

// addHerdrShortcut validates a whole keyboard document and adds the companion
// binding to its global object, creating that object when absent. Any binding
// of the same first keypress in any section prevents the insertion and is
// returned instead. Bytes outside the insertion stay unchanged.
func addHerdrShortcut(raw []byte) ([]byte, *herdrShortcutBinding, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil, errors.New("keyboard settings file is empty")
		}
		return nil, nil, err
	}
	if token != json.Delim('{') {
		return nil, nil, errors.New("keyboard settings must contain a JSON object")
	}
	var existing *herdrShortcutBinding
	var rootHasMembers, globalHasMembers bool
	globalClose := -1
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, nil, err
		}
		section := token.(string)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, nil, err
		}
		rootHasMembers = true
		if section != "global" && section != "terminal" && !strings.HasPrefix(section, "layout:") && !strings.HasPrefix(section, "mod:") {
			continue
		}
		var bindings map[string]json.RawMessage
		if err := json.Unmarshal(value, &bindings); err != nil || bindings == nil {
			return nil, nil, fmt.Errorf("%s must contain a JSON object", section)
		}
		if section == "global" {
			if globalClose >= 0 {
				return nil, nil, errors.New("keyboard settings contain more than one global section")
			}
			globalClose = int(decoder.InputOffset()) - 1
			globalHasMembers = len(bindings) > 0
		}
		for chord, action := range bindings {
			if !herdrShortcutMatches(chord) {
				continue
			}
			binding := herdrBindingFor(section, chord, action)
			// A conflicting binding wins over one that opens the companion,
			// because it can intercept the key in its context.
			if existing == nil || !binding.opensCompanion {
				existing = binding
			}
		}
	}
	if _, err := decoder.Token(); err != nil {
		return nil, nil, err
	}
	rootClose := int(decoder.InputOffset()) - 1
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, nil, errors.New("keyboard settings must contain one JSON object")
	}
	if existing != nil {
		return raw, existing, nil
	}
	member := fmt.Sprintf("%q: %q", herdrShortcut, herdrShortcutAction)
	if globalClose >= 0 {
		return insertJSONMember(raw, globalClose, globalHasMembers, member), nil, nil
	}
	member = fmt.Sprintf("%q: {%s}", "global", member)
	return insertJSONMember(raw, rootClose, rootHasMembers, member), nil, nil
}

// herdrBindingFor describes the action bound to chord in section, which is
// either the "command:arg" shorthand or a {command, args} object.
func herdrBindingFor(section, chord string, action json.RawMessage) *herdrShortcutBinding {
	var name string
	var object struct {
		// Command is the app action selected by this binding.
		Command string `json:"command"`
		// Args holds the action's named arguments without re-encoding them.
		Args map[string]json.RawMessage `json:"args"`
	}
	opens := false
	if json.Unmarshal(action, &name) == nil {
		opens = name == herdrShortcutAction
		name, _, _ = strings.Cut(name, ":")
	} else if json.Unmarshal(action, &object) == nil {
		name = object.Command
		var mod string
		opens = object.Command == "mod.open" && len(object.Args) == 1 && json.Unmarshal(object.Args["name"], &mod) == nil && mod == herdrModName
	}
	return &herdrShortcutBinding{
		section:        section,
		command:        name,
		opensCompanion: opens && len(strings.Fields(chord)) == 1,
	}
}

// herdrShortcutMatches reports whether chord starts with cmd-alt-shift-s,
// accepting Herdr's modifier aliases and the implicit shift of an uppercase
// letter. A sequence that starts with this keypress also occupies it.
func herdrShortcutMatches(chord string) bool {
	strokes := strings.Fields(chord)
	if len(strokes) == 0 {
		return false
	}
	parts := strings.Split(strokes[0], "-")
	key := parts[len(parts)-1]
	if key != "s" && key != "S" {
		return false
	}
	held := make(map[string]bool)
	for _, modifier := range parts[:len(parts)-1] {
		modifier = strings.ToLower(modifier)
		switch modifier {
		case "secondary", "super", "win":
			modifier = "cmd"
		case "option":
			modifier = "alt"
		}
		held[modifier] = true
	}
	if key == "S" {
		held["shift"] = true
	}
	return len(held) == 3 && held["cmd"] && held["alt"] && held["shift"]
}

// insertJSONMember adds member before a validated object's closing brace at
// closeOffset, matching the object's existing indentation and line endings
// and leaving every other byte unchanged.
func insertJSONMember(raw []byte, closeOffset int, hasMembers bool, member string) []byte {
	at := closeOffset
	for at > 0 && strings.ContainsRune(" \t\r\n", rune(raw[at-1])) {
		at--
	}
	separator := ""
	if hasMembers {
		separator = ","
	}
	var insertion string
	if bytes.ContainsRune(raw[at:closeOffset], '\n') {
		line := bytes.LastIndexByte(raw[:closeOffset], '\n') + 1
		indent := string(raw[line:closeOffset])
		memberIndent := indent + "  "
		if hasMembers {
			previous := bytes.LastIndexByte(raw[:at], '\n') + 1
			end := previous
			for end < at && (raw[end] == ' ' || raw[end] == '\t') {
				end++
			}
			memberIndent = string(raw[previous:end])
		}
		newline := "\n"
		if bytes.Contains(raw[at:closeOffset], []byte("\r\n")) {
			newline = "\r\n"
		}
		insertion = separator + newline + memberIndent + member + newline + indent
	} else {
		insertion = separator + member + string(raw[at:closeOffset])
	}
	return slices.Concat(raw[:at], []byte(insertion), raw[closeOffset:])
}

// replaceConfigFile atomically replaces the file at path with content,
// following a symlink to its target so dotfile managers keep their link. An
// existing file keeps its permissions; a new one gets perm and any missing
// parent folders.
func replaceConfigFile(path string, content []byte, perm os.FileMode) (err error) {
	target := path
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		target = resolved
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	if info, err := os.Stat(target); err == nil {
		perm = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect %s: %w", target, err)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create folder for %s: %w", target, err)
	}
	file, err := os.CreateTemp(filepath.Dir(target), ".surla-setup-*")
	if err != nil {
		return fmt.Errorf("create update for %s: %w", target, err)
	}
	defer func() {
		if cleanupErr := os.Remove(file.Name()); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary file for %s: %w", target, cleanupErr))
		}
	}()
	writeErr := file.Chmod(perm)
	if writeErr == nil {
		_, writeErr = file.Write(content)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if err := errors.Join(writeErr, file.Close()); err != nil {
		return fmt.Errorf("write %s: %w", target, err)
	}
	if err := os.Rename(file.Name(), target); err != nil {
		return fmt.Errorf("replace %s: %w", target, err)
	}
	return nil
}

// writeIntegrationFile installs bundled skill or mod content at path,
// creating folders as needed, and reports whether it wrote. Identical files
// are left untouched: Herdr reloads a mod whenever its files are written.
func writeIntegrationFile(path string, content []byte) (bool, error) {
	installed, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil && bytes.Equal(installed, content) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, fmt.Errorf("create folder for %s: %w", path, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return false, fmt.Errorf("write %s: %w", path, err)
	}
	return true, nil
}
