package api

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
	"golang.org/x/sync/errgroup"
)

type dashboardHandler struct {
	sessionStore      store.SessionStore
	auditStore        store.AuditStore
	downstreamStore   store.DownstreamServerStore
	approvalStore     store.ToolApprovalStore
	peerStore         store.P2PPeerStore      // optional; supplies peers_online/total
	meshStore         MeshCountStore          // optional; supplies mesh time-series
	manager           *downstream.Manager     // optional
	toolCache         *cache.ToolCache        // optional
	engine            *routing.Engine         // optional
	delegationCounter delegationReviewCounter // optional; supplies unreviewed_delegations count

	cacheMu  sync.RWMutex
	cacheKey string             // "range:seconds" key
	cacheVal *dashboardResponse // cached result
	cacheAt  time.Time          // when cache was stored
}

// MeshCountStore is the subset of the store needed for mesh-activity rollups.
// Kept narrow so tests can swap it without dragging in the full store.
type MeshCountStore interface {
	GetMeshMessageCountsBucketed(ctx context.Context, after, before time.Time, bucketSec int) (map[int64]int, error)
}

// delegationReviewCounter is the narrow capability the dashboard uses to
// surface unreviewed delegation count (review-sweep visibility metric).
// *workersadmin.Service satisfies it.
type delegationReviewCounter interface {
	CountUnreviewedRequiredDelegations(ctx context.Context) (int, error)
}

// peerOnlineWindow is the recency window used to classify a paired peer as
// "online" for the dashboard tile. Set to 5 minutes — long enough to cover
// momentary p2p reconnects, short enough that an actually-offline peer
// doesn't show as online for half an hour.
const peerOnlineWindow = 5 * time.Minute

const dashboardCacheTTL = 30 * time.Second

type downstreamStatus struct {
	ServerID      string `json:"server_id"`
	ServerName    string `json:"server_name"`
	InstanceCount int    `json:"instance_count"`
	State         string `json:"state"`
	Disabled      bool   `json:"disabled"`
}

type dashboardResponse struct {
	ActiveSessions    int                          `json:"active_sessions"`
	ActiveSessionList []store.Session              `json:"active_session_list"`
	ActiveDownstreams []downstreamStatus           `json:"active_downstreams"`
	RecentErrors      []store.AuditRecord          `json:"recent_errors"`
	RecentCalls       []store.AuditRecord          `json:"recent_calls"`
	Stats             *store.AuditStats            `json:"stats,omitempty"`
	TimeSeries        []store.TimeSeriesPoint      `json:"timeseries"`
	ToolLeaderboard   []store.ToolLeaderboardEntry `json:"tool_leaderboard"`
	ServerHealth      []store.ServerHealthEntry    `json:"server_health"`
	ErrorBreakdown    []store.ErrorBreakdownEntry  `json:"error_breakdown"`
	RouteHitMap       []store.RouteHitEntry        `json:"route_hit_map"`
	ApprovalMetrics   *store.ApprovalMetrics       `json:"approval_metrics,omitempty"`
	CacheStats        *cacheStatsResponse          `json:"cache_stats,omitempty"`
	// PeersOnline is the number of paired peers that are currently
	// reachable (last_seen within the recency window). PeersTotal is
	// every paired peer including offline. Both are point-in-time, not
	// time series — peer churn is a session-rate signal, not a per-bucket one.
	PeersOnline int `json:"peers_online"`
	PeersTotal  int `json:"peers_total"`
	// MeshMessages24h is the count of mesh messages whose created_at
	// falls in the selected dashboard range. Surfaced as the headline
	// number for the mesh-activity tile (the per-bucket counts are
	// already on TimeSeries[].MeshMessages).
	MeshMessages int `json:"mesh_messages"`
	// ServerTimings holds the most-recent tools/list outcome per
	// downstream — fast/slow/timeout/error + elapsed_ms. Dashboard
	// renders this as the "Server Performance" panel so users can spot
	// regressions without grepping logs.
	ServerTimings []downstream.ServerTiming `json:"server_timings"`
	// UnreviewedDelegations is the count of token-preserving delegations
	// (distinct delegation IDs) across all workspaces where review_required
	// is true and the parent has not yet called review_delegation.
	// Backs dashboard attention for review-sweep of unreviewed work.
	UnreviewedDelegations int `json:"unreviewed_delegations"`
}

