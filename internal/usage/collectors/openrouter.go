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

const openRouterKeyURL = "https://openrouter.ai/api/v1/key"

// OpenRouterCollector fetches credit data from the OpenRouter key API.
type OpenRouterCollector struct {
	Client httpClient
	Secret SecretReader
}

// Fetch calls GET /api/v1/key with Bearer auth and returns an
// OpenRouterSnapshot. The token is read from the secret store via
// cfg.SecretKey (a secret:// ref name). Returns partial on HTTP errors
// so the dashboard still renders.
func (c *OpenRouterCollector) Fetch(
	ctx context.Context, cfg store.SourceConfig,
) (store.ORCollectorResult, error) {
	start := time.Now()
	token, err := requireSecret(ctx, c.Secret, cfg.AuthScopeID, cfg.SecretKey)
	if err != nil {
		return orError(store.StatusUnconfigured, err.Error(), start), nil
	}

	url := openRouterKeyURL
	if cfg.BaseURL != "" {
		url = cfg.BaseURL + "/api/v1/key"
	}

	body, status, err := c.doFetch(ctx, url, token)
	if err != nil {
		return orError(store.StatusError, fmt.Sprintf("request failed: %v", err), start), nil
	}

	if status != http.StatusOK {
		return orError(store.StatusError, fmt.Sprintf("HTTP %d", status), start), nil
	}

	credits, err := parseORCredits(body)
	if err != nil {
		return orError(store.StatusError, fmt.Sprintf("parse: %v", err), start), nil
	}

	return store.ORCollectorResult{
		Snapshot: store.OpenRouterSnapshot{
			Status:    store.StatusOK,
			Credits:   credits,
			UpdatedAt: timePtr(start),
		},
		Duration: time.Since(start),
	}, nil
}

func (c *OpenRouterCollector) doFetch(
	ctx context.Context, url, token string,
) ([]byte, int, error) {
	req, err := newBearerRequest(ctx, url, token)
	if err != nil {
		return nil, 0, err
	}
	resp, err := requestClient(c.Client).Do(req)
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

type orKeyResponse struct {
	Data orKeyData `json:"data"`
}

type orKeyData struct {
	Usage          *float64 `json:"usage"`
	Limit          *float64 `json:"limit"`
	LimitRemaining *float64 `json:"limit_remaining"`
	UsageDaily     *float64 `json:"usage_daily"`
	UsageWeekly    *float64 `json:"usage_weekly"`
	UsageMonthly   *float64 `json:"usage_monthly"`
}

func parseORCredits(body []byte) (store.ORCreditInfo, error) {
	var resp orKeyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return store.ORCreditInfo{}, fmt.Errorf("unmarshal openrouter response: %w", err)
	}
	if !hasORCreditData(resp.Data) {
		return store.ORCreditInfo{}, fmt.Errorf("openrouter response contains no key usage fields")
	}
	remaining := resp.Data.LimitRemaining
	if remaining == nil && resp.Data.Limit != nil && resp.Data.Usage != nil {
		value := *resp.Data.Limit - *resp.Data.Usage
		if value < 0 {
			value = 0
		}
		remaining = numberPtr(value)
	}
	return store.ORCreditInfo{
		Usage: resp.Data.Usage, Limit: resp.Data.Limit, Remaining: remaining,
		UsageDaily: resp.Data.UsageDaily, UsageWeekly: resp.Data.UsageWeekly,
		UsageMonthly: resp.Data.UsageMonthly,
	}, nil
}

func hasORCreditData(data orKeyData) bool {
	return data.Usage != nil || data.Limit != nil || data.LimitRemaining != nil ||
		data.UsageDaily != nil || data.UsageWeekly != nil || data.UsageMonthly != nil
}

func orError(status, msg string, start time.Time) store.ORCollectorResult {
	return store.ORCollectorResult{
		Snapshot: store.OpenRouterSnapshot{
			Status: status,
			Error:  msg,
		},
		Duration: time.Since(start),
	}
}
