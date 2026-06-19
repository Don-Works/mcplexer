-- 084 — Bundled placeholder worker templates for Telegram + Slack.
--
-- A fresh install ships with these two templates pre-published in
-- worker_templates so the /workers/templates gallery (linked from the
-- sidebar) has a "common useful automation" entry the user can
-- one-click install instead of building from scratch.
--
-- "Disabled by default" is structural: worker_templates is a catalog of
-- shapes, not running workers. A template only becomes an enabled
-- Worker when the user clicks Install in the gallery, fills in the
-- modal (incl. binding the model_api_key secret slot), and submits.
-- Until then both templates are passive entries in the catalog.
--
-- Both templates tolerate missing downstream credentials cleanly:
--   - telegram__send_message returns "telegram bot token is not
--     configured" when the bot token is unset (mcpserver.go).
--   - slack_webhook channel errors on empty URL — the user is expected
--     to replace REPLACE_WITH_WEBHOOK_URL on the installed worker
--     before enabling.
--
-- Pattern mirrors 052_bundled_worker_templates.sql + 064: deterministic
-- IDs (`template-bundled-<name>`), recognisable content_hash prefix
-- (`bundled-builtin-`), and INSERT OR IGNORE so re-runs on a
-- partially-seeded DB are a no-op. Operators publishing a custom
-- version of either template name get higher version numbers via
-- MAX(version)+1 in the Publish flow.

INSERT OR IGNORE INTO worker_templates (
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    published_at, created_by_agent_id, workspace_id
) VALUES
(
    'template-bundled-telegram-responder',
    'telegram-responder',
    1,
    'bundled-builtin-telegram-responder-v1',
    'Ack inbound Telegram messages with a friendly one-liner. Built-in placeholder template — after install, add a mesh trigger with tag=telegram so it fires on incoming chats.',
    '{"name":"telegram-responder","description":"Reply to inbound Telegram messages from this workspace. Placeholder — replace the prompt with whatever behaviour you want.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"You received a message from a user on Telegram via mcplexer. The text is in the inbound mesh message that triggered this run.\n\nReply with a friendly one-liner acknowledging what they said. Keep it under 280 characters. If they asked a factual question, answer it directly; otherwise just acknowledge.\n\nUse the mesh output channel (with reply_to_trigger=true) so your reply is delivered back into the same Telegram thread.","schedule_spec_hint":"","tool_allowlist":["telegram__send_message","telegram__list_chats","mcpx__search_tools"],"output_channels_hint":[{"type":"mesh","priority":"normal"}],"exec_mode_hint":"autonomous","parameter_schema":[],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider that drafts the reply","provider_hint":"anthropic"}]}',
    '{}',
    '["builtin","telegram","starter","mesh-triggered"]',
    'mcplexer-builtin',
    NULL,
    CAST(strftime('%s', '2026-05-27') AS INTEGER),
    NULL,
    NULL
),
(
    'template-bundled-slack-status-notify',
    'slack-status-notify',
    1,
    'bundled-builtin-slack-status-notify-v1',
    'Post a daily mcplexer status digest to a Slack channel via incoming webhook. Built-in placeholder — replace REPLACE_WITH_WEBHOOK_URL on the installed worker before enabling.',
    '{"name":"slack-status-notify","description":"Daily Slack digest of mcplexer state — workspaces, servers, sessions. Posts via Slack incoming webhook.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"Summarize the current state of mcplexer in 2-3 sentences for a daily Slack digest. Cover: number of workspaces, active downstream servers, recent worker activity. Keep it under 240 characters total — Slack truncates the channel preview at 280 chars.","schedule_spec_hint":"0 9 * * *","tool_allowlist":["mcplexer__status"],"output_channels_hint":[{"type":"slack_webhook","url":"REPLACE_WITH_WEBHOOK_URL","prefix":"[mcplexer]"}],"exec_mode_hint":"autonomous","parameter_schema":[],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider that drafts the digest","provider_hint":"anthropic"}]}',
    '{}',
    '["builtin","slack","digest","starter"]',
    'mcplexer-builtin',
    NULL,
    CAST(strftime('%s', '2026-05-27') AS INTEGER),
    NULL,
    NULL
);
