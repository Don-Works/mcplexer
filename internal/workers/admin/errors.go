// errors.go — translate raw store-layer errors into user-friendly
// messages. The sqlite driver surfaces FOREIGN KEY constraint failures
// as the opaque string
//
//	"constraint failed: FOREIGN KEY constraint failed (787)"
//
// Today both bad secret_scope_id and bad workspace_id collapse into
// that same message, which is unhelpful for an operator (or an admin
// agent) trying to diagnose the actual misconfiguration. This package
// inspects the worker we tried to write and emits a named error
// pointing at the field that's wrong.
package admin

import (
	"errors"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// translateConstraintError checks whether err looks like a SQLite FK
// failure and, if so, returns a clearer message naming the offending
// field. Callers pass the Worker we were about to persist so we can
// embed the bad value in the message. Returns err unchanged when no
// translation applies (callers should keep using errors.Is for known
// sentinels like store.ErrAlreadyExists).
//
// This is best-effort: SQLite doesn't expose WHICH constraint failed
// without parsing the human string. We prefer to false-negative (return
// the raw error) over false-positive (claim an FK fail when it was a
// uniqueness violation).
func translateConstraintError(err error, w *store.Worker) error {
	if err == nil {
		return nil
	}
	if !looksLikeFKError(err) {
		return err
	}
	switch {
	case strings.TrimSpace(w.SecretScopeID) == "":
		// Empty scope hits a NOT NULL or FK depending on phase — the
		// user-friendly message is still "set it".
		return errors.New("secret_scope_id required")
	case strings.TrimSpace(w.WorkspaceID) == "":
		return errors.New("workspace_id required")
	}
	// Both fields populated → the bad one is whichever FK is missing.
	// We can't tell which without a probe; the workspace error is the
	// more common case (operators rarely guess scope IDs) so call out
	// both and let the caller fix the right one.
	return errors.New(
		"foreign key violation: workspace_id=" + w.WorkspaceID +
			" or secret_scope_id=" + w.SecretScopeID +
			" does not exist",
	)
}

// looksLikeFKError matches the sqlite driver's FK constraint string. We
// match on the substring rather than relying on a typed error so this
// works across modernc.org/sqlite versions.
func looksLikeFKError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "foreign key constraint failed")
}
