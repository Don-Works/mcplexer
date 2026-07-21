-- 124 — Quiet legacy task-update notifications emitted during PWA beta work.
--
-- Human task lists are live over SSE, so task_updated/task_claimed rows should
-- not appear as unread Signal flashes. Keep the audit/history row, but mark the
-- legacy notification as read so existing local installs stop resurfacing it on
-- every dashboard load.

UPDATE notifications
SET read_at = COALESCE(read_at, CURRENT_TIMESTAMP)
WHERE source = 'task'
  AND kind IN ('task_updated', 'task_claimed')
  AND read_at IS NULL;
