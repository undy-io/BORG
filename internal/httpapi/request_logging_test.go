package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/undy-io/BORG/internal/auth"
	"github.com/undy-io/BORG/internal/proxy"
	"github.com/undy-io/BORG/internal/requestlog"
)

type handlerEventExporter struct {
	mu      sync.Mutex
	records []requestlog.Record
	reject  bool
	calls   int
}

func (e *handlerEventExporter) TryExport(record requestlog.Record) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
	if e.reject {
		return false
	}
	e.records = append(e.records, record)
	return true
}

func (e *handlerEventExporter) reset() {
	e.mu.Lock()
	e.records = nil
	e.calls = 0
	e.mu.Unlock()
}

func newHandlerRequestLogger(t *testing.T, exporter requestlog.EventExporter) *requestlog.Logger {
	return newFilteredHandlerRequestLogger(t, exporter, []requestlog.FilterConfig{{}})
}

func newFilteredHandlerRequestLogger(t *testing.T, exporter requestlog.EventExporter, filters []requestlog.FilterConfig) *requestlog.Logger {
	t.Helper()
	matcher, err := requestlog.CompileMatcher(filters)
	if err != nil {
		t.Fatal(err)
	}
	return requestlog.NewLogger(requestlog.Config{
		Sink: requestlog.SinkKafka,
		Capture: requestlog.CaptureConfig{
			RequestBody:             true,
			ResponseBody:            true,
			RequestHeaders:          true,
			ResponseHeaders:         true,
			ExcludedRequestHeaders:  map[string]struct{}{"authorization": {}},
			ExcludedResponseHeaders: map[string]struct{}{"set-cookie": {}},
			MaxRequestBodyBytes:     1024 * 1024,
			MaxResponseBodyBytes:    1024 * 1024,
		},
		SessionHeaders:  []requestlog.SessionHeader{{Name: "x-session-id", ValueMode: requestlog.ValueModeRaw}},
		PartitionHeader: "x-session-id",
		Matcher:         matcher,
	}, exporter, requestlog.WithInstanceID("borg-test"))
}

func newLoggedHandler(t *testing.T, proxyService *proxy.Service, exporter requestlog.EventExporter) *Handler {
	t.Helper()
	authenticator, err := auth.New("EMPTY", "")
	if err != nil {
		t.Fatal(err)
	}
	return New(authenticator, proxyService, WithRequestLogger(newHandlerRequestLogger(t, exporter)))
}

