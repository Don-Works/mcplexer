package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const miniMaxDefaultURL = "https://www.minimax.io/v1/token_plan/remains"

// MiniMaxCollector fetches token plan balance from MiniMax.
type MiniMaxCollector struct {
	Client httpClient
	Secret SecretReader
}

// Fetch calls the MiniMax token_plan/remains endpoint.
func (c *MiniMaxCollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()
	token, err := c.Secret.Get(ctx, cfg.SecretKey)
	if err != nil {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusUnconfigured,
			fmt.Sprintf("secret read: %v", err), start), nil
	}
	if token == "" {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusUnconfigured,
			"no API key configured", start), nil
	}

	url := miniMaxDefaultURL
	if cfg.BaseURL != "" {
		url = cfg.BaseURL + "/v1/token_plan/remains"
	}

	body, status, err := c.doFetch(ctx, url, token)
	if err != nil {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusError,
			fmt.Sprintf("request failed: %v", err), start), nil
	}
	if status != http.StatusOK {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusError,
			fmt.Sprintf("HTTP %d", status), start), nil
	}

	window, err := parseMiniMaxResponse(body)
	if err != nil {
		return collectorError(store.ProviderMiniMax, cfg, store.StatusError,
			fmt.Sprintf("parse: %v", err), start), nil
	}

	snap := baseSnapshot(store.ProviderMiniMax, cfg, "api")
	snap.Status = store.StatusOK
	snap.UpdatedAt = timePtr(start)
	snap.Windows = []store.UsageWindow{window}
	return store.CollectorResult{Snapshot: snap, Duration: time.Since(start)}, nil
}

func (c *MiniMaxCollector) doFetch(
	ctx context.Context, url, token string,
) ([]byte, int, error) {
	req, err := newBearerRequest(ctx, url, token)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// miniMaxResponse is the expected response shape.
type miniMaxResponse struct {
	TotalTokensRemain int64 `json:"total_tokens_remain"`
	TotalTokensGrant  int64 `json:"total_tokens_grant"`
}

func parseMiniMaxResponse(body []byte) (store.UsageWindow, error) {
	var resp miniMaxResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return store.UsageWindow{}, fmt.Errorf("unmarshal minimax: %w", err)
	}
	used := float64(resp.TotalTokensGrant - resp.TotalTokensRemain)
	if used < 0 {
		used = 0
	}
	limit := float64(resp.TotalTokensGrant)
	var usedPct float64
	if limit > 0 {
		usedPct = (used / limit) * 100
	}
	return store.UsageWindow{
		ID:          "minimax_tokens",
		Label:       "Token Plan",
		UsedPercent: usedPct,
		Used:        used,
		Limit:       limit,
		Remaining:   float64(resp.TotalTokensRemain),
		Unit:        store.UnitTokens,
	}, nil
}

// baseSnapshot builds a ProviderSnapshot with common fields.
func baseSnapshot(provider string, cfg store.SourceConfig, source string) store.ProviderSnapshot {
	return store.ProviderSnapshot{
		Provider:    provider,
		Label:       cfg.Label,
		Plan:        cfg.Plan,
		Source:      source,
		SourceLabel: cfg.Label,
		Windows:     []store.UsageWindow{},
	}
}

func collectorError(provider string, cfg store.SourceConfig, status, msg string, start time.Time) store.CollectorResult {
	snap := baseSnapshot(provider, cfg, "api")
	snap.Status = status
	snap.Error = msg
	return store.CollectorResult{Snapshot: snap, Duration: time.Since(start)}
}

func timePtr(t time.Time) *time.Time { return &t }
