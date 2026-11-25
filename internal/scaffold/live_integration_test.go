//go:build scaffold

// This file is an external test package: it drives a generated starter
// through the control plane, and the control plane imports the starter
// package (scaffold) to initialize projects, so an internal test here would
// form an import cycle.
package scaffold_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/features/model/gateway"
	"goa.design/goa-ai/runtime/agent/model"
	"google.golang.org/grpc"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/goadesign/surla/internal/controlplane"
	"github.com/goadesign/surla/internal/designeval"
	"github.com/goadesign/surla/internal/project"
	"github.com/goadesign/surla/internal/scaffold"
	"github.com/goadesign/surla/internal/testmodule"
	"github.com/goadesign/surla/internal/testwriter"
	"github.com/goadesign/surla/internal/verify"
)

// echoSystem is a model provider that answers every request with the system
// text it received, so a test sees which prompt text a service sent.
type echoSystem struct{}

// echoStream replays one answer.
type echoStream struct {
	// chunks are returned in order; response is the final response.
	chunks   []model.Chunk
	response *model.Response
}

// starterWindowHost refuses the first external window operation. Other host
// methods are unreachable because the team owner stops on that refusal.
type starterWindowHost struct{ project.TeamHost }

// published is the Surla version the starter's go.mod pins; it exists on the
// module proxy. It matches the internal tests' fixture of the same name.
const published = testmodule.PublishedSurlaVersion

