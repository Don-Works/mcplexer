package install

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// hookReverseData is the JSON payload stamped into InstallReceipt.ReverseData
// for write_file actions; on uninstall we use it as a self-describing audit
// trail of what the install put in place (we restore from BackupPath either
// way, but the JSON makes the reversal intent legible without re-reading
// the backup file).
type hookReverseData struct {
	RemovedMatcher string `json:"removed_matcher"`
	Endpoint       string `json:"endpoint"`
}

// mergeClaudeHooks merges our PreToolUse Bash hook into `cfg` (Claude's
// settings.json shape). Returns (already, updated, err):
//   - already=true when an existing entry references endpoint AND its
//     command exactly equals the expected `command` — idempotent path, no
//     write needed.
//   - already=false when we either appended a fresh entry or rewrote an
//     existing entry whose command drifted (e.g. older $CLAUDE_HOOK_INPUT
//     shape). Drift rewrite is what lets a re-install fix users whose
//     hooks were installed before the stdin-payload fix landed.
//
// `updated` is the (possibly newly mutated) config to write.
func mergeClaudeHooks(
	cfg map[string]any, endpoint, command string,
) (bool, map[string]any, error) {
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}
	preList, _ := hooks["PreToolUse"].([]any)
	for _, entry := range preList {
		if !hookEntryReferences(entry, endpoint) {
			continue
		}
		if hookEntryCommandMatches(entry, command) {
			return true, cfg, nil
		}
		// Drift: same endpoint, different command. Rewrite in place so
		// existing settings.json files get the new hook contract on
		// re-install. Keeps surrounding matcher entries untouched.
		rewriteHookEntryCommand(entry, endpoint, command)
		hooks["PreToolUse"] = preList
		cfg["hooks"] = hooks
		return false, cfg, nil
	}
	newEntry := map[string]any{
		"matcher": claudeMatcher,
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": command,
			},
		},
	}
	preList = append(preList, newEntry)
	hooks["PreToolUse"] = preList
	cfg["hooks"] = hooks
	return false, cfg, nil
}

// mergeClaudeSessionHooks merges our session-lifecycle hooks into `cfg`
// (Claude's settings.json shape) under the session event keys
// (claudeSessionEvents — SessionStart + SessionEnd; NOT Stop, which fires
// per-turn). Unlike PreToolUse these events are not matched against a tool,
// so each entry omits a "matcher" key and is just `{hooks:[{type,command}]}`.
//
// Returns `already` = true only when EVERY session event key already holds
// an entry whose command exactly equals `command`. If any key is missing,
// or holds a drifted command referencing `endpoint`, this mutates `cfg` in
// place (rewrite-in-place on drift, append otherwise) and returns false so
// the caller persists the change. Unrelated entries the user added under
// these keys are preserved.
func mergeClaudeSessionHooks(cfg map[string]any, endpoint, command string) bool {
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = make(map[string]any)
	}
	allPresent := true
	for _, event := range claudeSessionEvents {
		if mergeSessionEvent(hooks, event, endpoint, command) {
			continue
		}
		allPresent = false
	}
	if !allPresent {
		cfg["hooks"] = hooks
	}
	return allPresent
}

// mergeSessionEvent merges our hook into a single session event key's list,
// returning true when an entry with the exact `command` was already present
// (no mutation). On drift (same endpoint, stale command) it rewrites in
// place; when no mcplexer entry exists it appends a fresh one. Either
// mutation returns false. Mirrors mergeClaudeHooks' per-matcher logic but
// for the matcher-less session event shape.
func mergeSessionEvent(hooks map[string]any, event, endpoint, command string) bool {
	list, _ := hooks[event].([]any)
	for _, entry := range list {
		if !hookEntryReferences(entry, endpoint) {
			continue
		}
		if hookEntryCommandMatches(entry, command) {
			return true
		}
		rewriteHookEntryCommand(entry, endpoint, command)
		hooks[event] = list
		return false
	}
	list = append(list, map[string]any{
		"hooks": []any{
			map[string]any{"type": "command", "command": command},
		},
	})
	hooks[event] = list
	return false
}

// hookEntryReferences reports whether an arbitrary PreToolUse matcher entry
// contains a command string that mentions the endpoint URL. Substring match
// is intentional: it tolerates flag reordering, alternative shells, or a
// future mcplexer endpoint path tweak as long as the host:port survives.
func hookEntryReferences(entry any, endpoint string) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	inner, _ := obj["hooks"].([]any)
	for _, h := range inner {
		hookObj, ok := h.(map[string]any)
		if !ok {
			continue
		}
		cmd, _ := hookObj["command"].(string)
		if strings.Contains(cmd, endpoint) {
			return true
		}
	}
	return false
}

// hookEntryCommandMatches reports whether the entry contains a hook whose
// command exactly equals `want`. Used to distinguish the no-op idempotent
// path from a drift-rewrite, so we don't churn settings.json on every
// install when nothing changed.
func hookEntryCommandMatches(entry any, want string) bool {
	obj, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	inner, _ := obj["hooks"].([]any)
	for _, h := range inner {
		hookObj, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hookObj["command"].(string); cmd == want {
			return true
		}
	}
	return false
}

// rewriteHookEntryCommand replaces every command-mentioning-endpoint within
// `entry`'s inner hooks with `command`. Leaves unrelated hook objects
// inside the same matcher entry alone — only the mcplexer-owned ones are
// rewritten.
func rewriteHookEntryCommand(entry any, endpoint, command string) {
	obj, ok := entry.(map[string]any)
	if !ok {
		return
	}
	inner, _ := obj["hooks"].([]any)
	for _, h := range inner {
		hookObj, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, _ := hookObj["command"].(string); strings.Contains(cmd, endpoint) {
			hookObj["command"] = command
		}
	}
}

// readJSONObject reads `path` as a JSON object. Returns (empty-map, false, nil)
// when the file is absent. Errors when the file exists but is malformed or
// its root is not a JSON object — refusing to silently overwrite a corrupt
// or array-rooted file protects user data.
func readJSONObject(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(data) == 0 {
		return map[string]any{}, true, nil
	}
	var obj map[string]any
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, true, fmt.Errorf("parse %s: %w", path, err)
	}
	if obj == nil {
		obj = map[string]any{}
	}
	return obj, true, nil
}

// writeJSONAtomic marshals `cfg` to indented JSON and atomically replaces
// `target` via write-to-tmp + rename. The parent directory is mkdir'd as
// needed. A trailing newline is appended for POSIX-friendliness.
func writeJSONAtomic(target string, cfg map[string]any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, out, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// copyFile copies src -> dst preserving content (not metadata) and sets
// dst's mode to `mode`. Caller is responsible for the dst directory.
func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy to %s: %w", dst, err)
	}
	return out.Close()
}

// newReceiptID returns a short opaque hex string used as the receipt
// primary key. 16 bytes = 128 random bits — plenty for uniqueness over the
// lifetime of one install. We avoid pulling in uuid/ulid for this package
// to keep the dependency surface narrow (already imported elsewhere, but
// the spec asks for stdlib-only in the new file).
func newReceiptID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal in practice; fall back to a time-based
		// id so a single bit of entropy loss doesn't crash an install path.
		return fmt.Sprintf("receipt-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
