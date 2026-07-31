package requestlog

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func goldenCommon(eventType EventType, sequence uint64) Common {
	return Common{
		SchemaVersion:          SchemaVersion,
		EventID:                "req-1:" + string(rune('0'+sequence)),
		EventType:              eventType,
		RequestID:              "req-1",
		Sequence:               sequence,
		Timestamp:              time.Date(2026, 7, 30, 12, 34, 56, 123456789, time.UTC),
		InstanceID:             "borg-0",
		Principal:              "team-a",
		PrincipalOriginalBytes: 6,
		PrincipalTruncated:     false,
		Model:                  "Qwen/32B",
		ModelOriginalBytes:     8,
		ModelTruncated:         false,
		Stream:                 true,
		SessionHeaders: map[string][]SessionHeaderValue{
			"x-session-id": {{Value: "sha256:abc", ValueMode: ValueModeSHA256, OriginalBytes: 7, Truncated: false}},
		},
	}
}

func TestEventGoldens(t *testing.T) {
	status := 200
	tests := []struct {
		name  string
		event Event
	}{
		{name: "request-started", event: &RequestStarted{Common: goldenCommon(EventRequestStarted, 0), Method: "POST", Path: "/v1/chat/completions", ContentType: "application/json", TotalRequestBytes: 42}},
		{name: "request-header", event: &RequestHeader{Common: goldenCommon(EventRequestHeader, 1), HeaderName: "x-session-id", ValueIndex: 0, Value: "session-123", OriginalBytes: 11}},
		{name: "request-body-chunk", event: &RequestBodyChunk{Common: goldenCommon(EventRequestBodyChunk, 2), Payload: []byte("request"), ByteOffset: 0, ByteCount: 7}},
		{name: "upstream-attempt", event: &UpstreamAttempt{Common: goldenCommon(EventUpstreamAttempt, 3), Attempt: 1, BackendSHA256: "sha256:backend"}},
		{name: "upstream-result", event: &UpstreamResult{Common: goldenCommon(EventUpstreamResult, 4), Attempt: 1, BackendSHA256: "sha256:backend", ResultKind: ResultResponse, Status: &status, AttemptDurationMS: 123}},
		{name: "response-started", event: &ResponseStarted{Common: goldenCommon(EventResponseStarted, 5), Status: 200, ContentType: "text/event-stream"}},
		{name: "response-header", event: &ResponseHeader{Common: goldenCommon(EventResponseHeader, 6), HeaderName: "x-trace-id", ValueIndex: 0, Value: "trace-1", OriginalBytes: 7}},
		{name: "response-body-chunk", event: &ResponseBodyChunk{Common: goldenCommon(EventResponseBodyChunk, 7), Payload: []byte("response"), ByteOffset: 0, ByteCount: 8}},
		{name: "request-completed", event: &RequestCompleted{Common: goldenCommon(EventRequestCompleted, 8), Outcome: OutcomeCompleted, DownstreamStatus: &status, TotalDurationMS: 456, RequestBytes: 42, ResponseBytes: 8, CapturedRequestBytes: 7, CapturedResponseBytes: 8, RequestTruncated: true, ResponseTruncated: false, AttemptCount: 1, EventsDropped: 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Encode(test.event)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile(filepath.Join("testdata", test.name+".golden.json"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != strings.TrimSpace(string(want)) {
				t.Fatalf("event differs from golden\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func TestEncodeRejectsInvalidAndOversizedEvents(t *testing.T) {
	if _, err := Encode(nil); err == nil {
		t.Fatal("expected nil event to fail")
	}
	event := &RequestStarted{Common: goldenCommon(EventRequestStarted, 0)}
	event.SchemaVersion = 2
	if _, err := Encode(event); err == nil {
		t.Fatal("expected schema mismatch to fail")
	}
	event.Common = goldenCommon(EventRequestStarted, 0)
	event.SessionHeaders = make(map[string][]SessionHeaderValue)
	for idx := 0; idx < 70; idx++ {
		event.SessionHeaders[string(rune('a'+idx%26))+strings.Repeat("x", idx)] = []SessionHeaderValue{{Value: strings.Repeat("y", MaxMetadataValueBytes), ValueMode: ValueModeRaw}}
	}
	if _, err := Encode(event); !errors.Is(err, ErrEventTooLarge) {
		t.Fatalf("expected typed oversize error, got %v", err)
	}
}

func TestMaximumBodyChunkFitsEventCeiling(t *testing.T) {
	event := &ResponseBodyChunk{
		Common:     goldenCommon(EventResponseBodyChunk, 1),
		Payload:    []byte(strings.Repeat("x", BodyChunkBytes)),
		ByteOffset: 0,
		ByteCount:  BodyChunkBytes,
	}
	encoded, err := Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= MaxEventValueBytes {
		t.Fatalf("maximum body chunk event uses %d bytes, ceiling is %d", len(encoded), MaxEventValueBytes)
	}
}

func TestMaximumHeaderValueFitsEventCeiling(t *testing.T) {
	raw := strings.Repeat("x", MaxMetadataValueBytes+1)
	bounded := BoundValue(raw)
	event := &RequestHeader{
		Common:        goldenCommon(EventRequestHeader, 1),
		HeaderName:    "x-large-header",
		Value:         bounded.Value,
		OriginalBytes: bounded.OriginalBytes,
		Truncated:     bounded.Truncated,
		SHA256:        bounded.SHA256,
	}
	encoded, err := Encode(event)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= MaxEventValueBytes {
		t.Fatalf("maximum header event uses %d bytes, ceiling is %d", len(encoded), MaxEventValueBytes)
	}
}

func TestResultAndOutcomeEnums(t *testing.T) {
	eventTypes := []EventType{EventRequestStarted, EventRequestHeader, EventRequestBodyChunk, EventUpstreamAttempt, EventUpstreamResult, EventResponseStarted, EventResponseHeader, EventResponseBodyChunk, EventRequestCompleted}
	resultKinds := []ResultKind{ResultResponse, ResultResponseHeaderTimeout, ResultTransportError, ResultClientCancelled}
	outcomes := []Outcome{OutcomeCompleted, OutcomeClientCancelled, OutcomeClientWriteError, OutcomeUnknownModel, OutcomeResponseHeaderTimeout, OutcomeUpstreamError, OutcomeInternalError}
	encoded, err := json.Marshal(struct {
		Events   []EventType  `json:"events"`
		Results  []ResultKind `json:"results"`
		Outcomes []Outcome    `json:"outcomes"`
	}{eventTypes, resultKinds, outcomes})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"events":["request.started","request.header","request.body_chunk","upstream.attempt","upstream.result","response.started","response.header","response.body_chunk","request.completed"],"results":["response","response_header_timeout","transport_error","client_cancelled"],"outcomes":["completed","client_cancelled","client_write_error","unknown_model","response_header_timeout","upstream_error","internal_error"]}`
	if string(encoded) != want {
		t.Fatalf("unexpected enum contract %s", encoded)
	}
}
