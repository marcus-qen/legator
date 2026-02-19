# Contributing to InfraAgent

Thank you for your interest in contributing! This document covers development setup, testing, and the PR process.

## Development Setup

### Prerequisites

- Go 1.24+
- Make
- kubebuilder v4.12+
- kubectl
- A Kubernetes cluster (kind recommended for development)

### Clone and Build

```bash
git clone https://github.com/marcus-qen/infraagent.git
cd infraagent
go build -o bin/manager ./cmd/
```

### Run Tests

```bash
# Unit tests
go test ./... -v

# With coverage
go test ./... -coverprofile=cover.out
go tool cover -html=cover.out
```

### Install CRDs (for local development)

```bash
make manifests  # Generate CRD YAML from Go types
make install    # Apply CRDs to current cluster
```

### Run the Controller Locally

```bash
make run  # Runs against your current kubeconfig
```

## Project Structure

```
api/v1alpha1/          # CRD type definitions
internal/
  assembler/           # Prompt assembly from skills + environment
  controller/          # Kubernetes reconcilers
  engine/              # Guardrail engine (safety-critical)
  lifecycle/           # Graceful shutdown
  mcp/                 # MCP client integration
  metrics/             # Prometheus metrics
  multicluster/        # Remote cluster client factory
  provider/            # LLM provider abstraction
  ratelimit/           # Rate limiting
  reporter/            # Notification channels + escalation
  resolver/            # Environment + model tier resolution
  retention/           # AgentRun TTL cleanup
  runner/              # Agent execution loop
  scheduler/           # Cron/interval/webhook scheduling
  security/            # Credential sanitization
  skill/               # Skill loader (git, configmap, bundled)
  telemetry/           # OpenTelemetry tracing
  tools/               # Tool implementations (kubectl, HTTP)
charts/infraagent/     # Helm chart
docs/                  # Documentation
examples/              # Example agents, environments, configs
test/                  # E2E tests
```

## Making Changes

### Safety-Critical Code

The following packages are safety-critical and require extra care:

- `internal/engine/` — Action classification, autonomy enforcement, data protection
- `internal/security/` — Credential sanitization

Changes to these packages must:
1. Include tests for every new code path
2. Include negative tests (verify that blocked operations stay blocked)
3. Not weaken existing protections

### Adding a New Tool

1. Implement the `tools.Tool` interface in `internal/tools/`
2. Register it in the tool registry
3. Add tests

### Adding a New CRD Field

1. Add the field to the Go types in `api/v1alpha1/`
2. Run `make manifests` to regenerate CRD YAML
3. Update the Helm chart CRDs
4. Update the CRD reference docs
5. Add tests

## Pull Request Process

1. **Fork** the repository
2. **Branch** from `main` (name: `feature/description` or `fix/description`)
3. **Write tests** — all changes must include tests
4. **Run the full suite**: `go test ./...`
5. **Lint**: `golangci-lint run`
6. **Open a PR** with:
   - Clear description of the change
   - Link to any relevant issue
   - Test evidence (test output or CI link)

### PR Review Criteria

- [ ] Tests pass
- [ ] No decrease in test coverage for safety-critical packages
- [ ] CRD changes are backward-compatible
- [ ] Documentation updated if user-facing
- [ ] No secrets or credentials in code (CI enforces this via GitHub push protection)

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Package documentation in the package comment
- Exported functions have doc comments
- Error messages are lowercase (Go convention)
- Use `context.Context` for cancellation throughout

## Reporting Issues

- Use [GitHub Issues](https://github.com/marcus-qen/infraagent/issues)
- Include: what you expected, what happened, reproduction steps
- For security issues, email the maintainers directly (do not file a public issue)

## License

By contributing, you agree that your contributions will be licensed under the Apache 2.0 License.
