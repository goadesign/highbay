// This file checks explicit client permission policies and pre-populates
// missing user-scoped folder trust outside git working copies before project
// roles launch.
package main

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/goadesign/surla/internal/project"
	"github.com/goadesign/surla/internal/testwriter"
)

type (
	// codexProjectLocation records where a single [projects.<folder>] entry
	// lives in a Codex config.toml document.
	codexProjectLocation struct {
		// afterHeader is the byte offset after [projects.<folder>], or 0 when absent.
		afterHeader int
		// trustLevel is the byte range of an existing trust_level string value.
		trustLevel unstable.Range
		// uneditable is true when projects.<folder> uses an inline or array table.
		uneditable bool
	}

	// clientTrustPlan groups the folders and policy requirements for one coding client.
	clientTrustPlan struct {
		// folders lists unique absolute folders needing folder trust when Yolo is false.
		folders []string
		// checkPolicy is true when at least one role for this client runs with Yolo false.
		checkPolicy bool
		// requireChorus is true when at least one non-Yolo role uses Chorus.
		requireChorus bool
	}
)

// prepareWorkerTrust checks read-only client permission policies and writes
// only missing user-scoped folder-trust entries resolved from env (falling back
// to os.UserHomeDir when HOME is absent in env). It never writes settings.json,
// cli-config.json, MCP server entries, or any path inside project or worktree folders.
func prepareWorkerTrust(env []string, targets []project.WorkerTrustTarget) error {
	if len(targets) == 0 {
		return nil
	}
	home := envLookup(env, "HOME")
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve home directory for worker trust: %w", err)
		}
	}
	getenv := func(name string) string { return envLookup(env, name) }
	lockDir := configLockDir(home, getenv, runtime.GOOS)

	protectedRoots := collectProtectedRoots(targets)
	plans, clientOrder := buildClientTrustPlans(targets)

	// Validate destinations and check explicit client restrictions before any write.
	for _, client := range clientOrder {
		plan := plans[client]
		if err := validateClientConfigPaths(client, home, getenv, protectedRoots, plan.folders); err != nil {
			return err
		}
		if !plan.checkPolicy {
			continue
		}
		if err := checkClientWorkerPolicy(client, home, getenv, plan); err != nil {
			return err
		}
	}

	var errs error
	for _, client := range clientOrder {
		plan := plans[client]
		if len(plan.folders) == 0 {
			continue
		}
		switch client {
		case "codex":
			codexDir := cmp.Or(getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
			errs = errors.Join(errs, mergeCodexFolderTrust(
				lockDir, filepath.Join(codexDir, "config.toml"), protectedRoots, plan.folders,
			))
		case "claude":
			claudeConfig := filepath.Join(home, ".claude.json")
			if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
				claudeConfig = filepath.Join(dir, ".claude.json")
			}
			errs = errors.Join(errs, mergeClaudeProjectTrust(lockDir, claudeConfig, protectedRoots, plan.folders))
		case "gemini":
			trustedPath := geminiTrustedFoldersPath(home, getenv)
			errs = errors.Join(errs, mergeGeminiTrustedFolders(lockDir, trustedPath, protectedRoots, plan.folders))
		case "cursor":
			errs = errors.Join(errs, mergeCursorTrustedWorkspaces(home, protectedRoots, plan.folders))
		}
		var policyErr *project.WorkerPolicyError
		if errors.As(errs, &policyErr) {
			return policyErr
		}
	}
	return errs
}

// bindPrepareWorkerTrust adapts prepareWorkerTrust to project.TeamDependencies.
func bindPrepareWorkerTrust(env []string) func(context.Context, []project.WorkerTrustTarget) error {
	return func(_ context.Context, targets []project.WorkerTrustTarget) error {
		return prepareWorkerTrust(env, targets)
	}
}

// geminiConfigDir resolves Gemini CLI's configuration directory beneath
// GEMINI_CLI_HOME when set, or beneath home otherwise.
func geminiConfigDir(home, geminiCLIHome string) string {
	return filepath.Join(cmp.Or(geminiCLIHome, home), ".gemini")
}

// geminiTrustedFoldersPath resolves Gemini CLI's trusted-folders file, honoring
// GEMINI_CLI_TRUSTED_FOLDERS_PATH before falling back to GEMINI_CLI_HOME/.gemini.
func geminiTrustedFoldersPath(home string, getenv func(string) string) string {
	if custom := getenv("GEMINI_CLI_TRUSTED_FOLDERS_PATH"); custom != "" {
		return custom
	}
	return filepath.Join(geminiConfigDir(home, getenv("GEMINI_CLI_HOME")), "trustedFolders.json")
}

