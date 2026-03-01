package fleet

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultFederationSnapshotStaleAfter = 5 * time.Minute

// FederationSourceStatus represents source health in the federated read model.
type FederationSourceStatus string

const (
	FederationSourceHealthy     FederationSourceStatus = "healthy"
	FederationSourceDegraded    FederationSourceStatus = "degraded"
	FederationSourceUnavailable FederationSourceStatus = "unavailable"
	FederationSourceUnknown     FederationSourceStatus = "unknown"
)

// FederationConsistencyFreshness describes snapshot freshness across source and rollup views.
type FederationConsistencyFreshness string

const (
	FederationFreshnessFresh   FederationConsistencyFreshness = "fresh"
	FederationFreshnessMixed   FederationConsistencyFreshness = "mixed"
	FederationFreshnessStale   FederationConsistencyFreshness = "stale"
	FederationFreshnessUnknown FederationConsistencyFreshness = "unknown"
)

// FederationConsistencyCompleteness describes completeness guarantees for source and rollup views.
type FederationConsistencyCompleteness string

const (
	FederationCompletenessComplete    FederationConsistencyCompleteness = "complete"
	FederationCompletenessPartial     FederationConsistencyCompleteness = "partial"
	FederationCompletenessUnavailable FederationConsistencyCompleteness = "unavailable"
	FederationCompletenessUnknown     FederationConsistencyCompleteness = "unknown"
)

// FederationFailoverMode communicates source failover behavior used for a response.
type FederationFailoverMode string

const (
	FederationFailoverNone           FederationFailoverMode = "none"
	FederationFailoverCachedSnapshot FederationFailoverMode = "cached_snapshot"
	FederationFailoverUnavailable    FederationFailoverMode = "unavailable"
)

// FederationFilter limits federated inventory queries.
type FederationFilter struct {
	Tag     string `json:"tag,omitempty"`
	Status  string `json:"status,omitempty"`
	Source  string `json:"source,omitempty"`
	Cluster string `json:"cluster,omitempty"`
	Site    string `json:"site,omitempty"`
	Search  string `json:"search,omitempty"`

	TenantID string `json:"tenant_id,omitempty"`
	OrgID    string `json:"org_id,omitempty"`
	ScopeID  string `json:"scope_id,omitempty"`

	AllowedTenantIDs []string `json:"-"`
	AllowedOrgIDs    []string `json:"-"`
	AllowedScopeIDs  []string `json:"-"`
}

// FederationSourceDescriptor identifies an inventory source.
type FederationSourceDescriptor struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Cluster string `json:"cluster,omitempty"`
	Site    string `json:"site,omitempty"`

	TenantID string `json:"tenant_id,omitempty"`
	OrgID    string `json:"org_id,omitempty"`
	ScopeID  string `json:"scope_id,omitempty"`
}

// FederatedSourceAttribution annotates probe data with source metadata.
type FederatedSourceAttribution struct {
	ID      string                 `json:"id"`
	Name    string                 `json:"name"`
	Kind    string                 `json:"kind"`
	Cluster string                 `json:"cluster,omitempty"`
	Site    string                 `json:"site,omitempty"`
	Status  FederationSourceStatus `json:"status"`

	TenantID string `json:"tenant_id,omitempty"`
	OrgID    string `json:"org_id,omitempty"`
	ScopeID  string `json:"scope_id,omitempty"`
}

// FederationSourceResult is the adapter snapshot returned for a source.
type FederationSourceResult struct {
	Inventory   FleetInventory `json:"inventory"`
	Partial     bool           `json:"partial"`
	Warnings    []string       `json:"warnings,omitempty"`
	CollectedAt time.Time      `json:"collected_at,omitempty"`
}

// FederationSourceAdapter returns read-only inventory snapshots for a source.
type FederationSourceAdapter interface {
	Source() FederationSourceDescriptor
	Inventory(ctx context.Context, filter InventoryFilter) (FederationSourceResult, error)
}

// FleetSourceAdapter wraps the existing Fleet inventory as a federation source.
type FleetSourceAdapter struct {
	source FederationSourceDescriptor
	fleet  Fleet
	now    func() time.Time
}

