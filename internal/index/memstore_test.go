package index

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

// memFile is one file plus its (child-replaced) symbols and edges.
type memFile struct {
	file   store.CodeIndexFile
	syms   []store.CodeIndexSymbol
	edges  []store.CodeIndexEdge
	chunks []store.CodeIndexChunk
}

type memChunkEmbedding struct {
	model   string
	version int
	vector  []float32
}

// memStore is an in-memory store.CodeIndexStore for tests. It honors the frozen
// upsert-replaces-children contract (a re-upsert of a path preserves the file
// id and fully replaces that file's symbols + edges) and implements a simple
// token-overlap search standing in for FTS BM25 (lower score = better).
type memStore struct {
	mu          sync.Mutex
	byKey       map[string]*memFile
	byID        map[int64]*memFile
	builds      map[string]store.CodeIndexBuild
	nextFileID  int64
	nextSymID   int64
	nextChunkID int64
	embeddings  map[int64]memChunkEmbedding
	upsertErr   error
}

func newMemStore() *memStore {
	return &memStore{
		byKey:      map[string]*memFile{},
		byID:       map[int64]*memFile{},
		builds:     map[string]store.CodeIndexBuild{},
		embeddings: map[int64]memChunkEmbedding{},
	}
}

func memKey(ws, path string) string { return ws + "\x00" + path }

var _ store.CodeIndexStore = (*memStore)(nil)

func (m *memStore) UpsertCodeIndexedFiles(ctx context.Context, ws string, files []store.IndexedFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.upsertErr != nil {
		return m.upsertErr
	}
	for _, f := range files {
		k := memKey(ws, f.File.Path)
		mf := m.byKey[k]
		if mf == nil {
			m.nextFileID++
			mf = &memFile{}
			mf.file.ID = m.nextFileID
			m.byKey[k] = mf
			m.byID[mf.file.ID] = mf
		}
		id := mf.file.ID
		mf.file = f.File
		mf.file.ID = id
		mf.file.WorkspaceID = ws
		mf.syms = assignSymbolIDs(m, ws, id, f.Symbols)
		mf.edges = assignEdges(ws, id, f.Edges)
		for _, old := range mf.chunks {
			delete(m.embeddings, old.ID)
		}
		mf.chunks = assignChunkIDs(m, ws, id, f.File.Path, f.Chunks)
	}
	return nil
}

func assignChunkIDs(m *memStore, ws string, fileID int64, path string, in []store.CodeIndexChunk) []store.CodeIndexChunk {
	out := make([]store.CodeIndexChunk, 0, len(in))
	for _, c := range in {
		m.nextChunkID++
		c.ID = m.nextChunkID
		c.FileID = fileID
		c.WorkspaceID = ws
		c.Path = path
		out = append(out, c)
	}
	return out
}

func assignSymbolIDs(m *memStore, ws string, fileID int64, in []store.CodeIndexSymbol) []store.CodeIndexSymbol {
	out := make([]store.CodeIndexSymbol, 0, len(in))
	for _, s := range in {
		m.nextSymID++
		s.ID = m.nextSymID
		s.FileID = fileID
		s.WorkspaceID = ws
		out = append(out, s)
	}
	return out
}

func assignEdges(ws string, fileID int64, in []store.CodeIndexEdge) []store.CodeIndexEdge {
	out := make([]store.CodeIndexEdge, 0, len(in))
	for _, e := range in {
		e.FromFileID = fileID
		e.WorkspaceID = ws
		out = append(out, e)
	}
	return out
}

func (m *memStore) DeleteCodeIndexFiles(ctx context.Context, ws string, paths []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range paths {
		k := memKey(ws, p)
		if mf := m.byKey[k]; mf != nil {
			for _, c := range mf.chunks {
				delete(m.embeddings, c.ID)
			}
			delete(m.byID, mf.file.ID)
			delete(m.byKey, k)
		}
	}
	return nil
}

