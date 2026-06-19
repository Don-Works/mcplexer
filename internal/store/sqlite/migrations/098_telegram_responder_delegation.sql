-- 098 - telegram-responder template v2: delegation from Telegram.
--
-- v1 (migration 084) is a bare acknowledge-and-reply placeholder. v2
-- turns the bundled Telegram worker into a delegation front-door: a
-- message like "use minimax to draft X" or "get me a model in lmstudio
-- and try it out" produces a real mcpx__delegate_worker call, with the
-- LM Studio kickoff tools (lmstudio__*, gated daemon-side behind
-- MCPLEXER_ALLOW_LMSTUDIO=1) available for the local-model path.
--
-- Same catalog semantics as 084: this is a passive template row, not a
-- running worker. Existing installs keep their installed v1 workers
-- untouched; the dashboard surfaces "v2 available - review changes" via
-- workers.source_template_version, and fresh installs see v2 in the
-- gallery. Deterministic id + INSERT OR IGNORE so re-runs are a no-op,
-- and operators who already published their own higher version keep
-- winning via MAX(version)+1 in the Publish flow.
--
-- The delegation tools work from a worker context: workers reach
-- builtins inside mcpx__execute_code, each inner call is checked
-- against this tool_allowlist (gateway/handler_codemode.go), and the
-- worker carries a write grant on its own workspace, which
-- mcpx__delegate_worker requires.

INSERT OR IGNORE INTO worker_templates (
    id, name, version, content_hash, description, body,
    metadata_json, tags_json, author, parent_version,
    published_at, created_by_agent_id, workspace_id
) VALUES
(
    'template-bundled-telegram-responder-v2',
    'telegram-responder',
    2,
    'bundled-builtin-telegram-responder-v2',
    'Reply to inbound Telegram messages AND turn "use <model> to do X" requests into real delegations (mcpx__delegate_worker), including kicking off LM Studio for local models. After install, add a mesh trigger with tag=telegram so it fires on incoming chats.',
    '{"name":"telegram-responder","description":"Reply to inbound Telegram messages from this workspace. Messages that name a model or provider become delegations; LM Studio is started and loaded on demand for local-model requests.","model_provider_hint":"anthropic","model_id_hint":"claude-haiku-4-5","prompt_template":"You received a message from a user on Telegram via mcplexer. The text is in the inbound mesh message that triggered this run.\n\nFirst decide which mode applies.\n\nMODE 1 - DELEGATION REQUEST. The user names a model, provider, or local runtime and asks for work to be done with it. Examples: \"use minimax to draft X\", \"have glm review Y\", \"get me a model in lmstudio and try it out\". Steps:\n1. Call mcpx__list_delegation_model_capacity and look for a registered model profile whose name or known models match what the user asked for. If one matches, delegate with that model_profile_id.\n2. If the user asked for LM Studio or a local model: call lmstudio__status. If the server is down, lmstudio__start_server. If no model is loaded, pick one from lmstudio__list_models and load it with lmstudio__load_model (lmstudio__download_model first if nothing suitable is downloaded - prefer small models). Then delegate with model_provider=openai_compat, model_endpoint_url set to the endpoint shown by lmstudio__status plus /v1, and model_id set to the loaded model id.\n3. Call mcpx__delegate_worker with a concrete objective and a bounded handoff: what to produce, constraints, acceptance criteria. Set name to a short slug.\n4. Poll mcpx__list_delegations for the returned delegation id roughly every 20 seconds while your run budget allows. If it completes, summarize the worker output. If it is still running near your budget, stop polling and report the delegation id instead.\n5. Reply with what you delegated, which model ran it, and the result or the delegation id (results also appear on the dashboard under /delegations). Keep it under 1000 characters.\n\nMODE 2 - EVERYTHING ELSE. Reply with a friendly, direct answer. If they asked a factual question, answer it; otherwise acknowledge. Keep it under 280 characters.\n\nAlways send your reply through the mesh output channel with reply_to_trigger=true so it lands back in the same Telegram thread.","schedule_spec_hint":"","tool_allowlist":["telegram__send_message","telegram__list_chats","mcpx__search_tools","mcpx__delegate_worker","mcpx__list_delegations","mcpx__list_delegation_model_capacity","mcpx__review_delegation","lmstudio__status","lmstudio__start_server","lmstudio__stop_server","lmstudio__list_models","lmstudio__load_model","lmstudio__unload_model","lmstudio__download_model"],"output_channels_hint":[{"type":"mesh","priority":"normal"}],"exec_mode_hint":"autonomous","parameter_schema":[],"secret_slots":[{"name":"model_api_key","description":"API key for the model provider that drives the responder itself (delegated workers bring their own credentials via model profiles or secret scopes)","provider_hint":"anthropic"}]}',
    '{}',
    '["builtin","telegram","delegation","lmstudio","mesh-triggered"]',
    'mcplexer-builtin',
    1,
    CAST(strftime('%s', '2026-06-10') AS INTEGER),
    NULL,
    NULL
);
