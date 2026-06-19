package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// wireMeshP2P attaches the libp2p mesh transport to the mesh manager and
// starts the inbound bridge so cross-machine envelopes flow into the local
// mesh_messages table. No-op when either dependency is nil (mesh disabled,
// p2p disabled, or running in slim build).
//
// Returns the transport so the caller can defer Close. May return nil.
func wireMeshP2P(
	ctx context.Context,
	host *p2p.Host,
	lookup *p2p.SQLPeerLookup,
	mgr *mesh.Manager,
) *p2p.MeshTransport {
	if host == nil || mgr == nil {
		return nil
	}
	transport := p2p.NewMeshTransport(host, lookup, nil, slog.Default())
	// Scope outbound broadcasts to each peer's bound workspaces (workspace-
	// scoped pairing). lookup (*SQLPeerLookup) resolves workspace_peer_bindings.
	if lookup != nil {
		transport.SetWorkspaceLookup(lookup)
	}
	transport.Start()
	mgr.SetP2PTransport(transport, host.PeerID())
	mgr.StartP2PBridge(ctx, slog.Default())
	slog.Info("mesh p2p transport ready",
		"protocol", p2p.MeshProtocol, "self_peer_id", host.PeerID())
	return transport
}

// wireMeshOutboundQueue attaches the offline-delivery queue to the mesh
// manager so to_peer sends that fail at the libp2p layer (peer offline)
// get parked durably and retried when the peer comes back online.
//
// Subscribes to the reconnector's online-transition signal so a peer
// reconnect drains its queued envelopes immediately. Starts the 30s
// background sweep + daily prune. No-op when prerequisites are missing.
func wireMeshOutboundQueue(
	ctx context.Context,
	mgr *mesh.Manager, st store.MeshStore,
	transport *p2p.MeshTransport, reconnector *p2p.Reconnector,
	liveness *p2p.LivenessMonitor, notifyBus *notify.Bus,
) *mesh.OutboundQueue {
	if mgr == nil || st == nil || transport == nil {
		return nil
	}
	q := mesh.NewOutboundQueue(st, transport, liveness, notifyBus, slog.Default())
	mgr.SetOutboundQueue(q)
	q.Start(ctx)
	if reconnector != nil {
		reconnector.AddOnlineObserver(func(peerID string) {
			drainCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			q.DrainForPeer(drainCtx, peerID)
		})
	}
	slog.Info("mesh outbound queue ready")
	return q
}
