package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultHookEndpoint is the local URL the installed curl hook posts to.
// Kept as a package-level constant so callers needn't hard-code it.
const DefaultHookEndpoint = "http://127.0.0.1:3333/v1/hooks/pretool"

// claudeMatcher is the Claude Code "matcher" key our hook attaches to.
// Bash is the canonical shell-execution surface the Shell Guard cares
// about; subsequent matchers (e.g. Edit, Write) are M2+ work.
const claudeMatcher = "Bash"

// HookReceiptStore is the narrow store surface HookInstaller needs.
// Defined here so the package depends only on the methods it actually uses,
// which keeps unit tests cheap (the fake in hooks_test.go implements just
// these five methods, not the full sqlite.DB).
type HookReceiptStore interface {
	UpsertInstalledClient(ctx context.Context, c *store.InstalledClient) error
	GetInstalledClient(ctx context.Context, id string) (*store.InstalledClient, error)
	CreateInstallReceipt(ctx context.Context, r *store.InstallReceipt) error
	ListInstallReceipts(ctx context.Context, clientID string, includeReversed bool) ([]store.InstallReceipt, error)
	MarkReceiptReversed(ctx context.Context, id string, reverseError string) error
}

// HookInstaller writes PreToolUse hook blocks into AI-client configuration
// files and records a Receipt for each mutation so the uninstall path can
// reverse it cleanly. Today only Claude Code is wired; M1-F adds picoclaw.
//
// All file mutations go through writeJSONAtomic so an interrupted install
// leaves either the prior state intact or a recoverable .bak alongside.
type HookInstaller struct {
	home      string           // resolved at construction
	backupDir string           // ~/.mcplexer/backups
	store     HookReceiptStore // narrow interface, defined above
	endpoint  string           // e.g. http://127.0.0.1:3333/v1/hooks/pretool
}

// NewHookInstaller constructs a HookInstaller anchored at `home`. If
// `endpoint` is empty, DefaultHookEndpoint is used. The backup directory
// (~/.mcplexer/backups) is NOT created here — it is created lazily on the
// first write so callers that only ever read state don't side-effect FS.
func NewHookInstaller(home string, s HookReceiptStore, endpoint string) (*HookInstaller, error) {
	if home == "" {
		return nil, errors.New("home directory required")
	}
	if s == nil {
		return nil, errors.New("store required")
	}
	if endpoint == "" {
		endpoint = DefaultHookEndpoint
	}
	return &HookInstaller{
		home:      home,
		backupDir: filepath.Join(home, ".mcplexer", "backups"),
		store:     s,
		endpoint:  endpoint,
	}, nil
}

// claudeSettingsPath returns the absolute path to Claude Code's settings.json
// for this installer's home. NOT the same file as ~/.claude.json, which is
// where MCP server entries live (see clients.go::claudeCodePath).
func (h *HookInstaller) claudeSettingsPath() string {
	return filepath.Join(h.home, ".claude", "settings.json")
}

// hookCommand renders the curl invocation Claude Code will exec for every
// PreToolUse event matching the configured matcher. `-s` silences progress
// output; `--data-binary @-` streams the hook envelope from STDIN, which is
// how Claude Code actually delivers PreToolUse payloads (NOT an env var —
// an earlier version of this command tried `$CLAUDE_HOOK_INPUT`, which is
// never set, so every hook posted an empty body and the gateway 400'd).
// No `-f` flag: curl exiting 0 on a daemon-down 4xx/5xx is intentional, so
// the agent keeps working when mcplexer is unreachable (graceful degrade).
func (h *HookInstaller) hookCommand() string {
	return fmt.Sprintf(
		`curl -s -X POST -H 'Content-Type: application/json' --data-binary @- %s`,
		h.endpoint,
	)
}

// InstallClaudeCodeHooks writes a PreToolUse hook block into
// ~/.claude/settings.json. Idempotent: if a matcher entry already references
// the same endpoint substring, returns (nil, nil) — caller treats nil
// receipt as "already installed". Backs up the prior file to
// ~/.mcplexer/backups/claude_settings-<ts>.json before touching it.
func (h *HookInstaller) InstallClaudeCodeHooks(ctx context.Context) (*store.InstallReceipt, error) {
	target := h.claudeSettingsPath()
	cfg, existed, err := readJSONObject(target)
	if err != nil {
		return nil, fmt.Errorf("read claude settings at %s: %w; Claude Code settings.json appears corrupted, delete it to regenerate or fix the JSON syntax", target, err)
	}

	preAlready, updated, err := mergeClaudeHooks(cfg, h.endpoint, h.hookCommand())
	if err != nil {
		return nil, err
	}
	// Merge the session-lifecycle hooks (SessionStart/SessionEnd/Stop ->
	// /v1/hooks/session) into the SAME config in the same install pass.
	// Only when BOTH the PreToolUse hook and every session hook were
	// already present-and-current do we treat the install as a no-op.
	sessionAlready := mergeClaudeSessionHooks(
		updated, h.sessionEndpoint(), h.sessionHookCommand(),
	)
	if preAlready && sessionAlready {
		return nil, nil
	}

	backupPath := ""
	if existed {
		backupPath, err = h.backupFile(target, "claude_settings")
		if err != nil {
			return nil, fmt.Errorf("backup claude settings: %w", err)
		}
	}

	if err := writeJSONAtomic(target, updated, 0644); err != nil {
		return nil, fmt.Errorf("write claude settings: %w", err)
	}

	receipt, err := h.recordReceipt(ctx, target, backupPath)
	if err != nil {
		return nil, err
	}
	if err := h.markClientHooksInstalled(ctx, target); err != nil {
		return nil, fmt.Errorf("update installed_client: %w", err)
	}
	return receipt, nil
}

