package api

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/install"
	"github.com/don-works/mcplexer/internal/sandbox"
	"github.com/don-works/mcplexer/internal/sanitize"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// guardsHandler exposes the read+toggle surface for the Guards UI. All
// real logic lives in the per-Guard packages (sanitize, scheduler,
// sandbox, install); this handler is a thin HTTP shim over those.
//
// envelopeAlways persists across requests within one daemon run; full
// persistence lands when sanitizer_meta wiring goes in M3.5.
type guardsHandler struct {
	store          store.Store
	approvalMgr    *approval.Manager
	scheduler      *scheduler.Scheduler
	installMgr     *install.Manager
	hookInstaller  *install.HookInstaller
	sanitizer      *sanitize.Denylist
	sandboxInstall *sandbox.Installer
	settingsSvc    *config.SettingsService // persists envelope_always + sandbox toggles
	dsManager      *downstream.Manager     // for hot-swapping sandbox wrapper

	// Drift reconciler state — see shell_drift.go for the read+cache logic.
	// Re-reading settings.json on every shellDetail GET is cheap but the
	// dashboard polls every few seconds, so we throttle per client.
	driftMu     sync.Mutex
	driftLast   map[string]time.Time // clientID → last reconcile time
	driftCached map[string]driftResult
}

// guardsOverview is the response shape for GET /api/v1/guards. Each
// sub-struct mirrors what its dedicated detail endpoint returns at the
// top level, but lighter — overview cards don't need denylist names or
// full job rows.
type guardsOverview struct {
	Shell     shellGuardSummary     `json:"shell"`
	Sanitizer sanitizerGuardSummary `json:"sanitizer"`
	Schedule  scheduleGuardSummary  `json:"schedule"`
	Sandbox   sandboxGuardSummary   `json:"sandbox"`
	MCP       mcpGuardSummary       `json:"mcp"`
}

type shellGuardSummary struct {
	HooksInstalledCount  int `json:"hooks_installed_count"`
	HooksTotalClients    int `json:"hooks_total_clients"`
	RecentDeniedCount24h int `json:"recent_denied_count_24h"`
}

type sanitizerGuardSummary struct {
	DenylistSize     int  `json:"denylist_size"`
	DetectedCount24h int  `json:"detected_count_24h"`
	EnvelopeAlways   bool `json:"envelope_always"`
}

type scheduleGuardSummary struct {
	JobsTotal  int `json:"jobs_total"`
	JobsRan24h int `json:"jobs_ran_24h"`
}

type sandboxGuardSummary struct {
	Driver        string                `json:"driver"`
	Clients       []sandboxClientStatus `json:"clients"`
	UnsupportedOS bool                  `json:"unsupported_os"`
}

type sandboxClientStatus struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type mcpGuardSummary struct {
	DownstreamCount int `json:"downstream_count"`
	RouteCount      int `json:"route_count"`
}

// guardsOverview serves GET /api/v1/guards. Best-effort: failures from
// any one of the data sources degrade that field to its zero value
// rather than 500-ing the entire response — the UI is a status surface
// and partial data is more useful than no data.
func (h *guardsHandler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	out := guardsOverview{}

	out.Shell = h.shellSummary(ctx)
	out.Sanitizer = h.sanitizerSummary(ctx)
	out.Schedule = h.scheduleSummary(ctx)
	out.Sandbox = h.sandboxSummary(ctx)
	out.MCP = h.mcpSummary(ctx)

	writeJSON(w, http.StatusOK, out)
}

func (h *guardsHandler) shellSummary(ctx context.Context) shellGuardSummary {
	s := shellGuardSummary{}
	if h.store != nil {
		clients, err := h.store.ListInstalledClients(ctx)
		if err == nil {
			s.HooksTotalClients = len(clients)
			for _, c := range clients {
				if c.HooksInstalled {
					s.HooksInstalledCount++
				}
			}
		}
		// Recent denied count: last 24h, surface=shell, status=denied.
		// We use the approval metrics API plus an in-memory filter on
		// surface via ListPendingApprovals + DB query. The cheapest
		// path is QueryAuditRecords filtered by status=denied; absent
		// a surface column on audit_records we approximate with the
		// metrics API global denied count and trust the UI to scope.
		after := time.Now().Add(-24 * time.Hour).UTC()
		before := time.Now().UTC()
		m, mErr := h.store.GetApprovalMetrics(ctx, after, before)
		if mErr == nil && m != nil {
			s.RecentDeniedCount24h = m.DeniedCount
		}
	}
	return s
}