// TestStarterLiveTargetAppliesADraftToTheNextModelRequest creates and commits
// one real starter, then clones that immutable seed for two independent
// journeys. The first starts C1 with the stored evaluator before any build,
// applies a runtime prompt draft, then starts a real build and reads its
// builder assignment. The builder commits that prompt as C2 in its returned
// working copy: replacing the live target sends C2's text without a draft,
// and verification of the same card and revision passes. The second journey
// skips live drafting and verifies its own committed prompt change. All
// design evaluations, working copies, builds and suite processes are real;
// only the model and external window host are deterministic test adapters.
// It needs the network and macOS or Linux.
func TestStarterLiveTargetAppliesADraftToTheNextModelRequest(t *testing.T) {
	seed := filepath.Join(t.TempDir(), "shop")
	buildCache := testmodule.CompilerCache(t)
	generator, err := designeval.NewEvaluatorWithCache(t.TempDir(), buildCache)
	require.NoError(t, err)
	name, err := scaffold.Init(t.Context(), scaffold.Options{Dir: seed, WorkDir: t.TempDir(), Module: "example.com/shop", SurlaVersion: published, Example: true}, scaffold.RunCommand, generator)
	require.NoError(t, err)
	commit := commitAll(t, seed)

	surlaBin := filepath.Join(t.TempDir(), "surla")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", surlaBin, "./cmd/surla")
	build.Dir = filepath.Join("..", "..")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))

	gatewayAddr := serveEchoGateway(t)
	for _, tc := range []struct {
		name, changeID string
		live           bool
	}{
		{name: "draft_commit_and_verify", changeID: "starter-live-prompt", live: true},
		{name: "verify_without_live", changeID: "starter-skip-cp3"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "shop")
			starterGit(t, seed, "clone", "--no-hardlinks", "--", seed, dir)
			require.Equal(t, commit, starterGit(t, dir, "rev-parse", "HEAD"))
			starterGit(t, dir, "config", "user.name", "Starter Tester")
			starterGit(t, dir, "config", "user.email", "starter@example.com")
			starterGit(t, dir, "config", "commit.gpgsign", "false")
			starterGit(t, dir, "config", "core.hooksPath", "/dev/null")
			cardsDir, runsDir := t.TempDir(), t.TempDir()
			team := starterTeam(t, dir)
			cp, err := controlplane.New(controlplane.Options{BuildCacheDir: buildCache,
				CardsDir: cardsDir, WorkspaceDir: dir,
				Team: &controlplane.TeamBinding{Owner: "alice@example.com", Service: team},
				Verification: &controlplane.VerificationConfig{
					RunsDir: runsDir,
					Plan: verify.Plan{
						CandidateRepo: dir, CandidateModule: ".", Binaries: map[string]string{name: "./cmd/" + name},
						SuiteRepo: dir, SuiteCommit: commit, SuiteModule: "eval", SuiteCommand: "./gen/cmd/" + name + "-evals",
						ReportDir: t.TempDir(),
					},
					Command: []string{surlaBin},
					Timeout: 15 * time.Minute,
				},
				Live: &controlplane.LiveConfig{
					Command:      []string{surlaBin},
					Env:          os.Environ(),
					StartTimeout: 10 * time.Minute,
				},
			})
			require.NoError(t, err)
			t.Cleanup(func() { assert.NoError(t, cp.Close()) })
			user := starterCaller(t, controlplane.CallerUser)
			builder := starterCaller(t, controlplane.CallerBuilder)
			guide := starterCaller(t, controlplane.CallerGuide)
			// Live targets plan through the scripted gateway the user chose.
			_, err = cp.ConfigureModel(user, &gensurla.ConfigureModelPayload{GatewayAddr: &gatewayAddr})
			require.NoError(t, err)
			card := confirmCard(t, cp, guide, user, tc.changeID)
			const text = "You answer questions about item prices. [drafted]"
			if tc.live {
				// Stored-evaluator live targets need neither a build nor
				// accepted test-writer tests. Keep this start before Build.
				started, err := cp.StartLiveTarget(builder, &gensurla.StartLiveTargetPayload{ChangeID: card.ChangeID, CandidateCommit: commit})
				require.NoError(t, err)
				before := askLive(t, started.URL)
				assert.True(t, strings.HasPrefix(before, "SYSTEM="), before)
				promptID := name + ".helper.system"
				drafted, err := cp.DraftOverride(builder, &gensurla.DraftOverridePayload{
					TargetKind: controlplane.TargetKindPromptTemplate, PromptID: &promptID, NewContent: text,
				})
				require.NoError(t, err)
				assert.True(t, drafted.AppliedInStore)
				assert.Equal(t, "SYSTEM="+text, askLive(t, started.URL), "the next model request carries the drafted text")
				afterDraft, err := cp.GetChange(builder, &gensurla.GetChangePayload{ChangeID: card.ChangeID})
				require.NoError(t, err)
				assert.Equal(t, card.RequestRevision, afterDraft.RequestRevision, "a runtime draft never changes the card")
				assert.Equal(t, controlplane.StatusConfirmed, afterDraft.Status)
			}

			// CP4 requires an open build and an assignment read even when
			// the operator supplies the evaluator. The builder authors its
			// candidate only in the working copy returned by that read.
			started, err := cp.StartBuild(user, &gensurla.StartBuildPayload{
				ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
			})
			require.NoError(t, err)
			assert.Equal(t, card.RequestRevision, started.Build.RequestRevision)
			assert.Equal(t, gensurla.TeamKindUnavailable, started.Team.Kind(), "this host launches no agents")
			assert.Equal(t, gensurla.TestsKindNotUsed, started.Build.TestWriter.Tests.Kind())
			assignment, err := cp.GetBuilderAssignment(builder, &gensurla.GetBuilderAssignmentPayload{ChangeID: card.ChangeID})
			require.NoError(t, err)
			require.True(t, assignment.Read)
			require.True(t, assignment.InForce)
			require.Equal(t, card.RequestRevision, assignment.RequestRevision)
			require.NotEqual(t, dir, assignment.WorkingFolder)
			require.Equal(t, testwriter.BuilderRole.Branch(1), starterGit(t, assignment.WorkingFolder, "symbolic-ref", "--short", "HEAD"))
			require.Equal(t, commit, starterGit(t, assignment.WorkingFolder, "rev-parse", "HEAD"))
			c2 := commitStarterPrompt(t, assignment.WorkingFolder, text)
			require.NotEqual(t, commit, c2, "the candidate contains this build's own work")
			assert.Equal(t, commit, starterGit(t, dir, "rev-parse", "HEAD"), "the builder never commits in the project folder")
			if tc.live {
				restarted, err := cp.StartLiveTarget(builder, &gensurla.StartLiveTargetPayload{ChangeID: card.ChangeID, CandidateCommit: c2})
				require.NoError(t, err, "starting C2 replaces the C1 target")
				assert.Equal(t, "SYSTEM="+text, askLive(t, restarted.URL), "C2 carries the committed text without any draft")
				stopped, err := cp.StopLiveTarget(builder)
				require.NoError(t, err)
				assert.True(t, stopped.Stopped)
			}
			assertVerified(t, cp, builder, runsDir, card, c2, "the committed text verifies for the same card, including when CP3 is skipped")
		})
	}
}

