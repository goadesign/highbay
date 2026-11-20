// This file checks explicit client permission policies against each role's
// required MCP operations and pre-populates missing user-scoped folder trust
// outside git working copies before project roles launch.
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
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	genmcp "github.com/goadesign/surla/gen/mcp_surla"
	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
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

	// clientTrustPlan groups the folders and required MCP operations for one coding client.
	clientTrustPlan struct {
		// folders lists unique absolute folders needing folder trust when Yolo is false.
		folders []string
		// checkPolicy is true when at least one role for this client runs with Yolo false.
		checkPolicy bool
		// requiredServers lists MCP servers ("surla", and "chorus" when needed) in deterministic order.
		requiredServers []string
		// requiredTools maps each server name to the exact MCP tool names the non-Yolo roles need.
		requiredTools map[string][]string
	}
)

// workerTrustLockTimeout bounds waiting on a contended client configuration
// lock so a stalled writer cannot hold the team start mutex for its full budget.
const workerTrustLockTimeout = 5 * time.Second

var (
	// chorusMCPToolNames lists the Chorus MCP tools workers use in Herdr mode.
	chorusMCPToolNames = []string{"join", "read", "send", "who", "leave"}

	// allSurlaMCPToolNames caches the generated Surla MCP tool catalog once.
	allSurlaMCPToolNames = sync.OnceValue(func() []string {
		res, err := genmcp.NewMCPAdapter(&gensurla.Endpoints{}, nil).ToolsList(
			context.Background(), &genmcp.ToolsListPayload{},
		)
		if err != nil || res == nil {
			return nil
		}
		names := make([]string, 0, len(res.Tools))
		for _, tool := range res.Tools {
			if tool != nil && tool.Name != "" {
				names = append(names, tool.Name)
			}
		}
		return names
	})
)

// prepareWorkerTrust checks read-only client permission policies and writes
// only missing user-scoped folder-trust entries resolved from env (falling back
// to os.UserHomeDir when HOME is absent in env). It honors ctx (bounded to
// workerTrustLockTimeout) and never writes settings.json, cli-config.json, MCP
// server entries, or any path inside project or worktree folders.
func prepareWorkerTrust(ctx context.Context, env []string, targets []project.WorkerTrustTarget) error {
	if len(targets) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, workerTrustLockTimeout)
	defer cancel()

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
		if err := lockCtx.Err(); err != nil {
			return err
		}
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
				lockCtx, lockDir, filepath.Join(codexDir, "config.toml"), protectedRoots, plan.folders,
			))
		case "claude":
			claudeConfig := filepath.Join(home, ".claude.json")
			if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
				claudeConfig = filepath.Join(dir, ".claude.json")
			}
			errs = errors.Join(errs, mergeClaudeProjectTrust(lockCtx, lockDir, claudeConfig, protectedRoots, plan.folders))
		case "gemini":
			trustedPath := geminiTrustedFoldersPath(home, getenv)
			errs = errors.Join(errs, mergeGeminiTrustedFolders(lockCtx, lockDir, trustedPath, protectedRoots, plan.folders))
		case "cursor":
			errs = errors.Join(errs, mergeCursorTrustedWorkspaces(lockCtx, home, protectedRoots, plan.folders))
		}
		var policyErr *controlplane.TeamPolicyRefusal
		if errors.As(errs, &policyErr) {
			return policyErr
		}
	}
	return errs
}

