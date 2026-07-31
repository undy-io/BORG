package requestlog

import (
	"reflect"
	"strings"
	"testing"
)

func ptr[T any](value T) *T { return &value }

func TestResolveDefaults(t *testing.T) {
	got, err := Resolve(FileConfig{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sink != SinkNoop || got.Enabled() {
		t.Fatalf("expected disabled noop sink, got %#v", got)
	}
	if got.QueueCapacity != DefaultQueueCapacity || got.QueueCapacityBytes != DefaultQueueCapacityBytes {
		t.Fatalf("unexpected queue defaults: %#v", got)
	}
	if got.ShutdownTimeoutSeconds != DefaultShutdownTimeoutSeconds {
		t.Fatalf("unexpected shutdown timeout %d", got.ShutdownTimeoutSeconds)
	}
	if !got.Capture.RequestBody || !got.Capture.ResponseBody ||
		got.Capture.RequestHeaders || got.Capture.ResponseHeaders ||
		len(got.Capture.ExcludedRequestHeaders) != 0 || len(got.Capture.ExcludedResponseHeaders) != 0 ||
		got.Capture.MaxRequestBodyBytes != DefaultMaxRequestBodyBytes ||
		got.Capture.MaxResponseBodyBytes != DefaultMaxResponseBodyBytes {
		t.Fatalf("unexpected capture defaults: %#v", got.Capture)
	}
	if got.Kafka.Topic != DefaultKafkaTopic || got.Kafka.ClientID != DefaultKafkaClientID || got.Kafka.SASL.Mechanism != SASLNone {
		t.Fatalf("unexpected Kafka defaults: %#v", got.Kafka)
	}
	if got.Matcher.Match("any", "model", nil) {
		t.Fatal("omitted filters must deny capture")
	}
}

func TestResolveCaptureZeroAndDisabled(t *testing.T) {
	got, err := Resolve(FileConfig{Capture: FileCaptureConfig{
		RequestBody:             ptr(false),
		ResponseBody:            ptr(false),
		RequestHeaders:          ptr(true),
		ResponseHeaders:         ptr(true),
		ExcludedRequestHeaders:  []string{"Authorization"},
		ExcludedResponseHeaders: []string{"Set-Cookie"},
		MaxRequestBodyBytes:     ptr[int64](0),
		MaxResponseBodyBytes:    ptr[int64](0),
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Capture.RequestBody || got.Capture.ResponseBody || !got.Capture.RequestHeaders || !got.Capture.ResponseHeaders ||
		got.Capture.MaxRequestBodyBytes != 0 || got.Capture.MaxResponseBodyBytes != 0 {
		t.Fatalf("zero and false values were not preserved: %#v", got.Capture)
	}
	if _, ok := got.Capture.ExcludedRequestHeaders["authorization"]; !ok {
		t.Fatalf("request exclusion was not normalized: %#v", got.Capture.ExcludedRequestHeaders)
	}
	if _, ok := got.Capture.ExcludedResponseHeaders["set-cookie"]; !ok {
		t.Fatalf("response exclusion was not normalized: %#v", got.Capture.ExcludedResponseHeaders)
	}
}

func TestResolveRejectsInvalidGenericConfiguration(t *testing.T) {
	tests := []struct {
		name string
		file FileConfig
		want string
	}{
		{name: "sink", file: FileConfig{Sink: "file"}, want: "unsupported"},
		{name: "record capacity", file: FileConfig{QueueCapacity: ptr(0)}, want: "queue_capacity"},
		{name: "byte capacity", file: FileConfig{QueueCapacityBytes: ptr[int64](-1)}, want: "queue_capacity_bytes"},
		{name: "shutdown", file: FileConfig{ShutdownTimeoutSeconds: ptr(0)}, want: "shutdown_timeout_seconds"},
		{name: "request capture", file: FileConfig{Capture: FileCaptureConfig{MaxRequestBodyBytes: ptr[int64](-1)}}, want: "max_request_body_bytes"},
		{name: "response capture", file: FileConfig{Capture: FileCaptureConfig{MaxResponseBodyBytes: ptr[int64](-1)}}, want: "max_response_body_bytes"},
		{name: "bad header", file: FileConfig{SessionHeaders: []SessionHeaderConfig{{Name: "bad header"}}}, want: "valid HTTP header"},
		{name: "duplicate header", file: FileConfig{SessionHeaders: []SessionHeaderConfig{{Name: "X-Session"}, {Name: "x-session"}}}, want: "more than once"},
		{name: "value mode", file: FileConfig{SessionHeaders: []SessionHeaderConfig{{Name: "X-Session", ValueMode: "clear"}}}, want: "value_mode"},
		{name: "bad partition", file: FileConfig{PartitionHeader: "bad header"}, want: "valid HTTP header"},
		{name: "bad request exclusion", file: FileConfig{Capture: FileCaptureConfig{ExcludedRequestHeaders: []string{"bad header"}}}, want: "excluded_request_headers"},
		{name: "duplicate request exclusion", file: FileConfig{Capture: FileCaptureConfig{ExcludedRequestHeaders: []string{"Authorization", "authorization"}}}, want: "more than once"},
		{name: "bad response exclusion", file: FileConfig{Capture: FileCaptureConfig{ExcludedResponseHeaders: []string{"bad header"}}}, want: "excluded_response_headers"},
		{name: "duplicate response exclusion", file: FileConfig{Capture: FileCaptureConfig{ExcludedResponseHeaders: []string{"Set-Cookie", "set-cookie"}}}, want: "more than once"},
		{name: "empty principal patterns", file: FileConfig{Filters: []FilterConfig{{Principals: []string{}}}}, want: "cannot be an empty list"},
		{name: "empty model patterns", file: FileConfig{Filters: []FilterConfig{{Models: []string{}}}}, want: "cannot be an empty list"},
		{name: "empty header patterns", file: FileConfig{SessionHeaders: []SessionHeaderConfig{{Name: "X-Session"}}, Filters: []FilterConfig{{Headers: map[string][]string{"X-Session": {}}}}}, want: "patterns cannot be empty"},
		{name: "invalid regex", file: FileConfig{Filters: []FilterConfig{{Models: []string{"["}}}}, want: "invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Resolve(test.file, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestResolveAllowsSensitiveAndIndependentHeaderUses(t *testing.T) {
	got, err := Resolve(FileConfig{
		SessionHeaders:  []SessionHeaderConfig{{Name: "Authorization", ValueMode: "raw"}},
		PartitionHeader: "X-Partition-Only",
		Filters: []FilterConfig{{Headers: map[string][]string{
			"X-Filter-Only": {"^allowed$"},
		}}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.SessionHeaders) != 1 || got.SessionHeaders[0].Name != "authorization" || got.PartitionHeader != "x-partition-only" {
		t.Fatalf("header uses were not resolved independently: %#v", got)
	}
}

func TestResolveKafkaValidationAndCredentials(t *testing.T) {
	base := FileConfig{Sink: "kafka", Kafka: FileKafkaConfig{Brokers: []string{"kafka:9092"}}}
	got, err := Resolve(base, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kafka.Topic != DefaultKafkaTopic || got.Kafka.SASL.Mechanism != SASLNone {
		t.Fatalf("unexpected Kafka config: %#v", got.Kafka)
	}

	withSASL := base
	withSASL.Kafka.SASL = FileSASLConfig{
		Mechanism:       "scram-sha-512",
		UsernameFromEnv: "KAFKA_USER",
		PasswordFromEnv: "KAFKA_PASSWORD",
	}
	got, err = Resolve(withSASL, func(name string) (string, bool) {
		values := map[string]string{"KAFKA_USER": "user", "KAFKA_PASSWORD": "secret"}
		value, ok := values[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kafka.SASL.Username != "user" || got.Kafka.SASL.Password != "secret" {
		t.Fatalf("credentials were not resolved: %#v", got.Kafka.SASL)
	}
}

func TestResolveKafkaDefaultCredentialEnvironmentNames(t *testing.T) {
	got, err := Resolve(FileConfig{
		Sink: "kafka",
		Kafka: FileKafkaConfig{
			Brokers: []string{"kafka:9092"},
			SASL:    FileSASLConfig{Mechanism: "plain"},
		},
	}, func(name string) (string, bool) {
		values := map[string]string{DefaultKafkaUsernameEnv: "user", DefaultKafkaPasswordEnv: "password"}
		value, ok := values[name]
		return value, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Kafka.SASL.UsernameFromEnv != DefaultKafkaUsernameEnv || got.Kafka.SASL.PasswordFromEnv != DefaultKafkaPasswordEnv {
		t.Fatalf("unexpected default environment names: %#v", got.Kafka.SASL)
	}
}

func TestResolveKafkaOnlyValidation(t *testing.T) {
	invalidKafka := FileKafkaConfig{
		TLS:  FileTLSConfig{CAFile: "/missing"},
		SASL: FileSASLConfig{Mechanism: "oauth"},
	}
	if _, err := Resolve(FileConfig{Kafka: invalidKafka}, nil); err != nil {
		t.Fatalf("noop must ignore Kafka-only validation: %v", err)
	}

	tests := []struct {
		name string
		file FileKafkaConfig
		want string
	}{
		{name: "brokers", file: FileKafkaConfig{}, want: "brokers"},
		{name: "empty broker", file: FileKafkaConfig{Brokers: []string{" "}}, want: "empty broker"},
		{name: "certificate pair", file: FileKafkaConfig{Brokers: []string{"k:1"}, TLS: FileTLSConfig{Enabled: true, CertFile: "cert"}}, want: "configured together"},
		{name: "disabled TLS material", file: FileKafkaConfig{Brokers: []string{"k:1"}, TLS: FileTLSConfig{CAFile: "ca"}}, want: "enabled=true"},
		{name: "unsupported SASL", file: FileKafkaConfig{Brokers: []string{"k:1"}, SASL: FileSASLConfig{Mechanism: "oauth"}}, want: "unsupported"},
		{name: "bad env", file: FileKafkaConfig{Brokers: []string{"k:1"}, SASL: FileSASLConfig{Mechanism: "plain", UsernameFromEnv: "BAD-NAME", PasswordFromEnv: "PASS"}}, want: "portable syntax"},
		{name: "missing username", file: FileKafkaConfig{Brokers: []string{"k:1"}, SASL: FileSASLConfig{Mechanism: "plain", UsernameFromEnv: "USER", PasswordFromEnv: "PASS"}}, want: "USER"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Resolve(FileConfig{Sink: "kafka", Kafka: test.file}, nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("expected error containing %q, got %v", test.want, err)
			}
		})
	}
}

func TestResolveDoesNotMutateInput(t *testing.T) {
	file := FileConfig{
		Capture: FileCaptureConfig{
			ExcludedRequestHeaders:  []string{"Authorization"},
			ExcludedResponseHeaders: []string{"Set-Cookie"},
		},
		SessionHeaders:  []SessionHeaderConfig{{Name: "X-Session", ValueMode: "raw"}},
		PartitionHeader: "X-Session",
		Filters:         []FilterConfig{{Headers: map[string][]string{"X-Session": {".+"}}}},
		Kafka:           FileKafkaConfig{Brokers: []string{"one:9092"}},
	}
	want := FileConfig{
		Capture: FileCaptureConfig{
			ExcludedRequestHeaders:  append([]string(nil), file.Capture.ExcludedRequestHeaders...),
			ExcludedResponseHeaders: append([]string(nil), file.Capture.ExcludedResponseHeaders...),
		},
		SessionHeaders:  append([]SessionHeaderConfig(nil), file.SessionHeaders...),
		PartitionHeader: file.PartitionHeader,
		Filters:         []FilterConfig{{Headers: map[string][]string{"X-Session": append([]string(nil), file.Filters[0].Headers["X-Session"]...)}}},
		Kafka:           FileKafkaConfig{Brokers: append([]string(nil), file.Kafka.Brokers...)},
	}
	if _, err := Resolve(file, nil); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(file, want) {
		t.Fatalf("Resolve mutated input\n got: %#v\nwant: %#v", file, want)
	}
}
