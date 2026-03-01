package reliability

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const defaultWindow = 15 * time.Minute

// Inputs captures the telemetry/state needed to compute a reliability scorecard.
type Inputs struct {
	Now          time.Time
	Window       time.Duration
	ControlPlane ControlPlaneInputs
	ProbeFleet   ProbeFleetInputs
	Command      CommandInputs
}

// ControlPlaneInputs summarizes control-plane request telemetry for the scoring window.
type ControlPlaneInputs struct {
	TotalRequests       int
	SuccessfulRequests  int
	ServerErrorRequests int
	P95Latency          time.Duration
}

// ProbeFleetInputs summarizes current probe connectivity state.
type ProbeFleetInputs struct {
	TotalProbes     int
	ConnectedProbes int
}

// CommandInputs summarizes command-result outcomes for the scoring window.
type CommandInputs struct {
	TotalResults      int
	SuccessfulResults int
}

// Scorecard is the additive API response contract for reliability scorecards.
type Scorecard struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Window      ScorecardWindow `json:"window"`
	Overall     Rollup          `json:"overall"`
	Surfaces    []Surface       `json:"surfaces"`
}

// ScorecardWindow describes the scoring time window.
type ScorecardWindow struct {
	Duration string    `json:"duration"`
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
}

// Rollup summarizes score/status/compliance counters.
type Rollup struct {
	Score      int        `json:"score"`
	Status     string     `json:"status"`
	Rationale  string     `json:"rationale"`
	Compliance Compliance `json:"compliance"`
}

// Compliance is a simple pass/warn/fail/unknown counter set.
type Compliance struct {
	Passing int `json:"passing"`
	Warning int `json:"warning"`
	Failing int `json:"failing"`
	Unknown int `json:"unknown"`
}

// Surface represents one reliability surface (control-plane / probe fleet).
type Surface struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Score      int         `json:"score"`
	Status     string      `json:"status"`
	Rationale  string      `json:"rationale"`
	Compliance Compliance  `json:"compliance"`
	Indicators []Indicator `json:"indicators"`
}

// Indicator represents one SLO/SLI pair with explainable thresholds.
type Indicator struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	Score       int       `json:"score"`
	Metric      Metric    `json:"metric"`
	Objective   Objective `json:"objective"`
	Rationale   string    `json:"rationale"`
}

// Metric is the measured SLI value.
type Metric struct {
	Value      *float64 `json:"value,omitempty"`
	Unit       string   `json:"unit"`
	SampleSize int      `json:"sample_size"`
}

// Objective captures target/warn/critical thresholds and comparator semantics.
type Objective struct {
	Target     float64 `json:"target"`
	Warning    float64 `json:"warning"`
	Critical   float64 `json:"critical"`
	Comparator string  `json:"comparator"`
	Window     string  `json:"window"`
}

// BuildScorecard computes additive reliability scorecards from existing telemetry/state.
func BuildScorecard(in Inputs) Scorecard {
	now := in.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	window := in.Window
	if window <= 0 {
		window = defaultWindow
	}

	windowLabel := window.String()

	controlPlane := buildControlPlaneSurface(in.ControlPlane, windowLabel)
	probeFleet := buildProbeFleetSurface(in.ProbeFleet, in.Command, windowLabel)

	surfaces := []Surface{controlPlane, probeFleet}
	overall := summarizeOverall(surfaces)

	return Scorecard{
		GeneratedAt: now,
		Window: ScorecardWindow{
			Duration: windowLabel,
			From:     now.Add(-window),
			To:       now,
		},
		Overall:  overall,
		Surfaces: surfaces,
	}
}

