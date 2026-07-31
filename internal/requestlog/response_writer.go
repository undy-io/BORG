package requestlog

import (
	"errors"
	"net/http"
	"sync"
)

type ResponseWriter struct {
	underlying http.ResponseWriter
	recorder   *Recorder

	mu         sync.Mutex
	status     int
	writeError bool
}

func NewResponseWriter(underlying http.ResponseWriter, recorder *Recorder) *ResponseWriter {
	return &ResponseWriter{underlying: underlying, recorder: recorder}
}

func (w *ResponseWriter) Header() http.Header {
	return w.underlying.Header()
}

func (w *ResponseWriter) WriteHeader(status int) {
	w.mu.Lock()
	if status >= 100 && status < 200 {
		w.mu.Unlock()
		w.underlying.WriteHeader(status)
		return
	}
	if w.status != 0 {
		w.mu.Unlock()
		return
	}
	w.status = status
	headers := w.underlying.Header().Clone()
	contentType := headers.Get("Content-Type")
	w.mu.Unlock()
	w.recorder.ResponseStarted(status, contentType, headers)
	w.underlying.WriteHeader(status)
}

func (w *ResponseWriter) Write(payload []byte) (int, error) {
	w.ensureStarted()
	n, err := w.underlying.Write(payload)
	if n > 0 {
		w.recorder.RecordResponseWrite(payload[:n])
	}
	if err != nil {
		w.mu.Lock()
		w.writeError = true
		w.mu.Unlock()
	}
	return n, err
}

func (w *ResponseWriter) Flush() {
	w.ensureStarted()
	if err := http.NewResponseController(w.underlying).Flush(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		w.mu.Lock()
		w.writeError = true
		w.mu.Unlock()
	}
}

func (w *ResponseWriter) Unwrap() http.ResponseWriter {
	return w.underlying
}

func (w *ResponseWriter) Snapshot() (status int, writeError bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.status, w.writeError
}

func (w *ResponseWriter) ensureStarted() {
	w.mu.Lock()
	started := w.status != 0
	w.mu.Unlock()
	if !started {
		w.WriteHeader(http.StatusOK)
	}
}
