package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/undy-io/BORG/internal/discovery"
	"github.com/undy-io/BORG/internal/openai"
)

type Endpoint struct {
	URL    string
	APIKey string
}

type HTTPError struct {
	StatusCode int
	Detail     string
}

type AttemptResultKind string

const (
	AttemptResultResponse              AttemptResultKind = "response"
	AttemptResultResponseHeaderTimeout AttemptResultKind = "response_header_timeout"
	AttemptResultTransportError        AttemptResultKind = "transport_error"
	AttemptResultClientCancelled       AttemptResultKind = "client_cancelled"
)

type AttemptStarted struct {
	Attempt  int
	Endpoint string
	Started  time.Time
}

type AttemptResult struct {
	Attempt  int
	Endpoint string
	Kind     AttemptResultKind
	Status   int
	Duration time.Duration
}

type Observer interface {
	OnAttemptStarted(AttemptStarted)
	OnAttemptResult(AttemptResult)
}

type ForwardResult struct {
	AttemptCount          int
	DownstreamResponse    bool
	UnknownModel          bool
	ResponseHeaderTimeout bool
	UpstreamError         bool
	ClientCancelled       bool
	UpstreamBodyError     bool
	ClientWriteError      bool
}

type streamResult struct {
	upstreamBodyError bool
	clientWriteError  bool
}

func (e *HTTPError) Error() string {
	return e.Detail
}

type compressionMode int

const (
	compressionRegular compressionMode = iota
	compressionStreaming
)

const (
	RetryHeader                      = "Borg-Retry"
	RetryResponseHeaderTimeout       = "response-header-timeout"
	defaultResponseHeaderTimeout     = 5 * time.Minute
	responseHeaderTimeoutErrorDetail = "Upstream response header timeout"
)

type BackendHealthConfig struct {
	Enabled          bool
	FailureThreshold int
	Cooldown         time.Duration
	EjectOn500       bool
}

type Option func(*Service)

func WithBackendHealth(config BackendHealthConfig) Option {
	return func(s *Service) {
		s.healthConfig = normalizeBackendHealthConfig(config)
	}
}

func WithResponseHeaderTimeout(timeout time.Duration) Option {
	return func(s *Service) {
		if timeout >= 0 {
			s.responseHeaderTimeout = timeout
		}
	}
}

type endpointHealth struct {
	consecutiveFailures int
	unavailableUntil    time.Time
}

type upstreamErrorKind int

const (
	upstreamErrorOther upstreamErrorKind = iota
	upstreamErrorResponseHeaderTimeout
)

type upstreamError struct {
	kind upstreamErrorKind
	err  error
}

func (e *upstreamError) Error() string {
	return e.err.Error()
}

func (e *upstreamError) Unwrap() error {
	return e.err
}

type responseHeaderTimeoutCause struct{}

func (responseHeaderTimeoutCause) Error() string {
	return "upstream response header timeout"
}

type responseHeaderTimeoutTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

func (t *responseHeaderTimeoutTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.timeout == 0 {
		return t.base.RoundTrip(req)
	}

	attemptContext, cancelAttempt := context.WithCancelCause(req.Context())
	var mu sync.Mutex
	var timer *time.Timer
	completed := false
	timedOut := false

	trace := &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if completed {
				return
			}
			if timer != nil {
				timer.Stop()
			}
			timer = time.AfterFunc(t.timeout, func() {
				mu.Lock()
				defer mu.Unlock()
				if completed {
					return
				}
				timedOut = true
				cancelAttempt(responseHeaderTimeoutCause{})
			})
		},
	}
	tracedRequest := req.Clone(httptrace.WithClientTrace(attemptContext, trace))
	resp, err := t.base.RoundTrip(tracedRequest)

	mu.Lock()
	completed = true
	if timer != nil {
		timer.Stop()
	}
	didTimeOut := timedOut
	mu.Unlock()

	if didTimeOut {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		cancelAttempt(responseHeaderTimeoutCause{})
		return nil, &upstreamError{kind: upstreamErrorResponseHeaderTimeout, err: responseHeaderTimeoutCause{}}
	}
	if err != nil {
		cancelAttempt(nil)
		return nil, err
	}
	if resp.Body == nil {
		cancelAttempt(nil)
		return resp, nil
	}
	resp.Body = &cancelOnCloseReadCloser{
		ReadCloser: resp.Body,
		cancel:     func() { cancelAttempt(nil) },
	}
	return resp, nil
}

