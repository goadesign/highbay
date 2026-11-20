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
	// stdioLineageInspector reads OS process parents and Unix-domain socket
	// peers for `surla stdio` attachment. Tests supply deterministic readers.
	stdioLineageInspector struct {
		// selfPID returns the current `surla stdio` process ID.
		selfPID func() int
		// selfPPID returns the immediate parent process ID.
		selfPPID func() int
		// parentProcess returns pid's parent PID and full command line.
		parentProcess func(ctx context.Context, pid int) (int, string, error)
		// userParents returns pid -> ppid for every process of the current user.
		userParents func(ctx context.Context) (map[int]int, error)
		// unixSockets returns `lsof -nP -U -FpcdfDn` output for the current user.
		unixSockets func(ctx context.Context) (string, error)
	}
)

const (
	// maxStdioAncestorHops bounds parent-chain walks against process-ID reuse cycles.
	maxStdioAncestorHops = 32
	// maxAttachAncestorPIDs matches attach_guide's schema MaxLength on ancestor_pids.
	maxAttachAncestorPIDs = 64
	// stdioLineageTimeout bounds local ps/lsof inspection during first-call binding.
	stdioLineageTimeout = 2 * time.Second
)

// buildAttachGuidePayload constructs the attach_guide request for `surla stdio`,
// attaching OS lineage or a verified 1-to-1 Codex daemon client peer.
func buildAttachGuidePayload(ctx context.Context, terminal, client string) *genproject.AttachGuidePayload {
	return defaultStdioLineageInspector().attachPayload(ctx, terminal, client)
}

// defaultStdioLineageInspector binds real OS process and socket queries.
func defaultStdioLineageInspector() stdioLineageInspector {
	return stdioLineageInspector{
		selfPID:       os.Getpid,
		selfPPID:      os.Getppid,
		parentProcess: readProcessParentAndArgs,
		userParents:   readUserProcessParents,
		unixSockets:   readUserUnixSockets,
	}
}

