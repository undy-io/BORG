package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/undy-io/BORG/internal/config"
	"github.com/undy-io/BORG/internal/discovery"
	"github.com/undy-io/BORG/internal/openai"
)

const (
	defaultNamespace         = "default"
	defaultProtocol          = "http"
	defaultAPIPort           = "8000"
	defaultModelsEP          = "/v1/models"
	defaultModelConcurrency  = 8
	defaultKubernetesTimeout = 30 * time.Second
)

type Service struct {
	podSelectors     []config.DiscoverySelector
	serviceSelector  *config.ResolvedServiceDiscovery
	client           kubernetes.Interface
	httpClient       *http.Client
	automodel        bool
	modelConcurrency int
}

type Option func(*Service)

func WithHTTPClient(client *http.Client) Option {
	return func(s *Service) {
		if client != nil {
			s.httpClient = client
		}
	}
}

func WithAutomodel(enabled bool) Option {
	return func(s *Service) {
		s.automodel = enabled
	}
}

func WithModelConcurrency(limit int) Option {
	return func(s *Service) {
		if limit > 0 {
			s.modelConcurrency = limit
		}
	}
}

func NewSources(runtime *config.Runtime) ([]discovery.Source, error) {
	if runtime == nil {
		return nil, fmt.Errorf("runtime config is nil")
	}
	restConfig, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	sources := make([]discovery.Source, 0, len(runtime.K8SDiscover)+len(runtime.K8SServiceDiscover))
	for _, selector := range runtime.K8SDiscover {
		sources = append(sources, discovery.Source{
			ID:         selector.ID,
			Discoverer: NewWithClient([]config.DiscoverySelector{selector}, client),
		})
	}
	for _, selector := range runtime.K8SServiceDiscover {
		sources = append(sources, discovery.Source{
			ID:         selector.ID,
			Discoverer: NewServiceWithClient(selector, client),
		})
	}
	return sources, nil
}

func New(selectors []config.DiscoverySelector) (*Service, error) {
	restConfig, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}
	return NewWithClient(selectors, client), nil
}

func NewWithClient(selectors []config.DiscoverySelector, client kubernetes.Interface, opts ...Option) *Service {
	service := newService(client, opts...)
	service.podSelectors = append([]config.DiscoverySelector(nil), selectors...)
	return service
}

func NewServiceWithClient(selector config.ResolvedServiceDiscovery, client kubernetes.Interface, opts ...Option) *Service {
	service := newService(client)
	service.serviceSelector = &selector
	service.automodel = selector.Automodel
	for _, opt := range opts {
		opt(service)
	}
	return service
}

func newService(client kubernetes.Interface, opts ...Option) *Service {
	service := &Service{
		client: client,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		automodel:        true,
		modelConcurrency: defaultModelConcurrency,
	}
	for _, opt := range opts {
		opt(service)
	}
	return service
}

func LoadConfig() (*rest.Config, error) {
	restConfig, err := rest.InClusterConfig()
	if err == nil {
		return withKubernetesTimeoutCap(restConfig), nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
	if err != nil {
		return nil, err
	}
	return withKubernetesTimeoutCap(restConfig), nil
}

func withKubernetesTimeoutCap(restConfig *rest.Config) *rest.Config {
	configured := rest.CopyConfig(restConfig)
	if configured.Timeout <= 0 || configured.Timeout > defaultKubernetesTimeout {
		configured.Timeout = defaultKubernetesTimeout
	}
	return configured
}

func (s *Service) Discover(ctx context.Context) ([]discovery.Endpoint, error) {
	if s.serviceSelector != nil {
		return s.discoverServiceSelector(ctx, *s.serviceSelector)
	}

	var endpoints []discovery.Endpoint
	for _, selector := range s.podSelectors {
		discovered, err := s.discoverPodSelector(ctx, selector)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, discovered...)
	}
	return endpoints, nil
}

