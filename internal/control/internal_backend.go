package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/backup"
	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// InternalBackend exposes the control-server's full CRUD surface to the main
// gateway via the downstream.InternalBackend interface, so admin tools are
// available alongside the user's normal MCP servers without a second process
// or the read-only default of the standalone control-server.
//
// Tool visibility is enforced separately by the gateway's CWD gate — the
// backend always returns the full tool set, and the gateway's tools/list
// filter hides them when the agent's CWD is not the data directory.
type InternalBackend struct {
	store        store.Store
	backupSvc    *backup.Service           // optional; nil means backup tools error gracefully
	skillReg     *skillregistry.Registry   // optional; nil means skill_registry_import_dir errors
	gitSrc       *skillregistry.GitSource  // optional; nil means import_skill_registry_git errors
	workerSvc    *workersadmin.Service     // optional; nil means worker admin tools error gracefully
	workerTplReg *workertemplates.Registry // optional; nil means list_worker_templates errors
	enc          *secrets.AgeEncryptor     // optional; nil means create_oauth_provider errors when a client_secret is supplied
	brainGit     *brain.Git                // optional; nil means brain_push/brain_status return a structured error
	brainSecrets *brainSecretsConfig       // optional; nil means brain_migrate_secrets returns a structured error
	brainMig     *brainMigrationConfig     // optional; nil means brain_init/import/verify/disable return a structured error
	usageSvc     *usage.Service            // optional; nil means usage snapshot tools error gracefully

	// routeInvalidator is called after any successful route/workspace/server
	// mutation so the routing engine's rules cache picks up the change
	// immediately instead of after the cache TTL. Optional; nil means admin
	// mutations become routable only once the 30s rules cache expires (the
	// REST API handlers invalidate on the same mutation set).
	routeInvalidator func()
}

// routeCacheMutators is the set of control tools whose success must
// invalidate the routing rules cache — mirrors the REST handlers
// (route_handler.go, workspace_handler.go, downstream_handler.go), which
// call Engine.InvalidateAllRoutes on the same mutations.
var routeCacheMutators = map[string]bool{
	"create_route":     true,
	"update_route":     true,
	"delete_route":     true,
	"create_workspace": true,
	"update_workspace": true,
	"delete_workspace": true,
	"create_server":    true,
	"update_server":    true,
	"delete_server":    true,
}

// brainMigrationConfig carries the deps the M5 migration tools
// (brain_init / brain_import / brain_verify / brain_disable) need: the
// brain config (dir + enabled flag), the already-wired serializer +
// indexer (so import writes are byte-identical to live dual-writes), the
// backup service (pre-init snapshot), and a settings store (brain_disable
// flips settings.brain_enabled). All optional fields degrade to a
// structured error when their tool runs without them.
type brainMigrationConfig struct {
	cfg      brain.Config
	ser      *brain.Serializer
	ix       *brain.Indexer
	settings store.SettingsStore
}

// brainSecretsConfig carries the deps brain_migrate_secrets needs that
// don't live on the store/encryptor: the brain repo dir, the age public
// recipients (Max's machines only), and the age key file path used for the
// round-trip verify.
type brainSecretsConfig struct {
	dir        string
	recipients []string
	ageKeyFile string
}

// NewInternalBackend constructs a backend bound to the given store. It always
// runs in full read-write mode; gating happens at the gateway level so the
// security boundary is the agent's CWD, not a server-level toggle.
//
// backupSvc is optional — when nil, the backup tools (create_backup,
// list_backups, restore_backup, delete_backup) return a structured error
// rather than panicking. Live binaries should always pass one.
func NewInternalBackend(s store.Store, backupSvc *backup.Service) *InternalBackend {
	return &InternalBackend{store: s, backupSvc: backupSvc}
}

// SetSkillRegistry wires the registry into the backend so the
// import_dir admin tool can publish via the same code path agents use.
// Optional — if unset the tool returns a structured error.
func (b *InternalBackend) SetSkillRegistry(r *skillregistry.Registry) {
	b.skillReg = r
}

