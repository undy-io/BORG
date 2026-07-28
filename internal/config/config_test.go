package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/undy-io/BORG/internal/discovery"
)

func TestResolvePathHostAndPort(t *testing.T) {
	t.Setenv(ProxyConfigEnv, "env.yaml")
	t.Setenv(PortEnv, "9000")

	if got := ResolveConfigPath(""); got != "env.yaml" {
		t.Fatalf("expected env config path, got %q", got)
	}
	if got := ResolveConfigPath("flag.yaml"); got != "flag.yaml" {
		t.Fatalf("expected flag config path, got %q", got)
	}
	if got := ResolveHost(""); got != DefaultHost {
		t.Fatalf("expected default host, got %q", got)
	}
	if got := ResolveHost("127.0.0.1"); got != "127.0.0.1" {
		t.Fatalf("expected flag host, got %q", got)
	}

	port, err := ResolvePort("")
	if err != nil {
		t.Fatal(err)
	}
	if port != 9000 {
		t.Fatalf("expected env port, got %d", port)
	}

	port, err = ResolvePort("9001")
	if err != nil {
		t.Fatal(err)
	}
	if port != 9001 {
		t.Fatalf("expected flag port, got %d", port)
	}
}

func TestLoadYAMLAndResolveRuntime(t *testing.T) {
	t.Setenv(APIKeyEnv, "sk-default")
	t.Setenv("VLLM_APIKEY_1", "sk-env")
	t.Setenv("VLLM_APIKEY_MISSING", "")

	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, `
borg:
  auth_key: "EMPTY"
  auth_prefix: "BORG:"
  update_interval: 30
  instances:
    - endpoint: "http://upstream-one"
      apikeyEnv: "VLLM_APIKEY_1"
      apikey: "sk-env-loses"
      models: ["m1"]
    - endpoint: "http://upstream-two"
      apikey: "sk-inline"
      models: ["m2"]
    - endpoint: "http://upstream-three"
      models: ["m3"]
    - endpoint: "http://upstream-four"
      apikeyEnv: "VLLM_APIKEY_MISSING"
      apikey: "sk-inline-fallback"
      models: ["m4"]
`)

	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AuthKey != DefaultKeyValue {
		t.Fatalf("expected EMPTY auth key, got %q", runtime.AuthKey)
	}
	if runtime.AuthPrefix != "BORG:" {
		t.Fatalf("expected BORG prefix, got %q", runtime.AuthPrefix)
	}
	if runtime.UpdateInterval != 30 {
		t.Fatalf("expected update interval 30, got %d", runtime.UpdateInterval)
	}
	if runtime.MaxRequestBodyBytes != DefaultMaxRequestBodyBytes {
		t.Fatalf("expected default max request body bytes %d, got %d", DefaultMaxRequestBodyBytes, runtime.MaxRequestBodyBytes)
	}
	if !runtime.BackendHealth.Enabled {
		t.Fatal("expected backend health to default enabled")
	}
	if runtime.BackendHealth.FailureThreshold != 3 {
		t.Fatalf("expected backend health threshold 3, got %d", runtime.BackendHealth.FailureThreshold)
	}
	if runtime.BackendHealth.CooldownSeconds != 30 {
		t.Fatalf("expected backend health cooldown 30, got %d", runtime.BackendHealth.CooldownSeconds)
	}
	if runtime.Upstream.ResponseHeaderTimeoutSeconds != DefaultResponseHeaderTimeout {
		t.Fatalf("expected response-header timeout %d, got %d", DefaultResponseHeaderTimeout, runtime.Upstream.ResponseHeaderTimeoutSeconds)
	}

	assertInstanceKey(t, runtime.Instances, "http://upstream-one", "sk-env")
	assertInstanceKey(t, runtime.Instances, "http://upstream-two", "sk-inline")
	assertInstanceKey(t, runtime.Instances, "http://upstream-three", "sk-default")
	assertInstanceKey(t, runtime.Instances, "http://upstream-four", "sk-inline-fallback")
}

