// This file tests the Highbay Studio redesign contracts across DraftChangeWorkspace
// send confirmation and recovery, ProposalReviewQueue non-displacement, revision-guarded
// composer attachments and recipient detachment, per-change draft isolation and
// refresh generation guards, multi-draft pending-link recovery, opaque history.state
// navigation, and actionable current-build failure routing.
package controlplane

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

type (
	// pageStudioFailureFixture attaches a LatestVerificationItem to ListChanges
	// matching ControlPlane's on-disk verification summary contract.
	pageStudioFailureFixture struct {
		*pageVerifications
		changeID string
		latest   *genhighbay.LatestVerificationItem
	}
)

// ListChanges returns the control plane's change list with the fixture's latest verification summary.
func (s *pageStudioFailureFixture) ListChanges(ctx context.Context) (*genhighbay.ChangeList, error) {
	res, err := s.pageVerifications.ListChanges(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range res.Changes {
		if item.ChangeID == s.changeID {
			item.LatestVerification = s.latest
		}
	}
	return res, nil
}

// TestViewsPageStudioDraftChangeSendAndRecovery exercises Moment 0 and + New change:
// uncertain sends (503 and invalid 201 body) vs. 422 refusals, post-receipt Guide
// restart without re-posting the message, linked Chorus reply without duplicate
// new_change context, and ProposalReviewQueue preserving an active + New change draft.
func TestViewsPageStudioDraftChangeSendAndRecovery(t *testing.T) {
	var (
		mu   sync.Mutex
		sent []*genhighbay.SendTeamMessagePayload
	)
	team := &teamServiceFixture{
		snapshot: genhighbay.TeamSnapshot{
			Team:         "studio-team",
			Channel:      "#studio-team",
			ChorusStatus: "connected",
			Roles: []*genhighbay.TeamRoleState{
				{Role: "guide", Handle: "guide-1", HasTerminal: true, State: "waiting", Attention: "none", Assignment: "no_work"},
				{Role: "builder", Handle: "builder-1", HasTerminal: true, State: "waiting", Attention: "none", Assignment: "nothing_owed"},
				{Role: "test_writer", Handle: "writer-1", HasTerminal: true, State: "waiting", Attention: "none", Assignment: "nothing_owed"},
			},
			SavedChoices:     map[string]*genhighbay.RoleAgentChoice{},
			DismissedNotices: []string{},
		},
		conversation: genhighbay.TeamConversation{
			Team:         "studio-team",
			Channel:      "#studio-team",
			ChorusStatus: "connected",
		},
	}
	team.send = func(_ context.Context, p *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error) {
		mu.Lock()
		sent = append(sent, p)
		mu.Unlock()
		team.mu.Lock()
		defer team.mu.Unlock()
		seq := len(team.conversation.Messages) + 1
		msg := &genhighbay.TeamMessage{
			ID:         "#studio-team:" + string(rune('0'+seq)),
			Target:     "#studio-team",
			Seq:        seq,
			Sender:     "you",
			SenderRole: "you",
			Text:       p.Text,
			ReplyTo:    p.ReplyTo,
			SentAtMs:   time.Now().UnixMilli(),
		}
		team.conversation.Messages = append(team.conversation.Messages, msg)
		return msg, nil
	}

	workspace := t.TempDir()
	writeWorkspaceFiles(t, workspace, validWorkspaceStub("package design\n"))
	cp := newBuildControlPlane(t, Options{
		WorkspaceDir: workspace,
		Team:         &TeamBinding{Owner: pageOwner, Service: team},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond

	ps := startPageServer(t, cp)
	ps.act("guide-ended", func() {
		team.mu.Lock()
		defer team.mu.Unlock()
		roles := make([]*genhighbay.TeamRoleState, len(team.snapshot.Roles))
		for i, r := range team.snapshot.Roles {
			c := *r
			roles[i] = &c
		}
		roles[0].State = "ended"
		roles[0].HasTerminal = false
		team.snapshot.Roles = roles
	})
	ps.act("guide-waiting-with-question", func() {
		team.mu.Lock()
		defer team.mu.Unlock()
		roles := make([]*genhighbay.TeamRoleState, len(team.snapshot.Roles))
		for i, r := range team.snapshot.Roles {
			c := *r
			roles[i] = &c
		}
		roles[0].State = "waiting"
		roles[0].HasTerminal = true
		team.snapshot.Roles = roles
		firstSeq := 1
		msgs := append([]*genhighbay.TeamMessage{}, team.conversation.Messages...)
		msgs = append(msgs, &genhighbay.TeamMessage{
			ID:         "#studio-team:question",
			Target:     "#studio-team",
			Seq:        len(msgs) + 1,
			Sender:     "guide-1",
			SenderRole: "guide",
			Text:       "Should refunds support partial amounts?",
			ReplyTo:    &firstSeq,
			SentAtMs:   time.Now().UnixMilli(),
		})
		team.conversation.Messages = msgs
	})
	ps.act("propose-first", func() {
		guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
		op := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
		_, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
			ChangeID:         "chg-first",
			StoryRefreshOpID: &op,
			Prompt:           "Build refund workflows.",
			Analysis:         refundAnalysis("Build refund workflows."),
		})
		require.NoError(t, err)
	})
	ps.act("propose-second", func() {
		guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
		wsDir, _, _ := cp.currentWorkspaceTarget()
		writeWorkspaceFiles(t, filepath.Join(wsDir, draftFolderName), map[string]string{"design/design.go": "package design // second\n"})
		op := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
		_, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
			ChangeID:         "chg-second",
			StoryRefreshOpID: &op,
			Prompt:           "Add dispute webhooks.",
			Analysis:         refundAnalysis("Add dispute webhooks."),
		})
		require.NoError(t, err)
	})

	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-describe-input") && page.$("#highbay-describe-submit"), "Moment 0 DraftChangeWorkspace did not open");