type cancelOnCloseReadCloser struct {
	io.ReadCloser
	once   sync.Once
	cancel func()
}

func (r *cancelOnCloseReadCloser) Close() error {
	err := r.ReadCloser.Close()
	r.once.Do(r.cancel)
	return err
}

type registeredEndpoint struct {
	APIKey string
	Models map[string]struct{}
}

const StaticSourceID = discovery.StaticSourceID

type Service struct {
	mu                    sync.Mutex
	models                map[string]*roundRobin
	sources               map[string]map[string]registeredEndpoint
	health                map[string]*endpointHealth
	healthConfig          BackendHealthConfig
	responseHeaderTimeout time.Duration
	now                   func() time.Time
	regular               *http.Client
	streaming             *http.Client
	bufferPool            sync.Pool
}

func New(opts ...Option) *Service {
	service := &Service{
		models:                make(map[string]*roundRobin),
		sources:               make(map[string]map[string]registeredEndpoint),
		health:                make(map[string]*endpointHealth),
		healthConfig:          normalizeBackendHealthConfig(BackendHealthConfig{Enabled: true}),
		responseHeaderTimeout: defaultResponseHeaderTimeout,
		now:                   time.Now,
		bufferPool: sync.Pool{New: func() any {
			buffer := make([]byte, 32*1024)
			return &buffer
		}},
	}
	for _, opt := range opts {
		opt(service)
	}

	baseTransport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4096,
		MaxIdleConnsPerHost:   512,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0,
		ExpectContinueTimeout: 1 * time.Second,
	}
	transport := &responseHeaderTimeoutTransport{
		base:    baseTransport,
		timeout: service.responseHeaderTimeout,
	}

	service.regular = &http.Client{Transport: transport}
	service.streaming = &http.Client{Transport: transport}
	return service
}

func normalizeBackendHealthConfig(config BackendHealthConfig) BackendHealthConfig {
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = 3
	}
	if config.Cooldown <= 0 {
		config.Cooldown = 30 * time.Second
	}
	return config
}

func (s *Service) AddInstance(endpoint string, apiKey string, models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	source := cloneSource(s.sources[StaticSourceID])
	registration := source[endpoint]
	registration.APIKey = apiKey
	if registration.Models == nil {
		registration.Models = make(map[string]struct{})
	}
	for _, model := range models {
		if model != "" {
			registration.Models[model] = struct{}{}
		}
	}
	source[endpoint] = registration
	_ = s.replaceSourceLocked(StaticSourceID, source)
}

func (s *Service) RemoveInstance(endpoint string, models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	source := cloneSource(s.sources[StaticSourceID])
	registration, ok := source[endpoint]
	if !ok {
		return
	}
	if models == nil {
		delete(source, endpoint)
	} else {
		for _, model := range models {
			delete(registration.Models, model)
		}
		if len(registration.Models) == 0 {
			delete(source, endpoint)
		} else {
			source[endpoint] = registration
		}
	}
	_ = s.replaceSourceLocked(StaticSourceID, source)
}

func (s *Service) ReplaceSource(sourceID string, endpoints []discovery.Endpoint) error {
	if sourceID == "" {
		return errors.New("source ID is required")
	}

	source, err := normalizeSource(endpoints)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceSourceLocked(sourceID, source)
}

func (s *Service) RemoveSource(sourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.replaceSourceLocked(sourceID, nil)
}

