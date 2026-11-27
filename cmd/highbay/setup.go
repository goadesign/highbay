// This file implements "highbay setup -global". It registers `highbay stdio` as
// the highbay MCP server in the global configuration of every coding agent it
// detects and installs the /highbay agent skill next to each. "highbay up" runs
// the same setup before it starts.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
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

	"github.com/goadesign/highbay/skills"
)

type (
	// setupEnvironment holds the machine facts setup reads. runSetup fills it
	// from the running process; tests point it at a temporary home so they
	// never touch the user's own configuration.
	setupEnvironment struct {
		// home is the user's home directory. Every default location is under it.
		home string
		// executable is the absolute path of the highbay binary that agents start
		// as `<executable> stdio`.
		executable string
		// getenv reads a variable that moves a client's files, such as
		// CLAUDE_CONFIG_DIR.
		getenv func(string) string
		// onPath reports whether a client's command is installed, so a client
		// that has not created its configuration folder yet is still set up.
		onPath func(string) bool
		// goos selects VS Code's settings folder.
		goos string
	}

	// mcpJSONTarget is one JSON configuration file and the highbay entry its
	// client expects.
	mcpJSONTarget struct {
		// path is the configuration file to create or update.
		path string
		// serversKey names the top-level object holding server entries:
		// "mcpServers" for most clients, "servers" for VS Code.
		serversKey string
		// entry is the object written at serversKey["highbay"].
		entry map[string]any
	}

	// permissionsJSONTarget is a client settings file whose permissions.allow
	// array pre-authorizes Highbay and Chorus MCP tools.
	permissionsJSONTarget struct {
		// path is the settings or CLI configuration file.
		path string
		// rules are the allowlist entries ensured in permissions.allow.
		rules []string
		// createWhenMissing creates path when absent; when false, only an
		// existing non-blank file is updated.
		createWhenMissing bool
	}

	// codexEntryEdit says where mergeCodexConfig writes mcp_servers.highbay and
	// optional mcp_servers.chorus defaults in a Codex config.toml document.
	// Zero-length ranges mean the key is absent.
	codexEntryEdit struct {
		// command is the byte range of an existing command string, quotes included.
		command unstable.Range
		// args is the byte range of an existing args array.
		args unstable.Range
		// approvalMode is the byte range of an existing default_tools_approval_mode string.
		approvalMode unstable.Range
		// envVars is the byte range of an existing env_vars array.
		envVars unstable.Range
		// afterHeader is the offset just past the [mcp_servers.highbay] header
		// line, or 0 when the document has no such table.
		afterHeader int
		// afterLastHighbayLine is the offset just past the last dotted highbay.*
		// key line under [mcp_servers].
		afterLastHighbayLine int
		// chorusAfterHeader is the offset just past a standard
		// [mcp_servers.chorus] header line, or 0 when absent.
		chorusAfterHeader int
		// dotted is true when a dotted key such as highbay.command under
		// [mcp_servers] defines part of the entry.
		dotted bool
	}
)

const (
	// highbayServerName is the MCP server name and skill folder setup writes.
	highbayServerName = "highbay"
)

// runSetup implements `highbay setup -global` for the CLI. It reads the
// process's home, executable and environment, then runs setupGlobal.
// Without -global it explains the flag instead of writing anything.
func runSetup(args []string, stdout io.Writer) error {
	var global bool
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.BoolVar(&global, "global", false, "Register Highbay with every coding agent on this computer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	if !global {
		return errors.New(`highbay setup changes the configuration of every coding agent on this computer; run "highbay setup -global" to do that`)
	}
	env, err := processSetupEnvironment()
	if err != nil {
		return err
	}
	return setupGlobal(env, stdout)
}

