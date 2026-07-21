-- Tool description version history for model-driven refinement.
CREATE TABLE tool_description_versions (
    id            TEXT PRIMARY KEY,
    tool_name     TEXT NOT NULL,
    description   TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'model',
    status        TEXT NOT NULL DEFAULT 'pending',
    session_id    TEXT NOT NULL DEFAULT '',
    model         TEXT NOT NULL DEFAULT '',
    workspace_id  TEXT NOT NULL DEFAULT '',
    rationale     TEXT NOT NULL DEFAULT '',
    reviewed_by   TEXT NOT NULL DEFAULT '',
    review_note   TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    reviewed_at   TEXT
);
CREATE INDEX idx_tdv_tool_status ON tool_description_versions(tool_name, status);
CREATE INDEX idx_tdv_status ON tool_description_versions(status);
