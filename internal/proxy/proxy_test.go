package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/undy-io/BORG/internal/discovery"
)

func TestRegistryRoundRobinAndModels(t *testing.T) {
	service := New()
	service.AddInstance("http://e1:8000", "k1", []string{"zulu", "alpha"})
	service.AddInstance("http://e2:8000", "k2", []string{"alpha"})
	service.RemoveInstance("http://e1:8000", []string{"zulu"})

	listing := service.ListModels()
	if listing.Object != "list" {
		t.Fatalf("expected list object, got %q", listing.Object)
	}
	var names []string
	for _, model := range listing.Data {
		names = append(names, model.ID)
	}
	if !reflect.DeepEqual(names, []string{"alpha"}) {
		t.Fatalf("unexpected models: %#v", names)
	}

	first, ok := service.PickEndpoint("alpha")
	if !ok {
		t.Fatal("expected first endpoint")
	}
	second, ok := service.PickEndpoint("alpha")
	if !ok {
		t.Fatal("expected second endpoint")
	}
	if first.URL == second.URL {
		t.Fatalf("expected round-robin to rotate, got %q then %q", first.URL, second.URL)
	}
}

func TestPickUnknownModel(t *testing.T) {
	service := New()
	if _, ok := service.PickEndpoint("missing"); ok {
		t.Fatal("expected missing model")
	}
}

func TestRemoveInstanceClearsHealthAfterEndpointRetires(t *testing.T) {
	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	const endpoint = "http://e1:8000"
	service.AddInstance(endpoint, "k1", []string{"alpha", "beta"})
	service.recordFailure(endpoint)

	service.RemoveInstance(endpoint, []string{"alpha"})
	if service.health[endpoint] == nil {
		t.Fatal("expected health to remain while endpoint still serves a model")
	}

	service.RemoveInstance(endpoint, []string{"beta"})
	if _, ok := service.health[endpoint]; ok {
		t.Fatal("expected health to be removed with the retired endpoint")
	}
}

func TestReplaceSourceKeepsEndpointOwnedByAnotherSource(t *testing.T) {
	service := New()
	service.AddInstance("http://shared:8000", "sk-shared", []string{"static-model"})
	if err := service.ReplaceSource("pods:models", []discovery.Endpoint{{
		URL:    "http://shared:8000",
		APIKey: "sk-shared",
		Models: []string{"discovered-model"},
	}}); err != nil {
		t.Fatal(err)
	}

	service.RemoveSource("pods:models")
	if _, ok := service.PickEndpoint("static-model"); !ok {
		t.Fatal("expected static registration to survive discovery source removal")
	}
	if _, ok := service.PickEndpoint("discovered-model"); ok {
		t.Fatal("expected removed source model to disappear")
	}
}

func TestReplaceSourceRejectsAPIKeyConflictAtomically(t *testing.T) {
	service := New()
	if err := service.ReplaceSource("pods:models", []discovery.Endpoint{{
		URL:    "http://shared:8000",
		APIKey: "sk-one",
		Models: []string{"alpha"},
	}}); err != nil {
		t.Fatal(err)
	}

	err := service.ReplaceSource("services:routers", []discovery.Endpoint{{
		URL:    "http://shared:8000",
		APIKey: "sk-two",
		Models: []string{"beta"},
	}})
	if err == nil {
		t.Fatal("expected conflicting source API key to be rejected")
	}
	if _, ok := service.PickEndpoint("alpha"); !ok {
		t.Fatal("expected prior source snapshot to remain after rejection")
	}
	if _, ok := service.PickEndpoint("beta"); ok {
		t.Fatal("expected rejected source snapshot not to be installed")
	}
}

func TestRegularForwardUsesTransportManagedCompression(t *testing.T) {
	acceptEncoding := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding <- r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(gzipPayload(t, `{"ok":true}`))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")

	if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if got := <-acceptEncoding; got != "gzip" {
		t.Fatalf("expected transport-managed gzip upstream, got %q", got)
	}
	if got := rec.Body.String(); got != `{"ok":true}` {
		t.Fatalf("expected decoded body, got %q", got)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("expected content-encoding to be stripped, got %q", got)
	}
}

