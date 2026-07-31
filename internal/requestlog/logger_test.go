package requestlog

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/undy-io/BORG/internal/proxy"
)

type collectingExporter struct {
	mu         sync.Mutex
	records    []Record
	rejectAt   map[int]struct{}
	calls      int
	localDrops map[LocalDropReason]int
}

func (e *collectingExporter) RecordLocalDrop(reason LocalDropReason) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.localDrops == nil {
		e.localDrops = make(map[LocalDropReason]int)
	}
	e.localDrops[reason]++
}

func (e *collectingExporter) TryExport(record Record) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	call := e.calls
	e.calls++
	if _, reject := e.rejectAt[call]; reject {
		return false
	}
	e.records = append(e.records, record)
	return true
}

func captureAllConfig(t *testing.T) Config {
	t.Helper()
	matcher, err := CompileMatcher([]FilterConfig{{}})
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Sink: SinkKafka,
		Capture: CaptureConfig{
			RequestBody:          true,
			ResponseBody:         true,
			MaxRequestBodyBytes:  DefaultMaxRequestBodyBytes,
			MaxResponseBodyBytes: DefaultMaxResponseBodyBytes,
		},
		Matcher: matcher,
	}
}

func TestLoggerUnmatchedDoesNotExport(t *testing.T) {
	matcher, err := CompileMatcher(nil)
	if err != nil {
		t.Fatal(err)
	}
	exporter := &collectingExporter{}
	logger := NewLogger(Config{Sink: SinkKafka, Matcher: matcher}, exporter)
	if recorder := logger.Start(StartInput{Principal: "ANONYMOUS", Model: "m"}); recorder != nil {
		t.Fatal("unmatched request returned a recorder")
	}
	if exporter.calls != 0 {
		t.Fatalf("unmatched request attempted %d exports", exporter.calls)
	}
}

func TestNoopAndUnmatchedSelectionAllocateNothing(t *testing.T) {
	input := StartInput{Principal: "ANONYMOUS", Model: "model", Headers: http.Header{"Authorization": {"Bearer sensitive"}}}
	noop := NewLogger(Config{Sink: SinkNoop}, nil)
	if allocations := testing.AllocsPerRun(1000, func() { _ = noop.Start(input) }); allocations != 0 {
		t.Fatalf("noop selection allocated %.2f objects", allocations)
	}
	matcher, err := CompileMatcher(nil)
	if err != nil {
		t.Fatal(err)
	}
	unmatched := NewLogger(Config{Sink: SinkKafka, Matcher: matcher, Capture: CaptureConfig{RequestHeaders: true}}, &collectingExporter{})
	if allocations := testing.AllocsPerRun(1000, func() { _ = unmatched.Start(input) }); allocations != 0 {
		t.Fatalf("unmatched selection allocated %.2f objects", allocations)
	}
}

