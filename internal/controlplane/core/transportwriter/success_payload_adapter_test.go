package transportwriter

import "testing"

type testSuccessPayload struct {
	Value string
}

func normalizeTestSuccessPayload(payload *testSuccessPayload) *testSuccessPayload {
	if payload == nil {
		return &testSuccessPayload{}
	}
	return payload
}

func TestConvertSuccessPayload_Parity(t *testing.T) {
	typed := &testSuccessPayload{Value: "ok"}
	var typedNil *testSuccessPayload

	tests := []struct {
		name      string
		payload   any
		normalize func(*testSuccessPayload) *testSuccessPayload
		want      *testSuccessPayload
	}{
		{name: "typed payload", payload: typed, normalize: normalizeTestSuccessPayload, want: typed},
		{name: "typed nil payload", payload: typedNil, normalize: normalizeTestSuccessPayload, want: &testSuccessPayload{}},
		{name: "untyped nil payload", payload: nil, normalize: normalizeTestSuccessPayload, want: &testSuccessPayload{}},
		{name: "wrong payload type", payload: "wrong", normalize: normalizeTestSuccessPayload, want: &testSuccessPayload{}},
		{name: "no normalization", payload: nil, normalize: nil, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvertSuccessPayload[*testSuccessPayload](tt.payload, tt.normalize)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("nil parity mismatch: got=%+v want=%+v", got, tt.want)
			}
			if got == nil {
				return
			}
			if *got != *tt.want {
				t.Fatalf("payload parity mismatch: got=%+v want=%+v", got, tt.want)
			}
		})
	}
}

func TestAdaptSuccessPayloadWriter_Parity(t *testing.T) {
	if got := AdaptSuccessPayloadWriter[*testSuccessPayload](nil, normalizeTestSuccessPayload); got != nil {
		t.Fatalf("expected nil adapter for nil callback, got type %T", got)
	}

	var captured *testSuccessPayload
	adapted := AdaptSuccessPayloadWriter(func(payload *testSuccessPayload) {
		captured = payload
	}, normalizeTestSuccessPayload)
	if adapted == nil {
		t.Fatal("expected success adapter callback")
	}

	adapted(nil)
	if captured == nil || *captured != (testSuccessPayload{}) {
		t.Fatalf("unexpected normalized nil payload: %+v", captured)
	}

	adapted(&testSuccessPayload{Value: "ok"})
	if captured == nil || captured.Value != "ok" {
		t.Fatalf("unexpected adapted payload: %+v", captured)
	}
}