// bindPrepareWorkerTrust adapts prepareWorkerTrust to project.TeamDependencies.
func bindPrepareWorkerTrust(env []string) func(context.Context, []project.WorkerTrustTarget) error {
	return func(ctx context.Context, targets []project.WorkerTrustTarget) error {
		return prepareWorkerTrust(ctx, env, targets)
	}
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

// buildClientTrustPlans aggregates non-Yolo folders and role-specific MCP tools per client.
func buildClientTrustPlans(
	targets []project.WorkerTrustTarget,
) (map[string]clientTrustPlan, []string) {
	plans := make(map[string]clientTrustPlan)
	var order []string
	addTools := func(plan *clientTrustPlan, server string, tools []string) {
		if !slices.Contains(plan.requiredServers, server) {
			plan.requiredServers = append(plan.requiredServers, server)
		}
		if plan.requiredTools == nil {
			plan.requiredTools = make(map[string][]string)
		}
		existing := plan.requiredTools[server]
		for _, tool := range tools {
			if !slices.Contains(existing, tool) {
				existing = append(existing, tool)
			}
		}
		plan.requiredTools[server] = existing
	}

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
			role := resolveTargetRole(target)
			addTools(&plan, surlaServerName, requiredSurlaTools(role))
			if target.RequireChorus {
				addTools(&plan, "chorus", chorusMCPToolNames)
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

// resolveTargetRole returns target.Role, falling back to its worktree folder name when omitted.
func resolveTargetRole(target project.WorkerTrustTarget) string {
	if target.Role != "" {
		return target.Role
	}
	if filepath.Base(filepath.Clean(target.Folder)) == testwriter.TestsRole.Folder {
		return "test_writer"
	}
	return "builder"
}

// requiredSurlaTools returns the Surla MCP tools allowed for role.
func requiredSurlaTools(role string) []string {
	var kind controlplane.CallerKind
	switch role {
	case "guide":
		kind = controlplane.CallerGuide
	case "test_writer":
		kind = controlplane.CallerTestWriter
	default:
		kind = controlplane.CallerBuilder
	}
	all := allSurlaMCPToolNames()
	allowed := make([]string, 0, len(all))
	for _, name := range all {
		if controlplane.OperationAllowed(kind, name) {
			allowed = append(allowed, name)
		}
	}
	return allowed
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
// that block non-Yolo workers from calling their role's required Surla or Chorus
// MCP tools or trusting the project folder.
func checkClientWorkerPolicy(
	client, home string,
	getenv func(string) string,
	plan clientTrustPlan,
) error {
	switch client {
	case "codex":
		codexDir := cmp.Or(getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
		return checkCodexWorkerPolicy(filepath.Join(codexDir, "config.toml"), plan.requiredServers, plan.folders)
	case "claude":
		claudeDir := cmp.Or(getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
		return checkPermissionsFilePolicy("claude", filepath.Join(claudeDir, "settings.json"), plan)
	case "gemini":
		geminiDir := geminiConfigDir(home, getenv("GEMINI_CLI_HOME"))
		if err := checkGeminiSettingsPolicy(filepath.Join(geminiDir, "settings.json"), plan); err != nil {
			return err
		}
		return checkGeminiTrustedFoldersPolicy(geminiTrustedFoldersPath(home, getenv), plan.folders)
	case "cursor":
		return checkPermissionsFilePolicy("cursor", filepath.Join(home, ".cursor", "cli-config.json"), plan)
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
				return &controlplane.TeamPolicyRefusal{
					Kind:   controlplane.PolicyCodexServerDisabled,
					Client: "codex",
					Server: name,
				}
			}
			if mode, hasMode := entry["default_tools_approval_mode"]; hasMode && mode != "approve" {
				return &controlplane.TeamPolicyRefusal{
					Kind:   controlplane.PolicyCodexServerApproval,
					Client: "codex",
					Server: name,
					Detail: fmt.Sprint(mode),
				}
			}
		}
	}
	if projects, ok := decoded["projects"].(map[string]any); ok {
		return checkCodexProjectsMapPolicy(projects, folders)
	}
	return nil
}

// checkCodexProjectsMapPolicy verifies that neither folders nor an unoverridden
// ancestor project in projects sets trust_level to a value other than "trusted".
func checkCodexProjectsMapPolicy(projects map[string]any, folders []string) error {
	for _, folder := range folders {
		if entry, ok := projects[folder].(map[string]any); ok {
			if level, hasLevel := entry["trust_level"]; hasLevel {
				if level != "trusted" {
					return &controlplane.TeamPolicyRefusal{
						Kind:   controlplane.PolicyCodexProjectUntrusted,
						Client: "codex",
						Detail: fmt.Sprint(level),
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
				return &controlplane.TeamPolicyRefusal{
					Kind:   controlplane.PolicyCodexProjectUntrusted,
					Client: "codex",
					Detail: fmt.Sprint(level),
				}
			}
		}
	}
	return nil
}

// checkPermissionsFilePolicy checks permissions.deny and permissions.ask in
// Claude settings.json or Cursor cli-config.json against plan's required tools.
func checkPermissionsFilePolicy(client, path string, plan clientTrustPlan) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	perms, ok := root["permissions"].(map[string]any)
	if !ok || perms == nil {
		return nil
	}
	kind := controlplane.PolicyClaudePermission
	if client == "cursor" {
		kind = controlplane.PolicyCursorPermission
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
			for _, server := range plan.requiredServers {
				for _, tool := range plan.requiredTools[server] {
					if permissionRuleBlocksTool(client, rule, server, tool) {
						return &controlplane.TeamPolicyRefusal{
							Kind:   kind,
							Client: client,
							Server: server,
							List:   listName,
							Detail: strings.TrimSpace(rule),
						}
					}
				}
			}
		}
	}
	return nil
}

// permissionRuleBlocksTool reports whether rule blocks calling tool on MCP
// server under client ("claude" or "cursor") using each client's wildcard syntax.
func permissionRuleBlocksTool(client, rule, server, tool string) bool {
	trimmed := strings.TrimSpace(rule)
	if trimmed == "" {
		return false
	}
	if trimmed == "*" {
		return true
	}
	switch client {
	case "claude":
		return trimmed == "mcp__"+server || globMatch(trimmed, "mcp__"+server+"__"+tool)
	case "cursor":
		if !strings.HasPrefix(trimmed, "Mcp(") || !strings.HasSuffix(trimmed, ")") {
			return false
		}
		inner := trimmed[4 : len(trimmed)-1]
		if srvPat, toolPat, hasColon := strings.Cut(inner, ":"); hasColon {
			return globMatch(strings.TrimSpace(srvPat), server) && globMatch(strings.TrimSpace(toolPat), tool)
		}
		return globMatch(strings.TrimSpace(inner), server)
	default:
		return false
	}
}

// permissionListsBlockCandidateRule reports whether permissions.deny or
// permissions.ask restricts any tool of candidateRule's server so `surla setup`
// never widens permissions over an explicit user restriction.
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
	var tools []string
	if server == surlaServerName {
		tools = append(slices.Clone(allSurlaMCPToolNames()), "__any_tool__")
	} else if server != "" {
		tools = append(slices.Clone(chorusMCPToolNames), "__any_tool__")
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
			trimmed := strings.TrimSpace(rule)
			if trimmed == candidateRule {
				return true
			}
			if client == "claude" && server != "" &&
				(strings.HasPrefix(trimmed, "mcp__"+server+"__") || trimmed == "mcp__"+server) {
				return true
			}
			if client == "cursor" && strings.HasPrefix(trimmed, "Mcp(") && strings.HasSuffix(trimmed, ")") {
				inner := trimmed[4 : len(trimmed)-1]
				srvPat, _, _ := strings.Cut(inner, ":")
				if globMatch(strings.TrimSpace(srvPat), server) {
					return true
				}
			}
			for _, tool := range tools {
				if permissionRuleBlocksTool(client, trimmed, server, tool) {
					return true
				}
			}
		}
	}
	return false
}

// globMatch reports whether pattern (where '*' matches zero or more bytes) matches value.
func globMatch(pattern, value string) bool {
	px, vx := 0, 0
	nextPx, nextVx := 0, 0
	for px < len(pattern) || vx < len(value) {
		if px < len(pattern) {
			switch pattern[px] {
			case '*':
				nextPx = px
				nextVx = vx + 1
				px++
				continue
			default:
				if vx < len(value) && pattern[px] == value[vx] {
					px++
					vx++
					continue
				}
			}
		}
		if nextVx > 0 && nextVx <= len(value) {
			px = nextPx + 1
			vx = nextVx
			nextVx++
			continue
		}
		return false
	}
	return true
}

// checkGeminiSettingsPolicy checks whether Gemini settings.json marks a required
// MCP server as untrusted (`"trust": false`) or filters out a required role tool
// via `excludeTools` or `includeTools`.
func checkGeminiSettingsPolicy(path string, plan clientTrustPlan) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		return nil
	}
	for _, name := range plan.requiredServers {
		entry, ok := servers[name].(map[string]any)
		if !ok || entry == nil {
			continue
		}
		if trust, hasTrust := entry["trust"].(bool); hasTrust && !trust {
			return &controlplane.TeamPolicyRefusal{
				Kind:   controlplane.PolicyGeminiServerUntrusted,
				Client: "gemini",
				Server: name,
			}
		}
		if excluded, ok := entry["excludeTools"].([]any); ok {
			for _, tool := range plan.requiredTools[name] {
				if geminiToolListMatches(excluded, name, tool) {
					return &controlplane.TeamPolicyRefusal{
						Kind:   controlplane.PolicyGeminiToolFiltered,
						Client: "gemini",
						Server: name,
						List:   "excludeTools",
						Detail: tool,
					}
				}
			}
		}
		if rawInclude, hasInclude := entry["includeTools"]; hasInclude {
			if included, ok := rawInclude.([]any); ok {
				for _, tool := range plan.requiredTools[name] {
					if !geminiToolListMatches(included, name, tool) {
						return &controlplane.TeamPolicyRefusal{
							Kind:   controlplane.PolicyGeminiToolFiltered,
							Client: "gemini",
							Server: name,
							List:   "includeTools",
							Detail: tool,
						}
					}
				}
			}
		}
	}
	return nil
}

