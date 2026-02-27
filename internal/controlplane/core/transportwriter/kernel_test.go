package transportwriter

import (
	"errors"
	"reflect"
	"testing"
)

func TestWriteForSurface_HTTP(t *testing.T) {
	envelope := &ResponseEnvelope{HTTPSuccess: map[string]string{"status": "ok"}}
	var got any

	handled := WriteForSurface(SurfaceHTTP, envelope, WriterKernel{
		WriteHTTPSuccess: func(v any) {
			got = v
		},
	})

	if handled {
		t.Fatal("expected handled=false for HTTP success")
	}
	if !reflect.DeepEqual(got, envelope.HTTPSuccess) {
		t.Fatalf("unexpected HTTP success payload: got %#v want %#v", got, envelope.HTTPSuccess)
	}
}

func TestWriteForSurface_HTTPErrorSuppressed(t *testing.T) {
	envelope := &ResponseEnvelope{HTTPError: &HTTPError{SuppressWrite: true}}
	called := false

	handled := WriteForSurface(SurfaceHTTP, envelope, WriterKernel{
		WriteHTTPError: func(*HTTPError) {
			called = true
		},
	})

	if !handled {
		t.Fatal("expected handled=true for suppressed HTTP error")
	}
	if called {
		t.Fatal("expected suppressed HTTP error to skip writer callback")
	}
}

func TestWriteForSurface_MCPError(t *testing.T) {
	want := errors.New("boom")
	envelope := &ResponseEnvelope{MCPError: want}
	var got error

	handled := WriteForSurface(SurfaceMCP, envelope, WriterKernel{
		WriteMCPError: func(err error) {
			got = err
		},
	})

	if !handled {
		t.Fatal("expected handled=true for MCP error")
	}
	if !errors.Is(got, want) {
		t.Fatalf("unexpected MCP error callback value: got %v want %v", got, want)
	}
}
