package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/undy-io/BORG/internal/discovery"
	"github.com/undy-io/BORG/internal/requestlog"
)

const (
	DefaultConfigPath                  = "config.yaml"
	DefaultHost                        = "0.0.0.0"
	DefaultPort                        = 8000
	DefaultKeyValue                    = "EMPTY"
	DefaultAuthPrefix                  = "PROXY:"
	DefaultMaxRequestBodyBytes   int64 = 64 * 1024 * 1024
	DefaultResponseHeaderTimeout       = 300

	ProxyConfigEnv   = "PROXY_CONFIG"
	PortEnv          = "PORT"
	APIKeyEnv        = "API_KEY"
	AuthKeyEnv       = "AUTH_KEY"
	LegacyAuthKeyEnv = "BORG_AUTH_KEY"
)

type File struct {
	Borg BorgConfig `json:"borg" yaml:"borg"`
}

type BorgConfig struct {
	AuthKey             string                `json:"auth_key" yaml:"auth_key"`
	AuthKeyFromEnv      string                `json:"auth_key_from_env" yaml:"auth_key_from_env"`
	AuthPrefix          string                `json:"auth_prefix" yaml:"auth_prefix"`
	Instances           []Instance            `json:"instances" yaml:"instances"`
	UpdateInterval      int                   `json:"update_interval" yaml:"update_interval"`
	K8SDiscover         []DiscoverySelector   `json:"k8s_discover" yaml:"k8s_discover"`
	K8SServiceDiscover  []ServiceDiscovery    `json:"k8s_service_discover" yaml:"k8s_service_discover"`
	BackendHealth       BackendHealthConfig   `json:"backend_health" yaml:"backend_health"`
	Upstream            UpstreamConfig        `json:"upstream" yaml:"upstream"`
	MaxRequestBodyBytes *int64                `json:"max_request_body_bytes" yaml:"max_request_body_bytes"`
	RequestLogging      requestlog.FileConfig `json:"request_logging" yaml:"request_logging"`
}

type Instance struct {
	Endpoint  string   `json:"endpoint" yaml:"endpoint"`
	APIKey    string   `json:"apikey" yaml:"apikey"`
	APIKeyEnv string   `json:"apikeyEnv" yaml:"apikeyEnv"`
	Models    []string `json:"models" yaml:"models"`
}

type DiscoverySelector struct {
	ID        string `json:"id" yaml:"id"`
	Namespace string `json:"namespace" yaml:"namespace"`
	Selector  string `json:"selector" yaml:"selector"`
	ModelKey  string `json:"modelkey" yaml:"modelkey"`
}

type ServiceDiscovery struct {
	ID          string   `json:"id" yaml:"id"`
	Namespace   string   `json:"namespace" yaml:"namespace"`
	ServiceName string   `json:"service_name" yaml:"service_name"`
	Selector    string   `json:"selector" yaml:"selector"`
	Port        int32    `json:"port" yaml:"port"`
	PortName    string   `json:"port_name" yaml:"port_name"`
	Protocol    string   `json:"protocol" yaml:"protocol"`
	APIBase     string   `json:"api_base" yaml:"api_base"`
	Models      []string `json:"models" yaml:"models"`
	ModelKey    string   `json:"modelkey" yaml:"modelkey"`
	Automodel   *bool    `json:"automodel" yaml:"automodel"`
	ModelsPath  string   `json:"models_path" yaml:"models_path"`
	APIKey      string   `json:"apikey" yaml:"apikey"`
	APIKeyEnv   string   `json:"apikeyEnv" yaml:"apikeyEnv"`
}

type BackendHealthConfig struct {
	Enabled          *bool `json:"enabled" yaml:"enabled"`
	FailureThreshold int   `json:"failure_threshold" yaml:"failure_threshold"`
	CooldownSeconds  int   `json:"cooldown_seconds" yaml:"cooldown_seconds"`
	EjectOn500       bool  `json:"eject_on_500" yaml:"eject_on_500"`
}

type UpstreamConfig struct {
	ResponseHeaderTimeoutSeconds *int `json:"response_header_timeout_seconds" yaml:"response_header_timeout_seconds"`
}

