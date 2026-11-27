// This file proves build admission persists roles independently of window readiness.
package project

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/controlplane"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildRegistrationPersistsFoldersAndCredentials exercises admission, restart and authentication.
func TestBuildRegistrationPersistsFoldersAndCredentials(t *testing.T) {
	for _, storedEvaluator := range []bool{false, true} {
		t.Run(map[bool]string{false: "builder and test writer", true: "stored evaluator"}[storedEvaluator], func(t *testing.T) {
			state := testState(t)
			deps := fixtureTeamDependencies(state)
			server, err := NewServer(state, func() {}, deps)
			require.NoError(t, err)
			builder := filepath.Join(state.Record.Directory, "service")
			require.NoError(t, os.Mkdir(builder, 0o700))
			tests := t.TempDir()
			_, err = registerTestAgent(server, "custom-guide", "guide", state.Record.Directory)
			require.NoError(t, err)
			_, err = registerTestAgent(server, "custom-builder", "builder", state.Record.Directory)
			require.NoError(t, err)
			if !storedEvaluator {
				_, err = registerTestAgent(server, "custom-tests", "test_writer", state.Record.Directory)
				require.NoError(t, err)
			}
			credentials := map[string][]byte{}
			for handle := range state.Record.Agents {
				credentials[handle], err = os.ReadFile(state.AgentFile(handle))
				require.NoError(t, err)
			}
			var folders controlplane.BuildFolders = controlplane.BuilderOnly{BuilderFolder: builder}
			if !storedEvaluator {
				folders = controlplane.BuilderAndTestWriter{BuilderFolder: builder, TestWriterFolder: tests}
			}
			for attempt := 0; attempt < 2; attempt++ {
				err := server.RegisterBuildRoles(t.Context(), folders)
				require.NoError(t, err)
				assert.Len(t, state.Record.Agents, len(credentials))
			}
			assert.Equal(t, agentRecord{Role: "guide", Folder: state.Record.Directory}, state.Record.Agents["custom-guide"])
			assert.Equal(t, agentRecord{Role: "builder", Folder: builder}, state.Record.Agents["custom-builder"])
			if !storedEvaluator {
				assert.Equal(t, agentRecord{Role: "test_writer", Folder: tests}, state.Record.Agents["custom-tests"])
			}
			for handle, original := range credentials {
				raw, err := os.ReadFile(state.AgentFile(handle))
				require.NoError(t, err)
				assert.Equal(t, original, raw)
				request := httptest.NewRequest(http.MethodGet, state.Origin()+"/mcp", nil)
				request.Header.Set("Authorization", "Bearer "+string(raw))
				principal, err := server.Authenticate(request)
				require.NoError(t, err)
				assert.Equal(t, controlplane.CallerKind(state.Record.Agents[handle].Role), principal.Kind)
			}
			loaded, err := Discover(filepath.Dir(state.Dir), builder)
			require.NoError(t, err)
			assert.Equal(t, state.Record, loaded.Record)

		})
	}
}

// TestBuildRegistrationNeverCallsHerdrCore proves the documented recovery
// step: registering a build's roles saves them even while herdr-core cannot
// be reached, so a later start of the agents finds the saved build.
func TestBuildRegistrationNeverCallsHerdrCore(t *testing.T) {
	state := testState(t)
	fakes, deps := fixtureTeam(t.Context())
	fakes.core.dialErr = errors.New("hc is not installed")
	server, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	folders := controlplane.BuilderAndTestWriter{BuilderFolder: state.Record.Directory, TestWriterFolder: t.TempDir()}
	for range 2 {
		require.NoError(t, server.RegisterBuildRoles(t.Context(), folders))
	}
	loaded, err := Discover(filepath.Dir(state.Dir), state.Record.Directory)
	require.NoError(t, err)
	require.Len(t, loaded.Record.Agents, 2)
	assert.Equal(t, folders.BuilderFolder, loaded.Record.Agents[teamName("builder", state.Record.ID)].Folder)
	assert.Equal(t, folders.TestWriterFolder, loaded.Record.Agents[teamName("test_writer", state.Record.ID)].Folder)
	assert.Zero(t, fakes.core.dials, "registration must never dial herdr-core")
}

