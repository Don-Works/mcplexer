package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// sandboxEnableAction is the Receipt.Action tag for "sandbox was turned
// on for client X". Kept lowercase + underscore-separated to match the
// existing receipt actions ("write_file", "shim_install").
const sandboxEnableAction = "sandbox_enable"

// ReceiptStore is the narrow store surface Installer needs. Mirrors
// the same five-method shape that internal/install.HookReceiptStore uses
// so the same sqlite.DB satisfies both.
type ReceiptStore interface {
	UpsertInstalledClient(ctx context.Context, c *store.InstalledClient) error
	GetInstalledClient(ctx context.Context, id string) (*store.InstalledClient, error)
	CreateInstallReceipt(ctx context.Context, r *store.InstallReceipt) error
	ListInstallReceipts(ctx context.Context, clientID string, includeReversed bool) ([]store.InstallReceipt, error)
	MarkReceiptReversed(ctx context.Context, id string, reverseError string) error
}

// Installer manages sandbox driver installation for a given AI client.
// Today it's a thin wrapper that records an InstallReceipt; a later
// milestone adds the shim binaries that intercept /bin/bash inside the
// sandbox. We split this out from internal/install so the sandbox state
// is owned by the package that knows what "sandbox" means.
type Installer struct {
	home  string
	store ReceiptStore
}

// NewInstaller constructs a sandbox Installer anchored at `home`. Mirror
// of internal/install.NewHookInstaller's shape so wiring in the daemon
// looks symmetric.
func NewInstaller(home string, s ReceiptStore) (*Installer, error) {
	if home == "" {
		return nil, errors.New("home directory required")
	}
	if s == nil {
		return nil, errors.New("store required")
	}
	return &Installer{home: home, store: s}, nil
}

// EnableSandbox records that <clientID> should be launched inside a
// sandbox. Idempotent — calling twice does not duplicate the Receipt.
// Returns the canonical Receipt (the existing one on the no-op path),
// or a freshly created Receipt on first call.
func (i *Installer) EnableSandbox(
	ctx context.Context, clientID string,
) (*store.InstallReceipt, error) {
	if clientID == "" {
		return nil, errors.New("clientID required")
	}
	existing, err := i.activeReceipt(ctx, clientID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	receipt := &store.InstallReceipt{
		ID:         newReceiptID(),
		ClientID:   clientID,
		Action:     sandboxEnableAction,
		TargetPath: clientID, // logical "where" — there's no FS mutation yet
		AppliedAt:  time.Now().UTC(),
	}
	if err := i.store.CreateInstallReceipt(ctx, receipt); err != nil {
		return nil, fmt.Errorf("record sandbox_enable receipt: %w", err)
	}
	if err := i.markClient(ctx, clientID, true); err != nil {
		return nil, fmt.Errorf("update installed_client: %w", err)
	}
	return receipt, nil
}

// DisableSandbox marks the latest sandbox_enable Receipt as reversed.
// Idempotent: if no active Receipt exists, returns nil without error.
func (i *Installer) DisableSandbox(ctx context.Context, clientID string) error {
	if clientID == "" {
		return errors.New("clientID required")
	}
	r, err := i.activeReceipt(ctx, clientID)
	if err != nil {
		return err
	}
	if r == nil {
		return nil
	}
	if err := i.store.MarkReceiptReversed(ctx, r.ID, ""); err != nil {
		return fmt.Errorf("mark reversed: %w", err)
	}
	if err := i.markClient(ctx, clientID, false); err != nil {
		return fmt.Errorf("update installed_client: %w", err)
	}
	return nil
}

// Status reports whether <clientID> currently has sandbox enabled. Source
// of truth is the receipt log: if there's any un-reversed sandbox_enable
// receipt, sandbox is on. The InstalledClient.SandboxEnabled column is a
// derived view used by the dashboard for cheap reads.
func (i *Installer) Status(ctx context.Context, clientID string) (bool, error) {
	r, err := i.activeReceipt(ctx, clientID)
	if err != nil {
		return false, err
	}
	return r != nil, nil
}

// activeReceipt returns the most recent un-reversed sandbox_enable
// Receipt for clientID, or nil if none. List is contracted DESC by
// applied_at, so the first match is "latest".
func (i *Installer) activeReceipt(
	ctx context.Context, clientID string,
) (*store.InstallReceipt, error) {
	receipts, err := i.store.ListInstallReceipts(ctx, clientID, false)
	if err != nil {
		return nil, fmt.Errorf("list receipts: %w", err)
	}
	for idx := range receipts {
		r := receipts[idx]
		if r.Action == sandboxEnableAction && r.ReversedAt == nil {
			return &r, nil
		}
	}
	return nil, nil
}

// markClient flips InstalledClient.SandboxEnabled to enabled. If no row
// exists we create a minimal one — the hooks installer may not have run
// yet for this client.
func (i *Installer) markClient(ctx context.Context, clientID string, enabled bool) error {
	now := time.Now().UTC()
	existing, err := i.store.GetInstalledClient(ctx, clientID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	row := &store.InstalledClient{
		ID:             clientID,
		SandboxEnabled: enabled,
		UpdatedAt:      now,
	}
	if existing != nil {
		row.Name = existing.Name
		row.ConfigPath = existing.ConfigPath
		row.Installed = existing.Installed
		row.HooksInstalled = existing.HooksInstalled
		row.ShimInstalled = existing.ShimInstalled
		row.InstalledAt = existing.InstalledAt
	} else {
		row.Name = clientID
		row.InstalledAt = &now
	}
	return i.store.UpsertInstalledClient(ctx, row)
}

// newReceiptID matches internal/install's helper — 16 random bytes hex.
// Duplicated to keep this package's dependency surface narrow.
func newReceiptID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("receipt-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
