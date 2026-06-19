UPDATE downstream_servers
SET discovery = 'dynamic', updated_at = datetime('now')
WHERE discovery = 'static'
  AND transport != 'internal'
  AND source = 'api';