expect(page.$$('[data-primary-action="true"]').length === 1 && page.$("#highbay-describe-submit").dataset.primaryAction === "true", "Moment 0 must highlight #highbay-describe-submit as the sole primary action");

page.type(page.$("#highbay-describe-input"), "Build refund workflows");

// 1. 503 team_unavailable -> uncertain banner, draft preserved, no sent receipt.
await control(origin, "reply", { key: "POST /team/messages", status: 503, body: { name: "team_unavailable", id: "u1", message: "Chorus timeout after publish.", temporary: true, timeout: true, fault: false } });
page.$("#highbay-describe-submit").click();
await until(() => page.$("#highbay-describe-uncertain"), "503 did not render #highbay-describe-uncertain");
expect(page.text("#highbay-describe-uncertain").includes("Send not confirmed") && page.$("#highbay-describe-input").value === "Build refund workflows" && !page.$("#highbay-draft-receipt"), "503 lost draft or rendered receipt");

// 2. 201 with invalid payload -> uncertain banner, draft preserved.
await control(origin, "reply", { key: "POST /team/messages", status: 201, body: {} });
page.$("#highbay-describe-send-again").click();
await until(() => page.sent("POST", "/team/messages").length === 2 && page.$("#highbay-describe-uncertain"), "invalid 201 body did not keep #highbay-describe-uncertain");
expect(page.$("#highbay-describe-input").value === "Build refund workflows" && !page.$("#highbay-draft-receipt"), "invalid 201 body cleared draft");

// 3. 422 refusal -> destructive #highbay-describe-error, clears uncertain banner, preserves draft.
await control(origin, "reply", { key: "POST /team/messages", status: 422, body: { name: "unprocessable", id: "u2", message: "Prompt rejected by policy.", temporary: false, timeout: false, fault: false } });
page.$("#highbay-describe-send-again").click();
await until(() => page.$("#highbay-describe-error"), "422 did not render #highbay-describe-error");
expect(!page.$("#highbay-describe-uncertain") && page.text("#highbay-describe-error").includes("Prompt rejected by policy.") && page.$("#highbay-describe-input").value === "Build refund workflows", "422 did not replace uncertain state cleanly");

