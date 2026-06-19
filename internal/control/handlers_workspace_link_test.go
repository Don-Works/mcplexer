package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestHandleLinkAndListAndUnlinkWorkspace(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "gateway", DefaultPolicy: "deny"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	// A paired peer to link to — the link grants it the task_assign scope.
	if err := db.AddPeer(ctx, &store.P2PPeer{PeerID: "peer-B", DisplayName: "vm"}); err != nil {
		t.Fatalf("add peer: %v", err)
	}

	// Link by workspace NAME (resolveLocalWorkspace handles id-or-name).
	linkArgs := json.RawMessage(`{
		"peer_id": "peer-B",
		"local_workspace": "gateway",
		"remote_workspace_id": "remote-ws-1",
		"remote_workspace_name": "gateway"
	}`)
	res, err := handleLinkWorkspace(ctx, db, linkArgs)
	if err != nil {
		t.Fatalf("handleLinkWorkspace: %v", err)
	}
	text, isErr := parseToolResult(t, res)
	if isErr {
		t.Fatalf("link returned error result: %s", text)
	}
	var linkOut struct {
		Linked           bool   `json:"linked"`
		LocalWorkspaceID string `json:"local_workspace_id"`
	}
	if err := json.Unmarshal([]byte(text), &linkOut); err != nil {
		t.Fatalf("unmarshal link result: %v", err)
	}
	if !linkOut.Linked || linkOut.LocalWorkspaceID != ws.ID {
		t.Fatalf("unexpected link result: %+v (ws.ID=%s)", linkOut, ws.ID)
	}

	// List shows the link with the resolved local name.
	res, err = handleListWorkspaceLinks(ctx, db, nil)
	if err != nil {
		t.Fatalf("handleListWorkspaceLinks: %v", err)
	}
	text, _ = parseToolResult(t, res)
	var links []struct {
		PeerID             string `json:"peer_id"`
		LocalWorkspaceName string `json:"local_workspace_name"`
		RemoteWorkspaceID  string `json:"remote_workspace_id"`
		LinkEstablishedBy  string `json:"link_established_by"`
	}
	if err := json.Unmarshal([]byte(text), &links); err != nil {
		t.Fatalf("unmarshal links: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("got %d links, want 1", len(links))
	}
	if links[0].PeerID != "peer-B" || links[0].LocalWorkspaceName != "gateway" || links[0].LinkEstablishedBy != "local" {
		t.Fatalf("unexpected link row: %+v", links[0])
	}

	// Linked peers for the workspace is the send-side replication gate.
	peers, err := db.ListLinkedPeersForWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("ListLinkedPeersForWorkspace: %v", err)
	}
	if len(peers) != 1 || peers[0] != "peer-B" {
		t.Fatalf("linked peers = %v, want [peer-B]", peers)
	}

	// Linking granted the peer the task_assign scope for its workspace, so
	// its replicated tasks are accepted inbound. This is what makes the
	// feature work end-to-end (the receiver gates on task_assign:<ws>).
	if ok, _ := db.HasPeerScope(ctx, "peer-B", "task_assign:gateway"); !ok {
		t.Fatalf("link did not grant task_assign:gateway to peer-B")
	}
	// Linking also granted task_sync:<local_workspace_id> so the peer's
	// gossip catch-up pulls (/mcplexer/task-sync/1.0.0) pass the
	// server-side workspace scope gate.
	if ok, _ := db.HasPeerScope(ctx, "peer-B", "task_sync:"+ws.ID); !ok {
		t.Fatalf("link did not grant task_sync:%s to peer-B", ws.ID)
	}

	// Unlink removes it.
	unlinkArgs := json.RawMessage(`{"peer_id":"peer-B","remote_workspace_id":"remote-ws-1"}`)
	if _, err := handleUnlinkWorkspace(ctx, db, unlinkArgs); err != nil {
		t.Fatalf("handleUnlinkWorkspace: %v", err)
	}
	// Unlink revoked both scopes too.
	if ok, _ := db.HasPeerScope(ctx, "peer-B", "task_assign:gateway"); ok {
		t.Fatalf("unlink did not revoke task_assign:gateway from peer-B")
	}
	if ok, _ := db.HasPeerScope(ctx, "peer-B", "task_sync:"+ws.ID); ok {
		t.Fatalf("unlink did not revoke task_sync:%s from peer-B", ws.ID)
	}
	res, _ = handleListWorkspaceLinks(ctx, db, nil)
	text, _ = parseToolResult(t, res)
	links = links[:0]
	if err := json.Unmarshal([]byte(text), &links); err != nil {
		t.Fatalf("unmarshal links after unlink: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("got %d links after unlink, want 0", len(links))
	}
}

func TestHandleLinkWorkspaceUnknownLocalWorkspace(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	args := json.RawMessage(`{"peer_id":"peer-B","local_workspace":"nope","remote_workspace_id":"r1"}`)
	res, err := handleLinkWorkspace(ctx, db, args)
	if err == nil {
		// resolveLocalWorkspace returns a Go error (not an error result),
		// so handler returns (nil, err).
		_ = res
		t.Fatalf("expected error for unknown local workspace")
	}
}