func TestDroppedHeaderEventAdvancesSequenceAndCompletionCount(t *testing.T) {
	config := captureAllConfig(t)
	config.Capture.RequestHeaders = true
	config.Capture.RequestBody = false
	exporter := &collectingExporter{rejectAt: map[int]struct{}{1: {}}}
	logger := NewLogger(config, exporter)
	recorder := logger.Start(StartInput{
		Principal: "p",
		Model:     "m",
		Headers:   http.Header{"X-Session": {"one"}},
		Method:    http.MethodPost,
	})
	recorder.Complete(CompletionInput{})
	if len(exporter.records) != 2 {
		t.Fatalf("expected start and completion, got %d records", len(exporter.records))
	}
	var completed RequestCompleted
	if err := json.Unmarshal(exporter.records[1].Value, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Sequence != 2 || completed.EventsDropped != 1 {
		t.Fatalf("header drop was not represented by a sequence gap: %#v", completed)
	}
}

func TestRecorderSequenceGapsAndCompletionDropCount(t *testing.T) {
	base := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	now := base
	exporter := &collectingExporter{rejectAt: map[int]struct{}{1: {}}}
	logger := NewLogger(captureAllConfig(t), exporter, WithInstanceID("borg-0"), WithClock(func() time.Time { return now }))
	recorder := logger.Start(StartInput{
		Started:   base,
		Principal: "ANONYMOUS",
		Model:     "m",
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
		Body:      []byte("body"),
	})
	if recorder == nil {
		t.Fatal("capture-all did not return a recorder")
	}
	now = base.Add(10 * time.Millisecond)
	recorder.Complete(CompletionInput{Forward: proxy.ForwardResult{UnknownModel: true}, DownstreamStatus: 404})

	if len(exporter.records) != 2 {
		t.Fatalf("expected start and completion records, got %d", len(exporter.records))
	}
	var completed RequestCompleted
	if err := json.Unmarshal(exporter.records[1].Value, &completed); err != nil {
		t.Fatal(err)
	}
	if completed.Sequence != 2 || completed.EventID != completed.RequestID+":2" || completed.EventsDropped != 1 {
		t.Fatalf("sequence gap was not visible: %#v", completed)
	}
	if completed.Outcome != OutcomeUnknownModel || completed.TotalDurationMS != 10 {
		t.Fatalf("unexpected completion: %#v", completed)
	}
}

func TestRecorderReportsAllOversizedEvents(t *testing.T) {
	config := captureAllConfig(t)
	headers := make(http.Header)
	for index := range 70 {
		name := fmt.Sprintf("x-session-%d", index)
		config.SessionHeaders = append(config.SessionHeaders, SessionHeader{Name: name, ValueMode: ValueModeRaw})
		headers.Set(name, strings.Repeat("x", MaxMetadataValueBytes))
	}
	exporter := &collectingExporter{}
	logger := NewLogger(config, exporter)
	recorder := logger.Start(StartInput{
		Principal: "principal",
		Model:     "model",
		Headers:   headers,
		Method:    http.MethodPost,
		Path:      "/v1/chat/completions",
	})
	if recorder == nil {
		t.Fatal("capture-all did not return a recorder")
	}
	recorder.Complete(CompletionInput{})

	if exporter.calls != 0 || len(exporter.records) != 0 {
		t.Fatalf("oversized events reached queue admission: calls=%d records=%d", exporter.calls, len(exporter.records))
	}
	if exporter.localDrops[LocalDropEventTooLarge] != 2 {
		t.Fatalf("expected both oversized events to be reported, got %#v", exporter.localDrops)
	}
	if recorder.sequence != 2 || recorder.dropped != 2 {
		t.Fatalf("expected two visible sequence drops, got sequence=%d dropped=%d", recorder.sequence, recorder.dropped)
	}
}

func TestRecorderReportsEncodingFailures(t *testing.T) {
	invalidJSONTime := time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC)
	exporter := &collectingExporter{}
	logger := NewLogger(captureAllConfig(t), exporter, WithClock(func() time.Time { return invalidJSONTime }))
	recorder := logger.Start(StartInput{Principal: "principal", Model: "model", Method: http.MethodPost})
	if recorder == nil {
		t.Fatal("capture-all did not return a recorder")
	}
	recorder.Complete(CompletionInput{})

	if exporter.calls != 0 || len(exporter.records) != 0 {
		t.Fatalf("unencodable events reached queue admission: calls=%d records=%d", exporter.calls, len(exporter.records))
	}
	if exporter.localDrops[LocalDropEventEncodingFailed] != 2 {
		t.Fatalf("expected both encoding failures to be reported, got %#v", exporter.localDrops)
	}
	if recorder.sequence != 2 || recorder.dropped != 2 {
		t.Fatalf("expected two visible sequence drops, got sequence=%d dropped=%d", recorder.sequence, recorder.dropped)
	}
}

