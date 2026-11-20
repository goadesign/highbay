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
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"github.com/goadesign/surla/internal/abspath"
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

	// preflightAccumulator collects independent client preflight outcomes so an
	// explicit user policy refusal always takes precedence over a generic config
	// parse or destination failure on an earlier client.
	preflightAccumulator struct {
		// explicit holds the first explicit policy refusal across all checked clients.
		explicit *controlplane.TeamPolicyRefusal
		// unusable holds the first malformed, unreadable, or worktree-aliased config failure.
		unusable *controlplane.TeamPolicyRefusal
	}
)

// workerTrustLockTimeout bounds waiting on a contended client configuration
// lock so a stalled writer cannot hold the team start mutex for its full budget.
const workerTrustLockTimeout = 5 * time.Second

// chorusMCPToolNames lists the Chorus MCP tools required for Herdr coordination.
var chorusMCPToolNames = []string{"join", "read", "send", "who"}

// prepareWorkerTrust checks read-only client permission policies across all
// selected clients and writes only missing user-scoped folder-trust entries
// resolved from env (falling back to os.UserHomeDir when HOME is absent). It
// honors ctx (bounded to workerTrustLockTimeout) and never writes settings.json,
// cli-config.json, MCP server entries, or paths inside project or worktree folders.
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

	// Complete every independent client policy and destination check before any write.
	var acc preflightAccumulator
	for _, client := range clientOrder {
		if err := lockCtx.Err(); err != nil {
			return err
		}
		plan := plans[client]
		if !plan.checkPolicy && len(plan.folders) == 0 {
			continue
		}
		preflightClientWorkerTrust(client, home, getenv, protectedRoots, plan, &acc)
	}
	if acc.explicit != nil {
		return acc.explicit
	}
	if acc.unusable != nil {
		return acc.unusable
	}

	for _, client := range clientOrder {
		plan := plans[client]
		if len(plan.folders) == 0 {
			continue
		}
		var (
			writeErr   error
			configBase string
		)
		switch client {
		case "codex":
			codexDir := cmp.Or(getenv("CODEX_HOME"), filepath.Join(home, ".codex"))
			configBase = "config.toml"
			writeErr = mergeCodexFolderTrust(
				lockCtx, lockDir, filepath.Join(codexDir, "config.toml"), protectedRoots, plan.folders,
			)
		case "claude":
			claudeConfig := filepath.Join(home, ".claude.json")
			if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
				claudeConfig = filepath.Join(dir, ".claude.json")
			}
			configBase = ".claude.json"
			writeErr = mergeClaudeProjectTrust(lockCtx, lockDir, claudeConfig, protectedRoots, plan.folders)
		case "gemini":
			trustedPath := geminiTrustedFoldersPath(home, getenv)
			configBase = filepath.Base(trustedPath)
			writeErr = mergeGeminiTrustedFolders(lockCtx, lockDir, trustedPath, protectedRoots, plan.folders)
		case "cursor":
			configBase = ".workspace-trusted"
			writeErr = mergeCursorTrustedWorkspaces(lockCtx, home, protectedRoots, plan.folders)
		}
		if writeErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			var policyErr *controlplane.TeamPolicyRefusal
			if errors.As(writeErr, &policyErr) {
				return policyErr
			}
			return &controlplane.TeamPolicyRefusal{
				Kind:   controlplane.PolicyConfigUnusable,
				Client: client,
				Detail: configBase,
			}
		}
	}
	return nil
}

