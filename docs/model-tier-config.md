# Model Tier Configuration

InfraAgent uses **tiers, not model names**. Agent specs declare `tier: fast` or `tier: reasoning` — the runtime maps this to actual provider/model strings via the ModelTierConfig CRD.

## Why Tiers?

- **Portability**: Move agents between clusters without changing specs
- **Cost control**: Swap expensive models for cheaper ones cluster-wide
- **Provider flexibility**: Switch from Anthropic to OpenAI by changing one CR
- **Right-sizing**: Fast agents get fast models, complex agents get reasoning models

## Configuration

```yaml
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
    - tier: reasoning
      provider: anthropic
      model: claude-opus-4-20250514
      maxTokens: 16384
      costPerMillionInput: "15.00"
      costPerMillionOutput: "75.00"
```

## Auth Types

| Type | Description | Secret Contents |
|------|-------------|-----------------|
| `apiKey` | API key header | API key string |
| `oauth` | OAuth2 token refresh | client_id, client_secret, token_url |
| `serviceAccount` | K8s service account token | — (uses pod SA) |
| `none` | No auth (local models) | — |
| `custom` | Custom auth headers | Arbitrary key-value pairs |

### Per-Tier Auth Override

Override auth for a specific tier (e.g. use OpenAI for fast, Anthropic for reasoning):

```yaml
tiers:
  - tier: fast
    provider: openai
    model: gpt-4o-mini
    auth:
      type: apiKey
      secretRef: openai-key
      secretKey: key
  - tier: reasoning
    provider: anthropic
    model: claude-opus-4-20250514
    # Uses defaultAuth (no override)
```

## Cost Estimation

The `costPerMillionInput` and `costPerMillionOutput` fields enable per-run cost estimation in AgentRun records and reports:

```yaml
status:
  usage:
    tokensIn: 3200
    tokensOut: 1800
    estimatedCost: "$0.04"
```

## Provider Support

| Provider | Endpoint | Notes |
|----------|----------|-------|
| `anthropic` | `https://api.anthropic.com/v1/messages` | Native tool use |
| `openai` | `https://api.openai.com/v1/chat/completions` | Function calling |
| Any OpenAI-compatible | Custom endpoint via env var | Ollama, vLLM, etc. |

## Multiple ModelTierConfigs

While the CRD is cluster-scoped and agents reference the `default` config, you can create multiple configs for different teams or environments. Agents select the config by name (defaults to `default`).