// keepRun copies a failed run's directory to $SURLA_TEST_KEEP_RUNS, when it
// is set, so its evidence outlives the test's temporary directories.
func keepRun(t *testing.T, runsDir, runID string) {
	t.Helper()
	dest := os.Getenv("SURLA_TEST_KEEP_RUNS")
	if dest == "" {
		return
	}
	out, err := exec.Command("cp", "-R", filepath.Join(runsDir, runID), dest).CombinedOutput()
	if err != nil {
		t.Logf("keep run %s: %v: %s", runID, err, out)
		return
	}
	t.Logf("kept run %s in %s", runID, dest)
}

// buildLogs returns every log and evidence file a verification run left,
// each with its name and byte count, including empty files and read errors,
// so a failed run shows exactly what was and was not written.
func buildLogs(t *testing.T, runsDir, runID string) string {
	t.Helper()
	evidence, err := filepath.Glob(filepath.Join(runsDir, runID, "work", "evidence", "*"))
	if err != nil {
		return err.Error()
	}
	runLogs, err := filepath.Glob(filepath.Join(runsDir, runID, "*.log"))
	if err != nil {
		return err.Error()
	}
	logs := append(runLogs, evidence...)
	var b strings.Builder
	for _, l := range logs {
		data, err := os.ReadFile(l)
		if err != nil {
			fmt.Fprintf(&b, "== %s: read error: %v\n", filepath.Base(l), err)
			continue
		}
		fmt.Fprintf(&b, "== %s (%d bytes)\n%s\n", filepath.Base(l), len(data), data)
	}
	return b.String()
}

// confirmCard evaluates the guide's real draft, proposes that exact snapshot
// with a fixed analysis, and has the user confirm its current revision.
func confirmCard(
	t *testing.T, cp *controlplane.ControlPlane, guide, user context.Context, changeID string,
) *gensurla.ChangeCardResult {
	t.Helper()
	draft, err := cp.UpdateDraft(guide)
	require.NoError(t, err)
	require.Empty(t, draft.ToCorrect)
	refresh, err := cp.RefreshStory(guide)
	require.NoError(t, err)
	require.NotNil(t, refresh.OpID)
	opID := *refresh.OpID
	var readErr error
	require.Eventually(t, func() bool {
		refresh, readErr = cp.GetStoryRefresh(guide)
		return readErr != nil || refresh.State == "succeeded" || refresh.State == "failed"
	}, 5*time.Minute+30*time.Second, 100*time.Millisecond, "refresh %s did not finish", opID)
	require.NoError(t, readErr)
	require.Equal(t, "succeeded", refresh.State, "Story evaluation failed: %v", refresh.Diagnostic)
	require.NotNil(t, refresh.OpID)
	require.Equal(t, opID, *refresh.OpID)
	card, err := cp.ProposeChange(guide, &gensurla.ProposeChangePayload{
		ChangeID: changeID, StoryRefreshOpID: &opID,
		Prompt: "Answer price questions in a friendlier tone.",
		Analysis: &gensurla.CP1Analysis{
			Summary:     "Answer price questions in a friendlier tone.",
			Ambiguities: []*gensurla.CP1AnalysisAmbiguity{},
			Examples: []*gensurla.CP1AnalysisExample{{
				Scenario: "Tea price", InputSummary: "How much is tea?",
				ExpectedOutcome: "Answers $3.50", VerificationKind: "deterministic",
			}},
			PreservedBehaviors: []string{"Prices come only from lookup_price."},
		},
	})
	require.NoError(t, err)
	card, err = cp.ConfirmIntent(user, &gensurla.ConfirmIntentPayload{
		ChangeID: changeID, RequestRevision: card.RequestRevision, Answers: map[string]string{}, Examples: card.Examples,
	})
	require.NoError(t, err)
	require.Equal(t, controlplane.StatusConfirmed, card.Status)
	return card
}

