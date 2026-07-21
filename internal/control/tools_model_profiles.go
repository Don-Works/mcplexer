package control

import "github.com/don-works/mcplexer/internal/gateway"

// tools_model_profiles.go declares the CWD-gated mcplexer__*_model_profile
// admin tools. Model profiles are the reusable (provider + endpoint +
// secret + known-model-list) bundles a Worker references instead of
// carrying those fields inline; their KnownModels list is what surfaces
// as delegation candidates (see list_delegation_model_capacity). This was
// the last config surface with a REST + dashboard surface but no MCP
// admin tool — these close that gap.
//
// Every tool dispatches through admin.Service.ModelProfiles(), the same
// ModelProfileCore the HTTP handlers use, so the Builtin-protection and
// secret-required rules are enforced in exactly one place.

const modelProfileProviderDesc = "Provider: anthropic | openai | openai_compat | claude_cli | opencode_cli | grok_cli | mimo_cli | gemini_cli | codex_cli | pi_cli. secret_scope_id is required for anthropic/openai/openai_compat; CLI providers inherit host credentials. endpoint_url is required for openai_compat."

// modelProfileListToolDef declares mcplexer__list_model_profiles.
func modelProfileListToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "list_model_profiles",
		Description: "List all model profiles (reusable provider+endpoint+secret+known-models bundles a Worker references by id). Each profile's known_models list is the delegation candidate pool surfaced via the delegation capacity view. Returns every row ordered by name; builtin rows (daemon-managed defaults) are flagged and cannot be mutated/deleted.",
		InputSchema: schema(nil, nil),
	}
}

// modelProfileGetToolDef declares mcplexer__get_model_profile.
func modelProfileGetToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "get_model_profile",
		Description: "Get one model profile by id. Returns the full record including provider, endpoint_url, secret_scope_id, known_models, and the builtin flag.",
		InputSchema: schema(props{"id": propStr("Model profile id.")}, []string{"id"}),
	}
}

// modelProfileCreateToolDef declares mcplexer__create_model_profile.
// Required fields mirror admin.validateModelProfile.
func modelProfileCreateToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "create_model_profile",
		Description: "Create a model profile. " + modelProfileProviderDesc + " The created row is always non-builtin (the API/MCP surface cannot mint daemon-managed profiles). Returns the created profile. Conflicts on a duplicate name.",
		InputSchema: schema(props{
			"name":            propStr("Unique profile name (max 80 chars)."),
			"provider":        propStr(modelProfileProviderDesc),
			"endpoint_url":    propStr("Base URL (required for openai_compat; holds the binary path for CLI providers; optional/baked-in for anthropic/openai)."),
			"secret_scope_id": propStr("AuthScope id holding the model API key. Required for anthropic/openai/openai_compat; omit for CLI providers."),
			"known_models":    propArr("Model identifiers this profile can serve. This list is the delegation candidate pool surfaced to agents curating the model mix."),
		}, []string{"name", "provider"}),
	}
}

// modelProfileUpdateToolDef declares mcplexer__update_model_profile.
// Only id is required; every other field is applied iff present (sparse
// patch — omit = unchanged). Refuses builtin rows.
func modelProfileUpdateToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "update_model_profile",
		Description: "Update a model profile (partial — only fields explicitly present are applied; omit a field to leave it unchanged). Refuses to mutate builtin rows. Re-validates the merged result the same way create does (" + modelProfileProviderDesc + ").",
		InputSchema: schema(props{
			"id":              propStr("Model profile id."),
			"name":            propStr("New unique profile name (max 80 chars)."),
			"provider":        propStr(modelProfileProviderDesc),
			"endpoint_url":    propStr("New base URL / binary path."),
			"secret_scope_id": propStr("New AuthScope id (set empty string to clear for CLI providers)."),
			"known_models":    propArr("Replacement known-models list (replaces the whole list, not append). Use set_model_profile_known_models for the curation shorthand."),
		}, []string{"id"}),
	}
}

// modelProfileSetKnownModelsToolDef declares
// mcplexer__set_model_profile_known_models — the convenience path for the
// core use case (curating the delegation pool) so an agent doesn't have
// to read-modify-write the whole profile.
func modelProfileSetKnownModelsToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "set_model_profile_known_models",
		Description: "Replace a model profile's known_models list in one call (curation shorthand — no read-modify-write of the whole profile). The known_models list is the delegation candidate pool. Refuses builtin rows. Returns the updated profile.",
		InputSchema: schema(props{
			"id":           propStr("Model profile id."),
			"known_models": propArr("The complete replacement list of model identifiers this profile serves."),
		}, []string{"id", "known_models"}),
	}
}

// modelProfileDeleteToolDef declares mcplexer__delete_model_profile.
func modelProfileDeleteToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "delete_model_profile",
		Description: "Hard-delete a model profile by id. Refuses builtin rows. Workers referencing the profile have their model_profile_id set to NULL (ON DELETE SET NULL). Returns {\"deleted\": true}.",
		InputSchema: schema(props{"id": propStr("Model profile id.")}, []string{"id"}),
	}
}
