package requestlog

import (
	"net/http"
	"testing"
	"time"
)

type benchmarkExporter struct{}

func (benchmarkExporter) TryExport(Record) bool { return true }

func BenchmarkLoggerNoop(b *testing.B) {
	logger := NewLogger(Config{Sink: SinkNoop}, nil)
	input := StartInput{Principal: "ANONYMOUS", Model: "model", Body: []byte(`{"model":"model"}`)}
	b.ReportAllocs()
	for b.Loop() {
		if logger.Start(input) != nil {
			b.Fatal("noop logger returned a recorder")
		}
	}
}

func BenchmarkLoggerUnmatched(b *testing.B) {
	matcher, _ := CompileMatcher([]FilterConfig{{Headers: map[string][]string{"Host": {"^matched$"}}}})
	logger := NewLogger(Config{Sink: SinkKafka, Matcher: matcher}, benchmarkExporter{})
	input := StartInput{Principal: "ANONYMOUS", Model: "model", Host: "unmatched", Body: []byte(`{"model":"model"}`)}
	b.ReportAllocs()
	for b.Loop() {
		if logger.Start(input) != nil {
			b.Fatal("unmatched request returned a recorder")
		}
	}
}

func BenchmarkLoggerMetadataOnly(b *testing.B) {
	matcher, _ := CompileMatcher([]FilterConfig{{}})
	logger := NewLogger(Config{Sink: SinkKafka, Matcher: matcher}, benchmarkExporter{})
	input := StartInput{Started: time.Now(), Principal: "ANONYMOUS", Model: "model", Method: http.MethodPost}
	b.ReportAllocs()
	for b.Loop() {
		recorder := logger.Start(input)
		recorder.Complete(CompletionInput{})
	}
}

func BenchmarkLoggerFullCapture(b *testing.B) {
	matcher, _ := CompileMatcher([]FilterConfig{{}})
	logger := NewLogger(Config{
		Sink:    SinkKafka,
		Matcher: matcher,
		Capture: CaptureConfig{
			RequestBody:          true,
			ResponseBody:         true,
			MaxRequestBodyBytes:  1024 * 1024,
			MaxResponseBodyBytes: 1024 * 1024,
		},
	}, benchmarkExporter{})
	payload := make([]byte, 32*1024)
	input := StartInput{Started: time.Now(), Principal: "ANONYMOUS", Model: "model", Method: http.MethodPost, Body: payload}
	b.ReportAllocs()
	for b.Loop() {
		recorder := logger.Start(input)
		recorder.RecordResponseWrite(payload)
		recorder.Complete(CompletionInput{})
	}
}
