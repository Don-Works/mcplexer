package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/backup"
)

// callBackup dispatches the four backup tools. Lives on InternalBackend
// (rather than the global handlers map) because backup needs access to
// the *backup.Service that the gateway-style handlers don't have.
func (b *InternalBackend) callBackup(ctx context.Context, name string, args json.RawMessage) json.RawMessage {
	if b.backupSvc == nil {
		return errorResult("backup service not available — daemon may have been built without it")
	}
	switch name {
	case "create_backup":
		return b.handleCreateBackup(ctx, args)
	case "list_backups":
		return b.handleListBackups()
	case "restore_backup":
		return b.handleRestoreBackup(ctx, args)
	case "delete_backup":
		return b.handleDeleteBackup(args)
	}
	return errorResult(fmt.Sprintf("unknown backup tool: %q", name))
}

func (b *InternalBackend) handleCreateBackup(ctx context.Context, args json.RawMessage) json.RawMessage {
	var p struct {
		Note            string `json:"note"`
		IncludeIdentity *bool  `json:"include_identity"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return errorResult("invalid params: " + err.Error())
	}
	// Default on: a backup should be a drop-in replica on a replacement
	// machine. Only an explicit include_identity:false opts out (clone case).
	includeIdentity := p.IncludeIdentity == nil || *p.IncludeIdentity
	mf, err := b.backupSvc.Create(ctx, p.Note, includeIdentity)
	if err != nil {
		return errorResult("create backup: " + err.Error())
	}
	res, mErr := jsonResult(mf)
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

func (b *InternalBackend) handleListBackups() json.RawMessage {
	items, err := b.backupSvc.List()
	if err != nil {
		return errorResult("list backups: " + err.Error())
	}
	res, mErr := jsonResult(items)
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

func (b *InternalBackend) handleRestoreBackup(ctx context.Context, args json.RawMessage) json.RawMessage {
	id, err := requireID(args)
	if err != nil {
		return errorResult(err.Error())
	}
	preID, err := b.backupSvc.Restore(ctx, id)
	if err != nil {
		if errors.Is(err, backup.ErrNotFound) {
			return errorResult("backup not found: " + id)
		}
		return errorResult("restore: " + err.Error())
	}
	res, mErr := jsonResult(map[string]any{
		"restored_from":           id,
		"pre_restore_snapshot_id": preID,
		"daemon_restart_required": true,
		"note":                    "Restart the mcplexer daemon to pick up the restored config. If anything is broken, restore_backup with id=" + preID + " rolls back.",
	})
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}

func (b *InternalBackend) handleDeleteBackup(args json.RawMessage) json.RawMessage {
	id, err := requireID(args)
	if err != nil {
		return errorResult(err.Error())
	}
	if err := b.backupSvc.Delete(id); err != nil {
		if errors.Is(err, backup.ErrNotFound) {
			return errorResult("backup not found: " + id)
		}
		return errorResult("delete: " + err.Error())
	}
	res, mErr := jsonResult(map[string]any{"id": id, "deleted": true})
	if mErr != nil {
		return errorResult(mErr.Error())
	}
	return res
}