// geminiToolListMatches reports whether any string pattern in patterns matches tool on server.
func geminiToolListMatches(patterns []any, server, tool string) bool {
	for _, item := range patterns {
		pat, ok := item.(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(pat)
		if trimmed == "" {
			continue
		}
		if globMatch(trimmed, tool) ||
			globMatch(trimmed, "mcp_"+server+"_"+tool) ||
			globMatch(trimmed, "mcp__"+server+"__"+tool) {
			return true
		}
	}
	return false
}

// checkGeminiTrustedFoldersPolicy checks whether Gemini trustedFolders.json
// explicitly marks any target folder or its ancestor as "DO_NOT_TRUST".
func checkGeminiTrustedFoldersPolicy(path string, folders []string) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	return checkGeminiTrustedMapPolicy(root, folders)
}

// checkGeminiTrustedMapPolicy checks decoded trustedFolders.json entries against folders.
func checkGeminiTrustedMapPolicy(root map[string]any, folders []string) error {
	for _, folder := range folders {
		for key, raw := range root {
			state, ok := raw.(string)
			if !ok || state != "DO_NOT_TRUST" || !filepath.IsAbs(key) {
				continue
			}
			if isPathOrAncestor(key, folder) {
				return &controlplane.TeamPolicyRefusal{
					Kind:   controlplane.PolicyGeminiFolderUntrusted,
					Client: "gemini",
				}
			}
		}
	}
	return nil
}