// collectProtectedRoots lists project and worker working-copy roots where client
// configuration files and lockfiles must never be created.
func collectProtectedRoots(targets []project.WorkerTrustTarget) []string {
	var roots []string
	add := func(dir string) {
		if !filepath.IsAbs(dir) {
			return
		}
		cleaned := filepath.Clean(dir)
		if !slices.Contains(roots, cleaned) {
			roots = append(roots, cleaned)
		}
	}
	for _, target := range targets {
		if filepath.IsAbs(target.ProjectDir) {
			proj := filepath.Clean(target.ProjectDir)
			add(proj)
			add(filepath.Join(proj, testwriter.BuilderRole.Folder))
			add(filepath.Join(proj, testwriter.TestsRole.Folder))
		}
		add(target.Folder)
	}
	return roots
}

// buildClientTrustPlans aggregates non-Yolo folders and policy checks per client.
func buildClientTrustPlans(
	targets []project.WorkerTrustTarget,
) (map[string]clientTrustPlan, []string) {
	plans := make(map[string]clientTrustPlan)
	var order []string
	for _, target := range targets {
		if target.Client == "" {
			continue
		}
		plan, seen := plans[target.Client]
		if !seen {
			order = append(order, target.Client)
		}
		if !target.Yolo {
			plan.checkPolicy = true
			if target.RequireChorus {
				plan.requireChorus = true
			}
			for _, candidate := range []string{target.ProjectDir, target.Folder} {
				if !filepath.IsAbs(candidate) {
					continue
				}
				cleaned := filepath.Clean(candidate)
				if !slices.Contains(plan.folders, cleaned) {
					plan.folders = append(plan.folders, cleaned)
				}
			}
		}
		plans[target.Client] = plan
	}
	return plans, order
}

// validateClientConfigPaths verifies that every configuration file for client
// resolves outside project and worker working trees before any read or write.
func validateClientConfigPaths(
	client, home string,
	getenv func(string) string,
	protectedRoots, folders []string,
) error {
	var paths []string
	switch client {
	case "codex":
		codexDir := cmp.Or(getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
		paths = append(paths, filepath.Join(codexDir, "config.toml"))
	case "claude":
		claudeDir := filepath.Join(home, ".claude")
		claudeConfig := filepath.Join(home, ".claude.json")
		if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			claudeDir = dir
			claudeConfig = filepath.Join(dir, ".claude.json")
		}
		paths = append(paths, claudeConfig, filepath.Join(claudeDir, "settings.json"))
	case "gemini":
		geminiDir := geminiConfigDir(home, getenv("GEMINI_CLI_HOME"))
		paths = append(paths, filepath.Join(geminiDir, "settings.json"), geminiTrustedFoldersPath(home, getenv))
	case "cursor":
		paths = append(paths, filepath.Join(home, ".cursor", "cli-config.json"))
		for _, folder := range folders {
			if slug := cursorWorkspaceSlug(folder); slug != "" {
				paths = append(paths, filepath.Join(home, ".cursor", "projects", slug, ".workspace-trusted"))
			}
		}
	}
	for _, path := range paths {
		if _, err := validateConfigDestination(path, protectedRoots); err != nil {
			return err
		}
	}
	return nil
}

