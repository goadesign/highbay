// This file runs "hc connect" as the byte stream to the core and reaps it
// when the connection closes.

package hc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

type (
	// execConnector starts "<program> connect" for each connection.
	execConnector struct {
		// program is the herdr-core executable, "hc" or a path to it.
		program string
		// env is the child's whole environment; nil inherits this process's.
		env []string
		// grace is how long the child may take to exit once its input is
		// closed before it is killed.
		grace time.Duration
		// drain is how long, after the child exits, to wait for the rest of
		// its error output when another process still holds that output open.
		drain time.Duration
	}

	// execStream is the child's standard input and output as one stream.
	execStream struct {
		// program names the child in errors.
		program string
		// grace is execConnector.grace.
		grace time.Duration
		// cmd is the started child.
		cmd *exec.Cmd
		// in is the write end of the child's standard input.
		in *os.File
		// out is the read end of the child's standard output.
		out *os.File
		// stderr keeps the end of what the child wrote to standard error.
		stderr *tail
		// exited receives the child's exit result once.
		exited chan error
		// closeOnce closes in once; closeErr is that close's error.
		closeOnce sync.Once
		closeErr  error
	}

	// tail keeps the last limit bytes written to it.
	tail struct {
		mu    sync.Mutex
		limit int
		data  []byte
	}
)

const (
	// exitGrace is how long "hc connect" may take to exit after its input
	// closes. It only forwards bytes, so it normally exits at once.
	exitGrace = 2 * time.Second
	// stderrDrain is how long to wait for the child's remaining error output
	// after it exits. What the child wrote is already in the pipe, so this
	// only matters when a process it started, such as the core itself, keeps
	// the output open.
	stderrDrain = 100 * time.Millisecond
	// stderrTail bounds how much of the child's error output an error quotes.
	stderrTail = 4 << 10
)

// ExecConnector returns a Connector that runs "<program> connect", which
// speaks herdr-core's protocol on its standard input and output and starts
// the core when none runs. env is the child's whole environment; nil inherits
// this process's.
//
// Closing the stream closes the child's input, which makes it exit. The
// release function waits for that exit, kills the child if it has not exited
// after two seconds, and reports a kill or a non-zero exit together with the
// end of the child's error output. A child that exits by itself is never
// reported as killed, even when a process it started keeps its error output
// open.
func ExecConnector(program string, env []string) Connector {
	return execConnector{program: program, env: env, grace: exitGrace, drain: stderrDrain}.connect
}

// connect starts the child with pipes this process owns, so the read end
// stays open until release closes it and no output is lost at exit.
func (e execConnector) connect(ctx context.Context) (io.ReadWriteCloser, func() error, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	childIn, in, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	out, childOut, err := os.Pipe()
	if err != nil {
		return nil, nil, errors.Join(err, childIn.Close(), in.Close())
	}
	stderr := &tail{limit: stderrTail}
	cmd := exec.Command(e.program, "connect")
	cmd.Env = e.env
	cmd.Stdin = childIn
	cmd.Stdout = childOut
	cmd.Stderr = stderr
	// A process the child started may keep its error output open after the
	// child exits; Wait then returns after drain instead of at end of file.
	cmd.WaitDelay = e.drain
	startErr := cmd.Start()
	// The child holds its own copies of its ends now, or never will.
	endsErr := errors.Join(childIn.Close(), childOut.Close())
	if startErr != nil {
		return nil, nil, errors.Join(fmt.Errorf("start %s connect: %w", e.program, startErr),
			endsErr, in.Close(), out.Close())
	}
	s := &execStream{
		program: e.program,
		grace:   e.grace,
		cmd:     cmd,
		in:      in,
		out:     out,
		stderr:  stderr,
		exited:  make(chan error, 1),
	}
	go s.wait()
	if endsErr != nil {
		return nil, nil, errors.Join(endsErr, s.Close(), s.release())
	}
	return s, s.release, nil
}

// Read reads the child's standard output.
func (s *execStream) Read(p []byte) (int, error) {
	return s.out.Read(p)
}

// Write writes to the child's standard input.
func (s *execStream) Write(p []byte) (int, error) {
	return s.in.Write(p)
}

// Close closes the child's standard input, which tells it to exit. The
// output stays readable until release.
func (s *execStream) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.in.Close() })
	return s.closeErr
}

// wait reaps the child and reports its exit result once.
func (s *execStream) wait() {
	s.exited <- s.cmd.Wait()
}

// release waits for the child to exit, killing it after the grace period,
// then closes the read end so a pending Read returns. It must be called once.
func (s *execStream) release() error {
	var result error
	timer := time.NewTimer(s.grace)
	defer timer.Stop()
	select {
	case err := <-s.exited:
		result = s.exitError(err)
	case <-timer.C:
		killErr := s.cmd.Process.Kill()
		err := <-s.exited
		if errors.Is(killErr, os.ErrProcessDone) {
			// The child had exited by itself; Wait was still collecting its
			// error output. Report its own exit.
			result = s.exitError(err)
		} else {
			result = errors.Join(
				fmt.Errorf("%s connect did not exit within %s of its input closing and was killed", s.program, s.grace),
				killErr)
		}
	}
	return errors.Join(result, s.out.Close())
}

// exitError describes a child that exited by itself with a failure. Output
// still held open by another process after the child exited is not one.
func (s *execStream) exitError(err error) error {
	if err == nil || errors.Is(err, exec.ErrWaitDelay) {
		return nil
	}
	if said := strings.TrimSpace(s.stderr.String()); said != "" {
		return fmt.Errorf("%s connect: %w: %s", s.program, err, said)
	}
	return fmt.Errorf("%s connect: %w", s.program, err)
}

// Write keeps the last limit bytes of everything written.
func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.data = append(t.data, p...)
	if extra := len(t.data) - t.limit; extra > 0 {
		t.data = append(t.data[:0], t.data[extra:]...)
	}
	return len(p), nil
}

// String returns what is kept.
func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.data)
}