func TestHandlerRequestLoggingCapturesPrincipalAndExactBodies(t *testing.T) {
	handler, upstream, key := newTestHandler(t, true)
	defer upstream.Close()
	exporter := &handlerEventExporter{}
	handler.requestLogger = newHandlerRequestLogger(t, exporter)
	body := `{"model":"openai/gpt-oss-20b","messages":[{"role":"user","content":"hello"}]}`
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?trace=1", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+mintHTTPToken(t, key, "BORG:", "alice"))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Session-ID", "session-1")
	request.Header.Set("X-API-Key", "privileged-api-key")
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("unexpected status %d: %s", recorder.Code, recorder.Body.String())
	}

	events := decodeHandlerEvents(t, exporter.records)
	if len(events) < 7 {
		t.Fatalf("expected a complete event stream, got %d events", len(events))
	}
	var capturedRequest bytes.Buffer
	var capturedResponse bytes.Buffer
	var requestHeaders []requestlog.RequestHeader
	var started requestlog.RequestStarted
	var completed requestlog.RequestCompleted
	for _, raw := range events {
		var common requestlog.Common
		if err := json.Unmarshal(raw, &common); err != nil {
			t.Fatal(err)
		}
		if common.Principal != "alice" {
			t.Fatalf("principal handling is wrong: %s", raw)
		}
		switch common.EventType {
		case requestlog.EventRequestStarted:
			if err := json.Unmarshal(raw, &started); err != nil {
				t.Fatal(err)
			}
		case requestlog.EventRequestBodyChunk:
			var chunk requestlog.RequestBodyChunk
			if err := json.Unmarshal(raw, &chunk); err != nil {
				t.Fatal(err)
			}
			capturedRequest.Write(chunk.Payload)
		case requestlog.EventRequestHeader:
			var header requestlog.RequestHeader
			if err := json.Unmarshal(raw, &header); err != nil {
				t.Fatal(err)
			}
			requestHeaders = append(requestHeaders, header)
		case requestlog.EventResponseBodyChunk:
			var chunk requestlog.ResponseBodyChunk
			if err := json.Unmarshal(raw, &chunk); err != nil {
				t.Fatal(err)
			}
			capturedResponse.Write(chunk.Payload)
		case requestlog.EventRequestCompleted:
			if err := json.Unmarshal(raw, &completed); err != nil {
				t.Fatal(err)
			}
		}
	}
	if capturedRequest.String() != body || capturedResponse.String() != recorder.Body.String() {
		t.Fatalf("captured bytes differ: request=%q response=%q", capturedRequest.String(), capturedResponse.String())
	}
	var sawAPIKey bool
	for _, header := range requestHeaders {
		if header.HeaderName == "authorization" {
			t.Fatal("excluded Authorization header was exported")
		}
		if header.HeaderName == "x-api-key" && header.Value == "privileged-api-key" {
			sawAPIKey = true
		}
	}
	if !sawAPIKey {
		t.Fatalf("explicitly permitted sensitive header was not captured: %#v", requestHeaders)
	}
	if !strings.Contains(capturedResponse.String(), "Bearer sk-test") {
		t.Fatalf("opaque response body did not retain backend credential content: %q", capturedResponse.String())
	}
	if started.Path != "/v1/chat/completions" || strings.Contains(started.Path, "trace") {
		t.Fatalf("logged path contains query data: %q", started.Path)
	}
	var upstreamResponse map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &upstreamResponse); err != nil {
		t.Fatal(err)
	}
	if upstreamResponse["query"] != "trace=1" {
		t.Fatalf("expected query to be forwarded upstream, got %#v", upstreamResponse["query"])
	}
	if completed.Outcome != requestlog.OutcomeCompleted || completed.DownstreamStatus == nil || *completed.DownstreamStatus != http.StatusCreated || completed.AttemptCount != 1 {
		t.Fatalf("unexpected completion: %#v", completed)
	}
	if string(exporter.records[0].Key) == "session-1" || !strings.HasPrefix(string(exporter.records[0].Key), "sha256:") {
		t.Fatalf("raw partition value escaped hashing: %q", exporter.records[0].Key)
	}
}

