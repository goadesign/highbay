// These tests cover the conversation between the user and the team: what the
// page reads from Chorus and what a posted message becomes.
package project

import (
	"errors"
	"strings"
	"testing"

	genchorus "github.com/goadesign/chorus/gen/view"
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	genproject "github.com/goadesign/highbay/gen/project"
	"github.com/goadesign/highbay/internal/hc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// observeChorus gives the server's observation the stand-in's current swarm,
// as the supervisor's next read would.
func observeChorus(t *testing.T, server *Server, fakes *teamFakes) {
	t.Helper()
	view, err := fakes.chorus.View(t.Context(), &genchorus.ViewPayload{})
	require.NoError(t, err)
	server.team.observed.chorusRead(view, true)
}

// TestMessageText checks the text Chorus stores for each recipient and each
// kind of page context.
func TestMessageText(t *testing.T) {
	guide := teamRole{Role: "guide", Handle: "guide-1"}
	roles := []teamRole{guide, {Role: "builder", Handle: "builder-1"}}
	change, flow := "refunds", "over-limit"
	for _, test := range []struct {
		name    string
		payload genhighbay.SendTeamMessagePayload
		want    string
	}{
		{name: "the whole team", payload: genhighbay.SendTeamMessagePayload{To: "team", Text: "Status?"}, want: "Status?"},
		{name: "the guide", payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Build refunds"}, want: "@guide-1 Build refunds"},
		{name: "a worker", payload: genhighbay.SendTeamMessagePayload{To: "builder", Text: "Why two commits?"}, want: "@builder-1 Why two commits?"},
		{
			name: "a separate new Change Card",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Add audit exports.", Context: genhighbay.NewContextNewChange(
				&genhighbay.NewChangeContext{Scope: "separate_card"},
			)},
			want: "@guide-1 Add audit exports.\n\n**New Change Card requested:** Prepare a separate change in the draft and save it as a new Change Card with propose_change (do not call revise_change on an existing Change Card).",
		},
		{
			name: "free-form refinement of an existing Change Card",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Support CSV output too.", Context: genhighbay.NewContextRefineChange(
				&genhighbay.RefineChangeContext{ChangeID: "refunds", RequestRevision: "rev-3"},
			)},
			want: "@guide-1 Support CSV output too.\n\n**Revise this Change Card** (change `refunds`, latest request revision `rev-3`): Update the draft for this request and submit a revised Change Card with revise_change.",
		},
		{
			name: "answers to revise, in question order",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Revise the draft.", Context: genhighbay.NewContextReviseAnswers(
				&genhighbay.ReviseAnswersContext{ChangeID: "refunds", RequestRevision: "rev-2", Answers: map[string]string{"q2": "Manager approves", "q1": "Total charged"}},
			)},
			want: "@guide-1 Revise the draft.\n\n**Revise the draft to implement my answers** (change `refunds`, request revision `rev-2`):\n" +
				"- `q1`: Total charged\n- `q2`: Manager approves",
		},
		{
			name: "an architecture element of a change",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Why a queue?", Context: genhighbay.NewContextArchitectureElement(
				&genhighbay.ArchitectureElementContext{ChangeID: &change, ElementID: "refunds.queue", ElementName: "Refund queue"},
			)},
			want: "@guide-1 Why a queue?\n\n**About this architecture element:** Refund queue (`refunds.queue`), change `refunds`",
		},
		{
			name: "an architecture element without a change",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Why a queue?", Context: genhighbay.NewContextArchitectureElement(
				&genhighbay.ArchitectureElementContext{ElementID: "refunds.queue", ElementName: "Refund queue"},
			)},
			want: "@guide-1 Why a queue?\n\n**About this architecture element:** Refund queue (`refunds.queue`)",
		},
		{
			name: "a Story flow",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Split this.", Context: genhighbay.NewContextStoryFlow(
				&genhighbay.StoryFlowContext{ChangeID: &change, Story: "Refunds", FlowKey: &flow},
			)},
			want: "@guide-1 Split this.\n\n**About this Story:** Refunds, flow `over-limit`, change `refunds`",
		},
		{
			name: "a whole Story",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Split this.", Context: genhighbay.NewContextStoryFlow(
				&genhighbay.StoryFlowContext{Story: "Refunds"},
			)},
			want: "@guide-1 Split this.\n\n**About this Story:** Refunds",
		},
		{
			name: "an architecture element sent to builder",
			payload: genhighbay.SendTeamMessagePayload{To: "builder", Text: "Check retry policy here.", Context: genhighbay.NewContextArchitectureElement(
				&genhighbay.ArchitectureElementContext{ChangeID: &change, ElementID: "refunds.queue", ElementName: "Refund queue"},
			)},
			want: "@builder-1 Check retry policy here.\n\n**About this architecture element:** Refund queue (`refunds.queue`), change `refunds`",
		},
		{
			name: "a Story flow sent to team",
			payload: genhighbay.SendTeamMessagePayload{To: "team", Text: "Edge case note.", Context: genhighbay.NewContextStoryFlow(
				&genhighbay.StoryFlowContext{ChangeID: &change, Story: "Refunds", FlowKey: &flow},
			)},
			want: "Edge case note.\n\n**About this Story:** Refunds, flow `over-limit`, change `refunds`",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := messageText(&test.payload, guide, roles)
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}

	for _, test := range []struct {
		name    string
		payload genhighbay.SendTeamMessagePayload
		want    string
	}{
		{name: "a worker no build has registered", payload: genhighbay.SendTeamMessagePayload{To: "test_writer", Text: "Hello"}, want: "not part of this project's team yet"},
		{
			name: "new_change context sent to team",
			payload: genhighbay.SendTeamMessagePayload{To: "team", Text: "Add exports", Context: genhighbay.NewContextNewChange(
				&genhighbay.NewChangeContext{Scope: "separate_card"},
			)},
			want: "only be sent to guide",
		},
		{
			name: "refine_change context sent to builder",
			payload: genhighbay.SendTeamMessagePayload{To: "builder", Text: "Revise card", Context: genhighbay.NewContextRefineChange(
				&genhighbay.RefineChangeContext{ChangeID: "refunds", RequestRevision: "rev-1"},
			)},
			want: "only be sent to guide",
		},
		{
			name: "revise_answers context sent to builder",
			payload: genhighbay.SendTeamMessagePayload{To: "builder", Text: "Revise answers", Context: genhighbay.NewContextReviseAnswers(
				&genhighbay.ReviseAnswersContext{ChangeID: "refunds", RequestRevision: "rev-1", Answers: map[string]string{"q1": "A"}},
			)},
			want: "only be sent to guide",
		},
		{
			name:    "one character past what Chorus stores",
			payload: genhighbay.SendTeamMessagePayload{To: "team", Text: strings.Repeat("a", chorusTextLimit+1)},
			want:    "too long",
		},
		{
			name:    "one multi-byte character past what Chorus stores",
			payload: genhighbay.SendTeamMessagePayload{To: "team", Text: strings.Repeat("é", chorusTextLimit+1)},
			want:    "too long",
		},
		{
			name:    "a mention that takes the text past what Chorus stores",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: strings.Repeat("a", chorusTextLimit)},
			want:    "too long",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := messageText(&test.payload, guide, roles)
			assert.Equal(t, "unprocessable", errorName(err))
			assert.ErrorContains(t, err, test.want)
		})
	}

	// Chorus counts characters, not bytes: a message of multi-byte
	// characters is as long as one of single-byte characters.
	for _, character := range []string{"a", "é", "語"} {
		t.Run("exactly what Chorus stores, of "+character, func(t *testing.T) {
			got, err := messageText(&genhighbay.SendTeamMessagePayload{To: "team", Text: strings.Repeat(character, chorusTextLimit)}, guide, roles)
			require.NoError(t, err)
			assert.Equal(t, strings.Repeat(character, chorusTextLimit), got)
		})
	}
}

