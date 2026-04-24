package proxy

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestRegistryRoundRobinAndModels(t *testing.T) {
	service := New()
	service.AddInstance("http://e1:8000", "k1", []string{"zulu", "alpha"})
	service.AddInstance("http://e2:8000", "k2", []string{"alpha"})
	service.RemoveInstance("http://e1:8000", []string{"zulu"})

	listing := service.ListModels()
	if listing.Object != "list" {
		t.Fatalf("expected list object, got %q", listing.Object)
	}
	var names []string
	for _, model := range listing.Data {
		names = append(names, model.ID)
	}
	if !reflect.DeepEqual(names, []string{"alpha"}) {
		t.Fatalf("unexpected models: %#v", names)
	}

	first, ok := service.PickEndpoint("alpha")
	if !ok {
		t.Fatal("expected first endpoint")
	}
	second, ok := service.PickEndpoint("alpha")
	if !ok {
		t.Fatal("expected second endpoint")
	}
	if first.URL == second.URL {
		t.Fatalf("expected round-robin to rotate, got %q then %q", first.URL, second.URL)
	}
}

func TestPickUnknownModel(t *testing.T) {
	service := New()
	if _, ok := service.PickEndpoint("missing"); ok {
		t.Fatal("expected missing model")
	}
}

func TestRegularForwardUsesTransportManagedCompression(t *testing.T) {
	acceptEncoding := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding <- r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(gzipPayload(t, `{"ok":true}`))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Accept-Encoding", "gzip, br")

	if err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false); err != nil {
		t.Fatal(err)
	}
	if got := <-acceptEncoding; got != "gzip" {
		t.Fatalf("expected transport-managed gzip upstream, got %q", got)
	}
	if got := rec.Body.String(); got != `{"ok":true}` {
		t.Fatalf("expected decoded body, got %q", got)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("expected content-encoding to be stripped, got %q", got)
	}
}

func TestStreamingForwardForcesIdentityEncoding(t *testing.T) {
	acceptEncoding := make(chan string, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		acceptEncoding <- r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Accept-Encoding", "gzip")

	if err := service.Forward(rec, req, []byte(`{"model":"m","stream":true}`), "m", true); err != nil {
		t.Fatal(err)
	}
	if got := <-acceptEncoding; got != "identity" {
		t.Fatalf("expected streaming identity encoding upstream, got %q", got)
	}
}

func TestRequestHopByHopHeadersAreStripped(t *testing.T) {
	src := http.Header{}
	src.Set("Accept-Encoding", "gzip")
	src.Set("Connection", "X-Foo, X-Bar")
	src.Set("Trailer", "X-Trailer")
	src.Set("Trailers", "X-Trailers")
	src.Set("TE", "trailers")
	src.Set("X-Bar", "remove-me-too")
	src.Set("X-Foo", "remove-me")
	src.Set("X-Keep", "ok")

	dst := http.Header{}
	copyRequestHeaders(dst, src)

	for _, key := range []string{"Accept-Encoding", "Connection", "Trailer", "Trailers", "TE", "X-Bar", "X-Foo"} {
		if got := dst.Get(key); got != "" {
			t.Fatalf("expected %s to be stripped, got %q", key, got)
		}
	}
	if got := dst.Get("X-Keep"); got != "ok" {
		t.Fatalf("expected X-Keep to be preserved, got %q", got)
	}
}

func TestResponseHopByHopHeadersAreStripped(t *testing.T) {
	src := http.Header{}
	src.Set("Connection", "X-Bar")
	src.Set("Content-Encoding", "gzip")
	src.Set("Content-Length", "12")
	src.Set("Trailer", "X-Trailer")
	src.Set("Transfer-Encoding", "chunked")
	src.Set("X-Bar", "remove-me")
	src.Set("X-Keep", "ok")

	regular := http.Header{}
	copyResponseHeaders(regular, src, regularExcludedResponseHeaders)
	for _, key := range []string{"Connection", "Content-Encoding", "Trailer", "Transfer-Encoding", "X-Bar"} {
		if got := regular.Get(key); got != "" {
			t.Fatalf("expected regular response %s to be stripped, got %q", key, got)
		}
	}
	if got := regular.Get("Content-Length"); got != "12" {
		t.Fatalf("expected regular response content-length to be preserved, got %q", got)
	}
	if got := regular.Get("X-Keep"); got != "ok" {
		t.Fatalf("expected regular response X-Keep to be preserved, got %q", got)
	}

	streaming := http.Header{}
	copyResponseHeaders(streaming, src, streamingExcludedResponseHeaders)
	if got := streaming.Get("Content-Length"); got != "" {
		t.Fatalf("expected streaming response content-length to be stripped, got %q", got)
	}
}

func TestRegularForwardReturnsReadErrorBeforeWritingResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("partial"))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := service.Forward(rec, req, []byte(`{"model":"m"}`), "m", false)
	if err == nil {
		t.Fatal("expected read error from truncated upstream response")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected no response body before upstream read succeeds, got %q", rec.Body.String())
	}
}

func TestStreamingForwardTreatsReadErrorAsStreamEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: partial\n\n"))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := service.Forward(rec, req, []byte(`{"model":"m","stream":true}`), "m", true)
	if err != nil {
		t.Fatalf("expected streaming read error to end quietly, got %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected upstream status, got %d", rec.Code)
	}
	if rec.Body.String() != "data: partial\n\n" {
		t.Fatalf("expected partial stream body, got %q", rec.Body.String())
	}
}

func TestStreamingForwardTreatsWriteErrorAsStreamEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: hello\n\n"))
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	err := service.Forward(&failingResponseWriter{header: http.Header{}}, req, []byte(`{"model":"m","stream":true}`), "m", true)
	if err != nil {
		t.Fatalf("expected streaming write error to end quietly, got %v", err)
	}
}

func BenchmarkStreamingForward(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for i := 0; i < 100; i++ {
			_, _ = fmt.Fprintf(w, "data: chunk-%d\n\n", i)
			flusher.Flush()
		}
	}))
	defer upstream.Close()

	service := New()
	service.AddInstance(upstream.URL, "sk-test", []string{"m"})
	body := []byte(`{"model":"m","stream":true}`)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		rec := httptest.NewRecorder()
		if err := service.Forward(rec, req, body, "m", true); err != nil {
			b.Fatal(err)
		}
		if rec.Code != http.StatusOK {
			b.Fatalf("unexpected status %d", rec.Code)
		}
	}
}

type failingResponseWriter struct {
	header http.Header
}

func (w *failingResponseWriter) Header() http.Header {
	return w.header
}

func (w *failingResponseWriter) WriteHeader(statusCode int) {}

func (w *failingResponseWriter) Write(data []byte) (int, error) {
	return 0, net.ErrClosed
}

func gzipPayload(t *testing.T, value string) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := gzip.NewWriter(&buf)
	if _, err := writer.Write([]byte(value)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
