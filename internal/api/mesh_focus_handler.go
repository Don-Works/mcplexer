package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// meshFocusHandler implements POST /api/v1/mesh/agents/{session_id}/focus.
//
// Switches the user's local tmux client to a target agent's pane:
//   - origin=local: runs `tmux switch-client -t <session>:<window>.<pane>`
//     against the user's tmux socket (the daemon and the user's shells share
//     a UID-keyed TMPDIR on both macOS launchd and systemd-user, so the
//     default tmux socket Just Works).
//   - origin=peer:<peer_id>: spawns a NEW window in the user's *local* tmux
//     that holds `ssh -t <ssh_target> tmux attach -t S \; select-window -t W
//     \; select-pane -t P`. The new window comes pre-focused on the right
//     remote pane. SSH target is per-peer (p2p_peers.ssh_target); 400 with a
//     friendly message when unset.
//
// Returns 200 with a `{ ok: true, mode: "local"|"remote" }` body so the UI
// can render a short confirmation. Errors come back as 4xx + JSON
// `{ error: "..." }` for the toast.
type meshFocusHandler struct {
	store store.Store
}

type focusResponse struct {
	OK   bool   `json:"ok"`
	Mode string `json:"mode"`
	Msg  string `json:"message,omitempty"`
}

type focusError struct {
	Error string `json:"error"`
}

func (h *meshFocusHandler) focus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sessionID := r.PathValue("session_id")
	if sessionID == "" {
		writeFocusError(w, http.StatusBadRequest, "session_id is required")
		return
	}

	agent, err := h.store.GetMeshAgent(ctx, sessionID)
	if err != nil || agent == nil {
		writeFocusError(w, http.StatusNotFound, "agent not found")
		return
	}

	if agent.TmuxSession == "" {
		writeFocusError(w, http.StatusBadRequest,
			"this agent didn't advertise a tmux pane on registration "+
				"(its process probably wasn't running inside tmux)")
		return
	}

	// Cap the actual tmux exec at 10s — local switch-client returns in ms,
	// remote ssh + tmux can stall on auth / network.
	tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if strings.HasPrefix(agent.Origin, store.MeshAgentOriginPeerPrefix) {
		peerID := strings.TrimPrefix(agent.Origin, store.MeshAgentOriginPeerPrefix)
		peer, err := h.store.GetPeer(ctx, peerID)
		if err != nil || peer == nil {
			writeFocusError(w, http.StatusNotFound,
				"peer record missing — was the device unpaired?")
			return
		}
		if peer.SSHTarget == "" {
			writeFocusError(w, http.StatusBadRequest,
				"this peer has no ssh_target configured — set one on the Paired Devices page first")
			return
		}
		if err := remoteFocus(tctx, agent, peer.SSHTarget); err != nil {
			writeFocusError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeFocusOK(w, "remote", fmt.Sprintf("opened ssh window to %s", peer.SSHTarget))
		return
	}

	if err := localFocus(tctx, agent); err != nil {
		writeFocusError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeFocusOK(w, "local", "")
}

// localFocus runs `tmux switch-client -t S:W.P` against the user's tmux
// socket. Returns a clean error string when tmux is absent / no client is
// attached so the UI can surface why the click did nothing.
func localFocus(ctx context.Context, agent *store.MeshAgent) error {
	target := buildTmuxTarget(agent)
	cmd := exec.CommandContext(ctx, "tmux", "switch-client", "-t", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("tmux is not installed on this host")
		}
		return fmt.Errorf("tmux switch-client: %s", msg)
	}
	return nil
}

// remoteFocus opens a new window in the user's LOCAL tmux that holds an
// SSH session attached to the peer's tmux at the target pane. The command
// chained through tmux's `\;` separator lets one ssh invocation attach +
// select-window + select-pane atomically inside the remote tmux session.
//
// Quoting: the outer shell command (run by `tmux new-window` via sh -c)
// single-quotes the whole tmux-on-the-remote-side. The remote shell sees
// `\;` outside quotes, where backslash escapes the `;` to a literal arg
// that tmux's own parser then treats as a command separator. End-to-end
// safe because tmux session/window/pane names are validated as a-z0-9._-
// by tmux itself; we still shell-escape them defensively here.
func remoteFocus(ctx context.Context, agent *store.MeshAgent, sshTarget string) error {
	// Each `;` is literal at this point and survives both the local and
	// the remote shell to land as a tmux command-chain separator.
	remoteTmux := fmt.Sprintf(`tmux attach -t %s \; select-window -t %s \; select-pane -t %s`,
		shellQuoteSingle(agent.TmuxSession),
		shellQuoteSingle(agent.TmuxWindow),
		shellQuoteSingle(agent.TmuxPane),
	)
	// The whole right-hand side of ssh-target is one shell-token to ssh
	// — wrap in single quotes so the local shell hands it through intact.
	cmdline := fmt.Sprintf(`ssh -t %s '%s'`,
		shellQuoteSingle(sshTarget),
		remoteTmux,
	)

	windowName := "focus-" + shortPeerSuffix(strings.TrimPrefix(agent.Origin, store.MeshAgentOriginPeerPrefix))

	cmd := exec.CommandContext(ctx, "tmux", "new-window", "-n", windowName, cmdline)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("tmux is not installed on this host")
		}
		return fmt.Errorf("tmux new-window: %s", msg)
	}
	return nil
}

func buildTmuxTarget(a *store.MeshAgent) string {
	switch {
	case a.TmuxWindow != "" && a.TmuxPane != "":
		return fmt.Sprintf("%s:%s.%s", a.TmuxSession, a.TmuxWindow, a.TmuxPane)
	case a.TmuxWindow != "":
		return fmt.Sprintf("%s:%s", a.TmuxSession, a.TmuxWindow)
	default:
		return a.TmuxSession
	}
}

// shellQuoteSingle wraps s in single quotes, escaping any inner ones via
// the standard '\” trick. Defends against a malicious peer crafting a
// tmux pane name with `; rm -rf` in it.
func shellQuoteSingle(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// shortPeerSuffix mirrors the helper in handler_mesh_agents.go; kept
// local to avoid an import cycle and to stay tight to the API layer's
// label needs.
func shortPeerSuffix(peerID string) string {
	if len(peerID) <= 10 {
		return peerID
	}
	return peerID[len(peerID)-10:]
}

func writeFocusOK(w http.ResponseWriter, mode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(focusResponse{OK: true, Mode: mode, Msg: msg})
}

func writeFocusError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(focusError{Error: msg})
}
