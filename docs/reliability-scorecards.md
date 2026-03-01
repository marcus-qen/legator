# Reliability scorecards (Stage 3.9.1)

Legator exposes additive reliability scorecards for core surfaces:

- **Control plane**
  - API availability
  - API p95 latency
  - API error-rate (5xx)
- **Probe fleet**
  - connectivity (online/degraded probes)
  - command success (`command.result` exit_code=0)

## Endpoints

- `GET /api/v1/reliability/scorecard`
  - Optional query param: `window` (duration, default `15m`, max `24h`)
- `GET /api/v1/fleet/summary`
  - Includes additive `reliability` payload using the default `15m` window.

Both endpoints require `fleet:read` (or admin).

## Response model

The scorecard response is additive and backward-compatible:

- `generated_at`
- `window` (`duration`, `from`, `to`)
- `overall` (`score`, `status`, compliance counters, rationale)
- `surfaces[]`
  - `id`, `name`, `score`, `status`, compliance counters, rationale
  - `indicators[]`
    - SLI measurement: `metric.value`, `metric.unit`, `metric.sample_size`
    - SLO thresholds: `objective.target`, `objective.warning`, `objective.critical`, `objective.comparator`, `objective.window`
    - explainability: `status`, `score`, `rationale`

## Thresholds

Current default thresholds:

- `control_plane.availability`: target `>= 99.5%`, warning `>= 99.0%`, critical `>= 97.0%`
- `control_plane.latency_p95`: target `<= 500ms`, warning `<= 1000ms`, critical `<= 1500ms`
- `control_plane.error_rate`: target `<= 1.0%`, warning `<= 2.0%`, critical `<= 5.0%`
- `probe_fleet.connectivity`: target `>= 95.0%`, warning `>= 90.0%`, critical `>= 80.0%`
- `probe_fleet.command_success`: target `>= 98.0%`, warning `>= 95.0%`, critical `>= 90.0%`

## Notes

- Scorecards rely on existing in-memory telemetry/state:
  - sampled HTTP request telemetry,
  - fleet probe state,
  - recent `command.result` audit events.
- Indicators with zero samples return `status="unknown"` with explicit rationale.
