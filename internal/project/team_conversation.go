// This file carries the conversation between the user and the team: it reads
// the project's Chorus channel for the page and posts the user's messages,
// starting the guide first when nobody would answer.
package project

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	genchorus "github.com/goadesign/chorus/gen/view"
	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/hc"
	"go.opentelemetry.io/otel"
)

// chorusTextLimit is the longest message Chorus stores, in characters.
const chorusTextLimit = 4000

// ReadConversation returns the project's Chorus channel and the direct
// messages sent to the user, oldest first, as last read from Chorus. It calls
// nothing: before the first read, and for a project nobody has joined, the
// conversation is empty.
func (s *Server) ReadConversation(ctx context.Context) (*genhighbay.TeamConversation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	record, roles := s.currentTeam()
	channel := "#" + record.Team
	conversation := &genhighbay.TeamConversation{
		Team: record.Team, Channel: channel, ChorusStatus: s.team.observed.chorusStatus(), Messages: []*genhighbay.TeamMessage{},
	}
	view := s.team.observed.chorusView()
	if view == nil {
		return conversation, nil
	}
	user := userHandle(record)
	for _, c := range view.Channels {
		if c.Name != channel {
			continue
		}
		for _, message := range c.Messages {
			conversation.Messages = append(conversation.Messages, teamMessage(channel, message, user, roles))
		}
		if c.Pin != nil {
			pin := &genchorus.Message{Seq: c.Pin.Seq, Sender: c.Pin.Sender, Text: c.Pin.Text, Human: c.Pin.Human, Model: c.Pin.Model}
			for _, message := range c.Messages {
				if message.Seq == pin.Seq {
					pin.SentAtMs, pin.ReplyTo = message.SentAtMs, message.ReplyTo
				}
			}
			conversation.Pin = teamMessage(channel, pin, user, roles)
		}
	}
	for _, inbox := range view.Inboxes {
		if inbox.Recipient != user {
			continue
		}
		for _, message := range inbox.Messages {
			conversation.Messages = append(conversation.Messages, teamMessage("@"+user, message, user, roles))
		}
	}
	sort.SliceStable(conversation.Messages, func(i, j int) bool {
		return conversation.Messages[i].SentAtMs < conversation.Messages[j].SentAtMs
	})
	return conversation, nil
}

// SendMessage posts the user's message to the project's Chorus channel,
// mentioning the role it is for so that role wakes. A message for the guide
// or the whole team first starts a guide in a herdr-core terminal when none
// is running and no attached guide session is present in Chorus, using
// p.GuideChoice, else the saved choice, else an installed default. It returns
// the message as Chorus stored it.
func (s *Server) SendMessage(ctx context.Context, p *genhighbay.SendTeamMessagePayload) (*genhighbay.TeamMessage, error) {
	ctx, cancel := s.acceptedTeamContext(ctx)
	defer cancel()
	ctx, span := otel.Tracer("highbay/project").Start(ctx, "highbay.project.send_message")
	defer span.End()
	s.team.Lock()
	defer s.team.Unlock()
	if s.team.closed || ctx.Err() != nil {
		return nil, userTeamError(teamUnavailable(ctx, "lifetime", "stopping"))
	}
	record, roles := s.currentTeam()
	guide := s.guide(record, roles)
	text, err := messageText(p, guide, roles)
	if err != nil {
		return nil, err
	}
	if p.To == "guide" || p.To == "team" {
		if err := s.startGuide(ctx, record, guide, p.GuideChoice); err != nil {
			return nil, err
		}
		record, roles = s.currentTeam()
	}
	chorus, err := s.team.chorus.client(ctx, s.team.dependencies.ConnectChorus)
	if err != nil {
		return nil, userTeamError(teamUnavailable(ctx, "connect_chorus", "chorus"))
	}
	user, title := userHandle(record), "You"
	posted, err := chorus.PostMessage(ctx, &genchorus.PostMessagePayload{
		Swarm: record.Team, Sender: user, Kind: "human", Role: &title, To: "#" + record.Team, Text: text, ReplyTo: p.ReplyTo,
	})
	if err != nil {
		s.team.chorus.forget(chorus)
		return nil, userTeamError(teamUnavailable(ctx, "post_message", "chorus"))
	}
	return teamMessage(posted.Target, posted.Message, user, roles), nil
}

// userHandle is the Chorus handle the user posts under in this project.
// Chorus binds a handle to one swarm, so each project has its own.
func userHandle(record Record) string {
	return teamName("you", record.ID)
}

// highbayHandle is the Chorus handle Highbay itself posts under in this
// project, for notices such as an unread assignment.
func highbayHandle(record Record) string {
	return teamName("highbay", record.ID)
}