// starterCaller supplies explicit user, guide and builder attribution for one owner.
func starterCaller(t *testing.T, kind controlplane.CallerKind) context.Context {
	t.Helper()
	return controlplane.WithPrincipal(t.Context(), controlplane.Principal{
		ID: "alice@example.com", CallerID: string(kind), Kind: kind,
	})
}

// starterTeam binds the actual project-owned team and durable role registry.
// Its unavailable window host prevents this test from launching agents.
func starterTeam(t *testing.T, dir string) *project.Server {
	t.Helper()
	state, err := project.Discover(t.TempDir(), dir)
	require.NoError(t, err)
	lock, err := state.StartLock(t.Context())
	require.NoError(t, err)
	_, err = state.Initialize("alice@example.com")
	require.NoError(t, errors.Join(err, lock.Close()))
	server, err := project.NewServer(state, func() {}, project.TeamDependencies{
		Lifetime: t.Context(), Host: starterWindowHost{}, RosterDirectory: t.TempDir(),
		Connect: func(context.Context) (project.TeamConnection, error) {
			return project.TeamConnection{}, nil
		},
		DiscoverAgents: func(context.Context) ([]*gensurla.InstalledAgentOption, error) {
			return []*gensurla.InstalledAgentOption{}, nil
		},
	})
	require.NoError(t, err)
	return server
}

// commitStarterPrompt keeps prompt wording in the actual builder's source and
// commits only that file, leaving the agreed design and operator suite intact.
func commitStarterPrompt(t *testing.T, dir, text string) string {
	t.Helper()
	planner := filepath.Join(dir, "internal", "helper", "planner.go")
	src, err := os.ReadFile(planner)
	require.NoError(t, err)
	const prefix = "systemPrompt = `"
	start := strings.Index(string(src), prefix)
	require.GreaterOrEqual(t, start, 0, "the starter declares its system prompt as a raw string")
	end := strings.Index(string(src)[start+len(prefix):], "`")
	require.GreaterOrEqual(t, end, 0, "the starter's system prompt has a closing delimiter")
	end += start + len(prefix)
	edited := string(src[:start]) + prefix + text + "`" + string(src[end+1:])
	require.NoError(t, os.WriteFile(planner, []byte(edited), 0o600))
	starterGit(t, dir, "add", "--", "internal/helper/planner.go")
	starterGit(t, dir, "commit", "--quiet", "-m", "Keep the agreed prompt wording")
	return starterGit(t, dir, "rev-parse", "HEAD")
}

// starterGit runs one bounded Git operation in this test's explicit repository.
func starterGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return strings.TrimSpace(string(out))
}