func TestHandlerRequestLoggingDefaultDenyAndFilterMatch(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()
	exporter := &handlerEventExporter{}
	body := `{"model":"alpha"}`

	handler.requestLogger = newFilteredHandlerRequestLogger(t, exporter, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("X-Session-ID", "capture")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if exporter.calls != 0 || len(exporter.records) != 0 {
		t.Fatalf("default-deny filters emitted events: calls=%d records=%d", exporter.calls, len(exporter.records))
	}

	filters := []requestlog.FilterConfig{{
		Principals: []string{"^ANONYMOUS$"},
		Models:     []string{"^alpha$"},
		Headers:    map[string][]string{"X-Session-ID": {"^capture$"}},
	}}
	handler.requestLogger = newFilteredHandlerRequestLogger(t, exporter, filters)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if exporter.calls != 0 {
		t.Fatalf("header mismatch attempted %d exports", exporter.calls)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("X-Session-ID", "capture")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if exporter.calls == 0 || len(exporter.records) == 0 {
		t.Fatal("matching principal, model, and header did not emit events")
	}
}

func TestHandlerRequestLoggingMatchesAndCapturesRequestHost(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()
	exporter := &handlerEventExporter{}
	handler.requestLogger = newFilteredHandlerRequestLogger(t, exporter, []requestlog.FilterConfig{{
		Headers: map[string][]string{"Host": {`^borg\.internal:8443$`}},
	}})

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha"}`))
	request.Host = "borg.internal:8443"
	request.Header.Set("Host", "synthetic.invalid")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	var hosts []requestlog.RequestHeader
	for _, raw := range decodeHandlerEvents(t, exporter.records) {
		var common requestlog.Common
		if err := json.Unmarshal(raw, &common); err != nil {
			t.Fatal(err)
		}
		if common.EventType != requestlog.EventRequestHeader {
			continue
		}
		var header requestlog.RequestHeader
		if err := json.Unmarshal(raw, &header); err != nil {
			t.Fatal(err)
		}
		if header.HeaderName == "host" {
			hosts = append(hosts, header)
		}
	}
	if len(hosts) != 1 || hosts[0].Value != "borg.internal:8443" || hosts[0].ValueIndex != 0 {
		t.Fatalf("captured Host headers = %#v", hosts)
	}
}

func TestHandlerRequestLoggingUpstreamExhaustion(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	exporter := &handlerEventExporter{}
	handler.requestLogger = newHandlerRequestLogger(t, exporter)
	upstream.Close()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha"}`)))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", recorder.Code, recorder.Body.String())
	}
	completed := handlerCompletion(t, exporter.records)
	if completed.Outcome != requestlog.OutcomeUpstreamError || completed.DownstreamStatus == nil || *completed.DownstreamStatus != http.StatusBadGateway {
		t.Fatalf("unexpected exhaustion completion: %#v", completed)
	}
}

func TestHandlerRequestLoggingResponseHeaderTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer upstream.Close()
	proxyService := proxy.New(proxy.WithResponseHeaderTimeout(10 * time.Millisecond))
	proxyService.AddInstance(upstream.URL, "key", []string{"model"})
	exporter := &handlerEventExporter{}
	handler := newLoggedHandler(t, proxyService, exporter)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model"}`)))
	if recorder.Code != http.StatusGatewayTimeout {
		t.Fatalf("expected 504, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if result := handlerUpstreamResult(t, exporter.records); result.ResultKind != requestlog.ResultResponseHeaderTimeout {
		t.Fatalf("unexpected timeout result: %#v", result)
	}
	if completed := handlerCompletion(t, exporter.records); completed.Outcome != requestlog.OutcomeResponseHeaderTimeout {
		t.Fatalf("unexpected timeout completion: %#v", completed)
	}
}

func TestHandlerRequestLoggingClientCancellation(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()
	exporter := &handlerEventExporter{}
	handler.requestLogger = newHandlerRequestLogger(t, exporter)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha"}`)).WithContext(ctx)

	handler.ServeHTTP(httptest.NewRecorder(), request)
	if completed := handlerCompletion(t, exporter.records); completed.Outcome != requestlog.OutcomeClientCancelled {
		t.Fatalf("unexpected cancellation completion: %#v", completed)
	}
}

type partialHandlerWriter struct {
	header http.Header
	status int
	body   []byte
}

func (w *partialHandlerWriter) Header() http.Header { return w.header }
func (w *partialHandlerWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *partialHandlerWriter) Write(payload []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	count := min(2, len(payload))
	w.body = append(w.body, payload[:count]...)
	return count, errors.New("downstream write failed")
}

func TestHandlerRequestLoggingPartialDownstreamWrite(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()
	exporter := &handlerEventExporter{}
	handler.requestLogger = newHandlerRequestLogger(t, exporter)
	writer := &partialHandlerWriter{header: make(http.Header)}

	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha"}`)))
	completed := handlerCompletion(t, exporter.records)
	if completed.Outcome != requestlog.OutcomeClientWriteError || completed.ResponseBytes != 2 || string(writer.body) == "" {
		t.Fatalf("unexpected partial-write completion: %#v body=%q", completed, writer.body)
	}
}