// 4. Confirmed 201 send -> receipt shown, draft cleared.
page.$("#highbay-describe-submit").click();
await until(() => page.$("#highbay-draft-receipt"), "confirmed 201 send did not render #highbay-draft-receipt");
expect(page.text("#highbay-draft-receipt").includes("Build refund workflows"), "receipt omitted sent message text");

// 5. Post-receipt Guide stop -> Restart Guide calls POST /team/roles/guide/restart without re-posting POST /team/messages.
const sendsBeforeRestart = page.sent("POST", "/team/messages").length;
await control(origin, "act", { key: "guide-ended" });
page.$("#highbay-nav-team").click();
page.$("#highbay-team-refresh").click();
await until(() => page.text("#highbay-team-stage").includes("Ended"), "Team snapshot did not observe ended Guide");
page.win.history.back();
await until(() => page.$("#highbay-draft-restart-guide"), "ended Guide after confirmed receipt did not offer #highbay-draft-restart-guide");
page.$("#highbay-draft-restart-guide").click();
await until(() => page.sent("POST", "/team/roles/guide/restart").length === 1, "Restart Guide did not call POST /team/roles/guide/restart");
expect(page.sent("POST", "/team/messages").length === sendsBeforeRestart, "Restart Guide re-posted the user request");

// 6. Linked Chorus clarification (reply_to === sentMessage.seq) -> focuses composer with ONLY reply_to (no new_change).
await control(origin, "act", { key: "guide-waiting-with-question" });
page.$("#highbay-nav-team").click();
page.$("#highbay-team-refresh").click();
await until(() => page.text("#highbay-team-stage").includes("Waiting"), "Guide waiting state did not refresh");
page.win.history.back();
page.$("#highbay-conversation-refresh").click();
await until(() => page.$("#highbay-draft-reply-conversation"), "linked Guide reply did not render #highbay-draft-reply-conversation");
expect(page.text("#highbay-draft-guide-reply").includes("Should refunds support partial amounts?"), "linked Guide question text missing");
page.$("#highbay-draft-reply-conversation").click();
expect(page.doc.activeElement === page.$("#highbay-composer-input") && page.$("#highbay-composer-reply-chip") && !page.$("#highbay-composer-context-chip"), "Reply in conversation did not attach solely reply_to");
page.type(page.$("#highbay-composer-input"), "Yes, support partial refunds.");
page.$("#highbay-composer-send").click();
await until(() => page.sent("POST", "/team/messages").length === sendsBeforeRestart + 1, "clarification reply was not sent");

