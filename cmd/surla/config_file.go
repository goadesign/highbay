// This file provides destination validation, context-bounded locking, and JSON
// helpers shared by `surla setup -global` and worker folder-trust updates.
package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/goadesign/surla/internal/abspath"
	"github.com/goadesign/surla/internal/testwriter"
)

// configPathLock is a 1-buffered channel token serializing in-process updates
// to one resolved file path while honoring context cancellation.
type configPathLock chan struct{}

const (
	// maxConfigUpdateAttempts bounds pre-rename rechecks when an external process
	// modifies a configuration file while Surla is merging an update.
	maxConfigUpdateAttempts = 5
	// configFlockPollInterval spaces non-blocking flock retries while waiting on ctx.
	configFlockPollInterval = 10 * time.Millisecond
)

var (
	// configLocks maps each resolved path to its in-process channel lock.
	configLocks = struct {
		mu    sync.Mutex
		locks map[string]configPathLock
	}{
		locks: make(map[string]configPathLock),
	}

	// testBeforeConfigReplace is an optional test hook invoked after reading and
	// transforming a config file and before renaming the replacement into place.
	testBeforeConfigReplace func(attempt int, resolvedTarget string)
)

// configLockDir returns Surla's operator lock directory (<UserConfigDir>/surla/operator/locks)
// derived from home and getenv so concurrent project servers and `surla setup`
// serialize updates to shared client configuration files without placing `.lock`
// files inside client dot-folders.
func configLockDir(home string, getenv func(string) string, goos string) string {
	if home == "" && getenv != nil {
		home = getenv("HOME")
	}
	if home == "" {
		return ""
	}
	var cfgRoot string
	switch goos {
	case "darwin":
		cfgRoot = filepath.Join(home, "Library", "Application Support")
	default:
		if getenv != nil {
			cfgRoot = getenv("XDG_CONFIG_HOME")
		}
		if cfgRoot == "" {
			cfgRoot = filepath.Join(home, ".config")
		}
	}
	return filepath.Join(cfgRoot, "surla", "operator", "locks")
}

// validateConfigDestination expands symlinks across existing and missing path
// components and refuses destinations inside Surla worktrees or protectedRoots.
func validateConfigDestination(path string, protectedRoots []string) (string, error) {
	resolved, err := abspath.EvalMissing(path)
	if err != nil {
		return "", fmt.Errorf("resolve configuration path %s: %w", path, err)
	}
	for part := range strings.SplitSeq(filepath.ToSlash(resolved), "/") {
		if part == testwriter.BuilderRole.Folder || part == testwriter.TestsRole.Folder || part == ".surla-draft" {
			return "", fmt.Errorf("refusing to use client configuration path inside Surla worktree: %s", resolved)
		}
	}
	for _, root := range protectedRoots {
		if root == "" {
			continue
		}
		within, err := abspath.Within(root, resolved)
		if errors.Is(err, os.ErrNotExist) {
			resolvedRoot, evalErr := abspath.EvalMissing(root)
			if evalErr != nil {
				return "", evalErr
			}
			if isPathOrAncestor(resolvedRoot, resolved) {
				return "", fmt.Errorf("refusing to use client configuration path inside project tree %s: %s", root, resolved)
			}
			continue
		}
		if err != nil {
			return "", err
		}
		if within {
			return "", fmt.Errorf("refusing to use client configuration path inside project tree %s: %s", root, resolved)
		}
	}
	return resolved, nil
}

// updateLockedConfigFile serializes concurrent Surla writers (both goroutines in
// this process and other `surla serve` or `surla setup` processes sharing lockDir)
// using a context-bounded in-process lock and non-blocking `syscall.Flock` on
// `<lockDir>/<sha256(resolvedTarget)>.lock`. Before `os.Rename`, it re-reads the
// target and retries up to maxConfigUpdateAttempts times if the bytes changed
// since `os.ReadFile`; this narrows races with non-cooperating external saves
// without claiming kernel-atomic compare-and-swap across `ReadFile` and `Rename`.
func updateLockedConfigFile(
	ctx context.Context,
	lockDir, path string,
	protectedRoots []string,
	perm os.FileMode,
	transform func(before []byte, existed bool) ([]byte, bool, error),
) (wrote bool, err error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	resolvedTarget, err := validateConfigDestination(path, protectedRoots)
	if err != nil {
		return false, err
	}

	releaseLocal, err := acquireInProcessConfigLock(ctx, resolvedTarget)
	if err != nil {
		return false, err
	}
	defer releaseLocal()

	if lockDir != "" {
		unlock, lockErr := acquireCrossProcessConfigLock(ctx, lockDir, resolvedTarget, protectedRoots)
		if lockErr != nil {
			return false, lockErr
		}
		defer func() {
			err = errors.Join(err, unlock())
		}()
	}

	for attempt := range maxConfigUpdateAttempts {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		resolvedTarget, err = validateConfigDestination(path, protectedRoots)
		if err != nil {
			return false, err
		}
		before, readErr := os.ReadFile(resolvedTarget)
		existed := true
		if errors.Is(readErr, os.ErrNotExist) {
			existed = false
			before = nil
		} else if readErr != nil {
			return false, fmt.Errorf("read %s: %w", resolvedTarget, readErr)
		}

		next, write, transformErr := transform(before, existed)
		if transformErr != nil || !write {
			return false, transformErr
		}

		filePerm := perm
		if info, statErr := os.Stat(resolvedTarget); statErr == nil {
			filePerm = info.Mode().Perm()
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return false, fmt.Errorf("inspect %s: %w", resolvedTarget, statErr)
		}

		if testBeforeConfigReplace != nil {
			testBeforeConfigReplace(attempt, resolvedTarget)
		}

		replaced, retry, replaceErr := writeAndReplaceIfUnchanged(resolvedTarget, before, existed, next, filePerm)
		if replaceErr != nil {
			return false, replaceErr
		}
		if retry {
			continue
		}
		return replaced, nil
	}
	return false, fmt.Errorf("concurrent external updates prevented saving %s", resolvedTarget)
}