// assertVerified runs verify_change for commit on card and waits, within a
// phase deadline, until the run completes. It fails at once on a run that
// ends without a report or a report that does not pass.
func assertVerified(t *testing.T, cp *controlplane.ControlPlane, ctx context.Context, runsDir string, card *gensurla.ChangeCardResult, commit, why string) {
	t.Helper()
	run, err := cp.VerifyChange(ctx, &gensurla.VerifyChangePayload{ChangeID: card.ChangeID, RequestRevision: card.RequestRevision, CandidateCommit: commit})
	require.NoError(t, err)
	phase, cancel := context.WithTimeout(t.Context(), 15*time.Minute)
	defer cancel()
	for {
		status, err := cp.GetVerification(ctx, &gensurla.GetVerificationPayload{RunID: run.RunID})
		require.NoError(t, err)
		require.Equal(t, card.RequestRevision, status.RequestRevision)
		require.Equal(t, commit, status.CandidateCommit)
		if done, ok := status.State.AsCompleted(); ok {
			assert.True(t, done.Report.Passed, "%s: %+v", why, done.Report)
			return
		}
		if failed, ok := status.State.AsNotCompleted(); ok {
			keepRun(t, runsDir, run.RunID)
			t.Fatalf("%s: the run ended without a report: %+v\n%s", why, failed, buildLogs(t, runsDir, run.RunID))
		}
		if _, ok := status.State.AsRunning(); !ok {
			t.Fatalf("%s: unexpected verification state %v", why, status.State.Kind())
		}
		select {
		case <-phase.Done():
			t.Fatalf("%s: verification did not finish: %v", why, phase.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

// Overlays reports the test host's absent window service before any launcher work.
func (starterWindowHost) Overlays(context.Context) ([]string, error) {
	return nil, errors.New("the starter integration host has no agent windows")
}

// Complete is not used; the starter's planner streams.
func (echoSystem) Complete(context.Context, *model.Request) (*model.Response, error) {
	return nil, errors.New("echo model supports streaming only")
}

// Stream answers with "SYSTEM=" and the request's system text.
func (echoSystem) Stream(_ context.Context, req *model.Request) (model.Streamer, error) {
	var system strings.Builder
	for _, msg := range req.Messages {
		if msg.Role != model.ConversationRoleSystem {
			continue
		}
		for _, part := range msg.Parts {
			if text, ok := part.(model.TextPart); ok {
				system.WriteString(text.Text)
			}
		}
	}
	answer := "SYSTEM=" + system.String()
	msg := model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: answer}}}
	return &echoStream{
		chunks:   []model.Chunk{model.TextChunk{Message: msg}, model.StopChunk{Reason: "end_turn"}},
		response: &model.Response{Content: []model.Message{msg}, StopReason: "end_turn"},
	}, nil
}

// Recv returns the next chunk or io.EOF.
func (s *echoStream) Recv() (model.Chunk, error) {
	if len(s.chunks) == 0 {
		return nil, io.EOF
	}
	c := s.chunks[0]
	s.chunks = s.chunks[1:]
	return c, nil
}

// Response returns the final response.
func (s *echoStream) Response() *model.Response {
	return s.response
}

// Close releases nothing.
func (s *echoStream) Close() error {
	return nil
}

// commitAll commits every file in dir, creating its git repository on the
// first call, and returns the commit ID.
func commitAll(t *testing.T, dir string) string {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		cmd := exec.CommandContext(t.Context(), "git", "init", "-q")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	for _, args := range [][]string{
		{"add", "-A"},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "starter"},
	} {
		cmd := exec.CommandContext(t.Context(), "git", args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
	}
	cmd := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
}

// serveEchoGateway serves echoSystem through a goa-ai gateway on a loopback
// port for the test's lifetime and returns its address.
func serveEchoGateway(t *testing.T) string {
	t.Helper()
	gw, err := gateway.NewServer(gateway.WithProvider(echoSystem{}))
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	gateway.RegisterGRPCServer(server, gw)
	served := make(chan error, 1)
	go func() { served <- server.Serve(ln) }()
	t.Cleanup(func() {
		server.Stop()
		if err := <-served; err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			t.Errorf("echo gateway: %v", err)
		}
	})
	return ln.Addr().String()
}

// askLive asks the live starter a question over its public API and returns
// the answer.
func askLive(t *testing.T, origin string) string {
	t.Helper()
	resp, err := http.Post(origin+"/ask", "application/json", strings.NewReader(`{"question":"How much is tea?"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	var out struct {
		Answer string `json:"answer"`
	}
	require.NoError(t, json.Unmarshal(body, &out))
	return out.Answer
}
