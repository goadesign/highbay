// Command testcheck forwards go test -json output and rejects an invocation
// that selected no named tests, examples, or fuzz tests across its packages.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
)

// testEvent contains only the fields that establish actual test selection.
// Package-level events and output text cannot establish selection.
type testEvent struct {
	Action string
	Test   string
}

// main leaves test failures to the caller's pipefail and rejects invalid or
// empty selection independently, with diagnostics on stderr.
func main() {
	if err := check(os.Stdin, os.Stdout); err != nil {
		log.New(os.Stderr, "testcheck: ", 0).Fatal(err)
	}
}

// check preserves original output bytes while inspecting one JSON event at a
// time. It accepts testless packages when another package selects a named test.
// On malformed input it forwards the unread remainder before returning failure.
func check(input io.Reader, output io.Writer) error {
	reader := bufio.NewReader(input)
	selected := false
	for {
		record, readErr := reader.ReadBytes('\n')
		if len(record) > 0 {
			n, err := output.Write(record)
			if err != nil {
				return fmt.Errorf("forward test output: %w", err)
			}
			if n != len(record) {
				return fmt.Errorf("forward test output: %w", io.ErrShortWrite)
			}
			if len(bytes.TrimSpace(record)) > 0 {
				var event testEvent
				if err := json.Unmarshal(record, &event); err != nil {
					// Diagnostics after the bad record remain visible to CI.
					_, drainErr := io.Copy(output, reader)
					return errors.Join(
						fmt.Errorf("decode go test JSON event: %w", err),
						readFailure(readErr),
						forwardFailure(drainErr),
					)
				}
				if event.Test != "" {
					switch event.Action {
					case "run", "pass", "skip", "fail":
						selected = true
					}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readFailure(readErr)
		}
	}
	if !selected {
		return errors.New("go test selected no named tests, examples, or fuzz tests")
	}
	return nil
}

// readFailure distinguishes a clean stream end from lost input.
func readFailure(err error) error {
	if err == nil || errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("read test output: %w", err)
}

// forwardFailure reports a failed drain without hiding the original parse error.
func forwardFailure(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("forward remaining test output: %w", err)
}