// TestSendMessage follows the user's first message from the page: nobody
// would answer it, so Highbay starts the guide, then posts the message under
// the user's own handle with a mention that wakes the guide.
func TestSendMessage(t *testing.T) {
	server, fakes := teamServer(t)
	record, _ := server.currentTeam()
	reply := 4
	message, err := server.SendMessage(userContext(server.state), &genhighbay.SendTeamMessagePayload{
		To: "guide", Text: "Build a refund assistant", ReplyTo: &reply,
		GuideChoice: &genhighbay.RoleAgentChoice{Client: "codex", Program: "/bin/codex"},
	})
	require.NoError(t, err)

	guide := teamName("guide", record.ID)
	require.Len(t, fakes.core.runs, 1, "the guide is started for the message")
	assert.Equal(t, "/bin/codex", fakes.core.runs[0].Argv[0], "with the coding agent the user chose")
	assert.Equal(t, "guide", fakes.core.runs[0].Labels[labelRole])
	posts := fakes.chorus.posted()
	require.Len(t, posts, 1)
	title := "You"
	assert.Equal(t, &genchorus.PostMessagePayload{
		Swarm: record.Team, Sender: teamName("you", record.ID), Kind: "human", Role: &title,
		To: "#" + record.Team, Text: "@" + guide + " Build a refund assistant", ReplyTo: &reply,
	}, posts[0])
	kind := "human"
	assert.Equal(t, &genhighbay.TeamMessage{
		ID: "#" + record.Team + ":1", Target: "#" + record.Team, Seq: 1, Sender: teamName("you", record.ID), SenderRole: "you",
		Model: &kind, Text: "@" + guide + " Build a refund assistant", ReplyTo: &reply, SentAtMs: message.SentAtMs,
	}, message)

	_, err = server.SendMessage(userContext(server.state), &genhighbay.SendTeamMessagePayload{To: "team", Text: "Thanks"})
	require.NoError(t, err)
	assert.Len(t, fakes.core.runs, 1, "a running guide is not started again")
	assert.Len(t, fakes.chorus.posted(), 2)
}

