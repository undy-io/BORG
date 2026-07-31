package proxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/undy-io/BORG/internal/discovery"
)

type collectingObserver struct {
	events []string
	starts []AttemptStarted
	result []AttemptResult
}

func (o *collectingObserver) OnAttemptStarted(event AttemptStarted) {
	o.events = append(o.events, "start")
	o.starts = append(o.starts, event)
}

func (o *collectingObserver) OnAttemptResult(event AttemptResult) {
	o.events = append(o.events, "result")
	o.result = append(o.result, event)
}

func TestForwardObservedAttemptOrderingAndStatusRetry(t *testing.T) {
	service := New()
	service.AddInstance("http://a.invalid", "a", []string{"m"})
	service.AddInstance("http://b.invalid", "b", []string{"m"})
	service.regular = &http.Client{Transport: proxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "a.invalid" {
			return testUpstreamResponse(http.StatusServiceUnavailable, "retry"), nil
		}
		return testUpstreamResponse(http.StatusOK, "ok"), nil
	})}
	observer := &collectingObserver{}
	result, err := service.ForwardObserved(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), []byte(`{"model":"m"}`), "m", false, observer)
	if err != nil {
		t.Fatal(err)
	}
	if result.AttemptCount != 2 || !result.DownstreamResponse {
		t.Fatalf("unexpected forward result: %#v", result)
	}
	if !reflect.DeepEqual(observer.events, []string{"start", "result", "start", "result"}) {
		t.Fatalf("unexpected event ordering: %#v", observer.events)
	}
	if observer.starts[0].Attempt != 1 || observer.result[0].Status != 503 || observer.result[0].Kind != AttemptResultResponse || observer.result[1].Status != 200 {
		t.Fatalf("unexpected observations: %#v %#v", observer.starts, observer.result)
	}
}

func TestForwardObservedClassifiesTransportAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want AttemptResultKind
	}{
		{name: "transport", err: errors.New("dial failed"), want: AttemptResultTransportError},
		{name: "cancelled", err: context.Canceled, want: AttemptResultClientCancelled},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := New()
			service.AddInstance("http://a.invalid", "a", []string{"m"})
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if test.want == AttemptResultClientCancelled {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			service.regular = &http.Client{Transport: proxyRoundTripFunc(func(*http.Request) (*http.Response, error) { return nil, test.err })}
			observer := &collectingObserver{}
			_, _ = service.ForwardObserved(httptest.NewRecorder(), req, []byte(`{"model":"m"}`), "m", false, observer)
			if len(observer.result) != 1 || observer.result[0].Kind != test.want {
				t.Fatalf("unexpected result events: %#v", observer.result)
			}
		})
	}
}

func TestForwardObservedResponseHeaderTimeoutRetryPolicy(t *testing.T) {
	tests := []struct {
		name         string
		retry        bool
		allTimeout   bool
		wantAttempts int
		wantError    bool
	}{
		{name: "default does not retry", wantAttempts: 1, wantError: true},
		{name: "opt-in succeeds", retry: true, wantAttempts: 2},
		{name: "opt-in exhausts", retry: true, allTimeout: true, wantAttempts: 2, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := New()
			service.AddInstance("http://a-timeout.invalid", "a", []string{"m"})
			service.AddInstance("http://b-healthy.invalid", "b", []string{"m"})
			service.regular = &http.Client{Transport: proxyRoundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Host == "a-timeout.invalid" || test.allTimeout {
					return nil, newTestHeaderTimeoutError()
				}
				return testUpstreamResponse(http.StatusOK, "ok"), nil
			})}

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			if test.retry {
				req.Header.Set(RetryHeader, RetryResponseHeaderTimeout)
			}
			observer := &collectingObserver{}
			result, err := service.ForwardObserved(httptest.NewRecorder(), req, []byte(`{"model":"m"}`), "m", false, observer)
			if test.wantError {
				assertGatewayTimeout(t, err)
			} else if err != nil {
				t.Fatal(err)
			}
			if result.AttemptCount != test.wantAttempts || len(observer.starts) != test.wantAttempts || len(observer.result) != test.wantAttempts {
				t.Fatalf("expected %d observed attempts, got result=%#v starts=%d results=%d", test.wantAttempts, result, len(observer.starts), len(observer.result))
			}
			if observer.result[0].Kind != AttemptResultResponseHeaderTimeout {
				t.Fatalf("expected first attempt to be a response-header timeout, got %#v", observer.result[0])
			}
			if !test.wantError && observer.result[1].Kind != AttemptResultResponse {
				t.Fatalf("expected retry response, got %#v", observer.result[1])
			}
		})
	}
}

func TestForwardObservedMalformedDynamicEndpointIsPenalizedWithoutAttemptEvent(t *testing.T) {
	service := New(WithBackendHealth(BackendHealthConfig{
		Enabled:          true,
		FailureThreshold: 1,
		Cooldown:         time.Hour,
	}))
	const malformed = "http://%zz"
	if err := service.ReplaceSource("dynamic", []discovery.Endpoint{
		{URL: malformed, APIKey: "bad", Models: []string{"m"}},
		{URL: "http://healthy.invalid", APIKey: "good", Models: []string{"m"}},
	}); err != nil {
		t.Fatal(err)
	}
	service.regular = &http.Client{Transport: proxyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return testUpstreamResponse(http.StatusOK, "ok"), nil
	})}

	observer := &collectingObserver{}
	result, err := service.ForwardObserved(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), []byte(`{"model":"m"}`), "m", false, observer)
	if err != nil {
		t.Fatal(err)
	}
	if result.AttemptCount != 1 || len(observer.starts) != 1 || observer.starts[0].Endpoint != "http://healthy.invalid" {
		t.Fatalf("expected only the valid endpoint to produce an attempt, got result=%#v starts=%#v", result, observer.starts)
	}
	health := service.health[malformed]
	if health == nil || health.consecutiveFailures != 1 || health.unavailableUntil.IsZero() {
		t.Fatalf("expected malformed dynamic endpoint to be quarantined, got %#v", health)
	}
}

type bodyErrorReader struct{ sent bool }

func (r *bodyErrorReader) Read(payload []byte) (int, error) {
	if r.sent {
		return 0, errors.New("upstream body failed")
	}
	r.sent = true
	return copy(payload, "partial"), nil
}
func (*bodyErrorReader) Close() error { return nil }

func TestForwardObservedReportsSelectedBodyFailure(t *testing.T) {
	service := New()
	service.AddInstance("http://a.invalid", "a", []string{"m"})
	service.regular = &http.Client{Transport: proxyRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &bodyErrorReader{}}, nil
	})}
	recorder := httptest.NewRecorder()
	result, err := service.ForwardObserved(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil), []byte(`{"model":"m"}`), "m", false, &collectingObserver{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpstreamBodyError || result.ClientWriteError || !strings.Contains(recorder.Body.String(), "partial") {
		t.Fatalf("unexpected body result: %#v body=%q", result, recorder.Body.String())
	}
}

var _ io.ReadCloser = (*bodyErrorReader)(nil)