// setupGlobal registers `<executable> stdio` as the highbay MCP server in each
// detected coding agent's global configuration, installs the /highbay skill
// for each. It writes one line per change to out. Files that already hold the same entry
// or content are left untouched. A configuration file that is not valid, or
// that defines the highbay entry in a form setup cannot edit, stops setup with
// an error naming the file; files written before it keep their changes.
func setupGlobal(env setupEnvironment, out io.Writer) error {
	lockDir := configLockDir(env.home, env.getenv, env.goos)
	jsonTargets, tomlTargets, permTargets, skillTargets := setupTargets(env)
	if len(jsonTargets) == 0 && len(tomlTargets) == 0 {
		fmt.Fprintln(out, "No coding agent was found on this computer; install one, then run highbay setup -global again.")
	}
	for _, target := range jsonTargets {
		changed, err := mergeMCPJSONConfigWithLock(lockDir, target)
		if err != nil {
			return err
		}
		writeRegistration(out, target.path, changed)
	}
	for _, path := range tomlTargets {
		changed, err := mergeCodexConfigWithLock(lockDir, path, env.executable)
		if err != nil {
			return err
		}
		writeRegistration(out, path, changed)
	}
	for _, target := range permTargets {
		changed, present, err := mergePermissionsAllowJSONWithLock(lockDir, target)
		if err != nil {
			return err
		}
		if present {
			writePermissions(out, target.path, changed)
		}
	}
	for _, path := range skillTargets {
		changed, err := writeIntegrationFile(path, skills.HighbaySkillMD)
		if err != nil {
			return err
		}
		if changed {
			fmt.Fprintf(out, "Installed the /highbay skill in %s\n", path)
		}
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
		return setupEnvironment{}, fmt.Errorf("resolve the highbay executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return setupEnvironment{}, fmt.Errorf("resolve the highbay executable: %w", err)
	}
	if temporaryExecutable(executable, os.TempDir(), os.Getenv("GOTMPDIR")) {
		return setupEnvironment{}, fmt.Errorf("highbay is running from the temporary file %s; install it (go install github.com/goadesign/highbay/cmd/highbay@latest) and run that highbay setup -global", executable)
	}
	return setupEnvironment{
		home:       home,
		executable: registeredProgram(executable, os.Getenv("HIGHBAY_NPM_LAUNCHER")),
		getenv:     os.Getenv,
		onPath: func(name string) bool {
			_, err := exec.LookPath(name)
			return err == nil
		},
		goos: runtime.GOOS,
	}, nil
}

// registeredProgram returns the program coding agents start as
// `<program> stdio`. It is the running executable, except for a Highbay
// installed with npm (npm install -g @goadesign/highbay): then it is npm's
// launcher, named by launcher, which stays in place across updates while the
// executable it downloads and runs is replaced with each version. A launcher
// in npx's cache (a path with an _npx folder) is removed by npm at any time,
// so a Highbay started with npx registers the executable itself.
func registeredProgram(executable, launcher string) string {
	if launcher == "" || !filepath.IsAbs(launcher) {
		return executable
	}
	for _, folder := range strings.Split(filepath.ToSlash(filepath.Clean(launcher)), "/") {
		if folder == "_npx" {
			return executable
		}
	}
	return launcher
}

// temporaryExecutable reports whether executable lies under one of the
// temporary folders, where `go run` builds. Symlinks are resolved on both
// sides, since macOS reaches /var and /tmp through /private. An empty folder
// name is skipped.
func temporaryExecutable(executable string, tempDirs ...string) bool {
	exe := resolveExistingPrefix(executable)
	for _, dir := range tempDirs {
		if dir == "" {
			continue
		}
		if rel, err := filepath.Rel(resolveExistingPrefix(dir), exe); err == nil && filepath.IsLocal(rel) {
			return true
		}
	}
	return false
}

// resolveExistingPrefix resolves symlinks in the longest existing leading
// part of path and appends the rest unchanged.
func resolveExistingPrefix(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	parent := filepath.Dir(path)
	if parent == path {
		return path
	}
	return filepath.Join(resolveExistingPrefix(parent), filepath.Base(path))
}

// setupTargets lists the configuration and skill files of the coding agents
// installed under env.home: Claude Code, OpenAI Codex, Cursor, Antigravity,
// Gemini CLI, Windsurf, GitHub Copilot CLI and VS Code. The universal
// ~/.agents skill folder is always included.
func setupTargets(env setupEnvironment) ([]mcpJSONTarget, []string, []permissionsJSONTarget, []string) {
	var (
		home         = env.home
		jsonTargets  []mcpJSONTarget
		tomlTargets  []string
		permTargets  []permissionsJSONTarget
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

	// Claude Code keeps user servers in .claude.json, tool permissions in
	// settings.json, and skills in skills/, under CLAUDE_CONFIG_DIR when set.
	claudeDir := filepath.Join(home, ".claude")
	claudeConfig := filepath.Join(home, ".claude.json")
	if dir := env.getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		claudeDir = dir
		claudeConfig = filepath.Join(dir, ".claude.json")
	}
	if isDir(claudeDir) || exists(claudeConfig) || env.onPath("claude") {
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: claudeConfig, serversKey: "mcpServers", entry: stdioEntry()})
		permTargets = append(permTargets, permissionsJSONTarget{
			path:              filepath.Join(claudeDir, "settings.json"),
			rules:             []string{"mcp__highbay__*", "mcp__chorus__*"},
			createWhenMissing: true,
		})
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
		} else {
			permTargets = append(permTargets, permissionsJSONTarget{
				path:              filepath.Join(dir, "cli-config.json"),
				rules:             []string{"Mcp(highbay:*)", "Mcp(chorus:*)"},
				createWhenMissing: false,
			})
		}
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: config, serversKey: "mcpServers", entry: stdioEntry()})
		skillTargets = append(skillTargets, skillPath(dir))
	}
	// Gemini CLI resolves under GEMINI_CLI_HOME/.gemini when set, else ~/.gemini.
	// When sharing ~/.gemini with Antigravity's ~/.gemini/config, it counts as
	// installed only through its own settings, skills or command.
	geminiDir := geminiConfigDir(home, env.getenv("GEMINI_CLI_HOME"))
	geminiSettings := filepath.Join(geminiDir, "settings.json")
	if exists(geminiSettings) || isDir(filepath.Join(geminiDir, "skills")) || env.onPath("gemini") {
		entry := stdioEntry()
		entry["trust"] = true
		jsonTargets = append(jsonTargets, mcpJSONTarget{path: geminiSettings, serversKey: "mcpServers", entry: entry})
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
	return jsonTargets, tomlTargets, permTargets, skillTargets
}

