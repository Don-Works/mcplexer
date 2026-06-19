-- 110_telegram_responder_human_trigger.sql - stop worker-output echo triggers.
--
-- Inbound Telegram messages are tagged human,<platform> (see telegram/routing.go).
-- Worker mesh output is tagged worker,output,telegram, which also matched the
-- legacy tag_match=telegram trigger and could re-fire telegram-responder on its
-- own replies. Scope the bundled responder trigger to human so only real inbound
-- chat messages fire it.

UPDATE worker_mesh_triggers
SET tag_match = 'human'
WHERE tag_match = 'telegram'
  AND worker_id IN (
      SELECT id FROM workers WHERE name = 'telegram-responder'
  );
