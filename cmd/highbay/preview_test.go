package main

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genhighbay "github.com/goadesign/highbay/gen/highbay"
	"github.com/goadesign/highbay/internal/designeval"
)

// TestWritePreviewResultKeepsJSONOnStdout verifies highbay preview's output
// contract: stdout holds only the indented JSON result, which decodes back to
// the same value with nothing after it, and stderr holds one summary line,
// which stays one line even when the system name contains a newline.
func TestWritePreviewResultKeepsJSONOnStdout(t *testing.T) {
	cases := []struct {
		name       string
		systemName string
		wantStderr string
	}{
		{
			name:       "plain name",
			systemName: "orders",
			wantStderr: "highbay: \"orders\": 1 container(s), 0 boundary edge(s), 0 agent policy(s), 1 tool contract(s)\n",
		},
		{
			name:       "name with a newline",
			systemName: "orders\nhighbay: forged",
			wantStderr: "highbay: \"orders\\nhighbay: forged\": 1 container(s), 0 boundary edge(s), 0 agent policy(s), 1 tool contract(s)\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := designeval.Result{
				SystemName:   tc.systemName,
				Declarations: map[string]string{},
				Containers:   []*genhighbay.C4ContainerItem{{ID: "orders"}},
				Tools:        []*genhighbay.ToolContractItem{{Name: "price"}},
			}
			var stdout, stderr bytes.Buffer
			require.NoError(t, writePreviewResult(&stdout, &stderr, res))

			dec := json.NewDecoder(&stdout)
			var got designeval.Result
			require.NoError(t, dec.Decode(&got))
			assert.Equal(t, res, got)
			var extra json.RawMessage
			assert.ErrorIs(t, dec.Decode(&extra), io.EOF, "stdout holds nothing after the JSON")
			assert.Equal(t, tc.wantStderr, stderr.String())
		})
	}
}
