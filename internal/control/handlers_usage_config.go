package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
)

func handleConfigureUsageSource(
	ctx context.Context,
	s store.Store,
	args json.RawMessage,
) (json.RawMessage, error) {
	var source config.UsageSourceSettings
	if err := json.Unmarshal(args, &source); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	normalizeUsageSource(&source)
	if err := config.ValidateUsageSources([]config.UsageSourceSettings{source}); err != nil {
		return nil, err
	}

	svc := config.NewSettingsService(s)
	settings := svc.Load(ctx)
	replaced := false
	for i := range settings.UsageSources {
		if sameUsageSource(settings.UsageSources[i], source) {
			settings.UsageSources[i] = source
			replaced = true
			break
		}
	}
	if !replaced {
		settings.UsageSources = append(settings.UsageSources, source)
	}
	if err := svc.Save(ctx, settings); err != nil {
		return nil, fmt.Errorf("save usage source: %w", err)
	}
	return jsonResult(map[string]any{
		"source":   source,
		"replaced": replaced,
	})
}

func handleRemoveUsageSource(
	ctx context.Context,
	s store.Store,
	args json.RawMessage,
) (json.RawMessage, error) {
	var in struct {
		Provider string `json:"provider"`
		Harness  string `json:"harness,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.Harness = strings.TrimSpace(in.Harness)
	probe := config.UsageSourceSettings{Provider: in.Provider, Harness: in.Harness}
	if err := config.ValidateUsageSources([]config.UsageSourceSettings{probe}); err != nil {
		return nil, err
	}

	svc := config.NewSettingsService(s)
	settings := svc.Load(ctx)
	kept := settings.UsageSources[:0]
	removed := 0
	for _, source := range settings.UsageSources {
		if shouldRemoveUsageSource(source, in.Provider, in.Harness) {
			removed++
			continue
		}
		kept = append(kept, source)
	}
	settings.UsageSources = kept
	if err := svc.Save(ctx, settings); err != nil {
		return nil, fmt.Errorf("save usage sources: %w", err)
	}
	return jsonResult(map[string]any{"removed": removed})
}

func normalizeUsageSource(source *config.UsageSourceSettings) {
	source.Provider = strings.ToLower(strings.TrimSpace(source.Provider))
	source.Label = strings.TrimSpace(source.Label)
	source.Plan = strings.TrimSpace(source.Plan)
	source.Harness = strings.TrimSpace(source.Harness)
	source.AuthScopeID = strings.TrimSpace(source.AuthScopeID)
	source.SecretKey = strings.TrimSpace(source.SecretKey)
	source.BaseURL = strings.TrimRight(strings.TrimSpace(source.BaseURL), "/")
	source.Unit = strings.ToLower(strings.TrimSpace(source.Unit))
	source.WindowLabel = strings.TrimSpace(source.WindowLabel)
}

func sameUsageSource(a, b config.UsageSourceSettings) bool {
	return strings.EqualFold(a.Provider, b.Provider)
}

func shouldRemoveUsageSource(source config.UsageSourceSettings, provider, _ string) bool {
	return strings.EqualFold(source.Provider, provider)
}
