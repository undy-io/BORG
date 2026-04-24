package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

const (
	DefaultConfigPath = "config.yaml"
	DefaultHost       = "0.0.0.0"
	DefaultPort       = 8000
	DefaultKeyValue   = "EMPTY"
	DefaultAuthPrefix = "PROXY:"

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
	AuthKey        string              `json:"auth_key" yaml:"auth_key"`
	AuthPrefix     string              `json:"auth_prefix" yaml:"auth_prefix"`
	Instances      []Instance          `json:"instances" yaml:"instances"`
	UpdateInterval int                 `json:"update_interval" yaml:"update_interval"`
	K8SDiscover    []DiscoverySelector `json:"k8s_discover" yaml:"k8s_discover"`
}

type Instance struct {
	Endpoint  string   `json:"endpoint" yaml:"endpoint"`
	APIKey    string   `json:"apikey" yaml:"apikey"`
	APIKeyEnv string   `json:"apikeyEnv" yaml:"apikeyEnv"`
	Models    []string `json:"models" yaml:"models"`
}

type DiscoverySelector struct {
	Namespace string `json:"namespace" yaml:"namespace"`
	Selector  string `json:"selector" yaml:"selector"`
	ModelKey  string `json:"modelkey" yaml:"modelkey"`
}

type Runtime struct {
	AuthKey        string
	AuthPrefix     string
	Instances      []ResolvedInstance
	UpdateInterval int
	K8SDiscover    []DiscoverySelector
}

type ResolvedInstance struct {
	Endpoint string
	APIKey   string
	Models   []string
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
	runtime := &Runtime{
		AuthKey:        resolveAuthKey(borg.AuthKey),
		AuthPrefix:     resolveAuthPrefix(borg.AuthPrefix),
		UpdateInterval: borg.UpdateInterval,
		K8SDiscover:    append([]DiscoverySelector(nil), borg.K8SDiscover...),
	}

	apiKeyDefault := os.Getenv(APIKeyEnv)
	if apiKeyDefault == "" {
		apiKeyDefault = DefaultKeyValue
	}

	for _, inst := range borg.Instances {
		if inst.Endpoint == "" {
			return nil, errors.New("instance endpoint is required")
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

	return runtime, nil
}

func resolveAuthKey(configValue string) string {
	if value := os.Getenv(AuthKeyEnv); value != "" {
		return value
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

func resolveInstanceAPIKey(inst Instance, defaultValue string) string {
	if inst.APIKeyEnv != "" {
		if value := os.Getenv(inst.APIKeyEnv); value != "" {
			return value
		}
	}
	if inst.APIKey != "" {
		return inst.APIKey
	}
	return defaultValue
}