func (h *guardsHandler) sanitizerSummary(ctx context.Context) sanitizerGuardSummary {
	s := sanitizerGuardSummary{}
	if h.sanitizer != nil {
		s.DenylistSize = len(h.sanitizer.Names())
	}
	if h.settingsSvc != nil {
		s.EnvelopeAlways = h.settingsSvc.Load(ctx).SanitizerEnvelopeAlways
	}
	// DetectedCount24h: requires sanitizer_meta wiring; left at zero
	// for M1-D and surfaced as "no events yet" in the UI.
	return s
}

func (h *guardsHandler) scheduleSummary(ctx context.Context) scheduleGuardSummary {
	s := scheduleGuardSummary{}
	if h.store == nil {
		return s
	}
	jobs, err := h.store.ListScheduledJobs(ctx)
	if err != nil {
		return s
	}
	s.JobsTotal = len(jobs)
	after := time.Now().Add(-24 * time.Hour)
	for _, j := range jobs {
		if j.LastRunAt != nil && j.LastRunAt.After(after) {
			s.JobsRan24h++
		}
	}
	return s
}

func (h *guardsHandler) sandboxSummary(ctx context.Context) sandboxGuardSummary {
	// Initialise as empty slice (NOT nil) so the JSON encodes as `[]`
	// rather than `null`. The UI calls .filter() / .length on this and
	// crashes on null. Same rule applies to every []T field on the
	// guards response surface.
	s := sandboxGuardSummary{Clients: []sandboxClientStatus{}}
	driver := sandbox.SelectDriver()
	if driver == nil {
		s.UnsupportedOS = runtime.GOOS == "windows"
	} else {
		s.Driver = driver.Name()
	}
	if h.store == nil {
		return s
	}
	clients, err := h.store.ListInstalledClients(ctx)
	if err != nil {
		return s
	}
	for _, c := range clients {
		s.Clients = append(s.Clients, sandboxClientStatus{
			ID: c.ID, Name: c.Name, Enabled: c.SandboxEnabled,
		})
	}
	return s
}

func (h *guardsHandler) mcpSummary(ctx context.Context) mcpGuardSummary {
	s := mcpGuardSummary{}
	if h.store == nil {
		return s
	}
	ds, err := h.store.ListDownstreamServers(ctx)
	if err == nil {
		s.DownstreamCount = len(ds)
	}
	// Empty workspace ID lists rules for all workspaces.
	rules, err := h.store.ListRouteRules(ctx, "")
	if err == nil {
		s.RouteCount = len(rules)
	}
	return s
}

// shellGuardClient flattens install.ClientInfo + the InstalledClient row's
// HooksInstalled bool so the dashboard can show install state without
// having to cross-reference two endpoints. The two flags mean different
// things — `configured` = mcplexer is registered as an MCP server in the
// client's config; `hooks_installed` = the PreToolUse curl shim is wired
// into the client's settings.json. The Install / Uninstall button keys
// off hooks_installed (this is what flips when the user clicks).
//
// HooksDrifted = DB says hooks_installed=true but the reconciler just
// re-read settings.json and the mcplexer endpoint is no longer there
// (rules sync overwrote it, the user edited it, another tool replaced
// the file, etc.). When true, hooks_installed stays true (so the
// uninstall path still works) but the UI flips to a red "repair" badge.
// HooksDriftError carries a human-readable parse-error message when
// the file was unreadable; empty otherwise.
type shellGuardClient struct {
	install.ClientInfo
	HooksInstalled  bool   `json:"hooks_installed"`
	HooksDrifted    bool   `json:"hooks_drifted"`
	HooksDriftError string `json:"hooks_drift_error,omitempty"`
}

// shellDetail serves GET /api/v1/guards/shell.
type shellGuardDetail struct {
	HooksEnabled    bool                 `json:"hooks_enabled"`
	Clients         []shellGuardClient   `json:"clients"`
	RecentApprovals []store.ToolApproval `json:"recent_approvals"`
}

