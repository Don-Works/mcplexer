-- 108_telegram_responder_reply_output.sql - preserve Telegram reply delivery.
--
-- The telegram-responder must emit a notify-able mesh reply to the inbound
-- Telegram mesh message. Earlier bundled templates only hinted a plain mesh
-- output channel, and the worker edit UI could save that reduced shape. Repair
-- the catalog row and simple installed responders while preserving priority.

UPDATE worker_templates
SET
    content_hash = 'bundled-builtin-telegram-responder-v2-reply-output',
    body = json_set(
        body,
        '$.output_channels_hint',
        json('[{"type":"mesh","priority":"normal","tags":"telegram","notify_user":true,"reply_to_trigger":true}]')
    )
WHERE
    id = 'template-bundled-telegram-responder-v2'
    AND name = 'telegram-responder'
    AND version = 2
    AND json_valid(body);

UPDATE workers
SET output_channels_json = json_set(
    json_set(
        json_set(
            output_channels_json,
            '$[0].notify_user',
            json('true')
        ),
        '$[0].reply_to_trigger',
        json('true')
    ),
    '$[0].tags',
    CASE
        WHEN trim(COALESCE(json_extract(output_channels_json, '$[0].tags'), '')) = '' THEN 'telegram'
        WHEN instr(lower(json_extract(output_channels_json, '$[0].tags')), 'telegram') > 0
            THEN json_extract(output_channels_json, '$[0].tags')
        ELSE json_extract(output_channels_json, '$[0].tags') || ',telegram'
    END
)
WHERE
    name = 'telegram-responder'
    AND json_valid(output_channels_json)
    AND json_array_length(output_channels_json) = 1
    AND json_extract(output_channels_json, '$[0].type') = 'mesh'
    AND COALESCE(json_extract(output_channels_json, '$[0].notify_user'), 0) = 0
    AND COALESCE(json_extract(output_channels_json, '$[0].reply_to_trigger'), 0) = 0;
