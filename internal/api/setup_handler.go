package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/harnesssync"
	"github.com/don-works/mcplexer/internal/install"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

type harnessSetupHandler struct {
	store         store.Store
	installMgr    *install.Manager
	skillRegistry *skillregistry.Registry
}

type setupStatusResponse struct {
	Harnesses []harnessRow `json:"harnesses"`
}

type harnessRow struct {
	Key                string                       `json:"key"`
	MCPWired           bool                         `json:"mcp_wired"`
	ConfigPath         string                       `json:"config_path"`
	LastInitializeAt   *string                      `json:"last_initialize_at"`
	ClientInfo         *string                      `json:"client_info"`
	BootstrapInstalled bool                         `json:"bootstrap_installed"`
	BootstrapVersion   *int                         `json:"bootstrap_version"`
	RegistryVersion    int                          `json:"registry_version"`
	Drifted            bool                         `json:"drifted"`
	Accretion          *harnesssync.AccretionReport `json:"accretion,omitempty"`
}

type setupInstallRequest struct {
	Harness string `json:"harness"`
}

type setupError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Hint    string `json:"hint,omitempty"`
	} `json:"error"`
}

func (h *harnessSetupHandler) registryVersion(ctx context.Context) int {
	return harnesssync.UsingMcplexerRegistryVersion(ctx, h.skillRegistry)
}

