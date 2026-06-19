-- 054_audit_actor_columns_recategorize.sql
--
-- 053 backfilled actor_kind using a CASE expression whose first branch
-- was `client_type LIKE 'worker%'` — that LIKE pattern is greedy and
-- catches BOTH `client_type='worker'` (the runner) AND
-- `client_type='worker_admin'` (the CRUD surface). Historical
-- worker_admin rows got backfilled as actor_kind='worker', mixing two
-- distinct actor classes into the same forensics bucket. A reviewer
-- asking "show me everything the worker_admin CRUD surface did" sees
-- only the new (post-053) rows and misses every legacy CRUD mutation.
--
-- The fix is idempotent + safe on both code paths:
--
--   - Fresh installs (053 ran with the LIKE-only CASE before this
--     migration shipped): the rewrite below patches the affected rows.
--   - Upgraded installs (053 will run with the corrected ordering when
--     this file is applied; see comment in 053): the WHERE clause is a
--     no-op because no row will satisfy `actor_kind='worker' AND
--     client_type='worker_admin'`.
--   - Future installs landing both 053 + 054 in one boot: 053 already
--     runs with the corrected ordering, so this is also a no-op.
--
-- We narrow the WHERE clause to the exact mistake (actor_kind='worker'
-- AND client_type='worker_admin') so the migration cannot accidentally
-- rewrite rows the operator may have hand-corrected after the fact.

UPDATE audit_records
SET actor_kind = 'worker_admin'
WHERE actor_kind = 'worker'
  AND client_type = 'worker_admin';
