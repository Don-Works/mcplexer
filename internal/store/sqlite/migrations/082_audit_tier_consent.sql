-- 082_audit_tier_consent.sql
--
-- Bulletproof e2e (epic 01KSK91Q4W8TNED9MAF0CTRVKC) requires every
-- cross-boundary audit row (mesh__skill_share, mesh__memory_share,
-- mesh__task_share, peer-addressed mesh__send) to carry the trust
-- tier + the consent envelope that authorized the data movement.
--
-- Adds four nullable columns so old rows stay valid:
--
--   - tier           — one of same_user | same_org | cross_org. NULL
--                      for non-cross-boundary rows (everything pre-081
--                      and any in-process row).
--   - accepted_by    — JSON object describing who acknowledged the
--                      share. Shape:
--                        {kind:"auto_pair"}                    (Tier 1)
--                        {kind:"human", user_id, agent_id, timestamp}
--                                                              (Tier 2/3)
--   - grant_origin   — JSON object referencing the scope grant that
--                      authorized a Tier 2/3 explicit-grant share.
--                      Shape:
--                        {peer_id, agent_id, grant_id}
--                      NULL on Tier 1 (no explicit grant) and on
--                      pre-grant denial rows.
--   - denial_reason  — short stable string (e.g. "scope_revoked",
--                      "cross_org_no_grant", "not_paired") on rows that
--                      record a rejection. Lets ops + UI filter without
--                      string-matching error_message. NULL when status
--                      is success.
--
-- All four are NULLable (no DEFAULT) so legacy rows continue to scan
-- as nil; new emit sites populate them at insertion. No backfill — the
-- columns are forward-only metadata, historical rows had no concept
-- of trust tier.

ALTER TABLE audit_records ADD COLUMN tier TEXT;
ALTER TABLE audit_records ADD COLUMN accepted_by TEXT;
ALTER TABLE audit_records ADD COLUMN grant_origin TEXT;
ALTER TABLE audit_records ADD COLUMN denial_reason TEXT;

-- Index supports the consent_audit walk: "every cross-boundary row
-- with tier T". The bulletproof scenario reads /api/v1/audit?limit=N
-- and groups by tier; without this index the filter is a full table
-- scan.
CREATE INDEX IF NOT EXISTS idx_audit_records_tier
    ON audit_records(tier, timestamp DESC) WHERE tier IS NOT NULL;