// TestBuildRegistrationRejectsInvalidConstructedFolders checks all inputs before registry or host effects.
func TestBuildRegistrationRejectsInvalidConstructedFolders(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	server, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	for _, folders := range []controlplane.BuildFolders{
		nil,
		controlplane.BuilderOnly{},
		controlplane.BuilderOnly{BuilderFolder: "relative"},
		controlplane.BuilderAndTestWriter{BuilderFolder: state.Record.Directory, TestWriterFolder: "relative"},
	} {
		err := server.RegisterBuildRoles(t.Context(), folders)
		assert.Error(t, err)
		assert.Empty(t, state.Record.Agents)
	}
}

// TestBuildRegistrationCompletesAfterPhaseCancellation keeps a saved build recoverable.
func TestBuildRegistrationCompletesAfterPhaseCancellation(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	server, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.NoError(t, server.RegisterBuildRoles(ctx, controlplane.BuilderAndTestWriter{BuilderFolder: state.Record.Directory, TestWriterFolder: t.TempDir()}))
	assert.Len(t, state.Record.Agents, 2)
}

// TestBuilderOnlyRevokesTestWriter keeps the exact roles selected by stored verification settings.
func TestBuilderOnlyRevokesTestWriter(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	server, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	require.NoError(t, server.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{BuilderFolder: state.Record.Directory, TestWriterFolder: t.TempDir()}))

	writer := teamName("test_writer", state.Record.ID)
	credential, err := os.ReadFile(state.AgentFile(writer))
	require.NoError(t, err)
	preserved := map[string][]byte{}
	for handle, agent := range state.Record.Agents {
		if agent.Role != "test_writer" {
			preserved[handle], err = os.ReadFile(state.AgentFile(handle))
			require.NoError(t, err)
		}
	}
	require.NoError(t, server.RegisterBuildRoles(t.Context(), controlplane.BuilderOnly{BuilderFolder: state.Record.Directory}))
	_, err = os.Stat(state.AgentFile(writer))
	assert.ErrorIs(t, err, os.ErrNotExist)
	assert.NotContains(t, state.Record.Agents, writer)
	request := httptest.NewRequest(http.MethodGet, state.Origin()+"/mcp", nil)
	request.Header.Set("Authorization", "Bearer "+string(credential))
	_, err = server.Authenticate(request)
	assert.Error(t, err)
	for handle, original := range preserved {
		raw, err := os.ReadFile(state.AgentFile(handle))
		require.NoError(t, err)
		assert.Equal(t, original, raw)
	}
	loaded, err := Discover(filepath.Dir(state.Dir), state.Record.Directory)
	require.NoError(t, err)
	assert.Equal(t, state.Record, loaded.Record)

	// Returning to Highbay's tests admits a fresh credential; revocation stays final.
	require.NoError(t, server.RegisterBuildRoles(t.Context(), controlplane.BuilderAndTestWriter{BuilderFolder: state.Record.Directory, TestWriterFolder: t.TempDir()}))
	freshCredential, err := os.ReadFile(state.AgentFile(writer))
	require.NoError(t, err)
	assert.NotEqual(t, credential, freshCredential)
	_, err = server.Authenticate(request)
	assert.Error(t, err)
	request.Header.Set("Authorization", "Bearer "+string(freshCredential))
	principal, err := server.Authenticate(request)
	require.NoError(t, err)
	assert.Equal(t, controlplane.CallerTestWriter, principal.Kind)

}

// TestTeamOperationsSerialize shares one registry among every entry.
func TestTeamOperationsSerialize(t *testing.T) {
	state := testState(t)
	deps := fixtureTeamDependencies(state)
	server, err := NewServer(state, func() {}, deps)
	require.NoError(t, err)
	var workers sync.WaitGroup
	for range 6 {
		workers.Go(func() {
			err := server.RegisterBuildRoles(t.Context(), controlplane.BuilderOnly{BuilderFolder: state.Record.Directory})
			assert.NoError(t, err)
		})
		workers.Go(func() {
			_, err := server.AttachGuide(teamContext(state), &genproject.AttachGuidePayload{})
			assert.NoError(t, err)
		})
		workers.Go(func() {
			_, err := server.ReadTeam(t.Context())
			assert.NoError(t, err)
		})
	}
	workers.Wait()
	assert.Len(t, state.Record.Agents, 2)
}