// checkClientWorkerPolicy performs read-only checks on explicit user settings
// that block non-Yolo workers from calling required Surla or Chorus MCP tools
// or trusting the project folder. Remedies name only the exact file and scoped
// setting to update, never suggesting permission-bypass flags.
func checkClientWorkerPolicy(
	client, home string,
	getenv func(string) string,
	plan clientTrustPlan,
) error {
	requiredServers := []string{surlaServerName}
	if plan.requireChorus {
		requiredServers = append(requiredServers, "chorus")
	}

	switch client {
	case "codex":
		codexDir := cmp.Or(getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
		return checkCodexWorkerPolicy(filepath.Join(codexDir, "config.toml"), requiredServers, plan.folders)
	case "claude":
		claudeDir := cmp.Or(getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
		return checkPermissionsFilePolicy("claude", "Claude Code", filepath.Join(claudeDir, "settings.json"), requiredServers)
	case "gemini":
		geminiDir := geminiConfigDir(home, getenv("GEMINI_CLI_HOME"))
		if err := checkGeminiSettingsPolicy(filepath.Join(geminiDir, "settings.json"), requiredServers); err != nil {
			return err
		}
		return checkGeminiTrustedFoldersPolicy(geminiTrustedFoldersPath(home, getenv), plan.folders)
	case "cursor":
		return checkPermissionsFilePolicy("cursor", "Cursor", filepath.Join(home, ".cursor", "cli-config.json"), requiredServers)
	default:
		return nil
	}
}

// checkCodexWorkerPolicy inspects Codex config.toml without modifying it.
func checkCodexWorkerPolicy(path string, requiredServers, folders []string) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var decoded map[string]any
	if err := toml.Unmarshal(content, &decoded); err != nil {
		return fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	if servers, ok := decoded["mcp_servers"].(map[string]any); ok {
		for _, name := range requiredServers {
			entry, ok := servers[name].(map[string]any)
			if !ok {
				continue
			}
			if enabled, hasEnabled := entry["enabled"].(bool); hasEnabled && !enabled {
				return &project.WorkerPolicyError{
					Message: fmt.Sprintf(
						"Codex disables MCP server %q in %s ([mcp_servers.%s] enabled = false); set enabled = true on [mcp_servers.%s] in %s and start build workers again.",
						name, path, name, name, path,
					),
				}
			}
			if mode, hasMode := entry["default_tools_approval_mode"]; hasMode && mode != "approve" {
				return &project.WorkerPolicyError{
					Message: fmt.Sprintf(
						"Codex requires interactive approval for MCP server %q in %s ([mcp_servers.%s] default_tools_approval_mode = %q); set default_tools_approval_mode = \"approve\" on [mcp_servers.%s] in %s and start build workers again.",
						name, path, name, fmt.Sprint(mode), name, path,
					),
				}
			}
		}
	}
	if projects, ok := decoded["projects"].(map[string]any); ok {
		return checkCodexProjectsMapPolicy(path, projects, folders)
	}
	return nil
}

// checkCodexProjectsMapPolicy verifies that neither folders nor an unoverridden
// ancestor project in projects sets trust_level to a value other than "trusted".
func checkCodexProjectsMapPolicy(path string, projects map[string]any, folders []string) error {
	for _, folder := range folders {
		if entry, ok := projects[folder].(map[string]any); ok {
			if level, hasLevel := entry["trust_level"]; hasLevel {
				if level != "trusted" {
					return &project.WorkerPolicyError{
						Message: fmt.Sprintf(
							"Codex marks %s as %q in %s ([projects.%s]); set trust_level = \"trusted\" for that folder in %s and start build workers again.",
							folder, fmt.Sprint(level), path, strconv.Quote(folder), path,
						),
					}
				}
				continue
			}
		}
		for key, raw := range projects {
			if !filepath.IsAbs(key) || !isStrictAncestorPath(key, folder) {
				continue
			}
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if level, hasLevel := entry["trust_level"]; hasLevel && level != "trusted" {
				return &project.WorkerPolicyError{
					Message: fmt.Sprintf(
						"Codex marks %s as %q in %s ([projects.%s]); set trust_level = \"trusted\" for that folder in %s and start build workers again.",
						key, fmt.Sprint(level), path, strconv.Quote(key), path,
					),
				}
			}
		}
	}
	return nil
}

// checkPermissionsFilePolicy checks permissions.deny and permissions.ask in
// Claude settings.json or Cursor cli-config.json without writing the file.
func checkPermissionsFilePolicy(client, displayName, path string, requiredServers []string) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	perms, ok := root["permissions"].(map[string]any)
	if !ok || perms == nil {
		return nil
	}
	for _, listName := range []string{"deny", "ask"} {
		items, ok := perms[listName].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			rule, ok := item.(string)
			if !ok {
				continue
			}
			for _, server := range requiredServers {
				if permissionRuleBlocksServer(client, rule, server) {
					return &project.WorkerPolicyError{
						Message: fmt.Sprintf(
							"%s blocks %s MCP tools via permissions.%s (%q) in %s; remove %q from permissions.%s in %s and start build workers again.",
							displayName, server, listName, rule, path, rule, listName, path,
						),
					}
				}
			}
		}
	}
	return nil
}