func buildControlPlaneSurface(in ControlPlaneInputs, window string) Surface {
	availability := indicatorHigherBetter(indicatorConfig{
		ID:          "control_plane.availability",
		Name:        "Control-plane API availability",
		Description: "Percentage of sampled control-plane API requests that completed without 5xx responses.",
		Target:      99.5,
		Warning:     99.0,
		Critical:    97.0,
		Comparator:  "gte",
		Unit:        "percent",
		Window:      window,
		SampleSize:  in.TotalRequests,
		NoDataHint:  "No control-plane request samples were observed in the selected window.",
	}, percentage(in.SuccessfulRequests, in.TotalRequests))

	latency := indicatorLowerBetter(indicatorConfig{
		ID:          "control_plane.latency_p95",
		Name:        "Control-plane API latency (p95)",
		Description: "95th percentile response latency for sampled control-plane API requests.",
		Target:      500,
		Warning:     1000,
		Critical:    1500,
		Comparator:  "lte",
		Unit:        "ms",
		Window:      window,
		SampleSize:  in.TotalRequests,
		NoDataHint:  "No latency samples were observed in the selected window.",
	}, float64(in.P95Latency.Milliseconds()))

	errorRate := indicatorLowerBetter(indicatorConfig{
		ID:          "control_plane.error_rate",
		Name:        "Control-plane API error rate",
		Description: "Percentage of sampled control-plane API requests that returned 5xx responses.",
		Target:      1.0,
		Warning:     2.0,
		Critical:    5.0,
		Comparator:  "lte",
		Unit:        "percent",
		Window:      window,
		SampleSize:  in.TotalRequests,
		NoDataHint:  "No control-plane request samples were observed in the selected window.",
	}, percentage(in.ServerErrorRequests, in.TotalRequests))

	surface := Surface{
		ID:         "control_plane",
		Name:       "Control Plane",
		Indicators: []Indicator{availability, latency, errorRate},
	}
	applySurfaceRollup(&surface)
	return surface
}

func buildProbeFleetSurface(probe ProbeFleetInputs, command CommandInputs, window string) Surface {
	connectivity := indicatorHigherBetter(indicatorConfig{
		ID:          "probe_fleet.connectivity",
		Name:        "Probe connectivity",
		Description: "Percentage of registered probes currently in connected states (online/degraded).",
		Target:      95.0,
		Warning:     90.0,
		Critical:    80.0,
		Comparator:  "gte",
		Unit:        "percent",
		Window:      window,
		SampleSize:  probe.TotalProbes,
		NoDataHint:  "No probes are currently registered.",
	}, percentage(probe.ConnectedProbes, probe.TotalProbes))

	commandSuccess := indicatorHigherBetter(indicatorConfig{
		ID:          "probe_fleet.command_success",
		Name:        "Probe command success",
		Description: "Percentage of command result payloads that completed with exit_code=0.",
		Target:      98.0,
		Warning:     95.0,
		Critical:    90.0,
		Comparator:  "gte",
		Unit:        "percent",
		Window:      window,
		SampleSize:  command.TotalResults,
		NoDataHint:  "No command results were observed in the selected window.",
	}, percentage(command.SuccessfulResults, command.TotalResults))

	surface := Surface{
		ID:         "probe_fleet",
		Name:       "Probe Fleet",
		Indicators: []Indicator{connectivity, commandSuccess},
	}
	applySurfaceRollup(&surface)
	return surface
}

func summarizeOverall(surfaces []Surface) Rollup {
	var compliance Compliance
	totalScore := 0
	scoredSurfaces := 0
	for _, surface := range surfaces {
		compliance.Passing += surface.Compliance.Passing
		compliance.Warning += surface.Compliance.Warning
		compliance.Failing += surface.Compliance.Failing
		compliance.Unknown += surface.Compliance.Unknown
		if surface.Status != "unknown" {
			totalScore += surface.Score
			scoredSurfaces++
		}
	}

	score := 0
	status := "unknown"
	if scoredSurfaces > 0 {
		score = int(math.Round(float64(totalScore) / float64(scoredSurfaces)))
		switch {
		case compliance.Failing > 0:
			status = "critical"
		case compliance.Warning > 0:
			status = "warning"
		default:
			status = "healthy"
		}
	}

	rationale := fmt.Sprintf(
		"%d passing, %d warning, %d failing, %d unknown SLO indicators.",
		compliance.Passing,
		compliance.Warning,
		compliance.Failing,
		compliance.Unknown,
	)

	return Rollup{
		Score:      clampScore(score),
		Status:     status,
		Rationale:  rationale,
		Compliance: compliance,
	}
}

type indicatorConfig struct {
	ID          string
	Name        string
	Description string
	Target      float64
	Warning     float64
	Critical    float64
	Comparator  string
	Unit        string
	Window      string
	SampleSize  int
	NoDataHint  string
}

