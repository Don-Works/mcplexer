# Code index

MCPlexer's `index` namespace is a persistent, local code-navigation layer shared by every connected harness. It combines exact symbol lookup, source-chunk search, imports/importers, test ownership, git freshness, failure mapping, and token-budgeted context packs.

The useful split is:

| Need | Call inside `mcpx__execute_code` |
| --- | --- |
| Orient to a task | `index.context({query, budget_tokens})` |
| Find implementation or behavior | `index.search({query, kind, limit})` |
| Find a declaration | `index.symbols({query, kind, exported_only})` |
| Check edit blast radius | `index.deps({file, direction:"both"})` |
| Find owning tests | `index.tests_for({file})` |
| Map a failure or stack trace | `index.map_failure({text})` |
| Check or refresh | `index.status({})`, `index.build({paths, force})` |

`index.search` returns bounded source snippets with root-relative `path:start-end` citations and retrieval provenance. FTS5 lexical search is always available. If optional local embeddings are ready, lexical and semantic ranks are fused; callers never compare BM25 scores with vector distances.

`index.context` is the opening move for a broader task. It ranks files using source chunks, symbols, file metadata, import-graph proximity, and git churn, then returns snippets, key declarations, owning tests, and recent commits within the requested token budget.

## One physical index per repository root

The gateway authorizes the logical workspace first, then derives a private physical index id from the exact canonical absolute root (including symlink resolution). Two authorized/shared workspace ids pointing to that same root reuse one build, FTS corpus, embedding set, and in-process build flight. Different roots, clones, and worktrees stay isolated because they may contain different commits or dirty files.

This sharing is local to the gateway/database on that machine. MCPlexer does not mesh-replicate source, chunks, or vectors. Returning the same physical cache to two workspaces never grants access: each query must still pass normal workspace/CWD authorization before the index service receives the root.

Upgrading from the older workspace-id namespace performs a one-time purge of the derived cache; the next query rebuilds it under the shared repository namespace. No durable user data is involved.

## Exclusion and privacy boundary

One centralized path predicate is applied to git enumeration, filesystem walking, file processing, chunking, and—again immediately before an embedding request. It rejects traversal/absolute paths, symlinks, credentials, and common dependency/build/cache/generated trees, including:

- `node_modules`, `vendor`, `third_party`, virtual environments, `site-packages`, Pods, Gradle/Maven/Rust build trees;
- `dist`, `build`, `out`, coverage, framework caches, `.git`, agent scratch, and hidden directories;
- lock/checksum files, source maps, minified assets, generated filename suffixes, `.env*`, private keys, and keystores;
- binary files, files above 1 MiB, generated comment headers, and pathological minified long-line content.

This policy applies even when an excluded file is committed or force-added to git. A corrupt legacy chunk with a denied path causes embedding backfill to stop before the provider is called.

## Optional semantic search

Code embeddings are off by default and configured independently from memory embeddings. Generic OpenAI credentials and `MCPLEXER_EMBED_*` settings cannot enable them.

Use a local OpenAI-compatible server explicitly:

```bash
MCPLEXER_CODE_INDEX_EMBED_PROVIDER=local \
MCPLEXER_CODE_INDEX_EMBED_BASE_URL=http://127.0.0.1:1234/v1 \
MCPLEXER_CODE_INDEX_EMBED_MODEL=nomic-embed-code \
mcplexer serve
```

Or set `MCPLEXER_CODE_INDEX_EMBED_PROVIDER=auto` to explicitly allow probing known loopback model-server ports. The endpoint validator accepts only `localhost` or loopback IP URLs; there is no cloud provider mode. A dedicated `MCPLEXER_CODE_INDEX_EMBED_API_KEY` is available for a loopback server that requires a bearer token.

Backfill runs asynchronously in batches after a build or query. `index.status`, `index.build`, `index.search`, and `index.context` report `disabled`, `pending`, `ready`, or `error`, plus embedded/pending totals. Until vectors are ready—or if the local model disappears—search remains lexical. Vectors are isolated by exact model and embedding schema version; changing either safely requeues chunks. Provider vectors are validated, unit-normalized, and stored in the local SQLite `vec0` plane.

Restart the daemon after changing embedding settings.

## Freshness and bounds

Queries auto-build when no physical index exists. `index.context` also checks tracked source state and refreshes an index whose git HEAD, dirty paths, file hashes, or non-git tree changed. Excluded-only edits do not create refresh loops.

Resource guards bound a build to 50,000 files, 1 MiB per file, 128 overlapping chunks per file, 250,000 chunks per repository, and a 120-second wall guard. Writes are batched and file children are atomically replaced. If the repository-wide chunk cap truncates a file, that file remains marked for a future retry when capacity becomes available.

The repository includes an end-to-end SQLite round trip and a live golden-query regression over MCPlexer's own source. Run:

```bash
go test ./internal/index -run 'TestSQLiteBuildSearchSharedRootRoundTrip|TestRepositorySearchQuality' -count=1 -v
```
