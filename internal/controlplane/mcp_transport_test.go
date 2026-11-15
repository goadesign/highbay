// This file exercises transport failures through the mounted MCP server.
package controlplane

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gensurla "github.com/goadesign/surla/gen/surla"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"goa.design/goa-ai/runtime/mcp"
)

type progressService struct {
	gensurla.Service
	entered chan error
}

// GoldenPath emits progress before waiting for the disconnected client.
func (s progressService) GoldenPath(ctx context.Context) (string, error) {
	err := mcp.ReportProgress(ctx, 1, nil, nil)
	s.entered <- err
	<-ctx.Done()
	return "finished", nil
}

type failingBody struct{ readErr, closeErr error }

func (b failingBody) Read([]byte) (int, error) { return 0, b.readErr }
func (b failingBody) Close() error             { return b.closeErr }

type failingResponse struct {
	header http.Header
	writes int
}

func (w *failingResponse) Header() http.Header { return w.header }
func (w *failingResponse) WriteHeader(int)     {}
func (w *failingResponse) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("private-write-sentinel")
}

// TestMCPDisconnectSurvives verifies the real server survives a client
// disconnect while the progress stream is active.
func TestMCPDisconnectSurvives(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); assert.NoError(t, provider.Shutdown(context.Background())) })
	service := progressService{entered: make(chan error, 1)}
	handler := NewHTTPHandler(service, func(*http.Request) (Principal, error) { return Principal{ID: "test", Kind: CallerGuide}, nil })
	done := make(chan struct{}, 1)
	var serverLog bytes.Buffer
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, parent := provider.Tracer("test").Start(r.Context(), "parent")
		defer func() { parent.End(); done <- struct{}{} }()
		handler.ServeHTTP(w, r.WithContext(ctx))
	}))
	server.Config.ErrorLog = log.New(&serverLog, "", 0)
	server.Start()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req := mcpTransportRequest(t, ctx, server.URL+"/mcp", `{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"surla://golden-path","_meta":{"progressToken":"test","io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.True(t, strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream"))
	line, err := bufio.NewReader(resp.Body).ReadString('\n')
	require.NoError(t, err)
	assert.NotEmpty(t, line)
	select {
	case err := <-service.entered:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("progress did not start")
	}
	require.NoError(t, resp.Body.Close())
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not finish")
	}
	spans := recorder.Ended()
	var failure, parent sdktrace.ReadOnlySpan
	for _, span := range spans {
		if span.Name() == "surla.mcp.transport_error" {
			failure = span
		}
		if span.Name() == "parent" {
			parent = span
		}
	}
	require.NotNil(t, parent)
	assert.NotEqual(t, codes.Error, parent.Status().Code)
	// Cancellation and the final socket write race. Check a reported failure
	// when one occurs; the controlled writer below proves the error path.
	if failure != nil {
		assert.Equal(t, parent.SpanContext().SpanID(), failure.Parent().SpanID())
		assert.Equal(t, codes.Error, failure.Status().Code)
		assert.Equal(t, "MCP transport failure", failure.Status().Description)
		require.NotEmpty(t, failure.Events())
		assert.Equal(t, "exception", failure.Events()[0].Name)
	}

	// A separate request remains usable after the disconnected stream.
	successCtx, successCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer successCancel()
	success := mcpTransportRequest(t, successCtx, server.URL+"/mcp", `{"jsonrpc":"2.0","id":2,"method":"resources/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
	result, err := server.Client().Do(success)
	require.NoError(t, err)
	body, err := io.ReadAll(result.Body)
	require.NoError(t, err)
	require.NoError(t, result.Body.Close())
	assert.Equal(t, http.StatusOK, result.StatusCode)
	assert.Contains(t, string(body), `"id":2`)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("success request did not finish")
	}
	server.Close()
	assert.NotContains(t, serverLog.String(), "panic")
}

