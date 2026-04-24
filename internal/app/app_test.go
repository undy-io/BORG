package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestNewWiresHandlerFromConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`
borg:
  auth_key: "EMPTY"
  instances:
    - endpoint: "http://upstream"
      apikey: "sk-test"
      models: ["m"]
`), 0o600); err != nil {
		t.Fatal(err)
	}

	borgApp, err := New(path)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	borgApp.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected root 200, got %d", rec.Code)
	}
}
