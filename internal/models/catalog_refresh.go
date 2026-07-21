package models

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// Refresher keeps a cached model Catalog current by probing each enabled
// provider on a cadence and folding in declared KnownModels as a fallback.
//
// CADENCE — hourly (defaultCatalogInterval). Model availability changes on
// the order of provider releases and login/logout events, not seconds, so an
// hourly probe keeps the catalog fresh without hammering the CLIs; the boot
// warm-up (Run refreshes immediately, then ticks) closes the cold-start gap.
// On-demand refresh is available via Refresh for callers that need it now.
//
// The delegation hot path NEVER calls a prober: preflight and the API read
// only the cached snapshot via Catalog(), so a slow or wedged provider probe
// can never block a delegation — the worst case is a slightly stale list with
// an observable LastRefreshed timestamp.
//
// PERSISTENCE — none, deliberately. The catalog is cheap to rebuild (a file
// read plus two quick listing commands) and is warmed at boot, so persisting
// it across restarts would add a migration and a staleness-across-restart
// risk for no benefit: an in-memory snapshot refreshed at boot is always at
// least as fresh as a persisted one, and never wrong after a config change.
type Refresher struct {
	mu       sync.RWMutex
	snapshot Catalog

	probers  map[string]ModelProber
	static   StaticModelSource
	universe []string
	interval time.Duration
	now      func() time.Time

	// onRefresh, when set, is invoked with each freshly published snapshot
	// right after Refresh swaps it in. The auth-alert evaluator hangs here so
	// alerting rides the same cadence as the catalog. It runs inside the
	// refresh goroutine and must stay cheap; a slow hook only delays the next
	// tick, never boot or a delegation (which read the cached snapshot).
	onRefresh func(context.Context, Catalog)
}

// StaticModelSource returns the declared KnownModels per provider (typically
// the model-profile catalog from the store). It is the fallback the refresher
// labels "static" when a provider has no usable live source.
type StaticModelSource func(ctx context.Context) (map[string][]string, error)

// RefresherOptions configure a Refresher. Every field is optional; the zero
// value yields an empty catalog that refreshes into nothing.
type RefresherOptions struct {
	// Probers enumerate providers live. First prober wins per provider.
	Probers []ModelProber
	// Static supplies declared KnownModels as the fallback list.
	Static StaticModelSource
	// Providers seeds the catalog universe so an enabled provider appears
	// even with no prober and no declared models.
	Providers []string
	// Interval overrides the refresh cadence (default: hourly).
	Interval time.Duration
	// Clock overrides the time source (tests pin it for stable timestamps).
	Clock func() time.Time
	// OnRefresh, when set, is invoked with each freshly published snapshot
	// after every Refresh. The daemon wires the auth-alert evaluator here so
	// enabled-but-unauthenticated providers are flagged on the same cadence.
	OnRefresh func(context.Context, Catalog)
}

const defaultCatalogInterval = time.Hour

// NewRefresher builds a Refresher from opts. The returned catalog is empty
// until the first Refresh (or Run) executes.
func NewRefresher(opts RefresherOptions) *Refresher {
	probers := make(map[string]ModelProber, len(opts.Probers))
	for _, p := range opts.Probers {
		if p == nil {
			continue
		}
		if _, exists := probers[p.Provider()]; !exists {
			probers[p.Provider()] = p
		}
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = defaultCatalogInterval
	}
	clock := opts.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Refresher{
		probers:   probers,
		static:    opts.Static,
		universe:  append([]string(nil), opts.Providers...),
		interval:  interval,
		now:       clock,
		onRefresh: opts.OnRefresh,
	}
}

// HasRefreshHook reports whether an OnRefresh hook is wired. Exported so the
// daemon boot-wiring test can assert the auth-alert evaluation is attached to
// the refresh loop (an unwired evaluator is exactly the dead-on-arrival
// failure mode this codebase keeps hitting).
func (r *Refresher) HasRefreshHook() bool {
	return r.onRefresh != nil
}

