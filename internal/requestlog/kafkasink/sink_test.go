package kafkasink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/undy-io/BORG/internal/requestlog"
)

type fakeProducer struct {
	records []*kgo.Record
	aborted bool
	closed  bool
}

func TestKfakeDeliversKeysValuesAndOrder(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "events"))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()

	config := kafkaConfig()
	config.Kafka.Brokers = cluster.ListenAddrs()
	sink, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := requestlog.NewExporter(config.QueueCapacity, config.QueueCapacityBytes, sink)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"one", "two"} {
		if !exporter.TryExport(requestlog.Record{Key: []byte("request-1"), Value: []byte(value)}) {
			t.Fatalf("record %q was not admitted", value)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exporter.Close(ctx); err != nil {
		t.Fatal(err)
	}

	consumer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.ConsumeTopics("events"))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	fetches := consumer.PollRecords(ctx, 2)
	if errs := fetches.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	records := fetches.Records()
	if len(records) != 2 || string(records[0].Key) != "request-1" || string(records[1].Key) != "request-1" || string(records[0].Value) != "one" || string(records[1].Value) != "two" {
		t.Fatalf("unexpected consumed records: %#v", records)
	}
}

func TestMissingTopicAndAuthenticationFailureRemainAsynchronous(t *testing.T) {
	tests := []struct {
		name    string
		cluster []kfake.Opt
		config  func(requestlog.Config) requestlog.Config
	}{
		{
			name:    "missing topic",
			cluster: []kfake.Opt{kfake.NumBrokers(1)},
			config:  func(config requestlog.Config) requestlog.Config { return config },
		},
		{
			name: "authentication",
			cluster: []kfake.Opt{
				kfake.NumBrokers(1),
				kfake.SeedTopics(1, "events"),
				kfake.EnableSASL(),
				kfake.Superuser("SCRAM-SHA-256", "borg", "correct"),
			},
			config: func(config requestlog.Config) requestlog.Config {
				config.Kafka.SASL = requestlog.SASLConfig{
					Mechanism: requestlog.SASLSCRAMSHA256,
					Username:  "borg",
					Password:  "wrong",
				}
				return config
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cluster, err := kfake.NewCluster(test.cluster...)
			if err != nil {
				t.Fatal(err)
			}
			defer cluster.Close()
			config := kafkaConfig()
			config.Kafka.Brokers = cluster.ListenAddrs()
			config = test.config(config)
			sink, err := New(config)
			if err != nil {
				t.Fatalf("remote failure affected startup: %v", err)
			}
			exporter, err := requestlog.NewExporter(config.QueueCapacity, config.QueueCapacityBytes, sink)
			if err != nil {
				t.Fatal(err)
			}
			if !exporter.TryExport(requestlog.Record{Key: []byte("key"), Value: []byte("value")}) {
				t.Fatal("record was rejected before the shared budget filled")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			if err := exporter.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected bounded flush timeout, got %v", err)
			}
			if stats := exporter.Stats(); stats != (requestlog.ExporterStats{}) {
				t.Fatalf("shutdown did not release reservations: %#v", stats)
			}
		})
	}
}

func TestMissingTopicRecordIsDeliveredAfterTopicCreation(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1))
	if err != nil {
		t.Fatal(err)
	}
	defer cluster.Close()
	metadataObserved := make(chan struct{})
	var observedOnce sync.Once
	cluster.ControlKey(int16(kmsg.Metadata), func(kmsg.Request) (kmsg.Response, error, bool) {
		observedOnce.Do(func() { close(metadataObserved) })
		cluster.DropControl()
		return nil, nil, false
	})

	config := kafkaConfig()
	config.Kafka.Brokers = cluster.ListenAddrs()
	sink, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	exporter, err := requestlog.NewExporter(config.QueueCapacity, config.QueueCapacityBytes, sink)
	if err != nil {
		t.Fatal(err)
	}
	if !exporter.TryExport(requestlog.Record{Key: []byte("retained"), Value: []byte("after-create")}) {
		t.Fatal("missing-topic record was not admitted")
	}
	select {
	case <-metadataObserved:
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not request metadata for the missing topic")
	}
	if stats := exporter.Stats(); stats.ReservedRecords != 1 {
		t.Fatalf("record was not retained while the topic was missing: %#v", stats)
	}

	admin, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	request := kmsg.NewPtrCreateTopicsRequest()
	topic := kmsg.NewCreateTopicsRequestTopic()
	topic.Topic = "events"
	topic.NumPartitions = 1
	topic.ReplicationFactor = 1
	request.Topics = append(request.Topics, topic)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := request.RequestWith(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Topics) != 1 {
		t.Fatalf("unexpected create-topic response: %#v", response)
	}
	if err := kerr.ErrorForCode(response.Topics[0].ErrorCode); err != nil {
		t.Fatal(err)
	}
	if err := exporter.Close(ctx); err != nil {
		t.Fatalf("retained record was not delivered after topic creation: %v", err)
	}

	consumer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.ConsumeTopics("events"))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	fetches := consumer.PollRecords(ctx, 1)
	if errs := fetches.Errors(); len(errs) > 0 {
		t.Fatal(errs)
	}
	records := fetches.Records()
	if len(records) != 1 || string(records[0].Key) != "retained" || string(records[0].Value) != "after-create" {
		t.Fatalf("unexpected retained record: %#v", records)
	}
}

