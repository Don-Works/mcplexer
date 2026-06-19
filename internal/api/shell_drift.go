package api

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/install"
	"github.com/don-works/mcplexer/internal/store"
)

// driftCheckInterval bounds how often the read-side reconciler reads
// settings.json per client. Picked to be comfortably shorter than the
// dashboard's natural refresh cadence (a few seconds for an open tab)
// so a user manually editing settings.json sees the red badge within
// one refresh of their next dashboard visit.
const driftCheckInterval = 10 * time.Second

// driftResult is the cached outcome of a single settings-file reconcile
// for one client. err is non-empty when the JSON failed to parse — the
// dashboard still shows drifted=true in that case, but the error text
// is propagated so the operator can tell parse-broken from absent.
type driftResult struct {
	drifted bool
	err     string
}

// reconcileClientDrift re-reads the client's underlying settings file
// when the DB says hooks are installed and reports whether the mcplexer
// endpoint substring is still present. Caches per client for
// driftCheckInterval so dashboard polling doesn't repeatedly hit the
// filesystem. Write-through: when the in-DB hooks_drifted flag disagrees
// with the just-observed reality, the row is upserted so a later GET
// against a different code path (e.g. the overview summary) sees the
// same truth.
//
// Returns (drifted, errText). errText is non-empty when settings.json
// failed to parse — in that case drifted is reported true (better to
// surface a red badge with an explanation than to silently lie green).
//
// Today only claude_code is reconciled; other client IDs short-circuit
// to (false, "") because we don't install hooks for them yet.
func (h *guardsHandler) reconcileClientDrift(
	ctx context.Context, clientID install.ClientID,
	row store.InstalledClient, hasRow bool,
) (bool, string) {
	if h.hookInstaller == nil || clientID != install.ClaudeCode ||
		!hasRow || !row.HooksInstalled {
		return false, ""
	}
	id := string(clientID)
	if cached, ok := h.cachedDriftWithinInterval(id); ok {
		return cached.drifted, cached.err
	}
	settingsPath := h.hookInstaller.ClaudeSettingsPath()
	endpoint := h.hookInstaller.Endpoint()
	referenced, err := install.ClaudeSettingsReferencesEndpoint(settingsPath, endpoint)
	res := driftResult{drifted: !referenced}
	if err != nil {
		// Parse error: surface as drifted=true with the error text so
		// the dashboard renders a red badge that explains why.
		res.drifted = true
		res.err = err.Error()
	}
	// Also confirm the session-lifecycle hooks (SessionStart/SessionEnd) are
	// present: the PreToolUse check above can't see stripped session hooks, so
	// a settings.json with PreToolUse intact but the session hooks removed
	// would otherwise read as not-drifted. Drift if either arm is missing.
	if !res.drifted {
		sessionEP := h.hookInstaller.SessionEndpoint()
		sessionRef, serr := install.ClaudeSettingsReferencesSessionEndpoint(settingsPath, sessionEP)
		if serr != nil {
			res.drifted = true
			res.err = serr.Error()
		} else if !sessionRef {
			res.drifted = true
		}
	}
	h.persistDriftIfChanged(ctx, row, res.drifted)
	h.cacheDrift(id, res)
	return res.drifted, res.err
}

// cachedDriftWithinInterval returns the cached drift result for id when
// the last reconcile happened within driftCheckInterval. Mutex held for
// the duration of the read so a parallel writer can't race the cache
// into an inconsistent state.
func (h *guardsHandler) cachedDriftWithinInterval(id string) (driftResult, bool) {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	last, ok := h.driftLast[id]
	if !ok || time.Since(last) > driftCheckInterval {
		return driftResult{}, false
	}
	cached, ok := h.driftCached[id]
	return cached, ok
}

// cacheDrift writes res to the per-client cache and stamps the
// last-checked timestamp. Lazy-init the maps so a zero-value
// guardsHandler stays usable in tests that don't exercise this path.
func (h *guardsHandler) cacheDrift(id string, res driftResult) {
	h.driftMu.Lock()
	defer h.driftMu.Unlock()
	if h.driftLast == nil {
		h.driftLast = map[string]time.Time{}
	}
	if h.driftCached == nil {
		h.driftCached = map[string]driftResult{}
	}
	h.driftLast[id] = time.Now().UTC()
	h.driftCached[id] = res
}

// persistDriftIfChanged write-throughs the drift flag to the DB row when
// it disagrees with what we just observed. Best-effort: a store error
// here doesn't fail the request — the in-memory cache still surfaces the
// correct value to the current caller, and the next request will retry
// the write.
func (h *guardsHandler) persistDriftIfChanged(
	ctx context.Context, row store.InstalledClient, drifted bool,
) {
	if h.store == nil || row.HooksDrifted == drifted {
		return
	}
	updated := row
	updated.HooksDrifted = drifted
	_ = h.store.UpsertInstalledClient(ctx, &updated)
}