// TestSendMessageStartsTheGuideOnlyWhenNobodyAnswers covers who would answer
// a message for the guide or the team.
func TestSendMessageStartsTheGuideOnlyWhenNobodyAnswers(t *testing.T) {
	for _, test := range []struct {
		name    string
		to      string
		arrange func(t *testing.T, server *Server, fakes *teamFakes)
		starts  int
	}{
		{name: "no guide yet", to: "guide", arrange: func(*testing.T, *Server, *teamFakes) {}, starts: 1},
		{name: "no guide yet, message for the team", to: "team", arrange: func(*testing.T, *Server, *teamFakes) {}, starts: 1},
		{
			name: "a guide session in the user's editor is present in Chorus",
			to:   "guide",
			arrange: func(t *testing.T, server *Server, fakes *teamFakes) {
				handle := teamName("guide", server.state.Record.ID)
				fakes.chorus.set(func(view *genchorus.SwarmView) {
					view.Agents = []*genchorus.Agent{{Handle: handle, WatchingOnThisServer: true}}
				})
				observeChorus(t, server, fakes)
			},
		},
		{
			name: "the guide's terminal ended",
			to:   "guide",
			arrange: func(_ *testing.T, server *Server, fakes *teamFakes) {
				fakes.core.add(hc.Terminal{Address: "old", State: hc.StateFinished, Labels: map[string]string{
					labelProject: server.state.Record.ID, labelRole: "guide", labelManaged: "true",
				}})
			},
			starts: 1,
		},
		{
			name: "a message for a worker starts no guide",
			to:   "builder",
			arrange: func(t *testing.T, server *Server, _ *teamFakes) {
				_, err := registerTestAgent(server, "builder-1", "builder", t.TempDir())
				require.NoError(t, err)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, fakes := teamServer(t)
			test.arrange(t, server, fakes)
			_, err := server.SendMessage(userContext(server.state), &genhighbay.SendTeamMessagePayload{To: test.to, Text: "Hello"})
			require.NoError(t, err)
			assert.Len(t, fakes.core.runs, test.starts)
			assert.Len(t, fakes.chorus.posted(), 1)
		})
	}
}

