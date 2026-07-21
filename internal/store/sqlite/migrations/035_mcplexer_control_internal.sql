-- Migrate the "mcplexer" downstream entry from the old transport=stdio
-- (which spawned `mcplexer control-server` as a child process and defaulted
-- to read-only) to transport=internal, backed by an in-process
-- InternalBackend on the gateway. The new backend always exposes full
-- CRUD; visibility is gated to the data directory by AdminCWDGate.
--
-- Idempotent: only updates rows where the OLD shape is present, so
-- re-running on a fresh install is a no-op.
UPDATE downstream_servers
   SET transport = 'internal',
       command = '',
       args = '[]',
       discovery = 'static',
       max_instances = 1,
       idle_timeout_sec = 0,
       restart_policy = 'on-failure'
 WHERE id = 'mcplexer'
   AND transport = 'stdio'
   AND command = 'mcplexer';