func TestCompletionPrecedence(t *testing.T) {
	tests := []struct {
		name  string
		input CompletionInput
		want  Outcome
	}{
		{name: "client write", input: CompletionInput{ClientWriteError: true, ClientCancelled: true, Forward: proxy.ForwardResult{ResponseHeaderTimeout: true, UpstreamError: true, UnknownModel: true}}, want: OutcomeClientWriteError},
		{name: "cancelled", input: CompletionInput{ClientCancelled: true, Forward: proxy.ForwardResult{ResponseHeaderTimeout: true, UpstreamError: true, UnknownModel: true}}, want: OutcomeClientCancelled},
		{name: "timeout", input: CompletionInput{Forward: proxy.ForwardResult{ResponseHeaderTimeout: true, UpstreamError: true, UnknownModel: true}}, want: OutcomeResponseHeaderTimeout},
		{name: "upstream", input: CompletionInput{Forward: proxy.ForwardResult{UpstreamError: true, UnknownModel: true}}, want: OutcomeUpstreamError},
		{name: "internal", input: CompletionInput{Err: errors.New("unexpected")}, want: OutcomeInternalError},
		{name: "unknown", input: CompletionInput{Forward: proxy.ForwardResult{UnknownModel: true}}, want: OutcomeUnknownModel},
		{name: "completed status", input: CompletionInput{Forward: proxy.ForwardResult{DownstreamResponse: true}, DownstreamStatus: 503}, want: OutcomeCompleted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			exporter := &collectingExporter{}
			logger := NewLogger(captureAllConfig(t), exporter, WithClock(time.Now))
			recorder := logger.Start(StartInput{Started: time.Now(), Principal: "p", Model: "m", Method: "POST"})
			recorder.Complete(test.input)
			var completed RequestCompleted
			if err := json.Unmarshal(exporter.records[len(exporter.records)-1].Value, &completed); err != nil {
				t.Fatal(err)
			}
			if completed.Outcome != test.want {
				t.Fatalf("outcome = %q, want %q", completed.Outcome, test.want)
			}
		})
	}
}

type partialWriter struct {
	header   http.Header
	body     []byte
	statuses []int
}

func (w *partialWriter) Header() http.Header { return w.header }
func (w *partialWriter) WriteHeader(status int) {
	w.statuses = append(w.statuses, status)
}
func (w *partialWriter) Write(payload []byte) (int, error) {
	w.body = append(w.body, payload[:2]...)
	return 2, errors.New("client disconnected")
}

