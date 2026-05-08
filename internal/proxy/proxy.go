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

type Service struct {
	mu         sync.Mutex
	models     map[string]*roundRobin
	regular    *http.Client
	streaming  *http.Client
	bufferPool sync.Pool
}

func New() *Service {
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

	return &Service{
		models:    make(map[string]*roundRobin),
		regular:   &http.Client{Transport: transport, Timeout: 30 * time.Second},
		streaming: &http.Client{Transport: transport},
		bufferPool: sync.Pool{New: func() any {
			return make([]byte, 32*1024)
		}},
	}
}

func (s *Service) AddInstance(endpoint string, apiKey string, models []string) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
	s.mu.Lock()
	defer s.mu.Unlock()

	bucket := s.models[model]
	if bucket == nil {
		return Endpoint{}, false
	}
	return bucket.pick()
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
	endpoint, ok := s.PickEndpoint(model)
	if !ok {
		return &HTTPError{StatusCode: http.StatusNotFound, Detail: fmt.Sprintf("Unknown model: %q", model)}
	}

	if stream {
		return s.forwardStreaming(w, r, rawBody, endpoint)
	}
	return s.forwardRegular(w, r, rawBody, endpoint)
}

func (s *Service) forwardRegular(w http.ResponseWriter, r *http.Request, rawBody []byte, endpoint Endpoint) error {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	upstreamReq, err := buildUpstreamRequest(ctx, r, rawBody, endpoint, compressionRegular)
	if err != nil {
		return err
	}

	resp, err := s.regular.Do(upstreamReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	copyResponseHeaders(w.Header(), resp.Header, regularExcludedResponseHeaders)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return nil
}

func (s *Service) forwardStreaming(w http.ResponseWriter, r *http.Request, rawBody []byte, endpoint Endpoint) error {
	upstreamReq, err := buildUpstreamRequest(r.Context(), r, rawBody, endpoint, compressionStreaming)
	if err != nil {
		return err
	}

	resp, err := s.streaming.Do(upstreamReq)
	if err != nil {
		return err
	}
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
				return nil
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr == nil {
			continue
		}
		if readErr == io.EOF {
			return nil
		}
		return nil
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
