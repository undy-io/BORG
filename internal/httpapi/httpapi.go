package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/undy-io/BORG/internal/auth"
	"github.com/undy-io/BORG/internal/proxy"
)

type Handler struct {
	auth  *auth.Authenticator
	proxy *proxy.Service
}

func New(authenticator *auth.Authenticator, proxyService *proxy.Service) *Handler {
	return &Handler{auth: authenticator, proxy: proxyService}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		writeJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
			"detail": "Proxy router is running",
		})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		writeJSON(w, http.StatusOK, h.proxy.ListModels())
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/"):
		h.handleProxy(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleProxy(w http.ResponseWriter, r *http.Request) {
	if _, err := h.auth.Require(r); err != nil {
		writeError(w, err)
		return
	}

	rawBody, err := io.ReadAll(r.Body)
	if err != nil {
		writeDetail(w, http.StatusBadRequest, "Body must be valid JSON")
		return
	}

	model, stream, err := parseProxyBody(rawBody, r.Header.Get("Accept"))
	if err != nil {
		writeError(w, err)
		return
	}

	if err := h.proxy.Forward(w, r, rawBody, model, stream); err != nil {
		writeError(w, err)
	}
}

func parseProxyBody(rawBody []byte, accept string) (string, bool, error) {
	var parsed any
	if err := json.Unmarshal(rawBody, &parsed); err != nil {
		return "", false, &proxy.HTTPError{StatusCode: http.StatusBadRequest, Detail: "Body must be valid JSON"}
	}

	body, ok := parsed.(map[string]any)
	if !ok {
		return "", false, &proxy.HTTPError{StatusCode: http.StatusBadRequest, Detail: "Body must be valid JSON"}
	}

	model, ok := body["model"].(string)
	if !ok || model == "" {
		return "", false, &proxy.HTTPError{StatusCode: http.StatusBadRequest, Detail: "Missing 'model' in request body"}
	}

	stream := truthy(body["stream"]) || strings.Contains(accept, "text/event-stream")
	return model, stream, nil
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	default:
		return true
	}
}

func writeError(w http.ResponseWriter, err error) {
	var authErr *auth.HTTPError
	if errors.As(err, &authErr) {
		writeDetail(w, authErr.StatusCode, authErr.Detail)
		return
	}

	var proxyErr *proxy.HTTPError
	if errors.As(err, &proxyErr) {
		writeDetail(w, proxyErr.StatusCode, proxyErr.Detail)
		return
	}

	writeDetail(w, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
}

func writeDetail(w http.ResponseWriter, statusCode int, detail string) {
	writeJSON(w, statusCode, map[string]string{"detail": detail})
}

func writeJSON(w http.ResponseWriter, statusCode int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(value)
}
