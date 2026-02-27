package server

import (
	"testing"
	"time"
)

func TestParseHumanDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		err  bool
	}{
		{in: "30d", want: 30 * 24 * time.Hour},
		{in: "12h", want: 12 * time.Hour},
		{in: "0s", want: 0},
		{in: "-1h", err: true},
		{in: "abc", err: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseHumanDuration(tt.in)
			if tt.err {
				if err == nil {
					t.Fatalf("expected error for %q", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseHumanDuration(%q): %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("parseHumanDuration(%q)=%s, want %s", tt.in, got, tt.want)
			}
		})
	}
}