// permissionRuleBlocksServer reports whether a permission deny/ask rule blocks
// tools of MCP server for client ("claude" or "cursor").
func permissionRuleBlocksServer(client, rule, server string) bool {
	trimmed := strings.TrimSpace(rule)
	if trimmed == "*" {
		return true
	}
	switch client {
	case "claude":
		return trimmed == "mcp__*" ||
			trimmed == "mcp__"+server ||
			trimmed == "mcp__"+server+"__*" ||
			strings.HasPrefix(trimmed, "mcp__"+server+"__")
	case "cursor":
		return trimmed == "Mcp(*)" ||
			trimmed == "Mcp(*:*)" ||
			trimmed == "Mcp("+server+")" ||
			trimmed == "Mcp("+server+":*)" ||
			strings.HasPrefix(trimmed, "Mcp("+server+":")
	default:
		return false
	}
}

// permissionListsBlockCandidateRule reports whether permissions.deny or
// permissions.ask blocks adding candidateRule to permissions.allow during setup.
func permissionListsBlockCandidateRule(perms map[string]any, candidateRule string) bool {
	client, server := "", ""
	switch {
	case strings.HasPrefix(candidateRule, "mcp__") && strings.HasSuffix(candidateRule, "__*"):
		client = "claude"
		server = strings.TrimSuffix(strings.TrimPrefix(candidateRule, "mcp__"), "__*")
	case strings.HasPrefix(candidateRule, "Mcp(") && strings.HasSuffix(candidateRule, ":*)"):
		client = "cursor"
		server = strings.TrimSuffix(strings.TrimPrefix(candidateRule, "Mcp("), ":*)")
	}
	for _, listName := range []string{"deny", "ask"} {
		items, ok := perms[listName].([]any)
		if !ok {
			continue
		}
		for _, item := range items {
			rule, ok := item.(string)
			if !ok {
				continue
			}
			if strings.TrimSpace(rule) == candidateRule {
				return true
			}
			if client != "" && server != "" && permissionRuleBlocksServer(client, rule, server) {
				return true
			}
		}
	}
	return false
}

// checkGeminiSettingsPolicy checks whether Gemini settings.json explicitly marks
// a required MCP server as untrusted (`"trust": false`).
func checkGeminiSettingsPolicy(path string, requiredServers []string) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		return nil
	}
	for _, name := range requiredServers {
		entry, ok := servers[name].(map[string]any)
		if !ok || entry == nil {
			continue
		}
		if trust, hasTrust := entry["trust"].(bool); hasTrust && !trust {
			return &project.WorkerPolicyError{
				Message: fmt.Sprintf(
					"Gemini CLI marks MCP server %q as untrusted in %s (mcpServers.%s.trust is false); set mcpServers.%s.trust to true in %s and start build workers again.",
					name, path, name, name, path,
				),
			}
		}
	}
	return nil
}

// checkGeminiTrustedFoldersPolicy checks whether Gemini trustedFolders.json
// explicitly marks any target folder or its ancestor as "DO_NOT_TRUST".
func checkGeminiTrustedFoldersPolicy(path string, folders []string) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	return checkGeminiTrustedMapPolicy(path, root, folders)
}

// checkGeminiTrustedMapPolicy checks decoded trustedFolders.json entries against folders.
func checkGeminiTrustedMapPolicy(path string, root map[string]any, folders []string) error {
	for _, folder := range folders {
		for key, raw := range root {
			state, ok := raw.(string)
			if !ok || state != "DO_NOT_TRUST" || !filepath.IsAbs(key) {
				continue
			}
			if isPathOrAncestor(key, folder) {
				return &project.WorkerPolicyError{
					Message: fmt.Sprintf(
						"Gemini CLI marks %s as DO_NOT_TRUST in %s; set %q to \"TRUST_FOLDER\" in %s and start build workers again.",
						key, path, key, path,
					),
				}
			}
		}
	}
	return nil
}

