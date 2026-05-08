package httpapi

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/undy-io/BORG/internal/auth"
	"github.com/undy-io/BORG/internal/proxy"
)

func TestRootAndListModels(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()

	root := httptest.NewRecorder()
	handler.ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", root.Code)
	}
	assertJSONDetail(t, root.Body.String(), "status", "ok")
	assertJSONDetail(t, root.Body.String(), "detail", "Proxy router is running")

	models := httptest.NewRecorder()
	handler.ServeHTTP(models, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if models.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", models.Code)
	}
	var payload struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(models.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Object != "list" {
		t.Fatalf("expected list, got %q", payload.Object)
	}
	var names []string
	for _, model := range payload.Data {
		names = append(names, model.ID)
	}
	if !reflect.DeepEqual(names, []string{"alpha", "openai/gpt-oss-20b"}) {
		t.Fatalf("unexpected models: %#v", names)
	}
}

func TestProxyBodyValidation(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()

	tests := []struct {
		name   string
		body   string
		detail string
	}{
		{name: "invalid", body: "{not-json", detail: "Body must be valid JSON"},
		{name: "array", body: "[]", detail: "Body must be valid JSON"},
		{name: "null", body: "null", detail: "Body must be valid JSON"},
		{name: "missing model", body: `{"messages":[]}`, detail: "Missing 'model' in request body"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tt.body))
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d", rec.Code)
			}
			assertJSONDetail(t, rec.Body.String(), "detail", tt.detail)
		})
	}
}

func TestUnknownModel(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing"}`))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Unknown model") {
		t.Fatalf("expected Unknown model detail, got %s", rec.Body.String())
	}
}

func TestAuthErrorsAndSuccess(t *testing.T) {
	handler, upstream, key := newTestHandler(t, true)
	defer upstream.Close()
	body := `{"model":"openai/gpt-oss-20b"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertJSONDetail(t, rec.Body.String(), "detail", "Missing API key")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Token nope")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertJSONDetail(t, rec.Body.String(), "detail", "Missing API key")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer malformed")
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertJSONDetail(t, rec.Body.String(), "detail", "Invalid API key")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mintHTTPToken(t, key, "WRONG:", "alice"))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertJSONDetail(t, rec.Body.String(), "detail", "Invalid API key")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+mintHTTPToken(t, key, "BORG:", "alice"))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestNonStreamingForwarding(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions?trace=1", strings.NewReader(`{"model":"openai/gpt-oss-20b","messages":[]}`))
	req.Header.Set("Authorization", "Bearer inbound")
	req.Header.Set("Content-Type", "application/json")

	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected upstream status 201, got %d body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["auth"] != "Bearer sk-test" {
		t.Fatalf("expected rewritten auth, got %#v", payload["auth"])
	}
	if payload["path"] != "/v1/chat/completions" {
		t.Fatalf("expected forwarded path, got %#v", payload["path"])
	}
	if payload["query"] != "trace=1" {
		t.Fatalf("expected forwarded query, got %#v", payload["query"])
	}
	if payload["content_type"] != "application/json" {
		t.Fatalf("expected content-type, got %#v", payload["content_type"])
	}
}

func TestStreamingForwarding(t *testing.T) {
	handler, upstream, _ := newTestHandler(t, false)
	defer upstream.Close()

	for _, tt := range []struct {
		name   string
		body   string
		accept string
	}{
		{name: "stream flag", body: `{"model":"openai/gpt-oss-20b","stream":true}`},
		{name: "accept header", body: `{"model":"openai/gpt-oss-20b"}`, accept: "text/event-stream; charset=utf-8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(tt.body))
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "[DONE]") {
				t.Fatalf("expected streamed DONE marker, got %s", rec.Body.String())
			}
			if got := rec.Header().Get("Content-Length"); got != "" {
				t.Fatalf("expected content-length to be stripped, got %q", got)
			}
		})
	}
}

func TestDownstreamCancellationCancelsUpstream(t *testing.T) {
	upstreamDone := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, "data: hello\n\n")
		flusher.Flush()
		<-r.Context().Done()
		close(upstreamDone)
	}))
	defer upstream.Close()

	proxyService := proxy.New()
	proxyService.AddInstance(upstream.URL, "sk-test", []string{"openai/gpt-oss-20b"})
	authenticator, err := auth.New("EMPTY", "")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(New(authenticator, proxyService))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"openai/gpt-oss-20b","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}

	reader := bufio.NewReader(resp.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	select {
	case <-upstreamDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream cancellation")
	}
}

func newTestHandler(t *testing.T, withAuth bool) (*Handler, *httptest.Server, []byte) {
	t.Helper()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(map[string]any{
			"upstream":     "ok",
			"auth":         r.Header.Get("Authorization"),
			"path":         r.URL.Path,
			"query":        r.URL.RawQuery,
			"content_type": r.Header.Get("Content-Type"),
		})
		if err != nil {
			t.Fatal(err)
		}

		if strings.Contains(r.Header.Get("Accept"), "text/event-stream") || strings.Contains(string(body), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = fmt.Fprint(w, "data: hello\n\n")
			flusher.Flush()
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			flusher.Flush()
			return
		}
		if strings.Contains(r.Header.Get("X-Force-Stream"), "true") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(raw)
	}))

	proxyService := proxy.New()
	proxyService.AddInstance(upstream.URL, "sk-test", []string{"openai/gpt-oss-20b", "alpha"})

	var authenticator *auth.Authenticator
	var err error
	if withAuth {
		key := []byte("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		authenticator, err = auth.New(base64.URLEncoding.EncodeToString(key), "BORG:")
		if err != nil {
			t.Fatal(err)
		}
		return New(authenticator, proxyService), upstream, key
	}
	authenticator, err = auth.New("EMPTY", "")
	if err != nil {
		t.Fatal(err)
	}
	return New(authenticator, proxyService), upstream, nil
}

func mintHTTPToken(t *testing.T, key []byte, prefix string, username string) string {
	t.Helper()

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, auth.NonceLen)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	ciphertext := aead.Seal(nil, nonce, []byte(prefix+username), nil)
	return base64.URLEncoding.EncodeToString(append(nonce, ciphertext...))
}

func assertJSONDetail(t *testing.T, body string, key string, want string) {
	t.Helper()
	var payload map[string]string
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload[key] != want {
		t.Fatalf("expected %s=%q, got %q in %s", key, want, payload[key], body)
	}
}
