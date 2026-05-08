package k8s

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/undy-io/BORG/internal/config"
	"github.com/undy-io/BORG/internal/discovery"
)

func TestDiscoverRunningPodsProduceEndpoints(t *testing.T) {
	client := fake.NewSimpleClientset(
		testPod("model-a", "models", corev1.PodRunning, "10.0.0.1", map[string]string{
			"borg/models": "alpha,beta",
		}, map[string]string{"app": "vllm"}),
		testPod("model-b", "models", corev1.PodRunning, "10.0.0.2", map[string]string{
			"borg/models": "gamma",
		}, map[string]string{"app": "other"}),
	)
	service := NewWithClient([]config.DiscoverySelector{{
		Namespace: "models",
		Selector:  "app=vllm",
		ModelKey:  "borg/models",
	}}, client)

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	want := []discovery.Endpoint{
		{URL: "http://10.0.0.1:8000", Models: []string{"alpha", "beta"}, APIKey: discovery.DefaultAPIKey},
	}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("unexpected endpoints\nwant: %#v\n got: %#v", want, endpoints)
	}
}

func TestDiscoverSkipsIneligiblePods(t *testing.T) {
	client := fake.NewSimpleClientset(
		testPod("pending", "default", corev1.PodPending, "10.0.0.1", map[string]string{
			"borg/models": "alpha",
		}, nil),
		testPod("no-annotations", "default", corev1.PodRunning, "10.0.0.2", nil, nil),
		testPod("no-ip", "default", corev1.PodRunning, "", map[string]string{
			"borg/models": "beta",
		}, nil),
		testPod("no-models", "default", corev1.PodRunning, "10.0.0.3", map[string]string{
			"borg/models": "",
		}, nil),
	)
	service := NewWithClient([]config.DiscoverySelector{{
		ModelKey: "borg/models",
	}}, client, WithAutomodel(false))

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("expected no endpoints, got %#v", endpoints)
	}
}

func TestDiscoverAppliesNamespaceAndSelectorDefaults(t *testing.T) {
	client := fake.NewSimpleClientset(
		testPod("model", "default", corev1.PodRunning, "10.0.0.1", map[string]string{
			"borg/models": "alpha",
		}, nil),
	)
	client.Fake.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		listAction := action.(ktesting.ListAction)
		if listAction.GetNamespace() != defaultNamespace {
			t.Fatalf("expected default namespace %q, got %q", defaultNamespace, listAction.GetNamespace())
		}
		if got := listAction.GetListRestrictions().Labels.String(); got != "" {
			t.Fatalf("expected empty selector, got %q", got)
		}
		return false, nil, nil
	})
	service := NewWithClient([]config.DiscoverySelector{{
		ModelKey: "borg/models",
	}}, client)

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 1 {
		t.Fatalf("expected one endpoint, got %#v", endpoints)
	}
}

func TestDiscoverAppliesEndpointAnnotationDefaultsAndOverrides(t *testing.T) {
	client := fake.NewSimpleClientset(
		testPod("default-endpoint", "default", corev1.PodRunning, "10.0.0.1", map[string]string{
			"borg/models": "alpha",
		}, nil),
		testPod("custom-endpoint", "default", corev1.PodRunning, "10.0.0.2", map[string]string{
			"borg/models":   "beta",
			"borg/protocol": "https",
			"borg/apiport":  "9000",
			"borg/apibase":  "/openai",
		}, nil),
		testPod("ipv6-endpoint", "default", corev1.PodRunning, "fd00::1", map[string]string{
			"borg/models": "gamma",
		}, nil),
	)
	service := NewWithClient([]config.DiscoverySelector{{
		ModelKey: "borg/models",
	}}, client)

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sortEndpoints(endpoints)

	want := []discovery.Endpoint{
		{URL: "http://10.0.0.1:8000", Models: []string{"alpha"}, APIKey: discovery.DefaultAPIKey},
		{URL: "http://[fd00::1]:8000", Models: []string{"gamma"}, APIKey: discovery.DefaultAPIKey},
		{URL: "https://10.0.0.2:9000/openai", Models: []string{"beta"}, APIKey: discovery.DefaultAPIKey},
	}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("unexpected endpoints\nwant: %#v\n got: %#v", want, endpoints)
	}
}