func indicatorHigherBetter(cfg indicatorConfig, value float64) Indicator {
	return buildIndicator(cfg, value, true)
}

func indicatorLowerBetter(cfg indicatorConfig, value float64) Indicator {
	return buildIndicator(cfg, value, false)
}

func buildIndicator(cfg indicatorConfig, value float64, higherBetter bool) Indicator {
	ind := Indicator{
		ID:          cfg.ID,
		Name:        cfg.Name,
		Description: cfg.Description,
		Status:      "unknown",
		Score:       0,
		Metric: Metric{
			Unit:       cfg.Unit,
			SampleSize: cfg.SampleSize,
		},
		Objective: Objective{
			Target:     cfg.Target,
			Warning:    cfg.Warning,
			Critical:   cfg.Critical,
			Comparator: cfg.Comparator,
			Window:     cfg.Window,
		},
		Rationale: cfg.NoDataHint,
	}

	if cfg.SampleSize <= 0 {
		return ind
	}

	ind.Metric.Value = float64Ptr(value)
	ind.Rationale = formatThresholdRationale(cfg, value)

	if higherBetter {
		switch {
		case value >= cfg.Target:
			ind.Status = "pass"
			ind.Score = 100
		case value >= cfg.Warning:
			ind.Status = "warning"
			ind.Score = 70
		case value < cfg.Critical:
			ind.Status = "fail"
			ind.Score = 20
			ind.Rationale += " Severe breach beyond critical threshold."
		default:
			ind.Status = "fail"
			ind.Score = 40
		}
		return ind
	}

	switch {
	case value <= cfg.Target:
		ind.Status = "pass"
		ind.Score = 100
	case value <= cfg.Warning:
		ind.Status = "warning"
		ind.Score = 70
	case value > cfg.Critical:
		ind.Status = "fail"
		ind.Score = 20
		ind.Rationale += " Severe breach beyond critical threshold."
	default:
		ind.Status = "fail"
		ind.Score = 40
	}
	return ind
}

func formatThresholdRationale(cfg indicatorConfig, value float64) string {
	comparator := "<="
	if strings.EqualFold(cfg.Comparator, "gte") {
		comparator = ">="
	}
	return fmt.Sprintf(
		"Observed %.2f %s across %d samples (target %s %.2f, warning %s %.2f, critical %s %.2f).",
		value,
		cfg.Unit,
		cfg.SampleSize,
		comparator,
		cfg.Target,
		comparator,
		cfg.Warning,
		comparator,
		cfg.Critical,
	)
}

func applySurfaceRollup(surface *Surface) {
	if surface == nil {
		return
	}

	compliance := Compliance{}
	scoreTotal := 0
	scoredIndicators := 0
	for _, indicator := range surface.Indicators {
		switch indicator.Status {
		case "pass":
			compliance.Passing++
			scoreTotal += indicator.Score
			scoredIndicators++
		case "warning":
			compliance.Warning++
			scoreTotal += indicator.Score
			scoredIndicators++
		case "fail":
			compliance.Failing++
			scoreTotal += indicator.Score
			scoredIndicators++
		default:
			compliance.Unknown++
		}
	}

	surface.Compliance = compliance
	surface.Score = 0
	surface.Status = "unknown"
	surface.Rationale = "No scored indicators in selected window."

	if scoredIndicators > 0 {
		surface.Score = int(math.Round(float64(scoreTotal) / float64(scoredIndicators)))
		switch {
		case compliance.Failing > 0:
			surface.Status = "critical"
			surface.Rationale = "At least one SLO is failing."
		case compliance.Warning > 0:
			surface.Status = "warning"
			surface.Rationale = "All failing SLOs cleared, but warning-level drift remains."
		default:
			surface.Status = "healthy"
			surface.Rationale = "All scored SLO indicators are meeting target thresholds."
		}
		if compliance.Unknown > 0 {
			surface.Rationale += " Some indicators have no samples yet."
		}
	}

	surface.Score = clampScore(surface.Score)
}

func percentage(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return (float64(numerator) / float64(denominator)) * 100
}

func float64Ptr(v float64) *float64 {
	vv := v
	return &vv
}

func clampScore(v int) int {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