// 7. 0 -> 1 proposal auto-opens Agree; + New change attaches new_change and ProposalReviewQueue preserves active draft.
await control(origin, "act", { key: "propose-first" });
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-first", "first proposal did not auto-open Agree");
page.$("#highbay-new-change").click();
await until(() => page.$("#highbay-new-change-stage") && page.$("#highbay-describe-input"), "+ New change did not open");
page.type(page.$("#highbay-describe-input"), "Add dispute webhooks");
page.$("#highbay-describe-submit").click();
await until(() => page.$("#highbay-draft-receipt"), "+ New change send did not record receipt");
page.$("#highbay-draft-start-another").click();
await until(() => page.$("#highbay-describe-input"), "Start another request did not reopen form");
page.type(page.$("#highbay-describe-input"), "Keep this third request safe while chg-second arrives");
await control(origin, "act", { key: "propose-second" });
await until(() => page.$("#highbay-proposals-ready") && page.$("#highbay-review-ready-chg-second"), "concurrent proposal did not appear in #highbay-proposals-ready");
expect(page.$("#highbay-describe-input").value === "Keep this third request safe while chg-second arrives", "concurrent proposal overwrote active + New change draft");
page.close();
`)

	mu.Lock()
	recorded := append([]*genhighbay.SendTeamMessagePayload{}, sent...)
	mu.Unlock()
	require.Len(t, recorded, 3)
	nc0, ok0 := recorded[0].Context.AsNewChange()
	require.True(t, ok0, "Moment 0 request attaches new_change context")
	assert.Equal(t, "separate_card", nc0.Scope)
	assert.NotNil(t, recorded[1].ReplyTo, "linked clarification reply carries reply_to")
	assert.Empty(t, recorded[1].Context.Kind(), "linked clarification reply must not re-attach new_change")
	nc2, ok2 := recorded[2].Context.AsNewChange()
	require.True(t, ok2, "+ New change request attaches new_change context")
	assert.Equal(t, "separate_card", nc2.Scope)
}

// TestViewsPageStudioComposerGuardsAndRecipientDetachment verifies that stale
// refine_change and revise_answers contexts hold submission until reconciled
// against the latest card revision, and that switching To: away from guide
// detaches guide-only contexts while keeping story_flow for non-guide roles.
func TestViewsPageStudioComposerGuardsAndRecipientDetachment(t *testing.T) {
	var (
		mu   sync.Mutex
		sent []*genhighbay.SendTeamMessagePayload
	)
	team := &teamServiceFixture{snapshot: pageTeamSnapshot("waiting")}
	team.send = func(_ context.Context, p *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error) {
		mu.Lock()
		sent = append(sent, p)
		mu.Unlock()
		return fixtureTeamMessage(p.Text), nil
	}

	workspace := t.TempDir()
	writeWorkspaceFiles(t, workspace, validWorkspaceStub("package design\n"))
	cp := newBuildControlPlane(t, Options{
		WorkspaceDir: workspace,
		Team:         &TeamBinding{Owner: pageOwner, Service: team},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond

	guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
	op := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	usdOpt := "usd"
	noOpt := "no"
	r1, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID:         "chg-guard",
		StoryRefreshOpID: &op,
		Prompt:           "Configure refund limits.",
		Analysis: &genhighbay.CP1Analysis{
			Summary: "Configure refund limits.",
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{
				{Question: "Limit currency?", Options: []string{"usd", "multi"}, DraftOption: &usdOpt},
				{Question: "Legacy CSV?", Options: []string{"no", "yes"}, DraftOption: &noOpt},
			},
			Examples: []*genhighbay.CP1AnalysisExample{
				{Scenario: "USD refund", InputSummary: "$25", ExpectedOutcome: "Allowed", VerificationKind: "deterministic"},
			},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)
	require.Len(t, r1.Ambiguities, 2)
	qKeepID := r1.Ambiguities[0].ID
	qDropID := r1.Ambiguities[1].ID

	var r2Rev string
	ps := startPageServer(t, cp)
	ps.act("advance-revision", func() {
		writeWorkspaceFiles(t, filepath.Join(workspace, draftFolderName), map[string]string{"design/design.go": "package design // v2\n"})
		op2 := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
		priorEx := r1.Examples[0].ID
		updated, err := cp.ReviseChange(guide, &genhighbay.ReviseChangePayload{
			ChangeID:         "chg-guard",
			RequestRevision:  r1.RequestRevision,
			StoryRefreshOpID: &op2,
			Prompt:           "Configure refund limits v2.",
			RevisionNote:     "Drop legacy CSV question.",
			Analysis: &genhighbay.CP1RevisionAnalysis{
				Summary: "Configure refund limits v2.",
				Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{
					{Question: "Limit currency?", Options: []string{"usd", "multi"}, DraftOption: &usdOpt},
				},
				Examples: []*genhighbay.CP1RevisionAnalysisExample{
					{PriorID: &priorEx, Scenario: "USD refund", InputSummary: "$25", ExpectedOutcome: "Allowed", VerificationKind: "deterministic"},
				},
				RemovedExampleIds:  []string{},
				PreservedBehaviors: []string{},
			},
		})
		require.NoError(t, err)
		r2Rev = updated.RequestRevision
	})

	runPageScript(t, ps, map[string]any{"qKeep": qKeepID, "qDrop": qDropID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-guard", "chg-guard did not open on Agree");

// 1. Seed revise_answers across [qKeep=multi, qDrop=yes], then advance card to R2 (which drops qDrop).
page.$('button[data-ambiguity-id="' + input.qKeep + '"][data-option="multi"]').click();
page.$('button[data-ambiguity-id="' + input.qDrop + '"][data-option="yes"]').click();
await until(() => page.$("#highbay-confirm-revision-needed-compose"), "RevisionAid did not appear");
page.$("#highbay-confirm-revision-needed-compose").click();
expect(page.$("#highbay-composer-context-chip")?.textContent.includes("2 answers"), "revise_answers chip missing");

await control(origin, "act", { key: "advance-revision" });
await until(() => page.$("#highbay-composer-stale-context"), "stale revise_answers guard did not appear after revision advance");
expect(page.$("#highbay-composer-send").disabled && page.$("#highbay-composer-send-reconciled"), "stale revise_answers did not hold Send or offer reconciled send");
page.$("#highbay-composer-send-reconciled").click();
await until(() => page.sent("POST", "/team/messages")[0]?.status === 201, "Send reconciled answers did not complete");

// 2. Seed refine_change, then switch To: from guide to builder -> detaches refine_change while keeping user text.
await until(() => page.$("#highbay-agree-refine-with-guide"), "Ask Guide to revise button missing");
page.$("#highbay-agree-refine-with-guide").click();
page.type(page.$("#highbay-composer-input"), "Add audit logging to refund limits.");
expect(page.$("#highbay-composer-context-chip")?.textContent.includes("chg-guard"), "refine_change chip missing");

const toSelect = page.$("#highbay-composer-to");
toSelect.value = "builder";
toSelect.dispatchEvent(new page.win.Event("change", { bubbles: true }));
expect(page.$("#highbay-composer-detached-notice") && !page.$("#highbay-composer-context-chip"), "switching recipient to builder did not detach refine_change");
expect(page.$("#highbay-composer-input").value === "Add audit logging to refund limits.", "detaching refine_change lost user text");

// 3. Open Story, attach story_flow, switch To: to builder -> story_flow remains valid & attached!
page.$("#highbay-nav-story").click();
await until(() => page.$('button[aria-label^="Ask Guide about flow "]'), "Story flow Ask Guide button did not render");
page.$('button[aria-label^="Ask Guide about flow "]').click();
expect(page.$("#highbay-composer-context-chip")?.textContent.includes("Story:"), "story_flow chip did not attach");
const storyToSelect = page.$("#highbay-composer-to");
storyToSelect.value = "builder";
storyToSelect.dispatchEvent(new page.win.Event("input", { bubbles: true }));
storyToSelect.dispatchEvent(new page.win.Event("change", { bubbles: true }));
expect(storyToSelect.value === "builder" && page.$("#highbay-composer-context-chip")?.textContent.includes("To Builder") && page.$("#highbay-composer-context-chip")?.textContent.includes("Story:") && !page.$("#highbay-composer-detached-notice"), "switching recipient to builder wrongly detached story_flow");
page.type(page.$("#highbay-composer-input"), "Check this Story flow in builder.");
expect(!page.$("#highbay-composer-send").disabled, "builder story_flow send remained disabled");
page.$("#highbay-composer-send").click();
await until(() => page.sent("POST", "/team/messages")[1]?.status === 201, "builder story_flow message did not complete");
page.close();
`)

	mu.Lock()
	recorded := append([]*genhighbay.SendTeamMessagePayload{}, sent...)
	mu.Unlock()
	require.Len(t, recorded, 2)
	ra, ok := recorded[0].Context.AsReviseAnswers()
	require.True(t, ok, "expected reconciled revise_answers context")
	assert.Equal(t, r2Rev, ra.RequestRevision, "reconciled send must target R2")
	assert.Equal(t, map[string]string{qKeepID: "multi"}, ra.Answers, "removed question qDrop must be pruned")

	assert.Equal(t, "builder", recorded[1].To)
	require.NotNil(t, recorded[1].Context)
	_, flowOK := recorded[1].Context.AsStoryFlow()
	assert.True(t, flowOK, "story_flow context must remain attached for builder")
}