func TestDiscoverParsesModelKeyCommaList(t *testing.T) {
	client := fake.NewSimpleClientset(
		testPod("model", "default", corev1.PodRunning, "10.0.0.1", map[string]string{
			"borg/models": "alpha, ,beta,, gamma ",
		}, nil),
	)
	service := NewWithClient([]config.DiscoverySelector{{
		ModelKey: "borg/models",
	}}, client)

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if !reflect.DeepEqual(endpoints[0].Models, want) {
		t.Fatalf("expected models %#v, got %#v", want, endpoints[0].Models)
	}
}

func TestDiscoverAutomodelSuccess(t *testing.T) {
	var sawAuth string
	var sawPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		if r.URL.Path != defaultModelsEP {
			http.NotFound(w, r)
			return
		}
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[{"id":"alpha"},{"id":"beta"}]}`)
	}))
	defer server.Close()

	host, port := serverHostPort(t, server.URL)
	client := fake.NewSimpleClientset(
		testPod("model", "default", corev1.PodRunning, host, map[string]string{
			"borg/apiport": port,
		}, nil),
	)
	service := NewWithClient([]config.DiscoverySelector{{}}, client, WithHTTPClient(server.Client()))

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if sawPath != defaultModelsEP {
		t.Fatalf("expected models path %q, got %q", defaultModelsEP, sawPath)
	}
	if sawAuth != "Bearer "+discovery.DefaultAPIKey {
		t.Fatalf("expected automodel bearer auth, got %q", sawAuth)
	}

	want := []discovery.Endpoint{
		{URL: "http://" + host + ":" + port, Models: []string{"alpha", "beta"}, APIKey: discovery.DefaultAPIKey},
	}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("unexpected endpoints\nwant: %#v\n got: %#v", want, endpoints)
	}
}

func TestDiscoverAutomodelFailuresReturnErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "status",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "nope", http.StatusBadGateway)
			},
		},
		{
			name: "json",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, `not-json`)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			host, port := serverHostPort(t, server.URL)
			client := fake.NewSimpleClientset(
				testPod("model", "default", corev1.PodRunning, host, map[string]string{
					"borg/apiport": port,
				}, nil),
			)
			service := NewWithClient([]config.DiscoverySelector{{}}, client, WithHTTPClient(server.Client()))

			if _, err := service.Discover(context.Background()); err == nil {
				t.Fatal("expected automodel error")
			}
		})
	}
}

func TestDiscoverAutomodelHTTPErrorReturnsError(t *testing.T) {
	client := fake.NewSimpleClientset(
		testPod("model", "default", corev1.PodRunning, "10.0.0.1", map[string]string{
			"borg/apiport": "8000",
		}, nil),
	)
	service := NewWithClient([]config.DiscoverySelector{{}}, client, WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("network failed")
		}),
	}))

	if _, err := service.Discover(context.Background()); err == nil {
		t.Fatal("expected automodel HTTP error")
	}
}

func TestDiscoverKubernetesListErrorReturnsError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.Fake.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("list failed")
	})
	service := NewWithClient([]config.DiscoverySelector{{}}, client)

	if _, err := service.Discover(context.Background()); err == nil {
		t.Fatal("expected list error")
	}
}

func testPod(name string, namespace string, phase corev1.PodPhase, ip string, annotations map[string]string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
			Labels:      labels,
		},
		Status: corev1.PodStatus{
			Phase: phase,
			PodIP: ip,
		},
	}
}

func serverHostPort(t *testing.T, rawURL string) (string, string) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	return host, port
}

func sortEndpoints(endpoints []discovery.Endpoint) {
	sort.Slice(endpoints, func(i, j int) bool {
		return endpoints[i].URL < endpoints[j].URL
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