// rangeConfig holds computed parameters for a dashboard time range.
type rangeConfig struct {
	statsWindow time.Duration
	bucketSec   int
	dataPoints  int
	callsLimit  int
	errorsLimit int
}

var rangeConfigs = map[string]rangeConfig{
	"1h":  {statsWindow: 1 * time.Hour, bucketSec: 60, dataPoints: 60, callsLimit: 20, errorsLimit: 10},
	"6h":  {statsWindow: 6 * time.Hour, bucketSec: 300, dataPoints: 72, callsLimit: 50, errorsLimit: 20},
	"24h": {statsWindow: 24 * time.Hour, bucketSec: 900, dataPoints: 96, callsLimit: 50, errorsLimit: 20},
	"7d":  {statsWindow: 7 * 24 * time.Hour, bucketSec: 3600, dataPoints: 168, callsLimit: 50, errorsLimit: 20},
}

func (h *dashboardHandler) get(w http.ResponseWriter, r *http.Request) {
	rangeParam := r.URL.Query().Get("range")
	rc, ok := rangeConfigs[rangeParam]
	if !ok {
		rc = rangeConfigs["1h"]
		rangeParam = "1h"
	}

	// Check cache.
	h.cacheMu.RLock()
	if h.cacheKey == rangeParam && h.cacheVal != nil && time.Since(h.cacheAt) < dashboardCacheTTL {
		val := h.cacheVal
		h.cacheMu.RUnlock()
		writeJSON(w, http.StatusOK, val)
		return
	}
	h.cacheMu.RUnlock()

	resp, err := h.fetchDashboard(r.Context(), rc)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Store in cache.
	h.cacheMu.Lock()
	h.cacheKey = rangeParam
	h.cacheVal = resp
	h.cacheAt = time.Now()
	h.cacheMu.Unlock()

	writeJSON(w, http.StatusOK, resp)
}