// NewFleetSourceAdapter builds a federation adapter over a Fleet inventory source.
func NewFleetSourceAdapter(fleet Fleet, source FederationSourceDescriptor) *FleetSourceAdapter {
	return &FleetSourceAdapter{
		source: normalizeFederationSourceDescriptor(source),
		fleet:  fleet,
		now:    time.Now,
	}
}

// Source describes this adapter's source metadata.
func (a *FleetSourceAdapter) Source() FederationSourceDescriptor {
	return a.source
}

// Inventory returns a read-only Fleet inventory snapshot.
func (a *FleetSourceAdapter) Inventory(ctx context.Context, filter InventoryFilter) (FederationSourceResult, error) {
	if err := ctx.Err(); err != nil {
		return FederationSourceResult{}, err
	}
	if a.fleet == nil {
		return FederationSourceResult{}, fmt.Errorf("fleet source unavailable")
	}

	return FederationSourceResult{
		Inventory:   a.fleet.Inventory(filter),
		CollectedAt: a.now().UTC(),
	}, nil
}

// FederatedProbeInventory is a probe summary annotated with source attribution.
type FederatedProbeInventory struct {
	Source FederatedSourceAttribution `json:"source"`
	Probe  ProbeInventorySummary      `json:"probe"`
}

// FederatedSourceConsistency communicates freshness/completeness/failover semantics for a source snapshot.
type FederatedSourceConsistency struct {
	Freshness          FederationConsistencyFreshness    `json:"freshness"`
	Completeness       FederationConsistencyCompleteness `json:"completeness"`
	Degraded           bool                              `json:"degraded"`
	FailoverMode       FederationFailoverMode            `json:"failover_mode"`
	SnapshotAgeSeconds int64                             `json:"snapshot_age_seconds,omitempty"`
}

// FederatedSourceSummary reports per-source inventory + health state.
type FederatedSourceSummary struct {
	Source      FederatedSourceAttribution `json:"source"`
	Aggregates  FleetAggregates            `json:"aggregates"`
	Partial     bool                       `json:"partial"`
	Warnings    []string                   `json:"warnings,omitempty"`
	Error       string                     `json:"error,omitempty"`
	CollectedAt time.Time                  `json:"collected_at,omitempty"`
	Consistency FederatedSourceConsistency `json:"consistency"`
}

// FederatedSourceHealth represents per-source health rollup entries.
type FederatedSourceHealth struct {
	Source      FederatedSourceAttribution `json:"source"`
	Partial     bool                       `json:"partial"`
	Warnings    []string                   `json:"warnings,omitempty"`
	Error       string                     `json:"error,omitempty"`
	Consistency FederatedSourceConsistency `json:"consistency"`
}

// FederationHealthRollup summarizes overall and per-source source health.
type FederationHealthRollup struct {
	Overall     FederationSourceStatus  `json:"overall"`
	Healthy     int                     `json:"healthy"`
	Degraded    int                     `json:"degraded"`
	Unavailable int                     `json:"unavailable"`
	Unknown     int                     `json:"unknown"`
	Sources     []FederatedSourceHealth `json:"sources"`
}

// FederationConsistency summarizes consistency guard behavior for the full federated response.
type FederationConsistency struct {
	Freshness          FederationConsistencyFreshness    `json:"freshness"`
	Completeness       FederationConsistencyCompleteness `json:"completeness"`
	Degraded           bool                              `json:"degraded"`
	PartialResults     bool                              `json:"partial_results"`
	FailoverActive     bool                              `json:"failover_active"`
	StaleSources       int                               `json:"stale_sources"`
	PartialSources     int                               `json:"partial_sources"`
	UnavailableSources int                               `json:"unavailable_sources"`
	FailoverSources    int                               `json:"failover_sources"`
}

// FederatedAggregates summarizes fleet totals across sources.
type FederatedAggregates struct {
	TotalSources        int            `json:"total_sources"`
	HealthySources      int            `json:"healthy_sources"`
	DegradedSources     int            `json:"degraded_sources"`
	UnavailableSources  int            `json:"unavailable_sources"`
	UnknownSources      int            `json:"unknown_sources"`
	TotalProbes         int            `json:"total_probes"`
	Online              int            `json:"online"`
	TotalCPUs           int            `json:"total_cpus"`
	TotalRAMBytes       uint64         `json:"total_ram_bytes"`
	ProbesByOS          map[string]int `json:"probes_by_os"`
	TagDistribution     map[string]int `json:"tag_distribution"`
	SourceDistribution  map[string]int `json:"source_distribution"`
	ClusterDistribution map[string]int `json:"cluster_distribution"`
	SiteDistribution    map[string]int `json:"site_distribution"`
	TenantDistribution  map[string]int `json:"tenant_distribution"`
	OrgDistribution     map[string]int `json:"org_distribution"`
	ScopeDistribution   map[string]int `json:"scope_distribution"`
}

