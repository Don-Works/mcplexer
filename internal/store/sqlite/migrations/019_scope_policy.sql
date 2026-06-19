-- Migrate allowed_orgs/allowed_repos into a generic scope_policy column.
ALTER TABLE route_rules ADD COLUMN scope_policy TEXT NOT NULL DEFAULT '{}';

-- Convert existing allowlists to scope_policy format.
-- Only rows with non-empty allowlists are migrated.
UPDATE route_rules
SET scope_policy = json_object(
    'org',  json(allowed_orgs),
    'repo', json(allowed_repos)
)
WHERE allowed_orgs != '[]' OR allowed_repos != '[]';

-- Drop the old columns (SQLite requires recreating the table).
-- Since SQLite 3.35.0+ supports DROP COLUMN, and modernc.org/sqlite bundles 3.40+.
ALTER TABLE route_rules DROP COLUMN allowed_orgs;
ALTER TABLE route_rules DROP COLUMN allowed_repos;