// SetGitSource wires the git clone helper for the
// import_skill_registry_git admin tool. Optional — if unset the tool
// returns a structured error.
func (b *InternalBackend) SetGitSource(g *skillregistry.GitSource) {
	b.gitSrc = g
}

// SetWorkerAdmin wires the Workers admin service (M0.5) so the
// mcplexer__*_worker tools can dispatch validation + persistence and
// (when supplied) fire ad-hoc runs through the runner. Optional — when
// unset, every worker tool returns a structured error rather than
// panicking.
func (b *InternalBackend) SetWorkerAdmin(s *workersadmin.Service) {
	b.workerSvc = s
}

// SetUsageService wires the unified AI subscription dashboard into the
// CWD-gated admin MCP surface.
func (b *InternalBackend) SetUsageService(s *usage.Service) {
	b.usageSvc = s
}

// SetWorkerTemplateRegistry wires the worker_templates registry so the
// list_worker_templates admin tool can read directly from it. Optional —
// when unset, the tool returns a structured error.
func (b *InternalBackend) SetWorkerTemplateRegistry(r *workertemplates.Registry) {
	b.workerTplReg = r
}

// SetBrainGit wires the brain git backplane (M2) so the brain_push /
// brain_status admin tools can pull --rebase --autostash, push, and report
// ahead/behind/dirty. Optional — when unset (brain disabled or git binary
// absent), the tools return a structured error rather than panicking.
func (b *InternalBackend) SetBrainGit(g *brain.Git) {
	b.brainGit = g
}

// SetBrainMigrateSecrets wires the deps for the brain_migrate_secrets admin
// tool (M3): the brain repo dir, the age public recipients (Max's machines
// only — Appendix B #5), and the age key file used for the round-trip
// verify. The store + encryptor already on the backend supply the source
// rows + decryption. Optional — when unset, brain_migrate_secrets returns a
// structured error rather than panicking.
func (b *InternalBackend) SetBrainMigrateSecrets(dir string, recipients []string, ageKeyFile string) {
	b.brainSecrets = &brainSecretsConfig{dir: dir, recipients: recipients, ageKeyFile: ageKeyFile}
}

// SetBrainMigration wires the M5 migration tooling (brain_init /
// brain_import / brain_verify / brain_disable). The serializer + indexer
// MUST be the same instances the live engine uses so import-time writes
// share the hash-CAS + atomic path; settings backs brain_disable's flag
// flip. Optional — when unset, the tools return a structured error.
func (b *InternalBackend) SetBrainMigration(cfg brain.Config, ser *brain.Serializer, ix *brain.Indexer, settings store.SettingsStore) {
	b.brainMig = &brainMigrationConfig{cfg: cfg, ser: ser, ix: ix, settings: settings}
}

// SetRouteInvalidator wires the routing engine's cache invalidation (typically
// Engine.InvalidateAllRoutes) so route/workspace/server mutations made through
// the MCP admin tools take effect immediately, matching the REST API path.
// Optional — when unset, changes become routable after the rules cache TTL.
func (b *InternalBackend) SetRouteInvalidator(f func()) {
	b.routeInvalidator = f
}

// SetEncryptor wires the age encryptor so the create_oauth_provider admin tool
// can seal the OAuth client secret at rest exactly as the REST path does.
// Optional — when unset, create_oauth_provider only errors if a client_secret
// is actually supplied (a provider with no secret still works).
func (b *InternalBackend) SetEncryptor(e *secrets.AgeEncryptor) {
	b.enc = e
}

// ListTools returns every control tool. Tools-list filtering by CWD happens
// in the gateway handler.
func (b *InternalBackend) ListTools(ctx context.Context) (json.RawMessage, error) {
	tools := allTools()
	return json.Marshal(map[string]any{"tools": tools})
}