func (h *harnessSetupHandler) status(w http.ResponseWriter, r *http.Request) {
	home, _ := HomeDirForTest()
	regVer := h.registryVersion(r.Context())
	mcpStatus, _ := h.installStatus() // best effort; may be nil mgr

	var rows []harnessRow
	for _, k := range harnesssync.AllKeys() {
		row := harnessRow{Key: string(k), RegistryVersion: regVer}
		// mcp wired + path from install manager if present
		if mcpStatus != nil && harnesssync.ClientIDForMCP(k) != "" {
			for _, c := range mcpStatus.Clients {
				if c.ID == install.ClientID(harnesssync.ClientIDForMCP(k)) {
					row.MCPWired = c.Configured
					row.ConfigPath = c.ConfigPath
					break
				}
			}
		}
		// init + bootstrap from store
		if h.store != nil {
			if hi, err := h.store.GetHarnessInitialization(r.Context(), string(k)); err == nil && hi != nil {
				if hi.LastInitializeAt != nil {
					s := hi.LastInitializeAt.UTC().Format(time.RFC3339)
					row.LastInitializeAt = &s
				}
				if hi.ClientInfo != "" {
					row.ClientInfo = &hi.ClientInfo
				}
				row.BootstrapInstalled = hi.BootstrapInstalled
				row.BootstrapVersion = hi.BootstrapVersion
				if hi.RegistryVersion > 0 {
					row.RegistryVersion = hi.RegistryVersion
				}
				row.Drifted = hi.Drifted
			}
		}
		// if no store row but file present, synthesize basic
		if !row.BootstrapInstalled {
			if st, _ := harnesssync.Recheck(home, harnesssync.HarnessKey(k), regVer); st.BootstrapInstalled {
				row.BootstrapInstalled = true
				row.BootstrapVersion = st.BootstrapVersion
				row.Drifted = st.Drifted
				row.Accretion = st.Accretion
			}
		}
		if row.Accretion == nil {
			if acc := harnesssync.DetectHarnessAccretion(home, k); !acc.Empty() {
				row.Accretion = &acc
			}
		}
		if row.ConfigPath == "" || k == harnesssync.Pi {
			row.ConfigPath = harnesssync.TargetPath(home, k)
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, setupStatusResponse{Harnesses: rows})
}

func (h *harnessSetupHandler) install(w http.ResponseWriter, r *http.Request) {
	var req setupInstallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Harness == "" {
		writeSetupError(w, http.StatusBadRequest, "bad_request", "missing or invalid harness", "body: {\"harness\":\"claude\"}")
		return
	}
	k := harnesssync.HarnessKey(req.Harness)
	if !harnesssync.Valid(k) {
		writeSetupError(w, http.StatusBadRequest, "unknown_harness", "unrecognized harness key: "+req.Harness, validHarnessHint())
		return
	}
	home, _ := HomeDirForTest()
	regVer := h.registryVersion(r.Context())
	_, st, err := harnesssync.Install(home, k, regVer)
	if err != nil {
		writeSetupError(w, http.StatusInternalServerError, "install_failed", err.Error(), "check permissions on target path")
		return
	}
	st.RegistryVersion = regVer
	// record bootstrap receipt in store (best effort)
	if h.store != nil {
		hi := &store.HarnessInitialization{
			Key:                string(k),
			BootstrapInstalled: st.BootstrapInstalled,
			BootstrapVersion:   st.BootstrapVersion,
			BootstrapHash:      harnesssync.RenderedHash(k, regVer),
			RegistryVersion:    regVer,
			Drifted:            false,
		}
		_ = h.store.UpsertHarnessBootstrap(r.Context(), hi)
	}
	row := h.rowFor(k, st, home)
	writeJSON(w, http.StatusOK, row)
}

func (h *harnessSetupHandler) recheck(w http.ResponseWriter, r *http.Request) {
	var req setupInstallRequest
	_ = json.NewDecoder(r.Body).Decode(&req) // optional body
	kStr := req.Harness
	if kStr == "" {
		writeSetupError(w, http.StatusBadRequest, "bad_request", "missing harness", "POST {\"harness\":\"...\"}")
		return
	}
	k := harnesssync.HarnessKey(kStr)
	if !harnesssync.Valid(k) {
		writeSetupError(w, http.StatusBadRequest, "unknown_harness", "unrecognized harness key: "+kStr, validHarnessHint())
		return
	}
	home, _ := HomeDirForTest()
	regVer := h.registryVersion(r.Context())
	st, err := harnesssync.Recheck(home, k, regVer)
	if err != nil {
		writeSetupError(w, http.StatusInternalServerError, "recheck_failed", err.Error(), "")
		return
	}
	st.RegistryVersion = regVer
	if h.store != nil {
		hi := &store.HarnessInitialization{
			Key:                string(k),
			BootstrapInstalled: st.BootstrapInstalled,
			BootstrapVersion:   st.BootstrapVersion,
			BootstrapHash:      harnesssync.RenderedHash(k, regVer),
			RegistryVersion:    regVer,
			Drifted:            st.Drifted,
		}
		_ = h.store.UpsertHarnessBootstrap(r.Context(), hi)
	}
	row := h.rowFor(k, st, home)
	writeJSON(w, http.StatusOK, row)
}

func (h *harnessSetupHandler) rowFor(k harnesssync.HarnessKey, st harnesssync.HarnessStatus, home string) harnessRow {
	row := harnessRow{
		Key:                string(k),
		RegistryVersion:    st.RegistryVersion,
		BootstrapInstalled: st.BootstrapInstalled,
		BootstrapVersion:   st.BootstrapVersion,
		Drifted:            st.Drifted,
		Accretion:          st.Accretion,
	}
	if h.installMgr != nil {
		if m, _ := h.installStatus(); m != nil {
			clientID := harnesssync.ClientIDForMCP(k)
			if clientID != "" {
				for _, c := range m.Clients {
					if c.ID == install.ClientID(clientID) {
						row.MCPWired = c.Configured
						row.ConfigPath = c.ConfigPath
						break
					}
				}
			}
		}
	}
	if row.ConfigPath == "" || k == harnesssync.Pi {
		row.ConfigPath = harnesssync.TargetPath(home, k)
	}
	// last_initialize populated by status path from store; install/recheck
	// responses focus on the bootstrap result (init time is recorded at MCP handshake).
	return row
}

func (h *harnessSetupHandler) installStatus() (*install.StatusResult, error) {
	if h.installMgr == nil {
		return nil, nil
	}
	return h.installMgr.Status()
}

func writeSetupError(w http.ResponseWriter, status int, code, msg, hint string) {
	var e setupError
	e.Error.Code = code
	e.Error.Message = msg
	e.Error.Hint = hint
	writeJSON(w, status, e)
}

func validHarnessHint() string {
	var keys []string
	for _, k := range harnesssync.AllKeys() {
		keys = append(keys, string(k))
	}
	return "valid keys: " + strings.Join(keys, ", ")
}

func homeDir() (string, error) { return os.UserHomeDir() }

// HomeDirForTest exposes homeDir for testing; not for production use.
var HomeDirForTest = homeDir
