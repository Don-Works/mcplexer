package config

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

//go:embed catalog_default.json
var defaultCatalogJSON []byte

// CatalogEntry is the wire format for a server catalog entry. It extends
// store.DownstreamServer with frontend presentation fields (description,
// category, tags, auth) so a single JSON document drives both seeding and
// the dashboard UI.
type CatalogEntry struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Description    string          `json:"description,omitempty"`
	Category       string          `json:"category,omitempty"`
	Tags           []string        `json:"tags,omitempty"`
	Auth           string          `json:"auth,omitempty"`
	Transport      string          `json:"transport"`
	Command        string          `json:"command,omitempty"`
	Args           json.RawMessage `json:"args,omitempty"`
	URL            *string         `json:"url,omitempty"`
	ToolNamespace  string          `json:"tool_namespace"`
	Discovery      string          `json:"discovery"`
	IdleTimeoutSec int             `json:"idle_timeout_sec"`
	MaxInstances   int             `json:"max_instances"`
	RestartPolicy  string          `json:"restart_policy"`
	Disabled       bool            `json:"disabled,omitempty"`
}

// CatalogResponse is the JSON envelope returned by GET /api/v1/catalog.
type CatalogResponse struct {
	Entries   []CatalogEntry `json:"entries"`
	Source    string         `json:"source"` // "embedded" or "remote"
	FetchedAt time.Time      `json:"fetched_at"`
}

// CatalogService serves the server catalog, optionally fetching from an
// external URL (MCPLEXER_CATALOG_URL) with a 1-hour cache TTL. Falls back
// to the embedded default catalog when the env var is unset or fetch fails.
type CatalogService struct {
	entries    []CatalogEntry
	source     string
	fetchedAt  time.Time
	mu         sync.RWMutex
	httpClient *http.Client
}

// NewCatalogService creates a catalog service. If MCPLEXER_CATALOG_URL is set,
// it fetches once at startup (falling back to embedded on failure) and caches
// for 1 hour.
func NewCatalogService() *CatalogService {
	svc := &CatalogService{
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}

	embedded := svc.parseEmbedded()
	svc.entries = embedded
	svc.source = "embedded"
	svc.fetchedAt = time.Now()

	catalogURL := os.Getenv("MCPLEXER_CATALOG_URL")
	if catalogURL == "" {
		return svc
	}

	if remote, err := svc.fetchRemote(catalogURL); err != nil {
		slog.Warn("catalog: initial fetch from MCPLEXER_CATALOG_URL failed, using embedded defaults",
			"url", catalogURL, "error", err)
	} else {
		svc.entries = remote
		svc.source = "remote"
		svc.fetchedAt = time.Now()
		slog.Info("catalog: loaded from remote URL", "url", catalogURL, "count", len(remote))
	}

	return svc
}

// Get returns the current catalog entries, refreshing from the remote URL
// if the cache TTL has expired.
func (s *CatalogService) Get() CatalogResponse {
	s.mu.RLock()
	stale := time.Since(s.fetchedAt) > time.Hour
	source := s.source
	entries := s.entries
	fetchedAt := s.fetchedAt
	s.mu.RUnlock()

	if stale && source == "remote" {
		s.mu.Lock()
		// Double-check: another goroutine may have refreshed while we waited.
		if time.Since(s.fetchedAt) > time.Hour {
			catalogURL := os.Getenv("MCPLEXER_CATALOG_URL")
			if catalogURL != "" {
				if remote, err := s.fetchRemote(catalogURL); err != nil {
					slog.Warn("catalog: periodic refresh failed, serving stale", "error", err)
				} else {
					s.entries = remote
					s.source = "remote"
					s.fetchedAt = time.Now()
				}
			}
		}
		entries = s.entries
		source = s.source
		fetchedAt = s.fetchedAt
		s.mu.Unlock()
	}

	return CatalogResponse{
		Entries:   entries,
		Source:    source,
		FetchedAt: fetchedAt,
	}
}

// EntriesForSeeding returns the catalog entries as store.DownstreamServer
// values for use by SeedDefaultDownstreamServers when the catalog is loaded
// from a remote source.
func (s *CatalogService) EntriesForSeeding() []store.DownstreamServer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]store.DownstreamServer, 0, len(s.entries))
	for _, e := range s.entries {
		out = append(out, e.ToDownstreamServer())
	}
	return out
}

// ToDownstreamServer converts a CatalogEntry into the store model used by
// the seeding logic.
func (e CatalogEntry) ToDownstreamServer() store.DownstreamServer {
	return store.DownstreamServer{
		ID:             e.ID,
		Name:           e.Name,
		Transport:      e.Transport,
		Command:        e.Command,
		Args:           e.Args,
		URL:            e.URL,
		ToolNamespace:  e.ToolNamespace,
		Discovery:      e.Discovery,
		IdleTimeoutSec: e.IdleTimeoutSec,
		MaxInstances:   e.MaxInstances,
		RestartPolicy:  e.RestartPolicy,
		Disabled:       e.Disabled,
		Source:         "default",
	}
}

func (s *CatalogService) parseEmbedded() []CatalogEntry {
	var entries []CatalogEntry
	if err := json.Unmarshal(defaultCatalogJSON, &entries); err != nil {
		slog.Error("catalog: failed to parse embedded catalog JSON", "error", err)
		return nil
	}
	return entries
}

func (s *CatalogService) fetchRemote(url string) ([]CatalogEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MiB limit
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}

	var entries []CatalogEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}
	return entries, nil
}
