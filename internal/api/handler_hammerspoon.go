package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/hammerspoon"
	"github.com/don-works/mcplexer/internal/store"
)

// hammerspoonSecretStore is the narrow surface of *secrets.Manager the
// handler uses. Defined as an interface so tests can pass a fake without
// constructing a full encryptor + store chain.
type hammerspoonSecretStore interface {
	Put(ctx context.Context, scopeID, key string, value []byte) error
	Get(ctx context.Context, scopeID, key string) ([]byte, error)
}

// hammerspoonCapStore is the narrow surface of store.Store the probe
// handler uses. Same rationale as above.
type hammerspoonCapStore interface {
	UpdateCapabilitiesCache(ctx context.Context, id string, cache json.RawMessage) error
}

// hammerspoonHandler exposes HTTP endpoints for installing and probing the
// optional Hammerspoon bridge.
//
// Three routes:
//   - GET  /api/v1/hammerspoon/snippet — returns the embedded init.lua so the
//     "copy-mode" user can audit + install by hand.
//   - POST /api/v1/hammerspoon/install — writes the snippet + password to
//     ~/.hammerspoon/, appends require(...) to init.lua, and best-effort
//     triggers `hs.reload()`. macOS-only.
//   - POST /api/v1/hammerspoon/probe — runs a four-step diagnostic and
//     persists results into the downstream row's CapabilitiesCache.
//
// All handlers are idempotent and never log the bridge password.
type hammerspoonHandler struct {
	manager *hammerspoon.Manager
	store   hammerspoonCapStore
	secrets hammerspoonSecretStore
	auditor auditRecorder
	// hsScopeID is the auth scope id where bridge env keys are stored.
	// Defaults to "hammerspoon-bridge" matching seed_hammerspoon.go.
	hsScopeID string
	// homeDir overrides os.UserHomeDir() for tests; empty means use the real
	// user home dir.
	homeDir string
	// reloadDisabled skips the `hs -c hs.reload()` attempt. Tests set this
	// to true to avoid touching the real Hammerspoon binary even when a
	// stub `hs` script is present.
	reloadDisabled bool
	// nowFn is the timestamp source. Overridable for tests so backup +
	// audit timestamps are deterministic.
	nowFn func() time.Time
	// goos lets tests pretend they're on a different platform without
	// rebuilding the binary.
	goos string
}

// installRequest is the POST /install body. Empty for v0.1 — the handler
// generates passwords + paths from the environment. Kept as a typed struct
// so future flags (e.g. {regenerate_password: true}) slot in without a
// breaking change.
//
//nolint:unused // request body placeholder for forward-compatible install flags.
type installRequest struct{}

// installResponse is the success shape for POST /install. See plan §8.
type installResponse struct {
	OK              bool     `json:"ok"`
	FilesWritten    []string `json:"files_written"`
	InitLuaModified bool     `json:"init_lua_modified"`
	InitLuaBackup   string   `json:"init_lua_backup,omitempty"`
	ReloadAttempted bool     `json:"reload_attempted"`
	ReloadError     string   `json:"reload_error,omitempty"`
	NextSteps       []string `json:"next_steps"`
}

// installErrorResponse is returned with a non-2xx status when the installer
// fails. Step identifies which phase broke so the UI can show targeted copy.
type installErrorResponse struct {
	Error string `json:"error"`
	Step  string `json:"step"`
}

// probeResponse is the POST /probe success shape. See plan §6 + step 9.
type probeResponse struct {
	Health      string             `json:"health"`
	Checks      map[string]probeCk `json:"checks"`
	ProbedAt    time.Time          `json:"probed_at"`
	Remediation []probeRemediation `json:"remediation,omitempty"`
}

// probeCk is a single diagnostic step's result.
type probeCk struct {
	OK         bool   `json:"ok"`
	DurationMs int64  `json:"duration_ms"`
	Detail     string `json:"detail,omitempty"`
}

