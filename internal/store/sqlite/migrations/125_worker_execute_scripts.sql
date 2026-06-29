-- Worker pre/post-execute JS hooks: two first-class columns carrying
-- optional user-authored JavaScript run in the code-mode sandbox around
-- the model loop.
--
--   pre_execute_script  — runs BEFORE any model/CLI spend; may BLOCK the
--                         run (throw / abort(reason)). Use to hit an
--                         endpoint and gate the run on a condition.
--   post_execute_script — runs AFTER output is produced; may REJECT the
--                         output (throw on a successful run => "blocked").
--
-- Empty string = no hook = today's behaviour. Behaviour-bearing config
-- MUST live in a column (not the display-only parameters_json blob) so a
-- corrupt or absent blob can never silently change execution. Matching
-- schema invariant (ensureWorkerExecuteScripts) adds these idempotently on
-- every boot, covering branch swaps / partially-restored backups.

ALTER TABLE workers ADD COLUMN pre_execute_script TEXT NOT NULL DEFAULT '';
ALTER TABLE workers ADD COLUMN post_execute_script TEXT NOT NULL DEFAULT '';
