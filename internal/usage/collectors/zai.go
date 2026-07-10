package collectors

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const zaiDefaultBase = "https://api.z.ai"

// ZAICollector fetches quota data from Z.AI (zhipu).
type ZAICollector struct {
	Client httpClient
	Secret SecretReader
}

// Fetch calls the Z.AI usage/quota/limit endpoint. The Authorization
// header is the raw token (no "Bearer " prefix).
func (c *ZAICollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.CollectorResult, error) {
	start := time.Now()
	token, err := c.Secret.Get(ctx, cfg.SecretKey)
	if err != nil {
		return collectorError(store.ProviderZAI, cfg, store.StatusUnconfigured,
			fmt.Sprintf("secret read: %v", err), start), nil
	}
	if token == "" {
		return collectorError(store.ProviderZAI, cfg, store.StatusUnconfigured,
			"no API key configured", start), nil
	}

	base := zaiDefaultBase
	if cfg.BaseURL != "" {
		base = strings.TrimRight(cfg.BaseURL, "/")
	}
	url := base + "/api/monitor/usage/quota/limit"

	body, status, err := c.doFetch(ctx, url, token)
	if err != nil {
		return collectorError(store.ProviderZAI, cfg, store.StatusError,
			fmt.Sprintf("request failed: %v", err), start), nil
	}
	if status != http.StatusOK {
		return collectorError(store.ProviderZAI, cfg, store.StatusError,
			fmt.Sprintf("HTTP %d", status), start), nil
	}

	windows, err := parseZAIResponse(body)
	if err != nil {
		return collectorError(store.ProviderZAI, cfg, store.StatusError,
			fmt.Sprintf("parse: %v", err), start), nil
	}

	snap := baseSnapshot(store.ProviderZAI, cfg, "api")
	snap.Status = store.StatusOK
	snap.UpdatedAt = timePtr(start)
	snap.Windows = windows
	return store.CollectorResult{Snapshot: snap, Duration: time.Since(start)}, nil
}

func (c *ZAICollector) doFetch(
	ctx context.Context, url, token string,
) ([]byte, int, error) {
	req, err := newRawAuthRequest(ctx, url, token)
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

// zaiResponse is the expected response. The shape is a JSON object with
// a "data" field containing an array of quota entries.
type zaiResponse struct {
	Data []zaiQuotaEntry `json:"data"`
}

type zaiQuotaEntry struct {
	Name      string  `json:"name"`
	Used      float64 `json:"used"`
	Total     float64 `json:"total"`
	Remaining float64 `json:"remaining"`
	Unit      string  `json:"unit"`
}

func parseZAIResponse(body []byte) ([]store.UsageWindow, error) {
	var resp zaiResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unmarshal zai: %w", err)
	}
	if len(resp.Data) == 0 {
		return nil, fmt.Errorf("empty data array")
	}
	windows := make([]store.UsageWindow, 0, len(resp.Data))
	for _, q := range resp.Data {
		unit := q.Unit
		if unit == "" {
			unit = store.UnitCredits
		}
		var usedPct float64
		if q.Total > 0 {
			usedPct = (q.Used / q.Total) * 100
		}
		windows = append(windows, store.UsageWindow{
			ID:          "zai_" + strings.ToLower(q.Name),
			Label:       q.Name,
			UsedPercent: usedPct,
			Used:        q.Used,
			Limit:       q.Total,
			Remaining:   q.Remaining,
			Unit:        unit,
		})
	}
	return windows, nil
}
