package control

import (
	"encoding/json"
	"strings"

	"github.com/don-works/mcplexer/internal/gateway"
)

// adminCWDNote is prepended to every admin tool's description so that
// agents and humans browsing a tool catalog see the visibility rule in
// the same place as the tool itself. The note is terse on purpose —
// 40+ admin tools × full sentence would be wasteful in tools/list
// payloads. The gateway's AdminCWDGate is the authoritative enforcer;
// this string is documentation.
const adminCWDNote = "[admin — CWD must be ⊆ ~/.mcplexer or a mcplexer source repo] "

// decorateAdminDescriptions prefixes every tool returned by allTools()
// with adminCWDNote. Every entry here is in the mcplexer__ namespace
// and therefore CWD-gated by gateway.IsAdminTool, so we mark them all
// uniformly rather than maintaining a parallel "which of these is
// admin" set.
func decorateAdminDescriptions(tools []gateway.Tool) []gateway.Tool {
	for i := range tools {
		if strings.HasPrefix(tools[i].Description, adminCWDNote) {
			continue
		}
		tools[i].Description = adminCWDNote + tools[i].Description
	}
	return tools
}

func allTools() []gateway.Tool {
	return decorateAdminDescriptions(append([]gateway.Tool{
		// Server tools
		{
			Name:        "list_servers",
			Description: "List all downstream MCP servers",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "get_server",
			Description: "Get a downstream server by ID",
			InputSchema: schema(props{"id": propStr("Server ID")}, []string{"id"}),
		},
		{
			Name:        "create_server",
			Description: "Create a new downstream MCP server",
			InputSchema: schema(props{
				"name":             propStr("Unique server name"),
				"transport":        propStr("Transport type: stdio"),
				"command":          propStr("Command to run"),
				"args":             propArr("Command arguments"),
				"tool_namespace":   propStr("Tool namespace prefix"),
				"discovery":        propStr("Discovery mode: static or dynamic"),
				"idle_timeout_sec": propInt("Idle timeout in seconds"),
				"max_instances":    propInt("Maximum concurrent instances"),
				"restart_policy":   propStr("Restart policy: never, on-failure, always"),
			}, []string{"name", "command", "tool_namespace"}),
		},
		{
			Name:        "update_server",
			Description: "Update a downstream MCP server (partial update, only provided fields change)",
			InputSchema: schema(props{
				"id":               propStr("Server ID"),
				"name":             propStr("Unique server name"),
				"transport":        propStr("Transport type"),
				"command":          propStr("Command to run"),
				"args":             propArr("Command arguments"),
				"tool_namespace":   propStr("Tool namespace prefix"),
				"discovery":        propStr("Discovery mode"),
				"idle_timeout_sec": propInt("Idle timeout in seconds"),
				"max_instances":    propInt("Maximum concurrent instances"),
				"restart_policy":   propStr("Restart policy"),
			}, []string{"id"}),
		},
		{
			Name:        "delete_server",
			Description: "Delete a downstream MCP server",
			InputSchema: schema(props{"id": propStr("Server ID")}, []string{"id"}),
		},

		// Workspace tools
		{
			Name:        "list_workspaces",
			Description: "List all workspaces",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "get_workspace",
			Description: "Get a workspace by ID",
			InputSchema: schema(props{"id": propStr("Workspace ID")}, []string{"id"}),
		},
		{
			Name:        "create_workspace",
			Description: "Create a new workspace",
			InputSchema: schema(props{
				"name":           propStr("Unique workspace name"),
				"root_path":      propStr("Root file path for the workspace"),
				"default_policy": propStr("Default routing policy: allow or deny"),
				"tags":           propArr("Workspace tags"),
			}, []string{"name"}),
		},
		{
			Name:        "update_workspace",
			Description: "Update a workspace (partial update, only provided fields change)",
			InputSchema: schema(props{
				"id":             propStr("Workspace ID"),
				"name":           propStr("Unique workspace name"),
				"root_path":      propStr("Root file path"),
				"default_policy": propStr("Default routing policy"),
				"tags":           propArr("Workspace tags"),
			}, []string{"id"}),
		},
		{
			Name:        "delete_workspace",
			Description: "Delete a workspace",
			InputSchema: schema(props{"id": propStr("Workspace ID")}, []string{"id"}),
		},

		// Linked-workspace tools (cross-machine task replication).
		{
			Name: "link_workspace",
			Description: "Link a local workspace to a paired peer's workspace so tasks replicate between machines. " +
				"Declares an explicit cross-machine link (e.g. gateway@mac ↔ gateway@vm). After linking, tasks " +
				"created/updated in the local workspace silently replicate to the linked peer. Identity stays local — " +
				"this is an explicit declaration, not derived from path/name.",
			InputSchema: schema(props{
				"peer_id":               propStr("Paired peer's libp2p peer ID"),
				"local_workspace":       propStr("Local workspace id OR name (e.g. \"gateway\")"),
				"remote_workspace_id":   propStr("The peer's workspace id to link to"),
				"remote_workspace_name": propStr("The peer's workspace name (optional, for display + same-name mirror)"),
			}, []string{"peer_id", "local_workspace", "remote_workspace_id"}),
		},
		{
			Name:        "list_workspace_links",
			Description: "List all declared linked workspaces (which local workspace replicates tasks to which peer workspace).",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "unlink_workspace",
			Description: "Remove a workspace link. Stops task replication for the pair; the underlying routing binding is preserved so in-flight offers still resolve.",
			InputSchema: schema(props{
				"peer_id":             propStr("Paired peer's libp2p peer ID"),
				"remote_workspace_id": propStr("The peer's workspace id of the link to remove"),
			}, []string{"peer_id", "remote_workspace_id"}),
		},
		{
			Name:        "suggest_workspace_links",
			Description: "Suggest same-name workspaces across paired peers that are not yet linked. Discovery only — confirm with link_workspace.",
			InputSchema: schema(nil, nil),
		},

		// Route rule tools
		{
			Name:        "list_routes",
			Description: "List route rules for a workspace",
			InputSchema: schema(props{
				"workspace_id": propStr("Workspace ID"),
			}, []string{"workspace_id"}),
		},
		{
			Name:        "create_route",
			Description: "Create a new route rule",
			InputSchema: schema(props{
				"priority":             propInt("Route priority (lower number = higher priority)"),
				"workspace_id":         propStr("Workspace ID"),
				"path_glob":            propStr("Path glob pattern"),
				"tool_match":           propObj("Tool match criteria"),
				"scope_policy":         propObj("Scope policy: resource allowlists, e.g. {\"org\": [\"acme\"], \"repo\": [\"acme/api\"]}"),
				"downstream_server_id": propStr("Downstream server ID"),
				"auth_scope_id":        propStr("Auth scope ID"),
				"policy":               propStr("Policy: allow or deny"),
				"log_level":            propStr("Log level override"),
			}, []string{"workspace_id", "downstream_server_id", "policy"}),
		},
		{
			Name:        "update_route",
			Description: "Update a route rule (partial update, only provided fields change)",
			InputSchema: schema(props{
				"id":                   propStr("Route rule ID"),
				"priority":             propInt("Route priority"),
				"path_glob":            propStr("Path glob pattern"),
				"tool_match":           propObj("Tool match criteria"),
				"scope_policy":         propObj("Scope policy: resource allowlists"),
				"downstream_server_id": propStr("Downstream server ID"),
				"auth_scope_id":        propStr("Auth scope ID"),
				"policy":               propStr("Policy"),
				"log_level":            propStr("Log level override"),
			}, []string{"id"}),
		},
		{
			Name:        "delete_route",
			Description: "Delete a route rule",
			InputSchema: schema(props{"id": propStr("Route rule ID")}, []string{"id"}),
		},

		// Auth scope tools
		{
			Name:        "list_auth_scopes",
			Description: "List all auth scopes",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "get_auth_scope",
			Description: "Get a single auth scope by id",
			InputSchema: schema(props{"id": propStr("Auth scope ID")}, []string{"id"}),
		},
		{
			Name:        "create_auth_scope",
			Description: "Create a new auth scope for downstream authentication",
			InputSchema: schema(props{
				"name": propStr("Unique scope name"),
				"type": propStr("Scope type (e.g. env, header)"),
			}, []string{"name", "type"}),
		},
		{
			Name:        "update_auth_scope",
			Description: "Update an auth scope (partial update; only provided fields change)",
			InputSchema: schema(props{
				"id":                propStr("Auth scope ID"),
				"name":              propStr("New unique scope name"),
				"type":              propStr("New scope type"),
				"oauth_provider_id": propStr("Linked OAuth provider id (oauth2 type only)"),
				"redaction_hints":   propArr("Per-scope redaction key hints"),
			}, []string{"id"}),
		},
		{
			Name:        "delete_auth_scope",
			Description: "Delete an auth scope",
			InputSchema: schema(props{"id": propStr("Auth scope ID")}, []string{"id"}),
		},

		// OAuth provider tools. create_oauth_provider replaces the old
		// REST workaround (POST /api/v1/oauth-providers + update_auth_scope)
		// with a single MCP call that creates the provider and, when
		// link_scope_id is set, links it to an existing auth scope (partial
		// update — other scope fields untouched). client_secret is plaintext
		// and is age-encrypted at rest; it is never logged or returned.
		{
			Name:        "create_oauth_provider",
			Description: "Create an OAuth2 provider and optionally link it to an existing auth scope in one call. client_secret is accepted as plaintext, age-encrypted at rest, and never returned (only has_client_secret). When link_scope_id is set, the target scope's oauth_provider_id is set via a partial update.",
			InputSchema: schema(props{
				"name":          propStr("Unique provider name"),
				"authorize_url": propStr("OAuth2 authorization endpoint URL"),
				"token_url":     propStr("OAuth2 token endpoint URL"),
				"client_id":     propStr("OAuth2 client id"),
				"client_secret": propStr("OAuth2 client secret (plaintext; encrypted at rest, never logged/returned)"),
				"scopes":        propArr("OAuth2 scopes to request"),
				"use_pkce":      map[string]any{"type": "boolean", "description": "Whether to use PKCE"},
				"link_scope_id": propStr("Optional: existing auth scope id to link to this provider (sets its oauth_provider_id)"),
			}, []string{"name"}),
		},

		// Backup tools — full restorable dump of ~/.mcplexer (DB +
		// config + secrets + skills). Designed for the agent workflow: backup → mutate
		// → restore-on-broken. Restore always auto-snapshots first
		// and returns the snapshot ID so callers can roll back.
		{
			Name:        "create_backup",
			Description: "Create a portable tarball backup of the live mcplexer config (SQLite DB + master age key + mcplexer.yaml + api-key + addons + skills + secrets + machine-identity files). Returns the backup id. Machine-identity files (p2p identity, secret-transfer key) are captured BY DEFAULT so the backup is a drop-in replica on a replacement machine. Set include_identity=false only when restoring onto a second machine that will run concurrently with the original (avoids a duplicate peer ID).",
			InputSchema: schema(props{
				"note":             propStr("Optional human-readable note recorded in the manifest"),
				"include_identity": map[string]any{"type": "boolean", "description": "Capture machine-identity files (p2p identity, secret-transfer key). Default true — the backup is a portable replica. Set false only for a second concurrently-live machine (avoids a duplicate peer ID)."},
			}, nil),
		},
		{
			Name:        "list_backups",
			Description: "List backups stored in ~/.mcplexer/backups, newest first.",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "restore_backup",
			Description: "Restore a backup. Auto-takes a pre-restore snapshot first; returns its id so you can roll back. Daemon restart required after.",
			InputSchema: schema(props{
				"id": propStr("Backup id (from list_backups / create_backup)"),
			}, []string{"id"}),
		},
		{
			Name:        "delete_backup",
			Description: "Delete a backup tarball.",
			InputSchema: schema(props{"id": propStr("Backup id")}, []string{"id"}),
		},

		// Skills registry admin tools (CWD-gated to ~/.mcplexer).
		// Universal search/get/publish/list lives at mcpx__skill_*.
		{
			Name:        "list_skill_registry",
			Description: "List skill registry entries. With name: returns version history of that skill. Without: returns the head version of every skill.",
			InputSchema: schema(props{
				"name":            propStr("Optional: filter to one skill's version history"),
				"include_deleted": map[string]any{"type": "boolean", "description": "When listing one skill, include soft-deleted versions"},
				"limit":           propInt("Optional row cap"),
				"view":            propStr("effective (default) or scope_heads (one head per workspace/name pair)"),
			}, nil),
		},
		{
			Name:        "get_skill_registry",
			Description: "Get one skill registry entry by name (and optional version; default = head)",
			InputSchema: schema(props{
				"name":         propStr("Skill name"),
				"version":      propInt("Optional explicit version (default: latest active)"),
				"workspace_id": propStr("Exact workspace ID; omit to preserve effective admin lookup"),
			}, []string{"name"}),
		},
		{
			Name:        "delete_skill_registry",
			Description: "Soft-delete a skill registry entry. version=0 deletes every active version of name.",
			InputSchema: schema(props{
				"name":         propStr("Skill name"),
				"version":      propInt("Version to delete (0 = all active versions)"),
				"workspace_id": propStr("Exact workspace ID; omit for global scope"),
			}, []string{"name"}),
		},
		{
			Name:        "set_skill_registry_tag",
			Description: "Point a tag (e.g. @stable) at a specific version. @latest is derived and cannot be set manually.",
			InputSchema: schema(props{
				"name":    propStr("Skill name"),
				"tag":     propStr("Tag to set (e.g. @stable)"),
				"version": propInt("Target version (must exist and be active)"),
				"set_by":  propStr("Optional identity hint"),
			}, []string{"name", "tag", "version"}),
		},
		{
			Name:        "delete_skill_registry_tag",
			Description: "Remove a (name, tag) pointer from the registry.",
			InputSchema: schema(props{
				"name": propStr("Skill name"),
				"tag":  propStr("Tag to remove"),
			}, []string{"name", "tag"}),
		},
		{
			Name:        "import_skill_registry_dir",
			Description: "Import a skill (or folder of skills) from a local directory into the registry. Three shapes are supported: (1) path = a directory containing SKILL.md → imports as a path-source skill (assets like scripts/ and reference/ stay accessible at source_path); (2) path = a single .md file → inline import; (3) path = a directory of .md files (and optionally nested skill folders) → bulk import. Use this to onboard agentskills.io-format skill packs (e.g. Anthropic's skills repo, ~/.claude/skills/*) without copying.",
			InputSchema: schema(props{
				"path":         propStr("Absolute path to a SKILL.md, a single .md file, or a directory of skills"),
				"author":       propStr("Author label recorded on each row (default: 'import')"),
				"scope":        propStr("'global' (default) or 'workspace' (with workspace_id)"),
				"workspace_id": propStr("Workspace ID — required when scope='workspace'"),
				"recursive":    map[string]any{"type": "boolean", "description": "Recurse into subdirectories when path is a folder of skills"},
			}, []string{"path"}),
		},
		{
			Name:        "import_skill_registry_git",
			Description: "Clone a git repository and import the SKILL.md (or folder of SKILL.md files) at the given subpath into the registry. The clone lives under ~/.mcplexer/skill-registry-git/<hash>/ — re-imports become a fast `git fetch + checkout`. Each row records (git_url, git_ref, git_commit) in metadata under the 'source' key for provenance + future refresh. Auth uses your local git credentials (ssh-agent / system keychain). Use this to onboard remote agentskills.io packs (e.g. anthropics/skills) without manual cloning.",
			InputSchema: schema(props{
				"url":          propStr("Git URL (https or ssh). Required."),
				"ref":          propStr("Branch, tag, or commit to check out (default = repo default branch)"),
				"subpath":      propStr("Path inside the repo to import from (default = repo root). E.g. 'skills' for anthropics/skills."),
				"author":       propStr("Author label (default: 'git:<host/repo>')"),
				"scope":        propStr("'global' (default) or 'workspace' (with workspace_id)"),
				"workspace_id": propStr("Workspace ID — required when scope='workspace'"),
				"recursive":    map[string]any{"type": "boolean", "description": "Recurse into subdirectories when subpath is a folder of skills"},
			}, []string{"url"}),
		},

		// Workers admin tools (M0.5). CWD-gated to ~/.mcplexer like
		// every other mcplexer__* tool. Workers are scheduled in-process
		// AI agents — the gateway exposes this CRUD surface so an admin
		// agent can configure, pause, and dispatch them without raw SQL.
		workerListToolDef(),
		workerGetToolDef(),
		workerCreateToolDef(),
		workerUpdateToolDef(),
		workerDeleteToolDef(),
		workerPauseToolDef(),
		workerResumeToolDef(),
		workerRunNowToolDef(),
		workerListRunsToolDef(),
		workerGetRunToolDef(),
		workerCancelRunToolDef(),
		// M1 — propose-first WorkerApproval surface.
		workerListApprovalsToolDef(),
		workerApproveApprovalToolDef(),
		workerRejectApprovalToolDef(),

		// M3 — publishable Worker templates via the skill registry.
		workerPublishAsTemplateToolDef(),
		workerInstallTemplateToolDef(),
		workerListTemplatesToolDef(),

		// Model profiles — reusable provider+endpoint+secret+known-models
		// bundles a Worker references by id. The known_models list is the
		// delegation candidate pool; this CRUD surface lets an admin agent
		// curate it without raw SQL (parity with the REST + dashboard
		// surface). Builtin rows refuse mutate/delete.
		modelProfileListToolDef(),
		modelProfileGetToolDef(),
		modelProfileCreateToolDef(),
		modelProfileUpdateToolDef(),
		modelProfileSetKnownModelsToolDef(),
		modelProfileDeleteToolDef(),

		// M0.7 — MCP-only admin parity: cost rollup + tool discovery.
		workerCostAggregateToolDef(),
		listAvailableToolsToolDef(),

		// Memory — one-shot warm-start importer for the user's existing
		// Claude Code auto-memory files.
		memoryImportClaudeCliToolDef(),

		// M4 — mesh-triggered workers: per-worker trigger CRUD +
		// per-peer grant convenience.
		listWorkerMeshTriggersToolDef(),
		createWorkerMeshTriggerToolDef(),
		updateWorkerMeshTriggerToolDef(),
		deleteWorkerMeshTriggerToolDef(),
		grantTriggerToPeerToolDef(),
		revokeTriggerGrantToolDef(),

		// Concierge / orchestration — single-call sub-agent dispatch.
		// Creates + runs a one-shot Worker for "heavy lifting" handed off
		// from a router/concierge worker (typically the telegram bot).
		spawnSubagentToolDef(),

		// Brain git backplane (M2) — AUTO local commit, MANUAL push.
		{
			Name:        "brain_push",
			Description: "Sync the MCPlexer Brain repo: git pull --rebase --autostash then push to origin. Manual only (honours deploy-hygiene). Rebase conflicts are surfaced for you to resolve, never auto-resolved.",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "brain_status",
			Description: "Report the MCPlexer Brain git status: ahead/behind/dirty, current branch, last commit.",
			InputSchema: schema(nil, nil),
		},
		// Brain secrets (M3) — one-time, idempotent migration of every
		// auth_scope value + OAuth client secret from the age-DB blob into
		// the SOPS+age value-only-encrypted scopes.enc.yaml (Max's machines
		// only). Round-trip verified; DB blobs are LEFT in place for dual-read.
		{
			Name:        "brain_migrate_secrets",
			Description: "Migrate secrets (auth scope values + OAuth client secrets) from the age-DB store into the Brain's SOPS+age encrypted global/secrets/scopes.enc.yaml. Value-only encryption, round-trip verified, DB blobs left in place for dual-read rollout. Idempotent.",
			InputSchema: schema(nil, nil),
		},
		// Brain migration tooling (M5) — opt-in, parity-verified, reversible.
		{
			Name:        "brain_init",
			Description: "Initialise the MCPlexer Brain repo: take a backup snapshot FIRST (rollback-able), scaffold the repo layout (.gitignore/.gitattributes/brain.json/README + dir skeleton), and git init + initial commit. Idempotent — never clobbers existing files. Run before brain_import.",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "brain_import",
			Description: "One-way, parity-verified import of every Brain-canonical DB row (workspaces, tasks, memories, skills) to its Markdown/YAML file, then reindex + assert row-count + content-hash parity vs the live DB. Aborts (reports drift, leaves the DB authoritative) on any mismatch. Does NOT enable the brain — flip MCPLEXER_BRAIN_ENABLED / settings.brain_enabled only after this reports parity_ok.",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "brain_verify",
			Description: "Re-derive every indexed Brain file's row and diff it against the live DB, reporting drift without mutating anything. Backs the dashboard's drift indicator.",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "brain_disable",
			Description: "Flip settings.brain_enabled=false so the gateway resumes reading the authoritative DB. The brain repo is LEFT on disk as a readable archive — nothing is destroyed. Reversible (re-enable + reindex). Takes effect on the next daemon restart.",
			InputSchema: schema(nil, nil),
		},

		// Info tools
		{
			Name:        "status",
			Description: "Get MCPlexer status with counts of servers, workspaces, sessions, and auth scopes",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "query_audit",
			Description: "Query audit log records with optional filters",
			InputSchema: schema(props{
				"tool_name":            propStr("Filter by tool name"),
				"status":               propStr("Filter by status (success, error, blocked)"),
				"workspace_id":         propStr("Filter by workspace_id — scopes results to one workspace's audit trail"),
				"actor_kind":           propStr("Filter by actor kind (user, worker, scheduler, api, mesh, secrets, worker_admin)"),
				"actor_id":             propStr("Filter by actor id (worker_id, run_id, peer_id, user_id, etc.)"),
				"downstream_server_id": propStr("Filter by downstream server id"),
				"route_rule_id":        propStr("Filter by route rule id"),
				"client_type":          propStr("Filter by client type"),
				"error_code":           propStr("Filter by error code"),
				"tier":                 propStr("Filter by trust tier (same_user, same_org, cross_org)"),
				"q":                    propStr("Free-text full-text search over tool_name, error_message, params, subpath, etc."),
				"sort":                 propStr("Sort order: time_desc (default), time_asc, latency_desc, latency_asc"),
				"limit":                propInt("Max records to return (default 50)"),
				"offset":               propInt("Offset for pagination"),
			}, nil),
		},

		// Identity — read-only views over the M7.1 users + peer_users
		// tables. whoami surfaces the local self row (returns
		// {user: null, self_bootstrapped:false} before first boot so an
		// admin agent can detect the gap). list_users / get_user / list
		// _user_devices project the UserStore for admin dashboards.
		// Mutating peer-ownership flows are deliberately not in v1.
		{
			Name:        "whoami",
			Description: "Return the local self user row. On a fresh install before the boot path runs, returns {user: null, self_bootstrapped: false} so callers can detect the gap and prompt the user to bootstrap.",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "list_users",
			Description: "List every known user (self first, then ordered by display_name). Each user is a per-human identity that may own zero or more paired peers (devices).",
			InputSchema: schema(nil, nil),
		},
		{
			Name:        "get_user",
			Description: "Fetch one user by id. Returns a structured error when the id is unknown.",
			InputSchema: schema(props{
				"id": propStr("User id (user_id column)"),
			}, []string{"id"}),
		},
		{
			Name:        "list_user_devices",
			Description: "List the paired peers (devices) linked to one user. Empty when the user has no paired machines. Returns a structured error when the user id is unknown.",
			InputSchema: schema(props{
				"id": propStr("User id (user_id column)"),
			}, []string{"id"}),
		},
	}, supplementaryToolDefs()...))
}