// attachPayload builds the guide attachment payload from the observed process tree.
// When `codex app-server` is an ancestor, its inherited HC_TERMINAL_ID and
// ancestor chain belong to the daemon's launch environment rather than the
// requesting client, so both are dropped and DaemonClientPid is populated only
// when exactly one external process holds a Unix socket to that daemon.
func (i stdioLineageInspector) attachPayload(
	ctx context.Context, terminal, client string,
) *genproject.AttachGuidePayload {
	inspectCtx, cancel := context.WithTimeout(ctx, stdioLineageTimeout)
	defer cancel()

	var (
		ancestors     []int
		seen          = make(map[int]bool)
		daemonSpawner bool
		daemonPID     int
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
		if isCodexAppServerArgs(args) {
			daemonSpawner = true
			if daemonPID == 0 {
				daemonPID = pid
			}
		}
		pid = ppid
	}

	payload := &genproject.AttachGuidePayload{}
	if client != "" {
		payload.Client = &client
	}
	if daemonSpawner {
		if client == "codex" && daemonPID > 0 {
			parents, parentErr := i.userParents(inspectCtx)
			sockets, sockErr := i.unixSockets(inspectCtx)
			if parentErr == nil && sockErr == nil {
				excluded := excludedDaemonPIDs(daemonPID, ancestors, parents)
				if peerPID, ok := parseDaemonSocketPeer(sockets, daemonPID, excluded); ok && peerPID > 0 {
					payload.DaemonClientPid = &peerPID
				}
			}
		}
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

// isCodexAppServerArgs reports whether command-line text invokes `codex app-server`.
func isCodexAppServerArgs(args string) bool {
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

// excludedDaemonPIDs collects daemonPID, stdio's ancestors, and every process
// whose parent chain passes through daemonPID (such as helper subprocesses).
func excludedDaemonPIDs(daemonPID int, ancestors []int, parents map[int]int) map[int]bool {
	excluded := make(map[int]bool, len(ancestors)+4)
	excluded[daemonPID] = true
	for _, pid := range ancestors {
		excluded[pid] = true
	}
	for pid := range parents {
		if isProcessDescendant(pid, daemonPID, parents) {
			excluded[pid] = true
		}
	}
	return excluded
}

// isProcessDescendant reports whether pid has ancestor in its parent chain.
func isProcessDescendant(pid, ancestor int, parents map[int]int) bool {
	cur := pid
	for range maxStdioAncestorHops {
		ppid, ok := parents[cur]
		if !ok || ppid <= 1 || ppid == cur {
			return false
		}
		if ppid == ancestor {
			return true
		}
		cur = ppid
	}
	return false
}

// parseDaemonSocketPeer parses `lsof -nP -U -FpcdfDn` output and returns the
// sole external process ID connected to daemonPID over a Unix-domain socket.
// When zero or multiple external processes are connected, it returns (0, false).
func parseDaemonSocketPeer(lsofOutput string, daemonPID int, excluded map[int]bool) (int, bool) {
	if daemonPID <= 0 || lsofOutput == "" {
		return 0, false
	}
	ownPCBs := make(map[int]map[string]struct{})
	peerTargets := make(map[int]map[string]struct{})

	currentPID := 0
	currentSocket := false
	for line := range strings.SplitSeq(lsofOutput, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, err := strconv.Atoi(line[1:])
			if err != nil || pid <= 0 {
				currentPID = 0
				currentSocket = false
				continue
			}
			currentPID = pid
			currentSocket = false
		case 'f':
			currentSocket = false
		case 'd':
			if currentPID > 0 && strings.HasPrefix(line[1:], "0x") {
				currentSocket = true
				set := ownPCBs[currentPID]
				if set == nil {
					set = make(map[string]struct{})
					ownPCBs[currentPID] = set
				}
				set[line[1:]] = struct{}{}
			}
		case 'n':
			if currentPID <= 0 || !currentSocket {
				continue
			}
			target, ok := strings.CutPrefix(line[1:], "->")
			if !ok || !strings.HasPrefix(target, "0x") {
				continue
			}
			set := peerTargets[currentPID]
			if set == nil {
				set = make(map[string]struct{})
				peerTargets[currentPID] = set
			}
			set[target] = struct{}{}
		}
	}

	daemonOwn := ownPCBs[daemonPID]
	daemonTargets := peerTargets[daemonPID]
	if len(daemonOwn) == 0 && len(daemonTargets) == 0 {
		return 0, false
	}

	candidates := make(map[int]struct{})
	for pid := range ownPCBs {
		candidates[pid] = struct{}{}
	}
	for pid := range peerTargets {
		candidates[pid] = struct{}{}
	}

	var externalPeers []int
	for otherPID := range candidates {
		if otherPID <= 1 || otherPID == daemonPID || excluded[otherPID] {
			continue
		}
		connected := false
		for target := range peerTargets[otherPID] {
			if _, ok := daemonOwn[target]; ok {
				connected = true
				break
			}
		}
		if !connected {
			for own := range ownPCBs[otherPID] {
				if _, ok := daemonTargets[own]; ok {
					connected = true
					break
				}
			}
		}
		if connected {
			externalPeers = append(externalPeers, otherPID)
			if len(externalPeers) > 1 {
				return 0, false
			}
		}
	}
	if len(externalPeers) != 1 {
		return 0, false
	}
	return externalPeers[0], true
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

// readUserProcessParents queries `ps` for pid -> ppid across the current UID's processes.
func readUserProcessParents(ctx context.Context) (map[int]int, error) {
	uid := strconv.Itoa(os.Geteuid())
	cmd := exec.CommandContext(ctx, "ps", "-u", uid, "-o", "pid=,ppid=")
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	parents := make(map[int]int)
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		ppid, ppidErr := strconv.Atoi(fields[1])
		if pidErr == nil && ppidErr == nil && pid > 0 {
			parents[pid] = ppid
		}
	}
	return parents, nil
}

// readUserUnixSockets queries `lsof` for Unix-domain sockets owned by the current UID.
func readUserUnixSockets(ctx context.Context) (string, error) {
	uid := strconv.Itoa(os.Geteuid())
	cmd := exec.CommandContext(ctx, "lsof", "-a", "-u", uid, "-nP", "-U", "-FpcdfDn")
	cmd.Stdin = nil
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
