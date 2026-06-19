-- 099 - Introduce kind='review' for the 'review' status.
--
-- Migration 070 seeded the suggested-default 'review' status with
-- kind='blocked'. That conflates two lifecycle states: a blocked task is
-- waiting on an external unblock; a review task has finished its working
-- phase and is awaiting verification / signoff.
--
-- kind='review' semantics:
--   * NOT a working kind: no lease, no auto-claim, not reclaimed by the
--     lease sweep.
--   * NOT terminal: closed_at is not stamped on entry.
--
-- Only rows still carrying the migration-070 default are rewritten; a
-- workspace that deliberately reclassified 'review' keeps its choice.

UPDATE task_status_vocabulary
   SET kind = 'review'
 WHERE status_text = 'review'
   AND kind = 'blocked';