func TestHandlerRequestLoggingQueueSaturationIsFailOpen(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()
	exporter := &handlerEventExporter{reject: true}
	handler.requestLogger = newHandlerRequestLogger(t, exporter)
	recorder := httptest.NewRecorder()

	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"alpha"}`)))
	if recorder.Code != http.StatusCreated || !strings.Contains(recorder.Body.String(), `"upstream":"ok"`) {
		t.Fatalf("saturation changed proxy response: %d %s", recorder.Code, recorder.Body.String())
	}
	if exporter.calls == 0 || len(exporter.records) != 0 {
		t.Fatalf("expected rejected logging attempts without records, got calls=%d records=%d", exporter.calls, len(exporter.records))
	}
}

func TestHandlerRequestLoggingSSEAndLocalError(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()
	exporter := &handlerEventExporter{}
	handler.requestLogger = newHandlerRequestLogger(t, exporter)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai/gpt-oss-20b","stream":true}`)))
	var captured bytes.Buffer
	for _, raw := range decodeHandlerEvents(t, exporter.records) {
		var common requestlog.Common
		_ = json.Unmarshal(raw, &common)
		if common.EventType == requestlog.EventResponseBodyChunk {
			var chunk requestlog.ResponseBodyChunk
			_ = json.Unmarshal(raw, &chunk)
			captured.Write(chunk.Payload)
		}
	}
	if captured.String() != recorder.Body.String() || !strings.Contains(captured.String(), "[DONE]") {
		t.Fatalf("SSE reconstruction differs: captured=%q response=%q", captured.String(), recorder.Body.String())
	}

	exporter.reset()
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing"}`)))
	events := decodeHandlerEvents(t, exporter.records)
	var completed requestlog.RequestCompleted
	if err := json.Unmarshal(events[len(events)-1], &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Outcome != requestlog.OutcomeUnknownModel || completed.DownstreamStatus == nil || *completed.DownstreamStatus != 404 {
		t.Fatalf("unexpected unknown-model completion: %#v", completed)
	}
}

func TestHandlerRequestLoggingExcludesMalformedRequests(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()
	exporter := &handlerEventExporter{}
	handler.requestLogger = newHandlerRequestLogger(t, exporter)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("not-json")))
	if len(exporter.records) != 0 {
		t.Fatalf("malformed request emitted %d events", len(exporter.records))
	}
}

func decodeHandlerEvents(t *testing.T, records []requestlog.Record) [][]byte {
	t.Helper()
	events := make([][]byte, len(records))
	for idx, record := range records {
		if !json.Valid(record.Value) {
			t.Fatalf("record %d is not valid JSON: %q", idx, record.Value)
		}
		events[idx] = record.Value
	}
	return events
}

func handlerCompletion(t *testing.T, records []requestlog.Record) requestlog.RequestCompleted {
	t.Helper()
	for _, raw := range decodeHandlerEvents(t, records) {
		var common requestlog.Common
		if err := json.Unmarshal(raw, &common); err != nil {
			t.Fatal(err)
		}
		if common.EventType == requestlog.EventRequestCompleted {
			var completed requestlog.RequestCompleted
			if err := json.Unmarshal(raw, &completed); err != nil {
				t.Fatal(err)
			}
			return completed
		}
	}
	t.Fatal("request.completed event not found")
	return requestlog.RequestCompleted{}
}

func handlerUpstreamResult(t *testing.T, records []requestlog.Record) requestlog.UpstreamResult {
	t.Helper()
	for _, raw := range decodeHandlerEvents(t, records) {
		var common requestlog.Common
		if err := json.Unmarshal(raw, &common); err != nil {
			t.Fatal(err)
		}
		if common.EventType == requestlog.EventUpstreamResult {
			var result requestlog.UpstreamResult
			if err := json.Unmarshal(raw, &result); err != nil {
				t.Fatal(err)
			}
			return result
		}
	}
	t.Fatal("upstream.result event not found")
	return requestlog.UpstreamResult{}
}
