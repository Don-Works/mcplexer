package config

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// SeedDefaultDownstreamServers creates downstream server records if none exist.
// For existing databases, ensures required default servers exist.
//
// External downstream servers (Transport != "internal") are seeded as Disabled.
// The user opts each one in deliberately — connecting an MCP client must not
// trigger 30+ external processes / OAuth flows on first launch. Internal
// builtins (mcpx, mesh, secret, email, telegram) seed enabled.
func SeedDefaultDownstreamServers(ctx context.Context, s store.Store) error {
	existing, err := s.ListDownstreamServers(ctx)
	if err != nil {
		return err
	}

	if len(existing) > 0 {
		return ensureRequiredDefaultServers(ctx, s, existing)
	}

	slog.Info("seeding default downstream servers",
		"count", len(defaultDownstreamServers))

	now := time.Now().UTC()
	for _, d := range defaultDownstreamServers {
		d.CreatedAt = now
		d.UpdatedAt = now
		applyDefaultEnablementPolicy(&d)
		if err := s.CreateDownstreamServer(ctx, &d); err != nil {
			return err
		}
		slog.Info("seeded downstream server",
			"id", d.ID, "name", d.Name, "transport", d.Transport, "disabled", d.Disabled)
	}
	return nil
}

// applyDefaultEnablementPolicy enforces "external servers start disabled"
// at seed time. Internal builtins (in-process) are always enabled. Catalog
// entries can still set Disabled=true explicitly to override; this only
// flips the default for entries that left it false.
func applyDefaultEnablementPolicy(d *store.DownstreamServer) {
	if d.Transport == "internal" {
		return
	}
	d.Disabled = true
}

// ensureRequiredDefaultServers creates critical default servers if missing.
func ensureRequiredDefaultServers(ctx context.Context, s store.Store, existing []store.DownstreamServer) error {
	requiredIDs := []string{
		"mcpx-builtin",
		"mesh-builtin",
		"telegram",
		"lmstudio",
		"mcplexer", // self-CRUD via the InternalBackend (gated by AdminCWDGate)
		"secret-builtin",
		"email-builtin",
		"memory-builtin",
		"task-builtin",
		"skill-builtin",
		"brain-builtin",
		"data-builtin",
		"kv-builtin",
		"index-builtin",
		"monitoring-builtin",
		"notion",
		obsidianServerID,
		aikidoServerID,
		freeagentServerID,
		paddleServerSandboxReadID,
		paddleServerSandboxWriteID,
		paddleServerProdReadID,
		paddleServerProdWriteID,
		"excalidraw",
		hammerspoonServerID,
	}

	existingByID := make(map[string]struct{}, len(existing))
	for _, srv := range existing {
		existingByID[srv.ID] = struct{}{}
	}

	now := time.Now().UTC()
	for _, id := range requiredIDs {
		if _, ok := existingByID[id]; ok {
			continue
		}

		seed, ok := defaultDownstreamServerByID(id)
		if !ok {
			continue
		}
		seed.CreatedAt = now
		seed.UpdatedAt = now
		applyDefaultEnablementPolicy(&seed)

		if err := s.CreateDownstreamServer(ctx, &seed); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				slog.Warn("skipping default downstream server backfill; conflicting row already exists",
					"id", seed.ID, "name", seed.Name)
				continue
			}
			return err
		}
		slog.Info("migrated: seeded default downstream server", "id", seed.ID, "name", seed.Name)
	}
	return nil
}

func defaultDownstreamServerByID(id string) (store.DownstreamServer, bool) {
	for _, srv := range defaultDownstreamServers {
		if srv.ID == id {
			return srv, true
		}
	}
	return store.DownstreamServer{}, false
}