// TestViewsPageStudioMultiChangeIsolationAndHistory proves per-change draft
// isolation across A -> B -> A, stale refreshOpenCard generation discard after
// adoption, handleAdopt diff subtraction, one-click #highbay-discard-all-drafts
// on a deferred link, and opaque history.state Back/Forward without prompt leaks.
func TestViewsPageStudioMultiChangeIsolationAndHistory(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceFiles(t, workspace, validWorkspaceStub("package design\n"))
	cp := newBuildControlPlane(t, Options{
		WorkspaceDir: workspace,
		Team:         &TeamBinding{Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("waiting")}},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond

	guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
	user := asCaller(pageOwner, CallerUser)
	opA := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	fastOpt := "fast"
	cardA, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID:         "chg-iso-a",
		StoryRefreshOpID: &opA,
		Prompt:           "First isolated change.",
		Analysis: &genhighbay.CP1Analysis{
			Summary: "First isolated change.",
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{
				{Question: "Mode?", Options: []string{"fast", "safe"}, DraftOption: &fastOpt},
			},
			Examples: []*genhighbay.CP1AnalysisExample{
				{Scenario: "Scenario A", InputSummary: "in-a", ExpectedOutcome: "out-a", VerificationKind: "deterministic"},
			},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)
	_, err = cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
		ChangeID:        cardA.ChangeID,
		RequestRevision: cardA.RequestRevision,
		Answers:         map[string]string{cardA.Ambiguities[0].ID: "fast"},
		Examples:        cardA.Examples,
	})
	require.NoError(t, err)

	opB := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	cardB, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID:         "chg-iso-b",
		StoryRefreshOpID: &opB,
		Prompt:           "Second isolated change.",
		Analysis: &genhighbay.CP1Analysis{
			Summary:     "Second isolated change.",
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{},
			Examples: []*genhighbay.CP1AnalysisExample{
				{Scenario: "Scenario B1", InputSummary: "in-b1", ExpectedOutcome: "out-b1", VerificationKind: "deterministic"},
				{Scenario: "Scenario B2", InputSummary: "in-b2", ExpectedOutcome: "out-b2", VerificationKind: "deterministic"},
			},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)
	confirmedB, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
		ChangeID:        cardB.ChangeID,
		RequestRevision: cardB.RequestRevision,
		Answers:         map[string]string{},
		Examples:        cardB.Examples,
	})
	require.NoError(t, err)

	writeWorkspaceFiles(t, filepath.Join(workspace, draftFolderName), map[string]string{"design/design.go": "package design // iso-b-v2\n"})
	opB2 := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	priorBEx1 := confirmedB.Examples[0].ID
	priorBEx2 := confirmedB.Examples[1].ID
	_, err = cp.ReviseChange(guide, &genhighbay.ReviseChangePayload{
		ChangeID:         cardB.ChangeID,
		RequestRevision:  confirmedB.RequestRevision,
		StoryRefreshOpID: &opB2,
		Prompt:           "Second isolated change proposed v2.",
		RevisionNote:     "Update expected outcome.",
		Analysis: &genhighbay.CP1RevisionAnalysis{
			Summary:     "Second isolated change proposed v2.",
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{},
			Examples: []*genhighbay.CP1RevisionAnalysisExample{
				{PriorID: &priorBEx1, Scenario: "Scenario B1", InputSummary: "in-b1", ExpectedOutcome: "out-b1-adopted", VerificationKind: "deterministic"},
				{PriorID: &priorBEx2, Scenario: "Scenario B2", InputSummary: "in-b2", ExpectedOutcome: "out-b2", VerificationKind: "deterministic"},
			},
			RemovedExampleIds:  []string{},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)

	ps := startPageServer(t, cp)
	ps.act("touch-b", func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		rec, ok, err := cp.cards.load("chg-iso-b")
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, cp.cards.save(rec))
	})

	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-open-change-chg-iso-a") && page.$("#highbay-open-change-chg-iso-b"), "both changes did not list");

