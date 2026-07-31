package requestlog

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/undy-io/BORG/internal/proxy"
)

type Logger struct {
	config       Config
	exporter     EventExporter
	dropReporter LocalDropReporter
	instanceID   string
	now          func() time.Time
}

type LoggerOption func(*Logger)

func WithInstanceID(instanceID string) LoggerOption {
	return func(logger *Logger) { logger.instanceID = instanceID }
}

func WithClock(now func() time.Time) LoggerOption {
	return func(logger *Logger) { logger.now = now }
}

func NewLogger(config Config, exporter EventExporter, opts ...LoggerOption) *Logger {
	instanceID := os.Getenv("HOSTNAME")
	if instanceID == "" {
		instanceID = NewRequestID()
	}
	logger := &Logger{config: config, exporter: exporter, instanceID: instanceID, now: time.Now}
	logger.dropReporter, _ = exporter.(LocalDropReporter)
	for _, opt := range opts {
		opt(logger)
	}
	return logger
}

func (l *Logger) Enabled() bool {
	return l != nil && l.config.Enabled() && l.exporter != nil
}

type StartInput struct {
	Started     time.Time
	Principal   string
	Model       string
	Stream      bool
	Headers     http.Header
	Host        string
	Method      string
	Path        string
	ContentType string
	Body        []byte
}

func (l *Logger) Start(input StartInput) *Recorder {
	if !l.Enabled() || !l.config.Matcher.MatchRequest(input.Principal, input.Model, input.Headers, input.Host) {
		return nil
	}
	if input.Started.IsZero() {
		input.Started = l.now()
	}
	principal := BoundValue(input.Principal)
	model := BoundValue(input.Model)
	headers := requestHeaderView(input.Headers, input.Host)
	sessionHeaders, rawHeaders := transformSessionHeaders(headers, l.config.SessionHeaders)
	requestID := NewRequestID()
	recorder := &Recorder{
		config:       l.config,
		exporter:     l.exporter,
		dropReporter: l.dropReporter,
		key:          PartitionKey(l.config.PartitionHeader, partitionValues(headers, l.config.PartitionHeader, rawHeaders), requestID),
		requestID:    requestID,
		started:      input.Started,
		now:          l.now,
		common: Common{
			SchemaVersion:          SchemaVersion,
			RequestID:              requestID,
			InstanceID:             l.instanceID,
			Principal:              principal.Value,
			PrincipalOriginalBytes: principal.OriginalBytes,
			PrincipalTruncated:     principal.Truncated,
			PrincipalSHA256:        principal.SHA256,
			Model:                  model.Value,
			ModelOriginalBytes:     model.OriginalBytes,
			ModelTruncated:         model.Truncated,
			ModelSHA256:            model.SHA256,
			Stream:                 input.Stream,
			SessionHeaders:         sessionHeaders,
		},
		requestBytes: int64(len(input.Body)),
	}
	recorder.emit(func(common Common) Event {
		return &RequestStarted{
			Common:            common,
			Method:            input.Method,
			Path:              input.Path,
			ContentType:       input.ContentType,
			TotalRequestBytes: int64(len(input.Body)),
		}
	})
	recorder.recordRequestHeaders(headers)
	recorder.recordRequestBody(input.Body)
	return recorder
}

func partitionValues(headers headerView, name string, declared map[string][]string) map[string][]string {
	if name == "" || len(declared[name]) > 0 {
		return declared
	}
	values := headers.Values(name)
	if len(values) == 0 {
		return declared
	}
	result := make(map[string][]string, len(declared)+1)
	for key, existing := range declared {
		result[key] = existing
	}
	result[name] = append([]string(nil), values...)
	return result
}

type Recorder struct {
	config       Config
	exporter     EventExporter
	dropReporter LocalDropReporter
	key          []byte
	common       Common
	now          func() time.Time

	mu                    sync.Mutex
	requestID             string
	started               time.Time
	sequence              uint64
	dropped               uint64
	requestBytes          int64
	responseBytes         int64
	capturedRequestBytes  int64
	capturedResponseBytes int64
	requestTruncated      bool
	responseTruncated     bool
	attemptCount          int
}

