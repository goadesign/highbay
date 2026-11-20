// This file provides destination validation, advisory locking, and JSON
// helpers shared by `surla setup -global` and worker folder-trust updates.
package main

import (
	"bytes"
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

	"github.com/goadesign/surla/internal/abspath"
	"github.com/goadesign/surla/internal/testwriter"
)

// maxConfigUpdateAttempts bounds pre-rename rechecks when an external process
// modifies a configuration file while Surla is merging an update.
const maxConfigUpdateAttempts = 5

var (
	// configMutexes serializes in-process configuration updates per resolved path.
	configMutexes = struct {
		mu    sync.Mutex
		locks map[string]*sync.Mutex
	}{
		locks: make(map[string]*sync.Mutex),
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
// using an in-process per-path mutex and `syscall.Flock` on
// `<lockDir>/<sha256(resolvedTarget)>.lock`. Before `os.Rename`, it re-reads the
// target and retries up to maxConfigUpdateAttempts times if the bytes changed
// since `os.ReadFile`; this narrows races with non-cooperating external saves
// without claiming kernel-atomic compare-and-swap across `ReadFile` and `Rename`.
func updateLockedConfigFile(
	lockDir, path string,
	protectedRoots []string,
	perm os.FileMode,
	transform func(before []byte, existed bool) ([]byte, bool, error),
) (wrote bool, err error) {
	resolvedTarget, err := validateConfigDestination(path, protectedRoots)
	if err != nil {
		return false, err
	}

	mu := inProcessConfigMutex(resolvedTarget)
	mu.Lock()
	defer mu.Unlock()

	if lockDir != "" {
		unlock, lockErr := acquireCrossProcessConfigLock(lockDir, resolvedTarget, protectedRoots)
		if lockErr != nil {
			return false, lockErr
		}
		defer func() {
			err = errors.Join(err, unlock())
		}()
	}

	for attempt := range maxConfigUpdateAttempts {
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

// inProcessConfigMutex returns the process-wide mutex for resolvedTarget.
func inProcessConfigMutex(resolvedTarget string) *sync.Mutex {
	configMutexes.mu.Lock()
	defer configMutexes.mu.Unlock()
	mu, ok := configMutexes.locks[resolvedTarget]
	if !ok {
		mu = &sync.Mutex{}
		configMutexes.locks[resolvedTarget] = mu
	}
	return mu
}

// acquireCrossProcessConfigLock acquires an exclusive advisory flock for
// resolvedTarget inside lockDir without creating files in client dot-folders.
func acquireCrossProcessConfigLock(
	lockDir, resolvedTarget string, protectedRoots []string,
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
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		closeErr := file.Close()
		return nil, errors.Join(fmt.Errorf("lock %s: %w", lockPath, err), closeErr)
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