func normalizeSource(endpoints []discovery.Endpoint) (map[string]registeredEndpoint, error) {
	source := make(map[string]registeredEndpoint)
	for _, endpoint := range endpoints {
		if endpoint.URL == "" || len(endpoint.Models) == 0 {
			continue
		}
		apiKey := endpoint.APIKey
		if apiKey == "" {
			apiKey = discovery.DefaultAPIKey
		}

		registration := source[endpoint.URL]
		if registration.APIKey != "" && registration.APIKey != apiKey {
			return nil, fmt.Errorf("source declares conflicting API keys for endpoint %q", endpoint.URL)
		}
		registration.APIKey = apiKey
		if registration.Models == nil {
			registration.Models = make(map[string]struct{})
		}
		for _, model := range endpoint.Models {
			if model != "" {
				registration.Models[model] = struct{}{}
			}
		}
		if len(registration.Models) > 0 {
			source[endpoint.URL] = registration
		}
	}
	return source, nil
}

func cloneSource(source map[string]registeredEndpoint) map[string]registeredEndpoint {
	cloned := make(map[string]registeredEndpoint, len(source))
	for endpoint, registration := range source {
		models := make(map[string]struct{}, len(registration.Models))
		for model := range registration.Models {
			models[model] = struct{}{}
		}
		cloned[endpoint] = registeredEndpoint{APIKey: registration.APIKey, Models: models}
	}
	return cloned
}

func sourcesEqual(a map[string]registeredEndpoint, b map[string]registeredEndpoint) bool {
	if len(a) != len(b) {
		return false
	}
	for endpoint, registration := range a {
		other, ok := b[endpoint]
		if !ok || registration.APIKey != other.APIKey || len(registration.Models) != len(other.Models) {
			return false
		}
		for model := range registration.Models {
			if _, ok := other.Models[model]; !ok {
				return false
			}
		}
	}
	return true
}

func (s *Service) replaceSourceLocked(sourceID string, source map[string]registeredEndpoint) error {
	if sourcesEqual(s.sources[sourceID], source) {
		return nil
	}

	candidate := make(map[string]map[string]registeredEndpoint, len(s.sources)+1)
	for id, existing := range s.sources {
		candidate[id] = existing
	}
	if len(source) == 0 {
		delete(candidate, sourceID)
	} else {
		candidate[sourceID] = source
	}

	materialized, err := materializeSources(candidate)
	if err != nil {
		return err
	}
	s.sources = candidate
	s.applyMaterializedLocked(materialized)
	return nil
}

func materializeSources(sources map[string]map[string]registeredEndpoint) (map[string]map[string]Endpoint, error) {
	models := make(map[string]map[string]Endpoint)
	apiKeys := make(map[string]string)
	for sourceID, source := range sources {
		for endpointURL, registration := range source {
			if existing, ok := apiKeys[endpointURL]; ok && existing != registration.APIKey {
				return nil, fmt.Errorf("source %q conflicts with another API key for endpoint %q", sourceID, endpointURL)
			}
			apiKeys[endpointURL] = registration.APIKey
			for model := range registration.Models {
				if models[model] == nil {
					models[model] = make(map[string]Endpoint)
				}
				models[model][endpointURL] = Endpoint{URL: endpointURL, APIKey: registration.APIKey}
			}
		}
	}
	return models, nil
}

func (s *Service) applyMaterializedLocked(next map[string]map[string]Endpoint) {
	for model, bucket := range s.models {
		desired := next[model]
		for _, endpoint := range append([]Endpoint(nil), bucket.endpoints...) {
			if _, ok := desired[endpoint.URL]; !ok {
				bucket.remove(endpoint.URL)
			}
		}
		if bucket.len() == 0 {
			delete(s.models, model)
		}
	}

	registeredURLs := make(map[string]struct{})
	for model, desired := range next {
		bucket := s.models[model]
		if bucket == nil {
			bucket = &roundRobin{}
			s.models[model] = bucket
		}
		urls := make([]string, 0, len(desired))
		for endpointURL := range desired {
			urls = append(urls, endpointURL)
		}
		sort.Strings(urls)
		for _, endpointURL := range urls {
			bucket.add(desired[endpointURL])
			registeredURLs[endpointURL] = struct{}{}
			if s.health[endpointURL] == nil {
				s.health[endpointURL] = &endpointHealth{}
			}
		}
	}
	for endpointURL := range s.health {
		if _, ok := registeredURLs[endpointURL]; !ok {
			delete(s.health, endpointURL)
		}
	}
}

