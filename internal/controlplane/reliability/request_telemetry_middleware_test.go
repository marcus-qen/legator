package reliability

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var (
	errHijackSentinel = errors.New("hijack sentinel")
	errPushSentinel   = errors.New("push sentinel")
)

type streamingWriter struct {
	header         http.Header
	status         int
	body           bytes.Buffer
	flushed        bool
	hijackCalled   bool
	readFromCalled bool
	pushCalled     bool
}

func (w *streamingWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *streamingWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *streamingWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(p)
}

func (w *streamingWriter) Flush() {
	w.flushed = true
}

func (w *streamingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.hijackCalled = true
	return nil, nil, errHijackSentinel
}

func (w *streamingWriter) ReadFrom(src io.Reader) (int64, error) {
	w.readFromCalled = true
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return io.Copy(&w.body, src)
}

func (w *streamingWriter) Push(string, *http.PushOptions) error {
	w.pushCalled = true
	return errPushSentinel
}

type basicWriter struct {
	header http.Header
	status int
}

func (w *basicWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *basicWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
}

func (w *basicWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(p), nil
}

func TestRequestTelemetryMiddlewarePreservesFlusherForStreaming(t *testing.T) {
	telemetry := NewRequestTelemetry(10, time.Minute, time.Now().UTC().Add(-time.Minute))

	handler := telemetry.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("missing flusher"))
			return
		}
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
	}))

	writer := &streamingWriter{}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil)
	handler.ServeHTTP(writer, req)

	if writer.status != http.StatusOK {
		t.Fatalf("expected status 200, got %d", writer.status)
	}
	if !writer.flushed {
		t.Fatal("expected underlying flusher to be called")
	}

	stats := telemetry.Snapshot(time.Minute, time.Now().UTC())
	if stats.TotalRequests != 1 || stats.SuccessfulRequests != 1 || stats.ServerErrors != 0 {
		t.Fatalf("unexpected telemetry stats: %+v", stats)
	}
}

func TestRequestTelemetryMiddlewareTracksImplicitWriteStatus(t *testing.T) {
	telemetry := NewRequestTelemetry(10, time.Minute, time.Now().UTC().Add(-time.Minute))

	handler := telemetry.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/ok", nil)
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected recorder code 200, got %d", rr.Code)
	}

	stats := telemetry.Snapshot(time.Minute, time.Now().UTC())
	if stats.TotalRequests != 1 {
		t.Fatalf("expected 1 sampled request, got %d", stats.TotalRequests)
	}
	if stats.SuccessfulRequests != 1 {
		t.Fatalf("expected successful request count 1, got %d", stats.SuccessfulRequests)
	}
	if stats.ServerErrors != 0 {
		t.Fatalf("expected 0 server errors, got %d", stats.ServerErrors)
	}
}

func TestStatusRecorderDelegatesOptionalInterfaces(t *testing.T) {
	writer := &streamingWriter{}
	recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}

	if _, ok := interface{}(recorder).(http.Flusher); !ok {
		t.Fatal("expected recorder to expose http.Flusher")
	}
	if _, ok := interface{}(recorder).(http.Hijacker); !ok {
		t.Fatal("expected recorder to expose http.Hijacker")
	}
	if _, ok := interface{}(recorder).(io.ReaderFrom); !ok {
		t.Fatal("expected recorder to expose io.ReaderFrom")
	}
	if _, ok := interface{}(recorder).(http.Pusher); !ok {
		t.Fatal("expected recorder to expose http.Pusher")
	}

	recorder.Flush()
	if !writer.flushed {
		t.Fatal("expected flush delegation to underlying writer")
	}

	if _, _, err := recorder.Hijack(); !errors.Is(err, errHijackSentinel) {
		t.Fatalf("expected hijack sentinel error, got %v", err)
	}
	if !writer.hijackCalled {
		t.Fatal("expected hijack to be delegated")
	}

	n, err := recorder.ReadFrom(strings.NewReader("stream payload"))
	if err != nil {
		t.Fatalf("unexpected ReadFrom error: %v", err)
	}
	if n == 0 {
		t.Fatal("expected ReadFrom to copy bytes")
	}
	if !writer.readFromCalled {
		t.Fatal("expected ReadFrom delegation to underlying writer")
	}
	if recorder.status != http.StatusOK {
		t.Fatalf("expected implicit status 200, got %d", recorder.status)
	}

	if err := recorder.Push("/assets/app.js", nil); !errors.Is(err, errPushSentinel) {
		t.Fatalf("expected push sentinel error, got %v", err)
	}
	if !writer.pushCalled {
		t.Fatal("expected Push delegation to underlying writer")
	}
}

func TestStatusRecorderUnsupportedOptionalInterfaces(t *testing.T) {
	recorder := &statusRecorder{ResponseWriter: &basicWriter{}, status: http.StatusOK}

	recorder.Flush() // should be a safe no-op

	if _, _, err := recorder.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("expected ErrNotSupported from Hijack, got %v", err)
	}
	if err := recorder.Push("/assets/app.js", nil); !errors.Is(err, http.ErrNotSupported) {
		t.Fatalf("expected ErrNotSupported from Push, got %v", err)
	}

	unwrapped := recorder.Unwrap()
	if unwrapped == nil {
		t.Fatal("expected non-nil unwrapped writer")
	}
}
