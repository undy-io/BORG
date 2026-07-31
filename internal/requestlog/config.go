package requestlog

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	DefaultQueueCapacity          = 100_000
	DefaultQueueCapacityBytes     = int64(4 * 1024 * 1024 * 1024)
	DefaultShutdownTimeoutSeconds = 10
	DefaultMaxRequestBodyBytes    = int64(512 * 1024)
	DefaultMaxResponseBodyBytes   = int64(16 * 1024 * 1024)
	DefaultKafkaTopic             = "borg.request-events.v1"
	DefaultKafkaClientID          = "borg"
	DefaultKafkaUsernameEnv       = "BORG_KAFKA_USERNAME"
	DefaultKafkaPasswordEnv       = "BORG_KAFKA_PASSWORD"
)

type Sink string

const (
	SinkNoop  Sink = "noop"
	SinkKafka Sink = "kafka"
)

type ValueMode string

const (
	ValueModeSHA256 ValueMode = "sha256"
	ValueModeRaw    ValueMode = "raw"
)

type SASLMechanism string

const (
	SASLNone        SASLMechanism = "none"
	SASLPlain       SASLMechanism = "plain"
	SASLSCRAMSHA256 SASLMechanism = "scram-sha-256"
	SASLSCRAMSHA512 SASLMechanism = "scram-sha-512"
)

type FileConfig struct {
	Sink                   string                `json:"sink" yaml:"sink"`
	QueueCapacity          *int                  `json:"queue_capacity" yaml:"queue_capacity"`
	QueueCapacityBytes     *int64                `json:"queue_capacity_bytes" yaml:"queue_capacity_bytes"`
	ShutdownTimeoutSeconds *int                  `json:"shutdown_timeout_seconds" yaml:"shutdown_timeout_seconds"`
	Capture                FileCaptureConfig     `json:"capture" yaml:"capture"`
	SessionHeaders         []SessionHeaderConfig `json:"session_headers" yaml:"session_headers"`
	PartitionHeader        string                `json:"partition_header" yaml:"partition_header"`
	Filters                []FilterConfig        `json:"filters" yaml:"filters"`
	Kafka                  FileKafkaConfig       `json:"kafka" yaml:"kafka"`
}

type FileCaptureConfig struct {
	RequestBody             *bool    `json:"request_body" yaml:"request_body"`
	ResponseBody            *bool    `json:"response_body" yaml:"response_body"`
	RequestHeaders          *bool    `json:"request_headers" yaml:"request_headers"`
	ResponseHeaders         *bool    `json:"response_headers" yaml:"response_headers"`
	ExcludedRequestHeaders  []string `json:"excluded_request_headers" yaml:"excluded_request_headers"`
	ExcludedResponseHeaders []string `json:"excluded_response_headers" yaml:"excluded_response_headers"`
	MaxRequestBodyBytes     *int64   `json:"max_request_body_bytes" yaml:"max_request_body_bytes"`
	MaxResponseBodyBytes    *int64   `json:"max_response_body_bytes" yaml:"max_response_body_bytes"`
}

type SessionHeaderConfig struct {
	Name      string `json:"name" yaml:"name"`
	ValueMode string `json:"value_mode" yaml:"value_mode"`
}

type FilterConfig struct {
	Principals []string            `json:"principals" yaml:"principals"`
	Models     []string            `json:"models" yaml:"models"`
	Headers    map[string][]string `json:"headers" yaml:"headers"`
}

type FileKafkaConfig struct {
	Brokers  []string       `json:"brokers" yaml:"brokers"`
	Topic    string         `json:"topic" yaml:"topic"`
	ClientID string         `json:"client_id" yaml:"client_id"`
	TLS      FileTLSConfig  `json:"tls" yaml:"tls"`
	SASL     FileSASLConfig `json:"sasl" yaml:"sasl"`
}

