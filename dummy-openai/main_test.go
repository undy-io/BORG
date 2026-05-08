package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestModelsHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()

	modelsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"id":"gpt-3.5-turbo"`) {
		t.Fatalf("expected model id in response, got %s", rec.Body.String())
	}
}

func TestChatCompletionsHandler(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-3.5-turbo","messages":[]}`),
	)
	req.Header.Set("Authorization", "Bearer EMPTY")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	chatCompletionsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response should be JSON: %v", err)
	}
	if response["upstream"] != "dummy-openai" {
		t.Fatalf("expected upstream dummy-openai, got %#v", response["upstream"])
	}
	if response["path"] != "/v1/chat/completions" {
		t.Fatalf("expected forwarded path, got %#v", response["path"])
	}
	if response["auth"] != "Bearer EMPTY" {
		t.Fatalf("expected auth header echo, got %#v", response["auth"])
	}
	if response["content_type"] != "application/json" {
		t.Fatalf("expected content type echo, got %#v", response["content_type"])
	}
}

func TestChatCompletionsHandlerStreams(t *testing.T) {
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-3.5-turbo","stream":true,"messages":[]}`),
	)
	rec := httptest.NewRecorder()

	chatCompletionsHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data: {"id":"dummy"`) {
		t.Fatalf("expected dummy SSE chunk, got %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Fatalf("expected DONE sentinel, got %s", body)
	}
}
