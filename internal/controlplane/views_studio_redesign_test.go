// This file tests the Highbay Studio redesign contracts across DraftChangeWorkspace
// send confirmation and recovery (status 0, 503, invalid 201, 422, Guide working),
// ProposalReviewQueue non-displacement, composer scope isolation across linked
// clarifications, question-identity revise_answers reconciliation, incomingCard
// refine_change precedence, per-change draft and mutation isolation, answer-later
// refresh generation guards, multi-draft pending-link recovery, opaque history.state
// reload/popstate navigation, accessible stepper labels, passive-to-needs_you
// terminal promotion, and current-build failure routing beside an older mergeable pass.
package controlplane

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

type (
	// pageStudioFailureFixture attaches a LatestVerificationItem and StartedBuild
	// to ListChanges and GetBuild matching ControlPlane's on-disk contracts.
	pageStudioFailureFixture struct {
		*pageVerifications
		changeID string
		latest   *genhighbay.LatestVerificationItem
		build    *genhighbay.ChangeBuild
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

// GetBuild returns the fixture's started build when configured.
func (s *pageStudioFailureFixture) GetBuild(ctx context.Context, p *genhighbay.GetBuildPayload) (*genhighbay.ChangeBuild, error) {
	if s.build != nil && p.ChangeID == s.changeID {
		return s.build, nil
	}
	return s.pageVerifications.GetBuild(ctx, p)
}

// TestViewsPageStudioDraftChangeSendAndRecovery exercises Moment 0 and + New change:
// uncertain sends (status 0 network drop, 503 while Guide is working, and invalid 201 body)
// vs. 422 refusals, post-receipt Guide restart without re-posting, editable + New change
// while Guide works on another card, isolated linked Chorus reply with exact ReplyTo seq
// preserving Change A's unsent composer draft, and ProposalReviewQueue non-displacement.
func TestViewsPageStudioDraftChangeSendAndRecovery(t *testing.T) {
	var (
		mu           sync.Mutex
		sent         []*genhighbay.SendTeamMessagePayload
		questionSeq  int
		secondMsgSeq int
		bQuestionSeq int
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
	setGuideState := func(state string, hasTerm bool) {
		team.mu.Lock()
		defer team.mu.Unlock()
		roles := make([]*genhighbay.TeamRoleState, len(team.snapshot.Roles))
		for i, r := range team.snapshot.Roles {
			c := *r
			roles[i] = &c
		}
		roles[0].State = state
		roles[0].HasTerminal = hasTerm
		team.snapshot.Roles = roles
	}
	team.send = func(_ context.Context, p *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error) {
		mu.Lock()
		sent = append(sent, p)
		callIdx := len(sent)
		mu.Unlock()
		team.mu.Lock()
		defer team.mu.Unlock()
		seq := len(team.conversation.Messages) + 1
		if callIdx == 3 {
			secondMsgSeq = seq
		}
		msg := &genhighbay.TeamMessage{
			ID:         "#studio-team:" + strconv.Itoa(seq),
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
		setGuideState("ended", false)
	})
	ps.act("guide-working", func() {
		setGuideState("working", true)
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
		questionSeq = len(msgs) + 1
		msgs = append(msgs, &genhighbay.TeamMessage{
			ID:         "#studio-team:question-1",
			Target:     "#studio-team",
			Seq:        questionSeq,
			Sender:     "guide-1",
			SenderRole: "guide",
			Text:       "Should refunds support partial amounts?",
			ReplyTo:    &firstSeq,
			SentAtMs:   time.Now().UnixMilli(),
		})
		team.conversation.Messages = msgs
	})
	ps.act("guide-waiting-with-second-question", func() {
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
		parentSeq := secondMsgSeq
		msgs := append([]*genhighbay.TeamMessage{}, team.conversation.Messages...)
		bQuestionSeq = len(msgs) + 1
		msgs = append(msgs, &genhighbay.TeamMessage{
			ID:         "#studio-team:question-2",
			Target:     "#studio-team",
			Seq:        bQuestionSeq,
			Sender:     "guide-1",
			SenderRole: "guide",
			Text:       "Should dispute webhooks retry with exponential backoff?",
			ReplyTo:    &parentSeq,
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

// 1a. Dropped network connection (status === 0) -> uncertain banner, draft preserved, no sent receipt.
const realFetch = page.win.fetch.bind(page.win);
let dropNextMessagePost = true;
page.win.fetch = (inputUrl, init = {}) => {
  if (dropNextMessagePost && String(init.method || "GET").toUpperCase() === "POST" && String(inputUrl).includes("/team/messages")) {
    dropNextMessagePost = false;
    return Promise.reject(new page.win.TypeError("Failed to fetch"));
  }
  return realFetch(inputUrl, init);
};
page.$("#highbay-describe-submit").click();
await until(() => page.$("#highbay-describe-uncertain"), "status 0 network drop did not render #highbay-describe-uncertain");
expect(page.text("#highbay-describe-uncertain").includes("Send not confirmed") && page.$("#highbay-describe-input").value === "Build refund workflows" && !page.$("#highbay-draft-receipt"), "status 0 lost draft or rendered receipt");

// 1b. 503 team_unavailable while Guide is ALREADY working -> #highbay-describe-uncertain stays visible (never hidden by activelyWorking).
await control(origin, "act", { key: "guide-working" });
await control(origin, "reply", { key: "POST /team/messages", status: 503, body: { name: "team_unavailable", id: "u1", message: "Chorus timeout after publish.", temporary: true, timeout: true, fault: false } });
page.$("#highbay-describe-send-again").click();
await until(() => page.sent("POST", "/team/messages").length === 1 && page.$("#highbay-describe-uncertain") && page.$("#highbay-describe-review-conversation") && page.$("#highbay-describe-send-again"), "503 while Guide was working hid #highbay-describe-uncertain");
expect(page.$("#highbay-describe-input").value === "Build refund workflows" && !page.$("#highbay-draft-receipt"), "503 lost draft or rendered receipt");

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

// 6. Linked Chorus clarification (reply_to === sentMessage.seq) -> focuses composer with ONLY reply_to.
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

// 7. 0 -> 1 proposal auto-opens Agree; seed an unsent refine_change draft on chg-first,
// set Guide to working, and verify + New change stays editable AND isolates chg-first's composer draft!
await control(origin, "act", { key: "propose-first" });
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-first", "first proposal did not auto-open Agree");
page.$("#highbay-agree-refine-with-guide").click();
page.type(page.$("#highbay-composer-input"), "Unsent refinement on chg-first");
expect(page.$("#highbay-composer-context-chip")?.textContent.includes("chg-first"), "chg-first refine_change chip missing");

await control(origin, "act", { key: "guide-working" });
page.$("#highbay-nav-team").click();
page.$("#highbay-team-refresh").click();
await until(() => page.text("#highbay-team-stage").includes("Working"), "Guide working state did not refresh");
page.$("#highbay-new-change").click();
await until(() => page.$("#highbay-new-change-stage") && page.$("#highbay-describe-form") && page.$("#highbay-describe-input") && page.$("#highbay-new-change-activity"), "+ New change hid #highbay-describe-form while Guide was working on chg-first");
expect(page.$("#highbay-composer-input").value === "" && !page.$("#highbay-composer-context-chip"), "+ New change leaked chg-first's composer draft into draft scope");

page.type(page.$("#highbay-describe-input"), "Add dispute webhooks");
page.$("#highbay-describe-submit").click();
await until(() => page.$("#highbay-draft-receipt"), "+ New change send did not record receipt");

// Linked clarification on Change B must carry ONLY B's reply_to (zero chg-first refine_change or text)!
await control(origin, "act", { key: "guide-waiting-with-second-question" });
page.$("#highbay-nav-team").click();
page.$("#highbay-team-refresh").click();
await until(() => page.text("#highbay-team-stage").includes("Waiting"), "Guide waiting state for second question did not refresh");
page.win.history.back();
page.$("#highbay-conversation-refresh").click();
await until(() => page.$("#highbay-draft-reply-conversation"), "second linked Guide reply did not render");
page.$("#highbay-draft-reply-conversation").click();
expect(page.$("#highbay-composer-input").value === "" && page.$("#highbay-composer-reply-chip") && !page.$("#highbay-composer-context-chip"), "second linked clarification inherited chg-first text or context");
page.type(page.$("#highbay-composer-input"), "Support retryable dispute webhooks.");
const beforeSecondReply = page.sent("POST", "/team/messages").length;
page.$("#highbay-composer-send").click();
await until(() => page.sent("POST", "/team/messages").length === beforeSecondReply + 1, "second clarification reply was not sent");

// Returning to chg-first restores chg-first's unsent composer text and refine_change attachment intact!
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-first").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-first", "chg-first did not reopen");
expect(page.$("#highbay-composer-input").value === "Unsent refinement on chg-first" && page.$("#highbay-composer-context-chip")?.textContent.includes("chg-first"), "returning to chg-first lost its unsent composer draft");

// ProposalReviewQueue preserves active + New change draft when chg-second arrives concurrently.
page.$("#highbay-new-change").click();
page.$("#highbay-draft-start-another").click();
await until(() => page.$("#highbay-describe-input"), "Start another request did not reopen form");
expect(page.$("#highbay-describe-input").value === "", "Start another request did not start empty");
page.type(page.$("#highbay-describe-input"), "Keep this third request safe while chg-second arrives");
await control(origin, "act", { key: "propose-second" });
await until(() => page.$("#highbay-proposals-ready") && page.$("#highbay-review-ready-chg-second"), "concurrent proposal did not appear in #highbay-proposals-ready");
expect(page.$("#highbay-describe-input").value === "Keep this third request safe while chg-second arrives", "concurrent proposal overwrote active + New change draft");
page.close();
`)

	mu.Lock()
	recorded := append([]*genhighbay.SendTeamMessagePayload{}, sent...)
	mu.Unlock()
	require.Len(t, recorded, 4)
	nc0, ok0 := recorded[0].Context.AsNewChange()
	require.True(t, ok0, "Moment 0 request attaches new_change context")
	assert.Equal(t, "separate_card", nc0.Scope)

	require.NotNil(t, recorded[1].ReplyTo, "first linked clarification reply carries reply_to")
	assert.Equal(t, questionSeq, *recorded[1].ReplyTo, "first linked reply must equal Guide question seq")
	assert.Empty(t, recorded[1].Context.Kind(), "first linked clarification reply must not re-attach new_change")

	nc2, ok2 := recorded[2].Context.AsNewChange()
	require.True(t, ok2, "+ New change request attaches new_change context")
	assert.Equal(t, "separate_card", nc2.Scope)

	require.NotNil(t, recorded[3].ReplyTo, "second linked clarification reply carries reply_to")
	assert.Equal(t, bQuestionSeq, *recorded[3].ReplyTo, "second linked reply must equal second Guide question seq")
	assert.Empty(t, recorded[3].Context.Kind(), "second linked reply must not inherit chg-first refine_change")
	assert.Equal(t, "Support retryable dispute webhooks.", recorded[3].Text)
}

// TestViewsPageStudioComposerGuardsAndRecipientDetachment verifies question-identity
// revise_answers reconciliation when an earlier question is dropped, ConversationRail
// message Reply boilerplate detachment, incomingCard precedence over older proposals on
// stale refine_change ("Update target & send"), failed agree-aid composer seeding, and
// recipient detachment preserving story_flow for non-guide roles.
func TestViewsPageStudioComposerGuardsAndRecipientDetachment(t *testing.T) {
	var (
		mu   sync.Mutex
		sent []*genhighbay.SendTeamMessagePayload
	)
	team := &teamServiceFixture{
		snapshot: pageTeamSnapshot("waiting"),
		conversation: genhighbay.TeamConversation{
			Team:         "studio-team",
			Channel:      "#studio-team",
			ChorusStatus: "connected",
			Messages: []*genhighbay.TeamMessage{
				{
					ID:         "#studio-team:1",
					Target:     "#studio-team",
					Seq:        1,
					Sender:     "builder-1",
					SenderRole: "builder",
					Text:       "Ready to inspect Story flow.",
					SentAtMs:   time.Now().UnixMilli(),
				},
			},
		},
	}
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
	user := asCaller(pageOwner, CallerUser)
	op := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
	usdOpt := "usd"
	noOpt := "no"
	onOpt := "on"
	// Place qDrop FIRST (index 0 -> ambiguity-01) and qKeep SECOND (index 1 -> ambiguity-02),
	// plus an unchanged third question (index 2 -> ambiguity-03), so dropping qDrop in R2
	// shifts "Limit currency?" from ambiguity-02 to ambiguity-01!
	r1, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
		ChangeID:         "chg-guard",
		StoryRefreshOpID: &op,
		Prompt:           "Configure refund limits.",
		Analysis: &genhighbay.CP1Analysis{
			Summary: "Configure refund limits.",
			Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{
				{Question: "Legacy CSV?", Options: []string{"no", "yes"}, DraftOption: &noOpt},
				{Question: "Limit currency?", Options: []string{"usd", "multi"}, DraftOption: &usdOpt},
				{Question: "Audit log?", Options: []string{"on", "off"}, DraftOption: &onOpt},
			},
			Examples: []*genhighbay.CP1AnalysisExample{
				{Scenario: "USD refund", InputSummary: "$25", ExpectedOutcome: "Allowed", VerificationKind: "deterministic"},
			},
			PreservedBehaviors: []string{},
		},
	})
	require.NoError(t, err)
	require.Len(t, r1.Ambiguities, 3)
	qDropID := r1.Ambiguities[0].ID
	qKeepID := r1.Ambiguities[1].ID

	var (
		r2Rev    string
		r2KeepID string
		r4Rev    string
	)
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
					{Question: "Audit log?", Options: []string{"on", "off"}, DraftOption: &onOpt},
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
		r2KeepID = updated.Ambiguities[0].ID
	})
	ps.act("seed-confirmed-r2-with-proposal-r3", func() {
		current, err := cp.GetChange(user, &genhighbay.GetChangePayload{ChangeID: "chg-guard"})
		require.NoError(t, err)
		confirmedR2, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
			ChangeID:        "chg-guard",
			RequestRevision: current.RequestRevision,
			Answers:         map[string]string{current.Ambiguities[0].ID: "usd", current.Ambiguities[1].ID: "on"},
			Examples:        current.Examples,
		})
		require.NoError(t, err)
		writeWorkspaceFiles(t, filepath.Join(workspace, draftFolderName), map[string]string{"design/design.go": "package design // v3\n"})
		op3 := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
		priorEx := confirmedR2.Examples[0].ID
		_, err = cp.ReviseChange(guide, &genhighbay.ReviseChangePayload{
			ChangeID:         "chg-guard",
			RequestRevision:  confirmedR2.RequestRevision,
			StoryRefreshOpID: &op3,
			Prompt:           "Configure refund limits v3.",
			RevisionNote:     "Propose R3.",
			Analysis: &genhighbay.CP1RevisionAnalysis{
				Summary:     "Configure refund limits v3.",
				Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{},
				Examples: []*genhighbay.CP1RevisionAnalysisExample{
					{PriorID: &priorEx, Scenario: "USD refund", InputSummary: "$25", ExpectedOutcome: "Allowed v3", VerificationKind: "deterministic"},
				},
				RemovedExampleIds:  []string{},
				PreservedBehaviors: []string{},
			},
		})
		require.NoError(t, err)
	})
	ps.act("advance-to-incoming-r4", func() {
		current, err := cp.GetChange(user, &genhighbay.GetChangePayload{ChangeID: "chg-guard"})
		require.NoError(t, err)
		require.NotNil(t, current.ProposedRevision)
		adoptedR3, err := cp.ConfirmIntent(user, &genhighbay.ConfirmIntentPayload{
			ChangeID:        "chg-guard",
			RequestRevision: current.ProposedRevision.RequestRevision,
			Answers:         map[string]string{},
			Examples:        current.ProposedRevision.Examples,
		})
		require.NoError(t, err)
		writeWorkspaceFiles(t, filepath.Join(workspace, draftFolderName), map[string]string{"design/design.go": "package design // v4\n"})
		op4 := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
		priorEx := adoptedR3.Examples[0].ID
		withR4, err := cp.ReviseChange(guide, &genhighbay.ReviseChangePayload{
			ChangeID:         "chg-guard",
			RequestRevision:  adoptedR3.RequestRevision,
			StoryRefreshOpID: &op4,
			Prompt:           "Configure refund limits v4.",
			RevisionNote:     "Propose R4.",
			Analysis: &genhighbay.CP1RevisionAnalysis{
				Summary:     "Configure refund limits v4.",
				Ambiguities: []*genhighbay.CP1AnalysisAmbiguity{},
				Examples: []*genhighbay.CP1RevisionAnalysisExample{
					{PriorID: &priorEx, Scenario: "USD refund", InputSummary: "$25", ExpectedOutcome: "Allowed v4", VerificationKind: "deterministic"},
				},
				RemovedExampleIds:  []string{},
				PreservedBehaviors: []string{},
			},
		})
		require.NoError(t, err)
		r4Rev = withR4.ProposedRevision.RequestRevision
	})

	runPageScript(t, ps, map[string]any{"qKeep": qKeepID, "qDrop": qDropID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-guard", "chg-guard did not open on Agree");

// 1a. Select [qDrop=yes, qKeep=multi] while leaving qUnchanged at draft_option=on -> RevisionAid attaches ONLY 2 changed answers.
page.$('button[data-ambiguity-id="' + input.qDrop + '"][data-option="yes"]').click();
page.$('button[data-ambiguity-id="' + input.qKeep + '"][data-option="multi"]').click();
await until(() => page.$("#highbay-confirm-revision-needed-send"), "RevisionAid did not appear");

// 1b. Failed direct agree-aid send seeds composerDraft with the unsent request rather than opening an empty composer.
await control(origin, "reply", { key: "POST /team/messages", status: 422, body: { name: "unprocessable", id: "u-aid", message: "Simulated agree refusal.", temporary: false, timeout: false, fault: false } });
page.$("#highbay-confirm-revision-needed-send").click();
await until(() => page.$("#highbay-composer-error"), "failed agree-aid send did not surface #highbay-composer-error");
expect(page.$("#highbay-composer-input").value.includes("Please revise change chg-guard") && page.$("#highbay-composer-context-chip")?.textContent.includes("2 answers"), "failed agree-aid send did not seed composerDraft with unsent request");

// 1c. Clicking Reply on a builder message in ConversationRail strips Guide-seeded boilerplate and detaches revise_answers.
await until(() => page.$('button[aria-label^="Reply to message #1"]'), "builder message Reply button missing");
page.$('button[aria-label^="Reply to message #1"]').click();
expect(page.$("#highbay-composer-to").value === "builder" && page.$("#highbay-composer-input").value === "" && page.$("#highbay-composer-detached-notice") && !page.$("#highbay-composer-context-chip") && page.$("#highbay-composer-reply-chip"), "message Reply to builder did not strip Guide boilerplate and detach revise_answers");

// 1d. Re-open Edit message first, advance card to R2 (which drops qDrop at index 0, shifting Limit currency to index 0), and send reconciled!
page.$("#highbay-confirm-revision-needed-compose").click();
expect(page.$("#highbay-composer-context-chip")?.textContent.includes("2 answers"), "revise_answers chip did not re-attach");
await control(origin, "act", { key: "advance-revision" });
await until(() => page.$("#highbay-composer-stale-context") && page.$("#highbay-composer-send-reconciled"), "stale revise_answers guard did not reconcile R2 answers after revision advance");
expect(page.$("#highbay-composer-send").disabled && page.text("#highbay-composer-review-reconciled").includes("1 compatible answer was kept"), "stale revise_answers did not reconcile 1 kept answer by question identity");
page.$("#highbay-composer-send-reconciled").click();
await until(() => page.sent("POST", "/team/messages").some((r) => r.status === 201), "Send reconciled answers did not complete");
await until(() => page.$("#highbay-replace-incoming-card"), "incoming R2 card banner did not appear");
page.$("#highbay-replace-incoming-card").click();
if (page.$("#highbay-discard-draft")) page.$("#highbay-discard-draft").click();

// 2. Confirmed R2 + proposal R3 + dirty local example -> incoming R3+proposal R4 arrives -> stale refine_change targets R4 via #highbay-composer-update-send!
await control(origin, "act", { key: "seed-confirmed-r2-with-proposal-r3" });
await until(() => page.$("#highbay-proposal-refine-with-guide"), "proposal R3 did not appear");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "Dirty local edit on R2");
page.$("#highbay-proposal-refine-with-guide").click();
page.type(page.$("#highbay-composer-input"), "Add retry jitter to latest revision.");
expect(page.$("#highbay-composer-context-chip")?.textContent.includes("chg-guard"), "proposal refine_change chip missing");

await control(origin, "act", { key: "advance-to-incoming-r4" });
await until(() => page.$("#highbay-incoming-change-card") && page.$("#highbay-composer-stale-context") && page.$("#highbay-composer-update-send"), "incoming card R3+proposal R4 did not trigger stale refine_change guard over older cached proposal R3");
expect(page.$("#highbay-composer-send").disabled, "stale refine_change did not hold primary Send");
const beforeUpdateSend = page.sent("POST", "/team/messages").length;
page.$("#highbay-composer-update-send").click();
await until(() => page.sent("POST", "/team/messages").length === beforeUpdateSend + 1 && page.sent("POST", "/team/messages").at(-1)?.status === 201, "Update target & send did not complete");

// 3. Seed refine_change, then switch To: from guide to builder -> detaches refine_change while keeping user text.
page.$("#highbay-proposal-refine-with-guide").click();
page.type(page.$("#highbay-composer-input"), "Add audit logging to refund limits.");
const toSelect = page.$("#highbay-composer-to");
toSelect.value = "builder";
toSelect.dispatchEvent(new page.win.Event("change", { bubbles: true }));
expect(page.$("#highbay-composer-detached-notice") && !page.$("#highbay-composer-context-chip") && page.$("#highbay-composer-input").value === "Add audit logging to refund limits.", "switching recipient to builder did not detach refine_change cleanly");

// 4. Open Story, attach story_flow, switch To: to builder -> story_flow remains valid & attached!
page.$("#highbay-nav-story").click();
await until(() => page.$('button[aria-label^="Ask Guide about flow "]'), "Story flow Ask Guide button did not render");
page.$('button[aria-label^="Ask Guide about flow "]').click();
const storyToSelect = page.$("#highbay-composer-to");
storyToSelect.value = "builder";
storyToSelect.dispatchEvent(new page.win.Event("input", { bubbles: true }));
storyToSelect.dispatchEvent(new page.win.Event("change", { bubbles: true }));
expect(storyToSelect.value === "builder" && page.$("#highbay-composer-context-chip")?.textContent.includes("To Builder") && page.$("#highbay-composer-context-chip")?.textContent.includes("Story:") && !page.$("#highbay-composer-detached-notice"), "switching recipient to builder wrongly detached story_flow");
page.type(page.$("#highbay-composer-input"), "Check this Story flow in builder.");
page.$("#highbay-composer-send").click();
await until(() => page.sent("POST", "/team/messages").at(-1)?.status === 201, "builder story_flow message did not complete");
page.close();
`)

	mu.Lock()
	recorded := append([]*genhighbay.SendTeamMessagePayload{}, sent...)
	mu.Unlock()
	require.Len(t, recorded, 3)

	ra, ok := recorded[0].Context.AsReviseAnswers()
	require.True(t, ok, "expected reconciled revise_answers context")
	assert.Equal(t, r2Rev, ra.RequestRevision, "reconciled send must target R2")
	assert.Equal(t, "chg-guard-ambiguity-01", r2KeepID, "dropping index 0 question shifted Limit currency to ambiguity-01")
	assert.Equal(t, map[string]string{r2KeepID: "multi"}, ra.Answers, "question-identity matching must bind Limit currency=multi to shifted ID without misbinding dropped Legacy CSV=yes")

	rc, rcOK := recorded[1].Context.AsRefineChange()
	require.True(t, rcOK, "expected updated refine_change context")
	assert.Equal(t, "chg-guard", rc.ChangeID)
	assert.Equal(t, r4Rev, rc.RequestRevision, "Update target & send must target incoming card's newest proposal R4")

	assert.Equal(t, "builder", recorded[2].To)
	require.NotNil(t, recorded[2].Context)
	_, flowOK := recorded[2].Context.AsStoryFlow()
	assert.True(t, flowOK, "story_flow context must remain attached for builder")
}

// TestViewsPageStudioMultiChangeIsolationAndHistory proves per-change draft and
// late-mutation isolation across A -> B -> A, answer-later stale refreshOpenCard
// generation discard after adoption, handleAdopt diff subtraction, skip-link focus
// without URL/history mutation, 2-card Story reload + uncached popstate Forward
// preservation, and one-click #highbay-discard-all-drafts on a deferred link.
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

// 1. Open A -> Agree, type draft on A; hold POST /changes/confirm (409 failure), switch to B, release -> error stays on A, B undisturbed!
page.$("#highbay-open-change-chg-iso-a").click();
await until(() => page.$("#highbay-checkpoint-view").dataset.changeId === "chg-iso-a" && page.state() === "build", "chg-iso-a did not open on Build");
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a Agree did not open");
expect(page.text("#highbay-nav-cp1").includes("completed"), "confirmed Agree step omitted accessible screen-reader status");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "draft-only-on-A");
expect(page.text("#highbay-nav-cp1").includes("unsaved edits"), "dirty Agree step omitted unsaved accessible status");

await control(origin, "reply", { key: "POST /changes/confirm", status: 409, body: { name: "conflict", id: "c-a", message: "Simulated conflict on A.", temporary: false, timeout: false, fault: false } });
await control(origin, "hold", { key: "POST /changes/confirm" });
page.$("#highbay-confirm-submit").click();
await until(() => page.sent("POST", "/changes/confirm").some((r) => r.status === undefined), "chg-iso-a confirm was not held");

// Navigate to B while A's confirm is in flight.
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-b").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b" && page.$("#highbay-proposal"), "chg-iso-b proposal did not open");
await control(origin, "release", { key: "POST /changes/confirm" });
await until(() => page.sent("POST", "/changes/confirm")[0]?.status === 409, "chg-iso-a 409 confirm did not settle");
expect(page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b" && page.win.history.state.changeId === "chg-iso-b" && !page.$("#highbay-confirm-error"), "late failure on A wrote #highbay-confirm-error onto B or hijacked view");

// 2. On B, use answer-later to compute a pre-adopt R1+proposal GET /changes/chg-iso-b BEFORE adopting, then release after adopt!
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_01"][data-field="expected_outcome"]'), "out-b1-adopted");
page.type(page.$('textarea[data-example-id="check_02"][data-field="scenario"]'), "Scenario B2 local edit");
await control(origin, "answer-later", { key: "GET /changes/chg-iso-b" });
await control(origin, "act", { key: "touch-b" });
await until(() => page.sent("GET", "/changes/chg-iso-b").some((r) => r.status === undefined), "pre-adopt answer-later GET /changes/chg-iso-b did not start");
page.$("#highbay-proposal-adopt").click();
await until(() => !page.$("#highbay-proposal"), "chg-iso-b proposal did not adopt");
await control(origin, "release", { key: "GET /changes/chg-iso-b" });
await until(() => page.sent("GET", "/changes/chg-iso-b").every((r) => r.status === 200), "delayed pre-adopt GET /changes/chg-iso-b did not finish");
expect(!page.$("#highbay-incoming-change-card") && !page.$("#highbay-proposal"), "stale pre-adopt get_change was applied as incomingCard or resurrected #highbay-proposal");
expect(page.$("#highbay-preserved-draft") && page.text("#highbay-preserved-draft").includes("Scenario B2 local edit") && !page.text("#highbay-preserved-draft").includes("out-b1-adopted"), "handleAdopt did not subtract examples already satisfied by the adopted revision");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-example-id="check_02"][data-field="scenario"]'), "Scenario B2 post-adopt dirty");

// 3. Reopen A -> A's draft AND A's earlier 409 #highbay-confirm-error are intact; now hold a 200 confirm on A, switch to B, release -> B stays open!
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-a").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a did not reopen on Agree");
expect(page.$('textarea[data-field="expected_outcome"]').value === "draft-only-on-A" && page.$("#highbay-confirm-error") && page.text("#highbay-confirm-error").includes("Simulated conflict on A."), "A lost its draft or 409 formError");

await control(origin, "hold", { key: "POST /changes/confirm" });
page.$("#highbay-confirm-submit").click();
await until(() => page.sent("POST", "/changes/confirm").some((r) => r.status === undefined), "second confirm on A was not held");
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-b").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b", "chg-iso-b did not reopen while A save was pending");
await control(origin, "release", { key: "POST /changes/confirm" });
await until(() => page.sent("POST", "/changes/confirm").at(-1)?.status === 200, "second confirm on A did not complete");
expect(page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-b" && page.win.history.state.changeId === "chg-iso-b", "late 200 confirm on A hijacked visible sibling change B");

// Re-dirty A so both A and B are dirty for #highbay-discard-all-drafts.
page.$("#highbay-nav-changes").click();
page.$("#highbay-open-change-chg-iso-a").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "chg-iso-a Agree did not reopen");
page.$("#highbay-refine-agreement").open = true;
page.type(page.$('textarea[data-field="expected_outcome"]'), "draft-again-on-A");

// 4. Both A and B are dirty -> deferred #link= offers one-click #highbay-discard-all-drafts.
page.win.location.hash = "link=multi-dirty-link";
await until(() => page.$("#highbay-pending-link") && page.$("#highbay-discard-all-drafts"), "multi-change dirty deferred link did not offer #highbay-discard-all-drafts");
expect(page.text("#highbay-pending-link").includes("chg-iso-a") && page.text("#highbay-pending-link").includes("chg-iso-b") && page.text("#highbay-discard-all-drafts") === "Discard all unsaved drafts" && page.$("#highbay-open-pending-link").disabled, "discard-all notice did not name both dirty changes");
page.$("#highbay-discard-all-drafts").click();
await until(() => !page.$("#highbay-open-pending-link").disabled && !page.beforeUnload(), "Discard all unsaved drafts did not clear both dirty editors");

// 5. Skip link moves focus programmatically without mutating location.hash or window.history.state.
page.$("#highbay-new-change").click();
await until(() => page.$("#highbay-describe-input"), "+ New change did not open");
page.type(page.$("#highbay-describe-input"), "Sensitive prompt never written to history.state");
const stateBeforeSkip = JSON.stringify(page.win.history.state);
expect(!stateBeforeSkip.includes("Sensitive prompt") && page.win.location.pathname === "/views" && page.win.location.hash === "", "prompt text leaked into history.state or mutated URL");
page.$("#highbay-skip-link").click();
expect(page.win.location.hash === "" && JSON.stringify(page.win.history.state) === stateBeforeSkip, "skip link mutated location.hash or pushed null history.state");

// 6. Cold reload on 2-card Story restores activeChangeId + stepper, and history.back() to uncached card A preserves history.forward()!
const reloaded = await openPage(origin, {
  storage: page.win.localStorage,
  historyEntries: [
    { view: "change", changeId: "chg-iso-a", stage: "agree" },
    { view: "story", changeId: "chg-iso-b" },
  ],
});
await until(() => reloaded.state() === "story" && reloaded.$("#highbay-nav-cp1") && reloaded.$("#highbay-result-view")?.dataset.changeId === "chg-iso-b", "2-card Story reload did not restore activeChangeId chg-iso-b and 5-step stepper");
reloaded.win.history.back();
await until(() => reloaded.state() === "agree" && reloaded.$("#highbay-cp1-editor")?.dataset.changeId === "chg-iso-a", "history.back() did not load uncached chg-iso-a on Agree");
reloaded.win.history.forward();
await until(() => reloaded.state() === "story" && reloaded.$("#highbay-result-view")?.dataset.changeId === "chg-iso-b", "async uncached openChange called pushState and wiped Forward history");

reloaded.close();
page.close();
`)
}

// TestViewsPageStudioActionableFailureRoutesToVerifyAndBuilder verifies that an
// in-force build with an older eligible passed run AND a newer failed run opens
// Verify (!, "needs attention") ahead of Approve, keeps Approve neutral until a
// fresh review is recorded, preserves approval/merge and cold-reload runId on the
// older passed run even when a newer pass arrives, preserves 1-card Architecture
// reload without auto-open clobbering, and promotes Type in terminal when a
// passively watched Builder enters needs_you.
func TestViewsPageStudioActionableFailureRoutesToVerifyAndBuilder(t *testing.T) {
	reportDir := t.TempDir()
	tests := strings.Repeat("2", 40)
	team := &teamServiceFixture{snapshot: pageTeamSnapshot("working")}
	cp := newBuildControlPlane(t, Options{
		BuildCacheDir:  t.TempDir(),
		RunsDir:        t.TempDir(),
		HighbayVersion: testHighbayVersion,
		WrittenTestRuns: &WrittenTestRuns{
			ReportDir: reportDir,
			Command:   []string{"/bin/sh", "-c", "exit 0", "sh"},
			Timeout:   time.Second,
		},
		Team: &TeamBinding{Owner: pageOwner, Service: team},
	})
	cp.changesWait, cp.changesPoll = 400*time.Millisecond, 20*time.Millisecond
	card := saveBuildStoryCard(t, cp, "chg-verify-fail", StatusConfirmed, refundStory("Refund fails check"))
	user := asCaller(pageOwner, CallerUser)
	started, err := cp.StartBuild(user, &genhighbay.StartBuildPayload{
		ChangeID: card.ChangeID, RequestRevision: card.RequestRevision,
	})
	require.NoError(t, err)
	acceptTests(t, cp, card.ChangeID, tests)
	candidateOld := commitBuilderCandidateFixture(t, cp, card)

	at, err := time.Parse(time.RFC3339Nano, started.Build.StartedAt)
	require.NoError(t, err)

	reportOld := passingReport(t, card.RequestRevision, candidateOld, reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: tests, Environment: "sim",
		Artifacts: map[string]string{"orders": strings.Repeat("a", 64)},
	})
	writeReportFile(t, reportDir, reportOld)
	olderPass := passedRun(card, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", candidateOld, reportOld.Digest)
	olderPass.AcceptedAt = at.Add(time.Second).Format(time.RFC3339Nano)
	olderPass.InputsCurrent = true
	olderPass.SuiteCommit = tests
	olderPass.TestSource = genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{
		Commit: tests, Notice: writtenTestsNotice,
	})

	failed := passedRun(card, "ffffffff-ffff-4fff-8fff-ffffffffffff", candidateOld, strings.Repeat("e", 64))
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

	reportNew := passingReport(t, card.RequestRevision, candidateOld, reportSpec{
		SuiteID: "orders_acceptance", SuiteCommit: tests, Environment: "sim",
		Artifacts: map[string]string{"orders": strings.Repeat("b", 64)},
	})
	writeReportFile(t, reportDir, reportNew)
	newerPass := passedRun(card, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", candidateOld, reportNew.Digest)
	newerPass.AcceptedAt = at.Add(3 * time.Second).Format(time.RFC3339Nano)
	newerPass.InputsCurrent = true
	newerPass.SuiteCommit = tests
	newerPass.TestSource = genhighbay.NewTestSourceWrittenTests(&genhighbay.AgentWrittenTests{
		Commit: tests, Notice: writtenTestsNotice,
	})

	afterTests, err := cp.GetBuild(user, &genhighbay.GetBuildPayload{ChangeID: card.ChangeID})
	require.NoError(t, err)
	acceptedBuild, ok := afterTests.Build.AsStarted()
	require.True(t, ok)
	progress := *acceptedBuild
	progress.Taking = genhighbay.NewBuildTakingPassed(&genhighbay.BuildPassed{
		RunID:           olderPass.RunID,
		CandidateCommit: candidateOld,
		CandidateBranch: "highbay/build-1",
		Tests: genhighbay.NewBuildPassedTestSourceWrittenTests(&genhighbay.BuildPassedTests{
			Commit: tests,
			Branch: "highbay/tests-1",
		}),
		BaseInBranch: true,
	})

	verifications := newPageVerifications(cp)
	verifications.set(olderPass, failed)
	verifications.list(card.ChangeID, failed.RunID, olderPass.RunID)
	svc := &pageStudioFailureFixture{
		pageVerifications: verifications,
		changeID:          card.ChangeID,
		latest: &genhighbay.LatestVerificationItem{
			RunID:         failed.RunID,
			State:         "failed",
			InputsCurrent: true,
		},
		build: &genhighbay.ChangeBuild{
			ChangeID: card.ChangeID,
			Build:    genhighbay.NewBuildStarted(&progress),
		},
	}

	ps := startPageServer(t, svc)
	ps.act("add-newer-pass", func() {
		verifications.set(newerPass)
		verifications.list(card.ChangeID, newerPass.RunID, failed.RunID, olderPass.RunID)
		svc.latest = &genhighbay.LatestVerificationItem{
			RunID:         newerPass.RunID,
			State:         "passed",
			InputsCurrent: true,
		}
	})
	ps.act("builder-needs-you", func() {
		team.mu.Lock()
		defer team.mu.Unlock()
		roles := make([]*genhighbay.TeamRoleState, len(team.snapshot.Roles))
		for i, r := range team.snapshot.Roles {
			c := *r
			roles[i] = &c
		}
		for _, r := range roles {
			if r.Role == "builder" {
				r.State = "needs_you"
				r.Attention = "question"
			}
		}
		team.snapshot.Roles = roles
	})

	runPageScript(t, ps, map[string]any{"oldRun": olderPass.RunID, "failRun": failed.RunID, "newRun": newerPass.RunID}, `
const page = await openPage(origin, { link: input.link });

// 1. Even though build.taking.state === "passed" from olderPass, newer current-build failed run opens Verify (!, needs attention) with neutral Approve.
await until(() => page.state() === "verify" && page.$("#highbay-result-view")?.dataset.runId === input.failRun && page.$("#highbay-cp4-message-builder"), "current-build failed run beside older pass did not open Verify with #highbay-cp4-message-builder");
expect(page.text("#highbay-nav-cp4").includes("!") && page.text("#highbay-nav-cp4").includes("needs attention"), "Verify stepper pill omitted ! glyph or screen-reader status");
expect(page.$("#highbay-nav-cp5").dataset.stepStatus === "neutral" && !page.text("#highbay-nav-cp5").includes("✓"), "Approve showed ✓ before any recorded review");
page.$("#highbay-cp4-message-builder").click();
expect(page.doc.activeElement === page.$("#highbay-composer-input") && page.$("#highbay-composer-to").value === "builder", "Message Builder did not focus composer addressed to builder");

// 2. Select olderPass, open Approve, approve olderPass, and verify olderPass keeps merge action even after newerPass arrives AND across cold reload!
await until(() => page.$('button[data-run-id="' + input.oldRun + '"]'), "olderPass was not listed in Verify history");
page.$('button[data-run-id="' + input.oldRun + '"]').click();
page.$("#highbay-nav-cp5").click();
await until(() => page.state() === "approve" && page.$("#highbay-result-view")?.dataset.runId === input.oldRun && page.$("#highbay-cp5-approve") && !page.$("#highbay-cp5-approve").disabled, "olderPass did not become eligible for approval on Approve");
page.$("#highbay-cp5-approve").click();
await until(() => page.$("#highbay-cp5-approval") && page.$("#highbay-cp5-merge") && !page.$("#highbay-cp5-merge").disabled, "approving olderPass did not enable #highbay-cp5-merge");

await control(origin, "act", { key: "add-newer-pass" });
page.$("#highbay-load-verifications").click();
await until(() => page.$('button[data-run-id="' + input.newRun + '"]') && page.$("#highbay-cp5-merge") && !page.$("#highbay-cp5-merge").disabled, "arriving newerPass clobbered selected olderPass approval/merge");
expect(page.$("#highbay-result-view")?.dataset.runId === input.oldRun, "arriving newerPass changed selectedRunId away from olderPass");

// Cold reload on 1-card Approve with olderPass runId preserves exact olderPass (and cold reload on Architecture preserves arch)!
const reloadApprove = await openPage(origin, {
  storage: page.win.localStorage,
  historyState: { view: "change", changeId: "chg-verify-fail", stage: "approve", runId: input.oldRun },
});
await until(() => reloadApprove.state() === "approve" && reloadApprove.$("#highbay-result-view")?.dataset.runId === input.oldRun && reloadApprove.$("#highbay-cp5-merge") && !reloadApprove.$("#highbay-cp5-merge").disabled, "cold reload on Approve lost olderPass runId");
reloadApprove.close();

const reloadArch = await openPage(origin, {
  storage: page.win.localStorage,
  historyState: { view: "change", changeId: "chg-verify-fail", stage: "arch" },
});
await until(() => reloadArch.state() === "arch", "1-card cold reload on Architecture was clobbered by auto-open");
reloadArch.close();

// 3. Passively watching Builder terminal (observe=true) when Builder enters needs_you promotes #highbay-terminal-interactive as primary!
page.$("#highbay-rail-terminal-builder").click();
await until(() => page.$("#highbay-terminal-drawer")?.dataset.role === "builder" && page.$("#highbay-terminal-drawer")?.dataset.observe === "true", "Builder terminal did not open in passive observe=true mode");
await control(origin, "act", { key: "builder-needs-you" });
page.$("#highbay-nav-team").click();
page.$("#highbay-team-refresh").click();
await until(() => page.$("#highbay-team-attention-terminal-builder"), "Builder needs_you banner did not appear");
expect(page.$("#highbay-terminal-interactive")?.dataset.primaryAction === "true" && !page.text("#highbay-team-attention-terminal-builder").includes("terminal open") && page.$$('[data-primary-action="true"]').length === 1, "passive terminal entering needs_you did not highlight Type in terminal as sole primary action");

page.close();
`)
}