// FederatedInventory is the additive API payload for federated inventory reads.
type FederatedInventory struct {
	Probes      []FederatedProbeInventory `json:"probes"`
	Sources     []FederatedSourceSummary  `json:"sources"`
	Aggregates  FederatedAggregates       `json:"aggregates"`
	Health      FederationHealthRollup    `json:"health"`
	Consistency FederationConsistency     `json:"consistency"`
}

// FederatedInventorySummary is the additive API payload for federation summaries.
type FederatedInventorySummary struct {
	Sources     []FederatedSourceSummary `json:"sources"`
	Aggregates  FederatedAggregates      `json:"aggregates"`
	Health      FederationHealthRollup   `json:"health"`
	Consistency FederationConsistency    `json:"consistency"`
}

// FederationStore aggregates read-only inventory snapshots from multiple sources.
type FederationStore struct {
	mu         sync.RWMutex
	adapters   map[string]FederationSourceAdapter
	snapshots  map[string]FederationSourceResult
	now        func() time.Time
	staleAfter time.Duration
}

// NewFederationStore creates a federation read-model store over source adapters.
func NewFederationStore(adapters ...FederationSourceAdapter) *FederationStore {
	store := &FederationStore{
		adapters:   make(map[string]FederationSourceAdapter, len(adapters)),
		snapshots:  map[string]FederationSourceResult{},
		now:        time.Now,
		staleAfter: defaultFederationSnapshotStaleAfter,
	}
	for _, adapter := range adapters {
		store.RegisterSource(adapter)
	}
	return store
}

// RegisterSource adds or replaces an inventory source adapter.
func (s *FederationStore) RegisterSource(adapter FederationSourceAdapter) {
	if adapter == nil {
		return
	}

	source := normalizeFederationSourceDescriptor(adapter.Source())
	if source.ID == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.adapters[source.ID] = &descriptorSourceAdapter{
		source:  source,
		adapter: adapter,
	}
}

