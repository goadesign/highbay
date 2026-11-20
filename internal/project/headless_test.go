package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHeadlessArgs checks CLI-owned invocation syntax without starting a model or shell.
func TestHeadlessArgs(t *testing.T) {
	model, effort := "chosen-model", "high"
	for _, test := range []struct {
		name   string
		client string
		model  *string
		effort *string
		yolo   bool
		want   []string
	}{
		{"claude yolo", "claude", &model, &effort, true, []string{"--model", model, "--effort", effort, "--dangerously-skip-permissions", "--print"}},
		{"claude non-yolo", "claude", &model, &effort, false, []string{"--model", model, "--effort", effort, "--allowedTools", "mcp__surla__*", "--print"}},
		{"codex non-yolo", "codex", &model, &effort, false, []string{"exec", "--sandbox", "workspace-write", "--model", model, "-c", `model_reasoning_effort="high"`, "-c", `mcp_servers.surla.default_tools_approval_mode="approve"`}},
		{"codex yolo", "codex", nil, nil, true, []string{"exec", "-c", `mcp_servers.surla.default_tools_approval_mode="approve"`, "--dangerously-bypass-approvals-and-sandbox"}},
		{"antigravity yolo", "antigravity", &model, &effort, true, []string{"--model", model, "--effort", effort, "--dangerously-skip-permissions", "--print"}},
		{"antigravity non-yolo", "antigravity", &model, nil, false, []string{"--model", model, "--print"}},
		{"cursor yolo", "cursor", nil, nil, true, []string{"--print", "--yolo"}},
		{"cursor non-yolo", "cursor", nil, nil, false, []string{"--trust", "--approve-mcps", "--print"}},
		{"gemini yolo", "gemini", nil, nil, true, []string{"--approval-mode=yolo", "--prompt"}},
		{"opencode", "opencode", &model, nil, false, []string{"run", "--model", model}},
	} {
		t.Run(test.name, func(t *testing.T) {
			const prompt = "Read assignment; run tests and report."
			got, err := headlessArgs(&gensurla.RoleAgentChoice{
				Client: test.client, Model: test.model, Thinking: test.effort, Yolo: test.yolo,
			}, prompt)
			require.NoError(t, err)
			assert.Equal(t, append(test.want, prompt), got)
		})
	}
	_, err := headlessArgs(&gensurla.RoleAgentChoice{Client: "unknown"}, "Assignment")
	assert.Error(t, err)
}

// TestHeadlessBuildTurns exercises exact invocations with two fixture programs.
// The script records cwd, inherited input and literal argv, and must see EOF on stdin.
func TestHeadlessBuildTurns(t *testing.T) {
	program := writeHeadlessFixture(t, `
if IFS= read -r input; then
	exit 81
fi
printf '%s\n' "$PWD" "$WORKER_LOGIN" "$@" >> invocation
`)
	s := &Server{team: teamOperation{dependencies: TeamDependencies{
		Lifetime: t.Context(), WorkerEnv: []string{"WORKER_LOGIN=fixture-login", "PWD=surla"},
	}}}
	t.Cleanup(func() { assert.NoError(t, s.closeHeadless()) })
	roles := []launcherRole{{Role: "builder", Folder: t.TempDir()}, {Role: "test_writer", Folder: t.TempDir()}}
	choices := map[string]*gensurla.RoleAgentChoice{
		"builder":     {Client: "codex", Program: program},
		"test_writer": {Client: "claude", Program: program},
	}
	a := controlplane.TeamAssignments{
		BuildIdentity:  &controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Now()},
		BuilderHasWork: true, TestWriterHasWork: true,
		Unread: []controlplane.AssignmentGeneration{
			{Role: "builder", Generation: "build"},
			{Role: "test_writer", Generation: "initial-test"},
		},
		ReportCheckInFlight: true, VerificationInFlight: true,
	}
	require.NoError(t, s.startHeadless(t.Context(), roles, choices, a))
	build := s.team.headless.current.Load()
	require.NotNil(t, build)
	assert.Nil(t, build.builder.invocation.Load())
	assert.Nil(t, build.testWriter.invocation.Load())

	a.ReportCheckInFlight = false
	s.reconcileHeadless(t.Context(), a)
	assert.Nil(t, build.builder.invocation.Load())
	assert.Nil(t, build.testWriter.invocation.Load())

	a.VerificationInFlight = false
	s.reconcileHeadless(t.Context(), a)
	builderFirst := waitHeadlessFixture(t, &build.builder)
	writerFirst := waitHeadlessFixture(t, &build.testWriter)
	for _, role := range roles {
		readback, err := os.ReadFile(filepath.Join(role.Folder, "invocation"))
		require.NoError(t, err)
		lines := strings.Split(string(readback), "\n")
		gotFolder, err := filepath.EvalSymlinks(lines[0])
		require.NoError(t, err)
		wantFolder, err := filepath.EvalSymlinks(role.Folder)
		require.NoError(t, err)
		assert.Equal(t, wantFolder, gotFolder)
		assert.Equal(t, "fixture-login", lines[1])
		assert.Contains(t, string(readback), "Work on your current Surla assignment")
		assert.NotContains(t, strings.ToLower(string(readback)), "chorus")
	}

	// A rejected report supplies a new test-writer assignment generation.
	a.Unread = []controlplane.AssignmentGeneration{{Role: "test_writer", Generation: "rejected-report"}}
	a.ReportCheckInFlight = true
	s.reconcileHeadless(t.Context(), a)
	assert.Same(t, writerFirst, build.testWriter.invocation.Load())
	a.ReportCheckInFlight = false
	s.reconcileHeadless(t.Context(), a)
	assert.NotSame(t, writerFirst, waitHeadlessFixture(t, &build.testWriter))
	assert.Same(t, builderFirst, build.builder.invocation.Load())

	// Test acceptance wakes the builder even without unread facts, for example
	// when an attached worker session has already read the current assignment.
	a.Unread = nil
	a.TestWriterHasWork = false
	s.reconcileHeadless(t.Context(), a)
	builderSecond := waitHeadlessFixture(t, &build.builder)
	assert.NotSame(t, builderFirst, builderSecond)

	// A failed verification wakes it once, after its owner completes the run.
	a.LatestCompletedRunID = "failed-1"
	a.VerificationInFlight = true
	s.reconcileHeadless(t.Context(), a)
	assert.Same(t, builderSecond, build.builder.invocation.Load())
	a.VerificationInFlight = false
	s.reconcileHeadless(t.Context(), a)
	builderThird := waitHeadlessFixture(t, &build.builder)
	assert.NotSame(t, builderSecond, builderThird)
	a.LatestCompletedRunID = "failed-2"
	s.reconcileHeadless(t.Context(), a)
	require.NoError(t, s.startHeadless(t.Context(), roles, choices, a))
	assert.Same(t, builderThird, build.builder.invocation.Load())
	assert.Equal(t, 3, build.builder.attempts)

	readback, err := os.ReadFile(filepath.Join(roles[0].Folder, "invocation"))
	require.NoError(t, err)
	assert.Equal(t, 3, strings.Count(string(readback), "Work on your current Surla assignment"))
}