func (r *Recorder) recordRequestBody(body []byte) {
	if !r.config.Capture.RequestBody {
		r.mu.Lock()
		r.requestTruncated = len(body) > 0
		r.mu.Unlock()
		return
	}
	chunks, truncated := BodyChunks(body, r.config.Capture.MaxRequestBodyBytes)
	r.mu.Lock()
	r.requestTruncated = truncated
	r.mu.Unlock()
	var offset int64
	for _, chunk := range chunks {
		payload := chunk
		r.emit(func(common Common) Event {
			return &RequestBodyChunk{Common: common, Payload: payload, ByteOffset: offset, ByteCount: len(payload)}
		})
		offset += int64(len(chunk))
	}
	r.mu.Lock()
	r.capturedRequestBytes = offset
	r.mu.Unlock()
}

func (r *Recorder) recordRequestHeaders(headers headerView) {
	if !r.config.Capture.RequestHeaders {
		return
	}
	for _, header := range captureHeaders(headers, r.config.Capture.ExcludedRequestHeaders) {
		header := header
		r.emit(func(common Common) Event {
			return &RequestHeader{
				Common:        common,
				HeaderName:    header.Name,
				ValueIndex:    header.ValueIndex,
				Value:         header.Value,
				OriginalBytes: header.OriginalBytes,
				Truncated:     header.Truncated,
				SHA256:        header.SHA256,
			}
		})
	}
}

func (r *Recorder) OnAttemptStarted(attempt proxy.AttemptStarted) {
	r.mu.Lock()
	r.attemptCount = max(r.attemptCount, attempt.Attempt)
	r.mu.Unlock()
	r.emitAt(attempt.Started, func(common Common) Event {
		return &UpstreamAttempt{Common: common, Attempt: attempt.Attempt, BackendSHA256: BackendIdentifier(attempt.Endpoint)}
	})
}

func (r *Recorder) OnAttemptResult(result proxy.AttemptResult) {
	kind := ResultTransportError
	switch result.Kind {
	case proxy.AttemptResultResponse:
		kind = ResultResponse
	case proxy.AttemptResultResponseHeaderTimeout:
		kind = ResultResponseHeaderTimeout
	case proxy.AttemptResultClientCancelled:
		kind = ResultClientCancelled
	}
	r.emit(func(common Common) Event {
		event := &UpstreamResult{
			Common:            common,
			Attempt:           result.Attempt,
			BackendSHA256:     BackendIdentifier(result.Endpoint),
			ResultKind:        kind,
			AttemptDurationMS: DurationMilliseconds(result.Duration),
		}
		if result.Kind == proxy.AttemptResultResponse {
			status := result.Status
			event.Status = &status
		}
		return event
	})
}

func (r *Recorder) ResponseStarted(status int, contentType string, headers http.Header) {
	r.emit(func(common Common) Event {
		return &ResponseStarted{Common: common, Status: status, ContentType: contentType}
	})
	if !r.config.Capture.ResponseHeaders {
		return
	}
	for _, header := range CaptureHeaders(headers, r.config.Capture.ExcludedResponseHeaders) {
		header := header
		r.emit(func(common Common) Event {
			return &ResponseHeader{
				Common:        common,
				HeaderName:    header.Name,
				ValueIndex:    header.ValueIndex,
				Value:         header.Value,
				OriginalBytes: header.OriginalBytes,
				Truncated:     header.Truncated,
				SHA256:        header.SHA256,
			}
		})
	}
}