// TestStrictAgentRecord rejects the former string shape and invalid structured records.
func TestStrictAgentRecord(t *testing.T) {
	for _, agent := range []string{`"builder"`, `{"role":"builder"}`, `{"role":"manager","folder":"/work"}`, `{"role":"builder","folder":"relative"}`, `{"role":"builder","folder":"/work\u0000bad"}`, `{"role":"builder","folder":"/work","first_message":"stale"}`} {
		t.Run(agent, func(t *testing.T) {
			state := testState(t)
			raw, err := json.Marshal(state.Record)
			require.NoError(t, err)
			var object map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &object))
			object["agents"] = json.RawMessage(`{"builder":` + agent + `}`)
			raw, err = json.Marshal(object)
			require.NoError(t, err)
			require.NoError(t, writePrivate(filepath.Join(state.Dir, "project.json"), raw))
			_, err = Discover(filepath.Dir(state.Dir), state.Record.Directory)
			assert.Error(t, err)
		})
	}
}

// TestFirstMessagesNameOnlyAllowedOperations checks each named operation against the invocation policy.
// File paths such as eval/design/story_tests.go name files, not operations.
func TestFirstMessagesNameOnlyAllowedOperations(t *testing.T) {
	names := regexp.MustCompile(`\b[a-z]+(?:_[a-z]+)+\b`)
	files := regexp.MustCompile(`\S+\.go\b`)
	for _, role := range []string{"guide", "builder", "test_writer"} {
		message := roleInstructions(role)
		assert.True(t, controlplane.OperationAllowed(controlplane.CallerKind(role), "golden_path"))
		for _, operation := range names.FindAllString(files.ReplaceAllString(message, ""), -1) {
			// Assignment/report fields name data, not calls. All remaining
			// snake-case words must remain permitted operations for this role.
			if role == "guide" && (operation == "draft_option" || operation == "worked_example") {
				continue
			}
			if role == "builder" && (operation == "working_folder" || operation == "gowork_off" || operation == "since_version") {
				continue
			}
			if role == "test_writer" && (operation == "coverage_statements" || operation == "question_ids" || operation == "example_ids" || operation == "since_version") {
				continue
			}
			assert.Contains(t, genhighbay.MethodNames, operation)
			assert.True(t, controlplane.OperationAllowed(controlplane.CallerKind(role), operation), "%s may not call %s", role, operation)
		}
	}
}

// TestProjectRecordRejectsTrailingData preserves the single strict stored-document contract.
func TestProjectRecordRejectsTrailingData(t *testing.T) {
	state := testState(t)
	raw, err := json.Marshal(state.Record)
	require.NoError(t, err)
	raw = append(raw, []byte("\n{}")...)
	require.NoError(t, writePrivate(filepath.Join(state.Dir, "project.json"), raw))
	_, err = Discover(filepath.Dir(state.Dir), state.Record.Directory)
	assert.ErrorContains(t, err, "trailing project state")
}

// TestTheTestWriterMayRegenerateItsTestModule checks the test writer's first
// message: Highbay writes the Story table and the test writer leaves it as it
// is, reporting or regenerating after a design change, because regenerating
// keeps the required tests. No role is told that regenerating drops a test.
func TestTheTestWriterMayRegenerateItsTestModule(t *testing.T) {
	message := roleInstructions("test_writer")
	for _, sentence := range []string{
		"Highbay writes eval/design/story_tests.go, which declares the required tests, from the agreed Story",
		"leave that file as it is",
		"report to have it generated again or run highbay gen -dir eval in the working folder first",
		"regenerating keeps the required tests",
	} {
		assert.Contains(t, message, sentence)
	}
	for _, role := range []string{"guide", "builder", "test_writer"} {
		message := roleInstructions(role)
		assert.NotContains(t, message, "regenerating drops", role)
		assert.NotContains(t, message, "never regenerate", role)
	}
}