// skillPath is where a client whose folder is dir reads the /highbay skill.
func skillPath(dir string) string {
	return filepath.Join(dir, "skills", highbayServerName, "SKILL.md")
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
		fmt.Fprintf(out, "Registered the highbay MCP server in %s\n", path)
		return
	}
	fmt.Fprintf(out, "The highbay MCP server is already registered in %s\n", path)
}

// writePermissions reports whether setup updated a client's tool permissions.
func writePermissions(out io.Writer, path string, changed bool) {
	if changed {
		fmt.Fprintf(out, "Configured Highbay tool permissions in %s\n", path)
		return
	}
	fmt.Fprintf(out, "Highbay tool permissions are already configured in %s\n", path)
}

// mergePermissionsAllowJSON ensures target.rules appear in permissions.allow
// inside target.path when not blocked by permissions.deny or permissions.ask,
// preserving every other key, number representation and permission list. When
// target.createWhenMissing is false, a missing or blank file is left untouched,
// while an existing JSON object without permissions or allow receives them.
func mergePermissionsAllowJSON(target permissionsJSONTarget) (changed, present bool, err error) {
	return mergePermissionsAllowJSONWithLock("", target)
}

// mergePermissionsAllowJSONWithLock runs mergePermissionsAllowJSON under lockDir.
func mergePermissionsAllowJSONWithLock(
	lockDir string, target permissionsJSONTarget,
) (changed, present bool, err error) {
	path := target.path
	changed, err = updateLockedConfigFile(context.Background(), lockDir, path, nil, 0o644, func(raw []byte, existed bool) ([]byte, bool, error) {
		if (!existed || len(bytes.TrimSpace(raw)) == 0) && !target.createWhenMissing {
			present = false
			return nil, false, nil
		}
		present = true
		root, err := decodeConfigJSONBytes(path, raw)
		if err != nil {
			return nil, false, err
		}
		perms := make(map[string]any)
		if existing, has := root["permissions"]; has {
			var ok bool
			perms, ok = existing.(map[string]any)
			if !ok || perms == nil {
				return nil, false, fmt.Errorf("%s: permissions must be a JSON object", path)
			}
		}
		var allow []any
		if existing, has := perms["allow"]; has {
			var ok bool
			allow, ok = existing.([]any)
			if !ok || allow == nil {
				return nil, false, fmt.Errorf("%s: permissions.allow must be a JSON array", path)
			}
		}
		existingRules := make([]string, 0, len(allow))
		for _, item := range allow {
			s, ok := item.(string)
			if !ok {
				return nil, false, fmt.Errorf("%s: permissions.allow must contain strings", path)
			}
			existingRules = append(existingRules, s)
		}
		added := false
		for _, rule := range target.rules {
			if slices.Contains(existingRules, rule) || permissionListsBlockCandidateRule(perms, rule) {
				continue
			}
			allow = append(allow, rule)
			existingRules = append(existingRules, rule)
			added = true
		}
		if !added {
			return nil, false, nil
		}
		perms["allow"] = allow
		root["permissions"] = perms
		encoded, err := encodeConfigJSONBytes(path, root)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	})
	if err != nil {
		return false, false, err
	}
	return changed, present, nil
}

