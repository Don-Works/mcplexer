package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type meshHandler struct {
	store store.Store
}

type meshStatusResponse struct {
	Agents       []meshAgentView   `json:"agents"`
	Messages     []meshMessageView `json:"messages"`
	LiveMessages int               `json:"live_messages"`
}

type meshAgentView struct {
	SessionID  string `json:"session_id"`
	Name       string `json:"name"`
	Role       string `json:"role"`
	ClientType string `json:"client_type"`
	ModelHint  string `json:"model_hint"`
	// Origin is "local" for agents on the stdio MCP socket and
	// "peer:<peer_id>" for agents observed via libp2p. The UI uses this
	// to distinguish socket-attached Claude Code/Codex sessions from
	// remote paired-machine agents.
	Origin string `json:"origin"`
	// Status is the free-form persistent string the agent advertised
	// via mesh__set_agent_status. omitempty so older API consumers see
	// no diff when no status is set.
	Status string `json:"status,omitempty"`
	// WorkspaceID + WorkspaceName let the UI render a workspace badge
	// per agent. Name is resolved server-side from the workspaces table;
	// for peer-origin agents (where the local store has no row for the
	// remote workspace id), the name falls back to "".
	WorkspaceID   string `json:"workspace_id,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	// Tmux locator — UI greys the "Focus" button when all three are
	// empty (agent wasn't inside tmux when it registered).
	TmuxSession string `json:"tmux_session,omitempty"`
	TmuxWindow  string `json:"tmux_window,omitempty"`
	TmuxPane    string `json:"tmux_pane,omitempty"`
	LastSeenAt  string `json:"last_seen_at"`
}

type meshMessageView struct {
	ID            string `json:"id"`
	AgentName     string `json:"agent_name"`
	Kind          string `json:"kind"`
	Priority      string `json:"priority"`
	Content       string `json:"content"`
	Audience      string `json:"audience"`
	Tags          string `json:"tags"`
	ReplyTo       string `json:"reply_to"`
	ThreadRoot    string `json:"thread_root"`
	ReplyCount    int    `json:"reply_count"`
	ExpiresAt     string `json:"expires_at"`
	CreatedAt     string `json:"created_at"`
	Repo          string `json:"repo,omitempty"`
	Branch        string `json:"branch,omitempty"`
	WorkspacePath string `json:"workspace_path,omitempty"`
	// WorkspaceName is derived from workspace_path (last path component)
	// so the UI can render a short workspace badge without doing client-
	// side path parsing. For messages whose workspace_path matches a row
	// in the workspaces table, the friendly name there wins.
	WorkspaceName string `json:"workspace_name,omitempty"`
}

func (h *meshHandler) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	activeSince := time.Now().UTC().Add(-30 * time.Minute)

	// Get agents across all workspaces by querying with empty workspace ID.
	agents, err := h.store.ListActiveMeshAgents(ctx, "", activeSince)
	if err != nil {
		// Fall back to empty list if query with empty workspace fails.
		agents = nil
	}

	// Try to get agents for all workspaces if empty string didn't work.
	if agents == nil {
		agents = []store.MeshAgent{}
	}

	sinceTime := time.Now().UTC().Add(-2 * time.Hour)
	q := r.URL.Query()
	msgs, err := h.store.QueryMeshMessages(ctx, store.MeshMessageFilter{
		SinceTime:     &sinceTime,
		StatusLive:    true,
		Limit:         50,
		Repo:          q.Get("repo"),
		Branch:        q.Get("branch"),
		WorkspacePath: q.Get("workspace_path"),
	})
	if err != nil {
		msgs = []store.MeshMessage{}
	}

	resp := meshStatusResponse{
		Agents:   make([]meshAgentView, 0, len(agents)),
		Messages: make([]meshMessageView, 0, len(msgs)),
	}

	// Resolve workspace names once for the whole page render rather than
	// per-row — keeps the status endpoint cheap when many agents share a
	// workspace. wsByID maps workspace_id → friendly name; wsByPath maps
	// root_path → friendly name (used for messages, which carry path not
	// id). Both are best-effort: lookup failures fall back to empty so
	// the UI shows a path-derived label.
	wsByID, wsByPath := h.resolveWorkspaceNames(ctx)

	for _, a := range agents {
		origin := a.Origin
		if origin == "" {
			origin = store.MeshAgentOriginLocal
		}
		resp.Agents = append(resp.Agents, meshAgentView{
			SessionID:     a.SessionID,
			Name:          a.Name,
			Role:          a.Role,
			ClientType:    a.ClientType,
			ModelHint:     a.ModelHint,
			Origin:        origin,
			Status:        a.Status,
			WorkspaceID:   a.WorkspaceID,
			WorkspaceName: wsByID[a.WorkspaceID],
			TmuxSession:   a.TmuxSession,
			TmuxWindow:    a.TmuxWindow,
			TmuxPane:      a.TmuxPane,
			LastSeenAt:    a.LastSeenAt.Format(time.RFC3339),
		})
	}

	for _, m := range msgs {
		// Friendly workspace label: try the workspaces table by root_path,
		// fall back to the last path component, fall back to empty.
		wsName := wsByPath[m.WorkspacePath]
		if wsName == "" && m.WorkspacePath != "" {
			wsName = filepath.Base(m.WorkspacePath)
		}
		resp.Messages = append(resp.Messages, meshMessageView{
			ID:            m.ID,
			AgentName:     m.AgentName,
			Kind:          m.Kind,
			Priority:      m.Priority,
			Content:       m.Content,
			Audience:      m.Audience,
			Tags:          m.Tags,
			ReplyTo:       m.ReplyTo,
			ThreadRoot:    m.ThreadRoot,
			ReplyCount:    m.ReplyCount,
			ExpiresAt:     m.ExpiresAt.Format(time.RFC3339),
			CreatedAt:     m.CreatedAt.Format(time.RFC3339),
			Repo:          m.Repo,
			Branch:        m.Branch,
			WorkspacePath: m.WorkspacePath,
			WorkspaceName: wsName,
		})
	}

	resp.LiveMessages = len(msgs)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// resolveWorkspaceNames builds id→name + path→name lookup maps once per
// status call. Best-effort: when the workspaces table is unavailable
// we return empty maps so the rest of the response still renders.
func (h *meshHandler) resolveWorkspaceNames(ctx context.Context) (map[string]string, map[string]string) {
	byID := map[string]string{}
	byPath := map[string]string{}
	wss, err := h.store.ListWorkspaces(ctx)
	if err != nil {
		return byID, byPath
	}
	for _, w := range wss {
		if w.Name != "" {
			byID[w.ID] = w.Name
			if w.RootPath != "" {
				byPath[w.RootPath] = w.Name
			}
		}
	}
	return byID, byPath
}
