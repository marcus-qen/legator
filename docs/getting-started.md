# Getting Started

Get an autonomous infrastructure agent running on your cluster in 10 minutes.

## Prerequisites

- Kubernetes 1.32+
- Helm 3.x
- An LLM API key (Anthropic or OpenAI)

## 1. Install the Operator

```bash
helm install infraagent oci://ghcr.io/marcus-qen/infraagent/charts/infraagent \
  -n infraagent-system --create-namespace \
  --set leaderElection.enabled=true
```

Or from a local checkout:

```bash
git clone https://github.com/marcus-qen/infraagent.git
cd infraagent
helm install infraagent charts/infraagent/ \
  -n infraagent-system --create-namespace
```

Verify the controller is running:

```bash
kubectl get pods -n infraagent-system
# NAME                                          READY   STATUS    AGE
# infraagent-controller-xxxxx-yyyyy             1/1     Running   30s
```

## 2. Configure Model Access

Create a Secret with your LLM API key:

```bash
kubectl create namespace agents
kubectl create secret generic llm-api-key \
  -n agents \
  --from-literal=api-key=sk-your-key-here
```

Apply a ModelTierConfig that maps tier names to providers:

```yaml
# model-tier-config.yaml
apiVersion: core.infraagent.io/v1alpha1
kind: ModelTierConfig
metadata:
  name: default
spec:
  defaultAuth:
    type: apiKey
    secretRef: llm-api-key
    secretKey: api-key
  tiers:
    - tier: fast
      provider: anthropic
      model: claude-haiku-3-5-20241022
      maxTokens: 4096
      costPerMillionInput: "0.80"
      costPerMillionOutput: "4.00"
    - tier: standard
      provider: anthropic
      model: claude-sonnet-4-20250514
      maxTokens: 8192
      costPerMillionInput: "3.00"
      costPerMillionOutput: "15.00"
```

```bash
kubectl apply -f model-tier-config.yaml
```

## 3. Create an Environment

The AgentEnvironment tells agents about your cluster — what endpoints exist, where data lives, and how to report findings:

```yaml
# environment.yaml
apiVersion: core.infraagent.io/v1alpha1
kind: AgentEnvironment
metadata:
  name: my-cluster
  namespace: agents
spec:
  connection:
    kind: in-cluster
  endpoints:
    grafana:
      url: https://grafana.example.com
      healthPath: /api/health
  namespaces:
    monitoring:
      - monitoring
    apps:
      - default
    system:
      - kube-system
  channels:
    alerts:
      type: webhook
      target: https://hooks.slack.com/services/XXX/YYY/ZZZ
```

```bash
kubectl apply -f environment.yaml -n agents
```

## 4. Deploy Your First Agent

Here's a simple monitoring agent that checks endpoint health every 5 minutes:

```yaml
# watchman.yaml
apiVersion: core.infraagent.io/v1alpha1
kind: InfraAgent
metadata:
  name: watchman
  namespace: agents
spec:
  description: "Monitor endpoints and alert on failures"
  emoji: "👁️"
  schedule:
    cron: "*/5 * * * *"
  model:
    tier: fast
    tokenBudget: 8000
    timeout: "60s"
  skills:
    - name: endpoint-monitoring
      source: bundled
  guardrails:
    autonomy: observe
    maxIterations: 5
  reporting:
    onSuccess: silent
    onFailure: escalate
    onFinding: notify
  environmentRef: my-cluster
```

```bash
kubectl apply -f watchman.yaml -n agents
```

## 5. Verify It Works

Check the agent status:

```bash
kubectl get infraagents -n agents
# NAME       PHASE   AUTONOMY   SCHEDULE      LAST RUN   RUNS   AGE
# watchman   Ready   observe    */5 * * * *                0     10s
```

Trigger a manual run:

```bash
kubectl annotate infraagent watchman \
  infraagent.io/run-now=true -n agents
```

Watch the AgentRun:

```bash
kubectl get agentruns -n agents -w
# NAME              AGENT     PHASE       TRIGGER   AGE
# watchman-abc123   watchman  Succeeded   manual    15s
```

Inspect the audit trail:

```bash
kubectl describe agentrun watchman-abc123 -n agents
```

## What Just Happened?

1. The controller assembled a prompt from the agent's skill, environment, and guardrails
2. It called the LLM (Haiku, configured as "fast" tier)
3. The LLM requested tool calls (HTTP health checks, kubectl queries)
4. Each tool call was evaluated by the guardrail engine before execution
5. Results were recorded in an immutable AgentRun CR
6. The run completed and findings were reported via configured channels

## Next Steps

- [CRD Reference](crd-reference.md) — every field explained
- [Writing Skills](skills-authoring.md) — create custom agent expertise
- [Guardrails Deep-Dive](guardrails.md) — understand the safety model
- [Environment Binding](environment-binding.md) — configure for your cluster
- [Data Protection](data-protection.md) — how data resources are protected
- [Troubleshooting](troubleshooting.md) — common issues and solutions