// mergeMCPJSONConfig updates target.serversKey["highbay"] to Highbay's stdio
// transport in a JSON configuration file while preserving every other key,
// server, number representation, and user-configured highbay entry fields such as
// includeTools, excludeTools, env, timeout, and explicit trust.
func mergeMCPJSONConfig(target mcpJSONTarget) (bool, error) {
	return mergeMCPJSONConfigWithLock("", target)
}

// mergeMCPJSONConfigWithLock runs mergeMCPJSONConfig under lockDir.
func mergeMCPJSONConfigWithLock(lockDir string, target mcpJSONTarget) (bool, error) {
	path := target.path
	return updateLockedConfigFile(context.Background(), lockDir, path, nil, 0o644, func(raw []byte, _ bool) ([]byte, bool, error) {
		root, err := decodeConfigJSONBytes(path, raw)
		if err != nil {
			return nil, false, err
		}
		servers := make(map[string]any)
		if existing, present := root[target.serversKey]; present {
			var ok bool
			if servers, ok = existing.(map[string]any); !ok || servers == nil {
				return nil, false, fmt.Errorf("%s: %s must be a JSON object", path, target.serversKey)
			}
		}
		var desired map[string]any
		if existingRaw, hasExisting := servers[highbayServerName]; hasExisting {
			existingServer, ok := existingRaw.(map[string]any)
			if !ok || existingServer == nil {
				return nil, false, fmt.Errorf("%s: %s.%s must be a JSON object", path, target.serversKey, highbayServerName)
			}
			desired = maps.Clone(existingServer)
			delete(desired, "url")
			delete(desired, "httpUrl")
			delete(desired, "headers")
			if transportType, hasType := target.entry["type"]; hasType {
				desired["type"] = transportType
			} else {
				delete(desired, "type")
			}
			desired["command"] = target.entry["command"]
			desired["args"] = target.entry["args"]
			for key, val := range target.entry {
				if key == "command" || key == "args" || key == "type" {
					continue
				}
				if _, alreadySet := desired[key]; !alreadySet {
					desired[key] = val
				}
			}
		} else {
			desired = maps.Clone(target.entry)
		}
		if sameJSON(servers[highbayServerName], desired) {
			return nil, false, nil
		}
		servers[highbayServerName] = desired
		root[target.serversKey] = servers
		encoded, err := encodeConfigJSONBytes(path, root)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	})
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

// mergeCodexConfig sets mcp_servers.highbay to command = executable,
// args = ["stdio"], env_vars containing "HC_TERMINAL_ID" (preserving both plain
// strings and structured { name = ..., source = ... } items), and adds
// default_tools_approval_mode = "approve" to [mcp_servers.highbay] and an
// existing standard [mcp_servers.chorus] table only when no approval mode is
// set yet. Every other byte stays unchanged so comments, formatting, explicit
// approval policies, other servers and other highbay keys such as env survive.
func mergeCodexConfig(path, executable string) (bool, error) {
	return mergeCodexConfigWithLock("", path, executable)
}

// mergeCodexConfigWithLock runs mergeCodexConfig under lockDir.
func mergeCodexConfigWithLock(lockDir, path, executable string) (bool, error) {
	commandValue := strconv.Quote(executable)
	const (
		argsValue       = `["stdio"]`
		approvalValue   = `"approve"`
		defaultEnvVars  = `["HC_TERMINAL_ID"]`
		terminalEnvName = "HC_TERMINAL_ID"
	)
	byHand := fmt.Sprintf("set [mcp_servers.highbay] command = %s and args = %s by hand", commandValue, argsValue)

	return updateLockedConfigFile(context.Background(), lockDir, path, nil, 0o600, func(content []byte, _ bool) ([]byte, bool, error) {
		// Decoding checks what the position-aware parser does not: duplicate keys,
		// redefined tables and type conflicts, which Codex itself refuses.
		var decoded map[string]any
		if err := toml.Unmarshal(content, &decoded); err != nil {
			return nil, false, fmt.Errorf("%s is not valid TOML: %w; %s", path, err, byHand)
		}
		servers := map[string]any{}
		if existing, present := decoded["mcp_servers"]; present {
			var ok bool
			if servers, ok = existing.(map[string]any); !ok {
				return nil, false, fmt.Errorf("%s: mcp_servers is not a table; %s", path, byHand)
			}
		}
		var (
			highbayEntry      map[string]any
			highbayHasApprove bool
			envVarsMatched    bool
		)
		if entry, ok := servers[highbayServerName].(map[string]any); ok {
			highbayEntry = entry
			if _, ok := entry["url"]; ok {
				return nil, false, fmt.Errorf("%s: mcp_servers.highbay has a url; remove it and %s", path, byHand)
			}
			_, highbayHasApprove = entry["default_tools_approval_mode"]
			if rawVars, present := entry["env_vars"]; present {
				matched, err := codexEnvVarsHasName(rawVars, terminalEnvName)
				if err != nil {
					return nil, false, fmt.Errorf("%s: %w; %s", path, err, byHand)
				}
				envVarsMatched = matched
			}
		}
		highbayMatched := highbayEntry != nil &&
			highbayEntry["command"] == executable &&
			reflect.DeepEqual(highbayEntry["args"], []any{"stdio"}) &&
			highbayHasApprove &&
			envVarsMatched

		chorusEntry, hasChorus := servers["chorus"].(map[string]any)
		_, chorusHasApproval := chorusEntry["default_tools_approval_mode"]
		if highbayMatched && (!hasChorus || chorusHasApproval) {
			return nil, false, nil
		}

		edit, err := locateCodexHighbayEntry(content)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w; %s", path, err, byHand)
		}
		chorusNeedsApproval := hasChorus && !chorusHasApproval && edit.chorusAfterHeader > 0
		if highbayMatched && !chorusNeedsApproval {
			return nil, false, nil
		}

		type replacement struct {
			at, length int
			text       string
		}
		applyReplacements := func(base []byte, edits []replacement) []byte {
			if len(edits) == 0 {
				return slices.Clone(base)
			}
			slices.SortFunc(edits, func(a, b replacement) int { return b.at - a.at })
			result := slices.Clone(base)
			for _, r := range edits {
				text := r.text
				if r.length == 0 && r.at > 0 && result[r.at-1] != '\n' {
					text = "\n" + text
				}
				result = slices.Concat(result[:r.at], []byte(text), result[r.at+r.length:])
			}
			return result
		}

		var edits []replacement
		if chorusNeedsApproval {
			edits = append(edits, replacement{edit.chorusAfterHeader, 0, "default_tools_approval_mode = " + approvalValue + "\n"})
		}

		var out []byte
		switch {
		case highbayMatched:
			out = applyReplacements(content, edits)
		case edit.afterHeader == 0 && !edit.dotted:
			base := applyReplacements(content, edits)
			out = append(out, bytes.TrimRight(base, " \t\r\n")...)
			if len(out) > 0 {
				out = append(out, "\n\n"...)
			}
			out = append(out, "[mcp_servers.highbay]\n"+
				"command = "+commandValue+"\n"+
				"args = "+argsValue+"\n"+
				"default_tools_approval_mode = "+approvalValue+"\n"+
				"env_vars = "+defaultEnvVars+"\n"...)
		case edit.afterHeader == 0 && (edit.command.Length == 0 || edit.args.Length == 0):
			return nil, false, fmt.Errorf("%s: mcp_servers.highbay is defined with dotted keys missing command or args; %s", path, byHand)
		default:
			keyPrefix := ""
			insertAt := edit.afterHeader
			if edit.dotted {
				keyPrefix = highbayServerName + "."
				insertAt = edit.afterLastHighbayLine
			}
			var missing string
			if edit.command.Length > 0 {
				if highbayEntry["command"] != executable {
					edits = append(edits, replacement{int(edit.command.Offset), int(edit.command.Length), commandValue})
				}
			} else {
				missing += keyPrefix + "command = " + commandValue + "\n"
			}
			if edit.args.Length > 0 {
				if !reflect.DeepEqual(highbayEntry["args"], []any{"stdio"}) {
					edits = append(edits, replacement{int(edit.args.Offset), int(edit.args.Length), argsValue})
				}
			} else {
				missing += keyPrefix + "args = " + argsValue + "\n"
			}
			if edit.approvalMode.Length == 0 && !highbayHasApprove {
				missing += keyPrefix + "default_tools_approval_mode = " + approvalValue + "\n"
			}
			if edit.envVars.Length > 0 {
				if !envVarsMatched {
					rawArray := content[edit.envVars.Offset : edit.envVars.Offset+edit.envVars.Length]
					spliced, spliceErr := appendTOMLArrayString(rawArray, terminalEnvName)
					if spliceErr != nil {
						return nil, false, fmt.Errorf("%s: %w; %s", path, spliceErr, byHand)
					}
					edits = append(edits, replacement{int(edit.envVars.Offset), int(edit.envVars.Length), string(spliced)})
				}
			} else {
				missing += keyPrefix + "env_vars = " + defaultEnvVars + "\n"
			}
			if missing != "" {
				edits = append(edits, replacement{insertAt, 0, missing})
			}
			out = applyReplacements(content, edits)
		}
		var verified map[string]any
		if err := toml.Unmarshal(out, &verified); err != nil {
			return nil, false, fmt.Errorf("%s: validate updated TOML: %w; %s", path, err, byHand)
		}
		return out, true, nil
	})
}

