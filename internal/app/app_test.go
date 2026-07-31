package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/undy-io/BORG/internal/config"
	"github.com/undy-io/BORG/internal/discovery"
	"github.com/undy-io/BORG/internal/requestlog"
)

func TestNewWiresHandlerFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
borg:
  auth_key: "EMPTY"
  instances:
    - endpoint: "http://upstream"
      apikey: "sk-test"
      models: ["m"]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	borgApp, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	defer borgApp.Close()

	rec := httptest.NewRecorder()
	borgApp.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected root 200, got %d", rec.Code)
	}
}

func TestRequestLoggerFactoryIsInjectedAndClosed(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  request_logging:
    sink: kafka
    filters:
      - {}
    kafka:
      brokers: ["kafka.invalid:9092"]
`)
	called := false
	closed := false
	borgApp, err := NewWithOptions(path, Options{
		RequestLoggerFactory: func(loggingConfig requestlog.Config) (*requestlog.Logger, func(context.Context) error, error) {
			called = loggingConfig.Sink == requestlog.SinkKafka
			return requestlog.NewLogger(loggingConfig, nil), func(context.Context) error {
				closed = true
				return nil
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("request logger factory did not receive Kafka config")
	}
	borgApp.Close()
	if !closed {
		t.Fatal("App.Close did not close request logging")
	}
}

func TestKafkaTLSMaterialFailureStopsStartup(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  request_logging:
    sink: kafka
    kafka:
      brokers: ["kafka.invalid:9092"]
      tls:
        enabled: true
        ca_file: /definitely/missing/ca.pem
`)
	if _, err := New(path); err == nil || !strings.Contains(err.Error(), "Kafka TLS CA") {
		t.Fatalf("expected local TLS material failure, got %v", err)
	}
}

