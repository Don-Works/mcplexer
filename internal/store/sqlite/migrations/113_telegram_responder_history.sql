-- 113_telegram_responder_history.sql - give telegram-responder conversation memory.
--
-- The bundled telegram-responder (template-bundled-telegram-responder-v2) only
-- ever saw the single inbound message that triggered its run. It had no window
-- onto prior turns, and critically no view of replies other agents sent the
-- user via the telegram__send_message tool. Two pieces are required for the
-- responder to read history; this migration handles the worker side:
--
--   1. The runner pre-loads {mesh_history} ONLY when the worker's merged
--      parameters declare mesh_history_count > 0 (internal/workers/runner.go;
--      historyCount/historyTags). No bundled migration ever set those params,
--      so history was disabled. We set mesh_history_count=12 and
--      mesh_history_tags="telegram" so the runner scopes the window to the
--      Telegram conversation (the human inbound rows tagged human,telegram, the
--      worker replies tagged worker,output,telegram, and the new agent-outbound
--      rows tagged telegram,agent-outbound that manager.go now persists).
--
--   2. The prompt_template must actually render {mesh_history}, otherwise the
--      loaded block is dropped on the floor. We prepend a clearly-labelled
--      "Recent conversation" block to the top of the prompt.
--
-- mesh_history_count / mesh_history_tags live in the runtime parameters map the
-- runner reads (worker.ParametersJSON), NOT in the model prompt. The template's
-- parameter_schema carries them as defaults so fresh installs flow them into the
-- installed worker's parameters_json via mergeParameterDefaults
-- (workers/admin/template_install.go); already-installed workers are patched
-- below. historyCount accepts numeric strings, so the string default "12" is
-- read correctly at runtime.
--
-- TAG SAFETY: the bundled responder mesh trigger is scoped to tag_match='human'
-- (migration 110), so only inbound human messages fire it. The agent-outbound
-- rows manager.go writes are tagged "telegram,agent-outbound" — they match the
-- history filter (substring "telegram") but DO NOT contain "human", so they
-- cannot re-fire the responder. No loop.
--
-- Do not mutate 106/108: deployed DBs carry their checksums in
-- applied_migrations. This migration patches the catalog row + installed
-- workers in-place via json_set so both fresh and upgraded installs converge.

-- (A) Catalog template row: set the two history parameter defaults and inject
-- the {mesh_history} block at the top of the prompt. Guarded so re-runs and the
-- absence of the template are both no-ops, and json_valid keeps a malformed
-- body from being clobbered. The instr() guard on prompt_template makes the
-- prompt prepend idempotent — never duplicate the block.
UPDATE worker_templates
SET
    content_hash = 'bundled-builtin-telegram-responder-v2-history',
    body = json_set(
        CASE
            WHEN instr(json_extract(body, '$.prompt_template'), '{mesh_history}') > 0
                THEN body
            ELSE json_set(
                body,
                '$.prompt_template',
                'Recent conversation (most recent last; "[empty]" when this is the first turn):' || char(10)
                    || '{mesh_history}' || char(10) || char(10)
                    || json_extract(body, '$.prompt_template')
            )
        END,
        '$.parameter_schema',
        json('[{"name":"mesh_history_count","label":"Conversation history turns","type":"number","required":false,"default":"12","description":"How many recent Telegram mesh messages to render into {mesh_history}. 0 disables history."},{"name":"mesh_history_tags","label":"History tag filter","type":"text","required":false,"default":"telegram","description":"Comma-substring tag filter scoping {mesh_history} to the Telegram conversation."}]')
    )
WHERE
    id = 'template-bundled-telegram-responder-v2'
    AND name = 'telegram-responder'
    AND version = 2
    AND json_valid(body);

-- (B) Already-installed responder workers: mirror the template patch onto the
-- live rows so existing installs get history without a reinstall. Sets the two
-- runtime params the runner reads and prepends the {mesh_history} block to the
-- stored prompt_template. json_valid guards the params blob; instr() makes the
-- prompt prepend idempotent.
UPDATE workers
SET
    parameters_json = json_set(
        json_set(
            CASE
                WHEN json_valid(parameters_json) AND json_type(parameters_json) = 'object'
                    THEN parameters_json
                ELSE json('{}')
            END,
            '$.mesh_history_count',
            '12'
        ),
        '$.mesh_history_tags',
        'telegram'
    ),
    prompt_template =
        CASE
            WHEN instr(prompt_template, '{mesh_history}') > 0 THEN prompt_template
            ELSE 'Recent conversation (most recent last; "[empty]" when this is the first turn):' || char(10)
                || '{mesh_history}' || char(10) || char(10)
                || prompt_template
        END
WHERE
    name = 'telegram-responder';
