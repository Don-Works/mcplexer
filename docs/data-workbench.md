# Data Workbench

The `data__*` tools provide a workspace-scoped scratch area for temporary
tables, API payloads, logs, and document snippets. The surface is intentionally
higher level than a raw SQLite shell: agents ingest data by name, inspect it,
run bounded read queries, search it with FTS5, and drop it when the run is done.

Scratch collections are not durable knowledge. Promote conclusions with
`memory__save` when the result should survive beyond the working session.

## Tools

| Tool | Purpose |
| --- | --- |
| `data__ingest` | Create or replace a named scratch collection from rows, documents, CSV, or JSONL |
| `data__list` | List active collections in the current workspace |
| `data__describe` | Return counts, schema metadata, TTL, tags, and provenance |
| `data__query` | Run a single bounded `SELECT`/`WITH` query against one collection |
| `data__search` | Search a collection using the FTS5 retrieval floor |
| `data__drop` | Purge a collection and its indexed payloads |

In code mode these are available as `data.ingest(...)`, `data.query(...)`, and
so on.

## Example

```js
const rows = [
  { id: "A-1", state: "open", tier: "p0", title: "urgent customer outage" },
  { id: "A-2", state: "done", tier: "p1", title: "routine cleanup" },
];

data.ingest({
  name: "issues",
  rows,
  tags: ["run-123"],
  ttl_minutes: 120,
});

const byState = data.query({
  name: "issues",
  sql: "SELECT state, COUNT(*) AS c FROM data GROUP BY state",
});

const urgent = data.search({
  name: "issues",
  query: "urgent customer",
  limit: 5,
});

memory.save({
  name: "run-123-issue-summary",
  content: `Open issue counts: ${JSON.stringify(byState.rows)}`,
  tags: ["run-123", "summary"],
});

data.drop({ name: "issues" });
```

`data.query` runs in an isolated in-memory SQLite database that contains only
the selected collection. Use table `data`; when the collection name is a valid
SQL identifier, that name is also available as a view. Queries are wrapped with
a server-side limit and must be a single read-only statement.

## Safety Model

- Collections are scoped to the current workspace and session provenance is
  recorded.
- `data__ingest` and `data__drop` are write-class tools. Read-only workers can
  list, describe, query, and search, but not mutate collections.
- Ingest audit records redact row, document, and text payloads. Audit metadata
  keeps collection name, tags, counts, and payload sizes.
- Default TTL is 24 hours unless a collection is pinned or a TTL override is
  supplied.
- Generic downstream `sqlite__*` tools remain an opt-in power-user escape hatch;
  the `data__*` surface is the blessed agent UX for transient worksets.

Semantic/vector retrieval is staged for a later embedding-backed path. The MVP
always uses FTS5 as the retrieval floor.