type Runtime struct {
	AuthKey             string
	AuthPrefix          string
	Instances           []ResolvedInstance
	UpdateInterval      int
	K8SDiscover         []DiscoverySelector
	K8SServiceDiscover  []ResolvedServiceDiscovery
	BackendHealth       ResolvedBackendHealth
	Upstream            ResolvedUpstream
	MaxRequestBodyBytes int64
	RequestLogging      requestlog.Config
}

type ResolvedInstance struct {
	Endpoint string
	APIKey   string
	Models   []string
}

type ResolvedServiceDiscovery struct {
	ID          string
	Namespace   string
	ServiceName string
	Selector    string
	Port        int32
	PortName    string
	Protocol    string
	APIBase     string
	Models      []string
	ModelKey    string
	Automodel   bool
	ModelsPath  string
	APIKey      string
}

type ResolvedBackendHealth struct {
	Enabled          bool
	FailureThreshold int
	CooldownSeconds  int
	EjectOn500       bool
}

type ResolvedUpstream struct {
	ResponseHeaderTimeoutSeconds int
}

func ResolveConfigPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if envValue := os.Getenv(ProxyConfigEnv); envValue != "" {
		return envValue
	}
	return DefaultConfigPath
}

func ResolveHost(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return DefaultHost
}

func ResolvePort(flagValue string) (int, error) {
	if flagValue != "" {
		return parsePort(flagValue, "port")
	}
	if envValue := os.Getenv(PortEnv); envValue != "" {
		return parsePort(envValue, PortEnv)
	}
	return DefaultPort, nil
}

func parsePort(value string, source string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", source, err)
	}
	if port < 0 || port > 65535 {
		return 0, fmt.Errorf("%s must be between 0 and 65535", source)
	}
	return port, nil
}

func Load(path string) (*Runtime, error) {
	file, err := LoadFile(path)
	if err != nil {
		return nil, err
	}
	return ResolveRuntime(file)
}

func LoadFile(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var file File
	switch filepath.Ext(path) {
	case ".json":
		if err := json.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("parse JSON config %q: %w", path, err)
		}
	default:
		if err := yaml.Unmarshal(raw, &file); err != nil {
			return nil, fmt.Errorf("parse YAML config %q: %w", path, err)
		}
	}

	return &file, nil
}

func ResolveRuntime(file *File) (*Runtime, error) {
	if file == nil {
		return nil, errors.New("config file is nil")
	}

	borg := file.Borg
	upstream, err := resolveUpstream(borg.Upstream)
	if err != nil {
		return nil, err
	}
	maxRequestBodyBytes, err := resolveMaxRequestBodyBytes(borg.MaxRequestBodyBytes)
	if err != nil {
		return nil, err
	}
	requestLogging, err := requestlog.Resolve(borg.RequestLogging, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	if err := validateRequestLoggingEnvironment(borg, requestLogging); err != nil {
		return nil, err
	}

	runtime := &Runtime{
		AuthKey:             resolveAuthKey(borg.AuthKey, borg.AuthKeyFromEnv),
		AuthPrefix:          resolveAuthPrefix(borg.AuthPrefix),
		UpdateInterval:      borg.UpdateInterval,
		K8SDiscover:         append([]DiscoverySelector(nil), borg.K8SDiscover...),
		BackendHealth:       resolveBackendHealth(borg.BackendHealth),
		Upstream:            upstream,
		MaxRequestBodyBytes: maxRequestBodyBytes,
		RequestLogging:      requestLogging,
	}

	apiKeyDefault := os.Getenv(APIKeyEnv)
	if apiKeyDefault == "" {
		apiKeyDefault = DefaultKeyValue
	}

	for _, inst := range borg.Instances {
		if inst.Endpoint == "" {
			return nil, errors.New("instance endpoint is required")
		}
		if err := validateStaticEndpoint(inst.Endpoint); err != nil {
			return nil, fmt.Errorf("instance endpoint %q is invalid: %w", inst.Endpoint, err)
		}
		if len(inst.Models) == 0 {
			return nil, fmt.Errorf("instance %q must declare at least one model", inst.Endpoint)
		}

		runtime.Instances = append(runtime.Instances, ResolvedInstance{
			Endpoint: inst.Endpoint,
			APIKey:   resolveInstanceAPIKey(inst, apiKeyDefault),
			Models:   append([]string(nil), inst.Models...),
		})
	}

	for idx := range runtime.K8SDiscover {
		selector := &runtime.K8SDiscover[idx]
		if selector.ID == "" {
			selector.ID = derivedPodSourceID(*selector)
		}
	}

	for _, service := range borg.K8SServiceDiscover {
		resolved, err := resolveServiceDiscovery(service, apiKeyDefault)
		if err != nil {
			return nil, err
		}
		runtime.K8SServiceDiscover = append(runtime.K8SServiceDiscover, resolved)
	}
	if err := validateDiscoverySourceIDs(runtime); err != nil {
		return nil, err
	}

	return runtime, nil
}

func validateStaticEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("scheme must be http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("host is required")
	}
	if parsed.User != nil {
		return errors.New("userinfo is not allowed")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("query is not allowed")
	}
	if parsed.Fragment != "" {
		return errors.New("fragment is not allowed")
	}
	return nil
}