// codexEnvVarsHasName validates a Codex env_vars array (accepting both string
// entries and inline tables with a non-empty string "name") and reports whether
// targetName is already listed.
func codexEnvVarsHasName(rawVars any, targetName string) (bool, error) {
	items, ok := rawVars.([]any)
	if !ok {
		return false, errors.New("mcp_servers.highbay.env_vars is not an array")
	}
	found := false
	for _, item := range items {
		switch entry := item.(type) {
		case string:
			if entry == targetName {
				found = true
			}
		case map[string]any:
			name, ok := entry["name"].(string)
			if !ok || name == "" {
				return false, errors.New(`mcp_servers.highbay.env_vars must contain strings or { name = "..." } tables`)
			}
			if name == targetName {
				found = true
			}
		default:
			return false, errors.New(`mcp_servers.highbay.env_vars must contain strings or { name = "..." } tables`)
		}
	}
	return found, nil
}

// appendTOMLArrayString appends value as a quoted string to a validated TOML
// array literal `[ ... ]`, preserving existing items (including inline tables),
// quotes, comments and multiline indentation.
func appendTOMLArrayString(rawArray []byte, value string) ([]byte, error) {
	if len(rawArray) < 2 || rawArray[0] != '[' || rawArray[len(rawArray)-1] != ']' {
		return nil, errors.New("mcp_servers.highbay.env_vars cannot be edited in place")
	}
	quoted := strconv.Quote(value)
	lastIdx := lastTOMLArrayContentIndex(rawArray)
	multiline := bytes.ContainsRune(rawArray, '\n')

	if lastIdx == 0 {
		if !multiline {
			return []byte("[" + quoted + "]"), nil
		}
		closeLineStart := bytes.LastIndexByte(rawArray[:len(rawArray)-1], '\n') + 1
		closeIndent := leadingIndent(rawArray[closeLineStart : len(rawArray)-1])
		insertion := closeIndent + "  " + quoted + "\n"
		return slices.Concat(rawArray[:closeLineStart], []byte(insertion), rawArray[closeLineStart:]), nil
	}

	if !multiline {
		sep := ", "
		if rawArray[lastIdx] == ',' {
			sep = " "
		}
		return slices.Concat(rawArray[:lastIdx+1], []byte(sep+quoted), rawArray[lastIdx+1:]), nil
	}

	closeLineStart := bytes.LastIndexByte(rawArray[:len(rawArray)-1], '\n') + 1
	if closeLineStart <= lastIdx {
		sep := ", "
		if rawArray[lastIdx] == ',' {
			sep = " "
		}
		return slices.Concat(rawArray[:lastIdx+1], []byte(sep+quoted), rawArray[lastIdx+1:]), nil
	}

	elemLineStart := bytes.LastIndexByte(rawArray[:lastIdx], '\n') + 1
	elemIndent := leadingIndent(rawArray[elemLineStart:lastIdx])
	if rawArray[lastIdx] == ',' {
		insertion := elemIndent + quoted + ",\n"
		return slices.Concat(rawArray[:closeLineStart], []byte(insertion), rawArray[closeLineStart:]), nil
	}
	withComma := slices.Concat(rawArray[:lastIdx+1], []byte(","), rawArray[lastIdx+1:])
	shiftedCloseLine := closeLineStart + 1
	insertion := elemIndent + quoted + "\n"
	return slices.Concat(withComma[:shiftedCloseLine], []byte(insertion), withComma[shiftedCloseLine:]), nil
}

