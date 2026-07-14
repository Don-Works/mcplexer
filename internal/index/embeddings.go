package index

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	codeEmbeddingVersion = 1
	embedBatchSize       = 32
	embedVectorDim       = 1536
	embedMaxAttempts     = 3
)

// Embedder is the deliberately tiny boundary between the code index and a
// vector provider. The daemon only wires a loopback provider after explicit
// code-index opt-in; tests can supply deterministic fakes. It intentionally
// mirrors memory.EmbedProvider without coupling the index package to memory.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) (vectors [][]float32, model string, err error)
	HasModel() bool
}

// NoopEmbedder keeps the lexical source-chunk index fully operational without
// any network call. It is the default for every code index service.
type NoopEmbedder struct{}

func (NoopEmbedder) Embed(context.Context, []string) ([][]float32, string, error) {
	return nil, "", nil
}

func (NoopEmbedder) HasModel() bool { return false }

type embeddingError struct {
	generation uint64
	message    string
}

// ConfigureEmbeddings replaces the optional vector provider. Production only
// passes an explicitly opted-in loopback provider; keeping this method on the
// index package also makes deterministic offline quality tests possible.
func (s *Service) ConfigureEmbeddings(ctx context.Context, emb Embedder, model string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if emb == nil || !emb.HasModel() || strings.TrimSpace(model) == "" {
		emb = NoopEmbedder{}
		model = ""
	}

	s.embedMu.Lock()
	if s.embedCancel != nil {
		s.embedCancel()
	}
	bg, cancel := context.WithCancel(ctx)
	s.embedder = emb
	s.embedModel = strings.TrimSpace(model)
	s.embedContext = bg
	s.embedCancel = cancel
	s.embedGeneration++
	s.embedMu.Unlock()

	s.backfillMu.Lock()
	s.backfillErrors = make(map[string]embeddingError)
	s.backfillMu.Unlock()
}

func (s *Service) embeddingSnapshot() (Embedder, string, context.Context, uint64) {
	s.embedMu.RLock()
	defer s.embedMu.RUnlock()
	return s.embedder, s.embedModel, s.embedContext, s.embedGeneration
}

func (s *Service) embeddingConfigured() bool {
	emb, model, _, _ := s.embeddingSnapshot()
	return emb != nil && emb.HasModel() && model != ""
}

// embeddingStatus reports durable store progress plus the last background
// provider error. It never turns a lexical query into an error.
func (s *Service) embeddingStatus(ctx context.Context, indexID string) EmbeddingStatus {
	emb, model, _, generation := s.embeddingSnapshot()
	if emb == nil || !emb.HasModel() || model == "" {
		return EmbeddingStatus{State: "disabled"}
	}
	status := EmbeddingStatus{Enabled: true, Model: model, State: "pending"}
	pending, total, err := s.store.CountCodeIndexEmbeddingProgress(
		ctx, indexID, model, codeEmbeddingVersion,
	)
	if err != nil {
		status.State = "error"
		status.LastError = err.Error()
		return status
	}
	status.Pending, status.Total, status.Embedded = pending, total, total-pending
	if pending == 0 {
		status.State = "ready"
	}
	s.backfillMu.Lock()
	if last, ok := s.backfillErrors[indexID]; ok && last.generation == generation && last.message != "" {
		status.State = "error"
		status.LastError = last.message
	}
	s.backfillMu.Unlock()
	return status
}

// startEmbeddingBackfill schedules one background worker per physical repo
// index and provider generation. It is safe to call after every build/query;
// duplicate calls collapse without blocking the caller.
func (s *Service) startEmbeddingBackfill(indexID string) {
	emb, model, ctx, generation := s.embeddingSnapshot()
	if emb == nil || !emb.HasModel() || model == "" || strings.TrimSpace(indexID) == "" {
		return
	}
	s.backfillMu.Lock()
	if running, ok := s.backfillRunning[indexID]; ok && running == generation {
		s.backfillMu.Unlock()
		return
	}
	s.backfillRunning[indexID] = generation
	delete(s.backfillErrors, indexID)
	s.backfillMu.Unlock()

	go s.runEmbeddingBackfill(ctx, indexID, emb, model, generation)
}