// 1. Open A -> Agree, type draft on A.
page.$("#highbay-open-change-chg-iso-a").click();
await until(() => page.$("#highbay-checkpoint-view").dataset.changeId === "chg-iso-a", "chg-iso-a did not open");
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a Agree did not open");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "draft-only-on-A");

// 2. Open B (opens Agree directly because proposed_revision exists), type divergent scenario + matching outcome.
page.$("#highbay-nav-changes").click();
await until(() => page.text('li[data-change-id="chg-iso-a"]').includes("Unsaved"), "Changes list omitted Unsaved badge for chg-iso-a");
page.$("#highbay-open-change-chg-iso-b").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b" && page.$("#highbay-proposal"), "chg-iso-b proposal did not open");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="expected_outcome"]'), "out-b1-adopted");
page.type(page.$('textarea[data-example-id="check_02"][data-field="scenario"]'), "Scenario B2 local edit");

// Hold a background GET /changes/chg-iso-b BEFORE adopting, then adopt B and release the stale GET after!
await control(origin, "hold", { key: "GET /changes/chg-iso-b" });
await control(origin, "act", { key: "touch-b" });
await until(() => page.sent("GET", "/changes/chg-iso-b").some((r) => r.status === undefined), "background refresh of chg-iso-b was not held");
page.$("#highbay-proposal-adopt").click();
await until(() => !page.$("#highbay-proposal"), "chg-iso-b proposal did not adopt");
// Release the pre-adopt GET /changes/chg-iso-b -> cardRefreshSeq must discard it!
await control(origin, "release", { key: "GET /changes/chg-iso-b" });
await sleep(60);
expect(!page.$("#highbay-proposal"), "stale pre-adopt get_change resurrected the already-adopted proposal");
// Verify diff subtraction: check_01 matched out-b1-adopted so only check_02 remains in preservedDraft!
expect(page.$("#highbay-preserved-draft") && page.text("#highbay-preserved-draft").includes("Scenario B2 local edit") && !page.text("#highbay-preserved-draft").includes("out-b1-adopted"), "handleAdopt did not subtract examples already satisfied by the adopted revision");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_02"][data-field="scenario"]'), "Scenario B2 post-adopt dirty");