// record classifies err into explicit policy refusal vs unusable config failure.
func (a *preflightAccumulator) record(client, configFile string, err error) {
	if err == nil {
		return
	}
	var policyErr *controlplane.TeamPolicyRefusal
	if errors.As(err, &policyErr) {
		if policyErr.Kind == controlplane.PolicyConfigUnusable {
			if a.unusable == nil {
				a.unusable = policyErr
			}
			return
		}
		if a.explicit == nil {
			a.explicit = policyErr
		}
		return
	}
	if a.unusable == nil {
		a.unusable = &controlplane.TeamPolicyRefusal{
			Kind:   controlplane.PolicyConfigUnusable,
			Client: client,
			Detail: configFile,
		}
	}
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

// requiredSurlaTools returns the minimal Surla MCP operations that role
// necessarily invokes to execute its assigned startup and workflow. Optional
// operations such as start_live_target do not block ordinary worker admission.
func requiredSurlaTools(role string) []string {
	switch role {
	case "guide":
		return []string{
			"open_views",
			"list_changes",
			"get_change",
			"update_draft",
			"refresh_story",
			"get_story_refresh",
			"propose_change",
			"revise_change",
		}
	case "test_writer":
		return []string{
			"list_test_writer_assignments",
			"get_test_writer_assignment",
			"report_tests_finished",
		}
	default:
		return []string{
			"list_builder_assignments",
			"get_builder_assignment",
			"generate_code",
			"check_project",
			"verify_change",
		}
	}
}

// preflightClientWorkerTrust validates destinations and checks read-only client
// policies and folder-trust target syntax for client without modifying any file.
func preflightClientWorkerTrust(
	client, home string,
	getenv func(string) string,
	protectedRoots []string,
	plan clientTrustPlan,
	acc *preflightAccumulator,
) {
	checkPath := func(path, label string, fn func(resolvedPath string) error) {
		resolved, err := validateConfigDestination(path, protectedRoots)
		if err != nil {
			acc.record(client, label, err)
			return
		}
		if fn != nil {
			acc.record(client, label, fn(resolved))
		}
	}

	switch client {
	case "codex":
		codexPath := filepath.Join(cmp.Or(getenv("CODEX_HOME"), filepath.Join(home, ".codex")), "config.toml")
		checkPath(codexPath, "config.toml", func(p string) error {
			if !plan.checkPolicy {
				return nil
			}
			return checkCodexWorkerPolicy(p, plan.requiredServers, plan.folders)
		})
	case "claude":
		claudeDir := filepath.Join(home, ".claude")
		claudeConfig := filepath.Join(home, ".claude.json")
		if dir := getenv("CLAUDE_CONFIG_DIR"); dir != "" {
			claudeDir = dir
			claudeConfig = filepath.Join(dir, ".claude.json")
		}
		checkPath(filepath.Join(claudeDir, "settings.json"), "settings.json", func(p string) error {
			if !plan.checkPolicy {
				return nil
			}
			return checkPermissionsFilePolicy("claude", p, plan)
		})
		checkPath(claudeConfig, ".claude.json", func(p string) error {
			if len(plan.folders) == 0 {
				return nil
			}
			return checkClaudeProjectTrustReadable(p, plan.folders)
		})
	case "gemini":
		geminiDir := geminiConfigDir(home, getenv("GEMINI_CLI_HOME"))
		checkPath(filepath.Join(geminiDir, "settings.json"), "settings.json", func(p string) error {
			if !plan.checkPolicy {
				return nil
			}
			return checkGeminiSettingsPolicy(p, plan)
		})
		trustedPath := geminiTrustedFoldersPath(home, getenv)
		checkPath(trustedPath, filepath.Base(trustedPath), func(p string) error {
			if !plan.checkPolicy {
				return nil
			}
			return checkGeminiTrustedFoldersPolicy(p, plan.folders)
		})
	case "cursor":
		checkPath(filepath.Join(home, ".cursor", "cli-config.json"), "cli-config.json", func(p string) error {
			if !plan.checkPolicy {
				return nil
			}
			return checkPermissionsFilePolicy("cursor", p, plan)
		})
		resolvedHome, homeErr := abspath.EvalMissing(home)
		if homeErr != nil {
			acc.record(client, ".workspace-trusted", homeErr)
			return
		}
		for _, folder := range plan.folders {
			if slug := cursorWorkspaceSlug(folder); slug != "" {
				marker := filepath.Join(home, ".cursor", "projects", slug, ".workspace-trusted")
				checkPath(marker, ".workspace-trusted", func(p string) error {
					return checkCursorWorkspaceMarker(resolvedHome, p)
				})
			}
		}
	}
}

// checkCodexWorkerPolicy inspects Codex config.toml without modifying it.
func checkCodexWorkerPolicy(path string, requiredServers, folders []string) error {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return nil
	}
	var decoded map[string]any
	if err := toml.Unmarshal(content, &decoded); err != nil {
		return fmt.Errorf("%s is not valid TOML: %w", path, err)
	}
	var shapeErr error
	recordShape := func(err error) {
		if err != nil && shapeErr == nil {
			shapeErr = err
		}
	}
	if rawServers, hasServers := decoded["mcp_servers"]; hasServers {
		servers, ok := rawServers.(map[string]any)
		if !ok || servers == nil {
			recordShape(fmt.Errorf("%s: mcp_servers is not a table", path))
		} else {
			for _, name := range requiredServers {
				rawEntry, present := servers[name]
				if !present {
					continue
				}
				entry, ok := rawEntry.(map[string]any)
				if !ok || entry == nil {
					recordShape(fmt.Errorf("%s: mcp_servers.%s is not a table", path, name))
					continue
				}
				if rawEnabled, hasEnabled := entry["enabled"]; hasEnabled {
					enabled, ok := rawEnabled.(bool)
					if !ok {
						recordShape(fmt.Errorf("%s: mcp_servers.%s.enabled must be a boolean", path, name))
					} else if !enabled {
						return &controlplane.TeamPolicyRefusal{
							Kind:   controlplane.PolicyCodexServerDisabled,
							Client: "codex",
							Server: name,
						}
					}
				}
				if rawMode, hasMode := entry["default_tools_approval_mode"]; hasMode {
					mode, ok := rawMode.(string)
					if !ok {
						recordShape(fmt.Errorf("%s: mcp_servers.%s.default_tools_approval_mode must be a string", path, name))
					} else if mode != "approve" {
						return &controlplane.TeamPolicyRefusal{
							Kind:   controlplane.PolicyCodexServerApproval,
							Client: "codex",
							Server: name,
							Detail: mode,
						}
					}
				}
			}
		}
	}
	projects := map[string]any{}
	if existing, present := decoded["projects"]; present {
		var ok bool
		if projects, ok = existing.(map[string]any); !ok || projects == nil {
			recordShape(fmt.Errorf("%s: projects is not a table", path))
			projects = map[string]any{}
		} else if err := checkCodexProjectsMapPolicy(path, projects, folders); err != nil {
			var policyErr *controlplane.TeamPolicyRefusal
			if errors.As(err, &policyErr) {
				return policyErr
			}
			recordShape(err)
		}
	}
	if len(folders) > 0 {
		projLocs, projectsInline, err := locateCodexProjectEntries(content)
		if err != nil {
			recordShape(fmt.Errorf("%s: %w", path, err))
		} else {
			for _, folder := range folders {
				if existing, present := projects[folder]; present {
					entry, ok := existing.(map[string]any)
					if !ok || entry == nil {
						recordShape(fmt.Errorf("%s: projects.%q is not a table", path, folder))
						continue
					}
					if rawLevel, hasLevel := entry["trust_level"]; hasLevel {
						if _, ok := rawLevel.(string); ok {
							continue
						}
					}
				}
				if projectsInline || projLocs[folder].uneditable {
					recordShape(fmt.Errorf("%s: projects or projects.%q cannot be edited in place", path, folder))
				}
			}
		}
	}
	return shapeErr
}