func TestKafkaBrokerOutageDoesNotAffectStartupOrReadiness(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  request_logging:
    sink: kafka
    kafka:
      brokers: ["127.0.0.1:1"]
`)
	borgApp, err := New(path)
	if err != nil {
		t.Fatalf("broker outage affected startup: %v", err)
	}
	defer borgApp.Close()
	recorder := httptest.NewRecorder()
	borgApp.Handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("broker outage affected readiness: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestNewDoesNotStartDiscoveryWhenDisabled(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  update_interval: -1
  k8s_discover:
    - selector: "app=vllm"
`)
	called := false

	borgApp, err := NewWithOptions(path, Options{
		DiscoveryFactory: func(*config.Runtime) ([]discovery.Source, error) {
			called = true
			return []discovery.Source{{ID: "pods:test", Discoverer: &appTestDiscoverer{}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer borgApp.Close()

	if called {
		t.Fatal("expected discovery factory not to be called")
	}
}

func TestNewDoesNotStartDiscoveryWithoutSelectors(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  update_interval: 1
`)
	called := false

	borgApp, err := NewWithOptions(path, Options{
		DiscoveryFactory: func(*config.Runtime) ([]discovery.Source, error) {
			called = true
			return []discovery.Source{{ID: "pods:test", Discoverer: &appTestDiscoverer{}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer borgApp.Close()

	if called {
		t.Fatal("expected discovery factory not to be called")
	}
}

func TestNewStartsServiceOnlyDiscovery(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  update_interval: 3600
  k8s_service_discover:
    - id: llmd-router
      namespace: models
      selector: "app=vllm"
`)
	var got []config.ResolvedServiceDiscovery

	borgApp, err := NewWithOptions(path, Options{
		DiscoveryFactory: func(runtime *config.Runtime) ([]discovery.Source, error) {
			got = append([]config.ResolvedServiceDiscovery(nil), runtime.K8SServiceDiscover...)
			return []discovery.Source{{ID: "llmd-router", Discoverer: &appTestDiscoverer{}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer borgApp.Close()

	if len(got) != 1 || got[0].ID != "llmd-router" {
		t.Fatalf("expected one service selector, got %#v", got)
	}
}

func TestNewContinuesWhenDiscoveryFactoryFails(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  update_interval: 1
  k8s_discover:
    - selector: "app=vllm"
`)

	borgApp, err := NewWithOptions(path, Options{
		DiscoveryFactory: func(*config.Runtime) ([]discovery.Source, error) {
			return nil, errors.New("no kube config")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer borgApp.Close()

	rec := httptest.NewRecorder()
	borgApp.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected root 200, got %d", rec.Code)
	}
}

func TestDiscoveryRegistersDiscoveredModel(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  update_interval: 3600
  k8s_discover:
    - selector: "app=vllm"
`)

	borgApp, err := NewWithOptions(path, Options{
		DiscoveryFactory: func(runtime *config.Runtime) ([]discovery.Source, error) {
			return []discovery.Source{{
				ID: runtime.K8SDiscover[0].ID,
				Discoverer: &appTestDiscoverer{
					endpoints: []discovery.Endpoint{
						{URL: "http://discovered", Models: []string{"dynamic-model"}},
					},
				},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer borgApp.Close()

	deadline := time.Now().Add(2 * time.Second)
	for {
		rec := httptest.NewRecorder()
		borgApp.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
		if strings.Contains(rec.Body.String(), "dynamic-model") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovered model was not registered; last response: %s", rec.Body.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDiscoverySourcesRefreshIndependently(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  update_interval: 1
  k8s_discover:
    - id: broken-pods
      selector: "app=broken"
    - id: working-pods
      selector: "app=working"
`)
	blocked := &appTestDiscoverer{
		block:   true,
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	working := &appRefreshingDiscoverer{}

	borgApp, err := NewWithOptions(path, Options{
		DiscoveryFactory: func(*config.Runtime) ([]discovery.Source, error) {
			return []discovery.Source{
				{ID: "broken-pods", Discoverer: blocked},
				{ID: "working-pods", Discoverer: working},
			}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer borgApp.Close()

	select {
	case <-blocked.started:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked discovery source did not start")
	}

	waitForModel := func(model string, deadline time.Time) {
		t.Helper()
		for {
			if _, ok := borgApp.Proxy.PickEndpoint(model); ok {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("a blocked source prevented model %q from being registered", model)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitForModel("working-model", time.Now().Add(2*time.Second))
	waitForModel("refreshed-model", time.Now().Add(3*time.Second))
}

type appRefreshingDiscoverer struct {
	mu    sync.Mutex
	calls int
}

func (d *appRefreshingDiscoverer) Discover(context.Context) ([]discovery.Endpoint, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.calls++
	model := "working-model"
	if d.calls > 1 {
		model = "refreshed-model"
	}
	return []discovery.Endpoint{{URL: "http://working", Models: []string{model}}}, nil
}

func TestCloseStopsDiscovery(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  update_interval: 3600
  k8s_discover:
    - selector: "app=vllm"
`)
	discoverer := &appTestDiscoverer{
		block:   true,
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}

	borgApp, err := NewWithOptions(path, Options{
		DiscoveryFactory: func(runtime *config.Runtime) ([]discovery.Source, error) {
			return []discovery.Source{{ID: runtime.K8SDiscover[0].ID, Discoverer: discoverer}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-discoverer.started:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not start")
	}

	done := make(chan struct{})
	go func() {
		borgApp.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return")
	}

	select {
	case <-discoverer.stopped:
	default:
		t.Fatal("discovery did not observe cancellation")
	}
}

type appTestDiscoverer struct {
	endpoints []discovery.Endpoint
	err       error
	block     bool
	started   chan struct{}
	stopped   chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once
}

func (d *appTestDiscoverer) Discover(ctx context.Context) ([]discovery.Endpoint, error) {
	if d.started != nil {
		d.startOnce.Do(func() {
			close(d.started)
		})
	}
	if d.block {
		<-ctx.Done()
		if d.stopped != nil {
			d.stopOnce.Do(func() {
				close(d.stopped)
			})
		}
		return nil, ctx.Err()
	}
	if d.err != nil {
		return nil, d.err
	}
	return d.endpoints, nil
}

func writeAppConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