func (s *Service) PickEndpoint(model string) (Endpoint, bool) {
	endpoint, _, ok := s.pickEndpoint(model, nil, true)
	return endpoint, ok
}

func (s *Service) ListModels() openai.ModelListResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	names := make([]string, 0, len(s.models))
	for model, bucket := range s.models {
		if bucket.len() > 0 {
			names = append(names, model)
		}
	}
	sort.Strings(names)

	data := make([]openai.ModelInfo, 0, len(names))
	for _, name := range names {
		data = append(data, openai.ModelInfo{
			ID:      name,
			Object:  "model",
			Created: nil,
			OwnedBy: "vllm-proxy",
		})
	}

	return openai.ModelListResponse{Object: "list", Data: data}
}

func (s *Service) Forward(w http.ResponseWriter, r *http.Request, rawBody []byte, model string, stream bool) error {
	_, err := s.ForwardObserved(w, r, rawBody, model, stream, nil)
	return err
}

func (s *Service) ForwardObserved(w http.ResponseWriter, r *http.Request, rawBody []byte, model string, stream bool, observer Observer) (ForwardResult, error) {
	if stream {
		return s.forward(w, r, rawBody, model, compressionStreaming, streamingExcludedResponseHeaders, observer)
	}
	return s.forward(w, r, rawBody, model, compressionRegular, regularExcludedResponseHeaders, observer)
}

func (s *Service) forward(w http.ResponseWriter, r *http.Request, rawBody []byte, model string, compression compressionMode, excludedHeaders map[string]struct{}, observer Observer) (ForwardResult, error) {
	result := ForwardResult{}
	attempted := make(map[string]struct{})
	allowQuarantined := true
	retryHeaderTimeout := retryResponseHeaderTimeout(r.Header)
	sawHeaderTimeout := false
	var lastErr error
	var lastResp *http.Response
	var lastEndpoint Endpoint

	for {
		endpoint, quarantined, ok := s.pickEndpoint(model, attempted, allowQuarantined)
		if !ok {
			if len(attempted) == 0 {
				result.UnknownModel = true
				return result, &HTTPError{StatusCode: http.StatusNotFound, Detail: fmt.Sprintf("Unknown model: %q", model)}
			}
			if sawHeaderTimeout {
				if lastResp != nil {
					_ = lastResp.Body.Close()
				}
				result.ResponseHeaderTimeout = true
				return result, responseHeaderTimeoutFailure()
			}
			if lastResp != nil {
				streamed := s.streamResponse(w, r, lastResp, lastEndpoint, false, excludedHeaders)
				result.DownstreamResponse = true
				result.UpstreamBodyError = streamed.upstreamBodyError
				result.ClientWriteError = streamed.clientWriteError
				result.ClientCancelled = r.Context().Err() != nil
				return result, nil
			}
			if r.Context().Err() != nil {
				result.ClientCancelled = true
				return result, r.Context().Err()
			}
			result.UpstreamError = true
			return result, upstreamFailure(lastErr)
		}
		if quarantined {
			allowQuarantined = false
		}
		attempted[endpoint.URL] = struct{}{}

		upstreamReq, err := buildUpstreamRequest(r.Context(), r, rawBody, endpoint, compression)
		if err != nil {
			if requestEnded(r) {
				if lastResp != nil {
					_ = lastResp.Body.Close()
				}
				result.ClientCancelled = true
				return result, r.Context().Err()
			}
			s.recordFailure(endpoint.URL)
			lastErr = err
			continue
		}
		result.AttemptCount++
		attempt := result.AttemptCount
		started := time.Now()
		if observer != nil {
			observer.OnAttemptStarted(AttemptStarted{Attempt: attempt, Endpoint: endpoint.URL, Started: started})
		}
		resp, err := s.openUpstream(upstreamReq, compression)
		attemptResult := AttemptResult{Attempt: attempt, Endpoint: endpoint.URL, Duration: time.Since(started)}
		if err == nil {
			attemptResult.Kind = AttemptResultResponse
			attemptResult.Status = resp.StatusCode
		} else if requestEnded(r) {
			attemptResult.Kind = AttemptResultClientCancelled
		} else if isResponseHeaderTimeout(err) {
			attemptResult.Kind = AttemptResultResponseHeaderTimeout
		} else {
			attemptResult.Kind = AttemptResultTransportError
		}
		if observer != nil {
			observer.OnAttemptResult(attemptResult)
		}
		if err != nil {
			if requestEnded(r) {
				if lastResp != nil {
					_ = lastResp.Body.Close()
				}
				result.ClientCancelled = true
				return result, r.Context().Err()
			}
			s.recordFailure(endpoint.URL)
			lastErr = err
			if isResponseHeaderTimeout(err) {
				sawHeaderTimeout = true
				if !retryHeaderTimeout {
					if lastResp != nil {
						_ = lastResp.Body.Close()
					}
					result.ResponseHeaderTimeout = true
					return result, responseHeaderTimeoutFailure()
				}
			}
			continue
		}

		if s.statusIsRetryable(resp.StatusCode) {
			s.recordFailure(endpoint.URL)
			if lastResp != nil {
				_ = lastResp.Body.Close()
			}
			lastResp = resp
			lastEndpoint = endpoint
			continue
		}

		if lastResp != nil {
			_ = lastResp.Body.Close()
		}
		streamed := s.streamResponse(w, r, resp, endpoint, true, excludedHeaders)
		result.DownstreamResponse = true
		result.UpstreamBodyError = streamed.upstreamBodyError
		result.ClientWriteError = streamed.clientWriteError
		result.ClientCancelled = r.Context().Err() != nil
		return result, nil
	}
}

