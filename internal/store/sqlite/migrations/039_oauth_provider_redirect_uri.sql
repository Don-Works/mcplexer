-- Persist the redirect URI registered with the OAuth authorization server.
-- Required so the authorize step and the token exchange both send the exact
-- URI that was registered (otherwise the auth server rejects the request).
-- Empty string means legacy / unknown; callers fall back to a request-derived
-- URL and may re-register the client.
ALTER TABLE oauth_providers ADD COLUMN redirect_uri TEXT NOT NULL DEFAULT '';