func (h *guardsHandler) shellDetail(w http.ResponseWriter, r *http.Request) {
	out := shellGuardDetail{
		Clients:         []shellGuardClient{},
		RecentApprovals: []store.ToolApproval{},
	}
	var base []install.ClientInfo
	if h.installMgr != nil {
		status, err := h.installMgr.Status()
		if err == nil && status != nil {
			base = status.Clients
		}
	}
	// Build an id -> InstalledClient lookup from the DB (the source of
	// truth for whether the PreToolUse hook is currently wired in the
	// client's settings.json). hooks_enabled stays true when any client
	// reports installed.
	rows := map[string]store.InstalledClient{}
	if h.store != nil {
		clients, err := h.store.ListInstalledClients(r.Context())
		if err == nil {
			for _, c := range clients {
				rows[c.ID] = c
				if c.HooksInstalled {
					out.HooksEnabled = true
				}
			}
		}
	}
	for _, c := range base {
		row, hasRow := rows[string(c.ID)]
		drifted, driftErr := h.reconcileClientDrift(r.Context(), c.ID, row, hasRow)
		out.Clients = append(out.Clients, shellGuardClient{
			ClientInfo:      c,
			HooksInstalled:  row.HooksInstalled,
			HooksDrifted:    drifted,
			HooksDriftError: driftErr,
		})
	}
	// Recent shell-surface approvals: filter the existing approvals
	// store client-side since the SQL surface doesn't expose a Surface
	// filter on listPending today.
	if h.store != nil {
		recent, err := h.store.ListPendingApprovals(r.Context())
		if err == nil {
			for _, a := range recent {
				if a.Surface == "shell" {
					out.RecentApprovals = append(out.RecentApprovals, a)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// shellInstallHooks serves POST /api/v1/guards/shell/clients/{id}/install_hooks.
// Today only claude_code is supported by HookInstaller; other client IDs
// return 400.
func (h *guardsHandler) shellInstallHooks(w http.ResponseWriter, r *http.Request) {
	if h.hookInstaller == nil {
		writeError(w, http.StatusServiceUnavailable, "hook installer not configured")
		return
	}
	clientID := r.PathValue("id")
	if clientID != string(install.ClaudeCode) {
		writeError(w, http.StatusBadRequest, "shell hooks only supported for claude_code today")
		return
	}
	receipt, err := h.hookInstaller.InstallClaudeCodeHooks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installed":  receipt != nil,
		"receipt_id": receiptID(receipt),
	})
}

func (h *guardsHandler) shellUninstallHooks(w http.ResponseWriter, r *http.Request) {
	if h.hookInstaller == nil {
		writeError(w, http.StatusServiceUnavailable, "hook installer not configured")
		return
	}
	clientID := r.PathValue("id")
	if clientID != string(install.ClaudeCode) {
		writeError(w, http.StatusBadRequest, "shell hooks only supported for claude_code today")
		return
	}
	if err := h.hookInstaller.UninstallClaudeCodeHooks(r.Context()); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"uninstalled": true})
}

func receiptID(r *store.InstallReceipt) string {
	if r == nil {
		return ""
	}
	return r.ID
}

// sanitizerDetail serves GET /api/v1/guards/sanitizer.
type sanitizerGuardDetail struct {
	EnvelopeAlways bool     `json:"envelope_always"`
	Denylist       []string `json:"denylist"`
	RecentEvents   []any    `json:"recent_events"`
}

func (h *guardsHandler) sanitizerDetail(w http.ResponseWriter, r *http.Request) {
	out := sanitizerGuardDetail{
		Denylist:     []string{},
		RecentEvents: []any{},
	}
	if h.sanitizer != nil {
		out.Denylist = h.sanitizer.Names()
	}
	if h.settingsSvc != nil {
		out.EnvelopeAlways = h.settingsSvc.Load(r.Context()).SanitizerEnvelopeAlways
	}
	writeJSON(w, http.StatusOK, out)
}

// sanitizerUpdate serves PUT /api/v1/guards/sanitizer. Persists
// envelope_always to the settings table so the gateway's sanitize
// path picks it up on the next tool-result via SettingsService.Load.
// No daemon restart needed — the read path is per-request.
func (h *guardsHandler) sanitizerUpdate(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "settings service not configured")
		return
	}
	var body struct {
		EnvelopeAlways bool `json:"envelope_always"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	current := h.settingsSvc.Load(r.Context())
	current.SanitizerEnvelopeAlways = body.EnvelopeAlways
	if err := h.settingsSvc.Save(r.Context(), current); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.sanitizerDetail(w, r)
}

// sanitizerDenylist serves GET /api/v1/guards/sanitizer/denylist.
func (h *guardsHandler) sanitizerDenylist(w http.ResponseWriter, _ *http.Request) {
	names := []string{}
	if h.sanitizer != nil {
		names = h.sanitizer.Names()
	}
	writeJSON(w, http.StatusOK, map[string][]string{"names": names})
}

// scheduleList serves GET /api/v1/guards/schedule.
func (h *guardsHandler) scheduleList(w http.ResponseWriter, r *http.Request) {
	jobs := []store.ScheduledJob{}
	if h.store != nil {
		got, err := scheduler.ScheduleListHandler(r.Context(), h.store, scheduler.ScheduleListArgs{})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if got.Jobs != nil {
			jobs = got.Jobs
		}
	}
	writeJSON(w, http.StatusOK, map[string][]store.ScheduledJob{"jobs": jobs})
}

// scheduleCreate serves POST /api/v1/guards/schedule.
func (h *guardsHandler) scheduleCreate(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store not configured")
		return
	}
	var args scheduler.ScheduleCreateArgs
	if err := decodeJSON(r, &args); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	res, err := scheduler.ScheduleCreateHandler(r.Context(), h.store, h.scheduler, nil, args)
	if err != nil {
		// validate errors are user-facing; bucket them as 400.
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "invalid spec") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// scheduleRun serves POST /api/v1/guards/schedule/{id}/run.
func (h *guardsHandler) scheduleRun(w http.ResponseWriter, r *http.Request) {
	if h.scheduler == nil {
		writeError(w, http.StatusServiceUnavailable, "scheduler not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing job id")
		return
	}
	if err := h.scheduler.RunOnce(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ran": true})
}

// scheduleDelete serves DELETE /api/v1/guards/schedule/{id}.
func (h *guardsHandler) scheduleDelete(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "store not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing job id")
		return
	}
	_, err := scheduler.ScheduleDeleteHandler(r.Context(), h.store, h.scheduler, nil, scheduler.ScheduleDeleteArgs{ID: id})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

// sandboxDetail serves GET /api/v1/guards/sandbox.
// DownstreamsEnabled + ActiveDescription are the load-bearing fields
// — they reflect whether downstream MCP server spawns are actually
// wrapped in sandbox-exec right now. The per-client list is kept for
// the install_clients display but is informational only; the M2 work
// to make client-scoped sandboxing real is M2.5+.
type sandboxGuardDetail struct {
	Driver             string                `json:"driver"`
	UnsupportedOS      bool                  `json:"unsupported_os"`
	DownstreamsEnabled bool                  `json:"downstreams_enabled"`
	ActiveDescription  string                `json:"active_description"`
	Clients            []sandboxClientStatus `json:"clients"`
}

func (h *guardsHandler) sandboxDetail(w http.ResponseWriter, r *http.Request) {
	out := sandboxGuardDetail{Clients: []sandboxClientStatus{}}
	driver := sandbox.SelectDriver()
	if driver == nil {
		out.UnsupportedOS = runtime.GOOS == "windows"
	} else {
		out.Driver = driver.Name()
	}
	if h.settingsSvc != nil {
		out.DownstreamsEnabled = h.settingsSvc.Load(r.Context()).SandboxDownstreams
	}
	if h.dsManager != nil {
		if wrapper := h.dsManager.SandboxWrapper(); wrapper != nil {
			out.ActiveDescription = wrapper.Describe()
		}
	}
	if out.ActiveDescription == "" {
		out.ActiveDescription = "off"
	}
	if h.store != nil {
		clients, err := h.store.ListInstalledClients(r.Context())
		if err == nil {
			for _, c := range clients {
				out.Clients = append(out.Clients, sandboxClientStatus{
					ID: c.ID, Name: c.Name, Enabled: c.SandboxEnabled,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// sandboxUpdate serves PUT /api/v1/guards/sandbox. Persists the
// SandboxDownstreams toggle and immediately re-installs (or clears)
// the wrapper on downstream.Manager so the change applies to the
// NEXT spawn — existing instances keep their original wrapper for
// the lifetime of the process and pick up the new setting on restart
// (idle-timeout or manual). No daemon restart required.
func (h *guardsHandler) sandboxUpdate(w http.ResponseWriter, r *http.Request) {
	if h.settingsSvc == nil {
		writeError(w, http.StatusServiceUnavailable, "settings service not configured")
		return
	}
	var body struct {
		DownstreamsEnabled bool `json:"downstreams_enabled"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	settings := h.settingsSvc.Load(r.Context())
	settings.SandboxDownstreams = body.DownstreamsEnabled
	if err := h.settingsSvc.Save(r.Context(), settings); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.dsManager != nil {
		if body.DownstreamsEnabled {
			wrapper := sandbox.NewCommandWrapper(sandbox.Config{Network: sandbox.NetworkHost})
			h.dsManager.SetSandboxWrapper(wrapper)
		} else {
			h.dsManager.SetSandboxWrapper(nil)
		}
	}
	h.sandboxDetail(w, r)
}

// sandboxEnable serves POST /api/v1/guards/sandbox/clients/{id}/enable.
func (h *guardsHandler) sandboxEnable(w http.ResponseWriter, r *http.Request) {
	if h.sandboxInstall == nil {
		writeError(w, http.StatusServiceUnavailable, "sandbox installer not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing client id")
		return
	}
	if _, err := h.sandboxInstall.EnableSandbox(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": true})
}

// sandboxDisable serves POST /api/v1/guards/sandbox/clients/{id}/disable.
func (h *guardsHandler) sandboxDisable(w http.ResponseWriter, r *http.Request) {
	if h.sandboxInstall == nil {
		writeError(w, http.StatusServiceUnavailable, "sandbox installer not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing client id")
		return
	}
	if err := h.sandboxInstall.DisableSandbox(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"disabled": true})
}
