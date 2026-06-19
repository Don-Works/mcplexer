ALTER TABLE route_rules ADD COLUMN allowed_orgs TEXT NOT NULL DEFAULT '[]';
ALTER TABLE route_rules ADD COLUMN allowed_repos TEXT NOT NULL DEFAULT '[]';