func (m *memStore) ListCodeIndexFileStats(ctx context.Context, ws string) ([]store.CodeIndexFileStat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.CodeIndexFileStat
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID != ws {
			continue
		}
		out = append(out, store.CodeIndexFileStat{
			Path: mf.file.Path, SizeBytes: mf.file.SizeBytes,
			MtimeUnix: mf.file.MtimeUnix, ContentHash: mf.file.ContentHash,
			ChunkVersion: mf.file.ChunkVersion, ChunkCount: len(mf.chunks),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

func (m *memStore) GetCodeIndexFile(ctx context.Context, ws, path string) (*store.CodeIndexFile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mf := m.byKey[memKey(ws, path)]; mf != nil {
		f := mf.file
		return &f, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) ListCodeIndexSymbolsByPath(ctx context.Context, ws, path string) ([]store.CodeIndexSymbol, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mf := m.byKey[memKey(ws, path)]
	if mf == nil {
		return nil, nil
	}
	out := append([]store.CodeIndexSymbol(nil), mf.syms...)
	sort.Slice(out, func(i, j int) bool { return out[i].StartLine < out[j].StartLine })
	return out, nil
}

func (m *memStore) SearchCodeIndexSymbols(ctx context.Context, q store.CodeIndexSymbolQuery) ([]store.CodeIndexSymbolHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	terms := splitIdent(q.Query)
	var hits []store.CodeIndexSymbolHit
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID != q.WorkspaceID {
			continue
		}
		for _, s := range mf.syms {
			if q.Kind != "" && s.Kind != q.Kind {
				continue
			}
			if q.ExportedOnly && !s.Exported {
				continue
			}
			if n := tokenOverlap(terms, s.NameTokens+" "+strings.ToLower(s.Name)); n > 0 {
				// Real store convention: negated BM25, higher = better.
				hits = append(hits, store.CodeIndexSymbolHit{Symbol: s, Path: mf.file.Path, Score: float64(n)})
			}
		}
	}
	return limitSymbolHits(hits, q.Limit), nil
}

func (m *memStore) SearchCodeIndexFiles(ctx context.Context, ws, query string, limit int) ([]store.CodeIndexFileHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	terms := splitIdent(query)
	var hits []store.CodeIndexFileHit
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID != ws {
			continue
		}
		hay := mf.file.PathTokens + " " + mf.file.Package + " " + strings.ToLower(mf.file.DocSummary)
		if n := tokenOverlap(terms, hay); n > 0 {
			hits = append(hits, store.CodeIndexFileHit{File: mf.file, Score: float64(n)})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

func (m *memStore) SearchCodeIndexChunks(ctx context.Context, q store.CodeIndexChunkQuery) ([]store.CodeIndexChunkHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	terms := splitIdent(q.Query)
	var hits []store.CodeIndexChunkHit
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID != q.WorkspaceID {
			continue
		}
		for _, c := range mf.chunks {
			if q.Kind != "" && c.Kind != q.Kind {
				continue
			}
			hay := c.PathTokens + " " + c.SymbolTokens + " " + c.CodeTokens + " " + strings.ToLower(c.Content)
			if n := tokenOverlap(terms, hay); n > 0 {
				hits = append(hits, store.CodeIndexChunkHit{Chunk: c, Path: c.Path, Score: float64(n), Source: "fts"})
			}
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Chunk.ID < hits[j].Chunk.ID
	})
	if q.Limit > 0 && len(hits) > q.Limit {
		hits = hits[:q.Limit]
	}
	return hits, nil
}

func (m *memStore) VectorSearchCodeIndexChunks(ctx context.Context, ws, model string, version int, vector []float32, k int) ([]store.CodeIndexChunkHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var hits []store.CodeIndexChunkHit
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID != ws {
			continue
		}
		for _, c := range mf.chunks {
			e, ok := m.embeddings[c.ID]
			if !ok || e.model != model || e.version != version {
				continue
			}
			d := vectorDistance(vector, e.vector)
			hits = append(hits, store.CodeIndexChunkHit{Chunk: c, Path: c.Path, Score: d, Source: "vector"})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score < hits[j].Score })
	if k > 0 && len(hits) > k {
		hits = hits[:k]
	}
	return hits, nil
}

func vectorDistance(a, b []float32) float64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var d float64
	for i := 0; i < n; i++ {
		x := float64(a[i] - b[i])
		d += x * x
	}
	return d
}

func (m *memStore) ListCodeIndexChunksNeedingEmbedding(ctx context.Context, ws, model string, version, limit int) ([]store.CodeIndexEmbedTarget, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.CodeIndexEmbedTarget
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID != ws {
			continue
		}
		for _, c := range mf.chunks {
			e, ok := m.embeddings[c.ID]
			if ok && e.model == model && e.version == version {
				continue
			}
			out = append(out, store.CodeIndexEmbedTarget{ChunkID: c.ID, Path: c.Path, EmbedText: c.Path + "\n" + c.SymbolName + "\n" + c.Content})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChunkID < out[j].ChunkID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) CountCodeIndexEmbeddingProgress(ctx context.Context, ws, model string, version int) (int, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, total := 0, 0
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID != ws {
			continue
		}
		for _, c := range mf.chunks {
			total++
			e, ok := m.embeddings[c.ID]
			if !ok || e.model != model || e.version != version {
				pending++
			}
		}
	}
	return pending, total, nil
}

func (m *memStore) UpsertCodeIndexChunkEmbeddings(ctx context.Context, ws, model string, version int, rows []store.CodeIndexChunkEmbedding) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, row := range rows {
		m.embeddings[row.ChunkID] = memChunkEmbedding{model: model, version: version, vector: append([]float32(nil), row.Vector...)}
	}
	return nil
}

func (m *memStore) ListCodeIndexEdges(ctx context.Context, f store.CodeIndexEdgeFilter) ([]store.CodeIndexEdgeHit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.CodeIndexEdgeHit
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID != f.WorkspaceID {
			continue
		}
		for _, e := range mf.edges {
			if f.FromPath != "" && mf.file.Path != f.FromPath {
				continue
			}
			if f.ToPath != "" && e.ToPath != f.ToPath {
				continue
			}
			out = append(out, store.CodeIndexEdgeHit{
				FromPath: mf.file.Path, Kind: e.Kind, ToPath: e.ToPath, ToModule: e.ToModule,
			})
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (m *memStore) PutCodeIndexBuild(ctx context.Context, b *store.CodeIndexBuild) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.builds[b.WorkspaceID] = *b
	return nil
}

func (m *memStore) GetCodeIndexBuild(ctx context.Context, ws string) (*store.CodeIndexBuild, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.builds[ws]; ok {
		bb := b
		return &bb, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) CountCodeIndexSymbols(ctx context.Context, ws string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID == ws {
			n += len(mf.syms)
		}
	}
	return n, nil
}

func (m *memStore) CountCodeIndexChunks(ctx context.Context, ws string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, mf := range m.byKey {
		if mf.file.WorkspaceID == ws {
			n += len(mf.chunks)
		}
	}
	return n, nil
}

// tokenOverlap counts how many query terms appear as tokens in haystack.
func tokenOverlap(terms []string, haystack string) int {
	hay := " " + strings.ToLower(haystack) + " "
	n := 0
	for _, t := range terms {
		if strings.Contains(hay, " "+t+" ") || strings.Contains(hay, t) {
			n++
		}
	}
	return n
}

// limitSymbolHits sorts by score (higher better, real-store convention) and
// truncates.
func limitSymbolHits(hits []store.CodeIndexSymbolHit, limit int) []store.CodeIndexSymbolHit {
	sort.Slice(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
