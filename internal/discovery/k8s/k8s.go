package k8s

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/undy-io/BORG/internal/config"
	"github.com/undy-io/BORG/internal/discovery"
	"github.com/undy-io/BORG/internal/openai"
)

const (
	defaultNamespace = "default"
	defaultProtocol  = "http"
	defaultAPIPort   = "8000"
	defaultModelsEP  = "/v1/models"
)

type Service struct {
	selectors  []config.DiscoverySelector
	client     kubernetes.Interface
	httpClient *http.Client
	automodel  bool
	modelsPath string
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
	service := &Service{
		selectors: append([]config.DiscoverySelector(nil), selectors...),
		client:    client,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		automodel:  true,
		modelsPath: defaultModelsEP,
	}

	for _, opt := range opts {
		opt(service)
	}

	return service
}

func LoadConfig() (*rest.Config, error) {
	restConfig, err := rest.InClusterConfig()
	if err == nil {
		return restConfig, nil
	}

	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{},
	).ClientConfig()
}

func (s *Service) Discover(ctx context.Context) ([]discovery.Endpoint, error) {
	var endpoints []discovery.Endpoint
	for _, selector := range s.selectors {
		discovered, err := s.discoverSelector(ctx, selector)
		if err != nil {
			return nil, err
		}
		endpoints = append(endpoints, discovered...)
	}
	return endpoints, nil
}

func (s *Service) discoverSelector(ctx context.Context, selector config.DiscoverySelector) ([]discovery.Endpoint, error) {
	namespace := selector.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}

	pods, err := s.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.Selector,
	})
	if err != nil {
		return nil, err
	}

	endpoints := make([]discovery.Endpoint, 0, len(pods.Items))
	for _, pod := range pods.Items {
		endpoint, ok := endpointFromPod(pod)
		if !ok {
			continue
		}

		models := parseModelsFromPod(pod, selector.ModelKey)
		if len(models) == 0 && s.automodel {
			var err error
			models, err = s.enumModels(ctx, endpoint)
			if err != nil {
				return nil, err
			}
		}
		if len(models) == 0 {
			continue
		}

		endpoints = append(endpoints, discovery.Endpoint{
			URL:    endpoint,
			Models: models,
			APIKey: discovery.DefaultAPIKey,
		})
	}

	return endpoints, nil
}

func endpointFromPod(pod corev1.Pod) (string, bool) {
	if pod.Status.Phase != corev1.PodRunning {
		return "", false
	}
	if pod.Status.PodIP == "" {
		return "", false
	}
	if len(pod.Annotations) == 0 {
		return "", false
	}

	protocol := annotationDefault(pod.Annotations, "borg/protocol", defaultProtocol)
	apiPort := annotationDefault(pod.Annotations, "borg/apiport", defaultAPIPort)
	apiBase := pod.Annotations["borg/apibase"]

	return fmt.Sprintf("%s://%s%s", protocol, net.JoinHostPort(pod.Status.PodIP, apiPort), apiBase), true
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

func (s *Service) enumModels(ctx context.Context, endpoint string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+s.modelsPath, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+discovery.DefaultAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

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
