package control

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// handlers_model_profiles.go dispatches the CWD-gated
// mcplexer__*_model_profile tools to admin.Service.ModelProfiles() — the
// same ModelProfileCore the REST handlers use. The two surfaces share one
// validation + Builtin/secret implementation, so they cannot drift.

// modelProfileToolNames enumerates the model-profile admin tools routed
// through the InternalBackend's *admin.Service. Matched in Call() before
// the (store, args) handler map, like the worker tools.
var modelProfileToolNames = map[string]bool{
	"list_model_profiles":            true,
	"get_model_profile":              true,
	"create_model_profile":           true,
	"update_model_profile":           true,
	"set_model_profile_known_models": true,
	"delete_model_profile":           true,
}

// callModelProfile routes one model-profile admin tool to the wired
// *admin.Service. Returns a structured error result when the service or
// its model-profile store isn't available.
func (b *InternalBackend) callModelProfile(
	ctx context.Context, name string, args json.RawMessage,
) json.RawMessage {
	svc := b.workerSvc
	if svc == nil {
		return errorResult(
			"worker admin service not available — daemon built without it",
		)
	}
	core := svc.ModelProfiles()
	if core == nil {
		return errorResult("model profile store not available")
	}
	switch name {
	case "list_model_profiles":
		return handleListModelProfiles(ctx, core)
	case "get_model_profile":
		return handleGetModelProfile(ctx, core, args)
	case "create_model_profile":
		return handleCreateModelProfile(ctx, core, args)
	case "update_model_profile":
		return handleUpdateModelProfile(ctx, core, args)
	case "set_model_profile_known_models":
		return handleSetModelProfileKnownModels(ctx, core, args)
	case "delete_model_profile":
		return handleDeleteModelProfile(ctx, core, args)
	}
	return errorResult("unknown model profile tool: " + name)
}

func handleListModelProfiles(
	ctx context.Context, core *admin.ModelProfileCore,
) json.RawMessage {
	profiles, err := core.List(ctx)
	if err != nil {
		return errorResult(err.Error())
	}
	return mustJSONResult(profiles)
}

func handleGetModelProfile(
	ctx context.Context, core *admin.ModelProfileCore, args json.RawMessage,
) json.RawMessage {
	id, err := requireID(args)
	if err != nil {
		return errorResult(err.Error())
	}
	p, err := core.Get(ctx, id)
	if err != nil {
		return mapModelProfileErr(err)
	}
	return mustJSONResult(p)
}

func handleCreateModelProfile(
	ctx context.Context, core *admin.ModelProfileCore, args json.RawMessage,
) json.RawMessage {
	var in struct {
		Name          string   `json:"name"`
		Provider      string   `json:"provider"`
		EndpointURL   string   `json:"endpoint_url,omitempty"`
		SecretScopeID string   `json:"secret_scope_id,omitempty"`
		KnownModels   []string `json:"known_models,omitempty"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	p := store.ModelProfile{
		Name:          in.Name,
		Provider:      in.Provider,
		EndpointURL:   in.EndpointURL,
		SecretScopeID: in.SecretScopeID,
		KnownModels:   in.KnownModels,
	}
	created, err := core.Create(ctx, &p)
	if err != nil {
		return mapModelProfileErr(err)
	}
	return mustJSONResult(created)
}

func handleUpdateModelProfile(
	ctx context.Context, core *admin.ModelProfileCore, args json.RawMessage,
) json.RawMessage {
	// Decode into pointers so omitted fields stay nil (sparse patch:
	// omit = unchanged). KnownModels uses RawMessage so we can tell
	// "absent" from "present but empty/null".
	var raw struct {
		ID            string           `json:"id"`
		Name          *string          `json:"name"`
		Provider      *string          `json:"provider"`
		EndpointURL   *string          `json:"endpoint_url"`
		SecretScopeID *string          `json:"secret_scope_id"`
		KnownModels   *json.RawMessage `json:"known_models"`
	}
	if err := json.Unmarshal(args, &raw); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	if raw.ID == "" {
		return errorResult("id is required")
	}
	patch := admin.ModelProfilePatch{
		Name:          raw.Name,
		Provider:      raw.Provider,
		EndpointURL:   raw.EndpointURL,
		SecretScopeID: raw.SecretScopeID,
	}
	if raw.KnownModels != nil {
		models, err := decodeKnownModels(*raw.KnownModels)
		if err != nil {
			return errorResult("invalid params: " + err.Error())
		}
		patch.KnownModels = &models
	}
	updated, err := core.Update(ctx, raw.ID, patch)
	if err != nil {
		return mapModelProfileErr(err)
	}
	return mustJSONResult(updated)
}

func handleSetModelProfileKnownModels(
	ctx context.Context, core *admin.ModelProfileCore, args json.RawMessage,
) json.RawMessage {
	var in struct {
		ID          string   `json:"id"`
		KnownModels []string `json:"known_models"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	if in.ID == "" {
		return errorResult("id is required")
	}
	updated, err := core.SetKnownModels(ctx, in.ID, in.KnownModels)
	if err != nil {
		return mapModelProfileErr(err)
	}
	return mustJSONResult(updated)
}

func handleDeleteModelProfile(
	ctx context.Context, core *admin.ModelProfileCore, args json.RawMessage,
) json.RawMessage {
	id, err := requireID(args)
	if err != nil {
		return errorResult(err.Error())
	}
	if err := core.Delete(ctx, id); err != nil {
		return mapModelProfileErr(err)
	}
	return mustJSONResult(map[string]bool{"deleted": true})
}

// decodeKnownModels parses a known_models value that may be null, []
// (clear the list), or a string array. A null is treated as an empty
// list — the patch already signalled "present" by being non-nil.
func decodeKnownModels(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []string{}, nil
	}
	var models []string
	if err := json.Unmarshal(raw, &models); err != nil {
		return nil, err
	}
	if models == nil {
		models = []string{}
	}
	return models, nil
}

// mapModelProfileErr converts the core's typed errors into a readable
// errorResult so the admin agent sees something useful for each case.
func mapModelProfileErr(err error) json.RawMessage {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return errorResult("model profile not found")
	case errors.Is(err, admin.ErrModelProfileBuiltin):
		return errorResult("model profile is builtin and cannot be modified or deleted")
	case errors.Is(err, store.ErrAlreadyExists):
		return errorResult("model profile name already exists")
	}
	// Validation errors and anything else fall through to the raw message.
	return errorResult(err.Error())
}
