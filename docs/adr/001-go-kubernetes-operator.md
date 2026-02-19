# ADR-001: Go + Kubernetes Operator

**Status:** Accepted
**Date:** 2026-02-19

## Context

We needed a runtime for autonomous infrastructure agents. Options considered:
1. Python-based standalone service
2. Go CLI with cron
3. Go Kubernetes operator (kubebuilder)

## Decision

Go + kubebuilder Kubernetes operator.

## Rationale

- **Native K8s integration**: CRDs for agent definitions, controller-runtime for lifecycle management, leader election for HA
- **Single binary**: `helm install infraagent` — one chart, one deployment
- **Performance**: Go's concurrency model suits scheduling many agents
- **Ecosystem**: kubebuilder/controller-runtime is the standard for K8s operators
- **Type safety**: Go structs for CRDs catch schema errors at compile time

## Consequences

- Learning curve for Go/kubebuilder (mitigated by extensive documentation)
- Less flexibility than Python for rapid prototyping
- Must maintain CRD schemas carefully (backward compatibility)