// mergeCodexFolderTrust adds missing [projects.<folder>] trust_level = "trusted"
// entries in path while leaving existing trust_level values, comments, and all
// [mcp_servers.*] tables untouched.
func mergeCodexFolderTrust(lockDir, path string, protectedRoots, folders []string) error {
	_, err := updateLockedConfigFile(lockDir, path, protectedRoots, 0o600, func(content []byte, _ bool) ([]byte, bool, error) {
		var decoded map[string]any
		if err := toml.Unmarshal(content, &decoded); err != nil {
			return nil, false, fmt.Errorf("%s is not valid TOML: %w", path, err)
		}
		projects := map[string]any{}
		if existing, present := decoded["projects"]; present {
			var ok bool
			if projects, ok = existing.(map[string]any); !ok {
				return nil, false, fmt.Errorf("%s: projects is not a table", path)
			}
		}
		if err := checkCodexProjectsMapPolicy(path, projects, folders); err != nil {
			return nil, false, err
		}

		projLocs, projectsInline, err := locateCodexProjectEntries(content)
		if err != nil {
			return nil, false, fmt.Errorf("%s: %w", path, err)
		}

		type replacement struct {
			at, length int
			text       string
		}
		var (
			edits    []replacement
			appended []string
		)
		for _, folder := range folders {
			if existing, present := projects[folder]; present {
				entry, ok := existing.(map[string]any)
				if !ok {
					return nil, false, fmt.Errorf("%s: projects.%q is not a table", path, folder)
				}
				if _, hasLevel := entry["trust_level"]; hasLevel {
					continue
				}
			}
			loc := projLocs[folder]
			if projectsInline || loc.uneditable {
				return nil, false, fmt.Errorf("%s: projects or projects.%q cannot be edited in place", path, folder)
			}
			if loc.afterHeader > 0 {
				edits = append(edits, replacement{loc.afterHeader, 0, "trust_level = \"trusted\"\n"})
			} else {
				appended = append(appended, "[projects."+strconv.Quote(folder)+"]\ntrust_level = \"trusted\"\n")
			}
		}
		if len(edits) == 0 && len(appended) == 0 {
			return nil, false, nil
		}

		slices.SortFunc(edits, func(a, b replacement) int { return b.at - a.at })
		out := slices.Clone(content)
		for _, r := range edits {
			text := r.text
			if r.length == 0 && r.at > 0 && out[r.at-1] != '\n' {
				text = "\n" + text
			}
			out = slices.Concat(out[:r.at], []byte(text), out[r.at+r.length:])
		}
		if len(appended) > 0 {
			out = bytes.TrimRight(out, " \t\r\n")
			if len(out) > 0 {
				out = append(out, "\n\n"...)
			}
			out = append(out, strings.Join(appended, "\n")...)
		}

		var verified map[string]any
		if err := toml.Unmarshal(out, &verified); err != nil {
			return nil, false, fmt.Errorf("%s: validate updated TOML: %w", path, err)
		}
		return out, true, nil
	})
	return err
}