func TestStreamingForwardForcesIdentityEncoding(t *testing.T) {
	acceptEncoding := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding <- r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	if err := service.Forward(rec, req, []byte(`{"model":"m","stream":true}`), "m", true); err != nil {
		t.Fatal(err)
	}
	if got := <-acceptEncoding; got != "identity" {
		t.Fatalf("expected streaming identity encoding upstream, got %q", got)
	}
}

func TestRegularForwardRetriesFailureStatusBeforeWriting(t *testing.T) {
	var failingHits atomic.Int32
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failingHits.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer failing.Close()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ok.Close()

	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	service.AddInstance(failing.URL, "sk-fail", []string{"m"})
	service.AddInstance(ok.URL, "sk-ok", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected retry to healthy endpoint, got %d body=%s", rec.Code, rec.Body.String())
	}
	if failingHits.Load() != 1 {
		t.Fatalf("expected failing endpoint to be tried once, got %d", failingHits.Load())
	}

	rec = httptest.NewRecorder()
	if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected unhealthy endpoint to be skipped, got %d", rec.Code)
	}
	if failingHits.Load() != 1 {
		t.Fatalf("expected unhealthy endpoint to remain skipped, got %d hits", failingHits.Load())
	}
}

func TestTransportFailureRetriesAndEjectsEndpoint(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	failingURL := failing.URL
	failing.Close()

	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ok.Close()

	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	service.AddInstance(failingURL, "sk-fail", []string{"m"})
	service.AddInstance(ok.URL, "sk-ok", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected retry to healthy endpoint, got %d body=%s", rec.Code, rec.Body.String())
	}

	service.mu.Lock()
	defer service.mu.Unlock()
	if health := service.health[failingURL]; health == nil || health.consecutiveFailures == 0 || health.unavailableUntil.IsZero() {
		t.Fatalf("expected failing endpoint to be quarantined, got %#v", health)
	}
}

func TestResponseHeaderTimeoutReturnsGatewayTimeoutAndCountsFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		time.Sleep(250 * time.Millisecond)
	}))
	defer upstream.Close()

	service := New(
		WithResponseHeaderTimeout(25*time.Millisecond),
		WithBackendHealth(BackendHealthConfig{
			Enabled:          true,
			FailureThreshold: 1,
			Cooldown:         time.Hour,
		}),
	)
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected structured 504, got %#v", err)
	}
	health := service.health[upstream.URL]
	if health == nil || health.consecutiveFailures != 1 || health.unavailableUntil.IsZero() {
		t.Fatalf("expected timed-out endpoint to be quarantined, got %#v", health)
	}
}

func TestResponseHeaderTimeoutClassifiesPartialHTTP1Headers(t *testing.T) {
	upstream := newRawHeaderStallServer(t, "HTTP/1.1 200")
	service := New(WithResponseHeaderTimeout(25 * time.Millisecond))
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	err := service.Forward(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		[]byte(`{"model":"m"}`),
		"m",
		false,
	)
	assertGatewayTimeout(t, err)
}

func TestResponseHeaderTimeoutClassifiesInformationalResponseStall(t *testing.T) {
	upstream := newRawHeaderStallServer(t, "HTTP/1.1 100 Continue\r\n\r\n")
	service := New(WithResponseHeaderTimeout(25 * time.Millisecond))
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	err := service.Forward(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		[]byte(`{"model":"m"}`),
		"m",
		false,
	)
	assertGatewayTimeout(t, err)
}