func TestUpstreamResponseHeaderTimeoutConfiguration(t *testing.T) {
	custom := 900
	runtime, err := ResolveRuntime(&File{Borg: BorgConfig{
		Upstream: UpstreamConfig{ResponseHeaderTimeoutSeconds: &custom},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Upstream.ResponseHeaderTimeoutSeconds != custom {
		t.Fatalf("expected custom timeout %d, got %d", custom, runtime.Upstream.ResponseHeaderTimeoutSeconds)
	}

	unlimited := 0
	runtime, err = ResolveRuntime(&File{Borg: BorgConfig{
		Upstream: UpstreamConfig{ResponseHeaderTimeoutSeconds: &unlimited},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Upstream.ResponseHeaderTimeoutSeconds != 0 {
		t.Fatalf("expected unlimited timeout, got %d", runtime.Upstream.ResponseHeaderTimeoutSeconds)
	}

	negative := -1
	if _, err := ResolveRuntime(&File{Borg: BorgConfig{
		Upstream: UpstreamConfig{ResponseHeaderTimeoutSeconds: &negative},
	}}); err == nil {
		t.Fatal("expected negative response-header timeout to fail")
	}
}

func TestMaxRequestBodyBytesConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		value     *int64
		want      int64
		wantError bool
	}{
		{name: "omitted", want: DefaultMaxRequestBodyBytes},
		{name: "unlimited", value: int64Pointer(0), want: 0},
		{name: "positive", value: int64Pointer(1024), want: 1024},
		{name: "negative", value: int64Pointer(-1), wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, err := ResolveRuntime(&File{Borg: BorgConfig{
				MaxRequestBodyBytes: test.value,
			}})
			if test.wantError {
				if err == nil {
					t.Fatal("expected negative request body limit to fail")
				}
				if !strings.Contains(err.Error(), "max_request_body_bytes") {
					t.Fatalf("expected request body limit error, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if runtime.MaxRequestBodyBytes != test.want {
				t.Fatalf("expected max request body bytes %d, got %d", test.want, runtime.MaxRequestBodyBytes)
			}
		})
	}
}

func TestNegativeMaxRequestBodyBytesRejectedFromConfigFiles(t *testing.T) {
	tests := []struct {
		name    string
		ext     string
		content string
	}{
		{name: "YAML", ext: ".yaml", content: "borg:\n  max_request_body_bytes: -1\n"},
		{name: "JSON", ext: ".json", content: `{"borg":{"max_request_body_bytes":-1}}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config"+test.ext)
			writeFile(t, path, test.content)
			_, err := Load(path)
			if err == nil {
				t.Fatal("expected negative request body limit to fail")
			}
			if !strings.Contains(err.Error(), "max_request_body_bytes") {
				t.Fatalf("expected request body limit error, got %v", err)
			}
		})
	}
}

func TestExampleConfigLoads(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.yaml")
	if _, err := Load(path); err != nil {
		t.Fatalf("load config.example.yaml: %v", err)
	}
}

func TestRuntimeOptionalDefaultsCanBeOverridden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, `
borg:
  max_request_body_bytes: 0
  backend_health:
    enabled: false
    failure_threshold: 5
    cooldown_seconds: 7
    eject_on_500: true
`)

	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.MaxRequestBodyBytes != 0 {
		t.Fatalf("expected unlimited max request body bytes, got %d", runtime.MaxRequestBodyBytes)
	}
	if runtime.BackendHealth.Enabled {
		t.Fatal("expected backend health to be disabled")
	}
	if runtime.BackendHealth.FailureThreshold != 5 {
		t.Fatalf("expected threshold 5, got %d", runtime.BackendHealth.FailureThreshold)
	}
	if runtime.BackendHealth.CooldownSeconds != 7 {
		t.Fatalf("expected cooldown 7, got %d", runtime.BackendHealth.CooldownSeconds)
	}
	if !runtime.BackendHealth.EjectOn500 {
		t.Fatal("expected eject_on_500 to be true")
	}
}

func TestDiscoverySourcesResolveDefaultsAndCredentials(t *testing.T) {
	t.Setenv("LLMD_API_KEY", "sk-router")
	runtime, err := ResolveRuntime(&File{Borg: BorgConfig{
		K8SDiscover: []DiscoverySelector{{Namespace: "pods", Selector: "app=vllm", ModelKey: "borg/models"}},
		K8SServiceDiscover: []ServiceDiscovery{{
			ID:          "llmd-qwen",
			Namespace:   "services",
			ServiceName: "qwen-epp",
			PortName:    "http",
			APIKeyEnv:   "LLMD_API_KEY",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.K8SDiscover[0].ID; got != "pods:pods:app=vllm:borg/models" {
		t.Fatalf("unexpected derived pod source ID %q", got)
	}
	service := runtime.K8SServiceDiscover[0]
	if service.ID != "llmd-qwen" || service.ServiceName != "qwen-epp" || service.ModelsPath != "/v1/models" || !service.Automodel {
		t.Fatalf("unexpected resolved service discovery: %#v", service)
	}
	if service.APIKey != "sk-router" {
		t.Fatalf("expected service API key from env, got %q", service.APIKey)
	}
}

func TestServiceDiscoveryValidation(t *testing.T) {
	tests := []struct {
		name    string
		service ServiceDiscovery
	}{
		{name: "missing id", service: ServiceDiscovery{ServiceName: "router"}},
		{name: "missing target", service: ServiceDiscovery{ID: "router"}},
		{name: "name and selector", service: ServiceDiscovery{ID: "router", ServiceName: "router", Selector: "app=router"}},
		{name: "port conflict", service: ServiceDiscovery{ID: "router", ServiceName: "router", Port: 80, PortName: "http"}},
		{name: "bad protocol", service: ServiceDiscovery{ID: "router", ServiceName: "router", Protocol: "grpc"}},
		{name: "bad api base", service: ServiceDiscovery{ID: "router", ServiceName: "router", APIBase: "openai"}},
		{name: "bad models path", service: ServiceDiscovery{ID: "router", ServiceName: "router", ModelsPath: "v1/models"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ResolveRuntime(&File{Borg: BorgConfig{K8SServiceDiscover: []ServiceDiscovery{tt.service}}})
			if err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDiscoverySourceIDsMustBeUnique(t *testing.T) {
	_, err := ResolveRuntime(&File{Borg: BorgConfig{
		K8SDiscover: []DiscoverySelector{{ID: "router"}},
		K8SServiceDiscover: []ServiceDiscovery{{
			ID:          "router",
			ServiceName: "router",
		}},
	}})
	if err == nil {
		t.Fatal("expected duplicate source ID error")
	}
}

func TestDiscoverySourceIDCannotReplaceStaticConfiguration(t *testing.T) {
	_, err := ResolveRuntime(&File{Borg: BorgConfig{
		K8SDiscover: []DiscoverySelector{{ID: discovery.StaticSourceID}},
	}})
	if err == nil {
		t.Fatal("expected reserved static source ID error")
	}
}

func TestBackendAPIKeyDefaultsToEmpty(t *testing.T) {
	t.Setenv(APIKeyEnv, "")

	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, `
borg:
  instances:
    - endpoint: "http://upstream"
      models: ["m"]
`)

	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	assertInstanceKey(t, runtime.Instances, "http://upstream", DefaultKeyValue)
}

func TestLoadJSONAndAuthKeyPrecedence(t *testing.T) {
	configKey := base64.URLEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	legacyKey := base64.URLEncoding.EncodeToString([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	authKey := base64.URLEncoding.EncodeToString([]byte("cccccccccccccccccccccccccccccccc"))
	t.Setenv(LegacyAuthKeyEnv, legacyKey)
	t.Setenv(AuthKeyEnv, authKey)

	path := filepath.Join(t.TempDir(), "config.json")
	writeFile(t, path, `{"borg":{"auth_key":"`+configKey+`","instances":[]}}`)

	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AuthKey != authKey {
		t.Fatalf("expected AUTH_KEY precedence, got %q", runtime.AuthKey)
	}
	if runtime.AuthPrefix != DefaultAuthPrefix {
		t.Fatalf("expected default auth prefix, got %q", runtime.AuthPrefix)
	}
}

func TestLegacyAuthKeyPrecedence(t *testing.T) {
	configKey := base64.URLEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	legacyKey := base64.URLEncoding.EncodeToString([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	t.Setenv(LegacyAuthKeyEnv, legacyKey)

	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, "borg:\n  auth_key: \""+configKey+"\"\n")

	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AuthKey != legacyKey {
		t.Fatalf("expected BORG_AUTH_KEY precedence, got %q", runtime.AuthKey)
	}
}

func TestAuthKeyFromEnvPrecedence(t *testing.T) {
	configKey := base64.URLEncoding.EncodeToString([]byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	fromEnvKey := base64.URLEncoding.EncodeToString([]byte("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"))
	legacyKey := base64.URLEncoding.EncodeToString([]byte("cccccccccccccccccccccccccccccccc"))
	t.Setenv("CHART_AUTH_KEY", fromEnvKey)
	t.Setenv(LegacyAuthKeyEnv, legacyKey)

	path := filepath.Join(t.TempDir(), "config.yaml")
	writeFile(t, path, `
borg:
  auth_key_from_env: CHART_AUTH_KEY
  auth_key: "`+configKey+`"
`)

	runtime, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AuthKey != fromEnvKey {
		t.Fatalf("expected auth_key_from_env precedence, got %q", runtime.AuthKey)
	}

	authKey := base64.URLEncoding.EncodeToString([]byte("dddddddddddddddddddddddddddddddd"))
	t.Setenv(AuthKeyEnv, authKey)
	runtime, err = Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.AuthKey != authKey {
		t.Fatalf("expected AUTH_KEY to win, got %q", runtime.AuthKey)
	}
}

func assertInstanceKey(t *testing.T, instances []ResolvedInstance, endpoint string, want string) {
	t.Helper()
	for _, inst := range instances {
		if inst.Endpoint == endpoint {
			if inst.APIKey != want {
				t.Fatalf("expected %s key %q, got %q", endpoint, want, inst.APIKey)
			}
			return
		}
	}
	t.Fatalf("missing endpoint %s", endpoint)
}

func int64Pointer(value int64) *int64 {
	return &value
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
