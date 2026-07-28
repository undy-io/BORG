package k8s

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
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
	deleting := testPod("deleting", "default", corev1.PodRunning, "10.0.0.4", map[string]string{
		"borg/models": "delta",
	}, nil)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now

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
		notReadyPod(testPod("not-ready", "default", corev1.PodRunning, "10.0.0.4", map[string]string{
			"borg/models": "gamma",
		}, nil)),
		deleting,
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
	client.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
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

func TestDiscoverServicesProduceStableDNSEndpoints(t *testing.T) {
	client := fake.NewSimpleClientset(
		testService("model-api", "models", map[string]string{
			"borg/models": "alpha,beta",
		}, map[string]string{"app": "vllm"}, 8080),
		testService("ignored", "models", map[string]string{
			"borg/models": "gamma",
		}, map[string]string{"app": "other"}, 8080),
	)
	service := NewServiceWithClient(config.ResolvedServiceDiscovery{
		ID:         "llmd-router",
		Namespace:  "models",
		Selector:   "app=vllm",
		ModelKey:   "borg/models",
		Automodel:  true,
		ModelsPath: defaultModelsEP,
		APIKey:     discovery.DefaultAPIKey,
	}, client)

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []discovery.Endpoint{
		{URL: "http://model-api.models.svc:8080", Models: []string{"alpha", "beta"}, APIKey: discovery.DefaultAPIKey},
	}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("unexpected endpoints\nwant: %#v\n got: %#v", want, endpoints)
	}
}

func TestDiscoverServiceEndpointAnnotationsOverrideAmbiguousPorts(t *testing.T) {
	discovered := testService("model-api", "models", map[string]string{
		"borg/models":   "alpha",
		"borg/protocol": "https",
		"borg/apiport":  "9443",
		"borg/apibase":  "/openai",
	}, nil, 8080, 9090)
	client := fake.NewSimpleClientset(discovered)
	service := NewServiceWithClient(config.ResolvedServiceDiscovery{
		ID:         "llmd-router",
		Namespace:  "models",
		ModelKey:   "borg/models",
		Automodel:  true,
		ModelsPath: defaultModelsEP,
		APIKey:     discovery.DefaultAPIKey,
	}, client)

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []discovery.Endpoint{
		{URL: "https://model-api.models.svc:9443/openai", Models: []string{"alpha"}, APIKey: discovery.DefaultAPIKey},
	}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("unexpected endpoints\nwant: %#v\n got: %#v", want, endpoints)
	}
}

func TestDiscoverServicesSkipDeletingAndAmbiguousPorts(t *testing.T) {
	deleting := testService("deleting", "models", map[string]string{"borg/models": "alpha"}, nil, 8080)
	now := metav1.Now()
	deleting.DeletionTimestamp = &now
	client := fake.NewSimpleClientset(
		deleting,
		testService("ambiguous", "models", map[string]string{"borg/models": "beta"}, nil, 8080, 9090),
		testService("no-ports", "models", map[string]string{"borg/models": "gamma"}, nil),
	)
	service := NewServiceWithClient(config.ResolvedServiceDiscovery{
		ID:         "llmd-router",
		Namespace:  "models",
		ModelKey:   "borg/models",
		ModelsPath: defaultModelsEP,
		APIKey:     discovery.DefaultAPIKey,
	}, client, WithAutomodel(false))

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("expected ineligible services to be skipped, got %#v", endpoints)
	}
}