func TestResponseWriterRecordsOnlyAcceptedBytes(t *testing.T) {
	exporter := &collectingExporter{}
	logger := NewLogger(captureAllConfig(t), exporter)
	recorder := logger.Start(StartInput{Started: time.Now(), Principal: "p", Model: "m", Method: "POST"})
	underlying := &partialWriter{header: make(http.Header)}
	writer := NewResponseWriter(underlying, recorder)
	n, err := writer.Write([]byte("abcdef"))
	if n != 2 || err == nil {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	status, writeErr := writer.Snapshot()
	if status != 200 || !writeErr {
		t.Fatalf("unexpected writer snapshot: %d %v", status, writeErr)
	}
	var chunks []ResponseBodyChunk
	for _, record := range exporter.records {
		var envelope Common
		if err := json.Unmarshal(record.Value, &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.EventType == EventResponseBodyChunk {
			var chunk ResponseBodyChunk
			if err := json.Unmarshal(record.Value, &chunk); err != nil {
				t.Fatal(err)
			}
			chunks = append(chunks, chunk)
		}
	}
	if len(chunks) != 1 || string(chunks[0].Payload) != "ab" || chunks[0].ByteCount != 2 {
		t.Fatalf("unexpected captured chunks: %#v", chunks)
	}
}

func TestResponseWriterUnsupportedFlushIsNotClientFailure(t *testing.T) {
	exporter := &collectingExporter{}
	logger := NewLogger(captureAllConfig(t), exporter)
	recorder := logger.Start(StartInput{Principal: "p", Model: "m", Method: "POST"})
	writer := NewResponseWriter(&partialWriter{header: make(http.Header)}, recorder)
	writer.Flush()
	status, writeErr := writer.Snapshot()
	if status != http.StatusOK || writeErr {
		t.Fatalf("unsupported optional flush was classified as a client failure: %d %v", status, writeErr)
	}
}

func TestRequestHeaderCaptureIsDeterministicAndIndependent(t *testing.T) {
	config := captureAllConfig(t)
	config.Capture.RequestHeaders = true
	config.Capture.ExcludedRequestHeaders = map[string]struct{}{"authorization": {}}
	config.SessionHeaders = []SessionHeader{{Name: "authorization", ValueMode: ValueModeRaw}}
	config.PartitionHeader = "x-partition-only"
	config.Matcher, _ = CompileMatcher([]FilterConfig{{Headers: map[string][]string{"X-Filter-Only": {"^allowed$"}}}})

	exporter := &collectingExporter{}
	logger := NewLogger(config, exporter)
	headers := http.Header{
		"Authorization":    {"Bearer privileged"},
		"X-Partition-Only": {"partition-value"},
		"X-Filter-Only":    {"allowed"},
		"X-Repeated":       {"one", "two"},
	}
	recorder := logger.Start(StartInput{Principal: "p", Model: "m", Headers: headers, Method: http.MethodPost})
	if recorder == nil {
		t.Fatal("header-only filter did not match")
	}

	var names []string
	var indexes []int
	for _, record := range exporter.records {
		var common Common
		if err := json.Unmarshal(record.Value, &common); err != nil {
			t.Fatal(err)
		}
		if common.EventType == EventRequestStarted {
			if got := common.SessionHeaders["authorization"]; len(got) != 1 || got[0].Value != "Bearer privileged" {
				t.Fatalf("explicit session metadata was not retained: %#v", common.SessionHeaders)
			}
		}
		if common.EventType != EventRequestHeader {
			continue
		}
		var event RequestHeader
		if err := json.Unmarshal(record.Value, &event); err != nil {
			t.Fatal(err)
		}
		names = append(names, event.HeaderName)
		indexes = append(indexes, event.ValueIndex)
		if event.HeaderName == "authorization" {
			t.Fatal("excluded header reached generic capture")
		}
	}
	wantNames := []string{"x-filter-only", "x-partition-only", "x-repeated", "x-repeated"}
	wantIndexes := []int{0, 0, 0, 1}
	if !reflect.DeepEqual(names, wantNames) || !reflect.DeepEqual(indexes, wantIndexes) {
		t.Fatalf("captured headers = %v %v, want %v %v", names, indexes, wantNames, wantIndexes)
	}
	if len(exporter.records) == 0 || string(exporter.records[0].Key) == recorder.requestID {
		t.Fatal("undeclared partition header did not produce a hashed key")
	}
}

func TestResponseWriterCapturesFirstFinalResponseHeaders(t *testing.T) {
	config := captureAllConfig(t)
	config.Capture.ResponseHeaders = true
	config.Capture.ExcludedResponseHeaders = map[string]struct{}{"set-cookie": {}}
	exporter := &collectingExporter{}
	logger := NewLogger(config, exporter)
	recorder := logger.Start(StartInput{Principal: "p", Model: "m", Method: http.MethodPost})
	underlying := &partialWriter{header: http.Header{
		"Authorization": {"Bearer response-privileged"},
		"Content-Type":  {"application/json"},
		"Set-Cookie":    {"session=secret"},
		"X-Trace":       {"one", "two"},
	}}
	writer := NewResponseWriter(underlying, recorder)
	writer.WriteHeader(http.StatusEarlyHints)
	writer.WriteHeader(http.StatusCreated)
	writer.WriteHeader(http.StatusAccepted)

	status, writeErr := writer.Snapshot()
	if status != http.StatusCreated || writeErr || !reflect.DeepEqual(underlying.statuses, []int{http.StatusEarlyHints, http.StatusCreated}) {
		t.Fatalf("unexpected final status handling: status=%d error=%v writes=%v", status, writeErr, underlying.statuses)
	}
	var eventTypes []EventType
	var headers []ResponseHeader
	for _, record := range exporter.records {
		var common Common
		if err := json.Unmarshal(record.Value, &common); err != nil {
			t.Fatal(err)
		}
		eventTypes = append(eventTypes, common.EventType)
		if common.EventType == EventResponseHeader {
			var event ResponseHeader
			if err := json.Unmarshal(record.Value, &event); err != nil {
				t.Fatal(err)
			}
			headers = append(headers, event)
		}
	}
	wantTypes := []EventType{EventRequestStarted, EventResponseStarted, EventResponseHeader, EventResponseHeader, EventResponseHeader, EventResponseHeader}
	if !reflect.DeepEqual(eventTypes, wantTypes) {
		t.Fatalf("event order = %v, want %v", eventTypes, wantTypes)
	}
	if len(headers) != 4 || headers[0].HeaderName != "authorization" || headers[0].Value != "Bearer response-privileged" ||
		headers[1].HeaderName != "content-type" || headers[2].HeaderName != "x-trace" || headers[2].ValueIndex != 0 || headers[3].ValueIndex != 1 {
		t.Fatalf("unexpected response headers: %#v", headers)
	}
}