func (r *Recorder) RecordResponseWrite(payload []byte) {
	r.mu.Lock()
	r.responseBytes += int64(len(payload))
	capture := r.config.Capture.ResponseBody
	limit := r.config.Capture.MaxResponseBodyBytes
	remaining := int64(len(payload))
	if !capture {
		r.responseTruncated = r.responseTruncated || len(payload) > 0
		remaining = 0
	} else if limit > 0 {
		remaining = min(remaining, max(limit-r.capturedResponseBytes, 0))
		if remaining < int64(len(payload)) {
			r.responseTruncated = true
		}
	}
	captureOffset := r.capturedResponseBytes
	r.capturedResponseBytes += remaining
	r.mu.Unlock()

	for consumed := int64(0); consumed < remaining; {
		count := min(int64(BodyChunkBytes), remaining-consumed)
		chunk := append([]byte(nil), payload[consumed:consumed+count]...)
		chunkOffset := captureOffset + consumed
		r.emit(func(common Common) Event {
			return &ResponseBodyChunk{Common: common, Payload: chunk, ByteOffset: chunkOffset, ByteCount: len(chunk)}
		})
		consumed += count
	}
}

type CompletionInput struct {
	Forward          proxy.ForwardResult
	Err              error
	DownstreamStatus int
	ClientWriteError bool
	ClientCancelled  bool
}

func (r *Recorder) Complete(input CompletionInput) {
	outcome := OutcomeCompleted
	switch {
	case input.ClientWriteError || input.Forward.ClientWriteError:
		outcome = OutcomeClientWriteError
	case input.ClientCancelled || input.Forward.ClientCancelled:
		outcome = OutcomeClientCancelled
	case input.Forward.ResponseHeaderTimeout:
		outcome = OutcomeResponseHeaderTimeout
	case input.Forward.UpstreamBodyError || input.Forward.UpstreamError:
		outcome = OutcomeUpstreamError
	case input.Err != nil && !input.Forward.UnknownModel:
		outcome = OutcomeInternalError
	case input.Forward.UnknownModel:
		outcome = OutcomeUnknownModel
	}

	r.mu.Lock()
	dropped := r.dropped
	requestBytes := r.requestBytes
	responseBytes := r.responseBytes
	capturedRequestBytes := r.capturedRequestBytes
	capturedResponseBytes := r.capturedResponseBytes
	requestTruncated := r.requestTruncated
	responseTruncated := r.responseTruncated
	attemptCount := r.attemptCount
	r.mu.Unlock()
	r.emit(func(common Common) Event {
		event := &RequestCompleted{
			Common:                common,
			Outcome:               outcome,
			TotalDurationMS:       DurationMilliseconds(r.now().Sub(r.started)),
			RequestBytes:          requestBytes,
			ResponseBytes:         responseBytes,
			CapturedRequestBytes:  capturedRequestBytes,
			CapturedResponseBytes: capturedResponseBytes,
			RequestTruncated:      requestTruncated,
			ResponseTruncated:     responseTruncated,
			AttemptCount:          attemptCount,
			EventsDropped:         dropped,
		}
		if input.DownstreamStatus != 0 {
			status := input.DownstreamStatus
			event.DownstreamStatus = &status
		}
		return event
	})
}

func (r *Recorder) emit(build func(Common) Event) {
	r.emitAt(r.now(), build)
}

func (r *Recorder) emitAt(timestamp time.Time, build func(Common) Event) {
	r.mu.Lock()
	sequence := r.sequence
	r.sequence++
	common := r.common
	common.Sequence = sequence
	common.EventID = fmt.Sprintf("%s:%d", r.requestID, sequence)
	common.Timestamp = timestamp.UTC()
	event := build(common)
	event.eventCommon().EventType = event.eventType()
	encoded, err := Encode(event)
	if err != nil {
		r.dropped++
		if r.dropReporter != nil {
			reason := LocalDropEventEncodingFailed
			if errors.Is(err, ErrEventTooLarge) {
				reason = LocalDropEventTooLarge
			}
			r.dropReporter.RecordLocalDrop(reason)
		}
		r.mu.Unlock()
		return
	}
	if !r.exporter.TryExport(Record{Key: append([]byte(nil), r.key...), Value: encoded}) {
		r.dropped++
	}
	r.mu.Unlock()
}