// TestSendMessageRefusals covers every way a message is refused. A refused
// message is not posted.
func TestSendMessageRefusals(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload genhighbay.SendTeamMessagePayload
		arrange func(t *testing.T, server *Server, fakes *teamFakes)
		want    string
		cause   string
	}{
		{
			name:    "the server is stopping",
			payload: genhighbay.SendTeamMessagePayload{To: "team", Text: "Hello"},
			arrange: func(t *testing.T, server *Server, _ *teamFakes) { require.NoError(t, server.Close()) },
			want:    "team_unavailable", cause: "stopping",
		},
		{
			name:    "the guide cannot be started: herdr-core is unreachable",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Hello"},
			arrange: func(_ *testing.T, _ *Server, fakes *teamFakes) {
				fakes.core.dialErr = errors.New("hc is not installed")
			},
			want: "team_unavailable", cause: "hc",
		},
		{
			name:    "the chosen guide is not installed",
			payload: genhighbay.SendTeamMessagePayload{To: "guide", Text: "Hello", GuideChoice: &genhighbay.RoleAgentChoice{Client: "gemini", Program: "/bin/gemini"}},
			arrange: func(*testing.T, *Server, *teamFakes) {},
			want:    "unprocessable",
		},
		{
			name:    "a worker no build has registered",
			payload: genhighbay.SendTeamMessagePayload{To: "builder", Text: "Hello"},
			arrange: func(*testing.T, *Server, *teamFakes) {},
			want:    "unprocessable",
		},
		{
			name: "Change Card authoring context sent to team refuses before starting guide",
			payload: genhighbay.SendTeamMessagePayload{To: "team", Text: "New card", Context: genhighbay.NewContextNewChange(
				&genhighbay.NewChangeContext{Scope: "separate_card"},
			)},
			arrange: func(*testing.T, *Server, *teamFakes) {},
			want:    "unprocessable",
		},
		{
			name:    "Chorus cannot be resolved",
			payload: genhighbay.SendTeamMessagePayload{To: "team", Text: "Hello"},
			arrange: func(_ *testing.T, _ *Server, fakes *teamFakes) {
				fakes.chorus.connectErr = errors.New("chorus is not installed")
			},
			want: "team_unavailable", cause: "chorus",
		},
		{
			name:    "Chorus refuses the post",
			payload: genhighbay.SendTeamMessagePayload{To: "team", Text: "Hello"},
			arrange: func(_ *testing.T, _ *Server, fakes *teamFakes) { fakes.chorus.fail(errors.New("handle in use")) },
			want:    "team_unavailable", cause: "chorus",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, fakes := teamServer(t)
			test.arrange(t, server, fakes)
			_, err := server.SendMessage(userContext(server.state), &test.payload)
			assert.Equal(t, test.want, errorName(err))
			var unavailable *genproject.TeamUnavailable
			if test.cause != "" && assert.ErrorAs(t, err, &unavailable) {
				assert.Equal(t, test.cause, unavailable.Cause)
			}
			assert.Empty(t, fakes.chorus.posted())
		})
	}

	t.Run("Chorus is resolved again after a failed post", func(t *testing.T) {
		server, fakes := teamServer(t)
		fakes.chorus.fail(errors.New("connection refused"))
		_, err := server.SendMessage(userContext(server.state), &genhighbay.SendTeamMessagePayload{To: "team", Text: "Hello"})
		require.Error(t, err)
		fakes.chorus.fail(nil)
		_, err = server.SendMessage(userContext(server.state), &genhighbay.SendTeamMessagePayload{To: "team", Text: "Hello"})
		require.NoError(t, err)
		assert.Equal(t, 2, fakes.chorus.connects)
	})
}