func TestPartialHeaderTimeoutRetriesWithOptIn(t *testing.T) {
	timedOut := newRawHeaderStallServer(t, "HTTP/1.1 200")
	var healthyHits atomic.Int32
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		healthyHits.Add(1)
		if got := r.Header.Get(RetryHeader); got != "" {
			t.Fatalf("expected %s to be stripped upstream, got %q", RetryHeader, got)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer healthy.Close()

	service := New(WithResponseHeaderTimeout(25 * time.Millisecond))
	service.AddInstance(timedOut.URL, "sk-timeout", []string{"m"})
	service.AddInstance(healthy.URL, "sk-healthy", []string{"m"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set(RetryHeader, RetryResponseHeaderTimeout)
	rec := httptest.NewRecorder()
	if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("expected successful partial-header failover, got status=%d body=%q", rec.Code, rec.Body.String())
	}
	if healthyHits.Load() != 1 {
		t.Fatalf("expected one healthy retry, got %d", healthyHits.Load())
	}
}

func TestResponseHeaderTimeoutStartsAfterRequestUpload(t *testing.T) {
	uploadRelease := make(chan struct{})
	requestWritten := make(chan struct{})
	base := proxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		<-uploadRelease
		trace := httptrace.ContextClientTrace(req.Context())
		trace.WroteRequest(httptrace.WroteRequestInfo{})
		close(requestWritten)
		return testUpstreamResponse(http.StatusOK, "ok"), nil
	})
	transport := &responseHeaderTimeoutTransport{base: base, timeout: 20 * time.Millisecond}
	req := httptest.NewRequest(http.MethodPost, "http://upstream.invalid/v1/chat/completions", nil)
	done := make(chan error, 1)
	go func() {
		resp, err := transport.RoundTrip(req)
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		done <- err
	}()

	time.Sleep(50 * time.Millisecond)
	close(uploadRelease)
	<-requestWritten
	if err := <-done; err != nil {
		t.Fatalf("expected slow upload not to consume response timeout, got %v", err)
	}
}

func TestResponseHeaderTimeoutStopsBeforeSlowResponseBody(t *testing.T) {
	var attemptContext context.Context
	base := proxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attemptContext = req.Context()
		httptrace.ContextClientTrace(req.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		return testUpstreamResponse(http.StatusOK, "slow body"), nil
	})
	transport := &responseHeaderTimeoutTransport{base: base, timeout: 20 * time.Millisecond}
	req := httptest.NewRequest(http.MethodGet, "http://upstream.invalid/v1/models", nil)
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := attemptContext.Err(); err != nil {
		t.Fatalf("expected response body context to remain active, got %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "slow body" {
		t.Fatalf("unexpected response body %q", body)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(attemptContext.Err(), context.Canceled) {
		t.Fatalf("expected response body close to release attempt context, got %v", attemptContext.Err())
	}
}

func TestResponseHeaderTimeoutPreservesClientCancellation(t *testing.T) {
	base := proxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		httptrace.ContextClientTrace(req.Context()).WroteRequest(httptrace.WroteRequestInfo{})
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	transport := &responseHeaderTimeoutTransport{base: base, timeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://upstream.invalid/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, roundTripErr := transport.RoundTrip(req)
		done <- roundTripErr
	}()
	cancel()
	err = <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected client cancellation, got %v", err)
	}
	if isResponseHeaderTimeout(err) {
		t.Fatalf("expected client cancellation not to be classified as a header timeout")
	}
}

func TestUnlimitedResponseHeaderTimeout(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()
	service := New(WithResponseHeaderTimeout(0))
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	if err := service.Forward(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("expected unlimited timeout request to succeed, got status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestResponseHeaderTimeoutDoesNotRetryWithoutOptIn(t *testing.T) {
	var healthyHits atomic.Int32
	service := serviceWithTimeoutTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "a-timeout.invalid" {
			return nil, newTestHeaderTimeoutError()
		}
		healthyHits.Add(1)
		return testUpstreamResponse(http.StatusOK, `{"ok":true}`), nil
	})
	service.AddInstance("http://a-timeout.invalid", "sk-timeout", []string{"m"})
	service.AddInstance("http://b-healthy.invalid", "sk-healthy", []string{"m"})

	err := service.Forward(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil),
		[]byte(`{"model":"m"}`),
		"m",
		false,
	)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected structured 504, got %#v", err)
	}
	if healthyHits.Load() != 0 {
		t.Fatalf("expected no retry without opt-in, got %d healthy hits", healthyHits.Load())
	}
}