func (s *Service) openUpstream(upstreamReq *http.Request, compression compressionMode) (*http.Response, error) {
	client := s.regular
	if compression == compressionStreaming {
		client = s.streaming
	}
	return client.Do(upstreamReq)
}

func (s *Service) streamResponse(w http.ResponseWriter, r *http.Request, resp *http.Response, endpoint Endpoint, trackHealth bool, excludedHeaders map[string]struct{}) streamResult {
	defer func() { _ = resp.Body.Close() }()

	copyResponseHeaders(w.Header(), resp.Header, excludedHeaders)
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buffer := s.bufferPool.Get().(*[]byte)
	defer s.bufferPool.Put(buffer)
	buf := *buffer

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return streamResult{clientWriteError: true}
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			if trackHealth {
				s.recordSuccess(endpoint.URL)
			}
			return streamResult{}
		}
		if trackHealth && r.Context().Err() == nil {
			s.recordFailure(endpoint.URL)
		}
		return streamResult{upstreamBodyError: r.Context().Err() == nil}
	}
}

func requestEnded(r *http.Request) bool {
	return r.Context().Err() != nil
}

func upstreamFailure(err error) error {
	if err == nil {
		return &HTTPError{StatusCode: http.StatusBadGateway, Detail: "No upstream endpoint was available"}
	}
	return &HTTPError{StatusCode: http.StatusBadGateway, Detail: "All upstream endpoint attempts failed"}
}

func responseHeaderTimeoutFailure() error {
	return &HTTPError{StatusCode: http.StatusGatewayTimeout, Detail: responseHeaderTimeoutErrorDetail}
}

func isResponseHeaderTimeout(err error) bool {
	var upstreamErr *upstreamError
	return errors.As(err, &upstreamErr) && upstreamErr.kind == upstreamErrorResponseHeaderTimeout
}

func retryResponseHeaderTimeout(headers http.Header) bool {
	for _, value := range headers.Values(RetryHeader) {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), RetryResponseHeaderTimeout) {
				return true
			}
		}
	}
	return false
}

func (s *Service) pickEndpoint(model string, attempted map[string]struct{}, allowQuarantined bool) (Endpoint, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := s.models[model]
	if bucket == nil {
		return Endpoint{}, false, false
	}

	now := s.now()
	endpoint, ok := bucket.pickWhere(func(endpoint Endpoint) bool {
		if _, seen := attempted[endpoint.URL]; seen {
			return false
		}
		return s.endpointAvailableLocked(endpoint.URL, now)
	})
	if ok {
		return endpoint, false, true
	}
	if !allowQuarantined {
		return Endpoint{}, false, false
	}

	endpoint, ok = bucket.pickWhere(func(endpoint Endpoint) bool {
		_, seen := attempted[endpoint.URL]
		return !seen
	})
	return endpoint, ok, ok
}

