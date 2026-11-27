// This file holds one open channel's queue of frames from the core and the
// flow control that bounds it.

package hc

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
)

// stream is one open channel other than channel 0: an event subscription or
// an attached terminal. The connection's reader queues the core's frames with
// push; one consumer takes them with next. The queue is bounded by the core's
// own flow control: the core sends at most the credit it was granted, and
// credit is granted only for bytes the consumer has taken.
type stream struct {
	// client is the connection the channel belongs to.
	client *Client
	// channel is the channel number the core assigned.
	channel uint32
	// wake holds at most one signal that the queue or ended changed.
	wake chan struct{}

	// mu guards the fields below.
	mu sync.Mutex
	// queue holds frames from the core that the consumer has not taken.
	queue []frame
	// allowance is how many more stream bytes the core may send.
	allowance int
	// ended is why no further frame will be queued: errCoreClosed after the
	// core's closing frame, ErrStreamEnded after a local close, or the
	// connection's error. It is nil while the channel is open.
	ended error
	// closed records that this side closed the channel.
	closed bool
}

// errCoreClosed marks a channel the core closed. It never leaves this
// package: a lost connection's error may wrap io.EOF, so io.EOF cannot serve
// as the marker.
var errCoreClosed = errors.New("hc: the core closed the channel")

// newStream returns a stream holding the initial credit and the frames that
// arrived before a reply named its channel. The caller has bounded their
// stream bytes by the initial credit and kept nothing after a closing frame.
// The stream is already over when they end with the core's closing frame.
func newStream(c *Client, channel uint32, early []frame) *stream {
	s := &stream{client: c, channel: channel, wake: make(chan struct{}, 1), allowance: window}
	for _, f := range early {
		switch f.kind {
		case kindData:
			s.allowance -= len(f.payload)
		case kindClose:
			s.ended = errCoreClosed
		}
		s.queue = append(s.queue, f)
	}
	return s
}

// push queues one frame from the core. It returns ErrProtocol when the core
// sends more stream bytes than it has credit for. Frames for a channel this
// side already closed are dropped.
func (s *stream) push(f frame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended != nil {
		return nil
	}
	switch f.kind {
	case kindData:
		if len(f.payload) > s.allowance {
			return fmt.Errorf("%w: channel %d sent %d bytes with %d of credit",
				ErrProtocol, s.channel, len(f.payload), s.allowance)
		}
		s.allowance -= len(f.payload)
	case kindClose:
		s.ended = errCoreClosed
	}
	s.queue = append(s.queue, f)
	s.signal()
	return nil
}

// next returns the next queued frame, waiting for one if needed. Once the
// queue is empty and the channel is over it returns why: errCoreClosed after
// the core's closing frame was taken, ErrStreamEnded after a local close, or
// the connection's error. A ctx that is over wins over a queued frame: next
// returns ctx's error and leaves the frame queued.
func (s *stream) next(ctx context.Context) (frame, error) {
	for {
		if err := ctx.Err(); err != nil {
			return frame{}, err
		}
		s.mu.Lock()
		if len(s.queue) > 0 {
			f := s.queue[0]
			s.queue[0] = frame{}
			s.queue = s.queue[1:]
			s.mu.Unlock()
			return f, nil
		}
		err := s.ended
		s.mu.Unlock()
		if err != nil {
			return frame{}, err
		}
		select {
		case <-s.wake:
		case <-ctx.Done():
			return frame{}, ctx.Err()
		}
	}
}

// credit tells the core that n stream bytes were consumed, so it may send
// that many more. It does nothing once the channel is over.
func (s *stream) credit(n uint32) error {
	if n == 0 {
		return nil
	}
	s.mu.Lock()
	if s.ended != nil {
		s.mu.Unlock()
		return nil
	}
	s.allowance += int(n)
	s.mu.Unlock()
	var payload [4]byte
	binary.LittleEndian.PutUint32(payload[:], n)
	return s.client.send(s.channel, kindCredit, payload[:])
}

// write sends one frame to the core on this channel. It returns ErrStreamEnded
// when the channel is over, or the connection's error when that ended it.
func (s *stream) write(kind frameKind, payload []byte) error {
	s.mu.Lock()
	ended := s.ended
	s.mu.Unlock()
	switch {
	case ended == nil:
		return s.client.send(s.channel, kind, payload)
	case ended == errCoreClosed:
		return ErrStreamEnded
	default:
		return ended
	}
}

// close ends the channel from this side: it drops queued frames and, when the
// core has not closed the channel already, sends the closing frame. Frames
// the core sent meanwhile are dropped by the reader until the core confirms.
// Closing twice is safe.
func (s *stream) close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	open := s.ended == nil
	if open {
		s.ended = ErrStreamEnded
	}
	s.queue = nil
	s.signal()
	s.mu.Unlock()
	if !open {
		return nil
	}
	c := s.client
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil
	}
	delete(c.streams, s.channel)
	c.closing[s.channel] = true
	c.mu.Unlock()
	return c.send(s.channel, kindClose, nil)
}

// end marks the channel over because the connection ended with err. Frames
// already queued stay available to the consumer.
func (s *stream) end(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended == nil {
		s.ended = err
	}
	s.signal()
}

// signal wakes the consumer if it waits. s.mu is held.
func (s *stream) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