func TestResponseHeaderTimeoutRetriesWithOptInAndStripsHeader(t *testing.T) {
	var healthyHits atomic.Int32
	service := serviceWithTimeoutTransport(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "a-timeout.invalid" {
			return nil, newTestHeaderTimeoutError()
		}
		healthyHits.Add(1)
		if got := req.Header.Get(RetryHeader); got != "" {
			t.Fatalf("expected %s to be stripped upstream, got %q", RetryHeader, got)
		}
		return testUpstreamResponse(http.StatusOK, `{"ok":true}`), nil
	})
	service.AddInstance("http://a-timeout.invalid", "sk-timeout", []string{"m"})
	service.AddInstance("http://b-healthy.invalid", "sk-healthy", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set(RetryHeader, "Response-Header-Timeout")
	if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != `{"ok":true}` {
		t.Fatalf("expected successful opt-in retry, got status=%d body=%q", rec.Code, rec.Body.String())
	}
	if healthyHits.Load() != 1 {
		t.Fatalf("expected one healthy retry, got %d", healthyHits.Load())
	}
}

func TestResponseHeaderTimeoutOptInExhaustionReturnsGatewayTimeout(t *testing.T) {
	var hits atomic.Int32
	service := serviceWithTimeoutTransport(t, func(*http.Request) (*http.Response, error) {
		hits.Add(1)
		return nil, newTestHeaderTimeoutError()
	})
	service.AddInstance("http://a-timeout.invalid", "sk-a", []string{"m"})
	service.AddInstance("http://b-timeout.invalid", "sk-b", []string{"m"})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set(RetryHeader, RetryResponseHeaderTimeout)
	err := service.Forward(httptest.NewRecorder(), req, []byte(`{"model":"m"}`), "m", false)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected structured 504, got %#v", err)
	}
	if hits.Load() != 2 {
		t.Fatalf("expected both endpoints to be attempted, got %d attempts", hits.Load())
	}
}

func TestAllTransportFailuresReturnBadGateway(t *testing.T) {
	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	service.AddInstance("http://one.invalid", "sk-one", []string{"m"})
	service.AddInstance("http://two.invalid", "sk-two", []string{"m"})
	service.regular = &http.Client{Transport: proxyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false)
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected structured 502, got %#v", err)
	}
	for _, endpoint := range []string{"http://one.invalid", "http://two.invalid"} {
		health := service.health[endpoint]
		if health == nil || health.unavailableUntil.IsZero() {
			t.Fatalf("expected %s to be quarantined, got %#v", endpoint, health)
		}
	}
}

func TestClientCancellationDoesNotPenalizeBackend(t *testing.T) {
	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	const endpoint = "http://cancelled.invalid"
	service.AddInstance(endpoint, "sk-test", []string{"m"})

	ctx, cancel := context.WithCancel(context.Background())
	service.regular = &http.Client{Transport: proxyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()
		return nil, context.Canceled
	})}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)
	err := service.Forward(httptest.NewRecorder(), req, []byte(`{"model":"m"}`), "m", false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected client cancellation, got %v", err)
	}
	health := service.health[endpoint]
	if health == nil || health.consecutiveFailures != 0 || !health.unavailableUntil.IsZero() {
		t.Fatalf("expected cancellation not to affect backend health, got %#v", health)
	}
}

