package kafkasink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"

	"github.com/undy-io/BORG/internal/requestlog"
)

type producer interface {
	TryProduce(context.Context, *kgo.Record, func(*kgo.Record, error))
	Flush(context.Context) error
	UnsafeAbortBufferedRecords()
	Close()
}

type clientFactory func(...kgo.Opt) (producer, error)

type Sink struct {
	client producer
	topic  string
}

func New(config requestlog.Config) (*Sink, error) {
	return newWithFactory(config, func(options ...kgo.Opt) (producer, error) {
		return kgo.NewClient(options...)
	})
}

func newWithFactory(config requestlog.Config, factory clientFactory) (*Sink, error) {
	options, err := clientOptions(config)
	if err != nil {
		return nil, err
	}
	client, err := factory(options...)
	if err != nil {
		return nil, fmt.Errorf("create Kafka request logging client: %w", err)
	}
	return &Sink{client: client, topic: config.Kafka.Topic}, nil
}

func clientOptions(config requestlog.Config) ([]kgo.Opt, error) {
	if config.Sink != requestlog.SinkKafka {
		return nil, errors.New("kafka request logging sink requires sink=kafka")
	}
	options := []kgo.Opt{
		kgo.SeedBrokers(config.Kafka.Brokers...),
		kgo.DefaultProduceTopic(config.Kafka.Topic),
		kgo.ClientID(config.Kafka.ClientID),
		kgo.MaxBufferedRecords(config.QueueCapacity),
		kgo.MaxBufferedBytes(int(config.QueueCapacityBytes)),
		kgo.UnknownTopicRetries(-1),
	}

	if config.Kafka.TLS.Enabled {
		tlsConfig, err := loadTLSConfig(config.Kafka.TLS)
		if err != nil {
			return nil, err
		}
		options = append(options, kgo.DialTLSConfig(tlsConfig))
	}

	auth := config.Kafka.SASL
	switch auth.Mechanism {
	case requestlog.SASLNone:
	case requestlog.SASLPlain:
		options = append(options, kgo.SASL(plain.Auth{User: auth.Username, Pass: auth.Password}.AsMechanism()))
	case requestlog.SASLSCRAMSHA256:
		options = append(options, kgo.SASL(scram.Auth{User: auth.Username, Pass: auth.Password}.AsSha256Mechanism()))
	case requestlog.SASLSCRAMSHA512:
		options = append(options, kgo.SASL(scram.Auth{User: auth.Username, Pass: auth.Password}.AsSha512Mechanism()))
	default:
		return nil, fmt.Errorf("unsupported Kafka SASL mechanism %q", auth.Mechanism)
	}
	return options, nil
}

func loadTLSConfig(config requestlog.TLSConfig) (*tls.Config, error) {
	result := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: config.ServerName,
	}
	if config.CAFile != "" {
		certificate, err := os.ReadFile(config.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read Kafka TLS CA file: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(certificate) {
			return nil, errors.New("kafka TLS CA file does not contain a valid PEM certificate")
		}
		result.RootCAs = roots
	}
	if config.CertFile != "" {
		certificate, err := tls.LoadX509KeyPair(config.CertFile, config.KeyFile)
		if err != nil {
			return nil, fmt.Errorf("load Kafka TLS client certificate: %w", err)
		}
		result.Certificates = []tls.Certificate{certificate}
	}
	return result, nil
}

func (s *Sink) Produce(record requestlog.Record, callback func(error)) {
	kafkaRecord := &kgo.Record{
		Topic: s.topic,
		Key:   record.Key,
		Value: record.Value,
	}
	s.client.TryProduce(context.Background(), kafkaRecord, func(_ *kgo.Record, err error) {
		callback(err)
	})
}

func (s *Sink) Flush(ctx context.Context) error {
	return s.client.Flush(ctx)
}

func (s *Sink) Close() {
	s.client.UnsafeAbortBufferedRecords()
	s.client.Close()
}