func validateRequestLoggingEnvironment(borg BorgConfig, logging requestlog.Config) error {
	if logging.Sink != requestlog.SinkKafka || logging.Kafka.SASL.Mechanism == requestlog.SASLNone {
		return nil
	}
	reserved := map[string]struct{}{
		PortEnv: {}, ProxyConfigEnv: {}, APIKeyEnv: {}, AuthKeyEnv: {}, LegacyAuthKeyEnv: {},
		"TLS_CERT_FILE": {}, "TLS_KEY_FILE": {},
	}
	if borg.AuthKeyFromEnv != "" {
		reserved[borg.AuthKeyFromEnv] = struct{}{}
	}
	for _, entry := range borg.Instances {
		if entry.APIKeyEnv != "" {
			reserved[entry.APIKeyEnv] = struct{}{}
		}
	}
	for _, entry := range borg.K8SServiceDiscover {
		if entry.APIKeyEnv != "" {
			reserved[entry.APIKeyEnv] = struct{}{}
		}
	}
	for _, name := range []string{logging.Kafka.SASL.UsernameFromEnv, logging.Kafka.SASL.PasswordFromEnv} {
		if _, collision := reserved[name]; collision {
			return fmt.Errorf("request_logging Kafka SASL environment variable %q collides with another BORG environment variable", name)
		}
	}
	return nil
}

func resolveUpstream(configValue UpstreamConfig) (ResolvedUpstream, error) {
	timeout := DefaultResponseHeaderTimeout
	if configValue.ResponseHeaderTimeoutSeconds != nil {
		timeout = *configValue.ResponseHeaderTimeoutSeconds
	}
	if timeout < 0 {
		return ResolvedUpstream{}, errors.New("upstream response_header_timeout_seconds cannot be negative")
	}
	return ResolvedUpstream{ResponseHeaderTimeoutSeconds: timeout}, nil
}

func derivedPodSourceID(selector DiscoverySelector) string {
	namespace := selector.Namespace
	if namespace == "" {
		namespace = "default"
	}
	return fmt.Sprintf("pods:%s:%s:%s", namespace, selector.Selector, selector.ModelKey)
}

