package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/config"
)

var usageSnapshotToolNames = map[string]bool{
	"get_usage_dashboard":     true,
	"refresh_usage_dashboard": true,
}

func (b *InternalBackend) callUsageSnapshot(
	ctx context.Context,
	toolName string,
	args json.RawMessage,
) json.RawMessage {
	if b.usageSvc == nil {
		return errorResult("usage service not initialised")
	}
	var in struct {
		Days int `json:"days,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	if in.Days == 0 {
		in.Days = 30
	}
	if in.Days < 1 || in.Days > 365 {
		return errorResult("days must be between 1 and 365")
	}
	settings := config.NewSettingsService(b.store).Load(ctx)
	force := toolName == "refresh_usage_dashboard"
	snapshot, err := b.usageSvc.Snapshot(
		ctx, config.UsageSourceConfigs(settings), in.Days, force,
	)
	if err != nil {
		return errorResult(fmt.Sprintf("usage snapshot: %v", err))
	}
	result, err := jsonResult(snapshot)
	if err != nil {
		return errorResult(err.Error())
	}
	return result
}
