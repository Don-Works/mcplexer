-- 118_delegation_review_optional.sql - make parent review opt-in by default.
--
-- Earlier delegation guidance and schemas defaulted review_required=true. That
-- made routine worker results land in needs_review until the parent spent more
-- tokens scoring them. The product default is now optional review: callers can
-- still set review_required=true when parent review must gate completion or
-- feed model-ranking telemetry.

-- Existing unreviewed delegation worker rows mostly carry the old default as
-- explicit metadata. Clear that stale gate so current dashboards stop showing a
-- wall of needs_review rows after this migration.
UPDATE workers
SET parameters_json = json_set(
    parameters_json,
    '$._mcplexer_delegation.review_required',
    json('false')
)
WHERE
    json_valid(parameters_json)
    AND json_extract(parameters_json, '$._mcplexer_delegation.kind') = 'token_preserving_delegation'
    AND COALESCE(json_extract(parameters_json, '$._mcplexer_delegation.review_required'), 0) = 1
    AND COALESCE(json_extract(parameters_json, '$._mcplexer_delegation.review.reviewed'), 0) = 0;

-- The bundled Telegram responder prompt also taught workers to request
-- review_required:true for user-triggered model delegations. Patch both the
-- catalog template and already-installed workers in place; do not mutate older
-- migration files because deployed databases ledger their checksums.
UPDATE worker_templates
SET body = replace(body, 'review_required:true', 'review_required:false')
WHERE
    name = 'telegram-responder'
    AND json_valid(body)
    AND instr(body, 'review_required:true') > 0;

UPDATE workers
SET prompt_template = replace(prompt_template, 'review_required:true', 'review_required:false')
WHERE
    name = 'telegram-responder'
    AND instr(prompt_template, 'review_required:true') > 0;
