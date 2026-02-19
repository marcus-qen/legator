# Troubleshooting

Common issues and how to resolve them.

## Agent stuck in Pending

**Symptom:** `kubectl get infraagents` shows `Phase: Pending`

**Cause:** The referenced AgentEnvironment doesn't exist.

**Fix:**
```bash
# Check the condition
kubectl describe infraagent <name> -n <namespace>
# Look for: EnvironmentReady=False, Reason=EnvironmentNotFound

# Verify the environment exists in the same namespace
kubectl get agentenvironments -n <namespace>
```

## Agent not running on schedule

**Symptom:** Agent is `Ready` but no AgentRuns are created.

**Checks:**
1. **Leader election**: Only the leader schedules runs. Check controller logs:
   ```bash
   kubectl logs -n infraagent-system deploy/infraagent-controller | grep "leader"
   ```
2. **Paused**: Check `spec.paused`:
   ```bash
   kubectl get infraagent <name> -o jsonpath='{.spec.paused}'
   ```
3. **Rate limited**: Check controller logs for "rate-limited"
4. **Cron expression**: Validate with `crontab.guru`

## AgentRun Failed — "assembly failed"

**Symptom:** AgentRun with phase=Failed, report says "assembly failed"

**Causes:**
- Skill not found (ConfigMap missing, Git clone failed)
- Invalid skill (missing frontmatter, invalid actions.yaml)
- Environment credential resolution failed (Secret missing)
- ModelTierConfig not found

**Fix:**
```bash
# Check the AgentRun for details
kubectl describe agentrun <name> -n <namespace>

# Check skill source
kubectl get configmap <skill-name> -n <namespace>

# Check model config
kubectl get modeltierconfigs
```

## AgentRun Failed — "LLM call failed"

**Symptom:** AgentRun Failed, report mentions LLM errors.

**Causes:**
- API key invalid or expired
- Provider endpoint unreachable
- Rate limit from provider
- Token budget insufficient for prompt

**Fix:**
```bash
# Check the Secret
kubectl get secret llm-api-key -n <namespace> -o jsonpath='{.data.api-key}' | base64 -d

# Check controller logs for HTTP status codes
kubectl logs -n infraagent-system deploy/infraagent-controller | grep "LLM"

# Check network policy isn't blocking egress
kubectl get networkpolicy -n infraagent-system
```

## All actions blocked

**Symptom:** AgentRun with phase=Blocked, all actions have status=blocked.

**Causes:**
- Autonomy level too low for the actions the agent is trying to take
- Allow list too restrictive
- Deny list catching everything
- Actions not declared in Action Sheet

**Fix:**
```bash
# Check the audit trail
kubectl get agentrun <name> -n <namespace> -o jsonpath='{.status.actions[*].preFlightCheck}'

# Common: agent at "observe" trying to do mutations
# Fix: raise autonomy to "automate-safe" if appropriate
```

## High token usage

**Symptom:** Agents consuming more tokens than expected.

**Diagnosis:**
```bash
# Check per-agent usage
kubectl get agentruns -n <namespace> -l infraagent.io/agent=<name> \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.status.usage.totalTokens}{"\n"}{end}'
```

**Common causes:**
- Verbose tool output (large pod listings)
- Too many iterations
- Prompt too large (too many endpoints, namespaces)

**Fixes:**
- Reduce `tokenBudget` and `maxIterations`
- Use `fast` tier for simple agents
- Reduce environment complexity (fewer namespaces)

## MCP server unavailable

**Symptom:** Agent logs warning about MCP server, reduced capabilities.

**This is expected behaviour.** Agents degrade gracefully when optional MCP servers are down.

**To diagnose:**
```bash
# Check the MCP server is running
kubectl get pods -l app=k8sgpt -n agents

# Check the endpoint
curl http://k8sgpt.agents:8089/health
```

## AgentRuns accumulating

**Symptom:** Many old AgentRun CRs consuming etcd storage.

**Fix:** Enable retention (default: 7-day TTL):

```yaml
# values.yaml
retention:
  enabled: true
  ttl: 168h
  preserveMinPerAgent: 5
```

Or manually clean old runs:

```bash
kubectl delete agentruns -n <namespace> \
  --field-selector=status.phase=Succeeded \
  --older-than=7d
```

## Controller crash loop

**Symptom:** Controller pod restarting repeatedly.

**Checks:**
```bash
# Check logs from previous instance
kubectl logs -n infraagent-system deploy/infraagent-controller --previous

# Check events
kubectl get events -n infraagent-system --sort-by=.lastTimestamp

# Check resource limits
kubectl describe pod -n infraagent-system -l app.kubernetes.io/component=controller
```

**Common causes:**
- CRDs not installed (`make install` or Helm CRDs)
- Insufficient RBAC permissions
- OOM (increase memory limits)

## Webhook triggers not firing

**Symptom:** Alertmanager fires but agent doesn't run.

**Checks:**
1. Verify the agent has a webhook trigger configured
2. Check the webhook handler port is exposed
3. Check the Alertmanager webhook config points to the controller
4. Check debounce settings (default 30s — rapid events may be deduplicated)
5. Check controller logs for "webhook" entries

## Getting Help

1. Check controller logs: `kubectl logs -n infraagent-system deploy/infraagent-controller`
2. Check AgentRun details: `kubectl describe agentrun <name> -n <namespace>`
3. Check InfraAgent conditions: `kubectl describe infraagent <name> -n <namespace>`
4. File an issue: [github.com/marcus-qen/infraagent/issues](https://github.com/marcus-qen/infraagent/issues)