// Inventory aggregates read-only inventory views across registered sources.
func (s *FederationStore) Inventory(ctx context.Context, filter FederationFilter) FederatedInventory {
	result := FederatedInventory{
		Probes:  []FederatedProbeInventory{},
		Sources: []FederatedSourceSummary{},
		Aggregates: FederatedAggregates{
			ProbesByOS:          map[string]int{},
			TagDistribution:     map[string]int{},
			SourceDistribution:  map[string]int{},
			ClusterDistribution: map[string]int{},
			SiteDistribution:    map[string]int{},
			TenantDistribution:  map[string]int{},
			OrgDistribution:     map[string]int{},
			ScopeDistribution:   map[string]int{},
		},
		Health: FederationHealthRollup{Sources: []FederatedSourceHealth{}},
		Consistency: FederationConsistency{
			Freshness:    FederationFreshnessUnknown,
			Completeness: FederationCompletenessUnknown,
		},
	}

	adapters := s.adaptersSnapshot()
	invFilter := normalizeFederationInventoryFilter(InventoryFilter{
		Tag:    filter.Tag,
		Status: filter.Status,
	})
	searchNeedle := normalizeFederationSearch(filter.Search)
	observedAt := s.now().UTC()

	for _, adapter := range adapters {
		source := normalizeFederationSourceDescriptor(adapter.Source())
		if !matchesFederationSourceFilter(source, filter) {
			continue
		}

		sourceMatchesSearch := searchNeedle == "" || matchesFederationSearchInSource(source, searchNeedle)
		summary := FederatedSourceSummary{
			Source: FederatedSourceAttribution{
				ID:       source.ID,
				Name:     source.Name,
				Kind:     source.Kind,
				Cluster:  source.Cluster,
				Site:     source.Site,
				Status:   FederationSourceHealthy,
				TenantID: source.TenantID,
				OrgID:    source.OrgID,
				ScopeID:  source.ScopeID,
			},
			Aggregates: FleetAggregates{
				ProbesByOS:      map[string]int{},
				TagDistribution: map[string]int{},
			},
			Consistency: FederatedSourceConsistency{
				Freshness:    FederationFreshnessUnknown,
				Completeness: FederationCompletenessUnknown,
				FailoverMode: FederationFailoverNone,
			},
		}

		cacheKey := federationSnapshotCacheKey(source.ID, invFilter)
		sourceResult, sourceErr := adapter.Inventory(ctx, invFilter)
		failoverUsed := false
		if sourceErr == nil {
			s.storeSnapshot(cacheKey, sourceResult)
		} else {
			cached, ok := s.cachedSnapshot(cacheKey)
			if !ok {
				if searchNeedle != "" && !sourceMatchesSearch {
					continue
				}

				result.Aggregates.TotalSources++
				summary.Source.Status = FederationSourceUnavailable
				summary.Error = sourceErr.Error()
				summary.Consistency = FederatedSourceConsistency{
					Freshness:    FederationFreshnessUnknown,
					Completeness: FederationCompletenessUnavailable,
					Degraded:     true,
					FailoverMode: FederationFailoverUnavailable,
				}
				result.Sources = append(result.Sources, summary)
				result.Health.Sources = append(result.Health.Sources, FederatedSourceHealth{
					Source:      summary.Source,
					Error:       summary.Error,
					Consistency: summary.Consistency,
				})
				result.bumpHealthCounters(FederationSourceUnavailable)
				continue
			}

			sourceResult = cached
			failoverUsed = true
		}

		filteredProbes := filterFederatedProbes(sourceResult.Inventory.Probes, source, invFilter, searchNeedle, sourceMatchesSearch)
		if searchNeedle != "" && len(filteredProbes) == 0 && !sourceMatchesSearch {
			continue
		}

		summary.Warnings = dedupeAndSortStrings(sourceResult.Warnings)
		summary.CollectedAt = sourceResult.CollectedAt.UTC()
		summary.Aggregates = aggregateProbeSummaries(filteredProbes)
		summary.Partial = sourceResult.Partial || failoverUsed
		if sourceErr != nil {
			summary.Error = strings.TrimSpace(sourceErr.Error())
		}

		sourceConsistency, sourceStatus, extraWarnings := classifyFederationSourceConsistency(sourceResult, observedAt, s.staleAfter, sourceErr, failoverUsed)
		summary.Consistency = sourceConsistency
		summary.Source.Status = sourceStatus
		if summary.Consistency.Completeness == FederationCompletenessPartial {
			summary.Partial = true
		}
		if len(extraWarnings) > 0 {
			summary.Warnings = dedupeAndSortStrings(append(summary.Warnings, extraWarnings...))
		}

		result.Aggregates.TotalSources++
		result.bumpHealthCounters(summary.Source.Status)

		result.Sources = append(result.Sources, summary)
		result.Health.Sources = append(result.Health.Sources, FederatedSourceHealth{
			Source:      summary.Source,
			Partial:     summary.Partial,
			Warnings:    append([]string(nil), summary.Warnings...),
			Error:       summary.Error,
			Consistency: summary.Consistency,
		})

		result.Aggregates.TotalProbes += summary.Aggregates.TotalProbes
		result.Aggregates.Online += summary.Aggregates.Online
		result.Aggregates.TotalCPUs += summary.Aggregates.TotalCPUs
		result.Aggregates.TotalRAMBytes += summary.Aggregates.TotalRAMBytes

		for osKey, count := range summary.Aggregates.ProbesByOS {
			result.Aggregates.ProbesByOS[osKey] += count
		}
		for tagKey, count := range summary.Aggregates.TagDistribution {
			result.Aggregates.TagDistribution[tagKey] += count
		}

		result.Aggregates.SourceDistribution[source.ID] += summary.Aggregates.TotalProbes
		result.Aggregates.ClusterDistribution[source.Cluster] += summary.Aggregates.TotalProbes
		result.Aggregates.SiteDistribution[source.Site] += summary.Aggregates.TotalProbes
		result.Aggregates.TenantDistribution[source.TenantID] += summary.Aggregates.TotalProbes
		result.Aggregates.OrgDistribution[source.OrgID] += summary.Aggregates.TotalProbes
		result.Aggregates.ScopeDistribution[source.ScopeID] += summary.Aggregates.TotalProbes

		for _, probe := range filteredProbes {
			result.Probes = append(result.Probes, FederatedProbeInventory{
				Source: summary.Source,
				Probe:  cloneProbeInventorySummary(probe),
			})
		}
	}

	result.Health.Overall = computeOverallFederationHealth(result.Health)
	result.Consistency = computeFederationConsistency(result.Sources, result.Health)
	sort.Slice(result.Sources, func(i, j int) bool {
		return result.Sources[i].Source.ID < result.Sources[j].Source.ID
	})
	sort.Slice(result.Health.Sources, func(i, j int) bool {
		return result.Health.Sources[i].Source.ID < result.Health.Sources[j].Source.ID
	})
	sort.Slice(result.Probes, func(i, j int) bool {
		lhs := strings.ToLower(strings.TrimSpace(result.Probes[i].Probe.Hostname))
		rhs := strings.ToLower(strings.TrimSpace(result.Probes[j].Probe.Hostname))
		if lhs == "" {
			lhs = result.Probes[i].Probe.ID
		}
		if rhs == "" {
			rhs = result.Probes[j].Probe.ID
		}
		if lhs == rhs {
			return result.Probes[i].Source.ID < result.Probes[j].Source.ID
		}
		return lhs < rhs
	})

	return result
}