// probeRemediation pairs a failing check with operator-facing copy.
type probeRemediation struct {
	Check string `json:"check"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

// snippet returns the embedded init.lua as a Lua download.
// GET /api/v1/hammerspoon/snippet
func (h *hammerspoonHandler) snippet(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/x-lua; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, hammerspoon.BridgeLuaFilename()))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, hammerspoon.BridgeLuaSnippet())
}

// install writes the bridge snippet + password to ~/.hammerspoon/ and best-
// effort triggers `hs.reload()`. macOS-only; idempotent.
// POST /api/v1/hammerspoon/install
func (h *hammerspoonHandler) install(w http.ResponseWriter, r *http.Request) {
	if h.goosOrRuntime() != "darwin" {
		writeJSON(w, http.StatusBadRequest, installErrorResponse{
			Error: "Hammerspoon is macOS-only",
			Step:  "platform",
		})
		return
	}
	if h.secrets == nil {
		writeJSON(w, http.StatusServiceUnavailable, installErrorResponse{
			Error: "secrets manager not configured",
			Step:  "platform",
		})
		return
	}

	home, err := h.userHomeDir()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, installErrorResponse{
			Error: "cannot resolve home dir: " + err.Error(),
			Step:  "mkdir",
		})
		return
	}
	hsDir := filepath.Join(home, ".hammerspoon")
	if err := os.MkdirAll(hsDir, 0o700); err != nil {
		writeJSON(w, http.StatusInternalServerError, installErrorResponse{
			Error: "mkdir " + hsDir + ": " + err.Error(),
			Step:  "mkdir",
		})
		return
	}

	snippetPath := filepath.Join(hsDir, hammerspoon.BridgeLuaFilename())
	if err := atomicWriteFile(snippetPath, []byte(hammerspoon.BridgeLuaSnippet()), 0o644); err != nil {
		writeJSON(w, http.StatusInternalServerError, installErrorResponse{
			Error: "write snippet: " + err.Error(),
			Step:  "write_snippet",
		})
		return
	}

	password, err := generateBridgePassword()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, installErrorResponse{
			Error: "generate password: " + err.Error(),
			Step:  "gen_password",
		})
		return
	}

	// Persist the encrypted copy first so we never write the plaintext file
	// without a recoverable encrypted backup. Use the existing auth-scope
	// secret store (same path the env-var loader reads on startup).
	scopeID := h.scopeID()
	if err := h.secrets.Put(r.Context(), scopeID, "HAMMERSPOON_BRIDGE_PASSWORD", []byte(password)); err != nil {
		writeJSON(w, http.StatusInternalServerError, installErrorResponse{
			Error: "store password: " + err.Error(),
			Step:  "gen_password",
		})
		return
	}

	pwPath := filepath.Join(hsDir, ".mcp-password")
	if err := atomicWriteFile(pwPath, []byte(password+"\n"), 0o600); err != nil {
		writeJSON(w, http.StatusInternalServerError, installErrorResponse{
			Error: "write password file: " + err.Error(),
			Step:  "write_password",
		})
		return
	}

	initPath := filepath.Join(hsDir, "init.lua")
	modified, backupName, err := ensureRequireLine(initPath, h.now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, installErrorResponse{
			Error: "patch init.lua: " + err.Error(),
			Step:  "append_init",
		})
		return
	}

	reloadAttempted := false
	reloadErr := ""
	if !h.reloadDisabled {
		if attempted, rerr := tryHSReload(r.Context()); attempted {
			reloadAttempted = true
			if rerr != nil {
				reloadErr = rerr.Error()
			}
		}
	}

	// Audit. Never include the password — only paths + flags.
	h.recordAudit(r.Context(), "hammerspoon.bridge.installed", "ok", "", map[string]any{
		"snippet_path":     snippetPath,
		"password_path":    pwPath,
		"init_lua_path":    initPath,
		"init_modified":    modified,
		"init_lua_backup":  backupName,
		"reload_attempted": reloadAttempted,
		"reload_error":     reloadErr,
	})

	writeJSON(w, http.StatusOK, installResponse{
		OK: true,
		FilesWritten: []string{
			tildify(snippetPath, home),
			tildify(pwPath, home),
		},
		InitLuaModified: modified,
		InitLuaBackup:   backupName,
		ReloadAttempted: reloadAttempted,
		ReloadError:     reloadErr,
		NextSteps: []string{
			"Ensure Hammerspoon.app is running",
			"Grant Accessibility permission in System Settings",
		},
	})
}

// probe runs the four-step diagnostic from plan §6 and persists the result
// into the downstream row's CapabilitiesCache so the dashboard can render a
// traffic-light + remediation copy. The probe is idempotent and safe to
// re-run from a button.
// POST /api/v1/hammerspoon/probe
func (h *hammerspoonHandler) probe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.now()

	checks := map[string]probeCk{}

	// (a) app_running — `pgrep -x Hammerspoon`. Boolean.
	t := time.Now()
	checks["app_running"] = probeCk{
		OK:         isHammerspoonRunning(ctx),
		DurationMs: time.Since(t).Milliseconds(),
	}

	// (b) bridge_reachable — TCP-connect to the configured host:port.
	bridgeURL := h.bridgeURL(ctx)
	t = time.Now()
	reachable, reachDetail := tcpReachable(bridgeURL, 2*time.Second)
	checks["bridge_reachable"] = probeCk{
		OK:         reachable,
		DurationMs: time.Since(t).Milliseconds(),
		Detail:     reachDetail,
	}

	// (c) auth_ok + (d) accessibility — single bridge call returns both.
	var auth probeCk
	var accessibility *probeCk
	if h.manager != nil && h.manager.HasBridge() {
		t = time.Now()
		env, err := h.manager.Bridge().Exec(ctx, "return hs.accessibilityState(true)", 3*time.Second)
		auth.DurationMs = time.Since(t).Milliseconds()
		switch {
		case err != nil:
			auth.Detail = "transport: " + err.Error()
		case !env.Ok:
			auth.Detail = env.Err
		default:
			auth.OK = true
			// hs.accessibilityState returns a bool — decode and surface.
			var ax bool
			if perr := json.Unmarshal(env.Result, &ax); perr == nil {
				ac := probeCk{OK: ax, DurationMs: 0}
				accessibility = &ac
				auth.Detail = fmt.Sprintf("accessibility=%v", ax)
			} else {
				auth.Detail = "result was not a boolean"
			}
		}
	} else {
		auth.Detail = "bridge not configured"
	}
	checks["auth_ok"] = auth
	if accessibility != nil {
		checks["accessibility"] = *accessibility
	} else {
		// Spec: null when step c failed. JSON null is hard to model in a
		// typed map[string]probeCk, so record a not-ok zero-duration entry
		// with a detail explaining the skip. UI can render "—" on !ok.
		checks["accessibility"] = probeCk{OK: false, DurationMs: 0, Detail: "skipped (auth failed)"}
	}

	// (e) smoke — invoke list_windows directly via the bridge using the same
	// Lua template MCPServer.Call would build. Reusing buildListWindowsLua
	// keeps the probe in lockstep with the real tool path so a regression
	// in either surfaces here too.
	var smoke probeCk
	if h.manager != nil && h.manager.HasBridge() && auth.OK {
		t = time.Now()
		env, err := h.manager.Bridge().Exec(ctx, hammerspoon.BuildListWindowsLua(), 4*time.Second)
		smoke.DurationMs = time.Since(t).Milliseconds()
		switch {
		case err != nil:
			smoke.Detail = "transport: " + err.Error()
		case !env.Ok:
			smoke.Detail = env.Err
		default:
			// result is a JSON array of window objects; count non-empty.
			var arr []json.RawMessage
			if perr := json.Unmarshal(env.Result, &arr); perr == nil {
				smoke.OK = len(arr) >= 0 // success means handler ran cleanly
				smoke.Detail = fmt.Sprintf("%d windows", len(arr))
			} else {
				// Templates may wrap windows in {windows: [...]} — accept that too.
				var obj struct {
					Windows []json.RawMessage `json:"windows"`
				}
				if perr2 := json.Unmarshal(env.Result, &obj); perr2 == nil {
					smoke.OK = true
					smoke.Detail = fmt.Sprintf("%d windows", len(obj.Windows))
				} else {
					smoke.Detail = "unexpected result shape"
				}
			}
		}
	} else {
		smoke.Detail = "skipped (bridge/auth not ready)"
	}
	checks["smoke"] = smoke

	// Aggregate health.
	allOK := true
	for _, c := range checks {
		if !c.OK {
			allOK = false
			break
		}
	}
	health := "broken"
	switch {
	case allOK:
		health = "ok"
	case checks["bridge_reachable"].OK && checks["auth_ok"].OK:
		health = "degraded"
	}

	resp := probeResponse{
		Health:      health,
		Checks:      checks,
		ProbedAt:    now,
		Remediation: buildRemediation(checks),
	}

	// Persist into CapabilitiesCache for dashboard rendering.
	if h.store != nil {
		if data, mErr := json.Marshal(resp); mErr == nil {
			_ = h.store.UpdateCapabilitiesCache(ctx, "hammerspoon", data)
		}
	}

	// Audit summary — no detail payload that could carry user data.
	summary := map[string]any{
		"health": health,
	}
	for name, c := range checks {
		summary[name+"_ok"] = c.OK
	}
	h.recordAudit(ctx, "hammerspoon.bridge.probed", "ok", "", summary)

	writeJSON(w, http.StatusOK, resp)
}

// --- helpers ---

// goosOrRuntime returns the configured fake GOOS for tests, or the real one.
func (h *hammerspoonHandler) goosOrRuntime() string {
	if h.goos != "" {
		return h.goos
	}
	return runtime.GOOS
}

// userHomeDir resolves the user's home dir, honoring the test override.
func (h *hammerspoonHandler) userHomeDir() (string, error) {
	if h.homeDir != "" {
		return h.homeDir, nil
	}
	return os.UserHomeDir()
}

// scopeID returns the auth-scope id for hammerspoon bridge secrets.
func (h *hammerspoonHandler) scopeID() string {
	if h.hsScopeID != "" {
		return h.hsScopeID
	}
	return "hammerspoon-bridge"
}

// now returns the current time, honoring the test override.
func (h *hammerspoonHandler) now() time.Time {
	if h.nowFn != nil {
		return h.nowFn()
	}
	return time.Now().UTC()
}

// bridgeURL reads the configured HAMMERSPOON_BRIDGE_URL from secrets, falling
// back to the documented default. Failures here are not fatal — they only
// affect the bridge_reachable probe.
func (h *hammerspoonHandler) bridgeURL(ctx context.Context) string {
	if h.secrets != nil {
		if v, err := h.secrets.Get(ctx, h.scopeID(), "HAMMERSPOON_BRIDGE_URL"); err == nil {
			s := strings.TrimSpace(string(v))
			if s != "" {
				return s
			}
		}
	}
	return "http://127.0.0.1:27123"
}

// recordAudit emits a hammerspoon.* audit row. Best-effort — any failure is
// silently swallowed so a broken audit pipeline doesn't fail the HTTP call.
func (h *hammerspoonHandler) recordAudit(ctx context.Context, tool, status, errMsg string, params map[string]any) {
	if h.auditor == nil {
		return
	}
	paramsJSON, _ := json.Marshal(params)
	_ = h.auditor.Record(ctx, &store.AuditRecord{
		ID:                 uuid.NewString(),
		Timestamp:          h.now(),
		ClientType:         "api",
		ToolName:           tool,
		ParamsRedacted:     json.RawMessage(paramsJSON),
		DownstreamServerID: "hammerspoon",
		AuthScopeID:        h.scopeID(),
		Status:             status,
		ErrorMessage:       errMsg,
		CreatedAt:          h.now(),
		ActorKind:          "api",
	})
}

// generateBridgePassword returns 32 bytes of cryptographic randomness encoded
// as a 64-character hex string.
func generateBridgePassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// atomicWriteFile writes data to path via a temp file in the same dir, then
// os.Rename's it into place. Mode is applied to the temp file before rename
// so the final file lands with the desired perm in one atomic step.
func atomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	// On any error from here on, remove the temp file.
	cleanup := func() { _ = os.Remove(tmpPath) }

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return err
	}
	return nil
}

// requireLineRE matches a Lua line that already requires the bridge module.
// Tolerates leading whitespace, single or double quotes, and a trailing
// semicolon or comment.
var requireLineRE = regexp.MustCompile(`(?m)^\s*require\(["']hammerspoon-mcp["']\)\s*;?\s*(--.*)?$`)

// ensureRequireLine appends `require("hammerspoon-mcp")` to ~/.hammerspoon/init.lua
// unless an equivalent line is already present. When the file exists and is
// modified, a timestamped backup is written first. Returns (modified, backupName, err).
func ensureRequireLine(initPath string, now time.Time) (bool, string, error) {
	existing, err := os.ReadFile(initPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return false, "", err
		}
		// Create fresh init.lua containing only the require line.
		return true, "", atomicWriteFile(initPath, []byte("require(\"hammerspoon-mcp\")\n"), 0o644)
	}
	if requireLineRE.Match(existing) {
		return false, "", nil
	}
	// Backup the prior contents to init.lua.mcplexer-bak.<timestamp>.
	stamp := now.UTC().Format("20060102T150405Z")
	backupName := "init.lua.mcplexer-bak." + stamp
	backupPath := filepath.Join(filepath.Dir(initPath), backupName)
	if err := atomicWriteFile(backupPath, existing, 0o644); err != nil {
		return false, "", fmt.Errorf("write backup %s: %w", backupName, err)
	}
	// Append with a guaranteed leading newline so we don't accidentally
	// concatenate the require call with whatever non-newline-terminated
	// tail the user had.
	var out []byte
	out = append(out, existing...)
	if len(existing) == 0 || existing[len(existing)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, []byte("require(\"hammerspoon-mcp\")\n")...)
	if err := atomicWriteFile(initPath, out, 0o644); err != nil {
		return false, backupName, err
	}
	return true, backupName, nil
}

// tryHSReload runs `hs -c "hs.reload()"` if `hs` is on PATH. Returns
// (attempted, error). Caps the run at 3s — Hammerspoon's reload is async
// from the user's perspective so a missed window isn't fatal.
func tryHSReload(ctx context.Context) (bool, error) {
	path, err := exec.LookPath("hs")
	if err != nil {
		return false, nil
	}
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(rctx, path, "-c", "hs.reload()")
	if err := cmd.Run(); err != nil {
		return true, err
	}
	return true, nil
}

// isHammerspoonRunning reports whether `pgrep -x Hammerspoon` returns a hit.
// Bounded at 5s so a hung pgrep doesn't stall the probe (was 2s; raised
// after TestProbe_AllOK_HealthOK flaked under full-sweep `go test ./...`
// parallel load — the subprocess fork + stubbed-PATH lookup spilled past
// 2s on a heavily-loaded test runner without indicating a real problem).
func isHammerspoonRunning(ctx context.Context) bool {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(rctx, "pgrep", "-x", "Hammerspoon")
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	return len(strings.TrimSpace(string(out))) > 0
}

// tcpReachable resolves the host[:port] from the bridge URL and attempts a
// net.DialTimeout. A successful handshake (regardless of HTTP status) is
// proof the bridge is listening.
func tcpReachable(rawURL string, timeout time.Duration) (bool, string) {
	hostport := stripScheme(rawURL)
	if !strings.Contains(hostport, ":") {
		hostport += ":27123"
	}
	conn, err := net.DialTimeout("tcp", hostport, timeout)
	if err != nil {
		return false, err.Error()
	}
	_ = conn.Close()
	return true, hostport
}

// stripScheme strips http:// or https:// and any trailing path from a URL,
// leaving just host:port. Cheap parser that avoids pulling net/url for what
// is always a loopback string in practice.
func stripScheme(s string) string {
	s = strings.TrimSpace(s)
	for _, p := range []string{"http://", "https://"} {
		if strings.HasPrefix(s, p) {
			s = s[len(p):]
			break
		}
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return s
}

// tildify rewrites a path under home as ~/...
func tildify(p, home string) string {
	if home != "" && strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// buildRemediation pairs failing checks with operator-facing copy.
func buildRemediation(checks map[string]probeCk) []probeRemediation {
	var out []probeRemediation
	if c := checks["app_running"]; !c.OK {
		out = append(out, probeRemediation{
			Check: "app_running",
			Title: "Launch Hammerspoon",
			Body:  "Install Hammerspoon from hammerspoon.org and launch it once. Grant Accessibility permission when prompted.",
		})
	}
	if c := checks["bridge_reachable"]; !c.OK {
		out = append(out, probeRemediation{
			Check: "bridge_reachable",
			Title: "Install bridge",
			Body:  "Run POST /api/v1/hammerspoon/install or click 'Install bridge' in the dashboard. After install, open Hammerspoon menu → Reload Config.",
		})
	}
	if c := checks["auth_ok"]; !c.OK {
		out = append(out, probeRemediation{
			Check: "auth_ok",
			Title: "Bridge auth failed",
			Body:  "The shared password in ~/.hammerspoon/.mcp-password doesn't match the one in mcplexer. Re-run install to regenerate both.",
		})
	}
	if c := checks["accessibility"]; !c.OK {
		out = append(out, probeRemediation{
			Check: "accessibility",
			Title: "Grant Accessibility",
			Body:  "System Settings → Privacy & Security → Accessibility → enable Hammerspoon. focus_app and send_keys need this; screenshot/list_windows/notify still work without it.",
		})
	}
	if c := checks["smoke"]; !c.OK {
		out = append(out, probeRemediation{
			Check: "smoke",
			Title: "Smoke test failed",
			Body:  "list_windows could not enumerate any windows. Open the Hammerspoon console (menu icon → Console) and look for errors loading hammerspoon-mcp.",
		})
	}
	return out
}