// messageText builds the text Chorus stores for the user's message: a mention
// of the role it is for, the user's words, and the page's context for them,
// if any. It refuses a Change Card authoring context sent to anyone other than
// guide, a message for a worker role that no build has registered, and a
// message that outgrows what Chorus stores.
func messageText(p *genhighbay.SendTeamMessagePayload, guide teamRole, roles []teamRole) (string, error) {
	_, isNew := p.Context.AsNewChange()
	_, isRefine := p.Context.AsRefineChange()
	_, isAnswers := p.Context.AsReviseAnswers()
	if (isNew || isRefine || isAnswers) && p.To != "guide" {
		return "", genhighbay.MakeUnprocessable(errors.New("Change Card authoring context may only be sent to guide"))
	}
	var text strings.Builder
	switch p.To {
	case "team":
	case "guide":
		text.WriteString("@" + guide.Handle + " ")
	default:
		at := slices.IndexFunc(roles, func(role teamRole) bool { return role.Role == p.To })
		if at < 0 {
			return "", genhighbay.MakeUnprocessable(errors.New("that role is not part of this project's team yet; it joins when a build starts"))
		}
		text.WriteString("@" + roles[at].Handle + " ")
	}
	text.WriteString(p.Text)
	text.WriteString(messageContext(p.Context))
	message := text.String()
	if utf8.RuneCountInString(message) > chorusTextLimit {
		return "", genhighbay.MakeUnprocessable(errors.New("the message with its context is too long; shorten it and send it again"))
	}
	return message, nil
}

// messageContext says, in Markdown the recipient can act on, what on the page
// the user's message is about. It is empty when the message carries no context.
func messageContext(context genhighbay.Context) string {
	var text strings.Builder
	if _, ok := context.AsNewChange(); ok {
		text.WriteString("\n\n**New Change Card requested:** Prepare a separate change in the draft and save it as a new Change Card with propose_change (do not call revise_change on an existing Change Card).")
	}
	if refine, ok := context.AsRefineChange(); ok {
		text.WriteString("\n\n**Revise this Change Card** (change `" + refine.ChangeID +
			"`, latest request revision `" + refine.RequestRevision + "`): Update the draft for this request and submit a revised Change Card with revise_change.")
	}
	if answers, ok := context.AsReviseAnswers(); ok {
		text.WriteString("\n\n**Revise the draft to implement my answers** (change `" + answers.ChangeID +
			"`, request revision `" + answers.RequestRevision + "`):")
		questions := make([]string, 0, len(answers.Answers))
		for question := range answers.Answers {
			questions = append(questions, question)
		}
		sort.Strings(questions)
		for _, question := range questions {
			text.WriteString("\n- `" + question + "`: " + answers.Answers[question])
		}
	}
	if element, ok := context.AsArchitectureElement(); ok {
		text.WriteString("\n\n**About this architecture element:** " + element.ElementName + " (`" + element.ElementID + "`)")
		if element.ChangeID != nil {
			text.WriteString(", change `" + *element.ChangeID + "`")
		}
	}
	if flow, ok := context.AsStoryFlow(); ok {
		text.WriteString("\n\n**About this Story:** " + flow.Story)
		if flow.FlowKey != nil {
			text.WriteString(", flow `" + *flow.FlowKey + "`")
		}
		if flow.ChangeID != nil {
			text.WriteString(", change `" + *flow.ChangeID + "`")
		}
	}
	return text.String()
}

// teamMessage presents one Chorus message for the page. The sender's role is
// decided by its handle: the user, one of this project's roles, a local
// service such as Highbay, or anyone else in the swarm.
func teamMessage(target string, message *genchorus.Message, user string, roles []teamRole) *genhighbay.TeamMessage {
	role := "peer"
	switch {
	case message.Sender == user:
		role = "you"
	case message.Model != nil && *message.Model == "service":
		role = "service"
	}
	for _, registered := range roles {
		if registered.Handle == message.Sender {
			role = registered.Role
		}
	}
	return &genhighbay.TeamMessage{
		ID:         target + ":" + strconv.Itoa(message.Seq),
		Target:     target,
		Seq:        message.Seq,
		Sender:     message.Sender,
		SenderRole: role,
		Model:      message.Model,
		Text:       message.Text,
		ReplyTo:    message.ReplyTo,
		SentAtMs:   message.SentAtMs,
	}
}

// startGuide starts the guide's terminal unless a guide is already there to
// answer: herdr-core runs an agent for it, or the guide has no terminal at
// all and is present in Chorus, as a session attached from an editor is. A
// guide whose terminal ended is started again, whatever Chorus still shows.
// requested, when set, is the user's choice of coding agent. The team lock
// is held.
func (s *Server) startGuide(ctx context.Context, record Record, guide teamRole, requested *genhighbay.RoleAgentChoice) error {
	client, err := s.team.terminals.client(ctx, s.team.dependencies.DialTerminals)
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "dial_terminals", "hc"))
	}
	terminals, err := s.listRoleTerminals(ctx, client, "guide")
	if err != nil {
		return userTeamError(teamUnavailable(ctx, "list_terminals", "hc"))
	}
	for _, terminal := range terminals {
		if hc.RoleRunning(terminal, s.team.dependencies.Shell) {
			return nil
		}
	}
	if len(terminals) == 0 && s.team.observed.guidePresent(s.now(), guide.Handle) {
		return nil
	}
	explicit := map[string]*genhighbay.RoleAgentChoice{}
	if requested != nil {
		explicit["guide"] = requested
	}
	choices, err := s.resolveChoices(ctx, []teamRole{guide}, explicit)
	if err != nil {
		return err
	}
	return s.startRoles(ctx, record, []teamRole{guide}, choices, false)
}

// chorusView returns the swarm as last read; nil before the first read.
func (o *teamObservation) chorusView() *genchorus.SwarmView {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.view
}

// guidePresent reports whether a guide session without a terminal is present
// in Chorus, as an attached session in the user's own editor is.
func (o *teamObservation) guidePresent(now time.Time, handle string) bool {
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.present(now, handle)
}
