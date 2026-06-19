-- 028 — display_name propagation across the mesh.
--
-- Two changes needed for the user-facing display-name UX:
--
-- 1. mesh_messages.sender_display_name TEXT — captured from incoming
--    libp2p envelopes so the UI can render "from peer-laptop" instead of
--    "from peer:pLYmq366A7". NOT auth-bearing: the cryptographic identity
--    is still the libp2p PeerID; this column is purely a display hint.
--
-- 2. (no schema change for p2p_peers — display_name already exists on
--    that table from migration 024; we just plumb writes from the new
--    pairing payload + the display_name_changed mesh event handler).

ALTER TABLE mesh_messages
    ADD COLUMN sender_display_name TEXT NOT NULL DEFAULT '';