// Summary returns federated health + aggregate rollups without full probe listings.
func (s *FederationStore) Summary(ctx context.Context, filter FederationFilter) FederatedInventorySummary {
	inventory := s.Inventory(ctx, filter)
	return FederatedInventorySummary{
		Sources:     inventory.Sources,
		Aggregates:  inventory.Aggregates,
		Health:      inventory.Health,
		Consistency: inventory.Consistency,
	}
}

func (s *FederationStore) adaptersSnapshot() []FederationSourceAdapter {
	s.mu.RLock()
	defer s.mu.RUnlock()

	adapters := make([]FederationSourceAdapter, 0, len(s.adapters))
	for _, adapter := range s.adapters {
		adapters = append(adapters, adapter)
	}
	sort.Slice(adapters, func(i, j int) bool {
		return adapters[i].Source().ID < adapters[j].Source().ID
	})
	return adapters
}

func (s *FederationStore) storeSnapshot(key string, result FederationSourceResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[key] = cloneFederationSourceResult(result)
}

func (s *FederationStore) cachedSnapshot(key string) (FederationSourceResult, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result, ok := s.snapshots[key]
	if !ok {
		return FederationSourceResult{}, false
	}
	return cloneFederationSourceResult(result), true
}

func (r *FederatedInventory) bumpHealthCounters(status FederationSourceStatus) {
	switch status {
	case FederationSourceHealthy:
		r.Health.Healthy++
		r.Aggregates.HealthySources++
	case FederationSourceDegraded:
		r.Health.Degraded++
		r.Aggregates.DegradedSources++
	case FederationSourceUnavailable:
		r.Health.Unavailable++
		r.Aggregates.UnavailableSources++
	default:
		r.Health.Unknown++
		r.Aggregates.UnknownSources++
	}
}

func classifyFederationSourceConsistency(result FederationSourceResult, observedAt time.Time, staleAfter time.Duration, sourceErr error, failoverUsed bool) (FederatedSourceConsistency, FederationSourceStatus, []string) {
	consistency := FederatedSourceConsistency{
		Freshness:    FederationFreshnessUnknown,
		Completeness: FederationCompletenessComplete,
		FailoverMode: FederationFailoverNone,
	}
	extraWarnings := []string{}

	if sourceErr != nil && !failoverUsed {
		consistency.Completeness = FederationCompletenessUnavailable
		consistency.Degraded = true
		consistency.FailoverMode = FederationFailoverUnavailable
		return consistency, FederationSourceUnavailable, extraWarnings
	}

	if failoverUsed {
		consistency.FailoverMode = FederationFailoverCachedSnapshot
		extraWarnings = append(extraWarnings, "source unavailable; serving cached snapshot")
	}

	if !result.CollectedAt.IsZero() {
		age := observedAt.Sub(result.CollectedAt.UTC())
		if age < 0 {
			age = 0
		}
		consistency.SnapshotAgeSeconds = int64(age / time.Second)
		if staleAfter > 0 && age > staleAfter {
			consistency.Freshness = FederationFreshnessStale
			extraWarnings = append(extraWarnings, fmt.Sprintf("snapshot stale (%ds old)", consistency.SnapshotAgeSeconds))
		} else {
			consistency.Freshness = FederationFreshnessFresh
		}
	}

	warningCount := len(dedupeAndSortStrings(result.Warnings))
	isStale := consistency.Freshness == FederationFreshnessStale
	if result.Partial || failoverUsed || isStale {
		consistency.Completeness = FederationCompletenessPartial
	}

	consistency.Degraded = result.Partial || warningCount > 0 || failoverUsed || isStale
	if consistency.Degraded {
		return consistency, FederationSourceDegraded, extraWarnings
	}
	return consistency, FederationSourceHealthy, extraWarnings
}