// 3. Switch back to A -> A's draft is completely isolated and intact.
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-a").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a did not reopen");
expect(page.$('textarea[data-field="expected_outcome"]').value === "draft-only-on-A", "switching A -> B -> A lost or cross-contaminated A's draft");

// 4. Both A and B are dirty -> deferred #link= offers one-click #highbay-discard-all-drafts.
page.win.location.hash = "link=multi-dirty-link";
await until(() => page.$("#highbay-pending-link") && page.$("#highbay-discard-all-drafts"), "multi-change dirty deferred link did not offer #highbay-discard-all-drafts");
expect(page.text("#highbay-pending-link").includes("chg-iso-a") && page.text("#highbay-pending-link").includes("chg-iso-b") && page.text("#highbay-discard-all-drafts") === "Discard all unsaved drafts" && page.$("#highbay-open-pending-link").disabled, "discard-all notice did not name both dirty changes");
page.$("#highbay-discard-all-drafts").click();
await until(() => !page.$("#highbay-open-pending-link").disabled && !page.beforeUnload(), "Discard all unsaved drafts did not clear both dirty editors");

// 5. + New change -> Story -> history.back() preserves in-memory draft without leaking prompt text into history.state.
page.$("#highbay-new-change").click();
await until(() => page.$("#highbay-describe-input"), "+ New change did not open");
page.type(page.$("#highbay-describe-input"), "Sensitive prompt never written to history.state");
expect(!JSON.stringify(page.win.history.state).includes("Sensitive prompt") && page.win.location.pathname === "/views", "prompt text leaked into history.state or mutated URL");
page.$("#highbay-nav-story").click();
await until(() => page.state() === "story", "Story did not open");
page.win.history.back();
await until(() => page.$("#highbay-describe-input"), "history.back() did not return to + New change");
expect(page.$("#highbay-describe-input").value === "Sensitive prompt never written to history.state", "history.back() lost in-memory + New change draft");
page.close();
`)
}

// TestViewsPageStudioActionableFailureRoutesToVerifyAndBuilder verifies that a
// failed verification run started in the current in-force build marks Verify !,
// opens Verify by default, and wires #highbay-cp4-message-builder to Builder.
func TestViewsPageStudioActionableFailureRoutesToVerifyAndBuilder(t *testing.T) {
	reportDir := t.TempDir()
	tests := strings.Repeat("2", 40)
	cp := newBuildControlPlane(t, Options{
		BuildCacheDir:  t.TempDir(),
		RunsDir:        t.TempDir(),
		HighbayVersion: testHighbayVersion,
		WrittenTestRuns: &WrittenTestRuns{
			ReportDir: reportDir,
			Command:   []string{"/bin/sh", "-c", "exit 0", "sh"},
			Timeout:   time.Second,
		},
		Team: &TeamBinding{Owner: pageOwner, Service: &teamServiceFixture{snapshot: pageTeamSnapshot("waiting")}},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := saveBuildStoryCard(t, cp, "chg-verify-fail", StatusConfirmed, refundStory("Refund fails check"))
	user := asCaller(pageOwner, CallerUser)
	started, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	acceptTests(t, cp, card.ChangeID, tests)
	candidate := commitBuilderCandidateFixture(t, cp, card)

	at, err := time.Parse(time.RFC3339Nano, started.Build.StartedAt)
	require.NoError(t, err)
	failed := passedRun(card, "ffffffff-ffff-4fff-8fff-ffffffffffff", candidate, strings.Repeat("e", 64))
	failed.AcceptedAt = at.Add(2 * time.Second).Format(time.RFC3339Nano)
	failed.InputsCurrent = true
	failed.SuiteCommit = tests
	failed.TestSource = genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{
		Commit: tests, Notice: writtenTestsNotice,
	})
	failed.State = genhighbay.NewStateCompleted(&genhighbay.VerificationCompleted{
		SuiteID:     "orders_acceptance",
		Environment: "sim",
		Report: &genhighbay.BuilderVerificationReport{
			ReportDigest: strings.Repeat("e", 64),
			Passed:       false,
			Counts:       &genhighbay.VerificationCounts{Required: 2, Passed: 1, Failed: 1},
			Scenarios: []*genhighbay.VerificationScenario{
				{ID: "visible_refund", Outcome: "failed", Checks: []*genhighbay.VerificationCheck{{Name: "status_200", Passed: false}}},
				{ID: "holdout-1", Outcome: "passed"},
			},
		},
	})
	_, err = genhighbay.EncodeVerificationStatusResult(failed)
	require.NoError(t, err)

	verifications := newPageVerifications(cp)
	verifications.set(failed)
	verifications.list(card.ChangeID, failed.RunID)
	svc := &pageStudioFailureFixture{
		pageVerifications: verifications,
		changeID:          card.ChangeID,
		latest: &genhighbay.LatestVerificationItem{
			RunID:         failed.RunID,
			State:         "failed",
			InputsCurrent: true,
		},
	}

	ps := startPageServer(t, svc)
	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.state() === "verify" && page.$("#highbay-cp4-message-builder"), "confirmed change with current-build failed verification did not open Verify with #highbay-cp4-message-builder");
expect(page.text("#highbay-nav-cp4").includes("!"), "Verify stepper pill omitted actionable ! glyph");
page.$("#highbay-cp4-message-builder").click();
expect(page.doc.activeElement === page.$("#highbay-composer-input") && page.$("#highbay-composer-to").value === "builder", "Message Builder did not focus composer addressed to builder");
page.close();
`)
}