func TestRegularForwardWritesBeforeUpstreamBodyCompletes(t *testing.T) {
	service := New()
	service.AddInstance("http://streaming.invalid", "sk-test", []string{"m"})
	release := make(chan struct{})
	service.regular = &http.Client{Transport: proxyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       &stagedResponseBody{release: release},
		}, nil
	})}

	wrote := make(chan struct{})
	w := &signalingResponseWriter{recorder: httptest.NewRecorder(), wrote: wrote}
	done := make(chan error, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		done <- service.Forward(w, req, []byte(`{"model":"m"}`), "m", false)
	}()

	select {
	case <-wrote:
	case <-time.After(time.Second):
		t.Fatal("regular response did not stream before upstream completion")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("regular response did not finish")
	}
	if got := w.recorder.Body.String(); got != "firstsecond" {
		t.Fatalf("unexpected streamed body %q", got)
	}
}

func TestBackendHealthDoesNotEjectClientOrDefaultServerErrors(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
	}{
		{name: "client error", status: http.StatusBadRequest},
		{name: "default server error", status: http.StatusInternalServerError},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "status", tt.status)
			}))
			defer upstream.Close()

			service := New(WithBackendHealth(BackendHealthConfig{
				Enabled:          true,
				FailureThreshold: 1,
				Cooldown:         time.Hour,
			}))
			service.AddInstance(upstream.URL, "sk-test", []string{"m"})

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
				t.Fatal(err)
			}
			if rec.Code != tt.status {
				t.Fatalf("expected upstream status %d, got %d", tt.status, rec.Code)
			}

			service.mu.Lock()
			health := service.health[upstream.URL]
			service.mu.Unlock()
			if health == nil {
				t.Fatal("expected health entry")
			}
			if health.consecutiveFailures != 0 || !health.unavailableUntil.IsZero() {
				t.Fatalf("expected endpoint not to be ejected, got %#v", health)
			}
		})
	}
}

func TestPickEndpointFallsBackWhenAllEndpointsUnhealthy(t *testing.T) {
	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	service.AddInstance("http://e1:8000", "k1", []string{"m"})
	service.AddInstance("http://e2:8000", "k2", []string{"m"})
	service.recordFailure("http://e1:8000")
	service.recordFailure("http://e2:8000")

	endpoint, ok := service.PickEndpoint("m")
	if !ok {
		t.Fatal("expected an all-unhealthy fallback endpoint")
	}
	if endpoint.URL == "" {
		t.Fatalf("expected fallback endpoint URL, got %#v", endpoint)
	}
}

func TestForwardAllowsOnlyOneAllUnhealthyFallback(t *testing.T) {
	var hits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer second.Close()

	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	service.AddInstance(first.URL, "k1", []string{"m"})
	service.AddInstance(second.URL, "k2", []string{"m"})
	service.recordFailure(first.URL)
	service.recordFailure(second.URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected fallback response status 503, got %d", rec.Code)
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("expected exactly one quarantined endpoint attempt, got %d", got)
	}
}

func TestStreamingRetriesSetupFailureBeforeWriting(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer failing.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer ok.Close()

	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	service.AddInstance(failing.URL, "sk-fail", []string{"m"})
	service.AddInstance(ok.URL, "sk-ok", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err := service.Forward(rec, req, []byte(`{"model":"m","stream":true}`), "m", true); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "data: [DONE]\n\n" {
		t.Fatalf("expected successful retry stream, got status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestStreamingDoesNotRetryAfterBytesAreWritten(t *testing.T) {
	var secondHits atomic.Int32
	partial := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: partial\n\n"))
	}))
	defer partial.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		_, _ = w.Write([]byte("data: second\n\n"))
	}))
	defer second.Close()

	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	service.AddInstance(partial.URL, "sk-partial", []string{"m"})
	service.AddInstance(second.URL, "sk-second", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if err := service.Forward(rec, req, []byte(`{"model":"m","stream":true}`), "m", true); err != nil {
		t.Fatal(err)
	}
	if rec.Body.String() != "data: partial\n\n" {
		t.Fatalf("expected partial stream body, got %q", rec.Body.String())
	}
	if secondHits.Load() != 0 {
		t.Fatalf("expected no retry after stream bytes were written, got %d second hits", secondHits.Load())
	}
}