// mergeCodexFolderTrust adds missing [projects.<folder>] trust_level = "trusted"
// entries in path while leaving existing trust_level values, comments, and all
// [mcp_servers.*] tables untouched.
func mergeCodexFolderTrust(
	ctx context.Context, lockDir, path string, protectedRoots, folders []string,
) error {
	_, err := updateLockedConfigFile(ctx, lockDir, path, protectedRoots, 0o600, func(content []byte, _ bool) ([]byte, bool, error) {
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
		if err := checkCodexProjectsMapPolicy(projects, folders); err != nil {
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
func mergeClaudeProjectTrust(
	ctx context.Context, lockDir, path string, protectedRoots, folders []string,
) error {
	_, err := updateLockedConfigFile(ctx, lockDir, path, protectedRoots, 0o600, func(raw []byte, _ bool) ([]byte, bool, error) {
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
func mergeGeminiTrustedFolders(
	ctx context.Context, lockDir, path string, protectedRoots, folders []string,
) error {
	_, err := updateLockedConfigFile(ctx, lockDir, path, protectedRoots, 0o600, func(raw []byte, _ bool) ([]byte, bool, error) {
		root, err := decodeConfigJSONBytes(path, raw)
		if err != nil {
			return nil, false, err
		}
		if err := checkGeminiTrustedMapPolicy(root, folders); err != nil {
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
func mergeCursorTrustedWorkspaces(ctx context.Context, home string, protectedRoots, folders []string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, folder := range folders {
		if err := ctx.Err(); err != nil {
			return err
		}
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