func (s *Service) endpointRegisteredLocked(endpoint string) bool {
	for _, bucket := range s.models {
		for _, candidate := range bucket.endpoints {
			if candidate.URL == endpoint {
				return true
			}
		}
	}
	return false
}

func (s *Service) endpointAvailableLocked(endpoint string, now time.Time) bool {
	if !s.healthConfig.Enabled {
		return true
	}
	health := s.health[endpoint]
	if health == nil {
		return true
	}
	return health.unavailableUntil.IsZero() || !now.Before(health.unavailableUntil)
}

func (s *Service) recordFailure(endpoint string) {
	if !s.healthConfig.Enabled {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	health := s.health[endpoint]
	if health == nil {
		if !s.endpointRegisteredLocked(endpoint) {
			return
		}
		health = &endpointHealth{}
		s.health[endpoint] = health
	}
	health.consecutiveFailures++
	if health.consecutiveFailures >= s.healthConfig.FailureThreshold {
		health.unavailableUntil = s.now().Add(s.healthConfig.Cooldown)
	}
}

func (s *Service) recordSuccess(endpoint string) {
	if !s.healthConfig.Enabled {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	health := s.health[endpoint]
	if health == nil {
		return
	}
	health.consecutiveFailures = 0
	health.unavailableUntil = time.Time{}
}

func (s *Service) statusIsRetryable(statusCode int) bool {
	switch statusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	case http.StatusInternalServerError:
		return s.healthConfig.EjectOn500
	default:
		return false
	}
}

func buildUpstreamRequest(ctx context.Context, original *http.Request, rawBody []byte, endpoint Endpoint, compression compressionMode) (*http.Request, error) {
	upstreamURL := endpoint.URL + original.URL.Path
	req, err := http.NewRequestWithContext(ctx, original.Method, upstreamURL, bytes.NewReader(rawBody))
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = original.URL.RawQuery
	copyRequestHeaders(req.Header, original.Header)
	req.Header.Set("Authorization", "Bearer "+endpoint.APIKey)
	if compression == compressionStreaming {
		req.Header.Set("Accept-Encoding", "identity")
	}
	return req, nil
}

var excludedRequestHeaders = map[string]struct{}{
	"accept-encoding":     {},
	"borg-retry":          {},
	"host":                {},
	"content-length":      {},
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"trailers":            {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

var regularExcludedResponseHeaders = map[string]struct{}{
	"connection":          {},
	"content-encoding":    {},
	"content-length":      {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"trailers":            {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

var streamingExcludedResponseHeaders = map[string]struct{}{
	"connection":          {},
	"content-encoding":    {},
	"content-length":      {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"trailers":            {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

func copyRequestHeaders(dst http.Header, src http.Header) {
	connectionHeaders := connectionHeaderNames(src)
	for key, values := range src {
		if headerExcluded(key, excludedRequestHeaders, connectionHeaders) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func copyResponseHeaders(dst http.Header, src http.Header, excluded map[string]struct{}) {
	connectionHeaders := connectionHeaderNames(src)
	for key, values := range src {
		if headerExcluded(key, excluded, connectionHeaders) {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func headerExcluded(key string, static map[string]struct{}, dynamic map[string]struct{}) bool {
	lowerKey := strings.ToLower(key)
	if _, excluded := static[lowerKey]; excluded {
		return true
	}
	if _, excluded := dynamic[lowerKey]; excluded {
		return true
	}
	return false
}

func connectionHeaderNames(headers http.Header) map[string]struct{} {
	values := headers.Values("Connection")
	if len(values) == 0 {
		return nil
	}

	names := make(map[string]struct{})
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			name := strings.ToLower(strings.TrimSpace(token))
			if name != "" {
				names[name] = struct{}{}
			}
		}
	}
	return names
}
