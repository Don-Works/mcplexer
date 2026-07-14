-- 133 — Re-key the derived code index by canonical repository root.
--
-- Migration 127 keyed cache rows by logical workspace id. The v2 index uses a
-- private repo-<hash> namespace so multiple authorized/shared workspace ids
-- pointing at the exact same canonical root reuse one FTS/vector corpus.
-- SQLite cannot reproduce Go's absolute-path + symlink canonicalization, so a
-- one-time cache purge is safer than guessing a mapping. No durable user data
-- lives here; the next index query rebuilds incrementally from source.

-- vec0 has no foreign-key cascade, and the base tables intentionally have no
-- FKs, so remove every derived plane explicitly. FTS mirrors are cleaned by
-- their DELETE triggers.
DELETE FROM code_index_chunks_vec;
DELETE FROM code_index_chunks;
DELETE FROM code_index_symbols;
DELETE FROM code_index_edges;
DELETE FROM code_index_files;
DELETE FROM code_index_builds;
