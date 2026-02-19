/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// setupTestTracer installs an in-memory span exporter for test assertions.
func setupTestTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := trace.NewTracerProvider(
		trace.WithSyncer(exporter),
	)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})
	return exporter
}

func TestInitTraceProviderNoopWhenEmpty(t *testing.T) {
	shutdown, err := InitTraceProvider(context.Background(), "", "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should be a no-op shutdown
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown error: %v", err)
	}
}

func TestStartRunSpan(t *testing.T) {
	exporter := setupTestTracer(t)

	ctx := context.Background()
	ctx, span := StartRunSpan(ctx, "watchman-light", "scheduled")
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Name != "agent.run" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "agent.run")
	}

	// Check attributes
	attrs := spans[0].Attributes
	foundAgent := false
	foundTrigger := false
	for _, a := range attrs {
		if string(a.Key) == "infraagent.agent" && a.Value.AsString() == "watchman-light" {
			foundAgent = true
		}
		if string(a.Key) == "infraagent.trigger" && a.Value.AsString() == "scheduled" {
			foundTrigger = true
		}
	}
	if !foundAgent {
		t.Error("missing infraagent.agent attribute")
	}
	if !foundTrigger {
		t.Error("missing infraagent.trigger attribute")
	}
}

func TestStartLLMCallSpan(t *testing.T) {
	exporter := setupTestTracer(t)

	ctx := context.Background()
	_, llmSpan := StartLLMCallSpan(ctx, "claude-sonnet-4-5", "anthropic", 1)
	EndLLMCallSpan(llmSpan, 1000, 500, true)

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Name != "gen_ai.chat" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "gen_ai.chat")
	}

	// Verify GenAI attributes
	attrs := spans[0].Attributes
	foundModel := false
	foundSystem := false
	foundInputTokens := false
	for _, a := range attrs {
		if string(a.Key) == "gen_ai.request.model" && a.Value.AsString() == "claude-sonnet-4-5" {
			foundModel = true
		}
		if string(a.Key) == "gen_ai.system" && a.Value.AsString() == "anthropic" {
			foundSystem = true
		}
		if string(a.Key) == "gen_ai.usage.input_tokens" && a.Value.AsInt64() == 1000 {
			foundInputTokens = true
		}
	}
	if !foundModel {
		t.Error("missing gen_ai.request.model")
	}
	if !foundSystem {
		t.Error("missing gen_ai.system")
	}
	if !foundInputTokens {
		t.Error("missing gen_ai.usage.input_tokens")
	}
}

func TestStartToolCallSpan(t *testing.T) {
	exporter := setupTestTracer(t)

	ctx := context.Background()
	_, toolSpan := StartToolCallSpan(ctx, "kubectl.get", "pods -n backstage", "read")
	EndToolCallSpan(toolSpan, "executed", false, "")

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Name != "agent.tool_call" {
		t.Errorf("span name = %q, want %q", spans[0].Name, "agent.tool_call")
	}
}

func TestToolCallSpanBlocked(t *testing.T) {
	exporter := setupTestTracer(t)

	ctx := context.Background()
	_, toolSpan := StartToolCallSpan(ctx, "kubectl.delete", "pvc -n data", "data-mutation")
	EndToolCallSpan(toolSpan, "blocked", true, "data protection")

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}

	attrs := spans[0].Attributes
	foundBlocked := false
	foundReason := false
	for _, a := range attrs {
		if string(a.Key) == "infraagent.blocked" && a.Value.AsBool() {
			foundBlocked = true
		}
		if string(a.Key) == "infraagent.block_reason" && a.Value.AsString() == "data protection" {
			foundReason = true
		}
	}
	if !foundBlocked {
		t.Error("missing infraagent.blocked attribute")
	}
	if !foundReason {
		t.Error("missing infraagent.block_reason attribute")
	}
}

func TestNestedSpans(t *testing.T) {
	exporter := setupTestTracer(t)

	ctx := context.Background()
	ctx, runSpan := StartRunSpan(ctx, "test-agent", "manual")
	_, asmSpan := StartAssemblySpan(ctx, "test-agent")
	asmSpan.End()
	runSpan.End()

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2", len(spans))
	}

	// Assembly span should be a child of run span
	asmStub := spans[0] // Assembly ends first
	runStub := spans[1]

	if asmStub.Parent.TraceID() != runStub.SpanContext.TraceID() {
		t.Error("assembly span should share trace ID with run span")
	}
	if !asmStub.Parent.SpanID().IsValid() {
		t.Error("assembly span should have a valid parent span ID")
	}
}