// TestMCPGuardFailures records actual read, close, and protocol write errors
// without attempting another write; ordinary refusals do not create transport errors.
// The generated guard's post-validation unmarshal and legacy marshal failures
// are invariant failures and cannot be induced by an honest request.
func TestMCPGuardFailures(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { otel.SetTracerProvider(previous); assert.NoError(t, provider.Shutdown(context.Background())) })
	handler := NewHTTPHandler(progressService{}, func(*http.Request) (Principal, error) { return Principal{ID: "test", Kind: CallerGuide}, nil })
	sentinel := errors.New("private-body-sentinel")
	for _, tc := range []struct {
		name string
		body io.ReadCloser
	}{
		{"read", failingBody{readErr: sentinel}},
		{"close", failingBody{readErr: io.EOF, closeErr: sentinel}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := mcpTransportRequest(t, context.Background(), "/mcp", "")
			req.Body = tc.body
			writer := &failingResponse{header: make(http.Header)}
			handler.ServeHTTP(writer, req)
			assert.Zero(t, writer.writes)
		})
	}
	req := mcpTransportRequest(t, context.Background(), "/mcp", `{`)
	writer := &failingResponse{header: make(http.Header)}
	handler.ServeHTTP(writer, req)
	assert.Equal(t, 1, writer.writes)
	// JSON encodes directly; SSE buffers then fails the transport's final write.
	for _, accept := range []string{"application/json", "application/json, text/event-stream"} {
		accepted := mcpTransportRequest(t, context.Background(), "/mcp", `{"jsonrpc":"2.0","id":9,"method":"resources/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`)
		accepted.Header.Set("Accept", accept)
		failedAccepted := &failingResponse{header: make(http.Header)}
		handler.ServeHTTP(failedAccepted, accepted)
		assert.Equal(t, 1, failedAccepted.writes, accept)
	}
	var spans []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "surla.mcp.transport_error" {
			spans = append(spans, span)
		}
	}
	require.Len(t, spans, 5)
	for _, span := range spans {
		assert.Equal(t, "surla.mcp.transport_error", span.Name())
		assert.Equal(t, codes.Error, span.Status().Code)
		assert.Equal(t, "MCP transport failure", span.Status().Description)
		assert.NotContains(t, fmt.Sprint(span.Attributes()), "private-body-sentinel")
		assert.NotContains(t, fmt.Sprint(span.Attributes()), "private-write-sentinel")
		assert.NotEmpty(t, span.Events())
	}
	for _, tc := range []struct {
		name, body string
		code       int
		contains   string
	}{
		{"malformed", `{`, http.StatusBadRequest, `"error"`},
		{"unknown method", `{"jsonrpc":"2.0","id":7,"method":"unknown","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, http.StatusNotFound, `"id":7`},
		{"success", `{"jsonrpc":"2.0","id":8,"method":"resources/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, http.StatusOK, `"id":8`},
		{"accepted refusal", `{"jsonrpc":"2.0","id":10,"method":"resources/read","params":{"uri":"surla://missing","_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}`, http.StatusBadRequest, `"id":10`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(recorder.Ended())
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, mcpTransportRequest(t, context.Background(), "/mcp", tc.body))
			assert.Equal(t, tc.code, response.Code)
			assert.Contains(t, response.Body.String(), tc.contains)
			newSpans := recorder.Ended()[before:]
			for _, span := range newSpans {
				assert.NotEqual(t, "surla.mcp.transport_error", span.Name())
			}
		})
	}
	t.Run("injected provider", func(t *testing.T) {
		selected := tracetest.NewSpanRecorder()
		provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(selected))
		t.Cleanup(func() { assert.NoError(t, provider.Shutdown(context.Background())) })
		cp := newControlPlane(t, Options{TracerProvider: provider})
		ownedHandler := NewHTTPHandler(cp, func(*http.Request) (Principal, error) {
			return Principal{ID: "test", CallerID: "guide", Kind: CallerGuide}, nil
		})
		ctx, parent := provider.Tracer("test").Start(context.Background(), "upstream")
		request := mcpTransportRequest(t, ctx, "/mcp", "")
		request.Body = failingBody{readErr: sentinel}
		ownedHandler.ServeHTTP(httptest.NewRecorder(), request)
		parent.End()
		spans := selected.Ended()
		require.Len(t, spans, 2)
		assert.Equal(t, "surla.mcp.transport_error", spans[0].Name())
		assert.Equal(t, codes.Error, spans[0].Status().Code)
		require.NotEmpty(t, spans[0].Events())
		assert.Equal(t, spans[1].SpanContext().SpanID(), spans[0].Parent().SpanID())
		assert.NotEqual(t, codes.Error, spans[1].Status().Code)
	})

}

// mcpTransportRequest constructs the accepted HTTP transport envelope.
func mcpTransportRequest(t *testing.T, ctx context.Context, url, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", "2026-07-28")
	var envelope struct {
		Method string `json:"method"`
		Params struct {
			URI string `json:"uri"`
		} `json:"params"`
	}
	if json.Unmarshal([]byte(body), &envelope) == nil {
		req.Header.Set("Mcp-Method", envelope.Method)
		if envelope.Params.URI != "" {
			req.Header.Set("Mcp-Name", envelope.Params.URI)
		}
	}
	return req
}