type FileTLSConfig struct {
	Enabled    bool   `json:"enabled" yaml:"enabled"`
	ServerName string `json:"server_name" yaml:"server_name"`
	CAFile     string `json:"ca_file" yaml:"ca_file"`
	CertFile   string `json:"cert_file" yaml:"cert_file"`
	KeyFile    string `json:"key_file" yaml:"key_file"`
}

type FileSASLConfig struct {
	Mechanism       string `json:"mechanism" yaml:"mechanism"`
	UsernameFromEnv string `json:"username_from_env" yaml:"username_from_env"`
	PasswordFromEnv string `json:"password_from_env" yaml:"password_from_env"`
}

type Config struct {
	Sink                   Sink
	QueueCapacity          int
	QueueCapacityBytes     int64
	ShutdownTimeoutSeconds int
	Capture                CaptureConfig
	SessionHeaders         []SessionHeader
	PartitionHeader        string
	Matcher                *Matcher
	Kafka                  KafkaConfig
}

type CaptureConfig struct {
	RequestBody             bool
	ResponseBody            bool
	RequestHeaders          bool
	ResponseHeaders         bool
	ExcludedRequestHeaders  map[string]struct{}
	ExcludedResponseHeaders map[string]struct{}
	MaxRequestBodyBytes     int64
	MaxResponseBodyBytes    int64
}

type SessionHeader struct {
	Name      string
	ValueMode ValueMode
}

type KafkaConfig struct {
	Brokers  []string
	Topic    string
	ClientID string
	TLS      TLSConfig
	SASL     SASLConfig
}

type TLSConfig struct {
	Enabled    bool
	ServerName string
	CAFile     string
	CertFile   string
	KeyFile    string
}

type SASLConfig struct {
	Mechanism       SASLMechanism
	UsernameFromEnv string
	PasswordFromEnv string
	Username        string
	Password        string
}

type LookupEnv func(string) (string, bool)

func Resolve(file FileConfig, lookupEnv LookupEnv) (Config, error) {
	if lookupEnv == nil {
		lookupEnv = func(string) (string, bool) { return "", false }
	}

	sink := Sink(file.Sink)
	if sink == "" {
		sink = SinkNoop
	}
	if sink != SinkNoop && sink != SinkKafka {
		return Config{}, fmt.Errorf("request_logging sink %q is unsupported", file.Sink)
	}

	queueCapacity := valueOr(file.QueueCapacity, DefaultQueueCapacity)
	if queueCapacity <= 0 {
		return Config{}, errors.New("request_logging queue_capacity must be positive")
	}
	queueCapacityBytes := valueOr(file.QueueCapacityBytes, DefaultQueueCapacityBytes)
	if queueCapacityBytes <= 0 {
		return Config{}, errors.New("request_logging queue_capacity_bytes must be positive")
	}
	maxInt := int64(^uint(0) >> 1)
	if queueCapacityBytes > maxInt {
		return Config{}, fmt.Errorf("request_logging queue_capacity_bytes %d exceeds this architecture's maximum supported value %d", queueCapacityBytes, maxInt)
	}
	shutdownTimeout := valueOr(file.ShutdownTimeoutSeconds, DefaultShutdownTimeoutSeconds)
	if shutdownTimeout <= 0 {
		return Config{}, errors.New("request_logging shutdown_timeout_seconds must be positive")
	}

	capture, err := resolveCapture(file.Capture)
	if err != nil {
		return Config{}, err
	}
	sessionHeaders, err := resolveSessionHeaders(file.SessionHeaders)
	if err != nil {
		return Config{}, err
	}
	partitionHeader := strings.ToLower(file.PartitionHeader)
	if file.PartitionHeader != "" {
		if !validHeaderName(file.PartitionHeader) {
			return Config{}, fmt.Errorf("request_logging partition_header %q is not a valid HTTP header name", file.PartitionHeader)
		}
	}

	matcher, err := CompileMatcher(file.Filters)
	if err != nil {
		return Config{}, err
	}
	kafka, err := resolveKafka(file.Kafka, sink, lookupEnv)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Sink:                   sink,
		QueueCapacity:          queueCapacity,
		QueueCapacityBytes:     queueCapacityBytes,
		ShutdownTimeoutSeconds: shutdownTimeout,
		Capture:                capture,
		SessionHeaders:         sessionHeaders,
		PartitionHeader:        partitionHeader,
		Matcher:                matcher,
		Kafka:                  kafka,
	}, nil
}

