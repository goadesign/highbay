// This file checks explicit choices at the generated transport and team boundary.
package controlplane

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTeamChoicesUseTheGeneratedContract proves choices reach their owner and
// invalid keys or required fields fail before a team effect.
func TestTeamChoicesUseTheGeneratedContract(t *testing.T) {
	f := &teamServiceFixture{}
	cp := newControlPlane(t, Options{Team: &TeamBinding{Owner: "owner", Service: f}})
	handler := NewHTTPHandler(cp, func(*http.Request) (Principal, error) { return Principal{ID: "owner", Kind: CallerUser}, nil })
	valid := `{"choices":{"builder":{"client":"codex","program":"/fixture/codex","yolo":false,"model":"fixture-model","thinking":"high"}}}`
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/team/agents", strings.NewReader(valid)))
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.Contains(t, f.choices, "builder")
	assert.Equal(t, "fixture-model", *f.choices["builder"].Model)
	assert.Equal(t, "high", *f.choices["builder"].Thinking)
	assert.False(t, f.choices["builder"].Yolo)
	for _, body := range []string{
		`{"choices":{"other":{"client":"codex","program":"/fixture/codex","yolo":false}}}`,
		`{"choices":{"builder":{"client":"codex","program":"/fixture/codex"}}}`,
	} {
		before := len(f.calls)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/team/agents", strings.NewReader(body)))
		assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		assert.Len(t, f.calls, before)
	}
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/changes/change/build", strings.NewReader(`{"request_revision":"revision","choices":{"guide":{"client":"codex","program":"/fixture/codex","yolo":false}}}`)))
	assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "guide")
}