func (h *dashboardHandler) fetchDashboard(
	ctx context.Context, rc rangeConfig,
) (*dashboardResponse, error) {
	now := time.Now().UTC()
	after := now.Add(-rc.statsWindow)

	// Clean up stale sessions (fire-and-forget, non-critical).
	staleThreshold := now.Add(-1 * time.Hour)
	h.sessionStore.CleanupStaleSessions(ctx, staleThreshold) //nolint:errcheck

	// Run all queries in parallel.
	g, ctx := errgroup.WithContext(ctx)

	var sessions []store.Session
	g.Go(func() error {
		var err error
		sessions, err = h.sessionStore.ListActiveSessions(ctx)
		return err
	})

	var recentCalls []store.AuditRecord
	g.Go(func() error {
		var err error
		recentCalls, _, err = h.auditStore.QueryAuditRecords(ctx, store.AuditFilter{
			After: &after,
			Limit: rc.callsLimit,
		})
		return err
	})

	var errorRecords []store.AuditRecord
	var blockedRecords []store.AuditRecord
	g.Go(func() error {
		errStatus := "error"
		var err error
		errorRecords, _, err = h.auditStore.QueryAuditRecords(ctx, store.AuditFilter{
			Status: &errStatus,
			After:  &after,
			Limit:  rc.errorsLimit,
		})
		return err
	})
	g.Go(func() error {
		blockedStatus := "blocked"
		var err error
		blockedRecords, _, err = h.auditStore.QueryAuditRecords(ctx, store.AuditFilter{
			Status: &blockedStatus,
			After:  &after,
			Limit:  rc.errorsLimit,
		})
		return err
	})

	var stats *store.AuditStats
	g.Go(func() error {
		var err error
		stats, err = h.auditStore.GetAuditStats(ctx, "", after, now)
		return err
	})

	var rawTS []store.TimeSeriesPoint
	g.Go(func() error {
		var err error
		rawTS, err = h.auditStore.GetDashboardTimeSeriesBucketed(ctx, after, now, rc.bucketSec)
		return err
	})

	// Mesh message counts per bucket — feeds the new "mesh" tile sparkline.
	var meshBuckets map[int64]int
	if h.meshStore != nil {
		g.Go(func() error {
			counts, err := h.meshStore.GetMeshMessageCountsBucketed(ctx, after, now, rc.bucketSec)
			if err != nil {
				// Non-critical — leave the chart at zero rather than failing
				// the whole dashboard query.
				return nil
			}
			meshBuckets = counts
			return nil
		})
	}

	// Paired-peer roster — point-in-time counts for the new "peers" tile.
	var peers []store.P2PPeer
	if h.peerStore != nil {
		g.Go(func() error {
			ps, err := h.peerStore.ListPeers(ctx)
			if err != nil {
				return nil // non-critical
			}
			peers = ps
			return nil
		})
	}

	var toolLeaderboard []store.ToolLeaderboardEntry
	g.Go(func() error {
		var err error
		toolLeaderboard, err = h.auditStore.GetToolLeaderboard(ctx, after, now, 10)
		return err
	})

	var serverHealth []store.ServerHealthEntry
	g.Go(func() error {
		var err error
		serverHealth, err = h.auditStore.GetServerHealth(ctx, after, now)
		return err
	})

	var errorBreakdown []store.ErrorBreakdownEntry
	g.Go(func() error {
		var err error
		errorBreakdown, err = h.auditStore.GetErrorBreakdown(ctx, after, now, 10)
		return err
	})

	var routeHitMap []store.RouteHitEntry
	g.Go(func() error {
		var err error
		routeHitMap, err = h.auditStore.GetRouteHitMap(ctx, after, now)
		return err
	})

	var approvalMetrics *store.ApprovalMetrics
	if h.approvalStore != nil {
		g.Go(func() error {
			var err error
			approvalMetrics, err = h.approvalStore.GetApprovalMetrics(ctx, after, now)
			if err != nil {
				approvalMetrics = nil // non-critical
			}
			return nil
		})
	}

	var cacheStats *cacheStatsResponse
	if h.engine != nil {
		g.Go(func() error {
			cs := &cacheStatsResponse{
				RouteResolution: h.engine.RouteStats(),
			}
			auditCache, cacheErr := h.auditStore.GetAuditCacheStats(ctx, after, now)
			if cacheErr == nil && auditCache != nil {
				cs.ToolCall = cache.Stats{
					Hits:    int64(auditCache.Hits),
					Misses:  int64(auditCache.Misses),
					HitRate: auditCache.HitRate,
				}
			}
			cacheStats = cs
			return nil
		})
	}

	var unreviewedDelegations int
	if h.delegationCounter != nil {
		g.Go(func() error {
			n, err := h.delegationCounter.CountUnreviewedRequiredDelegations(ctx)
			if err != nil {
				// Non-critical for dashboard; surface 0 rather than fail the tile.
				unreviewedDelegations = 0
				return nil
			}
			unreviewedDelegations = n
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Merge errors + blocked, sorted by timestamp. Only surface errors from
	// the last 24h — older ones are almost always stale state from previous
	// sessions and cause false-alarm noise on the dashboard.
	const recentWindow = 24 * time.Hour
	cutoff := time.Now().Add(-recentWindow)
	allErrors := append(errorRecords, blockedRecords...)
	fresh := allErrors[:0]
	for _, e := range allErrors {
		if e.Timestamp.After(cutoff) {
			fresh = append(fresh, e)
		}
	}
	allErrors = fresh
	slices.SortFunc(allErrors, func(a, b store.AuditRecord) int {
		return b.Timestamp.Compare(a.Timestamp)
	})
	if len(allErrors) > rc.errorsLimit {
		allErrors = allErrors[:rc.errorsLimit]
	}

	// Ensure non-nil slices.
	if recentCalls == nil {
		recentCalls = []store.AuditRecord{}
	}
	if allErrors == nil {
		allErrors = []store.AuditRecord{}
	}
	if toolLeaderboard == nil {
		toolLeaderboard = []store.ToolLeaderboardEntry{}
	}
	if serverHealth == nil {
		serverHealth = []store.ServerHealthEntry{}
	}
	if errorBreakdown == nil {
		errorBreakdown = []store.ErrorBreakdownEntry{}
	}
	if routeHitMap == nil {
		routeHitMap = []store.RouteHitEntry{}
	}

	timeseries := fillTimeSeriesBucketed(rawTS, after, rc.bucketSec, rc.dataPoints)
	mergeMeshCounts(timeseries, meshBuckets)
	meshTotal := 0
	for _, n := range meshBuckets {
		meshTotal += n
	}

	peersOnline, peersTotal := countPeerStates(peers, time.Now().UTC())

	activeDownstreams := h.buildDownstreamStatus(ctx)

	var timings []downstream.ServerTiming
	if h.manager != nil {
		timings = h.manager.LatestTimings()
	}
	if timings == nil {
		timings = []downstream.ServerTiming{}
	}

	return &dashboardResponse{
		ActiveSessions:        len(sessions),
		ActiveSessionList:     sessions,
		ActiveDownstreams:     activeDownstreams,
		RecentErrors:          allErrors,
		RecentCalls:           recentCalls,
		Stats:                 stats,
		TimeSeries:            timeseries,
		ToolLeaderboard:       toolLeaderboard,
		ServerHealth:          serverHealth,
		ErrorBreakdown:        errorBreakdown,
		RouteHitMap:           routeHitMap,
		ApprovalMetrics:       approvalMetrics,
		CacheStats:            cacheStats,
		PeersOnline:           peersOnline,
		PeersTotal:            peersTotal,
		MeshMessages:          meshTotal,
		ServerTimings:         timings,
		UnreviewedDelegations: unreviewedDelegations,
	}, nil
}

// mergeMeshCounts overlays a per-bucket mesh-message count map onto the
// pre-zero-filled time series. Buckets without a mesh entry stay at zero.
func mergeMeshCounts(ts []store.TimeSeriesPoint, counts map[int64]int) {
	if len(counts) == 0 {
		return
	}
	for i := range ts {
		if n, ok := counts[ts[i].Bucket.Unix()]; ok {
			ts[i].MeshMessages = n
		}
	}
}

// countPeerStates partitions peers into online (last_seen within
// peerOnlineWindow) and total (every paired peer including offline and
// revoked-but-not-yet-deleted). A peer with no LastSeen is treated as
// offline because we have no signal that it is reachable.
func countPeerStates(peers []store.P2PPeer, now time.Time) (online, total int) {
	cutoff := now.Add(-peerOnlineWindow)
	for _, p := range peers {
		total++
		if p.LastSeen != nil && !p.LastSeen.Before(cutoff) {
			online++
		}
	}
	return online, total
}

// fillTimeSeriesBucketed zero-fills missing buckets so the frontend always
// gets exactly `count` data points at the given bucket interval.
func fillTimeSeriesBucketed(raw []store.TimeSeriesPoint, start time.Time, bucketSec, count int) []store.TimeSeriesPoint {
	interval := time.Duration(bucketSec) * time.Second
	start = start.Truncate(interval)

	idx := make(map[int64]store.TimeSeriesPoint, len(raw))
	for _, p := range raw {
		idx[p.Bucket.Truncate(interval).Unix()] = p
	}

	out := make([]store.TimeSeriesPoint, count)
	for i := range count {
		bucket := start.Add(time.Duration(i) * interval)
		if p, ok := idx[bucket.Unix()]; ok {
			out[i] = p
		} else {
			out[i] = store.TimeSeriesPoint{Bucket: bucket}
		}
	}
	return out
}

// buildDownstreamStatus lists all configured downstream servers and overlays
// live instance state from the process manager when available.
func (h *dashboardHandler) buildDownstreamStatus(ctx context.Context) []downstreamStatus {
	servers, err := h.downstreamStore.ListDownstreamServers(ctx)
	if err != nil {
		return []downstreamStatus{}
	}

	type instanceAgg struct {
		count int
		state string
	}
	running := make(map[string]instanceAgg)
	if h.manager != nil {
		for _, info := range h.manager.ListInstances() {
			agg := running[info.Key.ServerID]
			agg.count++
			agg.state = info.State.String()
			running[info.Key.ServerID] = agg
		}
	}

	result := make([]downstreamStatus, 0, len(servers))
	for _, srv := range servers {
		ds := downstreamStatus{
			ServerID:   srv.ID,
			ServerName: srv.Name,
			Disabled:   srv.Disabled,
		}
		if srv.Disabled {
			ds.State = "disabled"
		} else if agg, ok := running[srv.ID]; ok {
			ds.InstanceCount = agg.count
			ds.State = agg.state
		} else if srv.Transport == "http" {
			ds.State = "external"
		} else {
			ds.State = "stopped"
		}
		result = append(result, ds)
	}
	return result
}