// TestReadConversation checks what the page shows of the swarm: the
// project's channel and the direct messages to the user, oldest first, each
// with the sender's role, and the channel's pin.
func TestReadConversation(t *testing.T) {
	server, fakes := teamServer(t)
	record, _ := server.currentTeam()
	_, err := registerTestAgent(server, "guide-1", "guide", record.Directory)
	require.NoError(t, err)
	_, err = registerTestAgent(server, "builder-1", "builder", t.TempDir())
	require.NoError(t, err)
	user, channel := teamName("you", record.ID), "#"+record.Team

	empty, err := server.ReadConversation(t.Context())
	require.NoError(t, err)
	assert.Equal(t, &genhighbay.TeamConversation{
		Team: record.Team, Channel: channel, ChorusStatus: "unavailable", Messages: []*genhighbay.TeamMessage{},
	}, empty, "before Chorus was read the conversation is empty")

	human, service, model, replyTo := "human", "service", "claude-opus", 1
	isHuman := true
	fakes.chorus.set(func(view *genchorus.SwarmView) {
		view.Channels = []*genchorus.Channel{
			{Name: "#another", Messages: []*genchorus.Message{{Seq: 1, Sender: "stranger", Text: "elsewhere", SentAtMs: 1}}},
			{
				Name: channel,
				Pin:  &genchorus.Pin{Seq: 2, Sender: "guide-1", Text: "Plan: refunds", Model: &model},
				Messages: []*genchorus.Message{
					{Seq: 1, Sender: user, Text: "@guide-1 Build refunds", SentAtMs: 10, Human: &isHuman, Model: &human},
					{Seq: 2, Sender: "guide-1", Text: "Plan: refunds", ReplyTo: &replyTo, SentAtMs: 20, Model: &model},
					{Seq: 3, Sender: teamName("highbay", record.ID), Text: "@builder-1 you have an unread assignment", SentAtMs: 40, Model: &service},
					{Seq: 4, Sender: "helper", Text: "I can review", SentAtMs: 50, Model: &model},
				},
			},
		}
		view.Inboxes = []*genchorus.Inbox{
			{Recipient: user, Messages: []*genchorus.Message{{Seq: 1, Sender: "builder-1", Text: "Tests pass", SentAtMs: 30, Model: &model}}},
			{Recipient: "builder-1", Messages: []*genchorus.Message{{Seq: 1, Sender: "guide-1", Text: "private", SentAtMs: 35}}},
		}
	})
	observeChorus(t, server, fakes)

	conversation, err := server.ReadConversation(t.Context())
	require.NoError(t, err)
	message := func(target string, seq int, sender, role string, kind *string, text string, reply *int, sent int64) *genhighbay.TeamMessage {
		return &genhighbay.TeamMessage{
			ID: target + ":" + string(rune('0'+seq)), Target: target, Seq: seq, Sender: sender, SenderRole: role,
			Model: kind, Text: text, ReplyTo: reply, SentAtMs: sent,
		}
	}
	assert.Equal(t, &genhighbay.TeamConversation{
		Team: record.Team, Channel: channel, ChorusStatus: "connected",
		Pin: message(channel, 2, "guide-1", "guide", &model, "Plan: refunds", &replyTo, 20),
		Messages: []*genhighbay.TeamMessage{
			message(channel, 1, user, "you", &human, "@guide-1 Build refunds", nil, 10),
			message(channel, 2, "guide-1", "guide", &model, "Plan: refunds", &replyTo, 20),
			message("@"+user, 1, "builder-1", "builder", &model, "Tests pass", nil, 30),
			message(channel, 3, teamName("highbay", record.ID), "service", &service, "@builder-1 you have an unread assignment", nil, 40),
			message(channel, 4, "helper", "peer", &model, "I can review", nil, 50),
		},
	}, conversation)
}
