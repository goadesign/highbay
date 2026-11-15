// These cases preserve JSON meaning and exact source consumption during normalization.
package main

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestWatcherPrefixReaderPreservesJSONMeaning keeps string contents and invalid
// token separators intact while reducing only insignificant whitespace runs.
func TestWatcherPrefixReaderPreservesJSONMeaning(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		valid bool
	}{
		{"outside whitespace", " \t\r\n { \t\n\"values\" : [ 1, \n\r\t 2 ] } \t\n", true},
		{"quoted whitespace", `{"value": "two  spaces\tand\nnewlines"}`, true},
		{"odd escape", `{"value": "quote\"  stays quoted", "next":  true}`, true},
		{"even escapes", `{"value": "slash\\",   "next":  true}`, true},
		{"escaped slash and quote", `{"value": "slash\\\"  and spaces", "next":  true}`, true},
		{"unicode escape", `{"value": "quote\u0022  stays quoted", "next":  true}`, true},
		{"split true", `{"value": tr   ue}`, false},
		{"split false", `{"value": fal   se}`, false},
		{"split null", `{"value": nu   ll}`, false},
		{"split number", `{"value": 1   2}`, false},
		{"missing comma", `[1   2]`, false},
		{"raw string newline", "{\"value\": \"line\n  next\"}", false},
		{"raw string tab", "{\"value\": \"tab\t  next\"}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := io.ReadAll(&watcherPrefixReader{source: strings.NewReader(tc.input)})
			require.NoError(t, err)
			var original, result any
			originalErr := json.Unmarshal([]byte(tc.input), &original)
			resultErr := json.Unmarshal(normalized, &result)
			if !tc.valid {
				assert.Error(t, originalErr, "fixture must start malformed")
				assert.Error(t, resultErr, "normalization must not repair malformed JSON")
				return
			}
			require.NoError(t, originalErr)
			require.NoError(t, resultErr)
			assert.Equal(t, original, result)
		})
	}
}

// TestTeamWatcherPrefixStopsAfterWhitespaceAndAgentValues exercises long runs
// at token boundaries without allowing a read of the following conversation.
func TestTeamWatcherPrefixStopsAfterWhitespaceAndAgentValues(t *testing.T) {
	padding := strings.Repeat(" \t\r\n", 1024)
	for _, tc := range []struct {
		name   string
		prefix string
		held   map[string]bool
	}{
		{"leading", padding + `{"swarm":"team","agents":[]`, map[string]bool{}},
		{"empty array", `{ "swarm" : ` + padding + `"team", "agents" : [` + padding + `]`, map[string]bool{}},
		{"false and unregistered", `{"swarm":"team",` + padding + `"agents":[` + padding + `{"handle":"guide","watching_on_this_server":false},` + padding + `{"handle":"other","watching_on_this_server":true}` + padding + `]`, map[string]bool{"guide": false}},
		{"quoted metadata", `{"swarm":"team","agents":[{"handle":"guide","watching_on_this_server":true,"extra":{"text":"two  spaces and a quote\"  or slash\\","list":[` + padding + `1, 2]}}` + padding + `]`, map[string]bool{"guide": true}},
		{"escaped handle", `{"swarm":"team","agents":[{"handle":"\u0067uide","watching_on_this_server":true}` + padding + `]`, map[string]bool{"guide": true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := tc.prefix
			require.Less(t, len(prefix), teamViewPrefixMaxBytes)
			view, err, body := watcherPrefixObservation(t, prefix+` ,"channels":[{"text":"conversation suffix"}],"inboxes":[]}`, 200)
			require.NoError(t, err)
			assert.Equal(t, tc.held, view.Held)
			assert.Equal(t, `"version"`, view.Version)
			assert.Equal(t, len(prefix), body.bytes)
			assert.True(t, body.closed)
		})
	}
}
