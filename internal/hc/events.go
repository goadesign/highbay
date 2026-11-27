// This file follows herdr-core's event subscription: it turns the event
// channel's JSON lines into typed updates, in order.

package hc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

type (
	// StreamUpdate is what SubscribeEvents delivers: Snapshot first, then
	// Event, Heartbeat and Missed in the order the core sent them.
	StreamUpdate interface {
		streamUpdate()
	}

	// Snapshot is the first update: every matching terminal as of Seq.
	Snapshot struct {
		Seq       uint64
		Terminals []Terminal
	}

	// Missed replaces everything known so far. The core sends it when the
	// subscriber fell too far behind to be sent each change: Terminals is
	// every matching terminal as of Seq.
	Missed struct {
		Seq       uint64
		Terminals []Terminal
	}

	// Heartbeat says the subscription is alive and current as of Seq. The
	// core sends one about every 15 seconds while it has nothing else to say.
	Heartbeat struct {
		Seq uint64
	}

	// Event is one change to one terminal. Seq rises by one per event the
	// core records, so a subscriber with a filter sees gaps.
	Event struct {
		Seq uint64
		// Terminal is the changed terminal's address. It is empty for a
		// change that concerns no terminal.
		Terminal string
		// Change says what changed.
		Change Change
	}

	// Change is the fact one Event carries: Created, Restarted, Lost,
	// Finished, Removed, StatusChanged, TurnEnded, TitleChanged,
	// LabelsChanged, SizeChanged, ForegroundChanged or OtherChange.
	Change interface {
		change()
	}

	// Created adds a terminal. Terminal is its whole record.
	Created struct {
		Terminal Terminal
	}

	// Restarted replaces a terminal's record: a kept terminal got a new
	// shell after losing its program. Streams attached to it have closed.
	Restarted struct {
		Terminal Terminal
	}

	// Lost reports that a kept terminal's program ended. The terminal stays
	// live without a program until Restarted.
	Lost struct {
		End CloseReason
	}

	// Finished reports that a terminal's program ended for good: its state
	// is now StateFinished.
	Finished struct {
		End CloseReason
	}

	// Removed drops a terminal's record.
	Removed struct{}

	// StatusChanged replaces what the program reports about itself. Status
	// is nil when the program cleared it.
	StatusChanged struct {
		Status   *Status
		Records  map[string]Status
		Reported bool
	}

	// TurnEnded reports that the status left "working": Turn is the counter
	// afterwards and State the word the turn ended on.
	TurnEnded struct {
		Turn  uint64
		State string
	}

	// TitleChanged replaces the terminal's title.
	TitleChanged struct {
		Title string
	}

	// LabelsChanged replaces all of a terminal's labels. Record is set when
	// the change made the terminal match the subscription's filter, so the
	// subscriber must add it. A terminal whose labels no longer match leaves
	// the subscription with this event.
	LabelsChanged struct {
		Labels map[string]string
		Record *Terminal
	}

	// SizeChanged replaces the terminal's grid size.
	SizeChanged struct {
		Cols, Rows uint16
	}

	// ForegroundChanged replaces the terminal's foreground process. It is
	// nil when the core reports none.
	ForegroundChanged struct {
		Foreground *Foreground
	}

	// OtherChange is a change this client does not decode: a kind added by a
	// newer core, or one Highbay does not use. Data is the core's JSON.
	OtherChange struct {
		Type string
		Data json.RawMessage
	}

	// eventDecoder turns the event channel's bytes, JSON lines split at
	// arbitrary points, into updates in order.
	eventDecoder struct {
		// partial holds the bytes of a line whose end has not arrived.
		partial []byte
		// floor is the sequence of the latest snapshot. Events at or below
		// it are already part of that snapshot and are dropped.
		floor uint64
	}

	// notification is one JSON line on the event channel.
	notification struct {
		Method string `json:"method"`
		Params struct {
			Seq       uint64          `json:"seq"`
			Type      string          `json:"type"`
			Terminal  string          `json:"terminal"`
			Data      json.RawMessage `json:"data"`
			Terminals []Terminal      `json:"terminals"`
		} `json:"params"`
	}
)

// SubscribeEvents follows every change to the terminals carrying all of
// labels; with no labels, every terminal. It calls emit from the calling
// goroutine, first with a Snapshot and then with each update in order, until
// ctx ends or the connection does. Once ctx is over, emit is not called
// again. The core is told an update is consumed only after emit returns, so
// a slow emit makes the core fall back to Missed rather than queue without
// bound.
//
// It returns ctx's error when ctx ends, the connection's error when the
// connection ended, an error matching ErrStreamEnded when the core closed the
// subscription, and one matching ErrProtocol when an update cannot be
// decoded.
func (c *Client) SubscribeEvents(ctx context.Context, labels map[string]string, emit func(StreamUpdate)) error {
	var result subscribeResult
	s, err := c.open(ctx, "events.subscribe", subscribeParams{Labels: labels}, &result)
	if err != nil {
		return err
	}
	emit(Snapshot{Seq: result.Seq, Terminals: result.Terminals})
	decoder := eventDecoder{floor: result.Seq}
	err = follow(ctx, s, &decoder, emit)
	return errors.Join(err, s.close())
}