func (c Config) Enabled() bool {
	return c.Sink != SinkNoop
}

func resolveCapture(file FileCaptureConfig) (CaptureConfig, error) {
	requestBody := valueOr(file.RequestBody, true)
	responseBody := valueOr(file.ResponseBody, true)
	requestHeaders := valueOr(file.RequestHeaders, false)
	responseHeaders := valueOr(file.ResponseHeaders, false)
	maxRequest := valueOr(file.MaxRequestBodyBytes, DefaultMaxRequestBodyBytes)
	maxResponse := valueOr(file.MaxResponseBodyBytes, DefaultMaxResponseBodyBytes)
	if maxRequest < 0 {
		return CaptureConfig{}, errors.New("request_logging capture.max_request_body_bytes cannot be negative")
	}
	if maxResponse < 0 {
		return CaptureConfig{}, errors.New("request_logging capture.max_response_body_bytes cannot be negative")
	}
	excludedRequestHeaders, err := resolveExcludedHeaders("excluded_request_headers", file.ExcludedRequestHeaders)
	if err != nil {
		return CaptureConfig{}, err
	}
	excludedResponseHeaders, err := resolveExcludedHeaders("excluded_response_headers", file.ExcludedResponseHeaders)
	if err != nil {
		return CaptureConfig{}, err
	}
	return CaptureConfig{
		RequestBody:             requestBody,
		ResponseBody:            responseBody,
		RequestHeaders:          requestHeaders,
		ResponseHeaders:         responseHeaders,
		ExcludedRequestHeaders:  excludedRequestHeaders,
		ExcludedResponseHeaders: excludedResponseHeaders,
		MaxRequestBodyBytes:     maxRequest,
		MaxResponseBodyBytes:    maxResponse,
	}, nil
}

func resolveExcludedHeaders(field string, values []string) (map[string]struct{}, error) {
	resolved := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validHeaderName(value) {
			return nil, fmt.Errorf("request_logging capture.%s header %q is not a valid HTTP header name", field, value)
		}
		name := strings.ToLower(value)
		if _, duplicate := resolved[name]; duplicate {
			return nil, fmt.Errorf("request_logging capture.%s header %q is declared more than once", field, value)
		}
		resolved[name] = struct{}{}
	}
	return resolved, nil
}

func resolveSessionHeaders(files []SessionHeaderConfig) ([]SessionHeader, error) {
	resolved := make([]SessionHeader, 0, len(files))
	declared := make(map[string]struct{}, len(files))
	for _, file := range files {
		if !validHeaderName(file.Name) {
			return nil, fmt.Errorf("request_logging session header %q is not a valid HTTP header name", file.Name)
		}
		name := strings.ToLower(file.Name)
		if _, duplicate := declared[name]; duplicate {
			return nil, fmt.Errorf("request_logging session header %q is declared more than once", file.Name)
		}
		mode := ValueMode(file.ValueMode)
		if mode == "" {
			mode = ValueModeSHA256
		}
		if mode != ValueModeSHA256 && mode != ValueModeRaw {
			return nil, fmt.Errorf("request_logging session header %q has unsupported value_mode %q", file.Name, file.ValueMode)
		}
		declared[name] = struct{}{}
		resolved = append(resolved, SessionHeader{Name: name, ValueMode: mode})
	}
	return resolved, nil
}

