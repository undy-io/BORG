package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const modelID = "gpt-3.5-turbo"

func main() {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", modelsHandler)
	mux.HandleFunc("/v1/chat/completions", chatCompletionsHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	addr := ":" + port
	log.Printf("dummy-openai listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func modelsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       modelID,
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "dummy",
			},
		},
	})
}

func chatCompletionsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		body = nil
	}

	if wantsStream(r, body) {
		streamResponse(w)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"upstream":     "dummy-openai",
		"path":         r.URL.Path,
		"auth":         r.Header.Get("Authorization"),
		"content_type": r.Header.Get("Content-Type"),
		"body":         body,
	})
}

func wantsStream(r *http.Request, body any) bool {
	if strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream") {
		return true
	}

	payload, ok := body.(map[string]any)
	if !ok {
		return false
	}
	stream, ok := payload["stream"].(bool)
	return ok && stream
}

func streamResponse(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	chunks := []string{
		"data: {\"id\":\"dummy\",\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n",
		"data: {\"id\":\"dummy\",\"choices\":[{\"delta\":{\"content\":\" from KinD\"}}]}\n\n",
		"data: [DONE]\n\n",
	}

	flusher, _ := w.(http.Flusher)
	for _, chunk := range chunks {
		if _, err := w.Write([]byte(chunk)); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		http.Error(w, "failed to encode response", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}