func computeFederationConsistency(sources []FederatedSourceSummary, health FederationHealthRollup) FederationConsistency {
	consistency := FederationConsistency{
		Freshness:    FederationFreshnessUnknown,
		Completeness: FederationCompletenessUnknown,
	}
	if len(sources) == 0 {
		return consistency
	}

	freshSources := 0
	staleSources := 0
	completeSources := 0

	for _, source := range sources {
		sourceConsistency := source.Consistency
		switch sourceConsistency.Freshness {
		case FederationFreshnessFresh:
			freshSources++
		case FederationFreshnessStale:
			staleSources++
			consistency.StaleSources++
			consistency.PartialResults = true
		}

		switch sourceConsistency.Completeness {
		case FederationCompletenessComplete:
			completeSources++
		case FederationCompletenessPartial:
			consistency.PartialSources++
			consistency.PartialResults = true
		case FederationCompletenessUnavailable:
			consistency.UnavailableSources++
			consistency.PartialResults = true
		}

		if sourceConsistency.FailoverMode == FederationFailoverCachedSnapshot {
			consistency.FailoverSources++
			consistency.FailoverActive = true
			consistency.PartialResults = true
		}
		if sourceConsistency.Degraded {
			consistency.Degraded = true
		}
	}

	if staleSources > 0 && freshSources > 0 {
		consistency.Freshness = FederationFreshnessMixed
	} else if staleSources > 0 {
		consistency.Freshness = FederationFreshnessStale
	} else if freshSources > 0 {
		consistency.Freshness = FederationFreshnessFresh
	}

	total := len(sources)
	switch {
	case completeSources == total:
		consistency.Completeness = FederationCompletenessComplete
	case consistency.UnavailableSources == total:
		consistency.Completeness = FederationCompletenessUnavailable
	default:
		consistency.Completeness = FederationCompletenessPartial
	}

	if health.Overall == FederationSourceDegraded || health.Overall == FederationSourceUnavailable {
		consistency.Degraded = true
	}

	return consistency
}

func computeOverallFederationHealth(health FederationHealthRollup) FederationSourceStatus {
	total := health.Healthy + health.Degraded + health.Unavailable + health.Unknown
	if total == 0 {
		return FederationSourceUnknown
	}
	if health.Unavailable == total {
		return FederationSourceUnavailable
	}
	if health.Unavailable > 0 || health.Degraded > 0 {
		return FederationSourceDegraded
	}
	if health.Healthy > 0 {
		return FederationSourceHealthy
	}
	return FederationSourceUnknown
}

func normalizeFederationInventoryFilter(filter InventoryFilter) InventoryFilter {
	filter.Tag = strings.TrimSpace(filter.Tag)
	filter.Status = strings.TrimSpace(filter.Status)
	return filter
}

func federationSnapshotCacheKey(sourceID string, filter InventoryFilter) string {
	return fmt.Sprintf("%s|tag=%s|status=%s", strings.ToLower(strings.TrimSpace(sourceID)), strings.ToLower(strings.TrimSpace(filter.Tag)), strings.ToLower(strings.TrimSpace(filter.Status)))
}