type modelCandidate struct {
	kind       string
	namespace  string
	name       string
	endpoint   string
	models     []string
	apiKey     string
	automodel  bool
	modelsPath string
}

func (s *Service) discoverPodSelector(ctx context.Context, selector config.DiscoverySelector) ([]discovery.Endpoint, error) {
	namespace := namespaceDefault(selector.Namespace)
	pods, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.Selector})
	if err != nil {
		return nil, err
	}

	candidates := make([]modelCandidate, 0, len(pods.Items))
	for _, pod := range pods.Items {
		endpoint, ok := endpointFromPod(pod)
		if !ok {
			continue
		}
		candidates = append(candidates, modelCandidate{
			kind:       "pod",
			namespace:  pod.Namespace,
			name:       pod.Name,
			endpoint:   endpoint,
			models:     parseModelsFromPod(pod, selector.ModelKey),
			apiKey:     discovery.DefaultAPIKey,
			automodel:  s.automodel,
			modelsPath: defaultModelsEP,
		})
	}
	return s.resolveCandidates(ctx, candidates), nil
}

func (s *Service) discoverServiceSelector(ctx context.Context, selector config.ResolvedServiceDiscovery) ([]discovery.Endpoint, error) {
	namespace := namespaceDefault(selector.Namespace)
	services, err := s.listServices(ctx, namespace, selector)
	if err != nil {
		return nil, err
	}

	candidates := make([]modelCandidate, 0, len(services))
	for _, service := range services {
		if service.DeletionTimestamp != nil {
			continue
		}
		endpoint, err := endpointFromService(service, selector)
		if err != nil {
			log.Printf("Skipping discovered service %s/%s: %v", service.Namespace, service.Name, err)
			continue
		}
		models := append([]string(nil), selector.Models...)
		if len(models) == 0 && selector.ModelKey != "" {
			models = parseModelList(service.Annotations[selector.ModelKey])
		}
		candidates = append(candidates, modelCandidate{
			kind:       "service",
			namespace:  service.Namespace,
			name:       service.Name,
			endpoint:   endpoint,
			models:     models,
			apiKey:     selector.APIKey,
			automodel:  s.automodel,
			modelsPath: selector.ModelsPath,
		})
	}
	return s.resolveCandidates(ctx, candidates), nil
}

func (s *Service) listServices(ctx context.Context, namespace string, selector config.ResolvedServiceDiscovery) ([]corev1.Service, error) {
	if selector.ServiceName != "" {
		service, err := s.client.CoreV1().Services(namespace).Get(ctx, selector.ServiceName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []corev1.Service{*service}, nil
	}

	services, err := s.client.CoreV1().Services(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.Selector})
	if err != nil {
		return nil, err
	}
	return services.Items, nil
}

func (s *Service) resolveCandidates(ctx context.Context, candidates []modelCandidate) []discovery.Endpoint {
	results := make([][]string, len(candidates))
	jobs := make(chan int, len(candidates))
	for idx := range candidates {
		candidate := candidates[idx]
		if len(candidate.models) > 0 {
			results[idx] = candidate.models
			continue
		}
		if candidate.automodel {
			jobs <- idx
		}
	}
	close(jobs)

	workerCount := min(s.modelConcurrency, len(jobs))
	var wg sync.WaitGroup
	for range workerCount {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					return
				}
				candidate := candidates[idx]
				models, err := s.enumModels(ctx, candidate.endpoint, candidate.modelsPath, candidate.apiKey)
				if err != nil {
					log.Printf("Skipping discovered %s %s/%s endpoint %s after model enumeration failed: %v", candidate.kind, candidate.namespace, candidate.name, candidate.endpoint, err)
					continue
				}
				results[idx] = models
			}
		}()
	}
	wg.Wait()

	endpoints := make([]discovery.Endpoint, 0, len(candidates))
	for idx, candidate := range candidates {
		if len(results[idx]) == 0 {
			continue
		}
		endpoints = append(endpoints, discovery.Endpoint{
			URL:    candidate.endpoint,
			Models: results[idx],
			APIKey: candidate.apiKey,
		})
	}
	return endpoints
}

