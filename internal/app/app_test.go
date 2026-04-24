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
		DiscoveryFactory: func([]config.DiscoverySelector) (discovery.Discoverer, error) {
			called = true
			return &appTestDiscoverer{}, nil
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
		DiscoveryFactory: func([]config.DiscoverySelector) (discovery.Discoverer, error) {
			called = true
			return &appTestDiscoverer{}, nil
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

func TestNewContinuesWhenDiscoveryFactoryFails(t *testing.T) {
	path := writeAppConfig(t, `
borg:
  auth_key: "EMPTY"
  update_interval: 1
  k8s_discover:
    - selector: "app=vllm"
`)

	borgApp, err := NewWithOptions(path, Options{
		DiscoveryFactory: func([]config.DiscoverySelector) (discovery.Discoverer, error) {
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
		DiscoveryFactory: func([]config.DiscoverySelector) (discovery.Discoverer, error) {
			return &appTestDiscoverer{
				endpoints: []discovery.Endpoint{
					{URL: "http://discovered", Models: []string{"dynamic-model"}},
				},
			}, nil
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
		DiscoveryFactory: func([]config.DiscoverySelector) (discovery.Discoverer, error) {
			return discoverer, nil
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