// TestFirstMessagesKeepTheRolesInTheirOwnedFolders checks the role boundaries
// alongside the operation-policy check, including stop and branch invariants.
func TestFirstMessagesKeepTheRolesInTheirOwnedFolders(t *testing.T) {
	for _, tc := range []struct {
		role     string
		required []string
	}{
		{"guide", []string{
			"Set an ambiguity's draft_option to the option your draft already implements whenever another option would change the design, Story, examples or preserved behaviors",
			"ask one short, plain question and wait; never ask for a Go module path, folder name or requirements checklist",
			"call init_project with dir \".\" and no module path unless the user gave one",
			"Before that call, tell the user in one sentence that creating the project downloads Go modules and can take a few minutes the first time, and that the Highbay page shows its progress.",
			"instead of interviewing them in chat: put open product questions",
			"into the analysis's ambiguities with concrete options, which the user answers on the views page",
			"init_project creates a blank project; pass worked_example only when the user asks to see Highbay's worked example.",
			"The project's starting files are plumbing the user never asked about: never mention or ask about them on the Change Card or in chat",
			"without repository file paths (such as design/design.go) or Highbay runner flags (such as -listen)",
			"call update_draft before each new change",
			"Work only in the draft folder returned by update_draft or refresh_story",
			"never edit the project folder or copy design files between it and the draft",
			"when one is in the project folder, tell the user what to correct there and wait",
			"run the same formatter in the draft, refresh and revise",
		}},
		{"builder", []string{
			"Read each new assignment with get_builder_assignment before any other work",
			"Then run git status: implementation files you authored or generated for this build are expected current work.",
			"If you find unexpected files or work from an earlier build, stop and tell the user before carrying it forward.",
			"it writes the agreed design into design/ and regenerates gen/; never edit either",
			"When your assignment's gowork_off is true",
			"Never merge, cherry-pick or copy files from another highbay/ branch",
			"While you wait for that, do not stop and do not wait for a message: call wait_for_builder_assignment, passing the version it returned as since_version",
			"when your client does not offer that tool, call list_builder_assignments about every 30 seconds instead",
		}},
		{"test_writer", []string{
			"Read each new assignment with get_test_writer_assignment before any other work",
			"Then run git status: the Story table and generated test-module files Highbay just prepared, and tests you already authored for this build, are expected current work.",
			"Leave Highbay's prepared files unchanged.",
			"If you find unexpected files or work from an earlier build, stop and tell the user before carrying it forward.",
			"do not commit: Highbay commits what it accepts",
			"Run every go command as GOWORK=off",
			"Never bring files or commits from another highbay/ branch",
			"Never build or start the application itself, which is every program under cmd/, and never run your tests against it: Highbay's verification builds and runs the application.",
			"after reporting, do not stop and do not wait for a message: call wait_for_test_writer_assignment, and while the list it returns still shows your assignment as reported, call it again, passing the version it returned as since_version",
			"Highbay may reach its verdict before your first call, so decide by what the list shows: as soon as it shows another state or no assignment at all, read get_test_writer_assignment again.",
			"when your client does not offer that tool, call list_test_writer_assignments about every 30 seconds instead",
		}},
	} {
		t.Run(tc.role, func(t *testing.T) {
			message := roleInstructions(tc.role)
			for _, text := range tc.required {
				assert.Contains(t, message, text)
			}
			if tc.role == "guide" {
				return
			}
			assert.NotContains(t, message, "a file it lists was left by earlier work")
			for _, text := range []string{
				"An empty list means no build needs you: say so and stop.",
				"When a Highbay call says a build was dropped or a new one started, stop",
				"Never name, move or delete a highbay/ branch.",
			} {
				assert.Contains(t, message, text)
			}
		})
	}
}

// TestFirstMessageNamesTheConversation checks what every started role is told
// before its instructions: the swarm, channel and handle to join in Chorus
// and the watch to keep, and for a worker that Highbay tells it there about
// an unread assignment.
func TestFirstMessageNamesTheConversation(t *testing.T) {
	for _, role := range []string{"guide", "builder", "test_writer"} {
		t.Run(role, func(t *testing.T) {
			message := roleFirstMessage(teamRole{Role: role, Handle: role + "-0123456789ab"}, "refunds-0123456789ab")
			assert.True(t, strings.HasPrefix(message, "Use the installed chorus skill now. Join swarm 'refunds-0123456789ab', "+
				"channel '#refunds-0123456789ab', using handle '"+role+"-0123456789ab' and role '"+roleTitle(role)+"'."))
			assert.Contains(t, message, "Keep `chorus watch "+role+"-0123456789ab -wait 24h` running in the background while you work.")
			assert.True(t, strings.HasSuffix(message, roleInstructions(role)))
			assert.Equal(t, role != "guide", strings.Contains(message, "Highbay messages you there when you have an unread assignment"))
		})
	}
}