// Call dispatches to the handler matching the tool name. The gateway has
// already CWD-gated visibility, but defence-in-depth: a non-existent or
// unrecognised tool name still returns a structured error rather than
// panicking.
func (b *InternalBackend) Call(
	ctx context.Context,
	toolName string,
	args json.RawMessage,
) (json.RawMessage, error) {
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	if usageSnapshotToolNames[toolName] {
		return b.callUsageSnapshot(ctx, toolName, args), nil
	}
	// Worker admin tools need the *admin.Service that lives on the
	// InternalBackend, so they short-circuit the (store, args) handler
	// map the same way backup tools do.
	if workerToolNames[toolName] {
		return b.callWorker(ctx, toolName, args), nil
	}
	// Model-profile admin tools share the same *admin.Service the worker
	// tools use (it holds the ModelProfileStore), so they short-circuit
	// the (store, args) handler map the same way.
	if modelProfileToolNames[toolName] {
		return b.callModelProfile(ctx, toolName, args), nil
	}
	// Backup tools need the *backup.Service that lives on InternalBackend,
	// so they don't fit the (store, args) signature of the regular handler
	// map. Dispatch them here before the map lookup.
	switch toolName {
	case "create_backup", "list_backups", "restore_backup", "delete_backup":
		return b.callBackup(ctx, toolName, args), nil
	case "import_skill_registry_dir":
		if b.skillReg == nil {
			return errorResult("skill registry not initialised"), nil
		}
		result, err := handleImportSkillRegistryDir(b.skillReg)(ctx, b.store, args)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return result, nil
	case "import_skill_registry_git":
		if b.skillReg == nil {
			return errorResult("skill registry not initialised"), nil
		}
		if b.gitSrc == nil {
			return errorResult("git source not initialised"), nil
		}
		result, err := handleImportSkillRegistryGit(b.skillReg, b.gitSrc)(ctx, b.store, args)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return result, nil
	case "memory_import_claude_cli":
		return b.handleMemoryImportClaudeCli(ctx, args), nil
	case "brain_push", "brain_status":
		return b.callBrain(ctx, toolName), nil
	case "brain_migrate_secrets":
		return b.handleBrainMigrateSecrets(ctx), nil
	case "brain_init", "brain_import", "brain_verify", "brain_disable":
		return b.callBrainMigration(ctx, toolName), nil
	case "create_oauth_provider":
		// Needs the AgeEncryptor that lives on the backend (to seal the
		// client secret at rest), so it short-circuits the (store, args)
		// handler map like the skill-import tools above.
		result, err := handleCreateOAuthProvider(b.enc)(ctx, b.store, args)
		if err != nil {
			return errorResult(err.Error()), nil
		}
		return result, nil
	}
	handler, ok := handlers[toolName]
	if !ok {
		return errorResult(fmt.Sprintf("unknown control tool: %q", toolName)), nil
	}
	result, err := handler(ctx, b.store, args)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if routeCacheMutators[toolName] && b.routeInvalidator != nil {
		b.routeInvalidator()
	}
	return result, nil
}

// AdminToolNames returns the basename (no namespace) of every admin tool
// that the gateway should hide outside the data directory. Used by the
// gateway's tools/list filter to know which mcplexer__* names to drop.
func AdminToolNames() []string {
	out := make([]string, 0, len(allTools()))
	for _, t := range allTools() {
		out = append(out, t.Name)
	}
	return out
}

// Compile-time check.
var _ interface {
	ListTools(context.Context) (json.RawMessage, error)
	Call(context.Context, string, json.RawMessage) (json.RawMessage, error)
} = (*InternalBackend)(nil)

// We deliberately don't import the downstream package here to avoid an
// import cycle (gateway -> control would otherwise close the loop). The
// control.InternalBackend type matches the downstream.InternalBackend
// interface structurally; serve.go does the registration.
//
// The gateway type alias keeps this comment accurate even if the gateway's
// package imports rearrange.
var _ = gateway.Tool{} // ensure gateway type stays in scope
