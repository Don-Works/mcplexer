-- M4.1 — agent-to-human email (send-only via AWS SES).
--
-- Three tables back the email__send MCP tool:
--
--   emails:                    full record per send (body kept here, never in audit)
--   email_approved_recipients: per-(agent_id, recipient) approval ledger; first
--                              send to a new pair blocks on human approval,
--                              subsequent sends bypass.
--   email_send_budget:         per-agent rolling counters for hour/day/month
--                              caps + monthly send budget (200/mo default).
--
-- Direction is constrained to 'out' for now; receive flow is deferred.

CREATE TABLE IF NOT EXISTS emails (
    id              TEXT PRIMARY KEY,
    agent_id        TEXT NOT NULL,
    direction       TEXT NOT NULL DEFAULT 'out' CHECK (direction IN ('out', 'in')),
    message_id      TEXT NOT NULL DEFAULT '',
    from_addr       TEXT NOT NULL,
    to_addrs        TEXT NOT NULL DEFAULT '[]',
    cc_addrs        TEXT NOT NULL DEFAULT '[]',
    bcc_addrs       TEXT NOT NULL DEFAULT '[]',
    subject         TEXT NOT NULL DEFAULT '',
    body_text       TEXT NOT NULL DEFAULT '',
    body_html       TEXT NOT NULL DEFAULT '',
    ses_message_id  TEXT NOT NULL DEFAULT '',
    sent_at         TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_emails_agent_id  ON emails(agent_id);
CREATE INDEX IF NOT EXISTS idx_emails_sent_at   ON emails(sent_at);
CREATE INDEX IF NOT EXISTS idx_emails_direction ON emails(direction);

CREATE TABLE IF NOT EXISTS email_approved_recipients (
    agent_id        TEXT NOT NULL,
    recipient_email TEXT NOT NULL,
    approved_at     TEXT NOT NULL DEFAULT (datetime('now')),
    approved_by     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (agent_id, recipient_email)
);

CREATE INDEX IF NOT EXISTS idx_email_approved_recipients_agent
    ON email_approved_recipients(agent_id);

CREATE TABLE IF NOT EXISTS email_send_budget (
    agent_id            TEXT PRIMARY KEY,
    sends_this_hour     INTEGER NOT NULL DEFAULT 0,
    sends_this_day      INTEGER NOT NULL DEFAULT 0,
    sends_this_month    INTEGER NOT NULL DEFAULT 0,
    last_reset          TEXT NOT NULL DEFAULT (datetime('now'))
);
