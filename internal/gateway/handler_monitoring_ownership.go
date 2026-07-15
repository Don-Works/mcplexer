package gateway

import (
	"context"
	"errors"

	"github.com/don-works/mcplexer/internal/store"
)

// Ownership helpers deliberately return (false, nil) for both a missing row
// and a foreign row. Callers therefore expose one constant-shape not-found
// response without hiding genuine database failures.
func (h *handler) monitoringSourceInWorkspace(
	ctx context.Context, sourceID, workspaceID string,
) (bool, error) {
	source, err := h.store.GetLogSource(ctx, sourceID)
	if errors.Is(err, store.ErrLogSourceNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return source.WorkspaceID == workspaceID, nil
}

func (h *handler) monitoringTemplateInWorkspace(
	ctx context.Context, templateID, workspaceID string,
) (bool, error) {
	template, err := h.store.GetLogTemplate(ctx, templateID)
	if errors.Is(err, store.ErrLogTemplateNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return h.monitoringSourceInWorkspace(ctx, template.SourceID, workspaceID)
}

func (h *handler) monitoringHostInWorkspace(
	ctx context.Context, hostID, workspaceID string,
) (*store.RemoteHost, bool, error) {
	host, err := h.store.GetRemoteHost(ctx, hostID)
	if errors.Is(err, store.ErrRemoteHostNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return host, host.WorkspaceID == workspaceID, nil
}