// lastTOMLArrayContentIndex returns the byte index inside rawArray (`[...]`) of
// the last non-whitespace, non-comment token character, or 0 when empty.
func lastTOMLArrayContentIndex(rawArray []byte) int {
	lastIdx := 0
	end := len(rawArray) - 1
	for i := 1; i < end; i++ {
		switch {
		case rawArray[i] == '#':
			for i < end && rawArray[i] != '\n' {
				i++
			}
		case bytes.HasPrefix(rawArray[i:], []byte(`"""`)):
			i += 3
			for i < end && !bytes.HasPrefix(rawArray[i:], []byte(`"""`)) {
				if rawArray[i] == '\\' && i+1 < end {
					i++
				}
				i++
			}
			if i+2 < end {
				i += 2
				lastIdx = i
			}
		case bytes.HasPrefix(rawArray[i:], []byte(`'''`)):
			i += 3
			for i < end && !bytes.HasPrefix(rawArray[i:], []byte(`'''`)) {
				i++
			}
			if i+2 < end {
				i += 2
				lastIdx = i
			}
		case rawArray[i] == '"':
			i++
			for i < end && rawArray[i] != '"' {
				if rawArray[i] == '\\' && i+1 < end {
					i++
				}
				i++
			}
			if i < end {
				lastIdx = i
			}
		case rawArray[i] == '\'':
			i++
			for i < end && rawArray[i] != '\'' {
				i++
			}
			if i < end {
				lastIdx = i
			}
		case rawArray[i] == ' ' || rawArray[i] == '\t' || rawArray[i] == '\r' || rawArray[i] == '\n':
			continue
		default:
			lastIdx = i
		}
	}
	return lastIdx
}