func (p *fakeProducer) TryProduce(_ context.Context, record *kgo.Record, callback func(*kgo.Record, error)) {
	p.records = append(p.records, record)
	callback(record, nil)
}
func (*fakeProducer) Flush(context.Context) error   { return nil }
func (p *fakeProducer) UnsafeAbortBufferedRecords() { p.aborted = true }
func (p *fakeProducer) Close()                      { p.closed = true }

func kafkaConfig() requestlog.Config {
	return requestlog.Config{
		Sink:               requestlog.SinkKafka,
		QueueCapacity:      10,
		QueueCapacityBytes: 1024,
		Kafka: requestlog.KafkaConfig{
			Brokers:  []string{"kafka.invalid:9092"},
			Topic:    "events",
			ClientID: "borg-test",
			SASL:     requestlog.SASLConfig{Mechanism: requestlog.SASLNone},
		},
	}
}

func TestSinkProducesConfiguredTopicKeyAndValue(t *testing.T) {
	fake := &fakeProducer{}
	sink, err := newWithFactory(kafkaConfig(), func(options ...kgo.Opt) (producer, error) {
		if len(options) == 0 {
			t.Fatal("expected Kafka options")
		}
		return fake, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var callbackErr error
	sink.Produce(requestlog.Record{Key: []byte("key"), Value: []byte("value")}, func(err error) { callbackErr = err })
	if callbackErr != nil {
		t.Fatal(callbackErr)
	}
	if len(fake.records) != 1 || fake.records[0].Topic != "events" || string(fake.records[0].Key) != "key" || string(fake.records[0].Value) != "value" {
		t.Fatalf("unexpected Kafka record: %#v", fake.records)
	}
	sink.Close()
	if !fake.aborted || !fake.closed {
		t.Fatalf("sink did not abort and close: %#v", fake)
	}
}

func TestClientOptionsAcceptSupportedSASL(t *testing.T) {
	for _, mechanism := range []requestlog.SASLMechanism{
		requestlog.SASLNone,
		requestlog.SASLPlain,
		requestlog.SASLSCRAMSHA256,
		requestlog.SASLSCRAMSHA512,
	} {
		t.Run(string(mechanism), func(t *testing.T) {
			config := kafkaConfig()
			config.Kafka.SASL = requestlog.SASLConfig{Mechanism: mechanism, Username: "user", Password: "password"}
			if _, err := clientOptions(config); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestClientOptionsRejectsWrongSinkAndMechanism(t *testing.T) {
	config := kafkaConfig()
	config.Sink = requestlog.SinkNoop
	if _, err := clientOptions(config); err == nil {
		t.Fatal("expected wrong sink to fail")
	}
	config = kafkaConfig()
	config.Kafka.SASL.Mechanism = "oauth"
	if _, err := clientOptions(config); err == nil {
		t.Fatal("expected unsupported mechanism to fail")
	}
}

func TestProducerConstructionFailureIsReturned(t *testing.T) {
	want := errors.New("producer init failed")
	_, err := newWithFactory(kafkaConfig(), func(...kgo.Opt) (producer, error) {
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Fatalf("expected producer construction error, got %v", err)
	}
}

func TestTLSMaterialValidatedBeforeClientConstruction(t *testing.T) {
	config := kafkaConfig()
	config.Kafka.TLS = requestlog.TLSConfig{Enabled: true, CAFile: filepath.Join(t.TempDir(), "missing.pem")}
	called := false
	_, err := newWithFactory(config, func(...kgo.Opt) (producer, error) {
		called = true
		return nil, errors.New("unexpected")
	})
	if err == nil || called {
		t.Fatalf("expected local TLS failure before client construction, called=%v err=%v", called, err)
	}

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	config.Kafka.TLS.CAFile = caPath
	if _, err := clientOptions(config); err == nil {
		t.Fatal("expected malformed CA to fail")
	}
}