func TestDiscoverServiceAutomodel(t *testing.T) {
	client := fake.NewSimpleClientset(testService("model-api", "models", nil, nil, 8080))
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.URL.String(); got != "http://model-api.models.svc:8080/v1/models" {
			t.Fatalf("unexpected automodel URL %q", got)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer sk-router" {
			t.Fatalf("unexpected automodel authorization %q", got)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"alpha"}]}`)),
		}, nil
	})}
	service := NewServiceWithClient(config.ResolvedServiceDiscovery{
		ID:         "llmd-router",
		Namespace:  "models",
		Automodel:  true,
		ModelsPath: defaultModelsEP,
		APIKey:     "sk-router",
	}, client, WithHTTPClient(httpClient))

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []discovery.Endpoint{
		{URL: "http://model-api.models.svc:8080", Models: []string{"alpha"}, APIKey: "sk-router"},
	}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("unexpected endpoints\nwant: %#v\n got: %#v", want, endpoints)
	}
}

func TestDiscoverServiceUsesConfiguredFrontDoorContract(t *testing.T) {
	serviceObject := testService("qwen-inference-scheduler", "llm-d", map[string]string{
		"borg/models":   "annotation-model",
		"borg/protocol": "http",
		"borg/apiport":  "9999",
		"borg/apibase":  "/annotation",
	}, map[string]string{"llm-d.ai/inferenceServing": "true"})
	serviceObject.Spec.Ports = []corev1.ServicePort{
		{Name: "grpc", Port: 9000},
		{Name: "http", Port: 8000},
	}
	client := fake.NewSimpleClientset(serviceObject)
	service := NewServiceWithClient(config.ResolvedServiceDiscovery{
		ID:         "qwen-router",
		Namespace:  "llm-d",
		Selector:   "llm-d.ai/inferenceServing=true",
		PortName:   "http",
		Protocol:   "https",
		APIBase:    "/openai",
		Models:     []string{"Qwen/Qwen3-32B"},
		Automodel:  true,
		ModelsPath: defaultModelsEP,
		APIKey:     "sk-router",
	}, client)

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []discovery.Endpoint{{
		URL:    "https://qwen-inference-scheduler.llm-d.svc:8000/openai",
		Models: []string{"Qwen/Qwen3-32B"},
		APIKey: "sk-router",
	}}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("unexpected llm-d front-door endpoint\nwant: %#v\n got: %#v", want, endpoints)
	}
}

func TestDiscoverNamedServiceNotFoundProducesEmptySnapshot(t *testing.T) {
	client := fake.NewSimpleClientset()
	service := NewServiceWithClient(config.ResolvedServiceDiscovery{
		ID:          "qwen-router",
		Namespace:   "llm-d",
		ServiceName: "missing-router",
		PortName:    "http",
		Models:      []string{"Qwen/Qwen3-32B"},
	}, client)

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("expected missing named Service to clear its snapshot, got %#v", endpoints)
	}
}

func TestDiscoverAutomodelConcurrencyIsBounded(t *testing.T) {
	objects := make([]runtime.Object, 0, 6)
	for i := 1; i <= 6; i++ {
		objects = append(objects, testPod(
			fmt.Sprintf("model-%d", i),
			"default",
			corev1.PodRunning,
			fmt.Sprintf("10.0.0.%d", i),
			nil,
			nil,
		))
	}
	client := fake.NewSimpleClientset(objects...)
	started := make(chan struct{}, len(objects))
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		current := active.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"object":"list","data":[{"id":"alpha"}]}`)),
		}, nil
	})}
	service := NewWithClient(
		[]config.DiscoverySelector{{}},
		client,
		WithHTTPClient(httpClient),
		WithModelConcurrency(2),
	)

	type result struct {
		endpoints []discovery.Endpoint
		err       error
	}
	done := make(chan result, 1)
	go func() {
		endpoints, err := service.Discover(context.Background())
		done <- result{endpoints: endpoints, err: err}
	}()

	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for bounded automodel requests")
		}
	}
	select {
	case <-started:
		t.Fatal("more than two automodel requests started concurrently")
	case <-time.After(25 * time.Millisecond):
	}
	close(release)

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if len(got.endpoints) != len(objects) {
			t.Fatalf("expected %d endpoints, got %d", len(objects), len(got.endpoints))
		}
		for idx, endpoint := range got.endpoints {
			wantURL := fmt.Sprintf("http://10.0.0.%d:8000", idx+1)
			if endpoint.URL != wantURL {
				t.Fatalf("endpoint %d URL = %q, want %q", idx, endpoint.URL, wantURL)
			}
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for discovery")
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("expected maximum automodel concurrency 2, got %d", got)
	}
}

func TestResolveCandidatesStopsWorkersAfterCancellation(t *testing.T) {
	var requests atomic.Int32
	service := newService(fake.NewSimpleClientset(), WithHTTPClient(&http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, errors.New("unexpected request")
		}),
	}))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	endpoints := service.resolveCandidates(ctx, []modelCandidate{
		{endpoint: "http://one.invalid", automodel: true, modelsPath: defaultModelsEP},
		{endpoint: "http://two.invalid", automodel: true, modelsPath: defaultModelsEP},
	})
	if len(endpoints) != 0 {
		t.Fatalf("expected no endpoints after cancellation, got %#v", endpoints)
	}
	if requests.Load() != 0 {
		t.Fatalf("expected canceled workers not to issue requests, got %d", requests.Load())
	}
}

