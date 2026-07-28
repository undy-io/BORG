package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
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

	assertInstanceKey(t, runtime.Instances, "http://upstream-one", "sk-env")
	assertInstanceKey(t, runtime.Instances, "http://upstream-two", "sk-inline")
	assertInstanceKey(t, runtime.Instances, "http://upstream-three", "sk-default")
	assertInstanceKey(t, runtime.Instances, "http://upstream-four", "sk-inline-fallback")
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

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
