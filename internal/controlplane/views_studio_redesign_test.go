// This file tests the Highbay Studio redesign contracts across DraftChangeWorkspace
// send confirmation and recovery (status 0, 503, invalid 201, 422, Guide working),
// ProposalReviewQueue independence and non-displacement, composer scope isolation
// across linked multi-turn clarifications, question-identity revise_answers
// reconciliation, incomingCard refine_change precedence, and recipient detachment.
package controlplane

import (
	"context"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
)

// TestViewsPageStudioDraftChangeSendAndRecovery exercises Moment 0 and + New change:
// uncertain sends (status 0 network drop, 503 while Guide is working, and invalid 201 body)
// vs. 422 refusals, post-receipt Guide restart without re-posting, editable + New change
// while Guide works on another card, isolated multi-turn linked Chorus replies with exact
// ReplyTo seq preserving Change A's unsent composer draft, survival of + New change
// receipts across earlier-card saves and independent proposal arrivals, and explicit
// Start another request reset.
func TestViewsPageStudioDraftChangeSendAndRecovery(t *testing.T) {
	var (
		mu           sync.Mutex
		sent         []*genhighbay.SendTeamMessagePayload
		questionSeq  int
		followupSeq  int
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
	ps.act("guide-waiting-with-followup-question", func() {
		team.mu.Lock()
		defer team.mu.Unlock()
		msgs := append([]*genhighbay.TeamMessage{}, team.conversation.Messages...)
		userReplySeq := msgs[len(msgs)-1].Seq
		followupSeq = len(msgs) + 1
		msgs = append(msgs, &genhighbay.TeamMessage{
			ID:         "#studio-team:question-followup",
			Target:     "#studio-team",
			Seq:        followupSeq,
			Sender:     "guide-1",
			SenderRole: "guide",
			Text:       "Should partial refunds also allow restocking fees?",
			ReplyTo:    &userReplySeq,
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
	ps.act("touch-first", func() {
		cp.mu.Lock()
		defer cp.mu.Unlock()
		rec, ok, err := cp.cards.load("chg-first")
		require.NoError(t, err)
		require.True(t, ok)
		require.NoError(t, cp.cards.save(rec))
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
	ps.act("propose-third", func() {
		guide := WithPrincipal(context.Background(), Principal{ID: pageOwner, CallerID: "guide", Kind: CallerGuide})
		wsDir, _, _ := cp.currentWorkspaceTarget()
		writeWorkspaceFiles(t, filepath.Join(wsDir, draftFolderName), map[string]string{"design/design.go": "package design // third\n"})
		op := seedStoryRefreshForTest(t, cp, pageOwner, storyRefreshStateSucceeded, pageStories(), "")
		_, err := cp.ProposeChange(guide, &genhighbay.ProposeChangePayload{
			ChangeID:         "chg-third",
			StoryRefreshOpID: &op,
			Prompt:           "Add chargeback exports.",
			Analysis:         refundAnalysis("Add chargeback exports."),
		})
		require.NoError(t, err)
	})

	runPageScript(t, ps, nil, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-describe-input") && page.$("#highbay-describe-submit"), "Moment 0 DraftChangeWorkspace did not open");
expect(page.$$('[data-primary-action="true"]').length === 1 && page.$("#highbay-describe-submit").dataset.primaryAction === "true", "Moment 0 must highlight #highbay-describe-submit as the sole primary action");

page.type(page.$("#highbay-describe-input"), "Build refund workflows");
await until(() => page.$("#highbay-describe-submit")?.disabled === false, "Moment 0 #highbay-describe-submit remained disabled");

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
// Answered Guide question must clear immediately while Guide is still waiting, then surface multi-hop follow-up!
await until(() => !page.$("#highbay-draft-reply-conversation"), "already-answered Guide question stayed active on DraftChangeWorkspace");
await control(origin, "act", { key: "guide-waiting-with-followup-question" });
page.$("#highbay-conversation-refresh").click();
await until(() => page.$("#highbay-draft-reply-conversation") && page.text("#highbay-draft-guide-reply").includes("restocking fees"), "multi-turn Guide follow-up question was missed");

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

// Re-saving earlier chg-first while Guide is waiting, then navigating Team -> + New change,
// MUST NOT wipe + New change's pending request receipt!
await control(origin, "act", { key: "touch-first" });
page.$("#highbay-nav-team").click();
await until(() => page.state() === "team", "Team view did not open");
page.$("#highbay-new-change").click();
await until(() => page.state() === "new" && page.$("#highbay-draft-receipt"), "re-saving chg-first wiped + New change receipt on Team -> + New change");
expect(page.text("#highbay-draft-receipt").includes("Add dispute webhooks"), "preserved receipt lost B's request text");

// Returning to chg-first restores chg-first's unsent composer text and refine_change attachment intact,
// including when navigating Changes -> header stepper #highbay-nav-cp1!
page.$("#highbay-nav-changes").click();
page.$("#highbay-nav-cp1").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-first", "chg-first did not reopen via header stepper");
expect(page.$("#highbay-composer-input").value === "Unsent refinement on chg-first" && page.$("#highbay-composer-context-chip")?.textContent.includes("chg-first"), "returning to chg-first via #highbay-nav-cp1 lost its unsent composer draft");

// Independent proposal chg-third arrives and Guide exits: opening chg-third via ProposalReviewQueue
// and returning to + New change preserves B's receipt and Restart Guide recovery until explicit Start another request!
await control(origin, "act", { key: "propose-third" });
await control(origin, "act", { key: "guide-ended" });
page.$("#highbay-nav-team").click();
page.$("#highbay-team-refresh").click();
await until(() => page.text("#highbay-team-stage").includes("Ended"), "Guide ended state did not refresh after chg-third");
page.$("#highbay-new-change").click();
await until(() => page.state() === "new" && page.$("#highbay-proposals-ready") && page.$("#highbay-review-ready-chg-third") && page.$("#highbay-draft-restart-guide"), "independent chg-third and Restart Guide did not render on + New change");
expect(page.text("#highbay-new-change-title") === "Guide is not running" && page.text("#highbay-draft-receipt").includes("Add dispute webhooks"), "+ New change stopped-Guide heading was untruthful or omitted B's receipt");
page.$("#highbay-review-ready-chg-third").click();
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-third", "Review in Agree did not open independent chg-third");
page.$("#highbay-new-change").click();
await until(() => page.state() === "new" && page.$("#highbay-draft-receipt") && page.$("#highbay-draft-restart-guide"), "opening independent chg-third wiped B's receipt or Restart Guide recovery");

// Explicit Start another request resets + New change to a fresh composer while keeping ProposalReviewQueue intact.
page.$("#highbay-draft-start-another").click();
await until(() => page.$("#highbay-describe-input") && page.$("#highbay-describe-input").value === "", "Start another request did not open a fresh composer");
page.type(page.$("#highbay-describe-input"), "Keep this next request safe while chg-second arrives");
await control(origin, "act", { key: "propose-second" });
await until(() => page.$("#highbay-proposals-ready") && page.$("#highbay-review-ready-chg-second"), "chg-second did not appear in #highbay-proposals-ready");
expect(page.$("#highbay-describe-input").value === "Keep this next request safe while chg-second arrives", "arriving proposal overwrote active + New change draft");
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
// stale refine_change ("Update target & send"), failed agree-aid composer seeding when
// the composer is empty, and recipient detachment preserving story_flow for non-guide roles.
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

	ps.act("chorus-unavailable", func() {
		team.mu.Lock()
		defer team.mu.Unlock()
		team.snapshot.ChorusStatus = "unavailable"
		team.conversation.ChorusStatus = "unavailable"
	})
	ps.act("chorus-connected", func() {
		team.mu.Lock()
		defer team.mu.Unlock()
		team.snapshot.ChorusStatus = "connected"
		team.conversation.ChorusStatus = "connected"
	})

	runPageScript(t, ps, map[string]any{"qKeep": qKeepID, "qDrop": qDropID}, `
const page = await openPage(origin, { link: input.link });
await until(() => page.$("#highbay-cp1-editor")?.dataset.changeId === "chg-guard", "chg-guard did not open on Agree");

// 1a. Select [qDrop=yes, qKeep=multi] while leaving qUnchanged at draft_option=on -> RevisionAid attaches ONLY 2 changed answers.
page.$('button[data-ambiguity-id="' + input.qDrop + '"][data-option="yes"]').click();
page.$('button[data-ambiguity-id="' + input.qKeep + '"][data-option="multi"]').click();
await until(() => page.$("#highbay-confirm-revision-needed-send")?.disabled === false, "RevisionAid send button did not become enabled");

// 1b. Failed direct agree-aid send on an empty composer seeds composerDraft with the unsent request.
await control(origin, "reply", { key: "POST /team/messages", status: 422, body: { name: "unprocessable", id: "u-aid", message: "Simulated agree refusal.", temporary: false, timeout: false, fault: false } });
page.$("#highbay-confirm-revision-needed-send").click();
await until(() => page.$("#highbay-composer-error"), "failed agree-aid send did not surface #highbay-composer-error");
expect(page.$("#highbay-composer-input").value.includes("Please revise change chg-guard") && page.$("#highbay-composer-context-chip")?.textContent.includes("2 answers"), "failed agree-aid send did not seed empty composerDraft with unsent request");

// 1c. Clicking Reply on a builder message in ConversationRail strips Guide-seeded boilerplate and detaches revise_answers.
await until(() => page.$('button[aria-label^="Reply to message #1"]'), "builder message Reply button missing");
page.$('button[aria-label^="Reply to message #1"]').click();
expect(page.$("#highbay-composer-to").value === "builder" && page.$("#highbay-composer-input").value === "" && page.$("#highbay-composer-detached-notice") && !page.$("#highbay-composer-context-chip") && page.$("#highbay-composer-reply-chip"), "message Reply to builder did not strip Guide boilerplate and detach revise_answers");

// 1d. Edit message first + Remove attachment strips Highbay-seeded boilerplate cleanly;
// then re-open Edit message first, advance card to R2, verify Chorus-unavailable disables stale send, and send reconciled!
page.$("#highbay-confirm-revision-needed-compose").click();
expect(page.$("#highbay-composer-context-chip")?.textContent.includes("2 answers"), "revise_answers chip did not re-attach");
page.$("#highbay-composer-context-chip button").click();
expect(!page.$("#highbay-composer-context-chip") && page.$("#highbay-composer-input").value === "", "Remove attachment left Highbay-seeded boilerplate behind");
page.$("#highbay-confirm-revision-needed-compose").click();
await control(origin, "act", { key: "advance-revision" });
await until(() => page.$("#highbay-composer-stale-context") && page.$("#highbay-composer-send-reconciled"), "stale revise_answers guard did not reconcile R2 answers after revision advance");
expect(page.$("#highbay-composer-send").disabled && page.text("#highbay-composer-review-reconciled").includes("1 compatible answer was kept"), "stale revise_answers did not reconcile 1 kept answer by question identity");
await control(origin, "act", { key: "chorus-unavailable" });
page.$("#highbay-conversation-refresh").click();
await until(() => page.$("#highbay-conversation-unavailable") && page.$("#highbay-composer-send-reconciled").disabled, "Chorus unavailable did not disable #highbay-composer-send-reconciled");
await control(origin, "act", { key: "chorus-connected" });
page.$("#highbay-conversation-refresh").click();
await until(() => !page.$("#highbay-conversation-unavailable") && !page.$("#highbay-composer-send-reconciled").disabled, "Chorus recovery did not re-enable #highbay-composer-send-reconciled");
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
