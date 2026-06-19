// workspace_link_test.go — coverage for the linked-workspace columns on
// workspace_peer_bindings (migration 088): SetWorkspaceLink,
// ClearWorkspaceLink, ListWorkspaceLinks, ListLinkedPeersForWorkspace,
// and the invariant that a plain offer-routing upsert never clobbers an
// established link.
package sqlite

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSetAndListWorkspaceLink(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	local := seedWorkspace(t, d, "gateway")

	if err := d.SetWorkspaceLink(ctx, &store.WorkspacePeerBinding{
		PeerID:              "peer-B",
		RemoteWorkspaceID:   "remote-ws-1",
		LocalWorkspaceID:    local,
		RemoteWorkspaceName: "gateway",
	}, "local"); err != nil {
		t.Fatalf("SetWorkspaceLink: %v", err)
	}

	got, err := d.GetWorkspacePeerBinding(ctx, "peer-B", "remote-ws-1")
	if err != nil {
		t.Fatalf("GetWorkspacePeerBinding: %v", err)
	}
	if !got.Linked {
		t.Fatalf("binding.Linked = false, want true")
	}
	if got.LinkEstablishedBy != "local" {
		t.Fatalf("LinkEstablishedBy = %q, want local", got.LinkEstablishedBy)
	}
	if got.LinkEstablishedAt == nil {
		t.Fatalf("LinkEstablishedAt is nil, want a timestamp")
	}

	links, err := d.ListWorkspaceLinks(ctx)
	if err != nil {
		t.Fatalf("ListWorkspaceLinks: %v", err)
	}
	if len(links) != 1 || links[0].PeerID != "peer-B" {
		t.Fatalf("ListWorkspaceLinks = %+v, want 1 row for peer-B", links)
	}

	peers, err := d.ListLinkedPeersForWorkspace(ctx, local)
	if err != nil {
		t.Fatalf("ListLinkedPeersForWorkspace: %v", err)
	}
	if len(peers) != 1 || peers[0] != "peer-B" {
		t.Fatalf("ListLinkedPeersForWorkspace = %v, want [peer-B]", peers)
	}
}

func TestPlainUpsertDoesNotClobberLink(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	local := seedWorkspace(t, d, "gateway")

	// Establish the link first.
	if err := d.SetWorkspaceLink(ctx, &store.WorkspacePeerBinding{
		PeerID:            "peer-B",
		RemoteWorkspaceID: "remote-ws-1",
		LocalWorkspaceID:  local,
	}, "local"); err != nil {
		t.Fatalf("SetWorkspaceLink: %v", err)
	}

	// A later offer-accept upsert (the routing-memoization path) must NOT
	// demote the link back to a plain binding.
	if err := d.UpsertWorkspacePeerBinding(ctx, &store.WorkspacePeerBinding{
		PeerID:              "peer-B",
		RemoteWorkspaceID:   "remote-ws-1",
		LocalWorkspaceID:    local,
		RemoteWorkspaceName: "gateway-renamed",
	}); err != nil {
		t.Fatalf("UpsertWorkspacePeerBinding: %v", err)
	}

	got, err := d.GetWorkspacePeerBinding(ctx, "peer-B", "remote-ws-1")
	if err != nil {
		t.Fatalf("GetWorkspacePeerBinding: %v", err)
	}
	if !got.Linked {
		t.Fatalf("plain upsert cleared the link (Linked=false)")
	}
	if got.RemoteWorkspaceName != "gateway-renamed" {
		t.Fatalf("RemoteWorkspaceName = %q, want gateway-renamed (routing fields should still update)", got.RemoteWorkspaceName)
	}
}

func TestClearWorkspaceLink(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	local := seedWorkspace(t, d, "gateway")

	if err := d.SetWorkspaceLink(ctx, &store.WorkspacePeerBinding{
		PeerID:            "peer-B",
		RemoteWorkspaceID: "remote-ws-1",
		LocalWorkspaceID:  local,
	}, "local"); err != nil {
		t.Fatalf("SetWorkspaceLink: %v", err)
	}
	if err := d.ClearWorkspaceLink(ctx, "peer-B", "remote-ws-1"); err != nil {
		t.Fatalf("ClearWorkspaceLink: %v", err)
	}

	// Link gone, but the routing row is preserved.
	got, err := d.GetWorkspacePeerBinding(ctx, "peer-B", "remote-ws-1")
	if err != nil {
		t.Fatalf("GetWorkspacePeerBinding after clear: %v", err)
	}
	if got.Linked {
		t.Fatalf("binding still linked after ClearWorkspaceLink")
	}
	peers, err := d.ListLinkedPeersForWorkspace(ctx, local)
	if err != nil {
		t.Fatalf("ListLinkedPeersForWorkspace: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("ListLinkedPeersForWorkspace = %v, want empty after clear", peers)
	}
}

func TestSetWorkspaceLinkRejectsBadEstablishedBy(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	local := seedWorkspace(t, d, "gateway")
	err := d.SetWorkspaceLink(ctx, &store.WorkspacePeerBinding{
		PeerID:            "peer-B",
		RemoteWorkspaceID: "remote-ws-1",
		LocalWorkspaceID:  local,
	}, "bogus")
	if err == nil {
		t.Fatalf("SetWorkspaceLink accepted invalid establishedBy")
	}
}
