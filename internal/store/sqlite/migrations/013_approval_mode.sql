-- Replace boolean requires_approval with approval_mode ('none', 'write', 'all').
ALTER TABLE route_rules ADD COLUMN approval_mode TEXT NOT NULL DEFAULT 'none';
UPDATE route_rules SET approval_mode = 'all' WHERE requires_approval = 1;