func matchesFederationSourceFilter(source FederationSourceDescriptor, filter FederationFilter) bool {
	sourceNeedle := strings.ToLower(strings.TrimSpace(filter.Source))
	if sourceNeedle != "" {
		sourceID := strings.ToLower(strings.TrimSpace(source.ID))
		sourceName := strings.ToLower(strings.TrimSpace(source.Name))
		if sourceNeedle != sourceID && sourceNeedle != sourceName {
			return false
		}
	}

	clusterNeedle := strings.ToLower(strings.TrimSpace(filter.Cluster))
	if clusterNeedle != "" && clusterNeedle != strings.ToLower(strings.TrimSpace(source.Cluster)) {
		return false
	}

	siteNeedle := strings.ToLower(strings.TrimSpace(filter.Site))
	if siteNeedle != "" && siteNeedle != strings.ToLower(strings.TrimSpace(source.Site)) {
		return false
	}

	tenantNeedle := strings.ToLower(strings.TrimSpace(filter.TenantID))
	if tenantNeedle != "" && tenantNeedle != strings.ToLower(strings.TrimSpace(source.TenantID)) {
		return false
	}

	orgNeedle := strings.ToLower(strings.TrimSpace(filter.OrgID))
	if orgNeedle != "" && orgNeedle != strings.ToLower(strings.TrimSpace(source.OrgID)) {
		return false
	}

	scopeNeedle := strings.ToLower(strings.TrimSpace(filter.ScopeID))
	if scopeNeedle != "" && scopeNeedle != strings.ToLower(strings.TrimSpace(source.ScopeID)) {
		return false
	}

	if !matchesFederationAllowedDimension(source.TenantID, filter.AllowedTenantIDs) {
		return false
	}
	if !matchesFederationAllowedDimension(source.OrgID, filter.AllowedOrgIDs) {
		return false
	}
	if !matchesFederationAllowedDimension(source.ScopeID, filter.AllowedScopeIDs) {
		return false
	}

	return true
}

func matchesFederationAllowedDimension(value string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	needle := strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if needle == strings.ToLower(strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func filterFederatedProbes(probes []ProbeInventorySummary, source FederationSourceDescriptor, filter InventoryFilter, searchNeedle string, sourceMatchesSearch bool) []ProbeInventorySummary {
	if len(probes) == 0 {
		return nil
	}

	out := make([]ProbeInventorySummary, 0, len(probes))
	for _, probe := range probes {
		if !matchesFederatedProbeFilter(probe, source, filter, searchNeedle, sourceMatchesSearch) {
			continue
		}
		out = append(out, cloneProbeInventorySummary(probe))
	}
	return out
}

func matchesFederatedProbeFilter(probe ProbeInventorySummary, source FederationSourceDescriptor, filter InventoryFilter, searchNeedle string, sourceMatchesSearch bool) bool {
	statusNeedle := strings.ToLower(strings.TrimSpace(filter.Status))
	if statusNeedle != "" && statusNeedle != strings.ToLower(strings.TrimSpace(probe.Status)) {
		return false
	}

	tagNeedle := strings.ToLower(strings.TrimSpace(filter.Tag))
	if tagNeedle != "" && !hasTag(probe.Tags, tagNeedle) {
		return false
	}

	if searchNeedle == "" {
		return true
	}
	if sourceMatchesSearch {
		return true
	}
	return matchesFederationSearchInProbe(probe, searchNeedle)
}

func normalizeFederationSearch(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func matchesFederationSearchInSource(source FederationSourceDescriptor, needle string) bool {
	if needle == "" {
		return true
	}
	fields := []string{source.ID, source.Name, source.Kind, source.Cluster, source.Site, source.TenantID, source.OrgID, source.ScopeID}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(strings.TrimSpace(field)), needle) {
			return true
		}
	}
	return false
}

func matchesFederationSearchInProbe(probe ProbeInventorySummary, needle string) bool {
	if needle == "" {
		return true
	}

	fields := []string{
		probe.ID,
		probe.Hostname,
		probe.Status,
		probe.OS,
		probe.Arch,
		probe.Kernel,
		string(probe.PolicyLevel),
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(strings.TrimSpace(field)), needle) {
			return true
		}
	}

	for _, tag := range probe.Tags {
		if strings.Contains(strings.ToLower(strings.TrimSpace(tag)), needle) {
			return true
		}
	}

	return false
}

