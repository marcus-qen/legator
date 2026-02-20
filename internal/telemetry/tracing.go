/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package telemetry configures OpenTelemetry tracing for the InfraAgent operator.
//
// Spans follow the OTel GenAI semantic conventions where applicable:
//   - gen_ai.system — the LLM provider
//   - gen_ai.request.model — the model name
//   - gen_ai.usage.input_tokens — tokens consumed
//   - gen_ai.usage.output_tokens — tokens generated
//
// Custom span attributes use the `infraagent.` prefix.
package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.24.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName = "infraagent.io/controller"
)

// Tracer returns the package-level tracer.
func Tracer() trace.Tracer {
	return otel.Tracer(tracerName)
}

// InitTraceProvider initialises the OTel trace provider with an OTLP gRPC exporter.
// If endpoint is empty, tracing is disabled (noop provider is used).
// Returns a shutdown function that must be called on application exit.
func InitTraceProvider(ctx context.Context, endpoint string, version string) (func(context.Context) error, error) {
	if endpoint == "" {
		// No-op: tracing disabled
		return func(context.Context) error { return nil }, nil
	}

	exporter, err := otlptracegrpc.New(ctx,
		otlptracegrpc.WithEndpoint(endpoint),
		otlptracegrpc.WithInsecure(), // TLS configurable via env (OTEL_EXPORTER_OTLP_INSECURE)
	)
	if err != nil {
		return nil, fmt.Errorf("create OTLP exporter: %w", err)
	}

	res, err := resource.New(ctx,
		resource.WithHost(),
		resource.WithAttributes(
			semconv.ServiceNameKey.String("infraagent-controller"),
			semconv.ServiceVersionKey.String(version),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)

	otel.SetTracerProvider(tp)

	return tp.Shutdown, nil
}

// --- Span helpers ---

// StartRunSpan creates the parent span for an agent run.
func StartRunSpan(ctx context.Context, agent, trigger string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "agent.run",
		trace.WithAttributes(
			attribute.String("infraagent.agent", agent),
			attribute.String("infraagent.trigger", trigger),
		),
		trace.WithSpanKind(trace.SpanKindInternal),
	)
}

// StartAssemblySpan creates a child span for prompt assembly.
func StartAssemblySpan(ctx context.Context, agent string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "agent.assemble",
		trace.WithAttributes(
			attribute.String("infraagent.agent", agent),
		),
	)
}

// StartLLMCallSpan creates a child span for an LLM call, following GenAI conventions.
func StartLLMCallSpan(ctx context.Context, model, provider string, iteration int) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "gen_ai.chat",
		trace.WithAttributes(
			attribute.String("gen_ai.system", provider),
			attribute.String("gen_ai.request.model", model),
			attribute.Int("infraagent.iteration", iteration),
		),
		trace.WithSpanKind(trace.SpanKindClient),
	)
}

// EndLLMCallSpan enriches the LLM span with usage data.
func EndLLMCallSpan(span trace.Span, inputTokens, outputTokens int64, hasToolCalls bool) {
	span.SetAttributes(
		attribute.Int64("gen_ai.usage.input_tokens", inputTokens),
		attribute.Int64("gen_ai.usage.output_tokens", outputTokens),
		attribute.Bool("infraagent.has_tool_calls", hasToolCalls),
	)
	span.End()
}

// StartToolCallSpan creates a child span for a tool execution.
func StartToolCallSpan(ctx context.Context, tool, target, tier string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "agent.tool_call",
		trace.WithAttributes(
			attribute.String("infraagent.tool", tool),
			attribute.String("infraagent.target", target),
			attribute.String("infraagent.action_tier", tier),
		),
	)
}

// EndToolCallSpan enriches the tool span with result data.
func EndToolCallSpan(span trace.Span, status string, blocked bool, blockReason string) {
	span.SetAttributes(
		attribute.String("infraagent.action_status", status),
		attribute.Bool("infraagent.blocked", blocked),
	)
	if blocked {
		span.SetAttributes(attribute.String("infraagent.block_reason", blockReason))
	}
	span.End()
}

// StartReportSpan creates a child span for report delivery.
func StartReportSpan(ctx context.Context, agent, channel string) (context.Context, trace.Span) {
	return Tracer().Start(ctx, "agent.report",
		trace.WithAttributes(
			attribute.String("infraagent.agent", agent),
			attribute.String("infraagent.report_channel", channel),
		),
	)
}