// locateCodexProjectEntries locates [projects.<folder>] tables in doc.
func locateCodexProjectEntries(doc []byte) (map[string]codexProjectLocation, bool, error) {
	var (
		projects       = make(map[string]codexProjectLocation)
		projectsInline bool
		table          []string
		parser         unstable.Parser
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
			if len(table) == 2 && table[0] == "projects" {
				loc := projects[table[1]]
				if expr.Kind == unstable.ArrayTable {
					loc.uneditable = true
				} else {
					loc.afterHeader = afterLine(int(last.Raw.Offset + last.Raw.Length))
				}
				projects[table[1]] = loc
			}
		case unstable.KeyValue:
			key, last := tomlKeyPath(slices.Clone(table), expr.Key())
			value := expr.Value()
			if len(key) == 1 && key[0] == "projects" {
				projectsInline = true
				continue
			}
			if len(key) == 2 && key[0] == "projects" {
				loc := projects[key[1]]
				loc.uneditable = true
				projects[key[1]] = loc
				continue
			}
			if len(key) == 3 && key[0] == "projects" && key[2] == "trust_level" {
				loc := projects[key[1]]
				if value.Kind != unstable.String {
					loc.uneditable = true
				} else {
					loc.trustLevel = tomlValueRange(doc, expr, last)
				}
				projects[key[1]] = loc
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, false, fmt.Errorf("not valid TOML: %w", err)
	}
	return projects, projectsInline, nil
}

// mergeClaudeProjectTrust ensures projects[folder].hasTrustDialogAccepted is
// true in Claude Code's .claude.json, preserving exact numbers and other keys.
func mergeClaudeProjectTrust(lockDir, path string, protectedRoots, folders []string) error {
	_, err := updateLockedConfigFile(lockDir, path, protectedRoots, 0o600, func(raw []byte, _ bool) ([]byte, bool, error) {
		root, err := decodeConfigJSONBytes(path, raw)
		if err != nil {
			return nil, false, err
		}
		projects := make(map[string]any)
		if existing, present := root["projects"]; present {
			var ok bool
			projects, ok = existing.(map[string]any)
			if !ok || projects == nil {
				return nil, false, fmt.Errorf("%s: projects must be a JSON object", path)
			}
		}
		changed := false
		for _, folder := range folders {
			entry := make(map[string]any)
			if existing, present := projects[folder]; present {
				var ok bool
				entry, ok = existing.(map[string]any)
				if !ok || entry == nil {
					return nil, false, fmt.Errorf("%s: projects[%q] must be a JSON object", path, folder)
				}
			}
			if entry["hasTrustDialogAccepted"] == true {
				continue
			}
			entry["hasTrustDialogAccepted"] = true
			projects[folder] = entry
			changed = true
		}
		if !changed {
			return nil, false, nil
		}
		root["projects"] = projects
		encoded, err := encodeConfigJSONBytes(path, root)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	})
	return err
}

// mergeGeminiTrustedFolders ensures path maps each folder to "TRUST_FOLDER"
// when not already covered by "TRUST_FOLDER" or an ancestor "TRUST_PARENT",
// refusing to overwrite any "DO_NOT_TRUST" entry.
func mergeGeminiTrustedFolders(lockDir, path string, protectedRoots, folders []string) error {
	_, err := updateLockedConfigFile(lockDir, path, protectedRoots, 0o600, func(raw []byte, _ bool) ([]byte, bool, error) {
		root, err := decodeConfigJSONBytes(path, raw)
		if err != nil {
			return nil, false, err
		}
		if err := checkGeminiTrustedMapPolicy(path, root, folders); err != nil {
			return nil, false, err
		}
		changed := false
		for _, folder := range folders {
			if geminiFolderAlreadyTrusted(root, folder) {
				continue
			}
			root[folder] = "TRUST_FOLDER"
			changed = true
		}
		if !changed {
			return nil, false, nil
		}
		encoded, err := encodeConfigJSONBytes(path, root)
		if err != nil {
			return nil, false, err
		}
		return encoded, true, nil
	})
	return err
}

// geminiFolderAlreadyTrusted reports whether folder is already trusted by an
// exact "TRUST_FOLDER"/"TRUST_PARENT" entry or a strict ancestor's "TRUST_PARENT".
func geminiFolderAlreadyTrusted(root map[string]any, folder string) bool {
	if existing, _ := root[folder].(string); existing == "TRUST_FOLDER" || existing == "TRUST_PARENT" {
		return true
	}
	for key, raw := range root {
		state, ok := raw.(string)
		if !ok || !filepath.IsAbs(key) {
			continue
		}
		if state == "TRUST_PARENT" && isStrictAncestorPath(key, folder) {
			return true
		}
	}
	return false
}

// mergeCursorTrustedWorkspaces creates ~/.cursor/projects/<slug>/.workspace-trusted
// with O_CREAT|O_EXCL for each folder when absent, leaving existing markers untouched.
func mergeCursorTrustedWorkspaces(home string, protectedRoots, folders []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, folder := range folders {
		slug := cursorWorkspaceSlug(folder)
		if slug == "" {
			continue
		}
		marker := filepath.Join(home, ".cursor", "projects", slug, ".workspace-trusted")
		resolvedMarker, err := validateConfigDestination(marker, protectedRoots)
		if err != nil {
			return err
		}
		if _, err := os.Stat(resolvedMarker); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", resolvedMarker, err)
		}
		encoded, err := encodeConfigJSONBytes(resolvedMarker, map[string]any{
			"trustedAt":     now,
			"workspacePath": folder,
		})
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(resolvedMarker), 0o755); err != nil {
			return fmt.Errorf("create folder for %s: %w", resolvedMarker, err)
		}
		file, err := os.OpenFile(resolvedMarker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("create %s: %w", resolvedMarker, err)
		}
		_, writeErr := file.Write(encoded)
		if writeErr == nil {
			writeErr = file.Sync()
		}
		if closeErr := errors.Join(writeErr, file.Close()); closeErr != nil {
			return fmt.Errorf("write %s: %w", resolvedMarker, closeErr)
		}
	}
	return nil
}

// cursorWorkspaceSlug converts an absolute path into Cursor's per-workspace
// folder name by stripping leading slashes and replacing '/', '\', and '.' with '-'.
func cursorWorkspaceSlug(folder string) string {
	trimmed := strings.TrimLeft(filepath.Clean(folder), `/\`)
	return strings.NewReplacer("/", "-", `\`, "-", ".", "-").Replace(trimmed)
}

// envLookup returns the last entry matching name=value in env, or "".
func envLookup(env []string, name string) string {
	prefix := name + "="
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], prefix); ok {
			return value
		}
	}
	return ""
}