func (s *Service) runEmbeddingBackfill(
	ctx context.Context, indexID string, emb Embedder, model string, generation uint64,
) {
	defer func() {
		s.backfillMu.Lock()
		if s.backfillRunning[indexID] == generation {
			delete(s.backfillRunning, indexID)
		}
		s.backfillMu.Unlock()
	}()

	for {
		if ctx.Err() != nil || !s.embeddingGenerationCurrent(generation, model) {
			return
		}
		targets, err := s.store.ListCodeIndexChunksNeedingEmbedding(
			ctx, indexID, model, codeEmbeddingVersion, embedBatchSize,
		)
		if err != nil {
			s.recordEmbeddingError(indexID, generation, err)
			return
		}
		if len(targets) == 0 {
			s.recordEmbeddingError(indexID, generation, nil)
			return
		}
		inputs := make([]string, len(targets))
		for i, target := range targets {
			// This is the final privacy boundary before source leaves the index
			// package. A corrupt/legacy row can never smuggle a denied path into
			// an otherwise valid embedding batch.
			if !ShouldIndexPath(target.Path) {
				s.recordEmbeddingError(indexID, generation,
					fmt.Errorf("refusing to embed denied index path %q", target.Path))
				return
			}
			inputs[i] = target.EmbedText
		}

		vectors, returnedModel, err := embedWithRetry(ctx, emb, inputs)
		if err != nil {
			s.recordEmbeddingError(indexID, generation, err)
			return
		}
		if returnedModel != "" && returnedModel != model {
			s.recordEmbeddingError(indexID, generation,
				fmt.Errorf("embedding provider returned model %q, configured %q", returnedModel, model))
			return
		}
		if len(vectors) != len(targets) {
			s.recordEmbeddingError(indexID, generation,
				fmt.Errorf("embedding provider returned %d vectors for %d chunks", len(vectors), len(targets)))
			return
		}
		rows := make([]store.CodeIndexChunkEmbedding, len(targets))
		for i, vector := range vectors {
			normalized, normErr := normalizeCodeVector(vector)
			if normErr != nil {
				s.recordEmbeddingError(indexID, generation, fmt.Errorf("embedding vector %d: %w", i, normErr))
				return
			}
			rows[i] = store.CodeIndexChunkEmbedding{ChunkID: targets[i].ChunkID, Vector: normalized}
		}
		if !s.embeddingGenerationCurrent(generation, model) {
			return
		}
		if err := s.store.UpsertCodeIndexChunkEmbeddings(
			ctx, indexID, model, codeEmbeddingVersion, rows,
		); err != nil {
			s.recordEmbeddingError(indexID, generation, err)
			return
		}
	}
}

// normalizeCodeVector makes sqlite-vec's L2 distance equivalent to cosine
// ordering for providers that do not already unit-normalize their output.
func normalizeCodeVector(vector []float32) ([]float32, error) {
	if len(vector) != embedVectorDim {
		return nil, fmt.Errorf("dimension %d, want %d", len(vector), embedVectorDim)
	}
	var sum float64
	for _, value := range vector {
		f := float64(value)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("contains non-finite values")
		}
		sum += f * f
	}
	if sum == 0 {
		return nil, fmt.Errorf("has zero magnitude")
	}
	scale := float32(1 / math.Sqrt(sum))
	out := make([]float32, len(vector))
	for i, value := range vector {
		out[i] = value * scale
	}
	return out, nil
}

func embedWithRetry(ctx context.Context, emb Embedder, inputs []string) ([][]float32, string, error) {
	var (
		vectors [][]float32
		model   string
		err     error
	)
	for attempt := 0; attempt < embedMaxAttempts; attempt++ {
		vectors, model, err = emb.Embed(ctx, inputs)
		if err == nil {
			return vectors, model, nil
		}
		if attempt+1 == embedMaxAttempts {
			break
		}
		delay := time.Duration(250*(1<<attempt)) * time.Millisecond
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(delay):
		}
	}
	return nil, "", fmt.Errorf("embedding provider failed after %d attempts: %w", embedMaxAttempts, err)
}

func (s *Service) embeddingGenerationCurrent(generation uint64, model string) bool {
	s.embedMu.RLock()
	defer s.embedMu.RUnlock()
	return s.embedGeneration == generation && s.embedModel == model
}

func (s *Service) recordEmbeddingError(indexID string, generation uint64, err error) {
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	if err == nil {
		delete(s.backfillErrors, indexID)
		return
	}
	s.backfillErrors[indexID] = embeddingError{generation: generation, message: err.Error()}
	s.logger.Warn("code-index embedding backfill stopped", "index_id", indexID, "error", err)
}
