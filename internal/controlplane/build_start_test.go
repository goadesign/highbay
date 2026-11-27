// This file checks build admission outcomes without real agents or window effects.
package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/designeval"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildStartAdmissionOwnsCancellation keeps accepted work after a client
// disconnect and makes Close cancel and drain it before stores are released.
func TestBuildStartAdmissionOwnsCancellation(t *testing.T) {
	cp := newControlPlane(t, Options{})
	request, disconnect := context.WithCancel(asCaller("alice@example.com", CallerUser))
	accepted, finish, err := cp.admitBuildStart(request)
	require.NoError(t, err)
	finishStart := sync.OnceFunc(finish)
	defer finishStart()
	disconnect()
	assert.NoError(t, accepted.Err())
	principal, ok := PrincipalFromContext(accepted)
	assert.True(t, ok)
	assert.Equal(t, "alice@example.com", principal.ID)
	phase, cancelPhase := cp.buildStartDeadline(accepted)
	defer cancelPhase()
	deadline, ok := phase.Deadline()
	assert.True(t, ok)
	assert.InDelta(t, defaultBuildStartTimeout.Seconds(), time.Until(deadline).Seconds(), 1)

	closed := make(chan error, 1)
	go func() { closed <- cp.Close() }()
	select {
	case <-accepted.Done():
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel accepted work")
	}
	select {
	case err := <-closed:
		t.Fatalf("Close finished before accepted work drained: %v", err)
	default:
	}
	finishStart()
	select {
	case err := <-closed:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after accepted work drained")
	}
	_, _, err = cp.admitBuildStart(request)
	assertRefused(t, err, "unavailable")
}

// TestBuildTeamOutcomeUsesTypedConditions keeps peer-authored messages out of
// every warning and returns the original team result when admission succeeds.
func TestBuildTeamOutcomeUsesTypedConditions(t *testing.T) {
	team := &genhighbay.ProjectTeam{Team: "team", Step: "The fixture team is ready for its assignment.", Roles: []*genhighbay.ProjectTeamRole{}}
	private := "private external reply"
	cases := []struct {
		name string
		err  error
		kind genhighbay.TeamKind
	}{
		{name: "ready", kind: genhighbay.TeamKindReady},
		{name: "unavailable", err: genhighbay.MakeTeamUnavailable(errors.New(private)), kind: genhighbay.TeamKindUnavailable},
		{name: "unexpected failure", err: errors.New(private), kind: genhighbay.TeamKindUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := buildTeamOutcome(team, tc.err)
			assert.Equal(t, tc.kind, outcome.Kind())
			if ready, ok := outcome.AsReady(); ok {
				assert.Same(t, team, ready)
				assert.Equal(t, team.Step, ready.Step, "the ready wrapper preserves the owner's instruction")
			}
			if unavailable, ok := outcome.AsUnavailable(); ok {
				assert.True(t, boundedText(string(unavailable.Step), maxCheckStep))
				assert.NotContains(t, string(unavailable.Step), private)
			}
		})
	}
}

// TestBuildTeamOutcomeGivesEachCauseItsStep gives start_build the team failure
// the project owner returns, a team_unavailable error whose cause is the typed
// failure, and checks that each cause gets its own repair before the step to
// start the team agents for the saved build. An error without a typed cause,
// and a cause this version does not know, asks for that retry alone. Running highbay
// starts no agents, so only the stopping step names it, as the way to start
// the server again before the retry.
func TestBuildTeamOutcomeGivesEachCauseItsStep(t *testing.T) {
	const generic = "The project team could not be prepared. Use Team → Start workers on the views page " +
		"to start the team agents for this saved build."
	for _, tc := range []struct {
		name, cause string
		// repair is text the step must contain; an unknown cause and an
		// absent cause must equal generic instead.
		repair string
	}{
		{name: "stopping", cause: "stopping", repair: "Run highbay in the project folder, or ask your coding agent to open Highbay views; either starts the server again and opens a fresh page"},
		{name: "chorus", cause: "chorus", repair: "Run chorus up (or chorus status to see the error)"},
		{name: "herdr-core", cause: "hc", repair: "Run hc server status to see the error (highbay up installs and starts it)"},
		{name: "project state", cause: "project_state", repair: "Make sure this account can read and write this project's Highbay state (under highbay/operator in your user configuration folder) and restore anything missing or damaged there from a backup"},
		{name: "role folder", cause: "role_folder", repair: "Make sure this account can open the project folder and its build working folders"},
		{name: "a cause this version does not know", cause: "unknown_cause"},
		{name: "no typed cause"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := error(genhighbay.MakeTeamUnavailable(errors.New("private external reply")))
			if tc.cause != "" {
				failure := genhighbay.MakeTeamUnavailable(&genproject.TeamUnavailable{Name: "team_unavailable", Cause: tc.cause})
				failure.Message = "the views page's step"
				err = failure
			}
			unavailable, ok := buildTeamOutcome(nil, err).AsUnavailable()
			require.True(t, ok)
			step := string(unavailable.Step)
			if tc.repair == "" {
				assert.Equal(t, generic, step)
			} else {
				assert.Contains(t, step, tc.repair)
				assert.NotContains(t, step, "The project team could not be prepared.")
			}
			assert.True(t, strings.HasSuffix(step, "the team agents for this saved build."), step)
			if tc.cause != "stopping" {
				assert.NotContains(t, strings.ToLower(step), "run highbay")
			}
			assert.NotContains(t, step, "Herdr")
			assert.NotContains(t, step, "private external reply")
			assert.NotContains(t, step, "the views page's step")
			assert.True(t, boundedText(step, maxCheckStep), "the step fits the contract")
		})
	}
}

// TestStartedBuildDerivesRequiredTestCount reports finished flows from the saved
// Story, including a build that requires no Story test, without a saved count.
func TestStartedBuildDerivesRequiredTestCount(t *testing.T) {
	receipts := refundStory("Refund with a receipt")
	receipts[0].Name = "Receipts"
	receipts[0].Flows[0].Key = "receipt-refund"
	cases := []struct {
		name    string
		stories []designeval.StorySummary
		count   int
	}{
		{name: "no Story"},
		{name: "unfinished only", stories: []designeval.StorySummary{{Flows: []designeval.StoryFlow{{Key: "draft"}}}}},
		{name: "finished and unfinished", stories: refundStory("Refund over the limit"), count: 1},
		{name: "several Stories", stories: append(refundStory("Refund over the limit"), receipts...), count: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			build, err := startedBuild(heldBuild{file: buildFile{
				Stories: tc.stories, TestWriter: testWriterAssignment{State: testsAssigned},
			}}, genhighbay.NewBuildTakingBuilding(&genhighbay.BuildBuilding{}))
			require.NoError(t, err)
			assert.NotNil(t, build.RequiredTests)
			assert.Len(t, build.RequiredTests, tc.count)
			if tc.count > 0 {
				assert.Equal(t, &genhighbay.RequiredTest{Story: "Refunds", FlowKey: "refund-over-limit", Scenario: "refund_over_limit", Hook: "RefundOverLimit"}, build.RequiredTests[0])
			}
			if tc.count == 2 {
				assert.Equal(t, &genhighbay.RequiredTest{Story: "Receipts", FlowKey: "receipt-refund", Scenario: "receipt_refund", Hook: "ReceiptRefund"}, build.RequiredTests[1])
			}
			assert.Equal(t, tc.count, build.RequiredTestCount)
		})
	}
	raw, err := json.Marshal(buildFile{})
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "required_test_count")
	assert.NotContains(t, string(raw), "required_tests")
}