// checkCodexProjectsMapPolicy verifies that neither folders nor an unoverridden
// ancestor project in projects sets trust_level to a value other than "trusted",
// while flagging non-table project entries or non-string trust_level values.
func checkCodexProjectsMapPolicy(path string, projects map[string]any, folders []string) error {
	var shapeErr error
	for key, raw := range projects {
		entry, ok := raw.(map[string]any)
		if !ok || entry == nil {
			if shapeErr == nil {
				shapeErr = fmt.Errorf("%s: projects.%q is not a table", path, key)
			}
			continue
		}
		if rawLevel, hasLevel := entry["trust_level"]; hasLevel {
			if _, ok := rawLevel.(string); !ok && shapeErr == nil {
				shapeErr = fmt.Errorf("%s: projects.%q.trust_level must be a string", path, key)
			}
		}
	}
	for _, folder := range folders {
		if entry, ok := projects[folder].(map[string]any); ok && entry != nil {
			if level, ok := entry["trust_level"].(string); ok {
				if level != "trusted" {
					return &controlplane.TeamPolicyRefusal{
						Kind:   controlplane.PolicyCodexProjectUntrusted,
						Client: "codex",
						Detail: level,
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
			if !ok || entry == nil {
				continue
			}
			if level, ok := entry["trust_level"].(string); ok && level != "trusted" {
				return &controlplane.TeamPolicyRefusal{
					Kind:   controlplane.PolicyCodexProjectUntrusted,
					Client: "codex",
					Detail: level,
				}
			}
		}
	}
	return shapeErr
}

// checkPermissionsFilePolicy checks permissions.deny and permissions.ask in
// Claude settings.json or Cursor cli-config.json against plan's required tools,
// rejecting wrong-shaped present permissions objects or rule lists.
func checkPermissionsFilePolicy(client, path string, plan clientTrustPlan) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	rawPerms, hasPerms := root["permissions"]
	if !hasPerms {
		return nil
	}
	perms, ok := rawPerms.(map[string]any)
	if !ok || perms == nil {
		return fmt.Errorf("%s: permissions must be a JSON object", path)
	}
	kind := controlplane.PolicyClaudePermission
	if client == "cursor" {
		kind = controlplane.PolicyCursorPermission
	}
	var shapeErr error
	for _, listName := range []string{"deny", "ask"} {
		rawList, hasList := perms[listName]
		if !hasList {
			continue
		}
		rules, _, err := extractStringsAndShapeErr(path, "permissions."+listName, rawList)
		if err != nil && shapeErr == nil {
			shapeErr = err
		}
		for _, rule := range rules {
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
	if rawAllow, hasAllow := perms["allow"]; hasAllow {
		if _, _, err := extractStringsAndShapeErr(path, "permissions.allow", rawAllow); err != nil && shapeErr == nil {
			shapeErr = err
		}
	}
	return shapeErr
}

// checkGeminiSettingsPolicy checks whether Gemini settings.json marks a required
// MCP server as untrusted (`"trust": false`) or filters out a required role tool
// via `excludeTools` or `includeTools`, rejecting wrong-shaped present fields.
func checkGeminiSettingsPolicy(path string, plan clientTrustPlan) error {
	root, err := readConfigJSONObject(path)
	if err != nil {
		return err
	}
	rawServers, hasServers := root["mcpServers"]
	if !hasServers {
		return nil
	}
	servers, ok := rawServers.(map[string]any)
	if !ok || servers == nil {
		return fmt.Errorf("%s: mcpServers must be a JSON object", path)
	}
	var shapeErr error
	for _, name := range plan.requiredServers {
		rawEntry, present := servers[name]
		if !present {
			continue
		}
		entry, ok := rawEntry.(map[string]any)
		if !ok || entry == nil {
			if shapeErr == nil {
				shapeErr = fmt.Errorf("%s: mcpServers.%s must be a JSON object", path, name)
			}
			continue
		}
		if rawTrust, hasTrust := entry["trust"]; hasTrust {
			trust, ok := rawTrust.(bool)
			if !ok {
				if shapeErr == nil {
					shapeErr = fmt.Errorf("%s: mcpServers.%s.trust must be a boolean", path, name)
				}
			} else if !trust {
				return &controlplane.TeamPolicyRefusal{
					Kind:   controlplane.PolicyGeminiServerUntrusted,
					Client: "gemini",
					Server: name,
				}
			}
		}
		if rawExclude, hasExclude := entry["excludeTools"]; hasExclude {
			excluded, _, err := extractStringsAndShapeErr(path, "mcpServers."+name+".excludeTools", rawExclude)
			if err != nil && shapeErr == nil {
				shapeErr = err
			}
			for _, tool := range plan.requiredTools[name] {
				if matched, blocked := geminiExcludeMatches(excluded, tool); blocked {
					return &controlplane.TeamPolicyRefusal{
						Kind:   controlplane.PolicyGeminiToolFiltered,
						Client: "gemini",
						Server: name,
						List:   "excludeTools",
						Detail: matched,
					}
				}
			}
		}
		if rawInclude, hasInclude := entry["includeTools"]; hasInclude {
			included, allStrings, err := extractStringsAndShapeErr(path, "mcpServers."+name+".includeTools", rawInclude)
			if err != nil && shapeErr == nil {
				shapeErr = err
			}
			if allStrings {
				for _, tool := range plan.requiredTools[name] {
					if !geminiIncludeMatches(included, tool) {
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
	return shapeErr
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

// checkGeminiTrustedMapPolicy checks decoded trustedFolders.json entries against
// folders while rejecting non-string trust values.
func checkGeminiTrustedMapPolicy(path string, root map[string]any, folders []string) error {
	var shapeErr error
	for key, raw := range root {
		if _, ok := raw.(string); !ok && shapeErr == nil {
			shapeErr = fmt.Errorf("%s: %q trust value must be a string", path, key)
		}
	}
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
	return shapeErr
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
			if rawTrusted, hasTrusted := entry["hasTrustDialogAccepted"]; hasTrusted {
				trusted, ok := rawTrusted.(bool)
				if !ok {
					return nil, false, fmt.Errorf("%s: projects[%q].hasTrustDialogAccepted must be a boolean", path, folder)
				}
				if trusted {
					continue
				}
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