func resolveServiceDiscovery(service ServiceDiscovery, apiKeyDefault string) (ResolvedServiceDiscovery, error) {
	if service.ID == "" {
		return ResolvedServiceDiscovery{}, errors.New("k8s_service_discover entry id is required")
	}
	if (service.ServiceName == "") == (service.Selector == "") {
		return ResolvedServiceDiscovery{}, fmt.Errorf("k8s_service_discover %q must set exactly one of service_name or selector", service.ID)
	}
	if service.Port < 0 || service.Port > 65535 {
		return ResolvedServiceDiscovery{}, fmt.Errorf("k8s_service_discover %q port must be between 1 and 65535", service.ID)
	}
	if service.Port > 0 && service.PortName != "" {
		return ResolvedServiceDiscovery{}, fmt.Errorf("k8s_service_discover %q cannot set both port and port_name", service.ID)
	}
	if service.Protocol != "" && service.Protocol != "http" && service.Protocol != "https" {
		return ResolvedServiceDiscovery{}, fmt.Errorf("k8s_service_discover %q protocol must be http or https", service.ID)
	}
	if service.APIBase != "" && !strings.HasPrefix(service.APIBase, "/") {
		return ResolvedServiceDiscovery{}, fmt.Errorf("k8s_service_discover %q api_base must start with /", service.ID)
	}

	automodel := true
	if service.Automodel != nil {
		automodel = *service.Automodel
	}
	modelsPath := service.ModelsPath
	if modelsPath == "" {
		modelsPath = "/v1/models"
	}
	if !strings.HasPrefix(modelsPath, "/") {
		return ResolvedServiceDiscovery{}, fmt.Errorf("k8s_service_discover %q models_path must start with /", service.ID)
	}

	return ResolvedServiceDiscovery{
		ID:          service.ID,
		Namespace:   service.Namespace,
		ServiceName: service.ServiceName,
		Selector:    service.Selector,
		Port:        service.Port,
		PortName:    service.PortName,
		Protocol:    service.Protocol,
		APIBase:     service.APIBase,
		Models:      append([]string(nil), service.Models...),
		ModelKey:    service.ModelKey,
		Automodel:   automodel,
		ModelsPath:  modelsPath,
		APIKey:      resolveAPIKey(service.APIKey, service.APIKeyEnv, apiKeyDefault),
	}, nil
}

func validateDiscoverySourceIDs(runtime *Runtime) error {
	seen := make(map[string]struct{}, len(runtime.K8SDiscover)+len(runtime.K8SServiceDiscover))
	for _, selector := range runtime.K8SDiscover {
		if selector.ID == discovery.StaticSourceID {
			return fmt.Errorf("discovery source id %q is reserved", selector.ID)
		}
		if _, ok := seen[selector.ID]; ok {
			return fmt.Errorf("duplicate discovery source id %q", selector.ID)
		}
		seen[selector.ID] = struct{}{}
	}
	for _, service := range runtime.K8SServiceDiscover {
		if service.ID == discovery.StaticSourceID {
			return fmt.Errorf("discovery source id %q is reserved", service.ID)
		}
		if _, ok := seen[service.ID]; ok {
			return fmt.Errorf("duplicate discovery source id %q", service.ID)
		}
		seen[service.ID] = struct{}{}
	}
	return nil
}

func resolveAuthKey(configValue string, envName string) string {
	if value := os.Getenv(AuthKeyEnv); value != "" {
		return value
	}
	if envName != "" {
		if value := os.Getenv(envName); value != "" {
			return value
		}
	}
	if value := os.Getenv(LegacyAuthKeyEnv); value != "" {
		return value
	}
	if configValue != "" {
		return configValue
	}
	return DefaultKeyValue
}

func resolveAuthPrefix(configValue string) string {
	if configValue != "" {
		return configValue
	}
	return DefaultAuthPrefix
}

func resolveBackendHealth(configValue BackendHealthConfig) ResolvedBackendHealth {
	enabled := true
	if configValue.Enabled != nil {
		enabled = *configValue.Enabled
	}

	failureThreshold := configValue.FailureThreshold
	if failureThreshold <= 0 {
		failureThreshold = 3
	}

	cooldownSeconds := configValue.CooldownSeconds
	if cooldownSeconds <= 0 {
		cooldownSeconds = 30
	}

	return ResolvedBackendHealth{
		Enabled:          enabled,
		FailureThreshold: failureThreshold,
		CooldownSeconds:  cooldownSeconds,
		EjectOn500:       configValue.EjectOn500,
	}
}

func resolveMaxRequestBodyBytes(configValue *int64) (int64, error) {
	if configValue == nil {
		return DefaultMaxRequestBodyBytes, nil
	}
	if *configValue < 0 {
		return 0, errors.New("max_request_body_bytes cannot be negative")
	}
	return *configValue, nil
}

func resolveInstanceAPIKey(inst Instance, defaultValue string) string {
	return resolveAPIKey(inst.APIKey, inst.APIKeyEnv, defaultValue)
}

func resolveAPIKey(inline string, envName string, defaultValue string) string {
	if envName != "" {
		if value := os.Getenv(envName); value != "" {
			return value
		}
	}
	if inline != "" {
		return inline
	}
	return defaultValue
}
