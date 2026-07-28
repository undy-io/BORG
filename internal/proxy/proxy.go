package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

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

func (e *HTTPError) Error() string {
	return e.Detail
}

type compressionMode int

const (
	compressionRegular compressionMode = iota
	compressionStreaming
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

func withClock(now func() time.Time) Option {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

type endpointHealth struct {
	consecutiveFailures int
	unavailableUntil    time.Time
}

type Service struct {
	mu           sync.Mutex
	models       map[string]*roundRobin
	health       map[string]*endpointHealth
	healthConfig BackendHealthConfig
	now          func() time.Time
	regular      *http.Client
	streaming    *http.Client
	bufferPool   sync.Pool
}

func New(opts ...Option) *Service {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4096,
		MaxIdleConnsPerHost:   512,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	service := &Service{
		models:       make(map[string]*roundRobin),
		health:       make(map[string]*endpointHealth),
		healthConfig: normalizeBackendHealthConfig(BackendHealthConfig{Enabled: true}),
		now:          time.Now,
		regular:      &http.Client{Transport: transport, Timeout: 30 * time.Second},
		streaming:    &http.Client{Transport: transport},
		bufferPool: sync.Pool{New: func() any {
			return make([]byte, 32*1024)
		}},
	}
	for _, opt := range opts {
		opt(service)
	}
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

	if s.health[endpoint] == nil {
		s.health[endpoint] = &endpointHealth{}
	}
	for _, model := range models {
		bucket := s.models[model]
		if bucket == nil {
			bucket = &roundRobin{}
			s.models[model] = bucket
		}
		bucket.add(Endpoint{URL: endpoint, APIKey: apiKey})
	}
}

func (s *Service) RemoveInstance(endpoint string, models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	targetModels := models
	if targetModels == nil {
		targetModels = make([]string, 0, len(s.models))
		for model := range s.models {
			targetModels = append(targetModels, model)
		}
	}

	for _, model := range targetModels {
		bucket := s.models[model]
		if bucket == nil {
			continue
		}
		bucket.remove(endpoint)
		if bucket.len() == 0 {
			delete(s.models, model)
		}
	}
}

func (s *Service) PickEndpoint(model string) (Endpoint, bool) {
	return s.pickEndpoint(model, nil)
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
	if stream {
		return s.forwardStreaming(w, r, rawBody, model)
	}
	return s.forwardRegular(w, r, rawBody, model)
}

func (s *Service) forwardRegular(w http.ResponseWriter, r *http.Request, rawBody []byte, model string) error {
	attempted := make(map[string]struct{})
	var lastErr error
	var lastResp *http.Response
	var lastBody []byte

	for {
		endpoint, ok := s.pickEndpoint(model, attempted)
		if !ok {
			if len(attempted) == 0 {
				return &HTTPError{StatusCode: http.StatusNotFound, Detail: fmt.Sprintf("Unknown model: %q", model)}
			}
			if lastResp != nil {
				writeUpstreamResponse(w, lastResp, lastBody, regularExcludedResponseHeaders)
				return nil
			}
			return lastErr
		}
		attempted[endpoint.URL] = struct{}{}

		resp, body, err := s.fetchRegular(r, rawBody, endpoint)
		if err != nil {
			s.recordFailure(endpoint.URL)
			lastErr = err
			continue
		}

		if s.statusCountsAsFailure(resp.StatusCode) {
			s.recordFailure(endpoint.URL)
			lastResp = resp
			lastBody = body
			continue
		}

		s.recordSuccess(endpoint.URL)
		writeUpstreamResponse(w, resp, body, regularExcludedResponseHeaders)
		return nil
	}
}

func (s *Service) fetchRegular(r *http.Request, rawBody []byte, endpoint Endpoint) (*http.Response, []byte, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	upstreamReq, err := buildUpstreamRequest(ctx, r, rawBody, endpoint, compressionRegular)
	if err != nil {
		return nil, nil, err
	}

	resp, err := s.regular.Do(upstreamReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	return resp, body, nil
}

func (s *Service) forwardStreaming(w http.ResponseWriter, r *http.Request, rawBody []byte, model string) error {
	attempted := make(map[string]struct{})
	var lastErr error
	var lastResp *http.Response
	var lastEndpoint Endpoint

	for {
		endpoint, ok := s.pickEndpoint(model, attempted)
		if !ok {
			if len(attempted) == 0 {
				return &HTTPError{StatusCode: http.StatusNotFound, Detail: fmt.Sprintf("Unknown model: %q", model)}
			}
			if lastResp != nil {
				s.streamResponse(w, lastResp, lastEndpoint)
				return nil
			}
			return lastErr
		}
		attempted[endpoint.URL] = struct{}{}

		resp, err := s.openStreaming(r, rawBody, endpoint)
		if err != nil {
			s.recordFailure(endpoint.URL)
			lastErr = err
			continue
		}

		if s.statusCountsAsFailure(resp.StatusCode) {
			s.recordFailure(endpoint.URL)
			if lastResp != nil {
				_ = lastResp.Body.Close()
			}
			lastResp = resp
			lastEndpoint = endpoint
			continue
		}

		s.recordSuccess(endpoint.URL)
		if lastResp != nil {
			_ = lastResp.Body.Close()
		}
		s.streamResponse(w, resp, endpoint)
		return nil
	}
}

func (s *Service) openStreaming(r *http.Request, rawBody []byte, endpoint Endpoint) (*http.Response, error) {
	upstreamReq, err := buildUpstreamRequest(r.Context(), r, rawBody, endpoint, compressionStreaming)
	if err != nil {
		return nil, err
	}

	resp, err := s.streaming.Do(upstreamReq)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (s *Service) streamResponse(w http.ResponseWriter, resp *http.Response, endpoint Endpoint) {
	defer resp.Body.Close()

	copyResponseHeaders(w.Header(), resp.Header, streamingExcludedResponseHeaders)
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := s.bufferPool.Get().([]byte)
	defer s.bufferPool.Put(buf)

	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			return
		}
		s.recordFailure(endpoint.URL)
		return
	}
}

func writeUpstreamResponse(w http.ResponseWriter, resp *http.Response, body []byte, excluded map[string]struct{}) {
	copyResponseHeaders(w.Header(), resp.Header, excluded)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

func (s *Service) pickEndpoint(model string, attempted map[string]struct{}) (Endpoint, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := s.models[model]
	if bucket == nil {
		return Endpoint{}, false
	}

	now := s.now()
	endpoint, ok := bucket.pickWhere(func(endpoint Endpoint) bool {
		if _, seen := attempted[endpoint.URL]; seen {
			return false
		}
		return s.endpointAvailableLocked(endpoint.URL, now)
	})
	if ok {
		return endpoint, true
	}

	return bucket.pickWhere(func(endpoint Endpoint) bool {
		_, seen := attempted[endpoint.URL]
		return !seen
	})
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

func (s *Service) statusCountsAsFailure(statusCode int) bool {
	if !s.healthConfig.Enabled {
		return false
	}
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