// recordReceipt persists a write_file receipt for a successful install.
// Extracted so InstallClaudeCodeHooks stays under the 50-line cap and so
// the test fake can observe the exact receipt that was recorded.
func (h *HookInstaller) recordReceipt(
	ctx context.Context, target, backupPath string,
) (*store.InstallReceipt, error) {
	reverseJSON, err := json.Marshal(hookReverseData{
		RemovedMatcher: claudeMatcher,
		Endpoint:       h.endpoint,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal reverse data: %w", err)
	}
	receipt := &store.InstallReceipt{
		ID:          newReceiptID(),
		ClientID:    string(ClaudeCode),
		Action:      "write_file",
		TargetPath:  target,
		BackupPath:  backupPath,
		ReverseData: string(reverseJSON),
		AppliedAt:   time.Now().UTC(),
	}
	if err := h.store.CreateInstallReceipt(ctx, receipt); err != nil {
		return nil, fmt.Errorf("record receipt: %w", err)
	}
	return receipt, nil
}

// markClientHooksInstalled flips InstalledClient.HooksInstalled=true for
// Claude Code. On a fresh row we set Installed=true too because hooks
// installation implies the client is configured for mcplexer in some form.
func (h *HookInstaller) markClientHooksInstalled(ctx context.Context, configPath string) error {
	now := time.Now().UTC()
	existing, err := h.store.GetInstalledClient(ctx, string(ClaudeCode))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	row := &store.InstalledClient{
		ID:             string(ClaudeCode),
		Name:           "Claude Code",
		ConfigPath:     configPath,
		HooksInstalled: true,
		// Clear any previously-detected drift: this install just wrote
		// the curl shim back into settings.json, so the file once more
		// references our endpoint.
		HooksDrifted: false,
		UpdatedAt:    now,
	}
	if existing != nil {
		row.Installed = existing.Installed
		row.ShimInstalled = existing.ShimInstalled
		row.SandboxEnabled = existing.SandboxEnabled
		row.InstalledAt = existing.InstalledAt
	} else {
		row.InstalledAt = &now
	}
	return h.store.UpsertInstalledClient(ctx, row)
}

// UninstallClaudeCodeHooks consumes the latest Claude-Code write_file
// receipt and restores the prior settings.json from its backup_path. Marks
// the Receipt reversed. Idempotent: no Receipt -> no-op without error.
//
// Failure mode: if restoration fails partway, MarkReceiptReversed is still
// called with the error text so the failure is observable in audit but the
// receipt is NOT retried automatically.
func (h *HookInstaller) UninstallClaudeCodeHooks(ctx context.Context) error {
	receipts, err := h.store.ListInstallReceipts(ctx, string(ClaudeCode), false)
	if err != nil {
		return fmt.Errorf("list receipts: %w", err)
	}
	r := latestWriteFile(receipts, h.claudeSettingsPath())
	if r == nil {
		return nil
	}
	if err := h.reverseWriteFile(*r); err != nil {
		_ = h.store.MarkReceiptReversed(ctx, r.ID, err.Error())
		return err
	}
	return h.store.MarkReceiptReversed(ctx, r.ID, "")
}

// reverseWriteFile is the pure filesystem half of uninstall: either restore
// from the backup (rename so the swap is atomic) or delete the file we
// created. Returns nil if the target already matches the desired post-state.
func (h *HookInstaller) reverseWriteFile(r store.InstallReceipt) error {
	if r.BackupPath == "" {
		if err := os.Remove(r.TargetPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", r.TargetPath, err)
		}
		return nil
	}
	if _, err := os.Stat(r.BackupPath); err != nil {
		return fmt.Errorf("stat backup %s: %w", r.BackupPath, err)
	}
	if err := os.Rename(r.BackupPath, r.TargetPath); err != nil {
		return fmt.Errorf("restore %s: %w", r.TargetPath, err)
	}
	return nil
}

// backupFile copies `src` into ~/.mcplexer/backups/<label>-<RFC3339>.json
// and returns the backup path. The 0700 backup dir is created lazily on
// first call; backup files are written 0600 because they contain whatever
// the user had in their settings (theme prefs, but maybe more).
func (h *HookInstaller) backupFile(src, label string) (string, error) {
	if err := os.MkdirAll(h.backupDir, 0700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	ts := time.Now().UTC().Format("2006-01-02T150405Z")
	dst := filepath.Join(h.backupDir, fmt.Sprintf("%s-%s.json", label, ts))
	if err := copyFile(src, dst, 0600); err != nil {
		return "", err
	}
	return dst, nil
}

// latestWriteFile picks the most-recent un-reversed write_file receipt
// targeting `target`. ListInstallReceipts already orders DESC by applied_at,
// so we just take the first match.
func latestWriteFile(receipts []store.InstallReceipt, target string) *store.InstallReceipt {
	for i := range receipts {
		r := receipts[i]
		if r.Action == "write_file" && r.TargetPath == target && r.ReversedAt == nil {
			return &r
		}
	}
	return nil
}
