-- M0-A — Installed-client registry + receipt ledger.
--
-- installed_clients tracks every MCP client (claude_code, picoclaw, cursor,
-- ...) mcplexer has installed or hooked into on this machine, plus which
-- integration steps were applied (hooks, shim, sandbox). Setup writes one
-- row per client; uninstall flips the flags back off.
--
-- install_receipts records EACH reversible OS-side mutation as a row, so
-- `mcplexer uninstall` can replay them in reverse without guessing what the
-- prior state was. reverse_data is a JSON blob whose schema depends on
-- action (e.g. {"key": "..."} for a JSON config edit, {"line": "..."} for
-- a /etc/shells append). reversed_at flips when uninstall succeeds.

CREATE TABLE IF NOT EXISTS installed_clients (
    id                TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    config_path       TEXT NOT NULL DEFAULT '',
    installed         INTEGER NOT NULL DEFAULT 0,
    hooks_installed   INTEGER NOT NULL DEFAULT 0,
    shim_installed    INTEGER NOT NULL DEFAULT 0,
    sandbox_enabled   INTEGER NOT NULL DEFAULT 0,
    installed_at      DATETIME NULL,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS install_receipts (
    id             TEXT PRIMARY KEY,
    client_id      TEXT NOT NULL DEFAULT '',
    action         TEXT NOT NULL,
    target_path    TEXT NOT NULL DEFAULT '',
    backup_path    TEXT NOT NULL DEFAULT '',
    reverse_data   TEXT NOT NULL DEFAULT '',
    applied_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    reversed_at    DATETIME NULL,
    reverse_error  TEXT NOT NULL DEFAULT ''
);

-- "What's still active for this client?" — partial predicate keeps the
-- index small once historical receipts have been reversed.
CREATE INDEX IF NOT EXISTS idx_install_receipts_client
    ON install_receipts(client_id)
    WHERE reversed_at IS NULL;

-- Secondary index for global "show me everything we ever did to
-- /etc/shells" style queries from the admin UI.
CREATE INDEX IF NOT EXISTS idx_install_receipts_action
    ON install_receipts(action);