func TestStreamingDropsReachFailureThreshold(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: partial\n\n"))
	}))
	defer upstream.Close()

	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 3,
		Cooldown:         time.Hour,
	}))
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	for range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		if err := service.Forward(rec, req, []byte(`{"model":"m","stream":true}`), "m", true); err != nil {
			t.Fatal(err)
		}
	}

	service.mu.Lock()
	health := *service.health[upstream.URL]
	service.mu.Unlock()
	if health.consecutiveFailures != 3 || health.unavailableUntil.IsZero() {
		t.Fatalf("expected repeated stream drops to quarantine endpoint, got %#v", health)
	}
}

func TestRequestHopByHopHeadersAreStripped(t *testing.T) {
	src := http.Header{}
	src.Set("Accept-Encoding", "gzip")
	src.Set("Connection", "X-Foo, X-Bar")
	src.Set("Trailer", "X-Trailer")
	src.Set("Trailers", "X-Trailers")
	src.Set("TE", "trailers")
	src.Set(RetryHeader, RetryResponseHeaderTimeout)
	src.Set("X-Bar", "remove-me-too")
	src.Set("X-Foo", "remove-me")
	src.Set("X-Keep", "ok")

	dst := http.Header{}
	copyRequestHeaders(dst, src)

	for _, key := range []string{"Accept-Encoding", RetryHeader, "Connection", "Trailer", "Trailers", "TE", "X-Bar", "X-Foo"} {
		if got := dst.Get(key); got != "" {
			t.Fatalf("expected %s to be stripped, got %q", key, got)
		}
	}
	if got := dst.Get("X-Keep"); got != "ok" {
		t.Fatalf("expected X-Keep to be preserved, got %q", got)
	}
}

func TestResponseHopByHopHeadersAreStripped(t *testing.T) {
	src := http.Header{}
	src.Set("Connection", "X-Bar")
	src.Set("Content-Encoding", "gzip")
	src.Set("Content-Length", "12")
	src.Set("Trailer", "X-Trailer")
	src.Set("Transfer-Encoding", "chunked")
	src.Set("X-Bar", "remove-me")
	src.Set("X-Keep", "ok")

	regular := http.Header{}
	copyResponseHeaders(regular, src, regularExcludedResponseHeaders)
	for _, key := range []string{"Connection", "Content-Encoding", "Trailer", "Transfer-Encoding", "X-Bar"} {
		if got := regular.Get(key); got != "" {
			t.Fatalf("expected regular response %s to be stripped, got %q", key, got)
		}
	}
	if got := regular.Get("Content-Length"); got != "" {
		t.Fatalf("expected regular response content-length to be stripped for streaming, got %q", got)
	}
	if got := regular.Get("X-Keep"); got != "ok" {
		t.Fatalf("expected regular response X-Keep to be preserved, got %q", got)
	}

	streaming := http.Header{}
	copyResponseHeaders(streaming, src, streamingExcludedResponseHeaders)
	if got := streaming.Get("Content-Length"); got != "" {
		t.Fatalf("expected streaming response content-length to be stripped, got %q", got)
	}
}

func TestRegularForwardStreamsPartialResponseWithoutRetry(t *testing.T) {
	var secondHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("partial"))
	}))
	defer upstream.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		_, _ = w.Write([]byte("second"))
	}))
	defer second.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})
	service.AddInstance(second.URL, "sk-second", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false)
	if err != nil {
		t.Fatalf("expected committed read failure to end quietly, got %v", err)
	}
	if rec.Code != http.StatusCreated || rec.Body.String() != "partial" {
		t.Fatalf("expected partial upstream response, got status=%d body=%q", rec.Code, rec.Body.String())
	}
	if secondHits.Load() != 0 {
		t.Fatalf("expected no retry after regular response bytes were written, got %d hits", secondHits.Load())
	}
}