func TestKubernetesRequestTimeoutCap(t *testing.T) {
	tests := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{name: "unset", in: 0, want: defaultKubernetesTimeout},
		{name: "negative", in: -time.Second, want: defaultKubernetesTimeout},
		{name: "shorter", in: 10 * time.Second, want: 10 * time.Second},
		{name: "equal", in: defaultKubernetesTimeout, want: defaultKubernetesTimeout},
		{name: "longer", in: time.Minute, want: defaultKubernetesTimeout},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &rest.Config{Timeout: test.in}
			configured := withKubernetesTimeoutCap(source)
			if configured.Timeout != test.want {
				t.Fatalf("expected Kubernetes timeout %s, got %s", test.want, configured.Timeout)
			}
			if source.Timeout != test.in {
				t.Fatalf("expected source timeout %s to remain unchanged, got %s", test.in, source.Timeout)
			}
			if configured == source {
				t.Fatal("expected a copied Kubernetes config")
			}
		})
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
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"alpha"},{"id":"beta"}]}`)
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

func TestDiscoverAutomodelFailuresSkipEndpoints(t *testing.T) {
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
				_, _ = fmt.Fprint(w, `not-json`)
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

			endpoints, err := service.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(endpoints) != 0 {
				t.Fatalf("expected bad automodel endpoint to be skipped, got %#v", endpoints)
			}
		})
	}
}

func TestDiscoverAutomodelHTTPErrorSkipsEndpoint(t *testing.T) {
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

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(endpoints) != 0 {
		t.Fatalf("expected failed automodel endpoint to be skipped, got %#v", endpoints)
	}
}

func TestDiscoverAutomodelMixedFailuresKeepSuccessfulEndpoints(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"object":"list","data":[{"id":"alpha"}]}`)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusBadGateway)
	}))
	defer bad.Close()

	goodHost, goodPort := serverHostPort(t, good.URL)
	badHost, badPort := serverHostPort(t, bad.URL)
	client := fake.NewSimpleClientset(
		testPod("good", "default", corev1.PodRunning, goodHost, map[string]string{
			"borg/apiport": goodPort,
		}, nil),
		testPod("bad", "default", corev1.PodRunning, badHost, map[string]string{
			"borg/apiport": badPort,
		}, nil),
	)
	service := NewWithClient([]config.DiscoverySelector{{}}, client, WithHTTPClient(good.Client()))

	endpoints, err := service.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []discovery.Endpoint{
		{URL: "http://" + goodHost + ":" + goodPort, Models: []string{"alpha"}, APIKey: discovery.DefaultAPIKey},
	}
	if !reflect.DeepEqual(endpoints, want) {
		t.Fatalf("unexpected endpoints\nwant: %#v\n got: %#v", want, endpoints)
	}
}

func TestDiscoverKubernetesListErrorReturnsError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("list failed")
	})
	service := NewWithClient([]config.DiscoverySelector{{}}, client)

	if _, err := service.Discover(context.Background()); err == nil {
		t.Fatal("expected list error")
	}
}

func TestDiscoverKubernetesServiceListErrorReturnsError(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("list", "services", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("list failed")
	})
	service := NewServiceWithClient(config.ResolvedServiceDiscovery{
		ID:         "llmd-router",
		Selector:   "app=router",
		Automodel:  true,
		ModelsPath: defaultModelsEP,
		APIKey:     discovery.DefaultAPIKey,
	}, client)

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
			Conditions: []corev1.PodCondition{
				{Type: corev1.PodReady, Status: corev1.ConditionTrue},
			},
		},
	}
}

func testService(name string, namespace string, annotations map[string]string, labels map[string]string, ports ...int32) *corev1.Service {
	servicePorts := make([]corev1.ServicePort, 0, len(ports))
	for _, port := range ports {
		servicePorts = append(servicePorts, corev1.ServicePort{Port: port})
	}
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   namespace,
			Annotations: annotations,
			Labels:      labels,
		},
		Spec: corev1.ServiceSpec{Ports: servicePorts},
	}
}

func notReadyPod(pod *corev1.Pod) *corev1.Pod {
	pod.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.PodReady, Status: corev1.ConditionFalse},
	}
	return pod
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
