// These tests own the CI selection guard's parsing and byte-forwarding contract.
package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusalReader returns a stable failure after its supplied bytes are consumed.
type refusalReader struct {
	io.Reader
	err error
}

// refusalWriter fails the requested write, including drains after parse errors.
type refusalWriter struct {
	buffer           bytes.Buffer
	writes, refuseAt int
	err              error
}

// shortWriter violates the Writer contract without returning an error.
type shortWriter struct{}

// TestCheckSelection distinguishes named lifecycle events from package results
// and output text, preserving exact bytes even after malformed input.
func TestCheckSelection(t *testing.T) {
	for _, tc := range []struct {
		name, input, wantError string
	}{
		{"test", "{\"Action\":\"run\",\"Test\":\"TestSelected\"}\n", ""},
		{"example", "{\"Action\":\"pass\",\"Test\":\"ExampleSelected\"}\n", ""},
		{"fuzz seed", "{\"Action\":\"run\",\"Test\":\"FuzzSelected/seed#0\"}\n", ""},
		{"skip", "{\"Action\":\"skip\",\"Test\":\"TestSelected\"}\n", ""},
		{"fail", "{\"Action\":\"fail\",\"Test\":\"TestSelected\"}\n", ""},
		{"mixed packages", "{\"Action\":\"skip\",\"Package\":\"testless\"}\n" +
			"{\"Action\":\"run\",\"Package\":\"tested\",\"Test\":\"TestSelected\"}\n" +
			"{\"Action\":\"pass\",\"Package\":\"tested\"}\n", ""},
		{"no final newline", "{\"Action\":\"run\",\"Test\":\"TestSelected\"}", ""},
		{"empty", "", "selected no named"},
		{"testless", "{\"Action\":\"skip\",\"Package\":\"testless\"}\n", "selected no named"},
		{"warning", "{\"Action\":\"output\",\"Output\":\"testing: warning: no tests to run\\n\"}\n" +
			"{\"Action\":\"pass\",\"Package\":\"tested\"}\n", "selected no named"},
		{"named output only", "{\"Action\":\"output\",\"Test\":\"TestSelected\"}\n", "selected no named"},
		{"malformed with remainder", "not json\noriginal diagnostics\n", "decode go test JSON event"},
		{"truncated", "{\"Action\":\"run\",\"Test\":", "decode go test JSON event"},
		{"malformed after selection", "{\"Action\":\"run\",\"Test\":\"TestSelected\"}\n{\nrest\n", "decode go test JSON event"},
		{"wrong field type", "{\"Action\":false}\n", "decode go test JSON event"},
		{"large output event", "{\"Action\":\"output\",\"Output\":\"" + strings.Repeat("x", 128*1024) +
			"\"}\n{\"Action\":\"run\",\"Test\":\"TestSelected\"}\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			err := check(strings.NewReader(tc.input), &output)
			if tc.wantError == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tc.wantError)
			}
			assert.Equal(t, tc.input, output.String())
		})
	}
}

// TestCheckIOFailures keeps every input/output refusal fatal, even when a named
// test appeared earlier or malformed input requires forwarding the remainder.
func TestCheckIOFailures(t *testing.T) {
	refused := errors.New("refused")
	for _, tc := range []struct {
		name, input string
		readFailure bool
		refuseAt    int
		want        string
	}{
		{"read empty", "", true, 0, "read test output"},
		{"read after selection", "{\"Action\":\"run\",\"Test\":\"TestSelected\"}\n", true, 0, "read test output"},
		{"read partial record", "{", true, 0, "read test output"},
		{"write", "{}\n", false, 1, "forward test output"},
		{"drain write", "bad\nremainder\n", false, 2, "forward remaining test output"},
		{"drain read", "bad\nremainder\n", true, 0, "forward remaining test output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var input io.Reader = strings.NewReader(tc.input)
			if tc.readFailure {
				input = &refusalReader{Reader: input, err: refused}
			}
			output := &refusalWriter{refuseAt: tc.refuseAt, err: refused}
			err := check(input, output)
			assert.ErrorContains(t, err, tc.want)
			assert.ErrorIs(t, err, refused)
			if tc.refuseAt == 0 {
				assert.Equal(t, tc.input, output.buffer.String())
			}
		})
	}
	assert.ErrorIs(t, check(strings.NewReader("{}\n"), shortWriter{}), io.ErrShortWrite)
}

// TestCheckRejectsRenamedTest exercises the real Go CLI seam: go test exits
// successfully when -run names a removed test. One tiny standard-library-only
// fixture build is needed to prove that behavior; it reuses the default/shared
// Go cache, bounds execution, and creates only mutable scratch in t.TempDir.
func TestCheckRejectsRenamedTest(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module testcheckfixture\n\ngo 1.27.3\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "rename_test.go"), []byte(
		"package fixture\nimport \"testing\"\nfunc TestAfterRename(t *testing.T) {}\n"), 0o600))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "test", "-json", "-count=1", "-run", "^TestBeforeRename$", ".")
	command.Dir = dir
	command.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	require.NoError(t, err, "Go should accept the stale selector: %s", stderr.String())
	var forwarded bytes.Buffer
	assert.ErrorContains(t, check(bytes.NewReader(output), &forwarded), "selected no named")
	assert.Equal(t, output, forwarded.Bytes())
}

// Read reports the refusal only after the supplied bytes, as an interrupted
// command can emit complete or partial records before its pipe fails.
func (reader *refusalReader) Read(data []byte) (int, error) {
	n, err := reader.Reader.Read(data)
	if errors.Is(err, io.EOF) {
		return n, reader.err
	}
	return n, err
}

// Write can fail a later drain independently of the first forwarded record.
func (writer *refusalWriter) Write(data []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.refuseAt {
		return 0, writer.err
	}
	return writer.buffer.Write(data)
}

// Write simulates a silent partial write that must still fail the guard.
func (shortWriter) Write(data []byte) (int, error) {
	return len(data) / 2, nil
}