func TestStreamingForwardTreatsReadErrorAsStreamEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: partial\n\n"))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := service.Forward(rec, req, []byte(`{"model":"m","stream":true}`), "m", true)
	if err != nil {
		t.Fatalf("expected streaming read error to end quietly, got %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected upstream status, got %d", rec.Code)
	}
	if rec.Body.String() != "data: partial\n\n" {
		t.Fatalf("expected partial stream body, got %q", rec.Body.String())
	}
}

func TestStreamingForwardTreatsWriteErrorAsStreamEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := service.Forward(&failingResponseWriter{header: http.Header{}}, req, []byte(`{"model":"m","stream":true}`), "m", true)
	if err != nil {
		t.Fatalf("expected streaming write error to end quietly, got %v", err)
	}
}

func BenchmarkStreamingForward(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			_, _ = fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})
	body := []byte(`{"model":"m","stream":true}`)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		rec := httptest.NewRecorder()
		if err := service.Forward(rec, req, body, "m", true); err != nil {
			b.Fatal(err)
		}
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status %d", rec.Code)
		}
	}
}

type failingResponseWriter struct {
	header http.Header
}

type proxyRoundTripFunc func(*http.Request) (*http.Response, error)

func (f proxyRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func serviceWithTimeoutTransport(t *testing.T, roundTrip proxyRoundTripFunc) *Service {
	t.Helper()
	service := New(WithResponseHeaderTimeout(time.Second))
	service.regular = &http.Client{Transport: roundTrip}
	return service
}

func newTestHeaderTimeoutError() error {
	return &upstreamError{
		kind: upstreamErrorResponseHeaderTimeout,
		err:  errors.New("test response-header timeout"),
	}
}

func assertGatewayTimeout(t *testing.T, err error) {
	t.Helper()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("expected structured 504, got %#v", err)
	}
}

type rawHeaderStallServer struct {
	URL      string
	listener net.Listener
	release  chan struct{}
	once     sync.Once
}

func newRawHeaderStallServer(t *testing.T, responsePrefix string) *rawHeaderStallServer {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &rawHeaderStallServer{
		URL:      "http://" + listener.Addr().String(),
		listener: listener,
		release:  make(chan struct{}),
	}
	t.Cleanup(server.Close)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		reader := bufio.NewReader(conn)
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				return
			}
			if line == "\r\n" {
				break
			}
		}
		_, _ = io.WriteString(conn, responsePrefix)
		<-server.release
	}()
	return server
}

func (s *rawHeaderStallServer) Close() {
	s.once.Do(func() {
		close(s.release)
		_ = s.listener.Close()
	})
}

func testUpstreamResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type stagedResponseBody struct {
	release <-chan struct{}
	stage   int
}

func (b *stagedResponseBody) Read(data []byte) (int, error) {
	switch b.stage {
	case 0:
		b.stage++
		return copy(data, "first"), nil
	case 1:
		<-b.release
		b.stage++
		return copy(data, "second"), nil
	default:
		return 0, io.EOF
	}
}

func (b *stagedResponseBody) Close() error {
	return nil
}

type signalingResponseWriter struct {
	recorder *httptest.ResponseRecorder
	wrote    chan struct{}
	once     sync.Once
}

func (w *signalingResponseWriter) Header() http.Header {
	return w.recorder.Header()
}

func (w *signalingResponseWriter) WriteHeader(statusCode int) {
	w.recorder.WriteHeader(statusCode)
}

func (w *signalingResponseWriter) Write(data []byte) (int, error) {
	w.once.Do(func() { close(w.wrote) })
	return w.recorder.Write(data)
}

func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

func (w *failingResponseWriter) WriteHeader(statusCode int) {}

func (w *failingResponseWriter) Write(data []byte) (int, error) {
	return 0, net.ErrClosed
}

func gzipPayload(t *testing.T, value string) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