// acquireInProcessConfigLock acquires the in-process token for resolvedTarget or returns ctx.Err().
func acquireInProcessConfigLock(ctx context.Context, resolvedTarget string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ch := inProcessConfigLock(resolvedTarget)
	select {
	case <-ch:
		return func() { ch <- struct{}{} }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// inProcessConfigLock returns the 1-buffered channel lock for resolvedTarget.
func inProcessConfigLock(resolvedTarget string) configPathLock {
	configLocks.mu.Lock()
	defer configLocks.mu.Unlock()
	ch, ok := configLocks.locks[resolvedTarget]
	if !ok {
		ch = make(configPathLock, 1)
		ch <- struct{}{}
		configLocks.locks[resolvedTarget] = ch
	}
	return ch
}

// acquireCrossProcessConfigLock acquires an exclusive advisory flock for
// resolvedTarget inside lockDir using non-blocking LOCK_NB polls bounded by ctx.
func acquireCrossProcessConfigLock(
	ctx context.Context, lockDir, resolvedTarget string, protectedRoots []string,
) (func() error, error) {
	resolvedLockDir, err := validateConfigDestination(lockDir, protectedRoots)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(resolvedLockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create config lock folder %s: %w", resolvedLockDir, err)
	}
	sum := sha256.Sum256([]byte(resolvedTarget))
	lockPath := filepath.Join(resolvedLockDir, hex.EncodeToString(sum[:])+".lock")
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open config lock %s: %w", lockPath, err)
	}
	ticker := time.NewTicker(configFlockPollInterval)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			closeErr := file.Close()
			return nil, errors.Join(err, closeErr)
		}
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			closeErr := file.Close()
			return nil, errors.Join(fmt.Errorf("lock %s: %w", lockPath, err), closeErr)
		}
		select {
		case <-ctx.Done():
			closeErr := file.Close()
			return nil, errors.Join(ctx.Err(), closeErr)
		case <-ticker.C:
		}
	}
	return func() error {
		unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		closeErr := file.Close()
		return errors.Join(unlockErr, closeErr)
	}, nil
}

// writeAndReplaceIfUnchanged writes next to a sibling temporary file, verifies
// that resolvedTarget still matches before/existed, and renames it into place.
func writeAndReplaceIfUnchanged(
	resolvedTarget string, before []byte, existed bool, next []byte, perm os.FileMode,
) (replaced, retry bool, err error) {
	if err := os.MkdirAll(filepath.Dir(resolvedTarget), 0o755); err != nil {
		return false, false, fmt.Errorf("create folder for %s: %w", resolvedTarget, err)
	}
	file, err := os.CreateTemp(filepath.Dir(resolvedTarget), ".surla-setup-*")
	if err != nil {
		return false, false, fmt.Errorf("create update for %s: %w", resolvedTarget, err)
	}
	tempName := file.Name()
	defer func() {
		if cleanupErr := os.Remove(tempName); cleanupErr != nil && !errors.Is(cleanupErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary file for %s: %w", resolvedTarget, cleanupErr))
		}
	}()
	writeErr := file.Chmod(perm)
	if writeErr == nil {
		_, writeErr = file.Write(next)
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	if closeErr := errors.Join(writeErr, file.Close()); closeErr != nil {
		return false, false, fmt.Errorf("write %s: %w", resolvedTarget, closeErr)
	}

	current, curErr := os.ReadFile(resolvedTarget)
	if curErr != nil && !errors.Is(curErr, os.ErrNotExist) {
		return false, false, fmt.Errorf("recheck %s: %w", resolvedTarget, curErr)
	}
	unchanged := (!existed && errors.Is(curErr, os.ErrNotExist)) ||
		(existed && curErr == nil && bytes.Equal(current, before))
	if !unchanged {
		return false, true, nil
	}
	if err := os.Rename(tempName, resolvedTarget); err != nil {
		return false, false, fmt.Errorf("replace %s: %w", resolvedTarget, err)
	}
	return true, false, nil
}

// readConfigJSONObject decodes a single JSON object from path with UseNumber,
// or returns an empty map when path is absent or blank.
func readConfigJSONObject(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return decodeConfigJSONBytes(path, raw)
}

// decodeConfigJSONBytes decodes raw as a single JSON object with UseNumber,
// returning an empty map when raw is blank.
func decodeConfigJSONBytes(path string, raw []byte) (map[string]any, error) {
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

// encodeConfigJSONBytes encodes root with 2-space indentation and unescaped HTML.
func encodeConfigJSONBytes(path string, root map[string]any) ([]byte, error) {
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(root); err != nil {
		return nil, fmt.Errorf("encode JSON for %s: %w", path, err)
	}
	return encoded.Bytes(), nil
}

// isPathOrAncestor reports whether ancestor is target or an ancestor directory of target.
func isPathOrAncestor(ancestor, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(ancestor), filepath.Clean(target))
	return err == nil && filepath.IsLocal(rel)
}

// isStrictAncestorPath reports whether ancestor is a strict parent/ancestor directory of target.
func isStrictAncestorPath(ancestor, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(ancestor), filepath.Clean(target))
	return err == nil && rel != "." && filepath.IsLocal(rel)
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
