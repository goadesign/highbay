// This file pre-populates user-scoped folder trust and MCP tool approvals
// outside git working copies before project roles launch.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/goadesign/surla/internal/project"
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

	// codexServerApprovalLocation records where an existing standard
	// [mcp_servers.<name>] table can receive default_tools_approval_mode.
	codexServerApprovalLocation struct {
		// afterHeader is the byte offset after [mcp_servers.<name>], or 0 when absent.
		afterHeader int
		// approvalMode is the byte range of an existing default_tools_approval_mode string.
		approvalMode unstable.Range
	}
)

// prepareWorkerTrust idempotently records folder trust and MCP tool approval in
// user-scoped client configuration files resolved from env (falling back to
// os.UserHomeDir when HOME is absent in env). It never writes inside project or
// git worktree folders.
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

	byClient := make(map[string][]string)
	var clientOrder []string
	for _, target := range targets {
		if target.Client == "" || !filepath.IsAbs(target.Folder) {
			continue
		}
		folder := filepath.Clean(target.Folder)
		folders, seen := byClient[target.Client]
		if !seen {
			clientOrder = append(clientOrder, target.Client)
		}
		if !slices.Contains(folders, folder) {
			byClient[target.Client] = append(folders, folder)
		}
	}

	var errs error
	for _, client := range clientOrder {
		folders := byClient[client]
		switch client {
		case "codex":
			codexDir := cmp.Or(envLookup(env, "CODEX_HOME"), filepath.Join(home, ".codex"))
			errs = errors.Join(errs, mergeCodexWorkerTrust(filepath.Join(codexDir, "config.toml"), folders))
		case "claude":
			claudeDir := filepath.Join(home, ".claude")
			claudeConfig := filepath.Join(home, ".claude.json")
			if dir := envLookup(env, "CLAUDE_CONFIG_DIR"); dir != "" {
				claudeDir = dir
				claudeConfig = filepath.Join(dir, ".claude.json")
			}
			errs = errors.Join(errs, mergeClaudeProjectTrust(claudeConfig, folders))
			_, _, permErr := mergePermissionsAllowJSON(permissionsJSONTarget{
				path:              filepath.Join(claudeDir, "settings.json"),
				rules:             []string{"mcp__surla__*", "mcp__chorus__*"},
				createWhenMissing: true,
			})
			errs = errors.Join(errs, permErr)
		case "gemini":
			geminiDir := cmp.Or(envLookup(env, "GEMINI_CLI_HOME"), filepath.Join(home, ".gemini"))
			errs = errors.Join(errs, mergeGeminiTrustedFolders(filepath.Join(geminiDir, "trustedFolders.json"), folders))
		case "cursor":
			errs = errors.Join(errs, mergeCursorTrustedWorkspaces(home, folders))
			_, _, permErr := mergePermissionsAllowJSON(permissionsJSONTarget{
				path:              filepath.Join(home, ".cursor", "cli-config.json"),
				rules:             []string{"Mcp(surla:*)", "Mcp(chorus:*)"},
				createWhenMissing: false,
			})
			errs = errors.Join(errs, permErr)
		}
	}
	return errs
}

// envLookup returns the last non-key entry matching name=value in env, or "".
func envLookup(env []string, name string) string {
	prefix := name + "="
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], prefix); ok {
			return value
		}
	}
	return ""
}

// mergeCodexWorkerTrust ensures each folder in folders has
// [projects.<quoted folder>] trust_level = "trusted" in path, and ensures any
// existing standard [mcp_servers.surla] or [mcp_servers.chorus] table sets
// default_tools_approval_mode = "approve", preserving comments and unrelated bytes.
func mergeCodexWorkerTrust(path string, folders []string) error {
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	var decoded map[string]any
	if err := toml.Unmarshal(content, &decoded); err != nil {
		return fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	projects := map[string]any{}
	if existing, present := decoded["projects"]; present {
		var ok bool
		if projects, ok = existing.(map[string]any); !ok {
			return fmt.Errorf("%s: projects is not a table", path)
		}
	}
	servers := map[string]any{}
	if existing, present := decoded["mcp_servers"]; present {
		var ok bool
		if servers, ok = existing.(map[string]any); !ok {
			return fmt.Errorf("%s: mcp_servers is not a table", path)
		}
	}

	projLocs, serverLocs, projectsInline, err := locateCodexTrustEdits(content)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	type replacement struct {
		at, length int
		text       string
	}
	var (
		edits    []replacement
		appended []string
	)
	for _, name := range []string{surlaServerName, "chorus"} {
		entry, ok := servers[name].(map[string]any)
		if !ok || entry["default_tools_approval_mode"] == "approve" {
			continue
		}
		loc := serverLocs[name]
		if loc.approvalMode.Length > 0 {
			edits = append(edits, replacement{int(loc.approvalMode.Offset), int(loc.approvalMode.Length), `"approve"`})
		} else if loc.afterHeader > 0 {
			edits = append(edits, replacement{loc.afterHeader, 0, "default_tools_approval_mode = \"approve\"\n"})
		}
	}

	for _, folder := range folders {
		if existing, present := projects[folder]; present {
			entry, ok := existing.(map[string]any)
			if !ok {
				return fmt.Errorf("%s: projects.%q is not a table", path, folder)
			}
			if entry["trust_level"] == "trusted" {
				continue
			}
		}
		loc := projLocs[folder]
		if projectsInline || loc.uneditable {
			return fmt.Errorf("%s: projects or projects.%q cannot be edited in place", path, folder)
		}
		switch {
		case loc.trustLevel.Length > 0:
			edits = append(edits, replacement{int(loc.trustLevel.Offset), int(loc.trustLevel.Length), `"trusted"`})
		case loc.afterHeader > 0:
			edits = append(edits, replacement{loc.afterHeader, 0, "trust_level = \"trusted\"\n"})
		default:
			appended = append(appended, "[projects."+strconv.Quote(folder)+"]\ntrust_level = \"trusted\"\n")
		}
	}

	if len(edits) == 0 && len(appended) == 0 {
		return nil
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
		return fmt.Errorf("%s: validate updated TOML: %w", path, err)
	}
	return replaceConfigFile(path, out, 0o600)
}

// locateCodexTrustEdits locates [projects.<folder>] entries and standard
// [mcp_servers.surla]/[mcp_servers.chorus] tables in doc.
func locateCodexTrustEdits(doc []byte) (
	map[string]codexProjectLocation,
	map[string]codexServerApprovalLocation,
	bool,
	error,
) {
	var (
		projects       = make(map[string]codexProjectLocation)
		servers        = make(map[string]codexServerApprovalLocation)
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
			if len(table) == 2 && table[0] == "mcp_servers" && expr.Kind == unstable.Table &&
				(table[1] == surlaServerName || table[1] == "chorus") {
				loc := servers[table[1]]
				loc.afterHeader = afterLine(int(last.Raw.Offset + last.Raw.Length))
				servers[table[1]] = loc
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
				continue
			}
			if len(key) == 3 && key[0] == "mcp_servers" && key[2] == "default_tools_approval_mode" &&
				(key[1] == surlaServerName || key[1] == "chorus") && value.Kind == unstable.String {
				loc := servers[key[1]]
				loc.approvalMode = tomlValueRange(doc, expr, last)
				servers[key[1]] = loc
			}
		}
	}
	if err := parser.Error(); err != nil {
		return nil, nil, false, fmt.Errorf("not valid TOML: %w", err)
	}
	return projects, servers, projectsInline, nil
}

