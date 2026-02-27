package transportwriter

import "testing"

type testHTTPErrorContract struct {
	Status  int
	Code    string
	Message string
}

func buildTestHTTPErrorContract(status int, code, message string) *testHTTPErrorContract {
	return &testHTTPErrorContract{Status: status, Code: code, Message: message}
}

func TestConvertHTTPErrorContract_Parity(t *testing.T) {
	tests := []struct {
		name string
		err  *HTTPError
		want *testHTTPErrorContract
	}{
		{
			name: "nil error",
			err:  nil,
			want: nil,
		},
		{
			name: "field parity",
			err:  &HTTPError{Status: 502, Code: "bad_gateway", Message: "probe offline", SuppressWrite: true},
			want: &testHTTPErrorContract{Status: 502, Code: "bad_gateway", Message: "probe offline"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvertHTTPErrorContract(tt.err, buildTestHTTPErrorContract)
			if (got == nil) != (tt.want == nil) {
				t.Fatalf("nil parity mismatch: got=%+v want=%+v", got, tt.want)
			}
			if got == nil {
				return
			}
			if *got != *tt.want {
				t.Fatalf("contract parity mismatch: got=%+v want=%+v", got, tt.want)
			}
		})
	}
}

func TestAdaptHTTPErrorWriter_Parity(t *testing.T) {
	if got := AdaptHTTPErrorWriter[testHTTPErrorContract](nil, buildTestHTTPErrorContract); got != nil {
		t.Fatalf("expected nil adapter for nil writer, got type %T", got)
	}

	var captured *testHTTPErrorContract
	adapted := AdaptHTTPErrorWriter(func(contract *testHTTPErrorContract) {
		captured = contract
	}, buildTestHTTPErrorContract)
	if adapted == nil {
		t.Fatal("expected adapter callback")
	}

	adapted(nil)
	if captured != nil {
		t.Fatalf("expected nil capture on nil error, got %+v", captured)
	}

	adapted(&HTTPError{Status: 500, Code: "internal_error", Message: "boom"})
	if captured == nil {
		t.Fatal("expected converted contract")
	}
	if captured.Status != 500 || captured.Code != "internal_error" || captured.Message != "boom" {
		t.Fatalf("unexpected converted contract: %+v", captured)
	}
}