func namespaceDefault(namespace string) string {
	if namespace == "" {
		return defaultNamespace
	}
	return namespace
}

func endpointFromPod(pod corev1.Pod) (string, bool) {
	if pod.Status.Phase != corev1.PodRunning || pod.DeletionTimestamp != nil || pod.Status.PodIP == "" || !podReady(pod) {
		return "", false
	}
	protocol := annotationDefault(pod.Annotations, "borg/protocol", defaultProtocol)
	apiPort := annotationDefault(pod.Annotations, "borg/apiport", defaultAPIPort)
	apiBase := pod.Annotations["borg/apibase"]
	return fmt.Sprintf("%s://%s%s", protocol, net.JoinHostPort(pod.Status.PodIP, apiPort), apiBase), true
}

func endpointFromService(service corev1.Service, selector config.ResolvedServiceDiscovery) (string, error) {
	if service.Name == "" || service.Namespace == "" {
		return "", fmt.Errorf("service name and namespace are required")
	}

	protocol := selector.Protocol
	if protocol == "" {
		protocol = annotationDefault(service.Annotations, "borg/protocol", defaultProtocol)
	}
	apiBase := selector.APIBase
	if apiBase == "" {
		apiBase = service.Annotations["borg/apibase"]
	}
	port, err := servicePort(service, selector)
	if err != nil {
		return "", err
	}

	host := fmt.Sprintf("%s.%s.svc", service.Name, service.Namespace)
	return fmt.Sprintf("%s://%s%s", protocol, net.JoinHostPort(host, strconv.Itoa(int(port))), apiBase), nil
}

func servicePort(service corev1.Service, selector config.ResolvedServiceDiscovery) (int32, error) {
	if selector.Port > 0 {
		return selector.Port, nil
	}
	if selector.PortName != "" {
		for _, port := range service.Spec.Ports {
			if port.Name == selector.PortName && port.Port > 0 {
				return port.Port, nil
			}
		}
		return 0, fmt.Errorf("configured port_name %q was not found", selector.PortName)
	}
	if annotated := service.Annotations["borg/apiport"]; annotated != "" {
		port, err := strconv.ParseInt(annotated, 10, 32)
		if err != nil || port < 1 || port > 65535 {
			return 0, fmt.Errorf("borg/apiport %q is invalid", annotated)
		}
		return int32(port), nil
	}
	if len(service.Spec.Ports) != 1 || service.Spec.Ports[0].Port <= 0 {
		return 0, fmt.Errorf("port or port_name is required when the Service does not expose exactly one port")
	}
	return service.Spec.Ports[0].Port, nil
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func annotationDefault(annotations map[string]string, key string, fallback string) string {
	if value := annotations[key]; value != "" {
		return value
	}
	return fallback
}

func parseModelsFromPod(pod corev1.Pod, modelKey string) []string {
	if modelKey == "" {
		return nil
	}
	return parseModelList(pod.Annotations[modelKey])
}

func parseModelList(value string) []string {
	parts := strings.Split(value, ",")
	models := make([]string, 0, len(parts))
	for _, part := range parts {
		model := strings.TrimSpace(part)
		if model != "" {
			models = append(models, model)
		}
	}
	return models
}

func (s *Service) enumModels(ctx context.Context, endpoint string, modelsPath string, apiKey string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+modelsPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("enumerate models from %s: status %d", endpoint, resp.StatusCode)
	}

	var modelList openai.ModelListResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelList); err != nil {
		return nil, fmt.Errorf("enumerate models from %s: %w", endpoint, err)
	}

	models := make([]string, 0, len(modelList.Data))
	for _, model := range modelList.Data {
		if model.ID != "" {
			models = append(models, model.ID)
		}
	}
	return models, nil
}
