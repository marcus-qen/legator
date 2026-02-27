package projectiondispatch

import (
	"strconv"
	"testing"
)

func TestPolicyRegistryResolve(t *testing.T) {
	registry := NewPolicyRegistry(map[string]int{
		"http": 1,
		"mcp":  2,
	})

	tests := []struct {
		surface string
		want    int
		ok      bool
	}{
		{surface: "http", want: 1, ok: true},
		{surface: "mcp", want: 2, ok: true},
		{surface: "bogus", want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.surface+"/"+strconv.FormatBool(tt.ok), func(t *testing.T) {
			got, ok := registry.Resolve(tt.surface)
			if ok != tt.ok {
				t.Fatalf("unexpected resolve presence: got %v want %v", ok, tt.ok)
			}
			if tt.ok && got != tt.want {
				t.Fatalf("unexpected policy value: got %d want %d", got, tt.want)
			}
		})
	}
}

func TestDispatchForSurface(t *testing.T) {
	type writer struct {
		events []string
	}

	registry := NewPolicyRegistry(map[string]Policy[int, *writer]{
		"http": PolicyFunc[int, *writer](func(projection int, w *writer) {
			w.events = append(w.events, "http:"+strconv.Itoa(projection))
		}),
		"mcp": PolicyFunc[int, *writer](func(projection int, w *writer) {
			w.events = append(w.events, "mcp:"+strconv.Itoa(projection))
		}),
	})

	w := &writer{}
	DispatchForSurface[string](registry, "http", 7, w, func(surface string, w *writer) {
		w.events = append(w.events, "unsupported:"+surface)
	})
	if len(w.events) != 1 || w.events[0] != "http:7" {
		t.Fatalf("unexpected dispatch events for supported surface: %+v", w.events)
	}

	DispatchForSurface[string](registry, "bogus", 9, w, func(surface string, w *writer) {
		w.events = append(w.events, "unsupported:"+surface)
	})
	if len(w.events) != 2 || w.events[1] != "unsupported:bogus" {
		t.Fatalf("unexpected dispatch events for unsupported surface: %+v", w.events)
	}
}