// leadingIndent returns the leading spaces and tabs of line.
func leadingIndent(line []byte) string {
	end := 0
	for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
		end++
	}
	return string(line[:end])
}

// locateCodexHighbayEntry parses a Codex config.toml and reports where
// mergeCodexConfig writes mcp_servers.highbay and any standard
// [mcp_servers.chorus] header. It returns an error for invalid TOML and for
// entries it cannot edit in place. Tables nested under the entry, such as
// [mcp_servers.highbay.env], are left as they are.
func locateCodexHighbayEntry(doc []byte) (codexEntryEdit, error) {
	var (
		edit         codexEntryEdit
		table        []string
		parser       unstable.Parser
		entry        = []string{"mcp_servers", highbayServerName}
		chorusEntry  = []string{"mcp_servers", "chorus"}
		command      = []string{"mcp_servers", highbayServerName, "command"}
		args         = []string{"mcp_servers", highbayServerName, "args"}
		approvalMode = []string{"mcp_servers", highbayServerName, "default_tools_approval_mode"}
		envVars      = []string{"mcp_servers", highbayServerName, "env_vars"}
	)
	afterLine := func(offset int) int {
		if nl := bytes.IndexByte(doc[offset:], '\n'); nl >= 0 {
			return offset + nl + 1
		}
		return len(doc)
	}
	parser.Reset(doc)
	for parser.NextExpression() {
		expr := parser.Expression()
		switch expr.Kind {
		case unstable.Table, unstable.ArrayTable:
			var last *unstable.Node
			table, last = tomlKeyPath(nil, expr.Key())
			if slices.Equal(table, chorusEntry) && expr.Kind == unstable.Table {
				edit.chorusAfterHeader = afterLine(int(last.Raw.Offset + last.Raw.Length))
				continue
			}
			if !slices.Equal(table, entry) {
				continue
			}
			if expr.Kind == unstable.ArrayTable {
				return edit, errors.New("mcp_servers.highbay is defined as an array of tables")
			}
			edit.afterHeader = afterLine(int(last.Raw.Offset + last.Raw.Length))
		case unstable.KeyValue:
			key, last := tomlKeyPath(slices.Clone(table), expr.Key())
			value := expr.Value()
			switch {
			case slices.Equal(key, command):
				if value.Kind != unstable.String {
					return edit, errors.New("mcp_servers.highbay.command is not a string")
				}
				edit.command = tomlValueRange(doc, expr, last)
			case slices.Equal(key, args):
				if value.Kind != unstable.Array {
					return edit, errors.New("mcp_servers.highbay.args is not an array")
				}
				edit.args = tomlValueRange(doc, expr, last)
			case slices.Equal(key, approvalMode):
				if value.Kind != unstable.String {
					return edit, errors.New("mcp_servers.highbay.default_tools_approval_mode is not a string")
				}
				edit.approvalMode = tomlValueRange(doc, expr, last)
			case slices.Equal(key, envVars):
				if value.Kind != unstable.Array {
					return edit, errors.New("mcp_servers.highbay.env_vars is not an array")
				}
				edit.envVars = tomlValueRange(doc, expr, last)
			case slices.Equal(key, entry) || slices.Equal(key, entry[:1]):
				return edit, errors.New("mcp_servers or mcp_servers.highbay is an inline table")
			}
			if len(key) > 2 && slices.Equal(key[:2], entry) && len(table) < 2 {
				edit.dotted = true
				edit.afterLastHighbayLine = afterLine(int(expr.Raw.Offset + expr.Raw.Length))
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
func tomlValueRange(doc []byte, expr, lastKey *unstable.Node) unstable.Range {
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
	file, err := os.CreateTemp(filepath.Dir(target), ".highbay-setup-*")
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

// writeIntegrationFile installs bundled skill content at path, creating
// folders as needed, and reports whether it wrote. The file is replaced
// atomically so a client never reads a partly written one; identical files
// are left alone.
func writeIntegrationFile(path string, content []byte) (bool, error) {
	installed, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	if err == nil && bytes.Equal(installed, content) {
		return false, nil
	}
	if err := replaceConfigFile(path, content, 0o644); err != nil {
		return false, err
	}
	return true, nil
}