// TestHeadlessStopDrains checks real signal delivery and reaping through the
// process-group boundary. The second script requires the two-second kill grace.
func TestHeadlessStopDrains(t *testing.T) {
	graceful := writeHeadlessFixture(t, `
exec 3<> gate
(
	trap 'printf child > child-stopped; exit 0' TERM
	printf child > child-ready
	IFS= read -r input <&3
) &
child=$!
trap 'wait "$child"; printf leader > leader-stopped; exit 0' TERM
printf '%s' "$$" > pid
wait "$child"
`)
	stubborn := writeHeadlessFixture(t, `
trap '' TERM
exec 3<> gate
printf '%s' "$$" > pid
IFS= read -r input <&3
`)
	s := &Server{team: teamOperation{dependencies: TeamDependencies{Lifetime: t.Context()}}}
	t.Cleanup(func() { assert.NoError(t, s.closeHeadless()) })
	roles := []launcherRole{{Role: "builder", Folder: t.TempDir()}, {Role: "test_writer", Folder: t.TempDir()}}
	for _, role := range roles {
		require.NoError(t, syscall.Mkfifo(filepath.Join(role.Folder, "gate"), 0o600))
	}
	choices := map[string]*gensurla.RoleAgentChoice{
		"builder":     {Client: "codex", Program: graceful},
		"test_writer": {Client: "claude", Program: stubborn},
	}
	id := controlplane.BuildIdentity{ChangeID: "change", StartedAt: time.Now()}
	a := controlplane.TeamAssignments{BuildIdentity: &id, BuilderHasWork: true, TestWriterHasWork: true}
	request, disconnect := context.WithCancel(t.Context())
	require.NoError(t, s.startHeadless(request, roles, choices, a))
	disconnect()
	for _, fixtureFile := range []string{
		filepath.Join(roles[0].Folder, "pid"),
		filepath.Join(roles[0].Folder, "child-ready"),
		filepath.Join(roles[1].Folder, "pid"),
	} {
		require.Eventually(t, func() bool {
			raw, err := os.ReadFile(fixtureFile)
			return err == nil && len(raw) != 0
		}, 5*time.Second, 10*time.Millisecond)
	}

	newer := controlplane.BuildIdentity{ChangeID: id.ChangeID, StartedAt: id.StartedAt.Add(time.Second)}
	require.NoError(t, s.stopHeadless(newer))
	assert.True(t, s.team.headless.running("builder"))
	assert.True(t, s.team.headless.running("test_writer"))

	// Close drains even a role that requires the SIGKILL escalation.
	require.NoError(t, s.closeHeadless())
	for _, role := range roles {
		assert.False(t, s.team.headless.running(role.Role))
		raw, err := os.ReadFile(filepath.Join(role.Folder, "pid"))
		require.NoError(t, err)
		var pid int
		_, err = fmt.Sscan(string(raw), &pid)
		require.NoError(t, err)
		assert.ErrorIs(t, syscall.Kill(-pid, 0), syscall.ESRCH)
	}
	assert.FileExists(t, filepath.Join(roles[0].Folder, "child-stopped"))
	assert.FileExists(t, filepath.Join(roles[0].Folder, "leader-stopped"))
	assert.Error(t, s.startHeadless(t.Context(), roles, choices, a))
}

// writeHeadlessFixture creates an executable fixture independent of agent installations.
func writeHeadlessFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker")
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o700))
	return path
}

// waitHeadlessFixture joins one short-lived fixture and verifies process-group cleanup.
func waitHeadlessFixture(t *testing.T, worker *headlessWorker) *headlessTurn {
	t.Helper()
	turn := worker.invocation.Load()
	require.NotNil(t, turn)
	select {
	case <-turn.done:
		require.NoError(t, turn.stopErr)
	case <-time.After(5 * time.Second):
		t.Fatal("worker fixture did not finish")
	}
	return turn
}