func resolveKafka(file FileKafkaConfig, sink Sink, lookupEnv LookupEnv) (KafkaConfig, error) {
	topic := file.Topic
	if topic == "" {
		topic = DefaultKafkaTopic
	}
	clientID := file.ClientID
	if clientID == "" {
		clientID = DefaultKafkaClientID
	}
	mechanism := SASLMechanism(file.SASL.Mechanism)
	if mechanism == "" {
		mechanism = SASLNone
	}
	usernameEnv := file.SASL.UsernameFromEnv
	if usernameEnv == "" {
		usernameEnv = DefaultKafkaUsernameEnv
	}
	passwordEnv := file.SASL.PasswordFromEnv
	if passwordEnv == "" {
		passwordEnv = DefaultKafkaPasswordEnv
	}

	resolved := KafkaConfig{
		Brokers:  append([]string(nil), file.Brokers...),
		Topic:    topic,
		ClientID: clientID,
		TLS: TLSConfig{
			Enabled:    file.TLS.Enabled,
			ServerName: file.TLS.ServerName,
			CAFile:     file.TLS.CAFile,
			CertFile:   file.TLS.CertFile,
			KeyFile:    file.TLS.KeyFile,
		},
		SASL: SASLConfig{
			Mechanism:       mechanism,
			UsernameFromEnv: usernameEnv,
			PasswordFromEnv: passwordEnv,
		},
	}
	if sink != SinkKafka {
		return resolved, nil
	}

	if len(resolved.Brokers) == 0 {
		return KafkaConfig{}, errors.New("request_logging kafka.brokers must contain at least one broker")
	}
	for _, broker := range resolved.Brokers {
		if strings.TrimSpace(broker) == "" {
			return KafkaConfig{}, errors.New("request_logging kafka.brokers cannot contain an empty broker")
		}
	}
	if strings.TrimSpace(resolved.Topic) == "" {
		return KafkaConfig{}, errors.New("request_logging kafka.topic must be non-empty")
	}
	if strings.TrimSpace(resolved.ClientID) == "" {
		return KafkaConfig{}, errors.New("request_logging kafka.client_id must be non-empty")
	}
	if (resolved.TLS.CertFile == "") != (resolved.TLS.KeyFile == "") {
		return KafkaConfig{}, errors.New("request_logging Kafka TLS cert_file and key_file must be configured together")
	}
	if !resolved.TLS.Enabled && (resolved.TLS.ServerName != "" || resolved.TLS.CAFile != "" || resolved.TLS.CertFile != "" || resolved.TLS.KeyFile != "") {
		return KafkaConfig{}, errors.New("request_logging Kafka TLS files and server_name require tls.enabled=true")
	}

	switch mechanism {
	case SASLNone:
		return resolved, nil
	case SASLPlain, SASLSCRAMSHA256, SASLSCRAMSHA512:
	default:
		return KafkaConfig{}, fmt.Errorf("request_logging Kafka SASL mechanism %q is unsupported", mechanism)
	}
	if !portableEnvName(usernameEnv) || !portableEnvName(passwordEnv) {
		return KafkaConfig{}, errors.New("request_logging Kafka SASL environment names must use portable syntax")
	}
	if usernameEnv == passwordEnv {
		return KafkaConfig{}, errors.New("request_logging Kafka SASL username and password environment names must be different")
	}
	username, usernameOK := lookupEnv(usernameEnv)
	password, passwordOK := lookupEnv(passwordEnv)
	if !usernameOK || username == "" {
		return KafkaConfig{}, fmt.Errorf("request_logging Kafka SASL username environment variable %q is empty", usernameEnv)
	}
	if !passwordOK || password == "" {
		return KafkaConfig{}, fmt.Errorf("request_logging Kafka SASL password environment variable %q is empty", passwordEnv)
	}
	resolved.SASL.Username = username
	resolved.SASL.Password = password
	return resolved, nil
}

var portableEnvPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func portableEnvName(value string) bool {
	return portableEnvPattern.MatchString(value)
}

func validHeaderName(value string) bool {
	if value == "" {
		return false
	}
	for idx := 0; idx < len(value); idx++ {
		ch := value[idx]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' {
			continue
		}
		switch ch {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func valueOr[T any](value *T, fallback T) T {
	if value == nil {
		return fallback
	}
	return *value
}
