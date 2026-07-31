package requestlog

import "time"

const (
	SchemaVersion         = 1
	MaxEventValueBytes    = 262_144
	MaxMetadataValueBytes = 4_096
	BodyChunkBytes        = 32 * 1024
)

type EventType string

const (
	EventRequestStarted    EventType = "request.started"
	EventRequestHeader     EventType = "request.header"
	EventRequestBodyChunk  EventType = "request.body_chunk"
	EventUpstreamAttempt   EventType = "upstream.attempt"
	EventUpstreamResult    EventType = "upstream.result"
	EventResponseStarted   EventType = "response.started"
	EventResponseHeader    EventType = "response.header"
	EventResponseBodyChunk EventType = "response.body_chunk"
	EventRequestCompleted  EventType = "request.completed"
)

type ResultKind string

const (
	ResultResponse              ResultKind = "response"
	ResultResponseHeaderTimeout ResultKind = "response_header_timeout"
	ResultTransportError        ResultKind = "transport_error"
	ResultClientCancelled       ResultKind = "client_cancelled"
)

type Outcome string

const (
	OutcomeCompleted             Outcome = "completed"
	OutcomeClientCancelled       Outcome = "client_cancelled"
	OutcomeClientWriteError      Outcome = "client_write_error"
	OutcomeUnknownModel          Outcome = "unknown_model"
	OutcomeResponseHeaderTimeout Outcome = "response_header_timeout"
	OutcomeUpstreamError         Outcome = "upstream_error"
	OutcomeInternalError         Outcome = "internal_error"
)

type Common struct {
	SchemaVersion          int                             `json:"schema_version"`
	EventID                string                          `json:"event_id"`
	EventType              EventType                       `json:"event_type"`
	RequestID              string                          `json:"request_id"`
	Sequence               uint64                          `json:"sequence"`
	Timestamp              time.Time                       `json:"timestamp"`
	InstanceID             string                          `json:"instance_id"`
	Principal              string                          `json:"principal"`
	PrincipalOriginalBytes int                             `json:"principal_original_bytes"`
	PrincipalTruncated     bool                            `json:"principal_truncated"`
	PrincipalSHA256        string                          `json:"principal_sha256,omitempty"`
	Model                  string                          `json:"model"`
	ModelOriginalBytes     int                             `json:"model_original_bytes"`
	ModelTruncated         bool                            `json:"model_truncated"`
	ModelSHA256            string                          `json:"model_sha256,omitempty"`
	Stream                 bool                            `json:"stream"`
	SessionHeaders         map[string][]SessionHeaderValue `json:"session_headers"`
}

type SessionHeaderValue struct {
	Value         string    `json:"value"`
	ValueMode     ValueMode `json:"value_mode"`
	OriginalBytes int       `json:"original_bytes"`
	Truncated     bool      `json:"truncated"`
	SHA256        string    `json:"sha256,omitempty"`
}

type RequestStarted struct {
	Common
	Method            string `json:"method"`
	Path              string `json:"path"`
	ContentType       string `json:"content_type"`
	TotalRequestBytes int64  `json:"total_request_bytes"`
}

type RequestBodyChunk struct {
	Common
	Payload    []byte `json:"payload"`
	ByteOffset int64  `json:"byte_offset"`
	ByteCount  int    `json:"byte_count"`
}

type RequestHeader struct {
	Common
	HeaderName    string `json:"header_name"`
	ValueIndex    int    `json:"value_index"`
	Value         string `json:"value"`
	OriginalBytes int    `json:"original_bytes"`
	Truncated     bool   `json:"truncated"`
	SHA256        string `json:"sha256,omitempty"`
}

type UpstreamAttempt struct {
	Common
	Attempt       int    `json:"attempt"`
	BackendSHA256 string `json:"backend_sha256"`
}

type UpstreamResult struct {
	Common
	Attempt           int        `json:"attempt"`
	BackendSHA256     string     `json:"backend_sha256"`
	ResultKind        ResultKind `json:"result_kind"`
	Status            *int       `json:"status,omitempty"`
	AttemptDurationMS int64      `json:"attempt_duration_ms"`
}

type ResponseStarted struct {
	Common
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
}

type ResponseBodyChunk struct {
	Common
	Payload    []byte `json:"payload"`
	ByteOffset int64  `json:"byte_offset"`
	ByteCount  int    `json:"byte_count"`
}

type ResponseHeader struct {
	Common
	HeaderName    string `json:"header_name"`
	ValueIndex    int    `json:"value_index"`
	Value         string `json:"value"`
	OriginalBytes int    `json:"original_bytes"`
	Truncated     bool   `json:"truncated"`
	SHA256        string `json:"sha256,omitempty"`
}

type RequestCompleted struct {
	Common
	Outcome               Outcome `json:"outcome"`
	DownstreamStatus      *int    `json:"downstream_status,omitempty"`
	TotalDurationMS       int64   `json:"total_duration_ms"`
	RequestBytes          int64   `json:"request_bytes"`
	ResponseBytes         int64   `json:"response_bytes"`
	CapturedRequestBytes  int64   `json:"captured_request_bytes"`
	CapturedResponseBytes int64   `json:"captured_response_bytes"`
	RequestTruncated      bool    `json:"request_truncated"`
	ResponseTruncated     bool    `json:"response_truncated"`
	AttemptCount          int     `json:"attempt_count"`
	EventsDropped         uint64  `json:"events_dropped"`
}

type Event interface {
	eventCommon() *Common
	eventType() EventType
}

func (e *RequestStarted) eventCommon() *Common    { return &e.Common }
func (e *RequestHeader) eventCommon() *Common     { return &e.Common }
func (e *RequestBodyChunk) eventCommon() *Common  { return &e.Common }
func (e *UpstreamAttempt) eventCommon() *Common   { return &e.Common }
func (e *UpstreamResult) eventCommon() *Common    { return &e.Common }
func (e *ResponseStarted) eventCommon() *Common   { return &e.Common }
func (e *ResponseHeader) eventCommon() *Common    { return &e.Common }
func (e *ResponseBodyChunk) eventCommon() *Common { return &e.Common }
func (e *RequestCompleted) eventCommon() *Common  { return &e.Common }

func (*RequestStarted) eventType() EventType    { return EventRequestStarted }
func (*RequestHeader) eventType() EventType     { return EventRequestHeader }
func (*RequestBodyChunk) eventType() EventType  { return EventRequestBodyChunk }
func (*UpstreamAttempt) eventType() EventType   { return EventUpstreamAttempt }
func (*UpstreamResult) eventType() EventType    { return EventUpstreamResult }
func (*ResponseStarted) eventType() EventType   { return EventResponseStarted }
func (*ResponseHeader) eventType() EventType    { return EventResponseHeader }
func (*ResponseBodyChunk) eventType() EventType { return EventResponseBodyChunk }
func (*RequestCompleted) eventType() EventType  { return EventRequestCompleted }