// Catalog returns the last cached snapshot. Safe for concurrent readers and
// never blocks on a probe. A cold Refresher returns the zero Catalog, which
// callers treat as "nothing known yet" and fall back to their own rules.
func (r *Refresher) Catalog() Catalog {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.snapshot
}

// Refresh runs one probe pass, atomically swaps in the new snapshot, and
// returns it. Callable on demand (boot warm-up, API forced refresh).
func (r *Refresher) Refresh(ctx context.Context) Catalog {
	cat := r.buildCatalog(ctx)
	r.mu.Lock()
	r.snapshot = cat
	r.mu.Unlock()
	// Fire post-refresh hooks (auth-alert evaluation) AFTER the snapshot is
	// published, so a hook that reads Catalog() sees the fresh data. Runs
	// synchronously in the refresh goroutine: the hook is deterministic
	// bookkeeping, and keeping it inline makes "evaluated every refresh"
	// observable rather than racing a detached goroutine.
	if r.onRefresh != nil {
		r.onRefresh(ctx, cat)
	}
	return cat
}

// Run warms the catalog immediately, then refreshes on the cadence until ctx
// is cancelled. This is the daemon boot entry point.
func (r *Refresher) Run(ctx context.Context) {
	r.Refresh(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.Refresh(ctx)
		}
	}
}

// buildCatalog probes every provider in the union of the seeded universe, the
// registered probers, and the static source, producing a sorted snapshot.
func (r *Refresher) buildCatalog(ctx context.Context) Catalog {
	now := r.now().UTC()
	staticMap := r.loadStatic(ctx)

	provSet := map[string]struct{}{}
	for _, p := range r.universe {
		provSet[p] = struct{}{}
	}
	for p := range r.probers {
		provSet[p] = struct{}{}
	}
	for p := range staticMap {
		provSet[p] = struct{}{}
	}

	names := make([]string, 0, len(provSet))
	for p := range provSet {
		if p = strings.TrimSpace(p); p != "" {
			names = append(names, p)
		}
	}
	sort.Strings(names)

	entries := make([]ProviderCatalog, 0, len(names))
	for _, name := range names {
		entries = append(entries, r.buildProviderEntry(ctx, name, staticMap[name], now))
	}
	return Catalog{Providers: entries, RefreshedAt: now}
}

// loadStatic pulls the declared KnownModels map, tolerating a nil or failing
// source (the catalog then carries live entries only).
func (r *Refresher) loadStatic(ctx context.Context) map[string][]string {
	if r.static == nil {
		return map[string][]string{}
	}
	m, err := r.static(ctx)
	if err != nil {
		slog.Warn("model catalog: static source failed", "err", err)
		return map[string][]string{}
	}
	if m == nil {
		return map[string][]string{}
	}
	return m
}

// buildProviderEntry resolves one provider's entry: a live probe when a
// prober exists and succeeds, otherwise the static KnownModels fallback with
// a labelled source and an explanatory note.
func (r *Refresher) buildProviderEntry(
	ctx context.Context, name string, staticIDs []string, now time.Time,
) ProviderCatalog {
	entry := ProviderCatalog{Provider: name, LastRefreshed: now}
	prober := r.probers[name]
	if prober == nil {
		entry.Models = dedupeSortModels(staticIDs)
		entry.Source = ModelSourceStatic
		entry.AuthState = ModelAuthNotApplicable
		entry.Note = "no live source; showing declared known models"
		return entry
	}
	res, err := prober.Probe(ctx)
	if err != nil {
		entry.Models = dedupeSortModels(staticIDs)
		entry.Source = ModelSourceStatic
		if errors.Is(err, ErrNoLiveModelSource) {
			entry.AuthState = ModelAuthNotApplicable
			entry.Note = "no live source; showing declared known models"
		} else {
			entry.AuthState = ModelAuthUnknown
			entry.Note = "live probe failed (" + strings.TrimSpace(err.Error()) +
				"); showing declared known models"
		}
		return entry
	}
	entry.Models = dedupeSortModels(res.Models)
	entry.Source = ModelSourceLive
	entry.AuthState = res.AuthState
	if entry.AuthState == "" {
		entry.AuthState = ModelAuthOK
	}
	entry.Note = res.Note
	return entry
}
