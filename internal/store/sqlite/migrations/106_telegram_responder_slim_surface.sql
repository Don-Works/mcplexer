-- 106_telegram_responder_slim_surface.sql - repair telegram-responder v2.
--
-- Migration 098 originally published the delegation-capable Telegram template
-- before delegated workers consistently received the slim mcplexer surface.
-- Do not mutate 098: deployed DBs have its checksum in applied_migrations.
-- This migration upgrades the bundled catalog row in-place so both fresh and
-- already-upgraded installs get the corrected prompt.

UPDATE worker_templates
SET
    content_hash = 'bundled-builtin-telegram-responder-v2-slim-surface',
    description = 'Reply to inbound Telegram messages AND turn "use <model> to do X" requests into real delegations (mcpx__delegate_worker), including kicking off LM Studio for local models. After install, add a mesh trigger with tag=telegram so it fires on incoming chats.',
    body = '{"name":"telegram-responder","description":"Reply to inbound Telegram messages from this workspace. Messages that name a model or provider become delegations; LM Studio is started and loaded on demand for local-model requests.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"MODE 1 - DELEGATION REQUEST. The user names a model, provider, or local runtime and asks for work to be done with it. Examples: \"use grok to draft X\", \"have minimax review Y\", \"get me a model in lmstudio and try it out\". Workers only expose mcpx__search_tools + mcpx__execute_code directly (per slim surface + preamble). All mcpx__* / lmstudio__* / telegram__* are used from inside mcpx__execute_code JS after search discovery. Namespaces mcpx, task, mesh etc are globals inside the snippet; use print(JSON.stringify(val)) to surface values. Steps: 1. search_tools queries for delegation/capacity then execute_code JS to list capacity and pick free/native profile (grok_cli/opencode_cli/local preferred). 2. Same for lmstudio__* tools to start/load if needed; delegate with openai_compat endpoint. 3. execute_code to call mcpx.delegate_worker({objective, handoff, model_profile_id or provider/model, review_required:true}). 4. Poll list_delegations via execute. Summarize or report id. 5. Mesh reply with model used + outcome/id (<1000 chars).\n\nMODE 2 - EVERYTHING ELSE. Reply with a friendly, direct answer. If they asked a factual question, answer it; otherwise acknowledge. Keep it under 280 characters.\n\nAlways send your reply through the mesh output channel with reply_to_trigger=true so it lands back in the same Telegram thread.","schedule_spec_hint":"","tool_allowlist":["telegram__send_message","telegram__list_chats","mcpx__search_tools","mcpx__delegate_worker","mcpx__list_delegations","mcpx__list_delegation_model_capacity","mcpx__review_delegation","lmstudio__status","lmstudio__start_server","lmstudio__stop_server","lmstudio__list_models","lmstudio__load_model","lmstudio__unload_model","lmstudio__download_model"],"output_channels_hint":[{"type":"mesh","priority":"normal"}],"exec_mode_hint":"autonomous","parameter_schema":[],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider that drives the responder itself (delegated workers bring their own credentials via model profiles or secret scopes)","provider_hint":"anthropic"}]}'
WHERE
    id = 'template-bundled-telegram-responder-v2'
    AND name = 'telegram-responder'
    AND version = 2
    AND content_hash = 'bundled-builtin-telegram-responder-v2';