func supplementaryToolDefs() []gateway.Tool {
	tools := append(monitoringToolDefs(), usageToolDefs()...)
	return append(tools, skillRegistryAdminToolDefs()...)
}

// adminTools is the set of tool names that require admin (read-write) access.
// These are blocked when the control server runs in read-only mode.
var adminTools = map[string]bool{
	"create_server":             true,
	"update_server":             true,
	"delete_server":             true,
	"create_workspace":          true,
	"update_workspace":          true,
	"delete_workspace":          true,
	"create_route":              true,
	"update_route":              true,
	"delete_route":              true,
	"create_auth_scope":         true,
	"update_auth_scope":         true,
	"delete_auth_scope":         true,
	"create_oauth_provider":     true,
	"create_backup":             true,
	"restore_backup":            true,
	"delete_backup":             true,
	"delete_skill_registry":     true,
	"set_skill_registry_tag":    true,
	"delete_skill_registry_tag": true,
	"import_skill_registry_dir": true,
	"import_skill_registry_git": true,
	"publish_skill_registry":    true,
	"create_worker":             true,
	"update_worker":             true,
	"delete_worker":             true,
	// Model profiles — list/get are read-only (visible to read-only admin
	// sessions, like list_workers); the mutators below require admin.
	"create_model_profile":           true,
	"update_model_profile":           true,
	"set_model_profile_known_models": true,
	"delete_model_profile":           true,
	"pause_worker":                   true,
	"resume_worker":                  true,
	"run_worker_now":                 true,
	"cancel_worker_run":              true,
	"approve_worker_approval":        true,
	"reject_worker_approval":         true,
	// M3 — publishable Worker templates. publish + install mutate the
	// registry / worker catalog. list_worker_templates is read-only.
	"publish_worker_as_template": true,
	"install_worker_template":    true,
	// M4 — mesh-trigger admin (read-only list stays non-admin).
	"create_worker_mesh_trigger": true,
	"update_worker_mesh_trigger": true,
	"delete_worker_mesh_trigger": true,
	"grant_trigger_to_peer":      true,
	"revoke_trigger_grant":       true,
	// Concierge orchestration — spawn_subagent creates + runs a worker.
	"spawn_subagent": true,
	// Memory — warm-start importer mutates the memory table.
	"memory_import_claude_cli": true,
	// Linked workspaces — link/unlink mutate state; list/suggest are
	// read-only (visible to read-only admin sessions, like list_workspaces).
	"link_workspace":   true,
	"unlink_workspace": true,
	// Brain git backplane (M2) — brain_push mutates the remote; brain_status
	// is read-only (visible to read-only admin sessions, like status).
	"brain_push": true,
	// Brain secrets (M3) — writes the SOPS file; admin-only.
	"brain_migrate_secrets": true,
	// Brain migration tooling (M5) — init scaffolds + commits, import writes
	// files, disable flips the flag. All mutate; brain_verify is read-only
	// (visible to read-only admin sessions, like status / brain_status).
	"brain_init":    true,
	"brain_import":  true,
	"brain_disable": true,
	// Usage source mutators persist non-secret collector metadata. Reads and
	// refreshes remain read-only, though every tool is still CWD-gated.
	"configure_usage_source": true,
	"remove_usage_source":    true,
	// M0.7 — read-only MCP-parity tools are NOT in adminTools (they're
	// classed alongside list_workers / status — visible to read-only
	// admin sessions). They remain CWD-gated like every mcplexer__*.
}

// isAdminTool returns true if the tool requires admin access.
func isAdminTool(name string) bool {
	return adminTools[name]
}

// Schema helpers for building JSON Schema objects.

type props = map[string]any

func schema(properties map[string]any, required []string) json.RawMessage {
	s := map[string]any{"type": "object"}
	if properties != nil {
		s["properties"] = properties
	}
	if len(required) > 0 {
		s["required"] = required
	}
	data, _ := json.Marshal(s)
	return data
}

func propStr(desc string) map[string]string {
	return map[string]string{"type": "string", "description": desc}
}

func propInt(desc string) map[string]string {
	return map[string]string{"type": "integer", "description": desc}
}

func propArr(desc string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": desc,
		"items":       map[string]string{"type": "string"},
	}
}

func propObj(desc string) map[string]string {
	return map[string]string{"type": "object", "description": desc}
}