// follow delivers the subscription's updates until it ends, returning why.
func follow(ctx context.Context, s *stream, decoder *eventDecoder, emit func(StreamUpdate)) error {
	for {
		f, err := s.next(ctx)
		if err == errCoreClosed {
			return fmt.Errorf("%w: the core closed the event subscription", ErrStreamEnded)
		}
		if err != nil {
			return err
		}
		if f.kind != kindData {
			continue
		}
		if err := decoder.feed(ctx, f.payload, emit); err != nil {
			return err
		}
		if err := s.credit(uint32(len(f.payload))); err != nil {
			return err
		}
	}
}

// feed takes the next bytes of the event channel and emits an update for
// every line they complete. It stops with ctx's error, before the next emit,
// once ctx is over.
func (d *eventDecoder) feed(ctx context.Context, data []byte, emit func(StreamUpdate)) error {
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			if len(d.partial)+len(data) > MaxPayload {
				return fmt.Errorf("%w: event line exceeds %d bytes", ErrProtocol, MaxPayload)
			}
			d.partial = append(d.partial, data...)
			return nil
		}
		line := data[:end]
		if len(d.partial) > 0 {
			line = append(d.partial, line...)
			d.partial = d.partial[:0]
		}
		data = data[end+1:]
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		update, err := d.decode(line)
		if err != nil {
			return err
		}
		if update == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		emit(update)
	}
	return nil
}

// decode turns one JSON line into an update. It returns nil for an event the
// latest snapshot already covers and for a notification this client does not
// know.
func (d *eventDecoder) decode(line []byte) (StreamUpdate, error) {
	var n notification
	if err := json.Unmarshal(line, &n); err != nil {
		return nil, fmt.Errorf("%w: event line: %v", ErrProtocol, err)
	}
	p := n.Params
	switch n.Method {
	case "heartbeat":
		return Heartbeat{Seq: p.Seq}, nil
	case "missed":
		d.floor = p.Seq
		return Missed{Seq: p.Seq, Terminals: p.Terminals}, nil
	case "event":
		if p.Seq <= d.floor {
			return nil, nil
		}
		change, err := decodeChange(p.Type, p.Data)
		if err != nil {
			return nil, fmt.Errorf("%w: %s event %d: %v", ErrProtocol, p.Type, p.Seq, err)
		}
		return Event{Seq: p.Seq, Terminal: p.Terminal, Change: change}, nil
	default:
		return nil, nil
	}
}

// decodeChange decodes an event's data by its type.
func decodeChange(kind string, data json.RawMessage) (Change, error) {
	switch kind {
	case "created":
		var c Created
		err := json.Unmarshal(data, &c.Terminal)
		return c, err
	case "restarted":
		var c Restarted
		err := json.Unmarshal(data, &c.Terminal)
		return c, err
	case "lost":
		var c Lost
		err := json.Unmarshal(data, &c.End)
		return c, err
	case "finished":
		var c Finished
		err := json.Unmarshal(data, &c.End)
		return c, err
	case "removed":
		return Removed{}, nil
	case "status":
		var body struct {
			Status   *Status           `json:"status"`
			Records  map[string]Status `json:"records"`
			Reported bool              `json:"reported"`
		}
		err := json.Unmarshal(data, &body)
		return StatusChanged{Status: body.Status, Records: body.Records, Reported: body.Reported}, err
	case "turn":
		var body struct {
			Turn  uint64 `json:"turn"`
			State string `json:"state"`
		}
		err := json.Unmarshal(data, &body)
		return TurnEnded{Turn: body.Turn, State: body.State}, err
	case "title":
		var body struct {
			Title string `json:"title"`
		}
		err := json.Unmarshal(data, &body)
		return TitleChanged{Title: body.Title}, err
	case "labels":
		var body struct {
			Labels map[string]string `json:"labels"`
			Record *Terminal         `json:"record"`
		}
		err := json.Unmarshal(data, &body)
		return LabelsChanged{Labels: body.Labels, Record: body.Record}, err
	case "size":
		var body Size
		err := json.Unmarshal(data, &body)
		return SizeChanged{Cols: body.Cols, Rows: body.Rows}, err
	case "foreground":
		var c ForegroundChanged
		err := json.Unmarshal(data, &c.Foreground)
		return c, err
	default:
		return OtherChange{Type: kind, Data: data}, nil
	}
}

func (Snapshot) streamUpdate()  {}
func (Missed) streamUpdate()    {}
func (Heartbeat) streamUpdate() {}
func (Event) streamUpdate()     {}

func (Created) change()           {}
func (Restarted) change()         {}
func (Lost) change()              {}
func (Finished) change()          {}
func (Removed) change()           {}
func (StatusChanged) change()     {}
func (TurnEnded) change()         {}
func (TitleChanged) change()      {}
func (LabelsChanged) change()     {}
func (SizeChanged) change()       {}
func (ForegroundChanged) change() {}
func (OtherChange) change()       {}
