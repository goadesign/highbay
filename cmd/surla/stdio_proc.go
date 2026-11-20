// This file inspects the local OS process lineage of `surla stdio` so
// `attach_guide` adopts only a Herdr pane proven to belong to this session.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	genproject "github.com/goadesign/surla/gen/project"
)

type (
	// stdioLineageInspector reads OS process parents for `surla stdio`
	// attachment. Tests supply deterministic readers.
	stdioLineageInspector struct {
		// selfPID returns the current `surla stdio` process ID.
		selfPID func() int
		// selfPPID returns the immediate parent process ID.
		selfPPID func() int
		// parentProcess returns pid's parent PID and full command line.
		parentProcess func(ctx context.Context, pid int) (int, string, error)
	}
)

const (
	// maxStdioAncestorHops bounds parent-chain walks against process-ID reuse cycles.
	maxStdioAncestorHops = 32
	// maxAttachAncestorPIDs matches attach_guide's schema MaxLength on ancestor_pids.
	maxAttachAncestorPIDs = 64
	// stdioLineageTimeout bounds local ps inspection during first-call binding.
	stdioLineageTimeout = 2 * time.Second
)

// buildAttachGuidePayload constructs the attach_guide request for `surla stdio`,
// attaching direct OS process lineage when the session is not a shared daemon.
func buildAttachGuidePayload(ctx context.Context, terminal, client string) *genproject.AttachGuidePayload {
	return defaultStdioLineageInspector().attachPayload(ctx, terminal, client)
}

// defaultStdioLineageInspector binds real OS process queries.
func defaultStdioLineageInspector() stdioLineageInspector {
	return stdioLineageInspector{
		selfPID:       os.Getpid,
		selfPPID:      os.Getppid,
		parentProcess: readProcessParentAndArgs,
	}
}

// attachPayload builds the guide attachment payload from the observed process tree.
// When `codex app-server` is an ancestor, its inherited HC_TERMINAL_ID and
// ancestor chain belong to the shared daemon rather than a proven requesting
// conversation, so both are omitted to keep the session external.
func (i stdioLineageInspector) attachPayload(
	ctx context.Context, terminal, client string,
) *genproject.AttachGuidePayload {
	inspectCtx, cancel := context.WithTimeout(ctx, stdioLineageTimeout)
	defer cancel()

	var (
		ancestors     []int
		seen          = make(map[int]bool)
		daemonSpawner bool
	)
	pid := i.selfPID()
	for range maxStdioAncestorHops {
		if pid <= 1 || seen[pid] {
			break
		}
		seen[pid] = true
		ancestors = append(ancestors, pid)
		ppid, args, err := i.parentProcess(inspectCtx, pid)
		if err != nil {
			if len(ancestors) == 1 {
				if fallback := i.selfPPID(); fallback > 1 && !seen[fallback] {
					ancestors = append(ancestors, fallback)
				}
			}
			break
		}
		if isCodexManagedDaemon(args) {
			daemonSpawner = true
		}
		pid = ppid
	}

	payload := &genproject.AttachGuidePayload{}
	if client != "" {
		payload.Client = &client
	}
	if daemonSpawner {
		return payload
	}
	if terminal != "" {
		payload.HcTerminalID = &terminal
	}
	if len(ancestors) > 0 {
		payload.AncestorPids = ancestors[:min(len(ancestors), maxAttachAncestorPIDs)]
	}
	return payload
}

// isCodexManagedDaemon reports whether command-line text invokes `codex app-server`.
func isCodexManagedDaemon(args string) bool {
	fields := strings.Fields(args)
	codexIdx := -1
	for idx, field := range fields {
		if strings.HasPrefix(field, "-") {
			continue
		}
		if filepath.Base(field) == "codex" {
			codexIdx = idx
			break
		}
	}
	if codexIdx < 0 {
		return false
	}
	skipValue := false
	for _, arg := range fields[codexIdx+1:] {
		if skipValue {
			skipValue = false
			continue
		}
		switch arg {
		case "-c", "--config", "-C", "--cd", "-m", "--model", "-p", "--profile":
			skipValue = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg == "app-server"
	}
	return false
}

// readProcessParentAndArgs queries `ps` for one process's parent PID and command line.
func readProcessParentAndArgs(ctx context.Context, pid int) (int, string, error) {
	cmd := exec.CommandContext(ctx, "ps", "-ww", "-o", "ppid=,args=", "-p", strconv.Itoa(pid))
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return 0, "", err
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return 0, "", fmt.Errorf("process %d not found", pid)
	}
	ppidField, rest, _ := strings.Cut(line, " ")
	ppidField = strings.TrimSpace(ppidField)
	if idx := strings.IndexAny(line, " \t"); idx >= 0 {
		ppidField = line[:idx]
		rest = strings.TrimSpace(line[idx+1:])
	}
	ppid, err := strconv.Atoi(ppidField)
	if err != nil {
		return 0, "", err
	}
	return ppid, rest, nil
}