// mergeClaudeProjectTrust ensures projects[folder].hasTrustDialogAccepted is
// true in Claude Code's .claude.json, preserving exact numbers and other keys.
func mergeClaudeProjectTrust(path string, folders []string) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	projects := make(map[string]any)
	if existing, present := root["projects"]; present {
		var ok bool
		projects, ok = existing.(map[string]any)
		if !ok || projects == nil {
			return fmt.Errorf("%s: projects must be a JSON object", path)
		}
	}
	changed := false
	for _, folder := range folders {
		entry := make(map[string]any)
		if existing, present := projects[folder]; present {
			var ok bool
			entry, ok = existing.(map[string]any)
			if !ok || entry == nil {
				return fmt.Errorf("%s: projects[%q] must be a JSON object", path, folder)
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
		return nil
	}
	root["projects"] = projects
	return writeConfigJSONObject(path, root, 0o600)
}

// mergeGeminiTrustedFolders ensures path maps each folder to "TRUST_FOLDER"
// unless already set to "TRUST_FOLDER" or "TRUST_PARENT".
func mergeGeminiTrustedFolders(path string, folders []string) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	changed := false
	for _, folder := range folders {
		existing, _ := root[folder].(string)
		if existing == "TRUST_FOLDER" || existing == "TRUST_PARENT" {
			continue
		}
		root[folder] = "TRUST_FOLDER"
		changed = true
	}
	if !changed {
		return nil
	}
	return writeConfigJSONObject(path, root, 0o600)
}

// mergeCursorTrustedWorkspaces creates ~/.cursor/projects/<slug>/.workspace-trusted
// for each folder when absent, leaving existing markers untouched.
func mergeCursorTrustedWorkspaces(home string, folders []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, folder := range folders {
		slug := cursorWorkspaceSlug(folder)
		if slug == "" {
			continue
		}
		marker := filepath.Join(home, ".cursor", "projects", slug, ".workspace-trusted")
		if _, err := os.Stat(marker); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect %s: %w", marker, err)
		}
		root := map[string]any{
			"trustedAt":     now,
			"workspacePath": folder,
		}
		if err := writeConfigJSONObject(marker, root, 0o600); err != nil {
			return err
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

// readConfigJSONObject decodes a single JSON object from path with UseNumber,
// or returns an empty map when path is absent or blank.
func readConfigJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	root := make(map[string]any)
	if len(bytes.TrimSpace(raw)) == 0 {
		return root, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("parse JSON in %s: %w", path, err)
	}
	if root == nil {
		return nil, fmt.Errorf("%s must contain a JSON object, not null", path)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%s must contain one JSON object", path)
	}
	return root, nil
}

// writeConfigJSONObject atomically writes root to path with 2-space indentation
// and unescaped HTML.
func writeConfigJSONObject(path string, root map[string]any, perm os.FileMode) error {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(root); err != nil {
		return fmt.Errorf("encode JSON for %s: %w", path, err)
	}
	return replaceConfigFile(path, encoded.Bytes(), perm)
}

// bindPrepareWorkerTrust adapts prepareWorkerTrust to project.TeamDependencies.
func bindPrepareWorkerTrust(env []string) func(context.Context, []project.WorkerTrustTarget) error {
	return func(_ context.Context, targets []project.WorkerTrustTarget) error {
		return prepareWorkerTrust(env, targets)
	}
}