func aggregateProbeSummaries(probes []ProbeInventorySummary) FleetAggregates {
	out := FleetAggregates{
		ProbesByOS:      map[string]int{},
		TagDistribution: map[string]int{},
	}
	for _, probe := range probes {
		out.TotalProbes++
		if strings.EqualFold(strings.TrimSpace(probe.Status), "online") {
			out.Online++
		}
		out.TotalCPUs += probe.CPUs
		out.TotalRAMBytes += probe.RAMBytes

		osKey := strings.ToLower(strings.TrimSpace(probe.OS))
		if osKey == "" {
			osKey = "unknown"
		}
		out.ProbesByOS[osKey]++

		for _, tag := range probe.Tags {
			trimmed := strings.TrimSpace(tag)
			if trimmed == "" {
				continue
			}
			out.TagDistribution[trimmed]++
		}
	}
	return out
}

func normalizeFederationSourceDescriptor(source FederationSourceDescriptor) FederationSourceDescriptor {
	source.ID = sanitizeSourceID(source.ID)
	if source.ID == "" {
		source.ID = sanitizeSourceID(source.Name)
	}
	if source.ID == "" {
		source.ID = "source"
	}

	source.Name = strings.TrimSpace(source.Name)
	if source.Name == "" {
		source.Name = source.ID
	}

	source.Kind = strings.TrimSpace(source.Kind)
	if source.Kind == "" {
		source.Kind = "fleet"
	}

	source.Cluster = strings.TrimSpace(source.Cluster)
	if source.Cluster == "" {
		source.Cluster = "unknown"
	}

	source.Site = strings.TrimSpace(source.Site)
	if source.Site == "" {
		source.Site = "unknown"
	}

	source.TenantID = normalizeFederationTenantField(source.TenantID)
	source.OrgID = normalizeFederationTenantField(source.OrgID)
	source.ScopeID = normalizeFederationTenantField(source.ScopeID)

	return source
}

func normalizeFederationTenantField(raw string) string {
	norm := strings.ToLower(strings.TrimSpace(raw))
	if norm == "" {
		return "default"
	}
	return norm
}

func sanitizeSourceID(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}

	var out strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			out.WriteRune(r)
		case r >= '0' && r <= '9':
			out.WriteRune(r)
		case r == '-', r == '_':
			out.WriteRune(r)
		default:
			out.WriteRune('-')
		}
	}
	return strings.Trim(out.String(), "-")
}

func dedupeAndSortStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		seen[trimmed] = struct{}{}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func cloneProbeInventorySummary(probe ProbeInventorySummary) ProbeInventorySummary {
	clone := probe
	if probe.Tags != nil {
		clone.Tags = append([]string(nil), probe.Tags...)
	}
	return clone
}

func cloneFederationSourceResult(result FederationSourceResult) FederationSourceResult {
	clone := result
	clone.Warnings = append([]string(nil), result.Warnings...)
	clone.Inventory = cloneFleetInventory(result.Inventory)
	return clone
}

func cloneFleetInventory(inventory FleetInventory) FleetInventory {
	clone := FleetInventory{
		Probes:     make([]ProbeInventorySummary, 0, len(inventory.Probes)),
		Aggregates: inventory.Aggregates,
	}
	for _, probe := range inventory.Probes {
		clone.Probes = append(clone.Probes, cloneProbeInventorySummary(probe))
	}
	clone.Aggregates.ProbesByOS = cloneStringIntMap(inventory.Aggregates.ProbesByOS)
	clone.Aggregates.TagDistribution = cloneStringIntMap(inventory.Aggregates.TagDistribution)
	return clone
}

func cloneStringIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return map[string]int{}
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

type descriptorSourceAdapter struct {
	source  FederationSourceDescriptor
	adapter FederationSourceAdapter
}

func (a *descriptorSourceAdapter) Source() FederationSourceDescriptor {
	return a.source
}

func (a *descriptorSourceAdapter) Inventory(ctx context.Context, filter InventoryFilter) (FederationSourceResult, error) {
	if a.adapter == nil {
		return FederationSourceResult{}, fmt.Errorf("source adapter unavailable")
	}
	result, err := a.adapter.Inventory(ctx, filter)
	if err != nil {
		return FederationSourceResult{}, err
	}
	if result.CollectedAt.IsZero() {
		result.CollectedAt = time.Now().UTC()
	}
	return result, nil
}
